package api

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/showmeshsystems/showmesh/internal/coordinator/config"
	"github.com/showmeshsystems/showmesh/internal/coordinator/identity"
	"github.com/showmeshsystems/showmesh/internal/coordinator/store"
	pkgaudio "github.com/showmeshsystems/showmesh/pkg/audio"
	"github.com/showmeshsystems/showmesh/pkg/mqttproto"
)

// This file proves the pre-show bed's start chain: how soon after
// start-preshow the start is dispatched, and what instant it names.

const bedStartClockReading = int64(1_700_000_000_000_000_000)

// preparedLeadSlackNs allows for the real milliseconds one fake node's
// prepare may answer after the clock holder's, which the lead adds.
const preparedLeadSlackNs = int64(50 * time.Millisecond)

// twoNodeBedForStart registers a two-node bed (node-a holds the clock) on a
// session in state, and makes every command confirm.
func twoNodeBedForStart(t *testing.T, st *store.Store, pub *fakeAudioPublisher, state string) store.NightSessionRecord {
	t.Helper()
	putBackgroundAudioAsset(t, st, "halloween", "bg-1", "node-a", "asset-1")
	putBackgroundAudioAsset(t, st, "halloween", "bg-2", "node-a", "asset-2")
	putAudioNodeForTest(t, st, "node-a")
	putAudioNodeNoLTCForTest(t, st, "node-b")
	rec := mustCreateRestingSessionWithBackgroundAudio(t, st, "sess-1", "node-a", multiNodeBedConfig("node-a", "node-a", "node-b"), state)
	pub.result = confirmedResultForAction("x", nightBackgroundAudioSessionID(rec), "started")
	pub.resultsByNode = map[string]mqttproto.ResultPayload{
		"node-a:audio.session.prepare": scheduleProbeEvidenceResult(true, bedStartClockReading, ""),
	}
	return rec
}

func bedActionsForNode(pub *fakeAudioPublisher, nodeID string) []string {
	var out []string
	for _, d := range pub.dispatchedSnapshot() {
		if d.NodeID == nodeID {
			out = append(out, d.Action)
		}
	}
	return out
}

func bedStartInstant(t *testing.T, pub *fakeAudioPublisher, nodeID string) (int64, bool) {
	t.Helper()
	params, ok := dispatchedByNodeAction(pub, nodeID, "audio.session.start")
	if !ok {
		t.Fatalf("%s: no audio.session.start dispatched", nodeID)
	}
	raw, present := params[pkgaudio.ParamScheduledAtNs]
	if !present {
		return 0, false
	}
	// The fake publisher decodes params as float64, which is exact only to
	// a few hundred nanoseconds here: far inside what these tests assert.
	ns, ok := raw.(float64)
	if !ok {
		t.Fatalf("%s: scheduledAtNs = %v (%T), want a number", nodeID, raw, raw)
	}
	return int64(ns), true
}

func assertPreparedLead(t *testing.T, gotNs int64, wantLead time.Duration) {
	t.Helper()
	lead := gotNs - bedStartClockReading
	if lead < int64(wantLead)-int64(time.Microsecond) || lead > int64(wantLead)+preparedLeadSlackNs {
		t.Fatalf("start instant is %s past the clock reading, want %s (plus at most %s for the slower prepare)",
			time.Duration(lead), wantLead, time.Duration(preparedLeadSlackNs))
	}
}

