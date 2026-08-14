package mqttproto

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"time"

	"github.com/google/uuid"

	"github.com/showmeshsystems/showmesh/pkg/capability"
)

// Schema strings for the payloads this package defines. Step 2 shipped the
// first three (hello, health, lwt); cmd, result, and the one observed
// signal a command execution produces (agent echo) were added once
// pkg/command stopped being a stub — see this file's CmdPayload,
// ResultPayload, and AgentEchoPayload doc comments, and the package doc
// comment's note on why these are independent, JSON-tagged types rather
// than pkg/command's own Envelope reused directly.
const (
	SchemaNodeHelloV1  = "showmesh.node.hello/v1"
	SchemaNodeHealthV1 = "showmesh.node.health/v1"
	SchemaNodeLWTV1    = "showmesh.node.lwt/v1"

	SchemaNodeCmdV1       = "showmesh.node.cmd/v1"
	SchemaNodeResultV1    = "showmesh.node.result/v1"
	SchemaNodeAgentEchoV1 = "showmesh.node.agent.echo/v1"
)

// HelloPayload is the payload of the showmesh.node.hello/v1 schema,
// published retained on showmesh/nodes/<node-id>/hello: a node's capability
// advertisement.
//
// HelloPayload deliberately carries no NodeID and no send timestamp: the
// envelope is the sole carrier of both. A second copy of identity in the
// payload would give a topic-injection attempt (see [ValidateNodeID]'s doc
// comment) a second place to disagree with the topic, on top of the one
// [CheckNodeID] already reconciles; a second copy of the send time would
// invite exactly the "treat SentAt as freshness evidence" misuse
// [Envelope.SentAt] warns against. This also keeps HelloPayload,
// [HealthPayload], and [LWTPayload] consistent with each other: none of the
// three payloads carries identity or send time, only the envelope does.
type HelloPayload struct {
	// Label is a human-readable name for the node (e.g. "Media Node 03"),
	// distinct from the machine node ID carried by the envelope and used in
	// topics.
	Label string `json:"label"`

	// Platform is a short platform string in "os-arch" style, e.g.
	// "linux-amd64", matching ARCHITECTURE section 6's YAML example.
	Platform string `json:"platform"`

	// AgentVersion is the showmesh-agent build version reporting this
	// hello (see internal/version, mirrored in the coordinator's own
	// buildDate/version HTTP fields).
	AgentVersion string `json:"agentVersion"`

	// BootID is freshly generated once per agent process start (a UUID or
	// similar opaque token; this package does not constrain its format
	// beyond "non-empty string"), so a restart is distinguishable from a
	// continuous session even if the node ID and Label are unchanged.
	BootID string `json:"bootId"`

	// StartedAt is when this agent process started, on the agent's own
	// clock. Like Envelope.SentAt, this is sender-clock evidence, not
	// something the receiver should treat as globally synchronized.
	StartedAt time.Time `json:"startedAt"`

	// Capabilities is the node's advertised capability set. This package
	// does not call [capability.Set.Validate] on decode; a caller wiring
	// this up against real advertisements (Task C/D) decides whether and
	// when to validate and what to do with an invalid set. Validate does,
	// however, bound the set's size and each ID's length; see
	// [maxCapabilityCount].
	Capabilities capability.Set `json:"capabilities"`
}

// maxCapabilityCount and maxCapabilityIDLength bound a hello payload's
// capability set against a hostile or buggy publisher, independent of
// [capability.Set.Validate]'s semantic checks (duplicate IDs, ID syntax),
// which this package does not call on decode (see [HelloPayload.
// Capabilities]'s doc comment) and which in any case has no size or length
// bound of its own — [capability.ID]'s own syntax pattern does not cap
// length. Hello is retained (see [HelloDeliveryPolicy]), so an oversized or
// pathologically long-ID capability set is not a one-time cost: the broker
// replays it to every new subscriber, and the coordinator re-parses and
// re-allocates it on every restart and every reconnect.
//
// SHOWMESH HYPOTHESIS, NOT AN ADR-008 REQUIREMENT: like [maxSubpathLength]
// in topic.go, ADR-008 says nothing about how many capabilities a node may
// advertise or how long one ID may be. These are conservative, unmeasured
// guesses at "far more than any real node needs, far less than abuse would
// send"; widen them if a real deployment needs more.
const (
	maxCapabilityCount    = 256
	maxCapabilityIDLength = 128
)

