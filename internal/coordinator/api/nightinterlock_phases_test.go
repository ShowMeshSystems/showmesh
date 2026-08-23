package api

import (
	"net/http"
	"testing"
	"time"

	"github.com/showmeshsystems/showmesh/internal/coordinator/broker"
	"github.com/showmeshsystems/showmesh/internal/coordinator/identity"
)

// This file closes the gap the coordinator's own review named: interlock
// gating previously had a control effect ONLY on start-night. It proves,
// against the real route table, that prepare-site, run-readiness,
// start-preshow, fade-out-night, and power-down-presentation each
// actually withhold their own declared phase, and that an observe-posture
// or other-phase rule stops nothing on any of them. projector-strike,
// enter-resting, and presentation-power-on stay out of reach; the first
// two live inside nightcuerun.go (a sibling seam's own file), and
// presentation-power-on has no lifecycle command of its own, so they are
// not covered here; RESTING-MODE.md's own configuration and display
// contract for those three is unchanged (validated and shown, no control
// effect).

func nightSessionBodyWithRawInterlock(interlockJSON string) string {
	return `{
		"show": "halloween-2026",
		"label": "Halloween main loop",
		"showPlaylist": {"fppInstanceId": "player-01", "playlist": "halloween-show"},
		"resting": {
			"fppInstanceId": "player-01",
			"playlist": "halloween-resting",
			"timelineAsset": {"show": "halloween-2026", "sequence": "resting-loop", "target": "player-01"},
			"endOfNightRepeat": true
		},
		"enterShow": {
			"cues": [
				{"name": "lighting-fade", "role": "lighting", "action": "lighting-fade-out", "offsetMs": -20000, "barrier": true}
			],
			"blackoutHoldMs": 6000
		},
		"enterResting": {"cues": [], "blackoutAfterShowMs": 6000},
		"interlocks": [` + interlockJSON + `]
	}`
}

func nightBlockInterlockJSON(name, phase string) string {
	return `{
		"name": "` + name + `",
		"phase": "` + phase + `",
		"posture": "block",
		"signal": "cooldown-check",
		"failureText": "enclosure has not confirmed a safe cooldown",
		"onUnavailable": "block",
		"overridePolicy": "authorized-operator"
	}`
}

func nightObserveInterlockJSON(name, phase string) string {
	return `{
		"name": "` + name + `",
		"phase": "` + phase + `",
		"posture": "observe",
		"signal": "cooldown-check",
		"failureText": "enclosure has not confirmed a safe cooldown"
	}`
}

// setupNightInterlockRawFixture is [setupNightInterlockFixture] generalized
// to accept an arbitrary interlock rule body, so a test can exercise a
// phase other than start-night or an observe/other-phase rule.
func setupNightInterlockRawFixture(t *testing.T, interlockJSON string) (api *API, brokers *fakeMQTTBrokerRegistry, operatorToken, adminToken string, obs *fakeObservationLister) {
	t.Helper()
	clock := fixedClock(testNow)
	svc, st, _ := newTestIdentityServiceWithStore(t, clock)
	admin := mustCreatePrincipal(t, svc, "admin-1", identity.RoleAdmin)
	adminToken = mustIssueToken(t, svc, admin.ID)
	operator := mustCreatePrincipal(t, svc, "operator-1", identity.RoleOperator)
	operatorToken = mustIssueToken(t, svc, operator.ID)

	deps, obsLister := nightControlTestDeps(svc, st)
	deps.FPP = nightWireFPPForReadiness(t)
	deps.AssetBackend = nightTestAssetBackend(t)
	brokers = &fakeMQTTBrokerRegistry{}
	deps.MQTTBrokers = brokers

	api = New(deps, Options{Clock: clock, Logger: testLogger(), NightReadinessMaxAge: time.Hour})

	mustPutShow(t, api, adminToken, "halloween-2026", `{"name":"halloween-2026"}`)
	mustPutShowAction(t, api, adminToken, "lighting-fade-out", validShowActionFPPBody)
	mustPutShowAction(t, api, adminToken, "cooldown-check", validCooldownCheckActionBody)
	mustCreateNightSessionFSEQAsset(t, st, deps.AssetBackend, "halloween-2026", "resting-loop", "player-01")
	mustPutNightSession(t, api, adminToken, "halloween-main", nightSessionBodyWithRawInterlock(interlockJSON))
	mustActivateNightSession(t, api, adminToken, "halloween-main")

	return api, brokers, operatorToken, adminToken, obsLister
}

