package coordinator

import (
	"context"
	"errors"
	"log/slog"
	"sync"
	"time"

	"github.com/showmeshsystems/showmesh/internal/coordinator/config"
	"github.com/showmeshsystems/showmesh/internal/coordinator/store"
	"github.com/showmeshsystems/showmesh/pkg/mqttproto"
)

// This file makes ADR-033's installation-wide operating mode LIVE, on
// fppendpoints.go's shape and for ADR-036's reason: a value that decides
// what a subsystem does is read from the active revision at the point of
// decision, never captured into a field at process start.
//
// Two things read the mode in this build, and only two. The Resolume
// WebSocket footprint switch (ADR-033 decision 2's named first consumer),
// and nodes, which are told the current value so a later seam can read it
// where it decides something. FPP poll cadence, the audio device-loss
// policy and the asset sync timer are named in ADR-033's Context as future
// consumers and are deliberately untouched here.
//
// Nothing in this file may gate a command. ADR-033 decision 4 is the
// non-negotiable clause: no mode may refuse, delay, or degrade blackout,
// stop, or power-off. The applier writes an atomic bool and publishes a
// retained message; it sits on no dispatch path and returns nothing any
// command path waits on.

// showModeReconcileInterval is how often [runShowMode] re-reads the active
// show.mode revision, applies it, and republishes it to nodes.
//
// Five seconds, chosen against two deadlines rather than picked for feeling
// small. Downstream, the Resolume watcher supervisor re-checks its own
// atomic every [resolumeWebSocketSupervisorInterval] (two seconds), so an
// operator flipping the mode sees the WebSocket follow within about seven
// seconds worst case. Upstream, this same tick is what keeps a node's
// held-value freshness alive: a node treats the mode as held rather than
// current once it has gone [agent.ShowModeFreshnessWindow] without a
// message, and that window is a multiple of this interval, so an ordinary
// tick is never mistaken for a coordinator that has gone away.
const showModeReconcileInterval = 5 * time.Second

// showModeProvider is what a reader of the mode actually needs: the current
// value, on demand. Declared at the consumer, matching this package's
// convention, so a test can supply a fixed mode without a store.
type showModeProvider interface {
	Current(ctx context.Context) showModeValue
}

// showModeValue is one resolved answer about the mode: the value, the
// revision it came from, and whether that answer is stale (the product of a
// failed read rather than a successful one).
type showModeValue struct {
	Mode     string
	Revision int64

	// Stale is true when the store could not be read and this value is the
	// last one that WAS read, or the conservative fallback described in
	// [showModeSource.Current]. ADR-036 decision 4: a failed configuration
	// read returns the last known value and says so.
	Stale bool
}

// showModeSource resolves the currently active show.mode configuration on
// demand, caching the decoded value against the revision number it came
// from so the common case is one indexed row read rather than a decode.
//
// There is deliberately no refresh method and no invalidation call, for
// [fppEndpointSource]'s reason: a caller cannot forget to invalidate
// something it never has to.
type showModeSource struct {
	st     *store.Store
	logger *slog.Logger

	mu       sync.Mutex
	revision int64
	cached   string
	loaded   bool
}

var _ showModeProvider = (*showModeSource)(nil)

func newShowModeSource(st *store.Store, logger *slog.Logger) *showModeSource {
	return &showModeSource{st: st, logger: logger}
}

// Current returns the active mode.
//
// Three outcomes, and keeping them apart is the whole point of this
// function:
//
//   - No show.mode object has ever been written. That is a normal state, not
//     a failure, and the answer is [config.ShowModeDefault] ("program") at
//     revision 0. A fresh install is by definition being set up.
//
//   - The store was read. The answer is the stored mode at its revision.
//
//   - The store could not be read. The last successfully read value is
//     returned with Stale set, and the failure is logged. A transient read
//     failure must not silently change the installation's operating mode,
//     which is [fppEndpointSource.Current]'s argument applied to a value
//     where manufacturing an answer would change live behaviour rather than
//     empty a list.
//
// With no successful read to fall back on, the conservative answer is
// [config.ShowModeShow], NOT the fresh-install default. Those are different
// questions: "nothing has ever been set" is program, and "this coordinator
// cannot read its own store" is the case ADR-033 decision 5 already rules
// on, where a mode that cannot be read behaves as show. Publishing program
// out of a store failure would be the coordinator asserting the LESS
// conservative side from no evidence at all.
func (s *showModeSource) Current(ctx context.Context) showModeValue {
	s.mu.Lock()
	defer s.mu.Unlock()

	obj, err := s.st.GetConfigObject(ctx, config.ShowModeConfigKind, config.ShowModeConfigObjectID)
	switch {
	case errors.Is(err, store.ErrConfigObjectNotFound):
		// Never configured is a normal state, not an error worth logging on
		// every tick.
		s.revision, s.cached, s.loaded = 0, config.ShowModeDefault, true
		return showModeValue{Mode: config.ShowModeDefault, Revision: 0}
	case err != nil:
		return s.staleLocked("failed to read the active show.mode configuration; continuing with the last known mode", err)
	}
	if obj.CurrentRevision == 0 {
		s.revision, s.cached, s.loaded = 0, config.ShowModeDefault, true
		return showModeValue{Mode: config.ShowModeDefault, Revision: 0}
	}
	if s.loaded && obj.CurrentRevision == s.revision {
		return showModeValue{Mode: s.cached, Revision: s.revision}
	}

	rev, err := s.st.GetConfigRevision(ctx, config.ShowModeConfigKind, config.ShowModeConfigObjectID, obj.CurrentRevision)
	if err != nil {
		return s.staleLocked("failed to read the active show.mode revision; continuing with the last known mode", err)
	}
	payload, verr := config.DecodeShowModePayload(rev.PayloadJSON)
	if verr != nil {
		return s.staleLocked("failed to decode the active show.mode revision; continuing with the last known mode",
			errors.New(verr.Error()))
	}

	s.revision, s.cached, s.loaded = obj.CurrentRevision, payload.Mode, true
	return showModeValue{Mode: payload.Mode, Revision: obj.CurrentRevision}
}

