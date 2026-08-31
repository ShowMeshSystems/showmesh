package api

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"sync"
	"time"

	"github.com/google/uuid"

	v1 "github.com/showmeshsystems/showmesh/internal/coordinator/api/v1"
	"github.com/showmeshsystems/showmesh/internal/coordinator/config"
	"github.com/showmeshsystems/showmesh/internal/coordinator/identity"
	"github.com/showmeshsystems/showmesh/internal/coordinator/store"
	"github.com/showmeshsystems/showmesh/pkg/command"
)

// Three emergency-stop levels, each stopping playout on every
// configured FPP instance immediately, each with its own OPTIONAL,
// best-effort follow-up show.action list (show.emergencystop.go handles
// that configuration kind's own GET/PUT/revisions; this file is the four
// trigger routes).
//
//   - stop            (level 1): the immediate stop and its follow-ups.
//   - stop-power-down (level 2): the immediate stop, PLUS forcing the
//     active night session's own existing graceful-shutdown sequence to
//     start now instead of deferring — see nightEmergencyPowerDown's own
//     doc comment — plus its own follow-ups.
//   - hard-stop       (level 3): the immediate stop, PLUS abandoning the
//     active night session straight to stopped with no wait (reusing
//     nightEndSessionDecide unchanged), plus its own follow-ups. Gated by
//     the arm/fire pair below — this is "the big red button" and the one
//     level a retry or a redelivered command must never be able to fire
//     twice.
//
// A follow-up action's own failure is reported per-action and NEVER turns
// a successful stop into a reported failure (this build's own
// degrade-safely rule): [v1.EmergencyStopResult] carries StopOutcomes and
// FollowUps as two separate arrays with no combined boolean, and a
// caller's exit code is driven by StopOutcomes alone (cmd_emergency_stop.go).
//
// The deliberate-intent gate lives here, in the API, not the UI alone
// (standing rule: operator capabilities are API-first, showmeshctl at
// practical parity — a UI-only gate would let showmeshctl hard-stop the
// show with one command and no gate at all). It is a two-call arm/fire
// sequence: arm has no side effect on the show and is freely retryable;
// fire atomically consumes a single-use, short-lived, one-per-principal
// token before it dispatches anything, so neither an accidental retry nor
// a redelivered command can fire twice. showmeshctl exposes arm and fire
// as two distinct subcommands that are NEVER chained by one command —
// that IS the gate; see cmd_emergency_stop.go's own doc comment.

// scopeShowEmergencyStopInvoke exists only so api.go's route registration
// can take its address — see scopeActionInvoke's identical pattern.
var scopeShowEmergencyStopInvoke = identity.ScopeShowEmergencyStopInvoke

const maxEmergencyStopRequestBodyBytes = 1024

// The three level names on the wire, in URL paths, and in this build's own
// three minted audit action strings — deliberately the SAME spelling in
// all three places (config/emergencystop.go's own doc comment states the
// identical reason for its JSON field names).
const (
	emergencyStopLevelStop          = "stop"
	emergencyStopLevelStopPowerDown = "stop-power-down"
	emergencyStopLevelHardStop      = "hard-stop"
)

// The three minted audit action strings (docs/build/IDENTIFIER-REGISTER.md),
// this build's own register update.
const (
	auditActionEmergencyStop          = "show.emergencystop.stop"
	auditActionEmergencyStopPowerDown = "show.emergencystop.stop_power_down"
	auditActionEmergencyStopHardStop  = "show.emergencystop.hard_stop"
)

