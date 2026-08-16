# Track E session specification: E1 through E6

[Track E](TRACK-E-show-authoring-and-assets.md) · [Build plan](BUILD-PLAN.md) ·
[ADR-027](../decisions/ADR-027-show-and-surface-model.md) ·
[ADR-028](../decisions/ADR-028-show-asset-store-and-identity.md) ·
[ADR-030](../decisions/ADR-030-operator-ui-is-the-authoring-surface.md)

Status: specified 2026-08-16 by the orchestrating session. E7 and E8 are **out of
scope** and must not be pulled in (E7's action/binding configuration is what Track D's
ADR-037 seam B is rewriting right now; E8's `ui/` is D-4's active surface).

This document narrows the track specification into buildable seams and pre-assigns every
scarce identifier. Where this document and the track specification disagree, this one
wins on scope and the track specification wins on requirements.

---

## 0. Identifiers, minted once, here

The orchestrator alone mints these. A builder that needs one not listed here **stops and
asks** rather than inventing it.

| Class | Track E's allocation |
| --- | --- |
| SQLite schema version | **v8** (`main` tops out at v7; verify before applying) |
| Config kinds | `show`, `show.surface`, `show.active` |
| Scopes | reuse **`config:write`**; mint **`asset:write`** (`identity.ScopeAssetWrite`) |
| `showmeshctl` exit codes | **20** and **21** only (16–19 are Track D headroom) |
| MQTT topics | one new observed subpath: `assets` → `showmesh/nodes/<id>/observed/assets` |
| Agent allowlisted operations | `asset.fetch` |
| ADR numbers | **none**; an ADR-shaped decision goes to `docs/private/DECISION-QUEUE.md` |

New problem type URIs (all under `problemBaseURI`):

- `payload-too-large` — already exists as a value (`ProblemTypeResolumeCompositionTooLarge`).
  Reuse the **same URI string**; do not mint a second one.
- `storage-full` — 507, the disk-exhaustion refusal.
- `asset-target-required` — 400, a node-specific asset uploaded with no target.

New coordinator environment variables:

- `SHOWMESH_ASSET_DIR` — the volume backend's root. Defaults to `<DataDir>/assets`.
- `SHOWMESH_ASSET_MAX_UPLOAD_BYTES` — defaults to `assetstore.DefaultMaxUploadBytes`.
- `SHOWMESH_ASSET_CONTENT_BASE_URL` — the base URL an agent fetches asset bytes from.
  Empty (default) means **the sync service does not run**, stated as evidence rather
  than silently.
- `SHOWMESH_ASSET_SYNC_INTERVAL` — defaults to 5 minutes.

New agent environment variables:

- `SHOWMESH_ASSET_DIR` — node-local asset directory. Defaults to `<state dir>/assets`.
- `SHOWMESH_AGENT_API_TOKEN` — only needed when the coordinator has `CloseReads` set.
  **Never** name it `SHOWMESH_API_TOKEN`: the coordinator refuses to start when it sees
  that variable (ADR-024 decision 2), and an operator who copies an agent `.env` line
  into a coordinator `.env` must not brick the coordinator.
- `SHOWMESH_ASSET_INVENTORY_INTERVAL` — defaults to 2 minutes.

---

## 1. Rules that bind every seam

These are not restatements for flavour. Each one names a defect this project has already
shipped, and each is the most likely wrong thing a builder writes this week.

1. **Absent, `null`, and explicitly empty are three different things on every write
   surface.** A `PUT` with no `channelRange` key is not a `PUT` clearing the channel
   range. Follow `internal/coordinator/config/showaction.go`'s decoder shape exactly:
   decode into `map[string]json.RawMessage`, reject unknown top-level keys, and treat a
   JSON `null` distinctly from an absent key (`isJSONNull`). Every field gets all three
   cases tested. Step 7 shipped a config `PUT` that wiped every endpoint because a
   missing key decoded as empty.

2. **A capability nothing can invoke is not shipped.** Step 6 shipped three. Every
   endpoint added here gets its `showmeshctl` verb **in the same seam**, and the seam is
   not done until the path has been driven end to end against a running coordinator.

3. **A test that passes with the behaviour removed is worse than no test.** Reviewers
   break the behaviour and confirm the test fails. The D-3 review found six decorative
   tests in one diff, one hiding inside the fix for another.

