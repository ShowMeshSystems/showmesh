package main

import (
	"bytes"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestCmdAuditPassesSinceAndLimitAsQueryParams(t *testing.T) {
	var gotQuery string
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotQuery = r.URL.RawQuery
		w.Header().Set("Content-Type", "application/json")
		w.Header().Set("ShowMesh-API-Version", "1")
		_, _ = fmt.Fprint(w, `{"serverTime":"2026-08-10T21:00:00Z","entries":[]}`)
	}))
	defer ts.Close()

	var stdout, stderr bytes.Buffer
	code := cmdAudit([]string{"--server", ts.URL, "--since", "42", "--limit", "5"}, &stdout, &stderr, time.Now)
	if code != exitOK {
		t.Fatalf("exit code = %d, want exitOK; stderr=%s", code, stderr.String())
	}
	if !strings.Contains(gotQuery, "since=42") || !strings.Contains(gotQuery, "limit=5") {
		t.Errorf("query = %q, want since=42 and limit=5", gotQuery)
	}
}

func TestCmdAuditRejectsInvalidSince(t *testing.T) {
	var stdout, stderr bytes.Buffer
	code := cmdAudit([]string{"--since", "not-a-number"}, &stdout, &stderr, time.Now)
	if code != exitUsage {
		t.Errorf("exit code = %d, want exitUsage (%d)", code, exitUsage)
	}
}

func TestCmdAuditRejectsInvalidLimit(t *testing.T) {
	var stdout, stderr bytes.Buffer
	code := cmdAudit([]string{"--limit", "not-a-number"}, &stdout, &stderr, time.Now)
	if code != exitUsage {
		t.Errorf("exit code = %d, want exitUsage (%d)", code, exitUsage)
	}
}

// TestCmdAuditForbiddenNamesMissingScope pins ADR-024 decision 4 through
// the audit command specifically: GET /api/v1/audit "always requires
// audit:read, regardless of whether reads are otherwise open" (openapi.yaml),
// so a viewer or operator token — which never holds it — must be reported
// as forbidden (exit 7), distinctly from unauthenticated (exit 3), with the
// missing scope named.
func TestCmdAuditForbiddenNamesMissingScope(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/problem+json")
		w.WriteHeader(http.StatusForbidden)
		_, _ = fmt.Fprint(w, `{"type":"https://showmesh.dev/problems/forbidden","title":"Forbidden","status":403,"detail":"this principal does not hold the required scope: audit:read"}`)
	}))
	defer ts.Close()

	var stdout, stderr bytes.Buffer
	code := cmdAudit([]string{"--server", ts.URL, "--token", "t"}, &stdout, &stderr, time.Now)
	if code != exitForbidden {
		t.Errorf("exit code = %d, want exitForbidden (%d); stderr=%s", code, exitForbidden, stderr.String())
	}
	if !strings.Contains(stderr.String(), "audit:read") {
		t.Errorf("stderr = %q, want it to name the missing scope", stderr.String())
	}
}

func TestCmdAuditUnauthorizedWithNoToken(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/problem+json")
		w.WriteHeader(http.StatusUnauthorized)
		_, _ = fmt.Fprint(w, `{"type":"https://showmesh.dev/problems/unauthorized","title":"Unauthorized","status":401,"detail":"missing or invalid bearer token"}`)
	}))
	defer ts.Close()

	var stdout, stderr bytes.Buffer
	code := cmdAudit([]string{"--server", ts.URL}, &stdout, &stderr, time.Now)
	if code != exitUnauthorized {
		t.Errorf("exit code = %d, want exitUnauthorized (%d)", code, exitUnauthorized)
	}
}

// TestCmdAuditRendersEntriesDistinctlyByKind pins that a replay or an
// auth_failure entry is visibly distinguishable from an ordinary
// dispatch/outcome row in the text table — ADR-024 decision 11's "the
// replay is precisely the case an investigator wants to see."
func TestCmdAuditRendersEntriesDistinctlyByKind(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.Header().Set("ShowMesh-API-Version", "1")
		_, _ = fmt.Fprint(w, `{"serverTime":"2026-08-10T21:00:00Z","entries":[
			{"timestamp":"2026-08-10T20:00:00Z","principalId":"p-1","principalName":"eric","form":"token",
			 "credentialId":"tok-1","clientAddr":"","action":"session.create","target":"sess-1","params":{},
			 "idempotencyKey":"","kind":"dispatch","commandId":"","outcome":"","outcomeState":"","outcomeReason":""},
			{"timestamp":"2026-08-10T20:01:00Z","principalId":"","principalName":"","form":"password",
			 "credentialId":"","clientAddr":"10.0.0.5","action":"session.create","target":"","params":{},
			 "idempotencyKey":"","kind":"auth_failure","commandId":"","outcome":"","outcomeState":"","outcomeReason":"invalid credentials"},
			{"timestamp":"2026-08-10T20:02:00Z","principalId":"p-2","principalName":"scheduler","form":"token",
			 "credentialId":"tok-2","clientAddr":"","action":"show.macro.run","target":"begin-set","params":{"key":"abc"},
			 "idempotencyKey":"abc","kind":"replay","commandId":"cmd-9","outcome":"replayed","outcomeState":"current","outcomeReason":""}
		]}`)
	}))
	defer ts.Close()

	var stdout, stderr bytes.Buffer
	code := cmdAudit([]string{"--server", ts.URL, "--token", "t"}, &stdout, &stderr, time.Now)
	if code != exitOK {
		t.Fatalf("exit code = %d, want exitOK; stderr=%s", code, stderr.String())
	}
	out := stdout.String()
	if !strings.Contains(out, "REPLAY") {
		t.Errorf("stdout = %q, want the replay entry rendered loudly as REPLAY", out)
	}
	if !strings.Contains(out, "AUTH-FAILURE") {
		t.Errorf("stdout = %q, want the auth_failure entry rendered loudly as AUTH-FAILURE", out)
	}
	if !strings.Contains(out, "dispatch") {
		t.Errorf("stdout = %q, want the ordinary dispatch entry rendered", out)
	}
}

