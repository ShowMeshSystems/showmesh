package agent

import (
	"context"
	"log/slog"
	"sync"
	"time"

	"github.com/eclipse/paho.golang/autopaho"
	"github.com/eclipse/paho.golang/paho"

	"github.com/showmeshsystems/showmesh/internal/agent/pipeline"
	"github.com/showmeshsystems/showmesh/pkg/mqttproto"
)

// This file is the node's half of ADR-033: it holds the installation-wide
// operating mode so any subsystem on this node can read it AT THE POINT OF
// DECISION.
//
// The renderer's frame writer is the first consumer on this node: it reads
// the holder on the frame a coverage-gap failure happens on, to decide
// whether that failure reaches the wall as an unmistakable alert field or
// as black (internal/agent/pipeline/frame.go).
//
// The shape is [resolume.FootprintControls]' shape, named by ADR-033 and by
// TRACK-D-D2-SPEC.md §3.3 as the pattern for this: an atomic-backed value
// with a setter, read fresh on every call, never captured into a field at
// construction. ADR-036 decision 1 is the rule it satisfies.
//
// THE AGENT DERIVES THE MODE FROM NOTHING. Not playback state, not the
// presence of an assignment, not whether a pipeline is running. The only
// input is a message from the coordinator on [mqttproto.ShowModeTopic].

// Compile-time check that a holder can be handed straight to a frame
// writer as its mode source (internal/agent/pipeline's ShowModeSource).
var _ pipeline.ShowModeSource = (*ShowModeHolder)(nil)

// ShowModeUnknown is what this node reports when it has never been told the
// mode. Per ADR-033 decision 5 it BEHAVES AS SHOW, which is what
// [ShowModeState.BehavesAsShow] answers; the value stays "unknown" rather
// than being silently rewritten to "show", because a caller that wants to
// log why it is being conservative needs to be able to tell "I was told
// show" from "nobody has told me anything".
const ShowModeUnknown = "unknown"

// ShowModeFreshnessWindow is how long a received mode stays "current"
// before this node reports it as HELD.
//
// Twenty seconds: the coordinator republishes the retained mode every five
// seconds (internal/coordinator's showModeReconcileInterval), so this is
// four consecutive missed republishes. Wide enough that an ordinary
// scheduling hiccup or one dropped message never reads as a coordinator
// that has gone away; narrow enough that an operator looking at a node
// after a real coordinator loss is told promptly that the value is held.
//
// SHOWMESH CHOICE, NOT MEASURED. It is a multiple of a publish interval,
// not a number derived from any observed failure.
const ShowModeFreshnessWindow = 20 * time.Second

// ShowModeState is one answer about the mode, at the moment it was asked.
type ShowModeState struct {
	// Mode is "program", "show", or [ShowModeUnknown].
	Mode string

	// Held is true when this value is the last one this node was told and
	// is no longer being confirmed: either the freshness window has passed
	// with no message, or nothing has ever arrived. ADR-033 decision 5: a
	// node that cannot reach the coordinator keeps the last mode it knew
	// and SAYS THE VALUE IS HELD RATHER THAN CURRENT. It never falls back
	// to a default, because reverting to program because the coordinator
	// went away would turn a coordinator outage into a live behaviour
	// change mid-show.
	Held bool

	// ReceivedAt is when this node received the value, zero if never.
	ReceivedAt time.Time

	// Revision is the coordinator's show.mode revision the value came from,
	// 0 when the coordinator was publishing its built-in default or when
	// nothing has ever been received. Informational only.
	Revision int64
}

// BehavesAsShow is the question every consumer should actually ask, because
// it folds ADR-033 decision 5's rule in one place instead of leaving each
// caller to remember it: unknown behaves as show. Show is the conservative
// side (smaller footprint, fewer edit surfaces), and per decision 4 nothing
// safety-critical is withheld by it, so erring here costs nothing an
// operator needs.
func (s ShowModeState) BehavesAsShow() bool {
	return s.Mode != mqttproto.ShowModeProgram
}

// ShowModeHolder holds this node's current view of the installation-wide
// operating mode. Safe for concurrent use; the zero value is ready to use
// and reports [ShowModeUnknown] as held.
//
// It is deliberately IN MEMORY ONLY. ADR-033 decision 5 also says the mode
// is part of ADR-025's signed agent fallback cache, so that a node keeps it
// across a restart. That cache does not exist yet, in this repository or
// anywhere else, and this type does not pretend otherwise: a restarted
// agent reads unknown, which behaves as show. Building an unsigned cache
// here would be strictly worse than having none, because ADR-025's whole
// point is that a cache that fails verification leaves the mode unknown.
type ShowModeHolder struct {
	mu         sync.Mutex
	mode       string
	receivedAt time.Time
	revision   int64
	now        func() time.Time
}

// NewShowModeHolder constructs a holder that has never been told the mode.
// now may be nil, meaning time.Now.
func NewShowModeHolder(now func() time.Time) *ShowModeHolder {
	return &ShowModeHolder{now: now}
}

func (h *ShowModeHolder) clock() time.Time {
	if h.now != nil {
		return h.now()
	}
	return time.Now()
}

