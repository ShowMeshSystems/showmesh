# Track E, phase 2: FPP Connect compatibility

[Build plan](BUILD-PLAN.md) · [Track E](TRACK-E-show-authoring-and-assets.md) · [RES-003](../research/RES-003-xlights-fpp-connect-compatibility.md) · [ADR-026](../decisions/ADR-026-renderer-surface-model-and-reference-transport.md) · [ADR-027](../decisions/ADR-027-show-and-surface-model.md) · [ADR-028](../decisions/ADR-028-show-asset-store-and-identity.md) · [ADR-044](../decisions/ADR-044-agent-inbound-http-listener.md)

Status: specified 2026-08-16 from RES-003 §9. **Every decision this track owed was settled 2026-08-25 and is recorded in [ADR-044](../decisions/ADR-044-agent-inbound-http-listener.md) and the RES-003 §10 amendment.** The build is **pulled forward to start 2026-08-26** on an integration branch, landing on `main` after the 28 August hardware landing rather than in early October. Nothing changes about what it is: day-0 still uses manual asset upload, which ADR-027 decision 4 and ADR-028 decision 8 make a permanent first-class path, and this work still lands as one more ingestion path rather than a redesign.

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

**FC1. The agent's discovery surface.** The HTTP listener with `GET /api/system/info`, `GET /api/fppd/multiSyncSystems`, `GET /api/playlists` and `GET /api/playlist/{name}`, plus the UDP responder wired into the agent. The advertised values are settled and are not a builder's choice: **`typeId` `0x7F`** (provisional, never `0x01`, ADR-044 decision 6), **mode `player`** rather than `remote`, because xLights renders the playlist widget only for `player` or `master` and sparse-by-default holds for every mode but `master`, and **version `9.5.0`** with explicit `majorVersion` 9 and `minorVersion` 5, past the 7.1 eligibility gate and the 7.0 and 9.3 FSEQ gates and deliberately below 10.0. Each is a deliberate compatibility choice whose reasoning is RES-003 §10.2, §10.5 and §10.6, and each changes which xLights code path runs, so none may be edited as a cosmetic detail. `uuid` maps to the node's ShowMesh identity and must be stable across restarts. **`channelRanges` is `start-end`, 0-based, with an inclusive end** (RES-003 §10.1, correcting §9.5): `show.surface`'s range is 1-based, so the advertised string runs `start-1` to `start+count-2`, and the base conversion is the point of the formatter. A node with no configured surface advertises nothing rather than an empty string. `GET /api/playlists` returns a **bare JSON array** of ShowMesh show names, not an object; `GET /api/playlist/{name}` answers with the show's current entries, and a 404 is tolerated by the client.

