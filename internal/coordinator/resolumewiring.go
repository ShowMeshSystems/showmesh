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
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"net/url"
	"sync"
	"time"

	"github.com/showmeshsystems/showmesh/internal/coordinator/api"
	"github.com/showmeshsystems/showmesh/internal/coordinator/collector"
	"github.com/showmeshsystems/showmesh/internal/coordinator/collector/resolume"
	"github.com/showmeshsystems/showmesh/internal/coordinator/config"
	"github.com/showmeshsystems/showmesh/internal/coordinator/store"
	"github.com/showmeshsystems/showmesh/pkg/observation"
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

// resolumeInstanceLister adapts *store.Store plus the coordinator's
// resolved SHOWMESH_RESOLUME_ID into api.ResolumeLister (Track D seam E),
// mirroring fppInstanceLister's shape one file over: instanceID is resolved
// at CONSTRUCTION time here, unlike fppInstanceLister's live-per-call
// endpoints — deliberately, because this instance's identity does not
// change without a coordinator restart (SHOWMESH_RESOLUME_ID is not a
// runtime-editable configuration surface the way fpp.endpoints is), so
// there is no equivalent "removed via the API but still served" hazard
// fppInstanceLister's own doc comment records for itself. An empty
// instanceID (SHOWMESH_RESOLUME_URL unset) means ListInstances always
// answers an empty slice — Track D seam E spec section 2.2 rule 4's "an
// unconfigured coordinator returns an empty array."
type resolumeInstanceLister struct {
	st         *store.Store
	instanceID string
}

func (l resolumeInstanceLister) ListInstances(ctx context.Context) ([]api.ResolumeInstanceView, error) {
	if l.instanceID == "" {
		return nil, nil
	}
	obs, err := l.st.ListObservations(ctx, store.ObservationFilter{
		ResourceKind: observation.ResourceResolume,
		ResourceID:   l.instanceID,
	})
	if err != nil {
		return nil, fmt.Errorf("coordinator: list resolume instance observations for %q: %w", l.instanceID, err)
	}
	return []api.ResolumeInstanceView{{InstanceID: l.instanceID, Observations: obs}}, nil
}

// resolumeWiring is what newResolumeWiring hands back to coordinator.go's
// Run: the collector (Track D seam D-2/C), the watcher, and the
// collector's own status lister for apiDeps.Collectors. watcher and
// collector are both nil when the collector is disabled — see
// newResolumeWiring's doc comment.
//
// There used to be a second goroutine here, an Adapter that owned the
// only `GET /composition` read this seam performed. ADR-032 decision 2
// forbids that call outright — it is known to crash the target Arena
// build — so the Adapter, and the goroutine that ran it, are gone. What
// replaces the object-id resolution it used to perform is a later seam's
// job (a stored id map sourced from the operator's own composition file),
// not this file's.
type resolumeWiring struct {
	watcher   *resolume.Watcher
	collector *resolume.Collector
	status    resolumeCollectorStatusLister
}

// resolumeWebSocketSupervisorInterval bounds how quickly
// [resolumeWiring.RunWatcherSupervisor] notices a runtime change to
// resolume.FootprintControls.WebSocketEnabled — ADR-033/TRACK-D-D2-SPEC.md
// §3.3's own runtime switch. Short enough that flipping it (today: only at
// startup, from SHOWMESH_RESOLUME_WEBSOCKET_DISABLED; later: a future show
// mode) takes effect promptly; cheap enough that checking a single atomic
// bool this often costs nothing worth naming.
const resolumeWebSocketSupervisorInterval = 2 * time.Second

