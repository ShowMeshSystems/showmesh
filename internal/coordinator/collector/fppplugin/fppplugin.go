// Package fppplugin is the push-fed collector.Collector that makes the
// ShowMesh FPP plugin the primary source for FPP playback state, with the
// REST collector (internal/coordinator/collector/fpp) as its fallback.
// Unlike every other collector in this repository, this one
// never dials anything itself: Poll only renders whatever Observe has
// already recorded, exactly like internal/coordinator/collector/fppmqtt's
// messageStore does for its own push-to-poll shape (see that package's
// store.go doc comment) — the ingest handler
// (internal/coordinator/api/fppobservations.go) calls Observe once per
// newly accepted plugin observation, and the caller-supplied pushSignal
// callback is how it asks its own dedicated collector.Runner to poll
// sooner than its own cadence, rather than waiting one out.
package fppplugin

import (
	"context"
	"sync"
	"time"

	"github.com/showmeshsystems/showmesh/internal/coordinator/collector"
	"github.com/showmeshsystems/showmesh/internal/coordinator/collector/fpp"
	"github.com/showmeshsystems/showmesh/pkg/observation"
)

// Collector implements collector.Collector; enforced at compile time so a
// signature drift is caught here rather than only at the wiring site.
var _ collector.Collector = (*Collector)(nil)

// CollectorID is this collector's fixed [collector.Collector.ID], used both
// to register it with its own dedicated [collector.Runner] and to nudge
// that same registration from the ingest handler.
const CollectorID = "fpp-plugin"

// sourceName is [observation.Observation.Source] for every observation this
// package produces.
const sourceName = "fpp-plugin"

// DefaultValidFor is deliberately [fpp.DefaultValidFor] (45s), the REST
// collector's own bound, and NOT a number derived from the plugin's actual
// "playing" tick cadence — that cadence is not measured or documented
// anywhere this collector's contract (FPP-PLUGIN-COORDINATOR-CONTRACTS.md
// §1) or the plugin's own source states, and inventing one would be
// exactly the mistake this design was built to avoid. This bound is safe
// regardless: the plugin's tick, whatever its real interval turns out to
// be, is surely faster than 45s, so an actively playing entry stays
// current between ticks and the launch decision sees sub-second evidence,
// which is the whole win; a plugin that stops ticking without ever
// reporting "stop" ages this source's own value out at 45s exactly like a
// REST value would, at which point [observation.ResolveObservations]'s
// tier-1 "later ObservedAt wins" rule falls back to the REST collector's
// own 15s-cadence evidence, so the worst case degrades to today's
// behavior rather than regressing past it. Owner ruling, 2026-09-09: this
// is a deliberate conservative placeholder pending a real measurement of
// the plugin's "playing" tick interval on a bench FPP, and it can be
// tightened once that measurement exists.
const DefaultValidFor = fpp.DefaultValidFor

// DefaultPollInterval is this collector's own backstop poll cadence. It
// exists only so a newly-registered instance with no plugin evidence yet
// (or one whose endpoint correlation is currently ambiguous) still gets
// re-rendered periodically; it plays no part in freshness, since every
// value's ObservedAt/CollectedAt is stamped at push time, not at Poll
// time. Deliberately long: this collector is push-fed and a
// [collector.Runner.Nudge] (registered by the wiring site with a short
// [collector.WithNudgeMinInterval]) is what actually delivers evidence
// promptly, not this cadence.
const DefaultPollInterval = 30 * time.Second

// EndpointResolver maps a plugin-reported instanceUUID to the configured
// fpp.endpoints id that currently, and uniquely, owns it — the identical
// correlation internal/coordinator/api/fppobservations.go's
// fppEndpointIDsByInstanceUUID already performs for the GET surface.
// Declared here, at the consumer, because this package must not import
// internal/coordinator/api or internal/coordinator/store (the reverse
// import already runs the other way): the real implementation is built by
// coordinator.go from the same *store.Store and configured-endpoints
// source api's own adapter uses.
type EndpointResolver interface {
	// ResolveEndpointID reports the single configured endpoint id that
	// currently claims instanceUUID. ok is false when zero or more than
	// one endpoint claims it right now, matching
	// fppEndpointIDsByInstanceUUID's identical "ambiguous means absent"
	// rule.
	ResolveEndpointID(ctx context.Context, instanceUUID string) (endpointID string, ok bool)
}

