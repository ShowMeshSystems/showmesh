# ADR-028: The Show Asset Store, Asset Identity, and Distribution

Status: Accepted  
Date: 2026-08-13

## Context

Day-0 requires FSEQ files on render nodes and audio files on the audio node. Neither exists as a mechanism, and both were open questions in [Track B](../build/TRACK-B-nodes-and-projection.md) and [Track C](../build/TRACK-C-audio-node.md) with an instruction to solve them once rather than twice. This record is that.

**The operator's numbers decide the shape.** The reference installation's xLights project carries **over 30,000 channels before any matrix content**, and a full-show FSEQ containing every channel would run to gigabytes per song. xLights and FPP Connect already render per-target FSEQ files containing only the channels a given controller needs, and the operator reports that *not* splitting is actually the harder path. So ShowMesh receives per-node FSEQ variants rather than one whole-show file, and the entire design below follows from that.

## Decision

### 1. A filename is not an asset identity

This is the load-bearing decision and everything else is machinery around it.

For one xLights sequence, FPP Connect generates a **different FSEQ for each target**, containing only that target's channels, and gives **every one of them the same filename**:

```
media-front:   HalloweenOpening.fseq   (front projection channels)
media-side:    HalloweenOpening.fseq   (side projection channels)
media-garage:  HalloweenOpening.fseq   (garage projection channels)
```

Three distinct artifacts, one filename. Any store keyed on filename silently collapses them, and the failure mode is a node rendering another node's content, which looks like a mapping bug and is not one.

**ShowMesh assigns every asset an internal identity independent of its runtime filename.** For FSEQ content that identity distinguishes at minimum the show, the logical sequence, the target node or surface, and the artifact's content hash.

This is the fourth time this project has decided that two things which look the same are not the same. Absent is not null is not empty; a retained MQTT message is not a fresh one; `unsupported` is not `unknown`; and now a filename is not an identity.

### 2. The runtime filename is preserved, and resolution happens in node context

Internal uniqueness must never require renaming what a node plays. FPP, xLights, and the local renderer all expect the filename they were given, so a node stores and plays `HalloweenOpening.fseq` while the store holds that object under its own identifier.

ShowMesh resolves `logical sequence + target node` to the correct stored variant and then to the expected local filename. **The identical local filename is unambiguous because resolution always occurs in the context of one node.**

### 3. One asset store and one sync service, for every node type

Audio, FSEQ, images, video, and announcements use the same store, the same identity model, and the same distribution mechanism. Building a separate path per node capability is how a system acquires four distribution mechanisms with four different failure modes and one shared bug.

### 4. Storage is pluggable, and bytes never live in SQLite

The store's backend is a deployment choice: a directory in the coordinator's volume, a mounted filesystem, an SMB share on a NAS, or a node advertising a storage capability under [ADR-002](ADR-002-capability-based-nodes.md).

**Metadata lives in SQLite; bytes do not.** [ADR-009](ADR-009-sqlite-configuration-storage.md) makes SQLite the authoritative store for configuration and state, and gigabyte blobs in it would be a straightforward misreading of that: it would inflate the file every backup and export has to move, and [ADR-012](ADR-012-docker-coordinator-deployment.md) expects a coordinator that runs on modest hardware. The database holds identity, provenance, hash, size, and assignment. The backend holds the file.

### 5. Playback is always from node-local storage

**A node plays from its own disk, always.** The store is for management, distribution, and validation. It is never in the playback path.

This is what keeps the standing constraint true: a running show survives coordinator loss and broker loss. A node that fetched assets at showtime would fail exactly when the coordinator is down, which is the case the whole architecture is shaped to survive. **No playback path may reach the store**, and a reviewer should treat any code that does as a defect regardless of how well it performs in testing.

### 6. Integrity is a content hash, not a signature