// --- prepare-site: live-evidence gate ---

func TestInterlockBlocksPrepareSiteWhenConditionFalse(t *testing.T) {
	api, brokers, opToken, _, _ := setupNightInterlockRawFixture(t, nightBlockInterlockJSON("cooldown", "prepare-site"))
	brokers.msg = broker.Message{Payload: []byte("false")}

	status, problem := nightCommandProblem(t, api, opToken, "prepare-site")
	if status != http.StatusConflict {
		t.Fatalf("prepare-site status = %d, want 409; problem: %+v", status, problem)
	}
	if !containsAll(problem.Detail, "cooldown") {
		t.Fatalf("problem detail does not name the withholding rule: %+v", problem)
	}

	got := mustGetNightSession(t, api)
	if got.Session.ID != "" {
		t.Fatalf("a withheld prepare-site created a session anyway: %+v", got.Session)
	}
}

func TestInterlockAllowsPrepareSiteWhenConditionTrue(t *testing.T) {
	api, brokers, opToken, _, _ := setupNightInterlockRawFixture(t, nightBlockInterlockJSON("cooldown", "prepare-site"))
	brokers.msg = broker.Message{Payload: []byte("true")}

	out := mustNightCommand(t, api, opToken, "prepare-site")
	if out.Command.Outcome != "applied" {
		t.Fatalf("prepare-site outcome = %q, want applied", out.Command.Outcome)
	}
}

func TestInterlockObserveDoesNotBlockPrepareSite(t *testing.T) {
	api, brokers, opToken, _, _ := setupNightInterlockRawFixture(t, nightObserveInterlockJSON("cooldown", "prepare-site"))
	brokers.msg = broker.Message{Payload: []byte("false")}

	out := mustNightCommand(t, api, opToken, "prepare-site")
	if out.Command.Outcome != "applied" {
		t.Fatalf("an OBSERVE rule blocked prepare-site: outcome = %q, want applied", out.Command.Outcome)
	}
}

func TestInterlockOtherPhaseDoesNotBlockPrepareSite(t *testing.T) {
	api, brokers, opToken, _, _ := setupNightInterlockRawFixture(t, nightBlockInterlockJSON("cooldown", "fade-out-night"))
	brokers.msg = broker.Message{Payload: []byte("false")}

	out := mustNightCommand(t, api, opToken, "prepare-site")
	if out.Command.Outcome != "applied" {
		t.Fatalf("a rule declared for a DIFFERENT phase blocked prepare-site: outcome = %q, want applied", out.Command.Outcome)
	}
}

// --- run-readiness: live-evidence gate ---

func TestInterlockBlocksRunReadinessWhenConditionFalse(t *testing.T) {
	api, brokers, opToken, _, _ := setupNightInterlockRawFixture(t, nightBlockInterlockJSON("cooldown", "run-readiness"))
	brokers.msg = broker.Message{Payload: []byte("true")}
	mustNightCommand(t, api, opToken, "prepare-site")

	brokers.msg = broker.Message{Payload: []byte("false")}
	status, problem := nightCommandProblem(t, api, opToken, "run-readiness")
	if status != http.StatusConflict {
		t.Fatalf("run-readiness status = %d, want 409; problem: %+v", status, problem)
	}
	if !containsAll(problem.Detail, "cooldown") {
		t.Fatalf("problem detail does not name the withholding rule: %+v", problem)
	}
}

