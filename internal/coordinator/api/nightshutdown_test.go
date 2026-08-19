package api

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/showmeshsystems/showmesh/internal/coordinator/config"
	"github.com/showmeshsystems/showmesh/internal/coordinator/store"
	"github.com/showmeshsystems/showmesh/pkg/observation"
)

// The fading-out path: a real FPP stop, and stopped only on fresh idle
// evidence that postdates it.

type nightShutdownFixture struct {
	h        *handlers
	store    *store.Store
	obs      *mutableObservationLister
	endpoint string

	mu       sync.Mutex
	commands []string
}

func (f *nightShutdownFixture) sentCommands() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]string(nil), f.commands...)
}

// newNightShutdownFixture builds a fading-out session against a real
// store, a real FPP command endpoint, and a mutable observation lister.
func newNightShutdownFixture(t *testing.T, now *time.Time, payload config.NightSessionPayload, rec store.NightSessionRecord) *nightShutdownFixture {
	t.Helper()
	f := &nightShutdownFixture{obs: &mutableObservationLister{}}

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body struct {
			Command string   `json:"command"`
			Args    []string `json:"args"`
		}
		_ = json.NewDecoder(r.Body).Decode(&body)
		f.mu.Lock()
		f.commands = append(f.commands, body.Command)
		f.mu.Unlock()
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("OK"))
	}))
	t.Cleanup(srv.Close)
	f.endpoint = srv.URL

	svc, st, _ := newTestIdentityServiceWithStore(t, func() time.Time { return *now })
	deps := Dependencies{
		NightSessions: st, Observations: f.obs, Identity: svc, Config: st,
		FPP: &fakeFPPLister{views: []FPPInstanceView{{InstanceID: "player-01", Endpoint: srv.URL}}},
	}.withDefaults()
	// A short confirmation deadline: these tests supply or withhold
	// evidence deliberately and never need the production wait.
	f.h = &handlers{
		deps: deps, clock: func() time.Time { return *now }, logger: testLogger(),
		fppCommandConfirmDeadline: 50 * time.Millisecond,
		fppCommandPollInterval:    10 * time.Millisecond,
	}
	f.store = st

	payloadJSON, err := config.EncodeNightSessionPayload(payload)
	if err != nil {
		t.Fatalf("encode payload: %v", err)
	}
	if _, err := st.CreateConfigObject(context.Background(), config.NightSessionConfigKind, "halloween-main"); err != nil {
		t.Fatalf("create config object: %v", err)
	}
	if _, err := st.CreateConfigRevision(context.Background(), store.ConfigRevisionRecord{
		Kind: config.NightSessionConfigKind, ObjectID: "halloween-main", Revision: 1, PayloadJSON: payloadJSON, Source: "api",
	}); err != nil {
		t.Fatalf("create config revision: %v", err)
	}
	if err := st.CreateNightSession(context.Background(), rec, *now); err != nil {
		t.Fatalf("create night session: %v", err)
	}
	return f
}

func nightShutdownPayload() config.NightSessionPayload {
	return config.NightSessionPayload{
		Show: "halloween-2026", Label: "test",
		ShowPlaylist: config.NightSessionFPPPlaylist{FPPInstanceID: "player-01", Playlist: "halloween-show"},
		Resting:      config.NightSessionResting{FPPInstanceID: "player-01", Playlist: "halloween-resting", EndOfNightPlaylist: "halloween-resting"},
	}
}

func fadingOutSession(enteredAt time.Time, intent string) store.NightSessionRecord {
	return store.NightSessionRecord{
		ID: "sess-1", ConfigObjectID: "halloween-main", ConfigRevision: 1,
		State: nightStateFadingOut, StateEnteredAt: enteredAt, Cycle: 1,
		AdmissionClosed: true, ShutdownIntent: intent,
	}
}

