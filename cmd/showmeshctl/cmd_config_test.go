package main

import (
	"bytes"
	"encoding/json"
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

const testFPPEndpointsConfigResponse = `{
	"serverTime":"2026-08-12T00:00:00Z","kind":"fpp.endpoints","revision":1,
	"payload":{"endpoints":[{"id":"player-01","url":"http://10.0.1.20"}]},
	"updatedAt":"2026-08-12T00:00:00Z","createdByPrincipalId":"p-1","createdByPrincipalName":"admin-1",
	"source":"api","restartRequired":false,
	"restartRequiredReason":"this change is already in effect: command dispatch resolves the endpoint list per request, and the collector set follows this configuration within about ten seconds. No restart is needed."
}`

// testFPPEndpointsConfigResponseRestartRequired is the same fixture with
// restartRequired true, for the loud-warning rendering path. No shipped
// server response looks like this since ADR-036, but the CLI renders
// whatever the wire says rather than assuming the current server behavior.
const testFPPEndpointsConfigResponseRestartRequired = `{
	"serverTime":"2026-08-12T00:00:00Z","kind":"fpp.endpoints","revision":1,
	"payload":{"endpoints":[{"id":"player-01","url":"http://10.0.1.20"}]},
	"updatedAt":"2026-08-12T00:00:00Z","createdByPrincipalId":"p-1","createdByPrincipalName":"admin-1",
	"source":"api","restartRequired":true,
	"restartRequiredReason":"this coordinator does not hot-reload configuration; restart to apply"
}`

func TestCmdConfigGetRendersActiveConfiguration(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet || r.URL.Path != "/api/v1/config/fpp.endpoints" {
			t.Errorf("request = %s %s, want GET /api/v1/config/fpp.endpoints", r.Method, r.URL.Path)
		}
		w.Header().Set("Content-Type", "application/json")
		w.Header().Set("ShowMesh-API-Version", "1")
		_, _ = fmt.Fprint(w, testFPPEndpointsConfigResponse)
	}))
	defer ts.Close()

	var stdout, stderr bytes.Buffer
	code := cmdConfig([]string{"get", "--server", ts.URL, "--token", "t"}, &stdout, &stderr, time.Now)
	if code != exitOK {
		t.Fatalf("exit code = %d, want exitOK; stderr=%s", code, stderr.String())
	}
	out := stdout.String()
	if !strings.Contains(out, "player-01") || !strings.Contains(out, "10.0.1.20") {
		t.Errorf("stdout = %q, want it to render the configured endpoint", out)
	}
	if !strings.Contains(out, "this change is already in effect") {
		t.Errorf("stdout = %q, want the reason rendered at the point of use", out)
	}
	if strings.Contains(out, "RESTART REQUIRED") {
		t.Errorf("stdout = %q, want no RESTART REQUIRED label when restartRequired is false", out)
	}
}

// TestCmdConfigGetRendersRestartRequiredWarning proves the CLI still
// renders the loud warning when the wire says restartRequired is true: it
// must render what the wire says, not hardcode today's server behavior.
func TestCmdConfigGetRendersRestartRequiredWarning(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.Header().Set("ShowMesh-API-Version", "1")
		_, _ = fmt.Fprint(w, testFPPEndpointsConfigResponseRestartRequired)
	}))
	defer ts.Close()

	var stdout, stderr bytes.Buffer
	code := cmdConfig([]string{"get", "--server", ts.URL, "--token", "t"}, &stdout, &stderr, time.Now)
	if code != exitOK {
		t.Fatalf("exit code = %d, want exitOK; stderr=%s", code, stderr.String())
	}
	out := stdout.String()
	if !strings.Contains(out, "RESTART REQUIRED: this coordinator does not hot-reload configuration; restart to apply") {
		t.Errorf("stdout = %q, want the loud restart-required warning when restartRequired is true", out)
	}
}

