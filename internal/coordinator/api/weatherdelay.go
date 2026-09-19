package api

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"sync"
	"time"

	"github.com/google/uuid"

	v1 "github.com/showmeshsystems/showmesh/internal/coordinator/api/v1"
	"github.com/showmeshsystems/showmesh/internal/coordinator/broker"
	"github.com/showmeshsystems/showmesh/internal/coordinator/config"
	"github.com/showmeshsystems/showmesh/internal/coordinator/identity"
	"github.com/showmeshsystems/showmesh/internal/coordinator/store"
	"github.com/showmeshsystems/showmesh/pkg/command"
	"github.com/showmeshsystems/showmesh/pkg/mqttproto"
	"github.com/showmeshsystems/showmesh/pkg/observation"
	"github.com/showmeshsystems/showmesh/pkg/weatherdelay"
)

// ADR-053 weather delay start and resume. Cancel-night is not built yet.

var (
	scopeShowWeatherDelayInvoke = identity.ScopeShowWeatherDelayInvoke
	scopeShowWeatherDelayResume = identity.ScopeShowWeatherDelayResume
)

const maxWeatherDelayActionRequestBodyBytes = 1024

// weatherDelayNodeCommandConfirmDeadline bounds how long the
// weatherdelay.start/resume node command waits for evidence on its
// result topic, mirroring audioCommandConfirmDeadline's identical role
// one file over. Each node's own wait runs in its own goroutine
// (dispatchWeatherDelayNodeCommand's callers), so one dead node's timeout
// never delays another's.
const weatherDelayNodeCommandConfirmDeadline = 5 * time.Second

// weatherDelayHTTPClientTimeout bounds the direct-HTTP start: short, no
// retries inside the request.
const weatherDelayHTTPClientTimeout = 3 * time.Second

// weatherDelayHTTPClient never follows a redirect: the address came from a
// node's own hello, and a redirect would send the signed start elsewhere.
var weatherDelayHTTPClient = &http.Client{
	Timeout:       weatherDelayHTTPClientTimeout,
	CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
}

// weatherDelayRetainedPublishTimeout bounds the retained state publish that
// start makes before any stop, so a stalled broker cannot hold the stops.
const weatherDelayRetainedPublishTimeout = 1 * time.Second

const weatherDelayHandlerWriteDeadlineMargin = 15 * time.Second

func weatherDelayHandlerWriteDeadline() time.Duration {
	return weatherDelayNodeCommandConfirmDeadline + weatherDelayHandlerWriteDeadlineMargin
}

// weatherDelayActiveProblem is every hold-side gate's shared refusal.
// detail is the operator-visible sentence: fact first, then the action,
// no internals vocabulary (this build's own operator-copy standard).
func weatherDelayActiveProblem(detail string) v1.Problem {
	return v1.Problem{
		Type:   ProblemTypeConflict,
		Title:  "A weather delay is active",
		Status: http.StatusConflict,
		Detail: detail,
	}
}

// weatherDelayActive reads the current stored state and reports whether
// it is active, the one check every hold-side gate (nightloop.go,
// cueactivationloop.go, cuefire.go, fppcommand_handler.go,
// nightsessioncontrol.go, weatherdelayconfig.go) makes, read fresh from
// the store at each decision (ADR-053 decision 2's own "survives a
// restart" requirement).
func (h *handlers) weatherDelayActive(ctx context.Context) (bool, error) {
	rec, err := h.deps.WeatherDelay.GetWeatherDelayState(ctx)
	if err != nil {
		return false, err
	}
	return rec.Active, nil
}

// handleGetWeatherDelayState serves GET /api/v1/weather-delay.
func (h *handlers) handleGetWeatherDelayState(w http.ResponseWriter, r *http.Request) {
	now := h.now()
	ctx := r.Context()

	rec, err := h.deps.WeatherDelay.GetWeatherDelayState(ctx)
	if err != nil {
		h.writeInternalError(w, now, "get weather delay state", err)
		return
	}

	resp := v1.WeatherDelayStateResponse{ServerTime: formatTime(now), Active: rec.Active, Revision: rec.Revision, Assets: []v1.WeatherDelayNodeAssets{}}
	if rec.Active {
		resp.Kind = rec.Kind
		resp.StartedAt = formatTime(rec.StartedAt)
		resp.StartedBy = rec.StartedBy
	}

	payload, _, _, _, err := resolveWeatherDelayConfig(ctx, h.deps.Config)
	if err != nil {
		h.logWarn("weather delay: failed to resolve show.weatherdelay config for asset readiness; reporting no asset evidence", "error", err)
	} else {
		resp.Assets = h.weatherDelayAssetReadiness(ctx, payload)
	}
	jsonWrite(w, resp)
}

