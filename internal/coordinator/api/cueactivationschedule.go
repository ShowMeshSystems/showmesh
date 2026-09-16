package api

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sync"
	"time"

	v1 "github.com/showmeshsystems/showmesh/internal/coordinator/api/v1"
	"github.com/showmeshsystems/showmesh/internal/coordinator/audiosched"
	"github.com/showmeshsystems/showmesh/internal/coordinator/config"
	"github.com/showmeshsystems/showmesh/internal/coordinator/cueactivate"
	"github.com/showmeshsystems/showmesh/internal/coordinator/store"
	pkgaudio "github.com/showmeshsystems/showmesh/pkg/audio"
	"github.com/showmeshsystems/showmesh/pkg/cueactivation"
)

// This file is ADR-049 decision 3's shared scheduling step: the one place
// between resolve ([cueactivate.Decide]/[cueactivate.ResolveDirectCueActivations])
// and dispatch ([handlers.dispatchCueActivations], cuefire.go's own
// direct-fire loop) that chooses a Cue activation's shared multi-node
// start instant, so the Playlist path and the direct-fire path reach the
// identical code rather than two independently-written copies (ADR-049's
// own "not two implementations" rule).
//
// A Cue reaching one (or zero) audio-bearing node is untouched: no
// reading round, no instant, dispatch proceeds exactly as it always has
// (ADR-049's own "a Cue reaching one node behaves exactly as today").

// scheduleProbeStepTimeout bounds how long readScheduleProbe waits for
// any ONE of its dispatches (apply, prepare, clear) before giving up on
// it. executeAudioSessionDispatch moves to a context.WithoutCancel'd
// context before its own wire wait, deliberately, so an abandoned caller
// can never abort a command already durably recorded; that means a
// shortened context passed in here would NOT itself bound the wait. This
// timeout instead bounds how long THIS function waits for a result: an
// abandoned step keeps running in the background exactly as it would for
// any other caller who stopped waiting, and dispatchProbeStep's own
// channel still eventually receives its real outcome.
//
// ADR-049 decision 6: on the rehearsal rig on 2026-09-16, preparing a
// real show MP3 took about 1.4s on the program+ltc node and 2.4s on a
// Raspberry Pi 3B+. The former 400ms bound was shorter than every real
// prepare measured, so it never once let a multi-node Cue start aligned;
// this value is sized to clear the slower of those two measurements with
// real margin.
//
// A plain const, deliberately, NOT a var a test can shrink the way
// cueActivationConfirmDeadline is (cueactivationdispatch.go): a step this
// bound gives up on keeps its own dispatch goroutine running in the
// background, by design (see this comment's own second paragraph above),
// and that goroutine can legitimately outlive the test that started it.
// A mutable package var a later, unrelated test's own cleanup then
// restores would race against that still-running goroutine's read of it
// — caught by go test -race. A test that needs a node to exceed this
// bound pays the real cost of doing so.
const scheduleProbeStepTimeout = 5 * time.Second

// scheduleProbeMaxDeliveryContribution is the ceiling MANAGER DECISION 2
// clamps the clock holder's own measured probe span to before it is
// folded into the delivery bound (SelectAudioStartInstant). It equals
// scheduleProbeStepTimeout deliberately: that timeout already upper-
// bounds the measured span by construction, and this is defense in depth
// against any measurement slop, never a value a well-behaved probe read
// could exceed on its own.
const scheduleProbeMaxDeliveryContribution = scheduleProbeStepTimeout

// scheduleProbeNodeLocks serializes [cueactivation.ScheduleProbeSessionID]
// access per node, across every concurrent scheduling attempt: the
// Playlist loop's own tick and an operator's own direct Fire can resolve
// onto the same node at the same time, and that session id is one fixed
// string per node, so two overlapping attempts' apply, prepare and clear
// calls would otherwise interleave and corrupt each other's evidence.
//
// Package-level, not a *handlers field: the Playlist loop
// (cueactivationloop.go's NewCueActivationLoop) and the direct-fire HTTP
// route each build their OWN *handlers, so a struct field could not be
// shared between the two paths this lock must actually serialize.
var scheduleProbeNodeLocks sync.Map // nodeID string -> chan struct{}, a buffered-1 token

// scheduleProbeNodeLock returns the one token channel every scheduling
// attempt touching nodeID's own probe session must acquire (send a token
// in) and release (receive it back out), creating it on first use. Never
// removed from the map: a node is a small, bounded set for the life of
// this process, so this never grows unbounded the way one per ACTIVATION
// would.
//
// A channel, not a *sync.Mutex: [acquireScheduleProbeNodeLock] needs a
// bounded wait, and a plain sync.Mutex has no timed Lock. The token is
// held for as long as nodeID's own probe session might still be affected
// by THIS attempt's dispatches, including a step this attempt gave up
// WAITING for but did not cancel: see finishScheduleProbeAfter's own doc
// comment for why releasing early, while an abandoned apply or prepare
// may still be in flight, would let a second attempt's own dispatches
// interleave with the first's late arrival.
func scheduleProbeNodeLock(nodeID string) chan struct{} {
	v, _ := scheduleProbeNodeLocks.LoadOrStore(nodeID, make(chan struct{}, 1))
	return v.(chan struct{})
}

