package api

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/showmeshsystems/showmesh/internal/coordinator/config"
	"github.com/showmeshsystems/showmesh/internal/coordinator/store"
	"github.com/showmeshsystems/showmesh/pkg/observation"
)

func (r *resumeHarness) post(path, body string) (int, string) {
	r.t.Helper()
	auth := map[string]string{"Authorization": "Bearer " + r.token}
	resp, respBody := doRawRequest(r.t, r.api.Handler, newJSONRequest(r.t, http.MethodPost, path, body, auth))
	return resp.StatusCode, string(respBody)
}

func (r *resumeHarness) levelOneStop() {
	r.t.Helper()
	if code, body := r.post("/api/v1/emergency-stop/stop", `{"idempotencyKey":"stop-1"}`); code != http.StatusOK {
		r.t.Fatalf("level 1 stop: status = %d, want 200; body: %s", code, body)
	}
}

func (r *resumeHarness) startPlaylistArgs() [][]string {
	cmds, args := r.fppCommands()
	var out [][]string
	for i, c := range cmds {
		if c == "Start Playlist" {
			out = append(out, args[i])
		}
	}
	return out
}

func (r *resumeHarness) createLiveSession() {
	r.t.Helper()
	liveAt := r.now.Add(-10 * time.Minute)
	r.createSession(store.NightSessionRecord{State: nightStateLive, StateEnteredAt: liveAt, Cycle: 3, ShowCommitted: true, ContentAnchorJSON: showAnchorAt(liveAt)})
	if err := r.st.OpenNightCycleOutcome(context.Background(), "sess-1", 3, liveAt); err != nil {
		r.t.Fatalf("open cycle outcome: %v", err)
	}
}

// tickIdle runs n loop ticks with the player reporting idle and nothing
// playing, the state a level 1 stop leaves it in.
func (r *resumeHarness) tickIdle(n int) {
	for i := 0; i < n; i++ {
		r.now = r.now.Add(time.Second)
		r.obs.set([]observation.Observation{
			statusObservation("player-01", fppStatusValueIdle, r.now),
			playlistNameObservation("player-01", "", r.now),
		})
		r.h.nightTick(context.Background(), r.now)
	}
}

func TestLevelOneStopHoldsAnActiveNightAndTheLoopStartsNothing(t *testing.T) {
	for _, state := range []string{nightStateLive, nightStateRestingIntershow, nightStatePreshow} {
		t.Run(state, func(t *testing.T) {
			r := newResumeHarness(t)
			if state == nightStateLive {
				r.createLiveSession()
			} else {
				enteredAt := r.now.Add(-2 * time.Minute)
				r.createSession(store.NightSessionRecord{State: state, StateEnteredAt: enteredAt, Cycle: 2,
					ContentAnchorJSON: restingAnchorAt(nightAnchorPurposeRestingRepeat, enteredAt)})
			}

			r.levelOneStop()

			got := mustGetCurrentSession(t, r.st)
			if got.StopHold == nil || got.StopHold.Principal != "admin-1" || !got.StopHold.At.Equal(r.now) {
				t.Fatalf("stop hold = %+v, want set by admin-1 at the stop", got.StopHold)
			}
			if got.State != state {
				t.Fatalf("state = %q, want %q: the hold never moves the session", got.State, state)
			}
			held, err := r.h.nightStopHoldActive(context.Background())
			if err != nil || !held {
				t.Fatalf("nightStopHoldActive = %v (err %v), want true so the cue activation loop refuses output", held, err)
			}

			r.tickIdle(5)
			if starts := r.startPlaylistArgs(); len(starts) != 0 {
				t.Fatalf("Start Playlist sent while held: %v", starts)
			}
			if got := mustGetCurrentSession(t, r.st); got.State != state || got.StopHold == nil {
				t.Fatalf("after held ticks: state %q hold %+v, want %q still held", got.State, got.StopHold, state)
			}
		})
	}
}

func TestLevelOneStopClosesTheLiveCycleAsStopped(t *testing.T) {
	r := newResumeHarness(t)
	r.createLiveSession()
	r.levelOneStop()
	outcomes, err := r.st.ListNightCycleOutcomes(context.Background(), "sess-1")
	if err != nil || len(outcomes) != 1 || outcomes[0].Outcome != store.NightCycleOutcomeStopped {
		t.Fatalf("cycle outcomes = %+v (err %v), want cycle 3 closed stopped", outcomes, err)
	}
}

