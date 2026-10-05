package api

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	v1 "github.com/showmeshsystems/showmesh/internal/coordinator/api/v1"
	"github.com/showmeshsystems/showmesh/internal/coordinator/config"
	"github.com/showmeshsystems/showmesh/internal/coordinator/currentrun"
	"github.com/showmeshsystems/showmesh/internal/coordinator/fallbackhold"
	"github.com/showmeshsystems/showmesh/internal/coordinator/identity"
	"github.com/showmeshsystems/showmesh/internal/coordinator/store"
)

const fallbackStateTestBootID = "aaaaaaaa-aaaa-4aaa-8aaa-aaaaaaaaaaaa"

// fallbackStateAPI is executorKeyAPI with the hold service wired.
func fallbackStateAPI(t *testing.T) (*API, *fppCommandTestSetup, *fallbackhold.Service, string) {
	t.Helper()
	setup := newFPPCommandTestSetup(t, fixedClock(testNow))
	holds := fallbackhold.NewService(setup.st, testNow.Add(-time.Hour), testLogger())
	deps := setup.deps()
	deps.FallbackPrograms = setup.st
	deps.FallbackHolds = holds
	api := New(deps, Options{Clock: fixedClock(testNow), Logger: testLogger()})
	if _, _, err := setup.st.RecordFPPInstanceUUIDObservation(t.Context(), "bench-fpp", executorKeyTestInstanceUUID, testNow); err != nil {
		t.Fatalf("record instance uuid: %v", err)
	}
	plugin, err := setup.svc.CreatePrincipal(t.Context(), fppPairingPrincipalPrefix+"bench-fpp", identity.KindMachine, identity.RoleScheduler, "")
	if err != nil {
		t.Fatalf("create plugin principal: %v", err)
	}
	return api, setup, holds, mustIssueToken(t, setup.svc, plugin.ID)
}

func sendFallbackState(t *testing.T, api *API, method, instanceUUID, token, body string) (*http.Response, []byte) {
	t.Helper()
	req := httptest.NewRequest(method, "/api/v1/fallback-programs/"+instanceUUID+"/fallback-state", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	return doRawRequest(t, api.Handler, req)
}

const (
	normalStateReport   = `{"schemaVersion":1,"bootId":"` + fallbackStateTestBootID + `","sequence":1,"state":"normal","since":"2026-10-05T20:00:00Z"}`
	fallbackStateReport = `{"schemaVersion":1,"bootId":"` + fallbackStateTestBootID + `","sequence":2,"state":"fallback","since":"2026-10-05T20:05:00Z",` +
		`"playlistName":"Main Show","packageId":"pkg-1","packageRevision":"rev-1","cutoffAt":"2026-10-06T08:00:00Z"}`
)

func TestFallbackStateReportIsStoredAndAnsweredAndAuditedOnlyOnAChange(t *testing.T) {
	api, setup, _, token := fallbackStateAPI(t)

	resp, raw := sendFallbackState(t, api, http.MethodPut, executorKeyTestInstanceUUID, token, normalStateReport)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("first report: status %d, body %s", resp.StatusCode, raw)
	}
	assertMatchesSchema(t, newOpenAPICompiler(t), "FallbackStateReportResponse", raw)
	var out v1.FallbackStateReportResponse
	if err := json.Unmarshal(raw, &out); err != nil || !out.Recorded || out.State != "normal" || out.FPPInstanceUUID != executorKeyTestInstanceUUID {
		t.Fatalf("first report answer = %+v err %v, want recorded normal for this player", out, err)
	}

	// A member this coordinator does not know is ignored, not refused.
	withExtra := strings.Replace(fallbackStateReport, `"sequence":2`, `"sequence":2,"somethingNewer":true`, 1)
	if resp, raw = sendFallbackState(t, api, http.MethodPut, executorKeyTestInstanceUUID, token, withExtra); resp.StatusCode != http.StatusOK {
		t.Fatalf("report with an unknown member: status %d, body %s", resp.StatusCode, raw)
	}
	rec, err := setup.st.GetFallbackPlayerState(t.Context(), executorKeyTestInstanceUUID)
	if err != nil || rec.State != "fallback" || rec.PlaylistName != "Main Show" || rec.CutoffAt != "2026-10-06T08:00:00Z" || !rec.ReceivedAt.Equal(testNow) {
		t.Fatalf("stored report = %+v err %v", rec, err)
	}

	// The 10 second repeat of the same state, and a late copy of an older
	// report, change nothing and write no audit entry.
	repeat := strings.Replace(fallbackStateReport, `"sequence":2`, `"sequence":3`, 1)
	if resp, raw = sendFallbackState(t, api, http.MethodPut, executorKeyTestInstanceUUID, token, repeat); resp.StatusCode != http.StatusOK {
		t.Fatalf("repeat report: status %d, body %s", resp.StatusCode, raw)
	}
	resp, raw = sendFallbackState(t, api, http.MethodPut, executorKeyTestInstanceUUID, token, normalStateReport)
	if err := json.Unmarshal(raw, &out); resp.StatusCode != http.StatusOK || err != nil || out.Recorded || out.State != "fallback" {
		t.Fatalf("late older report: status %d answer %+v err %v, want 200, not recorded, fallback kept", resp.StatusCode, out, err)
	}
	if got := countAuditActions(t, setup, auditActionFallbackPlayerStateReport); got != 2 {
		t.Fatalf("%d audit entries, want 2: the first report and the change to fallback", got)
	}
}

