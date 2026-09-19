package api

import (
	"context"
	"net/http"
	"testing"
	"time"

	"github.com/showmeshsystems/showmesh/internal/coordinator/config"
)

func weatherDelayGateDeps(t *testing.T) (Dependencies, func()) {
	t.Helper()
	svc, st, _ := newTestIdentityServiceWithStore(t, time.Now)
	deps := Dependencies{Identity: svc, Config: st, Commands: st, WeatherDelay: st}.withDefaults()
	return deps, func() { setWeatherDelayActive(t, st) }
}

// The macro executor and show actions reach FPP through this dispatcher,
// not the HTTP route, so the hold has to live here.
func TestFPPDispatcherRefusesStartDuringWeatherDelay(t *testing.T) {
	deps, activate := weatherDelayGateDeps(t)
	activate()
	d := NewFPPCommandDispatcher(deps, Options{})
	for _, action := range []string{"startPlaylist", "resumePlaylist"} {
		params := map[string]any{}
		if action == "startPlaylist" {
			params["playlist"] = "main"
		}
		_, problem, err := d.Dispatch(context.Background(), FPPCommandInput{InstanceID: "inst-1", Action: action, Params: params, IdempotencyKey: "k-" + action})
		if err != nil || problem == nil || problem.Status != http.StatusConflict {
			t.Fatalf("%s during a weather delay: problem=%+v err=%v, want a 409 refusal", action, problem, err)
		}
	}
	_, problem, _ := d.Dispatch(context.Background(), FPPCommandInput{InstanceID: "inst-1", Action: "stopPlaylist", IdempotencyKey: "k-stop"})
	if problem != nil && problem.Status == http.StatusConflict && problem.Title == "A weather delay is active" {
		t.Fatal("stopPlaylist was refused by the weather delay hold")
	}
}

func TestAudioDispatcherRefusesStartDuringWeatherDelay(t *testing.T) {
	deps, activate := weatherDelayGateDeps(t)
	activate()
	d := NewAudioActionDispatcher(deps, Options{})
	for _, action := range []string{"audio.session.start", "audio.session.resume", "audio.output.unmute"} {
		_, problem, err := d.Dispatch(context.Background(), AudioDispatchInput{Action: action, NodeID: "node-01", SessionID: "s1", IdempotencyKey: "k-" + action})
		if err != nil || problem == nil || problem.Status != http.StatusConflict {
			t.Fatalf("%s during a weather delay: problem=%+v err=%v, want a 409 refusal", action, problem, err)
		}
	}
}

func TestResolumeGateRefusesLaunchButPassesBlackout(t *testing.T) {
	deps, activate := weatherDelayGateDeps(t)
	inner := &fakeResolumeActionDispatcher{results: map[string]ResolumeActionResult{
		config.ShowActionResolumeBlackout:   {Outcome: ResolumeOutcomeConfirmed},
		config.ShowActionResolumeLaunchClip: {Outcome: ResolumeOutcomeConfirmed},
	}}
	gate := WeatherDelayGatedResolumeActions(inner, deps.WeatherDelay)

	if res, _ := gate.Dispatch(context.Background(), config.ShowActionResolumeLaunchClip, nil, time.Now()); res.Outcome != ResolumeOutcomeConfirmed {
		t.Fatalf("launchClip with no delay = %q, want it passed through", res.Outcome)
	}
	activate()
	for _, action := range []string{config.ShowActionResolumeLaunchClip, config.ShowActionResolumeLaunchColumn} {
		if res, _ := gate.Dispatch(context.Background(), action, nil, time.Now()); res.Outcome != ResolumeOutcomeRefused {
			t.Fatalf("%s during a weather delay = %q, want refused", action, res.Outcome)
		}
	}
	if res, _ := gate.Dispatch(context.Background(), config.ShowActionResolumeBlackout, nil, time.Now()); res.Outcome != ResolumeOutcomeConfirmed {
		t.Fatalf("blackout during a weather delay = %q, want it passed through", res.Outcome)
	}
	if n := inner.callCount(); n != 2 {
		t.Fatalf("inner dispatcher calls = %d, want 2 (the launch before the delay and the blackout)", n)
	}
}

func TestRenderApplyRefusedDuringWeatherDelay(t *testing.T) {
	api, st, adminToken, _ := newWeatherDelayTestAPI(t)
	setWeatherDelayActive(t, st)
	auth := map[string]string{"Authorization": "Bearer " + adminToken}

	for _, path := range []string{"apply", "restart"} {
		resp, body := doRawRequest(t, api.Handler, newJSONRequest(t, http.MethodPost, "/api/v1/nodes/node-01/render/surfaces/main/"+path, `{"sequenceId":"seq-1","idempotencyKey":"k-1"}`, auth))
		if resp.StatusCode != http.StatusConflict {
			t.Fatalf("render %s during a weather delay: status = %d, want 409; body: %s", path, resp.StatusCode, body)
		}
	}
	resp, body := doRawRequest(t, api.Handler, newJSONRequest(t, http.MethodPost, "/api/v1/nodes/node-01/render/surfaces/main/clear", `{"idempotencyKey":"k-2"}`, auth))
	if resp.StatusCode == http.StatusConflict {
		t.Fatalf("render clear during a weather delay was refused; body: %s", body)
	}
}
