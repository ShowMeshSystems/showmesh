package api

import (
	"fmt"
	"net/url"
	"sort"
	"time"

	v1 "github.com/showmeshsystems/showmesh/internal/coordinator/api/v1"
	"github.com/showmeshsystems/showmesh/internal/coordinator/inventory"
	"github.com/showmeshsystems/showmesh/internal/coordinator/store"
	"github.com/showmeshsystems/showmesh/pkg/observation"
)

// This file is the one place domain values (pkg/observation.Observation,
// inventory.NodeView, the store's records, and this package's own
// EventRecord/FPPInstanceView/CollectorState) become v1 wire types. See the
// v1 package doc comment for why that boundary exists at all, and this
// package's doc comment for why the mapping lives here rather than in v1
// itself.

// formatTime renders t as RFC 3339 with an explicit offset (contract
// section 6.2: "always including a timezone"). time.RFC3339Nano keeps
// sub-second precision when t has any, and reduces to whole seconds when it
// does not — both are valid RFC 3339, and the pinned examples in contract
// section 6.10 show both forms.
func formatTime(t time.Time) string {
	return t.Format(time.RFC3339Nano)
}

// formatTimePtr renders t as an RFC 3339 string pointer, or nil if t is
// nil. Used everywhere a domain *time.Time (meaning "unknown", never
// "zero") becomes a v1 field that must render JSON null for that same
// unknown case — never an empty string, and never the zero time formatted
// as if it meant something.
func formatTimePtr(t *time.Time) *string {
	if t == nil {
		return nil
	}
	s := formatTime(*t)
	return &s
}

func strPtr(s string) *string { return &s }

// nonEmptyStrPtr is strPtr but returns nil for an empty string, matching
// the rule that an optional string field renders as null rather than "",
// e.g. [v1.Evidence.Unit].
func nonEmptyStrPtr(s string) *string {
	if s == "" {
		return nil
	}
	return &s
}

// --- Evidence: the one envelope every observation-bearing field uses ---

// mapEvidence renders o through the contract section 6.3 envelope, at the
// given now. This is the single function every evidence-bearing field in
// this API goes through — node hello/lastWill/heartbeat (via the
// synthesized observations below), and every FPP-collected observation —
// so contract section 6.3's rules ("observedAt is null whenever it is
// unknown... reason is non-null whenever state is not current... value is
// null for every absence state") are enforced in exactly one place.
func mapEvidence(o observation.Observation, now time.Time) v1.Evidence {
	state := o.StateAt(now)

	ev := v1.Evidence{
		Signal:      string(o.Signal),
		Value:       o.Value,
		Unit:        nonEmptyStrPtr(o.Unit),
		State:       string(state),
		ObservedAt:  formatTimePtr(o.ObservedAt),
		CollectedAt: collectedAtForWire(o, state),
		Source:      o.Source,
		Quality:     string(o.Quality),
	}

	if o.ValidFor > 0 {
		secs := int64(o.ValidFor / time.Second)
		ev.ValidForSeconds = &secs
	}

	if state != observation.StateCurrent {
		reason := evidenceReason(o, state, now)
		ev.Reason = &reason
	}

	return ev
}

