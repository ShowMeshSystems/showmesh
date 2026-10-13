package fppreconcile

import (
	"context"
	"errors"
	"fmt"

	"github.com/showmeshsystems/showmesh/internal/coordinator/assetsync"
	"github.com/showmeshsystems/showmesh/internal/coordinator/config"
	"github.com/showmeshsystems/showmesh/internal/coordinator/store"
)

// This file is the ONE place that resolves whether a recorded cue-catalog
// deploy override (store.NodeCueCatalogOverrideRecord) still covers a
// node's currently resolved H0.5 conflict. Both exclusiveClaimReadiness
// (readiness.go) and nightCheckNodeCatalogCurrent (internal/coordinator/
// api/nightcatalogreadiness.go) call this rather than deciding it twice.

// CueCatalogOverrideStatus resolves whether nodeID's recorded override
// covers catalog: covered only when the override names exactly
// catalog.Revision and its stored conflicts include every one catalog
// carries now. Not found or a revision/conflict mismatch reads covered=false.
func CueCatalogOverrideStatus(ctx context.Context, st *store.Store, nodeID string, catalog assetsync.Catalog) (covered bool, rec store.NodeCueCatalogOverrideRecord, err error) {
	if len(catalog.Conflicts) == 0 {
		return false, store.NodeCueCatalogOverrideRecord{}, nil
	}
	rec, err = st.GetNodeCueCatalogOverride(ctx, nodeID)
	if errors.Is(err, store.ErrNodeCueCatalogOverrideNotFound) {
		return false, store.NodeCueCatalogOverrideRecord{}, nil
	}
	if err != nil {
		return false, store.NodeCueCatalogOverrideRecord{}, fmt.Errorf("fppreconcile: get node cue-catalog override %q: %w", nodeID, err)
	}
	if rec.Revision != catalog.Revision {
		return false, store.NodeCueCatalogOverrideRecord{}, nil
	}
	if !overrideCoversConflicts(rec.Conflicts, catalog.Conflicts) {
		return false, store.NodeCueCatalogOverrideRecord{}, nil
	}
	return true, rec, nil
}

// overrideCoversConflicts reports whether every conflict in current
// appears in recorded by (CueA, CueB, Claim). recorded may carry more
// than current, but never fewer.
func overrideCoversConflicts(recorded []store.StoredCatalogConflict, current []assetsync.CatalogConflict) bool {
	acceptedNames := make(map[[3]string]bool, len(recorded))
	for _, c := range recorded {
		acceptedNames[[3]string{c.CueA, c.CueB, c.Claim}] = true
	}
	for _, c := range current {
		if !acceptedNames[[3]string{c.CueA, c.CueB, c.Claim.String()}] {
			return false
		}
	}
	return true
}

// CueCatalogOverrideWarning is the one line both readiness paths show for a
// node whose cue list was deployed with its conflicts accepted. It counts the
// pairs; the cue catalog deploy result lists them.
func CueCatalogOverrideWarning(nodeID string, catalog assetsync.Catalog, rec store.NodeCueCatalogOverrideRecord) string {
	n := len(catalog.Conflicts)
	pairs := "cue pairs"
	if n == 1 {
		pairs = "cue pair"
	}
	fact := fmt.Sprintf("Node %s has %d %s sharing %s.", nodeID, n, pairs, sharedOutputInWords(catalog.Conflicts))
	if rec.OverriddenByPrincipalName == "" {
		return fact + " Accepted at deploy."
	}
	return fact + " Accepted by " + rec.OverriddenByPrincipalName + "."
}

// sharedOutputInWords names the output every conflict is about, or "an
// output" when they are about different ones.
func sharedOutputInWords(conflicts []assetsync.CatalogConflict) string {
	if len(conflicts) == 0 {
		return "an output"
	}
	kind := conflicts[0].Claim.Kind
	for _, c := range conflicts[1:] {
		if c.Claim.Kind != kind {
			return "an output"
		}
	}
	switch kind {
	case config.ShowCueClaimKindProgramAudioRoute:
		return "its program audio output"
	case config.ShowCueClaimKindLTCOutput:
		return "its LTC output"
	case config.ShowCueClaimKindAnnouncementSession:
		return "its announcements"
	case config.ShowCueClaimKindRenderSurface:
		return "a render surface"
	}
	return "an output"
}

// conflictReason is the failing reason for one conflict nobody has accepted.
func conflictReason(c assetsync.CatalogConflict) string {
	var what string
	switch c.Claim.Kind {
	case config.ShowCueClaimKindProgramAudioRoute:
		what = "the program audio output on " + c.Claim.Node
	case config.ShowCueClaimKindLTCOutput:
		what = "the LTC output on " + c.Claim.Node
	case config.ShowCueClaimKindAnnouncementSession:
		what = "announcements on " + c.Claim.Node
	case config.ShowCueClaimKindRenderSurface:
		what = "the render surface " + c.Claim.Resource
	default:
		what = "the same output"
	}
	return fmt.Sprintf("Cues %s and %s both use %s. Change one cue, or accept this when you deploy.", c.CueA, c.CueB, what)
}
