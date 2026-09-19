package api

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"

	"github.com/showmeshsystems/showmesh/internal/coordinator/config"
	"github.com/showmeshsystems/showmesh/internal/coordinator/fppcommand"
	"github.com/showmeshsystems/showmesh/internal/coordinator/identity"
	"github.com/showmeshsystems/showmesh/internal/coordinator/store"
	"github.com/showmeshsystems/showmesh/pkg/mqttproto"
	"github.com/showmeshsystems/showmesh/pkg/observation"
	"github.com/showmeshsystems/showmesh/pkg/weatherdelay"
)

// darkPublishCount counts how many times groupID's own dark heartbeat
// topic was published, for a test to assert the heartbeat's own cadence
// and stop condition against [fakeWeatherDelayPublisher] (defined in
// weatherdelaydispatch_test.go).
func (f *fakeWeatherDelayPublisher) darkPublishCount(groupID string) int {
	topic, err := mqttproto.WeatherDelayDarkTopic(groupID)
	if err != nil {
		return 0
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	n := 0
	for _, t := range f.publishedTopics {
		if t == topic {
			n++
		}
	}
	return n
}

// fakeFPPPluginServer stands in for one FPP host's own plugin: FPP's
// command endpoint plus the weather gate route, both recorded so a test
// can assert on them.
type fakeFPPPluginServer struct {
	srv *httptest.Server

	mu              sync.Mutex
	commands        []string
	gateSets        int
	closed          bool
	revision        int64
	gateUnsupported bool
	gateReadFails   bool
	gateWriteFails  bool
	responseDelay   time.Duration
}

func newFakeFPPPluginServer(t *testing.T) *fakeFPPPluginServer {
	t.Helper()
	f := &fakeFPPPluginServer{}
	f.srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		f.mu.Lock()
		delay := f.responseDelay
		f.mu.Unlock()
		if delay > 0 {
			time.Sleep(delay)
		}
		switch r.URL.Path {
		case fppcommand.WeatherGatePath:
			f.handleGate(w, r)
		default:
			f.handleCommand(w, r)
		}
	}))
	t.Cleanup(f.srv.Close)
	return f
}

func (f *fakeFPPPluginServer) handleCommand(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Command string `json:"command"`
	}
	raw, _ := io.ReadAll(r.Body)
	_ = json.Unmarshal(raw, &body)
	f.mu.Lock()
	f.commands = append(f.commands, body.Command)
	f.mu.Unlock()
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write([]byte("Stopped"))
}

func (f *fakeFPPPluginServer) handleGate(w http.ResponseWriter, r *http.Request) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.gateUnsupported {
		w.WriteHeader(http.StatusNotFound)
		return
	}
	if r.Method == http.MethodPost && f.gateWriteFails {
		w.WriteHeader(http.StatusInternalServerError)
		return
	}
	if r.Method == http.MethodPost {
		var body struct {
			Closed   bool  `json:"closed"`
			Revision int64 `json:"revision"`
		}
		raw, _ := io.ReadAll(r.Body)
		_ = json.Unmarshal(raw, &body)
		f.gateSets++
		f.closed = body.Closed
		if body.Revision > f.revision {
			f.revision = body.Revision
		}
	} else if f.gateReadFails {
		w.WriteHeader(http.StatusInternalServerError)
		return
	}
	effective := 100
	if f.closed {
		effective = 0
	}
	w.Header().Set("Content-Type", "application/json")
	_, _ = w.Write([]byte(mustGateJSON(f.closed, f.revision, effective)))
}

func mustGateJSON(closed bool, revision int64, effective int) string {
	b, _ := json.Marshal(map[string]any{
		"weatherGateClosed": closed, "weatherGateRevision": revision, "effectiveOutputPercent": effective,
	})
	return string(b)
}

func (f *fakeFPPPluginServer) setGate(closed bool, revision int64) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.closed, f.revision = closed, revision
}

func (f *fakeFPPPluginServer) commandList() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]string(nil), f.commands...)
}