// collectedAtForWire renders o.CollectedAt as an RFC 3339 string pointer,
// except when state is [observation.StateNotCollected]: that state means,
// per its own doc comment, "no attempt has been made" — the collector is
// not configured, is disabled, or has not completed its first poll — so
// o.CollectedAt in that case is never itself evidence that a collection
// happened. It is only ever a fallback value a caller supplied to satisfy
// [pkg/observation.Observation.Validate]'s non-zero-CollectedAt invariant:
// for node evidence, the node row's own last-touched bookkeeping time (see
// helloObservation and its siblings below); for an FPP instance that has
// never been polled, an arbitrary placeholder with no meaning at all (see
// internal/coordinator/apiwiring.go's notYetPolledObservations). Rendering
// either as "when this was collected" is exactly the [v1.Evidence.ObservedAt]
// fabrication contract section 3.3 already forbids, one field over — Step 3
// review finding 3.6. This is the one place that mask is applied, so
// CollectedAt is never accidentally non-null for this one state anywhere
// this package renders an [Evidence] envelope.
//
// Masking here, rather than changing what CollectedAt the constructors in
// this file store on the domain Observation, is also what keeps a
// never-attempted signal's rendered JSON byte-identical from one hub
// render tick to the next regardless of what placeholder time a caller
// happened to pass in: [Hub.updateRendered] diffs rendered bytes (contract
// section 6.5), and a CollectedAt that changed on every call — the bug
// [helloObservation]'s own doc comment already describes fixing once —
// would defeat that the moment any caller reached for time.Now() as its
// fallback, which is exactly what a never-polled FPP instance has no other
// reasonable value to reach for.
func collectedAtForWire(o observation.Observation, state observation.State) *string {
	if state == observation.StateNotCollected {
		return nil
	}
	s := formatTime(o.CollectedAt)
	return &s
}

// evidenceReason returns the non-null reason contract section 6.3 requires
// whenever state is not "current". o.Reason already satisfies this for
// every absence state ([observation.Observation.Validate] rejects an empty
// Reason on an absence), but pkg/observation's Measured and
// MeasuredUnknownAge constructors have no Reason parameter at all — a
// value can be "stale" or "unknown_age" and still carry o.Reason == "",
// because those two states are derived from time and from the constructor
// used, not authored by the collector. This function is where that gap is
// closed for the wire: it prefers an authored o.Reason when one exists, and
// synthesizes a plain-language explanation for the two time-derived states
// when it does not.
func evidenceReason(o observation.Observation, state observation.State, now time.Time) string {
	if o.Reason != "" {
		return o.Reason
	}
	switch state {
	case observation.StateUnknownAge:
		return "observation time is unknown (e.g. a retained MQTT delivery replayed on reconnect)"
	case observation.StateStale:
		age := now.Sub(*o.ObservedAt).Round(time.Second)
		return fmt.Sprintf("value is %s old, past its %s validity window", age, o.ValidFor)
	default:
		// Unreachable: StateNotCollected/StateCollectionFailed/StateUnsupported
		// are exactly the absence states Validate requires a non-empty Reason
		// for, and the o.Reason != "" branch above already returns for those.
		// Kept as an honest fallback, not a panic, in case a hand-built
		// Observation skipped Validate — see that method's own defensive
		// branches for the same posture.
		return "no current value"
	}
}

// mustObservation panics if err is non-nil. It is used only at call sites
// in this file where every argument to the [observation.Measured],
// [observation.MeasuredUnknownAge], or [observation.NotCollected] call is
// a fixed constant or a value already known non-empty (a resource ID that
// came from an existing store record, a fixed signal, a fixed non-empty
// reason string) — a non-nil err at one of those call sites means this
// mapping code itself violated pkg/observation's invariants, a programming
// error to fix, not a runtime condition to render as unknown evidence.
// Mirrors regexp.MustCompile's posture for the same reason.
func mustObservation(o observation.Observation, err error) observation.Observation {
	if err != nil {
		panic(fmt.Sprintf("api: internal observation construction invariant violated: %v", err))
	}
	return o
}

// Signal IDs and source name for the three evidence kinds every node
// carries. Namespaced under node.*, per contract section 7.
const (
	signalNodeHello     = observation.SignalID("node.hello")
	signalNodeLastWill  = observation.SignalID("node.control_plane.last_will")
	signalNodeHeartbeat = observation.SignalID("node.heartbeat")

	// sourceInventory names the collector these three evidence kinds come
	// from: internal/coordinator/inventory's MQTT subscriptions, not a
	// polled collector. It appears as [v1.Evidence.Source] the same way
	// "fpp-rest" does for FPP-collected observations.
	sourceInventory = "mqtt-inventory"
)

