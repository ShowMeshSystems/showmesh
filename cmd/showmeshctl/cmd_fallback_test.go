package main

import (
	"bytes"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

const fallbackListBody = `{"serverTime":"2026-10-05T20:10:00Z","programs":[
 {"fppInstanceUuid":"u-fallback","packageId":"pkg-2","revision":"rev-2","show":"halloween","generation":3,
  "expiresAt":"2026-10-06T20:00:00Z","compiledAt":"2026-10-05T20:00:00Z",
  "playerState":{"state":"fallback","since":"2026-10-05T20:05:00Z","playlistName":"Main Show","packageId":"pkg-1",
   "packageRevision":"rev-1","cutoffAt":"2026-10-06T08:00:00Z","reportedAt":"2026-10-05T20:09:55Z","pluginReporting":true,
   "held":true,"holdReason":"running-from-fallback",
   "message":"This player is running the show from its fallback program. The coordinator starts no Cues for it until the playlist ends."}},
 {"fppInstanceUuid":"u-waiting","packageId":"pkg-3","revision":"rev-3","show":"halloween","generation":3,
  "expiresAt":"2026-10-06T20:00:00Z","compiledAt":"2026-10-05T20:00:00Z",
  "playerState":{"state":"normal","since":"2026-10-05T20:08:00Z","reportedAt":"2026-10-05T20:09:58Z","pluginReporting":true,
   "held":true,"holdReason":"waiting-for-acknowledgement","acknowledgementWaitSeconds":118}},
 {"fppInstanceUuid":"u-quiet","packageId":"pkg-4","revision":"rev-4","show":"halloween","generation":3,
  "expiresAt":"2026-10-06T20:00:00Z","compiledAt":"2026-10-05T20:00:00Z",
  "playerState":{"state":"normal","since":"2026-10-05T19:00:00Z","reportedAt":"2026-10-05T19:30:00Z","pluginReporting":false,"held":false}},
 {"fppInstanceUuid":"u-old-plugin","packageId":"pkg-5","revision":"rev-5","show":"halloween","generation":3,
  "expiresAt":"2026-10-06T20:00:00Z","compiledAt":"2026-10-05T20:00:00Z"}
]}`

func fallbackListServer(t *testing.T) *httptest.Server {
	t.Helper()
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet || r.URL.Path != "/api/v1/fallback-programs" {
			t.Errorf("unexpected request %s %s", r.Method, r.URL.Path)
		}
		w.Header().Set("ShowMesh-API-Version", "1")
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(fallbackListBody))
	}))
	t.Cleanup(ts.Close)
	return ts
}

func runFallback(t *testing.T, args ...string) (int, string, string) {
	t.Helper()
	var stdout, stderr bytes.Buffer
	code := run(append([]string{"fallback"}, args...), &stdout, &stderr, fixedClock(mustParse(t, "2026-10-05T20:10:00Z")))
	return code, stdout.String(), stderr.String()
}

func TestCmdFallbackListShowsEveryPlayersStateAndTheOperatorMessage(t *testing.T) {
	ts := fallbackListServer(t)
	code, out, errOut := runFallback(t, "list", "--server", ts.URL)
	if code != exitOK {
		t.Fatalf("exit code = %d, want exitOK; stderr=%s", code, errOut)
	}
	for _, want := range []string{
		"u-fallback", "fallback", "yes (running-from-fallback)", "Main Show", "2026-10-06T08:00:00Z",
		"yes (waiting-for-acknowledgement)", "not reporting", "not reported",
		"u-fallback: This player is running the show from its fallback program.",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("list output is missing %q:\n%s", want, out)
		}
	}
	var quiet, old string
	for _, line := range strings.Split(out, "\n") {
		if strings.HasPrefix(line, "u-quiet") {
			quiet = line
		}
		if strings.HasPrefix(line, "u-old-plugin") {
			old = line
		}
	}
	if !strings.Contains(quiet, "normal") || !strings.Contains(quiet, "no ") || !strings.Contains(quiet, "not reporting") {
		t.Errorf("the quiet plugin's row is %q, want normal, not held, not reporting", quiet)
	}
	if !strings.Contains(old, "not reported") || strings.Contains(old, "yes") {
		t.Errorf("the row of a plugin that never reported is %q, want not reported and not held", old)
	}
}