func TestInterlockAllowsRunReadinessWhenConditionTrue(t *testing.T) {
	api, brokers, opToken, _, obs := setupNightInterlockRawFixture(t, nightBlockInterlockJSON("cooldown", "run-readiness"))
	brokers.msg = broker.Message{Payload: []byte("true")}
	mustNightCommand(t, api, opToken, "prepare-site")
	setHealthyFPPReachable(obs, testNow)

	out := mustNightCommand(t, api, opToken, "run-readiness")
	if out.Command.Outcome != "applied" {
		t.Fatalf("run-readiness outcome = %q, want applied", out.Command.Outcome)
	}
}

func TestInterlockObserveDoesNotBlockRunReadiness(t *testing.T) {
	api, brokers, opToken, _, obs := setupNightInterlockRawFixture(t, nightObserveInterlockJSON("cooldown", "run-readiness"))
	brokers.msg = broker.Message{Payload: []byte("true")}
	mustNightCommand(t, api, opToken, "prepare-site")
	setHealthyFPPReachable(obs, testNow)

	brokers.msg = broker.Message{Payload: []byte("false")}
	out := mustNightCommand(t, api, opToken, "run-readiness")
	if out.Command.Outcome != "applied" {
		t.Fatalf("an OBSERVE rule blocked run-readiness: outcome = %q, want applied", out.Command.Outcome)
	}
}

func TestInterlockOtherPhaseDoesNotBlockRunReadiness(t *testing.T) {
	api, brokers, opToken, _, obs := setupNightInterlockRawFixture(t, nightBlockInterlockJSON("cooldown", "start-preshow"))
	brokers.msg = broker.Message{Payload: []byte("true")}
	mustNightCommand(t, api, opToken, "prepare-site")
	setHealthyFPPReachable(obs, testNow)

	brokers.msg = broker.Message{Payload: []byte("false")}
	out := mustNightCommand(t, api, opToken, "run-readiness")
	if out.Command.Outcome != "applied" {
		t.Fatalf("a rule declared for a DIFFERENT phase blocked run-readiness: outcome = %q, want applied", out.Command.Outcome)
	}
}

// --- start-preshow: stored-evidence gate ---

func TestInterlockBlocksStartPreshowWhenConditionFalse(t *testing.T) {
	api, brokers, opToken, _, obs := setupNightInterlockRawFixture(t, nightBlockInterlockJSON("cooldown", "start-preshow"))
	mustNightCommand(t, api, opToken, "prepare-site")
	setHealthyFPPReachable(obs, testNow)
	brokers.msg = broker.Message{Payload: []byte("false")}
	mustNightCommand(t, api, opToken, "run-readiness")

	status, problem := nightCommandProblem(t, api, opToken, "start-preshow")
	if status != http.StatusConflict {
		t.Fatalf("start-preshow status = %d, want 409; problem: %+v", status, problem)
	}
	if !containsAll(problem.Detail, "cooldown") {
		t.Fatalf("problem detail does not name the withholding rule: %+v", problem)
	}
}

func TestInterlockAllowsStartPreshowWhenConditionTrue(t *testing.T) {
	api, brokers, opToken, _, obs := setupNightInterlockRawFixture(t, nightBlockInterlockJSON("cooldown", "start-preshow"))
	mustNightCommand(t, api, opToken, "prepare-site")
	setHealthyFPPReachable(obs, testNow)
	brokers.msg = broker.Message{Payload: []byte("true")}
	mustNightCommand(t, api, opToken, "run-readiness")

	out := mustNightCommand(t, api, opToken, "start-preshow")
	if out.Command.Outcome != "applied" {
		t.Fatalf("start-preshow outcome = %q, want applied", out.Command.Outcome)
	}
}

func TestInterlockObserveDoesNotBlockStartPreshow(t *testing.T) {
	api, brokers, opToken, _, obs := setupNightInterlockRawFixture(t, nightObserveInterlockJSON("cooldown", "start-preshow"))
	mustNightCommand(t, api, opToken, "prepare-site")
	setHealthyFPPReachable(obs, testNow)
	brokers.msg = broker.Message{Payload: []byte("false")}
	mustNightCommand(t, api, opToken, "run-readiness")

	out := mustNightCommand(t, api, opToken, "start-preshow")
	if out.Command.Outcome != "applied" {
		t.Fatalf("an OBSERVE rule blocked start-preshow: outcome = %q, want applied", out.Command.Outcome)
	}
}

