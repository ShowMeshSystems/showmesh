package coordinator

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/showmeshsystems/showmesh/internal/coordinator/api"
	"github.com/showmeshsystems/showmesh/internal/coordinator/config"
	"github.com/showmeshsystems/showmesh/internal/coordinator/identity"
)

// oscStopFixture is a real API over a real OSC-controlled Resolume manager.
type oscStopFixture struct {
	handler  http.Handler
	token    string
	listener *oscListener
	requests func() int64
}

func newOSCStopFixture(t *testing.T) *oscStopFixture {
	t.Helper()
	srv, requests := countingArena(t)
	l := newOSCListener(t)
	mgr := newZeroHTTPTestManager(t)
	mgr.reconcile(context.Background(), []config.ResolumeInstance{{ID: "arena-1", URL: srv.URL, OSCPort: l.port(), Control: config.ResolumeControlOSC}})

	now := time.Now()
	st := mgr.st
	ctx := context.Background()
	svc := identity.NewService(st, func() time.Time { return now }, filepath.Join(t.TempDir(), "identity"), identity.WithLogger(testLogger()))
	admin, err := svc.CreatePrincipal(ctx, "admin-1", identity.KindHuman, identity.RoleAdmin, "bench-pass-1234")
	if err != nil {
		t.Fatalf("create admin principal: %v", err)
	}
	tok, err := svc.IssueToken(ctx, admin.ID, "", nil)
	if err != nil {
		t.Fatalf("issue token: %v", err)
	}
	a := api.New(api.Dependencies{
		Identity: svc, Config: st, Commands: st, NightSessions: st, WeatherDelay: st,
		Resolume: mgr, ResolumeActions: mgr, ResolumeRecovery: mgr,
	}, api.Options{Clock: time.Now, Logger: testLogger()})
	return &oscStopFixture{handler: a.Handler, token: tok.Value, listener: l, requests: requests.Load}
}

func (f *oscStopFixture) post(t *testing.T, path, body string) (int, map[string]any) {
	t.Helper()
	req := httptest.NewRequest(http.MethodPost, path, strings.NewReader(body))
	req.Header.Set("Authorization", "Bearer "+f.token)
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	f.handler.ServeHTTP(rec, req)
	raw, _ := io.ReadAll(rec.Body)
	var m map[string]any
	_ = json.Unmarshal(raw, &m)
	return rec.Code, m
}

// resolumeOutcome finds the one Resolume entry in a list of stop or delay outcomes.
func resolumeOutcome(t *testing.T, list []any) map[string]any {
	t.Helper()
	for _, e := range list {
		m := e.(map[string]any)
		if m["targetKind"] == "resolume" {
			return m
		}
	}
	t.Fatalf("no resolume outcome in %v", list)
	return nil
}

func requireOSCBlackoutSent(t *testing.T, f *oscStopFixture, outcome map[string]any) {
	t.Helper()
	if outcome["outcome"] != "unconfirmable" || !strings.Contains(outcome["outcomeReason"].(string), "Resolume does not answer OSC") {
		t.Errorf("resolume outcome = %v, want unconfirmable saying Resolume does not answer OSC", outcome)
	}
	got := f.listener.read(t, 2)
	if got[0].addr != "/composition/layers/1/clear" || got[1].addr != "/composition/layers/2/clear" {
		t.Errorf("blackout sent %+v, want a clear for each layer", got)
	}
	if n := f.requests(); n != 0 {
		t.Errorf("Arena's web server got %d requests, want 0", n)
	}
}

func TestOSCResolumeLevelOneStopSendsTheBlackoutUnconfirmable(t *testing.T) {
	f := newOSCStopFixture(t)
	code, body := f.post(t, "/api/v1/emergency-stop/stop", `{"idempotencyKey":"stop-1"}`)
	if code != http.StatusOK {
		t.Fatalf("stop: status = %d, body %v", code, body)
	}
	requireOSCBlackoutSent(t, f, resolumeOutcome(t, body["result"].(map[string]any)["stopOutcomes"].([]any)))
}

func TestOSCResolumeWeatherDelayStartSendsTheBlackoutUnconfirmable(t *testing.T) {
	f := newOSCStopFixture(t)
	code, body := f.post(t, "/api/v1/weather-delay/start", `{"idempotencyKey":"delay-1"}`)
	if code != http.StatusOK {
		t.Fatalf("weather delay start: status = %d, body %v", code, body)
	}
	requireOSCBlackoutSent(t, f, resolumeOutcome(t, body["result"].(map[string]any)["targets"].([]any)))
}

func TestOSCResolumeManualRestoreIsRefusedPlainly(t *testing.T) {
	f := newOSCStopFixture(t)
	code, body := f.post(t, "/api/v1/resolume/recovery/restore", `{}`)
	if code != http.StatusConflict || body["detail"] != "Resolume is controlled over OSC, so ShowMesh cannot restore it automatically. Restore it in Arena." {
		t.Fatalf("restore: status = %d, body %v, want 409 with the OSC reason", code, body)
	}
	if n := f.requests(); n != 0 {
		t.Errorf("Arena's web server got %d requests, want 0", n)
	}
}
