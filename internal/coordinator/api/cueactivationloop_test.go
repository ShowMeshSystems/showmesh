package api

import (
	"context"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/showmeshsystems/showmesh/internal/coordinator/config"
	"github.com/showmeshsystems/showmesh/internal/coordinator/store"
	pkgaudio "github.com/showmeshsystems/showmesh/pkg/audio"
	"github.com/showmeshsystems/showmesh/pkg/cueactivation"
	"github.com/showmeshsystems/showmesh/pkg/cueauth"
	"github.com/showmeshsystems/showmesh/pkg/cuecatalog"
)

// countingFPPObservationStore counts every call to
// ListFPPPlaylistEntryObservations and always reports zero observations —
// enough for [handlers.cueActivationTick] to run its full body (list, then
// return, since there is nothing to reconcile) without needing a real
// store or a real FPPReconciliationStore.
type countingFPPObservationStore struct {
	calls atomic.Int64
}

func (c *countingFPPObservationStore) ListFPPPlaylistEntryObservations(context.Context) ([]store.FPPPlaylistEntryObservationRecord, error) {
	c.calls.Add(1)
	return nil, nil
}

func (c *countingFPPObservationStore) GetFPPPlaylistEntryObservation(context.Context, string) (store.FPPPlaylistEntryObservationRecord, error) {
	return store.FPPPlaylistEntryObservationRecord{}, store.ErrFPPPlaylistEntryObservationNotFound
}

func (c *countingFPPObservationStore) InTx(ctx context.Context, fn func(ctx context.Context, tx *store.Tx) error) error {
	return fn(ctx, nil)
}

// TestCueActivationLoopNudgeWakesBeforeInterval proves this seam's own
// fix: an interval long enough that the periodic ticker alone could not
// explain a prompt tick, followed by a single [CueActivationLoop.Nudge],
// must still produce a tick well within the interval — the whole point of
// waking the loop on a fresh FPP playlist-entry observation instead of
// leaving it to the periodic tick alone (up to 1 second on a real show).
func TestCueActivationLoopNudgeWakesBeforeInterval(t *testing.T) {
	obs := &countingFPPObservationStore{}
	deps := Dependencies{
		FPPReconciliation: noFPPReconciliationStore{}, // never reached: obs always reports zero rows.
		FPPObservations:   obs,
	}
	loop := NewCueActivationLoop(deps, Options{CueActivationLoopInterval: time.Hour})

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go loop.Run(ctx)

	// The periodic ticker is set to 1 hour, so Run has NOT ticked yet —
	// give it a moment to reach its select, then nudge it. Any tick
	// observed within the budget below can only be explained by Nudge,
	// never by the 1h ticker.
	time.Sleep(20 * time.Millisecond)
	before := obs.calls.Load()
	if before != 0 {
		t.Fatalf("calls = %d before any Nudge, want 0 (the 1h ticker must not have fired yet)", before)
	}
	loop.Nudge()

	deadline := time.After(2 * time.Second)
	for obs.calls.Load() <= before {
		select {
		case <-deadline:
			t.Fatalf("Nudge did not produce a new tick within 2s (interval is 1h, so only Nudge could explain a tick this soon); calls = %d, want > %d", obs.calls.Load(), before)
		case <-time.After(5 * time.Millisecond):
		}
	}
}

// TestCueActivationLoopNudgeBeforeRunIsSafe proves Nudge is safe to call
// before Run starts (matching [assetsync.Service.Nudge]'s identical
// contract): the buffered channel absorbs it, and Run's first tick still
// happens once started.
func TestCueActivationLoopNudgeBeforeRunIsSafe(t *testing.T) {
	obs := &countingFPPObservationStore{}
	deps := Dependencies{
		FPPReconciliation: noFPPReconciliationStore{},
		FPPObservations:   obs,
	}
	loop := NewCueActivationLoop(deps, Options{CueActivationLoopInterval: time.Hour})

	// Called twice before Run ever starts: the buffered channel coalesces
	// this to at most one pending nudge, never blocking.
	loop.Nudge()
	loop.Nudge()

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go loop.Run(ctx)

	deadline := time.After(2 * time.Second)
	for obs.calls.Load() == 0 {
		select {
		case <-deadline:
			t.Fatalf("no tick observed after Run started, despite a pre-Run Nudge")
		case <-time.After(5 * time.Millisecond):
		}
	}
}