// ProblemTypeEmergencyStopHardStopNotArmed is fire's own refusal when the
// caller presents no valid, unexpired, unconsumed arm token: never armed,
// the wrong token, or the token expired. Distinct from [ProblemTypeConflict]
// (both are 409s) because the remedy differs — arm again and fire promptly,
// vs. someone else already consumed THIS token and the operator should
// check whether the hard stop already happened before retrying blindly.
// cmd_emergency_stop.go maps this type to exitActionRefused directly
// (orchestrator ruling: "a refused arm is an action refusal"); the
// compare-and-swap race case below reuses the ordinary, already-wired
// [ProblemTypeConflict] -> exitConflict path and mints nothing new for it.
const ProblemTypeEmergencyStopHardStopNotArmed = problemBaseURI + "emergency-stop-hard-stop-not-armed"

func emergencyStopHardStopNotArmedProblem(detail string) v1.Problem {
	return v1.Problem{
		Type:   ProblemTypeEmergencyStopHardStopNotArmed,
		Title:  "Hard stop is not armed",
		Status: http.StatusConflict,
		Detail: detail,
	}
}

// --- the arm/fire token store ---

// emergencyStopArmTTL bounds how long an armed token stays fireable.
// Eric's own example ("2x presses within 5 seconds or something") was
// explicitly offered as an example, not a specification, and is a UI
// presentation choice layered on top of this server-side limit, which does
// not have to match it: the UI is free to require its own faster
// double-press within this window. 10s is chosen as workable for two
// separate showmeshctl invocations typed by hand, which a UI's own faster
// requirement does not have to use up.
const emergencyStopArmTTL = 10 * time.Second

// emergencyStopArmEntry is one principal's own current arm token.
type emergencyStopArmEntry struct {
	token     string
	expiresAt time.Time
	consumed  bool
}

// emergencyStopArmStore holds AT MOST ONE live token per principal:
// arming again before a principal's own previous token is consumed or
// expired invalidates that previous token immediately (orchestrator
// ruling, endorsed hardening). Arming is evidence of a RECENT deliberate
// act; several live tokens per principal would let a caller fire on an
// act that is no longer recent, defeating the gate while every individual
// check still passes. In-memory and unpersisted, deliberately: a
// coordinator restart invalidating every outstanding arm is the CORRECT
// behavior for a token whose entire purpose is proving recent intent, not
// a durability gap to fix.
type emergencyStopArmStore struct {
	mu      sync.Mutex
	entries map[string]*emergencyStopArmEntry // keyed by principal ID
}

func newEmergencyStopArmStore() *emergencyStopArmStore {
	return &emergencyStopArmStore{entries: make(map[string]*emergencyStopArmEntry)}
}

// arm mints a fresh single-use token for principalID, discarding any
// previous live token for that principal.
func (s *emergencyStopArmStore) arm(principalID string, now time.Time) (token string, expiresAt time.Time, err error) {
	token = uuid.NewString()
	expiresAt = now.Add(emergencyStopArmTTL)
	s.mu.Lock()
	s.entries[principalID] = &emergencyStopArmEntry{token: token, expiresAt: expiresAt}
	s.mu.Unlock()
	return token, expiresAt, nil
}

// emergencyStopArmConsumeResult is consume's own closed outcome, so its
// caller never has to infer "which kind of refusal" from a generic error.
type emergencyStopArmConsumeResult int

const (
	emergencyStopArmConsumeOK emergencyStopArmConsumeResult = iota
	emergencyStopArmConsumeNotArmed
	emergencyStopArmConsumeAlreadyConsumed
)

// consume atomically checks and, only on success, marks principalID's own
// current token consumed — a single mutex-guarded compare-and-set, so two
// concurrent fire calls presenting the identical token can never both
// succeed: the second always observes consumed==true already. A caller
// that armed, let the token expire, and presents it anyway gets
// emergencyStopArmConsumeNotArmed, the same as never having armed at all —
// the two are indistinguishable on purpose, since the remedy ("arm again")
// is identical either way.
func (s *emergencyStopArmStore) consume(principalID, token string, now time.Time) emergencyStopArmConsumeResult {
	s.mu.Lock()
	defer s.mu.Unlock()
	e, ok := s.entries[principalID]
	if !ok || e.token != token || token == "" {
		return emergencyStopArmConsumeNotArmed
	}
	if e.consumed {
		return emergencyStopArmConsumeAlreadyConsumed
	}
	if !now.Before(e.expiresAt) {
		return emergencyStopArmConsumeNotArmed
	}
	e.consumed = true
	return emergencyStopArmConsumeOK
}

