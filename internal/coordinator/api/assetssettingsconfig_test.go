package api

import (
	"context"
	"fmt"
	"net/http"
	"strings"
	"testing"

	"github.com/showmeshsystems/showmesh/internal/coordinator/identity"
)

// This file is Track G seam G-4's own test suite (ADR-039), mirroring
// resolumeinstancesconfig_test.go's coverage for the identical shape: the
// configuration write surface, ADR-024 decision 11's same-transaction
// rule, the still-set-env-var 409 (including its deferred-migration remedy
// correction), and the absent/null/present payload discipline — narrowed
// to this kind's own PARTIAL-update semantics (each of the four fields is
// independently optional; only null is rejected, unlike a list's
// absent-or-null-both-rejected rule).

const validAssetsSettingsBody = `{"contentBaseUrl":"https://coordinator.example","maxUploadBytes":1048576,"syncIntervalSeconds":300,"inventoryIntervalSeconds":120}`

func TestPutAssetsSettingsConfigAuthAndScope(t *testing.T) {
	svc, st, _ := newTestIdentityServiceWithStore(t, fixedClock(testNow))
	viewer := mustCreatePrincipal(t, svc, "viewer-1", identity.RoleViewer)
	admin := mustCreatePrincipal(t, svc, "admin-1", identity.RoleAdmin)
	viewerToken := mustIssueToken(t, svc, viewer.ID)
	adminToken := mustIssueToken(t, svc, admin.ID)

	api := New(configTestDeps(svc, st), Options{Clock: fixedClock(testNow), Logger: testLogger()})

	t.Run("unauthenticated", func(t *testing.T) {
		req := newJSONRequest(t, http.MethodPut, "/api/v1/config/assets.settings", validAssetsSettingsBody, nil)
		resp, body := doRawRequest(t, api.Handler, req)
		if resp.StatusCode != http.StatusUnauthorized {
			t.Fatalf("status = %d, want 401; body: %s", resp.StatusCode, body)
		}
	})

	t.Run("viewer forbidden naming config:write", func(t *testing.T) {
		req := newJSONRequest(t, http.MethodPut, "/api/v1/config/assets.settings", validAssetsSettingsBody,
			map[string]string{"Authorization": "Bearer " + viewerToken})
		resp, body := doRawRequest(t, api.Handler, req)
		if resp.StatusCode != http.StatusForbidden {
			t.Fatalf("status = %d, want 403; body: %s", resp.StatusCode, body)
		}
		if !strings.Contains(string(body), "config:write") {
			t.Errorf("body = %s, want it to name the missing scope config:write", body)
		}
	})

	t.Run("admin accepted", func(t *testing.T) {
		req := newJSONRequest(t, http.MethodPut, "/api/v1/config/assets.settings", validAssetsSettingsBody,
			map[string]string{"Authorization": "Bearer " + adminToken})
		resp, body := doRawRequest(t, api.Handler, req)
		if resp.StatusCode != http.StatusOK {
			t.Fatalf("status = %d, want 200; body: %s", resp.StatusCode, body)
		}
		m := decodeMap(t, body)
		if m["revision"] != float64(1) {
			t.Errorf("revision = %v, want 1", m["revision"])
		}
	})
}

