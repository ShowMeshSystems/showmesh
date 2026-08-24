package api

import (
	"context"
	"sync/atomic"
	"testing"
	"time"

	"github.com/showmeshsystems/showmesh/internal/coordinator/config"
	"github.com/showmeshsystems/showmesh/internal/coordinator/store"
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

	h.dispatchBlackAndSilence(context.Background(), testNow, []string{"audio-01", "render-01"}, issuer)

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
