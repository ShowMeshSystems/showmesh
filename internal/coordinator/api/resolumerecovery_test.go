package api

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/showmeshsystems/showmesh/internal/coordinator/config"
	"github.com/showmeshsystems/showmesh/internal/coordinator/identity"
	"github.com/showmeshsystems/showmesh/internal/coordinator/store"
)

// This file is Track D seam D-3a's own handler test suite for
// resolumerecovery.go (review finding 5): the five HTTP routes shipped
// with no direct test coverage of their own, and
// decodeResolumeRecoveryConfigPutBody in particular — the function
// enforcing this project's own repeated "an absent JSON key is not an
// empty value" rule (CLAUDE.md) — shipped with zero tests despite
// implementing exactly that rule. Reuses mutableResolumeRecoveryProvider
// and mutableResolumeRecoveryConfigStore where a lighter fake suffices
// (decodeResolumeRecoveryConfigPutBody's own direct tests need neither),
// and otherwise follows resolumeaction_test.go's real-store,
// real-identity.Service pattern.

type resolumeRecoveryTestSetup struct {
	st       *store.Store
	storeDir string
	svc      identity.Service
	rec      *mutableResolumeRecoveryProvider
}

func newResolumeRecoveryTestSetup(t *testing.T, now func() time.Time) *resolumeRecoveryTestSetup {
	t.Helper()
	dir := t.TempDir()
	storeDir := filepath.Join(dir, "db")
	st, err := store.Open(context.Background(), storeDir, nil, store.WithClock(now))
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(func() { _ = st.Close() })
	svc := identity.NewService(st, now, filepath.Join(dir, "data"), identity.WithLogger(testLogger()))
	return &resolumeRecoveryTestSetup{
		st: st, storeDir: storeDir, svc: svc,
		rec: &mutableResolumeRecoveryProvider{},
	}
}

func (s *resolumeRecoveryTestSetup) deps() Dependencies {
	return Dependencies{
		Nodes: &fakeNodeLister{}, FPP: &fakeFPPLister{}, Observations: &fakeObservationLister{},
		Events: &fakeEventReader{}, Collectors: &fakeCollectorStatusLister{},
		Identity: s.svc, Config: s.st, ResolumeRecovery: s.rec, ResolumeRecoverySettleSeconds: 8,
	}
}

// --- GET /resolume/recovery: the open read -------------------------------

// TestGetResolumeRecoveryIsOpenAndReturnsFields proves the route needs no
// credential at all (build contract §1.3's "the dashboard renders with no
// session") and that every field — including resolumeConfigured and a last
// restore's own omittedLayerCount, both added after this route first
// shipped — comes through on a real response.
func TestGetResolumeRecoveryIsOpenAndReturnsFields(t *testing.T) {
	setup := newResolumeRecoveryTestSetup(t, fixedClock(testNow))
	setup.rec.setRecord([]ResolumeRecoveryRecordEntryView{{Layer: "Whole House 1", State: "dark"}})
	setup.rec.setLastReport(&ResolumeRecoveryRestoreReportView{
		StartedAt: "2026-08-16T00:00:00Z", FinishedAt: "2026-08-16T00:00:01Z",
		Trigger: "manual", Outcome: "restored", Principal: "admin-1",
		Layers:            []ResolumeRecoveryRestoreLayerView{{Layer: "Whole House 1", Result: "restored"}},
		OmittedLayerCount: 2,
	})
	api := New(setup.deps(), Options{Clock: fixedClock(testNow), Logger: testLogger()})

	req := httptest.NewRequest(http.MethodGet, "/api/v1/resolume/recovery", nil)
	resp, body := doRawRequest(t, api.Handler, req)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200 (no credential presented); body: %s", resp.StatusCode, body)
	}
	m := decodeMap(t, body)
	if m["resolumeConfigured"] != true {
		t.Errorf("resolumeConfigured = %v, want true (a real ResolumeRecoveryProvider is wired)", m["resolumeConfigured"])
	}
	if m["autoRestoreEnabled"] != true {
		t.Errorf("autoRestoreEnabled = %v, want true (default: nothing has ever been written)", m["autoRestoreEnabled"])
	}
	if m["autoRestoreConfigured"] != false {
		t.Errorf("autoRestoreConfigured = %v, want false", m["autoRestoreConfigured"])
	}
	record, _ := m["record"].([]any)
	if len(record) != 1 {
		t.Fatalf("record = %v, want 1 entry", m["record"])
	}
	lastRestore, _ := m["lastRestore"].(map[string]any)
	if lastRestore == nil || lastRestore["outcome"] != "restored" {
		t.Fatalf("lastRestore = %v, want outcome \"restored\"", m["lastRestore"])
	}
	if lastRestore["omittedLayerCount"] != float64(2) {
		t.Errorf("lastRestore.omittedLayerCount = %v, want 2", lastRestore["omittedLayerCount"])
	}
}