4. **`complete: true` is the licence to assert absence and has to be earned.** A node
   manifest built from an inventory report the coordinator could not read is `unknown`
   with a reason — never `not_ready`, never `ready`. A 40-second broker outage must not
   flip an installation to "missing everything".

5. **Confirmation needs evidence that post-dates the action.** A sync reported complete
   rests on an agent inventory report whose `reportedAt` is after the transfer's own
   dispatch. Step 7 measured a command reporting `confirmed` 179 microseconds after its
   own dispatch off a pre-dispatch reading.

6. **Timeouts on opposite sides of one contract are a single decision.** See §6.

7. **A refusal is not a null action.** Nothing in this track may refuse a *read* because
   a credential, an audit store, or an evidence source was unavailable. Reads stay open
   (ADR-024 constraint 23).

8. **Bytes never go in SQLite** (ADR-028). Metadata rows only.

9. **No verification claims that have not happened.** Nothing in this track touches real
   show hardware. Hardware-dependent checks go to `docs/private/PUNCH-LIST.md`.

10. **The live fleet is read-only, absolutely.** No write, command, restart, settings
    change, or MQTT publish against the deployed FPP hosts or the operator's broker.
    Everything here runs against the local dev stack and the bench container.

---

## 2. Seam E1/E2 — Show, Surface, and the active-show pointer

New configuration **kinds** under the existing `config_objects`/`config_revisions`
tables. **No schema migration.** The pattern to copy is
`internal/coordinator/config/showaction.go` (payload type, `Encode…`, `Decode…` with a
`*ValidationError`) plus `internal/coordinator/api/showconfig.go` (handlers, the
`identity.Service.AuditedWrite` one-transaction pattern, revision listing).

### 2.1 Kind `show`

Object id is the show id (`mqttproto.ValidateNodeID` syntax — the same slug syntax every
other id in this system uses). Payload:

```json
{ "name": "Halloween 2026", "notes": "" }
```

- `name` required, non-empty, ≤ 200 runes. `notes` optional, ≤ 4000 runes, absent means
  unchanged is **not** a thing here: a `PUT` is a full replacement of the payload, and an
  absent `notes` means empty. That is stated in the API description and tested, because
  the alternative (absent means keep) is what erased the operator's node label in Step 7.
  Being explicit either way is fine; being silent is not.
- A Show is a **namespace, not a container** (ADR-027 decision 2). The payload carries no
  list of surfaces, actions, or macros. Adding one is a review-blocking defect.

### 2.2 Kind `show.surface`

Object id is the surface id (same slug syntax). Payload:

```json
{
  "show": "halloween-2026",
  "name": "Garage Door",
  "node": "render-01",
  "channelRange": { "startChannel": 1, "channelCount": 3600 },
  "geometry": { "width": 40, "height": 30, "pixelFormat": "rgb" },
  "frameRate": 40,
  "output": { "transport": "ndi", "ndi": { "sourceName": "ShowMesh Garage" } }
}
```

Validation, all of it server-side (ADR-030 decision 2):

- `show` **must** name an existing `show` config object. Refuse with an
  invalid-parameter problem naming the missing show. Precedent: `showaction.go`'s
  `validateShowRef` and its "must name a configured FPP endpoint" rule.
- `node` **must** name a node with a declaration (`store.ListNodeDeclarations`). Refuse
  otherwise, naming the node.
- `channelRange` is **required and must be non-empty**: `startChannel ≥ 1`,
  `channelCount ≥ 1`. This is the whole point of the check — RES-003 established that an
  empty `channelRanges` makes xLights render a full non-sparse FSEQ, which is the
  gigabytes-per-song case. An absent `channelRange`, a `null` one, and
  `{"startChannel":1,"channelCount":0}` are three distinct refusals with three distinct
  messages.
- `startChannel + channelCount - 1` must not exceed `maxChannelNumber`. **Set that
  constant to 8,388,608 and label its doc comment as a sanity bound this project has not
  verified against FPP, not as a protocol constant.** It exists to catch a typo'd
  `channelCount`, not to enforce FPP's real ceiling.
- `geometry.pixelFormat` ∈ {`rgb`, `rgbw`} → 3 or 4 channels per pixel.
  `width × height × channelsPerPixel` **must equal** `channelCount` exactly. Refuse with
  both numbers in the message. **This is an orchestrator decision, recorded here so a
  later change argues against something written down:** a surface's channel range is the
  range that surface drives, so a mismatch is a configuration error the renderer would
  otherwise discover at frame time. If Track B finds a real padded case, that supersedes
  this with evidence.
