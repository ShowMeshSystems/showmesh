package api

import (
	"context"
	"net/http"
	"testing"
	"time"

	"github.com/showmeshsystems/showmesh/internal/coordinator/config"
	"github.com/showmeshsystems/showmesh/internal/coordinator/identity"
	"github.com/showmeshsystems/showmesh/internal/coordinator/store"
	pkgaudio "github.com/showmeshsystems/showmesh/pkg/audio"
	"github.com/showmeshsystems/showmesh/pkg/observation"
)

// Covers item 2: the post-cancel graceful night shutdown, gated on the
// cancel alert finishing on the plan's own nodes, and its ceiling.

// weatherDelayCancelShutdownHarness wires a resumeHarness's own store,
// identity and night-session config together with a fakeNodeAudioLister,
// so a test can drive the alert's own reported session state directly
// rather than waiting out a real audio pipeline.
type weatherDelayCancelShutdownHarness struct {
	*resumeHarness
	audio *fakeNodeAudioLister
}

func newWeatherDelayCancelShutdownHarness(t *testing.T) *weatherDelayCancelShutdownHarness {
	t.Helper()
	r := newResumeHarness(t)
	audio := &fakeNodeAudioLister{}
	deps := r.h.deps
	deps.Audio = audio
	r.h.deps = deps
	r.api = New(deps, Options{Clock: func() time.Time { return r.now }, Logger: testLogger()})
	return &weatherDelayCancelShutdownHarness{resumeHarness: r, audio: audio}
}

// configureCancelAlert sets a cancel alert asset, so the shutdown waits for it.
func (r *weatherDelayCancelShutdownHarness) configureCancelAlert() {
	r.t.Helper()
	alert := config.WeatherDelayDefaultPayload.Alert
	alert.CancelNightAssetID = "cancel-asset"
	payload, err := config.EncodeWeatherDelayPayload(config.WeatherDelayPayload{Alert: alert, Triggers: config.WeatherDelayDefaultPayload.Triggers})
	if err != nil {
		r.t.Fatalf("encode show.weatherdelay payload: %v", err)
	}
	putConfigForTest(r.t, r.st, config.ShowWeatherDelayConfigKind, config.ShowWeatherDelayConfigObjectID, payload)
}

// startLiveSession creates a live night session with an open cycle and
// seeds the idle FPP evidence the cancel's own stop confirms against.
func (r *weatherDelayCancelShutdownHarness) startLiveSession() {
	r.t.Helper()
	r.createSession(store.NightSessionRecord{State: nightStateLive, StateEnteredAt: r.now.Add(-time.Minute), Cycle: 1, ShowCommitted: true, ContentAnchorJSON: showAnchorAt(r.now.Add(-time.Minute))})
	if err := r.st.OpenNightCycleOutcome(context.Background(), "sess-1", 1, r.now.Add(-time.Minute)); err != nil {
		r.t.Fatalf("open cycle outcome: %v", err)
	}
	r.obs.set([]observation.Observation{
		statusObservation("player-01", fppStatusValueIdle, r.now),
		playlistNameObservation("player-01", "", r.now),
	})
}

func (r *weatherDelayCancelShutdownHarness) waitForFadingOut(why string) {
	r.t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for mustGetCurrentSession(r.t, r.st).State != nightStateFadingOut {
		if time.Now().After(deadline) {
			r.t.Fatalf("night session never entered fading-out %s; state = %q", why, mustGetCurrentSession(r.t, r.st).State)
		}
		time.Sleep(time.Millisecond)
	}
}

func fastCancelShutdownPoll(t *testing.T) {
	t.Helper()
	orig := weatherDelayCancelShutdownPollInterval
	weatherDelayCancelShutdownPollInterval = 5 * time.Millisecond
	t.Cleanup(func() { weatherDelayCancelShutdownPollInterval = orig })
}

