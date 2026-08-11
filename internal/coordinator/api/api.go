package api

import (
	"context"
	"log/slog"
	"net/http"
	"os"
	"strconv"
	"time"

	"github.com/showmeshsystems/showmesh/internal/coordinator/inventory"
	"github.com/showmeshsystems/showmesh/pkg/observation"
)

// Dependencies are the data sources this API renders. Every field is an
// interface declared in interfaces.go, not a concrete package this task
// does not own — see this package's doc comment. A nil field is replaced
// by a no-op implementation that returns an empty, successful result
// (never an error): a dependency nobody has wired in yet is not this API
// failing, it is this API accurately reporting that it currently has
// nothing to say about that resource. Production wiring (a later task)
// is expected to supply all five; tests in this package exercise the
// no-op defaults deliberately, to prove the router itself works before any
// real store exists.
type Dependencies struct {
	Nodes        NodeLister
	FPP          FPPLister
	Observations ObservationLister
	Events       EventReader
	Collectors   CollectorStatusLister
}

// withDefaults returns d with every nil field replaced by a no-op
// implementation.
func (d Dependencies) withDefaults() Dependencies {
	if d.Nodes == nil {
		d.Nodes = noNodeLister{}
	}
	if d.FPP == nil {
		d.FPP = noFPPLister{}
	}
	if d.Observations == nil {
		d.Observations = noObservationLister{}
	}
	if d.Events == nil {
		d.Events = noEventReader{}
	}
	if d.Collectors == nil {
		d.Collectors = noCollectorStatusLister{}
	}
	return d
}

type noNodeLister struct{}

func (noNodeLister) Snapshot(context.Context, time.Time) ([]inventory.NodeView, error) {
	return nil, nil
}

type noFPPLister struct{}

func (noFPPLister) ListInstances(context.Context) ([]FPPInstanceView, error) { return nil, nil }

type noObservationLister struct{}

func (noObservationLister) ListObservations(context.Context, ObservationFilter) ([]observation.Observation, error) {
	return nil, nil
}

type noEventReader struct{}

func (noEventReader) ListEvents(context.Context, uint64, int) ([]EventRecord, bool, error) {
	return nil, false, nil
}
func (noEventReader) LatestEventSeq(context.Context) (uint64, error) { return 0, nil }
func (noEventReader) OldestEventSeq(context.Context) (uint64, bool, error) {
	return 0, false, nil
}

type noCollectorStatusLister struct{}

func (noCollectorStatusLister) CollectorStatuses(context.Context) ([]CollectorState, error) {
	return nil, nil
}

// Options configures [New]. The zero value is usable: auth and CORS are
// disabled (contract section 6.8's documented default posture), the clock
// is time.Now, and every stream tuning value below falls back to this
// package's own labeled hypothesis.
type Options struct {
	// AuthToken is SHOWMESH_API_TOKEN's value. Empty disables auth
	// entirely, per contract section 6.8. New does not itself log the
	// startup warning contract section 6.8 requires when this is empty —
	// that belongs to whatever loads config and calls New (a later
	// wiring task), which has the logger and the deployment context this
	// package does not.
	AuthToken string

	// AllowedOrigins is SHOWMESH_API_ALLOWED_ORIGINS, comma-split by the
	// caller. Empty means no CORS headers at all.
	AllowedOrigins []string

	// Clock is substituted in tests; defaults to time.Now.
	Clock func() time.Time

	Logger *slog.Logger

	// StreamTickInterval is how often the SSE hub re-renders every
	// resource and diffs it against what it last published, independent of
	// [Hub.Notify] — the mechanism that catches a Observation transitioning
	// current -> stale with no new evidence (contract section 6.5). THIS IS
	// A SHOWMESH HYPOTHESIS, not a measured value: chosen as a tradeoff
	// between staleness-transition latency and render/diff cost with no
	// load testing behind it. Defaults to 5 seconds.
	StreamTickInterval time.Duration

	// StreamKeepaliveInterval is the SSE ": keepalive" comment cadence
	// (contract section 6.4). A SHOWMESH HYPOTHESIS: chosen to be well
	// inside typical intermediary idle-connection timeouts, not measured
	// against this project's actual deployment path. Defaults to 15
	// seconds.
	StreamKeepaliveInterval time.Duration

	// StreamSubscriberBuffer is how many pending frames a slow SSE
	// subscriber may accumulate before contract section 6.4's overflow rule
	// fires (stream.reset, then disconnect). A SHOWMESH HYPOTHESIS: large
	// enough to absorb a burst of node.changed events from one collector
	// tick without false-positive disconnects, not derived from a measured
	// worst case. Defaults to 64.
	StreamSubscriberBuffer int
}

const (
	defaultStreamTickInterval      = 5 * time.Second
	defaultStreamKeepaliveInterval = 15 * time.Second
)

// envStreamSubscriberBufferOverride is a TEST-SUPPORT-ONLY environment
// variable (Step 3 wiring task) that lets the integration test harness in
// /test/integration shrink defaultStreamSubscriberBuffer, so a test proving
// contract section 6.4's overflow-then-disconnect behavior can force it
// deterministically with a small burst of real changes, rather than
// needing an implausibly large flood or relying on OS-level TCP
// backpressure against a non-draining client to (eventually, maybe) fill
// the production default of 64 — the exact "genuinely flaky" flood-test
// shape this package's own builder already tried and rejected in favor of
// the white-box unit tests in stream_test.go. It is read exactly once, at
// package initialization, so it can only take effect via the coordinator
// process's environment at startup — e.g. the integration harness exec'ing
// the real showmesh-coordinator binary with it set — never by calling code
// afterward. It must never become a documented production tuning surface:
// unset in every real deployment, it has no effect and
// defaultStreamSubscriberBuffer is exactly 64, matching
// [Options.StreamSubscriberBuffer]'s documented default.
const envStreamSubscriberBufferOverride = "SHOWMESH_TEST_STREAM_SUBSCRIBER_BUFFER"

