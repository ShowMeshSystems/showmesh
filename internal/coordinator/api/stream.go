package api

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"sort"
	"sync"
	"time"

	"github.com/google/uuid"

	v1 "github.com/showmeshsystems/showmesh/internal/coordinator/api/v1"
	"github.com/showmeshsystems/showmesh/internal/coordinator/identity"
	"github.com/showmeshsystems/showmesh/pkg/observation"
)

// hubEventsBatchLimit bounds how many events [Hub.renderNewEvents] fetches
// in a single tick. A SHOWMESH HYPOTHESIS, not a measured value: large
// enough that a normal tick interval's worth of event volume is delivered
// in one pass, small enough to bound one render call's cost. If the store
// ever produces more than this many events between two ticks, the
// remainder is picked up on the following tick — [Hub.lastEventSeq] only
// advances to the highest seq actually turned into a frame, never skips
// ahead — so no event is ever lost, only delayed.
const hubEventsBatchLimit = 500

// streamWriteTimeout bounds how long ONE write to an SSE connection
// (stream.start, one frame, one keepalive comment) may take before
// [Hub.ServeHTTP] gives up on that connection and tears it down. Reset
// immediately before every write — see resetWriteDeadline — rather than set
// once for the connection's whole lifetime: a healthy stream legitimately
// runs for hours, but any ONE write should complete almost instantly once
// the kernel accepts the bytes, so this bounds each write, not the
// connection.
//
// This closes finding 1.1: ServeHTTP used to clear the write deadline
// unconditionally right after upgrading, to defeat httpapi's own
// WriteTimeout (see TestStreamSurvivesServerWriteTimeout, which this value
// must still satisfy), and put nothing in its place. A subscriber that
// stops reading but holds its socket open eventually fills the kernel send
// buffer; with no deadline, the next w.Write blocks forever, ServeHTTP
// never returns to its select, and the stream.reset the hub already queued
// on sub.reset for exactly this situation is never read and never written
// — the silent drop ADR-020 decision 4 exists to forbid. See
// TestStreamWedgedSubscriberIsReclaimedByWriteDeadline for the regression
// guard, which uses a client that provably never reads at all.
//
// A SHOWMESH HYPOTHESIS, not a measured value: long enough that ordinary
// network jitter or a briefly-busy real client never trips it, short
// enough that a genuinely wedged connection is reclaimed within a bounded
// time an operator would notice as a stuck dashboard tile rather than a
// leak that outlives the process. A package-level var, not a const, purely
// so a test can shrink it (see stream_test.go) without inventing an env
// var for something this test-local — the same posture
// defaultStreamSubscriberBuffer already takes for a different knob.
var streamWriteTimeout = 10 * time.Second

// resetWriteDeadline gives the connection underlying w up to
// streamWriteTimeout to accept the NEXT write, refreshed immediately before
// every write [Hub.ServeHTTP] makes. A write that blocks past this deadline
// means the peer has stopped reading — the kernel send buffer is full and
// nothing is draining it — and the expired deadline turns that block into
// an error the write call returns, which every caller here already treats
// as "the connection is gone, stop" (see [writeSSE] and the keepalive
// write in [Hub.ServeHTTP]).
//
// The error return is deliberately ignored, matching this file's existing
// posture for an http.ResponseWriter that does not support deadlines at
// all (e.g. httptest.ResponseRecorder in a unit test with no real
// connection underneath): such a writer has no timeout to set in the first
// place, so failing to set one is not a fault here.
func resetWriteDeadline(w http.ResponseWriter) {
	_ = http.NewResponseController(w).SetWriteDeadline(time.Now().Add(streamWriteTimeout))
}

// Hub is the SSE change stream's server side (contract sections 6.4 and
// 6.5). It owns change detection: on a fixed tick, and whenever poked via
// [Hub.Notify], it re-renders every node and FPP instance through the same
// mapping functions the REST handlers use, diffs each against what it last
// published, and broadcasts only the resources that actually changed —
// including a change caused by nothing but time passing, such as an
// [pkg/observation.Observation] crossing from current to stale (contract
// section 6.5's reason the hub re-renders on a tick at all, not only when
// poked).
//
// One Hub serves every subscriber; each subscriber gets its own bounded
// buffer and its own per-connection seq counter (contract section 6.4:
// "seq is per-connection... so it can never become a global cursor"). A
// subscriber whose buffer overflows is sent stream.reset and disconnected
// — see [Hub.broadcast] — never blocking the producer and never growing
// without bound.
//
// [Hub.Run] must be started, in its own goroutine, exactly once, with a
// context tied to the coordinator's shutdown; [New] does not start it.
// Cancelling that context closes every open stream cleanly and Run
// returns; nothing in this type spawns a goroutine Run does not own, so a
// caller that starts Run and later cancels its context can assert the
// goroutine count returns to baseline.
type Hub struct {
	deps        Dependencies
	identitySvc identity.Service
	clock       func() time.Time
	logger      *slog.Logger

	tickInterval      time.Duration
	keepaliveInterval time.Duration
	bufSize           int

	mu           sync.Mutex
	subscribers  map[uint64]*subscriber
	nextID       uint64
	lastRendered map[string][]byte
	lastEventSeq uint64

	// lastRenderedInstanceOnly and lastObservationsBySignal are ADR-023's
	// second and third change-detection caches, alongside lastRendered
	// (which continues to gate the full-frame fpp.changed send exactly as
	// it did before deltas existed — see [Hub.updateRendered]). Both are
	// keyed by the same "fpp:"+instanceID strings lastRendered uses, and
	// both are evicted alongside it in [Hub.evictRendered] whenever an
	// instance disappears from a render pass, so the three caches can never
	// drift out of sync with each other about which instances currently
	// exist.
	//
	// lastRenderedInstanceOnly holds the masked, Observations-stripped
	// projection [instanceOnlyForDelta] produces — see
	// [Hub.updateInstanceOnlyRendered] — used only to decide whether a
	// delta-subscribed connection receives fpp.changed at all (ADR-023
	// decision 3: a change confined to Observations must not trigger it).
	//
	// lastObservationsBySignal holds, per instance, the masked-for-diff
	// bytes of the most recently rendered [v1.Evidence] for every signal
	// this hub has ever reported for it — see
	// [Hub.updateObservationDeltas], which is also what removes a signal's
	// entry the render pass after it stops appearing, which is exactly what
	// lets that same removal be reported to a delta client via
	// fpp.observations.changed's "removed" list.
	lastRenderedInstanceOnly map[string][]byte
	lastObservationsBySignal map[string]map[string][]byte

	notifyCh chan struct{}
	done     chan struct{}
}

