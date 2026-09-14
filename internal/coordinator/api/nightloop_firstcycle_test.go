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

// countEventsByCategory lists every recorded event and counts how many
// carry category.
func countEventsByCategory(t *testing.T, st *store.Store, category string) int {
	t.Helper()
	events, _, err := st.ListEvents(context.Background(), 0, 0)
	if err != nil {
		t.Fatalf("list events: %v", err)
	}
	n := 0
	for _, ev := range events {
		if ev.Category == category {
			n++
		}
	}
	return n
}

// TestFullNight_FirstCycleStaleRestingEvidenceNudgesAndLaunchesOnFreshEvidence
// checks whether ordinary FPP poll cadence alone - fpp.DefaultPollInterval
// is 15s, nightShowLaunchEvidenceMaxAge is 5s - trips the stale-evidence
// path documented for later cycles, with no enterShow lead configured at
// all. This must nudge once and launch on the tick fresh evidence lands,
// well inside nightShowLaunchStaleNudgeWindow, never waiting out
// nightDispatchRetryBackoff for evidence a nudge could fetch in one LAN
// round trip.
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
	if got := countEventsByCategory(t, f.st, nightEventCategoryStaleEvidenceRefusal); got != 1 {
		t.Fatalf("stale-evidence-refusal events = %d, want exactly 1", got)
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

	// A second tick, still well inside nightShowLaunchStaleNudgeWindow,
	// must not nudge again.
	f.advance(1 * time.Second)
	f.tick()
	if got := nudger.callsFor("player-01"); got != 1 {
		t.Fatalf("NudgePoll calls for player-01 after a second stale tick = %d, want still exactly 1 (no repeat within the window)", got)
	}

	// The nudged poll lands: launch must happen on THIS tick, nowhere
	// near nightDispatchRetryBackoff (10s).
	f.advance(1 * time.Second)
	f.obs.obs = []observation.Observation{
		statusObservation("player-01", fppStatusValuePlaying, *f.now),
		playlistNameObservation("player-01", "halloween-resting", *f.now),
	}
	f.tick()
	if got := f.state(t); got.State != nightStateLive {
		t.Fatalf("state once fresh evidence landed = %q, want %q", got.State, nightStateLive)
	}
	if n := strings.Count(f.logBuf.String(), "startPlaylist did not launch on this attempt"); n != 0 {
		t.Fatalf("log line count over the whole run = %d, want 0 (never dispatched into a refusal)", n)
	}
	// 2, not 1: the stale-evidence episode's own nudge, plus the
	// pre-existing, unrelated post-dispatch confirmation nudge every
	// successful dispatch already issues (fppcommand_dispatch.go).
	if got := nudger.callsFor("player-01"); got != 2 {
		t.Fatalf("NudgePoll calls for player-01 over the whole run = %d, want exactly 2", got)
	}
}

