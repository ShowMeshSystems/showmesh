package api

import (
	"encoding/json"
	"log/slog"
	"net/http"
	"net/url"
	"regexp"
	"runtime"
	"strconv"
	"sync"
	"sync/atomic"
	"time"

	v1 "github.com/showmeshsystems/showmesh/internal/coordinator/api/v1"
	"github.com/showmeshsystems/showmesh/internal/coordinator/store"
	"github.com/showmeshsystems/showmesh/internal/version"
	"github.com/showmeshsystems/showmesh/pkg/mqttproto"
	"github.com/showmeshsystems/showmesh/pkg/observation"
)

// defaultEventsLimit and maxEventsLimit bound GET /api/v1/events' limit
// parameter. They are literally [store.DefaultEventsPageSize] and
// [store.MaxEventsPageSize], not independently-chosen values of the same
// size: Step 3 review finding 3.3 caught that this package used to declare
// its own maxEventsLimit = 1000 while store.Store.ListEvents silently
// clamped to its own, different MaxEventsPageSize = 500 underneath it, so a
// caller asking for anything in (500, 1000] got told nothing about the
// second, tighter clamp it was actually subject to, and api/openapi.yaml
// documented the wrong (1000) number as the contract. One constant pair —
// store's — is now the only place either bound is chosen; this package
// derives from it instead of restating it, and the two can no longer drift
// apart from each other by construction. api/openapi.yaml's own maximum
// must be corrected to match (see this task's report).
const (
	defaultEventsLimit = store.DefaultEventsPageSize
	maxEventsLimit     = store.MaxEventsPageSize
)

// jsonWrite encodes v as the response body with the standard content type.
// Every success handler in this file uses it, so a change to that content
// type happens in one place.
func jsonWrite(w http.ResponseWriter, v any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(http.StatusOK)
	_ = json.NewEncoder(w).Encode(v)
}

// handlers holds this package's dependencies and options after defaulting,
// and is the receiver for every /api/v1 route handler. It is deliberately
// not exported: [New] is the only supported way to build one, so that
// Options' zero-value defaults ([Options.withDefaults]) are always applied.
type handlers struct {
	deps   Dependencies
	clock  func() time.Time
	logger *slog.Logger

	// closeReads, secureCookie, trustClientAddr, and loginLimiter back
	// ADR-024 (auth.go, session.go, audit.go, loginlimiter.go) — see
	// [Options.CloseReads]/[Options.SecureCookie]/[Options.TrustClientAddr]'s
	// doc comments in api.go for what each controls.
	closeReads      bool
	secureCookie    bool
	trustClientAddr bool
	loginLimiter    *loginLimiter

	// fppCommandConfirmDeadline and fppCommandPollInterval back Step 7
	// seam C's fppcommand_handler.go — see
	// [Options.FPPCommandConfirmDeadline]/[Options.FPPCommandPollInterval]'s
	// doc comments in api.go.
	fppCommandConfirmDeadline time.Duration
	fppCommandPollInterval    time.Duration

	// nightReadinessMaxAge backs Track F seam F2's nightsessioncontrol.go
	// — see [Options.NightReadinessMaxAge]'s doc comment in api.go.
	nightReadinessMaxAge time.Duration

	// discoveryRunInFlight serializes POST /api/v1/discovery/runs
	// (discovery.go's handleStartDiscoveryRun): a second concurrent run is
	// refused with a 409, never queued — see that handler's own doc
	// comment for why interleaving, not merely double-counting, is the
	// failure this guards against. An in-process atomic is sufficient
	// because this coordinator is a single process (ADR-012); it is a
	// struct field (not a package var) because one *handlers is
	// constructed per [New] call and tests build several independent APIs
	// in one process.
	discoveryRunInFlight atomic.Bool

	// nightCueHooks is Track F seam F4's own crash-injection seam for
	// RESTING-MODE.md §7.1.1's commit/dispatch boundary — see
	// [nightCueDispatchHooks]'s own doc comment (nightcuerun.go). Its zero
	// value is a no-op; only a test ever sets it.
	nightCueHooks nightCueDispatchHooks

	// cueActivationFailToBlackWG owns every dispatchAssetMissingFailToBlack
	// goroutine cueActivationTickOne has launched but not yet finished (see
	// that method's own doc comment in cueactivationloop.go for why the
	// dispatch runs off the tick's own critical path in the first place).
	// [CueActivationLoop.Run] Waits on this SAME *handlers' WaitGroup before
	// returning from its own ctx.Done() case, so shutdown cannot close the
	// store while one of these goroutines is still writing to it, and it
	// gives this package's own tests an explicit way to synchronize with a
	// dispatch's real completion instead of inferring it from a side effect
	// (e.g. a dispatched command appearing in a fake publisher) that can
	// still be running well after that side effect is observed.
	cueActivationFailToBlackWG sync.WaitGroup
}