// TestCueActivationLoopNudgeFloorBoundsBackToBackTicks is defect 6's own
// regression test: fppobservations.go nudges on every accepted, non-replay
// observation, and the loop's own in-flight skip only bounds CONCURRENT
// ticks, never the RATE a fast-posting plugin can drive back-to-back full
// reconcile passes at. A minimum interval between nudge-driven ticks
// (CueActivationNudgeMinInterval) must bound that rate while still never
// dropping the evidence a nudge represents — a nudge arriving inside the
// floor is deferred to the floor, not lost.
func TestCueActivationLoopNudgeFloorBoundsBackToBackTicks(t *testing.T) {
	obs := &countingFPPObservationStore{}
	deps := Dependencies{
		FPPReconciliation: noFPPReconciliationStore{},
		FPPObservations:   obs,
	}
	const floor = 150 * time.Millisecond
	loop := NewCueActivationLoop(deps, Options{
		CueActivationLoopInterval:     time.Hour, // only nudges can explain any tick in this test
		CueActivationNudgeMinInterval: floor,
	})

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go loop.Run(ctx)
	time.Sleep(20 * time.Millisecond) // let Run reach its select before nudging

	// First nudge: nothing has run yet, so this must tick promptly.
	loop.Nudge()
	deadline := time.After(2 * time.Second)
	for obs.calls.Load() < 1 {
		select {
		case <-deadline:
			t.Fatalf("first nudge did not produce a tick within 2s; calls = %d", obs.calls.Load())
		case <-time.After(5 * time.Millisecond):
		}
	}

	// A burst of nudges immediately afterward, well inside the floor: none
	// of them may produce a SECOND tick before the floor elapses.
	for i := 0; i < 5; i++ {
		loop.Nudge()
	}
	time.Sleep(floor / 2)
	if got := obs.calls.Load(); got != 1 {
		t.Fatalf("calls = %d before the floor elapsed, want 1 (a burst of nudges inside the floor must not produce extra ticks)", got)
	}

	// Once the floor elapses, the deferred nudge must still produce a
	// second tick — evidence deferred, never dropped.
	deadline = time.After(2 * time.Second)
	for obs.calls.Load() < 2 {
		select {
		case <-deadline:
			t.Fatalf("no second tick within 2s after the floor elapsed; calls = %d, want >= 2 (a nudge inside the floor must be deferred, not dropped)", obs.calls.Load())
		case <-time.After(5 * time.Millisecond):
		}
	}
}

// --- blackAndSilence audio half ----------------------------------------

func putAudioNodeForTest(t *testing.T, st *store.Store, nodeID string) {
	t.Helper()
	raw, err := config.EncodeAudioNodePayload(config.AudioNodePayload{
		ProgramRoute: "usb-interface", LTCRoute: "usb-interface",
		ProgramChannels: []int{1, 2}, LTCChannel: 3,
		ClockDomain:           "single-interface",
		ClockDomainProvenance: "single interface, both routes on it",
	})
	if err != nil {
		t.Fatalf("encode audio.node payload: %v", err)
	}
	putConfigForTest(t, st, config.AudioNodeConfigKind, nodeID, raw)
}

