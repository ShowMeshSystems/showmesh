# ADR-061: Render Nodes Play the Resting Sequence

Status: Accepted (owner, 2026-10-17)
Date: 2026-10-17

## Context

A resting sequence can carry virtual matrix content, the same way a show
sequence does. The owner's resting sequences from last season do. During a
show, render nodes draw that content. During resting, they draw black.

The cause is that nothing tells a render node about the resting sequence.
During a show, the coordinator matches FPP's playlist entry to a Cue and sends
the Cue's activation to each render node. The node opens the Cue's FSEQ,
checks its content hash against the catalog, and follows FPP's MultiSync
position. Resting has no Cue. The night controller starts the resting FPP
playlist and reads the resting FSEQ only for its length
(`resting.timelineAsset`, [RESTING-MODE.md](../architecture/RESTING-MODE.md)
section 6.1). When FPP switches to the resting FSEQ, the render node's frame
writer sees a filename it does not hold and draws black on purpose
(`internal/agent/pipeline/frame.go`).

The recorded design treats resting as FPP-only lighting.
[ADR-043](ADR-043-show-scoped-cues-and-playlist-authority.md) describes FPP
running "a resting lighting sequence" beside ShowMesh's background audio, and
RESTING-MODE.md section 9 makes resting projections Resolume logical actions.
That matches how the owner runs Christmas, where projection comes from
Resolume. It does not match a display whose resting sequences carry matrix
content.

Selecting the file by name is not available. ADR-043 decision 6 forbids it,
because two shows can hold sequences with the same name and a filename says
nothing about which version of the file a node holds.

## Decision

### 1. The resting sequence renders wherever a node holds it

During resting, every render node that holds an FSEQ asset for the resting
sequence draws it on the surfaces assigned to it. The resting sequence is the
`sequence` named by `resting.timelineAsset`, in the active show.

A node holds the asset when the asset store has an FSEQ for that show and
sequence targeted at that node, under
[ADR-028](ADR-028-show-asset-store-and-identity.md)'s identity rules. There is
no list of render targets to configure. Uploading the sequence for a node is
what makes the node render it.

`resting.timelineAsset.target` keeps its current meaning: the FPP player's copy
of the file, read for the resting length. It does not choose render nodes.

### 2. The file is chosen by asset identity and verified by hash

The coordinator adds the resting sequence to each such node's resolved catalog
with its runtime filename and content hashes, as it does for a Cue's render
output. The node opens that file and checks its hash before it stops what the
surface is drawing. A file that is missing or fails the check leaves the
surface on its current output and reports why. FPP's reported filename
corroborates the selection and never makes it.

Because the entry is in the catalog, the file reaches the node before showtime
with the rest of the catalog, as ADR-043 decision 7 requires.

### 3. The coordinator starts it on evidence that resting is playing

When the night controller has post-dispatch evidence that the resting FPP
playlist is playing the resting sequence (RESTING-MODE.md section 6.2), the
coordinator sends each node from decision 1 a resting render activation. It
carries the active show, the catalog revision, and the resting entry, and the
node refuses it on the same grounds it refuses a Cue activation from another
show or an older catalog.

The coordinator may also name the resting sequence as the next sequence on the
activation of a show's last playlist entry, when the night controller will
start the resting playlist after that show. The node then switches on FPP's
MultiSync naming the file, without waiting for the coordinator, and the later
activation confirms it. This is the same queue a Cue uses for its next entry.

The node follows FPP's MultiSync position for the resting sequence exactly as
it does for a show sequence. The coordinator is not in the frame timing path.

### 4. Both resting playlists use the same rule

Decision 1 applies to `resting.playlist` between shows and to
`resting.endOfNightPlaylist` after the last show, when that playlist plays the
resting sequence. An end-of-night playlist that plays a different sequence is
not rendered, because no asset reference names it, and night readiness says
so.

### 5. Nothing new overrides a stop

A weather delay ([ADR-053](ADR-053-weather-delay.md)) and a level 1 hold or
higher emergency stop ([ADR-054](ADR-054-level-1-stop-holds-the-night.md))
black out or hold render surfaces exactly as they do for a Cue. A resting
render activation is refused while either is active. When resting ends, the
next show's first Cue activation replaces the resting sequence through the
ordinary Cue path. When FPP stops, the surface draws its configured idle
output, as today.

### 6. A show Cue that already renders the sequence wins

If a show Cue's render output already names the resting sequence, the resting
entry is left out of that node's catalog and the Cue path renders it. Night
readiness notes this. One file never has two render authorities on a node.

### 7. Resting render is optional

A night with no render node holding the resting sequence behaves exactly as it
does today. Night readiness lists, per render node, whether it will draw the
resting sequence, and a node that will not is informational, never degraded or
failed.

### 8. The resting sequence field says what it selects

The operator-facing label for `resting.timelineAsset` is "Resting sequence",
not "Resting target". Once this decision is built, its help text says, in one
or two sentences, that the selected sequence sets how long resting runs and is
drawn by any render node that has it. The API field names do not change. The
OpenAPI description of `timelineAsset` is updated to state both roles.

## Consequences

- Resting matrix content reaches render nodes and the dashboard previews,
  which show each render node's own output.
- Resting gains a render path that does not depend on Resolume. Resolume
  resting actions under RESTING-MODE.md section 9 still work and are
  unaffected.
- The asset store, not a new setting, decides which render nodes draw resting
  content. An operator who does not want a node to draw it does not upload the
  sequence for that node.
- A show switch invalidates the resting entry with the rest of the old show's
  catalog under ADR-043 decision 6.
- The node agent and coordinator gain one activation kind that is not a Cue.
  Its refusal rules are a Cue activation's rules.

## Alternatives considered

**Render whatever matching sequence the node holds.** Rejected. It selects by
filename, which ADR-043 decision 6 forbids, and it cannot tell which version
of the file the node holds.

**A separate list of resting render nodes.** Rejected. The asset store already
records which nodes have the file, and a second list can disagree with it.

**Make the operator wrap the resting sequence in a Cue and a show playlist.**
Rejected. The night controller, not a show playlist, owns resting, and the
binding would duplicate configuration the night session already holds.

**Keep resting FPP-only and drive resting video through Resolume.** Rejected as
the only path. It is right for a display that projects from Resolume and wrong
for one whose resting sequences carry matrix content.

## Related decisions

- [ADR-043](ADR-043-show-scoped-cues-and-playlist-authority.md): **narrowed**.
  Its description of resting as FPP-only lighting no longer holds for render
  nodes that hold the resting sequence. Decisions 6 and 7 apply unchanged.
- [RESTING-MODE.md](../architecture/RESTING-MODE.md): sections 6.1 and 9 are
  extended by this decision.
- [ADR-028](ADR-028-show-asset-store-and-identity.md): the asset identity that
  decides which nodes hold the sequence.
- [ADR-053](ADR-053-weather-delay.md), [ADR-054](ADR-054-level-1-stop-holds-the-night.md):
  the states that override resting render.
- [ADR-058](ADR-058-bed-resumes-on-the-resting-sequence-start-packet.md): the
  night bed's resume on the same resting sequence start.
