# ADR-051: A Cue's Audio Starts on FPP's MultiSync START Packet

Status: Accepted (owner, 2026-09-18)
Date: 2026-09-18

## Context

[ADR-049](ADR-049-same-audio-on-several-nodes-at-one-instant.md) decision 3
starts a multi-node Cue's audio at one instant the coordinator chooses, and
decision 6 reads the instant once per activation after a real prepare on the
slowest target. As built, that instant is chosen after the coordinator has
observed that FPP already started the sequence. Measured on the rehearsal rig
on 2026-09-17, from the coordinator's own command records, the audio started
10.5 s after the `wake-up` sequence and 14.2 s after `kpop-audio`, and from the
beginning of the file, so it stayed that far behind the lights for the whole
song. The lead is the sum of the observation delay, a probe round that loads
the media on every node to read its clock (2.5 s to 3.7 s on the Raspberry Pi
3B+), the configured delivery bound and margin, and a second load for the real
prepare. The activation's `positionMs` carries the FPP playlist slot index, not
media time, so nothing could seek forward even if the lead were known.

The owner ruled that audio starting after the lights is unacceptable, and so is
asking the operator to author the delay into the show. The start must follow
the trigger FPP's own remotes use.

That trigger exists. FPP sends a MultiSync OPEN packet when it opens a
sequence and a START packet when it starts it, back to back, and its remotes
start on START ([RES-002](../research/RES-002-fpp-multisync-compatibility.md)).
The node agent already runs the MultiSync listener for the render timeline and
answers discovery pings (`internal/agent/multisync.go`, `pkg/multisync`).
[ADR-008](ADR-008-mqtt-control-plane.md) says real-time timing never traverses
MQTT and MultiSync remains the timing path. [ADR-046](ADR-046-rate-lock-to-a-shared-clock-is-not-chasing.md)
says the FPP timeline remains what audio aligns to at cue start, and forbids
only chasing its position feed afterwards. [ADR-017](ADR-017-showmesh-owns-audience-audio.md)
rejects following the feed with rate correction; it does not reject starting
on it.

A second cost is the media itself. A node prepares an MP3 in 1.4 s on the x86
node and 2.4 s or more on the Pi, and never learns its duration, so the item
boundaries ADR-049 decision 8 schedules cannot be checked against a file's real
length. PCM has a known length and seeks by byte offset.

## Decision

### 1. A Cue's audio starts on the sequence START packet, on every node

When a node receives a MultiSync START packet for a sequence filename its Cue
catalog names as a trigger for a Cue with an audio output, it starts that Cue's
audio itself. The start instant is packet arrival plus a fixed lead on the
node's media clock, and the start position is the packet's frame number times
the sequence step time plus the Cue's `startOffsetMillis`. Every target node
receives the same packet, so every node computes the same instant on the shared
PTP clock, and the existing scheduled start path presents the first sample at
it. The lead is an `audio.settings` value with a default of 100 ms; the rig
measurement in [RES-020](../research/RES-020-multisync-triggered-cue-start.md)
sets it.

No coordinator round trip is in the start path. Media START packets are
ignored: their elapsed field is always zero (RES-002). A node whose clock
provider is not locked starts on arrival and says so, as today.

### 2. The catalog carries the trigger

Each Cue catalog entry lists the FPP sequence filenames that start it, resolved
from the bound FPP show playlist entries that name the Cue and from the Cue's
own render output. Two Cues in one show may not claim the same filename. A Cue
with an audio output and no trigger is a readiness warning: it will start from
the fallback in decision 4.

### 3. Audio is prepared ahead, and a cold start prepares on OPEN

The coordinator arms the current entry's Cue and the next entry's Cue on every
target node ahead of time, and a node keeps an armed Cue prepared until it
plays or is superseded. A sequence started by hand, from any playlist, is not
armed: the node prepares on the OPEN packet and starts on START, and reports
that it started late by the prepare time. A one-off test therefore plays its
audio without a night session and without the coordinator in the path, late by
one prepare.

### 4. The coordinator's chosen instant is the fallback

ADR-049 decisions 3 and 6 now define the fallback start only. If the
coordinator sees no MultiSync start reported for an activation within 1.5 s of
the FPP entry observation, it dispatches the start as it does today, on
arrival, and records the activation as unaligned with the reason that no START
packet arrived. A coordinator start that arrives after a node already started
on the packet is a confirmation, never a second start. Readiness warns when the
bound FPP instance has MultiSync disabled or a target node is missing from its
MultiSync systems list.

The night bed and night announcements keep ADR-049 decisions 7 to 9 unchanged.
No FPP packet exists for them.

### 5. Show audio is delivered to nodes as PCM

The coordinator keeps the operator's upload as the original and produces a
48 kHz, 16-bit, stereo WAV rendition at upload. Asset sync delivers the
rendition, nodes verify the rendition's hash, and the rendition's duration is
stored with the asset. Existing assets are renditioned after upgrade without
operator action.

### 6. Every start records how it started

A node's audio session report carries the trigger source (MultiSync or
coordinator), the packet arrival time, the lead used, and whether prepare ran
late. The per-node Cue outcome, the API, `showmeshctl`, and the Operator UI
show them.

## Consequences

- The start path for a Cue is FPP, the LAN, and the node. The coordinator and
  the broker leave it, which is what ADR-008 already required.
- The coordinator's probe round leaves the Cue path. The aligned-start endpoint
  and the night paths keep it.
- A Cue started by hand plays its audio, late by one prepare on a cold node and
  on time on an armed one.
- A show player that cannot reach the nodes with MultiSync (FPP 10 unicasts
  only to discovered remotes; a containerized player must be on the host
  network) gets the fallback and a readiness warning naming why.
- Disk on a node grows to about 10 MB per minute of show audio.

## Alternatives considered

**Seek the audio forward by the measured lead at the coordinator's instant.**
Rejected by the owner. The first 10 to 14 seconds of every song would never be
heard.

**Shrink the coordinator's lead by making the clock read cheap.** Not enough.
The trigger is still an observation of a start that already happened, so the
audio is still late by the observation and dispatch path.

**Predict the entry start from the FPP schedule and schedule the audio for
it.** Not chosen. It needs a model of FPP's own timing and a second clock
mapping, and the START packet carries the same information with no model.

**Have the FPP plugin announce the next entry early.** FPP's `query_next` hook
fires at the moment FPP picks the next item, milliseconds before OPEN, so it is
not an early warning. The bound playlist order already gives the arming window.

**Keep MP3 delivery and prepare on OPEN only.** Rejected. A Pi 3B+ prepares an
MP3 in seconds, and the duration stays unknown.

## Related decisions

- [ADR-049](ADR-049-same-audio-on-several-nodes-at-one-instant.md): decisions
  3 and 6 narrowed to the fallback by decision 4 here; decisions 7 to 9
  unchanged.
- [ADR-008](ADR-008-mqtt-control-plane.md), [ADR-017](ADR-017-showmesh-owns-audience-audio.md),
  [ADR-046](ADR-046-rate-lock-to-a-shared-clock-is-not-chasing.md): the rules
  this record follows.
- [ADR-013](ADR-013-no-fpp-control-port-sharing.md): a node that starts on
  MultiSync must not share a host with an FPP player.
- [RES-002](../research/RES-002-fpp-multisync-compatibility.md): the packet
  lifecycle and the media START elapsed rule.
- [RES-020](../research/RES-020-multisync-triggered-cue-start.md): the
  measurements this record's lead and threshold depend on.
