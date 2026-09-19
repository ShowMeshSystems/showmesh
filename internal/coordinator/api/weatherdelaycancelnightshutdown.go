package api

import (
	"context"
	"fmt"
	"sync"
	"time"

	"github.com/showmeshsystems/showmesh/internal/coordinator/identity"
	"github.com/showmeshsystems/showmesh/internal/coordinator/store"
	pkgaudio "github.com/showmeshsystems/showmesh/pkg/audio"
	"github.com/showmeshsystems/showmesh/pkg/weatherdelay"
)

// After a cancel-night alert starts, the coordinator runs the same
// graceful night shutdown emergency stop's stop-power-down level does
// (nightEmergencyPowerDown, forced power-down-presentation), but only
// once the cancel alert has finished on the plan's own nodes. The level-1
// style immediate stop is never re-run here: cancel-night's own dispatch
// (weatherDelayStartOrChange's shared fan-out) already did that.

// weatherDelayCancelAlertSessionID mirrors internal/agent's own
// weatherDelayAlertSessionID literal; this package must never import
// internal/agent (see audionode.go's identical precedent for a node-side
// literal copied here rather than imported).
const weatherDelayCancelAlertSessionID = "weatherdelay:alert"

// weatherDelayCancelShutdownCeiling is the longest the shutdown waits for
// the cancel alert to finish before running the night shutdown anyway.
// This coordinator does not track a decoded asset duration (only a node
// does, after downloading and decoding the file), so the duration is
// always "unknown" from here and the fixed ceiling always applies.
var weatherDelayCancelShutdownCeiling = 15 * time.Minute

// weatherDelayCancelShutdownPollInterval paces the wait for the alert to
// end. A var so a test can drive it down; production never overrides it.
var weatherDelayCancelShutdownPollInterval = 2 * time.Second

// weatherDelayCancelShutdownPrincipalID attributes the post-alert
// shutdown's own night-session commands to a stable, clearly-labeled
// identity, mirroring weatherDelayEnforceSystemPrincipalID one file over.
const weatherDelayCancelShutdownPrincipalID = "system:weather-delay-cancel-night"

// weatherDelayCancelShutdownBackground counts a shutdown watcher still
// running, so a test can wait for it to finish rather than sleeping.
var weatherDelayCancelShutdownBackground sync.WaitGroup

// weatherDelayCancelNightAfterDispatch is cancel-night's own afterDispatch
// hook (weatherDelayStartOrChange): it runs in the background, detached
// from the triggering request, so the operator's own response is never
// held up by however long the alert takes to finish.
func (h *handlers) weatherDelayCancelNightAfterDispatch(rec store.WeatherDelayStateRecord, planNodeIDs []string) {
	weatherDelayCancelShutdownBackground.Add(1)
	go func() {
		defer weatherDelayCancelShutdownBackground.Done()
		defer func() {
			if r := recover(); r != nil {
				h.logWarn("weather delay cancel night: shutdown watcher panicked; recovered", "panic", fmt.Sprintf("%v", r))
			}
		}()
		h.weatherDelayRunCancelNightShutdown(context.Background(), rec, planNodeIDs)
	}()
}

// weatherDelayRunCancelNightShutdown waits for the cancel alert to end (or
// the ceiling) and then runs the graceful night shutdown, unless this
// cancel has already been superseded (resumed, or changed again) by the
// time the wait ends.
func (h *handlers) weatherDelayRunCancelNightShutdown(ctx context.Context, rec store.WeatherDelayStateRecord, planNodeIDs []string) {
	ended, waited := h.weatherDelayWaitForCancelAlertToEnd(ctx, planNodeIDs)

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
			"alertEnded": ended, "waitedSeconds": waited.Seconds(),
			"nightSessionPresent": outcome.Present, "nightSessionOutcome": outcome.Outcome, "nightSessionError": outcome.Error,
		},
	})
	h.appendWeatherDelayChangedEvent(ctx, now, fmt.Sprintf(
		"cancel night shutdown: alert ended=%v after %s; night session present=%v", ended, waited.Round(time.Second), outcome.Present))
}

// weatherDelayWaitForCancelAlertToEnd polls until the cancel alert has
// ended on every reachable plan node or the ceiling passes. This wait is
// timed on the real wall clock, not [handlers.now]: it paces a real
// background poll against a real alert playing on real hardware, never a
// business timestamp a test's own fake clock stands in for (contrast the
// audit and event timestamps in
// [handlers.weatherDelayRunCancelNightShutdown], which do use [handlers.now]).
func (h *handlers) weatherDelayWaitForCancelAlertToEnd(ctx context.Context, nodeIDs []string) (ended bool, waited time.Duration) {
	start := time.Now()
	deadline := start.Add(weatherDelayCancelShutdownCeiling)
	ticker := time.NewTicker(weatherDelayCancelShutdownPollInterval)
	defer ticker.Stop()
	for {
		if h.weatherDelayCancelAlertEnded(h.now(), nodeIDs) {
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

// weatherDelayCancelAlertEnded reports whether the cancel alert has ended
// on every reachable plan node. A node with no audio observation at all is
// unreachable and never blocks the wait; one whose alert session reports
// anything but playing has ended it.
func (h *handlers) weatherDelayCancelAlertEnded(now time.Time, nodeIDs []string) bool {
	for _, nodeID := range nodeIDs {
		if len(h.deps.Audio.NodeAudioObservations(nodeID)) == 0 {
			continue
		}
		state, ok := nightBackgroundAudioReportedSessionState(h.deps.Audio, now, time.Time{}, nodeID, weatherDelayCancelAlertSessionID)
		if ok && state == string(pkgaudio.StatePlaying) {
			return false
		}
	}
	return true
}
