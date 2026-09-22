package v1

// FPPBrightnessCeilingRequest is the body of POST
// /api/v1/fpp/{instanceId}/brightness/ceiling. Ceiling is a pointer so an
// absent field, an explicit null, and a deliberate 0 stay three different
// things.
type FPPBrightnessCeilingRequest struct {
	// Ceiling is the brightness ceiling, 0-100 inclusive. Out of range is
	// refused, never clamped.
	Ceiling *int `json:"ceiling"`

	// RequestID is the caller-minted idempotency key. Omitted, the
	// coordinator mints one and echoes it.
	RequestID string `json:"requestId,omitempty"`
}

// FPPBrightnessCeilingResponse is the 200 body of the ceiling write: the
// dispatched command's own result, plus the ceiling the plugin read back.
type FPPBrightnessCeilingResponse struct {
	ServerTime string           `json:"serverTime"`
	Command    FPPCommandResult `json:"command"`

	// Ceiling is present only when the plugin's own brightness route
	// reported the new value within the read-back window. Absent means
	// the value was not read back, never that the write failed.
	Ceiling *int `json:"ceiling,omitempty"`
}
