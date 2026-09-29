package api

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/showmeshsystems/showmesh/internal/coordinator/config"
	"github.com/showmeshsystems/showmesh/internal/coordinator/store"
	"github.com/showmeshsystems/showmesh/pkg/mqttproto"
	"github.com/showmeshsystems/showmesh/pkg/observation"
)

// A bed item's file reaches a node on asset sync's own schedule, not the
// night controller's, so a node can refuse the bed simply because its copy
// has not arrived yet. Before this, nothing tried again: that speaker was
// silent for the night unless an operator deployed the catalog by hand.

// bedItemInventoryForTest is the inventory rows a node holding every one
// of the two bed items would report, matching what
// nightBuildBackgroundPlaylistItems pins on the apply: the asset's own
// content hash under its runtime filename.
func bedItemInventoryForTest(nodeID string, assetIDs ...string) []store.NodeAssetInventoryRecord {
	out := make([]store.NodeAssetInventoryRecord, 0, len(assetIDs))
	for _, assetID := range assetIDs {
		out = append(out, store.NodeAssetInventoryRecord{
			NodeID: nodeID, ContentHash: "sha256:" + assetID,
			RuntimeFilename: assetID + ".mp3", SizeBytes: 12345, VerifiedAt: testNow,
		})
	}
	return out
}

// singleNodeBedRefusedForAMissingFile drives a one-node bed to the state
// the issue describes: the playlist was applied and the node refused to
// start it because a file is not there, and the node holds neither file.
func singleNodeBedRefusedForAMissingFile(t *testing.T, h *handlers, st *store.Store, pub *fakeAudioPublisher) (store.NightSessionRecord, string) {
	t.Helper()
	putBackgroundAudioAsset(t, st, "halloween", "bg-1", "node-a", "asset-1")
	putBackgroundAudioAsset(t, st, "halloween", "bg-2", "node-a", "asset-2")
	ba := twoItemBackgroundAudioConfig("node-a", config.NightSessionBackgroundRepeatPlaylist, config.NightSessionBackgroundResumeResume, config.NightSessionItemTransitionSequential)
	rec := mustCreateRestingSessionWithBackgroundAudio(t, st, "sess-1", "node-a", ba, nightStateRestingIntershow)
	sessionID := nightBackgroundAudioSessionID(rec)

	putNodeInventoryForTest(t, st, "node-a", testNow)
	pub.result = confirmedResultForAction("x", sessionID, "started")
	pub.resultsByNode = map[string]mqttproto.ResultPayload{
		"node-a:audio.session.start": nodeRefusedResult("start", sessionID,
			"media not ready: asset asset-1 is not present at /var/lib/showmesh/assets/asset-1.mp3"),
	}
	driveNightAdvanceBackgroundAudioUntilStable(t, h, pub, rec, 12)

	history, err := h.nightBackgroundAudioHistory(context.Background(), rec)
	if err != nil {
		t.Fatalf("history: %v", err)
	}
	latest, ok := nightBackgroundAudioLatestStepForNode(history, "node-a")
	if !ok || latest.Step.Kind != nightBGStepStart || latest.Row.Outcome != nightCueOutcomeRefused {
		t.Fatalf("setup: node-a latest step = %+v (%s), want a refused start", latest.Step, latest.Row.Outcome)
	}
	return rec, sessionID
}

// TestNightBedFileRetry_ReAppliesOnceTheFileLands is the issue's own
// acceptance: the first attempt is refused for a missing file, the node's
// inventory then reports the file present, and the coordinator dispatches
// a fresh apply, gain and start under a new revision.
func TestNightBedFileRetry_ReAppliesOnceTheFileLands(t *testing.T) {
	h, st, pub, _ := nightBackgroundAudioTestHandlers(t)
	rec, sessionID := singleNodeBedRefusedForAMissingFile(t, h, st, pub)

	beforeRevision := bedLatestRevisionForTest(t, h, rec, "node-a")
	applies := countDispatchedActionForSession(pub, "audio.session.apply", sessionID)

	// Asset sync delivers both files, and the node reports them.
	later := testNow.Add(2 * time.Minute)
	putNodeInventoryForTest(t, st, "node-a", later, bedItemInventoryForTest("node-a", "asset-1", "asset-2")...)
	pub.resultsByNode = nil
	driveNightAdvanceBackgroundAudioUntilStableAt(t, h, pub, rec, later, 12)

	if got := countDispatchedActionForSession(pub, "audio.session.apply", sessionID); got <= applies {
		t.Fatalf("bed applies = %d, want more than %d: the file landing must buy a fresh attempt", got, applies)
	}
	history, err := h.nightBackgroundAudioHistory(context.Background(), rec)
	if err != nil {
		t.Fatalf("history: %v", err)
	}
	var sawFilesApply, sawGain, sawStart bool
	for _, row := range nightBackgroundAudioStepsForNode(history, "node-a") {
		if row.Row.ActionRevision <= beforeRevision {
			continue
		}
		switch {
		case row.Step.Kind == nightBGStepApply && row.Step.Recovery:
			sawFilesApply = true
			if !strings.Contains(row.Row.OutcomeReason, "now has every file") {
				t.Errorf("recovery apply reason = %q, want it to say the speaker now has every file it needs", row.Row.OutcomeReason)
			}
		case row.Step.Kind == nightBGStepGain:
			sawGain = true
		case row.Step.Kind == nightBGStepStart:
			sawStart = true
		}
	}
	if !sawFilesApply || !sawGain || !sawStart {
		t.Fatalf("fresh attempt under a new revision: apply=%v gain=%v start=%v, want all three", sawFilesApply, sawGain, sawStart)
	}
}