// TestFullNight_FirstCycleStaleRestingEvidenceNudgeUnproductiveFallsBackToBackoff
// is the required fallback: a nudge that never produces fresh evidence
// (FPP unreachable, or its poll otherwise fails) must not wedge the
// launch decision forever, and must not repeat the nudge on every tick
// while it waits. Once nightShowLaunchStaleNudgeWindow elapses with
// evidence still stale, the existing dispatch-and-backoff path takes over
// exactly as it did before this change.
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
	// host): several ticks pass while still inside the window, one nudge
	// only, nothing dispatches.
	for i := 0; i < 3; i++ {
		f.tick()
		f.advance(1 * time.Second)
	}
	if got := len(f.host.sent()); got != beforeArgs {
		t.Fatalf("Start Playlist reached FPP while nudging was unproductive = %d commands, want none", got-beforeArgs)
	}
	if got := nudger.totalCalls(); got != 1 {
		t.Fatalf("NudgePoll calls while stale = %d, want exactly 1 (one per episode, not one per tick)", got)
	}
	if got := countEventsByCategory(t, f.st, nightEventCategoryStaleEvidenceRefusal); got != 1 {
		t.Fatalf("stale-evidence-refusal events = %d, want exactly 1", got)
	}
	got := f.state(t)
	transition := mapNightTransition(got)
	if !strings.Contains(transition.Reason, "coordinator's own evidence") {
		t.Fatalf("transition reason while stale = %q, want it to keep naming the coordinator's own stale evidence", transition.Reason)
	}

	// nightShowLaunchStaleNudgeWindow elapses with evidence still stale
	// (well inside its own 1-minute ValidFor): the ordinary refuse path
	// takes over, dispatches once, and FPP itself refuses as busy - the
	// persisted reason must still carry the original stale-evidence
	// wording, not just the wire-level busy detail.
	f.advance(nightShowLaunchStaleNudgeWindow)
	f.tick()
	if got := len(f.host.sent()); got != beforeArgs {
		t.Fatalf("Start Playlist reached FPP on the fallback refusal = %d commands, want none (refused before the wire)", got-beforeArgs)
	}
	if n := strings.Count(f.logBuf.String(), "startPlaylist did not launch on this attempt"); n != 1 {
		t.Fatalf("log line count after the fallback refusal = %d, want exactly 1: %s", n, f.logBuf.String())
	}
	if got := countEventsByCategory(t, f.st, nightEventCategoryStaleEvidenceUnproductive); got != 1 {
		t.Fatalf("stale-evidence-nudge-unproductive events = %d, want exactly 1", got)
	}
	fallbackTransition := mapNightTransition(f.state(t))
	if !strings.Contains(fallbackTransition.Reason, "coordinator's own evidence") {
		t.Fatalf("transition reason after the fallback refusal = %q, want it to still name the coordinator's own stale evidence", fallbackTransition.Reason)
	}

	// Still inside the backoff window: silence, no retry, no repeat nudge.
	f.advance(3 * time.Second)
	f.tick()
	if n := strings.Count(f.logBuf.String(), "startPlaylist did not launch on this attempt"); n != 1 {
		t.Fatalf("log line count mid-backoff = %d, want still 1: %s", n, f.logBuf.String())
	}
	if got := nudger.totalCalls(); got != 1 {
		t.Fatalf("NudgePoll calls mid-backoff = %d, want still exactly 1", got)
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

// TestFullNight_FirstCycleDispatchedShowAnchorSkipsStaleNudge covers a show
// anchor already dispatched and awaiting confirmation (a replace already
// sent to FPP): the stale-evidence nudge must never run here, even when
// the resting-playlist evidence nightShowLaunchIfBusy reads independently
// is itself stale - nightEnsureAnchor's own observation-polling path for
// the ALREADY-DISPATCHED show playlist owns this decision, unchanged.
func TestFullNight_FirstCycleDispatchedShowAnchorSkipsStaleNudge(t *testing.T) {
	f := newFirstCycleFixture(t)
	nudger := &recordingNudger{accept: true}
	f.h.deps.Nudger = nudger

	pollAt := f.now.Add(-8 * time.Second)
	f.obs.obs = []observation.Observation{
		statusObservation("player-01", fppStatusValuePlaying, pollAt),
		playlistNameObservation("player-01", "halloween-resting", pollAt),
	}
	mustNightCommand(t, f.api, f.opTok, "start-night")

	dispatchedAt := *f.now
	anchor := nightContentAnchor{
		Purpose: nightAnchorPurposeShow, FPPInstanceID: "player-01", Playlist: "halloween-show",
		DispatchedAt: dispatchedAt,
	}
	rec := f.state(t)
	rec.ContentAnchorJSON = encodeNightContentAnchor(anchor)
	if err := f.st.UpdateNightSession(context.Background(), rec, *f.now); err != nil {
		t.Fatalf("seed dispatched show anchor: %v", err)
	}

	before := f.state(t)
	f.tick()

	if got := nudger.totalCalls(); got != 0 {
		t.Fatalf("NudgePoll calls with a show anchor already dispatched = %d, want 0", got)
	}
	if got := countEventsByCategory(t, f.st, nightEventCategoryStaleEvidenceRefusal); got != 0 {
		t.Fatalf("stale-evidence-refusal events with a show anchor already dispatched = %d, want 0", got)
	}
	after := f.state(t)
	if after.ContentAnchorJSON != before.ContentAnchorJSON || after.BoundaryJSON != before.BoundaryJSON {
		t.Fatalf("session record changed while awaiting confirmation of an already-dispatched anchor: before anchor=%s boundary=%s; after anchor=%s boundary=%s",
			before.ContentAnchorJSON, before.BoundaryJSON, after.ContentAnchorJSON, after.BoundaryJSON)
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

// TestFullNight_FirstCycleMixedSourcePluginStaleButCurrentNudgesOnceThenFallsBack
// is the mixed-source case: the only evidence for the resting playlist
// comes from fpp-plugin (REST is unreachable, so it has recorded nothing),
// and that plugin row is itself older than nightShowLaunchEvidenceMaxAge
// but still within its own 45s ValidFor. This must still nudge the REST
// collector exactly once - a fresher REST poll could still outrank the
// aging plugin claim on recency - and, since REST stays unreachable and
// nothing fresher ever arrives, fall back to the ordinary backoff path
// once nightShowLaunchStaleNudgeWindow elapses, exactly like a REST-only
// host.
func TestFullNight_FirstCycleMixedSourcePluginStaleButCurrentNudgesOnceThenFallsBack(t *testing.T) {
	f := newFirstCycleFixture(t)
	nudger := &recordingNudger{accept: false}
	f.h.deps.Nudger = nudger

	pluginAt := f.now.Add(-14 * time.Second)
	f.obs.obs = []observation.Observation{
		pluginStatusObservation("player-01", fppStatusValuePlaying, pluginAt),
		pluginPlaylistNameObservation("player-01", "halloween-resting", pluginAt),
	}

	mustNightCommand(t, f.api, f.opTok, "start-night")
	beforeArgs := len(f.host.sent())

	for i := 0; i < 3; i++ {
		f.tick()
		f.advance(1 * time.Second)
	}
	if got := nudger.totalCalls(); got != 1 {
		t.Fatalf("NudgePoll calls with a stale-but-current plugin row and unreachable REST = %d, want exactly 1", got)
	}
	if got := len(f.host.sent()); got != beforeArgs {
		t.Fatalf("Start Playlist reached FPP while nudging was unproductive = %d commands, want none", got-beforeArgs)
	}

	f.advance(nightShowLaunchStaleNudgeWindow)
	f.tick()
	if got := len(f.host.sent()); got != beforeArgs {
		t.Fatalf("Start Playlist reached FPP on the fallback refusal = %d commands, want none (refused before the wire)", got-beforeArgs)
	}
	if n := strings.Count(f.logBuf.String(), "startPlaylist did not launch on this attempt"); n != 1 {
		t.Fatalf("log line count after the fallback refusal = %d, want exactly 1: %s", n, f.logBuf.String())
	}
	if got := nudger.totalCalls(); got != 1 {
		t.Fatalf("NudgePoll calls after the fallback = %d, want still exactly 1", got)
	}
}
