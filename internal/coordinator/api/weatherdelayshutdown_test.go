package api

import (
	"context"
	"testing"
	"time"

	"github.com/showmeshsystems/showmesh/internal/coordinator/config"
	"github.com/showmeshsystems/showmesh/internal/coordinator/identity"
	"github.com/showmeshsystems/showmesh/internal/coordinator/store"
	"github.com/showmeshsystems/showmesh/pkg/observation"
)

// During a weather delay the night loop still shuts down and still holds
// everything that would start output.

func clearWeatherDelay(t *testing.T, st *store.Store) {
	t.Helper()
	if err := st.SetWeatherDelayState(context.Background(), store.WeatherDelayStateRecord{Revision: 2}); err != nil {
		t.Fatalf("SetWeatherDelayState: %v", err)
	}
}

func (r *resumeHarness) tick() {
	r.t.Helper()
	r.h.nightTick(context.Background(), r.now)
}

func (r *resumeHarness) assertFPPCommands(want ...string) {
	r.t.Helper()
	cmds, args := r.fppCommands()
	if len(cmds) != len(want) {
		r.t.Fatalf("FPP commands = %v %v, want %v", cmds, args, want)
	}
	for i := range want {
		if cmds[i] != want[i] {
			r.t.Fatalf("FPP commands = %v %v, want %v", cmds, args, want)
		}
	}
}

// fadeToStopped reports idle playback after the stop and ticks until the
// session is stopped.
func (r *resumeHarness) fadeToStopped() {
	r.t.Helper()
	r.tick()
	if cmds, _ := r.fppCommands(); len(cmds) == 0 || cmds[len(cmds)-1] == "Start Playlist" {
		r.t.Fatalf("FPP commands after the fading-out tick = %v, want a stop", cmds)
	}
	r.now = r.now.Add(5 * time.Second)
	r.obs.set([]observation.Observation{
		statusObservation("player-01", fppStatusValueIdle, r.now),
		playlistNameObservation("player-01", "", r.now),
	})
	r.tick()
	if got := mustGetCurrentSession(r.t, r.st); got.State != nightStateStopped {
		r.t.Fatalf("state = %q after fresh idle evidence during a weather delay, want stopped", got.State)
	}
}

func TestWeatherDelayHoldsTransitionToShow(t *testing.T) {
	r := newResumeHarness(t)
	e := r.now
	r.createSession(store.NightSessionRecord{
		State: nightStateTransitionToShow, StateEnteredAt: r.now.Add(-time.Minute), Cycle: 2, ArmedShowID: "show-2",
		BoundaryJSON: encodeNightBoundary(nightBoundary{State: nightBoundaryStateArmed, ExpectedAt: &e, LastTickAt: &e}),
	})
	r.obs.set([]observation.Observation{statusObservation("player-01", fppStatusValueIdle, r.now)})
	setWeatherDelayActive(t, r.st)

	r.tick()
	r.assertFPPCommands()
	if got := mustGetCurrentSession(t, r.st); got.State != nightStateTransitionToShow {
		t.Fatalf("state = %q during a weather delay, want transition-to-show held", got.State)
	}

	clearWeatherDelay(t, r.st)
	r.tick()
	r.assertFPPCommands("Start Playlist")
}

func TestWeatherDelayHoldsRestingStart(t *testing.T) {
	r := newResumeHarness(t)
	r.createSession(store.NightSessionRecord{State: nightStatePreshow, StateEnteredAt: r.now.Add(-time.Minute)})
	r.obs.set([]observation.Observation{statusObservation("player-01", fppStatusValueIdle, r.now)})
	setWeatherDelayActive(t, r.st)

	r.tick()
	r.assertFPPCommands()

	clearWeatherDelay(t, r.st)
	r.tick()
	r.assertFPPCommands("Start Playlist")
}