func (f *fakeFPPPluginServer) gateSetCount() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.gateSets
}

func (f *fakeFPPPluginServer) isClosed() bool {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.closed
}

type enforceHarness struct {
	t    *testing.T
	st   *store.Store
	obs  *mutableObservationLister
	pub  *fakeWeatherDelayPublisher
	now  time.Time
	deps Dependencies
}

func newEnforceHarness(t *testing.T, fppViews []FPPInstanceView) *enforceHarness {
	t.Helper()
	now := time.Date(2026, 10, 31, 20, 30, 0, 0, time.UTC)
	svc, st, _ := newTestIdentityServiceWithStore(t, func() time.Time { return now })
	h := &enforceHarness{t: t, st: st, obs: &mutableObservationLister{}, pub: &fakeWeatherDelayPublisher{}, now: now}
	h.deps = Dependencies{
		Observations: h.obs, Events: &fakeEventReader{}, Collectors: &fakeCollectorStatusLister{},
		Identity: svc, Config: st, Commands: st, WeatherDelay: st,
		FPP:                   &fakeFPPLister{views: fppViews},
		WeatherDelayPublisher: h.pub,
	}.withDefaults()
	return h
}

func (h *enforceHarness) setState(active bool, revision int64) {
	h.t.Helper()
	rec := store.WeatherDelayStateRecord{Revision: revision}
	if active {
		rec.Active, rec.Kind, rec.StartedAt, rec.StartedBy = true, weatherdelay.KindDelay, h.now, "op-1"
	}
	if err := h.deps.WeatherDelay.SetWeatherDelayState(context.Background(), rec); err != nil {
		h.t.Fatalf("set weather delay state: %v", err)
	}
}

func (h *enforceHarness) enforcer() *WeatherDelayEnforcer {
	return NewWeatherDelayEnforcer(h.deps, Options{Clock: func() time.Time { return h.now }, Logger: testLogger()})
}

func (h *enforceHarness) handlers() *handlers {
	return &handlers{deps: h.deps, clock: func() time.Time { return h.now }, logger: testLogger()}
}

func TestWeatherDelayEnforceStopsAPlayerThatStartsPlayingMidDelay(t *testing.T) {
	fpp := newFakeFPPPluginServer(t)
	h := newEnforceHarness(t, []FPPInstanceView{{InstanceID: "player-01", Endpoint: fpp.srv.URL}})
	h.setState(true, 5)
	h.obs.set([]observation.Observation{statusObservation("player-01", fppStatusValuePlaying, h.now)})

	h.enforcer().tick(context.Background(), h.now)

	cmds := fpp.commandList()
	if len(cmds) != 1 || cmds[0] != "Stop Now" {
		t.Fatalf("commands = %v, want exactly one Stop Now", cmds)
	}
}

func TestWeatherDelayEnforceDoesNotStopAnIdlePlayer(t *testing.T) {
	fpp := newFakeFPPPluginServer(t)
	h := newEnforceHarness(t, []FPPInstanceView{{InstanceID: "player-01", Endpoint: fpp.srv.URL}})
	h.setState(true, 5)
	h.obs.set([]observation.Observation{statusObservation("player-01", fppStatusValueIdle, h.now)})

	h.enforcer().tick(context.Background(), h.now)

	if cmds := fpp.commandList(); len(cmds) != 0 {
		t.Fatalf("commands = %v, want none for an idle, current observation", cmds)
	}
}

func TestWeatherDelayEnforceClosesAnOpenGateMidDelay(t *testing.T) {
	fpp := newFakeFPPPluginServer(t)
	fpp.setGate(false, 4)
	h := newEnforceHarness(t, []FPPInstanceView{{InstanceID: "player-01", Endpoint: fpp.srv.URL}})
	h.setState(true, 5)
	h.obs.set([]observation.Observation{statusObservation("player-01", fppStatusValueIdle, h.now)})

	h.enforcer().tick(context.Background(), h.now)

	if !fpp.isClosed() {
		t.Fatal("gate was not closed within one tick")
	}
	if fpp.gateSetCount() != 1 {
		t.Fatalf("gate set count = %d, want 1", fpp.gateSetCount())
	}
}

