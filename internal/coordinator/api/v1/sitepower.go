package v1

// ConfigShutdownStep is one step of a shutdown sequence: an action, or a
// wait when action is empty (ADR-065).
type ConfigShutdownStep struct {
	Action              string `json:"action,omitempty"`
	RequireConfirmation bool   `json:"requireConfirmation,omitempty"`
	PowerDomain         string `json:"powerDomain,omitempty"`
	DomainProvenance    string `json:"domainProvenance,omitempty"`
	DelayMs             int    `json:"delayMs,omitempty"`
}

type ConfigShutdownSequence struct {
	ID    string               `json:"id"`
	Label string               `json:"label"`
	Steps []ConfigShutdownStep `json:"steps"`
}

// ConfigSitePowerPayload is the show.sitepower configuration payload.
type ConfigSitePowerPayload struct {
	ShutdownSequences []ConfigShutdownSequence `json:"shutdownSequences"`
	PowerOnActions    []string                 `json:"powerOnActions"`
}

// SitePowerConfigResponse is the body of GET and PUT /config/show.sitepower.
type SitePowerConfigResponse struct {
	ServerTime             string                 `json:"serverTime"`
	Kind                   string                 `json:"kind"`
	Revision               int64                  `json:"revision"`
	Payload                ConfigSitePowerPayload `json:"payload"`
	UpdatedAt              string                 `json:"updatedAt"`
	CreatedByPrincipalID   *string                `json:"createdByPrincipalId"`
	CreatedByPrincipalName *string                `json:"createdByPrincipalName"`
	Source                 string                 `json:"source"`
}

// SitePowerRunResponse is the 202 body of POST /site-power/on and /off.
// The run continues after the response; its result goes to the audit log.
type SitePowerRunResponse struct {
	ServerTime string   `json:"serverTime"`
	RunID      string   `json:"runId"`
	Started    []string `json:"started"`
	Message    string   `json:"message"`
}

// ShutdownStepResult is one step's outcome in a sequence run.
type ShutdownStepResult struct {
	Step          int    `json:"step"`
	Action        string `json:"action,omitempty"`
	DelayMs       int    `json:"delayMs,omitempty"`
	Outcome       string `json:"outcome"`
	OutcomeReason string `json:"outcomeReason,omitempty"`
}
