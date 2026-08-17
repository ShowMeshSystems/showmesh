# ADR-040: The Renderer Extracts Channels; GStreamer Owns Every Frame the Audience Sees

Status: **Proposed** — drafted 2026-08-17 by the Track B orchestration session
for the owner to rule on. **Not accepted.** An unattended session drafts an ADR
and never accepts one.
Date: 2026-08-17

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
- writing one **matrix-sized** raw buffer into the pipeline.

Everything downstream of that buffer belongs to GStreamer: scaling to canvas
dimensions, colour-space conversion, frame pacing against the pipeline clock,
encoding, and transport.

**The test:** every frame the audience sees is produced inside GStreamer.
ShowMesh never holds, transforms, composites, or encodes a canvas-resolution
frame.

### 2. The buffer ShowMesh writes is matrix-sized, and that is a hard boundary

The buffer is `surface.geometry.width` by `surface.geometry.height`, the
virtual-matrix dimensions, not the output canvas dimensions.

This is the load-bearing half of the decision and the half that can be
violated silently. At virtual-matrix geometry the buffer is small and the
per-frame cost is a bounded memory copy. At canvas geometry it is megabytes per
frame at 40 fps, ShowMesh is squarely in the media path, and decision 1's
reasoning no longer holds.

**A surface authored at canvas resolution therefore falls outside this
record**, and if one is ever wanted, this decision must be revisited rather
than stretched. The distinction is not stylistic: it is the difference between
a data transform and a video pipeline.

### 3. The pixel format of that buffer is a pipeline-spec field, decided by measurement

The format ShowMesh writes is not fixed here. It is an explicit field of the
pipeline specification and is hardcoded nowhere.

B0 measured the reference pipeline encoding at **86% of one core with no queue
present**, on a **zero-conversion path at canvas dimensions**. That result
argues for emitting the sink's native format directly. But the spike's source
did no scaling, and the renderer's does: a small matrix must be scaled up.
Scaling and conversion have to happen somewhere, and where they are cheapest is
an open measurement, not a settled fact.

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
- **Decision 2 creates an obligation nothing currently discharges**: something
  must notice a surface whose geometry approaches canvas dimensions and say so,
  because the boundary is silent when crossed. Today nothing does.
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
