# RES-005: NDI Versus HDMI Transport

[Architecture](../architecture/ARCHITECTURE.md#47-transport-adapters) · [Tracker](README.md) · [Linux NDI research](RES-006-linux-ndi-support.md) · [Failure testing](RES-009-failure-mode-testing.md)

Status: testing (single-surface NDI soak passed 2026-08-16; alignment, multi-surface, HDMI and recovery open) · Risk: critical · Verification: **L2 for single-surface NDI stability and pacing on the wired amd64 path**; L0 for everything else, including alignment

## Decision to validate

NDI is the v1 and reference transport from renderer nodes into Resolume. HDMI remains a supported alternate and fallback transport.

This does not select one global transport winner. Transport is a node capability and configuration choice: a node may advertise NDI send, HDMI output, or both, and a deployment assigns a surface to a supported transport according to its topology and hardware. Rendering remains separate from transport.

## Comparison criteria

- Observed alignment and frame pacing between simultaneous surfaces.
- Stability over a representative full-show soak.
- CPU/GPU load, image quality, and practical end-to-end latency.
- Aggregate bandwidth and operational visibility on the reference wired network.
- Linux, Resolume, HDMI/capture, EDID, and driver compatibility relevant to the chosen node profile.
- Cabling, capture density, cost, and replacement time.

## Acceptance criteria

The NDI reference path must sustain the planned reference surfaces for a representative full-show soak with observed alignment and stable frame pacing. Record any drift, jitter, visible faults, and missed frames.

Acceptance does not require exhaustive theoretical packet-loss or every-environment testing. Successful development over Wi-Fi is useful evidence, while the intended wired Ethernet show network is expected to be more robust; the practical wired path remains the acceptance environment.

HDMI profiles must demonstrate stable output and capture for the deployment that selects them. A node advertising both transports must report each capability independently; support for one is not evidence for the other.

## Test method

Feed identical time-marked content from the reference renderer profile into Resolume over NDI and observe sustained alignment, pacing, and stability on wired Ethernet. Record node, NIC, switch, Resolume, runtime, canvas dimensions, pixel count, and soak duration. Validate HDMI/capture when a deployment selects that alternate profile rather than making it a prerequisite for the NDI v1 path.

Broader congestion, packet-loss, link-flap, EDID, and capture-card fault injection remains required in targeted transport and [failure-mode testing](RES-009-failure-mode-testing.md) before show readiness. It is not a closure gate for selecting the v1/reference transport and does not require an exhaustive theoretical environment matrix.

## Evidence and findings

The transport roles and capability model above were settled by the project owner on 2026-08-13. The owner also reports successful NDI development testing over Wi-Fi; that observation supports the expectation that the intended wired Ethernet path will be at least as robust, but it is not a recorded repository bench and does not raise this record above L0. The reference-path alignment and stability bench remains open.

These decisions are recorded as durable constraints in [ADR-026](../decisions/ADR-026-renderer-surface-model-and-reference-transport.md), which also fixes the rule that the reference profile is described as intended rather than supported until this record's bench runs.

### The wired NDI soak, 2026-08-16 (L2, partial)

The [Track B spike](../bench/TRACK-B-NDI-SPIKE.md) ran overnight on the owner's bench and replaces the Wi-Fi expectation above with a wired measurement. **982,100 frames at 1920x1080 UYVY, 0 dropped, 0 late, 0 errors, sustained 40.00 fps over 6 h 49 min continuous**, from a Debian 13 Dell OptiPlex Micro 7050 across wired Ethernet into Resolume Arena, with a second receiver (NDI Video Monitor) attached throughout and also reporting clean. The sink ran `sync=true`, so the zero-drop count was produced by an active QoS path rather than by a disabled one. Full parameter table in [RES-006](RES-006-linux-ndi-support.md).

**Which acceptance criteria this satisfies.** Stability over a representative full-show soak, for one surface: yes, and by a comfortable margin, since 6 h 49 min exceeds any show night. Frame pacing: yes, by the counters; pacing was not separately instrumented but a QoS sink that drops nothing for 982,100 consecutive frames leaves little room for hitching. Operational visibility on the reference wired network: partially, in that the path worked and nothing anomalous surfaced.

**CPU load, measured 2026-08-16: 86% of one core**, for a single-threaded pipeline carrying frame generation, SpeedHQ encode and network send together, with the machine otherwise nearly idle and no thermal throttling observed. The transport's cost is therefore concentrated on one thread rather than spread, which is the property that matters and is developed in [RES-004](RES-004-virtual-matrix-renderer-performance.md). GPU load is not applicable: the standard NDI SDK has no GPU offload.

**Which it does not.** **Observed alignment between simultaneous surfaces is untouched and needs two senders to test at all**, so the criterion this record lists first remains L0. Aggregate bandwidth was not measured. Latency was not measured. HDMI remains entirely unvalidated, and per this record's own rule, support for NDI is not evidence for it. Recovery behaviour, meaning the sender and receiver surviving each other's restart, is spike run 5 and has not run.

**The source was synthetic.** These frames came from `videotestsrc`, not from a rendered virtual matrix, so this is a transport result and not a renderer result. That separation is deliberate and is why it is recorded here rather than in RES-004.

## Decision, fallback, and revalidation

Use NDI for the v1/reference renderer path and retain HDMI as a supported alternate/fallback. Preserve adapters and capability advertisement for NDI, HDMI, or both. Revalidate an affected profile after material transport runtime, NIC, switch, GPU, capture-card, driver, cabling, or Resolume changes.