// TestGetResolumeRecoveryUnconfiguredReportsResolumeConfiguredFalse proves
// resolumeConfigured is false when Dependencies.ResolumeRecovery is never
// wired (noResolumeRecoveryProvider, this API's nil-safe default) —
// distinct from autoRestoreConfigured, which is about the toggle, not
// whether a real Resolume instance exists at all.
func TestGetResolumeRecoveryUnconfiguredReportsResolumeConfiguredFalse(t *testing.T) {
	deps := Dependencies{
		Nodes: &fakeNodeLister{}, FPP: &fakeFPPLister{}, Observations: &fakeObservationLister{},
		Events: &fakeEventReader{}, Collectors: &fakeCollectorStatusLister{},
	}
	api := New(deps, Options{Clock: fixedClock(testNow), Logger: testLogger()})

	req := httptest.NewRequest(http.MethodGet, "/api/v1/resolume/recovery", nil)
	resp, body := doRawRequest(t, api.Handler, req)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200; body: %s", resp.StatusCode, body)
	}
	m := decodeMap(t, body)
	if m["resolumeConfigured"] != false {
		t.Errorf("resolumeConfigured = %v, want false (no ResolumeRecoveryProvider wired)", m["resolumeConfigured"])
	}
	if m["lastRestore"] != nil {
		t.Errorf("lastRestore = %v, want null (no restore has ever run)", m["lastRestore"])
	}
}

// --- POST /resolume/recovery/restore: the manual restore ------------------

// TestPostResolumeRecoveryRestoreAuthAndScope is this route's own
// acceptance criterion: unauthenticated -> 401, a viewer (holds no
// resolume:action) -> 403 naming resolume:action, an operator (holds
// resolume:action) -> 200 with a real restore report.
func TestPostResolumeRecoveryRestoreAuthAndScope(t *testing.T) {
	setup := newResolumeRecoveryTestSetup(t, fixedClock(testNow))
	setup.rec.setRestoreResult(ResolumeRecoveryRestoreReportView{
		StartedAt: "2026-08-16T00:00:00Z", FinishedAt: "2026-08-16T00:00:01Z",
		Trigger: "manual", Outcome: "restored",
		Layers: []ResolumeRecoveryRestoreLayerView{{Layer: "Whole House 1", Result: "restored"}},
	})
	viewer := mustCreatePrincipal(t, setup.svc, "viewer-1", identity.RoleViewer)
	operator := mustCreatePrincipal(t, setup.svc, "operator-1", identity.RoleOperator)
	viewerToken := mustIssueToken(t, setup.svc, viewer.ID)
	operatorToken := mustIssueToken(t, setup.svc, operator.ID)
	api := New(setup.deps(), Options{Clock: fixedClock(testNow), Logger: testLogger()})

	t.Run("unauthenticated", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodPost, "/api/v1/resolume/recovery/restore", nil)
		resp, body := doRawRequest(t, api.Handler, req)
		if resp.StatusCode != http.StatusUnauthorized {
			t.Fatalf("status = %d, want 401; body: %s", resp.StatusCode, body)
		}
	})

	t.Run("viewer forbidden naming resolume:action", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodPost, "/api/v1/resolume/recovery/restore", nil)
		req.Header.Set("Authorization", "Bearer "+viewerToken)
		resp, body := doRawRequest(t, api.Handler, req)
		if resp.StatusCode != http.StatusForbidden {
			t.Fatalf("status = %d, want 403; body: %s", resp.StatusCode, body)
		}
		if !strings.Contains(string(body), "resolume:action") {
			t.Errorf("body = %s, want it to name the missing scope resolume:action", body)
		}
	})

	t.Run("operator accepted", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodPost, "/api/v1/resolume/recovery/restore", nil)
		req.Header.Set("Authorization", "Bearer "+operatorToken)
		resp, body := doRawRequest(t, api.Handler, req)
		if resp.StatusCode != http.StatusOK {
			t.Fatalf("status = %d, want 200; body: %s", resp.StatusCode, body)
		}
		m := decodeMap(t, body)
		restore, _ := m["restore"].(map[string]any)
		if restore == nil || restore["outcome"] != "restored" {
			t.Errorf("restore = %v, want outcome \"restored\"", m["restore"])
		}
		if restore["principal"] != "operator-1" {
			t.Errorf("restore.principal = %v, want %q (the acting principal's own name)", restore["principal"], "operator-1")
		}
	})
}

