package audio

import (
	"context"
	"testing"
	"time"

	pkgaudio "github.com/showmeshsystems/showmesh/pkg/audio"
)

// TestPromoteReleasesHandleAlreadyHeldByDestinationSession reproduces the
// exact orphan sequence a rehearsal rig hit: a show session still holds a
// loaded handle for the previous cue (no watchTick has yet observed it
// complete) when the NEXT cue's staged handle is promoted onto that same
// session with a scheduled instant. Before the fix, Promote overwrote
// to.handle with the staged handle and never released the one already
// there, leaving it live in the engine, unaddressable by anything — a
// later Stop only ever reaches the NEW handle. This must fail before the
// fix (the engine still reports a live handle after Stop) and pass after
// it (no live handle remains).
func TestPromoteReleasesHandleAlreadyHeldByDestinationSession(t *testing.T) {
	c := newClock(time.Unix(1_700_000_000, 0))
	dir := t.TempDir()
	engine := NewFakeEngine(c.now)
	m := NewManager(availableFakeEngine{engine}, NewFileSessionStore(dir), dir, staticDecoder{duration: time.Hour}, c.now, nil)
	media := newFakeClockSource(time.Unix(4_000_000_000, 0))
	media.setWallNow(c.now)
	m.SetClockSource(media)

	ctx := context.Background()
	const show = pkgaudio.SessionID("cue-activation:show")
	const staging = pkgaudio.SessionID("cue-activation:stage")

	// Session A (the show session) ends up holding a loaded handle for
	// media X (the previous cue) that nothing has released yet.
	refX := writeTestAsset(t, dir, "x.wav", "asset-x", []byte("kpop"))
	reqX := pkgaudio.ApplyRequest{Media: pkgaudio.SetField(refX)}
	if r := m.Apply(ctx, show, "show-apply-x", 1, reqX); r.Outcome == pkgaudio.OutcomeRefused {
		t.Fatalf("show apply X refused: %+v", r)
	}
	if r := m.Start(ctx, show, "show-start-x", 2); r.Outcome != pkgaudio.OutcomeStarted {
		t.Fatalf("show start X = %+v, want started", r)
	}
	handleX, ok := engine.LastLoadedHandle()
	if !ok {
		t.Fatal("no handle loaded for show session X; test setup is broken")
	}

	// Staging holds a prepared handle for media Y (the next cue).
	refY := writeTestAsset(t, dir, "y.wav", "asset-y", []byte("wake-up"))
	reqY := pkgaudio.ApplyRequest{Media: pkgaudio.SetField(refY)}
	if r := m.Apply(ctx, staging, "stage-apply-y", 1, reqY); r.Outcome == pkgaudio.OutcomeRefused {
		t.Fatalf("stage apply Y refused: %+v", r)
	}
	if r := m.Prepare(ctx, staging, "stage-prepare-y", 2); r.Outcome == pkgaudio.OutcomeRefused {
		t.Fatalf("stage prepare Y refused: %+v", r)
	}
	handleY, ok := engine.LastLoadedHandle()
	if !ok || handleY == handleX {
		t.Fatalf("no distinct handle loaded for staged Y; test setup is broken (got %q)", handleY)
	}

	// Apply Y onto the show session -- Apply never touches the engine, so
	// A's own handle for X is still loaded and untouched here.
	if r := m.Apply(ctx, show, "show-apply-y", 3, reqY); r.Outcome == pkgaudio.OutcomeRefused {
		t.Fatalf("show apply Y refused: %+v", r)
	}

	// Promote staging onto the show session with a scheduled instant,
	// exactly as the night controller's early-staged first cue does.
	t0 := media.Now(ctx).Time.Add(30 * time.Millisecond)
	before := media.reads()
	done := make(chan pkgaudio.OutcomeResult, 1)
	go func() {
		done <- m.PromoteAtPosition(ctx, staging, show, "promote-y", 4, t0.UnixNano(), 0)
	}()
	media.waitForReads(t, before+1)
	media.advance(30 * time.Millisecond)

	var promoted pkgaudio.OutcomeResult
	select {
	case promoted = <-done:
	case <-time.After(10 * time.Second):
		t.Fatal("PromoteAtPosition never returned after the media clock reached T0")
	}
	if promoted.Outcome != pkgaudio.OutcomeStarted {
		t.Fatalf("PromoteAtPosition = %+v, want started", promoted)
	}

	if r := m.Stop(ctx, show, "show-stop", 5); r.Outcome == pkgaudio.OutcomeRefused {
		t.Fatalf("show stop refused: %+v", r)
	}

	live, err := engine.LiveHandles(ctx)
	if err != nil {
		t.Fatalf("LiveHandles: %v", err)
	}
	if len(live) != 0 {
		t.Fatalf("engine still holds live handle(s) after promote+stop: %v (media X's handle %q was never released, orphaning it)", live, handleX)
	}
}

