# RES-005: NDI Versus HDMI Transport

[Architecture](../architecture/ARCHITECTURE.md#47-transport-adapters) · [Tracker](README.md) · [Linux NDI research](RES-006-linux-ndi-support.md) · [Failure testing](RES-009-failure-mode-testing.md)

Status: planned (transport roles decided; validation pending) · Risk: critical · Verification: L0

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

## Decision, fallback, and revalidation

Use NDI for the v1/reference renderer path and retain HDMI as a supported alternate/fallback. Preserve adapters and capability advertisement for NDI, HDMI, or both. Revalidate an affected profile after material transport runtime, NIC, switch, GPU, capture-card, driver, cabling, or Resolume changes.
