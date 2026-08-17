# ADR-040: The Renderer Extracts Channels; GStreamer Owns Every Frame the Audience Sees

Status: **Accepted** by the owner, 2026-08-17. Drafted the same day by the Track B
orchestration session, which left it Proposed because an unattended session drafts
an ADR and never accepts one.
Date: 2026-08-17

**The owner's reasoning on accepting it, recorded because it is a statement about
how this project draws lines and not only about this one.** ADR-007's per-frame
prohibition was written on 2026-08-10, before anyone understood the shape of the
renderer. Setting the strict line first and then finding out where it has to be
crossed was the cheaper order: the boundary was known and defended from day one,
and crossing it required naming the crossing. That is the project's general
pattern. The intentions are firm; what the real world permits is discovered by
building, and a constraint that has to be narrowed once evidence arrives was still
doing its job the whole time it stood unnarrowed.

## Context

[ADR-007](ADR-007-gstreamer-media-engine.md) makes the node agent supervise
GStreamer for all media execution and then draws a line:

> ShowMesh-owned code is limited to: pipeline construction per assignment,
> MultiSync-derived clock/position control (seek, rate slew), readiness
> probing, health/stats extraction (buffer levels, QoS messages, underruns),
> and restart policy. **ShowMesh must not implement codecs or per-frame
> rendering in application code.**

Track B is the first work that has to build against that sentence, and it does
not fit cleanly, for a reason nobody had in front of them when ADR-007 was
written on 2026-08-10.

**No GStreamer element reads an FSEQ file.** The virtual-matrix renderer's
input is a Falcon Player sequence file: a compressed, optionally sparse,
channel-major recording of lighting channel values. Its output is pixels. There
is no decoder, no plugin, and no upstream project that bridges the two. So
something inside ShowMesh has to open that file, seek to a frame, and turn
channel bytes into a pixel buffer, and there is no arrangement of existing
GStreamer elements that avoids it.

That makes the ADR-007 sentence ambiguous in exactly the place Track B needs it
to be precise. Read narrowly, "per-frame rendering in application code" forbids
the only implementation that exists. Read loosely, it forbids nothing, because
any work can be described as data preparation.

This record exists to fix the boundary in one place rather than letting each
seam re-decide it, which is [ADR-026](ADR-026-renderer-surface-model-and-reference-transport.md)'s
own argument for existing.

## Decision

### 1. ShowMesh extracts channel data; GStreamer produces frames

ShowMesh-owned code in the renderer path is limited to:

- opening and parsing the node-local FSEQ,
- locating and decompressing the block containing a given frame,
- extracting the surface's assigned channel range from that frame, and
- writing one raw buffer at the surface's own geometry into the pipeline.

Everything downstream of that buffer belongs to GStreamer: scaling to canvas
dimensions where the geometries differ, colour-space conversion, frame pacing
against the pipeline clock, encoding, and transport.

**The test:** every frame the audience sees is produced inside GStreamer.
ShowMesh never transforms, composites, or encodes a frame — at any resolution,
including one equal to the canvas. See decision 2: the test is on the verb,
not on the dimensions.

### 2. The boundary is the kind of work, not the size of it

**Corrected 2026-08-17 by the owner, before this record was ruled on.** An
earlier draft of this decision drew the line at resolution: matrix-sized
buffers were inside, canvas-resolution surfaces were outside and would need
this record revisited. **That is wrong, and it would have forbidden the thing
the project exists to do.**

The owner's statement: last season ran on Raspberry Pi FPP players that could
not render 1080, **that limitation is part of why ShowMesh exists**, and the
intent is higher-resolution matrix panels. **ShowMesh should support any
matrix size.** Finding the ceiling is real work, but it is not now.

So the boundary is what ShowMesh *does*, not how much of it:

- **ShowMesh may**: locate, decompress, and copy channel bytes. Work that is
  linear in the byte count and involves no interpretation of a byte as a
  pixel.
- **ShowMesh may not**: scale, convert colour spaces, composite, blend,
  encode, or pace. Anything that requires knowing what a pixel *means*.

A 6 MB buffer copied per frame is still a copy. A 480 KB buffer scaled and
colour-converted in Go is a renderer, and it is the wrong side of the line
however small it is. Size is a **performance parameter owned by
[RES-004](../research/RES-004-virtual-matrix-renderer-performance.md)**, not
an architectural boundary, and a size ceiling that turns out to exist is a
measurement to record rather than a decision to revisit.

**The numbers available today**, for scale rather than as a limit. The owner's
real show FSEQ implies **480,000 channels, 160,000 pixels, about 480 KB per
frame at RGB, 19 MB/s at 40 fps** per matrix — and he describes that as
*small*, constrained by the Pi hardware being replaced. A 1920x1080 matrix
would be 6,220,800 channels, about 6.2 MB per frame and 249 MB/s. Both are
inside decision 2 as now written. Neither has been measured end to end.

**One consequence worth stating**, because it simplifies a question this track
had flagged as open: if a matrix is authored at or near canvas resolution,
**the scaler largely disappears**, and the pipeline gets closer to the
zero-conversion path B0 actually measured rather than further from it. The
open question about where scaling and conversion are cheapest is therefore
sharpest at *small* matrix sizes, not large ones, which is the opposite of the
intuition the earlier draft was built on.

