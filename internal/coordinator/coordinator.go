// Package coordinator wires the coordinator's config, SQLite store,
// inventory, MQTT broker connection, and HTTP server together and runs
// them until shutdown. Per ADR-001 it is never a scheduler and per ADR-008
// its loss (and the broker's) must never affect a running show.
package coordinator

import (
	"context"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"sync"
	"syscall"
	"time"

	"github.com/showmeshsystems/showmesh/internal/coordinator/api"
	"github.com/showmeshsystems/showmesh/internal/coordinator/assetstore"
	"github.com/showmeshsystems/showmesh/internal/coordinator/assetsync"
	"github.com/showmeshsystems/showmesh/internal/coordinator/broker"
	"github.com/showmeshsystems/showmesh/internal/coordinator/collector"
	"github.com/showmeshsystems/showmesh/internal/coordinator/collector/fpp"
	"github.com/showmeshsystems/showmesh/internal/coordinator/collector/nodeaudio"
	"github.com/showmeshsystems/showmesh/internal/coordinator/collector/noderender"
	"github.com/showmeshsystems/showmesh/internal/coordinator/config"
	"github.com/showmeshsystems/showmesh/internal/coordinator/httpapi"
	"github.com/showmeshsystems/showmesh/internal/coordinator/identity"
	"github.com/showmeshsystems/showmesh/internal/coordinator/inventory"
	"github.com/showmeshsystems/showmesh/internal/coordinator/macro"
	"github.com/showmeshsystems/showmesh/internal/coordinator/readiness"
	"github.com/showmeshsystems/showmesh/internal/coordinator/store"
	"github.com/showmeshsystems/showmesh/internal/version"
	"github.com/showmeshsystems/showmesh/pkg/observation"
)

