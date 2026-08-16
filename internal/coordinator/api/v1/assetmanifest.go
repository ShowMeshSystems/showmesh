package v1

// Wire types for Track E seam E5's asset manifest surface (ADR-028,
// ADR-020): "what should a node hold" versus "what does it actually
// hold", rendered read-only. The readiness verdict itself is computed
// entirely by internal/coordinator/assetsync.ComputeNodeManifest; this
// file only names its wire shape.

// MissingAsset is one expected asset a manifest found the node does not
// currently hold, named by sequence, filename, and content hash (never
// filename alone — ADR-028 decision 1).
type MissingAsset struct {
	AssetID     string `json:"assetId"`
	Sequence    string `json:"sequence"`
	Filename    string `json:"filename"`
	ContentHash string `json:"contentHash"`
	SizeBytes   int64  `json:"sizeBytes"`
}

// AssetGap names a sequence the active show has some current asset for
// that a node carrying one or more surfaces in that show has no coverage
// for at all — inferred from the show's own asset rows, not a stored
// surface-to-sequence link (see assetsync's own doc comment).
type AssetGap struct {
	Sequence string   `json:"sequence"`
	Surfaces []string `json:"surfaces"`
}

// ExtraAsset is one asset a node holds that this manifest did not expect.
// Never an error and never a basis for deletion.
type ExtraAsset struct {
	ContentHash string `json:"contentHash"`
	Filename    string `json:"filename"`
	SizeBytes   int64  `json:"sizeBytes"`
}

// NodeAssetManifest is one node's asset readiness verdict: the body of
// GET /nodes/{nodeId}/assets, and one element of GET /assets/manifest's
// "nodes" array.
//
// State is one of "ready", "not_ready", "unknown" — see
// assetsync.ManifestState. Reason is non-null whenever State is not
// "ready" (ADR-020: absent evidence is stated, never omitted), naming the
// specific cause; a node's report going stale, going missing, or reporting
// incomplete, and "no active show is configured", each render a distinct
// Reason string. Missing and Gaps are populated only when State is
// "not_ready". Extra is populated whenever a fresh report exists,
// regardless of State — see assetsync.NodeManifest's own doc comment for
// why a stale report populates neither Missing nor Extra: what a stale
// report says a node holds is exactly as unreliable as what it says a
// node lacks. ObservedAt is null when State is "unknown": there is no
// evidence an unknown verdict rests on, so there is nothing to date it by
// — never defaulted to serverTime.
type NodeAssetManifest struct {
	Node       string         `json:"node"`
	State      string         `json:"state"`
	Reason     *string        `json:"reason"`
	Missing    []MissingAsset `json:"missing"`
	Gaps       []AssetGap     `json:"gaps"`
	Extra      []ExtraAsset   `json:"extra"`
	ObservedAt *string        `json:"observedAt"`
}

// NodeAssetManifestResponse is the body of GET /nodes/{nodeId}/assets.
type NodeAssetManifestResponse struct {
	ServerTime string            `json:"serverTime"`
	Manifest   NodeAssetManifest `json:"manifest"`
}

// AssetManifestResponse is the body of GET /assets/manifest: every
// declared node's manifest, in the order the coordinator's node
// declarations are stored.
type AssetManifestResponse struct {
	ServerTime string              `json:"serverTime"`
	Nodes      []NodeAssetManifest `json:"nodes"`
}