func TestCmdConfigGetNotFound(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/problem+json")
		w.WriteHeader(http.StatusNotFound)
		_, _ = fmt.Fprint(w, `{"type":"https://showmesh.dev/problems/resource-not-found","title":"Resource not found","status":404,"detail":"no fpp.endpoints configuration has been created yet; PUT one to create it"}`)
	}))
	defer ts.Close()

	var stdout, stderr bytes.Buffer
	code := cmdConfig([]string{"get", "--server", ts.URL, "--token", "t"}, &stdout, &stderr, time.Now)
	if code != exitNotFound {
		t.Errorf("exit code = %d, want exitNotFound (%d); stderr=%s", code, exitNotFound, stderr.String())
	}
}

// TestCmdConfigGetForbiddenNamesMissingScope pins that this surface is
// gated by config:write for READS too (Step 7 seam A's own deliberate
// decision — there is no config:read scope).
func TestCmdConfigGetForbiddenNamesMissingScope(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/problem+json")
		w.WriteHeader(http.StatusForbidden)
		_, _ = fmt.Fprint(w, `{"type":"https://showmesh.dev/problems/forbidden","title":"Forbidden","status":403,"detail":"this principal does not hold the required scope: config:write"}`)
	}))
	defer ts.Close()

	var stdout, stderr bytes.Buffer
	code := cmdConfig([]string{"get", "--server", ts.URL, "--token", "t"}, &stdout, &stderr, time.Now)
	if code != exitForbidden {
		t.Errorf("exit code = %d, want exitForbidden (%d); stderr=%s", code, exitForbidden, stderr.String())
	}
	if !strings.Contains(stderr.String(), "config:write") {
		t.Errorf("stderr = %q, want it to name the missing scope", stderr.String())
	}
}

// TestCmdConfigSetPutsTheFileContents proves `config set --file` issues a
// real PUT carrying the file's JSON contents, unmodified, to
// /api/v1/config/fpp.endpoints — this CLI's first-ever write.
func TestCmdConfigSetPutsTheFileContents(t *testing.T) {
	var gotMethod, gotPath string
	var gotBody []byte
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotMethod, gotPath = r.Method, r.URL.Path
		gotBody, _ = io.ReadAll(r.Body)
		w.Header().Set("Content-Type", "application/json")
		w.Header().Set("ShowMesh-API-Version", "1")
		_, _ = fmt.Fprint(w, testFPPEndpointsConfigResponse)
	}))
	defer ts.Close()

	dir := t.TempDir()
	file := filepath.Join(dir, "endpoints.json")
	payload := `{"endpoints":[{"id":"player-01","url":"http://10.0.1.20"}]}`
	if err := os.WriteFile(file, []byte(payload), 0o600); err != nil {
		t.Fatalf("write payload file: %v", err)
	}

	var stdout, stderr bytes.Buffer
	code := cmdConfig([]string{"set", "--file", file, "--server", ts.URL, "--token", "t"}, &stdout, &stderr, time.Now)
	if code != exitOK {
		t.Fatalf("exit code = %d, want exitOK; stderr=%s", code, stderr.String())
	}
	if gotMethod != http.MethodPut {
		t.Errorf("method = %q, want PUT", gotMethod)
	}
	if gotPath != "/api/v1/config/fpp.endpoints" {
		t.Errorf("path = %q, want /api/v1/config/fpp.endpoints", gotPath)
	}
	if !strings.Contains(string(gotBody), "player-01") || !strings.Contains(string(gotBody), "10.0.1.20") {
		t.Errorf("request body = %s, want the file's endpoint", gotBody)
	}
	if !strings.Contains(stderr.String(), "revision 1 is now active") {
		t.Errorf("stderr = %q, want confirmation of the new active revision and the restart note", stderr.String())
	}
}