// ErrPayloadCapabilitySetTooLarge is wrapped by [HelloPayload.Validate] when
// the capability set has more than [maxCapabilityCount] members.
var ErrPayloadCapabilitySetTooLarge = errors.New("mqttproto: capability set exceeds the maximum allowed size")

// ErrPayloadCapabilityIDTooLong is wrapped by [HelloPayload.Validate] when a
// capability ID exceeds [maxCapabilityIDLength].
var ErrPayloadCapabilityIDTooLong = errors.New("mqttproto: capability ID exceeds the maximum allowed length")

// Validate reports whether p has every field a well-formed hello payload
// requires: non-empty Platform, AgentVersion, and BootID, and a non-zero
// StartedAt. BootID is the only thing that distinguishes an agent restart
// from a continuous session (see BootID's doc comment), so a zero-valued
// HelloPayload — which is exactly what `json.Unmarshal` of a `null` or
// missing payload silently produces — must never pass as a genuine
// advertisement. Validate also bounds the capability set's cardinality and
// each member's ID length (see [maxCapabilityCount]); it does not otherwise
// validate Capabilities — see that field's doc comment.
func (p HelloPayload) Validate() error {
	switch {
	case p.Platform == "":
		return fmt.Errorf("%w: platform", ErrPayloadMissingField)
	case p.AgentVersion == "":
		return fmt.Errorf("%w: agentVersion", ErrPayloadMissingField)
	case p.BootID == "":
		return fmt.Errorf("%w: bootId", ErrPayloadMissingField)
	case p.StartedAt.IsZero():
		return fmt.Errorf("%w: startedAt", ErrPayloadMissingField)
	}
	if len(p.Capabilities) > maxCapabilityCount {
		return fmt.Errorf("%w: %d capabilities, max %d", ErrPayloadCapabilitySetTooLarge, len(p.Capabilities), maxCapabilityCount)
	}
	for _, c := range p.Capabilities {
		if len(c.ID) > maxCapabilityIDLength {
			return fmt.Errorf("%w: %d bytes, max %d", ErrPayloadCapabilityIDTooLong, len(c.ID), maxCapabilityIDLength)
		}
	}
	return nil
}

// HealthPayload is the payload of the showmesh.node.health/v1 schema,
// published retained on showmesh/nodes/<node-id>/observed/health: a
// periodic agent health heartbeat.
//
// Like [HelloPayload], HealthPayload carries no NodeID and no send
// timestamp; see HelloPayload's doc comment for why. Provenance and receipt
// time for this observation come from the envelope and, per ADR-011, from
// the coordinator's own receipt time, not from anything stored here.
type HealthPayload struct {
	// BootID identifies which agent process session this heartbeat belongs
	// to; see [HelloPayload.BootID].
	BootID string `json:"bootId"`

	// Sequence is a monotonically increasing counter, scoped to BootID: it
	// resets to 0 (or whatever the agent chooses to start at) on every
	// process restart, since a new BootID means a new session. A consumer
	// can use it to detect gaps or out-of-order delivery within one boot.
	Sequence uint64 `json:"sequence"`

	// AgentState is the agent's self-reported state as a short string
	// (e.g. "running", "degraded"). ARCHITECTURE section 7's operational
	// state machine (offline/resting/pre-show/live/...) describes the
	// SHOW's lifecycle state, not necessarily the agent process's own
	// state; this package does not constrain AgentState to that vocabulary
	// or invent a competing one, since neither is decided at this layer.
	AgentState string `json:"state"`

	// UptimeMS is the agent process's uptime in milliseconds at the
	// envelope's SentAt. An explicit *MS suffix is used, following the
	// *Secs precedent in internal/coordinator/server.go's observedAgeSecs,
	// rather than encoding a time.Duration field (which would marshal as a
	// bare nanosecond integer with no unit in the field name).
	UptimeMS int64 `json:"uptimeMs"`
}

