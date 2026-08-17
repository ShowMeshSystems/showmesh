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
| 22+ | unallocated | free |

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
| `resolume.instances` | `default` singleton | **reserved** | Track G seam G-2 |
| `fpp.mqtt` | `default` singleton | **reserved** | Track G seam G-3 |
| `assets.settings` | `default` singleton | **reserved** | Track G seam G-4 |

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
| `principal:write` | **declared, guards nothing** | first callers land in Track G seam G-5 |
| `principal:read` | **reserved** | Track G seam G-5 |

`principal:write` has been in the admin bundle since Step 6 and no handler
checks it. Treat it as reserved-and-unimplemented, not as available.

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

**Instance ids share this namespace.** A Resolume instance id must not
collide with any FPP endpoint id, for the same `Runner` reason. Validation
enforces it (`config.ValidateResolumeIDAgainstFPPEndpoints`); this table is
not the enforcement, it is where a builder looks before choosing.

## MQTT topics

ADR-008 conventions, `pkg/mqttproto`. No track has needed a new topic since
Step 2; add rows here before minting one.

| Topic pattern | Status |
|---|---|
| `showmesh/node/<id>/hello` (retained) | shipped |
| `showmesh/node/<id>/status` (LWT) | shipped |
| `showmesh/node/<id>/heartbeat` | shipped |
| `showmesh/node/<id>/command` | shipped |

**Never publish on `falcon/player/<host>/command/run` or any other
`falcon/` topic against the live fleet.** FPP acts on it. This is a safety
rule, not a naming convention, and it is in CLAUDE.md for the same reason.

## Schema versions

The store schema version, bumped by migrations in
`internal/coordinator/store/`.

| Version | Status | Introduced by |
|---|---|---|
| v6 | shipped | Step 7 seam 0 (atomic audit variant, strict login CSRF) |
| later versions | see `store/` migrations | |

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
