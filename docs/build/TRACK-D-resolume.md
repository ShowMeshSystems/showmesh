# Track D: Resolume control and timecode

[Build plan](BUILD-PLAN.md) · [RES-001](../research/RES-001-resolume-smpte-behavior.md) · [ADR-018](../decisions/ADR-018-program-and-ltc-share-a-clock-domain.md) · [ADR-003](../decisions/ADR-003-desired-and-observed-state.md)

Status: capture complete, adapter specified, build not started. Specified 2026-08-13. **Day-0 scope**, promoted from "not sequenced" the same day.

> **Corrected 2026-08-14 by [the bench capture](../bench/resolume-control-surface.md).** Resolume's REST, WebSocket and OSC surfaces were captured from a running Arena 7.23.2, and **four things this document asserts turned out to be false**. They are corrected in place below and each correction is dated, rather than being left for a builder to trip over. The implementation specification is [TRACK-D-ADAPTER-SPEC.md](TRACK-D-ADAPTER-SPEC.md).
>
> The four: **OSC cannot address a pinned clip**, so the "OSC to act, REST to confirm" split is reversed; **the page race is a clip race, not a layer race**, and dropping OSC removes it rather than guarding it; **the single-page assumption is not the one to guard**, because the operator's composition already has three decks; and **a fixed confirmation deadline is wrong by 35×**, because a disconnect confirms one layer transition after a connect does.
>
> What the capture *confirmed*: OSC is fire-and-forget with no reply of any kind, and an inactive layer really is a silent failure, in a sharper form than this document describes. Both are unchanged below.

## Goal

ShowMesh drives Resolume: launches what should be playing, feeds it timecode so it follows the show, and observes enough state to tell whether it actually is.

**This is reason three the project exists.** The three founding problems were generating virtual matrix data, which produced the video node in Track B; controlling Resolume, which produces this track; and the FPP scheduler, which produces macros in Track A. Once all three pointed the same direction, bringing the whole media path into ShowMesh became the plan rather than three separate workarounds. **None of the three is optional**, because each one on its own is why this was started.

## The dependency nobody will notice until it bites

**Resolume's timecode arrives as audio over a physical cable, and in the finished system that cable starts at [Track C](TRACK-C-audio-node.md)'s audio node.**

Resolume Arena accepts SMPTE only as **audio LTC**, configured per clip. It is not a network protocol and it is not something ShowMesh can send over the control plane. So the real chain is:

1. The audio node generates LTC on a discrete output, in the same clock domain as program audio ([ADR-018](../decisions/ADR-018-program-and-ltc-share-a-clock-domain.md)).
2. That output goes by cable into an audio input on the machine running Resolume.
3. Resolume clips are configured to follow that input.

Three consequences that are software problems, and one that is not:

- **D0 does not wait for Track C, corrected 2026-08-14.** This section previously said nothing in Track D's timecode half could be benched before the audio node generates LTC. That is false and it made this track look weeks further out than it is. **D0 needs LTC, not ShowMesh's LTC.** Any off-the-shelf generator on any working interface answers the actual open question, which is what Resolume does when timecode is late, absent, jumps, or restarts. The audio node is the show's LTC source; it is not a prerequisite for observing Resolume's behaviour.
- **Track C is still on the critical path for the show, just not for this bench.** The finished timecode chain does depend on the audio node, so the two tracks converge before day-0 even though D0 does not wait for them to.
- **ShowMesh cannot confirm timecode delivery from its own side.** It can confirm that LTC is being generated and it can ask Resolume what it thinks the time is, but the cable in between is unobserved. Confirmation logic must rest on Resolume's own reported state, never on the audio node having sent something.
- **The cable and the interface are the owner's problem and need no design here.** He is an audio engineer; getting LTC out of an interface and into a computer is his day job. Interface selection was reopened on 2026-08-14 and is deliberately not a gate on either track, per Track C. This is recorded so that nobody spends planning effort on it.

## What is known, and the large hole in it

**The installation, confirmed by the owner 2026-08-13.** Resolume Arena **7.23.2**, currently on macOS, and **Halloween runs this version**. Two things follow. The REST API needs 7.8 or later, so it is comfortably available and the adapter can rely on it rather than leaning on OSC for everything. And the machine is a Hackintosh whose platform is effectively dead, so **it may move to Windows**: the adapter must not acquire a macOS assumption, and nothing may depend on host-specific behaviour. A version upgrade is planned for Christmas on the Black Friday sale, which is a recorded revalidation trigger rather than a surprise.

From RES-001's desk research, all L1 from documentation and none of it benched:

