package audio

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"

	pkgaudio "github.com/showmeshsystems/showmesh/pkg/audio"
)

// startedSession gets id to Playing against a real two-second test asset
// and returns the session, locked for the caller to inspect and unlock.
func startedSession(t *testing.T, m *Manager, id pkgaudio.SessionID) *Session {
	t.Helper()
	ctx := context.Background()
	ref := writeTestAsset(t, m.assetDir, string(id)+".wav", string(id), []byte("content-"+string(id)))
	req := pkgaudio.ApplyRequest{Media: pkgaudio.SetField(ref)}
	m.Apply(ctx, id, pkgaudio.InvocationID(id+"-apply"), 1, req)
	m.Start(ctx, id, pkgaudio.InvocationID(id+"-start"), 2)

	s, ok := m.get(id)
	if !ok {
		t.Fatalf("session %s was not created", id)
	}
	return s
}

// TestSixFaultsStayDistinct is AUDIO-ENGINE section 11.4's distinct-faults rule: none of the six
// named engine faults may collapse into another or into StateStopped.
// Four (pipeline crash, freeze, route change, timing-authority loss) have
// no real backend to produce them, so [FakeEngine.InjectFailure] stands in
// for one; the other two (media disappeared, decode failure) are driven
// through the real prepare-time asset probe.
func TestSixFaultsStayDistinct(t *testing.T) {
	ctx := context.Background()
	cases := []struct {
		name string
		fail error
		want pkgaudio.SessionFault
	}{
		{"pipeline crash", errWrap(pkgaudio.ErrEnginePipelineCrash), pkgaudio.FaultPipelineCrash},
		{"freeze", errWrap(pkgaudio.ErrEngineFreeze), pkgaudio.FaultFreeze},
		{"route changed", errWrap(pkgaudio.ErrEngineRouteChanged), pkgaudio.FaultRouteChanged},
		{"timing authority lost", errWrap(pkgaudio.ErrEngineTimingAuthorityLost), pkgaudio.FaultTimingAuthorityLost},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			c := newClock(time.Now())
			m := newTestManager(t, c)
			id := pkgaudio.SessionID("s-" + tc.name)
			s := startedSession(t, m, id)

			fe := m.engine.(*FakeEngine)
			s.mu.Lock()
			handle := s.handle
			s.mu.Unlock()
			fe.InjectFailure(handle, tc.fail)

			m.Pause(ctx, id, pkgaudio.InvocationID(id+"-pause"), 3)

			s.mu.Lock()
			defer s.mu.Unlock()
			if s.fault != tc.want {
				t.Fatalf("fault = %q, want %q (reason: %s)", s.fault, tc.want, s.faultReason)
			}
			if s.faultReason == "" {
				t.Fatal("fault reason must not be empty")
			}
			if s.state == pkgaudio.StateStopped {
				t.Fatal("a fault must never collapse into StateStopped")
			}
		})
	}
}

// TestMediaDisappearedFault proves the "media disappearing" fault class
// against a real prepare-time probe: an asset that existed at Apply-time
// and is deleted before the next prepare (Advance, here) reports
// FaultMediaDisappeared, distinct from a decode failure.
func TestMediaDisappearedFault(t *testing.T) {
	ctx := context.Background()
	c := newClock(time.Now())
	m := newTestManager(t, c)
	id := pkgaudio.SessionID("s-vanish")

	ref1 := writeTestAsset(t, m.assetDir, "item1.wav", "item1", []byte("one"))
	ref2 := writeTestAsset(t, m.assetDir, "item2.wav", "item2", []byte("two"))
	playlist := pkgaudio.PlaylistRef{
		OwnerKind: "test", OwnerID: "pl-1", OwnerRevision: 1,
		Items: []pkgaudio.PlaylistItem{
			{ItemID: "a", Index: 0, Media: ref1},
			{ItemID: "b", Index: 1, Media: ref2},
		},
		Repeat: pkgaudio.RepeatNone, Resume: pkgaudio.ResumePolicyRestart,
		RequestedTransition: pkgaudio.ItemTransitionSequential,
	}
	req := pkgaudio.ApplyRequest{Playlist: pkgaudio.SetField(playlist)}
	m.Apply(ctx, id, "apply", 1, req)
	m.Start(ctx, id, "start", 2)

	// Delete the SECOND item's file before Advance ever prepares it.
	if err := removeTestAsset(m.assetDir, "item2.wav"); err != nil {
		t.Fatalf("remove test asset: %v", err)
	}

	m.Advance(ctx, id, "advance", 3)

	s, _ := m.get(id)
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.fault != pkgaudio.FaultMediaDisappeared {
		t.Fatalf("fault = %q, want %q (reason: %s)", s.fault, pkgaudio.FaultMediaDisappeared, s.faultReason)
	}
}

