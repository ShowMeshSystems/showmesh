package main

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

type releaseSettingsRequest struct {
	method, ifMatch, body string
}

// releaseSettingsServer answers GET with the stored revision and PUT with the next one.
func releaseSettingsServer(t *testing.T, putStatus int) (*httptest.Server, *[]releaseSettingsRequest) {
	t.Helper()
	var seen []releaseSettingsRequest
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/v1/config/release.settings" {
			t.Errorf("unexpected request %s %s", r.Method, r.URL.Path)
		}
		body, _ := io.ReadAll(r.Body)
		seen = append(seen, releaseSettingsRequest{r.Method, r.Header.Get("If-Match"), string(body)})
		w.Header().Set("ShowMesh-API-Version", "1")
		switch {
		case r.Method == http.MethodGet:
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"serverTime":"2026-10-12T18:05:00Z","kind":"release.settings","revision":2,
				"payload":{"enabled":true,"checkIntervalSeconds":21600},"updatedAt":"2026-10-12T17:00:00Z",
				"createdByPrincipalId":"p1","createdByPrincipalName":"ada","source":"api"}`))
		case putStatus != http.StatusOK:
			w.Header().Set("Content-Type", "application/problem+json")
			w.WriteHeader(putStatus)
			_, _ = w.Write([]byte(`{"type":"https://showmesh.dev/problems/conflict","title":"conflict","status":409,
				"detail":"The release check is still set in the coordinator's environment. Remove SHOWMESH_RELEASE_CHECK and SHOWMESH_RELEASE_CHECK_INTERVAL, then restart the coordinator.",
				"serverTime":"2026-10-12T18:05:00Z"}`))
		default:
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"serverTime":"2026-10-12T18:05:00Z","kind":"release.settings","revision":3,
				"payload":{"enabled":false,"checkIntervalSeconds":900},"updatedAt":"2026-10-12T18:05:00Z",
				"createdByPrincipalId":"p1","createdByPrincipalName":"ada","source":"api"}`))
		}
	}))
	t.Cleanup(ts.Close)
	return ts, &seen
}

func runRelease(t *testing.T, ts *httptest.Server, args ...string) (int, string, string) {
	t.Helper()
	var stdout, stderr bytes.Buffer
	full := append([]string{"release", "settings"}, args...)
	full = append(full, "--server", ts.URL)
	code := run(full, &stdout, &stderr, fixedClock(mustParse(t, "2026-10-12T18:05:00Z")))
	return code, stdout.String(), stderr.String()
}

func TestCmdReleaseSettingsGetPrintsTheSetting(t *testing.T) {
	ts, _ := releaseSettingsServer(t, http.StatusOK)
	code, out, errOut := runRelease(t, ts, "get")
	if code != exitOK {
		t.Fatalf("exit code = %d, want exitOK; stderr=%s", code, errOut)
	}
	want := [][]string{{"check:", "on"}, {"interval:", "6h0m0s"}, {"revision:", "2"}, {"source:", "api"}, {"changedBy:", "ada"}}
	lines := strings.Split(strings.TrimRight(out, "\n"), "\n")
	if len(lines) != len(want) {
		t.Fatalf("output = %q, want %d lines", out, len(want))
	}
	for i, line := range lines {
		if got := strings.Fields(line); len(got) != 2 || got[0] != want[i][0] || got[1] != want[i][1] {
			t.Errorf("line %d = %q, want %v", i, line, want[i])
		}
	}
}