func TestInterlockOtherPhaseDoesNotBlockStartPreshow(t *testing.T) {
	api, brokers, opToken, _, obs := setupNightInterlockRawFixture(t, nightBlockInterlockJSON("cooldown", "start-night"))
	mustNightCommand(t, api, opToken, "prepare-site")
	setHealthyFPPReachable(obs, testNow)
	brokers.msg = broker.Message{Payload: []byte("false")}
	mustNightCommand(t, api, opToken, "run-readiness")

	out := mustNightCommand(t, api, opToken, "start-preshow")
	if out.Command.Outcome != "applied" {
		t.Fatalf("a rule declared for a DIFFERENT phase blocked start-preshow: outcome = %q, want applied", out.Command.Outcome)
	}
}

// TestInterlockStartPreshowUngatedWithNoReadinessRun proves the
// no-readiness-at-all case is treated as evidence-unavailable, not as a
// hard requirement that readiness exist: with onUnavailable "allow" this
// still succeeds.
func TestInterlockStartPreshowUngatedWithNoReadinessRun(t *testing.T) {
	api, _, opToken, _, _ := setupNightInterlockRawFixture(t, `{
		"name": "cooldown", "phase": "start-preshow", "posture": "block",
		"signal": "cooldown-check", "failureText": "f",
		"onUnavailable": "allow", "overridePolicy": "none"
	}`)
	mustNightCommand(t, api, opToken, "prepare-site")

	out := mustNightCommand(t, api, opToken, "start-preshow")
	if out.Command.Outcome != "applied" {
		t.Fatalf("start-preshow with onUnavailable=allow and no readiness ever run: outcome = %q, want applied", out.Command.Outcome)
	}
}

// --- fade-out-night: stored-evidence gate ---

func TestInterlockBlocksFadeOutNightWhenConditionFalse(t *testing.T) {
	api, brokers, opToken, _, obs := setupNightInterlockRawFixture(t, nightBlockInterlockJSON("cooldown", "fade-out-night"))
	mustNightCommand(t, api, opToken, "prepare-site")
	setHealthyFPPReachable(obs, testNow)
	brokers.msg = broker.Message{Payload: []byte("false")}
	mustNightCommand(t, api, opToken, "run-readiness")
	mustNightCommand(t, api, opToken, "start-preshow")

	status, problem := nightCommandProblem(t, api, opToken, "fade-out-night")
	if status != http.StatusConflict {
		t.Fatalf("fade-out-night status = %d, want 409; problem: %+v", status, problem)
	}
	if !containsAll(problem.Detail, "cooldown") {
		t.Fatalf("problem detail does not name the withholding rule: %+v", problem)
	}

	got := mustGetNightSession(t, api)
	if got.Session.State != "preshow" {
		t.Fatalf("a withheld fade-out-night changed state anyway: %q, want preshow unchanged", got.Session.State)
	}
}

func TestInterlockAllowsFadeOutNightWhenConditionTrue(t *testing.T) {
	api, brokers, opToken, _, obs := setupNightInterlockRawFixture(t, nightBlockInterlockJSON("cooldown", "fade-out-night"))
	mustNightCommand(t, api, opToken, "prepare-site")
	setHealthyFPPReachable(obs, testNow)
	brokers.msg = broker.Message{Payload: []byte("true")}
	mustNightCommand(t, api, opToken, "run-readiness")
	mustNightCommand(t, api, opToken, "start-preshow")

	out := mustNightCommand(t, api, opToken, "fade-out-night")
	if out.Command.Outcome != "applied" || out.Session.State != "fading-out" {
		t.Fatalf("fade-out-night = outcome=%q state=%q, want applied/fading-out", out.Command.Outcome, out.Session.State)
	}
}

