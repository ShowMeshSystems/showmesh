// Package capability contains the namespaced, versioned capability model
// (ADR-002) shared by the coordinator and node agents to describe what a
// node can do, independent of its underlying hardware type.
//
// This is a pure data model: no network I/O, no MQTT client, no goroutines,
// no filesystem access. The agent advertises a Set over MQTT (see
// pkg/mqttproto's hello payload) and the coordinator stores and reasons
// about it; wiring that up is Task C and Task D of Step 2 (see CLAUDE.md),
// not this package.
//
// # Unknown and withdrawn IDs
//
// Per ADR-002, hardware support must be able to expand without changing the
// core object model, and per docs/architecture/OPERATOR-UI.md an
// unrecognized capability must render as a generic panel rather than fail
// the view. This package therefore validates ID syntax only, never
// vocabulary membership: [ID.Validate] accepts any syntactically valid ID,
// known or not. [ID.IsKnown] and [ID.IsWithdrawn] exist purely as
// informational lookups against the vocabulary in
// docs/architecture/ARCHITECTURE.md section 6, and MUST NOT be used to gate
// acceptance anywhere in this codebase.
//
// # Scope
//
// The known and withdrawn vocabularies below are taken verbatim from
// ARCHITECTURE section 6, as it stands 2026-08-10. This is ShowMesh's own
// model, not a third-party wire protocol, so there is no external source to
// cite the way pkg/multisync cites FPP's own documentation and source; the
// authoritative reference for any change to the vocabulary is ARCHITECTURE
// section 6 itself.
package capability
