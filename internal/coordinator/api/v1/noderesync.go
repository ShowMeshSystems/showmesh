package v1

// ResyncNodeAssetsResult is what POST /nodes/{nodeId}/assets/resync
// accepts: nothing more. The outcome surfaces later on
// GET /nodes/{nodeId}/assets, never here.
type ResyncNodeAssetsResult struct {
	Node       string `json:"node"`
	AcceptedAt string `json:"acceptedAt"`

	// InventoryRequestCommandID names the asset.inventory.request this
	// route issued to nodeID, if publishing it succeeded. Empty when
	// nothing could be published (never a placeholder value): the node's
	// own outstanding re-sync intent still runs off its next ordinary
	// report in that case, just without this specific push.
	InventoryRequestCommandID string `json:"inventoryRequestCommandId,omitempty"`
}

// ResyncNodeAssetsResponse wraps ResyncNodeAssetsResult with the standard
// serverTime envelope (contract section 6.2).
type ResyncNodeAssetsResponse struct {
	ServerTime string                 `json:"serverTime"`
	Resync     ResyncNodeAssetsResult `json:"resync"`
}
