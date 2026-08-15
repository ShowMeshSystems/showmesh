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
	"github.com/showmeshsystems/showmesh/internal/coordinator/broker"
	"github.com/showmeshsystems/showmesh/internal/coordinator/collector"
	"github.com/showmeshsystems/showmesh/internal/coordinator/collector/fpp"
	"github.com/showmeshsystems/showmesh/internal/coordinator/collector/fppmqtt"
	"github.com/showmeshsystems/showmesh/internal/coordinator/config"
	"github.com/showmeshsystems/showmesh/internal/coordinator/httpapi"
	"github.com/showmeshsystems/showmesh/internal/coordinator/identity"
	"github.com/showmeshsystems/showmesh/internal/coordinator/inventory"
	"github.com/showmeshsystems/showmesh/internal/coordinator/readiness"
	"github.com/showmeshsystems/showmesh/internal/coordinator/store"
	"github.com/showmeshsystems/showmesh/internal/version"
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

	// Track D seam D-1's identical re-check, for the identical reason:
	// config.Config.Validate's own validateResolumeConfig only ever saw
	// cfg.FPPEndpoints as parsed from the ENVIRONMENT, which may differ in
	// existence (env unset, store populated) from the store-authoritative
	// list resolveAuthoritativeFPPEndpoints just resolved above. A
	// deployment that migrated SHOWMESH_FPP_ENDPOINTS into the store and
	// removed the variable, then later set SHOWMESH_RESOLUME_URL to a
	// value whose id now happens to match a store-only FPP endpoint id,
	// would sail past config.Validate with nothing to compare against —
	// this is the only place left that can still catch it before the two
	// collectors are actually registered on one shared Runner below.
	//
	// Gated on cfg.ResolumeURL != "", mirroring validateResolumeConfig's
	// own gate exactly: cfg.ResolumeID defaults to "resolume" even with
	// the collector disabled (see [config.Config.ResolumeID]'s doc
	// comment), so an ungated check here would fatally refuse to start a
	// coordinator that happens to have an FPP endpoint literally named
	// "resolume" and no Resolume instance configured at all — a
	// collision that can never actually happen, because the Resolume
	// collector this id would apply to is never constructed.
	// resolumeConfiguredID is threaded into apiDeps.ResolumeID below,
	// gated on the SAME cfg.ResolumeURL != "" condition as the fatal
	// re-check immediately above, so PUT /api/v1/config/fpp.endpoints
	// (api/config.go's handlePutFPPEndpointsConfig, Track D seam D-1
	// review finding 1) refuses a write under EXACTLY the condition this
	// boot-time check would refuse to start under — never a superset
	// (which would refuse writes this coordinator could never actually
	// fail to boot from) and never a subset (which would accept a write
	// this coordinator then cannot survive its own next restart).
	var resolumeConfiguredID string
	if cfg.ResolumeURL != "" {
		if err := config.ValidateResolumeIDAgainstFPPEndpoints(cfg.ResolumeID, cfg.FPPEndpoints); err != nil {
			logger.Error("SHOWMESH_RESOLUME_ID no longer cross-checks against the authoritative fpp.endpoints configuration", "error", err)
			_ = st.Close()
			return 1
		}
		resolumeConfiguredID = cfg.ResolumeID
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

	inv := inventory.New(st, logger, inventory.WithOnChange(notifyHub))

	bm, err := broker.NewBrokerManager(ctx, cfg, logger, inv.Subscriptions(), inv.HandleMessage)
	if err != nil {
		logger.Error("failed to start mqtt connection manager", "error", err)
		_ = st.Close()
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

	// Track D seam D-1: the Resolume Arena REST/WebSocket collector
	// (internal/coordinator/collector/resolume), constructed here —
	// BEFORE apiDeps, for the identical reason fppRunner itself just was —
	// so apiDeps.Collectors (below) can read resolumeWire.status rather
	// than a value that does not exist yet. See newResolumeWiring's own
	// doc comment in resolumewiring.go for why it joins this SAME
	// fppRunner rather than a second collector.Runner, and for why every
	// error path here is fatal: cfg.Validate already checked
	// SHOWMESH_RESOLUME_URL's shape and SHOWMESH_RESOLUME_ID's syntax and
	// uniqueness (config.validateResolumeConfig), so a construction
	// failure here would mean that check and this package's own have
	// drifted apart — the identical judgment the fpp.New loop further down
	// already applies to its own endpoints, never a per-instance condition
	// to skip past silently. resolumeWire.watcher is nil, and
	// resolumeWire.status reports CollectorNotConfigured, when
	// SHOWMESH_RESOLUME_URL is unset — no goroutine, no warning storm, no
	// failed-connection signals for a feature the operator did not enable.
	resolumeWire, err := newResolumeWiring(cfg, fppRunner, logger)
	if err != nil {
		logger.Error("failed to construct resolume collector/watcher", "error", err)
		_ = bm.Disconnect(ctx)
		_ = st.Close()
		return 1
	}

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
		FPP:          fppInstanceLister{st: st, endpoints: cfg.FPPEndpoints},
		Observations: storeObservationLister{st: st},
		Events:       storeEventReader{st: st},
		// Step 5 (contract section 5.4): both FPP collector sources must be
		// visible in /api/v1/snapshot's collectors[] — a second source that
		// is invisible there is a source an operator cannot tell is broken.
		// multiCollectorStatusLister (apiwiring.go) concatenates the two;
		// the MQTT half's "configured" bit is exactly the same condition
		// that decides whether *fppmqtt.Collector is constructed and
		// registered below, so this line and that one can never disagree
		// about whether FPP MQTT collection is enabled.
		Collectors: multiCollectorStatusLister{
			fppCollectorStatusLister{endpoints: cfg.FPPEndpoints},
			fppMQTTCollectorStatusLister{configured: cfg.FPPMQTTBrokerURL != ""},
			// Track D seam D-1: resolumeWire.status was already built above
			// (newResolumeWiring), from the same cfg.ResolumeURL != ""
			// condition that decided whether the collector itself was
			// constructed and registered on fppRunner — this line and that
			// one can never disagree about whether Resolume collection is
			// enabled, the identical guarantee the FPP MQTT comment above
			// states for its own pair.
			resolumeWire.status,
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
		// ResolumeID plumbs cfg.ResolumeID through to api.Dependencies ONLY
		// when the Resolume collector is enabled (cfg.ResolumeURL != ""),
		// matching the boot-time re-check just above
		// (config.ValidateResolumeIDAgainstFPPEndpoints) by the identical
		// gate: cfg.ResolumeID defaults to "resolume" even with the
		// collector disabled, so passing it unconditionally here would make
		// handlePutFPPEndpointsConfig (api/config.go) refuse an endpoint id
		// that can never actually collide with anything, since no Resolume
		// collector is ever constructed for it to collide with. Track D
		// seam D-1 review finding 1.
		ResolumeID: resolumeConfiguredID,
		// Deliberately no ResolumeCompositionID field here (Track D seam
		// D-2a review finding F): the stored composition's config_objects
		// id is a fixed constant inside the api package
		// (resolumeCompositionObjectIDConst in resolumecomposition.go),
		// not derived from cfg.ResolumeID. An earlier version of this line
		// plumbed cfg.ResolumeID through unconditionally, which meant
		// renaming SHOWMESH_RESOLUME_ID for a reason unrelated to the
		// composition subsystem — e.g. disambiguating a second live
		// Resolume instance — would silently orphan every stored
		// composition revision. See that constant's own doc comment.
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
	}
	apiInst := api.New(apiDeps, api.Options{
		CloseReads:          cfg.CloseReads,
		SecureCookie:        cfg.SecureCookie,
		TrustClientAddr:     cfg.TrustClientAddr,
		LoginConcurrency:    cfg.LoginConcurrency,
		LoginQueueWait:      cfg.LoginQueueWait,
		LoginPerSourceDelay: cfg.LoginPerSourceDelay,
		LoginMaxDelay:       cfg.LoginMaxDelay,
		AllowedOrigins:      cfg.APIAllowedOrigins,
		Logger:              logger,
	})
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

	// Seam B's FPP MQTT collector (internal/coordinator/collector/fppmqtt),
	// contract section 5.4: constructed and registered only when its broker
	// URL is configured — unset SHOWMESH_FPP_MQTT_BROKER_URL means this
	// collector does not exist at all for this process, no warning storm,
	// no failed-connection signals for a feature the operator did not
	// enable (contract section 4.4). cfg.Validate has already checked
	// FPPMQTTHosts' ids against cfg.FPPEndpoints and FPPMQTTBrokerURL's
	// shape (internal/coordinator/config's validateFPPMQTTConfig); fppmqtt.New
	// re-checks the same shape on its own for the identical "safe to
	// construct directly, without relying on config validation having
	// already run" reason fpp.New already documents for the REST collector
	// above, so a failure here would mean those two checks have drifted
	// apart — a startup-worthy inconsistency in this binary, not a
	// condition to skip past silently.
	var mqttFPPCollector *fppmqtt.Collector
	if cfg.FPPMQTTBrokerURL != "" {
		mqttFPPCollector, err = fppmqtt.New(fppmqtt.Options{
			BrokerURL:   cfg.FPPMQTTBrokerURL,
			Username:    cfg.FPPMQTTUsername,
			Password:    cfg.FPPMQTTPassword,
			TopicPrefix: cfg.FPPMQTTTopicPrefix,
			Hosts:       cfg.FPPMQTTHosts,
			Logger:      logger,
		})
		if err != nil {
			logger.Error("failed to construct fpp mqtt collector", "error", err)
			_ = bm.Disconnect(ctx)
			_ = st.Close()
			return 1
		}
		fppRunner.Add(mqttFPPCollector, mqttFPPCollector.PollInterval())
	}

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
	backgroundWG.Add(3)
	go func() {
		defer backgroundWG.Done()
		hub.Run(ctx)
	}()
	go func() {
		defer backgroundWG.Done()
		fppRunner.Run(ctx)
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

	// mqttFPPCollector.Run owns the FPP MQTT broker connection's own
	// lifecycle (connect, resubscribe across reconnects, graceful
	// disconnect on ctx cancellation — see that method's doc comment),
	// entirely separate from fppRunner's poll-loop goroutine above: Poll
	// only ever renders whatever mqttFPPCollector's message store already
	// holds (contract section 4.1 — Poll never touches the network), so the
	// connection itself needs its own goroutine the same way hub.Run and
	// fppRunner.Run already get theirs, joined via the identical
	// backgroundWG so shutdown still waits for every one of them cleanly.
	// Started (and only started) exactly when mqttFPPCollector was
	// constructed above, matching that same cfg.FPPMQTTBrokerURL != ""
	// condition — an unconfigured FPP MQTT collector contributes no
	// goroutine at all, per contract section 4.4.
	if mqttFPPCollector != nil {
		backgroundWG.Add(1)
		go func() {
			defer backgroundWG.Done()
			if err := mqttFPPCollector.Run(ctx); err != nil {
				logger.Error("fpp mqtt collector connection ended", "error", err)
			}
		}()
	}

	// resolumeWire.watcher.Run owns the Resolume WebSocket connection's own
	// lifecycle (connect, reconnect with backoff on any loss, graceful
	// shutdown on ctx cancellation — see resolume.Watcher.Run's own doc
	// comment), entirely separate from fppRunner's poll-loop goroutine
	// above the same way mqttFPPCollector.Run is: joined via the identical
	// backgroundWG so shutdown waits for every one of them cleanly, and
	// started (and only started) exactly when resolumeWire.watcher was
	// constructed above — an unconfigured Resolume collector contributes
	// no goroutine at all. Run returns no error (unlike
	// mqttFPPCollector.Run): a lost or refused connection is never fatal
	// here or anywhere in this seam — see newResolumeWiring's own doc
	// comment for the fatal/non-fatal split this wiring draws.
	//
	// There used to be a second goroutine here, resolumeWire.adapter.Run,
	// which owned the only `GET /composition` read this seam performed.
	// ADR-032 decision 2 forbids that call outright — measured live, it
	// crashes the target Arena build — so the adapter, and the goroutine
	// that ran it, are gone; resolumeWire.watcher.Run is the only
	// Resolume-specific background goroutine this seam starts now.
	if resolumeWire.watcher != nil {
		backgroundWG.Add(1)
		go func() {
			defer backgroundWG.Done()
			resolumeWire.watcher.Run(ctx)
		}()
	}

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

	if err := bm.Disconnect(shutdownCtx); err != nil {
		logger.Warn("mqtt disconnect error", "error", err)
	}

	if err := st.Close(); err != nil {
		logger.Warn("store close error", "error", err)
	}

	logger.Info("showmesh-coordinator exited cleanly")
	return 0
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