// RunWatcherSupervisor holds w.watcher's WebSocket connection open only
// while w.collector.Footprint().WebSocketEnabled() reports true —
// re-checked on resolumeWebSocketSupervisorInterval's own cadence, never
// captured once. This is the mechanism ADR-033/TRACK-D-D2-SPEC.md §3.3
// asks for: a value read at the point of decision, so a future
// installation-wide show mode can drive it at runtime via
// [resolume.FootprintControls.SetWebSocketEnabled] without reconstructing
// the Collector, the Watcher, or this coordinator. resolume.Watcher itself
// (watch.go) has no pause/resume concept — this supervisor is what adds
// one, entirely from the outside, by starting and cancelling a child
// context around w.watcher.Run as the footprint value changes.
//
// Must only be called when w.watcher != nil (i.e. SHOWMESH_RESOLUME_URL is
// set — see newResolumeWiring's own doc comment). Returns once ctx is
// cancelled, after any currently-running watcher goroutine this supervisor
// itself started has stopped — the identical "no leaked goroutines"
// contract every other background loop coordinator.go's Run starts
// already satisfies.
func (w resolumeWiring) RunWatcherSupervisor(ctx context.Context) {
	ticker := time.NewTicker(resolumeWebSocketSupervisorInterval)
	defer ticker.Stop()

	var running bool
	var cancelWatcher context.CancelFunc
	var wg sync.WaitGroup

	stop := func() {
		if !running {
			return
		}
		cancelWatcher()
		wg.Wait()
		running = false
	}
	start := func() {
		if running {
			return
		}
		var watcherCtx context.Context
		watcherCtx, cancelWatcher = context.WithCancel(ctx)
		wg.Add(1)
		go func() {
			defer wg.Done()
			w.watcher.Run(watcherCtx)
		}()
		running = true
	}

	for {
		if w.collector.Footprint().WebSocketEnabled() {
			start()
		} else {
			stop()
		}

		select {
		case <-ctx.Done():
			stop()
			return
		case <-ticker.C:
		}
	}
}

