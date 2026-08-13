# Track B: Nodes and projection

[Build plan](BUILD-PLAN.md) · [ADR-026](../decisions/ADR-026-renderer-surface-model-and-reference-transport.md) · [Spike procedure](../bench/TRACK-B-NDI-SPIKE.md) · [RES-004](../research/RES-004-virtual-matrix-renderer-performance.md)

Status: not started. Specified 2026-08-13.

## Goal

ShowMesh drives projection. A render node follows the show timeline, extracts its surface's virtual-matrix channels from a local FSEQ, renders them, and sends the result to Resolume over NDI.

**This is reason two for the project existing.** Projection did not work on the Raspberry Pis last season, which is both why the path is being rebuilt on x86 and why this track carries more risk than its size suggests.

## The track is smaller than it looks, and the reason matters

The obvious read is that this starts at zero, because `internal/agent` says so in its own package comment: *no GStreamer, no media, no command handling, no local fallback cache, no real capability. The agent exists to be seen, nothing more.*

**But the hard half is already built.** A render node needs to know where in the sequence the show currently is, and that answer arrives over FPP MultiSync. `pkg/multisync` is Step 1's work: the wire codec, a listener on UDP 32320 accepting multicast, broadcast and unicast, and a timeline state machine implementing FPP remote semantics on an injectable clock, including free-run through sync silence, slew, jump, and the STOP plus blank delay. That package has one consumer today, the bench probe, and **this track is what it was written for.**

Two consequences worth stating before anyone re-derives them:

- **[ADR-013](../decisions/ADR-013-no-fpp-control-port-sharing.md) does not constrain the render node.** That constraint exists because a listener co-located with a running `fppd` can steal its unicast sync stream. A render node runs no `fppd`, so it binds 32320 normally, with no port sharing and no `SO_REUSEADDR`.
- **[RES-002](../research/RES-002-fpp-multisync-compatibility.md) is this track's dependency, not just a research chore.** The renderer's clock is MultiSync. Drift accumulation over a show and multicast behaviour on the reference switch are the two open items no container can close, and they are exactly the properties a projected surface will show on a wall. The owner benches it 2026-08-16.

## Deliverables

**B0. The transport spike**, per [`TRACK-B-NDI-SPIKE.md`](../bench/TRACK-B-NDI-SPIKE.md). Run before anything else. It answers whether the NDI assumption in ADR-026 survives contact with the reference hardware, and a failure here changes the track's design rather than delaying it.

**B1. A real node agent.** The current one advertises capabilities and heartbeats. It needs to receive and act: subscribe to its command topic, execute an allowlisted operation, and report the outcome. ARCHITECTURE §10.4's "agents accept only allowlisted operations" is recorded in ADR-024's consequences as **not delivered**, and this is where it lands.

**B2. GStreamer pipeline supervision.** The agent builds, starts, watches, and restarts pipelines, and reports their health as observations with provenance. ShowMesh code owns supervision and health and never touches per-frame rendering, per [ADR-007](../decisions/ADR-007-gstreamer-media-engine.md).

**B3. The surface model and FSEQ extraction.** A surface is a configuration object holding its canvas dimensions and its assigned virtual-matrix channel range. **This needs no schema migration**: the RES-008 re-survey established that `config_objects` and `config_revisions` are keyed `(kind, id)` with a JSON payload, so a surface is a new `kind`. Extraction reads the local FSEQ at the frame the timeline reports.

**B4. NDI output.** The transport adapter, dynamically loading a user-installed runtime, honouring the documented location override, and **never bundling the runtime**. A missing runtime degrades the node and never stops it, per ADR-026 decision 6.

**B5. Capability advertisement for all of it.** `render.surface` and the transport capabilities, versioned and attributed per [ADR-002](../decisions/ADR-002-capability-based-nodes.md), so the coordinator composes surfaces from advertisement rather than from a hardcoded node class. A node advertising both NDI and HDMI reports each independently, since support for one is never evidence for the other.

## Decisions this track must make

- **Where the FSEQ comes from, and how the node knows it is current.** RES-003 settles that FPP Connect uploads ahead of playback. What that leaves open is detection: a node holding last week's FSEQ, or no FSEQ, is invisible at playback time and shows up as a wrong or black surface. This is a readiness signal that does not exist yet and is a direct consequence of ADR-026 decision 2.
- **What a surface does when sync goes away.** `pkg/multisync`'s timeline already free-runs through silence and transitions to `unsynchronized` after a defined interval. What the *output* does at that point is a product decision: hold the last frame, go black, or show a diagnostic. Resolume's own timecode-loss behaviour is undocumented and only forum-reported as holding the last frame (RES-001), so ShowMesh should not assume the downstream does anything sensible.
- **Whether ShowMesh controls Resolume at all for day-0.** It does not have to. Resolume can hold a composition that displays the NDI input while the operator does mapping by hand. Clip launch, timecode, and the Resolume adapter are RES-001 and are **recommended out of day-0 scope**, because they add an integration with L0 fault behaviour to a track that already carries the project's largest unknown.
- **The operating system**, recommended as Debian 13 in the spike document, pending the packaging check there.

## Acceptance criteria

- A render node advertises a surface capability, is visible in the Operator UI, and reports pipeline health with provenance and freshness like every other observation.
- With FPP playing a sequence, the node follows the MultiSync timeline and Resolume displays the surface's content in step with the lighting.
- Stopping the sequence produces the decided sync-loss behaviour, and that behaviour is the one written down rather than whatever the pipeline happens to do.
- Killing the pipeline underneath the agent is detected, reported, and restarted, and the restart is visible as an event rather than silent.
- A node with no NDI runtime installed **starts**, keeps its other capabilities, does not advertise a usable sender, and says why.
- The achieved frame rate and pacing at the intended canvas dimensions are recorded in RES-004 as measurements, replacing ADR-026 decision 5's unvalidated target.

**Bound by:** ADR-002, ADR-007, ADR-008, ADR-011, ADR-013, ADR-026, and above all the standing constraint that the coordinator is never in the timing or media path. The node renders; the coordinator watches.

**Out of scope:** the Resolume adapter and clip launch (RES-001), audio of any kind (Track C), HDMI output profiles, multiple surfaces per node, ARM hardware, and the preview wall (RES-010, cut from day-0).