// defaultStreamSubscriberBuffer is a package-level var, not a const, ONLY
// so envStreamSubscriberBufferOverride can override it for integration
// tests; see that constant's doc comment for why this must not be read as
// an invitation to change it any other way.
var defaultStreamSubscriberBuffer = resolveDefaultStreamSubscriberBuffer()

// resolveDefaultStreamSubscriberBuffer returns the
// envStreamSubscriberBufferOverride value when it is set to a valid
// positive integer, and the production default (64) otherwise. An invalid
// or non-positive override is silently ignored in favor of the default
// rather than failing package initialization, since a malformed test-only
// environment variable must never be able to crash production startup.
func resolveDefaultStreamSubscriberBuffer() int {
	const def = 64
	if raw := os.Getenv(envStreamSubscriberBufferOverride); raw != "" {
		if n, err := strconv.Atoi(raw); err == nil && n > 0 {
			return n
		}
	}
	return def
}

func (o Options) withDefaults() Options {
	if o.Clock == nil {
		o.Clock = time.Now
	}
	if o.Logger == nil {
		o.Logger = slog.Default()
	}
	if o.StreamTickInterval <= 0 {
		o.StreamTickInterval = defaultStreamTickInterval
	}
	if o.StreamKeepaliveInterval <= 0 {
		o.StreamKeepaliveInterval = defaultStreamKeepaliveInterval
	}
	if o.StreamSubscriberBuffer <= 0 {
		o.StreamSubscriberBuffer = defaultStreamSubscriberBuffer
	}
	return o
}

// API is what [New] builds: an http.Handler covering everything under
// /api/v1 (contract section 6.1), and the [Hub] backing /api/v1/stream.
//
// Neither field wires itself into anything. The caller (a later wiring
// task, per this task's spec: "do NOT wire anything into
// internal/coordinator/coordinator.go") is responsible for mounting
// Handler on the coordinator's HTTP server, starting Hub.Run in its own
// goroutine with a context tied to coordinator shutdown, and calling
// Hub.Notify whenever a wired dependency has fresh data — a store write, an
// inventory update, a completed collector poll — so a change reaches
// subscribers well before the next tick.
type API struct {
	Handler http.Handler
	Hub     *Hub
}

// New builds an [API] from deps and opts. It does not start anything: no
// goroutine runs until the caller starts [API.Hub].Run, and no HTTP
// request is served until the caller mounts [API.Handler] on a listening
// server.
func New(deps Dependencies, opts Options) *API {
	deps = deps.withDefaults()
	opts = opts.withDefaults()

	h := &handlers{deps: deps, clock: opts.Clock, logger: opts.Logger}
	hub := newHub(deps, opts, opts.Logger)

	mux := http.NewServeMux()
	// "{$}" matches only the exact path "/api/v1/", not every path under
	// it: net/http.ServeMux treats a bare trailing-slash pattern as a
	// subtree match (matching any path with that prefix). Without "{$}"
	// this route would silently swallow every unmatched /api/v1/... path
	// into the service descriptor handler instead of falling through to
	// the catch-all below, so a typo'd endpoint would 200 with a service
	// descriptor instead of 404 — caught by this package's own
	// TestUnknownV1RouteIsResourceNotFound, not by inspection.
	mux.HandleFunc("GET /api/v1/{$}", h.handleServiceDescriptor)
	mux.HandleFunc("GET /api/v1/snapshot", h.handleSnapshot)
	mux.HandleFunc("GET /api/v1/nodes", h.handleNodes)
	mux.HandleFunc("GET /api/v1/nodes/{nodeId}", h.handleNode)
	mux.HandleFunc("GET /api/v1/fpp", h.handleFPPList)
	mux.HandleFunc("GET /api/v1/fpp/{instanceId}", h.handleFPPInstance)
	mux.HandleFunc("GET /api/v1/observations", h.handleObservations)
	mux.HandleFunc("GET /api/v1/events", h.handleEvents)
	mux.HandleFunc("GET /api/v1/stream", hub.ServeHTTP)
	// Catch-all for anything else under /api/ (an unknown path version, or
	// a typo'd v1 route): see handleUnknownAPIPath's doc comment.
	//
	// Registered as "GET /api/...", not the unrestricted "/api/..." this
	// used to be (a Step 3 review correction, finding 2.8): every real
	// route above is also GET-only, so restricting this one to GET too is
	// what lets net/http.ServeMux's own method-mismatch detection actually
	// fire for a non-GET request to a real route, instead of this
	// catch-all winning the match first and answering a lying 404
	// resource-not-found for what is actually a 405.
	// withMethodNotAllowedAsProblem below (middleware.go) reformats
	// ServeMux's resulting plain-text 405 into this package's usual
	// problem+json shape; this pattern's own job is unchanged from before
	// — a GET to an unknown path version or a typo'd v1 route still
	// reaches handleUnknownAPIPath exactly as before, since GET requests
	// are unaffected by this restriction.
	mux.HandleFunc("GET /api/", handleUnknownAPIPath(opts.Logger, opts.Clock))

	handler := chain(
		withMethodNotAllowedAsProblem(mux, opts.Logger, opts.Clock),
		withRequestLogging(opts.Logger),
		withAPIVersionHeader,
		withCORS(opts.AllowedOrigins),
		withVersionNegotiation(opts.Logger, opts.Clock),
		withAuth(opts.AuthToken, opts.Logger, opts.Clock),
	)

	return &API{Handler: handler, Hub: hub}
}
