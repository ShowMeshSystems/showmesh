package api

import (
	"context"
	"encoding/json"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/showmeshsystems/showmesh/internal/coordinator/config"
	"github.com/showmeshsystems/showmesh/internal/coordinator/cueactivate"
	"github.com/showmeshsystems/showmesh/internal/coordinator/store"
	pkgaudio "github.com/showmeshsystems/showmesh/pkg/audio"
	"github.com/showmeshsystems/showmesh/pkg/cueactivation"
	"github.com/showmeshsystems/showmesh/pkg/mqttproto"
)

// This file proves ADR-049 decision 3's own coordinator-side scheduling
// step. These fixtures give each of two nodes its OWN single-targeted Cue
// rather than one Cue naming both nodes in outputs.audio.targets:
// scheduleCueActivations never inspects whether the Activations in one
// batch share a CueID, only whether each node's own resolved outputs are
// audio-bearing, so this is indistinguishable from its own code's
// perspective from the shared-CueID case ADR-049 decision 1 also enables.

// putAudioNodeNoLTCForTest declares nodeID's own audio.node WITHOUT an
// LTC route: [handlers.nodeHoldsMediaClock] reports false for it, unlike
// putAudioNodeForTest's own fixed LTCRoute.
func putAudioNodeNoLTCForTest(t *testing.T, st *store.Store, nodeID string) {
	t.Helper()
	raw, err := config.EncodeAudioNodePayload(config.AudioNodePayload{
		ProgramRoute:          "usb-interface",
		ProgramChannels:       []int{1, 2},
		ClockDomain:           "single-interface",
		ClockDomainProvenance: "single interface, both routes on it",
	})
	if err != nil {
		t.Fatalf("encode audio.node payload: %v", err)
	}
	putConfigForTest(t, st, config.AudioNodeConfigKind, nodeID, raw)
}

// putTargetedAudioCueForTest writes a show.cue whose audio output
// explicitly targets nodeID, the one node this Cue resolves on.
func putTargetedAudioCueForTest(t *testing.T, st *store.Store, cueID, showID, nodeID string) {
	t.Helper()
	payload, err := config.EncodeShowCuePayload(config.ShowCuePayload{
		Show: showID, Name: cueID,
		Outputs: config.ShowCueOutputs{Audio: &config.ShowCueAudioOutput{Asset: "asset-" + cueID, Targets: []string{nodeID}}},
	})
	if err != nil {
		t.Fatalf("encode show.cue payload: %v", err)
	}
	putConfigForTest(t, st, config.ShowCueConfigKind, cueID, payload)
}

// scheduleProbeEvidenceResult builds the audio.session.prepare result
// [handlers.readScheduleProbe] reads its evidence from.
func scheduleProbeEvidenceResult(valid bool, nowNs int64, reason string) mqttproto.ResultPayload {
	return mqttproto.ResultPayload{
		Outcome: mqttproto.OutcomeConfirmed,
		Evidence: &mqttproto.ResultEvidence{
			Signal: "audio.session",
			Value: map[string]any{
				pkgaudio.ResultMediaClockValid:  valid,
				pkgaudio.ResultMediaClockNowNs:  json.Number(strconv.FormatInt(nowNs, 10)),
				pkgaudio.ResultMediaClockReason: reason,
			},
		},
	}
}

// twoNodeScheduleFixture builds two audio-bearing, individually-
// authorized activations: "audio-holder" (holds the media clock) and
// "audio-second" (does not), each for its own single-targeted Cue.
func twoNodeScheduleFixture(t *testing.T, setup *audioDispatchTestSetup, now time.Time) (activations map[string]cueactivation.Activation) {
	t.Helper()
	const showID = "halloween-2026"
	putShowForTest(t, setup.st, showID, "Halloween 2026")
	putAudioNodeForTest(t, setup.st, "audio-holder")
	putAudioNodeNoLTCForTest(t, setup.st, "audio-second")
	declareNodeForTest(t, setup.st, "audio-holder")
	declareNodeForTest(t, setup.st, "audio-second")
	putFreshReportForTest(t, setup.st, "audio-holder", now)
	putFreshReportForTest(t, setup.st, "audio-second", now)
	putTargetedAudioCueForTest(t, setup.st, "cue-holder", showID, "audio-holder")
	putTargetedAudioCueForTest(t, setup.st, "cue-second", showID, "audio-second")
	putAuthorizedAudioAssetForTest(t, setup.st, showID, "cue-holder", "audio-holder", now)
	putAuthorizedAudioAssetForTest(t, setup.st, showID, "cue-second", "audio-second", now)
	// A Cue only appears in a node's own resolved catalog once some
	// show.playlist entry references it (assetsync's own referencedCueIDs
	// scoping rule), this playlist exists only to satisfy that, its
	// runner/entries are otherwise irrelevant to this test.
	putPlaylistForTest(t, setup.st, "playlist-schedule", config.ShowPlaylistPayload{
		Show: showID, Name: "Main", Runner: config.ShowPlaylistRunnerFPP,
		MismatchPolicy: config.ShowPlaylistMismatchPolicyHold,
		FPP:            &config.ShowPlaylistFPPBinding{InstanceUUID: "inst-1", PlaylistName: "Main", PlaylistHash: hash64ForTest("5c4ed")},
		Entries: []config.ShowPlaylistEntry{
			{ID: "entry-holder", Cue: "cue-holder", FPP: &config.ShowPlaylistEntryFPP{Section: "mainPlaylist", Position: 0}},
			{ID: "entry-second", Cue: "cue-second", FPP: &config.ShowPlaylistEntryFPP{Section: "mainPlaylist", Position: 1}},
		},
	})
	putActiveShowForTest(t, setup.st, showID)

	catalogRevHolder := resolvedCatalogRevisionForTest(t, setup.st, showID, "audio-holder")
	catalogRevSecond := resolvedCatalogRevisionForTest(t, setup.st, showID, "audio-second")

	return map[string]cueactivation.Activation{
		"audio-holder": {
			Runner: "fpp", RunnerInstance: "inst-1", ActivationID: "act-holder",
			Show: showID, Generation: 1, CatalogRevision: catalogRevHolder,
			CueID: "cue-holder", CueRevision: 1, PositionMS: 4200, EvidenceAt: now,
		},
		"audio-second": {
			Runner: "fpp", RunnerInstance: "inst-1", ActivationID: "act-second",
			Show: showID, Generation: 1, CatalogRevision: catalogRevSecond,
			CueID: "cue-second", CueRevision: 1, PositionMS: 4200, EvidenceAt: now,
		},
	}
}