// nodeResourceRef builds the [observation.ResourceRef] every node evidence
// observation below is "about".
func nodeResourceRef(nodeID string) observation.ResourceRef {
	return observation.ResourceRef{Kind: observation.ResourceNode, ID: nodeID}
}

// helloObservation synthesizes an [observation.Observation] from a node's
// stored hello evidence (store.HelloRecord), so it can go through
// [mapEvidence] exactly like every other evidence source. Value is a bare
// true — deliberately not a copy of the hello's content — because that
// content (label, platform, agentVersion, bootId, startedAt) already has
// its own dedicated fields on [v1.Node]; this evidence signal's only job is
// to say whether, and as of when, a hello has been confirmed for this
// node. See this package's report for why this is a Task D design choice
// contract section 6.10 leaves open rather than a pinned shape.
//
// fallbackCollectedAt is used as CollectedAt whenever there is no per-
// delivery timestamp to use instead (the nil-record and unknown-age
// branches below); callers pass [inventory.NodeView.UpdatedAt]. This is
// NOT "now" — see this function's own history for why that distinction is
// load-bearing, not stylistic: an earlier version of this file stamped
// CollectedAt from the render's own current time on every call, which
// silently defeated internal/coordinator/api's SSE hub entirely.
// [Hub.updateRendered] detects change by comparing this function's
// rendered JSON byte-for-byte against what it last published — see
// contract section 6.5 — and CollectedAt is part of that JSON. A value
// that advances on every render (a hub tick fires every 5 seconds by
// default) makes every node with a hello ever observed look "changed" on
// every tick, forever, which is indistinguishable from the hub genuinely
// having a change to report. Found via this task's own real-process
// integration harness — a synthetic stream never re-renders on a timer the
// way the real Hub.Run does, so no unit test caught it — watching a
// single, completely idle node keep re-appearing as node.changed every few
// seconds with nothing having changed about it at all.
func helloObservation(nodeID string, rec *store.HelloRecord, fallbackCollectedAt time.Time) observation.Observation {
	res := nodeResourceRef(nodeID)
	if rec == nil {
		return mustObservation(observation.NotCollected(res, signalNodeHello,
			"no hello has ever been observed for this node",
			observation.WithSource(sourceInventory), observation.WithCollectedAt(fallbackCollectedAt)))
	}
	if rec.ObservedAt == nil {
		return mustObservation(observation.MeasuredUnknownAge(res, signalNodeHello, true,
			observation.WithSource(sourceInventory), observation.WithCollectedAt(fallbackCollectedAt)))
	}
	// A live delivery's collection and observation coincide exactly:
	// internal/coordinator/inventory.Manager.classify stamps ObservedAt
	// from its own receipt-time clock the instant the message arrives, so
	// that same instant is also correctly "when this was collected" — and,
	// crucially, it is a value fixed at storage time rather than one this
	// function invents afresh on every call.
	return mustObservation(observation.Measured(res, signalNodeHello, true, *rec.ObservedAt,
		observation.WithSource(sourceInventory), observation.WithCollectedAt(*rec.ObservedAt)))
}

// lastWillObservation is [helloObservation]'s counterpart for a node's
// last-will/online-state evidence (store.LWTRecord). Unlike hello, the
// record's own content IS the value: Online is exactly what this signal
// exists to report, with no separate dedicated field elsewhere the way
// hello's content has on [v1.Node] (controlPlane.state is a DERIVED
// liveness verdict computed by internal/coordinator/inventory from this
// evidence plus health evidence together — see [mapNode] — not a restating
// of this evidence alone).
//
// See [helloObservation]'s doc comment for fallbackCollectedAt and why it
// must never be "now".
func lastWillObservation(nodeID string, rec *store.LWTRecord, fallbackCollectedAt time.Time) observation.Observation {
	res := nodeResourceRef(nodeID)
	if rec == nil {
		return mustObservation(observation.NotCollected(res, signalNodeLastWill,
			"no last-will evidence has ever been observed for this node",
			observation.WithSource(sourceInventory), observation.WithCollectedAt(fallbackCollectedAt)))
	}
	if rec.ObservedAt == nil {
		return mustObservation(observation.MeasuredUnknownAge(res, signalNodeLastWill, rec.Online,
			observation.WithSource(sourceInventory), observation.WithCollectedAt(fallbackCollectedAt)))
	}
	return mustObservation(observation.Measured(res, signalNodeLastWill, rec.Online, *rec.ObservedAt,
		observation.WithSource(sourceInventory), observation.WithCollectedAt(*rec.ObservedAt)))
}

