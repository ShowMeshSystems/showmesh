# ADR-045: An Installation May Declare More Than One Audio Node, With Roles

Status: Accepted (owner, 2026-08-26)
Date: 2026-08-26

## Context

Every audio decision made so far — [ADR-017](ADR-017-showmesh-owns-audience-audio.md) (ShowMesh owns audience audio), [ADR-018](ADR-018-program-and-ltc-share-a-clock-domain.md) (program and LTC share one clock domain), and the `audio.node` shape [ADR-039](ADR-039-operator-configuration-is-store-backed.md) made store-backed — was written and implemented against exactly one audio node per installation. [AUDIO-ENGINE.md](../architecture/AUDIO-ENGINE.md) §4 and §11 both say "the active audio node" as if there could only ever be one, and §11.3's node-failure procedure talks about "standby nodes" that stand by for the same single program+LTC role rather than performing distinct roles of their own.

The reference installation and every installation described in RESTING-MODE.md's background-audio and announcement model already assume a single audio output for program, LTC, and announcements together. That has been sufficient for one FM transmitter, one Resolume LTC feed, and one set of house speakers. It stops being sufficient the moment an installation adds an independent zone — a porch speaker bed separate from the house mix, a garage feed, a second yard zone — because today's `show.cue` and `audio.node` shapes have no field that says which node a Cue's audio, LTC, or announcement output should reach, and no field that says what role a given `audio.node` plays.

SM-308 recorded five owner decisions, ruled 2026-08-26, that settle the shape of a multi-node audio model without deciding how nodes stay in sample-accurate sync with one another — that is [SM-309](../build/BUILD-LOG.md), explicitly out of scope here. SM-311 is the first seam of SM-308: it records the five decisions as this ADR and lands the additive contract change that lets an installation declare more than one `audio.node` with roles. Everything that actually routes a Cue to a specific node at runtime — cue-catalog resolution, dispatch, activation, readiness — is follow-on seam work; this record is the contract these later seams build against, not their implementation.

## Decision

### 1. A Cue's audio, LTC, and announcement outputs each name an optional target node

`show.cue.outputs.audio`, `outputs.ltc`, and `outputs.announcement` each gain an optional `target` field naming an `audio.node` id, mirroring `show.surface.node`'s existing "node" field on the render side. A Cue that declares a `target` is refused unless that id names a currently configured `audio.node` object — the same "refused against what actually exists" posture `show.surface.node`'s `nodeDeclared` check already uses for render placement.

A Cue with no `target` on a given output is not an incomplete Cue. It resolves later to the installation's single `program+ltc` audio.node — exactly the node every existing single-node installation's Cues already resolve to today. This is what keeps a one-node installation's authored Cues valid and unchanged: absence of `target` is the default, not a gap that must be filled in before ADR-045 ships.

### 2. Exactly one LTC emitter per installation, enforced at authoring and at readiness

An installation may have any number of `audio.node` objects, but at most one of them may be the LTC emitter. [ADR-018](ADR-018-program-and-ltc-share-a-clock-domain.md) is unchanged and unnarrowed by this record: program audio and LTC still share one clock domain, so there is still exactly one LTC generator, on exactly one node, for the whole installation — multiple nodes do not mean multiple LTC feeds.

This is enforced twice: at authoring, writing a second `audio.node` with the LTC-carrying role is refused outright (decision 3 names the mechanism); and at readiness, a later seam must confirm the deployed configuration still has exactly one such node before a show is called ready. Only the authoring half ships in this record; the readiness half is explicitly follow-on seam work, matching this record's own scoping note above.

### 3. `audio.node` gains a role and an optional zone name

Every `audio.node` object carries a `role`, one of:

- `program` — plays program audio only, no LTC;
- `program+ltc` — plays program audio and is this installation's sole LTC emitter (decision 2);
- `zone` — plays an independent local speaker zone, never program or LTC for the main mix.

`role` is optional on the wire. Absent, it defaults to `program+ltc` — the role every pre-ADR-045 `audio.node` object already implicitly held, since there was only ever one and it always carried both program and LTC. This is what keeps every already-configured single-node installation's stored `audio.node` object valid and unchanged without a forced rewrite.

`audio.node` also gains an optional `zone` name, meaningful only when `role` is `zone` — an operator-facing label ("porch", "garage") for which independent zone this node drives. `zone` present on any other role is refused: an ignored field would read as an applied one, the same posture this package already takes with `show.cue.outputs.announcement.duckGainDb`.

At most one `audio.node` may carry `program+ltc` at a time (decision 2); any number may carry `program` or `zone`.

### 4. `audio.settings` stays one installation-wide object this season

`audio.settings` (drift ignore threshold, default fade curve and duration, default background gain ceiling, duck target gain, LTC frame rate and default start offset) is not split per node or per zone this season. Every node in a multi-node installation shares the same engine-wide defaults. A per-zone override, if a real installation needs one, is a future decision with its own evidence — this record does not anticipate it.

### 5. Night-mode background beds and announcements accept a list of target nodes

`night.session`'s resting background-audio playlist and its announcement cues are extended to accept a **list** of target nodes rather than the single implicit output every background-audio item currently shares. A resting bed or an announcement may play on more than one node at once — the porch and the garage both getting the same background music, for instance — rather than being pinned to exactly one audio output.

This decision is recorded here because it is one of SM-308's five frozen decisions and belongs in the durable record with the other four. Its implementation — changing `night.session`'s stored shape, its validation, and the dispatch path that plays a background item on more than one node — is not part of this seam's contract change (see the scope note in Context) and lands in a later SM-308 seam.