// TestScheduleCueActivationsSelectsOneInstantForTwoLockedTargets is the
// seam's own central proof: exactly one audiosched.Select's worth of
// work runs for a two-audio-node activation batch, and the identical
// instant and PositionMS land on both.
func TestScheduleCueActivationsSelectsOneInstantForTwoLockedTargets(t *testing.T) {
	now := testNow
	setup := newAudioDispatchTestSetup(t, fixedClock(now))
	activations := twoNodeScheduleFixture(t, setup, now)

	const holderReading = int64(1_700_000_000_000_000_000)
	setup.pub.resultsByNode = map[string]mqttproto.ResultPayload{
		"audio-holder:audio.session.prepare": scheduleProbeEvidenceResult(true, holderReading, ""),
		"audio-second:audio.session.prepare": scheduleProbeEvidenceResult(true, holderReading+5_000_000, ""),
	}

	deps := setup.deps()
	deps.AssetManifests = setup.st
	h := &handlers{deps: deps.withDefaults(), clock: fixedClock(now), logger: testLogger()}
	issuer := cueActivationIssuer{PrincipalID: "system:cue-activation-loop:test"}

	h.scheduleCueActivations(context.Background(), now, activations, issuer, nil)

	holder := activations["audio-holder"]
	second := activations["audio-second"]
	if holder.ScheduledAtNs == nil || second.ScheduledAtNs == nil {
		t.Fatalf("ScheduledAtNs = %v / %v, want both set", holder.ScheduledAtNs, second.ScheduledAtNs)
	}
	if *holder.ScheduledAtNs != *second.ScheduledAtNs {
		t.Fatalf("ScheduledAtNs differ: holder=%d second=%d, want identical (one Select call, one instant)", *holder.ScheduledAtNs, *second.ScheduledAtNs)
	}
	if *holder.ScheduledAtNs <= holderReading {
		t.Fatalf("ScheduledAtNs = %d, want it past the clock-holder's own reading %d (a lead time must be added)", *holder.ScheduledAtNs, holderReading)
	}
	if holder.PositionMS != second.PositionMS {
		t.Fatalf("PositionMS differ: holder=%d second=%d, want identical", holder.PositionMS, second.PositionMS)
	}
	if holder.UnalignedReason != "" || second.UnalignedReason != "" {
		t.Fatalf("UnalignedReason = %q / %q, want both empty", holder.UnalignedReason, second.UnalignedReason)
	}

	aligned, reason, at := cueActivationAlignment(activations)
	if !aligned || reason != "" || at == nil || *at != *holder.ScheduledAtNs {
		t.Fatalf("cueActivationAlignment = (%v,%q,%v), want (true,\"\",%d)", aligned, reason, at, *holder.ScheduledAtNs)
	}

	// The probe session must be cleared on the success path too.
	foundClear := map[string]bool{}
	for _, d := range setup.pub.dispatched {
		if d.Action == "audio.session.clear" {
			foundClear[d.NodeID] = true
		}
	}
	if !foundClear["audio-holder"] || !foundClear["audio-second"] {
		t.Fatalf("audio.session.clear dispatched for %+v, want both nodes cleared", foundClear)
	}
}