// TestDispatchBlackAndSilenceStopsAudioOnlyForNodesWithAudioNode proves
// this seam's own fix: H0.2's blackAndSilence policy stops EVERY session
// in [blackAndSilenceAudioSessionIDs] (TRACK-H-cues-and-playlists.md
// section H5 build item 4 extended this from the one show session to all
// three: the show session, the showmesh-audio background session, and the
// announcement session — H5 created the latter two, and blackAndSilence
// used to leave both completely outside every silence path) on every node
// that has declared an audio.node object, and dispatches nothing
// audio-related to a node that has not (nothing to silence there —
// ADR-018).
func TestDispatchBlackAndSilenceStopsAudioOnlyForNodesWithAudioNode(t *testing.T) {
	setup := newAudioDispatchTestSetup(t, fixedClock(testNow))
	putAudioNodeForTest(t, setup.st, "audio-01")
	// "render-01" deliberately gets no audio.node object.

	h := &handlers{deps: setup.deps().withDefaults(), clock: fixedClock(testNow), logger: testLogger()}
	issuer := cueActivationIssuer{PrincipalID: "system:cue-activation-loop:test"}

	h.dispatchBlackAndSilence(context.Background(), testNow, []string{"audio-01", "render-01"}, issuer, "inst-1-1")

	setup.pub.mu.Lock()
	defer setup.pub.mu.Unlock()
	gotSessions := map[string]bool{}
	for _, d := range setup.pub.dispatched {
		if d.Action != "audio.session.stop" {
			continue
		}
		sessionID, _ := d.Params["sessionId"].(string)
		gotSessions[sessionID] = true
	}
	if len(gotSessions) != len(blackAndSilenceAudioSessionIDs) {
		t.Fatalf("audio.session.stop dispatched to sessions %v, want exactly %v (only audio-01 has an audio.node object; render-01 must be skipped)", gotSessions, blackAndSilenceAudioSessionIDs)
	}
	for _, want := range blackAndSilenceAudioSessionIDs {
		if !gotSessions[want] {
			t.Fatalf("audio.session.stop was never dispatched against session %q; got %v", want, gotSessions)
		}
	}
}

// TestDispatchBlackAndSilenceRedispatchesOnANewEpisode is defect 3's own
// regression test: idempotency_key is globally unique on the commands
// table, and both replay resolvers answer a reused key with the FIRST
// command's already-resolved outcome forever, never dispatching again.
// Before the fix, both the render clear key ("cueact-clear-"+nodeID+"-"+
// surfaceID) and the audio silence key ("cueact-silence-"+nodeID) carried
// no episode dimension at all, so a SECOND, later, genuinely separate
// mismatch on the same node published nothing — the render half not even
// logging that it had suppressed anything. This proves the fix from both
// directions: the SAME episode called twice dispatches once (still no
// per-tick republish, the original point of the idempotency key), and a
// DIFFERENT episode dispatches again.
func TestDispatchBlackAndSilenceRedispatchesOnANewEpisode(t *testing.T) {
	setup := newAudioDispatchTestSetup(t, fixedClock(testNow))
	putAudioNodeForTest(t, setup.st, "audio-01")

	h := &handlers{deps: setup.deps().withDefaults(), clock: fixedClock(testNow), logger: testLogger()}
	issuer := cueActivationIssuer{PrincipalID: "system:cue-activation-loop:test"}

	countStops := func() int {
		setup.pub.mu.Lock()
		defer setup.pub.mu.Unlock()
		n := 0
		for _, d := range setup.pub.dispatched {
			if d.Action == "audio.session.stop" {
				n++
			}
		}
		return n
	}

	// Two ticks of the SAME episode (a continuing mismatch the coordinator
	// re-reads the identical stored observation for every tick): the
	// second must be suppressed as a replay, not published again. Each
	// tick dispatches one stop per session in blackAndSilenceAudioSessionIDs
	// (build item 4), so a genuinely fresh dispatch of one episode counts
	// len(blackAndSilenceAudioSessionIDs), not 1.
	perEpisode := len(blackAndSilenceAudioSessionIDs)
	h.dispatchBlackAndSilence(context.Background(), testNow, []string{"audio-01"}, issuer, "inst-1-1")
	h.dispatchBlackAndSilence(context.Background(), testNow, []string{"audio-01"}, issuer, "inst-1-1")
	if got := countStops(); got != perEpisode {
		t.Fatalf("audio.session.stop dispatch count after two ticks of ONE episode = %d, want %d (a repeat tick must not republish)", got, perEpisode)
	}

	// A later, genuinely separate mismatch episode — a new
	// EntryOccurrenceSequence — on the SAME node must dispatch again.
	h.dispatchBlackAndSilence(context.Background(), testNow, []string{"audio-01"}, issuer, "inst-1-2")
	if got := countStops(); got != 2*perEpisode {
		t.Fatalf("audio.session.stop dispatch count after a NEW episode = %d, want %d (a later, separate mismatch must dispatch again)", got, 2*perEpisode)
	}
}