// ErrPayloadSequenceTooLarge is wrapped by [HealthPayload.Validate] when
// Sequence exceeds math.MaxInt64.
//
// Sequence is wire-typed uint64, but internal/coordinator/store's
// node_health.sequence column is a signed 64-bit integer (SQLite has no
// unsigned integer type), and database/sql's driver binding rejects a
// uint64 value with the high bit set outright ("uint64 values with high bit
// set are not supported") rather than silently truncating or wrapping it.
// Rejecting the out-of-range value here, at payload validation, means that
// wire-versus-column type mismatch can never reach SQL at all: the message
// is skipped with a warning instead of RecordHealth returning a driver
// error. See CLAUDE.md's build-phase guidance on why this package, not
// internal/coordinator/store, is where a wire-format value's legal range is
// enforced.
var ErrPayloadSequenceTooLarge = errors.New("mqttproto: sequence exceeds the maximum value the store's signed 64-bit column can hold")

// Validate reports whether p has every field a well-formed health payload
// requires: a non-empty BootID. See [HelloPayload.Validate]'s doc comment
// for why a zero-valued payload (what a `null` or missing payload silently
// decodes into) must never pass as a genuine heartbeat. Validate also
// rejects a Sequence above math.MaxInt64; see [ErrPayloadSequenceTooLarge].
func (p HealthPayload) Validate() error {
	if p.BootID == "" {
		return fmt.Errorf("%w: bootId", ErrPayloadMissingField)
	}
	if p.Sequence > math.MaxInt64 {
		return fmt.Errorf("%w: %d", ErrPayloadSequenceTooLarge, p.Sequence)
	}
	return nil
}

// LWTPayload is the payload of the showmesh.node.lwt/v1 schema, published
// on showmesh/nodes/<node-id>/lwt as a node's registered MQTT Last Will.
// Like [HelloPayload] and [HealthPayload], it carries no NodeID: the
// envelope is the sole carrier of identity for all three payloads.
//
// TIMESTAMP TRAP: the broker publishes a Last Will verbatim, exactly as it
// was registered at CONNECT time. That means the Envelope wrapping this
// payload has a SentAt that is when the agent CONNECTED, not when it died;
// by the time this message is actually delivered, SentAt may be wrong by
// the entire length of the session that just ended. Do not use it, or any
// timestamp on this payload (there is none here for exactly this reason),
// as a time of death.
//
// The correct provenance for an LWT observation is "broker last will", and
// the correct observation time is the coordinator's own receipt time, per
// ADR-011 (evidence must carry freshness and provenance; the sender's
// clock is not a substitute for the receiver's).
type LWTPayload struct {
	// Online is always false for a genuine Last Will: the broker only ever
	// publishes the registered will when a client disconnects without a
	// clean DISCONNECT. The field still exists (rather than the payload
	// being will-only-ever-means-false) so this schema could in principle
	// also be used for a node's own graceful "I am going offline" message
	// in a later step, without needing a second schema.
	Online bool `json:"online"`

	// Reason is a short, human-readable string for why the node is
	// (or, per Online, is not) online. For a genuine broker-delivered will
	// this is whatever the agent registered at connect time (e.g.
	// "unexpected disconnect"), not something the agent can update after
	// the fact.
	Reason string `json:"reason"`
}

// CmdTarget mirrors pkg/command.Target's field semantics on the wire — see
// this package's doc comment for why this package defines its own
// JSON-tagged types rather than importing pkg/command.
type CmdTarget struct {
	Kind string `json:"kind"`
	ID   string `json:"id"`
}

// CmdIssuer mirrors pkg/command.Issuer's field semantics on the wire.
type CmdIssuer struct {
	PrincipalID   string `json:"principalId"`
	PrincipalName string `json:"principalName"`
}

