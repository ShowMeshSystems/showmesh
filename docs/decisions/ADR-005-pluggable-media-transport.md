# ADR-005: Media Transport Is Pluggable

Status: Accepted  
Date: 2026-08-10

## Context

NDI offers flexible network routing, while HDMI/capture offers broad compatibility and a physical fallback. Linux support, synchronization, licensing, and failure behavior are not yet sufficiently verified to mandate either path universally.

## Decision

Separate frame rendering from transport. Support transport through capability adapters, initially targeting NDI and local HDMI/capture profiles. Architecture and media formats must not assume one transport.

## Consequences

- Deployments can choose based on topology, hardware, and evidence.
- Transport-specific health, latency, and recovery remain explicit.
- NDI licensing and runtime dependencies stay isolated from the open core.
- Supporting two paths increases testing, configuration, and operational documentation.

## Alternatives considered

NDI-only was rejected pending Linux and synchronization evidence. HDMI-only was rejected because it limits routing flexibility and scales capture hardware with source count.

## Related research

[NDI versus HDMI](../research/RES-005-ndi-vs-hdmi-transport.md) · [Linux NDI](../research/RES-006-linux-ndi-support.md) · [Renderer performance](../research/RES-004-virtual-matrix-renderer-performance.md)
