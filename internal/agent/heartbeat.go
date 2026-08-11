package agent

import (
	"context"
	"encoding/json"
	"log/slog"
	"os"
	"time"

	"github.com/showmeshsystems/showmesh/pkg/mqttproto"
)

// envHeartbeatIntervalOverride is a TEST-SUPPORT-ONLY environment variable
// (Step 2 round 2 Task E) that lets the integration test harness in
// /test/integration compress this package's real heartbeat cadence, so a
// test that needs to observe several heartbeats (or a staleness timeout
// derived from them) does not have to wait out the production interval. It
// is read exactly once, at package initialization (see HeartbeatInterval
// below), so it can only take effect via the process environment the agent
// binary is started with — e.g. the integration harness exec'ing this
// agent as a subprocess with it set — never by calling code after startup.
// It must never become a documented production tuning surface: unset in
// every real deployment, it has no effect and HeartbeatInterval is exactly
// the 10 second value below.
const envHeartbeatIntervalOverride = "SHOWMESH_TEST_HEARTBEAT_INTERVAL"

// HeartbeatInterval is how often the agent publishes a health heartbeat.
//
// SHOWMESH HYPOTHESIS, NOT DERIVED FROM ADR-008 OR FROM ANY MEASUREMENT:
// per the round-2 shared liveness contract, 10 seconds, chosen so that
// three missed heartbeats land inside the coordinator's own (independently
// owned) 30 second health-staleness window. This package does not read, or
// assume anything about, that window: the window is the coordinator's
// policy, this interval is the agent's, and the shared contract is
// explicit that the coordinator must not hardcode an assumption that the
// two are equal. Expect this value to change once RES-009 failure testing
// produces real evidence, the way pkg/multisync/timeline.go labels its own
// ShowMesh-chosen thresholds as guesses rather than FPP-derived values.
//
// A package-level var, not a const, ONLY so [envHeartbeatIntervalOverride]
// can override it for integration tests; see that constant's doc comment
// for why this must not be read as an invitation to change it any other
// way.
var HeartbeatInterval = resolveHeartbeatInterval()

// resolveHeartbeatInterval returns the envHeartbeatIntervalOverride value
// when it is set to a valid positive duration, and the production default
// otherwise. Invalid or non-positive overrides are silently ignored in
// favor of the default rather than failing package initialization, since a
// malformed test-only environment variable must never be able to crash
// production startup.
func resolveHeartbeatInterval() time.Duration {
	const def = 10 * time.Second
	if raw := os.Getenv(envHeartbeatIntervalOverride); raw != "" {
		if d, err := time.ParseDuration(raw); err == nil && d > 0 {
			return d
		}
	}
	return def
}

// heartbeatPublishTimeout bounds a single heartbeat publish attempt so a
// hung publish cannot delay the next tick.
const heartbeatPublishTimeout = 5 * time.Second

// agentStateRunning is the only AgentState value this Step 2 agent ever
// reports: there is no GStreamer pipeline, no command handling, and no
// failure mode of its own yet for a "degraded" state to describe. See
// mqttproto.HealthPayload.AgentState's doc comment for why this package
// does not constrain the field to a fixed vocabulary.
const agentStateRunning = "running"

