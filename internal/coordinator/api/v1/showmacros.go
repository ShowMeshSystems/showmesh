package v1

// This file is Step 9 wave 2 Builder C's own wire-type addition: the two
// new configuration kinds (show.action, show.macro — STEP-9-SPEC.md
// section 5) and the macro run surface (STEP-9-SPEC.md section 6.6). It
// follows types.go's own standing rule (ADR-020 consequences: "the wire
// types are a separate layer from the domain types and are mapped
// between") — nothing here is internal/coordinator/config's
// ShowActionPayload/ShowMacroPayload or internal/coordinator/store's
// MacroRunRecord/MacroRunStepRecord reused directly, even where the shape
// is identical field-for-field, so a refactor of either package cannot
// silently rename a public field on the wire.

// ConfigObjectSummary is one element of [ConfigObjectsListResponse]:
// enough to enumerate and label every show.action or show.macro object
// without fetching each one's full payload (STEP-9-SPEC.md section 5.5:
// "Returns object ids with label, show, and current revision number. Not
// the full payloads.").
type ConfigObjectSummary struct {
	ID              string `json:"id"`
	Label           string `json:"label"`
	Show            string `json:"show"`
	CurrentRevision int64  `json:"currentRevision"`
	UpdatedAt       string `json:"updatedAt"`
}

// ConfigObjectsListResponse is the body of GET /config/show.action and GET
// /config/show.macro.
type ConfigObjectsListResponse struct {
	ServerTime string                `json:"serverTime"`
	Kind       string                `json:"kind"`
	Objects    []ConfigObjectSummary `json:"objects"`
}

// ConfigShowActionMQTTPublish is show.action.target.publish
// (STEP-9-SPEC.md section 5.3), present only when target.integration is
// "mqtt".
type ConfigShowActionMQTTPublish struct {
	Topic string `json:"topic"`
	// Payload is a real MQTT payload; an explicit empty string is a valid,
	// meaningful value (an empty publish is ordinary MQTT usage), so it is
	// never omitted the way a genuinely absent field would be.
	Payload string `json:"payload"`
	QoS     int    `json:"qos"`
	// Retain is the one field on this payload besides show.macro's
	// onFailure/onUnconfirmed where an absent key on the write side carries
	// a default (false) rather than being an error — see
	// internal/coordinator/config's ShowActionMQTTPublish.Retain doc
	// comment. On the READ side (this type) it is always present: the
	// stored revision already carries the resolved value, never "absent".
	Retain bool `json:"retain"`
}

// ConfigShowActionMQTTExpect is show.action.target.expect (STEP-9-SPEC.md
// section 7.3), present only when target.integration is "mqtt". Topic,
// Value, and DeadlineSeconds are omitted on the wire (not merely
// zero-valued) for kind "none", which declares no expected response at
// all and carries none of the three.
type ConfigShowActionMQTTExpect struct {
	Kind            string  `json:"kind"`
	Topic           string  `json:"topic,omitempty"`
	Value           *string `json:"value,omitempty"`
	DeadlineSeconds int     `json:"deadlineSeconds,omitempty"`
}

// ConfigShowActionTarget is show.action.target (STEP-9-SPEC.md section
// 5.3), flattened exactly as the specification's own wire examples show it:
// Integration plus either the fpp fields or the mqtt fields directly,
// never nested a second level under an "fpp"/"mqtt" key.
type ConfigShowActionTarget struct {
	Integration string `json:"integration"`

	// fpp-only.
	InstanceID string         `json:"instanceId,omitempty"`
	Primitive  string         `json:"primitive,omitempty"`
	Params     map[string]any `json:"params,omitempty"`

	// mqtt-only.
	Broker  string                       `json:"broker,omitempty"`
	Publish *ConfigShowActionMQTTPublish `json:"publish,omitempty"`
	Expect  *ConfigShowActionMQTTExpect  `json:"expect,omitempty"`
}

// ConfigShowAction is the "show.action" configuration kind's decoded
// payload (STEP-9-SPEC.md section 5.3): the body PUT /config/show.action/{id}
// accepts, and the "payload" member of GET /config/show.action/{id}'s
// response.
type ConfigShowAction struct {
	Show        string                 `json:"show"`
	Label       string                 `json:"label"`
	Description string                 `json:"description"`
	SafetyClass string                 `json:"safetyClass"`
	Target      ConfigShowActionTarget `json:"target"`
}

// ShowActionConfigResponse is the body of GET and PUT
// /config/show.action/{id}. CreatedByPrincipalID/CreatedByPrincipalName
// are null only for a revision this coordinator itself created with no
// authenticated principal attached — in practice this never happens for
// show.action (unlike fpp.endpoints, there is no startup env migration for
// this kind), but the fields stay nullable rather than assumed non-null so
// a future non-API write path is not a silent contract change.
type ShowActionConfigResponse struct {
	ServerTime             string           `json:"serverTime"`
	Kind                   string           `json:"kind"`
	ID                     string           `json:"id"`
	Revision               int64            `json:"revision"`
	Payload                ConfigShowAction `json:"payload"`
	UpdatedAt              string           `json:"updatedAt"`
	CreatedByPrincipalID   *string          `json:"createdByPrincipalId"`
	CreatedByPrincipalName *string          `json:"createdByPrincipalName"`
	Source                 string           `json:"source"`
}

