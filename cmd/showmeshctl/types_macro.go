package main

import (
	"encoding/json"
	"time"
)

// This file is Step 9 wave 3's own transcription of
// internal/coordinator/api/v1/showmacros.go into showmeshctl's independent
// wire-decoding layer, matching types.go's own standing rule (see that
// file's doc comment and doc.go/importgraph_test.go): nothing here is that
// package's ConfigObjectSummary/ShowActionConfigResponse/ShowMacroConfigResponse/
// MacroRun*/CreateMacroRunRequest reused directly, even where the shape is
// identical field-for-field, so a server-side JSON tag rename cannot
// silently rename both sides of this program's own decode at once.
//
// STEP-9-SPEC.md section 5.5's four-routes-per-kind shape and section 6.6's
// run surface are this file's own source; see that document for what each
// field means. macroRunStepCommand.Detail reuses [fppCommandResult]
// (types.go) rather than a fourth transcription of the identical
// FPPCommandResult shape — the two are the same wire object read from two
// different endpoints (a fresh dispatch response vs. a historical read
// through a macro run's step), and this program already has one
// independent copy of it.

// showConfigObjectSummary mirrors v1.ConfigObjectSummary: one element of a
// show.action or show.macro list, without its full payload (STEP-9-SPEC.md
// section 5.5: "Returns object ids with label, show, and current revision
// number. Not the full payloads.").
type showConfigObjectSummary struct {
	ID              string    `json:"id"`
	Label           string    `json:"label"`
	Show            string    `json:"show"`
	CurrentRevision int64     `json:"currentRevision"`
	UpdatedAt       time.Time `json:"updatedAt"`
}

// showConfigObjectsListResponse is the body of GET /config/show.action and
// GET /config/show.macro.
type showConfigObjectsListResponse struct {
	ServerTime time.Time                 `json:"serverTime"`
	Kind       string                    `json:"kind"`
	Objects    []showConfigObjectSummary `json:"objects"`
}

// showActionMQTTPublish is show.action.target.publish (STEP-9-SPEC.md
// section 5.3), present only when target.integration is "mqtt".
type showActionMQTTPublish struct {
	Topic   string `json:"topic"`
	Payload string `json:"payload"`
	QoS     int    `json:"qos"`
	Retain  bool   `json:"retain"`
}

// showActionMQTTExpect is show.action.target.expect (STEP-9-SPEC.md
// section 7.3). Topic/Value/DeadlineSeconds are omitted on the wire (not
// merely zero) for kind "none", which declares no expected response at
// all — see the "omitempty" tags, matching v1.ConfigShowActionMQTTExpect
// exactly.
type showActionMQTTExpect struct {
	Kind            string  `json:"kind"`
	Topic           string  `json:"topic,omitempty"`
	Value           *string `json:"value,omitempty"`
	DeadlineSeconds int     `json:"deadlineSeconds,omitempty"`
}

// showActionTarget is show.action.target: Integration plus either the
// fpp, mqtt, or resolume fields directly, never nested a second level
// under an "fpp"/"mqtt"/"resolume" key — matching the server's own
// flattened wire shape exactly.
type showActionTarget struct {
	Integration string `json:"integration"`

	// fpp-only.
	InstanceID string         `json:"instanceId,omitempty"`
	Primitive  string         `json:"primitive,omitempty"`
	Params     map[string]any `json:"params,omitempty"`

	// mqtt-only.
	Broker  string                 `json:"broker,omitempty"`
	Publish *showActionMQTTPublish `json:"publish,omitempty"`
	Expect  *showActionMQTTExpect  `json:"expect,omitempty"`

	// resolume-only. Action is one of the seven Track D D-3 action names;
	// Ref carries ADR-037's named-reference vocabulary (clip, deck, layer,
	// column, persistent, bypassed, master) — never a Resolume object id.
	Action string         `json:"action,omitempty"`
	Ref    map[string]any `json:"ref,omitempty"`

	// audio-only. AudioAction is one of the reserved audio.session.*/
	// audio.gain.*/audio.output.* operation names; Params (above) is
	// that operation's own command params. AudioNodeIDs is the
	// target audio node(s): the server accepts and returns either a bare
	// string (one node) or an array of strings (more than one, for a
	// night-mode bed or announcement) under this same "audioNodeId" key.
	AudioNodeIDs   audioNodeIDList `json:"audioNodeId,omitempty"`
	AudioSessionID string          `json:"audioSessionId,omitempty"`
	AudioAction    string          `json:"audioAction,omitempty"`
}

