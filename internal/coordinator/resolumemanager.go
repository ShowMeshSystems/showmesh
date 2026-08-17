package coordinator

// This file is Track G seam G-2's no-restart apply (ADR-039 decision 6,
// applying ADR-036): the FPP endpoint list has followed configuration live
// since fppendpoints.go; SHOWMESH_RESOLUME_URL/SHOWMESH_RESOLUME_ID never
// did, because they were still an environment variable read once at
// process start. resolumeManager is fppEndpointSource/reconcileFPPCollectors'
// shape narrowed to at most one instance: a live source
// ([resolumeInstanceSource]) plus a reconciler that builds or tears down
// exactly one collector/watcher/action-dispatcher/recovery bundle as the
// active configuration changes, with NO restart in either direction.
//
// The zero-to-one and one-to-zero transitions are the ones that matter
// (this seam's own spec: "that is the exact transition an operator setting
// this up for the first time performs, and it is the transition a
// configuration path built by editing an already-populated list never
// exercises") — unlike the FPP case there is only ever 0 or 1 instance, so
// this manager holds one bundle rather than a map.
//
// Every per-instance construction step below is byte-for-byte the SAME
// call sequence coordinator.go's Run used to perform exactly once at
// startup (newResolumeWiring, then — only when a collector was built — the
// action dispatcher adapter and the recovery controller): this file adds
// the ability to run that sequence again, and to tear it down, never a
// second way to build any of those four things.

import (
	"context"
	"errors"
	"log/slog"
	"sync"
	"sync/atomic"
	"time"

	"github.com/showmeshsystems/showmesh/internal/coordinator/api"
	"github.com/showmeshsystems/showmesh/internal/coordinator/collector"
	"github.com/showmeshsystems/showmesh/internal/coordinator/collector/resolume"
	"github.com/showmeshsystems/showmesh/internal/coordinator/config"
	"github.com/showmeshsystems/showmesh/internal/coordinator/identity"
	"github.com/showmeshsystems/showmesh/internal/coordinator/store"
)

// errResolumeManagerNotConfigured is returned by a write-shaped method
// (Dispatch, Restore) when no Resolume instance is currently configured —
// mirroring api.errResolumeActionsNotConfigured/errResolumeRecoveryNotConfigured's
// identical "refuse loudly, never fabricate success" posture for an
// unwired write dependency, redeclared here because those two are
// unexported in package api.
var errResolumeManagerNotConfigured = errors.New("coordinator: no Resolume instance is currently configured")

// resolumeInstanceReconcileInterval mirrors fppCollectorReconcileInterval's
// identical reasoning: well inside a command's 20-second confirmation
// deadline, so a newly configured instance is already being polled before
// an operator could plausibly send it a command.
const resolumeInstanceReconcileInterval = 10 * time.Second

// resolumeInstanceSource is [fppEndpointSource]'s mirror for the
// resolume.instances kind: resolves the currently active configuration on
// demand, caching the decoded list against the revision number it came
// from. See that type's own doc comment for the "no refresh method, no
// invalidation call" reasoning, which applies unchanged here.
type resolumeInstanceSource struct {
	st     *store.Store
	logger *slog.Logger

	mu       sync.Mutex
	revision int64
	cached   []config.ResolumeInstance
	loaded   bool
}

func newResolumeInstanceSource(st *store.Store, logger *slog.Logger) *resolumeInstanceSource {
	return &resolumeInstanceSource{st: st, logger: logger}
}

// Current returns the active Resolume instance list (0 or 1 entries in
// practice, enforced by validation at write time). On any store error it
// returns the last list it successfully read, and logs — mirroring
// [fppEndpointSource.Current]'s identical "stale-but-real beats
// manufactured empty" reasoning.
func (s *resolumeInstanceSource) Current(ctx context.Context) []config.ResolumeInstance {
	s.mu.Lock()
	defer s.mu.Unlock()

	obj, err := s.st.GetConfigObject(ctx, config.ResolumeInstancesConfigKind, config.ResolumeInstancesConfigObjectID)
	if err != nil {
		if !s.loaded {
			return nil
		}
		s.logWarn("failed to read the active resolume.instances configuration; continuing with the last known list", err)
		return s.cached
	}
	if s.loaded && obj.CurrentRevision == s.revision {
		return s.cached
	}

	rev, err := s.st.GetConfigRevision(ctx, config.ResolumeInstancesConfigKind, config.ResolumeInstancesConfigObjectID, obj.CurrentRevision)
	if err != nil {
		s.logWarn("failed to read the active resolume.instances revision; continuing with the last known list", err)
		return s.cached
	}
	instances, err := config.DecodeResolumeInstancesPayload(rev.PayloadJSON)
	if err != nil {
		s.logWarn("failed to decode the active resolume.instances revision; continuing with the last known list", err)
		return s.cached
	}

	s.revision = obj.CurrentRevision
	s.cached = instances
	s.loaded = true
	return instances
}

