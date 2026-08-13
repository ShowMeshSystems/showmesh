# ADR-026: The Renderer Models Logical Surfaces, and NDI Is the Reference Transport

Status: Accepted  
Date: 2026-08-13

## Context

A research pass on 2026-08-13 settled the direction of four records that had
sat at `unresearched` since the design package was written: [RES-003](../research/RES-003-xlights-fpp-connect-compatibility.md)
(xLights and FPP Connect), [RES-004](../research/RES-004-virtual-matrix-renderer-performance.md)
(renderer performance), [RES-005](../research/RES-005-ndi-vs-hdmi-transport.md)
(NDI versus HDMI), and [RES-006](../research/RES-006-linux-ndi-support.md)
(Linux NDI support). Each record now carries an owner decision, and each
correctly records that decision as architecture intent rather than as evidence.

Those decisions were written directly into ARCHITECTURE §4.4 and §4.7. This
record exists because that is not where they belong. CLAUDE.md states the rule
three times over, and [ADR-002](ADR-002-capability-based-nodes.md) is the precedent
that makes it concrete: *nodes are modeled by capabilities, not hardware types*
is a modelling decision, and it got a record rather than a paragraph. **The
renderer models logical surfaces, not physical projectors** is the same shape of
decision about a different component, and a future contributor who wanted to
reintroduce a projector object would otherwise find nothing standing in the way
except prose in a component description.

**One prior sentence makes this urgent rather than tidy.** ARCHITECTURE §4.4
previously read:

> One logical surface per projector is the preferred authoring model **unless
> performance tests show that a combined canvas provides a material advantage.**

That conditional named RES-004's bench as the thing that would settle the
question. RES-004 has not run, and remains L0 by its own status line. The
question has now been settled anyway, by the owner, on grounds that are
perfectly legitimate but are not the grounds the sentence promised. Deleting the
conditional without recording that substitution would leave the architecture
spec asserting, in its own voice and with no qualifier, a conclusion its earlier
self said would come from a measurement nobody took.

### The evidence posture, stated before the decisions rather than after

Everything in this record is **L0 design intent**. No renderer has been built,
no frame has been sent to Resolume from ShowMesh code, and no performance figure
here has been measured. That is an acceptable basis for an ADR in this project
and there is direct precedent: [ADR-017](ADR-017-showmesh-owns-audience-audio.md),
[ADR-018](ADR-018-program-and-ltc-share-a-clock-domain.md), and
[ADR-019](ADR-019-audio-device-loss-fails-silent.md) were all accepted against
RES-007 at critical-risk L0, on the reasoning that implementation cannot start
against an undecided authority boundary. The same reasoning applies here.

What that precedent does **not** license is an architecture document that reads
as though the evidence exists. Decision 5 is where this record spends most of
its weight, and it is the one most likely to be read as pedantry and deleted.

## Decision

### 1. The renderer models logical surfaces, not physical projectors

A renderer node supports one or more independent **logical surfaces**. A surface
owns its canvas, its extraction of assigned virtual-matrix channels, and its
output transport or stream.

**No projector object exists in the renderer model.** A surface may feed one
projector, or a single combined surface may feed a projector pair downstream in
Resolume or in the physical video path. That mapping is deployment
configuration, Resolume composition, and cabling. It does not enter the renderer
object model, and a surface does not know how many projectors it eventually
lands on.

This is [ADR-002](ADR-002-capability-based-nodes.md)'s argument applied to a second
component. Modelling the physical device makes the model wrong every time the
physical arrangement changes without the logical one changing, and in this
installation the projector pairing is exactly the kind of thing that changes
between seasons.

### 2. Show data reaches the renderer as files ahead of playback, never as a live stream

FPP Connect uploads the sequence and FSEQ to each renderer node **before**
playback. Each surface extracts its assigned virtual-matrix channels from the
local FSEQ file and renders them to its own canvas.

**The renderer does not consume a live matrix stream**, and nothing in this
architecture may introduce one. This keeps the renderer off the critical network
path during a show, which is the same property [ADR-008](ADR-008-mqtt-control-plane.md)
and the standing constraint about a running show surviving coordinator and
broker loss exist to protect. A node that already holds its FSEQ can render
through a network partition; a node pulling frames cannot.

Timing still arrives over MultiSync, which is unchanged and remains the only
timing path.

### 3. The architecture supports `N` surfaces per node; v1 implements `N = 1`

The data model, capability advertisement, and configuration must express `N`
independent surfaces per node from the start. **v1 implements one.**

The limit is an implementation scope, not an architectural property, and it must
not be embedded in schema, wire types, or capability attributes. This is stated
because the cheapest wrong implementation is a single-surface assumption spread
across a dozen places, which is invisible until the second surface is attempted
and expensive at that point.

### 4. NDI is the v1 and reference transport; HDMI remains a supported alternate

NDI is the transport from renderer nodes into Resolume for v1 and for the
reference installation. HDMI with capture remains supported as an alternate and
fallback.

**This narrows [ADR-005](ADR-005-pluggable-media-transport.md) without
superseding it**, in the same way ADR-024 narrowed ADR-022 decision 4 and ADR-025
narrowed ADR-009's `checksummed` clause. ADR-005's substantive rule, that
rendering and transport are separate interfaces and that architecture and media
formats must not assume one transport, is **unchanged and still binding**. What
changes is only that one profile is now named as the reference, which ADR-005
deliberately declined to do pending evidence.

Transport is selected **per node and per surface from advertised capabilities**,
never as one global system setting. A node may advertise NDI send, HDMI output,
or both. **Support for one transport is never evidence for the other**, and a
node advertising both reports each capability independently with its own
evidence.