// heartbeatObservation is [helloObservation]'s counterpart for a node's
// health heartbeat (store.HealthRecord). Value is the agent's self-reported
// AgentState string verbatim (which mqttproto.HealthPayload deliberately
// leaves unconstrained — see its doc comment — so this package does not
// invent a vocabulary for it either).
//
// ValidFor is deliberately left at zero (never expires on its own) rather
// than set to internal/coordinator/inventory.StalenessWindow: contract
// section 3.4 requires that the heartbeat staleness window, an unmeasured
// ShowMesh guess, not be baked into the API as settled behavior. The
// node's one actual liveness verdict is [v1.ControlPlane.State], computed
// by inventory.deriveLiveness from this same evidence using that window
// internally; duplicating the window's effect into this evidence's own
// current/stale state would let the two disagree (evidence "current" but
// controlPlane "offline", or the reverse) for no reason a client could act
// on differently than it already can from controlPlane alone.
//
// See [helloObservation]'s doc comment for fallbackCollectedAt and why it
// must never be "now".
func heartbeatObservation(nodeID string, rec *store.HealthRecord, fallbackCollectedAt time.Time) observation.Observation {
	res := nodeResourceRef(nodeID)
	if rec == nil {
		return mustObservation(observation.NotCollected(res, signalNodeHeartbeat,
			"no health heartbeat has ever been observed for this node",
			observation.WithSource(sourceInventory), observation.WithCollectedAt(fallbackCollectedAt)))
	}
	if rec.ObservedAt == nil {
		return mustObservation(observation.MeasuredUnknownAge(res, signalNodeHeartbeat, rec.AgentState,
			observation.WithSource(sourceInventory), observation.WithCollectedAt(fallbackCollectedAt)))
	}
	return mustObservation(observation.Measured(res, signalNodeHeartbeat, rec.AgentState, *rec.ObservedAt,
		observation.WithSource(sourceInventory), observation.WithCollectedAt(*rec.ObservedAt)))
}

// mapControlPlane derives [v1.ControlPlane] from a node's
// inventory-computed [inventory.Liveness] and reason. Contract section 3.2,
// pinned by name: online/offline/unknown, never "state"/"online"/"status".
func mapControlPlane(liveness inventory.Liveness, reason string) v1.ControlPlane {
	cp := v1.ControlPlane{State: string(liveness)}
	if liveness != inventory.LivenessOnline {
		cp.Reason = nonEmptyStrPtr(reason)
	}
	return cp
}

// nodeEvidenceObservations returns the same three
// [observation.Observation] values [mapNode] builds nv's evidence envelope
// from — hello, last-will, and heartbeat — as a plain slice, for GET
// /api/v1/observations to union in alongside the FPP collector's persisted
// rows (see handleObservations in handlers.go).
//
// This is Step 3 review finding 3.1's fix, and the fix is deliberately
// "synthesize again at read time", not "also write these into the
// observations table": the store holds evidence and never a derived
// verdict (see internal/coordinator/store's package doc comment), and node
// evidence is exactly this — hello/lastWill/heartbeat rows already exist in
// their own tables, so writing a second copy into observations would be a
// second source of truth for the same three facts that could drift from
// the first, and would reopen the CollectedAt-restamping bug
// [helloObservation]'s own doc comment describes fixing once already this
// step: a write path that runs on every render tick would need its own
// non-"now" timestamp discipline all over again, for data this package
// already has a correct read-time source for.
func nodeEvidenceObservations(nv inventory.NodeView) []observation.Observation {
	return []observation.Observation{
		helloObservation(nv.NodeID, nv.Hello, nv.UpdatedAt),
		lastWillObservation(nv.NodeID, nv.LWT, nv.UpdatedAt),
		heartbeatObservation(nv.NodeID, nv.Health, nv.UpdatedAt),
	}
}

