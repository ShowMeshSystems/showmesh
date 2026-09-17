package api

import (
	"context"
	"encoding/json"
	"sort"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/showmeshsystems/showmesh/internal/coordinator/config"
	"github.com/showmeshsystems/showmesh/internal/coordinator/store"
	pkgaudio "github.com/showmeshsystems/showmesh/pkg/audio"
	"github.com/showmeshsystems/showmesh/pkg/cueactivation"
	"github.com/showmeshsystems/showmesh/pkg/mqttproto"
	"github.com/showmeshsystems/showmesh/pkg/observation"
)

// This file proves ADR-049 decisions 7-9's multi-node bed runtime: the
// shared start/resume instant (decisions 3, 4, 6), the program+ltc bookmark
// push (decision 8), and regression coverage for an untargeted bed.

// multiNodeBedConfig builds a declared-Targets bed (ADR-049 decision 7)
// naming targets, whose items are all registered against registeredOn -
// decision 7's own point that an item's own Asset.Target no longer decides
// WHERE it plays, only which registered copy of the file is canonical.
func multiNodeBedConfig(registeredOn string, targets ...string) *config.NightSessionBackgroundAudio {
	return &config.NightSessionBackgroundAudio{
		Items: []config.NightSessionBackgroundAudioItem{
			{ItemID: "track-1", Asset: config.NightSessionAssetRef{Show: "halloween", Sequence: "bg-1", Target: registeredOn}},
			{ItemID: "track-2", Asset: config.NightSessionAssetRef{Show: "halloween", Sequence: "bg-2", Target: registeredOn}},
		},
		Repeat: config.NightSessionBackgroundRepeatPlaylist, Resume: config.NightSessionBackgroundResumeResume,
		ItemTransition: config.NightSessionItemTransitionSequential, MaxGainDb: -10,
		Targets: targets,
	}
}

// driveNightAdvanceBackgroundAudioUntilStable ticks h.nightAdvanceBackgroundAudio
// at testNow until a tick dispatches nothing new (or maxTicks is reached),
// so a test need not hand-count exactly how many ticks a multi-node bed's
// per-node convergence takes.
func driveNightAdvanceBackgroundAudioUntilStable(t *testing.T, h *handlers, pub *fakeAudioPublisher, rec store.NightSessionRecord, maxTicks int) {
	t.Helper()
	driveNightAdvanceBackgroundAudioUntilStableAt(t, h, pub, rec, testNow, maxTicks)
}

// driveNightAdvanceBackgroundAudioUntilStableAt is
// [driveNightAdvanceBackgroundAudioUntilStable]'s own now-parameterized
// form: a test driving a bed through two distinct schedule reads (a start,
// then a later resume) must advance now between them, exactly as a real
// overnight tick loop would - the probe session's own invocationId
// (readScheduleProbe, cueactivationschedule.go) is derived from (nodeID,
// now) alone, so two reads sharing the identical instant collide.
func driveNightAdvanceBackgroundAudioUntilStableAt(t *testing.T, h *handlers, pub *fakeAudioPublisher, rec store.NightSessionRecord, now time.Time, maxTicks int) {
	t.Helper()
	for i := 0; i < maxTicks; i++ {
		before := len(pub.dispatchedSnapshot())
		h.nightAdvanceBackgroundAudio(context.Background(), now, rec)
		if len(pub.dispatchedSnapshot()) == before {
			return
		}
	}
	t.Fatalf("nightAdvanceBackgroundAudio did not stabilize within %d ticks", maxTicks)
}

// pauseResultWithBookmark builds the audio.session.pause result ADR-049
// decision 8's own NEW evidence keys ([pkgaudio.ResultBookmarkKnown] and
// siblings) ride, exactly as nightBedBookmarkFromEvidence decodes them.
func pauseResultWithBookmark(known bool, itemID string, index int, positionMs int64) mqttproto.ResultPayload {
	return mqttproto.ResultPayload{
		Outcome: mqttproto.OutcomeConfirmed,
		Evidence: &mqttproto.ResultEvidence{
			Signal: "audio.session",
			Value: map[string]any{
				"outcome":                         "started",
				pkgaudio.ResultBookmarkKnown:      known,
				pkgaudio.ResultBookmarkItemID:     itemID,
				pkgaudio.ResultBookmarkIndex:      json.Number(strconv.Itoa(index)),
				pkgaudio.ResultBookmarkPositionMs: json.Number(strconv.FormatInt(positionMs, 10)),
			},
		},
	}
}

// dispatchedByNodeAction narrows a fakeAudioPublisher's own dispatched
// snapshot to nodeID/action, returning params of the LAST such dispatch.
func dispatchedByNodeAction(pub *fakeAudioPublisher, nodeID, action string) (map[string]any, bool) {
	var params map[string]any
	found := false
	for _, d := range pub.dispatchedSnapshot() {
		if d.NodeID == nodeID && d.Action == action {
			params, found = d.Params, true
		}
	}
	return params, found
}

func countDispatchedAction(pub *fakeAudioPublisher, action string) int {
	n := 0
	for _, d := range pub.dispatchedSnapshot() {
		if d.Action == action {
			n++
		}
	}
	return n
}

// countDispatchedActionForSession is [countDispatchedAction] narrowed to
// dispatches carrying params.sessionId == sessionID: a bed schedule read
// dispatches its own apply/prepare/clear against
// [cueactivation.ScheduleProbeSessionID], never the bed's own real
// session, so a count scoped to the bed session is what proves nothing
// beyond it changed.
func countDispatchedActionForSession(pub *fakeAudioPublisher, action, sessionID string) int {
	n := 0
	for _, d := range pub.dispatchedSnapshot() {
		if d.Action == action && d.Params["sessionId"] == sessionID {
			n++
		}
	}
	return n
}

// TestNightAdvanceMultiNodeBackgroundAudio_AppliesCompleteItemListToBothNodes
// proves decision 7 directly: both items are registered against node-a
// alone, but a bed declaring [node-a, node-b] as Targets applies the
// COMPLETE item list to both nodes, node-b included, since every listed
// node plays every item regardless of which node's own copy is canonical.
func TestNightAdvanceMultiNodeBackgroundAudio_AppliesCompleteItemListToBothNodes(t *testing.T) {
	h, st, pub, _ := nightBackgroundAudioTestHandlers(t)
	putBackgroundAudioAsset(t, st, "halloween", "bg-1", "node-a", "asset-1")
	putBackgroundAudioAsset(t, st, "halloween", "bg-2", "node-a", "asset-2")
	ba := multiNodeBedConfig("node-a", "node-a", "node-b")
	rec := mustCreateRestingSessionWithBackgroundAudio(t, st, "sess-1", "node-a", ba, nightStateRestingIntershow)

	h.nightAdvanceBackgroundAudio(context.Background(), testNow, rec)

	for _, nodeID := range []string{"node-a", "node-b"} {
		params, ok := dispatchedByNodeAction(pub, nodeID, "audio.session.apply")
		if !ok {
			t.Fatalf("node %q: no audio.session.apply dispatched", nodeID)
		}
		playlist, ok := params["playlist"].(map[string]any)
		if !ok {
			t.Fatalf("node %q: params.playlist = %v, want a JSON object", nodeID, params["playlist"])
		}
		items, ok := playlist["items"].([]any)
		if !ok || len(items) != 2 {
			t.Fatalf("node %q: playlist.items = %v, want 2 items (the complete list, including items registered only for node-a)", nodeID, playlist["items"])
		}
	}
}

// TestNightAdvanceMultiNodeBackgroundAudio_ReferenceFormAppliesCompleteItemListToBothNodes
// is the same proof for the reference form: Targets lives on the SESSION's
// own outer resting.backgroundAudio wrapper (a media.playlist object
// carries no targets field of its own), so nightMediaPlaylistBackgroundAudio
// must carry it through into the resolved struct every node's own apply is
// built from.
func TestNightAdvanceMultiNodeBackgroundAudio_ReferenceFormAppliesCompleteItemListToBothNodes(t *testing.T) {
	h, st, pub, _ := nightBackgroundAudioTestHandlers(t)
	putBackgroundAudioAsset(t, st, "halloween", "bg-1", "node-a", "asset-1")
	putBackgroundAudioAsset(t, st, "halloween", "bg-2", "node-a", "asset-2")
	mustCreateMediaPlaylist(t, st, "planetary-bed", twoItemMediaPlaylistPayload("halloween", "node-a",
		config.NightSessionBackgroundRepeatPlaylist, config.NightSessionBackgroundResumeResume, config.NightSessionItemTransitionSequential))
	ba := &config.NightSessionBackgroundAudio{MediaPlaylist: "planetary-bed", Targets: []string{"node-a", "node-b"}}
	rec := mustCreateRestingSessionWithBackgroundAudio(t, st, "sess-1", "node-a", ba, nightStateRestingIntershow)

	h.nightAdvanceBackgroundAudio(context.Background(), testNow, rec)

	params, ok := dispatchedByNodeAction(pub, "node-b", "audio.session.apply")
	if !ok {
		t.Fatalf("node-b: no audio.session.apply dispatched")
	}
	playlist, ok := params["playlist"].(map[string]any)
	if !ok {
		t.Fatalf("node-b: params.playlist = %v, want a JSON object", params["playlist"])
	}
	if playlist["ownerKind"] != config.MediaPlaylistConfigKind || playlist["ownerId"] != "planetary-bed" {
		t.Fatalf("node-b: playlist owner = %v/%v, want %q/%q", playlist["ownerKind"], playlist["ownerId"], config.MediaPlaylistConfigKind, "planetary-bed")
	}
	items, ok := playlist["items"].([]any)
	if !ok || len(items) != 2 {
		t.Fatalf("node-b: playlist.items = %v, want 2 items from the referenced media.playlist", playlist["items"])
	}
}

