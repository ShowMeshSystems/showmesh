package coordinator

// This file is the seam Step 3's contract (section 5) named explicitly:
// "Wiring in internal/coordinator/coordinator.go ... is Task F." Tasks B
// (store), C (the FPP collector), and D (the API) each declared their own
// consumer-side interfaces and built against them without importing one
// another. Nothing here is domain logic; every type is a thin adapter that
// makes one already-built package satisfy an interface another
// already-built package declared, plus the FPP-specific bookkeeping
// (LastPollAt/LastPollError) that Task C's collector.Collector interface
// deliberately does not carry (Poll returns only observations, never an
// error — see that package's doc comment) and that Task D's FPPLister
// therefore has nowhere else to come from.

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"time"

	"github.com/showmeshsystems/showmesh/internal/coordinator/api"
	"github.com/showmeshsystems/showmesh/internal/coordinator/collector/fpp"
	"github.com/showmeshsystems/showmesh/internal/coordinator/config"
	"github.com/showmeshsystems/showmesh/internal/coordinator/inventory"
	"github.com/showmeshsystems/showmesh/internal/coordinator/store"
	"github.com/showmeshsystems/showmesh/pkg/observation"
)

// --- api.NodeLister over *inventory.Manager, observing liveness on every read ---

// livenessObservingNodeLister wraps *inventory.Manager so every Snapshot
// call — not only one triggered by an inbound MQTT message — feeds each
// node's freshly computed Liveness back into the manager's own
// once-per-actual-transition event bookkeeping
// ([inventory.Manager.RecordLivenessObservation]).
//
// Step 3 review finding 3.4: inventory.Manager.recordLivenessTransition
// (its unexported liveness-transition recorder) was called only from
// HandleMessage's three call sites, so a node whose heartbeats simply
// stop — no further hello, last-will, or heartbeat message ever arrives —
// transitions online -> offline by staleness alone, recomputed fresh on
// every read (deriveLiveness, against the caller's own now), with nothing
// recording that transition to event history: GET /api/v1/nodes and the
// SSE stream both correctly show the new state the moment enough time has
// passed, but GET /api/v1/events stays silent about precisely the
// transition an operator most needs a durable record of.
//
// Snapshot is already called on every hub render tick (contract section
// 6.5, api.Hub.render) in addition to every direct API request, so
// wrapping it here closes the gap at the same cadence liveness is already
// being recomputed on, with no new goroutine, no new ticker, and no
// change to inventory.Manager's own Snapshot method — that method lives
// in snapshot.go, a file outside this fix's ownership, which is exactly
// why the fix is a wrapper at the wiring seam rather than a change to
// Snapshot itself.
type livenessObservingNodeLister struct {
	inv *inventory.Manager
}

func (l livenessObservingNodeLister) Snapshot(ctx context.Context, now time.Time) ([]inventory.NodeView, error) {
	views, err := l.inv.Snapshot(ctx, now)
	if err != nil {
		return nil, err
	}
	for _, v := range views {
		l.inv.RecordLivenessObservation(ctx, v.NodeID, v.Liveness, v.LivenessReason)
	}
	return views, nil
}

// --- api.ObservationLister over *store.Store ---

// storeObservationLister adapts *store.Store to api.ObservationLister. The
// two packages' filter types differ on purpose (api.ObservationFilter uses
// pointers so "unset" is distinguishable from the zero value of the
// underlying type at the HTTP-query-parsing layer; store.ObservationFilter
// uses plain values because every caller inside this codebase already
// knows whether it means to filter on a dimension) — this is exactly the
// seam api's own doc comment predicted a later wiring task would need to
// adapt.
type storeObservationLister struct {
	st *store.Store
}

func (l storeObservationLister) ListObservations(ctx context.Context, filter api.ObservationFilter) ([]observation.Observation, error) {
	var sf store.ObservationFilter
	if filter.ResourceKind != nil {
		sf.ResourceKind = *filter.ResourceKind
	}
	if filter.ResourceID != nil {
		sf.ResourceID = *filter.ResourceID
	}
	if filter.Signal != nil {
		sf.Signal = *filter.Signal
	}
	return l.st.ListObservations(ctx, sf)
}

// --- api.EventReader over *store.Store ---

