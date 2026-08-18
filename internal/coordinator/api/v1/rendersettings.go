package v1

// This file is Track B seam B2c's wire contract (ADR-039,
// TRACK-B-BUILD-CONTRACT.md §"render.settings"): the singleton
// idle-output/restart-policy configuration kind. Mirrors
// resolumerecovery.go's ConfigResolumeRecoveryPayload/
// ResolumeRecoveryConfigResponse shape exactly.

// ConfigRenderRestartPolicy is render.settings.restartPolicy: the render
// pipeline supervisor's bounded exponential backoff.
type ConfigRenderRestartPolicy struct {
	InitialDelaySeconds        int `json:"initialDelaySeconds"`
	MaxDelaySeconds            int `json:"maxDelaySeconds"`
	MaxConsecutiveFastFailures int `json:"maxConsecutiveFastFailures"`
}

// ConfigRenderSettingsPayload is the "render.settings" configuration kind's
// decoded payload: the body PUT /config/render.settings accepts (a full
// replacement — every field required), and the "payload" member of GET
// /config/render.settings' response.
type ConfigRenderSettingsPayload struct {
	IdleOutput    string                    `json:"idleOutput"`
	RestartPolicy ConfigRenderRestartPolicy `json:"restartPolicy"`
}

// RenderSettingsConfigResponse is the body of GET and PUT
// /config/render.settings. revision 0 / source "default" means nothing has
// ever been written and payload carries the built-in default — mirrors
// ResolumeRecoveryConfigResponse's identical "no 404, a stated default"
// posture.
type RenderSettingsConfigResponse struct {
	ServerTime             string                      `json:"serverTime"`
	Kind                   string                      `json:"kind"`
	Revision               int64                       `json:"revision"`
	Payload                ConfigRenderSettingsPayload `json:"payload"`
	UpdatedAt              string                      `json:"updatedAt"`
	CreatedByPrincipalID   *string                     `json:"createdByPrincipalId"`
	CreatedByPrincipalName *string                     `json:"createdByPrincipalName"`
	Source                 string                      `json:"source"`

	// IdleOutputEffectiveNote states, in every response (there is no
	// config-push path to a node beyond render.surface.apply — build
	// contract ruling 4), that this value takes effect at each surface's
	// OWN next render.surface.apply dispatch: a surface already applied
	// keeps drawing whatever idleOutput was resolved into ITS assignment at
	// the time it was applied, unaffected by this write, until it is
	// re-applied. Stated explicitly rather than left for an operator to
	// discover by watching a surface not change.
	IdleOutputEffectiveNote string `json:"idleOutputEffectiveNote"`
}