// simulateNodeAudioSessionRevision replays exactly the four steps
// internal/agent/cueactivationaudio.go's activateAudio drives against the
// real session on the node (Apply, Prepare, Start, Seek, in that order),
// deriving each one's revision through the identical
// [cueactivation.AudioSessionRevision] rule the node itself uses, keyed off
// evidenceAt — never the coordinator's clock. Returns the RevisionState
// left in exactly the state a real node's session would be in after a
// cue.activate with this EvidenceAt, so a test can then Apply a
// coordinator-dispatched stop revision against it and observe whether the
// REAL anti-rewind rule (pkg/audio's RevisionState.Apply) would accept or
// refuse it — proof at the level that actually matters, not just "some
// larger uint64 was computed".
func simulateNodeAudioSessionRevision(t *testing.T, activationID string, evidenceAt time.Time) *pkgaudio.RevisionState {
	t.Helper()
	rs := pkgaudio.NewRevisionState(pkgaudio.SessionID(cueactivation.AudioSessionID))
	for i, step := range []int{
		cueactivation.AudioSessionStepApply, cueactivation.AudioSessionStepPrepare,
		cueactivation.AudioSessionStepStart, cueactivation.AudioSessionStepSeek,
	} {
		invocation := pkgaudio.InvocationID(activationID + ":" + string(rune('0'+i)))
		revision := pkgaudio.Revision(cueactivation.AudioSessionRevision(evidenceAt, step))
		decision := rs.Apply(invocation, revision)
		if !decision.Accepted {
			t.Fatalf("simulated node step %d not accepted: %+v", step, decision)
		}
	}
	return rs
}

// dispatchedAudioStopRevision reads back the STORED store.AudioSessionRecord
// for [blackAndSilenceAudioSessionID] and returns its Revision — not the
// value round-tripped through the fake publisher's JSON wire encoding,
// which decodes a JSON number into float64 and silently loses precision
// for a revision this large (nanosecond-since-epoch magnitudes exceed
// float64's exact-integer range). store.AudioSessionRecord.Revision is a
// plain uint64 column, set directly from the Go value this seam computed
// (persistAudioSessionDesiredState's in.Revision, audiodispatch.go), so
// reading it back here is the precise value actually dispatched.
func dispatchedAudioStopRevision(t *testing.T, st *store.Store, nodeID string) uint64 {
	t.Helper()
	rec, err := st.GetAudioSession(context.Background(), nodeID, blackAndSilenceAudioSessionID)
	if err != nil {
		t.Fatalf("get audio session %q for node %q: %v", blackAndSilenceAudioSessionID, nodeID, err)
	}
	return rec.Revision
}

// TestDispatchBlackAndSilenceAudioStopSurvivesNodeClockAheadOfCoordinator
// is the residual defect's own regression test: AudioSessionRevision's
// correctness depends on both callers reading the SAME clock, and they do
// not — the node's activateAudio steps key off act.EvidenceAt (a reading
// taken on the FPP player), while the coordinator's blackAndSilence stop
// used to key off its own now. An FPP player is a Raspberry Pi with no
// real-time clock and no guaranteed internet; it can boot with a clock far
// ahead of the coordinator's. This proves that when it does, the stop this
// coordinator dispatches is still ACCEPTED by the real
// pkg/audio.RevisionState the node would apply it against — not refused as
// stale, which is the exact "blackAndSilence silently fails to silence"
// defect the unification fix already closed for the multiplier case, now
// reopened by clock skew.
func TestDispatchBlackAndSilenceAudioStopSurvivesNodeClockAheadOfCoordinator(t *testing.T) {
	now := testNow
	nodeClockEvidenceAt := testNow.Add(1 * time.Hour) // the FPP player's clock is an hour AHEAD.

	setup := newAudioDispatchTestSetup(t, fixedClock(now))
	nodeID, act := cueActivationDispatchTestFixture(t, setup, now)
	putAuthorizedAudioAssetForTest(t, setup.st, act.Show, act.CueID, nodeID, now)
	act.EvidenceAt = nodeClockEvidenceAt
	act.CatalogRevision = resolvedCatalogRevisionForTest(t, setup.st, act.Show, nodeID)
	setup.pub.result = cueActivationNodeResultPayload(true, cueActivationNodeOutcomeAuthorized)

	deps := setup.deps()
	deps.AssetManifests = setup.st
	h := &handlers{deps: deps.withDefaults(), clock: fixedClock(now), logger: testLogger()}
	issuer := cueActivationIssuer{PrincipalID: "system:cue-activation-loop:test"}

	// Dispatch the activation itself first, so the coordinator has a
	// cue.activate command row on file for this node — exactly what
	// dispatchBlackAndSilenceAudioStop now reads back to compensate for
	// the node's ahead-of-coordinator clock.
	activateOutcome := h.dispatchOneCueActivation(context.Background(), now, nodeID, act, issuer, nil)
	if activateOutcome.Err != nil || !activateOutcome.Confirmed {
		t.Fatalf("dispatchOneCueActivation: outcome = %+v", activateOutcome)
	}

	// The blackAndSilence mismatch is dispatched using the COORDINATOR'S
	// OWN now, which is an hour BEHIND the node's clock — the exact
	// condition that left the old bare-now derivation refused as stale.
	h.dispatchBlackAndSilence(context.Background(), now, []string{nodeID}, issuer, "skew-episode-1")

	stopRevision := dispatchedAudioStopRevision(t, setup.st, nodeID)

	rs := simulateNodeAudioSessionRevision(t, act.ActivationID, nodeClockEvidenceAt)
	decision := rs.Apply(pkgaudio.InvocationID("stop-invocation"), pkgaudio.Revision(stopRevision))
	if !decision.Accepted {
		t.Fatalf("stop revision %d refused by the real RevisionState (%+v); blackAndSilence would silently fail to silence a node whose clock runs ahead of the coordinator's", stopRevision, decision)
	}
}