func (h *handlers) now() time.Time { return h.clock() }

// handleServiceDescriptor serves GET /api/v1/.
func (h *handlers) handleServiceDescriptor(w http.ResponseWriter, _ *http.Request) {
	now := h.now()
	jsonWrite(w, v1.ServiceDescriptor{
		ServerTime:        formatTime(now),
		APIVersion:        1,
		SupportedVersions: supportedAPIVersions,
		Coordinator: v1.CoordinatorInfo{
			Version:   version.Version,
			Commit:    version.Commit,
			BuildDate: version.BuildDate,
			GoVersion: goVersion(),
		},
	})
}

// handleNodes serves GET /api/v1/nodes.
func (h *handlers) handleNodes(w http.ResponseWriter, r *http.Request) {
	now := h.now()
	views, err := h.deps.Nodes.Snapshot(r.Context(), now)
	if err != nil {
		h.writeInternalError(w, now, "list nodes", err)
		return
	}
	// BUILD-PLAN Step 7 seam B: fetched once for this whole response, not
	// once per node — see fetchDeclarationContext's own doc comment
	// (discovery.go).
	declByNodeID, latestRun, err := fetchDeclarationContext(r.Context(), h.deps.Discovery)
	if err != nil {
		h.writeInternalError(w, now, "fetch node declarations", err)
		return
	}
	// DEFECT 4: a declared node with no inventory row (never once said
	// hello) must still appear here — see mergeDeclaredOnlyNodes' own doc
	// comment.
	views = mergeDeclaredOnlyNodes(views, declByNodeID)
	nodes := make([]v1.Node, 0, len(views))
	for _, nv := range views {
		render := nodeRenderView(r.Context(), h.deps.Render, h.deps.AssetManifests, nv.NodeID, now)
		nodes = append(nodes, mapNode(nv, now, declPtr(declByNodeID, nv.NodeID), latestRun, render, h.deps.Audio.NodeAudioObservations(nv.NodeID), h.deps.FPPConnectStatus.NodeFPPConnectObservations(nv.NodeID)))
	}
	jsonWrite(w, v1.NodesResponse{ServerTime: formatTime(now), Nodes: nodes})
}