func TestFallbackStateReportRefusesEveryCallerButThePairedPlugin(t *testing.T) {
	api, setup, _, _ := fallbackStateAPI(t)
	admin := mustCreatePrincipal(t, setup.svc, "admin-1", identity.RoleAdmin)
	otherPlugin, err := setup.svc.CreatePrincipal(t.Context(), fppPairingPrincipalPrefix+"other-fpp", identity.KindMachine, identity.RoleScheduler, "")
	if err != nil {
		t.Fatalf("create second plugin principal: %v", err)
	}
	if _, _, err := setup.st.RecordFPPInstanceUUIDObservation(t.Context(), "other-fpp", "33333333-3333-4333-8333-333333333333", testNow); err != nil {
		t.Fatalf("record second instance uuid: %v", err)
	}
	unseenPlugin, err := setup.svc.CreatePrincipal(t.Context(), fppPairingPrincipalPrefix+"unseen-fpp", identity.KindMachine, identity.RoleScheduler, "")
	if err != nil {
		t.Fatalf("create third plugin principal: %v", err)
	}
	for _, tc := range []struct {
		name   string
		token  string
		status int
	}{
		{"no credential", "", http.StatusUnauthorized},
		{"an administrator", mustIssueToken(t, setup.svc, admin.ID), http.StatusForbidden},
		{"a plugin paired as another player", mustIssueToken(t, setup.svc, otherPlugin.ID), http.StatusForbidden},
		{"a plugin whose player has reported no identity", mustIssueToken(t, setup.svc, unseenPlugin.ID), http.StatusConflict},
	} {
		resp, raw := sendFallbackState(t, api, http.MethodPut, executorKeyTestInstanceUUID, tc.token, fallbackStateReport)
		if resp.StatusCode != tc.status {
			t.Errorf("%s: status = %d, want %d; body: %s", tc.name, resp.StatusCode, tc.status, raw)
		}
	}
	if _, err := setup.st.GetFallbackPlayerState(t.Context(), executorKeyTestInstanceUUID); !errors.Is(err, store.ErrFallbackPlayerStateNotFound) {
		t.Fatalf("a refused caller stored a state: %v", err)
	}
}

