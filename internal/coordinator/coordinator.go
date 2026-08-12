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
		},
		// Identity is ADR-024's own dependency: wiring it in is what makes
		// POST/GET/DELETE /api/v1/session, POST /api/v1/bootstrap, and
		// GET /api/v1/audit do anything other than always answer 401/403
		// against api.noIdentityService's no-op default.
		Identity: identitySvc,
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

	// One shared *http.Client per contract/Task C's own guidance ("callers
	// SHOULD construct one *http.Client and pass it to every fpp.New call")
	// rather than one per instance.
	fppHTTPClient := &http.Client{
		Transport: &http.Transport{
			MaxIdleConnsPerHost: 4,
			IdleConnTimeout:     90 * time.Second,
		},
	}
	fppRunner := collector.NewRunner(&fppSink{st: st, notify: notifyHub, logger: logger}, logger)
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