// A fade during resting must issue a real FPP stop and reach stopped only
// once idle evidence collected after that stop confirms it.
func TestNightFadingOut_IssuesStopAndReachesStoppedOnFreshIdleEvidence(t *testing.T) {
	now := time.Date(2026, 10, 31, 23, 0, 0, 0, time.UTC)
	f := newNightShutdownFixture(t, &now, nightShutdownPayload(), fadingOutSession(now, "fade-out"))

	f.h.nightAdvanceFadingOut(context.Background(), now, mustGetCurrentSession(t, f.store))

	if cmds := f.sentCommands(); len(cmds) != 1 || cmds[0] != "Stop Now" {
		t.Fatalf("commands sent to FPP = %v, want exactly one %q", cmds, "Stop Now")
	}
	got := mustGetCurrentSession(t, f.store)
	if got.State != nightStateFadingOut {
		t.Fatalf("state = %q with no confirming evidence yet, want still fading-out", got.State)
	}
	anchor, ok := decodeNightContentAnchor(got.ContentAnchorJSON)
	if !ok || anchor.Purpose != nightAnchorPurposeShutdownStop || anchor.DispatchedAt.IsZero() {
		t.Fatalf("shutdown anchor = %+v, want a shutdown-stop anchor carrying DispatchedAt", anchor)
	}

	// Idle with a cleared playlist, collected after the dispatch.
	now = now.Add(5 * time.Second)
	f.obs.set([]observation.Observation{
		statusObservation("player-01", fppStatusValueIdle, now),
		playlistNameObservation("player-01", "", now),
	})
	f.h.nightAdvanceFadingOut(context.Background(), now, mustGetCurrentSession(t, f.store))

	got = mustGetCurrentSession(t, f.store)
	if got.State != nightStateStopped {
		t.Fatalf("state = %q after fresh idle evidence, want stopped", got.State)
	}
	if cmds := f.sentCommands(); len(cmds) != 1 {
		t.Fatalf("commands sent to FPP = %v, want the stop to be sent exactly once across both ticks", cmds)
	}
}

// Evidence that predates the stop can never confirm it: a session already
// reading idle before the fade must still send a stop and must not report
// stopped off that pre-dispatch reading.
func TestNightFadingOut_PreDispatchIdleEvidenceNeverConfirmsTheStop(t *testing.T) {
	now := time.Date(2026, 10, 31, 23, 0, 0, 0, time.UTC)
	f := newNightShutdownFixture(t, &now, nightShutdownPayload(), fadingOutSession(now, "fade-out"))
	f.obs.set([]observation.Observation{
		statusObservation("player-01", fppStatusValueIdle, now.Add(-30*time.Second)),
		playlistNameObservation("player-01", "", now.Add(-30*time.Second)),
	})

	f.h.nightAdvanceFadingOut(context.Background(), now, mustGetCurrentSession(t, f.store))

	if cmds := f.sentCommands(); len(cmds) != 1 {
		t.Fatalf("commands sent = %v, want the stop to be issued regardless of stale idle evidence", cmds)
	}
	if got := mustGetCurrentSession(t, f.store); got.State != nightStateFadingOut {
		t.Fatalf("state = %q off evidence collected before the stop was dispatched, want fading-out", got.State)
	}
}

// An ambiguous stop degrades with a stated reason past its deadline and
// never reports stopped.
func TestNightFadingOut_AmbiguousStopDegradesAndNeverReportsStopped(t *testing.T) {
	now := time.Date(2026, 10, 31, 23, 0, 0, 0, time.UTC)
	f := newNightShutdownFixture(t, &now, nightShutdownPayload(), fadingOutSession(now, "fade-out"))

	f.h.nightAdvanceFadingOut(context.Background(), now, mustGetCurrentSession(t, f.store))

	// No confirming evidence ever arrives.
	now = now.Add(nightShutdownStopConfirmDeadline + time.Second)
	f.h.nightAdvanceFadingOut(context.Background(), now, mustGetCurrentSession(t, f.store))

	got := mustGetCurrentSession(t, f.store)
	if got.State == nightStateStopped {
		t.Fatal("state = stopped with no confirming evidence; an ambiguous stop must never claim success")
	}
	if !got.Degraded {
		t.Fatalf("session is not degraded after an unconfirmed stop past its deadline: %+v", got)
	}
	if !strings.Contains(got.DegradedReason, "not reported stopped") {
		t.Fatalf("degradedReason = %q, want it to state the stop was not confirmed", got.DegradedReason)
	}
}

