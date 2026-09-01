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

// TestCmdNightPrepareSiteAppliedExitsOK proves the write shape (POST the
// exact path, decode the 202 body) and that a successful command exits 0.
func TestCmdNightPrepareSiteAppliedExitsOK(t *testing.T) {
	var gotMethod, gotPath string
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotMethod, gotPath = r.Method, r.URL.Path
		w.Header().Set("Content-Type", "application/json")
		w.Header().Set("ShowMesh-API-Version", "1")
		w.WriteHeader(http.StatusAccepted)
		_, _ = fmt.Fprint(w, `{"serverTime":"2026-08-18T22:00:00Z","command":{"command":"prepare-site","outcome":"applied"},
			"session":{"id":"s1","configObjectId":"halloween-main","configRevision":1,"state":"preparing",
			"stateEnteredAt":"2026-08-18T22:00:00Z","cycle":0,"finalShowRequested":false,"finalShowRequestedAt":null,
			"admissionClosed":false,"admissionClosedAt":null,"shutdownIntent":"","armedShowId":"","showCommitted":false,
			"readiness":{"state":"unknown","reason":"no readiness result recorded","sameEpoch":false,"fresh":false,"checks":[]},
			"powerPhase":{"state":"unknown","reason":""},"transition":{"state":"not_available","reason":""},
			"degraded":false,"updatedAt":"2026-08-18T22:00:00Z"}}`)
	}))
	defer ts.Close()

	var stdout, stderr bytes.Buffer
	code := cmdNight([]string{"prepare-site", "--server", ts.URL, "--token", "smsh_test"}, &stdout, &stderr, time.Now)
	if code != exitOK {
		t.Fatalf("exit code = %d, want exitOK; stdout=%s stderr=%s", code, stdout.String(), stderr.String())
	}
	if gotMethod != http.MethodPost {
		t.Errorf("method = %q, want POST", gotMethod)
	}
	if gotPath != "/api/v1/night/commands/prepare-site" {
		t.Errorf("path = %q, want /api/v1/night/commands/prepare-site", gotPath)
	}
	if !strings.Contains(stdout.String(), "applied") {
		t.Errorf("stdout = %q, want it to report the outcome", stdout.String())
	}
}

// TestCmdNightStatusPrintsCueDetail: "night status" must answer "why has
// the show not started" without the operator reading SQLite, so the
// outbox's per-cue state, outcome, reason, and timestamps must reach
// stdout.
func TestCmdNightStatusPrintsCueDetail(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.Header().Set("ShowMesh-API-Version", "1")
		w.WriteHeader(http.StatusOK)
		_, _ = fmt.Fprint(w, `{"serverTime":"2026-08-18T22:00:00Z",
			"session":{"id":"s1","configObjectId":"halloween-main","configRevision":1,"state":"transition-to-show",
			"stateEnteredAt":"2026-08-18T22:00:00Z","cycle":0,"finalShowRequested":false,"finalShowRequestedAt":null,
			"admissionClosed":false,"admissionClosedAt":null,"shutdownIntent":"","armedShowId":"","showCommitted":true,
			"readiness":{"state":"unknown","reason":"no readiness result recorded","sameEpoch":false,"fresh":false,"checks":[]},
			"powerPhase":{"state":"unknown","reason":""},
			"transition":{"state":"recorded","reason":"barrier cue \"lighting-fade\" is dispatched, not resolved"},
			"cues":{"state":"recorded","reason":"","cues":[{"name":"lighting-fade","phase":"enterShow","role":"lighting","action":"lighting-fade-out",
			"actionRevision":1,"state":"resolved","outcome":"unconfirmed","reason":"no confirming evidence arrived",
			"dispatchedAt":"2026-08-18T22:00:00Z","resolvedAt":"2026-08-18T22:00:01Z"}]},
			"degraded":false,"updatedAt":"2026-08-18T22:00:00Z"}}`)
	}))
	defer ts.Close()

	var stdout, stderr bytes.Buffer
	code := cmdNight([]string{"status", "--server", ts.URL}, &stdout, &stderr, time.Now)
	if code != exitOK {
		t.Fatalf("exit code = %d, want exitOK; stderr=%s", code, stderr.String())
	}
	out := stdout.String()
	for _, want := range []string{
		"lighting-fade", "unconfirmed", "no confirming evidence arrived",
		`barrier cue "lighting-fade" is dispatched, not resolved`,
		"2026-08-18T22:00:00Z", "2026-08-18T22:00:01Z",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("stdout does not contain %q; stdout=%s", want, out)
		}
	}
	if strings.Contains(out, "\"failed\"") || strings.Contains(out, " outcome=failed") {
		t.Errorf("an unconfirmed cue must never render as failed; stdout=%s", out)
	}
}