// ConfigShowMacroLocalFallback is one step's required localFallback object
// (STEP-9-SPEC.md section 5.4, ADR-004, ADR-016).
type ConfigShowMacroLocalFallback struct {
	Class  string `json:"class"`
	Reason string `json:"reason"`
}

// ConfigShowMacroStep is one element of show.macro.steps. OnFailure and
// OnUnconfirmed are always the RESOLVED value (default or explicit), never
// blank standing in for "the default applies" — a stored revision states
// its own policy outright (STEP-9-SPEC.md section 5.4's own decoder rule,
// carried onto the wire).
type ConfigShowMacroStep struct {
	ID            string                       `json:"id"`
	Action        string                       `json:"action"`
	OnFailure     string                       `json:"onFailure"`
	OnUnconfirmed string                       `json:"onUnconfirmed"`
	LocalFallback ConfigShowMacroLocalFallback `json:"localFallback"`
}

// ConfigShowMacro is the "show.macro" configuration kind's decoded payload
// (STEP-9-SPEC.md section 5.4).
type ConfigShowMacro struct {
	Show        string                `json:"show"`
	Label       string                `json:"label"`
	Description string                `json:"description"`
	Steps       []ConfigShowMacroStep `json:"steps"`
}

// ShowMacroConfigResponse is the body of GET and PUT
// /config/show.macro/{id}. See [ShowActionConfigResponse]'s doc comment
// for why CreatedByPrincipalID/CreatedByPrincipalName stay nullable.
type ShowMacroConfigResponse struct {
	ServerTime             string          `json:"serverTime"`
	Kind                   string          `json:"kind"`
	ID                     string          `json:"id"`
	Revision               int64           `json:"revision"`
	Payload                ConfigShowMacro `json:"payload"`
	UpdatedAt              string          `json:"updatedAt"`
	CreatedByPrincipalID   *string         `json:"createdByPrincipalId"`
	CreatedByPrincipalName *string         `json:"createdByPrincipalName"`
	Source                 string          `json:"source"`
}

// MacroRunSummary is one element of [MacroRunsListResponse.Runs] and of
// [Snapshot.MacroRuns]: a run's own state without its steps (STEP-9-SPEC.md
// section 6.6: "Steps are not included: a list of runs is a list of runs,
// and a client wanting step detail fetches the run.").
//
// Completed and Confirmed are null while the run is still "running"
// (ADR-031 decision 3: "before a run finishes, neither is known") — never
// defaulted to false, which would read as a definite, premature verdict on
// an in-flight run.
type MacroRunSummary struct {
	ID                  string  `json:"id"`
	MacroObjectID       string  `json:"macroObjectId"`
	MacroRevision       int64   `json:"macroRevision"`
	Show                string  `json:"show"`
	Trigger             string  `json:"trigger"`
	IssuerPrincipalID   string  `json:"issuerPrincipalId"`
	IssuerPrincipalName string  `json:"issuerPrincipalName"`
	CreatedAt           string  `json:"createdAt"`
	FinishedAt          *string `json:"finishedAt"`
	State               string  `json:"state"`
	Completed           *bool   `json:"completed"`
	Confirmed           *bool   `json:"confirmed"`
	Reason              string  `json:"reason"`
	AttributionDegraded bool    `json:"attributionDegraded"`
}

// MacroRunStepCommand states the FPP command a step dispatched, or
// explicitly why no command detail is available — never omitted, per this
// API's standing "absent evidence is stated, never omitted" rule
// (ADR-020). State is one of:
//
//   - "none" — this step never dispatched a command at all: an MQTT step
//     (which has no commands row by construction), or an FPP step that has
//     not dispatched yet.
//   - "retained" — the step dispatched command ID, and that command's own
//     row is still present in the (retention-bounded) commands table; Detail
//     carries it.
//   - "not_retained" — the step dispatched command ID, but retention has
//     since pruned that row (STEP-9-SPEC.md section 6.1: "the commands.id
//     reference is dangling by design and must be read as one... the run
//     view renders the step's own recorded outcome with the command detail
//     marked not retained, with a reason. It never renders blank."). The
//     step's own Outcome/OutcomeState/OutcomeReason on [MacroRunStep] are
//     unaffected — they are this package's own record of what happened,
//     independent of whether the command journal still holds a matching row.
type MacroRunStepCommand struct {
	State  string            `json:"state"`
	ID     *string           `json:"id,omitempty"`
	Reason string            `json:"reason,omitempty"`
	Detail *FPPCommandResult `json:"detail,omitempty"`
}

