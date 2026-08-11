// Package readiness holds the coordinator's readiness domain types:
// Report, one dependency's contribution to /readyz, and Source, the
// interface a dependency implements to contribute one. It is a coordinator
// domain concept, not an HTTP concept, so it lives here rather than in
// httpapi, and is neutral so that both a dependency (e.g. broker) and the
// HTTP layer (httpapi) can depend on it without either depending on the
// other.
//
// Per ADR-011, stale or insufficient evidence is unknown, and unknown is
// never reported as healthy: a Source's Report must reflect that, not
// paper over missing or aged-out confirmation. BrokerManager.Readiness is
// today's only Source and is the local minimum that keeps that precedent
// correct; Step 3 replaces it with the canonical observation model from
// OBSERVABILITY section 4.1, once the SQLite store and the FPP collectors
// also need to contribute to readiness.
package readiness

import "time"

// Report is one dependency's contribution to /readyz.
type Report struct {
	Ready  bool
	Reason string // empty when Ready

	// ObservedAt is when this Report's evidence was last confirmed. Per
	// ADR-011, freshness must be structurally guaranteed, not a convention
	// each Source has to remember to build into Details by hand: the HTTP
	// layer (httpapi) derives the not-ready response's observedAgeSecs from
	// this field, so a Source with real evidence must always set it. A
	// Source with no evidence at all (e.g. no dependency configured) may
	// leave it zero; the HTTP layer omits observedAgeSecs rather than
	// fabricate a freshness claim.
	ObservedAt time.Time

	Details map[string]any // merged into the not-ready response body
}

// Source is consulted on every /readyz request. Implementations must be
// safe for concurrent use.
type Source interface {
	Readiness() Report
}

// Aggregate combines several Sources into one, itself satisfying Source.
// httpapi.NewServer still takes a single readiness.Source; this is how
// Step 2 round 2 adds a second contributor (the SQLite store, alongside
// the existing broker connection) without changing that signature or the
// shape of the /readyz response body — see coordinator.Run, the only
// caller.
//
// Readiness reports not-ready as soon as any member is not-ready, using
// that member's Report verbatim (Reason, Details, and ObservedAt): per
// ADR-011 a single confident verdict is wanted, not an averaged or merged
// one that could paper over which dependency actually failed. When every
// member is ready, the aggregate is ready, and ObservedAt is the oldest
// (least fresh) ObservedAt among the members — the aggregate's freshness is
// only as good as its weakest contributor.
//
// A ready member with a zero ObservedAt (no freshness evidence at all —
// see Report.ObservedAt's doc comment) IS the weakest possible contributor,
// not one to skip when computing the oldest: a zero time.Time already
// chronologically precedes every real observation, so it naturally wins
// the "oldest" comparison below and the aggregate's own ObservedAt comes
// out zero too, propagating "no freshness evidence" up rather than
// reporting the freshness of some other, better-evidenced member as if it
// bounded the whole aggregate. An earlier version of this method special-
// cased zero ObservedAt values out of the comparison, which produced
// exactly the contradiction this doc comment used to warn against without
// fixing: a member with unknown freshness made the aggregate look no worse
// than its best-evidenced member, the opposite of "only as good as its
// weakest contributor."
type Aggregate []Source

// Readiness implements Source.
func (a Aggregate) Readiness() Report {
	reports := make([]Report, len(a))
	for i, s := range a {
		reports[i] = s.Readiness()
	}

	for _, r := range reports {
		if !r.Ready {
			return r
		}
	}

	var oldest time.Time
	for i, r := range reports {
		if i == 0 || r.ObservedAt.Before(oldest) {
			oldest = r.ObservedAt
		}
	}
	return Report{Ready: true, ObservedAt: oldest}
}
