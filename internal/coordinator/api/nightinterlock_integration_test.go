package api

import (
	"context"
	"net/http"
	"testing"
	"time"

	v1 "github.com/showmeshsystems/showmesh/internal/coordinator/api/v1"
	"github.com/showmeshsystems/showmesh/internal/coordinator/broker"
	"github.com/showmeshsystems/showmesh/internal/coordinator/identity"
)

// End-to-end coverage for Track F seam F6's own interlock gate: the
// whole path from a configured "block" rule, through run-readiness
// actually dispatching the named mqtt action, to start-night refusing
// (or, with a valid override, accepting) exactly as
// RESTING-MODE.md §10.1 requires. Everything below runs against fakes
// (fakeMQTTBrokerRegistry, an in-memory *store.Store); nothing here is
// evidence against a real broker, Home Assistant instance, or projector.

const validCooldownCheckActionBody = `{
	"show": "halloween-2026",
	"label": "Cooldown check",
	"safetyClass": "none",
	"target": {
		"integration": "mqtt",
		"broker": "home-automation",
		"publish": {"topic": "cmd/cooldown/check", "payload": "{}", "qos": 0, "retain": false},
		"expect": {"kind": "boolean", "topic": "cmd/cooldown/check/response", "deadlineSeconds": 5}
	}
}`

