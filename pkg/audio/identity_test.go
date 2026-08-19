package audio

import (
	"fmt"
	"testing"
)

func TestRevisionStateAcceptsGreaterRevision(t *testing.T) {
	s := NewRevisionState("sess-1")
	d := s.Apply("inv-1", 5)
	if !d.Accepted {
		t.Fatalf("Apply(5) on fresh state: got Accepted=false, want true")
	}
	if d.Revision != 5 {
		t.Fatalf("Apply(5): got Revision=%d, want 5", d.Revision)
	}
	if d.Result != nil {
		t.Fatalf("Apply(5): got Result=%+v, want nil", d.Result)
	}
	if s.Current() != 5 {
		t.Fatalf("Current() = %d, want 5", s.Current())
	}
}

func TestRevisionStateRefusesStale(t *testing.T) {
	s := NewRevisionState("sess-1")
	s.Apply("inv-1", 5)

	for _, requested := range []Revision{5, 4, 0} {
		d := s.Apply(InvocationID(fmt.Sprintf("inv-stale-%d", requested)), requested)
		if d.Accepted {
			t.Fatalf("Apply(%d) after current=5: got Accepted=true, want false", requested)
		}
		if d.Result == nil || d.Result.Outcome != OutcomeRefused {
			t.Fatalf("Apply(%d): got Result=%+v, want Refused", requested, d.Result)
		}
		if d.Result.Reason != ReasonStaleRevision {
			t.Fatalf("Apply(%d): got Reason=%q, want %q", requested, d.Result.Reason, ReasonStaleRevision)
		}
		if d.Revision != 5 {
			t.Fatalf("Apply(%d): got Revision=%d, want unchanged 5", requested, d.Revision)
		}
	}
	if s.Current() != 5 {
		t.Fatalf("Current() after refused applies = %d, want unchanged 5", s.Current())
	}
}

func TestRevisionStateReplayIsIdempotent(t *testing.T) {
	s := NewRevisionState("sess-1")
	first := s.Apply("inv-1", 5)
	if !first.Accepted {
		t.Fatalf("first Apply: got Accepted=false, want true")
	}

	replay := s.Apply("inv-1", 5)
	if replay != first {
		t.Fatalf("replay of inv-1 = %+v, want identical to original %+v", replay, first)
	}
	if s.Current() != 5 {
		t.Fatalf("Current() after replay = %d, want unchanged 5 (no second effect)", s.Current())
	}
}

// TestRevisionStateReplayOfRefusalIsIdempotent proves a refused decision
// is answered from its memo rather than re-evaluated: it advances
// current between the original refusal and the replay, so a
// re-evaluating implementation would report the refusal's Revision as
// the NEW current (10) instead of the current at the time of the
// original refusal (5) — a byte-for-byte difference a re-evaluation
// cannot avoid, unlike replaying an unchanged refusal.
func TestRevisionStateReplayOfRefusalIsIdempotent(t *testing.T) {
	s := NewRevisionState("sess-1")
	s.Apply("inv-1", 5)

	original := s.Apply("inv-2", 3)
	if original.Accepted || original.Revision != 5 {
		t.Fatalf("original refusal = %+v, want Accepted=false, Revision=5", original)
	}

	s.Apply("inv-3", 10)

	replay := s.Apply("inv-2", 3)
	if replay != original {
		t.Fatalf("replay of refused inv-2 = %+v, want identical to original %+v", replay, original)
	}
}

func TestRevisionStateReplayWithMismatchedRevisionRefused(t *testing.T) {
	s := NewRevisionState("sess-1")
	first := s.Apply("inv-1", 5)
	if !first.Accepted {
		t.Fatalf("first Apply: got Accepted=false, want true")
	}

	mismatched := s.Apply("inv-1", 9)
	if mismatched.Accepted {
		t.Fatalf("replay with mismatched revision: got Accepted=true, want false")
	}
	if mismatched.Result == nil || mismatched.Result.Reason != ReasonInvocationRevisionMismatch {
		t.Fatalf("replay with mismatched revision: got Result=%+v, want reason %q", mismatched.Result, ReasonInvocationRevisionMismatch)
	}
	if s.Current() != 5 {
		t.Fatalf("Current() after mismatched replay = %d, want unchanged 5", s.Current())
	}
}