// acquireScheduleProbeNodeLock waits up to timeout to place a token in
// lock, never longer: a hung node's own finishScheduleProbeAfter can hold
// this token in the background for up to the dispatch's own much longer
// deadline, and this bound is what keeps a second attempt's own wait from
// paying that full cost. On a false return, no token was placed, so the
// caller owns nothing and must not release it.
func acquireScheduleProbeNodeLock(lock chan struct{}, timeout time.Duration) bool {
	select {
	case lock <- struct{}{}:
		return true
	case <-time.After(timeout):
		return false
	}
}

// awaitScheduleProbeIdle bounded-waits, up to scheduleProbeStepTimeout,
// for nodeID's own [scheduleProbeNodeLock] to be free — idle, meaning no
// probe apply, prepare, or clear from any scheduling attempt (this tick's
// own, a concurrent one, or one this tick abandoned and handed off to
// finishScheduleProbeAfter) is still outstanding — then immediately
// releases it again. It is a readiness gate, never a hold: the real
// dispatch that follows owns no exclusion against some FUTURE probe
// attempt, only this one wait against whatever probe already started.
//
// [handlers.dispatchOneCueActivation] calls this for every audio-bearing
// node right before its own real cue.activate reaches the wire, so that
// dispatch is never published while nodeID's own probe session might
// still be loading media in the background. A single-node activation, or
// a node scheduleCueActivations never touched this tick, finds the lock
// uncontended and returns immediately — an unmeasurable cost, matching
// ADR-049's own "a Cue reaching one node behaves exactly as today."
func awaitScheduleProbeIdle(nodeID string) {
	lock := scheduleProbeNodeLock(nodeID)
	if acquireScheduleProbeNodeLock(lock, scheduleProbeStepTimeout) {
		<-lock
	}
}

// audioBearing is one node this activation batch resolved an audio
// output for, with the media reference [handlers.readScheduleProbe]
// applies onto the probe session.
type audioBearing struct {
	nodeID string
	out    pkgaudio.MediaRef
}

// nodeIDsOfBearing projects bearing's own node ids, in order, for a
// caller (markNodesUnaligned, SelectAudioStartInstant) that only needs
// the ids.
func nodeIDsOfBearing(bearing []audioBearing) []string {
	out := make([]string, 0, len(bearing))
	for _, b := range bearing {
		out = append(out, b.nodeID)
	}
	return out
}

// cueActivationRecordedSchedule is one bearing node's own ADR-049
// decision 3 scheduling outcome, read back from ITS OWN command row (the
// same [act.ActivationID] idempotency key [handlers.
// dispatchOneCueActivation] dispatches under) rather than a fresh probe.
type cueActivationRecordedSchedule struct {
	nodeID          string
	scheduledAtNs   *int64
	unalignedReason string
	// createdAt is the coordinator's own wall-clock moment this row was
	// inserted (store.CommandRecord.CreatedAt, stamped from the store's
	// own clock at InsertCommand time) — see
	// [extendRecordedScheduleToLateNodes]'s own doc comment for what it is
	// used for.
	createdAt time.Time
}

// decodeCueActivationRecordedSchedule reads ScheduledAtNs and
// UnalignedReason back off rec's own stored params: rec.ParamsJSON is
// exactly [json.Marshal] of the dispatched Activation (canonicalParamsJSON,
// cueactivationdispatch.go), so it decodes straight back into one. ok is
// false only when the stored row is somehow not a decodable Activation, a
// caller treats that identically to "never recorded" — probing again
// costs time, never correctness, where trusting a malformed row would
// cost correctness.
func decodeCueActivationRecordedSchedule(rec store.CommandRecord) (scheduledAtNs *int64, unalignedReason string, ok bool) {
	var act cueactivation.Activation
	if err := json.Unmarshal([]byte(rec.ParamsJSON), &act); err != nil {
		return nil, "", false
	}
	return act.ScheduledAtNs, act.UnalignedReason, true
}