// mapNode renders one [inventory.NodeView] as a [v1.Node], at the given
// now. This is the one function that must produce byte-identical output
// whether called for GET /api/v1/nodes, GET /api/v1/nodes/{nodeId}, the
// snapshot's nodes list, or a node.changed stream event — contract section
// 6.4's "each *.changed event carries the resource's full current
// representation, identical in shape to its element in the snapshot".
func mapNode(nv inventory.NodeView, now time.Time) v1.Node {
	n := v1.Node{
		NodeID:       nv.NodeID,
		FirstSeenAt:  formatTime(nv.FirstSeenAt),
		UpdatedAt:    formatTime(nv.UpdatedAt),
		Capabilities: []v1.Capability{},
		ControlPlane: mapControlPlane(nv.Liveness, nv.LivenessReason),
		Evidence: v1.NodeEvidence{
			Hello:     mapEvidence(helloObservation(nv.NodeID, nv.Hello, nv.UpdatedAt), now),
			LastWill:  mapEvidence(lastWillObservation(nv.NodeID, nv.LWT, nv.UpdatedAt), now),
			Heartbeat: mapEvidence(heartbeatObservation(nv.NodeID, nv.Health, nv.UpdatedAt), now),
		},
	}

	if nv.Hello != nil {
		n.Label = nonEmptyStrPtr(nv.Hello.Label)
		n.Platform = nonEmptyStrPtr(nv.Hello.Platform)
		n.AgentVersion = nonEmptyStrPtr(nv.Hello.AgentVersion)
		n.BootID = nonEmptyStrPtr(nv.Hello.BootID)
		if !nv.Hello.StartedAt.IsZero() {
			n.StartedAt = strPtr(formatTime(nv.Hello.StartedAt))
		}
		n.Capabilities = make([]v1.Capability, 0, len(nv.Hello.Capabilities))
		for _, c := range nv.Hello.Capabilities {
			n.Capabilities = append(n.Capabilities, v1.Capability{
				ID: string(c.ID), Version: c.Version, Attributes: c.Attributes,
			})
		}
	}

	return n
}

// --- FPP instances ---

// sanitizeEndpoint strips userinfo from endpoint (a config value that may
// carry "http://user:pass@host" credentials) before it is ever rendered on
// the wire, per contract section 6.10 ("endpoint never includes userinfo;
// strip credentials before rendering"). An endpoint that fails to parse as
// a URL is returned unchanged rather than dropped: this package renders
// whatever the collector configured, and a malformed value is the
// operator's own configuration to fix, not something this handler should
// hide.
func sanitizeEndpoint(endpoint string) string {
	u, err := url.Parse(endpoint)
	if err != nil || u.User == nil {
		return endpoint
	}
	u.User = nil
	return u.String()
}

