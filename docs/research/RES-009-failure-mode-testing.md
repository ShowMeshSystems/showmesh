# RES-009: Failure-Mode Testing

[Architecture](../architecture/ARCHITECTURE.md#11-failure-handling) · [Tracker](README.md)

Status: planned · Risk: critical · Verification: L0

## Decision to make

Define the failure contract for every show-critical component and establish the evidence required to declare a release show-ready.

## Failure record format

Each scenario must state the injected condition, detection signal and deadline, expected safe or degraded state, autonomous recovery deadline, operator notification, manual action, persistent evidence, and conditions that prevent automatic recovery.

## Minimum matrix

| Failure | Expected concern |
|---|---|
| Coordinator loss or restart | Current show continues; local state is not corrupted |
| Switch, VLAN, or control-network loss | Nodes expose loss and follow bounded local policy |
| Packet delay, loss, duplication, or reordering | Timing becomes degraded rather than falsely healthy |
| Node reboot or process crash | Assignment and recovery are visible and deterministic |
| Missing or corrupt media | Playback does not substitute the wrong asset |
| Decoder hang or frozen frames | Fault is detected without treating intentional stills as failures |
| NDI sender, receiver, or link loss | Output enters defined state and reconnect is measurable |
| HDMI/capture or EDID loss | Signal loss is distinguished from black content |
| SMPTE loss, noise, or jump | Resolume and audio follow defined hold/recover policy |
| Audio device or Dante route loss | Program and LTC failure are independently visible |
| Resolume restart | Composition and timing reconcile safely |
| Conflicting or duplicate commands | Idempotency and revision rules prevent oscillation |
| Stale configuration or partial deployment | Mixed revisions are detected and contained |
| Disk exhaustion | Existing show assets remain protected where possible |
| Full power restoration | Services start in safe order without unintended playback |

## Acceptance criteria

Before show readiness, every critical scenario has an observed L4 result on the reference topology, a documented manual recovery path, and retained proof. No critical failure may be reported healthy merely because a process or host responds.

## Method

Build automated fault injection where safe and use controlled physical tests for device, cable, audio, capture, and power failures. Run faults during resting, pre-show, live, intermission, and post-show states. Repeat failures during recovery to identify unstable loops.

## Evidence and findings

No evidence collected.

## Decision, fallback, and revalidation

This record remains open throughout development. Any new component, capability, transport, lifecycle state, or recovery mechanism must add corresponding scenarios. A release cannot inherit L4 status across material topology or timing changes without targeted revalidation.
