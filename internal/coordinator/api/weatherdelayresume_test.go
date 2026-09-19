package api

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"

	"github.com/showmeshsystems/showmesh/internal/coordinator/broker"
	"github.com/showmeshsystems/showmesh/internal/coordinator/config"
	"github.com/showmeshsystems/showmesh/internal/coordinator/identity"
	"github.com/showmeshsystems/showmesh/internal/coordinator/inventory"
	"github.com/showmeshsystems/showmesh/internal/coordinator/store"
	"github.com/showmeshsystems/showmesh/pkg/mqttproto"
	"github.com/showmeshsystems/showmesh/pkg/observation"
)

// recordingWeatherDelayPublisher records every retained publish and every
// node command, and answers no node command.
type recordingWeatherDelayPublisher struct {
	mu        sync.Mutex
	retained  [][]byte
	cmdTopics []string
	actions   []string
}

func (p *recordingWeatherDelayPublisher) Publish(_ context.Context, topic string, _ byte, _ bool, payload []byte) error {
	p.mu.Lock()
	defer p.mu.Unlock()
	if topic == mqttproto.WeatherDelayTopic() {
		p.retained = append(p.retained, payload)
	}
	return nil
}

func (p *recordingWeatherDelayPublisher) AwaitResponse(_ context.Context, req broker.ResponseRequest) (broker.Message, error) {
	var env struct {
		Payload struct {
			Action string `json:"action"`
		} `json:"payload"`
	}
	_ = json.Unmarshal(req.PublishPayload, &env)
	p.mu.Lock()
	p.cmdTopics = append(p.cmdTopics, req.PublishTopic)
	p.actions = append(p.actions, env.Payload.Action)
	p.mu.Unlock()
	return broker.Message{}, broker.ErrResponseDeadlineExceeded
}

// countingNodeAddrs reports a listener for every node and counts lookups,
// so a test can prove resume never takes the direct HTTP path.
type countingNodeAddrs struct {
	mu    sync.Mutex
	calls int
}

func (c *countingNodeAddrs) InboundListener(string) (string, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.calls++
	return "127.0.0.1:1", true
}

type resumeHarness struct {
	t        *testing.T
	st       *store.Store
	api      *API
	h        *handlers
	token    string
	pub      *recordingWeatherDelayPublisher
	addrs    *countingNodeAddrs
	obs      *mutableObservationLister
	mu       sync.Mutex
	commands []string
	args     [][]string
	now      time.Time
}

func (r *resumeHarness) fppCommands() ([]string, [][]string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]string(nil), r.commands...), append([][]string(nil), r.args...)
}

func newResumeHarness(t *testing.T) *resumeHarness {
	t.Helper()
	return newResumeHarnessWith(t, func(st *store.Store) WeatherDelayStore { return st })
}

