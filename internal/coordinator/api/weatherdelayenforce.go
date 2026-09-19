package api

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"sync"
	"time"

	v1 "github.com/showmeshsystems/showmesh/internal/coordinator/api/v1"
	"github.com/showmeshsystems/showmesh/internal/coordinator/config"
	"github.com/showmeshsystems/showmesh/internal/coordinator/fppcommand"
	"github.com/showmeshsystems/showmesh/internal/coordinator/identity"
	"github.com/showmeshsystems/showmesh/internal/coordinator/store"
	"github.com/showmeshsystems/showmesh/pkg/mqttproto"
	"github.com/showmeshsystems/showmesh/pkg/observation"
)

// ADR-053 decision 4: the coordinator's own enforcement loop. It runs
// unconditionally, like every other reconcile loop in coordinator.go, and
// reads the stored state fresh every tick so a coordinator restart
// mid-delay resumes enforcing with no special-cased startup path. It only
// ever stops a playlist and closes a gate; the one exception is reopening
// a gate a player kept closed after a delay it never learned had ended
// (ADR-053 decision 10's own dark-heartbeat reasoning applies here too: a
// player that missed the resume must not stay dark forever).
//
// No configuration value, mode, scope or flag may disable, slow, or exempt
// an instance from this loop (ADR-053 decision 13). Do not add one.

// weatherDelayEnforceInterval is how often the loop ticks. A var so a test
// can drive it down; production never overrides it.
var weatherDelayEnforceInterval = 5 * time.Second

// weatherDelayEnforceInstanceTimeout bounds one FPP instance's own work
// within a tick, so one slow or dead instance never delays another's. A
// var so a test can drive it down; production never overrides it.
var weatherDelayEnforceInstanceTimeout = 4 * time.Second

// weatherDelayEnforceAuditInterval rate-limits the show.weatherdelay.enforce
// audit entry to at most one per instance per minute, even on a tick that
// re-sends both a stop and a close.
const weatherDelayEnforceAuditInterval = time.Minute

// weatherDelayStaleGateReopenInterval bounds the not-active reopen
// exception to at most once per instance per 30 seconds.
const weatherDelayStaleGateReopenInterval = 30 * time.Second

// weatherDelayGateFreshnessWindow bounds how old a cached gate reading may
// be and still count toward a power group's own confirmedDark: three
// ticks of headroom over [weatherDelayEnforceInterval], so one skipped
// tick never flips a group's own reported darkness.
const weatherDelayGateFreshnessWindow = 15 * time.Second

// weatherDelayFPPClientTimeout bounds each of the enforcer's own FPP
// requests (stop, gate read, gate write), shorter than
// [weatherDelayEnforceInstanceTimeout] so a client-level deadline fires
// before the per-instance context does. A var so a test can drive it down.
var weatherDelayFPPClientTimeout = 3 * time.Second

// WeatherDelayEnforcer is this loop's own background driver, built the
// same way [NightLoop] is: a private *handlers with no HTTP request of its
// own, ticking on its own interval.
type WeatherDelayEnforcer struct {
	h        *handlers
	interval time.Duration
	logger   *slog.Logger
	inFlight chan struct{}

	mu        sync.Mutex
	lastAudit map[string]time.Time
}

// NewWeatherDelayEnforcer builds a [WeatherDelayEnforcer] against
// deps/opts, mirroring [NewNightLoop].
func NewWeatherDelayEnforcer(deps Dependencies, opts Options) *WeatherDelayEnforcer {
	deps = deps.withDefaults()
	opts = opts.withDefaults()
	return &WeatherDelayEnforcer{
		h:         &handlers{deps: deps, clock: opts.Clock, logger: opts.Logger},
		interval:  weatherDelayEnforceInterval,
		logger:    opts.Logger,
		inFlight:  make(chan struct{}, 1),
		lastAudit: map[string]time.Time{},
	}
}

