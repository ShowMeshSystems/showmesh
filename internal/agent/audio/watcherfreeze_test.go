package audio

import (
	"context"
	"strings"
	"sync"
	"testing"
	"time"

	pkgaudio "github.com/showmeshsystems/showmesh/pkg/audio"
)

// hangingStartEngine wraps [FakeEngine] and makes Start against one
// specific handle block until either releaseHang is called or its own
// context is done, simulating the wedged natural-completion advance
// this file is about: a real engine's cgo call that never returns, the way
// [hangingObserveEngine] simulates the identical hazard for Observe.
type hangingStartEngine struct {
	*FakeEngine

	mu         sync.Mutex
	hangHandle EngineHandle
	release    chan struct{}
}

func newHangingStartEngine(now func() time.Time) *hangingStartEngine {
	return &hangingStartEngine{FakeEngine: NewFakeEngine(now), release: make(chan struct{})}
}

// armHang makes the NEXT Start against handle block until releaseHang is
// called (or ctx is done). Only one handle is ever armed at a time.
func (e *hangingStartEngine) armHang(handle EngineHandle) {
	e.mu.Lock()
	defer e.mu.Unlock()
	e.hangHandle = handle
}

// releaseHang lets every currently-blocked and future Start proceed
// normally. Idempotent, so a test's t.Cleanup can always call it safely
// even after the test already released it itself.
func (e *hangingStartEngine) releaseHang() {
	e.mu.Lock()
	defer e.mu.Unlock()
	select {
	case <-e.release:
	default:
		close(e.release)
	}
}

func (e *hangingStartEngine) Start(ctx context.Context, handle EngineHandle, position time.Duration) (EngineObservation, error) {
	e.mu.Lock()
	hang := handle == e.hangHandle && e.hangHandle != ""
	release := e.release
	e.mu.Unlock()
	if hang {
		select {
		case <-release:
		case <-ctx.Done():
			return EngineObservation{}, ctx.Err()
		}
	}
	return e.FakeEngine.Start(ctx, handle, position)
}

// wedgedAdvanceSetup builds a two-item playlist session already Playing
// item-a, with item-b's engine handle armed to hang the next Start
// against it, and the clock already advanced past item-a's known
// duration so the next watchTick observes natural completion and drives
// advanceLocked into the wedged Start. t.Cleanup releases the hang so no
// goroutine this test starts can outlive it indefinitely.
func wedgedAdvanceSetup(t *testing.T) (m *Manager, id pkgaudio.SessionID) {
	t.Helper()
	c := newClock(time.Now())
	dir := t.TempDir()
	engine := newHangingStartEngine(c.now)
	m = NewManager(engine, NewFileSessionStore(dir), dir, staticDecoder{duration: 50 * time.Millisecond}, c.now, nil)
	t.Cleanup(engine.releaseHang)
	ctx := context.Background()
	id = pkgaudio.SessionID("night-session")

	playlist := twoItemPlaylist(t, dir)
	// Apply/Start report Unconfirmable against [hangingStartEngine]'s
	// embedded [FakeEngine], which always reports Available() false (see
	// that type's own doc comment); gateAvailability rewrites every
	// non-Refused/Failed outcome accordingly, so the session's own
	// internal state, not the reported outcome, is what this setup and
	// its callers assert against, matching advance_test.go's identical
	// convention.
	m.Apply(ctx, id, "inv-apply", 1, pkgaudio.ApplyRequest{Playlist: pkgaudio.SetField(playlist)})
	m.Start(ctx, id, "inv-start", 2)

	s, ok := m.get(id)
	if !ok {
		t.Fatal("session not created")
	}
	s.mu.Lock()
	state := s.state
	s.mu.Unlock()
	if state != pkgaudio.StatePlaying {
		t.Fatalf("precondition: session not playing item-a: state=%s", state)
	}
	hangHandle := s.engineHandleFor("item-b")
	engine.armHang(hangHandle)

	// Past item-a's 50ms known duration: the next Observe reports
	// Completed and watchTick's own advanceLocked call takes over.
	c.advance(200 * time.Millisecond)

	return m, id
}

