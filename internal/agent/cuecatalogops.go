package agent

import (
	"context"
	"encoding/json"
	"fmt"
	"sort"
	"time"

	"github.com/showmeshsystems/showmesh/internal/agent/heldcatalog"
	"github.com/showmeshsystems/showmesh/pkg/cuecatalog"
)

// This file is TRACK-H-H3-SPEC.md section 6's build ruling for how a
// resolved Cue catalog reaches a node: not a standing coordinator base URL
// fetched on demand (the agent has none — see internal/agent/assets.go's
// assetFetchOperation, which is handed a URL per command instead of
// configuring one), but a coordinator-issued MQTT command, exactly as
// render.surface.apply already carries its own params (renderops.go). The
// node persists the deployed catalog, computes its OWN revision via
// [cuecatalog.ComputeRevision] (the one function both sides call — see
// pkg/cuecatalog's own doc comment), and refuses to store anything it
// cannot independently corroborate: build item 1's "the node must verify
// the revision it computes matches the one the coordinator sent, and
// refuse the deployment if they disagree rather than storing a catalog it
// disagrees about."

// cuecatalogDeployKnownKeys is the complete set of top-level keys
// "cuecatalog.deploy" recognizes, matching this project's
// reject-unknown-keys convention (renderApplyKnownKeys's identical
// posture, one file over).
var cuecatalogDeployKnownKeys = map[string]bool{
	"show": true, "generation": true, "revision": true, "entries": true,
}

// catalogDeployWireParams is "cuecatalog.deploy"'s params, decoded via
// json.Marshal(params)+json.Unmarshal rather than field-by-field type
// assertions (renderops.go's own convention for scalar params does not
// scale to this operation's nested Entries array): the coordinator's own
// resolved-catalog shape (H3 spec section 3) reusing [cuecatalog.Entry]'s
// own JSON tags directly, so a node and the coordinator can never disagree
// about what one entry's wire shape is — there is exactly one definition
// of it, in pkg/cuecatalog, and both sides decode into it.
type catalogDeployWireParams struct {
	Show       string             `json:"show"`
	Generation int64              `json:"generation"`
	Revision   string             `json:"revision"`
	Entries    []cuecatalog.Entry `json:"entries"`
}

// catalogDeployOperation is the OperationFunc receiver for
// "cuecatalog.deploy". nodeID is needed because [cuecatalog.RevisionInput]
// covers the node id (H3 spec section 3.1) and this operation must compute
// the identical hash the coordinator computed for THIS node, not merely
// re-hash whatever it was told.
type catalogDeployOperation struct {
	nodeID string
	store  *heldcatalog.FileStore
}

// sortCatalogEntries returns a copy of entries, sorted by CueID with each
// entry's own AssetHashes sorted, matching [cuecatalog.RevisionInput]'s own
// doc comment requirement that a caller supply entries in exactly this
// canonical order before calling [cuecatalog.ComputeRevision]: JSON Schema
// canonicalization sorts object member names but never reorders an array,
// so an unstable order here would make an honest, byte-identical catalog
// hash differently than the coordinator's depending only on wire order.
// Never mutates entries itself, so a caller can persist the ORIGINAL
// (already coordinator-sorted, in practice) order if it ever mattered
// separately from the hash input.
func sortCatalogEntries(entries []cuecatalog.Entry) []cuecatalog.Entry {
	out := make([]cuecatalog.Entry, len(entries))
	copy(out, entries)

	sortAssetHashes := func(h []string) []string {
		if len(h) == 0 {
			return h
		}
		s := make([]string, len(h))
		copy(s, h)
		sort.Strings(s)
		return s
	}
	for i := range out {
		if out[i].Outputs.Render != nil {
			r := *out[i].Outputs.Render
			r.AssetHashes = sortAssetHashes(r.AssetHashes)
			out[i].Outputs.Render = &r
		}
		if out[i].Outputs.Audio != nil {
			a := *out[i].Outputs.Audio
			a.AssetHashes = sortAssetHashes(a.AssetHashes)
			out[i].Outputs.Audio = &a
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].CueID < out[j].CueID })
	return out
}

// deploy is the OperationFunc for "cuecatalog.deploy": decode the pushed
// catalog, recompute its revision locally, refuse to store it if the
// node's own computation disagrees with the coordinator's claimed
// revision, and otherwise persist it as this node's held catalog.
func (o *catalogDeployOperation) deploy(_ context.Context, params map[string]any, now func() time.Time) (OperationResult, error) {
	const action = "cuecatalog.deploy"

	if err := rejectUnknownKeys(action, params, cuecatalogDeployKnownKeys); err != nil {
		return OperationResult{}, err
	}

	raw, err := json.Marshal(params)
	if err != nil {
		return OperationResult{}, fmt.Errorf("%s: encoding params: %w", action, err)
	}
	var wire catalogDeployWireParams
	if err := json.Unmarshal(raw, &wire); err != nil {
		return OperationResult{}, fmt.Errorf("%s: decoding params: %w", action, err)
	}
	if wire.Show == "" {
		return OperationResult{}, fmt.Errorf("%s: params.show is required", action)
	}
	if wire.Generation < 1 {
		return OperationResult{}, fmt.Errorf("%s: params.generation must be a positive integer, got %d", action, wire.Generation)
	}
	if wire.Revision == "" {
		return OperationResult{}, fmt.Errorf("%s: params.revision is required", action)
	}

	sorted := sortCatalogEntries(wire.Entries)

	// The node computes its OWN revision from the pushed content, using the
	// identical function and canonicalization the coordinator used — see
	// pkg/cuecatalog's own doc comment. This is the check build item 1
	// requires: a node must never store a catalog it cannot independently
	// corroborate, even one delivered over an authenticated coordinator
	// command.
	computed, err := cuecatalog.ComputeRevision(cuecatalog.RevisionInput{
		Show: wire.Show, Generation: wire.Generation, Node: o.nodeID, Entries: sorted,
	})
	if err != nil {
		return OperationResult{}, fmt.Errorf("%s: computing catalog revision: %w", action, err)
	}
	if computed != wire.Revision {
		return OperationResult{}, fmt.Errorf(
			"%s: refusing to store catalog: computed revision %q does not match the coordinator's claimed revision %q for show %q generation %d",
			action, computed, wire.Revision, wire.Show, wire.Generation)
	}

	executedAt := now()
	rec := heldcatalog.HeldCatalog{
		Show: wire.Show, Generation: wire.Generation, Node: o.nodeID,
		Revision: computed, Entries: sorted, ReceivedAt: executedAt,
	}
	if err := o.store.Save(rec); err != nil {
		return OperationResult{}, fmt.Errorf("%s: persisting held catalog: %w", action, err)
	}

	// Read the persisted record back before reporting Confirmed, matching
	// OperationResult's own "evidence collected after the change" rule
	// (this package's standing convention — see agentEchoState.apply's
	// identical read-back-not-echo shape in command.go). ObservedAt is a
	// separate clock read from ExecutedAt (when the write happened), per
	// OperationResult's own doc comment.
	readBack, ok, err := o.store.Load()
	observedAt := now()
	if err != nil {
		return OperationResult{}, fmt.Errorf("%s: reading back persisted held catalog: %w", action, err)
	}

	return OperationResult{
		Confirmed:  ok && readBack.Revision == computed && readBack.Show == wire.Show && readBack.Generation == wire.Generation,
		Signal:     "node.cuecatalog.revision",
		Value:      readBack.Revision,
		ExecutedAt: executedAt,
		ObservedAt: observedAt,
	}, nil
}
