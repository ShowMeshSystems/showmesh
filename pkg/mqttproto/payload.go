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

	// SchemaNodeAssetInventoryV1 is Track E's addition: the schema for
	// [AssetInventoryPayload], published retained on
	// showmesh/nodes/<node-id>/observed/assets.
	SchemaNodeAssetInventoryV1 = "showmesh.node.asset.inventory/v1"

	// SchemaNodeRenderV1 is Track B seam B2a's addition: the schema for
	// [RenderPayload], published retained on
	// showmesh/nodes/<node-id>/observed/render.
	SchemaNodeRenderV1 = "showmesh.node.render/v1"

	// SchemaNodeAudioV1 is the schema for
	// [AudioPayload], published retained on
	// showmesh/nodes/<node-id>/observed/audio.
	SchemaNodeAudioV1 = "showmesh.node.audio/v1"
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

// ErrPayloadTooLarge is wrapped by [RenderPayload.Validate] when a bounded
// slice or string field exceeds its stated cap — the general-purpose
// sibling of [ErrPayloadCapabilitySetTooLarge], which stays named for the
// one payload it was written for.
var ErrPayloadTooLarge = errors.New("mqttproto: payload field exceeds the maximum allowed size")

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

// AssetInventoryEntry is one asset a node reports holding, inside
// [AssetInventoryPayload].
type AssetInventoryEntry struct {
	// ContentHash is "sha256:<hex>", matching ADR-028 decision 1's identity
	// scheme.
	ContentHash string `json:"contentHash"`

	// Filename is the runtime filename this asset is stored under on the
	// node's local disk — never part of any identity or lookup key (ADR-028:
	// three different artifacts can share one filename), carried here purely
	// so a consumer can display or cross-reference it.
	Filename string `json:"filename"`

	SizeBytes int64 `json:"sizeBytes"`

	// VerifiedAt is when this node last computed and confirmed ContentHash
	// for this file, on the node's own clock.
	VerifiedAt time.Time `json:"verifiedAt"`
}

// AssetInventoryPayload is the payload of the showmesh.node.asset.inventory/v1
// schema, published RETAINED to a node's observed/assets topic
// ([ObservedTopic], [ObservedDeliveryPolicy]): what this node's local asset
// directory actually holds, as of the report's own construction.
//
// Complete IS THE LICENCE THIS PROJECT'S COORDINATOR USES TO ASSERT AN ASSET
// IS ABSENT FROM A NODE, so it has to be earned by the publisher: it must be
// false, with a specific Reason, whenever the directory could not be fully
// enumerated (could not be read, did not exist, or any file's hash could not
// be computed) — never true off a partial walk. A wrong true here is
// indistinguishable, downstream, from a coordinator manufacturing absence
// from ambiguous evidence, which this project has already shipped and fixed
// once in a different subsystem.
type AssetInventoryPayload struct {
	Complete bool   `json:"complete"`
	Reason   string `json:"reason"`

	// Assets is nil-safe: this package's own encoder emits "assets":null
	// for a nil slice and "assets":[] for an explicit empty one, matching
	// CmdPayload.Params's own no-omitempty rule for the same reason — a
	// consumer should be able to tell "nothing enumerated because Complete
	// is false" apart from "enumerated successfully, found nothing" if a
	// future caller ever needs to.
	Assets []AssetInventoryEntry `json:"assets"`
}

// Validate reports whether p is well-formed: Reason must be non-empty
// whenever Complete is false, mirroring ResultPayload.Reason's identical
// "required whenever there is no plain success" rule one layer up. A
// Complete:true payload with no Reason is the expected shape and is not an
// error.
func (p AssetInventoryPayload) Validate() error {
	if !p.Complete && p.Reason == "" {
		return fmt.Errorf("%w: reason (required whenever complete is false)", ErrPayloadMissingField)
	}
	return nil
}

// Open pipelineState vocabulary for [RenderSurfaceReport.PipelineState].
// Deliberately a string, not a closed enum: [pkg/agent/pipeline] may need a
// finer-grained state later, and a consumer that does not recognize one must
// treat it as evidence-with-an-unrecognized-label, never as an error, per
// this schema family's "the API is additive; clients ignore what they don't
// know" convention (ADR-020).
const (
	RenderPipelineStateRunning     = "running"
	RenderPipelineStateStarting    = "starting"
	RenderPipelineStateRestarting  = "restarting"
	RenderPipelineStateFailed      = "failed"
	RenderPipelineStateStopped     = "stopped"
	RenderPipelineStateUnsupported = "unsupported"
)

// maxRenderSurfaces bounds [RenderPayload.Surfaces], matching
// [maxCapabilityCount]'s role: ADR-026 expresses N surfaces per node in the
// schema even though v1 runs N=1, so this must not be 1. Deliberately small
// relative to [maxCapabilityCount]: at [maxRenderStderrBytes] each, this
// bound times that one must stay comfortably under [maxEnvelopeSize]
// (8*4KiB = 32KiB), not merely under it — a conservative, unmeasured guess
// at "far more surfaces than any real node will ever run, far short of the
// envelope cap."
const maxRenderSurfaces = 8

// maxRenderHeldFiles and maxRenderHeldEvents bound
// [RenderPayload.FPPConnectHeld] and [RenderPayload.FPPConnectHeldEvents]
// (review round 3 finding 2): an unbounded held-file list, or an unbounded
// evidence log, could otherwise ride every render report past
// [maxEnvelopeSize] once enough files or events accumulate. Conservative,
// unmeasured guesses at "far more than a real show's node ever holds or
// generates, far short of the envelope cap," the same reasoning
// [maxRenderSurfaces] states for Surfaces one field up.
//
// maxRenderHeldEvents is deliberately above internal/agent/fppconnectheld.
// go's own fppConnectMaxEvents (50): that constant already bounds how many
// events the store itself ever holds, so this wire cap exists only as a
// backstop against a caller that skips the publisher's own truncation, and
// must never be smaller than what normal operation legitimately produces.
const (
	maxRenderHeldFiles  = 256
	maxRenderHeldEvents = 64
)

// maxRenderStderrBytes bounds [RenderSurfaceReport.LastStderr] before
// [RenderPayload.Validate] rejects the payload outright — the wire-boundary
// backstop behind whatever cap internal/agent/pipeline already applies when
// it captures a process's stderr. Truncation must happen, and be visible,
// before the payload ever reaches this package; see LastStderr's own doc
// comment.
const maxRenderStderrBytes = 4 * 1024

// RenderStderrTruncatedSuffix is appended by the publisher (never by this
// package) when it cuts LastStderr down to [maxRenderStderrBytes], so a
// truncated tail reads as truncated rather than as a stderr that happened to
// end mid-sentence.
const RenderStderrTruncatedSuffix = "...[truncated]"