// --- request/idempotency-key plumbing shared by all four routes ---

func decodeEmergencyStopIdempotencyKeyBody(r *http.Request, maxBytes int64) (idempotencyKey string, problem *v1.Problem) {
	body, err := io.ReadAll(io.LimitReader(r.Body, maxBytes+1))
	if err != nil {
		p := invalidParameterProblem(fmt.Sprintf("reading request body: %v", err))
		return "", &p
	}
	if int64(len(body)) > maxBytes {
		p := invalidParameterProblem("request body too large")
		return "", &p
	}
	var top map[string]json.RawMessage
	if len(body) > 0 {
		if err := json.Unmarshal(body, &top); err != nil {
			p := invalidParameterProblem(`request body must be a JSON object matching {"idempotencyKey":string}`)
			return "", &p
		}
	}
	var unknown []string
	for k := range top {
		if k != "idempotencyKey" && k != "armToken" {
			unknown = append(unknown, k)
		}
	}
	if len(unknown) > 0 {
		p := invalidParameterProblem(fmt.Sprintf("request body contains unrecognized key(s): %v", unknown))
		return "", &p
	}
	if raw, ok := top["idempotencyKey"]; ok {
		_ = json.Unmarshal(raw, &idempotencyKey)
	}
	if err := command.ValidateIdempotencyKey(idempotencyKey); err != nil {
		p := invalidParameterProblem("idempotencyKey: " + err.Error())
		return "", &p
	}
	return idempotencyKey, nil
}

// --- dispatching the immediate stop across every configured FPP instance ---

// emergencyStopAllInstances dispatches [fppActionStopPlaylist] to every
// configured FPP instance CONCURRENTLY — an emergency stop must not let
// instance N+1's dispatch wait on instance N's own confirm deadline, which
// existing per-command dispatch can take tens of seconds to reach.
// idempotencyKey derives a stable, deterministic per-instance idempotency
// key so a retried emergency-stop request (the SAME top-level
// idempotencyKey) reproduces the SAME per-instance key and hits
// dispatchFPPCommand's own existing replay handling instead of dispatching
// a second stop — the identical protection a broker redelivery gets for
// free from the SAME mechanism.
func (h *handlers) emergencyStopAllInstances(ctx context.Context, now time.Time, idempotencyKey string, ac authContext, clientAddr string) []v1.EmergencyStopInstanceOutcome {
	endpoints, err := currentFPPEndpoints(ctx, h.deps.FPP)
	if err != nil {
		h.logWarn("emergency stop: failed to list configured FPP instances", "error", err)
		return nil
	}
	if len(endpoints) == 0 {
		return []v1.EmergencyStopInstanceOutcome{}
	}

	out := make([]v1.EmergencyStopInstanceOutcome, len(endpoints))
	var wg sync.WaitGroup
	for i, ep := range endpoints {
		wg.Add(1)
		go func(i int, instanceID string) {
			defer wg.Done()
			outcome, problem, err := h.dispatchFPPCommand(ctx, now, FPPCommandInput{
				InstanceID:     instanceID,
				Action:         fppActionStopPlaylist,
				IdempotencyKey: emergencyStopInstanceIdempotencyKey(idempotencyKey, instanceID),
				Issuer: FPPCommandIssuer{
					PrincipalID: ac.result.Principal.ID, PrincipalName: ac.result.Principal.Name,
					Form: ac.result.Form, CredentialID: ac.result.CredentialID, ClientAddr: clientAddr,
				},
				NeverWithholdOnAuditFailure: true,
			})
			switch {
			case err != nil:
				out[i] = v1.EmergencyStopInstanceOutcome{InstanceID: instanceID, Outcome: "failed", OutcomeReason: "this stop could not be dispatched because of an internal coordinator error"}
			case problem != nil:
				out[i] = v1.EmergencyStopInstanceOutcome{InstanceID: instanceID, Outcome: "refused", OutcomeReason: problem.Detail}
			case outcome.DispatchFailed:
				out[i] = v1.EmergencyStopInstanceOutcome{InstanceID: instanceID, Outcome: "failed", OutcomeReason: fmt.Sprintf("the request to %s did not succeed, so this stop never reached it", instanceID), DispatchedAt: formatTimePtr(outcome.DispatchedAt), Replay: outcome.Replay}
			default:
				out[i] = v1.EmergencyStopInstanceOutcome{InstanceID: instanceID, Outcome: outcome.Outcome, OutcomeReason: outcome.OutcomeReason, DispatchedAt: formatTimePtr(outcome.DispatchedAt), Replay: outcome.Replay}
			}
		}(i, ep.ID)
	}
	wg.Wait()
	return out
}

