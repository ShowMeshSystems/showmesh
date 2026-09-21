package api

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
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

// putAudioNodeNoLTCForTest declares nodeID's own audio.node with role
// "program", not "program+ltc": [handlers.nodeHoldsMediaClock] reports
// false for it on that basis alone, unlike putAudioNodeForTest's own
// fixed "program+ltc" role. It also carries no LTC route, matching a
// program-only node's real shape (ValidateAudioNodePlacement never allows
// an ltcRoute the node's own advertised routes don't support), but the
// missing LTCRoute is incidental to this fixture's own purpose now: role
// alone is what nodeHoldsMediaClock reads. An explicit Role is required
// here specifically because absent Role decodes to
// [config.AudioNodeRoleDefault] ("program+ltc"), which would silently
// make this node a SECOND holder alongside putAudioNodeForTest's.
func putAudioNodeNoLTCForTest(t *testing.T, st *store.Store, nodeID string) {
	t.Helper()
	raw, err := config.EncodeAudioNodePayload(config.AudioNodePayload{
		ProgramRoute:    "usb-interface",
		ProgramChannels: []int{1, 2},
		Role:            config.AudioNodeRoleProgram,
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

// awaitFinishScheduleProbeAfterForTest hooks finishScheduleProbeAfterDoneForTest
// so a test that provokes that background goroutine can wait for its
// clear and lock release to finish before its own store teardown runs.
func awaitFinishScheduleProbeAfterForTest(t *testing.T) func() {
	t.Helper()
	done := make(chan struct{}, 1)
	prev := finishScheduleProbeAfterDoneForTest
	finishScheduleProbeAfterDoneForTest = func() {
		select {
		case done <- struct{}{}:
		default:
		}
	}
	t.Cleanup(func() { finishScheduleProbeAfterDoneForTest = prev })
	return func() {
		t.Helper()
		select {
		case <-done:
		case <-time.After(scheduleProbeStepTimeout + 2*time.Second):
			t.Fatal("finishScheduleProbeAfter never signaled completion")
		}
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

// TestReadScheduleProbeWaitsBrieflyForAHealthyOverlappingAttempt proves
// finding 2's own tightened rule: a second scheduling attempt that lands
// while a healthy first attempt on the SAME node is still finishing (a
// Playlist tick and an operator Fire within a few hundred milliseconds,
// for example) waits for it, bounded by scheduleProbeStepTimeout, and
// succeeds once the first attempt releases the lock. Reporting the
// second attempt unaligned over a lock it only needed to wait a moment
// for would be a worse show than the wait itself.
//
// Attempt A's own apply is held for 50ms, well under
// scheduleProbeStepTimeout, modeling a healthy attempt rather than a
// hang; the fake publisher's onAwaitResponse hook fires BEFORE Publish,
// so B is guaranteed to start while A still holds the lock, not a lucky
// non-overlapping run.
func TestReadScheduleProbeWaitsBrieflyForAHealthyOverlappingAttempt(t *testing.T) {
	setup := newAudioDispatchTestSetup(t, fixedClock(testNow))
	h := &handlers{deps: setup.deps().withDefaults(), clock: fixedClock(testNow), logger: testLogger()}
	issuer := cueActivationIssuer{PrincipalID: "system:test"}

	mediaA := pkgaudio.MediaRef{AssetID: "asset-a", RuntimeFilename: "a.wav"}
	mediaB := pkgaudio.MediaRef{AssetID: "asset-b", RuntimeFilename: "b.wav"}
	nowA := testNow
	nowB := testNow.Add(time.Millisecond) // distinct so the two attempts' invocation keys never collide

	const holderReading = int64(1_700_000_000_000_000_000)
	setup.pub.resultsByNode = map[string]mqttproto.ResultPayload{
		"shared-node:audio.session.prepare": scheduleProbeEvidenceResult(true, holderReading, ""),
	}

	var awaitCount atomic.Int64
	aEnteredApply := make(chan struct{})
	setup.pub.onAwaitResponse = func() {
		if awaitCount.Add(1) == 1 {
			close(aEnteredApply)
			time.Sleep(50 * time.Millisecond)
		}
	}

	aDone := make(chan struct{})
	go func() {
		defer close(aDone)
		if _, _, err := h.readScheduleProbe(context.Background(), nowA, "shared-node", mediaA, issuer); err != nil {
			t.Errorf("attempt A's own readScheduleProbe = %v, want nil", err)
		}
	}()

	select {
	case <-aEnteredApply:
	case <-time.After(2 * time.Second):
		t.Fatal("attempt A's own apply dispatch never reached AwaitResponse")
	}

	start := time.Now()
	evidence, _, err := h.readScheduleProbe(context.Background(), nowB, "shared-node", mediaB, issuer)
	took := time.Since(start)

	if err != nil {
		t.Fatalf("readScheduleProbe for B = (%v, _, %v), want nil: a brief wait for a healthy overlapping attempt must still succeed", evidence, err)
	}
	if evidence == nil {
		t.Fatal("readScheduleProbe for B returned nil evidence alongside a nil error")
	}
	if took >= scheduleProbeStepTimeout {
		t.Fatalf("readScheduleProbe for B took %s, want well under scheduleProbeStepTimeout (%s): it should succeed once A releases, not wait out the whole bound", took, scheduleProbeStepTimeout)
	}

	select {
	case <-aDone:
	case <-time.After(2 * time.Second):
		t.Fatal("attempt A never completed")
	}
}

// TestReadScheduleProbeReportsBusyForAGenuinelyStuckNodeWithinTheBound
// proves a genuinely stuck node still costs a second attempt at most
// scheduleProbeStepTimeout, never an unbounded wait.
func TestReadScheduleProbeReportsBusyForAGenuinelyStuckNodeWithinTheBound(t *testing.T) {
	setup := newAudioDispatchTestSetup(t, fixedClock(testNow))
	h := &handlers{deps: setup.deps().withDefaults(), clock: fixedClock(testNow), logger: testLogger()}
	issuer := cueActivationIssuer{PrincipalID: "system:test"}
	waitFinish := awaitFinishScheduleProbeAfterForTest(t)

	mediaA := pkgaudio.MediaRef{AssetID: "asset-a", RuntimeFilename: "a.wav"}
	mediaB := pkgaudio.MediaRef{AssetID: "asset-b", RuntimeFilename: "b.wav"}
	nowA := testNow
	nowB := testNow.Add(time.Millisecond)

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
		_, _, _ = h.readScheduleProbe(context.Background(), nowA, "shared-node", mediaA, issuer)
	}()

	select {
	case <-aEnteredApply:
	case <-time.After(2 * time.Second):
		t.Fatal("attempt A's own apply dispatch never reached AwaitResponse")
	}

	start := time.Now()
	_, _, err := h.readScheduleProbe(context.Background(), nowB, "shared-node", mediaB, issuer)
	took := time.Since(start)
	close(holdA)

	if err == nil {
		t.Fatal("readScheduleProbe for B = nil error, want busy while attempt A is genuinely stuck")
	}
	if took < scheduleProbeStepTimeout {
		t.Fatalf("readScheduleProbe for B took %s, want at least scheduleProbeStepTimeout (%s): it must actually wait, not fail immediately", took, scheduleProbeStepTimeout)
	}
	if took > scheduleProbeStepTimeout+2*time.Second {
		t.Fatalf("readScheduleProbe for B took %s, want close to scheduleProbeStepTimeout (%s): the wait must stay bounded", took, scheduleProbeStepTimeout)
	}

	select {
	case <-aDone:
	case <-time.After(2 * time.Second):
		t.Fatal("attempt A never completed after being released")
	}
	waitFinish()
}

// TestDispatchProbeStepRecoversAPanicInTheUnderlyingDispatch proves
// finding 1's own fix: dispatchProbeStep runs executeAudioSessionDispatch
// on its own goroutine with no caller left to recover it once
// dispatchProbeStep itself has returned, so an unrecovered panic there
// (a store, audit, or broker defect) would take down the whole
// coordinator process mid show, not just cost this one node's own
// reading. The fake publisher's onAwaitResponse hook is made to panic
// directly inside AwaitResponse, the same call executeAudioSessionDispatch
// itself makes, so this exercises the real call chain, not a stand-in.
//
// The test surviving to completion at all IS the process-survival
// evidence: a real unrecovered goroutine panic would crash this whole
// test binary, not just fail an assertion.
func TestDispatchProbeStepRecoversAPanicInTheUnderlyingDispatch(t *testing.T) {
	setup := newAudioDispatchTestSetup(t, fixedClock(testNow))
	h := &handlers{deps: setup.deps().withDefaults(), clock: fixedClock(testNow), logger: testLogger()}
	issuer := cueActivationIssuer{PrincipalID: "system:test"}

	setup.pub.onAwaitResponse = func() { panic("simulated broker panic") }

	pending := h.dispatchProbeStep(context.Background(), testNow, AudioDispatchInput{
		Action: "audio.session.apply", NodeID: "panicking-node", SessionID: cueactivation.ScheduleProbeSessionID,
		Params:   map[string]any{"sessionId": cueactivation.ScheduleProbeSessionID},
		IssuerID: issuer.PrincipalID, IssuerName: issuer.PrincipalName,
	})

	select {
	case out := <-pending:
		if out.err == nil {
			t.Fatal("dispatchProbeStep outcome.err = nil, want an error naming the panic")
		}
		if !strings.Contains(out.err.Error(), "panicking-node") {
			t.Fatalf("dispatchProbeStep outcome.err = %q, want it to name the node", out.err.Error())
		}
	case <-time.After(2 * time.Second):
		t.Fatal("dispatchProbeStep never delivered an outcome after the underlying dispatch panicked")
	}
}

// TestReadScheduleProbeLockIsAcquirableAfterAPanicInTheLockedSection
// proves round 3's own fix: a panic anywhere in readScheduleProbe's own
// synchronous body between acquiring nodeID's own token and any return
// must still release it during unwinding, through the SAME deferred,
// handedOff-guarded release every ordinary return path uses. Before this
// fix, explicit unlocks on each path meant a panic between the lock and
// its matching unlock left that node's token held forever, and because
// finding 1's own recover keeps the process alive, every later
// scheduling attempt for that node would then wait out the full bound
// and still report busy, wedging the Playlist activation loop for that
// node permanently.
//
// This check's own acquire must release what it took: it only proves the
// token is free, it does not need to hold it afterward.
//
// Finding 1's own fix means a dispatch-side panic (broker, store, audit)
// can no longer reach this far: dispatchProbeStep's own goroutine recovers
// it first and turns it into an ordinary error. This is exactly why the
// guarantee still needs its own direct proof: scheduleProbeLockedSectionPanicForTest
// (nil in production) stands in for any OTHER cause synchronous code in
// this function's own body could someday panic from.
func TestReadScheduleProbeLockIsAcquirableAfterAPanicInTheLockedSection(t *testing.T) {
	setup := newAudioDispatchTestSetup(t, fixedClock(testNow))
	h := &handlers{deps: setup.deps().withDefaults(), clock: fixedClock(testNow), logger: testLogger()}
	issuer := cueActivationIssuer{PrincipalID: "system:test"}
	media := pkgaudio.MediaRef{AssetID: "asset-x", RuntimeFilename: "x.wav"}

	scheduleProbeLockedSectionPanicForTest = func() { panic("simulated locked-section panic") }
	defer func() { scheduleProbeLockedSectionPanicForTest = nil }()

	func() {
		defer func() {
			if r := recover(); r == nil {
				t.Fatal("readScheduleProbe did not panic; this test no longer exercises the locked section's own panic-safety")
			}
		}()
		_, _, _ = h.readScheduleProbe(context.Background(), testNow, "panicking-node", media, issuer)
	}()

	lock := scheduleProbeNodeLock("panicking-node")
	select {
	case lock <- struct{}{}:
		<-lock
	default:
		t.Fatal("panicking-node's own probe lock is still held after the panic unwound past readScheduleProbe: a later scheduling attempt for this node would block forever")
	}
}

// panicOnWarnHandler is a slog.Handler whose Handle panics on its first
// record only, then succeeds: a way to force a genuine panic inside one
// real logWarn call, without a nil-by-default test-only hook in
// production code, while still letting the recover's OWN follow-up
// logWarn call succeed rather than compounding into a second panic.
type panicOnWarnHandler struct{ fired atomic.Bool }

func (h *panicOnWarnHandler) Enabled(context.Context, slog.Level) bool { return true }
func (h *panicOnWarnHandler) Handle(context.Context, slog.Record) error {
	if !h.fired.Swap(true) {
		panic("simulated logging panic")
	}
	return nil
}
func (h *panicOnWarnHandler) WithAttrs(_ []slog.Attr) slog.Handler { return h }
func (h *panicOnWarnHandler) WithGroup(_ string) slog.Handler      { return h }

// TestFinishScheduleProbeAfterRecoversAPanic proves this round's own
// fix: finishScheduleProbeAfter is started with a bare go statement, the
// identical shape dispatchProbeStep had before its own round 3 fix, so a
// panic anywhere in its own body, its logging call or a revision helper,
// not only the dispatch that dispatchProbeStep's own recover already
// shields, must not kill the coordinator process, and must still release
// nodeID's own lock.
//
// A panicking slog.Handler forces clearScheduleProbe's own logWarn call
// to panic for real, exercising the actual call chain rather than a
// stand-in. Calling finishScheduleProbeAfter directly, not via go, is
// equivalent: recover() catches a panic anywhere in its own goroutine's
// call stack regardless of how that goroutine was started, and a
// synchronous call keeps this test simple to reason about.
func TestFinishScheduleProbeAfterRecoversAPanic(t *testing.T) {
	setup := newAudioDispatchTestSetup(t, fixedClock(testNow))
	h := &handlers{deps: setup.deps().withDefaults(), clock: fixedClock(testNow), logger: slog.New(&panicOnWarnHandler{})}
	issuer := cueActivationIssuer{PrincipalID: "system:test"}

	setup.pub.awaitErr = errors.New("simulated broker failure")

	pending := make(chan audioDispatchOutcome, 1)
	pending <- audioDispatchOutcome{}
	mu := make(chan struct{}, 1)
	mu <- struct{}{} // the token this caller already holds, being handed off

	h.finishScheduleProbeAfter(context.Background(), testNow, "panicking-node", issuer, pending, mu)

	select {
	case mu <- struct{}{}:
		<-mu
	default:
		t.Fatal("panicking-node's own probe lock is still held after finishScheduleProbeAfter panicked: a later scheduling attempt for this node would wait out the full bound and still report busy")
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
	waitFinish := awaitFinishScheduleProbeAfterForTest(t)

	release := make(chan struct{})
	setup.pub.onAwaitResponse = func() { <-release }

	start := time.Now()
	evidence, elapsed, err := h.readScheduleProbe(context.Background(), testNow, "hung-node", media, issuer)
	took := time.Since(start)

	if evidence != nil || elapsed != 0 || err == nil {
		t.Fatalf("readScheduleProbe = (%v, %v, %v), want (nil, 0, <non-nil>) for a node that never responds", evidence, elapsed, err)
	}
	if took > 2*scheduleProbeStepTimeout {
		t.Fatalf("readScheduleProbe took %v to return, want at most about one step timeout (%v): a hung node must not cost anywhere near its own 15s dispatch deadline", took, scheduleProbeStepTimeout)
	}
	close(release)
	waitFinish()
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
	waitFinish := awaitFinishScheduleProbeAfterForTest(t)

	release := make(chan struct{})
	var held atomic.Bool
	setup.pub.onAwaitResponse = func() {
		if held.CompareAndSwap(false, true) {
			<-release
		}
	}

	evidence, elapsed, err := h.readScheduleProbe(context.Background(), testNow, "late-node", media, issuer)
	if evidence != nil || elapsed != 0 || err == nil {
		t.Fatalf("readScheduleProbe = (%v, %v, %v), want (nil, 0, <non-nil>): the apply step's own wait must have timed out", evidence, elapsed, err)
	}
	for _, d := range setup.pub.dispatchedSnapshot() {
		if d.Action == "audio.session.clear" {
			close(release)
			t.Fatalf("audio.session.clear already dispatched while the apply it must follow is still in flight; this is exactly the ordering that orphans a probe session")
		}
	}

	close(release)
	waitFinish()
	found := false
	for _, d := range setup.pub.dispatchedSnapshot() {
		if d.Action == "audio.session.clear" {
			found = true
		}
	}
	if !found {
		t.Fatal("audio.session.clear was never dispatched after the late apply resolved")
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

// TestScheduleCueActivationsToleratesARealisticSlowPrepare proves the
// probe bound tolerates a realistic slow prepare (1500ms, between the
// 1.4s and 2.4s rehearsal-rig measurements) without falling back unaligned.
func TestScheduleCueActivationsToleratesARealisticSlowPrepare(t *testing.T) {
	now := testNow
	setup := newAudioDispatchTestSetup(t, fixedClock(now))
	activations := twoNodeScheduleFixture(t, setup, now)

	const holderReading = int64(1_700_000_000_000_000_000)
	setup.pub.resultsByNode = map[string]mqttproto.ResultPayload{
		"audio-holder:audio.session.prepare": scheduleProbeEvidenceResult(true, holderReading, ""),
		"audio-second:audio.session.prepare": scheduleProbeEvidenceResult(true, holderReading+5_000_000, ""),
	}
	var awaitCount atomic.Int64
	setup.pub.onAwaitResponse = func() {
		if awaitCount.Add(1) <= 4 {
			time.Sleep(1500 * time.Millisecond)
		}
	}

	deps := setup.deps()
	deps.AssetManifests = setup.st
	h := &handlers{deps: deps.withDefaults(), clock: fixedClock(now), logger: testLogger()}
	issuer := cueActivationIssuer{PrincipalID: "system:cue-activation-loop:test"}

	start := time.Now()
	h.scheduleCueActivations(context.Background(), now, activations, issuer, nil)
	took := time.Since(start)
	if took >= scheduleProbeStepTimeout {
		t.Fatalf("scheduleCueActivations took %s, want well under scheduleProbeStepTimeout (%s): a 1500ms step must not exhaust the bound", took, scheduleProbeStepTimeout)
	}

	holder := activations["audio-holder"]
	second := activations["audio-second"]
	if holder.ScheduledAtNs == nil || second.ScheduledAtNs == nil {
		t.Fatalf("ScheduledAtNs = %v / %v, want both set: a 1500ms step must not fall back to unaligned", holder.ScheduledAtNs, second.ScheduledAtNs)
	}
	if *holder.ScheduledAtNs != *second.ScheduledAtNs {
		t.Fatalf("ScheduledAtNs differ: holder=%d second=%d, want the identical shared instant", *holder.ScheduledAtNs, *second.ScheduledAtNs)
	}
	if holder.UnalignedReason != "" || second.UnalignedReason != "" {
		t.Fatalf("UnalignedReason = %q / %q, want both empty", holder.UnalignedReason, second.UnalignedReason)
	}
}

// TestScheduleCueActivationsProbesEachNodeExactlyOnce proves one
// media-clock probe sequence per activation: exactly one apply, one
// prepare, and one clear against the probe session for each node.
func TestScheduleCueActivationsProbesEachNodeExactlyOnce(t *testing.T) {
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

	counts := map[string]map[string]int{"audio-holder": {}, "audio-second": {}}
	for _, d := range setup.pub.dispatchedSnapshot() {
		if s, _ := d.Params["sessionId"].(string); s != cueactivation.ScheduleProbeSessionID {
			continue
		}
		if _, ok := counts[d.NodeID]; !ok {
			t.Fatalf("probe dispatch for unexpected node %q: %+v", d.NodeID, d)
		}
		counts[d.NodeID][d.Action]++
	}
	for _, nodeID := range []string{"audio-holder", "audio-second"} {
		for _, action := range []string{"audio.session.apply", "audio.session.prepare", "audio.session.clear"} {
			if got := counts[nodeID][action]; got != 1 {
				t.Fatalf("node %q action %q dispatched %d times against the probe session, want exactly 1", nodeID, action, got)
			}
		}
	}
}

// TestDispatchCueActivationsReplayTickProbesNothingAndDispatchesNothingNew
// proves a second tick over an unchanged activations map dispatches
// nothing new, answering entirely from what the first tick recorded.
func TestDispatchCueActivationsReplayTickProbesNothingAndDispatchesNothingNew(t *testing.T) {
	now := testNow
	setup := newAudioDispatchTestSetup(t, fixedClock(now))
	activations := twoNodeScheduleFixture(t, setup, now)

	const holderReading = int64(1_700_000_000_000_000_000)
	setup.pub.resultsByNode = map[string]mqttproto.ResultPayload{
		"audio-holder:audio.session.prepare": scheduleProbeEvidenceResult(true, holderReading, ""),
		"audio-second:audio.session.prepare": scheduleProbeEvidenceResult(true, holderReading+5_000_000, ""),
		"audio-holder:cue.activate":          cueActivationNodeResultPayload(true, cueActivationNodeOutcomeAuthorized),
		"audio-second:cue.activate":          cueActivationNodeResultPayload(true, cueActivationNodeOutcomeAuthorized),
	}

	deps := setup.deps()
	deps.AssetManifests = setup.st
	h := &handlers{deps: deps.withDefaults(), clock: fixedClock(now), logger: testLogger()}
	issuer := cueActivationIssuer{PrincipalID: "system:cue-activation-loop:test"}

	first := h.dispatchCueActivations(context.Background(), now, activations, issuer, nil)
	for _, o := range first {
		if !o.Confirmed {
			t.Fatalf("first tick outcome = %+v, want confirmed", o)
		}
	}
	firstCount := len(setup.pub.dispatchedSnapshot())
	if firstCount == 0 {
		t.Fatal("first tick dispatched nothing at all")
	}

	second := h.dispatchCueActivations(context.Background(), now, activations, issuer, nil)
	for _, o := range second {
		if !o.Confirmed {
			t.Fatalf("replay tick outcome = %+v, want confirmed (replayed from the recorded row)", o)
		}
	}
	secondSnapshot := setup.pub.dispatchedSnapshot()
	if len(secondSnapshot) != firstCount {
		t.Fatalf("replay tick dispatched %d additional commands, want 0: %+v",
			len(secondSnapshot)-firstCount, secondSnapshot[firstCount:])
	}
}

// TestScheduleCueActivationsPartialReplayGivesLateNodeTheRecordedInstant
// proves a late-joining audio-second gets audio-holder's own already
// recorded ScheduledAtNs, never a fresh probe, while the lead still covers it.
func TestScheduleCueActivationsPartialReplayGivesLateNodeTheRecordedInstant(t *testing.T) {
	now := testNow
	setup := newAudioDispatchTestSetup(t, fixedClock(now))
	activations := twoNodeScheduleFixture(t, setup, now)

	deps := setup.deps()
	deps.AssetManifests = setup.st
	h := &handlers{deps: deps.withDefaults(), clock: fixedClock(now), logger: testLogger()}
	issuer := cueActivationIssuer{PrincipalID: "system:cue-activation-loop:test"}

	holderAt := int64(1_700_000_005_000_000_000)
	holder := activations["audio-holder"]
	holder.ScheduledAtNs = &holderAt
	activations["audio-holder"] = holder
	setup.pub.resultsByNode = map[string]mqttproto.ResultPayload{
		"audio-holder:cue.activate": cueActivationNodeResultPayload(true, cueActivationNodeOutcomeAuthorized),
	}
	seed := h.dispatchOneCueActivation(context.Background(), now, "audio-holder", holder, issuer, nil)
	if !seed.Confirmed {
		t.Fatalf("seed dispatch for audio-holder = %+v, want confirmed", seed)
	}
	setup.pub.dispatched = nil // isolate what THIS tick's own scheduleCueActivations call dispatches

	// Well within the default audio.settings lead (deliveryBound 2000ms +
	// margin 1000ms = 3000ms): the recorded instant is still usable.
	h.scheduleCueActivations(context.Background(), now.Add(200*time.Millisecond), activations, issuer, nil)

	second := activations["audio-second"]
	if second.ScheduledAtNs == nil || *second.ScheduledAtNs != holderAt {
		t.Fatalf("audio-second ScheduledAtNs = %v, want %d (audio-holder's own recorded instant, reused without a fresh probe)", second.ScheduledAtNs, holderAt)
	}
	if second.UnalignedReason != "" {
		t.Fatalf("audio-second UnalignedReason = %q, want empty", second.UnalignedReason)
	}
	if got := activations["audio-holder"].ScheduledAtNs; got == nil || *got != holderAt {
		t.Fatalf("audio-holder ScheduledAtNs = %v, want unchanged at %d", got, holderAt)
	}
	if len(setup.pub.dispatchedSnapshot()) != 0 {
		t.Fatalf("dispatched %d commands during the partial-replay schedule call, want 0: no probe for either node", len(setup.pub.dispatchedSnapshot()))
	}
}

// TestScheduleCueActivationsPartialReplayLateNodeUnalignedAfterLeadElapses
// proves the same case past the point the recorded instant can be
// vouched for: audio-second reports unaligned, never a start already behind it.
func TestScheduleCueActivationsPartialReplayLateNodeUnalignedAfterLeadElapses(t *testing.T) {
	now := testNow
	setup := newAudioDispatchTestSetup(t, fixedClock(now))
	activations := twoNodeScheduleFixture(t, setup, now)

	deps := setup.deps()
	deps.AssetManifests = setup.st
	h := &handlers{deps: deps.withDefaults(), clock: fixedClock(now), logger: testLogger()}
	issuer := cueActivationIssuer{PrincipalID: "system:cue-activation-loop:test"}

	holderAt := int64(1_700_000_005_000_000_000)
	holder := activations["audio-holder"]
	holder.ScheduledAtNs = &holderAt
	activations["audio-holder"] = holder
	setup.pub.resultsByNode = map[string]mqttproto.ResultPayload{
		"audio-holder:cue.activate": cueActivationNodeResultPayload(true, cueActivationNodeOutcomeAuthorized),
	}
	seed := h.dispatchOneCueActivation(context.Background(), now, "audio-holder", holder, issuer, nil)
	if !seed.Confirmed {
		t.Fatalf("seed dispatch for audio-holder = %+v, want confirmed", seed)
	}
	setup.pub.dispatched = nil

	h.scheduleCueActivations(context.Background(), now.Add(time.Hour), activations, issuer, nil)

	second := activations["audio-second"]
	if second.ScheduledAtNs != nil {
		t.Fatalf("audio-second ScheduledAtNs = %v, want nil: the configured lead has long since elapsed", second.ScheduledAtNs)
	}
	if second.UnalignedReason == "" {
		t.Fatalf("audio-second UnalignedReason is empty, want a concrete reason naming it")
	}
	if !strings.Contains(second.UnalignedReason, "audio-second") {
		t.Fatalf("audio-second UnalignedReason = %q, want it to name the node", second.UnalignedReason)
	}
	if len(setup.pub.dispatchedSnapshot()) != 0 {
		t.Fatalf("dispatched %d commands during the partial-replay schedule call, want 0: no probe for either node", len(setup.pub.dispatchedSnapshot()))
	}
}

// TestDispatchOneCueActivationWaitsForAnOutstandingProbeBeforeTheRealDispatch
// proves a real cue.activate waits for an outstanding probe's own clear
// to resolve rather than racing it.
func TestDispatchOneCueActivationWaitsForAnOutstandingProbeBeforeTheRealDispatch(t *testing.T) {
	now := testNow
	setup := newAudioDispatchTestSetup(t, fixedClock(now))
	nodeID, act := cueActivationDispatchTestFixture(t, setup, now)
	putAuthorizedAudioAssetForTest(t, setup.st, act.Show, act.CueID, nodeID, now)
	act.CatalogRevision = resolvedCatalogRevisionForTest(t, setup.st, act.Show, nodeID)

	deps := setup.deps()
	deps.AssetManifests = setup.st
	h := &handlers{deps: deps.withDefaults(), clock: fixedClock(now), logger: testLogger()}
	issuer := cueActivationIssuer{PrincipalID: "system:test"}
	waitFinish := awaitFinishScheduleProbeAfterForTest(t)

	release := make(chan struct{})
	var held atomic.Bool
	setup.pub.onAwaitResponse = func() {
		if held.CompareAndSwap(false, true) {
			<-release
		}
	}
	setup.pub.result = cueActivationNodeResultPayload(true, cueActivationNodeOutcomeAuthorized)

	media := pkgaudio.MediaRef{AssetID: "asset-x", RuntimeFilename: "x.wav"}
	probeDone := make(chan struct{})
	go func() {
		defer close(probeDone)
		_, _, err := h.readScheduleProbe(context.Background(), now, nodeID, media, issuer)
		if err == nil {
			t.Errorf("readScheduleProbe = nil error, want a timeout: its own apply never answers within scheduleProbeStepTimeout")
		}
	}()
	select {
	case <-probeDone:
	case <-time.After(scheduleProbeStepTimeout + 2*time.Second):
		t.Fatal("readScheduleProbe never gave up on the hung apply")
	}

	realDone := make(chan cueActivationDispatchOutcome, 1)
	go func() {
		realDone <- h.dispatchOneCueActivation(context.Background(), now, nodeID, act, issuer, nil)
	}()

	select {
	case out := <-realDone:
		t.Fatalf("the real dispatch for %q returned (%+v) before its own outstanding probe was released; it must wait for the clear or the bound", nodeID, out)
	case <-time.After(50 * time.Millisecond):
		// Still waiting, as required.
	}

	close(release)

	select {
	case out := <-realDone:
		if !out.Confirmed {
			t.Fatalf("real dispatch for %q = %+v, want confirmed once the probe's own clear resolved", nodeID, out)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("the real dispatch never completed after the outstanding probe was released")
	}

	var clearIdx, activateIdx = -1, -1
	for i, d := range setup.pub.dispatchedSnapshot() {
		if d.NodeID != nodeID {
			continue
		}
		if d.Action == "audio.session.clear" && clearIdx == -1 {
			clearIdx = i
		}
		if d.Action == "cue.activate" && activateIdx == -1 {
			activateIdx = i
		}
	}
	if clearIdx == -1 {
		t.Fatalf("no audio.session.clear was ever dispatched for %q: the abandoned probe must still be cleared", nodeID)
	}
	if activateIdx == -1 {
		t.Fatalf("no cue.activate was ever dispatched for %q", nodeID)
	}
	if clearIdx > activateIdx {
		t.Fatalf("cue.activate (dispatch #%d) reached the wire before audio.session.clear (dispatch #%d) for %q", activateIdx, clearIdx, nodeID)
	}
	waitFinish()
}

// TestDispatchOneCueActivationSingleTargetUnaffectedByTheProbeGate proves
// a single-target activation finds its own probe lock uncontended and
// dispatches with no measurable added latency.
func TestDispatchOneCueActivationSingleTargetUnaffectedByTheProbeGate(t *testing.T) {
	now := testNow
	setup := newAudioDispatchTestSetup(t, fixedClock(now))
	nodeID, act := cueActivationDispatchTestFixture(t, setup, now)
	putAuthorizedAudioAssetForTest(t, setup.st, act.Show, act.CueID, nodeID, now)
	act.CatalogRevision = resolvedCatalogRevisionForTest(t, setup.st, act.Show, nodeID)
	setup.pub.result = cueActivationNodeResultPayload(true, cueActivationNodeOutcomeAuthorized)

	deps := setup.deps()
	deps.AssetManifests = setup.st
	h := &handlers{deps: deps.withDefaults(), clock: fixedClock(now), logger: testLogger()}
	issuer := cueActivationIssuer{PrincipalID: "system:cue-activation-loop:test"}

	start := time.Now()
	outcome := h.dispatchOneCueActivation(context.Background(), now, nodeID, act, issuer, nil)
	took := time.Since(start)
	if !outcome.Confirmed {
		t.Fatalf("outcome = %+v, want confirmed", outcome)
	}
	// A generous ceiling: this only needs to prove the gate did not
	// block, not bound ordinary dispatch latency under a slow test run.
	if took >= scheduleProbeStepTimeout/2 {
		t.Fatalf("dispatchOneCueActivation took %s, want well under scheduleProbeStepTimeout (%s): the probe-idle gate must be an uncontended, near-instant check for a node no probe ever touched", took, scheduleProbeStepTimeout)
	}
}
