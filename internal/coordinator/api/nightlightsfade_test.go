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
	"github.com/showmeshsystems/showmesh/pkg/observation"
)

// lightsNight runs a real night through the controller's own ticks against a
// fake FPP host, logging gain writes and playlist starts in one ordered list.
// Gain writes run off the tick, so the fake start handler waits briefly for a
// write it expects, the way a real player takes longer than the tick's own
// goroutine launch.
type lightsNight struct {
	t     *testing.T
	now   time.Time
	st    *store.Store
	h     *handlers
	api   *API
	host  *fakeFPPHost
	token string

	mu            sync.Mutex
	log           []string
	gains         int
	gainsAtStart  int
	awaitShowGain bool
	awaitRestGain bool
	noPosition    bool
	failIf        func(f nightLightingFade, instance string) bool
	hang          bool
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

func (n *lightsNight) gainCount() int {
	n.mu.Lock()
	defer n.mu.Unlock()
	return n.gains
}

// tick runs one controller tick and then lets the gain writes it started finish.
func (n *lightsNight) tick() {
	n.h.nightTick(context.Background(), n.now)
	n.h.nightLightsGain.wait()
}

func (n *lightsNight) advance(d time.Duration)           { n.now = n.now.Add(d) }
func (n *lightsNight) session() store.NightSessionRecord { return mustGetCurrentSession(n.t, n.st) }

func (n *lightsNight) mustState(step, want string) {
	n.t.Helper()
	if got := n.session(); got.State != want {
		n.t.Fatalf("%s: state = %q, want %q (degraded=%v %q)", step, got.State, want, got.Degraded, got.DegradedReason)
	}
}

func (n *lightsNight) setFail(f func(nightLightingFade, string) bool) {
	n.mu.Lock()
	defer n.mu.Unlock()
	n.failIf = f
}

func (n *lightsNight) failedEvents() []store.EventRecord {
	all, _, err := n.st.ListEvents(context.Background(), 0, 0)
	if err != nil {
		n.t.Fatal(err)
	}
	var out []store.EventRecord
	for _, e := range all {
		if e.Category == nightEventCategoryLightsGainFailed {
			out = append(out, e)
		}
	}
	return out
}

func lightsBody(extra, showCues, restingCues string) string {
	body := strings.Replace(nightFullNightBody, `"enterShow"`, extra+`"enterShow"`, 1)
	if showCues != "" {
		body = strings.Replace(body, `"enterShow": {"cues": []`, `"enterShow": {"cues": [`+showCues+`]`, 1)
	}
	if restingCues != "" {
		body = strings.Replace(body, `"enterResting": {"cues": []`, `"enterResting": {"cues": [`+restingCues+`]`, 1)
	}
	return body
}

const lightsQuietCue = `{"name": "quiet", "role": "other", "action": "lights-cue", "offsetMs": -30000}`

func newLightsNight(t *testing.T, extra string) *lightsNight {
	t.Helper()
	return newLightsNightWithCues(t, extra, "", "")
}

func newLightsNightWithCues(t *testing.T, extra, showCues, restingCues string) *lightsNight {
	t.Helper()
	n := &lightsNight{t: t, now: testNow, awaitShowGain: true, awaitRestGain: strings.Contains(extra, "lightsFadeInMs")}
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
			if len(body.Args) == 0 {
				break
			}
			wait := (body.Args[0] == "halloween-show" && n.awaitShowGain) || (body.Args[0] == "halloween-resting" && n.awaitRestGain)
			if wait {
				deadline := time.Now().Add(time.Second)
				for time.Now().Before(deadline) && n.gainCount() <= n.gainsAtStart {
					time.Sleep(time.Millisecond)
				}
				n.mu.Lock()
				n.gainsAtStart = n.gains
				n.mu.Unlock()
			}
			n.record("start:" + body.Args[0])
			n.mu.Lock()
			noPos := n.noPosition && body.Args[0] == "halloween-resting"
			n.mu.Unlock()
			if noPos {
				n.host.setPlayingNoPosition(body.Args[0])
			} else {
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
		nightGainWriter: func(ctx context.Context, target config.ShowActionTarget, fade nightLightingFade, _ string) (fppcommand.TransitionGainOutcome, error) {
			n.mu.Lock()
			fail := n.failIf != nil && n.failIf(fade, target.InstanceID)
			hang := n.hang
			n.gains++
			n.mu.Unlock()
			n.record(fmt.Sprintf("gain:%s:%d:%ds", target.InstanceID, fade.TargetPercent, fade.FadeSeconds))
			if hang {
				<-ctx.Done()
				return fppcommand.TransitionGainOutcome{}, ctx.Err()
			}
			if fail {
				return fppcommand.TransitionGainOutcome{}, errors.New("the plugin did not answer")
			}
			return fppcommand.TransitionGainOutcome{Applied: true}, nil
		},
	}

	mustPutShow(t, api, adminToken, "halloween-2026", `{"name":"halloween-2026"}`)
	mustPutShowAction(t, api, adminToken, "lights-cue", `{"show":"halloween-2026","label":"Quiet","safetyClass":"none","idempotent":true,"target":{"integration":"fpp","instanceId":"player-01","primitive":"setVolume","params":{"volume":40}}}`)
	mustCreateNightSessionFSEQAsset(t, st, backend, "halloween-2026", "resting-loop", "player-01")
	mustPutNightSession(t, api, adminToken, "halloween-main", lightsBody(extra, showCues, restingCues))
	mustActivateNightSession(t, api, adminToken, "halloween-main")
	return n
}

func (f *fakeFPPHost) setPlayingNoPosition(playlist string) {
	at := f.now()
	f.obs.obs = []observation.Observation{
		statusObservation("player-01", fppStatusValuePlaying, at),
		playlistNameObservation("player-01", playlist, at),
	}
}

func (n *lightsNight) prepareAndStartPreshow() {
	n.host.setIdle()
	mustNightCommand(n.t, n.api, n.token, "prepare-site")
	mustNightCommand(n.t, n.api, n.token, "run-readiness")
	mustNightCommand(n.t, n.api, n.token, "start-preshow")
	n.tick()
	n.advance(time.Second)
	n.tick()
}

// openToFirstShow takes the night through pre-show and start-night, then
// launches the first show.
func (n *lightsNight) openToFirstShow() {
	n.prepareAndStartPreshow()
	mustNightCommand(n.t, n.api, n.token, "start-night")
	for i := 0; i < 40 && n.session().State != nightStateLive; i++ {
		n.advance(time.Second)
		n.host.setPlaying("halloween-resting", 0)
		n.tick()
	}
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

// restingEnd reads E back from the stored boundary the loop recorded.
func (n *lightsNight) restingEnd() time.Time {
	b, ok := decodeNightBoundary(n.session().BoundaryJSON)
	if !ok || b.ExpectedAt == nil {
		n.t.Fatalf("no resting end recorded: %+v", b)
	}
	return *b.ExpectedAt
}

func eventIndex(events []string, want string) int {
	for i, e := range events {
		if e == want {
			return i
		}
	}
	return -1
}

func joined(ev []string) string { return strings.Join(ev, ",") }

func TestLightsFade_GainIsFullBeforeEveryShowPlaylistStarts(t *testing.T) {
	n := newLightsNight(t, "")
	n.openToFirstShow()

	ev := n.events()
	gain, start := eventIndex(ev, "gain:player-01:100:0s"), eventIndex(ev, "start:halloween-show")
	if gain < 0 || start < 0 || gain > start {
		t.Fatalf("the first show must be preceded by gain 100 at once; events: %v", ev)
	}

	n.endShowIntoResting()
	n.advance(n.restingEnd().Sub(n.now) + time.Second)
	n.host.setIdle()
	n.tick()
	n.advance(time.Second)
	n.tick()
	n.mustState("second show launch", nightStateLive)

	rest := n.events()[start+1:]
	second, secondGain := eventIndex(rest, "start:halloween-show"), eventIndex(rest, "gain:player-01:100:0s")
	if second < 0 || secondGain < 0 || secondGain > second {
		t.Fatalf("the second show must also be preceded by gain 100; events after the first launch: %v", rest)
	}
	for _, e := range n.events() {
		if strings.HasPrefix(e, "gain:") && e != "gain:player-01:100:0s" {
			t.Fatalf("with no fade durations configured the only gain write is 100 before a show; got %q in %v", e, n.events())
		}
	}
}

func TestLightsFade_FadeOutFinishesWhenRestingEnds(t *testing.T) {
	n := newLightsNight(t, `"lightsFadeOutMs": 20000, `)
	n.openToFirstShow()
	n.endShowIntoResting()
	end := n.restingEnd()

	n.advance(end.Add(-30 * time.Second).Sub(n.now))
	n.host.setPlaying("halloween-resting", 0)
	n.tick()
	n.tick()
	n.mustState("before the fade window", nightStateRestingIntershow)
	before := len(n.events())

	n.advance(10 * time.Second)
	n.tick()
	n.mustState("fade window", nightStateTransitionToShow)
	n.tick()
	fadeOut := n.events()[before:]
	if joined(fadeOut) != "gain:player-01:0:20s" {
		t.Fatalf("expected one fade to 0 over 20s starting at E minus 20s; got %v", fadeOut)
	}
	n.tick()
	if got := n.events()[before:]; len(got) != 1 {
		t.Fatalf("the fade-out must be written once, got %v", got)
	}

	n.advance(end.Sub(n.now) + time.Second)
	n.host.setIdle()
	n.tick()
	n.mustState("show launch", nightStateLive)
	want := "gain:player-01:0:20s,gain:player-01:100:0s,start:halloween-show"
	if got := joined(n.events()[before:]); got != want {
		t.Fatalf("events around the second show = %v, want %v", got, want)
	}
}

// A cue that fires ahead of the fade window commits the show early; the fade
// must still run.
func TestLightsFade_FadeOutRunsWhenAnEnterShowCueFiresAheadOfTheWindow(t *testing.T) {
	for _, tc := range []struct{ name, cue string }{
		{"other-role cue", lightsQuietCue},
		{"lighting cue without a cue fade", `{"name": "lamps", "role": "lighting", "action": "lights-cue", "offsetMs": -30000}`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			n := newLightsNightWithCues(t, `"lightsFadeOutMs": 10000, `, tc.cue, "")
			n.openToFirstShow()
			n.endShowIntoResting()
			end := n.restingEnd()

			n.advance(end.Add(-30 * time.Second).Sub(n.now))
			n.host.setPlaying("halloween-resting", 0)
			n.tick()
			n.mustState("transition begins at the cue lead", nightStateTransitionToShow)
			n.tick()
			if !n.session().ShowCommitted {
				t.Fatal("the cue ahead of the window should have committed the show")
			}
			before := len(n.events())

			n.advance(20 * time.Second)
			n.tick()
			if got := joined(n.events()[before:]); got != "gain:player-01:0:10s" {
				t.Fatalf("the fade-out must run at E minus 10s even after the show committed; got %q", got)
			}
		})
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

	want := "gain:player-01:0:0s,start:halloween-resting,gain:player-01:100:8s"
	if got := joined(n.events()[before:]); got != want {
		t.Fatalf("events after the show = %v, want %v: dark first, then resting starts, then the fade-in", got, want)
	}
}

// The fade-in keys on the resting start being dispatched, not on position
// evidence arriving afterwards.
func TestLightsFade_FadeInDoesNotWaitForRestingToBeConfirmed(t *testing.T) {
	n := newLightsNight(t, `"lightsFadeInMs": 8000, `)
	n.openToFirstShow()
	n.mu.Lock()
	n.noPosition = true
	n.mu.Unlock()
	before := len(n.events())

	n.advance(30 * time.Second)
	n.host.setIdle()
	n.tick()
	n.advance(time.Second)
	n.tick()
	n.mustState("resting started but not yet confirmed", nightStateTransitionToResting)
	if got := joined(n.events()[before:]); got != "gain:player-01:0:0s,start:halloween-resting,gain:player-01:100:8s" {
		t.Fatalf("the fade-in must start with the dispatch, got %v", got)
	}
}

func TestLightsFade_FadeInIsRetriedUntilItLands(t *testing.T) {
	n := newLightsNight(t, `"lightsFadeInMs": 8000, `)
	n.openToFirstShow()
	fails := 2
	n.setFail(func(f nightLightingFade, _ string) bool {
		if f.TargetPercent == 100 && f.FadeSeconds == 8 && fails > 0 {
			fails--
			return true
		}
		return false
	})
	n.advance(30 * time.Second)
	n.host.setIdle()
	n.tick()
	n.advance(time.Second)
	n.tick()
	n.mustState("enter resting", nightStateRestingIntershow)
	for i := 0; i < 4; i++ {
		n.advance(nightLightsRetryBackoff)
		n.tick()
	}
	var ups int
	for _, e := range n.events() {
		if e == "gain:player-01:100:8s" {
			ups++
		}
	}
	if ups != 3 {
		t.Fatalf("expected two failed writes and one that lands, got %d: %v", ups, n.events())
	}
	if !n.h.nightLightsGain.stepDone(nightLightsKey(n.session(), nightLightsStepFadeIn)) {
		t.Fatal("the fade-in never landed")
	}
	failures := n.failedEvents()
	if len(failures) != 1 || !strings.Contains(failures[0].Summary, "may be dark") || !strings.Contains(failures[0].Summary, "Set the brightness") {
		t.Fatalf("a failed fade-in must say the lights may be dark and what to do, once: %+v", failures)
	}
}

// A session that degrades while the gain is 0 gets one write back to 100.
func TestLightsFade_DegradedSessionGetsTheGainBack(t *testing.T) {
	n := newLightsNight(t, `"lightsFadeInMs": 8000, `)
	n.openToFirstShow()
	n.setFail(func(f nightLightingFade, _ string) bool { return f.FadeSeconds > 0 })
	n.mu.Lock()
	n.noPosition = true
	n.mu.Unlock()
	n.advance(30 * time.Second)
	n.host.setIdle()
	n.tick()
	n.advance(time.Second)
	n.tick()
	before := len(n.events())

	n.advance(nightStartConfirmWindow + time.Second)
	n.tick()
	if !n.session().Degraded {
		t.Fatal("an unconfirmed resting start should degrade the session")
	}
	n.tick()
	if got := n.events()[before:]; eventIndex(got, "gain:player-01:100:0s") < 0 {
		t.Fatalf("the degrade must write the gain back to 100, got %v", got)
	}
}

func TestLightsFade_AbandonedTransitionRestoresTheGain(t *testing.T) {
	n := newLightsNight(t, `"lightsFadeOutMs": 20000, `)
	n.openToFirstShow()
	n.endShowIntoResting()
	end := n.restingEnd()
	n.advance(end.Add(-20 * time.Second).Sub(n.now))
	n.host.setPlaying("halloween-resting", 0)
	n.tick()
	n.tick()
	if eventIndex(n.events(), "gain:player-01:0:20s") < 0 {
		t.Fatalf("the fade-out should have been written: %v", n.events())
	}
	before := len(n.events())

	n.advance(2 * time.Second)
	n.host.setPlaying("something-else", 0)
	n.tick()
	n.mustState("transition abandoned", nightStateRestingIntershow)
	if got := joined(n.events()[before:]); got != "gain:player-01:100:0s" {
		t.Fatalf("an abandoned transition must restore the gain, got %q", got)
	}
}

func TestLightsFade_EndOfNightAndNextPreshowRestoreTheGain(t *testing.T) {
	n := newLightsNight(t, `"lightsFadeOutMs": 20000, `)
	n.openToFirstShow()
	n.endShowIntoResting()
	end := n.restingEnd()
	n.advance(end.Add(-20 * time.Second).Sub(n.now))
	n.host.setPlaying("halloween-resting", 0)
	n.tick()
	n.tick()
	before := len(n.events())

	// An emergency stop writes nothing.
	if _, _ = n.h.nightEmergencyStopHold(context.Background(), n.now, identity.AuditEntry{PrincipalName: "operator-1"}); n.session().StopHold == nil {
		t.Fatal("the emergency stop did not hold the night")
	}
	n.advance(time.Second)
	n.tick()
	if got := n.events()[before:]; len(got) != 0 {
		t.Fatalf("a stop must not write the gain, got %v", got)
	}

	// The session ends inside the fade window: the gain comes back.
	mustNightCommand(t, n.api, n.token, "end-session")
	n.advance(time.Second)
	n.tick()
	n.mustState("ended", nightStateStopped)
	n.tick()
	if got := joined(n.events()[before:]); got != "gain:player-01:100:0s" {
		t.Fatalf("ending the night must restore the gain once, got %q", got)
	}
}

func TestLightsFade_PreshowStartsAtFullBrightnessWhenFadesAreConfigured(t *testing.T) {
	n := newLightsNight(t, `"lightsFadeInMs": 8000, `)
	n.awaitRestGain = false
	n.prepareAndStartPreshow()
	if eventIndex(n.events(), "gain:player-01:100:0s") < 0 {
		t.Fatalf("pre-show must write the gain to 100: %v", n.events())
	}
	plain := newLightsNight(t, "")
	plain.prepareAndStartPreshow()
	if len(plain.events()) != 1 || plain.events()[0] != "start:halloween-resting" {
		t.Fatalf("a night with no fades writes no gain at pre-show: %v", plain.events())
	}
}

// A writer that hangs must not delay the show or the resting start.
func TestLightsFade_AHangingGainWriteNeverDelaysTheLaunch(t *testing.T) {
	n := newLightsNight(t, `"lightsFadeInMs": 8000, `)
	n.awaitShowGain, n.awaitRestGain = false, false
	n.mu.Lock()
	n.hang = true
	n.mu.Unlock()
	n.prepareAndStartPreshow()
	mustNightCommand(t, n.api, n.token, "start-night")
	n.advance(time.Second)

	began := time.Now()
	n.h.nightTick(context.Background(), n.now)
	took := time.Since(began)
	n.mustState("show launch with every gain write hanging", nightStateLive)
	if eventIndex(n.events(), "start:halloween-show") < 0 {
		t.Fatalf("the show playlist was not dispatched on the same tick: %v", n.events())
	}
	if took >= nightLightsGainTimeout/2 {
		t.Fatalf("the launch tick took %s with a hanging gain write", took)
	}

	// Resting after the show is the same.
	n.advance(30 * time.Second)
	n.host.setIdle()
	n.h.nightTick(context.Background(), n.now)
	n.advance(time.Second)
	began = time.Now()
	n.h.nightTick(context.Background(), n.now)
	if eventIndex(n.events(), "start:halloween-resting") < 0 || time.Since(began) >= nightLightsGainTimeout/2 {
		t.Fatalf("resting start was delayed or not dispatched: %v", n.events())
	}
	n.h.nightLightsGain.wait()
}

// One dead player must not make a healthy player's landed write repeat.
func TestLightsFade_CompletionIsTrackedPerInstance(t *testing.T) {
	n := newLightsNight(t, "")
	n.setFail(func(_ nightLightingFade, instance string) bool { return instance == "dead" })
	payload := config.NightSessionPayload{
		Resting:      config.NightSessionResting{FPPInstanceID: "healthy"},
		ShowPlaylist: config.NightSessionFPPPlaylist{FPPInstanceID: "dead"},
	}
	rec := store.NightSessionRecord{ID: "sess-x", Cycle: 1}
	n.h.nightLightsStart(context.Background(), n.now, rec, payload, nightLightsStep{step: nightLightsStepShowGain, target: 100})
	n.h.nightLightsGain.wait()
	for i := 0; i < 3; i++ {
		n.advance(nightLightsRetryBackoff)
		n.h.nightLightsAdvance(context.Background(), n.now, rec)
		n.h.nightLightsGain.wait()
	}
	var healthy, dead int
	for _, e := range n.events() {
		switch e {
		case "gain:healthy:100:0s":
			healthy++
		case "gain:dead:100:0s":
			dead++
		}
	}
	if healthy != 1 || dead != 4 {
		t.Fatalf("healthy written %d times (want 1), dead %d (want 4): %v", healthy, dead, n.events())
	}
	if got := len(n.failedEvents()); got != 1 {
		t.Fatalf("a dead player is reported once per night, got %d reports", got)
	}
}

func TestLightsFade_AFailingPlayerIsReportedOncePerNightNotPerShow(t *testing.T) {
	n := newLightsNight(t, "")
	n.setFail(func(nightLightingFade, string) bool { return true })
	n.openToFirstShow()
	n.endShowIntoResting()
	n.advance(n.restingEnd().Sub(n.now) + time.Second)
	n.host.setIdle()
	n.tick()
	n.advance(time.Second)
	n.tick()
	n.mustState("second show launch", nightStateLive)
	if got := len(n.failedEvents()); got != 1 {
		t.Fatalf("expected one report for the night, got %d", got)
	}
	if got := n.failedEvents()[0].Summary; !strings.Contains(got, "player-01") || strings.Contains(got, "still started") {
		t.Fatalf("report wording: %q", got)
	}
}

func TestLightsFade_AFailedGainWriteNeverBlocksTheShow(t *testing.T) {
	n := newLightsNight(t, "")
	n.setFail(func(f nightLightingFade, _ string) bool { return f.TargetPercent == 100 })
	n.openToFirstShow()
	if eventIndex(n.events(), "start:halloween-show") < 0 {
		t.Fatalf("the show playlist was not started; events: %v", n.events())
	}
	if n.session().Degraded {
		t.Fatalf("a failed gain write must not degrade the night: %q", n.session().DegradedReason)
	}
	n.setFail(nil)
	n.advance(nightLightsRetryBackoff)
	n.tick()
	if !n.h.nightLightsGain.stepDone(nightLightsKey(n.session(), nightLightsStepShowGain)) {
		t.Fatal("the show gain was not retried until it landed")
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
		before := len(n.events())
		n.advance(n.restingEnd().Sub(n.now) - time.Second)
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

func overrunScenario(n *lightsNight, fadeOutAt time.Duration) {
	n.openToFirstShow()
	n.endShowIntoResting()
	end := n.restingEnd()
	n.advance(end.Add(-fadeOutAt).Sub(n.now))
	n.host.setPlaying("halloween-resting", 0)
	n.tick()
	n.tick()
}

func TestLightsFade_GainWaitsForTheStartWhenRestingIsStillPlaying(t *testing.T) {
	for _, tc := range []struct {
		name, extra, cues string
		fadeAt            time.Duration
	}{
		{"setting", `"lightsFadeOutMs": 20000, `, "", 20 * time.Second},
		{"cue fade without the setting", "", `{"name": "dim", "role": "lighting", "action": "lights-cue", "offsetMs": -20000, "fadeDurationMs": 3000}`, 20 * time.Second},
	} {
		t.Run(tc.name, func(t *testing.T) {
			n := newLightsNightWithCues(t, tc.extra, tc.cues, "")
			n.awaitShowGain = false
			overrunScenario(n, tc.fadeAt)
			before := len(n.events())

			n.advance(n.restingEnd().Sub(n.now) + 2*time.Second)
			n.host.setPlaying("halloween-resting", 0)
			n.tick()
			n.mustState("show launch over a resting playlist still playing", nightStateLive)
			if got := joined(n.events()[before:]); got != "start:halloween-show,gain:player-01:100:0s" {
				t.Fatalf("events = %v: lighting the resting look before it is replaced would show it at full brightness", got)
			}
		})
	}
}

func TestLightsFade_ReadinessNamesEachFadeAndWarnsWhenTheyDoNotFit(t *testing.T) {
	n := newLightsNight(t, "")
	p := config.NightSessionPayload{
		Show:            "halloween-2026",
		ShowPlaylist:    config.NightSessionFPPPlaylist{FPPInstanceID: "show-fpp"},
		Resting:         config.NightSessionResting{FPPInstanceID: "rest-fpp", TimelineAsset: config.NightSessionAssetRef{Show: "halloween-2026", Sequence: "resting-loop", Target: "player-01"}},
		LightsFadeOutMs: nightFadeMs(20000),
	}
	checks := n.h.nightCheckLightsFades(context.Background(), p)
	if len(checks) != 1 || checks[0].name != "lights:fade-out" ||
		!strings.Contains(checks[0].reason, "20 seconds") || !strings.Contains(checks[0].reason, "rest-fpp, show-fpp") ||
		!strings.Contains(checks[0].reason, "every show after the first") {
		t.Fatalf("readiness must name the fade, its length and both instances: %+v", checks)
	}
	p.LightsFadeInMs = nightFadeMs(86_400_000)
	checks = n.h.nightCheckLightsFades(context.Background(), p)
	var overlap *nightReadinessCheck
	for i := range checks {
		if checks[i].name == "lights:fade-length" {
			overlap = &checks[i]
		}
	}
	if overlap == nil || overlap.health != nightHealthDegraded() {
		t.Fatalf("fades longer than the resting sequence must warn without blocking: %+v", checks)
	}
	if got := n.h.nightCheckLightsFades(context.Background(), config.NightSessionPayload{}); len(got) != 0 {
		t.Fatalf("no fade configured must add no check: %+v", got)
	}
}