func newResumeHarnessWith(t *testing.T, weatherDelay func(*store.Store) WeatherDelayStore) *resumeHarness {
	t.Helper()
	r := &resumeHarness{t: t, now: time.Date(2026, 10, 31, 20, 30, 0, 0, time.UTC), obs: &mutableObservationLister{}}
	cmdSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		var body struct {
			Command string   `json:"command"`
			Args    []string `json:"args"`
		}
		_ = json.NewDecoder(req.Body).Decode(&body)
		r.mu.Lock()
		r.commands = append(r.commands, body.Command)
		r.args = append(r.args, body.Args)
		r.mu.Unlock()
		if body.Command == "Start Playlist" && len(body.Args) > 0 {
			r.obs.add(
				statusObservation("player-01", fppStatusValuePlaying, r.now),
				playlistNameObservation("player-01", body.Args[0], r.now),
				positionMSObservation("player-01", 0, r.now),
			)
		}
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("Playlist Starting"))
	}))
	t.Cleanup(cmdSrv.Close)

	svc, st, _ := newTestIdentityServiceWithStore(t, func() time.Time { return r.now })
	admin := mustCreatePrincipal(t, svc, "admin-1", identity.RoleAdmin)
	r.st, r.token = st, mustIssueToken(t, svc, admin.ID)
	r.pub, r.addrs = &recordingWeatherDelayPublisher{}, &countingNodeAddrs{}

	nodePayload, err := config.EncodeAudioNodePayload(config.AudioNodePayload{
		ProgramRoute: "usb-interface", ProgramChannels: []int{1, 2},
		ClockDomain: "single-interface", ClockDomainProvenance: "test fixture",
	})
	if err != nil {
		t.Fatalf("encode audio node payload: %v", err)
	}
	putConfigForTest(t, st, config.AudioNodeConfigKind, "audio-01", nodePayload)

	payloadJSON, err := config.EncodeNightSessionPayload(config.NightSessionPayload{
		Show: "halloween-2026", Label: "test",
		ShowPlaylist: config.NightSessionFPPPlaylist{FPPInstanceID: "player-01", Playlist: "halloween-show"},
		Resting:      config.NightSessionResting{FPPInstanceID: "player-01", Playlist: "halloween-resting", EndOfNightPlaylist: "halloween-resting"},
	})
	if err != nil {
		t.Fatalf("encode night payload: %v", err)
	}
	if _, err := st.CreateConfigObject(context.Background(), config.NightSessionConfigKind, "halloween-main"); err != nil {
		t.Fatalf("create config object: %v", err)
	}
	if _, err := st.CreateConfigRevision(context.Background(), store.ConfigRevisionRecord{
		Kind: config.NightSessionConfigKind, ObjectID: "halloween-main", Revision: 1, PayloadJSON: payloadJSON, Source: "api",
	}); err != nil {
		t.Fatalf("create config revision: %v", err)
	}

	deps := Dependencies{
		Nodes:        &fakeNodeLister{views: []inventory.NodeView{{NodeID: "render-01"}}},
		Observations: r.obs, Events: &fakeEventReader{}, Collectors: &fakeCollectorStatusLister{},
		Identity: svc, Config: st, Commands: st, NightSessions: st, WeatherDelay: weatherDelay(st),
		FPP:                   &fakeFPPLister{views: []FPPInstanceView{{InstanceID: "player-01", Endpoint: cmdSrv.URL}}},
		WeatherDelayPublisher: r.pub, WeatherDelayNodeAddrs: r.addrs,
	}.withDefaults()
	clock := func() time.Time { return r.now }
	r.api = New(deps, Options{Clock: clock, Logger: testLogger()})
	opts := Options{}.withDefaults()
	r.h = &handlers{deps: deps, clock: clock, logger: testLogger(),
		fppCommandConfirmDeadline: opts.FPPCommandConfirmDeadline, fppCommandPollInterval: opts.FPPCommandPollInterval}
	return r
}

func (r *resumeHarness) createSession(rec store.NightSessionRecord) {
	r.t.Helper()
	rec.ID, rec.ConfigObjectID, rec.ConfigRevision = "sess-1", "halloween-main", 1
	if err := r.st.CreateNightSession(context.Background(), rec, r.now); err != nil {
		r.t.Fatalf("create night session: %v", err)
	}
}

func (r *resumeHarness) resume() {
	r.t.Helper()
	auth := map[string]string{"Authorization": "Bearer " + r.token}
	resp, body := doRawRequest(r.t, r.api.Handler, newJSONRequest(r.t, http.MethodPost, "/api/v1/weather-delay/resume", `{"idempotencyKey":"resume-1"}`, auth))
	if resp.StatusCode != http.StatusOK {
		r.t.Fatalf("resume: status = %d, want 200; body: %s", resp.StatusCode, body)
	}
}

func (r *resumeHarness) assertNoResumePlaylist() {
	r.t.Helper()
	cmds, _ := r.fppCommands()
	for _, c := range cmds {
		if c != "Start Playlist" {
			r.t.Fatalf("FPP received %q; after a weather delay only Start Playlist may be sent", c)
		}
	}
}