- **Arena, not Avenue**, accepts SMPTE as audio LTC configured per clip.
- ~~The REST API (7.8 and later, port 8080, `/api/v1`) plus a WebSocket give confirmable state. **OSC gives low-latency triggers.**~~ **Superseded 2026-08-14 by the capture.** REST and the WebSocket do give confirmable state, on the same port, and the port is configuration rather than a constant (this installation runs 9080). **OSC does not give usable triggers**: its address space is positional only, a positional clip address means a different clip on every deck, and it has no reply of any kind. A REST connect is observable in 4–64 ms, so the latency argument for OSC does not survive either. See the capture's §1 and §6.
- **Timecode-loss behaviour is undocumented.** Forums report that it holds the last frame, and the forum pages are bot-gated so even that is a search excerpt rather than a read source. **Unchanged by the capture**, which deliberately did not touch the timecode path.

That last point is the hole, and it is exactly the shape this project keeps getting caught by: the failure path is the undocumented one. A composition that holds its last frame on timecode loss looks identical to one that is running correctly on a paused show, from across a yard, at night.

## Deliverables

**D0. Bench RES-001 before building the adapter.** Acquisition, late start, loss of one to ten seconds, jumps, source restart, and a Resolume restart mid-show. This is the record's own test matrix and it needs the real Resolume and an LTC source, **which is any working generator rather than Track C's node** per the correction above. It gates the adapter's error handling, because that is otherwise a design against behaviour nobody has observed.

**D0 is not the only thing that can start, and it is not the first.** Capturing what Resolume's REST, WebSocket and OSC surfaces actually expose needs no timecode and no cable, only a running Arena. It is the same ordering Step 8 used when it captured FPP's real command vocabulary before naming a single command, which immediately overturned four assumptions that read as entirely plausible. Everything in D1 through D4 is currently reasoned from documentation and forum posts.

**D1. The Resolume adapter.** ~~REST and WebSocket for state and confirmable operations, OSC for low-latency triggers.~~ **Corrected 2026-08-14: REST for every action and every read, one WebSocket held purely as a change signal, and no OSC at all in v1.** ARCHITECTURE §4.6 permits a lower-latency interface for operational triggers; the capture established that Resolume's is not one, because it cannot name the right clip and cannot say whether it arrived. **The adapter never enters the frame path**, which is unchanged and free. Specified in [TRACK-D-ADAPTER-SPEC.md](TRACK-D-ADAPTER-SPEC.md) §3.1.

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

Note the tension the adapter has to resolve: the owner's shape names **OSC** as the transport, and OSC is fire-and-forget UDP with no reply. [ADR-003](../decisions/ADR-003-desired-and-observed-state.md) still wants evidence. ~~Since 7.23.2 has the REST API, the resolution is **OSC to act and REST or WebSocket to confirm**~~

**Corrected 2026-08-14. The resolution is REST to act and REST to confirm, and the tension dissolves rather than being managed.** The capture proved OSC cannot address a pinned clip at all, so acting over OSC would mean acting positionally, which is the index drift the addressing section below exists to forbid. It also measured a REST connect at 4–64 ms, which removes the reason to reach for a lower-latency path. Nothing confirms off the OSC send, and now nothing is sent over OSC either. The owner verified the pinning limitation on his own installation on 2026-08-14 and notes the contrast that makes it a Resolume design choice: **MIDI and Art-Net/DMX can pin, OSC cannot.**

## Decisions this track must make

- **What happens on timecode loss**, once D0 establishes what Resolume actually does. If it holds the last frame, ShowMesh must surface that as a fault rather than trusting the wall to look wrong.
- ~~How composition state is addressed stably.~~ **Answered by the owner 2026-08-13:** clips and layers are both pinned by identity, and the page is the one dimension that cannot be. See the addressing section below, including the deferred race and the tripwire that guards it.
- **What ShowMesh does about a composition it does not recognise**, since the operator authors the composition in Resolume and ShowMesh learns about it rather than owning it. **Partly answered 2026-08-14**: it is asserted structurally by the expected clip ids resolving, because the composition has no id in REST and its name is not an identifier. What ShowMesh should *do* about a mismatch is still open, beyond surfacing it as evidence and never refusing.
- **What should happen to Resolume when the coordinator is offline.** Raised by the owner 2026-08-14 and **held for its own session.** The mechanics are settled: the wall keeps showing what it was showing, and no ShowMesh-driven transition happens, which is why every Resolume action is `coordinator-required`. Whether that is the behaviour the show wants is a different question, and it is the same one [RES-015](../research/RES-015-fpp-plugin-distribution-model.md) answered for FPP with a plugin on the host. **It does not block the adapter build.**

## Addressing: everything is pinned, except the page

Settled by the owner 2026-08-13, who already works this way in practice.

**Clips are pinned.** Resolume can bind a DMX, MIDI, or OSC command to *this clip*, rather than to a layer-and-column position. The owner always pins, so a clip trigger addresses an identity and keeps addressing the right thing after the composition is reordered. **This is the safe half and it removes the index-drift defect entirely.** ShowMesh should require pinned addressing for clips and should not offer positional clip triggering at all, because offering both means someone eventually uses the fragile one.

**Layers are pinned too, confirmed by the owner 2026-08-13.** Resolume can bind to "this layer", so activating a layer is **one command**, not the two this document assumed an hour earlier. The clip and layer halves therefore use the same identity-based addressing and the adapter needs one model rather than two.

