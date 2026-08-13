package api

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"sort"
	"strconv"
	"time"

	"github.com/google/uuid"

	v1 "github.com/showmeshsystems/showmesh/internal/coordinator/api/v1"
	"github.com/showmeshsystems/showmesh/internal/coordinator/identity"
	"github.com/showmeshsystems/showmesh/internal/coordinator/inventory"
	"github.com/showmeshsystems/showmesh/internal/coordinator/store"
	"github.com/showmeshsystems/showmesh/pkg/mqttproto"
	"github.com/showmeshsystems/showmesh/pkg/observation"
)

// This file implements BUILD-PLAN Step 7 seam B: node discovery and
// declaration (RES-008 D2/D6). The idea, stated once here because getting
// it backwards crosses ADR-003: a discovery run PROPOSES what it observes,
// and an operator action PROMOTES a proposal to a declaration.
// RecordNodeDiscoverySeen (seam 0, store/nodes_declared.go) can only ever
// touch a node ALREADY declared; nothing in this file creates or deletes a
// declaration except handlePromoteNode and handleDeleteNodeDeclaration,
// both direct operator actions behind config:write.
//
// Discovery here performs NO active probing — no mDNS, no subnet sweep, no
// MultiSync discover ping. It reads what this coordinator already
// observes: agent hellos already in inventory (h.deps.Nodes.Snapshot), and
// configured FPP instances (h.deps.FPP.ListInstances). That narrowing is
// deliberate: active probing puts new traffic on the show LAN, touches
// ADR-013's rules about MultiSync sockets, and would need its own research
// record and bench verification before anyone could trust it — a later
// step's work, not a missing piece of this one. The honest consequence:
// discovery cannot find equipment that has never talked to ShowMesh.

// discoverySourceNode and discoverySourceFPP name where a
// [v1.DiscoveryProposal] came from — the two, and only two, sources a
// discovery run reads per this file's own doc comment.
const (
	discoverySourceNode = "node"
	discoverySourceFPP  = "fpp"
)

// maxDeclarationRequestBodyBytes bounds the request body of every
// discovery/declaration write in this file. SHOWMESH HYPOTHESIS, matching
// [maxSessionRequestBodyBytes]'s own posture: generous for
// {"label":string,"notes":string} or {"confirm":bool} with headroom, small
// enough to cost nothing to enforce against an authenticated (so already
// somewhat trusted, but still not unbounded) caller.
const maxDeclarationRequestBodyBytes = 8 * 1024

// --- what counts as "currently observed" (shared by a run and by a
// promote-time re-check; DEFECT 1 and DEFECT 3) ---

// fppInstanceObserved reports whether fv counts as CURRENTLY, positively
// observed — actual collection evidence, never mere configuration. Mirrors
// the node branch's inventory.LivenessOnline requirement: a configured but
// silent (or persistently failing) FPP instance is exactly RES-008 D2's
// "powered-off equipment" case and must never be indistinguishable, to a
// discovery run, from a genuinely reachable one.
//
// This is the SAME standard [handleStartDiscoveryRun] applies to nodes
// (Liveness == LivenessOnline) applied to the FPP branch, which it
// previously did not: [fppInstanceLister.ListInstances]
// (apiwiring.go) returns a view for EVERY configured endpoint
// unconditionally, synthesizing not_collected placeholders when the store
// holds nothing, so "this FPP instance is configured" and "this FPP
// instance is observed" used to be the same question here. They are not:
// LastPollAt is nil only when no poll has ever completed (fresh
// configuration, or a coordinator that has not ticked once since restart —
// see [fppInstanceEvidenceAmbiguous] for that case), and LastPollError is
// non-nil only when the most recent poll's own reachability signal
// reported collection_failed — a real, connectable-but-erroring attempt.
// Both must be clear for an instance to count as positively observed.
func fppInstanceObserved(fv FPPInstanceView) bool {
	return fv.LastPollAt != nil && fv.LastPollError == nil
}

// fppInstanceEvidenceAmbiguous reports whether fv currently carries NO
// evidence either way — the FPP-branch analog of a node's LivenessUnknown
// (DEFECT 2): never yet polled (LastPollAt nil), most plausibly because the
// coordinator restarted and the FPP collector has not completed its first
// cycle yet. This is deliberately NOT the same condition as
// !fppInstanceObserved(fv): an instance that WAS polled and whose poll
// FAILED (LastPollAt set, LastPollError set) is real negative evidence —
// the LivenessOffline analog — and must be free to support a not_seen
// verdict once a run completes; ambiguous-evidence handling exists only for
// "no attempt has been made yet", never for "an attempt was made and
// failed".
func fppInstanceEvidenceAmbiguous(fv FPPInstanceView) bool {
	return fv.LastPollAt == nil
}

// pluralY renders the English "-y"/"-ies" ending for n, used only by
// handleStartDiscoveryRun's DEFECT 2 incomplete-run reason text.
func pluralY(n int) string {
	if n == 1 {
		return "y"
	}
	return "ies"
}

// --- read-time declaration state (RES-008 D6, B3) ---