// weatherDelayAssetReadiness reports, per plan node, whether each alert
// asset is present and hash-verified there. Anything unreadable reads as
// not present rather than failing the whole response.
func (h *handlers) weatherDelayAssetReadiness(ctx context.Context, payload config.WeatherDelayPayload) []v1.WeatherDelayNodeAssets {
	if payload.Alert.DelayAssetID == "" && payload.Alert.CancelNightAssetID == "" {
		return []v1.WeatherDelayNodeAssets{}
	}
	nodeIDs, err := h.weatherDelayPlanNodeIDs(ctx, payload.Alert.NodeIDs)
	if err != nil {
		h.logWarn("weather delay: failed to resolve plan node ids for asset readiness", "error", err)
		return []v1.WeatherDelayNodeAssets{}
	}
	out := make([]v1.WeatherDelayNodeAssets, 0, len(nodeIDs))
	for _, nodeID := range nodeIDs {
		na := v1.WeatherDelayNodeAssets{NodeID: nodeID}
		if payload.Alert.DelayAssetID != "" {
			na.DelayAsset = h.weatherDelayAssetStatus(ctx, payload.Alert.DelayAssetID, nodeID)
		}
		if payload.Alert.CancelNightAssetID != "" {
			na.CancelAsset = h.weatherDelayAssetStatus(ctx, payload.Alert.CancelNightAssetID, nodeID)
		}
		out = append(out, na)
	}
	return out
}

func (h *handlers) weatherDelayAssetStatus(ctx context.Context, assetID, nodeID string) *v1.WeatherDelayAssetStatus {
	rec, err := h.deps.Assets.GetAsset(ctx, assetID)
	if err != nil {
		return &v1.WeatherDelayAssetStatus{AssetID: assetID}
	}
	present, err := h.deps.WeatherDelayAssetSync.AssetPresence(ctx, nodeID, rec.ContentHash)
	if err != nil {
		h.logWarn("weather delay: failed to read node asset inventory for readiness", "nodeId", nodeID, "assetId", assetID, "error", err)
	}
	return &v1.WeatherDelayAssetStatus{AssetID: assetID, Present: present, Filename: rec.RuntimeFilename}
}

// weatherDelayPlanNodeIDs resolves the plan's own node id list: configured
// explicitly, or every declared audio.node when configured is empty
// (ADR-053's own "empty means every declared audio node").
func (h *handlers) weatherDelayPlanNodeIDs(ctx context.Context, configured []string) ([]string, error) {
	if len(configured) > 0 {
		return configured, nil
	}
	return h.declaredAudioNodeIDs(ctx)
}

// decodeWeatherDelayActionRequestBody reads the {"idempotencyKey": string}
// body the three action routes share.
func decodeWeatherDelayActionRequestBody(r *http.Request) (idempotencyKey string, problem *v1.Problem) {
	body, err := io.ReadAll(io.LimitReader(r.Body, maxWeatherDelayActionRequestBodyBytes+1))
	if err != nil {
		p := invalidParameterProblem(fmt.Sprintf("reading request body: %v", err))
		return "", &p
	}
	if int64(len(body)) > maxWeatherDelayActionRequestBodyBytes {
		p := invalidParameterProblem("request body too large")
		return "", &p
	}
	var top map[string]json.RawMessage
	if len(body) > 0 {
		if err := json.Unmarshal(body, &top); err != nil {
			p := invalidParameterProblem(`request body must be a JSON object matching {"idempotencyKey":string}`)
			return "", &p
		}
	}
	var unknown []string
	for k := range top {
		if k != "idempotencyKey" {
			unknown = append(unknown, k)
		}
	}
	if len(unknown) > 0 {
		p := invalidParameterProblem(fmt.Sprintf("request body contains unrecognized key(s): %v", unknown))
		return "", &p
	}
	if raw, ok := top["idempotencyKey"]; ok {
		if err := json.Unmarshal(raw, &idempotencyKey); err != nil {
			p := invalidParameterProblem("idempotencyKey must be a string")
			return "", &p
		}
	}
	if err := command.ValidateIdempotencyKey(idempotencyKey); err != nil {
		p := invalidParameterProblem("idempotencyKey: " + err.Error())
		return "", &p
	}
	return idempotencyKey, nil
}