// TestCmdConfigSetAcceptsAFullConfigGetResponse is Step 7 seam A review
// defect 2's own regression test: feed `config set` the EXACT bytes
// `config get --output json` prints (testFPPEndpointsConfigResponse — the
// endpoint nested three levels down, under "payload"), and prove the PUT
// body it sends still carries that endpoint. Before this fix, `config
// set` decoded this shape directly as {"endpoints":[...]}, found no
// top-level "endpoints" key, silently kept Endpoints nil, and PUT a body
// that wiped every configured instance — reproduced against a live
// coordinator, which then refused to start on its next restart.
func TestCmdConfigSetAcceptsAFullConfigGetResponse(t *testing.T) {
	var gotBody []byte
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotBody, _ = io.ReadAll(r.Body)
		w.Header().Set("Content-Type", "application/json")
		w.Header().Set("ShowMesh-API-Version", "1")
		_, _ = fmt.Fprint(w, testFPPEndpointsConfigResponse)
	}))
	defer ts.Close()

	dir := t.TempDir()
	file := filepath.Join(dir, "get-output.json")
	// The EXACT shape `config get --output json` emits — not a hand-typed
	// approximation of it.
	if err := os.WriteFile(file, []byte(testFPPEndpointsConfigResponse), 0o600); err != nil {
		t.Fatalf("write payload file: %v", err)
	}

	var stdout, stderr bytes.Buffer
	code := cmdConfig([]string{"set", "--file", file, "--server", ts.URL, "--token", "t"}, &stdout, &stderr, time.Now)
	if code != exitOK {
		t.Fatalf("exit code = %d, want exitOK; stderr=%s", code, stderr.String())
	}

	var sent configFPPEndpointsPayload
	if err := json.Unmarshal(gotBody, &sent); err != nil {
		t.Fatalf("PUT body did not decode as {\"endpoints\":[...]}: %v; body=%s", err, gotBody)
	}
	if len(sent.Endpoints) != 1 || sent.Endpoints[0].ID != "player-01" || sent.Endpoints[0].URL != "http://10.0.1.20" {
		t.Fatalf("PUT body endpoints = %+v, want the one endpoint from the config get response — the round trip must survive",
			sent.Endpoints)
	}
}

// TestUnwrapConfigGetResponseUnwrapsTheWrapperShape proves
// unwrapConfigGetResponse — the generalization of parseConfigSetPayload's
// own wrapper detection to every config kind — recognizes the full object
// every "<kind> get --output json" prints (kind, revision, payload,
// updatedAt, source all present together) and returns the bytes of its
// "payload" field alone.
func TestUnwrapConfigGetResponseUnwrapsTheWrapperShape(t *testing.T) {
	got, err := unwrapConfigGetResponse([]byte(testFPPEndpointsConfigResponse))
	if err != nil {
		t.Fatalf("unwrapConfigGetResponse: %v", err)
	}
	var payload configFPPEndpointsPayload
	if err := json.Unmarshal(got, &payload); err != nil {
		t.Fatalf("unwrapped bytes did not decode as the bare payload: %v; got=%s", err, got)
	}
	if len(payload.Endpoints) != 1 || payload.Endpoints[0].ID != "player-01" {
		t.Fatalf("unwrapped payload = %+v, want the one endpoint from the wrapper", payload)
	}
}

// TestUnwrapConfigGetResponsePassesThroughBarePayload proves a payload
// with no top-level "payload" key at all — the shape an operator would
// hand-type — passes through byte-for-byte unchanged: today's behavior.
func TestUnwrapConfigGetResponsePassesThroughBarePayload(t *testing.T) {
	const bare = `{"endpoints":[{"id":"player-01","url":"http://10.0.1.20"}]}`
	got, err := unwrapConfigGetResponse([]byte(bare))
	if err != nil {
		t.Fatalf("unwrapConfigGetResponse: %v", err)
	}
	if string(got) != bare {
		t.Errorf("got = %s, want the bare payload returned unchanged: %s", got, bare)
	}
}

// TestUnwrapConfigGetResponseRejectsNullPayload proves a JSON `null`
// "payload" value, alongside the wrapper's own marker keys, is refused
// rather than silently unwrapped to a zero value: a JSON null is not an
// absent key.
func TestUnwrapConfigGetResponseRejectsNullPayload(t *testing.T) {
	const wrapper = `{"kind":"fpp.endpoints","revision":1,"payload":null,"updatedAt":"2026-08-12T00:00:00Z","source":"api"}`
	_, err := unwrapConfigGetResponse([]byte(wrapper))
	if err == nil {
		t.Fatal("unwrapConfigGetResponse returned no error, want a refusal naming the null payload")
	}
	if !strings.Contains(err.Error(), "null") {
		t.Errorf("error = %q, want it to name the null payload", err.Error())
	}
}