func TestInterlockObserveDoesNotBlockFadeOutNight(t *testing.T) {
	api, brokers, opToken, _, obs := setupNightInterlockRawFixture(t, nightObserveInterlockJSON("cooldown", "fade-out-night"))
	mustNightCommand(t, api, opToken, "prepare-site")
	setHealthyFPPReachable(obs, testNow)
	brokers.msg = broker.Message{Payload: []byte("false")}
	mustNightCommand(t, api, opToken, "run-readiness")
	mustNightCommand(t, api, opToken, "start-preshow")

	out := mustNightCommand(t, api, opToken, "fade-out-night")
	if out.Command.Outcome != "applied" {
		t.Fatalf("an OBSERVE rule blocked fade-out-night: outcome = %q, want applied", out.Command.Outcome)
	}
}

func TestInterlockOtherPhaseDoesNotBlockFadeOutNight(t *testing.T) {
	api, brokers, opToken, _, obs := setupNightInterlockRawFixture(t, nightBlockInterlockJSON("cooldown", "power-down-presentation"))
	mustNightCommand(t, api, opToken, "prepare-site")
	setHealthyFPPReachable(obs, testNow)
	brokers.msg = broker.Message{Payload: []byte("false")}
	mustNightCommand(t, api, opToken, "run-readiness")
	mustNightCommand(t, api, opToken, "start-preshow")

	out := mustNightCommand(t, api, opToken, "fade-out-night")
	if out.Command.Outcome != "applied" {
		t.Fatalf("a rule declared for a DIFFERENT phase blocked fade-out-night: outcome = %q, want applied", out.Command.Outcome)
	}
}

// TestInterlockFadeOutNightIdempotentReplayConsultsNoInterlock proves a
// pure replay (nothing left to fade) never re-evaluates the gate: it
// always succeeds even against condition-false evidence, because
// applyNightShutdownEffect itself reports no change.
func TestInterlockFadeOutNightIdempotentReplayConsultsNoInterlock(t *testing.T) {
	api, brokers, opToken, _, obs := setupNightInterlockRawFixture(t, nightBlockInterlockJSON("cooldown", "fade-out-night"))
	mustNightCommand(t, api, opToken, "prepare-site")
	setHealthyFPPReachable(obs, testNow)
	brokers.msg = broker.Message{Payload: []byte("true")}
	mustNightCommand(t, api, opToken, "run-readiness")
	mustNightCommand(t, api, opToken, "start-preshow")
	mustNightCommand(t, api, opToken, "fade-out-night")

	brokers.msg = broker.Message{Payload: []byte("false")}
	out := mustNightCommand(t, api, opToken, "fade-out-night")
	if out.Command.Outcome != "idempotent_no_op" {
		t.Fatalf("replaying fade-out-night once already fading out: outcome = %q, want idempotent_no_op", out.Command.Outcome)
	}
}

// --- power-down-presentation: stored-evidence gate ---

func TestInterlockBlocksPowerDownPresentationWhenConditionFalse(t *testing.T) {
	api, brokers, opToken, _, obs := setupNightInterlockRawFixture(t, nightBlockInterlockJSON("cooldown", "power-down-presentation"))
	mustNightCommand(t, api, opToken, "prepare-site")
	setHealthyFPPReachable(obs, testNow)
	brokers.msg = broker.Message{Payload: []byte("false")}
	mustNightCommand(t, api, opToken, "run-readiness")
	mustNightCommand(t, api, opToken, "start-preshow")

	status, problem := nightCommandProblem(t, api, opToken, "power-down-presentation")
	if status != http.StatusConflict {
		t.Fatalf("power-down-presentation status = %d, want 409; problem: %+v", status, problem)
	}
	if !containsAll(problem.Detail, "cooldown") {
		t.Fatalf("problem detail does not name the withholding rule: %+v", problem)
	}

	got := mustGetNightSession(t, api)
	if got.Session.State != "preshow" {
		t.Fatalf("a withheld power-down-presentation changed state anyway: %q, want preshow unchanged", got.Session.State)
	}
}

