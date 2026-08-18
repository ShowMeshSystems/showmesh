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
// advances a session out of transition-to-show or transition-to-resting —
// that needs FPP playback evidence and cue dispatch (seams F3/F4).
//
// No wall-clock scheduling anywhere: every deadline check is relative to
// h.now().

// Night lifecycle states — RESTING-MODE.md §3, exactly.
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

// Night lifecycle commands — RESTING-MODE.md §4. The path segment IS the
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
// its own path), are direction-safe and never gated on Degraded — a
// refusal here would be strictly worse than no coordinator at all, since
// it is a successful conversation that fires no fallback.
var nightExemptFromDegradedGate = map[string]bool{
	nightCommandFadeOutNight:          true,
	nightCommandPowerDownPresentation: true,
	nightCommandRequestFinalShow:      true,
}

const (
	nightOutcomeApplied        = "applied"
	nightOutcomeIdempotentNoOp = "idempotent_no_op"
)

// errNightCommandRefused is the sentinel a Tx closure returns to signal
// "roll back, no error occurred — a *v1.Problem describes the refusal",
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

// decodeNightCommandBody reads the optional {"idempotencyKey": string}
// body. An absent or empty body is valid — every field is optional.
func decodeNightCommandBody(r *http.Request) (idempotencyKey string, problem *v1.Problem) {
	if r.ContentLength == 0 {
		return "", nil
	}
	var body struct {
		IdempotencyKey string `json:"idempotencyKey"`
	}
	dec := json.NewDecoder(io.LimitReader(r.Body, maxNightCommandRequestBodyBytes+1))
	if err := dec.Decode(&body); err != nil && err != io.EOF {
		p := invalidParameterProblem("request body must be a JSON object matching {\"idempotencyKey\":string?}")
		return "", &p
	}
	return body.IdempotencyKey, nil
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
	idempotencyKey, problem := decodeNightCommandBody(r)
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

	switch {
	case cmd == nightCommandEndSession:
		out, problem, opErr = h.nightRunExempt(ctx, now, cmd, issuer, &attributionDegraded, func(ctx context.Context, tx *store.Tx, current *store.NightSessionRecord) (nightCommandOutcome, *v1.Problem, error) {
			return h.nightEndSessionDecide(now, current), nil, nil
		})
	case nightExemptFromDegradedGate[cmd]:
		out, problem, opErr = h.nightRunExempt(ctx, now, cmd, issuer, &attributionDegraded, func(ctx context.Context, tx *store.Tx, current *store.NightSessionRecord) (nightCommandOutcome, *v1.Problem, error) {
			return h.nightDecideExemptCommand(cmd, now, current)
		})
	default:
		out, problem, opErr = h.nightRunGated(ctx, now, cmd, issuer, func(ctx context.Context, tx *store.Tx, current *store.NightSessionRecord) (nightCommandOutcome, *v1.Problem, error) {
			return h.nightDecideGatedCommand(ctx, tx, cmd, now, current, idempotencyKey)
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
// commands' own decide functions.
func (h *handlers) nightDecideExemptCommand(cmd string, now time.Time, current *store.NightSessionRecord) (nightCommandOutcome, *v1.Problem, error) {
	switch cmd {
	case nightCommandRequestFinalShow:
		return h.nightRequestFinalShow(now, current)
	case nightCommandFadeOutNight:
		return h.nightFadeOutNight(now, current)
	case nightCommandPowerDownPresentation:
		return h.nightPowerDownPresentation(now, current)
	}
	return nightCommandOutcome{}, nil, fmt.Errorf("api: no exempt decide function for %q", cmd)
}

// nightDecideGatedCommand applies invariant 4 (a degraded, non-terminal
// session refuses every gated command) and then dispatches to the four
// admission-opening commands' own decide functions.
func (h *handlers) nightDecideGatedCommand(ctx context.Context, tx *store.Tx, cmd string, now time.Time, current *store.NightSessionRecord, idempotencyKey string) (nightCommandOutcome, *v1.Problem, error) {
	if current != nil && current.Degraded && current.State != nightStateStopped {
		p := nightAmbiguousProblem(fmt.Sprintf("night session is degraded (%s); run end-session, then prepare-site, to recover", current.DegradedReason))
		return nightCommandOutcome{}, &p, nil
	}
	switch cmd {
	case nightCommandPrepareSite:
		return h.nightPrepareSiteTx(ctx, tx, now, current, idempotencyKey)
	case nightCommandRunReadiness:
		return h.nightRunReadinessTx(ctx, tx, now, current)
	case nightCommandStartPreshow:
		return h.nightStartPreshow(now, current)
	case nightCommandStartNight:
		return h.nightStartNightTx(ctx, tx, now, current)
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
			return tx.CreateNightSession(ctx, out.result, now)
		case "update":
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
			if err := tx.CreateNightSession(ctx, out.result, now); err != nil {
				return identity.AuditEntry{}, err
			}
		case "update":
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
// command decides for when no session has EVER been created — never
// persisted.
func nightSyntheticInactiveSession(now time.Time) store.NightSessionRecord {
	return store.NightSessionRecord{State: nightStateInactive, StateEnteredAt: now}
}

// nightPrepareSiteTx is prepare-site's decide function. idempotencyKey,
// checked first, replays the original session rather than creating a
// second one.
func (h *handlers) nightPrepareSiteTx(ctx context.Context, tx *store.Tx, now time.Time, current *store.NightSessionRecord, idempotencyKey string) (nightCommandOutcome, *v1.Problem, error) {
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

	rec := store.NightSessionRecord{
		ID: uuid.NewString(), ConfigObjectID: objectID, ConfigRevision: revision,
		State: nightStatePreparing, StateEnteredAt: now, PrepareSiteIdempotencyKey: idempotencyKey,
	}
	return nightCommandOutcome{result: rec, outcome: nightOutcomeApplied, persist: "create"}, nil, nil
}

func (h *handlers) nightStartPreshow(now time.Time, current *store.NightSessionRecord) (nightCommandOutcome, *v1.Problem, error) {
	if current == nil {
		p := nightStateRejectedProblem("start-preshow has no unconsumed preparation epoch; run prepare-site first")
		return nightCommandOutcome{}, &p, nil
	}
	switch current.State {
	case nightStatePreparing:
		next := *current
		next.State = nightStatePreshow
		next.StateEnteredAt = now
		return nightCommandOutcome{result: next, outcome: nightOutcomeApplied, persist: "update"}, nil, nil
	case nightStatePreshow, nightStateTransitionToShow, nightStateLive, nightStateTransitionToResting, nightStateRestingIntershow, nightStateEndOfNightResting:
		return nightCommandOutcome{result: *current, outcome: nightOutcomeIdempotentNoOp}, nil, nil
	default: // fading-out, stopped
		p := nightStateRejectedProblem("start-preshow has no unconsumed preparation epoch; run prepare-site first")
		return nightCommandOutcome{}, &p, nil
	}
}

// nightStartNightTx encodes RESTING-MODE.md §4.4's table exactly.
func (h *handlers) nightStartNightTx(ctx context.Context, tx *store.Tx, now time.Time, current *store.NightSessionRecord) (nightCommandOutcome, *v1.Problem, error) {
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
		next := *current
		next.State = nightStateTransitionToShow
		next.StateEnteredAt = now
		next.ArmedShowID = uuid.NewString()
		next.ShowCommitted = false
		next.Cycle = current.Cycle + 1
		return nightCommandOutcome{result: next, outcome: nightOutcomeApplied, persist: "update"}, nil, nil
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
// finishes first — RESTING-MODE.md §7.1.1) or reach stopped immediately —
// this seam has no outward cue to wait on, so the immediate case reaches
// stopped directly; a future seam's cue wait replaces this.
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
		rec.State = nightStateStopped
		rec.StateEnteredAt = now
		changed = true
	}
	return rec, changed
}

func (h *handlers) nightFadeOutNight(now time.Time, current *store.NightSessionRecord) (nightCommandOutcome, *v1.Problem, error) {
	if current == nil {
		return nightCommandOutcome{result: nightSyntheticInactiveSession(now), outcome: nightOutcomeIdempotentNoOp}, nil, nil
	}
	next, changed := applyNightShutdownEffect(now, *current, "fade-out")
	if !changed {
		return nightCommandOutcome{result: *current, outcome: nightOutcomeIdempotentNoOp}, nil, nil
	}
	return nightCommandOutcome{result: next, outcome: nightOutcomeApplied, persist: "update"}, nil, nil
}

// nightPowerDownPresentation is invariant 6: with no power configuration
// (Track F seam F6 not built), reaching stopped records "not_configured"
// for that optional phase.
func (h *handlers) nightPowerDownPresentation(now time.Time, current *store.NightSessionRecord) (nightCommandOutcome, *v1.Problem, error) {
	if current == nil {
		return nightCommandOutcome{result: nightSyntheticInactiveSession(now), outcome: nightOutcomeIdempotentNoOp}, nil, nil
	}
	if current.State == nightStateStopped && current.PowerPhase != "" {
		// Already stopped AND the power phase already resolved — truly
		// nothing left to do. A session that reached stopped via
		// fade-out-night has State==stopped but PowerPhase=="",
		// and must still be allowed through below to resolve it.
		return nightCommandOutcome{result: *current, outcome: nightOutcomeIdempotentNoOp}, nil, nil
	}
	next, changed := applyNightShutdownEffect(now, *current, "power-down")
	if next.State == nightStateStopped && next.PowerPhase != "not_configured" {
		next.PowerPhase = "not_configured"
		changed = true
	}
	if !changed {
		return nightCommandOutcome{result: *current, outcome: nightOutcomeIdempotentNoOp}, nil, nil
	}
	return nightCommandOutcome{result: next, outcome: nightOutcomeApplied, persist: "update"}, nil, nil
}

// nightEndSessionDecide is the operator-recovery action: abandons
// the current session, reaches stopped, launches nothing. It deliberately
// leaves Degraded unchanged — resuming a session ShowMesh cannot confirm
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

type nightReadinessCheck struct {
	name   string
	health observation.Health
	reason string
}

// nightRunReadinessTx checks exactly one signal, fpp.reachable, for every
// FPP instance the pinned night.session revision references — see
// [nightCheckFPPReachable]'s own doc comment for the full scope statement.
// An outcome here never withholds start-night by itself (invariant 5);
// only the epoch/freshness gate does, since no blocking-interlock
// mechanism exists yet.
func (h *handlers) nightRunReadinessTx(ctx context.Context, tx *store.Tx, now time.Time, current *store.NightSessionRecord) (nightCommandOutcome, *v1.Problem, error) {
	if current == nil {
		p := nightNotReadyProblem("run-readiness: no preparation epoch is open; run prepare-site first")
		return nightCommandOutcome{}, &p, nil
	}
	switch current.State {
	case nightStateInactive, nightStateStopped, nightStateFadingOut:
		p := nightNotReadyProblem("run-readiness: no preparation epoch is open; run prepare-site first")
		return nightCommandOutcome{}, &p, nil
	}

	payload, err := h.getPinnedNightSessionPayloadTx(ctx, tx, *current)
	if err != nil {
		return nightCommandOutcome{}, nil, err
	}

	instanceIDs := map[string]bool{payload.ShowPlaylist.FPPInstanceID: true, payload.Resting.FPPInstanceID: true}
	var checks []nightReadinessCheck
	worst := observation.HealthHealthy
	for id := range instanceIDs {
		if id == "" {
			continue
		}
		checks = append(checks, h.nightCheckFPPReachable(ctx, now, id))
	}
	for _, c := range checks {
		if nightHealthSeverity(c.health) > nightHealthSeverity(worst) {
			worst = c.health
		}
	}
	var outcome string
	switch worst {
	case observation.HealthHealthy:
		outcome = "ready"
	case observation.HealthUnknown:
		outcome = "unknown"
	default:
		outcome = "not_ready"
	}

	checksJSON, err := json.Marshal(nightEncodeChecks(checks))
	if err != nil {
		return nightCommandOutcome{}, nil, fmt.Errorf("api: encode night readiness checks: %w", err)
	}
	rec := store.NightReadinessRecord{
		ID: uuid.NewString(), SessionID: current.ID, EpochID: current.ID,
		CompletedAt: now, Outcome: outcome, ChecksJSON: string(checksJSON),
	}
	if err := tx.CreateNightReadiness(ctx, rec); err != nil {
		return nightCommandOutcome{}, nil, err
	}

	next := *current
	next.ReadinessID = rec.ID
	return nightCommandOutcome{result: next, outcome: nightOutcomeApplied, persist: "update"}, nil, nil
}

func nightHealthSeverity(h observation.Health) int {
	switch h {
	case observation.HealthFailed:
		return 3
	case observation.HealthDegraded:
		return 2
	case observation.HealthUnknown:
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

// nightCheckFPPReachable checks exactly one signal, fpp.reachable. This is
// deliberately the entire readiness surface this build provides — see the
// OpenAPI NightReadinessCheck schema for the full statement; a caller must
// not read a healthy result here as anything broader.
func (h *handlers) nightCheckFPPReachable(ctx context.Context, now time.Time, instanceID string) nightReadinessCheck {
	kind := observation.ResourceFPP
	signal := observation.SignalID("fpp.reachable")
	obs, err := h.deps.Observations.ListObservations(ctx, ObservationFilter{
		ResourceKind: &kind, ResourceID: &instanceID, Signal: &signal,
	})
	name := "fpp:" + instanceID + ":reachable"
	if err != nil {
		return nightReadinessCheck{name: name, health: observation.HealthUnknown, reason: "failed to read fpp.reachable evidence: " + err.Error()}
	}
	if len(obs) == 0 {
		return nightReadinessCheck{name: name, health: observation.HealthUnknown, reason: "no fpp.reachable evidence recorded for this instance"}
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
	return nightReadinessCheck{name: name, health: health, reason: reason}
}

// --- config resolution ---

// resolveActiveNightSessionConfigTx reads night.session.active and, when
// it names a session, that session's current revision, both via tx —
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

// getPinnedNightSessionPayloadTx reads the EXACT revision rec pinned at
// prepare-site — never the currently-active one, which may have been
// edited since (session activation pins the revision).
func (h *handlers) getPinnedNightSessionPayloadTx(ctx context.Context, tx *store.Tx, rec store.NightSessionRecord) (config.NightSessionPayload, error) {
	rev, err := tx.GetConfigRevision(ctx, config.NightSessionConfigKind, rec.ConfigObjectID, rec.ConfigRevision)
	if err != nil {
		return config.NightSessionPayload{}, fmt.Errorf("api: get pinned night.session revision %s/%d: %w", rec.ConfigObjectID, rec.ConfigRevision, err)
	}
	var payload config.NightSessionPayload
	if err := jsonUnmarshalStrict(rev.PayloadJSON, &payload); err != nil {
		return config.NightSessionPayload{}, fmt.Errorf("api: decode pinned night.session payload: %w", err)
	}
	return payload, nil
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
		Transition: v1.NightPhaseEvidence{State: v1.NightEvidenceNotAvailable, Reason: "content anchor and boundary are not derived until Track F seam F3"},
	}
	if !rec.UpdatedAt.IsZero() {
		out.UpdatedAt = formatTime(rec.UpdatedAt)
	} else {
		out.UpdatedAt = formatTime(now)
	}

	out.PowerPhase = mapNightPowerPhase(rec)
	out.Readiness = mapNightReadiness(ctx, deps, rec, now, maxAge)
	return out
}

// mapNightPowerPhase: a deferred power-down (shutdownIntent
// == "power-down" but the phase itself has not resolved yet, because the
// show it is waiting on is still live) must say so, not repeat the
// "has not been requested" text that is only true before any shutdown was
// requested at all.
func mapNightPowerPhase(rec store.NightSessionRecord) v1.NightPhaseEvidence {
	switch rec.PowerPhase {
	case "":
		if rec.ShutdownIntent == "power-down" {
			return v1.NightPhaseEvidence{State: v1.NightEvidenceUnknown, Reason: "power-down-presentation was requested and is deferred until the current show finishes"}
		}
		return v1.NightPhaseEvidence{State: v1.NightEvidenceUnknown, Reason: "power-down-presentation has not been requested"}
	case "not_configured":
		return v1.NightPhaseEvidence{State: v1.NightEvidenceNotConfigured, Reason: "no site-power configuration is present (Track F seam F6)"}
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

// ReconcileNightSessionOnStartup is invariant 4's mechanism: called once,
// synchronously, before the coordinator starts serving requests (see
// [ReconcileStrandedFPPCommands] for the identical timing rationale). A
// session left in a state this build cannot confirm safe to resume is
// marked degraded rather than resumed by guess.
func ReconcileNightSessionOnStartup(ctx context.Context, deps Dependencies, now func() time.Time, logger *slog.Logger) error {
	deps = deps.withDefaults()
	rec, ok, err := deps.NightSessions.GetCurrentNightSession(ctx)
	if err != nil {
		return fmt.Errorf("api: reconcile night session on startup: %w", err)
	}
	if !ok || rec.Degraded {
		return nil
	}
	switch rec.State {
	case nightStateTransitionToShow, nightStateTransitionToResting, nightStateLive, nightStateRestingIntershow, nightStateEndOfNightResting:
		rec.Degraded = true
		rec.DegradedReason = fmt.Sprintf(
			"coordinator restarted while the session was in %q with no evidence this build can use to confirm it is safe to resume; run end-session, then prepare-site, to recover",
			rec.State)
		if err := deps.NightSessions.UpdateNightSession(ctx, rec, now()); err != nil {
			return fmt.Errorf("api: reconcile night session on startup: mark degraded: %w", err)
		}
		if logger != nil {
			logger.Warn("night session reconciliation: session marked degraded on startup", "sessionId", rec.ID, "state", rec.State)
		}
	}
	return nil
}
