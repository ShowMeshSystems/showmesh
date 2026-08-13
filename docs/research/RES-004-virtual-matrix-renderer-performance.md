# RES-004: Virtual-Matrix Renderer Performance

[Architecture](../architecture/ARCHITECTURE.md#44-renderer) · [Tracker](README.md) · [Transport research](RES-005-ndi-vs-hdmi-transport.md)

Status: planned (reference profile decided; bench pending) · Risk: critical · Verification: L0

## Decision to validate

A renderer node supports one or more independent logical surfaces. Each surface extracts its assigned virtual-matrix channels from a local FSEQ file, renders them to its own canvas, and owns an independent output transport or stream. FPP Connect uploads the sequence and FSEQ to the renderer node ahead of playback; the renderer does not consume a live matrix stream.

The renderer models logical surfaces, not physical projectors. A surface may feed one projector, or a single combined surface may feed a projector pair downstream in Resolume or the physical video path. That mapping is deployment configuration and does not enter the renderer object model.

The day-0/Halloween reference profile is:

- one logical surface per x86 renderer node (`N=1`);
- 40 frames per second;
- NDI output; and
- Dell OptiPlex Micro 7040-class hardware.

The architecture supports eventual `N` independent surfaces per node, but v1 implements `N=1`. Multiple surfaces per node and Raspberry Pi 4 / ARM HDMI profiles are deferred, not excluded.

## Questions

- What pixel throughput can the reference profile sustain at 40 fps without visible pacing artifacts?
- Which CPU, GPU, memory, conversion, copy, and NDI encode costs dominate as canvas dimensions and pixel count change?
- What frame-time distribution, output jitter, and missed-frame behavior does the reference profile exhibit?
- Does the renderer remain stable through a representative full-show soak and ordinary sender/receiver restart?

## Acceptance criteria

Treat canvas width, height, and pixel count as test parameters rather than selecting one universal resolution. For each tested layout, record pixel throughput, achieved frame rate, missed deadlines, frame-time distribution, output jitter, CPU/GPU load, and memory growth.

The day-0 profile must sustain 40 fps with stable frame pacing and no visible pacing faults over a representative full-show soak. Report jitter and missed frames as observed results; do not hide them behind an average frame rate.

## Test matrix

Start with one logical surface on a Dell OptiPlex Micro 7040-class x86 node, local FSEQ virtual-matrix extraction, and NDI output. Exercise representative layout dimensions and pixel counts, including low- and high-motion content, then record the parameters with every result.

Additional logical surfaces, Raspberry Pi 4 / ARM nodes, and HDMI output are later profiles. They require their own measured capability claims before use but are not prerequisites for closing the day-0 profile.

## Evidence and findings

The renderer and reference-profile decisions above were settled by the project owner on 2026-08-13. They are architecture intent, not performance evidence, so this record remains L0 until the physical renderer-to-Resolume bench runs.

## Decision, fallback, and revalidation

Implement v1 as `N=1` without embedding that limit into the architecture or data model. An unsupported layout receives an explicit reduced capability profile rather than a best-effort claim. Revalidate a profile after material renderer, driver, kernel, runtime, transport, or hardware changes.
