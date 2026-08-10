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

## Alternatives considered

Only primitives were rejected because schedules become fragile implementation scripts. Only macros were rejected because debugging and external integrations would lack precise operations. Coordinator-only macros were rejected because they make coordinator availability show-critical.

## Related research

[Audio node](../research/RES-007-audio-node-architecture.md) · [Configuration model](../research/RES-008-configuration-model.md) · [Failure testing](../research/RES-009-failure-mode-testing.md)