// subscriber is one open SSE connection's mailbox. frames is the bounded
// buffer [Hub.broadcast] delivers pending changes into; reset is a
// capacity-1 channel used to hand a reason — an overflow (ADR-023) or a
// revoked/invalidated credential (ADR-024 decision 5, see
// [Hub.revalidateSubscribers]) — to [Hub.ServeHTTP]'s own goroutine, which
// closes the connection on either. deltas is fixed for the connection's
// whole lifetime, set once from the ?deltas=1 query parameter at
// [Hub.ServeHTTP]'s own subscribe call — ADR-023 decision 1: a connection
// that did not ask for deltas gets exactly what it got before deltas
// existed, so this field is never mutated after [Hub.subscribe]
// constructs it. cred is nil for a connection that opened with no
// credential at all (the ordinary case while reads are open) and is never
// mutated either — see [streamCredential]'s doc comment.
type subscriber struct {
	frames chan pendingFrame
	reset  chan string
	deltas bool
	cred   *streamCredential
}

// streamCredential is the raw secret that authenticated an SSE connection
// at subscribe time, kept ONLY so [Hub.revalidateSubscribers] can present
// it to identity.Service again on a later tick (ADR-024 decision 5: "the
// coordinator therefore revalidates the credential of an open stream
// periodically and on generation change, and closes the connection" when
// it no longer authenticates). It is never logged and never rendered on
// the wire; it lives no longer than the subscriber itself, and is
// discarded with it — the same lifetime the secret already has sitting in
// the original request's Cookie/Authorization header, which this merely
// carries forward for the life of a connection that (unlike an ordinary
// request) never ends.
type streamCredential struct {
	form   identity.CredentialForm
	secret string
}

// frameAudience narrows which subscribers a [pendingFrame] is delivered to,
// per ADR-023 decision 1 (a connection that never asked for deltas must
// never receive an fpp.observations.changed frame, full stop) and decision
// 3 (a delta-subscribed connection receives fpp.changed only for a
// structural, non-observation change — see [Hub.render]'s fpp instance
// loop, the only place a non-default audience is ever constructed).
// audienceAll is the zero value deliberately: every pendingFrame literal
// this file built before ADR-023 (node.changed, event.recorded) never sets
// this field at all, and must keep reaching every subscriber exactly as it
// did before this field existed.
type frameAudience int

const (
	audienceAll frameAudience = iota
	audienceNonDeltaOnly
	audienceDeltaOnly
)

// includes reports whether a subscriber whose own deltas flag is deltas
// should receive a frame carrying this audience.
func (a frameAudience) includes(deltas bool) bool {
	switch a {
	case audienceDeltaOnly:
		return deltas
	case audienceNonDeltaOnly:
		return !deltas
	default:
		return true
	}
}

// pendingFrame is one change the hub has decided, at a particular render
// pass, to broadcast — captured with its resource payload and the
// serverTime of that render pass, but deliberately WITHOUT a seq: seq is
// assigned once per subscriber, in [Hub.ServeHTTP]'s own loop, at the
// moment a given subscriber actually writes the frame to its connection —
// never here, since the same logical change reaches every subscriber but
// each must see its own independent, connection-local seq sequence.
type pendingFrame struct {
	event      string
	serverTime string

	// audience defaults to audienceAll (the zero value) for every kind this
	// file constructed before ADR-023: node.changed and event.recorded
	// reach every subscriber regardless of its deltas flag. fpp.changed and
	// fpp.observations.changed are the only kinds [Hub.render] ever
	// constructs with a narrower audience — see its fpp instance loop.
	audience frameAudience

	node     *v1.Node
	instance *v1.FPPInstance
	ev       *v1.Event

	// resolumeInstance is set only for a "resolume.changed" pendingFrame,
	// mirroring instance's role for "fpp.changed" — full-frame only, no
	// ADR-023 delta narrowing: this resource has no delta event kind, so
	// audience is always the zero value (audienceAll).
	resolumeInstance *v1.ResolumeInstance

	// resolumeRecovery is set only for a "resolumeRecovery.changed"
	// pendingFrame (Track D seam D-3a, build contract §1.7): the
	// resolumerecovery:default singleton resource, full-frame only, same
	// posture as resolumeInstance immediately above — no delta kind exists
	// for it either.
	resolumeRecovery *v1.ResolumeRecoveryChangedEvent

	// macroRun is set only for a "macroRun.changed" pendingFrame (Step 9
	// wave 2, STEP-9-SPEC.md section 6.6): the run's state-transition
	// facts, WITHOUT its steps ("a run with 32 steps must not put 32
	// events on a stream every client receives" — step detail is fetched,
	// via GET /macro-runs/{runId}, never streamed). Seq is assigned in
	// materialize, exactly like every other kind.
	macroRun *v1.MacroRunChangedEvent

	// instanceID, changedObs, and removedSignals are set only for an
	// "fpp.observations.changed" pendingFrame (ADR-023); every other kind
	// leaves them at their zero value and materialize never reads them for
	// those kinds.
	instanceID     string
	changedObs     []v1.Evidence
	removedSignals []string
}

// materialize assigns seq to pf and returns the SSE event name and the
// exact struct to JSON-encode as its data.
func (pf pendingFrame) materialize(seq uint64) (event string, payload any) {
	switch pf.event {
	case "node.changed":
		return "node.changed", v1.NodeChangedEvent{Seq: seq, ServerTime: pf.serverTime, Node: *pf.node}
	case "fpp.changed":
		return "fpp.changed", v1.FPPChangedEvent{Seq: seq, ServerTime: pf.serverTime, Instance: *pf.instance}
	case "fpp.observations.changed":
		return "fpp.observations.changed", v1.FPPObservationsChangedEvent{
			Seq: seq, ServerTime: pf.serverTime, InstanceID: pf.instanceID,
			Changed: nonNilEvidenceSlice(pf.changedObs), Removed: nonNilStringSlice(pf.removedSignals),
		}
	case "event.recorded":
		return "event.recorded", v1.EventRecordedEvent{Seq: seq, ServerTime: pf.serverTime, Event: *pf.ev}
	case "macroRun.changed":
		ev := *pf.macroRun
		ev.Seq = seq
		ev.ServerTime = pf.serverTime
		return "macroRun.changed", ev
	case "resolume.changed":
		return "resolume.changed", v1.ResolumeChangedEvent{Seq: seq, ServerTime: pf.serverTime, Instance: *pf.resolumeInstance}
	case "resolumeRecovery.changed":
		ev := *pf.resolumeRecovery
		ev.Seq = seq
		ev.ServerTime = pf.serverTime
		return "resolumeRecovery.changed", ev
	default:
		// Unreachable: every pendingFrame this file constructs sets event
		// to one of the four cases above. A panic here is an internal
		// invariant violation in this package, not a runtime condition a
		// caller can trigger — see mapping.go's mustObservation for the
		// same posture.
		panic("api: pendingFrame with unknown event " + pf.event)
	}
}

// nonNilEvidenceSlice and nonNilStringSlice guarantee an
// fpp.observations.changed frame's "changed"/"removed" arrays render as `[]`
// rather than `null` when empty, matching this API's standing "absent
// evidence is stated, never omitted" rule applied to a collection rather
// than a scalar field (the same reasoning [v1.Node.Capabilities]'s own doc
// comment gives).
func nonNilEvidenceSlice(v []v1.Evidence) []v1.Evidence {
	if v == nil {
		return []v1.Evidence{}
	}
	return v
}