// TestCmdNightStatusPrintsUnreadableCuesReason: when the coordinator
// cannot read the cue outbox, the CLI must say so rather than rendering
// an empty, silent "no cues" page.
func TestCmdNightStatusPrintsUnreadableCuesReason(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.Header().Set("ShowMesh-API-Version", "1")
		w.WriteHeader(http.StatusOK)
		_, _ = fmt.Fprint(w, `{"serverTime":"2026-08-18T22:00:00Z",
			"session":{"id":"s1","configObjectId":"halloween-main","configRevision":1,"state":"transition-to-show",
			"stateEnteredAt":"2026-08-18T22:00:00Z","cycle":0,"finalShowRequested":false,"finalShowRequestedAt":null,
			"admissionClosed":false,"admissionClosedAt":null,"shutdownIntent":"","armedShowId":"","showCommitted":true,
			"readiness":{"state":"unknown","reason":"no readiness result recorded","sameEpoch":false,"fresh":false,"checks":[]},
			"powerPhase":{"state":"unknown","reason":""},
			"transition":{"state":"unknown","reason":""},
			"cues":{"state":"unknown","reason":"failed to read the cue outbox: simulated I/O error","cues":[]},
			"degraded":false,"updatedAt":"2026-08-18T22:00:00Z"}}`)
	}))
	defer ts.Close()

	var stdout, stderr bytes.Buffer
	code := cmdNight([]string{"status", "--server", ts.URL}, &stdout, &stderr, time.Now)
	if code != exitOK {
		t.Fatalf("exit code = %d, want exitOK; stderr=%s", code, stderr.String())
	}
	out := stdout.String()
	if !strings.Contains(out, "failed to read the cue outbox: simulated I/O error") {
		t.Errorf("stdout does not surface the unreadable-cues reason; stdout=%s", out)
	}
}

func nightProblemServer(status int, problemType string) *httptest.Server {
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/problem+json")
		w.Header().Set("ShowMesh-API-Version", "1")
		w.WriteHeader(status)
		_, _ = fmt.Fprintf(w, `{"type":%q,"title":"refused","status":%d,"detail":"refused","serverTime":"2026-08-18T22:00:00Z"}`, problemType, status)
	}))
}

// TestCmdNightStartNotReadyExitsExitNightNotReady proves the
// night-not-ready problem type maps to exit code 26.
func TestCmdNightStartNotReadyExitsExitNightNotReady(t *testing.T) {
	ts := nightProblemServer(http.StatusConflict, problemNightNotReady)
	defer ts.Close()

	var stdout, stderr bytes.Buffer
	code := cmdNight([]string{"start", "--server", ts.URL, "--token", "smsh_test"}, &stdout, &stderr, time.Now)
	if code != exitNightNotReady {
		t.Fatalf("exit code = %d, want exitNightNotReady (26); stderr=%s", code, stderr.String())
	}
}

// TestCmdNightStartStateRejectedExitsExitNightStateRejected proves the
// night-state-rejected problem type maps to exit code 27, distinctly from
// exitNightNotReady.
func TestCmdNightStartStateRejectedExitsExitNightStateRejected(t *testing.T) {
	ts := nightProblemServer(http.StatusConflict, problemNightStateRejected)
	defer ts.Close()

	var stdout, stderr bytes.Buffer
	code := cmdNight([]string{"start", "--server", ts.URL, "--token", "smsh_test"}, &stdout, &stderr, time.Now)
	if code != exitNightStateRejected {
		t.Fatalf("exit code = %d, want exitNightStateRejected (27); stderr=%s", code, stderr.String())
	}
}

// TestCmdNightPreshowAmbiguousExitsExitNightAmbiguous proves the
// night-ambiguous problem type maps to exit code 28, distinctly from the
// other two.
func TestCmdNightPreshowAmbiguousExitsExitNightAmbiguous(t *testing.T) {
	ts := nightProblemServer(http.StatusConflict, problemNightAmbiguous)
	defer ts.Close()

	var stdout, stderr bytes.Buffer
	code := cmdNight([]string{"preshow", "--server", ts.URL, "--token", "smsh_test"}, &stdout, &stderr, time.Now)
	if code != exitNightAmbiguous {
		t.Fatalf("exit code = %d, want exitNightAmbiguous (28); stderr=%s", code, stderr.String())
	}
}

