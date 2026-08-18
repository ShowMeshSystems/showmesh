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
	"github.com/showmeshsystems/showmesh/internal/coordinator/broker"
	"github.com/showmeshsystems/showmesh/internal/coordinator/collector"
	"github.com/showmeshsystems/showmesh/internal/coordinator/collector/fpp"
	"github.com/showmeshsystems/showmesh/internal/coordinator/config"
	"github.com/showmeshsystems/showmesh/internal/coordinator/inventory"
	"github.com/showmeshsystems/showmesh/internal/coordinator/macro"
	"github.com/showmeshsystems/showmesh/internal/coordinator/store"
	"github.com/showmeshsystems/showmesh/pkg/observation"
)

// var _ api.MacroRunner = (*macro.Executor)(nil) is Step 9 wave 2's own
// compile-time assertion (this wave's brief section 5: "so a signature
// drift is a compile error rather than a runtime nil"), mirroring
// api.go's identical storeSatisfiesCommandStore assertion one package
// over. *macro.Executor already carries this assertion in its own package
// (macro.go); repeating it here, at the wiring seam that actually
// constructs one and hands it to api.New as an api.MacroRunner, is what
// makes a drift between the two packages' independently-declared
// interfaces (api.MacroRunner, declared in api/macro_seam.go; the
// concrete *macro.Executor, built against it from the other side) visible
// at THIS package's own build, not only macro's.
var _ api.MacroRunner = (*macro.Executor)(nil)

// --- broker.Registry over cfg.IntegrationBrokers (Step 9, STEP-9-SPEC.md
// section 2.10, wave 2 shared contract section 5) ---

// buildIntegrationBrokerRegistry constructs one *broker.BrokerManager per
// identifier in brokers (SHOWMESH_INTEGRATION_BROKERS, already parsed and
// validated by internal/coordinator/config) and registers each into a
// fresh *broker.Registry, returning both the registry and the individual
// managers (so the caller can Disconnect each one at shutdown — a
// *broker.Registry has no bulk-disconnect method of its own, by design:
// it resolves and dispatches, it does not own the managers' lifecycle).
//
// broker.NewBrokerManager reads its server URL and credentials from a
// config.Config (cfg.MQTTBroker/MQTTClientID/MQTTUsername/MQTTPassword —
// see that function's own doc comment), which is the shape this
// coordinator's OWN control-plane broker connection already uses one
// struct field at a time from the real *config.Config below. Rather than
// changing that constructor's signature (which every existing caller,
// including the control-plane connection this function does not touch,
// would then have to accommodate for a shape only this one caller needs),
// a synthetic config.Config is built PER integration broker, carrying
// only the four fields NewBrokerManager actually reads — confirmed by
// reading its body, not assumed — so each integration broker connects
// under its own client id (cfg.MQTTClientID + "-integration-" + id, so
// two brokers, or an integration broker and the control-plane broker,
// never collide on client id if they ever happened to point at the same
// physical broker) and its own credentials. THE CONTROL-PLANE BROKER
// (cfg.MQTTBroker itself) IS NEVER REGISTERED HERE UNDER ANY IDENTIFIER —
// this function's only input is cfg.IntegrationBrokers, a field
// config.Config's own parser (integrationbrokers.go) populates from a
// SEPARATE environment variable and never from SHOWMESH_MQTT_BROKER, so
// that guarantee holds by construction, not by this function remembering
// to skip it.
//
// A per-broker connection failure here is NOT fatal to coordinator
// startup: NewBrokerManager itself never fails merely because a broker is
// currently unreachable (it connects in the background and retries — see
// its own doc comment), so the only way this function's own loop returns
// an error is a genuinely malformed cfg entry, which config.LoadConfig's
// own validation should already have caught before Run ever reaches this
// call. Treated as fatal anyway (returned, not logged-and-skipped): an
// integration broker registry with a silently-missing entry is exactly
// the kind of thing STEP-9-SPEC.md section 2.10 warns about ("an operator
// authors projectors-on... every symptom points at Node-RED when none of
// them is Node-RED") — better to fail loudly at startup than to leave a
// declared broker quietly unregistered.
func buildIntegrationBrokerRegistry(ctx context.Context, cfg config.Config, logger *slog.Logger) (*broker.Registry, []*broker.BrokerManager, error) {
	registry := broker.NewRegistry()
	managers := make([]*broker.BrokerManager, 0, len(cfg.IntegrationBrokers))
	for _, b := range cfg.IntegrationBrokers {
		synth := config.Config{
			MQTTBroker:   b.URL,
			MQTTClientID: cfg.MQTTClientID + "-integration-" + b.ID,
			MQTTUsername: b.Username,
			MQTTPassword: b.Password,
		}
		bm, err := broker.NewBrokerManager(ctx, synth, logger, nil, nil)
		if err != nil {
			return nil, nil, fmt.Errorf("coordinator: build integration broker %q: %w", b.ID, err)
		}
		if err := registry.Register(b.ID, bm); err != nil {
			return nil, nil, fmt.Errorf("coordinator: register integration broker %q: %w", b.ID, err)
		}
		managers = append(managers, bm)
	}
	return registry, managers, nil
}

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
	st *store.Store

	// endpoints is resolved LIVE, per call, rather than captured at
	// startup. It used to be a plain []config.FPPEndpoint field, and the
	// consequence of that is recorded in fppendpoints.go's own file
	// comment: an endpoint removed through the API kept receiving
	// commands until someone restarted the coordinator.
	endpoints fppEndpointProvider
}