// staleLocked is Current's failure path. The caller must hold s.mu.
func (s *showModeSource) staleLocked(msg string, err error) showModeValue {
	if s.logger != nil {
		s.logger.Warn(msg, "error", err)
	}
	if !s.loaded {
		if s.logger != nil {
			s.logger.Warn("no show.mode value has ever been read from the store; treating the mode as show, "+
				"the conservative side (ADR-033 decision 5)", "mode", config.ShowModeShow)
		}
		return showModeValue{Mode: config.ShowModeShow, Revision: 0, Stale: true}
	}
	return showModeValue{Mode: s.cached, Revision: s.revision, Stale: true}
}

// showModeFootprint is the part of [resolume.FootprintControls] the mode
// drives. Declared at the consumer so a test can observe the applied value
// without a Resolume collector, and narrowed to the ONE setter the mode is
// allowed to touch: SHOWMESH_RESOLUME_POLL_INTERVAL stays a startup-only
// debug override and this build does not let the mode move it.
type showModeFootprint interface {
	SetWebSocketEnabled(enabled bool)
}

// showModePublisher is this file's MQTT publish dependency.
// *broker.BrokerManager already satisfies it with no adapter, matching
// audioconfigpush.Publisher's identical shape.
type showModePublisher interface {
	Publish(ctx context.Context, topic string, qos byte, retain bool, payload []byte) error
}

// runShowMode is the mode's one applier loop: on every tick it resolves the
// active mode and makes both of this build's consumers agree with it.
//
// footprint may be nil (no Resolume instance is configured), and pub may be
// nil (no broker). Either one missing disables that half and leaves the
// other working, rather than disabling the loop.
//
// BOTH DIRECTIONS ARE LIVE, and that is not symmetry for its own sake:
// entering show closes the Resolume WebSocket and returning to program
// reopens it, with no restart in either direction. A half that only closed
// would leave an operator who fixed something at 17:00 with a footprint
// switch they cannot undo without restarting the coordinator, which is the
// failure ADR-036 decision 2 argues at length.
//
// The first pass runs immediately rather than after one tick, so a
// coordinator that starts in show mode is not briefly in program.
func runShowMode(
	ctx context.Context,
	source showModeProvider,
	footprint showModeFootprint,
	pub showModePublisher,
	now func() time.Time,
	logger *slog.Logger,
	interval time.Duration,
) {
	ticker := time.NewTicker(interval)
	defer ticker.Stop()

	var (
		applied     string
		haveApplied bool
	)
	pass := func() {
		v := source.Current(ctx)

		if footprint != nil && (!haveApplied || v.Mode != applied) {
			// ADR-033 decision 2: held open in program, closed in show.
			footprint.SetWebSocketEnabled(v.Mode == config.ShowModeProgram)
			if logger != nil {
				// ADR-033 decision 3: the behaviour names the mode as its
				// reason, in the place an operator reading logs will look.
				logger.Info("show mode applied to the Resolume WebSocket footprint",
					"mode", v.Mode, "revision", v.Revision, "stale", v.Stale,
					"websocket_enabled", v.Mode == config.ShowModeProgram,
					"reason", "show mode is "+v.Mode)
			}
			applied, haveApplied = v.Mode, true
		}

		if pub != nil {
			publishShowMode(ctx, pub, v, now, logger)
		}
	}

	pass()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			pass()
		}
	}
}

// publishShowMode republishes the retained installation-wide mode. It is
// best effort by construction: a publish that fails is logged and retried on
// the next tick, and nothing waits on it. A node that misses this message
// keeps the mode it already held (ADR-033 decision 5) rather than falling
// back to anything.
//
// A stale value is published exactly like a fresh one. The alternative,
// withholding the mode while the store is unreadable, would let a node's
// freshness window expire and flip it to "held" for a reason that has
// nothing to do with the coordinator being unreachable, which is a worse
// lie than republishing the last known value.
func publishShowMode(ctx context.Context, pub showModePublisher, v showModeValue, now func() time.Time, logger *slog.Logger) {
	msg, err := mqttproto.NewShowModeMessage(v.Mode, v.Revision, now())
	if err != nil {
		if logger != nil {
			logger.Error("refusing to publish an invalid show mode message", "mode", v.Mode, "error", err)
		}
		return
	}
	payload, err := mqttproto.EncodeShowModeMessage(msg)
	if err != nil {
		if logger != nil {
			logger.Error("failed to encode the show mode message", "mode", v.Mode, "error", err)
		}
		return
	}
	if err := pub.Publish(ctx, mqttproto.ShowModeTopic(), mqttproto.ShowModeDeliveryPolicy.QoS,
		mqttproto.ShowModeDeliveryPolicy.Retain, payload); err != nil {
		if logger != nil {
			logger.Warn("failed to publish the show mode to nodes; nodes keep the mode they already hold",
				"mode", v.Mode, "topic", mqttproto.ShowModeTopic(), "error", err)
		}
	}
}
