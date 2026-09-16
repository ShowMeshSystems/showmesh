# ADR-049: A Cue's Audio Plays on Several Nodes at One Instant

Status: Accepted (owner, 2026-09-15)
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

## Consequences

- A second node playing the show's audio in step with the M4 needs only the
  Cue's `targets` to list both nodes.
- Cross-node alignment is start alignment on the shared PTP clock plus
  ADR-046's rate lock. No continuous position correction between nodes is
  added.
- The Cue editor, `showmeshctl`, the OpenAPI description, the Cue catalog, and
  readiness change with the field.

## Alternatives considered

**Keep one target and author one Cue per node.** Rejected. An FPP Playlist
entry activates one Cue, so a second Cue for the second node is never
activated.

**Start each node on arrival and rely on the rate lock.** Rejected. Arrival
times differ by network and broker delay, and the rate lock keeps that offset
forever instead of removing it.

## Related decisions

- [ADR-045](ADR-045-multi-node-audio-and-roles.md): decision 1 superseded for
  audio and announcement outputs by decision 1 here.
- [ADR-046](ADR-046-rate-lock-to-a-shared-clock-is-not-chasing.md): the rate
  lock this record's alignment depends on.
- [ADR-018](ADR-018-program-and-ltc-share-a-clock-domain.md): unchanged.