func TestWeatherDelayEnforceTreatsAFailingGateReadAsNotClosed(t *testing.T) {
	fpp := newFakeFPPPluginServer(t)
	fpp.gateReadFails = true
	h := newEnforceHarness(t, []FPPInstanceView{{InstanceID: "player-01", Endpoint: fpp.srv.URL}})
	h.setState(true, 5)
	h.obs.set([]observation.Observation{statusObservation("player-01", fppStatusValueIdle, h.now)})

	h.enforcer().tick(context.Background(), h.now)

	if fpp.gateSetCount() != 1 {
		t.Fatalf("gate set count = %d, want 1 (a failed read must be treated as not closed)", fpp.gateSetCount())
	}
}

func TestWeatherDelayEnforceNotActiveSendsOnlyTheStaleGateReopen(t *testing.T) {
	fpp := newFakeFPPPluginServer(t)
	fpp.setGate(true, 3) // stayed closed after a resume it never learned about
	h := newEnforceHarness(t, []FPPInstanceView{{InstanceID: "player-01", Endpoint: fpp.srv.URL}})
	h.setState(false, 5)
	h.obs.set([]observation.Observation{statusObservation("player-01", fppStatusValuePlaying, h.now)})

	e := h.enforcer()
	e.tick(context.Background(), h.now)

	if cmds := fpp.commandList(); len(cmds) != 0 {
		t.Fatalf("commands = %v, want none while not active, even for a playing observation", cmds)
	}
	if fpp.isClosed() {
		t.Fatal("the stale-closed gate was not reopened")
	}
	if got := fpp.gateSetCount(); got != 1 {
		t.Fatalf("gate set count = %d, want exactly 1", got)
	}

	// A second tick immediately after must not reopen again: throttled to
	// once per 30 seconds per instance.
	fpp.setGate(true, 3)
	e.tick(context.Background(), h.now.Add(time.Second))
	if got := fpp.gateSetCount(); got != 1 {
		t.Fatalf("gate set count after a second immediate tick = %d, want still 1 (throttled)", got)
	}
}

func TestWeatherDelayEnforceResumesAfterRestartOverSameStore(t *testing.T) {
	fpp := newFakeFPPPluginServer(t)
	h := newEnforceHarness(t, []FPPInstanceView{{InstanceID: "player-01", Endpoint: fpp.srv.URL}})
	h.setState(true, 7)
	h.obs.set([]observation.Observation{statusObservation("player-01", fppStatusValuePlaying, h.now)})

	// A fresh enforcer, as a coordinator restart would build, reading the
	// SAME store: no special-cased startup path is needed for it to act.
	fresh := NewWeatherDelayEnforcer(h.deps, Options{Clock: func() time.Time { return h.now }, Logger: testLogger()})
	fresh.tick(context.Background(), h.now)

	if cmds := fpp.commandList(); len(cmds) != 1 || cmds[0] != "Stop Now" {
		t.Fatalf("commands = %v, want exactly one Stop Now after a simulated restart", cmds)
	}
}

