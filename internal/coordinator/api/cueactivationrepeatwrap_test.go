package api

import (
	"context"
	"testing"
	"time"

	"github.com/showmeshsystems/showmesh/internal/coordinator/store"
	"github.com/showmeshsystems/showmesh/pkg/cueactivation"
	"github.com/showmeshsystems/showmesh/pkg/observation"
)

func repeatModeObs(instanceID string, value any, at time.Time) observation.Observation {
	return mustObs(observation.Measured(
		observation.ResourceRef{Kind: observation.ResourceFPP, ID: instanceID},
		observation.SignalID(nightSignalPlaylistRepeatMode), value, at,
		observation.WithValidFor(45*time.Second), observation.WithCollectedAt(at), observation.WithSource("fpp-rest"),
	))
}

func repeatWrapFixture(t *testing.T, now time.Time, obs ...observation.Observation) (*handlers, *audioDispatchTestSetup, string, cueactivation.Activation) {
	t.Helper()
	setup := newAudioDispatchTestSetup(t, fixedClock(now))
	nodeID, act := cuePrepareAheadTestFixture(t, setup, now)
	act.EntryID, act.CueID = "entry-2", "cue-2"
	setup.pub.result = cueActivationNodeResultPayload(true, cueActivationNodeOutcomeAuthorized)

	deps := setup.deps()
	deps.AssetManifests = setup.st
	deps.FPP = &fakeFPPLister{views: []FPPInstanceView{{InstanceID: "fpp-main", InstanceUUID: &store.FPPInstanceUUIDRecord{UUID: "inst-1"}}}}
	lister := &dynamicObservationLister{}
	lister.setObs(obs)
	deps.Observations = lister
	return &handlers{deps: deps.withDefaults(), clock: fixedClock(now), logger: testLogger()}, setup, nodeID, act
}

func TestRepeatingPlaylistStagesTheOpenerOnItsLastEntry(t *testing.T) {
	now := testNow
	h, setup, nodeID, act := repeatWrapFixture(t, now, repeatModeObs("fpp-main", 1.0, now))
	activations := map[string]cueactivation.Activation{nodeID: act}

	h.wrapNextCueOnRepeat(context.Background(), now, "inst-1", activations)

	if got := activations[nodeID].NextCueID; got != "cue-1" {
		t.Fatalf("NextCueID = %q, want the opener cue-1", got)
	}
	h.dispatchPrepareAheadAudio(context.Background(), now, nodeID, activations[nodeID], cueActivationIssuer{PrincipalID: "system:test"})
	staged := dispatchedActionsToSession(setup, cueactivation.PrepareStagingSessionID)
	if len(staged) != 2 {
		t.Fatalf("staged %d commands, want apply and prepare: %+v", len(staged), staged)
	}
	media, _ := staged[0].Params["media"].(map[string]any)
	if media["assetId"] != "asset-cue-1" {
		t.Fatalf("staged asset = %v, want asset-cue-1", media["assetId"])
	}
}

func TestLastEntryHasNoNextCueWithoutRepeatEvidence(t *testing.T) {
	now := testNow
	for name, obs := range map[string][]observation.Observation{
		"repeat off":   {repeatModeObs("fpp-main", 0.0, now)},
		"idle string":  {repeatModeObs("fpp-main", "0", now)},
		"never seen":   nil,
		"stale":        {repeatModeObs("fpp-main", 1.0, now.Add(-time.Hour))},
		"other player": {repeatModeObs("fpp-other", 1.0, now)},
	} {
		t.Run(name, func(t *testing.T) {
			h, _, nodeID, act := repeatWrapFixture(t, now, obs...)
			activations := map[string]cueactivation.Activation{nodeID: act}
			h.wrapNextCueOnRepeat(context.Background(), now, "inst-1", activations)
			if got := activations[nodeID].NextCueID; got != "" {
				t.Fatalf("NextCueID = %q, want none", got)
			}
		})
	}
}

func TestRepeatWrapLeavesAnOrdinaryNextCueAlone(t *testing.T) {
	now := testNow
	h, _, nodeID, act := repeatWrapFixture(t, now, repeatModeObs("fpp-main", 1.0, now))
	act.EntryID, act.CueID, act.NextCueID = "entry-1", "cue-1", "cue-2"
	activations := map[string]cueactivation.Activation{nodeID: act}

	h.wrapNextCueOnRepeat(context.Background(), now, "inst-1", activations)

	if got := activations[nodeID].NextCueID; got != "cue-2" {
		t.Fatalf("NextCueID = %q, want cue-2", got)
	}
}
