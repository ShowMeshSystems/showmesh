# Track D specification: the Resolume adapter (D1–D4)

[Build plan](BUILD-PLAN.md) · [Track D](TRACK-D-resolume.md) · [Bench capture](../bench/resolume-control-surface.md) · [Build log](BUILD-LOG.md) · [Lessons](LESSONS.md)

Written 2026-08-14 by the orchestrating session, the day after
[the bench capture](../bench/resolume-control-surface.md) and against it rather
than against expectations. [Track D](TRACK-D-resolume.md) states the goal, the
deliverables and the bounding records. This document makes the decisions that
section leaves open, and **corrects the ones the capture proved wrong**.

**Read this whole document before writing code**, including §2, which lists four
places where Track D's own text is now incorrect. Step 8's retrospective
established that a defect introduced by a specification is invisible to a reviewer
who trusts the specification. If something here is wrong, say so rather than
implementing it.

**Scope.** D1 the adapter, D2 explicit composition control, D3 Resolume state as
observations, D4 confirmation by evidence. **D0, the timecode bench, is not in this
document** and does not gate it: everything specified here was measured with no LTC
source, no audio interface, and no cable.

## 1. What this step is

ShowMesh currently cannot see or touch Resolume at all. This step gives it a
read-only collector first, then a small vocabulary of confirmable actions over the
composition, in that order and for the same reason Step 3 preceded Step 7.

Three obligations attach by name:

- **[ADR-003](../decisions/ADR-003-desired-and-observed-state.md).** A command is
  not successful because it was sent. Every action here confirms on observed state
  that post-dates its own dispatch, and §3.5 and §6.2 establish that post-dating is
  necessary but not sufficient for this target.
- **[ADR-011](../decisions/ADR-011-context-aware-observability.md).** Evidence
  carries provenance and freshness; stale reads `unknown`, never healthy. §7.2 is
  where this bites hardest, because Resolume answers `200 OK` with a composition
  that is not the show.
- **[ADR-029](../decisions/ADR-029-logical-actions-and-integration-bindings.md).** Macros
  invoke logical actions and never protocol commands. **The adapter owns the
  protocol *and* how that protocol reports success**, so no OSC address, REST path,
  or Resolume object id appears in an ordinary macro. This is what makes §4's
  vocabulary the deliverable rather than a convenience.

## 2. Corrections to Track D, carried by this specification

The capture overturned four things Track D asserts. They are named rather than
silently replaced, because three of them are instances of failure modes this
project has recorded before.

**2a. "OSC to act, REST or WebSocket to confirm" is reversed.** Track D's
addressing section requires pinned addressing for clips, forbids positional clip
triggering outright, and names OSC as the acting transport. Those cannot both
hold: **OSC's default address space is positional only**, proven by A/B from a
disconnected baseline and confirmed by 1,545 outbound addresses containing no
pinned form. Pinning is a shortcut-system feature that lives in a preset file no
API exposes, so ShowMesh cannot derive, verify, or discover a pinned OSC address.
§3.1 resolves this by dropping OSC from the control path entirely.

**2b. The page race is a clip race, not a layer race.** Track D describes it as
affecting pinned layer commands. Measured: layer identity is deck-independent, and
a positional *clip* path resolves to a different object on every deck. Since OSC is
the only positional transport and §3.1 removes it, **the race is removed with it**
rather than guarded. §3.8 replaces the tripwire with something that can still fire.

**2c. "The Halloween show is built on a single page" is not the assumption to
guard.** `Christmas 25` already has three decks, so a tripwire that fires on "more
than one page exists" fires immediately and teaches nothing. The owner confirmed
2026-08-14 that "page" and "deck" are the same thing and he uses both words.

**2d. A fixed confirmation deadline is wrong by 35×.** Track D and
[RES-001](../research/RES-001-resolume-smpte-behavior.md) have no deadline model at
all. Connect confirms in 4–64 ms; disconnect confirms one layer transition later,
proven causal at 0.0 s → 75 ms, 0.5 s → 531 ms, 2.5 s → 2,527 ms, 5.0 s → 4,068 ms.
§3.5 makes the deadline a value read from state.

