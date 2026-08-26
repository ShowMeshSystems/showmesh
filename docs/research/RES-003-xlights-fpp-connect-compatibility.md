# RES-003: xLights and FPP Connect Compatibility

[Architecture](../architecture/ARCHITECTURE.md#3-system-architecture) · [Tracker](README.md) · [MultiSync research](RES-002-fpp-multisync-compatibility.md)

Status: planned (renderer delivery decided; compat surface **L1, source-verified 2026-08-13**, **amended and re-pinned 2026-08-25 in section 10**; integration L0) · Risk: high · Verification: L1 for the FPP Connect compatibility surface in sections 9 and 10; L0 for everything requiring a live xLights

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

**Section 9 was read at older pins and three of its statements are superseded by section 10, dated 2026-08-25: the `channelRanges` syntax in 9.5, the version framing in 9.3, and the "not required" verdict on the playlist endpoints in 9.6. Each is marked in place below. Section 10 is pinned to different commits of both projects.**

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

**Superseded in part by section 10.5: the chunked `PATCH` upload path is selected by type, not by version. The version gates below are otherwise confirmed, and section 10.5 records which one does what.**

**A second version gate** applies once the object exists: `FPP::supportedForFPPConnect() const` requires version 7.1 or later, or 6.3.3 or later with a `capeInfo.verifiedKeyId`. Advertising explicit `majorVersion` and `minorVersion` integers at 7.1 or above avoids needing to emulate `/api/cape` at all.

**Fact worth carrying into implementation:** the advertised version changes which code path xLights runs for every later step, not just eligibility. Different FSEQ features gate at 7.0, 9.3, and 10.0. So the version is a deliberate compatibility choice and should match a real, recent, well-tested FPP release rather than being set arbitrarily high.

### 9.4 Upload is chunked PATCH, and the documented protocol is wrong

**Fact.** xLights uploads via repeated `PATCH /api/file/{dir}` in 16 MiB chunks, with `Content-Type: application/offset+octet-stream` and headers `Upload-Offset`, `Upload-Length`, `Upload-Name`, plus `X-Requested-With: FPPConnect`.

**Fact, and a trap.** FPP's own OpenAPI document describes an initiating `POST` that returns an upload session ID. **xLights never sends it**, and FPP's real handler treats `POST` as a no-op whose return value is never checked. The `PATCH` handler needs no prior session: offset 0 clears stale fragments, and the file is assembled when accumulated size equals `Upload-Length`, with **no separate commit or move call**. A server that insisted on the documented `POST` would break against the real client.

Directories: `sequences` for FSEQ, `effects` for `.eseq`, `music` for audio, `videos` for known video extensions.

`GET /api/sequence/{name}/meta` and `GET /api/media/{name}/meta` are **pure upload-skip optimisations**. A 404 means "always re-upload" and breaks nothing.

### 9.5 Per-node channel rendering is pulled, automatic, and on by default

**This is the mechanism ADR-028 depends on, and it works in ShowMesh's favour.**

**Fact.** The channel set is **pulled from the target**, not pushed to it. It is the `channelRanges` string obtained during discovery. **Superseded by section 10.1: the syntax stated here as `start-len,start-len,...` is wrong. It is `start-end`, 0-based, with an inclusive end.** In `PrepareUploadSequence`, when a sparse FSEQ type is selected, that string is parsed and channels outside it are **not written into the rendered file at all**.

**Fact.** Sparse rendering is the **default, not an operator opt-in**: the per-target FSEQ type defaults to "V2 Sparse/zstd" for any target whose mode is not `"master"`. So a device advertising `channelRanges` and a mode of `"remote"` gets reduced per-node rendering out of the box.

**Fact.** An empty `channelRanges` produces a **full, non-sparse** FSEQ. That is the correct-but-undesired fallback rather than a failure, and it is exactly the gigabytes-per-song case [ADR-028](../decisions/ADR-028-show-asset-store-and-identity.md) exists to avoid. **A surface whose channel range is unset would silently produce whole-show files**, which is a defect worth catching at configuration time rather than at upload time.

**A consequence for [ADR-027](../decisions/ADR-027-show-and-surface-model.md) decision 4.** That decision treats manual channel-range configuration as the path available *until* xLights model metadata can be imported. The direction of travel here is the reverse: ShowMesh advertises the ranges it wants and xLights honours them, so **manual range configuration is the natural input to this flow rather than a stand-in for it.** Model import is a genuinely separate, opt-in path (9.6) whose value is saving the operator from transcribing a start channel out of xLights by hand. Decision 4's conclusion stands; its stated reasoning needs this correction.

### 9.6 Model and playlist upload are opt-in and default-off

**Narrowed by section 10.6.** The playlist endpoints remain optional in xLights, but ShowMesh serves them deliberately: the playlist dropdown is how an operator names the ShowMesh show an upload belongs to. The models and universe-output endpoints stay deferred as stated here.

`POST /api/models`, `POST /api/playlist/{name}`, and the universe-output endpoints all exist, and all three are per-target dropdowns **defaulting to "None"** in the FPP Connect dialog. None is required for "select target, upload FSEQ". They serve FPP-as-pixel-controller deployments, not a render node that receives an FSEQ and plays it under ShowMesh's control.

### 9.7 Smallest viable surface, in build order

1. **UDP ping responder** in the v3 layout, with a `typeId` in `1..0x7F` and a mode other than `"master"`. Without this nothing else matters. Risk: choosing a `typeId` that collides with a real hardware class.
2. **`GET /api/system/info`** with `uuid`, version at 7.1 or above, mode, and `channelRanges`.
3. **`GET /api/fppd/multiSyncSystems`** with a correct self-entry.
4. **`PATCH /api/file/{dir}`** chunked upload.

### 9.7a A second surface this pass did not cover: the Controllers tab

**Recorded 2026-08-13, raised by the operator after reading section 9.5, and it narrows that section's conclusion.**

This pass was scoped to the **FPP Connect dialog**, so its finding that channel ranges are *pulled from the target* is correct for sequence rendering and **does not generalise**. xLights has a separate **Controllers tab** where a controller is defined by name, address, vendor and protocol, and where an **"Upload Outputs"** action pushes configuration to it. The operator reports that a controller can be defined with no hardware present but that upload requires the device to be online, which implies a reachability or identity check first.

This matters because it appears to be **the only path by which xLights pushes model definitions to a device**, and therefore the only way ShowMesh could learn model names and layouts rather than requiring an operator to transcribe a start channel by hand. That is the capability [ADR-027](../decisions/ADR-027-show-and-surface-model.md) decision 4 calls the preferred mapping.

This pass brushed against the machinery without tracing it: `POST /api/models` with a body from `CreateModelMemoryMap()`, and `GET`/`POST /api/channel/output/universeOutputs` from `FPP::UploadUDPOut()`, both surfacing in the FPP Connect dialog as dropdowns defaulting to "None". Whether the Controllers tab drives the same code is **unverified**, and the `/api/models` body schema is listed below as explicitly undetermined.

**That pass has now reported: see section 9.7b.** Its answer is that the Controllers tab does *not* push models, so the surface described in this section is not the one that matters. Day-0 was never dependent on it.

### 9.7b The Controllers tab, traced (L1, 2026-08-13, second pass)

Read at xLights `97f78a15b` and FPP `01e0d5cc3`, both shallow-cloned and read directly.

**The button exists and does something other than what it was thought to do.** It is labelled **"Upload Output"**, singular, in `ControllerListPanel.cpp`, and it reaches `FPP::SetOutputs()`, which calls `UploadPanelOutputs`, `UploadVirtualMatrixOutputs`, `UploadPixelOutputs`, `UploadSerialOutputs`, `UploadPWMOutputs`, then restarts the target. **None of those calls `CreateModelMemoryMap()` or `UploadModels()`.** A grep of the whole file finds those two functions referenced only at their own definitions and from three call sites: the FPP Connect dialog, an automation action, and the iPad bridge. The Controllers tab is nowhere among them.

So **"Upload Output" pushes per-port pixel, panel, serial and PWM wiring configuration, not model definitions.** Model definitions reach a device only through FPP Connect's own Models dropdown, which section 9.6 already found and which defaults to "None".

**This settles the question 9.7a was opened to answer, and the answer is that this surface is not the one we want.** Every payload it produces describes how a device drives physical pixel, serial or DMX outputs, which a render node does not do.

**xLights already ships the precedent, which is the most useful single finding here.** `resources/controllers/fpp.xcontroller` defines variants named **"FPP Player Only"** and **"FPP Video Playing Remote Only"**, each with `MaxPixelPort=0`, `MaxSerialPort=0`, a `<PlayerOnly/>` tag, and **no `<SupportsUpload/>` tag at all**. `ControllerSupportsOutputUpload()` checks exactly that, so a controller declared as one of these has "Upload Output" permanently disabled regardless of reachability. xLights has already modelled "a device that plays media and drives no pixel outputs" and deliberately excluded it from this surface. That is the shape of a ShowMesh render node, described by xLights itself.

#### A fifth endpoint the first pass did not find, and it is an identity claim

**Read section 10.3 alongside this.** At the 2026-08-25 pin the FSEQ upload path never reaches `parseConfig()`, so the identity gate described here is confined to the Controllers tab and the model-push path and does not touch the feature Track E is building. The bench still owes the confirmation.

**Fact.** The Controllers-tab gate is `FPP::AuthenticateAndUpdateVersions()`, which is narrower than FPP Connect discovery: `GET /config.php`, then `GET /api/system/info`, with no UDP, no multicast, no mDNS and no `multiSyncSystems`. `parseConfig()` sets `fppType = FPP_TYPE::FPP` **only if the parsed `settings["Title"]` contains the substring `"Falcon Player"`.**

This explains the operator's observation exactly: a controller is definable offline because the property grid never touches the network, and "Upload Output" fails until those two GETs succeed against that IP at click time.

**The unreconciled part, flagged rather than smoothed over.** This second pass reports that the **model push** also passes through `AuthenticateAndUpdateVersions()`, and therefore also requires `/config.php` to advertise `"Falcon Player"`. The first pass, which traced the FSEQ and media path, never encountered `/config.php` at all and derived a different gate (`typeId` plus `httpConnected`). Both may be true of different operations, since they are different functions guarding different features. **They have not been reconciled against a single call graph, and until they are, section 9.7's four-item list should be read as covering FSEQ and media upload only.** Anyone implementing the model-receive path should assume `/config.php` is also required and verify it on the bench.

**That requirement is a decision, and the owner made it on 2026-08-13. It is a hard no for any release.**

The line he drew is worth stating precisely, because it is not squeamishness and it generalises past this endpoint. **Implementing someone else's protocol in order to talk to their devices is ordinary interoperability and is what this project already does everywhere.** Serving a document that claims *to be* their product is different in kind: it is asserting an identity, not speaking a language.

So:

- **Development and bench work may fake it**, to prove the concept and to exercise the rest of the path. That is a temporary scaffold and must be recognisable as one in the code.
- **No release may ship it.** Not behind a flag, not as a default, not as a documented workaround.

**The preferred route is to be listed legitimately**, and section 9.7b turned up the concrete mechanism: xLights ships one `.xcontroller` XML file per vendor in `resources/controllers/`, twenty-five of them at the commit read, and `ControllerCaps::LoadControllers()` knows only what is in those files. Being a real entry in the manufacturer and model list is therefore a matter of getting a vendor definition into xLights, most likely upstream, rather than of imitating an existing one.

**This needs its own research pass and did not get one here.** Open questions, none of them answered: what xLights' own rules and process are for adding a vendor, whether a `.xcontroller` entry alone is sufficient or whether a `ConfigDriver` must also be recognised in `BaseController::CreateBaseController()` (which would mean a C++ change upstream rather than an XML addition), what the maintainers expect of a new vendor, and whether a device can be listed at all when it drives no pixel outputs. The "FPP Player Only" variant suggests the last one is possible, since it is a shipped entry with zero pixel ports.

**Realistic sequencing, per the owner:** the official route is very unlikely to be in place for day-0 on his personal display. So the practical position is a faked identity confined to development, an unfaked release, and the listing question researched properly before any release is contemplated. **This paragraph is the flag: anyone reaching this point should stop and open that research rather than shipping the string.**

#### The model schema, now traced

**Fact.** `POST /api/models` takes `{"models": [ ... ]}` where each entry carries `Name` (spaces replaced with underscores), `ChannelCount`, `StartChannel` (1-based), `ChannelCountPerNode`, `xLights: true`, `Orientation` (`horizontal`, `vertical` or `custom`), `StringCount`, `StrandsPerString`, `StartCorner` (two characters, one of `TL`/`TR`/`BL`/`BR`), and `Type` (always `"Channel"` from this function). Custom-orientation models additionally carry `compressedData` on FPP 10 and later, or `data` before it.

Three traps in that exchange, all facts:

- **`GET /api/models` returns a bare array while `POST` expects `{"models": [...]}`.** A client that reads back its own POST body will be confused.
- **xLights merges client-side before posting.** It GETs the existing models and folds forward any whose `xLights` flag is not `true`, so hand-authored FPP models survive a re-upload. If that GET fails, the merge is silently skipped and the POST contains only xLights' models. That is xLights' robustness problem, noted because a device that answers the GET badly can cause silent data loss on the device itself.
- **`FPP::UploadModels()` discards the HTTP status and unconditionally returns "not cancelled".** **xLights never checks whether the model POST succeeded**, so a target that accepts the connection and returns anything at all, including a non-2xx, looks successful to the operator. Any ShowMesh implementation must therefore be correct without expecting xLights to report a failure, and should surface its own evidence that a push was received and understood.

**Fact, server side.** FPP writes the request body **verbatim with `O_TRUNC`** to `config/model-overlays.json`, with no schema validation at write time, then sets a restart flag over loopback. A malformed body is accepted and only surfaces on the next `fppd` restart.

#### What this pass could not determine

- Whether the version gate at `supportedForFPPConnect()` blocks the Models checkbox specifically, or only some other FPP Connect capability. The function was read; not every caller was.
- Whether the other `Upload*` functions check their POST status the way `UploadModels()` demonstrably does not.
- Whether absent keys in a model entry degrade cleanly or throw on FPP's side. The code path was read but not run.
- Which FPP version introduced the POST-wrapper versus GET-bare-array asymmetry.

### 9.8 What this research could not determine

Recorded because an honest unknown is worth more than a plausible guess.

- Whether an HTTP-only device with no UDP responder is actually usable. That is a multi-hop inference through three functions, **not an observed result**, and needs a bench test against a real xLights before anyone relies on it.
- The JSON schema `CreateModelMemoryMap()` sends to `POST /api/models`. The call site was found; the body builder was not traced. Needed only if ShowMesh later wants model round-tripping.
- The implementation of the `GET /` HTML-sniffing fallback, so its behaviour against an unrecognised device is unknown. Avoided by implementing endpoint 3 above.
- Whether FPP's server-side upload handler differs across major versions 8, 9 and 10. Only master was read.
- Authentication behaviour. xLights sends HTTP Basic or Digest credentials when configured, which is unexamined here and will matter once a ShowMesh node needs to refuse unauthenticated writes in the spirit of [ADR-024](../decisions/ADR-024-identity-authorization-and-audit.md).

### 9.9 What still needs a bench

This section is **L1 and cannot go higher without a running xLights.** Everything above is read from source. The acceptance criteria in section 3 stay open, and a first bench should confirm that a stub answering the four items in 9.7 actually appears in the FPP Connect list and receives a correctly sparse FSEQ.

## 10. Amendment, 2026-08-25: the compatibility surface re-read at new pins

**Status of this section: L1, source-verified 2026-08-25. Nothing below was exercised against a running xLights or a running FPP.** It is read from two checkouts at fixed commits, and the bench described in section 9.9 is still what moves any of it to L2.

**New pins, and they differ from section 9's.** Section 9 was read at `smeighan/xLights` `97f78a15b` and `FalconChristmas/fpp` `01e0d5cc3`. This amendment was read at:

- xLights, `xLightsSequencer/xLights` at [`ae379c0408ab39f3de265aea13c326bf48ab84b7`](https://github.com/xLightsSequencer/xLights/tree/ae379c0408ab39f3de265aea13c326bf48ab84b7)
- FPP, `FalconChristmas/fpp` at [`139d7e6ba8c70d5a5f835a9664ddbab36853a945`](https://github.com/FalconChristmas/fpp/tree/139d7e6ba8c70d5a5f835a9664ddbab36853a945)

Neither pin is confirmed to be a shipping release tag, and neither is confirmed to be the build the owner runs. Where this section contradicts section 9, **this section governs and the earlier statement is marked superseded in place below rather than deleted**, per the research tracker's convention that a superseded finding stays readable.

### 10.1 `channelRanges` is `start-end`, 0-based, not `start-len`

**Superseded: section 9.5's "in the syntax `start-len,start-len,...`".** That wording is wrong and a lane that copied it would have shipped every node an off-by-one window of the wrong length.

**Fact.** `PrepareUploadSequence` splits each comma-separated element on `-` and computes the length as `end - start + 1`, so the second number is an **inclusive end**:

https://github.com/xLightsSequencer/xLights/blob/ae379c0408ab39f3de265aea13c326bf48ab84b7/src-core/controllers/FPP.cpp#L1244-L1253

**Fact.** The parsed pairs go straight into the output file's sparse range table, and FSEQ sparse ranges are 0-based ([RES-017](RES-017-fseq-format.md) section 6.1).

https://github.com/xLightsSequencer/xLights/blob/ae379c0408ab39f3de265aea13c326bf48ab84b7/src-core/controllers/FPP.cpp#L1356-L1357

**Fact, the corroborating write site.** Where xLights synthesises the string itself from a controller it converts down from its own 1-based start channel and emits an inclusive end: `sc = GetStartChannel() - 1` then `std::to_string(sc) + "-" + std::to_string(sc + channels - 1)`.

https://github.com/xLightsSequencer/xLights/blob/ae379c0408ab39f3de265aea13c326bf48ab84b7/src-core/controllers/FPP.cpp#L4245-L4247

**Consequence for ShowMesh.** `show.surface`'s `channelRange` is 1-based at the operator surface (`startChannel >= 1`, `channelCount >= 1`). The advertised string for one surface is therefore `start-1` to `start+count-2`, that is `fmt.Sprintf("%d-%d", startChannel-1, startChannel+channelCount-2)`. The base conversion is the whole point of the formatter; copying the surface numbers through unchanged is the defect.

**Fact.** The ping parser discards the literal `"0-0"`, so a zero-length advertisement is silently the empty case, which is section 9.5's full non-sparse file.

https://github.com/xLightsSequencer/xLights/blob/ae379c0408ab39f3de265aea13c326bf48ab84b7/src-core/controllers/FPP.cpp#L4193-L4196

### 10.2 A `typeId` at or above `0x80` is rejected, and `0x7F` is the chosen provisional value

**Fact.** Eligibility rejects `typeId == 0`, accepts `typeId < 0x80` when `httpConnected` is true, and otherwise accepts only four explicitly enumerated vendor bands, falling through to `return false` for everything else.

https://github.com/xLightsSequencer/xLights/blob/ae379c0408ab39f3de265aea13c326bf48ab84b7/src-core/controllers/FPP.cpp#L4226-L4274

**Fact.** `TypeIDtoControllerType` maps `0 < typeId < 0x80` to `FPP_TYPE::FPP`, `0x88` to `0x9F` to Falcon V4/V5, `0xC2` and `0xC3` to ESPixelStick, `0xA0` to `0xAF` to Genius, and `0xD0` to `0xDF` to PowerDMX. Nothing else is mapped.

https://github.com/xLightsSequencer/xLights/blob/ae379c0408ab39f3de265aea13c326bf48ab84b7/src-core/controllers/FPP.cpp#L4418-L4429

**Fact, and it matters to this repository today.** The agent's live discover response advertises `SystemTypeShowMesh`, which is `SystemTypeOther = 0xC0`. `0xC0` falls through every branch and returns false, so a ShowMesh render node cannot appear in FPP Connect as the code stands.

**Fact, FPP's own reservation.** `MultiSyncSystemType` in FPP's `src/MultiSync.h` carries exactly one documented rule, one line above `kSysTypeFalconController`: `// Values under 0x80 are "FPP based" and run full FPP`.

https://github.com/FalconChristmas/fpp/blob/139d7e6ba8c70d5a5f835a9664ddbab36853a945/src/MultiSync.h#L92-L93

https://github.com/FalconChristmas/fpp/blob/139d7e6ba8c70d5a5f835a9664ddbab36853a945/src/MultiSync.h#L66-L126

That boundary is load bearing inside FPP as well: `supportsUnicast` is computed as `type < kSysTypeFalconController && fppMode == REMOTE_MODE`.

https://github.com/FalconChristmas/fpp/blob/139d7e6ba8c70d5a5f835a9664ddbab36853a945/src/MultiSync.cpp#L205-L222

**The bands, as allocated at that pin.** `0x01` to `0x7F` is FPP's own platform enumeration, with family bases at `0x01` (Raspberry Pi), `0x41` (BeagleBone), `0x60` (Armbian) and `0x70` (macOS), each growing upward; free values are `0x10` to `0x40`, `0x48` to `0x5F`, `0x61` to `0x6F` and `0x71` to `0x7F`. `0x80` to `0x8F` is Falcon. `0xA0` to `0xAF` is Experience Lights and Genius. `0xC0` to `0xCF` is other systems, one value per third-party product (`0xC1` xSchedule, `0xC2` and `0xC3` ESPixelStick, `0xC4` Baldrick). `0xD0` to `0xDF` is claimed by xLights for Illuminous and PowerDMX and is **absent from FPP's enum at this pin**, which shows that allocations land in xLights independently of, and sometimes ahead of, FPP's header. `0xF0` to `0xFF` is non-MultiSync-capable and assorted pixel controllers.

**Chosen value: `0x7F`, provisional.** The reasoning, in order:

1. It must be below `0x80`, because every other accepted band maps to a non-FPP `FPP_TYPE` that drives a different dialog branch, a different FSEQ default, no playlist widget, and for several of them the legacy non-chunked upload path.
2. Inside `0x01` to `0x7F` there is no vendor sub-range to avoid, because the whole space is FPP's platform enum. There is no value that is both eligible and honestly unclaimed, so the honest choice is the value least likely to be allocated next.
3. Families grow upward from their bases, so collision risk falls as the value rises. `0x7F` is the last value before the vendor boundary and the furthest from every base. Second choice `0x7E`.
4. **Never `0x01`.** `MultiSync::ModelStringToType()` falls back to `kSysTypeFPP` for unrecognised hardware, so a real FPP on a new board reports `0x01` today and a ShowMesh node using it would be indistinguishable from one.

https://github.com/FalconChristmas/fpp/blob/139d7e6ba8c70d5a5f835a9664ddbab36853a945/src/MultiSync.cpp#L539-L605

5. Not `0xC5` or any other free value in the `0xC0` band, even though that is the correct band by product type: xLights rejects every `0xCx` except `0xC2` and `0xC3`, so registering there would make ShowMesh visible in FPP's multisync list and still invisible in FPP Connect.

On FPP's own side an unrecognised value is not an error; `GetTypeString()` ends in `default: return "Unknown System Type";`, which is accurate and harmless.

https://github.com/FalconChristmas/fpp/blob/139d7e6ba8c70d5a5f835a9664ddbab36853a945/src/MultiSync.cpp#L775-L921

**The honest residual.** Any value in `1..0x7F` makes xLights classify the node as `FPP_TYPE::FPP`. That is a device-class claim inside another project's protocol enum. It is not the forbidden thing, since no served content says "Falcon Player", but it is not nothing, which is why the owner ruled on it rather than a builder choosing (section 10.7). Proper allocation means pull requests to **both** FPP's `src/MultiSync.h` and xLights' `TypeIDtoControllerType` and `supportedForFPPConnect`, and the xLights one is the one that grants eligibility. That is a separate follow-up and does not gate the build.

### 10.3 The FSEQ upload path never reads `/config.php`, so this feature needs no identity string

**This answers the release-posture question section 9.7b left open, at L1, in the affirmative.**

**Fact.** `FPP::fppType` defaults to `FPP_TYPE::FPP` in every constructor and is set to `FPP_TYPE::FPP` from a low-band `typeId` in `TypeIDtoControllerType`. The `/config.php` `"Falcon Player"` title check in `parseConfig()` is the only other writer, and **nothing on the discovery-plus-upload path calls it** at this pin.

So the four-item surface of section 9.7, plus the playlist endpoints of section 10.6, gets a node listed and receiving FSEQ files with **no ShowMesh-served byte claiming to be Falcon Player**. Section 9.7b's identity gate is confined to the Controllers tab and the model-push path, both of which stay deferred. The upstream `.xcontroller` vendor-listing question therefore does not gate this feature.

**Still L1.** Section 9.7b's own caveat, that the two gates were traced through different functions and not reconciled against a single call graph, is narrowed but not removed: what is now established is that the FSEQ path at this pin contains no call into `parseConfig()`. A bench that lists a node and receives a file without serving the string is what closes it.

### 10.4 Port 80 is hardcoded, and the ping reply lands

**Fact.** Discovery builds `"http://" + host + url` where `host` is the bare address from the ping, and the upload URL is built the same way. There is no port in either. Section 9 recorded the port as unrecorded; it is now recorded as **80, hardcoded, for both discovery and upload**.

https://github.com/xLightsSequencer/xLights/blob/ae379c0408ab39f3de265aea13c326bf48ab84b7/src-core/discovery/Discovery.cpp#L485

**Inferred, not verified.** A manually typed `host:port` would concatenate into a working URL. That is inference from string concatenation and no one has run it.

**Fact.** xLights binds its discovery datagram socket to `ANY:32320` for receiving, so ShowMesh's unicast reply to the source address on the FPP control port lands. The existing reply-port behaviour is correct for xLights as well as for FPP.

### 10.5 The chunked PATCH path is type-gated, not version-gated

**Superseded in part: section 9.3's framing of the version gates.** Section 9.3 is right that the advertised version changes which code paths run, and right that 7.0, 9.3 and 10.0 are the gates that matter. What it does not say, and what a reader could reasonably infer from the name "V7", is that the chunked upload is version-gated. It is not.

**Fact.** `uploadOrCopyFile()` selects the chunked sender purely on `fppType == FPP_TYPE::FPP`:

https://github.com/xLightsSequencer/xLights/blob/ae379c0408ab39f3de265aea13c326bf48ab84b7/src-core/controllers/FPP.cpp#L1002-L1007

and the sender itself is the `PATCH /api/file/{dir}` chunked path with `Upload-Length` and `Upload-Name`:

https://github.com/xLightsSequencer/xLights/blob/ae379c0408ab39f3de265aea13c326bf48ab84b7/src-core/controllers/FPP.cpp#L964-L998

`fppType == FPP_TYPE::FPP` also gates the media upload branch, the `/api/sequence/{name}/meta` upload-skip check, and the dialog branch that renders the FSEQ type dropdown, the playlist widget and the models and UDP dropdowns. So the low-band `typeId` of section 10.2 is load bearing several times over, not once.

**The version gates, as they actually apply:**

| Gate | Effect |
|---|---|
| **7.1**, or 6.3.3 with `capeInfo.verifiedKeyId` | Eligibility. Below it the target is not offered at all. |
| **7.0** | `enableMinorVersionFeatures(2)` on the output file, applied only when `fppType == FPP_TYPE::FPP`. |
| **9.3** | Below it, xLights **strips the `XS`, `XN` and `XR` variable headers** from the FSEQ. At 9.3 and above with sparse selected they are kept. This is the sparse-FSEQ gate. |
| **10.0** | Virtual display map endpoint choice, output pacing, universe-output input flag, Falcon V4 differential receivers. **None of these is on the FSEQ upload path**, and none is implemented by a render node. |

https://github.com/xLightsSequencer/xLights/blob/ae379c0408ab39f3de265aea13c326bf48ab84b7/src-core/controllers/FPP.cpp#L4214-L4224

https://github.com/xLightsSequencer/xLights/blob/ae379c0408ab39f3de265aea13c326bf48ab84b7/src-core/controllers/FPP.cpp#L1310-L1316

https://github.com/xLightsSequencer/xLights/blob/ae379c0408ab39f3de265aea13c326bf48ab84b7/src-core/controllers/FPP.cpp#L1359-L1366

**Fact.** `IsVersionAtLeast` compares major, then minor, then patch; the fields are parsed out of the `Version` string and then **overridden by the explicit `majorVersion` and `minorVersion` JSON integers when present**. The string parser reads `version[2]` and `version.substr(2)`, so it expects a single-digit major and would misparse `"10.0.0"`.

https://github.com/xLightsSequencer/xLights/blob/ae379c0408ab39f3de265aea13c326bf48ab84b7/src-core/controllers/FPP.cpp#L601-L615

https://github.com/xLightsSequencer/xLights/blob/ae379c0408ab39f3de265aea13c326bf48ab84b7/src-core/controllers/FPP.cpp#L3946-L3980

**Chosen advertised version: `"9.5.0"`, with explicit `"majorVersion": 9` and `"minorVersion": 5`.** It clears 7.1 eligibility, clears the 7.0 minor-version-features gate, and clears the 9.3 sparse-header gate, which together is the best FSEQ path available; it stays below 10.0, so xLights takes none of the branches a render node does not implement; it is a real, shipped FPP release line rather than an arbitrary high number; and it keeps the major single-digit so the string parser and the integers agree. `"9.3.0"` clears exactly the same three gates and is the conservative alternative. Advertise **both** the string and the two integers.

### 10.6 The playlist flow, and why the node advertises mode `player`

**This reverses section 9.6's "not required" for one endpoint pair.** Section 9.6 is still correct that the playlist dropdown defaults to empty and that nothing forces an operator to use it. What section 9.6 did not know is that the dropdown is the cleanest binding available in the existing xLights UI for answering "which ShowMesh show does this upload belong to", which is the design question Track E's FC3 was otherwise going to have to guess at.

**Fact, and it decides the advertised mode.** The playlist cell is a real `wxComboBox` **only when the target's mode string starts with `player` or `master`**. For any other mode, `remote` included, the cell is an inert `wxStaticText` and the playlist path is unreachable.

https://github.com/xLightsSequencer/xLights/blob/ae379c0408ab39f3de265aea13c326bf48ab84b7/src-ui-wx/controllers/FPPConnectDialog.cpp#L740-L752

**Fact, and the two constraints are compatible.** The FSEQ type default is "V2 Sparse/zstd" for every mode **except** `master`. So `player` gets both the playlist dropdown and sparse-by-default rendering; `remote` gets sparse but no dropdown; `master` gets the dropdown but loses sparse.

https://github.com/xLightsSequencer/xLights/blob/ae379c0408ab39f3de265aea13c326bf48ab84b7/src-ui-wx/controllers/FPPConnectDialog.cpp#L653-L663

**So the node advertises `"Mode": "player"`.** Section 9.7's "a mode other than `master`" is narrowed to `player` specifically.

**Fact.** During discovery xLights issues `GET /api/playlists` and keeps the parsed body only on an exact 200, swallowing a parse failure, then reads it as a list of plain strings.

https://github.com/xLightsSequencer/xLights/blob/ae379c0408ab39f3de265aea13c326bf48ab84b7/src-core/controllers/FPP.cpp#L4014-L4025

https://github.com/xLightsSequencer/xLights/blob/ae379c0408ab39f3de265aea13c326bf48ab84b7/src-core/controllers/FPP.cpp#L4354-L4358

**Fact, FPP side, which fixes the wire shape.** `GET /api/playlists` lists the playlist directory, strips `.json`, sorts, and returns a **bare JSON array of strings**. Not an object, not `{"playlists":[...]}`.

https://github.com/FalconChristmas/fpp/blob/139d7e6ba8c70d5a5f835a9664ddbab36853a945/www/api/controllers/playlist.php#L14-L29

**Fact, on selection.** `FPP::UploadPlaylist(name)` is read-modify-write: `GET /api/playlist/{urlencoded name}`, tolerating a 404 or a non-object body by starting from an empty object; append each not-already-present sequence entry to `mainPlaylist`; rewrite `name`, `random` and `playlistInfo`; then `POST /api/playlist/{urlencoded name}` with the whole object. **It returns false unconditionally and the POST status is never inspected.**

https://github.com/xLightsSequencer/xLights/blob/ae379c0408ab39f3de265aea13c326bf48ab84b7/src-core/controllers/FPP.cpp#L1428-L1474

The entry shape is, with media:

```json
{"type":"both","enabled":1,"playOnce":0,
 "sequenceName":"<name>","mediaName":"<media>",
 "videoOut":"--Default--","duration":<float seconds>}
```

and without:

```json
{"type":"sequence","enabled":1,"playOnce":0,
 "sequenceName":"<name>","duration":<float seconds>}
```

**Fact.** The dropdown is read twice per row, so `UploadPlaylist` fires **up to twice per target**: once in the configuration phase before any sequence is prepared, and once in the finalisation phase after the uploads. On the first call `sequences` is still empty, so the node sees one POST carrying no new entries followed by one POST carrying them. **The operation must be idempotent.**

https://github.com/xLightsSequencer/xLights/blob/ae379c0408ab39f3de265aea13c326bf48ab84b7/src-ui-wx/controllers/FPPConnectDialog.cpp#L1286-L1290

https://github.com/xLightsSequencer/xLights/blob/ae379c0408ab39f3de265aea13c326bf48ab84b7/src-ui-wx/controllers/FPPConnectDialog.cpp#L1582-L1589

**Fact.** Both call sites skip `UploadPlaylist` entirely when the cell is empty, and empty is the default, so an operator who ignores the dropdown touches no playlist endpoint. The combo carries `wxTE_PROCESS_ENTER`, so an operator can also **type a name the target never advertised**, and since the POST status is never checked, ShowMesh cannot report a rejection back through xLights and must surface its own evidence.

**Fact, the join key.** `baseName` is the file name **with its extension and no directory**. It is the `sequences` map key, it is what `uploadFileV7` sets as `Upload-Name` verbatim, and it is what the appended playlist entry carries as `sequenceName`. So `Upload-Name` and `sequenceName` are the same string, extension included, and that identity is the join between the bytes and the show binding with no additional handshake.

https://github.com/xLightsSequencer/xLights/blob/ae379c0408ab39f3de265aea13c326bf48ab84b7/src-core/controllers/FPP.cpp#L1141-L1175

https://github.com/xLightsSequencer/xLights/blob/ae379c0408ab39f3de265aea13c326bf48ab84b7/src-core/controllers/FPP.cpp#L1383-L1400

**Fact, FPP side.** The `PATCH /api/file/{DirName}` handler takes the file name straight from `Upload-Name` and writes `"$dir/$fileName"` with **no sanitisation and no rename**, the only transformation being an `iconv` to UTF-8 when the header is not valid UTF-8. `POST` to the same route is a no-op returning a `uniqid()`, which confirms section 9.4.

https://github.com/FalconChristmas/fpp/blob/139d7e6ba8c70d5a5f835a9664ddbab36853a945/www/api/controllers/files.php#L1221-L1300

Sanitisation happens later and conditionally: `playlist_update` rewrites a `sequenceName` to a sanitised variant only if the literal name is not on disk and the sanitised one is.

https://github.com/FalconChristmas/fpp/blob/139d7e6ba8c70d5a5f835a9664ddbab36853a945/www/api/controllers/playlist.php#L316-L341

**Fact, encoding.** xLights URL-encodes the playlist name with its own encoder, preserving alphanumerics plus `- @ * _` and encoding everything else including spaces. The receiver must decode the path segment the way FPP does.

**Unverified.** None of section 10.6 was exercised against a running xLights. Whether advertising `player` changes any dialog behaviour beyond the FSEQ default and the playlist widget was not exhaustively traced.

### 10.7 Owner rulings, 2026-08-25

The five questions section 9 and the Track E seam document left open were answered by the owner on 2026-08-25 and are recorded durably in [ADR-044](../decisions/ADR-044-agent-inbound-http-listener.md). Summarised here because a reader of this record needs to know which way the facts above were resolved:

1. **`typeId` is `0x7F`, provisional**, with the reasoning of section 10.2 and never `0x01`. Upstream registration in both projects is a separate follow-up and gates nothing.
2. **The listener binds port 80**, granted by `AmbientCapabilities=CAP_NET_BIND_SERVICE` on the node service unit, with `SHOWMESH_FPPCONNECT_LISTEN_ADDR` as a start-time override for dev stacks and tests. Port 80 is not negotiable with the client (section 10.4), so the choice was only ever about how the process obtains it.
3. **The upload endpoint is unauthenticated, as an accepted and recorded risk**, bounded by a directory allowlist (`sequences`, `music`, `videos`, everything else refused), a per-file byte cap, and a total asset-directory byte cap. Section 9.8's open question about xLights' credential behaviour is unchanged and stays a bench item; the ruling does not depend on its answer, because the node demands nothing.
4. **Binding is by playlist name.** The node serves its ShowMesh show names at `GET /api/playlists`, so the operator's playlist choice is the show; `POST /api/playlist/{name}` binds the named files to it, joined by file name with extension per section 10.6, with the file name stem as the logical sequence. With no playlist chosen, bind to the active show the coordinator pushed. Anything unresolvable is kept on disk, registered nowhere, and reported as an unbound held file. Absent, null and empty stay three different things.
5. **The two byte caps are operator configuration and are store-backed**, as a `fppconnect.settings` kind under [ADR-039](../decisions/ADR-039-operator-configuration-is-store-backed.md), pushed to nodes together with the channel range string, the active show and the show name list. They are not environment variables.
6. **The MultiSync ping's mode byte stays `remote`.** FPP unicasts sync only to nodes whose ping reports remote (section 10.2's `supportsUnicast = type < 0x80 && fppMode == REMOTE_MODE`), while `player` is served through `GET /api/system/info`'s `Mode` field, which is what xLights itself reads. Owner ruling, 2026-08-25.

### 10.8 What this amendment does not establish

- That any of it works. Every claim is source-read at the two pins named above. **Nothing ran against a live xLights or a live FPP.** Section 9.9's bench is what raises this section to L2, and section 9.9's acceptance list stands unchanged.
- **Whether the owner's xLights build matches `ae379c0`'s range semantics.** If his build is older and genuinely uses `start-len`, section 10.1's formatter ships every node the wrong channels. Confirming the running build's behaviour is the first thing the bench should do.
- Whether either pin corresponds to a shipping release tag.
- Section 9.8's list is otherwise unchanged: the HTML-sniffing fallback, cross-major-version server behaviour, and xLights' credential behaviour are all still unexamined.

## Decision, fallback, and revalidation

Use FPP Connect to upload the sequence and FSEQ to renderer nodes ahead of playback. The fallback is documented manual export plus direct node upload. Any proposed xLights modification must demonstrate that an external helper cannot provide the same result. Revalidate after material xLights or FPP Connect changes.
