# Track E, phase 2: FPP Connect compatibility

[Build plan](BUILD-PLAN.md) · [Track E](TRACK-E-show-authoring-and-assets.md) · [RES-003](../research/RES-003-xlights-fpp-connect-compatibility.md) · [ADR-026](../decisions/ADR-026-renderer-surface-model-and-reference-transport.md) · [ADR-027](../decisions/ADR-027-show-and-surface-model.md) · [ADR-028](../decisions/ADR-028-show-asset-store-and-identity.md)

Status: specified 2026-08-16 from RES-003 §9. Not started. **Scheduled early October by the owner**, deliberately after day-0: day-0 uses manual asset upload, which ADR-027 decision 4 and ADR-028 decision 8 make a permanent first-class path, and this work lands as one more ingestion path rather than a redesign.

## Goal

A ShowMesh render node appears as its own upload target in xLights' FPP Connect dialog, so xLights renders and delivers the per-node sparse FSEQ automatically: the operator selects targets and clicks upload, and each node ends up holding exactly its own channels, registered in the Track E asset store with full identity. No manual export, no file hunting, no transcribing channel ranges by hand in the wrong direction.

## What is already known, and where it lives

**Do not re-derive the compatibility surface; it is RES-003 §9, source-verified at pinned commits of both xLights (master and the shipping 2026.15 release) and FPP.** This document sequences the build; the research record owns the facts. The load-bearing ones:

- **The required surface is four items** (§9.7): a UDP ping response in the **v3 layout**, `GET /api/system/info` (with `uuid`, version ≥ 7.1, `Mode`, `channelRanges`), `GET /api/fppd/multiSyncSystems` with a correct self-entry, and chunked `PATCH /api/file/{dir}`.
- **The UDP responder is mandatory**, because a confirmed typo in xLights (present in master and the shipping release) means `typeId` can never be read from `/api/system/info`, and a zero `typeId` is an unconditional rejection. Manual IP entry does not avoid it: `broadcastPing=false` suppresses only the broadcast, and a unicast ping is still sent. §9.1 also records the standing warning: the typo is an accidental constant a point release could fix silently, so both the UDP path and the `multiSyncSystems` path are implemented as independent routes rather than designing around today's bug.
- **Per-node sparse rendering is pulled, automatic, and default-on** (§9.5): xLights reads the target's `channelRanges` string and, for any mode other than `"master"`, defaults to a sparse FSEQ containing only those channels. **The direction of travel favours ShowMesh**: we advertise the ranges we want and xLights honours them, which makes E2's manual surface ranges the natural input to this flow, not a stand-in for it.
- **An empty `channelRanges` silently yields a full non-sparse FSEQ**, the gigabytes-per-song case. E2 already refuses an empty range at configuration time; this track must additionally never advertise an empty string for a node whose surfaces are configured.
- **The real upload protocol is not the documented one** (§9.4): xLights never sends the documented initiating `POST`; repeated `PATCH /api/file/{dir}` in 16 MiB chunks with `Upload-Offset`/`Upload-Length`/`Upload-Name` headers, offset 0 clears stale fragments, and the file is complete when accumulated size equals `Upload-Length`, with no commit call. Implement what the real client sends; tolerate the documented `POST` as the no-op FPP itself treats it as.
- **The `/config.php` "Falcon Player" identity gate sits on the Controllers-tab and model-push path only** (§9.7b). The FSEQ/media upload path traced in the first pass never touches it. §9.7b's own caveat stands: the two gates were traced through different functions and have not been reconciled against a single call graph, so this is a bench question, not a settled fact.

## The finding that shapes the release posture

**It is plausible, and not yet proven, that the FSEQ delivery path requires no identity claim at all.** The owner's interoperability line (2026-08-13, recorded in CLAUDE.md and RES-003 §9.7b) forbids any release serving content that claims ShowMesh *is* Falcon Player. If the four-item surface alone gets a node listed in FPP Connect and receiving sparse FSEQs, then the forbidden string is simply never needed for this feature, and the upstream `.xcontroller` vendor-listing question stops gating this track and gates only the deferred model-push path.

**That is the first bench's headline question, and it has a named decision hanging on it**: whether a release can ship FPP Connect compatibility at all without the upstream listing. Per the project's own rule, that is what earns the bench its place.

