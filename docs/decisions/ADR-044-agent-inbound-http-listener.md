# ADR-044: The Agent Serves One Inbound HTTP Listener, for xLights Ingestion Only

Status: Accepted (owner, 2026-08-25)
Date: 2026-08-25

## Context

A ShowMesh node agent has never listened for an inbound HTTP request. It speaks MQTT to the coordinator, opens one UDP socket for MultiSync discovery, and makes outbound HTTP calls to fetch asset bytes. Its listening TCP surface today is empty.

Track E phase 2 changes that. For a render node to appear as its own upload target in xLights' FPP Connect dialog, and to receive the per-node sparse FSEQ that xLights renders for it, the node must answer HTTP on the address xLights contacts. There is no alternative transport: xLights builds `http://<address>/...` from the address it learned in the discovery ping, and the port is not in the string. That is source-verified in [RES-003](../research/RES-003-xlights-fpp-connect-compatibility.md) §10.4 at the 2026-08-25 pins.

This is a real change to the agent's exposure model, and it deserves a decision rather than a seam-level default, for three reasons.

First, the surface receives **unauthenticated writes of multi-hundred-megabyte files from a third-party client**. [ADR-024](ADR-024-identity-authorization-and-audit.md) governs the coordinator's write surface and says nothing about agents, so nothing existing answers the posture question.

Second, an HTTP listener on a node is the obvious place for the next convenience endpoint. "While we are here, let the operator poke the pipeline" is how a compatibility shim becomes a second control API with no versioning, no authorization, no audit trail, and no `showmeshctl` parity, sitting on the one component that must keep running when the coordinator is gone.

Third, CLAUDE.md's rule that operator capability is API-first and reachable through `showmeshctl` at practical parity with the UI is written about the ShowMesh public API. Applying it mechanically to this listener would put third-party compatibility paths into `api/openapi.yaml` and imply an operator-facing contract that is not being offered.

The facts this record depends on are all L1 source reading at xLights `ae379c0408ab39f3de265aea13c326bf48ab84b7` and FPP `139d7e6ba8c70d5a5f835a9664ddbab36853a945`, recorded with permalinks in RES-003 §10. **Nothing has run against a live xLights.** That does not block the decision, because every choice below is reversible in configuration or in one seam, and because the bench cannot happen until something exists to bench.

## Decision

### 1. The agent may run exactly one inbound HTTP listener, and it exists to receive assets from xLights

One listener, one purpose. It answers the discovery and ingestion surface RES-003 §9.7 and §10.6 enumerate: `GET /api/system/info`, `GET /api/fppd/multiSyncSystems`, `GET /api/playlists`, `GET /api/playlist/{name}`, `PATCH /api/file/{dir}` and its no-op `POST`, and `POST /api/playlist/{name}`. Everything else is 404.

A second listener, or a second purpose on this one, needs a superseding record.

### 2. The listener is a compatibility shim, not part of the public API, and stays out of `api/openapi.yaml`

The paths it serves are another project's paths, shaped by another project's client. No ShowMesh operator, `showmeshctl` verb, Operator UI screen, or automation is meant to call them.

So CLAUDE.md's API-first parity rule does not apply to this surface. The rule exists so that an operator capability is never UI-only; this listener offers no operator capability. It is specified in [`TRACK-E-FPP-CONNECT.md`](../build/TRACK-E-FPP-CONNECT.md) and tested there, and it is absent from `api/openapi.yaml` by intent rather than by omission.

Every ShowMesh-facing capability the feature creates is a normal capability and follows the normal rule: the byte caps are a configuration kind with an endpoint and a CLI verb (decision 5), and the ingested file is registered through the existing `POST /api/v1/assets`.

### 3. It must not grow into a second control API

No endpoint on this listener may start, stop, reconfigure, or inspect anything for an operator's benefit. If a capability is worth having, it is worth having on the coordinator's versioned API where it gets authorization, audit and CLI parity, and where losing it does not mean losing the node.

A reviewer should treat a new endpoint here that is not required by the real xLights client as merge-blocking, and should ask which xLights code path calls it.

### 4. It is unauthenticated, as an accepted risk, with three bounds in code

The node demands no credential. xLights sends HTTP Basic or Digest credentials when configured, which RES-003 §9.8 records as unexamined, and building an authenticated path against an unexamined client behaviour would be guessing. Growing a real authentication surface here also contradicts the standing decision not to add security surface that was not asked for.

The risk is accepted because xLights is the only intended client, the show network is isolated, and the worst case is bounded: an unwanted writer can fill a disk, or leave a file that is registered nowhere and fails no hash it ever claimed, because a file only becomes an asset by passing through the store's own identity and hashing path.

Three bounds make that bounding real, and all three are code, not documentation:

- a **directory allowlist** of `sequences`, `music` and `videos`, with `effects` and everything else refused;
- a **per-file byte cap**;
- a **total asset-directory byte cap**.

Exceeding any bound, or exhausting the disk, registers nothing, removes the staging file, keeps every existing asset, and is reported as evidence.

### 5. Port 80 by capability, with a start-time bind override, and the byte caps are store-backed configuration

xLights contacts port 80 and the port is hardcoded in both the discovery and the upload URL (RES-003 §10.4). The node binds 80, granted by `AmbientCapabilities=CAP_NET_BIND_SERVICE` on its service unit. Moving the problem into a host firewall rule would relocate a ShowMesh behaviour into host state that is worse to debug on a show night.

`SHOWMESH_FPPCONNECT_LISTEN_ADDR` overrides the bind for dev stacks and tests. It is an allowed start-time setting under [ADR-039](ADR-039-operator-configuration-is-store-backed.md) decision 9's allow-list, and its stated reason is the same one ADR-039 decision 2 gives for a bind address: **the address a process listens on must be known before the process starts.** It is the only environment variable this feature adds.

The two byte caps fail that test in the other direction: the node can be told them after it is running. They are operator configuration and are therefore store-backed per ADR-039 decision 1, as a `fppconnect.settings` kind carrying `enabled`, `maxFileBytes` and `maxAssetDirBytes`, with an endpoint and matching `showmeshctl` verbs, pushed to the node over the existing command path.

### 6. `typeId` `0x7F` is provisional, `0x01` is refused, and upstream registration is a follow-up

xLights rejects every value at or above `0x80` that is not a registered third-party hardware class, and only `0x01` through `0x7F` reaches the `FPP_TYPE::FPP` branch that the chunked upload path requires. FPP's own header reserves that whole range for "FPP based" systems, so there is no value that is both eligible and unclaimed. `0x7F` is chosen as the value furthest from every growing FPP platform family base, recorded in code as provisional with its reasoning (RES-003 §10.2).

**`0x01` is refused.** It is FPP's live fallback for unrecognised hardware, so a real FPP on a new board reports it, and a ShowMesh node using it would be indistinguishable from one.

Getting a value allocated properly means pull requests to both FPP and xLights, and the xLights one is what grants eligibility. That is its own follow-up. It gates nothing here, and it is the honest long-term answer to a value ShowMesh currently squats.

### 7. Mode `player` and version `9.5.0` are deliberate compatibility choices

The node advertises `"Mode": "player"`, not `"remote"`, because xLights renders the playlist widget only for `player` or `master`, and the sparse-FSEQ default holds for every mode but `master`. `player` is the only mode that gets both.

It advertises `"Version": "9.5.0"` with explicit `"majorVersion": 9` and `"minorVersion": 5`: past the 7.1 eligibility gate, past the 7.0 and 9.3 FSEQ gates, and deliberately below 10.0 so xLights takes none of the branches a render node does not implement. Both values are recorded with their reasoning in RES-003 §10.5 and §10.6, and both change which xLights code path runs, so neither may be edited as a cosmetic detail.

### 8. An upload binds to a show by playlist name, and an unresolvable upload is held, never guessed

The node serves its ShowMesh show names at `GET /api/playlists`, so the playlist the operator picks in FPP Connect **is** the ShowMesh show. `POST /api/playlist/{name}` binds the named files to it. The join key between the bytes and the binding is the **file name with its extension**, which xLights sends identically as `Upload-Name` and as the playlist entry's `sequenceName`. The **file name stem** is the logical sequence.

xLights fires the playlist POST up to twice per target, the first time before any file exists, so the operation must be idempotent.

With no playlist chosen, the upload binds to the active show the coordinator pushed to the node. Anything unresolvable, no active show and no playlist, or a typed playlist name that matches no show, **keeps the file, registers nothing, and is reported as an unbound held file the operator can claim.** Absent, null and empty stay three different things, and a wrong silent guess is worse than a visible unbound asset. xLights never inspects the POST status, so a rejection cannot be reported through it and ShowMesh must surface its own evidence.

### 9. A partial upload registers nothing

Stage, assemble, hash, rename, register, in that order. Any failure before the last step leaves nothing an operator could mistake for an asset, and nothing that a manifest, an inventory, or a dispatch could select. This is Track E's standing criterion applied to a new ingestion path, not a new rule.

### 10. No served content identifies the node as Falcon Player

No response body, header, JSON field, ping string field, HTML title, or log line may contain "Falcon Player" or otherwise assert that a ShowMesh node is that product. RES-003 §10.3 establishes at L1 that the FSEQ path never asks: the `/config.php` identity gate is on the Controllers tab and the model-push path, both deferred.

Speaking another project's protocol to interoperate with its tools is ordinary interoperability. Claiming its identity is not. A violation is merge-blocking.

## Consequences

