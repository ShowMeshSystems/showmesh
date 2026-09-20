package api

import (
	"context"
	"log/slog"
	"reflect"
	"sync"
	"time"

	"github.com/showmeshsystems/showmesh/internal/coordinator/config"
	"github.com/showmeshsystems/showmesh/internal/coordinator/weathertrigger"
)

// weatherDelayTriggerLoopInterval paces both the pending-decision deadline
// check and the NWS poller lifecycle reconciliation. A var so a test can
// drive it down; production never overrides it.
var weatherDelayTriggerLoopInterval = 5 * time.Second

// WeatherDelayNWSStatus is a stable handle to whichever [weathertrigger.NWSPoller]
// is currently running, if any, wired once into [Dependencies.WeatherDelayNWS]
// at startup while [WeatherDelayTriggerLoop] swaps the poller underneath it
// as show.weatherdelay's triggers.nws configuration changes.
type WeatherDelayNWSStatus struct {
	mu      sync.Mutex
	current *weathertrigger.NWSPoller
}

// NewWeatherDelayNWSStatus builds a status handle with no poller running.
func NewWeatherDelayNWSStatus() *WeatherDelayNWSStatus { return &WeatherDelayNWSStatus{} }

func (s *WeatherDelayNWSStatus) setCurrent(p *weathertrigger.NWSPoller) {
	s.mu.Lock()
	s.current = p
	s.mu.Unlock()
}

// Health implements [WeatherDelayNWSHealth].
func (s *WeatherDelayNWSStatus) Health() (weathertrigger.NWSHealth, bool) {
	s.mu.Lock()
	p := s.current
	s.mu.Unlock()
	if p == nil {
		return weathertrigger.NWSHealth{}, false
	}
	return p.Health(), true
}

// WeatherDelayTriggerLoop is ADR-053 decision 12's own background driver,
// built the same way [WeatherDelayEnforcer] is: a private *handlers with
// no HTTP request of its own, ticking on its own interval. It owns two
// jobs: applying a pending decision's default action once its deadline
// passes, and starting, reconfiguring, or stopping the built-in NWS
// poller to match show.weatherdelay's current triggers.nws.
type WeatherDelayTriggerLoop struct {
	h        *handlers
	interval time.Duration
	logger   *slog.Logger
	status   *WeatherDelayNWSStatus

	mu        sync.Mutex
	nwsCfg    config.WeatherDelayNWSTriggerPayload
	nwsCancel context.CancelFunc
}

// NewWeatherDelayTriggerLoop builds a [WeatherDelayTriggerLoop] against
// deps/opts, mirroring [NewWeatherDelayEnforcer]. status must be the same
// handle wired into deps.WeatherDelayNWS, so GET /weather-delay sees
// whichever poller this loop is currently running.
func NewWeatherDelayTriggerLoop(deps Dependencies, opts Options, status *WeatherDelayNWSStatus) *WeatherDelayTriggerLoop {
	deps = deps.withDefaults()
	opts = opts.withDefaults()
	return &WeatherDelayTriggerLoop{
		h:        &handlers{deps: deps, clock: opts.Clock, logger: opts.Logger},
		interval: weatherDelayTriggerLoopInterval, logger: opts.Logger, status: status,
	}
}

// Run ticks until ctx is done, then stops any running poller.
func (l *WeatherDelayTriggerLoop) Run(ctx context.Context) {
	l.tick(ctx)
	ticker := time.NewTicker(l.interval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			l.stopNWSLocked()
			return
		case <-ticker.C:
			l.tick(ctx)
		}
	}
}

func (l *WeatherDelayTriggerLoop) tick(ctx context.Context) {
	defer func() {
		if r := recover(); r != nil {
			l.logger.Warn("weather delay trigger loop: tick panicked; recovered", "panic", r)
		}
	}()
	l.reconcileDeadline(ctx, l.h.now())
	l.reconcileNWS(ctx)
}

// reconcileDeadline applies the pending decision's default action once its
// deadline has passed.
func (l *WeatherDelayTriggerLoop) reconcileDeadline(ctx context.Context, now time.Time) {
	pending, ok, err := l.h.deps.WeatherDelayTrigger.GetPendingWeatherDelayDecision(ctx)
	if err != nil {
		l.logger.Warn("weather delay trigger loop: failed to read the pending decision", "error", err)
		return
	}
	if !ok || now.Before(pending.Deadline) {
		return
	}
	l.h.weatherDelayApplyTriggerDeadline(ctx, now, pending)
}

// reconcileNWS starts, restarts (on a configuration change), or stops the
// built-in poller to match the current triggers.nws.
func (l *WeatherDelayTriggerLoop) reconcileNWS(ctx context.Context) {
	payload, _, _, _, err := resolveWeatherDelayConfig(ctx, l.h.deps.Config)
	if err != nil {
		l.logger.Warn("weather delay trigger loop: failed to resolve show.weatherdelay config; leaving the NWS poller as it is", "error", err)
		return
	}
	want := payload.Triggers.NWS

	l.mu.Lock()
	defer l.mu.Unlock()
	if !want.Enabled {
		l.stopNWSLocked()
		return
	}
	if l.nwsCancel != nil && reflect.DeepEqual(l.nwsCfg, want) {
		return // already running with this exact configuration
	}
	l.stopNWSLocked()

	poller := weathertrigger.NewNWSPoller(weathertrigger.NWSPollerConfig{
		Latitude: want.Latitude, Longitude: want.Longitude, Contact: want.Contact,
		PollSeconds: want.PollSeconds, EventTypes: want.EventTypes,
	}, l.h.weatherDelayOnNWSAlert, l.h.now, l.logger)

	pollCtx, cancel := context.WithCancel(ctx)
	l.nwsCfg, l.nwsCancel = want, cancel
	l.status.setCurrent(poller)
	go poller.Run(pollCtx)
}

func (l *WeatherDelayTriggerLoop) stopNWSLocked() {
	if l.nwsCancel != nil {
		l.nwsCancel()
	}
	l.nwsCancel = nil
	l.nwsCfg = config.WeatherDelayNWSTriggerPayload{}
	if l.status != nil {
		l.status.setCurrent(nil)
	}
}