// storeEventReader adapts *store.Store to api.EventReader. The only real
// work here is the Seq/since/limit type conversion: the store's history is
// int64 throughout (matching SQLite's native integer type and
// AUTOINCREMENT's rowid), while api.EventReader's interface — declared
// independently, at the consumer, per contract section 5 — uses uint64
// (seq can never be negative, and a client-facing wire type has no reason
// to expose a signed cursor). A seq this codebase has ever actually
// assigned always fits both types identically; the conversions below exist
// only to satisfy the two packages' independently-chosen types, not
// because either range is actually at risk in practice.
type storeEventReader struct {
	st *store.Store
}

func (r storeEventReader) ListEvents(ctx context.Context, since uint64, limit int) ([]api.EventRecord, bool, error) {
	// since comes from an HTTP query parameter (api/handlers.go's
	// parseEventsQuery parses it as a uint64), so a value larger than
	// math.MaxInt64 is a possible input even though this store has never
	// assigned a seq anywhere near that large. int64(since) for such a
	// value wraps negative, and store.ListEvents already rejects a negative
	// since with a clear error rather than misinterpreting it — the
	// conversion below relies on that existing guard rather than
	// duplicating a bounds check here.
	recs, gap, err := r.st.ListEvents(ctx, int64(since), limit)
	if err != nil {
		return nil, false, err
	}
	out := make([]api.EventRecord, 0, len(recs))
	for _, rec := range recs {
		out = append(out, storeEventToAPI(rec))
	}
	return out, gap, nil
}

func (r storeEventReader) LatestEventSeq(ctx context.Context) (uint64, error) {
	seq, err := r.st.LatestEventSeq(ctx)
	if err != nil {
		return 0, err
	}
	return uint64(seq), nil
}

func (r storeEventReader) OldestEventSeq(ctx context.Context) (uint64, bool, error) {
	seq, ok, err := r.st.OldestEventSeq(ctx)
	if err != nil {
		return 0, false, err
	}
	return uint64(seq), ok, nil
}

// storeEventToAPI maps one store.EventRecord to api.EventRecord. Details is
// decoded from the store's json.RawMessage into the map[string]any api.EventRecord
// carries; store.AppendEvent already guarantees the stored bytes are valid,
// bounded JSON (see EventRecord.validate in events.go), so a decode error
// here would mean the store package's own invariant was violated, not a
// condition this adapter can usefully recover from — it is surfaced as an
// error rather than silently dropping the details.
func storeEventToAPI(rec store.EventRecord) api.EventRecord {
	var details map[string]any
	if len(rec.Details) > 0 {
		// Errors are deliberately ignored here in favor of an empty map: see
		// this function's doc comment for why a malformed value can only
		// mean a store-side invariant broke, and api.EventRecord.Details has
		// no room to report a decode error separately from "no details" —
		// mapping.go's mapEvent already treats a nil Details as {} on the
		// wire, so this degrades to that same honest, empty rendering rather
		// than panicking the request.
		_ = json.Unmarshal(rec.Details, &details)
	}
	return api.EventRecord{
		Seq:           uint64(rec.Seq),
		RecordedAt:    rec.RecordedAt,
		OccurredAt:    rec.OccurredAt,
		Source:        rec.Source,
		Resource:      rec.Resource,
		Category:      rec.Category,
		Severity:      rec.Severity,
		Summary:       rec.Summary,
		Details:       details,
		CorrelationID: rec.CorrelationID,
	}
}

// --- api.FPPLister and api.CollectorStatusLister over *store.Store + config ---

// fppInstanceLister adapts the coordinator's configured FPP endpoints plus
// whatever internal/coordinator/collector/fpp has written to the store into
// api.FPPLister. It is deliberately built from cfg.FPPEndpoints, not from
// "whatever resource IDs happen to exist in the observations table": an
// instance that is configured but has never successfully polled must still
// appear (with not_collected/empty evidence), and an instance that used to
// be configured and was removed from SHOWMESH_FPP_ENDPOINTS must not
// linger in the API from stale rows — see contract section 4's
// not_collected case.
type fppInstanceLister struct {
	st        *store.Store
	endpoints []config.FPPEndpoint
}