// handleWeatherDelayStart persists and publishes the active state before
// any stop, then sends every stop and node start concurrently. A failed
// target is reported and never rolls the state back.
func (h *handlers) handleWeatherDelayStart(w http.ResponseWriter, r *http.Request) {
	now := h.now()
	_ = http.NewResponseController(w).SetWriteDeadline(now.Add(weatherDelayHandlerWriteDeadline()))
	ctx := context.WithoutCancel(r.Context())

	idempotencyKey, problem := decodeWeatherDelayActionRequestBody(r)
	if problem != nil {
		writeProblem(w, h.logger, now, *problem)
		return
	}
	ac := authFromContext(r.Context())
	clientAddr := h.clientAddr(r)
	issuerID := ac.result.Principal.ID
	if issuerID == "" {
		issuerID = "unknown"
	}

	current, err := h.deps.WeatherDelay.GetWeatherDelayState(ctx)
	if err != nil {
		h.writeInternalError(w, now, "get weather delay state", err)
		return
	}

	rec := current
	if !current.Active {
		rec = store.WeatherDelayStateRecord{
			Active: true, Kind: weatherdelay.KindDelay, StartedAt: now, StartedBy: issuerID,
			Revision: current.Revision + 1,
		}
		if err := h.deps.WeatherDelay.SetWeatherDelayState(ctx, rec); err != nil {
			h.writeInternalError(w, now, "set weather delay state", err)
			return
		}
	}

	payload, _, _, _, err := resolveWeatherDelayConfig(ctx, h.deps.Config)
	if err != nil {
		h.logWarn("weather delay start: failed to resolve show.weatherdelay config; proceeding with no alert plan", "error", err)
		payload = config.WeatherDelayDefaultPayload
	}
	plan, planErr := h.buildWeatherDelayPlan(ctx, payload.Alert)
	if planErr != nil {
		h.logWarn("weather delay start: failed to build the alert plan; publishing with an empty plan", "error", planErr)
	}

	h.publishWeatherDelayState(ctx, "start", rec, plan, now)

	planNodeIDs, err := h.weatherDelayPlanNodeIDs(ctx, payload.Alert.NodeIDs)
	if err != nil {
		h.logWarn("weather delay start: failed to resolve plan node ids; no node command could be dispatched", "error", err)
		planNodeIDs = nil
	}

	var (
		wg                                                          sync.WaitGroup
		fppOutcomes, resolumeOutcomes, renderOutcomes, nodeOutcomes []v1.WeatherDelayTargetOutcome
	)
	wg.Add(4)
	go func() {
		defer wg.Done()
		outcomes, _ := h.emergencyStopAllInstances(ctx, now, idempotencyKey, ac, clientAddr)
		fppOutcomes = weatherDelayWireOutcomes(outcomes)
	}()
	go func() {
		defer wg.Done()
		resolumeOutcomes = weatherDelayWireOutcomes(h.emergencyStopAllResolumeInstances(ctx, now))
	}()
	go func() {
		defer wg.Done()
		renderOutcomes = weatherDelayWireOutcomes(h.clearAllRenderSurfaces(ctx, now, idempotencyKey, issuerID, ac.result.Principal.Name, ac.result.Form, ac.result.CredentialID, clientAddr))
	}()
	go func() {
		defer wg.Done()
		nodeOutcomes = h.weatherDelayDispatchToNodes(ctx, now, "weatherdelay.start", weatherdelay.KindDelay, idempotencyKey, planNodeIDs, ac, clientAddr, true)
	}()

	// Plan nodes never get audio.node.silence: it would race their own
	// weatherdelay.start and could silence the alert.
	var silenceOutcomes []v1.WeatherDelayTargetOutcome
	wg.Add(1)
	go func() {
		defer wg.Done()
		silenceOutcomes = weatherDelayWireOutcomes(h.weatherDelaySilenceNonPlanNodes(ctx, now, idempotencyKey, planNodeIDs, ac, clientAddr))
	}()

	wg.Wait()

	targets := make([]v1.WeatherDelayTargetOutcome, 0, len(fppOutcomes)+len(resolumeOutcomes)+len(renderOutcomes)+len(nodeOutcomes)+len(silenceOutcomes))
	targets = append(targets, fppOutcomes...)
	targets = append(targets, resolumeOutcomes...)
	targets = append(targets, renderOutcomes...)
	targets = append(targets, silenceOutcomes...)
	targets = append(targets, nodeOutcomes...)

	h.writeBestEffortAuditBounded(ctx, now, degradedAttributionReasonPostDispatch, identity.AuditEntry{
		Timestamp: now, PrincipalID: ac.result.Principal.ID, PrincipalName: ac.result.Principal.Name,
		Form: ac.result.Form, CredentialID: ac.result.CredentialID, ClientAddr: clientAddr,
		Action: identity.AuditActionShowWeatherDelayStart, Target: weatherdelay.KindDelay, IdempotencyKey: idempotencyKey,
		Kind: identity.AuditOutcome, Params: map[string]any{"targets": len(targets), "revision": rec.Revision},
	})
	h.appendWeatherDelayChangedEvent(ctx, now, "started")
	h.notifyStreamHub()

	jsonWrite(w, v1.WeatherDelayActionResponse{ServerTime: formatTime(now), Result: v1.WeatherDelayActionResult{
		Kind: weatherdelay.KindDelay, IdempotencyKey: idempotencyKey, Active: true,
		StartedAt: formatTime(rec.StartedAt), StartedBy: rec.StartedBy, Revision: rec.Revision,
		Targets: targets,
	}})
}

