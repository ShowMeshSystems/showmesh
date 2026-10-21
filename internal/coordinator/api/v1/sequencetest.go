package v1

// ConfigSequenceTestEntry maps one FPP sequence filename to a Cue (ADR-067).
type ConfigSequenceTestEntry struct {
	SequenceFilename string `json:"sequenceFilename"`
	Cue              string `json:"cue"`
}

// ConfigSequenceTestPayload is the show.sequencetest configuration payload.
type ConfigSequenceTestPayload struct {
	Show    string                    `json:"show"`
	Entries []ConfigSequenceTestEntry `json:"entries"`
}

// SequenceTestConfigResponse is the body of GET and PUT
// /config/show.sequencetest/{id}.
type SequenceTestConfigResponse struct {
	ServerTime             string                    `json:"serverTime"`
	Kind                   string                    `json:"kind"`
	ID                     string                    `json:"id"`
	Revision               int64                     `json:"revision"`
	Payload                ConfigSequenceTestPayload `json:"payload"`
	Running                bool                      `json:"running"`
	NotRunningReason       string                    `json:"notRunningReason,omitempty"`
	UpdatedAt              string                    `json:"updatedAt"`
	CreatedByPrincipalID   *string                   `json:"createdByPrincipalId"`
	CreatedByPrincipalName *string                   `json:"createdByPrincipalName"`
	Source                 string                    `json:"source"`
}
