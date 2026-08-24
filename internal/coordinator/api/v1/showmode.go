package v1

// This file is ADR-033's wire contract: the installation-wide operating
// mode singleton. Mirrors RenderSettingsConfigResponse's shape exactly.

// ConfigShowModePayload is the "show.mode" configuration kind's decoded
// payload: the body PUT /config/show.mode accepts (a full replacement -
// "mode" required), and the "payload" member of GET /config/show.mode's
// response.
//
// Mode is one of "program" or "show" and never "unknown". "unknown" is a
// node-side held-value state (ADR-033 decision 5), not a value an operator
// can write, so it is deliberately absent from this vocabulary.
type ConfigShowModePayload struct {
	Mode string `json:"mode"`
}

// ShowModeConfigResponse is the body of GET and PUT /config/show.mode.
// revision 0 / source "default" means nothing has ever been written and
// payload carries the built-in default ("program").
type ShowModeConfigResponse struct {
	ServerTime             string                `json:"serverTime"`
	Kind                   string                `json:"kind"`
	Revision               int64                 `json:"revision"`
	Payload                ConfigShowModePayload `json:"payload"`
	UpdatedAt              string                `json:"updatedAt"`
	CreatedByPrincipalID   *string               `json:"createdByPrincipalId"`
	CreatedByPrincipalName *string               `json:"createdByPrincipalName"`
	Source                 string                `json:"source"`

	// ResolumeWebSocketEffect states, in every response, what the CURRENT
	// mode does to the only behaviour that reads the mode in this build:
	// the Resolume WebSocket footprint switch (ADR-033 decision 2). It
	// names the mode as the reason, which is ADR-033 decision 3's
	// requirement that a degraded behaviour caused by the mode says so
	// where the operator can see it. Always non-empty.
	ResolumeWebSocketEffect string `json:"resolumeWebSocketEffect"`
}