func TestCmdAuditNoEntries(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.Header().Set("ShowMesh-API-Version", "1")
		_, _ = fmt.Fprint(w, `{"serverTime":"2026-08-10T21:00:00Z","entries":[]}`)
	}))
	defer ts.Close()

	var stdout, stderr bytes.Buffer
	code := cmdAudit([]string{"--server", ts.URL, "--token", "t"}, &stdout, &stderr, time.Now)
	if code != exitOK {
		t.Fatalf("exit code = %d, want exitOK; stderr=%s", code, stderr.String())
	}
	if !strings.Contains(stdout.String(), "no audit entries") {
		t.Errorf("stdout = %q, want an explicit empty message", stdout.String())
	}
	if strings.Contains(stderr.String(), "this page returned") {
		t.Errorf("stderr = %q, must not warn about a possibly-incomplete page when there were zero entries", stderr.String())
	}
}

// TestCmdAuditWarnsWhenPageMayBeIncomplete is the load-bearing test for
// this command's honesty requirement: when a page comes back exactly as
// full as what was requested, this CLI cannot know whether more entries
// exist (an auditEntry carries no row id — see cmd_audit.go's doc
// comment), and it must say so rather than silently presenting a partial
// log as complete.
func TestCmdAuditWarnsWhenPageMayBeIncomplete(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.Header().Set("ShowMesh-API-Version", "1")
		_, _ = fmt.Fprint(w, `{"serverTime":"2026-08-10T21:00:00Z","entries":[
			{"timestamp":"2026-08-10T20:00:00Z","principalId":"p-1","principalName":"eric","form":"token",
			 "credentialId":"tok-1","clientAddr":"","action":"session.create","target":"sess-1","params":{},
			 "idempotencyKey":"","kind":"dispatch","commandId":"","outcome":"","outcomeState":"","outcomeReason":""},
			{"timestamp":"2026-08-10T20:01:00Z","principalId":"p-1","principalName":"eric","form":"token",
			 "credentialId":"tok-1","clientAddr":"","action":"session.create","target":"sess-2","params":{},
			 "idempotencyKey":"","kind":"dispatch","commandId":"","outcome":"","outcomeState":"","outcomeReason":""}
		]}`)
	}))
	defer ts.Close()

	var stdout, stderr bytes.Buffer
	code := cmdAudit([]string{"--server", ts.URL, "--token", "t", "--limit", "2"}, &stdout, &stderr, time.Now)
	if code != exitOK {
		t.Fatalf("exit code = %d, want exitOK; stderr=%s", code, stderr.String())
	}
	if !strings.Contains(stderr.String(), "row id") {
		t.Errorf("stderr = %q, want an honest note that this CLI cannot compute the next page's cursor", stderr.String())
	}
}

// TestCmdAuditNoWarningWhenPageIsShortOfLimit is
// TestCmdAuditWarnsWhenPageMayBeIncomplete's inverse: break the behavior
// the name claims (a page strictly shorter than the requested limit) and
// confirm the warning does NOT fire, since a short page is exactly the
// signal that there is nothing more to fetch.
func TestCmdAuditNoWarningWhenPageIsShortOfLimit(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.Header().Set("ShowMesh-API-Version", "1")
		_, _ = fmt.Fprint(w, `{"serverTime":"2026-08-10T21:00:00Z","entries":[
			{"timestamp":"2026-08-10T20:00:00Z","principalId":"p-1","principalName":"eric","form":"token",
			 "credentialId":"tok-1","clientAddr":"","action":"session.create","target":"sess-1","params":{},
			 "idempotencyKey":"","kind":"dispatch","commandId":"","outcome":"","outcomeState":"","outcomeReason":""}
		]}`)
	}))
	defer ts.Close()

	var stdout, stderr bytes.Buffer
	code := cmdAudit([]string{"--server", ts.URL, "--token", "t", "--limit", "50"}, &stdout, &stderr, time.Now)
	if code != exitOK {
		t.Fatalf("exit code = %d, want exitOK; stderr=%s", code, stderr.String())
	}
	if strings.Contains(stderr.String(), "row id") {
		t.Errorf("stderr = %q, must not warn about incompleteness when the page came back short of --limit", stderr.String())
	}
}

func TestEffectiveAuditLimit(t *testing.T) {
	cases := []struct {
		requested int
		want      int
	}{
		{0, 100},
		{-1, 100},
		{5, 5},
		{500, 500},
		{9999, 500},
	}
	for _, tc := range cases {
		if got := effectiveAuditLimit(tc.requested); got != tc.want {
			t.Errorf("effectiveAuditLimit(%d) = %d, want %d", tc.requested, got, tc.want)
		}
	}
}
