package clock

import (
	"context"
	"fmt"
	"sync"
	"time"
)

// Logger is the minimal logging surface [Manager] and [ManagedProvider]
// need, matching internal/agent/pipeline.Logger's identical minimal-
// interface convention so this package does not import log/slog's full
// API into every call site.
type Logger interface {
	Warn(msg string, args ...any)
}

// Config is this node's currently-desired node.clock configuration — the
// agent-side counterpart of internal/coordinator/config.NodeClockPayload,
// independently reproduced rather than imported (this package has no
// coordinator dependency, matching every other wire boundary in this
// codebase).
type Config struct {
	Provider             ProviderKind
	Interface            string
	Domain               int
	ClientOnly           bool
	HoldoverLimit        time.Duration
	Priority1            int
	HardwareTimestamping bool
	ExternalUDSAddress   string
	FPPBaseURL           string
}

// Manager owns this node's current [Provider]/[Tracker] pair (or none, for
// a node with no node.clock configuration — see [Manager.Poll]), rebuilt
// whenever [Manager.SetConfig] is called with a genuinely new
// configuration. Safe for concurrent use: [Manager.Poll] (the report
// loop's own cadence) and [Manager.SetConfig] (a config push landing
// mid-report) never race each other's view of the current provider. The
// zero value is not usable; construct with [NewManager].
type Manager struct {
	now    func() time.Time
	logger Logger

	mu       sync.Mutex
	provider Provider
	tracker  *Tracker
}

// NewManager builds an unconfigured Manager. now defaults to [time.Now]
// when nil.
func NewManager(now func() time.Time, logger Logger) *Manager {
	if now == nil {
		now = time.Now
	}
	return &Manager{now: now, logger: logger}
}

// SetConfig closes whatever provider this Manager currently holds and
// builds a fresh one from cfg, starting it (for the managed provider —
// external and FPP providers own no process to start). An error here
// (most commonly the managed provider's ownership pre-check refusing —
// RES-019 section 5.3) leaves this Manager unconfigured; the caller
// (clockconfigops.go) is responsible for reporting that refusal back to
// the coordinator rather than silently keeping a stale provider running
// under a rejected configuration.
func (m *Manager) SetConfig(ctx context.Context, cfg Config) error {
	m.mu.Lock()
	defer m.mu.Unlock()

	if m.provider != nil {
		_ = m.provider.Close()
		m.provider = nil
		m.tracker = nil
	}

	provider, err := buildProvider(cfg, m.logger)
	if err != nil {
		// Logged here, not only returned: an operator watching this
		// node's own log is who RES-019 section 5.3's "and says why"
		// is actually for — the caller (clockconfigops.go) also carries
		// this same error text back to the coordinator via the command's
		// own result, but that is a second audience, not a substitute.
		if m.logger != nil {
			m.logger.Warn("node.clock configuration rejected", "provider", cfg.Provider, "interface", cfg.Interface, "domain", cfg.Domain, "error", err)
		}
		return err
	}
	if mp, ok := provider.(*ManagedProvider); ok {
		if err := mp.Start(ctx); err != nil {
			if m.logger != nil {
				m.logger.Warn("node.clock managed provider failed to start", "interface", cfg.Interface, "domain", cfg.Domain, "error", err)
			}
			return err
		}
	}

	m.provider = provider
	m.tracker = NewTracker(provider, TrackerConfig{
		Domain: cfg.Domain, DomainDeclared: true,
		HoldoverLimit: cfg.HoldoverLimit,
	}, m.now)
	return nil
}

func buildProvider(cfg Config, logger Logger) (Provider, error) {
	switch cfg.Provider {
	case ProviderManaged:
		return NewManagedProvider(ManagedConfig{
			Interface: cfg.Interface, Domain: cfg.Domain, ClientOnly: cfg.ClientOnly,
			Priority1: cfg.Priority1, HardwareTimestamping: cfg.HardwareTimestamping,
		}, logger)
	case ProviderExternal:
		return NewExternalProvider(ExternalConfig{
			Interface: cfg.Interface, Domain: cfg.Domain, UDSAddress: cfg.ExternalUDSAddress,
		}), nil
	case ProviderFPP:
		return NewFPPProvider(FPPConfig{Interface: cfg.Interface, BaseURL: cfg.FPPBaseURL}), nil
	default:
		return nil, fmt.Errorf("clock: unknown provider kind %q", cfg.Provider)
	}
}

// Poll reports this node's current clock status: [StatusUnconfigured] when
// no configuration has ever been accepted, otherwise the current
// [Tracker]'s own Poll — see internal/agent/clockreport.go, this node's
// report-loop caller.
func (m *Manager) Poll(ctx context.Context) Status {
	m.mu.Lock()
	t := m.tracker
	m.mu.Unlock()
	if t == nil {
		return StatusUnconfigured(m.now())
	}
	return t.Poll(ctx)
}

// Close releases whatever provider this Manager currently holds.
func (m *Manager) Close() error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.provider == nil {
		return nil
	}
	err := m.provider.Close()
	m.provider = nil
	m.tracker = nil
	return err
}