// fetchDeclarationContext loads every current node declaration, keyed by
// node id, and the single most recent [store.DiscoveryRunRecord] (nil if
// none has ever been recorded, or none is currently retained — see that
// type's own doc comment: discovery_runs is pruned by retention,
// node_declarations is not, so this is an expected, not exceptional,
// outcome). Every caller that renders one or more [v1.Node] values fetches
// this exactly once per response/render pass, never once per node — see
// [mapNode]'s doc comment for why decl/latestRun are threaded in as
// parameters rather than looked up inside it.
func fetchDeclarationContext(ctx context.Context, ds DeclarationStore) (map[string]store.NodeDeclarationRecord, *store.DiscoveryRunRecord, error) {
	decls, err := ds.ListNodeDeclarations(ctx)
	if err != nil {
		return nil, nil, err
	}
	byNodeID := make(map[string]store.NodeDeclarationRecord, len(decls))
	for _, d := range decls {
		byNodeID[d.NodeID] = d
	}

	runs, err := ds.ListDiscoveryRuns(ctx, 1)
	if err != nil {
		return nil, nil, err
	}
	var latest *store.DiscoveryRunRecord
	if len(runs) > 0 {
		latest = &runs[0]
	}
	return byNodeID, latest, nil
}

// mergeDeclaredOnlyNodes returns views plus one synthetic
// [inventory.NodeView] for every declaration in declByNodeID whose node id
// is NOT already present in views (DEFECT 4). This is the fix for RES-008
// D2's own "known narrowing", flagged rather than hidden in this seam's
// original commit: a declared node that has never once said hello (an
// agent node id nobody has ever heard from, or an FPP instance id declared
// straight off a discovery proposal — apiwiring's fppInstanceLister and
// this package's [NodeLister] are two entirely separate listings, so a
// declared FPP instance id has never appeared in either without this) used
// to be invisible to every node-listing endpoint, reachable by nothing but
// a direct GET/DELETE by id — the review found that one click on THIS
// seam's own "Declare" control (promoting an FPP-sourced proposal) reaches
// exactly that gap, permanently and with no UI path back.
//
// The fix is deliberately "synthesize an [inventory.NodeView] with every
// evidence field nil" rather than a parallel code path: [mapNode] and its
// helpers ([mapControlPlane], [helloObservation]/[lastWillObservation]/
// [heartbeatObservation]) ALREADY render a nil Hello/LWT/Health honestly —
// StateNotCollected evidence with a stated reason, controlPlane.state
// "unknown" — because an inventory node whose agent has said hello but
// never yet sent a heartbeat already exercises that exact path. A declared
// node that has NEVER said hello is just a more complete version of the
// same absence, so it needs no new rendering logic, only a synthetic row
// to feed the existing one.
//
// FirstSeenAt/UpdatedAt are the one honest compromise here: [v1.Node] pins
// them as always-set coordinator bookkeeping of "when the store row was
// created and last touched", which does not exist for a node with no
// inventory row at all. Reporting the DECLARATION's own DeclaredAt/UpdatedAt
// instead is still non-observation bookkeeping (never fabricated observation
// evidence — that distinction is what the doc comment on those two fields
// actually protects), just from a different table; a future step wanting a
// sharper answer would need those two fields to become nullable across
// every existing caller, which this defect fix does not attempt.
func mergeDeclaredOnlyNodes(views []inventory.NodeView, declByNodeID map[string]store.NodeDeclarationRecord) []inventory.NodeView {
	if len(declByNodeID) == 0 {
		return views
	}
	present := make(map[string]struct{}, len(views))
	for _, nv := range views {
		present[nv.NodeID] = struct{}{}
	}
	extra := 0
	for nodeID := range declByNodeID {
		if _, ok := present[nodeID]; !ok {
			extra++
		}
	}
	if extra == 0 {
		return views
	}
	merged := make([]inventory.NodeView, 0, len(views)+extra)
	merged = append(merged, views...)
	for nodeID, decl := range declByNodeID {
		if _, ok := present[nodeID]; ok {
			continue
		}
		merged = append(merged, declarationOnlyNodeView(nodeID, decl))
	}
	// store.ListNodes (what a real [NodeLister] is backed by) orders by node
	// ID, and that ordering is otherwise preserved verbatim above; a plain
	// append of declByNodeID's map-iteration order would make this
	// function's own output order non-deterministic across calls, which
	// would read as a spurious node.changed reordering on every stream hub
	// render tick even though nothing about any node actually changed.
	sort.Slice(merged, func(i, j int) bool { return merged[i].NodeID < merged[j].NodeID })
	return merged
}

// declarationOnlyNodeView builds the synthetic [inventory.NodeView]
// [mergeDeclaredOnlyNodes] appends for a declared node with no inventory
// row: every evidence field nil, Liveness explicitly LivenessUnknown (never
// LivenessOffline — this coordinator has no last-will evidence at all for
// an id nobody has ever heard from, which is a strictly weaker claim than
// "evidence says offline") with a reason naming exactly that, so
// [mapControlPlane] renders controlPlane.state "unknown" rather than a
// fabricated "offline".
func declarationOnlyNodeView(nodeID string, decl store.NodeDeclarationRecord) inventory.NodeView {
	return inventory.NodeView{
		NodeID:         nodeID,
		FirstSeenAt:    decl.DeclaredAt,
		UpdatedAt:      decl.UpdatedAt,
		Liveness:       inventory.LivenessUnknown,
		LivenessReason: "declared but never observed: no agent hello, last-will evidence, or health heartbeat has ever been recorded for this node id",
	}
}