// TestDispatchBlackAndSilenceAudioStopOrdinaryCoordinatorClockAhead is the
// companion ordinary-case proof: the coordinator's own clock is ahead of
// (or equal to) the last dispatched activation's EvidenceAt, matching every
// FPP player with a sane clock. The fix must not regress this case — the
// stop must still be accepted by the node's real RevisionState.
func TestDispatchBlackAndSilenceAudioStopOrdinaryCoordinatorClockAhead(t *testing.T) {
	now := testNow
	nodeClockEvidenceAt := testNow.Add(-1 * time.Hour) // the node's own clock reading is BEHIND now.

	setup := newAudioDispatchTestSetup(t, fixedClock(now))
	nodeID, act := cueActivationDispatchTestFixture(t, setup, now)
	putAuthorizedAudioAssetForTest(t, setup.st, act.Show, act.CueID, nodeID, now)
	act.EvidenceAt = nodeClockEvidenceAt
	act.CatalogRevision = resolvedCatalogRevisionForTest(t, setup.st, act.Show, nodeID)
	setup.pub.result = cueActivationNodeResultPayload(true, cueActivationNodeOutcomeAuthorized)

	deps := setup.deps()
	deps.AssetManifests = setup.st
	h := &handlers{deps: deps.withDefaults(), clock: fixedClock(now), logger: testLogger()}
	issuer := cueActivationIssuer{PrincipalID: "system:cue-activation-loop:test"}

	activateOutcome := h.dispatchOneCueActivation(context.Background(), now, nodeID, act, issuer, nil)
	if activateOutcome.Err != nil || !activateOutcome.Confirmed {
		t.Fatalf("dispatchOneCueActivation: outcome = %+v", activateOutcome)
	}

	h.dispatchBlackAndSilence(context.Background(), now, []string{nodeID}, issuer, "ordinary-episode-1")

	stopRevision := dispatchedAudioStopRevision(t, setup.st, nodeID)

	rs := simulateNodeAudioSessionRevision(t, act.ActivationID, nodeClockEvidenceAt)
	decision := rs.Apply(pkgaudio.InvocationID("stop-invocation"), pkgaudio.Revision(stopRevision))
	if !decision.Accepted {
		t.Fatalf("stop revision %d refused by the real RevisionState in the ordinary (coordinator clock ahead) case: %+v", stopRevision, decision)
	}
}

// --- Asset-missing fail-to-black is show-mode gated ---

