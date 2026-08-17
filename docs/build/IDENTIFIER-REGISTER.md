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
| 22 | `exitRenderUnavailable` | **reserved** (Track B seam B4) |
| 23 | `exitRenderPipelineDown` | **reserved** (Track B seam B2) |
| 24+ | unallocated | free |

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
| `render.settings` | `default` singleton | **reserved** | Track B seam B2 |

**Track B deliberately mints no per-surface kind.** `show.surface` already
exists (Track E) and Track B consumes it unchanged. `render.settings` holds
only what is renderer-wide and operator-settable, the sync-loss output
behaviour being the first entry. **Track B must not add a field to
`show.surface` tonight**: Track G seam G-8 is building that object's
Operator UI in a parallel worktree, and an additive payload field plus a new
UI over the same object is the collision this register exists to prevent.
A per-surface override of the renderer default is future work, sequenced
after G-8 folds.

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
| `render:command` | **reserved** | Track B seam B2: surface apply/clear, pipeline restart |
| `principal:write` | shipped | the nine principal/token administration writes (Track G seam G-5) |
| `principal:read` | shipped | principal/token/audit administration reads (Track G seam G-5) |

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
| `node-render` | **reserved** | Track B seam B2 (`collector/noderender`) |

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
| `render.surface.apply` | **reserved** | Track B seam B2 |
| `render.surface.clear` | **reserved** | Track B seam B2 |
| `render.pipeline.restart` | **reserved** | Track B seam B2 |
| `render.transport.probe` | **reserved** | Track B seam B4 |

## Observation resource kinds and signal namespaces

`observation.ResourceKind` in `pkg/observation/observation.go:64`, and the
dotted `SignalID` namespace that hangs off each one.

| Resource kind | Signal namespace | Status | Owner |
|---|---|---|---|
| `node` | `node.*` | shipped | Step 2 |
| `fpp` | `fpp.*` | shipped | Step 3 |
| `coordinator` | `coordinator.*` | shipped | Step 3 |
| `resolume` | `resolume.*` | shipped | Track D |
| `surface` | `surface.*` | **reserved** | Track B seam B2 |

**`surface` is a new resource kind and that is deliberate.** A render node
may host `N` surfaces (ADR-026 decision 3), so a signal keyed on the node id
cannot address one of them. Minting the kind now costs a constant and a
validation case; minting it after `N=1` has spread through the signal names
is the latent defect ADR-026 decision 3 warns about. The resource id is the
`show.surface` configuration object id.

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
| `showmesh/nodes/<id>/observed/render` (retained) | **reserved** | Track B seam B2 |

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
| v7 | **reserved** | Track B seam B2 (node render state), if a migration proves necessary |
| later versions | see `store/` migrations | |

**v7 is reserved but may go unused.** Track B's first choice is to carry
render state through the existing observations table via a collector `Sink`,
which needs no migration. The reservation exists so that if the node render
report needs its own table, no second branch mints v7 in parallel. If Track B
folds without a migration, release v7 rather than leaving it reserved.

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
