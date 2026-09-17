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

// This file is ADR-049 decision 3's shared scheduling step, choosing a
// Cue activation's shared start instant for both the Playlist path and
// cuefire.go's direct-fire path. A Cue reaching under two nodes is untouched.

// scheduleProbeStepTimeout bounds how long readScheduleProbe waits for any
// ONE of its dispatches (apply, prepare, clear) before abandoning it, kept
// past both nodes' measured real prepare times (roughly 1.4s and 2.4s).
const scheduleProbeStepTimeout = 5 * time.Second

// scheduleProbeMaxDeliveryContribution ceilings a probe round's own
// contribution to the delivery bound. Twice scheduleProbeStepTimeout: the
// bound can count the slowest target's own elapsed twice.
const scheduleProbeMaxDeliveryContribution = 2 * scheduleProbeStepTimeout

// scheduleProbeIdleWaitLogThreshold is how long awaitScheduleProbeIdle may
// wait before that wait is worth a warn log.
const scheduleProbeIdleWaitLogThreshold = 50 * time.Millisecond

// scheduleProbeNodeLocks serializes [cueactivation.ScheduleProbeSessionID]
// access per node. Package level, not a *handlers field: the Playlist
// loop and the direct-fire route each build their own *handlers.
var scheduleProbeNodeLocks sync.Map // nodeID string -> chan struct{}, a buffered-1 token

// scheduleProbeNodeLock returns the one token channel every scheduling
// attempt touching nodeID's own probe session must acquire and release,
// creating it on first use.
func scheduleProbeNodeLock(nodeID string) chan struct{} {
	v, _ := scheduleProbeNodeLocks.LoadOrStore(nodeID, make(chan struct{}, 1))
	return v.(chan struct{})
}

// acquireScheduleProbeNodeLock waits up to timeout to place a token in
// lock. On a false return, no token was placed, so the caller owns
// nothing and must not release it.
func acquireScheduleProbeNodeLock(lock chan struct{}, timeout time.Duration) bool {
	select {
	case lock <- struct{}{}:
		return true
	case <-time.After(timeout):
		return false
	}
}

// awaitScheduleProbeIdle bounded-waits for nodeID's own lock to be free,
// then releases it again: a readiness gate, never a hold against a probe
// that starts after this returns. waited/timedOut let the caller log it.
func awaitScheduleProbeIdle(nodeID string) (waited time.Duration, timedOut bool) {
	start := time.Now()
	lock := scheduleProbeNodeLock(nodeID)
	if !acquireScheduleProbeNodeLock(lock, scheduleProbeStepTimeout) {
		return time.Since(start), true
	}
	<-lock
	return time.Since(start), false
}

// audioBearing is one node this activation batch resolved an audio
// output for, with the media reference readScheduleProbe applies onto
// the probe session.
type audioBearing struct {
	nodeID string
	out    pkgaudio.MediaRef
}

// cueActivationRecordedSchedule is one bearing node's own scheduling
// outcome, read back from its own command row rather than a fresh probe.
type cueActivationRecordedSchedule struct {
	nodeID          string
	scheduledAtNs   *int64
	unalignedReason string
	// createdAt is when this row was inserted: see
	// [extendRecordedScheduleToLateNodes] for what it is used for.
	createdAt time.Time
}

// decodeCueActivationRecordedSchedule reads ScheduledAtNs and
// UnalignedReason back off rec's own stored params. ok is false only when
// the row is not a decodable Activation, treated as never recorded.
func decodeCueActivationRecordedSchedule(rec store.CommandRecord) (scheduledAtNs *int64, unalignedReason string, ok bool) {
	var act cueactivation.Activation
	if err := json.Unmarshal([]byte(rec.ParamsJSON), &act); err != nil {
		return nil, "", false
	}
	return act.ScheduledAtNs, act.UnalignedReason, true
}

// splitCueActivationReplayStatus partitions bearing into nodes already
// recorded for this activation and nodes seeing it for the first time
// (toProbe). A genuine store error is treated as unrecorded, never trusted.
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