func TestFallbackStateReportRefusesABodyThatIsNotTheContractsShape(t *testing.T) {
	api, setup, _, token := fallbackStateAPI(t)
	edit := func(body, old, replacement string) string {
		if !strings.Contains(body, old) {
			t.Fatalf("test body has no %q to replace", old)
		}
		return strings.Replace(body, old, replacement, 1)
	}
	for name, body := range map[string]string{
		"not json":                       `state`,
		"schema version 2":               edit(normalStateReport, `"schemaVersion":1`, `"schemaVersion":2`),
		"no boot id":                     edit(normalStateReport, `"bootId":"`+fallbackStateTestBootID+`",`, ``),
		"boot id in upper case":          edit(normalStateReport, fallbackStateTestBootID, strings.ToUpper(fallbackStateTestBootID)),
		"sequence zero":                  edit(normalStateReport, `"sequence":1`, `"sequence":0`),
		"an unknown state":               edit(normalStateReport, `"state":"normal"`, `"state":"takeover"`),
		"since is not a time":            edit(normalStateReport, `2026-10-05T20:00:00Z`, `yesterday`),
		"normal with a playlist":         edit(normalStateReport, `"state":"normal"`, `"state":"normal","playlistName":"Main Show"`),
		"fallback without a playlist":    edit(fallbackStateReport, `"playlistName":"Main Show",`, ``),
		"fallback without a package":     edit(fallbackStateReport, `"packageId":"pkg-1",`, ``),
		"fallback without a cutoff":      edit(fallbackStateReport, `,"cutoffAt":"2026-10-06T08:00:00Z"`, ``),
		"fallback with a cutoff in text": edit(fallbackStateReport, `2026-10-06T08:00:00Z`, `tomorrow morning`),
		"a body over 4 KiB":              edit(normalStateReport, `"state":"normal"`, `"state":"normal","padding":"`+strings.Repeat("x", 5000)+`"`),
	} {
		resp, raw := sendFallbackState(t, api, http.MethodPut, executorKeyTestInstanceUUID, token, body)
		if resp.StatusCode != http.StatusBadRequest {
			t.Errorf("%s: status = %d, want 400; body: %s", name, resp.StatusCode, raw)
		}
	}
	if _, err := setup.st.GetFallbackPlayerState(t.Context(), executorKeyTestInstanceUUID); !errors.Is(err, store.ErrFallbackPlayerStateNotFound) {
		t.Fatalf("a refused body stored a state: %v", err)
	}
}

func TestFallbackStateClearNeedsAnOperatorAndForgetsTheReport(t *testing.T) {
	api, setup, holds, token := fallbackStateAPI(t)
	if resp, raw := sendFallbackState(t, api, http.MethodPut, executorKeyTestInstanceUUID, token, strings.Replace(fallbackStateReport, `"sequence":2`, `"sequence":1`, 1)); resp.StatusCode != http.StatusOK {
		t.Fatalf("report: status %d, body %s", resp.StatusCode, raw)
	}
	if v, err := holds.Evaluate(t.Context(), executorKeyTestInstanceUUID, testNow); err != nil || !v.Held {
		t.Fatalf("before the clear: held %v err %v, want held", v.Held, err)
	}

	viewer := mustCreatePrincipal(t, setup.svc, "viewer-1", identity.RoleViewer)
	for name, tc := range map[string]struct {
		token  string
		status int
	}{
		"no credential": {"", http.StatusUnauthorized},
		"a viewer":      {mustIssueToken(t, setup.svc, viewer.ID), http.StatusForbidden},
		"the plugin":    {token, http.StatusForbidden},
	} {
		if resp, raw := sendFallbackState(t, api, http.MethodDelete, executorKeyTestInstanceUUID, tc.token, ""); resp.StatusCode != tc.status {
			t.Errorf("%s: status = %d, want %d; body: %s", name, resp.StatusCode, tc.status, raw)
		}
	}
	if v, _ := holds.Evaluate(t.Context(), executorKeyTestInstanceUUID, testNow); !v.Held {
		t.Fatal("a refused clear ended the hold")
	}

	operator := mustCreatePrincipal(t, setup.svc, "operator-1", identity.RoleOperator)
	operatorToken := mustIssueToken(t, setup.svc, operator.ID)
	if resp, raw := sendFallbackState(t, api, http.MethodDelete, executorKeyTestInstanceUUID, operatorToken, ""); resp.StatusCode != http.StatusNoContent {
		t.Fatalf("operator clear: status %d, body %s", resp.StatusCode, raw)
	}
	if v, err := holds.Evaluate(t.Context(), executorKeyTestInstanceUUID, testNow); err != nil || v.Held || v.Reported {
		t.Fatalf("after the clear: %+v err %v, want no report and no hold", v, err)
	}
	if got := countAuditActions(t, setup, auditActionFallbackPlayerStateClear); got != 1 {
		t.Fatalf("%d audit entries for the clear, want 1", got)
	}
	// Clearing a player with nothing stored succeeds too.
	if resp, raw := sendFallbackState(t, api, http.MethodDelete, executorKeyTestInstanceUUID, operatorToken, ""); resp.StatusCode != http.StatusNoContent {
		t.Fatalf("second clear: status %d, body %s", resp.StatusCode, raw)
	}

	// A plugin that still reports fallback is held again by its next report.
	next := strings.Replace(fallbackStateReport, `"sequence":2`, `"sequence":9`, 1)
	if resp, raw := sendFallbackState(t, api, http.MethodPut, executorKeyTestInstanceUUID, token, next); resp.StatusCode != http.StatusOK {
		t.Fatalf("report after the clear: status %d, body %s", resp.StatusCode, raw)
	}
	if v, _ := holds.Evaluate(t.Context(), executorKeyTestInstanceUUID, testNow); !v.Held {
		t.Fatal("a plugin that reports fallback after a clear is not held")
	}
}

