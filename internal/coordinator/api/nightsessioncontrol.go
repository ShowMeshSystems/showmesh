package api

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"time"

	"github.com/google/uuid"

	v1 "github.com/showmeshsystems/showmesh/internal/coordinator/api/v1"
	"github.com/showmeshsystems/showmesh/internal/coordinator/config"
	"github.com/showmeshsystems/showmesh/internal/coordinator/identity"
	"github.com/showmeshsystems/showmesh/internal/coordinator/store"
	"github.com/showmeshsystems/showmesh/pkg/observation"
)

// Track F seam F2: the persisted night-session lifecycle controller
// (RESTING-MODE.md §3/§4/§11, ADR-038). transition-to-show, live, and
// transition-to-resting are modeled and persisted, but nothing here
// advances a session out of transition-to-show or transition-to-resting -
// that needs FPP playback evidence and cue dispatch (seams F3/F4).
//
// No wall-clock scheduling anywhere: every deadline check is relative to
// h.now().

// Night lifecycle states - RESTING-MODE.md §3, exactly.
const (
	nightStateInactive            = "inactive"
	nightStatePreparing           = "preparing"
	nightStatePreshow             = "preshow"
	nightStateTransitionToShow    = "transition-to-show"
	nightStateLive                = "live"
	nightStateTransitionToResting = "transition-to-resting"
	nightStateRestingIntershow    = "resting-intershow"
	nightStateEndOfNightResting   = "end-of-night-resting"
	nightStateFadingOut           = "fading-out"
	nightStateStopped             = "stopped"
)

// Night lifecycle commands - RESTING-MODE.md §4. The path segment IS the
// command name. end-session is a provisional operator-recovery action
// (owner ADR question queued separately, not yet part of ADR-038's own
// closed vocabulary).
const (
	nightCommandPrepareSite           = "prepare-site"
	nightCommandRunReadiness          = "run-readiness"
	nightCommandStartPreshow          = "start-preshow"
	nightCommandStartNight            = "start-night"
	nightCommandRequestFinalShow      = "request-final-show"
	nightCommandFadeOutNight          = "fade-out-night"
	nightCommandPowerDownPresentation = "power-down-presentation"
	nightCommandEndSession            = "end-session"
)

var validNightCommands = map[string]bool{
	nightCommandPrepareSite:           true,
	nightCommandRunReadiness:          true,
	nightCommandStartPreshow:          true,
	nightCommandStartNight:            true,
	nightCommandRequestFinalShow:      true,
	nightCommandFadeOutNight:          true,
	nightCommandPowerDownPresentation: true,
	nightCommandEndSession:            true,
}

// nightExemptFromDegradedGate: these three, plus end-session (handled on
// its own path), are direction-safe and never gated on the session's OWN
// Degraded flag: a refusal here would be strictly worse than no
// coordinator at all, since it is a successful conversation that fires no
// fallback. This is unrelated to a configured interlock: fade-out-night
// and power-down-presentation CAN still be withheld by a "block" rule
// declared for their own phase (Track F seam F6); see
// [handlers.nightFadeOutNight] and [handlers.nightPowerDownPresentation]'s
// own doc comments for what remains available when one does.
var nightExemptFromDegradedGate = map[string]bool{
	nightCommandFadeOutNight:          true,
	nightCommandPowerDownPresentation: true,
	nightCommandRequestFinalShow:      true,
}

const (
	nightOutcomeApplied        = "applied"
	nightOutcomeIdempotentNoOp = "idempotent_no_op"
)

// nightDegradedGuidance is what a degraded session tells an operator, and
// it names every command that still works: refusing one of those would be
// a successful conversation that fires no fallback, which is worse than
// this coordinator being switched off. A configured "block" interlock can
// still separately refuse fade-out-night or power-down-presentation
// (Track F seam F6) even though the session's own Degraded flag never
// does; end-session is untouched by any interlock and is always the
// unconditional way to reach stopped.
const nightDegradedGuidance = "night session is degraded (%s). " +
	"request-final-show, fade-out-night and power-down-presentation are still accepted and are how you end the night through this session, " +
	"unless a configured interlock separately withholds one of them; " +
	"end-session abandons it instead, is never withheld by an interlock, and prepare-site then starts a fresh one. Every other lifecycle command is refused until then."

// errNightCommandRefused is the sentinel a Tx closure returns to signal
// "roll back, no error occurred - a *v1.Problem describes the refusal",
// distinguishing that from a genuine store/internal error.
var errNightCommandRefused = errors.New("api: night command refused")

// nightShutdownIntentRank orders the two shutdown intents: power-down is
// the stronger, terminal intent (invariant 1) and a later fade-out-night
// must never downgrade it.
func nightShutdownIntentRank(intent string) int {
	switch intent {
	case "power-down":
		return 2
	case "fade-out":
		return 1
	default:
		return 0
	}
}

// nightCommandOutcome is what every per-command decide function produces.
type nightCommandOutcome struct {
	result  store.NightSessionRecord
	outcome string
	persist string // "" | "create" | "update"

	// auditParams, when non-nil, is folded into this command's own audit
	// entry (ADR-024's Params field). Track F seam F6's only user: an
	// applied interlock override, which RESTING-MODE.md §10.1 requires the
	// audit entry to identify by rule, phase, reason, and bounded scope.
	auditParams map[string]any
}

// --- HTTP handlers ---

func (h *handlers) handleGetNightLifecycle(w http.ResponseWriter, r *http.Request) {
	now := h.now()
	rec, ok, err := h.deps.NightSessions.GetCurrentNightSession(r.Context())
	if err != nil {
		h.writeInternalError(w, now, "get current night session", err)
		return
	}
	if !ok {
		rec = nightSyntheticInactiveSession(now)
	}
	jsonWrite(w, v1.NightSessionResponse{ServerTime: formatTime(now), Session: mapNightSessionState(r.Context(), h.deps, rec, now, h.nightReadinessMaxAge)})
}

func (h *handlers) handleGetNightLifecycleByID(w http.ResponseWriter, r *http.Request) {
	now := h.now()
	id := r.PathValue("id")
	rec, err := h.deps.NightSessions.GetNightSession(r.Context(), id)
	if err != nil {
		if err == store.ErrNightSessionNotFound {
			writeProblem(w, h.logger, now, resourceNotFoundProblem(fmt.Sprintf("no night session with id %q", id)))
			return
		}
		h.writeInternalError(w, now, "get night session", err)
		return
	}
	jsonWrite(w, v1.NightSessionResponse{ServerTime: formatTime(now), Session: mapNightSessionState(r.Context(), h.deps, rec, now, h.nightReadinessMaxAge)})
}

const maxNightCommandRequestBodyBytes = 4 << 10

// decodeNightCommandBody reads the optional {"idempotencyKey": string,
// "interlockOverrides": [{"rule": string, "reason": string}]?} body. An
// absent or empty body is valid: every field is optional.
// interlockOverrides is Track F seam F6's own addition
// (RESTING-MODE.md §10.1): naming a rule here is the caller's request to
// override it, and is honored only where that rule itself declares
// overridePolicy "authorized-operator", the caller separately holds
// [identity.ScopeNightOverride], and the rule is actually withholding the
// phase this command is entering (nightEvaluatePhaseInterlockGate).
// nightCommandsConsultingNoInterlock names the two commands that declare
// no interlock phase at all (RESTING-MODE.md §10.1's nine-phase enum
// does not include either): request-final-show and end-session.
// decodeNightCommandBody refuses interlockOverrides on either rather than
// silently dropping a value the caller believed would do something:
// found by review, this seam's safety review round: silently ignoring it
// was the worst of the three options (reject, silently ignore, silently
// apply).
var nightCommandsConsultingNoInterlock = map[string]bool{
	nightCommandRequestFinalShow: true,
	nightCommandEndSession:       true,
}

func decodeNightCommandBody(r *http.Request, cmd string) (idempotencyKey string, overrides []nightInterlockOverrideRequest, problem *v1.Problem) {
	if r.ContentLength == 0 {
		return "", nil, nil
	}
	var body struct {
		IdempotencyKey     string                          `json:"idempotencyKey"`
		InterlockOverrides []nightInterlockOverrideRequest `json:"interlockOverrides"`
	}
	dec := json.NewDecoder(io.LimitReader(r.Body, maxNightCommandRequestBodyBytes+1))
	// Strict decoding, found by review this seam's safety review round: a
	// misspelled "interlockOverride" (missing the trailing "s") previously
	// decoded silently as zero overrides, and the caller then saw a 409
	// with no hint the override never arrived at all.
	dec.DisallowUnknownFields()
	if err := dec.Decode(&body); err != nil && err != io.EOF {
		p := invalidParameterProblem("request body must be a JSON object matching {\"idempotencyKey\":string?,\"interlockOverrides\":[{\"rule\":string,\"reason\":string}]?}, with no other keys")
		return "", nil, &p
	}
	for _, o := range body.InterlockOverrides {
		if o.Rule == "" || o.Reason == "" {
			p := invalidParameterProblem("every interlockOverrides entry requires a non-empty \"rule\" and \"reason\"")
			return "", nil, &p
		}
	}
	if len(body.InterlockOverrides) > 0 && nightCommandsConsultingNoInterlock[cmd] {
		p := invalidParameterProblem(fmt.Sprintf("%q declares no interlock phase and consults no gate; interlockOverrides must be omitted, not silently ignored", cmd))
		return "", nil, &p
	}
	return body.IdempotencyKey, body.InterlockOverrides, nil
}

