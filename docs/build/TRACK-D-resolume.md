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

Two consequences that are software problems, and one that is not:

- **Track C is on this track's critical path.** Audio is not just for the audience; it is the timecode source. Nothing in Track D's timecode half can be benched before the audio node generates LTC.
- **ShowMesh cannot confirm timecode delivery from its own side.** It can confirm that LTC is being generated and it can ask Resolume what it thinks the time is, but the cable in between is unobserved. Confirmation logic must rest on Resolume's own reported state, never on the audio node having sent something.
- **The cable and the interface are the owner's problem and need no design here.** He is an audio engineer; getting LTC out of an interface and into a computer is his day job. The interface's output addressing was checked against documentation and will be confirmed on arrival, with a known-good Focusrite as the fallback if it disappoints. This is recorded so that nobody spends planning effort on it.

## What is known, and the large hole in it

**The installation, confirmed by the owner 2026-08-13.** Resolume Arena **7.23.2**, currently on macOS, and **Halloween runs this version**. Two things follow. The REST API needs 7.8 or later, so it is comfortably available and the adapter can rely on it rather than leaning on OSC for everything. And the machine is a Hackintosh whose platform is effectively dead, so **it may move to Windows**: the adapter must not acquire a macOS assumption, and nothing may depend on host-specific behaviour. A version upgrade is planned for Christmas on the Black Friday sale, which is a recorded revalidation trigger rather than a surprise.

From RES-001's desk research, all L1 from documentation and none of it benched:

- **Arena, not Avenue**, accepts SMPTE as audio LTC configured per clip.
- The REST API (7.8 and later, port 8080, `/api/v1`) plus a WebSocket give confirmable state. **OSC gives low-latency triggers.**
- **Timecode-loss behaviour is undocumented.** Forums report that it holds the last frame, and the forum pages are bot-gated so even that is a search excerpt rather than a read source.

That last point is the hole, and it is exactly the shape this project keeps getting caught by: the failure path is the undocumented one. A composition that holds its last frame on timecode loss looks identical to one that is running correctly on a paused show, from across a yard, at night.

## Deliverables

**D0. Bench RES-001 before building the adapter.** Acquisition, late start, loss of one to ten seconds, jumps, source restart, and a Resolume restart mid-show. This is the record's own test matrix and it needs the real Resolume, the real interface, and Track C's LTC. It is the first thing on this track and it gates the rest, because the adapter's error handling is a design against behaviour nobody has observed.

**D1. The Resolume adapter.** REST and WebSocket for state and confirmable operations, OSC for low-latency triggers. The split follows ARCHITECTURE §4.6: management operations use confirmable interfaces, operational triggers may use lower-latency ones. **The adapter never enters the frame path.**

**D2. Explicit composition control**, and this is larger than "launch a clip". See the section below; it is the half of this track that has no timecode in it at all.

**D3. Resolume state as observations**, with provenance and freshness like every other signal: is it running, what is playing, is it following timecode, and what does it think the current time is. Stale reads `unknown` rather than healthy, per [ADR-011](../decisions/ADR-011-context-aware-observability.md).

**D4. Confirmation by evidence**, per [ADR-003](../decisions/ADR-003-desired-and-observed-state.md). A clip launch is confirmed by Resolume reporting that clip playing, on evidence that post-dates the dispatch. This is Step 7's 179-microsecond lesson in a third subsystem, and OSC makes it sharper: **OSC is fire-and-forget UDP with no reply**, so an OSC trigger has no acknowledgement at all and confirmation must come from the REST or WebSocket side. A trigger that was sent is not a clip that launched.

## Two control paths, not one, and the precondition that links them

Settled by the owner 2026-08-13. This track carries **both** halves, and the earlier framing of "either ShowMesh launches clips or Resolume follows timecode" was a false choice.

**Path one, timecode.** LTC launches timeline clips. **But a clip only launches from timecode if its layer is active**, which means layer activation is a *precondition for the timecode path working at all*, not a separate feature. ShowMesh must be able to activate layers.

This has a readiness consequence worth building for rather than discovering: **an inactive layer is a silent failure.** Timecode arrives, nothing launches, and nothing reports an error, because from Resolume's point of view nothing was asked for. Layer-active state belongs in readiness evidence checked before a show, in the same class as OBSERVABILITY §10's other pre-show checks, rather than being noticed from the yard when the wall stays dark.

**Path two, explicit control.** Everything that is not timeline content, and there is a lot of it: countdowns, resting visuals, pre-show text, blackout, "show starts in 5 minutes". For these **ShowMesh explicitly says what to launch.** They are not timecode-driven and never will be.

The system shape, in the owner's words:

> FPP schedule or ShowMesh macro or ShowMesh UI → ShowMesh coordinator → Resolume OSC command → launch or switch the appropriate clip, layer, column, or composition state.