func emergencyStopInstanceIdempotencyKey(idempotencyKey, instanceID string) string {
	return "emergencystop:" + idempotencyKey + ":fpp:" + instanceID
}

// --- dispatching one level's own optional, best-effort follow-up actions ---

// emergencyStopRunFollowUps invokes each configured show.action id, IN
// ORDER, best-effort: one action's own failure is recorded and dispatch
// continues to the next rather than aborting the list, and no failure
// here is ever allowed to change what emergencyStopAllInstances already
// reported. cmdID is derived the same deterministic way the per-instance
// stop's own idempotency key is, so a retried top-level request reproduces
// the same per-action dispatch identity instead of re-firing it — see
// [handlers.dispatchActionTarget]'s own use of cmdID to derive its child
// dispatch's idempotency key.
func (h *handlers) emergencyStopRunFollowUps(ctx context.Context, idempotencyKey string, actionIDs []string, ac authContext, clientAddr string) []v1.EmergencyStopFollowUpResult {
	out := make([]v1.EmergencyStopFollowUpResult, 0, len(actionIDs))
	for _, actionID := range actionIDs {
		out = append(out, h.emergencyStopRunOneFollowUp(ctx, idempotencyKey, actionID, ac, clientAddr))
	}
	return out
}

func (h *handlers) emergencyStopRunOneFollowUp(ctx context.Context, idempotencyKey, actionID string, ac authContext, clientAddr string) v1.EmergencyStopFollowUpResult {
	rev, _, problem, err := h.getActiveShowConfigRevision(ctx, config.ShowActionConfigKind, actionID)
	if err != nil {
		h.logWarn("emergency stop: failed to resolve follow-up action", "actionId", actionID, "error", err)
		return v1.EmergencyStopFollowUpResult{ActionID: actionID, OutcomeReason: "this follow-up action could not be resolved because of an internal coordinator error"}
	}
	if problem != nil {
		return v1.EmergencyStopFollowUpResult{ActionID: actionID, OutcomeReason: "this follow-up action no longer exists: " + problem.Detail}
	}
	payload, err := decodeShowActionPayloadForRead(rev.PayloadJSON)
	if err != nil {
		h.logWarn("emergency stop: failed to decode follow-up action payload", "actionId", actionID, "error", err)
		return v1.EmergencyStopFollowUpResult{ActionID: actionID, OutcomeReason: "this follow-up action's stored configuration could not be decoded"}
	}

	auditExempt := payload.SafetyClass != config.ShowSafetyClassNone
	cmdID := emergencyStopFollowUpCommandID(idempotencyKey, actionID)
	outcome, _, outcomeReason, _, _ := h.dispatchActionTarget(ctx, payload, cmdID, strconv.FormatInt(rev.Revision, 10), ac, clientAddr, auditExempt)
	return v1.EmergencyStopFollowUpResult{ActionID: actionID, Label: payload.Label, Outcome: outcome, OutcomeReason: outcomeReason}
}

