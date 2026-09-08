package assetsync

import (
	"context"
	"testing"

	"github.com/showmeshsystems/showmesh/internal/coordinator/config"
	"github.com/showmeshsystems/showmesh/internal/coordinator/store"
)

// twoAudioNodeShow sets up ADR-045's reference multi-node installation:
// "m4" carries program and LTC, "pi" carries program only.
func twoAudioNodeShow(t *testing.T) (*store.Store, context.Context) {
	t.Helper()
	st := openTestStore(t)
	declareNode(t, st, "m4")
	declareNode(t, st, "pi")
	putShow(t, st, "halloween-2026", "Halloween 2026")
	putAudioNode(t, st, "m4")
	putProgramOnlyAudioNode(t, st, "pi")
	return st, context.Background()
}

func resolveFor(t *testing.T, ctx context.Context, st *store.Store, nodeID string) Catalog {
	t.Helper()
	active, err := ResolveActiveShow(ctx, st)
	if err != nil {
		t.Fatalf("ResolveActiveShow: %v", err)
	}
	catalog, err := ResolveCueCatalog(ctx, st, active, nodeID)
	if err != nil {
		t.Fatalf("ResolveCueCatalog (%s): %v", nodeID, err)
	}
	return catalog
}

// TestUntargetedOutputsResolveToTheProgramLTCNodeOnly proves ADR-045's
// default: a Cue authored before target existed still reaches the one node
// a single-node installation always had, and reaches no other node.
func TestUntargetedOutputsResolveToTheProgramLTCNodeOnly(t *testing.T) {
	st, ctx := twoAudioNodeShow(t)
	putCue(t, st, "thriller", "halloween-2026", config.ShowCuePayload{
		Name: "Thriller",
		Outputs: config.ShowCueOutputs{
			Audio: &config.ShowCueAudioOutput{Asset: "thriller-audio"},
			LTC:   &config.ShowCueLTCOutput{},
		},
	})
	putPlaylist(t, st, "main", simplePlaylist("halloween-2026", "thriller"))
	putActiveShow(t, st, "halloween-2026")

	m4, ok := cueOutputsByID(resolveFor(t, ctx, st, "m4").Entries, "thriller")
	if !ok {
		t.Fatal("thriller missing from m4's catalog")
	}
	if m4.Audio == nil || m4.LTC == nil {
		t.Errorf("m4 outputs = %+v, want audio and LTC: it is the program+ltc node", m4)
	}

	pi, ok := cueOutputsByID(resolveFor(t, ctx, st, "pi").Entries, "thriller")
	if !ok {
		t.Fatal("thriller missing from pi's catalog")
	}
	if pi.Audio != nil || pi.LTC != nil {
		t.Errorf("pi outputs = %+v, want none: an untargeted output belongs to the program+ltc node alone", pi)
	}
}

// TestTargetedAudioReachesOnlyItsTargetNode proves the whole point of
// decision 1: a zone Cue plays where it was authored to play.
func TestTargetedAudioReachesOnlyItsTargetNode(t *testing.T) {
	st, ctx := twoAudioNodeShow(t)
	putCue(t, st, "porch", "halloween-2026", config.ShowCuePayload{
		Name: "Porch bed",
		Outputs: config.ShowCueOutputs{
			Audio: &config.ShowCueAudioOutput{Asset: "porch-audio", Target: "pi"},
		},
	})
	putPlaylist(t, st, "main", simplePlaylist("halloween-2026", "porch"))
	putActiveShow(t, st, "halloween-2026")

	pi, ok := cueOutputsByID(resolveFor(t, ctx, st, "pi").Entries, "porch")
	if !ok {
		t.Fatal("porch missing from pi's catalog")
	}
	if pi.Audio == nil || pi.Audio.Asset != "porch-audio" {
		t.Errorf("pi audio = %+v, want porch-audio: pi is the named target", pi.Audio)
	}

	m4, ok := cueOutputsByID(resolveFor(t, ctx, st, "m4").Entries, "porch")
	if !ok {
		t.Fatal("porch missing from m4's catalog")
	}
	if m4.Audio != nil {
		t.Errorf("m4 audio = %+v, want nil: the output names pi", m4.Audio)
	}
}

// TestLTCTargetedAtAProgramOnlyNodeShipsNothing proves target and
// capability are both required. Naming a node that emits no LTC as an LTC
// output's target must not ship it an LTC output it cannot produce.
func TestLTCTargetedAtAProgramOnlyNodeShipsNothing(t *testing.T) {
	st, ctx := twoAudioNodeShow(t)
	putCue(t, st, "misrouted", "halloween-2026", config.ShowCuePayload{
		Name: "Misrouted LTC",
		Outputs: config.ShowCueOutputs{
			Audio: &config.ShowCueAudioOutput{Asset: "audio", Target: "pi"},
			LTC:   &config.ShowCueLTCOutput{Target: "pi"},
		},
	})
	putPlaylist(t, st, "main", simplePlaylist("halloween-2026", "misrouted"))
	putActiveShow(t, st, "halloween-2026")

	pi, ok := cueOutputsByID(resolveFor(t, ctx, st, "pi").Entries, "misrouted")
	if !ok {
		t.Fatal("misrouted missing from pi's catalog")
	}
	if pi.LTC != nil {
		t.Errorf("pi LTC = %+v, want nil: pi declares no LTC route", pi.LTC)
	}
	if pi.Audio == nil {
		t.Errorf("pi audio = nil, want present: the audio half is targeted at pi and pi can play it")
	}
}

// TestNodeCueSequenceIDsCoversOnlyThisNodesOwnOutputs proves the asset
// manifest follows the same resolution: pi must not be asked to hold, or
// be refused for missing, an asset only m4's outputs name.
func TestNodeCueSequenceIDsCoversOnlyThisNodesOwnOutputs(t *testing.T) {
	st, ctx := twoAudioNodeShow(t)
	putCue(t, st, "house", "halloween-2026", config.ShowCuePayload{
		Name:    "House mix",
		Outputs: config.ShowCueOutputs{Audio: &config.ShowCueAudioOutput{Asset: "house-audio"}},
	})
	putCue(t, st, "porch", "halloween-2026", config.ShowCuePayload{
		Name:    "Porch bed",
		Outputs: config.ShowCueOutputs{Audio: &config.ShowCueAudioOutput{Asset: "porch-audio", Target: "pi"}},
	})
	putPlaylist(t, st, "main", simplePlaylist("halloween-2026", "house", "porch"))
	putActiveShow(t, st, "halloween-2026")

	piSeqs, err := NodeCueSequenceIDs(ctx, st, "halloween-2026", "pi")
	if err != nil {
		t.Fatalf("NodeCueSequenceIDs (pi): %v", err)
	}
	if piSeqs["house-audio"] {
		t.Errorf("pi expects house-audio, which only m4's untargeted output names")
	}
	if !piSeqs["porch-audio"] {
		t.Errorf("pi does not expect porch-audio, which its own targeted output names")
	}

	m4Seqs, err := NodeCueSequenceIDs(ctx, st, "halloween-2026", "m4")
	if err != nil {
		t.Fatalf("NodeCueSequenceIDs (m4): %v", err)
	}
	if !m4Seqs["house-audio"] || m4Seqs["porch-audio"] {
		t.Errorf("m4 sequences = %v, want house-audio only", m4Seqs)
	}
}