// audioNodeIDList is showActionTarget.AudioNodeIDs' own wire type: the
// server's own scalar-or-array flexible decode
// (internal/coordinator/config.AudioNodeIDList), independently
// transcribed here per this package's own "never reuse the server's
// types directly" rule.
type audioNodeIDList []string

func (l *audioNodeIDList) UnmarshalJSON(b []byte) error {
	if string(b) == "null" {
		*l = nil
		return nil
	}
	var single string
	if err := json.Unmarshal(b, &single); err == nil {
		*l = audioNodeIDList{single}
		return nil
	}
	var list []string
	if err := json.Unmarshal(b, &list); err != nil {
		return err
	}
	*l = audioNodeIDList(list)
	return nil
}

// showAction is the "show.action" configuration kind's decoded payload
// (STEP-9-SPEC.md section 5.3): the "payload" member of GET
// /config/show.action/{id}'s response. Idempotent is TRI-STATE -- true,
// false, or nil for "never declared" -- matching api/openapi.yaml's
// ConfigShowAction.idempotent doc comment exactly: nil is a real, distinct
// state from a declared false, not this program's zero value standing in
// for an absent server claim. A plain bool would silently turn "not
// declared" into "declared false" on decode.
type showAction struct {
	Show        string           `json:"show"`
	Label       string           `json:"label"`
	Description string           `json:"description"`
	SafetyClass string           `json:"safetyClass"`
	Target      showActionTarget `json:"target"`
	Idempotent  *bool            `json:"idempotent"`
}

// showActionConfigResponse is the body of GET /config/show.action/{id}.
// CreatedByPrincipalID/CreatedByPrincipalName stay nullable — see
// [fppEndpointsConfigResponse]'s own doc comment in types.go for why this
// program never assumes a config revision's creator is non-nil.
type showActionConfigResponse struct {
	ServerTime             time.Time  `json:"serverTime"`
	Kind                   string     `json:"kind"`
	ID                     string     `json:"id"`
	Revision               int64      `json:"revision"`
	Payload                showAction `json:"payload"`
	UpdatedAt              time.Time  `json:"updatedAt"`
	CreatedByPrincipalID   *string    `json:"createdByPrincipalId"`
	CreatedByPrincipalName *string    `json:"createdByPrincipalName"`
	Source                 string     `json:"source"`
}

// showMacroLocalFallback is one step's required localFallback object
// (STEP-9-SPEC.md section 5.4, ADR-004, ADR-016).
type showMacroLocalFallback struct {
	Class  string `json:"class"`
	Reason string `json:"reason"`
}

// showMacroStep is one element of show.macro.steps. OnFailure/OnUnconfirmed
// are always the RESOLVED value on a read (default or explicit), never
// blank standing in for "the default applies" (STEP-9-SPEC.md section
// 5.4's own decoder rule, carried onto the wire by the server).
type showMacroStep struct {
	ID            string                 `json:"id"`
	Action        string                 `json:"action"`
	OnFailure     string                 `json:"onFailure"`
	OnUnconfirmed string                 `json:"onUnconfirmed"`
	LocalFallback showMacroLocalFallback `json:"localFallback"`
}

// showMacro is the "show.macro" configuration kind's decoded payload
// (STEP-9-SPEC.md section 5.4).
type showMacro struct {
	Show        string          `json:"show"`
	Label       string          `json:"label"`
	Description string          `json:"description"`
	Steps       []showMacroStep `json:"steps"`
}

