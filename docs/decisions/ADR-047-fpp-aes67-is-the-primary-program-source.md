# ADR-047: FPP's AES67 Stream Is the Primary Program Source; Local Playback Is the Standby

Status: Accepted (owner, 2026-08-28)  
Date: 2026-08-28

## Context

[ADR-017](ADR-017-showmesh-owns-audience-audio.md) decided that ShowMesh owns audience-facing audio and that audio nodes play local copies of the media, aligned to the FPP show timeline. It rejected streaming PCM from FPP to audio nodes because that put the network inside the real-time audio path, and it required FPP's own audience audio output to be disabled or unused. At the time FPP had no network audio output and no shared clock; the only stream available would have been an ad hoc one, and the only timing reference was MultiSync's position feed.

FPP 10 changed both facts. It sends its program audio as AES67 with RTP timestamps derived from a PTP-domain clock ([RES-019](../research/RES-019-ptp-synchronized-multi-node-audio.md) §4.1), and inside FPP the media is the master that the sequence servos to (§15.2). A ShowMesh node that receives that stream is listening to the same audio FPP's pixels follow, on a timeline every PTP-locked node can read. Two players correcting relative position through MultiSync becomes one player and several synchronized listeners.

The network is still a failure point, and FPP is still a single upstream. ShowMesh's local playback engine exists, is proven on two real nodes, and is the only thing that can speak when FPP is gone: fault and shutdown announcements, the resting bed, test audio, and the orderly transition when site power fails while nodes stay on UPS.

## Decision

1. **The program bus has an ordered source list.** For a show item, the primary source is the FPP AES67 stream carrying that item; the standby source is the node's local synchronized copy of the same asset, started at the position the schedule and the stream's timestamps imply. There is one program bus and one active source at a time; the sources are never mixed together.

2. **ShowMesh-owned sources are always local.** Background and resting music, announcements, alerts and failure messages, and test audio are produced by the node's own engine and mixed into the program bus over whichever program source is active. Ducking, fades, and gain behave identically for both program sources.

3. **Output backends are fed from the program bus and are unchanged in kind** (AUDIO-ENGINE §8): the local hardware interface and the FM transmitter feed are PCM outputs of the one bus; the third-party synchronized listener system remains a synchronized remote output under [RES-016](../research/RES-016-third-party-synchronized-audio-output.md) with its own declared presentation offset; AES67 send is a future stream output on the same media clock.

4. **Source selection is an explicit policy, never a guess.** Which source is active, the definition of stream health, media identity matching between the stream and the local asset by content hash, the switch itself, and return to primary are settled by RES-019 §15.3's failover research before any seam implements them. The node reports the active source and the reason for every switch. Losing the primary is a degradation with a visible cause, not silence.

5. **This narrows ADR-017.** "Audio nodes play local media" becomes "audio nodes hold local media and play it as the standby program source and for every ShowMesh-owned source". "FPP's audience audio output is disabled or unused" becomes "FPP's audience output does not reach the audience directly; it may feed an AES67-only or null output group". ADR-017's ownership decision, its fail-silent and manual-recovery consequences, and its rejection of the coordinator or broker in the real-time path are unchanged. [ADR-018](ADR-018-program-and-ltc-share-a-clock-domain.md) is unchanged: LTC is generated locally from the same clock-locked interface whichever program source is active.

6. **Ordering rule, not optional.** The standby is only synchronized if the node already holds the shared PTP media clock, a scheduled-start timeline, and the measured offset between FPP's media position and its stream timestamps. The clock provider and scheduled start (RES-019 stages 1 and 2) therefore land before AES67 receive, and AES67 receive lands before the rate lock is required to protect the standby path. A build that takes the stream first and adds the clock later will produce a node that goes silent or jumps on failover, which is the failure this record exists to prevent.

## Consequences

- The audio node gains a program-source state (`primary`, `standby`, `none`) with a reason, reported as telemetry and shown to the operator, and an `audio.node` policy for whether the stream is preferred at all (a zone node may be local-only).
- FPP configuration on the primary controller changes from "audio disabled" to "an AES67 send instance on an output group that does not reach the audience". Whether that configuration keeps FPP's `secondsElapsed` identical to a real-card configuration is unverified and gates the primary source; the "disabled versus muted" capture already owed against ADR-017 is now on the critical path.
- The clock subsystem in RES-019 §5 is a prerequisite, not an alternative. A node with no locked clock provider may still receive the stream, but its standby is the cue-boundary realignment of today and it says so.
- ShowMesh must not run its own `ptp4l` on a host where FPP's AES67 subsystem owns it, and must expect FPP's `ptp4l` to be absent when no AES67 instance is enabled (RES-019 §12).
- The renderer may later use the same stream timeline as its absolute reference (RES-019 §15.4); nothing here requires it.
- Day-0 is unaffected. The current local-playback path is exactly this record's standby path and keeps working with no primary configured.

## Alternatives considered

**Keep local playback as the only program source and improve MultiSync alignment.** Rejected. Two independent players are bounded by MultiSync's packet cadence and FPP's own servo; a listener on FPP's stream is bounded only by FPP's servo.

**Make the stream the only source and drop local playback for show items.** Rejected. It removes the only path that works when FPP, the switch, or the multicast route is gone, and it removes every ShowMesh-owned source's engine.

**Run both sources at once and cross-mix.** Rejected. Two sources of the same material at slightly different phase comb-filter audibly; one active source with an explicit switch is the only honest model.

**Forward the FPP stream to the third-party listener system.** Rejected on current understanding: that system needs uploaded, transcoded assets and does not accept a live stream (RES-016).

## Related research

[RES-019](../research/RES-019-ptp-synchronized-multi-node-audio.md) §15 records the source facts, the failover questions Q6 to Q10, and the stage order. [RES-007](../research/RES-007-audio-node-architecture.md) keeps the ADR-017 "disabled versus muted" capture. [RES-002](../research/RES-002-fpp-multisync-compatibility.md) remains the position-feed record.

## Supersession

Narrows ADR-017 decisions on local playback and on FPP's audience output. Does not touch ADR-018, [ADR-019](ADR-019-audio-device-loss-fails-silent.md), [ADR-045](ADR-045-multi-node-audio-and-roles.md), or [ADR-046](ADR-046-rate-lock-to-a-shared-clock-is-not-chasing.md). Superseded by nothing.