// handleWeatherDelayResume always takes full effect, even when the stored
// state is already not active: a node can be delayed without this
// coordinator knowing, and clears only on a newer revision or the resume
// operation, which goes to every node over MQTT only.
func (h *handlers) handleWeatherDelayResume(w http.ResponseWriter, r *http.Request) {
	now := h.now()
	_ = http.NewResponseController(w).SetWriteDeadline(now.Add(weatherDelayHandlerWriteDeadline()))
	ctx := context.WithoutCancel(r.Context())

	idempotencyKey, problem := decodeWeatherDelayActionRequestBody(r)
	if problem != nil {
		writeProblem(w, h.logger, now, *problem)
		return
	}
	ac := authFromContext(r.Context())
	clientAddr := h.clientAddr(r)

	current, err := h.deps.WeatherDelay.GetWeatherDelayState(ctx)
	if err != nil {
		h.writeInternalError(w, now, "get weather delay state", err)
		return
	}

	// The night session is rewound while the delay still holds the night
	// loop, so no tick can act on the stopped show in between.
	nightOutcome := "untouched: no weather delay was active"
	if current.Active {
		nightOutcome = h.weatherDelayResumeNightSession(ctx, now)
	}

	rec := store.WeatherDelayStateRecord{Active: false, Revision: current.Revision + 1}
	if err := h.deps.WeatherDelay.SetWeatherDelayState(ctx, rec); err != nil {
		h.writeInternalError(w, now, "set weather delay state", err)
		return
	}

	payload, _, _, _, err := resolveWeatherDelayConfig(ctx, h.deps.Config)
	if err != nil {
		h.logWarn("weather delay resume: failed to resolve show.weatherdelay config; proceeding with no alert plan", "error", err)
		payload = config.WeatherDelayDefaultPayload
	}
	plan, planErr := h.buildWeatherDelayPlan(ctx, payload.Alert)
	if planErr != nil {
		h.logWarn("weather delay resume: failed to build the alert plan; publishing with an empty plan", "error", planErr)
	}
	h.publishWeatherDelayState(ctx, "resume", rec, plan, now)

	nodeIDs := h.weatherDelayResumeNodeIDs(ctx, now, payload.Alert.NodeIDs)
	nodeOutcomes := h.weatherDelayDispatchToNodes(ctx, now, "weatherdelay.resume", "", idempotencyKey, nodeIDs, ac, clientAddr, false)

	h.writeBestEffortAuditBounded(ctx, now, degradedAttributionReasonPostDispatch, identity.AuditEntry{
		Timestamp: now, PrincipalID: ac.result.Principal.ID, PrincipalName: ac.result.Principal.Name,
		Form: ac.result.Form, CredentialID: ac.result.CredentialID, ClientAddr: clientAddr,
		Action: identity.AuditActionShowWeatherDelayResume, Target: "resume", IdempotencyKey: idempotencyKey,
		Kind: identity.AuditOutcome, Params: map[string]any{"targets": len(nodeOutcomes), "revision": rec.Revision, "wasActive": current.Active, "nightSession": nightOutcome},
	})
	h.appendWeatherDelayChangedEvent(ctx, now, "resumed")
	h.notifyStreamHub()

	jsonWrite(w, v1.WeatherDelayActionResponse{ServerTime: formatTime(now), Result: v1.WeatherDelayActionResult{
		Kind: "resume", IdempotencyKey: idempotencyKey, Active: false, Revision: rec.Revision,
		Targets: nodeOutcomes,
	}})
}

// publishWeatherDelayState publishes rec retained, bounded by
// [weatherDelayRetainedPublishTimeout]; a failure is logged, never fatal.
func (h *handlers) publishWeatherDelayState(ctx context.Context, op string, rec store.WeatherDelayStateRecord, plan mqttproto.WeatherDelayPlan, now time.Time) {
	kind, startedAt, startedBy := "", time.Time{}, ""
	if rec.Active {
		kind, startedAt, startedBy = rec.Kind, rec.StartedAt, rec.StartedBy
	}
	msg, err := mqttproto.NewWeatherDelayMessage(rec.Active, kind, startedAt, startedBy, rec.Revision, plan, now)
	if err != nil {
		h.logWarn("weather delay "+op+": refusing to publish an invalid state message", "error", err)
		return
	}
	payloadBytes, err := mqttproto.EncodeWeatherDelayMessage(msg)
	if err != nil {
		h.logWarn("weather delay "+op+": failed to encode the state message", "error", err)
		return
	}
	pubCtx, cancel := context.WithTimeout(ctx, weatherDelayRetainedPublishTimeout)
	defer cancel()
	if err := h.deps.WeatherDelayPublisher.Publish(pubCtx, mqttproto.WeatherDelayTopic(), mqttproto.WeatherDelayDeliveryPolicy.QoS, mqttproto.WeatherDelayDeliveryPolicy.Retain, payloadBytes); err != nil {
		h.logWarn("weather delay "+op+": failed to publish the retained state", "error", err)
	}
}