// splitCueActivationReplayStatus partitions bearing into nodes whose
// cue.activate was already recorded for THIS activation (recorded, by
// act.ActivationID — the identical idempotency key [handlers.
// dispatchOneCueActivation] uses) and nodes seeing this activation for
// the first time (toProbe). ADR-049 decision 6: the clock reading behind
// a shared instant is taken once per activation and never again, so a
// recorded node's own outcome is read back, never re-derived.
//
// A node whose lookup itself fails — a genuine store error, never
// [store.ErrCommandNotFound] — is treated as unrecorded: probing it again
// costs time, never correctness, while wrongly skipping its probe over a
// transient read failure would.
func (h *handlers) splitCueActivationReplayStatus(ctx context.Context, activations map[string]cueactivation.Activation, bearing []audioBearing) (recorded []cueActivationRecordedSchedule, toProbe []audioBearing) {
	for _, b := range bearing {
		act := activations[b.nodeID]
		rec, err := h.deps.Commands.GetCommandByIdempotencyKey(ctx, act.ActivationID)
		if err != nil {
			toProbe = append(toProbe, b)
			continue
		}
		scheduledAtNs, unalignedReason, ok := decodeCueActivationRecordedSchedule(rec)
		if !ok {
			toProbe = append(toProbe, b)
			continue
		}
		recorded = append(recorded, cueActivationRecordedSchedule{
			nodeID: b.nodeID, scheduledAtNs: scheduledAtNs, unalignedReason: unalignedReason, createdAt: rec.CreatedAt,
		})
	}
	return recorded, toProbe
}

// applyRecordedCueActivationSchedule writes every recorded node's own
// already-known ScheduledAtNs/UnalignedReason back onto activations, with
// no clock read and no probe dispatch of any kind.
func applyRecordedCueActivationSchedule(activations map[string]cueactivation.Activation, recorded []cueActivationRecordedSchedule) {
	for _, r := range recorded {
		act := activations[r.nodeID]
		act.ScheduledAtNs = r.scheduledAtNs
		act.UnalignedReason = r.unalignedReason
		activations[r.nodeID] = act
	}
}

// extendRecordedScheduleToLateNodes gives every node in toProbe — one
// this activation never reached before, for example a node that was
// offline on the tick a peer of the SAME activation was already scheduled
// and dispatched — the identical ScheduledAtNs that peer already carries,
// never a fresh probe: ADR-049 decision 6's "never again" applies to the
// ACTIVATION, not to each node individually, so a late-arriving node must
// still share the one instant its peers already committed to rather than
// start on its own unaligned schedule.
//
// Whether that instant is still usable is judged without reading any
// clock. peer.createdAt is the coordinator's own wall-clock moment the
// peer's own reading was taken (store's InsertCommand, immediately after
// the scheduling call that chose it), and that original selection is
// guaranteed to have placed the instant at least
// settings.ScheduledStartDeliveryBoundMs+ScheduledStartMarginMs — "the
// configured lead" — past the reading it was derived from
// (audiosched.Select's own LeadNs sums that with preroll and the clock
// error bound, both additional and never negative). A late node is given
// the recorded instant only while that guaranteed FLOOR has not yet
// elapsed in wall-clock time; past it, this function cannot tell how much
// real headroom actually remains (the true lead may have included a
// preroll measurement this function never has access to), so the safe
// direction is to report the node unaligned rather than risk handing it a
// start already behind it.
func (h *handlers) extendRecordedScheduleToLateNodes(now time.Time, settings config.AudioSettingsPayload, activations map[string]cueactivation.Activation, recorded []cueActivationRecordedSchedule, toProbe []audioBearing) {
	if len(recorded) == 0 || len(toProbe) == 0 {
		return
	}
	// Every recorded peer of one activation carries the identical
	// ScheduledAtNs/UnalignedReason (scheduleCueActivations writes it
	// identically across nodeIDs on every path below), so any one of them
	// answers for the whole batch — mirrors cueActivationAlignment's own
	// "any one of them answers" reasoning.
	peer := recorded[0]
	configuredLead := time.Duration(settings.ScheduledStartDeliveryBoundMs+settings.ScheduledStartMarginMs) * time.Millisecond
	stillUsable := peer.scheduledAtNs != nil && !peer.createdAt.IsZero() && now.Before(peer.createdAt.Add(configuredLead))
	for _, b := range toProbe {
		act := activations[b.nodeID]
		if stillUsable {
			at := *peer.scheduledAtNs
			act.ScheduledAtNs = &at
			act.UnalignedReason = ""
		} else {
			act.ScheduledAtNs = nil
			reason := peer.unalignedReason
			if reason == "" {
				reason = fmt.Sprintf(
					"node %q joined this activation after its shared start instant was already chosen (on node %q); no clock is read again for an activation already dispatched, and the configured lead has since elapsed",
					b.nodeID, peer.nodeID)
			}
			act.UnalignedReason = reason
		}
		activations[b.nodeID] = act
	}
}

