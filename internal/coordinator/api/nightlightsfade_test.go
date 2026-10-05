package api

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/showmeshsystems/showmesh/internal/coordinator/config"
	"github.com/showmeshsystems/showmesh/internal/coordinator/fppcommand"
	"github.com/showmeshsystems/showmesh/internal/coordinator/identity"
	"github.com/showmeshsystems/showmesh/internal/coordinator/store"
)

// lightsNight runs a real night through the controller's own ticks against a
// fake FPP host, logging gain writes and playlist starts in one ordered list.
type lightsNight struct {
	t     *testing.T
	now   time.Time
	st    *store.Store
	h     *handlers
	api   *API
	host  *fakeFPPHost
	token string

	mu     sync.Mutex
	log    []string
	failIf func(f nightLightingFade) bool
}

func (n *lightsNight) record(s string) {
	n.mu.Lock()
	defer n.mu.Unlock()
	n.log = append(n.log, s)
}

func (n *lightsNight) events() []string {
	n.mu.Lock()
	defer n.mu.Unlock()
	return append([]string(nil), n.log...)
}

func (n *lightsNight) tick()                             { n.h.nightTick(context.Background(), n.now) }
func (n *lightsNight) advance(d time.Duration)           { n.now = n.now.Add(d) }
func (n *lightsNight) session() store.NightSessionRecord { return mustGetCurrentSession(n.t, n.st) }

func (n *lightsNight) mustState(step, want string) {
	n.t.Helper()
	if got := n.session(); got.State != want {
		n.t.Fatalf("%s: state = %q, want %q (degraded=%v %q)", step, got.State, want, got.Degraded, got.DegradedReason)
	}
}

func lightsBody(extra string) string {
	return strings.Replace(nightFullNightBody, `"enterShow"`, extra+`"enterShow"`, 1)
}

