package coordinator

// This file is Track D seam D-1's own wiring seam, the same role
// apiwiring.go plays for Step 3's Task C/Task D boundary: nothing here is
// domain logic, it makes internal/coordinator/collector/resolume's already-
// built types run inside this coordinator process. It is a separate file
// from coordinator.go and apiwiring.go on purpose — this seam is one
// self-contained, independently reviewable unit (construct, wire, tear
// down), the way configsync.go already carries Step 7 seam A's own
// self-contained unit.

import (
	"context"
	"fmt"
	"log/slog"
	"net/http"
	"net/url"
	"time"

	"github.com/showmeshsystems/showmesh/internal/coordinator/api"
	"github.com/showmeshsystems/showmesh/internal/coordinator/collector"
	"github.com/showmeshsystems/showmesh/internal/coordinator/collector/resolume"
	"github.com/showmeshsystems/showmesh/internal/coordinator/config"
)

// resolumeCollectorSourceID is this collector's own id in GET
// /api/v1/snapshot's "collectors" list. Duplicated here as a Go constant
// rather than imported from package resolume (whose matching sourceName
// constant is unexported) for the identical reason apiwiring.go's
// fppMQTTCollectorSourceID already documents for itself: this file already
// inlines fppCollectorSourceID = "fpp-rest" the same way, and mixing "this
// source's name is a local constant" with "that one is imported from its
// producer package" would be an arbitrary inconsistency, not a considered
// choice. Kept identical to resolume.go's sourceName by hand; a mismatch
// would only ever affect a log/snapshot label, never evidence.
const resolumeCollectorSourceID = "resolume-rest"

// resolumeCollectorStatusLister reports the Resolume collector's own run
// state for GET /api/v1/snapshot's "collectors" list, mirroring
// [fppCollectorStatusLister] and [fppMQTTCollectorStatusLister]'s identical
// shape: always exactly one entry, id [resolumeCollectorSourceID],
// [api.CollectorRunning] when SHOWMESH_RESOLUME_URL is configured,
// [api.CollectorNotConfigured] naming why when it is not. A collector
// source invisible in this list is a source an operator cannot tell is
// broken — the same reasoning that list's own siblings already state.
type resolumeCollectorStatusLister struct {
	configured bool
}

func (l resolumeCollectorStatusLister) CollectorStatuses(context.Context) ([]api.CollectorState, error) {
	if !l.configured {
		reason := "no Resolume Arena instance configured (SHOWMESH_RESOLUME_URL is unset)"
		return []api.CollectorState{{ID: resolumeCollectorSourceID, State: string(api.CollectorNotConfigured), Reason: &reason}}, nil
	}
	return []api.CollectorState{{ID: resolumeCollectorSourceID, State: string(api.CollectorRunning)}}, nil
}

// resolumeWiring is what newResolumeWiring hands back to coordinator.go's
// Run: the watcher AND the adapter, each to run in its own goroutine (both
// nil when the collector is disabled — see newResolumeWiring's doc
// comment), and the collector's own status lister for apiDeps.Collectors.
//
// watcher and adapter are two separate goroutines by design (review
// finding A, 2026-08-14): adapter.Run owns the ONLY composition read this
// seam performs and must run continuously for the adapter's own
// coalescing/retry policy to work (see resolume.Adapter's doc comment),
// entirely independent of whether the WebSocket happens to be up at any
// given moment — watcher.Run's OWN job is just driving that WebSocket and
// turning what it sees into cheap, non-blocking signals
// (HandleConnect/HandleDisconnect/HandleChange) that adapter.Run consumes.
type resolumeWiring struct {
	watcher *resolume.Watcher
	adapter *resolume.Adapter
	status  resolumeCollectorStatusLister
}