func emergencyStopFollowUpCommandID(idempotencyKey, actionID string) string {
	return "emergencystop:" + idempotencyKey + ":action:" + actionID
}

// --- the night-session side effects levels 2 and 3 force ---

// nightEmergencyPowerDown is level stop-power-down's own "standard
// graceful shutdown" component: RESTING-MODE.md's own words ("Only an
// explicitly configured and invoked emergency/force operation may
// interrupt playback or remove power immediately") are why this forces
// the SAME power-down-presentation apply step every ordinary
// power-down-presentation command already runs, immediately, bypassing
// both the ordinary live-show deferral (applyNightShutdownEffect's own
// force parameter) and interlock evaluation entirely — mirroring
// end-session's own existing precedent, "never deferred, never
// interlocked", for an operator's own explicit emergency command. Reuses
// nightCommandPowerDownPresentation as its own audit command name (no new
// audit action minted for this side effect: it IS a power-down-
// presentation, just forced rather than ordinarily scheduled), so it
// appears in the audit log as "night.power-down-presentation" exactly
// like an operator calling that command directly would.
//
// Returns present=false, with no error, when no night session is active:
// a real, valid, non-degraded outcome, not a failure to report.
func (h *handlers) nightEmergencyPowerDown(ctx context.Context, now time.Time, issuer identity.AuditEntry) (v1.EmergencyStopNightSessionOutcome, error) {
	current, hasCurrent, err := h.deps.NightSessions.GetCurrentNightSession(ctx)
	if err != nil {
		return v1.EmergencyStopNightSessionOutcome{}, err
	}
	if !hasCurrent {
		return v1.EmergencyStopNightSessionOutcome{Present: false}, nil
	}

	var attributionDegraded bool
	out, problem, err := h.nightRunExempt(ctx, now, nightCommandPowerDownPresentation, issuer, &attributionDegraded, func(ctx context.Context, tx *store.Tx, cur *store.NightSessionRecord) (nightCommandOutcome, *v1.Problem, error) {
		return h.nightPowerDownPresentationApply(ctx, tx, cur, now, nil, true)
	})
	if err != nil {
		return v1.EmergencyStopNightSessionOutcome{}, err
	}
	if problem != nil {
		// nightPowerDownPresentationApply is gate-free and never returns a
		// problem; a non-nil problem here would be this package's own
		// internal-contract violation, not a caller-facing refusal.
		return v1.EmergencyStopNightSessionOutcome{}, fmt.Errorf("api: emergency power-down: unexpected refusal: %s", problem.Detail)
	}
	return v1.EmergencyStopNightSessionOutcome{Present: true, SessionID: current.ID, Outcome: out.outcome}, nil
}

// nightEmergencyEndSession is level hard-stop's own "no wait time" night-
// session component: reuses nightEndSessionDecide UNCHANGED — the
// existing operator-recovery action that "abandons the current session,
// reaches stopped, launches nothing", already never deferred and never
// interlocked (docs/build/IDENTIFIER-REGISTER.md: "end-session... is
// always the unconditional way to reach stopped"). It sets State directly
// to stopped with no wait for idle evidence, which is exactly hard-stop's
// own "no wait time" requirement — unlike stop-power-down, which still
// waits on the night loop's own fading-out tick and its fresh idle
// evidence (RESTING-MODE.md §4.6/§4.7). Mints no new audit action: this
// reuses nightCommandEndSession, appearing as "night.end-session".
func (h *handlers) nightEmergencyEndSession(ctx context.Context, now time.Time, issuer identity.AuditEntry) (v1.EmergencyStopNightSessionOutcome, error) {
	current, hasCurrent, err := h.deps.NightSessions.GetCurrentNightSession(ctx)
	if err != nil {
		return v1.EmergencyStopNightSessionOutcome{}, err
	}
	if !hasCurrent {
		return v1.EmergencyStopNightSessionOutcome{Present: false}, nil
	}

	var attributionDegraded bool
	out, problem, err := h.nightRunExempt(ctx, now, nightCommandEndSession, issuer, &attributionDegraded, func(ctx context.Context, tx *store.Tx, cur *store.NightSessionRecord) (nightCommandOutcome, *v1.Problem, error) {
		return h.nightEndSessionDecide(now, cur), nil, nil
	})
	if err != nil {
		return v1.EmergencyStopNightSessionOutcome{}, err
	}
	if problem != nil {
		return v1.EmergencyStopNightSessionOutcome{}, fmt.Errorf("api: emergency end-session: unexpected refusal: %s", problem.Detail)
	}
	return v1.EmergencyStopNightSessionOutcome{Present: true, SessionID: current.ID, Outcome: out.outcome}, nil
}