// RenderSurfaceReport is one surface's pipeline health, inside
// [RenderPayload.Surfaces]. ADR-026 decision 3 requires N surfaces
// expressible even though v1 runs exactly one.
type RenderSurfaceReport struct {
	// SurfaceID is the show.surface config object id this report concerns.
	SurfaceID string `json:"surfaceId"`

	// PipelineState is this surface's current supervised pipeline state; see
	// the RenderPipelineState* constants for the minimum open vocabulary.
	PipelineState string `json:"pipelineState"`

	// Reason is required whenever PipelineState is not
	// [RenderPipelineStateRunning] — absent evidence is stated, never
	// omitted (ADR-020).
	Reason string `json:"reason"`

	// Since is when this surface entered PipelineState, on the node's own
	// clock.
	Since time.Time `json:"since"`

	RestartCount        int64 `json:"restartCount"`
	ConsecutiveFailures int64 `json:"consecutiveFailures"`

	// LastExitCode is nil when no attempt has exited yet (still starting, or
	// killed before any process was ever launched); non-nil is a genuine
	// observed exit code, including 0.
	LastExitCode *int `json:"lastExitCode"`

	// LastStderr is the supervised process's most recent captured stderr
	// tail, bounded to [maxRenderStderrBytes]. A truncated tail carries
	// [RenderStderrTruncatedSuffix] so truncation is visible on the wire,
	// never silent.
	LastStderr string `json:"lastStderr"`

	FramesWritten int64 `json:"framesWritten"`
	FramesLate    int64 `json:"framesLate"`
	FramesDropped int64 `json:"framesDropped"`

	// FramesRate is the frame writer's own measured achieved output rate in
	// frames/second (ADR-040's obligation), nil until it has completed at
	// least one full sampling window — never a plausible-looking zero and
	// never the surface's configured frameRate echoed back.
	FramesRate *float64 `json:"framesRate"`

	// Transport names the output transport this surface's pipeline is
	// configured for (e.g. "ndi"); empty when not yet meaningful (seam B2a
	// runs a test-pattern pipeline with no real output stage).
	Transport string `json:"transport"`

	// TransportAvailable is nil when the transport has not been probed,
	// true/false when it has (internal/agent/pipeline.ProbeNDISend) — see
	// ADR-011: nil is genuinely unknown, never defaulted to a boolean.
	TransportAvailable *bool `json:"transportAvailable"`

	// TransportReason is required whenever TransportAvailable is non-nil
	// and false — an actionable pointer (e.g. missing NDI runtime install
	// instructions), never a bare "unavailable." Left empty when
	// TransportAvailable is true or nil, matching Reason's identical rule
	// for PipelineState one field up.
	TransportReason string `json:"transportReason"`

	// ObservedAt is when the supervisor actually sampled this report, on
	// the node's own clock — the evidence timestamp ADR-003 requires,
	// distinct from Since (when the state itself began).
	ObservedAt time.Time `json:"observedAt"`

	// FramesObservedAt is when this surface's frame writer actually closed
	// a sampling window and sampled FramesWritten/FramesLate/
	// FramesDropped/FramesRate together: its own timestamp, deliberately
	// independent of ObservedAt above. ObservedAt only moves when
	// PipelineState transitions (pipeline.runner.setState); the frame
	// counters are continuously sampled measurements, not a lifecycle
	// transition, and sharing ObservedAt made every render signal read as
	// permanently stale 45s after any apply on an otherwise healthy
	// pipeline, because ObservedAt then never moved again while the
	// counters kept climbing. Zero (IsZero()) means the frame writer has
	// not yet completed its first sampling window (ADR-011: zero/nil is
	// unknown, never defaulted to "now" or to the report's own publish
	// time), not enforced as required by Validate, matching
	// RenderPayload.MultiSyncObservedAt's identical additive-compatibility
	// reasoning one field over: this field is added after
	// RenderSurfaceReport first shipped, and a hard requirement here would
	// reject every fixture and payload built before it existed.
	FramesObservedAt time.Time `json:"framesObservedAt"`

	// TimelineState is the multisync.Timeline state ("playing",
	// "unsynchronized", "opened", "stopping", "stopped", "unknown") this
	// surface's frame writer most recently sampled. "" means no frame
	// writer is currently active for this surface (a Track B seam B2a
	// test-pattern-only pipeline has no FSEQ and therefore no writer at
	// all) — never inferred from PipelineState.
	TimelineState string `json:"timelineState"`

	// TimelinePositionMS is the timeline position this surface's most
	// recent CONTENT frame (Drawing == [RenderDrawingContent]) was
	// extracted from. nil whenever Drawing is [RenderDrawingIdle],
	// [RenderDrawingFailure], or "":
	// a position is only meaningful when content is actually being read
	// from it (ADR-011: nil is genuinely inapplicable here, never a stale
	// or zero position echoed back).
	TimelinePositionMS *int64 `json:"timelinePositionMs"`

	// Drawing is what this surface's frame writer actually wrote to the
	// pipeline's stdin on its most recent tick: [RenderDrawingContent],
	// [RenderDrawingIdle], or [RenderDrawingFailure]. "" means no frame
	// writer is currently active for this surface. This is the evidence
	// this build contract names explicitly: PipelineState=="running" alone
	// cannot tell an operator "rendering content" from "emitting black at
	// 40fps," and this field is what can (Track B finding 7).
	Drawing string `json:"drawing"`

	// IdleMode is the configured idle output ([RenderIdleOutputBlack],
	// [RenderIdleOutputHold], or [RenderIdleOutputDiagnostic]) whenever
	// Drawing is [RenderDrawingIdle]; "" otherwise, matching Reason's and
	// TransportReason's identical required-whenever-the-flag-says-so rule.
	// Never carries a value while Drawing is [RenderDrawingFailure]: a
	// failure is not an idle mode, and reporting one there is what let a
	// broken assignment read as a normal idle cycle.
	IdleMode string `json:"idleMode"`

	// FailureOutput is what a [RenderDrawingFailure] tick actually put on
	// the wire, [RenderFailureOutputAlert] or [RenderFailureOutputBlack];
	// "" whenever Drawing is anything else. Required whenever Drawing is
	// [RenderDrawingFailure], IdleMode's identical rule one field up,
	// because the two failure outputs look nothing alike at the wall and
	// an operator reading this report has to know which one is in front of
	// the audience.
	FailureOutput string `json:"failureOutput"`
}

// RenderDrawingContent, RenderDrawingIdle, and RenderDrawingFailure are the
// three values [RenderSurfaceReport.Drawing] can carry.
//
// RenderDrawingFailure is neither of the other two on purpose: the writer
// could not extract the frame it was asked for, so what reached the wire is
// a fallback nobody configured. Reporting that as "idle" with an idle mode
// (which this payload did until an owner ruling) makes a broken assignment
// read as an operator-chosen idle cycle in every report that renders it.
const (
	RenderDrawingContent = "content"
	RenderDrawingIdle    = "idle"
	RenderDrawingFailure = "failure"
)

