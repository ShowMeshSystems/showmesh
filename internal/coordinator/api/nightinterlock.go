package api

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	v1 "github.com/showmeshsystems/showmesh/internal/coordinator/api/v1"
	"github.com/showmeshsystems/showmesh/internal/coordinator/config"
	"github.com/showmeshsystems/showmesh/internal/coordinator/identity"
	"github.com/showmeshsystems/showmesh/internal/coordinator/store"
	"github.com/showmeshsystems/showmesh/pkg/observation"
)

// Track F seam F6 (RESTING-MODE.md §10.1, ADR-016, ADR-029): interlock
// evaluation. Orchestrator ruling for this seam: a named logical action's
// own mqtt request/response IS the evidence read an interlock uses: this
// codebase has no other way to reach a site sensor today, and a signal
// that never answers is exactly the "source unavailable" column of
// RESTING-MODE.md §10.1's matrix. nightsitecontrol.go (config package)
// already refuses, at write time, any interlock signal that is not an
// mqtt action with expect.kind other than "none"; everything below
// assumes that refusal already ran.

// nightInterlockCondition is what one evidence read resolved to, before
// posture is applied.
type nightInterlockCondition int

const (
	nightInterlockConditionTrue nightInterlockCondition = iota
	nightInterlockConditionFalse
	nightInterlockConditionUnavailable
)

// classifyMQTTActionResult maps DispatchMQTTAction's own five-mode
// contract onto the two-valued condition RESTING-MODE.md §10.1's matrix
// needs: "confirmed" is the condition holding true; "failed" with
// mqttActionStateNegativeAnswer is the condition holding false (the
// external system answered and said no); every other outcome (a
// deadline, a transport error, an unknown broker, a malformed payload)
// is "source unavailable," never silently treated as either true or
// false.
func classifyMQTTActionResult(res MQTTActionResult) nightInterlockCondition {
	switch {
	case res.Outcome == outcomeWordConfirmed:
		return nightInterlockConditionTrue
	case res.Outcome == outcomeWordFailed && res.OutcomeState == mqttActionStateNegativeAnswer:
		return nightInterlockConditionFalse
	default:
		return nightInterlockConditionUnavailable
	}
}

// nightInterlockDecision is [evaluateNightInterlockRule]'s own output.
// Withhold is meaningful only for the phase rule.Phase itself declares:
// RESTING-MODE.md §10.1: "Only rules for the phase currently being
// entered can withhold that phase." A caller evaluating a rule for
// display outside its own phase must ignore Withhold entirely.
type nightInterlockDecision struct {
	Withhold bool
	Health   nightCheckState
	Reason   string
}

// evaluateNightInterlockRule applies RESTING-MODE.md §10.1's closed
// behavior matrix, exactly, given rule and the evidence read's own
// condition. Pure and side-effect free: every dispatch and every
// override-authorization decision lives in this file's other functions,
// never here, so this one function is the whole matrix and is unit
// tested as such.
func evaluateNightInterlockRule(rule config.NightInterlockRule, cond nightInterlockCondition) nightInterlockDecision {
	switch rule.Posture {
	case config.NightInterlockPostureDisabled:
		// "Do not evaluate" for either matrix column: reported, never
		// withheld, and its health is not_verifiable rather than a claim
		// about anything that was actually checked.
		return nightInterlockDecision{Health: nightCheckStateNotVerifiable, Reason: "disabled: not evaluated"}

	case config.NightInterlockPostureObserve:
		switch cond {
		case nightInterlockConditionTrue:
			return nightInterlockDecision{Health: nightHealthHealthy()}
		case nightInterlockConditionFalse:
			return nightInterlockDecision{Health: nightHealthFailed(), Reason: rule.FailureText}
		default:
			return nightInterlockDecision{Health: nightHealthUnknown(), Reason: "evidence source unavailable"}
		}

	case config.NightInterlockPostureBlock:
		switch cond {
		case nightInterlockConditionTrue:
			return nightInterlockDecision{Health: nightHealthHealthy()}
		case nightInterlockConditionFalse:
			return nightInterlockDecision{Withhold: true, Health: nightHealthFailed(), Reason: rule.FailureText}
		default:
			// RESTING-MODE.md §10.1: onUnavailable governs only whether
			// this withholds; the reported health is "unknown" either way
			// ("Report unknown and withhold" / "Report unknown and
			// allow").
			return nightInterlockDecision{
				Withhold: rule.OnUnavailable == config.NightInterlockOnUnavailableBlock,
				Health:   nightHealthUnknown(),
				Reason:   "evidence source unavailable",
			}
		}
	}
	// Unreachable given the closed posture enum nightsitecontrol.go
	// already validated; answered rather than left to a zero-value
	// nightInterlockDecision that would silently never withhold.
	return nightInterlockDecision{Health: nightHealthUnknown(), Reason: fmt.Sprintf("unrecognized posture %q", rule.Posture)}
}