// latestDiscoveryRun is [fetchDeclarationContext]'s narrower sibling for a
// caller (handlePromoteNode) that already has the one declaration it needs
// in hand and has no use for the full declared-node map.
func latestDiscoveryRun(ctx context.Context, ds DeclarationStore) (*store.DiscoveryRunRecord, error) {
	runs, err := ds.ListDiscoveryRuns(ctx, 1)
	if err != nil {
		return nil, err
	}
	if len(runs) == 0 {
		return nil, nil
	}
	return &runs[0], nil
}

// discoveryObservedNow reports whether id is CURRENTLY, positively observed
// by this coordinator — a live agent node, or an FPP instance with actual
// collection evidence ([fppInstanceObserved]) — using the identical
// predicate [handlers.handleStartDiscoveryRun] uses to decide what a run
// counts as observed. [handlePromoteNode] (DEFECT 1) uses this to decide,
// at the moment a brand-new declaration is created, whether it may
// honestly be stamped as seen by the latest discovery run: see that
// function's own doc comment for why this fresh re-check, rather than a
// client-supplied run id, is the design chosen.
func (h *handlers) discoveryObservedNow(ctx context.Context, id string) (bool, error) {
	now := h.now()
	nodeViews, err := h.deps.Nodes.Snapshot(ctx, now)
	if err != nil {
		return false, fmt.Errorf("list node inventory: %w", err)
	}
	for _, nv := range nodeViews {
		if nv.NodeID == id {
			return nv.Liveness == inventory.LivenessOnline, nil
		}
	}
	fppViews, err := h.deps.FPP.ListInstances(ctx)
	if err != nil {
		return false, fmt.Errorf("list fpp instances: %w", err)
	}
	for _, fv := range fppViews {
		if fv.InstanceID == id {
			return fppInstanceObserved(fv), nil
		}
	}
	return false, nil
}

// declPtr looks up nodeID in byNodeID and returns a pointer to a copy of
// the match, or nil — the shape [mapNode] wants for "no declaration
// exists". A pointer into a range variable would alias the loop variable
// across iterations pre-Go-1.22-semantics; taking the address of a fresh
// local copy here sidesteps that regardless of which Go version builds
// this package.
func declPtr(byNodeID map[string]store.NodeDeclarationRecord, nodeID string) *store.NodeDeclarationRecord {
	d, ok := byNodeID[nodeID]
	if !ok {
		return nil
	}
	return &d
}

// mapNodeDeclaration computes B3's four-state discovery verdict. See
// [v1.NodeDeclaration]'s doc comment for what each state means; this
// function is the one place that vocabulary is decided, matching every
// other derived-verdict function in this package (e.g. mapControlPlane,
// deriveInstanceHealth) in being called fresh on every render rather than
// reading a stored verdict — ADR-011: "a verdict is computed on read,
// never stored."
func mapNodeDeclaration(decl *store.NodeDeclarationRecord, latestRun *store.DiscoveryRunRecord) v1.NodeDeclaration {
	if decl == nil {
		return v1.NodeDeclaration{Declared: false, DiscoveryState: "not_applicable"}
	}

	out := v1.NodeDeclaration{
		Declared:                true,
		Label:                   nonEmptyStrPtr(decl.Label),
		Notes:                   nonEmptyStrPtr(decl.Notes),
		DeclaredAt:              strPtr(formatTime(decl.DeclaredAt)),
		DeclaredByPrincipalID:   nonEmptyStrPtr(decl.DeclaredByPrincipalID),
		DeclaredByPrincipalName: nonEmptyStrPtr(decl.DeclaredByPrincipalName),
	}

	// In BOTH "unknown" branches below, this declaration's own last-known
	// positive sighting (if it has ever had one) is still reported rather
	// than left null — B3/ADR-020's "never blank" rule applies to this
	// evidence exactly as it does everywhere else: a run id/time that no
	// longer resolves, or that predates a currently-incomplete run, is
	// still real, non-fabricated history, and reporting it is not the
	// same claim as DiscoveryState itself, which is what actually says
	// whether that history is trustworthy right now.
	if decl.LastDiscoveryRunID != "" {
		out.LastDiscoveryRunID = strPtr(decl.LastDiscoveryRunID)
		out.LastDiscoveredAt = formatTimePtr(decl.LastDiscoveredAt)
	}

	switch {
	case latestRun == nil:
		// "No run has happened, or the run this declaration points at has
		// been pruned" — B3's fourth bullet, deliberately ONE outcome for
		// both root causes: this API cannot and must not guess which,
		// only that no run history is currently available to say anything
		// about absence.
		out.DiscoveryState = "unknown"
		out.DiscoveryReason = strPtr("no discovery run history is available (either no discovery run has ever been performed, or discovery run history has been pruned)")
	case !latestRun.Complete:
		// "The most recent run was incomplete: unknown with the reason,
		// never not_seen." Deliberately does NOT fall back to an older
		// complete run — see this function's own report for why looking
		// further back would be the wrong direction to fail in.
		out.DiscoveryState = "unknown"
		reason := latestRun.Reason
		if reason == "" {
			reason = "the most recent discovery run has not completed"
		}
		out.DiscoveryReason = strPtr(reason)
	case decl.LastDiscoveryRunID == latestRun.ID:
		// LastDiscoveryRunID/LastDiscoveredAt are already set above from
		// decl, and decl.LastDiscoveryRunID == latestRun.ID here by
		// construction, so nothing further to assign.
		out.DiscoveryState = "present"
	default:
		// Not seen BY THE MOST RECENT COMPLETE RUN specifically. DEFECT 8:
		// LastDiscoveryRunID/LastDiscoveredAt above ALREADY report this
		// DECLARATION's own last-seen bookkeeping (or remain null if it has
		// never once been seen) and are deliberately left untouched here —
		// overwriting them with the run that did NOT see it was the bug: a
		// field literally named "lastDiscoveredAt" reporting a time seconds
		// old for a node that has been dark for a week is exactly backwards.
		// The run that failed to see it is named in its own, separately
		// named fields instead, so a client reading lastDiscoveredAt never
		// has to know which of two entirely different claims it is looking
		// at.
		out.DiscoveryState = "not_seen"
		out.DiscoveryReason = strPtr("not seen by the most recent complete discovery run (" + latestRun.ID + ")")
		out.NotSeenAsOfRunID = strPtr(latestRun.ID)
		out.NotSeenAsOfRunFinishedAt = formatTimePtr(latestRun.FinishedAt)
	}

	return out
}

