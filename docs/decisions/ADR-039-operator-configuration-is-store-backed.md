# ADR-039: Operator configuration is store-backed, and the environment holds only what precedes it

Status: Accepted
Date: 2026-08-17

Extends [ADR-030](ADR-030-operator-ui-is-the-authoring-surface.md) and CLAUDE.md's CLI-parity constraint by naming the condition both of them assume and neither of them states. Applies [ADR-036](ADR-036-dispatch-configuration-applies-without-a-restart.md)'s delivery requirement to every configuration kind this record creates. Implemented by [Track G](../build/TRACK-G-surface-parity.md).

## Context

On 2026-08-17 the owner tried to connect Resolume Arena to a running ShowMesh coordinator and could not, from any operator surface. There is no UI control, no `showmeshctl` verb, and no API endpoint. The Resolume host is `SHOWMESH_RESOLUME_URL`, an environment variable read once at process start, so connecting Arena requires editing a file inside the deployment bundle and restarting the container.

Resolume control is one of this project's three founding problems. Its observability, its seven-action vocabulary, its crash recovery, and its Operator UI all shipped. The one step that has to happen before any of it does anything, telling ShowMesh where Arena is, shipped as a file edit and a restart.

**Every existing rule passed.** CLAUDE.md says *"every API capability gets CLI coverage in the step that adds it."* ADR-030 says every authoring capability exists in the API first and `showmeshctl` must be able to drive it. Both are conditioned on an endpoint existing. `SHOWMESH_RESOLUME_URL` has no endpoint, so it owed no CLI verb and no UI control, and the subsystem shipped unconfigurable while fully compliant.

The rules enforce parity *between* surfaces. Nothing says a capability an operator needs must appear on any surface at all. A capability with zero surfaces is in perfect parity.

A full audit on the same date found the gap is not one variable. The FPP MQTT collector, an entire second collector source from Step 5, is configurable only through the environment, and an operator can neither set it nor see whether it is set. So are the asset store's operator-facing settings. Identity administration is worse: seven subcommands on the coordinator binary, in a distroless image with no shell, so revoking a leaked token requires container exec access on the coordinator host. And `principal:write` has been declared, bundled into the admin role, and checked by no handler since Step 6, which is the fourth time this project has shipped a capability nothing can reach.

**FPP already walked this path and the lesson did not survive the walk.** `SHOWMESH_FPP_ENDPOINTS` was an environment variable until Step 7 promoted it to a store-backed kind with revisions, an audited write path, a one-time migration, and eventually ADR-036's no-restart apply. Track D seam D-1 shipped after that migration completed and reproduced the pattern FPP had just been rescued from. Nothing was forgotten, because nothing had been written down: the promotion was recorded as work done to FPP, never as a rule about configuration.

That is the specific failure this record exists to stop. A correction applied to one subsystem, and not stated as a constraint, is a correction the next subsystem does not inherit.

## Decision

### 1. Operator configuration is store-backed, on three surfaces

Anything an operator must set for a subsystem to function is store-backed configuration under [ADR-009](ADR-009-sqlite-configuration-storage.md)'s revision model, reachable through a versioned API endpoint, a `showmeshctl` verb, and an Operator UI control.

The three surfaces are not three implementations. The endpoint is the capability; the CLI and the UI are clients of it, per [ADR-014](ADR-014-operator-ui-is-a-client.md). What this record adds is that the endpoint must **exist**, which is what nothing previously required.

### 2. The environment holds only what must be true before the process starts

The process environment carries bind address, data directory, the control-plane broker URL and its credential, and log level. Nothing else.

The test is temporal, not aesthetic: **can the coordinator read this value from its own store?** If yes, the value belongs in the store. `SHOWMESH_DATA_DIR` is the worked example of the boundary, because it is where the store lives; a filesystem path backed by a volume mount that must resolve before SQLite opens is genuinely start-time. The control-plane broker credential is the second, because the agent transport is how a coordinator reaches the fleet and a credential fetched through the fleet is circular. Bootstrap is the third: no principal exists yet, so there is nothing to authenticate, and claiming the first administrator stays coordinator-local.

A Resolume host URL fails that test in every direction. So does an MQTT collector's topic prefix, an asset sync interval, and every token an operator might need to revoke.

### 3. An environment variable a new kind replaces is migrated once, and the migration never refuses to start

When a store-backed kind replaces an environment variable, the existing value migrates once at startup, writing the configuration revision and its audit entry in one transaction per [ADR-024](ADR-024-identity-authorization-and-audit.md) decision 11.

**The migration must never exit non-zero when the audit append fails.** This is not a preference. Step 7 shipped exactly that, and under the deployment bundle's `restart: unless-stopped` it is a restart loop with no API, no change stream and no dashboard, reachable on the first boot after an existing deployment upgrades. A startup migration has no principal, so fail-closed protects nobody: constraint 23 and ADR-024 decision 7 scope an identity or audit failure to *you cannot act*, never *you cannot see*.

Every new kind this record authorizes inherits that correction rather than rediscovering it. Before applying a fail-closed rule anywhere in a migration path, name the actor the refusal holds accountable; if there is none, the refusal is doing nothing but removing the operator's visibility.

### 4. While the retired variable is still set, the write path refuses, and the refusal may not destroy the value