// Run ticks until ctx is done, mirroring [NightLoop.Run]'s own
// non-blocking-mutex shape: a tick still running when the next one is due
// is left to finish, and the next tick is skipped rather than piled up
// behind it.
func (e *WeatherDelayEnforcer) Run(ctx context.Context) {
	ticker := time.NewTicker(e.interval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			e.inFlight <- struct{}{}
			return
		case <-ticker.C:
			select {
			case e.inFlight <- struct{}{}:
				go func() {
					defer func() { <-e.inFlight }()
					e.tick(ctx, e.h.now())
				}()
			default:
			}
		}
	}
}

// tick is the loop's own body, exported to this file only so a test can
// drive it directly rather than waiting out a real ticker, mirroring
// [handlers.nightTick]'s identical testing shape.
func (e *WeatherDelayEnforcer) tick(ctx context.Context, now time.Time) {
	h := e.h
	rec, err := h.deps.WeatherDelay.GetWeatherDelayState(ctx)
	if err != nil {
		h.logWarn("weather delay enforce: failed to read the stored state; holding every instance dark this tick as a precaution", "error", err)
		rec.Active = true
	}

	endpoints, err := currentFPPEndpoints(ctx, h.deps.FPP)
	if err != nil {
		h.logWarn("weather delay enforce: failed to list configured FPP instances", "error", err)
		endpoints = nil
	}

	var wg sync.WaitGroup
	for _, ep := range endpoints {
		wg.Add(1)
		go func(ep config.FPPEndpoint) {
			defer wg.Done()
			e.tickInstance(ctx, now, ep, rec)
		}(ep)
	}
	wg.Wait()

	h.weatherDelayTickPowerGroups(ctx, now, rec)
}

// tickInstance runs one FPP instance's own stop/gate work, bounded by
// [weatherDelayEnforceInstanceTimeout] so it can never delay another
// instance's tick.
func (e *WeatherDelayEnforcer) tickInstance(ctx context.Context, now time.Time, ep config.FPPEndpoint, rec store.WeatherDelayStateRecord) {
	h := e.h
	instCtx, cancel := context.WithTimeout(ctx, weatherDelayEnforceInstanceTimeout)
	defer cancel()

	client, err := fppcommand.New(ep.URL, fppcommand.Options{Timeout: weatherDelayFPPClientTimeout})
	if err != nil {
		h.logWarn("weather delay enforce: failed to build the fpp client", "instanceId", ep.ID, "error", err)
		return
	}

	var stopped, closed bool

	if rec.Active && h.weatherDelayFPPShouldStop(instCtx, ep.ID, now) {
		if _, err := client.StopPlaylist(instCtx); err != nil {
			h.logWarn("weather delay enforce: stop failed", "instanceId", ep.ID, "error", err)
		} else {
			stopped = true
		}
	}

	gateOutcome, gateErr := client.ReadWeatherGate(instCtx)
	unsupported := errors.Is(gateErr, fppcommand.ErrWeatherGateUnsupported)
	if gateErr == nil {
		h.deps.WeatherDelayGateCache.setGate(ep.ID, weatherDelayGateStatus{
			closed: gateOutcome.Closed, supported: true,
			effectiveOutputPercent: gateOutcome.EffectiveOutputPercent, observedAt: now,
		})
	} else if unsupported {
		h.deps.WeatherDelayGateCache.setGate(ep.ID, weatherDelayGateStatus{supported: false, observedAt: now})
	}

	switch {
	case rec.Active:
		if unsupported {
			break
		}
		if gateErr != nil || !gateOutcome.Closed {
			setOutcome, err := client.SetWeatherGate(instCtx, true, rec.Revision)
			if err != nil {
				h.logWarn("weather delay enforce: close failed", "instanceId", ep.ID, "error", err)
			} else {
				closed = true
				h.deps.WeatherDelayGateCache.setGate(ep.ID, weatherDelayGateStatus{
					closed: setOutcome.Closed, supported: true,
					effectiveOutputPercent: setOutcome.EffectiveOutputPercent, observedAt: now,
				})
			}
		}
	default:
		if gateErr == nil && gateOutcome.Closed && h.deps.WeatherDelayGateCache.reopenDue(ep.ID, now, weatherDelayStaleGateReopenInterval) {
			if setOutcome, err := client.SetWeatherGate(instCtx, false, rec.Revision); err != nil {
				h.logWarn("weather delay enforce: reopening a gate a delay never told this instance to open failed", "instanceId", ep.ID, "error", err)
			} else {
				h.deps.WeatherDelayGateCache.setGate(ep.ID, weatherDelayGateStatus{
					closed: setOutcome.Closed, supported: true,
					effectiveOutputPercent: setOutcome.EffectiveOutputPercent, observedAt: now,
				})
			}
		}
	}

	if stopped || closed {
		e.auditEnforce(ctx, now, ep.ID, stopped, closed)
	}
}