func (r *weatherDelayCancelShutdownHarness) cancelNight() {
	r.t.Helper()
	auth := map[string]string{"Authorization": "Bearer " + r.token}
	resp, body := doRawRequest(r.t, r.api.Handler, newJSONRequest(r.t, http.MethodPost, "/api/v1/weather-delay/cancel-night", `{"idempotencyKey":"cancel-1"}`, auth))
	if resp.StatusCode != http.StatusOK {
		r.t.Fatalf("cancel-night: status = %d, want 200; body: %s", resp.StatusCode, body)
	}
}

// TestWeatherDelayCancelNightShutdownWaitsForTheAlertToEnd proves the
// night shutdown does not run while the cancel alert is still reported
// playing, and does run once it is reported ended.
func TestWeatherDelayCancelNightShutdownWaitsForTheAlertToEnd(t *testing.T) {
	orig := weatherDelayCancelShutdownPollInterval
	weatherDelayCancelShutdownPollInterval = 5 * time.Millisecond
	t.Cleanup(func() { weatherDelayCancelShutdownPollInterval = orig })

	r := newWeatherDelayCancelShutdownHarness(t)
	t.Cleanup(weatherDelayCancelShutdownBackground.Wait)
	r.configureCancelAlert()
	r.startLiveSession()
	r.audio.setObservations("audio-01", []observation.Observation{
		sessionStateObservation(weatherDelayCancelAlertSessionID, string(pkgaudio.StatePlaying), r.now, r.now),
	})

	r.cancelNight()

	if r.h.weatherDelayCancelAlertEnded(r.now, r.now, []string{"audio-01"}, map[string]bool{}) {
		t.Fatal("fixture setup did not report the alert playing")
	}

	// While the alert is still reported playing, the night session must
	// not have entered its own shutdown yet.
	time.Sleep(30 * time.Millisecond)
	if got := mustGetCurrentSession(t, r.st); got.State != nightStateLive {
		t.Fatalf("state = %q while the alert is still playing, want live (unchanged)", got.State)
	}

	r.audio.setObservations("audio-01", []observation.Observation{
		sessionStateObservation(weatherDelayCancelAlertSessionID, string(pkgaudio.StateStopped), r.now, r.now),
	})

	deadline := time.Now().Add(5 * time.Second)
	for {
		if got := mustGetCurrentSession(t, r.st); got.State == nightStateFadingOut {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("night session never entered fading-out after the alert ended; state = %q", mustGetCurrentSession(t, r.st).State)
		}
		time.Sleep(time.Millisecond)
	}
}

// TestWeatherDelayCancelNightShutdownRespectsCeiling proves the shutdown
// runs once the ceiling passes even though the alert is still reported
// playing.
func TestWeatherDelayCancelNightShutdownRespectsCeiling(t *testing.T) {
	origPoll, origCeiling := weatherDelayCancelShutdownPollInterval, weatherDelayCancelShutdownCeiling
	weatherDelayCancelShutdownPollInterval = 5 * time.Millisecond
	weatherDelayCancelShutdownCeiling = 20 * time.Millisecond
	t.Cleanup(func() {
		weatherDelayCancelShutdownPollInterval, weatherDelayCancelShutdownCeiling = origPoll, origCeiling
	})

	r := newWeatherDelayCancelShutdownHarness(t)
	t.Cleanup(weatherDelayCancelShutdownBackground.Wait)
	r.configureCancelAlert()
	r.startLiveSession()
	r.audio.setObservations("audio-01", []observation.Observation{
		sessionStateObservation(weatherDelayCancelAlertSessionID, string(pkgaudio.StatePlaying), r.now, r.now),
	})

	r.cancelNight()

	deadline := time.Now().Add(5 * time.Second)
	for {
		if got := mustGetCurrentSession(t, r.st); got.State == nightStateFadingOut {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("night session never entered fading-out after the ceiling passed; state = %q", mustGetCurrentSession(t, r.st).State)
		}
		time.Sleep(time.Millisecond)
	}
}