// handleNightCommand serves POST /api/v1/night/commands/{command}, behind
// writeGuard(&scopeNightCommand, ...). Invariant 7: never waits on
// anything beyond the transaction itself.
func (h *handlers) handleNightCommand(w http.ResponseWriter, r *http.Request) {
	now := h.now()
	ctx := r.Context()
	cmd := r.PathValue("command")
	if !validNightCommands[cmd] {
		writeProblem(w, h.logger, now, invalidParameterProblem(fmt.Sprintf("unsupported command %q; this coordinator supports: prepare-site, run-readiness, start-preshow, start-night, request-final-show, fade-out-night, power-down-presentation, end-session", cmd)))
		return
	}
	idempotencyKey, interlockOverrides, problem := decodeNightCommandBody(r, cmd)
	if problem != nil {
		writeProblem(w, h.logger, now, *problem)
		return
	}

	ac := authFromContext(ctx)
	issuer := identity.AuditEntry{
		Timestamp: now, PrincipalID: ac.result.Principal.ID, PrincipalName: ac.result.Principal.Name,
		Form: ac.result.Form, CredentialID: ac.result.CredentialID, ClientAddr: h.clientAddr(r),
	}

	var out nightCommandOutcome
	var attributionDegraded bool
	var opErr error

	callerHasOverrideScope := nightCallerHasOverrideScope(ac)

	switch {
	case cmd == nightCommandEndSession:
		out, problem, opErr = h.nightRunExempt(ctx, now, cmd, issuer, &attributionDegraded, func(ctx context.Context, tx *store.Tx, current *store.NightSessionRecord) (nightCommandOutcome, *v1.Problem, error) {
			return h.nightEndSessionDecide(now, current), nil, nil
		})
	case cmd == nightCommandRunReadiness:
		// Runs its own FPP/asset work, and its own phase="run-readiness"
		// interlock evidence, outside any transaction; see
		// [handlers.nightRunReadinessCommand]'s own doc comment.
		out, problem, opErr = h.nightRunReadinessCommand(ctx, now, issuer, interlockOverrides, callerHasOverrideScope)
	case cmd == nightCommandPrepareSite:
		// Its own phase="prepare-site" interlock evidence is dispatched
		// live, outside any transaction; see
		// [handlers.nightPrepareSiteCommand]'s own doc comment for why
		// prepare-site cannot be routed through nightRunGated directly the
		// way start-preshow and start-night are.
		out, problem, opErr = h.nightPrepareSiteCommand(ctx, now, issuer, idempotencyKey, interlockOverrides, callerHasOverrideScope)
	case cmd == nightCommandFadeOutNight:
		// Its own phase="fade-out-night" interlock evidence is dispatched
		// live, outside any transaction; see
		// [handlers.nightFadeOutNightCommand]'s own doc comment.
		out, problem, opErr = h.nightFadeOutNightCommand(ctx, now, issuer, &attributionDegraded, interlockOverrides, callerHasOverrideScope)
	case cmd == nightCommandPowerDownPresentation:
		// Its own phase="power-down-presentation" interlock evidence is
		// dispatched live, outside any transaction; see
		// [handlers.nightPowerDownPresentationCommand]'s own doc comment.
		out, problem, opErr = h.nightPowerDownPresentationCommand(ctx, now, issuer, &attributionDegraded, interlockOverrides, callerHasOverrideScope)
	case nightExemptFromDegradedGate[cmd]:
		// Only request-final-show remains here: it declares no interlock
		// phase (RESTING-MODE.md §10.1's nine-phase enum does not include
		// it) and takes no overrides at all.
		out, problem, opErr = h.nightRunExempt(ctx, now, cmd, issuer, &attributionDegraded, func(ctx context.Context, tx *store.Tx, current *store.NightSessionRecord) (nightCommandOutcome, *v1.Problem, error) {
			return h.nightDecideExemptCommand(ctx, tx, cmd, now, current, interlockOverrides, callerHasOverrideScope)
		})
	default:
		out, problem, opErr = h.nightRunGated(ctx, now, cmd, issuer, func(ctx context.Context, tx *store.Tx, current *store.NightSessionRecord) (nightCommandOutcome, *v1.Problem, error) {
			return h.nightDecideGatedCommand(ctx, tx, cmd, now, current, idempotencyKey, interlockOverrides, callerHasOverrideScope)
		})
	}

	if opErr != nil {
		h.writeInternalError(w, now, "apply night command "+cmd, opErr)
		return
	}
	if problem != nil {
		writeProblem(w, h.logger, now, *problem)
		return
	}

	state := mapNightSessionState(ctx, h.deps, out.result, now, h.nightReadinessMaxAge)
	state.AttributionDegraded = attributionDegraded

	// 202: the command is accepted, never confirmed by anything downstream
	// at this layer (invariant 7). jsonWrite always answers 200, so this
	// route writes its own status line.
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(http.StatusAccepted)
	_ = json.NewEncoder(w).Encode(v1.NightCommandResponse{
		ServerTime: formatTime(now),
		Command:    v1.NightCommandResult{Command: cmd, Outcome: out.outcome, AttributionDegraded: attributionDegraded},
		Session:    state,
	})
}

// nightDecideExemptCommand dispatches to the three degraded-gate-exempt
// commands' own decide functions. request-final-show declares no
// interlock phase (RESTING-MODE.md §10.1's nine-phase enum does not
// include it) and so takes no overrides at all.
func (h *handlers) nightDecideExemptCommand(ctx context.Context, tx *store.Tx, cmd string, now time.Time, current *store.NightSessionRecord, interlockOverrides []nightInterlockOverrideRequest, callerHasOverrideScope bool) (nightCommandOutcome, *v1.Problem, error) {
	switch cmd {
	case nightCommandRequestFinalShow:
		return h.nightRequestFinalShow(now, current)
	}
	return nightCommandOutcome{}, nil, fmt.Errorf("api: no exempt decide function for %q", cmd)
}

// nightDecideGatedCommand applies invariant 4 (a degraded, non-terminal
// session refuses every gated command) and then dispatches to
// start-preshow and start-night, which both gate their own declared
// phase against a STORED, trusted readiness result
// ([handlers.nightGatePhaseTx]/[nightEvaluatePhaseInterlockGate]) since
// both already run inside this transaction. run-readiness and
// prepare-site are NOT here: both need live evidence dispatched outside
// any transaction, so handleNightCommand routes them to
// [handlers.nightRunReadinessCommand] and
// [handlers.nightPrepareSiteCommand] directly instead.
func (h *handlers) nightDecideGatedCommand(ctx context.Context, tx *store.Tx, cmd string, now time.Time, current *store.NightSessionRecord, idempotencyKey string, interlockOverrides []nightInterlockOverrideRequest, callerHasOverrideScope bool) (nightCommandOutcome, *v1.Problem, error) {
	if current != nil && current.Degraded && current.State != nightStateStopped {
		p := nightAmbiguousProblem(fmt.Sprintf(nightDegradedGuidance, current.DegradedReason))
		return nightCommandOutcome{}, &p, nil
	}
	switch cmd {
	case nightCommandStartPreshow:
		return h.nightStartPreshow(ctx, tx, now, current, interlockOverrides, callerHasOverrideScope)
	case nightCommandStartNight:
		return h.nightStartNightTx(ctx, tx, now, current, interlockOverrides, callerHasOverrideScope)
	}
	return nightCommandOutcome{}, nil, fmt.Errorf("api: no gated decide function for %q", cmd)
}

// nightRunExempt wraps decide in one store transaction and, on success,
// a best-effort (never-blocking) audit write (ADR-024 decision 11's
// exemption, extended here to fade-out-night, power-down-presentation,
// request-final-show, and end-session). *attributionDegraded reports
// whether that audit write failed.
func (h *handlers) nightRunExempt(ctx context.Context, now time.Time, cmd string, issuer identity.AuditEntry, attributionDegraded *bool,
	decide func(ctx context.Context, tx *store.Tx, current *store.NightSessionRecord) (nightCommandOutcome, *v1.Problem, error),
) (nightCommandOutcome, *v1.Problem, error) {
	var out nightCommandOutcome
	var problem *v1.Problem
	txErr := h.deps.NightSessions.InTx(ctx, func(ctx context.Context, tx *store.Tx) error {
		current, hasCurrent, err := tx.GetCurrentNightSession(ctx)
		if err != nil {
			return err
		}
		var curPtr *store.NightSessionRecord
		if hasCurrent {
			curPtr = &current
		}
		var derr error
		out, problem, derr = decide(ctx, tx, curPtr)
		if derr != nil {
			return derr
		}
		if problem != nil {
			return nil
		}
		switch out.persist {
		case "create":
			out.result.Issuer = nightIssuerFromAudit(issuer, cmd, now)
			return tx.CreateNightSession(ctx, out.result, now)
		case "update":
			out.result.Issuer = nightIssuerFromAudit(issuer, cmd, now)
			return tx.UpdateNightSession(ctx, out.result, now)
		}
		return nil
	})
	if txErr != nil {
		return nightCommandOutcome{}, nil, txErr
	}
	if problem != nil {
		return nightCommandOutcome{}, problem, nil
	}

	issuer.Timestamp = now
	issuer.Action = "night." + cmd
	issuer.Target = out.result.ID
	issuer.Kind = identity.AuditOutcome
	issuer.OutcomeState = string(observation.StateCurrent)
	issuer.OutcomeReason = out.outcome
	if out.auditParams != nil {
		// D3, this seam's safety review round: nightRunExempt built its
		// own issuer and never read out.auditParams, while nightRunGated
		// did. fade-out-night and power-down-presentation both set
		// auditParams for an accepted interlock override and both route
		// through this function, so an accepted override on exactly the
		// two commands where a bypass matters most was never audited at
		// all (RESTING-MODE.md §10.1 requires rule, phase, reason, and
		// bounded scope in the audit log).
		issuer.Params = out.auditParams
	}
	if h.writeBestEffortAuditBounded(ctx, now, degradedAttributionReasonPostDispatch, issuer) {
		*attributionDegraded = true
		out.result.AttributionDegraded = true
		if uerr := h.deps.NightSessions.UpdateNightSession(ctx, out.result, now); uerr != nil {
			h.logWarn("failed to persist attributionDegraded on night session", "sessionId", out.result.ID, "error", uerr)
		}
	}
	return out, nil, nil
}

// nightAuditUnavailableProblem is the four fail-closed commands' own 503:
// nothing was dispatched and nothing was recorded (ADR-024 decision 11's
// non-exempt direction).
func nightAuditUnavailableProblem(cmd string) v1.Problem {
	return v1.Problem{
		Type: ProblemTypeNightAuditUnavailable, Title: "Command refused: it could not be durably recorded",
		Status: http.StatusServiceUnavailable,
		Detail: fmt.Sprintf("%s was not applied: its audit entry could not be written, and this command is refused rather than proceeding without one", cmd),
	}
}

// nightRunGated wraps decide and the resulting write in one atomic
// transaction with the command's own audit entry (identity.Service.
// AuditedWrite): an unwritable audit store here refuses the whole
// command rather than proceeding degraded.
func (h *handlers) nightRunGated(ctx context.Context, now time.Time, cmd string, issuer identity.AuditEntry,
	decide func(ctx context.Context, tx *store.Tx, current *store.NightSessionRecord) (nightCommandOutcome, *v1.Problem, error),
) (nightCommandOutcome, *v1.Problem, error) {
	var out nightCommandOutcome
	var problem *v1.Problem
	auditErr := h.deps.Identity.AuditedWrite(ctx, func(ctx context.Context, tx *store.Tx) (identity.AuditEntry, error) {
		current, hasCurrent, err := tx.GetCurrentNightSession(ctx)
		if err != nil {
			return identity.AuditEntry{}, err
		}
		var curPtr *store.NightSessionRecord
		if hasCurrent {
			curPtr = &current
		}
		out, problem, err = decide(ctx, tx, curPtr)
		if err != nil {
			return identity.AuditEntry{}, err
		}
		if problem != nil {
			return identity.AuditEntry{}, errNightCommandRefused
		}
		switch out.persist {
		case "create":
			out.result.Issuer = nightIssuerFromAudit(issuer, cmd, now)
			if err := tx.CreateNightSession(ctx, out.result, now); err != nil {
				return identity.AuditEntry{}, err
			}
		case "update":
			out.result.Issuer = nightIssuerFromAudit(issuer, cmd, now)
			if err := tx.UpdateNightSession(ctx, out.result, now); err != nil {
				return identity.AuditEntry{}, err
			}
		}
		issuer.Timestamp = now
		issuer.Action = "night." + cmd
		issuer.Target = out.result.ID
		issuer.Kind = identity.AuditOutcome
		issuer.OutcomeState = string(observation.StateCurrent)
		issuer.OutcomeReason = out.outcome
		if out.auditParams != nil {
			issuer.Params = out.auditParams
		}
		return issuer, nil
	})
	if auditErr != nil {
		if errors.Is(auditErr, errNightCommandRefused) {
			return nightCommandOutcome{}, problem, nil
		}
		if errors.Is(auditErr, identity.ErrAuditWrite) {
			p := nightAuditUnavailableProblem(cmd)
			return nightCommandOutcome{}, &p, nil
		}
		return nightCommandOutcome{}, nil, auditErr
	}
	return out, nil, nil
}

// --- per-command logic ---

// nightSyntheticInactiveSession is what a GET reports and every exempt
// command decides for when no session has EVER been created - never
// persisted.
func nightSyntheticInactiveSession(now time.Time) store.NightSessionRecord {
	return store.NightSessionRecord{State: nightStateInactive, StateEnteredAt: now}
}

