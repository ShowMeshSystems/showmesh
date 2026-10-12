package v1

// ConfigReleaseSettingsPayload is the release.settings payload: whether the
// newest-release check runs, and how often. On a PUT either field may be left
// out to keep its stored value.
type ConfigReleaseSettingsPayload struct {
	Enabled              bool  `json:"enabled"`
	CheckIntervalSeconds int64 `json:"checkIntervalSeconds"`
}

// ReleaseSettingsConfigResponse is the body of GET and PUT
// /config/release.settings. Revision 0 means nothing is stored and the payload
// is what the coordinator runs with: source "default", or "env" when it comes
// from the environment and could not be stored.
type ReleaseSettingsConfigResponse struct {
	ServerTime             string                       `json:"serverTime"`
	Kind                   string                       `json:"kind"`
	Revision               int64                        `json:"revision"`
	Payload                ConfigReleaseSettingsPayload `json:"payload"`
	UpdatedAt              string                       `json:"updatedAt"`
	CreatedByPrincipalID   *string                      `json:"createdByPrincipalId"`
	CreatedByPrincipalName *string                      `json:"createdByPrincipalName"`
	Source                 string                       `json:"source"`
}