func TestInterlockAllowsPowerDownPresentationWhenConditionTrue(t *testing.T) {
	api, brokers, opToken, _, obs := setupNightInterlockRawFixture(t, nightBlockInterlockJSON("cooldown", "power-down-presentation"))
	mustNightCommand(t, api, opToken, "prepare-site")
	setHealthyFPPReachable(obs, testNow)
	brokers.msg = broker.Message{Payload: []byte("true")}
	mustNightCommand(t, api, opToken, "run-readiness")
	mustNightCommand(t, api, opToken, "start-preshow")

	out := mustNightCommand(t, api, opToken, "power-down-presentation")
	if out.Command.Outcome != "applied" || out.Session.State != "fading-out" {
		t.Fatalf("power-down-presentation = outcome=%q state=%q, want applied/fading-out", out.Command.Outcome, out.Session.State)
	}
}

func TestInterlockObserveDoesNotBlockPowerDownPresentation(t *testing.T) {
	api, brokers, opToken, _, obs := setupNightInterlockRawFixture(t, nightObserveInterlockJSON("cooldown", "power-down-presentation"))
	mustNightCommand(t, api, opToken, "prepare-site")
	setHealthyFPPReachable(obs, testNow)
	brokers.msg = broker.Message{Payload: []byte("false")}
	mustNightCommand(t, api, opToken, "run-readiness")
	mustNightCommand(t, api, opToken, "start-preshow")

	out := mustNightCommand(t, api, opToken, "power-down-presentation")
	if out.Command.Outcome != "applied" {
		t.Fatalf("an OBSERVE rule blocked power-down-presentation: outcome = %q, want applied", out.Command.Outcome)
	}
}

func TestInterlockOtherPhaseDoesNotBlockPowerDownPresentation(t *testing.T) {
	api, brokers, opToken, _, obs := setupNightInterlockRawFixture(t, nightBlockInterlockJSON("cooldown", "fade-out-night"))
	mustNightCommand(t, api, opToken, "prepare-site")
	setHealthyFPPReachable(obs, testNow)
	brokers.msg = broker.Message{Payload: []byte("false")}
	mustNightCommand(t, api, opToken, "run-readiness")
	mustNightCommand(t, api, opToken, "start-preshow")

	out := mustNightCommand(t, api, opToken, "power-down-presentation")
	if out.Command.Outcome != "applied" {
		t.Fatalf("a rule declared for a DIFFERENT phase blocked power-down-presentation: outcome = %q, want applied", out.Command.Outcome)
	}
}

// --- end-session remains the unconditional escape ---

// TestConfigRejectsOverridePolicyNoneOnShutdownPhases is D1+D2's own
// write-time half: RESTING-MODE.md's own shutdown guarantee (this seam's
// safety review round) refuses a "block" rule on fade-out-night or
// power-down-presentation that declares overridePolicy "none" at
// PUT /config/night.session/{id} time, before any session ever pins it,
// rather than letting a night be configured that cannot be made safe.
func TestConfigRejectsOverridePolicyNoneOnShutdownPhases(t *testing.T) {
	for _, phase := range []string{"fade-out-night", "power-down-presentation"} {
		t.Run(phase, func(t *testing.T) {
			clock := fixedClock(testNow)
			svc, st, _ := newTestIdentityServiceWithStore(t, clock)
			admin := mustCreatePrincipal(t, svc, "admin-1", identity.RoleAdmin)
			adminToken := mustIssueToken(t, svc, admin.ID)
			deps, _ := nightControlTestDeps(svc, st)
			deps.FPP = nightWireFPPForReadiness(t)
			deps.AssetBackend = nightTestAssetBackend(t)
			deps.MQTTBrokers = &fakeMQTTBrokerRegistry{}
			api := New(deps, Options{Clock: clock, Logger: testLogger(), NightReadinessMaxAge: time.Hour})

			mustPutShow(t, api, adminToken, "halloween-2026", `{"name":"halloween-2026"}`)
			mustPutShowAction(t, api, adminToken, "lighting-fade-out", validShowActionFPPBody)
			mustPutShowAction(t, api, adminToken, "cooldown-check", validCooldownCheckActionBody)
			mustCreateNightSessionFSEQAsset(t, st, deps.AssetBackend, "halloween-2026", "resting-loop", "player-01")

			body := nightSessionBodyWithRawInterlock(`{
				"name": "cooldown", "phase": "` + phase + `", "posture": "block",
				"signal": "cooldown-check", "failureText": "f",
				"onUnavailable": "block", "overridePolicy": "none"
			}`)
			req := newJSONRequest(t, http.MethodPut, "/api/v1/config/night.session/halloween-main", body, map[string]string{"Authorization": "Bearer " + adminToken})
			resp, respBody := doRawRequest(t, api.Handler, req)
			if resp.StatusCode != http.StatusBadRequest {
				t.Fatalf("PUT night.session with overridePolicy:none on phase %q: status = %d, want 400; body: %s", phase, resp.StatusCode, respBody)
			}
			if !containsAll(string(respBody), "interlock-shutdown-phase-requires-override") {
				t.Fatalf("expected the interlock-shutdown-phase-requires-override problem type; body: %s", respBody)
			}
		})
	}
}