- `frameRate` required, integer, 1–120. Its doc comment states that ADR-026's day-0
  profile of 40 fps over NDI on OptiPlex 7040-class hardware is **L0 design intent, a
  target to validate and not a supported profile**.
- `output.transport` ∈ {`ndi`, `hdmi`} (ADR-026: NDI is the reference transport, HDMI a
  supported alternate). Exactly one of `output.ndi` / `output.hdmi` may be present and it
  must be the one matching `transport`. **Support for one transport is never evidence for
  the other**, so nothing here defaults a transport.
- **The coordinator must NOT check the node's advertised capabilities to decide whether
  it can do NDI.** Advertisement is observed state and is absent when the node is offline;
  refusing a configuration write on absent advertisement is manufacturing absence for the
  fifth time. The readiness surface states the gap (§4); the write surface does not.
- **`N=1` is a scope limit that must not reach the schema** (ADR-026). A second surface
  assigned to the same node is **accepted**. Write the test that proves it.

### 2.3 Kind `show.active`

A singleton. Fixed object id `active`, a package constant, **never** derived from any
configuration value — the reasoning is in `resolumecomposition.go`'s
`resolumeCompositionObjectIDConst` doc comment (renaming an unrelated identifier orphaned
every stored revision). Payload:

```json
{ "show": "halloween-2026" }
```

`show` must name an existing `show` config object. The active show is configuration:
revisioned and audited, exactly like every other kind here, so that programming Christmas
cannot break Halloween (ADR-027).

### 2.4 Routes

```
GET    /api/v1/config/show                      list summaries
GET    /api/v1/config/show/{id}
PUT    /api/v1/config/show/{id}
GET    /api/v1/config/show/{id}/revisions
GET    /api/v1/config/show.surface              list summaries (optional ?show=)
GET    /api/v1/config/show.surface/{id}
PUT    /api/v1/config/show.surface/{id}
GET    /api/v1/config/show.surface/{id}/revisions
GET    /api/v1/config/show.active
PUT    /api/v1/config/show.active
GET    /api/v1/config/show.active/revisions
```

Reads use `readAnyGuard(showConfigReadScopes, …)`, matching the existing
`show.action`/`show.macro` routes. Writes use `writeGuard(&scopeConfigWrite, …)`.

`GET /api/v1/config/show.active` when nothing has ever been activated answers **404 with
`resourceNotFoundProblem`**, matching what `fpp.endpoints` and `resolume.composition`
already answer for "nothing configured yet". Two configuration surfaces answering
differently for the same condition is a thing an operator has to learn twice.

### 2.5 CLI, in this seam

```
showmeshctl show list
showmeshctl show get <id>
showmeshctl show set <id> --name <name> [--notes <notes>]
showmeshctl show revisions <id>
showmeshctl show active                       # print the active show
showmeshctl show activate <id>
showmeshctl surface list [--show <id>]
showmeshctl surface get <id>
showmeshctl surface set <id> --show <id> --name <n> --node <n> \
    --start-channel N --channel-count N --width N --height N \
    [--pixel-format rgb|rgbw] --frame-rate N \
    --transport ndi|hdmi [--ndi-source-name <s>] [--hdmi-display <s>]
showmeshctl surface revisions <id>
```

`surface set` is a **full replacement**, and its `--help` says so in one sentence. It
does not read-modify-write, because a partial update against an immutable-revision store
is how a concurrent edit gets silently discarded.

`showmeshctl` may not import any coordinator package; the import-graph test enforces it.
Decode responses into this package's own types.

---

## 3. Seam E3/E4 — the asset store and manual upload

### 3.1 Schema v8

Three tables. Nothing here holds bytes.