func mapDiscoveryRun(rec store.DiscoveryRunRecord) v1.DiscoveryRun {
	return v1.DiscoveryRun{
		ID:                       rec.ID,
		StartedAt:                formatTime(rec.StartedAt),
		FinishedAt:               formatTimePtr(rec.FinishedAt),
		Complete:                 rec.Complete,
		Reason:                   nonEmptyStrPtr(rec.Reason),
		FoundCount:               rec.FoundCount,
		InitiatedByPrincipalID:   rec.InitiatedByPrincipalID,
		InitiatedByPrincipalName: rec.InitiatedByPrincipalName,
	}
}

// --- POST /api/v1/discovery/runs ---

// handleStartDiscoveryRun serves POST /api/v1/discovery/runs, behind
// [handlers.writeGuard](&identity.ScopeConfigWrite, ...). Declaring what
// hardware exists is configuration — ADR-024 decision 4 defines no
// narrower scope for it, so this is a deliberate choice of the closest
// fit rather than a default: see this file's report.
//
// A run never creates, modifies, or deletes a declaration's own identity
// (RES-008 D6) — it only stamps last-seen evidence on declarations ALREADY
// present (via [DeclarationStore.RecordNodeDiscoverySeen], which the store
// layer itself refuses to use to create a row) and reports proposals for
// what it saw that is not declared. complete/reason implement Step 5's
// rule for the fourth time in this codebase: only a complete run may
// support a claim about absence. A run that fails partway is a row with
// complete=0 and a stated reason — never a missing row, never a silent
// partial success — which is why every failure branch below calls
// [handlers.finishDiscoveryRunFailed] rather than simply returning.
func (h *handlers) handleStartDiscoveryRun(w http.ResponseWriter, r *http.Request) {
	now := h.now()
	ctx := r.Context()
	ac := authFromContext(ctx) // writeGuard has already required ac.ok

	// DEFECT 7a: serialize discovery runs. This coordinator is a single
	// process (ADR-012; SQLite/WAL assumes one writer), so an in-process
	// guard is sufficient — no cross-process coordination is needed. A
	// second concurrent run is refused outright, NEVER queued: queuing
	// would still let two runs' RecordNodeDiscoverySeen passes interleave
	// against the store, just delayed, and interleaving is exactly what let
	// two runs leave the whole fleet not_seen (run A's stamps landing after
	// run B, the run ListDiscoveryRuns' DESC ordering resolves as latest,
	// so every declaration point at A while B is "the most recent run").
	if !h.discoveryRunInFlight.CompareAndSwap(false, true) {
		writeProblem(w, h.logger, now, discoveryRunConflictProblem())
		return
	}
	defer h.discoveryRunInFlight.Store(false)

	runID := uuid.NewString()

	// Dispatch-style audit entry, written BEFORE anything is recorded. This
	// is NOT decision 11's fail-closed default rule keyed off the
	// config:write scope name — ADR-024 decision 11 does not name scopes at
	// all, it distinguishes a coordinator-local state change (same-
	// transaction audit) from a command dispatched to an agent (audit
	// written before dispatch, refused if that write fails). A discovery
	// run's own start/finish bookkeeping is coordinator-local, but this
	// entry is deliberately written dispatch-style anyway, matching the
	// shape ARCHITECTURE §8.1 gives every operator-initiated action with an
	// external effect: if this cannot be attributed, nothing happens at
	// all — not even the discovery_runs row itself.
	if !h.writeAuditOrFail(ctx, w, now, identity.AuditEntry{
		Timestamp: now, PrincipalID: ac.result.Principal.ID, PrincipalName: ac.result.Principal.Name,
		Form: ac.result.Form, CredentialID: ac.result.CredentialID, ClientAddr: h.clientAddr(r),
		Action: "discovery.run.start", Target: runID,
		Kind: identity.AuditDispatch, CommandID: runID,
	}) {
		return
	}

	rec, err := h.deps.Discovery.StartDiscoveryRun(ctx, store.DiscoveryRunRecord{
		ID:                       runID,
		InitiatedByPrincipalID:   ac.result.Principal.ID,
		InitiatedByPrincipalName: ac.result.Principal.Name,
	})
	if err != nil {
		h.auditDiscoveryOutcome(r, h.now(), ac, runID, "failed", "failed to start discovery run")
		h.writeInternalError(w, now, "start discovery run", err)
		return
	}

	// Read what this coordinator already observes. No active probing — see
	// this file's doc comment.
	nodeViews, err := h.deps.Nodes.Snapshot(ctx, now)
	if err != nil {
		h.finishDiscoveryRunFailed(r, ac, runID, "failed to list node inventory: "+err.Error())
		h.writeInternalError(w, now, "list nodes for discovery", err)
		return
	}
	fppViews, err := h.deps.FPP.ListInstances(ctx)
	if err != nil {
		h.finishDiscoveryRunFailed(r, ac, runID, "failed to list FPP instances: "+err.Error())
		h.writeInternalError(w, now, "list fpp instances for discovery", err)
		return
	}
	declared, err := h.deps.Discovery.ListNodeDeclarations(ctx)
	if err != nil {
		h.finishDiscoveryRunFailed(r, ac, runID, "failed to list declared nodes: "+err.Error())
		h.writeInternalError(w, now, "list node declarations for discovery", err)
		return
	}
	declaredSet := make(map[string]struct{}, len(declared))
	for _, d := range declared {
		declaredSet[d.NodeID] = struct{}{}
	}

	// De-duplicate by id (a node id and an FPP instance id share one
	// syntax and, in principle, one namespace — see handleFPPInstance's
	// own comment on that shared syntax) before diffing against declared,
	// so an id observed from both sources is never proposed twice.
	type observedEntity struct {
		id     string
		source string
	}
	var observed []observedEntity
	seen := make(map[string]struct{}, len(nodeViews)+len(fppViews))

	// DEFECT 2: ambiguousDeclaredCount counts DECLARED entities (nodes or
	// FPP instances) for which this run currently has NO evidence either
	// way — inventory.LivenessUnknown for a node, or
	// fppInstanceEvidenceAmbiguous for an FPP instance (never yet polled).
	// LivenessUnknown per inventory/liveness.go's own doc comment covers
	// "no evidence yet", "evidence of unknown age", "evidence aged past the
	// staleness window", AND "contradictory evidence" — none of which is
	// absence, unlike LivenessOffline (a real last-will "online: false"),
	// which remains free to support not_seen exactly as before. Conflating
	// the two — the bug this fixes — meant a broker outage (every node's
	// heartbeat ages past its staleness window at once, flipping every
	// LivenessOnline to LivenessUnknown, never LivenessOffline, since no
	// fresh last-will ever arrives to say so) still finished as
	// complete=true, and complete=true is the license
	// [mapNodeDeclaration]'s not_seen branch acts on: every declared node in
	// the installation flips to not_seen from an evidence source that was
	// simply down, not absent equipment. Scoped to DECLARED entities only
	// (not every row in raw inventory): an ambiguous, UNDECLARED node
	// affects no absence claim either way — it is not proposed (below) and
	// nothing renders not_seen for it — so it must not block this run's
	// completeness for every OTHER declaration that current evidence can
	// speak to honestly.
	var ambiguousDeclaredCount int
	for _, nv := range nodeViews {
		if nv.Liveness == inventory.LivenessUnknown {
			if _, isDeclared := declaredSet[nv.NodeID]; isDeclared {
				ambiguousDeclaredCount++
			}
		}
		// Liveness must be online, not merely "this node id has a
		// persisted nodes row": inventory.Manager never deletes a nodes
		// row (Step 2), so every node that has ever said hello even once
		// would otherwise count as "observed" forever, and RES-008 D6's
		// not_seen state — the entire reason this seam exists — would be
		// unreachable by any real power-off, only by a row that has
		// literally never existed. LivenessOffline is real evidence of
		// absence (a last-will "online: false") and LivenessUnknown is
		// insufficient evidence (see ambiguousDeclaredCount above); NEITHER
		// counts as currently observed, so this run does not count either
		// as observed — an already-declared instance is left alone and
		// flagged on read (mapNodeDeclaration: not_seen for Offline once
		// this run completes, unknown for Unknown via ambiguousDeclaredCount
		// forcing this run incomplete), and an undeclared one is not
		// proposed as new hardware to declare while it cannot currently be
		// confirmed.
		if nv.Liveness != inventory.LivenessOnline {
			continue
		}
		if _, dup := seen[nv.NodeID]; dup {
			continue
		}
		seen[nv.NodeID] = struct{}{}
		observed = append(observed, observedEntity{id: nv.NodeID, source: discoverySourceNode})
	}
	for _, fv := range fppViews {
		// DEFECT 3: a configured FPP instance used to be added to observed
		// unconditionally — apiwiring's fppInstanceLister returns a view for
		// EVERY entry in SHOWMESH_FPP_ENDPOINTS, synthesizing not_collected
		// placeholders when the store holds nothing, so an FPP that is
		// unplugged, or has reported collection_failed on every signal for a
		// week, was counted as observed on every run and rendered "present"
		// forever — the reassuring direction ADR-011 puts first, and the
		// asymmetry with the node branch immediately above (which already
		// required real liveness evidence) was the defect. fppInstanceObserved
		// applies the identical standard: actual collection evidence, never
		// mere configuration.
		if fppInstanceEvidenceAmbiguous(fv) {
			if _, isDeclared := declaredSet[fv.InstanceID]; isDeclared {
				ambiguousDeclaredCount++
			}
		}
		if !fppInstanceObserved(fv) {
			continue
		}
		if _, dup := seen[fv.InstanceID]; dup {
			continue
		}
		seen[fv.InstanceID] = struct{}{}
		observed = append(observed, observedEntity{id: fv.InstanceID, source: discoverySourceFPP})
	}

	var proposals []v1.DiscoveryProposal
	for _, e := range observed {
		if _, isDeclared := declaredSet[e.id]; isDeclared {
			// RecordNodeDiscoverySeen only ever stamps evidence on an
			// ALREADY-declared node — never creates one (RES-008 D6). This
			// is the ONLY write this handler performs against an existing
			// declaration.
			if err := h.deps.Discovery.RecordNodeDiscoverySeen(ctx, e.id, runID, now); err != nil {
				h.finishDiscoveryRunFailed(r, ac, runID, "failed to record discovery evidence for "+strconv.Quote(e.id)+": "+err.Error())
				h.writeInternalError(w, now, "record node discovery seen", err)
				return
			}
			continue
		}
		proposals = append(proposals, v1.DiscoveryProposal{NodeID: e.id, Source: e.source})
	}

	// DEFECT 2: complete is false — an honest "this run cannot assert
	// absence" — whenever ANY declared entity's evidence was ambiguous
	// rather than positive or negative. foundCount still reports len(observed):
	// those entities ARE genuinely, positively confirmed; only the ABSENCE
	// claim (not_seen for everything else) is what this run must not make.
	complete := ambiguousDeclaredCount == 0
	var reason string
	if !complete {
		reason = fmt.Sprintf(
			"%d declared entit%s reported no current evidence either way during this run (e.g. a broker outage, an unreachable coordinator restart before the first FPP poll completed, or evidence aged past its staleness window); a run cannot assert their absence without evidence they are actually offline or unreachable",
			ambiguousDeclaredCount, pluralY(ambiguousDeclaredCount))
	}

	if err := h.deps.Discovery.FinishDiscoveryRun(ctx, runID, complete, reason, int64(len(observed))); err != nil {
		// The run's declarations-seen bookkeeping above already committed;
		// only the terminal row update failed. Still refuse to claim
		// success to the caller — the row is left complete=false (its
		// StartDiscoveryRun-time state), which correctly renders as
		// "unknown", never as a false "complete" claim this handler cannot
		// actually back up.
		h.auditDiscoveryOutcome(r, h.now(), ac, runID, "failed", "failed to finish discovery run")
		h.writeInternalError(w, now, "finish discovery run", err)
		return
	}

	h.auditDiscoveryOutcome(r, h.now(), ac, runID, "succeeded", reason)

	finishedAt := h.now()
	rec.Complete = complete
	rec.FinishedAt = &finishedAt
	rec.Reason = reason
	rec.FoundCount = int64(len(observed))

	jsonWrite(w, v1.DiscoveryRunResponse{
		ServerTime: formatTime(h.now()),
		Run:        mapDiscoveryRun(rec),
		Proposals:  nonNilProposalSlice(proposals),
	})
}

