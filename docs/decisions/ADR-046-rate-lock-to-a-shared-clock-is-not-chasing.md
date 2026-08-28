# ADR-046: Rate-Locking an Audio Interface to a Shared PTP Clock Is Not Chasing

Status: Accepted (owner, 2026-08-28)  
Date: 2026-08-28

## Context

[ADR-017](ADR-017-showmesh-owns-audience-audio.md) says "playback-rate manipulation is avoided" and rejects "following a continuous time-position feed with rate correction, the way an FPP remote follows MultiSync". [ADR-018](ADR-018-program-and-ltc-share-a-clock-domain.md) rejects "correcting in software" because "the correction would have to be continuous rate adjustment or repeated seeks". [AUDIO-ENGINE.md](../architecture/AUDIO-ENGINE.md) §4.2 turns that into policy: "avoid audible playback-rate manipulation; never chase small timing differences continuously." ARCHITECTURE §5 and Track C repeat it.

Every one of those sentences was written from one observation: the owner's 2025 test of FPP MultiSync driving audio on a remote FPP node, where every catch-up sounded like a skipping CD. That observation is real and the rule it produced is right about what it saw. What it saw was a node chasing a *position feed* (MultiSync's `SecondsElapsed`, delivered at packet cadence and quantized to frames) with frame-sized slews and half-second jumps. That is a control loop with a noisy set point, a coarse actuator, and no shared clock. The rule was never tested against the other kind of correction, because no shared clock existed to test it with.

[RES-019](../research/RES-019-ptp-synchronized-multi-node-audio.md) (2026-08-28) found that the other kind of correction exists, is standard practice, and is how every AES67, RAVENNA, and PipeWire node keeps a sound card on a network clock: a ppm-scale trim of the resampling ratio, driven by a slow control loop against a PTP-disciplined clock, clamped to a few hundred ppm and slewed by a few ppm per buffer. FPP master shipped exactly this on 2026-08-27 (`driftResample`, `d081180`) after measuring a 56 ppm sound-card-versus-PHC difference; zita-ajbridge has done it for years for "sound cards which do not have a common word clock"; PipeWire does it to every ALSA follower node by default. None of these steps, slews, or seeks. RES-019 also found that the audio sink's own slaving in GStreamer does step (20 ms at a time), so leaving the problem to the sink would reproduce the skipping CD; the rule was protecting against something that is still real, just not the thing this record permits.

Multiple audio nodes now exist ([ADR-045](ADR-045-multi-node-audio-and-roles.md)), and two free-running interfaces separate by tens of ppm, which is milliseconds per minute. Cue-boundary realignment covers separate zones and does not cover outputs a listener hears together, or any AES67 output, which needs a PTP-referenced media clock by definition.

## Decision

1. **Three corrections are distinct, and the rule applies to two of them.**
   - **Rate trim**: a continuous, bounded, slew-limited adjustment of the resampling ratio of the whole interface output stream, on the order of parts per million, driven by the error between a locked shared media clock and the interface's measured sample clock. **Permitted**, under the conditions in decisions 2 to 4.
   - **Slew**: a playback-rate change of the magnitude MultiSync uses for pixels (frames per second, percent-scale) to catch a position feed. **Still forbidden** for program audio.
   - **Seek or jump**: a discontinuity. **Still forbidden as ordinary correction.** It remains the handling for start, operator commands, a PTP clock step, a node restart, a device change, and a position error beyond the discontinuity threshold, and it is reported as a resync event every time.

2. **Rate trim targets a clock, never a position feed.** The set point is the shared PTP-domain media clock the node's clock provider reports as locked. The FPP show timeline (MultiSync position) remains what audio aligns to at cue start and at explicit correction points, exactly as ADR-017 says; it is never the input to the rate loop. A node whose provider is not locked runs no trim and behaves as ADR-017 and AUDIO-ENGINE §4.2 describe today.