// deriveInstanceHealth aggregates an FPP instance's collected observations
// into one [observation.Health]. Step 3 has very little basis for
// "degraded" or "failed" (contract section 4's closing note): a REST
// collector reading FPP's own status either has current evidence or it
// does not, and nothing at this layer knows enough about any individual
// signal's meaning to call it unhealthy on its own (fpp.multisync.enabled
// == false is information, not a fault). So every observation contributes
// [observation.HealthHealthy] when current and [observation.HealthUnknown]
// otherwise (via [observation.DeriveHealth], which enforces that gate
// structurally), and the instance's aggregate is the worst of those via
// [observation.AggregateHealth] with every member marked critical.
//
// An instance with zero observations is [observation.HealthUnknown]:
// [observation.AggregateHealth] itself now returns HealthUnknown for an
// empty (or entirely suppressed/non-critical-unknown) member list — it
// used to return the vacuously-healthy HealthHealthy for that case, which
// this function's own comment used to call out as a deliberate
// divergence, but that was AggregateHealth's bug to fix, not a case this
// caller needed to keep working around. There is accordingly no explicit
// "len(obs) == 0" guard here any more: `members` is simply an empty slice
// in that case, and AggregateHealth(nil-or-empty) already answers
// HealthUnknown on its own — the correct, and now the honest, default
// either way.
func deriveInstanceHealth(obs []observation.Observation, now time.Time) observation.Health {
	members := make([]observation.AggregateMember, 0, len(obs))
	for _, o := range obs {
		h := observation.DeriveHealth(o, now, func(any) observation.Health {
			return observation.HealthHealthy
		})
		members = append(members, observation.AggregateMember{Health: h, Critical: true})
	}
	return observation.AggregateHealth(members)
}

// mapFPPInstance renders one [FPPInstanceView] as a [v1.FPPInstance].
func mapFPPInstance(fv FPPInstanceView, now time.Time) v1.FPPInstance {
	obsEvidence := make([]v1.Evidence, 0, len(fv.Observations))
	for _, o := range fv.Observations {
		obsEvidence = append(obsEvidence, mapEvidence(o, now))
	}

	return v1.FPPInstance{
		InstanceID:    fv.InstanceID,
		Endpoint:      sanitizeEndpoint(fv.Endpoint),
		Health:        string(deriveInstanceHealth(fv.Observations, now)),
		Observations:  obsEvidence,
		LastPollAt:    formatTimePtr(fv.LastPollAt),
		LastPollError: fv.LastPollError,
	}
}

// --- Observations (flat list) ---

// mapObservationEntry renders one [observation.Observation] as a
// [v1.ObservationEntry] for GET /api/v1/observations.
func mapObservationEntry(o observation.Observation, now time.Time) v1.ObservationEntry {
	return v1.ObservationEntry{
		Resource: v1.ResourceRef{Kind: string(o.Resource.Kind), ID: o.Resource.ID},
		Evidence: mapEvidence(o, now),
	}
}

// sortObservations orders obs by (resource kind, resource ID, signal) —
// the same ordering [store.Store.ListObservations] already applies via its
// own `ORDER BY resource_kind, resource_id, signal` — so that
// handleObservations' union of the store's persisted rows with
// [nodeEvidenceObservations]' synthesized ones (finding 3.1) still comes
// back in one stable, deterministic order regardless of which of the two
// sources a given entry came from, rather than "store rows, then whichever
// nodes happened to be appended after".
func sortObservations(obs []observation.Observation) {
	sort.Slice(obs, func(i, j int) bool {
		a, b := obs[i], obs[j]
		if a.Resource.Kind != b.Resource.Kind {
			return a.Resource.Kind < b.Resource.Kind
		}
		if a.Resource.ID != b.Resource.ID {
			return a.Resource.ID < b.Resource.ID
		}
		return a.Signal < b.Signal
	})
}

// --- Events ---

// mapEvent renders one [EventRecord] as a [v1.Event].
func mapEvent(e EventRecord) v1.Event {
	details := e.Details
	if details == nil {
		details = map[string]any{}
	}
	return v1.Event{
		Seq:           e.Seq,
		RecordedAt:    formatTime(e.RecordedAt),
		OccurredAt:    formatTimePtr(e.OccurredAt),
		Source:        e.Source,
		Resource:      v1.ResourceRef{Kind: string(e.Resource.Kind), ID: e.Resource.ID},
		Category:      e.Category,
		Severity:      e.Severity,
		Summary:       e.Summary,
		Details:       details,
		CorrelationID: e.CorrelationID,
	}
}

// --- Collector status ---

func mapCollectorState(c CollectorState) v1.CollectorStatus {
	return v1.CollectorStatus{ID: c.ID, State: c.State, Reason: c.Reason}
}
