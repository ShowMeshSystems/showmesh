package audio

import (
	"context"
	"sync"
	"testing"
	"time"

	pkgaudio "github.com/showmeshsystems/showmesh/pkg/audio"
)

// resumeCountingEngine wraps [FakeEngine] and counts calls to Resume and
// Start, so a test can assert SilenceAll issues neither across a whole
// call, the actual guarantee [Manager.SilenceAll] makes, instead of
// inferring it from a session's final state, which the original,
// defective code could also reach in some map-iteration orders.
type resumeCountingEngine struct {
	*FakeEngine
	mu    sync.Mutex
	calls int
}

func (e *resumeCountingEngine) reset() {
	e.mu.Lock()
	defer e.mu.Unlock()
	e.calls = 0
}

func (e *resumeCountingEngine) resumeStartCalls() int {
	e.mu.Lock()
	defer e.mu.Unlock()
	return e.calls
}

func (e *resumeCountingEngine) Resume(ctx context.Context, handle EngineHandle) (EngineObservation, error) {
	e.mu.Lock()
	e.calls++
	e.mu.Unlock()
	return e.FakeEngine.Resume(ctx, handle)
}

func (e *resumeCountingEngine) Start(ctx context.Context, handle EngineHandle, position time.Duration) (EngineObservation, error) {
	e.mu.Lock()
	e.calls++
	e.mu.Unlock()
	return e.FakeEngine.Start(ctx, handle, position)
}

// TestSilenceAllStopsEverySessionUnaddressedByRevision proves the reason
// SilenceAll exists: it reaches a session even though the
// invocation/revision it is called with (none at all) would never pass
// [pkgaudio.RevisionState.Apply] on the normal audio.session.stop path.
func TestSilenceAllStopsEverySessionUnaddressedByRevision(t *testing.T) {
	c := newClock(time.Now())
	m := newTestManager(t, c)
	ctx := context.Background()

	const s1 = pkgaudio.SessionID("s1")
	const s2 = pkgaudio.SessionID("s2")
	ref1 := writeTestAsset(t, m.assetDir, "a.wav", "asset-1", []byte("one"))
	ref2 := writeTestAsset(t, m.assetDir, "b.wav", "asset-2", []byte("two"))

	if r := m.Apply(ctx, s1, "inv-1", 1, pkgaudio.ApplyRequest{Media: pkgaudio.SetField(ref1)}); r.Outcome != pkgaudio.OutcomeUnconfirmable {
		t.Fatalf("apply s1 = %+v", r)
	}
	if r := m.Start(ctx, s1, "inv-2", 2); r.Outcome != pkgaudio.OutcomeUnconfirmable {
		t.Fatalf("start s1 = %+v", r)
	}
	if r := m.Apply(ctx, s2, "inv-1", 1, pkgaudio.ApplyRequest{Media: pkgaudio.SetField(ref2)}); r.Outcome != pkgaudio.OutcomeUnconfirmable {
		t.Fatalf("apply s2 = %+v", r)
	}
	if r := m.Start(ctx, s2, "inv-2", 2); r.Outcome != pkgaudio.OutcomeUnconfirmable {
		t.Fatalf("start s2 = %+v", r)
	}

	results := m.SilenceAll(ctx)
	if len(results) != 2 {
		t.Fatalf("SilenceAll returned %d results, want 2", len(results))
	}
	seen := map[pkgaudio.SessionID]pkgaudio.OutcomeResult{}
	for _, r := range results {
		seen[r.ID] = r.Outcome
	}
	for _, id := range []pkgaudio.SessionID{s1, s2} {
		outcome, ok := seen[id]
		if !ok {
			t.Fatalf("SilenceAll did not report %q", id)
		}
		if outcome.Outcome != pkgaudio.OutcomeUnconfirmable {
			t.Fatalf("SilenceAll(%q) outcome = %+v, want Unconfirmable (gated by the fake engine)", id, outcome)
		}
	}

	for _, id := range []pkgaudio.SessionID{s1, s2} {
		s, ok := m.get(id)
		if !ok {
			t.Fatalf("session %q missing after SilenceAll", id)
		}
		s.mu.Lock()
		state := s.state
		s.mu.Unlock()
		if state != pkgaudio.StateStopped {
			t.Fatalf("session %q state = %q, want Stopped", id, state)
		}
	}
}

