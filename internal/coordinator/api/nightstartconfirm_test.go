package api

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/showmeshsystems/showmesh/internal/coordinator/store"
)

func nightStartConfirmHandlers(t *testing.T, now time.Time) (*handlers, *store.Store) {
	t.Helper()
	svc, st, _ := newTestIdentityServiceWithStore(t, func() time.Time { return now })
	deps := Dependencies{
		NightSessions: st, Observations: &fakeObservationLister{}, Identity: svc, Config: st,
		FPP: &fakeFPPLister{views: []FPPInstanceView{{InstanceID: "player-01", Endpoint: "http://127.0.0.1:1"}}},
	}.withDefaults()
	return &handlers{deps: deps, clock: func() time.Time { return now }, logger: testLogger()}, st
}

func committedShowSession(t *testing.T, st *store.Store, now, dispatchedAt time.Time) store.NightSessionRecord {
	t.Helper()
	anchor := nightContentAnchor{
		Purpose: nightAnchorPurposeShow, FPPInstanceID: "player-01", Playlist: "halloween-show",
		DispatchedAt: dispatchedAt,
	}
	rec := store.NightSessionRecord{
		ID: "sess-1", ConfigObjectID: "halloween-main", ConfigRevision: 1,
		State: nightStateTransitionToShow, StateEnteredAt: dispatchedAt, Cycle: 8, ShowCommitted: true,
		ContentAnchorJSON: encodeNightContentAnchor(anchor),
		Issuer:            store.NightSessionIssuer{PrincipalID: "p-1", PrincipalName: "operator-1"},
	}
	if err := st.CreateNightSession(context.Background(), rec, now); err != nil {
		t.Fatalf("create night session: %v", err)
	}
	return rec
}

// A show start FPP accepted but never reported playing used to hold the
// session in transition-to-show forever, and every later night with it.
func TestNightEnsureAnchor_StartNeverConfirmedDegradesAfterTheWindow(t *testing.T) {
	now := time.Date(2026, 10, 31, 22, 0, 0, 0, time.UTC)
	h, st := nightStartConfirmHandlers(t, now)
	rec := committedShowSession(t, st, now, now.Add(-nightStartConfirmWindow-time.Second))

	_, ready, _ := h.nightEnsureAnchor(context.Background(), now, rec, nightAnchorPurposeShow, "player-01", "halloween-show", false, 0, fppIfBusyRefuse)

	if ready {
		t.Fatal("an unconfirmed start reported ready")
	}
	got := mustGetCurrentSession(t, st)
	if !got.Degraded {
		t.Fatalf("a start unconfirmed past the window did not degrade the session: %+v", got)
	}
	if !strings.Contains(got.DegradedReason, `"halloween-show"`) {
		t.Fatalf("degradedReason = %q, want it to name the playlist", got.DegradedReason)
	}
}

func TestNightEnsureAnchor_StartStillInsideTheWindowKeepsWaiting(t *testing.T) {
	now := time.Date(2026, 10, 31, 22, 0, 0, 0, time.UTC)
	h, st := nightStartConfirmHandlers(t, now)
	rec := committedShowSession(t, st, now, now.Add(-nightStartConfirmWindow/2))

	h.nightEnsureAnchor(context.Background(), now, rec, nightAnchorPurposeShow, "player-01", "halloween-show", false, 0, fppIfBusyRefuse)

	if got := mustGetCurrentSession(t, st); got.Degraded {
		t.Fatalf("a start still inside the confirmation window degraded the session: %q", got.DegradedReason)
	}
}

// The scheduled fade-out must close a night whose show never confirmed,
// instead of deferring to a show that cannot finish.
func TestApplyNightShutdownEffect_UnconfirmedShowStartDoesNotDefer(t *testing.T) {
	now := time.Date(2026, 10, 31, 22, 0, 0, 0, time.UTC)
	rec := store.NightSessionRecord{
		ID: "night-1", State: nightStateTransitionToShow, ShowCommitted: true,
		ContentAnchorJSON: encodeNightContentAnchor(nightContentAnchor{
			Purpose: nightAnchorPurposeShow, Playlist: "halloween-show", DispatchedAt: now.Add(-time.Hour),
		}),
	}

	next, changed := applyNightShutdownEffect(now, rec, "fade-out", nightShutdownOrdinary)

	if !changed || next.State != nightStateFadingOut {
		t.Fatalf("fade-out on an unconfirmed show start left state %q (changed=%v), want %q", next.State, changed, nightStateFadingOut)
	}
}

func TestApplyNightShutdownEffect_CommittedShowAwaitingConfirmationStillDefers(t *testing.T) {
	now := time.Date(2026, 10, 31, 22, 0, 0, 0, time.UTC)
	rec := store.NightSessionRecord{
		ID: "night-1", State: nightStateTransitionToShow, ShowCommitted: true,
		ContentAnchorJSON: encodeNightContentAnchor(nightContentAnchor{
			Purpose: nightAnchorPurposeShow, Playlist: "halloween-show", DispatchedAt: now.Add(-5 * time.Second),
		}),
	}

	next, _ := applyNightShutdownEffect(now, rec, "fade-out", nightShutdownOrdinary)

	if next.State != nightStateTransitionToShow || !next.FinalShowRequested {
		t.Fatalf("fade-out during a normal launch left state %q finalShowRequested=%v, want it deferred", next.State, next.FinalShowRequested)
	}
}
