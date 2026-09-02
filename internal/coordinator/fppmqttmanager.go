package coordinator

// Track G seam G-3 (ADR-039 decision 6, applying ADR-036): fppMQTTManager
// owns the live *fppmqtt.Collector for whatever fpp.mqtt currently
// configures, and reconciles it against the store with no restart in
// either direction — mirrors resolumemanager.go's bundle/reconcile shape,
// narrowed to one collector with no action/recovery surface.

import (
	"context"
	"errors"
	"log/slog"
	"sort"
	"sync"
	"time"

	"github.com/showmeshsystems/showmesh/internal/coordinator/api"
	"github.com/showmeshsystems/showmesh/internal/coordinator/collector"
	"github.com/showmeshsystems/showmesh/internal/coordinator/collector/fppmqtt"
	"github.com/showmeshsystems/showmesh/internal/coordinator/config"
	"github.com/showmeshsystems/showmesh/internal/coordinator/store"
)

// fppMQTTReconcileInterval mirrors resolumeInstanceReconcileInterval's
// identical reasoning: well inside a confirmation deadline, so a newly
// configured broker is already being polled before an operator could
// plausibly notice otherwise.
const fppMQTTReconcileInterval = 10 * time.Second

// fppMQTTConfigSource resolves the active fpp.mqtt configuration (plus its
// live password) on demand, caching against the revision number it came
// from — mirrors resolumeInstanceSource.
type fppMQTTConfigSource struct {
	st      *store.Store
	dataDir string
	logger  *slog.Logger

	mu       sync.Mutex
	revision int64
	cached   config.FPPMQTTConfig
	password string
}

// newFPPMQTTConfigSource seeds the source with the boot-resolved
// AUTHORITATIVE configuration and password — see newResolumeInstanceSource
// for why: while a deferred boot migration leaves the environment
// authoritative the store holds no fpp.mqtt object, and an unseeded source
// would manufacture an empty configuration and tear down the env-built
// collector on the first reconcile tick.
func newFPPMQTTConfigSource(st *store.Store, dataDir string, logger *slog.Logger, initialCfg config.FPPMQTTConfig, initialPassword string) *fppMQTTConfigSource {
	return &fppMQTTConfigSource{st: st, dataDir: dataDir, logger: logger, cached: initialCfg, password: initialPassword}
}

// Current returns the active fpp.mqtt configuration and password. A
// missing config object is a steady state and answers the seed; any other
// store or file error keeps the last known value, and logs — mirroring
// [resolumeInstanceSource.Current]'s "stale-but-real beats manufactured
// empty" reasoning.
func (s *fppMQTTConfigSource) Current(ctx context.Context) (config.FPPMQTTConfig, string) {
	s.mu.Lock()
	defer s.mu.Unlock()

	obj, err := s.st.GetConfigObject(ctx, config.FPPMQTTConfigKind, config.FPPMQTTConfigObjectID)
	if errors.Is(err, store.ErrConfigObjectNotFound) {
		return s.cached, s.password
	}
	if err != nil {
		s.logWarn("failed to read the active fpp.mqtt configuration; continuing with the last known value", err)
		return s.cached, s.password
	}
	if obj.CurrentRevision == s.revision {
		return s.cached, s.password
	}

	rev, err := s.st.GetConfigRevision(ctx, config.FPPMQTTConfigKind, config.FPPMQTTConfigObjectID, obj.CurrentRevision)
	if err != nil {
		s.logWarn("failed to read the active fpp.mqtt revision; continuing with the last known value", err)
		return s.cached, s.password
	}
	cfg, _, err := config.DecodeFPPMQTTPayload(rev.PayloadJSON)
	if err != nil {
		s.logWarn("failed to decode the active fpp.mqtt revision; continuing with the last known value", err)
		return s.cached, s.password
	}
	// Re-read on every revision change, even a non-secret-only edit: a
	// password rotation always bumps the revision number too (see
	// api/fppmqttconfig.go's handlePutFPPMQTTConfig), so this is a
	// sufficient invalidation signal for both halves at once.
	password, _, err := config.ReadFPPMQTTPassword(s.dataDir)
	if err != nil {
		s.logWarn("failed to read the stored fpp.mqtt password; continuing with the last known value", err)
		return s.cached, s.password
	}

	s.revision = obj.CurrentRevision
	s.cached = cfg
	s.password = password
	return cfg, password
}

func (s *fppMQTTConfigSource) logWarn(msg string, err error) {
	if s.logger != nil {
		s.logger.Warn(msg, "error", err)
	}
}