// MacroRunStep is one element of [MacroRun.Steps].
type MacroRunStep struct {
	StepIndex           int                 `json:"stepIndex"`
	StepID              string              `json:"stepId"`
	ActionObjectID      string              `json:"actionObjectId"`
	ActionRevision      int64               `json:"actionRevision"`
	Integration         string              `json:"integration"`
	SafetyClass         string              `json:"safetyClass"`
	LocalFallbackClass  string              `json:"localFallbackClass"`
	State               string              `json:"state"`
	DispatchedAt        *string             `json:"dispatchedAt"`
	ResolvedAt          *string             `json:"resolvedAt"`
	Outcome             string              `json:"outcome"`
	OutcomeState        string              `json:"outcomeState"`
	OutcomeReason       string              `json:"outcomeReason"`
	AttributionDegraded bool                `json:"attributionDegraded"`
	Command             MacroRunStepCommand `json:"command"`
}

// MacroRun is the body of GET /macro-runs/{runId} and the "run" member of
// POST /macros/{id}/runs' 202 response: [MacroRunSummary]'s fields plus its
// steps.
type MacroRun struct {
	ID                  string         `json:"id"`
	MacroObjectID       string         `json:"macroObjectId"`
	MacroRevision       int64          `json:"macroRevision"`
	Show                string         `json:"show"`
	Trigger             string         `json:"trigger"`
	IssuerPrincipalID   string         `json:"issuerPrincipalId"`
	IssuerPrincipalName string         `json:"issuerPrincipalName"`
	CreatedAt           string         `json:"createdAt"`
	FinishedAt          *string        `json:"finishedAt"`
	State               string         `json:"state"`
	Completed           *bool          `json:"completed"`
	Confirmed           *bool          `json:"confirmed"`
	Reason              string         `json:"reason"`
	AttributionDegraded bool           `json:"attributionDegraded"`
	Steps               []MacroRunStep `json:"steps"`
}

// MacroRunResponse is the body of GET /macro-runs/{runId}.
type MacroRunResponse struct {
	ServerTime string   `json:"serverTime"`
	Run        MacroRun `json:"run"`
}

// MacroRunSubmitResponse is the success body of POST /macros/{id}/runs
// (status 202 — ADR-031 decision 1: "the run is accepted and not
// complete"). A distinct type from [MacroRunResponse], not the same shape
// with an always-false field on a plain GET: Replay only ever means
// something in answer to a submission.
type MacroRunSubmitResponse struct {
	ServerTime string   `json:"serverTime"`
	Run        MacroRun `json:"run"`
	// Replay is true when the submission's idempotency key named an
	// already-existing run: nothing new was created by this call, and Run
	// reports the original run's current state (STEP-9-SPEC.md section 6.2).
	Replay bool `json:"replay"`
}

// MacroRunsListResponse is the body of GET /macro-runs.
type MacroRunsListResponse struct {
	ServerTime string            `json:"serverTime"`
	Runs       []MacroRunSummary `json:"runs"`
}

// MacroPriorFailureRequest is one element of [CreateMacroRunRequest.PriorFailures]
// (STEP-9-SPEC.md section 8.3 path 2): a degraded outcome the caller (the
// FPP plugin) buffered locally and is reporting now that it has reached
// this coordinator successfully.
type MacroPriorFailureRequest struct {
	MacroObjectID string `json:"macroObjectId"`
	// Class is "refused" | "rejected" | "unreachable" (STEP-9-SPEC.md
	// section 8.2); validated at the route.
	Class string `json:"class"`
	// HTTPStatus is 0 when there was no response at all — the ordinary case
	// for "unreachable". Zero is a real, distinct value here, not an absent
	// one, so it has no omitempty.
	HTTPStatus int    `json:"httpStatus"`
	At         string `json:"at"`
}

// CreateMacroRunRequest is the body of POST /macros/{id}/runs.
type CreateMacroRunRequest struct {
	IdempotencyKey string `json:"idempotencyKey"`
	// Trigger is one of "api" | "plugin" | "cli" | "ui" (STEP-9-SPEC.md
	// section 6.1), validated at the route.
	Trigger string `json:"trigger"`
	// PriorFailures and PriorFailuresDropped are optional; an absent
	// PriorFailures means "nothing buffered" (an empty slice server-side),
	// not an error — unlike show.macro's steps, an ordinary run submission
	// from the UI or CLI has nothing to report here and should not have to
	// say so explicitly.
	PriorFailures        []MacroPriorFailureRequest `json:"priorFailures,omitempty"`
	PriorFailuresDropped int                        `json:"priorFailuresDropped,omitempty"`
}

// MacroRunChangedEvent is the payload of a "macroRun.changed" SSE event
// (STEP-9-SPEC.md section 6.6): one run's state transition. Step-level
// detail is deliberately NOT carried here — "a run with 32 steps must not
// put 32 events on a stream every client receives" — a client wanting step
// detail fetches GET /macro-runs/{runId}, exactly as [MacroRunSummary]
// omits Steps for the identical reason.
type MacroRunChangedEvent struct {
	Seq                 uint64 `json:"seq"`
	ServerTime          string `json:"serverTime"`
	RunID               string `json:"runId"`
	MacroObjectID       string `json:"macroObjectId"`
	State               string `json:"state"`
	Completed           *bool  `json:"completed"`
	Confirmed           *bool  `json:"confirmed"`
	Reason              string `json:"reason"`
	AttributionDegraded bool   `json:"attributionDegraded"`
}
