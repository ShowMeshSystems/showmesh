package api

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/showmeshsystems/showmesh/internal/coordinator/displayceiling"
	"github.com/showmeshsystems/showmesh/internal/coordinator/identity"
)

// This file drives GET /api/v1/fpp/brightness and PUT
// /api/v1/fpp/brightness/ceiling through a real [API] with a real
// displayceiling.Reconciler over the real store.

// ceilingAPI wires a coordinator with one FPP endpoint at fppURL, a
// reconciler over the test store, and an operator token.
func ceilingAPI(t *testing.T, fppURL string) (*API, *fppCommandTestSetup, string) {
	t.Helper()
	setup := newFPPCommandTestSetup(t, fixedClock(testNow))
	setup.fppLister.views = []FPPInstanceView{{InstanceID: "bench-fpp", Endpoint: fppURL}}
	deps := setup.deps()
	deps.DisplayCeiling = newTestReconciler(setup)
	api := New(deps, Options{Clock: fixedClock(testNow), Logger: testLogger()})
	operator := mustCreatePrincipal(t, setup.svc, "operator-1", identity.RoleOperator)
	return api, setup, mustIssueToken(t, setup.svc, operator.ID)
}

func newTestReconciler(setup *fppCommandTestSetup) *displayceiling.Reconciler {
	return displayceiling.New(displayceiling.Options{
		Store: setup.st,
		Endpoints: func(ctx context.Context) ([]displayceiling.Endpoint, error) {
			eps, err := currentFPPEndpoints(ctx, setup.fppLister)
			out := make([]displayceiling.Endpoint, 0, len(eps))
			for _, ep := range eps {
				out = append(out, displayceiling.Endpoint{ID: ep.ID, URL: ep.URL})
			}
			return out, err
		},
		Write:  func(context.Context, string, int, int) error { return nil },
		Now:    fixedClock(testNow),
		Logger: testLogger(),
	})
}

func brightnessRequest(t *testing.T, method, path, body, token string) *http.Request {
	t.Helper()
	req := httptest.NewRequest(method, path, strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	return req
}

func ceilingRequest(t *testing.T, body, token string) *http.Request {
	return brightnessRequest(t, http.MethodPut, "/api/v1/fpp/brightness/ceiling", body, token)
}

type brightnessDocForTest struct {
	Ceiling *struct {
		TargetPercent int     `json:"targetPercent"`
		FadeEndsAt    *string `json:"fadeEndsAt"`
		ChangedBy     struct {
			Kind, ID, Name string
		} `json:"changedBy"`
	} `json:"ceiling"`
	Players []struct {
		InstanceID string `json:"instanceId"`
		State      string `json:"state"`
		Reason     string `json:"reason"`
	} `json:"players"`
}

func decodeBrightnessDoc(t *testing.T, body []byte) brightnessDocForTest {
	t.Helper()
	var out brightnessDocForTest
	if err := json.Unmarshal(body, &out); err != nil {
		t.Fatalf("decode brightness document: %v; body: %s", err, body)
	}
	return out
}

func TestBrightnessDocumentBeforeAnyCeiling(t *testing.T) {
	api, _, token := ceilingAPI(t, "http://127.0.0.1:9")
	resp, body := doRawRequest(t, api.Handler, brightnessRequest(t, http.MethodGet, "/api/v1/fpp/brightness", "", token))
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d; body: %s", resp.StatusCode, body)
	}
	if !strings.Contains(string(body), `"ceiling":null`) {
		t.Errorf("body = %s, want ceiling null before the first one", body)
	}
	doc := decodeBrightnessDoc(t, body)
	if len(doc.Players) != 1 || doc.Players[0].InstanceID != "bench-fpp" || doc.Players[0].State != "notReporting" || doc.Players[0].Reason == "" {
		t.Errorf("players = %+v, want bench-fpp not reporting with a reason", doc.Players)
	}
}

func TestSetBrightnessCeilingStoresAuditsAndReturnsTheDocument(t *testing.T) {
	api, setup, token := ceilingAPI(t, "http://127.0.0.1:9")
	resp, body := doRawRequest(t, api.Handler, ceilingRequest(t, `{"targetPercent":60,"fadeSeconds":30}`, token))
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d; body: %s", resp.StatusCode, body)
	}
	doc := decodeBrightnessDoc(t, body)
	if doc.Ceiling == nil || doc.Ceiling.TargetPercent != 60 || doc.Ceiling.FadeEndsAt == nil ||
		doc.Ceiling.ChangedBy.Kind != displayceiling.ChangedByOperator || doc.Ceiling.ChangedBy.Name != "operator-1" {
		t.Fatalf("ceiling = %+v, want 60 fading, set by operator-1", doc.Ceiling)
	}

	rec, ok, err := setup.st.GetDisplayCeiling(context.Background())
	if err != nil || !ok || rec.TargetPercent != 60 {
		t.Fatalf("stored = %+v ok %v err %v, want 60", rec, ok, err)
	}

	entries, err := setup.svc.ListAudit(context.Background(), 0, 100)
	if err != nil {
		t.Fatal(err)
	}
	var kinds []identity.AuditKind
	for _, e := range entries {
		if e.Action == auditActionFPPSetBrightnessCeiling {
			kinds = append(kinds, e.Kind)
		}
	}
	if len(kinds) != 2 {
		t.Errorf("audit entries for %s = %v, want a dispatch and an outcome", auditActionFPPSetBrightnessCeiling, kinds)
	}

	// Omitted fade means none.
	_, body = doRawRequest(t, api.Handler, ceilingRequest(t, `{"targetPercent":0}`, token))
	if doc := decodeBrightnessDoc(t, body); doc.Ceiling.TargetPercent != 0 || doc.Ceiling.FadeEndsAt != nil {
		t.Errorf("ceiling = %+v, want 0 with no fade", doc.Ceiling)
	}
}