// TestPostResolumeRecoveryRestoreProviderFailureIs500 proves a
// ResolumeRecoveryProvider.Restore error is surfaced as 500, never
// swallowed into a false 200.
func TestPostResolumeRecoveryRestoreProviderFailureIs500(t *testing.T) {
	setup := newResolumeRecoveryTestSetup(t, fixedClock(testNow))
	setup.rec.setRestoreErr(errors.New("simulated Restore failure"))
	operator := mustCreatePrincipal(t, setup.svc, "operator-1", identity.RoleOperator)
	operatorToken := mustIssueToken(t, setup.svc, operator.ID)
	api := New(setup.deps(), Options{Clock: fixedClock(testNow), Logger: testLogger()})

	req := httptest.NewRequest(http.MethodPost, "/api/v1/resolume/recovery/restore", nil)
	req.Header.Set("Authorization", "Bearer "+operatorToken)
	resp, body := doRawRequest(t, api.Handler, req)
	if resp.StatusCode != http.StatusInternalServerError {
		t.Fatalf("status = %d, want 500; body: %s", resp.StatusCode, body)
	}
}

// --- GET /config/resolume.recovery: the toggle's revision metadata -------

// TestGetResolumeRecoveryConfigRequiresConfigWriteScope proves this GET —
// unlike GET /resolume/recovery above — is gated by config:write even
// though it never returns 404 (build contract §1.1: the toggle always has
// a well-defined default).
func TestGetResolumeRecoveryConfigRequiresConfigWriteScope(t *testing.T) {
	setup := newResolumeRecoveryTestSetup(t, fixedClock(testNow))
	viewer := mustCreatePrincipal(t, setup.svc, "viewer-1", identity.RoleViewer)
	admin := mustCreatePrincipal(t, setup.svc, "admin-1", identity.RoleAdmin)
	viewerToken := mustIssueToken(t, setup.svc, viewer.ID)
	adminToken := mustIssueToken(t, setup.svc, admin.ID)
	api := New(setup.deps(), Options{Clock: fixedClock(testNow), Logger: testLogger()})

	t.Run("unauthenticated", func(t *testing.T) {
		resp, body := doRequest(t, api.Handler, "GET", "/api/v1/config/resolume.recovery", nil)
		if resp.StatusCode != http.StatusUnauthorized {
			t.Fatalf("status = %d, want 401; body: %s", resp.StatusCode, body)
		}
	})

	t.Run("viewer forbidden", func(t *testing.T) {
		resp, body := doRequest(t, api.Handler, "GET", "/api/v1/config/resolume.recovery",
			map[string]string{"Authorization": "Bearer " + viewerToken})
		if resp.StatusCode != http.StatusForbidden {
			t.Fatalf("status = %d, want 403; body: %s", resp.StatusCode, body)
		}
	})

	t.Run("admin default shape when nothing written", func(t *testing.T) {
		resp, body := doRequest(t, api.Handler, "GET", "/api/v1/config/resolume.recovery",
			map[string]string{"Authorization": "Bearer " + adminToken})
		if resp.StatusCode != http.StatusOK {
			t.Fatalf("status = %d, want 200 (a well-defined default, never 404); body: %s", resp.StatusCode, body)
		}
		m := decodeMap(t, body)
		if m["revision"] != float64(0) {
			t.Errorf("revision = %v, want 0", m["revision"])
		}
		if m["source"] != "default" {
			t.Errorf("source = %v, want %q", m["source"], "default")
		}
		payload, _ := m["payload"].(map[string]any)
		if payload["autoRestoreEnabled"] != config.ResolumeRecoveryDefaultEnabled {
			t.Errorf("payload.autoRestoreEnabled = %v, want %v (built-in default)", payload["autoRestoreEnabled"], config.ResolumeRecoveryDefaultEnabled)
		}
	})
}