// scheduleCueActivations is ADR-049 decision 3's own entry point. For
// every audio-bearing Activation in activations (outputs.Audio != nil,
// resolved via [cueactivate.Authorize] exactly as [handlers.
// dispatchOneCueActivation] independently re-resolves it per node: this
// never trusts a cached resolution any more than that file's own doc
// comment already insists on): when more than one node is audio-bearing,
// it obtains a fresh per-node media-clock reading (a throwaway apply-and-
// prepare round on [cueactivation.ScheduleProbeSessionID], cleared
// afterward on every path via [handlers.clearScheduleProbe]) and chooses
// ONE instant with [SelectAudioStartInstant], then writes that instant
// (or, on refusal, the concrete reason) back onto every audio-bearing
// Activation in activations, in place.
//
// ADR-049 decision 6: a node whose OWN cue.activate is already recorded
// for this activation (splitCueActivationReplayStatus, by
// act.ActivationID) is never probed again — its own recorded outcome is
// read back instead (applyRecordedCueActivationSchedule). When every
// bearing node is already recorded, this returns having read no clock and
// dispatched nothing at all. When only some are (a late-joining node —
// extendRecordedScheduleToLateNodes), the fresh probe is still skipped
// entirely for the whole batch; only a genuinely first-seen activation (no
// bearing node recorded at all) runs the probe round below.
//
// activations is mutated in place; there is no separate return value for
// the chosen instant because every caller (the Playlist loop and the
// direct-fire route) already reads each node's own outcome off its own
// Activation, and ADR-049's own per-node-attributable-outcome rule means
// the instant belongs on the thing every other per-node fact already
// lives on.
//
// The frozen rule ("an unaligned fallback is never reported as
// synchronized success") applies to this function's own early returns
// too: a return that leaves activations untouched reports aligned=true
// by default (cueActivationAlignment's own zero case), so both bail-out
// paths below that follow a CONFIRMED multi-audio-node batch set a
// concrete UnalignedReason before returning, rather than silently
// leaving one.
func (h *handlers) scheduleCueActivations(ctx context.Context, now time.Time, activations map[string]cueactivation.Activation, issuer cueActivationIssuer, pin *cueactivate.ShowPin) {
	if len(activations) < 2 {
		return
	}
	if h.deps.AssetManifests == nil {
		// Whether this batch is even audio-bearing cannot be determined
		// without AssetManifests (Authorize needs it), so every
		// Activation is marked, not only the ones that would turn out to
		// be audio-bearing: a real misconfiguration reaching this branch
		// at all is not expected to happen, and over-reporting unaligned
		// here is the safe direction to be wrong in.
		h.logWarn("cue activation schedule: no asset manifest store configured; every node starts on arrival")
		markAllUnaligned(activations, "no asset manifest store is configured on this coordinator, so no start instant could be selected")
		return
	}
	inventoryInterval := h.deps.AssetSettings.InventoryInterval()
	reconnectedAt := h.deps.BrokerConnection.ConnectedSince()

	var bearing []audioBearing
	for nodeID, act := range activations {
		_, _, outputs, _, err := cueactivate.Authorize(ctx, h.deps.AssetManifests, now, inventoryInterval, reconnectedAt, nodeID, act, pin)
		if err != nil || outputs.Audio == nil {
			continue
		}
		contentHash := ""
		if len(outputs.Audio.AssetHashes) > 0 {
			contentHash = outputs.Audio.AssetHashes[0]
		}
		bearing = append(bearing, audioBearing{nodeID: nodeID, out: pkgaudio.MediaRef{
			AssetID: outputs.Audio.Asset, ContentHash: contentHash, RuntimeFilename: outputs.Audio.Filename,
		}})
	}
	if len(bearing) < 2 {
		return
	}

	recorded, toProbe := h.splitCueActivationReplayStatus(ctx, activations, bearing)
	// Applied unconditionally and first: never depends on audio.settings
	// or a fresh clock reading, so a later failure below (an unreadable
	// audio.settings, for instance) must never overwrite what a recorded
	// node already legitimately carries.
	applyRecordedCueActivationSchedule(activations, recorded)
	if len(toProbe) == 0 {
		// Every bearing node's own cue.activate is already recorded for
		// this activation: no clock is read, nothing is dispatched.
		return
	}

	mediaByNode := make(map[string]pkgaudio.MediaRef, len(toProbe))
	nodeIDs := make([]string, 0, len(toProbe))
	for _, b := range toProbe {
		mediaByNode[b.nodeID] = b.out
		nodeIDs = append(nodeIDs, b.nodeID)
	}

	settings, err := h.alignedStartSettings(ctx)
	if err != nil {
		h.logWarn("cue activation schedule: read audio.settings failed; every node starts on arrival", "error", err)
		markNodesUnaligned(activations, nodeIDs, "could not read audio.settings to select a shared start instant: "+err.Error())
		return
	}

	if len(recorded) > 0 {
		// A partial replay (some bearing nodes already recorded, at least
		// one not): never probe again for this activation, ADR-049
		// decision 6 — extend the already-chosen instant to the
		// newly-seen nodes instead, or report them unaligned with a
		// concrete reason.
		h.extendRecordedScheduleToLateNodes(now, settings, activations, recorded, toProbe)
		return
	}

	read := func(ctx context.Context, nodeID string) (reading AudioStartInstantReading, err error) {
		holdsClock, err := h.nodeHoldsMediaClock(ctx, nodeID)
		if err != nil {
			return AudioStartInstantReading{}, err
		}
		// Recovered here, not only in SelectAudioStartInstant's own
		// outer recover: a panic inside readScheduleProbe would
		// otherwise unwind straight through this closure's return,
		// losing holdsClock (already known, above) along with it. That
		// loss matters: if THIS node is the clock holder, losing
		// HoldsMediaClock is what would make pickClock report "no
		// target holds the shared media clock" instead of naming the
		// holder's own panic.
		defer func() {
			if r := recover(); r != nil {
				reading = AudioStartInstantReading{HoldsMediaClock: holdsClock}
				err = fmt.Errorf("panic reading node %q: %v", nodeID, r)
			}
		}()
		evidence, elapsed, probeErr := h.readScheduleProbe(ctx, now, nodeID, mediaByNode[nodeID], issuer)
		if probeErr != nil {
			return AudioStartInstantReading{HoldsMediaClock: holdsClock}, probeErr
		}
		return AudioStartInstantReading{HoldsMediaClock: holdsClock, Evidence: evidence, ProbeElapsed: elapsed}, nil
	}

	sel, nodeErrs, selErr := SelectAudioStartInstant(ctx, nodeIDs, settings, read)
	for _, ne := range nodeErrs {
		h.logWarn("cue activation schedule: node's own reading failed", "nodeId", ne.NodeID, "error", ne.Err)
	}
	for _, nodeID := range nodeIDs {
		act := activations[nodeID]
		if selErr != nil {
			var noClock *audiosched.ErrNoUsableClock
			if !errors.As(selErr, &noClock) {
				h.logWarn("cue activation schedule: select start instant failed; every node starts on arrival", "error", selErr)
			}
			act.ScheduledAtNs = nil
			act.UnalignedReason = audiosched.DescribeUnscheduled(selErr)
		} else {
			at := sel.ScheduledAtNs
			act.ScheduledAtNs = &at
			act.UnalignedReason = ""
		}
		activations[nodeID] = act
	}
}

