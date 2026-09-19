package api

import (
	"context"
	"time"

	v1 "github.com/showmeshsystems/showmesh/internal/coordinator/api/v1"
	"github.com/showmeshsystems/showmesh/internal/coordinator/config"
	pkgaudio "github.com/showmeshsystems/showmesh/pkg/audio"
)

// Actions that start output. While a weather delay is active they are
// refused at the shared dispatch path, so a macro, a show action and the
// HTTP route are all held alike. Stop-direction actions are never listed.
var (
	weatherDelayHeldFPPActions = map[string]bool{"startPlaylist": true, "resumePlaylist": true}

	weatherDelayHeldAudioActions = map[string]bool{
		string(pkgaudio.OperationSessionStart):  true,
		string(pkgaudio.OperationSessionResume): true,
		string(pkgaudio.OperationOutputUnmute):  true,
	}

	weatherDelayHeldResolumeActions = map[string]bool{
		config.ShowActionResolumeLaunchClip:   true,
		config.ShowActionResolumeLaunchColumn: true,
	}
)

// weatherDelayHeldProblem returns the refusal for an output-starting
// action while a delay is active, nil when none is, or the read error.
func (h *handlers) weatherDelayHeldProblem(ctx context.Context, detail string) (*v1.Problem, error) {
	active, err := h.weatherDelayActive(ctx)
	if err != nil || !active {
		return nil, err
	}
	p := weatherDelayActiveProblem(detail)
	return &p, nil
}

// weatherDelayResolumeGate refuses the Resolume actions that start output
// while a delay is active and passes everything else, blackout included.
type weatherDelayResolumeGate struct {
	inner ResolumeActionDispatcher
	state WeatherDelayStore
}

// WeatherDelayGatedResolumeActions wraps inner so every caller of it, the
// macro executor included, is held during a weather delay.
func WeatherDelayGatedResolumeActions(inner ResolumeActionDispatcher, state WeatherDelayStore) ResolumeActionDispatcher {
	if inner == nil || state == nil {
		return inner
	}
	return weatherDelayResolumeGate{inner: inner, state: state}
}

func (g weatherDelayResolumeGate) Actions() []ResolumeActionDescriptor { return g.inner.Actions() }

func (g weatherDelayResolumeGate) Dispatch(ctx context.Context, action string, params map[string]any, now time.Time) (ResolumeActionResult, error) {
	if weatherDelayHeldResolumeActions[action] {
		rec, err := g.state.GetWeatherDelayState(ctx)
		if err != nil || rec.Active {
			return ResolumeActionResult{Outcome: ResolumeOutcomeRefused, Reason: "A weather delay is active. Resume the show to launch Resolume content."}, nil
		}
	}
	return g.inner.Dispatch(ctx, action, params, now)
}
