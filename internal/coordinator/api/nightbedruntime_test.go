package api

import (
	"context"
	"encoding/json"
	"strconv"
	"strings"
	"testing"

	"github.com/showmeshsystems/showmesh/internal/coordinator/config"
	"github.com/showmeshsystems/showmesh/internal/coordinator/store"
	pkgaudio "github.com/showmeshsystems/showmesh/pkg/audio"
	"github.com/showmeshsystems/showmesh/pkg/mqttproto"
	"github.com/showmeshsystems/showmesh/pkg/observation"
)

// This file proves ADR-049 decisions 7-9's own multi-node bed runtime
// (nightbackgroundaudio.go's own "multi-node bed" section): the shared
// start/resume instant (R3), the program+ltc bookmark push (R4), and the
// regression rules a bed with no declared Targets, or a single declared
// target, must keep exactly today's behavior.

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
// until a tick dispatches nothing new (or maxTicks is reached), so a test
// need not hand-count exactly how many ticks a multi-node bed's per-node
// convergence takes.
func driveNightAdvanceBackgroundAudioUntilStable(t *testing.T, h *handlers, pub *fakeAudioPublisher, rec store.NightSessionRecord, maxTicks int) {
	t.Helper()
	for i := 0; i < maxTicks; i++ {
		before := len(pub.dispatchedSnapshot())
		h.nightAdvanceBackgroundAudio(context.Background(), testNow, rec)
		if len(pub.dispatchedSnapshot()) == before {
			return
		}
	}
	t.Fatalf("nightAdvanceBackgroundAudio did not stabilize within %d ticks", maxTicks)
}

// pauseResultWithBookmark builds the audio.session.pause result R1's own
// NEW evidence keys (nightbedwire.go) ride, exactly as
// nightBedBookmarkFromEvidence decodes them.
func pauseResultWithBookmark(known bool, itemID string, index int, positionMs int64) mqttproto.ResultPayload {
	return mqttproto.ResultPayload{
		Outcome: mqttproto.OutcomeConfirmed,
		Evidence: &mqttproto.ResultEvidence{
			Signal: "audio.session",
			Value: map[string]any{
				"outcome":          "started",
				bookmarkKnown:      known,
				bookmarkItemId:     itemID,
				bookmarkIndex:      json.Number(strconv.Itoa(index)),
				bookmarkPositionMs: json.Number(strconv.FormatInt(positionMs, 10)),
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

// TestNightAdvanceMultiNodeBackgroundAudio_SharedStartInstantOneRead is R3's
// own acceptance proof: a two-node bed's shared first start reads the
// clock exactly once (only node-a, the program+ltc node, is ever asked for
// audio.session.prepare), dispatches the IDENTICAL scheduledAtNs to both
// nodes, and a later replay tick reads no clock and dispatches nothing new.
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
// proves single-node beds - even ones that DO declare Targets, naming just
// one node - never engage R3's scheduling at all: no
// audio.session.prepare is ever dispatched, exactly like a bed with no
// declared Targets.
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

	applyCountBeforeResume := countDispatchedAction(pub, "audio.session.apply")

	pub.resultsByNode = map[string]mqttproto.ResultPayload{
		"node-a:audio.session.pause": pauseResultWithBookmark(true, "track-2", 1, 4500),
	}
	h.nightStopBackgroundAudioIfRunning(context.Background(), testNow, rec)

	const resumeClockReading = int64(1_800_000_000_000_000_000)
	pub.resultsByNode = map[string]mqttproto.ResultPayload{
		"node-a:audio.session.prepare": scheduleProbeEvidenceResult(true, resumeClockReading, ""),
	}
	driveNightAdvanceBackgroundAudioUntilStable(t, h, pub, rec, 10)

	if got := countDispatchedAction(pub, "audio.session.apply"); got != applyCountBeforeResume {
		t.Fatalf("audio.session.apply dispatch count went from %d to %d, want unchanged (the resume point travels on resume itself, never a separate apply push)", applyCountBeforeResume, got)
	}

	resumeA, okA := dispatchedByNodeAction(pub, "node-a", "audio.session.resume")
	resumeB, okB := dispatchedByNodeAction(pub, "node-b", "audio.session.resume")
	if !okA || !okB {
		t.Fatalf("resumes dispatched: node-a=%v node-b=%v, want both", okA, okB)
	}
	for nodeID, params := range map[string]map[string]any{"node-a": resumeA, "node-b": resumeB} {
		if params[resumeItemId] != "track-2" {
			t.Fatalf("node %q: resume params = %v, want resumeItemId=track-2", nodeID, params)
		}
		index, _ := evidenceInt64(params[resumeIndex])
		if index != 1 {
			t.Fatalf("node %q: resume params = %v, want resumeIndex=1", nodeID, params)
		}
		positionMs, _ := evidenceInt64(params[resumePositionMs])
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

// TestNightAdvanceMultiNodeBackgroundAudio_ResumeUnknownBookmarkOnArrival
// is R4's own fallback: the program+ltc node's own pause result carries no
// bookmark evidence, so every node resumes from its own bookmark on
// arrival - no bookmark push, no scheduledAtNs - and it is recorded
// unaligned with a reason.
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
// is R4's other own fallback: no listed node holds the program+ltc role at
// all, so resume falls back exactly the same way.
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

func reasonMentionsAll(s string, subs ...string) bool {
	for _, sub := range subs {
		if !strings.Contains(s, sub) {
			return false
		}
	}
	return true
}