// weatherDelayResumeNodeIDs is every node that could hold a delay: the
// plan's nodes, every declared audio node, and every node in inventory.
func (h *handlers) weatherDelayResumeNodeIDs(ctx context.Context, now time.Time, configured []string) []string {
	seen := map[string]bool{}
	var out []string
	add := func(ids ...string) {
		for _, id := range ids {
			if id != "" && !seen[id] {
				seen[id] = true
				out = append(out, id)
			}
		}
	}
	add(configured...)
	if declared, err := h.declaredAudioNodeIDs(ctx); err != nil {
		h.logWarn("weather delay resume: failed to list declared audio.node ids", "error", err)
	} else {
		add(declared...)
	}
	if views, err := h.deps.Nodes.Snapshot(ctx, now); err != nil {
		h.logWarn("weather delay resume: failed to list inventory nodes", "error", err)
	} else {
		for _, v := range views {
			add(v.NodeID)
		}
	}
	return out
}

// weatherDelayInterruptedCycleReason is recorded when a delay stopped a
// live show; resume starts that show again from the top as a new cycle.
const weatherDelayInterruptedCycleReason = "A weather delay stopped the show before its last sequence played. The show starts again from the top when the delay ends."

// weatherDelayResumeNightSession returns an active night session to where
// it should be after a delay: a show that was live or about to start goes
// back to the start of its transition into the show, as start-night enters
// it; preshow and resting restart their resting playlist from the top.
// Other states and degraded sessions are left alone.
func (h *handlers) weatherDelayResumeNightSession(ctx context.Context, now time.Time) string {
	var (
		result       string
		closeCycle   int64
		closeCycleID string
	)
	err := h.deps.NightSessions.InTx(ctx, func(ctx context.Context, tx *store.Tx) error {
		cur, ok, err := tx.GetCurrentNightSession(ctx)
		if err != nil {
			return err
		}
		if !ok {
			result = "none"
			return nil
		}
		if cur.Degraded {
			result = "untouched: session " + cur.ID + " is degraded"
			return nil
		}
		next := cur
		switch cur.State {
		case nightStateLive, nightStateTransitionToShow:
			payload, err := h.getPinnedNightSessionPayloadTx(ctx, tx, cur)
			if err != nil {
				return err
			}
			if cur.State == nightStateLive {
				closeCycle, closeCycleID = cur.Cycle, cur.ID
			}
			boundaryE := now.Add(time.Duration(nightEnterShowLeadMs(payload.EnterShow.Cues)) * time.Millisecond)
			lastTick := now
			next.State = nightStateTransitionToShow
			next.ArmedShowID = uuid.NewString()
			next.ShowCommitted = false
			next.Cycle = cur.Cycle + 1
			next.ContentAnchorJSON = ""
			next.BoundaryJSON = encodeNightBoundary(nightBoundary{State: nightBoundaryStateArmed, ExpectedAt: &boundaryE, LastTickAt: &lastTick,
				Reason: fmt.Sprintf("The show starts again from the top after a weather delay, expected at %s.", boundaryE.Format(time.RFC3339))})
		case nightStatePreshow, nightStateRestingIntershow:
			next.ContentAnchorJSON = ""
			next.BoundaryJSON = ""
		default:
			result = "untouched: session " + cur.ID + " is " + cur.State
			return nil
		}
		next.StateEnteredAt = now
		result = "session " + cur.ID + " moved from " + cur.State + " to " + next.State
		return tx.UpdateNightSession(ctx, next, now)
	})
	if err != nil {
		h.logWarn("weather delay resume: failed to return the night session to its show transition", "error", err)
		return "failed: " + err.Error()
	}
	if closeCycleID != "" {
		if err := h.deps.NightSessions.CloseNightCycleOutcome(ctx, closeCycleID, closeCycle, now, store.NightCycleOutcomeInterrupted, weatherDelayInterruptedCycleReason); err != nil && err != store.ErrNightCycleOutcomeNotFound {
			h.logWarn("weather delay resume: failed to close the interrupted night cycle outcome", "sessionId", closeCycleID, "cycle", closeCycle, "error", err)
		}
	}
	return result
}

// buildWeatherDelayPlan resolves show.weatherdelay's alert config into the
// wire plan every retained state message carries.
func (h *handlers) buildWeatherDelayPlan(ctx context.Context, alert config.WeatherDelayAlertPayload) (mqttproto.WeatherDelayPlan, error) {
	plan := mqttproto.WeatherDelayPlan{RepeatCount: alert.RepeatCount, NodeIDs: alert.NodeIDs}
	if alert.DelayAssetID != "" {
		ref, err := h.weatherDelayAssetRef(ctx, alert.DelayAssetID)
		if err != nil {
			return plan, err
		}
		plan.Delay = ref
	}
	if alert.CancelNightAssetID != "" {
		ref, err := h.weatherDelayAssetRef(ctx, alert.CancelNightAssetID)
		if err != nil {
			return plan, err
		}
		plan.CancelNight = ref
	}
	return plan, nil
}