// TestAssetMissingFailToBlackTargetsCollectsBothRefusalKinds proves
// [assetMissingFailToBlackTargets] names every node refused asset-missing
// from EITHER side — this coordinator's own pre-dispatch Authorize
// refusal (AuthorizeOutcome) AND the node's own post-dispatch refusal
// (NodeOutcome) — carrying that node's own RefusedCueOutputs so the
// scoped dispatch downstream can black exactly what the Cue declared;
// never a node dispatched successfully, and never a node refused for an
// unrelated reason (cross-show, stale-cue) that fail-to-black has no
// bearing on.
func TestAssetMissingFailToBlackTargetsCollectsBothRefusalKinds(t *testing.T) {
	audioOutputs := cuecatalog.Outputs{Audio: &cuecatalog.AudioOutput{Asset: "asset-x"}}
	outcomes := []cueActivationDispatchOutcome{
		{NodeID: "node-confirmed", Dispatched: true, Confirmed: true},
		{NodeID: "node-coordinator-refused", AuthorizeOutcome: cueauth.OutcomeAssetMissing, AuthorizeReason: "cue \"x\": asset missing", RefusedCueOutputs: audioOutputs},
		{NodeID: "node-cross-show", AuthorizeOutcome: cueauth.OutcomeCrossShow},
		{NodeID: "node-stale-cue-refused", Dispatched: true, Confirmed: false, NodeOutcome: string(cueauth.OutcomeStaleCue)},
		{NodeID: "node-node-refused", Dispatched: true, Confirmed: false, NodeOutcome: string(cueauth.OutcomeAssetMissing), RefusedCueOutputs: audioOutputs},
	}
	got := assetMissingFailToBlackTargets(outcomes)
	want := map[string]bool{"node-coordinator-refused": true, "node-node-refused": true}
	if len(got) != len(want) {
		t.Fatalf("assetMissingFailToBlackTargets() = %+v, want exactly nodes %v", got, want)
	}
	for _, target := range got {
		if !want[target.NodeID] {
			t.Fatalf("assetMissingFailToBlackTargets() included unexpected node %q; got %+v", target.NodeID, got)
		}
		if target.Outputs.Audio == nil {
			t.Fatalf("target %q Outputs.Audio = nil, want the refused Cue's own RefusedCueOutputs carried through", target.NodeID)
		}
	}
}

// TestAssetMissingFailToBlackTargetsEmptyWhenNothingRefusedAssetMissing
// proves the zero-value/empty case: no asset-missing outcome anywhere
// yields no targets to black, never a nil-vs-empty-slice surprise a
// caller's own len() check would already handle either way, but pinned
// explicitly.
func TestAssetMissingFailToBlackTargetsEmptyWhenNothingRefusedAssetMissing(t *testing.T) {
	outcomes := []cueActivationDispatchOutcome{{NodeID: "node-confirmed", Dispatched: true, Confirmed: true}}
	if got := assetMissingFailToBlackTargets(outcomes); len(got) != 0 {
		t.Fatalf("assetMissingFailToBlackTargets() = %v, want none", got)
	}
}

// TestAssetMissingFailToBlackOnlyFiresInShowMode pins the owner's own
// ruling: a genuinely missing asset fails that Cue to black ONLY in show
// mode; in setup/program mode the refusal must stay loud (the existing
// log line and audit entry), never disappear to black, because an
// operator programming the show is expected to be watching for exactly
// this kind of error.
func TestAssetMissingFailToBlackOnlyFiresInShowMode(t *testing.T) {
	targets := []cueScopedFailToBlackTarget{{NodeID: "node-1"}}
	if !assetMissingFailToBlack(config.ShowModeShow, targets) {
		t.Error("assetMissingFailToBlack(show, [node-1]) = false, want true")
	}
	if assetMissingFailToBlack(config.ShowModeProgram, targets) {
		t.Error("assetMissingFailToBlack(program, [node-1]) = true, want false: setup/program mode keeps the refusal loud, never blacked (the owner's own ruling)")
	}
	if assetMissingFailToBlack(config.ShowModeShow, nil) {
		t.Error("assetMissingFailToBlack(show, nil) = true, want false: nothing to black")
	}
}

