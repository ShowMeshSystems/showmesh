# ADR-058: Between Shows, the Night Bed Resumes on the Resting Sequence's MultiSync START Packet

Status: Proposed
Date: 2026-09-29

## Context

[ADR-051](ADR-051-cue-audio-starts-on-the-multisync-start-packet.md) moved a
Cue's audio start off the coordinator and onto FPP's MultiSync START packet.
Decision 4 kept the night bed on
[ADR-049](ADR-049-same-audio-on-several-nodes-at-one-instant.md) decisions 7 to
9, on the grounds that "No FPP packet exists for them."

That is not true between shows. When a show ends, the night controller starts
the resting FPP playlist, and FPP sends OPEN and START packets for its
sequence like any other.

The coordinator path is slow. Measured on the rehearsal rig on 2026-09-29 from
the coordinator's own command records, a two-node bed began its fade-up 8.6 s
after the show's audio stopped. That time has four parts:

- 2.6 s of 1-second night loop ticks between the end of the show and the bed
  step;
- 1.0 s probing the `program+ltc` node's clock;
- a 4.9 s scheduled lead (delivery bound 2 s, margin 1 s, and the probe's own
  time);
- 0.1 s before the fade-up command went out.

The resting playlist itself started 1.6 s after the show ended. A bed that
starts on its START packet would begin about 1.7 s after the show, on the same
instant as the resting lights.

Outside the show loop, the packet is the wrong trigger. The preshow bed can
start before any FPP playlist runs, and a bed may play all day before the first
show. Neither has a resting sequence to follow.

## Decision

### 1. Only a resume after a show uses the packet

Only the bed's resume after a show waits for the resting sequence's START. This
covers every resume between shows and the resume into the end-of-night resting
playlist after the last show. The bed's first start of the night, and any bed
start or resume outside the show loop, keep ADR-049 decisions 7 to 9 unchanged.

### 2. The coordinator arms the resume when the pause lands

When the bed's pause into a show has confirmed on the `program+ltc` node, the
coordinator sends every listed node an armed resume. It names:

- the bed session;
- the resume position, taken from the `program+ltc` node's bookmark as ADR-049
  decision 8 already requires;
- the bed's `fadeInMs`;
- the trigger filenames.

The trigger filenames are the sequence filenames of the FPP playlist the night
controller will start after the show: `resting.playlist` between shows, and
`resting.endOfNightPlaylist` after the last show. The coordinator reads them
from the playlist definitions it already reads for night readiness.

The node answers that it is armed. It does not start anything yet.

### 3. The first matching START resumes the bed, once

On the first sequence START packet whose filename is in the armed trigger set,
the node resumes the bed at packet arrival plus `multisyncStartLeadMs` on its
media clock. It resumes from the armed position, not from the packet's frame
number, and starts the armed fade-in ramp from the silence floor at that same
instant. The arm is then spent, so later loops of the resting playlist do not
restart the bed. Media START packets are ignored, as in ADR-051.

### 4. The coordinator's scheduled resume becomes the fallback

The coordinator may see the resting playlist playing and get no report from any
node that it resumed on the packet within `multisyncFallbackWindowMs`. It then
resumes the bed as it does today, under ADR-049 decision 8, and records the
resume as unaligned with the reason that no START packet arrived.

A coordinator resume that reaches a node already resumed on the packet is a
confirmation, never a second resume. A node whose clock provider is not locked
resumes on arrival and says so, as today.

### 5. Anything that keeps the bed off disarms it

An armed resume is cleared by:

- a stop, clear, or pause of the bed session that is not itself the armed pause;
- a weather delay ([ADR-053](ADR-053-weather-delay.md));
- a level 1 hold or any higher emergency stop
  ([ADR-054](ADR-054-level-1-stop-holds-the-night.md));
- the end of the night session.

A node restart loses its arm. The fallback in decision 4 then resumes the bed.

### 6. A trigger filename a show Cue also claims is not armed

A resting sequence filename that the show playlist's Cue catalog also claims
under ADR-051 decision 2 is left out of the trigger set, and night readiness
warns naming it. If no trigger filename is left, the resume uses the fallback,
and readiness says why.

### 7. Every resume records how it started

The bed's resume row and the node's session report carry the trigger source
(MultiSync or coordinator), the packet arrival time, and the lead used, the same
fields ADR-051 decision 6 adds for a Cue.

## Consequences

- Between shows, the bed follows the resting lights within about 100 ms, and the
  coordinator and the broker leave the resume's timing path, as ADR-008 requires
  of real-time timing.
- The coordinator's bed fade-up step becomes a confirmation when the node
  ramped on its own.
- A night with no resting FPP playlist, or one whose resting sequences are all
  claimed by show Cues, behaves exactly as it does today.
- A node needs the MultiSync listener and a place on the FPP player's MultiSync
  systems list for the bed, as ADR-051 already requires for Cues.
- The first preshow start keeps its current delay of about 7 s to the start of
  the fade-up. Shortening it is a separate question.

## Alternatives considered

**Trim the coordinator path and keep the scheduled resume.** Ruled out as the
answer, though the trim is still worth doing. Starting the resume on the same
tick as the show end and lowering the delivery bound and margin leaves the
fade-up about 4 s after the show. The trigger would still be an observation of
something that already happened.

**Use the packet for every bed start, including preshow.** Rejected by the
owner. The bed may start before FPP plays anything, or play all day before the
show loop, and then no packet exists to wait for.

**Start from the packet's frame number, as a Cue does.** Rejected. The bed is
not authored against the resting sequence, and ADR-049 decision 8 resumes it
from its own bookmark.

## Related decisions

- [ADR-049](ADR-049-same-audio-on-several-nodes-at-one-instant.md): decision 8
  becomes the fallback for a resume after a show; decisions 7 and 9 unchanged.
- [ADR-051](ADR-051-cue-audio-starts-on-the-multisync-start-packet.md): decision
  4's exclusion of the night bed is narrowed to bed starts and resumes outside
  the show loop.
- [ADR-008](ADR-008-mqtt-control-plane.md): real-time timing stays off MQTT.
- [ADR-053](ADR-053-weather-delay.md), [ADR-054](ADR-054-level-1-stop-holds-the-night.md):
  the states that must keep the bed off.
- [RES-002](../research/RES-002-fpp-multisync-compatibility.md): the packet
  lifecycle and the media START rule.
- [RES-020](../research/RES-020-multisync-triggered-cue-start.md): the lead
  measurement `multisyncStartLeadMs` rests on.