- The agent gains its first listening TCP port, an eighth supervised goroutine, and a shutdown obligation ordered before the existing clean-shutdown path. Its failure must not take the agent down: a node that cannot bind the listener still renders, still answers MQTT, and reports the bind failure as evidence.
- Node deployment gains a capability grant on the service unit. A node running without it fails to bind 80 and must say so plainly rather than falling back to a port xLights will never contact.
- `fppconnect.settings` and `fppconnect.configure` are reserved in [`IDENTIFIER-REGISTER.md`](../build/IDENTIFIER-REGISTER.md), along with the `showmesh.node.fppconnect.config/v1` payload schema string. No schema version is taken: registration reuses the existing asset tables.
- The listener is deliberately invisible to the API conformance suite, so its correctness rests on the seam's own tests plus the bench. That asymmetry is stated rather than discovered.
- `0x7F` may collide if FPP allocates upward into `0x71` to `0x7F`. The mitigation is that the value is configuration-free but single-sourced and provisional, so changing it is a one-line change plus a re-pin of RES-003, and the upstream follow-up removes the exposure entirely.
- Nothing here is verified. Every claim is L1 source reading at two pinned commits, and the bench against the owner's real xLights (Track E seam FC4) is what moves it, or corrects it.

## Alternatives considered

**Put the listener's paths in `api/openapi.yaml`.** Rejected. They are another project's paths; publishing them would imply an operator-facing contract, a versioning promise, and a CLI parity obligation for endpoints no operator should call.

**Authenticate the upload endpoint.** Rejected for now. xLights' credential behaviour is unexamined (RES-003 §9.8), so any scheme would be guessing at the client, and the three bounds in decision 4 address the realistic exposure. If the bench shows xLights sends credentials the node can verify, that is a superseding record with evidence behind it.

**Bind a high port and redirect port 80 in the host firewall.** Rejected. It moves ShowMesh behaviour into host state that is invisible to `showmeshctl`, invisible to the node's own evidence, and worst to debug on a show night.

**Advertise `0x01`, or a free value in the `0xC0` band.** Both rejected. `0x01` is FPP's fallback for unknown hardware and is the strongest available identity claim; the `0xC0` band is the correct band by product type but xLights rejects every free value in it, so registering there would be visible in FPP and still invisible in FPP Connect.

**Advertise mode `remote`.** Rejected. It keeps sparse rendering but loses the playlist widget, which is the only binding signal xLights' existing UI can carry, leaving an unsolicited upload with nothing but a filename to guess from.

**Defer the whole feature and keep manual upload only.** Not rejected, and it remains true that manual upload is a permanent first-class path and that a node extracts its own channel window from a full-show FSEQ, so the dress rehearsal needs none of this. This record exists because the owner chose to build it, and its bounds are what make that safe.

## Related decisions and work

- [ADR-013](ADR-013-no-fpp-control-port-sharing.md): ShowMesh must not share the FPP control port with a running `fppd`. Unaffected. A render node runs no `fppd`, so it binds the MultiSync UDP port normally with no port sharing, and this record adds a TCP listener beside it rather than changing that.
- [ADR-024](ADR-024-identity-authorization-and-audit.md): identity, authorization and audit for the write surface. It governs the coordinator, not agents, which is why decision 4 exists rather than being answered by it. Nothing here weakens it: the coordinator's write surface is unchanged, and the node registers its ingested file through that surface with its own credential.
- [ADR-028](ADR-028-show-asset-store-and-identity.md): the show asset store and the rule that a filename is not an asset identity. This is one more ingestion path into that model, not a redesign of it. Any pressure to reshape the identity model to fit an upload is a defect in the seam.
- [ADR-039](ADR-039-operator-configuration-is-store-backed.md): operator configuration is store-backed and the environment holds only what must precede the process. Decision 5 applies its test in both directions.
- [ADR-026](ADR-026-renderer-surface-model-and-reference-transport.md) and [ADR-027](ADR-027-show-and-surface-model.md): the surface model whose 1-based channel range becomes the 0-based advertised string.
- [RES-003](../research/RES-003-xlights-fpp-connect-compatibility.md) §9 and §10: every fact this record rests on, with permalinks and pins.
- [RES-017](../research/RES-017-fseq-format.md) §6.1 and OI-1: the 0-based file format the advertised string must agree with.
- [`TRACK-E-FPP-CONNECT.md`](../build/TRACK-E-FPP-CONNECT.md): the seams that implement this, and the bench that verifies it.

## Supersession

This record supersedes nothing. It narrows nothing. It answers questions [`TRACK-E-FPP-CONNECT.md`](../build/TRACK-E-FPP-CONNECT.md) listed as open and RES-003 §9 left unanswered, and it adds one constraint the architecture did not previously carry: the agent's inbound HTTP surface is singular, bounded, and not an API.