func TestCmdReleaseSettingsSetSendsOnlyTheFlagsPassedAgainstTheReadRevision(t *testing.T) {
	ts, seen := releaseSettingsServer(t, http.StatusOK)
	code, out, errOut := runRelease(t, ts, "set", "--enabled=false", "--interval", "15m")
	if code != exitOK {
		t.Fatalf("exit code = %d, want exitOK; stderr=%s", code, errOut)
	}
	if len(*seen) != 2 || (*seen)[0].method != http.MethodGet || (*seen)[1].method != http.MethodPut {
		t.Fatalf("requests = %+v, want a GET then a PUT", *seen)
	}
	put := (*seen)[1]
	var body map[string]any
	if err := json.Unmarshal([]byte(put.body), &body); err != nil {
		t.Fatalf("decode PUT body %q: %v", put.body, err)
	}
	if put.ifMatch != `"2"` || len(body) != 2 || body["enabled"] != false || body["checkIntervalSeconds"] != float64(900) {
		t.Fatalf("PUT If-Match = %q, body = %v, want \"2\", off and 900 seconds", put.ifMatch, body)
	}
	if !strings.Contains(out, "off") || !strings.Contains(out, "15m0s") {
		t.Fatalf("output = %q, want the new setting", out)
	}

	*seen = nil
	if code, _, errOut := runRelease(t, ts, "set", "--interval", "1h"); code != exitOK {
		t.Fatalf("interval only: exit code = %d; stderr=%s", code, errOut)
	}
	if got := (*seen)[1].body; strings.Contains(got, "enabled") {
		t.Fatalf("PUT body = %s, want the flag that was not passed left out", got)
	}
}

func TestCmdReleaseSettingsSetShowsARefusalAndNeedsAFlag(t *testing.T) {
	ts, seen := releaseSettingsServer(t, http.StatusConflict)
	code, _, errOut := runRelease(t, ts, "set", "--enabled=false")
	if code == exitOK || !strings.Contains(errOut, "Remove SHOWMESH_RELEASE_CHECK") {
		t.Fatalf("exit code = %d, stderr = %q, want the coordinator's refusal", code, errOut)
	}

	*seen = nil
	if code, _, errOut := runRelease(t, ts, "set"); code != exitUsage || !strings.Contains(errOut, "pass at least one of --enabled, --interval") {
		t.Fatalf("no flags: exit code = %d, stderr = %q, want a usage error", code, errOut)
	}
	if len(*seen) != 0 {
		t.Fatalf("a set with no flags reached the coordinator: %+v", *seen)
	}
}

func TestCmdReleaseSettingsRevisionsPrintsTheHistoryNewestFirst(t *testing.T) {
	var gotPath string
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		w.Header().Set("Content-Type", "application/json")
		w.Header().Set("ShowMesh-API-Version", "1")
		_, _ = w.Write([]byte(`{"serverTime":"2026-10-12T18:05:00Z","kind":"release.settings","revisions":[
			{"revision":2,"createdByPrincipalId":"p1","createdByPrincipalName":"ada","source":"api","createdAt":"2026-10-12T18:00:00Z","active":true},
			{"revision":1,"createdByPrincipalId":null,"createdByPrincipalName":null,"source":"env_migration","createdAt":"2026-10-12T17:00:00Z","active":false}
		]}`))
	}))
	t.Cleanup(ts.Close)

	code, out, errOut := runRelease(t, ts, "revisions")
	if code != exitOK {
		t.Fatalf("exit code = %d, want exitOK; stderr=%s", code, errOut)
	}
	if gotPath != "/api/v1/config/release.settings/revisions" {
		t.Errorf("path = %q, want /api/v1/config/release.settings/revisions", gotPath)
	}
	lines := strings.Split(strings.TrimRight(out, "\n"), "\n")
	if len(lines) != 3 {
		t.Fatalf("output = %q, want a header and two rows", out)
	}
	if got := strings.Fields(lines[1]); len(got) < 5 || got[0] != "2" || got[1] != "*" || got[3] != "ada" || got[4] != "api" {
		t.Errorf("newest row = %q, want revision 2, active, by ada, from api", lines[1])
	}
	if got := strings.Fields(lines[2]); len(got) < 4 || got[0] != "1" || got[3] != "env_migration" {
		t.Errorf("oldest row = %q, want revision 1 from env_migration", lines[2])
	}

	if code, _, _ := runRelease(t, ts, "revisions", "extra"); code != exitUsage {
		t.Errorf("extra argument: exit code = %d, want exitUsage", code)
	}
}