The test that keeps ADR-005 real: if NDI were removed from this project
tomorrow, the renderer, the surface model, and the capability advertisement
would all still be correct, and only an adapter would be missing. If that ever
stops being true, this decision has been over-applied.

### 5. The reference profile is a target to validate, not a supported profile

The day-0 reference profile is one logical surface per x86 renderer node, 40
frames per second, NDI output, on Dell OptiPlex Micro 7040-class hardware.

**"Day-0" means mid-September 2026**, the date the operator must be able to
control a real show, chosen deliberately ahead of the Halloween show's 17
October opening so that faults surface with slack. It is not Halloween, and
source drafts that wrote "day-0/Halloween" as one phrase understated the
constraint by six weeks.

**No part of that has been measured.** It is the profile the bench exists to
validate, and until RES-004 runs it must be described everywhere as intended
rather than supported, including in ARCHITECTURE §4.4.

The distinction is not decoration. RES-004's stated purpose is to *establish*
supported renderer profiles by hardware class, resolution, frame rate, and
transport. A frame rate written into the architecture spec ahead of that bench
is the answer appearing before the work that produces it, and the specific risk
is ordinary: someone reads 40 fps as a settled capability, sizes a canvas
against it, and discovers the real number during a show. RES-004 correctly makes
canvas dimensions and pixel count test parameters rather than fixing a
resolution, which means **40 fps is not even a well-formed claim yet**; it is 40
fps at an unspecified pixel count.

Until the bench runs, any surface that states the profile states it with its
status. This record is the authority for that requirement, so that removing the
qualifier requires superseding a decision rather than editing a sentence.

### 6. A missing NDI runtime degrades the node, never stops it

ShowMesh vendors the MIT-licensed NDI headers and **never redistributes the NDI
runtime**, which is the existing standing constraint and is unchanged. The
adapter dynamically loads a user-installed runtime and honours the documented
location override.

When the runtime is absent, the node **starts, stays operational, keeps every
other capability, does not advertise a usable NDI sender, and reports an
actionable installation pointer.** It never refuses to start.

This is the same rule [ADR-025](ADR-025-agent-fallback-cache-is-signed.md)
decided for a failed cache verification, and the reasoning transfers exactly: a
missing optional dependency that stops a node has turned a degradation into an
outage, and in this architecture that points the wrong way. It is recorded here
rather than left to the adapter because it is the kind of behaviour an
implementer writes as a fatal startup check without noticing they have made one.

## Consequences

- The renderer object model has no projector, so surface-to-projector mapping
  must live in deployment configuration and Resolume. Anything that wants to
  render a per-projector view to an operator has to compose it from that
  configuration rather than reading it off a renderer object.
- Because show data is delivered as files ahead of playback, **asset delivery
  becomes a readiness concern**: a node missing its FSEQ is not detectable at
  playback time by anything currently built. That belongs to RES-003 and to
  OBSERVABILITY's readiness evidence, and it is a new obligation created by
  decision 2.
- Naming NDI the reference transport concentrates bench effort on one path.
  HDMI profiles are validated when a deployment selects one, which means **the
  fallback is less proven than the primary**, and RES-009 should carry that
  asymmetry rather than assume parity.
- `N = 1` in v1 means the multi-surface path is unexercised. Every place that
  could assume a single surface is a latent defect, and no test will find it
  until `N > 1` is attempted.
- Decision 5 will read as bureaucratic to whoever first wants to write "40 fps"
  without a qualifier. That is the point at which it is doing its job.

## Alternatives considered

**Leaving these decisions in ARCHITECTURE §4.4 and §4.7 with no record.**
Rejected because it is the exact practice CLAUDE.md forbids, and because the
renderer surface model is a modelling decision of the same class as ADR-002. The
practical failure is specific: a component description is the first thing
rewritten when someone refactors, and nothing in it signals that a sentence is
load-bearing.

**Waiting for RES-004's bench before recording anything.** Rejected on ADR-017's
precedent. Implementation cannot start against an undecided model, the bench
needs a profile to test against, and refusing to write the decision down does
not make the project undecided, it makes the decision unfindable.

**Modelling projectors in the renderer.** Rejected per decision 1. It also fails
the reference installation immediately, where a combined surface feeds a
projector pair.

**Declaring NDI the only transport and dropping HDMI.** Rejected: it would
supersede ADR-005 rather than narrow it, on zero evidence, and HDMI is the
fallback for exactly the deployments where NDI's network assumptions do not
hold. ADR-005's reasoning has not been refuted by anything; it has only been
supplemented by a choice of default.

**One ADR per decision.** Rejected because all six share one context and one
evidence posture, and splitting them would let decision 5's qualifier drift away
from the profile it qualifies.

## Related research

[Renderer performance](../research/RES-004-virtual-matrix-renderer-performance.md) ·
[NDI versus HDMI](../research/RES-005-ndi-vs-hdmi-transport.md) ·
[Linux NDI](../research/RES-006-linux-ndi-support.md) ·
[xLights and FPP Connect](../research/RES-003-xlights-fpp-connect-compatibility.md) ·
[Failure-mode testing](../research/RES-009-failure-mode-testing.md)

## Supersession

This record supersedes nothing. It **narrows** [ADR-005](ADR-005-pluggable-media-transport.md)
by naming a reference transport, and ADR-005's separation of rendering from
transport, its prohibition on assuming one transport, and its adapter model all
remain in force.

A future record must revisit decision 5 once RES-004's bench produces a measured
profile, at which point the intended profile becomes a supported one or is
replaced by what the hardware actually does. That is the only decision here
expected to change on evidence; the others are modelling choices that evidence
would inform rather than settle.