func TestLevelOneStopWithNoSessionIsUnchanged(t *testing.T) {
	r := newResumeHarness(t)
	code, body := r.post("/api/v1/emergency-stop/stop", `{"idempotencyKey":"stop-1"}`)
	if code != http.StatusOK || !strings.Contains(body, `"nightSession":{"present":false}`) {
		t.Fatalf("level 1 with no session: %d %s, want 200 with nightSession present=false", code, body)
	}
	if _, ok, _ := r.st.GetCurrentNightSession(context.Background()); ok {
		t.Fatal("level 1 with no session created one")
	}
}

func TestLevelOneStopOnAStoppedSessionSetsNoHold(t *testing.T) {
	r := newResumeHarness(t)
	r.createSession(store.NightSessionRecord{State: nightStateStopped, StateEnteredAt: r.now})
	r.levelOneStop()
	if got := mustGetCurrentSession(t, r.st); got.StopHold != nil {
		t.Fatalf("stop hold on a stopped session = %+v, want none", got.StopHold)
	}
}

func TestResumeShowClearsTheHoldAndStartsTheShowFromEntryOne(t *testing.T) {
	r := newResumeHarness(t)
	r.createLiveSession()
	r.levelOneStop()
	r.tickIdle(3)

	code, body := r.post("/api/v1/night/commands/resume-show", ``)
	if code != http.StatusAccepted {
		t.Fatalf("resume-show: %d %s, want 202", code, body)
	}
	got := mustGetCurrentSession(t, r.st)
	if got.StopHold != nil || got.State != nightStateTransitionToShow || got.Cycle != 4 || got.ContentAnchorJSON != "" {
		t.Fatalf("after resume-show: hold %+v state %q cycle %d anchor %q; want no hold, transition-to-show, cycle 4, no anchor",
			got.StopHold, got.State, got.Cycle, got.ContentAnchorJSON)
	}

	r.tickIdle(1)
	starts := r.startPlaylistArgs()
	if len(starts) != 1 || len(starts[0]) == 0 || starts[0][0] != "halloween-show" {
		t.Fatalf("Start Playlist after resume = %v, want exactly one for halloween-show", starts)
	}
	if got := mustGetCurrentSession(t, r.st); got.State != nightStateLive || got.Cycle != 4 {
		t.Fatalf("after the launch tick: %q cycle %d, want live cycle 4", got.State, got.Cycle)
	}
	entries, err := r.st.ListAuditEntries(context.Background(), 0, 1000)
	if err != nil {
		t.Fatalf("list audit: %v", err)
	}
	found := false
	for _, e := range entries {
		found = found || e.Action == "show.night.resume_show"
	}
	if !found {
		t.Fatal("no show.night.resume_show audit entry")
	}
}

func TestResumeShowFromRestingGoesStraightToTheShow(t *testing.T) {
	r := newResumeHarness(t)
	enteredAt := r.now.Add(-2 * time.Minute)
	r.createSession(store.NightSessionRecord{State: nightStateRestingIntershow, StateEnteredAt: enteredAt, Cycle: 2,
		ContentAnchorJSON: restingAnchorAt(nightAnchorPurposeRestingOneShot, enteredAt)})
	r.levelOneStop()
	if code, body := r.post("/api/v1/night/commands/resume-show", ``); code != http.StatusAccepted {
		t.Fatalf("resume-show: %d %s", code, body)
	}
	r.tickIdle(1)
	starts := r.startPlaylistArgs()
	if len(starts) != 1 || starts[0][0] != "halloween-show" {
		t.Fatalf("Start Playlist after resume = %v, want the show playlist, never the resting one", starts)
	}
}

func TestResumeShowWithoutAHoldIsRefused(t *testing.T) {
	r := newResumeHarness(t)
	r.createLiveSession()
	code, body := r.post("/api/v1/night/commands/resume-show", ``)
	if code != http.StatusConflict || !strings.Contains(body, nightResumeShowNoHoldDetail) {
		t.Fatalf("resume-show with no hold: %d %s, want 409 naming %q", code, body, nightResumeShowNoHoldDetail)
	}
	if got := mustGetCurrentSession(t, r.st); got.State != nightStateLive || got.Cycle != 3 {
		t.Fatalf("a refused resume-show changed the session: %q cycle %d", got.State, got.Cycle)
	}
}