// TestScheduleCueActivationsUnalignedWhenNoHolderLocked proves ADR-049
// decision 4: when the clock-holder node's own reading is invalid (its
// provider is not locked), both nodes are left WITHOUT an instant, they
// start on arrival, and the batch reports the concrete reason, never a
// synchronized success it did not reach.
func TestScheduleCueActivationsUnalignedWhenNoHolderLocked(t *testing.T) {
	now := testNow
	setup := newAudioDispatchTestSetup(t, fixedClock(now))
	activations := twoNodeScheduleFixture(t, setup, now)

	setup.pub.resultsByNode = map[string]mqttproto.ResultPayload{
		"audio-holder:audio.session.prepare": scheduleProbeEvidenceResult(false, 0, "provider is not locked"),
		"audio-second:audio.session.prepare": scheduleProbeEvidenceResult(true, 1_700_000_000_000_000_000, ""),
	}

	deps := setup.deps()
	deps.AssetManifests = setup.st
	h := &handlers{deps: deps.withDefaults(), clock: fixedClock(now), logger: testLogger()}
	issuer := cueActivationIssuer{PrincipalID: "system:cue-activation-loop:test"}

	h.scheduleCueActivations(context.Background(), now, activations, issuer, nil)

	holder := activations["audio-holder"]
	second := activations["audio-second"]
	if holder.ScheduledAtNs != nil || second.ScheduledAtNs != nil {
		t.Fatalf("ScheduledAtNs = %v / %v, want both nil: no usable clock reading exists", holder.ScheduledAtNs, second.ScheduledAtNs)
	}
	if holder.UnalignedReason == "" || second.UnalignedReason == "" {
		t.Fatalf("UnalignedReason = %q / %q, want both carry the concrete reason", holder.UnalignedReason, second.UnalignedReason)
	}
	if holder.UnalignedReason != second.UnalignedReason {
		t.Fatalf("UnalignedReason differs: holder=%q second=%q, want identical (one Select call)", holder.UnalignedReason, second.UnalignedReason)
	}

	aligned, reason, at := cueActivationAlignment(activations)
	if aligned || reason == "" || at != nil {
		t.Fatalf("cueActivationAlignment = (%v,%q,%v), want (false,<non-empty>,nil)", aligned, reason, at)
	}
}

// TestScheduleCueActivationsSkipsSingleAudioBearingNode proves the
// untouched path: a single audio-bearing node never attempts a reading
// round at all: no instant, no probe dispatch, nothing added to a
// single-node Cue's Activation, matching ADR-049's own "a Cue reaching
// one node behaves exactly as today."
func TestScheduleCueActivationsSkipsSingleAudioBearingNode(t *testing.T) {
	now := testNow
	setup := newAudioDispatchTestSetup(t, fixedClock(now))
	nodeID, act := cueActivationDispatchTestFixture(t, setup, now)
	putAuthorizedAudioAssetForTest(t, setup.st, act.Show, act.CueID, nodeID, now)
	act.CatalogRevision = resolvedCatalogRevisionForTest(t, setup.st, act.Show, nodeID)
	activations := map[string]cueactivation.Activation{nodeID: act}

	deps := setup.deps()
	deps.AssetManifests = setup.st
	h := &handlers{deps: deps.withDefaults(), clock: fixedClock(now), logger: testLogger()}
	issuer := cueActivationIssuer{PrincipalID: "system:cue-activation-loop:test"}

	h.scheduleCueActivations(context.Background(), now, activations, issuer, nil)

	got := activations[nodeID]
	if got.ScheduledAtNs != nil || got.UnalignedReason != "" {
		t.Fatalf("single-node Activation mutated: ScheduledAtNs=%v UnalignedReason=%q, want both unset", got.ScheduledAtNs, got.UnalignedReason)
	}
	if len(setup.pub.dispatched) != 0 {
		t.Fatalf("dispatched %d commands for a single-audio-node batch, want 0 (no reading round)", len(setup.pub.dispatched))
	}
}

// TestScheduleCueActivationsMarksUnalignedWhenAssetManifestsNil proves
// the frozen rule ("an unaligned fallback is never reported as
// synchronized success") on the AssetManifests-nil guard: a two-node
// audio-bearing batch that bails out before it can even resolve which
// nodes are audio-bearing must not default to aligned=true.
func TestScheduleCueActivationsMarksUnalignedWhenAssetManifestsNil(t *testing.T) {
	now := testNow
	setup := newAudioDispatchTestSetup(t, fixedClock(now))
	activations := twoNodeScheduleFixture(t, setup, now)

	h := &handlers{deps: setup.deps().withDefaults(), clock: fixedClock(now), logger: testLogger()}
	issuer := cueActivationIssuer{PrincipalID: "system:cue-activation-loop:test"}

	h.scheduleCueActivations(context.Background(), now, activations, issuer, nil)

	for nodeID, act := range activations {
		if act.ScheduledAtNs != nil {
			t.Fatalf("node %q ScheduledAtNs = %v, want nil: no asset manifest store means no instant could ever have been selected", nodeID, act.ScheduledAtNs)
		}
		if act.UnalignedReason == "" {
			t.Fatalf("node %q UnalignedReason is empty, want a concrete reason: never let this fall back to aligned=true by default", nodeID)
		}
	}
	aligned, reason, at := cueActivationAlignment(activations)
	if aligned || reason == "" || at != nil {
		t.Fatalf("cueActivationAlignment = (%v,%q,%v), want (false,<non-empty>,nil)", aligned, reason, at)
	}
}

