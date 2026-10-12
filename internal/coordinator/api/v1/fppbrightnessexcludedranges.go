package v1

// FPPChannelRange is one run of channels: a 1-based first channel and how
// many channels follow from it.
type FPPChannelRange struct {
	StartChannel int `json:"startChannel"`
	ChannelCount int `json:"channelCount"`
}

// FPPBrightnessExcludedRangesRequest is the body of PUT
// /api/v1/fpp/{instanceId}/brightness/excluded-ranges. Ranges replaces
// the whole list; an empty list clears it.
type FPPBrightnessExcludedRangesRequest struct {
	Ranges []FPPChannelRange `json:"ranges"`

	// RequestID is the caller-minted idempotency key. Omitted, the
	// coordinator mints one and echoes it.
	RequestID string `json:"requestId,omitempty"`
}

// FPPBrightnessExcludedRangesResponse is the 200 body of the excluded
// ranges write: the write's own result, plus the list the plugin holds.
type FPPBrightnessExcludedRangesResponse struct {
	ServerTime string           `json:"serverTime"`
	Command    FPPCommandResult `json:"command"`

	// ExcludedRanges is present only when the plugin reported the new
	// list within the read-back window. Absent means the list was not
	// read back, never that the write failed. An empty list means the
	// plugin now excludes nothing.
	ExcludedRanges *[]FPPChannelRange `json:"excludedRanges,omitempty"`
}