// --- resolving this kind's own configured follow-up lists ---

func (h *handlers) resolveEmergencyStopPayload(ctx context.Context) (config.EmergencyStopPayload, error) {
	obj, err := h.deps.Config.GetConfigObject(ctx, config.ShowEmergencyStopConfigKind, config.ShowEmergencyStopConfigObjectID)
	switch {
	case errors.Is(err, store.ErrConfigObjectNotFound):
		return config.EmergencyStopDefaultPayload, nil
	case err != nil:
		return config.EmergencyStopPayload{}, fmt.Errorf("api: get show.emergencystop config object: %w", err)
	case obj.CurrentRevision == 0:
		return config.EmergencyStopDefaultPayload, nil
	}
	rev, err := h.deps.Config.GetConfigRevision(ctx, config.ShowEmergencyStopConfigKind, config.ShowEmergencyStopConfigObjectID, obj.CurrentRevision)
	if err != nil {
		return config.EmergencyStopPayload{}, fmt.Errorf("api: get show.emergencystop config revision %d: %w", obj.CurrentRevision, err)
	}
	payload, verr := config.DecodeEmergencyStopPayload(rev.PayloadJSON, func(string) bool { return true })
	if verr != nil {
		// A stored row this package never wrote in this shape is a
		// store-integrity error, not a validation outcome to recover
		// from — the resolver is a no-op here because a value already
		// accepted at write time never needs re-validating at read time.
		return config.EmergencyStopPayload{}, fmt.Errorf("api: decode show.emergencystop payload: %s", verr.Error())
	}
	return payload, nil
}

// --- the four trigger routes ---

func (h *handlers) emergencyStopIssuer(now time.Time, ac authContext, clientAddr string) identity.AuditEntry {
	return identity.AuditEntry{
		Timestamp: now, PrincipalID: ac.result.Principal.ID, PrincipalName: ac.result.Principal.Name,
		Form: ac.result.Form, CredentialID: ac.result.CredentialID, ClientAddr: clientAddr,
	}
}

// handleEmergencyStop serves POST .../emergency-stop/stop (level 1):
// immediate stop, plus its own configured follow-ups. No night-session
// interaction of any kind.
func (h *handlers) handleEmergencyStop(w http.ResponseWriter, r *http.Request) {
	now := h.now()
	ctx := context.WithoutCancel(r.Context())
	idempotencyKey, problem := decodeEmergencyStopIdempotencyKeyBody(r, maxEmergencyStopRequestBodyBytes)
	if problem != nil {
		writeProblem(w, h.logger, now, *problem)
		return
	}
	ac := authFromContext(r.Context())
	clientAddr := h.clientAddr(r)

	payload, err := h.resolveEmergencyStopPayload(ctx)
	if err != nil {
		h.writeInternalError(w, now, "resolve show.emergencystop config", err)
		return
	}

	stopOutcomes := h.emergencyStopAllInstances(ctx, now, idempotencyKey, ac, clientAddr)
	followUps := h.emergencyStopRunFollowUps(ctx, idempotencyKey, payload.Stop.Actions, ac, clientAddr)

	h.writeBestEffortAuditBounded(ctx, now, degradedAttributionReasonPostDispatch, emergencyStopAuditEntry(
		h.emergencyStopIssuer(now, ac, clientAddr), auditActionEmergencyStop, idempotencyKey, emergencyStopLevelStop, stopOutcomes, followUps))

	jsonWrite(w, v1.EmergencyStopResponse{ServerTime: formatTime(now), Result: v1.EmergencyStopResult{
		Level: emergencyStopLevelStop, IdempotencyKey: idempotencyKey, StopOutcomes: stopOutcomes, FollowUps: followUps,
	}})
}