// nightPrepareSiteTx is prepare-site's decide function. idempotencyKey,
// checked first, replays the original session rather than creating a
// second one.
// nightPrepareSiteTx is prepare-site's own tx-bound decide function,
// invoked by [handlers.nightPrepareSiteCommand] with gateObjectID/
// gateRevision already resolved and cleared against phase="prepare-site"
// interlocks OUTSIDE this transaction. gateObjectID is "" when that
// precheck determined no NEW epoch could possibly be created (an
// idempotent replay or an already-open session), in which case no
// consistency check runs here either. When gateObjectID is set, this
// still re-resolves the active configuration itself, inside the SAME
// transaction that persists the new session, and refuses rather than
// silently using stale interlock evidence if the two disagree, the
// identical race the pre-tx/short-tx split in
// [handlers.nightRunReadinessCommand] already accepts and names for the
// identical reason.
func (h *handlers) nightPrepareSiteTx(ctx context.Context, tx *store.Tx, now time.Time, current *store.NightSessionRecord, idempotencyKey, gateObjectID string, gateRevision int64) (nightCommandOutcome, *v1.Problem, error) {
	if idempotencyKey != "" {
		existing, err := tx.GetNightSessionByIdempotencyKey(ctx, idempotencyKey)
		if err == nil {
			return nightCommandOutcome{result: existing, outcome: nightOutcomeIdempotentNoOp}, nil, nil
		}
		if !errors.Is(err, store.ErrNightSessionNotFound) {
			return nightCommandOutcome{}, nil, err
		}
	}
	if current != nil && current.State != nightStateStopped {
		if current.State == nightStateFadingOut {
			p := nightStateRejectedProblem("prepare-site is rejected during finalization or fade-out")
			return nightCommandOutcome{}, &p, nil
		}
		// Duplicate within the same preparation or active session:
		// idempotent no-op, never a second epoch (invariant 3).
		return nightCommandOutcome{result: *current, outcome: nightOutcomeIdempotentNoOp}, nil, nil
	}

	objectID, revision, problem, err := h.resolveActiveNightSessionConfigTx(ctx, tx)
	if err != nil {
		return nightCommandOutcome{}, nil, err
	}
	if problem != nil {
		return nightCommandOutcome{}, problem, nil
	}
	if gateObjectID != "" && (objectID != gateObjectID || revision != gateRevision) {
		p := nightNotReadyProblem("prepare-site: the active night.session configuration changed while its prepare-site interlocks were being evaluated; run prepare-site again")
		return nightCommandOutcome{}, &p, nil
	}

	rec := store.NightSessionRecord{
		ID: uuid.NewString(), ConfigObjectID: objectID, ConfigRevision: revision,
		State: nightStatePreparing, StateEnteredAt: now, PrepareSiteIdempotencyKey: idempotencyKey,
	}
	return nightCommandOutcome{result: rec, outcome: nightOutcomeApplied, persist: "create"}, nil, nil
}

// nightPrepareSiteCommand is prepare-site's own top-level entry, mirroring
// [handlers.nightRunReadinessCommand]'s shape: any interlock evidence
// read for phase "prepare-site" must dispatch live and OUTSIDE any store
// transaction, since prepare-site's own persist step already holds the
// store's one connection ([handlers.nightRunGated] wraps
// [handlers.nightPrepareSiteTx] in one transaction). An idempotent
// replay or an already-open session creates no new epoch, so it consults
// no interlock at all: nothing is being "entered."
func (h *handlers) nightPrepareSiteCommand(ctx context.Context, now time.Time, issuer identity.AuditEntry, idempotencyKey string, overrides []nightInterlockOverrideRequest, callerHasOverrideScope bool) (nightCommandOutcome, *v1.Problem, error) {
	current, hasCurrent, err := h.deps.NightSessions.GetCurrentNightSession(ctx)
	if err != nil {
		return nightCommandOutcome{}, nil, err
	}

	willAttemptNewEpoch := !hasCurrent || current.State == nightStateStopped
	if willAttemptNewEpoch && idempotencyKey != "" {
		if _, err := h.deps.NightSessions.GetNightSessionByIdempotencyKey(ctx, idempotencyKey); err == nil {
			willAttemptNewEpoch = false
		} else if !errors.Is(err, store.ErrNightSessionNotFound) {
			return nightCommandOutcome{}, nil, err
		}
	}

	var gateObjectID string
	var gateRevision int64
	var overrideAuditParams []map[string]any
	if willAttemptNewEpoch {
		objectID, revision, payload, problem, err := h.nightResolveActiveConfigForGate(ctx)
		if err != nil {
			return nightCommandOutcome{}, nil, err
		}
		if problem != nil {
			return nightCommandOutcome{}, problem, nil
		}
		dispatchCtx, cancel := nightBoundInterlockDispatch(ctx)
		gate := h.nightLiveEvaluatePhaseGate(dispatchCtx, payload, config.NightInterlockPhasePrepareSite, overrides, callerHasOverrideScope)
		cancel()
		if len(gate.Withheld) > 0 {
			p := nightNotReadyProblem(nightInterlockGateProblem(config.NightInterlockPhasePrepareSite, gate.Withheld).detail())
			return nightCommandOutcome{}, &p, nil
		}
		gateObjectID, gateRevision = objectID, revision
		if len(gate.Overridden) > 0 {
			overrideAuditParams = nightInterlockOverrideAuditParams(gate.Overridden)
		}
	}

	return h.nightRunGated(ctx, now, nightCommandPrepareSite, issuer, func(ctx context.Context, tx *store.Tx, cur *store.NightSessionRecord) (nightCommandOutcome, *v1.Problem, error) {
		if cur != nil && cur.Degraded && cur.State != nightStateStopped {
			p := nightAmbiguousProblem(fmt.Sprintf(nightDegradedGuidance, cur.DegradedReason))
			return nightCommandOutcome{}, &p, nil
		}
		out, problem, err := h.nightPrepareSiteTx(ctx, tx, now, cur, idempotencyKey, gateObjectID, gateRevision)
		if err == nil && problem == nil && len(overrideAuditParams) > 0 {
			out.auditParams = map[string]any{"interlockOverrides": overrideAuditParams}
		}
		return out, problem, err
	})
}

// nightResolveActiveConfigForGate is [handlers.resolveActiveNightSessionConfigTx]'s
// non-tx twin, for [handlers.nightPrepareSiteCommand]'s own pre-transaction
// interlock precheck: a plain read, never held alongside the store's one
// connection the way [handlers.nightPrepareSiteTx]'s own resolution
// (inside the transaction) already is. Also returns the decoded payload,
// since the precheck needs it and re-decoding it a second time inside the
// transaction would be wasted work for the common case where nothing
// changed in between.
func (h *handlers) nightResolveActiveConfigForGate(ctx context.Context) (objectID string, revision int64, payload config.NightSessionPayload, problem *v1.Problem, err error) {
	activeObj, aerr := h.deps.Config.GetConfigObject(ctx, config.NightSessionActiveConfigKind, config.NightSessionActiveObjectID)
	if aerr != nil {
		if errors.Is(aerr, store.ErrConfigObjectNotFound) {
			p := nightNotReadyProblem("prepare-site: no night.session.active pointer is configured; PUT /api/v1/config/night.session.active first")
			return "", 0, config.NightSessionPayload{}, &p, nil
		}
		return "", 0, config.NightSessionPayload{}, nil, aerr
	}
	if activeObj.CurrentRevision == 0 {
		p := nightNotReadyProblem("prepare-site: no night.session.active pointer is configured; PUT /api/v1/config/night.session.active first")
		return "", 0, config.NightSessionPayload{}, &p, nil
	}
	activeRev, aerr := h.deps.Config.GetConfigRevision(ctx, config.NightSessionActiveConfigKind, config.NightSessionActiveObjectID, activeObj.CurrentRevision)
	if aerr != nil {
		return "", 0, config.NightSessionPayload{}, nil, aerr
	}
	var activePayload config.NightSessionActivePayload
	if err := jsonUnmarshalStrict(activeRev.PayloadJSON, &activePayload); err != nil {
		return "", 0, config.NightSessionPayload{}, nil, fmt.Errorf("api: decode night.session.active payload: %w", err)
	}
	if activePayload.Session == "" {
		p := nightNotReadyProblem("prepare-site: night.session.active names no session; PUT /api/v1/config/night.session.active first")
		return "", 0, config.NightSessionPayload{}, &p, nil
	}

	obj, oerr := h.deps.Config.GetConfigObject(ctx, config.NightSessionConfigKind, activePayload.Session)
	if oerr != nil || obj.CurrentRevision == 0 {
		if oerr != nil && !errors.Is(oerr, store.ErrConfigObjectNotFound) {
			return "", 0, config.NightSessionPayload{}, nil, oerr
		}
		p := nightNotReadyProblem(fmt.Sprintf("prepare-site: night.session.active names %q, which has no active revision", activePayload.Session))
		return "", 0, config.NightSessionPayload{}, &p, nil
	}
	rev, rerr := h.deps.Config.GetConfigRevision(ctx, config.NightSessionConfigKind, activePayload.Session, obj.CurrentRevision)
	if rerr != nil {
		return "", 0, config.NightSessionPayload{}, nil, rerr
	}
	var sessionPayload config.NightSessionPayload
	if err := jsonUnmarshalStrict(rev.PayloadJSON, &sessionPayload); err != nil {
		return "", 0, config.NightSessionPayload{}, nil, fmt.Errorf("api: decode night.session payload: %w", err)
	}
	return activePayload.Session, obj.CurrentRevision, sessionPayload, nil, nil
}

// nightStartPreshow gates phase="start-preshow" against the most recent
// TRUSTED readiness result ([handlers.nightGatePhaseTx]): a live
// dispatch is not safe here, since this decide function already runs
// inside nightRunGated's own transaction.
func (h *handlers) nightStartPreshow(ctx context.Context, tx *store.Tx, now time.Time, current *store.NightSessionRecord, overrides []nightInterlockOverrideRequest, callerHasOverrideScope bool) (nightCommandOutcome, *v1.Problem, error) {
	if current == nil {
		p := nightStateRejectedProblem("start-preshow has no unconsumed preparation epoch; run prepare-site first")
		return nightCommandOutcome{}, &p, nil
	}
	switch current.State {
	case nightStatePreparing:
		gateProblem, auditParams := h.nightGatePhaseTx(ctx, tx, now, *current, config.NightInterlockPhaseStartPreshow, overrides, callerHasOverrideScope)
		if gateProblem != nil {
			return nightCommandOutcome{}, gateProblem, nil
		}
		next := *current
		next.State = nightStatePreshow
		next.StateEnteredAt = now
		out := nightCommandOutcome{result: next, outcome: nightOutcomeApplied, persist: "update"}
		if len(auditParams) > 0 {
			out.auditParams = map[string]any{"interlockOverrides": auditParams}
		}
		return out, nil, nil
	case nightStatePreshow, nightStateTransitionToShow, nightStateLive, nightStateTransitionToResting, nightStateRestingIntershow, nightStateEndOfNightResting:
		return nightCommandOutcome{result: *current, outcome: nightOutcomeIdempotentNoOp}, nil, nil
	default: // fading-out, stopped
		p := nightStateRejectedProblem("start-preshow has no unconsumed preparation epoch; run prepare-site first")
		return nightCommandOutcome{}, &p, nil
	}
}

