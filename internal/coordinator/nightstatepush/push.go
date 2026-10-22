// Package nightstatepush tells every node whether a night session is open
// (ADR-067), over the node command topic: on change, on node hello, and on a
// slow resend so a missed send heals. Best effort; nothing waits on it.
package nightstatepush

import (
	"context"
	"encoding/json"
	"log/slog"
	"time"

	"github.com/google/uuid"

	"github.com/showmeshsystems/showmesh/internal/coordinator/assetsync"
	"github.com/showmeshsystems/showmesh/internal/coordinator/store"
	"github.com/showmeshsystems/showmesh/pkg/mqttproto"
)

// Action is the node command this package sends.
const Action = "night.state.set"

const pushIssuer = "showmesh-coordinator-night-state"

const publishTimeout = 5 * time.Second

// Publisher is *broker.BrokerManager's publish method.
type Publisher interface {
	Publish(ctx context.Context, topic string, qos byte, retain bool, payload []byte) error
}

// Push sends {"open": open} to nodeID. Each send gets a fresh idempotency
// key so a later flip back is never answered from the node's cache.
func Push(ctx context.Context, pub Publisher, now func() time.Time, nodeID string, open bool) error {
	topic, err := mqttproto.CmdTopic(nodeID)
	if err != nil {
		return err
	}
	cmd := mqttproto.CmdPayload{
		CommandID: uuid.NewString(), IdempotencyKey: uuid.NewString(), Action: Action,
		Target: mqttproto.CmdTarget{Kind: "node", ID: nodeID}, Params: map[string]any{"open": open},
		Issuer:             mqttproto.CmdIssuer{PrincipalID: pushIssuer, PrincipalName: pushIssuer},
		ConfirmationMethod: "evidence",
	}
	env, err := mqttproto.NewCmdEnvelope(now, nodeID, cmd)
	if err != nil {
		return err
	}
	raw, err := json.Marshal(env)
	if err != nil {
		return err
	}
	pctx, cancel := context.WithTimeout(ctx, publishTimeout)
	defer cancel()
	return pub.Publish(pctx, topic, mqttproto.CmdDeliveryPolicy.QoS, mqttproto.CmdDeliveryPolicy.Retain, raw)
}

// BestEffort pushes the current night state to one node, logging failures.
func BestEffort(ctx context.Context, st *store.Store, pub Publisher, now func() time.Time, nodeID string, logger *slog.Logger) {
	open, err := assetsync.NightSessionOpen(ctx, st)
	if err == nil {
		err = Push(ctx, pub, now, nodeID, open)
	}
	if err != nil && logger != nil {
		logger.Warn("night state push failed", "node_id", nodeID, "error", err)
	}
}

// Loop pushes the night state to every declared node when it changes and
// every Resend otherwise. Poll is how often the night session is read.
type Loop struct {
	Store  *store.Store
	Pub    Publisher
	Now    func() time.Time
	Logger *slog.Logger
	Poll   time.Duration
	Resend time.Duration

	sent     bool
	lastOpen bool
	lastAll  time.Time
}

// Run runs passes until ctx ends.
func (l *Loop) Run(ctx context.Context) {
	ticker := time.NewTicker(l.Poll)
	defer ticker.Stop()
	for {
		l.Pass(ctx)
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
	}
}

// Pass reads the night state once and pushes it to every node when it
// changed or the resend interval elapsed.
func (l *Loop) Pass(ctx context.Context) {
	open, err := assetsync.NightSessionOpen(ctx, l.Store)
	if err != nil {
		l.warn("night state push: read night session failed", "error", err)
		return
	}
	now := l.Now()
	if l.sent && open == l.lastOpen && now.Sub(l.lastAll) < l.Resend {
		return
	}
	nodes, err := l.Store.ListNodes(ctx)
	if err != nil {
		l.warn("night state push: list nodes failed", "error", err)
		return
	}
	for _, n := range nodes {
		if err := Push(ctx, l.Pub, l.Now, n.NodeID, open); err != nil {
			l.warn("night state push failed", "node_id", n.NodeID, "open", open, "error", err)
		}
	}
	l.sent, l.lastOpen, l.lastAll = true, open, now
}

func (l *Loop) warn(msg string, args ...any) {
	if l.Logger != nil {
		l.Logger.Warn(msg, args...)
	}
}