// TestDispatchOneCueActivationAssetMissingNamesTheSequenceAndAsset proves
// the per-cue asset gate's dispatch-layer wiring: a Cue whose own asset is
// genuinely missing from the node's reported inventory is refused
// asset-missing, and AuthorizeReason — what writeCueActivationRefusalAudit
// records as the audit's own OutcomeReason — names the sequence, the
// node, and the Cue, never just the bare outcome string.
func TestDispatchOneCueActivationAssetMissingNamesTheSequenceAndAsset(t *testing.T) {
	now := testNow
	setup := newAudioDispatchTestSetup(t, fixedClock(now))
	nodeID, act := cueActivationDispatchTestFixture(t, setup, now)

	// cueActivationDispatchTestFixture's own Cue names an asset ("asset-
	// cue-1") that nothing ever uploads, so [assetsync.ExpectedAssetsForNode]
	// resolves no content hash for it and the fixture is normally
	// vacuously "ready" without uploading anything (see
	// putAudioOnlyCueForTest's own doc comment). Creating a REAL asset
	// record for it here, without adding it to node-1's own reported
	// inventory, turns this into a genuinely missing asset: an asset
	// record exists, targeted at this exact node, but the node does not
	// hold it. This must happen BEFORE resolving CatalogRevision below —
	// the asset's own hash is part of what the catalog revision covers
	// (cuecatalog.RevisionInput's own doc comment).
	if _, _, err := setup.st.CreateAsset(context.Background(), store.AssetRecord{
		ID: "sha256:missing-cue-1-node", ShowID: act.Show, SequenceID: "asset-" + act.CueID,
		TargetKind: store.AssetTargetKindNode, TargetID: nodeID, MediaType: "audio",
		ContentHash: "sha256:missing-cue-1-node", RuntimeFilename: "Missing.wav",
		SizeBytes: 1024, Backend: "volume", StorageKey: "sha256:missing-cue-1-node",
	}); err != nil {
		t.Fatalf("create asset: %v", err)
	}
	act.CatalogRevision = resolvedCatalogRevisionForTest(t, setup.st, act.Show, nodeID)

	deps := setup.deps()
	deps.AssetManifests = setup.st
	h := &handlers{deps: deps.withDefaults(), clock: fixedClock(now), logger: testLogger()}
	issuer := cueActivationIssuer{PrincipalID: "system:cue-activation-loop:test"}

	outcome := h.dispatchOneCueActivation(context.Background(), now, nodeID, act, issuer, nil)
	if outcome.Err != nil {
		t.Fatalf("dispatchOneCueActivation: %v", outcome.Err)
	}
	if outcome.AuthorizeOutcome != cueauth.OutcomeAssetMissing {
		t.Fatalf("AuthorizeOutcome = %q, want %q", outcome.AuthorizeOutcome, cueauth.OutcomeAssetMissing)
	}
	if outcome.Dispatched {
		t.Fatal("Dispatched = true, want false: this coordinator's own Authorize refused before publish")
	}
	if !strings.Contains(outcome.AuthorizeReason, "asset-cue-1") || !strings.Contains(outcome.AuthorizeReason, nodeID) || !strings.Contains(outcome.AuthorizeReason, "cue-1") {
		t.Fatalf("AuthorizeReason = %q, want it to name the sequence (asset-cue-1), the node (%s) and the cue (cue-1)", outcome.AuthorizeReason, nodeID)
	}

	// writeCueActivationRefusalAudit's own fallback ("outcomeReason := reason;
	// if outcomeReason == \"\" { outcomeReason = string(outcome) }") must
	// actually reach the persisted audit row, not just the in-process
	// outcome struct — this is what an operator reading the audit log sees.
	entries, err := setup.st.ListAuditEntries(context.Background(), 0, 10)
	if err != nil {
		t.Fatalf("list audit entries: %v", err)
	}
	var found bool
	for _, e := range entries {
		if e.Action == "cue.activate" && e.Outcome == "refused" {
			found = true
			if e.OutcomeReason != outcome.AuthorizeReason {
				t.Fatalf("audit OutcomeReason = %q, want it to match the resolved AuthorizeReason %q, not the bare outcome string", e.OutcomeReason, outcome.AuthorizeReason)
			}
		}
	}
	if !found {
		t.Fatal("no refused cue.activate audit entry was written")
	}
}

// --- nextPlaylistEntryCueID ---

func putThreeEntryPlaylistForNextEntryTest(t *testing.T, st *store.Store) {
	t.Helper()
	putPlaylistForTest(t, st, "playlist-1", config.ShowPlaylistPayload{
		Show: "show-1", Name: "Main", Runner: config.ShowPlaylistRunnerFPP,
		MismatchPolicy: config.ShowPlaylistMismatchPolicyHold,
		FPP:            &config.ShowPlaylistFPPBinding{InstanceUUID: "inst-1", PlaylistName: "Main", PlaylistHash: hash64ForTest("a1")},
		Entries: []config.ShowPlaylistEntry{
			{ID: "entry-1", Cue: "cue-1"},
			{ID: "entry-2", Cue: "cue-2"},
			{ID: "entry-3", Cue: "cue-3"},
		},
	})
}