func TestHardStopWhileHeldEndsTheSession(t *testing.T) {
	r := newResumeHarness(t)
	r.createLiveSession()
	r.levelOneStop()
	code, body := r.post("/api/v1/emergency-stop/hard-stop/arm", `{"idempotencyKey":"arm-1"}`)
	if code != http.StatusOK {
		t.Fatalf("arm: %d %s", code, body)
	}
	token := decodeMap(t, []byte(body))["armToken"].(string)
	if code, body := r.post("/api/v1/emergency-stop/hard-stop/fire", `{"idempotencyKey":"fire-1","armToken":"`+token+`"}`); code != http.StatusOK {
		t.Fatalf("fire: %d %s", code, body)
	}
	got := mustGetCurrentSession(t, r.st)
	if got.State != nightStateStopped || got.StopHold != nil {
		t.Fatalf("after hard stop: %q hold %+v, want stopped with no hold", got.State, got.StopHold)
	}
}

func TestPowerDownWhileHeldFadesOutAndDropsTheHold(t *testing.T) {
	r := newResumeHarness(t)
	enteredAt := r.now.Add(-2 * time.Minute)
	r.createSession(store.NightSessionRecord{State: nightStateRestingIntershow, StateEnteredAt: enteredAt, Cycle: 2})
	r.levelOneStop()
	if code, body := r.post("/api/v1/emergency-stop/stop-power-down", `{"idempotencyKey":"pd-1"}`); code != http.StatusOK {
		t.Fatalf("stop-power-down: %d %s", code, body)
	}
	got := mustGetCurrentSession(t, r.st)
	if got.State != nightStateFadingOut || got.StopHold != nil {
		t.Fatalf("after power-down: %q hold %+v, want fading-out with no hold", got.State, got.StopHold)
	}
}

func TestWeatherDelayWhileHeldWinsAndItsResumeClearsTheHold(t *testing.T) {
	r := newResumeHarness(t)
	r.createLiveSession()
	r.levelOneStop()
	setWeatherDelayActive(t, r.st)
	r.tickIdle(2)
	if starts := r.startPlaylistArgs(); len(starts) != 0 {
		t.Fatalf("Start Playlist during delay while held: %v", starts)
	}

	r.resume()

	got := mustGetCurrentSession(t, r.st)
	if got.StopHold != nil || got.State != nightStateTransitionToShow {
		t.Fatalf("after weather resume: hold %+v state %q, want no hold and the show transition", got.StopHold, got.State)
	}
	r.tickIdle(1)
	if starts := r.startPlaylistArgs(); len(starts) != 1 || starts[0][0] != "halloween-show" {
		t.Fatalf("Start Playlist after weather resume = %v, want the show once: one resume, not two", starts)
	}
}

func TestNightSessionReadShowsTheHold(t *testing.T) {
	r := newResumeHarness(t)
	r.createLiveSession()
	r.levelOneStop()
	auth := map[string]string{"Authorization": "Bearer " + r.token}
	resp, body := doRawRequest(t, r.api.Handler, newJSONRequest(t, http.MethodGet, "/api/v1/night/session", ``, auth))
	if resp.StatusCode != http.StatusOK || !strings.Contains(string(body), `"stopHold":{"reason":"`+nightStopHoldReason+`"`) {
		t.Fatalf("GET lifecycle: %d %s, want a stopHold", resp.StatusCode, body)
	}
}

func TestHeldTickStartsNoBackgroundBed(t *testing.T) {
	for _, state := range []string{nightStatePreshow, nightStateEndOfNightResting} {
		t.Run(state, func(t *testing.T) {
			h, st, pub, _ := nightBackgroundAudioTestHandlers(t)
			putBackgroundAudioAsset(t, st, "halloween", "bg-1", "node-a", "asset-1")
			putBackgroundAudioAsset(t, st, "halloween", "bg-2", "node-a", "asset-2")
			ba := multiNodeBedConfig("node-a", "node-a", "node-b")
			rec := mustCreateRestingSessionWithBackgroundAudio(t, st, "sess-1", "node-a", ba, state)
			rec.StopHold = &store.NightSessionStopHold{Reason: nightStopHoldReason, At: testNow}
			if err := st.UpdateNightSession(context.Background(), rec, testNow); err != nil {
				t.Fatalf("set hold: %v", err)
			}

			h.nightTick(context.Background(), testNow)
			if n := countDispatchedAction(pub, "audio.session.apply"); n != 0 {
				t.Fatalf("audio.session.apply dispatched %d times while held, want 0", n)
			}

			rec.StopHold = nil
			if err := st.UpdateNightSession(context.Background(), rec, testNow); err != nil {
				t.Fatalf("clear hold: %v", err)
			}
			h.nightAdvanceBackgroundAudio(context.Background(), testNow, mustGetCurrentSession(t, st))
			if n := countDispatchedAction(pub, "audio.session.apply"); n == 0 {
				t.Fatal("control: this bed configuration starts no bed even unheld, so the held assertion proves nothing")
			}
		})
	}
}