// TestGetResolumeRecoveryConfigReturnsStoredValueAfterWrite proves the
// GET reflects a value actually written via the PUT below, not merely the
// default shape.
func TestGetResolumeRecoveryConfigReturnsStoredValueAfterWrite(t *testing.T) {
	setup := newResolumeRecoveryTestSetup(t, fixedClock(testNow))
	admin := mustCreatePrincipal(t, setup.svc, "admin-1", identity.RoleAdmin)
	adminToken := mustIssueToken(t, setup.svc, admin.ID)
	api := New(setup.deps(), Options{Clock: fixedClock(testNow), Logger: testLogger()})

	putReq := newJSONRequest(t, http.MethodPut, "/api/v1/config/resolume.recovery", `{"autoRestoreEnabled":false}`,
		map[string]string{"Authorization": "Bearer " + adminToken})
	if resp, body := doRawRequest(t, api.Handler, putReq); resp.StatusCode != http.StatusOK {
		t.Fatalf("PUT: status = %d, want 200; body: %s", resp.StatusCode, body)
	}

	resp, body := doRequest(t, api.Handler, "GET", "/api/v1/config/resolume.recovery",
		map[string]string{"Authorization": "Bearer " + adminToken})
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("GET: status = %d, want 200; body: %s", resp.StatusCode, body)
	}
	m := decodeMap(t, body)
	if m["revision"] != float64(1) {
		t.Errorf("revision = %v, want 1", m["revision"])
	}
	if m["source"] != config.ResolumeRecoverySourceAPI {
		t.Errorf("source = %v, want %q", m["source"], config.ResolumeRecoverySourceAPI)
	}
	payload, _ := m["payload"].(map[string]any)
	if payload["autoRestoreEnabled"] != false {
		t.Errorf("payload.autoRestoreEnabled = %v, want false (the stored value)", payload["autoRestoreEnabled"])
	}
}

// --- PUT /config/resolume.recovery: the toggle write, and its own
//     absent/null/present body-decoding rule -------------------------------

// TestPutResolumeRecoveryConfigRequiresConfigWriteScope proves the write
// gate independent of body-decoding correctness.
func TestPutResolumeRecoveryConfigRequiresConfigWriteScope(t *testing.T) {
	setup := newResolumeRecoveryTestSetup(t, fixedClock(testNow))
	viewer := mustCreatePrincipal(t, setup.svc, "viewer-1", identity.RoleViewer)
	viewerToken := mustIssueToken(t, setup.svc, viewer.ID)
	api := New(setup.deps(), Options{Clock: fixedClock(testNow), Logger: testLogger()})

	t.Run("unauthenticated", func(t *testing.T) {
		req := newJSONRequest(t, http.MethodPut, "/api/v1/config/resolume.recovery", `{"autoRestoreEnabled":true}`, nil)
		resp, body := doRawRequest(t, api.Handler, req)
		if resp.StatusCode != http.StatusUnauthorized {
			t.Fatalf("status = %d, want 401; body: %s", resp.StatusCode, body)
		}
	})

	t.Run("viewer forbidden naming config:write", func(t *testing.T) {
		req := newJSONRequest(t, http.MethodPut, "/api/v1/config/resolume.recovery", `{"autoRestoreEnabled":true}`,
			map[string]string{"Authorization": "Bearer " + viewerToken})
		resp, body := doRawRequest(t, api.Handler, req)
		if resp.StatusCode != http.StatusForbidden {
			t.Fatalf("status = %d, want 403; body: %s", resp.StatusCode, body)
		}
		if !strings.Contains(string(body), "config:write") {
			t.Errorf("body = %s, want it to name the missing scope config:write", body)
		}
	})
}

