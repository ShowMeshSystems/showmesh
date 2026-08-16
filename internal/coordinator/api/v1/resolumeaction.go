package v1

// This file is Track D seam D-3/B's own wire contract: the seven-action
// Resolume vocabulary TRACK-D-D3-SPEC.md section 2 fixes (launchClip,
// clearLayer, blackout, launchColumn, selectDeck, setLayerBypass,
// setLayerMaster), each dispatched over POST /resolume/actions and
// reported honestly against the outcome vocabulary that spec's section 4
// and section 7 fix: "confirmed", "unconfirmed", "unconfirmable",
// "refused", "failed" — never a bare 200 read as success (ADR-003), and
// never rendered as an HTTP error (ADR-029: "an action whose effect cannot
// be observed reports as unconfirmable... never as success").
//
// Shaped deliberately close to FPPCommandRequest/FPPCommandResponse
// (commands.go, Step 7/8) — the same idempotency, replay, and audit-
// degradation story applies to a second vendor's command surface, and a
// second, differently-shaped contract for the identical property would be
// an inconsistency with no reason behind it.

// ResolumeActionParam describes one named parameter one action's "params"
// object accepts — rendered by GET /resolume/actions so a caller never has
// to hardcode the vocabulary out of band. kind is "string" or "bool";
// every parameter in this vocabulary is required (spec section 2's seven
// actions take either an object reference, a boolean value, or nothing —
// none takes an optional parameter with a default), so this type carries
// no default value.
type ResolumeActionParam struct {
	Name     string `json:"name"`
	Kind     string `json:"kind"`
	Required bool   `json:"required"`
}

// ResolumeAction is one entry of the fixed seven-action vocabulary.
// AuditExempt mirrors ADR-024 decision 11's safety class (spec section
// 5.2): true only for blackout and clearLayer, false for the other five —
// an audit-write failure refuses every non-exempt action before dispatch
// and never refuses an exempt one. CoordinatorRequired is true for every
// entry today (spec section 5.3: the Resolume adapter is coordinator-
// hosted and holds no local fallback) and is carried on the wire, not
// merely documented, so a macro author never has to know it out of band.
type ResolumeAction struct {
	Name                string                `json:"name"`
	Params              []ResolumeActionParam `json:"params"`
	AuditExempt         bool                  `json:"auditExempt"`
	CoordinatorRequired bool                  `json:"coordinatorRequired"`
}

// ResolumeActionsResponse is the body of GET /resolume/actions.
type ResolumeActionsResponse struct {
	ServerTime string           `json:"serverTime"`
	Actions    []ResolumeAction `json:"actions"`
}

// ResolumeActionRequest is the body of POST /resolume/actions.
// IdempotencyKey is required on every request (ARCHITECTURE section 8.1),
// scoped to (action, normalized params) exactly like FPPCommandRequest's
// own idempotencyKey (commands.go): reusing a key against the SAME action
// and the SAME normalized params is a replay; reusing it against a
// DIFFERENT action or DIFFERENT params is a 409 conflict, refused
// outright. Params is this action's own parameter object — absent (or
// {}) only for blackout, which takes none; every other action requires
// it, and an absent/null/empty distinction identical to
// FPPCommandRequest's is enforced server-side (an explicit null is never
// the same as an omitted field).
type ResolumeActionRequest struct {
	Action         string         `json:"action"`
	IdempotencyKey string         `json:"idempotencyKey"`
	Params         map[string]any `json:"params,omitempty"`
}

// ResolumeActionResponse is the body of a successful (200) response from
// POST /resolume/actions.
type ResolumeActionResponse struct {
	ServerTime string               `json:"serverTime"`
	Result     ResolumeActionResult `json:"result"`
}

// ResolumeActionResult is what happened to one dispatched (or replayed)
// action. Outcome is exactly one of "confirmed", "unconfirmed",
// "unconfirmable", "refused", or "failed" (TRACK-D-D3-SPEC.md sections 3
// and 4) — never inferred from the HTTP status, which is 200 for every one
// of these five: a 200 reports "this coordinator answered honestly about
// what happened," not "the action succeeded." OutcomeReason is a short,
// human-readable explanation and is non-empty for every resolved result,
// including "confirmed" — exactly as FPPCommandResult.OutcomeReason
// (commands.go) already documents for its own two-value outcome, widened
// here to five.
//
// Outcome (and, identically, OutcomeReason) may ALSO be "" — a sixth,
// narrow, accepted value — for a REPLAY response answered before the
// original request's own dispatch/confirmation has finished
// (api/openapi.yaml's own enum documents this, matching
// FPPCommandResult.Outcome's identical case exactly). A coordinator
// restart cannot make this permanent: ReconcileStrandedResolumeActions
// (resolumeaction_reconcile.go) resolves any Resolume command row still
// unresolved at startup before it can ever reach a client in that state.
type ResolumeActionResult struct {
	ID             string         `json:"id"`
	IdempotencyKey string         `json:"idempotencyKey"`
	Action         string         `json:"action"`
	Params         map[string]any `json:"params"`

	// Replay is true when this response answers a REPLAYED idempotency
	// key: nothing was dispatched by this request — see
	// FPPCommandResult.Replay's identical doc comment (commands.go) for the
	// ADR-024 decision 11 reasoning ("a replay is precisely the case an
	// investigator wants to see").
	Replay bool `json:"replay"`

	Outcome       string `json:"outcome"`
	OutcomeReason string `json:"outcomeReason"`

	// AttributionDegraded is true when this action's dispatch or outcome
	// audit entry could not be written — ADR-024 decision 11's blackout/
	// clearLayer safety-class exemption proceeding anyway with a degraded,
	// stderr-only attribution record. Every other action fails closed
	// instead (503, nothing dispatched) and never reaches this field set
	// true on its own dispatch-side write; a non-exempt action's own
	// post-dispatch OUTCOME entry can still degrade this way, mirroring
	// FPPCommandResult.AttributionDegraded exactly.
	AttributionDegraded bool `json:"attributionDegraded"`

	// DispatchedAt and ResolvedAt are RFC 3339 timestamps, or null — null
	// only when this action was refused before dispatch was ever attempted
	// (a deck mismatch, an unknown composition identity, a fail-closed
	// audit refusal) or, for a replay, in the same narrow accepted race
	// FPPCommandResult documents for its own identical fields.
	DispatchedAt *string `json:"dispatchedAt"`
	ResolvedAt   *string `json:"resolvedAt"`

	// ResolvedID is the Resolume object id this action's own name
	// reference resolved to, kept visible for debugging (ADR-037's own
	// Consequences: the reference an operator types is a name, never an
	// id, but the id "stays visible for debugging"). Omitted entirely for
	// blackout, which addresses nothing, and for a refusal reached before
	// any name was resolved.
	ResolvedID string `json:"resolvedId,omitempty"`

	// SelectedDeckChanged is TRACK-D-ADAPTER-SPEC.md §3.8's own evidence
	// field: whether the selected deck changed between this action's
	// decision and its confirmation. Meaningful only for a confirmed
	// launchClip (the only action that can race a deck — layers are
	// deck-independent). ALWAYS present, never omitted (ADR-020: absent
	// evidence is stated, never omitted) — null, never false, both when
	// the deck could not be read at confirmation time and for every
	// action other than a confirmed launchClip. Evidence, never a
	// refusal (ADR-024 decision 11's fail-closed inversion, avoided here
	// on purpose).
	SelectedDeckChanged *bool `json:"selectedDeckChanged"`
}