> **Corrected 2026-08-14, and this is the correction that reshapes the track.** The rule above is right and the transport assumption under it is wrong. **Pinning is a shortcut-system feature, not a protocol feature.** Resolume's own binding files record a pinned target as `/composition/objects/<id>/…` with `translationType="2"`, and DMX and MIDI honour it. **OSC's default address space does not**: a message to that address does nothing, verified from a disconnected baseline against five spellings, and Arena's own outbound stream emits 1,545 distinct addresses of which none is a pinned form. A pinned OSC trigger exists only as an operator-authored binding, at an address the operator chooses, in a preset file **no API exposes**, so ShowMesh can neither derive nor verify it.
>
> **REST has native pinned addressing** at `/composition/{kind}/by-id/{id}`, needing nothing from the operator, and the identifier is the same integer the shortcut files use. Object ids also survive a restart and survive edits and re-saves: 246 clip ids carried from `Christmas 24` to `Christmas 25`.
>
> **So the rule stands and the transport changes.** ShowMesh requires pinned addressing and offers no positional addressing at all, which is only possible over REST.

**What pinning does not solve is the page dimension.** A pinned layer command still lands against whatever page is current, and there is no way to pin "this layer on this page". So the race is real: if anything changes the page between ShowMesh deciding and Resolume acting, the command succeeds against the wrong page, reports no error, and the wall does the wrong thing.

### The page race, measured 2026-08-14, is a clip race and it is removed rather than deferred

**"Page" and "deck" are the same thing**, confirmed by the owner 2026-08-14, who uses both words. Everything below means `composition.decks`.

**The race is real, and it is not the one described above.** Measured by selecting each deck and re-reading:

- **Layer identity is deck-independent.** `layers[1].id` is the same object on all three decks; the same 18 layers exist under every deck. **A layer command does not race the deck**, so the paragraph above is wrong about which object is exposed.
- **A positional clip path resolves to a different object on every deck.** `/composition/layers/1/clips/5` is `Green screen snowstorm` on `Main` and two different empty clips on the other two decks. The column count itself changes with the selected deck: 14, 9, 9.
- **REST `by-id` is immune**, verified directly: a `Main` clip read by id while `Rest Staging` was selected returned the right clip.

**So dropping OSC removes the race rather than guarding it.** Positional addressing was the only thing that could race a deck, REST `by-id` is the only addressing the adapter offers, and the deferred fix is not needed.

~~**Owner's decision, 2026-08-13: the Halloween show is built on a single page**, which makes the race unreachable.~~ **That assumption was never true and could not have held**: `Christmas 25` already has three decks, so a tripwire firing on "more than one page exists" would have fired immediately and taught nobody anything.

**What survives of the tripwire's intent**, specified in [TRACK-D-ADAPTER-SPEC.md](TRACK-D-ADAPTER-SPEC.md) §3.8, is narrower and can still fire: **record on an action's outcome whether the selected deck changed between the decision and the confirmation.** `decks[i].selected` is readable in state the adapter already holds. It is evidence, never a refusal.

**And the assumption that actually needed a tripwire turned out to be a different one.** The composition object has **no id field in REST**, the composition-level `uniqueId` in the file is the same constant across all six of the operator's compositions, and after a restart the correct composition *name* appears ~0.7 s before the layers do. So "the right composition is loaded" cannot be asserted by name and is asserted structurally instead, by the expected clip ids resolving.

## Acceptance criteria

- RES-001's test matrix is run and recorded, moving its fault behaviour off L0. **Still outstanding; this is D0 and the capture deliberately did not touch it.**
- **Layer activation is confirmed by reading back layer state**, never by the message being sent. Unchanged, and now trivially satisfied because nothing is sent over OSC.
- ~~**A composition with more than one page surfaces as visible evidence.**~~ **Replaced 2026-08-14**: the deck race is removed by dropping positional addressing, and the surviving criterion is that **an action records whether the selected deck changed between its decision and its confirmation.**
- A macro step launches a clip, activates a layer, and triggers a column, each confirmed by Resolume reporting the result rather than by the message being sent. **"Activates a layer" resolves to bypass and master**, because the capture established there is no `active` field on a layer.
- **An inactive layer is reported as a readiness fault before a show**, not discovered when timecode arrives and nothing launches. **Sharper than this document assumed**: a clip on a bypassed layer, and a clip on a layer at zero master, both report `Connected` with `active_clip` present, so `connected` is not evidence anything reached the wall and readiness is a conjunction of seven readable fields.
- **A confirmation deadline is derived from state, never fixed.** Added 2026-08-14: a connect confirms in 4–64 ms and a disconnect confirms one layer transition later, measured at 0.0 s → 75 ms through 5.0 s → 4,068 ms.
- **Reachable is never treated as ready.** Added 2026-08-14: after a restart the REST API answers `200 OK` for ~1.2 s describing a composition that is not the show, carrying the correct name for the last 0.7 s of it, with no field saying "loading".
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