func TestSetBrightnessCeilingRefusesBadInputAndStoresNothing(t *testing.T) {
	for _, tt := range []struct{ name, body, wantIn string }{
		{"above 100", `{"targetPercent":101}`, "101%"},
		{"below 0", `{"targetPercent":-1}`, "-1%"},
		{"fade too long", `{"targetPercent":50,"fadeSeconds":86401}`, "86401 seconds"},
		{"negative fade", `{"targetPercent":50,"fadeSeconds":-1}`, "-1 seconds"},
		{"missing", `{"fadeSeconds":0}`, "brightness is missing"},
		{"null", `{"targetPercent":null}`, "not a whole number"},
		{"fractional", `{"targetPercent":50.5}`, "not a whole number"},
		{"unknown field", `{"targetPercent":50,"instanceId":"bench-fpp"}`, "instanceId"},
		{"not an object", `[]`, "not a JSON object"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			api, setup, token := ceilingAPI(t, "http://127.0.0.1:9")
			resp, body := doRawRequest(t, api.Handler, ceilingRequest(t, tt.body, token))
			if resp.StatusCode != http.StatusBadRequest {
				t.Fatalf("status = %d, want 400; body: %s", resp.StatusCode, body)
			}
			if detail, _ := decodeMap(t, body)["detail"].(string); !strings.Contains(detail, tt.wantIn) {
				t.Errorf("detail = %q, want it to contain %q", detail, tt.wantIn)
			}
			if _, ok, _ := setup.st.GetDisplayCeiling(context.Background()); ok {
				t.Error("a refused request stored a ceiling")
			}
		})
	}
}

func TestBrightnessScopes(t *testing.T) {
	setup := newFPPCommandTestSetup(t, fixedClock(testNow))
	deps := setup.deps()
	deps.DisplayCeiling = newTestReconciler(setup)
	api := New(deps, Options{Clock: fixedClock(testNow), Logger: testLogger()})
	viewer := mustCreatePrincipal(t, setup.svc, "viewer-1", identity.RoleViewer)
	token := mustIssueToken(t, setup.svc, viewer.ID)

	if resp, body := doRawRequest(t, api.Handler, brightnessRequest(t, http.MethodGet, "/api/v1/fpp/brightness", "", token)); resp.StatusCode != http.StatusOK {
		t.Errorf("viewer GET status = %d, want 200 under fpp:read; body: %s", resp.StatusCode, body)
	}
	resp, body := doRawRequest(t, api.Handler, ceilingRequest(t, `{"targetPercent":50}`, token))
	if resp.StatusCode != http.StatusForbidden || !strings.Contains(string(body), "fpp:command") {
		t.Errorf("viewer PUT status = %d body %s, want 403 naming fpp:command", resp.StatusCode, body)
	}
}

func TestBrightnessWithoutAReconcilerIsUnavailable(t *testing.T) {
	setup := newFPPCommandTestSetup(t, fixedClock(testNow))
	api := New(setup.deps(), Options{Clock: fixedClock(testNow), Logger: testLogger()})
	operator := mustCreatePrincipal(t, setup.svc, "operator-1", identity.RoleOperator)
	token := mustIssueToken(t, setup.svc, operator.ID)
	resp, body := doRawRequest(t, api.Handler, brightnessRequest(t, http.MethodGet, "/api/v1/fpp/brightness", "", token))
	if resp.StatusCode != http.StatusServiceUnavailable {
		t.Errorf("status = %d, want 503; body: %s", resp.StatusCode, body)
	}
}

func TestPerFPPBrightnessWritesAreGone(t *testing.T) {
	api, _, token := ceilingAPI(t, "http://127.0.0.1:9")
	for _, path := range []string{"/api/v1/fpp/bench-fpp/brightness/ceiling", "/api/v1/fpp/bench-fpp/brightness/transition-gain"} {
		resp, body := doRawRequest(t, api.Handler, brightnessRequest(t, http.MethodPost, path, `{"ceiling":50}`, token))
		if resp.StatusCode != http.StatusNotFound && resp.StatusCode != http.StatusMethodNotAllowed {
			t.Errorf("POST %s status = %d, want the route gone; body: %s", path, resp.StatusCode, body)
		}
	}
}
