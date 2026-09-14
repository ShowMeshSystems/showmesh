package api

import (
	"context"
	"testing"
	"time"

	"github.com/showmeshsystems/showmesh/pkg/observation"
)

// TestNightAdvanceTransitionToShow_LivePluginEvidenceNeverExtraNudges is
// the last required case for the owner's stale-evidence nudge ruling: when
// a push source (fpp-plugin here; fpp-mqtt is symmetric per
// nightloop_mqtt_precedence_test.go) already supplies fresh, matching
// evidence, nightShowLaunchIfBusy returns replace with an empty
// staleEvidenceReason, so this coordinator's own stale-evidence nudge
// (nightAdvanceTransitionToShow's own branch, distinct from the unrelated
// post-dispatch confirmation nudge every successful dispatch already
// issues - fppcommand_dispatch.go's own [FPPPollNudger] call) must never
// fire: a nudge here would poke the REST collector, which has nothing to
// fix and cannot affect a decision the plugin evidence already settled.
// Exactly one nudge call is still expected - the pre-existing
// post-dispatch one - proving this test isolates the NEW code path rather
// than asserting no nudge ever happens for a launch.
func TestNightAdvanceTransitionToShow_LivePluginEvidenceNeverExtraNudges(t *testing.T) {
	now0 := time.Date(2026, 10, 31, 20, 0, 0, 0, time.UTC)
	restStaleAt := now0.Add(-14 * time.Second)
	pluginFreshAt := now0.Add(-200 * time.Millisecond)

	h, st, rec, gotArgs, now := setupTransitionToShowTest(t, []observation.Observation{
		statusObservation("player-01", fppStatusValuePlaying, restStaleAt),
		playlistNameObservation("player-01", "halloween-resting", restStaleAt),
		pluginStatusObservation("player-01", fppStatusValuePlaying, pluginFreshAt),
		pluginPlaylistNameObservation("player-01", "halloween-resting", pluginFreshAt),
	})
	nudger := &recordingNudger{accept: true}
	h.deps.Nudger = nudger

	h.nightAdvanceTransitionToShow(context.Background(), now, rec)

	if got := nudger.callsFor("player-01"); got != 1 {
		t.Fatalf("NudgePoll calls with live plugin evidence = %d, want exactly 1 (only the pre-existing post-dispatch confirmation nudge; nothing here was stale)", got)
	}
	if len(*gotArgs) < 1 || (*gotArgs)[0] != "halloween-show" {
		t.Fatalf("Start Playlist args = %v, want [halloween-show, ...] (plugin evidence alone must be enough to launch)", *gotArgs)
	}
	if got := mustGetCurrentSession(t, st); got.State != nightStateLive {
		t.Fatalf("state = %q, want %q", got.State, nightStateLive)
	}
}