// CmdPayload is the payload of the showmesh.node.cmd/v1 schema, published
// (never retained — see [CmdDeliveryPolicy]) to a node's cmd topic. Field
// semantics mirror pkg/command.Envelope; see this file's top-of-package
// note for why this is an independent, JSON-tagged type rather than that
// package's Envelope reused directly. Unlike HelloPayload/HealthPayload/
// LWTPayload, CmdPayload DOES carry its own identifier (CommandID) and
// idempotency key: those are the command's own identity, not the sending
// node's, so there is no risk of the topic-injection double-carrying
// problem [HelloPayload]'s doc comment describes for NodeID.
type CmdPayload struct {
	// CommandID identifies this command, matching pkg/command.Envelope.ID.
	CommandID string `json:"commandId"`

	// IdempotencyKey is what a redelivery of the SAME logical command is
	// detected by (ADR-008: QoS 1 + idempotency keys so a redelivered
	// command executes exactly once). Distinct from CommandID for the same
	// reason pkg/command.Envelope.IdempotencyKey's doc comment gives.
	IdempotencyKey string `json:"idempotencyKey"`

	// Action identifies what this command does, e.g. "agent.echo". This
	// package defines no action vocabulary of its own, matching
	// pkg/command.Envelope.Action's doc comment.
	Action string `json:"action"`

	Target CmdTarget `json:"target"`

	// Params carries this command's arguments. Deliberately NOT
	// `omitempty`: this project has a standing rule that absent, null, and
	// explicitly empty are three different things on a wire payload (the
	// same class of bug this codebase has shipped four times — see
	// CLAUDE.md), and `omitempty` on a map collapses two of them (a nil
	// Params and an explicitly-set, empty map[string]any{}) into the same
	// "no params key at all" wire representation, indistinguishable from
	// each other. Without `omitempty`, this package's own encoder emits
	// "params":null for a nil map and "params":{} for an explicit empty
	// one, so those two stay distinguishable. A fully absent "params" key
	// (only reachable from a non-Go or hand-crafted producer, never from
	// this package's own constructors) still decodes to the same nil Go
	// map as an explicit null — an inherent limitation of representing
	// this field as map[string]any rather than a pointer, not something
	// `omitempty` was ever hiding or fixing. [CmdPayload.Validate]
	// deliberately does not require Params to be non-nil: a future
	// operation may legitimately take none.
	Params map[string]any `json:"params"`

	Issuer CmdIssuer `json:"issuer"`

	// RequestedRevision mirrors pkg/command.Envelope.RequestedRevision:
	// empty for a command with no revision to be sensitive to.
	//
	// This seam DECODES RequestedRevision but does not enforce it anywhere
	// — see internal/agent/command.go's HandleMessage, where cmd is
	// decoded, for where that gap is called out explicitly. The one
	// allowlisted operation this seam ships, "agent.echo", is not
	// revision-sensitive; a future operation that is must add its own
	// enforcement, not inherit one from here.
	RequestedRevision string `json:"requestedRevision,omitempty"`

	// ConfirmationMethod mirrors pkg/command.Envelope.ConfirmationMethod.
	// Today only "evidence" (pkg/command.ConfirmationEvidence's value) is
	// implemented anywhere in this codebase; this package does not import
	// pkg/command to enforce that as a shared constant (see this file's
	// top-of-package note), so a caller comparing against the literal must
	// keep it in sync with pkg/command.ConfirmationEvidence by convention,
	// the same way cmd/showmeshctl's own doc comments describe reconciling
	// independently-chosen values without a shared import.
	ConfirmationMethod string `json:"confirmationMethod"`

	// Deadline is the absolute time by which confirmation must succeed, or
	// nil for "no deadline was set" — never a zero time standing in for
	// that, matching pkg/command.Envelope.Deadline's doc comment exactly.
	// A nil Deadline is a legitimate, valid state: [CmdPayload.Validate]
	// deliberately performs no required-ness check on this field.
	Deadline *time.Time `json:"deadline"`
}

// Validate reports whether p has every field a well-formed command payload
// requires: non-empty CommandID, IdempotencyKey, Action, Target.Kind,
// Target.ID, Issuer.PrincipalID, and ConfirmationMethod. Deadline is
// deliberately NOT checked for presence — nil legitimately means "no
// deadline," not a defect (see the field's doc comment); this project has
// a standing rule that absent, null, and explicitly empty are three
// different things on any write surface, and treating a nil Deadline as
// "missing" here would be exactly that mistake.
func (p CmdPayload) Validate() error {
	switch {
	case p.CommandID == "":
		return fmt.Errorf("%w: commandId", ErrPayloadMissingField)
	case p.IdempotencyKey == "":
		return fmt.Errorf("%w: idempotencyKey", ErrPayloadMissingField)
	case p.Action == "":
		return fmt.Errorf("%w: action", ErrPayloadMissingField)
	case p.Target.Kind == "":
		return fmt.Errorf("%w: target.kind", ErrPayloadMissingField)
	case p.Target.ID == "":
		return fmt.Errorf("%w: target.id", ErrPayloadMissingField)
	case p.Issuer.PrincipalID == "":
		return fmt.Errorf("%w: issuer.principalId", ErrPayloadMissingField)
	case p.ConfirmationMethod == "":
		return fmt.Errorf("%w: confirmationMethod", ErrPayloadMissingField)
	}
	return nil
}

