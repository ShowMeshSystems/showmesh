package api

import (
	"context"
	"testing"
	"time"

	"github.com/showmeshsystems/showmesh/internal/coordinator/store"
	pkgaudio "github.com/showmeshsystems/showmesh/pkg/audio"
	"github.com/showmeshsystems/showmesh/pkg/mqttproto"
	"github.com/showmeshsystems/showmesh/pkg/observation"
)

// bedStepRowForTest is one resolved ledger row dispatched at dispatchedAt
// and resolved at resolvedAt, the shape a scheduled start has: resolved
// seconds after it was sent.
func bedStepRowForTest(kind string, outcome string, dispatchedAt, resolvedAt time.Time) nightBackgroundAudioHistoryRow {
	return nightBackgroundAudioHistoryRow{
		Step:   nightBackgroundAudioStep{Seq: 1, Kind: kind},
		NodeID: "node-a",
		Parsed: true,
		Row: store.NightCueOutboxRecord{
			State: nightCueStateResolved, Outcome: outcome,
			DispatchedAt: &dispatchedAt, ResolvedAt: &resolvedAt,
		},
	}
}

func bedReportForTest(nodeID, sessionID, state string, at time.Time) []observation.Observation {
	out := []observation.Observation{bedNodeAudioReport(nodeID, at, at)}
	if state != "" {
		out = append(out, sessionStateObservation(sessionID, state, at, at))
	}
	return out
}

func TestNightBedNodeLostSession_ScheduledStartReportFromTheWaitIsNotALoss(t *testing.T) {
	dispatched := testNow
	resolved := testNow.Add(4500 * time.Millisecond)
	steps := []nightBackgroundAudioHistoryRow{
		bedStepRowForTest(nightBGStepApply, nightCueOutcomeConfirmed, testNow.Add(-time.Minute), testNow.Add(-time.Minute)),
		bedStepRowForTest(nightBGStepStart, nightCueOutcomeConfirmed, dispatched, resolved),
	}
	const sessionID = "bed"
	cases := []struct {
		name  string
		state string
		at    time.Time
		lost  bool
	}{
		{"ready inside the wait", string(pkgaudio.StateReady), dispatched.Add(2 * time.Second), false},
		{"no session inside the wait", "", dispatched.Add(2 * time.Second), false},
		{"stopped after playback began", string(pkgaudio.StateStopped), resolved.Add(time.Second), true},
		{"no session after playback began", "", resolved.Add(time.Second), true},
		{"playing after playback began", string(pkgaudio.StatePlaying), resolved.Add(time.Second), false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			audio := &fakeNodeAudioLister{}
			audio.setObservations("node-a", bedReportForTest("node-a", sessionID, tc.state, tc.at))
			_, lost := nightBedNodeLostSession(audio, resolved.Add(10*time.Second), "node-a", sessionID, steps)
			if lost != tc.lost {
				t.Fatalf("lost = %v, want %v", lost, tc.lost)
			}
		})
	}
}

