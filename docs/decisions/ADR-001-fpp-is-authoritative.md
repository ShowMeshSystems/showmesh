# ADR-001: FPP Is the Authoritative Scheduler

Status: Accepted  
Date: 2026-08-10

## Context

Existing displays already use FPP schedules and playlists. Introducing a second calendar or timeline authority would create conflicting starts, unfamiliar operations, and a new single point of failure.

## Decision

FPP owns calendar schedules, playlist order, and scheduled show start. Platform lifecycle actions intended for playlists or schedules are exposed as native FPP commands. The coordinator orchestrates the resulting transition but does not independently schedule it.

## Consequences

- Existing FPP operating practices remain valid.
- Coordinator loss cannot cancel FPP's authority over an already configured show.
- FPP integration is a core compatibility boundary.
- The coordinator may offer editing or visibility later, but changes must resolve to FPP-owned schedule state.

## Alternatives considered

A coordinator-owned scheduler was rejected because it duplicates FPP and creates split-brain timing. An entirely external automation system was rejected as the required primary interface, though it may call platform commands.

## Related research

[FPP MultiSync](../research/RES-002-fpp-multisync-compatibility.md) · [xLights/FPP Connect](../research/RES-003-xlights-fpp-connect-compatibility.md)