// RenderFailureOutputAlert and RenderFailureOutputBlack are the two values
// [RenderSurfaceReport.FailureOutput] can carry: this package's own copy of
// internal/agent/pipeline's identical FailureOutputAlert/FailureOutputBlack
// constants, independently reproduced for the same reason the idle-output
// constants below are.
//
// Which one a node draws is the operating mode's decision, made fresh at
// the frame the failure happens on (ADR-033, ADR-036): an unmistakable
// alert field in Program Mode, black in Show Mode and whenever the mode is
// unknown. Black in front of an audience beats red; black in front of an
// operator who is programming says "fine" about a broken assignment.
const (
	RenderFailureOutputAlert = "alert"
	RenderFailureOutputBlack = "black"
)

// RenderIdleOutputBlack, RenderIdleOutputHold, and RenderIdleOutputDiagnostic
// are this package's own copy of internal/agent/pipeline's identical
// IdleOutputBlack/Hold/Diagnostic constants (build contract ruling 3) —
// independently reproduced, not imported, per this codebase's standing
// each-side-of-a-wire-boundary-decodes-independently convention
// (internal/agent/renderops.go's surfaceIDPattern doc comment already
// applies this once).
const (
	RenderIdleOutputBlack      = "black"
	RenderIdleOutputHold       = "hold"
	RenderIdleOutputDiagnostic = "diagnostic"
)

// RenderPayload is the payload of the showmesh.node.render/v1 schema,
// published RETAINED to a node's observed/render topic ([ObservedTopic],
// [ObservedDeliveryPolicy]): this node's supervised render pipeline health,
// per surface.
type RenderPayload struct {
	// GstLaunchPath is the resolved gst-launch-1.0 path this node is using
	// (from PATH, or SHOWMESH_GST_LAUNCH), or empty when unresolved.
	GstLaunchPath string `json:"gstLaunchPath"`

	// GstLaunchAvailable is false whenever the binary could not be located
	// at all; every surface's PipelineState is then
	// [RenderPipelineStateUnsupported], never a stale prior value, per this
	// project's absent-evidence-is-stated rule.
	GstLaunchAvailable bool `json:"gstLaunchAvailable"`

	// Surfaces is nil-safe: this package's own encoder emits "surfaces":null
	// for a nil slice and "surfaces":[] for an explicit empty one, matching
	// AssetInventoryPayload.Assets's identical rule — a node holding no
	// surface assignment reports "surfaces": [], never omits the key.
	Surfaces []RenderSurfaceReport `json:"surfaces"`

	// MultiSyncListening is true once this node's UDP 32320 MultiSync
	// listener has successfully bound and is running, false otherwise —
	// Track B finding 7's second half. Before this field existed, a bind
	// failure (port in use, permission, wrong interface) was ONLY a log
	// line: the timeline stayed StateUnknown forever, every surface's
	// frame writer drew idle output at full configured rate, and every
	// other reported field (PipelineState=="running", FramesWritten
	// climbing, FramesDropped==0) looked completely healthy. This field
	// is what makes that a stated degradation instead of a silent one.
	MultiSyncListening bool `json:"multiSyncListening"`

	// MultiSyncReason is the bind error (or "not yet attempted" before the
	// listener's first try) whenever MultiSyncListening is false. Not
	// enforced as required by Validate: this field was added after
	// SchemaNodeRenderV1 first shipped, and a hard requirement here would
	// reject every payload built before this field existed — including
	// this package's own existing fixtures in internal/coordinator, a
	// concurrently-developed package this fix must not collide with.
	// Publishers should always set it when MultiSyncListening is false
	// (see internal/agent/multisyncstatus.go); an empty reason on a
	// non-listening node is a real gap, just not one this wire boundary
	// can refuse without breaking additive compatibility (ADR-020).
	MultiSyncReason string `json:"multiSyncReason"`

	// MultiSyncObservedAt is the node's own clock at the moment the
	// MultiSync listener's status (MultiSyncListening/MultiSyncReason) was
	// last determined — a real bind attempt, success, or failure — never
	// the moment this report happens to publish. The coordinator's
	// noderender collector uses this as the observation's ObservedAt
	// (evidence time), keeping CollectedAt as its own receipt time,
	// exactly like RenderSurfaceReport.ObservedAt does one field up for
	// pipeline state. Zero (IsZero()) means genuinely never determined yet
	// (ADR-011: zero/nil is unknown, never defaulted to "now" or to a
	// healthy value) — not enforced as required by Validate, for the
	// identical additive-compatibility reason MultiSyncReason is not.
	MultiSyncObservedAt time.Time `json:"multiSyncObservedAt"`

	// FPPConnectListening is true once this node's FPP Connect HTTP
	// compatibility listener (ADR-044) has successfully bound and is
	// serving, false otherwise. It stays true while the listener is bound
	// but administratively disabled: the socket stays open (so the next
	// enable takes effect with no restart) and only the routes' behavior
	// changes, which FPPConnectReason states. Not enforced as required by
	// Validate, matching MultiSyncReason's identical additive-compatibility
	// reasoning: this field is added after SchemaNodeRenderV1 first shipped.
	FPPConnectListening bool `json:"fppConnectListening"`

	// FPPConnectReason is the bind error, the "not yet attempted" starting
	// value, or the disabled-by-configuration explanation, whenever
	// FPPConnectListening is false or the listener is bound but disabled.
	// See MultiSyncReason's identical rule one field up.
	FPPConnectReason string `json:"fppConnectReason"`

	// FPPConnectObservedAt is the node's own clock at the moment the FPP
	// Connect HTTP listener's status was last determined, mirroring
	// MultiSyncObservedAt's identical evidence-time reasoning one field up.
	FPPConnectObservedAt time.Time `json:"fppConnectObservedAt"`

	// FPPConnectHeld is nil-safe like Surfaces: this package's own encoder
	// emits "fppConnectHeld":[] for a nil slice, never null, matching
	// Surfaces' identical rule. Every file FC2's chunked upload receiver
	// (ADR-044) currently holds, bound or not, is here, up to
	// [maxRenderHeldFiles]: FPPConnectHeldCount states the true total
	// separately, so a publisher that must cut this list down (review
	// round 3 finding 2: an unbounded list here could otherwise ride
	// every render report past [maxEnvelopeSize] once enough files
	// accumulate) never has to also hide how many exist. This is the
	// only place an unbound held file is surfaced to an operator: ADR-044
	// decision 8 requires an unresolvable upload be "reported as an
	// unbound held file the operator can claim," and xLights never
	// inspects the playlist POST's status, so this node report is the
	// only evidence path available. Not enforced as required by Validate:
	// this field is added after SchemaNodeRenderV1 first shipped, matching
	// FPPConnectListening's identical additive-compatibility reasoning
	// (a hard requirement here would reject every render report a node
	// published before this field existed). The length cap IS enforced
	// (see Validate): that check passes trivially for a nil or short
	// slice, so it does not weaken the additive-compatibility promise
	// above, only refuses a payload that is genuinely too large to be
	// safe regardless of when the field was introduced.
	FPPConnectHeld []RenderFPPConnectHeldFile `json:"fppConnectHeld"`

	// FPPConnectHeldCount is the true total number of files this node
	// currently holds, independent of FPPConnectHeld's own length: the two
	// can differ once the publisher truncates the list to
	// [maxRenderHeldFiles], and a consumer that only needs "how many files
	// are held" should read this field rather than len(FPPConnectHeld).
	FPPConnectHeldCount int `json:"fppConnectHeldCount"`

	// FPPConnectHeldEvents is FC2's bounded evidence log (unknown and
	// ambiguous playlist posts, and refused uploads: too large, over the
	// asset-directory cap, disk full, an offset gap, an upload length that
	// changed mid-upload, an unsafe upload name, or a disallowed
	// directory), oldest first, up to [maxRenderHeldEvents]. ADR-044
	// decision 4 requires exceeding a bound, or exhausting the disk, be
	// "reported as evidence"; xLights never inspects any of these calls'
	// status, so this is that evidence's only path to an operator. Same
	// additive-compatibility, not-required-by-Validate treatment as
	// FPPConnectHeld, and the same reasoning for why its length cap is
	// enforced regardless.
	FPPConnectHeldEvents []RenderFPPConnectHeldEvent `json:"fppConnectHeldEvents"`
}