// Outcome vocabulary for [ResultPayload.Outcome]. This is a closed set,
// matching pkg/observation.State's closed-enum style: [ResultPayload.
// Validate] rejects any other string rather than treating outcome as
// freely extensible ad hoc.
const (
	OutcomeConfirmed   = "confirmed"
	OutcomeUnconfirmed = "unconfirmed"
	OutcomeRefused     = "refused"
	OutcomeFailed      = "failed"
)

// ResultEvidence is the evidence backing a [ResultPayload]'s outcome: what
// was observed, and when. Per ADR-003, an outcome must be backed by a
// distinct post-execution observation, not by the fact that a command
// merely arrived — this project has previously shipped a defect where a
// command was reported confirmed 179 microseconds after its own dispatch
// by comparing against a stale pre-dispatch reading; ResultEvidence exists
// so a result payload always carries the observation it was actually
// judged against, not merely a boolean.
type ResultEvidence struct {
	// Signal names what was observed, e.g. "node.agent.echo_value".
	Signal string `json:"signal"`

	// Value is what was observed. Left as `any` because the vocabulary of
	// evidence signals is not fixed by this package — see Action's doc
	// comment on why this package defines no operation vocabulary.
	Value any `json:"value"`

	// ObservedAt is when the observed value was true, or nil if genuinely
	// unknown — matching pkg/observation.Observation.ObservedAt's own
	// nil-means-unknown convention, never defaulted to CollectedAt.
	ObservedAt *time.Time `json:"observedAt"`

	// CollectedAt is when this evidence was gathered by the agent. Always
	// set; bookkeeping, not evidence of the subject's state, matching
	// pkg/observation.Observation.CollectedAt's own doc comment.
	CollectedAt time.Time `json:"collectedAt"`
}

// ResultPayload is the payload of the showmesh.node.result/v1 schema,
// published (never retained — see [ResultDeliveryPolicy]) to a command's
// result topic ([ResultTopic]).
type ResultPayload struct {
	CommandID      string `json:"commandId"`
	IdempotencyKey string `json:"idempotencyKey"`
	Action         string `json:"action"`

	// Outcome is one of [OutcomeConfirmed], [OutcomeUnconfirmed],
	// [OutcomeRefused], or [OutcomeFailed]. See [ResultPayload.Validate].
	Outcome string `json:"outcome"`

	// Reason is a short human-readable explanation. Required whenever
	// Outcome is not [OutcomeConfirmed] — mirroring, one layer up,
	// pkg/observation.Observation.Reason's own "required whenever there is
	// no current value" rule, applied here to "required whenever the
	// outcome isn't a plain success."
	Reason string `json:"reason"`

	// Evidence is the post-execution observation backing Outcome; see
	// [ResultEvidence]'s doc comment on why this must be a distinct,
	// separately-collected observation rather than an assumption from
	// dispatch. Nil when no evidence was collected (e.g. a refused or
	// failed command that never reached execution).
	Evidence *ResultEvidence `json:"evidence"`

	// ReceivedAt is when the agent received the command that produced this
	// result, on the agent's own clock.
	ReceivedAt time.Time `json:"receivedAt"`

	// ExecutedAt is when the operation actually ran, or nil if it never
	// did (refused, or failed before execution).
	ExecutedAt *time.Time `json:"executedAt"`

	// RespondedAt is when this result was published, on the agent's own
	// clock.
	RespondedAt time.Time `json:"respondedAt"`
}

// ErrPayloadInvalidOutcome is wrapped by [ResultPayload.Validate] when
// Outcome is not one of the four values this package's closed vocabulary
// permits.
var ErrPayloadInvalidOutcome = errors.New("mqttproto: outcome is not a recognized value")

