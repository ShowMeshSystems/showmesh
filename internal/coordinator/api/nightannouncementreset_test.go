package api

import (
	"bytes"
	"context"
	"errors"
	"log/slog"
	"strings"
	"testing"
	"time"

	"github.com/showmeshsystems/showmesh/internal/coordinator/identity"
	"github.com/showmeshsystems/showmesh/internal/coordinator/store"
)

// Item 3: prepare-site's own announcement-session reset
// (nightResetAnnouncementSessionsAtPrepareSite, nightsessioncontrol.go).

// announcementNightSessionBody names one announcement cue in enterResting,
// bound to the "thank-you" show.action announcementShowActionBody defines.
const announcementNightSessionBody = `{
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
	"enterResting": {
		"cues": [
			{"name": "thank-you", "role": "announcement", "action": "thank-you", "offsetMs": 0}
		],
		"blackoutAfterShowMs": 6000
	}
}`

const announcementShowActionBody = `{
	"show": "halloween-2026",
	"label": "Thank you announcement",
	"safetyClass": "none",
	"target": {
		"integration": "audio",
		"audioNodeId": "node-a",
		"audioSessionId": "announcement-1",
		"audioAction": "audio.session.apply"
	}
}`

// setupPrepareSiteAnnouncementFixture wires a full HTTP-driven night
// lifecycle fixture (setupNightControlFixture's own pattern, one file
// over) with one announcement cue bound to an audio show.action, a
// fakeAudioPublisher this test controls the node's own responses
// through, and a buffering logger so a test can assert on a logged
// warning - Item 3's own WARN AND PROCEED contract.
func setupPrepareSiteAnnouncementFixture(t *testing.T) (api *API, st *store.Store, token string, obs *fakeObservationLister, pub *fakeAudioPublisher, logs *bytes.Buffer) {
	t.Helper()
	svc, st, _ := newTestIdentityServiceWithStore(t, fixedClock(testNow))
	admin := mustCreatePrincipal(t, svc, "admin-1", identity.RoleAdmin)
	adminToken := mustIssueToken(t, svc, admin.ID)
	operator := mustCreatePrincipal(t, svc, "operator-1", identity.RoleOperator)
	opToken := mustIssueToken(t, svc, operator.ID)

	deps, obsLister := nightControlTestDeps(svc, st)
	pub = &fakeAudioPublisher{}
	deps.AudioPublisher = pub
	deps.AudioSessions = st

	logs = &bytes.Buffer{}
	logger := slog.New(slog.NewTextHandler(logs, nil))

	api = New(deps, Options{Clock: fixedClock(testNow), Logger: logger, NightReadinessMaxAge: time.Hour})

	mustPutShow(t, api, adminToken, "halloween-2026", `{"name":"halloween-2026"}`)
	mustPutShowAction(t, api, adminToken, "lighting-fade-out", validShowActionFPPBody)
	mustPutShowAction(t, api, adminToken, "thank-you", announcementShowActionBody)
	mustCreateNightSessionAsset(t, st, "halloween-2026", "resting-loop", "player-01")
	mustPutNightSession(t, api, adminToken, "halloween-main", announcementNightSessionBody)
	mustActivateNightSession(t, api, adminToken, "halloween-main")

	return api, st, opToken, obsLister, pub, logs
}

// mutation target: the out.outcome == nightOutcomeApplied guard around
// nightResetAnnouncementSessionsAtPrepareSite. Drop it (or the call
// itself) and no audio.session.clear is ever dispatched, failing here.
func TestNightPrepareSite_ResetsAnnouncementSessionOnNewEpoch(t *testing.T) {
	api, st, token, obs, pub, _ := setupPrepareSiteAnnouncementFixture(t)
	setHealthyFPPReachable(obs, testNow)
	pub.resultsByAction = announcementNodeResults("announcement-1")

	prep := mustNightCommand(t, api, token, "prepare-site")
	if prep.Command.Outcome != "applied" {
		t.Fatalf("prepare-site outcome = %q, want applied", prep.Command.Outcome)
	}

	var clears []dispatchedAudioCommand
	for _, d := range pub.dispatched {
		if d.Action == "audio.session.clear" {
			clears = append(clears, d)
		}
	}
	if len(clears) != 1 {
		t.Fatalf("dispatched %d audio.session.clear command(s) at prepare-site, want exactly 1: %v", len(clears), pub.dispatched)
	}
	if clears[0].Params["sessionId"] != "announcement-1" {
		t.Fatalf("clear params = %v, want sessionId \"announcement-1\"", clears[0].Params)
	}

	persisted, err := st.GetAudioSession(context.Background(), "node-a", "announcement-1")
	if err != nil {
		t.Fatalf("get persisted audio session: %v", err)
	}
	if persisted.Revision == 0 {
		t.Fatalf("persisted audio_sessions revision after the reset = 0, want it to have advanced past the confirmed clear")
	}
}