// TestScheduleCueActivationsMarksUnalignedWhenSettingsReadFails proves
// the identical frozen-rule fix on the audio.settings-read-failure path:
// bearing has already been resolved by then, so only the audio-bearing
// nodes are marked, but they must be marked, never left to default to
// aligned=true.
func TestScheduleCueActivationsMarksUnalignedWhenSettingsReadFails(t *testing.T) {
	now := testNow
	setup := newAudioDispatchTestSetup(t, fixedClock(now))
	activations := twoNodeScheduleFixture(t, setup, now)

	// A stored audio.settings payload that does not decode: triggers
	// alignedStartSettings' own "stored audio.settings does not decode"
	// error path, distinct from "nothing was ever written" (which falls
	// back to defaults, not an error).
	putConfigForTest(t, setup.st, config.AudioSettingsConfigKind, config.AudioSettingsConfigObjectID, `{"scheduledStartDeliveryBoundMs": "not a number"}`)

	deps := setup.deps()
	deps.AssetManifests = setup.st
	h := &handlers{deps: deps.withDefaults(), clock: fixedClock(now), logger: testLogger()}
	issuer := cueActivationIssuer{PrincipalID: "system:cue-activation-loop:test"}

	h.scheduleCueActivations(context.Background(), now, activations, issuer, nil)

	for nodeID, act := range activations {
		if act.ScheduledAtNs != nil {
			t.Fatalf("node %q ScheduledAtNs = %v, want nil: audio.settings could not be read, so no instant could have been selected", nodeID, act.ScheduledAtNs)
		}
		if act.UnalignedReason == "" {
			t.Fatalf("node %q UnalignedReason is empty, want a concrete reason", nodeID)
		}
	}
	if len(setup.pub.dispatchedSnapshot()) != 0 {
		t.Fatalf("dispatched %d commands, want 0: the settings read failed before any probe round could start", len(setup.pub.dispatchedSnapshot()))
	}
}