// markAllUnaligned sets ScheduledAtNs=nil and UnalignedReason=reason on
// every Activation in activations, in place.
func markAllUnaligned(activations map[string]cueactivation.Activation, reason string) {
	for nodeID, act := range activations {
		act.ScheduledAtNs = nil
		act.UnalignedReason = reason
		activations[nodeID] = act
	}
}

// markNodesUnaligned is [markAllUnaligned] narrowed to nodeIDs: used once
// the audio-bearing set has actually been resolved, so only the
// Activations this scheduling attempt was ever about are marked, never a
// render-only sibling in the same batch that never attempted scheduling
// at all.
func markNodesUnaligned(activations map[string]cueactivation.Activation, nodeIDs []string, reason string) {
	for _, nodeID := range nodeIDs {
		act := activations[nodeID]
		act.ScheduledAtNs = nil
		act.UnalignedReason = reason
		activations[nodeID] = act
	}
}

// cueActivationAlignment reports the ONE aligned/unaligned verdict
// [scheduleCueActivations] chose for activations, read back off the
// Activations it mutated: every audio-bearing Activation in one
// scheduling attempt carries the identical ScheduledAtNs (or the
// identical UnalignedReason), so any one of them answers for the whole
// batch. A Cue that never attempted scheduling at all (zero or one
// audio-bearing node) reports aligned=true with no instant and no
// reason, ADR-049's own "a Cue reaching one node behaves exactly as
// today", never reported as an unaligned failure it never attempted.
func cueActivationAlignment(activations map[string]cueactivation.Activation) (aligned bool, unalignedReason string, scheduledAtNs *int64) {
	for _, act := range activations {
		if act.ScheduledAtNs != nil {
			return true, "", act.ScheduledAtNs
		}
		if act.UnalignedReason != "" {
			return false, act.UnalignedReason, nil
		}
	}
	return true, "", nil
}

// audioDispatchOutcome bundles executeAudioSessionDispatch's three
// return values so a bounded wait can pass them through one channel.
type audioDispatchOutcome struct {
	result  v1.AudioSessionCommandResult
	problem *v1.Problem
	err     error
}

// dispatchProbeStep starts in on its own goroutine and returns a channel
// that always eventually receives exactly one outcome, however long the
// underlying dispatch actually takes: nothing here cancels it, since
// executeAudioSessionDispatch moves to its own context.WithoutCancel
// before the wire wait specifically so an abandoned caller can never
// abort a command already durably recorded (see scheduleProbeStepTimeout's
// own doc comment). This is what lets awaitProbeStep give up on the
// channel without giving up on the dispatch itself.
//
// A panic inside executeAudioSessionDispatch (store, audit or broker) is
// recovered here, turned into this step's own error: this goroutine has
// no caller left to recover it once dispatchProbeStep itself has
// returned, so an unrecovered panic here would take down the whole
// coordinator process mid show, not just this one node's own reading.
func (h *handlers) dispatchProbeStep(ctx context.Context, now time.Time, in AudioDispatchInput) <-chan audioDispatchOutcome {
	out := make(chan audioDispatchOutcome, 1)
	go func() {
		defer func() {
			if r := recover(); r != nil {
				out <- audioDispatchOutcome{err: fmt.Errorf("panic dispatching %s for node %q: %v", in.Action, in.NodeID, r)}
			}
		}()
		result, problem, err := h.executeAudioSessionDispatch(ctx, now, in)
		out <- audioDispatchOutcome{result, problem, err}
	}()
	return out
}

