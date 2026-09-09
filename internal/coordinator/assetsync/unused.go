package assetsync

import (
	"context"
	"fmt"

	"github.com/showmeshsystems/showmesh/internal/coordinator/store"
)

// This file answers which assets a node holds that no Cue in its current
// (live-resolved, never last-acknowledged) catalog references. It adds no
// second readiness computation: the hold side is
// [ComputeNodeManifest]'s own Extra field (a held content hash not in the
// expected set [ExpectedAssetsForNode] already computes from the SAME
// resolution [ResolveCueCatalog] uses to build catalog entries), and the
// reference side for the removal refusal is [Catalog.Entries] itself. Both
// are already-computed facts; this file only projects and enriches them.

// UnusedAsset is one asset a node holds ([NodeManifest.Extra]) that its
// resolved Cue catalog does not reference, enriched with the sequence this
// coordinator's own asset records attribute it to, when they still can.
type UnusedAsset struct {
	ContentHash string
	Filename    string
	SizeBytes   int64

	// SequenceID is nil when no asset record (current or superseded) for
	// showID can be traced to ContentHash - a file the node holds that was
	// never issued through the asset API for this show at all (placed some
	// other way, or left over from a show that is no longer active). Never
	// fabricated: an unattributed asset is reported as unattributed, not
	// silently dropped or guessed at.
	SequenceID *string
}

// UnusedAssetsForNode enriches m.Extra with each asset's originating
// sequence, one query for the whole show rather than one per asset,
// mirroring [supersededHashesByAssetID]'s identical one-query-per-show
// shape. m must already be a manifest whose Extra is meaningful (State !=
// Unknown; see [ComputeNodeManifest]'s own doc comment for why Extra is nil
// otherwise); a caller must check State before calling this, never infer
// "no unused assets" from an empty result on an Unknown manifest.
func UnusedAssetsForNode(ctx context.Context, st *store.Store, showID string, m NodeManifest) ([]UnusedAsset, error) {
	out := make([]UnusedAsset, 0, len(m.Extra))
	if len(m.Extra) == 0 {
		return out, nil
	}

	all, err := st.ListAssets(ctx, store.AssetFilter{ShowID: showID})
	if err != nil {
		return nil, fmt.Errorf("assetsync: unused assets for node %q: list assets: %w", m.NodeID, err)
	}
	// First writer wins: an operator only needs one sequence named for an
	// unused asset, and a content hash shared by two sequences in the same
	// show is not a case this attribution needs to disambiguate.
	seqByHash := make(map[string]string, len(all))
	for _, rec := range all {
		if _, ok := seqByHash[rec.ContentHash]; !ok {
			seqByHash[rec.ContentHash] = rec.SequenceID
		}
	}

	for _, e := range m.Extra {
		u := UnusedAsset{ContentHash: e.ContentHash, Filename: e.Filename, SizeBytes: e.SizeBytes}
		if seq, ok := seqByHash[e.ContentHash]; ok {
			s := seq
			u.SequenceID = &s
		}
		out = append(out, u)
	}
	return out, nil
}

// CuesReferencingAsset returns, in catalog entry order, the CueID of every
// Cue in catalog whose resolved render or audio output references
// contentHash - the authority a removal refusal is checked against
// synchronously, before any command reaches a node.
func CuesReferencingAsset(catalog Catalog, contentHash string) []string {
	var out []string
	for _, e := range catalog.Entries {
		if e.Outputs.Render != nil && containsHash(e.Outputs.Render.AssetHashes, contentHash) {
			out = append(out, e.CueID)
			continue
		}
		if e.Outputs.Audio != nil && containsHash(e.Outputs.Audio.AssetHashes, contentHash) {
			out = append(out, e.CueID)
		}
	}
	return out
}

func containsHash(hashes []string, contentHash string) bool {
	for _, h := range hashes {
		if h == contentHash {
			return true
		}
	}
	return false
}
