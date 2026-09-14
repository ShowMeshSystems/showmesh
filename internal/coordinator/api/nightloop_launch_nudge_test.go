package api

import (
	"context"
	"testing"
	"time"

	"github.com/showmeshsystems/showmesh/pkg/observation"
)

// TestNightAdvanceTransitionToShow_LivePluginEvidenceNeverExtraNudges: live
// plugin evidence (fpp-mqtt is symmetric) makes nightShowLaunchIfBusy
// return replace with an empty staleEvidenceReason, so the stale-evidence
// nudge branch must never fire. The one nudge still seen is the unrelated,
// pre-existing post-dispatch confirmation nudge every dispatch issues.
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
