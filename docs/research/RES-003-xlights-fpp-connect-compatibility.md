# RES-003: xLights and FPP Connect Compatibility

[Architecture](../architecture/ARCHITECTURE.md#3-system-architecture) · [Tracker](README.md) · [MultiSync research](RES-002-fpp-multisync-compatibility.md)

Status: planned (renderer delivery decided; compat surface **L1, source-verified 2026-08-13**; integration L0) · Risk: high · Verification: L1 for the FPP Connect compatibility surface in section 9; L0 for everything requiring a live xLights

## Decision to make

Validate the least disruptive workflow for exporting, naming, and delivering media-node assets while preserving normal xLights and FPP Connect use. The renderer boundary is settled: FPP Connect uploads the sequence and FSEQ to each renderer node ahead of playback, and the renderer extracts its assigned virtual-matrix channels locally.

## Questions

- Which supported FPP Connect mechanism can deliver the sequence and FSEQ to a renderer node without an xLights fork?
- Which additional media, model, or manifest artifacts does the renderer workflow require?
- Can one virtual matrix be produced per logical surface, including both one-surface-per-projector and combined-surface layouts?
- Are headless or batch exports available and deterministic?
- Which metadata is needed to relate exported media to sequence duration, frame rate, surface, and checksum?
- Where can validation and transcoding occur without changing xLights?

## Acceptance criteria

- A representative show can be prepared through a documented repeatable workflow.
- Existing FPP targets continue to use normal FPP Connect behavior.
- FPP Connect delivers the sequence and FSEQ to the renderer node before playback; playback requires no live matrix stream or coordinator asset transfer.
- Media-node artifacts have deterministic names, dimensions, duration, frame rate, and hashes.
- One content update can be validated and delivered without manual file hunting.

## Test method

Record xLights and FPP versions and use representative logical-surface layouts, including one surface per projector and a combined canvas. Exercise the supported FPP Connect delivery path to a renderer node, inspect the resulting local files, and prototype an external manifest/validation helper before considering upstream changes.

## Evidence and findings

The renderer delivery boundary was settled by the project owner on 2026-08-13. It is workflow intent rather than observed FPP Connect behavior, so this record remains L0 until the delivery path is exercised and recorded.

These decisions are recorded as durable constraints in [ADR-026](../decisions/ADR-026-renderer-surface-model-and-reference-transport.md), which also fixes the rule that the reference profile is described as intended rather than supported until this record's bench runs.

## 9. The FPP Connect compatibility surface (L1, source-verified 2026-08-13)

Read directly from `smeighan/xLights` at `97f78a15b` (master), cross-checked against release tag `2026.15` at `d16c2e434`, and from `FalconChristmas/fpp` at `01e0d5cc3` (master). Every claim below cites a file and function. No forum or blog material was used.

**The headline: the required surface is four HTTP endpoints and a UDP ping responder, and ShowMesh already owns most of the hard part.**

### 9.1 The eligibility gate, and why it forces a UDP responder

**Fact.** A device appears in the FPP Connect list only if it passes `::supportedForFPPConnect(DiscoveredData*, OutputManager*)` in `src-core/controllers/FPP.cpp`:

```
if (res->typeId == 0) { return false; }
if (res->typeId < 0x80) { return httpConnected == true; }
```

Both a non-zero `typeId` below `0x80` and a successful HTTP contact are required.

**Fact, and this is the finding that shapes the whole design.** `ProcessFPPSysinfo` in the same file contains a typo that is present in **both master and the shipping 2026.15 release**: it tests for the JSON key `"typId"` and reads the value from `"typeId"`. A search of the entire FPP repository finds **zero occurrences of `typId`**, and FPP's own documented example spells the key correctly. So that branch is dead against every real server, and **`typeId` can never be set from `/api/system/info`**.

The consequence is concrete: `/api/system/info` alone, however perfect, **cannot make a device eligible.** `typeId` has to arrive either from a UDP ping response or from a `multiSyncSystems` self-entry, both of which parse it correctly.

**This is exactly the kind of thing that must not be designed around.** It is an accidental constant that a future point release will fix without notice, which would silently change which path is authoritative. The response is to implement the UDP responder regardless and treat the `multiSyncSystems` path as an independent second route, rather than picking whichever is cheaper today.

### 9.2 ShowMesh already has the hard half

The UDP responder is `pkg/multisync`'s discover-ping responder, built in Step 1 and **never yet answered by a real FPP instance**, which BUILD-PLAN records as a known follow-up. This is its second consumer and its first real requirement.

Two things to verify rather than assume when Track B or Track E gets there:

- **The response must use the v3 ping layout.** xLights sends a ping with a v2 version byte but always parses responses at v3 offsets, and real FPP always responds v3. The layout was cross-verified in both directions this session: xLights' parser (`ProcessFPPPingPacket`) and FPP's own builder (`MultiSync::CreatePingPacket`) agree field for field. Whether `pkg/multisync` currently emits v3 is unchecked.
- **[ADR-013](../decisions/ADR-013-no-fpp-control-port-sharing.md) does not obstruct this**, because a render node runs no `fppd`, so it binds 32320 normally with no port sharing. Already recorded in [Track B](../build/TRACK-B-nodes-and-projection.md).

**Manual IP entry does not avoid the UDP requirement.** `FPPConnectDialog::OnAddFPPButtonClick` passes `broadcastPing=false`, which suppresses only the broadcast send; it still issues a unicast ping to that address plus both HTTP calls.

### 9.3 The four endpoints

| Endpoint | Why required | Notes |
|---|---|---|
| `GET /api/system/info` | `uuid` is a hard gate: absent or empty aborts all further parsing. Version must satisfy the second gate. | Must carry `uuid`, `Version`, `majorVersion`, `minorVersion`, `Mode`, `channelRanges`. Its `typeId` is ignored per 9.1. |
| `GET /api/fppd/multiSyncSystems` | Avoids a `GET /` HTML-sniffing fallback aimed at third-party pixel controllers, and supplies `typeId` through a parser without the bug. | Shape `{"systems":[{...}]}`, cross-verified against FPP's own `MultiSyncSystem::toJSON()`. |
| `PATCH /api/file/{dir}` | The actual transport for both FSEQ and media. | See 9.4. |
| UDP ping response | See 9.1. | Not HTTP, and not optional. |

**A second version gate** applies once the object exists: `FPP::supportedForFPPConnect() const` requires version 7.1 or later, or 6.3.3 or later with a `capeInfo.verifiedKeyId`. Advertising explicit `majorVersion` and `minorVersion` integers at 7.1 or above avoids needing to emulate `/api/cape` at all.

**Fact worth carrying into implementation:** the advertised version changes which code path xLights runs for every later step, not just eligibility. Different FSEQ features gate at 7.0, 9.3, and 10.0. So the version is a deliberate compatibility choice and should match a real, recent, well-tested FPP release rather than being set arbitrarily high.

### 9.4 Upload is chunked PATCH, and the documented protocol is wrong

**Fact.** xLights uploads via repeated `PATCH /api/file/{dir}` in 16 MiB chunks, with `Content-Type: application/offset+octet-stream` and headers `Upload-Offset`, `Upload-Length`, `Upload-Name`, plus `X-Requested-With: FPPConnect`.

**Fact, and a trap.** FPP's own OpenAPI document describes an initiating `POST` that returns an upload session ID. **xLights never sends it**, and FPP's real handler treats `POST` as a no-op whose return value is never checked. The `PATCH` handler needs no prior session: offset 0 clears stale fragments, and the file is assembled when accumulated size equals `Upload-Length`, with **no separate commit or move call**. A server that insisted on the documented `POST` would break against the real client.

Directories: `sequences` for FSEQ, `effects` for `.eseq`, `music` for audio, `videos` for known video extensions.

`GET /api/sequence/{name}/meta` and `GET /api/media/{name}/meta` are **pure upload-skip optimisations**. A 404 means "always re-upload" and breaks nothing.

### 9.5 Per-node channel rendering is pulled, automatic, and on by default

**This is the mechanism ADR-028 depends on, and it works in ShowMesh's favour.**

**Fact.** The channel set is **pulled from the target**, not pushed to it. It is the `channelRanges` string obtained during discovery, in the syntax `start-len,start-len,...`. In `PrepareUploadSequence`, when a sparse FSEQ type is selected, that string is parsed and channels outside it are **not written into the rendered file at all**.

**Fact.** Sparse rendering is the **default, not an operator opt-in**: the per-target FSEQ type defaults to "V2 Sparse/zstd" for any target whose mode is not `"master"`. So a device advertising `channelRanges` and a mode of `"remote"` gets reduced per-node rendering out of the box.

**Fact.** An empty `channelRanges` produces a **full, non-sparse** FSEQ. That is the correct-but-undesired fallback rather than a failure, and it is exactly the gigabytes-per-song case [ADR-028](../decisions/ADR-028-show-asset-store-and-identity.md) exists to avoid. **A surface whose channel range is unset would silently produce whole-show files**, which is a defect worth catching at configuration time rather than at upload time.

**A consequence for [ADR-027](../decisions/ADR-027-show-and-surface-model.md) decision 4.** That decision treats manual channel-range configuration as the path available *until* xLights model metadata can be imported. The direction of travel here is the reverse: ShowMesh advertises the ranges it wants and xLights honours them, so **manual range configuration is the natural input to this flow rather than a stand-in for it.** Model import is a genuinely separate, opt-in path (9.6) whose value is saving the operator from transcribing a start channel out of xLights by hand. Decision 4's conclusion stands; its stated reasoning needs this correction.

### 9.6 Model and playlist upload are opt-in and default-off

`POST /api/models`, `POST /api/playlist/{name}`, and the universe-output endpoints all exist, and all three are per-target dropdowns **defaulting to "None"** in the FPP Connect dialog. None is required for "select target, upload FSEQ". They serve FPP-as-pixel-controller deployments, not a render node that receives an FSEQ and plays it under ShowMesh's control.

### 9.7 Smallest viable surface, in build order

1. **UDP ping responder** in the v3 layout, with a `typeId` in `1..0x7F` and a mode other than `"master"`. Without this nothing else matters. Risk: choosing a `typeId` that collides with a real hardware class.
2. **`GET /api/system/info`** with `uuid`, version at 7.1 or above, mode, and `channelRanges`.
3. **`GET /api/fppd/multiSyncSystems`** with a correct self-entry.
4. **`PATCH /api/file/{dir}`** chunked upload.

### 9.8 What this research could not determine

Recorded because an honest unknown is worth more than a plausible guess.

- Whether an HTTP-only device with no UDP responder is actually usable. That is a multi-hop inference through three functions, **not an observed result**, and needs a bench test against a real xLights before anyone relies on it.
- The JSON schema `CreateModelMemoryMap()` sends to `POST /api/models`. The call site was found; the body builder was not traced. Needed only if ShowMesh later wants model round-tripping.
- The implementation of the `GET /` HTML-sniffing fallback, so its behaviour against an unrecognised device is unknown. Avoided by implementing endpoint 3 above.
- Whether FPP's server-side upload handler differs across major versions 8, 9 and 10. Only master was read.
- Authentication behaviour. xLights sends HTTP Basic or Digest credentials when configured, which is unexamined here and will matter once a ShowMesh node needs to refuse unauthenticated writes in the spirit of [ADR-024](../decisions/ADR-024-identity-authorization-and-audit.md).

### 9.9 What still needs a bench

This section is **L1 and cannot go higher without a running xLights.** Everything above is read from source. The acceptance criteria in section 3 stay open, and a first bench should confirm that a stub answering the four items in 9.7 actually appears in the FPP Connect list and receives a correctly sparse FSEQ.

## Decision, fallback, and revalidation

Use FPP Connect to upload the sequence and FSEQ to renderer nodes ahead of playback. The fallback is documented manual export plus direct node upload. Any proposed xLights modification must demonstrate that an external helper cannot provide the same result. Revalidate after material xLights or FPP Connect changes.