// heldTickSession builds a session in state that, unheld, starts a
// playlist on its next tick. End-of-night resting starts only the bed,
// which TestHeldTickStartsNoBackgroundBed covers.
func (r *resumeHarness) heldTickSession(state string) {
	r.t.Helper()
	switch state {
	case nightStateLive:
		r.createLiveSession()
	case nightStateTransitionToShow:
		lastTick := r.now
		r.createSession(store.NightSessionRecord{State: state, StateEnteredAt: r.now, Cycle: 4,
			BoundaryJSON: encodeNightBoundary(nightBoundary{State: nightBoundaryStateArmed, ExpectedAt: &lastTick, LastTickAt: &lastTick})})
	case nightStateTransitionToResting:
		r.createSession(store.NightSessionRecord{State: state, StateEnteredAt: r.now.Add(-time.Second), Cycle: 3, FinalShowRequested: true})
	default:
		enteredAt := r.now.Add(-2 * time.Minute)
		r.createSession(store.NightSessionRecord{State: state, StateEnteredAt: enteredAt, Cycle: 2,
			ContentAnchorJSON: restingAnchorAt(nightAnchorPurposeRestingRepeat, enteredAt)})
	}
}

func TestHeldTickStartsNothingInEveryPlayingState(t *testing.T) {
	for _, state := range []string{nightStateTransitionToShow, nightStateTransitionToResting} {
		t.Run(state, func(t *testing.T) {
			control := newResumeHarness(t)
			control.heldTickSession(state)
			control.tickIdle(3)
			if len(control.startPlaylistArgs()) == 0 {
				t.Fatalf("control: an unheld %s session starts no playlist, so the held assertion proves nothing", state)
			}

			r := newResumeHarness(t)
			r.heldTickSession(state)
			r.levelOneStop()
			r.tickIdle(5)
			if starts := r.startPlaylistArgs(); len(starts) != 0 {
				t.Fatalf("Start Playlist sent while held in %s: %v", state, starts)
			}
			if got := mustGetCurrentSession(t, r.st); got.State != state || got.StopHold == nil {
				t.Fatalf("after held ticks: state %q hold %+v, want %q still held", got.State, got.StopHold, state)
			}
		})
	}
}

func TestAHoldSetUnderATickStopsItsPlaylistStart(t *testing.T) {
	control := newResumeHarness(t)
	control.heldTickSession(nightStateTransitionToShow)
	control.obs.set([]observation.Observation{statusObservation("player-01", fppStatusValueIdle, control.now)})
	control.h.nightAdvanceTransitionToShow(context.Background(), control.now, mustGetCurrentSession(t, control.st))
	if len(control.startPlaylistArgs()) != 1 {
		t.Fatalf("control: unheld transition-to-show started %v, want the show once", control.startPlaylistArgs())
	}

	r := newResumeHarness(t)
	r.heldTickSession(nightStateTransitionToShow)
	stale := mustGetCurrentSession(t, r.st)
	held := stale
	held.StopHold = &store.NightSessionStopHold{Reason: nightStopHoldReason, At: r.now}
	if err := r.st.UpdateNightSession(context.Background(), held, r.now); err != nil {
		t.Fatalf("set hold: %v", err)
	}
	r.obs.set([]observation.Observation{statusObservation("player-01", fppStatusValueIdle, r.now)})
	r.h.nightAdvanceTransitionToShow(context.Background(), r.now, stale)
	if starts := r.startPlaylistArgs(); len(starts) != 0 {
		t.Fatalf("Start Playlist sent by a tick that read the session before the hold: %v", starts)
	}
}