func nonNilStringSlice(v []string) []string {
	if v == nil {
		return []string{}
	}
	return v
}

// newHub builds a Hub. Unexported: [New] is the only supported
// constructor, so Options' defaults are always applied first.
func newHub(deps Dependencies, opts Options, logger *slog.Logger) *Hub {
	return &Hub{
		deps:                     deps,
		identitySvc:              deps.Identity,
		clock:                    opts.Clock,
		logger:                   logger,
		tickInterval:             opts.StreamTickInterval,
		keepaliveInterval:        opts.StreamKeepaliveInterval,
		bufSize:                  opts.StreamSubscriberBuffer,
		subscribers:              make(map[uint64]*subscriber),
		lastRendered:             make(map[string][]byte),
		lastRenderedInstanceOnly: make(map[string][]byte),
		lastObservationsBySignal: make(map[string]map[string][]byte),
		notifyCh:                 make(chan struct{}, 1),
		done:                     make(chan struct{}),
	}
}

// Notify pokes the hub to render and broadcast immediately, rather than
// waiting for the next tick. Non-blocking: if a poke is already pending,
// this call coalesces into it rather than queuing a second one — the next
// render pass will see whatever is current regardless of how many times
// Notify was called since the last render.
func (h *Hub) Notify() {
	select {
	case h.notifyCh <- struct{}{}:
	default:
	}
}

// Run drives the hub's tick and poke loop until ctx is cancelled, at which
// point it closes every open stream and returns. See the [Hub] doc comment
// for the "exactly once" requirement.
func (h *Hub) Run(ctx context.Context) {
	ticker := time.NewTicker(h.tickInterval)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			close(h.done)
			return
		case <-ticker.C:
			// revalidateSubscribers runs only on the fixed tick, not on
			// every Notify() poke — Notify can fire many times per second
			// during an MQTT burst (see [Hub.render]'s own doc comment on
			// the identical reasoning for CollectedAt churn), and each
			// revalidation is a real identity.Service call (a session
			// digest lookup or a token digest lookup) per authenticated
			// subscriber. Bounding it to the tick interval (ADR-024
			// decision 5's "periodically") is what keeps that cost
			// bounded and predictable rather than proportional to
			// unrelated event volume. A generation bump is caught within
			// one tick interval of occurring, which the same call already
			// re-checks as a side effect of re-verifying the credential
			// (see [Hub.revalidateSubscribers]) — decision 5's "and on
			// generation change" needs no separate mechanism.
			h.revalidateSubscribers(ctx, h.clock())
			h.render(ctx)
		case <-h.notifyCh:
			h.render(ctx)
		}
	}
}