So the control vocabulary is **clip, layer, column, and composition state**, and all four are addressable. That is the scope of D2, and it is closer to "ShowMesh is the Resolume controller for everything that is not timecode" than to "ShowMesh can trigger a clip".

Note the tension the adapter has to resolve: the owner's shape names **OSC** as the transport, and OSC is fire-and-forget UDP with no reply. [ADR-003](../decisions/ADR-003-desired-and-observed-state.md) still wants evidence. Since 7.23.2 has the REST API, the resolution is **OSC to act and REST or WebSocket to confirm**, which is also exactly the split ARCHITECTURE §4.6 already describes. Nothing confirms off the OSC send.

## Decisions this track must make

- **What happens on timecode loss**, once D0 establishes what Resolume actually does. If it holds the last frame, ShowMesh must surface that as a fault rather than trusting the wall to look wrong.
- ~~How composition state is addressed stably.~~ **Answered by the owner 2026-08-13, and the answer is asymmetric.** See the section below, because the two halves of it have different safety properties and someone will otherwise try to unify them.
- **What ShowMesh does about a composition it does not recognise**, since the operator authors the composition in Resolume and ShowMesh learns about it rather than owning it.

## Addressing: clips are pinned, layers are coordinates

Settled by the owner 2026-08-13, who does it this way in practice already.

**Clips are pinned.** Resolume can bind a DMX, MIDI, or OSC command to *this clip*, rather than to a layer-and-column position. The owner always pins, so a clip trigger addresses an identity and keeps addressing the right thing after the composition is reordered. **This is the safe half and it removes the index-drift defect entirely.** ShowMesh should require pinned addressing for clips and should not offer positional clip triggering at all, because offering both means someone eventually uses the fragile one.

**Layers are coordinates, and this is the half that needs care.** There appears to be no way to pin "this layer on this page", so selecting a layer is **two commands: select the page, then select the layer.** The owner will confirm whether pinning is possible; if it turns out to be, this collapses to one command and everything below stops mattering.

Assuming two commands, three things follow.

**The page is global mutable state, so the pair is a race.** A layer command means "layer 3 of whatever page is currently selected". If anything changes the page between ShowMesh's two messages, the wrong layer activates, and it activates successfully with no error anywhere. That is the same class of defect as Step 7's confirmation coin flip: a correct-looking operation whose result depends on timing.

**So page-plus-layer is one ShowMesh action, never two macro steps.** The macro vocabulary exposes a single "activate layer" step taking a page and a layer, which emits two OSC messages internally. Exposing them separately would let an operator interleave other steps between them, which is how the race above gets built deliberately. Two of these actions must also not run concurrently.

**Confirmation checks the end state, not the messages.** Both messages are fire-and-forget UDP with no reply and no ordering guarantee, so neither one confirms anything. The adapter reads back over REST that the intended layer on the intended page is active. Always asserting the page, even when ShowMesh believes it is already selected, is cheaper than tracking assumed state and being wrong.

## Acceptance criteria

- RES-001's test matrix is run and recorded, moving its fault behaviour off L0.
- **Layer activation is confirmed by reading back page and layer state, not by the two OSC messages being sent**, and the two messages are never separately schedulable from a macro.
- A macro step launches a clip, activates a layer, and triggers a column, each confirmed by Resolume reporting the result rather than by the OSC message being sent.
- **An inactive layer is reported as a readiness fault before a show**, not discovered when timecode arrives and nothing launches.
- Resolume state appears in the Operator UI with provenance and freshness.
- Timecode loss produces the defined, operator-visible response decided above.
- A Resolume restart mid-show recovers to a defined composition state rather than an undefined one.
- With ShowMesh stopped, Resolume keeps doing what it was doing, per the standing constraint that the show survives coordinator loss.

**Bound by:** ADR-003, ADR-011, ADR-018, ARCHITECTURE §4.6, RES-001.

**Out of scope:** house mapping, which stays Resolume's job and the operator's; anything that puts ShowMesh in the frame path; and Resolume's own media management.

## Projector power is not in this track, and not in this project for day-0

Decided by the owner 2026-08-13. Projector power stays on Home Assistant and Node-RED, driven by an MQTT message the way it was driven from FPP.

What ShowMesh provides instead is **an external MQTT command step type with a declared response contract**, landing in Track A's Step 9. That is far smaller than [ADR-016](../decisions/ADR-016-controlled-devices-and-control-providers.md)'s provider model and buys back everything `pkg/pjlink`, a provider, and the RES-014 metadata question would have cost.

**It stays confirmable, which was the owner's refinement.** Node-RED can publish a status back when it sees the projector actually come on, so the step declares what it expects in return: nothing, a boolean, a number, text, or a matched value, on a named topic, within a deadline. That is a real ADR-003 evidence check against a contract the operator wrote, rather than a hope. A step configured to expect nothing is honestly unconfirmable and reports as such, never as success.

`pkg/pjlink` and the ADR-016 provider model are deferred, not cancelled. RES-012 and RES-014 keep them.
