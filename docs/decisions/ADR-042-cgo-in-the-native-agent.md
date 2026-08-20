# ADR-042: cgo lives in the native agent, and C generates every sample

Status: Accepted
Date: 2026-08-20

Narrows [ADR-006](ADR-006-go-implementation-language.md)'s "Go for coordinator and agent" and restates [ADR-007](ADR-007-gstreamer-media-engine.md)'s per-sample prohibition for audio, without superseding either. Bounded by [ADR-012](ADR-012-docker-coordinator-deployment.md), whose CGo-free rule is unchanged and applies to the coordinator image.

Pre-approved by the owner when the go-gst backend was selected on 2026-08-19, and issued when the LTC shape settled on 2026-08-20.

## Context

The audio node needs runtime control of a live pipeline: start a session, duck it under an announcement, fade it, seek it, swap its media, remove it. The `gst-launch-1.0` subprocess pattern Track B uses cannot do that, because a `gst-launch` graph cannot be changed after it starts. The owner selected go-gst, which is cgo.

LTC is the second half of the same question. It has to leave the interface on a discrete channel in the same clock domain as program audio ([ADR-018](ADR-018-program-and-ltc-share-a-clock-domain.md)). A pre-rendered LTC asset is refused because a file cannot be re-anchored on a seek. A separate generator process was specified while cgo was off the table, and once cgo was in the agent that reason was gone: the supervisor, its heartbeat watchdog and its restart lockout existed only to make a pipe between two processes trustworthy, and a pipe that does not exist needs nothing to make it trustworthy.

## Decision

### 1. The native agent may link C libraries; the coordinator may not

The agent already needs GPU, HDMI, audio, EDID and NDI access and is deployed natively, so it is the component where a native dependency costs what it looks like it costs. The coordinator's image stays static, distroless, CGo-free and cross-compilable to arm64, exactly as ADR-012 requires. That boundary is mechanically checked: `CGO_ENABLED=0 go build ./...` must stay exit 0, so every cgo-carrying package ships a `!cgo` companion that compiles and reports itself honestly unavailable.

Two libraries are authorized by this record: GStreamer through go-gst, and libltc. Another one is another decision.

### 2. C generates every sample; Go forwards buffers and never computes them

ADR-007 keeps Go out of the per-frame media path. That rule is unchanged here and this record states where the line falls for audio: libltc's encoder produces the PCM, and Go's part is to hand the resulting buffers to GStreamer and to decide *when* a run starts, stops, and re-anchors. No Go code computes, mixes, resamples, fades or attenuates a sample.

The same line already governs the rest of the audio graph: mixing, fades, ducking, interleave and channel placement are GStreamer elements, never a Go sample loop.

### 3. LTC is a capability of the engine, not a process beside it

The generator is not supervised, restarted, or heartbeat-checked, because it is not a process. It is a source inside the one output pipeline the node already runs, which is what makes "program and LTC share one clock domain" a property of the topology rather than a claim about two clocks staying close.

**The channel never starves.** `interleave` blocks until every sink pad has data, so an LTC channel that stopped producing would stall program audio. The LTC channel therefore always produces: silence when no run is active, LTC when one is.

### 4. Liveness is evidence that samples were emitted, never that a run was requested

A run that was asked for and has produced nothing is not `running`. The reported timecode is the timecode of samples actually pushed, and a stopped generator reports no timecode at all rather than carrying its last one forward. This is the same rule that took four subsystems to learn: absence of evidence is not evidence of absence, and its mirror, a request is not an outcome.

### 5. An LTC failure never stops program audio

If the encoder cannot be created or a run cannot start, the failure is reported as LTC evidence with its reason and the show keeps playing. This is [ADR-019](ADR-019-audio-device-loss-fails-silent.md)'s posture one component in: a timecode problem costs timecode, not the audience's audio.

## Consequences

- The agent's build dependencies gain `libltc-dev` alongside the GStreamer development headers; Debian 13 runtime gains `libltc11`. No ShowMesh LTC generator executable exists, is built, or is packaged.
- The agent requires GLib 2.80 or newer, measured: it does not build on Debian 12.
- `make build` produces every binary with `CGO_ENABLED=0`, so the agent has its own native build target; a build that forgets it compiles the audio engine out and the node reports itself honestly unable to play anything.
- Nothing here is evidence about sound. Every gate behind this record runs against a non-hardware sink; the interface, the channel discreteness, and the program-to-LTC alignment stay commissioning measurements.