func (h *handlers) weatherDelayAssetRef(ctx context.Context, assetID string) (*mqttproto.WeatherDelayAlertAssetRef, error) {
	rec, err := h.deps.Assets.GetAsset(ctx, assetID)
	if err != nil {
		return nil, fmt.Errorf("get asset %q: %w", assetID, err)
	}
	return &mqttproto.WeatherDelayAlertAssetRef{AssetID: rec.ID, ContentHash: rec.ContentHash, Filename: rec.RuntimeFilename}, nil
}

// weatherDelaySilenceNonPlanNodes dispatches audio.node.silence to every
// declared audio.node OUTSIDE planNodeIDs, concurrently, mirroring
// [handlers.emergencyStopAllAudioNodes]'s identical dispatch shape one
// file over, narrowed to the complement of the plan.
func (h *handlers) weatherDelaySilenceNonPlanNodes(ctx context.Context, now time.Time, idempotencyKey string, planNodeIDs []string, ac authContext, clientAddr string) []v1.EmergencyStopInstanceOutcome {
	all, err := h.declaredAudioNodeIDs(ctx)
	if err != nil {
		h.logWarn("weather delay start: failed to list declared audio.node ids; no silence could be dispatched to non-plan nodes", "error", err)
		return []v1.EmergencyStopInstanceOutcome{{
			InstanceID: emergencyStopNodeListUnavailableID, TargetKind: v1.EmergencyStopTargetKindNode,
			Outcome: "failed", OutcomeReason: fmt.Sprintf("could not list declared audio.node ids: %v", err),
		}}
	}
	inPlan := make(map[string]bool, len(planNodeIDs))
	for _, id := range planNodeIDs {
		inPlan[id] = true
	}
	var nonPlan []string
	for _, id := range all {
		if !inPlan[id] {
			nonPlan = append(nonPlan, id)
		}
	}
	if len(nonPlan) == 0 {
		return []v1.EmergencyStopInstanceOutcome{}
	}

	out := make([]v1.EmergencyStopInstanceOutcome, len(nonPlan))
	var wg sync.WaitGroup
	for i, nodeID := range nonPlan {
		wg.Add(1)
		go func(i int, nodeID string) {
			defer wg.Done()
			result, problem, err := h.executeAudioNodeSilenceDispatch(ctx, now, AudioNodeSilenceDispatchInput{
				NodeID: nodeID, IdempotencyKey: emergencyStopNodeIdempotencyKey(idempotencyKey, nodeID),
				IssuerID: ac.result.Principal.ID, IssuerName: ac.result.Principal.Name,
				IssuerForm: ac.result.Form, IssuerCredentialID: ac.result.CredentialID, ClientAddr: clientAddr,
			})
			switch {
			case err != nil:
				out[i] = v1.EmergencyStopInstanceOutcome{InstanceID: nodeID, TargetKind: v1.EmergencyStopTargetKindNode, Outcome: "failed", OutcomeReason: "this silence could not be dispatched because of an internal coordinator error"}
			case problem != nil:
				out[i] = v1.EmergencyStopInstanceOutcome{InstanceID: nodeID, TargetKind: v1.EmergencyStopTargetKindNode, Outcome: "refused", OutcomeReason: problem.Detail}
			default:
				out[i] = v1.EmergencyStopInstanceOutcome{InstanceID: nodeID, TargetKind: v1.EmergencyStopTargetKindNode, Outcome: result.Outcome, OutcomeReason: result.Reason, DispatchedAt: nonEmptyStrPtr(result.DispatchedAt), Replay: result.Replay}
			}
		}(i, nodeID)
	}
	wg.Wait()
	return out
}

// weatherDelayDispatchToNodes dispatches action ("weatherdelay.start" or
// "weatherdelay.resume") to every id in nodeIDs, CONCURRENTLY, over MQTT
// and, for start only, per ADR-053 decision 9, the direct signed HTTP
// path in parallel with it, never as a fallback. A node reached by either
// path counts as reached.
func (h *handlers) weatherDelayDispatchToNodes(ctx context.Context, now time.Time, action, kind, idempotencyKey string, nodeIDs []string, ac authContext, clientAddr string, allowHTTP bool) []v1.WeatherDelayTargetOutcome {
	if len(nodeIDs) == 0 {
		return []v1.WeatherDelayTargetOutcome{}
	}
	out := make([]v1.WeatherDelayTargetOutcome, len(nodeIDs))
	var wg sync.WaitGroup
	for i, nodeID := range nodeIDs {
		wg.Add(1)
		go func(i int, nodeID string) {
			defer wg.Done()
			out[i] = h.dispatchWeatherDelayNodeCommand(ctx, now, action, kind, idempotencyKey, nodeID, ac, clientAddr, allowHTTP)
		}(i, nodeID)
	}
	wg.Wait()
	return out
}