// TestSilenceAllNeverResumesAnInterruptedSessionRegardlessOfOrder proves
// SilenceAll never runs a real Engine.Resume/Start on a session it is
// about to silence anyway. One announcement, ann, interrupts many bg
// targets, all sharing ann as their only interrupter. SilenceAll iterates
// a map, so a real deployment could reach ann before any given target or
// after it; the regression this guards (silencing ann before a target
// still Paused resumes that target with a genuine Engine.Resume, then
// stops it again on the target's own later turn) fires for every target
// SilenceAll had not yet reached at the moment ann's own turn came up,
// and still ends with every session Stopped either way, so asserting
// only final state (as an earlier version of this test did) cannot catch
// it. Using many targets sharing one interrupter makes it overwhelmingly
// likely, on every run, that ann is not the very last session
// SilenceAll's map iteration reaches, the one order that would produce
// zero Resume calls even under the regression, so counting Resume/Start
// calls across the whole call catches it reliably without hand-composing
// SilenceAll's own steps or forcing one specific order.
func TestSilenceAllNeverResumesAnInterruptedSessionRegardlessOfOrder(t *testing.T) {
	c := newClock(time.Now())
	dir := t.TempDir()
	engine := &resumeCountingEngine{FakeEngine: NewFakeEngine(c.now)}
	m := NewManager(engine, NewFileSessionStore(dir), dir, staticDecoder{duration: 2 * time.Second}, c.now, nil)
	ctx := context.Background()

	const targets = 24
	var bgSessions []*Session
	for i := 0; i < targets; i++ {
		bgID := pkgaudio.SessionID("bg" + string(rune('a'+i)))
		bgRef := writeTestAsset(t, m.assetDir, string(bgID)+".wav", "asset-"+string(bgID), []byte(bgID))
		startPlaying(t, m, ctx, bgID, bgRef, pkgaudio.SourceRoleShow, pkgaudio.MixPolicyMix)
		bg, ok := m.get(bgID)
		if !ok {
			t.Fatalf("%s session not created", bgID)
		}
		bgSessions = append(bgSessions, bg)
	}

	annRef := writeTestAsset(t, m.assetDir, "ann.wav", "asset-ann", []byte("ann"))
	startPlaying(t, m, ctx, "ann", annRef, pkgaudio.SourceRoleAnnouncement, pkgaudio.MixPolicyInterrupt)

	for _, bg := range bgSessions {
		bg.mu.Lock()
		state := bg.state
		bg.mu.Unlock()
		if state != pkgaudio.StatePaused {
			t.Fatalf("precondition: %s state = %q, want paused (interrupted by ann)", bg.id, state)
		}
	}

	engine.reset()

	if results := m.SilenceAll(ctx); len(results) != targets+1 {
		t.Fatalf("SilenceAll returned %d results, want %d", len(results), targets+1)
	}

	if n := engine.resumeStartCalls(); n != 0 {
		t.Fatalf("SilenceAll made %d Resume/Start engine call(s); want 0. It must never restore a session it is about to silence", n)
	}

	for _, bg := range bgSessions {
		bg.mu.Lock()
		state := bg.state
		bg.mu.Unlock()
		if state != pkgaudio.StateStopped {
			t.Fatalf("%s state after the full SilenceAll = %q, want Stopped", bg.id, state)
		}
	}
}

