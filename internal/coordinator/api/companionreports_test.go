package api

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"strconv"
	"strings"
	"testing"

	"github.com/showmeshsystems/showmesh/internal/coordinator/identity"
)

type companionReportHarness struct {
	api           *API
	companionAuth map[string]string
	operatorAuth  map[string]string
	adminAuth     map[string]string
}

func newCompanionReportHarness(t *testing.T) companionReportHarness {
	t.Helper()
	svc, st, _ := newTestIdentityServiceWithStore(t, fixedClock(testNow))
	deps := assetsTestDeps(t, svc, st)
	deps.CompanionReports = st
	api := New(deps, Options{Clock: fixedClock(testNow), Logger: testLogger()})
	bearer := func(name string, role identity.Role) map[string]string {
		p := mustCreatePrincipal(t, svc, name, role)
		return map[string]string{"Authorization": "Bearer " + mustIssueToken(t, svc, p.ID)}
	}
	return companionReportHarness{
		api:           api,
		companionAuth: bearer("arena-mac-companion", identity.RoleCompanion),
		operatorAuth:  bearer("operator-1", identity.RoleOperator),
		adminAuth:     bearer("admin-1", identity.RoleAdmin),
	}
}

func companionReportBody(content, sha string) string {
	if sha == "" {
		sum := sha256.Sum256([]byte(content))
		sha = hex.EncodeToString(sum[:])
	}
	return `{"companionId":"arena-mac","kind":"crash_report","source":"resolume-arena",` +
		`"fileName":"Arena-2026-10-14-205842.ips","capturedAt":"2026-10-14T20:58:42Z",` +
		`"contentSha256":"` + sha + `","content":` + strconv.Quote(content) + `}`
}

func (h companionReportHarness) post(t *testing.T, body string, auth map[string]string) (int, string) {
	t.Helper()
	resp, out := doRawRequest(t, h.api.Handler, newJSONRequest(t, http.MethodPost, "/api/v1/companion-reports", body, auth))
	return resp.StatusCode, string(out)
}

func TestCompanionReportUploadStoresOnceAndReadsBack(t *testing.T) {
	h := newCompanionReportHarness(t)

	status, body := h.post(t, companionReportBody(`{"crash":1}`, ""), h.companionAuth)
	if status != http.StatusCreated {
		t.Fatalf("first upload status = %d, want 201; body: %s", status, body)
	}
	var first struct {
		Created bool `json:"created"`
		Report  struct {
			ID        string `json:"id"`
			SizeBytes int64  `json:"sizeBytes"`
		} `json:"report"`
	}
	if err := json.Unmarshal([]byte(body), &first); err != nil || !first.Created || first.Report.ID == "" || first.Report.SizeBytes != 11 {
		t.Fatalf("first upload body = %s err %v", body, err)
	}

	status, body = h.post(t, companionReportBody(`{"crash":1}`, ""), h.companionAuth)
	if status != http.StatusOK || !strings.Contains(body, `"created":false`) || !strings.Contains(body, first.Report.ID) {
		t.Fatalf("repeat upload status = %d body %s, want 200 naming the stored report", status, body)
	}

	resp, list := doRawRequest(t, h.api.Handler, newJSONRequest(t, http.MethodGet, "/api/v1/companion-reports?companionId=arena-mac", "", h.adminAuth))
	if resp.StatusCode != http.StatusOK || strings.Count(string(list), `"id":`) != 1 || strings.Contains(string(list), `"content"`) {
		t.Fatalf("list status = %d body %s, want one summary without content", resp.StatusCode, list)
	}

	resp, got := doRawRequest(t, h.api.Handler, newJSONRequest(t, http.MethodGet, "/api/v1/companion-reports/"+first.Report.ID, "", h.adminAuth))
	if resp.StatusCode != http.StatusOK || !strings.Contains(string(got), `"content":"{\"crash\":1}"`) {
		t.Fatalf("get status = %d body %s, want the stored content", resp.StatusCode, got)
	}

	resp, _ = doRawRequest(t, h.api.Handler, newJSONRequest(t, http.MethodGet, "/api/v1/companion-reports/missing", "", h.adminAuth))
	if resp.StatusCode != http.StatusNotFound {
		t.Fatalf("get missing status = %d, want 404", resp.StatusCode)
	}
}

func TestCompanionReportUploadRequiresCompanionScope(t *testing.T) {
	h := newCompanionReportHarness(t)
	if status, body := h.post(t, companionReportBody("x", ""), h.operatorAuth); status != http.StatusForbidden {
		t.Fatalf("operator upload status = %d, want 403; body: %s", status, body)
	}
	if status, body := h.post(t, companionReportBody("x", ""), nil); status != http.StatusUnauthorized {
		t.Fatalf("anonymous upload status = %d, want 401; body: %s", status, body)
	}
}

func TestCompanionReportUploadRefusesBadInput(t *testing.T) {
	h := newCompanionReportHarness(t)
	cases := map[string]struct {
		body string
		want int
	}{
		"hash mismatch":   {companionReportBody("x", strings.Repeat("0", 64)), http.StatusBadRequest},
		"unknown kind":    {strings.Replace(companionReportBody("x", ""), "crash_report", "log", 1), http.StatusBadRequest},
		"folder in name":  {strings.Replace(companionReportBody("x", ""), "Arena-2026", "../Arena-2026", 1), http.StatusBadRequest},
		"bad companionId": {strings.Replace(companionReportBody("x", ""), `"arena-mac"`, `"arena mac"`, 1), http.StatusBadRequest},
		"too large":       {companionReportBody(strings.Repeat("a", maxCompanionReportContentBytes+1), ""), http.StatusRequestEntityTooLarge},
	}
	for name, tc := range cases {
		if status, body := h.post(t, tc.body, h.companionAuth); status != tc.want {
			t.Errorf("%s: status = %d, want %d; body: %.300s", name, status, tc.want, body)
		}
	}
}
