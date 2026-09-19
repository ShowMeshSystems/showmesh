package api

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"strings"
	"sync"
	"testing"
	"time"

	v1 "github.com/showmeshsystems/showmesh/internal/coordinator/api/v1"
	"github.com/showmeshsystems/showmesh/internal/coordinator/store"
	"github.com/showmeshsystems/showmesh/pkg/observation"
)

// A start whose state write fails still stops everything and still holds
// this coordinator process until the write succeeds or resume clears it.

type failingWeatherDelayStore struct {
	*store.Store
	mu       sync.Mutex
	fail     bool
	failRead bool
}

func (f *failingWeatherDelayStore) GetWeatherDelayState(ctx context.Context) (store.WeatherDelayStateRecord, error) {
	f.mu.Lock()
	failRead := f.failRead
	f.mu.Unlock()
	if failRead {
		return store.WeatherDelayStateRecord{}, errors.New("disk I/O error")
	}
	return f.Store.GetWeatherDelayState(ctx)
}

func (f *failingWeatherDelayStore) setFail(fail bool) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.fail = fail
}

func (f *failingWeatherDelayStore) SetWeatherDelayState(ctx context.Context, rec store.WeatherDelayStateRecord) error {
	f.mu.Lock()
	fail := f.fail
	f.mu.Unlock()
	if fail {
		return errors.New("disk I/O error")
	}
	return f.Store.SetWeatherDelayState(ctx, rec)
}

func newNotSavedHarness(t *testing.T) (*resumeHarness, *failingWeatherDelayStore) {
	t.Helper()
	var fs *failingWeatherDelayStore
	r := newResumeHarnessWith(t, func(st *store.Store) WeatherDelayStore {
		fs = &failingWeatherDelayStore{Store: st, fail: true}
		return fs
	})
	return r, fs
}

func (r *resumeHarness) start() v1.WeatherDelayActionResult {
	r.t.Helper()
	auth := map[string]string{"Authorization": "Bearer " + r.token}
	resp, body := doRawRequest(r.t, r.api.Handler, newJSONRequest(r.t, http.MethodPost, "/api/v1/weather-delay/start", `{"idempotencyKey":"start-1"}`, auth))
	if resp.StatusCode != http.StatusOK {
		r.t.Fatalf("start: status = %d, want 200; body: %s", resp.StatusCode, body)
	}
	var out v1.WeatherDelayActionResponse
	if err := json.Unmarshal(body, &out); err != nil {
		r.t.Fatalf("decode start response: %v; body: %s", err, body)
	}
	return out.Result
}

