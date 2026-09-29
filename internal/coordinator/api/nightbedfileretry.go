package api

import (
	"context"
	"time"

	"github.com/showmeshsystems/showmesh/internal/coordinator/config"
	"github.com/showmeshsystems/showmesh/internal/coordinator/store"
	pkgaudio "github.com/showmeshsystems/showmesh/pkg/audio"
)

// A bed item's file reaches a node through asset sync, which runs on its
// own schedule and has no relationship to when the night controller first
// applies the playlist. A node the file has not reached yet refuses the
// attempt ("media not ready: asset ... is not present"), and before this
// file nothing ever tried again: that node stayed silent until an
// operator deployed the cue catalog or saved the playlist by hand, which
// on the rehearsal rig meant a whole night with no bed on one speaker.
//
// This gives the node the bed once its own asset inventory finally holds
// every file the bed needs, through the same
// [handlers.nightGiveNodeTheBedNow] the rejoin path uses, so a recovered
// node rejoins by exactly the route a healthy one takes: apply, gain,
// start, under a new revision.
//
// WHAT COUNTS AS "the file has arrived" is the node's OWN inventory
// report, the same evidence Night readiness already checks bed coverage
// against ([handlers.nightCheckBedTargetCoverage]): a complete report
// holding each item's content hash under the exact runtime filename the
// apply pins. Not the coordinator's own manifest, which says what the
// node SHOULD hold, and not the refusal text, which is an agent's prose
// and would tie this to its current wording.
//
// The trigger is deliberately the coverage itself rather than a
// transition into it. A node that never had the files and now has them is
// the case the issue names; a node that always had them never reaches
// here, because its own attempt confirmed.

// nightBedNodeMissingItemFile returns the first bed item nodeID's own
// inventory does not hold, or ok false when it holds every one of them.
// A node that has never reported an inventory, or whose last report did
// not complete, is treated as missing: absence of evidence is not
// evidence the file arrived, and re-applying on it would be a guess.
func nightBedNodeMissingItemFile(ctx context.Context, deps Dependencies, nodeID string, items []pkgaudio.PlaylistItem) (missing string, complete bool) {
	if deps.AssetManifests == nil || len(items) == 0 {
		return "", false
	}
	report, err := deps.AssetManifests.GetNodeAssetReport(ctx, nodeID)
	if err != nil || !report.Complete {
		return "", false
	}
	inventory, err := deps.AssetManifests.GetNodeAssetInventory(ctx, nodeID)
	if err != nil {
		return "", false
	}
	for _, item := range items {
		if !nightInventoryHoldsUnderFilename(inventory, item.Media.ContentHash, item.Media.RuntimeFilename) {
			return item.Media.RuntimeFilename, true
		}
	}
	return "", true
}

// nightBedAttemptWentNowhere reports whether nodeID's own latest step is a
// resolved attempt to get the bed playing that did not succeed, which is
// the only state a late file can rescue.
//
// A step still in flight is left alone, and so is a step the node
// CONFIRMED: an attempt that succeeded is not re-applied when the node's
// inventory changes, which is the issue's own explicit rule. Only apply
// and start qualify: those are the two steps a file the node does not
// hold can stop, and a gain or a fade failing says nothing about files.
//
// A start the node refused because of the PLAYBACK POINT it named is
// explicitly not an attempt that went nowhere: it has its own retry
// without the point still due
// ([handlers.nightAdvanceBackgroundAudioForNode]'s start branch), and
// answering it here instead would apply the bed again before that retry
// was ever sent, produce another point-carrying start, and cycle. A
// speaker left silent while this controller re-applied to it every few
// ticks is the exact failure this file exists to end.
func nightBedAttemptWentNowhere(latest nightBackgroundAudioHistoryRow) bool {
	if latest.Row.State != nightCueStateResolved || latest.Row.Outcome == nightCueOutcomeConfirmed {
		return false
	}
	if nightBedStartPointWasRefused(latest.Step, latest.Row) {
		return false
	}
	switch latest.Step.Kind {
	case nightBGStepApply, nightBGStepStart:
		return true
	}
	return false
}