// TestDispatchCueActivationsConcurrentlyDoesNotLetOneNodeDelayAnother is a
// direct, deterministic proof of dispatchCueActivationsConcurrently's own
// contract: ADR-049 decision 3's "one node refusing or failing never
// stops, cancels, or rolls back the others."
//
// It uses a mutual barrier BOTH fn calls must reach before either may
// proceed, which a sequential (one-node-then-the-other) implementation
// can never satisfy: the second call has not even started while the
// first is still waiting at the barrier, so a reverted implementation
// hangs and this test fails deterministically, rather than a fast node
// happening to be visited first by Go's own randomized map iteration
// order (which a sequential loop would still pass about half the time).
func TestDispatchCueActivationsConcurrentlyDoesNotLetOneNodeDelayAnother(t *testing.T) {
	activations := map[string]cueactivation.Activation{
		"a": {ActivationID: "act-a"},
		"b": {ActivationID: "act-b"},
	}
	var arrived sync.WaitGroup
	arrived.Add(2)
	bothArrived := make(chan struct{})
	go func() {
		arrived.Wait()
		close(bothArrived)
	}()

	fn := func(nodeID string, act cueactivation.Activation) cueActivationDispatchOutcome {
		arrived.Done()
		select {
		case <-bothArrived:
		case <-time.After(2 * time.Second):
			t.Errorf("node %q returned without ever observing the other node's own fn call start; a sequential implementation deadlocks here instead of racing to pass", nodeID)
		}
		return cueActivationDispatchOutcome{NodeID: nodeID, Dispatched: true, Confirmed: true}
	}

	done := make(chan []cueActivationDispatchOutcome, 1)
	go func() {
		done <- dispatchCueActivationsConcurrently(activations, fn)
	}()
	select {
	case out := <-done:
		byNode := map[string]cueActivationDispatchOutcome{}
		for _, o := range out {
			byNode[o.NodeID] = o
		}
		if !byNode["a"].Confirmed || !byNode["b"].Confirmed {
			t.Fatalf("outcomes = %+v, want both nodes confirmed", out)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("dispatchCueActivationsConcurrently never returned within 3s: a sequential implementation deadlocks forever at the mutual barrier")
	}
}

// TestDispatchCueActivationsConcurrentlyRecoversAPanicPerNode proves a
// panicking per-node fn call never takes the whole coordinator process
// down (the process crashing is exactly what an unrecovered panic in a
// launched goroutine does) and never corrupts another node's own outcome:
// the panicking node's own result carries an Err, and the other node's
// own dispatched/confirmed outcome comes back untouched.
func TestDispatchCueActivationsConcurrentlyRecoversAPanicPerNode(t *testing.T) {
	activations := map[string]cueactivation.Activation{
		"panics": {ActivationID: "act-panics"},
		"fine":   {ActivationID: "act-fine"},
	}
	out := dispatchCueActivationsConcurrently(activations, func(nodeID string, act cueactivation.Activation) cueActivationDispatchOutcome {
		if nodeID == "panics" {
			panic("boom")
		}
		return cueActivationDispatchOutcome{NodeID: nodeID, Dispatched: true, Confirmed: true}
	})
	byNode := map[string]cueActivationDispatchOutcome{}
	for _, o := range out {
		byNode[o.NodeID] = o
	}
	if len(byNode) != 2 {
		t.Fatalf("outcomes = %+v, want exactly 2", out)
	}
	if byNode["panics"].Err == nil {
		t.Fatalf("panicking node outcome = %+v, want a non-nil Err recovered from the panic", byNode["panics"])
	}
	fine := byNode["fine"]
	if !fine.Dispatched || !fine.Confirmed || fine.Err != nil {
		t.Fatalf("fine node outcome = %+v, want dispatched and confirmed, untouched by the other node's panic", fine)
	}
}

// TestDispatchCueActivationsOneNodeRefusalDoesNotAffectTheOther proves
// per-node outcome independence at the full dispatchCueActivations level:
// one node's refusal is reported distinctly and does not alter or roll
// back the other's own confirmed outcome.
func TestDispatchCueActivationsOneNodeRefusalDoesNotAffectTheOther(t *testing.T) {
	now := testNow
	setup := newAudioDispatchTestSetup(t, fixedClock(now))
	activations := twoNodeScheduleFixture(t, setup, now)

	setup.pub.resultsByNode = map[string]mqttproto.ResultPayload{
		"audio-holder:cue.activate": cueActivationNodeResultPayload(true, cueActivationNodeOutcomeAuthorized),
		"audio-second:cue.activate": cueActivationNodeResultPayload(false, "stale-catalog"),
	}

	deps := setup.deps()
	deps.AssetManifests = setup.st
	h := &handlers{deps: deps.withDefaults(), clock: fixedClock(now), logger: testLogger()}
	issuer := cueActivationIssuer{PrincipalID: "system:cue-activation-loop:test"}

	outcomes := h.dispatchCueActivations(context.Background(), now, activations, issuer, nil)
	byNode := map[string]cueActivationDispatchOutcome{}
	for _, o := range outcomes {
		byNode[o.NodeID] = o
	}
	if len(byNode) != 2 {
		t.Fatalf("outcomes = %+v, want exactly 2, one per node", outcomes)
	}
	holder := byNode["audio-holder"]
	second := byNode["audio-second"]
	if !holder.Dispatched || !holder.Confirmed {
		t.Fatalf("audio-holder outcome = %+v, want dispatched and confirmed regardless of audio-second's refusal", holder)
	}
	if !second.Dispatched || second.Confirmed {
		t.Fatalf("audio-second outcome = %+v, want dispatched but NOT confirmed", second)
	}
	if second.NodeOutcome != "stale-catalog" {
		t.Fatalf("audio-second NodeOutcome = %q, want its own refusal reason, not silently dropped or merged with audio-holder's", second.NodeOutcome)
	}
}

// TestDispatchCueActivationsScheduledStartInPastOnOneNodeDoesNotStopTheOther
// covers MANAGER DECISION 2's own scenario: a scheduled multi-node
// activation where one node's own StartAt refuses because the shared
// instant has already passed on ITS clock (pkgaudio.ReasonScheduledStartInPast)
// by the time the dispatch reaches it. That refusal is reported as this
// one node's own outcome and reason; the other node still starts.
func TestDispatchCueActivationsScheduledStartInPastOnOneNodeDoesNotStopTheOther(t *testing.T) {
	now := testNow
	setup := newAudioDispatchTestSetup(t, fixedClock(now))
	activations := twoNodeScheduleFixture(t, setup, now)

	const holderReading = int64(1_700_000_000_000_000_000)
	setup.pub.resultsByNode = map[string]mqttproto.ResultPayload{
		"audio-holder:audio.session.prepare": scheduleProbeEvidenceResult(true, holderReading, ""),
		"audio-second:audio.session.prepare": scheduleProbeEvidenceResult(true, holderReading+5_000_000, ""),
		"audio-holder:cue.activate":          cueActivationNodeResultPayload(true, cueActivationNodeOutcomeAuthorized),
		"audio-second:cue.activate":          cueActivationNodeResultPayload(false, pkgaudio.ReasonScheduledStartInPast),
	}

	deps := setup.deps()
	deps.AssetManifests = setup.st
	h := &handlers{deps: deps.withDefaults(), clock: fixedClock(now), logger: testLogger()}
	issuer := cueActivationIssuer{PrincipalID: "system:cue-activation-loop:test"}

	outcomes := h.dispatchCueActivations(context.Background(), now, activations, issuer, nil)
	byNode := map[string]cueActivationDispatchOutcome{}
	for _, o := range outcomes {
		byNode[o.NodeID] = o
	}
	holder := byNode["audio-holder"]
	second := byNode["audio-second"]
	if !holder.Dispatched || !holder.Confirmed {
		t.Fatalf("audio-holder outcome = %+v, want dispatched and confirmed: one node's scheduled_start_in_past refusal must never stop the other", holder)
	}
	if second.Confirmed || second.NodeOutcome != pkgaudio.ReasonScheduledStartInPast {
		t.Fatalf("audio-second outcome = %+v, want NOT confirmed with NodeOutcome %q", second, pkgaudio.ReasonScheduledStartInPast)
	}
}

// TestReadScheduleProbeSerializesConcurrentAttemptsOnOneNode proves
// MANAGER DECISION 3's own fix: two concurrent scheduling attempts (a
// Playlist tick and an operator Fire, for example) that both resolve onto
// ONE node never interleave their apply, prepare, and clear calls on
// cueactivation.ScheduleProbeSessionID, that one fixed session id per
// node.
//
// It forces a GENUINE overlap rather than inferring serialization from a
// contiguous dispatch order: with nothing to force the two attempts to
// actually race, a missing lock would still very likely produce a
// contiguous AAABBB block anyway, since attempt A's own synchronous
// dispatches would typically finish before attempt B's goroutine is even
// scheduled. Instead, attempt A's own apply is held mid-flight (via the
// fake publisher's onAwaitResponse hook, which fires BEFORE Publish, so
// nothing of A's is recorded yet), attempt B is then launched and given
// a real window to run, and THE KEY ASSERTION is that B has dispatched
// NOTHING while A is still held: with the lock removed, B has nothing
// stopping it from dispatching immediately, and this would catch that
// directly, not infer it from a sequence a lucky non-overlapping run
// would also produce.
func TestReadScheduleProbeSerializesConcurrentAttemptsOnOneNode(t *testing.T) {
	setup := newAudioDispatchTestSetup(t, fixedClock(testNow))
	h := &handlers{deps: setup.deps().withDefaults(), clock: fixedClock(testNow), logger: testLogger()}
	issuer := cueActivationIssuer{PrincipalID: "system:test"}

	mediaA := pkgaudio.MediaRef{AssetID: "asset-a", RuntimeFilename: "a.wav"}
	mediaB := pkgaudio.MediaRef{AssetID: "asset-b", RuntimeFilename: "b.wav"}
	nowA := testNow
	nowB := testNow.Add(time.Millisecond) // distinct so the two attempts' invocation keys never collide

	var awaitCount atomic.Int64
	aEnteredApply := make(chan struct{})
	holdA := make(chan struct{})
	setup.pub.onAwaitResponse = func() {
		if awaitCount.Add(1) == 1 {
			close(aEnteredApply)
			<-holdA
		}
	}

	aDone := make(chan struct{})
	go func() {
		defer close(aDone)
		h.readScheduleProbe(context.Background(), nowA, "shared-node", mediaA, issuer)
	}()

	select {
	case <-aEnteredApply:
	case <-time.After(2 * time.Second):
		t.Fatal("attempt A's own apply dispatch never reached AwaitResponse")
	}

	bDone := make(chan struct{})
	go func() {
		defer close(bDone)
		h.readScheduleProbe(context.Background(), nowB, "shared-node", mediaB, issuer)
	}()

	// A real window for B to dispatch if the lock is missing: the fake
	// publisher resolves every unheld call near-instantly, so this is
	// far more time than an unlocked B would need to complete all three
	// of its own dispatches.
	time.Sleep(150 * time.Millisecond)
	for _, d := range setup.pub.dispatchedSnapshot() {
		if d.NodeID == "shared-node" {
			close(holdA)
			t.Fatalf("node %q already dispatched %q while attempt A's own apply is still held: the per-node lock did not block attempt B", d.NodeID, d.Action)
		}
	}
	close(holdA)

	select {
	case <-aDone:
	case <-time.After(2 * time.Second):
		t.Fatal("attempt A never completed after being released")
	}
	select {
	case <-bDone:
	case <-time.After(2 * time.Second):
		t.Fatal("attempt B never completed")
	}

	var forNode []dispatchedAudioCommand
	for _, d := range setup.pub.dispatchedSnapshot() {
		if d.NodeID == "shared-node" {
			forNode = append(forNode, d)
		}
	}
	if len(forNode) != 6 {
		t.Fatalf("dispatched %d commands for shared-node, want 6 (apply, prepare, clear per attempt)", len(forNode))
	}
	labelOf := func(d dispatchedAudioCommand) byte {
		key, _ := d.Params["invocationId"].(string)
		switch {
		case strings.Contains(key, nowA.Format(time.RFC3339Nano)):
			return 'A'
		case strings.Contains(key, nowB.Format(time.RFC3339Nano)):
			return 'B'
		default:
			return '?'
		}
	}
	var sequence []byte
	clears := 0
	for _, d := range forNode {
		sequence = append(sequence, labelOf(d))
		if d.Action == "audio.session.clear" {
			clears++
		}
	}
	got := string(sequence)
	if got != "AAABBB" && got != "BBBAAA" {
		t.Fatalf("dispatch order = %q, want AAABBB or BBBAAA: one attempt's whole apply-prepare-clear cycle must complete before the other's begins", got)
	}
	if clears != 2 {
		t.Fatalf("audio.session.clear dispatched %d times, want 2: both attempts must clear the probe", clears)
	}
}

// TestReadScheduleProbeGivesUpOnAHungApplyWithinTheStepTimeout proves
// finding 1's own fix: a node whose apply dispatch never answers costs
// this attempt at most about one scheduleProbeStepTimeout, never
// anywhere near audioCommandConfirmDeadline's own 15s, let alone the
// roughly 30s the concrete failure case (apply plus a deferred clear)
// named.
func TestReadScheduleProbeGivesUpOnAHungApplyWithinTheStepTimeout(t *testing.T) {
	setup := newAudioDispatchTestSetup(t, fixedClock(testNow))
	h := &handlers{deps: setup.deps().withDefaults(), clock: fixedClock(testNow), logger: testLogger()}
	issuer := cueActivationIssuer{PrincipalID: "system:test"}
	media := pkgaudio.MediaRef{AssetID: "asset-x", RuntimeFilename: "x.wav"}

	release := make(chan struct{})
	setup.pub.onAwaitResponse = func() { <-release }
	defer close(release)

	start := time.Now()
	evidence, elapsed := h.readScheduleProbe(context.Background(), testNow, "hung-node", media, issuer)
	took := time.Since(start)

	if evidence != nil || elapsed != 0 {
		t.Fatalf("readScheduleProbe = (%v, %v), want (nil, 0) for a node that never responds", evidence, elapsed)
	}
	if took > 2*scheduleProbeStepTimeout {
		t.Fatalf("readScheduleProbe took %v to return, want at most about one step timeout (%v): a hung node must not cost anywhere near its own 15s dispatch deadline", took, scheduleProbeStepTimeout)
	}
}

// TestReadScheduleProbeNeverClearsBeforeALateApplyLands is the ordering
// hazard a race-and-abandon design must close: [audio.Manager.Clear] on a
// session that does not exist yet is a no-op, so if clear reached the
// agent BEFORE a since-delayed apply, the apply's own late arrival would
// create a session nobody is left watching, a permanently loaded,
// never-cleared probe session on a real show node. This proves
// audio.session.clear is never dispatched while the apply it must follow
// is still in flight, and that it IS dispatched once that late apply
// finally resolves.
func TestReadScheduleProbeNeverClearsBeforeALateApplyLands(t *testing.T) {
	setup := newAudioDispatchTestSetup(t, fixedClock(testNow))
	h := &handlers{deps: setup.deps().withDefaults(), clock: fixedClock(testNow), logger: testLogger()}
	issuer := cueActivationIssuer{PrincipalID: "system:test"}
	media := pkgaudio.MediaRef{AssetID: "asset-x", RuntimeFilename: "x.wav"}

	release := make(chan struct{})
	var held atomic.Bool
	setup.pub.onAwaitResponse = func() {
		if held.CompareAndSwap(false, true) {
			<-release
		}
	}

	evidence, elapsed := h.readScheduleProbe(context.Background(), testNow, "late-node", media, issuer)
	if evidence != nil || elapsed != 0 {
		t.Fatalf("readScheduleProbe = (%v, %v), want (nil, 0): the apply step's own wait must have timed out", evidence, elapsed)
	}
	for _, d := range setup.pub.dispatchedSnapshot() {
		if d.Action == "audio.session.clear" {
			close(release)
			t.Fatalf("audio.session.clear already dispatched while the apply it must follow is still in flight; this is exactly the ordering that orphans a probe session")
		}
	}

	close(release)
	deadline := time.Now().Add(2 * time.Second)
	for {
		found := false
		for _, d := range setup.pub.dispatchedSnapshot() {
			if d.Action == "audio.session.clear" {
				found = true
			}
		}
		if found {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("audio.session.clear was never dispatched after the late apply resolved")
		}
		time.Sleep(5 * time.Millisecond)
	}
}

// putMultiTargetAudioCueForTest writes ONE show.cue whose audio output
// names both nodeIDs in outputs.audio.targets, the real ADR-049
// multi-target mechanism, resolved through assetsync.ResolveCueCatalog's
// own normal per-node placement, never a synthesized activations map.
func putMultiTargetAudioCueForTest(t *testing.T, st *store.Store, cueID, showID string, nodeIDs []string) {
	t.Helper()
	payload, err := config.EncodeShowCuePayload(config.ShowCuePayload{
		Show: showID, Name: cueID,
		Outputs: config.ShowCueOutputs{Audio: &config.ShowCueAudioOutput{Asset: "asset-" + cueID, Targets: nodeIDs}},
	})
	if err != nil {
		t.Fatalf("encode show.cue payload: %v", err)
	}
	putConfigForTest(t, st, config.ShowCueConfigKind, cueID, payload)
}

// TestScheduleCueActivationsWithARealMultiTargetCueStartsBothNodesTogether
// is finding 9's own acceptance proof: ONE real show.cue whose
// outputs.audio.targets names two nodes, resolved through
// cueactivate.ResolveDirectCueActivations (the same catalog path a real
// operator Fire uses), scheduled through scheduleCueActivations, and
// both nodes must receive the identical instant and the identical
// position. A fixture of two separate single-target Cues (this file's
// other fixtures) can prove scheduleCueActivations' own logic works, but
// it can never prove THIS: that the multi-target schema itself resolves
// one Cue onto both nodes' catalogs the way ADR-049 decision 1 says it
// does.
func TestScheduleCueActivationsWithARealMultiTargetCueStartsBothNodesTogether(t *testing.T) {
	now := testNow
	setup := newAudioDispatchTestSetup(t, fixedClock(now))
	const showID, cueID = "halloween-2026", "cue-multi"
	nodeIDs := []string{"audio-holder", "audio-second"}

	putShowForTest(t, setup.st, showID, "Halloween 2026")
	putAudioNodeForTest(t, setup.st, "audio-holder")
	putAudioNodeNoLTCForTest(t, setup.st, "audio-second")
	declareNodeForTest(t, setup.st, "audio-holder")
	declareNodeForTest(t, setup.st, "audio-second")
	putFreshReportForTest(t, setup.st, "audio-holder", now)
	putFreshReportForTest(t, setup.st, "audio-second", now)
	putMultiTargetAudioCueForTest(t, setup.st, cueID, showID, nodeIDs)
	putAuthorizedAudioAssetForTest(t, setup.st, showID, cueID, "audio-holder", now)
	putAuthorizedAudioAssetForTest(t, setup.st, showID, cueID, "audio-second", now)
	putPlaylistForTest(t, setup.st, "playlist-multi", config.ShowPlaylistPayload{
		Show: showID, Name: "Main", Runner: config.ShowPlaylistRunnerFPP,
		MismatchPolicy: config.ShowPlaylistMismatchPolicyHold,
		FPP:            &config.ShowPlaylistFPPBinding{InstanceUUID: "inst-1", PlaylistName: "Main", PlaylistHash: hash64ForTest("70a11")},
		Entries: []config.ShowPlaylistEntry{
			{ID: "entry-multi", Cue: cueID, FPP: &config.ShowPlaylistEntryFPP{Section: "mainPlaylist", Position: 0}},
		},
	})
	putActiveShowForTest(t, setup.st, showID)

	activations, err := cueactivate.ResolveDirectCueActivations(context.Background(), setup.st, now, cueID, "test-runner", 1)
	if err != nil {
		t.Fatalf("ResolveDirectCueActivations: %v", err)
	}
	if len(activations) != 2 {
		t.Fatalf("activations = %+v, want exactly 2 nodes resolved from the multi-target Cue", activations)
	}
	for _, nodeID := range nodeIDs {
		act, ok := activations[nodeID]
		if !ok {
			t.Fatalf("activations has no entry for %q", nodeID)
		}
		if act.CueID != cueID {
			t.Fatalf("node %q CueID = %q, want %q: both nodes must resolve the SAME Cue, not a synthesized per-node one", nodeID, act.CueID, cueID)
		}
	}

	const holderReading = int64(1_700_000_000_000_000_000)
	setup.pub.resultsByNode = map[string]mqttproto.ResultPayload{
		"audio-holder:audio.session.prepare": scheduleProbeEvidenceResult(true, holderReading, ""),
		"audio-second:audio.session.prepare": scheduleProbeEvidenceResult(true, holderReading+5_000_000, ""),
	}

	deps := setup.deps()
	deps.AssetManifests = setup.st
	h := &handlers{deps: deps.withDefaults(), clock: fixedClock(now), logger: testLogger()}
	issuer := cueActivationIssuer{PrincipalID: "system:test"}

	h.scheduleCueActivations(context.Background(), now, activations, issuer, nil)

	holder := activations["audio-holder"]
	second := activations["audio-second"]
	if holder.ScheduledAtNs == nil || second.ScheduledAtNs == nil {
		t.Fatalf("ScheduledAtNs = %v / %v, want both set", holder.ScheduledAtNs, second.ScheduledAtNs)
	}
	if *holder.ScheduledAtNs != *second.ScheduledAtNs {
		t.Fatalf("ScheduledAtNs differ: holder=%d second=%d, want the identical shared instant", *holder.ScheduledAtNs, *second.ScheduledAtNs)
	}
	if holder.PositionMS != second.PositionMS {
		t.Fatalf("PositionMS differ: holder=%d second=%d, want identical", holder.PositionMS, second.PositionMS)
	}
}