// TestNightAdvanceMultiNodeBackgroundAudio_SharedStartInstantOneRead proves
// ADR-049 decisions 3, 4, and 6: a shared first start reads the clock once,
// dispatches identical scheduledAtNs to both nodes, and a replay reads none.
func TestNightAdvanceMultiNodeBackgroundAudio_SharedStartInstantOneRead(t *testing.T) {
	h, st, pub, _ := nightBackgroundAudioTestHandlers(t)
	putBackgroundAudioAsset(t, st, "halloween", "bg-1", "node-a", "asset-1")
	putBackgroundAudioAsset(t, st, "halloween", "bg-2", "node-a", "asset-2")
	putAudioNodeForTest(t, st, "node-a")
	putAudioNodeNoLTCForTest(t, st, "node-b")
	ba := multiNodeBedConfig("node-a", "node-a", "node-b")
	rec := mustCreateRestingSessionWithBackgroundAudio(t, st, "sess-1", "node-a", ba, nightStateRestingIntershow)

	pub.result = confirmedResultForAction("x", nightBackgroundAudioSessionID(rec), "started")
	const clockReading = int64(1_700_000_000_000_000_000)
	pub.resultsByNode = map[string]mqttproto.ResultPayload{
		"node-a:audio.session.prepare": scheduleProbeEvidenceResult(true, clockReading, ""),
	}

	driveNightAdvanceBackgroundAudioUntilStable(t, h, pub, rec, 10)

	if got := countDispatchedAction(pub, "audio.session.prepare"); got != 1 {
		t.Fatalf("audio.session.prepare dispatch count = %d, want exactly 1 (one schedule read)", got)
	}
	startA, okA := dispatchedByNodeAction(pub, "node-a", "audio.session.start")
	startB, okB := dispatchedByNodeAction(pub, "node-b", "audio.session.start")
	if !okA || !okB {
		t.Fatalf("starts dispatched: node-a=%v node-b=%v, want both", okA, okB)
	}
	atA, presentA := startA[pkgaudio.ParamScheduledAtNs]
	atB, presentB := startB[pkgaudio.ParamScheduledAtNs]
	if !presentA || !presentB {
		t.Fatalf("scheduledAtNs present: node-a=%v node-b=%v, want both present", presentA, presentB)
	}
	if atA != atB {
		t.Fatalf("scheduledAtNs differ: node-a=%v node-b=%v, want identical (one shared instant)", atA, atB)
	}

	history, err := h.nightBackgroundAudioHistory(context.Background(), rec)
	if err != nil {
		t.Fatalf("history: %v", err)
	}
	for _, nodeID := range []string{"node-a", "node-b"} {
		latest, ok := nightBackgroundAudioLatestStepForNode(history, nodeID)
		if !ok || latest.Step.Kind != nightBGStepStart || latest.Row.Outcome != nightCueOutcomeConfirmed {
			t.Fatalf("node %q latest step = %+v, want a confirmed start", nodeID, latest)
		}
	}

	// A replay tick: no further clock read, nothing new dispatched.
	beforePrepare := countDispatchedAction(pub, "audio.session.prepare")
	beforeTotal := len(pub.dispatchedSnapshot())
	h.nightAdvanceBackgroundAudio(context.Background(), testNow, rec)
	if got := countDispatchedAction(pub, "audio.session.prepare"); got != beforePrepare {
		t.Fatalf("replay tick dispatched another audio.session.prepare: count = %d, want unchanged at %d", got, beforePrepare)
	}
	if got := len(pub.dispatchedSnapshot()); got != beforeTotal {
		t.Fatalf("replay tick dispatched %d new command(s), want 0", got-beforeTotal)
	}
}

// TestNightAdvanceMultiNodeBackgroundAudio_NoUsableClockStartsOnArrival
// proves decision 4's own fallback for a bed's first start: with no listed
// node holding program+ltc, every node still starts (on arrival, no
// scheduledAtNs), and the outcome is recorded unaligned with a reason.
func TestNightAdvanceMultiNodeBackgroundAudio_NoUsableClockStartsOnArrival(t *testing.T) {
	h, st, pub, _ := nightBackgroundAudioTestHandlers(t)
	putBackgroundAudioAsset(t, st, "halloween", "bg-1", "node-a", "asset-1")
	putBackgroundAudioAsset(t, st, "halloween", "bg-2", "node-a", "asset-2")
	putAudioNodeNoLTCForTest(t, st, "node-a")
	putAudioNodeNoLTCForTest(t, st, "node-b")
	ba := multiNodeBedConfig("node-a", "node-a", "node-b")
	rec := mustCreateRestingSessionWithBackgroundAudio(t, st, "sess-1", "node-a", ba, nightStateRestingIntershow)

	pub.result = confirmedResultForAction("x", nightBackgroundAudioSessionID(rec), "started")
	driveNightAdvanceBackgroundAudioUntilStable(t, h, pub, rec, 10)

	if got := countDispatchedAction(pub, "audio.session.prepare"); got != 0 {
		t.Fatalf("audio.session.prepare dispatch count = %d, want 0 (no listed node holds a usable clock)", got)
	}
	for _, nodeID := range []string{"node-a", "node-b"} {
		params, ok := dispatchedByNodeAction(pub, nodeID, "audio.session.start")
		if !ok {
			t.Fatalf("node %q: no audio.session.start dispatched", nodeID)
		}
		if _, present := params[pkgaudio.ParamScheduledAtNs]; present {
			t.Fatalf("node %q: start params = %v, want no scheduledAtNs (unaligned, on arrival)", nodeID, params)
		}
	}

	history, err := h.nightBackgroundAudioHistory(context.Background(), rec)
	if err != nil {
		t.Fatalf("history: %v", err)
	}
	latest, ok := nightBackgroundAudioLatestStepForNode(history, "node-a")
	if !ok || latest.Step.Kind != nightBGStepStart || latest.Row.OutcomeReason == "" {
		t.Fatalf("node-a latest step = %+v, want a start recording a non-empty unaligned reason", latest)
	}
}

// TestNightAdvanceMultiNodeBackgroundAudio_OneNodeRefusingStartLeavesOtherStarted
// proves decision 4's other half: one node's refusal to start never stops
// the other's, and each outcome is reported against its own node.
func TestNightAdvanceMultiNodeBackgroundAudio_OneNodeRefusingStartLeavesOtherStarted(t *testing.T) {
	h, st, pub, _ := nightBackgroundAudioTestHandlers(t)
	putBackgroundAudioAsset(t, st, "halloween", "bg-1", "node-a", "asset-1")
	putBackgroundAudioAsset(t, st, "halloween", "bg-2", "node-a", "asset-2")
	putAudioNodeForTest(t, st, "node-a")
	putAudioNodeNoLTCForTest(t, st, "node-b")
	ba := multiNodeBedConfig("node-a", "node-a", "node-b")
	rec := mustCreateRestingSessionWithBackgroundAudio(t, st, "sess-1", "node-a", ba, nightStateRestingIntershow)

	pub.result = confirmedResultForAction("x", nightBackgroundAudioSessionID(rec), "started")
	const clockReading = int64(1_700_000_000_000_000_000)
	pub.resultsByNode = map[string]mqttproto.ResultPayload{
		"node-a:audio.session.prepare": scheduleProbeEvidenceResult(true, clockReading, ""),
		"node-b:audio.session.start":   refusedResultForAction("start", "node-b refuses this start"),
	}

	driveNightAdvanceBackgroundAudioUntilStable(t, h, pub, rec, 10)

	history, err := h.nightBackgroundAudioHistory(context.Background(), rec)
	if err != nil {
		t.Fatalf("history: %v", err)
	}
	latestA, okA := nightBackgroundAudioLatestStepForNode(history, "node-a")
	latestB, okB := nightBackgroundAudioLatestStepForNode(history, "node-b")
	if !okA || latestA.Step.Kind != nightBGStepStart || latestA.Row.Outcome != nightCueOutcomeConfirmed {
		t.Fatalf("node-a latest step = %+v, want a confirmed start", latestA)
	}
	if !okB || latestB.Step.Kind != nightBGStepStart || latestB.Row.Outcome == nightCueOutcomeConfirmed {
		t.Fatalf("node-b latest step = %+v, want its own start resolved but NOT confirmed", latestB)
	}
}