// TestUnwrapConfigGetResponseRejectsAmbiguousShape proves a "payload" key
// present WITHOUT both wrapper marker keys ("kind" and "revision") is
// refused as ambiguous — naming both shapes this helper accepts — rather
// than guessed either way. A bare payload could legitimately have its own
// field named "payload"; only the full marker set makes the wrapper shape
// unambiguous.
func TestUnwrapConfigGetResponseRejectsAmbiguousShape(t *testing.T) {
	const ambiguous = `{"kind":"fpp.endpoints","payload":{"endpoints":[]}}`
	_, err := unwrapConfigGetResponse([]byte(ambiguous))
	if err == nil {
		t.Fatal("unwrapConfigGetResponse returned no error, want a refusal naming both accepted shapes")
	}
	for _, want := range []string{"kind", "revision", "payload"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error = %q, want it to mention %q", err.Error(), want)
		}
	}
}

// TestCmdConfigSetRejectsBodyWithNoEndpointsKeyAnywhere is Step 7 seam A
// review defect 2's other half: this CLI must refuse, client-side, any
// input where it cannot find an "endpoints" key at all (neither bare nor
// under "payload") — it must never send a request with a nil/absent
// endpoints list. The server URL is deliberately unreachable: a correct
// fix never issues the request at all.
func TestCmdConfigSetRejectsBodyWithNoEndpointsKeyAnywhere(t *testing.T) {
	dir := t.TempDir()
	file := filepath.Join(dir, "no-endpoints.json")
	if err := os.WriteFile(file, []byte(`{"foo":"bar"}`), 0o600); err != nil {
		t.Fatalf("write payload file: %v", err)
	}

	var stdout, stderr bytes.Buffer
	code := cmdConfig([]string{"set", "--file", file, "--server", "http://unused.invalid"}, &stdout, &stderr, time.Now)
	if code != exitUsage {
		t.Errorf("exit code = %d, want exitUsage (%d); stderr=%s", code, exitUsage, stderr.String())
	}
	if !strings.Contains(stderr.String(), "endpoints") {
		t.Errorf("stderr = %q, want it to name the missing \"endpoints\" key", stderr.String())
	}
}

// TestCmdConfigSetRejectsNullEndpoints proves the identical "a JSON null
// is not an absent key" rule the coordinator's own decode enforces is ALSO
// enforced client-side: {"endpoints":null} must never reach the wire as a
// PUT body. The server URL is deliberately unreachable: a correct fix
// never issues the request at all.
func TestCmdConfigSetRejectsNullEndpoints(t *testing.T) {
	dir := t.TempDir()
	file := filepath.Join(dir, "null-endpoints.json")
	if err := os.WriteFile(file, []byte(`{"endpoints":null}`), 0o600); err != nil {
		t.Fatalf("write payload file: %v", err)
	}

	var stdout, stderr bytes.Buffer
	code := cmdConfig([]string{"set", "--file", file, "--server", "http://unused.invalid"}, &stdout, &stderr, time.Now)
	if code != exitUsage {
		t.Errorf("exit code = %d, want exitUsage (%d); stderr=%s", code, exitUsage, stderr.String())
	}
}

// TestCmdConfigSetRejectsPayloadWithNoEndpointsKey mirrors
// TestCmdConfigSetRejectsBodyWithNoEndpointsKeyAnywhere for the
// "config get"-shaped path: a "payload" object present but with no
// "endpoints" key inside it must not silently become an empty list.
func TestCmdConfigSetRejectsPayloadWithNoEndpointsKey(t *testing.T) {
	dir := t.TempDir()
	file := filepath.Join(dir, "payload-no-endpoints.json")
	if err := os.WriteFile(file, []byte(`{"serverTime":"2026-08-12T00:00:00Z","kind":"fpp.endpoints","payload":{}}`), 0o600); err != nil {
		t.Fatalf("write payload file: %v", err)
	}

	var stdout, stderr bytes.Buffer
	code := cmdConfig([]string{"set", "--file", file, "--server", "http://unused.invalid"}, &stdout, &stderr, time.Now)
	if code != exitUsage {
		t.Errorf("exit code = %d, want exitUsage (%d); stderr=%s", code, exitUsage, stderr.String())
	}
}

