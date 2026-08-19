package api

import (
	"strings"
	"testing"
	"time"
)

// The end-of-night playlist is dispatched by the same controller as the
// ordinary resting playlist, so readiness has to check it too. It
// previously went unchecked, which meant a misconfigured or missing
// end-of-night playlist read "ready" right up until the final show ended.

// nightSessionBodyWithEndOfNight is validNightSessionBody with a distinct
// end-of-night playlist that the fake FPP does not serve.
const nightSessionBodyWithEndOfNight = `{
	"show": "halloween-2026",
	"label": "Halloween main loop",
	"showPlaylist": {"fppInstanceId": "player-01", "playlist": "halloween-show"},
	"resting": {
		"fppInstanceId": "player-01",
		"playlist": "halloween-resting",
		"endOfNightPlaylist": "halloween-endofnight",
		"timelineAsset": {"show": "halloween-2026", "sequence": "resting-loop", "target": "player-01"},
		"endOfNightRepeat": true
	},
	"enterShow": {
		"cues": [
			{"name": "lighting-fade", "role": "lighting", "action": "lighting-fade-out", "offsetMs": -20000, "barrier": true}
		],
		"blackoutHoldMs": 6000
	},
	"enterResting": {"cues": [], "blackoutAfterShowMs": 6000}
}`

func nightReadinessCheckNames(t *testing.T, api *API) []string {
	t.Helper()
	got := mustGetNightSession(t, api)
	names := make([]string, 0, len(got.Session.Readiness.Checks))
	for _, c := range got.Session.Readiness.Checks {
		names = append(names, c.Name)
	}
	return names
}

// A distinct end-of-night playlist that cannot be read must not produce a
// green readiness result, and the failing check must name that playlist.
func TestReadiness_UnreadableEndOfNightPlaylistIsNotReady(t *testing.T) {
	api, _, token, _, _, obs := setupNightControlFixtureWithBody(t, time.Hour, nightSessionBodyWithEndOfNight)
	setHealthyFPPReachable(obs, testNow)

	mustNightCommand(t, api, token, "prepare-site")
	mustNightCommand(t, api, token, "run-readiness")

	got := mustGetNightSession(t, api)
	if got.Session.Readiness.Outcome == "ready" {
		t.Fatalf("readiness reported ready with an unreadable end-of-night playlist: %+v", got.Session.Readiness.Checks)
	}

	var found bool
	for _, c := range got.Session.Readiness.Checks {
		if c.Name != nightCheckPrefixEndOfNight+":playlist-shape:halloween-endofnight" {
			continue
		}
		found = true
		if c.State == string(nightHealthHealthy()) {
			t.Fatalf("the end-of-night shape check passed against a playlist FPP does not serve: %+v", c)
		}
		if c.Reason == "" {
			t.Error("the failing end-of-night check states no reason")
		}
	}
	if !found {
		t.Fatalf("no check names the configured end-of-night playlist; got %v", nightReadinessCheckNames(t, api))
	}
}

// The end-of-night playlist defaults to the ordinary resting playlist, so
// the common case must not run every resting check twice.
func TestReadiness_IdenticalEndOfNightPlaylistIsNotCheckedTwice(t *testing.T) {
	api, _, token, _, obs := setupNightControlFixture(t, time.Hour)
	setHealthyFPPReachable(obs, testNow)

	mustNightCommand(t, api, token, "prepare-site")
	mustNightCommand(t, api, token, "run-readiness")

	names := nightReadinessCheckNames(t, api)
	seen := map[string]int{}
	for _, n := range names {
		seen[n]++
	}
	for n, count := range seen {
		if count > 1 {
			t.Errorf("check %q ran %d times; identical playlists must be checked once", n, count)
		}
	}
	for _, n := range names {
		if strings.HasPrefix(n, nightCheckPrefixEndOfNight+":") {
			t.Errorf("a separate end-of-night check %q ran even though both playlists are the same", n)
		}
	}
}
