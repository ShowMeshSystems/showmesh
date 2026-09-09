package v1

// ResyncNodeAssetsResult is what POST /nodes/{nodeId}/assets/resync
// accepts: nothing more. The outcome surfaces later on
// GET /nodes/{nodeId}/assets, never here.
type ResyncNodeAssetsResult struct {
	Node       string `json:"node"`
	AcceptedAt string `json:"acceptedAt"`
}

// ResyncNodeAssetsResponse wraps ResyncNodeAssetsResult with the standard
// serverTime envelope (contract section 6.2).
type ResyncNodeAssetsResponse struct {
	ServerTime string                 `json:"serverTime"`
	Resync     ResyncNodeAssetsResult `json:"resync"`
}
