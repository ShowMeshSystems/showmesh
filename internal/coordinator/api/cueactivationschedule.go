package api

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"time"

	v1 "github.com/showmeshsystems/showmesh/internal/coordinator/api/v1"
	"github.com/showmeshsystems/showmesh/internal/coordinator/audiosched"
	"github.com/showmeshsystems/showmesh/internal/coordinator/cueactivate"
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
const scheduleProbeStepTimeout = 400 * time.Millisecond

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
var scheduleProbeNodeLocks sync.Map // nodeID string -> *sync.Mutex

// scheduleProbeNodeLock returns the one *sync.Mutex every scheduling
// attempt touching nodeID's own probe session must hold, creating it on
// first use. Never released back: a node is a small, bounded set for the
// life of this process, so this never grows unbounded the way a lock per
// ACTIVATION would.
//
// The lock is held for as long as nodeID's own probe session might still
// be affected by THIS attempt's dispatches, including a step this
// attempt gave up WAITING for but did not cancel: see
// finishScheduleProbeAfter's own doc comment for why releasing early,
// while an abandoned apply or prepare may still be in flight, would let
// a second attempt's own dispatches interleave with the first's late
// arrival.
func scheduleProbeNodeLock(nodeID string) *sync.Mutex {
	v, _ := scheduleProbeNodeLocks.LoadOrStore(nodeID, &sync.Mutex{})
	return v.(*sync.Mutex)
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

	type audioBearing struct {
		nodeID string
		out    pkgaudio.MediaRef
	}
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

	mediaByNode := make(map[string]pkgaudio.MediaRef, len(bearing))
	nodeIDs := make([]string, 0, len(bearing))
	for _, b := range bearing {
		mediaByNode[b.nodeID] = b.out
		nodeIDs = append(nodeIDs, b.nodeID)
	}

	settings, err := h.alignedStartSettings(ctx)
	if err != nil {
		h.logWarn("cue activation schedule: read audio.settings failed; every node starts on arrival", "error", err)
		markNodesUnaligned(activations, nodeIDs, "could not read audio.settings to select a shared start instant: "+err.Error())
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
		evidence, elapsed := h.readScheduleProbe(ctx, now, nodeID, mediaByNode[nodeID], issuer)
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
func (h *handlers) dispatchProbeStep(ctx context.Context, now time.Time, in AudioDispatchInput) <-chan audioDispatchOutcome {
	out := make(chan audioDispatchOutcome, 1)
	go func() {
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
func (h *handlers) finishScheduleProbeAfter(ctx context.Context, now time.Time, nodeID string, issuer cueActivationIssuer, pending <-chan audioDispatchOutcome, mu *sync.Mutex) {
	defer mu.Unlock()
	<-pending
	h.clearScheduleProbe(ctx, now, nodeID, issuer)
}

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
// Each of apply and prepare is given [scheduleProbeStepTimeout] to
// answer; a step that does not is abandoned, not canceled (see that
// constant's own doc comment), and this returns (nil, 0) immediately
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
func (h *handlers) readScheduleProbe(ctx context.Context, now time.Time, nodeID string, media pkgaudio.MediaRef, issuer cueActivationIssuer) (map[string]any, time.Duration) {
	mu := scheduleProbeNodeLock(nodeID)
	mu.Lock()

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
		go h.finishScheduleProbeAfter(ctx, now, nodeID, issuer, applyPending, mu)
		return nil, 0
	}
	switch {
	case applyOut.err != nil:
		h.logWarn("cue activation schedule: probe apply dispatch failed", "nodeId", nodeID, "error", applyOut.err)
		h.clearScheduleProbe(ctx, now, nodeID, issuer)
		mu.Unlock()
		return nil, 0
	case applyOut.problem != nil:
		h.logWarn("cue activation schedule: probe apply dispatch refused", "nodeId", nodeID, "detail", applyOut.problem.Detail)
		h.clearScheduleProbe(ctx, now, nodeID, issuer)
		mu.Unlock()
		return nil, 0
	case applyOut.result.Outcome == "refused" || applyOut.result.Outcome == "failed":
		h.logWarn("cue activation schedule: probe apply outcome", "nodeId", nodeID, "outcome", applyOut.result.Outcome, "reason", applyOut.result.Reason)
		h.clearScheduleProbe(ctx, now, nodeID, issuer)
		mu.Unlock()
		return nil, 0
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
		go h.finishScheduleProbeAfter(ctx, now, nodeID, issuer, preparePending, mu)
		return nil, 0
	}
	prepareElapsed := time.Since(prepareStart)
	switch {
	case prepareOut.err != nil:
		h.logWarn("cue activation schedule: probe prepare dispatch failed", "nodeId", nodeID, "error", prepareOut.err)
		h.clearScheduleProbe(ctx, now, nodeID, issuer)
		mu.Unlock()
		return nil, 0
	case prepareOut.problem != nil:
		h.logWarn("cue activation schedule: probe prepare dispatch refused", "nodeId", nodeID, "detail", prepareOut.problem.Detail)
		h.clearScheduleProbe(ctx, now, nodeID, issuer)
		mu.Unlock()
		return nil, 0
	}
	h.clearScheduleProbe(ctx, now, nodeID, issuer)
	mu.Unlock()
	return evidence, prepareElapsed
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