// showMacroConfigResponse is the body of GET /config/show.macro/{id}. See
// [showActionConfigResponse]'s doc comment for the nullable-creator fields.
type showMacroConfigResponse struct {
	ServerTime             time.Time `json:"serverTime"`
	Kind                   string    `json:"kind"`
	ID                     string    `json:"id"`
	Revision               int64     `json:"revision"`
	Payload                showMacro `json:"payload"`
	UpdatedAt              time.Time `json:"updatedAt"`
	CreatedByPrincipalID   *string   `json:"createdByPrincipalId"`
	CreatedByPrincipalName *string   `json:"createdByPrincipalName"`
	Source                 string    `json:"source"`
}

// macroRunSummary mirrors v1.MacroRunSummary: one element of GET
// /macro-runs, without step detail. Completed/Confirmed are nil while the
// run is still "running" (ADR-031 decision 3) — never defaulted to false,
// which would read as a definite, premature verdict on an in-flight run.
type macroRunSummary struct {
	ID                  string     `json:"id"`
	MacroObjectID       string     `json:"macroObjectId"`
	MacroRevision       int64      `json:"macroRevision"`
	Show                string     `json:"show"`
	Trigger             string     `json:"trigger"`
	IssuerPrincipalID   string     `json:"issuerPrincipalId"`
	IssuerPrincipalName string     `json:"issuerPrincipalName"`
	CreatedAt           time.Time  `json:"createdAt"`
	FinishedAt          *time.Time `json:"finishedAt"`
	State               string     `json:"state"`
	Completed           *bool      `json:"completed"`
	Confirmed           *bool      `json:"confirmed"`
	Reason              string     `json:"reason"`
	AttributionDegraded bool       `json:"attributionDegraded"`
}

// macroRunStepCommand states the FPP command a step dispatched, or
// explicitly why no command detail is available (STEP-9-SPEC.md section
// 6.1/6.6) — never omitted, per this API's "absent evidence is stated,
// never omitted" rule. State is one of "none", "retained", "not_retained";
// see v1.MacroRunStepCommand's own doc comment for what each means. Detail
// reuses [fppCommandResult] (types.go) — see this file's own doc comment.
type macroRunStepCommand struct {
	State  string            `json:"state"`
	ID     *string           `json:"id,omitempty"`
	Reason string            `json:"reason,omitempty"`
	Detail *fppCommandResult `json:"detail,omitempty"`
}

// macroRunStep is one element of macroRun.steps.
type macroRunStep struct {
	StepIndex           int                 `json:"stepIndex"`
	StepID              string              `json:"stepId"`
	ActionObjectID      string              `json:"actionObjectId"`
	ActionRevision      int64               `json:"actionRevision"`
	Integration         string              `json:"integration"`
	SafetyClass         string              `json:"safetyClass"`
	LocalFallbackClass  string              `json:"localFallbackClass"`
	State               string              `json:"state"`
	DispatchedAt        *time.Time          `json:"dispatchedAt"`
	ResolvedAt          *time.Time          `json:"resolvedAt"`
	Outcome             string              `json:"outcome"`
	OutcomeState        string              `json:"outcomeState"`
	OutcomeReason       string              `json:"outcomeReason"`
	AttributionDegraded bool                `json:"attributionDegraded"`
	Command             macroRunStepCommand `json:"command"`
}

// macroRun is the body of GET /macro-runs/{runId}'s "run" member, and of
// POST /macros/{id}/runs' 202 response's "run" member: [macroRunSummary]'s
// fields plus its steps.
type macroRun struct {
	ID                  string         `json:"id"`
	MacroObjectID       string         `json:"macroObjectId"`
	MacroRevision       int64          `json:"macroRevision"`
	Show                string         `json:"show"`
	Trigger             string         `json:"trigger"`
	IssuerPrincipalID   string         `json:"issuerPrincipalId"`
	IssuerPrincipalName string         `json:"issuerPrincipalName"`
	CreatedAt           time.Time      `json:"createdAt"`
	FinishedAt          *time.Time     `json:"finishedAt"`
	State               string         `json:"state"`
	Completed           *bool          `json:"completed"`
	Confirmed           *bool          `json:"confirmed"`
	Reason              string         `json:"reason"`
	AttributionDegraded bool           `json:"attributionDegraded"`
	Steps               []macroRunStep `json:"steps"`
}

