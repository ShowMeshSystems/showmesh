package main

import (
	"bytes"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// This file tests "fpp acknowledge-instance-uuid-change",
// following cmd_fpp_reset_observation_sequence_test.go's --confirm
// pattern: refusal without --confirm must never reach the coordinator,
// and a confirmed call must pin the POST method and path.

func TestCmdFPPAcknowledgeInstanceUUIDChangeRefusesWithoutConfirmFlag(t *testing.T) {
	called := false
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		called = true
		w.WriteHeader(http.StatusOK)
	}))
	defer ts.Close()

	var stdout, stderr bytes.Buffer
	code := cmdFPP([]string{"acknowledge-instance-uuid-change", "--server", ts.URL, "front-yard"}, &stdout, &stderr, fixedClock(mustParse(t, "2026-08-16T21:00:00Z")))
	if code != exitUsage {
		t.Fatalf("exit code = %d, want exitUsage; stderr=%s", code, stderr.String())
	}
	if called {
		t.Errorf("the coordinator was called without --confirm ever being passed")
	}
	if !strings.Contains(stderr.String(), "--confirm") {
		t.Errorf("stderr = %q, want it to name --confirm", stderr.String())
	}
}

func TestCmdFPPAcknowledgeInstanceUUIDChangeWithConfirmSucceeds(t *testing.T) {
	var gotMethod, gotPath string
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotMethod, gotPath = r.Method, r.URL.Path
		w.Header().Set("ShowMesh-API-Version", "1")
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"serverTime":"2026-08-16T21:00:00Z","instance":{"instanceId":"front-yard",` +
			`"endpoint":"http://10.0.1.20","health":"unknown","observations":[],"lastPollAt":null,` +
			`"lastPollError":null,"instanceUuid":"uuid-b","instanceUuidFirstObservedAt":"2026-08-16T20:00:00Z",` +
			`"instanceUuidChange":null,"duplicateInstanceUuidEndpointIds":[]}}`))
	}))
	defer ts.Close()

	var stdout, stderr bytes.Buffer
	code := cmdFPP([]string{"acknowledge-instance-uuid-change", "--server", ts.URL, "--confirm", "front-yard"}, &stdout, &stderr, fixedClock(mustParse(t, "2026-08-16T21:00:00Z")))
	if code != exitOK {
		t.Fatalf("exit code = %d, want exitOK; stderr=%s", code, stderr.String())
	}
	if gotMethod != http.MethodPost {
		t.Errorf("method = %q, want POST", gotMethod)
	}
	wantPath := "/api/v1/fpp/front-yard/instance-uuid/acknowledge"
	if gotPath != wantPath {
		t.Errorf("path = %q, want %q", gotPath, wantPath)
	}
	if !strings.Contains(stdout.String(), "front-yard") || !strings.Contains(stdout.String(), "uuid-b") {
		t.Errorf("output missing confirmation:\n%s", stdout.String())
	}
}

func TestCmdFPPAcknowledgeInstanceUUIDChangeWithConfirmSucceedsInstanceRemoved(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("ShowMesh-API-Version", "1")
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"serverTime":"2026-08-16T21:00:00Z","instance":null}`))
	}))
	defer ts.Close()

	var stdout, stderr bytes.Buffer
	code := cmdFPP([]string{"acknowledge-instance-uuid-change", "--server", ts.URL, "--confirm", "front-yard"}, &stdout, &stderr, fixedClock(mustParse(t, "2026-08-16T21:00:00Z")))
	if code != exitOK {
		t.Fatalf("exit code = %d, want exitOK; stderr=%s", code, stderr.String())
	}
	if !strings.Contains(stdout.String(), "front-yard") {
		t.Errorf("output missing instance id:\n%s", stdout.String())
	}
	if !strings.Contains(stdout.String(), "acknowledged") {
		t.Errorf("output missing acknowledgment confirmation:\n%s", stdout.String())
	}
	if !strings.Contains(stdout.String(), "no longer") || !strings.Contains(stdout.String(), "configured") {
		t.Errorf("output missing explanation that the instance is no longer configured:\n%s", stdout.String())
	}
}

func TestCmdFPPAcknowledgeInstanceUUIDChangeRequiresExactlyOneArg(t *testing.T) {
	var stdout, stderr bytes.Buffer
	code := cmdFPP([]string{"acknowledge-instance-uuid-change", "--server", "http://example.invalid", "--confirm"}, &stdout, &stderr, fixedClock(mustParse(t, "2026-08-16T21:00:00Z")))
	if code != exitUsage {
		t.Fatalf("exit code = %d, want exitUsage for a missing instance-id argument", code)
	}
}

func TestCmdFPPAcknowledgeInstanceUUIDChangeServerConflict(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/problem+json")
		w.Header().Set("ShowMesh-API-Version", "1")
		w.WriteHeader(http.StatusConflict)
		_, _ = w.Write([]byte(`{"type":"https://showmesh.dev/problems/conflict","title":"No pending FPP instance uuid change to acknowledge",
			"status":409,"detail":"nothing pending","serverTime":"2026-08-16T21:00:00Z"}`))
	}))
	defer ts.Close()

	var stdout, stderr bytes.Buffer
	code := cmdFPP([]string{"acknowledge-instance-uuid-change", "--server", ts.URL, "--confirm", "front-yard"}, &stdout, &stderr, fixedClock(mustParse(t, "2026-08-16T21:00:00Z")))
	if code == exitOK {
		t.Fatalf("exit code = %d, want a non-zero exit for a 409", code)
	}
}