// TestPutResolumeRecoveryConfigBodyDecoding is finding 5's headline case,
// driven THROUGH the real handler: an absent "autoRestoreEnabled" key, an
// explicit null, and a valid true/false must all be handled per
// decodeResolumeRecoveryConfigPutBody's own contract — an absent key is
// refused, never silently defaulted (CLAUDE.md's own repeated "an absent
// JSON key is not an empty value" lesson).
func TestPutResolumeRecoveryConfigBodyDecoding(t *testing.T) {
	setup := newResolumeRecoveryTestSetup(t, fixedClock(testNow))
	admin := mustCreatePrincipal(t, setup.svc, "admin-1", identity.RoleAdmin)
	adminToken := mustIssueToken(t, setup.svc, admin.ID)
	api := New(setup.deps(), Options{Clock: fixedClock(testNow), Logger: testLogger()})
	authHeader := map[string]string{"Authorization": "Bearer " + adminToken}

	t.Run("absent key rejected, no revision created", func(t *testing.T) {
		req := newJSONRequest(t, http.MethodPut, "/api/v1/config/resolume.recovery", `{}`, authHeader)
		resp, body := doRawRequest(t, api.Handler, req)
		if resp.StatusCode != http.StatusBadRequest {
			t.Fatalf("status = %d, want 400 (absent \"autoRestoreEnabled\"); body: %s", resp.StatusCode, body)
		}
		if !strings.Contains(string(body), "autoRestoreEnabled") {
			t.Errorf("body = %s, want it to name the missing field", body)
		}
		if _, err := setup.st.GetConfigObject(context.Background(), config.ResolumeRecoveryConfigKind, config.ResolumeRecoveryConfigObjectID); err == nil {
			t.Fatalf("GetConfigObject succeeded after a rejected write — an absent key must never silently activate a revision")
		}
	})

	t.Run("explicit null rejected, no revision created", func(t *testing.T) {
		req := newJSONRequest(t, http.MethodPut, "/api/v1/config/resolume.recovery", `{"autoRestoreEnabled":null}`, authHeader)
		resp, body := doRawRequest(t, api.Handler, req)
		if resp.StatusCode != http.StatusBadRequest {
			t.Fatalf("status = %d, want 400 (null \"autoRestoreEnabled\"); body: %s", resp.StatusCode, body)
		}
		if _, err := setup.st.GetConfigObject(context.Background(), config.ResolumeRecoveryConfigKind, config.ResolumeRecoveryConfigObjectID); err == nil {
			t.Fatalf("GetConfigObject succeeded after a null-value write — a JSON null must never decode to a usable value")
		}
	})

	t.Run("unknown top-level field rejected", func(t *testing.T) {
		req := newJSONRequest(t, http.MethodPut, "/api/v1/config/resolume.recovery", `{"autoRestore":true}`, authHeader)
		resp, body := doRawRequest(t, api.Handler, req)
		if resp.StatusCode != http.StatusBadRequest {
			t.Fatalf("status = %d, want 400 (unrecognized field \"autoRestore\"); body: %s", resp.StatusCode, body)
		}
	})

	t.Run("true accepted", func(t *testing.T) {
		req := newJSONRequest(t, http.MethodPut, "/api/v1/config/resolume.recovery", `{"autoRestoreEnabled":true}`, authHeader)
		resp, body := doRawRequest(t, api.Handler, req)
		if resp.StatusCode != http.StatusOK {
			t.Fatalf("status = %d, want 200; body: %s", resp.StatusCode, body)
		}
		m := decodeMap(t, body)
		if m["revision"] != float64(1) {
			t.Errorf("revision = %v, want 1 (the first successful write, after two rejected ones above)", m["revision"])
		}
	})

	t.Run("false accepted, writes a second revision", func(t *testing.T) {
		req := newJSONRequest(t, http.MethodPut, "/api/v1/config/resolume.recovery", `{"autoRestoreEnabled":false}`, authHeader)
		resp, body := doRawRequest(t, api.Handler, req)
		if resp.StatusCode != http.StatusOK {
			t.Fatalf("status = %d, want 200; body: %s", resp.StatusCode, body)
		}
		m := decodeMap(t, body)
		if m["revision"] != float64(2) {
			t.Errorf("revision = %v, want 2", m["revision"])
		}
		payload, _ := m["payload"].(map[string]any)
		if payload["autoRestoreEnabled"] != false {
			t.Errorf("payload.autoRestoreEnabled = %v, want false", payload["autoRestoreEnabled"])
		}
	})
}