// TestCmdNightStatusIsAnOpenRead proves "night status" issues a GET (never
// a write) and succeeds without a --token.
func TestCmdNightStatusIsAnOpenRead(t *testing.T) {
	var gotMethod string
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotMethod = r.Method
		w.Header().Set("Content-Type", "application/json")
		w.Header().Set("ShowMesh-API-Version", "1")
		_, _ = fmt.Fprint(w, `{"serverTime":"2026-08-18T22:00:00Z","session":{"id":"","configObjectId":"","configRevision":0,
			"state":"inactive","stateEnteredAt":"2026-08-18T22:00:00Z","cycle":0,"finalShowRequested":false,
			"finalShowRequestedAt":null,"admissionClosed":false,"admissionClosedAt":null,"shutdownIntent":"",
			"armedShowId":"","showCommitted":false,"readiness":{"state":"unknown","reason":"no session","sameEpoch":false,
			"fresh":false,"checks":[]},"powerPhase":{"state":"unknown","reason":""},
			"transition":{"state":"not_available","reason":""},"degraded":false,"updatedAt":"2026-08-18T22:00:00Z"}}`)
	}))
	defer ts.Close()

	var stdout, stderr bytes.Buffer
	code := cmdNight([]string{"status", "--server", ts.URL}, &stdout, &stderr, time.Now)
	if code != exitOK {
		t.Fatalf("exit code = %d, want exitOK; stderr=%s", code, stderr.String())
	}
	if gotMethod != http.MethodGet {
		t.Errorf("method = %q, want GET", gotMethod)
	}
	if !strings.Contains(stdout.String(), "inactive") {
		t.Errorf("stdout = %q, want it to report state=inactive", stdout.String())
	}
}

// TestCmdNightEndSessionSendsExpectedPath proves "night end-session" POSTs
// the expected path: the one command reachable against a degraded
// session (finding 1).
func TestCmdNightEndSessionSendsExpectedPath(t *testing.T) {
	var gotPath string
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		w.Header().Set("Content-Type", "application/json")
		w.Header().Set("ShowMesh-API-Version", "1")
		w.WriteHeader(http.StatusAccepted)
		_, _ = fmt.Fprint(w, `{"serverTime":"2026-08-18T22:00:00Z","command":{"command":"end-session","outcome":"applied","attributionDegraded":false},
			"session":{"id":"s1","configObjectId":"halloween-main","configRevision":1,"state":"stopped",
			"stateEnteredAt":"2026-08-18T22:00:00Z","cycle":0,"finalShowRequested":false,"finalShowRequestedAt":null,
			"admissionClosed":true,"admissionClosedAt":"2026-08-18T22:00:00Z","shutdownIntent":"","armedShowId":"","showCommitted":false,
			"readiness":{"state":"unknown","reason":"no readiness result recorded","sameEpoch":false,"fresh":false,"checks":[]},
			"powerPhase":{"state":"unknown","reason":""},"transition":{"state":"not_available","reason":""},
			"degraded":true,"degradedReason":"ambiguous restart","attributionDegraded":false,"updatedAt":"2026-08-18T22:00:00Z"}}`)
	}))
	defer ts.Close()

	var stdout, stderr bytes.Buffer
	code := cmdNight([]string{"end-session", "--server", ts.URL, "--token", "smsh_test"}, &stdout, &stderr, time.Now)
	if code != exitOK {
		t.Fatalf("exit code = %d, want exitOK; stdout=%s stderr=%s", code, stdout.String(), stderr.String())
	}
	if gotPath != "/api/v1/night/commands/end-session" {
		t.Errorf("path = %q, want /api/v1/night/commands/end-session", gotPath)
	}
	if !strings.Contains(stdout.String(), "DEGRADED") {
		t.Errorf("stdout = %q, want it to report the still-degraded flag", stdout.String())
	}
}