// Validate reports whether p has every field a well-formed result payload
// requires: non-empty CommandID, IdempotencyKey, and Action; Outcome one
// of the four [OutcomeConfirmed]/[OutcomeUnconfirmed]/[OutcomeRefused]/
// [OutcomeFailed] values; and a non-empty Reason whenever Outcome is not
// [OutcomeConfirmed] (see Reason's doc comment).
func (p ResultPayload) Validate() error {
	switch {
	case p.CommandID == "":
		return fmt.Errorf("%w: commandId", ErrPayloadMissingField)
	case p.IdempotencyKey == "":
		return fmt.Errorf("%w: idempotencyKey", ErrPayloadMissingField)
	case p.Action == "":
		return fmt.Errorf("%w: action", ErrPayloadMissingField)
	}
	switch p.Outcome {
	case OutcomeConfirmed, OutcomeUnconfirmed, OutcomeRefused, OutcomeFailed:
	default:
		return fmt.Errorf("%w: %q", ErrPayloadInvalidOutcome, p.Outcome)
	}
	if p.Outcome != OutcomeConfirmed && p.Reason == "" {
		return fmt.Errorf("%w: reason (required whenever outcome is not %q)", ErrPayloadMissingField, OutcomeConfirmed)
	}
	return nil
}

// AgentEchoPayload is the payload of the showmesh.node.agent.echo/v1
// schema, published RETAINED to a node's observed/agent/echo topic
// ([ObservedTopic]): the durable, current value of the agent's one
// trivial allowlisted operation ("agent.echo"), published like every
// other observed signal in this system, per [ObservedDeliveryPolicy].
//
// No Validate method, matching [LWTPayload]: an empty Value is a
// legitimate state (the operation has never run since agent start), and a
// non-zero AppliedAt is enforced by construction (internal/agent stamps it
// from a real clock read), not by decode-time validation.
type AgentEchoPayload struct {
	Value     string    `json:"value"`
	AppliedAt time.Time `json:"appliedAt"`
}

// ErrPayloadEmpty is wrapped by [DecodeHelloPayload], [DecodeHealthPayload],
// and [DecodeLWTPayload] when env.Payload is empty (including an absent
// "payload" key, which unmarshals to a zero-length json.RawMessage) or is
// literally the JSON value `null`. Both cases are rejected up front, before
// any schema-specific unmarshal: `json.Unmarshal([]byte("null"), &p)` is
// documented as a no-op, so without this check a `null` payload would
// silently decode into a fully zero-valued payload struct and pass as a
// genuine message from an untrusted node. This mirrors, one layer down, the
// explicit required-field validation [Envelope.Validate] already performs
// on the envelope itself.
var ErrPayloadEmpty = errors.New("mqttproto: payload is empty or null")

// ErrPayloadMissingField is wrapped by [HelloPayload.Validate] and
// [HealthPayload.Validate] when a required payload field is empty or zero,
// so a caller can distinguish a malformed (but schema-recognized) payload
// from an [UnsupportedSchemaError].
var ErrPayloadMissingField = errors.New("mqttproto: payload is missing a required field")

// checkPayloadPresent rejects an empty or literal-null payload; see
// [ErrPayloadEmpty].
func checkPayloadPresent(payload json.RawMessage) error {
	if len(payload) == 0 || bytes.Equal(bytes.TrimSpace(payload), []byte("null")) {
		return ErrPayloadEmpty
	}
	return nil
}

// DecodeHelloPayload decodes env.Payload as a [HelloPayload]. It returns an
// [*UnsupportedSchemaError] if env.Schema is not [SchemaNodeHelloV1], an
// error wrapping [ErrPayloadEmpty] if env.Payload is empty or null, and an
// error wrapping [ErrPayloadMissingField] (via [HelloPayload.Validate]) if
// a required field is missing.
func DecodeHelloPayload(env Envelope) (HelloPayload, error) {
	if env.Schema != SchemaNodeHelloV1 {
		return HelloPayload{}, &UnsupportedSchemaError{Got: env.Schema, Want: SchemaNodeHelloV1}
	}
	if err := checkPayloadPresent(env.Payload); err != nil {
		return HelloPayload{}, fmt.Errorf("mqttproto: decode hello payload: %w", err)
	}
	var p HelloPayload
	if err := json.Unmarshal(env.Payload, &p); err != nil {
		return HelloPayload{}, fmt.Errorf("mqttproto: decode hello payload: %w", err)
	}
	if err := p.Validate(); err != nil {
		return HelloPayload{}, fmt.Errorf("mqttproto: decode hello payload: %w", err)
	}
	return p, nil
}