// TestNightAdvanceMultiNodeBackgroundAudio_SingleTargetDispatchesNoScheduleStep
// proves a single-target bed never engages ADR-049 decisions 3, 4, and 6's
// scheduling: no audio.session.prepare is dispatched, same as no Targets.
func TestNightAdvanceMultiNodeBackgroundAudio_SingleTargetDispatchesNoScheduleStep(t *testing.T) {
	h, st, pub, _ := nightBackgroundAudioTestHandlers(t)
	putBackgroundAudioAsset(t, st, "halloween", "bg-1", "node-a", "asset-1")
	putBackgroundAudioAsset(t, st, "halloween", "bg-2", "node-a", "asset-2")
	putAudioNodeForTest(t, st, "node-a")
	ba := multiNodeBedConfig("node-a", "node-a")
	rec := mustCreateRestingSessionWithBackgroundAudio(t, st, "sess-1", "node-a", ba, nightStateRestingIntershow)

	pub.result = confirmedResultForAction("x", nightBackgroundAudioSessionID(rec), "started")
	driveNightAdvanceBackgroundAudioUntilStable(t, h, pub, rec, 10)

	if got := countDispatchedAction(pub, "audio.session.prepare"); got != 0 {
		t.Fatalf("audio.session.prepare dispatch count = %d, want 0 (a single-target bed dispatches no schedule step)", got)
	}
	params, ok := dispatchedByNodeAction(pub, "node-a", "audio.session.start")
	if !ok {
		t.Fatalf("node-a: no audio.session.start dispatched")
	}
	if _, present := params[pkgaudio.ParamScheduledAtNs]; present {
		t.Fatalf("node-a: start params = %v, want no scheduledAtNs (single-target beds are unscheduled)", params)
	}
}

// TestNightAdvanceBackgroundAudio_NoDeclaredTargetsRegression proves the
// regression rule directly: a bed whose two items name distinct targets,
// with NO declared Targets list, dispatches no schedule step at all -
// decisions 8 and 9 never engage for it, exactly as before this task.
func TestNightAdvanceBackgroundAudio_NoDeclaredTargetsRegression(t *testing.T) {
	h, st, pub, _ := nightBackgroundAudioTestHandlers(t)
	putBackgroundAudioAsset(t, st, "halloween", "bg-a", "node-a", "asset-a")
	putBackgroundAudioAsset(t, st, "halloween", "bg-b", "node-b", "asset-b")
	putAudioNodeForTest(t, st, "node-a")
	putAudioNodeNoLTCForTest(t, st, "node-b")
	ba := twoNodeBackgroundAudioConfig("node-a", "node-b")
	rec := mustCreateRestingSessionWithBackgroundAudio(t, st, "sess-1", "node-a", ba, nightStateRestingIntershow)

	pub.result = confirmedResultForAction("x", nightBackgroundAudioSessionID(rec), "started")
	driveNightAdvanceBackgroundAudioUntilStable(t, h, pub, rec, 10)

	if got := countDispatchedAction(pub, "audio.session.prepare"); got != 0 {
		t.Fatalf("audio.session.prepare dispatch count = %d, want 0 (no declared Targets: unchanged, unscheduled behavior)", got)
	}
	for _, nodeID := range []string{"node-a", "node-b"} {
		params, ok := dispatchedByNodeAction(pub, nodeID, "audio.session.start")
		if !ok {
			t.Fatalf("node %q: no audio.session.start dispatched", nodeID)
		}
		if _, present := params[pkgaudio.ParamScheduledAtNs]; present {
			t.Fatalf("node %q: start params = %v, want no scheduledAtNs", nodeID, params)
		}
	}
}

// twoNodeMultiNodeBedThroughStart drives a two-target bed (node-a holds
// program+ltc) all the way to a confirmed shared start, returning the
// session record advanced into a NEW cycle (mirroring how nightTick
// increments Cycle on entering transition-to-show, BEFORE the bed's own
// suspend is dispatched) so a subsequent pause/resume round records its
// own outbox rows distinctly from the start's.
func twoNodeMultiNodeBedThroughStart(t *testing.T, h *handlers, st *store.Store, pub *fakeAudioPublisher) (rec store.NightSessionRecord) {
	t.Helper()
	putBackgroundAudioAsset(t, st, "halloween", "bg-1", "node-a", "asset-1")
	putBackgroundAudioAsset(t, st, "halloween", "bg-2", "node-a", "asset-2")
	putAudioNodeForTest(t, st, "node-a")
	putAudioNodeNoLTCForTest(t, st, "node-b")
	ba := multiNodeBedConfig("node-a", "node-a", "node-b")
	rec = mustCreateRestingSessionWithBackgroundAudio(t, st, "sess-1", "node-a", ba, nightStateRestingIntershow)

	pub.result = confirmedResultForAction("x", nightBackgroundAudioSessionID(rec), "started")
	const clockReading = int64(1_700_000_000_000_000_000)
	pub.resultsByNode = map[string]mqttproto.ResultPayload{
		"node-a:audio.session.prepare": scheduleProbeEvidenceResult(true, clockReading, ""),
	}
	driveNightAdvanceBackgroundAudioUntilStable(t, h, pub, rec, 10)

	next := rec
	next.Cycle = rec.Cycle + 1
	return next
}

// TestNightAdvanceMultiNodeBackgroundAudio_ResumeSendsSharedBookmarkAndInstant
// proves the program+ltc node's own pause bookmark travels on
// audio.session.resume itself, to every listed node, at one shared instant.
func TestNightAdvanceMultiNodeBackgroundAudio_ResumeSendsSharedBookmarkAndInstant(t *testing.T) {
	h, st, pub, _ := nightBackgroundAudioTestHandlers(t)
	rec := twoNodeMultiNodeBedThroughStart(t, h, st, pub)
	sessionID := nightBackgroundAudioSessionID(rec)

	applyCountBeforeResume := countDispatchedActionForSession(pub, "audio.session.apply", sessionID)

	pub.resultsByNode = map[string]mqttproto.ResultPayload{
		"node-a:audio.session.pause": pauseResultWithBookmark(true, "track-2", 1, 4500),
	}
	h.nightStopBackgroundAudioIfRunning(context.Background(), testNow, rec)

	const resumeClockReading = int64(1_800_000_000_000_000_000)
	pub.resultsByNode = map[string]mqttproto.ResultPayload{
		"node-a:audio.session.prepare": scheduleProbeEvidenceResult(true, resumeClockReading, ""),
	}
	// A later instant than the start phase's own testNow: two genuinely
	// distinct schedule reads for the SAME node must never share a probe
	// invocationId (readScheduleProbe derives it from (nodeID, now) alone).
	driveNightAdvanceBackgroundAudioUntilStableAt(t, h, pub, rec, testNow.Add(time.Hour), 10)

	if got := countDispatchedActionForSession(pub, "audio.session.apply", sessionID); got != applyCountBeforeResume {
		t.Fatalf("audio.session.apply dispatch count on the bed session went from %d to %d, want unchanged (the resume point travels on resume itself, never a separate apply push)", applyCountBeforeResume, got)
	}

	resumeA, okA := dispatchedByNodeAction(pub, "node-a", "audio.session.resume")
	resumeB, okB := dispatchedByNodeAction(pub, "node-b", "audio.session.resume")
	if !okA || !okB {
		t.Fatalf("resumes dispatched: node-a=%v node-b=%v, want both", okA, okB)
	}
	for nodeID, params := range map[string]map[string]any{"node-a": resumeA, "node-b": resumeB} {
		if params[pkgaudio.ParamResumeItemID] != "track-2" {
			t.Fatalf("node %q: resume params = %v, want resumeItemId=track-2", nodeID, params)
		}
		index, _ := evidenceInt64(params[pkgaudio.ParamResumeIndex])
		if index != 1 {
			t.Fatalf("node %q: resume params = %v, want resumeIndex=1", nodeID, params)
		}
		positionMs, _ := evidenceInt64(params[pkgaudio.ParamResumePositionMs])
		if positionMs != 4500 {
			t.Fatalf("node %q: resume params = %v, want resumePositionMs=4500", nodeID, params)
		}
	}
	atA, presentA := resumeA[pkgaudio.ParamScheduledAtNs]
	atB, presentB := resumeB[pkgaudio.ParamScheduledAtNs]
	if !presentA || !presentB {
		t.Fatalf("scheduledAtNs present on resume: node-a=%v node-b=%v, want both present", presentA, presentB)
	}
	if atA != atB {
		t.Fatalf("resume scheduledAtNs differ: node-a=%v node-b=%v, want identical", atA, atB)
	}
	if got := countDispatchedAction(pub, "audio.session.prepare"); got != 2 {
		t.Fatalf("audio.session.prepare dispatch count = %d, want 2 (one for the first start, one for the resume)", got)
	}
}