func TestRevisionStateEmptyInvocationRefusedAndNotRecorded(t *testing.T) {
	s := NewRevisionState("sess-1")

	first := s.Apply("", 5)
	if first.Accepted {
		t.Fatalf("Apply(empty invocation): got Accepted=true, want false")
	}
	if first.Result == nil || first.Result.Reason != ReasonInvalidInvocation {
		t.Fatalf("Apply(empty invocation): got Result=%+v, want reason %q", first.Result, ReasonInvalidInvocation)
	}

	// A second empty-invocation command for a different revision must be
	// judged independently, not answered from the first one's memo.
	second := s.Apply("", 1)
	if second.Result == nil || second.Result.Reason != ReasonInvalidInvocation {
		t.Fatalf("second Apply(empty invocation): got Result=%+v, want reason %q", second.Result, ReasonInvalidInvocation)
	}

	if _, recorded := s.Decisions()[""]; recorded {
		t.Fatal("empty invocation was recorded in Decisions(), want not recorded")
	}
}

func TestRestoredRevisionStateRefusesDelayedLowerRevision(t *testing.T) {
	s := RestoreRevisionState("sess-1", 40, nil)
	d := s.Apply("inv-late", 12)
	if d.Accepted {
		t.Fatal("Apply(12) on restored state at current=40: got Accepted=true, want false")
	}
	if d.Result == nil || d.Result.Reason != ReasonStaleRevision {
		t.Fatalf("Apply(12): got Result=%+v, want reason %q", d.Result, ReasonStaleRevision)
	}
	if s.Current() != 40 {
		t.Fatalf("Current() = %d, want unchanged 40", s.Current())
	}
}

func TestRestoreRevisionStateCopiesDefensively(t *testing.T) {
	prior := map[InvocationID]RevisionDecision{
		"inv-1": {Requested: 5, Accepted: true, Revision: 5},
	}
	s := RestoreRevisionState("sess-1", 5, prior)

	// Mutating the caller's map after Restore must not affect s.
	prior["inv-2"] = RevisionDecision{Requested: 6, Accepted: true, Revision: 6}
	if _, leaked := s.Decisions()["inv-2"]; leaked {
		t.Fatal("mutating the input map after RestoreRevisionState leaked into the state")
	}

	// Mutating the map returned by Decisions() must not affect s.
	got := s.Decisions()
	got["inv-3"] = RevisionDecision{Requested: 7, Accepted: true, Revision: 7}
	if _, leaked := s.Decisions()["inv-3"]; leaked {
		t.Fatal("mutating the map returned by Decisions() leaked into the state")
	}
}

func TestRevisionStateConcurrentApply(t *testing.T) {
	s := NewRevisionState("sess-1")
	const n = 50
	done := make(chan RevisionDecision, n)
	for i := 0; i < n; i++ {
		go func(i int) {
			done <- s.Apply(InvocationID(fmt.Sprintf("inv-%d", i)), Revision(i+1))
		}(i)
	}

	highestAccepted := Revision(0)
	for i := 0; i < n; i++ {
		d := <-done
		if d.Accepted && d.Revision > highestAccepted {
			highestAccepted = d.Revision
		}
	}

	if s.Current() != highestAccepted {
		t.Fatalf("Current() = %d, want highest accepted revision %d", s.Current(), highestAccepted)
	}
	if len(s.Decisions()) != n {
		t.Fatalf("Decisions() has %d entries, want exactly one per invocation id (%d)", len(s.Decisions()), n)
	}
}