**FC2. The upload receiver and the show binding.** Chunked `PATCH /api/file/{dir}` per the real protocol, directories `sequences`/`music`/`videos` accepted, `effects` and everything else refused politely, the allowlist being the first of ADR-044 decision 4's three bounds. The other two are here too: a **per-file byte cap** and a **total asset-directory byte cap**, both from the store-backed `fppconnect.settings` kind, both registering nothing and removing the staging file when exceeded, and `ENOSPC` classified the same way. This seam also serves **`POST /api/playlist/{name}`**, which is how an upload binds to a show: the playlist the operator picks in FPP Connect is the ShowMesh show, the join key is the file name with its extension (identical as `Upload-Name` and as the entry's `sequenceName`), and the file name stem is the logical sequence. xLights fires it up to twice per target, the first time before any file exists, so it must be idempotent, and it never inspects the response status, so ShowMesh surfaces its own evidence. Assembly per §9.4 semantics; then hash, then register into the asset store, in that order. **A partial upload registers nothing** (Track E's standing criterion): stage, assemble, hash, rename, register, and any failure before the last step leaves no trace an operator could mistake for an asset. xLights sends no hash, so content identity is computed node-side after assembly. The meta endpoints (`/api/sequence/{name}/meta`, `/api/media/{name}/meta`) are pure upload-skip optimisations where a 404 breaks nothing; ship them only if trivially true, and never let them claim a file the store has not verified.

**FC3, minimal. Registration through the existing asset API.** On a completed assembly the node registers the file through the coordinator's existing **`POST /api/v1/assets` with `targetKind=node`** and its own node id as the target, bearer `SHOWMESH_AGENT_API_TOKEN`, then republishes its inventory. That is what makes an uploaded file dispatchable rather than merely held: supersession, rollback, hashing and audit all come from the path E5 already built, and the coordinator resolves identity as show plus logical sequence plus target plus content hash, with the runtime filename preserved separately per ADR-028. If the coordinator is unreachable the node keeps the file, retries, and says so as evidence; it never deletes and never claims registration. Binding is no longer this seam's design question: ADR-044 decision 8 settled it, and an unresolvable upload is kept, registered nowhere, and reported as an unbound held file. ADR-028 decision 8 is the test: this must land as an ingestion path into the existing model, and any pressure to reshape the model is a defect in this seam, not in the model.

**FC4. The bench against a real xLights, and it is the point of the whole track.** RES-003 §9.9: nothing above L1 without a running xLights. The owner runs xLights; this bench needs his machine and his hands, so it lands on the punch list as a guided session rather than an unattended gate. In order: the node appears in the FPP Connect list with **no identity string served anywhere**; it receives a correctly sparse FSEQ matching its advertised ranges; three nodes with distinct ranges each receive distinct correct files under one filename (Track E's criterion 2, now exercised end to end through xLights itself); existing real FPP targets in the same dialog behave exactly as before; and an upload interrupted mid-transfer registers nothing. Results move RES-003's §9 conclusions to L2 and answer the release-posture question above.

## Decisions this track had to make, all resolved 2026-08-25

Every item below was an open owner decision when this document was written on 2026-08-16. All five were ruled on 2026-08-25 and the durable record is [ADR-044](../decisions/ADR-044-agent-inbound-http-listener.md). The original reasoning is kept because it is why each answer is what it is.

- **`typeId` selection.** **Resolved: `0x7F`, provisional, never `0x01`; upstream registration is a separate follow-up that gates nothing (ADR-044 decision 6).** §9.7 names the risk: a value in `1..0x7F` that collides with a real hardware class. The honest long-term shape is the same as the `.xcontroller` question, an upstream registration in FPP's own protocol registry. The impersonation line applies to it: speaking the protocol is interoperability, claiming another device's registered identity is not.
- **Which port xLights contacts, and whether it is configurable.** **Resolved: port 80, hardcoded in the client, bound by `CAP_NET_BIND_SERVICE` with `SHOWMESH_FPPCONNECT_LISTEN_ADDR` as a start-time override (ADR-044 decision 5; the source reading is RES-003 §10.4).** RES-003 §9 did not record it. Real FPP serves its API on 80; an agent binding 80 needs privileges the agent currently does not ask for. Trace it in the xLights source first (cheap, same method as the rest of §9); the answer decides whether the listener needs a privileged port, a capability, or nothing special.
- **Authentication posture of the node's upload surface.** **Resolved: unauthenticated, as an accepted and recorded risk, bounded by the directory allowlist and the two byte caps (ADR-044 decision 4).** This is an unauthenticated write endpoint on a node, accepted because xLights is the client and §9.8 records its credential behaviour as unexamined. ADR-024 governs the coordinator, not agents, so nothing existing answers this. The show network is isolated and the exposure is bounded (a bad actor can fill a disk or plant an FSEQ that then fails no hash it ever claimed), but it must be a recorded decision with its reasoning, not a silence. Growing real auth here would also contradict the standing "no further security surface without being asked" cut, so the likely answer is a documented accepted risk plus disk bounds; the owner rules either way.
- **How an unsolicited upload binds to show and logical sequence.** **Resolved: the playlist name is the ShowMesh show, joined by file name with extension, with the active show as the fallback and unresolvable uploads held unbound (ADR-044 decision 8).** xLights sends a filename and bytes plus, when the operator uses the playlist dropdown, a show name. The obvious candidate is binding to the active show with the logical sequence inferred from the filename stem, with anything unresolvable registered as unbound and surfaced for the operator to claim, never guessed silently. Whatever is chosen, absent, null, and empty stay three different things, and a wrong silent guess is worse than a visible unbound asset.
- **Disk bounds on the node.** **Resolved: a per-file cap and a total asset-directory cap, both operator configuration in the store-backed `fppconnect.settings` kind (ADR-044 decisions 4 and 5).** FC2 accepts multi-hundred-MB files onto agent hosts. The full-disk behaviour E4 decides for the coordinator needs a node-side answer too, and "the disk filled during upload" must register nothing, same as any other interruption.

## Explicitly deferred, with reasons

- **Model-definition receive (`POST /api/models` path).** Blocked behind the `/config.php` identity gate (§9.7b), which no release may fake, and behind the upstream vendor-listing research. It is also not needed for the goal: it saves transcribing a start channel, and E2's manual ranges are first-class. Its schema is traced and waiting in §9.7b when the listing question resolves.
- **The `.xcontroller` upstream vendor listing.** Its own research pass, flagged in RES-003 §9.7b with the open questions already enumerated. Nothing in FC0 through FC4 waits on it if the bench confirms the FSEQ path needs no identity claim.
- **Universe-output endpoints and the models dropdown** (§9.6): per-target dropdowns defaulting to "None", serving FPP-as-pixel-controller deployments. A render node is not one. **The playlist endpoints are no longer deferred**: RES-003 §10.6 found that the playlist dropdown is the only binding signal xLights' existing UI can carry, so FC1 and FC2 serve them deliberately.
- **The `GET /` HTML-sniffing fallback**: unexamined upstream behaviour, avoided entirely by serving `multiSyncSystems` correctly.

## Listener surface

FC1's build, as specified and tested (ADR-044 decision 2: this section, not
`api/openapi.yaml`, is this listener's specification). The listener binds
`SHOWMESH_FPPCONNECT_LISTEN_ADDR` (default `:80`; ADR-044 decision 5) and
serves exactly four `GET` routes (`HEAD` is also served, with no body, per
`net/http`'s own handling). Anything else, including a known path with the
wrong method, is 404: ADR-044 decision 1 makes everything outside the four
routes 404, so a wrong method never gets `http.ServeMux`'s own 405-plus-
`Allow`-header answer. Routing does not use `http.ServeMux` at all, because
its automatic path cleaning would 301-redirect a dirty path (a doubled
slash, a literal `..` segment) with an HTML body before any pattern is even
considered; this listener matches the escaped path itself so that path is
structurally unreachable. The fourth route's `{name}` is additionally
refused, before the show-name check, if it contains `/`, `\`, a NUL byte, or
is exactly `..`: FC2's upload receiver reuses this same string near the
filesystem.

- **`GET /api/system/info`**: a JSON object with `uuid` (UUIDv5 of the node
  id under a fixed ShowMesh namespace UUID, stable across restarts and
  identical on every node sharing a node id), `HostName` (the node id),
  `Version`/`majorVersion`/`minorVersion` (`internal/fppconnect`'s
  `AdvertisedVersion`/`AdvertisedVersionMajor`/`AdvertisedVersionMinor`),
  `Mode` (`AdvertisedMode`, `"player"`), `typeId` (`127`, i.e.
  `multisync.SystemTypeShowMesh`), `channelRanges` (the holder's advertised
  string, key omitted entirely when it is empty, never `""`), and
  `Platform`/`Variant` (`"ShowMesh"`).
- **`GET /api/fppd/multiSyncSystems`**: `{"systems":[<one self-entry>]}`. The
  entry carries `address` (the local IP of the connection the request
  arrived on, never a wildcard bind address and never `127.0.0.1` unless
  that is genuinely what it arrived on), `hostname`, `fppMode`/
  `fppModeString` (`multisync.PingModePlayer` / `"player"`), `version`/
  `majorVersion`/`minorVersion`, `type`/`typeId` (`"ShowMesh"` / `127`),
  `uuid`, `channelRanges` (same omission rule as system info), and `local:
  true`.
- **`GET /api/playlists`**: a bare JSON array of the holder's show names,
  `200`, `[]` when there are none. Never an object.
- **`GET /api/playlist/{name}`**: `{name}` is percent-decoded the way FPP
  decodes it (segment-by-segment percent-decoding; a literal `+` is never
  treated as a space). A name on the holder's show list gets `{"name":
  <name>, "mainPlaylist": [], "leadIn": [], "leadOut": [], "playlistInfo":
  {"total_duration": 0, "total_items": 0}}`; FC2's upload receiver is what
  populates `mainPlaylist`. Any other name is 404.
- **Everything else**: 404, a short plain-text body, never HTML.

**Disabled behaviour.** While the pushed `fppconnect.settings.enabled` flag
is false, every route above answers 404 and the listener's status reports
"disabled by configuration." The socket stays bound throughout, so the next
enable takes effect with no restart. `fppConnectStateView.Enabled` reads this
flag from the holder the coordinator's `fppconnect.configure` push
(`internal/agent/fppconnectops.go`) applies, and reports `true` (enabled)
before the coordinator has ever pushed settings, matching the coordinator's
own `resolveFPPConnectSettings` default.

**Bind-failure behaviour.** A bind failure (most commonly a node without
`CAP_NET_BIND_SERVICE` trying to bind `:80`) never stops the agent. It is
recorded the way `multiSyncStatus` records a MultiSync bind failure, with
the reason text naming the address, and carried on the same
`showmesh.node.render/v1` payload MultiSync's own bind status is carried
on: `fppConnectListening`/`fppConnectReason`/`fppConnectObservedAt` sit
alongside `multiSyncListening`/`multiSyncReason`/`multiSyncObservedAt` in
the render report. No `node.fppconnect.*` collector signal or API field
exists yet to surface this to an operator the way the coordinator surfaces
other node evidence; that is a follow-up, not this seam's scope. A bind
failure never falls back to a different port: xLights only ever contacts
port 80.

**Environment.** `SHOWMESH_FPPCONNECT_LISTEN_ADDR` is the only environment
variable this feature adds (ADR-044 decision 5), allow-listed under ADR-039
decision 9 because a bind address must be known before the process starts.

**FC2's addition: the chunked upload and playlist bind routes.** Built and
tested as specified below (`internal/agent/fppconnectupload.go`'s HTTP
framing over `internal/agent/fppconnectheld.go`'s store). These two routes
never pass through the four fixed routes' small (4 KiB)
`fppConnectMaxBodyBytes` cap; each bounds its own, much larger, body.

**Per-route read deadlines (review round 1 finding 4).** The listener's
`http.Server` no longer sets a server-wide `ReadTimeout`: the original
fixed 10s value bounded header AND body together, so a real 16 MiB xLights
chunk (RES-003 section 9.4) on a slow link could time out mid-transfer.
Each route now sets its own deadline via `http.ResponseController.
SetReadDeadline` (`fppConnectSetReadDeadline`): `fppConnectDiscoveryReadDeadline`
(10s) on the four fixed discovery routes and the playlist routes,
`fppConnectFileReadDeadline` (10 minutes, generous rather than idle-based)
on the file PATCH route alone. `ReadHeaderTimeout` (5s) is unchanged and
still bounds a client that never finishes sending headers.
`WriteTimeout` (10s) is also unchanged: it governs only the connection's
Write() calls (the response), never request body reads, so it was never
the mechanism a slow upload could trip; the real prior risk on that axis
was a synchronous whole-file re-hash running between the last body byte
read and the response write, which finding 4's incremental hashing (below)
removes.

- **`PATCH /api/file/{dir}`**: the real chunked-upload transport (RES-003
  section 9.4). Headers `Upload-Offset`, `Upload-Length`, `Upload-Name` are
  all required; the body is one chunk, capped at 32 MiB
  (`fppConnectMaxChunkBytes`, headroom over xLights' own 16 MiB chunks, not
  an operator-configured cap). `{dir}` is matched as everything after the
  fixed prefix, including a literal `/`, so the directory allowlist check
  below is what turns a malformed segment into a 403 rather than routing
  matching it away as an unmapped 404.
- **`POST /api/file/{dir}`**: the documented initiating call xLights never
  sends. Accepted as FPP's own handler treats it, a no-op returning `200`
  with a JSON body carrying an opaque id (`{"id": "<uuid>"}`), after the
  same directory allowlist check PATCH applies.
- **`POST /api/playlist/{name}`**: binds files to a show (ADR-044 decision
  8). Reads only `mainPlaylist[].sequenceName` and `mainPlaylist[].mediaName`
  from the body; every other field of FPP's playlist object is ignored.
  Always answers `200` regardless of outcome, matching RES-003 section
  10.6's finding that xLights never inspects this call's status. `{name}`
  is validated by the same `fppConnectValidPlaylistName` check GET applies
  (a path-shaped name is a 404, before any show-name membership check).
- **`GET /api/playlist/{name}`** (FC1's route, FC2's data): `mainPlaylist`
  is no longer always empty. It lists one entry per held file currently
  bound to `name`, in RES-003 section 10.6's "without media" shape
  (`{"type":"sequence","enabled":1,"playOnce":0,"sequenceName":"<name>","duration":0}`),
  sorted by file name. `duration` is always `0`: this seam parses no FSEQ
  or media metadata, and xLights' own read-modify-write only checks
  `sequenceName` presence, never `duration`, so the round trip holds
  without it. This is what makes xLights' GET-then-append merge
  idempotent across the up-to-twice-per-target POST RES-003 records.
  **Review round 1 finding 10, stated here rather than only in code: a
  held file from `music/` or `videos/` gets exactly this same shape.**
  RES-003 section 10.6 also documents a "with media" entry pairing one
  sequence with one media file
  (`{"type":"both",...,"sequenceName":"<seq>","mediaName":"<media>",...}`),
  which this store never emits: a bound sequence file and a bound media
  file are two independent held records that happen to share a `show`,
  not one paired binding this store tracks together, so there is no
  `mediaName` to attach to either one's entry. A held `Halloween.mp3` in
  `music/` therefore comes back as `{"type":"sequence",...,
  "sequenceName":"Halloween.mp3",...}`, not paired with any sequence.
  This does not break xLights' round trip (it only ever checks whether a
  `sequenceName` it already posted is present, never `type` or pairing),
  but a future reader of this endpoint that expects real pairing
  information will not find it here.

**The three bounds (ADR-044 decision 4), as built:**

1. **Directory allowlist.** Exactly `sequences`, `music`, `videos`
   (`fppConnectAllowedDirs`). Anything else, including `effects`, a
   `../`-prefixed segment, an empty segment, or a segment containing `/`,
   is refused with `403` and a plain-text reason naming the directory,
   before either PATCH or POST writes anything. Checked for both methods,
   not just PATCH, even though POST never writes: consistency, not a
   requirement POST's no-op behavior demands.
2. **Upload-Name never escapes its directory.** Validated by the same
   `fppConnectValidPlaylistName` check the fourth route's `{name}` already
   used (no `/`, `\`, NUL byte, or `..` segment, no name that
   `filepath.Clean`s down to `.` or `..` (review round 1 finding 9: a bare
   `.` passed every other check and then failed the rename into the held
   area with a `500`), non-empty), reused rather than reimplemented: both
   are "a string this listener writes near the filesystem." A violation is
   `403`.
3. **Per-file and total asset-directory byte caps**
   (`fppConnectHeldStore.WriteChunk`). `Upload-Length` over
   `fppconnect.settings.maxFileBytes` is refused at offset `0` (the first
   chunk of a fresh attempt; offset `0` always starts fresh) with `413`
   naming both numbers. An accumulated total that would exceed either the
   declared `Upload-Length` or `maxFileBytes` is refused the same way on
   any later chunk, discarding the fragment. The bytes already under
   `AssetDir` (assets, held files, staging, checked by walking the whole
   tree), **plus every other in-progress upload's own still-outstanding
   remainder** (review round 1 finding 5: checking only bytes already on
   disk let two uploads that each individually fit under the cap pass
   independently and together exceed it, since neither's check saw the
   other's undelivered bytes), plus the declared `Upload-Length` exceeding
   `fppconnect.settings.maxAssetDirBytes` is refused with `507` naming
   the numbers, checked once, at offset `0`. Both refusals remove the
   staging fragment and leave every existing file untouched.

**Disk-full outcome.** `ENOSPC` while writing a chunk is classified
distinctly from a generic write failure: `507`, a reason naming the disk
as full, the staging fragment removed, nothing registered. Injected in
tests through `fppConnectChunkWriter`, the small interface
`fppConnectHeldStore.WriteChunk` streams through (a chunk is read directly
off the request body and copied into the positioned staging file via
`io.Copy`/`io.NewOffsetWriter`, review round 1 finding 3: never buffered
whole in memory the way an earlier `io.ReadAll` version did) rather than
by filling a real disk.

**Refused-upload evidence (review round 1 finding 2).** ADR-044 decision 4
says exceeding a bound, or exhausting the disk, "is reported as evidence."
The original build returned a reason to the HTTP caller and persisted
nothing. Every refusal now appends one `fppConnectEvent` to the same
bounded evidence log unknown/ambiguous playlist posts use (below), with a
`kind` of `too-large`, `dir-full`, `disk-full`, `gap`, `bad-name`, or
`bad-dir`, the attempted `dir`/`name`, and the refusal reason text. See
"Reporting" below for how this reaches an operator.

**Held area layout**, under `<AssetDir>/fppconnect-uploads/`:

```
staging/<dir>/<Upload-Name>.partial   in-progress bytes
held/<dir>/<Upload-Name>              assembled bytes, renamed in only after hashing
index.json                            every held record, pending binding, and evidence event
```

An in-progress upload's offset, running SHA-256, and asset-directory-cap
reservation live only in memory (`fppConnectHeldStore.inFlight`), not as a
per-chunk on-disk sidecar: review round 1 findings 3 and 4 found the
original sidecar file added a JSON read-modify-write on every chunk for
state a restart already discards regardless (the boot sweep below), and
found completion re-hashing the whole finished file from disk risked
exceeding the listener's write timeout on a large upload. The hash is now
computed incrementally as each chunk streams through
(`io.TeeReader`/`hash.Hash`), so completion only finalizes an already-
running sum.

Assembly is offset-gated: a chunk is written only when its `Upload-Offset`
equals the bytes already received for that name (tracked in memory, above);
a gap or an overlap is `409`, and the fragment is discarded. Offset `0`
truncates the staging file as it opens (review round 1 finding 7: the
prior code discarded a stale fragment only via a best-effort `os.Remove`
whose error was ignored, then reopened without `O_TRUNC`, so a failed
Remove could leave a longer previous attempt's tail bytes past the new,
shorter upload's end); `staging/` is also swept at boot
(`sweepFPPConnectUploadStaging`, called from `agent.go` alongside
`sweepAssetStaging`), mirroring `internal/agent/assets.go`'s identical
discipline; `held/` and `index.json` are untouched by that sweep. On
completion (accumulated bytes equal `Upload-Length`), the in-memory hash is
finalized (`sha256:<hex>`, the store's identity per ADR-028), then the
staging file is renamed into `held/`; any failure before that rename
removes the staging fragment and nothing is registered.

**Pending bindings are bounded too (review round 1 finding 6).** A single
`POST /api/playlist/{name}` body can name tens of thousands of files
(`fppConnectMaxPlaylistBodyBytes` is 1 MiB, with no per-entry count limit)
that this node has not yet received; every one becomes a pending binding.
`fppConnectHeldStore.pending` is capped at `fppConnectMaxPending` (500)
the same way the evidence log is capped at `fppConnectMaxEvents` (50):
oldest evicted first, order tracked and persisted alongside the map so
eviction survives a restart correctly.

**Binding rules, as built (ADR-044 decision 8):** on `POST
/api/playlist/{name}`, every held file (in any directory) whose name
appears as a `sequenceName` or `mediaName` in the body is bound to `name`,
recording the file name stem as its logical sequence; a name in the body
that matches no held file yet is remembered as a pending binding so a file
completing afterwards binds on completion. `name` matching none of the
holder's show names is an unknown-playlist evidence event (bound nothing,
still `200`); `name` occurring more than once in the holder's show name
list (two shows sharing a display name, the only ambiguity this listener
can detect without a show id) is an ambiguous-playlist evidence event
(bound nothing, still `200`, naming the match count). On completion of a
file with no binding already recorded for it: a pending binding from an
earlier POST wins first; otherwise a prior record for the same file name
that was already bound keeps that binding (a re-uploaded file is not
silently unbound); otherwise the active show's tri-state decides,
distinguishing "never pushed" from "pushed null" in the record's
`unboundReason` when neither yields a bound show. An unbound held file is
a first-class, visible state (`fppConnectHeldRecord.Bound == false`,
`UnboundReason` naming why), never deleted and never guessed.

**Reporting.** Review round 1 finding 1 caught this as a gap in the
original build: `Held()`, `Events()`, and `SetOnHeld` existed with no
non-test caller, so ADR-044 decision 8's "reported as an unbound held file
the operator can claim" had nowhere to actually reach an operator, since
xLights never inspects any of these calls' response status. Fixed in this
seam, not deferred to FC3: `internal/agent/renderreport.go` reads
`fcHeld.Held()` and `fcHeld.Events()` on every render report tick and
publishes them as `fppConnectHeldCount`/`fppConnectHeld` (every currently
held file, bound or not, with its `unboundReason` when unbound) and
`fppConnectHeldEvents` (the bounded evidence log: `unknown` and
`ambiguous` playlist posts, and refused uploads: `too-large`, `dir-full`,
`disk-full`, `gap`, `bad-name`, `bad-dir`, ADR-044 decision 4) on the same
`showmesh.node.render/v1` payload the listener's own bind status already
travels on. Neither list is enforced as required by
`RenderPayload.Validate`, matching `fppConnectListening`'s identical
additive-compatibility reasoning: both fields are added after
`SchemaNodeRenderV1` first shipped. No `node.fppconnect.*` collector
signal or coordinator API field exists yet to surface this list the way
the coordinator surfaces other node evidence (matching the bind-status gap
already noted above); that remains a follow-up, and is listed as an
acceptance gap on this seam's own pull request.

**FC3's hook.** `fppConnectHeldStore.SetOnHeld(func(fppConnectHeldRecord))`
registers a callback invoked whenever a record is created or its binding
changes (upload completion and every successful bind); `Held() []
fppConnectHeldRecord` (the same method the render report above reads) lists
every currently held record, for a registration seam that starts after
files already exist. This hook is a separate concern from the reporting
paragraph above: it is how FC3 learns a file is ready to register with the
coordinator's asset store, not how an operator learns a file exists. Both
are internal to `internal/agent`; FC3 wires the callback from `agent.go`.

## Acceptance criteria

1. From an unmodified, shipping xLights on the owner's machine: a ShowMesh render node is discovered and listed in FPP Connect alongside real FPP targets, with no ShowMesh-side content claiming to be Falcon Player.
2. Uploading a sequence delivers a sparse FSEQ containing exactly the node's advertised channels, verified by inspecting the received file's header and ranges, not by trusting the dialog.
3. Three nodes, three ranges, one filename: each node holds its own correct artifact, registered with full ADR-028 identity, and `showmeshctl` can show all three and their hashes.
4. Existing FPP targets in the same session upload exactly as before (Track E spec's standing criterion, proven rather than presumed).
5. An interrupted upload registers nothing on the node and nothing in the store.
6. A node with no configured surface does not advertise an empty `channelRanges`, and the configuration surface refuses to create the condition that would.
7. Every result recorded in RES-003 with versions, and §9's conclusions moved to L2 only for what the bench actually exercised.

**Bound by:** ADR-002, ADR-003, ADR-008, ADR-011, ADR-013, ADR-014, ADR-024 (which governs the coordinator, not agents, which is why ADR-044 decision 4 exists), ADR-026, ADR-027, ADR-028, ADR-030, ADR-039, [ADR-044](../decisions/ADR-044-agent-inbound-http-listener.md), RES-002, RES-003 §9 and §10. The interoperability-versus-impersonation rule (CLAUDE.md, owner 2026-08-13) binds every seam, and FC4's first criterion exists to prove compliance rather than assert it.

**Out of scope:** any xLights modification (RES-003's standing rule: an external helper must be shown insufficient first), sequence editing, FSEQ extraction and playback (Track B), asset retention across seasons, and anything that makes the agent's HTTP listener a general control surface.