func newLightsNight(t *testing.T, extra string) *lightsNight {
	t.Helper()
	n := &lightsNight{t: t, now: testNow}
	clock := func() time.Time { return n.now }
	obs := &fakeObservationLister{}
	n.host = &fakeFPPHost{obs: obs, now: clock}

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
		switch body.Command {
		case "Start Playlist":
			if len(body.Args) > 0 {
				n.record("start:" + body.Args[0])
				n.host.setPlaying(body.Args[0], 0)
			}
		case "Stop Now":
			n.host.setIdle()
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
	n.token = mustIssueToken(t, svc, operator.ID)
	n.st = st

	deps, _ := nightControlTestDeps(svc, st)
	deps.Observations = obs
	deps.FPP = &fakeFPPLister{views: []FPPInstanceView{{InstanceID: "player-01", Endpoint: srv.URL}}}
	backend := nightTestAssetBackend(t)
	deps.AssetBackend = backend
	deps = deps.withDefaults()

	api := New(deps, Options{Clock: clock, Logger: testLogger(), NightReadinessMaxAge: time.Hour})
	n.api = api
	n.h = &handlers{
		deps: deps, clock: clock, logger: testLogger(),
		fppCommandConfirmDeadline: 50 * time.Millisecond, fppCommandPollInterval: 10 * time.Millisecond,
		nightReadinessMaxAge: time.Hour,
		nightGainWriter: func(_ context.Context, target config.ShowActionTarget, fade nightLightingFade, _ string) (fppcommand.TransitionGainOutcome, error) {
			n.mu.Lock()
			fail := n.failIf != nil && n.failIf(fade)
			n.mu.Unlock()
			n.record(fmt.Sprintf("gain:%s:%d:%ds", target.InstanceID, fade.TargetPercent, fade.FadeSeconds))
			if fail {
				return fppcommand.TransitionGainOutcome{}, errors.New("the plugin did not answer")
			}
			return fppcommand.TransitionGainOutcome{Applied: true}, nil
		},
	}

	mustPutShow(t, api, adminToken, "halloween-2026", `{"name":"halloween-2026"}`)
	mustCreateNightSessionFSEQAsset(t, st, backend, "halloween-2026", "resting-loop", "player-01")
	mustPutNightSession(t, api, adminToken, "halloween-main", lightsBody(extra))
	mustActivateNightSession(t, api, adminToken, "halloween-main")
	return n
}

// openToFirstShow takes the night through pre-show and start-night, then
// launches the first show.
func (n *lightsNight) openToFirstShow() {
	n.host.setIdle()
	mustNightCommand(n.t, n.api, n.token, "prepare-site")
	mustNightCommand(n.t, n.api, n.token, "run-readiness")
	mustNightCommand(n.t, n.api, n.token, "start-preshow")
	n.tick()
	n.advance(time.Second)
	n.tick()
	mustNightCommand(n.t, n.api, n.token, "start-night")
	n.advance(time.Second)
	n.tick()
	n.mustState("first show launch", nightStateLive)
}

// endShowIntoResting ends the live show and runs the controller into the
// inter-show resting state.
func (n *lightsNight) endShowIntoResting() {
	n.advance(30 * time.Second)
	n.host.setIdle()
	n.tick()
	n.mustState("show completion", nightStateTransitionToResting)
	n.advance(time.Second)
	n.tick()
	n.mustState("enter resting", nightStateRestingIntershow)
}

func eventIndex(events []string, want string) int {
	for i, e := range events {
		if e == want {
			return i
		}
	}
	return -1
}

func TestLightsFade_GainIsFullBeforeEveryShowPlaylistStarts(t *testing.T) {
	n := newLightsNight(t, "")
	n.openToFirstShow()

	ev := n.events()
	gain, start := eventIndex(ev, "gain:player-01:100:0s"), eventIndex(ev, "start:halloween-show")
	if gain < 0 || start < 0 || gain > start {
		t.Fatalf("the first show must be preceded by gain 100 at once; events: %v", ev)
	}

	n.endShowIntoResting()
	b, _ := decodeNightBoundary(n.session().BoundaryJSON)
	n.advance(b.ExpectedAt.Sub(n.now) + time.Second)
	n.host.setIdle()
	n.tick()
	n.advance(time.Second)
	n.tick()
	n.mustState("second show launch", nightStateLive)

	ev = n.events()
	second := eventIndex(ev[start+1:], "start:halloween-show")
	secondGain := eventIndex(ev[start+1:], "gain:player-01:100:0s")
	if second < 0 || secondGain < 0 || secondGain > second {
		t.Fatalf("the second show must also be preceded by gain 100; events after the first launch: %v", ev[start+1:])
	}
	for _, e := range ev {
		if strings.HasPrefix(e, "gain:") && e != "gain:player-01:100:0s" {
			t.Fatalf("with no fade durations configured the only gain write is 100 before a show; got %q in %v", e, ev)
		}
	}
}

func TestLightsFade_FadeOutFinishesWhenRestingEnds(t *testing.T) {
	n := newLightsNight(t, `"lightsFadeOutMs": 20000, `)
	n.openToFirstShow()

	for _, e := range n.events() {
		if strings.Contains(e, ":0:") {
			t.Fatalf("the first show follows pre-show, which has no end to fade into: %q", e)
		}
	}

	n.endShowIntoResting()
	b, _ := decodeNightBoundary(n.session().BoundaryJSON)
	end := *b.ExpectedAt

	// Before E minus the fade, nothing is written.
	n.advance(end.Add(-30 * time.Second).Sub(n.now))
	n.host.setPlaying("halloween-resting", 0)
	n.tick()
	n.tick()
	n.mustState("before the fade window", nightStateRestingIntershow)
	before := len(n.events())

	// At E minus the fade the transition begins and the fade-out starts, taking
	// the time that remains until the resting sequence ends.
	n.advance(10 * time.Second)
	n.tick()
	n.mustState("fade window", nightStateTransitionToShow)
	n.tick()
	fadeOut := n.events()[before:]
	if len(fadeOut) != 1 || fadeOut[0] != "gain:player-01:0:20s" {
		t.Fatalf("expected one fade to 0 over 20s starting at E minus 20s; got %v", fadeOut)
	}
	n.tick()
	if got := n.events()[before:]; len(got) != 1 {
		t.Fatalf("the fade-out must be written once, got %v", got)
	}

	// Resting ends; the show then starts only after gain 100.
	n.advance(end.Sub(n.now) + time.Second)
	n.host.setIdle()
	n.tick()
	n.mustState("show launch", nightStateLive)
	ev := n.events()[before:]
	want := []string{"gain:player-01:0:20s", "gain:player-01:100:0s", "start:halloween-show"}
	if strings.Join(ev, ",") != strings.Join(want, ",") {
		t.Fatalf("events around the second show = %v, want %v", ev, want)
	}
}

func TestLightsFade_FadeInStartsWithRestingAfterAShow(t *testing.T) {
	n := newLightsNight(t, `"lightsFadeInMs": 8000, `)
	n.openToFirstShow()
	before := len(n.events())

	n.advance(30 * time.Second)
	n.host.setIdle()
	n.tick()
	n.mustState("show completion", nightStateTransitionToResting)
	n.advance(time.Second)
	n.tick()
	n.mustState("enter resting", nightStateRestingIntershow)

	got := n.events()[before:]
	want := []string{"gain:player-01:0:0s", "start:halloween-resting", "gain:player-01:100:8s"}
	if strings.Join(got, ",") != strings.Join(want, ",") {
		t.Fatalf("events after the show = %v, want %v: dark first, then resting starts, then the fade-in", got, want)
	}
}

func TestLightsFade_EachDurationOmittedMeansNoFadeThatWay(t *testing.T) {
	t.Run("fade-out only has no fade-in", func(t *testing.T) {
		n := newLightsNight(t, `"lightsFadeOutMs": 5000, `)
		n.openToFirstShow()
		before := len(n.events())
		n.endShowIntoResting()
		for _, e := range n.events()[before:] {
			if strings.HasPrefix(e, "gain:") {
				t.Fatalf("no fade-in was configured but the resting start wrote %q", e)
			}
		}
	})
	t.Run("fade-in only has no fade-out", func(t *testing.T) {
		n := newLightsNight(t, `"lightsFadeInMs": 5000, `)
		n.openToFirstShow()
		n.endShowIntoResting()
		b, _ := decodeNightBoundary(n.session().BoundaryJSON)
		before := len(n.events())
		n.advance(b.ExpectedAt.Sub(n.now) - time.Second)
		n.host.setPlaying("halloween-resting", 0)
		n.tick()
		n.tick()
		for _, e := range n.events()[before:] {
			if strings.HasPrefix(e, "gain:") {
				t.Fatalf("no fade-out was configured but the transition wrote %q", e)
			}
		}
	})
}

func TestLightsFade_FailedGainWriteIsReportedAndNeverBlocksTheShow(t *testing.T) {
	n := newLightsNight(t, "")
	n.failIf = func(f nightLightingFade) bool { return f.TargetPercent == 100 }

	n.host.setIdle()
	mustNightCommand(t, n.api, n.token, "prepare-site")
	mustNightCommand(t, n.api, n.token, "run-readiness")
	mustNightCommand(t, n.api, n.token, "start-preshow")
	n.tick()
	n.advance(time.Second)
	n.tick()
	mustNightCommand(t, n.api, n.token, "start-night")
	n.advance(time.Second)
	n.tick()

	n.mustState("launch with a failing gain write", nightStateLive)
	if eventIndex(n.events(), "start:halloween-show") < 0 {
		t.Fatalf("the show playlist was not started; events: %v", n.events())
	}
	if got := n.session(); got.Degraded {
		t.Fatalf("a failed gain write must not degrade the night: %q", got.DegradedReason)
	}

	all, _, err := n.st.ListEvents(context.Background(), 0, 0)
	if err != nil {
		t.Fatal(err)
	}
	var failures []store.EventRecord
	for _, e := range all {
		if e.Category == nightEventCategoryLightsGainFailed {
			failures = append(failures, e)
		}
	}
	if len(failures) != 1 {
		t.Fatalf("expected one reported gain failure, got %d", len(failures))
	}
	if !strings.Contains(failures[0].Summary, "player-01") {
		t.Fatalf("the report must name the instance: %q", failures[0].Summary)
	}

	// The plugin comes back: the next live tick restores the gain, once.
	n.mu.Lock()
	n.failIf = nil
	n.mu.Unlock()
	n.advance(time.Second)
	n.tick()
	n.advance(time.Second)
	n.tick()
	var gains int
	for _, e := range n.events() {
		if e == "gain:player-01:100:0s" {
			gains++
		}
	}
	if gains != 2 {
		t.Fatalf("expected the failed write and exactly one retry, got %d: %v", gains, n.events())
	}
}

func TestLightsFade_ReadinessNamesEachConfiguredFadeAndItsInstance(t *testing.T) {
	p := config.NightSessionPayload{
		ShowPlaylist:    config.NightSessionFPPPlaylist{FPPInstanceID: "show-fpp"},
		Resting:         config.NightSessionResting{FPPInstanceID: "rest-fpp"},
		LightsFadeOutMs: nightFadeMs(20000),
	}
	checks := nightCheckLightsFades(p)
	if len(checks) != 1 || checks[0].name != "lights:fade-out" ||
		!strings.Contains(checks[0].reason, "20 seconds") || !strings.Contains(checks[0].reason, "rest-fpp, show-fpp") {
		t.Fatalf("readiness must name the fade, its length and both instances: %+v", checks)
	}
	if got := nightCheckLightsFades(config.NightSessionPayload{}); len(got) != 0 {
		t.Fatalf("no fade configured must add no check: %+v", got)
	}
}

func TestLightsFade_GainWaitsForTheStartWhenRestingIsStillPlaying(t *testing.T) {
	n := newLightsNight(t, `"lightsFadeOutMs": 20000, `)
	n.openToFirstShow()
	n.endShowIntoResting()
	b, _ := decodeNightBoundary(n.session().BoundaryJSON)
	end := *b.ExpectedAt
	n.advance(end.Add(-20 * time.Second).Sub(n.now))
	n.host.setPlaying("halloween-resting", 0)
	n.tick()
	n.tick()
	before := len(n.events())

	// The resting playlist overruns its end, so the show replaces it.
	n.advance(end.Sub(n.now) + 2*time.Second)
	n.host.setPlaying("halloween-resting", 0)
	n.tick()
	n.mustState("show launch over a resting playlist still playing", nightStateLive)
	got := n.events()[before:]
	want := []string{"start:halloween-show", "gain:player-01:100:0s"}
	if strings.Join(got, ",") != strings.Join(want, ",") {
		t.Fatalf("events = %v, want %v: lighting the resting look again before it is replaced would show it at full brightness", got, want)
	}
}
