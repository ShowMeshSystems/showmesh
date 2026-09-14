package api

import (
	"bytes"
	"context"
	"encoding/json"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/showmeshsystems/showmesh/internal/coordinator/identity"
	"github.com/showmeshsystems/showmesh/internal/coordinator/store"
	"github.com/showmeshsystems/showmesh/pkg/observation"
)

// firstCycleFixture is TestFullNight_StartsCyclesAndEndsWithFPPObservablyStopped's
// own setup, reused so the stale and fresh cases below drive the first
// cycle through the real start-night command path (prepare-site,
// run-readiness, start-preshow, start-night), not a hand-built
// transition-to-show record.
type firstCycleFixture struct {
	h      *handlers
	api    *API
	st     *store.Store
	host   *fakeFPPHost
	obs    *fakeObservationLister
	opTok  string
	logBuf *bytes.Buffer
	now    *time.Time
}

func newFirstCycleFixture(t *testing.T) firstCycleFixture {
	t.Helper()
	now := testNow
	clock := func() time.Time { return now }

	obs := &fakeObservationLister{}
	host := &fakeFPPHost{obs: obs, now: clock}

	mux := http.NewServeMux()
	for _, p := range []string{"halloween-resting", "halloween-show"} {
		name := p
		mux.HandleFunc("/api/playlist/"+name, func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"name":"` + name + `","mainPlaylist":[{"type":"sequence","enabled":1,"playOnce":0,"sequenceName":"resting-loop.fseq"}]}`))
		})
	}
	mux.HandleFunc("/api/command", func(w http.ResponseWriter, r *http.Request) {
		var body struct {
			Command string   `json:"command"`
			Args    []string `json:"args"`
		}
		_ = json.NewDecoder(r.Body).Decode(&body)
		host.mu.Lock()
		host.commands = append(host.commands, body.Command)
		host.mu.Unlock()
		switch body.Command {
		case "Start Playlist":
			if len(body.Args) > 0 {
				host.setPlaying(body.Args[0], 0)
			}
		case "Stop Now":
			host.setIdle()
		}
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("OK"))
	})
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)

	svc, st, _ := newTestIdentityServiceWithStore(t, clock)
	admin := mustCreatePrincipal(t, svc, "admin-1", identity.RoleAdmin)
	adminToken := mustIssueToken(t, svc, admin.ID)
	operator := mustCreatePrincipal(t, svc, "operator-1", identity.RoleOperator)
	opToken := mustIssueToken(t, svc, operator.ID)

	deps, _ := nightControlTestDeps(svc, st)
	deps.Observations = obs
	deps.FPP = &fakeFPPLister{views: []FPPInstanceView{{InstanceID: "player-01", Endpoint: srv.URL}}}
	backend := nightTestAssetBackend(t)
	deps.AssetBackend = backend
	deps = deps.withDefaults()

	api := New(deps, Options{Clock: clock, Logger: testLogger(), NightReadinessMaxAge: time.Hour})
	logBuf := &bytes.Buffer{}
	h := &handlers{
		deps: deps, clock: clock, logger: slog.New(slog.NewTextHandler(logBuf, nil)),
		fppCommandConfirmDeadline: 50 * time.Millisecond, fppCommandPollInterval: 10 * time.Millisecond,
		nightReadinessMaxAge: time.Hour,
	}

	mustPutShow(t, api, adminToken, "halloween-2026", `{"name":"halloween-2026"}`)
	mustCreateNightSessionFSEQAsset(t, st, backend, "halloween-2026", "resting-loop", "player-01")
	mustPutNightSession(t, api, adminToken, "halloween-main", nightFullNightBody)
	mustActivateNightSession(t, api, adminToken, "halloween-main")

	host.setIdle()
	mustNightCommand(t, api, opToken, "prepare-site")
	mustNightCommand(t, api, opToken, "run-readiness")
	mustNightCommand(t, api, opToken, "start-preshow")

	f := firstCycleFixture{h: h, api: api, st: st, host: host, obs: obs, opTok: opToken, logBuf: logBuf, now: &now}

	// Pre-show starts the resting playlist in repeat.
	f.tick()
	if got := f.state(t); got.ContentAnchorJSON == "" {
		t.Fatal("pre-show did not start the resting playlist")
	}

	// The operator lets pre-show run a while before calling start-night -
	// ordinary practice, and long enough that the dispatch confirmation
	// above is no longer the freshest thing in the observation store.
	f.advance(2 * time.Minute)
	return f
}

func (f firstCycleFixture) tick()                   { f.h.nightTick(context.Background(), *f.now) }
func (f firstCycleFixture) advance(d time.Duration) { *f.now = f.now.Add(d) }
func (f firstCycleFixture) state(t *testing.T) store.NightSessionRecord {
	t.Helper()
	return mustGetCurrentSession(t, f.st)
}

// TestFullNight_FirstCycleStaleRestingEvidenceNudgesAndLaunchesOnFreshEvidence
// checks whether ordinary FPP poll cadence alone - fpp.DefaultPollInterval
// is 15s, nightShowLaunchEvidenceMaxAge is 5s - trips the stale-evidence
// path documented for later cycles, with no enterShow lead configured at
// all. Nothing here proves this is unique to the first cycle: the
// mechanism is ordinary poll-cadence timing, and nothing in this code
// makes a later cycle immune to the same race. Per the owner's ruling,
// this must never dispatch into a refusal and wait out
// nightDispatchRetryBackoff for evidence a nudge could fetch in one LAN
// round trip: it nudges the REST collector and launches on the very next
// tick once fresh evidence lands.
func TestFullNight_FirstCycleStaleRestingEvidenceNudgesAndLaunchesOnFreshEvidence(t *testing.T) {
	f := newFirstCycleFixture(t)
	nudger := &recordingNudger{accept: true}
	f.h.deps.Nudger = nudger

	// Model the real fpp collector (fpp.DefaultPollInterval is 15s): its
	// most recent poll landed 8 seconds ago - well within fpp.status's
	// own currency window, but past nightShowLaunchEvidenceMaxAge's 5s
	// for fpp.playlist.name.
	pollAt := f.now.Add(-8 * time.Second)
	f.obs.obs = []observation.Observation{
		statusObservation("player-01", fppStatusValuePlaying, pollAt),
		playlistNameObservation("player-01", "halloween-resting", pollAt),
	}

	mustNightCommand(t, f.api, f.opTok, "start-night")
	if got := f.state(t); got.State != nightStateTransitionToShow {
		t.Fatalf("state after start-night = %q, want %q", got.State, nightStateTransitionToShow)
	}

	beforeArgs := len(f.host.sent())
	f.tick()
	if got := len(f.host.sent()); got != beforeArgs {
		t.Fatalf("Start Playlist reached FPP while evidence was stale = %d commands, want none (nudged instead of dispatched)", got-beforeArgs)
	}
	if n := strings.Count(f.logBuf.String(), "startPlaylist did not launch on this attempt"); n != 0 {
		t.Fatalf("log line count while stale = %d, want 0 (no dispatch attempt was made to refuse)", n)
	}
	if got := nudger.callsFor("player-01"); got != 1 {
		t.Fatalf("NudgePoll calls for player-01 = %d, want exactly 1", got)
	}
	got := f.state(t)
	if got.State != nightStateTransitionToShow {
		t.Fatalf("state while stale = %q, want still %q", got.State, nightStateTransitionToShow)
	}

	// The persisted record, not just a log line, must distinguish this
	// coordinator-caused refusal from FPP genuinely reporting busy.
	transition := mapNightTransition(got)
	if !strings.Contains(transition.Reason, "coordinator's own evidence") {
		t.Fatalf("transition reason = %q, want it to name the coordinator's own stale evidence, not just a busy refusal", transition.Reason)
	}

	// The nudged poll lands one tick later: launch must happen on THIS
	// tick, nowhere near nightDispatchRetryBackoff (10s).
	f.advance(1 * time.Second)
	f.obs.obs = []observation.Observation{
		statusObservation("player-01", fppStatusValuePlaying, *f.now),
		playlistNameObservation("player-01", "halloween-resting", *f.now),
	}
	f.tick()
	if got := f.state(t); got.State != nightStateLive {
		t.Fatalf("state one tick after fresh evidence landed = %q, want %q", got.State, nightStateLive)
	}
	if n := strings.Count(f.logBuf.String(), "startPlaylist did not launch on this attempt"); n != 0 {
		t.Fatalf("log line count over the whole run = %d, want 0 (never dispatched into a refusal)", n)
	}
}

// TestFullNight_FirstCycleStaleRestingEvidenceNudgeUnproductiveFallsBackToBackoff
// is the required fallback: a nudge that never produces fresh evidence
// (FPP unreachable, or its poll otherwise fails) must not wedge the
// launch decision forever. Once the stale evidence ages past its own
// currency window (the fake fpp.playlist.name/fpp.status observations'
// ValidFor, 1 minute here), nightShowLaunchIfBusy's ordinary
// not-current handling takes over and this coordinator falls back to the
// existing dispatch-and-backoff path, which still applies and records
// its own reason exactly as it did before this change.
func TestFullNight_FirstCycleStaleRestingEvidenceNudgeUnproductiveFallsBackToBackoff(t *testing.T) {
	f := newFirstCycleFixture(t)
	nudger := &recordingNudger{accept: false}
	f.h.deps.Nudger = nudger

	pollAt := f.now.Add(-8 * time.Second)
	f.obs.obs = []observation.Observation{
		statusObservation("player-01", fppStatusValuePlaying, pollAt),
		playlistNameObservation("player-01", "halloween-resting", pollAt),
	}

	mustNightCommand(t, f.api, f.opTok, "start-night")
	beforeArgs := len(f.host.sent())

	// The nudge never refreshes the store (simulating an unreachable
	// host): several ticks pass, evidence stays stale but still current,
	// nothing hot-loops past one nudge call per tick, and nothing
	// dispatches.
	for i := 0; i < 3; i++ {
		f.tick()
		f.advance(1 * time.Second)
	}
	if got := len(f.host.sent()); got != beforeArgs {
		t.Fatalf("Start Playlist reached FPP while nudging was unproductive = %d commands, want none", got-beforeArgs)
	}
	if got := nudger.totalCalls(); got != 3 {
		t.Fatalf("NudgePoll calls while stale = %d, want exactly 3 (one per tick, no hot-loop)", got)
	}
	got := f.state(t)
	transition := mapNightTransition(got)
	if !strings.Contains(transition.Reason, "coordinator's own evidence") {
		t.Fatalf("transition reason while stale = %q, want it to keep naming the coordinator's own stale evidence", transition.Reason)
	}

	// Evidence now ages past its own currency window entirely: this is
	// no longer "stale but current", so the normal dispatch-and-backoff
	// path takes over, refuses once (FPP itself, not this coordinator's
	// bookkeeping, since evidence is not current at all), and starts the
	// unchanged nightDispatchRetryBackoff. Advanced in small steps - each
	// safely under nightClockForwardJumpTolerance (30s) - so this does not
	// trip the loop's own clock-discontinuity resync, which a single
	// one-minute jump would.
	for f.now.Sub(pollAt) <= time.Minute {
		f.tick()
		f.advance(5 * time.Second)
	}
	f.tick()
	if got := len(f.host.sent()); got != beforeArgs {
		t.Fatalf("Start Playlist reached FPP on the fallback refusal = %d commands, want none (refused before the wire)", got-beforeArgs)
	}
	if n := strings.Count(f.logBuf.String(), "startPlaylist did not launch on this attempt"); n != 1 {
		t.Fatalf("log line count after the fallback refusal = %d, want exactly 1: %s", n, f.logBuf.String())
	}

	// Still inside the backoff window: silence, no retry.
	f.advance(3 * time.Second)
	f.tick()
	if n := strings.Count(f.logBuf.String(), "startPlaylist did not launch on this attempt"); n != 1 {
		t.Fatalf("log line count mid-backoff = %d, want still 1: %s", n, f.logBuf.String())
	}

	// The backoff elapses and fresh evidence has landed: the retry lands.
	f.advance(nightDispatchRetryBackoff + time.Second)
	f.obs.obs = []observation.Observation{
		statusObservation("player-01", fppStatusValuePlaying, *f.now),
		playlistNameObservation("player-01", "halloween-resting", *f.now),
	}
	f.tick()
	if got := f.state(t); got.State != nightStateLive {
		t.Fatalf("state once the backoff elapsed and evidence refreshed = %q, want %q", got.State, nightStateLive)
	}
}

// TestFullNight_FirstCycleFreshRestingEvidenceLaunchesImmediately is the
// control for the test above: identical fixture, but the resting
// evidence is stamped fresh at the instant start-night is called. This
// must launch on the very first tick, with no refusal at all - proving
// evidence staleness, not merely being the first cycle, is what decides
// the outcome.
func TestFullNight_FirstCycleFreshRestingEvidenceLaunchesImmediately(t *testing.T) {
	f := newFirstCycleFixture(t)

	f.obs.obs = []observation.Observation{
		statusObservation("player-01", fppStatusValuePlaying, *f.now),
		playlistNameObservation("player-01", "halloween-resting", *f.now),
	}

	mustNightCommand(t, f.api, f.opTok, "start-night")
	f.tick()

	if n := strings.Count(f.logBuf.String(), "startPlaylist did not launch on this attempt"); n != 0 {
		t.Fatalf("log line count with fresh evidence = %d, want 0 (no refusal at all): %s", n, f.logBuf.String())
	}
	if got := f.state(t); got.State != nightStateLive {
		t.Fatalf("state with fresh evidence = %q, want %q on the very first tick", got.State, nightStateLive)
	}
}