// TestNightLoop_StartPreshowDispatchesTheBedStartWithoutATick drives the
// real loop from the operator's start-preshow. The loop's interval is an
// hour, so every command up to the start can only have gone out on the tick
// the command itself woke, with no tick of waiting between steps.
func TestNightLoop_StartPreshowDispatchesTheBedStartWithoutATick(t *testing.T) {
	h, st, pub, _ := nightBackgroundAudioTestHandlers(t)
	rec := twoNodeBedForStart(t, st, pub, nightStatePreparing)
	operator := mustCreatePrincipal(t, h.deps.Identity, "operator-1", identity.RoleOperator)
	token := mustIssueToken(t, h.deps.Identity, operator.ID)

	opts := Options{Clock: fixedClock(testNow), Logger: testLogger(), NightLoopInterval: time.Hour, NightReadinessMaxAge: time.Hour}
	api := New(h.deps, opts)
	loop := NewNightLoop(h.deps, opts)
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		defer close(done)
		loop.Run(ctx)
	}()
	t.Cleanup(func() {
		cancel()
		<-done
	})

	mustNightCommand(t, api, token, "start-preshow")

	waitForBedDispatchCondition(t, 10*time.Second, func() {
		t.Fatalf("the bed start was not dispatched to both nodes; node-a saw %v, node-b saw %v",
			bedActionsForNode(pub, "node-a"), bedActionsForNode(pub, "node-b"))
	}, func() bool {
		_, a := dispatchedByNodeAction(pub, "node-a", "audio.session.start")
		_, b := dispatchedByNodeAction(pub, "node-b", "audio.session.start")
		return a && b
	})

	want := "audio.session.apply audio.gain.set audio.session.prepare audio.session.start"
	for _, nodeID := range []string{"node-a", "node-b"} {
		if got := strings.Join(bedActionsForNode(pub, nodeID), " "); got != want {
			t.Fatalf("%s received %q, want %q", nodeID, got, want)
		}
	}
	atA, okA := bedStartInstant(t, pub, "node-a")
	atB, okB := bedStartInstant(t, pub, "node-b")
	if !okA || !okB || atA != atB {
		t.Fatalf("start instants: node-a=%d (%v) node-b=%d (%v), want one shared instant", atA, okA, atB, okB)
	}
	assertPreparedLead(t, atA, 750*time.Millisecond)

	history, err := h.nightBackgroundAudioHistory(context.Background(), rec)
	if err != nil {
		t.Fatalf("history: %v", err)
	}
	for _, row := range history {
		if row.Row.DispatchedAt != nil && !row.Row.DispatchedAt.Equal(testNow) {
			t.Fatalf("%s was dispatched at %s, want the instant of the command (%s)", row.Row.CueName, row.Row.DispatchedAt, testNow)
		}
	}
}

// ticksUntilBedStart ticks the night loop once per interval on a test clock
// until every node has been sent its start, and returns how many ticks ran.
func ticksUntilBedStart(t *testing.T, h *handlers, pub *fakeAudioPublisher, nodeIDs ...string) int {
	t.Helper()
	now := testNow
	for ticks := 1; ticks <= 6; ticks++ {
		h.nightTick(context.Background(), now)
		started := true
		for _, nodeID := range nodeIDs {
			if _, ok := dispatchedByNodeAction(pub, nodeID, "audio.session.start"); !ok {
				started = false
			}
		}
		if started {
			return ticks
		}
		now = now.Add(defaultNightLoopInterval)
	}
	t.Fatalf("the bed start was not dispatched within 6 ticks")
	return 0
}

// TestNightTick_BedStartChainTakesOneTick measures the chain on the test
// clock: the tick that sends the apply also sends the start, so no time
// passes between them. Before this change the start left on a later tick,
// one interval later for a two-node bed and two for a single-node bed.
func TestNightTick_BedStartChainTakesOneTick(t *testing.T) {
	t.Run("two nodes", func(t *testing.T) {
		h, st, pub, _ := nightBackgroundAudioTestHandlers(t)
		twoNodeBedForStart(t, st, pub, nightStatePreshow)
		ticks := ticksUntilBedStart(t, h, pub, "node-a", "node-b")
		t.Logf("two-node bed: start dispatched on tick %d, %s after the first tick", ticks, time.Duration(ticks-1)*defaultNightLoopInterval)
		if ticks != 1 {
			t.Fatalf("start dispatched on tick %d, want tick 1", ticks)
		}
	})
	t.Run("one node", func(t *testing.T) {
		h, st, pub, _ := nightBackgroundAudioTestHandlers(t)
		putBackgroundAudioAsset(t, st, "halloween", "bg-1", "node-a", "asset-1")
		putBackgroundAudioAsset(t, st, "halloween", "bg-2", "node-a", "asset-2")
		ba := twoItemBackgroundAudioConfig("node-a", config.NightSessionBackgroundRepeatPlaylist, config.NightSessionBackgroundResumeRestart, config.NightSessionItemTransitionSequential)
		rec := mustCreateRestingSessionWithBackgroundAudio(t, st, "sess-1", "node-a", ba, nightStatePreshow)
		pub.result = confirmedResultForAction("x", nightBackgroundAudioSessionID(rec), "started")
		ticks := ticksUntilBedStart(t, h, pub, "node-a")
		t.Logf("one-node bed: start dispatched on tick %d, %s after the first tick", ticks, time.Duration(ticks-1)*defaultNightLoopInterval)
		if ticks != 1 {
			t.Fatalf("start dispatched on tick %d, want tick 1", ticks)
		}
	})
}