// RenderFPPConnectHeldFile is one file FC2's chunked upload receiver
// (internal/agent/fppconnectheld.go) currently holds, inside
// [RenderPayload.FPPConnectHeld]. Field-for-field the same evidence that
// package's own fppConnectHeldRecord carries, independently reproduced
// for this wire boundary per this codebase's standing convention.
type RenderFPPConnectHeldFile struct {
	// Dir is the accepted upload directory ("sequences", "music", or
	// "videos") this file was received into.
	Dir string `json:"dir"`

	// Name is the file name with its extension, exactly as xLights sent
	// it in Upload-Name (RES-003 section 10.6's join key). Never a
	// resolved or sanitized variant.
	Name string `json:"name"`

	SizeBytes int64 `json:"sizeBytes"`

	// ContentHash is "sha256:<hex>", matching ADR-028 decision 1's
	// identity scheme and AssetInventoryEntry.ContentHash's identical
	// shape.
	ContentHash string `json:"contentHash"`

	// ReceivedAt is when this node finished assembling and hashing this
	// file, on the node's own clock.
	ReceivedAt time.Time `json:"receivedAt"`

	// Bound is false for a held-but-unbound file (ADR-044 decision 8):
	// kept, registered nowhere, and visible here rather than guessed at
	// or silently dropped.
	Bound bool `json:"bound"`

	// Show is the ShowMesh show this file is bound to, empty when Bound
	// is false.
	Show string `json:"show,omitempty"`

	// LogicalSequence is the file name stem, set only when Bound is true.
	LogicalSequence string `json:"logicalSequence,omitempty"`

	// UnboundReason names which of ADR-039 decision 5's distinct
	// unresolved states produced Bound==false: never pushed an active
	// show, pushed an explicit "no active show," or an active show
	// pushed with an empty name. Empty whenever Bound is true.
	UnboundReason string `json:"unboundReason,omitempty"`
}

// RenderFPPConnectHeldEvent is one entry in FC2's bounded evidence log,
// inside [RenderPayload.FPPConnectHeldEvents]. Kind is an open vocabulary
// (this schema's standing "clients ignore what they don't know"
// convention, ADR-020), currently one of: "unknown" and "ambiguous" (a
// POST /api/playlist/{name} whose name matched no show, or matched more
// than one), and "too-large", "dir-full", "disk-full", "gap",
// "length-mismatch", "bad-name", and "bad-dir" (a refused upload chunk,
// ADR-044 decision 4).
type RenderFPPConnectHeldEvent struct {
	Kind string `json:"kind"`

	// Name is the playlist name for a "unknown"/"ambiguous" event, or the
	// attempted Upload-Name for a refused-upload event.
	Name string `json:"name"`

	// Dir is the attempted upload directory, set only for a refused-
	// upload event.
	Dir string `json:"dir,omitempty"`

	// Reason is the human-readable refusal text, set only for a refused-
	// upload event.
	Reason string `json:"reason,omitempty"`

	// Entries is the set of file names (sequenceName/mediaName) the
	// posted playlist body named, set only for "unknown"/"ambiguous", and
	// capped independently of this whole event log's own length (review
	// round 3 finding 2: a single POST /api/playlist/{name} body, up to 1
	// MiB with no per-entry count limit, could otherwise name tens of
	// thousands of distinct values and carry every one of them onto every
	// render report forever). EntriesTruncated states how many were cut.
	Entries []string `json:"entries,omitempty"`

	// EntriesTruncated is how many additional names the posted body
	// carried beyond what Entries kept, 0 when nothing was cut.
	EntriesTruncated int `json:"entriesTruncated,omitempty"`

	// MatchCount is how many times Name occurred in the node's show name
	// list, set only for "ambiguous".
	MatchCount int `json:"matchCount,omitempty"`

	At time.Time `json:"at"`
}