// dispatchWeatherDelayNodeCommand runs one node's own MQTT command and
// (when allowHTTP) direct signed HTTP request CONCURRENTLY with each
// other, so neither ever waits on the other's confirmation or timeout.
func (h *handlers) dispatchWeatherDelayNodeCommand(ctx context.Context, now time.Time, action, kind, idempotencyKey, nodeID string, ac authContext, clientAddr string, allowHTTP bool) v1.WeatherDelayTargetOutcome {
	var (
		mqttOK, httpOK   bool
		mqttReason       string
		mqttDispatchedAt *string
		httpAttempted    bool
	)

	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		mqttOK, mqttReason, mqttDispatchedAt = h.weatherDelayMQTTNodeCommand(ctx, now, action, kind, idempotencyKey, nodeID, ac, clientAddr)
	}()

	if allowHTTP {
		if addr, ok := h.deps.WeatherDelayNodeAddrs.InboundListener(nodeID); ok && addr != "" {
			httpAttempted = true
			wg.Add(1)
			go func() {
				defer wg.Done()
				httpOK = h.weatherDelayHTTPNodeStart(ctx, kind, addr)
			}()
		}
	}
	wg.Wait()

	deliveredVia := "none"
	switch {
	case mqttOK && httpOK:
		deliveredVia = "both"
	case mqttOK:
		deliveredVia = "mqtt"
	case httpOK:
		deliveredVia = "http"
	}

	reached := mqttOK || httpOK
	outcome := "failed"
	reason := mqttReason
	if reached {
		outcome = "confirmed"
		reason = "reached via " + deliveredVia
	} else if !httpAttempted {
		reason = mqttReason + "; no reported inbound listener for the direct HTTP path"
	}

	return v1.WeatherDelayTargetOutcome{
		InstanceID: nodeID, TargetKind: v1.WeatherDelayTargetKindNodeCommand,
		Outcome: outcome, OutcomeReason: reason, DeliveredVia: deliveredVia, DispatchedAt: mqttDispatchedAt,
	}
}

func (h *handlers) weatherDelayMQTTNodeCommand(ctx context.Context, now time.Time, action, kind, idempotencyKey, nodeID string, ac authContext, clientAddr string) (ok bool, reason string, dispatchedAt *string) {
	commandID := uuid.NewString()
	params := map[string]any{}
	if kind != "" {
		params["kind"] = kind
	}

	cmdTopic, err := mqttproto.CmdTopic(nodeID)
	if err != nil {
		return false, fmt.Sprintf("build cmd topic: %v", err), nil
	}
	resultTopic, err := mqttproto.ResultTopic(nodeID, commandID)
	if err != nil {
		return false, fmt.Sprintf("build result topic: %v", err), nil
	}
	payload := mqttproto.CmdPayload{
		CommandID: commandID, IdempotencyKey: idempotencyKey, Action: action,
		Target: mqttproto.CmdTarget{Kind: "node", ID: nodeID}, Params: params,
		Issuer:             mqttproto.CmdIssuer{PrincipalID: ac.result.Principal.ID, PrincipalName: ac.result.Principal.Name},
		ConfirmationMethod: "evidence",
	}
	env, err := mqttproto.NewCmdEnvelope(func() time.Time { return now }, nodeID, payload)
	if err != nil {
		return false, fmt.Sprintf("build cmd envelope: %v", err), nil
	}
	rawEnv, err := json.Marshal(env)
	if err != nil {
		return false, fmt.Sprintf("marshal cmd envelope: %v", err), nil
	}

	dispatchedAtStr := formatTime(now)
	awaitCtx, cancel := context.WithTimeout(ctx, weatherDelayRetainedPublishTimeout+weatherDelayNodeCommandConfirmDeadline)
	defer cancel()
	msg, err := h.deps.WeatherDelayPublisher.AwaitResponse(awaitCtx, broker.ResponseRequest{
		PublishTopic: cmdTopic, PublishPayload: rawEnv,
		PublishQoS: mqttproto.CmdDeliveryPolicy.QoS, PublishRetain: mqttproto.CmdDeliveryPolicy.Retain,
		ResponseTopic: resultTopic, ResponseQoS: mqttproto.ResultDeliveryPolicy.QoS,
		Deadline: weatherDelayNodeCommandConfirmDeadline,
		Match: func(m broker.Message) bool {
			return audioResultCorrelates(m.Payload, nodeID, commandID, idempotencyKey, action)
		},
	})
	if err != nil {
		if errors.Is(err, broker.ErrResponseFailedBeforePublish) {
			return false, fmt.Sprintf("publish failed: %v", err), nil
		}
		// The command WAS published, see [broker.ResponseRequest]'s doc
		// comment: AwaitResponse subscribes then publishes, so a deadline
		// exceeded past this point still means the command reached the
		// broker. That is enough to count this node as reached over MQTT:
		// the plan's ordinary confirmation path (this same result topic)
		// is separate best-effort evidence, not this outcome's own gate.
		return true, fmt.Sprintf("published, but no result was confirmed before the deadline: %v", err), &dispatchedAtStr
	}
	env2, err := mqttproto.DecodeEnvelope(msg.Payload)
	if err != nil {
		return true, "published; result payload did not decode", &dispatchedAtStr
	}
	res, err := mqttproto.DecodeResultPayload(env2)
	if err != nil {
		return true, "published; result payload did not decode", &dispatchedAtStr
	}
	return true, res.Reason, &dispatchedAtStr
}