// render re-renders every node and FPP instance and diffs each against
// what was last published (contract section 6.5), fetches any events
// recorded since the last pass, and broadcasts whatever changed. Errors
// from a dependency are logged and that resource kind is simply skipped
// for this pass — a transient store error must not crash the hub's
// goroutine or poison every subsequent tick.
func (h *Hub) render(ctx context.Context) {
	now := h.clock()
	var pending []pendingFrame

	if views, err := h.deps.Nodes.Snapshot(ctx, now); err != nil {
		h.logger.Warn("stream hub: list nodes failed", "error", err)
	} else if declByNodeID, latestRun, err := fetchDeclarationContext(ctx, h.deps.Discovery); err != nil {
		// BUILD-PLAN Step 7 seam B: fetched once per render pass, mirroring
		// h.deps.Nodes/h.deps.FPP's own "one dependency error skips this
		// resource kind for the pass" posture immediately below — a
		// transient store error here must not crash the hub's goroutine or
		// poison every subsequent tick either.
		h.logger.Warn("stream hub: list node declarations failed", "error", err)
	} else {
		// DEFECT 4: a declared node with no inventory row must render, and
		// keep rendering, through the change stream exactly as it does
		// through GET /api/v1/nodes and the snapshot — see
		// mergeDeclaredOnlyNodes' own doc comment. Without this, a client
		// that fetched the snapshot (which already includes it, via
		// handleSnapshot's own identical merge) would see it silently
		// evicted the first time this hub ticks, because evictRendered
		// below removes any "node:"+id key not present in views.
		views = mergeDeclaredOnlyNodes(views, declByNodeID)
		present := make(map[string]struct{}, len(views))
		for _, nv := range views {
			key := "node:" + nv.NodeID
			present[key] = struct{}{}
			node := mapNode(nv, now, declPtr(declByNodeID, nv.NodeID), latestRun, h.deps.Render.NodeRenderObservations(nv.NodeID))
			if h.updateRendered(key, node) {
				n := node
				pending = append(pending, pendingFrame{event: "node.changed", serverTime: formatTime(now), node: &n})
			}
		}
		h.evictRendered("node:", present)
	}

	if views, err := h.deps.FPP.ListInstances(ctx); err != nil {
		h.logger.Warn("stream hub: list fpp instances failed", "error", err)
	} else {
		present := make(map[string]struct{}, len(views))
		for _, fv := range views {
			key := "fpp:" + fv.InstanceID
			present[key] = struct{}{}
			inst := mapFPPInstance(fv, now)
			proj := fppInstanceDiffProjection(inst)

			// fullChanged is EXACTLY the pre-ADR-023 gate, unmodified: it
			// alone decides whether a non-delta-subscribed connection ever
			// sees fpp.changed, which is the entire additive-compatibility
			// argument (ADR-023 decision 1) — a connection that never asks
			// for deltas must observe no difference from before this
			// feature existed.
			fullChanged := h.updateRendered(key, proj)
			// instChanged narrows that same signal to STRUCTURAL fields only
			// (health, endpoint, lastPollError — lastPollAt is already
			// excluded from proj itself, see fppInstanceDiffProjection), per
			// ADR-023 decision 3. Because proj's non-Observations fields are
			// a strict subset of what fullChanged already diffs, instChanged
			// can only be true when fullChanged is also true — so a
			// delta-subscribed connection audienceAll-widened below never
			// receives an fpp.changed the non-delta gate itself did not
			// license.
			instChanged := h.updateInstanceOnlyRendered(key, instanceOnlyForDelta(proj))
			changedObs, removedSignals := h.updateObservationDeltas(key, inst.Observations)

			if fullChanged {
				aud := audienceNonDeltaOnly
				if instChanged {
					aud = audienceAll
				}
				i := inst
				pending = append(pending, pendingFrame{event: "fpp.changed", serverTime: formatTime(now), instance: &i, audience: aud})
			}
			// fpp.observations.changed is skipped whenever instChanged is
			// true, even if changedObs/removedSignals is also non-empty:
			// instChanged true means the fpp.changed frame just appended
			// above ALREADY went to every delta-subscribed connection too
			// (audienceAll), carrying this exact instance's full, current
			// Observations — including whatever just changed or was
			// removed. Sending fpp.observations.changed in the same pass
			// would be pure duplication of information a delta client
			// already just received, never new information (this is also
			// what keeps a genuinely brand-new instance — where EVERY
			// signal is, trivially, "different from nothing" — from
			// producing both its first fpp.changed AND a redundant
			// fpp.observations.changed repeating every signal it just
			// carried).
			if !instChanged && (len(changedObs) > 0 || len(removedSignals) > 0) {
				pending = append(pending, pendingFrame{
					event: "fpp.observations.changed", serverTime: formatTime(now),
					instanceID: fv.InstanceID, changedObs: changedObs, removedSignals: removedSignals,
					audience: audienceDeltaOnly,
				})
			}
		}
		h.evictRendered("fpp:", present)
	}

	// Step 9 wave 2: run state transitions (STEP-9-SPEC.md section 6.6).
	// Rendered from the SAME bounded, in-flight-plus-recently-finished
	// window [Hub.deps.Macros.SnapshotRuns] serves GET /api/v1/snapshot
	// from (handlers.go's handleSnapshot) — deliberately, so a run this
	// hub ever announces a change for is always one a freshly connecting
	// client's own snapshot fetch can also see (ADR-020 decision 3): this
	// hub must never announce a transition for a run that has already
	// scrolled out of the window a reconnecting client would re-sync
	// against, or that client's local model would carry a change it can
	// never reconcile against its own snapshot.
	if runs, err := h.deps.Macros.SnapshotRuns(ctx); err != nil {
		h.logger.Warn("stream hub: snapshot macro runs failed", "error", err)
	} else {
		present := make(map[string]struct{}, len(runs))
		for _, run := range runs {
			key := "macrorun:" + run.ID
			present[key] = struct{}{}
			ev := macroRunChangedEventProjection(run)
			if h.updateRendered(key, ev) {
				e := ev
				pending = append(pending, pendingFrame{event: "macroRun.changed", serverTime: formatTime(now), macroRun: &e})
			}
		}
		h.evictRendered("macrorun:", present)
	}

	// Every configured Resolume instance, full-frame only — no ADR-023
	// delta narrowing exists for this resource. composition is
	// coordinator-wide stored configuration, fetched once per render pass
	// and shared across every instance rendered in it.
	if rviews, err := h.deps.Resolume.ListInstances(ctx); err != nil {
		h.logger.Warn("stream hub: list resolume instances failed", "error", err)
	} else if composition, err := resolumeInstanceComposition(ctx, h.deps.Config); err != nil {
		// The store erroring is not evidence Resolume is gone: skip this
		// resource kind for the pass, mirroring every other dependency-error
		// branch in this method — never publish an empty Resolume list on a
		// transient read failure.
		h.logger.Warn("stream hub: get resolume composition failed", "error", err)
	} else {
		present := make(map[string]struct{}, len(rviews))
		for _, rv := range rviews {
			key := "resolume:" + rv.InstanceID
			present[key] = struct{}{}
			inst := mapResolumeInstance(rv, composition, now)
			proj := resolumeInstanceDiffProjection(inst)
			if h.updateRendered(key, proj) {
				i := inst
				pending = append(pending, pendingFrame{event: "resolume.changed", serverTime: formatTime(now), resolumeInstance: &i})
			}
		}
		h.evictRendered("resolume:", present)
	}

	// The recovery record, the auto-restore toggle, and the last restore:
	// one fixed singleton resource, keyed "resolumerecovery:default" —
	// never evicted, full-frame only, no delta kind, mirroring
	// resolume.changed's own posture immediately above. Skipped entirely
	// when ResolumeRecovery is unwired ([noResolumeRecoveryProvider]): a
	// singleton has no natural empty state the way ListInstances gives
	// resolume:<id> above for free. Once wired, a toggle-read error skips
	// this pass rather than publishing a default-looking state —
	// Record/LastReport never error.
	if _, unwired := h.deps.ResolumeRecovery.(noResolumeRecoveryProvider); !unwired {
		if enabled, configured, err := ResolveResolumeRecoveryToggle(ctx, h.deps.Config); err != nil {
			h.logger.Warn("stream hub: resolve resolume.recovery toggle failed", "error", err)
		} else {
			const key = "resolumerecovery:default"
			// resolumeConfigured is always true here — this whole branch is
			// gated on ResolumeRecovery being wired (the !unwired check
			// above), which is exactly what that field reports.
			proj := resolumeRecoveryChangedEventProjection(true, enabled, configured, h.deps.ResolumeRecoverySettleSeconds,
				h.deps.ResolumeRecovery.Record(), h.deps.ResolumeRecovery.LastReport())
			if h.updateRendered(key, proj) {
				ev := proj
				pending = append(pending, pendingFrame{event: "resolumeRecovery.changed", serverTime: formatTime(now), resolumeRecovery: &ev})
			}
		}
	}

	pending = append(pending, h.renderNewEvents(ctx, now)...)

	for _, pf := range pending {
		h.broadcast(pf)
	}
}