// TestNightBedScheduleReadNeverTouchesTheBedSessionOnStartOrResume is
// Defect B's own regression guard. nightComputeBedSchedule used to read
// the clock by preparing the REAL bed session directly
// (audio.session.prepare against nightBackgroundAudioSessionID(rec)), on
// both the shared start and the shared resume - and the agent's own
// Manager.Prepare (internal/agent/audio/manager.go) releases the engine
// and marks the session Ready, ending a just-confirmed pause. This proves
// both reads instead go through readScheduleProbe's own dedicated
// [cueactivation.ScheduleProbeSessionID], never the bed's own session id,
// and that the paused bed session sees no prepare of any kind between its
// own confirmed pause and its own confirmed resume.
func TestNightBedScheduleReadNeverTouchesTheBedSessionOnStartOrResume(t *testing.T) {
	h, st, pub, _ := nightBackgroundAudioTestHandlers(t)
	putBackgroundAudioAsset(t, st, "halloween", "bg-1", "node-a", "asset-1")
	putBackgroundAudioAsset(t, st, "halloween", "bg-2", "node-a", "asset-2")
	putAudioNodeForTest(t, st, "node-a")
	putAudioNodeNoLTCForTest(t, st, "node-b")
	ba := multiNodeBedConfig("node-a", "node-a", "node-b")
	rec := mustCreateRestingSessionWithBackgroundAudio(t, st, "sess-1", "node-a", ba, nightStateRestingIntershow)
	bedSessionID := nightBackgroundAudioSessionID(rec)

	assertNoScheduleReadTouchesSession := func(from int) {
		t.Helper()
		for _, d := range pub.dispatchedSnapshot()[from:] {
			isScheduleReadAction := d.Action == "audio.session.apply" || d.Action == "audio.session.prepare" || d.Action == "audio.session.clear"
			if isScheduleReadAction && d.Params["sessionId"] == bedSessionID {
				// The bed's own real apply (playlist load) also uses this
				// action name; narrow to the probe's own giveaway shape (no
				// playlist, a bare media object) to avoid a false positive
				// against that legitimate dispatch.
				if _, hasPlaylist := d.Params["playlist"]; !hasPlaylist {
					t.Fatalf("schedule read dispatched %s against the bed's own session %q; the clock read must use the dedicated probe session, never the bed session (params=%v)", d.Action, bedSessionID, d.Params)
				}
			}
		}
	}

	pub.result = confirmedResultForAction("x", bedSessionID, "started")
	const startClockReading = int64(1_700_000_000_000_000_000)
	pub.resultsByNode = map[string]mqttproto.ResultPayload{
		"node-a:audio.session.prepare": scheduleProbeEvidenceResult(true, startClockReading, ""),
	}
	driveNightAdvanceBackgroundAudioUntilStable(t, h, pub, rec, 10)
	assertNoScheduleReadTouchesSession(0)

	startA, ok := dispatchedByNodeAction(pub, "node-a", "audio.session.start")
	if !ok {
		t.Fatalf("node-a: no audio.session.start dispatched")
	}
	if _, present := startA[pkgaudio.ParamScheduledAtNs]; !present {
		t.Fatalf("node-a start params = %v, want scheduledAtNs present (the start's own schedule read must have succeeded through the probe)", startA)
	}
	if got := countDispatchedActionForSession(pub, "audio.session.prepare", cueactivation.ScheduleProbeSessionID); got != 1 {
		t.Fatalf("audio.session.prepare on the probe session = %d, want exactly 1 (one schedule read for the start)", got)
	}

	rec.Cycle++
	pub.resultsByNode = map[string]mqttproto.ResultPayload{
		"node-a:audio.session.pause": pauseResultWithBookmark(true, "track-2", 1, 4500),
	}
	h.nightStopBackgroundAudioIfRunning(context.Background(), testNow, rec)
	if _, ok := dispatchedByNodeAction(pub, "node-a", "audio.session.pause"); !ok {
		t.Fatalf("node-a: no audio.session.pause dispatched")
	}
	dispatchedBeforeResume := len(pub.dispatchedSnapshot())

	const resumeClockReading = int64(1_800_000_000_000_000_000)
	pub.resultsByNode = map[string]mqttproto.ResultPayload{
		"node-a:audio.session.prepare": scheduleProbeEvidenceResult(true, resumeClockReading, ""),
	}
	// A later instant than the start phase's own testNow: see
	// driveNightAdvanceBackgroundAudioUntilStableAt's own doc comment.
	driveNightAdvanceBackgroundAudioUntilStableAt(t, h, pub, rec, testNow.Add(time.Hour), 10)

	// The core of Defect B: between the confirmed pause above and here, not
	// one prepare (of any kind, on any session) may have reached the
	// PAUSED bed session - that is exactly what silently un-pauses it.
	for _, d := range pub.dispatchedSnapshot()[dispatchedBeforeResume:] {
		if d.Action == "audio.session.prepare" && d.Params["sessionId"] == bedSessionID {
			t.Fatalf("a prepare was dispatched against the paused bed session %q between pause and resume; this is exactly Defect B (internal/agent/audio.Manager.Prepare ends a paused session)", bedSessionID)
		}
	}
	assertNoScheduleReadTouchesSession(dispatchedBeforeResume)

	resumeA, ok := dispatchedByNodeAction(pub, "node-a", "audio.session.resume")
	if !ok {
		t.Fatalf("node-a: no audio.session.resume dispatched")
	}
	if _, present := resumeA[pkgaudio.ParamScheduledAtNs]; !present {
		t.Fatalf("node-a resume params = %v, want scheduledAtNs present (the resume's own schedule read must have succeeded through the probe)", resumeA)
	}
	if got := countDispatchedActionForSession(pub, "audio.session.prepare", cueactivation.ScheduleProbeSessionID); got != 2 {
		t.Fatalf("audio.session.prepare on the probe session = %d, want exactly 2 (one for the start, one for the resume)", got)
	}
}

// TestNightAdvanceMultiNodeBackgroundAudio_ResumeUnknownBookmarkOnArrival
// proves ADR-049 decision 8's fallback: with no bookmark evidence, every
// node resumes on arrival from its own bookmark, recorded unaligned.
func TestNightAdvanceMultiNodeBackgroundAudio_ResumeUnknownBookmarkOnArrival(t *testing.T) {
	h, st, pub, _ := nightBackgroundAudioTestHandlers(t)
	rec := twoNodeMultiNodeBedThroughStart(t, h, st, pub)

	pub.resultsByNode = map[string]mqttproto.ResultPayload{
		"node-a:audio.session.pause": pauseResultWithBookmark(false, "", 0, 0),
	}
	h.nightStopBackgroundAudioIfRunning(context.Background(), testNow, rec)
	pub.resultsByNode = nil

	prepareCountBeforeResume := countDispatchedAction(pub, "audio.session.prepare")
	applyCountBeforeResume := countDispatchedAction(pub, "audio.session.apply")
	driveNightAdvanceBackgroundAudioUntilStable(t, h, pub, rec, 10)

	if got := countDispatchedAction(pub, "audio.session.prepare"); got != prepareCountBeforeResume {
		t.Fatalf("audio.session.prepare dispatch count went from %d to %d, want unchanged (unknown bookmark: no schedule read attempted)", prepareCountBeforeResume, got)
	}
	if got := countDispatchedAction(pub, "audio.session.apply"); got != applyCountBeforeResume {
		t.Fatalf("audio.session.apply dispatch count went from %d to %d, want unchanged (unknown bookmark: no bookmark push)", applyCountBeforeResume, got)
	}
	for _, nodeID := range []string{"node-a", "node-b"} {
		params, ok := dispatchedByNodeAction(pub, nodeID, "audio.session.resume")
		if !ok {
			t.Fatalf("node %q: no audio.session.resume dispatched", nodeID)
		}
		if _, present := params[pkgaudio.ParamScheduledAtNs]; present {
			t.Fatalf("node %q: resume params = %v, want no scheduledAtNs", nodeID, params)
		}
	}
	history, err := h.nightBackgroundAudioHistory(context.Background(), rec)
	if err != nil {
		t.Fatalf("history: %v", err)
	}
	latest, ok := nightBackgroundAudioLatestStepForNode(history, "node-a")
	if !ok || latest.Step.Kind != nightBGStepResume || latest.Row.OutcomeReason == "" {
		t.Fatalf("node-a latest step = %+v, want a resume recording a non-empty unaligned reason", latest)
	}
}