Assets are verified by content hash. They are **not** signed, and this is a deliberate contrast with [ADR-025](ADR-025-agent-fallback-cache-is-signed.md), which requires the agent's fallback cache to be signed with a pinned key.

The distinction is what the artifact does. ADR-025's cache is **configuration the agent acts on**, so its origin matters and a checksum only proves the file is intact rather than ours. An FSEQ or an audio file is **data that gets rendered or played**. A wrong one is visible immediately and cannot cause the node to behave differently. So integrity is the property worth having, and a hash provides it.

Recorded explicitly because ADR-025 set a signing precedent and someone will reasonably ask why it does not apply here. If assets ever carry executable content, this decision is void and must be revisited.

### 7. Assets sync ahead of showtime, never at showtime

Synchronisation runs on upload and on a timer. It does not run in response to a show starting.

A node's readiness is not "the file exists" but **"the variant assigned to this node for this sequence exists locally and matches the expected artifact"**, checked against the manifest. That is the desired-versus-observed split from [ADR-003](ADR-003-desired-and-observed-state.md) applied to files, and a node missing an asset is a readiness fault reported before a show rather than discovered during one.

### 8. Many ingestion paths, one internal model

Assets may arrive by manual upload through the UI, from xLights and FPP Connect, from an existing FPP host, or from future integrations. **Manual upload is a permanent supported feature**, not a development stopgap: it is the non-xLights path, the recovery path, and the testing path, and for day-0 it is the only path that exists.

Once an asset is in the store, **its origin is provenance, not a runtime dependency.** Nothing may require the original source to be reachable in order to use an asset that has already been ingested.

Where FPP Connect delivers directly to a node, that node registers the artifact into the store **without losing its target context**, because that context is the identity from decision 1. If the store is unreachable at that moment, the node holds the artifact and retries; the file is not lost and the operator is not blocked.

Reading media from an existing FPP host is a **read** and stays within the standing prohibition on writing to the deployed fleet.

## Consequences

- **The coordinator moves bytes now**, which it never did before. Upload size limits, timeouts, and disk-exhaustion behaviour are real concerns that did not previously apply to it, and ARCHITECTURE §11 already lists disk exhaustion as a failure mode to handle.
- **Manual asset upload requires the operator to state the target node**, because identity requires it. This is the visible cost of decision 1 and the UI must make it unavoidable rather than defaultable.
- Deduplication by content hash is possible where the same audio goes to several nodes, and is an optimisation rather than a requirement.
- Assets accumulate across seasons and eventually need a retention policy. Not day-0.
- **Whole-show FSEQ distribution is now excluded by design.** If a future deployment has small enough channel counts to prefer it, that is a new decision rather than a configuration option.

## Alternatives considered

**One whole-show FSEQ distributed to every node, each extracting its own channels.** Genuinely simpler: no variants, no target-aware identity, and decision 1 would not be needed at all. Rejected on the operator's numbers, since gigabytes per song to every render node is not a reasonable distribution problem to accept in exchange for schema simplicity, and xLights already does the splitting.

**Filename plus node as the key.** Rejected: it makes two artifacts with the same name and target indistinguishable across versions, so a re-render cannot be detected.

**Signing assets like ADR-025's cache.** Rejected per decision 6.

**A separate distribution mechanism per node type.** Rejected per decision 3.

**Blobs in SQLite.** Rejected per decision 4.

## Related research

[Configuration model](../research/RES-008-configuration-model.md) · [xLights and FPP Connect](../research/RES-003-xlights-fpp-connect-compatibility.md) · [Audio node](../research/RES-007-audio-node-architecture.md) · [Telemetry storage](../research/RES-013-telemetry-storage-and-alerting.md)

## Supersession

Supersedes nothing. It extends [ADR-009](ADR-009-sqlite-configuration-storage.md) into a class of data that record did not contemplate, and its decision 4 is a clarification of ADR-009 rather than a change to it: SQLite stays authoritative for state, and large binary artifacts were never state.