// TestNightBedStart_LeadComesFromThePreparedStartSettings proves the lead
// after every node confirmed its prepare is the operator's prepared-start
// pair, not the scheduled-start pair that governs cues and resumes.
func TestNightBedStart_LeadComesFromThePreparedStartSettings(t *testing.T) {
	h, st, pub, _ := nightBackgroundAudioTestHandlers(t)
	rec := twoNodeBedForStart(t, st, pub, nightStatePreshow)
	settings := config.AudioSettingsDefaultPayload
	settings.ScheduledStartDeliveryBoundMs, settings.ScheduledStartMarginMs = 9000, 9000
	settings.PreparedStartDeliveryBoundMs, settings.PreparedStartMarginMs = 100, 200
	raw, err := config.EncodeAudioSettingsPayload(settings)
	if err != nil {
		t.Fatalf("encode audio.settings: %v", err)
	}
	putConfigForTest(t, st, config.AudioSettingsConfigKind, config.AudioSettingsConfigObjectID, raw)

	h.nightAdvanceBackgroundAudioChain(context.Background(), testNow, rec)

	atA, okA := bedStartInstant(t, pub, "node-a")
	atB, okB := bedStartInstant(t, pub, "node-b")
	if !okA || !okB || atA != atB {
		t.Fatalf("start instants: node-a=%d (%v) node-b=%d (%v), want one shared instant", atA, okA, atB, okB)
	}
	assertPreparedLead(t, atA, 300*time.Millisecond)
}

// TestNightBedStart_NodeThatDidNotLoadStartsOnArrival proves a node whose
// prepare did not confirm is not given an instant it has no time to load
// for, while the node that did load keeps the shared instant.
func TestNightBedStart_NodeThatDidNotLoadStartsOnArrival(t *testing.T) {
	h, st, pub, _ := nightBackgroundAudioTestHandlers(t)
	rec := twoNodeBedForStart(t, st, pub, nightStatePreshow)
	pub.resultsByNode["node-b:audio.session.prepare"] = refusedResultForAction("prepare", "the file is still being read")

	h.nightAdvanceBackgroundAudioChain(context.Background(), testNow, rec)

	atA, okA := bedStartInstant(t, pub, "node-a")
	if !okA {
		t.Fatalf("node-a start carries no instant, want the shared one")
	}
	assertPreparedLead(t, atA, 750*time.Millisecond)
	if at, ok := bedStartInstant(t, pub, "node-b"); ok {
		t.Fatalf("node-b start carries instant %d, want none (it starts on arrival)", at)
	}
	history, err := h.nightBackgroundAudioHistory(context.Background(), rec)
	if err != nil {
		t.Fatalf("history: %v", err)
	}
	latest, ok := nightBackgroundAudioLatestStepForNode(history, "node-b")
	if !ok || latest.Step.Kind != nightBGStepStart || latest.Row.Outcome != nightCueOutcomeConfirmed {
		t.Fatalf("node-b latest step = %+v, want a confirmed start", latest)
	}
	if !strings.Contains(latest.Row.OutcomeReason, nightBedNotLoadedUnalignedReason) {
		t.Fatalf("node-b start reason = %q, want it to say %q", latest.Row.OutcomeReason, nightBedNotLoadedUnalignedReason)
	}
	// The node consumed the prepare's revision even though it refused, so
	// a start at that same revision would be refused as stale.
	prepare, _ := dispatchedByNodeAction(pub, "node-b", "audio.session.prepare")
	start, _ := dispatchedByNodeAction(pub, "node-b", "audio.session.start")
	if start["revision"].(float64) <= prepare["revision"].(float64) {
		t.Fatalf("node-b start revision %v does not exceed its prepare revision %v", start["revision"], prepare["revision"])
	}
}

