package main

import (
	"bytes"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// This file is the showmeshctl test suite for "fppconnect settings"
// (ADR-044 decision 5), mirroring cmd_audio_test.go's own shape one config
// kind over.

func writeFPPConnectSettingsPayloadFile(t *testing.T, payload string) string {
	t.Helper()
	dir := t.TempDir()
	file := filepath.Join(dir, "fppconnect-settings.json")
	if err := os.WriteFile(file, []byte(payload), 0o600); err != nil {
		t.Fatalf("write payload file: %v", err)
	}
	return file
}

func TestCmdFPPConnectSettingsGetPrintsDefault(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet || r.URL.Path != "/api/v1/config/fppconnect.settings" {
			t.Errorf("request = %s %s, want GET /api/v1/config/fppconnect.settings", r.Method, r.URL.Path)
		}
		w.Header().Set("Content-Type", "application/json")
		w.Header().Set("ShowMesh-API-Version", "1")
		_, _ = fmt.Fprint(w, `{"serverTime":"2026-08-25T00:00:00Z","kind":"fppconnect.settings","revision":0,
			"payload":{"enabled":true,"maxFileBytes":2147483648,"maxAssetDirBytes":21474836480},
			"updatedAt":"2026-08-25T00:00:00Z","createdByPrincipalId":null,"createdByPrincipalName":null,"source":"default"}`)
	}))
	defer ts.Close()

	var stdout, stderr bytes.Buffer
	code := cmdFPPConnect([]string{"settings", "get", "--server", ts.URL, "--token", "t"}, &stdout, &stderr, time.Now)
	if code != exitOK {
		t.Fatalf("exit code = %d, want exitOK; stderr=%s", code, stderr.String())
	}
	out := stdout.String()
	for _, want := range []string{"revision 0", "source default", "built-in default", "enabled:          true", "maxFileBytes:     2147483648", "maxAssetDirBytes: 21474836480"} {
		if !strings.Contains(out, want) {
			t.Errorf("output missing %q:\n%s", want, out)
		}
	}
}

// TestCmdFPPConnectSettingsSetPutsTheFileContents proves `fppconnect
// settings set --file` issues a real PUT carrying the file's contents
// unmodified, even when the file is INCOMPLETE (omits maxAssetDirBytes):
// the CLI must never reshape the payload through a typed struct, because
// doing so would silently turn the omitted field into an explicit 0 the
// server would accept, making the server's own field_required refusal
// unreachable through this emergency-path client.
func TestCmdFPPConnectSettingsSetPutsTheFileContents(t *testing.T) {
	var gotMethod, gotPath string
	var gotBody []byte
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotMethod, gotPath = r.Method, r.URL.Path
		gotBody, _ = io.ReadAll(r.Body)
		w.Header().Set("Content-Type", "application/json")
		w.Header().Set("ShowMesh-API-Version", "1")
		_, _ = fmt.Fprint(w, `{"serverTime":"2026-08-25T00:00:00Z","kind":"fppconnect.settings","revision":1,
			"payload":{"enabled":false,"maxFileBytes":1073741824,"maxAssetDirBytes":10737418240},
			"updatedAt":"2026-08-25T00:00:00Z","createdByPrincipalId":"p1","createdByPrincipalName":"admin","source":"api"}`)
	}))
	defer ts.Close()

	payload := `{"enabled":false,"maxFileBytes":1073741824}`
	file := writeFPPConnectSettingsPayloadFile(t, payload)

	var stdout, stderr bytes.Buffer
	code := cmdFPPConnect([]string{"settings", "set", "--file", file, "--server", ts.URL, "--token", "t"}, &stdout, &stderr, time.Now)
	if code != exitOK {
		t.Fatalf("exit code = %d, want exitOK; stderr=%s", code, stderr.String())
	}
	if gotMethod != http.MethodPut || gotPath != "/api/v1/config/fppconnect.settings" {
		t.Fatalf("request = %s %s, want PUT /api/v1/config/fppconnect.settings", gotMethod, gotPath)
	}
	if strings.TrimSpace(string(gotBody)) != payload {
		t.Fatalf("PUT body = %s, want the incomplete file's own bytes %s, unreshaped", gotBody, payload)
	}
}

// TestCmdFPPConnectSettingsSetRejectsNonObjectPayload proves the one check
// this command still performs client-side: the payload must be a JSON
// object, so a malformed file fails fast with a clear message instead of
// an opaque 400 from the server.
func TestCmdFPPConnectSettingsSetRejectsNonObjectPayload(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		t.Fatal("request reached the server; want a client-side rejection")
	}))
	defer ts.Close()

	file := writeFPPConnectSettingsPayloadFile(t, `[1,2,3]`)

	var stdout, stderr bytes.Buffer
	code := cmdFPPConnect([]string{"settings", "set", "--file", file, "--server", ts.URL, "--token", "t"}, &stdout, &stderr, time.Now)
	if code != exitUsage {
		t.Fatalf("exit code = %d, want exitUsage; stderr=%s", code, stderr.String())
	}
}

func TestCmdFPPConnectSettingsRevisionsListsRevisions(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet || r.URL.Path != "/api/v1/config/fppconnect.settings/revisions" {
			t.Errorf("request = %s %s, want GET /api/v1/config/fppconnect.settings/revisions", r.Method, r.URL.Path)
		}
		w.Header().Set("Content-Type", "application/json")
		w.Header().Set("ShowMesh-API-Version", "1")
		_, _ = fmt.Fprint(w, `{"serverTime":"2026-08-25T00:00:00Z","kind":"fppconnect.settings","revisions":[
			{"revision":1,"createdAt":"2026-08-25T00:00:00Z","createdByPrincipalId":"p1","createdByPrincipalName":"admin","source":"api","active":true}
		]}`)
	}))
	defer ts.Close()

	var stdout, stderr bytes.Buffer
	code := cmdFPPConnect([]string{"settings", "revisions", "--server", ts.URL, "--token", "t"}, &stdout, &stderr, time.Now)
	if code != exitOK {
		t.Fatalf("exit code = %d, want exitOK; stderr=%s", code, stderr.String())
	}
	if !strings.Contains(stdout.String(), "1") {
		t.Errorf("output missing revision 1:\n%s", stdout.String())
	}
}

func TestCmdFPPConnectUnknownSubcommand(t *testing.T) {
	var stdout, stderr bytes.Buffer
	code := cmdFPPConnect([]string{"bogus"}, &stdout, &stderr, time.Now)
	if code != exitUsage {
		t.Fatalf("exit code = %d, want exitUsage", code)
	}
}

func TestCmdFPPConnectSettingsUnknownSubcommand(t *testing.T) {
	var stdout, stderr bytes.Buffer
	code := cmdFPPConnectSettings([]string{"bogus"}, &stdout, &stderr, time.Now)
	if code != exitUsage {
		t.Fatalf("exit code = %d, want exitUsage", code)
	}
}
