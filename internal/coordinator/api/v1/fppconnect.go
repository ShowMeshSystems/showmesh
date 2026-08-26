package v1

// This file is the wire contract (ADR-039, ADR-044 decision 5) for the
// "fppconnect.settings" singleton: the enable flag and the two byte caps
// that bound a node's xLights ingestion listener.

// ConfigFPPConnectSettingsPayload is the "fppconnect.settings"
// configuration kind's decoded payload: the body PUT
// /config/fppconnect.settings accepts (a full replacement — every field
// required), and the "payload" member of GET /config/fppconnect.settings'
// response.
type ConfigFPPConnectSettingsPayload struct {
	Enabled          bool  `json:"enabled"`
	MaxFileBytes     int64 `json:"maxFileBytes"`
	MaxAssetDirBytes int64 `json:"maxAssetDirBytes"`
}

// FPPConnectSettingsConfigResponse is the body of GET and PUT
// /config/fppconnect.settings. revision 0 / source "default" means nothing
// has ever been written and payload carries the built-in default —
// mirrors AudioSettingsConfigResponse's identical "no 404, a stated
// default" posture.
type FPPConnectSettingsConfigResponse struct {
	ServerTime             string                          `json:"serverTime"`
	Kind                   string                          `json:"kind"`
	Revision               int64                           `json:"revision"`
	Payload                ConfigFPPConnectSettingsPayload `json:"payload"`
	UpdatedAt              string                          `json:"updatedAt"`
	CreatedByPrincipalID   *string                         `json:"createdByPrincipalId"`
	CreatedByPrincipalName *string                         `json:"createdByPrincipalName"`
	Source                 string                          `json:"source"`
}