// TestNightBedFileRetry_WaitsWhileAFileIsStillMissing is the other half:
// an inventory that changed but still does not hold every file buys
// nothing, because the refusal it would hit has not been answered.
func TestNightBedFileRetry_WaitsWhileAFileIsStillMissing(t *testing.T) {
	h, st, pub, _ := nightBackgroundAudioTestHandlers(t)
	rec, sessionID := singleNodeBedRefusedForAMissingFile(t, h, st, pub)

	applies := countDispatchedActionForSession(pub, "audio.session.apply", sessionID)
	later := testNow.Add(2 * time.Minute)
	// Only the first of the two files arrived.
	putNodeInventoryForTest(t, st, "node-a", later, bedItemInventoryForTest("node-a", "asset-1")...)
	for i := 0; i < 5; i++ {
		h.nightAdvanceBackgroundAudio(context.Background(), later.Add(time.Duration(i)*time.Second), rec)
	}
	if got := countDispatchedActionForSession(pub, "audio.session.apply", sessionID); got != applies {
		t.Fatalf("bed applies = %d, want unchanged at %d while a file is still missing", got, applies)
	}
}

// TestNightBedFileRetry_NeverReAppliesAnAttemptThatSucceeded is the
// issue's own explicit rule: a node whose attempt confirmed is not
// re-applied when its inventory changes.
func TestNightBedFileRetry_NeverReAppliesAnAttemptThatSucceeded(t *testing.T) {
	h, st, pub, _ := nightBackgroundAudioTestHandlers(t)
	putBackgroundAudioAsset(t, st, "halloween", "bg-1", "node-a", "asset-1")
	putBackgroundAudioAsset(t, st, "halloween", "bg-2", "node-a", "asset-2")
	ba := twoItemBackgroundAudioConfig("node-a", config.NightSessionBackgroundRepeatPlaylist, config.NightSessionBackgroundResumeResume, config.NightSessionItemTransitionSequential)
	rec := mustCreateRestingSessionWithBackgroundAudio(t, st, "sess-1", "node-a", ba, nightStateRestingIntershow)
	sessionID := nightBackgroundAudioSessionID(rec)
	pub.result = confirmedResultForAction("x", sessionID, "started")
	driveNightAdvanceBackgroundAudioUntilStable(t, h, pub, rec, 12)

	applies := countDispatchedActionForSession(pub, "audio.session.apply", sessionID)
	later := testNow.Add(2 * time.Minute)
	// The inventory changes under a node that is already playing the bed.
	putNodeInventoryForTest(t, st, "node-a", later, bedItemInventoryForTest("node-a", "asset-1", "asset-2")...)
	audio := h.deps.Audio.(*fakeNodeAudioLister)
	audio.setObservations("node-a", append(
		[]observation.Observation{bedNodeAudioReport("node-a", later, later)},
		bedSessionPlayingReport(sessionID, "track-1", 0, 5_000, later)...))

	for i := 0; i < 5; i++ {
		h.nightAdvanceBackgroundAudio(context.Background(), later.Add(time.Duration(i)*time.Second), rec)
	}
	if got := countDispatchedActionForSession(pub, "audio.session.apply", sessionID); got != applies {
		t.Fatalf("bed applies = %d, want unchanged at %d: an attempt that succeeded is never re-applied", got, applies)
	}
}

