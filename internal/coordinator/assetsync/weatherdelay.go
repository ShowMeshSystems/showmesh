package assetsync

import (
	"context"
	"fmt"
)

// This file is the weather delay alert's own, narrow use of this
// package's existing fetch dispatch (ADR-053 decision 7's alert asset).
// It deliberately does NOT go through
// [ExpectedAssetsForNode]/[BuildManifest]: those compute what a node
// should hold for the ACTIVE SHOW, and an alert asset must reach a node
// whether or not any show is active ("a calm day", the operator checking
// readiness with nothing running). [Service.maybeDispatch] itself has no
// show-scoped assumption, so this reuses it directly against an
// [ExpectedAsset] built from the asset's own stored metadata.

// EnsureAssetOnNode resolves assetID to its current stored metadata and,
// if nodeID does not already hold it in flight, dispatches asset.fetch to
// it exactly as an ordinary manifest-driven sync would, same in-flight
// budget, same dedupe key. A caller normally calls this once per plan
// node per configured alert asset id; repeated calls (an operator pressing
// "sync now", or this package's own caller re-checking readiness) are
// cheap no-ops once the node already holds the asset or a fetch for it is
// already in flight.
func (s *Service) EnsureAssetOnNode(ctx context.Context, assetID, nodeID string) error {
	rec, err := s.st.GetAsset(ctx, assetID)
	if err != nil {
		return fmt.Errorf("assetsync: ensure asset %q on node %q: %w", assetID, nodeID, err)
	}
	if present, err := s.AssetPresence(ctx, nodeID, rec.ContentHash); err == nil && present {
		return nil
	}
	s.maybeDispatch(ctx, nodeID, ExpectedAsset{
		AssetID: rec.ID, SequenceID: rec.SequenceID, MediaType: rec.MediaType,
		ContentHash: rec.ContentHash, Filename: rec.RuntimeFilename, SizeBytes: rec.SizeBytes,
	})
	return nil
}

// AssetPresence reports whether nodeID's last asset inventory report
// listed contentHash, hash-verified. Absent (nodeID has never reported)
// and present-but-different-hash both read false, never conflated.
func (s *Service) AssetPresence(ctx context.Context, nodeID, contentHash string) (bool, error) {
	if contentHash == "" {
		return false, nil
	}
	items, err := s.st.GetNodeAssetInventory(ctx, nodeID)
	if err != nil {
		return false, fmt.Errorf("assetsync: asset presence for node %q: %w", nodeID, err)
	}
	for _, item := range items {
		if item.ContentHash == contentHash {
			return true, nil
		}
	}
	return false, nil
}