// TestNightAdvanceMultiNodeBackgroundAudio_ResumeNoProgramLTCTargetOnArrival
// proves ADR-049 decision 8's other fallback: no listed node holds the
// program+ltc role at all, so resume falls back exactly the same way.
func TestNightAdvanceMultiNodeBackgroundAudio_ResumeNoProgramLTCTargetOnArrival(t *testing.T) {
	h, st, pub, _ := nightBackgroundAudioTestHandlers(t)
	putBackgroundAudioAsset(t, st, "halloween", "bg-1", "node-a", "asset-1")
	putBackgroundAudioAsset(t, st, "halloween", "bg-2", "node-a", "asset-2")
	putAudioNodeNoLTCForTest(t, st, "node-a")
	putAudioNodeNoLTCForTest(t, st, "node-b")
	ba := multiNodeBedConfig("node-a", "node-a", "node-b")
	rec := mustCreateRestingSessionWithBackgroundAudio(t, st, "sess-1", "node-a", ba, nightStateRestingIntershow)
	pub.result = confirmedResultForAction("x", nightBackgroundAudioSessionID(rec), "started")
	driveNightAdvanceBackgroundAudioUntilStable(t, h, pub, rec, 10)

	rec.Cycle++
	pub.resultsByNode = map[string]mqttproto.ResultPayload{
		"node-a:audio.session.pause": pauseResultWithBookmark(true, "track-2", 1, 4500),
	}
	h.nightStopBackgroundAudioIfRunning(context.Background(), testNow, rec)
	pub.resultsByNode = nil

	applyCountBeforeResume := countDispatchedAction(pub, "audio.session.apply")
	driveNightAdvanceBackgroundAudioUntilStable(t, h, pub, rec, 10)

	if got := countDispatchedAction(pub, "audio.session.apply"); got != applyCountBeforeResume {
		t.Fatalf("audio.session.apply dispatch count went from %d to %d, want unchanged (no bookmark push: no listed node holds program+ltc)", applyCountBeforeResume, got)
	}
	for _, nodeID := range []string{"node-a", "node-b"} {
		params, ok := dispatchedByNodeAction(pub, nodeID, "audio.session.resume")
		if !ok {
			t.Fatalf("node %q: no audio.session.resume dispatched", nodeID)
		}
		if _, present := params[pkgaudio.ParamScheduledAtNs]; present {
			t.Fatalf("node %q: resume params = %v, want no scheduledAtNs", nodeID, params)
		}
	}
}

// TestNightAdvanceMultiNodeBackgroundAudio_BoundedStartThenLateArrivalUnaligned
// proves a node that never confirms its own gain never silences the bed:
// node-a starts aligned once the bound elapses; node-b starts unaligned later.
func TestNightAdvanceMultiNodeBackgroundAudio_BoundedStartThenLateArrivalUnaligned(t *testing.T) {
	h, st, pub, _ := nightBackgroundAudioTestHandlers(t)
	putBackgroundAudioAsset(t, st, "halloween", "bg-1", "node-a", "asset-1")
	putBackgroundAudioAsset(t, st, "halloween", "bg-2", "node-a", "asset-2")
	putAudioNodeForTest(t, st, "node-a")
	putAudioNodeNoLTCForTest(t, st, "node-b")
	ba := multiNodeBedConfig("node-a", "node-a", "node-b")
	rec := mustCreateRestingSessionWithBackgroundAudio(t, st, "sess-1", "node-a", ba, nightStateRestingIntershow)

	pub.result = confirmedResultForAction("x", nightBackgroundAudioSessionID(rec), "started")
	const clockReading = int64(1_700_000_000_000_000_000)
	pub.resultsByNode = map[string]mqttproto.ResultPayload{
		"node-a:audio.session.prepare": scheduleProbeEvidenceResult(true, clockReading, ""),
		"node-b:audio.gain.set":        refusedResultForAction("gain", "node-b never confirms gain"),
	}

	now := testNow
	for i := 0; i < 3; i++ {
		h.nightAdvanceBackgroundAudio(context.Background(), now, rec)
	}
	if _, ok := dispatchedByNodeAction(pub, "node-a", "audio.session.start"); ok {
		t.Fatalf("node-a started before the bound elapsed")
	}

	now = now.Add(nightBedReadyStepBound)
	h.nightAdvanceBackgroundAudio(context.Background(), now, rec)

	startA, ok := dispatchedByNodeAction(pub, "node-a", "audio.session.start")
	if !ok {
		t.Fatalf("node-a: no audio.session.start dispatched once the bound elapsed")
	}
	if _, present := startA[pkgaudio.ParamScheduledAtNs]; !present {
		t.Fatalf("node-a: start params = %v, want scheduledAtNs (node-a is the sole ready node and it holds program+ltc)", startA)
	}
	if _, ok := dispatchedByNodeAction(pub, "node-b", "audio.session.start"); ok {
		t.Fatalf("node-b started before ever confirming its own gain")
	}

	delete(pub.resultsByNode, "node-b:audio.gain.set")
	driveNightAdvanceBackgroundAudioUntilStable(t, h, pub, rec, 10)

	startB, ok := dispatchedByNodeAction(pub, "node-b", "audio.session.start")
	if !ok {
		t.Fatalf("node-b: no audio.session.start dispatched after it finally confirmed gain")
	}
	if _, present := startB[pkgaudio.ParamScheduledAtNs]; present {
		t.Fatalf("node-b: start params = %v, want no scheduledAtNs (a late arrival after the bed's shared start window)", startB)
	}

	history, err := h.nightBackgroundAudioHistory(context.Background(), rec)
	if err != nil {
		t.Fatalf("history: %v", err)
	}
	latestB, ok := nightBackgroundAudioLatestStepForNode(history, "node-b")
	if !ok || latestB.Step.Kind != nightBGStepStart || latestB.Row.Outcome != nightCueOutcomeConfirmed || latestB.Row.OutcomeReason == "" {
		t.Fatalf("node-b latest step = %+v, want a confirmed start recording a non-empty unaligned reason", latestB)
	}
}

// TestNightAdvanceMultiNodeBackgroundAudio_ProgramLTCScheduleAndStartRevisionsStrictlyIncrease
// proves node-a's own confirmed gain revision (the history maximum, after
// retrying behind node-b) still lets its prepare and start strictly increase.
func TestNightAdvanceMultiNodeBackgroundAudio_ProgramLTCScheduleAndStartRevisionsStrictlyIncrease(t *testing.T) {
	h, st, pub, _ := nightBackgroundAudioTestHandlers(t)
	putBackgroundAudioAsset(t, st, "halloween", "bg-1", "node-a", "asset-1")
	putBackgroundAudioAsset(t, st, "halloween", "bg-2", "node-a", "asset-2")
	putAudioNodeForTest(t, st, "node-a")
	putAudioNodeNoLTCForTest(t, st, "node-b")
	ba := multiNodeBedConfig("node-a", "node-a", "node-b")
	rec := mustCreateRestingSessionWithBackgroundAudio(t, st, "sess-1", "node-a", ba, nightStateRestingIntershow)

	pub.result = confirmedResultForAction("x", nightBackgroundAudioSessionID(rec), "started")
	const clockReading = int64(1_700_000_000_000_000_000)
	pub.resultsByNode = map[string]mqttproto.ResultPayload{
		"node-a:audio.session.prepare": scheduleProbeEvidenceResult(true, clockReading, ""),
		"node-a:audio.gain.set":        refusedResultForAction("gain", "node-a's own gain lags behind node-b's"),
	}

	for i := 0; i < 3; i++ {
		h.nightAdvanceBackgroundAudio(context.Background(), testNow, rec)
	}
	history, err := h.nightBackgroundAudioHistory(context.Background(), rec)
	if err != nil {
		t.Fatalf("history: %v", err)
	}
	latestA, ok := nightBackgroundAudioLatestStepForNode(history, "node-a")
	if !ok || latestA.Step.Kind != nightBGStepGain || latestA.Row.Outcome == nightCueOutcomeConfirmed {
		t.Fatalf("node-a latest step = %+v, want an unconfirmed gain (still lagging behind node-b)", latestA)
	}
	latestB, ok := nightBackgroundAudioLatestStepForNode(history, "node-b")
	if !ok || latestB.Step.Kind != nightBGStepGain || latestB.Row.Outcome != nightCueOutcomeConfirmed {
		t.Fatalf("node-b latest step = %+v, want a confirmed gain (ready ahead of node-a)", latestB)
	}

	delete(pub.resultsByNode, "node-a:audio.gain.set")
	driveNightAdvanceBackgroundAudioUntilStable(t, h, pub, rec, 10)

	prepareParams, ok := dispatchedByNodeAction(pub, "node-a", "audio.session.prepare")
	if !ok {
		t.Fatalf("node-a: no audio.session.prepare dispatched")
	}
	startParams, ok := dispatchedByNodeAction(pub, "node-a", "audio.session.start")
	if !ok {
		t.Fatalf("node-a: no audio.session.start dispatched")
	}
	prepareRev, _ := evidenceInt64(prepareParams["revision"])
	startRev, _ := evidenceInt64(startParams["revision"])
	if startRev <= prepareRev {
		t.Fatalf("node-a's own start revision (%v) is not strictly greater than its own prepare revision (%v); a real agent would refuse the start as stale", startRev, prepareRev)
	}

	finalHistory, err := h.nightBackgroundAudioHistory(context.Background(), rec)
	if err != nil {
		t.Fatalf("history: %v", err)
	}
	latestAFinal, ok := nightBackgroundAudioLatestStepForNode(finalHistory, "node-a")
	if !ok || latestAFinal.Step.Kind != nightBGStepStart || latestAFinal.Row.Outcome != nightCueOutcomeConfirmed {
		t.Fatalf("node-a latest step = %+v, want a confirmed start", latestAFinal)
	}
}