// nightFallbackHoldFixture is a live night session whose background audio
// the next tick would stop, on an FPP player whose plugin can report.
func nightFallbackHoldFixture(t *testing.T) (*handlers, *store.Store, *fakeAudioPublisher, *fallbackhold.Service, store.NightSessionRecord) {
	t.Helper()
	h, st, pub, _ := nightBackgroundAudioTestHandlers(t)
	putBackgroundAudioAsset(t, st, "halloween", "bg-1", "node-a", "asset-1")
	putBackgroundAudioAsset(t, st, "halloween", "bg-2", "node-a", "asset-2")
	ba := twoItemBackgroundAudioConfig("node-a", config.NightSessionBackgroundRepeatPlaylist, config.NightSessionBackgroundResumeRestart, config.NightSessionItemTransitionSequential)
	rec := mustCreateRestingSessionWithBackgroundAudio(t, st, "sess-1", "node-a", ba, nightStateRestingIntershow)
	playThroughApplyGainStart(t, h, pub, rec)

	rec.State = nightStateLive
	if err := st.UpdateNightSession(context.Background(), rec, testNow); err != nil {
		t.Fatalf("UpdateNightSession: %v", err)
	}
	pub.result = confirmedResultForAction("stop", nightBackgroundAudioSessionID(rec), "stopped")

	holds := fallbackhold.NewService(st, testNow.Add(-time.Hour), testLogger())
	h.deps.FallbackHolds = holds
	h.deps.FPP = &fakeFPPLister{views: []FPPInstanceView{{
		InstanceID: "fpp-main", InstanceUUID: &store.FPPInstanceUUIDRecord{UUID: executorKeyTestInstanceUUID},
	}}}
	return h, st, pub, holds, rec
}

func recordHoldReport(t *testing.T, holds *fallbackhold.Service, state string, sequence int64, at time.Time) {
	t.Helper()
	r := fallbackhold.Report{FPPInstanceUUID: executorKeyTestInstanceUUID, BootID: fallbackStateTestBootID, Sequence: sequence, State: state, Since: at}
	if state != fallbackhold.StateNormal {
		r.PlaylistName, r.PackageID, r.PackageRevision, r.CutoffAt = "halloween-show", "pkg-1", "rev-1", "2026-10-06T08:00:00Z"
	}
	if _, err := holds.Record(t.Context(), r, at); err != nil {
		t.Fatalf("record %s report: %v", state, err)
	}
}

func TestNightTickDoesNotAdvanceASessionWhosePlayerRunsFromItsFallbackProgram(t *testing.T) {
	h, _, pub, holds, _ := nightFallbackHoldFixture(t)
	recordHoldReport(t, holds, fallbackhold.StateFallback, 1, testNow)
	before := pub.count()

	h.nightTick(context.Background(), testNow)
	if pub.count() != before {
		t.Fatalf("the night loop sent %d commands (last %q) while the player's plugin ran the show, want none", pub.count()-before, pub.lastAction)
	}

	// The hand-back: the very next tick does what it had been keeping back.
	recordHoldReport(t, holds, fallbackhold.StateNormal, 2, testNow)
	h.nightTick(context.Background(), testNow)
	if pub.lastAction != "audio.session.stop" {
		t.Fatalf("after the hand-back the night loop sent %q, want audio.session.stop", pub.lastAction)
	}
}