```sql
CREATE TABLE assets (
    id                         TEXT PRIMARY KEY,
    show_id                    TEXT NOT NULL,
    sequence_id                TEXT NOT NULL,
    target_kind                TEXT NOT NULL,   -- 'node' | 'show'
    target_id                  TEXT NOT NULL,   -- node id, or '' when target_kind='show'
    media_type                 TEXT NOT NULL,   -- 'fseq' | 'audio' | 'media'
    content_hash               TEXT NOT NULL,   -- 'sha256:<hex>'
    runtime_filename           TEXT NOT NULL,
    size_bytes                 INTEGER NOT NULL,
    backend                    TEXT NOT NULL,   -- 'volume'
    storage_key                TEXT NOT NULL,
    created_at                 TEXT NOT NULL,
    created_by_principal_id    TEXT NOT NULL,
    created_by_principal_name  TEXT NOT NULL,
    superseded_at              TEXT
);

-- ADR-028 decision 1: identity is show + logical sequence + target + content hash.
CREATE UNIQUE INDEX assets_identity
    ON assets (show_id, sequence_id, target_kind, target_id, content_hash);

-- Exactly one CURRENT asset per (show, sequence, target), enforced structurally
-- rather than by a convention a later query could forget.
CREATE UNIQUE INDEX assets_current
    ON assets (show_id, sequence_id, target_kind, target_id)
    WHERE superseded_at IS NULL;

CREATE INDEX assets_by_target ON assets (target_kind, target_id);

-- What a node reports it actually holds. Evidence, not bookkeeping.
CREATE TABLE node_asset_inventory (
    node_id          TEXT NOT NULL,
    content_hash     TEXT NOT NULL,
    runtime_filename TEXT NOT NULL,
    size_bytes       INTEGER NOT NULL,
    verified_at      TEXT NOT NULL,
    PRIMARY KEY (node_id, content_hash)
);

-- The report ITSELF, so "we have never heard from this node" is distinguishable
-- from "this node holds nothing".
CREATE TABLE node_asset_reports (
    node_id     TEXT PRIMARY KEY,
    reported_at TEXT NOT NULL,
    complete    INTEGER NOT NULL,
    reason      TEXT NOT NULL
);
```

`runtime_filename` is stored and served and is **never** part of any key or lookup
(ADR-028 decision 1: xLights gives three different artifacts the same filename). A test
uploads three files named `Thriller.fseq` with different bytes for three different nodes
and asserts each node resolves to its own.

Times are stored in this package's existing text-time convention — copy whatever
`store/config.go` and `store/observations.go` already do rather than inventing a second
one.

### 3.2 `internal/coordinator/assetstore` — a new package

```go
type Backend interface {
    // Put streams r into the backend, returning the content hash and byte
    // count. It writes to a temporary name and renames into place only after
    // the whole stream has been read and hashed.
    Put(ctx context.Context, r io.Reader, limit int64) (Blob, error)
    Open(ctx context.Context, key string) (io.ReadSeekCloser, int64, error)
    Stat(ctx context.Context, key string) (int64, error)
}
```

Ship **only** the volume directory backend. SMB/NAS is configuration for later behind the
same interface; do not build it, do not stub it in a way that looks built.

Volume backend rules, each of which is an acceptance criterion:

- **Content-addressed layout**: `<root>/<hex[0:2]>/<hex>`. Identical bytes uploaded twice
  occupy one file.
- **Stage, hash, rename.** Write to `<root>/.staging/<random>`, hash while writing, then
  `os.Rename` into the final path. An interrupted upload leaves a staging file and
  **registers nothing** (ADR-030). The staging directory is swept on startup.
- **A partial upload never becomes a blob.** If the reader errors, the staging file is
  removed and the error returned. There is no code path that renames an incompletely
  read stream.
- **Full disk**: `ENOSPC` (and `EDQUOT`) is classified, not swallowed. The store returns
  a sentinel `ErrNoSpace`; the API turns it into `507` with the `storage-full` problem
  type, and **nothing is registered**. ARCHITECTURE §11 names disk exhaustion as a
  required failure mode; this is where it is addressed.
- **Corruption is detectable**: `Open` does not re-hash (that would make every read
  O(file)), but the manifest and the agent both verify hashes, and a
  `GET …/content` whose on-disk size disagrees with the recorded `size_bytes` fails
  loudly rather than serving a truncated body. A truncated asset is **reported, never
  served** (acceptance criterion 4).

### 3.3 Routes

```
POST   /api/v1/assets                    multipart upload            asset:write
GET    /api/v1/assets                    list (?show= ?node= ?sequence=)
GET    /api/v1/assets/{id}
GET    /api/v1/assets/{id}/content       the bytes
```