// handleNode serves GET /api/v1/nodes/{nodeId}. The response is wrapped in
// [v1.NodeResponse], not a bare [v1.Node]: contract section 6.2 requires
// serverTime on every response with no exception (an orchestrator
// correction — the CLI builder's independent client caught this endpoint
// shipping without it), and a client computes evidence ages against
// serverTime, so its absence is not a cosmetic gap.
func (h *handlers) handleNode(w http.ResponseWriter, r *http.Request) {
	now := h.now()
	nodeID := r.PathValue("nodeId")
	if err := mqttproto.ValidateNodeID(nodeID); err != nil {
		writeProblem(w, h.logger, now, invalidParameterProblem("nodeId is not a syntactically valid node ID: "+err.Error()))
		return
	}

	views, err := h.deps.Nodes.Snapshot(r.Context(), now)
	if err != nil {
		h.writeInternalError(w, now, "list nodes", err)
		return
	}
	declByNodeID, latestRun, err := fetchDeclarationContext(r.Context(), h.deps.Discovery)
	if err != nil {
		h.writeInternalError(w, now, "fetch node declarations", err)
		return
	}
	// DEFECT 4: see handleNodes' identical call for why.
	views = mergeDeclaredOnlyNodes(views, declByNodeID)
	for _, nv := range views {
		if nv.NodeID == nodeID {
			render := nodeRenderView(r.Context(), h.deps.Render, h.deps.AssetManifests, nv.NodeID, now)
			jsonWrite(w, v1.NodeResponse{ServerTime: formatTime(now), Node: mapNode(nv, now, declPtr(declByNodeID, nv.NodeID), latestRun, render, h.deps.Audio.NodeAudioObservations(nv.NodeID), h.deps.FPPConnectStatus.NodeFPPConnectObservations(nv.NodeID))})
			return
		}
	}
	writeProblem(w, h.logger, now, resourceNotFoundProblem("no node with id "+strconv.Quote(nodeID)+" is in inventory"))
}

// handleFPPList serves GET /api/v1/fpp.
func (h *handlers) handleFPPList(w http.ResponseWriter, r *http.Request) {
	now := h.now()
	views, err := h.deps.FPP.ListInstances(r.Context())
	if err != nil {
		h.writeInternalError(w, now, "list fpp instances", err)
		return
	}
	instances := make([]v1.FPPInstance, 0, len(views))
	for _, fv := range views {
		instances = append(instances, mapFPPInstance(fv, now))
	}
	jsonWrite(w, v1.FPPResponse{ServerTime: formatTime(now), Instances: instances})
}

// handleFPPInstance serves GET /api/v1/fpp/{instanceId}. See
// [handlers.handleNode]'s doc comment for why the response is wrapped in
// [v1.FPPInstanceResponse] rather than a bare [v1.FPPInstance].
func (h *handlers) handleFPPInstance(w http.ResponseWriter, r *http.Request) {
	now := h.now()
	instanceID := r.PathValue("instanceId")
	if err := mqttproto.ValidateNodeID(instanceID); err != nil {
		// FPP instance IDs use the same syntax as node IDs per contract
		// section 7 ("FPP instance IDs use the same syntax as node IDs,
		// validated at config load"); reusing the validator here, not
		// duplicating its regexp, follows that same section's node-ID rule.
		writeProblem(w, h.logger, now, invalidParameterProblem("instanceId is not a syntactically valid instance ID: "+err.Error()))
		return
	}

	views, err := h.deps.FPP.ListInstances(r.Context())
	if err != nil {
		h.writeInternalError(w, now, "list fpp instances", err)
		return
	}
	for _, fv := range views {
		if fv.InstanceID == instanceID {
			jsonWrite(w, v1.FPPInstanceResponse{ServerTime: formatTime(now), Instance: mapFPPInstance(fv, now)})
			return
		}
	}
	writeProblem(w, h.logger, now, resourceNotFoundProblem("no FPP instance with id "+strconv.Quote(instanceID)+" is configured"))
}