// handleEmergencyStopPowerDown serves POST .../emergency-stop/stop-power-down
// (level 2): immediate stop, forcing the active night session's own
// existing graceful-shutdown sequence to start now (nightEmergencyPowerDown),
// plus its own configured follow-ups.
func (h *handlers) handleEmergencyStopPowerDown(w http.ResponseWriter, r *http.Request) {
	now := h.now()
	ctx := context.WithoutCancel(r.Context())
	idempotencyKey, problem := decodeEmergencyStopIdempotencyKeyBody(r, maxEmergencyStopRequestBodyBytes)
	if problem != nil {
		writeProblem(w, h.logger, now, *problem)
		return
	}
	ac := authFromContext(r.Context())
	clientAddr := h.clientAddr(r)
	issuer := h.emergencyStopIssuer(now, ac, clientAddr)

	payload, err := h.resolveEmergencyStopPayload(ctx)
	if err != nil {
		h.writeInternalError(w, now, "resolve show.emergencystop config", err)
		return
	}

	stopOutcomes := h.emergencyStopAllInstances(ctx, now, idempotencyKey, ac, clientAddr)
	nightOutcome, err := h.nightEmergencyPowerDown(ctx, now, issuer)
	if err != nil {
		h.writeInternalError(w, now, "force night session power-down for emergency stop", err)
		return
	}
	followUps := h.emergencyStopRunFollowUps(ctx, idempotencyKey, payload.StopPowerDown.Actions, ac, clientAddr)

	h.writeBestEffortAuditBounded(ctx, now, degradedAttributionReasonPostDispatch, emergencyStopAuditEntry(
		issuer, auditActionEmergencyStopPowerDown, idempotencyKey, emergencyStopLevelStopPowerDown, stopOutcomes, followUps))

	jsonWrite(w, v1.EmergencyStopResponse{ServerTime: formatTime(now), Result: v1.EmergencyStopResult{
		Level: emergencyStopLevelStopPowerDown, IdempotencyKey: idempotencyKey, StopOutcomes: stopOutcomes,
		NightSession: &nightOutcome, FollowUps: followUps,
	}})
}

// handleEmergencyStopArm serves POST .../emergency-stop/hard-stop/arm: the
// deliberate-intent gate's first call. No side effect on the show.
func (h *handlers) handleEmergencyStopArm(w http.ResponseWriter, r *http.Request) {
	now := h.now()
	_, problem := decodeEmergencyStopIdempotencyKeyBody(r, maxEmergencyStopRequestBodyBytes)
	if problem != nil {
		writeProblem(w, h.logger, now, *problem)
		return
	}
	ac := authFromContext(r.Context())
	token, expiresAt, err := h.emergencyStopArms.arm(ac.result.Principal.ID, now)
	if err != nil {
		h.writeInternalError(w, now, "arm emergency stop hard-stop", err)
		return
	}
	jsonWrite(w, v1.EmergencyStopArmResponse{ServerTime: formatTime(now), ArmToken: token, ExpiresAt: formatTime(expiresAt)})
}