`POST /api/v1/assets` is `multipart/form-data` with one file part named `file` plus form
fields `show`, `sequence`, `mediaType`, `targetKind`, `target`. Copy
`readResolumeCompositionFilePart`'s part-handling rules (exactly one part named `file`; a
second one refuses; a plain form field with no filename refuses) but **stream the part to
the backend instead of buffering it in memory** — an FSEQ is not a 407 KB XML file.

- `targetKind` is **required**, with no default. `targetKind=node` requires a non-empty
  `target` naming a declared node; a missing one is the `asset-target-required` problem.
  ADR-030: "target selection is mandatory because the target is part of the asset's
  identity" and a defaulted target produces a confidently mislabelled artifact.
- `show` must name an existing `show` config object; `sequence` is a slug.
- Re-uploading **identical bytes** for an identity that already exists is idempotent:
  200 with the existing asset, no new row, no new blob.
- Uploading **different bytes** for the same (show, sequence, target) creates a new
  asset and marks the previous one `superseded_at` **in the same transaction**, so the
  partial unique index can never see two current rows.
- The metadata row and its audit entry are written in one transaction
  (`identity.Service.AuditedWrite`, ADR-024 decision 11), **after** the bytes are whole
  and hashed. A failure anywhere before that leaves no row. Orphaned blobs from a failed
  registration are acceptable and are retention's problem (explicitly out of scope);
  orphaned rows are not.

`GET /api/v1/assets/{id}/content` is a read: `readGuard(identity.ScopeNodeRead, …)`, open
by default like every other read. It sets `Content-Length`, `ETag: "<contentHash>"`, and
`Content-Type: application/octet-stream`, and supports `Range` via `http.ServeContent`
so an interrupted agent transfer can resume.

### 3.4 CLI, in this seam

```
showmeshctl assets list [--show <id>] [--node <id>] [--sequence <id>]
showmeshctl assets get <assetId>
showmeshctl assets upload --show <id> --sequence <id> --media-type fseq|audio|media \
    --target-kind node|show [--target <nodeId>] --file <path>
showmeshctl assets fetch <assetId> --out <path>     # verifies the hash before writing
```

`assets upload` streams the file rather than reading it into memory, prints progress to
stderr, and **states failure rather than inferring success** (ADR-030 decision 4). A
non-2xx response prints the problem's `Detail`.

---

## 4. Seam E5 — the manifest and validation

### 4.1 What a node should hold

Derived, never stored as a second copy:

1. Read the active show (`show.active`). If none is set, every node's manifest is
   `unknown` with reason `no active show is configured` — **not** `ready`, and not an
   empty expectation that reads as fine.
