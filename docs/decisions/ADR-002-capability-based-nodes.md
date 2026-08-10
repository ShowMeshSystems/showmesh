# ADR-002: Nodes Are Modeled by Capabilities

Status: Accepted  
Date: 2026-08-10

## Context

Community deployments will use heterogeneous and repurposed hardware. Fixed types such as “audio node” or “projection node” prevent useful combinations and make failure reassignment harder.

## Decision

Nodes advertise versioned capabilities with measurable attributes and constraints. Assignments request capabilities rather than hardware brands, operating systems, or fixed node classes.

## Consequences

- One node may render video, output HDMI, and play audio when resources allow.
- Workloads can be reassigned to compatible nodes.
- Hardware support can expand without changing the core object model.
- Capability claims require validation; advertising a feature is not proof that it meets a show profile.

## Alternatives considered

Fixed node roles were rejected as too rigid. Hardware-specific scheduling was rejected because it would turn the reference installation into the platform definition.

## Related research

[Renderer performance](../research/RES-004-virtual-matrix-renderer-performance.md) · [Linux NDI](../research/RES-006-linux-ndi-support.md) · [Audio node](../research/RES-007-audio-node-architecture.md)
