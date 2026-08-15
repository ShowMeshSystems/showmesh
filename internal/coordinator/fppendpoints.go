package coordinator

import (
	"context"
	"log/slog"
	"sync"
	"time"

	"github.com/showmeshsystems/showmesh/internal/coordinator/collector"
	"github.com/showmeshsystems/showmesh/internal/coordinator/collector/fpp"
	"github.com/showmeshsystems/showmesh/internal/coordinator/config"
	"github.com/showmeshsystems/showmesh/internal/coordinator/store"
)

// This file makes the FPP endpoint list LIVE rather than a startup
// snapshot, added 2026-08-14 after the Step 9 wave 3 acceptance run
// measured what the snapshot cost.
//
// What was measured. STEP-9-SPEC.md section 5.6 states that removing an
// endpoint while a run is in flight resolves the affected step `failed`
// with a reason naming the instance, and acceptance criterion 20 requires
// it. What actually happened: the PUT returned 200, and the in-flight
// run's next FPP step dispatched to the removed endpoint and CONFIRMED.
// The list every dispatch resolved against was captured once, at process
// start, into a plain []config.FPPEndpoint field. Only a restart moved it.
//
// The response did say so — it carried restartRequired: true with a
// sentence explaining that the collector polls the list it read at
// startup. The owner's decision, 2026-08-14: "we can't have that kind of
// failure, it shouldn't blindly be sending commands and only having the
// info needed to fix the problem in the response or log. So how about we
// follow the spec and make it work immediately."
//
// Both halves have to move together, and the reason is not symmetry. Live
// resolution ALONE fixes removal and breaks addition: an endpoint added
// through the API would immediately be dispatchable while no collector was
// polling it, so every command against it would dispatch and then fail to
// confirm, which is a worse failure than the one being fixed because it
// looks like broken hardware. So [fppEndpointSource] makes the reads live
// and [reconcileFPPCollectors] makes the poll set follow, and neither is
// correct without the other.

// fppEndpointProvider is what the API adapters actually need from the
// endpoint list: the current one, on demand. Declared at the consumer
// rather than at the producer, matching this package's own convention, so
// a test can supply a fixed list without a store and without pretending
// its cache is warm.
type fppEndpointProvider interface {
	Current(ctx context.Context) []config.FPPEndpoint
}

// fppEndpointSource resolves the currently active fpp.endpoints
// configuration on demand, caching the decoded list against the revision
// number it came from so the common case is one indexed row read rather
// than a decode.
//
// Every reader that used to hold a []config.FPPEndpoint captured at
// startup now holds one of these instead. There is deliberately no
// "refresh" method and no invalidation call: a caller cannot forget to
// invalidate something it never has to, and the config_objects row is the
// single source of truth for which revision is active.
type fppEndpointSource struct {
	st     *store.Store
	logger *slog.Logger

	mu       sync.Mutex
	revision int64
	cached   []config.FPPEndpoint
	loaded   bool
}

var _ fppEndpointProvider = (*fppEndpointSource)(nil)

func newFPPEndpointSource(st *store.Store, logger *slog.Logger) *fppEndpointSource {
	return &fppEndpointSource{st: st, logger: logger}
}

// Current returns the active endpoint list.
//
// On any store error it returns the last list it successfully read, and
// logs. That is deliberate and it is the ADR-011 posture rather than
// laziness: a transient read failure must not silently empty the fleet,
// because an empty list is indistinguishable, to every caller downstream,
// from an operator having deliberately removed every endpoint. Returning
// stale-but-real configuration and saying so in the log is the honest
// degradation; manufacturing an empty one is not.
//
// Before the first successful read there is nothing to fall back to, and
// an empty list is then the truthful answer.
func (s *fppEndpointSource) Current(ctx context.Context) []config.FPPEndpoint {
	s.mu.Lock()
	defer s.mu.Unlock()

	obj, err := s.st.GetConfigObject(ctx, config.FPPEndpointsConfigKind, config.FPPEndpointsConfigObjectID)
	if err != nil {
		if !s.loaded {
			// No active fpp.endpoints object at all is a normal state (a
			// coordinator that has never had one configured), not an
			// error worth logging on every read.
			return nil
		}
		s.logWarn("failed to read the active fpp.endpoints configuration; continuing with the last known list", err)
		return s.cached
	}
	if s.loaded && obj.CurrentRevision == s.revision {
		return s.cached
	}

	rev, err := s.st.GetConfigRevision(ctx, config.FPPEndpointsConfigKind, config.FPPEndpointsConfigObjectID, obj.CurrentRevision)
	if err != nil {
		s.logWarn("failed to read the active fpp.endpoints revision; continuing with the last known list", err)
		return s.cached
	}
	endpoints, err := config.DecodeFPPEndpointsPayload(rev.PayloadJSON)
	if err != nil {
		s.logWarn("failed to decode the active fpp.endpoints revision; continuing with the last known list", err)
		return s.cached
	}

	s.revision = obj.CurrentRevision
	s.cached = endpoints
	s.loaded = true
	return endpoints
}