// TestNightBedFileRetry_LeavesANodeAlreadyPlayingTheBedAlone covers the
// same rule from the node's own side: whatever this controller's ledger
// stalled on, a speaker the bed is audibly running on is not re-applied.
func TestNightBedFileRetry_LeavesANodeAlreadyPlayingTheBedAlone(t *testing.T) {
	h, st, pub, _ := nightBackgroundAudioTestHandlers(t)
	rec, sessionID := singleNodeBedRefusedForAMissingFile(t, h, st, pub)

	applies := countDispatchedActionForSession(pub, "audio.session.apply", sessionID)
	later := testNow.Add(2 * time.Minute)
	putNodeInventoryForTest(t, st, "node-a", later, bedItemInventoryForTest("node-a", "asset-1", "asset-2")...)
	audio := h.deps.Audio.(*fakeNodeAudioLister)
	audio.setObservations("node-a", append(
		[]observation.Observation{bedNodeAudioReport("node-a", later, later)},
		bedSessionPlayingReport(sessionID, "track-1", 0, 5_000, later)...))

	for i := 0; i < 5; i++ {
		h.nightAdvanceBackgroundAudio(context.Background(), later.Add(time.Duration(i)*time.Second), rec)
	}
	if got := countDispatchedActionForSession(pub, "audio.session.apply", sessionID); got != applies {
		t.Fatalf("bed applies = %d, want unchanged at %d: a node already playing the bed is left alone", got, applies)
	}
}

// TestNightBedFileRetry_OnlyOnce bounds it: if the node still will not
// play the bed with every file present, the coordinator records that and
// stops, rather than re-applying on every inventory report for the night.
func TestNightBedFileRetry_OnlyOnce(t *testing.T) {
	h, st, pub, _ := nightBackgroundAudioTestHandlers(t)
	rec, sessionID := singleNodeBedRefusedForAMissingFile(t, h, st, pub)

	later := testNow.Add(2 * time.Minute)
	putNodeInventoryForTest(t, st, "node-a", later, bedItemInventoryForTest("node-a", "asset-1", "asset-2")...)
	// The node keeps refusing the start for some other reason.
	pub.resultsByNode = map[string]mqttproto.ResultPayload{
		"node-a:audio.session.start": nodeRefusedResult("start", sessionID, "the output is not available"),
	}
	for i := 0; i < 20; i++ {
		h.nightAdvanceBackgroundAudio(context.Background(), later.Add(time.Duration(i)*time.Second), rec)
	}

	history, err := h.nightBackgroundAudioHistory(context.Background(), rec)
	if err != nil {
		t.Fatalf("history: %v", err)
	}
	recoveries := 0
	for _, row := range nightBackgroundAudioStepsForNode(history, "node-a") {
		if row.Step.Kind == nightBGStepApply && row.Step.Recovery {
			recoveries++
		}
	}
	if recoveries != 1 {
		t.Fatalf("recovery applies over twenty ticks = %d, want exactly 1: a late file buys one fresh attempt", recoveries)
	}
}

// TestNightBedFileRetry_NeedsACompleteInventoryReport keeps absence of
// evidence from reading as the file having arrived: a node that has never
// reported, or whose report did not finish, is not re-applied.
func TestNightBedFileRetry_NeedsACompleteInventoryReport(t *testing.T) {
	h, st, pub, _ := nightBackgroundAudioTestHandlers(t)
	rec, sessionID := singleNodeBedRefusedForAMissingFile(t, h, st, pub)

	applies := countDispatchedActionForSession(pub, "audio.session.apply", sessionID)
	later := testNow.Add(2 * time.Minute)
	if err := st.ReplaceNodeAssetInventory(context.Background(), "node-a",
		bedItemInventoryForTest("node-a", "asset-1", "asset-2"),
		store.NodeAssetReportRecord{NodeID: "node-a", ReportedAt: later, Complete: false, Reason: "the node could not finish its scan"}); err != nil {
		t.Fatalf("replace node asset inventory: %v", err)
	}
	for i := 0; i < 5; i++ {
		h.nightAdvanceBackgroundAudio(context.Background(), later.Add(time.Duration(i)*time.Second), rec)
	}
	if got := countDispatchedActionForSession(pub, "audio.session.apply", sessionID); got != applies {
		t.Fatalf("bed applies = %d, want unchanged at %d: an incomplete inventory report is not evidence the file arrived", got, applies)
	}
}

// bedLatestRevisionForTest is nodeID's own latest bed step revision, so a
// test can tell a fresh attempt from the one that went nowhere.
func bedLatestRevisionForTest(t *testing.T, h *handlers, rec store.NightSessionRecord, nodeID string) int64 {
	t.Helper()
	history, err := h.nightBackgroundAudioHistory(context.Background(), rec)
	if err != nil {
		t.Fatalf("history: %v", err)
	}
	latest, ok := nightBackgroundAudioLatestStepForNode(history, nodeID)
	if !ok {
		t.Fatalf("node %q has no bed steps", nodeID)
	}
	return latest.Row.ActionRevision
}

