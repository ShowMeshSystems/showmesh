package mqttproto

import (
	"encoding/json"
	"fmt"
	"time"
)

// SchemaNodeFallbackV1 is the schema of a node's fallback report,
// published retained on its observed "fallback" subpath (ADR-048
// decision 3: the ingress "reports every refusal").
const SchemaNodeFallbackV1 = "showmesh.node.fallback/v1"

// ObservedSubpathFallback is the observed subpath the report is published on.
const ObservedSubpathFallback = "fallback"

// FallbackHeldProgram is one FPP player's fallback program as a node holds it.
type FallbackHeldProgram struct {
	FPPInstanceUUID  string    `json:"fppInstanceUuid"`
	PackageID        string    `json:"packageId"`
	Revision         string    `json:"revision"`
	ExpiresAt        time.Time `json:"expiresAt"`
	InstalledAt      time.Time `json:"installedAt"`
	ExecutorEnrolled bool      `json:"executorEnrolled"`
}

// FallbackDecision is one answer the node gave on its fallback routes.
// Count is above 1 only for a run of rate-limited refusals.
type FallbackDecision struct {
	At              time.Time `json:"at"`
	Route           string    `json:"route"`
	Accepted        bool      `json:"accepted"`
	Outcome         string    `json:"outcome"`
	Reason          string    `json:"reason"`
	FPPInstanceUUID string    `json:"fppInstanceUuid,omitempty"`
	ExecutionID     string    `json:"executionId,omitempty"`
	EntryKey        string    `json:"entryKey,omitempty"`
	CueID           string    `json:"cueId,omitempty"`
	Count           int       `json:"count"`
}

// FallbackPayload is the showmesh.node.fallback/v1 payload. Accepted and
// Refused count every answer since the agent started; Decisions is the
// most recent of them, oldest first, and is bounded by the node.
type FallbackPayload struct {
	ObservedAt           *time.Time `json:"observedAt"`
	CoordinatorKeyLoaded bool       `json:"coordinatorKeyLoaded"`
	// ExecutionRecordProblem says why the node cannot record handled
	// requests and so refuses every activation. Empty when it can.
	ExecutionRecordProblem string                `json:"executionRecordProblem,omitempty"`
	Programs               []FallbackHeldProgram `json:"programs"`
	Accepted               int64                 `json:"accepted"`
	Refused                int64                 `json:"refused"`
	Decisions              []FallbackDecision    `json:"decisions"`
}

// Validate reports whether p carries its one required field.
func (p FallbackPayload) Validate() error {
	if p.ObservedAt == nil {
		return fmt.Errorf("%w: observedAt", ErrPayloadMissingField)
	}
	return nil
}

// DecodeFallbackPayload decodes env.Payload as a [FallbackPayload].
func DecodeFallbackPayload(env Envelope) (FallbackPayload, error) {
	if env.Schema != SchemaNodeFallbackV1 {
		return FallbackPayload{}, &UnsupportedSchemaError{Got: env.Schema, Want: SchemaNodeFallbackV1}
	}
	if err := checkPayloadPresent(env.Payload); err != nil {
		return FallbackPayload{}, fmt.Errorf("mqttproto: decode fallback payload: %w", err)
	}
	var p FallbackPayload
	if err := json.Unmarshal(env.Payload, &p); err != nil {
		return FallbackPayload{}, fmt.Errorf("mqttproto: decode fallback payload: %w", err)
	}
	if err := p.Validate(); err != nil {
		return FallbackPayload{}, fmt.Errorf("mqttproto: decode fallback payload: %w", err)
	}
	return p, nil
}

// NewFallbackEnvelope builds the envelope a node publishes payload in.
func NewFallbackEnvelope(now func() time.Time, nodeID string, payload FallbackPayload) (Envelope, error) {
	if err := payload.Validate(); err != nil {
		return Envelope{}, fmt.Errorf("mqttproto: build fallback envelope: %w", err)
	}
	return newEnvelope(now, SchemaNodeFallbackV1, nodeID, payload)
}