// handleObservations serves GET /api/v1/observations, filtered per
// contract section 6.1 by the optional resourceKind, resourceId, and signal
// query parameters.
func (h *handlers) handleObservations(w http.ResponseWriter, r *http.Request) {
	now := h.now()
	filter, problem := parseObservationFilter(r.URL.Query())
	if problem != nil {
		writeProblem(w, h.logger, now, *problem)
		return
	}
	ctx := r.Context()

	// No match is a 200 with an empty list, never a 404: "no observations
	// match this filter" is not "this endpoint does not exist" (an
	// orchestrator correction the CLI builder's independent client
	// flagged). h.deps.Observations.ListObservations returning an empty
	// slice already produces exactly that outcome with no special-casing
	// needed here.
	obs, err := h.deps.Observations.ListObservations(ctx, filter)
	if err != nil {
		h.writeInternalError(w, now, "list observations", err)
		return
	}

	// Union in node evidence (hello/lastWill/heartbeat), synthesized at
	// read time exactly the way [mapNode] builds it for GET /api/v1/nodes
	// — never persisted into the observations table. Without this, this
	// endpoint's own doc comment's claim to list "every observation this
	// coordinator currently holds, across every resource" was false for
	// resourceKind=node specifically: h.deps.Observations only ever holds
	// what the FPP collector sink writes, so a node filter returned an
	// empty list unconditionally while the same coordinator rendered three
	// node signals under node.evidence one request later — ADR-020
	// decision 5's failure mode at endpoint granularity (Step 3 review
	// finding 3.1). See [nodeEvidenceObservations]'s doc comment for why
	// this is a deliberate read-time union and not a second write path.
	if filter.ResourceKind == nil || *filter.ResourceKind == observation.ResourceNode {
		views, err := h.deps.Nodes.Snapshot(ctx, now)
		if err != nil {
			h.writeInternalError(w, now, "list nodes for observations", err)
			return
		}
		for _, nv := range views {
			if filter.ResourceID != nil && *filter.ResourceID != nv.NodeID {
				continue
			}
			for _, o := range nodeEvidenceObservations(nv) {
				if filter.Signal != nil && *filter.Signal != o.Signal {
					continue
				}
				obs = append(obs, o)
			}
		}
		// The store already returns its own rows ordered by (resource
		// kind, resource ID, signal); re-sort after the union so the
		// combined list stays in that same stable order regardless of
		// which source a given entry came from — see sortObservations's
		// doc comment in mapping.go.
		sortObservations(obs)
	}

	// obs may carry more than one collector source's row for the same
	// (resourceKind, resourceId, signal) — see schemaV4's doc comment in
	// internal/coordinator/store/migrations.go — and this endpoint's own
	// doc comment (and its "ordered by resourceKind, then resourceId, then
	// signal" contract) already implies one entry per that triple, exactly
	// like mapFPPInstance's Observations does. ResolveObservations is the
	// same single, documented precedence function that call site uses (see
	// precedence.go); resolving here is what keeps a client of this flat
	// endpoint from ever having to notice, or implement, that precedence
	// rule itself.
	resolved := ResolveObservations(obs)
	sortObservations(resolved)

	entries := make([]v1.ObservationEntry, 0, len(resolved))
	for _, o := range resolved {
		entries = append(entries, mapObservationEntry(o, now))
	}
	jsonWrite(w, v1.ObservationsResponse{ServerTime: formatTime(now), Observations: entries})
}

// parseObservationFilter reads resourceKind, resourceId, and signal from
// query, returning a non-nil problem on a malformed resourceKind (the only
// one of the three with a closed vocabulary to check against;
// resourceId and signal are opaque strings as far as this handler is
// concerned).
func parseObservationFilter(query url.Values) (ObservationFilter, *v1.Problem) {
	var filter ObservationFilter

	if raw := query.Get("resourceKind"); raw != "" {
		kind := observation.ResourceKind(raw)
		switch kind {
		case observation.ResourceNode, observation.ResourceFPP, observation.ResourceCoordinator, observation.ResourceResolume, observation.ResourceSurface, observation.ResourceAudioSession:
			filter.ResourceKind = &kind
		default:
			p := invalidParameterProblem("resourceKind must be one of \"node\", \"fpp\", \"coordinator\", \"resolume\", \"surface\", \"audio_session\", got " + strconv.Quote(raw))
			return ObservationFilter{}, &p
		}
	}
	if raw := query.Get("resourceId"); raw != "" {
		filter.ResourceID = &raw
	}
	if raw := query.Get("signal"); raw != "" {
		sig := observation.SignalID(raw)
		filter.Signal = &sig
	}

	return filter, nil
}

