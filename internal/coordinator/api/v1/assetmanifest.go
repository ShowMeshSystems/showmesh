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

// AssetVerdictSource names WHY a node was expected to hold one asset:
// which of ADR-049's precedence tiers matched (see
// assetsync.AssetSourceKind), the node the row was actually uploaded for
// (RegisteredTarget, empty for a show-wide row), and, for a borrowed
// copy, the Cue or night.session id(s) whose declared target list put
// this node on the hook (ReferencedBy, always empty for "node"/"show").
type AssetVerdictSource struct {
	Kind             string   `json:"kind"`
	RegisteredTarget string   `json:"registeredTarget"`
	ReferencedBy     []string `json:"referencedBy"`
}

// AssetLastFetch is the asset-sync service's own process-lifetime record
// of its most recent asset.fetch dispatch for one expected asset:
// in-memory only, reset on every coordinator restart, and never a
// persisted commands-table row (unlike [ResyncRequestStatus]'s
// asset.inventory.request rows, which ARE persisted: asset.fetch
// dispatch is deliberately not, see assetsync.Service's own doc comment).
// Absent when this coordinator process has never dispatched or observed a
// failure for this exact (node, asset). state is "in_flight" (dispatched,
// no result yet) or "failed" (the node's own result reported a
// non-success outcome); failureReason/failedAt are set only when failed.
// dispatchedAt is null when this process no longer remembers a dispatch
// time but still remembers the failure.
type AssetLastFetch struct {
	DispatchedAt  *string `json:"dispatchedAt"`
	State         string  `json:"state"`
	FailureReason *string `json:"failureReason"`
	FailedAt      *string `json:"failedAt"`
}

// AssetSyncVerdict is one expected asset's per-node sync verdict (D-016
// item 2), added additively (ADR-020): a client that has never heard of
// this field keeps reading node/state/reason/missing/gaps/extra/observedAt
// exactly as before. It exists only for an EXPECTED asset, is keyed by
// assetId (never by filename — ADR-028 decision 1), and state is one of
// "held", "superseded" (the node holds bytes this exact asset identity
// used to serve, before being superseded), or "absent" (the node holds
// nothing recognizable for this identity at all). See
// assetsync.AssetVerdictState for the derivation. Source and LastFetch are
// each additive in the identical sense.
type AssetSyncVerdict struct {
	AssetID     string             `json:"assetId"`
	Sequence    string             `json:"sequence"`
	Filename    string             `json:"filename"`
	ContentHash string             `json:"contentHash"`
	SizeBytes   int64              `json:"sizeBytes"`
	State       string             `json:"state"`
	Source      AssetVerdictSource `json:"source"`
	LastFetch   *AssetLastFetch    `json:"lastFetch,omitempty"`
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
// Verdicts is additive (ADR-020): absent, or an empty array, whenever no
// fresh inventory report exists to derive it from — exactly the same
// condition Extra is populated under. See assetsync.NodeManifest.Verdicts.
type NodeAssetManifest struct {
	Node       string             `json:"node"`
	State      string             `json:"state"`
	Reason     *string            `json:"reason"`
	Missing    []MissingAsset     `json:"missing"`
	Gaps       []AssetGap         `json:"gaps"`
	Extra      []ExtraAsset       `json:"extra"`
	ObservedAt *string            `json:"observedAt"`
	Verdicts   []AssetSyncVerdict `json:"verdicts,omitempty"`
	// ResyncRequest is this node's most recent operator-issued "Re-sync
	// all" request (POST /nodes/{nodeId}/assets/resync), populated only
	// on GET /nodes/{nodeId}/assets (never on GET /assets/manifest's
	// fleet-wide listing) and omitted entirely when this node has never
	// had one. Additive: every other field above is unaffected by its
	// presence or absence.
	ResyncRequest *ResyncRequestStatus `json:"resyncRequest,omitempty"`
	// LastSyncPassAt is the asset-sync service's own process-lifetime
	// record of when it last ran a check against this node. Additive,
	// in-memory only, reset on restart, and distinct from ObservedAt (the
	// NODE's own report time): omitted when this process has never run a
	// pass against this node.
	LastSyncPassAt *string `json:"lastSyncPassAt,omitempty"`
}

// ResyncRequestStatus is one asset.inventory.request commands row,
// rendered for an operator reading GET /nodes/{nodeId}/assets rather than
// the commands table directly. State is the command's own lifecycle word
// ("dispatched" while still waiting on the node, "resolved" once a fresh
// report confirmed it, "failed" on a publish failure or a reconciliation
// sweep timeout); OutcomeReason carries the human-readable reason once
// State is "resolved" or "failed" and is nil while still "dispatched".
// ResolvedAt is nil until State leaves "dispatched".
type ResyncRequestStatus struct {
	CommandID     string  `json:"commandId"`
	State         string  `json:"state"`
	OutcomeReason *string `json:"outcomeReason"`
	IssuedAt      string  `json:"issuedAt"`
	ResolvedAt    *string `json:"resolvedAt"`
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
