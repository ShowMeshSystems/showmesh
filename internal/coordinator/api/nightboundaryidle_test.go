package api

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	v1 "github.com/showmeshsystems/showmesh/internal/coordinator/api/v1"
	"github.com/showmeshsystems/showmesh/internal/coordinator/config"
	"github.com/showmeshsystems/showmesh/internal/coordinator/store"
	"github.com/showmeshsystems/showmesh/pkg/observation"
)

// An armed boundary is derived from playback. When FPP goes idle before
// that playback was due to end, the boundary is describing something that
// no longer exists and must never survive to launch a show.

func idleObservation(instanceID string, at time.Time) []observation.Observation {
	return []observation.Observation{
		statusObservation(instanceID, fppStatusValueIdle, at),
		playlistNameObservation(instanceID, "", at),
	}
}

// The real FPP idle shape, against a one-shot resting anchor, at several
// distances from the expected end.
func TestNightBoundaryContradicted_IdleBeforeTheBoundary(t *testing.T) {
	anchor := oneShotAnchor() // observed 20:00:01 at 5s into a 60s item, so E is 20:00:56.
	expected := *deriveNightBoundary(anchor).ExpectedAt

	cases := []struct {
		name string
		now  time.Time
		want bool
	}{
		{"materially early", expected.Add(-30 * time.Second), true},
		{"just outside the tolerance", expected.Add(-nightBoundaryCompletionTolerance - time.Second), true},
		{"inside the tolerance", expected.Add(-nightBoundaryCompletionTolerance + time.Second), false},
		{"exactly at the boundary", expected, false},
		{"after the boundary", expected.Add(20 * time.Second), false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			obs := nightPlaybackObservation{Current: true, Status: fppStatusValueIdle, PlaylistCurrent: true}
			bad, reason := nightBoundaryContradicted(anchor, obs, tc.now)
			if bad != tc.want {
				t.Fatalf("contradicted = %v, want %v (reason %q)", bad, tc.want, reason)
			}
			if bad && reason == "" {
				t.Fatal("a contradiction carries no reason")
			}
		})
	}
}

// A repeating resting playlist that has stopped is always a
// contradiction: it has no expected end to be near.
func TestNightBoundaryContradicted_IdleUnderRepeatIsAlwaysAContradiction(t *testing.T) {
	anchor := oneShotAnchor()
	anchor.Purpose = nightAnchorPurposeRestingRepeat
	obs := nightPlaybackObservation{Current: true, Status: fppStatusValueIdle, PlaylistCurrent: true}

	bad, reason := nightBoundaryContradicted(anchor, obs, anchor.ObservedAt.Add(time.Hour))
	if !bad {
		t.Fatal("a stopped repeating playlist was not treated as a contradiction")
	}
	if !strings.Contains(reason, "should still be running") {
		t.Fatalf("reason = %q, want it to say the repeat should still be running", reason)
	}
}

// A show reaching idle is completion, which the live tick owns; it must
// never be reported as a contradicted boundary.
func TestNightBoundaryContradicted_IdleUnderAShowAnchorIsCompletion(t *testing.T) {
	anchor := oneShotAnchor()
	anchor.Purpose = nightAnchorPurposeShow
	obs := nightPlaybackObservation{Current: true, Status: fppStatusValueIdle, PlaylistCurrent: true}

	if bad, reason := nightBoundaryContradicted(anchor, obs, anchor.ObservedAt); bad {
		t.Fatalf("a finished show was treated as a contradiction: %q", reason)
	}
}

