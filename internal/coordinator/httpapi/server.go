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
// and version endpoints; the Step 3 versioned control API is mounted
// alongside them via [Server.Mount] rather than built in here — see that
// method's doc comment for why.
type Server struct {
	httpServer *http.Server
	mux        *http.ServeMux
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
	// "/" is net/http.ServeMux's least-specific subtree pattern: it never
	// shadows "GET /healthz"/"GET /readyz"/"GET /version" above (each is a
	// more specific, method-scoped pattern) or whatever [Server.Mount]
	// later registers under "/api/" (also more specific). It only ever
	// matches a path this server has no other route for at all — see
	// handleNotFound's own doc comment for why that case needs a handler
	// instead of net/http's bare-text default.
	mux.HandleFunc("/", s.handleNotFound)
	s.mux = mux

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

// Mount attaches handler under pattern (e.g. "/api/") on this server's
// existing mux, alongside /healthz, /readyz, and /version.
//
// This exists so internal/coordinator/coordinator.go (Step 3's wiring) can
// serve internal/coordinator/api's versioned control API from the same
// listener without this package ever importing that one, or anything else
// coordinator-specific: per this package's own doc comment, httpapi "knows
// nothing about MQTT or any other specific dependency", and that property
// is exactly as true of the Step 3 API as it was of the broker before it —
// httpapi never learns what an observation, a node, or an FPP instance is.
// The caller owns everything about handler's behavior; this method is
// nothing more than http.ServeMux.Handle, done here because mux is a
// private field.
//
// Mount must be called before [Server.ListenAndServe]; net/http.ServeMux
// is not safe to register new patterns on concurrently with serving
// requests through it, and this package makes no attempt to synchronize
// the two — Server's own lifecycle (build with NewServer, Mount whatever
// is needed, then ListenAndServe) already keeps them sequential for every
// caller in this codebase.
func (s *Server) Mount(pattern string, handler http.Handler) {
	s.mux.Handle(pattern, handler)
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

// notFoundAPIVersionHeaderValue mirrors internal/coordinator/api's
// apiVersionHeaderName/supportedAPIVersions[0] (contract section 6.2:
// "ShowMesh-API-Version: 1 on every /api/v1 response, including errors").
// This package deliberately does not import that one — see this file's own
// doc comment ("knows nothing about ... any other specific dependency") —
// so the value is a literal here, the same trade-off
// internal/coordinator/api/problem.go's ProblemTypeInternalError constant
// already makes for the mirror in the other direction. Kept manually in
// sync; there is exactly one other place this string appears at all
// (middleware.go's apiVersionHeaderName), so a future version bump has one
// sibling to update, not several scattered ones.
const notFoundAPIVersionHeaderValue = "1"

// handleNotFound answers any request this server has no route for at all —
// not /healthz, /readyz, /version, or anything under /api/ — with the same
// RFC 9457 problem+json shape and ShowMesh-API-Version header every /api/v1
// response carries, instead of net/http's bare "404 page not found" plain
// text.
//
// Step 3 review finding 4.9: before this handler existed, GET /nope (or any
// other path outside /api/) fell through to net/http's own default 404,
// which is plain text, carries no version header, and is not
// application/problem+json — a client that typos its base URL (a real
// integration mistake, not a hypothetical one) got a response shape it had
// no contract reason to expect, rather than the same structured error every
// other unmatched path in this API already returns (a request under
// /api/v1 that matches no route gets exactly this shape from
// internal/coordinator/api's own resourceNotFoundProblem — this handler
// only closes the gap for everything net/http routes to before ever
// reaching that package's mux).
func (s *Server) handleNotFound(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("ShowMesh-API-Version", notFoundAPIVersionHeaderValue)
	w.Header().Set("Content-Type", "application/problem+json")
	w.WriteHeader(http.StatusNotFound)
	body := map[string]any{
		"type":       "https://showmesh.dev/problems/resource-not-found",
		"title":      "Resource not found",
		"status":     http.StatusNotFound,
		"detail":     "no route matches " + r.Method + " " + r.URL.Path,
		"serverTime": time.Now().Format(time.RFC3339Nano),
	}
	if err := json.NewEncoder(w).Encode(body); err != nil {
		s.logger.Warn("failed to encode not-found response", "error", err, "path", r.URL.Path)
	}
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
