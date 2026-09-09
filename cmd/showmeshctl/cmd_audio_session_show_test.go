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

// This file is the showmeshctl test suite for "audio session show", the
// read-only sibling to cmd_audio_session.go's nine dispatch ops, over the
// same GET /api/v1/observations surface cmd_render_transport_test.go
// already exercises for resourceKind=surface.

func TestCmdAudioSessionShowWithSessionIDSendsResourceID(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet || r.URL.Path != "/api/v1/observations" {
			t.Errorf("request = %s %s, want GET /api/v1/observations", r.Method, r.URL.Path)
		}
		if got := r.URL.Query().Get("resourceKind"); got != "audio_session" {
			t.Errorf("resourceKind = %q, want audio_session", got)
		}
		if got := r.URL.Query().Get("resourceId"); got != "session-1" {
			t.Errorf("resourceId = %q, want session-1", got)
		}
		w.Header().Set("Content-Type", "application/json")
		w.Header().Set("ShowMesh-API-Version", "1")
		_, _ = fmt.Fprint(w, `{"serverTime":"2026-09-03T00:00:00Z","observations":[
			{"resource":{"kind":"audio_session","id":"session-1"},
			 "signal":"audio_session.desired_revision","value":3,"unit":null,"state":"current","reason":null,
			 "observedAt":"2026-09-03T00:00:00Z","collectedAt":"2026-09-03T00:00:00Z","source":"node-audio","quality":"measured","validForSeconds":45}
		]}`)
	}))
	defer ts.Close()

	var stdout, stderr bytes.Buffer
	code := cmdAudioSession([]string{"show", "--server", ts.URL, "--token", "t", "session-1"}, &stdout, &stderr, time.Now)
	if code != exitOK {
		t.Fatalf("exit code = %d, want exitOK (%d); stdout=%s stderr=%s", code, exitOK, stdout.String(), stderr.String())
	}
	if !strings.Contains(stdout.String(), "audio_session.desired_revision") {
		t.Errorf("stdout = %q, want it to contain the returned signal", stdout.String())
	}
}

// TestCmdAudioSessionShowWithNoSessionIDGroupsBySession proves the no-argument
// form sends no resourceId and groups the returned observations under each
// session's own header, mirroring printFPPTable's per-resource shape.
func TestCmdAudioSessionShowWithNoSessionIDGroupsBySession(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if got := r.URL.Query().Get("resourceKind"); got != "audio_session" {
			t.Errorf("resourceKind = %q, want audio_session", got)
		}
		if r.URL.Query().Has("resourceId") {
			t.Errorf("resourceId = %q, want it absent", r.URL.Query().Get("resourceId"))
		}
		w.Header().Set("Content-Type", "application/json")
		w.Header().Set("ShowMesh-API-Version", "1")
		_, _ = fmt.Fprint(w, `{"serverTime":"2026-09-03T00:00:00Z","observations":[
			{"resource":{"kind":"audio_session","id":"session-a"},
			 "signal":"audio_session.desired_revision","value":1,"unit":null,"state":"current","reason":null,
			 "observedAt":"2026-09-03T00:00:00Z","collectedAt":"2026-09-03T00:00:00Z","source":"node-audio","quality":"measured","validForSeconds":45},
			{"resource":{"kind":"audio_session","id":"session-b"},
			 "signal":"audio_session.desired_revision","value":2,"unit":null,"state":"current","reason":null,
			 "observedAt":"2026-09-03T00:00:00Z","collectedAt":"2026-09-03T00:00:00Z","source":"node-audio","quality":"measured","validForSeconds":45}
		]}`)
	}))
	defer ts.Close()

	var stdout, stderr bytes.Buffer
	code := cmdAudioSession([]string{"show", "--server", ts.URL, "--token", "t"}, &stdout, &stderr, time.Now)
	if code != exitOK {
		t.Fatalf("exit code = %d, want exitOK (%d); stdout=%s stderr=%s", code, exitOK, stdout.String(), stderr.String())
	}
	out := stdout.String()
	if !strings.Contains(out, "session-a") || !strings.Contains(out, "session-b") {
		t.Errorf("stdout = %q, want both session headers", out)
	}
	if strings.Index(out, "session-a") > strings.Index(out, "session-b") {
		t.Errorf("stdout = %q, want session-a before session-b", out)
	}
}

func TestCmdAudioSessionShowEmptyPrintsNoEvidenceLine(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.Header().Set("ShowMesh-API-Version", "1")
		_, _ = fmt.Fprint(w, `{"serverTime":"2026-09-03T00:00:00Z","observations":[]}`)
	}))
	defer ts.Close()

	var stdout, stderr bytes.Buffer
	code := cmdAudioSession([]string{"show", "--server", ts.URL, "--token", "t", "session-1"}, &stdout, &stderr, time.Now)
	if code != exitOK {
		t.Fatalf("exit code = %d, want exitOK (%d); stdout=%s stderr=%s", code, exitOK, stdout.String(), stderr.String())
	}
	if !strings.Contains(stdout.String(), "no audio session evidence") {
		t.Errorf("stdout = %q, want the empty-case line", stdout.String())
	}
}

func TestCmdAudioSessionShowAPIErrorExitsNonZero(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/problem+json")
		w.WriteHeader(http.StatusInternalServerError)
		_, _ = fmt.Fprint(w, `{"type":"about:blank","title":"Internal Server Error","status":500,"detail":"boom"}`)
	}))
	defer ts.Close()

	var stdout, stderr bytes.Buffer
	code := cmdAudioSession([]string{"show", "--server", ts.URL, "--token", "t", "session-1"}, &stdout, &stderr, time.Now)
	if code != exitAPIError {
		t.Fatalf("exit code = %d, want exitAPIError (%d); stdout=%s stderr=%s", code, exitAPIError, stdout.String(), stderr.String())
	}
}