// awaitProbeStep waits up to scheduleProbeStepTimeout for pending's own
// result. timedOut is true when the WAIT itself gave up; the underlying
// dispatch is never canceled, and pending still eventually receives its
// real outcome, see dispatchProbeStep's own doc comment.
func awaitProbeStep(pending <-chan audioDispatchOutcome) (out audioDispatchOutcome, timedOut bool) {
	select {
	case out = <-pending:
		return out, false
	case <-time.After(scheduleProbeStepTimeout):
		return audioDispatchOutcome{}, true
	}
}

// finishScheduleProbeAfter is readScheduleProbe's own follow-up for an
// apply or prepare step it gave up waiting for: it waits for that step's
// real completion, however long that actually takes, and only THEN
// dispatches audio.session.clear and releases nodeID's own
// [scheduleProbeNodeLock].
//
// Skipping the wait and clearing immediately is exactly what would let
// clear reach the agent BEFORE a since-delayed apply: [audio.Manager.
// Clear] on a session that does not exist yet is a no-op (nothing to
// destroy), so that ordering lets the late apply arrive afterward,
// create a session nobody is left watching, and leave a permanently
// loaded, never-cleared probe session on a real show node. MQTT delivery
// order from this one coordinator process to one node is what the rest
// of this file's own revision scheme, and [audio.Session.
// dispatchExemptFromStaleRevision]'s own doc comment, already depend on;
// this restores that ordering for the one path (a step this function
// gave up waiting for) that would otherwise break it.
//
// The identical hazard does not apply to clear's OWN timeout: a clear
// that lands late is [audio.Session.dispatchExemptFromStaleRevision]'s
// own already-accepted, self-healing trade (it may tear down a NEWER
// attempt's freshly-applied session, but never leaves an orphan, since
// the next apply for this id always starts a fresh ledger), so
// clearScheduleProbe's own timeout only logs and returns.
//
// If this coordinator shuts down or crashes while waiting on pending,
// clear is never sent: the node keeps a loaded probe session until some
// later apply overwrites it. Accepted, not fixed: a probe session is a
// throwaway resource by construction, and a clean restart already loses
// every other in-flight command's own follow-up the identical way.
//
// Started with a bare go statement, exactly like dispatchProbeStep's own
// goroutine: there is no caller left to recover a panic here once
// readScheduleProbe has already returned, so the recover below is this
// function's own, logging nodeID rather than letting it take down the
// coordinator process over a logging or revision-helper defect.
func (h *handlers) finishScheduleProbeAfter(ctx context.Context, now time.Time, nodeID string, issuer cueActivationIssuer, pending <-chan audioDispatchOutcome, mu chan struct{}) {
	defer func() { <-mu }()
	defer func() {
		if r := recover(); r != nil {
			h.logWarn("cue activation schedule: panic finishing an abandoned probe step", "nodeId", nodeID, "panic", r)
		}
	}()
	<-pending
	h.clearScheduleProbe(ctx, now, nodeID, issuer)
}

// errScheduleProbeBusy is readScheduleProbe's own reason when nodeID's
// [scheduleProbeNodeLock] is already held: a concrete node error, the
// same as a panic or a failed dispatch, so it reaches SelectAudioStartInstant's
// per-node failures and, when this node is the clock holder, the eventual
// UnalignedReason names it rather than a generic "no evidence."
func errScheduleProbeBusy(nodeID string) error {
	return fmt.Errorf("node %q's own probe session is still busy with a previous scheduling attempt", nodeID)
}

// scheduleProbeLockedSectionPanicForTest, when set by a test, panics
// immediately after readScheduleProbe acquires nodeID's own lock. Nil in
// production; it exists only to prove the handedOff-guarded unlock below
// survives a panic from anywhere in this function's own synchronous body,
// not only from dispatchProbeStep's already-recovered goroutine.
var scheduleProbeLockedSectionPanicForTest func()