// DecodeHealthPayload decodes env.Payload as a [HealthPayload]. It returns
// an [*UnsupportedSchemaError] if env.Schema is not [SchemaNodeHealthV1], an
// error wrapping [ErrPayloadEmpty] if env.Payload is empty or null, and an
// error wrapping [ErrPayloadMissingField] (via [HealthPayload.Validate]) if
// a required field is missing.
func DecodeHealthPayload(env Envelope) (HealthPayload, error) {
	if env.Schema != SchemaNodeHealthV1 {
		return HealthPayload{}, &UnsupportedSchemaError{Got: env.Schema, Want: SchemaNodeHealthV1}
	}
	if err := checkPayloadPresent(env.Payload); err != nil {
		return HealthPayload{}, fmt.Errorf("mqttproto: decode health payload: %w", err)
	}
	var p HealthPayload
	if err := json.Unmarshal(env.Payload, &p); err != nil {
		return HealthPayload{}, fmt.Errorf("mqttproto: decode health payload: %w", err)
	}
	if err := p.Validate(); err != nil {
		return HealthPayload{}, fmt.Errorf("mqttproto: decode health payload: %w", err)
	}
	return p, nil
}

// DecodeLWTPayload decodes env.Payload as an [LWTPayload]. It returns an
// [*UnsupportedSchemaError] if env.Schema is not [SchemaNodeLWTV1], and an
// error wrapping [ErrPayloadEmpty] if env.Payload is empty or null.
// LWTPayload has no Validate method (see its doc comment: Online is
// meaningfully false-or-true and Reason has no required content), so an
// empty/null rejection is the only payload-level check performed here.
func DecodeLWTPayload(env Envelope) (LWTPayload, error) {
	if env.Schema != SchemaNodeLWTV1 {
		return LWTPayload{}, &UnsupportedSchemaError{Got: env.Schema, Want: SchemaNodeLWTV1}
	}
	if err := checkPayloadPresent(env.Payload); err != nil {
		return LWTPayload{}, fmt.Errorf("mqttproto: decode lwt payload: %w", err)
	}
	var p LWTPayload
	if err := json.Unmarshal(env.Payload, &p); err != nil {
		return LWTPayload{}, fmt.Errorf("mqttproto: decode lwt payload: %w", err)
	}
	return p, nil
}

// DecodeCmdPayload decodes env.Payload as a [CmdPayload]. It returns an
// [*UnsupportedSchemaError] if env.Schema is not [SchemaNodeCmdV1], an
// error wrapping [ErrPayloadEmpty] if env.Payload is empty or null, and an
// error wrapping [ErrPayloadMissingField] (via [CmdPayload.Validate]) if a
// required field is missing.
func DecodeCmdPayload(env Envelope) (CmdPayload, error) {
	if env.Schema != SchemaNodeCmdV1 {
		return CmdPayload{}, &UnsupportedSchemaError{Got: env.Schema, Want: SchemaNodeCmdV1}
	}
	if err := checkPayloadPresent(env.Payload); err != nil {
		return CmdPayload{}, fmt.Errorf("mqttproto: decode cmd payload: %w", err)
	}
	var p CmdPayload
	if err := json.Unmarshal(env.Payload, &p); err != nil {
		return CmdPayload{}, fmt.Errorf("mqttproto: decode cmd payload: %w", err)
	}
	if err := p.Validate(); err != nil {
		return CmdPayload{}, fmt.Errorf("mqttproto: decode cmd payload: %w", err)
	}
	return p, nil
}

// DecodeResultPayload decodes env.Payload as a [ResultPayload]. It returns
// an [*UnsupportedSchemaError] if env.Schema is not [SchemaNodeResultV1],
// an error wrapping [ErrPayloadEmpty] if env.Payload is empty or null, and
// an error wrapping [ErrPayloadMissingField] or [ErrPayloadInvalidOutcome]
// (via [ResultPayload.Validate]) if the payload is malformed.
func DecodeResultPayload(env Envelope) (ResultPayload, error) {
	if env.Schema != SchemaNodeResultV1 {
		return ResultPayload{}, &UnsupportedSchemaError{Got: env.Schema, Want: SchemaNodeResultV1}
	}
	if err := checkPayloadPresent(env.Payload); err != nil {
		return ResultPayload{}, fmt.Errorf("mqttproto: decode result payload: %w", err)
	}
	var p ResultPayload
	if err := json.Unmarshal(env.Payload, &p); err != nil {
		return ResultPayload{}, fmt.Errorf("mqttproto: decode result payload: %w", err)
	}
	if err := p.Validate(); err != nil {
		return ResultPayload{}, fmt.Errorf("mqttproto: decode result payload: %w", err)
	}
	return p, nil
}