func nightSessionBodyWithCooldownInterlock(phase, onUnavailable string) string {
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
		"interlocks": [
			{
				"name": "cooldown",
				"phase": "` + phase + `",
				"posture": "block",
				"signal": "cooldown-check",
				"failureText": "enclosure has not confirmed a safe cooldown",
				"onUnavailable": "` + onUnavailable + `",
				"overridePolicy": "authorized-operator"
			}
		]
	}`
}

// setupNightInterlockFixture is setupNightControlFixtureWithBody plus a
// wired fakeMQTTBrokerRegistry and the cooldown-check show.action the
// interlock body above names.
func setupNightInterlockFixture(t *testing.T, phase, onUnavailable string) (api *API, brokers *fakeMQTTBrokerRegistry, operatorToken, adminToken string, obs *fakeObservationLister) {
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
	mustPutNightSession(t, api, adminToken, "halloween-main", nightSessionBodyWithCooldownInterlock(phase, onUnavailable))
	mustActivateNightSession(t, api, adminToken, "halloween-main")

	return api, brokers, operatorToken, adminToken, obsLister
}

func runToPreshowForInterlockTest(t *testing.T, api *API, token string, obs *fakeObservationLister) string {
	t.Helper()
	setHealthyFPPReachable(obs, testNow)
	mustNightCommand(t, api, token, "prepare-site")
	mustNightCommand(t, api, token, "run-readiness")
	got := mustNightCommand(t, api, token, "start-preshow")
	return got.Session.ID
}

func TestInterlockBlocksStartNightWhenConditionFalse(t *testing.T) {
	api, brokers, opToken, _, obs := setupNightInterlockFixture(t, "start-night", "block")
	brokers.msg = broker.Message{Payload: []byte("false")}
	runToPreshowForInterlockTest(t, api, opToken, obs)

	status, problem := nightCommandProblem(t, api, opToken, "start-night")
	if status != http.StatusConflict {
		t.Fatalf("start-night status = %d, want 409; problem: %+v", status, problem)
	}
	if !containsAll(problem.Detail, "cooldown") {
		t.Fatalf("problem detail does not name the withholding rule: %+v", problem)
	}
}

func TestInterlockAllowsStartNightWhenConditionTrue(t *testing.T) {
	api, brokers, opToken, _, obs := setupNightInterlockFixture(t, "start-night", "block")
	brokers.msg = broker.Message{Payload: []byte("true")}
	runToPreshowForInterlockTest(t, api, opToken, obs)

	got := mustNightCommand(t, api, opToken, "start-night")
	if got.Command.Outcome != "applied" {
		t.Fatalf("start-night outcome = %q, want applied when the interlock condition holds true", got.Command.Outcome)
	}
}

func TestInterlockOnUnavailableAllowLetsStartNightProceed(t *testing.T) {
	api, brokers, opToken, _, obs := setupNightInterlockFixture(t, "start-night", "allow")
	brokers.err = broker.ErrResponseDeadlineExceeded
	runToPreshowForInterlockTest(t, api, opToken, obs)

	got := mustNightCommand(t, api, opToken, "start-night")
	if got.Command.Outcome != "applied" {
		t.Fatalf("start-night outcome = %q, want applied: onUnavailable=allow must not withhold on a deadline", got.Command.Outcome)
	}
}

func TestInterlockOnUnavailableBlockRefusesStartNight(t *testing.T) {
	api, brokers, opToken, _, obs := setupNightInterlockFixture(t, "start-night", "block")
	brokers.err = broker.ErrResponseDeadlineExceeded
	runToPreshowForInterlockTest(t, api, opToken, obs)

	status, _ := nightCommandProblem(t, api, opToken, "start-night")
	if status != http.StatusConflict {
		t.Fatalf("start-night status = %d, want 409: onUnavailable=block must withhold on a deadline", status)
	}
}

// TestInterlockOverrideRequiresScopeNotJustNightCommand is
// IDENTIFIER-REGISTER.md's own rationale, proven end to end: an operator
// principal holds night:command (can start-night) but not night:override,
// and its override request must still be refused.
func TestInterlockOverrideRequiresScopeNotJustNightCommand(t *testing.T) {
	api, brokers, opToken, _, obs := setupNightInterlockFixture(t, "start-night", "block")
	brokers.msg = broker.Message{Payload: []byte("false")}
	runToPreshowForInterlockTest(t, api, opToken, obs)

	resp, body := nightCommandRawBody(t, api, opToken, "start-night", `{"interlockOverrides":[{"rule":"cooldown","reason":"operator confirmed safe by hand"}]}`)
	if resp.StatusCode != http.StatusConflict {
		t.Fatalf("start-night with an override from a non-override-scoped operator: status = %d, want 409; body: %s", resp.StatusCode, body)
	}
}

// TestInterlockOverrideByAuthorizedOperatorSucceeds is the full accepted
// path: an admin principal (holds night:override) names the withholding
// rule and a reason, and start-night proceeds.
func TestInterlockOverrideByAuthorizedOperatorSucceeds(t *testing.T) {
	api, brokers, _, adminToken, obs := setupNightInterlockFixture(t, "start-night", "block")
	brokers.msg = broker.Message{Payload: []byte("false")}
	runToPreshowForInterlockTest(t, api, adminToken, obs)

	resp, body := nightCommandRawBody(t, api, adminToken, "start-night", `{"interlockOverrides":[{"rule":"cooldown","reason":"operator confirmed safe by hand"}]}`)
	if resp.StatusCode != http.StatusAccepted {
		t.Fatalf("start-night with an authorized override: status = %d, want 202; body: %s", resp.StatusCode, body)
	}
}

// TestInterlockOverrideOfWrongRuleNameDoesNotHelp proves an override
// naming a DIFFERENT rule than the one actually withholding still
// refuses: a caller cannot bypass a rule it does not correctly name.
func TestInterlockOverrideOfWrongRuleNameDoesNotHelp(t *testing.T) {
	api, brokers, _, adminToken, obs := setupNightInterlockFixture(t, "start-night", "block")
	brokers.msg = broker.Message{Payload: []byte("false")}
	runToPreshowForInterlockTest(t, api, adminToken, obs)

	resp, body := nightCommandRawBody(t, api, adminToken, "start-night", `{"interlockOverrides":[{"rule":"not-the-real-rule","reason":"whatever"}]}`)
	if resp.StatusCode != http.StatusConflict {
		t.Fatalf("start-night with an override naming the wrong rule: status = %d, want 409; body: %s", resp.StatusCode, body)
	}
}

// --- power domain: siteControl.presentationPowerOff configured reports
// honestly, never as "not_configured" ---

const validSitePowerOffActionBody = `{
	"show": "halloween-2026",
	"label": "Site presentation power off",
	"safetyClass": "none",
	"target": {
		"integration": "mqtt",
		"broker": "home-automation",
		"publish": {"topic": "cmd/site/power-off", "payload": "{}", "qos": 0, "retain": false},
		"expect": {"kind": "none"}
	}
}`

func nightSessionBodyWithPresentationPowerOff() string {
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
		"siteControl": {
			"presentationPowerOff": {
				"action": "site-power-off",
				"powerDomain": "presentation",
				"domainProvenance": "operator-declared",
				"removalPolicy": "immediate",
				"immediateSafeAttestation": true
			}
		}
	}`
}