// A stop refused before it reaches FPP leaves DispatchedAt unset so a
// later tick retries under the same idempotency key, and the retry is
// paced rather than fired every tick.
func TestNightFadingOut_PreWireRefusalRetriesOnALaterTick(t *testing.T) {
	now := time.Date(2026, 10, 31, 23, 0, 0, 0, time.UTC)
	payload := nightShutdownPayload()
	payload.Resting.FPPInstanceID = "not-configured"
	payload.ShowPlaylist.FPPInstanceID = "not-configured"
	f := newNightShutdownFixture(t, &now, payload, fadingOutSession(now, "fade-out"))

	f.h.nightAdvanceFadingOut(context.Background(), now, mustGetCurrentSession(t, f.store))

	got := mustGetCurrentSession(t, f.store)
	anchor, ok := decodeNightContentAnchor(got.ContentAnchorJSON)
	if !ok || anchor.Purpose != nightAnchorPurposeShutdownStop {
		t.Fatalf("anchor = %+v, want a shutdown-stop anchor", anchor)
	}
	if !anchor.DispatchedAt.IsZero() {
		t.Fatalf("DispatchedAt = %v for a stop that never reached the wire, want zero", anchor.DispatchedAt)
	}
	if !strings.Contains(anchor.Source, "refused before it reached FPP") {
		t.Fatalf("anchor.Source = %q, want the pre-wire refusal reason", anchor.Source)
	}
	if got.State != nightStateFadingOut {
		t.Fatalf("state = %q after a refused stop, want still fading-out", got.State)
	}

	// Within the backoff: no new attempt, and the anchor is unchanged.
	now = now.Add(nightShutdownStopRetryBackoff / 2)
	f.h.nightAdvanceFadingOut(context.Background(), now, mustGetCurrentSession(t, f.store))
	again, _ := decodeNightContentAnchor(mustGetCurrentSession(t, f.store).ContentAnchorJSON)
	if !again.ObservedAt.Equal(anchor.ObservedAt) {
		t.Fatalf("the refusal was retried inside its own backoff window: %v then %v", anchor.ObservedAt, again.ObservedAt)
	}

	// Past the backoff, with the instance now reachable, the retry lands.
	now = now.Add(nightShutdownStopRetryBackoff)
	f.h.deps.FPP = &fakeFPPLister{views: []FPPInstanceView{{InstanceID: "not-configured", Endpoint: f.endpoint}}}
	f.h.nightAdvanceFadingOut(context.Background(), now, mustGetCurrentSession(t, f.store))
	if cmds := f.sentCommands(); len(cmds) != 1 || cmds[0] != "Stop Now" {
		t.Fatalf("commands sent after the backoff elapsed = %v, want one %q", cmds, "Stop Now")
	}
}

// A deferred shutdown outranks end-of-night resting: once the final show
// completes, nothing else starts.
func TestNightTransitionToResting_PendingShutdownSkipsEndOfNightRepeat(t *testing.T) {
	now := time.Date(2026, 10, 31, 23, 0, 0, 0, time.UTC)
	rec := store.NightSessionRecord{
		ID: "sess-1", ConfigObjectID: "halloween-main", ConfigRevision: 1,
		State: nightStateTransitionToResting, StateEnteredAt: now, Cycle: 1,
		AdmissionClosed: true, FinalShowRequested: true, ShutdownIntent: "fade-out",
	}
	f := newNightShutdownFixture(t, &now, nightShutdownPayload(), rec)

	f.h.nightAdvanceTransitionToResting(context.Background(), now, mustGetCurrentSession(t, f.store))

	got := mustGetCurrentSession(t, f.store)
	if got.State != nightStateFadingOut {
		t.Fatalf("state = %q with a shutdown pending, want fading-out", got.State)
	}
	if cmds := f.sentCommands(); len(cmds) != 0 {
		t.Fatalf("commands sent = %v, want none: the end-of-night playlist must not start under a pending shutdown", cmds)
	}
}

// Without a pending shutdown the same state still starts end-of-night
// resting, so the guard above is not simply disabling that path.
func TestNightTransitionToResting_WithoutShutdownStillStartsEndOfNightRepeat(t *testing.T) {
	now := time.Date(2026, 10, 31, 23, 0, 0, 0, time.UTC)
	rec := store.NightSessionRecord{
		ID: "sess-1", ConfigObjectID: "halloween-main", ConfigRevision: 1,
		State: nightStateTransitionToResting, StateEnteredAt: now, Cycle: 1,
		FinalShowRequested: true,
	}
	f := newNightShutdownFixture(t, &now, nightShutdownPayload(), rec)
	f.obs.set([]observation.Observation{statusObservation("player-01", fppStatusValueIdle, now)})

	f.h.nightAdvanceTransitionToResting(context.Background(), now, mustGetCurrentSession(t, f.store))

	if cmds := f.sentCommands(); len(cmds) != 1 || cmds[0] != "Start Playlist" {
		t.Fatalf("commands sent = %v, want one %q", cmds, "Start Playlist")
	}
}
