package coordinator

import (
	"context"
	"encoding/json"
	"log/slog"
	"net/http"
	"runtime"
	"time"

	"github.com/showmeshsystems/showmesh/internal/version"
)

// readyzStalenessWindow bounds how old BrokerState.ObservedAt may be before
// /readyz treats a "connected" observation as unknown rather than healthy,
// per ADR-011 (stale or insufficient evidence becomes unknown, not
// healthy). Partition detection latency is ultimately bounded by the MQTT
// keepalive interval (see BrokerManager's KeepAlive setting in broker.go),
// so this window is a floor on confidence, not a guarantee: a connection
// can go silently dead and still read as fresh for up to one keepalive
// cycle after the partition starts.
const readyzStalenessWindow = 15 * time.Second

// Server is the coordinator's HTTP server. It exposes liveness, readiness,
// and version endpoints; topic/state APIs land in later steps.
type Server struct {
	httpServer *http.Server
	logger     *slog.Logger

	// brokerState reports the most recently observed MQTT broker connection
	// state. It backs /readyz. Tests substitute this without a real broker.
	brokerState func() BrokerState
}

// NewServer builds (but does not start) an HTTP server listening on addr.
// brokerState is consulted on every /readyz request and must be safe for
// concurrent use; a nil func is treated as an always-disconnected,
// always-stale state.
func NewServer(addr string, brokerState func() BrokerState, logger *slog.Logger) *Server {
	if logger == nil {
		logger = slog.Default()
	}
	if brokerState == nil {
		brokerState = func() BrokerState { return BrokerState{} }
	}

	s := &Server{logger: logger, brokerState: brokerState}

	mux := http.NewServeMux()
	mux.HandleFunc("GET /healthz", s.handleHealthz)
	mux.HandleFunc("GET /readyz", s.handleReadyz)
	mux.HandleFunc("GET /version", s.handleVersion)

	s.httpServer = &http.Server{
		Addr:              addr,
		Handler:           mux,
		ReadHeaderTimeout: 5 * time.Second,
		ReadTimeout:       10 * time.Second,
		WriteTimeout:      10 * time.Second,
		IdleTimeout:       60 * time.Second,
	}

	return s
}

// ListenAndServe starts serving HTTP requests. It blocks until the server
// stops, returning http.ErrServerClosed after a clean Shutdown.
func (s *Server) ListenAndServe() error {
	return s.httpServer.ListenAndServe()
}

// Shutdown gracefully shuts down the HTTP server, honoring ctx's deadline.
func (s *Server) Shutdown(ctx context.Context) error {
	return s.httpServer.Shutdown(ctx)
}

// handleHealthz reports liveness only: 200 whenever the process is serving
// HTTP requests at all. It must never depend on broker state.
func (s *Server) handleHealthz(w http.ResponseWriter, _ *http.Request) {
	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write([]byte("ok"))
}

// handleReadyz reports readiness from the broker's BrokerState, per
// ADR-011: an observation is only as good as its freshness.
//
//   - Connected and evidence fresh (ObservedAt within readyzStalenessWindow):
//     200.
//   - Not connected: 503, reason "mqtt broker not connected".
//   - Connected but evidence stale: 503. Per ADR-011 this is unknown, not
//     healthy, so it is reported as not-ready rather than papering over the
//     missing confirmation.
func (s *Server) handleReadyz(w http.ResponseWriter, _ *http.Request) {
	state := s.brokerState()

	if !state.Connected {
		s.writeNotReady(w, "mqtt broker not connected", state)
		return
	}

	age := time.Since(state.ObservedAt)
	if age > readyzStalenessWindow {
		s.writeNotReady(w, "mqtt broker evidence is stale", state)
		return
	}

	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write([]byte("ok"))
}

func (s *Server) writeNotReady(w http.ResponseWriter, reason string, state BrokerState) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusServiceUnavailable)
	_ = json.NewEncoder(w).Encode(map[string]any{
		"status":          "not ready",
		"reason":          reason,
		"connected":       state.Connected,
		"observedAgeSecs": time.Since(state.ObservedAt).Seconds(),
	})
}

type versionResponse struct {
	Version   string `json:"version"`
	Commit    string `json:"commit"`
	BuildDate string `json:"buildDate"`
	GoVersion string `json:"goVersion"`
}

func (s *Server) handleVersion(w http.ResponseWriter, _ *http.Request) {
	resp := versionResponse{
		Version:   version.Version,
		Commit:    version.Commit,
		BuildDate: version.BuildDate,
		GoVersion: runtime.Version(),
	}
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	_ = json.NewEncoder(w).Encode(resp)
}