// nightConditionFromHealth is [classifyMQTTActionResult]'s own inverse,
// for a caller that only has a PERSISTED health string (a stored
// readiness check) rather than a live MQTTActionResult; see
// nightInterlockWithholdsPhase's own doc comment for why start-night
// re-derives Withhold from the stored readiness result instead of
// dispatching again inside its own transaction.
func nightConditionFromHealth(state string) nightInterlockCondition {
	switch observation.Health(state) {
	case observation.HealthHealthy:
		return nightInterlockConditionTrue
	case observation.HealthFailed:
		return nightInterlockConditionFalse
	default:
		return nightInterlockConditionUnavailable
	}
}

// nightInterlockCheckName is the stored [nightReadinessCheck].name every
// interlock uses, so a phase's own gating step and run-readiness's own
// display share one naming convention.
func nightInterlockCheckName(rule config.NightInterlockRule) string {
	return "interlock:" + rule.Phase + ":" + rule.Name
}

// nightEvaluateInterlockRuleLive dispatches rule's own named action and
// evaluates the result: the only place in this seam that puts anything
// on the wire for an interlock. Used by run-readiness (outside any store
// transaction, mirroring nightCheckFPPReachable's own siblings in
// nightasset.go) and never called from inside a store-bound decide
// function: an mqtt action's own expect.deadlineSeconds can run up to
// [mqttExpectMaxDeadlineSeconds], which must never hold the store's
// single connection.
func (h *handlers) nightEvaluateInterlockRuleLive(ctx context.Context, rule config.NightInterlockRule) nightInterlockDecision {
	if rule.Posture == config.NightInterlockPostureDisabled {
		return evaluateNightInterlockRule(rule, nightInterlockConditionUnavailable)
	}
	action, _, err := nightResolveShowAction(ctx, h.deps.Config, rule.Signal)
	if err != nil {
		return nightInterlockDecision{
			Withhold: rule.Posture == config.NightInterlockPostureBlock && rule.OnUnavailable == config.NightInterlockOnUnavailableBlock,
			Health:   nightHealthUnknown(),
			Reason:   "could not read this interlock's own signal action: " + err.Error(),
		}
	}
	res := DispatchMQTTAction(ctx, h.deps.MQTTBrokers, action.Target, h.now)
	return evaluateNightInterlockRule(rule, classifyMQTTActionResult(res))
}

// nightComputeInterlockChecks runs every enabled interlock's own live
// evidence read (nightEvaluateInterlockRuleLive) and reports one
// [nightReadinessCheck] per configured rule, disabled ones included
// (as not_verifiable; see evaluateNightInterlockRule's own doc comment).
// The reason text names the rule's own phase and posture explicitly so an
// operator reading run-readiness's own check list, not this seam's own
// gating code, can see which phase a failing rule would affect, since
// RESTING-MODE.md §10.1 makes that visibility a requirement independent
// of whether the CURRENT command is even entering that phase.
func (h *handlers) nightComputeInterlockChecks(ctx context.Context, rules []config.NightInterlockRule) []nightReadinessCheck {
	checks := make([]nightReadinessCheck, 0, len(rules))
	for _, rule := range rules {
		decision := h.nightEvaluateInterlockRuleLive(ctx, rule)
		reason := decision.Reason
		if rule.Posture != config.NightInterlockPostureDisabled {
			withholdNote := "would not withhold"
			if decision.Withhold {
				withholdNote = "withholds"
			}
			reason = fmt.Sprintf("posture=%s phase=%s (%s this phase): %s", rule.Posture, rule.Phase, withholdNote, decision.Reason)
		}
		checks = append(checks, nightReadinessCheck{name: nightInterlockCheckName(rule), health: decision.Health, reason: reason})
	}
	return checks
}

