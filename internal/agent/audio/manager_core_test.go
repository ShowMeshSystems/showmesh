package audio

import (
	"context"
	"testing"
	"time"

	pkgaudio "github.com/showmeshsystems/showmesh/pkg/audio"
)

func TestFakeEngineNeverReportsAvailable(t *testing.T) {
	e := NewFakeEngine(time.Now)
	ok, reason := e.Available()
	if ok {
		t.Fatal("FakeEngine.Available() must always be false; nothing may report it as a working audio engine")
	}
	if reason == "" {
		t.Fatal("FakeEngine.Available() must carry a reason when false")
	}
}

// mutation target: gateAvailability's outcome switch — every successful
// engine-shaped outcome, from a genuinely-executed operation, must arrive
// at the caller as Unconfirmable while the wired Engine is unavailable.
func TestManagerNeverReportsSuccessWithFakeEngine(t *testing.T) {
	c := newClock(time.Now())
	m := newTestManager(t, c)
	ctx := context.Background()
	const id = pkgaudio.SessionID("s1")

	ref := writeTestAsset(t, m.assetDir, "a.wav", "asset-1", []byte("content"))
	req := pkgaudio.ApplyRequest{Media: pkgaudio.SetField(ref)}

	apply := m.Apply(ctx, id, "inv-1", 1, req)
	assertUnconfirmable(t, apply)

	start := m.Start(ctx, id, "inv-2", 2)
	assertUnconfirmable(t, start)

	pause := m.Pause(ctx, id, "inv-3", 3)
	assertUnconfirmable(t, pause)

	// Internal state must still have genuinely progressed through the
	// state machine, proving gateAvailability only affects the OUTWARD
	// report — see the doc comment on why exec must still run.
	s, ok := m.get(id)
	if !ok {
		t.Fatal("session was not created")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.state != pkgaudio.StatePaused {
		t.Fatalf("internal session state = %q, want %q (fake engine ran but the caller sees Unconfirmable)", s.state, pkgaudio.StatePaused)
	}
}

func assertUnconfirmable(t *testing.T, r pkgaudio.OutcomeResult) {
	t.Helper()
	if r.Outcome != pkgaudio.OutcomeUnconfirmable {
		t.Fatalf("outcome = %q (%q), want %q", r.Outcome, r.Reason, pkgaudio.OutcomeUnconfirmable)
	}
	if r.Reason == "" {
		t.Fatal("Unconfirmable outcome must carry a reason")
	}
}

func TestApplyRejectsStaleRevision(t *testing.T) {
	c := newClock(time.Now())
	m := newTestManager(t, c)
	ctx := context.Background()
	const id = pkgaudio.SessionID("s1")
	ref := writeTestAsset(t, m.assetDir, "a.wav", "asset-1", []byte("x"))
	req := pkgaudio.ApplyRequest{Media: pkgaudio.SetField(ref)}

	if r := m.Apply(ctx, id, "inv-1", 5, req); r.Outcome != pkgaudio.OutcomeUnconfirmable {
		t.Fatalf("first apply = %+v", r)
	}
	r := m.Apply(ctx, id, "inv-2", 5, req) // not strictly greater than 5
	if r.Outcome != pkgaudio.OutcomeRefused || r.Reason != pkgaudio.ReasonStaleRevision {
		t.Fatalf("stale revision apply = %+v, want refused/%s", r, pkgaudio.ReasonStaleRevision)
	}
}

func TestApplyReplayIsIdempotentAndDoesNotReexecute(t *testing.T) {
	c := newClock(time.Now())
	m := newTestManager(t, c)
	ctx := context.Background()
	const id = pkgaudio.SessionID("s1")
	ref := writeTestAsset(t, m.assetDir, "a.wav", "asset-1", []byte("x"))
	req := pkgaudio.ApplyRequest{Media: pkgaudio.SetField(ref)}

	first := m.Apply(ctx, id, "inv-1", 1, req)
	second := m.Apply(ctx, id, "inv-1", 1, req) // exact replay
	if first != second {
		t.Fatalf("replay returned a different outcome: %+v vs %+v", first, second)
	}

	mismatch := m.Apply(ctx, id, "inv-1", 2, req) // same invocation, different revision
	if mismatch.Outcome != pkgaudio.OutcomeRefused || mismatch.Reason != pkgaudio.ReasonInvocationRevisionMismatch {
		t.Fatalf("mismatched replay = %+v", mismatch)
	}
}

func TestStopNeverRefusedForWantOfEvidence(t *testing.T) {
	c := newClock(time.Now())
	m := newTestManager(t, c)
	ctx := context.Background()
	// A session that has never been applied at all still reports Stopped
	// (well, Refused only because it does not exist — Stop on a NON-
	// EXISTENT session id is the one legitimate refusal shape; an
	// existing session with no loaded engine handle must not refuse).
	const id = pkgaudio.SessionID("s1")
	ref := writeTestAsset(t, m.assetDir, "a.wav", "asset-1", []byte("x"))
	m.Apply(ctx, id, "inv-1", 1, pkgaudio.ApplyRequest{Media: pkgaudio.SetField(ref)})

	r := m.Stop(ctx, id, "inv-2", 2)
	if r.Outcome == pkgaudio.OutcomeRefused {
		t.Fatalf("Stop on a session with no active playback must never be Refused, got %+v", r)
	}
}

func TestClearNeverRefusedForWantOfEvidence(t *testing.T) {
	c := newClock(time.Now())
	m := newTestManager(t, c)
	ctx := context.Background()
	r := m.Clear(ctx, "never-existed", "inv-1", 1)
	if r.Outcome == pkgaudio.OutcomeRefused {
		t.Fatalf("Clear on an unknown session must never be Refused, got %+v", r)
	}
}
