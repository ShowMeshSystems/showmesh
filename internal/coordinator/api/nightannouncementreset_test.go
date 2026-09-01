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

	persisted, err := st.GetAudioSession(context.Background(), "announcement-1")
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
