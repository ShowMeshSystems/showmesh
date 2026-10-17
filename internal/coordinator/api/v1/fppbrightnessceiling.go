package v1

// FPPBrightnessCeilingRequest is the body of PUT
// /api/v1/fpp/brightness/ceiling. Pointers keep an absent field, an explicit
// null and a deliberate 0 apart.
type FPPBrightnessCeilingRequest struct {
	// TargetPercent is the display ceiling, 0-100. Out of range is refused.
	TargetPercent *int `json:"targetPercent"`
	// FadeSeconds is the fade, 0-86400. Absent means 0.
	FadeSeconds *int `json:"fadeSeconds,omitempty"`
}

// FPPBrightnessResponse is GET /api/v1/fpp/brightness and the 200 body of
// the ceiling write.
type FPPBrightnessResponse struct {
	ServerTime string `json:"serverTime"`
	// Ceiling is null until a ceiling has been set.
	Ceiling *FPPBrightnessCeiling `json:"ceiling"`
	Players []FPPBrightnessPlayer `json:"players"`
}

// FPPBrightnessCeiling is the display's desired ceiling.
type FPPBrightnessCeiling struct {
	TargetPercent int `json:"targetPercent"`
	// FadeEndsAt is when a fade to TargetPercent ends, or null for none.
	FadeEndsAt *string              `json:"fadeEndsAt"`
	ChangedAt  string               `json:"changedAt"`
	ChangedBy  FPPBrightnessChanger `json:"changedBy"`
}

// FPPBrightnessChanger is who set the ceiling: an operator principal, or the
// FPP a change made on it was taken from.
type FPPBrightnessChanger struct {
	Kind string `json:"kind"`
	ID   string `json:"id"`
	Name string `json:"name,omitempty"`
}

// FPPBrightnessPlayer is one FPP against the display ceiling.
type FPPBrightnessPlayer struct {
	InstanceID string `json:"instanceId"`
	// State is following, catchingUp, notReporting or pluginTooOld.
	State string `json:"state"`
	// Reason is an operator sentence, empty only while following.
	Reason string `json:"reason,omitempty"`
	// Ceiling is the FPP's ceiling now; CeilingTarget where a fade on it is
	// going, absent from a plugin too old to report it.
	Ceiling       *int    `json:"ceiling,omitempty"`
	CeilingTarget *int    `json:"ceilingTarget,omitempty"`
	ReportedAt    *string `json:"reportedAt,omitempty"`
}