// bedPausedAndResumedForTest drives a two-node bed through start and a
// show pause, then a shared resume (and fade-in) at testNow plus an hour.
func bedPausedAndResumedForTest(t *testing.T, h *handlers, st *store.Store, pub *fakeAudioPublisher, resumeResults map[string]mqttproto.ResultPayload) (store.NightSessionRecord, string) {
	t.Helper()
	putBackgroundAudioAsset(t, st, "halloween", "bg-1", "node-a", "asset-1")
	putBackgroundAudioAsset(t, st, "halloween", "bg-2", "node-a", "asset-2")
	putAudioNodeForTest(t, st, "node-a")
	putAudioNodeNoLTCForTest(t, st, "node-b")
	ba := multiNodeBedConfig("node-a", "node-a", "node-b")
	fadeInMs := 500
	ba.FadeInMs = &fadeInMs
	rec := mustCreateRestingSessionWithBackgroundAudio(t, st, "sess-1", "node-a", ba, nightStateRestingIntershow)
	sessionID := nightBackgroundAudioSessionID(rec)

	pub.result = confirmedResultForAction("x", sessionID, "started")
	pub.resultsByNode = map[string]mqttproto.ResultPayload{
		"node-a:audio.session.prepare": scheduleProbeEvidenceResult(true, 1_700_000_000_000_000_000, ""),
	}
	driveNightAdvanceBackgroundAudioUntilStable(t, h, pub, rec, 10)

	pub.resultsByNode = map[string]mqttproto.ResultPayload{
		"node-a:audio.session.pause": pauseResultWithBookmark(true, "track-2", 1, 4500),
	}
	h.nightStopBackgroundAudioIfRunning(context.Background(), testNow, rec)

	next := rec
	next.Cycle = rec.Cycle + 1
	pub.resultsByNode = map[string]mqttproto.ResultPayload{
		"node-a:audio.session.prepare": scheduleProbeEvidenceResult(true, 1_800_000_000_000_000_000, ""),
	}
	for k, v := range resumeResults {
		pub.resultsByNode[k] = v
	}
	driveNightAdvanceBackgroundAudioUntilStableAt(t, h, pub, next, testNow.Add(time.Hour), 12)
	return next, sessionID
}

func TestNightBedResume_PausedReportFromBeforeTheResumeTookEffectDoesNotSendASecondResume(t *testing.T) {
	h, st, pub, _ := nightBackgroundAudioTestHandlers(t)
	rec, sessionID := bedPausedAndResumedForTest(t, h, st, pub, nil)
	if got := countDispatchedAction(pub, "audio.session.resume"); got != 2 {
		t.Fatalf("setup: resumes = %d, want one per node", got)
	}

	// Recorded before the resume instant, so it still says paused.
	before := testNow.Add(-2 * time.Second)
	audio := h.deps.Audio.(*fakeNodeAudioLister)
	for _, nodeID := range []string{"node-a", "node-b"} {
		audio.setObservations(nodeID, bedReportForTest(nodeID, sessionID, string(pkgaudio.StatePaused), before))
	}
	driveNightAdvanceBackgroundAudioUntilStableAt(t, h, pub, rec, testNow.Add(time.Hour), 6)

	if got := countDispatchedAction(pub, "audio.session.resume"); got != 2 {
		t.Fatalf("resumes = %d after a paused report that predates the resume, want still 2", got)
	}
}

func TestNightBedResume_ResumeRefusedBecauseTheNodeIsPlayingKeepsItsExpiryRefresh(t *testing.T) {
	h, st, pub, _ := nightBackgroundAudioTestHandlers(t)
	rec, sessionID := bedPausedAndResumedForTest(t, h, st, pub, map[string]mqttproto.ResultPayload{
		"node-b:audio.session.resume": nodeRefusedResult("resume", "bed", "session is not paused"),
	})
	refreshes := func() int {
		n := 0
		for _, d := range pub.dispatchedSnapshot() {
			if d.NodeID == "node-b" && d.Action == "audio.session.apply" {
				if _, ok := d.Params["expiresInMs"]; ok {
					n++
				}
			}
		}
		return n
	}
	audio := h.deps.Audio.(*fakeNodeAudioLister)
	resumedAt := testNow.Add(time.Hour)
	due := resumedAt.Add(5 * time.Minute)
	base := refreshes()

	// No report yet: the refusal alone does not say the bed is playing.
	driveNightAdvanceBackgroundAudioUntilStableAt(t, h, pub, rec, due, 4)
	if got := refreshes(); got != base {
		t.Fatalf("expiry refreshes for the refused node went from %d to %d with no report of it playing, want unchanged", base, got)
	}

	playing := resumedAt.Add(time.Minute)
	audio.setObservations("node-b", bedReportForTest("node-b", sessionID, string(pkgaudio.StatePlaying), playing))
	driveNightAdvanceBackgroundAudioUntilStableAt(t, h, pub, rec, due, 4)
	if got := refreshes(); got != base+1 {
		t.Fatalf("expiry refreshes for the refused node = %d after it reported playing, want %d", got, base+1)
	}
}