// newEnvelope stamps the fields every constructor must set so a caller
// cannot forget one: a fresh UUIDv4 MessageID, SentAt from now (in UTC),
// and the given schema and node ID. now is a clock function so tests do not
// depend on real time (the same pattern pkg/multisync's NewTimeline uses);
// if now is nil, time.Now is used.
func newEnvelope(now func() time.Time, schema, nodeID string, payload any) (Envelope, error) {
	if now == nil {
		now = time.Now
	}
	if err := ValidateNodeID(nodeID); err != nil {
		return Envelope{}, err
	}
	raw, err := json.Marshal(payload)
	if err != nil {
		return Envelope{}, fmt.Errorf("mqttproto: marshal %s payload: %w", schema, err)
	}
	return Envelope{
		Schema:    schema,
		MessageID: uuid.NewString(),
		NodeID:    nodeID,
		SentAt:    now().UTC(),
		Payload:   raw,
	}, nil
}

// NewHelloEnvelope builds a complete, schema-tagged [Envelope] carrying
// payload for nodeID, stamping MessageID and SentAt (see [newEnvelope]).
// All three constructors in this file take nodeID as an explicit argument
// and stamp Envelope.NodeID from it directly, uniformly: no constructor
// derives identity from payload contents, since [HelloPayload],
// [HealthPayload], and [LWTPayload] do not carry a NodeID field to derive
// it from.
func NewHelloEnvelope(now func() time.Time, nodeID string, payload HelloPayload) (Envelope, error) {
	return newEnvelope(now, SchemaNodeHelloV1, nodeID, payload)
}

// NewHealthEnvelope builds a complete, schema-tagged [Envelope] carrying
// payload for nodeID, stamping MessageID and SentAt (see [newEnvelope] and
// [NewHelloEnvelope]'s doc comment on the uniform nodeID argument).
func NewHealthEnvelope(now func() time.Time, nodeID string, payload HealthPayload) (Envelope, error) {
	return newEnvelope(now, SchemaNodeHealthV1, nodeID, payload)
}

// NewLWTEnvelope builds a complete, schema-tagged [Envelope] carrying
// payload for nodeID, stamping MessageID and SentAt (see [newEnvelope] and
// [NewHelloEnvelope]'s doc comment on the uniform nodeID argument).
//
// In real use this envelope is registered with the MQTT client at CONNECT
// time and only actually published by the broker later, verbatim, on
// unexpected disconnect: see [LWTPayload]'s TIMESTAMP TRAP doc comment for
// why the resulting SentAt must not be read as a time of death.
func NewLWTEnvelope(now func() time.Time, nodeID string, payload LWTPayload) (Envelope, error) {
	return newEnvelope(now, SchemaNodeLWTV1, nodeID, payload)
}

// NewCmdEnvelope builds a complete, schema-tagged [Envelope] carrying
// payload for nodeID, stamping MessageID and SentAt (see [newEnvelope] and
// [NewHelloEnvelope]'s doc comment on the uniform nodeID argument).
func NewCmdEnvelope(now func() time.Time, nodeID string, payload CmdPayload) (Envelope, error) {
	return newEnvelope(now, SchemaNodeCmdV1, nodeID, payload)
}

// NewResultEnvelope builds a complete, schema-tagged [Envelope] carrying
// payload for nodeID, stamping MessageID and SentAt (see [newEnvelope] and
// [NewHelloEnvelope]'s doc comment on the uniform nodeID argument).
func NewResultEnvelope(now func() time.Time, nodeID string, payload ResultPayload) (Envelope, error) {
	return newEnvelope(now, SchemaNodeResultV1, nodeID, payload)
}

// NewAgentEchoEnvelope builds a complete, schema-tagged [Envelope] carrying
// payload for nodeID, stamping MessageID and SentAt (see [newEnvelope] and
// [NewHelloEnvelope]'s doc comment on the uniform nodeID argument).
func NewAgentEchoEnvelope(now func() time.Time, nodeID string, payload AgentEchoPayload) (Envelope, error) {
	return newEnvelope(now, SchemaNodeAgentEchoV1, nodeID, payload)
}