// The case a dead or hung plugin must not turn into: a night that cannot
// advance. Its last report says normal and is old; nothing is held.
func TestNightTickAdvancesWhenAReportingPluginHasGoneQuiet(t *testing.T) {
	h, _, pub, holds, _ := nightFallbackHoldFixture(t)
	recordHoldReport(t, holds, fallbackhold.StateNormal, 1, testNow.Add(-10*time.Minute))

	h.nightTick(context.Background(), testNow)
	if pub.lastAction != "audio.session.stop" {
		t.Fatalf("with a plugin that stopped reporting the night loop sent %q, want audio.session.stop", pub.lastAction)
	}
}

func TestNightTickAdvancesWhenThePlayersPluginHasNeverReported(t *testing.T) {
	h, _, pub, _, _ := nightFallbackHoldFixture(t)
	h.nightTick(context.Background(), testNow)
	if pub.lastAction != "audio.session.stop" {
		t.Fatalf("with a plugin that never reported the night loop sent %q, want audio.session.stop", pub.lastAction)
	}
}

// A session that is fading out is how the show is stopped. A hold must
// not reach it.
func TestNightFallbackHoldDoesNotApplyToASessionThatIsFadingOutOrStopped(t *testing.T) {
	for _, state := range []string{nightStateFadingOut, nightStateStopped} {
		t.Run(state, func(t *testing.T) {
			h, st, pub, holds, rec := nightFallbackHoldFixture(t)
			recordHoldReport(t, holds, fallbackhold.StateFallback, 1, testNow)
			if !h.nightHeldForFallback(context.Background(), testNow, rec) {
				t.Fatal("the fixture's player is not held, so this test proves nothing")
			}
			rec.State = state
			if err := st.UpdateNightSession(context.Background(), rec, testNow); err != nil {
				t.Fatalf("UpdateNightSession: %v", err)
			}
			before := pub.count()
			h.nightTick(context.Background(), testNow)
			if pub.count() == before {
				t.Fatalf("a %s session sent nothing while its player was held; the hold must not reach it", state)
			}
		})
	}
}

func TestCueCatalogAutoDeployWaitsWhileAPlayerRunsFromItsFallbackProgram(t *testing.T) {
	setup := newAudioDispatchTestSetup(t, fixedClock(testNow))
	holds := fallbackhold.NewService(setup.st, testNow.Add(-time.Hour), testLogger())
	deps := setup.deps()
	deps.FallbackHolds = holds
	runs := &countingCurrentRuns{}
	deps.CurrentRuns = runs
	h := &handlers{deps: deps.withDefaults(), clock: fixedClock(testNow), logger: testLogger()}

	recordHoldReport(t, holds, fallbackhold.StateResting, 1, testNow)
	h.AutoDeployCueCatalog(context.Background(), testNow, "node-a")
	if runs.calls != 0 || setup.pub.count() != 0 {
		t.Fatalf("with a player held the deploy went on to read playback (%d reads) and sent %d commands, want neither", runs.calls, setup.pub.count())
	}

	recordHoldReport(t, holds, fallbackhold.StateNormal, 2, testNow)
	h.AutoDeployCueCatalog(context.Background(), testNow, "node-a")
	if runs.calls != 1 {
		t.Fatalf("after the hand-back the deploy read playback %d times, want once", runs.calls)
	}
}

// countingCurrentRuns counts playback reads and reports a read failure, so
// a deploy that gets as far as reading playback is held there.
type countingCurrentRuns struct{ calls int }

func (c *countingCurrentRuns) Snapshot(context.Context, time.Time) (currentrun.Snapshot, error) {
	c.calls++
	return currentrun.Snapshot{}, errors.New("no playback evidence in this test")
}