func (s *fppEndpointSource) logWarn(msg string, err error) {
	if s.logger != nil {
		s.logger.Warn(msg, "error", err)
	}
}

// fppCollectorReconcileInterval is how often [reconcileFPPCollectors]
// compares the running poll set against the configured one.
//
// Ten seconds, and the number is chosen against a deadline rather than
// picked for feeling small: a command's confirmation deadline is 20
// seconds, so a newly added endpoint is being polled well inside the
// window of the first command an operator could plausibly send to it after
// adding it. Removal does not depend on this interval at all — the
// dispatch path resolves the endpoint list live, so a removed endpoint
// stops receiving commands the moment the revision is active, and this
// loop only stops the now-pointless polling afterwards.
const fppCollectorReconcileInterval = 10 * time.Second

// reconcileFPPCollectors keeps the FPP REST collector set matching the
// active fpp.endpoints configuration until ctx is cancelled.
//
// Add and Remove are both idempotent ([collector.Runner.Add] ignores an id
// it already has), so this compares by id and does the minimum: a
// collector for an unchanged endpoint is never restarted, and its poll
// cadence and its evidence are undisturbed by an unrelated endpoint being
// added or removed alongside it.
//
// A CHANGED URL for an existing id is treated as remove-then-add rather
// than as no change, because "same id, different host" is exactly the
// case an id-only comparison would sail past while every subsequent poll
// went to the old host.
func reconcileFPPCollectors(
	ctx context.Context,
	runner *collector.Runner,
	source fppEndpointProvider,
	newCollector func(id, url string) (collector.Collector, error),
	logger *slog.Logger,
) {
	live := map[string]string{} // id -> url currently being polled

	// Seed from what the caller already registered before Run started, so
	// the first pass does not tear down and rebuild the whole fleet.
	for _, ep := range source.Current(ctx) {
		live[ep.ID] = ep.URL
	}

	ticker := time.NewTicker(fppCollectorReconcileInterval)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}

		desired := map[string]string{}
		for _, ep := range source.Current(ctx) {
			desired[ep.ID] = ep.URL
		}

		for id, url := range live {
			want, still := desired[id]
			if still && want == url {
				continue
			}
			if runner.Remove(id) {
				if still {
					logger.Info("fpp endpoint url changed; restarting its collector", "instance_id", id, "old_url", url, "new_url", want)
				} else {
					logger.Info("fpp endpoint removed from configuration; stopping its collector", "instance_id", id)
				}
			}
			delete(live, id)
		}

		for id, url := range desired {
			if _, running := live[id]; running {
				continue
			}
			c, err := newCollector(id, url)
			if err != nil {
				// Shape is validated at write time (config.ValidateFPPEndpoints
				// runs before any revision is created), so reaching here means
				// those two checks have drifted apart. Log and skip this one
				// endpoint rather than killing the loop: the rest of the fleet
				// keeps being polled.
				logger.Error("failed to construct a collector for a configured fpp endpoint", "instance_id", id, "error", err)
				continue
			}
			runner.Add(c, fpp.DefaultPollInterval)
			live[id] = url
			logger.Info("fpp endpoint added to configuration; started its collector", "instance_id", id)
		}
	}
}
