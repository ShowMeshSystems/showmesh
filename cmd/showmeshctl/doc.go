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
// the API it talks to: ADR-024 (Step 6) adds authentication, authorization,
// and an audit log, but no write endpoint of its own, and this package adds
// nothing beyond what that step shipped — `session` and `audit` are both
// GET.
//
// Step 6 is also this package's proof of ADR-024 decision 1's promise that
// "principal kind does not restrict credential form": a human may mint an
// API token and drive this CLI from a terminal exactly as a machine
// principal would, and the audit log then attributes the action to that
// person. If showmeshctl could only act as a bearer-token robot with no way
// to show a human which principal, role, and scopes a token resolves to
// (`session`), or could not read what was done under whose identity
// (`audit`), that promise would be true in the ADR and false in the one
// client ADR-014 requires exist outside the browser.
package main