// finishDiscoveryRunFailed marks runID complete=false with reason
// (best-effort — a failure here is logged, never escalated: the caller is
// already reporting the original failure to the client) and writes a
// best-effort failed-outcome audit entry. Called from every failure branch
// in [handlers.handleStartDiscoveryRun] after the row has been started, so
// a run that fails partway is always a row with complete=0 and a stated
// reason — never left dangling at "started, never finished" forever.
func (h *handlers) finishDiscoveryRunFailed(r *http.Request, ac authContext, runID, reason string) {
	if err := h.deps.Discovery.FinishDiscoveryRun(r.Context(), runID, false, reason, 0); err != nil && h.logger != nil {
		h.logger.Warn("api: failed to mark discovery run as incomplete after an earlier failure", "run_id", runID, "error", err)
	}
	h.auditDiscoveryOutcome(r, h.now(), ac, runID, "failed", reason)
}

// auditDiscoveryOutcome writes a best-effort Outcome audit entry
// correlated to runID's earlier Dispatch entry (ADR-024 decision 11's
// dispatch/outcome split). Best-effort, never gating the response, exactly
// like [handlers.handleDeleteSession]'s own outcome write: the effect (or
// its absence) has already happened by the time this is called, so
// refusing to answer the caller over a SECOND audit-write failure would
// only hide what already occurred, not undo it.
func (h *handlers) auditDiscoveryOutcome(r *http.Request, now time.Time, ac authContext, runID, outcome, reason string) {
	entry := identity.AuditEntry{
		Timestamp: now, PrincipalID: ac.result.Principal.ID, PrincipalName: ac.result.Principal.Name,
		Form: ac.result.Form, CredentialID: ac.result.CredentialID, ClientAddr: h.clientAddr(r),
		Action: "discovery.run.start", Target: runID,
		Kind: identity.AuditOutcome, CommandID: runID,
		Outcome:      outcome,
		OutcomeState: string(observation.StateCurrent),
	}
	if reason != "" {
		entry.OutcomeReason = reason
	}
	if err := h.deps.Identity.WriteAudit(r.Context(), entry); err != nil && h.logger != nil {
		h.logger.Warn("api: failed to write discovery.run.start outcome audit entry", "error", err, "command_id", runID)
	}
}