// Run loads config, starts the MQTT connection manager and HTTP server, and
// blocks until a shutdown signal is received. It returns a process exit
// code.
func Run() int {
	cfg, err := config.LoadConfig()
	if err != nil {
		fmt.Fprintf(os.Stderr, "showmesh-coordinator: config error: %v\n", err)
		return 1
	}

	logger := newLogger(cfg.LogLevel)

	logger.Info("starting showmesh-coordinator",
		"version", version.Version,
		"commit", version.Commit,
		"build_date", version.BuildDate,
		"http_addr", cfg.HTTPAddr,
		"mqtt_broker", cfg.MQTTBroker,
		"mqtt_client_id", cfg.MQTTClientID,
		"mqtt_username_set", cfg.MQTTUsername != "",
		"data_dir", cfg.DataDir,
		"log_level", cfg.LogLevel,
		"api_reads_closed", cfg.CloseReads,
		"fpp_endpoint_count", len(cfg.FPPEndpoints),
	)

	// ADR-024 decision 2, carrying ADR-021 rule 3 forward: reads stay open
	// by default and closable by configuration, and whichever posture is
	// in effect gets a startup warning naming it. internal/coordinator/api.New
	// itself does not log this (it has no logger opinion about deployment
	// posture, only about request handling); this is the one place that
	// decision belongs. Unlike the retired SHOWMESH_API_TOKEN warning this
	// replaces, "reads closed" is not itself unconditionally safe to leave
	// unremarked either: an operator who sets SHOWMESH_API_CLOSE_READS=true
	// with zero principals yet provisioned has locked themselves out of
	// their own dashboard, which watchUnclaimedBootstrap's own loud,
	// repeated warning (started below) is what actually surfaces.
	if !cfg.CloseReads {
		logger.Warn("SHOWMESH_API_CLOSE_READS is not set: the /api/v1 read surface is served with NO AUTHENTICATION required. " +
			"Anyone who can reach this coordinator's HTTP port can read the full node inventory, FPP state, and event history. " +
			"Every write endpoint still requires an authenticated principal regardless (ADR-024 decision 2); " +
			"set SHOWMESH_API_CLOSE_READS=true to require one for reads too.")
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	// Unlike the broker below — which per ADR-012 must start and stay up
	// with no broker reachable, forever, with no retry loop of its own to
	// add here — the SQLite store is a local deployment dependency, not a
	// network one. A database that fails to open or migrate (bad
	// permissions, a corrupt file, a schema newer than this binary knows;
	// see store.ErrSchemaTooNew) is a fault on this host right now that a
	// retry cannot fix, so this is a hard, non-retried, fatal startup
	// error, deliberately asymmetric with the broker's tolerance below. Do
	// not "fix" this into a retry loop to make it match the broker.
	st, err := store.Open(ctx, cfg.DataDir, logger)
	if err != nil {
		logger.Error("failed to open coordinator store", "error", err)
		return 1
	}

	// identitySvc is ADR-024's principal/session/token/bootstrap/audit
	// surface (internal/coordinator/identity), constructed once here over
	// the same *store.Store and the same real time.Now st itself was
	// opened with (store.Open's default clock — see identity.NewService's
	// doc comment for why the two must share a clock). cfg.DataDir is
	// where the bootstrap file lives (ADR-024 decision 9), the same
	// directory the SQLite database above already lives in.
	identitySvc := identity.NewService(st, time.Now, cfg.DataDir, identity.WithLogger(logger))

	// Step 7 seam A (RES-008 D1): the SHOWMESH_FPP_ENDPOINTS -> store
	// migration and the owner's 2026-08-12 disagreement rule, run BEFORE
	// anything below reads cfg.FPPEndpoints — see configsync.go. From this
	// point on, cfg.FPPEndpoints is overwritten with the AUTHORITATIVE
	// list (the store's active configuration, or the freshly migrated
	// environment value); nothing downstream — apiDeps, the FPP collector
	// construction loop, the FPPMQTTHosts cross-check below — may read the
	// raw env-parsed value again.
	// fppEndpointsMigrationDeferred is the migration-could-not-be-persisted
	// fact (configsync.go), threaded to api.Dependencies below so the
	// configuration read and write handlers can state it instead of
	// reporting a coordinator that nothing has ever configured.
	cfg, fppEndpointsMigrationDeferred, err := resolveAuthoritativeFPPEndpoints(ctx, st, identitySvc, cfg, time.Now, logger)
	if err != nil {
		logger.Error("failed to resolve the authoritative fpp.endpoints configuration", "error", err)
		_ = st.Close()
		return 1
	}

	// config.Config.Validate already cross-checked SHOWMESH_FPP_MQTT_HOSTS
	// against cfg.FPPEndpoints as parsed from the ENVIRONMENT (deferred,
	// not skipped, when that was empty — see
	// internal/coordinator/config's validateFPPMQTTConfig doc comment).
	// Now that cfg.FPPEndpoints is the store-authoritative list, which may
	// differ in EXISTENCE (env unset, store populated) from what config.Validate
	// saw, the identical rule is re-run here against the list this
	// coordinator will actually use — this is the "must keep working
	// against the store-sourced list ... and must not silently stop
	// running" requirement from this seam's own spec, not a redundant
	// check: it is the only place that requirement can be discharged once
	// SHOWMESH_FPP_ENDPOINTS is no longer the source of truth.
	if err := config.ValidateFPPMQTTHostIDs(cfg.FPPMQTTHosts, cfg.FPPEndpoints); err != nil {
		logger.Error("SHOWMESH_FPP_MQTT_HOSTS no longer cross-checks against the authoritative fpp.endpoints configuration", "error", err)
		_ = st.Close()
		return 1
	}

	// Track G seam G-2 (ADR-039): the SHOWMESH_RESOLUME_URL/SHOWMESH_RESOLUME_ID
	// -> store migration and the identical disagreement rule
	// resolveAuthoritativeFPPEndpoints already applies to its own
	// variable(s) — see resolumeinstancessync.go. From this point on,
	// envResolumeInstances is never read again: resolumeInstances is the
	// AUTHORITATIVE list (0 or 1 entries) everything downstream — the
	// boot-time collision re-check immediately below, and resolumeManager's
	// own initial reconcile further down — must use instead.
	var envResolumeInstances []config.ResolumeInstance
	if cfg.ResolumeURL != "" {
		envResolumeInstances = []config.ResolumeInstance{{ID: cfg.ResolumeID, URL: cfg.ResolumeURL}}
	}
	resolumeInstances, resolumeInstancesMigrationDeferred, err := resolveAuthoritativeResolumeInstances(ctx, st, identitySvc, envResolumeInstances, time.Now, logger)
	if err != nil {
		logger.Error("failed to resolve the authoritative resolume.instances configuration", "error", err)
		_ = st.Close()
		return 1
	}

	// Track D seam D-1's identical re-check, for the identical reason:
	// config.Config.Validate's own validateResolumeConfig only ever saw
	// cfg.FPPEndpoints and cfg.ResolumeURL/ResolumeID as parsed from the
	// ENVIRONMENT, which may differ in existence (env unset, store
	// populated) from what each side resolved to just above. A deployment
	// that migrated either variable into the store and removed it, then
	// later configured the other side to a value whose id now happens to
	// collide, would sail past config.Validate with nothing to compare
	// against — this is the only place left that can still catch it before
	// either collector is registered on the shared Runner below.
	//
	// resolumeConfiguredID is threaded into apiDeps.ResolumeID below —
	// see that field's own doc comment for why an empty string there means
	// exactly "no Resolume instance configured", never a value nothing can
	// actually collide with.
	if err := config.ValidateResolumeInstances(resolumeInstances, cfg.FPPEndpoints); err != nil {
		logger.Error("resolume.instances no longer cross-checks against the authoritative fpp.endpoints configuration", "error", err)
		_ = st.Close()
		return 1
	}
	var resolumeConfiguredID string
	if len(resolumeInstances) > 0 {
		resolumeConfiguredID = resolumeInstances[0].ID
	}

	// Track G seam G-3 (ADR-039): the SHOWMESH_FPP_MQTT_* -> store
	// migration and disagreement rule, mirroring resolume.instances above
	// — see fppmqttsync.go. From this point on, envFPPMQTT/envFPPMQTTPassword
	// are never read again: fppMQTTCfg/fppMQTTPassword are AUTHORITATIVE.
	envFPPMQTT := config.FPPMQTTConfig{
		BrokerURL: cfg.FPPMQTTBrokerURL, Username: cfg.FPPMQTTUsername,
		TopicPrefix: cfg.FPPMQTTTopicPrefix, Hosts: cfg.FPPMQTTHosts,
	}
	fppMQTTCfg, fppMQTTPassword, fppMQTTMigrationDeferred, err := resolveAuthoritativeFPPMQTT(ctx, st, identitySvc, cfg.DataDir, envFPPMQTT, cfg.FPPMQTTPassword, time.Now, logger)
	if err != nil {
		logger.Error("failed to resolve the authoritative fpp.mqtt configuration", "error", err)
		_ = st.Close()
		return 1
	}
	// The identical re-check config.ValidateFPPMQTTHostIDs already performed
	// against the env-parsed list at config-load time, re-run against the
	// AUTHORITATIVE fpp.endpoints and fpp.mqtt lists — see the fpp.mqtt
	// hosts cross-check above this block for why env-parsed alone is not
	// enough once either side may be store-authoritative instead.
	if err := config.ValidateFPPMQTTHostIDs(fppMQTTCfg.Hosts, cfg.FPPEndpoints); err != nil {
		logger.Error("fpp.mqtt no longer cross-checks against the authoritative fpp.endpoints configuration", "error", err)
		_ = st.Close()
		return 1
	}

	// hub is assigned below, once api.New has built it, but inv (which can
	// start delivering MQTT messages the instant broker.NewBrokerManager
	// begins connecting) needs a notify callback wired in before that
	// happens. Capturing hub by reference in a closure — rather than
	// building inv after hub exists — keeps this file's construction order
	// matching every other package's existing top-to-bottom shape (store,
	// then inventory, then the broker) instead of reordering Step 2's own
	// wiring to satisfy Step 3. The closure never runs concurrently with
	// this assignment: nothing that can call it (the broker's message
	// delivery) starts until broker.NewBrokerManager is called, several
	// lines below where hub is actually set.
	var hub *api.Hub
	notifyHub := func() {
		if hub != nil {
			hub.Notify()
		}
	}

	// Track B seam B2b: the push cache behind
	// internal/coordinator/collector/noderender. Constructed here,
	// unconditionally (a render node is optional; this cache costs nothing
	// when nothing ever publishes to it), and handed to inv below so
	// HandleMessage's "render" case has somewhere to push a decoded report
	// — see inventory.WithRenderSink. renderCollector (registered on
	// fppRunner further down, once fppRunner exists) reads back out of the
	// SAME store, on its own poll cadence.
	renderStore := noderender.NewStore()

	// Track C seam C1a: audio's own push cache, the identical shape as
	// renderStore above, one report type over — see nodeaudio's package
	// doc comment. *store.Store already satisfies nodeaudio.ClockDomainSource
	// directly, so a node never claims its own clock domain (that is
	// operator-declared audio.node configuration, read live on every poll).
	audioStore := nodeaudio.NewStore(nodeaudio.WithClockDomainSource(st))

	inv := inventory.New(st, logger, inventory.WithOnChange(notifyHub), inventory.WithRenderSink(renderStore), inventory.WithAudioSink(audioStore))

	bm, err := broker.NewBrokerManager(ctx, cfg, logger, inv.Subscriptions(), inv.HandleMessage)
	if err != nil {
		logger.Error("failed to start mqtt connection manager", "error", err)
		_ = st.Close()
		return 1
	}

	// Track G seam G-4 (ADR-039): the SHOWMESH_ASSET_CONTENT_BASE_URL/
	// SHOWMESH_ASSET_MAX_UPLOAD_BYTES/SHOWMESH_ASSET_SYNC_INTERVAL/
	// SHOWMESH_ASSET_INVENTORY_INTERVAL -> store migration and the identical
	// disagreement rule resolveAuthoritativeResolumeInstances already
	// applies to its own variable(s) — see assetsettingssync.go.
	// SHOWMESH_ASSET_DIR is never part of this (ADR-039 decision 2) and
	// stays read directly from cfg below.
	var envAssetSettings config.AssetSettings
	if cfg.AssetSettingsEnvVarsSet {
		envAssetSettings = config.AssetSettings{
			ContentBaseURL:    cfg.AssetContentBaseURL,
			MaxUploadBytes:    cfg.AssetMaxUploadBytes,
			SyncInterval:      cfg.AssetSyncInterval,
			InventoryInterval: cfg.AssetInventoryInterval,
		}
	}
	assetSettings, assetSettingsMigrationDeferred, err := resolveAuthoritativeAssetSettings(ctx, st, identitySvc, cfg.AssetSettingsEnvVarsSet, envAssetSettings, time.Now, logger)
	if err != nil {
		logger.Error("failed to resolve the authoritative assets.settings configuration", "error", err)
		_ = st.Close()
		return 1
	}

	// Track E seam E5/E6: the asset manifest's sync service (ADR-028
	// decision 7). Constructed unconditionally, over the SAME
	// *broker.BrokerManager (bm) inventory just subscribed through, since
	// asset.fetch commands are dispatched on the control-plane broker
	// exactly like every other node command. Seeded with the AUTHORITATIVE
	// assets.settings resolved just above; assetSettingsSource/
	// runAssetSettingsReconciler (assetsettingsmanager.go) keep it current
	// without a restart from here on (ADR-039 decision 6). assetSync.Run
	// itself checks its own live Enabled() and skips dispatch work — never
	// returns — when it is false (see assetsync.Service.Run's own doc
	// comment) — there is no separate "if configured" gate here, matching
	// that method's own contract rather than duplicating its condition.
	assetSync := assetsync.NewService(st, bm, logger, toAssetSyncSettings(assetSettings))
	// Seeded with the boot-resolved settings so a deferred migration (or a
	// store read failing before the source's first successful read) keeps
	// the reconciler answering these values rather than the defaults.
	assetSettingsSrc := newAssetSettingsSource(st, logger, assetSettings)

	// The volume backend owns the asset bytes; SQLite holds only their
	// metadata (ADR-028). A backend that cannot be opened is fatal, because
	// the alternative is an upload surface that accepts a request and has
	// nowhere to put it.
	assetBackend, assetBackendErr := assetstore.NewVolumeBackend(cfg.AssetDir)
	if assetBackendErr != nil {
		logger.Error("failed to open the asset store directory", "dir", cfg.AssetDir, "error", assetBackendErr)
		return 1
	}

	// fppRunner is constructed here — BEFORE apiDeps, not after it the way
	// this file had every other FPP collector wiring line ordered before
	// the 2026-08-13 post-dispatch poll nudge — so apiDeps.Nudger
	// (fppRunnerNudger, below) can wrap the real *collector.Runner instead
	// of a value that does not exist yet. No collector.Runner method used
	// here (Add, or NudgePoll's own Nudge) requires Run to have started,
	// so constructing the Runner early and adding every configured FPP
	// endpoint's collector to it further down (unchanged from before this
	// reordering) is safe regardless of when api.New or apiInst's own
	// construction happens relative to it.
	fppHTTPClient := &http.Client{
		Transport: &http.Transport{
			MaxIdleConnsPerHost: 4,
			IdleConnTimeout:     90 * time.Second,
		},
	}
	fppRunner := collector.NewRunner(&fppSink{st: st, notify: notifyHub, logger: logger}, logger)

	// Track B seam B2b: renderStore (built above, already wired into inv)
	// gets its own read side here — a Collector sharing this same
	// fppRunner rather than a second Runner, matching how the Resolume
	// collector joins it too (see newResolumeWiring's own doc comment for
	// why one Runner is the standing pattern for "another observation
	// source, no reason for its own goroutine-management copy"). Renders
	// whatever renderStore currently holds into surface.* observations on
	// its own cadence; see noderender's package doc comment for why this
	// never touches the network and is unconditional (a coordinator with
	// no render node ever attached simply Polls an empty store forever).
	//
	// Seeded from st's own persisted rows (seedNodeRenderKnownSurfaces)
	// so a coordinator restart does not forget every surface a still-live
	// node reported before the restart — without this, the FIRST poll
	// after every restart would see nothing to diff against and could
	// never emit the dropped-surface absence noderender.Collector.Poll
	// depends on, turning every already-deployed surface row into a
	// permanent ghost.
	knownSurfaces, err := seedNodeRenderKnownSurfaces(ctx, st)
	if err != nil {
		logger.Warn("failed to seed node-render known-surfaces from the store; starting with none", "error", err)
	}
	fppRunner.Add(noderender.New(renderStore, noderender.WithKnownSurfaces(knownSurfaces)), noderender.DefaultPollInterval)

	// Track C seam C1a: audioStore's own read side, sharing fppRunner for
	// the identical reason renderStore's does (see noderender's own
	// comment above) — no per-node dynamic list to seed here (engine/
	// device/program/ltc are fixed, one-per-node signals), so this needs
	// no known-surfaces-style restart bookkeeping.
	fppRunner.Add(nodeaudio.New(audioStore), nodeaudio.DefaultPollInterval)

	// Step 9 (STEP-9-SPEC.md section 2.10, wave 2 shared contract section
	// 5): one *broker.BrokerManager per declared external MQTT broker
	// (SHOWMESH_INTEGRATION_BROKERS), registered under its own identifier.
	// Built here, alongside every other connection this coordinator owns,
	// and BEFORE apiDeps for the identical reason fppRunner is: the macro
	// executor built below needs a live registry to hand its own
	// Dependencies.Brokers field, and apiDeps.Macros needs the executor.
	// The control-plane broker (bm, above) is never registered here under
	// any identifier — see buildIntegrationBrokerRegistry's own doc
	// comment for why that is a property of its INPUT, not a check it
	// performs.
	integrationBrokers, integrationBrokerManagers, err := buildIntegrationBrokerRegistry(ctx, cfg, logger)
	if err != nil {
		logger.Error("failed to build integration broker registry", "error", err)
		_ = bm.Disconnect(ctx)
		_ = st.Close()
		return 1
	}

	// Track D seam D-2/B: the stored composition's tracked-object set
	// (internal/coordinator/collector/resolume's CompositionStore),
	// constructed here — regardless of whether any Resolume instance is
	// configured, and BEFORE resolumeMgr just below — for the reason
	// resolumeCompositionWiring's own doc comment gives: composition
	// upload has no relationship to whether a live Resolume instance is
	// configured, so this must not wait on that condition. Never fatal:
	// see newResolumeCompositionWiring's own doc comment for why a failure
	// to load here is logged and this coordinator still starts.
	resolumeCompositionWire := newResolumeCompositionWiring(ctx, st, logger)

	// Track D seam C: the write-time reference resolver
	// (config.ResolumeReferenceResolver), built over
	// resolumeCompositionWire.store directly above — unconditionally, like
	// that store itself, so a resolume show.action can be authored and
	// validated whether or not a live Resolume instance is configured
	// (resolumereferencewiring.go's own doc comment).
	resolumeReferences := newResolumeReferenceResolverAdapter(resolumeCompositionWire.store)

	// fppEndpoints resolves the ACTIVE fpp.endpoints revision on demand
	// rather than handing every consumer the list read at startup. See
	// internal/coordinator/fppendpoints.go's own file comment for the
	// measurement that made this necessary and the owner decision behind
	// it; the short version is that a removed endpoint used to keep
	// receiving commands until someone restarted this process.
	fppEndpoints := newFPPEndpointSource(st, logger)

	// Track G seam G-2 (ADR-039): resolumeManager owns the live
	// collector/watcher/action-dispatcher/recovery bundle for whichever
	// Resolume instance resolume.instances currently names (0 or 1), and
	// reconciles it against that configuration with no restart in either
	// direction — see resolumemanager.go's own file comment. It replaces
	// the old one-shot newResolumeWiring/newResolumeActionDispatcherAdapter/
	// newResolumeRecoveryWiring call sequence this coordinator used to run
	// exactly once, right here, for the process's whole life; every one of
	// those three constructors is now called by resolumeMgr itself, on
	// every reconfiguration.
	//
	// The built-in recovery principal is ensured to exist regardless of
	// whether an instance is currently configured — it costs one idempotent
	// SQLite read/insert at startup and keeps "the principal exists" true
	// even for a coordinator that connects Resolume later with no restart.
	ensureResolumeRecoveryPrincipal(ctx, identitySvc, logger)
	resolumeMgr := newResolumeManager(cfg, fppRunner, resolumeCompositionWire.store, st, identitySvc, logger, notifyHub)
	// The FIRST reconcile runs synchronously, here, BEFORE the HTTP server
	// (further down) ever opens its listener — see resolumeMgr.Run's own
	// doc comment for why: a request served the instant the listener opens
	// must already see live state, never a manager that has not yet run
	// its first pass.
	resolumeMgr.reconcile(ctx, resolumeInstances)
	// Seeded with the boot-resolved instance list so a deferred migration
	// (or a store read failing before the source's first successful read)
	// never manufactures an empty list and tears the bundle above down.
	resolumeInstanceSrc := newResolumeInstanceSource(st, logger, resolumeInstances)

	// Track G seam G-3 (ADR-039): fppMQTTManager owns the live
	// *fppmqtt.Collector for whatever fpp.mqtt currently configures,
	// mirroring resolumeMgr immediately above — see fppmqttmanager.go's own
	// file comment. It replaces the old one-shot construction below cfg's
	// FPPEndpoints loop used to perform exactly once, for the process's
	// whole life.
	// Seeded with the boot-resolved configuration for the same reason the
	// Resolume source above is; the manager also answers CurrentHosts from
	// this source, so the fpp.endpoints collision check sees the stored
	// hosts even when no collector bundle is running.
	fppMQTTConfigSrc := newFPPMQTTConfigSource(st, cfg.DataDir, logger, fppMQTTCfg, fppMQTTPassword)
	fppMQTTMgr := newFPPMQTTManager(fppRunner, fppMQTTConfigSrc, logger)
	// The FIRST reconcile runs synchronously, here, for the identical
	// "no request may observe a partially-wired dependency" reason
	// resolumeMgr.reconcile above does.
	fppMQTTMgr.reconcile(ctx, fppMQTTCfg, fppMQTTPassword)

	// The FPP REST collector (Task C) and the versioned control API (Task
	// D) were each built against interfaces they declared themselves,
	// never against each other's or the store's concrete types (contract
	// section 5: "declare interfaces at the consumer, not the producer").
	// Everything from here to api.New is the adapter layer
	// (internal/coordinator/apiwiring.go) that makes the store and config
	// satisfy those interfaces.
	apiDeps := api.Dependencies{
		// livenessObservingNodeLister (internal/coordinator/apiwiring.go)
		// wraps inv so every Snapshot call — not only one triggered by an
		// inbound MQTT message — also feeds each node's freshly computed
		// Liveness back into inv's own transition bookkeeping. Step 3
		// review finding 3.4: without this, a node whose heartbeats simply
		// stopped (no further message, no last will) transitioned online
		// -> offline by staleness alone with nothing recording it to event
		// history. See that type's doc comment for the full reasoning.
		Nodes:        livenessObservingNodeLister{inv: inv},
		FPP:          fppInstanceLister{st: st, endpoints: fppEndpoints},
		Observations: storeObservationLister{st: st},
		Events:       storeEventReader{st: st},
		// Track B seam B2b: renderStore already satisfies
		// api.NodeRenderLister's NodeRenderObservations method directly, no
		// adapter needed, the same "the real dependency already has this
		// method set" pattern api.ConfigStore's own wiring below uses.
		Render: renderStore,
		// Track C seam C1a/C1b: audioStore already satisfies
		// api.NodeAudioLister's NodeAudioObservations method directly, no
		// adapter needed, matching renderStore's identical wiring above.
		Audio: audioStore,
		// RenderPublisher is Track B seam B2b-front's own dependency: the
		// SAME *broker.BrokerManager (bm) assetSync's own Publisher was
		// built from above already satisfies api.RenderPublisher with no
		// adapter needed, the identical property assetSync's own wiring
		// comment notes for itself. Wiring it in is what makes
		// POST /api/v1/nodes/{nodeId}/render/surfaces/{surfaceId}/apply|
		// clear|restart do anything other than always answer an internal
		// error naming the missing wiring, against api.noRenderPublisher's
		// no-op default.
		RenderPublisher: bm,
		// Step 5 (contract section 5.4): both FPP collector sources must be
		// visible in /api/v1/snapshot's collectors[] — a second source that
		// is invisible there is a source an operator cannot tell is broken.
		// multiCollectorStatusLister (apiwiring.go) concatenates the two;
		// fppMQTTMgr (Track G seam G-3) reports its own bundle live, so this
		// line can never disagree with whether FPP MQTT collection is
		// actually running.
		Collectors: multiCollectorStatusLister{
			fppCollectorStatusLister{endpoints: fppEndpoints},
			// Track G seam G-3: fppMQTTMgr reports its OWN current state
			// live, replacing the old fppMQTTCollectorStatusLister startup
			// snapshot — mirroring resolumeMgr immediately below.
			fppMQTTMgr,
			// Track G seam G-2: resolumeMgr reports its OWN current state
			// live (api.CollectorStatuses is called per request), so this
			// entry and the manager's own actual bundle can never disagree —
			// the identical guarantee the FPP MQTT comment above states for
			// its own pair, now true across a no-restart reconfiguration too.
			resolumeMgr,
		},
		// Identity is ADR-024's own dependency: wiring it in is what makes
		// POST/GET/DELETE /api/v1/session, POST /api/v1/bootstrap, and
		// GET /api/v1/audit do anything other than always answer 401/403
		// against api.noIdentityService's no-op default.
		Identity: identitySvc,
		// Config is Step 7 seam A's read half of the configuration write
		// surface (RES-008 D1) — *store.Store already satisfies
		// api.ConfigStore directly (see that interface's doc comment), no
		// adapter needed, the same way Identity is wired directly above.
		Config: st,
		// Track E: *store.Store satisfies api.AssetStore directly, the same
		// way it satisfies Config above.
		Assets:       st,
		AssetBackend: assetBackend,
		// AssetManifests is seam E5's own dependency: *store.Store, not an
		// interface (see that field's own doc comment for why), wired the
		// same *st already used for Config/Assets/Commands/Discovery above.
		AssetManifests: st,
		// AssetSettings is Track G seam G-4's live, no-restart view of the
		// assets.settings configuration kind (ADR-039 decision 6): the SAME
		// *assetsync.Service constructed above straight in. It already
		// satisfies api.AssetSettingsSource AND api.AssetSyncNudger with no
		// adapter needed (assetmanifest.go's own compile-time assertions),
		// so the upload byte limit, the manifest staleness window, and the
		// sync-enabled note can never disagree with what Service.Run itself
		// is currently using — there is exactly one live holder of this
		// configuration kind, reached through one field. This replaces the
		// old AssetMaxUploadBytes/AssetInventoryInterval/AssetSyncEnabled
		// startup-snapshot fields, which could not change without a restart.
		AssetSettings: assetSync,
		// AssetSyncNudger wires the SAME *assetsync.Service constructed
		// above straight in: its Nudge method already satisfies
		// api.AssetSyncNudger with no adapter needed (the identical
		// property fppRunnerNudger's own doc comment notes for
		// *collector.Runner one field over). This is what makes an upload
		// or a show activation trigger a sync pass immediately instead of
		// waiting out the configured sync interval (up to 5 minutes) — the
		// capability existed and was tested but had no production caller
		// until this line.
		AssetSyncNudger: assetSync,
		// AssetSettingsEnvVarsSet/AssetSettingsMigrationDeferred are Track G
		// seam G-4's mirror of ResolumeInstancesEnvVarSet/
		// ResolumeInstancesMigrationDeferred above — see those two fields'
		// own doc comments, which apply unchanged here.
		AssetSettingsEnvVarsSet:        cfg.AssetSettingsEnvVarsSet,
		AssetSettingsMigrationDeferred: assetSettingsMigrationDeferred,
		// FPPEndpointsEnvVarSet plumbs cfg.FPPEndpointsEnvSet — the RAW,
		// pre-migration fact of whether SHOWMESH_FPP_ENDPOINTS is set in
		// THIS PROCESS's environment — into the API package, which must
		// never read the environment itself (see [api.Dependencies]'s own
		// doc comment). Defect 3a (Step 7 seam A review): PUT
		// /api/v1/config/fpp.endpoints refuses with 409 while this is
		// true, because a write that succeeds now cannot actually survive
		// the coordinator's own disagreement rule (configsync.go) on the
		// very next restart — the refusal belongs at the moment of the
		// mistake, while this coordinator is still up and the operator can
		// still read why, not after a restart that never completes.
		FPPEndpointsEnvVarSet: cfg.FPPEndpointsEnvSet,
		// FPPEndpointsMigrationDeferred plumbs the other half of the same
		// fact: SHOWMESH_FPP_ENDPOINTS is set AND the migration of it into
		// the store could not be persisted on this boot, so this
		// coordinator is collecting from a list the store does not hold.
		// Without it the read handler cannot tell that state apart from a
		// coordinator nothing has ever configured, and the write handler's
		// 409 gives a remedy that would silently discard the endpoint list
		// (see both handlers in api/config.go).
		FPPEndpointsMigrationDeferred: fppEndpointsMigrationDeferred,
		// FPPMQTTHostIDs plumbs cfg.FPPMQTTHosts — the SHOWMESH_FPP_MQTT_HOSTS
		// mapping — into the API package for the identical reason: defect
		// 4 (Step 7 seam A review), PUT /api/v1/config/fpp.endpoints must
		// refuse an endpoint list that would break the MQTT host
		// cross-check startup already enforces fatally
		// ([config.ValidateFPPMQTTHostIDs] above), naming the host id at
		// write time rather than accepting 200 and refusing to boot on the
		// next restart.
		FPPMQTTHostIDs: cfg.FPPMQTTHosts,
		// ResolumeID plumbs resolumeConfiguredID (resolved from the
		// AUTHORITATIVE resolume.instances list, above) through to
		// api.Dependencies — see that field's own doc comment for why an
		// empty string means exactly "no Resolume instance configured".
		// This is now a startup snapshot ONLY: handlePutFPPEndpointsConfig
		// (api/config.go) additionally re-reads resolume.instances live at
		// write time (Track G seam G-2), so a Resolume instance connected
		// or changed after this coordinator started is still caught.
		ResolumeID: resolumeConfiguredID,
		// Deliberately no ResolumeCompositionID field here (Track D seam
		// D-2a review finding F): the stored composition's config_objects
		// id is a fixed constant inside the api package
		// (resolumeCompositionObjectIDConst in resolumecomposition.go),
		// not derived from any Resolume instance id. See that constant's
		// own doc comment.
		//
		// ResolumeActions, Resolume, and ResolumeRecovery are all wired to
		// the SAME resolumeMgr (Track G seam G-2, resolumemanager.go),
		// constructed once, above, before this coordinator's HTTP server
		// ever starts serving. Every request through any of the three sees
		// resolumeMgr's CURRENT bundle, live — unlike every other field in
		// this struct, these three can change what they answer while this
		// process runs, because resolume.instances applies without a
		// restart (ADR-036) and the old per-process nil defaults these used
		// to hold (noResolumeActionDispatcher, noResolumeLister,
		// noResolumeRecoveryProvider) were a startup-only snapshot of
		// exactly one condition: cfg.ResolumeURL != "".
		ResolumeActions:    resolumeMgr,
		ResolumeReferences: resolumeReferences,
		Resolume:           resolumeMgr,
		ResolumeRecovery:   resolumeMgr,
		// ResolumeRecoverySettleSeconds is threaded through unconditionally
		// (matching FPPMQTTTopicPrefix's own "defaults regardless of
		// whether the feature is active" posture) since it costs nothing
		// when unused.
		ResolumeRecoverySettleSeconds: cfg.ResolumeRecoverySettle.Seconds(),
		// ResolumeInstancesEnvVarSet/ResolumeInstancesMigrationDeferred are
		// Track G seam G-2's mirror of FPPEndpointsEnvVarSet/
		// FPPEndpointsMigrationDeferred above — see those two fields' own
		// doc comments, which apply unchanged here.
		ResolumeInstancesEnvVarSet:         cfg.ResolumeURL != "",
		ResolumeInstancesMigrationDeferred: resolumeInstancesMigrationDeferred,
		// FPPMQTT is Track G seam G-3's live host map, read from fppMQTTMgr
		// (the SAME manager wired into Collectors above) rather than a
		// startup snapshot — see api.FPPMQTTHostLister's own doc comment.
		FPPMQTT: fppMQTTMgr,
		// FPPMQTTSecret is Track G seam G-3's write-only credential surface
		// (ADR-039 decision 7), backed by the secret file under cfg.DataDir
		// — see fppMQTTSecretAdapter (apiwiring.go).
		FPPMQTTSecret: fppMQTTSecretAdapter{dataDir: cfg.DataDir},
		// FPPMQTTEnvVarSet/FPPMQTTMigrationDeferred are
		// FPPEndpointsEnvVarSet/FPPEndpointsMigrationDeferred's mirror for
		// Track G seam G-3 — see those two fields' own doc comments.
		FPPMQTTEnvVarSet:         cfg.FPPMQTTBrokerURL != "",
		FPPMQTTMigrationDeferred: fppMQTTMigrationDeferred,
		// Commands is Step 7 seam C's own dependency: *store.Store already
		// satisfies api.CommandStore with no adapter (api.go's own
		// compile-time assertion) — wiring it in is what makes
		// POST /api/v1/fpp/{instanceId}/commands do anything other than
		// always answer 500 against api.noCommandStore's no-op default.
		Commands: st,
		// Discovery is BUILD-PLAN Step 7 seam B's dependency (RES-008
		// D2/D6): *store.Store already satisfies api.DeclarationStore
		// directly, with no adapter — see that interface's own doc
		// comment. Wiring it in is what makes POST /api/v1/discovery/runs
		// and POST/DELETE /api/v1/nodes/{nodeId}/declaration do anything
		// other than always answer a "not configured" internal error
		// against api.noDeclarationStore's no-op default.
		Discovery: st,
		// Nudger is the post-dispatch poll nudge's dependency (owner
		// decision, 2026-08-13; api.FPPPollNudger's own doc comment has
		// the full contract): fppRunnerNudger wraps the SAME
		// *collector.Runner constructed above, whose Add calls further
		// down register every configured FPP instance's own collector
		// under the identical instance ID this wraps NudgePoll against —
		// wiring it in is what turns
		// POST /api/v1/fpp/{instanceId}/commands' confirmation wait from
		// "always the collector's own ~15s cadence" into "usually one LAN
		// round trip, falling back to that same cadence whenever the
		// nudge is suppressed or fails" against api.noFPPPollNudger's
		// no-op default.
		Nudger: fppRunnerNudger{runner: fppRunner},
		// IntegrationBrokers is Step 9's own dependency (wave 2 shared
		// contract section 5): the declared set show.action's own mqtt
		// target write-time validation checks a broker identifier
		// against (showconfig.go's decodeMQTTTarget, one layer up in
		// internal/coordinator/config). This is the SAME cfg.IntegrationBrokers
		// buildIntegrationBrokerRegistry just built actual connections
		// from, above — the validation set and the connection set can
		// never disagree about which identifiers exist, because both
		// read the identical field.
		IntegrationBrokers: cfg.IntegrationBrokers,
	}

	// apiOpts is named (not inlined into api.New's own call, as it used to
	// be) because Step 9's macro executor needs the IDENTICAL Dependencies
	// and Options api.New itself uses to build its own second, in-process
	// dispatch core (api.NewFPPCommandDispatcher's own doc comment: "the
	// supported pattern for a coordinator wiring both this API's HTTP
	// surface and Step 9's macro executor... both then dispatch through
	// the identical core"). apiDeps.Macros is set further down, AFTER
	// macroExecutor exists, so it does not need a value yet at the point
	// macroDispatcher below is built — NewFPPCommandDispatcher never reads
	// Dependencies.Macros.
	apiOpts := api.Options{
		CloseReads:          cfg.CloseReads,
		SecureCookie:        cfg.SecureCookie,
		TrustClientAddr:     cfg.TrustClientAddr,
		LoginConcurrency:    cfg.LoginConcurrency,
		LoginQueueWait:      cfg.LoginQueueWait,
		LoginPerSourceDelay: cfg.LoginPerSourceDelay,
		LoginMaxDelay:       cfg.LoginMaxDelay,
		AllowedOrigins:      cfg.APIAllowedOrigins,
		Logger:              logger,
	}

	// Step 9: the macro executor (internal/coordinator/macro), built
	// against the SAME apiDeps/apiOpts api.New itself uses — see
	// api.NewFPPCommandDispatcher's own doc comment for why two
	// independently-constructed *handlers values sharing one Dependencies
	// behave identically to one shared value for every purpose this
	// dispatch core exists for. macroExecutor.Reconcile is called below,
	// synchronously, before this coordinator starts listening (ADR-031
	// decision 4), alongside api.ReconcileStrandedFPPCommands.
	macroDispatcher := api.NewFPPCommandDispatcher(apiDeps, apiOpts)
	macroExecutor := macro.NewExecutor(macro.Dependencies{
		Store:    st,
		Identity: identitySvc,
		Dispatch: macroDispatcher,
		Brokers:  integrationBrokers,
		// ResolumeActions is the SAME value apiDeps.ResolumeActions holds
		// (resolumeMgr, wired above) — Track D seam C's own "one dispatch
		// path" rule: a macro's Resolume step and the HTTP endpoint dispatch
		// through the identical api.ResolumeActionDispatcher, never two
		// independently-wired copies of it.
		ResolumeActions: apiDeps.ResolumeActions,
		Notify:          notifyHub,
		Clock:           time.Now,
		Logger:          logger,
	}, macro.Options{})
	apiDeps.Macros = macroExecutor

	apiInst := api.New(apiDeps, apiOpts)
	hub = apiInst.Hub

	// Step 7 seam C review defect 5: resolve any command a PRIOR process
	// left dispatched-but-unresolved (a crash, a kill, or an abandoned
	// client connection between dispatch and outcome) before it can sit
	// blank forever. Finding 3 (Step 8 review): this used to be
	// `go func() { ... }()`, which is NOT a happens-before edge against
	// srv.ListenAndServe() below — api.ReconcileStrandedFPPCommands's own
	// doc comment claimed "before the HTTP server starts accepting
	// connections" while the code raced it, and a live request confirming
	// a command concurrently with this sweep was proved to resolve twice
	// (two outcome audit entries for the same command_id, one from each
	// racing path). Called SYNCHRONOUSLY here instead, so it genuinely
	// completes before the goroutine that calls ListenAndServe is even
	// started, making that doc comment's claim true rather than aspirational.
	//
	// Deliberately NOT fatal on error: this is a bounded local SQLite
	// scan, which is what makes blocking acceptable, but ADR-024
	// constraint 23 draws the line at "you cannot act", never "you cannot
	// see" — a store misbehaving here has no principal to hold
	// accountable for refusing to boot, so a failure is logged and the
	// coordinator still proceeds to start listening.
	if n, rerr := api.ReconcileStrandedFPPCommands(ctx, apiDeps, time.Now, logger); rerr != nil {
		logger.Warn("failed to reconcile stranded fpp commands at startup", "error", rerr)
	} else if n > 0 {
		logger.Warn("resolved commands left stranded by a prior process", "count", n)
	}

	// ADR-031 decision 4 / STEP-9-SPEC.md section 6.5: any macro run left
	// "running" by a prior process is finished completed:false, never
	// resumed — called synchronously, alongside the identical stranded-
	// command sweep immediately above and for the identical reason (this
	// coordinator must not start listening, and so must not accept a new
	// run submission, until both sweeps have settled what a prior process
	// left behind). Deliberately NOT fatal on error, for the identical
	// reasoning ReconcileStrandedFPPCommands' own non-fatal treatment
	// states above: constraint 23 draws the line at "you cannot act",
	// never "you cannot see".
	if rerr := macroExecutor.Reconcile(ctx); rerr != nil {
		logger.Warn("failed to reconcile stranded macro runs at startup", "error", rerr)
	}

	// Review fix 1 (2026-08-15): the Resolume-action sibling of the sweep
	// immediately above, closing the identical gap for a second command
	// family — see api.ReconcileStrandedResolumeActions' own doc comment
	// (resolumeaction_reconcile.go) for why a Resolume row a prior process
	// left dispatched-but-unresolved used to replay a blank outcome
	// forever. Same synchronous, non-fatal call shape and the same
	// reasoning for why it is safe to call before ListenAndServe below.
	if n, rerr := api.ReconcileStrandedResolumeActions(ctx, apiDeps, time.Now, logger); rerr != nil {
		logger.Warn("failed to reconcile stranded resolume actions at startup", "error", rerr)
	} else if n > 0 {
		logger.Warn("resolved resolume actions left stranded by a prior process", "count", n)
	}

	// fppHTTPClient and fppRunner were already constructed above (before
	// apiDeps), one shared *http.Client per contract/Task C's own guidance
	// ("callers SHOULD construct one *http.Client and pass it to every
	// fpp.New call") rather than one per instance — see that construction
	// site's own comment for why this reordering was necessary.
	for _, ep := range cfg.FPPEndpoints {
		// cfg.Validate already checked ep.ID's syntax and ep.URL's shape
		// (internal/coordinator/config's validateFPPEndpoints) using the
		// exact same rules fpp.New re-checks on its own — see that
		// function's doc comment for why the duplication is deliberate.
		// fpp.New failing here would mean those two checks have drifted
		// apart, which is a startup-worthy inconsistency in this binary,
		// not a per-endpoint condition to skip past silently.
		fppCollector, err := fpp.New(ep.ID, ep.URL, fpp.Options{HTTPClient: fppHTTPClient})
		if err != nil {
			logger.Error("failed to construct fpp collector", "instance_id", ep.ID, "error", err)
			_ = bm.Disconnect(ctx)
			_ = st.Close()
			return 1
		}
		fppRunner.Add(fppCollector, fpp.DefaultPollInterval)
	}

	// Keep that set matching the configuration while this process runs, so
	// an operator adding or removing an endpoint through the API does not
	// have to restart the coordinator for it to take effect. The listers
	// above already resolve the endpoint list live, which is what stops a
	// removed endpoint receiving commands; this is the other half, which
	// stops it being polled and starts polling a newly added one. Neither
	// half is correct alone — see internal/coordinator/fppendpoints.go.
	newFPPCollector := func(id, url string) (collector.Collector, error) {
		return fpp.New(id, url, fpp.Options{HTTPClient: fppHTTPClient})
	}
	go reconcileFPPCollectors(ctx, fppRunner, fppEndpoints, newFPPCollector, fppCollectorReconcileInterval, logger)

	// Track G seam G-3 (ADR-039): fppMQTTMgr already constructed and
	// registered its collector, if configured, in its first synchronous
	// reconcile call above (mirroring resolumeMgr) — replacing the old
	// one-shot construction this block used to perform here. See
	// fppmqttmanager.go's own file comment.

	// The store and the broker each contribute independently to /readyz;
	// readiness.Aggregate is not-ready as soon as either is, per ADR-011 —
	// see its doc comment for why this stays a single readiness.Source
	// rather than a change to httpapi.NewServer's signature. The FPP
	// collector deliberately never joins this aggregate: an FPP switched off
	// in July must not make /readyz 503 and restart-loop the container
	// (contract section 3, Task C spec section 3 — "the FPP collector must
	// never make the coordinator not-ready"). An unreachable FPP is an
	// observation, reported through /api/v1/fpp, not a readiness fault.
	srv := httpapi.NewServer(cfg.HTTPAddr, readiness.Aggregate{bm, st}, logger)

	// Mount the versioned control API under /api/ as a plain http.Handler
	// (contract/Task F spec: "Pass the API in as an http.Handler mounted
	// under /api/; do not let httpapi learn what an observation is") —
	// httpapi.Server.Mount does exactly that and nothing more. Must happen
	// before ListenAndServe, per Mount's own doc comment.
	srv.Mount("/api/", apiInst.Handler)

	// hub.Run and fppRunner.Run each own a goroutine's worth of background
	// work tied to ctx (contract/Task F spec: "run Hub.Run(ctx) in a
	// goroutine tied to shutdown"; "start the collector runner ... stop
	// both cleanly on shutdown ... with no leaked goroutines"). Both are
	// started before the HTTP listener so a request or stream connection
	// can never race ahead of either being ready, and both are joined via
	// backgroundWG before this function returns — see the shutdown sequence
	// below — so a caller (and this task's own goroutine-count test) can
	// verify nothing is left running once Run returns.
	var backgroundWG sync.WaitGroup
	backgroundWG.Add(8)
	go func() {
		defer backgroundWG.Done()
		hub.Run(ctx)
	}()
	go func() {
		defer backgroundWG.Done()
		fppRunner.Run(ctx)
	}()
	// assetSync.Run owns Track E seam E5/E6's own periodic gap-close loop
	// (assetsync/sync.go), joined via the identical backgroundWG so
	// shutdown waits for it cleanly like every other background loop here.
	// Track G seam G-4 (ADR-039 decision 6): it no longer returns early
	// when disabled — it keeps looping and picks up a later assets.settings
	// change (including the zero-to-one transition) with no restart.
	go func() {
		defer backgroundWG.Done()
		assetSync.Run(ctx)
	}()
	// runAssetSettingsReconciler is the OTHER half of that same no-restart
	// guarantee: it re-reads the active assets.settings configuration every
	// assetSettingsReconcileInterval and applies any change to assetSync
	// live (assetsettingsmanager.go). The FIRST value assetSync ever holds
	// came from assetSettings, resolved synchronously above before this
	// goroutine — or the HTTP server — ever starts, mirroring
	// resolumeMgr.reconcile's identical "no request observes a pre-reconcile
	// state" property.
	go func() {
		defer backgroundWG.Done()
		runAssetSettingsReconciler(ctx, assetSettingsSrc, assetSync)
	}()
	// resolumeCompositionWire.Run owns Track D seam D-2/B's own periodic
	// refresh loop (resolumewiring.go), started unconditionally — see
	// resolumeCompositionWiring's own doc comment for why this does not
	// share resolumeMgr's "an instance is currently configured" gate.
	// Joined via the identical backgroundWG so shutdown waits for it
	// cleanly like every other background loop here.
	go func() {
		defer backgroundWG.Done()
		resolumeCompositionWire.Run(ctx)
	}()
	// watchUnclaimedBootstrap is ADR-024 decision 9's "loud and
	// persistent" unclaimed-bootstrap signal's other half — the log side,
	// alongside SessionResponse.bootstrapRequired's UI-banner side (see
	// internal/coordinator/api/session.go). It logs once immediately (so a
	// fresh coordinator's very first log lines already carry it, not only
	// GET /api/v1/session's own visibility once a browser opens) and then
	// repeats on bootstrapWarningInterval for as long as no principal
	// exists — decision 9's own reasoning for why one line at startup is
	// not enough: a volume loss or a move to a fresh host returns this
	// coordinator to zero principals with reads still open, so the
	// dashboard renders and nothing looks wrong unless something says so
	// loudly and keeps saying so.
	go func() {
		defer backgroundWG.Done()
		watchUnclaimedBootstrap(ctx, identitySvc, logger)
	}()

	// fppMQTTMgr.Run owns Track G seam G-3's own reconcile loop, mirroring
	// resolumeMgr.Run immediately below: it keeps the current
	// *fppmqtt.Collector bundle matching the active fpp.mqtt configuration,
	// live, with no restart in either direction, and tears it down on
	// ctx.Done() — see fppmqttmanager.go's own doc comment. Started
	// UNCONDITIONALLY, replacing the old cfg.FPPMQTTBrokerURL != "" gated
	// goroutine this block used to run: this loop is what notices a FIRST
	// broker being configured (the zero-to-one transition ADR-039 decision
	// 6 requires work with no restart). Counted in this function's own
	// backgroundWG.Add(8) above.
	go func() {
		defer backgroundWG.Done()
		fppMQTTMgr.Run(ctx)
	}()

	// resolumeMgr.Run owns Track G seam G-2's own reconcile loop: it keeps
	// the current bundle (collector, watcher, action dispatcher, recovery)
	// matching the active resolume.instances configuration, live, with no
	// restart in either direction — see resolumemanager.go's own doc
	// comment. Started UNCONDITIONALLY, unlike the old
	// resolumeWire.watcher-gated goroutine this replaces: this loop is what
	// notices a FIRST instance being configured (the zero-to-one transition
	// ADR-039 decision 6 requires work with no restart), so it must run
	// even when nothing is configured yet at this exact line. Joined via
	// the identical backgroundWG so shutdown waits for it cleanly, and its
	// own ctx.Done() branch tears down whatever bundle is still active
	// before returning (resolumeManager.Run's own doc comment) — mirroring
	// reconcileFPPCollectors' identical "always running, adds/removes as
	// configuration changes" shape one field over. Counted in this
	// function's own backgroundWG.Add(8) above, alongside hub.Run and
	// fppRunner.Run, rather than a standalone conditional Add — it is
	// unconditional, matching them.
	go func() {
		defer backgroundWG.Done()
		resolumeMgr.Run(ctx, resolumeInstanceSrc)
	}()

	serveErrCh := make(chan error, 1)
	go func() {
		logger.Info("http server listening", "addr", cfg.HTTPAddr)
		serveErrCh <- srv.ListenAndServe()
	}()

	select {
	case <-ctx.Done():
		logger.Info("shutdown signal received")
	case err := <-serveErrCh:
		if err != nil && err != http.ErrServerClosed {
			logger.Error("http server failed", "error", err)
			return 1
		}
	}

	// Stop intercepting the shutdown signals now, not just via the deferred
	// call: restoring default signal behavior here lets an operator send a
	// second Ctrl-C to force-exit a shutdown that hangs below. The deferred
	// stop() remains as a harmless, idempotent safety net for early-return
	// paths above.
	stop()

	shutdownCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	if err := srv.Shutdown(shutdownCtx); err != nil {
		logger.Warn("http server shutdown error", "error", err)
	}

	// ctx is already cancelled by this point (it is the same signal-derived
	// context that made the select above wake up), so hub.Run and
	// fppRunner.Run are already unwinding; this blocks only long enough for
	// both to actually finish — closing every open SSE stream cleanly and
	// exiting every collector's poll loop — before the store and broker
	// they both still read from are torn down below. Waiting here, rather
	// than only via the deferred process exit, is what makes "no leaked
	// goroutines" something a test can assert instead of merely hope for.
	backgroundWG.Wait()

	// macroExecutor.Stop waits for any macro run goroutine's own bookkeeping
	// (a step's final store write, an audit append) to land before the
	// store it writes to is closed below; see that method's own doc
	// comment for what it deliberately does NOT do (cancel an in-flight
	// run; a running macro cannot be stopped by this or by anything else,
	// by construction). runShutdown gives Stop its own earlier sub-deadline
	// so a run still in flight cannot also starve the disconnects below of
	// their share of shutdownCtx.
	// ok is asserted rather than discarded: a deadline-free shutdownCtx
	// would make the zero time.Time below read as "already expired" and
	// silently turn an unbounded shutdown into zero budget for everything.
	shutdownDeadline, ok := shutdownCtx.Deadline()
	if !ok {
		shutdownDeadline = time.Now().Add(10 * time.Second)
	}
	stopErr := runShutdown(shutdownCtx, shutdownDeadline, macroExecutor.Stop, func(disconnectCtx context.Context) {
		// Step 9: every integration broker connection this coordinator
		// opened (buildIntegrationBrokerRegistry, above) is torn down
		// alongside the control-plane broker immediately below.
		for _, ibm := range integrationBrokerManagers {
			if err := ibm.Disconnect(disconnectCtx); err != nil {
				logger.Warn("integration broker disconnect error", "error", err)
			}
		}

		if err := bm.Disconnect(disconnectCtx); err != nil {
			logger.Warn("mqtt disconnect error", "error", err)
		}
	})
	// The message makes no causal claim: this branch is also reachable
	// with zero runs in flight, when earlier shutdown steps consumed the
	// whole budget and Stop's clamped context was born expired.
	if stopErr != nil {
		logger.Warn("macro executor stop did not finish within its shutdown budget", "error", stopErr)
	}

	if err := st.Close(); err != nil {
		logger.Warn("store close error", "error", err)
	}

	logger.Info("showmesh-coordinator exited cleanly")
	return 0
}

// macroExecutorStopReserve is how much of the overall shutdown deadline
// [runShutdown] reserves for what runs after macroExecutor.Stop, so a run
// still in flight when shutdown begins (Stop cannot cancel one; see its own
// doc comment) cannot also consume the budget the broker disconnects need.
// 3s is A SHOWMESH HYPOTHESIS, not a measured value: a clean MQTT
// DISCONNECT is far under it, and it leaves Stop the larger share.
const macroExecutorStopReserve = 3 * time.Second

// macroStopContext derives Stop's own sub-deadline from the shared shutdown
// deadline, reserving reserve for whatever runs after Stop returns. If less
// than reserve is already left, it returns a context deadlined at now
// instead of one already in the past.
func macroStopContext(parent context.Context, deadline time.Time, reserve time.Duration) (context.Context, context.CancelFunc) {
	stopBy := deadline.Add(-reserve)
	if now := time.Now(); stopBy.Before(now) {
		stopBy = now
	}
	return context.WithDeadline(parent, stopBy)
}

// runShutdown runs stop against its own earlier sub-deadline (see
// [macroStopContext]) and then afterStop against the full shared deadline,
// so stop's consumption can never eat afterStop's share. afterStop gets
// whatever remains of the shared deadline: real time whenever stop was
// what expired, and an already-expired context only if the steps BEFORE
// runShutdown consumed the entire budget themselves. Returns stop's error;
// afterStop reports its own errors itself since it may run more than one
// step.
func runShutdown(ctx context.Context, deadline time.Time, stop func(context.Context) error, afterStop func(context.Context)) error {
	stopCtx, cancelStop := macroStopContext(ctx, deadline, macroExecutorStopReserve)
	defer cancelStop()
	stopErr := stop(stopCtx)

	afterCtx, cancelAfter := context.WithDeadline(ctx, deadline)
	defer cancelAfter()
	afterStop(afterCtx)

	return stopErr
}

// bootstrapWarningInterval is how often [watchUnclaimedBootstrap] re-logs
// its warning while no principal has claimed the bootstrap code. A
// SHOWMESH HYPOTHESIS, not a measured value: frequent enough that an
// operator who only skims recent scrollback still catches it within one
// visit to this coordinator's logs, infrequent enough not to spam a log
// that is also carrying everything else this coordinator does.
const bootstrapWarningInterval = 5 * time.Minute

// watchUnclaimedBootstrap logs a loud warning immediately and then every
// bootstrapWarningInterval for as long as identitySvc reports zero
// principals, until ctx is cancelled. See its call site's doc comment for
// why this exists as a repeating log rather than a single startup line.
func watchUnclaimedBootstrap(ctx context.Context, identitySvc identity.Service, logger *slog.Logger) {
	logBootstrapStateIfUnclaimed(ctx, identitySvc, logger)

	ticker := time.NewTicker(bootstrapWarningInterval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			logBootstrapStateIfUnclaimed(ctx, identitySvc, logger)
		}
	}
}