// wedgedWaitBound is how long these tests wait for a watchTick or
// Snapshot call that must be bounded before giving up and reporting the
// hang as a test failure. Generous relative to the shrunk
// engineCallTimeout/snapshotLockBudget these tests install, so a slow
// CI machine cannot make a genuinely-fixed call look like a failure.
// This file's job is to prove the call returns in bounded time at all,
// not to pin an exact bound.
const wedgedWaitBound = 5 * time.Second

// TestWatchTickWedgedAdvanceStartDoesNotStallOtherSessions is this
// package's regression test for a wedged GStreamer call freezing all
// audio telemetry on the node. advanceLocked's own engine.Start
// call for the natural-completion advance runs under the caller's raw
// context; RunWatcher is wired with the agent's root context, which has
// no deadline (agent.go), so a wedged Start holds the session mutex
// forever and [Manager.Snapshot], which locks every session in turn,
// hangs behind it for every session on the node, not just the wedged
// one.
//
// This test proves three things: watchTick itself must return in bounded
// time (a wedged Start must be attributed and reported as a failure, not
// left to hang the tick); a concurrent Snapshot call must not stall on
// the wedged session either, and must keep reporting fresh telemetry for
// every OTHER session; and the wedged session's own fallback evidence
// must carry Stale=true and its ORIGINAL Fault (FaultNone here — nothing
// was actually wrong with it) forward unchanged, never collapsed into a
// fabricated fault that would destroy a real one.
func TestWatchTickWedgedAdvanceStartDoesNotStallOtherSessions(t *testing.T) {
	prevEngineCall := engineCallTimeout
	engineCallTimeout = 300 * time.Millisecond
	defer func() { engineCallTimeout = prevEngineCall }()
	prevBudget := snapshotLockBudget
	snapshotLockBudget = 50 * time.Millisecond
	defer func() { snapshotLockBudget = prevBudget }()

	m, wedgedID := wedgedAdvanceSetup(t)
	ctx := context.Background()

	// A second, healthy session sharing the same Manager/report tick.
	const healthyID = pkgaudio.SessionID("healthy-session")
	ref := writeTestAsset(t, m.assetDir, "h.wav", "asset-h", []byte("hhh"))
	m.Apply(ctx, healthyID, "inv-h-apply", 1, pkgaudio.ApplyRequest{Media: pkgaudio.SetField(ref)})
	m.Start(ctx, healthyID, "inv-h-start", 2)
	hs, ok := m.get(healthyID)
	if !ok {
		t.Fatal("healthy session not created")
	}
	hs.mu.Lock()
	healthyState, healthyLoaded := hs.state, hs.handleLoaded
	hs.mu.Unlock()
	if healthyState != pkgaudio.StatePlaying || !healthyLoaded {
		t.Fatalf("precondition: healthy session not playing: state=%s handleLoaded=%v", healthyState, healthyLoaded)
	}

	// Confirm the wedged session's own last-good evidence, BEFORE the
	// wedge starts, genuinely carries FaultNone: the assertion below that
	// the fallback preserves Fault unchanged is only meaningful if the
	// pre-wedge fault was not already "other".
	wedgedSession, _ := m.get(wedgedID)
	wedgedSession.mu.Lock()
	preWedgeFault := wedgedSession.fault
	wedgedSession.mu.Unlock()
	if preWedgeFault != pkgaudio.FaultNone {
		t.Fatalf("precondition: wedged session already faulted (%s) before the wedge started", preWedgeFault)
	}

	// Prime both sessions' lastSnapshot with one genuine, healthy
	// snapshot before the wedge starts: the fallback this test exercises
	// below reports THAT retained evidence, not a snapshot that was
	// never collected in the first place.
	m.Snapshot(ctx)

	watchDone := make(chan struct{})
	go func() {
		m.watchTick(ctx)
		close(watchDone)
	}()

	// watchTick locks the wedged session first inside the same goroutine;
	// give it a moment to actually reach the blocked Start call before
	// racing Snapshot against it.
	time.Sleep(20 * time.Millisecond)

	snapStart := time.Now()
	snapDone := make(chan []SessionSnapshot, 1)
	go func() {
		snapDone <- m.Snapshot(ctx)
	}()

	select {
	case snaps := <-snapDone:
		// Snapshot must return on its own snapshotLockBudget, not on
		// advanceLocked's much longer engineCallTimeout: if Snapshot
		// merely blocked on s.mu until the wedged Start itself timed out,
		// this would still eventually return within wedgedWaitBound, so
		// that bound alone cannot tell "bounded by design" apart from
		// "happened to finish before an unrelated generous test timeout".
		if elapsed := time.Since(snapStart); elapsed >= engineCallTimeout/2 {
			t.Errorf("Manager.Snapshot took %s, at or beyond engineCallTimeout (%s); it must be bounded by its own, much shorter snapshotLockBudget, not by waiting out the wedged session's own engine-call bound", elapsed, engineCallTimeout)
		}

		var sawWedged, sawHealthy bool
		for _, snap := range snaps {
			if snap.ID == wedgedID {
				sawWedged = true
				if !snap.Stale {
					t.Errorf("wedged session snapshot has Stale=false while Start is still in flight; want the staleness carrier set")
				}
				if snap.CollectedAt.IsZero() {
					t.Errorf("wedged session snapshot has a zero CollectedAt; a reader cannot age stale evidence without it")
				}
				// Fault answers "what is wrong"; Stale answers "how old
				// is this evidence" — the two axes must never collapse.
				// Nothing was ever wrong with this session, so its
				// fallback must still report FaultNone, not a fabricated
				// fault manufactured by the fallback path itself.
				if snap.Fault != pkgaudio.FaultNone {
					t.Errorf("wedged session snapshot Fault = %q, want the original FaultNone preserved unchanged", snap.Fault)
				}
				// The fallback must carry the session's own last known
				// evidence forward, not a blank placeholder: item-a was
				// genuinely playing right before the wedge, so that must
				// still show up here.
				if !snap.HasItem || snap.ItemID == "" {
					t.Errorf("wedged session snapshot lost its last-known item identity; got HasItem=%v ItemID=%q", snap.HasItem, snap.ItemID)
				}
				if snap.State == pkgaudio.StateUnknown {
					t.Errorf("wedged session snapshot reports State unknown; want its last known state carried forward, not a blank fallback")
				}
			}
			if snap.ID == healthyID {
				sawHealthy = true
				if !snap.PositionKnown {
					t.Errorf("healthy session's own telemetry was not freshly collected while an unrelated session was wedged")
				}
				if snap.Stale {
					t.Errorf("healthy session snapshot reports Stale=true; only the wedged session should")
				}
			}
		}
		if !sawWedged {
			t.Fatalf("Snapshot did not report the wedged session %q at all", wedgedID)
		}
		if !sawHealthy {
			t.Fatalf("Snapshot did not report the healthy session %q at all", healthyID)
		}
	case <-time.After(wedgedWaitBound):
		t.Fatal("Manager.Snapshot did not return within a bounded time while one session was wedged inside advanceLocked's Start call")
	}

	select {
	case <-watchDone:
	case <-time.After(wedgedWaitBound):
		t.Fatal("watchTick did not return within a bounded time despite a wedged advanceLocked Start call")
	}

	s, _ := m.get(wedgedID)
	s.mu.Lock()
	state, fault, reason := s.state, s.fault, s.faultReason
	s.mu.Unlock()
	if state != pkgaudio.StateFailed {
		t.Fatalf("wedged session state = %q after its Start call timed out, want Failed", state)
	}
	if fault == pkgaudio.FaultNone {
		t.Fatal("wedged session has no fault recorded after its Start call timed out")
	}
	if !strings.Contains(reason, "deadline") {
		t.Fatalf("wedged session fault reason = %q, want it to attribute the failure to the bounded context timing out", reason)
	}
}