func TestGetAssetsSettingsConfigRequiresConfigWriteScope(t *testing.T) {
	svc, st, _ := newTestIdentityServiceWithStore(t, fixedClock(testNow))
	viewer := mustCreatePrincipal(t, svc, "viewer-1", identity.RoleViewer)
	admin := mustCreatePrincipal(t, svc, "admin-1", identity.RoleAdmin)
	viewerToken := mustIssueToken(t, svc, viewer.ID)
	adminToken := mustIssueToken(t, svc, admin.ID)

	api := New(configTestDeps(svc, st), Options{Clock: fixedClock(testNow), Logger: testLogger()})

	for _, path := range []string{"/api/v1/config/assets.settings", "/api/v1/config/assets.settings/revisions"} {
		t.Run(path+"/unauthenticated", func(t *testing.T) {
			resp, body := doRequest(t, api.Handler, "GET", path, nil)
			if resp.StatusCode != http.StatusUnauthorized {
				t.Fatalf("status = %d, want 401; body: %s", resp.StatusCode, body)
			}
		})
		t.Run(path+"/viewer forbidden", func(t *testing.T) {
			resp, body := doRequest(t, api.Handler, "GET", path, map[string]string{"Authorization": "Bearer " + viewerToken})
			if resp.StatusCode != http.StatusForbidden {
				t.Fatalf("status = %d, want 403; body: %s", resp.StatusCode, body)
			}
		})
		t.Run(path+"/admin allowed", func(t *testing.T) {
			resp, body := doRequest(t, api.Handler, "GET", path, map[string]string{"Authorization": "Bearer " + adminToken})
			if path == "/api/v1/config/assets.settings" {
				if resp.StatusCode != http.StatusNotFound {
					t.Fatalf("status = %d, want 404 (scope gate passed, no config exists yet); body: %s", resp.StatusCode, body)
				}
				return
			}
			if resp.StatusCode != http.StatusOK {
				t.Fatalf("status = %d, want 200; body: %s", resp.StatusCode, body)
			}
		})
	}
}

func TestPutAssetsSettingsConfigValidatesBeforeActivation(t *testing.T) {
	svc, st, _ := newTestIdentityServiceWithStore(t, fixedClock(testNow))
	admin := mustCreatePrincipal(t, svc, "admin-1", identity.RoleAdmin)
	adminToken := mustIssueToken(t, svc, admin.ID)
	api := New(configTestDeps(svc, st), Options{Clock: fixedClock(testNow), Logger: testLogger()})

	invalidBody := `{"contentBaseUrl":"not a url"}`
	req := newJSONRequest(t, http.MethodPut, "/api/v1/config/assets.settings", invalidBody,
		map[string]string{"Authorization": "Bearer " + adminToken})
	resp, body := doRawRequest(t, api.Handler, req)
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400; body: %s", resp.StatusCode, body)
	}

	revs, err := st.ListConfigRevisions(context.Background(), "assets.settings", "default")
	if err != nil {
		t.Fatalf("ListConfigRevisions: %v", err)
	}
	if len(revs) != 0 {
		t.Fatalf("revisions after a rejected write = %v, want none", revs)
	}
}

func TestPutAssetsSettingsConfigRejectsNonPositiveMaxUploadBytes(t *testing.T) {
	svc, st, _ := newTestIdentityServiceWithStore(t, fixedClock(testNow))
	admin := mustCreatePrincipal(t, svc, "admin-1", identity.RoleAdmin)
	adminToken := mustIssueToken(t, svc, admin.ID)
	api := New(configTestDeps(svc, st), Options{Clock: fixedClock(testNow), Logger: testLogger()})

	req := newJSONRequest(t, http.MethodPut, "/api/v1/config/assets.settings", `{"maxUploadBytes":0}`,
		map[string]string{"Authorization": "Bearer " + adminToken})
	resp, body := doRawRequest(t, api.Handler, req)
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400; body: %s", resp.StatusCode, body)
	}
}

func TestPutAssetsSettingsConfigRejectsNonPositiveSyncInterval(t *testing.T) {
	svc, st, _ := newTestIdentityServiceWithStore(t, fixedClock(testNow))
	admin := mustCreatePrincipal(t, svc, "admin-1", identity.RoleAdmin)
	adminToken := mustIssueToken(t, svc, admin.ID)
	api := New(configTestDeps(svc, st), Options{Clock: fixedClock(testNow), Logger: testLogger()})

	req := newJSONRequest(t, http.MethodPut, "/api/v1/config/assets.settings", `{"syncIntervalSeconds":0}`,
		map[string]string{"Authorization": "Bearer " + adminToken})
	resp, body := doRawRequest(t, api.Handler, req)
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400; body: %s", resp.StatusCode, body)
	}
}