func TestWeatherDelayEnforceOneSlowInstanceDoesNotDelayOthers(t *testing.T) {
	oldTimeout, oldClientTimeout := weatherDelayEnforceInstanceTimeout, weatherDelayFPPClientTimeout
	weatherDelayEnforceInstanceTimeout, weatherDelayFPPClientTimeout = 200*time.Millisecond, 100*time.Millisecond
	t.Cleanup(func() {
		weatherDelayEnforceInstanceTimeout, weatherDelayFPPClientTimeout = oldTimeout, oldClientTimeout
	})

	slow := newFakeFPPPluginServer(t)
	slow.mu.Lock()
	slow.responseDelay = time.Second
	slow.mu.Unlock()
	fast := newFakeFPPPluginServer(t)

	h := newEnforceHarness(t, []FPPInstanceView{
		{InstanceID: "player-slow", Endpoint: slow.srv.URL},
		{InstanceID: "player-fast", Endpoint: fast.srv.URL},
	})
	h.setState(true, 5)
	h.obs.set([]observation.Observation{
		statusObservation("player-slow", fppStatusValuePlaying, h.now),
		statusObservation("player-fast", fppStatusValuePlaying, h.now),
	})

	done := make(chan struct{})
	start := time.Now()
	go func() {
		h.enforcer().tick(context.Background(), h.now)
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("tick did not return promptly; the slow instance delayed the tick")
	}
	if elapsed := time.Since(start); elapsed > time.Second {
		t.Fatalf("tick took %s, want well under a second despite one slow instance", elapsed)
	}
	if cmds := fast.commandList(); len(cmds) != 1 || cmds[0] != "Stop Now" {
		t.Fatalf("fast instance commands = %v, want exactly one Stop Now", cmds)
	}
}

func resolumeActiveClipObservation(instanceID, layerID, value string, collectedAt time.Time) observation.Observation {
	observedAt := collectedAt
	return observation.Observation{
		Resource: observation.ResourceRef{Kind: observation.ResourceResolume, ID: instanceID},
		Signal:   observation.SignalID("resolume.layer." + layerID + ".active_clip"), Value: value,
		ObservedAt: &observedAt, CollectedAt: collectedAt, Source: "resolume-rest",
		Quality: observation.QualityDirect, ValidFor: time.Minute,
	}
}

func setWeatherDelayPowerGroupsForTest(t *testing.T, st *store.Store, groups []config.WeatherDelayPowerGroupPayload) {
	t.Helper()
	payload, err := config.EncodeWeatherDelayPayload(config.WeatherDelayPayload{
		Alert: config.WeatherDelayDefaultPayload.Alert, PowerGroups: groups, Triggers: config.WeatherDelayDefaultPayload.Triggers,
	})
	if err != nil {
		t.Fatalf("encode show.weatherdelay payload: %v", err)
	}
	putConfigForTest(t, st, config.ShowWeatherDelayConfigKind, config.ShowWeatherDelayConfigObjectID, payload)
}

func TestWeatherDelayEnforceGroupFlipsToNotDarkWhenAMemberIsStale(t *testing.T) {
	fpp := newFakeFPPPluginServer(t)
	fpp.setGate(true, 5)
	h := newEnforceHarness(t, []FPPInstanceView{{InstanceID: "player-01", Endpoint: fpp.srv.URL}})
	h.setState(true, 5)
	setWeatherDelayPowerGroupsForTest(t, h.st, []config.WeatherDelayPowerGroupPayload{{
		ID: "group-a", Label: "Front Yard", FPPInstanceIDs: []string{"player-01"},
		ResolumeInstanceIDs: []string{"resolume-01"}, RenderNodeIDs: []string{},
		Heartbeat: config.WeatherDelayHeartbeatPayload{Enabled: true, IntervalSeconds: 30},
	}})
	h.obs.set([]observation.Observation{
		statusObservation("player-01", fppStatusValueIdle, h.now),
		resolumeActiveClipObservation("resolume-01", "layer-1", weatherDelayResolumeLayerActiveClipNone, h.now),
	})

	e := h.enforcer()
	e.tick(context.Background(), h.now)

	hs := h.handlers()
	payload, _, _, _, err := resolveWeatherDelayConfig(context.Background(), hs.deps.Config)
	if err != nil {
		t.Fatalf("resolve config: %v", err)
	}
	dark, members := hs.weatherDelayGroupDarkness(context.Background(), h.now, payload.PowerGroups[0])
	if !dark {
		t.Fatalf("group not confirmed dark: %+v", members)
	}
	if got := h.pub.darkPublishCount("group-a"); got != 1 {
		t.Fatalf("dark heartbeat publish count = %d, want 1 while active, dark, and enabled", got)
	}

	// One member's observation goes stale: the group must flip to not
	// dark, and the heartbeat must stop on this very tick.
	staleNow := h.now.Add(time.Hour)
	dark, members = hs.weatherDelayGroupDarkness(context.Background(), staleNow, payload.PowerGroups[0])
	if dark {
		t.Fatalf("group still reported dark with a stale member: %+v", members)
	}
	e.tick(context.Background(), staleNow)
	if got := h.pub.darkPublishCount("group-a"); got != 1 {
		t.Fatalf("dark heartbeat publish count after going stale = %d, want still 1 (no further publish)", got)
	}
}

