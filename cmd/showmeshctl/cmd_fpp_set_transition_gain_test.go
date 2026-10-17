package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

// transitionGainServer answers one write with players, built from the
// request's own requestId, and records the request body and path.
func transitionGainServer(t *testing.T, players func(requestID string) string) (*httptest.Server, *[]byte, *string) {
	t.Helper()
	var raw []byte
	var path string
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		raw, _ = io.ReadAll(r.Body)
		path = r.Method + " " + r.URL.Path
		var body map[string]any
		_ = json.Unmarshal(raw, &body)
		w.Header().Set("Content-Type", "application/json")
		w.Header().Set("ShowMesh-API-Version", "1")
		_, _ = fmt.Fprintf(w, `{"serverTime":"2026-08-13T22:00:00Z","players":[%s]}`, players(fmt.Sprint(body["requestId"])))
	}))
	t.Cleanup(ts.Close)
	return ts, &raw, &path
}

func appliedPlayer(id, outcome string, start, target, fade, ceiling, effective int) func(string) string {
	return func(req string) string {
		return fmt.Sprintf(`{"instanceId":%q,"requestId":"%s:%s","outcome":%q,"gainStart":%d,"gainTarget":%d,"fadeSeconds":%d,"ceiling":%d,"effectiveOutput":%d}`,
			id, req, id, outcome, start, target, fade, ceiling, effective)
	}
}

func TestCmdFPPSetTransitionGainWritesEveryFPPAndPrintsEach(t *testing.T) {
	a := appliedPlayer("bench-fpp", "applied", 100, 75, 30, 60, 45)
	b := appliedPlayer("remote-01", "applied", 100, 75, 30, 80, 60)
	ts, raw, path := transitionGainServer(t, func(req string) string { return a(req) + "," + b(req) })

	var stdout, stderr bytes.Buffer
	code := cmdFPP([]string{"set-transition-gain", "--server", ts.URL, "--fade-seconds", "30", "75"}, &stdout, &stderr, time.Now)
	if code != exitOK {
		t.Fatalf("exit code = %d, want exitOK; stdout=%s stderr=%s", code, stdout.String(), stderr.String())
	}
	if *path != "POST /api/v1/fpp/brightness/transition-gain" {
		t.Errorf("request = %q, want POST /api/v1/fpp/brightness/transition-gain", *path)
	}
	out := stdout.String()
	for _, want := range []string{"bench-fpp gain 100 -> 75 over 30s; ceiling 60, effective output 45", "remote-01 gain 100 -> 75 over 30s; ceiling 80, effective output 60"} {
		if !strings.Contains(out, want) {
			t.Errorf("stdout = %q, want it to contain %q", out, want)
		}
	}
	sent := string(*raw)
	if !strings.Contains(sent, `"targetPercent":75`) || !strings.Contains(sent, `"fadeSeconds":30`) || !strings.Contains(sent, `"requestId":"`) {
		t.Errorf("request body = %s, want targetPercent 75, fadeSeconds 30 and a minted requestId", sent)
	}
}

func TestCmdFPPSetTransitionGainExitsNonZeroWhenAnFPPFailed(t *testing.T) {
	a := appliedPlayer("bench-fpp", "applied", 100, 50, 0, 60, 30)
	ts, _, _ := transitionGainServer(t, func(req string) string {
		return a(req) + `,{"instanceId":"remote-01","requestId":"x","outcome":"failed","reason":"The gain on the player remote-01 was not set. Check that it is on and its ShowMesh plugin is installed."}`
	})
	var stdout, stderr bytes.Buffer
	code := cmdFPP([]string{"set-transition-gain", "--server", ts.URL, "50"}, &stdout, &stderr, time.Now)
	if code != exitActionFailed {
		t.Fatalf("exit code = %d, want exitActionFailed; stdout=%s", code, stdout.String())
	}
	if !strings.Contains(stdout.String(), "failed: The gain on the player remote-01 was not set.") || !strings.Contains(stdout.String(), "applied: bench-fpp") {
		t.Errorf("stdout = %q, want both FPPs' outcomes", stdout.String())
	}
}

// A repeated --request-id reports the gain unchanged and exits 0.
func TestCmdFPPSetTransitionGainAlreadyAppliedIsASuccess(t *testing.T) {
	ts, raw, _ := transitionGainServer(t, appliedPlayer("bench-fpp", "alreadyApplied", 75, 75, 0, 60, 45))
	var stdout, stderr bytes.Buffer
	code := cmdFPP([]string{"set-transition-gain", "--server", ts.URL, "--request-id", "req-1", "75"}, &stdout, &stderr, time.Now)
	if code != exitOK {
		t.Fatalf("exit code = %d, want exitOK; stderr=%s", code, stderr.String())
	}
	if !strings.Contains(stdout.String(), "unchanged") || !strings.Contains(stdout.String(), "effective output 45") {
		t.Errorf("stdout = %q, want unchanged with the current state", stdout.String())
	}
	if !strings.Contains(string(*raw), `"requestId":"req-1"`) {
		t.Errorf("request body = %s, want the caller's requestId verbatim", *raw)
	}
}

func TestCmdFPPSetTransitionGainRefusesBadArgumentsLocally(t *testing.T) {
	for _, tt := range []struct {
		name string
		args []string
	}{
		{"percent above 100", []string{"101"}},
		{"percent below 0", []string{"-1"}},
		{"fade above the bound", []string{"--fade-seconds", "86401", "50"}},
		{"an instance argument", []string{"bench-fpp", "50"}},
	} {
		t.Run(tt.name, func(t *testing.T) {
			called := false
			ts := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { called = true }))
			defer ts.Close()
			var stdout, stderr bytes.Buffer
			code := cmdFPP(append([]string{"set-transition-gain", "--server", ts.URL}, tt.args...), &stdout, &stderr, time.Now)
			if code != exitUsage || called {
				t.Fatalf("exit code = %d, called = %v; want a local exitUsage refusal", code, called)
			}
		})
	}
}