// TestPutAssetsSettingsConfigFailsClosedOnAuditFailure mirrors
// TestPutResolumeInstancesConfigFailsClosedOnAuditFailure: a real SQLite
// trigger, not a mock, proving ADR-024 decision 11's same-transaction rule
// holds for this kind too.
func TestPutAssetsSettingsConfigFailsClosedOnAuditFailure(t *testing.T) {
	svc, st, storeDir := newTestIdentityServiceWithStore(t, fixedClock(testNow))
	admin := mustCreatePrincipal(t, svc, "admin-1", identity.RoleAdmin)
	adminToken := mustIssueToken(t, svc, admin.ID)
	api := New(configTestDeps(svc, st), Options{Clock: fixedClock(testNow), Logger: testLogger()})

	installFailAuditTrigger(t, storeDir)

	req := newJSONRequest(t, http.MethodPut, "/api/v1/config/assets.settings", validAssetsSettingsBody,
		map[string]string{"Authorization": "Bearer " + adminToken})
	resp, body := doRawRequest(t, api.Handler, req)
	if resp.StatusCode != http.StatusInternalServerError {
		t.Fatalf("status = %d, want 500 (write refused: the audit store is failing); body: %s", resp.StatusCode, body)
	}

	_, err := st.GetConfigObject(context.Background(), "assets.settings", "default")
	if err == nil {
		t.Fatalf("GetConfigObject succeeded after a write whose audit entry failed to write — same-transaction rule violated")
	}
}

func TestPutAssetsSettingsConfigRefusesWhenEnvVarSet(t *testing.T) {
	svc, st, _ := newTestIdentityServiceWithStore(t, fixedClock(testNow))
	admin := mustCreatePrincipal(t, svc, "admin-1", identity.RoleAdmin)
	adminToken := mustIssueToken(t, svc, admin.ID)

	deps := configTestDeps(svc, st)
	deps.AssetSettingsEnvVarsSet = true
	api := New(deps, Options{Clock: fixedClock(testNow), Logger: testLogger()})

	req := newJSONRequest(t, http.MethodPut, "/api/v1/config/assets.settings", validAssetsSettingsBody,
		map[string]string{"Authorization": "Bearer " + adminToken})
	resp, body := doRawRequest(t, api.Handler, req)
	if resp.StatusCode != http.StatusConflict {
		t.Fatalf("status = %d, want 409; body: %s", resp.StatusCode, body)
	}
	m := decodeMap(t, body)
	if m["type"] != ProblemTypeConflict {
		t.Errorf("type = %v, want %v", m["type"], ProblemTypeConflict)
	}
	if !strings.Contains(fmt.Sprint(m["detail"]), "SHOWMESH_ASSET_CONTENT_BASE_URL") {
		t.Errorf("detail = %v, want it to name SHOWMESH_ASSET_CONTENT_BASE_URL", m["detail"])
	}

	if _, err := st.GetConfigObject(context.Background(), "assets.settings", "default"); err == nil {
		t.Fatalf("GetConfigObject succeeded after a refused write")
	}
}

// TestPutAssetsSettingsConfigDeferredMigrationGivesANonDestructiveRemedy
// mirrors TestPutResolumeInstancesConfigDeferredMigrationGivesANonDestructiveRemedy:
// the remedy must differ from the standard one while the migration is
// deferred, because the standard remedy would discard the operator's only
// copy of the settings.
func TestPutAssetsSettingsConfigDeferredMigrationGivesANonDestructiveRemedy(t *testing.T) {
	svc, st, _ := newTestIdentityServiceWithStore(t, fixedClock(testNow))
	admin := mustCreatePrincipal(t, svc, "admin-1", identity.RoleAdmin)
	adminToken := mustIssueToken(t, svc, admin.ID)

	deps := configTestDeps(svc, st)
	deps.AssetSettingsEnvVarsSet = true
	deps.AssetSettingsMigrationDeferred = true
	api := New(deps, Options{Clock: fixedClock(testNow), Logger: testLogger()})

	req := newJSONRequest(t, http.MethodPut, "/api/v1/config/assets.settings", validAssetsSettingsBody,
		map[string]string{"Authorization": "Bearer " + adminToken})
	resp, body := doRawRequest(t, api.Handler, req)
	if resp.StatusCode != http.StatusConflict {
		t.Fatalf("status = %d, want 409; body: %s", resp.StatusCode, body)
	}
	detail := fmt.Sprint(decodeMap(t, body)["detail"])
	if !strings.Contains(detail, "Do NOT remove") {
		t.Errorf("detail = %q, want an explicit warning against removing the variables while the migration is deferred", detail)
	}
}

