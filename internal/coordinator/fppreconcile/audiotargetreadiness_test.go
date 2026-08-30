package fppreconcile

import (
	"context"
	"strings"
	"testing"

	"github.com/showmeshsystems/showmesh/internal/coordinator/config"
	"github.com/showmeshsystems/showmesh/internal/coordinator/store"
)

func putProgramOnlyAudioNode(t *testing.T, st *store.Store, nodeID string) {
	t.Helper()
	raw, err := config.EncodeAudioNodePayload(config.AudioNodePayload{
		ProgramRoute:          "usb-interface",
		ProgramChannels:       []int{1, 2},
		ClockDomain:           "single-interface",
		ClockDomainProvenance: "two-output interface, program only",
	})
	if err != nil {
		t.Fatalf("encode program-only audio.node payload: %v", err)
	}
	putConfig(t, st, config.AudioNodeConfigKind, nodeID, raw)
}

func putCueWithOutputs(t *testing.T, st *store.Store, cueID, showID string, outputs config.ShowCueOutputs) {
	t.Helper()
	payload, err := config.EncodeShowCuePayload(config.ShowCuePayload{Show: showID, Name: cueID, Outputs: outputs})
	if err != nil {
		t.Fatalf("encode show.cue payload: %v", err)
	}
	putConfig(t, st, config.ShowCueConfigKind, cueID, payload)
}

func playlistWithCue(showID, cueID string) config.ShowPlaylistPayload {
	return config.ShowPlaylistPayload{
		Show: showID, Name: "Main", Runner: "showmesh",
		Entries: []config.ShowPlaylistEntry{{ID: "e1", Cue: cueID}},
	}
}

// TestAudioTargetReadinessRefusesTwoLTCEmitters proves ADR-045 decision 2
// is checked against the store, not only at authoring time.
func TestAudioTargetReadinessRefusesTwoLTCEmitters(t *testing.T) {
	st := openTestStore(t)
	putShow(t, st, "show-1", "Show One")
	putAudioNode(t, st, "m4")
	putAudioNode(t, st, "mac-mini")
	putCueWithOutputs(t, st, "cue-1", "show-1", config.ShowCueOutputs{
		Audio: &config.ShowCueAudioOutput{Asset: "a"},
	})

	cond, reason, err := audioTargetReadiness(context.Background(), st, nil, playlistWithCue("show-1", "cue-1"))
	if err != nil {
		t.Fatalf("audioTargetReadiness: %v", err)
	}
	if cond != ReadinessAudioLTCEmitterAmbiguous {
		t.Fatalf("condition = %q, want %q", cond, ReadinessAudioLTCEmitterAmbiguous)
	}
	if !strings.Contains(reason, "m4") || !strings.Contains(reason, "mac-mini") {
		t.Errorf("reason = %q, want it to name both nodes", reason)
	}
}

// TestAudioTargetReadinessRefusesUnboundTarget proves an output pointing at
// a node that lost its audio.node is named rather than silently dropped.
func TestAudioTargetReadinessRefusesUnboundTarget(t *testing.T) {
	st := openTestStore(t)
	putShow(t, st, "show-1", "Show One")
	putAudioNode(t, st, "m4")
	putCueWithOutputs(t, st, "cue-1", "show-1", config.ShowCueOutputs{
		Audio: &config.ShowCueAudioOutput{Asset: "a", Target: "pi"},
	})

	cond, reason, err := audioTargetReadiness(context.Background(), st, nil, playlistWithCue("show-1", "cue-1"))
	if err != nil {
		t.Fatalf("audioTargetReadiness: %v", err)
	}
	if cond != ReadinessAudioTargetUnbound {
		t.Fatalf("condition = %q, want %q", cond, ReadinessAudioTargetUnbound)
	}
	if !strings.Contains(reason, "pi") || !strings.Contains(reason, "cue-1") {
		t.Errorf("reason = %q, want it to name the cue and the target node", reason)
	}
}

// TestAudioTargetReadinessRefusesUnresolvableUntargetedOutput proves the
// ambiguity the catalog leaves unresolved is reported rather than silently
// producing a Cue that reaches no node at all.
func TestAudioTargetReadinessRefusesUnresolvableUntargetedOutput(t *testing.T) {
	st := openTestStore(t)
	putShow(t, st, "show-1", "Show One")
	putProgramOnlyAudioNode(t, st, "pi-a")
	putProgramOnlyAudioNode(t, st, "pi-b")
	putCueWithOutputs(t, st, "cue-1", "show-1", config.ShowCueOutputs{
		Audio: &config.ShowCueAudioOutput{Asset: "a"},
	})

	cond, reason, err := audioTargetReadiness(context.Background(), st, nil, playlistWithCue("show-1", "cue-1"))
	if err != nil {
		t.Fatalf("audioTargetReadiness: %v", err)
	}
	if cond != ReadinessAudioTargetUnresolved {
		t.Fatalf("condition = %q, want %q", cond, ReadinessAudioTargetUnresolved)
	}
	if !strings.Contains(reason, "outputs.audio") {
		t.Errorf("reason = %q, want it to name which output", reason)
	}
}

// TestAudioTargetReadinessPassesTheReferenceInstallation proves the shape
// ADR-045 exists to allow is ready: one program+ltc node, one program node
// named by a targeted output.
func TestAudioTargetReadinessPassesTheReferenceInstallation(t *testing.T) {
	st := openTestStore(t)
	putShow(t, st, "show-1", "Show One")
	putAudioNode(t, st, "m4")
	putProgramOnlyAudioNode(t, st, "pi")
	putCueWithOutputs(t, st, "cue-1", "show-1", config.ShowCueOutputs{
		Audio: &config.ShowCueAudioOutput{Asset: "a", Target: "pi"},
		LTC:   &config.ShowCueLTCOutput{},
	})

	cond, reason, err := audioTargetReadiness(context.Background(), st, nil, playlistWithCue("show-1", "cue-1"))
	if err != nil {
		t.Fatalf("audioTargetReadiness: %v", err)
	}
	if cond != "" {
		t.Fatalf("condition = %q (%s), want ready", cond, reason)
	}
}

// TestAudioTargetReadinessIgnoresACueWithNoAudioOutputs proves a
// render-only Show is never failed by an audio condition.
func TestAudioTargetReadinessIgnoresACueWithNoAudioOutputs(t *testing.T) {
	st := openTestStore(t)
	putShow(t, st, "show-1", "Show One")
	putCueWithOutputs(t, st, "cue-1", "show-1", config.ShowCueOutputs{
		Render: &config.ShowCueRenderOutput{Sequence: "seq"},
	})

	cond, _, err := audioTargetReadiness(context.Background(), st, nil, playlistWithCue("show-1", "cue-1"))
	if err != nil {
		t.Fatalf("audioTargetReadiness: %v", err)
	}
	if cond != "" {
		t.Fatalf("condition = %q, want ready: this cue declares no audio, LTC or announcement output", cond)
	}
}