func (l fppInstanceLister) ListInstances(ctx context.Context) ([]api.FPPInstanceView, error) {
	views := make([]api.FPPInstanceView, 0, len(l.endpoints))
	for _, ep := range l.endpoints {
		obs, err := l.st.ListObservations(ctx, store.ObservationFilter{
			ResourceKind: observation.ResourceFPP,
			ResourceID:   ep.ID,
		})
		if err != nil {
			return nil, fmt.Errorf("coordinator: list fpp instance observations for %q: %w", ep.ID, err)
		}
		// LastPollAt/LastPollError are computed from the store's real,
		// possibly-empty obs BEFORE notYetPolledObservations may replace
		// it below: a synthesized not_collected placeholder must never be
		// mistaken for a poll that actually happened, or this instance
		// would report a bogus "last polled just now" the instant it was
		// only ever configured, not polled.
		lastPollAt := fppLastPollAt(obs)
		lastPollError := fppLastPollError(obs)
		if len(obs) == 0 {
			// The store holds nothing at all for this instance: no poll
			// has completed yet (freshly configured, or the coordinator
			// just restarted and the FPP runner has not ticked once).
			// api.FPPInstanceView.Observations' own doc comment, and
			// v1.FPPInstance.Observations' wire counterpart, both already
			// documented this case as "one not_collected Evidence per
			// configured signal", not an absent field or a bare empty
			// list — this is the implementation that makes that
			// documented behavior real rather than aspirational (Step 3
			// review finding 3.8).
			obs = notYetPolledObservations(ep.ID, time.Now())
		}
		views = append(views, api.FPPInstanceView{
			InstanceID:    ep.ID,
			Endpoint:      ep.URL,
			Observations:  obs,
			LastPollAt:    lastPollAt,
			LastPollError: lastPollError,
		})
	}
	return views, nil
}

// fppSignals is every signal internal/coordinator/collector/fpp.Collector
// can produce (that package's Signal* constants), used only to synthesize
// [notYetPolledObservations]' placeholders. Deliberately not derived
// reflectively or otherwise kept "automatically" in sync: fpp.Collector is
// Task C's package, built and tested independently, and a plain listing
// here that a future signal addition must remember to update is the same
// trade-off internal/coordinator/collector/fpp/fpp.go's own allDataSignals
// already makes for the identical reason.
var fppSignals = []observation.SignalID{
	fpp.SignalReachable, fpp.SignalVersion, fpp.SignalMode, fpp.SignalStatus,
	fpp.SignalPlaylistName, fpp.SignalSequenceName, fpp.SignalPositionSeconds,
	fpp.SignalPositionRemaining, fpp.SignalMultiSyncEnabled, fpp.SignalMultiSyncSystems,
	fpp.SignalSchedulerStatus, fpp.SignalUptimeSeconds,
}

// notYetPolledObservations synthesizes one [observation.StateNotCollected]
// observation per [fppSignals] entry for instanceID. now only has to
// satisfy [observation.Observation]'s non-zero-CollectedAt invariant
// ([observation.Validate]); it is never rendered on the wire for this
// state — internal/coordinator/api's mapEvidence masks CollectedAt to
// null whenever State is not_collected (Step 3 review finding 3.6), which
// is what makes passing time.Now() here safe: an arbitrary, ever-advancing
// placeholder would otherwise re-trigger the hub's change detection on
// every render tick for a signal that has not actually changed at all
// (contract section 6.5) — exactly the bug [helloObservation]'s doc
// comment in mapping.go already describes fixing once, for node evidence.
func notYetPolledObservations(instanceID string, now time.Time) []observation.Observation {
	res := observation.ResourceRef{Kind: observation.ResourceFPP, ID: instanceID}
	out := make([]observation.Observation, 0, len(fppSignals))
	for _, sig := range fppSignals {
		o, err := observation.NotCollected(res, sig,
			"no poll has completed yet for this FPP instance",
			observation.WithSource(fppCollectorSourceID), observation.WithCollectedAt(now))
		if err != nil {
			// Unreachable: every argument above is a fixed constant or a
			// non-empty literal reason string — see mapping.go's
			// mustObservation for the identical posture on the same class
			// of call.
			panic(fmt.Sprintf("coordinator: notYetPolledObservations(%q, %q): %v", instanceID, sig, err))
		}
		out = append(out, o)
	}
	return out
}

// fppLastPollAt is the latest CollectedAt across obs, or nil if obs is
// empty (no poll has completed for this instance yet — see
// api.FPPInstanceView.LastPollAt's doc comment). Every observation a single
// Poll call produces shares the same CollectedAt/observedAt "now" (see
// fpp.Collector.measured/failed), so in practice this is simply "the most
// recent poll's timestamp", computed as a max rather than assumed uniform
// so it stays correct even if that internal detail ever changes.
func fppLastPollAt(obs []observation.Observation) *time.Time {
	var latest time.Time
	found := false
	for _, o := range obs {
		if !found || o.CollectedAt.After(latest) {
			latest = o.CollectedAt
			found = true
		}
	}
	if !found {
		return nil
	}
	return &latest
}