// newResolumeWiring constructs Track D seam D-1's REST client, collector,
// adapter, and WebSocket watcher, and registers the collector on runner.
// Returns a zero-value watcher (status reporting CollectorNotConfigured)
// when cfg.ResolumeURL is empty — the identical feature-flag shape
// cfg.FPPMQTTBrokerURL already established in coordinator.go: no
// goroutine, no warning storm, no failed-connection signals for a feature
// the operator did not enable.
//
// runner is the SAME *collector.Runner coordinator.go registers every
// configured FPP endpoint's collector on, not a second Runner built for
// this seam alone. collector.Runner's own package doc comment states the
// design intent directly ("a second source ... slots in later without
// reshaping anything here"), and Runner.Add/Runner.Nudge both key their
// internal maps by one collector id string regardless of which Runner
// instance holds the entry — so a second Runner here would not remove the
// id-collision hazard [config.ValidateResolumeIDAgainstFPPEndpoints]
// guards against, it would only hide the same hazard behind two maps
// instead of one, while adding a second goroutine-management and shutdown-
// join story for no benefit. One Runner, one shared collector.Sink
// (coordinator.go's *fppSink, which persists via *store.Store and pokes
// the hub — nothing in it is FPP-specific despite its name; see its own
// doc comment), one shutdown join.
//
// Every error path here is treated the same way fpp.New's own construction
// loop in coordinator.go already treats a per-endpoint construction
// failure: cfg.Validate has already checked SHOWMESH_RESOLUME_URL's shape
// (config.validateResolumeConfig) and SHOWMESH_RESOLUME_ID's syntax and
// uniqueness, so resolume.NewClient, resolume.New, or resolume.NewWatcher
// failing here would mean those checks and this package's own have
// drifted apart — a startup-worthy inconsistency in this binary, not a
// per-instance condition to skip past silently. This is deliberately
// UNLIKE the collector actually being unreachable at runtime (a wrong
// host, Resolume not running, a refused WebSocket handshake), which is
// never fatal — see resolume.Collector.Poll's and resolume.Watcher.Run's
// own doc comments: those failures become collection_failed observations
// and reconnect-with-backoff respectively, not a startup error.
func newResolumeWiring(cfg config.Config, runner *collector.Runner, logger *slog.Logger) (resolumeWiring, error) {
	if cfg.ResolumeURL == "" {
		return resolumeWiring{status: resolumeCollectorStatusLister{configured: false}}, nil
	}

	// One shared *http.Client for both the Adapter's own Client and the
	// Collector's internally-built one, matching coordinator.go's own
	// fppHTTPClient precedent ("callers SHOULD construct one *http.Client
	// ... rather than one per instance"). Only two instances ever share
	// this client (the Adapter's Client and the Collector's own), so the
	// same small MaxIdleConnsPerHost fppHTTPClient uses is generous here
	// too.
	resolumeHTTPClient := &http.Client{
		Transport: &http.Transport{
			MaxIdleConnsPerHost: 4,
			IdleConnTimeout:     90 * time.Second,
		},
	}

	client, err := resolume.NewClient(cfg.ResolumeURL, resolume.ClientOptions{HTTPClient: resolumeHTTPClient})
	if err != nil {
		return resolumeWiring{}, fmt.Errorf("resolume client %q: %w", cfg.ResolumeID, err)
	}

	resolumeCollector, err := resolume.New(cfg.ResolumeID, cfg.ResolumeURL, resolume.Options{
		HTTPClient: resolumeHTTPClient,
		Logger:     logger,
	})
	if err != nil {
		return resolumeWiring{}, fmt.Errorf("resolume collector %q: %w", cfg.ResolumeID, err)
	}
	runner.Add(resolumeCollector, resolume.DefaultPollInterval)

	// adapter.Run (started by coordinator.go alongside watcher.Run, on the
	// same backgroundWG) is the ONLY thing in this seam that ever performs
	// a composition read — see resolume.Adapter's own doc comment for why
	// that loop exists and what its coalescing/retry policy promises.
	// Every field left at AdapterOptions' zero value takes that package's
	// own documented default (resolveDebounceInterval, resolveMinInterval,
	// resolveRetryMinBackoff/Max) rather than being re-decided here.
	adapter := resolume.NewAdapter(client, resolume.AdapterOptions{Logger: logger})

	wsURL, err := resolumeWebSocketURL(cfg.ResolumeURL)
	if err != nil {
		// Unreachable given validateResolumeConfig already required an
		// http/https URL with a host — see resolumeWebSocketURL's own doc
		// comment — but handled per this function's own doc comment rather
		// than ignored, so a future drift between the two checks is a
		// startup error instead of a silently missing WebSocket wake-up.
		return resolumeWiring{}, fmt.Errorf("resolume websocket url for %q: %w", cfg.ResolumeID, err)
	}

	watcher, err := resolume.NewWatcher(resolume.WatcherOptions{
		URL:    wsURL,
		Logger: logger,

		// OnConnect/OnDisconnect fire the Adapter's own connect/disconnect
		// handling (resolume/adapter.go): a cheap, non-blocking signal to
		// adapter.Run's own loop, which is what actually performs the
		// fresh composition read that replaces the held Resolution
		// wholesale, or drops it entirely. Neither ever produces an
		// observation — Adapter is deliberately not a collector (see its
		// own doc comment) — so this wiring introduces no second source of
		// evidence alongside the collector's own REST poll.
		OnConnect: func(wsCtx context.Context) {
			adapter.HandleConnect(wsCtx)
		},
		OnDisconnect: func(wsCtx context.Context) {
			adapter.HandleDisconnect(wsCtx)
		},

		// OnChange is the whole point of the WebSocket (spec section 3.4):
		// a message means "something may have changed", and is NEVER
		// itself an observation. Review finding A (2026-08-14) is why BOTH
		// calls below fire from this one wake-up rather than either alone:
		// they are two different consumers of one signal, and neither
		// replaces the other.
		//
		// adapter.HandleChange feeds the Adapter's own resolve loop
		// (resolume/adapter.go's coalescing/retry policy), which is what
		// keeps the object/parameter id resolution this seam holds
		// current. runner.Nudge asks the shared collector.Runner to poll
		// this collector's own /api/v1/product read immediately instead of
		// waiting out its ordinary ~10s cadence, so resolume.reachable and
		// resolume.product catch up too — nothing about what that poll
		// finds, or how a caller interprets it, changes. Dropping either
		// call would silently stop updating whichever half it fed: dropping
		// HandleChange reintroduces this seam's own load-window defect
		// (Adapter's doc comment), and dropping Nudge means a live
		// WebSocket message stream with a stale reachable/product pair
		// until the next ordinary poll.
		//
		// Nudge is rate-limited per collector id by
		// collector.DefaultNudgeMinInterval, which is correct and
		// necessary here: the bench capture (resolume-control-surface.md
		// section 5.3) measured every clip connect, layer clear, and
		// disconnect-all pushing the full ~2.27 MB composition to every
		// connected WebSocket client, with no way found to subscribe
		// narrowly enough to avoid it, so a burst of change messages must
		// not become a burst of immediate REST polls against Resolume's
		// own API. A suppressed nudge (the rate limit, or a nudge already
		// pending) is NOT an error — Runner.Nudge's own doc comment states
		// the collector's ordinary cadence is entirely unaffected either
		// way — so its bool return is deliberately discarded here, exactly
		// as coordinator.go's fppRunnerNudger callers already treat it:
		// nothing about a suppressed nudge is worth a log line, let alone
		// a warning. adapter.HandleChange has its OWN, separate
		// debounce/min-interval policy (resolveDebounceInterval /
		// resolveMinInterval) and is never rate-limited by Nudge's — the
		// two consumers of this one wake-up bound their own costs
		// independently.
		OnChange: func(wsCtx context.Context) {
			adapter.HandleChange(wsCtx)
			runner.Nudge(cfg.ResolumeID)
		},
	})
	if err != nil {
		return resolumeWiring{}, fmt.Errorf("resolume watcher %q: %w", cfg.ResolumeID, err)
	}

	return resolumeWiring{watcher: watcher, adapter: adapter, status: resolumeCollectorStatusLister{configured: true}}, nil
}

// resolumeWebSocketURL derives Resolume's WebSocket endpoint from its REST
// base URL — http -> ws, https -> wss, same host and port, path
// "/api/v1" — rather than a second environment variable: BUILD-PLAN's own
// instruction for this seam is explicit that two URLs an operator must
// keep in agreement is a misconfiguration waiting to happen, and Resolume
// serves both the REST API and the WebSocket off the identical host/port
// (bench capture, resolume-control-surface.md section 5).
func resolumeWebSocketURL(restBaseURL string) (string, error) {
	u, err := url.Parse(restBaseURL)
	if err != nil {
		return "", fmt.Errorf("parsing %q: %w", restBaseURL, err)
	}
	switch u.Scheme {
	case "http":
		u.Scheme = "ws"
	case "https":
		u.Scheme = "wss"
	default:
		return "", fmt.Errorf("%q has scheme %q, want http or https", restBaseURL, u.Scheme)
	}
	u.Path = "/api/v1"
	u.RawQuery = ""
	u.Fragment = ""
	return u.String(), nil
}