// handleEvents serves GET /api/v1/events.
func (h *handlers) handleEvents(w http.ResponseWriter, r *http.Request) {
	now := h.now()
	since, limit, problem := parseEventsQuery(r.URL.Query())
	if problem != nil {
		writeProblem(w, h.logger, now, *problem)
		return
	}

	ctx := r.Context()

	// gap is a successful, honest answer, never an error: pruning may have
	// removed events between since and what the store still retains, and
	// that describes an incomplete page, not a failure to answer it. See
	// [v1.EventsResponse]'s doc comment — this is an orchestrator contract
	// addition closing a hole the store builder found; a 4xx here would
	// push a client into retrying against history that can never come
	// back.
	records, gap, err := h.deps.Events.ListEvents(ctx, since, limit)
	if err != nil {
		h.writeInternalError(w, now, "list events", err)
		return
	}
	latest, err := h.deps.Events.LatestEventSeq(ctx)
	if err != nil {
		h.writeInternalError(w, now, "read latest event seq", err)
		return
	}
	var oldestRetainedSeq *uint64
	if oldest, ok, err := h.deps.Events.OldestEventSeq(ctx); err != nil {
		h.writeInternalError(w, now, "read oldest event seq", err)
		return
	} else if ok {
		oldestRetainedSeq = &oldest
	}

	events := make([]v1.Event, 0, len(records))
	for _, rec := range records {
		events = append(events, mapEvent(rec))
	}
	jsonWrite(w, v1.EventsResponse{
		ServerTime:        formatTime(now),
		Events:            events,
		LatestSeq:         latest,
		Gap:               gap,
		OldestRetainedSeq: oldestRetainedSeq,
	})
}

// parseEventsQuery reads since (default 0, meaning "from the beginning")
// and limit (default [defaultEventsLimit], capped at [maxEventsLimit]) from
// query, returning a non-nil problem on a malformed or out-of-range value.
func parseEventsQuery(query url.Values) (since uint64, limit int, problem *v1.Problem) {
	limit = defaultEventsLimit

	if raw := query.Get("since"); raw != "" {
		v, err := strconv.ParseUint(raw, 10, 64)
		if err != nil {
			p := invalidParameterProblem("since must be a non-negative integer, got " + strconv.Quote(raw))
			return 0, 0, &p
		}
		since = v
	}

	if raw := query.Get("limit"); raw != "" {
		v, err := strconv.Atoi(raw)
		if err != nil || v <= 0 {
			p := invalidParameterProblem("limit must be a positive integer, got " + strconv.Quote(raw))
			return 0, 0, &p
		}
		if v > maxEventsLimit {
			v = maxEventsLimit
		}
		limit = v
	}

	return since, limit, nil
}