// TestWatchTickReleasesOrphanEngineHandleAfterTwoTicks proves an injected
// orphan handle survives one tick and is only released once a second
// consecutive tick still finds it unowned; an owned handle is untouched.
func TestWatchTickReleasesOrphanEngineHandleAfterTwoTicks(t *testing.T) {
	c := newClock(time.Unix(1_700_000_000, 0))
	dir := t.TempDir()
	engine := NewFakeEngine(c.now)
	m := NewManager(availableFakeEngine{engine}, NewFileSessionStore(dir), dir, staticDecoder{duration: 2 * time.Second}, c.now, nil)
	ctx := context.Background()

	const owned = pkgaudio.SessionID("owned")
	ref := writeTestAsset(t, m.assetDir, "a.wav", "asset-a", []byte("owned content"))
	req := pkgaudio.ApplyRequest{Media: pkgaudio.SetField(ref)}
	if r := m.Apply(ctx, owned, "owned-apply", 1, req); r.Outcome == pkgaudio.OutcomeRefused {
		t.Fatalf("owned apply refused: %+v", r)
	}
	if r := m.Start(ctx, owned, "owned-start", 2); r.Outcome != pkgaudio.OutcomeStarted {
		t.Fatalf("owned start = %+v, want started", r)
	}
	ownedHandle, ok := engine.LastLoadedHandle()
	if !ok {
		t.Fatal("no handle loaded for the owned session; test setup is broken")
	}

	const orphan = EngineHandle("nobody-owns-this/track")
	orphanRef := writeTestAsset(t, m.assetDir, "b.wav", "asset-b", []byte("orphan content"))
	if _, err := engine.Load(ctx, orphan, orphanRef, time.Hour); err != nil {
		t.Fatalf("injecting orphan handle: %v", err)
	}
	if _, err := engine.Start(ctx, orphan, 0); err != nil {
		t.Fatalf("starting orphan handle: %v", err)
	}

	live, err := engine.LiveHandles(ctx)
	if err != nil || len(live) != 2 {
		t.Fatalf("LiveHandles before tick = %v, %v, want exactly 2 (owned + orphan)", live, err)
	}

	m.watchTick(ctx)

	live, err = engine.LiveHandles(ctx)
	if err != nil {
		t.Fatalf("LiveHandles after first tick: %v", err)
	}
	if len(live) != 2 {
		t.Fatalf("LiveHandles after first tick = %v, want both handles still live (one sighting is not enough to release)", live)
	}

	m.watchTick(ctx)

	live, err = engine.LiveHandles(ctx)
	if err != nil {
		t.Fatalf("LiveHandles after second tick: %v", err)
	}
	if len(live) != 1 || live[0] != ownedHandle {
		t.Fatalf("LiveHandles after second tick = %v, want exactly [%q] (orphan released, owned handle untouched)", live, ownedHandle)
	}
}

// TestWatchTickNeverReleasesHandleThatBecomesOwnedBetweenTicks proves a
// handle unowned on one tick but claimed by a session before the next
// tick runs is never released.
func TestWatchTickNeverReleasesHandleThatBecomesOwnedBetweenTicks(t *testing.T) {
	c := newClock(time.Unix(1_700_000_000, 0))
	dir := t.TempDir()
	engine := NewFakeEngine(c.now)
	m := NewManager(availableFakeEngine{engine}, NewFileSessionStore(dir), dir, staticDecoder{duration: 2 * time.Second}, c.now, nil)
	ctx := context.Background()

	const claimant = pkgaudio.SessionID("claimant")
	const handle = EngineHandle("about-to-be-claimed/track")
	ref := writeTestAsset(t, m.assetDir, "c.wav", "asset-c", []byte("claimed content"))
	if _, err := engine.Load(ctx, handle, ref, time.Hour); err != nil {
		t.Fatalf("injecting handle: %v", err)
	}
	if _, err := engine.Start(ctx, handle, 0); err != nil {
		t.Fatalf("starting handle: %v", err)
	}

	m.watchTick(ctx)

	live, err := engine.LiveHandles(ctx)
	if err != nil || len(live) != 1 {
		t.Fatalf("LiveHandles after first tick = %v, %v, want exactly 1 (still unowned, not yet released)", live, err)
	}

	// A session claims the handle between ticks, exactly as Promote does
	// when it assigns a staged handle onto its destination session.
	s := m.getOrCreate(claimant)
	s.mu.Lock()
	s.handle = handle
	s.handleLoaded = true
	s.state = pkgaudio.StateReady
	s.mu.Unlock()

	m.watchTick(ctx)

	live, err = engine.LiveHandles(ctx)
	if err != nil {
		t.Fatalf("LiveHandles after second tick: %v", err)
	}
	if len(live) != 1 || live[0] != handle {
		t.Fatalf("LiveHandles after second tick = %v, want [%q] still live (now owned, never released)", live, handle)
	}
}
