package api

import (
	"bytes"
	"encoding/json"
	"flag"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// updateGolden regenerates every golden file this package's tests compare
// against, instead of comparing against it. Run as:
//
//	go test ./internal/coordinator/api/... -run TestGolden -update
//
// (documented again, verbatim, in this package's report and expected by
// Task D's spec section 7 — "golden files... with a -update flag, and a
// README line for regenerating them").
var updateGolden = flag.Bool("update", false, "update golden test files instead of comparing against them")

func testLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(io.Discard, nil))
}

func fixedClock(t time.Time) func() time.Time {
	return func() time.Time { return t }
}

// goldenPath returns testdata/golden/<name>.json.
func goldenPath(name string) string {
	return filepath.Join("testdata", "golden", name+".json")
}

// assertGolden compares got (a raw JSON response body) against the named
// golden file, byte for byte after re-indenting both to a stable format.
// This is deliberately a raw-bytes/raw-keys comparison, never a decode
// into a Go struct and re-encode: per the contract's standing rule
// (section 1), a test that round-trips through the same struct it is
// meant to be checking cannot catch a renamed JSON tag. Indenting is only
// cosmetic normalization (so this file's on-disk diffs are readable); it
// does not touch key names, key order beyond what encoding/json.Indent
// preserves from the original encoding, or values.
func assertGolden(t *testing.T, name string, got []byte) {
	t.Helper()

	var buf bytes.Buffer
	if err := json.Indent(&buf, got, "", "  "); err != nil {
		t.Fatalf("response for %s is not valid JSON: %v\nbody: %s", name, err, got)
	}
	gotPretty := append(buf.Bytes(), '\n')

	path := goldenPath(name)
	if *updateGolden {
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatalf("creating golden dir: %v", err)
		}
		if err := os.WriteFile(path, gotPretty, 0o644); err != nil {
			t.Fatalf("writing golden file %s: %v", path, err)
		}
		return
	}

	want, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("reading golden file %s: %v\n(run with -update to create it; got body:\n%s)", path, err, gotPretty)
	}
	if !bytes.Equal(gotPretty, want) {
		t.Errorf("response for %q does not match golden file %s.\n--- got ---\n%s\n--- want ---\n%s", name, path, gotPretty, want)
	}
}

// doRequest drives h directly with an httptest.NewRequest/ResponseRecorder
// pair — sufficient for every handler-level test in this package.
// stream_test.go uses a real httptest.Server instead, because SSE framing
// (contract section 6.4's exact "event:"/"data:" lines and blank-line
// terminators, and the absence of any "id:" line) can only be proven
// against a real HTTP response body being streamed over a real
// connection, not a ResponseRecorder's buffered write.
func doRequest(t *testing.T, h http.Handler, method, target string, headers map[string]string) (*http.Response, []byte) {
	t.Helper()
	req := httptest.NewRequest(method, target, nil)
	for k, v := range headers {
		req.Header.Set(k, v)
	}
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	resp := rec.Result()
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatalf("reading response body: %v", err)
	}
	return resp, body
}

// newJSONRequest builds an httptest.NewRequest with an
// "application/json" Content-Type and the given headers, for a test that
// needs to POST/DELETE a JSON body — every doRequest call site sends no
// body at all, which is not enough for the ADR-024 session endpoints.
func newJSONRequest(t *testing.T, method, target, body string, headers map[string]string) *http.Request {
	t.Helper()
	req := httptest.NewRequest(method, target, strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	for k, v := range headers {
		req.Header.Set(k, v)
	}
	return req
}

// doRawRequest is [doRequest] for a caller that already built its own
// *http.Request (via [newJSONRequest]) rather than starting from a bare
// method/target/headers triple.
func doRawRequest(t *testing.T, h http.Handler, req *http.Request) (*http.Response, []byte) {
	t.Helper()
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	resp := rec.Result()
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatalf("reading response body: %v", err)
	}
	return resp, body
}

// slogTestLogger is [testLogger] but writing to buf instead of discarding,
// for a test asserting on what does or (per ADR-021 rule 4/ADR-024) does
// NOT appear in a log line — e.g. that a credential value never reaches
// one.
func slogTestLogger(buf *strings.Builder) *slog.Logger {
	return slog.New(slog.NewTextHandler(buf, nil))
}

// postThroughShortWriteTimeoutServer posts body to path against a REAL
// *http.Server with a short WriteTimeout, then returns the raw status and
// body for the caller to assert on - both are real evidence a route's own
// SetWriteDeadline extension delivered, whether the route's own honest
// answer is 200 (an unconfirmed outcome) or a refusal status (a withheld
// interlock gate). This is the shared shape
// TestEmergencyStopSurvivesServerWriteTimeout (emergencystop_writetimeout_test.go)
// established and every per-route write-deadline-extension test in this
// package now reuses: a passing test against a handler that forgot its own
// extension would otherwise still pass if the caller's own pacing (an
// onAwaitResponse-style hook, sleeping past ts.Config.WriteTimeout while
// staying well inside the handler's real, much larger extended deadline)
// were removed - see that test's own doc comment for the trap this guards
// against. httptest.NewRecorder (doRequest/doRawRequest above) cannot
// exercise this at all: net/http.Server.WriteTimeout only ever fires
// against a real network connection.
func postThroughShortWriteTimeoutServer(t *testing.T, handler http.Handler, path, body string, auth map[string]string) (status int, respBody []byte) {
	t.Helper()
	ts := httptest.NewUnstartedServer(handler)
	ts.Config.WriteTimeout = 50 * time.Millisecond
	ts.Start()
	defer ts.Close()

	req, err := http.NewRequest(http.MethodPost, ts.URL+path, strings.NewReader(body))
	if err != nil {
		t.Fatalf("build request: %v", err)
	}
	req.Header.Set("Content-Type", "application/json")
	for k, v := range auth {
		req.Header.Set(k, v)
	}

	httpClient := &http.Client{Timeout: 10 * time.Second}
	resp, err := httpClient.Do(req)
	if err != nil {
		t.Fatalf("request paced past the server write timeout failed: %v", err)
	}
	defer func() { _ = resp.Body.Close() }()
	respBody, err = io.ReadAll(resp.Body)
	if err != nil {
		t.Fatalf("reading response body: %v", err)
	}
	return resp.StatusCode, respBody
}

// decodeMap decodes body into a generic map[string]any, the contract's
// standing-rule-compliant way to assert on specific keys without
// round-tripping through this package's own wire structs (which would
// hide a renamed JSON tag).
func decodeMap(t *testing.T, body []byte) map[string]any {
	t.Helper()
	var m map[string]any
	if err := json.Unmarshal(body, &m); err != nil {
		t.Fatalf("decoding response as a generic map: %v\nbody: %s", err, body)
	}
	return m
}
