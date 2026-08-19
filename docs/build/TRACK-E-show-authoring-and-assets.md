# Track E: Show authoring and assets

[Build plan](BUILD-PLAN.md) · [ADR-027](../decisions/ADR-027-show-and-surface-model.md) · [ADR-028](../decisions/ADR-028-show-asset-store-and-identity.md) · [ADR-029](../decisions/ADR-029-logical-actions-and-integration-bindings.md) · [ADR-030](../decisions/ADR-030-operator-ui-is-the-authoring-surface.md)

Status: **E1 through E6 are built, reviewed, fixed and acceptance-run, 2026-08-16.** **E7 is built, reviewed, fixed and acceptance-run, 2026-08-18, on `track-e/e7-actions-bindings` (not yet merged to `main`)**; see its deliverable entry below. E8 remains not started; see the deferral note under it. Specified 2026-08-13 from the owner's authoring specification; narrowed into buildable seams and its identifiers pre-assigned in [TRACK-E-SESSION-SPEC.md](TRACK-E-SESSION-SPEC.md), which is the authoritative detail for E1–E6 — this document is not amended to restate it.

## Goal

An operator can describe their show to ShowMesh: what the surfaces are, which channels feed them, which node renders each, what the logical actions are and what they bind to, and which assets each node needs. Then ShowMesh gets those assets onto the nodes and can tell whether they arrived intact.

## Why this is less new work than it looks

Three of the four tracks already needed pieces of this and had them recorded as open questions rather than owned by anyone. Surfaces are Track B's B3. Asset delivery was an open decision in **both** Track B (FSEQ) and Track C (audio), flagged in each as something to solve once rather than twice. Logical actions are Track D's D2.

**This track is mostly naming and owning work that was already floating**, plus three genuinely new things: the Show object, the asset store, and the sync service.

## The decisions that shape it

All four ADRs were written on 2026-08-13 and should be read before starting. The load-bearing points:

- **A filename is not an asset identity** ([ADR-028](../decisions/ADR-028-show-asset-store-and-identity.md) decision 1). xLights renders a different FSEQ per target and gives every one the same name. Three artifacts, one filename. A store keyed on filename collapses them and a node renders another node's content.
- **A Show is a namespace, not a container** ([ADR-027](../decisions/ADR-027-show-and-surface-model.md) decision 2). Surfaces, actions and macros are their own configuration objects with a show reference, so editing one binding does not revision the whole show.
- **Playback is always from node-local storage** (ADR-028 decision 5). The store is for management and distribution and is never in the playback path, because a node that fetched at showtime would fail exactly when the coordinator is down.
- **Macros invoke logical actions, never protocol paths** ([ADR-029](../decisions/ADR-029-logical-actions-and-integration-bindings.md)).
- **Every authoring capability exists in the API before the UI, and `showmeshctl` can drive it** ([ADR-030](../decisions/ADR-030-operator-ui-is-the-authoring-surface.md) decision 1).

## Deliverables

**E1. The configuration objects. Built, 2026-08-16.** As specified and built, this is the Show, Surface, and active-show pointer only — Action and Macro (originally listed here) are E7's, deferred below. All are new `kind` values under the existing `config_objects` and `config_revisions` tables, so **no schema migration is needed** for any of them. Each carries a show reference except the show itself and the active pointer. Detail: [TRACK-E-SESSION-SPEC.md](TRACK-E-SESSION-SPEC.md) §2.

**E2. Manual surface configuration, built, 2026-08-16**, meaning channel range, geometry, node assignment, and output settings entered by the operator. Per ADR-027 decision 4 this is a permanent first-class path, and until the FPP Connect work lands **it is the only path that exists**, so it is built first and built properly rather than as a stub behind a nicer future. Detail: session spec §2.2.

**E3. The asset store. Built, 2026-08-16**, as the volume directory backend only — SMB/NAS is deferred configuration behind the same interface, not built or stubbed. Metadata in SQLite, bytes in a pluggable backend. **Bytes never go in SQLite.** Identity is show plus logical sequence plus target plus content hash, with the runtime filename preserved separately. Detail: session spec §3.

