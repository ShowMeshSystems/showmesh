package api

import (
	"context"
	"log/slog"
	"sync"
	"time"

	"github.com/showmeshsystems/showmesh/internal/coordinator/identity"
	"github.com/showmeshsystems/showmesh/internal/coordinator/store"
	"github.com/showmeshsystems/showmesh/pkg/command"
	"github.com/showmeshsystems/showmesh/pkg/mqttproto"
	"github.com/showmeshsystems/showmesh/pkg/weatherdelay"
)

// A node can be delayed by a pre-signed start while the coordinator is down
// (ADR-053 decision 8). The node reports its delay on its heartbeat, and the
// coordinator enters the same delay through the ordinary start path.

// weatherDelayAdoption is one node's report, and what the start path did
// with it.
type weatherDelayAdoption struct {
	nodeID string
	report mqttproto.HealthWeatherDelay

	adopted bool
	stale   bool
}

func (a *weatherDelayAdoption) startedBy() string { return "node " + a.nodeID }

// summary is the audit sentence. changed means the coordinator already
// held a delay and the node's cancelled night replaced it.
func (a *weatherDelayAdoption) summary(changed bool) string {
	switch {
	case changed:
		return "Node " + a.nodeID + " reported a cancelled night, so the weather delay was changed to a cancelled night. Press Resume to clear it."
	case a.report.Kind == weatherdelay.KindCancelNight:
		return "The night was cancelled on node " + a.nodeID + ", and the coordinator joined it. Press Resume to clear it."
	default:
		return "A weather delay was started on node " + a.nodeID + ", and the coordinator joined it. Press Resume when it is safe to continue."
	}
}

// eventSummary follows "weather delay " in the event stream.
func (a *weatherDelayAdoption) eventSummary(changed bool) string {
	if changed {
		return "changed to a cancelled night reported by node " + a.nodeID
	}
	return "started on node " + a.nodeID
}

// startedAt is the node's own start time, never later than now: a node
// clock that runs ahead must not date the delay in the future.
func (a *weatherDelayAdoption) startedAt(now time.Time) time.Time {
	if a.report.StartedAt.IsZero() || a.report.StartedAt.After(now) {
		return now
	}
	return a.report.StartedAt
}

// weatherDelayAdoptionAllowed compares state numbers only, never clocks. The
// node echoes the newest coordinator state number it had seen when its delay
// began; a delay that began before the coordinator's last resume is stale.
func weatherDelayAdoptionAllowed(current store.WeatherDelayStateRecord, report mqttproto.HealthWeatherDelay) (allowed, stale bool) {
	if !report.Active {
		return false, false
	}
	// lastResume is the state number of the newest not-active state. A
	// delay is only ever entered one number above it.
	lastResume := current.Revision
	switch {
	case !current.Active:
	case current.Kind == weatherdelay.KindDelay && report.Kind == weatherdelay.KindCancelNight:
		lastResume = current.Revision - 1
	default:
		return false, false
	}
	if report.HeldRevision < lastResume {
		return false, true
	}
	return true, false
}

func weatherDelayAdoptSystemAuthContext(nodeID string) authContext {
	return authContext{ok: true, result: identity.Authenticated{
		Principal: identity.Principal{ID: "system:weather-delay-adopt:" + nodeID, Name: "node " + nodeID},
		Form:      identity.FormCLI,
	}}
}

// weatherDelayAdoptBackground counts adoptions still running, so a test can
// wait for them rather than sleeping.
var weatherDelayAdoptBackground sync.WaitGroup

// WeatherDelayAdopter is built the same way [WeatherDelayEnforcer] is: a
// private *handlers with no HTTP request of its own.
type WeatherDelayAdopter struct {
	h      *handlers
	logger *slog.Logger

	// busy admits one adoption at a time and guards loggedStale.
	busy        sync.Mutex
	loggedStale map[string]int64
}

// NewWeatherDelayAdopter builds a [WeatherDelayAdopter] against deps/opts.
func NewWeatherDelayAdopter(deps Dependencies, opts Options) *WeatherDelayAdopter {
	deps = deps.withDefaults()
	opts = opts.withDefaults()
	return &WeatherDelayAdopter{
		h:      newBackgroundHandlers(deps, opts),
		logger: opts.Logger, loggedStale: map[string]int64{},
	}
}

// Report takes one node's weather delay report from a live heartbeat and
// never blocks. A report that arrives while another is being acted on is
// dropped: the node sends it again on its next heartbeat.
func (a *WeatherDelayAdopter) Report(nodeID string, report mqttproto.HealthWeatherDelay) {
	if !report.Active || !a.busy.TryLock() {
		return
	}
	weatherDelayAdoptBackground.Add(1)
	go func() {
		defer weatherDelayAdoptBackground.Done()
		defer a.busy.Unlock()
		defer func() {
			if r := recover(); r != nil {
				a.logger.Warn("weather delay adopt: panicked; recovered", "nodeId", nodeID, "panic", r)
			}
		}()
		a.adoptLocked(context.Background(), nodeID, report)
	}()
}

// adoptLocked runs the ordinary start path for the node's kind. The caller
// holds busy. Each adoption has its own key, so its stops are sent every
// time and never answered from an earlier adoption's stored commands.
func (a *WeatherDelayAdopter) adoptLocked(ctx context.Context, nodeID string, report mqttproto.HealthWeatherDelay) {
	if report.Validate() != nil {
		return
	}
	adoption := &weatherDelayAdoption{nodeID: nodeID, report: report}
	var afterDispatch func(store.WeatherDelayStateRecord, []string)
	if report.Kind == weatherdelay.KindCancelNight {
		afterDispatch = a.h.weatherDelayCancelNightAfterDispatch
	}
	a.h.weatherDelayRunStartOrChange(ctx, a.h.now(), report.Kind, identity.AuditActionShowWeatherDelayAdopt, command.NewIdempotencyKey(),
		weatherDelayAdoptSystemAuthContext(nodeID), "", afterDispatch, adoption)

	switch {
	case adoption.adopted:
		delete(a.loggedStale, nodeID)
		a.logger.Warn("weather delay adopt: entered a weather delay reported by a node", "nodeId", nodeID, "kind", report.Kind)
	case adoption.stale && a.loggedStale[nodeID] != report.HeldRevision+1:
		a.loggedStale[nodeID] = report.HeldRevision + 1
		a.logger.Warn("weather delay adopt: ignoring a node's weather delay that began before the last resume; the retained state clears it on the node",
			"nodeId", nodeID, "kind", report.Kind, "heldRevision", report.HeldRevision)
	}
}