// nightStartNightTx encodes RESTING-MODE.md §4.4's table exactly, plus
// Track F seam F6's own interlock gate for phase "start-night": a
// "block" rule for that phase currently withholding it (per the SAME
// stored readiness result the freshness check just accepted) refuses
// start-night unless every withholding rule is covered by a valid,
// authorized override in interlockOverrides.
func (h *handlers) nightStartNightTx(ctx context.Context, tx *store.Tx, now time.Time, current *store.NightSessionRecord, interlockOverrides []nightInterlockOverrideRequest, callerHasOverrideScope bool) (nightCommandOutcome, *v1.Problem, error) {
	if current == nil {
		p := nightNotReadyProblem("start-night: no active preparation; run prepare-site, run-readiness, and start-preshow first")
		return nightCommandOutcome{}, &p, nil
	}
	switch current.State {
	case nightStatePreshow:
		readiness, err := tx.GetLatestNightReadiness(ctx, current.ID)
		if err != nil {
			if err == store.ErrNightReadinessNotFound {
				p := nightNotReadyProblem("start-night: no readiness result recorded for this preparation epoch; run run-readiness first")
				return nightCommandOutcome{}, &p, nil
			}
			return nightCommandOutcome{}, nil, err
		}
		if readiness.EpochID != current.ID {
			p := nightNotReadyProblem("start-night: the most recent readiness result belongs to a prior preparation epoch and is never adopted (invariant 2)")
			return nightCommandOutcome{}, &p, nil
		}
		// Wall-clock, not monotonic: an NTP step backwards can trip the
		// negative-age branch and reject start-night. That is the safe
		// direction and is deliberately left as-is.
		age := now.Sub(readiness.CompletedAt)
		if age < 0 || age > h.nightReadinessMaxAge {
			p := nightNotReadyProblem(fmt.Sprintf("start-night: readiness result is %s old, past the configured maximum age of %s; run run-readiness again", age.Round(time.Second), h.nightReadinessMaxAge))
			return nightCommandOutcome{}, &p, nil
		}

		payload, err := h.getPinnedNightSessionPayloadTx(ctx, tx, *current)
		if err != nil {
			return nightCommandOutcome{}, nil, err
		}
		var storedChecks []v1.NightReadinessCheck
		_ = json.Unmarshal([]byte(readiness.ChecksJSON), &storedChecks)
		gate := nightEvaluatePhaseInterlockGate(payload, config.NightInterlockPhaseStartNight, nightDecodeWireChecks(storedChecks), age, interlockOverrides, callerHasOverrideScope)
		if len(gate.Withheld) > 0 {
			p := nightNotReadyProblem(nightInterlockGateProblem(config.NightInterlockPhaseStartNight, gate.Withheld).detail())
			return nightCommandOutcome{}, &p, nil
		}

		next := *current
		next.State = nightStateTransitionToShow
		next.StateEnteredAt = now
		next.ArmedShowID = uuid.NewString()
		next.ShowCommitted = false
		next.Cycle = current.Cycle + 1
		next.ContentAnchorJSON = ""
		// The first show of the night has no resting playback to lead
		// from, so E is the moment this transition begins - written
		// explicitly, never left for a fallback to reconstruct.
		boundaryE := now
		next.BoundaryJSON = encodeNightBoundary(nightBoundary{State: nightBoundaryStateArmed, ExpectedAt: &boundaryE, LastTickAt: &boundaryE, Reason: "content boundary E is this transition's own start; no resting playback preceded it"})
		out := nightCommandOutcome{result: next, outcome: nightOutcomeApplied, persist: "update"}
		if len(gate.Overridden) > 0 {
			out.auditParams = map[string]any{"interlockOverrides": nightInterlockOverrideAuditParams(gate.Overridden)}
		}
		return out, nil, nil
	case nightStateRestingIntershow, nightStateTransitionToShow, nightStateLive, nightStateTransitionToResting:
		return nightCommandOutcome{result: *current, outcome: nightOutcomeIdempotentNoOp}, nil, nil
	case nightStateEndOfNightResting:
		p := nightStateRejectedProblem("start-night: finalization is monotonic; end-of-night resting never starts another show")
		return nightCommandOutcome{}, &p, nil
	case nightStateFadingOut, nightStateStopped:
		p := nightStateRejectedProblem("start-night: this session has closed; a new prepare-site epoch, readiness, and start-preshow are required")
		return nightCommandOutcome{}, &p, nil
	default: // preparing
		p := nightNotReadyProblem("start-night: not ready; operator recovery completes readiness and invokes start-preshow first")
		return nightCommandOutcome{}, &p, nil
	}
}

func (h *handlers) nightRequestFinalShow(now time.Time, current *store.NightSessionRecord) (nightCommandOutcome, *v1.Problem, error) {
	if current == nil {
		p := nightStateRejectedProblem("request-final-show: no active or prepared session")
		return nightCommandOutcome{}, &p, nil
	}
	switch current.State {
	case nightStateLive, nightStateRestingIntershow, nightStatePreshow, nightStateTransitionToShow, nightStateTransitionToResting:
		if current.FinalShowRequested {
			return nightCommandOutcome{result: *current, outcome: nightOutcomeIdempotentNoOp}, nil, nil
		}
		next := *current
		next.FinalShowRequested = true
		t := now
		next.FinalShowRequestedAt = &t
		return nightCommandOutcome{result: next, outcome: nightOutcomeApplied, persist: "update"}, nil, nil
	case nightStateEndOfNightResting, nightStateFadingOut, nightStateStopped:
		return nightCommandOutcome{result: *current, outcome: nightOutcomeIdempotentNoOp}, nil, nil
	default: // preparing
		p := nightStateRejectedProblem("request-final-show: no active or prepared session")
		return nightCommandOutcome{}, &p, nil
	}
}

// applyNightShutdownEffect is fade-out-night's and power-down-
// presentation's shared core: close admission, record the (monotonic)
// shutdown intent, and either defer (a live or already-committed show
// finishes first - RESTING-MODE.md §7.1.1) or enter the fade/shutdown
// path. It never reaches stopped on its own: stopped requires an issued
// FPP stop and fresh idle evidence, which the night loop's own
// fading-out tick owns (nightshutdown.go).
func applyNightShutdownEffect(now time.Time, rec store.NightSessionRecord, intent string) (store.NightSessionRecord, bool) {
	changed := false
	if !rec.AdmissionClosed {
		rec.AdmissionClosed = true
		t := now
		rec.AdmissionClosedAt = &t
		changed = true
	}
	if nightShutdownIntentRank(intent) > nightShutdownIntentRank(rec.ShutdownIntent) {
		rec.ShutdownIntent = intent
		changed = true
	}

	deferring := rec.State == nightStateLive || (rec.State == nightStateTransitionToShow && rec.ShowCommitted)
	switch {
	case rec.State == nightStateFadingOut || rec.State == nightStateStopped:
		// Nothing left to fade.
	case deferring:
		if !rec.FinalShowRequested {
			rec.FinalShowRequested = true
			t := now
			rec.FinalShowRequestedAt = &t
			changed = true
		}
	default: // preparing, preshow, uncommitted transition-to-show, transition-to-resting, resting-intershow, end-of-night-resting
		rec.ArmedShowID = ""
		rec.ShowCommitted = false
		rec.State = nightStateFadingOut
		rec.StateEnteredAt = now
		rec.ContentAnchorJSON = ""
		rec.BoundaryJSON = encodeNightBoundary(nightBoundary{
			State:  nightBoundaryStateInvalid,
			Reason: "the armed boundary was cancelled by a shutdown request",
		})
		changed = true
	}
	return rec, changed
}

// nightFadeOutNightCommand is fade-out-night's own top-level entry,
// mirroring nightPrepareSiteCommand and nightRunReadinessCommand: its own
// phase="fade-out-night" interlock evidence is dispatched LIVE, at the
// instant this command runs, outside any transaction.
//
// This replaces an earlier stored-readiness gate for the identical
// reason prepare-site and run-readiness already dispatch live: a night
// runs for hours and run-readiness runs once, near the start, so by the
// time fade-out-night or power-down-presentation actually runs, a
// stored-evidence gate almost always finds no trusted result at all
// (nightTrustedReadinessChecksTx discards anything older than
// h.nightReadinessMaxAge) and reports every rule "evidence unavailable"
// regardless of what is actually true at the site. On these two phases
// specifically that is a genuine trap, not a conservative default: with
// onUnavailable "block" the rule withholds forever, and with
// onUnavailable "allow" it is silently inert, since "unavailable" was
// the only condition it could ever see. Reviewer-confirmed, this
// review round: live dispatch removes the trap instead of papering over
// it.
//
// A "block" rule can still make this command refuse if the site
// genuinely reports condition-false, or if its own signal is
// unreachable and it declares onUnavailable: block. Neither can strand
// the night: nightsitecontrol.go's own write-time validation now refuses
// a "block" rule on this phase that declares overridePolicy: none, so
// every such rule has an authorized-operator override path, and
// end-session (a wholly separate decide path, declared for no interlock
// phase) always reaches stopped regardless.
func (h *handlers) nightFadeOutNightCommand(ctx context.Context, now time.Time, issuer identity.AuditEntry, attributionDegraded *bool, overrides []nightInterlockOverrideRequest, callerHasOverrideScope bool) (nightCommandOutcome, *v1.Problem, error) {
	current, hasCurrent, err := h.deps.NightSessions.GetCurrentNightSession(ctx)
	if err != nil {
		return nightCommandOutcome{}, nil, err
	}
	if !hasCurrent {
		return h.nightRunExempt(ctx, now, nightCommandFadeOutNight, issuer, attributionDegraded, func(ctx context.Context, tx *store.Tx, cur *store.NightSessionRecord) (nightCommandOutcome, *v1.Problem, error) {
			return h.nightFadeOutNightApply(cur, now, nil)
		})
	}

	_, changed := applyNightShutdownEffect(now, current, "fade-out")
	var overrideAuditParams []map[string]any
	if changed {
		payload, err := h.getPinnedNightSessionPayload(ctx, current)
		if err != nil {
			return nightCommandOutcome{}, nil, err
		}
		dispatchCtx, cancel := nightBoundInterlockDispatch(ctx)
		gate := h.nightLiveEvaluatePhaseGate(dispatchCtx, payload, config.NightInterlockPhaseFadeOutNight, overrides, callerHasOverrideScope)
		cancel()
		if len(gate.Withheld) > 0 {
			p := nightNotReadyProblem(nightInterlockGateProblem(config.NightInterlockPhaseFadeOutNight, gate.Withheld).detail())
			return nightCommandOutcome{}, &p, nil
		}
		if len(gate.Overridden) > 0 {
			overrideAuditParams = nightInterlockOverrideAuditParams(gate.Overridden)
		}
	}

	// The interlock decision above is NOT re-validated against a fresh
	// read here, unlike prepare-site's own identical-looking precheck:
	// prepare-site's own race is about which CONFIGURATION revision gets
	// pinned, which can genuinely change between the precheck and the
	// transaction. A running session's own interlocks are pinned once,
	// at prepare-site time, and never change for the life of the
	// session, so the only thing that can move between this precheck and
	// the transaction below is the session's own lifecycle state, and
	// nightFadeOutNightApply already recomputes applyNightShutdownEffect
	// fresh from whatever that state actually is when the transaction
	// runs, idempotently no-op'ing if nothing is left to do. Refusing on
	// a state-only mismatch here would rediscover the exact defect a
	// concurrency test already guards: fade-out-night must never be lost
	// to a race with another command.
	return h.nightRunExempt(ctx, now, nightCommandFadeOutNight, issuer, attributionDegraded, func(ctx context.Context, tx *store.Tx, cur *store.NightSessionRecord) (nightCommandOutcome, *v1.Problem, error) {
		return h.nightFadeOutNightApply(cur, now, overrideAuditParams)
	})
}

// nightFadeOutNightApply is the tx-bound apply step, gate-free: every
// interlock decision has already been made by
// [handlers.nightFadeOutNightCommand] before this runs.
func (h *handlers) nightFadeOutNightApply(current *store.NightSessionRecord, now time.Time, overrideAuditParams []map[string]any) (nightCommandOutcome, *v1.Problem, error) {
	if current == nil {
		return nightCommandOutcome{result: nightSyntheticInactiveSession(now), outcome: nightOutcomeIdempotentNoOp}, nil, nil
	}
	next, changed := applyNightShutdownEffect(now, *current, "fade-out")
	if !changed {
		return nightCommandOutcome{result: *current, outcome: nightOutcomeIdempotentNoOp}, nil, nil
	}
	out := nightCommandOutcome{result: next, outcome: nightOutcomeApplied, persist: "update"}
	if len(overrideAuditParams) > 0 {
		out.auditParams = map[string]any{"interlockOverrides": overrideAuditParams}
	}
	return out, nil, nil
}