3. **Rate trim applies to the interface, not to a signal.** The trim acts on the interleaved stream after mixing and before the sink, so program and LTC leave the interface with the same correction and stay sample-locked to each other. ADR-018 is unchanged: program and LTC still leave one hardware interface and one clock domain. This record makes that domain follow the network clock; it does not split it.

4. **Bounded, observable, and revocable.** The trim is clamped, slew-limited, frozen on a clock step or loss of lock, and reported as `node.audio.rate.*` telemetry (RES-019 §10) so an operator can see it. The clamp and slew values are configuration whose first values come from RES-019 stage 3, not constants in this record. If the trim proves audible on the reference interfaces (RES-019 H1 to H3), it is disabled by policy and this record is revisited; the correction is never made larger to compensate.

5. **The correction may live in PipeWire or in the ShowMesh pipeline.** Either PipeWire's PTP-clocked driver rate-matching an ALSA follower (RES-019 candidate A) or a stock variable-ratio resampler in the GStreamer pipeline driven by property updates from ShowMesh's loop (candidate B) satisfies this record. Neither puts sample generation in Go; [ADR-007](ADR-007-gstreamer-media-engine.md) and [ADR-042](ADR-042-cgo-in-the-native-agent.md) still govern.

## Consequences

- ADR-017's "playback-rate manipulation is avoided" is narrowed to "slew is avoided; rate trim against a locked shared clock is permitted". ADR-017's rejection of following a position feed stands in full.
- ADR-018's rejected alternative, "allowing independent clocks and correcting in software", stands for what it rejected: two signals on two interfaces reconciled by software. One interface carrying both signals, rate-locked as a whole to a shared clock, is what this record permits.
- AUDIO-ENGINE §4.2's policy list is amended in place; §16 no longer lists "dynamic clock-rate correction" as deferred without qualification; ARCHITECTURE §5's timing-coupling sentence is amended. Track C's principles list carries a dated note.
- `audio.settings.driftIgnoreThresholdMs`, stored and unread today, becomes the discontinuity threshold of decision 1 once RES-019 stage 2 ships.
- A node advertises whether it can rate-lock (provider locked plus a trim mechanism present). Placement may prefer such nodes for outputs that must play together; it must not refuse a node that cannot, because cue-boundary realignment remains a valid degraded mode.
- Nothing in this record changes the Day-0 show path. The trim ships only through RES-019's staged plan, after its measurements.

## Alternatives considered

**Keep the ban and rely on cue-boundary realignment.** Rejected. It leaves two overlapping outputs drifting milliseconds per minute inside a cue and makes AES67 impossible, and the ban's evidence was about a different mechanism.

**Permit slews too, with a small bound.** Rejected. A slew is a step in rate the listener can hear as pitch or tempo; the whole point of a ppm trim through a resampler is that it is below that threshold. Keeping the two words separate is what keeps the skipping CD out.

**Let the GStreamer audio sink slave to the pipeline clock.** Rejected on source evidence: `slave-method=skew` steps the playout pointer by half of `drift-tolerance` (20 ms default) and `slave-method=resample` performs no resampling (RES-019 §4.4). That would be the skipping CD again.

**Correct by periodic seek at a fine interval.** Rejected; it is the seek-as-ordinary-correction that decision 1 forbids.

## Related research

[RES-019](../research/RES-019-ptp-synchronized-multi-node-audio.md) owns the measurements this record depends on: the drift curve between two real nodes, the audibility of the trim on the reference interfaces, LTC reader behavior under trimming, and the achievable lock. [RES-007](../research/RES-007-audio-node-architecture.md) keeps the program-to-LTC alignment question. [RES-002](RES-002-fpp-multisync-compatibility.md) remains the record for the position feed this record refuses to chase.

## Supersession

Narrows ADR-017 and clarifies ADR-018 without superseding either. Amends AUDIO-ENGINE §4.2 and §16 and ARCHITECTURE §5. Superseded by nothing.
