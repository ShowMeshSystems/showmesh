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

	// Rendition carries this asset's 48kHz/16-bit/stereo WAV rendition
	// state (owner ruling 2026-09-18, PCM show audio). Present only when
	// MediaType is "audio"; nil for every other media type. nil also when
	// MediaType is "audio" but no rendition has ever been queued for this
	// asset's content: the coordinator's own reconcile pass has not
	// reached it yet.
	Rendition *AssetRendition `json:"rendition,omitempty"`
}

// AssetRendition is one audio asset's rendition state.
type AssetRendition struct {
	// Status is "rendering", "ready", or "failed".
	Status string `json:"status"`
	// Format is the rendition's fixed format string, set only when
	// Status is "ready".
	Format string `json:"format,omitempty"`
	// DurationMillis is the rendition's playable duration, set only when
	// Status is "ready".
	DurationMillis int64 `json:"durationMillis,omitempty"`
	// FailureReason names why the last transcode attempt failed, set only
	// when Status is "failed". The original file is still served to every
	// node regardless.
	FailureReason string `json:"failureReason,omitempty"`
}

// AssetResponse is the body of POST /assets and GET /assets/{id}.
// RolledBack is true only on a POST that rolled back (ADR-028 decision 10);
// always false on GET.
type AssetResponse struct {
	ServerTime string `json:"serverTime"`
	Asset      Asset  `json:"asset"`
	RolledBack bool   `json:"rolledBack"`
}

// AssetsListResponse is the body of GET /assets.
type AssetsListResponse struct {
	ServerTime string  `json:"serverTime"`
	Assets     []Asset `json:"assets"`
}
