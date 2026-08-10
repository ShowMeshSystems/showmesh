# Architecture Decision Records

[Documentation index](../README.md) · [Architecture specification](../architecture/ARCHITECTURE.md) · [Research tracker](../research/README.md)

ADRs record durable choices and their consequences. They do not replace research evidence. When new evidence invalidates a decision, add a superseding ADR and update the old record's status.

| ADR | Decision | Status |
|---|---|---|
| [ADR-001](ADR-001-fpp-is-authoritative.md) | FPP is the authoritative scheduler | Accepted |
| [ADR-002](ADR-002-capability-based-nodes.md) | Nodes are modeled by capabilities | Accepted |
| [ADR-003](ADR-003-desired-and-observed-state.md) | Desired and observed state remain separate | Accepted |
| [ADR-004](ADR-004-layered-commands-and-fallback.md) | Use primitives, macros, and reduced local fallback | Accepted |
| [ADR-005](ADR-005-pluggable-media-transport.md) | Media transport is pluggable | Accepted |
| [ADR-006](ADR-006-go-implementation-language.md) | Go is the implementation language | Accepted |
| [ADR-007](ADR-007-gstreamer-media-engine.md) | Node agent supervises GStreamer as media engine | Accepted |
| [ADR-008](ADR-008-mqtt-control-plane.md) | MQTT is the coordinator↔agent transport | Accepted |
| [ADR-009](ADR-009-sqlite-configuration-storage.md) | SQLite authoritative store; YAML portable bundles | Accepted |
| [ADR-010](ADR-010-apache-2-license.md) | Apache-2.0 license | Accepted |
| [ADR-011](ADR-011-context-aware-observability.md) | Health and alerts are context-aware | Accepted |
| [ADR-012](ADR-012-docker-coordinator-deployment.md) | Docker is the primary coordinator deployment | Accepted |

## Record template

Each ADR contains status, context, decision, consequences, alternatives, related research, and supersession information. Status values are `Proposed`, `Accepted`, `Deprecated`, and `Superseded`.