// newResolumeWiring constructs Track D seam D-1's REST collector and
// WebSocket watcher, and registers the collector on runner.
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
// compositionStore is Track D seam D-2/B's own [resolume.CompositionStore]
// (built by newResolumeCompositionWiring, which coordinator.go's Run now
// constructs BEFORE calling this function, precisely so that store already
// exists to hand to the Collector here) — the same store an uploaded
// composition lands in, regardless of whether any live Resolume instance
// is configured at all. Passing it through as a parameter, rather than
// this function constructing its own, is what lets [resolume.Collector.Survey]
// see a composition uploaded before this coordinator process ever started,
// with no restart required — the identical property
// newResolumeCompositionWiring's own doc comment already establishes for
// itself.
func newResolumeWiring(ctx context.Context, cfg config.Config, runner *collector.Runner, compositionStore *resolume.CompositionStore, logger *slog.Logger) (resolumeWiring, error) {
	if cfg.ResolumeURL == "" {
		return resolumeWiring{status: resolumeCollectorStatusLister{configured: false}}, nil
	}

	// One shared *http.Client for the Collector's internally-built one,
	// matching coordinator.go's own fppHTTPClient precedent ("callers
	// SHOULD construct one *http.Client ... rather than one per
	// instance"). Only one instance uses this client today (the
	// Collector's own), but it is still constructed once here rather than
	// left to resolume.New's own internal default, so a future seam that
	// adds a second Resolume-facing client at this wiring layer has
	// something to share rather than a reason to invent its own.
	resolumeHTTPClient := &http.Client{
		Transport: &http.Transport{
			MaxIdleConnsPerHost: 4,
			IdleConnTimeout:     90 * time.Second,
		},
	}

	// footprint is TRACK-D-D2-SPEC.md §3.3's two runtime-adjustable knobs
	// (ADR-033), seeded from this process's own config at startup and
	// never touched again by this function — see
	// [resolume.FootprintControls]'s own doc comment for why that is
	// exactly the shape a future show mode needs: a value read fresh at
	// the point of decision, not a constant baked into this call.
	footprint := resolume.NewFootprintControls()
	if cfg.ResolumePollInterval > 0 {
		footprint.SetPollInterval(cfg.ResolumePollInterval)
	}
	if cfg.ResolumeWebSocketDisabled {
		footprint.SetWebSocketEnabled(false)
	}

	resolumeCollector, err := resolume.New(cfg.ResolumeID, cfg.ResolumeURL, resolume.Options{
		HTTPClient:       resolumeHTTPClient,
		Logger:           logger,
		CompositionStore: compositionStore,
		Footprint:        footprint,
	})
	if err != nil {
		return resolumeWiring{}, fmt.Errorf("resolume collector %q: %w", cfg.ResolumeID, err)
	}

	// runner.Add's own interval is DefaultRunnerCheckInterval, NOT the
	// liveness poll interval — see that constant's own doc comment in
	// collector.go: [resolume.Collector.Poll] self-throttles its own
	// /product request against footprint.PollInterval(), re-read on every
	// call, which is what makes the poll interval itself one of ADR-033's
	// runtime-adjustable knobs rather than a value fixed here for the life
	// of this process.
	runner.Add(resolumeCollector, resolume.DefaultRunnerCheckInterval)

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

		// OnConnect is Track D seam D-2/C's own survey trigger
		// (TRACK-D-D2-SPEC.md §3.1: "an explicit operator request, and on
		// a confirmed reconnect" — this is the second of those two, and
		// the only one this seam wires automatically). RequestSurvey(true)
		// also reopens §7's load window, so every layer readiness and the
		// composition identity signal report unknown, naming it, until
		// this survey (or a later one) produces a determinate identity
		// result — exactly what §7 requires between a connect and a
		// successful identity check. runner.Nudge asks the collector to
		// poll sooner than its ordinary cadence so the survey (and the
		// liveness signals) catch up promptly rather than waiting out
		// whatever of the dynamic poll interval remains; a suppressed or
		// rate-limited nudge is not an error (see OnChange's own comment
		// below) — the survey request itself is queued regardless and is
		// picked up on this collector's next poll either way.
		//
		// OnDisconnect has no wiring here: nothing else in this seam needs
		// to know when the WebSocket disconnects specifically (as opposed
		// to reconnecting, which OnConnect already handles), and
		// [resolume.WatcherOptions.OnDisconnect]'s own doc comment already
		// treats a nil callback as ordinary.
		OnConnect: func(context.Context) {
			resolumeCollector.RequestSurvey(true)
			runner.Nudge(cfg.ResolumeID)
		},
		//
		// OnChange is the whole point of the WebSocket: a message means
		// "something may have changed", and is NEVER itself an
		// observation. Its only remaining consumer is runner.Nudge, which
		// asks the shared collector.Runner to poll this collector's own
		// /api/v1/product read immediately instead of waiting out its
		// ordinary ~10s cadence, so resolume.reachable and resolume.product
		// catch up sooner — nothing about what that poll finds, or how a
		// caller interprets it, changes.
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
		// nothing about a suppressed nudge is worth a log line, let alone a
		// warning.
		OnChange: func(context.Context) {
			runner.Nudge(cfg.ResolumeID)
		},
	})
	if err != nil {
		return resolumeWiring{}, fmt.Errorf("resolume watcher %q: %w", cfg.ResolumeID, err)
	}

	return resolumeWiring{watcher: watcher, collector: resolumeCollector, status: resolumeCollectorStatusLister{configured: true}}, nil
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

// --- Track D seam D-2/B: the stored composition's tracked-object set -----
//
// Everything below bridges internal/coordinator/api/resolumecomposition.go's
// stored resolume.composition config revision (ADR-032 decisions 1 and 7:
// the composition id map is configuration, sourced from an uploaded .avc
// file, never from a runtime Resolume read) into a
// [resolume.CompositionStore] a collector can query. It never constructs a
// resolume.Client, never opens a URL, and never touches anything Resolume
// itself is reachable at — this seam's only dependency is *store.Store,
// the same generic config_objects/config_revisions repository
// api/resolumecomposition.go itself is built on (store/config.go's own doc
// comment: "it only ever treats payload_json as an opaque string").

