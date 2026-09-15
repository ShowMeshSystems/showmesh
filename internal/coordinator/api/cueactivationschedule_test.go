package api

import (
	"context"
	"encoding/json"
	"strconv"
	"testing"
	"time"

	"github.com/showmeshsystems/showmesh/internal/coordinator/config"
	"github.com/showmeshsystems/showmesh/internal/coordinator/store"
	pkgaudio "github.com/showmeshsystems/showmesh/pkg/audio"
	"github.com/showmeshsystems/showmesh/pkg/cueactivation"
	"github.com/showmeshsystems/showmesh/pkg/mqttproto"
)

// This file proves ADR-049 decision 3's own coordinator-side scheduling
// step. show.cue's audio output is still single-target pre-merge (the
// parallel claude/sm-628-cue-targets-contract lane owns the multi-target
// schema), so a real single Cue cannot resolve on two nodes' catalogs yet
// — the task's own inventory names this and authorizes faking it in
// tests. These fixtures fake it by giving each of two nodes its OWN
// legitimately single-targeted Cue and building one Activation per node
// for its own Cue: scheduleCueActivations never inspects whether the
// Activations in one batch share a CueID, only whether each node's own
// resolved outputs are audio-bearing, so this is indistinguishable from
// its own code's perspective from the eventual shared-CueID case.

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
// explicitly targets nodeID — the one node this Cue resolves on,
// pre-merge (assetsync.audioTargets.Owns's own single-target rule).
func putTargetedAudioCueForTest(t *testing.T, st *store.Store, cueID, showID, nodeID string) {
	t.Helper()
	payload, err := config.EncodeShowCuePayload(config.ShowCuePayload{
		Show: showID, Name: cueID,
		Outputs: config.ShowCueOutputs{Audio: &config.ShowCueAudioOutput{Asset: "asset-" + cueID, Target: nodeID}},
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
	// scoping rule) — this playlist exists only to satisfy that, its
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
// provider is not locked), both nodes are left WITHOUT an instant — they
// start on arrival — and the batch reports the concrete reason, never a
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
// round at all — no instant, no probe dispatch, nothing added to a
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

// TestDispatchCueActivationsConcurrentlyDoesNotLetOneNodeDelayAnother is a
// direct, deterministic proof of dispatchCueActivationsConcurrently's own
// contract: a node whose fn call is still blocked does not hold up
// another node's fn call returning, or the other's outcome being
// available, before the slow one unblocks — ADR-049 decision 3's "one
// node refusing or failing never stops, cancels, or rolls back the
// others."
func TestDispatchCueActivationsConcurrentlyDoesNotLetOneNodeDelayAnother(t *testing.T) {
	release := make(chan struct{})
	fastDone := make(chan struct{})
	activations := map[string]cueactivation.Activation{
		"slow": {ActivationID: "act-slow"},
		"fast": {ActivationID: "act-fast"},
	}

	go func() {
		out := dispatchCueActivationsConcurrently(activations, func(nodeID string, act cueactivation.Activation) cueActivationDispatchOutcome {
			if nodeID == "slow" {
				<-release
				return cueActivationDispatchOutcome{NodeID: nodeID, Dispatched: true, Confirmed: true}
			}
			close(fastDone)
			return cueActivationDispatchOutcome{NodeID: nodeID, Dispatched: true, Confirmed: true}
		})
		_ = out
	}()

	select {
	case <-fastDone:
	case <-time.After(2 * time.Second):
		t.Fatal("the fast node's own dispatch never returned; it is blocked behind the slow node's")
	}
	close(release)
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