// fppMQTTBundle is one configured broker's collector plus the cancel func
// for its connection goroutine.
type fppMQTTBundle struct {
	cfg       config.FPPMQTTConfig
	password  string
	collector *fppmqtt.Collector
	cancel    context.CancelFunc
}

// fppMQTTManager owns at most one [fppMQTTBundle] and reconciles it
// against whatever [fppMQTTConfigSource.Current] reports, live, while
// this coordinator runs. Implements [api.CollectorStatusLister] and
// [api.FPPMQTTHostLister] by delegating to whichever bundle is current.
type fppMQTTManager struct {
	runner *collector.Runner
	source *fppMQTTConfigSource
	logger *slog.Logger

	mu     sync.Mutex
	bundle *fppMQTTBundle
	wg     sync.WaitGroup
}

var (
	_ api.CollectorStatusLister = (*fppMQTTManager)(nil)
	_ api.FPPMQTTHostLister     = (*fppMQTTManager)(nil)
)

// newFPPMQTTManager constructs a manager with no bundle over source, which
// both Run's reconcile loop and CurrentHosts read. Call
// [fppMQTTManager.reconcile] once, synchronously, with the startup-resolved
// configuration before starting any goroutine that serves requests — the
// same "no request may observe a partially-wired dependency" property
// resolumeManager already holds.
func newFPPMQTTManager(runner *collector.Runner, source *fppMQTTConfigSource, logger *slog.Logger) *fppMQTTManager {
	return &fppMQTTManager{runner: runner, source: source, logger: logger}
}

// buildBundle constructs a *fppmqtt.Collector for cfg/password, registers
// it on m.runner, and starts its connection goroutine, tracked by m.wg so
// shutdown can wait for it to actually exit.
func (m *fppMQTTManager) buildBundle(ctx context.Context, cfg config.FPPMQTTConfig, password string) (*fppMQTTBundle, error) {
	c, err := fppmqtt.New(fppmqtt.Options{
		BrokerURL:   cfg.BrokerURL,
		Username:    cfg.Username,
		Password:    password,
		TopicPrefix: cfg.TopicPrefix,
		Hosts:       cfg.Hosts,
		Logger:      m.logger,
	})
	if err != nil {
		return nil, err
	}
	m.runner.Add(c, c.PollInterval())

	bundleCtx, cancel := context.WithCancel(ctx)
	b := &fppMQTTBundle{cfg: cfg, password: password, collector: c, cancel: cancel}

	m.wg.Add(1)
	go func() {
		defer m.wg.Done()
		if err := c.Run(bundleCtx); err != nil {
			m.logger.Error("fpp mqtt collector connection ended", "error", err)
		}
	}()

	return b, nil
}

// stopCurrentLocked tears down m.bundle (must be non-nil) and clears it.
// Called with m.mu already held. Does NOT block waiting for the
// connection goroutine to exit — m.wg (waited on by
// [fppMQTTManager.Run]'s shutdown branch) is what makes final process
// shutdown clean without holding m.mu for the duration of that wait.
func (m *fppMQTTManager) stopCurrentLocked() {
	m.bundle.cancel()
	m.runner.Remove(fppMQTTCollectorSourceID)
	m.bundle = nil
}

// reconcile compares cfg/password against the current bundle and does the
// minimum: an unchanged configuration is left alone, so a collector's own
// poll cadence and its evidence are never disturbed by a reconcile pass
// that found nothing to do.
func (m *fppMQTTManager) reconcile(ctx context.Context, cfg config.FPPMQTTConfig, password string) {
	m.mu.Lock()
	defer m.mu.Unlock()

	configured := cfg.Configured()

	if m.bundle != nil {
		if configured && config.FPPMQTTConfigEqual(m.bundle.cfg, cfg) && m.bundle.password == password {
			return
		}
		m.logger.Info("fpp.mqtt configuration changed; stopping its collector")
		m.stopCurrentLocked()
	}

	if !configured {
		return
	}

	b, err := m.buildBundle(ctx, cfg, password)
	if err != nil {
		// Shape is validated at write time (config.ValidateFPPMQTTConfigKind
		// runs before any revision is created), so reaching here means that
		// check and this package's own construction have drifted apart —
		// log and leave this reconcile pass with no bundle rather than
		// killing the loop.
		m.logger.Error("failed to construct fpp mqtt collector for configured fpp.mqtt", "error", err)
		return
	}
	m.bundle = b
	m.logger.Info("fpp.mqtt configured; started its collector")
}