func (s *resolumeInstanceSource) logWarn(msg string, err error) {
	if s.logger != nil {
		s.logger.Warn(msg, "error", err)
	}
}

// resolumeBundle is everything one configured Resolume instance owns: the
// collector/watcher pair (resolumeWiring, from newResolumeWiring), the
// action dispatcher adapter, and the recovery controller — the SAME four
// things coordinator.go's Run used to build exactly once. cancel stops the
// bundle's own watcher-supervisor goroutine.
type resolumeBundle struct {
	instance        config.ResolumeInstance
	wiring          resolumeWiring
	actions         *resolumeActionDispatcherAdapter
	recoveryAdapter *resolumeRecoveryAdapter
	cancel          context.CancelFunc
}

// resolumeManager owns at most one [resolumeBundle] and reconciles it
// against whatever [resolumeInstanceSource.Current] reports, live, while
// this coordinator runs. It implements every interface
// internal/coordinator/api declares for a Resolume dependency
// (api.ResolumeLister, api.CollectorStatusLister,
// api.ResolumeActionDispatcher, api.ResolumeRecoveryProvider) by delegating
// to whichever bundle is current, or answering the identical "unconfigured"
// posture the old per-process nil defaults used to when [resolumeManager.bundle]
// is nil — so api.Dependencies wires ONE value, once, at startup, and every
// caller through it sees live state for the rest of this process's life.
type resolumeManager struct {
	baseCfg          config.Config
	runner           *collector.Runner
	compositionStore *resolume.CompositionStore
	st               *store.Store
	identitySvc      identity.Service
	logger           *slog.Logger
	notify           func()

	mu     sync.Mutex
	bundle *resolumeBundle
	wg     sync.WaitGroup
}

var (
	_ api.ResolumeLister           = (*resolumeManager)(nil)
	_ api.CollectorStatusLister    = (*resolumeManager)(nil)
	_ api.ResolumeActionDispatcher = (*resolumeManager)(nil)
	_ api.ResolumeRecoveryProvider = (*resolumeManager)(nil)
)

// newResolumeManager constructs a manager with no bundle. Call
// [resolumeManager.reconcile] once, synchronously, with the startup-resolved
// instance list before starting any goroutine that serves requests — the
// same "no request may observe a partially-wired dependency" property
// coordinator.go's Run already holds for every other subsystem.
func newResolumeManager(baseCfg config.Config, runner *collector.Runner, compositionStore *resolume.CompositionStore, st *store.Store, identitySvc identity.Service, logger *slog.Logger, notify func()) *resolumeManager {
	return &resolumeManager{
		baseCfg: baseCfg, runner: runner, compositionStore: compositionStore,
		st: st, identitySvc: identitySvc, logger: logger, notify: notify,
	}
}