### Cross-node sample sync is out of scope

Nothing in this record says how two audio nodes stay in sample-accurate sync with each other, whether a zone node's playback is expected to align with the program node's playback, or what drift tolerance applies between independent nodes. [ADR-017](ADR-017-showmesh-owns-audience-audio.md)'s existing drift model (align at start, measure continuously, correct at track boundaries, never chase continuously) still governs each node's own relationship to the show timeline; this record does not extend it to a relationship between nodes. That is [SM-309](../build/BUILD-LOG.md)'s question, not this one's.

## Consequences

- `show.cue`, `audio.node`, `api/openapi.yaml`, `showmeshctl`, and the generated UI client all gain the new optional fields this record adds. Every change is additive: no existing required field, enum member, or default changes meaning.
- A one-node installation's existing `show.cue` and `audio.node` objects — no `target`, no `role`, no `zone` — continue to decode, validate, and derive the identical resource claims they did before this record, because absent `target` resolves later to the sole `program+ltc` node and absent `role` defaults to `program+ltc`.
- Authoring a second `program+ltc` `audio.node` is refused, naming both node ids, so an operator who mistypes a role finds out at write time rather than at showtime.
- Authoring a Cue whose `target` names no configured `audio.node` is refused at write time for the same reason.
- [AUDIO-ENGINE.md](../architecture/AUDIO-ENGINE.md) §4 and §11 are amended to describe N nodes with roles rather than "the active audio node" and its standbys.
- Runtime resolution of a Cue's `target` to an actual node during cue-catalog assembly, dispatch, and activation; readiness enforcement of decision 2's one-LTC-emitter rule; and night-mode's list-of-targets implementation (decision 5) are not part of this record's shipped code and remain follow-on SM-308 seam work.
- `night.session`'s current single-target background-audio shape (`resting.backgroundAudio`, every item pinned to one `audio.node` via `Asset.Target`) is unchanged by this seam; decision 5 records the destination, not the migration.

## Alternatives considered

**Model multiple nodes as multiple independent installations instead of one installation with roles.** Rejected. `audio.settings` (decision 4) and every show-level authoring object (`show.cue`, `show.playlist`) are meant to describe one show across every output it drives; splitting an installation into several independent audio configurations would duplicate every one of those objects per node and reintroduce exactly the cross-namespace risk [ADR-043](ADR-043-show-scoped-cues-and-playlist-authority.md) exists to prevent, for no benefit.

**Make `target` required on every Cue output as soon as more than one node exists.** Rejected for this record. It would mean every existing one-node installation's Cues become invalid the moment a second node — even an unrelated zone node with no bearing on the existing Cues — is added, which converts an additive change into a breaking one for installations that never asked to change their existing outputs' behavior. Absent `target` resolving to the sole `program+ltc` node stays correct and unambiguous however many `zone` nodes exist alongside it.

**Allow more than one `program+ltc` node and pick one per Cue.** Rejected. [ADR-018](ADR-018-program-and-ltc-share-a-clock-domain.md) is unchanged: there is one clock domain for program and LTC together, which means one LTC emitter, full stop. Multiple nodes solve "more places for audio to come out of," not "more than one LTC feed."

**Fold decision 5 (night-mode list of targets) into this seam's shipped code.** Rejected. `night.session.resting.backgroundAudio` and its `OutputNodeID`/mixed-targets validation are a materially different shape change than the `show.cue`/`audio.node` additive fields this seam ships, and folding it in here would make this seam's diff span two independently reviewable changes. It is recorded as a frozen decision now so a later seam implements it against a settled contract rather than inventing one mid-implementation.

## Related research

No RES record covers multi-node audio placement or cross-node behavior specifically; [RES-007](../research/RES-007-audio-node-architecture.md) remains the audio-node architecture research record and is unchanged by this decision, since nothing here touches a single node's own clock, drift, or rendering behavior.

## Related decisions and work

- [ADR-017](ADR-017-showmesh-owns-audience-audio.md): audience-audio authority and per-node local playback — unchanged; this record extends placement, not authority.
- [ADR-018](ADR-018-program-and-ltc-share-a-clock-domain.md): one clock domain for program and LTC — unchanged and reaffirmed by decision 2's one-LTC-emitter rule.
- [ADR-039](ADR-039-operator-configuration-is-store-backed.md): `audio.node` and `audio.settings` as store-backed configuration kinds — this record adds fields to `audio.node`'s existing shape under that same posture.
- [ADR-043](ADR-043-show-scoped-cues-and-playlist-authority.md): `show.cue` as the show-scoped unit of synchronized playback — this record extends `show.cue.outputs`' audio, ltc, and announcement members with an optional target, mirroring the render output's existing `show.surface.node` precedent.
- [AUDIO-ENGINE.md](../architecture/AUDIO-ENGINE.md) §4, §6, §11, §13: the sections amended alongside this record to describe N nodes with roles.
- SM-308: the owner decision record this ADR transcribes.
- SM-311: this seam — the ADR plus the additive `show.cue`/`audio.node` contract change.
- SM-309: cross-node sample sync, explicitly out of scope here.

## Supersession

This record supersedes nothing and narrows nothing. Every ADR it touches — ADR-017, ADR-018, ADR-039, ADR-043 — remains fully in force; this record extends the shapes those decisions established to describe more than one audio node instead of exactly one.
