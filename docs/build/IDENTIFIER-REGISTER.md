# Identifier register

[Build plan](BUILD-PLAN.md) · [Build log](BUILD-LOG.md) · [ADR register](../decisions/README.md)

**This file is the authoritative register for every scarce identifier in
ShowMesh except ADR numbers, which live in
[`docs/decisions/README.md`](../decisions/README.md).**

CLAUDE.md's multi-track rule says the orchestrator alone mints scarce
identifiers, "from the authoritative registers." Until 2026-08-17 only one
such register existed. That is why two branches minted ADR-033 and ADR-034
independently on 2026-08-14, leaving 034 permanently unused, and why
`showmeshctl` exit codes 11 and 12 were issued twice and had to be
renumbered to 14 and 15 on the merge.

## How to use this file

**Builders never edit this file.** A builder that needs an identifier
requests one from the orchestrator, names what it is for, and waits.

**The orchestrator assigns from here before the work begins**, not at merge.
Assigning at merge is assigning after both branches have already written
the value into code, tests, help text, and documentation.

**A reserved identifier is taken.** An identifier reserved for planned work
is as unavailable as one already shipped. The `Status` column says which.

**Reserving costs nothing and collisions cost a rename across a whole
branch.** When in doubt, reserve.

---

## `showmeshctl` exit codes

Defined in `cmd/showmeshctl/problem.go` and documented in `--help`
(`usage.go`). Scripts branch on these, so a reused number is a wrong branch
taken silently.

| Code | Name | Status |
|---|---|---|
| 0 | `exitOK` | shipped |
| 1 | `exitUsage` | shipped |
| 2 | `exitUnreachable` | shipped |
| 3 | `exitUnauthorized` (401) | shipped |
| 4 | `exitVersionIncompatible` | shipped |
| 5 | `exitNotFound` | shipped |
| 6 | `exitAPIError` | shipped |
| 7 | `exitForbidden` (403) | shipped |
| 8 | `exitRateLimited` (429) | shipped |
| 9 | `exitCommandUnconfirmed` | shipped |
| 10 | `exitConflict` (409) | shipped |
| 11 | `exitActionUnconfirmable` | shipped |
| 12 | `exitActionFailed` | shipped |
| 13 | `exitActionRefused` | shipped |
| 14 | `exitFollowStillWatching` | shipped (was 11 on the Step 9 branch) |
| 15 | `exitMacroRunAborted` | shipped (was 12 on the Step 9 branch) |
| 16 to 19 | unallocated | free |
| 20 | `exitAssetsNotReady` | shipped |
| 21 | `exitAssetsUnknown` | shipped |
| 22 | `exitRenderUnavailable` | shipped (Track B seam B4) |
| 23 | `exitRenderPipelineDown` | shipped (Track B seam B2) |
| 24 | `exitAudioDeviceUnavailable` | reserved (Track C seam C1) |
| 25 | `exitAudioSessionFailed` | reserved (Track C seam C3) |
| 26 | `exitNightNotReady` | reserved (Track F seam F2) |
| 27 | `exitNightStateRejected` | reserved (Track F seam F2) |
| 28 | `exitNightAmbiguous` | reserved (Track F seam F2) |
| 29 | `exitActionBindingBroken` | reserved (Track E seam E7) |
| 30+ | unallocated | free |

**16 to 19 are deliberately free.** The asset codes were placed at 20 to
leave room below them; do not close that gap without a reason.