// buildBundle constructs a full bundle for inst: the collector/watcher
// pair, the action dispatcher, and the recovery controller — copying
// coordinator.go's OLD one-time construction sequence exactly, parameterized
// on inst instead of m.baseCfg.ResolumeURL/ResolumeID. Registers the
// collector on m.runner and starts its watcher-supervisor goroutine, tracked
// by m.wg so [resolumeManager.stopCurrentLocked] and final shutdown
// (resolumeManager.Run's ctx.Done() branch) can wait for it to actually
// exit.
//
// onReachableTransition/onUnreachableTransition need a *resolume.Recovery
// that does not exist until AFTER the collector they are passed to is
// built — the identical chicken-and-egg [newResolumeWiring]'s own doc
// comment describes for coordinator.go's OLD single-construction call site,
// solved the same way: a local holder the closures capture by reference,
// populated once construction finishes.
func (m *resolumeManager) buildBundle(ctx context.Context, inst config.ResolumeInstance) (*resolumeBundle, error) {
	instCfg := m.baseCfg
	instCfg.ResolumeURL = inst.URL
	instCfg.ResolumeID = inst.ID

	var recoveryHolder atomic.Pointer[resolume.Recovery]
	wiring, err := newResolumeWiring(ctx, instCfg, m.runner, m.compositionStore, m.logger,
		func(returnedAt time.Time) {
			if rec := recoveryHolder.Load(); rec != nil {
				rec.HandleReachableTransition(ctx, returnedAt)
			}
		},
		func(time.Time) {
			if rec := recoveryHolder.Load(); rec != nil {
				rec.CaptureCrashTarget()
			}
		},
	)
	if err != nil {
		return nil, err
	}

	recoveryDispatcher := resolume.NewActionDispatcher(wiring.collector, resolume.ActionDispatcherOptions{})
	recovery, recoveryAdapter := newResolumeRecoveryWiring(m.st, m.identitySvc, wiring.collector, recoveryDispatcher, m.baseCfg.ResolumeRecoverySettle, m.logger, m.notify)
	recoveryHolder.Store(recovery)
	actions := newResolumeActionDispatcherAdapter(wiring.collector)

	bundleCtx, cancel := context.WithCancel(ctx)
	b := &resolumeBundle{instance: inst, wiring: wiring, actions: actions, recoveryAdapter: recoveryAdapter, cancel: cancel}

	m.wg.Add(1)
	go func() {
		defer m.wg.Done()
		wiring.RunWatcherSupervisor(bundleCtx)
	}()

	return b, nil
}

// stopCurrentLocked tears down m.bundle (must be non-nil) and clears it.
// Called with m.mu already held. Cancels the bundle's own watcher-supervisor
// context and removes its collector from m.runner; does NOT block waiting
// for the supervisor goroutine to exit — m.wg (waited on by
// [resolumeManager.Run]'s shutdown branch) is what makes final process
// shutdown clean without holding m.mu for the duration of that wait.
func (m *resolumeManager) stopCurrentLocked() {
	m.bundle.cancel()
	m.runner.Remove(m.bundle.instance.ID)
	m.bundle = nil
}

// reconcile compares desired (0 or 1 entries; more is a validation defect
// upstream, and this treats a longer slice as its first element only,
// never as a fatal condition — a configuration write surface's own job is
// refusing that shape before it ever reaches here) against the current
// bundle and does the minimum: an unchanged (id, url) pair is left alone,
// so an instance's own collector, its poll cadence, and its evidence are
// never disturbed by a reconcile pass that found nothing to do.
func (m *resolumeManager) reconcile(ctx context.Context, desired []config.ResolumeInstance) {
	m.mu.Lock()
	defer m.mu.Unlock()

	var want *config.ResolumeInstance
	if len(desired) > 0 {
		w := desired[0]
		want = &w
	}

	if m.bundle != nil {
		if want != nil && m.bundle.instance == *want {
			return
		}
		m.logger.Info("resolume instance configuration changed; stopping its collector", "instance_id", m.bundle.instance.ID)
		m.stopCurrentLocked()
	}

	if want == nil {
		return
	}

	b, err := m.buildBundle(ctx, *want)
	if err != nil {
		// Shape is validated at write time (config.ValidateResolumeInstances
		// runs before any revision is created), so reaching here means that
		// check and this package's own construction have drifted apart —
		// mirroring reconcileFPPCollectors' identical judgment: log and
		// leave this reconcile pass with no bundle rather than killing the
		// loop, so a later configuration change (or the next tick, once the
		// drift is fixed) still gets a chance.
		m.logger.Error("failed to construct resolume collector for configured instance", "instance_id", want.ID, "error", err)
		return
	}
	m.bundle = b
	m.logger.Info("resolume instance configured; started its collector", "instance_id", want.ID)
}

// Run keeps m's bundle matching [resolumeInstanceSource.Current] until ctx
// is cancelled, then tears down whatever bundle is still active and waits
// for every goroutine this manager ever started (m.wg) before returning —
// mirroring the "no leaked goroutines" contract every other background loop
// in coordinator.go's Run already satisfies.
//
// The FIRST reconcile is deliberately NOT performed here: coordinator.go's
// Run calls [resolumeManager.reconcile] once, synchronously, with the
// startup-resolved instance list, before this goroutine (and the HTTP
// server) ever starts — so a request handled the instant the server opens
// its listener already sees live state, never a manager that has not yet
// run its first pass.
func (m *resolumeManager) Run(ctx context.Context, source *resolumeInstanceSource) {
	ticker := time.NewTicker(resolumeInstanceReconcileInterval)
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
			m.reconcile(ctx, source.Current(ctx))
		}
	}
}