// Run keeps m's bundle matching [fppMQTTConfigSource.Current] until ctx is
// cancelled, then tears down whatever bundle is still active and waits
// for every goroutine this manager ever started (m.wg) before returning.
//
// The FIRST reconcile is deliberately NOT performed here: the caller
// reconciles once, synchronously, with the startup-resolved configuration
// before this goroutine (and the HTTP server) ever starts — mirroring
// resolumeManager.Run's identical contract.
func (m *fppMQTTManager) Run(ctx context.Context) {
	ticker := time.NewTicker(fppMQTTReconcileInterval)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			m.mu.Lock()
			if m.bundle != nil {
				m.stopCurrentLocked()
			}
			m.mu.Unlock()
			m.wg.Wait()
			return
		case <-ticker.C:
			cfg, password := m.source.Current(ctx)
			m.reconcile(ctx, cfg, password)
		}
	}
}

// CollectorStatuses implements [api.CollectorStatusLister], reflecting
// whether a bundle is current RIGHT NOW — live, unlike the old
// fppMQTTCollectorStatusLister{configured: cfg.FPPMQTTBrokerURL != ""}
// startup snapshot this replaces.
//
// One row per configured host: a silent host among several publishing
// ones is now individually identifiable by its own row, rather than one
// publishing host clearing [api.CollectorConnectedNoData] for the whole
// collector. Rows are ordered by instance id. bundle.cfg.Hosts is a map
// with no ordering of its own anywhere in its lifecycle (env parsing, the
// stored JSON payload, and this map all carry no sequence), so sorting by
// id is the only order this method can make an actual, repeatable
// guarantee about, and it does: the same configuration always yields rows
// in the same order.
func (m *fppMQTTManager) CollectorStatuses(context.Context) ([]api.CollectorState, error) {
	m.mu.Lock()
	bundle := m.bundle
	m.mu.Unlock()

	if bundle == nil {
		reason := "no FPP MQTT broker configured"
		return []api.CollectorState{{ID: fppMQTTCollectorSourceID, State: string(api.CollectorNotConfigured), Reason: &reason}}, nil
	}

	ids := make([]string, 0, len(bundle.cfg.Hosts))
	for id := range bundle.cfg.Hosts {
		ids = append(ids, id)
	}
	sort.Strings(ids)

	states := make([]api.CollectorState, 0, len(ids))
	for _, id := range ids {
		silent, reason := bundle.collector.SilentSinceConnect(id)
		states = append(states, fppMQTTCollectorState(id, silent, reason))
	}
	return states, nil
}

// fppMQTTHostCollectorIDSeparator joins [fppMQTTCollectorSourceID] to a
// configured instance id to build one host's [api.CollectorState.ID].
// Colon, not hyphen: mqttproto.ValidateNodeID constrains every instance id
// to [a-z0-9-], so a hyphen-joined id could not be split back apart
// unambiguously (both the collector's own name and an instance id may
// contain one), while a colon never appears in an instance id and already
// has the same compound-id role elsewhere in this codebase (see
// config/showcue.go's CueTarget Kind:Resource / Kind:Node ids).
const fppMQTTHostCollectorIDSeparator = ":"

// fppMQTTCollectorState maps one host's [fppmqtt.Collector.SilentSinceConnect]
// result to a [api.CollectorState], pure and independent of any live
// connection so the silent=true direction is directly testable without a
// broker.
func fppMQTTCollectorState(instanceID string, silent bool, reason string) api.CollectorState {
	id := fppMQTTCollectorSourceID + fppMQTTHostCollectorIDSeparator + instanceID
	if silent {
		return api.CollectorState{ID: id, State: string(api.CollectorConnectedNoData), Reason: &reason}
	}
	return api.CollectorState{ID: id, State: string(api.CollectorRunning)}
}

// CurrentHosts implements [api.FPPMQTTHostLister]: the id->HostName map
// fpp.mqtt currently configures, live — used by handlePutFPPEndpointsConfig
// to cross-check a proposed fpp.endpoints list against fpp.mqtt as it
// stands RIGHT NOW, not a startup snapshot (mirroring the identical live
// re-check Track G seam G-2 added for the Resolume instance id).
//
// Resolved from the store-backed source, never from the running bundle: a
// stored fpp.mqtt with hosts but no brokerURL is valid and starts no
// bundle, yet its hosts must still refuse an fpp.endpoints write that
// would strand them — otherwise the next boot's own cross-check refuses
// to start with no API left to repair it.
func (m *fppMQTTManager) CurrentHosts(ctx context.Context) (map[string]string, error) {
	cfg, _ := m.source.Current(ctx)
	return cfg.Hosts, nil
}