// nightBedRecoveryAppliedSinceLastConfirmedStart reports whether this
// controller has already given nodeID the bed since the last start that
// node confirmed.
//
// It is the file path's own bound, and it is per CONFIRMED START rather
// than per attempt: a recovery that has not yet produced a playing bed is
// the recovery still in progress, however many steps it has taken, and
// starting another on top of it is how one silent speaker turns into a
// re-apply every few ticks. A confirmed start is the only thing that
// clears it, which is also what stops a failed recovery, a later
// confirmed stop and a fresh failed apply from letting this fire once per
// show cycle for the rest of the night.
func nightBedRecoveryAppliedSinceLastConfirmedStart(steps []nightBackgroundAudioHistoryRow) bool {
	from := 0
	for i, row := range steps {
		if row.Step.Kind == nightBGStepStart && row.Row.State == nightCueStateResolved && row.Row.Outcome == nightCueOutcomeConfirmed {
			from = i + 1
		}
	}
	for _, row := range steps[from:] {
		if row.Step.Kind == nightBGStepApply && row.Step.Recovery {
			return true
		}
	}
	return false
}

// nightBedFilesLandedOperatorReasonNote is the recovery apply's own
// operator-facing note.
const nightBedFilesLandedOperatorReasonNote = "This speaker now has every file the background music needs, so it is being given the music again."

// nightBedRetryOnceFilesLandedForNode is
// [handlers.nightAdvanceBackgroundAudioForNode]'s own late-file check.
// True means this tick handled the node and the caller must not also run
// its ordinary step machine.
//
// Two bounds apply. A node this controller has already given the bed
// since its last confirmed start is left alone
// ([nightBedRecoveryAppliedSinceLastConfirmedStart]), so a recovery in
// progress is never restarted on top of itself, whichever of the two
// recoveries began it. And a node whose most recent recovery ended in a
// start it did not confirm is left alone too
// ([nightBedRecoveryStartFailed]). So a file that lands buys exactly one
// fresh attempt, and a node that will not play the bed even with every
// file present is recorded rather than re-applied on every report.
func (h *handlers) nightBedRetryOnceFilesLandedForNode(ctx context.Context, now time.Time, rec store.NightSessionRecord, show, nodeID, sessionID string, ba *config.NightSessionBackgroundAudio, owner nightBackgroundAudioOwner, items []pkgaudio.PlaylistItem, history []nightBackgroundAudioHistoryRow) bool {
	steps := nightBackgroundAudioStepsForNode(history, nodeID)
	latest, ok := nightBackgroundAudioLatestStepForNode(history, nodeID)
	if !ok || !nightBedAttemptWentNowhere(latest) {
		return false
	}
	if nightBedRecoveryAppliedSinceLastConfirmedStart(steps) {
		return false
	}
	if nightBedRecoveryStartFailed(steps) {
		return false
	}
	// Already playing is the issue's other explicit rule: whatever the
	// ledger stalled on, a node the bed is audibly running on is not
	// re-applied.
	if reading := nightBedReadSession(h.deps.Audio, now, nodeID, sessionID); reading.Present && reading.State == string(pkgaudio.StatePlaying) {
		return false
	}
	if h.nightBedSessionTouchedOutsideLedger(ctx, nodeID, sessionID, latest.Row.ActionRevision) {
		return false
	}
	missing, complete := nightBedNodeMissingItemFile(ctx, h.deps, nodeID, items)
	if !complete {
		return false
	}
	if missing != "" {
		h.logBedFilesStillMissingOnce(rec, nodeID, missing)
		return false
	}
	h.logWarn("night loop: background audio: this node now holds every file the bed needs after an attempt that went nowhere; giving it the bed again",
		"sessionId", rec.ID, "nodeId", nodeID, "show", show,
		"attemptStep", latest.Step.Kind, "attemptOutcome", latest.Row.Outcome, "attemptRevision", latest.Row.ActionRevision)
	h.nightGiveNodeTheBedNow(ctx, now, rec, nodeID, sessionID, ba, owner, items, history,
		nightBedFilesLandedOperatorReasonNote, nightBackgroundAudioCueNameFilesApply)
	return true
}

// logBedFilesStillMissingOnce names the file a node is still waiting for,
// once per (session, node, file), so an operator watching the log sees
// which speaker is waiting on what without a line every tick.
func (h *handlers) logBedFilesStillMissingOnce(rec store.NightSessionRecord, nodeID, missing string) {
	key := rec.ID + "|" + nodeID + "|bed-files"
	if !h.nightBGConfirmFailures.shouldLog(key, []string{missing}) {
		return
	}
	h.logWarn("night loop: background audio: this node's bed attempt went nowhere and it still does not hold every file the bed needs",
		"sessionId", rec.ID, "nodeId", nodeID, "missingFile", missing)
}