// TestConfigAcceptsOverridePolicyAuthorizedOperatorOnShutdownPhases proves
// the refusal is specific to "none", not to configuring a shutdown-phase
// interlock at all.
func TestConfigAcceptsOverridePolicyAuthorizedOperatorOnShutdownPhases(t *testing.T) {
	for _, phase := range []string{"fade-out-night", "power-down-presentation"} {
		t.Run(phase, func(t *testing.T) {
			setupNightInterlockRawFixture(t, `{
				"name": "cooldown", "phase": "`+phase+`", "posture": "block",
				"signal": "cooldown-check", "failureText": "f",
				"onUnavailable": "block", "overridePolicy": "authorized-operator"
			}`)
		})
	}
}

// TestSafeSitePathExistsWithBothShutdownPhasesBlockedByEveryCombination is
// the coordinator's own required property proof, not the narrower claim
// this seam previously tested. It configures FOUR block rules across
// BOTH shutdown phases (every onUnavailable value on each phase, the
// full combination space this build can construct now that overridePolicy:
// none is refused at write time), drives the site to preshow with the
// cooldown check answering false, and shows two things by name:
//
//  1. An operator WITHOUT night:override cannot end the night through
//     fade-out-night or power-down-presentation (both refuse), but
//     end-session, which declares no interlock phase and consults no
//     gate, still reaches "stopped" for them. That is the guaranteed
//     exit for a caller who cannot override.
//  2. THE SAFE-SITE PATH: an admin holding night:override, naming every
//     one of the four rules in interlockOverrides, successfully runs
//     fade-out-night and reaches "fading-out": unlike end-session, this
//     path is the one that actually issues FPP's own stop (nightloop.go's
//     fading-out tick, nightshutdown.go), so it is the path that makes
//     the SITE safe, not merely the session record.
func TestSafeSitePathExistsWithBothShutdownPhasesBlockedByEveryCombination(t *testing.T) {
	rules := `
		{"name": "fade-block-block", "phase": "fade-out-night", "posture": "block", "signal": "cooldown-check", "failureText": "f", "onUnavailable": "block", "overridePolicy": "authorized-operator"},
		{"name": "fade-block-allow", "phase": "fade-out-night", "posture": "block", "signal": "cooldown-check", "failureText": "f", "onUnavailable": "allow", "overridePolicy": "authorized-operator"},
		{"name": "power-block-block", "phase": "power-down-presentation", "posture": "block", "signal": "cooldown-check", "failureText": "f", "onUnavailable": "block", "overridePolicy": "authorized-operator"},
		{"name": "power-block-allow", "phase": "power-down-presentation", "posture": "block", "signal": "cooldown-check", "failureText": "f", "onUnavailable": "allow", "overridePolicy": "authorized-operator"}
	`
	api, brokers, opToken, adminToken, obs := setupNightInterlockRawFixture(t, rules)
	mustNightCommand(t, api, opToken, "prepare-site")
	setHealthyFPPReachable(obs, testNow)
	mustNightCommand(t, api, opToken, "run-readiness")
	mustNightCommand(t, api, opToken, "start-preshow")

	// The site answers false: every "block" rule with onUnavailable
	// "allow" still withholds on a genuine negative answer (RESTING-MODE.md
	// §10.1's own matrix: onUnavailable governs only the unavailable
	// column), so all four rules withhold both phases right now.
	brokers.msg = broker.Message{Payload: []byte("false")}

	// (1) No override scope: both shutdown commands refuse, end-session
	// still reaches stopped.
	if status, _ := nightCommandProblem(t, api, opToken, "fade-out-night"); status != http.StatusConflict {
		t.Fatalf("fade-out-night without override: status = %d, want 409", status)
	}
	if status, _ := nightCommandProblem(t, api, opToken, "power-down-presentation"); status != http.StatusConflict {
		t.Fatalf("power-down-presentation without override: status = %d, want 409", status)
	}

	// (2) THE SAFE-SITE PATH: an authorized override on every withholding
	// rule lets fade-out-night actually run.
	overrideBody := `{"interlockOverrides":[
		{"rule":"fade-block-block","reason":"operator confirmed safe by hand"},
		{"rule":"fade-block-allow","reason":"operator confirmed safe by hand"}
	]}`
	resp, body := nightCommandRawBody(t, api, adminToken, "fade-out-night", overrideBody)
	if resp.StatusCode != http.StatusAccepted {
		t.Fatalf("fade-out-night with an authorized override on every fade-out-night rule: status = %d, want 202; body: %s", resp.StatusCode, body)
	}
	got := mustGetNightSession(t, api)
	if got.Session.State != "fading-out" {
		t.Fatalf("state after the authorized override = %q, want fading-out: this is the path that actually issues FPP's own stop, which end-session never does", got.Session.State)
	}
}