Two authorities for one setting is the condition where the operator's write silently loses to the environment on the next restart. The write path refuses with `409`, states the remedy, and the remedy is to remove the variable and restart once.

**The refusal's documented remedy must never discard the operator's only copy of the value.** Step 7's review found precisely that: a `409` whose stated remedy would have thrown away the endpoint list. A `409` here is a routing decision about which authority wins, not a reason to empty anything.

### 5. Absent, null, and empty are three different things on every write

A `PUT` with a key absent means leave the stored value alone. `null` and an empty collection are explicit, distinct, and mean what they say.

This project has shipped the opposite twice: a configuration `PUT` with no `endpoints` key wiped every endpoint and returned `200`, reachable by following the CLI's own documented round trip, and a second `declare` with no label erased the operator's label. Both were found by using the system, neither by a test.

### 6. Every kind this record creates applies without a restart

[ADR-036](ADR-036-dispatch-configuration-applies-without-a-restart.md) governs, including its second decision: live resolution and collector-set reconciliation are one decision, not two. A newly added instance that is dispatchable but unpolled produces commands that dispatch and never confirm, which reads as broken hardware rather than as configuration that has not landed, and sends an operator to the wrong place at showtime.

**The transition that must work is zero to one and back to zero**, because that is what an operator setting up a subsystem for the first time actually performs, and it is the transition a configuration path built by editing an already-populated list never exercises.

### 7. A credential in a configuration payload is write-only, and its history holds no copy

A credential a subsystem needs in order to authenticate cannot be hashed, so kinds that carry one (the FPP MQTT collector's broker password is the first) obey three additional rules. `GET` never returns the value and reports presence only. An absent key on `PUT` keeps the stored value, per decision 5, so a round trip through the CLI cannot erase a credential the operator never saw. And the revision history stores a reference rather than the secret, because ADR-009's revisions are immutable by design and an immutable permanent copy of a rotatable secret is the one thing rotation exists to prevent.

This makes the coordinator's SQLite volume credential-bearing, which it was not before, and that consequence is accepted deliberately rather than arrived at. The volume already holds password hashes and token digests; it is backed up and restored as a unit and must be treated as sensitive. The alternative, leaving credentials in the environment and moving only the non-secret fields of a kind, leaves the subsystem half-unreachable and reproduces this record's own failure mode at a smaller scale.

### 8. Locking out the last administrator is refused, and this is the direction fail-closed points correctly

Disabling the last enabled admin principal, or revoking the last credential that can reach `principal:write`, is refused with a stated reason.

**This is a fail-closed rule in a project that has been burned by one, so it is argued rather than assumed.** Run ADR-024 decision 7's test: name the actor the refusal holds accountable, then check what the refusal removes. The actor is the administrator performing the change, present and authenticated. The refusal removes nothing needed during a show: it blocks one administrative action and leaves every read, every command, every macro and the entire show path untouched. What it prevents is a coordinator with no reachable administrator and no shell to recover from, which is unrecoverable rather than degraded.

That is the opposite direction from the audit-gate defect ADR-024 warns about, where a refusal removed the operator's ability to stop the show. The distinction is not that this refusal is safe; it is that this one costs an administrative retry and that one cost blackout.

### 9. The boundary is enforced by a test, not by review

An inventory test enumerates every `SHOWMESH_*` setting the coordinator reads and asserts each appears on an explicit allow-list of start-time settings, each entry carrying a stated reason why it must be known before the process starts. Adding an operator-facing environment variable fails the build.

A second test asserts every non-`GET` path in `api/openapi.yaml` appears in a `showmeshctl` command registry, so a new write endpoint with no CLI verb fails the build. CLAUDE.md's parity constraint is currently honoured by discipline alone, which is how `showmeshctl` came to be unable to write a macro definition the UI can write.

**Neither test can reach the UI**, which is a separate program in a separate language, so UI coverage remains a review obligation. That asymmetry is stated here rather than left to be discovered: the two enforced surfaces are the API and the CLI, and a reviewer is the only thing standing between a shipped endpoint and a UI that cannot reach it.

## Consequences

Track G implements this record: `resolume.instances`, `fpp.mqtt`, and `assets.settings` become configuration kinds, identity administration gets an API with `principal:read` alongside the existing `principal:write`, `showmeshctl` gains macro definition writes, and decision 9's two tests land last because they fail until the surfaces exist.

`SHOWMESH_RESOLUME_URL`, `SHOWMESH_RESOLUME_ID`, the five `SHOWMESH_FPP_MQTT_*` variables, and the operator-facing `SHOWMESH_ASSET_*` variables are retired by migration under decision 3. `SHOWMESH_ASSET_DIR` stays, as decision 2's worked example.

**This record does not build the missing Operator UI for Track E.** Shows, surfaces, active-show activation, the asset browser, and the audit view have API endpoints and CLI verbs and no UI route. That is a parity gap of a different kind, recorded in Track G's audit and deliberately left for its own scheduled work rather than folded into a record about capabilities that exist nowhere.

The cost is that adding a subsystem is now more work than adding an environment variable, which is the point. A subsystem that takes a file edit and a container restart to connect is not connected, and the evidence that this is not a theoretical concern is that it took the owner of the project, holding root on the machine, to a dead end.