// logBootstrapStateIfUnclaimed logs ADR-024 decision 9's unclaimed-
// bootstrap warning if and only if identitySvc currently reports zero
// principals, and — before checking — ensures a valid bootstrap code and
// file exist via identity.Service.EnsureBootstrap. This is deliberately
// the ONLY place in this codebase that calls EnsureBootstrap on a
// recurring basis: watchUnclaimedBootstrap's caller runs it once
// immediately at startup and then every bootstrapWarningInterval,
// entirely on this coordinator's own internal timer, never in response to
// any request a network caller can trigger or accelerate. A review
// finding caught that GET /api/v1/session used to trigger the identical
// generation side effect through HasAnyPrincipal, which meant an
// unauthenticated caller polling that endpoint silently reissued an
// expired bootstrap code on the very next request, making the code's own
// expiry (ADR-024 decision 9: "carries an expiry") bound nothing in
// practice — see identity.Service.HasAnyPrincipal's and
// identity.Service.EnsureBootstrap's own doc comments for the full split.
// Never the code itself, and never anything read from the bootstrap
// file — OBSERVABILITY section 13's "never log a secret" rule, which this
// function's silence about the code's actual value is what satisfies.
func logBootstrapStateIfUnclaimed(ctx context.Context, identitySvc identity.Service, logger *slog.Logger) {
	if err := identitySvc.EnsureBootstrap(ctx); err != nil {
		logger.Warn("failed to ensure a bootstrap code is available", "error", err)
	}

	has, err := identitySvc.HasAnyPrincipal(ctx)
	if err != nil {
		logger.Warn("failed to check whether this coordinator has any administrator principal yet", "error", err)
		return
	}
	if has {
		return
	}
	logger.Warn("this coordinator has NO administrator principal yet: the write surface is unusable and the dashboard is showing " +
		"you an UNCLAIMED coordinator with reads still open (ADR-024 decision 9). Read the one-time bootstrap code from " +
		"the data volume (identity.BootstrapFileName under SHOWMESH_DATA_DIR) and claim it with POST /api/v1/bootstrap, " +
		"or run `showmesh-coordinator bootstrap` directly against this coordinator's data volume, before relying on this " +
		"coordinator for a real show.")
}