// TestNextPlaylistEntryCueIDFindsTheNextEntry proves the coordinator's own
// ordered lookup a node's flat, unordered catalog cannot derive itself:
// given the entry a Cue is currently activating from, it returns the
// CueID of the entry immediately after it, in Playlist order.
func TestNextPlaylistEntryCueIDFindsTheNextEntry(t *testing.T) {
	setup := newAudioDispatchTestSetup(t, fixedClock(testNow))
	putThreeEntryPlaylistForNextEntryTest(t, setup.st)

	cueID, ok, err := nextPlaylistEntryCueID(context.Background(), setup.st, "playlist-1", 1, "entry-2")
	if err != nil {
		t.Fatalf("nextPlaylistEntryCueID: %v", err)
	}
	if !ok || cueID != "cue-3" {
		t.Fatalf("nextPlaylistEntryCueID = (%q, %v), want (\"cue-3\", true)", cueID, ok)
	}
}

// TestNextPlaylistEntryCueIDNoNextOnLastEntry proves the last entry in a
// Playlist reports ok=false, not an error: there is genuinely nothing to
// prepare ahead when the show is about to end.
func TestNextPlaylistEntryCueIDNoNextOnLastEntry(t *testing.T) {
	setup := newAudioDispatchTestSetup(t, fixedClock(testNow))
	putThreeEntryPlaylistForNextEntryTest(t, setup.st)

	cueID, ok, err := nextPlaylistEntryCueID(context.Background(), setup.st, "playlist-1", 1, "entry-3")
	if err != nil {
		t.Fatalf("nextPlaylistEntryCueID: %v", err)
	}
	if ok {
		t.Fatalf("nextPlaylistEntryCueID = (%q, true), want ok=false (entry-3 is this Playlist's own last entry)", cueID)
	}
}

// TestNextPlaylistEntryCueIDSkipsWhenEntryIDEmpty proves an empty entryID
// — a directly-activated announcement, or a safeCue mismatch fallback,
// neither of which advances through an ordered Playlist (cueactivate/
// decide.go's own resolveActivationsForCue) — never looks anything up and
// never errors.
func TestNextPlaylistEntryCueIDSkipsWhenEntryIDEmpty(t *testing.T) {
	setup := newAudioDispatchTestSetup(t, fixedClock(testNow))
	putThreeEntryPlaylistForNextEntryTest(t, setup.st)

	cueID, ok, err := nextPlaylistEntryCueID(context.Background(), setup.st, "playlist-1", 1, "")
	if err != nil {
		t.Fatalf("nextPlaylistEntryCueID: %v", err)
	}
	if ok {
		t.Fatalf("nextPlaylistEntryCueID = (%q, true), want ok=false for an empty entryID", cueID)
	}
}

// TestNextPlaylistEntryCueIDUnknownEntryID proves an entryID this
// Playlist revision does not contain reports ok=false, not an error —
// mirrors TestNextPlaylistEntryCueIDNoNextOnLastEntry one case over.
func TestNextPlaylistEntryCueIDUnknownEntryID(t *testing.T) {
	setup := newAudioDispatchTestSetup(t, fixedClock(testNow))
	putThreeEntryPlaylistForNextEntryTest(t, setup.st)

	cueID, ok, err := nextPlaylistEntryCueID(context.Background(), setup.st, "playlist-1", 1, "entry-does-not-exist")
	if err != nil {
		t.Fatalf("nextPlaylistEntryCueID: %v", err)
	}
	if ok {
		t.Fatalf("nextPlaylistEntryCueID = (%q, true), want ok=false for an entryID this Playlist revision does not contain", cueID)
	}
}

// dispatchPrepareAheadAudio's own revision derivation (act.EvidenceAt plus
// a step past every AudioSessionStep* constant) is proven in
// cueactivationdispatch_test.go's
// TestPrepareStagingSessionRevisionClearsWhatTheNodeAlreadyHolds and
// TestDispatchPrepareAheadAudioRepeatTickReplaysIdempotently, alongside
// that file's other dispatchPrepareAheadAudio coverage.