2. Expected assets for node *N* = every current (`superseded_at IS NULL`) asset whose
   `show_id` is the active show and whose target is either `node`/*N* or `show`.
3. A node that has a `show.surface` assigned to it in the active show, but no coverage
   for a sequence the rest of the show already has assets for, is a **stated gap**, not
   an omission.

   *Corrected 2026-08-16.* An earlier version of this clause said "no current asset for
   that surface's sequence". A `show.surface` carries no sequence reference and should
   not: sequences are show-level (a song), and a surface is the channel extraction that
   every sequence is rendered through. The gap is therefore inferred from the show's own
   asset rows, which is what the builder implemented. If a real surface-to-sequence link
   is ever needed, that is an owner decision, not a schema tweak.

### 4.2 What a node actually holds

The agent publishes a retained inventory report on
`showmesh/nodes/<id>/observed/assets` (`mqttproto.ObservedTopic(nodeID, "assets")`,
`ObservedDeliveryPolicy` — retained, QoS 1) after every sync operation and on
`SHOWMESH_ASSET_INVENTORY_INTERVAL`. Payload, inside the existing envelope:

```json
{
  "complete": true,
  "reason": "",
  "assets": [
    { "contentHash": "sha256:…", "filename": "Thriller.fseq",
      "sizeBytes": 1234567, "verifiedAt": "2026-08-16T12:00:00Z" }
  ]
}
```

**`complete` has to be earned.** The agent sets it `false` with a reason when it could
not enumerate its own directory, when a file's hash could not be computed, or when the
asset directory does not exist. It never reports `complete: true` off a partial walk.
This is the coordinator's decision to say a file is *absent*; a discovery run
manufacturing absence from ambiguous liveness is the fourth subsystem this project caught
doing it.

### 4.3 The manifest

`GET /api/v1/nodes/{nodeId}/assets` and `GET /api/v1/assets/manifest` (every node).

Per node, a three-valued readiness in **exactly one function** that names its failing
term — copy the shape of Track D's layer-readiness conjunction rather than scattering the
rule:

- `ready` — a fresh, `complete` inventory report holds every expected content hash.
- `not_ready` — a fresh, `complete` inventory report is **missing** named assets. The
  response lists each missing asset by sequence, filename, and content hash.
- `unknown` — with a reason. Every one of these is a distinct reason string:
  no inventory report has ever been received; the last report is older than the staleness
  window; the last report said `complete: false`; no active show is configured.

`unknown` may **never** render as `ready`, and a node whose report is stale may **never**
render as `not_ready`. A stale report is not evidence of absence.

Staleness window: `3 × SHOWMESH_ASSET_INVENTORY_INTERVAL`, computed in one place and
exported so the API, the CLI and the tests read the same number.

Extra assets a node holds that are not expected are **reported** as `extra`, not treated
as an error and never deleted. Deleting a file because the coordinator did not expect it
is how a cloned SD card or a mid-reconfiguration node loses its show.

### 4.4 CLI, in this seam

```
showmeshctl assets manifest [--node <id>]
showmeshctl assets manifest --require-ready      # exit 20 / 21, see below
```

Exit codes, the only two Track E mints:

- **20 `exitAssetsNotReady`** — at least one node is `not_ready`. A script can branch on
  "do not start the show yet".
- **21 `exitAssetsUnknown`** — at least one node is `unknown` and none is `not_ready`.
  Deliberately distinct: "I cannot tell" and "I checked and it is missing" are different
  operational situations and collapsing them is the exact error this project keeps
  finding.

Both codes appear in `--help` alongside the existing ones.

---

## 5. Seam E6 — the sync service

### 5.1 Coordinator side

`internal/coordinator/assetsync`. Runs on upload and on `SHOWMESH_ASSET_SYNC_INTERVAL`,
**never in response to a show starting** (ADR-028 decision 7). If a future caller wants
to trigger sync from a macro step, that is a new decision, not an omission to fill.

For each declared node, compute the gap (§4.1 minus §4.2) and dispatch one
`asset.fetch` command per missing asset over the existing agent command path
(`mqttproto.CmdTopic`, QoS 1, idempotency key), with params:

```json
{ "assetId": "…", "contentHash": "sha256:…", "filename": "Thriller.fseq",
  "sizeBytes": 1234567, "url": "https://coordinator/api/v1/assets/<id>/content" }
```

- When `SHOWMESH_ASSET_CONTENT_BASE_URL` is empty the service **does not run** and says
  so once at startup and in the manifest response's own reason field. It does not
  silently do nothing.
- Concurrency is bounded (at most 2 in-flight fetches per node, at most 8 overall) so a
  new show's upload does not become a fleet-wide stampede.
- The service **never** deletes a node-side asset. Not for a superseded asset, not for an
  extra one, not for a show change.
- A sync is reported complete only on an inventory report whose `reportedAt` post-dates
  the fetch dispatch (§1 rule 5).

### 5.2 Agent side

A second entry in `newOperationRegistry()`: `asset.fetch`. That map **is** the allowlist
(ADR-024's "agents accept only allowlisted operations"), so adding an operation is adding
a map entry, not adding a bypass.

- Validates the URL's scheme is `http`/`https` before any request.
- Downloads to `<SHOWMESH_ASSET_DIR>/.staging/<random>`, hashing while writing, with
  `Range` resume on retry.
- **Verifies the content hash before reporting the asset held.** A mismatch discards the
  staged file and reports the mismatch as evidence. It never renames unverified content
  into place and it never stops the agent — the ADR-025 shape: verification failure is
  evidence, never a new way to stop a show.
- Renames into `<SHOWMESH_ASSET_DIR>/<filename>` only after verification. The node-local
  layout is by runtime filename because that is what a player opens; the inventory report
  carries the hash, so identity survives.
- **The store being unreachable means the node keeps what it has and retries.** No local
  file is removed, the failure is reported as evidence on the result topic, and the next
  sync tick tries again. Nothing is lost and nothing is blocked (acceptance criterion 7).
- Republishes its inventory after every fetch, so the coordinator's confirmation rests on
  a post-dispatch report.

### 5.3 Changing the active show

Changing `show.active` recomputes every node's expected set on the next manifest read —
there is no cached manifest to invalidate, which is deliberate. The sync service picks
the new set up on its next tick and on the activation itself. Nodes lacking the new
assets read `not_ready` and name what is missing (acceptance criterion 6). Nothing is
deleted.

---

## 6. The timeout budget — one decision, one place

Put every number in `internal/coordinator/assetstore/budget.go`, exported, with the
derivation in the doc comment, and write the test that asserts the client budget is
**greater than or equal to** the server budget. This project has hit the two-timeouts
problem three times (Step 7's CLI at 10 s against a server at 20 s; Step 9's SIGTERM at
10 s against a shutdown at 10 s; D-3's write deadline sized off a bound that was not
being honoured).

- `DefaultMaxUploadBytes` = 2 GiB.
- `MinTransferBytesPerSecond` = 1 MiB/s — a labelled assumption about the slowest link
  this is expected to work over, not a measurement.
- `UploadBudget(size int64) time.Duration` = `size / MinTransferBytesPerSecond` + 30 s
  grace, clamped to a 2 hour ceiling.

The coordinator's HTTP server sets `ReadTimeout: 10 * time.Second`
(`internal/coordinator/httpapi/server.go:90`). **A 200 MB upload over anything but a LAN
dies at ten seconds against that.** The upload handler must extend its own read deadline
with `http.NewResponseController(r.Body …).SetReadDeadline` — `stream.go` already does
exactly this for writes and `TestStreamSurvivesServerWriteTimeout` is the precedent for
proving it. Write the equivalent test: an upload slower than `ReadTimeout` must succeed.

`showmeshctl assets upload` and `assets fetch` compute their HTTP client timeout from the
same `UploadBudget`, not from a hand-picked constant.

---

## 7. Acceptance criteria, all CLI-driven with the UI container stopped

Each is verified against a **running coordinator** with **real agent subprocesses**, not
only in unit tests. `make test-integration` is where these live.

1. Create a show, define a surface with a manual channel range, assign it to a node,
   configure NDI output — entirely through `showmeshctl`, no browser, UI container
   stopped.
2. Three FSEQ files with the **same filename**, different content, uploaded for three
   nodes; each node resolves to and holds the correct one.
3. A node missing an asset reports `not_ready`, naming what is missing, before any show.
4. A corrupted or truncated asset fails its hash check and is reported, never served.
5. An interrupted upload registers **nothing**.
6. Changing the active show updates every manifest; nodes lacking the new assets say so.
7. The store unreachable → the node holds what it has and retries; nothing lost, nothing
   blocked.
8. Everything above with the UI container removed.

Plus the ones this specification adds because they are the traps, not the features:

9. A `PUT` with an absent `channelRange`, a `null` `channelRange`, and a zero
   `channelCount` produce three distinct refusals, and none of them silently clears
   anything.
10. Two surfaces assigned to the same node are accepted (`N=1` never reaches the schema).
11. A node whose inventory report is stale reads `unknown` with a reason, never
    `not_ready` and never `ready`.
12. An upload slower than the server's 10 second `ReadTimeout` succeeds.

---

## 8. Gates

Per seam: `make check` and `make test-integration`, actually executed and their output
captured. The FPP integration suite only if FPP code is touched — it should not be. Full
gates on `main` after every fold. `make ui-gen-check` must stay byte-identical:
regenerated types only, **no UI screens** (E8 is deferred and `ui/` is D-4's surface).

`api/openapi.yaml` is updated in the same seam as each endpoint and the conformance test
stays green in both directions.

---

## 9. Known gaps, recorded rather than hidden

- **No delete for shows, surfaces, or assets.** Retention across seasons is explicitly out
  of scope, and a delete surface for an immutable-revision store is its own decision.
  A retired surface is handled by show scoping: surfaces of a non-active show are not in
  any manifest.
- **Superseded assets leave orphaned blobs.** Retention's problem, out of scope.
- **Agent credentials when `CloseReads` is set.** The agent reads asset bytes over the
  open read API. A deployment that closes reads must set `SHOWMESH_AGENT_API_TOKEN` by
  hand; there is no provisioning path. Punch-list item, not a silent assumption.
- **Nothing here has run on real show hardware**, and no NDI or HDMI output exists yet to
  render a configured surface. A surface is configuration Track B will read.
