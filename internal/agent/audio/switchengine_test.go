package audio

import (
	"context"
	"testing"
	"time"

	pkgaudio "github.com/showmeshsystems/showmesh/pkg/audio"
)

// TestSwitchableEngineUnboundReportsNoBinding proves a SwitchableEngine
// with nothing ever Set reports unavailable and refuses every
// state-changing call with the same reason, rather than a nil-pointer
// panic or a silent no-op.
func TestSwitchableEngineUnboundReportsNoBinding(t *testing.T) {
	e := NewSwitchableEngine()
	if ok, reason := e.Available(); ok || reason != SwitchableEngineNoBindingReason {
		t.Errorf("Available() = (%v, %q), want (false, %q)", ok, reason, SwitchableEngineNoBindingReason)
	}
	if _, err := e.Load(context.Background(), "h", pkgaudio.MediaRef{}, time.Second); err == nil {
		t.Error("Load on an unbound SwitchableEngine returned nil error, want a refusal")
	}
	if err := e.Release(context.Background(), "h"); err != nil {
		t.Errorf("Release on an unbound SwitchableEngine = %v, want nil (idempotent on an unknown handle)", err)
	}
}

// TestSwitchableEngineDelegatesToCurrent proves Set actually rebinds
// every call to the newly set engine, not merely the first one ever set.
func TestSwitchableEngineDelegatesToCurrent(t *testing.T) {
	e := NewSwitchableEngine()
	now := time.Now
	first := NewFakeEngine(now)
	e.Set(first)
	if ok, reason := e.Available(); ok != false || reason != FakeEngineUnavailableReason {
		t.Errorf("Available() = (%v, %q), want (%v, %q)", ok, reason, false, FakeEngineUnavailableReason)
	}

	if _, err := e.Load(context.Background(), "h1", pkgaudio.MediaRef{AssetID: "a"}, time.Second); err != nil {
		t.Fatalf("Load against first engine: %v", err)
	}
	if _, err := e.Observe(context.Background(), "h1"); err != nil {
		t.Fatalf("Observe against first engine: %v", err)
	}

	second := NewFakeEngine(now)
	e.Set(second)
	if _, err := e.Observe(context.Background(), "h1"); err == nil {
		t.Error("Observe(h1) against the second engine returned nil error, want a failure: h1 was never loaded on it")
	}
}

// TestRebindEngineInvalidatesInFlightSessions proves the vertical-slice
// requirement: a session with a live engine handle survives a
// RebindEngine call as an explicit, visible failure — never a silent
// drop and never still reported as playing against a handle the new
// engine has never heard of.
func TestRebindEngineInvalidatesInFlightSessions(t *testing.T) {
	c := newClock(time.Now())
	dir := t.TempDir()
	store := NewFileSessionStore(dir)
	switchable := NewSwitchableEngine()
	first := NewFakeEngine(c.now)
	switchable.Set(first)

	m := NewManager(switchable, store, dir, staticDecoder{duration: 2 * time.Second}, c.now, nil)
	ctx := context.Background()
	const id = pkgaudio.SessionID("night-session")

	media := writeTestAsset(t, dir, "a.wav", "asset-a", []byte("aaa"))
	m.Apply(ctx, id, "inv-apply", 1, pkgaudio.ApplyRequest{Media: pkgaudio.SetField(media)})
	m.Start(ctx, id, "inv-start", 2)

	s, ok := m.get(id)
	if !ok {
		t.Fatal("session not created")
	}
	s.mu.Lock()
	if s.state != pkgaudio.StatePlaying {
		s.mu.Unlock()
		t.Fatalf("precondition: session not playing: state=%s", s.state)
	}
	s.mu.Unlock()

	second := NewFakeEngine(c.now)
	m.RebindEngine(context.Background(), switchable, second, "test rebind")

	s.mu.Lock()
	defer s.mu.Unlock()
	if s.state != pkgaudio.StateFailed {
		t.Errorf("session state after RebindEngine = %s, want %s (never still playing against a stale handle)", s.state, pkgaudio.StateFailed)
	}
	if s.fault != pkgaudio.FaultRouteChanged {
		t.Errorf("session fault after RebindEngine = %s, want %s", s.fault, pkgaudio.FaultRouteChanged)
	}
	if s.faultReason == "" {
		t.Error("session fault reason after RebindEngine is empty, want the rebind reason")
	}
	if s.handleLoaded {
		t.Error("session still reports handleLoaded after RebindEngine, want false")
	}

	// switchable now delegates to second, not first — same
	// FakeEngineUnavailableReason either way (both are FakeEngine), but
	// this proves the swap happened via the SwitchableEngine's own
	// pass-through rather than asserting anything second-engine-specific.
	if ok, reason := switchable.Available(); ok || reason != FakeEngineUnavailableReason {
		t.Errorf("switchable.Available() after RebindEngine = (%v, %q), want (false, %q)", ok, reason, FakeEngineUnavailableReason)
	}
}

// TestSwitchableEngineDetachedAfterBindingReportsRebindInProgress proves
// a SwitchableEngine that has ALREADY been bound to a real engine, and is
// then detached again (the window a rebind opens between releasing the
// outgoing engine and binding its replacement), reports
// [SwitchableEngineRebindInProgressReason] rather than
// [SwitchableEngineNoBindingReason]: a binding WAS delivered, so this
// must not read the same as a node that was never configured. A command
// attempted in that window must also classify as
// [pkgaudio.FaultRouteChanged], not [pkgaudio.FaultOther]: the node's
// engine changed out from under it, the same fact a live session's
// invalidation already reports.
func TestSwitchableEngineDetachedAfterBindingReportsRebindInProgress(t *testing.T) {
	e := NewSwitchableEngine()

	// Never bound: the classic "never configured" reason.
	if ok, reason := e.Available(); ok || reason != SwitchableEngineNoBindingReason {
		t.Fatalf("precondition: Available() = (%v, %q), want (false, %q)", ok, reason, SwitchableEngineNoBindingReason)
	}

	e.Set(NewFakeEngine(time.Now))
	e.Set(nil) // detach again, as a rebind does mid-flight

	if ok, reason := e.Available(); ok || reason != SwitchableEngineRebindInProgressReason {
		t.Errorf("Available() after a binding then a detach = (%v, %q), want (false, %q)", ok, reason, SwitchableEngineRebindInProgressReason)
	}

	_, err := e.Load(context.Background(), "h", pkgaudio.MediaRef{}, time.Second)
	if err == nil {
		t.Fatal("Load on a detached-after-binding SwitchableEngine returned nil error, want a refusal")
	}
	if fault := pkgaudio.ClassifyFault(err); fault != pkgaudio.FaultRouteChanged {
		t.Errorf("ClassifyFault(err) = %s, want %s: err=%v", fault, pkgaudio.FaultRouteChanged, err)
	}
}
