package resolume

import (
	"context"
	"fmt"
	"log/slog"
	"net/http"
	"time"

	"github.com/showmeshsystems/showmesh/internal/coordinator/collector"
	"github.com/showmeshsystems/showmesh/pkg/mqttproto"
	"github.com/showmeshsystems/showmesh/pkg/observation"
)

// Collector implements collector.Collector; enforced at compile time so a
// signature drift in either package is caught here.
var _ collector.Collector = (*Collector)(nil)

// sourceName is [observation.Observation.Source] for every observation
// this package produces.
const sourceName = "resolume-rest"

// DefaultPollInterval and DefaultValidFor are SHOWMESH HYPOTHESES, NOT
// MEASURED — the bench capture that produced this package never measured
// a safe polling cadence against a live show host, only /product's
// response shape and size (64 bytes) over loopback. Chosen the same way
// internal/coordinator/collector/fpp's own defaults are: frequent enough
// that a dashboard reads as reasonably live, infrequent enough that
// polling a live show host's REST API on a timer is not itself a concern
// — and /product is the cheapest possible probe this API offers, smaller
// than any single FPP endpoint this project already polls every 15s.
const (
	DefaultPollInterval = 10 * time.Second
	DefaultValidFor     = 3 * DefaultPollInterval
)

// Options configures a [Collector]. Every field left at its zero value is
// replaced by a documented default.
type Options struct {
	// HTTPClient is passed through to the [Client] this Collector builds
	// internally. See [ClientOptions.HTTPClient].
	HTTPClient *http.Client

	// ValidFor is set as [observation.WithValidFor] on every observation
	// this Collector produces. See DefaultValidFor.
	ValidFor time.Duration

	// RequestTimeout bounds the single GET /product request Poll makes.
	// See [DefaultRequestTimeout].
	RequestTimeout time.Duration

	// Now is the clock used for ObservedAt/CollectedAt on every
	// observation. nil (the default in production) means time.Now; tests
	// inject a fake clock.
	Now func() time.Time

	// Logger is currently unused by Collector itself (Poll never logs;
	// every outcome is an observation, per [collector.Collector]'s doc
	// comment) but is accepted for symmetry with [NewAdapter] and because
	// a future seam wiring this Collector alongside an Adapter will want
	// to construct both from one Options-shaped configuration. nil means
	// no logging.
	Logger *slog.Logger
}

// Collector polls one Resolume Arena instance's `/api/v1/product`
// endpoint and produces exactly two observations for it:
// [SignalReachable] and [SignalProduct]. It implements
// internal/coordinator/collector.Collector.
type Collector struct {
	id       string
	client   *Client
	validFor time.Duration
	now      func() time.Time
}

// New constructs a Collector for one Resolume Arena instance. id must
// satisfy [mqttproto.ValidateNodeID] — the same identifier syntax
// internal/coordinator/collector/fpp.New requires of its own instance
// ids, applied here for the same reason: Resolume is a controlled device
// (ADR-016), not a ShowMesh node, but its collector still needs a stable
// id for logging and the API's collectors[] list. baseURL is validated by
// [NewClient]; see its doc comment.
func New(id string, baseURL string, opts Options) (*Collector, error) {
	if err := mqttproto.ValidateNodeID(id); err != nil {
		return nil, fmt.Errorf("resolume collector: %w", err)
	}

	client, err := NewClient(baseURL, ClientOptions{
		HTTPClient:     opts.HTTPClient,
		RequestTimeout: opts.RequestTimeout,
	})
	if err != nil {
		return nil, fmt.Errorf("resolume collector %q: %w", id, err)
	}

	validFor := opts.ValidFor
	if validFor <= 0 {
		validFor = DefaultValidFor
	}
	now := opts.Now
	if now == nil {
		now = time.Now
	}

	return &Collector{
		id:       id,
		client:   client,
		validFor: validFor,
		now:      now,
	}, nil
}

// ID returns this instance's identifier.
func (c *Collector) ID() string { return c.id }