// weatherDelayFPPShouldStop reports whether ep's latest FPP observation
// says a playlist is playing, or the status is not current (unknown or
// stale) — ADR-053 decision 4's own "sends Stop Now to any FPP instance it
// observes playing" plus the safe default for evidence it cannot trust.
func (h *handlers) weatherDelayFPPShouldStop(ctx context.Context, instanceID string, now time.Time) bool {
	value, _, current, _, _ := resolveConfirmationEvidence(ctx, h.deps.Observations, instanceID, fppStatusSignal, time.Time{}, now)
	if !current {
		return true
	}
	status, _ := value.(string)
	return status == fppStatusValuePlaying || status == fppStatusValueUnknown
}

// auditEnforce records show.weatherdelay.enforce, rate limited to at most
// one per instance per minute, and only when this tick actually re-sent a
// stop or a close.
func (e *WeatherDelayEnforcer) auditEnforce(ctx context.Context, now time.Time, instanceID string, stopped, closed bool) {
	e.mu.Lock()
	last, ok := e.lastAudit[instanceID]
	if ok && now.Sub(last) < weatherDelayEnforceAuditInterval {
		e.mu.Unlock()
		return
	}
	e.lastAudit[instanceID] = now
	e.mu.Unlock()

	reason := "closed the gate"
	switch {
	case stopped && closed:
		reason = "sent Stop Now and closed the gate"
	case stopped:
		reason = "sent Stop Now"
	}
	if err := e.h.deps.Identity.WriteAudit(ctx, identity.AuditEntry{
		Timestamp: now, PrincipalID: weatherDelayEnforceSystemPrincipalID(instanceID),
		PrincipalName: "weather delay enforcement", Action: identity.AuditActionShowWeatherDelayEnforce,
		Target: instanceID, Kind: identity.AuditOutcome, Outcome: outcomeWordConfirmed, OutcomeReason: reason,
		Params: map[string]any{"stopped": stopped, "closedGate": closed},
	}); err != nil {
		e.h.logWarn("weather delay enforce: failed to write the audit entry", "instanceId", instanceID, "error", err)
	}
}

// weatherDelayEnforceSystemPrincipalID attributes an autonomous tick's own
// action to a stable, clearly-labeled identity, mirroring
// [cueActivationSystemPrincipalID]'s identical reasoning.
func weatherDelayEnforceSystemPrincipalID(instanceID string) string {
	return "system:weather-delay-enforce:" + instanceID
}

// --- power groups: dark confirmation and the per-group dark heartbeat ---

// weatherDelayTickPowerGroups computes every configured power group's own
// confirmedDark, records it (so GET /api/v1/weather-delay and the next
// tick agree), emits weatherDelay.changed on a flip, and publishes the
// dark heartbeat for a group that is active, confirmed dark, and has its
// own heartbeat enabled.
func (h *handlers) weatherDelayTickPowerGroups(ctx context.Context, now time.Time, rec store.WeatherDelayStateRecord) {
	payload, _, _, _, err := resolveWeatherDelayConfig(ctx, h.deps.Config)
	if err != nil || len(payload.PowerGroups) == 0 {
		return
	}
	for _, group := range payload.PowerGroups {
		dark, _ := h.weatherDelayGroupDarkness(ctx, now, group)
		_, changed := h.deps.WeatherDelayGateCache.recordGroupDark(group.ID, dark, now)
		if changed {
			h.appendWeatherDelayChangedEvent(ctx, now, fmt.Sprintf("power group %q confirmed dark: %v", group.ID, dark))
		}
		if rec.Active && dark && group.Heartbeat.Enabled {
			h.weatherDelayPublishGroupHeartbeat(ctx, now, group)
		}
	}
}