// handleSnapshot serves GET /api/v1/snapshot: the authoritative state the
// SSE stream's deltas are relative to (contract section 6.1). It must
// render every resource the stream can notify about, so a client that
// fetches this and then applies node.changed/fpp.changed/event.recorded
// deltas never has a gap.
func (h *handlers) handleSnapshot(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	now := h.now()

	// latestEventSeq is read FIRST, before the resources it is supposed to
	// be a consistent high-water mark for. Step 3 review finding 3.2: this
	// handler used to read it last, so a node's own last-will could arrive
	// (and get recorded as an events-table transition) in the window
	// between rendering the nodes list and reading latestEventSeq —
	// leaving the snapshot say "online" while latestEventSeq already
	// counted that transition, and a client that then asked
	// GET /api/v1/events?since=latestEventSeq would never see the event
	// describing what the snapshot itself just showed as current. Reading
	// this first instead makes the worst case a harmless replay (a
	// transition that happens between this read and the resource reads
	// below shows up in both the snapshot AND a later events page) rather
	// than a silent hole — exactly the trade [v1.Snapshot]'s own doc
	// comment already promises ("no gap and no duplicate").
	latestSeq, err := h.deps.Events.LatestEventSeq(ctx)
	if err != nil {
		h.writeInternalError(w, now, "read latest event seq", err)
		return
	}

	views, err := h.deps.Nodes.Snapshot(ctx, now)
	if err != nil {
		h.writeInternalError(w, now, "list nodes", err)
		return
	}
	declByNodeID, latestRun, err := fetchDeclarationContext(ctx, h.deps.Discovery)
	if err != nil {
		h.writeInternalError(w, now, "fetch node declarations", err)
		return
	}
	// DEFECT 4: see handleNodes' identical call for why.
	views = mergeDeclaredOnlyNodes(views, declByNodeID)
	nodes := make([]v1.Node, 0, len(views))
	for _, nv := range views {
		render := nodeRenderView(ctx, h.deps.Render, h.deps.AssetManifests, nv.NodeID, now)
		nodes = append(nodes, mapNode(nv, now, declPtr(declByNodeID, nv.NodeID), latestRun, render, h.deps.Audio.NodeAudioObservations(nv.NodeID), h.deps.FPPConnectStatus.NodeFPPConnectObservations(nv.NodeID)))
	}

	fppViews, err := h.deps.FPP.ListInstances(ctx)
	if err != nil {
		h.writeInternalError(w, now, "list fpp instances", err)
		return
	}
	instances := make([]v1.FPPInstance, 0, len(fppViews))
	for _, fv := range fppViews {
		instances = append(instances, mapFPPInstance(fv, now))
	}

	collectorViews, err := h.deps.Collectors.CollectorStatuses(ctx)
	if err != nil {
		h.writeInternalError(w, now, "list collector statuses", err)
		return
	}
	collectors := make([]v1.CollectorStatus, 0, len(collectorViews))
	for _, c := range collectorViews {
		collectors = append(collectors, mapCollectorState(c))
	}

	// Step 9 wave 2: in-flight macro runs, plus a bounded window of
	// recently finished ones (STEP-9-SPEC.md section 6.6) — fatal to omit
	// per ADR-020 decision 3, see [MacroRunner.SnapshotRuns]'s own doc
	// comment (macro_seam.go).
	runViews, err := h.deps.Macros.SnapshotRuns(ctx)
	if err != nil {
		h.writeInternalError(w, now, "snapshot macro runs", err)
		return
	}
	runs := make([]v1.MacroRunSummary, 0, len(runViews))
	for _, run := range runViews {
		runs = append(runs, mapMacroRunSummary(run))
	}

	// Every configured Resolume instance, rendered exactly as GET
	// /resolume/instances renders it — fatal to omit under ADR-020 decision
	// 3, matching MacroRuns above. The composition read is skipped when
	// there are no views and degrades to null on a config-store error
	// rather than failing the whole snapshot — see
	// resolumeCompositionDegradeOnError's own doc comment.
	resolumeViews, err := h.deps.Resolume.ListInstances(ctx)
	if err != nil {
		h.writeInternalError(w, now, "list resolume instances", err)
		return
	}
	var resolumeComposition *v1.ResolumeInstanceComposition
	if len(resolumeViews) > 0 {
		resolumeComposition = resolumeCompositionDegradeOnError(ctx, h.deps.Config, h.logger, "snapshot")
	}
	resolumeInstances := make([]v1.ResolumeInstance, 0, len(resolumeViews))
	for _, rv := range resolumeViews {
		resolumeInstances = append(resolumeInstances, mapResolumeInstance(rv, resolumeComposition, now))
	}

	// Coordinator-wide, computed fresh from the same decode a real push
	// performs — see audioConfigPushStatus's own doc comment. A
	// config-store failure here degrades this one field (state=unknown)
	// rather than failing the whole snapshot, matching
	// resolumeCompositionDegradeOnError's own precedent two calls above.
	pushState, pushReason := audioConfigPushStatusDegradeOnError(ctx, h.deps.Config, h.logger, "snapshot")

	// Computed fresh via a real probe write to audit_log (always rolled
	// back), never cached: see [identity.Service.AuditWriteStatus]'s own
	// doc comment for why a stale, traffic-fed latch alone was not
	// answerable enough for this standing signal.
	auditState, auditReasonStr := h.deps.Identity.AuditWriteStatus(ctx)
	var auditReason *string
	if auditReasonStr != "" {
		auditReason = &auditReasonStr
	}

	jsonWrite(w, v1.Snapshot{
		ServerTime:     formatTime(now),
		LatestEventSeq: latestSeq,
		Nodes:          nodes,
		FPP:            v1.FPPSection{Instances: instances},
		Collectors:     collectors,
		MacroRuns:      runs,
		Resolume:       resolumeInstances,
		AuditStore: v1.AuditStoreStatus{
			State: auditState, Reason: auditReason,
		},
		AudioConfigPush: v1.AudioConfigPushStatus{
			State: string(pushState), Reason: pushReason,
		},
	})
}

