// Command showmesh-coordinator is the ShowMesh coordinator: the management
// plane that observes and commands node agents over MQTT. Per ADR-001 it is
// never a scheduler and per ADR-008 its loss (and the broker's) must never
// affect a running show.
package main

import (
	"context"
	"flag"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/showmeshsystems/showmesh/internal/coordinator"
	"github.com/showmeshsystems/showmesh/internal/version"
)

func main() {
	versionFlag := flag.Bool("version", false, "print version and exit")
	healthcheckFlag := flag.Bool("healthcheck", false, "check the local /healthz endpoint and exit (used by container HEALTHCHECK)")
	flag.Parse()

	if *versionFlag {
		fmt.Println(version.String())
		os.Exit(0)
	}

	if *healthcheckFlag {
		os.Exit(runHealthcheck())
	}

	os.Exit(runCoordinator())
}

// runHealthcheck performs a local HTTP GET against /healthz and returns a
// process exit code (0 on success). It exists so a shell-less distroless
// container can define a Docker HEALTHCHECK; it prints nothing on success.
func runHealthcheck() int {
	addr := os.Getenv(coordinator.EnvHTTPAddr)
	if addr == "" {
		addr = coordinator.DefaultHTTPAddr
	}

	_, port, err := net.SplitHostPort(addr)
	if err != nil {
		fmt.Fprintf(os.Stderr, "healthcheck: invalid %s %q: %v\n", coordinator.EnvHTTPAddr, addr, err)
		return 1
	}

	// Always target the local process, regardless of the configured bind
	// host (which may be "0.0.0.0" or empty, neither of which is dialable).
	url := fmt.Sprintf("http://127.0.0.1:%s/healthz", port)

	client := &http.Client{Timeout: 3 * time.Second}
	resp, err := client.Get(url)
	if err != nil {
		fmt.Fprintf(os.Stderr, "healthcheck: %v\n", err)
		return 1
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode != http.StatusOK {
		fmt.Fprintf(os.Stderr, "healthcheck: unexpected status %s\n", resp.Status)
		return 1
	}

	return 0
}

// runCoordinator loads config, starts the MQTT connection manager and HTTP
// server, and blocks until a shutdown signal is received.
func runCoordinator() int {
	cfg, err := coordinator.LoadConfig()
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

	if err := os.MkdirAll(cfg.DataDir, 0o755); err != nil {
		// Step 0 has no persistence yet; a bad DATA_DIR is not fatal.
		logger.Warn("could not create data dir; continuing without persistence", "data_dir", cfg.DataDir, "error", err)
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	broker, err := coordinator.NewBrokerManager(ctx, cfg, logger)
	if err != nil {
		logger.Error("failed to start mqtt connection manager", "error", err)
		return 1
	}

	srv := coordinator.NewServer(cfg.HTTPAddr, broker.State, logger)

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

	if err := broker.Disconnect(shutdownCtx); err != nil {
		logger.Warn("mqtt disconnect error", "error", err)
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