// resolumeCompositionConfigKind and resolumeCompositionObjectID mirror
// internal/coordinator/api's own unexported resolumeCompositionConfigKind
// ("resolume.composition") and resolumeCompositionObjectIDConst
// ("resolume") constants — see that package's resolumecomposition.go — by
// VALUE, not by import. This seam does not own the api package, and
// importing it here to reach two private string constants would be a
// larger coupling than duplicating two literals, the identical tradeoff
// resolumeCollectorSourceID above already made for resolume.go's own
// unexported sourceName. A mismatch between these two definitions would
// silently make [storeCompositionConfigReader] report "nothing uploaded"
// forever, regardless of what an operator actually uploads — which is
// exactly why this comment names both constants explicitly, rather than
// leaving a future reader to rediscover the coupling by testing it.
const (
	resolumeCompositionConfigKind = "resolume.composition"
	resolumeCompositionObjectID   = "resolume"
)

// resolumeCompositionRefreshInterval bounds how quickly an uploaded
// composition (POST /config/resolume/composition) reaches this
// coordinator's in-memory tracked-object set without a restart. SHOWMESH
// GUESS, NOT MEASURED: composition upload is a rare, operator-initiated
// authoring action, never a live-show event on any clock this project has
// an accepted rule about (unlike resolume.DefaultPollInterval, which this
// interval is deliberately NOT tied to — see
// [newResolumeCompositionWiring]'s own doc comment for why this runs
// independently of whether a live Resolume collector is even configured).
// This only needs to be short enough that "upload, then check the
// dashboard" does not look broken to an operator sitting at the console.
const resolumeCompositionRefreshInterval = 5 * time.Second

// storeCompositionConfigReader adapts *store.Store to
// [resolume.CompositionConfigReader] — the interface that package declares
// for itself, at the consumer, per this project's own standing convention
// (Step 3 contract §5: "declare interfaces at the consumer, not the
// producer"). *store.Store's own generic config_objects/config_revisions
// methods already have everything this needs; this type's only job is
// naming the (kind, id) pair and unwrapping the "composition" member of
// the stored payload envelope api/resolumecomposition.go writes, so
// package resolume's own [resolume.CompositionStore] never has to know
// that envelope's shape.
type storeCompositionConfigReader struct {
	st *store.Store
}

// resolumeCompositionStoredEnvelope mirrors ONLY the one field of
// api/resolumecomposition.go's private resolumeCompositionStoredPayload
// this reader needs — the "composition" member — decoded as raw JSON so
// this file never has to import pkg/resolumecomp's own type twice or keep
// a second struct in step with sourceFilename/contentHash/sizeBytes, which
// nothing here reads. json.RawMessage here is handed straight to
// [resolume.CompositionStore.Refresh], which is the one place that bytes
// are actually unmarshaled into a [resolumecomp.Composition] — see that
// method's own doc comment.
type resolumeCompositionStoredEnvelope struct {
	Composition json.RawMessage `json:"composition"`
}

// CurrentCompositionRevision implements [resolume.CompositionConfigReader].
// ok is false — with revision 0, compositionJSON nil, and err nil — for
// every shape of "nothing uploaded yet": no config object at all for this
// kind (store.ErrConfigObjectNotFound), or one that exists with
// CurrentRevision still 0 (store/config.go's own "declared, nothing active
// yet" state — unreachable via api/resolumecomposition.go's own upload
// handler today, which activates a revision in the same transaction that
// creates the object, but not assumed unreachable here either, matching
// handleGetResolumeComposition's own identical guard in that file).
func (r storeCompositionConfigReader) CurrentCompositionRevision(ctx context.Context) (revision int64, compositionJSON []byte, ok bool, err error) {
	obj, err := r.st.GetConfigObject(ctx, resolumeCompositionConfigKind, resolumeCompositionObjectID)
	if errors.Is(err, store.ErrConfigObjectNotFound) {
		return 0, nil, false, nil
	}
	if err != nil {
		return 0, nil, false, fmt.Errorf("get resolume.composition config object: %w", err)
	}
	if obj.CurrentRevision == 0 {
		return 0, nil, false, nil
	}

	rev, err := r.st.GetConfigRevision(ctx, resolumeCompositionConfigKind, resolumeCompositionObjectID, obj.CurrentRevision)
	if err != nil {
		return 0, nil, false, fmt.Errorf("get resolume.composition config revision %d: %w", obj.CurrentRevision, err)
	}

	var envelope resolumeCompositionStoredEnvelope
	if err := json.Unmarshal([]byte(rev.PayloadJSON), &envelope); err != nil {
		return 0, nil, false, fmt.Errorf("decode resolume.composition payload envelope (revision %d): %w", obj.CurrentRevision, err)
	}

	return obj.CurrentRevision, envelope.Composition, true, nil
}