// fppInstanceDiffProjection returns a copy of inst with pure
// collection-bookkeeping timestamps cleared — LastPollAt at the instance
// level and CollectedAt on every observation — used ONLY as
// [Hub.updateRendered]'s change-detection key for an FPP instance, never as
// what is actually broadcast (the caller keeps the unmodified inst, with
// its real timestamps, for that).
//
// This closes finding 1.5: the FPP collector polls on a fixed interval and
// stamps a fresh CollectedAt/LastPollAt on every attempt regardless of
// outcome, so an FPP that is — or stays — unreachable produces a
// byte-different rendering every poll even though nothing about the FPP
// itself changed, and [Hub.updateRendered]'s diff (contract section 6.5)
// would broadcast fpp.changed forever at poll cadence with nothing for a
// client to act on. Contract section 6.2 already forbids precomputed ages
// in payloads for exactly this reason (an age field would make the stream
// a firehose); collectedAt/lastPollAt are the same pathology one layer
// down, in the hub's OWN bookkeeping rather than in what it renders.
//
// Step 5 review finding 3 adds a second source of the identical pathology,
// one layer up from CollectedAt, measured against the real fleet at ~43
// KB/s per connected browser on an otherwise IDLE system (860 KB in 20s):
// with finding 2 fixed (evidenceReason no longer embeds a computed age),
// two causes remained, both entirely legitimate collector/precedence
// behavior that this projection — not the collectors, and not
// [ResolveObservations] — is the correct place to absorb:
//
//   - internal/coordinator/collector/fpp re-stamps ObservedAt on every poll
//     (every ~15s) even when the decoded value is byte-identical to the
//     last poll's.
//   - [ResolveObservations]' tier-1 "later ObservedAt wins" rule
//     legitimately flips which SOURCE wins for a signal both fpp-rest and
//     fpp-mqtt report, roughly twice per REST poll interval, whenever an
//     MQTT delivery lands one nanosecond newer — correct precedence
//     behavior (contract section 5.2), but it changes the rendered
//     `source` field with nothing an operator can act on having changed.
//
// For a resolved observation whose rendered State is "current" — a real
// value with a real known ObservedAt, current as of the render's own clock
// — ObservedAt and Source are ALSO cleared from the projection, alongside
// CollectedAt, on the reasoning contract section 5.2's own precedence rule
// already establishes: which source most recently confirmed a value, and
// exactly when, is provenance and freshness bookkeeping about how the
// value was obtained, not itself part of what the value or its state ARE.
// ValidForSeconds is cleared under the same condition, and leaving it in
// was a real defect measured against the live fleet rather than a
// hypothetical. It is a per-collector constant — 45s for fpp-rest, 30s
// for fpp-mqtt — so it flips for exactly the same reason Source does and
// at exactly the same moment. Masking Source while keeping ValidForSeconds
// is internally inconsistent, and the cost was not subtle: with both
// collectors reporting the ~151 overlapping signals of one instance, every
// precedence flip made all 151 look changed. Delta frames on the real
// fleet carried 67 to 76 signals each instead of the four to nine that had
// actually moved, including signals that cannot change at all
// (fpp.host_name, fpp.mode, fpp.mqtt.configured). Quality is deliberately
// NOT masked: it describes how a value was determined (direct, derived,
// inferred, operator) rather than which collector won the race, and it does
// not currently differ between the two sources for the same signal. If a
// future source makes it flip the same way, it belongs here too.
//
// What remains in the projection after that — Signal, Value, Unit, State,
// Reason, Quality — is exactly "if value, unit, reason
// and state are all unchanged AND the state is still current, nothing an
// operator can act on has changed" (Step 5 review finding 3's own words),
// so a byte-identical projection on the next render correctly produces no
// frame even though the real, broadcast inst (never mutated here) legitimately
// carries a fresher observedAt or a flipped source underneath.
//
// This masking is deliberately conditioned on State == "current", never
// unconditional, which is the ADR-011 safety property that makes it
// correct rather than the exact defect this project keeps re-catching: a
// value that STOPS being reconfirmed does not stay "current" — it ages
// into "stale" (or the source that was answering it starts reporting an
// absence), and State itself then differs in the projection, still
// producing a frame. Masking ObservedAt/Source unconditionally, across
// every state, would instead make a value that silently stopped updating
// look byte-identical to one still being actively reconfirmed, which is
// precisely the "stale reads as healthy" shape ADR-011 exists to forbid —
// see TestFPPInstanceDiffProjectionAgingToStaleStillProducesAFrame in
// stream_test.go for the regression guard.
func fppInstanceDiffProjection(inst v1.FPPInstance) v1.FPPInstance {
	proj := inst
	proj.LastPollAt = nil
	proj.Observations = make([]v1.Evidence, len(inst.Observations))
	for i, o := range inst.Observations {
		// Delegated, never repeated: see maskEvidenceForDiff's doc comment
		// for what happened the one time these were two implementations.
		proj.Observations[i] = maskEvidenceForDiff(o)
	}
	return proj
}

// resolumeInstanceDiffProjection mirrors [fppInstanceDiffProjection] for a
// Resolume instance, masking evidence via the SAME [maskEvidenceForDiff]
// function rather than a second implementation of the rule.
func resolumeInstanceDiffProjection(inst v1.ResolumeInstance) v1.ResolumeInstance {
	proj := inst
	proj.Observations = make([]v1.Evidence, len(inst.Observations))
	for i, o := range inst.Observations {
		proj.Observations[i] = maskEvidenceForDiff(o)
	}
	return proj
}

// instanceOnlyForDelta takes an ALREADY-masked projection (a
// [fppInstanceDiffProjection] result) and clears its Observations entirely,
// leaving only the instance-level fields ADR-023 decision 3 names as
// "structural": health, endpoint, and lastPollError (lastPollAt is already
// nil in proj — see [fppInstanceDiffProjection] — for the same
// collection-bookkeeping-churn reason it is excluded from the full-frame
// gate too, so it is excluded from this narrower one for free rather than
// by a second, separate rule). [Hub.updateInstanceOnlyRendered] diffs the
// result of this function against its own cache, entirely independent of
// [Hub.updateRendered]'s full-projection cache, so a change confined to
// Observations never trips it — which is exactly what decides whether a
// delta-subscribed connection receives fpp.changed at all for a given
// render pass.
func instanceOnlyForDelta(proj v1.FPPInstance) v1.FPPInstance {
	proj.Observations = nil
	return proj
}

// updateInstanceOnlyRendered mirrors [Hub.updateRendered] exactly, against
// the separate lastRenderedInstanceOnly cache: it reports whether v's JSON
// rendering differs from what was last stored under key in THAT cache,
// updating it unconditionally either way. Kept as a distinct method (rather
// than parameterizing updateRendered over which map to use) so each cache's
// own doc comment on the [Hub] struct can name exactly what it is for
// without a caller having to thread that context through an extra
// parameter.
func (h *Hub) updateInstanceOnlyRendered(key string, v any) bool {
	b, err := json.Marshal(v)
	if err != nil {
		// Unreachable for a v1.FPPInstance built by this package's own
		// mapping functions; see [Hub.updateRendered]'s identical posture.
		h.logger.Warn("stream hub: failed to render instance-only projection for change detection", "key", key, "error", err)
		return false
	}

	h.mu.Lock()
	defer h.mu.Unlock()
	prev, ok := h.lastRenderedInstanceOnly[key]
	if ok && bytes.Equal(prev, b) {
		return false
	}
	h.lastRenderedInstanceOnly[key] = b
	return true
}

// maskEvidenceForDiff is THE masking rule for change detection, and the only
// copy of it: [fppInstanceDiffProjection] calls this function rather than
// repeating its body. That is not tidiness. The two started as separate
// implementations whose comments each asserted they applied "EXACTLY" the
// same rule, and they silently stopped agreeing the moment ValidForSeconds
// was added to one of them, which cost a measured live re-run to find. A rule
// duplicated in two places with a comment claiming equivalence is worse than
// a rule duplicated in two places, because the comment stops anyone checking.
// See TestDiffMaskingRuleHasExactlyOneImplementation.
//
// The rule: CollectedAt is always cleared. When State is "current" —
// meaning a real value with a known ObservedAt that has not aged out —
// ObservedAt, Source and ValidForSeconds are cleared too, because all three
// describe how and when the value was obtained rather than what it is, and
// all three change together when contract section 5.2's precedence rule flips
// which collector most recently confirmed an unchanged reading.
//
// Used ONLY for change detection, never for what is placed on the wire: a
// delta frame's "changed" list always carries the caller's UNMASKED Evidence,
// exactly as fpp.changed always broadcasts the unmasked instance even though a
// masked copy decided whether to send it at all.
func maskEvidenceForDiff(o v1.Evidence) v1.Evidence {
	o.CollectedAt = nil
	if o.State == string(observation.StateCurrent) {
		o.ObservedAt = nil
		o.Source = ""
		o.ValidForSeconds = nil
	}
	return o
}