func TestWeatherDelayFadeOutNightFromLiveReachesStopped(t *testing.T) {
	r := newResumeHarness(t)
	r.h.fppCommandConfirmDeadline, r.h.fppCommandPollInterval = 50*time.Millisecond, 10*time.Millisecond
	liveAt := r.now.Add(-10 * time.Minute)
	r.createSession(store.NightSessionRecord{State: nightStateLive, StateEnteredAt: liveAt, Cycle: 3, ShowCommitted: true, ContentAnchorJSON: showAnchorAt(liveAt)})
	if err := r.st.OpenNightCycleOutcome(context.Background(), "sess-1", 3, liveAt); err != nil {
		t.Fatalf("open cycle outcome: %v", err)
	}
	setWeatherDelayActive(t, r.st)

	if resp, body := nightCommandRaw(t, r.api, r.token, "fade-out-night"); resp.StatusCode != 202 {
		t.Fatalf("fade-out-night during a weather delay: status = %d, want 202; body: %s", resp.StatusCode, body)
	}
	r.tick()
	if got := mustGetCurrentSession(t, r.st); got.State != nightStateFadingOut {
		t.Fatalf("state = %q, want fading-out: the delay already stopped the show, so the fade must not wait for it", got.State)
	}
	outcomes, err := r.st.ListNightCycleOutcomes(context.Background(), "sess-1")
	if err != nil || len(outcomes) != 1 || outcomes[0].Outcome != store.NightCycleOutcomeStopped {
		t.Fatalf("cycle outcomes = %+v (err %v), want cycle 3 closed stopped", outcomes, err)
	}
	r.fadeToStopped()
}

func TestWeatherDelayEmergencyStopPowerDownCompletesItsNightStep(t *testing.T) {
	r := newResumeHarness(t)
	r.h.fppCommandConfirmDeadline, r.h.fppCommandPollInterval = 50*time.Millisecond, 10*time.Millisecond
	r.createSession(store.NightSessionRecord{State: nightStateLive, StateEnteredAt: r.now.Add(-time.Minute), Cycle: 1, ShowCommitted: true, ContentAnchorJSON: showAnchorAt(r.now.Add(-time.Minute))})
	setWeatherDelayActive(t, r.st)

	out := r.h.nightEmergencyPowerDown(context.Background(), r.now, identity.AuditEntry{Timestamp: r.now, PrincipalID: "admin-1"})
	if out.Error != "" || !out.Present {
		t.Fatalf("emergency power-down night step = %+v, want applied", out)
	}
	r.fadeToStopped()
}

// activeWeatherDelayStore always reads as an active delay.
type activeWeatherDelayStore struct{}

func (activeWeatherDelayStore) GetWeatherDelayState(context.Context) (store.WeatherDelayStateRecord, error) {
	return store.WeatherDelayStateRecord{Active: true, Kind: "delay", Revision: 1}, nil
}

func (activeWeatherDelayStore) SetWeatherDelayState(context.Context, store.WeatherDelayStateRecord) error {
	return nil
}

func TestWeatherDelayPowerDownPresentationCompletes(t *testing.T) {
	now := time.Date(2026, 10, 31, 23, 0, 0, 0, time.UTC)
	off := &config.NightPresentationPowerOff{
		NightPowerBinding: config.NightPowerBinding{
			Action: "power-off-action", PowerDomain: config.NightPowerDomainPresentation, DomainProvenance: config.NightDomainProvenanceOperatorDeclared,
		},
		RemovalPolicy:            config.NightRemovalPolicyImmediate,
		ImmediateSafeAttestation: true,
	}
	f := newNightPowerOffFixture(t, &now, off, map[string]ResolumeActionResult{
		"blackout": resolumeConfirmed("power relay confirmed open", now),
	}, map[string]string{"power-off-action": "blackout"})
	f.h.deps.WeatherDelay = NewWeatherDelayStateKeeper(activeWeatherDelayStore{})

	f.h.nightTick(context.Background(), now)

	if got := f.res.callCount(); got != 1 {
		t.Fatalf("power-off dispatches during a weather delay = %d, want 1", got)
	}
	if final := mustGetCurrentSession(t, f.st); final.PowerPhase == nightPowerPhaseConfiguredNotDispatched {
		t.Fatal("power-down never completed during a weather delay")
	}
}
