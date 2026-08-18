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

// This file is the showmeshctl test suite for "audio settings" and "audio
// node" (ADR-039), mirroring cmd_render_test.go's own shape.

func writeAudioSettingsPayloadFile(t *testing.T, payload string) string {
	t.Helper()
	dir := t.TempDir()
	file := filepath.Join(dir, "audio-settings.json")
	if err := os.WriteFile(file, []byte(payload), 0o600); err != nil {
		t.Fatalf("write payload file: %v", err)
	}
	return file
}

func TestCmdAudioSettingsGetPrintsDefault(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet || r.URL.Path != "/api/v1/config/audio.settings" {
			t.Errorf("request = %s %s, want GET /api/v1/config/audio.settings", r.Method, r.URL.Path)
		}
		w.Header().Set("Content-Type", "application/json")
		w.Header().Set("ShowMesh-API-Version", "1")
		_, _ = fmt.Fprint(w, `{"serverTime":"2026-08-17T00:00:00Z","kind":"audio.settings","revision":0,
			"payload":{"driftIgnoreThresholdMs":20,"defaultFadeCurve":"linear","defaultFadeDurationMs":1000,"defaultMaxBackgroundGain":0.6},
			"updatedAt":"2026-08-17T00:00:00Z","createdByPrincipalId":null,"createdByPrincipalName":null,"source":"default"}`)
	}))
	defer ts.Close()

	var stdout, stderr bytes.Buffer
	code := cmdAudio([]string{"settings", "get", "--server", ts.URL, "--token", "t"}, &stdout, &stderr, time.Now)
	if code != exitOK {
		t.Fatalf("exit code = %d, want exitOK; stderr=%s", code, stderr.String())
	}
	out := stdout.String()
	for _, want := range []string{"revision 0", "source default", "built-in default", "driftIgnoreThresholdMs:   20", "defaultFadeCurve:         linear"} {
		if !strings.Contains(out, want) {
			t.Errorf("output missing %q:\n%s", want, out)
		}
	}
}

// TestCmdAudioSettingsSetPutsTheFileContents proves `audio settings set
// --file` issues a real PUT carrying the file's contents unmodified, even
// when the file is INCOMPLETE (omits defaultMaxBackgroundGain): the CLI
// must never reshape the payload through a typed struct, because doing so
// would silently turn the omitted field into an explicit 0.0 the server
// would accept, making the server's own field_required refusal
// unreachable through this emergency-path client.
func TestCmdAudioSettingsSetPutsTheFileContents(t *testing.T) {
	var gotMethod, gotPath string
	var gotBody []byte
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotMethod, gotPath = r.Method, r.URL.Path
		gotBody, _ = io.ReadAll(r.Body)
		w.Header().Set("Content-Type", "application/json")
		w.Header().Set("ShowMesh-API-Version", "1")
		_, _ = fmt.Fprint(w, `{"serverTime":"2026-08-17T00:00:00Z","kind":"audio.settings","revision":1,
			"payload":{"driftIgnoreThresholdMs":30,"defaultFadeCurve":"linear","defaultFadeDurationMs":2000,"defaultMaxBackgroundGain":0.4},
			"updatedAt":"2026-08-17T00:00:00Z","createdByPrincipalId":"p1","createdByPrincipalName":"admin","source":"api"}`)
	}))
	defer ts.Close()

	payload := `{"driftIgnoreThresholdMs":30,"defaultFadeCurve":"linear","defaultFadeDurationMs":2000}`
	file := writeAudioSettingsPayloadFile(t, payload)

	var stdout, stderr bytes.Buffer
	code := cmdAudio([]string{"settings", "set", "--file", file, "--server", ts.URL, "--token", "t"}, &stdout, &stderr, time.Now)
	if code != exitOK {
		t.Fatalf("exit code = %d, want exitOK; stderr=%s", code, stderr.String())
	}
	if gotMethod != http.MethodPut || gotPath != "/api/v1/config/audio.settings" {
		t.Fatalf("request = %s %s, want PUT /api/v1/config/audio.settings", gotMethod, gotPath)
	}
	if strings.TrimSpace(string(gotBody)) != payload {
		t.Fatalf("PUT body = %s, want the incomplete file's own bytes %s, unreshaped", gotBody, payload)
	}
}

// TestCmdAudioSettingsSetRejectsNonObjectPayload proves the one check this
// command still performs client-side: the payload must be a JSON object,
// so a malformed file fails fast with a clear message instead of an
// opaque 400 from the server.
func TestCmdAudioSettingsSetRejectsNonObjectPayload(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		t.Fatal("request reached the server; want a client-side rejection")
	}))
	defer ts.Close()

	file := writeAudioSettingsPayloadFile(t, `[1,2,3]`)

	var stdout, stderr bytes.Buffer
	code := cmdAudio([]string{"settings", "set", "--file", file, "--server", ts.URL, "--token", "t"}, &stdout, &stderr, time.Now)
	if code != exitUsage {
		t.Fatalf("exit code = %d, want exitUsage; stderr=%s", code, stderr.String())
	}
}

