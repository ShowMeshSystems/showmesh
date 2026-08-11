// Package api is the coordinator's versioned public control API and
// Server-Sent Events change stream, per ADR-014 and the Step 3 shared
// design contract section 6. It serves everything under /api/v1; the
// container healthcheck endpoints (/healthz, /readyz, /version) are
// internal/coordinator/httpapi's and stay outside the versioned contract
// (contract section 6.1).
//
// This package is read-only, by construction and by review: it issues no
// MQTT publish, mutates no FPP instance, and defines no PUT/POST/DELETE
// route. Step 3 is read-only observability; a write endpoint belongs to a
// later step with its own ADR, not to a quiet addition here.
//
// # Boundaries this package holds
//
//   - Wire types live in the v1 subpackage and are the contract; domain
//     types (pkg/observation.Observation,
//     internal/coordinator/inventory.NodeView, the coordinator's store
//     records) are mapped into them explicitly in mapping.go. See v1's
//     package doc comment for why that mapping layer exists rather than
//     JSON tags on the domain structs themselves.
//   - This package declares the small consumer-side interfaces it needs
//     from the store and the FPP collector — [NodeLister], [FPPLister],
//     [ObservationLister], [EventReader], [CollectorStatusLister] — in
//     interfaces.go, rather than importing
//     internal/coordinator/store or internal/coordinator/collector
//     directly. Those packages are being built in parallel by other Step 3
//     tasks; declaring the interface at the consumer means this package
//     does not import a type that does not exist yet, and does not need to
//     change when the producer's internal representation does, as long as
//     the shape declared here keeps being satisfiable. Wiring the real
//     implementations in is a later task's job (see [New]'s doc comment).
//   - This package does not wire itself into coordinator.Run. [New] builds
//     an [API] value — an http.Handler plus a [Hub] whose Run method must
//     be started by the caller — for the wiring task to mount and start.
//     Nothing in this package assumes it owns the process lifecycle.
//
// # What Step 3 deliberately does not add here
//
// No desired state, no assignments, no reconciliation status (contract
// section 6.7 — those do not exist in the coordinator yet, and a
// placeholder field that no code computes would be read by an operator as
// a verdict). No metric history (observations are latest-only; see
// pkg/observation and OBSERVABILITY's retention note). No alert model.
// No write operations of any kind, including through the SSE stream, which
// is strictly server-to-client (contract section 6.4).
package api
