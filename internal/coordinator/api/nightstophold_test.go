package api

import (
	"context"
	"net/http"
	"strings"
	"testing"
	"time"

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
	h, st, pub, _ := nightBackgroundAudioTestHandlers(t)
	putBackgroundAudioAsset(t, st, "halloween", "bg-1", "node-a", "asset-1")
	putBackgroundAudioAsset(t, st, "halloween", "bg-2", "node-a", "asset-2")
	ba := multiNodeBedConfig("node-a", "node-a", "node-b")
	rec := mustCreateRestingSessionWithBackgroundAudio(t, st, "sess-1", "node-a", ba, nightStatePreshow)
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
}