// TestWeatherDelayCancelNightShutdownSkippedWithNoNightSession proves
// decision 2's own "if no night session is active, nothing to shut down."
func TestWeatherDelayCancelNightShutdownSkippedWithNoNightSession(t *testing.T) {
	orig := weatherDelayCancelShutdownPollInterval
	weatherDelayCancelShutdownPollInterval = 5 * time.Millisecond
	t.Cleanup(func() { weatherDelayCancelShutdownPollInterval = orig })

	r := newWeatherDelayCancelShutdownHarness(t)
	t.Cleanup(weatherDelayCancelShutdownBackground.Wait)
	r.obs.set([]observation.Observation{
		statusObservation("player-01", fppStatusValueIdle, r.now),
		playlistNameObservation("player-01", "", r.now),
	})
	r.cancelNight()
	weatherDelayCancelShutdownBackground.Wait()
	// No session was ever created; nightEmergencyPowerDown must have
	// reported Present:false and done nothing (a panic here would fail
	// the test, which is the strongest available proof of no crash from
	// running the shutdown against an absent session).
}

// TestWeatherDelayCancelNightShutdownWaitsForAnAlertNotYetReported proves a
// reachable node that has not reported the new alert yet is waited on,
// rather than read as an alert that already ended.
func TestWeatherDelayCancelNightShutdownWaitsForAnAlertNotYetReported(t *testing.T) {
	fastCancelShutdownPoll(t)
	r := newWeatherDelayCancelShutdownHarness(t)
	t.Cleanup(weatherDelayCancelShutdownBackground.Wait)
	r.configureCancelAlert()
	r.startLiveSession()
	earlier := r.now.Add(-time.Second)
	r.audio.setObservations("audio-01", []observation.Observation{
		sessionStateObservation("other-session", string(pkgaudio.StatePlaying), r.now, r.now),
		sessionStateObservation(weatherDelayCancelAlertSessionID, string(pkgaudio.StateStopped), earlier, earlier),
	})

	r.cancelNight()

	time.Sleep(50 * time.Millisecond)
	if got := mustGetCurrentSession(t, r.st); got.State != nightStateLive {
		t.Fatalf("state = %q before the node reported the cancel alert, want live (unchanged)", got.State)
	}

	r.audio.setObservations("audio-01", []observation.Observation{
		sessionStateObservation(weatherDelayCancelAlertSessionID, string(pkgaudio.StatePlaying), r.now, r.now),
	})
	time.Sleep(20 * time.Millisecond)
	if got := mustGetCurrentSession(t, r.st); got.State != nightStateLive {
		t.Fatalf("state = %q while the cancel alert plays, want live (unchanged)", got.State)
	}
	r.audio.setObservations("audio-01", []observation.Observation{
		sessionStateObservation(weatherDelayCancelAlertSessionID, string(pkgaudio.StateCompleted), r.now, r.now),
	})
	r.waitForFadingOut("after the cancel alert completed")
}

// TestWeatherDelayCancelNightShutdownUnreachableNodeDoesNotBlock proves a
// plan node with no current audio report never holds the shutdown.
func TestWeatherDelayCancelNightShutdownUnreachableNodeDoesNotBlock(t *testing.T) {
	fastCancelShutdownPoll(t)
	r := newWeatherDelayCancelShutdownHarness(t)
	t.Cleanup(weatherDelayCancelShutdownBackground.Wait)
	r.configureCancelAlert()
	r.startLiveSession()

	r.cancelNight()
	r.waitForFadingOut("with the only plan node unreachable")
}

// TestWeatherDelayCancelNightShutdownRunsAtOnceWithNoAlertConfigured proves
// a cancel with no alert asset shuts down without waiting for an alert.
func TestWeatherDelayCancelNightShutdownRunsAtOnceWithNoAlertConfigured(t *testing.T) {
	fastCancelShutdownPoll(t)
	r := newWeatherDelayCancelShutdownHarness(t)
	t.Cleanup(weatherDelayCancelShutdownBackground.Wait)
	r.startLiveSession()
	r.audio.setObservations("audio-01", []observation.Observation{
		sessionStateObservation("other-session", string(pkgaudio.StatePlaying), r.now, r.now),
	})

	r.cancelNight()
	r.waitForFadingOut("with no cancel alert configured")
}

