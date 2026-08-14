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

// TestCmdFPPSetVolumeRequestShape pins the exact request body: params
// present with volume as a JSON NUMBER, not a string — capture section
// 1.5's own point applies one layer up from FPP: this program's own
// wire parameter is typed, not stringly.
func TestCmdFPPSetVolumeRequestShape(t *testing.T) {
	var rawBody []byte
	var gotBody map[string]any
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		rawBody, _ = io.ReadAll(r.Body)
		_ = json.Unmarshal(rawBody, &gotBody)
		w.Header().Set("Content-Type", "application/json")
		w.Header().Set("ShowMesh-API-Version", "1")
		_, _ = fmt.Fprint(w, `{"serverTime":"2026-08-13T22:00:00Z","command":{
			"id":"cmd-1","idempotencyKey":"`+fmt.Sprint(gotBody["idempotencyKey"])+`","action":"fpp.set_volume","instanceId":"bench-fpp",
			"replay":false,"outcome":"confirmed","outcomeState":"current","outcomeReason":"",
			"attributionDegraded":false,"dispatchedAt":"2026-08-13T22:00:00Z","resolvedAt":"2026-08-13T22:00:01Z"}}`)
	}))
	defer ts.Close()

	var stdout, stderr bytes.Buffer
	code := cmdFPP([]string{"set-volume", "--server", ts.URL, "bench-fpp", "55"}, &stdout, &stderr, time.Now)
	if code != exitOK {
		t.Fatalf("exit code = %d, want exitOK; stdout=%s stderr=%s", code, stdout.String(), stderr.String())
	}
	if gotBody["action"] != "setVolume" {
		t.Errorf("action = %v, want \"setVolume\"", gotBody["action"])
	}
	params, ok := gotBody["params"].(map[string]any)
	if !ok {
		t.Fatalf("body = %s, want a \"params\" object", rawBody)
	}
	volume, ok := params["volume"].(float64) // JSON numbers decode as float64 via encoding/json into any
	if !ok {
		t.Fatalf("params.volume = %v (%T), want a JSON number", params["volume"], params["volume"])
	}
	if volume != 55 {
		t.Errorf("params.volume = %v, want 55", volume)
	}
	// Confirm it is not sent as a quoted string.
	if !strings.Contains(string(rawBody), `"volume":55`) {
		t.Errorf("body = %s, want volume encoded as an unquoted JSON number (55), not a string", rawBody)
	}
}

// TestCmdFPPSetVolumeRejectsOutOfRangeLocally proves an out-of-range
// volume is refused BEFORE dispatch, as a usage error — capture section
// 1.5 measured FPP itself silently CLAMPING an out-of-range value (999 ->
// 100) rather than rejecting it, so "let FPP reject it" does not work;
// this command must validate itself.
func TestCmdFPPSetVolumeRejectsOutOfRangeLocally(t *testing.T) {
	called := false
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		called = true
	}))
	defer ts.Close()

	var stdout, stderr bytes.Buffer
	code := cmdFPP([]string{"set-volume", "--server", ts.URL, "bench-fpp", "999"}, &stdout, &stderr, time.Now)
	if code != exitUsage {
		t.Fatalf("exit code = %d, want exitUsage for an out-of-range volume", code)
	}
	if called {
		t.Error("the coordinator was contacted despite an out-of-range volume; want a local refusal before dispatch")
	}
	if !strings.Contains(stderr.String(), "range") {
		t.Errorf("stderr = %q, want it to explain the volume is out of range", stderr.String())
	}
}

// TestCmdFPPSetVolumeRejectsNonNumericLocally is this primitive's own
// version of capture section 1.5's OTHER finding: FPP coerces a
// non-numeric argument to zero silently rather than rejecting it. This
// command must refuse locally instead of silently sending something FPP
// would then silently mis-coerce.
func TestCmdFPPSetVolumeRejectsNonNumericLocally(t *testing.T) {
	called := false
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		called = true
	}))
	defer ts.Close()

	var stdout, stderr bytes.Buffer
	code := cmdFPP([]string{"set-volume", "--server", ts.URL, "bench-fpp", "abc"}, &stdout, &stderr, time.Now)
	if code != exitUsage {
		t.Fatalf("exit code = %d, want exitUsage for a non-numeric volume", code)
	}
	if called {
		t.Error("the coordinator was contacted despite a non-numeric volume; want a local refusal before dispatch")
	}
}

// TestCmdFPPSetVolumeUnconfirmedSurfacesReason follows the established
// convention: the server's own outcomeReason appears verbatim.
func TestCmdFPPSetVolumeUnconfirmedSurfacesReason(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.Header().Set("ShowMesh-API-Version", "1")
		_, _ = fmt.Fprint(w, `{"serverTime":"2026-08-13T22:00:00Z","command":{
			"id":"cmd-2","idempotencyKey":"k","action":"fpp.set_volume","instanceId":"bench-fpp",
			"replay":false,"outcome":"unconfirmed","outcomeState":"current",
			"outcomeReason":"observed fpp.volume = 40 (source fpp_poll), want 55",
			"attributionDegraded":false,"dispatchedAt":"2026-08-13T22:00:00Z","resolvedAt":"2026-08-13T22:00:20Z"}}`)
	}))
	defer ts.Close()

	var stdout, stderr bytes.Buffer
	code := cmdFPP([]string{"set-volume", "--server", ts.URL, "bench-fpp", "55"}, &stdout, &stderr, time.Now)
	if code != exitCommandUnconfirmed {
		t.Fatalf("exit code = %d, want exitCommandUnconfirmed", code)
	}
	if !strings.Contains(stdout.String(), "unconfirmed") {
		t.Errorf("stdout = %q, want it to report \"unconfirmed\"", stdout.String())
	}
	if !strings.Contains(stdout.String(), "want 55") {
		t.Errorf("stdout = %q, want the server's own outcomeReason surfaced verbatim", stdout.String())
	}
}

func TestCmdFPPSetVolumeRequiresVolumeArgument(t *testing.T) {
	var stdout, stderr bytes.Buffer
	code := cmdFPP([]string{"set-volume", "bench-fpp"}, &stdout, &stderr, time.Now)
	if code != exitUsage {
		t.Fatalf("exit code = %d, want exitUsage for a missing volume argument", code)
	}
}

// TestCmdFPPSetVolumeAcceptsBoundaryValues proves 0 and 100 (the
// documented inclusive range endpoints) are both accepted, not
// off-by-one excluded.
func TestCmdFPPSetVolumeAcceptsBoundaryValues(t *testing.T) {
	for _, v := range []string{"0", "100"} {
		t.Run(v, func(t *testing.T) {
			ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Content-Type", "application/json")
				w.Header().Set("ShowMesh-API-Version", "1")
				_, _ = fmt.Fprint(w, `{"serverTime":"2026-08-13T22:00:00Z","command":{
					"id":"cmd-b","idempotencyKey":"k","action":"fpp.set_volume","instanceId":"bench-fpp",
					"replay":false,"outcome":"confirmed","outcomeState":"current","outcomeReason":"",
					"attributionDegraded":false,"dispatchedAt":"2026-08-13T22:00:00Z","resolvedAt":"2026-08-13T22:00:01Z"}}`)
			}))
			defer ts.Close()

			var stdout, stderr bytes.Buffer
			code := cmdFPP([]string{"set-volume", "--server", ts.URL, "bench-fpp", v}, &stdout, &stderr, time.Now)
			if code != exitOK {
				t.Fatalf("exit code = %d, want exitOK for boundary volume %s; stderr=%s", code, v, stderr.String())
			}
		})
	}
}
