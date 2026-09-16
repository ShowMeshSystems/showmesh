package fppreconcile

import (
	"context"
	"errors"
	"fmt"

	"github.com/showmeshsystems/showmesh/internal/coordinator/assetsync"
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

// CueCatalogOverrideWarning renders rec as the named warning text both
// readiness paths surface in place of a hard failure, naming the
// accepted claim(s) and who accepted them.
func CueCatalogOverrideWarning(nodeID string, catalog assetsync.Catalog, rec store.NodeCueCatalogOverrideRecord) string {
	claims := make([]string, 0, len(catalog.Conflicts))
	for _, c := range catalog.Conflicts {
		claims = append(claims, c.Detail())
	}
	joined := ""
	for i, c := range claims {
		if i > 0 {
			joined += "; "
		}
		joined += c
	}
	return fmt.Sprintf(
		"node %q deployed with operator override: %s (accepted by %s at revision %q)",
		nodeID, joined, overrideAttribution(rec), rec.Revision)
}

// overrideAttribution renders rec's own operator attribution for
// [CueCatalogOverrideWarning], falling back to the principal id when no
// display name was recorded.
func overrideAttribution(rec store.NodeCueCatalogOverrideRecord) string {
	if rec.OverriddenByPrincipalName != "" {
		return rec.OverriddenByPrincipalName
	}
	return rec.OverriddenByPrincipalID
}
