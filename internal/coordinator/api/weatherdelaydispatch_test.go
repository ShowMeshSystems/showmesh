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
)

// This file is ADR-053 decision 4d's own observable acceptance: the
// weatherdelay.start node command is published immediately, never waiting
// on the Resolume blackout's own confirmation.

// blockingResolumeActionDispatcher blocks Dispatch until release is
// closed, recording when Dispatch was first entered.
type blockingResolumeActionDispatcher struct {
	release   chan struct{}
	mu        sync.Mutex
	enteredAt time.Time
}

func (b *blockingResolumeActionDispatcher) Actions() []ResolumeActionDescriptor { return nil }

func (b *blockingResolumeActionDispatcher) Dispatch(ctx context.Context, action string, params map[string]any, _ time.Time) (ResolumeActionResult, error) {
	b.mu.Lock()
	b.enteredAt = time.Now()
	b.mu.Unlock()
	<-b.release
	return ResolumeActionResult{Outcome: ResolumeOutcomeConfirmed, Reason: "went dark"}, nil
}

// fakeWeatherDelayPublisher is a minimal [WeatherDelayPublisher]: Publish
// records the retained state publish; AwaitResponse records the moment
// it was called (proving the node command was published) and always
// times out, so this test needs no matching result-topic delivery.
type fakeWeatherDelayPublisher struct {
	mu               sync.Mutex
	publishedTopics  []string
	nodeCommandCalls []time.Time
}

func (f *fakeWeatherDelayPublisher) Publish(_ context.Context, topic string, _ byte, _ bool, _ []byte) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.publishedTopics = append(f.publishedTopics, topic)
	return nil
}

func (f *fakeWeatherDelayPublisher) AwaitResponse(_ context.Context, _ broker.ResponseRequest) (broker.Message, error) {
	f.mu.Lock()
	f.nodeCommandCalls = append(f.nodeCommandCalls, time.Now())
	f.mu.Unlock()
	return broker.Message{}, broker.ErrResponseDeadlineExceeded
}

func (f *fakeWeatherDelayPublisher) firstNodeCommandAt() (time.Time, bool) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if len(f.nodeCommandCalls) == 0 {
		return time.Time{}, false
	}
	return f.nodeCommandCalls[0], true
}

func TestWeatherDelayStartPublishesNodeCommandWhileResolumeBlocked(t *testing.T) {
	now := time.Now()
	svc, st, _ := newTestIdentityServiceWithStore(t, fixedClock(now))
	admin := mustCreatePrincipal(t, svc, "admin-1", identity.RoleAdmin)
	adminToken := mustIssueToken(t, svc, admin.ID)

	nodePayload, err := config.EncodeAudioNodePayload(config.AudioNodePayload{
		ProgramRoute: "usb-interface", ProgramChannels: []int{1, 2},
		ClockDomain: "single-interface", ClockDomainProvenance: "test fixture",
	})
	if err != nil {
		t.Fatalf("encode audio node payload: %v", err)
	}
	putConfigForTest(t, st, config.AudioNodeConfigKind, "node-01", nodePayload)

	resolume := &blockingResolumeActionDispatcher{release: make(chan struct{})}
	pub := &fakeWeatherDelayPublisher{}

	api := New(Dependencies{
		Nodes: &fakeNodeLister{}, Observations: &fakeObservationLister{},
		Events: &fakeEventReader{}, Collectors: &fakeCollectorStatusLister{},
		Identity: svc, Config: st, Commands: st, WeatherDelay: st,
		ResolumeActions: resolume, Resolume: &fakeResolumeLister{views: []ResolumeInstanceView{{InstanceID: "resolume-01"}}},
		WeatherDelayPublisher: pub,
	}.withDefaults(), Options{Clock: fixedClock(now), Logger: testLogger()})

	auth := map[string]string{"Authorization": "Bearer " + adminToken}
	done := make(chan struct{})
	go func() {
		defer close(done)
		resp, body := doRawRequest(t, api.Handler, newJSONRequest(t, http.MethodPost, "/api/v1/weather-delay/start", `{"idempotencyKey":"key-1"}`, auth))
		if resp.StatusCode != http.StatusOK {
			t.Errorf("start: status = %d, want 200; body: %s", resp.StatusCode, body)
		}
	}()

	// Wait for the node command to be dispatched WHILE Resolume is still
	// blocked, proving it never waited on Resolume's own confirmation.
	deadline := time.After(2 * time.Second)
	for {
		if _, ok := pub.firstNodeCommandAt(); ok {
			break
		}
		select {
		case <-deadline:
			t.Fatal("node command was never dispatched while Resolume blocked")
		case <-time.After(5 * time.Millisecond):
		}
	}

	close(resolume.release)
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("start never completed after Resolume unblocked")
	}

	got, err := st.GetWeatherDelayState(context.Background())
	if err != nil {
		t.Fatalf("GetWeatherDelayState: %v", err)
	}
	if !got.Active {
		t.Fatalf("stored state = %+v, want active", got)
	}
}

