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

// This file is the showmeshctl test suite for "render transport
// <surface-id>" (Track B seam B4), over GET /api/v1/observations.

func renderTransportObservationsHandler(t *testing.T, body string) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet || r.URL.Path != "/api/v1/observations" {
			t.Errorf("request = %s %s, want GET /api/v1/observations", r.Method, r.URL.Path)
		}
		if got := r.URL.Query().Get("resourceKind"); got != "surface" {
			t.Errorf("resourceKind = %q, want surface", got)
		}
		if got := r.URL.Query().Get("resourceId"); got != "garage-window" {
			t.Errorf("resourceId = %q, want garage-window", got)
		}
		w.Header().Set("Content-Type", "application/json")
		w.Header().Set("ShowMesh-API-Version", "1")
		_, _ = fmt.Fprint(w, body)
	}
}

func TestCmdRenderTransportAvailableExitsOK(t *testing.T) {
	body := `{"serverTime":"2026-08-17T00:00:00Z","observations":[
		{"signal":"surface.transport.available","value":true,"unit":null,"state":"current","reason":null,
		 "observedAt":"2026-08-17T00:00:00Z","collectedAt":"2026-08-17T00:00:00Z","source":"node-render","quality":"measured","validForSeconds":45},
		{"signal":"surface.transport.reason","value":"","unit":null,"state":"current","reason":null,
		 "observedAt":"2026-08-17T00:00:00Z","collectedAt":"2026-08-17T00:00:00Z","source":"node-render","quality":"measured","validForSeconds":45}
	]}`
	ts := httptest.NewServer(renderTransportObservationsHandler(t, body))
	defer ts.Close()

	var stdout, stderr bytes.Buffer
	code := cmdRender([]string{"transport", "--server", ts.URL, "--token", "t", "garage-window"}, &stdout, &stderr, time.Now)
	if code != exitOK {
		t.Fatalf("exit code = %d, want exitOK (%d); stdout=%s stderr=%s", code, exitOK, stdout.String(), stderr.String())
	}
	if !strings.Contains(stdout.String(), "available") {
		t.Errorf("stdout = %q, want it to say available", stdout.String())
	}
}

// TestCmdRenderTransportUnavailableExits22 proves the seam's own named exit
// code: a confirmed-unavailable probe exits exitRenderUnavailable (22) and
// prints the real reason, not a generic failure message.
func TestCmdRenderTransportUnavailableExits22(t *testing.T) {
	body := `{"serverTime":"2026-08-17T00:00:00Z","observations":[
		{"signal":"surface.transport.available","value":false,"unit":null,"state":"current","reason":null,
		 "observedAt":"2026-08-17T00:00:00Z","collectedAt":"2026-08-17T00:00:00Z","source":"node-render","quality":"measured","validForSeconds":45},
		{"signal":"surface.transport.reason","value":"NDI runtime not found: install the NDI SDK runtime library","unit":null,"state":"current","reason":null,
		 "observedAt":"2026-08-17T00:00:00Z","collectedAt":"2026-08-17T00:00:00Z","source":"node-render","quality":"measured","validForSeconds":45}
	]}`
	ts := httptest.NewServer(renderTransportObservationsHandler(t, body))
	defer ts.Close()

	var stdout, stderr bytes.Buffer
	code := cmdRender([]string{"transport", "--server", ts.URL, "--token", "t", "garage-window"}, &stdout, &stderr, time.Now)
	if code != exitRenderUnavailable {
		t.Fatalf("exit code = %d, want exitRenderUnavailable (%d); stdout=%s stderr=%s", code, exitRenderUnavailable, stdout.String(), stderr.String())
	}
	if !strings.Contains(stdout.String(), "UNAVAILABLE") || !strings.Contains(stdout.String(), "NDI runtime not found") {
		t.Errorf("stdout = %q, want UNAVAILABLE and the real reason", stdout.String())
	}
}

// TestCmdRenderTransportNeverProbedExits22 proves the never-probed case
// (no surface.transport.available observation at all — e.g. a surface
// that has never been applied with an ndi output) is treated the same as
// confirmed-unavailable for exit-code purposes: both mean "cannot show the
// operator NDI is usable right now."
func TestCmdRenderTransportNeverProbedExits22(t *testing.T) {
	body := `{"serverTime":"2026-08-17T00:00:00Z","observations":[]}`
	ts := httptest.NewServer(renderTransportObservationsHandler(t, body))
	defer ts.Close()

	var stdout, stderr bytes.Buffer
	code := cmdRender([]string{"transport", "--server", ts.URL, "--token", "t", "garage-window"}, &stdout, &stderr, time.Now)
	if code != exitRenderUnavailable {
		t.Fatalf("exit code = %d, want exitRenderUnavailable (%d); stdout=%s stderr=%s", code, exitRenderUnavailable, stdout.String(), stderr.String())
	}
	if !strings.Contains(stdout.String(), "not probed") {
		t.Errorf("stdout = %q, want it to say the transport was never probed", stdout.String())
	}
}

// TestCmdRenderTransportNotCollectedExits22 proves the not_collected
// observation state (an agent that has probed nothing yet, but IS being
// reported on) is also treated as unavailable, using the observation's own
// stated reason rather than this command's generic fallback text.
func TestCmdRenderTransportNotCollectedExits22(t *testing.T) {
	body := `{"serverTime":"2026-08-17T00:00:00Z","observations":[
		{"signal":"surface.transport.available","value":null,"unit":null,"state":"not_collected",
		 "reason":"transport availability has not been probed for this surface",
		 "observedAt":null,"collectedAt":"2026-08-17T00:00:00Z","source":"node-render","quality":"measured","validForSeconds":null}
	]}`
	ts := httptest.NewServer(renderTransportObservationsHandler(t, body))
	defer ts.Close()

	var stdout, stderr bytes.Buffer
	code := cmdRender([]string{"transport", "--server", ts.URL, "--token", "t", "garage-window"}, &stdout, &stderr, time.Now)
	if code != exitRenderUnavailable {
		t.Fatalf("exit code = %d, want exitRenderUnavailable (%d); stdout=%s stderr=%s", code, exitRenderUnavailable, stdout.String(), stderr.String())
	}
	if !strings.Contains(stdout.String(), "has not been probed for this surface") {
		t.Errorf("stdout = %q, want the observation's own stated reason", stdout.String())
	}
}
