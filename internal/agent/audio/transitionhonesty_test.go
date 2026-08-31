package audio

import (
	"context"
	"sync"
	"testing"
	"time"

	pkgaudio "github.com/showmeshsystems/showmesh/pkg/audio"
)

// engineCallOrderRecorder wraps a *FakeEngine and records the order of
// Load/Start/Release calls, each tagged with the handle they named, so a
// test can prove ordering BETWEEN two items' handles rather than merely
// that both calls eventually happened.
type engineCallOrderRecorder struct {
	*FakeEngine
	mu    sync.Mutex
	calls []string
}

func (e *engineCallOrderRecorder) record(op string, handle EngineHandle) {
	e.mu.Lock()
	e.calls = append(e.calls, op+":"+string(handle))
	e.mu.Unlock()
}

func (e *engineCallOrderRecorder) Load(ctx context.Context, handle EngineHandle, media pkgaudio.MediaRef, duration time.Duration) (EngineObservation, error) {
	e.record("Load", handle)
	return e.FakeEngine.Load(ctx, handle, media, duration)
}

func (e *engineCallOrderRecorder) Start(ctx context.Context, handle EngineHandle, position time.Duration) (EngineObservation, error) {
	e.record("Start", handle)
	return e.FakeEngine.Start(ctx, handle, position)
}

func (e *engineCallOrderRecorder) Release(ctx context.Context, handle EngineHandle) error {
	e.record("Release", handle)
	return e.FakeEngine.Release(ctx, handle)
}

// indexOf returns the position of op:handle in the recorded call order, or
// -1 if it never happened.
func (e *engineCallOrderRecorder) indexOf(op string, handle EngineHandle) int {
	e.mu.Lock()
	defer e.mu.Unlock()
	target := op + ":" + string(handle)
	for i, c := range e.calls {
		if c == target {
			return i
		}
	}
	return -1
}

// TestAdvanceReleasesThePredecessorBeforeTheSuccessorEverLoadsRegardlessOfRequestedTransition
// is the ground-truth proof internal/agent/audiocapabilities.go's own
// audioSessionCapabilityIDs doc comment rests on: [Session.advanceLocked]
// never reads Playlist.RequestedTransition at all (grep this package
// outside _test.go for that field name: zero hits), so its
// Release-before-Load-before-Start ordering is IDENTICAL for gapless and
// crossfade to what it is for sequential. A node cannot honestly
// advertise either ability while this holds: there is no overlap for a
// gapless seam to close, and no second engine handle for a crossfade to
// blend against.
//
// This is the test a future change to advanceLocked that genuinely
// implemented overlap would have to break FIRST, before
// internal/agent's own capability-set test could ever have reason to
// change: that test only checks a literal ID list two files away and
// cannot see this fact for itself, matching
// TestFakeAudioEngineNeverAdvertisesPlaybackCapability's own pattern of
// driving a real signal rather than a hardcoded expectation.
func TestAdvanceReleasesThePredecessorBeforeTheSuccessorEverLoadsRegardlessOfRequestedTransition(t *testing.T) {
	for _, transition := range []pkgaudio.ItemTransition{pkgaudio.ItemTransitionGapless, pkgaudio.ItemTransitionCrossfade} {
		t.Run(string(transition), func(t *testing.T) {
			c := newClock(time.Now())
			dir := t.TempDir()
			store := NewFileSessionStore(dir)
			engine := &engineCallOrderRecorder{FakeEngine: NewFakeEngine(c.now)}
			m := NewManager(engine, store, dir, staticDecoder{duration: 2 * time.Second}, c.now, nil)
			ctx := context.Background()
			const id = pkgaudio.SessionID("night-session")

			playlist := twoItemPlaylist(t, dir)
			playlist.RequestedTransition = transition
			m.Apply(ctx, id, "inv-apply", 1, pkgaudio.ApplyRequest{Playlist: pkgaudio.SetField(playlist)})
			m.Start(ctx, id, "inv-start", 2)

			s, _ := m.get(id)
			s.mu.Lock()
			predecessorHandle := s.handle
			s.mu.Unlock()

			c.advance(3 * time.Second) // past item-a's 2s duration
			m.watchTick(ctx)

			s.mu.Lock()
			if s.currentItemID != "item-b" || s.state != pkgaudio.StatePlaying {
				item, state := s.currentItemID, s.state
				s.mu.Unlock()
				t.Fatalf("did not naturally advance to item-b playing: item=%q state=%s", item, state)
			}
			successorHandle := s.handle
			s.mu.Unlock()

			if predecessorHandle == successorHandle {
				t.Fatal("predecessor and successor share one handle; this test proves nothing about overlap")
			}

			releaseIdx := engine.indexOf("Release", predecessorHandle)
			loadIdx := engine.indexOf("Load", successorHandle)
			startIdx := engine.indexOf("Start", successorHandle)
			if releaseIdx < 0 {
				t.Fatalf("predecessor handle %q was never released", predecessorHandle)
			}
			if loadIdx < 0 || startIdx < 0 {
				t.Fatalf("successor handle %q was never loaded/started: load=%d start=%d", successorHandle, loadIdx, startIdx)
			}
			if releaseIdx > loadIdx || releaseIdx > startIdx {
				t.Fatalf("predecessor released at call %d, AFTER successor's load (%d) or start (%d): that is exactly the overlap %q would need, and no capability declaration may claim this build honestly does it", releaseIdx, loadIdx, startIdx, transition)
			}
		})
	}
}