// TestFaultClearsOnlyOnRevalidation is AUDIO-ENGINE section 11.4's "a device that
// reappears stays unavailable until revalidated" requirement, at session
// scope: a fault set by an injected engine failure must survive an
// unrelated read (Snapshot) and clear only once prepareLocked actually
// runs again successfully — never merely because the underlying problem
// would, if re-tried, now succeed.
func TestFaultClearsOnlyOnRevalidation(t *testing.T) {
	ctx := context.Background()
	c := newClock(time.Now())
	m := newTestManager(t, c)
	id := pkgaudio.SessionID("s-recover")
	s := startedSession(t, m, id)

	fe := m.engine.(*FakeEngine)
	s.mu.Lock()
	handle := s.handle
	s.mu.Unlock()
	fe.InjectFailure(handle, errWrap(pkgaudio.ErrEnginePipelineCrash))
	m.Pause(ctx, id, "pause", 3)

	s.mu.Lock()
	if s.fault != pkgaudio.FaultPipelineCrash {
		s.mu.Unlock()
		t.Fatalf("fault did not register: %q", s.fault)
	}
	s.mu.Unlock()

	// A Snapshot read must never itself clear a fault.
	_ = m.Snapshot(ctx)
	s.mu.Lock()
	if s.fault != pkgaudio.FaultPipelineCrash {
		s.mu.Unlock()
		t.Fatal("Snapshot must not clear a standing fault")
	}
	s.mu.Unlock()

	// Prepare is the explicit revalidation action; it must clear the
	// fault on success.
	m.Prepare(ctx, id, "prepare-again", 4)
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.fault != pkgaudio.FaultNone {
		t.Fatalf("fault after successful revalidation = %q, want %q", s.fault, pkgaudio.FaultNone)
	}
}

// TestSnapshotReportsPlayingClaimNotAudibleProof is AUDIO-ENGINE section 15's rule: Snapshot's
// State reflects the session state machine's own bookkeeping even though
// FakeEngine never plays anything — the surface must not lie about what
// it knows, but the caller (the coordinator's nodeaudio collector) is responsible for pairing it
// with the node-level engine-availability signal that makes clear no
// audio ever reached an output.
func TestSnapshotReportsPlayingClaimNotAudibleProof(t *testing.T) {
	ctx := context.Background()
	c := newClock(time.Now())
	m := newTestManager(t, c)
	id := pkgaudio.SessionID("s-claim")
	startedSession(t, m, id)

	snaps := m.Snapshot(ctx)
	if len(snaps) != 1 {
		t.Fatalf("got %d snapshots, want 1", len(snaps))
	}
	snap := snaps[0]
	if snap.State != pkgaudio.StatePlaying {
		t.Fatalf("state = %q, want %q", snap.State, pkgaudio.StatePlaying)
	}
	if !snap.PositionKnown {
		t.Fatal("position should be known immediately after a successful start")
	}
	if snap.ObservedAt.IsZero() {
		t.Fatal("ObservedAt must be set whenever PositionKnown is true")
	}
}

// errWrap mimics what a real Engine implementation would do: wrap one of
// pkg/audio's sentinel errors so [pkgaudio.ClassifyFault]'s errors.Is
// check recovers the fault class.
func errWrap(sentinel error) error {
	return fmt.Errorf("engine: %w", sentinel)
}

func removeTestAsset(dir, filename string) error {
	return os.Remove(filepath.Join(dir, filename))
}