// TestNightTick_NodeThatMissesTheInstantIsStartedAgainWithoutOne proves a
// node the start reached too late is not left silent: the next tick sends
// its start once more with no instant, and records why on that step.
func TestNightTick_NodeThatMissesTheInstantIsStartedAgainWithoutOne(t *testing.T) {
	h, st, pub, _ := nightBackgroundAudioTestHandlers(t)
	rec := twoNodeBedForStart(t, st, pub, nightStatePreshow)
	missed := mqttproto.ResultPayload{
		Outcome: mqttproto.OutcomeConfirmed,
		Evidence: &mqttproto.ResultEvidence{Signal: "audio.session", Value: map[string]any{
			"outcome": "refused", "reason": pkgaudio.ReasonScheduledStartInPast + ": the instant has passed",
		}},
	}
	pub.resultsByNode["node-b:audio.session.start"] = missed

	h.nightTick(context.Background(), testNow)

	if _, ok := bedStartInstant(t, pub, "node-b"); !ok {
		t.Fatalf("node-b's first start carries no instant; the test needs it to miss one")
	}
	if got := countDispatchedByNodeAction(pub, "node-b", "audio.session.start"); got != 1 {
		t.Fatalf("node-b was sent %d starts on the first tick, want 1", got)
	}

	// A real node refuses for this reason only when the start names an instant.
	delete(pub.resultsByNode, "node-b:audio.session.start")
	h.nightTick(context.Background(), testNow.Add(defaultNightLoopInterval))

	if got := countDispatchedByNodeAction(pub, "node-b", "audio.session.start"); got != 2 {
		t.Fatalf("node-b was sent %d starts after the second tick, want 2 (one retry)", got)
	}
	if at, ok := bedStartInstant(t, pub, "node-b"); ok {
		t.Fatalf("node-b's retried start carries instant %d, want none", at)
	}
	history, err := h.nightBackgroundAudioHistory(context.Background(), rec)
	if err != nil {
		t.Fatalf("history: %v", err)
	}
	latestB, _ := nightBackgroundAudioLatestStepForNode(history, "node-b")
	if latestB.Step.Kind != nightBGStepStart || latestB.Row.Outcome != nightCueOutcomeConfirmed || !strings.Contains(latestB.Row.OutcomeReason, nightBedMissedInstantUnalignedReason) {
		t.Fatalf("node-b latest step = %s %s %q, want a confirmed start saying %q", latestB.Step.Kind, latestB.Row.Outcome, latestB.Row.OutcomeReason, nightBedMissedInstantUnalignedReason)
	}
	latestA, _ := nightBackgroundAudioLatestStepForNode(history, "node-a")
	if latestA.Step.Kind != nightBGStepStart || latestA.Row.Outcome != nightCueOutcomeConfirmed {
		t.Fatalf("node-a latest step = %s %s, want a confirmed start", latestA.Step.Kind, latestA.Row.Outcome)
	}

	for i := 2; i < 5; i++ {
		h.nightTick(context.Background(), testNow.Add(time.Duration(i)*defaultNightLoopInterval))
	}
	if got := countDispatchedByNodeAction(pub, "node-b", "audio.session.start"); got != 2 {
		t.Fatalf("node-b was sent %d starts in all, want 2 (the retry is not repeated)", got)
	}
	if got := countDispatchedByNodeAction(pub, "node-a", "audio.session.start"); got != 1 {
		t.Fatalf("node-a was sent %d starts, want 1", got)
	}
}

