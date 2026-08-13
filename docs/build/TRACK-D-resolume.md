# Track D: Resolume control and timecode

[Build plan](BUILD-PLAN.md) · [RES-001](../research/RES-001-resolume-smpte-behavior.md) · [ADR-018](../decisions/ADR-018-program-and-ltc-share-a-clock-domain.md) · [ADR-003](../decisions/ADR-003-desired-and-observed-state.md)

Status: not started. Specified 2026-08-13. **Day-0 scope**, promoted from "not sequenced" the same day.

## Goal

ShowMesh drives Resolume: launches what should be playing, feeds it timecode so it follows the show, and observes enough state to tell whether it actually is.

**This is reason three the project exists.** The three founding problems were generating virtual matrix data, which produced the video node in Track B; controlling Resolume, which produces this track; and the FPP scheduler, which produces macros in Track A. Once all three pointed the same direction, bringing the whole media path into ShowMesh became the plan rather than three separate workarounds. **None of the three is optional**, because each one on its own is why this was started.

## The dependency nobody will notice until it bites

**Resolume's timecode comes from Track C's audio node, over a physical cable.**

Resolume Arena accepts SMPTE only as **audio LTC**, configured per clip. It is not a network protocol and it is not something ShowMesh can send over the control plane. So the real chain is:

1. The audio node generates LTC on output 3, in the same clock domain as program audio ([ADR-018](../decisions/ADR-018-program-and-ltc-share-a-clock-domain.md)).
2. That output goes by cable into an audio input on the machine running Resolume.
3. Resolume clips are configured to follow that input.

Three consequences that should be settled early rather than discovered:

- **Track C is on this track's critical path.** Audio is not just for the audience; it is the timecode source. If the UMC204HD's output 3 turns out to mirror outputs 1 and 2, both tracks break, which is why that twenty-minute check leads Track C.
- **The Resolume machine is not the render node.** Arena runs on Windows or macOS, while the render node is Debian. They are separate machines with a cable between them, and that cable is a single point of failure with no software mitigation.
- **ShowMesh cannot confirm timecode delivery from its own side.** It can confirm that LTC is being generated and it can ask Resolume what it thinks the time is, but the cable in between is unobserved. Any confirmation logic must rest on Resolume's own reported state, never on the audio node having sent something.

## What is known, and the large hole in it

From RES-001's desk research, all L1 from documentation and none of it benched:

- **Arena, not Avenue**, accepts SMPTE as audio LTC configured per clip. Clip launch is **not** driven by timecode; those are two separate mechanisms.
- The REST API (7.8 and later, port 8080, `/api/v1`) plus a WebSocket give confirmable state. **OSC gives low-latency triggers.**
- **Timecode-loss behaviour is undocumented.** Forums report that it holds the last frame, and the forum pages are bot-gated so even that is a search excerpt rather than a read source.

That last point is the hole, and it is exactly the shape this project keeps getting caught by: the failure path is the undocumented one. A composition that holds its last frame on timecode loss looks identical to one that is running correctly on a paused show, from across a yard, at night.

## Deliverables

**D0. Bench RES-001 before building the adapter.** Acquisition, late start, loss of one to ten seconds, jumps, source restart, and a Resolume restart mid-show. This is the record's own test matrix and it needs the real Resolume, the real interface, and Track C's LTC. It is the first thing on this track and it gates the rest, because the adapter's error handling is a design against behaviour nobody has observed.

**D1. The Resolume adapter.** REST and WebSocket for state and confirmable operations, OSC for low-latency triggers. The split follows ARCHITECTURE §4.6: management operations use confirmable interfaces, operational triggers may use lower-latency ones. **The adapter never enters the frame path.**

**D2. Composition and clip control**, as macro step types Track A's Step 9 can sequence: launch what should be playing, and blackout.

**D3. Resolume state as observations**, with provenance and freshness like every other signal: is it running, what is playing, is it following timecode, and what does it think the current time is. Stale reads `unknown` rather than healthy, per [ADR-011](../decisions/ADR-011-context-aware-observability.md).

**D4. Confirmation by evidence**, per [ADR-003](../decisions/ADR-003-desired-and-observed-state.md). A clip launch is confirmed by Resolume reporting that clip playing, on evidence that post-dates the dispatch. This is Step 7's 179-microsecond lesson in a third subsystem, and OSC makes it sharper: **OSC is fire-and-forget UDP with no reply**, so an OSC trigger has no acknowledgement at all and confirmation must come from the REST or WebSocket side. A trigger that was sent is not a clip that launched.

## Decisions this track must make

- **Resolume version and host OS**, which determine whether the REST API exists at all: it arrived in 7.8. This needs checking against the installed copy before the adapter is designed.
- **Whether ShowMesh launches clips or Resolume's own timeline runs from timecode.** These are genuinely different designs. Timecode-driven means ShowMesh feeds LTC and Resolume follows; ShowMesh-driven means OSC or REST triggers at moments ShowMesh decides. The reference installation may want both, and RES-001 notes that clip launch is not driven by timecode, so choosing "just feed timecode" does not get clip selection for free.
- **What happens on timecode loss**, once D0 establishes what Resolume actually does. If it holds the last frame, ShowMesh must surface that as a fault rather than trusting the wall to look wrong.
- **Where the audio node's LTC output physically lands**, which is a cabling decision with no software component and a real chance of being deferred until it blocks something.

## Acceptance criteria

- RES-001's test matrix is run and recorded, moving its fault behaviour off L0.
- A macro step launches a composition and is confirmed by Resolume reporting it, not by the trigger being sent.
- Resolume state appears in the Operator UI with provenance and freshness.
- Timecode loss produces the defined, operator-visible response decided above.
- A Resolume restart mid-show recovers to a defined composition state rather than an undefined one.
- With ShowMesh stopped, Resolume keeps doing what it was doing, per the standing constraint that the show survives coordinator loss.

**Bound by:** ADR-003, ADR-011, ADR-018, ARCHITECTURE §4.6, RES-001.

**Out of scope:** house mapping, which stays Resolume's job and the operator's; anything that puts ShowMesh in the frame path; and Resolume's own media management.

## Projector power is not in this track, and not in this project for day-0

Decided by the owner 2026-08-13. Projector power stays on Home Assistant and Node-RED, driven by an MQTT message the way it was driven from FPP.

What ShowMesh provides instead is **an arbitrary MQTT publish step type for macros**, landing in Track A's Step 9. That is a much smaller thing than [ADR-016](../decisions/ADR-016-controlled-devices-and-control-providers.md)'s provider model, and it buys back the time that `pkg/pjlink`, a provider, and the RES-014 metadata question would have cost.

**The honest cost, which must be stated in the macro rather than papered over:** an arbitrary MQTT publish has **no observable effect from ShowMesh's side**. It cannot be confirmed under ADR-003, and it fails Step 8's rule that a command ships only when its effect is visible. It is a deliberate escape hatch, and it must report as unconfirmable with a reason from [ADR-020](../decisions/ADR-020-control-api-shape-and-change-stream.md)'s vocabulary, never as success. A macro step that always reports success is worse than no step, because the operator stops reading it.

`pkg/pjlink` and the ADR-016 provider model are deferred, not cancelled. RES-012 and RES-014 keep them.