// weatherDelayPublishGroupHeartbeat publishes at most once per
// group.Heartbeat.IntervalSeconds, tracked on the cache so the interval
// survives across ticks.
func (h *handlers) weatherDelayPublishGroupHeartbeat(ctx context.Context, now time.Time, group config.WeatherDelayPowerGroupPayload) {
	if !h.deps.WeatherDelayGateCache.heartbeatDue(group.ID, now, time.Duration(group.Heartbeat.IntervalSeconds)*time.Second) {
		return
	}
	topic, err := mqttproto.WeatherDelayDarkTopic(group.ID)
	if err != nil {
		h.logWarn("weather delay enforce: invalid power group id for its dark heartbeat topic", "groupId", group.ID, "error", err)
		return
	}
	msg, err := mqttproto.NewWeatherDelayDarkMessage(group.ID, true, now)
	if err != nil {
		h.logWarn("weather delay enforce: failed to build the dark heartbeat message", "groupId", group.ID, "error", err)
		return
	}
	payload, err := mqttproto.EncodeWeatherDelayDarkMessage(msg)
	if err != nil {
		h.logWarn("weather delay enforce: failed to encode the dark heartbeat message", "groupId", group.ID, "error", err)
		return
	}
	pubCtx, cancel := context.WithTimeout(ctx, weatherDelayRetainedPublishTimeout)
	defer cancel()
	if err := h.deps.WeatherDelayPublisher.Publish(pubCtx, topic, mqttproto.WeatherDelayDarkDeliveryPolicy.QoS, mqttproto.WeatherDelayDarkDeliveryPolicy.Retain, payload); err != nil {
		h.logWarn("weather delay enforce: failed to publish the dark heartbeat", "groupId", group.ID, "error", err)
	}
}

// weatherDelayGroupDarkness computes one power group's own confirmedDark
// and per-member detail (ADR-053 decision 10). A member with no fresh
// observation is never dark.
func (h *handlers) weatherDelayGroupDarkness(ctx context.Context, now time.Time, group config.WeatherDelayPowerGroupPayload) (bool, []v1.WeatherDelayPowerGroupMember) {
	var members []v1.WeatherDelayPowerGroupMember
	allDark := true

	for _, id := range group.FPPInstanceIDs {
		dark, reason := h.weatherDelayFPPMemberDark(ctx, now, id)
		members = append(members, v1.WeatherDelayPowerGroupMember{Kind: v1.EmergencyStopTargetKindFPP, ID: id, Dark: dark, Reason: reason})
		allDark = allDark && dark
	}
	for _, id := range group.ResolumeInstanceIDs {
		dark, reason := h.weatherDelayResolumeMemberDark(ctx, now, id)
		members = append(members, v1.WeatherDelayPowerGroupMember{Kind: v1.EmergencyStopTargetKindResolume, ID: id, Dark: dark, Reason: reason})
		allDark = allDark && dark
	}
	for _, id := range group.RenderNodeIDs {
		dark, reason := h.weatherDelayRenderMemberDark(ctx, now, id)
		members = append(members, v1.WeatherDelayPowerGroupMember{Kind: v1.EmergencyStopTargetKindRender, ID: id, Dark: dark, Reason: reason})
		allDark = allDark && dark
	}
	if len(members) == 0 {
		allDark = false
	}
	return allDark, members
}