// TestCmdNightStatusPrintsBackgroundAudioDetail proves Track F seam F5's
// background-audio/announcement steps reach the operator CLI surface
// (RESTING-MODE.md section 14), not only a coordinator log line.
func TestCmdNightStatusPrintsBackgroundAudioDetail(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.Header().Set("ShowMesh-API-Version", "1")
		w.WriteHeader(http.StatusOK)
		_, _ = fmt.Fprint(w, `{"serverTime":"2026-08-18T22:00:00Z",
			"session":{"id":"s1","configObjectId":"halloween-main","configRevision":1,"state":"resting-intershow",
			"stateEnteredAt":"2026-08-18T22:00:00Z","cycle":1,"finalShowRequested":false,"finalShowRequestedAt":null,
			"admissionClosed":false,"admissionClosedAt":null,"shutdownIntent":"","armedShowId":"","showCommitted":false,
			"readiness":{"state":"unknown","reason":"no readiness result recorded","sameEpoch":false,"fresh":false,"checks":[]},
			"powerPhase":{"state":"unknown","reason":""},
			"transition":{"state":"unknown","reason":""},
			"cues":{"state":"recorded","reason":"","cues":[]},
			"backgroundAudio":{"state":"recorded","reason":"","steps":[
				{"sequence":"background","phase":"restingBackground:node-a","cueName":"bg-0002-gain","nodeId":"node-a","kind":"gain","actionRevision":2,
				 "state":"resolved","outcome":"unconfirmable","reason":"no confirmation evidence was reported",
				 "dispatchedAt":"2026-08-18T22:00:00Z","resolvedAt":"2026-08-18T22:00:01Z"},
				{"sequence":"announcement","phase":"announcementSession:clear:enterResting:node-b","cueName":"thank-you",
				 "nodeId":"node-b","kind":"announcementClear","actionRevision":4,
				 "state":"resolved","outcome":"refused","reason":"stale_revision",
				 "dispatchedAt":"2026-08-18T22:00:02Z","resolvedAt":"2026-08-18T22:00:03Z"}
			]},
			"degraded":false,"updatedAt":"2026-08-18T22:00:00Z"}}`)
	}))
	defer ts.Close()

	var stdout, stderr bytes.Buffer
	code := cmdNight([]string{"status", "--server", ts.URL}, &stdout, &stderr, time.Now)
	if code != exitOK {
		t.Fatalf("exit code = %d, want exitOK; stderr=%s", code, stderr.String())
	}
	out := stdout.String()
	for _, want := range []string{
		"Background audio", "bg-0002-gain", "gain", "unconfirmable", "no confirmation evidence was reported",
		// The announcement sequence prints under its own heading. A
		// refused clear means a previous announcement may still be
		// playing and still holding the bed ducked, which must be
		// readable here and not only in a coordinator log line.
		"Announcement sessions", "thank-you", "announcementClear", "refused", "stale_revision",
		// CLI parity with the API/UI - each step's own node is rendered,
		// so a refused step is answerable by node from this output alone.
		"node=node-a", "node=node-b",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("stdout does not contain %q; stdout=%s", want, out)
		}
	}
	// And under the right heading: an announcement failure attributed to
	// background audio is worse than one that is merely hard to find.
	bgAt := strings.Index(out, "Background audio:")
	annAt := strings.Index(out, "Announcement sessions:")
	if bgAt < 0 || annAt < 0 || annAt < bgAt {
		t.Fatalf("headings out of order or missing (background at %d, announcement at %d); stdout=%s", bgAt, annAt, out)
	}
	if strings.Contains(out[bgAt:annAt], "announcementClear") {
		t.Errorf("the announcement step printed under the background-audio heading; stdout=%s", out)
	}
	if strings.Contains(out[annAt:], "bg-0002-gain") {
		t.Errorf("a background step printed under the announcement heading; stdout=%s", out)
	}
}

