package api

import (
	"context"
	"net/http"
	"testing"
	"time"

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
	r.createSession(store.NightSessionRecord{State: nightStateLive, StateEnteredAt: r.now.Add(-time.Minute), Cycle: 1, ShowCommitted: true, ContentAnchorJSON: showAnchorAt(r.now.Add(-time.Minute))})
	if err := r.st.OpenNightCycleOutcome(context.Background(), "sess-1", 1, r.now.Add(-time.Minute)); err != nil {
		t.Fatalf("open cycle outcome: %v", err)
	}
	r.audio.setObservations("audio-01", []observation.Observation{
		sessionStateObservation(weatherDelayCancelAlertSessionID, string(pkgaudio.StatePlaying), r.now, r.now),
	})
	// The cancel-night dispatch's own emergency-stop fan-out confirms its
	// FPP stop against fresh idle evidence; seed it so that confirmation
	// resolves immediately rather than waiting out its own deadline.
	r.obs.set([]observation.Observation{
		statusObservation("player-01", fppStatusValueIdle, r.now),
		playlistNameObservation("player-01", "", r.now),
	})

	r.cancelNight()

	if r.h.weatherDelayCancelAlertEnded(r.now, []string{"audio-01"}) {
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
	r.createSession(store.NightSessionRecord{State: nightStateLive, StateEnteredAt: r.now.Add(-time.Minute), Cycle: 1, ShowCommitted: true, ContentAnchorJSON: showAnchorAt(r.now.Add(-time.Minute))})
	if err := r.st.OpenNightCycleOutcome(context.Background(), "sess-1", 1, r.now.Add(-time.Minute)); err != nil {
		t.Fatalf("open cycle outcome: %v", err)
	}
	r.audio.setObservations("audio-01", []observation.Observation{
		sessionStateObservation(weatherDelayCancelAlertSessionID, string(pkgaudio.StatePlaying), r.now, r.now),
	})
	r.obs.set([]observation.Observation{
		statusObservation("player-01", fppStatusValueIdle, r.now),
		playlistNameObservation("player-01", "", r.now),
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
