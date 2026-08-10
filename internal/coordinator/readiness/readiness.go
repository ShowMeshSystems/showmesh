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