// instanceState is the latest derived playback state for one plugin
// instanceUUID, each field carrying its own capture time so Poll can
// render it with the SAME ObservedAt/CollectedAt Observe stamped it with —
// never a time recomputed at Poll time. See Observe's own doc comment for
// how these fields are derived from the wire action vocabulary.
type instanceState struct {
	hasStatus   bool
	status      string
	statusAt    time.Time
	hasPlaylist bool
	playlist    string
	playlistAt  time.Time
}

// Collector is the push-fed collector.Collector this package exists to
// provide. Construct with New.
type Collector struct {
	resolver     EndpointResolver
	pushSignal   func()
	now          func() time.Time
	validFor     time.Duration
	pollInterval time.Duration

	mu    sync.Mutex
	state map[string]*instanceState // instanceUUID -> latest derived state
}

// Option adjusts optional Collector construction fields.
type Option func(*Collector)

// WithValidFor overrides DefaultValidFor.
func WithValidFor(d time.Duration) Option {
	return func(c *Collector) { c.validFor = d }
}

// WithPollInterval overrides DefaultPollInterval. Exists for tests that
// need to prove a delivery did NOT happen via an ordinary poll tick — see
// this package's own end-to-end latency test — production wiring has no
// reason to override it.
func WithPollInterval(d time.Duration) Option {
	return func(c *Collector) { c.pollInterval = d }
}

// New constructs a Collector. pushSignal is called, with no arguments,
// once per Observe call that changed this collector's state — the wiring
// site's own closure over its dedicated collector.Runner.Nudge(CollectorID)
// (see collector.go's Nudge doc comment for why a suppressed nudge is
// never an error: this collector's own scheduled cadence covers it either
// way). pushSignal may be nil, which is exactly like every nudge this
// codebase already treats as "no [collector.Runner.Nudge] wired in": this
// collector still works, on its ordinary poll cadence alone.
func New(resolver EndpointResolver, pushSignal func(), opts ...Option) *Collector {
	c := &Collector{
		resolver:     resolver,
		pushSignal:   pushSignal,
		now:          time.Now,
		validFor:     DefaultValidFor,
		pollInterval: DefaultPollInterval,
		state:        make(map[string]*instanceState),
	}
	for _, opt := range opts {
		opt(c)
	}
	return c
}

// ID implements collector.Collector.
func (c *Collector) ID() string { return CollectorID }

// PollInterval is the recommended collector.Runner.Add cadence for this
// Collector. See DefaultPollInterval.
func (c *Collector) PollInterval() time.Duration { return c.pollInterval }

