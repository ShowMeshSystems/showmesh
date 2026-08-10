package coordinator

import (
	"context"
	"fmt"
	"log/slog"
	"net/url"
	"sync"
	"time"

	"github.com/eclipse/paho.golang/autopaho"
	"github.com/eclipse/paho.golang/paho"
)

// brokerProbeInterval is how often the background probe goroutine re-checks
// the broker connection and re-stamps BrokerState.ObservedAt, independent of
// the OnConnectionUp/OnConnectionDown callbacks.
const brokerProbeInterval = 5 * time.Second

// brokerProbeTimeout bounds each individual probe so a hung check cannot
// delay the next one or block shutdown.
const brokerProbeTimeout = 250 * time.Millisecond

// BrokerState is evidence about the broker connection, not just a flag.
// ADR-011 requires observations to carry freshness; a value with no
// observation time cannot degrade to unknown. The canonical observation
// type arrives in Step 3 with the collector model (OBSERVABILITY 4.1);
// this is the local minimum that keeps the precedent correct.
type BrokerState struct {
	Connected bool

	// Since is when the current Connected value was first observed (i.e.
	// when it last changed). It does not move while Connected is
	// re-confirmed at the same value.
	Since time.Time

	// ObservedAt is when this state was last confirmed, whether or not
	// Connected changed. Callers use it to judge freshness: evidence older
	// than a staleness window is not proof of current health.
	ObservedAt time.Time
}

// BrokerManager owns the coordinator's connection to the MQTT broker.
//
// Per ADR-008, broker loss is a management-plane outage only: a running
// show must survive it, and so must the coordinator process. BrokerManager
// never blocks startup on a successful connection and never causes the
// coordinator to exit because the broker is unreachable — the underlying
// autopaho connection manager retries forever with backoff.
type BrokerManager struct {
	cm     *autopaho.ConnectionManager
	logger *slog.Logger

	// now is the clock BrokerManager uses to stamp BrokerState. It is a
	// field (rather than a direct time.Now call) so tests can drive state
	// transitions with a fake clock instead of real sleeps.
	now func() time.Time

	mu    sync.Mutex
	state BrokerState

	wg sync.WaitGroup
}

// NewBrokerManager begins connecting to cfg.MQTTBroker in the background and
// returns immediately. It does not wait for the connection to come up, and
// it does not return an error merely because the broker is currently
// unreachable — connection failures are logged and retried.
//
// The probe goroutine that periodically re-confirms BrokerState is tied to
// ctx: it exits when ctx is done, so callers must cancel ctx (or call
// Disconnect) on shutdown to avoid leaking it.
func NewBrokerManager(ctx context.Context, cfg Config, logger *slog.Logger) (*BrokerManager, error) {
	if logger == nil {
		logger = slog.Default()
	}

	serverURL, err := url.Parse(cfg.MQTTBroker)
	if err != nil {
		// Config.Validate should already have caught this; guard anyway
		// since a malformed URL here would otherwise loop forever without
		// ever attempting a connection.
		return nil, fmt.Errorf("parsing mqtt broker url %q: %w", cfg.MQTTBroker, err)
	}

	bm := &BrokerManager{logger: logger, now: time.Now}
	initAt := bm.now()
	bm.state = BrokerState{Connected: false, Since: initAt, ObservedAt: initAt}

	clientCfg := autopaho.ClientConfig{
		ServerUrls: []*url.URL{serverURL},
		// KeepAlive is deliberately tied to readyzStalenessWindow (see
		// server.go): partition detection latency is ultimately bounded by
		// this interval, since a silent network partition (no TCP FIN) is
		// only caught when a keepalive round-trip fails. Keeping it at or
		// below the staleness window is what makes that window a meaningful
		// floor on confidence rather than a number disconnected from the
		// mechanism that actually detects loss.
		KeepAlive:        uint16(readyzStalenessWindow.Seconds()),
		ConnectTimeout:   10 * time.Second,
		ReconnectBackoff: autopaho.DefaultExponentialBackoff(),
		OnConnectionUp: func(*autopaho.ConnectionManager, *paho.Connack) {
			bm.setConnected(true)
			logger.Info("mqtt broker connection up", "broker", cfg.MQTTBroker, "client_id", cfg.MQTTClientID)
		},
		OnConnectionDown: func() bool {
			bm.setConnected(false)
			logger.Warn("mqtt broker connection lost; will retry", "broker", cfg.MQTTBroker)
			return true // never give up retrying
		},
		OnConnectError: func(err error) {
			bm.setConnected(false)
			logger.Warn("mqtt broker connect attempt failed; will retry", "broker", cfg.MQTTBroker, "error", err)
		},
		ClientConfig: paho.ClientConfig{
			ClientID: cfg.MQTTClientID,
		},
	}

	if cfg.MQTTUsername != "" {
		clientCfg.ConnectUsername = cfg.MQTTUsername
		clientCfg.ConnectPassword = []byte(cfg.MQTTPassword)
	}

	cm, err := autopaho.NewConnection(ctx, clientCfg)
	if err != nil {
		// This only happens for programmer errors (e.g. no server URLs);
		// it is not a "broker unreachable" condition.
		return nil, fmt.Errorf("starting mqtt connection manager: %w", err)
	}
	bm.cm = cm

	bm.wg.Add(1)
	go bm.runProbe(ctx)

	return bm, nil
}

// runProbe periodically re-confirms the broker connection independent of
// the OnConnectionUp/OnConnectionDown callbacks, so ObservedAt advances even
// when nothing changes and evidence can still go stale (and degrade to
// unknown, per ADR-011) if this loop itself stops running. It exits when
// ctx is done.
func (b *BrokerManager) runProbe(ctx context.Context) {
	defer b.wg.Done()

	ticker := time.NewTicker(brokerProbeInterval)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			probeCtx, cancel := context.WithTimeout(ctx, brokerProbeTimeout)
			err := b.cm.AwaitConnection(probeCtx)
			cancel()
			b.setConnected(err == nil)
		}
	}
}

// setConnected records a fresh observation of the connection state. Since
// only moves when Connected actually changes value; ObservedAt always
// advances, since a re-confirmation of the same value is still new
// evidence.
func (b *BrokerManager) setConnected(connected bool) {
	b.mu.Lock()
	defer b.mu.Unlock()

	now := b.now()
	if connected != b.state.Connected {
		b.state.Since = now
	}
	b.state.Connected = connected
	b.state.ObservedAt = now
}

// State returns the most recently observed BrokerState. Safe for concurrent
// use.
func (b *BrokerManager) State() BrokerState {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.state
}

// Disconnect shuts down the broker connection cleanly, honoring ctx's
// deadline. The probe goroutine is stopped via ctx cancellation by the
// caller (the same ctx passed to NewBrokerManager); Disconnect itself only
// tears down the MQTT connection.
func (b *BrokerManager) Disconnect(ctx context.Context) error {
	if b.cm == nil {
		return nil
	}
	return b.cm.Disconnect(ctx)
}
