package api

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"
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

// setupNightInterlockFixtureAdvanceable is setupNightInterlockFixture with
// a caller-chosen maxAge and a mutable clock exposed via advance, added for
// this seam's own start-night stale-readiness re-run coverage: proving a
// live re-run that itself fails is reported as THAT failure, not the
// staleness message it replaced.
func setupNightInterlockFixtureAdvanceable(t *testing.T, phase, onUnavailable string, maxAge time.Duration) (api *API, brokers *fakeMQTTBrokerRegistry, operatorToken, adminToken string, obs *fakeObservationLister, advance func(time.Duration)) {
	t.Helper()
	advanceFn, clock := mutableClock(testNow)
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

	api = New(deps, Options{Clock: clock, Logger: testLogger(), NightReadinessMaxAge: maxAge})

	mustPutShow(t, api, adminToken, "halloween-2026", `{"name":"halloween-2026"}`)
	mustPutShowAction(t, api, adminToken, "lighting-fade-out", validShowActionFPPBody)
	mustPutShowAction(t, api, adminToken, "cooldown-check", validCooldownCheckActionBody)
	mustCreateNightSessionFSEQAsset(t, st, deps.AssetBackend, "halloween-2026", "resting-loop", "player-01")
	mustPutNightSession(t, api, adminToken, "halloween-main", nightSessionBodyWithCooldownInterlock(phase, onUnavailable))
	mustActivateNightSession(t, api, adminToken, "halloween-main")

	return api, brokers, operatorToken, adminToken, obsLister, advanceFn
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

// TestInterlockFreshReadinessRerunOnStaleStartNightReportsTheFreshFailure is
// this seam's own required coverage: a stale readiness result plus a
// FAILING fresh re-run must refuse start-night with THAT failure, never the
// staleness text it replaced. The interlock rule declares phase
// run-readiness (not start-night), so a withhold here can only have come
// from the live run-readiness pass start-night triggers on finding its
// stored result stale, never from start-night's own separate phase gate -
// this proves specifically that a fresh run-readiness refusal surfaces as
// itself. The condition passes before staleness (so preshow is reached at
// all) and is flipped to false only after the clock has advanced past the
// configured maximum age.
func TestInterlockFreshReadinessRerunOnStaleStartNightReportsTheFreshFailure(t *testing.T) {
	api, brokers, opToken, _, obs, advance := setupNightInterlockFixtureAdvanceable(t, "run-readiness", "block", time.Minute)
	brokers.msg = broker.Message{Payload: []byte("true")}
	runToPreshowForInterlockTest(t, api, opToken, obs)

	advance(2 * time.Minute)
	setHealthyFPPReachable(obs, testNow.Add(2*time.Minute))
	brokers.msg = broker.Message{Payload: []byte("false")}

	status, problem := nightCommandProblem(t, api, opToken, "start-night")
	if status != http.StatusConflict {
		t.Fatalf("start-night after a stale readiness result and a failing fresh re-run: status = %d, want 409; problem: %+v", status, problem)
	}
	if strings.Contains(problem.Detail, "past the configured maximum age") {
		t.Fatalf("start-night reported the staleness message instead of the fresh re-run's own failure: %q", problem.Detail)
	}
	if !containsAll(problem.Detail, "cooldown") {
		t.Fatalf("problem detail does not name the rule the FRESH re-run withheld: %+v", problem)
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
// TestPowerDownPresentationCommandPathReportsHonestlyNotNotConfigured is
// D6, this seam's safety review round: the ONLY existing honesty test
// covered nightshutdown.go's own nightReachStopped assignment. Reverting
// nightPowerDownPresentationApply's OWN assignment in
// nightsessioncontrol.go (the command path exercised when
// power-down-presentation is called against a session that is ALREADY
// stopped with no power phase resolved yet, e.g. one that reached
// stopped via fade-out-night alone) left the entire suite green, because
// nothing exercised that specific branch through the command itself.
func TestPowerDownPresentationCommandPathReportsHonestlyNotNotConfigured(t *testing.T) {
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
	mustNightCommand(t, api, opToken, "fade-out-night")

	// Force the session to "stopped" with no power phase resolved,
	// bypassing nightReachStopped entirely (a fresh restart, or a stop
	// observed by some other path, could plausibly leave exactly this
	// shape): State=stopped, PowerPhase="", ShutdownIntent still whatever
	// fade-out-night left it as.
	rec := mustGetCurrentSession(t, st)
	rec.State = nightStateStopped
	rec.PowerPhase = ""
	if err := st.UpdateNightSession(context.Background(), rec, testNow); err != nil {
		t.Fatalf("force session to stopped: %v", err)
	}

	out := mustNightCommand(t, api, opToken, "power-down-presentation")
	if out.Session.State != "stopped" {
		t.Fatalf("power-down-presentation against an already-stopped session: state = %q, want stopped", out.Session.State)
	}
	final := mustGetCurrentSession(t, st)
	if final.PowerPhase != nightPowerPhaseConfiguredNotDispatched {
		t.Fatalf("PowerPhase = %q, want %q: the COMMAND path must never report not_configured for a configured presentationPowerOff either", final.PowerPhase, nightPowerPhaseConfiguredNotDispatched)
	}
}

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

// TestNightBoundInterlockDispatch_ExpiredBudgetDegradesEvidenceToUnavailable
// is the reviewer's own aggregate-dispatch-time suspicion, proven at the
// mechanism it actually operates through: nightBoundInterlockDispatch
// derives an already-expired context when the aggregate budget has been
// exhausted, and a live rule evaluation against that context degrades to
// evidence-unavailable (via the ordinary ctx-cancellation path every
// store read already honors) instead of dispatching anyway.
func TestNightBoundInterlockDispatch_ExpiredBudgetDegradesEvidenceToUnavailable(t *testing.T) {
	origBudget := nightInterlockAggregateDispatchBudget
	nightInterlockAggregateDispatchBudget = -1 * time.Second
	defer func() { nightInterlockAggregateDispatchBudget = origBudget }()

	clock := fixedClock(testNow)
	svc, st, _ := newTestIdentityServiceWithStore(t, clock)
	admin := mustCreatePrincipal(t, svc, "admin-1", identity.RoleAdmin)
	adminToken := mustIssueToken(t, svc, admin.ID)
	deps, _ := nightControlTestDeps(svc, st)
	deps.MQTTBrokers = &fakeMQTTBrokerRegistry{msg: broker.Message{Payload: []byte("true")}}
	api := New(deps, Options{Clock: clock, Logger: testLogger()})
	mustPutShow(t, api, adminToken, "halloween-2026", `{"name":"halloween-2026"}`)
	mustPutShowAction(t, api, adminToken, "cooldown-check", validCooldownCheckActionBody)

	h := &handlers{deps: deps.withDefaults(), clock: clock, logger: testLogger()}
	rule := blockRule("cooldown", "start-night")
	rule.Signal = "cooldown-check"

	dispatchCtx, cancel := nightBoundInterlockDispatch(context.Background())
	defer cancel()
	if dispatchCtx.Err() == nil {
		t.Fatalf("expected an already-expired context from a negative aggregate budget")
	}

	decision := h.nightEvaluateInterlockRuleLive(dispatchCtx, rule)
	if decision.Health != nightHealthUnknown() {
		t.Fatalf("live evaluation against an expired dispatch budget = %+v, want health=unknown (evidence-unavailable), even though the broker would have answered true", decision)
	}
}

// TestNightCommandSurvivesServerWriteTimeout is this endpoint's own
// version of TestCueCatalogDeploySurvivesServerWriteTimeout
// (cuecatalogdeploy_test.go): a real *http.Server with a short
// WriteTimeout, and prepare-site's own live interlock dispatch paced
// slower than it but with NO reply ever arriving, proving
// handleNightCommand's own SetWriteDeadline extension
// (nightCommandHTTPWriteDeadline) is what lets the real withheld-gate
// Problem body reach the client, not merely that the constant is large on
// paper. Unlike the AwaitResponse-shaped routes this package's other
// write-timeout tests cover, this route's own honest slow-dispatch answer
// is a 409 naming the withholding interlock, never a 200 - proving that
// answer is what asserts the extension actually worked here.
func TestNightCommandSurvivesServerWriteTimeout(t *testing.T) {
	// A REAL current time, deliberately NOT this file's own fixed testNow
	// clock: SetWriteDeadline sets an ABSOLUTE deadline anchored to
	// h.now(), so a fixed-in-the-past clock would make that deadline
	// already elapsed before this test's real wall-clock write ever
	// happens - see TestCueCatalogDeploySurvivesServerWriteTimeout's
	// identical doc comment one file over.
	now := time.Now()
	svc, st, _ := newTestIdentityServiceWithStore(t, fixedClock(now))
	admin := mustCreatePrincipal(t, svc, "admin-1", identity.RoleAdmin)
	adminToken := mustIssueToken(t, svc, admin.ID)

	deps, _ := nightControlTestDeps(svc, st)
	backend := nightTestAssetBackend(t)
	deps.AssetBackend = backend
	brokers := &fakeMQTTBrokerRegistry{}
	deps.MQTTBrokers = brokers

	api := New(deps, Options{Clock: fixedClock(now), Logger: testLogger(), NightReadinessMaxAge: time.Hour})

	mustPutShow(t, api, adminToken, "halloween-2026", `{"name":"halloween-2026"}`)
	mustPutShowAction(t, api, adminToken, "lighting-fade-out", validShowActionFPPBody)
	mustPutShowAction(t, api, adminToken, "cooldown-check", validCooldownCheckActionBody)
	mustCreateNightSessionFSEQAsset(t, st, backend, "halloween-2026", "resting-loop", "player-01")
	// phase "prepare-site", onUnavailable "block": prepare-site's own live
	// interlock dispatch (nightPrepareSiteCommand, outside any transaction)
	// is what this test paces past the server's short WriteTimeout.
	mustPutNightSession(t, api, adminToken, "halloween-main", nightSessionBodyWithCooldownInterlock("prepare-site", "block"))
	mustActivateNightSession(t, api, adminToken, "halloween-main")

	auth := map[string]string{"Authorization": "Bearer " + adminToken}

	// No reply ever arrives, paced past the server's own short
	// WriteTimeout below, while staying comfortably inside this handler's
	// own (much larger) real nightCommandHTTPWriteDeadline.
	brokers.err = broker.ErrResponseDeadlineExceeded
	brokers.delay = 300 * time.Millisecond

	status, body := postThroughShortWriteTimeoutServer(t, api.Handler, "/api/v1/night/commands/prepare-site", "{}", auth)
	if status != http.StatusConflict {
		t.Fatalf("status = %d, want 409 (a withheld prepare-site interlock, evaluated slower than the server's own WriteTimeout, must still be reported honestly rather than severing the connection); body: %s", status, body)
	}

	var problem v1.Problem
	if err := json.Unmarshal(body, &problem); err != nil {
		t.Fatalf("decode problem: %v\nbody: %s", err, body)
	}
	if !strings.Contains(problem.Detail, "evidence source unavailable") {
		t.Fatalf("problem.Detail = %q, want it to name the interlock's own real \"evidence source unavailable\" reason, not a generic transport error", problem.Detail)
	}
}

// TestFadeOutNightOverrideIsAudited is D3, this seam's safety review
// round: nightRunExempt built its own issuer and never read
// out.auditParams, while nightRunGated did. fade-out-night and
// power-down-presentation both set auditParams for an accepted override
// and both route through nightRunExempt, so an accepted override on
// exactly the two commands where a bypass matters most was never
// audited at all. RESTING-MODE.md §10.1 requires rule, phase, reason,
// and bounded scope in the audit log; this asserts all four are actually
// present in the stored entry's own params, not merely that the command
// succeeded.
func TestFadeOutNightOverrideIsAudited(t *testing.T) {
	clock := fixedClock(testNow)
	svc, st, _ := newTestIdentityServiceWithStore(t, clock)
	admin := mustCreatePrincipal(t, svc, "admin-1", identity.RoleAdmin)
	adminToken := mustIssueToken(t, svc, admin.ID)
	operator := mustCreatePrincipal(t, svc, "operator-1", identity.RoleOperator)
	opToken := mustIssueToken(t, svc, operator.ID)

	deps, obs := nightControlTestDeps(svc, st)
	deps.FPP = nightWireFPPForReadiness(t)
	deps.AssetBackend = nightTestAssetBackend(t)
	brokers := &fakeMQTTBrokerRegistry{}
	deps.MQTTBrokers = brokers
	api := New(deps, Options{Clock: clock, Logger: testLogger(), NightReadinessMaxAge: time.Hour})

	mustPutShow(t, api, adminToken, "halloween-2026", `{"name":"halloween-2026"}`)
	mustPutShowAction(t, api, adminToken, "lighting-fade-out", validShowActionFPPBody)
	mustPutShowAction(t, api, adminToken, "cooldown-check", validCooldownCheckActionBody)
	mustCreateNightSessionFSEQAsset(t, st, deps.AssetBackend, "halloween-2026", "resting-loop", "player-01")
	mustPutNightSession(t, api, adminToken, "halloween-main", nightSessionBodyWithCooldownInterlock("fade-out-night", "block"))
	mustActivateNightSession(t, api, adminToken, "halloween-main")

	runToPreshowForInterlockTest(t, api, opToken, obs)
	brokers.msg = broker.Message{Payload: []byte("false")}

	resp, body := nightCommandRawBody(t, api, adminToken, "fade-out-night", `{"interlockOverrides":[{"rule":"cooldown","reason":"operator confirmed safe by hand"}]}`)
	if resp.StatusCode != http.StatusAccepted {
		t.Fatalf("fade-out-night with an authorized override: status = %d, want 202; body: %s", resp.StatusCode, body)
	}

	entries, err := st.ListAuditEntries(context.Background(), 0, 0)
	if err != nil {
		t.Fatalf("list audit entries: %v", err)
	}
	var found *string
	for _, e := range entries {
		if e.Action == "night.fade-out-night" {
			p := e.ParamsJSON
			found = &p
		}
	}
	if found == nil {
		t.Fatalf("no night.fade-out-night audit entry found among %d entries", len(entries))
	}
	for _, want := range []string{`"rule":"cooldown"`, `"phase":"fade-out-night"`, `"reason":"operator confirmed safe by hand"`, `"scope":"invocation"`} {
		if !strings.Contains(*found, want) {
			t.Fatalf("fade-out-night audit entry params = %q, want it to contain %q (RESTING-MODE.md §10.1: rule, phase, reason, bounded scope)", *found, want)
		}
	}
}