// TestCmdNightStatusPrintsPinnedMaxGainConfiguredButNotStarted proves
// finding 4's reconciliation directly: a non-nil pinnedMaxGainDb is proof
// the pinned revision DOES configure background audio, so the "not
// configured, or never started this cycle" header (which reads as
// possibly-not-configured) must not print alongside it - only "never
// started this cycle" is still a possible reading once a pinned ceiling
// is on the screen.
func TestCmdNightStatusPrintsPinnedMaxGainConfiguredButNotStarted(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.Header().Set("ShowMesh-API-Version", "1")
		w.WriteHeader(http.StatusOK)
		_, _ = fmt.Fprint(w, `{"serverTime":"2026-08-18T22:00:00Z",
			"session":{"id":"s1","configObjectId":"halloween-main","configRevision":1,"state":"resting-intershow",
			"stateEnteredAt":"2026-08-18T22:00:00Z","cycle":1,"finalShowRequested":false,"finalShowRequestedAt":null,
			"admissionClosed":false,"admissionClosedAt":null,"shutdownIntent":"","armedShowId":"","showCommitted":false,
			"readiness":{"state":"unknown","reason":"no readiness result recorded","sameEpoch":false,"fresh":false,"checks":[]},
			"powerPhase":{"state":"unknown","reason":""},
			"transition":{"state":"unknown","reason":""},
			"cues":{"state":"recorded","reason":"","cues":[]},
			"backgroundAudio":{"state":"recorded","reason":"","steps":[],"pinnedMaxGainDb":-10},
			"degraded":false,"updatedAt":"2026-08-18T22:00:00Z"}}`)
	}))
	defer ts.Close()

	var stdout, stderr bytes.Buffer
	code := cmdNight([]string{"status", "--server", ts.URL}, &stdout, &stderr, time.Now)
	if code != exitOK {
		t.Fatalf("exit code = %d, want exitOK; stderr=%s", code, stderr.String())
	}
	out := stdout.String()
	if !strings.Contains(out, "Pinned max gain: -10.0 dB") {
		t.Errorf("stdout does not contain the pinned max gain line; stdout=%s", out)
	}
	if !strings.Contains(out, "Background audio: never started this cycle") {
		t.Errorf("stdout does not contain the narrowed never-started header; stdout=%s", out)
	}
	if strings.Contains(out, "not configured") {
		t.Errorf("stdout still offers the possibly-not-configured reading alongside a real pinned ceiling; stdout=%s", out)
	}
}

// TestCmdNightStatusPrintsPinnedMaxGainNoneWithReason proves the none
// case prints its own reason rather than staying silent: an operator
// reading a printed session with no pinnedMaxGainDb needs to see WHY,
// same as every other evidence block on this screen.
func TestCmdNightStatusPrintsPinnedMaxGainNoneWithReason(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.Header().Set("ShowMesh-API-Version", "1")
		w.WriteHeader(http.StatusOK)
		_, _ = fmt.Fprint(w, `{"serverTime":"2026-08-18T22:00:00Z",
			"session":{"id":"s1","configObjectId":"halloween-main","configRevision":1,"state":"resting-intershow",
			"stateEnteredAt":"2026-08-18T22:00:00Z","cycle":1,"finalShowRequested":false,"finalShowRequestedAt":null,
			"admissionClosed":false,"admissionClosedAt":null,"shutdownIntent":"","armedShowId":"","showCommitted":false,
			"readiness":{"state":"unknown","reason":"no readiness result recorded","sameEpoch":false,"fresh":false,"checks":[]},
			"powerPhase":{"state":"unknown","reason":""},
			"transition":{"state":"unknown","reason":""},
			"cues":{"state":"recorded","reason":"","cues":[]},
			"backgroundAudio":{"state":"recorded",
				"reason":"resting.backgroundAudio is not configured on this session's pinned night.session revision",
				"steps":[]},
			"degraded":false,"updatedAt":"2026-08-18T22:00:00Z"}}`)
	}))
	defer ts.Close()

	var stdout, stderr bytes.Buffer
	code := cmdNight([]string{"status", "--server", ts.URL}, &stdout, &stderr, time.Now)
	if code != exitOK {
		t.Fatalf("exit code = %d, want exitOK; stderr=%s", code, stderr.String())
	}
	out := stdout.String()
	if !strings.Contains(out, "Pinned max gain: none (resting.backgroundAudio is not configured on this session's pinned night.session revision)") {
		t.Errorf("stdout does not contain the none-with-reason line; stdout=%s", out)
	}
}