// Set records a mode received from the coordinator. A value outside
// ADR-033's closed vocabulary is ignored rather than stored: this node
// keeps whatever it already held, which is the same rule it follows for a
// coordinator that has gone silent.
func (h *ShowModeHolder) Set(mode string, revision int64, receivedAt time.Time) bool {
	if mode != mqttproto.ShowModeProgram && mode != mqttproto.ShowModeShow {
		return false
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	h.mode, h.revision, h.receivedAt = mode, revision, receivedAt
	return true
}

// Current reports the mode as of right now. This is the point-of-decision
// read: a caller must call it each time it decides something, never hold
// its result.
func (h *ShowModeHolder) Current() ShowModeState {
	h.mu.Lock()
	defer h.mu.Unlock()

	if h.mode == "" {
		// Never told. Not "program", because this node has no evidence of
		// program, and not "show" either, because saying show would claim
		// evidence it does not have. Unknown, which behaves as show.
		return ShowModeState{Mode: ShowModeUnknown, Held: true}
	}
	return ShowModeState{
		Mode:       h.mode,
		Held:       h.clock().Sub(h.receivedAt) > ShowModeFreshnessWindow,
		ReceivedAt: h.receivedAt,
		Revision:   h.revision,
	}
}

// BehavesAsShow is [ShowModeState.BehavesAsShow] asked of the holder
// itself, which is the whole read a consumer deciding one thing needs: it
// re-reads the current value on every call, so a consumer that keeps the
// holder (never a value taken from it) satisfies ADR-036 decision 1's
// point-of-decision rule by construction.
//
// This is also what makes *ShowModeHolder a [pipeline.ShowModeSource]
// without that package importing this one.
func (h *ShowModeHolder) BehavesAsShow() bool {
	return h.Current().BehavesAsShow()
}

// registerShowMode binds holder to receive the retained installation-wide
// mode and subscribes to its topic, both fresh on every call (that is,
// every OnConnectionUp), for [registerCommandHandling]'s reason: a
// reconnect gives autopaho a brand new underlying client with no memory of
// a prior SUBSCRIBE or callback registration.
//
// The subscription is QoS 1 on a retained topic, so a node that connects
// long after the mode was last set is told it immediately by the broker's
// retained copy rather than waiting for the coordinator's next republish.
//
// Nothing here can block or fail a command. A failed SUBSCRIBE leaves this
// node holding whatever mode it already had (unknown on a fresh agent) and
// is retried on the next reconnect; ADR-033 decision 4 forbids the mode
// from delaying or degrading anything, and that starts with its own
// delivery path.
func registerShowMode(ctx context.Context, cm *autopaho.ConnectionManager, holder *ShowModeHolder, now func() time.Time, logger *slog.Logger) {
	topic := mqttproto.ShowModeTopic()

	cm.AddOnPublishReceived(func(pr autopaho.PublishReceived) (bool, error) {
		if pr.Packet == nil || pr.Packet.Topic != topic {
			return false, nil
		}
		msg, err := mqttproto.DecodeShowModeMessage(pr.Packet.Payload)
		if err != nil {
			// A malformed message is not a reason to change the mode. The
			// node keeps what it held, which is the same posture it takes
			// toward a coordinator that has gone silent.
			logger.Warn("ignoring a malformed show mode message; keeping the mode already held",
				"topic", topic, "error", err)
			return true, nil
		}
		receivedAt := time.Now()
		if now != nil {
			receivedAt = now()
		}
		previous := holder.Current()
		if holder.Set(msg.Mode, msg.Revision, receivedAt) && previous.Mode != msg.Mode {
			logger.Info("show mode received", "mode", msg.Mode, "revision", msg.Revision,
				"previous_mode", previous.Mode)
		}
		return true, nil
	})

	go func() {
		subCtx, cancel := context.WithTimeout(ctx, cmdSubscribeTimeout)
		defer cancel()

		suback, err := cm.Subscribe(subCtx, &paho.Subscribe{
			Subscriptions: []paho.SubscribeOptions{
				{Topic: topic, QoS: mqttproto.ShowModeDeliveryPolicy.QoS},
			},
		})
		// WARN, not ERROR, unlike the cmd topic's own failed SUBSCRIBE: a
		// node that cannot subscribe here still executes every command it
		// is sent and simply holds the mode it already had, which is a
		// documented, safe state rather than a silently broken one.
		if err != nil {
			logger.Warn("failed to subscribe to the show mode topic; this node keeps the mode it already holds",
				"topic", topic, "error", err)
			return
		}
		if len(suback.Reasons) == 0 || suback.Reasons[0] >= 0x80 {
			logger.Warn("broker rejected the show mode topic subscription; this node keeps the mode it already holds",
				"topic", topic, "reasons", suback.Reasons)
			return
		}
		logger.Info("subscribed to the show mode topic", "topic", topic)
	}()
}

// showModeHeldWatchInterval is how often [runShowModeWatch] re-reads the
// holder to notice a current-to-held transition. Half the freshness window,
// so the transition is logged within one window of it becoming true.
const showModeHeldWatchInterval = ShowModeFreshnessWindow / 2

// runShowModeWatch logs the mode's current-to-held transitions and back,
// until ctx is cancelled.
//
// This exists because a value nobody can observe is a value nobody can
// trust. ADR-011's rule is that absence of evidence must be visible rather
// than silent; a coordinator that has stopped confirming the mode is
// exactly that, and an operator reading a node's logs after an outage needs
// to see that the node is running on a held value rather than a current
// one. A consumer reads the holder directly at its own point of decision;
// this loop is the observability half and nothing depends on it.
func runShowModeWatch(ctx context.Context, holder *ShowModeHolder, logger *slog.Logger, interval time.Duration) {
	ticker := time.NewTicker(interval)
	defer ticker.Stop()

	var (
		lastHeld bool
		reported bool
	)
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			s := holder.Current()
			if reported && s.Held == lastHeld {
				continue
			}
			if s.Held {
				logger.Warn("show mode is HELD, not current: the coordinator has stopped confirming it",
					"mode", s.Mode, "behaves_as_show", s.BehavesAsShow(),
					"received_at", s.ReceivedAt, "revision", s.Revision)
			} else {
				logger.Info("show mode is current", "mode", s.Mode, "revision", s.Revision)
			}
			lastHeld, reported = s.Held, true
		}
	}
}