func TestAHoldSetUnderATickStopsItsBed(t *testing.T) {
	h, st, pub, _ := nightBackgroundAudioTestHandlers(t)
	putBackgroundAudioAsset(t, st, "halloween", "bg-1", "node-a", "asset-1")
	putBackgroundAudioAsset(t, st, "halloween", "bg-2", "node-a", "asset-2")
	ba := multiNodeBedConfig("node-a", "node-a", "node-b")
	stale := mustCreateRestingSessionWithBackgroundAudio(t, st, "sess-1", "node-a", ba, nightStatePreshow)
	held := stale
	held.StopHold = &store.NightSessionStopHold{Reason: nightStopHoldReason, At: testNow}
	if err := st.UpdateNightSession(context.Background(), held, testNow); err != nil {
		t.Fatalf("set hold: %v", err)
	}
	h.nightAdvanceBackgroundAudio(context.Background(), testNow, stale)
	if n := countDispatchedAction(pub, "audio.session.apply"); n != 0 {
		t.Fatalf("audio.session.apply dispatched %d times by a tick that read the session before the hold, want 0", n)
	}
}

func TestResumeShowInPreshowIsRefusedAndStartNightClearsTheHold(t *testing.T) {
	r := newResumeHarness(t)
	enteredAt := r.now.Add(-2 * time.Minute)
	r.createSession(store.NightSessionRecord{State: nightStatePreshow, StateEnteredAt: enteredAt,
		ContentAnchorJSON: restingAnchorAt(nightAnchorPurposeRestingRepeat, enteredAt)})
	r.levelOneStop()

	code, body := r.post("/api/v1/night/commands/resume-show", ``)
	if code != http.StatusConflict || !strings.Contains(body, nightResumeShowPreshowDetail) {
		t.Fatalf("resume-show in preshow: %d %s, want 409 naming %q", code, body, nightResumeShowPreshowDetail)
	}
	if got := mustGetCurrentSession(t, r.st); got.State != nightStatePreshow || got.StopHold == nil {
		t.Fatalf("a refused resume-show changed the session: %q hold %+v", got.State, got.StopHold)
	}

	if err := r.st.CreateNightReadiness(context.Background(), store.NightReadinessRecord{ID: "r1", SessionID: "sess-1", EpochID: "sess-1", CompletedAt: r.now, Outcome: "ready", ChecksJSON: "[]"}); err != nil {
		t.Fatalf("create readiness: %v", err)
	}
	if code, body := r.post("/api/v1/night/commands/start-night", ``); code != http.StatusAccepted {
		t.Fatalf("start-night while held: %d %s, want 202", code, body)
	}
	if got := mustGetCurrentSession(t, r.st); got.State != nightStateTransitionToShow || got.StopHold != nil {
		t.Fatalf("after start-night: %q hold %+v, want transition-to-show with no hold", got.State, got.StopHold)
	}
}

func TestLevelOneTwiceWhileHeldIsANoOp(t *testing.T) {
	r := newResumeHarness(t)
	r.createLiveSession()
	r.levelOneStop()
	first := mustGetCurrentSession(t, r.st)

	r.now = r.now.Add(time.Minute)
	code, body := r.post("/api/v1/emergency-stop/stop", `{"idempotencyKey":"stop-2"}`)
	if code != http.StatusOK || !strings.Contains(body, `"outcome":"`+nightOutcomeIdempotentNoOp+`"`) {
		t.Fatalf("second level 1: %d %s, want 200 with a no-op night outcome", code, body)
	}
	got := mustGetCurrentSession(t, r.st)
	if got.StopHold == nil || !got.StopHold.At.Equal(first.StopHold.At) || got.State != first.State || got.Cycle != first.Cycle {
		t.Fatalf("second level 1 changed the held session: %+v, want %+v", got, first)
	}
	outcomes, err := r.st.ListNightCycleOutcomes(context.Background(), "sess-1")
	if err != nil || len(outcomes) != 1 {
		t.Fatalf("cycle outcomes = %+v (err %v), want the one closed cycle", outcomes, err)
	}
}

type failingHoldTxStore struct{ NightSessionStore }

func (failingHoldTxStore) InTx(context.Context, func(context.Context, *store.Tx) error) error {
	return errors.New("database is locked")
}