// TestCmdNightStartWithOverrideSendsInterlockOverrides proves --override
// RULE=REASON is translated into the request body's own
// interlockOverrides array, which the coordinator's start-night gate
// (internal/coordinator/api/nightinterlock.go) reads.
func TestCmdNightStartWithOverrideSendsInterlockOverrides(t *testing.T) {
	var gotBody string
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		buf := make([]byte, r.ContentLength)
		_, _ = r.Body.Read(buf)
		gotBody = string(buf)
		w.Header().Set("Content-Type", "application/json")
		w.Header().Set("ShowMesh-API-Version", "1")
		w.WriteHeader(http.StatusAccepted)
		_, _ = fmt.Fprint(w, `{"serverTime":"2026-08-18T22:00:00Z","command":{"command":"start-night","outcome":"applied"},
			"session":{"id":"s1","configObjectId":"halloween-main","configRevision":1,"state":"transition-to-show",
			"stateEnteredAt":"2026-08-18T22:00:00Z","cycle":1,"finalShowRequested":false,"finalShowRequestedAt":null,
			"admissionClosed":false,"admissionClosedAt":null,"shutdownIntent":"","armedShowId":"a1","showCommitted":false,
			"readiness":{"state":"unknown","reason":"","sameEpoch":true,"fresh":true,"checks":[]},
			"powerPhase":{"state":"unknown","reason":""},"transition":{"state":"not_available","reason":""},
			"degraded":false,"updatedAt":"2026-08-18T22:00:00Z"}}`)
	}))
	defer ts.Close()

	var stdout, stderr bytes.Buffer
	code := cmdNight([]string{"start", "--override", "cooldown=operator confirmed safe", "--server", ts.URL, "--token", "smsh_test"}, &stdout, &stderr, time.Now)
	if code != exitOK {
		t.Fatalf("exit code = %d, want exitOK; stdout=%s stderr=%s", code, stdout.String(), stderr.String())
	}
	if !strings.Contains(gotBody, `"interlockOverrides"`) || !strings.Contains(gotBody, `"cooldown"`) || !strings.Contains(gotBody, "operator confirmed safe") {
		t.Fatalf("request body = %q, want it to carry the interlockOverrides entry", gotBody)
	}
}

// TestCmdNightStartOverrideFlagRejectsMalformedValue proves a value with
// no "=" is refused at parse time, before any request is sent.
func TestCmdNightStartOverrideFlagRejectsMalformedValue(t *testing.T) {
	var stdout, stderr bytes.Buffer
	code := cmdNight([]string{"start", "--override", "no-equals-sign", "--server", "http://unused.invalid", "--token", "smsh_test"}, &stdout, &stderr, time.Now)
	if code == exitOK {
		t.Fatalf("exit code = %d, want a parse failure for a malformed --override value", code)
	}
}

// TestCmdNightFadeOutWithOverrideSendsInterlockOverrides proves --override
// is available on "night fade-out" too, not only "night start"; Track F
// seam F6's gate now covers fade-out-night's own phase.
func TestCmdNightFadeOutWithOverrideSendsInterlockOverrides(t *testing.T) {
	var gotBody string
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		buf := make([]byte, r.ContentLength)
		_, _ = r.Body.Read(buf)
		gotBody = string(buf)
		w.Header().Set("Content-Type", "application/json")
		w.Header().Set("ShowMesh-API-Version", "1")
		w.WriteHeader(http.StatusAccepted)
		_, _ = fmt.Fprint(w, `{"serverTime":"2026-08-18T22:00:00Z","command":{"command":"fade-out-night","outcome":"applied"},
			"session":{"id":"s1","configObjectId":"halloween-main","configRevision":1,"state":"fading-out",
			"stateEnteredAt":"2026-08-18T22:00:00Z","cycle":0,"finalShowRequested":false,"finalShowRequestedAt":null,
			"admissionClosed":true,"admissionClosedAt":"2026-08-18T22:00:00Z","shutdownIntent":"fade-out","armedShowId":"","showCommitted":false,
			"readiness":{"state":"unknown","reason":"","sameEpoch":true,"fresh":true,"checks":[]},
			"powerPhase":{"state":"unknown","reason":""},"transition":{"state":"not_available","reason":""},
			"degraded":false,"updatedAt":"2026-08-18T22:00:00Z"}}`)
	}))
	defer ts.Close()

	var stdout, stderr bytes.Buffer
	code := cmdNight([]string{"fade-out", "--override", "cooldown=operator confirmed safe", "--server", ts.URL, "--token", "smsh_test"}, &stdout, &stderr, time.Now)
	if code != exitOK {
		t.Fatalf("exit code = %d, want exitOK; stdout=%s stderr=%s", code, stdout.String(), stderr.String())
	}
	if !strings.Contains(gotBody, `"interlockOverrides"`) || !strings.Contains(gotBody, `"cooldown"`) {
		t.Fatalf("request body = %q, want it to carry the interlockOverrides entry", gotBody)
	}
}

// TestCmdNightEndSessionHasNoOverrideFlag proves end-session (which
// consults no interlock) does not even accept --override.
func TestCmdNightEndSessionHasNoOverrideFlag(t *testing.T) {
	var stdout, stderr bytes.Buffer
	code := cmdNight([]string{"end-session", "--override", "cooldown=x", "--server", "http://unused.invalid", "--token", "smsh_test"}, &stdout, &stderr, time.Now)
	if code == exitOK {
		t.Fatalf("exit code = %d, want a parse failure: end-session has no --override flag", code)
	}
}
