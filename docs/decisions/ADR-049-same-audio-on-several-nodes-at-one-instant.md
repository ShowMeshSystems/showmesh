# ADR-049: A Cue's Audio Plays on Several Nodes at One Instant

Status: Accepted (owner, 2026-09-15; decisions 6 to 9 added 2026-09-16); decisions 3 and 6 narrowed to the fallback start by [ADR-051](ADR-051-cue-audio-starts-on-the-multisync-start-packet.md) (2026-09-18)
Date: 2026-09-15

## Context

[ADR-045](ADR-045-multi-node-audio-and-roles.md) decision 1 gave each Cue
output one optional `target` node. As built, a Cue's audio reaches exactly one
node: the named target, or, when the target is absent, the installation's
`program+ltc` node, or its sole `audio.node` when no node holds that role
(`internal/coordinator/assetsync/audiotarget.go`). Cue
activation on that node starts playback when the command arrives
(`internal/agent/cueactivationaudio.go`).

The point of running more than one audio node was to play the same show audio
from several outputs at the same time. The clock work for that exists: nodes
run their pipelines on a PTP hardware clock ([ADR-046](ADR-046-rate-lock-to-a-shared-clock-is-not-chasing.md)),
and `POST /audio/sessions/{sessionId}/aligned-start` picks one start instant
from the `program+ltc` node's media clock and starts a list of nodes at it
(`internal/coordinator/api/alignedstart.go`). Nothing in a show uses it. Only
an operator calling that endpoint by hand gets an aligned start.

## Decision

### 1. A Cue's audio and announcement outputs name a list of target nodes

`show.cue.outputs.audio` and `outputs.announcement` carry `targets`, a list of
`audio.node` ids. The Cue's audio plays on every node in the list, and the Cue catalog includes
the Cue on every node in the list. Each id must
name a configured `audio.node`, and a repeated id is refused.

An absent or empty list resolves exactly as an absent `target` does today: to
the installation's `program+ltc` node, or to its sole `audio.node` when no node
holds that role. The existing single `target` field is
still accepted and means a one-element list; a Cue carrying both `target` and
`targets` on one output is refused.

This supersedes ADR-045 decision 1's one-target rule for these two outputs.

### 2. LTC stays on one node

`outputs.ltc` keeps its single optional `target`. [ADR-018](ADR-018-program-and-ltc-share-a-clock-domain.md)
and ADR-045 decision 2 are unchanged: one LTC generator, on the
`program+ltc` node, for the whole installation.

### 3. A Cue reaching more than one node starts at one instant

When a Cue activation reaches more than one node, the coordinator chooses one
start instant with the same selection the aligned-start endpoint uses
(`internal/coordinator/audiosched`), and every target node starts the Cue's
audio at that instant. The instant is chosen once per activation, never per
node. Only that selection is shared with the endpoint; the endpoint's handling
of one node's problem as a failure of the whole request is not, because
decision 4 forbids it.

The clock reading the selection uses comes from a result obtained for this
activation, never from a retained observation, for the same reason the
aligned-start endpoint refuses one: a retained reading looks current while
describing whenever the node last published.

Every target node receives the same playback position. On an unaligned start
each node begins at that position when the command arrives; no per-node
correction for arrival delay is applied, consistent with ADR-046.

A Cue reaching one node behaves as it does today.

The same one-instant rule applies to night mode when a start reaches more than
one node. Night mode already reaches several nodes without a schema change: a
background bed plays on every distinct `target` its items name, and an
announcement plays on every node in its bound `show.action`'s `audioNodeId`
list. This record does not change either stored shape; ADR-045 decision 5's
list-valued `night.session` shape remains unbuilt.

Amended 2026-09-16: decisions 7 to 9 build that shape for the background bed,
and extend the one-instant rule past the bed's first start to its item changes
and to every resume after a show.

### 4. Failure degrades to playing, never to silence

