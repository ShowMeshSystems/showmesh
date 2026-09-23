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

const nightHeldSessionJSON = `{"id":"s1","configObjectId":"halloween-main","configRevision":1,"state":"live",
	"stateEnteredAt":"2026-09-23T20:00:00Z","cycle":3,"finalShowRequested":false,"finalShowRequestedAt":null,
	"admissionClosed":false,"admissionClosedAt":null,"shutdownIntent":"","armedShowId":"","showCommitted":true,
	"readiness":{"state":"unknown","reason":"","sameEpoch":false,"fresh":false,"checks":[]},
	"powerPhase":{"state":"unknown","reason":""},"transition":{"state":"not_available","reason":""},
	"stopHold":{"reason":"The show was stopped with Stop.","at":"2026-09-23T20:05:00Z","principal":"bench"},
	"degraded":false,"updatedAt":"2026-09-23T20:05:00Z"}`

func TestCmdNightResumeShowPostsTheCommand(t *testing.T) {
	var gotMethod, gotPath string
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotMethod, gotPath = r.Method, r.URL.Path
		w.Header().Set("Content-Type", "application/json")
		w.Header().Set("ShowMesh-API-Version", "1")
		w.WriteHeader(http.StatusAccepted)
		_, _ = fmt.Fprint(w, `{"serverTime":"2026-09-23T20:06:00Z","command":{"command":"resume-show","outcome":"applied"},
			"session":{"id":"s1","configObjectId":"halloween-main","configRevision":1,"state":"transition-to-show",
			"stateEnteredAt":"2026-09-23T20:06:00Z","cycle":4,"finalShowRequested":false,"finalShowRequestedAt":null,
			"admissionClosed":false,"admissionClosedAt":null,"shutdownIntent":"","armedShowId":"a1","showCommitted":false,
			"readiness":{"state":"unknown","reason":"","sameEpoch":false,"fresh":false,"checks":[]},
			"powerPhase":{"state":"unknown","reason":""},"transition":{"state":"not_available","reason":""},
			"degraded":false,"updatedAt":"2026-09-23T20:06:00Z"}}`)
	}))
	defer ts.Close()

	var stdout, stderr bytes.Buffer
	code := cmdNight([]string{"resume-show", "--server", ts.URL, "--token", "smsh_test"}, &stdout, &stderr, time.Now)
	if code != exitOK {
		t.Fatalf("exit code = %d, want exitOK; stdout=%s stderr=%s", code, stdout.String(), stderr.String())
	}
	if gotMethod != http.MethodPost || gotPath != "/api/v1/night/commands/resume-show" {
		t.Errorf("request = %s %s, want POST /api/v1/night/commands/resume-show", gotMethod, gotPath)
	}
	if !strings.Contains(stdout.String(), "applied") {
		t.Errorf("stdout = %q, want the outcome", stdout.String())
	}
}

func TestCmdNightResumeShowRefusedWithoutAHold(t *testing.T) {
	ts := nightProblemServer(http.StatusConflict, problemNightStateRejected)
	defer ts.Close()
	var stdout, stderr bytes.Buffer
	code := cmdNight([]string{"resume-show", "--server", ts.URL, "--token", "smsh_test"}, &stdout, &stderr, time.Now)
	if code != exitNightStateRejected {
		t.Fatalf("exit code = %d, want exitNightStateRejected; stderr=%s", code, stderr.String())
	}
}

func TestCmdNightStatusPrintsTheStopHold(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.Header().Set("ShowMesh-API-Version", "1")
		_, _ = fmt.Fprint(w, `{"serverTime":"2026-09-23T20:06:00Z","session":`+nightHeldSessionJSON+`}`)
	}))
	defer ts.Close()

	var stdout, stderr bytes.Buffer
	if code := cmdNight([]string{"status", "--server", ts.URL}, &stdout, &stderr, time.Now); code != exitOK {
		t.Fatalf("exit code = %d; stderr=%s", code, stderr.String())
	}
	out := stdout.String()
	for _, want := range []string{"STOPPED:", "The show was stopped with Stop.", "2026-09-23T20:05:00Z", "by bench", "night resume-show"} {
		if !strings.Contains(out, want) {
			t.Errorf("status does not contain %q; stdout=%s", want, out)
		}
	}
}
