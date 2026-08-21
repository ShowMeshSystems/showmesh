package api

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"

	"github.com/showmeshsystems/showmesh/internal/coordinator/identity"
	"github.com/showmeshsystems/showmesh/internal/coordinator/store"
	"github.com/showmeshsystems/showmesh/pkg/observation"
)

// One night, end to end: it starts, it cycles through a second show, and
// it finishes with FPP observably stopped. Every state change comes from
// the controller's own tick reading evidence a fake FPP produced in
// response to commands the controller actually sent.

// nightFullNightBody has no transition cues, so the lifecycle is the only
// thing under test; cue behavior has its own coverage.
const nightFullNightBody = `{
	"show": "halloween-2026",
	"label": "Halloween main loop",
	"showPlaylist": {"fppInstanceId": "player-01", "playlist": "halloween-show"},
	"resting": {
		"fppInstanceId": "player-01",
		"playlist": "halloween-resting",
		"timelineAsset": {"show": "halloween-2026", "sequence": "resting-loop", "target": "player-01"},
		"endOfNightRepeat": true
	},
	"enterShow": {"cues": [], "blackoutHoldMs": 0},
	"enterResting": {"cues": [], "blackoutAfterShowMs": 0}
}`

// fakeFPPHost answers playlist reads and commands, and reports playback
// evidence that follows the commands it was actually sent.
type fakeFPPHost struct {
	mu       sync.Mutex
	commands []string
	obs      *fakeObservationLister
	now      func() time.Time
}

func (f *fakeFPPHost) sent() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]string(nil), f.commands...)
}

func (f *fakeFPPHost) setPlaying(playlist string, positionMS int64) {
	at := f.now()
	observedAt := at
	f.obs.obs = []observation.Observation{
		{Resource: observation.ResourceRef{Kind: observation.ResourceFPP, ID: "player-01"}, Signal: "fpp.reachable", Value: true, ObservedAt: &observedAt, CollectedAt: at, Source: "fpp-rest", Quality: observation.QualityDirect, ValidFor: time.Minute},
		statusObservation("player-01", fppStatusValuePlaying, at),
		playlistNameObservation("player-01", playlist, at),
		positionMSObservation("player-01", positionMS, at),
	}
}

func (f *fakeFPPHost) setIdle() {
	at := f.now()
	observedAt := at
	f.obs.obs = []observation.Observation{
		{Resource: observation.ResourceRef{Kind: observation.ResourceFPP, ID: "player-01"}, Signal: "fpp.reachable", Value: true, ObservedAt: &observedAt, CollectedAt: at, Source: "fpp-rest", Quality: observation.QualityDirect, ValidFor: time.Minute},
		statusObservation("player-01", fppStatusValueIdle, at),
		playlistNameObservation("player-01", "", at),
	}
}

func TestFullNight_StartsCyclesAndEndsWithFPPObservablyStopped(t *testing.T) {
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
	h := &handlers{
		deps: deps, clock: clock, logger: testLogger(),
		fppCommandConfirmDeadline: 50 * time.Millisecond, fppCommandPollInterval: 10 * time.Millisecond,
		nightReadinessMaxAge: time.Hour,
	}

	mustPutShow(t, api, adminToken, "halloween-2026", `{"name":"halloween-2026"}`)
	mustCreateNightSessionFSEQAsset(t, st, backend, "halloween-2026", "resting-loop", "player-01")
	mustPutNightSession(t, api, adminToken, "halloween-main", nightFullNightBody)
	mustActivateNightSession(t, api, adminToken, "halloween-main")

	tick := func() { h.nightTick(context.Background(), now) }
	advance := func(d time.Duration) { now = now.Add(d) }
	state := func() store.NightSessionRecord { return mustGetCurrentSession(t, st) }
	mustState := func(step, want string) {
		t.Helper()
		if got := state(); got.State != want {
			t.Fatalf("%s: state = %q, want %q (degraded=%v %q)", step, got.State, want, got.Degraded, got.DegradedReason)
		}
	}

	// --- open the night ---
	host.setIdle()
	mustNightCommand(t, api, opToken, "prepare-site")
	mustNightCommand(t, api, opToken, "run-readiness")
	mustNightCommand(t, api, opToken, "start-preshow")
	mustState("start-preshow", nightStatePreshow)

	// Pre-show resting starts, in repeat.
	tick()
	if got := state(); got.ContentAnchorJSON == "" {
		t.Fatal("pre-show did not start the resting playlist")
	}
	advance(time.Second)
	tick()

	mustNightCommand(t, api, opToken, "start-night")
	mustState("start-night", nightStateTransitionToShow)

	// --- show 1 ---
	advance(time.Second)
	tick()
	mustState("show 1 launch", nightStateLive)
	if got := state(); got.Cycle != 1 {
		t.Fatalf("cycle = %d after the first show launched, want 1", got.Cycle)
	}

	// The show ends.
	advance(30 * time.Second)
	host.setIdle()
	tick()
	mustState("show 1 completion", nightStateTransitionToResting)

	// Inter-show resting starts as a one-shot, with a derived boundary.
	advance(time.Second)
	tick()
	mustState("enter resting", nightStateRestingIntershow)
	b, ok := decodeNightBoundary(state().BoundaryJSON)
	if !ok || b.State != nightBoundaryStateArmed || b.ExpectedAt == nil {
		t.Fatalf("resting-intershow armed no boundary: %+v", b)
	}

	// --- the cycle: the boundary drives the second show ---
	expectedE := *b.ExpectedAt
	advance(expectedE.Sub(now))
	host.setPlaying("halloween-resting", 0)
	tick()
	mustState("boundary reached", nightStateTransitionToShow)
	if got := state(); got.Cycle != 2 {
		t.Fatalf("cycle = %d after the boundary drove a second transition, want 2", got.Cycle)
	}

	advance(time.Second)
	tick()
	mustState("show 2 launch", nightStateLive)

	// --- close the night ---
	mustNightCommand(t, api, opToken, "request-final-show")
	advance(30 * time.Second)
	host.setIdle()
	tick()
	mustState("show 2 completion", nightStateTransitionToResting)

	advance(time.Second)
	tick()
	mustState("end of night", nightStateEndOfNightResting)

	out := mustNightCommand(t, api, opToken, "fade-out-night")
	if out.Session.State != nightStateFadingOut {
		t.Fatalf("fade-out-night: state = %q, want fading-out", out.Session.State)
	}

	// The fade tick issues a real stop; the next tick sees the idle
	// evidence that stop produced and only then reports stopped.
	advance(time.Second)
	beforeStop := len(host.sent())
	tick()
	advance(time.Second)
	tick()

	mustState("night closed", nightStateStopped)

	// The stop must actually have been sent, not inferred.
	var stops int
	for _, c := range host.sent()[beforeStop:] {
		if c == "Stop Now" {
			stops++
		}
	}
	if stops == 0 {
		t.Fatalf("the night reported stopped without ever sending Stop Now; commands: %v", host.sent())
	}
	if got := state(); got.Degraded {
		t.Fatalf("a clean night ended degraded: %q", got.DegradedReason)
	}

	// And FPP is observably idle at the end, not merely asserted stopped.
	final := nightObservePlayback(context.Background(), obs, "player-01", time.Time{}, now)
	if !final.Current || final.Status != fppStatusValueIdle || !final.PlaylistCurrent || final.Playlist != "" {
		t.Fatalf("FPP is not observably idle at the end of the night: %+v", final)
	}
}