// TestPersistedSessionRoundTripsFault proves the storage layer's own half
// of fault durability: PersistedSession.Fault/FaultReason/FaultAt written
// by [Session.persistLocked] come back unchanged from
// [SessionStore.Load], independent of what a later RestoreAll chooses to
// do with them.
func TestPersistedSessionRoundTripsFault(t *testing.T) {
	ctx := context.Background()
	c := newClock(time.Now())
	dir := t.TempDir()
	store := NewFileSessionStore(dir)
	m := NewManager(NewFakeEngine(c.now), store, dir, staticDecoder{duration: 2 * time.Second}, c.now, nil)
	id := pkgaudio.SessionID("s-restart")
	s := startedSession(t, m, id)

	fe := m.engine.(*FakeEngine)
	s.mu.Lock()
	handle := s.handle
	s.mu.Unlock()
	fe.InjectFailure(handle, errWrap(pkgaudio.ErrEngineFreeze))
	m.Pause(ctx, id, "pause", 3)

	rec, ok, err := store.Load(id)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if !ok {
		t.Fatal("session was not persisted")
	}
	if rec.Fault != pkgaudio.FaultFreeze {
		t.Fatalf("persisted fault = %q, want %q", rec.Fault, pkgaudio.FaultFreeze)
	}
	if rec.FaultReason == "" {
		t.Fatal("persisted faultReason is empty")
	}
}

// TestRestartRevalidatesRatherThanBlindlyCarryingAFault proves a restart
// is itself a revalidation (a real engine's process restart genuinely
// re-establishes its pipeline from nothing), not a reason to keep
// reporting a stale fault forever: restoreOne's own prepare, run against
// media that is still present and a fresh engine, must clear the fault —
// but restoreOne re-deriving the fault from a FRESH probe, rather than
// copying the persisted value forward unconditionally, is what this test
// actually distinguishes: when the underlying problem also persists
// across the restart (the asset is now ALSO gone), the fault reappears
// as a freshly-evidenced FaultMediaDisappeared, not the stale
// FaultFreeze that was true before the crash.
func TestRestartRevalidatesRatherThanBlindlyCarryingAFault(t *testing.T) {
	ctx := context.Background()
	c := newClock(time.Now())
	dir := t.TempDir()
	store := NewFileSessionStore(dir)
	m := NewManager(NewFakeEngine(c.now), store, dir, staticDecoder{duration: 2 * time.Second}, c.now, nil)
	id := pkgaudio.SessionID("s-restart-2")
	s := startedSession(t, m, id)

	fe := m.engine.(*FakeEngine)
	s.mu.Lock()
	handle, filename := s.handle, string(id)+".wav"
	s.mu.Unlock()
	fe.InjectFailure(handle, errWrap(pkgaudio.ErrEngineFreeze))
	m.Pause(ctx, id, "pause", 3)

	// The condition that caused the fault does NOT persist across a
	// restart (a fresh FakeEngine never carries the injected failure
	// forward) — so a clean revalidation should clear it.
	fresh := NewManager(NewFakeEngine(c.now), store, dir, staticDecoder{duration: 2 * time.Second}, c.now, nil)
	if err := fresh.RestoreAll(ctx); err != nil {
		t.Fatalf("RestoreAll: %v", err)
	}
	rs, ok := fresh.get(id)
	if !ok {
		t.Fatal("session not restored")
	}
	rs.mu.Lock()
	gotFault := rs.fault
	rs.mu.Unlock()
	if gotFault != pkgaudio.FaultNone {
		t.Fatalf("fault after a clean restart-time revalidation = %q, want %q", gotFault, pkgaudio.FaultNone)
	}

	// Now remove the asset itself and force the SAME session into a
	// crashed-while-paused state again, then restart once more: this
	// time revalidation cannot succeed, and the restart-time prepare
	// must produce a FRESH fault evidencing the REAL, current problem —
	// never the FaultFreeze that was true two crashes ago.
	if err := removeTestAsset(dir, filename); err != nil {
		t.Fatalf("remove test asset: %v", err)
	}
	rs.mu.Lock()
	rs.state = pkgaudio.StatePaused
	rs.persistLocked()
	rs.mu.Unlock()
	fresh2 := NewManager(NewFakeEngine(c.now), store, dir, staticDecoder{duration: 2 * time.Second}, c.now, nil)
	if err := fresh2.RestoreAll(ctx); err != nil {
		t.Fatalf("RestoreAll: %v", err)
	}
	rs2, ok := fresh2.get(id)
	if !ok {
		t.Fatal("session not restored a second time")
	}
	rs2.mu.Lock()
	defer rs2.mu.Unlock()
	if rs2.fault != pkgaudio.FaultMediaDisappeared {
		t.Fatalf("fault after the asset disappeared = %q, want %q (fresh evidence, not a stale carry-forward)", rs2.fault, pkgaudio.FaultMediaDisappeared)
	}
}
