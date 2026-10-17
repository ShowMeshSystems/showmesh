package assetsync

import (
	"context"
	"fmt"
	"sort"

	"github.com/showmeshsystems/showmesh/internal/coordinator/store"
)

// AssetLister is the one store read [BedAudioCopyFor] needs, satisfied by
// both *store.Store and *store.Tx.
type AssetLister interface {
	ListAssets(ctx context.Context, filter store.AssetFilter) ([]store.AssetRecord, error)
}

// BedAudioCopyFor resolves which current audio upload of sequence nodeID
// plays for a night bed, in the order the node manifest delivers them:
// nodeID's own upload, then the show-wide upload, then the first of
// targets' uploads in declared order, then any node's upload (lowest node
// id first). An empty nodeID skips the first tier. ok is false when the
// show has no current audio upload of sequence at all.
func BedAudioCopyFor(ctx context.Context, lister AssetLister, show, sequence, nodeID string, targets []string) (store.AssetRecord, bool, error) {
	recs, err := lister.ListAssets(ctx, store.AssetFilter{ShowID: show, SequenceID: sequence})
	if err != nil {
		return store.AssetRecord{}, false, fmt.Errorf("assetsync: bed audio copy for %q on %q: %w", sequence, nodeID, err)
	}
	var showWide *store.AssetRecord
	byNode := make(map[string]store.AssetRecord)
	for i := range recs {
		rec := recs[i]
		if rec.SupersededAt != nil || rec.MediaType != audioMediaType {
			continue
		}
		switch rec.TargetKind {
		case store.AssetTargetKindShow:
			showWide = &rec
		case store.AssetTargetKindNode:
			byNode[rec.TargetID] = rec
		}
	}
	if rec, ok := byNode[nodeID]; ok && nodeID != "" {
		return rec, true, nil
	}
	if showWide != nil {
		return *showWide, true, nil
	}
	for _, t := range targets {
		if rec, ok := byNode[t]; ok {
			return rec, true, nil
		}
	}
	return lowestNodeCopy(byNode)
}

// anyNodeAudioCopy is [BedAudioCopyFor]'s last tier on its own: any node's
// current audio upload of sequence, lowest node id first.
func anyNodeAudioCopy(ctx context.Context, lister AssetLister, show, sequence string) (store.AssetRecord, bool, error) {
	recs, err := lister.ListAssets(ctx, store.AssetFilter{ShowID: show, SequenceID: sequence})
	if err != nil {
		return store.AssetRecord{}, false, fmt.Errorf("assetsync: any node audio copy for %q: %w", sequence, err)
	}
	byNode := make(map[string]store.AssetRecord)
	for _, rec := range recs {
		if rec.SupersededAt == nil && rec.MediaType == audioMediaType && rec.TargetKind == store.AssetTargetKindNode {
			byNode[rec.TargetID] = rec
		}
	}
	return lowestNodeCopy(byNode)
}

func lowestNodeCopy(byNode map[string]store.AssetRecord) (store.AssetRecord, bool, error) {
	if len(byNode) == 0 {
		return store.AssetRecord{}, false, nil
	}
	ids := make([]string, 0, len(byNode))
	for id := range byNode {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	return byNode[ids[0]], true, nil
}