// Stop Now during resting: the operator stops the resting playlist well
// before its end, and the armed boundary must not survive to launch the
// show when its time arrives. This drives three ticks.
func TestNightAdvanceRestingIntershow_StopNowNeverLaunchesFromTheStaleBoundary(t *testing.T) {
	dispatchedAt := time.Date(2026, 10, 31, 22, 0, 0, 0, time.UTC)
	now := dispatchedAt.Add(10 * time.Second)

	var starts int
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body struct {
			Command string `json:"command"`
		}
		_ = json.NewDecoder(r.Body).Decode(&body)
		if body.Command == "Start Playlist" {
			starts++
		}
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("Playlist Starting"))
	}))
	t.Cleanup(srv.Close)

	obs := &mutableObservationLister{}
	svc, st, _ := newTestIdentityServiceWithStore(t, func() time.Time { return now })
	deps := Dependencies{
		NightSessions: st, Observations: obs, Identity: svc, Config: st,
		FPP: &fakeFPPLister{views: []FPPInstanceView{{InstanceID: "player-01", Endpoint: srv.URL}}},
	}.withDefaults()
	h := &handlers{
		deps: deps, clock: func() time.Time { return now }, logger: testLogger(),
		fppCommandConfirmDeadline: 50 * time.Millisecond, fppCommandPollInterval: 10 * time.Millisecond,
	}

	anchor := nightContentAnchor{
		Purpose: nightAnchorPurposeRestingOneShot, FPPInstanceID: "player-01", Playlist: "halloween-resting",
		Item: "halloween-resting.fseq", DurationMS: 300000, PositionMS: 0, PositionMSKnown: true,
		DispatchedAt: dispatchedAt, ObservedAt: dispatchedAt,
	}
	expectedE := *deriveNightBoundary(anchor).ExpectedAt
	rec := store.NightSessionRecord{
		ID: "sess-1", ConfigObjectID: "halloween-main", ConfigRevision: 1,
		State: nightStateRestingIntershow, StateEnteredAt: dispatchedAt, Cycle: 1,
		ContentAnchorJSON: encodeNightContentAnchor(anchor),
		BoundaryJSON:      encodeNightBoundary(deriveNightBoundary(anchor)),
		Issuer:            store.NightSessionIssuer{PrincipalID: "p-1", PrincipalName: "operator-1"},
	}
	if err := st.CreateNightSession(context.Background(), rec, now); err != nil {
		t.Fatalf("create night session: %v", err)
	}
	seedNightSessionRevision(t, st, nightShutdownPayload())

	// Tick 1: still playing. The boundary stays armed.
	obs.set(nightPlayingObservations("player-01", "halloween-resting", now))
	h.nightAdvanceRestingIntershow(context.Background(), now, mustGetCurrentSession(t, st))
	if b, _ := decodeNightBoundary(mustGetCurrentSession(t, st).BoundaryJSON); b.State != nightBoundaryStateArmed {
		t.Fatalf("boundary state = %q while playback is healthy, want armed", b.State)
	}

	// Tick 2: Stop Now. FPP reads idle, four minutes before E.
	now = expectedE.Add(-4 * time.Minute)
	obs.set(idleObservation("player-01", now))
	h.nightAdvanceRestingIntershow(context.Background(), now, mustGetCurrentSession(t, st))

	got := mustGetCurrentSession(t, st)
	b, _ := decodeNightBoundary(got.BoundaryJSON)
	if b.State != nightBoundaryStateInvalid {
		t.Fatalf("boundary state = %q after an early idle, want invalid", b.State)
	}
	if b.Reason == "" {
		t.Fatal("the invalidated boundary carries no reason")
	}
	// The operator surface must not still say "armed".
	if ev := mapNightTransition(got); ev.State == v1.NightEvidenceRecorded {
		t.Fatalf("transition still reports recorded evidence after invalidation: %+v", ev)
	}

	// Tick 3: past the old E. Nothing may launch off the dead boundary.
	now = expectedE.Add(time.Minute)
	obs.set(idleObservation("player-01", now))
	h.nightAdvanceRestingIntershow(context.Background(), now, mustGetCurrentSession(t, st))

	got = mustGetCurrentSession(t, st)
	if got.State != nightStateRestingIntershow {
		t.Fatalf("state = %q past the invalidated boundary, want a visible hold in resting-intershow", got.State)
	}
	if starts != 0 {
		t.Fatalf("Start Playlist was dispatched %d times off an invalidated boundary", starts)
	}
}

// The same evidence during the lead window, before the show commits,
// returns the session to resting rather than committing a show.
func TestNightAdvanceTransitionToShow_EarlyIdleReturnsToRestingWithoutCommitting(t *testing.T) {
	dispatchedAt := time.Date(2026, 10, 31, 22, 0, 0, 0, time.UTC)
	anchor := nightContentAnchor{
		Purpose: nightAnchorPurposeRestingOneShot, FPPInstanceID: "player-01", Playlist: "halloween-resting",
		Item: "halloween-resting.fseq", DurationMS: 300000, PositionMS: 0, PositionMSKnown: true,
		DispatchedAt: dispatchedAt, ObservedAt: dispatchedAt,
	}
	expectedE := *deriveNightBoundary(anchor).ExpectedAt
	now := expectedE.Add(-30 * time.Second)

	obs := &mutableObservationLister{obs: idleObservation("player-01", now)}
	svc, st, _ := newTestIdentityServiceWithStore(t, func() time.Time { return now })
	deps := Dependencies{
		NightSessions: st, Observations: obs, Identity: svc, Config: st,
		FPP: &fakeFPPLister{views: []FPPInstanceView{{InstanceID: "player-01", Endpoint: "http://127.0.0.1:1"}}},
	}.withDefaults()
	h := &handlers{deps: deps, clock: func() time.Time { return now }, logger: testLogger()}

	rec := store.NightSessionRecord{
		ID: "sess-1", ConfigObjectID: "halloween-main", ConfigRevision: 1,
		State: nightStateTransitionToShow, StateEnteredAt: now, Cycle: 2,
		ArmedShowID: "show-1", ShowCommitted: false,
		ContentAnchorJSON: encodeNightContentAnchor(anchor),
		BoundaryJSON:      encodeNightBoundary(nightBoundary{State: nightBoundaryStateArmed, ExpectedAt: &expectedE}),
		Issuer:            store.NightSessionIssuer{PrincipalID: "p-1", PrincipalName: "operator-1"},
	}
	if err := st.CreateNightSession(context.Background(), rec, now); err != nil {
		t.Fatalf("create night session: %v", err)
	}
	payload := nightShutdownPayload()
	payload.EnterShow = config.NightSessionEnterShow{BlackoutHoldMs: 0}
	seedNightSessionRevision(t, st, payload)

	h.nightAdvanceTransitionToShow(context.Background(), now, mustGetCurrentSession(t, st))

	got := mustGetCurrentSession(t, st)
	if got.State != nightStateRestingIntershow {
		t.Fatalf("state = %q after an early idle in an uncommitted transition, want resting-intershow", got.State)
	}
	if got.ArmedShowID != "" {
		t.Fatalf("armedShowId = %q, want it cleared", got.ArmedShowID)
	}
	if b, _ := decodeNightBoundary(got.BoundaryJSON); b.State != nightBoundaryStateInvalid {
		t.Fatalf("boundary state = %q, want invalid", b.State)
	}
}