func TestWeatherDelayEnforceReportsAPluginWithoutTheRouteRatherThanCountingItDark(t *testing.T) {
	fpp := newFakeFPPPluginServer(t)
	fpp.gateUnsupported = true
	h := newEnforceHarness(t, []FPPInstanceView{{InstanceID: "player-01", Endpoint: fpp.srv.URL}})
	h.setState(true, 5)
	setWeatherDelayPowerGroupsForTest(t, h.st, []config.WeatherDelayPowerGroupPayload{{
		ID: "group-a", Label: "Front Yard", FPPInstanceIDs: []string{"player-01"},
		ResolumeInstanceIDs: []string{}, RenderNodeIDs: []string{},
	}})
	h.obs.set([]observation.Observation{statusObservation("player-01", fppStatusValueIdle, h.now)})

	h.enforcer().tick(context.Background(), h.now)

	hs := h.handlers()
	dark, reason := hs.weatherDelayFPPMemberDark(context.Background(), h.now, "player-01")
	if dark {
		t.Fatal("an unsupported gate route must never be counted as dark")
	}
	if reason == "" {
		t.Fatal("want a reason naming that this player cannot be held dark")
	}
}

// scriptedWeatherDelayStore answers each read with the next scripted
// result, repeating the last one, so a test can place a state change
// between the enforcer's own reads.
type scriptedWeatherDelayStore struct {
	mu    sync.Mutex
	reads []scriptedWeatherDelayRead
}

type scriptedWeatherDelayRead struct {
	rec store.WeatherDelayStateRecord
	err error
}

func (s *scriptedWeatherDelayStore) GetWeatherDelayState(context.Context) (store.WeatherDelayStateRecord, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	next := s.reads[0]
	if len(s.reads) > 1 {
		s.reads = s.reads[1:]
	}
	return next.rec, next.err
}

func (s *scriptedWeatherDelayStore) SetWeatherDelayState(context.Context, store.WeatherDelayStateRecord) error {
	return nil
}

func (s *scriptedWeatherDelayStore) script(reads ...scriptedWeatherDelayRead) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.reads = reads
}

func activeRead(revision int64) scriptedWeatherDelayRead {
	return scriptedWeatherDelayRead{rec: store.WeatherDelayStateRecord{Active: true, Kind: weatherdelay.KindDelay, Revision: revision}}
}

func inactiveRead(revision int64) scriptedWeatherDelayRead {
	return scriptedWeatherDelayRead{rec: store.WeatherDelayStateRecord{Revision: revision}}
}

var errScriptedStateRead = errors.New("state store unavailable")

func failedRead() scriptedWeatherDelayRead {
	return scriptedWeatherDelayRead{err: errScriptedStateRead}
}

func (h *enforceHarness) useScriptedState(reads ...scriptedWeatherDelayRead) *scriptedWeatherDelayStore {
	s := &scriptedWeatherDelayStore{}
	s.script(reads...)
	h.deps.WeatherDelay = s
	return s
}