// readScheduleProbe applies media onto [cueactivation.
// ScheduleProbeSessionID] and prepares it, to read a fresh media-clock
// evidence map without touching nodeID's real show session (which may
// already be Playing the PRECEDING Cue) or the prepare-ahead staging
// session (which a concurrent tick may already be using for a
// DIFFERENT, later Cue), see [cueactivation.ScheduleProbeSessionID]'s
// own doc comment. The probe apply carries only Media: no LTC start
// offset, no mix policy, no announcement role, so it can never start LTC
// or participate in a duck/interrupt relationship even if a caller later
// mistakenly started it (which this function itself never does, Prepare
// only, never Start).
//
// nodeID's own [scheduleProbeNodeLock] is acquired with a wait bounded by
// [scheduleProbeStepTimeout], never an unbounded Lock: a hung node can
// keep that token held in the background (via finishScheduleProbeAfter)
// for up to the dispatch's own much longer deadline, and waiting past
// this bound would pay the very delay this whole step timeout scheme
// exists to remove. Within the bound, though, waiting is the point: two
// healthy overlapping attempts on one node (a Playlist tick and an
// operator Fire within a few hundred milliseconds, for example) are
// common and harmless, and reporting the second one unaligned when it
// only needed to wait a moment is a worse show than the wait itself. A
// node still busy after the full bound is reported as having no reading
// for THIS activation.
//
// Every return path releases the token through the SAME deferred call,
// guarded by handedOff: a panic anywhere in this function's own body,
// not only an explicit early return, still runs that defer during
// unwinding, so the token is never left held by an interrupted
// synchronous path. The only case the defer must NOT release it is a
// handoff to finishScheduleProbeAfter, which takes ownership of the
// token (and its eventual release) for as long as it waits on an
// abandoned step.
//
// Each of apply and prepare is given [scheduleProbeStepTimeout] to
// answer; a step that does not is abandoned, not canceled (see that
// constant's own doc comment), and this returns (nil, 0, nil) immediately
// rather than wait out the dispatch's own much longer wire deadline, so
// one silent node costs THIS attempt at most about two step timeouts,
// never up to 30 seconds. finishScheduleProbeAfter is what still clears
// the probe, correctly ordered after the abandoned step's real
// completion, and releases nodeID's own lock, in that case.
//
// elapsed is how long the PREPARE step specifically took, measured only
// on its own success: SelectAudioStartInstant folds only the clock
// holder's own such span into the delivery bound (MANAGER DECISION 2),
// never a failed or abandoned read's, and never apply's or clear's own
// span, since the staleness that matters is the age of the reading
// itself, not of the round trip that fetched or discarded it.
func (h *handlers) readScheduleProbe(ctx context.Context, now time.Time, nodeID string, media pkgaudio.MediaRef, issuer cueActivationIssuer) (map[string]any, time.Duration, error) {
	mu := scheduleProbeNodeLock(nodeID)
	if !acquireScheduleProbeNodeLock(mu, scheduleProbeStepTimeout) {
		return nil, 0, errScheduleProbeBusy(nodeID)
	}
	handedOff := false
	defer func() {
		if !handedOff {
			<-mu
		}
	}()
	if scheduleProbeLockedSectionPanicForTest != nil {
		scheduleProbeLockedSectionPanicForTest()
	}

	session := cueactivation.ScheduleProbeSessionID
	applyInvocation := "schedule-probe-apply:" + nodeID + ":" + now.Format(time.RFC3339Nano)
	prepareInvocation := "schedule-probe-prepare:" + nodeID + ":" + now.Format(time.RFC3339Nano)
	applyRevision := cueactivation.ScheduleProbeSessionRevision(now, cueactivation.ScheduleProbeSessionStepApply)
	prepareRevision := cueactivation.ScheduleProbeSessionRevision(now, cueactivation.ScheduleProbeSessionStepPrepare)

	applyPending := h.dispatchProbeStep(ctx, now, AudioDispatchInput{
		Action: "audio.session.apply", NodeID: nodeID, SessionID: session,
		Params: map[string]any{
			"sessionId": session, "invocationId": applyInvocation, "revision": applyRevision,
			"sourceRole": string(pkgaudio.SourceRoleShow),
			"media": map[string]any{
				"assetId": media.AssetID, "contentHash": media.ContentHash, "filename": media.RuntimeFilename,
			},
		},
		Revision: applyRevision, IdempotencyKey: applyInvocation,
		IssuerID: issuer.PrincipalID, IssuerName: issuer.PrincipalName,
		IssuerForm: issuer.Form, IssuerCredentialID: issuer.CredentialID,
	})
	applyOut, applyTimedOut := awaitProbeStep(applyPending)
	if applyTimedOut {
		h.logWarn("cue activation schedule: probe apply dispatch timed out", "nodeId", nodeID, "timeout", scheduleProbeStepTimeout)
		handedOff = true
		go h.finishScheduleProbeAfter(ctx, now, nodeID, issuer, applyPending, mu)
		return nil, 0, fmt.Errorf("node %q's own probe apply did not answer within %s", nodeID, scheduleProbeStepTimeout)
	}
	switch {
	case applyOut.err != nil:
		h.logWarn("cue activation schedule: probe apply dispatch failed", "nodeId", nodeID, "error", applyOut.err)
		h.clearScheduleProbe(ctx, now, nodeID, issuer)
		return nil, 0, applyOut.err
	case applyOut.problem != nil:
		h.logWarn("cue activation schedule: probe apply dispatch refused", "nodeId", nodeID, "detail", applyOut.problem.Detail)
		h.clearScheduleProbe(ctx, now, nodeID, issuer)
		return nil, 0, fmt.Errorf("node %q's own probe apply was refused: %s", nodeID, applyOut.problem.Detail)
	case applyOut.result.Outcome == "refused" || applyOut.result.Outcome == "failed":
		h.logWarn("cue activation schedule: probe apply outcome", "nodeId", nodeID, "outcome", applyOut.result.Outcome, "reason", applyOut.result.Reason)
		h.clearScheduleProbe(ctx, now, nodeID, issuer)
		return nil, 0, fmt.Errorf("node %q's own probe apply reported %s: %s", nodeID, applyOut.result.Outcome, applyOut.result.Reason)
	}

	var evidence map[string]any
	prepareStart := time.Now()
	preparePending := h.dispatchProbeStep(ctx, now, AudioDispatchInput{
		Action: "audio.session.prepare", NodeID: nodeID, SessionID: session,
		Params:   map[string]any{"sessionId": session, "invocationId": prepareInvocation, "revision": prepareRevision},
		Revision: prepareRevision, IdempotencyKey: prepareInvocation,
		IssuerID: issuer.PrincipalID, IssuerName: issuer.PrincipalName,
		IssuerForm: issuer.Form, IssuerCredentialID: issuer.CredentialID,
		OnEvidence: func(v map[string]any) { evidence = v },
	})
	prepareOut, prepareTimedOut := awaitProbeStep(preparePending)
	if prepareTimedOut {
		h.logWarn("cue activation schedule: probe prepare dispatch timed out", "nodeId", nodeID, "timeout", scheduleProbeStepTimeout)
		handedOff = true
		go h.finishScheduleProbeAfter(ctx, now, nodeID, issuer, preparePending, mu)
		return nil, 0, fmt.Errorf("node %q's own probe prepare did not answer within %s", nodeID, scheduleProbeStepTimeout)
	}
	prepareElapsed := time.Since(prepareStart)
	switch {
	case prepareOut.err != nil:
		h.logWarn("cue activation schedule: probe prepare dispatch failed", "nodeId", nodeID, "error", prepareOut.err)
		h.clearScheduleProbe(ctx, now, nodeID, issuer)
		return nil, 0, prepareOut.err
	case prepareOut.problem != nil:
		h.logWarn("cue activation schedule: probe prepare dispatch refused", "nodeId", nodeID, "detail", prepareOut.problem.Detail)
		h.clearScheduleProbe(ctx, now, nodeID, issuer)
		return nil, 0, fmt.Errorf("node %q's own probe prepare was refused: %s", nodeID, prepareOut.problem.Detail)
	}
	h.clearScheduleProbe(ctx, now, nodeID, issuer)
	return evidence, prepareElapsed, nil
}