// TestNightAdvanceMultiNodeBackgroundAudio_StalePendingScheduleRowIsAbandonedForAFreshDecision
// simulates a crash leaving a pending schedule row behind; the next tick
// reaches a genuinely aligned decision rather than concluding no clock.
func TestNightAdvanceMultiNodeBackgroundAudio_StalePendingScheduleRowIsAbandonedForAFreshDecision(t *testing.T) {
	h, st, pub, _ := nightBackgroundAudioTestHandlers(t)
	putBackgroundAudioAsset(t, st, "halloween", "bg-1", "node-a", "asset-1")
	putBackgroundAudioAsset(t, st, "halloween", "bg-2", "node-a", "asset-2")
	putAudioNodeForTest(t, st, "node-a")
	putAudioNodeNoLTCForTest(t, st, "node-b")
	ba := multiNodeBedConfig("node-a", "node-a", "node-b")
	rec := mustCreateRestingSessionWithBackgroundAudio(t, st, "sess-1", "node-a", ba, nightStateRestingIntershow)

	pub.result = confirmedResultForAction("x", nightBackgroundAudioSessionID(rec), "started")
	const clockReading = int64(1_700_000_000_000_000_000)
	pub.resultsByNode = map[string]mqttproto.ResultPayload{
		"node-a:audio.session.prepare": scheduleProbeEvidenceResult(true, clockReading, ""),
		"node-b:audio.gain.set":        refusedResultForAction("gain", "node-b lags behind so the bound (not immediate convergence) drives this test"),
	}

	now := testNow
	for i := 0; i < 3; i++ {
		h.nightAdvanceBackgroundAudio(context.Background(), now, rec)
	}
	if _, ok := dispatchedByNodeAction(pub, "node-a", "audio.session.start"); ok {
		t.Fatalf("node-a started before the simulated crash; test setup drove too far")
	}

	history, err := h.nightBackgroundAudioHistory(context.Background(), rec)
	if err != nil {
		t.Fatalf("history: %v", err)
	}
	phase := nightPhaseRestingBackgroundNode(nightBedScheduleNodeID)
	staleRevision := nightNextBackgroundAudioRevision(history)
	staleCueName := nightBackgroundAudioCueNameSchedule(int(staleRevision))
	if err := h.nightCommitCueRow(context.Background(), now, rec, phase, staleCueName, staleRevision); err != nil {
		t.Fatalf("simulate a crashed schedule row: %v", err)
	}

	now = now.Add(nightBedReadyStepBound)
	h.nightAdvanceBackgroundAudio(context.Background(), now, rec)

	startA, ok := dispatchedByNodeAction(pub, "node-a", "audio.session.start")
	if !ok {
		t.Fatalf("node-a: no audio.session.start dispatched once the bound elapsed; the stale pending row must not block a fresh decision")
	}
	if _, present := startA[pkgaudio.ParamScheduledAtNs]; !present {
		t.Fatalf("node-a: start params = %v, want scheduledAtNs (a genuinely aligned decision, not \"no clock\")", startA)
	}

	history, err = h.nightBackgroundAudioHistory(context.Background(), rec)
	if err != nil {
		t.Fatalf("history: %v", err)
	}
	var pending, resolved int
	for _, row := range nightBackgroundAudioStepsForNode(history, nightBedScheduleNodeID) {
		if row.Step.Kind != nightBGStepSchedule {
			continue
		}
		if row.Row.State == nightCueStateResolved {
			resolved++
		} else {
			pending++
		}
	}
	if resolved != 1 {
		t.Fatalf("resolved schedule rows = %d, want exactly 1 (a fresh decision)", resolved)
	}
	if pending != 1 {
		t.Fatalf("pending schedule rows = %d, want exactly 1 (the abandoned, simulated-crash row)", pending)
	}
}

// TestNightCheckBackgroundAudioBedProgramLTCCoverage_WarnsNamingTheBedWhenTargetsExcludeProgramLTC
// proves a multi-node bed whose targets exclude the program+ltc node
// warns, never fails - decision 4 still plays it unaligned.
func TestNightCheckBackgroundAudioBedProgramLTCCoverage_WarnsNamingTheBedWhenTargetsExcludeProgramLTC(t *testing.T) {
	h, st, _, _ := nightBackgroundAudioTestHandlers(t)
	putAudioNodeNoLTCForTest(t, st, "node-a")
	putAudioNodeNoLTCForTest(t, st, "node-b")
	ba := multiNodeBedConfig("node-a", "node-a", "node-b")

	check := h.nightCheckBackgroundAudioBedProgramLTCCoverage(context.Background(), ba)
	if check.health != nightHealthDegraded() {
		t.Fatalf("health = %v, reason = %q, want degraded (a warning, not a failure)", check.health, check.reason)
	}
	if !reasonMentionsAll(check.reason, "node-a", "node-b") {
		t.Fatalf("reason = %q, want it to name the bed's own targets", check.reason)
	}
}

// TestNightCheckBackgroundAudioBedProgramLTCCoverage_HealthyWhenATargetHoldsProgramLTC
// is the positive case: one listed target holds the role.
func TestNightCheckBackgroundAudioBedProgramLTCCoverage_HealthyWhenATargetHoldsProgramLTC(t *testing.T) {
	h, st, _, _ := nightBackgroundAudioTestHandlers(t)
	putAudioNodeForTest(t, st, "node-a")
	putAudioNodeNoLTCForTest(t, st, "node-b")
	ba := multiNodeBedConfig("node-a", "node-a", "node-b")

	check := h.nightCheckBackgroundAudioBedProgramLTCCoverage(context.Background(), ba)
	if check.health != nightHealthHealthy() {
		t.Fatalf("health = %v, reason = %q, want healthy (node-a holds program+ltc)", check.health, check.reason)
	}
}

// TestNightCheckBackgroundAudioBedTargetCoverage_FailsNamingNodeAndFile
// proves a listed node with no way to receive a copy fails, naming the
// node, the item, and the file (when a copy is registered elsewhere).
func TestNightCheckBackgroundAudioBedTargetCoverage_FailsNamingNodeAndFile(t *testing.T) {
	h, st, _, _ := nightBackgroundAudioTestHandlers(t)
	putBackgroundAudioAsset(t, st, "halloween", "bg-2", "node-c", "asset-2")
	ba := multiNodeBedConfig("node-a", "node-a", "node-b")

	check := h.nightCheckBackgroundAudioBedTargetCoverage(context.Background(), "halloween", ba)
	if check.health != nightHealthFailed() {
		t.Fatalf("health = %v, want failed (no listed target has a registered copy)", check.health)
	}
	if !reasonMentionsAll(check.reason, "node-a", "node-b", "track-1", "track-2") {
		t.Fatalf("reason = %q, want it to name both nodes and both items", check.reason)
	}
	if !strings.Contains(check.reason, "asset-2.mp3") {
		t.Fatalf("reason = %q, want it to name track-2's own registered file (asset-2.mp3), even though node-c is not a listed target", check.reason)
	}
}

// TestNightCheckBackgroundAudioBedTargetCoverage_HealthyViaFallback proves
// the positive case: node-a's own registered row is enough to cover
// node-b too, via the registered-copy rule.
func TestNightCheckBackgroundAudioBedTargetCoverage_HealthyViaFallback(t *testing.T) {
	h, st, _, _ := nightBackgroundAudioTestHandlers(t)
	putBackgroundAudioAsset(t, st, "halloween", "bg-1", "node-a", "asset-1")
	putBackgroundAudioAsset(t, st, "halloween", "bg-2", "node-a", "asset-2")
	ba := multiNodeBedConfig("node-a", "node-a", "node-b")

	check := h.nightCheckBackgroundAudioBedTargetCoverage(context.Background(), "halloween", ba)
	if check.health != nightHealthHealthy() {
		t.Fatalf("health = %v, reason = %q, want healthy (node-b covered via node-a's own registered row)", check.health, check.reason)
	}
}

// TestNightCheckAudioAlignmentForNode_UnlockedClockIsWarning proves an
// unlocked (beyond_threshold) audio-node clock reads as a readiness
// WARNING (degraded), never a failure - decision 4 still plays a bed's
// audio on such a node, so readiness must not report a stopped show.
func TestNightCheckAudioAlignmentForNode_UnlockedClockIsWarning(t *testing.T) {
	h, _, _, _ := nightBackgroundAudioTestHandlers(t)
	h.deps.Audio.(*fakeNodeAudioLister).setObservations("node-b", []observation.Observation{
		{Signal: audioClockAlignmentStateSignalID, Value: audioClockAlignmentStateBeyond, ObservedAt: &testNow, CollectedAt: testNow},
	})

	check := h.nightCheckAudioAlignmentForNode(context.Background(), testNow, "node-b")
	if check.health != nightHealthDegraded() {
		t.Fatalf("health = %v, reason = %q, want degraded (a warning, not a failure)", check.health, check.reason)
	}
}

