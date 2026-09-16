package main

import (
	"bytes"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

// This file is showmeshctl's own test suite for "node-clock" (Track I
// seam I1), covering only --phc-device: every other flag already has no
// dedicated coverage of its own here, and this is not the seam to add it.

// TestCmdNodeClockSetSendsPHCDeviceFlag proves --phc-device reaches the
// PUT body and the printed detail, mirroring cmd_audio_test.go's own
// TestCmdAudioNodeSetSendsAllFlags for the identical "set is a full
// replacement, flags become PUT body fields" shape.
func TestCmdNodeClockSetSendsPHCDeviceFlag(t *testing.T) {
	var gotMethod, gotPath string
	var gotBody []byte
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotMethod, gotPath = r.Method, r.URL.Path
		gotBody, _ = io.ReadAll(r.Body)
		w.Header().Set("Content-Type", "application/json")
		w.Header().Set("ShowMesh-API-Version", "1")
		_, _ = fmt.Fprint(w, `{"serverTime":"2026-09-16T00:00:00Z","kind":"node.clock","id":"node-01","revision":1,
			"payload":{"provider":"external","interface":"eno2","domain":0,"externalUdsAddress":"/var/run/ptp/ptp4lro","phcDevice":"/dev/ptp0"},
			"updatedAt":"2026-09-16T00:00:00Z","createdByPrincipalId":"p1","createdByPrincipalName":"admin","source":"api"}`)
	}))
	defer ts.Close()

	var stdout, stderr bytes.Buffer
	code := cmdNodeClock([]string{
		"set",
		"--provider", "external", "--interface", "eno2", "--domain", "0",
		"--external-uds-address", "/var/run/ptp/ptp4lro", "--phc-device", "/dev/ptp0",
		"--server", ts.URL, "--token", "t",
		"node-01",
	}, &stdout, &stderr, time.Now)
	if code != exitOK {
		t.Fatalf("exit code = %d, want exitOK; stderr=%s", code, stderr.String())
	}
	if gotMethod != http.MethodPut || gotPath != "/api/v1/config/node.clock/node-01" {
		t.Fatalf("request = %s %s, want PUT /api/v1/config/node.clock/node-01", gotMethod, gotPath)
	}
	if !strings.Contains(string(gotBody), `"phcDevice":"/dev/ptp0"`) {
		t.Errorf("PUT body missing phcDevice; body: %s", gotBody)
	}
	if !strings.Contains(stdout.String(), "PHC device:              /dev/ptp0") {
		t.Errorf("printed detail missing phcDevice:\n%s", stdout.String())
	}
}

// TestCmdNodeClockGetPrintsPHCDevice proves the read side of the same
// round trip: a GET response naming phcDevice prints it in the detail
// view.
func TestCmdNodeClockGetPrintsPHCDevice(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.Header().Set("ShowMesh-API-Version", "1")
		_, _ = fmt.Fprint(w, `{"serverTime":"2026-09-16T00:00:00Z","kind":"node.clock","id":"node-01","revision":2,
			"payload":{"provider":"external","interface":"eno2","domain":0,"phcDevice":"/dev/ptp0"},
			"updatedAt":"2026-09-16T00:00:00Z","createdByPrincipalId":"p1","createdByPrincipalName":"admin","source":"api"}`)
	}))
	defer ts.Close()

	var stdout, stderr bytes.Buffer
	code := cmdNodeClock([]string{"get", "--server", ts.URL, "--token", "t", "node-01"}, &stdout, &stderr, time.Now)
	if code != exitOK {
		t.Fatalf("exit code = %d, want exitOK; stderr=%s", code, stderr.String())
	}
	if !strings.Contains(stdout.String(), "PHC device:              /dev/ptp0") {
		t.Errorf("printed detail missing phcDevice:\n%s", stdout.String())
	}
}

// TestCmdNodeClockGetOmitsPHCDeviceLineForManaged proves the field only
// prints for the provider it applies to, matching printNodeClockDetail's
// own external-only gate.
func TestCmdNodeClockGetOmitsPHCDeviceLineForManaged(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.Header().Set("ShowMesh-API-Version", "1")
		_, _ = fmt.Fprint(w, `{"serverTime":"2026-09-16T00:00:00Z","kind":"node.clock","id":"node-01","revision":1,
			"payload":{"provider":"managed","interface":"eno2","domain":0},
			"updatedAt":"2026-09-16T00:00:00Z","createdByPrincipalId":"p1","createdByPrincipalName":"admin","source":"api"}`)
	}))
	defer ts.Close()

	var stdout, stderr bytes.Buffer
	code := cmdNodeClock([]string{"get", "--server", ts.URL, "--token", "t", "node-01"}, &stdout, &stderr, time.Now)
	if code != exitOK {
		t.Fatalf("exit code = %d, want exitOK; stderr=%s", code, stderr.String())
	}
	if strings.Contains(stdout.String(), "PHC device") {
		t.Errorf("printed detail names PHC device for a managed node, want it omitted:\n%s", stdout.String())
	}
}
