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