// Validate enforces: at most [maxRenderSurfaces] entries, every SurfaceID
// non-empty, Reason required whenever PipelineState is not "running",
// TransportReason required whenever TransportAvailable is false, and
// LastStderr bounded to [maxRenderStderrBytes] — all five exist so this
// payload can never exceed [maxEnvelopeSize] regardless of what a caller
// tries to put in it.
func (p RenderPayload) Validate() error {
	// p.Surfaces == nil covers BOTH an absent "surfaces" key and a literal
	// JSON null — encoding/json leaves a slice field nil in both cases, so
	// this is the only place that distinction can still be enforced (see
	// this field's own doc comment: this package's encoder always emits
	// "surfaces":[] for a node with none, never omits or nulls the key).
	// Accepting nil here would treat "no assertion was made" the same as
	// "this node affirmatively holds no surfaces", which is exactly the
	// absent-key-as-empty-value defect this project has shipped before —
	// see CLAUDE.md's recurring absent/null/empty lesson.
	if p.Surfaces == nil {
		return fmt.Errorf("%w: surfaces (a node reports \"surfaces\":[] when it holds none; the key must never be absent or null)", ErrPayloadMissingField)
	}
	if len(p.Surfaces) > maxRenderSurfaces {
		return fmt.Errorf("%w: %d surfaces, max %d", ErrPayloadTooLarge, len(p.Surfaces), maxRenderSurfaces)
	}
	for i, s := range p.Surfaces {
		if s.SurfaceID == "" {
			return fmt.Errorf("%w: surfaces[%d].surfaceId", ErrPayloadMissingField, i)
		}
		if s.PipelineState != RenderPipelineStateRunning && s.Reason == "" {
			return fmt.Errorf("%w: surfaces[%d].reason (required whenever pipelineState is not %q)",
				ErrPayloadMissingField, i, RenderPipelineStateRunning)
		}
		if s.TransportAvailable != nil && !*s.TransportAvailable && s.TransportReason == "" {
			return fmt.Errorf("%w: surfaces[%d].transportReason (required whenever transportAvailable is false)",
				ErrPayloadMissingField, i)
		}
		if len(s.LastStderr) > maxRenderStderrBytes {
			return fmt.Errorf("%w: surfaces[%d].lastStderr is %d bytes, max %d (must be truncated before publish, with %q appended)",
				ErrPayloadTooLarge, i, len(s.LastStderr), maxRenderStderrBytes, RenderStderrTruncatedSuffix)
		}
		if s.Drawing != "" && s.Drawing != RenderDrawingContent && s.Drawing != RenderDrawingIdle && s.Drawing != RenderDrawingFailure {
			return fmt.Errorf("%w: surfaces[%d].drawing %q must be %q, %q, %q, or empty",
				ErrPayloadInvalidDrawing, i, s.Drawing, RenderDrawingContent, RenderDrawingIdle, RenderDrawingFailure)
		}
		if s.Drawing == RenderDrawingIdle && s.IdleMode == "" {
			return fmt.Errorf("%w: surfaces[%d].idleMode (required whenever drawing is %q)",
				ErrPayloadMissingField, i, RenderDrawingIdle)
		}
		if s.Drawing == RenderDrawingFailure && s.FailureOutput == "" {
			return fmt.Errorf("%w: surfaces[%d].failureOutput (required whenever drawing is %q)",
				ErrPayloadMissingField, i, RenderDrawingFailure)
		}
		if s.FailureOutput != "" && s.FailureOutput != RenderFailureOutputAlert && s.FailureOutput != RenderFailureOutputBlack {
			return fmt.Errorf("%w: surfaces[%d].failureOutput %q must be %q, %q, or empty",
				ErrPayloadInvalidDrawing, i, s.FailureOutput, RenderFailureOutputAlert, RenderFailureOutputBlack)
		}
	}
	// These two length caps are enforced even though neither field is
	// required (see FPPConnectHeld's own doc comment): a nil or short
	// slice always passes trivially, so this never rejects a payload
	// built before either field existed, only one that is genuinely too
	// large to be safe regardless of when the field was introduced
	// (review round 3 finding 2).
	if len(p.FPPConnectHeld) > maxRenderHeldFiles {
		return fmt.Errorf("%w: %d fppConnectHeld entries, max %d", ErrPayloadTooLarge, len(p.FPPConnectHeld), maxRenderHeldFiles)
	}
	if len(p.FPPConnectHeldEvents) > maxRenderHeldEvents {
		return fmt.Errorf("%w: %d fppConnectHeldEvents entries, max %d", ErrPayloadTooLarge, len(p.FPPConnectHeldEvents), maxRenderHeldEvents)
	}
	return nil
}

// maxAudioRoutes bounds [AudioPayload.Routes], the same "an advertisement
// can't consume its own whole budget" cap [maxRenderSurfaces] enforces one
// payload over.
const maxAudioRoutes = 8

// AudioRouteReport is one candidate ALSA device's real probe outcome,
// inside [AudioPayload.Routes].
type AudioRouteReport struct {
	// Device is the ALSA PCM device name probed (e.g. "hw:CARD=PCH,DEV=0").
	// Never "null" or "default" — see internal/agent/audio.CandidateDevices.
	Device string `json:"device"`

	// Available is true only when a real pipeline reached PLAYING against
	// Device — never inferred from enumeration alone.
	Available bool `json:"available"`

	// Reason is required whenever Available is false.
	Reason string `json:"reason"`

	// Channels, Rate, and Format are what the pipeline actually negotiated,
	// never what was requested. Channels is a graph-level property only:
	// this cannot detect an interface that mirrors one physical pair from
	// another downstream of anything ALSA exposes (RES-007).
	Channels int64  `json:"channels"`
	Rate     int64  `json:"rate"`
	Format   string `json:"format"`
}

// maxAudioSessions bounds [AudioPayload.Sessions], the same "an
// advertisement can't consume its own whole budget" cap [maxAudioRoutes]
// enforces one field over.
const maxAudioSessions = 16

// AudioSessionReport is one session's retained telemetry (AUDIO-ENGINE
// section 15), inside [AudioPayload.Sessions]. Every Has*/*Known field
// distinguishes "not set" from its paired field's zero value — the wire
// form of internal/agent/audio.SessionSnapshot.
type AudioSessionReport struct {
	SessionID string `json:"sessionId"`

	HasSourceRole bool   `json:"hasSourceRole"`
	SourceRole    string `json:"sourceRole"`

	HasPlaylist      bool   `json:"hasPlaylist"`
	PlaylistRevision uint64 `json:"playlistRevision"`

	HasItem   bool   `json:"hasItem"`
	ItemID    string `json:"itemId"`
	ItemIndex int64  `json:"itemIndex"`

	// PositionKnown is false immediately after a discontinuity or when no
	// handle is loaded — never a stale position presented as current.
	PositionKnown bool  `json:"positionKnown"`
	PositionMs    int64 `json:"positionMs"`

	State           string `json:"state"`
	DesiredRevision uint64 `json:"desiredRevision"`

	HasGain    bool    `json:"hasGain"`
	Gain       float64 `json:"gain"`
	HasCeiling bool    `json:"hasCeiling"`
	Ceiling    float64 `json:"ceiling"`

	// FadeState is "none", "in_progress", or "complete" — a state rather
	// than a boolean because a boolean cannot distinguish "just
	// completed" from "never started".
	FadeState string `json:"fadeState"`

	Ducked   bool   `json:"ducked"`
	DuckedBy string `json:"duckedBy"`

	HasAssetProbe    bool   `json:"hasAssetProbe"`
	AssetProbeState  string `json:"assetProbeState"`
	AssetProbeReason string `json:"assetProbeReason"`

	// GapKnown, ItemGapMs, ItemGapReason, and ItemGapObservedAt are the
	// measured interval between the previous playlist item's natural
	// completion and this item's confirmed start
	// (docs/build/IDENTIFIER-REGISTER.md audio_session.item_gap_ms) —
	// never derived from a requested transition or a known duration.
	// ItemGapReason is required whenever GapKnown is false.
	// ItemGapObservedAt is the successor's own engine-clock evidence
	// time, non-nil only when GapKnown is true.
	GapKnown          bool       `json:"gapKnown"`
	ItemGapMs         int64      `json:"itemGapMs"`
	ItemGapReason     string     `json:"itemGapReason"`
	ItemGapObservedAt *time.Time `json:"itemGapObservedAt"`

	// Fault is "none" or one of the six named AUDIO-ENGINE section 11.4
	// fault classes; FaultReason is required whenever Fault != "none".
	Fault       string `json:"fault"`
	FaultReason string `json:"faultReason"`

	// ObservedAt is the engine's own evidence time for PositionMs, nil
	// when PositionKnown is false. Never the coordinator's or this
	// node's own receipt time (ADR-011).
	ObservedAt *time.Time `json:"observedAt"`

	// Stale is true when this session's fields above were NOT collected
	// during this report tick: the node could not acquire this session's
	// own lock in time (it was busy inside an in-flight engine call) and
	// is reporting its last known evidence instead. CollectedAt is when
	// that evidence actually WAS collected. Fault/FaultReason are never
	// repurposed to carry this: they keep answering "what is wrong with
	// the session"; Stale/CollectedAt answer "how old is this evidence".
	Stale bool `json:"stale"`

	// CollectedAt is when this session's own fields (State, Fault,
	// ItemID, Gain, ...) were captured on the node's clock — distinct
	// from ObservedAt, which is specifically PositionMs's own evidence
	// time and is nil whenever PositionKnown is false. Nil only for a
	// session that has never produced one successful snapshot.
	CollectedAt *time.Time `json:"collectedAt"`
}

