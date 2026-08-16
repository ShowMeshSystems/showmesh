package v1

// Wire types for the asset store (Track E seam E3/E4, ADR-028). Nothing
// here reuses internal/coordinator/store's AssetRecord directly (ADR-020:
// the wire layer is separate from the domain layer), matching
// showobjects.go's precedent one seam over.

// Asset is one row of the coordinator's asset metadata store: an
// artifact's identity, never its bytes (ADR-028 decision 4). Target mirrors
// store.AssetRecord.TargetID — empty when TargetKind is "show".
// RuntimeFilename is preserved but carries no identity of its own (ADR-028
// decision 1): two different Asset values may share the same
// RuntimeFilename.
type Asset struct {
	ID                     string  `json:"id"`
	Show                   string  `json:"show"`
	Sequence               string  `json:"sequence"`
	TargetKind             string  `json:"targetKind"`
	Target                 string  `json:"target"`
	MediaType              string  `json:"mediaType"`
	ContentHash            string  `json:"contentHash"`
	RuntimeFilename        string  `json:"runtimeFilename"`
	SizeBytes              int64   `json:"sizeBytes"`
	CreatedAt              string  `json:"createdAt"`
	CreatedByPrincipalID   *string `json:"createdByPrincipalId"`
	CreatedByPrincipalName *string `json:"createdByPrincipalName"`
	SupersededAt           *string `json:"supersededAt"`
	Current                bool    `json:"current"`
}

// AssetResponse is the body of POST /assets and GET /assets/{id}.
type AssetResponse struct {
	ServerTime string `json:"serverTime"`
	Asset      Asset  `json:"asset"`
}

// AssetsListResponse is the body of GET /assets.
type AssetsListResponse struct {
	ServerTime string  `json:"serverTime"`
	Assets     []Asset `json:"assets"`
}
