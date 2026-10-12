package api

import (
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/showmeshsystems/showmesh/internal/coordinator/identity"
	"github.com/showmeshsystems/showmesh/internal/coordinator/versions"
)

type releaseCheckerFake struct {
	status  versions.Status
	applied []versions.Settings
}

func (f *releaseCheckerFake) Status() versions.Status { return f.status }
func (f *releaseCheckerFake) SetSettings(s versions.Settings) {
	f.applied = append(f.applied, s)
	f.status.Enabled, f.status.Interval = s.Enabled, s.Interval
}

const releaseSettingsPath = "/api/v1/config/release.settings"

func releaseSettingsTestAPI(t *testing.T, change func(*Dependencies)) (api *API, adminToken, viewerToken string, checker *releaseCheckerFake) {
	t.Helper()
	svc, st, _ := newTestIdentityServiceWithStore(t, fixedClock(testNow))
	adminToken = mustIssueToken(t, svc, mustCreatePrincipal(t, svc, "admin-1", identity.RoleAdmin).ID)
	viewerToken = mustIssueToken(t, svc, mustCreatePrincipal(t, svc, "viewer-1", identity.RoleViewer).ID)
	checker = &releaseCheckerFake{status: versions.Status{Enabled: true, Interval: 6 * time.Hour}}
	deps := configTestDeps(svc, st)
	deps.Releases = checker
	if change != nil {
		change(&deps)
	}
	return New(deps, Options{Clock: fixedClock(testNow), Logger: testLogger()}), adminToken, viewerToken, checker
}

func putReleaseSettings(t *testing.T, api *API, token, body string, headers map[string]string) (*http.Response, map[string]any) {
	t.Helper()
	h := map[string]string{"Authorization": "Bearer " + token}
	for k, v := range headers {
		h[k] = v
	}
	resp, raw := doRawRequest(t, api.Handler, newJSONRequest(t, http.MethodPut, releaseSettingsPath, body, h))
	return resp, decodeMap(t, raw)
}

func TestReleaseSettingsReadsTheDefaultBeforeAnythingIsStored(t *testing.T) {
	api, admin, viewer, _ := releaseSettingsTestAPI(t, nil)

	if resp, body := doRequest(t, api.Handler, http.MethodGet, releaseSettingsPath, map[string]string{"Authorization": "Bearer " + viewer}); resp.StatusCode != http.StatusForbidden {
		t.Fatalf("viewer GET status = %d, want 403: %s", resp.StatusCode, body)
	}
	resp, body := doRequest(t, api.Handler, http.MethodGet, releaseSettingsPath, map[string]string{"Authorization": "Bearer " + admin})
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200: %s", resp.StatusCode, body)
	}
	m := decodeMap(t, body)
	payload := m["payload"].(map[string]any)
	if m["revision"] != float64(0) || m["source"] != "default" || payload["enabled"] != true || payload["checkIntervalSeconds"] != float64(21600) {
		t.Fatalf("body = %s, want revision 0, source default, on, 21600 seconds", body)
	}
}