### 3. The pixel format of that buffer is a pipeline-spec field, decided by measurement

The format ShowMesh writes is not fixed here. It is an explicit field of the
pipeline specification and is hardcoded nowhere.

B0 measured the reference pipeline encoding at **86% of one core with no queue
present**, on a **zero-conversion path at canvas dimensions**. That result
argues for emitting the sink's native format directly.

But the spike's source did no scaling, and the renderer's may. **How much this
matters depends on the surface geometry the operator chooses**, and per
decision 2 that is deliberately unconstrained: a matrix authored at canvas
dimensions needs no scaler at all and lands on exactly the path B0 measured,
while a small matrix needs a real upscale. So the format decision is not one
answer, it is a function of the surface, which is precisely why it belongs in
the pipeline spec rather than in this record.

Fixing a format in this record would be the answer appearing before the work
that produces it, which is the failure
[ADR-026](ADR-026-renderer-surface-model-and-reference-transport.md) decision 5
exists to name.

### 4. Thread boundaries are chosen, not inherited

A `queue` between pipeline stages is part of the pipeline specification, not a
tuning detail applied when something proves slow.

B0's 86% was one core while the rest of the machine sat near idle, because
generation, encode and send shared a single streaming thread. **The ceiling is
per-core, not per-machine.** A renderer hung off the front of that pipeline
shape inherits what is left of one core, which is not a budget anyone chose.

This belongs in a decision record rather than in the pipeline builder because
it is invisible in a passing test: a pipeline with no queues works correctly,
at a frame rate nobody measured against a target nobody stated.

### 5. The escape hatch is ADR-007's, unchanged

If measurement shows this boundary cannot meet the profile — the extraction
cannot feed the pipeline fast enough, or the pipeline cannot pace what it is
fed — ADR-007's existing exit criterion applies: a custom renderer for that
capability requires a superseding ADR. This record does not create a new one.

## Consequences

- The renderer's cost is dominated by decompression and a bounded copy, both of
  which are measurable in isolation and belong in
  [RES-004](../research/RES-004-virtual-matrix-renderer-performance.md) as
  measurements rather than estimates.
- **Decision 2 creates a measurement obligation, not a guard.** Because size is
  a performance parameter rather than a boundary, nothing needs to refuse a
  large surface. What is needed is that the achieved frame rate at the
  configured geometry is reported as evidence, so an operator who authors a
  matrix the hardware cannot sustain finds out from the dashboard rather than
  from the wall. Today nothing reports it.
- **The maximum sustainable matrix size is unknown and is deliberately not
  being established now** (owner, 2026-08-17). It is RES-004's work when the
  renderer exists to measure.
- ADR-007's sentence stays intact and is narrowed by interpretation, not
  superseded. If the owner rejects this reading, ADR-007 is unchanged and Track
  B needs a different implementation, of which none is currently known.
- Fixing the format in decision 3 as a spec field means the pipeline
  specification carries a decision that has not been made, which is deliberate
  and is why decision 3 says so out loud rather than defaulting quietly.

## Alternatives considered

**Reading ADR-007 narrowly and building no renderer.** Rejected as
self-defeating: it forbids the only implementation that exists and would cut
one of the three founding problems, which BUILD-PLAN says may not be cut.

**A custom GStreamer element that reads FSEQ, in C or Rust.** This is the
reading of ADR-007 that needs no interpretation at all, and it is genuinely the
architecturally cleanest answer: the extraction would sit inside GStreamer,
where ADR-007 says media work belongs. Rejected for day-0 on cost and risk, not
on principle. It introduces a second language, a build toolchain on every node,
and a plugin that must be compiled per platform — on a node that **already**
carries one source-built plugin because the NDI element is not packaged on
Debian 13. It is the right answer if this boundary later proves wrong, and it
should be the first thing considered at that point rather than a bigger Go
renderer.

**Treating the extraction as "readiness probing" or "health extraction"** to
fit it under ADR-007's existing allowlist. Rejected as dishonest. It is neither,
and stretching a list until the new thing fits is how a constraint stops
constraining.

**Saying nothing and letting each seam decide.** Rejected: the boundary would
be re-derived per seam, differently, and the first canvas-resolution surface
would cross it without anyone noticing. That is the failure mode decision 2
exists to make loud.

## Related

[ADR-007](ADR-007-gstreamer-media-engine.md) ·
[ADR-026](ADR-026-renderer-surface-model-and-reference-transport.md) ·
[ADR-028](ADR-028-show-asset-store-and-identity.md) ·
[RES-004](../research/RES-004-virtual-matrix-renderer-performance.md) ·
[RES-017](../research/RES-017-fseq-format.md) ·
[Track B build contract](../build/TRACK-B-BUILD-CONTRACT.md)

## Supersession

Supersedes nothing. **Narrows** ADR-007 by interpreting its per-frame
prohibition, in the same way ADR-026 narrowed ADR-005 without superseding it.
ADR-007's substantive rules — GStreamer owns media execution, ShowMesh owns
supervision and health, no codecs in application code — are unchanged and still
binding.
