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
| 26+ | unallocated | free |

**16 to 19 are deliberately free.** The asset codes were placed at 20 to
leave room below them; do not close that gap without a reason.

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
| `show.surface` | operator-chosen | shipped | Track E |
| `show.action` | operator-chosen | shipped | Step 9 |
| `show.macro` | operator-chosen | shipped | Step 9 |
| `resolume.instances` | `default` singleton | shipped | Track G seam G-2 |
| `fpp.mqtt` | `default` singleton | shipped | Track G seam G-3 |
| `assets.settings` | `default` singleton | shipped | Track G seam G-4 |
| `render.settings` | `default` singleton | shipped | Track B seam B2 |
| `audio.settings` | `default` singleton | reserved | Track C seam C1 |
| `audio.node` | operator-chosen (the node id) | reserved | Track C seam C1 |

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
surface. **Track C mints no audio-playlist configuration kind**: Track F's
Night Session configuration embeds the ordered audio slots (TRACK-F F1), and
a second authoring path for the same list is the collision this register
exists to prevent.

Note the Resolume composition is **not** a configuration kind. It is stored
behind `/api/v1/config/resolume/composition` with its own upload path
(ADR-032), and the path shape differs deliberately.

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
| `config:write` | shipped | every configuration write |
| `audit:read` | shipped | audit log reads |
| `resolume:action` | shipped | the seven Resolume actions |
| `asset:write` | shipped | asset upload |
| `render:command` | shipped | Track B seam B2: surface apply/clear, pipeline restart |
| `principal:write` | shipped | the nine principal/token administration writes (Track G seam G-5) |
| `principal:read` | shipped | principal/token/audit administration reads (Track G seam G-5) |
| `audio:command` | reserved | Track C seams C3/C4: session, gain, fade, mute |

`principal:write` sat in the admin bundle unchecked from Step 6 until Track
G seam G-5 landed its first callers (merged 2026-08-17).

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
| `node-audio` | reserved | Track C seam C6 (`collector/nodeaudio`) |

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
| `audio.device.probe` | reserved | Track C seam C1 |
| `audio.media.probe` | reserved | Track C seam C2 |

**AUDIO-ENGINE §14's `select_media`, `select_playlist`, `set_loop`,
`announce` and `duck` mint no operation of their own.** §14 permits combining
operations where confirmation and idempotency stay unambiguous, and all five
are properties of the session being applied: an announcement is
`audio.session.apply` with source role `announcement` and a declared
`mix`/`duck`/`interrupt` policy, followed by `audio.session.start`. A
separate `audio.session.duck` would be a second way to reach the same state
with no way to say which one won.

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
| `node` | `node.audio.*` | reserved | Track C seam C6 (engine, device, buses) |
| `audio_session` | `audio_session.*` | reserved | Track C seam C6 |

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

Adding a resource kind is not only a constant: `internal/coordinator/api/
handlers.go:301` switches over the allowed kinds and silently rejects any
kind not listed there.

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
| `node.multisync.listening` | shipped | Track B review fix, finding 7 (node-level, not surface) |
| `node.multisync.reason` | shipped | Track B review fix, finding 7 (node-level, not surface) |

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
| `showmesh/nodes/<id>/observed/audio` (retained) | reserved | Track C seam C6 |

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
| v9 | reserved | Track C (audio session desired state) |
| v10+ | unallocated | free |

**Track B took no schema version.** Its render state travels through the
existing observations table via a collector `Sink`, so the v7 it had reserved
was released and went to Step 9.

A track that needs a schema change requests the next version number here
before writing the migration. Two branches writing migration `v7`
independently is the ADR-034 failure with data attached.

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