// TestNightBedStart_SlowFailedPrepareDoesNotPutTheInstantInThePast proves
// the lead covers the time a failing node kept the starts waiting: the clock
// holder answers in 0.3 s, the other node fails later, and the holder's
// instant is still ahead of it when its start goes out.
func TestNightBedStart_SlowFailedPrepareDoesNotPutTheInstantInThePast(t *testing.T) {
	const holderAnswers = 300 * time.Millisecond
	for _, tc := range []struct {
		name     string
		failsAt  time.Duration // zero: never answers, so the wait times out.
		heldBack time.Duration
	}{
		{name: "refuses after 2 s", failsAt: 2 * time.Second, heldBack: 2 * time.Second},
		{name: "times out", heldBack: scheduleProbeStepTimeout},
	} {
		t.Run(tc.name, func(t *testing.T) {
			h, st, pub, _ := nightBackgroundAudioTestHandlers(t)
			rec := twoNodeBedForStart(t, st, pub, nightStatePreshow)
			pub.resultsByNode["node-b:audio.session.prepare"] = refusedResultForAction("prepare", "the file is still being read")
			h.nightAdvanceBackgroundAudio(context.Background(), testNow, rec) // apply
			holder, other := make(chan struct{}), make(chan struct{})
			pub.blockUntilByNode = map[string]<-chan struct{}{
				"node-a:audio.session.prepare": holder,
				"node-b:audio.session.prepare": other,
			}
			time.AfterFunc(holderAnswers, func() { close(holder) })
			if tc.failsAt > 0 {
				time.AfterFunc(tc.failsAt, func() { close(other) })
			} else {
				t.Cleanup(func() {
					close(other)
					time.Sleep(200 * time.Millisecond) // let the abandoned dispatch finish before the store closes.
				})
			}

			h.nightAdvanceBackgroundAudio(context.Background(), testNow, rec) // gain, prepare, start

			at, ok := bedStartInstant(t, pub, "node-a")
			if !ok {
				t.Fatalf("node-a start carries no instant, want the shared one")
			}
			// The reading is 0.3 s old when the holder answers, and the
			// start goes out once the other node has been waited for.
			sinceReading := tc.heldBack - holderAnswers
			lead := time.Duration(at - bedStartClockReading)
			if ahead := lead - sinceReading; ahead < 700*time.Millisecond {
				t.Fatalf("instant leads the reading by %s, but the start goes out %s after it: only %s ahead, want about 750ms", lead, sinceReading, ahead)
			}
			if _, ok := bedStartInstant(t, pub, "node-b"); ok {
				t.Fatalf("node-b start carries an instant, want none (its prepare did not confirm)")
			}
			history, err := h.nightBackgroundAudioHistory(context.Background(), rec)
			if err != nil {
				t.Fatalf("history: %v", err)
			}
			latestA, _ := nightBackgroundAudioLatestStepForNode(history, "node-a")
			if latestA.Step.Kind != nightBGStepStart || latestA.Row.Outcome != nightCueOutcomeConfirmed || !strings.Contains(latestA.Row.OutcomeReason, "bed-aligned start") {
				t.Fatalf("node-a latest step = %s %s %q, want a confirmed aligned start", latestA.Step.Kind, latestA.Row.Outcome, latestA.Row.OutcomeReason)
			}
		})
	}
}

// TestNightBedStart_StartRevisionExceedsAPrepareStillInFlight proves a
// start's revision passes its node's prepare revision even when that
// prepare has not completed, so nothing has recorded the revision yet.
func TestNightBedStart_StartRevisionExceedsAPrepareStillInFlight(t *testing.T) {
	h, st, pub, _ := nightBackgroundAudioTestHandlers(t)
	rec := twoNodeBedForStart(t, st, pub, nightStatePreshow)
	sessionID := nightBackgroundAudioSessionID(rec)
	sched := nightBedScheduleResult{
		UnalignedReason: nightBedNotLoadedUnalignedReason,
		prepares:        map[string]nightBedPrepare{"node-b": {revision: 41}},
	}

	h.nightBackgroundAudioStartScheduled(context.Background(), testNow, rec, "node-b", sessionID, sched, nil, nil)

	start, ok := dispatchedByNodeAction(pub, "node-b", "audio.session.start")
	if !ok {
		t.Fatal("node-b: no audio.session.start dispatched")
	}
	if got := start["revision"].(float64); got != 42 {
		t.Fatalf("node-b start revision = %v, want 42 (one past its prepare)", got)
	}
}