// TestPowerDownPresentationConfiguredReportsHonestlyNotNotConfigured is
// the domain-refusal test's own sibling for observability: a deployment
// that DOES configure presentationPowerOff must never see "not_configured"
// once it reaches stopped: that would claim nothing exists to remove
// power when something does (CLAUDE.md's evidence rule).
func TestPowerDownPresentationConfiguredReportsHonestlyNotNotConfigured(t *testing.T) {
	clock := fixedClock(testNow)
	svc, st, _ := newTestIdentityServiceWithStore(t, clock)
	admin := mustCreatePrincipal(t, svc, "admin-1", identity.RoleAdmin)
	adminToken := mustIssueToken(t, svc, admin.ID)
	operator := mustCreatePrincipal(t, svc, "operator-1", identity.RoleOperator)
	opToken := mustIssueToken(t, svc, operator.ID)

	deps, obs := nightControlTestDeps(svc, st)
	deps.FPP = nightWireFPPForReadiness(t)
	deps.AssetBackend = nightTestAssetBackend(t)
	api := New(deps, Options{Clock: clock, Logger: testLogger(), NightReadinessMaxAge: time.Hour})

	mustPutShow(t, api, adminToken, "halloween-2026", `{"name":"halloween-2026"}`)
	mustPutShowAction(t, api, adminToken, "lighting-fade-out", validShowActionFPPBody)
	mustPutShowAction(t, api, adminToken, "site-power-off", validSitePowerOffActionBody)
	mustCreateNightSessionFSEQAsset(t, st, deps.AssetBackend, "halloween-2026", "resting-loop", "player-01")
	mustPutNightSession(t, api, adminToken, "halloween-main", nightSessionBodyWithPresentationPowerOff())
	mustActivateNightSession(t, api, adminToken, "halloween-main")

	runToPreshowForInterlockTest(t, api, opToken, obs)
	out := mustNightCommand(t, api, opToken, "power-down-presentation")
	if out.Session.State != nightStateFadingOut {
		t.Fatalf("power-down-presentation: state = %q, want fading-out", out.Session.State)
	}

	rec := mustGetCurrentSession(t, st)
	h := &handlers{deps: deps.withDefaults(), clock: clock, logger: testLogger()}
	h.nightReachStopped(context.Background(), testNow, rec)

	final := mustGetCurrentSession(t, st)
	if final.State != nightStateStopped {
		t.Fatalf("after the stop was observed: state = %q, want stopped", final.State)
	}
	if final.PowerPhase != nightPowerPhaseConfiguredNotDispatched {
		t.Fatalf("PowerPhase = %q, want %q: a configured presentationPowerOff must never report not_configured", final.PowerPhase, nightPowerPhaseConfiguredNotDispatched)
	}
	got := mapNightPowerPhase(final)
	if got.State == v1.NightEvidenceNotConfigured {
		t.Fatalf("mapNightPowerPhase reports not_configured for a CONFIGURED presentationPowerOff: %+v", got)
	}
}