// updateObservationDeltas compares obs (an FPP instance's resolved,
// already-sorted [v1.Evidence] list, exactly as [mapFPPInstance] produces
// it) against what this hub last recorded for key in
// lastObservationsBySignal, using [maskEvidenceForDiff] for the comparison
// so a mere re-poll of an unchanged value is never reported as "changed"
// here — reproducing that exact volume problem one layer down from
// fpp.changed is precisely what ADR-023 exists to avoid. It always replaces
// the cached set with obs's current signals, unconditionally, mirroring
// [Hub.updateRendered]'s own "always advance the cache, report whether it
// moved" contract.
//
// changed carries every signal's full, UNMASKED [v1.Evidence] — never the
// masked copy used only to decide whether it counts as changed — for every
// signal that is new or whose masked rendering differs from last time,
// in the same order obs was given in (already sorted, per
// [mapFPPInstance]/[sortObservations]). removed carries the signal ID,
// sorted for a deterministic wire order despite iterating a Go map, of
// every signal this hub previously held for key that is no longer present
// in obs at all — the "cape swapped for a smaller one" case ADR-023's
// Context section names.
func (h *Hub) updateObservationDeltas(key string, obs []v1.Evidence) (changed []v1.Evidence, removed []string) {
	current := make(map[string][]byte, len(obs))
	for _, o := range obs {
		b, err := json.Marshal(maskEvidenceForDiff(o))
		if err != nil {
			// Unreachable for a v1.Evidence built by mapEvidence; see
			// [Hub.updateRendered]'s identical posture for a marshal
			// failure. Skipping this signal for this pass (rather than
			// treating it as changed, or crashing) is the same
			// fail-safe-to-"no frame" posture the rest of this file takes
			// for a rendering error.
			h.logger.Warn("stream hub: failed to render evidence for delta change detection", "key", key, "signal", o.Signal, "error", err)
			continue
		}
		current[o.Signal] = b
	}

	h.mu.Lock()
	prev := h.lastObservationsBySignal[key]
	h.lastObservationsBySignal[key] = current
	h.mu.Unlock()

	for _, o := range obs {
		b, ok := current[o.Signal]
		if !ok {
			continue // marshal failed above; already logged, already skipped
		}
		if pb, ok2 := prev[o.Signal]; !ok2 || !bytes.Equal(pb, b) {
			changed = append(changed, o)
		}
	}
	for sig := range prev {
		if _, ok := current[sig]; !ok {
			removed = append(removed, sig)
		}
	}
	sort.Strings(removed)
	return changed, removed
}

// evictRendered removes every key with the given prefix from
// h.lastRendered that is not in present. present is this render pass's
// complete, just-listed membership for one resource kind, built only
// inside [Hub.render]'s success branch for that kind: a failed
// Snapshot/ListInstances call must never reach this method at all, because
// "the store errored" is not evidence that a resource is gone — see
// render()'s own doc comment on why a dependency error simply skips that
// kind for the pass instead of treating it as an empty list.
//
// Without this, h.lastRendered (finding 1.6) keeps one full JSON rendering
// per resource forever, evicting nothing: anyone with MQTT broker publish
// rights can already forge an arbitrary node ID into the coordinator's
// SQLite inventory (this is Step 2's own threat model, not a new one), and
// every forged node's rendering would otherwise be pinned in coordinator
// memory for the process's entire lifetime even after the row is deleted.
// This is silent, not a broadcast: contract section 6.4 and ADR-020 both
// say v1 carries no deletion event, so a resource disappearing from a
// render pass produces no frame — only a forgotten diff key, so that if
// the same resource ID legitimately reappears later it is treated as new
// (correctly re-announced) rather than compared against a stale rendering
// from before it vanished.
//
// ADR-023 adds two sibling caches keyed by the exact same strings —
// lastRenderedInstanceOnly and lastObservationsBySignal, both described on
// the [Hub] struct — and this method evicts a departed key from both of
// them too, in the same pass, so all three caches can never disagree about
// which instances currently exist. A node: key never appears in either
// (they exist only for "fpp:" keys — see [Hub.render]'s fpp instance loop,
// the only caller that ever writes to them), so deleting a node: key from
// either is always a harmless no-op.
func (h *Hub) evictRendered(prefix string, present map[string]struct{}) {
	h.mu.Lock()
	defer h.mu.Unlock()
	for key := range h.lastRendered {
		if len(key) <= len(prefix) || key[:len(prefix)] != prefix {
			continue
		}
		if _, ok := present[key]; !ok {
			delete(h.lastRendered, key)
			delete(h.lastRenderedInstanceOnly, key)
			delete(h.lastObservationsBySignal, key)
		}
	}
}

// updateRendered reports whether v's JSON rendering differs from what was
// last stored under key, updating the stored value when it does. This is
// the hub's entire change-detection mechanism (contract section 6.5:
// "determined by comparing the resource's rendered wire representation to
// the last one published").
func (h *Hub) updateRendered(key string, v any) bool {
	b, err := json.Marshal(v)
	if err != nil {
		// Unreachable for a v1.Node/v1.FPPInstance built by this package's
		// own mapping functions (every field is a plain JSON-marshalable
		// type); treated as "no change" rather than crashing the hub.
		h.logger.Warn("stream hub: failed to render resource for change detection", "key", key, "error", err)
		return false
	}

	h.mu.Lock()
	defer h.mu.Unlock()
	prev, ok := h.lastRendered[key]
	if ok && bytes.Equal(prev, b) {
		return false
	}
	h.lastRendered[key] = b
	return true
}