// handleEmergencyStopFire serves POST .../emergency-stop/hard-stop/fire
// (level 3): the deliberate-intent gate's second call, then immediate
// stop, abandoning the active night session straight to stopped with no
// wait (nightEmergencyEndSession), plus its own configured follow-ups.
func (h *handlers) handleEmergencyStopFire(w http.ResponseWriter, r *http.Request) {
	now := h.now()
	body, err := io.ReadAll(io.LimitReader(r.Body, maxEmergencyStopRequestBodyBytes+1))
	if err != nil {
		writeProblem(w, h.logger, now, invalidParameterProblem(fmt.Sprintf("reading request body: %v", err)))
		return
	}
	var top struct {
		IdempotencyKey string `json:"idempotencyKey"`
		ArmToken       string `json:"armToken"`
	}
	if len(body) > 0 {
		if err := json.Unmarshal(body, &top); err != nil {
			writeProblem(w, h.logger, now, invalidParameterProblem(`request body must be a JSON object matching {"idempotencyKey":string,"armToken":string}`))
			return
		}
	}
	if err := command.ValidateIdempotencyKey(top.IdempotencyKey); err != nil {
		writeProblem(w, h.logger, now, invalidParameterProblem("idempotencyKey: "+err.Error()))
		return
	}

	ac := authFromContext(r.Context())
	clientAddr := h.clientAddr(r)
	issuer := h.emergencyStopIssuer(now, ac, clientAddr)

	switch h.emergencyStopArms.consume(ac.result.Principal.ID, top.ArmToken, now) {
	case emergencyStopArmConsumeNotArmed:
		writeProblem(w, h.logger, now, emergencyStopHardStopNotArmedProblem(
			"no valid, unexpired armed hard-stop token for this principal; call the arm endpoint again, then fire promptly"))
		return
	case emergencyStopArmConsumeAlreadyConsumed:
		writeProblem(w, h.logger, now, v1.Problem{
			Type:   ProblemTypeConflict,
			Title:  "Hard stop already fired",
			Status: http.StatusConflict,
			Detail: "this arm token was already consumed by an earlier fire request; if the hard stop did not actually happen, arm again",
		})
		return
	}

	ctx := context.WithoutCancel(r.Context())
	payload, err := h.resolveEmergencyStopPayload(ctx)
	if err != nil {
		h.writeInternalError(w, now, "resolve show.emergencystop config", err)
		return
	}

	stopOutcomes := h.emergencyStopAllInstances(ctx, now, top.IdempotencyKey, ac, clientAddr)
	nightOutcome, err := h.nightEmergencyEndSession(ctx, now, issuer)
	if err != nil {
		h.writeInternalError(w, now, "force night session end-session for emergency hard stop", err)
		return
	}
	followUps := h.emergencyStopRunFollowUps(ctx, top.IdempotencyKey, payload.HardStop.Actions, ac, clientAddr)

	h.writeBestEffortAuditBounded(ctx, now, degradedAttributionReasonPostDispatch, emergencyStopAuditEntry(
		issuer, auditActionEmergencyStopHardStop, top.IdempotencyKey, emergencyStopLevelHardStop, stopOutcomes, followUps))

	jsonWrite(w, v1.EmergencyStopResponse{ServerTime: formatTime(now), Result: v1.EmergencyStopResult{
		Level: emergencyStopLevelHardStop, IdempotencyKey: top.IdempotencyKey, StopOutcomes: stopOutcomes,
		NightSession: &nightOutcome, FollowUps: followUps,
	}})
}

func emergencyStopAuditEntry(issuer identity.AuditEntry, action, idempotencyKey, level string, stopOutcomes []v1.EmergencyStopInstanceOutcome, followUps []v1.EmergencyStopFollowUpResult) identity.AuditEntry {
	issuer.Action = action
	issuer.Target = level
	issuer.IdempotencyKey = idempotencyKey
	issuer.Kind = identity.AuditOutcome
	issuer.Params = map[string]any{
		"level":         level,
		"stopInstances": len(stopOutcomes),
		"followUps":     followUps,
	}
	return issuer
}
