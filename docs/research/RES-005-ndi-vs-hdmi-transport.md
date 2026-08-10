# RES-005: NDI Versus HDMI Transport

[Architecture](../architecture/ARCHITECTURE.md#47-transport-adapters) · [Tracker](README.md) · [Linux NDI research](RES-006-linux-ndi-support.md) · [Failure testing](RES-009-failure-mode-testing.md)

Status: unresearched · Risk: critical · Verification: L0

## Decision to make

Choose the preferred and fallback transports for media-node output into Resolume, with deployment profiles rather than one universal winner.

## Hypothesis

NDI may provide simpler routing and scaling for the reference network, while HDMI/capture may provide a more deterministic or broadly compatible fallback. This is not yet verified.

## Comparison criteria

- End-to-end latency, jitter, frame loss, and recovery.
- Synchronization between simultaneous surfaces.
- CPU/GPU load and image quality.
- Aggregate bandwidth and switch behavior.
- Linux and hardware compatibility.
- Cabling distance, capture density, EDID, hot-plug, and driver stability.
- Mapping flexibility, operational visibility, licensing, cost, and replacement time.

## Acceptance criteria

The selected reference path must sustain all planned surfaces for a full-show soak, remain within measured synchronization tolerances, recover predictably from sender/receiver and link interruptions, and expose enough health data to distinguish media, network, capture, and Resolume faults.

## Test method

Feed identical time-marked content through NDI and HDMI/capture into the same Resolume composition. Record glass-to-glass timing with a common reference. Test steady state, congestion, packet loss, link flap, sender restart, capture disconnect, EDID change, and Resolume restart.

## Evidence and findings

No evidence collected.

## Decision, fallback, and revalidation

Decision pending. Both transports remain architectural modules. Revalidate after transport runtime, NIC, switch, GPU, capture-card, driver, or Resolume changes.