func TestWeatherDelayEnforceStateReadFailureNeverStopsAShowItNeverKnewDelayed(t *testing.T) {
	fpp := newFakeFPPPluginServer(t)
	fpp.setGate(true, 3)
	h := newEnforceHarness(t, []FPPInstanceView{{InstanceID: "player-01", Endpoint: fpp.srv.URL}})
	h.useScriptedState(failedRead())
	h.obs.set([]observation.Observation{statusObservation("player-01", fppStatusValuePlaying, h.now)})

	h.enforcer().tick(context.Background(), h.now)

	if cmds := fpp.commandList(); len(cmds) != 0 {
		t.Fatalf("commands = %v, want none: this process never saw a delay active", cmds)
	}
	if got := fpp.gateSetCount(); got != 0 {
		t.Fatalf("gate set count = %d, want 0 while the state is unknown", got)
	}
}

func TestWeatherDelayEnforceStateReadFailureKeepsEnforcingADelayItKnewActive(t *testing.T) {
	fpp := newFakeFPPPluginServer(t)
	h := newEnforceHarness(t, []FPPInstanceView{{InstanceID: "player-01", Endpoint: fpp.srv.URL}})
	h.useScriptedState(activeRead(5), failedRead())
	h.obs.set([]observation.Observation{statusObservation("player-01", fppStatusValuePlaying, h.now)})

	e := h.enforcer()
	e.tick(context.Background(), h.now)
	fpp.setGate(false, 9)
	e.tick(context.Background(), h.now.Add(5*time.Second))

	if cmds := fpp.commandList(); len(cmds) != 2 {
		t.Fatalf("commands = %v, want Stop Now on both ticks", cmds)
	}
	if !fpp.isClosed() {
		t.Fatal("gate left open while the last known state was an active delay")
	}
}

func TestWeatherDelayEnforceStopsAPlayerStoppingGracefullyAfterItsLoop(t *testing.T) {
	fpp := newFakeFPPPluginServer(t)
	h := newEnforceHarness(t, []FPPInstanceView{{InstanceID: "player-01", Endpoint: fpp.srv.URL}})
	h.setState(true, 5)
	h.obs.set([]observation.Observation{statusObservation("player-01", fppStatusValueStoppingGracefullyAfterLoop, h.now)})

	h.enforcer().tick(context.Background(), h.now)

	if cmds := fpp.commandList(); len(cmds) != 1 || cmds[0] != "Stop Now" {
		t.Fatalf("commands = %v, want Stop Now for a player still finishing its loop", cmds)
	}
}

func TestWeatherDelayEnforceNeverReopensAGateAStartClosedDuringTheTick(t *testing.T) {
	fpp := newFakeFPPPluginServer(t)
	fpp.setGate(true, 6)
	h := newEnforceHarness(t, []FPPInstanceView{{InstanceID: "player-01", Endpoint: fpp.srv.URL}})
	h.useScriptedState(inactiveRead(5), activeRead(6))
	h.obs.set([]observation.Observation{statusObservation("player-01", fppStatusValueIdle, h.now)})

	h.enforcer().tick(context.Background(), h.now)

	if !fpp.isClosed() {
		t.Fatal("the enforcer opened a gate a delay started during its own tick had closed")
	}
}

func TestWeatherDelayEnforceNeverClosesAGateAResumeOpenedDuringTheTick(t *testing.T) {
	fpp := newFakeFPPPluginServer(t)
	fpp.setGate(false, 6)
	h := newEnforceHarness(t, []FPPInstanceView{{InstanceID: "player-01", Endpoint: fpp.srv.URL}})
	h.useScriptedState(activeRead(5), inactiveRead(6))
	h.obs.set([]observation.Observation{statusObservation("player-01", fppStatusValueIdle, h.now)})

	h.enforcer().tick(context.Background(), h.now)

	if fpp.isClosed() {
		t.Fatal("the enforcer closed a gate a resume during its own tick had opened")
	}
}