// AudioPayload is the payload of the showmesh.node.audio/v1 schema,
// published RETAINED to a node's observed/audio topic ([ObservedTopic],
// [ObservedDeliveryPolicy]): this node's audio discovery evidence and,
// its current session telemetry.
type AudioPayload struct {
	// EngineAvailable is real evidence (a PLAYING transition against the
	// always-present ALSA "null" device) that this node's GStreamer/ALSA
	// element chain works, independent of whether real hardware is
	// attached: false means "this node has no audio engine".
	EngineAvailable bool `json:"engineAvailable"`

	// EngineReason is required whenever EngineAvailable is false.
	EngineReason string `json:"engineReason"`

	// HardwareEnumerated is true only when this node's own device and
	// hardware-card enumeration both completed without error. False makes
	// DeviceAvailable/ProgramAvailable/LTCAvailable mean "we do not know
	// yet", never "confirmed absent" — a shell-out failure (permissions,
	// a missing aplay binary, a transient error) must never be reported
	// the same way as a clean enumeration that genuinely found no card.
	HardwareEnumerated bool `json:"hardwareEnumerated"`

	// HardwareEnumeratedReason is required whenever HardwareEnumerated is
	// false, carrying the actual enumeration error text.
	HardwareEnumeratedReason string `json:"hardwareEnumeratedReason"`

	// DeviceAvailable is true when at least one real hardware candidate
	// (never "null"/"default") probed to PLAYING — the middle state: an
	// engine with no usable output. Only meaningful when HardwareEnumerated
	// is true.
	DeviceAvailable bool `json:"deviceAvailable"`

	// DeviceReason is required whenever DeviceAvailable is false.
	DeviceReason string `json:"deviceReason"`

	// OutputsCount is how many real hardware candidates probed to PLAYING.
	OutputsCount int64 `json:"outputsCount"`

	// ProgramAvailable is true when at least one real hardware route
	// achieved 1 or more channels — the program bus (AUDIO-ENGINE
	// section 6).
	ProgramAvailable bool `json:"programAvailable"`

	// ProgramReason is required whenever ProgramAvailable is false.
	ProgramReason string `json:"programReason"`

	// LTCAvailable is true when at least one real hardware route's
	// SEPARATE, explicitly-constrained probe achieved 3 or more channels
	// — evidence a discrete LTC channel (ADR-018) is physically reachable
	// on this node, never a claim that any specific route has been
	// assigned to carry it, and never inferred from an unconstrained
	// probe's own achieved channel count alone.
	LTCAvailable bool `json:"ltcAvailable"`

	// LTCReason is required whenever LTCAvailable is false.
	LTCReason string `json:"ltcReason"`

	// Routes is every probed real-hardware candidate's outcome, bounded to
	// maxAudioRoutes. Nil-unsafe like RenderPayload.Surfaces: this
	// package's own encoder emits "routes":[] for a node with none, never
	// omits or nulls the key.
	Routes []AudioRouteReport `json:"routes"`

	// Truncated is true when more real candidate devices were enumerated
	// than fit within maxAudioRoutes — stated, never a silent drop.
	Truncated bool `json:"truncated"`

	// EnumeratedCount is the total number of PCM device names this node's
	// enumerator reported, before virtual-name filtering or truncation.
	EnumeratedCount int64 `json:"enumeratedCount"`

	// DiscoveredAt is when this node actually ran its one-shot discovery
	// probes (EngineAvailable, HardwareEnumerated, DeviceAvailable,
	// ProgramAvailable, LTCAvailable, and Routes), on the node's own
	// clock — the evidence timestamp ADR-003 requires for THOSE fields.
	// It is reused unchanged on every report tick this agent process
	// publishes (see audioreport.go's own doc comment for why) and is
	// never refreshed to make discovery evidence look fresher than it
	// is. nil means genuinely unknown, matching pkg/observation.
	// Observation.ObservedAt's own nil-means-unknown convention
	// (ADR-011: never defaulted to "now").
	DiscoveredAt *time.Time `json:"discoveredAt"`

	// ObservedAt is when THIS report tick's live evidence (Sessions,
	// LTCGeneratorState/Reason, LTCFrameRate, LTCTimecode) was gathered,
	// on the node's own clock — refreshed on every tick, unlike
	// DiscoveredAt. Before this field existed it also stood in for the
	// discovery probe time, which pinned every session and LTC signal to
	// the agent's startup time forever and made them read stale after 45
	// seconds no matter how fresh the underlying data actually was. nil
	// means genuinely unknown, matching pkg/observation.Observation.
	// ObservedAt's own nil-means-unknown convention (ADR-011: never
	// defaulted to "now").
	ObservedAt *time.Time `json:"observedAt"`

	// Sessions is every audio session this node currently holds, rebuilt
	// fresh on every report tick (unlike the discovery fields above,
	// which are a one-shot cache — see audioreport.go's own doc comment).
	// Nil-unsafe like Routes: this package's own encoder emits
	// "sessions":[] for a node with none.
	Sessions []AudioSessionReport `json:"sessions"`

	// SessionsTruncated is true when more sessions existed than fit
	// within maxAudioSessions — stated, never a silent drop.
	SessionsTruncated bool `json:"sessionsTruncated"`

	// LTCGeneratorState and LTCGeneratorReason are the supervised
	// LTC generator's own reported lifecycle (node.audio.ltc.generator.state
	// / .reason) — never inferred from EngineAvailable, DeviceAvailable, or
	// any other field above: a generator can die while the rest of this
	// node's audio reports healthy, and reporting it any other way would
	// let silent timecode loss look exactly like a show between cues.
	// Always non-empty.
	LTCGeneratorState string `json:"ltcGeneratorState"`

	// LTCGeneratorReason is required whenever LTCGeneratorState is not
	// "running".
	LTCGeneratorReason string `json:"ltcGeneratorReason"`

	// LTCFrameRateKnown/LTCFrameRate report the closed-vocabulary rate
	// (node.audio.ltc.frame_rate) the currently- or most-recently-started
	// generator was started against. False whenever no generator has ever
	// been started on this node — never a plausible-looking default rate.
	LTCFrameRateKnown bool   `json:"ltcFrameRateKnown"`
	LTCFrameRate      string `json:"ltcFrameRate"`

	// LTCTimecodeKnown/LTCTimecode report the generator's own
	// self-reported current position (node.audio.ltc.timecode) — evidence
	// this node's supervisor read back from the generator process's own
	// heartbeat, never decoded off the generated audio (no decoder exists
	// anywhere in this seam) and never reported while the generator is not
	// confirmed running.
	LTCTimecodeKnown bool   `json:"ltcTimecodeKnown"`
	LTCTimecode      string `json:"ltcTimecode"`

	// EngineGlitchCountsKnown is true only when the bound engine backend
	// actually counts glitch-class bus conditions. False means "not
	// collected": every field below must read zero and
	// EngineGlitchCountsSince must be nil, never a fabricated healthy
	// zero (enforced by Validate).
	EngineGlitchCountsKnown bool `json:"engineGlitchCountsKnown"`

	// EngineGlitchCountsSince is when the currently bound engine instance
	// started counting. A rebind (audio.node.configure) swaps in a fresh
	// engine whose counts and Since both restart at zero/now; comparing
	// Since across ticks is how a consumer tells that reset apart from a
	// genuinely quiet period. nil exactly when EngineGlitchCountsKnown is
	// false.
	EngineGlitchCountsSince *time.Time `json:"engineGlitchCountsSince"`

	// EngineStreamWarningCount, EngineResourceWarningCount, and
	// EngineOtherWarningCount are cumulative GStreamer WARNING-class bus
	// messages since EngineGlitchCountsSince, bucketed by GError domain
	// (gstengine's own classifyWarningDomain). Not confirmed to identify
	// an ALSA xrun/underrun specifically -- see gstengine's watchBus doc
	// comment for what evidence this is and is not. Meaningful only when
	// EngineGlitchCountsKnown is true.
	EngineStreamWarningCount   uint64 `json:"engineStreamWarningCount"`
	EngineResourceWarningCount uint64 `json:"engineResourceWarningCount"`
	EngineOtherWarningCount    uint64 `json:"engineOtherWarningCount"`

	// EngineQosDropCount is the cumulative count of QOS-class bus
	// messages since EngineGlitchCountsSince: a downstream element
	// (typically the sink, which the engine sets "qos" true on)
	// reporting it dropped or skipped a buffer to keep pace with the
	// clock. Meaningful only when EngineGlitchCountsKnown is true.
	EngineQosDropCount uint64 `json:"engineQosDropCount"`
}