// TestAdvanceLockedBoundsItsOwnEngineCalls is a narrower, single-session
// version of the same defect: even with no concurrent Snapshot call,
// advanceLocked itself must not block forever on a wedged Start.
func TestAdvanceLockedBoundsItsOwnEngineCalls(t *testing.T) {
	prevEngineCall := engineCallTimeout
	engineCallTimeout = 300 * time.Millisecond
	defer func() { engineCallTimeout = prevEngineCall }()

	m, id := wedgedAdvanceSetup(t)
	ctx := context.Background()

	done := make(chan struct{})
	go func() {
		m.watchTick(ctx)
		close(done)
	}()

	select {
	case <-done:
	case <-time.After(wedgedWaitBound):
		t.Fatal("watchTick did not return within a bounded time despite a wedged advanceLocked Start call")
	}

	s, _ := m.get(id)
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.state != pkgaudio.StateFailed {
		t.Fatalf("wedged session state = %q, want Failed once its bounded Start call timed out", s.state)
	}
}

// hangingLoadEngine wraps [FakeEngine] and makes Load against one
// specific handle block until either releaseHang is called or its own
// context is done, the same simulated wedge [hangingStartEngine] gives
// Start, but for advanceLocked's own prepareLocked/Load call, which is
// bounded by its own, separate [boundedEngineCallContext] call.
type hangingLoadEngine struct {
	*FakeEngine

	mu         sync.Mutex
	hangHandle EngineHandle
	release    chan struct{}
}