RES-001 is separately wrong on four points, all recorded in the capture's §11. The
one that matters here: **REST can set SMPTE transport**, so the adapter is not
forced to keep OSC alive for that purpose either.

## 3. Decisions taken before the build starts

Recorded so each is a decision rather than an artefact of whichever branch happened
to run. §3.1, §3.3, §3.4 and §3.6 are candidates for an ADR once they have survived
a build; they are step-scoped until then.

### 3.1 OSC is not used for control. At all. In v1.

The adapter speaks **REST for every action and every read**, and holds one
WebSocket purely as a change signal (§3.4).

Track D reached for OSC on a latency argument that the measurements do not
support. Over loopback a REST `connect` became observable in **4–64 ms**, which is
inside a video frame at 60 fps and far inside anything a show cue cares about.
Against that, OSC costs:

- **positional addressing only**, which is the exact index-drift defect Track D
  wanted removed (§2a);
- **a different clip on every deck** (§2b);
- **no reply of any kind**, verified directly from a bound socket for a working
  address, a non-working address, and RES-001's `"?"` query form;
- **no way to express a pinned target** without operator-authored bindings ShowMesh
  cannot read.

**A transport that cannot name the right clip and cannot say whether it arrived is
not a control path.** If a future measurement on the show host shows REST latency
that actually threatens a cue, OSC comes back as an optimisation with REST still
holding identity and confirmation, and that is a decision to make with a number
rather than now.

### 3.2 Resolume's OSC output is not used for observation

"Output All Messages" streams **236 datagrams/s with nothing connected** and 481
with one clip playing, in positional addresses whose integer values are
`ParamState` option indices only decodable from a REST read anyway.

It is also **a single configured target**. `osc.xml` holds one `targetAddress` and
one `targetPort`, so pointing it at ShowMesh points it away from whatever the
operator had. **ShowMesh must never rewrite that preference**, and an observation
channel that requires taking a resource away from the operator is not one this
project should build.

### 3.3 Every reference is an object id. Positional addressing is not offered.

Clips, layers, columns and decks are referenced by their Resolume object id, via
`/composition/{kind}/by-id/{id}`. **The adapter exposes no positional addressing at
all**, which is Track D's own rule ("offering both means someone eventually uses
the fragile one") applied to the transport that can actually honour it.

This is safe to build on. Object ids are persisted in the composition file and
survive edits, re-saves and a year-over-year rebuild: 246 clip ids carried from
`Christmas 24` to `Christmas 25`. A replaced clip loses its id and a stored
reference then returns **`404`, which is a stale reference announcing itself**.

Two limits are stated rather than discovered:

- **A `404` on a stored id means the composition changed underneath us**, not that
  the request was malformed. It is a signal, not an error, and §6.4 says what to do
  with it.
- **Layer ids are shared between the operator's own shows**, because he builds one
  from the other: `Halloween 2025` and `Christmas 25` share 21 layer ids and zero
  clip ids. **A layer id may never be used to assert which composition is loaded.**

### 3.4 One authority for confirmation. The WebSocket is a wake-up, never an authority.

This is the decision most likely to be got wrong, because it looks like a merge and
is not.

| Channel | Role | Why not more |
|---|---|---|
| `GET /composition/{kind}/by-id/{id}` | **the authority** | complete, correct, keyed on identity that survives restart |
| WS `parameter_update` | wake-up | its parameter ids are session-scoped and die on every restart with nothing announcing it (§3.6). As an authority it goes permanently silent, which is indistinguishable from a quiet system |
| WS full composition push | wake-up, and a bare bit | reliable, but 2.27 MB per structural change |
| OSC | nothing | no reply exists (§3.1) |

**No observed value is ever taken from a WebSocket message.** A WS message means
"read again now"; the read is what produces evidence.

The reason this is a decision and not an implementation note is Step 7's
`precedence.go` defect, which is in LESSONS.md: a shared resolver existed, its own
comment claimed that resolving there made inconsistent application impossible, and
the command confirmation path one file over took the first matching row instead.
**One authority plus wake-ups means there is nothing to arbitrate**, so that class
of defect has no place to live here.