// Validate enforces: at most maxAudioRoutes entries, every route's Device
// non-empty, ObservedAt present, every Reason field required wherever
// this payload's own doc comments say so, and the same for Sessions —
// the same shape RenderPayload.Validate enforces one type up.
func (p AudioPayload) Validate() error {
	if p.Routes == nil {
		return fmt.Errorf("%w: routes (a node reports \"routes\":[] when it holds none; the key must never be absent or null)", ErrPayloadMissingField)
	}
	if len(p.Routes) > maxAudioRoutes {
		return fmt.Errorf("%w: %d routes, max %d", ErrPayloadTooLarge, len(p.Routes), maxAudioRoutes)
	}
	if p.Sessions == nil {
		return fmt.Errorf("%w: sessions (a node reports \"sessions\":[] when it holds none; the key must never be absent or null)", ErrPayloadMissingField)
	}
	if len(p.Sessions) > maxAudioSessions {
		return fmt.Errorf("%w: %d sessions, max %d", ErrPayloadTooLarge, len(p.Sessions), maxAudioSessions)
	}
	for i, sess := range p.Sessions {
		if sess.SessionID == "" {
			return fmt.Errorf("%w: sessions[%d].sessionId", ErrPayloadMissingField, i)
		}
		if sess.Fault != "" && sess.Fault != "none" && sess.FaultReason == "" {
			return fmt.Errorf("%w: sessions[%d].faultReason (required whenever fault is not \"none\")", ErrPayloadMissingField, i)
		}
	}
	if p.ObservedAt == nil {
		return fmt.Errorf("%w: observedAt", ErrPayloadMissingField)
	}
	if !p.EngineAvailable && p.EngineReason == "" {
		return fmt.Errorf("%w: engineReason (required whenever engineAvailable is false)", ErrPayloadMissingField)
	}
	if !p.HardwareEnumerated && p.HardwareEnumeratedReason == "" {
		return fmt.Errorf("%w: hardwareEnumeratedReason (required whenever hardwareEnumerated is false)", ErrPayloadMissingField)
	}
	if !p.DeviceAvailable && p.DeviceReason == "" {
		return fmt.Errorf("%w: deviceReason (required whenever deviceAvailable is false)", ErrPayloadMissingField)
	}
	if !p.ProgramAvailable && p.ProgramReason == "" {
		return fmt.Errorf("%w: programReason (required whenever programAvailable is false)", ErrPayloadMissingField)
	}
	if !p.LTCAvailable && p.LTCReason == "" {
		return fmt.Errorf("%w: ltcReason (required whenever ltcAvailable is false)", ErrPayloadMissingField)
	}
	for i, r := range p.Routes {
		if r.Device == "" {
			return fmt.Errorf("%w: routes[%d].device", ErrPayloadMissingField, i)
		}
		if !r.Available && r.Reason == "" {
			return fmt.Errorf("%w: routes[%d].reason (required whenever available is false)", ErrPayloadMissingField, i)
		}
	}
	if p.LTCGeneratorState == "" {
		return fmt.Errorf("%w: ltcGeneratorState", ErrPayloadMissingField)
	}
	if p.LTCGeneratorState != "running" && p.LTCGeneratorReason == "" {
		return fmt.Errorf("%w: ltcGeneratorReason (required whenever ltcGeneratorState is not \"running\")", ErrPayloadMissingField)
	}
	if p.LTCFrameRateKnown && p.LTCFrameRate == "" {
		return fmt.Errorf("%w: ltcFrameRate (required whenever ltcFrameRateKnown is true)", ErrPayloadMissingField)
	}
	if p.LTCTimecodeKnown && p.LTCTimecode == "" {
		return fmt.Errorf("%w: ltcTimecode (required whenever ltcTimecodeKnown is true)", ErrPayloadMissingField)
	}
	if p.EngineGlitchCountsKnown {
		if p.EngineGlitchCountsSince == nil {
			return fmt.Errorf("%w: engineGlitchCountsSince (required whenever engineGlitchCountsKnown is true)", ErrPayloadMissingField)
		}
	} else if p.EngineGlitchCountsSince != nil || p.EngineStreamWarningCount != 0 || p.EngineResourceWarningCount != 0 ||
		p.EngineOtherWarningCount != 0 || p.EngineQosDropCount != 0 {
		return fmt.Errorf("%w: engine glitch counts/since must be zero/nil when engineGlitchCountsKnown is false", ErrPayloadInconsistentField)
	}
	return nil
}

