// Package httpapi is the coordinator's HTTP server: liveness, readiness,
// and version endpoints today; topic/state APIs land in later steps. It
// knows nothing about MQTT or any other specific dependency — readiness is
// contributed by whatever readiness.Source NewServer is given, per the
// neutral readiness package's contract.
package httpapi

import (
	"context"
	"encoding/json"
	"log/slog"
	"net/http"
	"runtime"
	"time"

	"github.com/showmeshsystems/showmesh/internal/coordinator/readiness"
	"github.com/showmeshsystems/showmesh/internal/version"
)

// alwaysNotReadySource is the readiness.Source NewServer falls back to when
// given a nil source, standing in for a generic missing dependency rather
// than leaving /readyz with nothing to consult. It knows nothing about MQTT
// or any other specific dependency, matching this package's doc comment.
//
// This path is unreachable from coordinator.Run(), which always supplies a
// real broker.BrokerManager; it exists only so NewServer never leaves
// s.readiness nil. It deliberately leaves ObservedAt zero: there is no
// dependency and so no evidence to report an age for (see
// readiness.Report.ObservedAt).
type alwaysNotReadySource struct{}

func (alwaysNotReadySource) Readiness() readiness.Report {
	return readiness.Report{
		Ready:  false,
		Reason: "no readiness source configured",
	}
}

// Server is the coordinator's HTTP server. It exposes liveness, readiness,
// and version endpoints; topic/state APIs land in later steps.
type Server struct {
	httpServer *http.Server
	logger     *slog.Logger

	// readiness reports the coordinator's readiness. It backs /readyz.
	// Tests substitute this without a real dependency.
	readiness readiness.Source
}

// NewServer builds (but does not start) an HTTP server listening on addr.
// source is consulted on every /readyz request and must be safe for
// concurrent use; a nil source is treated as an always-not-ready dependency
// with no evidence to report.
//
// source must not be a typed-nil pointer (e.g. a nil *broker.BrokerManager
// stored in the readiness.Source interface). The `source == nil` check
// below only catches an untyped nil interface value; a typed-nil pointer
// satisfies the interface and would panic on the first call to Readiness().
func NewServer(addr string, source readiness.Source, logger *slog.Logger) *Server {
	if logger == nil {
		logger = slog.Default()
	}
	if source == nil {
		source = alwaysNotReadySource{}
	}

	s := &Server{logger: logger, readiness: source}

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
// HTTP requests at all. It must never depend on readiness.
func (s *Server) handleHealthz(w http.ResponseWriter, _ *http.Request) {
	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write([]byte("ok"))
}

// handleReadyz reports readiness from s.readiness, per ADR-011: an
// observation is only as good as its freshness.
//
//   - Ready: 200.
//   - Not ready: 503, with the source's Reason and Details merged into the
//     response body. For the broker source (the only one that exists
//     today), that means reason "mqtt broker not connected" when
//     disconnected, or "mqtt broker evidence is stale" when connected but
//     the evidence has aged past the source's staleness window — per
//     ADR-011 that is unknown, not healthy, so it is reported as not-ready
//     rather than papering over the missing confirmation.
func (s *Server) handleReadyz(w http.ResponseWriter, _ *http.Request) {
	report := s.readiness.Readiness()

	if report.Ready {
		w.Header().Set("Content-Type", "text/plain; charset=utf-8")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("ok"))
		return
	}

	s.writeNotReady(w, report)
}

// writeNotReady renders a not-ready readiness.Report as the /readyz 503
// body. observedAgeSecs is derived here from the typed report.ObservedAt
// field, not read out of Details: per ADR-011 and readiness.Report.ObservedAt,
// freshness must be structurally present on every Report, not a value each
// Source has to remember to build into Details by hand.
func (s *Server) writeNotReady(w http.ResponseWriter, report readiness.Report) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusServiceUnavailable)

	body := map[string]any{}
	if !report.ObservedAt.IsZero() {
		body["observedAgeSecs"] = time.Since(report.ObservedAt).Seconds()
	}
	for k, v := range report.Details {
		body[k] = v
	}
	// status and reason are set after merging Details so a source cannot
	// mask its own not-ready verdict by returning a Details map that
	// happens to use those keys; the HTTP code is always 503 here
	// regardless of what Details contains.
	body["status"] = "not ready"
	body["reason"] = report.Reason

	_ = json.NewEncoder(w).Encode(body)
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