func newHangingLoadEngine(now func() time.Time) *hangingLoadEngine {
	return &hangingLoadEngine{FakeEngine: NewFakeEngine(now), release: make(chan struct{})}
}

func (e *hangingLoadEngine) armHang(handle EngineHandle) {
	e.mu.Lock()
	defer e.mu.Unlock()
	e.hangHandle = handle
}

func (e *hangingLoadEngine) releaseHang() {
	e.mu.Lock()
	defer e.mu.Unlock()
	select {
	case <-e.release:
	default:
		close(e.release)
	}
}

func (e *hangingLoadEngine) Load(ctx context.Context, handle EngineHandle, media pkgaudio.MediaRef, duration time.Duration) (EngineObservation, error) {
	e.mu.Lock()
	hang := handle == e.hangHandle && e.hangHandle != ""
	release := e.release
	e.mu.Unlock()
	if hang {
		select {
		case <-release:
		case <-ctx.Done():
			return EngineObservation{}, ctx.Err()
		}
	}
	return e.FakeEngine.Load(ctx, handle, media, duration)
}

// TestAdvanceLockedBoundsItsOwnLoadCall proves advanceLocked's
// prepareLocked/Load call has its own bound distinct from the Start call
// [TestAdvanceLockedBoundsItsOwnEngineCalls] exercises: a wedge in Load
// alone, never reaching Start, must still make watchTick return in
// bounded time.
func TestAdvanceLockedBoundsItsOwnLoadCall(t *testing.T) {
	prevEngineCall := engineCallTimeout
	engineCallTimeout = 300 * time.Millisecond
	defer func() { engineCallTimeout = prevEngineCall }()

	c := newClock(time.Now())
	dir := t.TempDir()
	engine := newHangingLoadEngine(c.now)
	m := NewManager(engine, NewFileSessionStore(dir), dir, staticDecoder{duration: 50 * time.Millisecond}, c.now, nil)
	t.Cleanup(engine.releaseHang)
	ctx := context.Background()
	const id = pkgaudio.SessionID("night-session")

	playlist := twoItemPlaylist(t, dir)
	m.Apply(ctx, id, "inv-apply", 1, pkgaudio.ApplyRequest{Playlist: pkgaudio.SetField(playlist)})
	m.Start(ctx, id, "inv-start", 2)

	s, ok := m.get(id)
	if !ok {
		t.Fatal("session not created")
	}
	s.mu.Lock()
	state := s.state
	s.mu.Unlock()
	if state != pkgaudio.StatePlaying {
		t.Fatalf("precondition: session not playing item-a: state=%s", state)
	}
	engine.armHang(s.engineHandleFor("item-b"))
	c.advance(200 * time.Millisecond)

	done := make(chan struct{})
	go func() {
		m.watchTick(ctx)
		close(done)
	}()

	select {
	case <-done:
	case <-time.After(wedgedWaitBound):
		t.Fatal("watchTick did not return within a bounded time despite a wedged advanceLocked Load call")
	}

	s.mu.Lock()
	defer s.mu.Unlock()
	if s.state != pkgaudio.StateFailed {
		t.Fatalf("wedged session state = %q, want Failed once its bounded Load call timed out", s.state)
	}
}

