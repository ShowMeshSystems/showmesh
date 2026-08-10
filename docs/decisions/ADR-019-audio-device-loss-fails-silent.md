# ADR-019: Audio Device Loss Fails Silent, With No Automatic Fallback to FPP Audio

Status: Accepted  
Date: 2026-08-10

Supersedes the fallback position recorded in [RES-007](../research/RES-007-audio-node-architecture.md) ("FPP-hosted stereo playback remains the conservative fallback"), which was written before [ADR-017](ADR-017-showmesh-owns-audience-audio.md) made ShowMesh authoritative for audience audio.

## Context

[ADR-017](ADR-017-showmesh-owns-audience-audio.md) moves audience-facing audio to ShowMesh, which requires FPP's own audio output to be unused in a ShowMesh deployment. That raises the question ADR-017 deliberately did not answer: when a ShowMesh audio output fails mid-show, does anything take over?

The obvious answer is that FPP should resume playing audio, since it still has the media and is still running the sequence. [ADR-004](ADR-004-layered-commands-and-fallback.md) actively pushes in that direction — it requires every critical macro to define what runs locally when the coordinator is unreachable, and FPP-hosted audio is the most available fallback in the system.

The reference installation is what makes that answer wrong. Program audio feeds an FM transmitter. An automatic handover to a path whose routing, gain staging, and content ShowMesh does not control does not produce degraded audio; it produces unknown audio at unknown level into a transmitter, potentially simultaneously with a partially working ShowMesh output.

## Decision

**Audio output failure fails silent and reports critical degradation.** On device loss the engine stops sending to the failed output, keeps session and show state alive, marks the output critical, notifies the coordinator, and continues every unaffected output.

**ShowMesh never automatically hands audio back to FPP's own output.** Reinstating FPP audio is a deliberate operator action taken with knowledge of the routing, not an automatic recovery path.

**Failover to another audio node is permitted only after verification** that the candidate has the required media, the required output capabilities, the expected physical routing, and healthy status. Recovery prioritizes safe, deterministic behavior over seamless continuity; audio will be interrupted, and the goal is that it resumes correctly rather than inaudibly.

**A partial failure does not escalate.** If one output fails, others continue and the session stays active.

Automatic failover policy may become configurable per deployment later. It is not automatic in the initial implementation, which is also what ARCHITECTURE §12's deferral of automatic failover during a live set requires.

## Consequences

- **This is a deliberate, narrow exception to ADR-004's local-fallback expectation, and must be recorded in the macro definitions rather than left implicit.** Macros with audio steps declare that the reduced local behavior for audio output failure is silence plus a critical alert. ADR-004 requires every critical macro to state what runs locally; here the honest answer is "nothing, and that is the safe outcome". A fallback that produces unsafe output is worse than no fallback, and ADR-004's purpose is protecting the show, not populating a field.
- The operator is now the recovery mechanism for a class of failure, so the failure must be unmissable. Audio output loss is a critical alert with an unambiguous presentation, and OBSERVABILITY §11 must treat it as show-stopping rather than as one degraded resource among many.
- A documented manual procedure for restoring audio — including deliberately reverting to FPP audio — must exist and be tested, because the automatic path was removed on purpose. ARCHITECTURE §12 Phase 1's "documented manual recovery procedures" now has a specific, mandatory entry.
- Silence during a show is an accepted outcome. That is a real cost, stated plainly: the decision trades a chance of automatic recovery for the certainty of not producing uncontrolled output.
- Standby audio nodes are only useful if their media and routing are verified continuously rather than at failover time, since verification at the moment of failure is verification during the emergency. Media availability per node becomes a readiness check.
- Detection latency now matters directly. A device that has vanished but has not yet been detected is silent and unreported, so device-loss detection time is a bench measurement in RES-007, not an assumption.

## Alternatives considered

**Automatic fallback to FPP audio output** was rejected for unexpected routing, duplicate audio if the ShowMesh output partially recovers, timing differences against LTC and lighting, unsafe gain levels, and uncontrolled transmitter behavior. Every one of these is worse in front of an audience than silence, and several are worse than a stopped show.

**Automatic failover to a standby audio node without verification** was rejected because an unverified node may lack the media, lack the LTC channel, or be wired to different physical outputs, converting one silent output into wrong audio from the wrong place.

**Continuing to send to a failed device and hoping it returns** was rejected because it hides the fault and produces an unbounded silent period with no alert.

**Muting the whole program bus on any output failure** was rejected as over-escalation: losing a monitor feed should not silence the audience.

## Related research

[Audio-node architecture](../research/RES-007-audio-node-architecture.md) · [Failure-mode testing](../research/RES-009-failure-mode-testing.md) · [Device telemetry adapters](../research/RES-012-device-telemetry-adapters.md)

RES-007 must measure device-loss detection latency and verify that session state survives it. RES-009 must inject USB interface removal, Dante interruption, and audio-node loss during live playback, and must verify that no audio reaches the FM transmitter from any path ShowMesh did not intend.