func TestPutReleaseSettingsStoresARevisionAndAppliesItToTheRunningCheck(t *testing.T) {
	api, admin, viewer, checker := releaseSettingsTestAPI(t, nil)

	if resp, _ := putReleaseSettings(t, api, viewer, `{"enabled":false}`, nil); resp.StatusCode != http.StatusForbidden {
		t.Fatalf("viewer PUT status = %d, want 403", resp.StatusCode)
	}
	resp, m := putReleaseSettings(t, api, admin, `{"enabled":false}`, map[string]string{"If-None-Match": "*"})
	if resp.StatusCode != http.StatusOK || m["revision"] != float64(1) || m["source"] != "api" {
		t.Fatalf("first PUT: status %d, body %v", resp.StatusCode, m)
	}
	if p := m["payload"].(map[string]any); p["enabled"] != false || p["checkIntervalSeconds"] != float64(21600) {
		t.Fatalf("payload = %v, want off with the interval kept at its default", p)
	}

	// A field left out keeps its stored value.
	resp, m = putReleaseSettings(t, api, admin, `{"checkIntervalSeconds":900}`, map[string]string{"If-Match": `"1"`})
	if resp.StatusCode != http.StatusOK || m["revision"] != float64(2) {
		t.Fatalf("second PUT: status %d, body %v", resp.StatusCode, m)
	}
	if p := m["payload"].(map[string]any); p["enabled"] != false || p["checkIntervalSeconds"] != float64(900) {
		t.Fatalf("payload = %v, want still off and 900 seconds", p)
	}
	want := []versions.Settings{{Enabled: false, Interval: 6 * time.Hour}, {Enabled: false, Interval: 15 * time.Minute}}
	if len(checker.applied) != 2 || checker.applied[0] != want[0] || checker.applied[1] != want[1] {
		t.Fatalf("applied = %+v, want %+v", checker.applied, want)
	}

	_, body := doRequest(t, api.Handler, http.MethodGet, releaseSettingsPath, map[string]string{"Authorization": "Bearer " + admin})
	if got := decodeMap(t, body); got["revision"] != float64(2) {
		t.Fatalf("GET after two writes = %s, want revision 2", body)
	}
}

func TestPutReleaseSettingsRefusesAStaleRevisionAndBadValues(t *testing.T) {
	api, admin, _, checker := releaseSettingsTestAPI(t, nil)
	if resp, m := putReleaseSettings(t, api, admin, `{"enabled":true}`, nil); resp.StatusCode != http.StatusOK {
		t.Fatalf("seed PUT: status %d, body %v", resp.StatusCode, m)
	}

	if resp, m := putReleaseSettings(t, api, admin, `{"enabled":false}`, map[string]string{"If-Match": `"7"`}); resp.StatusCode != http.StatusConflict {
		t.Fatalf("stale If-Match: status %d, want 409: %v", resp.StatusCode, m)
	}
	resp, m := putReleaseSettings(t, api, admin, `{"checkIntervalSeconds":299}`, nil)
	if resp.StatusCode != http.StatusBadRequest || m["detail"] != "The check interval is too short. Set it to 300 seconds or more." {
		t.Fatalf("short interval: status %d, body %v", resp.StatusCode, m)
	}
	for _, body := range []string{`{"enabled":null}`, `{"interval":600}`, `null`, `{"checkIntervalSeconds":"soon"}`} {
		if resp, m := putReleaseSettings(t, api, admin, body, nil); resp.StatusCode != http.StatusBadRequest {
			t.Errorf("PUT %s: status %d, want 400: %v", body, resp.StatusCode, m)
		}
	}
	if len(checker.applied) != 1 {
		t.Fatalf("applied = %+v, want only the seed write to reach the running check", checker.applied)
	}
}

func TestPutReleaseSettingsIsRefusedWhileTheVariablesAreStillSet(t *testing.T) {
	api, admin, _, checker := releaseSettingsTestAPI(t, func(d *Dependencies) { d.ReleaseSettingsEnvVarsSet = true })
	resp, m := putReleaseSettings(t, api, admin, `{"enabled":false}`, nil)
	if resp.StatusCode != http.StatusConflict || !strings.Contains(m["detail"].(string), "Remove SHOWMESH_RELEASE_CHECK") {
		t.Fatalf("status %d, body %v, want 409 naming the variables to remove", resp.StatusCode, m)
	}

	api, admin, _, _ = releaseSettingsTestAPI(t, func(d *Dependencies) {
		d.ReleaseSettingsEnvVarsSet, d.ReleaseSettingsMigrationDeferred = true, true
	})
	resp, m = putReleaseSettings(t, api, admin, `{"enabled":false}`, nil)
	if resp.StatusCode != http.StatusConflict || !strings.Contains(m["detail"].(string), "keep the SHOWMESH_RELEASE_CHECK variables") {
		t.Fatalf("deferred: status %d, body %v, want 409 that keeps the variables", resp.StatusCode, m)
	}
	if len(checker.applied) != 0 {
		t.Fatalf("a refused write reached the running check: %+v", checker.applied)
	}
}

