package api

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/showmeshsystems/showmesh/internal/coordinator/config"
	"github.com/showmeshsystems/showmesh/internal/coordinator/store"
)

// Track F seam F7a: mapNightCue/mapNightCues unit coverage. SM-98's own
// gap was "nothing can read the outbox at all"; these defend that a cue
// with no outbox row yet is told apart from one that ran, and that a
// resolved row's own state/outcome/reason/timestamps/revision reach the
// wire unchanged.

// TestMapNightCue_NoRowIsNotDispatched: a cue the current cycle has not
// reached yet must never be reported with a pending/failed/any
// dispatch-implying state.
func TestMapNightCue_NoRowIsNotDispatched(t *testing.T) {
	cue := config.NightSessionCue{Name: "lighting-fade", Role: "lighting", Action: "lighting-fade-out"}
	got := mapNightCue(nightPhaseEnterShow, cue, store.NightCueOutboxRecord{}, false)
	if got.State != nightCueStateNotDispatched {
		t.Fatalf("State = %q, want %q", got.State, nightCueStateNotDispatched)
	}
	if got.Name != "lighting-fade" || got.Phase != nightPhaseEnterShow || got.Role != "lighting" || got.Action != "lighting-fade-out" {
		t.Fatalf("cue identity not carried through: %#v", got)
	}
	if got.ActionRevision != nil {
		t.Fatalf("ActionRevision = %v, want nil (nothing pinned yet)", got.ActionRevision)
	}
	if got.Outcome != "" {
		t.Fatalf("Outcome = %q, want empty (nothing resolved yet)", got.Outcome)
	}
	if got.DispatchedAt != nil || got.ResolvedAt != nil {
		t.Fatalf("timestamps = dispatchedAt=%v resolvedAt=%v, want both nil", got.DispatchedAt, got.ResolvedAt)
	}
	if got.Reason == "" {
		t.Fatal("expected a stated reason for the not-dispatched state, never omitted (ADR-020)")
	}
}

// TestMapNightCue_ResolvedRowCarriesEvidenceThrough: once a row exists,
// its state, outcome, reason, pinned revision, and both timestamps reach
// the wire type verbatim — never resummarized into one collapsed field.
func TestMapNightCue_ResolvedRowCarriesEvidenceThrough(t *testing.T) {
	cue := config.NightSessionCue{Name: "lighting-fade", Role: "lighting", Action: "lighting-fade-out"}
	dispatched := testNow
	resolved := testNow.Add(time.Second)
	row := store.NightCueOutboxRecord{
		ActionRevision: 3, State: nightCueStateResolved, Outcome: nightCueOutcomeUnconfirmed,
		OutcomeReason: "no confirming evidence arrived", DispatchedAt: &dispatched, ResolvedAt: &resolved,
	}
	got := mapNightCue(nightPhaseEnterShow, cue, row, true)
	if got.State != nightCueStateResolved {
		t.Fatalf("State = %q, want %q", got.State, nightCueStateResolved)
	}
	if got.Outcome != nightCueOutcomeUnconfirmed {
		t.Fatalf("Outcome = %q, want %q", got.Outcome, nightCueOutcomeUnconfirmed)
	}
	if got.Reason != "no confirming evidence arrived" {
		t.Fatalf("Reason = %q, want the row's own outcome reason", got.Reason)
	}
	if got.ActionRevision == nil || *got.ActionRevision != 3 {
		t.Fatalf("ActionRevision = %v, want 3", got.ActionRevision)
	}
	if got.DispatchedAt == nil || got.ResolvedAt == nil {
		t.Fatalf("timestamps = dispatchedAt=%v resolvedAt=%v, want both present", got.DispatchedAt, got.ResolvedAt)
	}
}

// TestMapNightCues_JoinsOutboxRowsByPhaseAndName defends the (phase,
// cueName) join key: two phases may legitimately share a cue name, and a
// row for one must never answer for the other.
func TestMapNightCues_JoinsOutboxRowsByPhaseAndName(t *testing.T) {
	sameName := config.NightSessionCue{Name: "shared", Role: "lighting", Action: "a"}
	dispatched := testNow
	byKey := map[string]store.NightCueOutboxRecord{
		nightPhaseEnterShow + "\x00shared": {State: nightCueStateResolved, Outcome: nightCueOutcomeConfirmed, DispatchedAt: &dispatched},
	}
	out := appendMappedNightCues(nil, nightPhaseEnterShow, []config.NightSessionCue{sameName}, byKey)
	out = appendMappedNightCues(out, nightPhaseEnterResting, []config.NightSessionCue{sameName}, byKey)
	if len(out) != 2 {
		t.Fatalf("len(out) = %d, want 2", len(out))
	}
	if out[0].State != nightCueStateResolved {
		t.Fatalf("enterShow cue state = %q, want %q", out[0].State, nightCueStateResolved)
	}
	if out[1].State != nightCueStateNotDispatched {
		t.Fatalf("enterResting cue (no row of its own) state = %q, want %q", out[1].State, nightCueStateNotDispatched)
	}
}

// nightCueOutboxReadFailureStore wraps a real *store.Store and fails only
// ListNightCueOutboxRows, to prove a store I/O error cannot manufacture a
// false "not_dispatched" claim about any individual cue.
type nightCueOutboxReadFailureStore struct {
	*store.Store
}

func (nightCueOutboxReadFailureStore) ListNightCueOutboxRows(context.Context, string, int64) ([]store.NightCueOutboxRecord, error) {
	return nil, errors.New("simulated outbox read failure")
}

// TestMapNightCues_OutboxReadFailureNeverClaimsNotDispatched: an outbox
// read failure must report NightCues itself as unreadable rather than
// letting every cue individually assert it was never dispatched — a cue
// that is actually ambiguous must never render as "not_dispatched", which
// would invite an operator to re-run a dispatch that may have already
// reached the device.
func TestMapNightCues_OutboxReadFailureNeverClaimsNotDispatched(t *testing.T) {
	api, st, token := setupNightSessionFixture(t)
	mustPutNightSession(t, api, token, "halloween-main", validNightSessionBody)

	deps := Dependencies{Config: st, NightSessions: nightCueOutboxReadFailureStore{st}}.withDefaults()
	rec := store.NightSessionRecord{ID: "s1", ConfigObjectID: "halloween-main", ConfigRevision: 1}

	got := mapNightCues(context.Background(), deps, rec)
	if got.State == "recorded" {
		t.Fatalf("State = %q, want anything but recorded when the outbox could not be read", got.State)
	}
	if got.Reason == "" {
		t.Fatal("expected a stated reason for the unreadable outbox, never omitted (ADR-020)")
	}
	for _, cue := range got.Cues {
		if cue.State == nightCueStateNotDispatched {
			t.Fatalf("cue %q reports %q under a read failure; must report nothing rather than a false claim", cue.Name, nightCueStateNotDispatched)
		}
	}
}