func TestWeatherDelayStartWriteFailureStillStopsAndHolds(t *testing.T) {
	r, fs := newNotSavedHarness(t)
	r.createSession(store.NightSessionRecord{State: nightStatePreshow, StateEnteredAt: r.now.Add(-time.Minute)})
	r.obs.set([]observation.Observation{statusObservation("player-01", fppStatusValueIdle, r.now)})

	result := r.start()

	if !result.NotSaved || result.NotSavedMessage != weatherDelayNotSavedMessage || !result.Active {
		t.Fatalf("start result = %+v, want active and not saved with the operator sentence", result)
	}
	kinds := map[string]bool{}
	for _, target := range result.Targets {
		kinds[target.TargetKind+"/"+target.InstanceID] = true
	}
	if !kinds[v1.EmergencyStopTargetKindFPP+"/player-01"] || !kinds[v1.WeatherDelayTargetKindNodeCommand+"/audio-01"] {
		t.Fatalf("targets = %+v, want the FPP stop and the node start dispatched", result.Targets)
	}
	if cmds, _ := r.fppCommands(); len(cmds) == 0 {
		t.Fatal("no FPP command was sent: a failed write must never stop the stops")
	}
	r.pub.mu.Lock()
	retained, actions := append([][]byte(nil), r.pub.retained...), append([]string(nil), r.pub.actions...)
	r.pub.mu.Unlock()
	if len(retained) != 1 || !strings.Contains(string(retained[0]), `"active":true`) {
		t.Fatalf("retained publishes = %q, want one active state", retained)
	}
	if len(actions) == 0 || actions[0] != "weatherdelay.start" {
		t.Fatalf("node actions = %v, want weatherdelay.start", actions)
	}

	before, _ := r.fppCommands()
	r.h.nightTick(context.Background(), r.now)
	if after, _ := r.fppCommands(); len(after) != len(before) {
		t.Fatalf("night loop sent %v after an unsaved start, want it held", after[len(before):])
	}
	if active, err := r.h.weatherDelayActive(context.Background()); err != nil || !active {
		t.Fatalf("weatherDelayActive = %v (err %v), want the cue loop and every refusal held", active, err)
	}
	if stored, _ := r.st.GetWeatherDelayState(context.Background()); stored.Active {
		t.Fatal("the store reads active although every write failed")
	}

	fs.setFail(false)
	if err := r.h.deps.WeatherDelay.(*WeatherDelayStateKeeper).SaveUnsaved(context.Background()); err != nil {
		t.Fatalf("SaveUnsaved: %v", err)
	}
	stored, err := r.st.GetWeatherDelayState(context.Background())
	if err != nil || !stored.Active || stored.Revision != result.Revision {
		t.Fatalf("stored state after the retry = %+v (err %v), want active at revision %d", stored, err, result.Revision)
	}
}

func TestWeatherDelayStartReadFailureStillStopsAndHolds(t *testing.T) {
	r, fs := newNotSavedHarness(t)
	r.obs.set([]observation.Observation{statusObservation("player-01", fppStatusValueIdle, r.now)})
	fs.mu.Lock()
	fs.failRead = true
	fs.mu.Unlock()

	result := r.start()

	if !result.Active || !result.NotSaved {
		t.Fatalf("start result = %+v, want active and not saved", result)
	}
	if cmds, _ := r.fppCommands(); len(cmds) == 0 {
		t.Fatal("no FPP command was sent: a failed read must never stop the stops")
	}
	r.pub.mu.Lock()
	actions := append([]string(nil), r.pub.actions...)
	r.pub.mu.Unlock()
	if len(actions) == 0 || actions[0] != "weatherdelay.start" {
		t.Fatalf("node actions = %v, want weatherdelay.start", actions)
	}
	if active, err := r.h.weatherDelayActive(context.Background()); err != nil || !active {
		t.Fatalf("weatherDelayActive = %v (err %v), want held in this process", active, err)
	}

	fs.mu.Lock()
	fs.failRead = false
	fs.mu.Unlock()
	fs.setFail(false)
	if err := r.h.deps.WeatherDelay.(*WeatherDelayStateKeeper).SaveUnsaved(context.Background()); err != nil {
		t.Fatalf("SaveUnsaved: %v", err)
	}
	if stored, err := r.st.GetWeatherDelayState(context.Background()); err != nil || !stored.Active {
		t.Fatalf("stored state after the retry = %+v (err %v), want active", stored, err)
	}
}

func TestWeatherDelayResumeClearsAnUnsavedStart(t *testing.T) {
	r, fs := newNotSavedHarness(t)
	r.obs.set([]observation.Observation{statusObservation("player-01", fppStatusValueIdle, r.now)})
	r.start()

	fs.setFail(false)
	r.resume()

	if active, err := r.h.weatherDelayActive(context.Background()); err != nil || active {
		t.Fatalf("weatherDelayActive after resume = %v (err %v), want not active", active, err)
	}
	if err := r.h.deps.WeatherDelay.(*WeatherDelayStateKeeper).SaveUnsaved(context.Background()); err != nil {
		t.Fatalf("SaveUnsaved: %v", err)
	}
	if stored, _ := r.st.GetWeatherDelayState(context.Background()); stored.Active {
		t.Fatal("a retry after resume stored the old active state")
	}
}