// nightInterlockOverrideRequest is one entry of the night command body's
// optional "interlockOverrides" array: RESTING-MODE.md §10.1's own
// requirement that an accepted override identify the rule, the reason,
// and, via the command that carried it, the phase and bounded
// invocation scope.
type nightInterlockOverrideRequest struct {
	Rule   string `json:"rule"`
	Reason string `json:"reason"`
}

// nightPhaseInterlockGate is what a gated command computes for the phase
// it is about to enter. Two distinct evidence sources feed it, and a
// caller must not treat them as the same freshness guarantee
// (orchestrator ruling, this seam's own build report):
//
//   - LIVE: prepare-site and run-readiness dispatch every phase-matching
//     rule's own action at the instant the command runs
//     ([handlers.nightLiveEvaluatePhaseGate]), because both already run
//     outside any store transaction (nightPrepareSiteCommand,
//     nightRunReadinessCommand) and can safely put something on the wire
//     without holding the store's single connection.
//   - STORED: start-night, start-preshow, fade-out-night, and
//     power-down-presentation are dispatched from inside a store
//     transaction (nightRunGated/nightRunExempt) and gate on the most
//     recent TRUSTED readiness result instead
//     ([handlers.nightTrustedReadinessChecksTx] plus
//     [nightEvaluatePhaseInterlockGate]): an mqtt action's own
//     expect.deadlineSeconds can run up to
//     [mqttExpectMaxDeadlineSeconds], which must never be dispatched
//     while the store's one connection is held. That evidence is only as
//     fresh as the last successful run-readiness call, bounded by
//     h.nightReadinessMaxAge on the WHOLE readiness result (not per
//     check); a caller with no trusted readiness at all is
//     evidence-unavailable for every phase-matching rule, governed by
//     that rule's own onUnavailable.
type nightPhaseInterlockGate struct {
	// Withheld names every "block" rule for phase that currently withholds
	// it and was not covered by a valid override.
	Withheld []nightWithheldInterlock
	// Overridden names every rule an override actually authorized, for the
	// audit entry ADR-024/RESTING-MODE.md §10.1 both require.
	Overridden []nightWithheldInterlock
}

type nightWithheldInterlock struct {
	Rule   config.NightInterlockRule
	Reason string
}

// nightEvaluatePhaseInterlockGate is the STORED-evidence gate (see
// [nightPhaseInterlockGate]'s own doc comment): it walks
// payload.Interlocks for phase, resolves each block rule's own decision
// from checks (a trusted readiness result; see
// [handlers.nightTrustedReadinessChecksTx]), and applies overrides. A
// rule not named in checks at all (readiness never ran for it, the
// pinned revision changed after the checks were computed, checks predate
// this rule, or no trusted readiness exists at all) is treated as
// evidence-unavailable rather than skipped, so a configuration change or
// a stale/missing readiness result cannot silently disable an
// interlock's own withhold.
func nightEvaluatePhaseInterlockGate(payload config.NightSessionPayload, phase string, checks []nightReadinessCheck, overrides []nightInterlockOverrideRequest, callerHasOverrideScope bool) nightPhaseInterlockGate {
	checksByName := make(map[string]nightReadinessCheck, len(checks))
	for _, c := range checks {
		checksByName[c.name] = c
	}
	decide := func(rule config.NightInterlockRule) nightInterlockDecision {
		cond := nightInterlockConditionUnavailable
		if c, ok := checksByName[nightInterlockCheckName(rule)]; ok {
			cond = nightConditionFromHealth(string(c.health))
		}
		return evaluateNightInterlockRule(rule, cond)
	}
	return nightBuildPhaseGate(payload, phase, decide, overrides, callerHasOverrideScope)
}