func TestCmdFallbackListAsJSONCarriesThePlayerStateThrough(t *testing.T) {
	ts := fallbackListServer(t)
	code, out, errOut := runFallback(t, "list", "--server", ts.URL, "--output", "json")
	if code != exitOK {
		t.Fatalf("exit code = %d, want exitOK; stderr=%s", code, errOut)
	}
	for _, want := range []string{`"holdReason": "running-from-fallback"`, `"acknowledgementWaitSeconds": 118`, `"pluginReporting": false`} {
		if !strings.Contains(out, want) {
			t.Errorf("JSON output is missing %s:\n%s", want, out)
		}
	}
}

func TestCmdFallbackShowPrintsOnePlayerInFull(t *testing.T) {
	ts := fallbackListServer(t)
	code, out, errOut := runFallback(t, "show", "--server", ts.URL, "u-waiting")
	if code != exitOK {
		t.Fatalf("exit code = %d, want exitOK; stderr=%s", code, errOut)
	}
	for _, want := range []string{"u-waiting", "normal since 2026-10-05T20:08:00Z", "yes (waiting-for-acknowledgement)", "118 seconds"} {
		if !strings.Contains(out, want) {
			t.Errorf("show output is missing %q:\n%s", want, out)
		}
	}
	if strings.Contains(out, "u-fallback") {
		t.Errorf("show printed another player:\n%s", out)
	}

	code, out, _ = runFallback(t, "show", "--server", ts.URL, "u-old-plugin")
	if code != exitOK || !strings.Contains(out, "never reported") {
		t.Errorf("show for a plugin that never reported: exit %d, output:\n%s", code, out)
	}

	code, _, errOut = runFallback(t, "show", "--server", ts.URL, "u-nobody")
	if code != exitNotFound || !strings.Contains(errOut, "u-nobody") {
		t.Errorf("show for an unknown player: exit %d, stderr %q, want exitNotFound naming the player", code, errOut)
	}
}

func TestCmdFallbackClearRefusesWithoutConfirmAndSendsTheDeleteWithIt(t *testing.T) {
	var gotMethod, gotPath string
	calls := 0
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		gotMethod, gotPath = r.Method, r.URL.Path
		w.Header().Set("ShowMesh-API-Version", "1")
		w.WriteHeader(http.StatusNoContent)
	}))
	defer ts.Close()

	code, _, errOut := runFallback(t, "clear", "--server", ts.URL, "u1")
	if code != exitUsage || calls != 0 || !strings.Contains(errOut, "--confirm") {
		t.Fatalf("without --confirm: exit %d, %d calls, stderr %q; want exitUsage, no call, and --confirm named", code, calls, errOut)
	}

	code, out, errOut := runFallback(t, "clear", "--server", ts.URL, "--confirm", "u1")
	if code != exitOK {
		t.Fatalf("exit code = %d, want exitOK; stderr=%s", code, errOut)
	}
	if gotMethod != http.MethodDelete || gotPath != "/api/v1/fallback-programs/u1/fallback-state" {
		t.Errorf("sent %s %s, want DELETE /api/v1/fallback-programs/u1/fallback-state", gotMethod, gotPath)
	}
	if !strings.Contains(out, "u1") {
		t.Errorf("output does not name the player:\n%s", out)
	}
}

func TestCmdFallbackWithNoSubcommandPrintsUsage(t *testing.T) {
	code, _, errOut := runFallback(t)
	if code != exitUsage || !strings.Contains(errOut, "fallback list") && !strings.Contains(errOut, "list") {
		t.Fatalf("exit %d, stderr %q, want exitUsage and the subcommands", code, errOut)
	}
}