// renderNewEvents fetches events recorded since the last render pass and
// returns one event.recorded pendingFrame per event, in ascending seq
// order. See [hubEventsBatchLimit] for why a single pass may not catch up
// entirely, and why that is safe.
//
// The store prunes history, so ListEvents can report gap: true — this
// hub's own internal cursor fell so far behind (a long tick interval, a
// very aggressive retention policy) that one or more events were pruned
// before this hub ever read them. Unlike the REST events handler, which
// surfaces gap on the wire so a caller can see the missing interval (see
// [v1.EventsResponse]), the SSE stream has no "history gap" event type to
// produce — event.recorded is a live feed, not a paged history read, and
// contract section 6.4 already gives clients a distinct, deliberate
// mechanism for "your model may be incomplete, re-synchronize": a stream
// gap or overflow, not a per-event notification about entries that no
// longer exist to describe. So this method's only obligation on a gap is
// to stop retrying the pruned interval forever: it advances the internal
// cursor to the oldest row the store still retains, sacrificing the
// pruned events (which are gone regardless of what this hub does) rather
// than calling ListEvents with the same stale since on every future tick
// and always getting zero records back for a region that will never
// un-prune. The degenerate case — retention has pruned the events table
// down to nothing at all, so there is no "oldest row" to skip to either —
// is handled the same way, one step further: see the innermost switch
// case below for why jumping straight to latest is still correct there,
// and TestRenderNewEventsAdvancesCursorWhenHistoryIsFullyPruned in
// stream_test.go for the regression guard on the infinite-retry hazard
// this closes.
func (h *Hub) renderNewEvents(ctx context.Context, now time.Time) []pendingFrame {
	h.mu.Lock()
	since := h.lastEventSeq
	h.mu.Unlock()

	latest, err := h.deps.Events.LatestEventSeq(ctx)
	if err != nil {
		h.logger.Warn("stream hub: read latest event seq failed", "error", err)
		return nil
	}
	if latest <= since {
		return nil
	}

	records, gap, err := h.deps.Events.ListEvents(ctx, since, hubEventsBatchLimit)
	if err != nil {
		h.logger.Warn("stream hub: list events since failed", "error", err)
		return nil
	}

	frames := make([]pendingFrame, 0, len(records))
	newCursor := since
	for _, rec := range records {
		ev := mapEvent(rec)
		frames = append(frames, pendingFrame{event: "event.recorded", serverTime: formatTime(now), ev: &ev})
		if rec.Seq > newCursor {
			newCursor = rec.Seq
		}
	}

	if gap {
		switch oldest, ok, oerr := h.deps.Events.OldestEventSeq(ctx); {
		case oerr != nil:
			h.logger.Warn("stream hub: read oldest event seq failed after a reported gap", "error", oerr)
		case ok && oldest > 0 && oldest-1 > newCursor:
			newCursor = oldest - 1
		case !ok && latest > newCursor:
			// The events table currently holds no rows at all — every
			// event that ever existed up to latest has been pruned — so
			// OldestEventSeq has nothing to report (ok is false) and the
			// oldest-1 skip above cannot fire. Left alone, newCursor would
			// stay at since forever: latest (read from durable
			// bookkeeping that survives the table emptying, see
			// [store.Store.LatestEventSeq]'s doc comment) would still be
			// greater than since on every future tick, so this method
			// would call ListEvents with the SAME since, get gap: true
			// and zero records back, and retry identically forever — the
			// hazard finding 1.4 named. There is nothing further
			// ListEvents could ever return for this range (the table is
			// empty), so jump straight to latest: any event appended
			// after this point gets its own fresh seq beyond latest and
			// is picked up normally on a later tick.
			newCursor = latest
		}
	}

	h.mu.Lock()
	if newCursor > h.lastEventSeq {
		h.lastEventSeq = newCursor
	}
	h.mu.Unlock()

	return frames
}

// broadcast delivers pf to every current subscriber WHOSE audience it
// matches (see [frameAudience.includes] — ADR-023's per-subscriber filter,
// checked here rather than by never queuing the frame for an excluded
// subscriber in the first place, since one pf can match some subscribers
// and not others), non-blocking. A subscriber whose frames buffer is full
// has clearly fallen behind (its own ServeHTTP goroutine is not draining
// fast enough — a slow client, a stalled network write); rather than block
// this call (which would stall every other subscriber's delivery, and
// eventually the render loop itself) or silently drop the frame, it is
// handed a stream.reset reason through its capacity-1 reset channel, which
// ServeHTTP's loop picks up on its next iteration and uses to close that
// one connection. Every other subscriber is unaffected.
func (h *Hub) broadcast(pf pendingFrame) {
	h.mu.Lock()
	defer h.mu.Unlock()
	for id, sub := range h.subscribers {
		if !pf.audience.includes(sub.deltas) {
			continue
		}
		select {
		case sub.frames <- pf:
		default:
			select {
			case sub.reset <- "subscriber_too_slow":
				// Logged, not silent: dropping a subscriber is a real
				// operational event (a client that cannot keep up, or a
				// peer that has stopped reading its socket), and the
				// client learns of it only through the stream.reset frame
				// it is about to be sent — which it may or may not still
				// be able to read. Without this line the coordinator
				// disconnects a client and keeps no record anywhere that
				// it did.
				h.logger.Warn("stream hub: subscriber buffer overflowed; sending stream.reset and disconnecting",
					"subscriber", id, "buffer_size", h.bufSize, "reason", "subscriber_too_slow")
			default:
				// Already signaled for this subscriber; it just hasn't
				// been torn down yet.
			}
		}
	}
}

// subscribe registers a new subscriber, fixing its deltas flag (ADR-023
// decision 1) and cred (ADR-024 decision 5 — nil when the connection
// opened with no credential) for the connection's whole lifetime — see
// [subscriber]'s doc comment.
func (h *Hub) subscribe(deltas bool, cred *streamCredential) (id uint64, sub *subscriber) {
	h.mu.Lock()
	defer h.mu.Unlock()
	id = h.nextID
	h.nextID++
	sub = &subscriber{
		frames: make(chan pendingFrame, h.bufSize),
		reset:  make(chan string, 1),
		deltas: deltas,
		cred:   cred,
	}
	h.subscribers[id] = sub
	return id, sub
}

func (h *Hub) unsubscribe(id uint64) {
	h.mu.Lock()
	defer h.mu.Unlock()
	delete(h.subscribers, id)
}

// revalidateSubscribers re-presents every credential-bearing subscriber's
// original secret to h.identitySvc and, for any that no longer
// authenticates (revoked, generation bumped, disabled principal, idle-
// expired session, revoked or expired token), signals its reset channel
// with "credential_invalidated" — the exact mechanism [Hub.broadcast]
// already uses for an overflowed buffer, reused rather than duplicated,
// which is also what satisfies ADR-024 decision 5's "it does not emit a
// new event kind to announce it": the wire-visible SSE event name is
// still stream.reset either way, only the reason string differs, and
// ADR-023 already established that a stream.reset reason is informational
// (a client need not recognize "subscriber_too_slow" to do the right
// thing — reconnect and re-snapshot).
//
// A subscriber with no credential (cred == nil — the ordinary case while
// reads are open and this connection presented none) is never touched: it
// has nothing to revalidate and this mechanism has no opinion about it.
//
// I/O (an identity.Service call per credential-bearing subscriber) is
// deliberately done AFTER releasing h.mu, on a snapshot of the relevant
// subscriber state taken under the lock — mirroring [Hub.render]'s own
// posture of never holding h.mu across a call that can block.
func (h *Hub) revalidateSubscribers(ctx context.Context, now time.Time) {
	type check struct {
		id   uint64
		cred streamCredential
	}

	h.mu.Lock()
	var checks []check
	for id, sub := range h.subscribers {
		if sub.cred != nil {
			checks = append(checks, check{id: id, cred: *sub.cred})
		}
	}
	h.mu.Unlock()

	for _, c := range checks {
		var err error
		switch c.cred.form {
		case identity.FormSession:
			// RevalidateSession, never AuthenticateSession: this tick is a
			// periodic re-check the connection itself triggers, not a use
			// of the credential by an operator — see [identity.Service.
			// RevalidateSession]'s doc comment. A review finding
			// (reproduced directly) caught that calling
			// AuthenticateSession here touched LastUsedAt every tick
			// (defaultStreamTickInterval, 5s in production), which meant a
			// browser tab merely left open in the background — nobody
			// there, nothing "used" — slid decision 5's 90-day idle window
			// forever, making it unenforceable for exactly the abandoned-
			// device case it exists to catch, while also writing one
			// UPDATE per tick per open connection for no attribution
			// benefit.
			_, err = h.identitySvc.RevalidateSession(ctx, c.cred.secret, now)
		case identity.FormToken:
			_, err = h.identitySvc.RevalidateToken(ctx, c.cred.secret)
		default:
			// Unreachable: [Hub.ServeHTTP] only ever constructs a
			// streamCredential from an authContext whose Form is one of
			// these two (see resolveCredential in auth.go, this
			// connection's only source of a non-nil cred).
			continue
		}
		if err == nil {
			continue
		}

		h.mu.Lock()
		sub, ok := h.subscribers[c.id]
		h.mu.Unlock()
		if !ok {
			continue // already disconnected for an unrelated reason
		}
		select {
		case sub.reset <- "credential_invalidated":
			h.logger.Warn("stream hub: subscriber's credential no longer authenticates; closing the connection",
				"subscriber", c.id)
		default:
			// Already signaled for this subscriber; it just hasn't been
			// torn down yet — mirrors [Hub.broadcast]'s identical
			// non-blocking send.
		}
	}
}

