package api

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"strings"
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

// The enforcement loop only stops playlists and closes gates; it never opens
// one. No configuration value, mode, scope or flag may disable, slow or
// exempt a player. Do not add one.

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

// weatherDelayIdleGateReadInterval is how often a gate is read while no
// delay is active, only to report a player still held dark.
const weatherDelayIdleGateReadInterval = 30 * time.Second

// weatherDelayGateFreshnessWindow is the oldest gate reading a power group
// may count as dark: three ticks, so one skipped tick never flips a group.
const weatherDelayGateFreshnessWindow = 15 * time.Second

// weatherDelayIdleGateFreshnessWindow is the same three-read allowance for a
// reading taken at the idle interval.
const weatherDelayIdleGateFreshnessWindow = 3 * weatherDelayIdleGateReadInterval

// weatherDelayFPPClientTimeout bounds each FPP request, shorter than the
// per-instance timeout. A var so a test can drive it down.
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
	lastState store.WeatherDelayStateRecord
	lastHeld  string
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

// Run ticks until ctx is done. A tick still running when the next is due is
// left to finish and the next one is skipped, so ticks never pile up.
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
					defer e.recoverPanic("tick")
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
	rec, _ := e.state(ctx)

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
			defer e.recoverPanic(ep.ID)
			e.tickInstance(ctx, now, ep, rec)
		}(ep)
	}
	wg.Wait()

	rec, _ = e.state(ctx)
	h.weatherDelayTickPowerGroups(ctx, now, rec)
	e.reportHeldPlayers(ctx, now, rec.Active)
}

// reportHeldPlayers records weatherDelay.changed when the set of players
// held dark with no delay active changes.
func (e *WeatherDelayEnforcer) reportHeldPlayers(ctx context.Context, now time.Time, active bool) {
	var ids []string
	for _, p := range e.h.weatherDelayHeldPlayers(ctx, now, active) {
		ids = append(ids, p.InstanceID)
	}
	key := strings.Join(ids, ",")
	e.mu.Lock()
	changed := key != e.lastHeld
	e.lastHeld = key
	e.mu.Unlock()
	if changed {
		e.h.appendWeatherDelayChangedEvent(ctx, now, fmt.Sprintf("players held dark with no delay active: [%s]", key))
	}
}

// state reads the weather delay state. A failed read falls back to the
// last state this process read, so a delay it knew active stays enforced
// and a store outage outside a delay stops nothing. fresh is false then.
func (e *WeatherDelayEnforcer) state(ctx context.Context) (rec store.WeatherDelayStateRecord, fresh bool) {
	rec, err := e.h.deps.WeatherDelay.GetWeatherDelayState(ctx)
	e.mu.Lock()
	defer e.mu.Unlock()
	if err == nil {
		e.lastState = rec
		return rec, true
	}
	e.h.logWarn("weather delay enforce: failed to read the stored state; acting on the last state read", "lastActive", e.lastState.Active, "error", err)
	if e.lastState.Active {
		return e.lastState, false
	}
	return store.WeatherDelayStateRecord{}, false
}

func (e *WeatherDelayEnforcer) recoverPanic(scope string) {
	if r := recover(); r != nil {
		e.h.logWarn("weather delay enforce: pass panicked; recovered", "scope", scope, "panic", fmt.Sprintf("%v", r))
	}
}

// tickInstance runs one FPP instance's own stop/gate work, bounded by
// [weatherDelayEnforceInstanceTimeout] so it can never delay another
// instance's tick.
func (e *WeatherDelayEnforcer) tickInstance(ctx context.Context, now time.Time, ep config.FPPEndpoint, rec store.WeatherDelayStateRecord) {
	h := e.h
	freshFor := weatherDelayGateFreshnessWindow
	if !rec.Active {
		if !h.deps.WeatherDelayGateCache.idleReadDue(ep.ID, now, weatherDelayIdleGateReadInterval) {
			return
		}
		freshFor = weatherDelayIdleGateFreshnessWindow
	}
	instCtx, cancel := context.WithTimeout(ctx, weatherDelayEnforceInstanceTimeout)
	defer cancel()

	client, err := fppcommand.New(ep.URL, fppcommand.Options{Timeout: weatherDelayFPPClientTimeout})
	if err != nil {
		h.logWarn("weather delay enforce: failed to build the fpp client", "instanceId", ep.ID, "error", err)
		return
	}

	var stopped, closed bool

	if rec.Active && h.weatherDelayFPPShouldStop(instCtx, ep.ID, now) {
		if cur, _ := e.state(instCtx); !cur.Active {
			return
		}
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
			effectiveOutputPercent: gateOutcome.EffectiveOutputPercent, observedAt: now, freshFor: freshFor,
		})
	} else if unsupported {
		h.deps.WeatherDelayGateCache.setGate(ep.ID, weatherDelayGateStatus{supported: false, observedAt: now, freshFor: freshFor})
	} else {
		h.deps.WeatherDelayGateCache.setGate(ep.ID, weatherDelayGateStatus{supported: true, unreadable: true, observedAt: now, freshFor: freshFor})
	}

	if rec.Active && !unsupported {
		if gateErr != nil || !gateOutcome.Closed {
			unlock := h.deps.WeatherDelayGateCache.lockGateWrite(ep.ID)
			defer unlock()
			if cur, _ := e.state(instCtx); cur.Active {
				setOutcome, err := client.SetWeatherGate(instCtx, true, cur.Revision)
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
		}
	}

	if stopped || closed {
		e.auditEnforce(ctx, now, ep.ID, stopped, closed)
	}
}

// weatherDelayFPPShouldStop reports whether ep's latest FPP observation is
// anything but a current idle status: playing, paused, still finishing a
// loop, unknown or stale all get Stop Now.
func (h *handlers) weatherDelayFPPShouldStop(ctx context.Context, instanceID string, now time.Time) bool {
	value, _, current, _, _ := resolveConfirmationEvidence(ctx, h.deps.Observations, instanceID, fppStatusSignal, time.Time{}, now)
	if !current {
		return true
	}
	status, _ := value.(string)
	return status != fppStatusValueIdle
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

// weatherDelayTickPowerGroups records each power group's darkness, emits
// weatherDelay.changed on a flip, and publishes the dark heartbeat only for
// a group that is dark, has it enabled, and while the delay is active.
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
	if gate.unreadable {
		return false, fmt.Sprintf("Player %s's gate could not be read.", instanceID)
	}
	if now.Sub(gate.observedAt) > gate.freshWindow() {
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

// weatherDelayResolumeLayerActiveClipNone copies the Resolume collector's
// unexported "no clip" value rather than importing it.
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

// isResolumeActiveClipSignal reports whether sig is
// "resolume.layer.<id>.active_clip", as the Resolume collector mints it.
func isResolumeActiveClipSignal(sig observation.SignalID) bool {
	s := string(sig)
	return len(s) > len("resolume.layer..active_clip") &&
		s[:len("resolume.layer.")] == "resolume.layer." && s[len(s)-len(".active_clip"):] == ".active_clip"
}

// weatherDelaySurfaceOutputModeSignal copies the render collector's
// "surface.output.mode" literal rather than importing it.
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
	unlock := h.deps.WeatherDelayGateCache.lockGateWrite(ep.ID)
	defer unlock()
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