// nightLiveEvaluatePhaseGate is the LIVE-evidence gate (see
// [nightPhaseInterlockGate]'s own doc comment): every phase-matching
// "block" rule is dispatched at this instant
// ([handlers.nightEvaluateInterlockRuleLive]). Callable only from a
// command path that runs OUTSIDE any store transaction
// (nightPrepareSiteCommand, nightRunReadinessCommand): an mqtt action's
// own expect.deadlineSeconds can run up to [mqttExpectMaxDeadlineSeconds],
// which must never be dispatched while the store's one connection is
// held.
func (h *handlers) nightLiveEvaluatePhaseGate(ctx context.Context, payload config.NightSessionPayload, phase string, overrides []nightInterlockOverrideRequest, callerHasOverrideScope bool) nightPhaseInterlockGate {
	decide := func(rule config.NightInterlockRule) nightInterlockDecision {
		return h.nightEvaluateInterlockRuleLive(ctx, rule)
	}
	return nightBuildPhaseGate(payload, phase, decide, overrides, callerHasOverrideScope)
}

// nightBuildPhaseGate is the shared core of the live and stored gates
// above: filter payload.Interlocks to phase's own "block" rules, resolve
// each one's decision via decide, and apply any matching, authorized
// override.
func nightBuildPhaseGate(payload config.NightSessionPayload, phase string, decide func(config.NightInterlockRule) nightInterlockDecision, overrides []nightInterlockOverrideRequest, callerHasOverrideScope bool) nightPhaseInterlockGate {
	overrideReasonByRule := make(map[string]string, len(overrides))
	for _, o := range overrides {
		overrideReasonByRule[o.Rule] = o.Reason
	}

	var gate nightPhaseInterlockGate
	for _, rule := range payload.Interlocks {
		if rule.Phase != phase || rule.Posture != config.NightInterlockPostureBlock {
			continue
		}
		decision := decide(rule)
		if !decision.Withhold {
			continue
		}
		reason, requested := overrideReasonByRule[rule.Name]
		if requested && callerHasOverrideScope && rule.OverridePolicy == config.NightInterlockOverridePolicyAuthorizedOperator {
			gate.Overridden = append(gate.Overridden, nightWithheldInterlock{Rule: rule, Reason: reason})
			continue
		}
		gate.Withheld = append(gate.Withheld, nightWithheldInterlock{Rule: rule, Reason: decision.Reason})
	}
	return gate
}

// nightInterlockOverrideAuditParams builds the audit Params ADR-024's own
// Params map carries for an accepted override: rule, phase, reason, and a
// bounded scope. Scope is always "invocation" here; this build has no
// session-scoped override concept, only "this one command."
func nightInterlockOverrideAuditParams(overridden []nightWithheldInterlock) []map[string]any {
	out := make([]map[string]any, 0, len(overridden))
	for _, o := range overridden {
		out = append(out, map[string]any{
			"rule": o.Rule.Name, "phase": o.Rule.Phase, "reason": o.Reason, "scope": "invocation",
		})
	}
	return out
}

// nightCallerHasOverrideScope reports whether ac's principal holds
// [identity.ScopeNightOverride]: deliberately checked separately from
// [identity.ScopeNightCommand]: IDENTIFIER-REGISTER.md's own rationale is
// that starting a night must never imply bypassing a blocking interlock.
func nightCallerHasOverrideScope(ac authContext) bool {
	return ac.ok && ac.result.Principal.Role.Has(identity.ScopeNightOverride)
}

// nightInterlockGateProblem is the 409 a withheld phase answers with,
// naming every withholding rule's own failure text so an operator sees
// why without cross-referencing readiness separately.
func nightInterlockGateProblem(phase string, withheld []nightWithheldInterlock) *interlockGateProblem {
	if len(withheld) == 0 {
		return nil
	}
	return &interlockGateProblem{phase: phase, withheld: withheld}
}

