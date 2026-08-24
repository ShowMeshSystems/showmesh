package api

import (
	"context"
	"sync/atomic"
	"testing"
	"time"

	"github.com/showmeshsystems/showmesh/internal/coordinator/config"
	"github.com/showmeshsystems/showmesh/internal/coordinator/store"
	pkgaudio "github.com/showmeshsystems/showmesh/pkg/audio"
	"github.com/showmeshsystems/showmesh/pkg/cueactivation"
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
// this seam's own fix: H0.2's blackAndSilence policy stops
// [blackAndSilenceAudioSessionID] on every node that has declared an
// audio.node object, and dispatches nothing audio-related to a node that
// has not (nothing to silence there — ADR-018).
func TestDispatchBlackAndSilenceStopsAudioOnlyForNodesWithAudioNode(t *testing.T) {
	setup := newAudioDispatchTestSetup(t, fixedClock(testNow))
	putAudioNodeForTest(t, setup.st, "audio-01")
	// "render-01" deliberately gets no audio.node object.

	h := &handlers{deps: setup.deps().withDefaults(), clock: fixedClock(testNow), logger: testLogger()}
	issuer := cueActivationIssuer{PrincipalID: "system:cue-activation-loop:test"}

	h.dispatchBlackAndSilence(context.Background(), testNow, []string{"audio-01", "render-01"}, issuer, "inst-1-1")

	setup.pub.mu.Lock()
	defer setup.pub.mu.Unlock()
	count := 0
	for _, d := range setup.pub.dispatched {
		if d.Action != "audio.session.stop" {
			continue
		}
		count++
		sessionID, _ := d.Params["sessionId"].(string)
		if sessionID != blackAndSilenceAudioSessionID {
			t.Fatalf("audio.session.stop dispatched with sessionId = %q, want %q", sessionID, blackAndSilenceAudioSessionID)
		}
	}
	if count != 1 {
		t.Fatalf("audio.session.stop dispatch count = %d, want exactly 1 (only audio-01 has an audio.node object; render-01 must be skipped)", count)
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
	// second must be suppressed as a replay, not published again.
	h.dispatchBlackAndSilence(context.Background(), testNow, []string{"audio-01"}, issuer, "inst-1-1")
	h.dispatchBlackAndSilence(context.Background(), testNow, []string{"audio-01"}, issuer, "inst-1-1")
	if got := countStops(); got != 1 {
		t.Fatalf("audio.session.stop dispatch count after two ticks of ONE episode = %d, want 1 (a repeat tick must not republish)", got)
	}

	// A later, genuinely separate mismatch episode — a new
	// EntryOccurrenceSequence — on the SAME node must dispatch again.
	h.dispatchBlackAndSilence(context.Background(), testNow, []string{"audio-01"}, issuer, "inst-1-2")
	if got := countStops(); got != 2 {
		t.Fatalf("audio.session.stop dispatch count after a NEW episode = %d, want 2 (a later, separate mismatch must dispatch again)", got)
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
func dispatchedAudioStopRevision(t *testing.T, st *store.Store) uint64 {
	t.Helper()
	rec, err := st.GetAudioSession(context.Background(), blackAndSilenceAudioSessionID)
	if err != nil {
		t.Fatalf("get audio session %q: %v", blackAndSilenceAudioSessionID, err)
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
	activateOutcome := h.dispatchOneCueActivation(context.Background(), now, nodeID, act, issuer)
	if activateOutcome.Err != nil || !activateOutcome.Confirmed {
		t.Fatalf("dispatchOneCueActivation: outcome = %+v", activateOutcome)
	}

	// The blackAndSilence mismatch is dispatched using the COORDINATOR'S
	// OWN now, which is an hour BEHIND the node's clock — the exact
	// condition that left the old bare-now derivation refused as stale.
	h.dispatchBlackAndSilence(context.Background(), now, []string{nodeID}, issuer, "skew-episode-1")

	stopRevision := dispatchedAudioStopRevision(t, setup.st)

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
	act.EvidenceAt = nodeClockEvidenceAt
	act.CatalogRevision = resolvedCatalogRevisionForTest(t, setup.st, act.Show, nodeID)
	setup.pub.result = cueActivationNodeResultPayload(true, cueActivationNodeOutcomeAuthorized)

	deps := setup.deps()
	deps.AssetManifests = setup.st
	h := &handlers{deps: deps.withDefaults(), clock: fixedClock(now), logger: testLogger()}
	issuer := cueActivationIssuer{PrincipalID: "system:cue-activation-loop:test"}

	activateOutcome := h.dispatchOneCueActivation(context.Background(), now, nodeID, act, issuer)
	if activateOutcome.Err != nil || !activateOutcome.Confirmed {
		t.Fatalf("dispatchOneCueActivation: outcome = %+v", activateOutcome)
	}

	h.dispatchBlackAndSilence(context.Background(), now, []string{nodeID}, issuer, "ordinary-episode-1")

	stopRevision := dispatchedAudioStopRevision(t, setup.st)

	rs := simulateNodeAudioSessionRevision(t, act.ActivationID, nodeClockEvidenceAt)
	decision := rs.Apply(pkgaudio.InvocationID("stop-invocation"), pkgaudio.Revision(stopRevision))
	if !decision.Accepted {
		t.Fatalf("stop revision %d refused by the real RevisionState in the ordinary (coordinator clock ahead) case: %+v", stopRevision, decision)
	}
}