The full push is **not unmarshalled**. It is recognised by the absence of a `type`
field, counted, and discarded. Parsing 2.27 MB per clip launch to learn a fact a
6.8 KB targeted read states directly is work the adapter must not do.

### 3.5 The confirmation deadline is derived per action from readable state

A single constant is wrong. Deadlines are computed, not configured:

| Action class | Deadline |
|---|---|
| Anything that makes a clip start, or sets a parameter | a short fixed budget, defaulting to **2 s** |
| Anything that makes a clip stop | `layer.transition.duration` + a fixed margin, defaulting to **1 s** |
| Blackout across layers | **max** over the affected layers' `transition.duration`, + the same margin |

`transition.duration` is read from state the adapter already holds. The measured
overshoot past the transition was 75–113 ms across every run, so a 1 s margin is
roughly an order of magnitude of headroom and should be a named constant rather
than a literal.

**A deadline expiring produces `unconfirmed` with a stated reason, never `failed`.**
That is [Step 9's specification](STEP-9-SPEC.md) §2.2 applied here before it can be got wrong: `failed`
is a statement about the show, `unconfirmed` is a statement about ShowMesh's own
evidence pipeline, and this architecture degrades toward the show continuing.

### 3.6 A parameter id is never persisted, cached across a reconnect, or written to configuration

Measured across a restart: **object ids 14/14 identical, parameter ids 0/14
identical.** Parameter ids are minted at composition load.

So parameter ids live only in memory, only for the lifetime of one WebSocket
connection, and are re-resolved from a fresh composition read every time the
connection is established. A parameter id must not appear in SQLite, in a config
revision, in an export bundle, or in an API payload.

**A subscription to a dead parameter id does not error. It goes quiet.** Nothing
distinguishes it from a parameter that has not changed, which is precisely the
shape [ADR-020](../decisions/ADR-020-control-api-shape-and-change-stream.md)
refuses to trust in ShowMesh's own change stream, for the same reason.

### 3.7 Layer readiness is a conjunction, computed in exactly one place

**`connected` is not evidence that anything reached the output.** Measured: a clip
on a bypassed layer, and a clip on a layer at `master` 0, both report `Connected`
with `active_clip` present, and nothing on the clip says otherwise.

A layer can put content on the wall only when all of these hold:

```
layers[i].bypassed        == false
layers[i].master           > 0
layers[i].video.opacity    > 0
layergroup(i).bypassed    == false
layergroup(i).master       > 0
composition.bypassed      == false
composition.master         > 0
```

**This lives in one function and every caller uses it**, for the §3.4 reason. When
it is false, the observation states *which* term failed, because "layer 7 is not
ready" and "layer 7 is bypassed" are different amounts of help at 17:00.

Not determined by the capture and therefore **not** in the conjunction:
`crossfadergroup` assignment combined with crossfader position may be an eighth way
to silence a layer that passes all seven. It is an open item (§10), and the
observation must not claim completeness it has not earned.

### 3.8 Composition identity is asserted structurally. Never by name.

Three independent facts make the obvious check wrong:

- The composition object has **no `id` field** in REST. The only identifier is
  `name`, a mutable `ParamString`.
- The composition-level `uniqueId` in the file is **the same constant in all six**
  of the operator's compositions. It identifies the installation, not the
  composition.
- During the ~1.2 s load window after a restart, **the name is already correct
  while the composition is not** (§7.2).

So "the right composition is loaded" is asserted by **the expected clip ids
resolving**, optionally with layer, column and deck counts as a cheap pre-filter.
Clip ids are disjoint between the operator's shows; layer ids are not (§3.3).

**This replaces Track D's page tripwire**, which §2b and §2c retired. What survives
of the intent, and what the adapter still owes:

- **The composition is not what ShowMesh expects** → visible evidence, never a
  refusal. The operator may legitimately have loaded something else.
- **The selected deck changed between a decision and its confirmation** →
  recorded on the action's outcome. `decks[i].selected` is in state the adapter
  already reads. This is the only surviving form of the page race and it is now
  cheap, because §3.3 means the action itself was never deck-dependent.

### 3.9 With ShowMesh stopped, Resolume keeps doing what it was doing

Free, and stated so it is checked rather than assumed. The adapter is not in the
frame path, holds no lock, and drives nothing continuously. **Killing the
coordinator mid-show leaves the wall exactly as it is**, which is Track D's
acceptance criterion and standing constraint 6.

The corollary is the one that needs building for: **Arena comes back from a restart
with nothing playing.** Measured. Resolume will not resume the show, so whatever
re-establishes playback is ShowMesh or the operator, and §7.2's observation has to
make that visible rather than leaving the operator to notice from the yard.

## 4. The seam to cut first: read before write

**The collector ships before a single action exists**, and is verified against a
running Arena, on the same reasoning that put Step 3 before Step 7 and for the
reason [ADR-011](../decisions/ADR-011-context-aware-observability.md) states: this
project monitors before it controls.

Four seams, in order. The first two are the deliverable; the second two are only
safe once the first two are real.

| Seam | Contents |
|---|---|
| **D-1** | The Resolume REST client, the WebSocket change signal, the object-id resolver, and the reachability observation. No composition semantics. |
| **D-2** | Observations (§5), including layer readiness (§3.7) and composition identity (§3.8), on the dashboard with provenance and freshness. |
| **D-3** | The action vocabulary (§6) with confirmation (§3.4, §3.5). |
| **D-4** | `showmeshctl` coverage and the Operator UI controls. |

Placement follows the existing shape: the collector goes in
`internal/coordinator/collector/` beside the FPP REST collector and behind the same
source-neutral interface; observations use `pkg/observation` unchanged. **Nothing
new is invented at the observation layer** — if Resolume needs a concept
`pkg/observation` does not have, that is a finding to report before building it.

## 5. Observations

Signal naming follows the FPP collector's convention. `ObservedAt` is a pointer and
`nil` means the time is genuinely unknown; it is never defaulted to collection
time.

| Signal | Type | Notes |
|---|---|---|
| `resolume.reachable` | health | transport-level only. §7.2 is why this is not enough on its own |
| `resolume.product` | string | `name`, `major.minor.micro`, `revision` from `/product`. A change here is a revalidation trigger for this whole specification |
| `resolume.composition.name` | string | stated as a display value and explicitly **not** an identity (§3.8) |
| `resolume.composition.identified` | evidence | whether the expected clip ids resolve. `unknown` while loading, never healthy |
| `resolume.composition.decks` | int | deck count, plus the selected deck's name and id |
| `resolume.layer.<id>.ready` | evidence | §3.7's conjunction, with the failing term named when false |
| `resolume.layer.<id>.active_clip` | string / absent | clip id and name, or explicit absence with a reason. Never omitted |
| `resolume.clip.<id>.connected` | state | the five-state `ParamState`, not a boolean |
| `resolume.clip.<id>.transporttype` | string | `Timeline`, `SMPTE 1`, … Feeds §8's drift check |

Two rules carried from ADR-020 and from Step 3's review:

- **Absent evidence is stated, never omitted.** A layer with no active clip
  publishes an absence with a reason, because a missing field renders as blank and
  blank reads as fine.
- **`connected` is a five-state value.** A predicate written `== "Connected"`
  misses `Connected & previewing`. Column `connected` is a *different* three-state
  set. Neither may be modelled as a boolean.

## 6. The action vocabulary

Logical actions per [ADR-029](../decisions/ADR-029-logical-actions-and-integration-bindings.md).
Every one takes a ShowMesh reference that resolves to a Resolume object id; **no
macro ever contains a Resolume path or id.**

| Action | Call | Confirming evidence |
|---|---|---|
| `launchClip` | `POST /composition/clips/by-id/{id}/connect` | clip `connected` ∈ {`Connected`, `Connected & previewing`} **and** the owning layer's `active_clip.id == id` |
| `clearLayer` | `POST /composition/layers/by-id/{id}/clear` | layer `active_clip` is absent |
| `blackout` | `POST /composition/disconnect-all` | every tracked layer's `active_clip` is absent |
| `launchColumn` | `POST /composition/columns/by-id/{id}/connect` | column `connected == Connected` |
| `selectDeck` | `POST /composition/decks/by-id/{id}/select` | that deck's `selected == true` |
| `setLayerBypass` | `PUT /parameter/by-id/{bypassed}` | layer `bypassed` equals the requested value |
| `setLayerMaster` | `PUT /parameter/by-id/{master}` | layer `master` equals the requested value |

### 6.1 `launchClip` confirms on identity, not just on "something is playing"

The layer's `active_clip.id` must equal the requested clip. `connected` alone
credits ShowMesh with a clip the operator launched by hand, which is Step 8's
`startPlaylist` reasoning in a second system, and here it is likelier rather than
less: the operator has a keyboard in front of the same composition.

### 6.2 The limit on `launchClip`, stated rather than hidden

**Re-launching a clip that is already playing has no confirming predicate.** The
observable is already at the desired value before dispatch, so post-dispatch
evidence proves nothing. That is Step 7's 179-microsecond defect, and it cannot be
predicated away with the signals in §5.

The action carries that limit in its outcome reason, exactly as
`nextPlaylistItem`/`prevPlaylistItem` do for FPP.

**One candidate, offered rather than specified:** `transport.position` is readable
and a launch restarts a clip from its in-point, so position moving backwards across
dispatch would be genuine evidence of a re-trigger. It was not measured, it depends
on `playmode` and in-point, and it must be benched before being relied on. Do not
implement it on the strength of this paragraph.

### 6.3 Never issue `connect` with body `false`

It returns **204 and does nothing**. It is mouse-up, not disconnect. Measured. The
disconnect operations are `clear` and `disconnect-all`, and they are the only ones
in §6.

### 6.4 A `404` on a stored id is a composition change, not a failed command

It aborts the action as `failed` with the reason stated as a stale reference, and
it triggers a fresh full composition read to re-resolve everything (§3.6), and it
sets `resolume.composition.identified` to false. It does **not** retry, and it does
**not** fall back to a positional path.

## 7. Two behaviours that must be built for, not discovered

### 7.1 A `204` is an acknowledgement, never evidence

Resolume is better than FPP here: a command against a target that does not exist
genuinely 404s. It is not perfect: §6.3's `connect false` is a 204 that does
nothing, and one `POST …/connect` was observed returning 204 while leaving the clip
disconnected, not reproduced in five subsequent clean attempts and cause unknown.

ADR-003 handles both without special-casing either.

### 7.2 Reachable is not ready, and this is the sharpest hazard in the step

Measured across three restarts, continuous polling from process launch:

```
t=3.65s  first successful GET /composition  ->  name=''             layers=3   110,009 bytes
t=4.15s                                         name='Christmas 25' layers=3   110,080 bytes
t=4.85s                                         name='Christmas 25' layers=18  2,258,988 bytes
```

**For ~1.2 s the API answers `200 OK` with a complete, well-formed composition that
is not the show, and for the last 0.7 s of that it carries the correct name.**
There is no `loading` field, no status, no generation counter.

So the obvious readiness check — poll until it answers, then verify the name —
**passes while 15 of 18 layers do not exist.** That is ADR-011's rule in a new
subsystem: evidence that is fresh, well-formed, and wrong.

`resolume.reachable` therefore never implies `resolume.composition.identified`, the
two are separate signals, and **no action may be dispatched on reachability
alone.**

The owner's operating note bounds how often this happens without removing it:
Resolume runs 24/7 once the show is up, and the only restarts are deliberate ones
or a new composition version arriving from another computer. **Both are planned
changes made shortly before or during a show**, which is exactly when a readiness
check is being read.

## 8. Readiness evidence, and one thing it should offer to fix

Track D requires that an inactive layer is reported as a readiness fault **before**
a show rather than discovered when timecode arrives and nothing launches. §3.7 is
that check.

A second one falls out of the capture and is specified as a **candidate, not a
commitment**, because it depends on configuration that does not exist yet. The
composition states which clips are SMPTE-capable (244 of 252) and which are
currently on SMPTE transport (0 of 252 at capture time), and `transporttype` is
writable over REST. So ShowMesh can tell an operator on show day that a clip which
should be following timecode is not, and offer to put it back.

**It needs ShowMesh to know which clips are *supposed* to be on SMPTE**, which is
show configuration owned by Track E. Until that exists this is a read-only
observation (§5's `resolume.clip.<id>.transporttype`) and nothing more.

**ShowMesh does not otherwise write Resolume configuration.** The owner's position,
2026-08-14: Resolume configuration belongs to the programmer, in Resolume, before
the show.

## 9. What is not built

Recorded so each is a decision rather than an omission discovered in October.

- **Anything in the timecode path.** That is D0 and it needs a generator and a
  cable.
- **OSC, in either direction** (§3.1, §3.2).
- **Any write to Resolume preferences**, including `osc.xml` and `server.xml`.
- **Composition loading**, which has no REST path in 7.23.2.
- **A preview or output image.** There is no rendered-output endpoint; only
  per-clip thumbnails exist. [RES-010](../research/RES-010-projection-preview-monitoring.md)
  is unaffected by this step.
- **Shortcut, binding or preset management.** No API exists.
- **`POST /composition/action`.** It is undo/redo, it is reachable, and ShowMesh
  has no business issuing it.
- **Anything destructive.** No `DELETE` on layers, columns, decks or clips. The API
  offers them and this adapter does not call them.

## 10. Acceptance criteria

Proved against a running Arena and a running coordinator, not against the test
suite. Track D's own criteria are marked with which are in scope here.

1. The collector reads a real Arena and publishes every §5 signal with provenance
   and freshness, and a disconnected Arena renders `unknown` rather than healthy.
2. **A layer that is bypassed, or at zero master, reports not-ready while a clip on
   it reports `Connected`** — the §3.7 conjunction demonstrated against the state
   that fools the naive check. *(Track D: "an inactive layer is reported as a
   readiness fault before a show.")*
3. **A clip launch, a layer clear and a column trigger are each confirmed by
   Resolume reporting the result**, on evidence post-dating dispatch. *(Track D,
   adapted: the criterion says "activates a layer"; §6 exposes bypass and master,
   which is what activation turned out to mean.)*
4. **A blackout on a layer with a 2.5 s transition confirms**, and the same
   blackout with the transition set to 0.1 s confirms faster, demonstrating the
   deadline is derived and not constant.
5. **Arena is restarted while the adapter is connected**, and the adapter neither
   reports the show as present during the load window nor dispatches an action into
   it. It re-resolves parameter ids and resumes without a coordinator restart.
6. **A stored clip id that no longer exists produces a stated stale reference**, not
   a retry and not a positional fallback.
7. Resolume state appears in the Operator UI with provenance and freshness, and
   `showmeshctl` can drive every §6 action, per
   [ADR-030](../decisions/ADR-030-operator-ui-is-the-authoring-surface.md).
8. **With the coordinator stopped mid-clip, Resolume keeps playing.** *(Track D,
   and standing constraint 6.)*

Deferred to D0 and explicitly not claimed by this step: timecode loss producing a
defined operator-visible response, and RES-001's test matrix moving off L0.

## 11. Open questions

Answer before or during the build; none of them blocks starting.

- **Does the crossfader silence a layer that passes §3.7's conjunction?** If yes it
  is an eighth term. Measurable in minutes against the running Arena.
- **Does a composition swap without a restart have its own load window?** §7.2 is a
  restart measurement. Given the restart result, assuming the swap is atomic would
  be unwise.
- **Which clips does ShowMesh track?** Everything in the composition is 252 slots
  and 30 non-empty clips; the show configuration Track E owns will name far fewer.
  Until it exists, "every non-empty clip" is the honest default and it is cheap.
- **Does `transport.position` reset on re-launch** (§6.2), and is it usable as
  evidence.
- **How does this adapter relate to
  [ADR-016](../decisions/ADR-016-controlled-devices-and-control-providers.md)'s
  provider model?** Resolume has no agent, no advertisement and no LWT, which fits
  the controlled-device class, but the provider model is deferred and
  [RES-014](../research/RES-014-control-provider-model.md) is unresearched. This
  specification treats Resolume as an ADR-029 integration target with a
  purpose-built adapter, which is the smaller commitment and is reversible.
