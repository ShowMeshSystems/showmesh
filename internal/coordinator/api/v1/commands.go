package v1

// FPPCommandRequest is the body of
// POST /api/v1/fpp/{instanceId}/commands, Step 7 seam C's first write
// endpoint. Action is checked against a fixed, small vocabulary at the
// handler ("stopPlaylist" is the only member today; see
// internal/coordinator/api/fppcommand_handler.go) — carried in the body,
// not implied solely by the URL, so a second primitive command added by a
// later step is an additive change to this same endpoint rather than a
// second route. IdempotencyKey is required on every request: ARCHITECTURE
// section 8.1 requires an idempotency key on every command, not on some
// of them, and RES-015 section 7.3 established that FPP supplies nothing
// to derive one from, so the caller (showmeshctl, the Operator UI) mints
// it — see pkg/command.NewIdempotencyKey.
//
// IdempotencyKey's uniqueness is scoped to (Action, the instanceId path
// value), never global (Step 7 seam C review defect 6): reusing a key
// against the SAME action and instanceId is a replay (nothing is
// dispatched again; the original result is returned, flagged
// "replay": true); reusing it against a DIFFERENT action or a DIFFERENT
// instanceId is a 409 conflict, refused outright — see
// fppcommand_handler.go's handleFPPCommandReplay. Before this fix this
// constraint existed only as a bare database-level UNIQUE on the key
// column alone, undocumented anywhere an integrator could find it; see
// api/openapi.yaml's identical documentation on this same field.
type FPPCommandRequest struct {
	Action         string `json:"action"`
	IdempotencyKey string `json:"idempotencyKey"`
}

// FPPCommandResponse is the body of a successful (200) response from
// POST /api/v1/fpp/{instanceId}/commands. See [NodeResponse]'s doc
// comment for why ServerTime is present with no exception.
type FPPCommandResponse struct {
	ServerTime string           `json:"serverTime"`
	Command    FPPCommandResult `json:"command"`
}

// FPPCommandResult is what happened to one dispatched (or replayed)
// command. Outcome is never "successful" on a bare 200 from FPP (ADR-003):
// it is exactly "confirmed" or "unconfirmed", per this step's own
// decision record, and OutcomeState/OutcomeReason always carry ADR-020's
// absent-evidence vocabulary and a human reason — present and non-empty
// whenever Outcome is not "confirmed", per contract section 3.3's rule
// applied to a command's own outcome rather than to an observation.
type FPPCommandResult struct {
	ID             string `json:"id"`
	IdempotencyKey string `json:"idempotencyKey"`
	Action         string `json:"action"`
	InstanceID     string `json:"instanceId"`

	// Replay is true when this response answers a REPLAYED idempotency
	// key: the command described here was NOT dispatched by this request
	// — it is the ORIGINAL command's result, returned per ADR-024
	// decision 11 ("a replay is precisely the case an investigator wants
	// to see, because it means the operator did not get their response").
	Replay bool `json:"replay"`

	// Outcome is "confirmed", "unconfirmed", or empty. Empty is a real,
	// honest value — not a bug — for the narrow race where a REPLAY
	// request returns the original command's row before that original
	// request's own dispatch/confirmation has finished; see
	// fppcommand_handler.go's doc comment on why this is accepted rather
	// than papered over.
	Outcome string `json:"outcome"`

	// OutcomeState carries pkg/observation's six-value evidence-state
	// vocabulary — the exact state of the evidence this command's outcome
	// was decided from (e.g. "current" for a fresh, successfully-read
	// observation that simply did not yet show the wanted value;
	// "collection_failed" when the collector itself could not reach FPP
	// during confirmation). Matches store.CommandRecord.OutcomeState and
	// audit_log.outcome_state's own documented convention (schemaV5/V6):
	// this is the SAME vocabulary the audit trail's own outcome entries
	// use, not a second one invented for the wire. api/openapi.yaml
	// constrains this to that six-value enum (Step 7 seam C review defect
	// 5): NEVER blank for a resolved command — a coordinator restart that
	// leaves a command dispatched-but-unresolved is caught and resolved by
	// a startup reconciliation sweep before this field can ever reach a
	// client empty (see internal/coordinator/api/fppcommand_reconcile.go).
	OutcomeState string `json:"outcomeState"`

	// OutcomeReason is a short, human-readable explanation. Non-empty
	// whenever Outcome is "confirmed" or "unconfirmed" — i.e. whenever
	// this command has actually resolved. This was previously documented
	// as "non-empty whenever Outcome is not confirmed", which was false on
	// the one path that could leave Outcome AND OutcomeReason both blank
	// (a coordinator-restart-stranded command): that path is now closed
	// (see OutcomeState's own comment), so the only case OutcomeReason may
	// still be empty is the identical narrow race Outcome's own comment
	// names — a REPLAY response returned before the original request's own
	// dispatch/confirmation has finished.
	OutcomeReason string `json:"outcomeReason"`

	// AttributionDegraded is true when this command's dispatch or outcome
	// audit entry could not be written (ADR-024 decision 11's blackout/
	// stop/power-off safety-class exemption: Stop Playlist proceeds
	// regardless, with a degraded attribution record written to stderr
	// instead of the audit log). Stated honestly on the wire rather than
	// silently absorbed, because an operator or later investigator
	// deserves to know the command that ran has no normal audit trail —
	// see fppcommand_handler.go.
	AttributionDegraded bool `json:"attributionDegraded"`

	// DispatchedAt and ResolvedAt are RFC 3339 timestamps, or null —
	// DispatchedAt is null only if dispatch itself could not be attempted
	// (e.g. the configured FPP endpoint URL could not be turned into a
	// request at all; see internal/coordinator/fppcommand.New's own
	// validation) — a real, honest value, not a bug, distinct from every
	// other outcome, all of which DID attempt dispatch (Step 7 seam C
	// review defect 9: this field used to be stamped unconditionally even
	// on that first case). ResolvedAt is null only if this response was
	// itself abandoned before its own bookkeeping finished, which this
	// handler's post-dispatch writes no longer inherit the client's
	// cancellation for (defect 4) — in practice this means never, for a
	// fresh dispatch; a REPLAY response can still report both as null, in
	// the narrow accepted race documented on Outcome above.
	DispatchedAt *string `json:"dispatchedAt"`
	ResolvedAt   *string `json:"resolvedAt"`
}