// seedNodeRenderKnownSurfaces reads back every persisted surface.pipeline.
// state row and groups it by the node that reported it (via
// noderender.NodeFromSource), for noderender.WithKnownSurfaces. This is
// what lets noderender.Collector.Poll emit a dropped-surface absence on the
// very first poll after a restart, rather than only once one more real
// delivery has arrived to compare against — a row from a source this
// package's SourceFor never produced (e.g. none yet, a fresh store) is
// silently skipped, not an error.
func seedNodeRenderKnownSurfaces(ctx context.Context, st *store.Store) (map[string]map[string]struct{}, error) {
	rows, err := st.ListObservations(ctx, store.ObservationFilter{
		ResourceKind: observation.ResourceSurface,
		Signal:       noderender.SignalSurfacePipelineState,
	})
	if err != nil {
		return nil, fmt.Errorf("list persisted surface.pipeline.state observations: %w", err)
	}
	known := make(map[string]map[string]struct{})
	for _, o := range rows {
		nodeID, ok := noderender.NodeFromSource(o.Source)
		if !ok {
			continue
		}
		if known[nodeID] == nil {
			known[nodeID] = make(map[string]struct{})
		}
		known[nodeID][o.Resource.ID] = struct{}{}
	}
	return known, nil
}

func newLogger(level string) *slog.Logger {
	var lvl slog.Level
	switch level {
	case "debug":
		lvl = slog.LevelDebug
	case "warn":
		lvl = slog.LevelWarn
	case "error":
		lvl = slog.LevelError
	default:
		lvl = slog.LevelInfo
	}

	handler := slog.NewJSONHandler(os.Stdout, &slog.HandlerOptions{Level: lvl})
	return slog.New(handler)
}