// --- api.ResolumeLister / api.CollectorStatusLister ------------------------

func (m *resolumeManager) currentInstanceID() string {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.bundle == nil {
		return ""
	}
	return m.bundle.instance.ID
}

// ListInstances implements [api.ResolumeLister], delegating to a freshly
// constructed [resolumeInstanceLister] carrying whatever instance id is
// current RIGHT NOW. resolumeInstanceLister.instanceID's own doc comment
// used to say that id "cannot change without a coordinator restart" — that
// was true when it was resolved once, at coordinator.go's own startup, into
// a value handed to api.Dependencies directly. It is resolved fresh on
// every call now, which is what makes it true instead.
func (m *resolumeManager) ListInstances(ctx context.Context) ([]api.ResolumeInstanceView, error) {
	return resolumeInstanceLister{st: m.st, instanceID: m.currentInstanceID()}.ListInstances(ctx)
}

// CollectorStatuses implements [api.CollectorStatusLister], delegating to a
// freshly constructed [resolumeCollectorStatusLister] reflecting whether a
// bundle is current RIGHT NOW.
func (m *resolumeManager) CollectorStatuses(ctx context.Context) ([]api.CollectorState, error) {
	return resolumeCollectorStatusLister{configured: m.currentInstanceID() != ""}.CollectorStatuses(ctx)
}

// --- api.ResolumeActionDispatcher ------------------------------------------

func (m *resolumeManager) currentActions() *resolumeActionDispatcherAdapter {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.bundle == nil {
		return nil
	}
	return m.bundle.actions
}

// Actions mirrors [api.noResolumeActionDispatcher.Actions]'s "empty
// vocabulary" answer when no instance is currently configured.
func (m *resolumeManager) Actions() []api.ResolumeActionDescriptor {
	a := m.currentActions()
	if a == nil {
		return nil
	}
	return a.Actions()
}

// Dispatch mirrors [api.noResolumeActionDispatcher.Dispatch]'s "refuse
// loudly" answer when no instance is currently configured — unreachable
// through the normal request path in practice, since [Actions] already
// reports empty and every action name is refused as unsupported before
// Dispatch is ever called (matching that type's own doc comment).
func (m *resolumeManager) Dispatch(ctx context.Context, action string, params map[string]any, t time.Time) (api.ResolumeActionResult, error) {
	a := m.currentActions()
	if a == nil {
		return api.ResolumeActionResult{}, errResolumeManagerNotConfigured
	}
	return a.Dispatch(ctx, action, params, t)
}

// --- api.ResolumeRecoveryProvider ------------------------------------------

func (m *resolumeManager) currentRecoveryAdapter() *resolumeRecoveryAdapter {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.bundle == nil {
		return nil
	}
	return m.bundle.recoveryAdapter
}

func (m *resolumeManager) Record() []api.ResolumeRecoveryRecordEntryView {
	a := m.currentRecoveryAdapter()
	if a == nil {
		return nil
	}
	return a.Record()
}

func (m *resolumeManager) LastReport() *api.ResolumeRecoveryRestoreReportView {
	a := m.currentRecoveryAdapter()
	if a == nil {
		return nil
	}
	return a.LastReport()
}

func (m *resolumeManager) Restore(ctx context.Context, principalName string) (api.ResolumeRecoveryRestoreReportView, error) {
	a := m.currentRecoveryAdapter()
	if a == nil {
		return api.ResolumeRecoveryRestoreReportView{}, errResolumeManagerNotConfigured
	}
	return a.Restore(ctx, principalName)
}

// Configured reports whether a Resolume instance is configured on this
// coordinator RIGHT NOW — see [api.ResolumeRecoveryProvider.Configured]'s
// own doc comment for why this replaced a type assertion against
// api.noResolumeRecoveryProvider once ResolumeRecovery could be wired to a
// live manager holding zero instances.
func (m *resolumeManager) Configured() bool {
	return m.currentInstanceID() != ""
}