**E4. Manual asset upload, built, 2026-08-16**, through the API. Target selection is **mandatory** for node-specific assets, because the target is part of the identity and a defaulted target produces a confidently mislabelled artifact. The UI half is E8, deferred below; the API and `showmeshctl` are the whole delivered surface. Detail: session spec §3.3–3.4.

**E5. The asset manifest and validation. Built, 2026-08-16.** Per node, what it should hold and whether what it holds matches, as a three-valued readiness (`ready`/`not_ready`/`unknown`) computed in one function. Detail: session spec §4.

**E6. The asset sync service. Built, 2026-08-16.** One generic mechanism for every node type. Runs on upload and on a timer, **never in response to a show starting**. Detail: session spec §5.

**E7. Logical actions and their bindings**, with the Resolume adapter as the first consumer. Track D builds the adapter; this track builds the action vocabulary and the binding configuration it reads. **Built, reviewed, fixed, and acceptance-run 2026-08-18** on `track-e/e7-actions-bindings` (not yet merged to `main`). The `show.action` config kind and macro step binding already existed from Step 9; E7 added what ADR-029 still needed on top of them: ADR-027 namespace enforcement on both `show.action` and `show.macro` (a macro step may not reference another show's action, and `?show=` filters both list endpoints server-side), a three-valued (ok/broken/unknown) binding check at `GET /actions/{id}/binding` and `GET /actions/bindings` with `showmeshctl action check` and exit code 29 on broken, and `POST /actions/{id}/invocations` behind scope `show:action:invoke`, which Track F is specified to consume. See the 2026-08-18 BUILD-LOG entry for the full review record, the acceptance evidence, and a disclosed incident (a `mv` collision destroyed and required rebuilding `internal/coordinator/api/actioninvoke.go` from its own tests). ADR-029 decision 3's raw-protocol escape hatch remains unbuilt; E7 only guarded the new invoke endpoint against becoming that hatch by accident.

**E8. The authoring UI**, per ADR-030. Last, because every screen depends on an API endpoint that has to exist first. **Deliberately not started**: `ui/` is D-4's active surface, and E8 depends on E7 as well. Every E1–E6 acceptance criterion was proved through `showmeshctl` alone with the UI container stopped, so nothing here is blocked on E8 landing.

## What is deferred to early October

**FPP Connect compatibility**, meaning ShowMesh render nodes appearing as their own targets in xLights so it renders and delivers per-node FSEQ files automatically. The owner wants this and has scheduled it for early October rather than day-0.

**The surface is no longer unknown.** [RES-003](../research/RES-003-xlights-fpp-connect-compatibility.md) §9 was source-verified on 2026-08-13 against xLights and FPP master plus the shipping xLights release, and the requirement is four items: a UDP ping responder in the v3 layout, `GET /api/system/info`, `GET /api/fppd/multiSyncSystems`, and chunked `PATCH /api/file/{dir}`.

Three findings change how this is built.

**A UDP ping responder is mandatory**, because a confirmed typo in xLights (present in master and in the shipping release) means `typeId` can never be read from `/api/system/info`, and a zero `typeId` is an unconditional rejection. **ShowMesh already has that responder**: `pkg/multisync`'s discover-ping responder from Step 1, which has never been answered by a real FPP and whose v3 conformance is unverified. This is its first real consumer.

**Per-node sparse rendering is automatic and default-on**, given a `channelRanges` string and a mode other than `"master"`. That is the mechanism [ADR-028](../decisions/ADR-028-show-asset-store-and-identity.md) assumed, now confirmed, and it is pulled from the target rather than pushed to it.

**An empty `channelRanges` yields a full non-sparse FSEQ**, which is exactly the gigabytes-per-song case being avoided. A surface with no range configured must be caught at configuration time.

**One hard constraint on this work, decided 2026-08-13.** xLights classifies a device as FPP only when `GET /config.php` returns a title containing the substring `"Falcon Player"`. **Faking that is permitted on the bench and forbidden in any release.** Implementing a protocol is interoperability; claiming to be someone else's product is not. The legitimate path is an upstream vendor listing, which is unresearched and flagged in RES-003 §9.7b. Anyone who reaches the point of writing that string should stop and open the research instead.

The owner's position on the residual risk is recorded and worth carrying: none of this project's integrations rest on a published stable contract, an xLights or FPP update could break any of them, and that is not a reason to avoid the work. RES-003 §9.8 lists what remains unknown, and §9.1 warns specifically against designing around the typo rather than implementing the responder.

Everything in E1 through E8 is built so this arrives as an additional ingestion path rather than a redesign. That is the point of ADR-028 decision 8: many ingestion paths, one internal model.

## Decisions this track must make

Resolved for E1–E6, 2026-08-16, in [TRACK-E-SESSION-SPEC.md](TRACK-E-SESSION-SPEC.md) §0:

- **What the surface configuration payload actually contains.** ADR-027 lists the fields conceptually; the schema is this track's to design, and it is the thing Track B's renderer reads.
- **How a node reports what it holds.** The manifest comparison needs the node's actual state, which means either the agent reports its inventory or the coordinator tracks what it sent. The first is evidence and the second is an assumption, and this project has a strong preference between those.
- **What happens to assets when the active show changes.** Nodes will be holding the wrong set. Sync brings them current, and the readiness surface has to state the gap rather than appearing fine while a node lacks half its files.
- **Upload size limits, timeouts, and behaviour on a full disk.** The coordinator has never moved bytes before. ARCHITECTURE §11 already lists disk exhaustion as a failure mode this system must address.

## Acceptance criteria

**Evidenced against a running coordinator, real agent processes and a real broker, `make test-integration`, 2026-08-16** — every criterion below, plus four the session specification added because they are the traps rather than the features (an absent/`null`/zero-length `channelRange` producing three distinct refusals; two surfaces on one node accepted; a stale inventory report reading `unknown` and never `not_ready` or `ready`; an upload slower than the server's 10-second `ReadTimeout` succeeding). Full detail in the 2026-08-16 BUILD-LOG entry.

- An operator creates a show, defines a surface with a manual channel range, assigns it to a node, and configures NDI output, **entirely through `showmeshctl`** with no browser, **with the UI container stopped**. This is ADR-030 decision 1's test and it is deliberately the first criterion, because the CLI is the path an operator takes when the show is broken and the UI is down. Every authoring verb is exercised against a running coordinator, not only in unit tests: an emergency path nobody has run is an assumption, not a path.
- Three FSEQ files with the **same filename** and different content are uploaded for three different nodes, and each node resolves to and holds the correct one.
- A node missing an asset reports as not ready, naming what is missing, before any show starts.
- A corrupted or truncated asset fails its hash check and is reported rather than being served or silently accepted.
- An upload interrupted partway registers **nothing**, rather than a half-written artifact that resolves as present.
- Changing the active show updates every node's manifest, and nodes lacking the new assets say so.
- The store being unreachable when a node receives an asset means the node **holds it and retries**, losing nothing and blocking nobody.
- With the UI container removed, everything above still works.

**Bound by:** ADR-003, ADR-009, ADR-011, ADR-014, ADR-020, ADR-024, ADR-026, ADR-027, ADR-028, ADR-029, ADR-030.

**Out of scope:** any sequence or timeline editing, reimplementing xLights' per-controller FSEQ rendering, FPP Connect compatibility (early October, above), and asset retention policy across seasons. **Known gaps recorded rather than hidden, carried from the session specification §9**: no delete for shows, surfaces or assets; superseded assets leave orphaned blobs; agent credential provisioning has no path when a deployment closes reads (`SHOWMESH_AGENT_API_TOKEN` must be set by hand — punch-list item, not an assumption); `internal/coordinator/assetsync` takes a concrete `*store.Store` rather than an interface, a deliberate deviation from the `Dependencies` convention. **Nothing here has run on real show hardware**: no real xLights FSEQ was ever uploaded, no upload crossed a real network link, and disk exhaustion was tested by error injection rather than by filling a real volume.