func TestWeatherDelayEnforceGroupIsNotDarkWhenTheGateCannotBeRead(t *testing.T) {
	fpp := newFakeFPPPluginServer(t)
	fpp.setGate(true, 5)
	h := newEnforceHarness(t, []FPPInstanceView{{InstanceID: "player-01", Endpoint: fpp.srv.URL}})
	h.setState(true, 5)
	setWeatherDelayPowerGroupsForTest(t, h.st, []config.WeatherDelayPowerGroupPayload{{
		ID: "group-a", Label: "Front Yard", FPPInstanceIDs: []string{"player-01"},
		ResolumeInstanceIDs: []string{}, RenderNodeIDs: []string{},
		Heartbeat: config.WeatherDelayHeartbeatPayload{Enabled: true, IntervalSeconds: 1},
	}})
	h.obs.set([]observation.Observation{statusObservation("player-01", fppStatusValueIdle, h.now)})

	e := h.enforcer()
	e.tick(context.Background(), h.now)
	if got := h.pub.darkPublishCount("group-a"); got != 1 {
		t.Fatalf("dark heartbeat publish count = %d, want 1 before the gate read fails", got)
	}

	fpp.mu.Lock()
	fpp.gateReadFails, fpp.gateWriteFails = true, true
	fpp.mu.Unlock()
	later := h.now.Add(5 * time.Second)
	h.obs.set([]observation.Observation{statusObservation("player-01", fppStatusValueIdle, later)})
	e.tick(context.Background(), later)

	if dark, _ := h.handlers().weatherDelayFPPMemberDark(context.Background(), later, "player-01"); dark {
		t.Fatal("a player whose gate could not be read was counted dark")
	}
	if got := h.pub.darkPublishCount("group-a"); got != 1 {
		t.Fatalf("dark heartbeat publish count = %d, want still 1 after the gate read failed", got)
	}
}

// panickingObservationLister panics for one instance's reads, standing in
// for any defect in one player's pass.
type panickingObservationLister struct {
	inner      ObservationLister
	panicForID string
}

func (p panickingObservationLister) ListObservations(ctx context.Context, f ObservationFilter) ([]observation.Observation, error) {
	if f.ResourceID != nil && *f.ResourceID == p.panicForID {
		panic("test panic for " + p.panicForID)
	}
	return p.inner.ListObservations(ctx, f)
}

func TestWeatherDelayEnforceSurvivesAPanicInOnePlayersPass(t *testing.T) {
	bad := newFakeFPPPluginServer(t)
	good := newFakeFPPPluginServer(t)
	h := newEnforceHarness(t, []FPPInstanceView{
		{InstanceID: "player-panic", Endpoint: bad.srv.URL},
		{InstanceID: "player-good", Endpoint: good.srv.URL},
	})
	h.setState(true, 5)
	h.obs.set([]observation.Observation{statusObservation("player-good", fppStatusValuePlaying, h.now)})
	h.deps.Observations = panickingObservationLister{inner: h.obs, panicForID: "player-panic"}

	h.enforcer().tick(context.Background(), h.now)

	if cmds := good.commandList(); len(cmds) != 1 || cmds[0] != "Stop Now" {
		t.Fatalf("healthy player commands = %v, want Stop Now despite another player's pass panicking", cmds)
	}
}

func TestWeatherDelayEnforceAuditsAStaleGateReopen(t *testing.T) {
	fpp := newFakeFPPPluginServer(t)
	fpp.setGate(true, 3)
	h := newEnforceHarness(t, []FPPInstanceView{{InstanceID: "player-01", Endpoint: fpp.srv.URL}})
	h.setState(false, 5)
	h.obs.set([]observation.Observation{statusObservation("player-01", fppStatusValueIdle, h.now)})

	h.enforcer().tick(context.Background(), h.now)

	entries, err := h.deps.Identity.ListAudit(context.Background(), 0, 100)
	if err != nil {
		t.Fatalf("list audit: %v", err)
	}
	for _, e := range entries {
		if e.Action == identity.AuditActionShowWeatherDelayEnforce && e.Target == "player-01" {
			return
		}
	}
	t.Fatal("reopening a gate left no show.weatherdelay.enforce audit entry")
}