func showAnchorAt(t time.Time) string {
	return encodeNightContentAnchor(nightContentAnchor{
		Purpose: nightAnchorPurposeShow, FPPInstanceID: "player-01", Playlist: "halloween-show",
		DispatchedAt: t, ObservedAt: t, PositionMSKnown: true,
	})
}

func restingAnchorAt(purpose string, t time.Time) string {
	return encodeNightContentAnchor(nightContentAnchor{
		Purpose: purpose, FPPInstanceID: "player-01", Playlist: "halloween-resting",
		DispatchedAt: t, ObservedAt: t, PositionMSKnown: true, DurationMS: 300000,
	})
}

func TestWeatherDelayResumeRestartsLiveShowFromTheTop(t *testing.T) {
	r := newResumeHarness(t)
	liveAt := r.now.Add(-10 * time.Minute)
	r.createSession(store.NightSessionRecord{State: nightStateLive, StateEnteredAt: liveAt, Cycle: 3, ShowCommitted: true, ContentAnchorJSON: showAnchorAt(liveAt)})
	if err := r.st.OpenNightCycleOutcome(context.Background(), "sess-1", 3, liveAt); err != nil {
		t.Fatalf("open cycle outcome: %v", err)
	}
	setWeatherDelayActive(t, r.st)

	r.resume()

	got := mustGetCurrentSession(t, r.st)
	if got.State != nightStateTransitionToShow || got.ShowCommitted || got.Cycle != 4 || got.ContentAnchorJSON != "" {
		t.Fatalf("session after resume = state %q committed %v cycle %d anchor %q; want transition-to-show, uncommitted, cycle 4, no anchor",
			got.State, got.ShowCommitted, got.Cycle, got.ContentAnchorJSON)
	}
	outcomes, err := r.st.ListNightCycleOutcomes(context.Background(), "sess-1")
	if err != nil || len(outcomes) != 1 || outcomes[0].Outcome != store.NightCycleOutcomeInterrupted || outcomes[0].EndedAt == nil {
		t.Fatalf("cycle outcomes = %+v (err %v), want cycle 3 closed interrupted", outcomes, err)
	}

	r.obs.set([]observation.Observation{statusObservation("player-01", fppStatusValueIdle, r.now)})
	r.h.nightTick(context.Background(), r.now)

	cmds, args := r.fppCommands()
	if len(cmds) != 1 || cmds[0] != "Start Playlist" || len(args[0]) == 0 || args[0][0] != "halloween-show" {
		t.Fatalf("FPP commands = %v %v, want exactly Start Playlist halloween-show", cmds, args)
	}
	r.assertNoResumePlaylist()
	if got := mustGetCurrentSession(t, r.st); got.State != nightStateLive || got.Cycle != 4 {
		t.Fatalf("session after the launch tick = %q cycle %d, want live cycle 4", got.State, got.Cycle)
	}
}

func TestWeatherDelayResumeReturnsPreshowToRestingNotShow(t *testing.T) {
	r := newResumeHarness(t)
	enteredAt := r.now.Add(-10 * time.Minute)
	r.createSession(store.NightSessionRecord{State: nightStatePreshow, StateEnteredAt: enteredAt, ContentAnchorJSON: restingAnchorAt(nightAnchorPurposeRestingRepeat, enteredAt)})
	setWeatherDelayActive(t, r.st)

	r.resume()

	got := mustGetCurrentSession(t, r.st)
	if got.State != nightStatePreshow || got.ContentAnchorJSON != "" {
		t.Fatalf("session after resume = state %q anchor %q; want preshow with its resting anchor cleared", got.State, got.ContentAnchorJSON)
	}
	r.obs.set([]observation.Observation{statusObservation("player-01", fppStatusValueIdle, r.now)})
	r.h.nightTick(context.Background(), r.now)

	cmds, args := r.fppCommands()
	if len(cmds) != 1 || args[0][0] != "halloween-resting" {
		t.Fatalf("FPP commands = %v %v, want exactly Start Playlist halloween-resting", cmds, args)
	}
	r.assertNoResumePlaylist()
	if got := mustGetCurrentSession(t, r.st); got.State != nightStatePreshow {
		t.Fatalf("state = %q, want preshow: resume must not push a preshow session into the show", got.State)
	}
}

