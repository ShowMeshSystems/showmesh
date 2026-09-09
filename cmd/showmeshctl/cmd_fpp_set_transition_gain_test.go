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

// transitionGainServer answers one write with the applied state the
// coordinator carries through from the FPP plugin, and records the request
// body so a refused value can be proven to have been sent nowhere.
func transitionGainServer(t *testing.T, applied bool, gainStart, gainTarget, fadeSeconds, ceiling, effective int) (*httptest.Server, *[]byte) {
	t.Helper()
	var raw []byte
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		raw, _ = io.ReadAll(r.Body)
		var body map[string]any
		_ = json.Unmarshal(raw, &body)
		w.Header().Set("Content-Type", "application/json")
		w.Header().Set("ShowMesh-API-Version", "1")
		_, _ = fmt.Fprintf(w, `{"serverTime":"2026-08-13T22:00:00Z","transitionGain":{
			"instanceId":"bench-fpp","requestId":%q,"applied":%t,"gainStart":%d,"gainTarget":%d,
			"fadeSeconds":%d,"ceiling":%d,"effectiveOutput":%d}}`,
			fmt.Sprint(body["requestId"]), applied, gainStart, gainTarget, fadeSeconds, ceiling, effective)
	}))
	t.Cleanup(ts.Close)
	return ts, &raw
}

// TestCmdFPPSetTransitionGainPrintsTheComposedResult: the output must
// carry the composition the caller has no other way to see - the fade's
// bounds, the host's own ceiling, and what actually reaches the channels -
// never a bare "ok".
func TestCmdFPPSetTransitionGainPrintsTheComposedResult(t *testing.T) {
	ts, raw := transitionGainServer(t, true, 100, 75, 30, 60, 45)

	var stdout, stderr bytes.Buffer
	code := cmdFPP([]string{"set-transition-gain", "--server", ts.URL, "--fade-seconds", "30", "bench-fpp", "75"}, &stdout, &stderr, time.Now)
	if code != exitOK {
		t.Fatalf("exit code = %d, want exitOK; stdout=%s stderr=%s", code, stdout.String(), stderr.String())
	}
	out := stdout.String()
	for _, want := range []string{"applied", "100 -> 75", "over 30s", "ceiling 60", "effective output 45"} {
		if !strings.Contains(out, want) {
			t.Errorf("stdout = %q, want it to contain %q", out, want)
		}
	}
	if strings.TrimSpace(out) == "ok" {
		t.Error("stdout is a bare success; want the applied state")
	}
	sent := string(*raw)
	if !strings.Contains(sent, `"targetPercent":75`) || !strings.Contains(sent, `"fadeSeconds":30`) {
		t.Errorf("request body = %s, want targetPercent 75 and fadeSeconds 30 as JSON numbers", sent)
	}
	if !strings.Contains(sent, `"requestId":"`) {
		t.Errorf("request body = %s, want a minted requestId", sent)
	}
}

// TestCmdFPPSetTransitionGainAppliedFalseIsASuccess: a repeated
// --request-id reports the gain unchanged and exits 0. Treating it as a
// failure would tell an operator to retry a write that already took.
func TestCmdFPPSetTransitionGainAppliedFalseIsASuccess(t *testing.T) {
	ts, _ := transitionGainServer(t, false, 75, 75, 0, 60, 45)

	var stdout, stderr bytes.Buffer
	code := cmdFPP([]string{"set-transition-gain", "--server", ts.URL, "--request-id", "req-1", "bench-fpp", "75"}, &stdout, &stderr, time.Now)
	if code != exitOK {
		t.Fatalf("exit code = %d, want exitOK - applied=false is a success; stdout=%s stderr=%s", code, stdout.String(), stderr.String())
	}
	out := stdout.String()
	if !strings.Contains(out, "unchanged") || !strings.Contains(out, "already applied") {
		t.Errorf("stdout = %q, want it to say the gain is unchanged because this requestId was already applied", out)
	}
	if !strings.Contains(out, "effective output 45") {
		t.Errorf("stdout = %q, want the current state reported rather than nothing", out)
	}
}

// TestCmdFPPSetTransitionGainSendsTheCallerSuppliedRequestID: a supplied
// key must reach the wire verbatim, or a retry is not a retry.
func TestCmdFPPSetTransitionGainSendsTheCallerSuppliedRequestID(t *testing.T) {
	ts, raw := transitionGainServer(t, true, 100, 40, 5, 100, 40)

	var stdout, stderr bytes.Buffer
	code := cmdFPP([]string{"set-transition-gain", "--server", ts.URL, "--request-id", "operator-key-1", "bench-fpp", "40"}, &stdout, &stderr, time.Now)
	if code != exitOK {
		t.Fatalf("exit code = %d, want exitOK; stderr=%s", code, stderr.String())
	}
	if !strings.Contains(string(*raw), `"requestId":"operator-key-1"`) {
		t.Errorf("request body = %s, want the caller's own requestId verbatim", *raw)
	}
}

// TestCmdFPPSetTransitionGainRefusesOutOfRangeLocally: refused before
// dispatch, never clamped.
func TestCmdFPPSetTransitionGainRefusesOutOfRangeLocally(t *testing.T) {
	for _, tt := range []struct {
		name string
		args []string
	}{
		{"percent above 100", []string{"bench-fpp", "101"}},
		{"percent below 0", []string{"bench-fpp", "-1"}},
		{"fade above the bound", []string{"--fade-seconds", "86401", "bench-fpp", "50"}},
	} {
		t.Run(tt.name, func(t *testing.T) {
			called := false
			ts := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { called = true }))
			defer ts.Close()

			var stdout, stderr bytes.Buffer
			args := append([]string{"set-transition-gain", "--server", ts.URL}, tt.args...)
			code := cmdFPP(args, &stdout, &stderr, time.Now)
			if code != exitUsage {
				t.Fatalf("exit code = %d, want exitUsage; stderr=%s", code, stderr.String())
			}
			if called {
				t.Error("the coordinator was contacted despite an out-of-range value; want a local refusal before dispatch")
			}
			if !strings.Contains(stderr.String(), "range") {
				t.Errorf("stderr = %q, want it to explain the value is out of range", stderr.String())
			}
		})
	}
}
