# ADR-006: Go Is the Implementation Language for Coordinator and Node Agent

Status: Accepted  
Date: 2026-08-10

## Context

Code is about to start. The coordinator ships as a container on `linux/amd64` and `linux/arm64`; node agents run natively on heterogeneous hardware (x86 mini PCs now, Raspberry Pi-class later, possibly Windows for the audio node). The agent needs tight UDP work (MultiSync listener), process supervision, and easy deployment. The project also wants a large potential contributor pool from the hobbyist community.

## Decision

Go is the implementation language for the coordinator and the node agent. One repository, one language, shared packages for the protocol, capability model, and state types.

Media-heavy work is delegated to GStreamer per [ADR-007](ADR-007-gstreamer-media-engine.md), so Go code stays in the control, timing, and supervision domain. GStreamer may be driven via `go-gst` bindings or supervised `gst-launch`-style subprocesses; that choice is an implementation detail decided by the walking-skeleton spike, not an ADR.

The coordinator's web UI framework is deliberately not decided here; the coordinator serves a bundled frontend whose stack may be chosen when UI work begins.

## Consequences

- Single static cross-compiled binaries for agents; trivial installs on nodes.
- Goroutine-based concurrency fits the MultiSync listener, health reporting, and supervision loops.
- GC pauses are acceptable because per-frame media timing lives in GStreamer, not Go (a constraint to preserve: keep Go out of the per-frame path).
- CGo is confined to the GStreamer boundary if bindings are used.
- Contributors need only one language across coordinator and agent.

## Alternatives considered

Rust was rejected for development speed and contributor accessibility, despite superior GStreamer bindings; nothing prevents a future Rust component behind the same MQTT protocol. Python was rejected for packaging weight on nodes and weak fit for timing loops. TypeScript/Node was rejected for the agent's supervision and timing needs.

## Related research

[FPP MultiSync](../research/RES-002-fpp-multisync-compatibility.md) · [Renderer performance](../research/RES-004-virtual-matrix-renderer-performance.md)
