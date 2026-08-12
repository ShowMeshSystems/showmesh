// Package api is the coordinator's versioned public control API and
// Server-Sent Events change stream, per ADR-014 and the Step 3 shared
// design contract section 6. It serves everything under /api/v1; the
// container healthcheck endpoints (/healthz, /readyz, /version) are
// internal/coordinator/httpapi's and stay outside the versioned contract
// (contract section 6.1).
//
// This package issues no MQTT publish and mutates no FPP instance: no
// show-affecting write exists here, and ADR-021 rule 5 continued to bar
// one until ADR-024 lifted it. Step 6 adds exactly three non-GET routes —
// POST and DELETE /api/v1/session, and no others — because authenticating
// is not a show operation; see auth.go and session.go. Every other route
// in this package remains GET-only, and the first actual show write
// endpoint (show:macro:run, device:power, fpp:command, or config:write)
// belongs to a later step, not to a quiet addition here.
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
// The SSE stream itself remains strictly server-to-client (contract
// section 6.4) — ADR-024 decision 5's periodic credential revalidation
// closes a connection rather than sending it anything the client could
// mistake for a command, and no write of any kind is reachable through
// it.
//
// # ADR-024: identity, authorization, and audit
//
// [Dependencies.Identity] is internal/coordinator/identity.Service,
// already built and not this package's to define (see that package's own
// doc comment for the layering rule: identity imports store, api imports
// identity, neither store nor identity may import api). This package owns
// the HTTP-layer half of ADR-024: credential resolution (auth.go),
// per-route scope enforcement and the CSRF rule (auth.go), the login cost
// bound (loginlimiter.go), the three session routes (session.go), the
// audit route (audit.go), and SSE stream revalidation (stream.go). It
// does not own principal/session/token storage, password hashing, or the
// audit trail's persistence — those are identity.Service's.
package api
