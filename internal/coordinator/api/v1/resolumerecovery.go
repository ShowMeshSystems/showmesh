package v1

// This file is Track D seam D-3a's wire contract (TRACK-D-D3A-BUILD-CONTRACT.md
// §1.3-§1.4): the recovery record, the restore report, and the
// auto-restore toggle. Every object reference is a NAME (ADR-037 seam B)
// — no Resolume object id ever appears here, matching resolumeaction.go's
// own rule for what an operator types, extended here to what this seam
// ever RENDERS.

// ResolumeRecoveryRecordEntry is one layer's row in
// [ResolumeRecoveryResponse.Record] — build contract §1.3. Clip,
// ClipNameGenerated, and Deck are omitted (empty string, "" — this API's
// standing convention for "not applicable" on a JSON string field) when
// State is not "clip". Reason is always non-empty when State is
// "unknown".
type ResolumeRecoveryRecordEntry struct {
	Layer              string `json:"layer"`
	LayerNameGenerated bool   `json:"layerNameGenerated"`
	State              string `json:"state"`
	Clip               string `json:"clip,omitempty"`
	ClipNameGenerated  bool   `json:"clipNameGenerated,omitempty"`
	Deck               string `json:"deck,omitempty"`
	EstablishedAt      string `json:"establishedAt,omitempty"`
	Source             string `json:"source,omitempty"`
	Reason             string `json:"reason,omitempty"`
}

// ResolumeRecoveryRestoreLayer is one layer's row in
// [ResolumeRecoveryRestoreReport.Layers].
type ResolumeRecoveryRestoreLayer struct {
	Layer              string `json:"layer"`
	LayerNameGenerated bool   `json:"layerNameGenerated"`
	Result             string `json:"result"`
	Reason             string `json:"reason,omitempty"`
	Clip               string `json:"clip,omitempty"`
	ActionOutcome      string `json:"actionOutcome,omitempty"`
}

// ResolumeRecoveryRestoreReport is one restore's whole outcome — carried
// as [ResolumeRecoveryResponse.LastRestore] and returned directly by
// POST /resolume/recovery/restore.
type ResolumeRecoveryRestoreReport struct {
	StartedAt  string                         `json:"startedAt"`
	FinishedAt string                         `json:"finishedAt"`
	Trigger    string                         `json:"trigger"`
	Outcome    string                         `json:"outcome"`
	Principal  string                         `json:"principal"`
	Layers     []ResolumeRecoveryRestoreLayer `json:"layers"`
}

// ResolumeRecoveryResponse is the body of GET /api/v1/resolume/recovery —
// the open read (build contract §1.3): the toggle, the recovery record,
// and the last restore report. LastRestore is null until a restore has
// run.
type ResolumeRecoveryResponse struct {
	ServerTime            string                         `json:"serverTime"`
	AutoRestoreEnabled    bool                           `json:"autoRestoreEnabled"`
	AutoRestoreConfigured bool                           `json:"autoRestoreConfigured"`
	SettleDelaySeconds    float64                        `json:"settleDelaySeconds"`
	Record                []ResolumeRecoveryRecordEntry  `json:"record"`
	LastRestore           *ResolumeRecoveryRestoreReport `json:"lastRestore"`
}

// ResolumeRecoveryRestoreResponse is the body of a successful POST
// /api/v1/resolume/recovery/restore.
type ResolumeRecoveryRestoreResponse struct {
	ServerTime string                        `json:"serverTime"`
	Restore    ResolumeRecoveryRestoreReport `json:"restore"`
}

// ResolumeRecoveryChangedEvent is the payload of a
// "resolumeRecovery.changed" SSE event (build contract §1.7): the
// resource's own rendered wire representation, minus ServerTime (stamped
// separately, alongside Seq, at broadcast time — every other change-stream
// resource in this package follows the identical split). No delta event
// kind exists for this resource, matching resolume.changed's own posture.
type ResolumeRecoveryChangedEvent struct {
	Seq                   uint64                         `json:"seq"`
	ServerTime            string                         `json:"serverTime"`
	AutoRestoreEnabled    bool                           `json:"autoRestoreEnabled"`
	AutoRestoreConfigured bool                           `json:"autoRestoreConfigured"`
	SettleDelaySeconds    float64                        `json:"settleDelaySeconds"`
	Record                []ResolumeRecoveryRecordEntry  `json:"record"`
	LastRestore           *ResolumeRecoveryRestoreReport `json:"lastRestore"`
}

// ConfigResolumeRecoveryPayload is the "resolume.recovery" configuration
// kind's decoded payload (build contract §1.1): the body PUT
// /config/resolume.recovery accepts, and the "payload" member of GET
// /config/resolume.recovery's response.
type ConfigResolumeRecoveryPayload struct {
	AutoRestoreEnabled bool `json:"autoRestoreEnabled"`
}

// ResolumeRecoveryConfigResponse is the body of GET and PUT
// /config/resolume.recovery.
type ResolumeRecoveryConfigResponse struct {
	ServerTime             string                        `json:"serverTime"`
	Kind                   string                        `json:"kind"`
	Revision               int64                         `json:"revision"`
	Payload                ConfigResolumeRecoveryPayload `json:"payload"`
	UpdatedAt              string                        `json:"updatedAt"`
	CreatedByPrincipalID   *string                       `json:"createdByPrincipalId"`
	CreatedByPrincipalName *string                       `json:"createdByPrincipalName"`
	Source                 string                        `json:"source"`
}
