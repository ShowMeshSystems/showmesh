package v1

// This file is the wire contract (ADR-039, Track I seam I1): the
// "node.clock" collection. ConfigObjectSummary/ConfigObjectsListResponse
// (showmacros.go) and ConfigRevisionMeta/ConfigRevisionsResponse
// (types.go) are reused verbatim, matching audio.go's identical note one
// kind over.

// ConfigNodeClock is the "node.clock" configuration kind's decoded
// payload: the body PUT /config/node.clock/{nodeId} accepts (a full
// replacement), and the "payload" member of GET
// /config/node.clock/{nodeId}'s response.
type ConfigNodeClock struct {
	Provider  string `json:"provider"`
	Interface string `json:"interface"`
	Domain    int    `json:"domain"`

	ClientOnly           bool `json:"clientOnly,omitempty"`
	HoldoverLimitSeconds int  `json:"holdoverLimitSeconds,omitempty"`
	Priority1            int  `json:"priority1,omitempty"`
	HardwareTimestamping bool `json:"hardwareTimestamping,omitempty"`

	ExternalUDSAddress string `json:"externalUdsAddress,omitempty"`
	FPPBaseURL         string `json:"fppBaseUrl,omitempty"`
}

// NodeClockConfigResponse is the body of GET and PUT
// /config/node.clock/{nodeId}.
type NodeClockConfigResponse struct {
	ServerTime             string          `json:"serverTime"`
	Kind                   string          `json:"kind"`
	ID                     string          `json:"id"`
	Revision               int64           `json:"revision"`
	Payload                ConfigNodeClock `json:"payload"`
	UpdatedAt              string          `json:"updatedAt"`
	CreatedByPrincipalID   *string         `json:"createdByPrincipalId"`
	CreatedByPrincipalName *string         `json:"createdByPrincipalName"`
	Source                 string          `json:"source"`
}