func TestGetReleaseSettingsRevisionsListsNewestFirst(t *testing.T) {
	api, admin, viewer, _ := releaseSettingsTestAPI(t, nil)
	const path = releaseSettingsPath + "/revisions"

	if resp, body := doRequest(t, api.Handler, http.MethodGet, path, map[string]string{"Authorization": "Bearer " + viewer}); resp.StatusCode != http.StatusForbidden {
		t.Fatalf("viewer GET status = %d, want 403: %s", resp.StatusCode, body)
	}
	resp, body := doRequest(t, api.Handler, http.MethodGet, path, map[string]string{"Authorization": "Bearer " + admin})
	if revs, ok := decodeMap(t, body)["revisions"].([]any); resp.StatusCode != http.StatusOK || !ok || len(revs) != 0 {
		t.Fatalf("nothing stored: status %d, body %s, want 200 with an empty list", resp.StatusCode, body)
	}

	for _, put := range []string{`{"enabled":false}`, `{"checkIntervalSeconds":900}`} {
		if resp, m := putReleaseSettings(t, api, admin, put, nil); resp.StatusCode != http.StatusOK {
			t.Fatalf("PUT %s: status %d, body %v", put, resp.StatusCode, m)
		}
	}
	resp, body = doRequest(t, api.Handler, http.MethodGet, path, map[string]string{"Authorization": "Bearer " + admin})
	m := decodeMap(t, body)
	revs, ok := m["revisions"].([]any)
	if resp.StatusCode != http.StatusOK || m["kind"] != "release.settings" || !ok || len(revs) != 2 {
		t.Fatalf("status %d, body %s, want 200 with two release.settings revisions", resp.StatusCode, body)
	}
	newest, oldest := revs[0].(map[string]any), revs[1].(map[string]any)
	if newest["revision"] != float64(2) || newest["active"] != true || oldest["revision"] != float64(1) || oldest["active"] != false {
		t.Fatalf("revisions = %v, want 2 (active) then 1", revs)
	}
}

func TestGetReleaseSettingsNamesTheEnvironmentWhileTheMigrationIsDeferred(t *testing.T) {
	api, admin, _, _ := releaseSettingsTestAPI(t, func(d *Dependencies) {
		d.ReleaseSettingsEnvVarsSet, d.ReleaseSettingsMigrationDeferred = true, true
		d.Releases = &releaseCheckerFake{status: versions.Status{Enabled: false, Interval: 10 * time.Minute}}
	})
	resp, body := doRequest(t, api.Handler, http.MethodGet, releaseSettingsPath, map[string]string{"Authorization": "Bearer " + admin})
	m := decodeMap(t, body)
	payload, _ := m["payload"].(map[string]any)
	if resp.StatusCode != http.StatusOK || m["revision"] != float64(0) || m["source"] != "env" || payload["enabled"] != false || payload["checkIntervalSeconds"] != float64(600) {
		t.Fatalf("status %d, body %s, want revision 0, source env, off, 600 seconds", resp.StatusCode, body)
	}
}

func TestPutReleaseSettingsRefusesAnIntervalOverOneWeek(t *testing.T) {
	api, admin, _, checker := releaseSettingsTestAPI(t, nil)
	const want = "The check interval is too long. Set it to 604800 seconds or less."
	for _, body := range []string{`{"checkIntervalSeconds":604801}`, `{"checkIntervalSeconds":9223372036854775807}`} {
		resp, m := putReleaseSettings(t, api, admin, body, nil)
		if resp.StatusCode != http.StatusBadRequest || m["detail"] != want {
			t.Errorf("PUT %s: status %d, body %v, want 400 %q", body, resp.StatusCode, m, want)
		}
	}
	if resp, m := putReleaseSettings(t, api, admin, `{"checkIntervalSeconds":604800}`, nil); resp.StatusCode != http.StatusOK {
		t.Fatalf("one week: status %d, body %v, want 200", resp.StatusCode, m)
	}
	if len(checker.applied) != 1 || checker.applied[0].Interval != 7*24*time.Hour {
		t.Fatalf("applied = %+v, want only the one-week write", checker.applied)
	}
}