// TestNightAdvanceMultiNodeBackgroundAudio_ResumeParamKeysMatchSharedWireConstants
// proves the coordinator's dispatched resume params use exactly the
// pkg/audio wire keys, not a coordinator-local synonym that only happens
// to share the same string today. internal/agent's own parseResumePoint
// (the real decode side of this contract) pulls in the cgo GStreamer
// engine chain and is not reachable from a coordinator test, so this
// checks the outgoing key set directly against the shared constants.
func TestNightAdvanceMultiNodeBackgroundAudio_ResumeParamKeysMatchSharedWireConstants(t *testing.T) {
	h, st, pub, _ := nightBackgroundAudioTestHandlers(t)
	rec := twoNodeMultiNodeBedThroughStart(t, h, st, pub)

	pub.resultsByNode = map[string]mqttproto.ResultPayload{
		"node-a:audio.session.pause": pauseResultWithBookmark(true, "track-2", 1, 4500),
	}
	h.nightStopBackgroundAudioIfRunning(context.Background(), testNow, rec)

	const resumeClockReading = int64(1_800_000_000_000_000_000)
	pub.resultsByNode = map[string]mqttproto.ResultPayload{
		"node-a:audio.session.prepare": scheduleProbeEvidenceResult(true, resumeClockReading, ""),
	}
	// A later instant than the start phase's own testNow: two genuinely
	// distinct schedule reads for the SAME node must never share a probe
	// invocationId (readScheduleProbe derives it from (nodeID, now) alone).
	driveNightAdvanceBackgroundAudioUntilStableAt(t, h, pub, rec, testNow.Add(time.Hour), 10)

	resumeA, ok := dispatchedByNodeAction(pub, "node-a", "audio.session.resume")
	if !ok {
		t.Fatalf("node-a: no audio.session.resume dispatched")
	}
	// "sessionId"/"invocationId"/"revision" are the dispatch envelope's own
	// required keys (internal/agent/audiosessionops.go's audioSessionCommonKeys),
	// outside this rebase's shared bookmark/resume wire contract.
	want := []string{pkgaudio.ParamResumeItemID, pkgaudio.ParamResumeIndex, pkgaudio.ParamResumePositionMs, pkgaudio.ParamScheduledAtNs, "sessionId", "invocationId", "revision"}
	sort.Strings(want)
	got := make([]string, 0, len(resumeA))
	for k := range resumeA {
		got = append(got, k)
	}
	sort.Strings(got)
	if strings.Join(got, ",") != strings.Join(want, ",") {
		t.Fatalf("node-a resume param keys = %v, want exactly %v (the shared pkg/audio wire keys)", got, want)
	}
}

// assertAudioSessionCommonParamsParse checks params against exactly the
// rules internal/agent/audiosessionops.go's own parseAudioSessionCommon
// applies to every audio.session.* dispatch (that function is unexported
// and lives in a package this one cannot import, so this mirrors its
// checks rather than calling it): sessionId and invocationId are
// non-empty strings, sessionId matches wantSessionID, and revision is a
// non-negative whole number carried as json.Number, float64, or int64.
func assertAudioSessionCommonParamsParse(t *testing.T, label string, params map[string]any, wantSessionID string) {
	t.Helper()
	sessionID, ok := params["sessionId"].(string)
	if !ok || sessionID == "" {
		t.Fatalf("%s: params.sessionId = %#v, want a non-empty string", label, params["sessionId"])
	}
	if sessionID != wantSessionID {
		t.Fatalf("%s: params.sessionId = %q, want %q", label, sessionID, wantSessionID)
	}
	invocationID, ok := params["invocationId"].(string)
	if !ok || invocationID == "" {
		t.Fatalf("%s: params.invocationId = %#v, want a non-empty string", label, params["invocationId"])
	}
	revision, ok := evidenceInt64(params["revision"])
	if !ok || revision < 0 {
		t.Fatalf("%s: params.revision = %#v, want a non-negative whole number", label, params["revision"])
	}
}

// TestNightAdvanceMultiNodeBackgroundAudio_BedDispatchesCarryAgentRequiredKeys
// proves every nightRunBedAudioCommand dispatch (start, resume, pause) carries
// sessionId, invocationId, and revision exactly as
// internal/agent/audiosessionops.go's parseAudioSessionCommon requires them
// on the wire. Two real nodes hit this gap: nightRunBedAudioCommand only
// ever added params["revision"], so every one of these actions resolved
// unconfirmable with the agent's own "params.sessionId is required".
func TestNightAdvanceMultiNodeBackgroundAudio_BedDispatchesCarryAgentRequiredKeys(t *testing.T) {
	h, st, pub, _ := nightBackgroundAudioTestHandlers(t)
	rec := twoNodeMultiNodeBedThroughStart(t, h, st, pub)
	sessionID := nightBackgroundAudioSessionID(rec)

	startA, ok := dispatchedByNodeAction(pub, "node-a", "audio.session.start")
	if !ok {
		t.Fatalf("node-a: no audio.session.start dispatched")
	}
	startB, ok := dispatchedByNodeAction(pub, "node-b", "audio.session.start")
	if !ok {
		t.Fatalf("node-b: no audio.session.start dispatched")
	}
	assertAudioSessionCommonParamsParse(t, "audio.session.start (node-a)", startA, sessionID)
	assertAudioSessionCommonParamsParse(t, "audio.session.start (node-b)", startB, sessionID)

	pub.resultsByNode = map[string]mqttproto.ResultPayload{
		"node-a:audio.session.pause": pauseResultWithBookmark(true, "track-2", 1, 4500),
	}
	h.nightStopBackgroundAudioIfRunning(context.Background(), testNow, rec)

	pauseA, ok := dispatchedByNodeAction(pub, "node-a", "audio.session.pause")
	if !ok {
		t.Fatalf("node-a: no audio.session.pause dispatched")
	}
	pauseB, ok := dispatchedByNodeAction(pub, "node-b", "audio.session.pause")
	if !ok {
		t.Fatalf("node-b: no audio.session.pause dispatched")
	}
	assertAudioSessionCommonParamsParse(t, "audio.session.pause (node-a)", pauseA, sessionID)
	assertAudioSessionCommonParamsParse(t, "audio.session.pause (node-b)", pauseB, sessionID)

	const resumeClockReading = int64(1_800_000_000_000_000_000)
	pub.resultsByNode = map[string]mqttproto.ResultPayload{
		"node-a:audio.session.prepare": scheduleProbeEvidenceResult(true, resumeClockReading, ""),
	}
	// A later instant than the start phase's own testNow: two genuinely
	// distinct schedule reads for the SAME node must never share a probe
	// invocationId (readScheduleProbe derives it from (nodeID, now) alone).
	driveNightAdvanceBackgroundAudioUntilStableAt(t, h, pub, rec, testNow.Add(time.Hour), 10)

	resumeA, ok := dispatchedByNodeAction(pub, "node-a", "audio.session.resume")
	if !ok {
		t.Fatalf("node-a: no audio.session.resume dispatched")
	}
	resumeB, ok := dispatchedByNodeAction(pub, "node-b", "audio.session.resume")
	if !ok {
		t.Fatalf("node-b: no audio.session.resume dispatched")
	}
	assertAudioSessionCommonParamsParse(t, "audio.session.resume (node-a)", resumeA, sessionID)
	assertAudioSessionCommonParamsParse(t, "audio.session.resume (node-b)", resumeB, sessionID)
}

// waitForBedDispatchCondition polls cond every 5ms until it reports true,
// or bound elapses - in which case onTimeout runs (releasing a blocked
// node so the driving goroutine below can still exit) before the test
// fails, rather than leaving that goroutine hung past the test's own end.
func waitForBedDispatchCondition(t *testing.T, bound time.Duration, onTimeout func(), cond func() bool) {
	t.Helper()
	deadline := time.After(bound)
	for {
		if cond() {
			return
		}
		select {
		case <-deadline:
			onTimeout()
			t.Fatal("condition was never satisfied within the bound")
		case <-time.After(5 * time.Millisecond):
		}
	}
}