// TestCmdConfigSetReadsStdinWhenNoFileGiven proves the spec's "reading a
// payload from a file OR stdin" requirement's stdin half.
func TestCmdConfigSetReadsStdinWhenNoFileGiven(t *testing.T) {
	var gotBody []byte
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotBody, _ = io.ReadAll(r.Body)
		w.Header().Set("Content-Type", "application/json")
		w.Header().Set("ShowMesh-API-Version", "1")
		_, _ = fmt.Fprint(w, testFPPEndpointsConfigResponse)
	}))
	defer ts.Close()

	oldStdin := os.Stdin
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatalf("os.Pipe: %v", err)
	}
	os.Stdin = r
	defer func() { os.Stdin = oldStdin }()

	payload := `{"endpoints":[{"id":"shed","url":"http://10.0.1.21"}]}`
	go func() {
		_, _ = w.Write([]byte(payload))
		_ = w.Close()
	}()

	var stdout, stderr bytes.Buffer
	code := cmdConfig([]string{"set", "--server", ts.URL, "--token", "t"}, &stdout, &stderr, time.Now)
	if code != exitOK {
		t.Fatalf("exit code = %d, want exitOK; stderr=%s", code, stderr.String())
	}
	if !strings.Contains(string(gotBody), "shed") {
		t.Errorf("request body = %s, want stdin's endpoint", gotBody)
	}
}

func TestCmdConfigSetRejectsMalformedJSON(t *testing.T) {
	dir := t.TempDir()
	file := filepath.Join(dir, "bad.json")
	if err := os.WriteFile(file, []byte("not json"), 0o600); err != nil {
		t.Fatalf("write payload file: %v", err)
	}

	var stdout, stderr bytes.Buffer
	code := cmdConfig([]string{"set", "--file", file, "--server", "http://unused.invalid"}, &stdout, &stderr, time.Now)
	if code != exitUsage {
		t.Errorf("exit code = %d, want exitUsage (%d); stderr=%s", code, exitUsage, stderr.String())
	}
}

func TestCmdConfigSetRejectsMissingFile(t *testing.T) {
	var stdout, stderr bytes.Buffer
	code := cmdConfig([]string{"set", "--file", "/no/such/file.json", "--server", "http://unused.invalid"}, &stdout, &stderr, time.Now)
	if code != exitUsage {
		t.Errorf("exit code = %d, want exitUsage (%d); stderr=%s", code, exitUsage, stderr.String())
	}
}

// TestCmdConfigSetInvalidConfigurationRejected pins that a coordinator
// refusal (400 invalid-parameter, ADR-009's before-activation rule) is
// reported honestly rather than as success.
func TestCmdConfigSetInvalidConfigurationRejected(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/problem+json")
		w.WriteHeader(http.StatusBadRequest)
		_, _ = fmt.Fprint(w, `{"type":"https://showmesh.dev/problems/invalid-parameter","title":"Invalid parameter","status":400,"detail":"instance id \"bad id\" is not valid"}`)
	}))
	defer ts.Close()

	dir := t.TempDir()
	file := filepath.Join(dir, "endpoints.json")
	if err := os.WriteFile(file, []byte(`{"endpoints":[{"id":"bad id","url":"http://x"}]}`), 0o600); err != nil {
		t.Fatalf("write payload file: %v", err)
	}

	var stdout, stderr bytes.Buffer
	code := cmdConfig([]string{"set", "--file", file, "--server", ts.URL, "--token", "t"}, &stdout, &stderr, time.Now)
	if code == exitOK {
		t.Fatalf("exit code = exitOK, want a non-zero exit for a rejected configuration; stdout=%s", stdout.String())
	}
	if !strings.Contains(stderr.String(), "not valid") {
		t.Errorf("stderr = %q, want the coordinator's rejection reason", stderr.String())
	}
}