// weatherDelayHTTPNodeStart POSTs a signed start directly to addr's
// showmesh/v1/weather-delay/start, in parallel with the MQTT path, never
// as its fallback (ADR-053 decision 8). false on any failure to sign,
// build, or send the request, or a non-2xx response, this path is
// evidence, not a requirement; MQTT alone can still deliver the start.
func (h *handlers) weatherDelayHTTPNodeStart(ctx context.Context, kind, addr string) bool {
	req := weatherdelay.StartRequest{Kind: kind, IssuedAt: h.now(), Nonce: uuid.NewString()}
	signed, err := weatherdelay.Sign(req, h.deps.WeatherDelaySigner)
	if err != nil {
		h.logWarn("weather delay start: failed to sign the direct-HTTP start request", "addr", addr, "error", err)
		return false
	}
	body, err := json.Marshal(signed)
	if err != nil {
		h.logWarn("weather delay start: failed to encode the direct-HTTP start request", "addr", addr, "error", err)
		return false
	}
	url := "http://" + addr + "/showmesh/v1/weather-delay/start"
	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(body))
	if err != nil {
		h.logWarn("weather delay start: failed to build the direct-HTTP start request", "addr", addr, "error", err)
		return false
	}
	httpReq.Header.Set("Content-Type", "application/json")
	resp, err := weatherDelayHTTPClient.Do(httpReq)
	if err != nil {
		h.logWarn("weather delay start: direct-HTTP start request failed", "addr", addr, "error", err)
		return false
	}
	defer func() { _ = resp.Body.Close() }()
	_, _ = io.Copy(io.Discard, resp.Body)
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		h.logWarn("weather delay start: direct-HTTP start request refused", "addr", addr, "status", resp.StatusCode)
		return false
	}
	return true
}

// weatherDelayWireOutcomes maps [v1.EmergencyStopInstanceOutcome] (this
// package's shared target-kind dispatch shape) onto
// [v1.WeatherDelayTargetOutcome], the wire shape weather delay's own
// endpoints answer with.
func weatherDelayWireOutcomes(in []v1.EmergencyStopInstanceOutcome) []v1.WeatherDelayTargetOutcome {
	out := make([]v1.WeatherDelayTargetOutcome, len(in))
	for i, o := range in {
		out[i] = v1.WeatherDelayTargetOutcome{
			InstanceID: o.InstanceID, TargetKind: o.TargetKind, Outcome: o.Outcome,
			OutcomeReason: o.OutcomeReason, DispatchedAt: o.DispatchedAt,
		}
	}
	return out
}

// appendWeatherDelayChangedEvent records [v1.EventKindWeatherDelayChanged]
// in the durable events table, best
// effort: a failure to append is logged and never turns a successful
// start/resume into an error.
func (h *handlers) appendWeatherDelayChangedEvent(ctx context.Context, now time.Time, summary string) {
	if h.deps.WeatherDelayEvents == nil {
		return
	}
	_, err := h.deps.WeatherDelayEvents.AppendEvent(ctx, store.EventRecord{
		Source:   "weather-delay",
		Resource: observation.ResourceRef{Kind: observation.ResourceCoordinator, ID: "weather-delay"},
		Category: v1.EventKindWeatherDelayChanged, Severity: "info",
		Summary: "weather delay " + summary, OccurredAt: &now,
	})
	if err != nil {
		h.logWarn("weather delay: failed to append change-stream event", "error", err)
	}
}

// handleWeatherDelayCancelNight serves POST /api/v1/weather-delay/cancel-night.
func (h *handlers) handleWeatherDelayCancelNight(w http.ResponseWriter, r *http.Request) {
	h.handleWeatherDelayNotImplemented(w, r, "cancelling the night for weather")
}

func (h *handlers) handleWeatherDelayNotImplemented(w http.ResponseWriter, r *http.Request, action string) {
	now := h.now()
	if _, problem := decodeWeatherDelayActionRequestBody(r); problem != nil {
		writeProblem(w, h.logger, now, *problem)
		return
	}
	writeProblem(w, h.logger, now, notImplementedProblem(
		fmt.Sprintf("%s is not available yet on this coordinator", action)))
}