// TestGetAssetsSettingsConfigDeferredMigrationStatesItRatherThanReportingNothingConfigured
// mirrors the resolume.instances read-side equivalent: absent evidence is
// stated with a reason, never reported as absence.
func TestGetAssetsSettingsConfigDeferredMigrationStatesItRatherThanReportingNothingConfigured(t *testing.T) {
	svc, st, _ := newTestIdentityServiceWithStore(t, fixedClock(testNow))
	admin := mustCreatePrincipal(t, svc, "admin-1", identity.RoleAdmin)
	adminToken := mustIssueToken(t, svc, admin.ID)

	deps := configTestDeps(svc, st)
	deps.AssetSettingsEnvVarsSet = true
	deps.AssetSettingsMigrationDeferred = true
	api := New(deps, Options{Clock: fixedClock(testNow), Logger: testLogger()})

	req := newJSONRequest(t, http.MethodGet, "/api/v1/config/assets.settings", "",
		map[string]string{"Authorization": "Bearer " + adminToken})
	resp, body := doRawRequest(t, api.Handler, req)
	if resp.StatusCode != http.StatusNotFound {
		t.Fatalf("status = %d, want 404; body: %s", resp.StatusCode, body)
	}
	detail := fmt.Sprint(decodeMap(t, body)["detail"])
	if strings.Contains(detail, "has been created yet") {
		t.Errorf("detail = %q, must not report a coordinator nothing has ever configured: settings ARE in effect", detail)
	}
	for _, want := range []string{"SHOWMESH_ASSET_CONTENT_BASE_URL", "could not be"} {
		if !strings.Contains(detail, want) {
			t.Errorf("detail = %q, want it to contain %q", detail, want)
		}
	}
}

// --- absent/null/present, and the partial-update semantics this kind
// --- adds on top of ADR-039 decision 5's baseline rule ---

func TestPutAssetsSettingsConfigRejectsNullContentBaseURL(t *testing.T) {
	svc, st, _ := newTestIdentityServiceWithStore(t, fixedClock(testNow))
	admin := mustCreatePrincipal(t, svc, "admin-1", identity.RoleAdmin)
	adminToken := mustIssueToken(t, svc, admin.ID)
	api := New(configTestDeps(svc, st), Options{Clock: fixedClock(testNow), Logger: testLogger()})

	req := newJSONRequest(t, http.MethodPut, "/api/v1/config/assets.settings", `{"contentBaseUrl":null}`,
		map[string]string{"Authorization": "Bearer " + adminToken})
	resp, body := doRawRequest(t, api.Handler, req)
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400 (null \"contentBaseUrl\"); body: %s", resp.StatusCode, body)
	}
}

func TestPutAssetsSettingsConfigRejectsNullMaxUploadBytes(t *testing.T) {
	svc, st, _ := newTestIdentityServiceWithStore(t, fixedClock(testNow))
	admin := mustCreatePrincipal(t, svc, "admin-1", identity.RoleAdmin)
	adminToken := mustIssueToken(t, svc, admin.ID)
	api := New(configTestDeps(svc, st), Options{Clock: fixedClock(testNow), Logger: testLogger()})

	req := newJSONRequest(t, http.MethodPut, "/api/v1/config/assets.settings", `{"maxUploadBytes":null}`,
		map[string]string{"Authorization": "Bearer " + adminToken})
	resp, body := doRawRequest(t, api.Handler, req)
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400 (null \"maxUploadBytes\"); body: %s", resp.StatusCode, body)
	}
}