func TestCmdAudioNodeSetSendsAllFlags(t *testing.T) {
	var gotMethod, gotPath string
	var gotBody []byte
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotMethod, gotPath = r.Method, r.URL.Path
		gotBody, _ = io.ReadAll(r.Body)
		w.Header().Set("Content-Type", "application/json")
		w.Header().Set("ShowMesh-API-Version", "1")
		_, _ = fmt.Fprint(w, `{"serverTime":"2026-08-17T00:00:00Z","kind":"audio.node","id":"render-01","revision":1,
			"payload":{"programRoute":"hw:0,0","ltcRoute":"hw:0,0","clockDomain":"single-interface","clockDomainProvenance":"one interface"},
			"updatedAt":"2026-08-17T00:00:00Z","createdByPrincipalId":"p1","createdByPrincipalName":"admin","source":"api"}`)
	}))
	defer ts.Close()

	var stdout, stderr bytes.Buffer
	code := cmdAudio([]string{
		"node", "set",
		"--program-route", "hw:0,0", "--ltc-route", "hw:0,0",
		"--clock-domain", "single-interface", "--clock-domain-provenance", "one interface",
		"--server", ts.URL, "--token", "t",
		"render-01",
	}, &stdout, &stderr, time.Now)
	if code != exitOK {
		t.Fatalf("exit code = %d, want exitOK; stderr=%s", code, stderr.String())
	}
	if gotMethod != http.MethodPut || gotPath != "/api/v1/config/audio.node/render-01" {
		t.Fatalf("request = %s %s, want PUT /api/v1/config/audio.node/render-01", gotMethod, gotPath)
	}
	for _, want := range []string{`"programRoute":"hw:0,0"`, `"ltcRoute":"hw:0,0"`, `"clockDomain":"single-interface"`, `"clockDomainProvenance":"one interface"`} {
		if !strings.Contains(string(gotBody), want) {
			t.Errorf("PUT body missing %q; body: %s", want, gotBody)
		}
	}
}

// TestCmdAudioNodeSetRequiresAllFlags proves the CLI refuses locally,
// before making any request, when a required flag is missing — a full
// replacement means every flag matters, not just the ones the operator
// remembered.
func TestCmdAudioNodeSetRequiresAllFlags(t *testing.T) {
	var requestSeen bool
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requestSeen = true
	}))
	defer ts.Close()

	var stdout, stderr bytes.Buffer
	code := cmdAudio([]string{
		"node", "set",
		"--program-route", "hw:0,0",
		"--server", ts.URL, "--token", "t",
		"render-01",
	}, &stdout, &stderr, time.Now)
	if code != exitUsage {
		t.Fatalf("exit code = %d, want exitUsage", code)
	}
	if requestSeen {
		t.Fatal("no request should have been made with a missing required flag")
	}
}

// TestCmdAudioNodeGetSurfacesPlacementRefusalMessage proves a coordinator
// refusal (400, this seam's placement-evidence rejection) is surfaced to
// the operator rather than swallowed, exercising the CLI's general error
// path against this specific new surface.
func TestCmdAudioNodeSetSurfacesRefusalMessage(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/problem+json")
		w.WriteHeader(http.StatusBadRequest)
		_, _ = fmt.Fprint(w, `{"type":"about:blank","title":"Invalid parameter","status":400,
			"detail":"audio.node: this node has advertised no audio output capability; placement is refused against the node's own probe evidence, never against the operator's claim alone"}`)
	}))
	defer ts.Close()

	var stdout, stderr bytes.Buffer
	code := cmdAudio([]string{
		"node", "set",
		"--program-route", "hw:0,0", "--ltc-route", "hw:0,0",
		"--clock-domain", "d", "--clock-domain-provenance", "p",
		"--server", ts.URL, "--token", "t",
		"render-01",
	}, &stdout, &stderr, time.Now)
	if code == exitOK {
		t.Fatalf("exit code = %d, want a failure code", code)
	}
	if !strings.Contains(stderr.String(), "probe evidence") {
		t.Fatalf("stderr does not surface the coordinator's refusal reason: %s", stderr.String())
	}
}

func TestCmdAudioListNodesTable(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet || r.URL.Path != "/api/v1/config/audio.node" {
			t.Errorf("request = %s %s, want GET /api/v1/config/audio.node", r.Method, r.URL.Path)
		}
		w.Header().Set("Content-Type", "application/json")
		w.Header().Set("ShowMesh-API-Version", "1")
		_, _ = fmt.Fprint(w, `{"serverTime":"2026-08-17T00:00:00Z","kind":"audio.node","objects":[
			{"id":"render-01","label":"hw:0,0","show":"","currentRevision":1,"updatedAt":"2026-08-17T00:00:00Z"}]}`)
	}))
	defer ts.Close()

	var stdout, stderr bytes.Buffer
	code := cmdAudio([]string{"node", "list", "--server", ts.URL, "--token", "t"}, &stdout, &stderr, time.Now)
	if code != exitOK {
		t.Fatalf("exit code = %d, want exitOK; stderr=%s", code, stderr.String())
	}
	if !strings.Contains(stdout.String(), "render-01") {
		t.Fatalf("output missing render-01:\n%s", stdout.String())
	}
}