func TestWeatherDelayResumeKeepsRestingIntershowInResting(t *testing.T) {
	r := newResumeHarness(t)
	enteredAt := r.now.Add(-2 * time.Minute)
	e := enteredAt.Add(5 * time.Minute)
	r.createSession(store.NightSessionRecord{
		State: nightStateRestingIntershow, StateEnteredAt: enteredAt, Cycle: 2,
		ContentAnchorJSON: restingAnchorAt(nightAnchorPurposeRestingOneShot, enteredAt),
		BoundaryJSON:      encodeNightBoundary(nightBoundary{State: nightBoundaryStateArmed, ExpectedAt: &e}),
	})
	setWeatherDelayActive(t, r.st)

	r.resume()

	got := mustGetCurrentSession(t, r.st)
	if got.State != nightStateRestingIntershow || got.ContentAnchorJSON != "" || got.BoundaryJSON != "" || got.Cycle != 2 || got.Degraded {
		t.Fatalf("session after resume = %+v; want resting-intershow, cycle 2, anchor and boundary cleared, not degraded", got)
	}
	r.h.nightTick(context.Background(), r.now)
	r.assertNoResumePlaylist()
	for _, a := range func() [][]string { _, a := r.fppCommands(); return a }() {
		if len(a) > 0 && a[0] == "halloween-show" {
			t.Fatal("resume pushed a resting session straight into the show")
		}
	}
}

func TestWeatherDelayResumeWhenNotActiveStillTakesEffect(t *testing.T) {
	r := newResumeHarness(t)
	liveAt := r.now.Add(-10 * time.Minute)
	r.createSession(store.NightSessionRecord{State: nightStateLive, StateEnteredAt: liveAt, Cycle: 3, ShowCommitted: true, ContentAnchorJSON: showAnchorAt(liveAt)})
	if err := r.st.SetWeatherDelayState(context.Background(), store.WeatherDelayStateRecord{Revision: 7}); err != nil {
		t.Fatalf("SetWeatherDelayState: %v", err)
	}

	r.resume()

	state, err := r.st.GetWeatherDelayState(context.Background())
	if err != nil || state.Active || state.Revision != 8 {
		t.Fatalf("stored state = %+v (err %v), want not active at revision 8", state, err)
	}
	r.pub.mu.Lock()
	retained, actions, topics := len(r.pub.retained), append([]string(nil), r.pub.actions...), append([]string(nil), r.pub.cmdTopics...)
	r.pub.mu.Unlock()
	if retained != 1 {
		t.Fatalf("retained publishes = %d, want 1", retained)
	}
	wantTopics := map[string]bool{}
	for _, n := range []string{"audio-01", "render-01"} {
		topic, _ := mqttproto.CmdTopic(n)
		wantTopics[topic] = true
	}
	if len(topics) != len(wantTopics) {
		t.Fatalf("resume node commands went to %v, want every declared and inventory node", topics)
	}
	for i, topic := range topics {
		if !wantTopics[topic] || actions[i] != "weatherdelay.resume" {
			t.Fatalf("node command %d = %q on %q, want weatherdelay.resume to a known node", i, actions[i], topic)
		}
	}
	r.addrs.mu.Lock()
	httpLookups := r.addrs.calls
	r.addrs.mu.Unlock()
	if httpLookups != 0 {
		t.Fatalf("resume looked up %d direct HTTP listeners, want 0: resume is MQTT only", httpLookups)
	}

	got := mustGetCurrentSession(t, r.st)
	if got.State != nightStateLive || got.Cycle != 3 || got.ContentAnchorJSON == "" {
		t.Fatalf("session = %q cycle %d; a resume with no active delay must not touch the night session", got.State, got.Cycle)
	}
	if cmds, _ := r.fppCommands(); len(cmds) != 0 {
		t.Fatalf("FPP commands = %v, want none", cmds)
	}
}