// ServeHTTP implements the SSE endpoint, GET /api/v1/stream. Contract
// section 6.4's mechanics, all enforced here:
//
//   - stream.start is always the first event, before this handler enters
//     its main loop, carrying snapshotRequired: true.
//   - No "id:" line is ever written — see [writeSSE] — and any
//     Last-Event-ID request header is never read, anywhere in this method,
//     which is the actual enforcement; a browser or client that sends one
//     gets no different behavior than one that does not.
//   - seq starts at 1 for the first frame after stream.start and increments
//     per connection, never shared with any other subscriber or with
//     [EventRecord.Seq]'s durable history cursor.
//   - A keepalive comment is written on a fixed interval so an idle stream
//     still traverses intermediaries and a dead peer is detectable.
//
// ADR-023: a request whose query carries exactly "?deltas=1" opts this ONE
// connection into delta frames (fpp.observations.changed, plus a narrower
// fpp.changed — see [Hub.render]'s fpp instance loop and [frameAudience]).
// Any other value — absent, empty, "0", "true", anything but the literal
// string "1" — leaves the connection on the pre-ADR-023 full-frame path
// with no behavioral difference whatsoever from a coordinator that has
// never heard of deltas; this is deliberately the strictest possible
// reading of the query parameter, not a lenient truthy check, because
// decision 1's entire additive-compatibility argument rests on "a
// connection that does not ask [for deltas] receives exactly what it
// receives today" — a parameter this handler interpreted loosely could
// silently flip an existing client's behavior the moment it added an
// unrelated "deltas=..." query parameter of its own for some other reason.
func (h *Hub) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	deltas := r.URL.Query().Get("deltas") == "1"

	// ADR-024 decision 5: carry forward whatever credential authenticated
	// THIS request (resolved once, upstream, by withIdentity in auth.go —
	// this handler is mounted behind that middleware exactly like every
	// other route) for the connection's whole lifetime, so
	// [Hub.revalidateSubscribers] can re-present it later. nil when this
	// connection opened with no credential at all (ac.ok is false), which
	// is the ordinary case while reads are open.
	var cred *streamCredential
	if ac := authFromContext(r.Context()); ac.ok {
		cred = &streamCredential{form: ac.result.Form, secret: ac.raw}
	}

	flusher, ok := w.(http.Flusher)
	if !ok {
		http.Error(w, "streaming unsupported", http.StatusInternalServerError)
		return
	}

	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("Connection", "keep-alive")
	w.Header().Set("X-Accel-Buffering", "no")
	w.WriteHeader(http.StatusOK)

	// internal/coordinator/httpapi.NewServer configures a WriteTimeout on
	// the *http.Server this handler is mounted on — a reasonable default
	// for its own ordinary REST-style probes — but net/http.Server.
	// WriteTimeout bounds the ENTIRE response-writing phase of one
	// request, reset only when that connection's NEXT request's headers
	// are read, which never happens here because this response is
	// intentionally never-ending. Left in place unmodified, every SSE
	// connection would be silently killed by the coordinator's own HTTP
	// server a few seconds after connecting — discovered only by actually
	// running the real binary and watching a real stream die
	// mid-keepalive, exactly the shape of defect this task's real-process
	// harness exists to catch (see TestStreamSurvivesServerWriteTimeout).
	//
	// [resetWriteDeadline] is called before every single write below
	// instead: it defeats httpapi's blanket timeout the same way an
	// unconditional clear would, but — unlike a clear — leaves a bounded
	// deadline of this handler's OWN choosing in place for each write,
	// closing finding 1.1 (see [streamWriteTimeout]'s doc comment for the
	// failure that fix closes).

	id, sub := h.subscribe(deltas, cred)
	defer h.unsubscribe(id)

	start := v1.StreamStart{
		StreamID:         uuid.NewString(),
		APIVersion:       1,
		ServerTime:       formatTime(h.clock()),
		SnapshotRequired: true,
	}
	resetWriteDeadline(w)
	if !writeSSE(w, "stream.start", start) {
		return
	}
	flusher.Flush()

	keepalive := time.NewTicker(h.keepaliveInterval)
	defer keepalive.Stop()

	var seq uint64
	for {
		select {
		case <-r.Context().Done():
			return
		case <-h.done:
			return
		case reason := <-sub.reset:
			seq++
			resetWriteDeadline(w)
			writeSSE(w, "stream.reset", v1.StreamReset{
				Seq: seq, ServerTime: formatTime(h.clock()), Reason: reason, SnapshotRequired: true,
			})
			flusher.Flush()
			return
		case pf := <-sub.frames:
			seq++
			event, payload := pf.materialize(seq)
			resetWriteDeadline(w)
			if !writeSSE(w, event, payload) {
				return
			}
			flusher.Flush()
		case <-keepalive.C:
			resetWriteDeadline(w)
			if _, err := w.Write([]byte(": keepalive\n\n")); err != nil {
				return
			}
			flusher.Flush()
		}
	}
}

// writeSSE writes one SSE frame: "event: <event>\ndata: <json>\n\n". It
// never writes an "id:" line — see the [Hub.ServeHTTP] doc comment for why
// that omission is load-bearing, not stylistic. It reports false on any
// write or encode failure, which callers treat as "the connection is
// gone, stop".
func writeSSE(w http.ResponseWriter, event string, payload any) bool {
	data, err := json.Marshal(payload)
	if err != nil {
		return false
	}
	if _, err := fmt.Fprintf(w, "event: %s\ndata: %s\n\n", event, data); err != nil {
		return false
	}
	return true
}