## Dependencies

- **Track E seams E1 through E5.** The asset store and identity model this ingestion path registers into, and the surface configuration whose channel ranges become the advertised `channelRanges` string. Building the receiver before the store exists would recreate the floating-ownership problem Track E was created to end.
- **`pkg/multisync`'s discover-ping responder** (Step 1). Its first real consumer. **Whether it currently emits the v3 layout is unchecked** (RES-003 §9.2); xLights always parses responses at v3 offsets. RES-002's open item that it has never been answered by a real FPP is unchanged and is not this track's to close.
- **The agent grows an HTTP listener.** It has never had one; today it speaks only MQTT. This is a real architectural addition to `cmd/showmesh-agent` and needs care rather than ceremony: the listener exists to receive assets from xLights, not to become a second control API. ADR-013 does not obstruct the UDP bind: a render node runs no `fppd`, so it binds 32320 normally with no port sharing.
- **Not dependent on Track B's media path.** Receiving and registering an FSEQ requires no GStreamer, no NDI, and no renderer. This track can land before or after B2; only the extraction and playback of what was delivered belongs to B.

## Seams, in build order

**FC0. The v3 ping layout, verified and fixed.** Confirm or correct `pkg/multisync`'s responder against the v3 offsets RES-003 cross-verified in both directions (xLights' `ProcessFPPPingPacket` and FPP's `MultiSync::CreatePingPacket`). Table-driven tests against hand-built v3 packets, same discipline as Step 1. Small, standalone, and everything else is dead until it is right.

**FC1. The agent's discovery surface.** The HTTP listener with `GET /api/system/info` and `GET /api/fppd/multiSyncSystems`, plus the UDP responder wired into the agent with `typeId` and mode from configuration. `uuid` maps to the node's ShowMesh identity and must be stable across restarts. The advertised version is a **deliberate compatibility choice** mirroring a real, recent, well-tested FPP release, recorded with its reasoning, because RES-003 §9.3 establishes the version changes which xLights code paths run for every later step (FSEQ features gate at 7.0, 9.3, and 10.0). `channelRanges` is derived from the node's assigned surfaces (E2), formatted as `start-len,start-len,...`; a node with no configured surface advertises nothing rather than an empty string.