// slowSuccessfulDecoder reports a ready, decoded asset only after
// sleeping for at least delay — a stand-in for ProbeAsset's own
// hashFile, which has no context parameter and so cannot be cancelled
// early (mediaprobe.go), used here to prove that a probe slower than
// engineCallTimeout still succeeds rather than being cut off by it.
type slowSuccessfulDecoder struct {
	delay    time.Duration
	duration time.Duration
}

// Decode honours ctx, matching RealDecoder's own subprocess-based
// implementation (exec.CommandContext): a caller that bounds ctx too
// tightly gets a genuine early failure here, not a mock that silently
// ignores the deadline it was given. That is what makes
// TestAdvanceLockedDoesNotBoundProbeAsset able to tell "ProbeAsset is
// unbounded" apart from "ProbeAsset is bounded but the mock ignores it".
func (d slowSuccessfulDecoder) Decode(ctx context.Context, _ string) DecodeResult {
	select {
	case <-time.After(d.delay):
	case <-ctx.Done():
		return DecodeResult{Available: false, Reason: "context expired before decode completed: " + ctx.Err().Error()}
	}
	return DecodeResult{
		Available: true, TypeIdentified: true, MIMEType: "audio/wav", Decoded: true,
		Discoverer: DiscovererEvidence{Ran: true, Duration: d.duration},
	}
}

// TestAdvanceLockedDoesNotBoundProbeAsset proves prepareLocked's own
// bounded context wraps only the engine.Load call, never ProbeAsset: a
// probe genuinely slower than engineCallTimeout (simulating hashFile
// reading a large, show-sized asset on slow storage) must still let the
// advance succeed, not fail it with a context-deadline error the probe
// itself never earned. Reproduces the review finding that an earlier
// version of this fix wrapped prepareLocked's ENTIRE call, including
// ProbeAsset, in the same bounded context meant only for engine calls.
func TestAdvanceLockedDoesNotBoundProbeAsset(t *testing.T) {
	prevEngineCall := engineCallTimeout
	engineCallTimeout = 100 * time.Millisecond
	defer func() { engineCallTimeout = prevEngineCall }()

	c := newClock(time.Now())
	dir := t.TempDir()
	engine := NewFakeEngine(c.now)
	// The probe alone takes 3x engineCallTimeout — if prepareLocked's
	// bounded context ever wraps ProbeAsset again, this advance fails.
	slowDecoder := slowSuccessfulDecoder{delay: 3 * engineCallTimeout, duration: 50 * time.Millisecond}
	m := NewManager(engine, NewFileSessionStore(dir), dir, slowDecoder, c.now, nil)
	ctx := context.Background()
	const id = pkgaudio.SessionID("night-session")

	playlist := twoItemPlaylist(t, dir)
	m.Apply(ctx, id, "inv-apply", 1, pkgaudio.ApplyRequest{Playlist: pkgaudio.SetField(playlist)})
	m.Start(ctx, id, "inv-start", 2)

	s, ok := m.get(id)
	if !ok {
		t.Fatal("session not created")
	}
	s.mu.Lock()
	state := s.state
	s.mu.Unlock()
	if state != pkgaudio.StatePlaying {
		t.Fatalf("precondition: session not playing item-a: state=%s", state)
	}
	// Past item-a's 50ms known duration: the next watchTick observes
	// natural completion and advances into item-b, whose own probe is
	// the slow one under test.
	c.advance(200 * time.Millisecond)

	m.watchTick(ctx)

	s.mu.Lock()
	defer s.mu.Unlock()
	if s.state != pkgaudio.StatePlaying {
		t.Fatalf("advance to item-b failed (state=%s, fault=%s, reason=%q); a slow-but-successful probe must not be cut off by engineCallTimeout", s.state, s.fault, s.faultReason)
	}
	if s.currentItemID != "item-b" {
		t.Fatalf("session did not advance to item-b: currentItemID=%q", s.currentItemID)
	}
}