// macroRunResponse is the body of GET /macro-runs/{runId}.
type macroRunResponse struct {
	ServerTime time.Time `json:"serverTime"`
	Run        macroRun  `json:"run"`
}

// macroRunSubmitResponse is the success body of POST /macros/{id}/runs
// (status 202 — ADR-031 decision 1: "the run is accepted and not
// complete"). Replay is true when the submission's idempotency key named
// an already-existing run: nothing new was created by this call.
type macroRunSubmitResponse struct {
	ServerTime time.Time `json:"serverTime"`
	Run        macroRun  `json:"run"`
	Replay     bool      `json:"replay"`
}

// macroRunsListResponse is the body of GET /macro-runs.
type macroRunsListResponse struct {
	ServerTime time.Time         `json:"serverTime"`
	Runs       []macroRunSummary `json:"runs"`
}

// createMacroRunRequest is the body of POST /macros/{id}/runs. This
// program never sends priorFailures/priorFailuresDropped (STEP-9-SPEC.md
// section 8.3 path 2): that buffer belongs to the FPP plugin, which
// experiences the ADR-024 decision 7 refusal this CLI never does when it
// runs a macro directly against a coordinator it can already reach. Those
// two fields are simply absent from this struct rather than present-and-
// zero, which encodes identically to an omitted key on the wire.
type createMacroRunRequest struct {
	IdempotencyKey string `json:"idempotencyKey"`
	// Trigger is "cli" for every submission this program makes — see
	// v1.CreateMacroRunRequest's own doc comment for the full
	// "api"|"plugin"|"cli"|"ui" vocabulary this value is validated against
	// at the route.
	Trigger string `json:"trigger"`
}

// actionBinding mirrors v1.ActionBinding: state is "ok", "broken", or
// "unknown", reason always non-empty.
type actionBinding struct {
	ActionID string `json:"actionId"`
	Label    string `json:"label"`
	Show     string `json:"show"`
	State    string `json:"state"`
	Reason   string `json:"reason"`
}

// actionBindingResponse is the body of GET /actions/{id}/binding.
type actionBindingResponse struct {
	ServerTime time.Time     `json:"serverTime"`
	Binding    actionBinding `json:"binding"`
}

// actionBindingsResponse is the body of GET /actions/bindings.
type actionBindingsResponse struct {
	ServerTime time.Time       `json:"serverTime"`
	Bindings   []actionBinding `json:"bindings"`
}

// actionInvocationRequest is the body of POST /actions/{id}/invocations.
// RequestedRevision is a pointer so an unset flag omits the key entirely
// (an interactive "invoke whatever is current" call) rather than sending
// a meaningless zero.
type actionInvocationRequest struct {
	IdempotencyKey    string `json:"idempotencyKey"`
	RequestedRevision *int64 `json:"requestedRevision,omitempty"`
}

// actionInvocationResult mirrors v1.ActionInvocationResult field for
// field. Outcome is a pointer: nil while State is "pending" — never a
// blank string pretending to be one of the five terminal words.
type actionInvocationResult struct {
	ID                        string     `json:"id"`
	IdempotencyKey            string     `json:"idempotencyKey"`
	ActionID                  string     `json:"actionId"`
	Revision                  int64      `json:"revision"`
	Label                     string     `json:"label"`
	Replay                    bool       `json:"replay"`
	State                     string     `json:"state"`
	Outcome                   *string    `json:"outcome"`
	OutcomeReason             string     `json:"outcomeReason"`
	DispatchAttribution       string     `json:"dispatchAttribution"`
	DispatchAttributionReason string     `json:"dispatchAttributionReason"`
	OutcomeAttribution        string     `json:"outcomeAttribution"`
	OutcomeAttributionReason  string     `json:"outcomeAttributionReason"`
	AttributionDegraded       bool       `json:"attributionDegraded"`
	DispatchedAt              *time.Time `json:"dispatchedAt"`
	ResolvedAt                *time.Time `json:"resolvedAt"`
}

// actionInvocationResponse is the body of a successful POST
// /actions/{id}/invocations.
type actionInvocationResponse struct {
	ServerTime time.Time              `json:"serverTime"`
	Result     actionInvocationResult `json:"result"`
}
