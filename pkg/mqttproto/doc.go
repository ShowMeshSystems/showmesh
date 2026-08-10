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
// # Scope boundary: Step 2 covers hello, observed state, and last will only
//
// This package builds and parses every topic in ADR-008's v1 set,
// including showmesh/nodes/<node-id>/cmd and
// showmesh/nodes/<node-id>/result/<cmd-id>, because the topic shape is
// fixed by ADR-008 regardless of what travels on it yet. But it defines NO
// payload type for cmd or result: the command model (idempotency keys,
// deadlines, revisions, confirmation methods) is ARCHITECTURE section 8 and
// pkg/command, and pkg/command remains a stub until a later step. Only
// three schemas exist here: showmesh.node.hello/v1,
// showmesh.node.health/v1, and showmesh.node.lwt/v1.
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
