# ADR-003: Desired and Observed State Remain Separate

Status: Accepted  
Date: 2026-08-10

## Context

Sending a command does not prove that a projector, renderer, audio route, or composition reached the intended condition. Complex shows need to distinguish intent from evidence.

## Decision

The coordinator stores desired state separately from timestamped observed state and continuously evaluates convergence. Commands declare how success will be confirmed or explicitly state that confirmation is unavailable.

## Consequences

- Operators can see `converged`, `progressing`, `degraded`, `unknown`, and `conflicted` conditions.
- Adapters need read paths or independent evidence where possible.
- Stale observations cannot be presented as current health.
- Reconciliation must avoid endless retries and respect operational-state policies.

## Alternatives considered

Command-result-only state was rejected because transport success is not device success. Treating discovery data as authoritative desired configuration was rejected because hardware facts and operator intent are different concerns.

## Related research

[Configuration model](../research/RES-008-configuration-model.md) · [Failure testing](../research/RES-009-failure-mode-testing.md)
