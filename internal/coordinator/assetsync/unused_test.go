package assetsync

import (
	"context"
	"testing"

	"github.com/showmeshsystems/showmesh/internal/coordinator/store"
	"github.com/showmeshsystems/showmesh/pkg/cuecatalog"
)

func TestUnusedAssetsForNodeNamesSequence(t *testing.T) {
	st := openTestStore(t)
	ctx := context.Background()
	putShow(t, st, "halloween-2026", "Halloween 2026")
	createAsset(t, st, "halloween-2026", "opening", store.AssetTargetKindShow, "", "sha256:stale", "Opening-old.fseq")

	m := NodeManifest{
		NodeID: "render-01", State: ManifestReady,
		Extra: []ExtraAsset{{ContentHash: "sha256:stale", Filename: "Opening-old.fseq", SizeBytes: 512}},
	}

	got, err := UnusedAssetsForNode(ctx, st, "halloween-2026", m)
	if err != nil {
		t.Fatalf("UnusedAssetsForNode() error = %v", err)
	}
	if len(got) != 1 {
		t.Fatalf("UnusedAssetsForNode() = %+v, want exactly one entry", got)
	}
	u := got[0]
	if u.ContentHash != "sha256:stale" || u.Filename != "Opening-old.fseq" || u.SizeBytes != 512 {
		t.Fatalf("UnusedAssetsForNode()[0] = %+v, want the Extra fields carried through unchanged", u)
	}
	if u.SequenceID == nil || *u.SequenceID != "opening" {
		t.Fatalf("UnusedAssetsForNode()[0].SequenceID = %v, want a pointer to %q", u.SequenceID, "opening")
	}
}

// TestUnusedAssetsForNodeNilSequenceWhenUntraceable pins the honest-absence
// case: a held file no asset record for this show can be traced to (never
// issued through the asset API, or left over from a different show) reports
// SequenceID as nil rather than a fabricated or zero-value sequence.
func TestUnusedAssetsForNodeNilSequenceWhenUntraceable(t *testing.T) {
	st := openTestStore(t)
	ctx := context.Background()
	putShow(t, st, "halloween-2026", "Halloween 2026")

	m := NodeManifest{
		NodeID: "render-01", State: ManifestReady,
		Extra: []ExtraAsset{{ContentHash: "sha256:mystery", Filename: "leftover.bin", SizeBytes: 1}},
	}

	got, err := UnusedAssetsForNode(ctx, st, "halloween-2026", m)
	if err != nil {
		t.Fatalf("UnusedAssetsForNode() error = %v", err)
	}
	if len(got) != 1 {
		t.Fatalf("UnusedAssetsForNode() = %+v, want exactly one entry", got)
	}
	if got[0].SequenceID != nil {
		t.Fatalf("UnusedAssetsForNode()[0].SequenceID = %v, want nil for a hash no asset record explains", *got[0].SequenceID)
	}
}

func TestUnusedAssetsForNodeEmptyExtraNoQuery(t *testing.T) {
	st := openTestStore(t)
	ctx := context.Background()

	// No show, no assets - a query against a nonexistent show would error;
	// this must not even attempt one when Extra is already empty.
	m := NodeManifest{NodeID: "render-01", State: ManifestReady}
	got, err := UnusedAssetsForNode(ctx, st, "no-such-show", m)
	if err != nil {
		t.Fatalf("UnusedAssetsForNode() error = %v, want nil", err)
	}
	if len(got) != 0 {
		t.Fatalf("UnusedAssetsForNode() = %+v, want empty", got)
	}
}

// TestUnusedAssetsForNodeWithdrawnOnUnknown pins the absent-evidence rule
// the design note commits to: a manifest whose State is Unknown carries a
// nil Extra (ComputeNodeManifest never populates it in that case - see that
// function's own doc comment), and this function must render that as
// nothing to report, never as "zero unused assets found," which a caller
// must distinguish by checking State itself before ever calling this.
func TestUnusedAssetsForNodeWithdrawnOnUnknown(t *testing.T) {
	st := openTestStore(t)
	ctx := context.Background()

	m := ComputeNodeManifest("render-01", ActiveShow{Configured: false}, ExpectedSet{}, nil, false, nil)
	if m.State != ManifestUnknown {
		t.Fatalf("fixture State = %q, want unknown", m.State)
	}
	if m.Extra != nil {
		t.Fatalf("fixture Extra = %+v, want nil on an unknown manifest", m.Extra)
	}

	got, err := UnusedAssetsForNode(ctx, st, "", m)
	if err != nil {
		t.Fatalf("UnusedAssetsForNode() error = %v", err)
	}
	if len(got) != 0 {
		t.Fatalf("UnusedAssetsForNode() = %+v, want empty for an unknown manifest's nil Extra", got)
	}
}

func TestCuesReferencingAssetNamesRenderAndAudioCues(t *testing.T) {
	catalog := Catalog{
		Entries: []cuecatalog.Entry{
			{CueID: "cue-render", Outputs: cuecatalog.Outputs{
				Render: &cuecatalog.RenderOutput{Sequence: "opening", AssetHashes: []string{"sha256:used"}},
			}},
			{CueID: "cue-audio", Outputs: cuecatalog.Outputs{
				Audio: &cuecatalog.AudioOutput{Asset: "narration", AssetHashes: []string{"sha256:used", "sha256:other"}},
			}},
			{CueID: "cue-unrelated", Outputs: cuecatalog.Outputs{
				Render: &cuecatalog.RenderOutput{Sequence: "closing", AssetHashes: []string{"sha256:other"}},
			}},
		},
	}

	got := CuesReferencingAsset(catalog, "sha256:used")
	if len(got) != 2 || got[0] != "cue-render" || got[1] != "cue-audio" {
		t.Fatalf("CuesReferencingAsset() = %v, want [cue-render cue-audio] in entry order", got)
	}
}

func TestCuesReferencingAssetNoneReturnsEmpty(t *testing.T) {
	catalog := Catalog{Entries: []cuecatalog.Entry{
		{CueID: "cue-a", Outputs: cuecatalog.Outputs{Render: &cuecatalog.RenderOutput{AssetHashes: []string{"sha256:a"}}}},
	}}
	got := CuesReferencingAsset(catalog, "sha256:nowhere")
	if len(got) != 0 {
		t.Fatalf("CuesReferencingAsset() = %v, want empty", got)
	}
}
