package api

import (
	"context"
	"testing"
	"time"

	"github.com/showmeshsystems/showmesh/internal/coordinator/config"
	"github.com/showmeshsystems/showmesh/pkg/cueactivation"
	"github.com/showmeshsystems/showmesh/pkg/mqttproto"
)

// TestNightStopBackgroundAudioIfRunning_NotDelayedByInFlightPrepareAhead is
// a diagnostic reproduction: a fake node that never answers a
// cue-activation-loop prepare-ahead staging prepare on the SAME node must
// never delay the night loop's own bed pause for that node.
func TestNightStopBackgroundAudioIfRunning_NotDelayedByInFlightPrepareAhead(t *testing.T) {
	h, st, pub, _ := nightBackgroundAudioTestHandlers(t)
	putBackgroundAudioAsset(t, st, "halloween", "bg-1", "node-a", "asset-1")
	putBackgroundAudioAsset(t, st, "halloween", "bg-2", "node-a", "asset-2")
	ba := twoItemBackgroundAudioConfig("node-a", config.NightSessionBackgroundRepeatPlaylist, config.NightSessionBackgroundResumeResume, config.NightSessionItemTransitionSequential)
	rec := mustCreateRestingSessionWithBackgroundAudio(t, st, "sess-1", "node-a", ba, nightStateRestingIntershow)
	playThroughApplyGainStart(t, h, pub, rec)

	// Simulate the cue activation loop's own in-flight prepare-ahead
	// staging prepare on the SAME node: the fake node never answers it.
	block := make(chan struct{})
	defer close(block)
	pub.blockUntilByNode = map[string]<-chan struct{}{
		"node-a:audio.session.prepare": block,
	}
	go func() {
		_, _, _ = h.executeAudioSessionDispatch(context.Background(), testNow, AudioDispatchInput{
			Action: "audio.session.prepare", NodeID: "node-a", SessionID: cueactivation.PrepareStagingSessionID,
			Params:   map[string]any{"sessionId": cueactivation.PrepareStagingSessionID, "invocationId": "prepare-ahead-prepare", "revision": uint64(1)},
			Revision: 1, IdempotencyKey: "prepare-ahead-prepare",
		})
	}()

	// Wait for the prepare-ahead dispatch to genuinely be in flight (its
	// own publish recorded) before racing the bed pause against it.
	deadline := time.After(2 * time.Second)
	for {
		found := false
		for _, d := range pub.dispatchedSnapshot() {
			if d.NodeID == "node-a" && d.Action == "audio.session.prepare" {
				found = true
			}
		}
		if found {
			break
		}
		select {
		case <-deadline:
			t.Fatal("prepare-ahead's own staging prepare never reached the fake publisher")
		case <-time.After(time.Millisecond):
		}
	}

	pub.result = confirmedResultForAction("pause", nightBackgroundAudioSessionID(rec), "started")
	done := make(chan struct{})
	go func() {
		h.nightStopBackgroundAudioIfRunning(context.Background(), testNow, rec)
		close(done)
	}()

	select {
	case <-done:
	case <-time.After(1 * time.Second):
		t.Fatal("the bed pause for node-a was not published within the bound while its own prepare-ahead staging prepare was still in flight")
	}
	if pub.lastAction != "audio.session.pause" {
		t.Fatalf("dispatched action = %q, want audio.session.pause", pub.lastAction)
	}
}

// TestNightStopBackgroundAudioIfRunning_OneNodeNeverAnsweringDoesNotDelayAnother
// proves nightStopBackgroundAudioIfRunning's own documented "a stall on one
// node never withholds the stop/pause another node's own history already
// shows is due" - reproduced here because the plain sequential for loop
// this test targets contradicts exactly that doc comment: node-a's own
// pause is never answered, and node-b's own pause must still be created
// and published within the test's short bound regardless.
func TestNightStopBackgroundAudioIfRunning_OneNodeNeverAnsweringDoesNotDelayAnother(t *testing.T) {
	h, st, pub, _ := nightBackgroundAudioTestHandlers(t)
	putBackgroundAudioAsset(t, st, "halloween", "bg-a", "node-a", "asset-a")
	putBackgroundAudioAsset(t, st, "halloween", "bg-b", "node-b", "asset-b")
	ba := twoNodeBackgroundAudioConfig("node-a", "node-b")
	ba.Resume = config.NightSessionBackgroundResumeResume
	rec := mustCreateRestingSessionWithBackgroundAudio(t, st, "sess-1", "node-a", ba, nightStateRestingIntershow)
	playThroughApplyGainStart(t, h, pub, rec)

	block := make(chan struct{})
	pub.blockUntilByNode = map[string]<-chan struct{}{
		"node-a:audio.session.pause": block,
	}
	pub.resultsByNode = map[string]mqttproto.ResultPayload{
		"node-a:audio.session.pause": confirmedResultForAction("pause", nightBackgroundAudioSessionID(rec), "started"),
		"node-b:audio.session.pause": confirmedResultForAction("pause", nightBackgroundAudioSessionID(rec), "started"),
	}

	done := make(chan struct{})
	go func() {
		h.nightStopBackgroundAudioIfRunning(context.Background(), testNow, rec)
		close(done)
	}()

	deadline := time.After(1 * time.Second)
	for {
		found := false
		for _, d := range pub.dispatchedSnapshot() {
			if d.NodeID == "node-b" && d.Action == "audio.session.pause" {
				found = true
			}
		}
		if found {
			break
		}
		select {
		case <-deadline:
			close(block)
			t.Fatal("node-b's own bed pause was not published within the bound while node-a's own pause was still in flight")
		case <-time.After(time.Millisecond):
		}
	}
	select {
	case <-done:
		close(block)
		t.Fatal("nightStopBackgroundAudioIfRunning returned before node-a's own in-flight pause was unblocked")
	default:
	}

	// Let node-a's own pause resolve too, and wait for the whole dispatch
	// to finish before the test's own store closes out from under it.
	close(block)
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("nightStopBackgroundAudioIfRunning never returned after node-a's own pause was unblocked")
	}
}
