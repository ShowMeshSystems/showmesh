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
	"syscall"
	"time"

	"github.com/showmeshsystems/showmesh/internal/coordinator/broker"
	"github.com/showmeshsystems/showmesh/internal/coordinator/config"
	"github.com/showmeshsystems/showmesh/internal/coordinator/httpapi"
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
	)

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

	inv := inventory.New(st, logger)

	bm, err := broker.NewBrokerManager(ctx, cfg, logger, inv.Subscriptions(), inv.HandleMessage)
	if err != nil {
		logger.Error("failed to start mqtt connection manager", "error", err)
		_ = st.Close()
		return 1
	}

	// The store and the broker each contribute independently to /readyz;
	// readiness.Aggregate is not-ready as soon as either is, per ADR-011 —
	// see its doc comment for why this stays a single readiness.Source
	// rather than a change to httpapi.NewServer's signature.
	srv := httpapi.NewServer(cfg.HTTPAddr, readiness.Aggregate{bm, st}, logger)

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

	if err := bm.Disconnect(shutdownCtx); err != nil {
		logger.Warn("mqtt disconnect error", "error", err)
	}

	if err := st.Close(); err != nil {
		logger.Warn("store close error", "error", err)
	}

	logger.Info("showmesh-coordinator exited cleanly")
	return 0
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