// nightPowerPhaseConfiguredNotDispatched is Track F seam F6's own
// recorded value for the optional presentation-power phase when
// siteControl.presentationPowerOff IS configured: unlike invariant 6's
// "not_configured", this build validates that configuration fully
// (nightsitecontrol.go) but does not yet dispatch it automatically from
// this shutdown path; reported honestly rather than as "not_configured",
// which would claim nothing exists to remove power when something does.
// An operator removes it today either by dispatching the configured
// action directly (POST /api/v1/actions/{id}/invocations, behind
// show:action:invoke) or through a separately configured force-power-off
// action on the identical surface (RESTING-MODE.md §10.4).
const nightPowerPhaseConfiguredNotDispatched = "configured_not_dispatched"

// nightPowerDownPresentationCommand is power-down-presentation's own
// top-level entry, mirroring [handlers.nightFadeOutNightCommand]: its own
// phase="power-down-presentation" interlock evidence is dispatched LIVE,
// at the instant this command runs, outside any transaction, for the
// identical staleness reason that function's own doc comment states in
// full.
//
// Invariant 6, extended by Track F seam F6: with no power configuration,
// reaching stopped still records "not_configured" for the optional
// phase; with siteControl.presentationPowerOff configured, it records
// [nightPowerPhaseConfiguredNotDispatched] instead of a false claim. That
// resolution is purely bookkeeping (never a dispatch of its own) and is
// never gated: only the FIRST call that actually closes admission or
// raises the shutdown intent consults the interlock.
func (h *handlers) nightPowerDownPresentationCommand(ctx context.Context, now time.Time, issuer identity.AuditEntry, attributionDegraded *bool, overrides []nightInterlockOverrideRequest, callerHasOverrideScope bool) (nightCommandOutcome, *v1.Problem, error) {
	current, hasCurrent, err := h.deps.NightSessions.GetCurrentNightSession(ctx)
	if err != nil {
		return nightCommandOutcome{}, nil, err
	}
	if !hasCurrent {
		return h.nightRunExempt(ctx, now, nightCommandPowerDownPresentation, issuer, attributionDegraded, func(ctx context.Context, tx *store.Tx, cur *store.NightSessionRecord) (nightCommandOutcome, *v1.Problem, error) {
			return h.nightPowerDownPresentationApply(ctx, tx, cur, now, nil)
		})
	}
	if current.State == nightStateStopped && current.PowerPhase != "" {
		return h.nightRunExempt(ctx, now, nightCommandPowerDownPresentation, issuer, attributionDegraded, func(ctx context.Context, tx *store.Tx, cur *store.NightSessionRecord) (nightCommandOutcome, *v1.Problem, error) {
			return h.nightPowerDownPresentationApply(ctx, tx, cur, now, nil)
		})
	}

	_, shutdownChanged := applyNightShutdownEffect(now, current, "power-down")
	var overrideAuditParams []map[string]any
	if shutdownChanged {
		payload, err := h.getPinnedNightSessionPayload(ctx, current)
		if err != nil {
			return nightCommandOutcome{}, nil, err
		}
		dispatchCtx, cancel := nightBoundInterlockDispatch(ctx)
		gate := h.nightLiveEvaluatePhaseGate(dispatchCtx, payload, config.NightInterlockPhasePowerDownPresentation, overrides, callerHasOverrideScope)
		cancel()
		if len(gate.Withheld) > 0 {
			p := nightNotReadyProblem(nightInterlockGateProblem(config.NightInterlockPhasePowerDownPresentation, gate.Withheld).detail())
			return nightCommandOutcome{}, &p, nil
		}
		if len(gate.Overridden) > 0 {
			overrideAuditParams = nightInterlockOverrideAuditParams(gate.Overridden)
		}
	}

	// See nightFadeOutNightCommand's own identical comment: this
	// session's own interlocks are pinned at prepare-site time and never
	// change, so only the session's own lifecycle state can move between
	// this precheck and the transaction below, and
	// nightPowerDownPresentationApply already recomputes fresh from
	// whatever that state actually is.
	return h.nightRunExempt(ctx, now, nightCommandPowerDownPresentation, issuer, attributionDegraded, func(ctx context.Context, tx *store.Tx, cur *store.NightSessionRecord) (nightCommandOutcome, *v1.Problem, error) {
		return h.nightPowerDownPresentationApply(ctx, tx, cur, now, overrideAuditParams)
	})
}

// nightPowerDownPresentationApply is the tx-bound apply step, gate-free:
// every interlock decision on the shutdown transition itself has already
// been made by [handlers.nightPowerDownPresentationCommand] before this
// runs. The PowerPhase resolution below is bookkeeping, not a dispatch,
// and consults no interlock.
func (h *handlers) nightPowerDownPresentationApply(ctx context.Context, tx *store.Tx, current *store.NightSessionRecord, now time.Time, overrideAuditParams []map[string]any) (nightCommandOutcome, *v1.Problem, error) {
	if current == nil {
		return nightCommandOutcome{result: nightSyntheticInactiveSession(now), outcome: nightOutcomeIdempotentNoOp}, nil, nil
	}
	if current.State == nightStateStopped && current.PowerPhase != "" {
		// Already stopped AND the power phase already resolved - truly
		// nothing left to do. A session that reached stopped via
		// fade-out-night has State==stopped but PowerPhase=="",
		// and must still be allowed through below to resolve it.
		return nightCommandOutcome{result: *current, outcome: nightOutcomeIdempotentNoOp}, nil, nil
	}
	next, changed := applyNightShutdownEffect(now, *current, "power-down")
	if next.State == nightStateStopped && next.PowerPhase == "" {
		phase := "not_configured"
		if payload, err := h.getPinnedNightSessionPayloadTx(ctx, tx, next); err == nil &&
			payload.SiteControl != nil && payload.SiteControl.PresentationPowerOff != nil {
			phase = nightPowerPhaseConfiguredNotDispatched
		}
		next.PowerPhase = phase
		changed = true
	}
	if !changed {
		return nightCommandOutcome{result: *current, outcome: nightOutcomeIdempotentNoOp}, nil, nil
	}
	out := nightCommandOutcome{result: next, outcome: nightOutcomeApplied, persist: "update"}
	if len(overrideAuditParams) > 0 {
		out.auditParams = map[string]any{"interlockOverrides": overrideAuditParams}
	}
	return out, nil, nil
}

// nightEndSessionDecide is the operator-recovery action: abandons
// the current session, reaches stopped, launches nothing. It deliberately
// leaves Degraded unchanged - resuming a session ShowMesh cannot confirm
// is the guess ADR-038 forbids, so recovery is a fresh prepare-site, not a
// clear-and-continue.
func (h *handlers) nightEndSessionDecide(now time.Time, current *store.NightSessionRecord) nightCommandOutcome {
	if current == nil {
		return nightCommandOutcome{result: nightSyntheticInactiveSession(now), outcome: nightOutcomeIdempotentNoOp}
	}
	if current.State == nightStateStopped {
		return nightCommandOutcome{result: *current, outcome: nightOutcomeIdempotentNoOp}
	}
	next := *current
	next.State = nightStateStopped
	next.StateEnteredAt = now
	next.ArmedShowID = ""
	next.ShowCommitted = false
	if !next.AdmissionClosed {
		next.AdmissionClosed = true
		t := now
		next.AdmissionClosedAt = &t
	}
	return nightCommandOutcome{result: next, outcome: nightOutcomeApplied, persist: "update"}
}

// --- readiness ---

// nightCheckState is one readiness check's own state - a superset of
// observation.Health, adding nightCheckStateNotVerifiable for a check that
// is structurally incapable of ever reporting anything else (owner
// ruling, ADR-029 decision 4's rule applied to readiness: an indicator
// that can never change colour teaches the operator to ignore it, and a
// check that is always "unknown" is that defect wearing the honest
// label). A not_verifiable check is excluded from the aggregate outcome
// below but always listed, so "ready" means "everything checkable checked
// out, and here is what is not checkable" rather than being permanently
// unreachable.
// The two resting-playlist check name prefixes. A readiness result names
// which configured playlist a check was about, since the two can differ.
const (
	nightCheckPrefixResting    = "resting"
	nightCheckPrefixEndOfNight = "resting-end-of-night"
)

type nightCheckState string

const (
	nightCheckStateNotVerifiable nightCheckState = "not_verifiable"

	// nightCheckStateNotConfigured is RESTING-MODE.md section 13's own
	// distinction from not_verifiable: absent OPTIONAL configuration
	// (nothing was ever asked for) is a different fact from a check this
	// coordinator is structurally incapable of ever verifying. Excluded
	// from the aggregate outcome the same way not_verifiable is.
	nightCheckStateNotConfigured nightCheckState = "not_configured"
)

type nightReadinessCheck struct {
	name   string
	health nightCheckState
	reason string
}

// nightRunReadinessCommand computes readiness OUTSIDE any store
// transaction and persists the result inside a short one: FPP HTTP reads
// and the asset blob copy inside nightComputeReadinessChecks can each
// take seconds, and the store's single connection (SetMaxOpenConns(1))
// must never be held across that or every other request stalls. Its own
// phase="run-readiness" interlocks are dispatched live, for the identical
// reason: a rule that withholds this phase refuses the command before
// anything else is computed or persisted.
func (h *handlers) nightRunReadinessCommand(ctx context.Context, now time.Time, issuer identity.AuditEntry, overrides []nightInterlockOverrideRequest, callerHasOverrideScope bool) (nightCommandOutcome, *v1.Problem, error) {
	current, ok, err := h.deps.NightSessions.GetCurrentNightSession(ctx)
	if err != nil {
		return nightCommandOutcome{}, nil, err
	}
	if problem := nightValidateReadinessEpoch(&current, ok); problem != nil {
		return nightCommandOutcome{}, problem, nil
	}

	payload, err := h.getPinnedNightSessionPayload(ctx, current)
	if err != nil {
		return nightCommandOutcome{}, nil, err
	}

	// D4, this seam's safety review round: a phase="run-readiness" rule's
	// signal action was previously dispatched TWICE per call, once here
	// (to decide the gate) and once more inside nightComputeReadinessChecks
	// (to populate the displayed check), which is both a real duplicate
	// message to the site and a correctness defect: the gate acted on the
	// first read while the operator saw the second, so the displayed
	// check was never the evidence the command actually acted on. Every
	// configured rule is now dispatched exactly once, here, and the same
	// result feeds both the gate and the displayed readiness checks.
	dispatchCtx, cancel := nightBoundInterlockDispatch(ctx)
	interlockChecks := h.nightComputeInterlockChecks(dispatchCtx, payload.Interlocks)
	cancel()
	// age is zero: interlockChecks was just dispatched live, above, so it
	// is always at least as fresh as any rule's own freshnessSeconds could
	// require.
	gate := nightEvaluatePhaseInterlockGate(payload, config.NightInterlockPhaseRunReadiness, interlockChecks, 0, overrides, callerHasOverrideScope)
	if len(gate.Withheld) > 0 {
		p := nightNotReadyProblem(nightInterlockGateProblem(config.NightInterlockPhaseRunReadiness, gate.Withheld).detail())
		return nightCommandOutcome{}, &p, nil
	}
	var overrideAuditParams []map[string]any
	if len(gate.Overridden) > 0 {
		overrideAuditParams = nightInterlockOverrideAuditParams(gate.Overridden)
	}

	checks, outcome := h.nightComputeReadinessChecks(ctx, now, payload, interlockChecks)
	checksJSON, err := json.Marshal(nightEncodeChecks(checks))
	if err != nil {
		return nightCommandOutcome{}, nil, fmt.Errorf("api: encode night readiness checks: %w", err)
	}

	return h.nightRunGated(ctx, now, nightCommandRunReadiness, issuer, func(ctx context.Context, tx *store.Tx, curTx *store.NightSessionRecord) (nightCommandOutcome, *v1.Problem, error) {
		var curVal store.NightSessionRecord
		if curTx != nil {
			curVal = *curTx
		}
		if problem := nightValidateReadinessEpoch(&curVal, curTx != nil); problem != nil {
			return nightCommandOutcome{}, problem, nil
		}
		if curTx.ID != current.ID {
			p := nightNotReadyProblem("run-readiness: the preparation epoch changed while readiness was being computed; run run-readiness again")
			return nightCommandOutcome{}, &p, nil
		}
		rec := store.NightReadinessRecord{
			ID: uuid.NewString(), SessionID: curTx.ID, EpochID: curTx.ID,
			CompletedAt: now, Outcome: outcome, ChecksJSON: string(checksJSON),
		}
		if err := tx.CreateNightReadiness(ctx, rec); err != nil {
			return nightCommandOutcome{}, nil, err
		}
		next := *curTx
		next.ReadinessID = rec.ID
		out := nightCommandOutcome{result: next, outcome: nightOutcomeApplied, persist: "update"}
		if len(overrideAuditParams) > 0 {
			out.auditParams = map[string]any{"interlockOverrides": overrideAuditParams}
		}
		return out, nil, nil
	})
}