// TestSilenceAllBoundsAWedgedSessionAndStillSilencesTheRest proves the
// blast radius a bounded engine call closes: SilenceAll's own loop is
// serial, so one session whose Engine.Stop wedges under an unbounded
// context would otherwise stop every session behind it from ever being
// silenced, and stop SilenceAll from ever returning a result at all.
func TestSilenceAllBoundsAWedgedSessionAndStillSilencesTheRest(t *testing.T) {
	withShrunkEngineCallTimeout(t, 200*time.Millisecond)

	c := newClock(time.Now())
	dir := t.TempDir()
	engine := newHangingCallEngine(c.now)
	m := NewManager(engine, NewFileSessionStore(dir), dir, staticDecoder{duration: 2 * time.Second}, c.now, nil)
	ctx := context.Background()

	wedgedRef := writeTestAsset(t, m.assetDir, "wedged.wav", "asset-wedged", []byte("wedged"))
	startPlaying(t, m, ctx, "wedged", wedgedRef, pkgaudio.SourceRoleShow, pkgaudio.MixPolicyMix)
	otherRef := writeTestAsset(t, m.assetDir, "other.wav", "asset-other", []byte("other"))
	startPlaying(t, m, ctx, "other", otherRef, pkgaudio.SourceRoleShow, pkgaudio.MixPolicyMix)

	wedged, ok := m.get("wedged")
	if !ok {
		t.Fatal("wedged session not created")
	}
	wedged.mu.Lock()
	handle := wedged.handle
	wedged.mu.Unlock()
	engine.arm(hangStop, handle)

	done := make(chan []SessionSilenceOutcome, 1)
	go func() { done <- m.SilenceAll(ctx) }()

	var results []SessionSilenceOutcome
	select {
	case results = <-done:
	case <-time.After(callBoundsWaitBound):
		t.Fatal("SilenceAll did not return within a bounded time despite a wedged Engine.Stop call")
	}

	if len(results) != 2 {
		t.Fatalf("SilenceAll returned %d results, want 2", len(results))
	}
	seen := map[pkgaudio.SessionID]pkgaudio.OutcomeResult{}
	for _, r := range results {
		seen[r.ID] = r.Outcome
	}
	if outcome, ok := seen["wedged"]; !ok || outcome.Outcome != pkgaudio.OutcomeUnconfirmable {
		t.Fatalf(`SilenceAll("wedged") = %+v, want Unconfirmable`, outcome)
	}
	if outcome, ok := seen["other"]; !ok || outcome.Outcome != pkgaudio.OutcomeUnconfirmable {
		t.Fatalf(`SilenceAll("other") = %+v, want Unconfirmable (gated by the fake engine, but reached and executed)`, outcome)
	}

	other, ok := m.get("other")
	if !ok {
		t.Fatal("other session missing after SilenceAll")
	}
	other.mu.Lock()
	state := other.state
	other.mu.Unlock()
	if state != pkgaudio.StateStopped {
		t.Fatalf(`session "other" state = %q after SilenceAll, want Stopped despite the wedged sibling`, state)
	}
}

// TestSilenceAllIsIdempotent proves silencing an already-silent node
// succeeds rather than erroring: a session with no engine handle loaded
// (never started, or already stopped) still reports the same outcome
// twice, never an error the second time.
func TestSilenceAllIsIdempotent(t *testing.T) {
	c := newClock(time.Now())
	m := newTestManager(t, c)
	ctx := context.Background()

	if results := m.SilenceAll(ctx); len(results) != 0 {
		t.Fatalf("SilenceAll on an empty node returned %d results, want 0", len(results))
	}

	const id = pkgaudio.SessionID("s1")
	ref := writeTestAsset(t, m.assetDir, "a.wav", "asset-1", []byte("x"))
	if r := m.Apply(ctx, id, "inv-1", 1, pkgaudio.ApplyRequest{Media: pkgaudio.SetField(ref)}); r.Outcome != pkgaudio.OutcomeUnconfirmable {
		t.Fatalf("apply = %+v", r)
	}

	first := m.SilenceAll(ctx)
	if len(first) != 1 || first[0].Outcome.Outcome != pkgaudio.OutcomeUnconfirmable {
		t.Fatalf("first SilenceAll = %+v, want one Unconfirmable outcome", first)
	}

	second := m.SilenceAll(ctx)
	if len(second) != 1 || second[0].Outcome.Outcome != pkgaudio.OutcomeUnconfirmable {
		t.Fatalf("second SilenceAll on an already-silent session = %+v, want Unconfirmable again, not an error", second)
	}
}