// mutation target: the WARN-AND-PROCEED contract itself. Making an
// unacknowledged reset return an error (or a *v1.Problem) from
// nightPrepareSiteCommand would fail this: prepare-site must still
// succeed, and the session must still open, when the announcement's own
// audio node cannot be reached.
func TestNightPrepareSite_AnnouncementResetUnacknowledged_NightStillStartsAndWarns(t *testing.T) {
	api, _, token, obs, pub, logs := setupPrepareSiteAnnouncementFixture(t)
	setHealthyFPPReachable(obs, testNow)
	pub.beforePublishErr = errors.New("node unreachable")

	prep := mustNightCommand(t, api, token, "prepare-site")
	if prep.Command.Outcome != "applied" {
		t.Fatalf("prepare-site outcome = %q, want applied even though the announcement reset could not reach the node", prep.Command.Outcome)
	}
	if prep.Session.State != nightStatePreparing {
		t.Fatalf("session state = %q, want %q; the night must start regardless of an unreachable node", prep.Session.State, nightStatePreparing)
	}
	if !strings.Contains(logs.String(), "announcement-1") {
		t.Fatalf("no warning naming the unacknowledged session %q was logged: %s", "announcement-1", logs.String())
	}
}

// multiAnnouncementNightSessionBody names three announcement cues in
// enterResting, each bound to its own show.action and its own distinct
// audio session on node-a - three-item counterpart to
// announcementNightSessionBody, above, purpose-built to prove
// nightAnnouncementResetAtPrepareSiteBudget bounds the reset pass's own
// total cost regardless of how many distinct sessions it walks.
const multiAnnouncementNightSessionBody = `{
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
	"enterResting": {
		"cues": [
			{"name": "thank-you-1", "role": "announcement", "action": "thank-you-1", "offsetMs": 0},
			{"name": "thank-you-2", "role": "announcement", "action": "thank-you-2", "offsetMs": 0},
			{"name": "thank-you-3", "role": "announcement", "action": "thank-you-3", "offsetMs": 0}
		],
		"blackoutAfterShowMs": 6000
	}
}`

func multiAnnouncementShowActionBody(sessionID string) string {
	return `{
		"show": "halloween-2026",
		"label": "Thank you announcement",
		"safetyClass": "none",
		"target": {
			"integration": "audio",
			"audioNodeId": "node-a",
			"audioSessionId": "` + sessionID + `",
			"audioAction": "audio.session.apply"
		}
	}`
}

// setupPrepareSiteMultiAnnouncementFixture is
// setupPrepareSiteAnnouncementFixture's three-session counterpart.
func setupPrepareSiteMultiAnnouncementFixture(t *testing.T) (api *API, token string, obs *fakeObservationLister, pub *fakeAudioPublisher, logs *bytes.Buffer) {
	t.Helper()
	svc, st, _ := newTestIdentityServiceWithStore(t, fixedClock(testNow))
	admin := mustCreatePrincipal(t, svc, "admin-1", identity.RoleAdmin)
	adminToken := mustIssueToken(t, svc, admin.ID)
	operator := mustCreatePrincipal(t, svc, "operator-1", identity.RoleOperator)
	opToken := mustIssueToken(t, svc, operator.ID)

	deps, obsLister := nightControlTestDeps(svc, st)
	pub = &fakeAudioPublisher{}
	deps.AudioPublisher = pub
	deps.AudioSessions = st

	logs = &bytes.Buffer{}
	logger := slog.New(slog.NewTextHandler(logs, nil))

	api = New(deps, Options{Clock: fixedClock(testNow), Logger: logger, NightReadinessMaxAge: time.Hour})

	mustPutShow(t, api, adminToken, "halloween-2026", `{"name":"halloween-2026"}`)
	mustPutShowAction(t, api, adminToken, "lighting-fade-out", validShowActionFPPBody)
	mustPutShowAction(t, api, adminToken, "thank-you-1", multiAnnouncementShowActionBody("announcement-1"))
	mustPutShowAction(t, api, adminToken, "thank-you-2", multiAnnouncementShowActionBody("announcement-2"))
	mustPutShowAction(t, api, adminToken, "thank-you-3", multiAnnouncementShowActionBody("announcement-3"))
	mustCreateNightSessionAsset(t, st, "halloween-2026", "resting-loop", "player-01")
	mustPutNightSession(t, api, adminToken, "halloween-main", multiAnnouncementNightSessionBody)
	mustActivateNightSession(t, api, adminToken, "halloween-main")

	return api, opToken, obsLister, pub, logs
}

