package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"
)

// writeStdinForTest redirects os.Stdin to a pipe carrying payload for the
// duration of the calling test, mirroring
// TestCmdConfigSetReadsStdinWhenNoFileGiven's own pattern.
func writeStdinForTest(t *testing.T, payload string) {
	t.Helper()
	oldStdin := os.Stdin
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatalf("os.Pipe: %v", err)
	}
	os.Stdin = r
	t.Cleanup(func() { os.Stdin = oldStdin })
	go func() {
		_, _ = w.Write([]byte(payload))
		_ = w.Close()
	}()
}

func emergencyStopConfigFakeResponse(revision int64) string {
	return fmt.Sprintf(`{"serverTime":"2026-08-23T21:00:00Z","kind":"show.emergencystop","revision":%d,
		"payload":{"stop":{"actions":["worklights-on"]},"stopPowerDown":{"actions":[]},"hardStop":{"actions":[]}},
		"updatedAt":"2026-08-23T21:00:00Z","createdByPrincipalId":"p1","createdByPrincipalName":"admin","source":"api"}`, revision)
}

func TestCmdEmergencyStopConfigGet(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet || r.URL.Path != "/api/v1/config/show.emergencystop" {
			t.Errorf("request = %s %s, want GET /api/v1/config/show.emergencystop", r.Method, r.URL.Path)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = fmt.Fprint(w, emergencyStopConfigFakeResponse(3))
	}))
	defer ts.Close()

	var stdout, stderr bytes.Buffer
	code := cmdEmergencyStop([]string{"config", "get", "--server", ts.URL}, &stdout, &stderr, func() time.Time { return time.Now() })
	if code != exitOK {
		t.Fatalf("exit code = %d, want exitOK; stderr=%s", code, stderr.String())
	}
	if !strings.Contains(stdout.String(), "worklights-on") {
		t.Errorf("stdout = %q, want it to mention the configured follow-up action", stdout.String())
	}
}

func TestCmdEmergencyStopConfigSetSendsTheFullReplacementBody(t *testing.T) {
	var gotMethod, gotPath string
	var gotBody []byte
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotMethod, gotPath = r.Method, r.URL.Path
		gotBody, _ = io.ReadAll(r.Body)
		w.Header().Set("Content-Type", "application/json")
		_, _ = fmt.Fprint(w, emergencyStopConfigFakeResponse(4))
	}))
	defer ts.Close()

	payload := `{"stop":{"actions":["worklights-on"]},"stopPowerDown":{"actions":[]},"hardStop":{"actions":[]}}`
	writeStdinForTest(t, payload)

	var stdout, stderr bytes.Buffer
	code := cmdEmergencyStop([]string{"config", "set", "--server", ts.URL}, &stdout, &stderr, func() time.Time { return time.Now() })
	if code != exitOK {
		t.Fatalf("exit code = %d, want exitOK; stderr=%s", code, stderr.String())
	}
	if gotMethod != http.MethodPut || gotPath != "/api/v1/config/show.emergencystop" {
		t.Errorf("request = %s %s, want PUT /api/v1/config/show.emergencystop", gotMethod, gotPath)
	}
	var sent map[string]any
	if err := json.Unmarshal(gotBody, &sent); err != nil {
		t.Fatalf("request body %s is not JSON: %v", gotBody, err)
	}
	if _, ok := sent["stop"]; !ok {
		t.Errorf("request body = %s, want it to carry the stop level", gotBody)
	}
	if !strings.Contains(stderr.String(), "revision 4 is now active") {
		t.Errorf("stderr = %q, want it to report the new revision", stderr.String())
	}
}

func TestCmdEmergencyStopConfigSetRejectsInvalidJSONWithoutCallingTheServer(t *testing.T) {
	called := false
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		called = true
	}))
	defer ts.Close()

	writeStdinForTest(t, "not json")

	var stdout, stderr bytes.Buffer
	code := cmdEmergencyStop([]string{"config", "set", "--server", ts.URL}, &stdout, &stderr, func() time.Time { return time.Now() })
	if code != exitUsage {
		t.Fatalf("exit code = %d, want exitUsage; stderr=%s", code, stderr.String())
	}
	if called {
		t.Error("invalid JSON reached the server; it must be rejected locally first")
	}
}