// nightValidateReadinessEpoch is run-readiness's own state gate (invariant
// 2), shared by the pre-read and the tx-bound re-check.
func nightValidateReadinessEpoch(current *store.NightSessionRecord, ok bool) *v1.Problem {
	if !ok || current == nil {
		p := nightNotReadyProblem("run-readiness: no preparation epoch is open; run prepare-site first")
		return &p
	}
	if current.Degraded && current.State != nightStateStopped {
		p := nightAmbiguousProblem(fmt.Sprintf(nightDegradedGuidance, current.DegradedReason))
		return &p
	}
	switch current.State {
	case nightStateInactive, nightStateStopped, nightStateFadingOut:
		p := nightNotReadyProblem("run-readiness: no preparation epoch is open; run prepare-site first")
		return &p
	}
	return nil
}

// nightComputeReadinessChecks runs fpp.reachable for every FPP instance
// the pinned night.session revision references, plus the FPP-facing
// checks in nightasset.go: the pinned resting FSEQ asset's duration, the
// resting playlist's idle-read shape, the show playlist's presence, and
// whether the exact deployed FSEQ variant can be confirmed
// (not_verifiable, excluded from outcome - see [nightCheckState]). None
// of this touches the store transactionally; every read here is the
// ordinary non-tx form.
func (h *handlers) nightComputeReadinessChecks(ctx context.Context, now time.Time, payload config.NightSessionPayload, interlockChecks []nightReadinessCheck) ([]nightReadinessCheck, string) {
	instanceIDs := map[string]bool{payload.ShowPlaylist.FPPInstanceID: true, payload.Resting.FPPInstanceID: true}
	var checks []nightReadinessCheck
	worst := nightHealthHealthy()
	for id := range instanceIDs {
		if id == "" {
			continue
		}
		checks = append(checks, h.nightCheckFPPReachable(ctx, now, id))
	}

	cueOffsets := append(nightParseCueOffsets(payload.EnterShow.Cues), nightParseCueOffsets(payload.EnterResting.Cues)...)
	checks = append(checks, nightCheckRestingAssetDuration(ctx, h.deps, h.deps.Assets, payload.Show, payload.Resting.TimelineAsset, cueOffsets))
	// The end-of-night playlist is dispatched by the same controller and so
	// gets the same checks, under its own names. It defaults to the
	// ordinary resting playlist, so it is only checked separately when the
	// operator actually configured a different one.
	type restingPlaylistCheck struct {
		prefix, playlist string
		// The inter-show playlist must be a single item, because the show
		// boundary is derived from that item's length. The end-of-night
		// loop has no boundary and may hold several.
		singleItem bool
	}
	restingPlaylists := []restingPlaylistCheck{{nightCheckPrefixResting, payload.Resting.Playlist, true}}
	if payload.Resting.EndOfNightPlaylist != "" && payload.Resting.EndOfNightPlaylist != payload.Resting.Playlist {
		restingPlaylists = append(restingPlaylists, restingPlaylistCheck{nightCheckPrefixEndOfNight, payload.Resting.EndOfNightPlaylist, false})
	}
	restingEndpoint, restingEndpointOK, restingEndpointErr := h.resolveFPPEndpoint(ctx, payload.Resting.FPPInstanceID)
	for _, p := range restingPlaylists {
		if restingEndpointErr == nil && restingEndpointOK {
			checks = append(checks, nightCheckRestingPlaylistShape(ctx, restingEndpoint, p.prefix, p.playlist, p.singleItem))
		} else {
			checks = append(checks, nightReadinessCheck{name: p.prefix + ":playlist-shape:" + p.playlist, health: nightHealthUnknown(), reason: "no configured FPP instance to read playlist " + p.playlist + " from"})
		}
	}
	if endpoint, ok, err := h.resolveFPPEndpoint(ctx, payload.ShowPlaylist.FPPInstanceID); err == nil && ok {
		checks = append(checks, nightCheckShowPlaylistPresent(ctx, endpoint, payload.ShowPlaylist.Playlist))
	} else {
		checks = append(checks, nightReadinessCheck{name: "show:playlist-present:" + payload.ShowPlaylist.Playlist, health: nightHealthUnknown(), reason: "no configured FPP instance to read the show playlist definition from"})
	}
	for _, p := range restingPlaylists {
		checks = append(checks, nightCheckRestingAssetExactVariant(p.prefix, p.playlist))
	}
	checks = append(checks, h.nightCheckFirstOutwardCueConfirmable(ctx, payload.EnterShow.Cues))
	checks = append(checks, nightCheckNoUnbuiltBrightnessComposition("enterShow", payload.EnterShow.Cues))
	checks = append(checks, nightCheckNoUnbuiltBrightnessComposition("enterResting", payload.EnterResting.Cues))
	// Track F seam F6: every configured interlock, disabled ones included,
	// gets its own check regardless of which phase this preparation epoch
	// is actually about to enter, per RESTING-MODE.md §13: "configured
	// observe-only interlocks report their current outcome without
	// blocking" and "blocking interlocks for the phase being entered have
	// fresh passing evidence... other-phase failures remain visible but do
	// not block." interlockChecks is computed exactly once by the caller
	// (nightRunReadinessCommand), shared with its own phase="run-readiness"
	// gate, and never recomputed here; see that caller's own comment for
	// why a second live dispatch of the same signal is a real defect, not
	// a redundant safety margin.
	checks = append(checks, interlockChecks...)

	// Track F seam F5: resting.backgroundAudio's own readiness (RESTING-
	// MODE.md §13), only when it is configured at all.
	if ba := payload.Resting.BackgroundAudio; ba != nil {
		checks = append(checks, h.nightCheckBackgroundAudioAssets(ctx, payload.Show, ba))
		checks = append(checks, nightCheckBackgroundAudioItemTransition(ba))
		checks = append(checks, nightCheckAudioOutputCapabilities(ba.OutputNodeID()))
	}
	allCues := append(append([]config.NightSessionCue{}, payload.EnterShow.Cues...), payload.EnterResting.Cues...)
	checks = append(checks, nightCheckAnnouncementAssets(allCues))
	checks = append(checks, h.nightCheckAnnouncementPolicyEnforceable(ctx, allCues, payload))

	for _, c := range checks {
		if c.health == nightCheckStateNotVerifiable || c.health == nightCheckStateNotConfigured {
			continue
		}
		if nightHealthSeverity(c.health) > nightHealthSeverity(worst) {
			worst = c.health
		}
	}
	var outcome string
	switch worst {
	case nightHealthHealthy():
		outcome = "ready"
	case nightHealthUnknown():
		outcome = "unknown"
	default:
		outcome = "not_ready"
	}
	return checks, outcome
}

func nightHealthSeverity(h nightCheckState) int {
	switch h {
	case nightCheckState(observation.HealthFailed):
		return 3
	case nightCheckState(observation.HealthDegraded):
		return 2
	case nightCheckState(observation.HealthUnknown):
		return 1
	default:
		return 0
	}
}

func nightEncodeChecks(checks []nightReadinessCheck) []v1.NightReadinessCheck {
	out := make([]v1.NightReadinessCheck, 0, len(checks))
	for _, c := range checks {
		out = append(out, v1.NightReadinessCheck{Name: c.name, State: string(c.health), Reason: c.reason})
	}
	return out
}

// nightCheckFPPReachable checks exactly one signal, fpp.reachable, for one
// FPP instance. It is one of several checks nightRunReadinessTx now runs
// (Track F seam F3 added the others in nightasset.go) - see the OpenAPI
// NightReadinessCheck schema for the full, current list; a caller must
// not read a healthy result on THIS check as evidence any other check
// passed.
func (h *handlers) nightCheckFPPReachable(ctx context.Context, now time.Time, instanceID string) nightReadinessCheck {
	kind := observation.ResourceFPP
	signal := observation.SignalID("fpp.reachable")
	obs, err := h.deps.Observations.ListObservations(ctx, ObservationFilter{
		ResourceKind: &kind, ResourceID: &instanceID, Signal: &signal,
	})
	name := "fpp:" + instanceID + ":reachable"
	if err != nil {
		return nightReadinessCheck{name: name, health: nightHealthUnknown(), reason: "failed to read fpp.reachable evidence: " + err.Error()}
	}
	if len(obs) == 0 {
		return nightReadinessCheck{name: name, health: nightHealthUnknown(), reason: "no fpp.reachable evidence recorded for this instance"}
	}
	health := observation.DeriveHealth(obs[0], now, func(v any) observation.Health {
		if b, ok := v.(bool); ok && b {
			return observation.HealthHealthy
		}
		return observation.HealthFailed
	})
	reason := ""
	if health != observation.HealthHealthy {
		reason = "fpp.reachable evidence state: " + string(obs[0].StateAt(now))
	}
	return nightReadinessCheck{name: name, health: nightCheckState(health), reason: reason}
}

// getPinnedNightSessionPayloadTx is [handlers.getPinnedNightSessionPayload]
// (nightloop.go) read through tx instead of h.deps.Config, necessary so a
// gated decide function (which already holds the store's one connection
// via tx; resolveActiveNightSessionConfigTx's own doc comment states the
// identical reason) never tries to acquire a second one and deadlock the
// single-connection pool.
func (h *handlers) getPinnedNightSessionPayloadTx(ctx context.Context, tx *store.Tx, rec store.NightSessionRecord) (config.NightSessionPayload, error) {
	rev, err := tx.GetConfigRevision(ctx, config.NightSessionConfigKind, rec.ConfigObjectID, rec.ConfigRevision)
	if err != nil {
		return config.NightSessionPayload{}, fmt.Errorf("api: get pinned night.session revision (tx) %s/%d: %w", rec.ConfigObjectID, rec.ConfigRevision, err)
	}
	var payload config.NightSessionPayload
	if err := jsonUnmarshalStrict(rev.PayloadJSON, &payload); err != nil {
		return config.NightSessionPayload{}, fmt.Errorf("api: decode pinned night.session payload (tx): %w", err)
	}
	return payload, nil
}

// nightDecodeWireChecks converts a stored readiness result's own decoded
// wire checks back into this package's internal [nightReadinessCheck]
// shape, the only shape [nightEvaluatePhaseInterlockGate] accepts. A
// malformed or missing entry simply is not found by name, which
// nightEvaluatePhaseInterlockGate already treats as evidence-unavailable.
func nightDecodeWireChecks(wire []v1.NightReadinessCheck) []nightReadinessCheck {
	out := make([]nightReadinessCheck, 0, len(wire))
	for _, w := range wire {
		out = append(out, nightReadinessCheck{name: w.Name, health: nightCheckState(w.State), reason: w.Reason})
	}
	return out
}

// --- config resolution ---