// interlockGateProblem carries enough to build the eventual v1.Problem in
// nightsessioncontrol.go, kept as its own type here rather than importing
// v1 into this file's own pure-decision functions above.
type interlockGateProblem struct {
	phase    string
	withheld []nightWithheldInterlock
}

func (p *interlockGateProblem) detail() string {
	reasons := ""
	for i, w := range p.withheld {
		if i > 0 {
			reasons += "; "
		}
		reasons += fmt.Sprintf("%q: %s", w.Rule.Name, w.Reason)
	}
	return fmt.Sprintf("blocked by %d configured interlock(s) for phase %q: %s", len(p.withheld), p.phase, reasons)
}

// A caller computing an overall budget for several rules
// (nightRunReadinessCommand evaluates every configured rule in one call)
// should reason from mqttExpectMaxDeadlineSeconds (config/showaction.go,
// 120s): each rule's own dispatch never exceeds its own action's
// expect.deadlineSeconds, which write-time validation already bounds to
// that same constant.

// nightTrustedReadinessChecksTx returns the given session's most recent
// readiness checks, but ONLY when that result belongs to THIS session and
// is within the coordinator's configured maximum age, the identical
// trust filter start-night's own freshness gate applies
// (nightStartNightTx). Anything else (no readiness at all, a different
// epoch, a stale result, a corrupt checks payload) returns nil, which
// [nightEvaluatePhaseInterlockGate] already treats as
// evidence-unavailable for every rule it cannot find a check for.
//
// Unlike start-night, fade-out-night, power-down-presentation, and
// start-preshow do NOT themselves require readiness to exist at all;
// only a rule that names one of their own phases is affected by its
// absence, exactly the way [nightEvaluatePhaseInterlockGate]'s own
// "missing check" case already behaves.
func (h *handlers) nightTrustedReadinessChecksTx(ctx context.Context, tx *store.Tx, sessionID string, now time.Time) []nightReadinessCheck {
	readiness, err := tx.GetLatestNightReadiness(ctx, sessionID)
	if err != nil {
		return nil
	}
	if readiness.EpochID != sessionID {
		return nil
	}
	age := now.Sub(readiness.CompletedAt)
	if age < 0 || age > h.nightReadinessMaxAge {
		return nil
	}
	var wire []v1.NightReadinessCheck
	if err := json.Unmarshal([]byte(readiness.ChecksJSON), &wire); err != nil {
		return nil
	}
	return nightDecodeWireChecks(wire)
}

// nightGatePhaseTx is the shared STORED-evidence gate every tx-bound
// command below consults for its own declared phase: start-preshow,
// fade-out-night, and power-down-presentation. It reads the pinned
// night.session revision and the session's own trusted readiness result
// via tx (never live; see [nightPhaseInterlockGate]'s own doc comment
// for why these three commands cannot dispatch live evidence without
// risking the store's single-connection deadlock). A withheld phase
// returns a refusal problem naming every blocking rule; an authorized
// override returns audit params for the caller to fold into its own
// command's audit entry.
func (h *handlers) nightGatePhaseTx(ctx context.Context, tx *store.Tx, now time.Time, rec store.NightSessionRecord, phase string, overrides []nightInterlockOverrideRequest, callerHasOverrideScope bool) (*v1.Problem, []map[string]any) {
	payload, err := h.getPinnedNightSessionPayloadTx(ctx, tx, rec)
	if err != nil {
		// The pinned configuration itself could not be read: every
		// phase-matching block rule is evidence-unavailable, exactly as
		// if none of its checks were found.
		payload = config.NightSessionPayload{}
	}
	checks := h.nightTrustedReadinessChecksTx(ctx, tx, rec.ID, now)
	gate := nightEvaluatePhaseInterlockGate(payload, phase, checks, overrides, callerHasOverrideScope)
	if len(gate.Withheld) > 0 {
		p := nightNotReadyProblem(nightInterlockGateProblem(phase, gate.Withheld).detail())
		return &p, nil
	}
	if len(gate.Overridden) > 0 {
		return nil, nightInterlockOverrideAuditParams(gate.Overridden)
	}
	return nil, nil
}