// writeInternalError logs err (never sent to the client — an internal
// dependency failure detail is not this API's to disclose) and writes a
// generic 500 problem. None of the four problem classes contract section
// 6.6 names for Step 3 fit "the store failed to answer a query", so this
// uses a fifth, minimal type rather than misusing e.g.
// resource-not-found for a condition that has nothing to do with whether
// the resource exists.
func (h *handlers) writeInternalError(w http.ResponseWriter, now time.Time, action string, err error) {
	if h.logger != nil {
		h.logger.Error("api: internal error", "action", action, "error", err)
	}
	writeProblem(w, h.logger, now, v1.Problem{
		Type:   problemBaseURI + "internal-error",
		Title:  "Internal error",
		Status: http.StatusInternalServerError,
		Detail: "an internal error occurred; see the coordinator's own logs for detail",
	})
}

// apiPathVersion extracts the version segment from a path of the shape
// "/api/vN..." or "/api/vN/...". ok is false for a path that does not even
// look like a versioned API path (e.g. "/api/", "/api/bogus") — see
// [handleUnknownAPIPath].
var apiPathVersionPattern = regexp.MustCompile(`^/api/v(\d+)(?:/|$)`)

func apiPathVersion(path string) (version string, ok bool) {
	m := apiPathVersionPattern.FindStringSubmatch(path)
	if m == nil {
		return "", false
	}
	return m[1], true
}

// handleUnknownAPIPath is the catch-all for any request under /api/ that
// did not match one of the specific routes registered in [newMux]. Per
// contract section 6.6 and Task D's spec section 6 ("an unknown path
// version... produces the same class of explicit error, not a bare 404
// page"):
//
//   - a path naming a version this coordinator does not serve (/api/v2/...,
//     /api/v0/...) gets the same 400 unsupported-api-version problem the
//     request-header check produces;
//   - a path naming version 1 that nonetheless matched no specific route
//     (a typo'd endpoint under /api/v1/) is a genuine 404
//     resource-not-found;
//   - anything else under /api/ that does not even look like a versioned
//     path (/api/, /api/bogus) is also a 404 resource-not-found: it is not
//     a version problem, there is simply no such resource.
func handleUnknownAPIPath(logger *slog.Logger, clock func() time.Time) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		now := clock()
		w.Header().Set(apiVersionHeaderName, servedAPIVersion)
		v, ok := apiPathVersion(r.URL.Path)
		if ok && v != servedAPIVersion {
			writeProblem(w, logger, now, unsupportedAPIVersionProblem(
				"this coordinator serves API version 1; path "+strconv.Quote(r.URL.Path)+" names a version it does not serve"))
			return
		}
		writeProblem(w, logger, now, resourceNotFoundProblem("no route matches "+strconv.Quote(r.URL.Path)))
	}
}

// goVersion reports the Go runtime version, matching what /version already
// reports (see internal/coordinator/httpapi.handleVersion). A package-level
// var, not a plain function, so a golden-file test can substitute a fixed
// value: runtime.Version() legitimately differs between the toolchain that
// happened to build this test binary and any other, which would otherwise
// make TestGoldenServiceDescriptor fail purely from a Go upgrade with no
// change to this package's own behavior.
var goVersion = func() string { return runtime.Version() }
