package api

import (
	"fmt"
	"net/url"
	"sort"
	"strings"
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

	reason := ""
	switch {
	case state != observation.StateCurrent:
		reason = evidenceReason(o, state, now)
	case o.Reason != "":
		// A state can be "current" (fresh evidence, per o.StateAt's own
		// freshness clock) while o.Reason still carries an authored
		// explanation of what the VALUE means — e.g.
		// applySupersededVerdict's superseded relabeling in
		// rendersuperseded.go, which never touches ObservedAt/ValidFor and
		// so never changes this freshness state. Dropping Reason here
		// because state reads "current" silently discarded that
		// explanation on the wire.
		reason = o.Reason
	}
	if reason != "" {
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
		// Deliberately NOT a computed age (contract section 6.2 forbids a
		// precomputed age anywhere in a payload, in as many words). This
		// used to be fmt.Sprintf("value is %s old, past its %s validity
		// window", now.Sub(*o.ObservedAt).Round(time.Second), o.ValidFor)
		// — reading as harmless because it only touches a "reason" string,
		// but `now` is the RENDER clock, so this reason changed on every
		// single render tick for any instance carrying even one stale
		// signal, which defeated Hub.updateRendered's byte-for-byte diff
		// (contract section 6.5) and re-broadcast that instance forever at
		// tick rate — measured against the real fleet at ~43 KB/s per
		// connected browser on an otherwise idle system (Step 5 review
		// finding 2). o.ValidFor is fixed per observation (not
		// clock-derived), so including it is safe; the wire already carries
		// observedAt, validForSeconds, and serverTime, and the UI derives
		// the actual age from those at render time (EvidenceValue.tsx) —
		// this reason only needs to explain WHY state is stale, not restate
		// a number the client can already compute more precisely itself.
		// See fppInstanceDiffProjection in stream.go for the other half of
		// this same fix (masking observedAt/source churn for
		// otherwise-unchanged current-state evidence).
		return fmt.Sprintf("value has not been reconfirmed within its %s validity window", o.ValidFor)
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
//
// decl and latestRun are BUILD-PLAN Step 7 seam B's declaration context
// (RES-008 D2/D6): decl is nv's own node_declarations row, or nil for a
// node nobody has ever declared; latestRun is the single most recent
// [store.DiscoveryRunRecord] across the whole coordinator (or nil if none
// exists), the same value for every node rendered in one pass — see
// discovery.go's fetchDeclarationContext, which every caller of mapNode
// fetches exactly once per render/response rather than once per node.
//
// renderObs is Track B seam B2b's addition: whatever
// [NodeRenderLister.NodeRenderObservations] currently holds for nv.NodeID.
// This is a plain pass-through render, not a per-node filter: most
// elements' Resource names a SURFACE nv.NodeID reported on, but the two
// node.multisync.* signals (finding 7) name nv.NodeID directly, since one
// MultiSync listener serves every surface a node supervises. nil (no
// render evidence at all) renders as an empty array, never an omitted
// field, matching every other "absent evidence is stated, never omitted"
// collection on [v1.Node].
//
// audioObs is [NodeAudioLister.NodeAudioObservations]'s identical
// pass-through, one dependency over.
func mapNode(nv inventory.NodeView, now time.Time, decl *store.NodeDeclarationRecord, latestRun *store.DiscoveryRunRecord, renderObs, audioObs []observation.Observation) v1.Node {
	render := make([]v1.ObservationEntry, 0, len(renderObs))
	for _, o := range renderObs {
		render = append(render, mapObservationEntry(o, now))
	}
	audio := make([]v1.ObservationEntry, 0, len(audioObs))
	for _, o := range audioObs {
		audio = append(audio, mapObservationEntry(o, now))
	}

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
		Declaration: mapNodeDeclaration(decl, latestRun),
		Render:      render,
		Audio:       audio,
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

// healthCriticalSignals is the fixed, named set of FPP signals whose value
// drives an FPP instance's aggregate [observation.Health], per Step 5
// contract section 5.3. Every signal NOT in this map is informational and
// contributes nothing to the verdict — it may still render prominently in
// [v1.FPPInstance.Observations], it simply never moves Health. This
// replaces Step 3's "every observation is a critical member, any value is
// healthy" rule (see this function's git history), which Step 5 would
// otherwise have broken two different ways simultaneously: the many
// legitimately-unsupported signals Step 5 adds (remote-mode playback,
// smart-receiver current, an absent pixelCount) would have dragged every
// real host to unknown forever, and fpp.power.bad == true would have kept
// contributing HealthHealthy because the old mapper never looked at the
// value at all.
//
// Deliberately NOT here: fpp.warnings.count / fpp.warnings.summary (or any
// fpp.warnings.* signal). FPP's own warning list mixes purely informational
// entries ("A Log Level is set to Debug") with entries that look like real
// faults ("Cannot Ping ArtNet Channel Data Target") in the same untyped
// string list, with no structured severity this coordinator can read.
// Classifying those strings into a health verdict would be ShowMesh
// inventing a verdict from text it does not understand — exactly what
// ADR-011 forbids ("never fabricate a confident verdict from evidence that
// does not support one"). Warnings are surfaced prominently by the
// Operator UI and never drive this field; do not add a warnings entry to
// this map without first getting a structured (not string-classified)
// signal for whatever specific fault it is meant to catch.
//
// This is Step 5's implementation of ADR-011, not itself a new durable
// constraint requiring its own ADR — see this task's report for that
// judgment call, which the orchestrator makes, not this file.
var healthCriticalSignals = map[observation.SignalID]func(value any) observation.Health{
	// "fpp.reachable" is [fpp.SignalReachable]'s exact wire value, inlined
	// as a literal rather than importing
	// internal/coordinator/collector/fpp into this package: this API
	// renders and now resolves (see [ResolveObservations]) evidence from
	// whichever collector source produced it, fpp-rest or fpp-mqtt, and
	// importing one concrete collector package to borrow a string constant
	// would make that source-agnostic promise a lie in the one file that
	// most needs to keep it honest.
	//
	// The `else HealthFailed` branch below is real, total behavior for any
	// current-state Measured value this function is ever handed (and is
	// pinned directly by TestDeriveInstanceHealthReachableFalseIsFailed),
	// but it is DEAD CODE against every actual poll internal/coordinator/
	// collector/fpp has ever produced: that collector never calls
	// observation.Measured(false, ...) for this signal — an unreachable FPP
	// is reported as observation.CollectionFailed instead (see that
	// package's Collector.Poll), and [observation.DeriveHealth] never even
	// invokes this function for a non-current state in the first place (see
	// this map's own doc comment above the deriveInstanceHealth call site).
	// So a definitively-down, correctly-configured FPP instance reports
	// health "unknown", never "failed" — Step 3's deliberate decision,
	// which this task's spec confirmed still stands (Step 5 review finding
	// 4(a): the spec's own health table was wrong to imply "failed" was
	// reachable here, the code was already right, and
	// TestDeriveInstanceHealthUnreachableInstanceReportsUnknown below pins
	// the real, reachable behavior by name). This comment — not a code
	// change — is what closes that gap between what this map's shape
	// implies and what a real collector poll can actually produce.
	"fpp.reachable": func(value any) observation.Health {
		if b, ok := value.(bool); ok && b {
			return observation.HealthHealthy
		}
		return observation.HealthFailed
	},
	// "fpp.fppd.state" carries FPP's own daemon state string, e.g.
	// "running" (contract section 3.1). Any other value — including one
	// this coordinator has never seen before — is degraded, never failed:
	// Step 5 has no evidence FPP's daemon-state vocabulary is closed, so
	// treating an unrecognized string as merely degraded (not a hard
	// failure) is the conservative reading available from what little this
	// signal says on its own.
	"fpp.fppd.state": func(value any) observation.Health {
		if s, ok := value.(string); ok && s == "running" {
			return observation.HealthHealthy
		}
		return observation.HealthDegraded
	},
	// "fpp.power.bad" is FPP's own boolean fault flag (contract section
	// 3.1). false is healthy; true (or any other type this coordinator
	// somehow received) is degraded — this is the literal Step 5 defect
	// this map exists to fix: the Step 3 mapper contributed HealthHealthy
	// for fpp.power.bad == true because it never looked at the value.
	"fpp.power.bad": func(value any) observation.Health {
		if b, ok := value.(bool); ok && !b {
			return observation.HealthHealthy
		}
		return observation.HealthDegraded
	},
}

// deriveInstanceHealth aggregates an FPP instance's RESOLVED (see
// [ResolveObservations] — callers must already have collapsed any
// multi-source duplicates before calling this) observations into one
// [observation.Health], per Step 5 contract section 5.3.
//
// Two things are true of every observation before it can become an
// [observation.AggregateMember] here:
//
//  1. Its resolved state must not be [observation.StateUnsupported].
//     Unsupported is a positive statement that a source cannot answer this
//     signal at all (remote-mode playback, a smart-receiver's current, an
//     absent pixelCount), not missing evidence — it contributes nothing to
//     the verdict, critical signal or not, and does not even get built into
//     an AggregateMember (a critical member whose Health resolved to
//     HealthUnknown would still count against the aggregate per
//     [observation.AggregateHealth]'s own critical-unknown rule, which is
//     exactly the wrong outcome for a signal that positively cannot be
//     answered).
//  2. Its Signal must be one of [healthCriticalSignals]' keys. Everything
//     else — every fpp.warnings.*, every fpp.sensor.*, every fpp.port.*,
//     platform/utilization signals, the identity/version signals — is
//     informational and is skipped entirely, never added as a
//     non-critical member either: OBSERVABILITY section 4.2's aggregate
//     rule already lets a non-critical HealthUnknown pass through without
//     blocking a healthy verdict, but skipping it here (rather than adding
//     it as Critical: false) also keeps a large fleet of informational
//     signals from silently padding the aggregate with HealthHealthy
//     members for values this file has no actual health opinion about.
//
// For the two above, [observation.DeriveHealth] still does the real work:
// it returns HealthUnknown for anything that is not
// [observation.StateCurrent] as of now (stale, unknown_age,
// collection_failed, not_collected), and only calls this map's per-signal
// function when the observation is genuinely current.
//
// An instance with no health-critical evidence at all — none of the three
// signals above has ever been observed for it — is [observation.HealthUnknown]:
// [observation.AggregateHealth] returns HealthUnknown for an empty (or
// entirely suppressed/non-critical-unknown) member list, which `members`
// legitimately is whenever every observation was either unsupported or
// informational.
//
// A third rule, beyond what the two numbered above and
// [observation.AggregateHealth] give for free (Step 5 review finding
// 4(b)): a HealthHealthy verdict additionally requires fpp.fppd.state
// itself to have resolved HealthHealthy — present, current, and reading
// "running" — not merely fpp.reachable and fpp.power.bad. Before this
// rule, an instance whose source never reports fpp.fppd.state at all (not
// a hypothetical — any source that only implements reachability and the
// power-fault flag) read fully healthy from two of three critical
// signals, with zero evidence the player daemon itself was ever running:
// "the HTTP server answered" is not evidence that the player is healthy,
// and ADR-011 forbids a confident verdict the evidence does not support.
// If fpp.fppd.state is absent or [observation.StateUnsupported] — either
// one means it never becomes a member at all, by rule 2 above and this
// map's key set — the aggregate is capped to [observation.HealthUnknown]
// even when reachable+power.bad alone would otherwise read healthy. A
// fpp.fppd.state that IS present but not current (stale,
// collection_failed, unknown_age...) already forces this outcome for free
// via [observation.AggregateHealth]'s own critical-unknown rule, since
// DeriveHealth returns HealthUnknown for it as a normal critical member —
// this cap exists only for the "never observed at all" gap that rule
// cannot see, because a signal with zero observations never becomes a
// member to begin with.
func deriveInstanceHealth(obs []observation.Observation, now time.Time) observation.Health {
	fppdStateHealthy := false
	health := deriveHealthFromCriticalSignals(obs, now, healthCriticalSignals, func(sig observation.SignalID, h observation.Health) {
		if sig == "fpp.fppd.state" && h == observation.HealthHealthy {
			fppdStateHealthy = true
		}
	})
	if health == observation.HealthHealthy && !fppdStateHealthy {
		return observation.HealthUnknown
	}
	return health
}

// deriveHealthFromCriticalSignals is the one aggregator loop every
// resource's own derive*Health function calls, parameterized on that
// resource's own critical-signal map — see [healthCriticalSignals] and
// [resolumeHealthCriticalSignals]. onMember, when non-nil, is invoked with
// each critical, non-unsupported observation's signal and resolved Health
// as its AggregateMember is built, so a resource-specific rule (deriveInstanceHealth's
// fpp.fppd.state cap) can inspect one signal without a second copy of this
// loop.
func deriveHealthFromCriticalSignals(obs []observation.Observation, now time.Time, critical map[observation.SignalID]func(value any) observation.Health, onMember func(sig observation.SignalID, h observation.Health)) observation.Health {
	members := make([]observation.AggregateMember, 0, len(critical))
	for _, o := range obs {
		if o.Absence == observation.StateUnsupported {
			continue
		}
		whenCurrent, ok := critical[o.Signal]
		if !ok {
			continue
		}
		h := observation.DeriveHealth(o, now, whenCurrent)
		members = append(members, observation.AggregateMember{Health: h, Critical: true})
		if onMember != nil {
			onMember(o.Signal, h)
		}
	}
	return observation.AggregateHealth(members)
}

// mapFPPInstance renders one [FPPInstanceView] as a [v1.FPPInstance].
// [ResolveObservations] runs here, once: fv.Observations may carry more
// than one collector source's row for the same signal (fpp-rest and
// fpp-mqtt both report the identically-named status/port signals — Step 5
// contract section 4.3), and this is the single call site every FPP
// instance rendering path — GET /api/v1/fpp, GET /api/v1/fpp/{id}, the
// snapshot's fpp section, and every fpp.changed stream event (see
// stream.go) — goes through, so resolving here makes it resolved
// everywhere, once, per contract section 5.2's "resolution happens once,
// at read."
func mapFPPInstance(fv FPPInstanceView, now time.Time) v1.FPPInstance {
	resolved := ResolveObservations(fv.Observations)
	sortObservations(resolved)

	obsEvidence := make([]v1.Evidence, 0, len(resolved))
	for _, o := range resolved {
		obsEvidence = append(obsEvidence, mapEvidence(o, now))
	}

	inst := v1.FPPInstance{
		InstanceID:                       fv.InstanceID,
		Endpoint:                         sanitizeEndpoint(fv.Endpoint),
		Health:                           string(deriveInstanceHealth(resolved, now)),
		Observations:                     obsEvidence,
		LastPollAt:                       formatTimePtr(fv.LastPollAt),
		LastPollError:                    fv.LastPollError,
		DuplicateInstanceUUIDEndpointIDs: fv.DuplicateInstanceUUIDEndpointIDs,
	}
	if inst.DuplicateInstanceUUIDEndpointIDs == nil {
		inst.DuplicateInstanceUUIDEndpointIDs = []string{}
	}
	if fv.InstanceUUID != nil {
		uuid := fv.InstanceUUID.UUID
		inst.InstanceUUID = &uuid
		firstObserved := formatTime(fv.InstanceUUID.FirstObservedAt)
		inst.InstanceUUIDFirstObservedAt = &firstObserved
		if fv.InstanceUUID.HasUnacknowledgedChange() {
			inst.InstanceUUIDChange = &v1.FPPInstanceUUIDChange{
				PreviousUUID: fv.InstanceUUID.PreviousUUID,
				ChangedAt:    formatTime(fv.InstanceUUID.ChangedAt),
			}
		}
	}
	return inst
}

// --- Resolume instances ---

// resolumeHealthCriticalSignals is Resolume's own health-critical set,
// beside [healthCriticalSignals] rather than folded into it: unrelated
// signal vocabularies, no fppdState-shaped cap. Signal IDs are inlined
// string literals rather than imported from
// internal/coordinator/collector/resolume for the identical
// source-agnostic reason [healthCriticalSignals] inlines "fpp.reachable".
var resolumeHealthCriticalSignals = map[observation.SignalID]func(value any) observation.Health{
	// "resolume.reachable" mirrors "fpp.reachable": true is healthy,
	// anything else is failed.
	"resolume.reachable": func(value any) observation.Health {
		if b, ok := value.(bool); ok && b {
			return observation.HealthHealthy
		}
		return observation.HealthFailed
	},
	// "resolume.composition.identified" carries a descriptive string
	// (resolume.Collector's own identityObservation), not a bare bool.
	// Exactly "identified" is healthy. A "not_identified" prefix is
	// degraded: the operator may legitimately have loaded a different
	// composition, which is a thing to surface, not a system failure. A
	// "deck_mismatch" prefix is unknown, not degraded (owner amendment,
	// 2026-08-16): it means the sampled clips did not resolve because the
	// selected deck changed mid-check, so identity could not be
	// determined — that is an absence of evidence, and claiming degraded
	// would assert ShowMesh found something wrong with the composition
	// when it did not. Anything else (the "unknown: ..." load-window
	// case, or an unrecognized type) is unknown.
	"resolume.composition.identified": func(value any) observation.Health {
		s, ok := value.(string)
		if !ok {
			return observation.HealthUnknown
		}
		switch {
		case s == "identified":
			return observation.HealthHealthy
		case strings.HasPrefix(s, "not_identified"):
			return observation.HealthDegraded
		default:
			return observation.HealthUnknown
		}
	},
}

// deriveResolumeHealth aggregates a Resolume instance's RESOLVED
// observations into one [observation.Health] against
// [resolumeHealthCriticalSignals], via the same
// [deriveHealthFromCriticalSignals] loop [deriveInstanceHealth] uses.
func deriveResolumeHealth(obs []observation.Observation, now time.Time) observation.Health {
	return deriveHealthFromCriticalSignals(obs, now, resolumeHealthCriticalSignals, nil)
}

// mapResolumeInstance renders one [ResolumeInstanceView] as a
// [v1.ResolumeInstance], at the given now. Mirrors [mapFPPInstance]'s shape:
// [ResolveObservations] runs here, once, so every caller — GET
// /resolume/instances, GET /resolume/instances/{id}, the snapshot's
// resolume section, and every resolume.changed stream event — resolves
// identically. composition is computed by the caller (resolumecomposition.go's
// resolumeInstanceComposition), never here: it is coordinator-wide stored
// configuration, not per-instance collector state, so every caller in this
// package computes it exactly once per response rather than once per
// instance.
func mapResolumeInstance(rv ResolumeInstanceView, composition *v1.ResolumeInstanceComposition, now time.Time) v1.ResolumeInstance {
	resolved := ResolveObservations(rv.Observations)
	sortObservations(resolved)

	obsEvidence := make([]v1.Evidence, 0, len(resolved))
	for _, o := range resolved {
		obsEvidence = append(obsEvidence, mapEvidence(o, now))
	}

	return v1.ResolumeInstance{
		InstanceID:   rv.InstanceID,
		Health:       string(deriveResolumeHealth(resolved, now)),
		Observations: obsEvidence,
		Composition:  composition,
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