func TestAFailingHoldWriteStillStopsEveryTarget(t *testing.T) {
	r := newResumeHarness(t)
	r.createLiveSession()
	r.h.deps.NightSessions = failingHoldTxStore{r.st}

	w := httptest.NewRecorder()
	r.h.handleEmergencyStop(w, httptest.NewRequest(http.MethodPost, "/api/v1/emergency-stop/stop", strings.NewReader(`{"idempotencyKey":"stop-1"}`)))
	if w.Code != http.StatusOK || !strings.Contains(w.Body.String(), "hold step failed") {
		t.Fatalf("level 1 with a failing hold write: %d %s, want 200 reporting the hold failure", w.Code, w.Body.String())
	}
	cmds, _ := r.fppCommands()
	stopped := false
	for _, c := range cmds {
		stopped = stopped || strings.HasPrefix(c, "Stop")
	}
	if !stopped {
		t.Fatalf("FPP commands = %v, want the player stopped despite the failed hold", cmds)
	}
	if got := mustGetCurrentSession(t, r.st); got.StopHold != nil {
		t.Fatalf("hold = %+v, want none after the failed write", got.StopHold)
	}
}

func TestAHoldSetUnderATickStopsItsEnterCues(t *testing.T) {
	cases := []struct {
		name, state, phase string
		first              bool
	}{
		{"enter-show first cue", nightStateTransitionToShow, nightPhaseEnterShow, true},
		{"enter-show later cue", nightStateTransitionToShow, nightPhaseEnterShow, false},
		{"enter-resting cue", nightStateTransitionToResting, nightPhaseEnterResting, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			for _, held := range []bool{false, true} {
				h, st := nightCueTestHandlers(t)
				dispatcher := h.deps.ResolumeActions.(*fakeResolumeActionDispatcher)
				dispatcher.results = map[string]ResolumeActionResult{
					config.ShowActionResolumeBlackout: {Outcome: ResolumeOutcomeConfirmed, Dispatched: true, Reason: "dark"},
				}
				putNightAction(t, st, "act-blackout", blackoutResolumeAction())
				stale := store.NightSessionRecord{ID: "sess-1", ConfigObjectID: "halloween-main", ConfigRevision: 1, State: tc.state, StateEnteredAt: testNow, Cycle: 1}
				if err := st.CreateNightSession(context.Background(), stale, testNow); err != nil {
					t.Fatalf("create night session: %v", err)
				}
				if held {
					next := stale
					next.StopHold = &store.NightSessionStopHold{Reason: nightStopHoldReason, At: testNow}
					if err := st.UpdateNightSession(context.Background(), next, testNow); err != nil {
						t.Fatalf("set hold: %v", err)
					}
				}
				cue := config.NightSessionCue{Name: "blackout", Role: config.NightSessionCueRoleLighting, Action: "act-blackout", OnFailure: config.NightSessionCueOnFailureContinue}

				_, err := h.nightRunCue(context.Background(), testNow, stale, tc.phase, cue, testIssuer, tc.first)
				n := dispatcher.callCount()
				if !held && (err != nil || n != 1) {
					t.Fatalf("control: unheld cue err %v, dispatched %d times, want once", err, n)
				}
				if held && (!errors.Is(err, errNightCueSessionMoved) || n != 0) {
					t.Fatalf("held: err %v, dispatched %d times, want the moved error and no dispatch", err, n)
				}
			}
		})
	}
}

func TestAFadeOutCueStillDispatchesWhileHeld(t *testing.T) {
	h, st := nightCueTestHandlers(t)
	dispatcher := h.deps.ResolumeActions.(*fakeResolumeActionDispatcher)
	dispatcher.results = map[string]ResolumeActionResult{
		config.ShowActionResolumeBlackout: {Outcome: ResolumeOutcomeConfirmed, Dispatched: true, Reason: "dark"},
	}
	putNightAction(t, st, "act-blackout", blackoutResolumeAction())
	rec := store.NightSessionRecord{ID: "sess-1", ConfigObjectID: "halloween-main", ConfigRevision: 1, State: nightStateFadingOut, StateEnteredAt: testNow, Cycle: 1,
		StopHold: &store.NightSessionStopHold{Reason: nightStopHoldReason, At: testNow}}
	if err := st.CreateNightSession(context.Background(), rec, testNow); err != nil {
		t.Fatalf("create night session: %v", err)
	}
	cue := config.NightSessionCue{Name: "blackout", Role: config.NightSessionCueRoleLighting, Action: "act-blackout", OnFailure: config.NightSessionCueOnFailureContinue}
	if _, err := h.nightRunCue(context.Background(), testNow, rec, nightPhaseFadeOut, cue, testIssuer, false); err != nil || dispatcher.callCount() != 1 {
		t.Fatalf("fade-out cue while held: err %v, dispatched %d times, want once: a shutdown only removes output", err, dispatcher.callCount())
	}
}