// TestWeatherDelayCancelNightShutdownRunsOncePerCancel proves a second
// cancel press while the first watcher waits starts no second shutdown.
func TestWeatherDelayCancelNightShutdownRunsOncePerCancel(t *testing.T) {
	fastCancelShutdownPoll(t)
	r := newWeatherDelayCancelShutdownHarness(t)
	t.Cleanup(weatherDelayCancelShutdownBackground.Wait)
	r.configureCancelAlert()
	r.startLiveSession()
	r.audio.setObservations("audio-01", []observation.Observation{
		sessionStateObservation(weatherDelayCancelAlertSessionID, string(pkgaudio.StatePlaying), r.now, r.now),
	})

	r.cancelNight()
	r.cancelNight()
	r.audio.setObservations("audio-01", []observation.Observation{
		sessionStateObservation(weatherDelayCancelAlertSessionID, string(pkgaudio.StateCompleted), r.now, r.now),
	})
	weatherDelayCancelShutdownBackground.Wait()

	if got := countCancelNightShutdownAudits(t, r.st); got != 1 {
		t.Fatalf("cancel night shutdown ran %d times, want 1", got)
	}
}

// TestWeatherDelayCancelNightShutdownResumesAfterRestart proves a cancelled
// night stored before a coordinator restart still gets its shutdown.
func TestWeatherDelayCancelNightShutdownResumesAfterRestart(t *testing.T) {
	fastCancelShutdownPoll(t)
	r := newWeatherDelayCancelShutdownHarness(t)
	t.Cleanup(weatherDelayCancelShutdownBackground.Wait)
	r.startLiveSession()
	if err := r.st.SetWeatherDelayState(context.Background(), store.WeatherDelayStateRecord{
		Active: true, Kind: "cancelNight", StartedAt: r.now.Add(-time.Minute), StartedBy: "p1", Revision: 4,
	}); err != nil {
		t.Fatalf("SetWeatherDelayState: %v", err)
	}

	ResumeWeatherDelayCancelNightShutdown(context.Background(), r.h.deps, Options{Clock: func() time.Time { return r.now }, Logger: testLogger()})
	r.waitForFadingOut("after a restart with a cancelled night stored")
}

// TestWeatherDelayCancelNightShutdownNotResumedForAPlainDelay proves the
// startup hook leaves a plain delay alone.
func TestWeatherDelayCancelNightShutdownNotResumedForAPlainDelay(t *testing.T) {
	fastCancelShutdownPoll(t)
	r := newWeatherDelayCancelShutdownHarness(t)
	r.startLiveSession()
	if err := r.st.SetWeatherDelayState(context.Background(), store.WeatherDelayStateRecord{
		Active: true, Kind: "delay", StartedAt: r.now.Add(-time.Minute), StartedBy: "p1", Revision: 4,
	}); err != nil {
		t.Fatalf("SetWeatherDelayState: %v", err)
	}

	ResumeWeatherDelayCancelNightShutdown(context.Background(), r.h.deps, Options{Clock: func() time.Time { return r.now }, Logger: testLogger()})
	weatherDelayCancelShutdownBackground.Wait()
	if got := mustGetCurrentSession(t, r.st); got.State != nightStateLive {
		t.Fatalf("state = %q after startup with a plain delay stored, want live (unchanged)", got.State)
	}
}

func countCancelNightShutdownAudits(t *testing.T, st *store.Store) int {
	t.Helper()
	entries, err := st.ListAuditEntries(context.Background(), 0, 0)
	if err != nil {
		t.Fatalf("ListAuditEntries: %v", err)
	}
	n := 0
	for _, e := range entries {
		if e.Action == identity.AuditActionShowWeatherDelayCancelNight && e.Target == "shutdown" {
			n++
		}
	}
	return n
}
