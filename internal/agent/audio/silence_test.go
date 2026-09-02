package audio

import (
	"context"
	"testing"
	"time"

	pkgaudio "github.com/showmeshsystems/showmesh/pkg/audio"
)

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
// about to silence anyway. bg is Paused, interrupted by ann. SilenceAll
// iterates a map, so a real deployment could reach ann before bg; this
// test forces exactly that worst-case order by running ann's own
// silence step alone first and checking bg has not moved before letting
// a full SilenceAll finish the job. Before the fix, silencing ann first
// called restoreInterrupted(ann), which ran a genuine Engine.Resume on
// bg and set it Playing, audible, before bg's own turn ever arrived.
func TestSilenceAllNeverResumesAnInterruptedSessionRegardlessOfOrder(t *testing.T) {
	c := newClock(time.Now())
	m := newTestManager(t, c)
	ctx := context.Background()

	bgRef := writeTestAsset(t, m.assetDir, "bg.wav", "asset-bg", []byte("bg"))
	startPlaying(t, m, ctx, "bg", bgRef, pkgaudio.SourceRoleShow, pkgaudio.MixPolicyMix)

	annRef := writeTestAsset(t, m.assetDir, "ann.wav", "asset-ann", []byte("ann"))
	startPlaying(t, m, ctx, "ann", annRef, pkgaudio.SourceRoleAnnouncement, pkgaudio.MixPolicyInterrupt)

	bg, ok := m.get("bg")
	if !ok {
		t.Fatal("bg session not created")
	}
	bg.mu.Lock()
	state := bg.state
	bg.mu.Unlock()
	if state != pkgaudio.StatePaused {
		t.Fatalf("precondition: bg state = %q, want paused (interrupted by ann)", state)
	}

	ann, ok := m.get("ann")
	if !ok {
		t.Fatal("ann session not created")
	}
	ann.mu.Lock()
	outcome := m.stopExecLocked(ctx, ann, boundedEngineCallContext)
	outcome = ann.persistOrFailLocked(outcome)
	ann.mu.Unlock()
	if outcome.Outcome != pkgaudio.OutcomeUnconfirmable {
		t.Fatalf("silencing ann alone = %+v, want Unconfirmable (gated by the fake engine)", outcome)
	}
	m.dropHoldMembershipEverywhere(ann.id)

	bg.mu.Lock()
	stateAfterAnnSilenced := bg.state
	bg.mu.Unlock()
	if stateAfterAnnSilenced != pkgaudio.StatePaused {
		t.Fatalf("bg state right after ann alone was silenced = %q, want still Paused: SilenceAll must never resume a session it is about to silence itself", stateAfterAnnSilenced)
	}

	if results := m.SilenceAll(ctx); len(results) != 2 {
		t.Fatalf("SilenceAll returned %d results, want 2", len(results))
	}

	bg.mu.Lock()
	defer bg.mu.Unlock()
	if bg.state != pkgaudio.StateStopped {
		t.Fatalf("bg state after the full SilenceAll = %q, want Stopped", bg.state)
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