**`exitActionBindingBroken` is a distinct code because a broken binding is
not an action that failed.** Codes 11 to 13 say a dispatched action did not
confirm, could not be confirmed, or was refused; 29 says the action was
never dispatchable, because what it names no longer exists in the
integration (ADR-029's own consequence: an action bound to a deleted clip).
A pre-show script wants to branch on that difference. Track E seam E7's
invoke verb reuses 9 and 11 to 13 unchanged — the ADR-020 outcome vocabulary
does not fork per surface.

## Configuration kinds

`config_objects.kind` and `config_revisions.kind`, used verbatim as the
second path segment of `/api/v1/config/<kind>`. Defined in
`internal/coordinator/config/`.

| Kind | Object id | Status | Owner |
|---|---|---|---|
| `fpp.endpoints` | `default` singleton | shipped | Step 7 seam A |
| `resolume.recovery` | singleton | shipped | Track D seam D-3a |
| `show` | operator-chosen | shipped | Track E |
| `show.active` | singleton | shipped | Track E |
| `show.mode` | `default` singleton | shipped | ADR-033 |
| `show.surface` | operator-chosen | shipped | Track E |
| `show.action` | operator-chosen | shipped | Step 9 |
| `show.macro` | operator-chosen | shipped | Step 9 |
| `show.cue` | operator-chosen | shipped | Track H seam H1 |
| `show.playlist` | operator-chosen | shipped | Track H seam H1 |
| `resolume.instances` | `default` singleton | shipped | Track G seam G-2 |
| `fpp.mqtt` | `default` singleton | shipped | Track G seam G-3 |
| `assets.settings` | `default` singleton | shipped | Track G seam G-4 |
| `render.settings` | `default` singleton | shipped | Track B seam B2 |
| `audio.settings` | `default` singleton | shipped | Track C seam C1b |
| `audio.node` | operator-chosen (the node id) | shipped | Track C seam C1b |
| `night.session` | operator-chosen | reserved | Track F seam F1 |
| `night.session.active` | `default` singleton | reserved | Track F seam F1 |

**Track B deliberately mints no per-surface kind.** `show.surface` already
exists (Track E) and Track B consumes it unchanged. `render.settings` holds
only what is renderer-wide and operator-settable, the sync-loss output
behaviour being the first entry. **Track B must not add a field to
`show.surface` tonight**: Track G seam G-8 is building that object's
Operator UI in a parallel worktree, and an additive payload field plus a new
UI over the same object is the collision this register exists to prevent.
A per-surface override of the renderer default is future work, sequenced
after G-8 folds.

**Track C's two kinds split engine-wide defaults from per-node physical
binding.** `audio.settings` holds what is engine-wide and operator-settable
(drift ignore threshold, LTC frame rate, default fade curve and duration,
default background gain ceiling). `audio.node` holds what is true of one
node's installed interface: which discovered output route carries program,
which carries LTC, and the declared clock domain. Both are store-backed
because ADR-039's test is temporal — the coordinator reads them from its own
store, so they may not be environment variables, and "which output is LTC"
is exactly the class of value that shipped as an environment variable in the
Resolume case and left the subsystem unconnectable from every operator
surface. **Track C mints no audio-playlist configuration kind**: Track H owns
the show-level `show.playlist` authoring object, while Track C's `PlaylistRef`
remains an execution primitive beneath the `showmesh-audio` runner.

**Track F mints two kinds and no Playlist kind.** `night.session` is
operator-chosen because a deployment holds more than one (the Christmas and
the Halloween definition differ in content and in FPP playlist references
while sharing a shape), and `night.session.active` is the singleton pointer
saying which one a session activation pins, on the `show`/`show.active`
precedent. `night.session` references and pins a same-show `show.playlist`
revision for background audio; Track H is the only authoring path for that
ordered list. **`night.session`
carries no calendar field of any kind** (ADR-038 decision 1), and F1's
validation rejecting one is part of the kind rather than a later check.
`night.session.active` may be released back to `free` if the active-show
reference turns out to be sufficient to resolve the session; it is reserved
now because releasing an unused reservation is free and colliding is not.

**Track H mints exactly two kinds.** `show.cue` is the stable show intent for
one synchronized playback item. `show.playlist` is the ordered program whose
entries reference same-show Cues and declare a runner. The Audio Engine's
`PlaylistRef` is not registered because it is not an authored configuration
kind. An FPP playlist name, index, filename, or imported hash belongs inside
the runner binding of `show.playlist`; none becomes a new global kind or a
Cue id.

**Track E seam E7 mints no configuration kind, no schema version, no change
stream event kind and no observation signal, and that is the finding rather
than an omission.** ADR-029's logical action and its binding are already
`show.action` and `show.macro.steps[].action`, shipped in Step 9 and
extended by Track D seam C. E7 is the part of ADR-029 that was never built
on top of them: invoking one action outside a macro, checking a binding
still resolves before a show rather than at showtime, and enforcing the
ADR-027 show namespace those objects have only ever range-checked. A
binding check is derived from configuration the coordinator already holds,
so it is a computed read on the asset-manifest precedent, not an observation
with provenance and freshness. If a builder finds itself wanting a new kind
here, the design has drifted and the orchestrator decides, not the builder.

**ADR-033 mints exactly one kind, `show.mode`, and nothing else.** It is the
installation-wide operating mode, `program` or `show`, held as a singleton
because ADR-033 decision 1 says there is exactly one value for the
installation: not per-node, not per-device, and not per-subsystem. Its two
members are a closed enum, and a third member requires an amendment to
ADR-033 rather than a payload that happens to parse.

**`show.mode` is a different thing from `show.active`**, which names which
Show is currently programmed. `show.active` says what is loaded;
`show.mode` says whether the installation is being programmed or is running.
The register lists them adjacently for that reason: an implementer reaching
for one when they meant the other is the collision worth catching here.

**ADR-033's build mints no other identifier.** No exit code: `showmeshctl
show mode` reads and writes an ordinary configuration kind and reuses the
existing outcome vocabulary. No agent operation name: the mode reaches nodes
as one retained, installation-wide message on the `showmesh/events` family,
never as a per-node command, so nothing is added to `newOperationRegistry`.
No new authorization scope: the write reuses `config:write`, and the
current-value read reuses `observation:read` rather than inventing the
`config:read` ADR-024 decision 4 deliberately does not define. No new
observation signal or resource kind: nothing publishes the mode as
observed state in this build. No new audit action: the write is
`config.write` targeting `show.mode`, like every other configuration write.

Note the Resolume composition is **not** a configuration kind. It is stored
behind `/api/v1/config/resolume/composition` with its own upload path
(ADR-032), and the path shape differs deliberately.

### show.action target integrations

`show.action.target.integration`, defined in
`internal/coordinator/config/showaction.go`. ADR-029: a macro or night-
session cue invokes the named action, and the action's own adapter owns
the protocol underneath it — a new member here is a new integration an
operator can bind an action to, not a new way to reach one that already
exists.

| Integration | Status | Owner |
|---|---|---|
| `fpp` | shipped | Step 9 |
| `resolume` | shipped | Track D seam C |
| `mqtt` | shipped | Step 9 |
| `audio` | shipped | Track F seam F5 |

`audio`'s own `target.audioAction` names one of the already-registered
`audio.session.*`/`audio.gain.*`/`audio.output.*` operation names in this
file's own "Agent operation names" table; it mints no new operation name.

## Authorization scopes

Defined in `internal/coordinator/identity/types.go`. Roles are named
bundles of these (ADR-024).

| Scope | Status | Guards |
|---|---|---|
| `node:read` | shipped | node reads |
| `fpp:read` | shipped | FPP reads |
| `observation:read` | shipped | observation reads |
| `event:read` | shipped | event reads |
| `show:macro:run` | shipped | macro run submission |
| `device:power` | shipped | controlled-device power |
| `fpp:command` | shipped | the eight FPP primitives |
| `fpp:observe` | shipped | SM-63/Track H: authenticated FPP playlist-entry observation ingestion |
| `node:observe` | shipped | Track H seam H3: authenticated node cue-catalog acknowledgement |
| `cuecatalog:deploy` | shipped | Track H seam H3: pushing a resolved cue catalog to a node |
| `config:write` | shipped | every configuration write |
| `audit:read` | shipped | audit log reads |
| `resolume:action` | shipped | the seven Resolume actions |
| `asset:write` | shipped | asset upload |
| `render:command` | shipped | Track B seam B2: surface apply/clear, pipeline restart |
| `principal:write` | shipped | the nine principal/token administration writes (Track G seam G-5) |
| `principal:read` | shipped | principal/token/audit administration reads (Track G seam G-5) |
| `audio:command` | reserved | Track C seams C3/C4: session, gain, fade, mute |
| `night:command` | reserved | Track F seam F2: the ADR-038 lifecycle command vocabulary |
| `night:override` | shipped | Track F seam F6: interlock override where a rule declares `authorized-operator` (force-power-off instead reuses `show:action:invoke`, see RESTING-MODE.md §10.4) |
| `show:action:invoke` | reserved | Track E seam E7: dispatching one named logical action outside a macro run |

**`night:override` is separate from `night:command` deliberately.** RESTING-MODE
§10.1 accepts an override only when the rule itself declares
`authorized-operator` *and* the caller holds the required permission; folding
that into `night:command` would mean every principal that can start a night
can also bypass a blocking interlock, which is the opposite of what a
blocking interlock is for. Night-session **reads** stay open per ADR-024
constraint 23; a credential problem never costs the operator visibility of
the lifecycle state.

`principal:write` sat in the admin bundle unchecked from Step 6 until Track
G seam G-5 landed its first callers (merged 2026-08-17).

**`show:action:invoke` is separate from `show:macro:run` deliberately, and
from the per-integration scopes as well.** A macro run is a submission of a
reviewed, revision-pinned sequence; invoking one action is an operator
pressing a button on the wall right now, and the two want to be grantable
independently. It is also not `resolume:action` or `fpp:command`: the whole
point of ADR-029 is that the caller names an action and never learns which
protocol it reaches, so a scope named after a protocol would leak the
binding back into the authorization model. Reads of an action, its
bindings, and their validation stay open per ADR-024 constraint 23.

**It is umbrella authority, not conjunctive** (owner, 2026-08-19, Linear
SM-104). A principal holding `show:action:invoke` may invoke any stored
action, including ones that reach FPP or Resolume, without also holding
`fpp:command` or `resolume:action`. This paragraph previously claimed the
opposite, that the dispatch path underneath still checked the
per-integration scope, and **that was simply false**: authorization lives
entirely in the HTTP write guard, and neither `dispatchFPPCommand` nor the
Resolume dispatcher checks a scope. The register described a security
property the code did not have, which is worse than describing none.

The ruling follows the shape rather than the plumbing. A caller selects a
stored logical action and supplies no protocol parameters, so what it is
authorized to do is bounded by what an operator already authored and an
administrator already accepted. That is the same argument `show:macro:run`
rests on, and it is ADR-029's own: the caller names an action and never
learns which protocol it reaches, so requiring a protocol-named scope would
reintroduce the binding the ADR exists to hide. The grant to watch is
therefore `config:write`, which decides what an action may be bound to, not
this one.

## Collector source ids

The `source` field on an observation and the id in `GET /api/v1/snapshot`'s
`collectors` list. A duplicate id makes an out-of-band poll nudge retarget
the wrong device, because every collector shares one
`internal/coordinator/collector.Runner` keyed by id.

| Id | Status | Package |
|---|---|---|
| `fpp-rest` | shipped | `collector/fpp` |
| `fpp-mqtt` | shipped | `collector/fppmqtt` |
| `resolume-rest` | shipped | `collector/resolume` |
| `resolume-survey` | shipped | `collector/resolume` (composition-derived) |
| `node-render` | shipped | Track B seam B2 (`collector/noderender`) |
| `node-audio` | shipped | Track C seam C1a (`collector/nodeaudio`) |

**Instance ids share this namespace.** A Resolume instance id must not
collide with any FPP endpoint id, for the same `Runner` reason. Validation
enforces it (`config.ValidateResolumeIDAgainstFPPEndpoints`); this table is
not the enforcement, it is where a builder looks before choosing.

## Agent operation names

The keys of `newOperationRegistry` in `internal/agent/command.go`. **That map
is the ARCHITECTURE §10.4 allowlist and there is no second path**, so a name
here is a name the node will execute. The value also travels as
`CmdPayload.Action` and is recorded in the audit trail, which makes a rename
after shipping a breaking change to stored history.

| Operation | Status | Owner |
|---|---|---|
| `agent.echo` | shipped | Track B seam B1 |
| `asset.fetch` | shipped | Track E |
| `render.surface.apply` | shipped | Track B seam B2 |
| `render.surface.clear` | shipped | Track B seam B2 |
| `render.pipeline.restart` | shipped | Track B seam B2 |
| `render.transport.probe` | shipped | Track B seam B4 |
| `audio.session.apply` | reserved | Track C seam C3 |
| `audio.session.prepare` | reserved | Track C seam C3 |
| `audio.session.start` | reserved | Track C seam C3 |
| `audio.session.pause` | reserved | Track C seam C3 |
| `audio.session.resume` | reserved | Track C seam C3 |
| `audio.session.seek` | reserved | Track C seam C3 |
| `audio.session.advance` | reserved | Track C seam C3 |
| `audio.session.stop` | reserved | Track C seam C3 |
| `audio.session.clear` | reserved | Track C seam C3 |
| `audio.gain.set` | reserved | Track C seam C4 |
| `audio.gain.fade` | reserved | Track C seam C4 |
| `audio.output.mute` | reserved | Track C seam C4 |
| `audio.output.unmute` | reserved | Track C seam C4 |
| `audio.device.probe` | shipped | Track C seam C1a |
| `audio.media.probe` | reserved | Track C seam C2 |
| `audio.node.configure` | shipped | Track C seam C5 |
| `audio.settings.configure` | shipped | Track C seam C5 |
| `cuecatalog.deploy` | shipped | Track H seam H3: the coordinator pushing a resolved Cue catalog onto a node over the existing MQTT command path (build ruling: the agent has no configured coordinator base URL to fetch one from) |
| `cue.activate` | shipped | Track H seam H4: a runner-neutral Cue activation envelope carried over the existing MQTT command path, authorized against the node's held Cue catalog and applied to rendering, audio, and LTC |

**AUDIO-ENGINE §14's `select_media`, `select_playlist`, `set_loop`,
`announce` and `duck` mint no operation of their own.** §14 permits combining
operations where confirmation and idempotency stay unambiguous, and all five
are properties of the session being applied: an announcement is
`audio.session.apply` with source role `announcement` and a declared
`mix`/`duck`/`interrupt` policy, followed by `audio.session.start`. A
separate `audio.session.duck` would be a second way to reach the same state
with no way to say which one won.

## Audit action strings

The `Action` field of an `identity.AuditEntry`, written into the audit
table and read back by `GET /api/v1/audit`, the `showmeshctl audit` verb,
and the Operator UI's audit view.

**This section exists because the register had no place for these until
2026-08-18**, when a review of Track E's asset rollback asked where its new
`asset.rollback` string had been reserved. The answer was nowhere. The
"Agent operation names" section above already states the reason this
matters, and it applies here word for word: the value **is recorded in the
audit trail, which makes a rename after shipping a breaking change to
stored history.** An operator asking what happened on the night of the 17th
is reading strings minted months earlier.

**There is no central constant.** Every call site passes a literal, so
nothing collides at compile time and nothing would catch two branches
minting one name with different meanings. That is the same condition that
produced the exit code 11 and 12 collision, minus the crash.

Written from the code on 2026-08-18 by enumerating `identity.AuditEntry`
construction sites, following this file's own correction rule that a
register entry comes from the code and never from a plan.

| Action | Status | Owner |
|---|---|---|
| `bootstrap.claim` | shipped | Step 6 |
| `session.create` | shipped | Step 6 |
| `session.revoke` | shipped | Step 6 |
| `credential.resolve` | shipped | Step 6 |
| `credential_in_url` | shipped | Step 6 |
| `principal.create` | shipped | Step 6 |
| `principal.enable` | shipped | Track G seam G-5 |
| `principal.disable` | shipped | Track G seam G-5 |
| `principal.set_role` | shipped | Track G seam G-5 |
| `principal.reset_password` | shipped | Track G seam G-5 |
| `config.write` | shipped | Step 7 |
| `config.migrate` | shipped | Step 7 |
| `node.declare` | shipped | Step 7 |
| `node.declaration.delete` | shipped | Step 7 |
| `discovery.run.start` | shipped | Step 7 |
| `fpp.start_playlist` and the seven other `fpp.<primitive>` names | shipped | Step 8 |
| `macro.run.submit` | shipped | Step 9 |
| `mqtt.publish` | shipped | Step 9 |
| `resolume.<action>` (seven, via `resolumeActionAuditAction`) | shipped | Track D seam D-3 |
| `resolume.recovery_restore` | shipped | Track D seam D-3a |
| `render.surface.apply` / `render.surface.clear` / `render.pipeline.restart` / `render.transport.probe` | shipped | Track B |
| `asset.upload` | shipped | Track E |
| `asset.fetch` | shipped | Track E |
| `asset.rollback` | shipped | Track E, ADR-028 decision 10 |
| `fpp.observe_playlist_entry` | shipped | SM-150, RES-018 section 6.3: written on a REFUSED ingestion only |
| `fpp.instance_uuid.acknowledge` | shipped | per-endpoint observed FPP instance uuid conflict acknowledgment |
| `cuecatalog.acknowledge` | shipped | Track H seam H3: a node's cue-catalog acknowledgement |
| `cue.activate` | shipped | Track H seam H4: the coordinator's own dispatch of, or independent `pkg/cueauth` refusal of, one node's cue.activate command — the same action string the Agent operation names table above already reserves, reused here for its audit entries (Kind distinguishes dispatch from refusal) |

**Two naming conventions are in use and neither is being changed
retroactively.** Most names are `<noun>.<verb>` with an underscore inside
the verb (`principal.reset_password`, `fpp.stop_playlist_gracefully`).
`credential_in_url` has no noun segment at all. Renaming any of them
rewrites the meaning of history that is already stored, so the rule going
forward is `<noun>.<verb>`, and the existing outliers stay.

**Four of these names are shared with other namespaces, deliberately and
harmlessly.** `asset.fetch`, the four `render.*` names, and `cue.activate`
are also agent operation names (the same MQTT action string the "Agent
operation names" table above reserves), and the `fpp.*` and `resolume.*`
audit actions echo their primitive and action names. They are different
tables reached by different code, and an audit action that matched its
operation is easier to read than one that did not. Do not "fix" the
duplication.

## Observation resource kinds and signal namespaces

`observation.ResourceKind` in `pkg/observation/observation.go:64`, and the
dotted `SignalID` namespace that hangs off each one.

| Resource kind | Signal namespace | Status | Owner |
|---|---|---|---|
| `node` | `node.*` | shipped | Step 2 |
| `fpp` | `fpp.*` | shipped | Step 3 |
| `coordinator` | `coordinator.*` | shipped | Step 3 |
| `resolume` | `resolume.*` | shipped | Track D |
| `surface` | `surface.*` | shipped | Track B seam B2 |
| `node` | `node.audio.*` | shipped | Track C seam C1a (engine, device, buses) |
| `audio_session` | `audio_session.*` | registered, unpopulated | Track C seam C1a; first signals in C2/C3 |
| `night_session` | `night_session.*` | reserved | Track F seam F2 |

**`surface` is a new resource kind and that is deliberate.** A render node
may host `N` surfaces (ADR-026 decision 3), so a signal keyed on the node id
cannot address one of them. Minting the kind now costs a constant and a
validation case; minting it after `N=1` has spread through the signal names
is the latent defect ADR-026 decision 3 warns about. The resource id is the
`show.surface` configuration object id.

**Track C splits its signals across two kinds, on the `node.multisync.*`
precedent above.** One audio engine, one installed interface, and two logical
buses (AUDIO-ENGINE §6) exist per node, so engine, device, program-bus,
LTC-bus and program-to-LTC-alignment signals are node-level: attributing them
to a session would report the same fact once per session and imply that many
independent faults. A **session** is a distinct observed thing with its own
lifecycle, exactly as a surface is, so it gets its own kind and its resource
id is the session id. The namespace carries no dynamic segment: the two buses
are `node.audio.program.*` and `node.audio.ltc.*` by name, never
`node.audio.output.<id>.*`.

**Track F's `night_session` kind is reserved with its namespace and without
its individual signal rows.** The rows are written from the code when seam F2
lands, following this file's own correction below: four of ten `surface.*`
rows were wrong because they were written from a plan. The resource id is the
night-session identity, not the `night.session` configuration object id,
because one definition is activated many times and a signal keyed on the
definition could not distinguish tonight's session from last night's.

Adding a resource kind is not only a constant: `internal/coordinator/api/
handlers.go:301` switches over the allowed kinds and silently rejects any
kind not listed there.

**One `fpp.*` signal reserved 2026-08-18 for Track F.** The shipped FPP
collector exports `fpp.position.elapsed.seconds`, whole seconds, which is too
coarse to arm a cue deadline: Track F seam F0 measured FPP's own
`milliseconds_elapsed` advancing in exact 50 ms quanta (the FSEQ's step time),
and the night controller's cue tolerance is finer than a second.

| Signal | Status | Owner |
|---|---|---|
| `fpp.position.elapsed.ms` | reserved | Track F (night-session cue timing) |

Track F seam F3 built against the existing whole-second signal rather than
minting this one, and **reported the need instead of inventing the name**,
which is what this register is for.

**Individual `surface.*` signals**, all inside the namespace reserved above,
listed because the last row was minted after the others had shipped:

| Signal | Status | Owner |
|---|---|---|
| `surface.pipeline.state` | shipped | Track B seam B2 |
| `surface.pipeline.reason` | shipped | Track B seam B2 |
| `surface.pipeline.restart_count` | shipped | Track B seam B2 |
| `surface.pipeline.consecutive_failures` | shipped | Track B seam B2 |
| `surface.frames.written` | shipped | Track B seam B2 |
| `surface.frames.late` | shipped | Track B seam B2 |
| `surface.frames.dropped` | shipped | Track B seam B2 |
| `surface.frames.rate` | shipped | Track B seam B5 (ADR-040's measurement obligation) |
| `surface.transport.available` | shipped | Track B seam B4 |
| `surface.transport.reason` | shipped | Track B seam B4 |
| `surface.timeline.state` | shipped | Track B review fix, finding 7 |
| `surface.timeline.position_ms` | shipped | Track B review fix, finding 7 |
| `surface.output.mode` | shipped | Track B review fix, finding 7 |
| `surface.output.idle_mode` | shipped | Track B review fix, finding 7 |
| `surface.output.failure` | shipped | Owner ruling: a broken assignment must not look like a healthy idle |
| `node.multisync.listening` | shipped | Track B review fix, finding 7 (node-level, not surface) |
| `node.multisync.reason` | shipped | Track B review fix, finding 7 (node-level, not surface) |

**Track C's `audio_session.*` signals, as shipped by seams C6/C7 on
2026-08-18.** They were reserved before C3 and C4 were written; the table
below is rewritten from
`internal/coordinator/collector/nodeaudio/signals.go` after the seam
landed, and **eight of the reserved spellings changed on the way**. The
builder emitted its own names, the orchestrator ruled a canonical set
mixing both (taking the reservation where it was as good or better, and
the builder's where it carried a fact the reservation could not), and the
code was renamed to match before anything shipped. Reserving still paid
for itself: the divergence was caught by comparing code against register
rather than by two tracks colliding.

All are on the `audio_session` resource kind, resource id the session id:

| Signal | Status | Owner |
|---|---|---|
| `audio_session.state` | shipped | C6/C7 |
| `audio_session.state.reason` | shipped | C6/C7 (replaces the reserved `.reason`) |
| `audio_session.source_role` | shipped | C6/C7 |
| `audio_session.playlist.revision` | shipped | C6/C7 |
| `audio_session.playlist.item_id` | shipped | C6/C7 |
| `audio_session.playlist.item_index` | shipped | C6/C7 |
| `audio_session.position_ms` | shipped | C6/C7 |
| `audio_session.reference_position_ms` | shipped | C6/C7 |
| `audio_session.drift_ms` | shipped | C6/C7 |
| `audio_session.desired_revision` | shipped | C6/C7 |
| `audio_session.gain.effective` | shipped | C6/C7 |
| `audio_session.gain.ceiling` | shipped | C6/C7 |
| `audio_session.fade.state` | shipped | C6/C7 |
| `audio_session.mix.ducked_by` | shipped | C6/C7 |
| `audio_session.readiness.state` | shipped | C6/C7 |
| `audio_session.readiness.reason` | shipped | C6/C7 |
| `audio_session.fault.kind` | shipped | C6/C7 |
| `audio_session.fault.reason` | shipped | C6/C7 |
| `audio_session.item_gap_ms` | shipped | the measured gap between consecutive playlist items (Track C) |
| `audio_session.item_gap.reason` | shipped | why no gap could be measured (Track C) |

**`audio_session.item_gap_ms` is a measurement, never a restatement of the
requested transition.** It is reported only from the node's own evidence of
one item ending and the next producing output, and is absent with a stated
reason otherwise, never zero.

**Reserved and never shipped**, deliberately: `audio_session.media.asset_id`,
`audio_session.media.content_hash`, `audio_session.playlist.repeat`,
`audio_session.mix.state`, `audio_session.last_outcome` and
`audio_session.last_outcome_reason`. The media identity and repeat mode
travel in the session's desired state rather than as observations;
`mix.state` was dropped because `mix.ducked_by` empty already means "not
ducked" and `interrupt` is refused, so a second signal could only
disagree with the first. They stay reserved rather than released, since
releasing a name only to re-mint it later is how a spelling drifts.

**Four more node-level `node.audio.ltc.*` signals, reserved 2026-08-19,
shipped 2026-08-20** by seam C5. The reservation originally recorded a
rationale the owner then overruled: LTC was to be generated by an external
libltc process streamed into the pipeline, and it is instead generated by
libltc linked into the agent, inside the same pipeline that carries program
audio. The rationale is corrected here rather than left standing, because a
builder looking this up would otherwise find a description of a design that
does not exist.

| Signal | Status | Owner |
|---|---|---|
| `node.audio.ltc.frame_rate` | shipped | Track C seam C5 |
| `node.audio.ltc.timecode` | shipped | Track C seam C5 |
| `node.audio.ltc.generator.state` | shipped | Track C seam C5 |
| `node.audio.ltc.generator.reason` | shipped | Track C seam C5 |

**The generator gets its own liveness signal deliberately.** A generator
process inside the media path can die while the pipeline keeps running, and
silent timecode loss looks exactly like a show sitting between cues. The
orchestrator raised that cost when recommending against live generation;
the owner ruled for it anyway on stronger grounds (a pre-rendered file
cannot carry a per-sequence start offset), so the failure mode is
engineered rather than argued: generator liveness is observed in its own
right and never inferred from the pipeline still being up.

**One new node-level signal shipped with them**, on the `node` kind:

| Signal | Status | Owner |
|---|---|---|
| `node.audio.clock.alignment` | shipped | C6/C7 |

**It is always `not_collected`, with a reason, by design.** Nothing in
software can measure program-to-LTC alignment, so it is never derived
from both outputs being usable and never from configuration declaring a
shared clock. A test fails if the signal is made to look measured. The
whole value of the signal is that a green alignment light means measured
rather than configured, and only the hardware work can make it say
anything else.

**`drift_ms` is reported, never acted on continuously.** ADR-017 makes
audio's divergence from the MultiSync slew/jump model deliberate: the
signal exists so the threshold can be set from measurement, and a future
reader who "fixes" audio to chase it has reintroduced the defect the ADR
exists to prevent.

**A probe result is a command outcome, not an observation.**
`audio.media.probe` answers about an asset, which is not a session and has
no resource of its own; only a session's own readiness becomes an
observation, which is why the two readiness rows above are the seam C2
entries and there are no node-level probe signals.

**Four rows above were wrong until 2026-08-17 and the review caught it.** The
register recorded `surface.reason`, `surface.restart.count` and
`surface.consecutive.failures` while the code shipped
`surface.pipeline.reason`, `surface.pipeline.restart_count` and
`surface.pipeline.consecutive_failures`, and `surface.transport.reason` was
absent from the register entirely. Four of ten wrong, in the register that
exists for exactly this. Reserving is only worth anything if the reservation
matches what ships, so a register entry is now written from the code rather
than from the plan.

**The six new rows are what finding 7 costs.** Nothing in this system reported
whether a surface was drawing content or idle black, so a node whose MultiSync
listener failed to bind emitted black at full rate while every counter and
health signal read normal: a green dashboard and a black wall. The first four
are per surface. The last two are **node-level rather than surface-level**,
deliberately: one MultiSync listener serves every surface on a node, so
attributing its failure to a surface would report the same fact N times and
imply N independent faults.

**`surface.output.failure` is what one owner ruling costs.** The frame
writer's coverage-gap fallback used to report `surface.output.mode` as
`idle` with `surface.output.idle_mode` as `black`, which kept a broken
assignment from being mislabelled as a `hold` idle and, in the same move,
made it indistinguishable from a surface an operator had deliberately set to
idle black. `surface.output.mode` now carries a third value, `failure`, and
this signal says which fallback that failure actually put on the wire:
`alert` in Program Mode, `black` in Show Mode. It is `not_collected` with a
stated reason for every other drawing state.

**`surface.frames.rate` is what ADR-040 costs.** That record makes matrix
size a performance parameter rather than an architectural boundary, so
nothing refuses a large surface; the achieved frame rate at the configured
geometry therefore has to be reported as evidence, or an operator who
authors a matrix the hardware cannot sustain finds out from the wall rather
than from the dashboard. It is a measured rate: where no measurement exists
it is `not_collected` with a stated reason, never a plausible-looking zero
and never the configured target rate echoed back.

## MQTT topics

ADR-008 conventions, `pkg/mqttproto`. No track has needed a new topic since
Step 2; add rows here before minting one.

| Topic pattern | Status | Owner |
|---|---|---|
| `showmesh/nodes/<id>/hello` (retained) | shipped | Step 2 |
| `showmesh/nodes/<id>/lwt` (retained, Will) | shipped | Step 2 |
| `showmesh/nodes/<id>/observed/health` (retained) | shipped | Step 2 |
| `showmesh/nodes/<id>/observed/assets` (retained) | shipped | Track E |
| `showmesh/nodes/<id>/observed/agent/echo` (retained) | shipped | Track B seam B1 |
| `showmesh/nodes/<id>/cmd` | shipped | Step 2 |
| `showmesh/nodes/<id>/result/<cmd-id>` | shipped | Step 2 |
| `showmesh/nodes/<id>/observed/render` (retained) | shipped | Track B seam B2 |
| `showmesh/nodes/<id>/observed/audio` (retained) | shipped | Track C seam C1a |

**Corrected 2026-08-17.** Every row in this table was previously wrong in
both halves: the prefix read `showmesh/node/` where `pkg/mqttproto/topic.go:14`
has `nodesPrefix = "showmesh/nodes"`, and three of the four leaf names
(`status`, `heartbeat`, `command`) do not exist — they are `lwt`,
`observed/health` and `cmd`. A builder minting a topic from the old table
would have produced one on a prefix nothing subscribes to. The table is now
generated from the builders in `topic.go:212-256`.

**Subpath charset is narrower than the topic charset.** Each `observed/`
segment must match `^[a-z0-9][a-z0-9_-]*$` (`topic.go:158`): no dots, no
uppercase. `observed/render` is legal, `observed/renderPipeline` is not.

**A new `observed/` subpath is only half the work.** The coordinator's
ingest switch (`internal/coordinator/inventory/inventory.go:315`) drops any
subpath it has no `case` for, at Debug level, silently. `observed/agent/echo`
is being dropped that way today.

**Never publish on `falcon/player/<host>/command/run` or any other
`falcon/` topic against the live fleet.** FPP acts on it. This is a safety
rule, not a naming convention, and it is in CLAUDE.md for the same reason.

## Schema versions

The store schema version, bumped by migrations in
`internal/coordinator/store/`.

| Version | Status | Introduced by |
|---|---|---|
| v6 | shipped | Step 7 seam 0 (atomic audit variant, strict login CSRF) |
| v7 | shipped | Step 9 wave 1a (macro execution history, ADR-031) |
| v8 | shipped | Track E (asset store tables, ADR-028) |
| v9 | shipped | Track C seam C3 (audio session desired state, `audio_sessions`) |
| v10 | shipped | Track F seam F2 (night-session lifecycle, ADR-038; cue outbox filled by seam F4) |
| v11 | reserved | credential storage moves from the data directory into SQLite (owner, 2026-08-18, Linear SM-95) |
| v12 | reserved, may be released | durable action-invocation attribution and lifecycle state (Linear SM-100/SM-102) |
| v13 | reserved | rename `commands.requested_revision` to an honest name and formalize its per-family discriminator (owner, 2026-08-19, Linear SM-111) |
| v14 | shipped | SM-150: latest FPP playlist-entry observation per instance (RES-018 section 6) |
| v15 | shipped | Track H seam H2: FPP playlist definition storage (FPP-PLUGIN-COORDINATOR-CONTRACTS.md §3, TRACK-H-H2-SPEC.md §3) |
| v16 | shipped | per-endpoint observed FPP instance uuid history (`fpp_instance_uuid_observations`), closing the gap FPP-PLUGIN-COORDINATOR-CONTRACTS.md §1.5 recorded between `fpp.endpoints`, the plugin's `instanceUuid`, and `node_declarations` |
| v17 | shipped | Track H seam H3: per-node cue-catalog acknowledgement storage (TRACK-H-H3-SPEC.md §4) |
| v18 | shipped | Track H seam H4 defect fix: `entry_occurrence_sequence` on `fpp_playlist_entry_observations`, the entry-start identity a looping FPP playlist needs to re-activate its Cues |
| v19+ | unallocated | free |

**v13 must not run until PRs #17, #18 and #19 are merged**, and that is a
sequencing constraint rather than a preference. The column's writers are
split across `main` (the `macro:`-prefixed macro-run revision) and two
unmerged branches: PR #19 writes an action configuration revision to it, and
PR #18 writes a JSON caller-identity struct that contains no revision at
all. A rename branched from `main` today would compile against one writer
and break when the other two land. Merge first, then rename with every
writer visible.

Note the column already carries an informal discriminator: values written by
a macro run begin with `macro:` (`macroRequestedRevisionPrefix`,
`store/macro_runs.go`). Three shapes now share the column, so v13 should
formalize that convention rather than leave a fourth reader guessing.

**v12 is reserved defensively and may well come back.** SM-100's lifecycle
state and SM-102's durable dispatch and outcome attribution may fit in the
existing `commands.result_json` payload and `commands.requested_revision`,
in which case no migration is needed and v12 is released. It is reserved
now because three sessions are running and discovering mid-build that the
number is taken costs a rename across a branch, while releasing an unused
reservation costs nothing.

**Track B took no schema version.** Its render state travels through the
existing observations table via a collector `Sink`, so the v7 it had reserved
was released and went to Step 9.

A track that needs a schema change requests the next version number here
before writing the migration. Two branches writing migration `v7`
independently is the ADR-034 failure with data attached.

**The stamped version is the maximum migration version, not the count, and
that distinction is invisible except while a branch holds a gap** (Track C
and Track F, 2026-08-18, agreed across both sessions). `migrate()` stamps
`PRAGMA user_version` and skips any migration whose version is `<=` the
stamp. Those two numbers were interchangeable for the whole life of this
project because versions were contiguous, and they stopped being
interchangeable the moment Track F took v10 while Track C's v9 was still
unwritten: a fresh database on that branch applied nine migrations, the last
of them v10, and a count-based stamp wrote `9`, which on the other branch
means "has the audio tables". One number, two schemas, and the equality
fast-path then skips whichever migration the other branch needs. The change
to a maximum is a no-op for every database that has ever existed, which is
why it landed mid-flight rather than at merge.

**Once both branches merge the versions are contiguous again and the fix
becomes invisible**, which is the reason it is written down here rather than
left to the line itself.

**A coordinator database created by a branch binary cannot survive a merge,
in either direction**, and no stamp scheme fixes it: the apply loop's guard
is `version <= current`, so a database stamped 10 skips v9 and one stamped 9
skips v10. Per-version tracking (a migrations table rather than one integer)
is the only thing that would, and that is a change to the store's contract
rather than a merge-week edit. Discard and recreate branch dev databases.
The deployed local dev stack is at v8 and is unaffected: a merged binary
sees 8 and applies v9 then v10 in order.

## Change stream event kinds

The `event:` field of an SSE frame, written by
`internal/coordinator/api/stream.go`. ADR-020 makes the stream
additive-only within a major version, so a client ignores a kind it does not
know — which is exactly why two tracks can mint the same name without
anything failing loudly.

| Event kind | Status | Owner |
|---|---|---|
| `node.changed` | shipped | Step 3 |
| `fpp.changed` | shipped | Step 3 |
| `fpp.observations.changed` | shipped | Step 3 |
| `event.recorded` | shipped | Step 3 |
| `macroRun.changed` | shipped | Step 9 |
| `resolume.changed` | shipped | Track D |
| `resolumeRecovery.changed` | shipped | Track D seam D-3a |
| `nightSession.changed` | reserved | Track F seam F2 |
| `fppPlaylistEntry.changed` | shipped | SM-150, Track H: latest accepted FPP playlist-entry observation |

**Track F mints one kind, not one per lifecycle transition.** ADR-020 makes
the stream non-resumable, so a client that misses frames re-fetches the
authoritative session state rather than reconstructing it from a sequence of
transition events; a per-transition kind would invite exactly the
reconstruction the non-resumable rule forbids.

## Research record numbers

`docs/research/RES-NNN-*.md`, tracked in
[`docs/research/README.md`](../research/README.md). Not previously in this
register, which is how ADR numbers came to collide, and there are three
sessions running.

| Number | Status | Subject |
|---|---|---|
| RES-001 to RES-017 | shipped | see the research tracker |
| RES-018 | issued | FPP brightness, ADR-043 playlist identity, and the three-repository plugin runtime (Tracks F/H) |
| RES-019+ | unallocated | free |

## API paths

Not enumerated here, because `api/openapi.yaml` is the machine-readable
register and is conformance-tested against real handler responses in both
directions. Two tracks adding paths to it collide in a merge conflict,
which is visible, rather than silently, which is what this file exists to
prevent everywhere else.

**The one thing that does not conflict visibly is a shared `enum`.** Track D
and Track E both added a kind to `ConfigRevisionsResponse` and the union
left two `enum` keys on one YAML mapping; the conformance suite caught it,
not git. When adding to an existing enum, say so in the seam spec.