func (h *handlers) weatherDelayFPPMemberDark(ctx context.Context, now time.Time, instanceID string) (bool, string) {
	gate, ok := h.deps.WeatherDelayGateCache.getGate(instanceID)
	if !ok {
		return false, fmt.Sprintf("Player %s has not reported a gate reading yet.", instanceID)
	}
	if !gate.supported {
		return false, fmt.Sprintf("Player %s cannot be held dark by ShowMesh.", instanceID)
	}
	if now.Sub(gate.observedAt) > weatherDelayGateFreshnessWindow {
		return false, fmt.Sprintf("Player %s's last gate reading is too old to trust.", instanceID)
	}
	if !gate.closed || gate.effectiveOutputPercent != 0 {
		return false, fmt.Sprintf("Player %s is not reporting zero output.", instanceID)
	}
	value, _, current, _, _ := resolveConfirmationEvidence(ctx, h.deps.Observations, instanceID, fppStatusSignal, time.Time{}, now)
	status, _ := value.(string)
	if !current || status != fppStatusValueIdle {
		return false, fmt.Sprintf("Player %s is still playing.", instanceID)
	}
	return true, ""
}

// weatherDelayResolumeLayerActiveClipNone is this package's own copy of
// internal/coordinator/collector/resolume's identical unexported value
// (its own doc comment explains why it is a value, not an absence): two
// packages agreeing today because they share a literal are not proven to
// still agree once one of them changes, only asserted to.
const weatherDelayResolumeLayerActiveClipNone = "none: no clip is connected on this layer"

func (h *handlers) weatherDelayResolumeMemberDark(ctx context.Context, now time.Time, instanceID string) (bool, string) {
	kind := observation.ResourceResolume
	obs, err := h.deps.Observations.ListObservations(ctx, ObservationFilter{ResourceKind: &kind, ResourceID: &instanceID})
	if err != nil {
		return false, fmt.Sprintf("Resolume instance %s could not be read.", instanceID)
	}
	var layerSignals []observation.Observation
	for _, o := range obs {
		if isResolumeActiveClipSignal(o.Signal) {
			layerSignals = append(layerSignals, o)
		}
	}
	if len(layerSignals) == 0 {
		return false, fmt.Sprintf("Resolume instance %s has not reported any layers yet.", instanceID)
	}
	for _, o := range ResolveObservations(layerSignals) {
		value, _ := o.Value.(string)
		if o.StateAt(now) != observation.StateCurrent || value != weatherDelayResolumeLayerActiveClipNone {
			return false, fmt.Sprintf("Resolume instance %s is still showing content on a tracked layer.", instanceID)
		}
	}
	return true, ""
}

// isResolumeActiveClipSignal reports whether sig is one of
// "resolume.layer.<id>.active_clip" — this package's own copy of the
// signal shape internal/coordinator/collector/resolume mints, not an
// import of it (this file's own doc comment on the identical reasoning
// one function up).
func isResolumeActiveClipSignal(sig observation.SignalID) bool {
	s := string(sig)
	return len(s) > len("resolume.layer..active_clip") &&
		s[:len("resolume.layer.")] == "resolume.layer." && s[len(s)-len(".active_clip"):] == ".active_clip"
}

// weatherDelaySurfaceOutputModeSignal is "surface.output.mode"'s own
// literal, copied rather than imported from
// internal/coordinator/collector/noderender for the identical reason
// [fppStatusSignal]'s doc comment gives.
const weatherDelaySurfaceOutputModeSignal = "surface.output.mode"

// weatherDelaySurfaceOutputModeIdle is that signal's "cleared" value.
const weatherDelaySurfaceOutputModeIdle = "idle"

func (h *handlers) weatherDelayRenderMemberDark(ctx context.Context, now time.Time, surfaceID string) (bool, string) {
	value, _, current, _, _ := resolveConfirmationEvidence(ctx, h.deps.Observations, surfaceID, weatherDelaySurfaceOutputModeSignal, time.Time{}, now)
	status, _ := value.(string)
	if !current || status != weatherDelaySurfaceOutputModeIdle {
		return false, fmt.Sprintf("Render surface %s has not reported cleared output yet.", surfaceID)
	}
	return true, ""
}

// --- weather gate close/open, dispatched from start and resume ---