func TestPutAssetsSettingsConfigRejectsUnknownTopLevelField(t *testing.T) {
	svc, st, _ := newTestIdentityServiceWithStore(t, fixedClock(testNow))
	admin := mustCreatePrincipal(t, svc, "admin-1", identity.RoleAdmin)
	adminToken := mustIssueToken(t, svc, admin.ID)
	api := New(configTestDeps(svc, st), Options{Clock: fixedClock(testNow), Logger: testLogger()})

	req := newJSONRequest(t, http.MethodPut, "/api/v1/config/assets.settings", `{"contentBaseURL":"https://x"}`,
		map[string]string{"Authorization": "Bearer " + adminToken})
	resp, body := doRawRequest(t, api.Handler, req)
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400 (typo'd top-level key); body: %s", resp.StatusCode, body)
	}
}

// TestPutAssetsSettingsConfigAcceptsExplicitEmptyContentBaseURL is decision
// 5's own positive case for a scalar string field: an explicit "" is the
// way to deliberately disable asset sync, and it must be accepted, not
// confused with absent/null.
func TestPutAssetsSettingsConfigAcceptsExplicitEmptyContentBaseURL(t *testing.T) {
	svc, st, _ := newTestIdentityServiceWithStore(t, fixedClock(testNow))
	admin := mustCreatePrincipal(t, svc, "admin-1", identity.RoleAdmin)
	adminToken := mustIssueToken(t, svc, admin.ID)
	api := New(configTestDeps(svc, st), Options{Clock: fixedClock(testNow), Logger: testLogger()})

	req := newJSONRequest(t, http.MethodPut, "/api/v1/config/assets.settings", `{"contentBaseUrl":""}`,
		map[string]string{"Authorization": "Bearer " + adminToken})
	resp, body := doRawRequest(t, api.Handler, req)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200 (an explicit empty string deliberately disables sync); body: %s", resp.StatusCode, body)
	}
	m := decodeMap(t, body)
	payload := m["payload"].(map[string]any)
	if payload["contentBaseUrl"] != "" {
		t.Errorf("contentBaseUrl = %v, want empty", payload["contentBaseUrl"])
	}
}

// TestPutAssetsSettingsConfigPartialUpdateLeavesOtherFieldsAlone is this
// kind's own load-bearing property beyond decision 5's baseline: a PUT
// naming only one field must not disturb the other three, so an operator
// changing the sync interval never has to already know (and risk
// mistyping) the upload limit or the other interval just to preserve them.
func TestPutAssetsSettingsConfigPartialUpdateLeavesOtherFieldsAlone(t *testing.T) {
	svc, st, _ := newTestIdentityServiceWithStore(t, fixedClock(testNow))
	admin := mustCreatePrincipal(t, svc, "admin-1", identity.RoleAdmin)
	adminToken := mustIssueToken(t, svc, admin.ID)
	api := New(configTestDeps(svc, st), Options{Clock: fixedClock(testNow), Logger: testLogger()})
	auth := map[string]string{"Authorization": "Bearer " + adminToken}

	seedReq := newJSONRequest(t, http.MethodPut, "/api/v1/config/assets.settings", validAssetsSettingsBody, auth)
	if resp, body := doRawRequest(t, api.Handler, seedReq); resp.StatusCode != http.StatusOK {
		t.Fatalf("seed PUT: status = %d; body: %s", resp.StatusCode, body)
	}

	partialReq := newJSONRequest(t, http.MethodPut, "/api/v1/config/assets.settings", `{"syncIntervalSeconds":60}`, auth)
	resp, body := doRawRequest(t, api.Handler, partialReq)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("partial PUT: status = %d, want 200; body: %s", resp.StatusCode, body)
	}

	m := decodeMap(t, body)
	payload := m["payload"].(map[string]any)
	if payload["syncIntervalSeconds"] != float64(60) {
		t.Errorf("syncIntervalSeconds = %v, want 60 (the field this PUT named)", payload["syncIntervalSeconds"])
	}
	if payload["contentBaseUrl"] != "https://coordinator.example" {
		t.Errorf("contentBaseUrl = %v, want it unchanged from the seeded value", payload["contentBaseUrl"])
	}
	if payload["maxUploadBytes"] != float64(1048576) {
		t.Errorf("maxUploadBytes = %v, want it unchanged from the seeded value", payload["maxUploadBytes"])
	}
	if payload["inventoryIntervalSeconds"] != float64(120) {
		t.Errorf("inventoryIntervalSeconds = %v, want it unchanged from the seeded value", payload["inventoryIntervalSeconds"])
	}
}