When no usable clock reading exists (no target holds `program+ltc`, or that
node's clock is not locked), every target node starts on arrival, and the
activation is reported as unaligned with the reason. A multi-node Cue whose
targets exclude the `program+ltc` node therefore always starts unaligned. One node refusing or
failing to start never stops the others; each node's outcome is reported
separately.

### 5. Every target node holds the asset before the show is ready

Asset sync delivers a Cue's audio asset to every node in its `targets`, and
Playlist readiness fails naming the node when any target lacks it. An unlocked
clock on a target is a readiness warning, not a failure, because decision 4
still plays the audio. A multi-node Cue whose targets exclude the
`program+ltc` node is also a readiness warning naming the Cue, because it can
never start aligned.

### 6. The start instant is read once per activation (amended 2026-09-16)

The clock reading behind decision 3 is taken when an activation is first
dispatched, and never again for that activation. A later tick that replays an
activation already sent reads no clock and dispatches nothing new.

The wait for that reading covers a real prepare on the slowest target. A fixed
wait shorter than the media's load time is not a bound on the start; it is a
guaranteed unaligned start. On the rehearsal rig on 2026-09-16, preparing a show
MP3 took about 1.4 s on the `program+ltc` node and 2.4 s on a Raspberry Pi 3B+,
against a 400 ms wait, and no multi-node Cue that day started aligned.

### 7. A night bed names its target nodes, and every listed node plays all of it (amended 2026-09-16)

`night.session`'s background audio carries `targets`, a list of `audio.node`
ids, whether its items are inline or come from a referenced `media.playlist`.
Every listed node plays every item, in the same order, with the same repeat,
resume, and transition settings. This is the list-valued shape ADR-045
decision 5 called for, and it covers the preshow bed and the postshow resting
bed alike.

An item's asset still names the file. The node that file was registered for no
longer decides where it plays. A listed node without its own copy receives the
registered copy, the same rule decision 5 applies to a Cue, and Night readiness
fails naming any listed node that still lacks an item's file.

A bed without `targets` keeps its existing behavior: each node plays the items
registered for it, and decisions 8 and 9 do not apply to it.

### 8. A multi-node bed starts, changes items, and resumes together (amended 2026-09-16)

When a bed's `targets` list more than one node:

- Its first start, and every resume after a show, use one instant chosen as
  decision 3 chooses one, from a reading taken for that start or resume.
- Every item change lands at the same instant on every node. The next item
  starts at the bed's shared start instant plus the durations of the items
  before it, read on each node's own media clock, not when that node's decoder
  happens to finish the previous item. A node that cannot have the next item
  ready by then reports it, and does not wait silently.
- On resume, every node resumes the same item at the same playback position:
  the position of the `program+ltc` node's paused bed. A node whose own
  bookmark differs starts from that item and position instead. A bed whose
  `resume` setting restarts it starts every node from its first item.

Pauses and fades into a show are not scheduled. They happen on arrival, and the
next resume puts every node back together.

Decision 4 applies unchanged. With no usable clock reading, or with a
`targets` list that excludes the `program+ltc` node, every node still plays on
arrival, and the report says the bed is unaligned and why.

### 9. A night announcement on several nodes has its file everywhere and starts together (amended 2026-09-16)

An announcement whose bound `show.action` lists more than one node in
`audioNodeId` has its file delivered to every listed node under decision 7's
registered-copy rule, fails Night readiness naming any listed node that lacks
it, and starts at one instant chosen as decision 3 chooses one, with decision
4's fallback. Each node's duck or interrupt of its own bed is unchanged.

## Consequences

- A second node playing the show's audio in step with the M4 needs only the
  Cue's `targets` to list both nodes.
- Cross-node alignment is start alignment on the shared PTP clock plus
  ADR-046's rate lock. No continuous position correction between nodes is
  added.
- The Cue editor, `showmeshctl`, the OpenAPI description, the Cue catalog, and
  readiness change with the field.
- The Night screen, `showmeshctl`, and the OpenAPI description gain the bed's
  `targets`, and Night readiness checks every listed node's files.
- A node keeps a multi-node bed together on its own between shows: item changes
  follow the shared start instant on the node's media clock, so the bed does not
  depend on the coordinator or the broker between one start or resume and the
  next.

## Alternatives considered

**Keep one target and author one Cue per node.** Rejected. An FPP Playlist
entry activates one Cue, so a second Cue for the second node is never
activated.

**Start each node on arrival and rely on the rate lock.** Rejected. Arrival
times differ by network and broker delay, and the rate lock keeps that offset
forever instead of removing it.

**Play the same bed list on each node independently.** Rejected by the owner on
2026-09-16. Each node loads files at its own speed, so nodes that start apart
stay apart, and nodes that start together drift apart at every item change.

**Have the coordinator start every bed item on every node.** Rejected. It puts
the coordinator and the broker in the timing path for the whole preshow, and
the show must keep playing when either is lost.

## Related decisions

- [ADR-045](ADR-045-multi-node-audio-and-roles.md): decision 1 superseded for
  audio and announcement outputs by decision 1 here.
- [ADR-046](ADR-046-rate-lock-to-a-shared-clock-is-not-chasing.md): the rate
  lock this record's alignment depends on.
- [ADR-018](ADR-018-program-and-ltc-share-a-clock-domain.md): unchanged.