// weatherDelayCloseGatesOnAllInstances closes the gate on every configured
// FPP instance, concurrently. Called from the start handler's own
// concurrent fan-out, never awaited by the alert dispatch.
func (h *handlers) weatherDelayCloseGatesOnAllInstances(ctx context.Context, now time.Time, revision int64) []v1.WeatherDelayTargetOutcome {
	return h.weatherDelaySetGatesOnAllInstances(ctx, now, true, revision)
}

// weatherDelayOpenGatesOnAllInstances opens the gate on every configured
// FPP instance at the new state revision, concurrently. Called from the
// resume handler.
func (h *handlers) weatherDelayOpenGatesOnAllInstances(ctx context.Context, now time.Time, revision int64) []v1.WeatherDelayTargetOutcome {
	return h.weatherDelaySetGatesOnAllInstances(ctx, now, false, revision)
}

func (h *handlers) weatherDelaySetGatesOnAllInstances(ctx context.Context, now time.Time, closed bool, revision int64) []v1.WeatherDelayTargetOutcome {
	endpoints, err := currentFPPEndpoints(ctx, h.deps.FPP)
	if err != nil {
		h.logWarn("weather delay: failed to list configured FPP instances; no gate could be dispatched", "error", err)
		return []v1.WeatherDelayTargetOutcome{{
			InstanceID: emergencyStopInstanceListUnavailableID, TargetKind: v1.WeatherDelayTargetKindGate,
			Outcome: "failed", OutcomeReason: fmt.Sprintf("could not list configured FPP instances: %v", err),
		}}
	}
	if len(endpoints) == 0 {
		return []v1.WeatherDelayTargetOutcome{}
	}

	out := make([]v1.WeatherDelayTargetOutcome, len(endpoints))
	var wg sync.WaitGroup
	for i, ep := range endpoints {
		wg.Add(1)
		go func(i int, ep config.FPPEndpoint) {
			defer wg.Done()
			out[i] = h.weatherDelaySetOneGate(ctx, now, ep, closed, revision)
		}(i, ep)
	}
	wg.Wait()
	return out
}

func (h *handlers) weatherDelaySetOneGate(ctx context.Context, now time.Time, ep config.FPPEndpoint, closed bool, revision int64) v1.WeatherDelayTargetOutcome {
	reqCtx, cancel := context.WithTimeout(ctx, weatherDelayFPPClientTimeout)
	defer cancel()

	client, err := fppcommand.New(ep.URL, fppcommand.Options{Timeout: weatherDelayFPPClientTimeout})
	if err != nil {
		return v1.WeatherDelayTargetOutcome{InstanceID: ep.ID, TargetKind: v1.WeatherDelayTargetKindGate, Outcome: "failed", OutcomeReason: err.Error()}
	}
	outcome, err := client.SetWeatherGate(reqCtx, closed, revision)
	dispatchedAt := formatTime(now)
	if err != nil {
		if errors.Is(err, fppcommand.ErrWeatherGateUnsupported) {
			h.deps.WeatherDelayGateCache.setGate(ep.ID, weatherDelayGateStatus{supported: false, observedAt: now})
			return v1.WeatherDelayTargetOutcome{InstanceID: ep.ID, TargetKind: v1.WeatherDelayTargetKindGate, Outcome: "refused", OutcomeReason: "this player cannot be held dark by ShowMesh", DispatchedAt: &dispatchedAt}
		}
		return v1.WeatherDelayTargetOutcome{InstanceID: ep.ID, TargetKind: v1.WeatherDelayTargetKindGate, Outcome: "failed", OutcomeReason: err.Error(), DispatchedAt: &dispatchedAt}
	}
	h.deps.WeatherDelayGateCache.setGate(ep.ID, weatherDelayGateStatus{
		closed: outcome.Closed, supported: true, effectiveOutputPercent: outcome.EffectiveOutputPercent, observedAt: now,
	})
	reason := fmt.Sprintf("gate closed=%v, effective output %d%%", outcome.Closed, outcome.EffectiveOutputPercent)
	return v1.WeatherDelayTargetOutcome{InstanceID: ep.ID, TargetKind: v1.WeatherDelayTargetKindGate, Outcome: "confirmed", OutcomeReason: reason, DispatchedAt: &dispatchedAt}
}