// mutation target: nightAnnouncementResetAtPrepareSiteBudget's own check
// in nightResetAnnouncementCueSessionOnce ("if
// !time.Now().Before(budgetDeadline)"). Removing that check (or the
// budget deadline computation in nightResetAnnouncementSessionsAtPrepareSite)
// makes every one of the three distinct sessions dispatch in full,
// regardless of how slow each one is - which is exactly the unbounded
// cost this test proves is gone. nightAnnouncementResetAtPrepareSiteBudget
// is driven down to 5ms (this package's own established test-only-override
// convention for a dispatch-timing const/var - see
// renderCommandConfirmDeadline in renderdispatch_test.go and
// nightInterlockAggregateDispatchBudget in
// nightinterlock_integration_test.go) and each simulated dispatch is made
// to cost 50ms of REAL wall-clock time via onAwaitResponse, mimicking an
// audio node that never answers within audioCommandConfirmDeadline. Only
// the first of the three distinct sessions should fit inside the 5ms
// budget; the other two must be skipped before ever being dispatched, so
// the whole prepare-site call finishes in a small multiple of one 50ms
// dispatch, not three.
func TestNightPrepareSite_AnnouncementResetBudget_BoundedRegardlessOfSessionCount(t *testing.T) {
	origBudget := nightAnnouncementResetAtPrepareSiteBudget
	nightAnnouncementResetAtPrepareSiteBudget = 5 * time.Millisecond
	t.Cleanup(func() { nightAnnouncementResetAtPrepareSiteBudget = origBudget })

	api, token, obs, pub, logs := setupPrepareSiteMultiAnnouncementFixture(t)
	setHealthyFPPReachable(obs, testNow)
	pub.resultsByAction = announcementNodeResults("announcement-1")
	pub.onAwaitResponse = func() { time.Sleep(50 * time.Millisecond) }

	start := time.Now()
	prep := mustNightCommand(t, api, token, "prepare-site")
	elapsed := time.Since(start)

	if prep.Command.Outcome != "applied" {
		t.Fatalf("prepare-site outcome = %q, want applied even though the announcement reset budget was exhausted", prep.Command.Outcome)
	}
	if prep.Session.State != nightStatePreparing {
		t.Fatalf("session state = %q, want %q; the night must still start when the reset budget runs out", prep.Session.State, nightStatePreparing)
	}

	// Not a boundedness check (the dispatch count and skipped-budget log
	// below already prove that); this only catches a genuine hang.
	if elapsed >= 10*time.Second {
		t.Fatalf("prepare-site took %s, want under 10s: the announcement reset pass appears to be hanging", elapsed)
	}

	var clears []dispatchedAudioCommand
	for _, d := range pub.dispatched {
		if d.Action == "audio.session.clear" {
			clears = append(clears, d)
		}
	}
	if len(clears) != 1 {
		t.Fatalf("dispatched %d audio.session.clear command(s), want exactly 1 (budget exhausted after the first): %v", len(clears), pub.dispatched)
	}

	if !strings.Contains(logs.String(), "budget exhausted") || !strings.Contains(logs.String(), "skipped=2") {
		t.Fatalf("no warning naming 2 skipped sessions and the exhausted budget was logged: %s", logs.String())
	}
}