// TestNightAdvanceMultiNodeBackgroundAudio_StartDispatchesNodesConcurrently
// proves nightDispatchBedNodesConcurrently's own contract for the bed's
// shared start instant: real hardware hit the opposite of this - a plain
// per-node for loop meant the second node's own audio.session.start
// reached it only after the first node's own agent had already answered,
// which for a ~4s lead time landed after the shared instant and the
// second node refused with scheduled_start_in_past. Here node-a's own
// agent never answers until this test releases it, so node-b's own start
// dispatching and confirming in the meantime is only possible if the two
// nodes are genuinely dispatched concurrently, not serially.
func TestNightAdvanceMultiNodeBackgroundAudio_StartDispatchesNodesConcurrently(t *testing.T) {
	h, st, pub, _ := nightBackgroundAudioTestHandlers(t)
	putBackgroundAudioAsset(t, st, "halloween", "bg-1", "node-a", "asset-1")
	putBackgroundAudioAsset(t, st, "halloween", "bg-2", "node-a", "asset-2")
	putAudioNodeForTest(t, st, "node-a")
	putAudioNodeNoLTCForTest(t, st, "node-b")
	ba := multiNodeBedConfig("node-a", "node-a", "node-b")
	rec := mustCreateRestingSessionWithBackgroundAudio(t, st, "sess-1", "node-a", ba, nightStateRestingIntershow)

	pub.result = confirmedResultForAction("x", nightBackgroundAudioSessionID(rec), "started")
	const clockReading = int64(1_700_000_000_000_000_000)
	pub.resultsByNode = map[string]mqttproto.ResultPayload{
		"node-a:audio.session.prepare": scheduleProbeEvidenceResult(true, clockReading, ""),
	}

	release := make(chan struct{})
	var released bool
	closeRelease := func() {
		if !released {
			released = true
			close(release)
		}
	}
	defer closeRelease()
	pub.blockUntilByNode = map[string]<-chan struct{}{"node-a:audio.session.start": release}

	done := make(chan struct{})
	go func() {
		defer close(done)
		for i := 0; i < 10; i++ {
			h.nightAdvanceBackgroundAudio(context.Background(), testNow, rec)
		}
	}()

	waitForBedDispatchCondition(t, 5*time.Second, closeRelease, func() bool {
		_, ok := dispatchedByNodeAction(pub, "node-a", "audio.session.start")
		return ok
	})

	waitForBedDispatchCondition(t, 5*time.Second, closeRelease, func() bool {
		history, err := h.nightBackgroundAudioHistory(context.Background(), rec)
		if err != nil {
			t.Fatalf("history: %v", err)
		}
		latestB, ok := nightBackgroundAudioLatestStepForNode(history, "node-b")
		return ok && latestB.Step.Kind == nightBGStepStart && latestB.Row.Outcome == nightCueOutcomeConfirmed
	})

	history, err := h.nightBackgroundAudioHistory(context.Background(), rec)
	if err != nil {
		t.Fatalf("history: %v", err)
	}
	latestA, ok := nightBackgroundAudioLatestStepForNode(history, "node-a")
	if !ok || latestA.Step.Kind != nightBGStepStart || latestA.Row.State != nightCueStateDispatched {
		t.Fatalf("node-a latest step = %+v, want still dispatched (unresolved) while node-b already confirmed its own start", latestA)
	}

	closeRelease()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("driving loop never finished after releasing node-a")
	}

	history, err = h.nightBackgroundAudioHistory(context.Background(), rec)
	if err != nil {
		t.Fatalf("history: %v", err)
	}
	latestA, ok = nightBackgroundAudioLatestStepForNode(history, "node-a")
	if !ok || latestA.Step.Kind != nightBGStepStart || latestA.Row.Outcome != nightCueOutcomeConfirmed {
		t.Fatalf("node-a latest step = %+v, want a confirmed start once released", latestA)
	}
}

// TestNightAdvanceMultiNodeBackgroundAudio_ResumeDispatchesNodesConcurrently
// is [TestNightAdvanceMultiNodeBackgroundAudio_StartDispatchesNodesConcurrently]'s
// own counterpart for the bed's shared resume instant.
func TestNightAdvanceMultiNodeBackgroundAudio_ResumeDispatchesNodesConcurrently(t *testing.T) {
	h, st, pub, _ := nightBackgroundAudioTestHandlers(t)
	rec := twoNodeMultiNodeBedThroughStart(t, h, st, pub)

	pub.resultsByNode = map[string]mqttproto.ResultPayload{
		"node-a:audio.session.pause": pauseResultWithBookmark(true, "track-2", 1, 4500),
	}
	h.nightStopBackgroundAudioIfRunning(context.Background(), testNow, rec)

	const resumeClockReading = int64(1_800_000_000_000_000_000)
	pub.resultsByNode = map[string]mqttproto.ResultPayload{
		"node-a:audio.session.prepare": scheduleProbeEvidenceResult(true, resumeClockReading, ""),
	}

	release := make(chan struct{})
	var released bool
	closeRelease := func() {
		if !released {
			released = true
			close(release)
		}
	}
	defer closeRelease()
	pub.blockUntilByNode = map[string]<-chan struct{}{"node-a:audio.session.resume": release}

	done := make(chan struct{})
	go func() {
		defer close(done)
		for i := 0; i < 10; i++ {
			h.nightAdvanceBackgroundAudio(context.Background(), testNow, rec)
		}
	}()

	waitForBedDispatchCondition(t, 5*time.Second, closeRelease, func() bool {
		_, ok := dispatchedByNodeAction(pub, "node-a", "audio.session.resume")
		return ok
	})

	waitForBedDispatchCondition(t, 5*time.Second, closeRelease, func() bool {
		history, err := h.nightBackgroundAudioHistory(context.Background(), rec)
		if err != nil {
			t.Fatalf("history: %v", err)
		}
		latestB, ok := nightBackgroundAudioLatestStepForNode(history, "node-b")
		return ok && latestB.Step.Kind == nightBGStepResume && latestB.Row.Outcome == nightCueOutcomeConfirmed
	})

	history, err := h.nightBackgroundAudioHistory(context.Background(), rec)
	if err != nil {
		t.Fatalf("history: %v", err)
	}
	latestA, ok := nightBackgroundAudioLatestStepForNode(history, "node-a")
	if !ok || latestA.Step.Kind != nightBGStepResume || latestA.Row.State != nightCueStateDispatched {
		t.Fatalf("node-a latest step = %+v, want still dispatched (unresolved) while node-b already confirmed its own resume", latestA)
	}

	closeRelease()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("driving loop never finished after releasing node-a")
	}

	history, err = h.nightBackgroundAudioHistory(context.Background(), rec)
	if err != nil {
		t.Fatalf("history: %v", err)
	}
	latestA, ok = nightBackgroundAudioLatestStepForNode(history, "node-a")
	if !ok || latestA.Step.Kind != nightBGStepResume || latestA.Row.Outcome != nightCueOutcomeConfirmed {
		t.Fatalf("node-a latest step = %+v, want a confirmed resume once released", latestA)
	}
}

// TestNightAdvanceMultiNodeBackgroundAudio_RefusedResumeIsNotRedispatched
// is Defect C's own regression guard. The per-node start path
// (nightAdvanceBackgroundAudioForNode's own nightBGStepStart case) logs
// "start did not confirm; not auto-retrying" and stops; before the fix,
// its nightBGStepResume sibling had no such gate for a multi-node bed, and
// unconditionally re-dispatched a fresh audio.session.resume every tick
// under nightBackgroundAudioResume's own "retry under a fresh revision:
// never wedge here" rule meant for a SINGLE-node bed - observed on a bench
// coordinator as revisions 9 to 29 and counting, every one refused. This
// proves a resume row that resolved refused is never re-dispatched on a
// later tick, while it still carries its own refusal reason (surfaced by
// mapNightBackgroundAudio, nightsessioncontrol.go, straight off history).
func TestNightAdvanceMultiNodeBackgroundAudio_RefusedResumeIsNotRedispatched(t *testing.T) {
	h, st, pub, _ := nightBackgroundAudioTestHandlers(t)
	rec := twoNodeMultiNodeBedThroughStart(t, h, st, pub)

	pub.resultsByNode = map[string]mqttproto.ResultPayload{
		"node-a:audio.session.pause": pauseResultWithBookmark(true, "track-2", 1, 4500),
	}
	h.nightStopBackgroundAudioIfRunning(context.Background(), testNow, rec)

	// A later instant than the start phase's own testNow: see
	// driveNightAdvanceBackgroundAudioUntilStableAt's own doc comment.
	resumeNow := testNow.Add(time.Hour)
	const resumeClockReading = int64(1_800_000_000_000_000_000)
	pub.resultsByNode = map[string]mqttproto.ResultPayload{
		"node-a:audio.session.prepare": scheduleProbeEvidenceResult(true, resumeClockReading, ""),
		"node-a:audio.session.resume":  refusedResultForAction("resume", "node-a refuses this resume"),
	}
	for i := 0; i < 5; i++ {
		h.nightAdvanceBackgroundAudio(context.Background(), resumeNow, rec)
	}

	if got := countDispatchedByNodeAction(pub, "node-a", "audio.session.resume"); got != 1 {
		t.Fatalf("node-a audio.session.resume dispatch count = %d, want exactly 1 (a refused resume must not be auto-retried in the same cycle)", got)
	}

	history, err := h.nightBackgroundAudioHistory(context.Background(), rec)
	if err != nil {
		t.Fatalf("history: %v", err)
	}
	latestA, ok := nightBackgroundAudioLatestStepForNode(history, "node-a")
	if !ok || latestA.Step.Kind != nightBGStepResume || latestA.Row.Outcome == nightCueOutcomeConfirmed || latestA.Row.OutcomeReason == "" {
		t.Fatalf("node-a latest step = %+v, want its own resume resolved refused (not confirmed) and still carrying a non-empty reason", latestA)
	}
}

// countDispatchedByNodeAction is [countDispatchedAction] narrowed to a
// single node id.
func countDispatchedByNodeAction(pub *fakeAudioPublisher, nodeID, action string) int {
	n := 0
	for _, d := range pub.dispatchedSnapshot() {
		if d.NodeID == nodeID && d.Action == action {
			n++
		}
	}
	return n
}

func reasonMentionsAll(s string, subs ...string) bool {
	for _, sub := range subs {
		if !strings.Contains(s, sub) {
			return false
		}
	}
	return true
}