// TestPutAssetsSettingsConfigPartialUpdateAppliesOverDefaultsWhenNothingStoredYet
// proves the merge baseline is [config.DefaultAssetSettings] rather than a
// zero-value config.AssetSettings when this is the FIRST write — a zero
// value would silently pass a non-positive maxUploadBytes/interval through
// validation as "unchanged", which must never happen.
func TestPutAssetsSettingsConfigPartialUpdateAppliesOverDefaultsWhenNothingStoredYet(t *testing.T) {
	svc, st, _ := newTestIdentityServiceWithStore(t, fixedClock(testNow))
	admin := mustCreatePrincipal(t, svc, "admin-1", identity.RoleAdmin)
	adminToken := mustIssueToken(t, svc, admin.ID)
	api := New(configTestDeps(svc, st), Options{Clock: fixedClock(testNow), Logger: testLogger()})

	req := newJSONRequest(t, http.MethodPut, "/api/v1/config/assets.settings", `{"syncIntervalSeconds":60}`,
		map[string]string{"Authorization": "Bearer " + adminToken})
	resp, body := doRawRequest(t, api.Handler, req)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200; body: %s", resp.StatusCode, body)
	}
	m := decodeMap(t, body)
	payload := m["payload"].(map[string]any)
	if payload["maxUploadBytes"] == float64(0) {
		t.Errorf("maxUploadBytes = %v, want config.DefaultAssetSettings' own positive default, not a zero value merged in", payload["maxUploadBytes"])
	}
}

func TestGetAssetsSettingsConfigRevisionsListsNewestFirst(t *testing.T) {
	svc, st, _ := newTestIdentityServiceWithStore(t, fixedClock(testNow))
	admin := mustCreatePrincipal(t, svc, "admin-1", identity.RoleAdmin)
	adminToken := mustIssueToken(t, svc, admin.ID)
	api := New(configTestDeps(svc, st), Options{Clock: fixedClock(testNow), Logger: testLogger()})

	for _, body := range []string{
		validAssetsSettingsBody,
		`{"syncIntervalSeconds":600}`,
	} {
		req := newJSONRequest(t, http.MethodPut, "/api/v1/config/assets.settings", body,
			map[string]string{"Authorization": "Bearer " + adminToken})
		if resp, respBody := doRawRequest(t, api.Handler, req); resp.StatusCode != http.StatusOK {
			t.Fatalf("PUT: status = %d; body: %s", resp.StatusCode, respBody)
		}
	}

	req := newJSONRequest(t, http.MethodGet, "/api/v1/config/assets.settings/revisions", "",
		map[string]string{"Authorization": "Bearer " + adminToken})
	resp, body := doRawRequest(t, api.Handler, req)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200; body: %s", resp.StatusCode, body)
	}
	m := decodeMap(t, body)
	revs, ok := m["revisions"].([]any)
	if !ok || len(revs) != 2 {
		t.Fatalf("revisions = %v, want 2 entries", m["revisions"])
	}
	first := revs[0].(map[string]any)
	if first["revision"] != float64(2) || first["active"] != true {
		t.Errorf("newest revision = %v, want revision 2, active", first)
	}
}

