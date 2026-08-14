// Package mqttproto contains the MQTT topic conventions and versioned JSON
// envelope shared by the coordinator and node agents, per ADR-008's v1
// topic set. This is ShowMesh's own protocol, not a third-party wire
// format, so there is no external source to cite the way pkg/multisync
// cites FPP's own documentation and source; the authoritative reference for
// any change here is ADR-008 itself.
//
// This is a pure data/codec package: no network I/O, no MQTT client (paho
// or otherwise), no goroutines, no filesystem access. The agent and
// coordinator wire an actual MQTT client up around these types in Step 2
// Task C and Task D (see CLAUDE.md); this package only builds and parses
// topic strings and encodes and decodes the envelope and payloads that
// travel on them.
//
// # Six schemas: advertisement, health, presence, command, result, and echo
//
// This package builds and parses every topic in ADR-008's v1 set, and
// defines a payload type for each: showmesh.node.hello/v1,
// showmesh.node.health/v1, and showmesh.node.lwt/v1 (Step 2), plus
// showmesh.node.cmd/v1, showmesh.node.result/v1, and
// showmesh.node.agent.echo/v1 (added once pkg/command's command model —
// ARCHITECTURE section 8's identifier, target, params, idempotency key,
// deadline, issuer, requested revision, and confirmation method — stopped
// being a stub). CmdPayload and ResultPayload mirror pkg/command.Envelope's
// field semantics without importing pkg/command: this package stays a pure
// data/codec package (see below), and pkg/command "stays deliberately
// thin" and is not meant to be marshaled directly (see its own doc
// comment) — two independently defined, JSON-tagged wire types, reconciled
// by convention and by integration tests, is this codebase's established
// idiom for a wire boundary (see cmd/showmeshctl's own doc comments on the
// identical choice for its own wire types).
//
// # Envelope compatibility model
//
// Every payload travels inside one versioned [Envelope]. Unknown JSON
// fields are always tolerated (never rejected) so that additive,
// same-version changes stay compatible; a schema string this package does
// not recognize produces a typed error ([UnsupportedSchemaError]) instead
// of a panic or a silently wrong decode, so a caller can log and skip a
// message from a newer or unrelated version rather than crash on it.
package mqttproto