// runHeartbeat publishes a health heartbeat to nodeID's observed-health
// topic on every tick received from ticks, until ctx is done. now stamps
// each heartbeat's envelope SentAt and derives UptimeMS from startedAt.
// bootID is threaded through unchanged on every tick — see
// mqttproto.HealthPayload.BootID's doc comment for why a stable boot ID
// across a whole process lifetime, generated fresh only at process start,
// is what lets a restart be told apart from a continuous session.
//
// connected additionally triggers an immediate, out-of-cadence publish
// whenever a value arrives on it — see mqtt.go's OnConnectionUp, which
// signals it on every successful MQTT connect, including reconnects. Per
// the round-2 review: without this, a freshly (re)connected node publishes
// hello and its online LWT immediately but no heartbeat until the ticker
// first fires a full HeartbeatInterval later, so the coordinator's liveness
// rule (which requires a LIVE heartbeat, not just online=true — see the
// shared contract's "Liveness derivation") reads the node as `unknown` for
// up to one whole interval after it actually has evidence the node is
// there. connected is expected to be buffered so an OnConnectionUp that
// fires before this loop starts (or a burst of them) is not silently
// dropped; runHeartbeat only ever reads it, so it is the sole owner of seq
// and there is no data race between "trigger" and "tick" publishes.
//
// Sequence starts at 0 and increments once per publish attempt (tick- or
// connect-triggered) REGARDLESS of whether that attempt's publish
// succeeds: a publish failure must not wedge the loop or stop later
// publishes (a transient failure is not a reason to stop reporting — see
// the Task D spec), and it must not stall Sequence either, since Sequence
// exists so the coordinator can notice a gap in what actually reached it.
// A connect-triggered publish and a tick-triggered publish share the same
// counter and never fire concurrently (both are handled by this one
// goroutine's select loop), so ordering stays monotonic across both
// sources with no double-counting and no reused sequence number.
//
// runHeartbeat returns only when ctx is done; a publish failure never
// causes it to return early.
func runHeartbeat(ctx context.Context, pub Publisher, nodeID, bootID string, startedAt time.Time, now func() time.Time, ticks <-chan time.Time, connected <-chan struct{}, logger *slog.Logger) {
	topic, err := mqttproto.ObservedTopic(nodeID, "health")
	if err != nil {
		// nodeID is validated at config load (mqttproto.ValidateNodeID via
		// internal/agent/config.LoadConfig), so this should be
		// unreachable in production; fail loudly here rather than silently
		// never publishing a heartbeat if that invariant is ever violated.
		logger.Error("bug: could not build health topic for a validated node ID", "node_id", nodeID, "error", err)
		return
	}

	var seq uint64
	for {
		select {
		case <-ctx.Done():
			return
		case tickAt, ok := <-ticks:
			if !ok {
				return
			}
			publishOneHeartbeat(ctx, pub, topic, nodeID, bootID, seq, startedAt, tickAt, now, logger)
			seq++
		case _, ok := <-connected:
			if !ok {
				// A closed (rather than merely empty) connected channel is
				// only expected from tests exercising the tick-only path;
				// treat it as "no more connect triggers" rather than
				// spinning a busy loop reading a closed channel forever.
				connected = nil
				continue
			}
			// There is no natural "tick time" for a connect-triggered
			// publish; log the current time in its place, same as ticks
			// do for their own tickAt.
			publishOneHeartbeat(ctx, pub, topic, nodeID, bootID, seq, startedAt, now(), now, logger)
			seq++
		}
	}
}

// publishOneHeartbeat builds and publishes a single health heartbeat.
// tickAt is only used to log the tick that triggered this publish; the
// envelope's own SentAt (and the uptime derived from it) is stamped from
// now() at publish time, matching how every other envelope constructor in
// this package stamps its own send time rather than reusing a caller-
// supplied one.
func publishOneHeartbeat(ctx context.Context, pub Publisher, topic, nodeID, bootID string, seq uint64, startedAt, tickAt time.Time, now func() time.Time, logger *slog.Logger) {
	pubCtx, cancel := context.WithTimeout(ctx, heartbeatPublishTimeout)
	defer cancel()

	sentAt := now()
	env, err := mqttproto.NewHealthEnvelope(fixedClock(sentAt), nodeID, mqttproto.HealthPayload{
		BootID:     bootID,
		Sequence:   seq,
		AgentState: agentStateRunning,
		UptimeMS:   uptimeMS(startedAt, sentAt),
	})
	if err != nil {
		logger.Error("failed to build heartbeat envelope", "sequence", seq, "error", err)
		return
	}

	payload, err := json.Marshal(env)
	if err != nil {
		logger.Error("failed to marshal heartbeat envelope", "sequence", seq, "error", err)
		return
	}

	if err := pub.Publish(pubCtx, topic, mqttproto.ObservedDeliveryPolicy.QoS, mqttproto.ObservedDeliveryPolicy.Retain, payload); err != nil {
		logger.Warn("heartbeat publish failed; will retry next tick", "sequence", seq, "tick", tickAt, "error", err)
		return
	}

	logger.Debug("published heartbeat", "sequence", seq, "uptime_ms", uptimeMS(startedAt, sentAt))
}

// uptimeMS returns sentAt's elapsed time since startedAt in milliseconds,
// clamped to 0.
//
// This package stamps every envelope from an injected now func() time.Time
// (see [publishOneHeartbeat]) rather than always calling time.Now()
// directly, specifically so tests can drive it with a deterministic fake
// clock (see heartbeat_test.go's fakeClock) — which means there is no
// os-level monotonic reading available here to fall back on the way
// time.Since(startedAt) would give in production. A wall clock is not
// guaranteed to be monotonic (NTP step, manual adjustment, VM clock
// correction), so sentAt.Sub(startedAt) can legitimately go negative even
// though real elapsed time did not run backward. UptimeMS is documented
// (mqttproto.HealthPayload.UptimeMS) as this process's uptime; reporting a
// negative uptime is a more confusing failure mode for a consumer than
// reporting 0, so this clamps rather than propagating the negative value.
func uptimeMS(startedAt, sentAt time.Time) int64 {
	d := sentAt.Sub(startedAt)
	if d < 0 {
		return 0
	}
	return d.Milliseconds()
}

// fixedClock returns a clock function that always reports t, matching the
// helper pkg/mqttproto's own tests use for the same purpose.
func fixedClock(t time.Time) func() time.Time {
	return func() time.Time { return t }
}