// TestPutAssetsSettingsConfigRejectsNullBody: a literal null body decodes
// into a nil map with no error, which would read as "every field absent"
// and mint a no-op revision — refused with the same 400 the per-field
// null rules use.
func TestPutAssetsSettingsConfigRejectsNullBody(t *testing.T) {
	svc, st, _ := newTestIdentityServiceWithStore(t, fixedClock(testNow))
	admin := mustCreatePrincipal(t, svc, "admin-1", identity.RoleAdmin)
	adminToken := mustIssueToken(t, svc, admin.ID)
	api := New(configTestDeps(svc, st), Options{Clock: fixedClock(testNow), Logger: testLogger()})

	req := newJSONRequest(t, http.MethodPut, "/api/v1/config/assets.settings", `null`,
		map[string]string{"Authorization": "Bearer " + adminToken})
	resp, body := doRawRequest(t, api.Handler, req)
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400 (a null body is not an object); body: %s", resp.StatusCode, body)
	}
}

// TestPutAssetsSettingsConfigRevisionPreconditionWiring is a smoke test
// proving handlePutAssetsSettingsConfig actually threads the shared
// revision precondition through to its own call site - checked AFTER the
// still-set-env-var refusal above and BEFORE the body is read, exactly
// like every other singleton kind. The full behavioural matrix lives
// once, on the representative kind fpp.endpoints (config_test.go).
func TestPutAssetsSettingsConfigRevisionPreconditionWiring(t *testing.T) {
	svc, st, _ := newTestIdentityServiceWithStore(t, fixedClock(testNow))
	admin := mustCreatePrincipal(t, svc, "admin-1", identity.RoleAdmin)
	adminToken := mustIssueToken(t, svc, admin.ID)
	api := New(configTestDeps(svc, st), Options{Clock: fixedClock(testNow), Logger: testLogger()})

	put := func(contentBaseURL string, headers map[string]string) (*http.Response, []byte) {
		h := map[string]string{"Authorization": "Bearer " + adminToken}
		for k, v := range headers {
			h[k] = v
		}
		body := fmt.Sprintf(`{"contentBaseUrl":%q}`, contentBaseURL)
		req := newJSONRequest(t, http.MethodPut, "/api/v1/config/assets.settings", body, h)
		return doRawRequest(t, api.Handler, req)
	}

	if resp, body := put("https://coordinator-v1.example", nil); resp.StatusCode != http.StatusOK {
		t.Fatalf("unconditional write: status = %d, want 200; body: %s", resp.StatusCode, body)
	}
	if resp, body := put("https://coordinator-v2.example", map[string]string{"If-Match": `"1"`}); resp.StatusCode != http.StatusOK {
		t.Fatalf("matching If-Match: status = %d, want 200; body: %s", resp.StatusCode, body)
	}
	resp, body := put("https://coordinator-v3-refused.example", map[string]string{"If-Match": `"1"`})
	if resp.StatusCode != http.StatusConflict {
		t.Fatalf("stale If-Match: status = %d, want 409; body: %s", resp.StatusCode, body)
	}

	getResp, getBody := doRequest(t, api.Handler, "GET", "/api/v1/config/assets.settings", map[string]string{"Authorization": "Bearer " + adminToken})
	if getResp.StatusCode != http.StatusOK {
		t.Fatalf("GET: status = %d; body: %s", getResp.StatusCode, getBody)
	}
	payload, _ := decodeMap(t, getBody)["payload"].(map[string]any)
	if payload["contentBaseUrl"] != "https://coordinator-v2.example" {
		t.Errorf("payload.contentBaseUrl = %v, want the matching-If-Match writer's value, which must have survived the refused stale write; body: %s", payload["contentBaseUrl"], getBody)
	}
}
