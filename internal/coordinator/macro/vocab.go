package macro

// This file collects the small, closed vocabularies this package writes
// into [store.MacroRunStepRecord]'s untyped string columns. Every one of
// those columns is documented as "not validated by this package" at the
// storage layer (Wave 1a's own precedent, matching commands.go's identical
// treatment of CommandRecord.State/Outcome) — the enforcement is here, at
// the one place that writes them, not at the store.

// Step State values (store.MacroRunStepRecord.State): the step's own
// dispatch lifecycle, mirroring commands.go's CommandRecord.State
// ("dispatched"/"resolved") one level up, with two additions this
// package's own control flow needs: "pending" (never touched yet) and
// "skipped" (an abort left this step never attempted).
const (
	stepStatePending    = "pending"
	stepStateDispatched = "dispatched"
	stepStateResolved   = "resolved"
	stepStateSkipped    = "skipped"
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
)

// mqttResponseQoS is the QoS this package subscribes at when waiting for
// an mqtt action's response. 1 (at-least-once) matches the QoS this
// project already uses for command-shaped MQTT traffic elsewhere
// (internal/agent/mqtt.go's own precedent) — a response we might act on
// (deciding a macro step confirmed or failed) should not be delivered
// at-most-once.
const mqttResponseQoS = 1
