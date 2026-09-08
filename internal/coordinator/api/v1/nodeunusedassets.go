package v1

// This file is this seam's own wire shapes: which of a node's held assets no
// Cue in its resolved catalog references (GET /nodes/{nodeId}/assets/unused),
// and dispatching removal of one (POST /nodes/{nodeId}/assets/remove).

// UnusedAsset is one asset a node holds that its resolved Cue catalog does
// not reference. Sequence is present only when this coordinator's own asset
// records can still attribute the content hash to one - absent, never a
// fabricated value, for a file no asset record explains.
type UnusedAsset struct {
	ContentHash string  `json:"contentHash"`
	Filename    string  `json:"filename"`
	SizeBytes   int64   `json:"sizeBytes"`
	Sequence    *string `json:"sequence,omitempty"`
}

// NodeUnusedAssetsResponse is GET /nodes/{nodeId}/assets/unused's body.
// State reuses the identical ready/not_ready/unknown vocabulary
// NodeAssetManifest.State already carries - this route answers a question
// over the SAME evidence that route's State already summarizes, never a
// second evidence source. Reason is present only when State is "unknown":
// the coordinator cannot yet tell what this node holds, or the active show
// is unconfigured, so Unused is withheld (an empty array here means
// nothing, never "zero unused assets" - a caller must read State first).
// ObservedAt is present exactly when State is not "unknown", mirroring
// NodeAssetManifest.ObservedAt's identical rule.
type NodeUnusedAssetsResponse struct {
	ServerTime string  `json:"serverTime"`
	Node       string  `json:"node"`
	State      string  `json:"state"`
	Reason     *string `json:"reason,omitempty"`
	ObservedAt *string `json:"observedAt,omitempty"`

	Unused []UnusedAsset `json:"unused"`
}

// RemoveNodeAssetRequest is POST /nodes/{nodeId}/assets/remove's body: the
// content hash to remove, named by value (not in the path - a
// "sha256:<hex>" value is not a safe path segment), mirroring
// CueCatalogAcknowledgeRequest's identical convention. IdempotencyKey is
// optional, mirroring cueCatalogDeployRequestBody's own field one route
// over.
type RemoveNodeAssetRequest struct {
	ContentHash    string `json:"contentHash"`
	IdempotencyKey string `json:"idempotencyKey,omitempty"`
}

// RemoveNodeAssetResult is the outcome of dispatching asset.remove to one
// node - field-for-field mirror of CueCatalogDeployResult with ContentHash
// standing in for Show/Generation/Revision. Outcome/Reason reuse
// pkg/mqttproto's own closed vocabulary ("confirmed", "unconfirmed",
// "failed") verbatim; there is no "refused" outcome here because a refusal
// (the asset is in use) is checked BEFORE dispatch and reported as a 409,
// never as a dispatched-and-refused command.
//
// Confirmed means only that the AGENT reported the file gone from its own
// disk on its own result topic - it does NOT mean this coordinator's own
// node-asset-inventory row has caught up yet; that only happens on the
// node's NEXT inventory report (which a successful removal triggers
// immediately, matching asset.fetch's existing trigger, but which this
// response does not itself wait for).
type RemoveNodeAssetResult struct {
	CommandID      string `json:"commandId"`
	IdempotencyKey string `json:"idempotencyKey"`
	Node           string `json:"node"`
	ContentHash    string `json:"contentHash"`
	Replay         bool   `json:"replay"`

	Outcome string `json:"outcome"`
	Reason  string `json:"reason,omitempty"`

	DispatchedAt *string `json:"dispatchedAt"`
	ResolvedAt   *string `json:"resolvedAt,omitempty"`
}

// RemoveNodeAssetResponse wraps RemoveNodeAssetResult with the standard
// serverTime envelope (contract section 6.2).
type RemoveNodeAssetResponse struct {
	ServerTime string                `json:"serverTime"`
	Command    RemoveNodeAssetResult `json:"command"`
}