// TestSilenceAllLeavesRevisionLedgerUsableForTheNextCommand proves that
// a safety command bypassing the anti-rewind ledger does not leave that
// ledger unable to accept the next genuine per-session command.
// SilenceAll never advances or resets s.revState,
// so a subsequent Apply at the session's next real revision must be
// accepted exactly as if SilenceAll had never run.
func TestSilenceAllLeavesRevisionLedgerUsableForTheNextCommand(t *testing.T) {
	c := newClock(time.Now())
	m := newTestManager(t, c)
	ctx := context.Background()

	const id = pkgaudio.SessionID("s1")
	ref := writeTestAsset(t, m.assetDir, "a.wav", "asset-1", []byte("x"))
	if r := m.Apply(ctx, id, "inv-1", 1, pkgaudio.ApplyRequest{Media: pkgaudio.SetField(ref)}); r.Outcome != pkgaudio.OutcomeUnconfirmable {
		t.Fatalf("apply = %+v", r)
	}
	if r := m.Start(ctx, id, "inv-2", 2); r.Outcome != pkgaudio.OutcomeUnconfirmable {
		t.Fatalf("start = %+v", r)
	}

	s, ok := m.get(id)
	if !ok {
		t.Fatal("session missing before SilenceAll")
	}
	before := s.revState.Current()

	if results := m.SilenceAll(ctx); len(results) != 1 || results[0].Outcome.Outcome != pkgaudio.OutcomeUnconfirmable {
		t.Fatalf("SilenceAll = %+v, want one Unconfirmable outcome", results)
	}

	after := s.revState.Current()
	if after != before {
		t.Fatalf("SilenceAll changed the session's current revision from %d to %d; it must never touch the ledger", before, after)
	}

	// A real, later revision (3) must still be accepted, and one that
	// would have been stale before SilenceAll ran (<=2) must still be
	// refused as stale: the ledger is exactly as it would be if
	// SilenceAll had never run.
	if r := m.Apply(ctx, id, "inv-3", 2, pkgaudio.ApplyRequest{Media: pkgaudio.SetField(ref)}); r.Outcome != pkgaudio.OutcomeRefused || r.Reason != pkgaudio.ReasonStaleRevision {
		t.Fatalf("apply at revision 2 after SilenceAll = %+v, want a stale-revision refusal", r)
	}
	if r := m.Apply(ctx, id, "inv-4", 3, pkgaudio.ApplyRequest{Media: pkgaudio.SetField(ref)}); r.Outcome != pkgaudio.OutcomeUnconfirmable {
		t.Fatalf("apply at revision 3 after SilenceAll = %+v, want an accepted (Unconfirmable, gated by the fake engine) outcome", r)
	}
}

// TestSilenceAllReportsFailedWhenPersistFails proves that a session whose
// state was genuinely silenced but could not be durably persisted
// reports Failed, not the stop's own optimistic outcome, via
// [Session.persistOrFailLocked]. This matters beyond the general
// dispatch-path rule ([TestDispatchReportsFailureWhenPersistFails]):
// [Manager.retryDeferredRestores] re-reads a session's record from the
// store for any session left in pendingEngineRestore, so a best-effort
// persist here could let a later audio.node binding restart audio the
// operator believed this emergency stop had already silenced.
func TestSilenceAllReportsFailedWhenPersistFails(t *testing.T) {
	c := newClock(time.Now())
	dir := t.TempDir()
	store := &failingSessionStore{SessionStore: NewFileSessionStore(dir)}
	m := NewManager(NewFakeEngine(c.now), store, dir, staticDecoder{duration: 2 * time.Second}, c.now, nil)
	ctx := context.Background()

	const id = pkgaudio.SessionID("s1")
	ref := writeTestAsset(t, m.assetDir, "a.wav", "asset-1", []byte("x"))
	if r := m.Apply(ctx, id, "inv-1", 1, pkgaudio.ApplyRequest{Media: pkgaudio.SetField(ref)}); r.Outcome != pkgaudio.OutcomeUnconfirmable {
		t.Fatalf("apply = %+v", r)
	}
	if r := m.Start(ctx, id, "inv-2", 2); r.Outcome != pkgaudio.OutcomeUnconfirmable {
		t.Fatalf("start = %+v", r)
	}

	store.armSaveFailures(1, nil)
	results := m.SilenceAll(ctx)
	if len(results) != 1 {
		t.Fatalf("SilenceAll returned %d results, want 1", len(results))
	}
	if results[0].Outcome.Outcome != pkgaudio.OutcomeFailed {
		t.Fatalf("SilenceAll(%q) outcome = %+v, want Failed when persist fails", id, results[0].Outcome)
	}
	if results[0].Outcome.Reason == "" {
		t.Fatal("a persist-failure outcome must carry a reason")
	}
}
