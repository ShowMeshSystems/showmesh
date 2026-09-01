package macro

// This file collects the small, closed vocabularies this package writes
// into [store.MacroRunStepRecord]'s untyped string columns. Every one of
// those columns is documented as "not validated by this package" at the
// storage layer (Wave 1a's own precedent, matching commands.go's identical
// treatment of CommandRecord.State/Outcome) — the enforcement is here, at
// the one place that writes them, not at the store.

// Step State values (store.MacroRunStepRecord.State): the step's own
// dispatch lifecycle. Three members, not the four commands.go's own
// CommandRecord.State pattern might suggest: "pending" (never touched
// yet), "resolved" (an outcome was recorded), and "skipped" (an abort
// left this step never attempted). There is no "dispatched" intermediate
// — persistStepOutcome's own comment (run.go) states why: dispatchStep
// always returns a terminal result before this package writes anything,
// so the first write a step ever gets IS its resolved state. A fourth
// constant for "dispatched" existed here through wave 2 and wave 3 with
// no caller and was removed by review (api/openapi.yaml published it as
// a permanent enum member nothing produced, which ADR-020's "a client
// ignores what it does not recognize" rule does not excuse — the
// producer side must not publish a value it cannot emit).
const (
	stepStatePending  = "pending"
	stepStateResolved = "resolved"
	stepStateSkipped  = "skipped"
)

// Step Outcome values (store.MacroRunStepRecord.Outcome): STEP-9-SPEC.md
// section 6.4's exact five-member vocabulary. [store.MacroRunStepRecord]'s
// own doc comment already states this list; these constants are what this
// package's own code writes rather than five repeated string literals.
const (
	outcomeConfirmed     = "confirmed"
	outcomeUnconfirmed   = "unconfirmed"
	outcomeUnconfirmable = "unconfirmable"
	outcomeFailed        = "failed"
	outcomeSkipped       = "skipped"
)

// OutcomeState values this package itself produces for an MQTT step
// (store.MacroRunStepRecord.OutcomeState). An FPP step never uses these —
// it carries whatever [api.FPPCommandOutcome.OutcomeState] already
// resolved through the existing dispatch seam's own evidence vocabulary
// (pkg/observation.State), unchanged, since re-deriving that classification
// a second time in this package would be exactly the "a shared rule is
// only shared where it is called" defect LESSONS.md already names.
const (
	mqttStateConfirmed           = "confirmed"
	mqttStateNegativeAnswer      = "negative_answer"
	mqttStateMalformedPayload    = "malformed_payload"
	mqttStateDeadlineExceeded    = "deadline_exceeded"
	mqttStateUnconfirmableByKind = "unconfirmable_declared"
	mqttStateUnknownBroker       = "unknown_broker"
	mqttStateTransportError      = "transport_error"
	mqttStateAuditUnavailable    = "audit_unavailable"
	mqttStatePublishFailed       = "publish_failed"

	// mqttStateRestartInterrupted is produced ONLY by the startup
	// reconciler (run.go's Reconcile, via reconcile_step.go), never by
	// ordinary dispatch (step_mqtt.go). It marks the one case an MQTT step
	// has no commands-table row to fall back on for: a dispatch audit
	// entry exists for this step's own idempotency key (so this
	// coordinator DID begin dispatching it before the prior process
	// stopped existing), but nothing records whether the publish itself
	// reached the broker or a response arrived before the crash. Neither
	// "confirmed" nor "skipped" is honest here — see reconcile_step.go's
	// own doc comment.
	mqttStateRestartInterrupted = "restart_interrupted"
)

// OutcomeState values this package itself produces for a Resolume step
// (store.MacroRunStepRecord.OutcomeState): a rename of D-3/A's own
// five-value ActionOutcomeState vocabulary, mirroring mqttState* above.
const (
	resolumeStateConfirmed     = "confirmed"
	resolumeStateUnconfirmed   = "unconfirmed"
	resolumeStateUnconfirmable = "unconfirmable"
	resolumeStateRefused       = "refused"
	resolumeStateFailed        = "failed"

	// resolumeStateNotConfigured is produced only when no live Resolume
	// instance is configured on this coordinator at all
	// (Dependencies.ResolumeActions is nil) — distinct from
	// resolumeStateRefused, which means Dispatch itself ran and refused.
	resolumeStateNotConfigured = "not_configured"

	// resolumeStateRestartInterrupted mirrors mqttStateRestartInterrupted's
	// identical meaning one section up, produced only by the startup
	// reconciler (reconcile_step.go): a dispatch audit entry exists for
	// this step's own idempotency key, so dispatch began before the prior
	// process stopped existing, but nothing records whether it reached
	// Resolume or resolved before the crash.
	resolumeStateRestartInterrupted = "restart_interrupted"
)

// OutcomeState values this package itself produces for an audio step
// (store.MacroRunStepRecord.OutcomeState). A successfully dispatched step's
// own state is the raw [pkgaudio.Outcome] wire word (e.g. "started",
// "completed", "refused") — the node's own vocabulary, carried through
// unchanged rather than re-collapsed into the five-member outcome above, so
// an operator reading a run's steps can tell a confirmed "started" from a
// confirmed "completed". audioStateNotConfigured/audioStateInternalError
// are this package's own local refusals, mirroring resolumeStateNotConfigured/
// step_fpp.go's "internal_error" one section up.
const (
	audioStateNotConfigured = "not_configured"
	audioStateInternalError = "internal_error"
)

// mqttResponseQoS is the QoS this package subscribes at when waiting for
// an mqtt action's response. 1 (at-least-once) matches the QoS this
// project already uses for command-shaped MQTT traffic elsewhere
// (internal/agent/mqtt.go's own precedent) — a response we might act on
// (deciding a macro step confirmed or failed) should not be delivered
// at-most-once.
const mqttResponseQoS = 1