// --- also-fix: decodeNightCommandBody strictness and scope ---

// TestNightCommandBodyRejectsMisspelledInterlockOverridesField proves the
// decoder is strict: a misspelled "interlockOverride" (missing the
// trailing "s") is refused outright rather than silently decoding as
// zero overrides, which previously left the caller staring at a 409 with
// no hint the override never arrived.
func TestNightCommandBodyRejectsMisspelledInterlockOverridesField(t *testing.T) {
	api, brokers, opToken, _, obs := setupNightInterlockFixture(t, "start-night", "block")
	brokers.msg = broker.Message{Payload: []byte("false")}
	runToPreshowForInterlockTest(t, api, opToken, obs)

	resp, body := nightCommandRawBody(t, api, opToken, "start-night", `{"interlockOverride":[{"rule":"cooldown","reason":"x"}]}`)
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("start-night with a misspelled interlockOverrides field: status = %d, want 400; body: %s", resp.StatusCode, body)
	}
}

// TestNightCommandBodyRejectsInterlockOverridesOnEndSession and its
// request-final-show sibling prove the decoder refuses interlockOverrides
// on the two commands that consult no interlock at all, rather than
// silently ignoring a value the caller believed would do something.
func TestNightCommandBodyRejectsInterlockOverridesOnEndSession(t *testing.T) {
	api, _, opToken, _, _ := setupNightInterlockFixture(t, "start-night", "block")
	resp, body := nightCommandRawBody(t, api, opToken, "end-session", `{"interlockOverrides":[{"rule":"cooldown","reason":"x"}]}`)
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("end-session with interlockOverrides: status = %d, want 400; body: %s", resp.StatusCode, body)
	}
}

func TestNightCommandBodyRejectsInterlockOverridesOnRequestFinalShow(t *testing.T) {
	api, _, opToken, _, _ := setupNightInterlockFixture(t, "start-night", "block")
	resp, body := nightCommandRawBody(t, api, opToken, "request-final-show", `{"interlockOverrides":[{"rule":"cooldown","reason":"x"}]}`)
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("request-final-show with interlockOverrides: status = %d, want 400; body: %s", resp.StatusCode, body)
	}
}