const testConfigRevisionsResponse = `{
	"serverTime":"2026-08-12T00:00:00Z","kind":"fpp.endpoints",
	"revisions":[
		{"revision":2,"createdAt":"2026-08-12T00:01:00Z","createdByPrincipalId":"p-1","createdByPrincipalName":"admin-1","source":"api","note":"","active":true},
		{"revision":1,"createdAt":"2026-08-12T00:00:00Z","createdByPrincipalId":null,"createdByPrincipalName":null,"source":"env_migration","note":"migrated from SHOWMESH_FPP_ENDPOINTS at coordinator startup","active":false}
	]
}`

func TestCmdConfigRevisionsRendersHistoryNewestFirst(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/v1/config/fpp.endpoints/revisions" {
			t.Errorf("path = %q, want /api/v1/config/fpp.endpoints/revisions", r.URL.Path)
		}
		w.Header().Set("Content-Type", "application/json")
		w.Header().Set("ShowMesh-API-Version", "1")
		_, _ = fmt.Fprint(w, testConfigRevisionsResponse)
	}))
	defer ts.Close()

	var stdout, stderr bytes.Buffer
	code := cmdConfig([]string{"revisions", "--server", ts.URL, "--token", "t"}, &stdout, &stderr, time.Now)
	if code != exitOK {
		t.Fatalf("exit code = %d, want exitOK; stderr=%s", code, stderr.String())
	}
	out := stdout.String()
	if !strings.Contains(out, "env_migration") {
		t.Errorf("stdout = %q, want the env_migration revision rendered", out)
	}
	// Newest first: revision 2's line (rendered "api", the newer
	// revision's source) must appear before revision 1's ("env_migration").
	if i2, i1 := strings.Index(out, "api"), strings.Index(out, "env_migration"); i2 == -1 || i1 == -1 || i2 > i1 {
		t.Errorf("stdout = %q, want revision 2 (source api) listed before revision 1 (source env_migration)", out)
	}
}

func TestCmdConfigRevisionsNoRevisions(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.Header().Set("ShowMesh-API-Version", "1")
		_, _ = fmt.Fprint(w, `{"serverTime":"2026-08-12T00:00:00Z","kind":"fpp.endpoints","revisions":[]}`)
	}))
	defer ts.Close()

	var stdout, stderr bytes.Buffer
	code := cmdConfig([]string{"revisions", "--server", ts.URL, "--token", "t"}, &stdout, &stderr, time.Now)
	if code != exitOK {
		t.Fatalf("exit code = %d, want exitOK; stderr=%s", code, stderr.String())
	}
	if !strings.Contains(stdout.String(), "no revisions") {
		t.Errorf("stdout = %q, want an explicit empty message", stdout.String())
	}
}

func TestCmdConfigUnknownSubcommand(t *testing.T) {
	var stdout, stderr bytes.Buffer
	code := cmdConfig([]string{"bogus"}, &stdout, &stderr, time.Now)
	if code != exitUsage {
		t.Errorf("exit code = %d, want exitUsage (%d)", code, exitUsage)
	}
}

func TestCmdConfigNoSubcommand(t *testing.T) {
	var stdout, stderr bytes.Buffer
	code := cmdConfig(nil, &stdout, &stderr, time.Now)
	if code != exitUsage {
		t.Errorf("exit code = %d, want exitUsage (%d)", code, exitUsage)
	}
}

func TestCmdConfigHelp(t *testing.T) {
	var stdout, stderr bytes.Buffer
	code := cmdConfig([]string{"help"}, &stdout, &stderr, time.Now)
	if code != exitOK {
		t.Errorf("exit code = %d, want exitOK", code)
	}
	if !strings.Contains(stdout.String(), "config:write") {
		t.Errorf("stdout = %q, want the help text to name the required scope", stdout.String())
	}
}
