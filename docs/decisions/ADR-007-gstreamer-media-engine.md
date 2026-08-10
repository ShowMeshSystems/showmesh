# ADR-007: The Node Agent Supervises GStreamer as Its Media Engine

Status: Accepted  
Date: 2026-08-10

## Context

The renderer and audio engine need video decode, frame-paced output, gapless audio, crossfades, NDI send, and HDMI output across Linux (and possibly Windows for audio). Building this from scratch means owning codecs, A/V sync, and device quirks. GStreamer already provides decode, audio routing, an NDI sink (`ndisink` from gst-plugins-rs, which dlopens the user-installed NDI runtime — exactly the pattern required by RES-006), and hardware acceleration. FPP 10 itself is moving to a GStreamer/PipeWire media engine, making this the ecosystem-aligned direction.

## Decision

The node agent constructs, supervises, and monitors GStreamer pipelines for all media execution: decode, render, audio playback and fades, NDI send, and HDMI/local display output.

ShowMesh-owned code is limited to: pipeline construction per assignment, MultiSync-derived clock/position control (seek, rate slew), readiness probing, health/stats extraction (buffer levels, QoS messages, underruns), and restart policy. ShowMesh must not implement codecs or per-frame rendering in application code.

Sync control uses GStreamer's clock and seek/rate APIs; the slew/jump policy mirrors FPP remote semantics (see RES-002 evidence).

## Consequences

- Custom code shrinks to synchronization and supervision — the parts that are actually novel.
- Nodes require a GStreamer 1.x runtime (+ gst-plugins-rs for NDI); the agent installer must manage this dependency.
- RES-004 renderer benchmarks now test concrete GStreamer pipelines per hardware profile rather than a hypothetical custom renderer.
- LTC generation (RES-007) may be a GStreamer element or a small dedicated process; the audio-node prototype decides.
- If GStreamer frame pacing proves inadequate for a profile (RES-004 exit criterion), a custom renderer for that capability requires a superseding ADR.

## Alternatives considered

A custom SDL/FFmpeg/NDI-SDK pipeline was rejected as owning too much undifferentiated media plumbing. A deferred spike was rejected because GStreamer's existing NDI sink and FPP 10's direction make the prior strong enough to commit, with RES-004 as the escape hatch.

## Related research

[Renderer performance](../research/RES-004-virtual-matrix-renderer-performance.md) · [Linux NDI](../research/RES-006-linux-ndi-support.md) · [Audio node](../research/RES-007-audio-node-architecture.md)
