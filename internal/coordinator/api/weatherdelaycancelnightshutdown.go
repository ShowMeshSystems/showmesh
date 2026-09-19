package api

import (
	"context"
	"fmt"
	"sync"
	"time"

	"github.com/showmeshsystems/showmesh/internal/coordinator/identity"
	"github.com/showmeshsystems/showmesh/internal/coordinator/store"
	pkgaudio "github.com/showmeshsystems/showmesh/pkg/audio"
	"github.com/showmeshsystems/showmesh/pkg/observation"
	"github.com/showmeshsystems/showmesh/pkg/weatherdelay"
)

// After a cancel night starts, the coordinator runs emergency stop's
// graceful night power-down, but only once the cancel alert has ended on
// every reachable plan node. The immediate stop already ran in the start.

// weatherDelayCancelAlertSessionID mirrors internal/agent's
// weatherDelayAlertSessionID; this package never imports internal/agent.
const weatherDelayCancelAlertSessionID = "weatherdelay:alert"

// weatherDelayCancelShutdownCeiling is the longest the shutdown waits for
// the alert. The coordinator never knows an asset's decoded duration, so
// the fixed ceiling for an unknown duration always applies.
var weatherDelayCancelShutdownCeiling = 15 * time.Minute

// weatherDelayCancelShutdownPollInterval paces the wait. A var so a test
// can drive it down; production never overrides it.
var weatherDelayCancelShutdownPollInterval = 2 * time.Second

// weatherDelayCancelShutdownPrincipalID attributes the shutdown's own
// night-session commands, like weatherDelayEnforceSystemPrincipalID.
const weatherDelayCancelShutdownPrincipalID = "system:weather-delay-cancel-night"

// weatherDelayCancelShutdownBackground counts running shutdown watchers,
// so a test can wait for them rather than sleeping.
var weatherDelayCancelShutdownBackground sync.WaitGroup

// weatherDelayCancelNightAfterDispatch is cancel-night's afterDispatch
// hook: the watcher runs detached so the response never waits on it.
func (h *handlers) weatherDelayCancelNightAfterDispatch(rec store.WeatherDelayStateRecord, planNodeIDs []string) {
	h.weatherDelayStartCancelNightShutdown(rec, planNodeIDs)
}

// weatherDelayStartCancelNightShutdown starts at most one watcher per
// state number, so a repeated cancel press never runs a second shutdown.
func (h *handlers) weatherDelayStartCancelNightShutdown(rec store.WeatherDelayStateRecord, planNodeIDs []string) {
	cache := h.deps.WeatherDelayGateCache
	if !cache.claimCancelShutdown(rec.Revision) {
		return
	}
	weatherDelayCancelShutdownBackground.Add(1)
	go func() {
		defer weatherDelayCancelShutdownBackground.Done()
		defer cache.releaseCancelShutdown(rec.Revision)
		defer func() {
			if r := recover(); r != nil {
				h.logWarn("weather delay cancel night: shutdown watcher panicked; recovered", "panic", fmt.Sprintf("%v", r))
			}
		}()
		h.weatherDelayRunCancelNightShutdown(context.Background(), rec, planNodeIDs)
	}()
}

// ResumeWeatherDelayCancelNightShutdown restarts the shutdown watcher at
// coordinator startup when a cancelled night is stored, so a restart
// during the alert never loses the shutdown. A repeat run is a no-op.
func ResumeWeatherDelayCancelNightShutdown(ctx context.Context, deps Dependencies, opts Options) {
	deps = deps.withDefaults()
	opts = opts.withDefaults()
	h := &handlers{deps: deps, clock: opts.Clock, logger: opts.Logger}
	rec, err := deps.WeatherDelay.GetWeatherDelayState(ctx)
	if err != nil {
		h.logWarn("weather delay cancel night: failed to read the state at startup; the night shutdown was not resumed", "error", err)
		return
	}
	if !rec.Active || rec.Kind != weatherdelay.KindCancelNight {
		return
	}
	payload, _, _, _, err := resolveWeatherDelayConfig(ctx, deps.Config)
	if err != nil {
		h.logWarn("weather delay cancel night: failed to resolve show.weatherdelay config at startup; waiting on no node", "error", err)
	}
	planNodeIDs, err := h.weatherDelayPlanNodeIDs(ctx, payload.Alert.NodeIDs)
	if err != nil {
		h.logWarn("weather delay cancel night: failed to resolve plan node ids at startup; waiting on no node", "error", err)
		planNodeIDs = nil
	}
	h.weatherDelayStartCancelNightShutdown(rec, planNodeIDs)
}