// nonNilProposalSlice matches this API's standing "absent evidence is
// stated, never omitted" rule applied to a collection: a run that found
// nothing to propose still renders "proposals": [], never null.
func nonNilProposalSlice(v []v1.DiscoveryProposal) []v1.DiscoveryProposal {
	if v == nil {
		return []v1.DiscoveryProposal{}
	}
	return v
}

// --- POST /api/v1/nodes/{nodeId}/declaration ---

// handlePromoteNode serves POST /api/v1/nodes/{nodeId}/declaration, behind
// [handlers.writeGuard](&identity.ScopeConfigWrite, ...). Promotes nodeId
// to declared, or — idempotently, per [store.Store.DeclareNode] — updates
// an already-declared node's label and notes. This is a coordinator-local
// state change, so ADR-024 decision 11's same-transaction rule applies in
// full: the declaration write and its audit entry land in one transaction
// via [identity.Service.AuditedWrite], or neither does.
func (h *handlers) handlePromoteNode(w http.ResponseWriter, r *http.Request) {
	now := h.now()
	ctx := r.Context()
	ac := authFromContext(ctx)

	nodeID := r.PathValue("nodeId")
	if err := mqttproto.ValidateNodeID(nodeID); err != nil {
		writeProblem(w, h.logger, now, invalidParameterProblem("nodeId is not a syntactically valid node ID: "+err.Error()))
		return
	}

	// DEFECT 6: Label/Notes are *string so this decode can tell "field
	// absent" (nil — leave whatever is already declared unchanged) apart
	// from "field present as an explicit empty string" (non-nil, pointing
	// at "" — set it to empty). A plain string field cannot represent that
	// distinction at all, which is exactly how `showmeshctl declare
	// roof-01` with no --label flag used to send Label:"" unconditionally
	// and silently erase a previously set label on every re-declare.
	var req v1.DeclareNodeRequest
	if r.ContentLength != 0 {
		dec := json.NewDecoder(io.LimitReader(r.Body, maxDeclarationRequestBodyBytes+1))
		if err := dec.Decode(&req); err != nil {
			writeProblem(w, h.logger, now, invalidParameterProblem(
				"request body, if present, must be JSON matching {\"label\":string|null,\"notes\":string|null}, "+
					"with an absent or null field leaving that field's currently declared value unchanged"))
			return
		}
	}

	// DEFECT 1: a promotion made from a LIVE proposal must be able to
	// record the evidence that produced it, or the primary workflow (run
	// discovery, click Declare) immediately contradicts itself — a node
	// that discovery is the sole reason this coordinator knows about
	// renders "not seen by the most recent discovery run" the instant it
	// is declared.
	//
	// Design chosen, deliberately, over the alternative the review named
	// (the client passing the run id it is promoting from): this
	// coordinator does not persist which entities any one run's PROPOSALS
	// named — RecordNodeDiscoverySeen only ever stamps an ALREADY-declared
	// node (see this file's own top-of-file doc comment) — so there is
	// nothing here to verify a client-supplied run id against beyond
	// "does this run id exist", which would let a client assert evidence
	// this coordinator never independently confirmed. Instead, this
	// handler re-derives "is nodeID observed RIGHT NOW", using the
	// IDENTICAL predicate handleStartDiscoveryRun itself uses
	// (discoveryObservedNow), and stamps the latest discovery run's
	// identity ONLY when that re-check confirms presence. That keeps the
	// evidence entirely server-computed: the common case (declare
	// immediately after discovery proposed it) is stamped correctly
	// because the entity is, in fact, still observed a few seconds later;
	// the rare case (it went away in between) is honestly left unstamped,
	// rendering not_seen or unknown rather than a fabricated "present".
	//
	// latestRun is fetched ONCE, before the write, and reused for both the
	// stamping decision and the final response below — never re-fetched
	// after the write completes. Refetching would open a window where a
	// brand-new run completes between the stamp decision and the render,
	// making decl.LastDiscoveryRunID (just stamped against the run seen
	// here) mismatch a NEWER latestRun the response renders against,
	// reproducing this exact defect inside its own fix.
	observedNow, err := h.discoveryObservedNow(ctx, nodeID)
	if err != nil {
		h.writeInternalError(w, now, "check current observation state for declare", err)
		return
	}
	latestRun, err := latestDiscoveryRun(ctx, h.deps.Discovery)
	if err != nil {
		h.writeInternalError(w, now, "fetch latest discovery run for declare", err)
		return
	}

	var declared store.NodeDeclarationRecord
	err = h.deps.Identity.AuditedWrite(ctx, func(ctx context.Context, tx *store.Tx) (identity.AuditEntry, error) {
		// DEFECT 6, continued: read the CURRENTLY declared label/notes
		// (if any) inside this same transaction, so a concurrent write
		// cannot land between this read and the DeclareNode call below,
		// and fall back to those rather than "" wherever the request left
		// a field unset. A brand-new declaration (ErrNodeDeclarationNotFound)
		// has nothing to preserve, so "" remains correct there — identical
		// to this handler's behavior before this fix.
		label, notes := "", ""
		if existing, err := tx.GetNodeDeclaration(ctx, nodeID); err == nil {
			label, notes = existing.Label, existing.Notes
		} else if !errors.Is(err, store.ErrNodeDeclarationNotFound) {
			return identity.AuditEntry{}, err
		}
		if req.Label != nil {
			label = *req.Label
		}
		if req.Notes != nil {
			notes = *req.Notes
		}

		rec := store.NodeDeclarationRecord{
			NodeID:                  nodeID,
			Label:                   label,
			Notes:                   notes,
			DeclaredByPrincipalID:   ac.result.Principal.ID,
			DeclaredByPrincipalName: ac.result.Principal.Name,
		}
		if observedNow && latestRun != nil {
			// Only consulted by [store.Store.DeclareNode] on the INSERT
			// (brand-new declaration) path — see its own doc comment. A
			// re-declare (already-declared node) leaves existing discovery
			// evidence exactly as DEFECT 6's fix above already requires.
			rec.LastDiscoveryRunID = latestRun.ID
			stampAt := now
			rec.LastDiscoveredAt = &stampAt
		}

		out, err := tx.DeclareNode(ctx, rec)
		if err != nil {
			return identity.AuditEntry{}, err
		}
		declared = out
		return identity.AuditEntry{
			Timestamp: now, PrincipalID: ac.result.Principal.ID, PrincipalName: ac.result.Principal.Name,
			Form: ac.result.Form, CredentialID: ac.result.CredentialID, ClientAddr: h.clientAddr(r),
			Action: "node.declare", Target: nodeID,
			Params: map[string]any{"label": label, "notes": notes},
			Kind:   identity.AuditAdmin,
		}, nil
	})
	if err != nil {
		// Whether this is [identity.ErrAuditWrite] (the audit append
		// itself failed) or fn's own error (e.g. a store failure inside
		// DeclareNode), ADR-024 decision 11's same-transaction rule has
		// already done its job by the time control reaches here: on ANY
		// error, [identity.Service.AuditedWrite] has rolled the whole
		// transaction back, so the declaration this handler was about to
		// create/update is absent afterwards exactly as if this request
		// never happened. Both cases are reported identically as an
		// internal error — matching handleClaimBootstrap/
		// handleCreateSession's identical posture for the same call.
		h.writeInternalError(w, now, "declare node", err)
		return
	}

	jsonWrite(w, v1.NodeDeclarationResponse{
		ServerTime:  formatTime(h.now()),
		Declaration: mapNodeDeclaration(&declared, latestRun),
	})
}

