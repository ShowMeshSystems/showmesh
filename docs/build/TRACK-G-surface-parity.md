# Track G: operator surface parity

[Build plan](BUILD-PLAN.md) · [Build log](BUILD-LOG.md) · [ADR-014](../decisions/ADR-014-operator-ui-is-a-client.md) · [ADR-024](../decisions/ADR-024-identity-authorization-and-audit.md) · [ADR-030](../decisions/ADR-030-operator-ui-is-the-authoring-surface.md) · [ADR-036](../decisions/ADR-036-dispatch-configuration-applies-without-a-restart.md)

**Status:** merged to `main` 2026-08-17 (PR #12, merge commit `c31cf7f`),
after a four-agent pre-merge review whose fold fixed two blocking defects
(the deferred-migration reconcile teardown in all three config managers,
and the Access view's token list never re-fetching), a shutdown panic
(eight background goroutines against `backgroundWG.Add(7)`), and
mutation-confirmed bypass holes in both G-7 guards. Gates run on the fold:
`make check`, `make test-integration`, `make test-integration-fpp`, plus
full CI. Owner called the track critical and scheduled it ahead of Track B.

| Seam | What it closes | PR |
|---|---|---|
| G-1 | [ADR-039](../decisions/ADR-039-operator-configuration-is-store-backed.md), accepted by the owner | on `main` |
| G-2 | `resolume.instances`, the seam the track exists for | #7 |
| G-3 | `fpp.mqtt` | #11 |
| G-4 | `assets.settings` | #10 |
| G-5 | identity administration, and `principal:write`'s first caller | #9 |
| G-6 | `showmeshctl macro put` | #6 |
| G-7 | the two ADR-039 decision 9 guards | #12 |
| G-8 | the Operator UI for Track E | #8 |

**PR #12 is the whole track folded into one branch plus G-7**, because the
write-parity guard cannot be written against any single seam: it would pass
for the wrong reason without G-6. Merge #12, or merge the six and take only
its final commit. #10 and #11 base on #7.

**What is not proved.** Nothing ran against real show hardware. G-2's
reachability used a stub, not a real Arena. G-8's write-scope flows were
never exercised authenticated in a browser, and that seam added roughly
2,450 lines of UI with no test files of its own, which makes it the
least-evidenced code in the track; both are on
`docs/private/PUNCH-LIST.md`. Two owner decisions are queued: G-3's
credential storage divergence from ADR-039 decision 7, and
`SHOWMESH_INTEGRATION_BROKERS`.

## Why this track exists

On 2026-08-17 the owner tried to connect Resolume Arena to the local test
stack and could not, from any operator surface. There is no UI control, no
`showmeshctl` verb, and no API endpoint. The Resolume host is
`SHOWMESH_RESOLUME_URL`, an environment variable read once at coordinator
startup, so connecting Arena requires editing a file inside the deployment
bundle and restarting the container.

Every existing rule passed.

CLAUDE.md's standing constraint says *"every API capability gets CLI
coverage in the step that adds it."* [ADR-030](../decisions/ADR-030-operator-ui-is-the-authoring-surface.md)
says every authoring capability exists in the API first and `showmeshctl`
must be able to drive it. Both rules are conditioned on an endpoint
existing. `SHOWMESH_RESOLUME_URL` has no endpoint, so it owed no CLI verb
and no UI control, and the subsystem shipped unconfigurable while in full
compliance.

**The rule enforces parity between surfaces and says nothing about whether
a capability an operator needs must appear on any surface at all.** That
hole is what this track closes, structurally, not by remembering harder.

FPP already walked this exact path. `SHOWMESH_FPP_ENDPOINTS` was an
environment variable until Step 7 promoted it to a store-backed
configuration kind with revisions, an audited write path, an env-to-store
migration, and no-restart apply. Track D seam D-1 shipped *after* that
migration and reproduced the pattern FPP had just been rescued from,
because nothing in the repository stated the pattern as a rule.

## The audit that produced this scope

Run 2026-08-17 against 44 paths in `api/openapi.yaml`, 22 `showmeshctl`
top-level commands and their subcommands, 16 routes in
`ui/src/app/App.tsx`, every write method in `ui/src/api/store.ts`, all 30
environment variables in `internal/coordinator/config/config.go`, and the
`showmesh-coordinator` binary's own subcommands.

### Class 1: the capability has no API at all, so it exists on no surface

| Capability | Only path today | Needs restart |
|---|---|---|
| Resolume host URL and instance id | edit `deploy/.env` | yes |
| FPP MQTT collector: broker URL, username, password, topic prefix, host map | edit `deploy/.env` | yes |
| Asset store: content base URL, max upload size, sync interval, inventory interval | edit `deploy/.env` | yes |
| Identity administration: `list-principals`, `create-principal`, `reset-password`, `issue-token`, `list-tokens`, `revoke-token`, `invalidate-all-sessions` | `docker exec` into the coordinator container | n/a |

The identity row is the one with operational bite. Those seven subcommands
live on the coordinator binary, and the coordinator image is distroless
with no shell (`docker exec ... sh` fails with `executable file not found`).
Revoking a leaked token therefore requires container exec access on the
coordinator host. [ADR-024](../decisions/ADR-024-identity-authorization-and-audit.md)
does not place identity administration off the network API by design;
decision 1 argues the opposite, that a CLI which can only act as a robot
does not satisfy ADR-014's usable-with-no-UI requirement. The surface was
never specified either way.

An entire second collector source, FPP MQTT ingestion from Step 5, is in
the same bucket: an operator can neither configure it nor see whether it
is configured.

### Class 2: the UI can do it and the CLI cannot

One finding, and it inverts the rule that makes the CLI worth having.

`PUT /api/v1/config/show.macro/{id}` is called by the UI
(`ui/src/api/store.ts:867`, routes `/macros/new` and `/macros/:id`).
`showmeshctl macro --help` refuses it: *"Writing a macro definition is not
this program's job, the show-authoring surface (Track E, not yet built)
owns PUT for show.macro."* Track E merged on 2026-08-16. The help text is
stale and the gap is real: a broken macro is repairable only from a
browser, which is precisely the condition the CLI exists for.

### Class 3: the CLI can do it and the UI cannot

Track E's entire operator surface has no route in the UI:
`/config/show`, `/config/show.surface`, `/config/show.active` (so the
active show cannot be activated from a browser), `/assets` list, upload
and fetch, `/assets/manifest`, `/nodes/{nodeId}/assets`, and `/audit`.

Class 3 was scoped out on 2026-08-17 and **scoped back in the same day**,
as seam G-8, when the owner said it should be resolved within this track.
It ships as its own pull request so it can be declined independently of
the rest of the track.

### A fifth Class 1 item, found by G-7's own guard rather than by this audit

`SHOWMESH_INTEGRATION_BROKERS` (`internal/coordinator/config/integrationbrokers.go`)
declares the external MQTT brokers a `show.action`'s `mqtt` target may name.
It is genuinely operator-facing, it fails
[ADR-039](../decisions/ADR-039-operator-configuration-is-store-backed.md)
decision 2's own test, and it was never converted to a store-backed kind.

**This audit missed it**, because the audit enumerated environment
variables by reading `config.go`, and this constant lives in a sibling file
of the same package. Seam G-7's inventory test parses the whole package and
found it on its first run.

It is **not fixed by Track G**. It is recorded in the guard's
`knownGapEnvVars` group with a stated reason, so the test passes while
asserting something true rather than something convenient, and it is in
`docs/private/DECISION-QUEUE.md` with options for the owner.

### A fourth finding, which is a known lesson recurring

`ScopePrincipalWrite = "principal:write"` is declared at
`internal/coordinator/identity/types.go:65` and bundled into the admin role
at line 101. **No handler checks it.** It compiles, it ships in every
admin's scope list, it renders in the UI's scope display, and nothing in
the codebase can be authorized by it.

This is CLAUDE.md's Step 6 lesson for the fourth time (`ClaimBootstrap`,
`IssueToken`, `CreatePrincipal`, now `principal:write`): *a test suite
cannot tell you that nothing calls the function.* Seam G-5 gives it
callers.

## Placement fault, separate from the missing capability

The UI nav labels `/config` as **"FPP & Resolume"**
(`ui/src/app/Layout.tsx:57`) and `ui/src/views/Configuration.tsx` contains
no Resolume reference at all. The Resolume composition upload lives on
`/resolume`; the crash recovery toggle lives on the Dashboard
(`ui/src/views/Dashboard.tsx:309`). An operator following the nav to
configure Resolume arrives at a page about FPP. Seam G-2 makes the label
true rather than changing the label.

## Seams

Seams G-2 through G-6 touch disjoint files and may be built in parallel.
G-1 gates all of them because it decides the rule they implement. G-7 must
be last: its guards fail until the surfaces they check exist.

### G-1: ADR-039, operator configuration is store-backed

**Owner decision required before any other seam starts.** Draft, do not
accept.

Proposed constraint: *anything an operator must set for a subsystem to
function is store-backed configuration, reachable through a versioned
endpoint, a `showmeshctl` verb, and a UI control. The process environment
carries only what must be true before the process can start: bind address,
data directory, control-plane broker URL and its credential, and log
level.*

The record must also carry, because each was paid for once already:

1. **The migration rule.** A new store-backed kind that replaces an
   environment variable migrates the existing value once at startup,
   writes the revision and its audit entry in one transaction
   ([ADR-024](../decisions/ADR-024-identity-authorization-and-audit.md)
   decision 11), and **never exits non-zero when the audit append fails.**
   CLAUDE.md's Step 7 lesson: a startup migration has no principal, so
   fail-closed protects nobody and under `restart: unless-stopped`
   produces a restart loop with no API, no change stream, and no
   dashboard.
2. **The still-set rule and what it may not destroy.** While the retired
   environment variable is still set in the coordinator's environment, the
   write path refuses with `409` and states the remedy. The remedy must
   never be one that discards the operator's only copy of the value.
3. **Absent, null, and empty are three different things.** A `PUT` with a
   key absent means "leave it alone"; `null` and `[]` are explicit and
   distinct. This is the defect this project has now shipped twice
   (endpoint list wiped by a keyless `PUT`, node label erased by a second
   `declare`).
4. **No-restart apply** per [ADR-036](../decisions/ADR-036-dispatch-configuration-applies-without-a-restart.md).
   A configuration change an operator can make through the API but that
   only takes effect after a container restart has moved the problem, not
   solved it.

### G-2: `resolume.instances`, the critical seam

Template is `internal/coordinator/config/fppendpoints.go` and Step 7 seam A
end to end.

- **Config kind** `resolume.instances`, singleton object id `default`,
  payload `{"instances":[{"id":"...","url":"..."}]}`. The list shape is
  already the established direction: `internal/coordinator/api/interfaces.go:72`
  states this list *"holds at most one element today and stays a list"*,
  and `api/openapi.yaml:1592` says the same. Validation rejects more than
  one instance with a reason naming the limit, in the spirit of
  [ADR-026](../decisions/ADR-026-renderer-surface-model-and-reference-transport.md)'s
  `N` surfaces implemented at `N=1`: the scope limit lives in validation,
  never in the schema.
- **Validation** reuses the four checks `validateResolumeConfig`
  (`internal/coordinator/config/config.go:1172`) already performs: http or
  https scheme, non-empty host, no userinfo, `mqttproto.ValidateNodeID` on
  the id, and no collision with any configured FPP endpoint id. The
  collision check is load-bearing: both collectors share one
  `collector.Runner` keyed by id, so a duplicate makes an out-of-band poll
  nudge retarget the wrong device.
- **API**: `GET`/`PUT /api/v1/config/resolume.instances` and
  `GET .../revisions`, behind `config:write`, matching
  `/config/fpp.endpoints` in every respect including the revision list
  shape.
- **Migration** from `SHOWMESH_RESOLUME_URL` and `SHOWMESH_RESOLUME_ID`
  per G-1 rules 1 and 2.
- **No-restart apply.** `internal/coordinator/resolumewiring.go` currently
  constructs the watcher once at startup and only when the URL is
  non-empty, and `resolumeInstanceLister.instanceID` is resolved once at
  construction with a comment stating it "cannot change without a
  coordinator restart." That comment becomes false in this seam. The
  collector set must follow the configuration the way the FPP collector set
  already does (about ten seconds, per `showmeshctl config set`'s own
  output). **Going from zero configured instances to one, and back to
  zero, must both work without a restart**, since that is the exact
  transition an operator setting this up for the first time performs.
- **CLI**: `showmeshctl resolume instance list|set|remove`. Not folded
  into `showmeshctl config`, whose help text, payload shapes, and `409`
  behaviour are all FPP-endpoint-specific.
- **UI**: a Resolume instance section on `/config`, alongside the existing
  FPP endpoint rows, which is what makes the "FPP & Resolume" nav label
  true. Server-side validation only, mirrored in the browser, per ADR-030.

**Acceptance is behavioural, not unit.** With a coordinator started
against an empty store and no Resolume environment variable set, an
operator must be able to open the UI, add `http://host:9080`, and see
`resolume.reachable` go true, with no container restart and no shell.
Then the same round trip through `showmeshctl` alone.

### G-3: `fpp.mqtt`

Same treatment for the Step 5 FPP MQTT collector: broker URL, username,
password, topic prefix, and the host map.

**Open decision for the owner, stated in `docs/private/DECISION-QUEUE.md`
before this seam starts.** The broker password must be recoverable to be
used, so unlike a principal password it cannot be hashed. Storing it in a
configuration revision makes the SQLite volume credential-bearing and makes
the credential immutable in revision history, which is the point of
[ADR-009](../decisions/ADR-009-storage-and-export.md)'s immutable
revisions and is exactly wrong for a secret an operator may need to rotate.

Recommended: the payload carries the credential, `GET` never returns it
and reports presence only, an absent key on `PUT` means "keep the stored
value" per G-1 rule 3, and revisions store a reference rather than the
secret itself so rotation does not write a permanent copy. The alternative,
leaving credentials in the environment and moving only the non-secret
fields, leaves the subsystem half-unreachable and is not recommended.

### G-4: `assets.settings`

Content base URL, max upload bytes, sync interval, inventory interval.

`SHOWMESH_ASSET_DIR` **stays an environment variable** and is the worked
example of G-1's boundary: it is a filesystem path backed by a volume
mount that must exist before the process starts, not a setting an operator
changes while the show runs.

### G-5: identity administration

Owner decision 2026-08-17: full treatment, API plus CLI plus UI.

- **Scopes**: `principal:write` exists and currently guards nothing; this
  seam is its first caller. Mint `principal:read` for the list and read
  paths, and add it to the admin bundle.
- **API**: list and create principals, change role, enable and disable,
  reset password, and list, issue and revoke tokens. Every one audited.

  **`invalidate-all-sessions` is deliberately NOT among them, correcting
  this document's own first draft.** It was listed here as an API
  capability, which contradicted this seam's own closing sentence two
  bullets down and, more importantly, contradicted
  [ADR-024](../decisions/ADR-024-identity-authorization-and-audit.md)
  decision 9: lockout recovery "is a coordinator subcommand run against the
  data volume on the host, requiring filesystem access, which is equivalent
  to owning the deployment. It is **not reachable over the API at any
  scope**." The operation exists for the case decision 5 describes, a
  restore from a backup taken before a revocation, which rolls the
  generation counter back along with everything else. An API path for it
  would be an API path that undoes every session revocation, reachable
  precisely when the API's own authority is what is in doubt. The G-5
  builder caught this and refused to build it, which was correct. A token is displayed exactly once at
  creation, per ADR-024 decision 1, which the API shape must not quietly
  break by making it re-readable.
- **A refusal that must be argued, not assumed.** Disabling the last
  enabled admin principal, or revoking the last credential that can reach
  `principal:write`, locks the operator out of their own coordinator with
  no shell available to recover. Refuse it, name the reason, and check the
  refusal against ADR-024 decision 7's test: name the actor the refusal
  holds accountable and state what the refusal removes. Here the refusal
  removes nothing an operator needs during a show and prevents an
  unrecoverable state, which is the opposite direction from the audit-gate
  defect that record warns about.
- **Bootstrap stays coordinator-local.** No principal exists yet, so
  there is nothing to authenticate; this is a genuine environment-side
  capability and G-1's boundary should say so explicitly.
- **CLI**: `showmeshctl principal list|create|disable|enable|reset-password`
  and `showmeshctl token list|issue|revoke`.
- **UI**: an access page. Reads render under `principal:read`; every
  control follows the existing `ScopedButton` posture where an unknown or
  stale scope list renders as not permitted, never permissive.

The coordinator subcommands stay as the break-glass path for a coordinator
with no reachable admin, and their help text should say that is what they
are for.

### G-6: `showmeshctl macro put`

Close the Class 2 inversion. Add macro definition writes to the CLI over
the existing `PUT /api/v1/config/show.macro/{id}`, and delete the stale
help text in `cmd/showmeshctl/cmd_macro.go` that defers to a track which
has shipped.

### G-8: the Operator UI for Track E

Added 2026-08-17 by owner decision, reversing the same day's scope call.
Closes the Class 3 findings above: routes and controls for `/config/show`,
`/config/show.surface`, `/config/show.active`, the asset browser with
upload and download, `/assets/manifest`, and `/audit`.

Every endpoint and every CLI verb already exists, so this seam adds no API
surface and no capability. It is the third client of work already shipped.

Three rules bind it beyond the ordinary ones. **Activation is the sharp
control**: making a show active changes what every node is expected to
hold, so it states what it is about to do and what will change, and
confirms. **Uploads state progress and failure rather than inferring them,
a partial upload registers nothing, and target selection is mandatory**
because the target is part of an asset's identity
([ADR-030](../decisions/ADR-030-operator-ui-is-the-authoring-surface.md),
[ADR-028](ADR-028-show-asset-store-and-identity.md)). And the manifest
view must keep `not_ready` and `unknown` visually distinct, never collapsed
into one "not ok" state: `showmeshctl` spends two separate exit codes (20
and 21) keeping them apart, because a client that conflates "I checked and
it is missing" with "I cannot tell" will either start a show it should not
or block one it should not.

### G-7: the guards, so this does not recur

Both are structural, in the spirit of `ParameterID.MarshalJSON` returning
an error rather than a comment asking nicely.

1. **Environment inventory test.** Enumerate every `SHOWMESH_*` constant in
   `internal/coordinator/config/config.go` and assert each appears on an
   explicit allow-list of start-time settings, with a comment on each entry
   saying why it must be known before the process starts. Adding a new
   operator-facing environment variable then fails the build rather than
   shipping an unreachable capability. This test is the executable form of
   ADR-039.
2. **Write-parity test.** Every non-`GET` path in `api/openapi.yaml` must
   appear in a `showmeshctl` command registry. A new write endpoint with no
   CLI verb fails the build. This is CLAUDE.md's existing rule with
   enforcement attached; it is currently honoured by discipline alone,
   which is how G-6 got missed.

The write-parity test cannot run against the UI, which is a separate
program in a separate language, so UI coverage stays a review obligation.
That asymmetry should be stated in ADR-039 rather than left to be
discovered.

## Verification

Per CLAUDE.md's multi-track rules, every seam ends with running-binary
gates actually executed: `make check`, `make test-integration`, and the FPP
integration suite where FPP is touched. Beyond that, each of G-2 through
G-6 has a **cold-start acceptance run**: a coordinator started against an
empty store with the relevant environment variable unset, driven through
the UI and then through `showmeshctl` alone, with the resulting observation
or effect confirmed from evidence post-dating the dispatch.

The cold-start run is the gate that matters. Every defect this track exists
to fix passed a green unit suite.
