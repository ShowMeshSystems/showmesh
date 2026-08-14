# Track E: Show authoring and assets

[Build plan](BUILD-PLAN.md) · [ADR-027](../decisions/ADR-027-show-and-surface-model.md) · [ADR-028](../decisions/ADR-028-show-asset-store-and-identity.md) · [ADR-029](../decisions/ADR-029-logical-actions-and-integration-bindings.md) · [ADR-030](../decisions/ADR-030-operator-ui-is-the-authoring-surface.md)

Status: not started. Specified 2026-08-13 from the owner's authoring specification.

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

**E1. The configuration objects.** Show, Surface, Action, Macro, and the active-show pointer. All are new `kind` values under the existing `config_objects` and `config_revisions` tables, so **no schema migration is needed** for any of them. Each carries a show reference except the show itself and the active pointer.

**E2. Manual surface configuration**, meaning channel range, geometry, node assignment, and output settings entered by the operator. Per ADR-027 decision 4 this is a permanent first-class path, and until the FPP Connect work lands **it is the only path that exists**, so it is built first and built properly rather than as a stub behind a nicer future.

**E3. The asset store.** Metadata in SQLite, bytes in a pluggable backend: a directory in the coordinator's volume, a mount, an SMB share on the NAS, or a node advertising a storage capability. **Bytes never go in SQLite.** Identity is show plus logical sequence plus target plus content hash, with the runtime filename preserved separately.

**E4. Manual asset upload**, through the API with the UI on top. Target selection is **mandatory** for node-specific assets, because the target is part of the identity and a defaulted target produces a confidently mislabelled artifact.

**E5. The asset manifest and validation.** Per node, what it should hold and whether what it holds matches. A node's readiness is not "the file exists" but "the variant assigned to this node for this sequence exists locally and matches the expected artifact", which is [ADR-003](../decisions/ADR-003-desired-and-observed-state.md)'s desired-versus-observed split applied to files.

**E6. The asset sync service.** One generic mechanism for every node type. Runs on upload and on a timer, **never in response to a show starting**.

**E7. Logical actions and their bindings**, with the Resolume adapter as the first consumer. Track D builds the adapter; this track builds the action vocabulary and the binding configuration it reads.

**E8. The authoring UI**, per ADR-030. Last, because every screen depends on an API endpoint that has to exist first.

## What is deferred to early October

**FPP Connect compatibility**, meaning ShowMesh render nodes appearing as their own targets in xLights so it renders and delivers per-node FSEQ files automatically. The owner wants this and has scheduled it for early October rather than day-0.

The reason it cannot be scheduled sooner is that **the required API surface is unknown**. Research is under way and will land in [RES-003](../research/RES-003-xlights-fpp-connect-compatibility.md). The owner's position on the risk is recorded and worth carrying: none of this project's integrations rest on a published stable contract, an xLights or FPP update could break any of them, and that is not a reason to avoid the work.

Everything in E1 through E8 is built so this arrives as an additional ingestion path rather than a redesign. That is the point of ADR-028 decision 8: many ingestion paths, one internal model.

## Decisions this track must make

- **What the surface configuration payload actually contains.** ADR-027 lists the fields conceptually; the schema is this track's to design, and it is the thing Track B's renderer reads.
- **How a node reports what it holds.** The manifest comparison needs the node's actual state, which means either the agent reports its inventory or the coordinator tracks what it sent. The first is evidence and the second is an assumption, and this project has a strong preference between those.
- **What happens to assets when the active show changes.** Nodes will be holding the wrong set. Sync brings them current, and the readiness surface has to state the gap rather than appearing fine while a node lacks half its files.
- **Upload size limits, timeouts, and behaviour on a full disk.** The coordinator has never moved bytes before. ARCHITECTURE §11 already lists disk exhaustion as a failure mode this system must address.

## Acceptance criteria

- An operator creates a show, defines a surface with a manual channel range, assigns it to a node, and configures NDI output, **entirely through `showmeshctl`** with no browser. This is ADR-030 decision 1's test and it is deliberately the first criterion.
- Three FSEQ files with the **same filename** and different content are uploaded for three different nodes, and each node resolves to and holds the correct one.
- A node missing an asset reports as not ready, naming what is missing, before any show starts.
- A corrupted or truncated asset fails its hash check and is reported rather than being served or silently accepted.
- An upload interrupted partway registers **nothing**, rather than a half-written artifact that resolves as present.
- Changing the active show updates every node's manifest, and nodes lacking the new assets say so.
- The store being unreachable when a node receives an asset means the node **holds it and retries**, losing nothing and blocking nobody.
- With the UI container removed, everything above still works.

**Bound by:** ADR-003, ADR-009, ADR-011, ADR-014, ADR-020, ADR-024, ADR-026, ADR-027, ADR-028, ADR-029, ADR-030.

**Out of scope:** any sequence or timeline editing, reimplementing xLights' per-controller FSEQ rendering, FPP Connect compatibility (early October, above), and asset retention policy across seasons.