// --- GET /config/resolume.recovery/revisions ------------------------------

// TestGetResolumeRecoveryConfigRevisionsRequiresConfigWriteScope proves the
// gate, and that revision history accumulates newest-first after more than
// one write.
func TestGetResolumeRecoveryConfigRevisionsRequiresConfigWriteScope(t *testing.T) {
	setup := newResolumeRecoveryTestSetup(t, fixedClock(testNow))
	viewer := mustCreatePrincipal(t, setup.svc, "viewer-1", identity.RoleViewer)
	admin := mustCreatePrincipal(t, setup.svc, "admin-1", identity.RoleAdmin)
	viewerToken := mustIssueToken(t, setup.svc, viewer.ID)
	adminToken := mustIssueToken(t, setup.svc, admin.ID)
	api := New(setup.deps(), Options{Clock: fixedClock(testNow), Logger: testLogger()})

	t.Run("viewer forbidden", func(t *testing.T) {
		resp, body := doRequest(t, api.Handler, "GET", "/api/v1/config/resolume.recovery/revisions",
			map[string]string{"Authorization": "Bearer " + viewerToken})
		if resp.StatusCode != http.StatusForbidden {
			t.Fatalf("status = %d, want 403; body: %s", resp.StatusCode, body)
		}
	})

	t.Run("admin sees history after writes, newest first", func(t *testing.T) {
		for _, body := range []string{`{"autoRestoreEnabled":false}`, `{"autoRestoreEnabled":true}`} {
			req := newJSONRequest(t, http.MethodPut, "/api/v1/config/resolume.recovery", body,
				map[string]string{"Authorization": "Bearer " + adminToken})
			if resp, respBody := doRawRequest(t, api.Handler, req); resp.StatusCode != http.StatusOK {
				t.Fatalf("PUT: status = %d; body: %s", resp.StatusCode, respBody)
			}
		}

		resp, body := doRequest(t, api.Handler, "GET", "/api/v1/config/resolume.recovery/revisions",
			map[string]string{"Authorization": "Bearer " + adminToken})
		if resp.StatusCode != http.StatusOK {
			t.Fatalf("status = %d, want 200; body: %s", resp.StatusCode, body)
		}
		m := decodeMap(t, body)
		revisions, _ := m["revisions"].([]any)
		if len(revisions) != 2 {
			t.Fatalf("revisions = %v, want 2 elements", m["revisions"])
		}
		first := revisions[0].(map[string]any)
		second := revisions[1].(map[string]any)
		if first["revision"] != float64(2) || second["revision"] != float64(1) {
			t.Errorf("revisions order = [%v, %v], want [2, 1] (newest first)", first["revision"], second["revision"])
		}
	})
}