// clearScheduleProbe bounded-waits (see scheduleProbeStepTimeout) to
// clear [cueactivation.ScheduleProbeSessionID] on nodeID. audio.session.
// clear is exempt from stale-revision refusal ([audio.Manager.Clear]'s
// own doc comment), so a fixed, always-fresh revision is sufficient. A
// timeout here is logged and left running in the background rather than
// escalated to finishScheduleProbeAfter's own wait-then-follow-up
// treatment: that treatment exists to stop a LATE APPLY from creating an
// orphan clear could have prevented, and clear itself has no follow-up
// dispatch that a late arrival could get out of order with.
func (h *handlers) clearScheduleProbe(ctx context.Context, now time.Time, nodeID string, issuer cueActivationIssuer) {
	session := cueactivation.ScheduleProbeSessionID
	invocation := "schedule-probe-clear:" + nodeID + ":" + now.Format(time.RFC3339Nano)
	revision := cueactivation.ScheduleProbeSessionRevision(now, cueactivation.ScheduleProbeSessionStepClear)
	pending := h.dispatchProbeStep(ctx, now, AudioDispatchInput{
		Action: "audio.session.clear", NodeID: nodeID, SessionID: session,
		Params:   map[string]any{"sessionId": session, "invocationId": invocation, "revision": revision},
		Revision: revision, IdempotencyKey: invocation,
		IssuerID: issuer.PrincipalID, IssuerName: issuer.PrincipalName,
		IssuerForm: issuer.Form, IssuerCredentialID: issuer.CredentialID,
	})
	out, timedOut := awaitProbeStep(pending)
	if timedOut {
		h.logWarn("cue activation schedule: probe clear dispatch timed out", "nodeId", nodeID, "timeout", scheduleProbeStepTimeout)
		return
	}
	if out.err != nil {
		h.logWarn("cue activation schedule: probe clear dispatch failed", "nodeId", nodeID, "error", out.err)
	} else if out.problem != nil {
		h.logWarn("cue activation schedule: probe clear dispatch refused", "nodeId", nodeID, "detail", out.problem.Detail)
	}
}