// extendRecordedScheduleToLateNodes gives a late-joining node in toProbe
// its peer's already-chosen ScheduledAtNs while the configured lead still
// covers it; past that floor it reports the node unaligned instead.
func (h *handlers) extendRecordedScheduleToLateNodes(now time.Time, settings config.AudioSettingsPayload, activations map[string]cueactivation.Activation, recorded []cueActivationRecordedSchedule, toProbe []audioBearing) {
	if len(recorded) == 0 || len(toProbe) == 0 {
		return
	}
	// Every recorded peer carries the identical ScheduledAtNs/UnalignedReason,
	// so any one of them answers for the whole batch.
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

// scheduleCueActivations reads every audio-bearing node's media clock and
// writes ONE chosen instant, or a concrete refusal reason, onto each
// Activation in activations, in place.
func (h *handlers) scheduleCueActivations(ctx context.Context, now time.Time, activations map[string]cueactivation.Activation, issuer cueActivationIssuer, pin *cueactivate.ShowPin) {
	if len(activations) < 2 {
		return
	}
	if h.deps.AssetManifests == nil {
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
	applyRecordedCueActivationSchedule(activations, recorded)
	if len(toProbe) == 0 {
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
		h.extendRecordedScheduleToLateNodes(now, settings, activations, recorded, toProbe)
		return
	}

	read := func(ctx context.Context, nodeID string) (reading AudioStartInstantReading, err error) {
		holdsClock, err := h.nodeHoldsMediaClock(ctx, nodeID)
		if err != nil {
			return AudioStartInstantReading{}, err
		}
		// Recovered here too so a panic inside readScheduleProbe does not
		// lose the already-known holdsClock along with it.
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

// markNodesUnaligned is [markAllUnaligned] narrowed to nodeIDs, so a
// render-only sibling that never attempted scheduling is never marked.
func markNodesUnaligned(activations map[string]cueactivation.Activation, nodeIDs []string, reason string) {
	for _, nodeID := range nodeIDs {
		act := activations[nodeID]
		act.ScheduledAtNs = nil
		act.UnalignedReason = reason
		activations[nodeID] = act
	}
}

// cueActivationAlignment reports the ONE aligned/unaligned verdict
// [scheduleCueActivations] chose: every audio-bearing Activation carries
// the identical verdict, so any one answers for the whole batch.
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

// dispatchProbeStep starts the dispatch on its own goroutine and returns
// a channel that always eventually receives exactly one outcome. A panic
// inside it is recovered here and turned into this step's own error.
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
// result; timedOut means the wait gave up, never the dispatch itself.
func awaitProbeStep(pending <-chan audioDispatchOutcome) (out audioDispatchOutcome, timedOut bool) {
	select {
	case out = <-pending:
		return out, false
	case <-time.After(scheduleProbeStepTimeout):
		return audioDispatchOutcome{}, true
	}
}

// finishScheduleProbeAfterDoneForTest, when set by a test, is called
// after finishScheduleProbeAfter's own clear and lock release complete,
// so a test can wait for it before its own teardown runs.
var finishScheduleProbeAfterDoneForTest func()

// finishScheduleProbeAfter waits for an abandoned step's real completion,
// then clears the probe session and releases nodeID's own lock, so a
// since-delayed apply can never land after clear.
func (h *handlers) finishScheduleProbeAfter(ctx context.Context, now time.Time, nodeID string, issuer cueActivationIssuer, pending <-chan audioDispatchOutcome, mu chan struct{}) {
	if finishScheduleProbeAfterDoneForTest != nil {
		defer finishScheduleProbeAfterDoneForTest()
	}
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
// lock is already held, so it reaches SelectAudioStartInstant's failures.
func errScheduleProbeBusy(nodeID string) error {
	return fmt.Errorf("node %q's own probe session is still busy with a previous scheduling attempt", nodeID)
}

// scheduleProbeLockedSectionPanicForTest, when set by a test, panics
// immediately after readScheduleProbe acquires nodeID's own lock. Nil in
// production.
var scheduleProbeLockedSectionPanicForTest func()

// readScheduleProbe applies and prepares [cueactivation.ScheduleProbeSessionID]
// to read a fresh media-clock evidence map. elapsed is the PREPARE step's
// own span, folded into the delivery bound.
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

// clearScheduleProbe bounded-waits to clear [cueactivation.ScheduleProbeSessionID]
// on nodeID. A timeout here is logged and left running in the background:
// clear has no follow-up dispatch a late arrival could race.
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