// TestWeatherDelayStopAndEmergencyStopNeverRefusedDuringWeatherDelay is
// ADR-053 decision 13's own observable acceptance for emergency stop
// specifically: none of its four routes check the weather delay state at
// all, so a delay never refuses or delays them.
func TestWeatherDelayStopAndEmergencyStopNeverRefusedDuringWeatherDelay(t *testing.T) {
	now := time.Now()
	svc, st, _ := newTestIdentityServiceWithStore(t, fixedClock(now))
	admin := mustCreatePrincipal(t, svc, "admin-1", identity.RoleAdmin)
	adminToken := mustIssueToken(t, svc, admin.ID)
	setWeatherDelayActive(t, st)

	api := New(Dependencies{
		Nodes: &fakeNodeLister{}, Observations: &fakeObservationLister{},
		Events: &fakeEventReader{}, Collectors: &fakeCollectorStatusLister{},
		Identity: svc, Config: st, Commands: st, NightSessions: st, WeatherDelay: st,
		Macros: &fakeMacroRunner{},
	}.withDefaults(), Options{Clock: fixedClock(now), Logger: testLogger()})

	auth := map[string]string{"Authorization": "Bearer " + adminToken}
	resp, body := doRawRequest(t, api.Handler, newJSONRequest(t, http.MethodPost, "/api/v1/emergency-stop/stop", `{"idempotencyKey":"key-1"}`, auth))
	if resp.StatusCode == http.StatusConflict {
		t.Fatalf("emergency stop while delayed must never be refused by the weather delay gate; body: %s", body)
	}
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("emergency stop while delayed: status = %d, want 200; body: %s", resp.StatusCode, body)
	}
}

// TestWeatherDelayStartReportsEveryStopTarget: the FPP and Resolume stops
// run concurrently and each keeps its own result.
func TestWeatherDelayStartReportsEveryStopTarget(t *testing.T) {
	now := time.Now()
	svc, st, _ := newTestIdentityServiceWithStore(t, fixedClock(now))
	admin := mustCreatePrincipal(t, svc, "admin-1", identity.RoleAdmin)
	fppSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusOK) }))
	t.Cleanup(fppSrv.Close)

	api := New(Dependencies{
		Nodes: &fakeNodeLister{}, Observations: &fakeObservationLister{},
		Events: &fakeEventReader{}, Collectors: &fakeCollectorStatusLister{},
		Identity: svc, Config: st, Commands: st, WeatherDelay: st,
		FPP: &fakeFPPLister{views: []FPPInstanceView{{InstanceID: "player-01", Endpoint: fppSrv.URL}}},
		ResolumeActions: &fakeResolumeActionDispatcher{results: map[string]ResolumeActionResult{
			config.ShowActionResolumeBlackout: {Outcome: ResolumeOutcomeConfirmed, Dispatched: true},
		}},
		Resolume:              &fakeResolumeLister{views: []ResolumeInstanceView{{InstanceID: "resolume-01"}}},
		WeatherDelayPublisher: &fakeWeatherDelayPublisher{},
	}.withDefaults(), Options{Clock: fixedClock(now), Logger: testLogger(), FPPCommandConfirmDeadline: 50 * time.Millisecond, FPPCommandPollInterval: 10 * time.Millisecond})

	auth := map[string]string{"Authorization": "Bearer " + mustIssueToken(t, svc, admin.ID)}
	resp, body := doRawRequest(t, api.Handler, newJSONRequest(t, http.MethodPost, "/api/v1/weather-delay/start", `{"idempotencyKey":"key-1"}`, auth))
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("start: status = %d, want 200; body: %s", resp.StatusCode, body)
	}
	var out struct {
		Result struct {
			Targets []struct {
				InstanceID string `json:"instanceId"`
				TargetKind string `json:"targetKind"`
			} `json:"targets"`
		} `json:"result"`
	}
	if err := json.Unmarshal(body, &out); err != nil {
		t.Fatalf("decode: %v", err)
	}
	kinds := map[string]bool{}
	for _, tg := range out.Result.Targets {
		kinds[tg.TargetKind] = true
	}
	if !kinds["fpp"] || !kinds["resolume"] {
		t.Fatalf("targets = %+v, want both the fpp and the resolume stop reported", out.Result.Targets)
	}
}