// --- DELETE /api/v1/nodes/{nodeId}/declaration ---

// handleDeleteNodeDeclaration serves DELETE /api/v1/nodes/{nodeId}/declaration,
// behind [handlers.writeGuard](&identity.ScopeConfigWrite, ...). Requires
// an explicit {"confirm":true} body — BUILD-PLAN Step 7 seam B B2: "so a
// mis-issued call cannot quietly remove inventory." The UI's own
// confirmation dialog is in addition to this, never instead of it: a
// script or a curl call gets no shortcut around this check just because it
// is not a browser. Audited the same way as promote, via
// [identity.Service.AuditedWrite] — ADR-024 decision 11's same-transaction
// rule in full.
//
// Discovery itself never calls this — see discovery.go's own doc comment
// and [store.Store.DeleteNodeDeclaration]'s: this is the one and only path
// that removes a declaration, and it is always a direct, confirmed
// operator action.
func (h *handlers) handleDeleteNodeDeclaration(w http.ResponseWriter, r *http.Request) {
	now := h.now()
	ctx := r.Context()
	ac := authFromContext(ctx)

	nodeID := r.PathValue("nodeId")
	if err := mqttproto.ValidateNodeID(nodeID); err != nil {
		writeProblem(w, h.logger, now, invalidParameterProblem("nodeId is not a syntactically valid node ID: "+err.Error()))
		return
	}

	var req v1.DeleteNodeDeclarationRequest
	dec := json.NewDecoder(io.LimitReader(r.Body, maxDeclarationRequestBodyBytes+1))
	if err := dec.Decode(&req); err != nil {
		writeProblem(w, h.logger, now, invalidParameterProblem(
			"request body must be JSON matching {\"confirm\":true}"))
		return
	}
	if !req.Confirm {
		writeProblem(w, h.logger, now, invalidParameterProblem(
			"deleting a node declaration requires an explicit {\"confirm\":true} body, so a mis-issued call cannot quietly remove inventory"))
		return
	}

	err := h.deps.Identity.AuditedWrite(ctx, func(ctx context.Context, tx *store.Tx) (identity.AuditEntry, error) {
		if err := tx.DeleteNodeDeclaration(ctx, nodeID); err != nil {
			return identity.AuditEntry{}, err
		}
		return identity.AuditEntry{
			Timestamp: now, PrincipalID: ac.result.Principal.ID, PrincipalName: ac.result.Principal.Name,
			Form: ac.result.Form, CredentialID: ac.result.CredentialID, ClientAddr: h.clientAddr(r),
			Action: "node.declaration.delete", Target: nodeID,
			Kind: identity.AuditAdmin,
		}, nil
	})
	if err != nil {
		if errors.Is(err, store.ErrNodeDeclarationNotFound) {
			writeProblem(w, h.logger, now, resourceNotFoundProblem("no declared node with id "+strconv.Quote(nodeID)))
			return
		}
		h.writeInternalError(w, now, "delete node declaration", err)
		return
	}

	w.WriteHeader(http.StatusNoContent)
}