// --- decodeResolumeRecoveryConfigPutBody: direct unit tests ---------------

// TestDecodeResolumeRecoveryConfigPutBody drives the decoding function
// directly (not just through the handler above), covering every case
// finding 5 named: absent key, explicit null, wrong type, an unknown
// extra top-level field, and the valid true/false cases.
func TestDecodeResolumeRecoveryConfigPutBody(t *testing.T) {
	tests := []struct {
		name      string
		body      string
		wantValue bool
		wantErr   bool
	}{
		{"absent key", `{}`, false, true},
		{"explicit null", `{"autoRestoreEnabled":null}`, false, true},
		{"wrong type (string)", `{"autoRestoreEnabled":"true"}`, false, true},
		{"unknown extra top-level field", `{"autoRestoreEnabled":true,"extra":1}`, false, true},
		{"unknown field only", `{"other":true}`, false, true},
		{"valid true", `{"autoRestoreEnabled":true}`, true, false},
		{"valid false", `{"autoRestoreEnabled":false}`, false, false},
		{"not a JSON object", `[]`, false, true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := decodeResolumeRecoveryConfigPutBody(strings.NewReader(tt.body))
			if (err != nil) != tt.wantErr {
				t.Fatalf("err = %v, wantErr = %v", err, tt.wantErr)
			}
			if !tt.wantErr && got != tt.wantValue {
				t.Errorf("value = %v, want %v", got, tt.wantValue)
			}
		})
	}
}

// TestPutResolumeRecoveryConfigRevisionPreconditionWiring is a smoke test
// proving handlePutResolumeRecoveryConfig actually threads the shared
// revision precondition through to its own call site. The full
// behavioural matrix lives once, on the representative kind
// fpp.endpoints (config_test.go).
func TestPutResolumeRecoveryConfigRevisionPreconditionWiring(t *testing.T) {
	setup := newResolumeRecoveryTestSetup(t, fixedClock(testNow))
	admin := mustCreatePrincipal(t, setup.svc, "admin-1", identity.RoleAdmin)
	adminToken := mustIssueToken(t, setup.svc, admin.ID)
	api := New(setup.deps(), Options{Clock: fixedClock(testNow), Logger: testLogger()})

	put := func(enabled bool, headers map[string]string) (*http.Response, []byte) {
		h := map[string]string{"Authorization": "Bearer " + adminToken}
		for k, v := range headers {
			h[k] = v
		}
		body := `{"autoRestoreEnabled":false}`
		if enabled {
			body = `{"autoRestoreEnabled":true}`
		}
		req := newJSONRequest(t, http.MethodPut, "/api/v1/config/resolume.recovery", body, h)
		return doRawRequest(t, api.Handler, req)
	}

	if resp, body := put(true, nil); resp.StatusCode != http.StatusOK {
		t.Fatalf("unconditional write: status = %d, want 200; body: %s", resp.StatusCode, body)
	}
	if resp, body := put(false, map[string]string{"If-Match": `"1"`}); resp.StatusCode != http.StatusOK {
		t.Fatalf("matching If-Match: status = %d, want 200; body: %s", resp.StatusCode, body)
	}
	resp, body := put(true, map[string]string{"If-Match": `"1"`})
	if resp.StatusCode != http.StatusConflict {
		t.Fatalf("stale If-Match: status = %d, want 409; body: %s", resp.StatusCode, body)
	}

	getResp, getBody := doRequest(t, api.Handler, "GET", "/api/v1/config/resolume.recovery", map[string]string{"Authorization": "Bearer " + adminToken})
	if getResp.StatusCode != http.StatusOK {
		t.Fatalf("GET: status = %d; body: %s", getResp.StatusCode, getBody)
	}
	payload, _ := decodeMap(t, getBody)["payload"].(map[string]any)
	if payload["autoRestoreEnabled"] != false {
		t.Errorf("payload.autoRestoreEnabled = %v, want false (the matching-If-Match writer's value, which must have survived the refused stale write); body: %s", payload["autoRestoreEnabled"], getBody)
	}
}
