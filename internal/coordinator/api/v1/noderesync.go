package v1

// ResyncNodeAssetsResult is what POST /nodes/{nodeId}/assets/resync accepts:
// nothing more, since this route dispatches nothing of its own and holds no
// downstream confirmation loop (mirroring NightCommandResponse's identical
// 202 posture one seam over). The re-sync itself runs on the existing
// asset-sync service's own gap-driven dispatch; its outcome surfaces later
// on GET /nodes/{nodeId}/assets, from the node's own next asset report,
// never from this response.
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