// Observe records instanceUUID's derived playback state from one accepted
// (never replayed or refused) plugin observation. The caller — the ingest
// handler — is responsible for calling this only on genuine acceptance:
// this method has no notion of sequence, replay, or conflict, and calling
// it for a replayed observation would fabricate a second, spurious push
// for evidence the store already holds.
//
// now is the coordinator's own clock at the moment the observation was
// accepted, NEVER the plugin's own observedAtMillis: it becomes both
// ObservedAt and CollectedAt for every Observation this state later
// produces, and observation.Observation.StateAt judges staleness against
// ObservedAt, so a foreign clock here would put an FPP host's clock skew
// on one side of every freshness comparison and the coordinator's own on
// the other.
//
// Status is derived from action alone, matching
// FPP-PLUGIN-COORDINATOR-CONTRACTS.md §1.8's own occurrence rule rather
// than inventing a second one: "start" or "playing" sets it to "playing",
// "stop" sets it to "idle", and "query_next" or "unknown" leaves it
// unchanged — an ordinary mid-playlist query or an unrecognized action is
// corroborating evidence for playlistName only, never a claim that
// playback just (re)started, which is exactly what
// TestObserveQueryNextNeverProducesPlaying pins.
//
// playlistName is recorded whenever unavailable is empty and playlistName
// is non-empty, independent of action, since the contract requires it as
// corroborating evidence on every non-unavailable observation regardless
// of which action carries it.
func (c *Collector) Observe(instanceUUID, action, playlistName, unavailable string, now time.Time) {
	if instanceUUID == "" {
		return
	}

	c.mu.Lock()
	st, ok := c.state[instanceUUID]
	if !ok {
		st = &instanceState{}
		c.state[instanceUUID] = st
	}
	changed := false
	switch action {
	case "start", "playing":
		if !st.hasStatus || st.status != "playing" {
			changed = true
		}
		st.hasStatus = true
		st.status = "playing"
		st.statusAt = now
	case "stop":
		if !st.hasStatus || st.status != "idle" {
			changed = true
		}
		st.hasStatus = true
		st.status = "idle"
		st.statusAt = now
	default:
		// "query_next", "unknown", or any future action this vocabulary
		// grows: carries no playback-status claim of its own (§1.8), so
		// the prior status (if any) is left exactly as it was, still
		// timestamped when IT was last observed, not now.
	}
	if unavailable == "" && playlistName != "" {
		if !st.hasPlaylist || st.playlist != playlistName {
			changed = true
		}
		st.hasPlaylist = true
		st.playlist = playlistName
		st.playlistAt = now
	}
	c.mu.Unlock()

	if changed && c.pushSignal != nil {
		c.pushSignal()
	}
}

// snapshot returns a shallow copy of every instanceUUID's current state,
// safe to range over without further locking.
func (c *Collector) snapshot() map[string]instanceState {
	c.mu.Lock()
	defer c.mu.Unlock()

	out := make(map[string]instanceState, len(c.state))
	for uuid, st := range c.state {
		out[uuid] = *st
	}
	return out
}

// Poll implements collector.Collector. It never performs network I/O: it
// only renders whatever Observe has already recorded, resolving each
// known instanceUUID to its configured endpoint id at render time (an
// endpoint reconfiguration or a resolved duplicate-uuid conflict is
// therefore reflected on the very next Poll, with no restart). An
// instanceUUID this collector holds state for but cannot currently
// resolve to a single endpoint produces no observation at all this
// cycle — never a guess at which endpoint it belongs to.
//
// complete is always true: every observation this collector could
// possibly produce this cycle is included, for every resource it mentions
// at all (see collector.Collector.Poll's own doc comment for why that,
// and not "every resource that has ever reported", is what complete
// means) — mirroring the fppmqtt package's identical "no notion of a
// partial or skipped cycle" rule.
func (c *Collector) Poll(ctx context.Context) ([]observation.Observation, bool) {
	snap := c.snapshot()

	var obs []observation.Observation
	for uuid, st := range snap {
		if ctx.Err() != nil {
			return nil, false
		}
		endpointID, ok := c.resolver.ResolveEndpointID(ctx, uuid)
		if !ok {
			continue
		}
		res := observation.ResourceRef{Kind: observation.ResourceFPP, ID: endpointID}

		if st.hasStatus {
			obs = append(obs, c.measured(res, fpp.SignalStatus, st.status, st.statusAt))
		}
		if st.hasPlaylist {
			obs = append(obs, c.measured(res, fpp.SignalPlaylistName, st.playlist, st.playlistAt))
		}
	}
	return obs, true
}

func (c *Collector) measured(res observation.ResourceRef, sig observation.SignalID, value string, observedAt time.Time) observation.Observation {
	o, err := observation.Measured(res, sig, value, observedAt,
		observation.WithSource(sourceName),
		observation.WithValidFor(c.validFor),
		observation.WithCollectedAt(observedAt),
	)
	if err != nil {
		// Unreachable in practice: value is always a non-empty string and
		// res/sig are both fixed, valid constants. Fall back to an
		// absence observation rather than a panic, matching this
		// codebase's standing rule that a Collector must never crash the
		// process over its own evidence.
		absent, _ := observation.CollectionFailed(res, sig, "internal: failed to construct observation: "+err.Error(),
			observation.WithSource(sourceName), observation.WithCollectedAt(c.now()))
		return absent
	}
	return o
}