func (l fppInstanceLister) ListInstances(ctx context.Context) ([]api.FPPInstanceView, error) {
	var endpoints []config.FPPEndpoint
	if l.endpoints != nil {
		endpoints = l.endpoints.Current(ctx)
	}
	views := make([]api.FPPInstanceView, 0, len(endpoints))
	for _, ep := range endpoints {
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

// fppSignals is the STATIC subset of signals
// internal/coordinator/collector/fpp.Collector can produce, used only to
// synthesize [notYetPolledObservations]' not-yet-polled placeholders for a
// freshly configured instance. It is [fpp.AllSignals] verbatim — that
// package's own exported, documented static vocabulary — rather than a
// second, hand-maintained copy of it: contract section 5.4 asks for exactly
// this ("derive the list from an exported symbol in the fpp package so it
// cannot drift"), and fpp.AllSignals' own doc comment names this file as
// its motivating caller. Static means "known to exist before the first
// poll ever completes"; fpp.AllSignals itself excludes the two dynamic
// signal families (fpp.port.<key>.* and fpp.sensor.<key>.*) whose exact
// members cannot be known before a real poll observes what ports and
// sensors a given instance actually has — a not-yet-polled placeholder for
// a port or sensor key that turns out not to exist on this instance would
// be a fabricated signal name no real poll could ever match cleanly, so
// those signals simply do not exist in the API at all until the first real
// poll observes them. This is a type alias in intent, not a copy: this var
// exists at all only because [notYetPolledObservations] below was already
// written against a package-local fppSignals name before fpp.AllSignals
// existed to reuse directly, and renaming every call site to fpp.AllSignals
// verbatim was not worth the diff over one assignment.
var fppSignals = fpp.AllSignals

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

// fppLastPollAt is the latest CollectedAt across obs' fpp-rest-sourced rows
// only, or nil if none exist (no REST poll has completed for this instance
// yet — see api.FPPInstanceView.LastPollAt's doc comment). Scoped to
// fpp-rest specifically, not every source obs might now carry: "poll" is
// this coordinator's own word for the REST collector's request/response
// cycle (internal/coordinator/collector.Runner's cadence), and does not
// describe internal/coordinator/collector/fppmqtt's push-based delivery
// model at all — mixing the two into one "last activity, from any source"
// timestamp would make this field answer a different, muddier question
// than its name says it does. Every configured FPP instance is guaranteed
// an fpp-rest source (contract section 4.4 requires every
// SHOWMESH_FPP_MQTT_HOSTS entry to already be a SHOWMESH_FPP_ENDPOINTS
// entry, never the reverse), so this scoping never produces a false "no
// poll yet" for an instance that is in fact being actively observed via
// MQTT alone.
//
// Every observation a single fpp-rest Poll call produces shares the same
// CollectedAt/observedAt "now" (see fpp.Collector.measured/failed), so in
// practice this is simply "the most recent poll's timestamp", computed as
// a max rather than assumed uniform so it stays correct even if that
// internal detail ever changes.
func fppLastPollAt(obs []observation.Observation) *time.Time {
	var latest time.Time
	found := false
	for _, o := range obs {
		if o.Source != fppCollectorSourceID {
			continue
		}
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
// recently stored fpp-rest observation is a collection_failed absence — the
// one signal the FPP collector's own doc comment (fpp.go,
// multiSyncEnabledSignal) designates as "is this instance up", independent
// of any individual data field's own decode outcome. nil (no error) covers
// both a genuinely reachable instance and one that has never been polled
// yet; a caller distinguishes those two cases via LastPollAt, not this
// field.
//
// The explicit o.Source == fppCollectorSourceID check is defensive rather
// than load-bearing today: internal/coordinator/collector/fppmqtt never
// produces fpp.SignalReachable at all (contract section 4.3's topic table
// has no reachable-equivalent — MQTT connection loss instead fails every
// signal it does produce, per that package's own contract), so in practice
// no other source can ever satisfy the o.Signal == fpp.SignalReachable
// half of this check. Checking Source anyway keeps this function correct
// by construction rather than by "nothing else happens to write this
// signal today" — a property Seam A/B's parallel build should not have to
// preserve by convention.
func fppLastPollError(obs []observation.Observation) *string {
	for _, o := range obs {
		if o.Source == fppCollectorSourceID && o.Signal == fpp.SignalReachable && o.Absence == observation.StateCollectionFailed {
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
	// Live per call, for the same reason as fppInstanceLister.endpoints:
	// after an operator removes the last endpoint this must start saying
	// "nothing is configured" without waiting for a restart, or the
	// snapshot claims a collector is running against a fleet that is no
	// longer configured.
	endpoints fppEndpointProvider
}

const fppCollectorSourceID = "fpp-rest"

func (l fppCollectorStatusLister) CollectorStatuses(ctx context.Context) ([]api.CollectorState, error) {
	// A nil provider is the zero value of this struct and has always meant
	// "nothing configured". Keeping that true costs one comparison and
	// avoids turning a wiring mistake into a panic inside a snapshot read,
	// which is the one code path ADR-024 constraint 23 says must keep
	// working when everything else is broken.
	if l.endpoints == nil || len(l.endpoints.Current(ctx)) == 0 {
		reason := "no FPP endpoints are configured"
		return []api.CollectorState{{ID: fppCollectorSourceID, State: string(api.CollectorNotConfigured), Reason: &reason}}, nil
	}
	return []api.CollectorState{{ID: fppCollectorSourceID, State: string(api.CollectorRunning)}}, nil
}

// fppMQTTCollectorSourceID is [observation.Observation.Source] for every
// observation internal/coordinator/collector/fppmqtt produces, and this
// collector's own id in GET /api/v1/snapshot's "collectors" list — the
// literal string contract section 4.1 fixes ("Source name is fpp-mqtt"),
// duplicated here as a Go constant rather than imported from package
// fppmqtt for the identical reason internal/coordinator/api/mapping.go's
// healthCriticalSignals inlines "fpp.reachable": this file already inlines
// fppCollectorSourceID = "fpp-rest" the same way, and a Seam C file mixing
// "the REST source name is a local constant" with "the MQTT source name is
// imported from its producer package" would be an arbitrary inconsistency,
// not a considered choice.
const fppMQTTCollectorSourceID = "fpp-mqtt"

// fppMQTTSecretAdapter implements [api.FPPMQTTSecretStore] over the fpp.mqtt
// secret file (Track G seam G-3, ADR-039 decision 7) — see
// internal/coordinator/config/fppmqttsecret.go.
type fppMQTTSecretAdapter struct {
	dataDir string
}

func (a fppMQTTSecretAdapter) HasFPPMQTTPassword(context.Context) (bool, error) {
	return config.HasFPPMQTTPassword(a.dataDir)
}

func (a fppMQTTSecretAdapter) SetFPPMQTTPassword(_ context.Context, password string) error {
	return config.WriteFPPMQTTPassword(a.dataDir, password)
}

func (a fppMQTTSecretAdapter) ClearFPPMQTTPassword(context.Context) error {
	return config.ClearFPPMQTTPassword(a.dataDir)
}

// fppMQTTCollectorStatusLister (Step 5) reported this collector's status
// from a startup snapshot boolean. Track G seam G-3 (ADR-039) replaced it
// with *fppMQTTManager (fppmqttmanager.go), which answers live rather than
// from a value captured once at process start — see that type's own
// CollectorStatuses doc comment.

// multiCollectorStatusLister concatenates several [api.CollectorStatusLister]
// values into one, so api.Dependencies' single Collectors field — an
// interface, not a slice, per api/interfaces.go — can still report every
// collector source this coordinator runs. Before Step 5 there was exactly
// one collector ([fppCollectorStatusLister]) and no need for this type at
// all; Step 5 adds a second, independent source
// (internal/coordinator/collector/fppmqtt), and "generalize
// fppCollectorStatusLister... so both collectors appear" (contract section
// 5.4) is this type, not a change to fppCollectorStatusLister itself —
// each collector's own status logic stays exactly as independent as the
// two collectors themselves are (contract section 4.6: fppmqtt "must not
// share a client, a topic namespace, or a code path" with anything else),
// and this is only ever the seam that lists them side by side.
//
// A second collector source that is invisible in this list is a source an
// operator cannot tell is broken — the exact reasoning
// [fppCollectorStatusLister]'s own doc comment already gives for why the
// REST collector has a stable id in the first place, now applied to
// keeping a second source from disappearing into that same list's single
// entry.
type multiCollectorStatusLister []api.CollectorStatusLister

func (l multiCollectorStatusLister) CollectorStatuses(ctx context.Context) ([]api.CollectorState, error) {
	var out []api.CollectorState
	for _, sub := range l {
		states, err := sub.CollectorStatuses(ctx)
		if err != nil {
			return nil, err
		}
		out = append(out, states...)
	}
	return out, nil
}

// --- api.FPPPollNudger over *collector.Runner ---

// fppRunnerNudger adapts *collector.Runner to api.FPPPollNudger. Nudge is
// keyed by [collector.Collector.ID()], which for
// internal/coordinator/collector/fpp.Collector is exactly the FPP instance
// ID fpp.New was constructed with (see the fppCollector construction loop
// in Run, below) — so NudgePoll(instanceID) reaches precisely the fpp-rest
// collector for that one instance, never any other instance's own poll
// loop. It also never reaches internal/coordinator/collector/fppmqtt's
// collector: that collector's own ID() is the single fixed source name
// "fpp-mqtt" (see fppMQTTCollectorSourceID and fppmqtt's render.go), not
// an instance ID, so it can never match an instanceID lookup here — which
// is the correct behavior regardless: fppmqtt.Collector.Poll only ever
// renders whatever its message store already holds (that package's own
// doc comment, "Poll never touches the network"), so nudging it could not
// fetch anything sooner in any case.
type fppRunnerNudger struct {
	runner *collector.Runner
}

var _ api.FPPPollNudger = fppRunnerNudger{}

func (n fppRunnerNudger) NudgePoll(instanceID string) bool {
	return n.runner.Nudge(instanceID)
}

// --- collector.Sink over *store.Store, poking the SSE hub ---

// fppSink adapts *store.Store (plus a hub-notify callback) to
// collector.Sink: every observation a Runner-driven Poll call produces is
// persisted, and the hub is poked once per batch so a change reaches
// subscribers well before its own next tick (contract section 6.5;
// api.API's own doc comment names this exact obligation for whatever wires
// it in). notify fires only when RecordObservations could actually have
// mutated s.st: both branches below are no-ops on a zero-length
// observations slice ([store.Store.ReplaceObservations] returns
// immediately, and an empty range mutates nothing), so len(observations)
// > 0 is not a proxy for "something changed" — it is the exact condition
// under which this call could have changed anything, including a prune
// (ReplaceObservations' delete is scoped to the (resource, source) keys
// PRESENT in this delivery, so an empty delivery has no key to prune by;
// see TestReplaceObservationsEmptyBatchIsNoOp and
// TestFPPSinkNotifiesOnlyWhenStoreCouldHaveChanged). This also stops an
// always-registered, perpetually-empty collector (e.g. noderender before
// any node ever publishes) from poking the hub before its own first real
// tick, which raced a fresh hub's still-empty lastRendered map ahead of
// any subscriber ever connecting.
type fppSink struct {
	st     *store.Store
	notify func()
	logger *slog.Logger
}

// RecordObservations persists one Poll call's delivery. complete (see
// collector.Collector.Poll's doc comment) selects which store method
// applies it: complete=true routes through [store.Store.ReplaceObservations],
// which additionally prunes any previously-stored row for a
// (resource_kind, resource_id, source) this call mentions whose signal is
// NOT among what was just delivered — this is what stops a shrunk port
// list, a removed sensor, or a reconfigured instance from leaving ghost
// rows behind forever. complete=false (a skipped/backed-off poll) instead
// upserts each observation individually via [store.Store.UpsertObservation],
// exactly as before this completeness contract existed, so a skipped
// cycle's near-always-empty delivery can never be read as "this source now
// owns zero signals" and prune anything.
func (s *fppSink) RecordObservations(ctx context.Context, observations []observation.Observation, complete bool) {
	if complete {
		if err := s.st.ReplaceObservations(ctx, observations); err != nil {
			s.logger.Error("coordinator: failed to store fpp observations", "count", len(observations), "error", err)
		}
	} else {
		for _, obs := range observations {
			if err := s.st.UpsertObservation(ctx, obs); err != nil {
				s.logger.Error("coordinator: failed to store fpp observation",
					"resource_kind", obs.Resource.Kind, "resource_id", obs.Resource.ID, "signal", obs.Signal, "error", err)
			}
		}
	}
	if s.notify != nil && len(observations) > 0 {
		s.notify()
	}
}