// resolveActiveNightSessionConfigTx reads night.session.active and, when
// it names a session, that session's current revision, both via tx -
// necessary so the whole prepare-site decision shares one transaction
// without deadlocking the single-connection pool against a
// Store-level read.
func (h *handlers) resolveActiveNightSessionConfigTx(ctx context.Context, tx *store.Tx) (objectID string, revision int64, problem *v1.Problem, err error) {
	activeObj, aerr := tx.GetConfigObject(ctx, config.NightSessionActiveConfigKind, config.NightSessionActiveObjectID)
	if aerr != nil {
		if errors.Is(aerr, store.ErrConfigObjectNotFound) {
			p := nightNotReadyProblem("prepare-site: no night.session.active pointer is configured; PUT /api/v1/config/night.session.active first")
			return "", 0, &p, nil
		}
		return "", 0, nil, aerr
	}
	if activeObj.CurrentRevision == 0 {
		p := nightNotReadyProblem("prepare-site: no night.session.active pointer is configured; PUT /api/v1/config/night.session.active first")
		return "", 0, &p, nil
	}
	activeRev, aerr := tx.GetConfigRevision(ctx, config.NightSessionActiveConfigKind, config.NightSessionActiveObjectID, activeObj.CurrentRevision)
	if aerr != nil {
		return "", 0, nil, aerr
	}
	var activePayload config.NightSessionActivePayload
	if err := jsonUnmarshalStrict(activeRev.PayloadJSON, &activePayload); err != nil {
		return "", 0, nil, fmt.Errorf("api: decode night.session.active payload: %w", err)
	}
	if activePayload.Session == "" {
		p := nightNotReadyProblem("prepare-site: night.session.active names no session; PUT /api/v1/config/night.session.active first")
		return "", 0, &p, nil
	}

	obj, oerr := tx.GetConfigObject(ctx, config.NightSessionConfigKind, activePayload.Session)
	if oerr != nil || obj.CurrentRevision == 0 {
		if oerr != nil && !errors.Is(oerr, store.ErrConfigObjectNotFound) {
			return "", 0, nil, oerr
		}
		p := nightNotReadyProblem(fmt.Sprintf("prepare-site: night.session.active names %q, which has no active revision", activePayload.Session))
		return "", 0, &p, nil
	}
	return activePayload.Session, obj.CurrentRevision, nil, nil
}

// --- wire mapping ---

func mapNightSessionState(ctx context.Context, deps Dependencies, rec store.NightSessionRecord, now time.Time, maxAge time.Duration) v1.NightSessionState {
	out := v1.NightSessionState{
		ID: rec.ID, ConfigObjectID: rec.ConfigObjectID, ConfigRevision: rec.ConfigRevision,
		State: rec.State, StateEnteredAt: formatTime(rec.StateEnteredAt), Cycle: rec.Cycle,
		FinalShowRequested: rec.FinalShowRequested, FinalShowRequestedAt: formatTimePtr(rec.FinalShowRequestedAt),
		AdmissionClosed: rec.AdmissionClosed, AdmissionClosedAt: formatTimePtr(rec.AdmissionClosedAt),
		ShutdownIntent: rec.ShutdownIntent, ArmedShowID: rec.ArmedShowID, ShowCommitted: rec.ShowCommitted,
		Degraded: rec.Degraded, DegradedReason: rec.DegradedReason, AttributionDegraded: rec.AttributionDegraded,
		Transition: mapNightTransition(rec),
	}
	if !rec.UpdatedAt.IsZero() {
		out.UpdatedAt = formatTime(rec.UpdatedAt)
	} else {
		out.UpdatedAt = formatTime(now)
	}

	out.Authorization = mapNightAuthorization(rec)
	out.PowerPhase = mapNightPowerPhase(rec)
	out.Readiness = mapNightReadiness(ctx, deps, rec, now, maxAge)
	out.Cues = mapNightCues(ctx, deps, rec)
	out.BackgroundAudio = mapNightBackgroundAudio(ctx, deps, rec)
	return out
}

// mapNightBackgroundAudio is RESTING-MODE.md section 14's own surface for
// Track F seam F5's background-audio/announcement steps
// (nightbackgroundaudio.go's own durable log), on the SAME "read failure
// states its own reason, never a silently empty list" rule mapNightCues
// already follows. Empty Steps with State Recorded is a legitimate
// reading (backgroundAudio not configured, or never started this
// cycle), distinct from a read failure.
func mapNightBackgroundAudio(ctx context.Context, deps Dependencies, rec store.NightSessionRecord) v1.NightBackgroundAudio {
	if rec.ID == "" {
		return v1.NightBackgroundAudio{State: v1.NightEvidenceUnknown, Reason: "no session", Steps: []v1.NightBackgroundAudioStep{}}
	}
	rows, err := deps.NightSessions.ListNightCueOutboxRowsForPhasePrefix(ctx, rec.ID, nightPhaseRestingBackground)
	if err != nil {
		return v1.NightBackgroundAudio{State: v1.NightEvidenceUnknown, Reason: "failed to read the background-audio step log: " + err.Error(), Steps: []v1.NightBackgroundAudioStep{}}
	}
	// The announcement-session sequence is read here too, under its own
	// phase family and tagged with its own sequence name. Its clear and
	// start steps are as durable as background audio's and carry the same
	// class of failure - a refused clear means a previous announcement may
	// still be playing and still holding the bed ducked - so surfacing one
	// sequence and not the other would leave that reachable from nothing
	// but a log line (ADR-039).
	announcementRows, err := deps.NightSessions.ListNightCueOutboxRowsForPhasePrefix(ctx, rec.ID, nightPhaseAnnouncementSession)
	if err != nil {
		return v1.NightBackgroundAudio{State: v1.NightEvidenceUnknown, Reason: "failed to read the announcement-session step log: " + err.Error(), Steps: []v1.NightBackgroundAudioStep{}}
	}
	out := make([]v1.NightBackgroundAudioStep, 0, len(rows)+len(announcementRows))
	for _, row := range rows {
		step, ok := nightParseBackgroundAudioRow(row)
		if !ok {
			continue
		}
		out = append(out, nightMapAudioStep(row, v1.NightAudioSequenceBackground, step.Kind))
	}
	for _, row := range announcementRows {
		kind, ok := nightParseAnnouncementRow(row)
		if !ok {
			continue
		}
		out = append(out, nightMapAudioStep(row, v1.NightAudioSequenceAnnouncement, kind))
	}
	pinnedMaxGainDb, err := nightPinnedBackgroundMaxGainDb(ctx, deps, rec)
	if err != nil {
		return v1.NightBackgroundAudio{State: v1.NightEvidenceUnknown, Reason: "failed to read the pinned background-audio configuration: " + err.Error(), Steps: out}
	}
	return v1.NightBackgroundAudio{State: v1.NightEvidenceRecorded, Steps: out, PinnedMaxGainDb: pinnedMaxGainDb}
}

// nightPinnedBackgroundMaxGainDb is the background-audio ceiling the
// RUNNING session pinned when it started - rec's own pinned night.session
// revision's resting.backgroundAudio.maxGainDb, never the value
// night.session.resting's config currently holds, which can differ across
// a later revision (owner ruling 2026-08-28). Nil, nil means that pinned
// revision configures no background audio at all - a legitimate reading,
// not a read failure.
func nightPinnedBackgroundMaxGainDb(ctx context.Context, deps Dependencies, rec store.NightSessionRecord) (*float64, error) {
	payload, err := nightPinnedNightSessionPayload(ctx, deps, rec)
	if err != nil {
		return nil, err
	}
	if payload.Resting.BackgroundAudio == nil {
		return nil, nil
	}
	gain := payload.Resting.BackgroundAudio.MaxGainDb
	return &gain, nil
}

// mapNightCues fills RESTING-MODE.md §14's per-cue outcome. A read
// failure at any step reports NightEvidenceUnknown with its own reason,
// never a claim about any individual cue's state.
func mapNightCues(ctx context.Context, deps Dependencies, rec store.NightSessionRecord) v1.NightCues {
	unreadable := func(reason string) v1.NightCues {
		return v1.NightCues{State: v1.NightEvidenceUnknown, Reason: reason, Cues: []v1.NightCue{}}
	}
	if rec.ID == "" || rec.ConfigObjectID == "" {
		return unreadable("no session")
	}
	rev, err := deps.Config.GetConfigRevision(ctx, config.NightSessionConfigKind, rec.ConfigObjectID, rec.ConfigRevision)
	if err != nil {
		return unreadable("failed to read the pinned night.session revision: " + err.Error())
	}
	var payload config.NightSessionPayload
	if err := jsonUnmarshalStrict(rev.PayloadJSON, &payload); err != nil {
		return unreadable("failed to decode the pinned night.session revision: " + err.Error())
	}
	rows, err := deps.NightSessions.ListNightCueOutboxRows(ctx, rec.ID, rec.Cycle)
	if err != nil {
		return unreadable("failed to read the cue outbox: " + err.Error())
	}
	byKey := make(map[string]store.NightCueOutboxRecord, len(rows))
	for _, row := range rows {
		byKey[row.Phase+"\x00"+row.CueName] = row
	}

	out := make([]v1.NightCue, 0, len(payload.EnterShow.Cues)+len(payload.EnterResting.Cues))
	out = appendMappedNightCues(out, nightPhaseEnterShow, sortedNightCues(payload.EnterShow.Cues), byKey)
	out = appendMappedNightCues(out, nightPhaseEnterResting, sortedNightCues(payload.EnterResting.Cues), byKey)
	// The fade-out phase replays the enterShow definitions, so it is only
	// listed once rows for it exist rather than always showing a second,
	// permanently not_dispatched copy of that list.
	out = appendDispatchedNightCues(out, nightPhaseFadeOut, sortedNightCues(payload.EnterShow.Cues), byKey)
	return v1.NightCues{State: v1.NightEvidenceRecorded, Cues: out}
}

func appendMappedNightCues(out []v1.NightCue, phase string, cues []config.NightSessionCue, byKey map[string]store.NightCueOutboxRecord) []v1.NightCue {
	for _, cue := range cues {
		row, has := byKey[phase+"\x00"+cue.Name]
		out = append(out, mapNightCue(phase, cue, row, has))
	}
	return out
}

// appendDispatchedNightCues lists only the cues of phase that already have
// an outbox row in this cycle.
func appendDispatchedNightCues(out []v1.NightCue, phase string, cues []config.NightSessionCue, byKey map[string]store.NightCueOutboxRecord) []v1.NightCue {
	for _, cue := range cues {
		row, has := byKey[phase+"\x00"+cue.Name]
		if !has {
			continue
		}
		out = append(out, mapNightCue(phase, cue, row, true))
	}
	return out
}

// mapNightCue joins one configured cue against its outbox row, if any.
func mapNightCue(phase string, cue config.NightSessionCue, row store.NightCueOutboxRecord, hasRow bool) v1.NightCue {
	out := v1.NightCue{Name: cue.Name, Phase: phase, Role: cue.Role, Action: cue.Action}
	if !hasRow {
		out.State = nightCueStateNotDispatched
		out.Reason = "this cue has not been dispatched in the current cycle"
		return out
	}
	rev := row.ActionRevision
	out.ActionRevision = &rev
	out.State = row.State
	out.Outcome = row.Outcome
	out.Reason = row.OutcomeReason
	out.DispatchedAt = formatTimePtr(row.DispatchedAt)
	out.ResolvedAt = formatTimePtr(row.ResolvedAt)
	return out
}

func mapNightAuthorization(rec store.NightSessionRecord) v1.NightAuthorization {
	if rec.Issuer.IsZero() {
		return v1.NightAuthorization{
			State:  v1.NightEvidenceUnknown,
			Reason: "no lifecycle command has been attributed to this session; autonomous actions still run, as the night controller, and are recorded as attribution-degraded",
		}
	}
	return v1.NightAuthorization{
		State:         v1.NightEvidenceRecorded,
		PrincipalID:   rec.Issuer.PrincipalID,
		PrincipalName: rec.Issuer.PrincipalName,
		Command:       rec.Issuer.Command,
		RecordedAt:    formatTimePtr(rec.Issuer.RecordedAt),
	}
}

