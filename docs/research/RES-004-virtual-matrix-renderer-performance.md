# RES-004: Virtual-Matrix Renderer Performance

[Architecture](../architecture/ARCHITECTURE.md#44-renderer) · [Tracker](README.md) · [Transport research](RES-005-ndi-vs-hdmi-transport.md)

Status: unresearched · Risk: critical · Verification: L0

## Decision to make

Establish supported renderer profiles by hardware class, resolution, frame rate, codec, output count, and transport.

## Questions

- Can the renderer consume the required FPP/xLights data without frame pacing artifacts?
- What CPU, GPU, memory, decode, and copy costs dominate?
- Is one process per surface or a combined canvas more reliable?
- How many displays or NDI streams can each reference platform sustain?
- What reduced profile is realistic on Raspberry Pi-class hardware?
- How do scaling, color conversion, alpha, and hardware decoding affect latency?

## Acceptance criteria

Define per-profile thresholds before benchmarking. At minimum measure achieved frame rate, missed deadlines, p50/p95/p99 frame time, output jitter, CPU/GPU load, memory growth, thermals, and recovery after media or output changes. A show-ready profile must complete an expected-night soak without unbounded resource growth or visible pacing faults.

## Test matrix

Include reference x86 mini PCs and at least one ARM candidate; one and two outputs; representative resolutions and frame rates; local HDMI and NDI; static, high-motion, and worst-case matrix content; software and hardware decoding; cold and thermally saturated states.

## Evidence and findings

No evidence collected.

## Decision, fallback, and revalidation

Decision pending. Unsupported hardware receives an explicit reduced capability profile rather than best-effort claims. Revalidate after renderer, driver, kernel, firmware, or codec changes.
