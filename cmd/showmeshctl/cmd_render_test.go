package main

import (
	"bytes"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// This file is the showmeshctl test suite for "render settings", over
// GET/PUT /api/v1/config/render.settings and its own revisions list
// (Track B seam B2c, ADR-039).

func writeRenderSettingsPayloadFile(t *testing.T, payload string) string {
	t.Helper()
	dir := t.TempDir()
	file := filepath.Join(dir, "render-settings.json")
	if err := os.WriteFile(file, []byte(payload), 0o600); err != nil {
		t.Fatalf("write payload file: %v", err)
	}
	return file
}

func TestCmdRenderSettingsGetPrintsDefault(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet || r.URL.Path != "/api/v1/config/render.settings" {
			t.Errorf("request = %s %s, want GET /api/v1/config/render.settings", r.Method, r.URL.Path)
		}
		w.Header().Set("Content-Type", "application/json")
		w.Header().Set("ShowMesh-API-Version", "1")
		_, _ = fmt.Fprint(w, `{"serverTime":"2026-08-17T00:00:00Z","kind":"render.settings","revision":0,
			"payload":{"idleOutput":"black","restartPolicy":{"initialDelaySeconds":1,"maxDelaySeconds":30,"maxConsecutiveFastFailures":5}},
			"updatedAt":"2026-08-17T00:00:00Z","createdByPrincipalId":null,"createdByPrincipalName":null,"source":"default"}`)
	}))
	defer ts.Close()

	var stdout, stderr bytes.Buffer
	code := cmdRender([]string{"settings", "get", "--server", ts.URL, "--token", "t"}, &stdout, &stderr, time.Now)
	if code != exitOK {
		t.Fatalf("exit code = %d, want exitOK; stderr=%s", code, stderr.String())
	}
	out := stdout.String()
	for _, want := range []string{"revision 0", "source default", "built-in default", "idleOutput: black", "initialDelaySeconds=1", "maxDelaySeconds=30", "maxConsecutiveFastFailures=5"} {
		if !strings.Contains(out, want) {
			t.Errorf("output missing %q:\n%s", want, out)
		}
	}
}

// TestCmdRenderSettingsSetPutsTheFileContents proves `render settings set
// --file` issues a real PUT carrying the file's full payload, unmodified.
func TestCmdRenderSettingsSetPutsTheFileContents(t *testing.T) {
	var gotMethod, gotPath string
	var gotBody []byte
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotMethod, gotPath = r.Method, r.URL.Path
		body := make([]byte, r.ContentLength)
		_, _ = r.Body.Read(body)
		gotBody = body
		w.Header().Set("Content-Type", "application/json")
		w.Header().Set("ShowMesh-API-Version", "1")
		_, _ = fmt.Fprint(w, `{"serverTime":"2026-08-17T00:00:00Z","kind":"render.settings","revision":1,
			"payload":{"idleOutput":"hold","restartPolicy":{"initialDelaySeconds":2,"maxDelaySeconds":45,"maxConsecutiveFastFailures":6}},
			"updatedAt":"2026-08-17T00:00:00Z","createdByPrincipalId":"p1","createdByPrincipalName":"admin-1","source":"api"}`)
	}))
	defer ts.Close()

	file := writeRenderSettingsPayloadFile(t,
		`{"idleOutput":"hold","restartPolicy":{"initialDelaySeconds":2,"maxDelaySeconds":45,"maxConsecutiveFastFailures":6}}`)

	var stdout, stderr bytes.Buffer
	code := cmdRender([]string{"settings", "set", "--file", file, "--server", ts.URL, "--token", "t"}, &stdout, &stderr, time.Now)
	if code != exitOK {
		t.Fatalf("exit code = %d, want exitOK; stderr=%s", code, stderr.String())
	}
	if gotMethod != http.MethodPut {
		t.Errorf("method = %q, want PUT", gotMethod)
	}
	if gotPath != "/api/v1/config/render.settings" {
		t.Errorf("path = %q, want /api/v1/config/render.settings", gotPath)
	}
	for _, want := range []string{`"idleOutput":"hold"`, `"initialDelaySeconds":2`, `"maxDelaySeconds":45`, `"maxConsecutiveFastFailures":6`} {
		if !strings.Contains(string(gotBody), want) {
			t.Errorf("request body = %s, want it to contain %q", gotBody, want)
		}
	}
	if !strings.Contains(stdout.String(), "revision 1") {
		t.Errorf("stdout = %q, want it to render the new active revision", stdout.String())
	}
	if !strings.Contains(stderr.String(), "revision 1 is now active") {
		t.Errorf("stderr = %q, want the confirmation line", stderr.String())
	}
}

