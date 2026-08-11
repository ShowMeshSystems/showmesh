// Command showmeshctl is the non-UI client for the ShowMesh coordinator's
// public control API (ADR-014). It exists to prove — mechanically, not by
// assertion — that the API described in api/openapi.yaml is a real, usable,
// versioned contract and not merely whatever the Operator UI happens to send.
//
// Per Step 3's design contract (see docs/build/BUILD-PLAN.md Step 3 and the
// contract the orchestrator wrote for it), this package declares its own
// wire-decoding structs from the contract's pinned shapes (contract §6.10)
// and the API's own api/openapi.yaml, rather than importing the
// coordinator's internal packages or pkg/observation. That independence is
// deliberate and is enforced by TestNoForbiddenImports in
// importgraph_test.go: if this package ever imports
// internal/coordinator/api, internal/coordinator/store,
// internal/coordinator/inventory, internal/coordinator/collector, or
// pkg/observation, that test fails the build. A JSON tag rename on the
// server would otherwise rename the field on both sides at once and every
// test would keep passing — exactly the shape of risk contract §1 warns
// about.
//
// showmeshctl is read-only. It has no write or command subcommand, matching
// Step 3's scope: there is no write API yet to call.
package main
