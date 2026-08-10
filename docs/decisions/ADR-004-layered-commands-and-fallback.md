# ADR-004: Use Primitive Commands, Show Macros, and Reduced Local Fallback

Status: Accepted  
Date: 2026-08-10

## Context

Operators need simple lifecycle actions, while integrations and diagnostics need bounded operations. Critical scheduled behavior must retain a safe subset when the coordinator is unavailable.

## Decision

Expose atomic, parameterized primitive commands and compose them into named show macros. FPP schedules macros through native commands. Each critical macro defines a reduced local fallback and explicitly identifies steps that require the coordinator.

## Consequences

- Daily schedules remain readable.
- Advanced integrations can use primitives without duplicating macro definitions.
- Macro execution must be persisted, idempotent, observable, and compensatable.
- Local fallback may provide reduced behavior and must report that degradation when connectivity returns.

## Later decisions narrowing this one

This ADR is not superseded, but two later decisions carve explicit exceptions out of its local-fallback expectation. Read them alongside it; the rule as stated above is not true without them.

- [ADR-016](ADR-016-controlled-devices-and-control-providers.md) creates a resource class — controlled devices — that runs no ShowMesh code and therefore holds no local fallback of its own. Where a network-reachable provider is hosted in the coordinator, a macro step driving it is unavailable exactly when the coordinator is. Such steps must be labelled coordinator-required, and genuinely show-critical device control belongs on a node instead.
- [ADR-019](ADR-019-audio-device-loss-fails-silent.md) makes silence the reduced local behavior when an audio output device is lost, and forbids automatic handover to FPP's own audio path. A macro with audio steps must declare that, rather than implying a fallback that does not exist.

Neither exception weakens the guarantee that a *running* show survives coordinator loss. Both constrain what lifecycle transitions can promise.

## Alternatives considered

Only primitives were rejected because schedules become fragile implementation scripts. Only macros were rejected because debugging and external integrations would lack precise operations. Coordinator-only macros were rejected because they make coordinator availability show-critical.

## Related research

[Audio node](../research/RES-007-audio-node-architecture.md) · [Configuration model](../research/RES-008-configuration-model.md) · [Failure testing](../research/RES-009-failure-mode-testing.md)