// resolumeCompositionWiring is Track D seam D-2/B's own wiring: the
// concurrency-safe [resolume.CompositionStore] holding this coordinator's
// current tracked-object set, kept current by periodically re-reading the
// resolume.composition config revision (ADR-032 decisions 1 and 7) —
// never by reading Resolume itself. This type's own construction never
// acquires a Resolume *http.Client, never builds a URL, and imports
// nothing this seam's own composition.go/client.go files do — there is
// nothing here capable of performing the ADR-032 decision 2 read even by
// accident.
//
// Deliberately constructed and run regardless of cfg.ResolumeURL, unlike
// [resolumeWiring] above: composition upload
// (internal/coordinator/api/resolumecomposition.go) has no relationship to
// whether any live Resolume instance is configured at all — see that
// file's own resolumeCompositionObjectIDConst doc comment, "a pure
// file-parsing feature that never talks to Arena at all." Gating this on
// cfg.ResolumeURL would mean an operator who uploads a composition before
// ever setting SHOWMESH_RESOLUME_URL gets no tracked-object set until a
// restart — exactly the kind of restart-dependency ADR-032's own
// configuration surface exists to avoid, and precisely the property
// TRACK-D-D2-SPEC.md §9's D-2/B row asks for ("pick up a newly uploaded
// composition without a coordinator restart").
type resolumeCompositionWiring struct {
	store  *resolume.CompositionStore
	reader storeCompositionConfigReader
	logger *slog.Logger
}

// newResolumeCompositionWiring constructs the wiring and performs one
// synchronous load of whatever composition revision is already active, so
// the returned wiring's store reflects real state (a loaded composition,
// or the correctly-distinguished "not uploaded" state) from the moment
// this function returns, rather than waiting out
// resolumeCompositionRefreshInterval's first tick.
//
// A load failure here is logged, never fatal — matching
// api.ReconcileStrandedFPPCommands' own reasoning at its call site in
// coordinator.go: this is a bounded local SQLite read with no principal to
// hold accountable for a boot-time failure of it, and ADR-024 constraint
// 23 draws its fail-closed line at "you cannot act," never "you cannot
// see." A coordinator that cannot read this on boot still starts, and
// [resolumeCompositionWiring.Run]'s own periodic retries get another
// chance every resolumeCompositionRefreshInterval.
func newResolumeCompositionWiring(ctx context.Context, st *store.Store, logger *slog.Logger) *resolumeCompositionWiring {
	w := &resolumeCompositionWiring{
		store:  &resolume.CompositionStore{},
		reader: storeCompositionConfigReader{st: st},
		logger: logger,
	}

	if err := w.store.Refresh(ctx, w.reader); err != nil {
		logger.Warn("failed to load resolume composition config revision at startup", "error", err)
	} else if rev := w.store.LoadedRevision(); rev > 0 {
		logger.Info("loaded resolume composition config revision", "revision", rev)
	} else {
		logger.Info("no resolume composition has been uploaded yet")
	}

	return w
}

// Run periodically re-checks the resolume.composition config revision and
// installs a freshly built tracked-object set whenever it has moved, until
// ctx is cancelled. Every failure is logged at Warn and retried on the
// next tick — never fatal, matching newResolumeCompositionWiring's own
// reasoning for its one synchronous load. Intended to be started in its
// own goroutine, joined the same way resolumeWire.watcher.Run and every
// other background loop in coordinator.go's Run function is (see that
// file's own backgroundWG).
func (w *resolumeCompositionWiring) Run(ctx context.Context) {
	ticker := time.NewTicker(resolumeCompositionRefreshInterval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			if err := w.store.Refresh(ctx, w.reader); err != nil {
				w.logger.Warn("failed to refresh resolume composition config revision", "error", err)
			}
		}
	}
}
