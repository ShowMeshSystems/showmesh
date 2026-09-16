package fallbackcompile

import (
	"context"
	"testing"
	"time"

	"github.com/showmeshsystems/showmesh/internal/coordinator/assetsync"
	"github.com/showmeshsystems/showmesh/internal/coordinator/config"
	"github.com/showmeshsystems/showmesh/internal/coordinator/store"
)

// createNodeAudioAsset uploads a node-scoped "audio" media-type asset,
// [createAsset]'s own show-scoped "fseq" fixture has no equivalent for.
func createNodeAudioAsset(t *testing.T, st *store.Store, showID, sequenceID, nodeID, contentHash, filename string) {
	t.Helper()
	if _, _, err := st.CreateAsset(context.Background(), store.AssetRecord{
		ID: contentHash + "-node-" + nodeID, ShowID: showID, SequenceID: sequenceID,
		TargetKind: store.AssetTargetKindNode, TargetID: nodeID, MediaType: "audio", ContentHash: contentHash,
		RuntimeFilename: filename, SizeBytes: 2048, Backend: "volume", StorageKey: contentHash,
	}); err != nil {
		t.Fatalf("create node audio asset %q for %q: %v", sequenceID, nodeID, err)
	}
}

// audioFallbackFixture is a two-audio-node, audio-only Cue ("wake-up",
// asset "wake-up-mh-test-audio") targeting both nodes, bound as an
// fpp-runner playlist entry so [Compile] resolves it, matching the real
// rig's own naming (a 4-channel M4 and a 2-channel Scarlett-fed Pi).
type audioFallbackFixture struct {
	st           *store.Store
	showID       string
	nodeA, nodeB string
	now          time.Time
}

func newAudioFallbackFixture(t *testing.T) audioFallbackFixture {
	t.Helper()
	st := openTestStore(t)
	now := testNow()
	showID := "halloween-2026"
	nodeA, nodeB := "showmesh-node-01", "pi-audio-01"

	putShow(t, st, showID, "Halloween 2026")
	declareNode(t, st, nodeA)
	declareNode(t, st, nodeB)
	putAudioNode(t, st, nodeA)
	putAudioNode(t, st, nodeB)

	putCue(t, st, "wake-up", showID, config.ShowCuePayload{
		Name:    "Wake Up",
		Outputs: config.ShowCueOutputs{Audio: &config.ShowCueAudioOutput{Asset: "wake-up-mh-test-audio", Targets: []string{nodeA, nodeB}}},
	})
	putPlaylist(t, st, "main", config.ShowPlaylistPayload{
		Show: showID, Name: "Main", Runner: config.ShowPlaylistRunnerFPP,
		FPP: &config.ShowPlaylistFPPBinding{
			InstanceUUID: testInstanceUUID, PlaylistName: "Main", PlaylistHash: testPlaylistHash,
		},
		Entries: []config.ShowPlaylistEntry{
			{ID: "entry-0", Cue: "wake-up", FPP: &config.ShowPlaylistEntryFPP{Section: "mainPlaylist", Position: 0}},
		},
	})
	putDefinition(t, st, testInstanceUUID, testPlaylistHash, "wake-up.mp3")
	putActiveShow(t, st, showID)

	return audioFallbackFixture{st: st, showID: showID, nodeA: nodeA, nodeB: nodeB, now: now}
}

func (f audioFallbackFixture) resolveActive(t *testing.T) assetsync.ActiveShow {
	t.Helper()
	active, err := assetsync.ResolveActiveShow(context.Background(), f.st)
	if err != nil {
		t.Fatalf("resolve active show: %v", err)
	}
	return active
}

func (f audioFallbackFixture) ackBoth(t *testing.T) {
	t.Helper()
	active := f.resolveActive(t)
	ackNodeCatalog(t, f.st, active, f.nodeA, f.now)
	ackNodeCatalog(t, f.st, active, f.nodeB, f.now)
}

// TestCompileResolvesAudioFallbackForSecondTarget proves ADR-049 decision
// 5's own read path: a Cue names two audio targets, only nodeA has a
// node-scoped asset row, and Compile must no longer refuse nodeB's output
// as unresolvable — it resolves nodeB's target to nodeA's own filename and
// content hash. Reverting the assetsync fix under test turns this back
// into OutcomeUnresolvableTarget, naming nodeB, exactly as the owner's
// rig reported it.
func TestCompileResolvesAudioFallbackForSecondTarget(t *testing.T) {
	f := newAudioFallbackFixture(t)
	createNodeAudioAsset(t, f.st, f.showID, "wake-up-mh-test-audio", f.nodeA, "sha256:aaa", "wake-up.mp3")
	f.ackBoth(t)

	result, err := Compile(context.Background(), f.st, fakeSigner{}, testInstanceUUID, f.now)
	if err != nil {
		t.Fatalf("Compile: %v", err)
	}
	if result.Outcome != OutcomePublished {
		t.Fatalf("Compile outcome = %q, want %q; reason: %s", result.Outcome, OutcomePublished, result.Reason)
	}

	entries := result.Program.Program.Entries
	if len(entries) != 1 {
		t.Fatalf("Entries = %+v, want exactly 1", entries)
	}
	byNode := make(map[string]string, len(entries[0].Targets))
	for _, target := range entries[0].Targets {
		if target.Audio == nil {
			t.Fatalf("target %+v has no audio activation", target)
		}
		byNode[target.NodeID] = target.Audio.Filename
	}
	if byNode[f.nodeA] != "wake-up.mp3" {
		t.Fatalf("nodeA filename = %q, want wake-up.mp3", byNode[f.nodeA])
	}
	if byNode[f.nodeB] != "wake-up.mp3" {
		t.Fatalf("nodeB filename = %q, want wake-up.mp3 (borrowed from nodeA)", byNode[f.nodeB])
	}
}

// TestCompileStillRefusesUnresolvableAudioWhenNothingResolvesAnywhere
// proves the fallback never fabricates a resolution: with no node-scoped
// or show-scoped row for either target, Compile still refuses with
// OutcomeUnresolvableTarget exactly as it always has.
func TestCompileStillRefusesUnresolvableAudioWhenNothingResolvesAnywhere(t *testing.T) {
	f := newAudioFallbackFixture(t)
	// Deliberately no createNodeAudioAsset call for either node.
	f.ackBoth(t)

	result, err := Compile(context.Background(), f.st, fakeSigner{}, testInstanceUUID, f.now)
	if err != nil {
		t.Fatalf("Compile: %v", err)
	}
	if result.Outcome != OutcomeUnresolvableTarget {
		t.Fatalf("Compile outcome = %q, want %q; reason: %s", result.Outcome, OutcomeUnresolvableTarget, result.Reason)
	}
	if result.Program != nil {
		t.Fatalf("a refused compile must never carry a Program")
	}
}
