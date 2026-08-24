// Package cuecatalog is the shared wire shape and revision-hash function
// for Track H seam H3's resolved Cue catalog (TRACK-H-H3-SPEC.md sections
// 3 and 3.1). It carries no store access, no HTTP, and no coordinator- or
// agent-specific logic: it exists ONLY so the coordinator
// (internal/coordinator/assetsync) and the node agent (internal/agent) can
// each build the identical input shape and call the identical
// [ComputeRevision] function, so a disagreement between what the
// coordinator resolved and what a node holds is detectable rather than
// assumed away (H3 spec section 3.1). internal/agent must never import
// internal/coordinator (or vice versa) — this package is the deliberate
// third place both sides depend on instead, the same role pkg/fppidentity
// and pkg/observation already play for other cross-boundary contracts.
package cuecatalog

import (
	"encoding/json"
	"fmt"

	"github.com/showmeshsystems/showmesh/pkg/fppidentity"
)

// RenderOutput is one Cue's resolved render output for one node: the
// logical sequence name, the runtime filename a node must open to render
// it, and the content hashes of the assets it needs (H3 spec section 3,
// reusing Track E's own asset identity rather than a second expectation
// model). Sequence is a LOGICAL identity (copied from show.cue's own
// outputs.render.sequence) and is never itself a filename a node may
// open — see Filename's own doc comment. AssetHashes is sorted and
// de-duplicated by the caller before this type is populated, so two
// callers resolving the identical output always produce byte-identical
// JSON.
type RenderOutput struct {
	Sequence string `json:"sequence"`

	// Filename is the runtime filename this Cue's resolved asset is
	// stored under on a node's asset directory — the same identity
	// renderApplyParamsPayload.FSEQFilename carries for a plain (non-Cue)
	// render.surface.apply. A node MUST open this filename and verify the
	// opened file's content hash against AssetHashes before rendering it;
	// Sequence is a logical identity only, never something a node opens
	// directly (ADR-043 decision 6 — filename/sequence identity is
	// corroboration, never selection authority). Empty when the resolved
	// catalog has no asset uploaded for this Cue's declared sequence yet.
	Filename string `json:"filename"`

	AssetHashes []string `json:"assetHashes"`
}

// AudioOutput is one Cue's resolved audio output for one node. Asset is a
// LOGICAL identity (the same "sequence" space RenderOutput.Sequence
// occupies — see internal/coordinator/assetsync/cuecatalog.go's own
// comment on resolveCueOutputs' Audio branch); Filename is the runtime
// filename a node must actually open, mirroring RenderOutput.Filename's
// identical rule one output over.
type AudioOutput struct {
	Asset             string   `json:"asset"`
	Filename          string   `json:"filename"`
	StartOffsetMillis int      `json:"startOffsetMillis"`
	AssetHashes       []string `json:"assetHashes"`
}

// LTCOutput is one Cue's resolved LTC output. It carries no asset of its
// own — LTC is derived from the audio output's own asset at runtime (H0.3),
// not a second file.
type LTCOutput struct {
	StartOffsetMillis int `json:"startOffsetMillis"`
}

// AnnouncementOutput is one Cue's resolved announcement policy. It carries
// no asset of its own for the identical reason LTCOutput does not: the
// announcement plays the Cue's own audio output (TRACK-H-H1-SPEC.md
// section 2).
type AnnouncementOutput struct {
	Policy     string   `json:"policy"`
	DuckGainDb *float64 `json:"duckGainDb,omitempty"`
	FadeMillis int      `json:"fadeMillis"`
}

// Outputs is one Cue's resolved outputs, restricted to the ones that
// concern the node the catalog was resolved for (H3 spec section 3, point
// 3). A nil member means that output either was not declared on the Cue,
// or does not concern this node.
type Outputs struct {
	Render       *RenderOutput       `json:"render,omitempty"`
	Audio        *AudioOutput        `json:"audio,omitempty"`
	LTC          *LTCOutput          `json:"ltc,omitempty"`
	Announcement *AnnouncementOutput `json:"announcement,omitempty"`
}

// Entry is one Cue's row in a resolved catalog: its identity (id and
// revision) plus its resolved, node-scoped outputs.
type Entry struct {
	CueID       string  `json:"cueId"`
	CueRevision int64   `json:"cueRevision"`
	Outputs     Outputs `json:"outputs"`
}

// RevisionInput is EXACTLY what the catalog revision hash covers (H3 spec
// section 3.1): "the Show id, the generation, the node id, and every Cue
// id, Cue revision, resolved output, and asset hash in the catalog." A
// resolved output's own Filename IS part of "resolved output" and is
// covered here, through Entries — it is what a node must actually open,
// not a display-only label the way an asset's size in bytes would be:
// two catalogs that differ only in which runtime filename a node must
// open to reach identical bytes are different catalogs, and must hash
// differently. Only genuinely display-only fields (sizes, upload
// timestamps, and the like) are excluded, because the coordinator and a
// node would then only agree on a revision when those ALSO matched, which
// is not what section 3.1 promises ("the revision identifies the content,
// not the delivery"). Entries must be sorted by CueID, and each Entry's
// AssetHashes sorted, by the caller before this type is built: JSON Schema
// canonicalization (RFC 8785, via pkg/fppidentity) sorts object member
// names but never reorders an array, so an unstable Entries or AssetHashes
// order would make two structurally-identical catalogs hash differently
// depending only on iteration order.
type RevisionInput struct {
	Show       string  `json:"show"`
	Generation int64   `json:"generation"`
	Node       string  `json:"node"`
	Entries    []Entry `json:"entries"`
}

// ComputeRevision is the ONE exported function TRACK-H-H3-SPEC.md section
// 3.1 requires the coordinator and the node to both call: SHA-256 over
// pkg/fppidentity's canonical JSON serialization of in. Two callers given
// the same in always compute the same revision; no other function in
// either binary may compute a catalog revision a second way.
func ComputeRevision(in RevisionInput) (string, error) {
	raw, err := json.Marshal(in)
	if err != nil {
		return "", fmt.Errorf("cuecatalog: marshal revision input: %w", err)
	}
	_, hashHex, err := fppidentity.HashCanonical(raw)
	if err != nil {
		return "", fmt.Errorf("cuecatalog: canonicalize revision input: %w", err)
	}
	return hashHex, nil
}