// ErrPayloadInvalidDrawing is wrapped by [RenderPayload.Validate] when a
// surface's Drawing or FailureOutput is set to a value outside its own
// closed vocabulary, matching [ErrPayloadInvalidOutcome]'s identical
// closed-vocabulary role for [ResultPayload].
var ErrPayloadInvalidDrawing = errors.New("mqttproto: drawing is not a recognized value")

// ErrPayloadInconsistentField is wrapped by [AudioPayload.Validate] when a
// field that means "not collected" (e.g. engineGlitchCountsKnown false)
// is paired with a nonzero/non-nil value the collected-evidence fields
// only mean anything under -- the reverse direction of
// [ErrPayloadMissingField], which catches a required field left empty.
var ErrPayloadInconsistentField = errors.New("mqttproto: payload field is inconsistent with its own known/collected flag")

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

// DecodeAssetInventoryPayload decodes env.Payload as an
// [AssetInventoryPayload]. It returns an [*UnsupportedSchemaError] if
// env.Schema is not [SchemaNodeAssetInventoryV1], an error wrapping
// [ErrPayloadEmpty] if env.Payload is empty or null, and an error wrapping
// [ErrPayloadMissingField] (via [AssetInventoryPayload.Validate]) if Reason
// is missing while Complete is false.
func DecodeAssetInventoryPayload(env Envelope) (AssetInventoryPayload, error) {
	if env.Schema != SchemaNodeAssetInventoryV1 {
		return AssetInventoryPayload{}, &UnsupportedSchemaError{Got: env.Schema, Want: SchemaNodeAssetInventoryV1}
	}
	if err := checkPayloadPresent(env.Payload); err != nil {
		return AssetInventoryPayload{}, fmt.Errorf("mqttproto: decode asset inventory payload: %w", err)
	}
	var p AssetInventoryPayload
	if err := json.Unmarshal(env.Payload, &p); err != nil {
		return AssetInventoryPayload{}, fmt.Errorf("mqttproto: decode asset inventory payload: %w", err)
	}
	if err := p.Validate(); err != nil {
		return AssetInventoryPayload{}, fmt.Errorf("mqttproto: decode asset inventory payload: %w", err)
	}
	return p, nil
}

// DecodeRenderPayload decodes env.Payload as a [RenderPayload]. It returns
// an [*UnsupportedSchemaError] if env.Schema is not [SchemaNodeRenderV1], an
// error wrapping [ErrPayloadEmpty] if env.Payload is empty or null, and an
// error wrapping [ErrPayloadMissingField] or [ErrPayloadTooLarge] (via
// [RenderPayload.Validate]) if the payload is malformed.
func DecodeRenderPayload(env Envelope) (RenderPayload, error) {
	if env.Schema != SchemaNodeRenderV1 {
		return RenderPayload{}, &UnsupportedSchemaError{Got: env.Schema, Want: SchemaNodeRenderV1}
	}
	if err := checkPayloadPresent(env.Payload); err != nil {
		return RenderPayload{}, fmt.Errorf("mqttproto: decode render payload: %w", err)
	}
	var p RenderPayload
	if err := json.Unmarshal(env.Payload, &p); err != nil {
		return RenderPayload{}, fmt.Errorf("mqttproto: decode render payload: %w", err)
	}
	if err := p.Validate(); err != nil {
		return RenderPayload{}, fmt.Errorf("mqttproto: decode render payload: %w", err)
	}
	return p, nil
}

// DecodeAudioPayload decodes env.Payload as an [AudioPayload]. It returns
// an [*UnsupportedSchemaError] if env.Schema is not [SchemaNodeAudioV1], an
// error wrapping [ErrPayloadEmpty] if env.Payload is empty or null, and an
// error wrapping [ErrPayloadMissingField] or [ErrPayloadTooLarge] (via
// [AudioPayload.Validate]) if the payload is malformed.
func DecodeAudioPayload(env Envelope) (AudioPayload, error) {
	if env.Schema != SchemaNodeAudioV1 {
		return AudioPayload{}, &UnsupportedSchemaError{Got: env.Schema, Want: SchemaNodeAudioV1}
	}
	if err := checkPayloadPresent(env.Payload); err != nil {
		return AudioPayload{}, fmt.Errorf("mqttproto: decode audio payload: %w", err)
	}
	var p AudioPayload
	if err := json.Unmarshal(env.Payload, &p); err != nil {
		return AudioPayload{}, fmt.Errorf("mqttproto: decode audio payload: %w", err)
	}
	if err := p.Validate(); err != nil {
		return AudioPayload{}, fmt.Errorf("mqttproto: decode audio payload: %w", err)
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

// NewAssetInventoryEnvelope builds a complete, schema-tagged [Envelope]
// carrying payload for nodeID, stamping MessageID and SentAt (see
// [newEnvelope] and [NewHelloEnvelope]'s doc comment on the uniform nodeID
// argument).
func NewAssetInventoryEnvelope(now func() time.Time, nodeID string, payload AssetInventoryPayload) (Envelope, error) {
	return newEnvelope(now, SchemaNodeAssetInventoryV1, nodeID, payload)
}

// NewRenderEnvelope builds a complete, schema-tagged [Envelope] carrying
// payload for nodeID, stamping MessageID and SentAt (see [newEnvelope] and
// [NewHelloEnvelope]'s doc comment on the uniform nodeID argument).
//
// Unlike newEnvelope's other callers, this constructor calls
// payload.Validate() itself before marshalling: newEnvelope never does, so a
// caller-built payload that DecodeRenderPayload would refuse (e.g. a
// non-running state with an empty reason) would otherwise be published
// as-is and only fail on the receiving end, silently and per-message. The
// producer must not emit what its own decoder rejects.
func NewRenderEnvelope(now func() time.Time, nodeID string, payload RenderPayload) (Envelope, error) {
	if err := payload.Validate(); err != nil {
		return Envelope{}, fmt.Errorf("mqttproto: build render envelope: %w", err)
	}
	return newEnvelope(now, SchemaNodeRenderV1, nodeID, payload)
}

// NewAudioEnvelope builds a complete, schema-tagged [Envelope] carrying
// payload for nodeID, stamping MessageID and SentAt (see [newEnvelope] and
// [NewHelloEnvelope]'s doc comment on the uniform nodeID argument).
//
// Like [NewRenderEnvelope], this constructor calls payload.Validate()
// itself before marshalling, so a caller-built payload
// [DecodeAudioPayload] would refuse is never published as-is.
func NewAudioEnvelope(now func() time.Time, nodeID string, payload AudioPayload) (Envelope, error) {
	if err := payload.Validate(); err != nil {
		return Envelope{}, fmt.Errorf("mqttproto: build audio envelope: %w", err)
	}
	return newEnvelope(now, SchemaNodeAudioV1, nodeID, payload)
}