// Poll performs one collection cycle: a single GET /api/v1/product.
//
// On success, [SignalReachable] is measured true
// ([observation.QualityDerived] — reachability is derived from the
// request having succeeded at all, not a field Resolume itself reports)
// and [SignalProduct] is measured as [Product.String]'s canonical form
// ([observation.QualityDirect] — read straight from the response). On
// failure, BOTH signals become [observation.StateCollectionFailed] with
// [ClassifyError]'s reason — never a fabricated `false` for reachable,
// and never an omission: CLAUDE.md's "absent evidence is stated, never
// omitted" rule, and the same shape
// internal/coordinator/collector/fpp.Collector already follows for its
// own SignalReachable.
//
// SignalReachable IS TRANSPORT-LEVEL ONLY. It answers "did the HTTP
// request to Resolume's REST API succeed", nothing more. It NEVER implies
// the loaded show is actually present: the bench capture measured a
// window of roughly 1.2 seconds after an Arena restart during which
// /composition answers 200 OK with a complete, well-formed composition
// that is not the show — carrying the correct composition name for the
// last part of that window while 15 of 18 layers do not exist yet. This
// Collector does not read /composition at all and has no way to detect
// that window; a caller must never dispatch an action, or treat the show
// as ready, on SignalReachable alone. Verifying that the loaded
// composition IS the expected show is a later seam's job — this
// Collector's whole contract stops at "Resolume's REST API answered."
//
// complete is ALWAYS true. There is no backoff in this seam: the probe
// this Collector makes is a single ~64-byte request against one already-
// configured host, so skipping a cycle to protect Resolume from being
// polled has no real cost to weigh against the value of a fresh
// collection_failed answer — unlike
// internal/coordinator/collector/fpp.Collector, which backs off a
// four-endpoint poll against a device that also has a live show to run.
// A future backoff path added to this Collector would need to return
// complete=false for exactly the reason [collector.Collector.Poll]
// documents: a skipped cycle's empty result must never be read by a Sink
// as "this instance now owns zero signals."
func (c *Collector) Poll(ctx context.Context) ([]observation.Observation, bool) {
	now := c.now()

	product, err := c.client.Product(ctx)
	if err != nil {
		reason := ClassifyError(err)
		return []observation.Observation{
			c.failed(SignalReachable, reason, now),
			c.failed(SignalProduct, reason, now),
		}, true
	}

	reachable := c.measured(SignalReachable, true, now, observation.WithQuality(observation.QualityDerived))
	productObs := c.measured(SignalProduct, product.String(), now, observation.WithQuality(observation.QualityDirect))

	return []observation.Observation{reachable, productObs}, true
}

func (c *Collector) resource() observation.ResourceRef {
	return observation.ResourceRef{Kind: observation.ResourceResolume, ID: c.id}
}

// measured builds a StateCurrent observation via [observation.Measured],
// stamped with this Collector's source, ValidFor, and clock.
func (c *Collector) measured(sig observation.SignalID, value any, observedAt time.Time, opts ...observation.Option) observation.Observation {
	allOpts := append([]observation.Option{
		observation.WithSource(sourceName),
		observation.WithValidFor(c.validFor),
		observation.WithCollectedAt(observedAt),
	}, opts...)

	o, err := observation.Measured(c.resource(), sig, value, observedAt, allOpts...)
	if err != nil {
		// Unreachable in practice: both call sites above pass a value type
		// Validate accepts (bool, string) and a non-empty signal/resource
		// ID. Surfaced as a collection_failed observation rather than
		// dropped or panicking, so a future bug in this file is visible on
		// the API instead of invisible — mirrors
		// internal/coordinator/collector/fpp.Collector.measured exactly.
		return c.failed(sig, fmt.Sprintf("internal error building observation: %v", err), observedAt)
	}
	return o
}

// failed builds a StateCollectionFailed observation via
// [observation.CollectionFailed]. reason must be non-empty; both call
// sites above ([Poll]'s error path and [measured]'s defensive fallback)
// already guarantee that.
func (c *Collector) failed(sig observation.SignalID, reason string, observedAt time.Time) observation.Observation {
	o, err := observation.CollectionFailed(c.resource(), sig, reason,
		observation.WithSource(sourceName), observation.WithCollectedAt(observedAt))
	if err != nil {
		// reason is non-empty and sig/resource are always set by this
		// file; a failure here is a bug in this file, not a runtime
		// condition to degrade gracefully from.
		panic(fmt.Sprintf("resolume collector %q: CollectionFailed(%q) unexpectedly failed: %v", c.id, sig, err))
	}
	return o
}