// mapNightPowerPhase: a power-down whose phase has not resolved yet must
// say why it is still pending, not repeat the "has not been requested"
// text that is only true before any shutdown was requested at all.
func mapNightPowerPhase(rec store.NightSessionRecord) v1.NightPhaseEvidence {
	switch rec.PowerPhase {
	case "":
		if rec.ShutdownIntent == "power-down" {
			return v1.NightPhaseEvidence{State: v1.NightEvidenceUnknown, Reason: "power-down-presentation was requested and is deferred until playback has been observed stopped"}
		}
		return v1.NightPhaseEvidence{State: v1.NightEvidenceUnknown, Reason: "power-down-presentation has not been requested"}
	case "not_configured":
		return v1.NightPhaseEvidence{State: v1.NightEvidenceNotConfigured, Reason: "no site-power configuration is present"}
	case nightPowerPhaseConfiguredNotDispatched:
		return v1.NightPhaseEvidence{
			State: v1.NightEvidenceUnknown,
			Reason: "siteControl.presentationPowerOff is configured, but this build does not yet dispatch it automatically; " +
				"remove power by invoking the configured action directly (POST /api/v1/actions/{id}/invocations) or through a configured force-power-off action",
		}
	default:
		return v1.NightPhaseEvidence{State: v1.NightEvidenceRecorded, Reason: rec.PowerPhase}
	}
}

// mapNightReadiness: a store error is distinguished from
// "no result exists yet", and a corrupt checks_json blob states the
// decode failure rather than silently rendering as zero checks.
func mapNightReadiness(ctx context.Context, deps Dependencies, rec store.NightSessionRecord, now time.Time, maxAge time.Duration) v1.NightReadiness {
	empty := v1.NightReadiness{Checks: []v1.NightReadinessCheck{}}
	if rec.ID == "" {
		empty.State = v1.NightEvidenceUnknown
		empty.Reason = "no session"
		return empty
	}
	readiness, err := deps.NightSessions.GetLatestNightReadiness(ctx, rec.ID)
	if errors.Is(err, store.ErrNightReadinessNotFound) {
		empty.State = v1.NightEvidenceUnknown
		empty.Reason = "no readiness result recorded for this preparation epoch"
		return empty
	}
	if err != nil {
		empty.State = v1.NightEvidenceUnknown
		empty.Reason = "failed to read the readiness result: " + err.Error()
		return empty
	}
	var checks []v1.NightReadinessCheck
	decodeReason := ""
	if jerr := json.Unmarshal([]byte(readiness.ChecksJSON), &checks); jerr != nil {
		checks = nil
		decodeReason = "stored checks payload could not be decoded: " + jerr.Error()
	}
	if checks == nil {
		checks = []v1.NightReadinessCheck{}
	}
	age := now.Sub(readiness.CompletedAt)
	return v1.NightReadiness{
		State: v1.NightEvidenceRecorded, Reason: decodeReason, Outcome: readiness.Outcome,
		EpochID: readiness.EpochID, CompletedAt: formatTime(readiness.CompletedAt),
		SameEpoch: readiness.EpochID == rec.ID,
		Fresh:     age >= 0 && age <= maxAge,
		Checks:    checks,
	}
}

// --- problems ---

const (
	ProblemTypeNightNotReady         = problemBaseURI + "night-not-ready"
	ProblemTypeNightStateRejected    = problemBaseURI + "night-state-rejected"
	ProblemTypeNightAmbiguous        = problemBaseURI + "night-ambiguous"
	ProblemTypeNightAuditUnavailable = problemBaseURI + "night-command-refused-audit-unavailable"
)

// nightNotReadyProblem is showmeshctl's exitNightNotReady (26).
func nightNotReadyProblem(detail string) v1.Problem {
	return v1.Problem{Type: ProblemTypeNightNotReady, Title: "Night session not ready", Status: http.StatusConflict, Detail: detail}
}

// nightStateRejectedProblem is showmeshctl's exitNightStateRejected (27).
func nightStateRejectedProblem(detail string) v1.Problem {
	return v1.Problem{Type: ProblemTypeNightStateRejected, Title: "Night command rejected by current state", Status: http.StatusConflict, Detail: detail}
}

// nightAmbiguousProblem is showmeshctl's exitNightAmbiguous (28).
func nightAmbiguousProblem(detail string) v1.Problem {
	return v1.Problem{Type: ProblemTypeNightAmbiguous, Title: "Night session is degraded", Status: http.StatusConflict, Detail: detail}
}

// ReconcileNightSessionOnStartup runs once, synchronously, before the
// coordinator starts serving requests (see [ReconcileStrandedFPPCommands]
// for the identical timing rationale). Pending cue outbox rows are
// resolved first, then the session's own state is reconciled against fresh
// FPP evidence. A state this build cannot confirm safe to resume is marked
// degraded rather than resumed by guess; end-session remains the way out.
func ReconcileNightSessionOnStartup(ctx context.Context, deps Dependencies, now func() time.Time, logger *slog.Logger) error {
	deps = deps.withDefaults()
	rec, ok, err := deps.NightSessions.GetCurrentNightSession(ctx)
	if err != nil {
		return fmt.Errorf("api: reconcile night session on startup: %w", err)
	}
	if !ok || rec.Degraded {
		return nil
	}
	h := &handlers{deps: deps, clock: now, logger: logger}
	at := now()

	if err := h.nightReconcileCueOutbox(ctx, at, rec); err != nil {
		return fmt.Errorf("api: reconcile night session on startup: %w", err)
	}

	reason := h.nightStartupDegradeReason(ctx, at, rec)
	if reason == "" {
		return nil
	}
	rec.Degraded = true
	rec.DegradedReason = reason
	if err := deps.NightSessions.UpdateNightSession(ctx, rec, at); err != nil {
		return fmt.Errorf("api: reconcile night session on startup: mark degraded: %w", err)
	}
	if logger != nil {
		logger.Warn("night session reconciliation: session marked degraded on startup", "sessionId", rec.ID, "state", rec.State, "reason", reason)
	}
	return nil
}

// nightStartupDegradeReason returns "" when rec may resume, or the reason
// it may not. RESTING-MODE.md §11's three resumable cases are live
// playback, the exact one-shot resting item, and end-of-night repeat; a
// session caught mid-transition is not among them.
func (h *handlers) nightStartupDegradeReason(ctx context.Context, now time.Time, rec store.NightSessionRecord) string {
	switch rec.State {
	case nightStateLive:
		return h.nightStartupReconcilePlayback(ctx, now, rec, nightAnchorPurposeShow,
			"the show playlist this session was playing")
	case nightStateRestingIntershow:
		return h.nightStartupReconcilePlayback(ctx, now, rec, nightAnchorPurposeRestingOneShot,
			"the one-shot resting item this session was playing")
	case nightStateEndOfNightResting:
		return h.nightStartupReconcilePlayback(ctx, now, rec, nightAnchorPurposeRestingRepeat,
			"the repeating end-of-night resting playlist")
	case nightStateTransitionToShow, nightStateTransitionToResting:
		return fmt.Sprintf(
			"coordinator restarted while the session was in %q, mid-transition, where this build cannot confirm what is safe to resume; run end-session, then prepare-site, to recover",
			rec.State)
	}
	return ""
}

// nightStartupReconcilePlayback checks rec's persisted anchor against fresh
// FPP evidence. Agreement resumes observation and launches nothing;
// disagreement, or evidence this coordinator cannot read at all, degrades
// with a stated reason.
func (h *handlers) nightStartupReconcilePlayback(ctx context.Context, now time.Time, rec store.NightSessionRecord, purpose, subject string) string {
	anchor, has := decodeNightContentAnchor(rec.ContentAnchorJSON)
	if !has || anchor.Purpose != purpose {
		return fmt.Sprintf(
			"coordinator restarted in %q with no usable content anchor for %s; run end-session, then prepare-site, to recover",
			rec.State, subject)
	}
	if anchor.ObservedAt.IsZero() {
		// Dispatched but never confirmed before the restart: the loop's own
		// pending-observation path re-derives it from fresh evidence.
		return ""
	}

	obs := nightObservePlayback(ctx, h.deps.Observations, anchor.FPPInstanceID, time.Time{}, now)
	if !obs.Current {
		return fmt.Sprintf(
			"coordinator restarted in %q and no current fpp.status evidence for instance %q is available to confirm %s; run end-session, then prepare-site, to recover",
			rec.State, anchor.FPPInstanceID, subject)
	}

	if bad, reason := nightBoundaryContradicted(anchor, obs, now); bad {
		return fmt.Sprintf(
			"coordinator restarted in %q and fresh evidence contradicts %s (%s); run end-session, then prepare-site, to recover",
			rec.State, subject, reason)
	}
	return ""
}

// nightCueActionIDs maps every configured cue's phase and name to the
// show.action it invokes. The fade-out phase replays the enterShow list.
func nightCueActionIDs(payload config.NightSessionPayload) map[string]string {
	out := make(map[string]string, 2*len(payload.EnterShow.Cues)+len(payload.EnterResting.Cues))
	for _, cue := range payload.EnterShow.Cues {
		out[nightPhaseEnterShow+"\x00"+cue.Name] = cue.Action
		out[nightPhaseFadeOut+"\x00"+cue.Name] = cue.Action
	}
	for _, cue := range payload.EnterResting.Cues {
		out[nightPhaseEnterResting+"\x00"+cue.Name] = cue.Action
	}
	return out
}

// nightReconcileCueOutbox resolves the current cycle's outbox before any
// boundary is reconstructed: a row left mid-dispatch by the restart, whose
// action carries no stable retry identity, becomes terminally ambiguous
// rather than being retried into a possible duplicate.
func (h *handlers) nightReconcileCueOutbox(ctx context.Context, now time.Time, rec store.NightSessionRecord) error {
	if rec.ID == "" {
		return nil
	}
	rows, err := h.deps.NightSessions.ListNightCueOutboxRows(ctx, rec.ID, rec.Cycle)
	if err != nil {
		return fmt.Errorf("list night cue outbox rows: %w", err)
	}
	if len(rows) == 0 {
		return nil
	}
	payload, err := h.getPinnedNightSessionPayload(ctx, rec)
	if err != nil {
		return fmt.Errorf("read the pinned night.session revision: %w", err)
	}
	actionByCue := nightCueActionIDs(payload)

	for _, row := range rows {
		if row.State != nightCueStateDispatched {
			continue
		}
		actionID, known := actionByCue[row.Phase+"\x00"+row.CueName]
		if !known {
			continue
		}
		action, aerr := nightResolveShowActionRevision(ctx, h.deps.Config, actionID, row.ActionRevision)
		if aerr != nil || nightCueRetryableByIdentity(action.Target) {
			continue
		}
		row.State = nightCueStateAmbiguous
		row.Outcome = nightCueOutcomeAmbiguous
		row.OutcomeReason = "the coordinator restarted after this cue was dispatched and before its outcome was recorded, and this action has no stable retry identity; end-session and prepare-site again to recover"
		resolvedAt := now
		row.ResolvedAt = &resolvedAt
		if uerr := h.deps.NightSessions.UpdateNightCueOutboxRow(ctx, row); uerr != nil {
			return fmt.Errorf("resolve stranded night cue row: %w", uerr)
		}
	}
	return nil
}

// nightMapAudioStep is one durable audio step's wire shape, shared by the
// background-audio and announcement-session sequences.
func nightMapAudioStep(row store.NightCueOutboxRecord, sequence, kind string) v1.NightBackgroundAudioStep {
	return v1.NightBackgroundAudioStep{
		Sequence: sequence, Phase: row.Phase, CueName: row.CueName, Kind: kind,
		ActionRevision: row.ActionRevision,
		State:          row.State, Outcome: row.Outcome, Reason: row.OutcomeReason,
		DispatchedAt: formatTimePtr(row.DispatchedAt), ResolvedAt: formatTimePtr(row.ResolvedAt),
	}
}