**FC2. The upload receiver.** Chunked `PATCH /api/file/{dir}` per the real protocol, directories `sequences`/`music`/`videos` accepted, `effects` and everything else refused politely. Assembly per §9.4 semantics; then hash, then register into the asset store, in that order. **A partial upload registers nothing** (Track E's standing criterion): stage, assemble, hash, rename, register, and any failure before the last step leaves no trace an operator could mistake for an asset. xLights sends no hash, so content identity is computed node-side after assembly. The meta endpoints (`/api/sequence/{name}/meta`, `/api/media/{name}/meta`) are pure upload-skip optimisations where a 404 breaks nothing; ship them only if trivially true, and never let them claim a file the store has not verified.

**FC3. Coordinator integration.** The node reports the ingested asset as evidence through the same manifest path E5 built; the coordinator resolves identity (show + logical sequence + target + content hash, runtime filename preserved separately per ADR-028). The target component is the receiving node itself. How an unsolicited upload binds to a show and logical sequence is this seam's design question, listed below. ADR-028 decision 8 is the test: this must land as an ingestion path into the existing model, and any pressure to reshape the model is a defect in this seam, not in the model.

**FC4. The bench against a real xLights, and it is the point of the whole track.** RES-003 §9.9: nothing above L1 without a running xLights. The owner runs xLights; this bench needs his machine and his hands, so it lands on the punch list as a guided session rather than an unattended gate. In order: the node appears in the FPP Connect list with **no identity string served anywhere**; it receives a correctly sparse FSEQ matching its advertised ranges; three nodes with distinct ranges each receive distinct correct files under one filename (Track E's criterion 2, now exercised end to end through xLights itself); existing real FPP targets in the same dialog behave exactly as before; and an upload interrupted mid-transfer registers nothing. Results move RES-003's §9 conclusions to L2 and answer the release-posture question above.

## Decisions this track must make

- **`typeId` selection.** §9.7 names the risk: a value in `1..0x7F` that collides with a real hardware class. The honest long-term shape is the same as the `.xcontroller` question, an upstream registration in FPP's own protocol registry, and it is unresearched. Bench work can proceed under a provisional value; **what ships in a release is an owner decision** informed by that research, and the impersonation line applies to it: speaking the protocol is interoperability, claiming another device's registered identity is not.
- **Which port xLights contacts, and whether it is configurable.** RES-003 did not record it. Real FPP serves its API on 80; an agent binding 80 needs privileges the agent currently does not ask for. Trace it in the xLights source first (cheap, same method as the rest of §9); the answer decides whether the listener needs a privileged port, a capability, or nothing special.
- **Authentication posture of the node's upload surface.** This is an unauthenticated write endpoint on a node, accepted because xLights is the client and §9.8 records its credential behaviour as unexamined. ADR-024 governs the coordinator, not agents, so nothing existing answers this. The show network is isolated and the exposure is bounded (a bad actor can fill a disk or plant an FSEQ that then fails no hash it ever claimed), but it must be a recorded decision with its reasoning, not a silence. Growing real auth here would also contradict the standing "no further security surface without being asked" cut, so the likely answer is a documented accepted risk plus disk bounds; the owner rules either way.
- **How an unsolicited upload binds to show and logical sequence.** xLights sends a filename and bytes, nothing more. The obvious candidate is binding to the active show with the logical sequence inferred from the filename stem, with anything unresolvable registered as unbound and surfaced for the operator to claim, never guessed silently. Whatever is chosen, absent, null, and empty stay three different things, and a wrong silent guess is worse than a visible unbound asset.
- **Disk bounds on the node.** FC2 accepts multi-hundred-MB files onto agent hosts. The full-disk behaviour E4 decides for the coordinator needs a node-side answer too, and "the disk filled during upload" must register nothing, same as any other interruption.

## Explicitly deferred, with reasons

- **Model-definition receive (`POST /api/models` path).** Blocked behind the `/config.php` identity gate (§9.7b), which no release may fake, and behind the upstream vendor-listing research. It is also not needed for the goal: it saves transcribing a start channel, and E2's manual ranges are first-class. Its schema is traced and waiting in §9.7b when the listing question resolves.
- **The `.xcontroller` upstream vendor listing.** Its own research pass, flagged in RES-003 §9.7b with the open questions already enumerated. Nothing in FC0 through FC4 waits on it if the bench confirms the FSEQ path needs no identity claim.
- **Playlist and universe-output endpoints** (§9.6): per-target dropdowns defaulting to "None", serving FPP-as-pixel-controller deployments. A render node is not one.
- **The `GET /` HTML-sniffing fallback**: unexamined upstream behaviour, avoided entirely by serving `multiSyncSystems` correctly.

## Acceptance criteria

1. From an unmodified, shipping xLights on the owner's machine: a ShowMesh render node is discovered and listed in FPP Connect alongside real FPP targets, with no ShowMesh-side content claiming to be Falcon Player.
2. Uploading a sequence delivers a sparse FSEQ containing exactly the node's advertised channels, verified by inspecting the received file's header and ranges, not by trusting the dialog.
3. Three nodes, three ranges, one filename: each node holds its own correct artifact, registered with full ADR-028 identity, and `showmeshctl` can show all three and their hashes.
4. Existing FPP targets in the same session upload exactly as before (Track E spec's standing criterion, proven rather than presumed).
5. An interrupted upload registers nothing on the node and nothing in the store.
6. A node with no configured surface does not advertise an empty `channelRanges`, and the configuration surface refuses to create the condition that would.
7. Every result recorded in RES-003 with versions, and §9's conclusions moved to L2 only for what the bench actually exercised.

**Bound by:** ADR-002, ADR-003, ADR-008, ADR-011, ADR-013, ADR-014, ADR-024 (in spirit, per the decision above), ADR-026, ADR-027, ADR-028, ADR-030, RES-002, RES-003. The interoperability-versus-impersonation rule (CLAUDE.md, owner 2026-08-13) binds every seam, and FC4's first criterion exists to prove compliance rather than assert it.

**Out of scope:** any xLights modification (RES-003's standing rule: an external helper must be shown insufficient first), sequence editing, FSEQ extraction and playback (Track B), asset retention across seasons, and anything that makes the agent's HTTP listener a general control surface.