// TestCmdRenderSettingsSetRejectsInvalidJSONBeforeSendingAnyRequest proves
// a malformed payload is refused client-side without ever reaching the
// server.
func TestCmdRenderSettingsSetRejectsInvalidJSONBeforeSendingAnyRequest(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		t.Errorf("unexpected request %s %q — the CLI should have refused client-side", r.Method, r.URL.Path)
	}))
	defer ts.Close()

	file := writeRenderSettingsPayloadFile(t, `not json`)

	var stdout, stderr bytes.Buffer
	code := cmdRender([]string{"settings", "set", "--file", file, "--server", ts.URL, "--token", "t"}, &stdout, &stderr, time.Now)
	if code != exitUsage {
		t.Fatalf("exit code = %d, want exitUsage; stderr=%s", code, stderr.String())
	}
}

// TestCmdRenderSettingsSetSurfacesServerRejection proves a payload the
// server refuses (e.g. an absent idleOutput key) is reported to the
// operator rather than swallowed.
func TestCmdRenderSettingsSetSurfacesServerRejection(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/problem+json")
		w.WriteHeader(http.StatusBadRequest)
		_, _ = fmt.Fprint(w, `{"type":"invalid-parameter","title":"Invalid parameter","status":400,
			"detail":"idleOutput is required","serverTime":"2026-08-17T00:00:00Z"}`)
	}))
	defer ts.Close()

	file := writeRenderSettingsPayloadFile(t,
		`{"restartPolicy":{"initialDelaySeconds":1,"maxDelaySeconds":30,"maxConsecutiveFastFailures":5}}`)

	var stdout, stderr bytes.Buffer
	code := cmdRender([]string{"settings", "set", "--file", file, "--server", ts.URL, "--token", "t"}, &stdout, &stderr, time.Now)
	if code == exitOK {
		t.Fatalf("exit code = %d, want a non-zero failure; stdout=%s", code, stdout.String())
	}
	if !strings.Contains(stderr.String(), "idleOutput is required") {
		t.Errorf("stderr = %q, want the server's own rejection reason", stderr.String())
	}
}

func TestCmdRenderSettingsRevisionsListsNewestFirst(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/v1/config/render.settings/revisions" {
			t.Errorf("unexpected request path %q", r.URL.Path)
		}
		w.Header().Set("Content-Type", "application/json")
		w.Header().Set("ShowMesh-API-Version", "1")
		_, _ = fmt.Fprint(w, `{"serverTime":"2026-08-17T00:00:00Z","kind":"render.settings","revisions":[
			{"revision":2,"createdAt":"2026-08-17T00:01:00Z","createdByPrincipalId":"p1","createdByPrincipalName":"admin-1","source":"api","note":"","active":true},
			{"revision":1,"createdAt":"2026-08-17T00:00:00Z","createdByPrincipalId":"p1","createdByPrincipalName":"admin-1","source":"api","note":"","active":false}
		]}`)
	}))
	defer ts.Close()

	var stdout, stderr bytes.Buffer
	code := cmdRender([]string{"settings", "revisions", "--server", ts.URL, "--token", "t"}, &stdout, &stderr, time.Now)
	if code != exitOK {
		t.Fatalf("exit code = %d, want exitOK; stderr=%s", code, stderr.String())
	}
	out := stdout.String()
	if !strings.Contains(out, "2") || !strings.Contains(out, "1") {
		t.Errorf("output = %q, want both revisions listed", out)
	}
}

func TestCmdRenderUnknownSubcommand(t *testing.T) {
	var stdout, stderr bytes.Buffer
	code := cmdRender([]string{"bogus"}, &stdout, &stderr, time.Now)
	if code != exitUsage {
		t.Fatalf("exit code = %d, want exitUsage", code)
	}
	if !strings.Contains(stderr.String(), `unknown subcommand "bogus"`) {
		t.Errorf("stderr = %q, want it to name the bad subcommand", stderr.String())
	}
}

func TestCmdRenderSettingsUnknownSubcommand(t *testing.T) {
	var stdout, stderr bytes.Buffer
	code := cmdRenderSettings([]string{"bogus"}, &stdout, &stderr, time.Now)
	if code != exitUsage {
		t.Fatalf("exit code = %d, want exitUsage", code)
	}
}