// TestNightBedFileRetry_DoesNotCutInOnARejoinsOwnPointRetry is the loop
// review reproduced, pinned so it cannot come back.
//
// A rejoined node in a multi-node bed is started with a playback point;
// refusing that point is the designed case the point-less retry exists
// for. On a rig whose assets have synced, every node's inventory is
// complete, so without a guard this file's own hook answered that refused
// start first, applied the bed again, produced another point-carrying
// start, and cycled: a silent speaker re-applied every few ticks for the
// whole night, with the point-less retry never sent at all.
func TestNightBedFileRetry_DoesNotCutInOnARejoinsOwnPointRetry(t *testing.T) {
	h, st, pub, _ := nightBackgroundAudioTestHandlers(t)
	rec, sessionID := startMultiNodeBedForTest(t, h, st, pub)
	audio := h.deps.Audio.(*fakeNodeAudioLister)

	at := testNow.Add(30 * time.Second)
	audio.setObservations("node-a", append(
		[]observation.Observation{bedNodeAudioReport("node-a", at, at)},
		bedSessionPlayingReport(sessionID, "track-2", 1, 65_000, at)...))
	audio.setObservations("node-b", []observation.Observation{bedNodeAudioReport("node-b", at, at)})

	// node-b refuses every start that names a position, which is what a
	// playlist edited mid-night does, and nothing else.
	pub.resultsByNode = map[string]mqttproto.ResultPayload{
		"node-b:audio.session.start": nodeRefusedResult("start", sessionID,
			`start point names item "track-2" at index 1, but this session's playlist has "track-9" there`),
	}
	for i := 0; i < 30; i++ {
		h.nightAdvanceBackgroundAudio(context.Background(), at.Add(time.Duration(i)*time.Second), rec)
	}

	history, err := h.nightBackgroundAudioHistory(context.Background(), rec)
	if err != nil {
		t.Fatalf("history: %v", err)
	}
	var filesApplies, recoveries, pointStarts, plainStarts int
	for _, row := range nightBackgroundAudioStepsForNode(history, "node-b") {
		switch {
		case row.Step.Kind == nightBGStepApply && row.Step.Recovery:
			recoveries++
			if strings.HasSuffix(row.Row.CueName, "-filesapply") {
				filesApplies++
			}
		case row.Step.Kind == nightBGStepStart && row.Step.WithPoint:
			pointStarts++
		case row.Step.Kind == nightBGStepStart:
			plainStarts++
		}
	}
	if filesApplies != 0 {
		t.Errorf("node-b recorded %d late-file applies, want 0: a rejoin's own point retry is not a node waiting on a file", filesApplies)
	}
	if plainStarts < 1 {
		t.Errorf("node-b was never sent a start without a playback point, want the designed retry to reach it")
	}
	if recoveries > 1 {
		t.Errorf("node-b recorded %d recovery applies over thirty ticks, want at most 1", recoveries)
	}
	if pointStarts > 1 {
		t.Errorf("node-b was sent %d starts carrying a position over thirty ticks, want at most 1", pointStarts)
	}
}

// TestNightBedFileRetry_OneRecoveryPerConfirmedStart is the second bound:
// once a recovery has been given, another is not started on top of it
// until the node confirms a start, so a failed recovery followed by a
// show cycle cannot let this fire again each time round.
func TestNightBedFileRetry_OneRecoveryPerConfirmedStart(t *testing.T) {
	steps := []nightBackgroundAudioHistoryRow{
		{Step: nightBackgroundAudioStep{Kind: nightBGStepApply}, Row: store.NightCueOutboxRecord{State: nightCueStateResolved, Outcome: nightCueOutcomeConfirmed}},
		{Step: nightBackgroundAudioStep{Kind: nightBGStepStart}, Row: store.NightCueOutboxRecord{State: nightCueStateResolved, Outcome: nightCueOutcomeConfirmed}},
	}
	if nightBedRecoveryAppliedSinceLastConfirmedStart(steps) {
		t.Fatal("a node that has only ever started normally read as mid-recovery")
	}

	steps = append(steps, nightBackgroundAudioHistoryRow{
		Step: nightBackgroundAudioStep{Kind: nightBGStepApply, Recovery: true},
		Row:  store.NightCueOutboxRecord{State: nightCueStateResolved, Outcome: nightCueOutcomeConfirmed},
	})
	if !nightBedRecoveryAppliedSinceLastConfirmedStart(steps) {
		t.Fatal("a recovery apply with no confirmed start after it read as finished")
	}

	steps = append(steps, nightBackgroundAudioHistoryRow{
		Step: nightBackgroundAudioStep{Kind: nightBGStepStart},
		Row:  store.NightCueOutboxRecord{State: nightCueStateResolved, Outcome: nightCueOutcomeConfirmed},
	})
	if nightBedRecoveryAppliedSinceLastConfirmedStart(steps) {
		t.Fatal("a confirmed start after the recovery did not clear it, so a later loss could never be answered")
	}
}