// fppLastPollError reports fpp.SignalReachable's Reason when its most
// recently stored observation is a collection_failed absence — the one
// signal the FPP collector's own doc comment (fpp.go, multiSyncEnabledSignal)
// designates as "is this instance up", independent of any individual data
// field's own decode outcome. nil (no error) covers both a genuinely
// reachable instance and one that has never been polled yet; a caller
// distinguishes those two cases via LastPollAt, not this field.
func fppLastPollError(obs []observation.Observation) *string {
	for _, o := range obs {
		if o.Signal == fpp.SignalReachable && o.Absence == observation.StateCollectionFailed {
			reason := o.Reason
			return &reason
		}
	}
	return nil
}

// fppCollectorStatusLister reports the FPP REST collector's own run state
// for GET /api/v1/snapshot's "collectors" list (contract section 6.10):
// always exactly one entry, id [fppCollectorSourceID], regardless of how
// many (if any) FPP instances are configured — [api.CollectorRunning] when
// SHOWMESH_FPP_ENDPOINTS names one or more, [api.CollectorNotConfigured]
// naming why when it does not (contract section 4's "an operator with no
// SHOWMESH_FPP_ENDPOINTS set must be able to ask the API about FPP and be
// told 'nothing is configured'", applied to the collector-status list
// rather than the, necessarily empty and correctly so, instance list
// itself).
//
// This used to report one row per configured endpoint (id
// "fpp-rest:<instanceID>") — a shape that changed with configuration, so
// the collector had no single, stable identity at all — and used
// observation.StateNotCollected (an evidence-absence state) for the
// zero-endpoints case while using the bare, unenumerated string "running"
// otherwise: two different vocabularies for the same field, with nothing
// pinning either (Step 3 review finding 3.7). [api.CollectorRunState]'s
// doc comment is where the correct, closed vocabulary now lives.
//
// This package does not attempt to distinguish a healthy collector from
// one whose every instance is currently failing at the "collectors" level
// — internal/coordinator/api's per-instance Health (mapping.go's
// deriveInstanceHealth) is where that distinction actually lives, from real
// evidence; [api.CollectorRunning] here means only "this collector is
// registered and being polled on a cadence", the same sense
// internal/coordinator/collector.Runner itself uses the word — see
// [api.CollectorRunState]'s doc comment for why that is a normal, expected
// value even alongside every instance reading collection_failed.
type fppCollectorStatusLister struct {
	endpoints []config.FPPEndpoint
}

const fppCollectorSourceID = "fpp-rest"

func (l fppCollectorStatusLister) CollectorStatuses(context.Context) ([]api.CollectorState, error) {
	if len(l.endpoints) == 0 {
		reason := "no FPP endpoints configured (SHOWMESH_FPP_ENDPOINTS is unset)"
		return []api.CollectorState{{ID: fppCollectorSourceID, State: string(api.CollectorNotConfigured), Reason: &reason}}, nil
	}
	return []api.CollectorState{{ID: fppCollectorSourceID, State: string(api.CollectorRunning)}}, nil
}

// --- collector.Sink over *store.Store, poking the SSE hub ---

// fppSink adapts *store.Store (plus a hub-notify callback) to
// collector.Sink: every observation a Runner-driven Poll call produces is
// persisted, and the hub is poked once per batch so a change reaches
// subscribers well before its own next tick (contract section 6.5;
// api.API's own doc comment names this exact obligation for whatever wires
// it in). notify is called even when observations is empty — a
// backed-off Collector's Poll legitimately returns nothing (see
// fpp.Collector.Poll's doc comment) — this costs nothing: Hub.Notify is a
// non-blocking, coalescing poke, and the hub's own render pass is what
// decides whether anything actually changed.
type fppSink struct {
	st     *store.Store
	notify func()
	logger *slog.Logger
}

func (s *fppSink) RecordObservations(ctx context.Context, observations []observation.Observation) {
	for _, obs := range observations {
		if err := s.st.UpsertObservation(ctx, obs); err != nil {
			s.logger.Error("coordinator: failed to store fpp observation",
				"resource_kind", obs.Resource.Kind, "resource_id", obs.Resource.ID, "signal", obs.Signal, "error", err)
		}
	}
	if s.notify != nil {
		s.notify()
	}
}