// weatherDelayRunCancelNightShutdown waits for the cancel alert to end (or
// the ceiling) and then runs the graceful night shutdown, unless this
// cancel was cleared or replaced by the time the wait ends.
func (h *handlers) weatherDelayRunCancelNightShutdown(ctx context.Context, rec store.WeatherDelayStateRecord, planNodeIDs []string) {
	alertConfigured := true
	if payload, _, _, _, err := resolveWeatherDelayConfig(ctx, h.deps.Config); err != nil {
		h.logWarn("weather delay cancel night: failed to resolve show.weatherdelay config; waiting for an alert anyway", "error", err)
	} else {
		alertConfigured = payload.Alert.CancelNightAssetID != ""
	}
	ended, waited := true, time.Duration(0)
	if alertConfigured {
		ended, waited = h.weatherDelayWaitForCancelAlertToEnd(ctx, planNodeIDs)
	}

	if cur, err := h.deps.WeatherDelay.GetWeatherDelayState(ctx); err != nil {
		h.logWarn("weather delay cancel night: failed to re-check the state before the night shutdown; running it anyway", "error", err)
	} else if !cur.Active || cur.Kind != weatherdelay.KindCancelNight || cur.Revision != rec.Revision {
		h.logWarn("weather delay cancel night: the cancel was superseded before the alert ended; the night shutdown is skipped",
			"revision", rec.Revision, "currentRevision", cur.Revision, "currentActive", cur.Active, "currentKind", cur.Kind)
		return
	}

	now := h.now()
	issuer := identity.AuditEntry{Timestamp: now, PrincipalID: weatherDelayCancelShutdownPrincipalID, PrincipalName: "weather delay cancel night"}
	outcome := h.nightEmergencyPowerDown(ctx, now, issuer)

	h.writeBestEffortAuditBounded(ctx, now, degradedAttributionReasonPostDispatch, identity.AuditEntry{
		Timestamp: now, PrincipalID: issuer.PrincipalID, PrincipalName: issuer.PrincipalName,
		Action: identity.AuditActionShowWeatherDelayCancelNight, Target: "shutdown", Kind: identity.AuditOutcome,
		Params: map[string]any{
			"alertConfigured": alertConfigured, "alertEnded": ended, "waitedSeconds": waited.Seconds(),
			"nightSessionPresent": outcome.Present, "nightSessionOutcome": outcome.Outcome, "nightSessionError": outcome.Error,
		},
	})
	summary := "cancel night: no show was running, so there was nothing to shut down"
	if outcome.Present {
		summary = "cancel night: the show is shutting down for the night"
	}
	h.appendWeatherDelayChangedEvent(ctx, now, summary)
}

// weatherDelayWaitForCancelAlertToEnd polls until the cancel alert has
// ended on every reachable plan node or the ceiling passes. It paces a real
// poll, so it uses the wall clock; evidence is fenced at h.now().
func (h *handlers) weatherDelayWaitForCancelAlertToEnd(ctx context.Context, nodeIDs []string) (ended bool, waited time.Duration) {
	start := time.Now()
	deadline := start.Add(weatherDelayCancelShutdownCeiling)
	ticker := time.NewTicker(weatherDelayCancelShutdownPollInterval)
	defer ticker.Stop()
	fence := h.now()
	seenPlaying := map[string]bool{}
	for {
		if h.weatherDelayCancelAlertEnded(h.now(), fence, nodeIDs, seenPlaying) {
			return true, time.Since(start)
		}
		if !time.Now().Before(deadline) {
			return false, time.Since(start)
		}
		select {
		case <-ctx.Done():
			return false, time.Since(start)
		case <-ticker.C:
		}
	}
}

// weatherDelayCancelAlertEnded reports whether the alert has ended on every
// reachable node. Only a report received after fence counts, so a node that
// has not reported the new alert yet is still waited on.
func (h *handlers) weatherDelayCancelAlertEnded(now, fence time.Time, nodeIDs []string, seenPlaying map[string]bool) bool {
	allEnded := true
	for _, nodeID := range nodeIDs {
		if !h.weatherDelayNodeAudioReachable(now, nodeID) {
			continue
		}
		state, ok := nightBackgroundAudioReportedSessionState(h.deps.Audio, now, fence, nodeID, weatherDelayCancelAlertSessionID)
		switch {
		case ok && state == string(pkgaudio.StatePlaying):
			seenPlaying[nodeID] = true
			allEnded = false
		case ok:
		case seenPlaying[nodeID]:
			// The node stopped reporting the alert session after playing it.
		default:
			allEnded = false
		}
	}
	return allEnded
}

// weatherDelayNodeAudioReachable reports whether nodeID has any current
// audio observation; a node with none cannot report the alert ending.
func (h *handlers) weatherDelayNodeAudioReachable(now time.Time, nodeID string) bool {
	for _, o := range h.deps.Audio.NodeAudioObservations(nodeID) {
		if o.StateAt(now) == observation.StateCurrent {
			return true
		}
	}
	return false
}
