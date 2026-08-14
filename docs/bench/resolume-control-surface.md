# Resolume control surface — bench capture

[Documentation index](../README.md) · [Track D](../build/TRACK-D-resolume.md) · [RES-001](../research/RES-001-resolume-smpte-behavior.md) · [ADR-003](../decisions/ADR-003-desired-and-observed-state.md) · [ADR-011](../decisions/ADR-011-context-aware-observability.md) · [ADR-020](../decisions/ADR-020-control-api-shape-and-change-stream.md)

This is Track D's equivalent of [the FPP command vocabulary
capture](fpp-command-vocabulary.md), and it exists for the same reason. Step 8
captured FPP's real command list before a single ShowMesh command was named, and
that ordering immediately overturned four assumptions that read as entirely
plausible. Everything in [TRACK-D-resolume.md](../build/TRACK-D-resolume.md)'s
D1–D4 and everything in [RES-001](../research/RES-001-resolume-smpte-behavior.md)'s
control-and-observability section was reasoned from documentation and forum
excerpts. This is what a running Arena actually does.

**No adapter, package, or interface is designed here.** The next session designs
against this document rather than against expectations, which is the whole point
of writing it first.

**The timecode path is not in this capture** and is deliberately out of scope: it
needs LTC on a physical input and is D0's bench. Everything below was obtained
with no audio interface, no LTC generator, and no cable.

## Provenance

| | |
|---|---|
| Product | `{"name":"Arena","major":7,"minor":23,"micro":2,"revision":51094}`, read from `GET /api/v1/product` |
| Host OS | macOS 26.5.2, build 25F84 |
| Hardware | MacBook Pro (MacBookPro18,4), Apple M1 Max, 64 GB — **arm64** |
| REST API | Enabled in Preferences ▸ Webserver before this session. Confirmed from `Preferences/server.xml`: `enabled="1" port="9080" address="0.0.0.0"`. **Port 9080, not the 8080 named in RES-001** — the port is configuration, not a constant. |
| OSC | Confirmed live with `lsof`: Arena holds `UDP *:7000`. `Preferences/osc.xml` reads `<Input Enabled="1" Port="7000"/>` and `<Output enabled="1" sendBundles="0" targetType="4" targetPort="12000" targetAddress="…"/>`. |
| Composition | The operator's `Christmas 25` — 18 layers in 3 layer groups, 14 columns, 3 decks, 252 clip slots of which 30 are non-empty |
| Captured | 2026-08-14 |
| Tooling | `curl` 8.7.1, Node 22.22.3 (built-in `WebSocket` and `dgram`; no third-party OSC or WS library) |

**This is not the deployed show machine, and that limit is load-bearing.**
[TRACK-D-resolume.md](../build/TRACK-D-resolume.md) and
[RES-001](../research/RES-001-resolume-smpte-behavior.md) both describe the
Resolume host as a Hackintosh that may move to Windows. This capture ran on the
owner's arm64 laptop. **The protocol and schema findings below should hold
anywhere the same Arena build runs; every timing number is this machine's and must
be re-measured on the show host before any deadline is set from it.**

Evidence level: **L2 for Arena 7.23.2 on this host.** Protocol shape, address
spaces, identity semantics and state schema are bench-verified. Nothing here is L3
— no ShowMesh code has been integrated against it.

**Changes made to the operator's installation, and their restoration.** Driving
Arena was authorised for this capture. Clips were connected and disconnected;
one layer was added and deleted; layer 1's `master`, `bypassed` and
`transition.duration` were changed and restored; one clip's `transporttype` was
set to `SMPTE 1` and restored to `Timeline`; Arena was restarted four times;
`Preferences/osc.xml`'s OSC output target was pointed at loopback for §6.3 and
restored byte-for-byte from a backup. `Compositions/Christmas 25.avc` was backed
up and is **untouched** — its mtime is still 2025-12-20, because Arena never wrote
it. Final state was verified: 18 layers, 0 connected clips, layer 1 at
`master 0.9173828125` / `bypassed false` / `transition 2.5`, deck `Main` selected.

---

## 1. The finding that governs the adapter's addressing

**OSC cannot address a pinned clip. Its default address space is positional
only.** REST can, natively and with no operator setup.

This is the exact inverse of the split
[TRACK-D-resolume.md](../build/TRACK-D-resolume.md) is currently built on, which
names OSC as the acting transport *and* requires pinned addressing for clips
"because offering both means someone eventually uses the fragile one."

The evidence is a clean A/B, each run from a disconnected baseline:

```
POST /composition/disconnect-all                            (baseline: nothing connected)
OSC  /composition/objects/1765396769079/connect  ,i 1   ->  layer 1 active clip: none
POST /composition/disconnect-all
OSC  /composition/layers/1/clips/2/connect       ,i 1   ->  layer 1 active clip: 'Solid Color'
```

`1765396769079` is the real, current object id of that clip — the same integer
REST answers on at `/composition/clips/by-id/1765396769079`, and the same integer
the operator's own `Shortcuts/DMX/Christmas 25v1.xml` contains as
`/composition/objects/1765396769079/connect`. Four other plausible spellings were
tried and none did anything:

```
/composition/clips/1765396769079/connect     -> no effect
/composition/by-id/1765396769079/connect     -> no effect
/composition/objects/1765396769079/connect   -> no effect
/objects/1765396769079/connect               -> no effect
/composition/object/1765396769079/connect    -> no effect
```

Confirmed independently from the other direction in §6.3: with "Output All
Messages" active, Arena emitted **1,545 distinct OSC addresses in 22 seconds and
not one of them was an `objects/<id>` form.** The whole outbound address space is
positional or `selected…`-relative.

### 1.1 Why the pinned form exists in the shortcut file but not on the wire

Resolume's pinning is a **shortcut-system** feature, not an OSC-protocol feature.
The operator's DMX preset records both forms side by side, and the distinction is
explicit in the file:

```xml
<ShortcutPath name="InputPath" path="/composition/layers/12/clips/3/connect"  translationType="1" …/>
<ShortcutPath name="InputPath" path="/composition/objects/1765224917762/connect" translationType="2" …/>
<ShortcutPath name="InputPath" path="/composition/objects/1733936639375/video/opacity" translationType="2" …/>
<ShortcutPath name="InputPath" path="/composition/groups/3/disconnectlayers"  translationType="1" …/>
```

`translationType="1"` is positional, `translationType="2"` is pinned. A pinned
*OSC* trigger therefore exists only as an operator-authored binding from an
arbitrary incoming address to a pinned target, stored in
`Shortcuts/OSC/<preset>.xml`. Three consequences:

- **ShowMesh cannot derive a pinned OSC address from composition state.** The
  incoming address is whatever the operator typed or learned. Nothing in REST,
  the WebSocket, or the OSC output exposes it.
- **There is no API for shortcuts.** The served OpenAPI spec has no shortcuts,
  bindings, or presets path; §2 lists every path it does have.
- **The bindings are per preset and the active preset is switchable.**
  `Shortcuts/activePresets.xml` names one active preset per input type. On this
  installation the active OSC preset is recorded as `OutputAllMessages`, and
  `Shortcuts/OSC/` contains only `Default.xml`, which declares an empty
  `ShortcutManager` — zero OSC bindings exist. Whether `OutputAllMessages` is a
  built-in with no backing file was not determined.

**Confirmed by the owner 2026-08-14** against his own installation, and he adds the
contrast that makes it a Resolume design choice rather than an oversight: **MIDI and
Art-Net/DMX can pin, OSC cannot.** His read of the consequence is the same as this
document's, which is that REST is the way ShowMesh should drive clips.

**What this means for the adapter, stated as a finding rather than a design:** the
identity-safe path and the low-latency path are not the same transport. REST
`by-id` is identity-safe and needs nothing from the operator. OSC is positional,
drifts on reorder, **and resolves to a different clip on every deck** (§9.4),
unless the operator hand-authors and maintains one binding
per target that ShowMesh cannot see, verify, or discover.

---

## 2. What the REST API actually exposes

### 2.1 The app serves its own OpenAPI spec, and the built-in web UI links to it

The webserver root serves a landing page linking to three built-in apps:

| Path | What it is |
|---|---|
| `/api/docs/rest/` | Swagger UI |
| `/api/docs/rest/swagger.yaml` | **OpenAPI 3.0.1, 216,828 bytes, 6,538 lines** — `title: Arena & Avenue REST API`, `version: 0.0.1`, `servers: [/api/v1]` |
| `/api/docs/example/` | A React app that drives the WebSocket; §5's protocol was read from its bundle |
| `/api/docs/triggered/` | A clip-overview app |

**The spec is served by the running application, so it is version-matched to the
binary by construction.** That is a materially better position than
[RES-003](../research/RES-003-xlights-fpp-connect-compatibility.md)'s or Step 8's:
there is a machine-readable contract to generate or verify types against, in the
same spirit as [ADR-015](../decisions/ADR-015-typescript-spa-frontend.md) does for
ShowMesh's own API. It is **not** a substitute for this capture — §2.4, §3.2, §4.4
and §8 each record a place where the spec's prose is wrong, stale, or silent about
behaviour that matters.

### 2.2 Three addressing schemes, and only two of them are identities

| Scheme | Example | Stable? |
|---|---|---|
| Positional | `/composition/layers/1/clips/2` | No — index, drifts on reorder |
| Object id | `/composition/clips/by-id/1765396769079` | **Yes**, across reorder *and* across restart (§3) |
| Selection-relative | `/composition/layers/selected`, `/composition/clips/selected` | No — follows the GUI selection |
| Parameter id | `/parameter/by-id/1786724946918` | **Session-scoped. Changes on every restart** (§3.2) |

`by-id` exists for clips, layers, layergroups, columns and decks. Every
`by-id` resource carries the same operations as its positional twin.

### 2.3 The path inventory

`GET`, `PUT`, `POST` and `DELETE` across 154 paths. The families:

| Family | Paths | Notable operations |
|---|---|---|
| api | `/product`, `/effects`, `/sources` | `/product` is 64 bytes and is the cheapest liveness probe |
| parameter | `/parameter/by-id/{id}`, `…/reset` | `GET`/`PUT` any parameter in the composition by its id |
| composition | `/composition`, `/composition/action`, `/composition/disconnect-all`, effects, `{parameter}/reset` | |
| column | `{index}` and `by-id`: get, put, delete, duplicate, **connect**, select | |
| layer | `{index}`, `by-id`, `selected`: get, put, delete, duplicate, add, select, **clear**, **clearclips**, effects | |
| layergroup | `{index}`, `by-id`, `selected`: …, `move-layer`, `add-layer`, `columns/{i}/connect` | |
| deck | `{index}`, `by-id`: get, put, delete, duplicate, add, select, **open**, **close** | |
| clip | positional, `by-id`, `selected`: get, put, **connect**, select, open, openfile, clear, thumbnail, `layers/{i}/clips/active` | |

**Four things that are not there, all of which RES-001 or Track D assume or imply.**

- **No `/composition/open`.** RES-001 lists it among confirmable operations. There
  is no path in the spec that loads a composition file, and none was found. **A
  composition cannot be loaded over REST in 7.23.2.**
- **No output snapshot.** RES-001 lists `snapshot.png`. What exists is per-clip
  thumbnails (`…/thumbnail`, `…/thumbnail/{last-updated}`) and
  `/composition/thumbnail/dummy`. There is no rendered-output image endpoint, so
  nothing here serves [RES-010](../research/RES-010-projection-preview-monitoring.md)'s
  preview need.
- **No collection endpoints.** `/composition/layers`, `/composition/columns`,
  `/composition/decks` and `/composition/layergroups` all return 404. The only way
  to enumerate is the full composition read, which is why §4.1's size matters.
- **No shortcuts, bindings or presets** (§1.1).

### 2.4 Everything is unauthenticated, CORS is wide open, and it binds `0.0.0.0`

```
HTTP/1.1 200 OK
Access-Control-Allow-Origin: *
```

No `WWW-Authenticate`, no cookie, no token, no key. `server.xml` binds
`address="0.0.0.0"`. **Anything on the show LAN can drive Resolume completely,
including `DELETE` on layers and `POST /composition/action` with `undo`.**

This is recorded as a fact about the target, not a criticism, but it bears on
[ADR-024](../decisions/ADR-024-identity-authorization-and-audit.md): ShowMesh's own
authorization can gate *ShowMesh's* commands to Resolume and cannot gate anything
else, so a scoped refusal in ShowMesh is not a control on the device. Any claim
that ShowMesh controls who can change the wall would be false.

### 2.5 Error shapes

Errors are **plain text, not JSON**, and echo the request path:

```
GET  /composition/layers/999             -> 404  The requested resource '/api/v1/composition/layers/999' was not found on this server.
GET  /composition/layers/0               -> 404  (indices are 1-based)
GET  /composition/clips/by-id/1          -> 404
POST /composition/clips/by-id/1/connect  -> 404
PUT  /parameter/by-id/999999             -> 404
```

Unlike FPP, **a command against a target that does not exist does fail loudly.**
That removes one entire class of the Step 8 hazard. It does not remove the other
— see §2.6.

### 2.6 One 204 that meant nothing, and one that means less than it looks

`POST /composition/clips/by-id/{id}/connect` takes an optional boolean body. The
spec explains it: *"This is analogous to whether the mouse is pressed down on the
clip. If omitted, true and false are both sent — as if a short click was
generated."*

```
POST /composition/clips/by-id/1765224917762/connect   body: false   -> 204
   clip state 1.5 s later: still Connected
```

**`false` is mouse-up, not disconnect.** It returns 204 and does nothing, and the
name reads exactly like a disconnect. This is FPP's *"200 means only that its
dispatcher ran"* in a second vendor's clothes, and it is reachable by anyone who
reasons that `connect true` has an opposite.

What actually disconnects, all verified:

| Intent | Call | Result |
|---|---|---|
| Stop one layer | `POST /composition/layers/by-id/{id}/clear` | layer's active clip gone |
| Stop everything | `POST /composition/disconnect-all` | all layers' active clips gone |
| Replace on a layer | connect another clip on the same layer | previous clip released |

Separately, and recorded because it happened rather than because it reproduced:
**one `POST …/connect` returned 204 and left the clip disconnected.** It occurred
immediately after a layer had been added and an OSC trigger fired at the shifted
index, and it did **not** reproduce in five consecutive clean attempts (5/5
Connected) or in any later run. Cause unknown. It is recorded because ADR-003's
rule is the correct handling either way: **a 204 is an acknowledgement that the
request was accepted, never evidence that the composition changed.**

---

## 3. Identity across the three transports

### 3.1 One identity, three path spellings

The pinned identifier is a single integer per object, and it is the same integer
everywhere it appears. What differs is the path built around it, and the
divergence is not cosmetic:

| Concept | REST | OSC / shortcut path | WebSocket |
|---|---|---|---|
| Clip by identity | `/composition/clips/by-id/1765396769079` | `/composition/objects/1765396769079/…` — **shortcut bindings only, not the OSC wire** (§1) | `id` field on the clip object in the full state push |
| Clip by position | `/composition/layers/1/clips/2` | `/composition/layers/1/clips/2/…` | position in the `layers[].clips[]` arrays |
| Layer group | `layergroups` | **`groups`** | `layergroups` |
| Disconnect all | `/composition/disconnect-all` | **`/composition/disconnectall`** | `{action:"post", path:"/composition/disconnect-all"}` |
| Parameter | `/parameter/by-id/1786724946918` | not addressable | `{action:"subscribe", parameter:"/parameter/by-id/1786724946918"}` |

So they are **the same identity under three names for the object, and a different
identity space entirely for parameters.**

### 3.2 Object ids survive; parameter ids do not

Tested by snapshotting ids, restarting Arena, and comparing:

```
layer  'Layer #'  object id 1765224917300 -> 1765224917300   SAME
                  master parameter id 1786724944352 -> 1786726562641   CHANGED
layer-1 clips     object ids identical              14/14
                  connected-parameter ids identical  0/14
composition       master parameter id 1786724934857 -> 1786726553663   CHANGED
decks             ids identical                      3/3
```

**Object ids are persisted in the composition file. Parameter ids are minted per
session.** The `1765…`/`1733…` object ids are creation timestamps preserved across
sessions; the `1786…` parameter ids are allocated at load.

This is a hard constraint on any observation design, because §5 establishes that
the WebSocket's *only* narrow subscription mechanism is keyed on parameter id.
**Every subscription is invalidated by an Arena restart, and nothing announces
it** — subscriptions to dead ids simply never fire. A parameter id may not be
persisted, cached across a reconnect, or written into configuration. It must be
re-resolved from a fresh full composition read every time the connection is
re-established, which is the same shape as
[ADR-020](../decisions/ADR-020-control-api-shape-and-change-stream.md)'s
non-resumable-stream rule: after any interruption, re-fetch an authoritative
snapshot.

### 3.3 Reorder test

A layer was inserted at index 1, shifting the whole composition, and then deleted.

| | before | after insert |
|---|---|---|
| index 1 | id `1765224917300`, clip 1 = `Virtual Matrix` | id `1786724953484` (new, empty) |
| index 2 | id `1765224916883` | id `1765224917300`, clip 1 = `Virtual Matrix` |
| `GET /composition/clips/by-id/1765224917762` | `Virtual Matrix` | **`Virtual Matrix`** |
| `GET /composition/layers/by-id/1765224917300` | clip 1 = `Virtual Matrix` | **clip 1 = `Virtual Matrix`** |
| `OSC /composition/layers/1/clips/1/connect` | launches `Virtual Matrix` | **launches the new empty layer's empty slot** |

**`by-id` resolves to the same object after reorder. The positional path — which
is the only OSC path (§1) — silently addresses a different thing.** Deleting the
added layer restored the composition exactly.

---

## 4. The full composition read

### 4.1 Size, and why it decides the polling question

`GET /api/v1/composition` on this composition:

```
2,258,982 bytes idle   ·   up to 2,272,644 bytes with a clip connected
```

**Half of that payload is a duplicate.** Every layer appears twice: once in the
top-level `layers` array and again nested inside `layergroups[].layers`, with an
identical id set.

```
total /composition               2,258,982 bytes
  'layers' array                 1,105,878
  'layergroups' array            1,140,786
    of which nested layer copies 1,105,878   = 49% of the whole payload
```

252 clip slots exist; 30 are non-empty. The rest are fully-populated empty-clip
objects.

**There is no partial, filtered, or projected read.** No `fields`, no `depth`, no
collection endpoint (§2.3). The cheap reads are per object:

| Endpoint | Bytes |
|---|---|
| `/product` | 64 |
| `/composition/decks/{i}` | 358 |
| `/composition/columns/{i}` | 418 |
| `/composition/clips/by-id/{id}` | 6,818 |
| `/composition/layers/by-id/{id}` | 62,795 |
| `/composition/layergroups/{i}` | 223,957 |
| `/composition` | **2,258,982** |

So polling for state is viable **only** against known object ids. Polling
`/composition` to discover what changed is not: at 1 Hz it is 2.2 MB/s of JSON
encode on the render host.

### 4.2 The payload is not byte-stable

Repeated reads of an idle, unchanging composition returned sizes
between 2,258,986 and 2,258,990 bytes. The variation is float serialisation —
`transport.position`, `speed`, `transition.phase` — which continues to move while
nothing is playing. **A client cannot use payload size, an ETag, or a content hash
to detect that the composition changed.** Nothing in the response carries a
revision, a generation counter, or a modified timestamp.

### 4.3 The parameter envelope

Every leaf is an object, never a bare value:

```json
{"valuetype":"ParamBoolean","id":1786724944353,"value":false}
{"valuetype":"ParamRange","id":1786724944352,"min":0.0,"max":1.0,"in":0.0,"out":1.0,"value":0.917}
{"valuetype":"ParamChoice","id":1786724944348,"value":"None","index":0,"options":["None","A","B"]}
{"valuetype":"ParamState","id":1786724946918,"value":"Disconnected","index":1,
 "options":["Empty","Disconnected","Previewing","Connected","Connected & previewing"]}
{"valuetype":"ParamString","id":1786724944345,"value":"Layer #"}
{"valuetype":"ParamTrigger","id":1786724944341,"value":true}
```

Observed valuetypes: `ParamBoolean`, `ParamRange`, `ParamChoice`, `ParamState`,
`ParamString`, `ParamTrigger`, `ParamNumber`, `ParamEvent`.

**`options` is carried inline on every choice and state parameter, and it is not
constant across objects of the same kind** — see §8.1, where two clips in the same
composition advertise different `transporttype` option lists. Anything that
hard-codes an enum from one sample will be wrong on another object.

`connected` is a five-state `ParamState`, not a boolean:
`Empty | Disconnected | Previewing | Connected | Connected & previewing`. Column
`connected` is a *different* three-state set: `Empty | Disconnected | Connected`.
A predicate written as `== "Connected"` misses `Connected & previewing`.

### 4.4 A `404` that means "nothing is playing"

`GET /composition/layers/{ref}/clips/active` returns the active clip, or 404. The
spec's own wording is *"Either the requested layer does not exist, or it does not
have an active clip"* — **one status code for two conditions that mean opposite
things to a readiness check.** "The layer you asked about is gone" and "the layer
is fine and idle" are indistinguishable from the response.

The unambiguous read is `layers[i].active_clip` in the composition or the layer
object: an object when something is playing, JSON `null` when nothing is. That is
**`null`, not an absent key** — the shape this project has already been caught by
once, on FPP's `"ma": null` producing a measured 0 mA.

---

## 5. WebSocket

`ws://<host>:9080/api/v1`. Same port, no separate listener, no handshake beyond
the upgrade.

### 5.1 On connect it pushes the entire composition, unsolicited and untyped

```
t=  0 ms  open
t= 67 ms  2,258,572 bytes   <- the whole composition, with NO "type" field
t= 68 ms  4,714 bytes       {"type":"sources_update","value":…}
t= 68 ms  11,017 bytes      {"type":"effects_update","value":…}
```

The composition push is distinguished from every other message by the *absence* of
`type`. The bundled example app discriminates exactly that way — `typeof e.type
!== "string"`, then check for `columns` and `layers`. A client keying off a
message type will never see the only message that carries the composition.

Idle for 12 seconds after that: **nothing.** No heartbeat, no keepalive, no ping.
So a silent connection and a dead connection look identical, and only the TCP
layer distinguishes them.

### 5.2 The protocol

Read from `/api/docs/example/static/js/main.*.js`, which the application serves.
Client to server:

```json
{"action":"subscribe",   "parameter":"/parameter/by-id/1786724946918"}
{"action":"unsubscribe", "parameter":"/parameter/by-id/1786724946918"}
{"action":"set",         "parameter":"/parameter/by-id/1786724946918", "value": …}
{"action":"reset",       "parameter":"/parameter/by-id/1786724946918"}
{"action":"post",        "path":"/composition/clips/by-id/…/connect", "body": …}
{"action":"remove",      "path":"…"}
```

Server to client: the untyped composition, plus `parameter_subscribed`,
`parameter_update`, `parameter_get`, `parameter_set`, `sources_update`,
`effects_update`, `thumbnail_update`.

**`post` and `remove` mean the WebSocket is a full command channel**, not merely an
observation one — every REST write is reachable over it without a second
connection.

### 5.3 Subscribing narrowly does not stop the firehose

This is the finding that decides how much the WebSocket costs.

| Change | Narrow `parameter_update` | Full composition push |
|---|---|---|
| Set a `ParamRange` (layer master) | **yes**, 160 bytes | **no** |
| Connect a clip | yes, 233 bytes | **yes, ~2,272,000 bytes** |
| Clear a layer | — | **yes** |
| `disconnect-all` | yes | **yes** |
| Select a deck | — | **yes** |

Measured with a client subscribed to **zero** parameters: every clip connect,
layer clear and disconnect-all still delivered the full composition. Measured with
a client subscribed to exactly **one** parameter: it received its 233-byte update
*and* the full 2.27 MB push 108 ms later.

**So the operations ShowMesh most needs — launch a clip, clear a layer, blackout —
each push ~2.27 MB to every connected WebSocket client, and there is no way found
to suppress it.** Plain parameter changes are cheap; structural changes are not.

### 5.4 `parameter_update` is reliable, and a mid-capture reading that said otherwise was wrong

Recorded because the error is instructive and because the corrected number is the
one the adapter needs.

An initial 16-operation tally showed **zero** `parameter_update` messages for a
subscribed `connected` parameter, which read as a dropped-event channel. It was
not. The clip's `connected` value had not changed at the sample points, for the
reason §7.2 sets out: layer 1 has a 2.5-second transition, and a clip stays
`Connected` for the whole fade-out, so a disconnect-then-reconnect inside that
window is genuinely a no-op on that parameter. The harness was measuring its own
too-short wait.

Re-run with transition-aware waits, 12 operations:

```
connect         6/6 emitted a parameter_update matching the REST value
disconnect-all  5/6 emitted one; the sixth was a no-op (already disconnected)
full pushes    12/12
```

**No drops.** The lesson is the repo's own, in a new subsystem: a test that samples
faster than the system settles reports a defect that is not there, as confidently
as one that samples slower reports success that is not there.

### 5.5 Through an Arena restart

```
t=0.02s  open
t=0.06s  FULL 2,272,212 bytes  name='Christmas 25' layers=18
t=3.08s  close  code=1006  wasClean=false
```

**Close code 1006, no close frame.** Arena going away is indistinguishable from
the network dropping. Combined with §5.1's absent heartbeat and §3.2's invalidated
parameter ids, the required client behaviour is exactly ADR-020's: treat any
interruption as a total loss of knowledge and re-fetch an authoritative snapshot.

---

## 6. OSC

### 6.1 Input

UDP 7000, confirmed live with `lsof` rather than assumed. Positional addresses
only (§1). Argument typing is permissive: `,i 1`, `,f 1.0`, `,T`, and **an empty
argument list** all fired a clip launch identically. An unrecognised address
produced no effect and no error.

### 6.2 OSC produces no reply. Ever.

Tested directly, which the brief asked for and which
[TRACK-D-resolume.md](../build/TRACK-D-resolume.md) assumes: every OSC message was
sent from an explicitly bound UDP socket which then listened for 3 seconds.
**No datagram was ever returned to the sender** — not for an address that worked,
not for one that did nothing, not for RES-001's `"?"` query form.

**Track D's assumption is confirmed, and it is stronger than "fire and forget".**
Resolume's outbound OSC is not a response mechanism at all: it goes to a
**preference-configured target address and port** (`osc.xml`'s
`targetAddress`/`targetPort`), unconditionally, with no relationship to who sent
anything. On this installation that target is a host on a different subnet from
the machine's own address, so the stream was going nowhere reachable and nobody
would have noticed.

Two consequences worth stating plainly. Confirmation cannot come from the OSC send
— that half was already Track D's position. And **the OSC output stream is not a
per-client channel**: pointing it at ShowMesh means pointing it away from whatever
else the operator had configured, because there is one target.

### 6.3 The outbound address space, and what it costs

Captured by temporarily pointing `osc.xml`'s output target at `127.0.0.1:12000`
and decoding the datagrams (restored afterwards, §Provenance).

**1,545 distinct addresses in 22 seconds.** The shape:

```
/composition/layers/1/clips/1/connected          ,i 3      <- 3 = Connected
/composition/layers/1/clips/1/connected          ,i 1      <- 1 = Disconnected
/composition/layers/1/clips/1/connect            ,i …
/composition/layers/1/clips/1/transport/position ,f 0.41
/composition/layers/1/position                   ,f 0.8103
/composition/layers/1/transition/phase           ,f …
/composition/layers/1/video/transition/mixer/opacity ,f …
/composition/layers/1/smpte1quickselect          ,i 0
/composition/layers/1/smpte2quickselect          ,i 0
/composition/columns/2/connected                 ,i …
/composition/groups/3/columns/9/connected        ,i …
/composition/decks/2/selected                    ,i …
/composition/disconnectall
/composition/selectedlayer/…  /composition/selectedclip/…  /composition/selectedcolumn/…
```

Three facts that a decoder needs:

- **The integer on a state address is the `ParamState` option index** from §4.3.
  `3` is `Connected`, `1` is `Disconnected`. It is not a boolean and the mapping is
  only recoverable from the REST `options` array.
- **`connect` and `connected` are both emitted** — the action and the state share a
  path prefix and differ by five characters.
- **Layer groups are `groups`, and disconnect-all is `disconnectall`.** Neither
  matches the REST spelling.

Measured rate, on this machine:

| Condition | Datagrams/s | KB/s |
|---|---|---|
| One clip playing | 481 | 23.6 |
| **Nothing connected at all** | **236** | **13.4** |

**"Output All Messages" streams continuously whether or not anything is
happening**, because playhead, transition-phase and mixer-opacity addresses keep
emitting. It is a monitoring firehose, not an event feed.

Also emitted: 39 messages with an **empty address string** (`,f 0.5`), and bare
rootless addresses `/name`, `/colorid`, `/beatsnap`, `/cliptarget`,
`/ignorecolumntrigger`. Cause not determined; a strict OSC parser should be
expected to encounter them.

### 6.4 What OSC input was confirmed to drive

| Action | Address | Result |
|---|---|---|
| Launch a clip | `/composition/layers/{L}/clips/{C}/connect` | confirmed |
| Select a deck | `/composition/decks/{D}/select` | confirmed — `Main` → `Rest Staging` |

Column connect, layer clear and group operations appear in the outbound address
space and were **not** exercised as inputs.

---

## 7. Confirmation latency

Fire an OSC clip launch; measure when the change becomes visible. Eight runs,
alternating between two clips, REST polled at 20 ms.

| Channel | min | median | max |
|---|---|---|---|
| REST `GET /composition/clips/by-id/{id}` | **4 ms** | 35 ms | 64 ms |
| WebSocket `parameter_update` (233 B) | 15 ms | 33 ms | 35 ms |
| WebSocket full composition (2.27 MB) | 102 ms | 121 ms | 134 ms |

For comparison, Step 8's equivalent measurement moved FPP confirmation from a
13–15 second window to 0.55 s. **A clip launch here confirms in tens of
milliseconds**, on this machine, on loopback.

### 7.1 That number is only true for connect

### 7.2 Disconnect confirms one layer-transition later, and the transition is composition configuration

`disconnect-all` on a layer with a 2.5-second transition:

```
run 1  Disconnected first observed at 2613 ms
run 2                                 2593 ms
run 3                                 2581 ms
run 4                                 2585 ms
```

Causation was proven rather than inferred, by driving the layer's own
`transition.duration` parameter and re-measuring:

| `layers[1].transition.duration` | time to observe `Disconnected` |
|---|---|
| 0.0 s | **75 ms** |
| 0.5 s | **531 ms** |
| 2.5 s | **2,527 ms** |
| 5.0 s | **4,068 ms** |

**A clip remains `Connected` for the whole fade-out.** The 5.0 s row came in
below its transition because the clip had not finished fading *in* when the
disconnect was issued — the delay is the remaining fade, so the transition
duration is an upper bound and the actual delay depends on where the fade was.

Three things follow, and they are findings rather than proposals:

- **A single fixed confirmation deadline is wrong.** Connect confirms in tens of
  milliseconds; disconnect and blackout confirm in up to the layer's transition
  duration plus ~85 ms. On this composition that is a **35× spread** between two
  operations on the same object.
- **The bound is readable**, at `layers[i].transition.duration` in state the
  adapter reads anyway. It is per layer, so a blackout across 18 layers is bounded
  by the slowest of them.
- **This is a second disguise of Step 7's 179-microsecond defect, pointing the
  other way.** There, evidence pre-dating dispatch was accepted as confirmation.
  Here, evidence post-dating dispatch is *correct* and still reads `Connected` for
  seconds — so a deadline set from the connect measurement would report
  `unconfirmed` for a blackout that worked perfectly.

---

## 8. Layer active state

**There is no `active` field on a layer.** [Track
D](../build/TRACK-D-resolume.md) requires layer-active state in pre-show readiness
evidence because an inactive layer is a silent failure in the timecode path. What
exists instead:

| Field | Type | Meaning |
|---|---|---|
| `layers[i].bypassed` | `ParamBoolean` | layer excluded from the mix |
| `layers[i].solo` | `ParamBoolean` | |
| `layers[i].master` | `ParamRange 0–1` | layer level |
| `layers[i].video.opacity` | `ParamRange` | |
| `layers[i].crossfadergroup` | `ParamChoice None/A/B` | |
| `layers[i].active_clip` | object or **`null`** | what is playing, or nothing |
| `layers[i].transition.duration` | `ParamRange` | §7.2's bound |
| `layers[i].ignorecolumntrigger`, `faderstart`, `autopilot.target`, `maskmode` | | |
| `layergroups[g].bypassed`, `.master`, `.solo` | | the containing group |
| composition `bypassed`, `master`, `video.opacity` | | |

### 8.1 `connected` is not evidence that anything reached the output

Tested directly, and this is the silent failure Track D names, in its readable
form:

```
set layers[1].bypassed = true
connect clip 'Virtual Matrix'          -> 204
read clip:  connected = "Connected"    <- on a bypassed layer
read layer: active_clip is present     <- on a bypassed layer

set layers[1].master = 0.0
connect clip                           -> 204
read clip:  connected = "Connected"    <- at zero level
```

**A clip on a bypassed layer, and a clip on a layer at zero master, both report
`Connected` with an `active_clip` present, and nothing on the clip says
otherwise.** So a readiness check built on `connected` or `active_clip` alone
reports healthy while the wall is dark — which is precisely the failure mode Track
D wants readiness evidence to catch.

The readable evidence for "this layer can actually put something on the wall" is
the conjunction of `layers[i].bypassed == false`, `layers[i].master > 0`,
`layers[i].video.opacity > 0`, the containing layergroup's `bypassed`/`master`,
and the composition's `bypassed`/`master`. **All six are readable; none of them is
one field, and no field named `active` exists.**

Not determined: whether `crossfadergroup` assignment plus crossfader position can
silence a layer that passes all six checks. The crossfader is present in state
(`composition.crossfader`, 1,440 bytes) and was not exercised.

---

## 9. Pages, decks, and what a tripwire can actually count

Track D defers the page race and guards it with a tripwire that surfaces a
multi-page composition as visible evidence, buildable only if a page count is
readable in state the adapter already reads.

### 9.1 The word "page" does not appear anywhere in Resolume's composition state

Searched the full 2.26 MB payload: `page` and `Page` occur **zero** times.
`SMPTE` occurs 980 times, all as option strings.

### 9.2 What is readable is `decks`

```json
"decks": [
 {"id":1733100600915,"closed":false,"name":{…"value":"Main"},         "selected":{…"value":true}},
 {"id":1733100600921,"closed":false,"name":{…"value":"Rest Staging"}, "selected":{…"value":false}},
 {"id":1733100600927,"closed":false,"name":{…"value":"Downloads"},    "selected":{…"value":false}}
]
```

`composition.decks` is a top-level array in the read the adapter already needs,
carrying id, name, `closed`, and `selected`. Deck ids are stable across restart
(§3.2). Decks are drivable from both REST (`/composition/decks/{i}/select`,
`by-id`, `open`, `close`) and OSC (`/composition/decks/{D}/select`, confirmed
§6.4).

**So a count is readable and a tripwire is buildable — against decks.**

### 9.3 "Page" and "deck" are the same thing, settled by the owner 2026-08-14

The owner uses "page" for what Resolume calls a "deck" and will use the two
interchangeably. Documents should expect both words and mean `composition.decks`.

That settles the vocabulary. It does **not** leave Track D's page race where it
found it, because §9.4 measures the race and it is not the one Track D describes.

For the record, Resolume does have a second page-like concept that this
resolution rules out: shortcut presets (`Shortcuts/activePresets.xml`, §1.1), one
active per input type, switchable, and **invisible to every API**. Switching
presets changes what every incoming OSC address does. It is not decks, it is not
readable, and no tripwire against it is buildable.

### 9.4 The race is real, and it is a clip race, not a layer race

Track D describes the page race as affecting pinned **layer** commands. Measured,
it affects **clip** positions and leaves layers alone.

Selecting each deck in turn and re-reading `/composition`:

| Selected deck | `columns` | `layers[1].clips[]` object ids |
|---|---|---|
| `Main` | **14** | `…917762, …769079, 1734376590063, 1733100601011, 1733100601427, …` |
| `Rest Staging` | **9** | `…917762, …769079, 1765224917471, 1765224917502, 1765224917533, …` |
| `Downloads` | **9** | `…917762, …769079, 1734376596335, 1734376596336, 1734376596337, …` |

Three facts follow, and the third is the one that matters:

- **`/composition` returns only the selected deck's grid.** The column count is 14,
  9 and 9 on the three decks, and the layer's `clips` array is the same length.
  There is no way to read a deck that is not selected.
- **Layer identity is deck-independent.** `layers[1].id` is `1765224917300` on all
  three decks. The same 18 layers exist under every deck; a deck changes the clip
  grid, not the layer stack. So a positional *layer* command does not race the
  deck.
- **A positional clip path resolves to a different object on every deck.**
  `/composition/layers/1/clips/5` is `Green screen snowstorm` on `Main`, object
  `1765224917533` on `Rest Staging`, and object `1734376596337` on `Downloads`.

Combined with §1, that is the whole race in one sentence: **OSC can only address
clips positionally, and a positional clip address means a different clip on every
deck.** Nothing in the OSC send says which deck it assumed, nothing replies, and
deck selection is itself drivable from OSC (`/composition/decks/{D}/select`,
confirmed §6.4) and from REST, and from the operator's hand on the keyboard.

**REST `by-id` is immune.** Verified directly: with `Rest Staging` selected,
`GET /composition/clips/by-id/1765224917762` still returned `Virtual Matrix`, a
`Main` clip. A by-id reference does not need to know which deck is showing.

One detail is unexplained and is recorded rather than smoothed over: **clip
positions 1 and 2 returned the same object ids on all three decks**, while every
position from 3 upward differed. Whether those two columns are shared, group-owned,
or an artefact of this composition was not determined.

**What this does to the tripwire.** Counting decks still works and is still cheap,
but `Christmas 25` already has three decks, so "more than one deck exists" fires
immediately and tells the operator nothing. The condition that actually matters is
narrower and is now measurable: **a positional clip command was dispatched while
more than one deck exists**, or better, **the selected deck changed between the
decision and the confirmation**. `decks[i].selected` is readable in state the
adapter already reads, so both are buildable. Which one to build is a design
decision for the adapter session, not this capture.

---

## 10. Restart behaviour

Four restarts. Arena did not respond to the `quit` AppleEvent (timed out after
~192 s while the REST API stayed at 200 throughout, so the application was
healthy); `SIGTERM` terminated it immediately and cleanly every time.

### 10.1 The API returns in under 4 seconds — describing a composition that is not the show

Continuous polling from process launch, 150 ms interval:

```
t=0.00s  launch
         21 connection refusals
t=3.65s  first successful GET /composition   ->  name=''             layers=3   decks=3  columns=9   110,009 bytes
t=4.15s                                          name='Christmas 25' layers=3   decks=3  columns=9   110,080 bytes
t=4.85s                                          name='Christmas 25' layers=18  decks=3  columns=14  2,258,988 bytes
```

**There is a ~1.2 second window in which the REST API answers `200 OK` with a
complete, well-formed composition that is not the operator's show** — and for the
last 0.7 s of it, that composition carries the **correct name** with 3 of 18
layers. Nothing in the response says "loading". There is no readiness field, no
status, no generation counter.

So the obvious readiness check — poll until the API answers, then verify the
composition name — **passes during the window in which 15 layers do not exist.**
That is [ADR-011](../decisions/ADR-011-context-aware-observability.md)'s rule in a
new subsystem: this is evidence that is fresh, well-formed, and wrong, and the
only defence found is to check structure rather than liveness or name.

The default empty composition also has 3 decks, so deck count does not distinguish
it either.

### 10.2 Nothing is playing when it comes back

```
before restart:  clip 'Virtual Matrix' = Connected
after  restart:  connected clips: []   selected deck: 'Main'
```

**Arena reloads the saved composition and connects nothing.** This answers Track
D's acceptance criterion *"a Resolume restart mid-show recovers to a defined
composition state rather than an undefined one"* — the state is defined, and it is
**dark**. Resolume will not resume the show; whatever re-establishes playback has
to be ShowMesh or the operator.

Also confirmed: Arena did **not** write the composition file at any point across
four restarts. `Christmas 25.avc`'s mtime is unchanged from 2025-12-20, so every
in-session change was discarded on exit rather than persisted.

### 10.3 How often this actually happens, per the owner

**Once the show is up, Resolume runs 24/7.** The owner's position on 2026-08-14 is
that the only restarts are ones he performs himself, or ones caused by loading new
content as a new composition version from another computer.

That lowers the operational weight of §10.1's loading window without removing it,
because both remaining causes are **planned changes made shortly before or during a
show**, which is exactly when a readiness check is being read. It also moves the
sharper question elsewhere: a composition swap without a restart is the disruption
that actually recurs, and it is untested (§12).

---

## 11. Corrections to RES-001 and TRACK-D

Recorded as first-class results per the brief. Each is bench evidence against a
documented or assumed claim.

### 11.1 Overturned

| Claim | Where | What was measured |
|---|---|---|
| "OSC can switch clips onto SMPTE **where REST cannot**"; "SMPTE transport … **not modeled in REST**" | RES-001 §Control and observability | **False.** `PUT /parameter/by-id/{transporttype-id}` with `"SMPTE 1"` returned 204 and read back `SMPTE 1`. `transporttype` is a first-class `ParamChoice` in REST with options `Timeline, BPM Sync, SMPTE 1, SMPTE 2, Denon DJ, Pioneer DJ`. Restored to `Timeline`. |
| Pinned addressing is available to OSC; ShowMesh "should require pinned addressing for clips and should not offer positional clip triggering at all" | TRACK-D §Addressing | **Inverted.** OSC's address space is positional only (§1). REST has native `by-id`. The pinned form exists only as an operator-authored shortcut binding that no API exposes. |
| REST port 8080 | RES-001, Track D | Configuration, not a constant. This installation runs **9080** (`server.xml`). |
| `/composition/open` and output `snapshot.png` are confirmable REST operations | RES-001 §Control and observability | Neither path exists in 7.23.2's own served spec, and neither was found. Composition loading is not available over REST; only per-clip thumbnails exist. |

### 11.2 Confirmed, and now measured

| Claim | Status |
|---|---|
| REST + WebSocket since 7.8, base `/api/v1`, WS on the same port | Confirmed. |
| Resources addressable by index, by stable id, or `/selected` | Confirmed, and the stability is now quantified: **object ids survive restart, parameter ids do not** (§3.2). |
| "OSC is fire-and-forget UDP with no reply" | Confirmed directly (§6.2), and it is stronger than assumed: outbound OSC goes to one preference-configured target, unrelated to any sender. |
| `smpte1quickselect` / `smpte2quickselect` exist | Confirmed from Arena's own output — and they are **layer-scoped** (`/composition/layers/{n}/smpteNquickselect`), which is a different thing from a clip's `transporttype`. Semantics not determined. |
| "Output All OSC Messages" streams triggers and playhead position | Confirmed, and costed: **236 datagrams/s idle, 481 with one clip playing** (§6.3). |
| SMPTE is unavailable on clips with an audio track | Consistent with state: **244 of 252 clips offer SMPTE options; 8 do not**, and the 8 advertise only `Timeline, BPM Sync`. Not proven causal here — the audio-track correlation was not independently verified. |

### 11.3 Answered from RES-001's own open-item list

**Open item 2, "enumerate `transporttype` choices; test setting SMPTE transport via
OSC vs REST":** the choices are `Timeline, BPM Sync, SMPTE 1, SMPTE 2, Denon DJ,
Pioneer DJ`, they vary per clip, and REST sets them (§11.1). OSC was not tested for
this.

**Open item 4, "what REST returns for a SMPTE-transport clip's `transport`
object":**

```json
{"position":{"valuetype":"ParamRange","min":0.0,"max":5000.0,"value":1132.009},
 "controls": null}
```

`position` is retained with the clip's own duration bounds. **`controls` becomes
JSON `null`.** No offset, no delay, no lock status, no incoming timecode value —
which is consistent with the spec's own note that *"Only Timeline and BPM Sync
transport types are supported at the moment"*, a line that describes the
`transport` **sub-object's** schema and not, as RES-001 read it, the
`transporttype` parameter.

That `null` deserves naming. **This project has already been bitten once by
treating a JSON `null` as an absent key** — FPP's `"ma": null` decoding to a
plausible 0 mA on a blind port. A decoder that maps a missing `controls` to a zero
value here would manufacture playback controls for a clip that has none.

The good news for D0: **`transport.position` remains readable under SMPTE
transport**, which is the indirect evidence RES-001's decision section hoped for.
Whether it tracks incoming LTC is exactly what D0 must measure and is **not**
established here.

**What the writable side of this is actually for, owner's position 2026-08-14.**
ShowMesh has no reason to set SMPTE transport as a normal operation: Resolume
configuration belongs to the programmer, in Resolume, before the show. The value
of `transporttype` being both readable and writable is **drift detection** — the
composition itself states which clips are SMPTE-capable (244 of 252) and which are
currently on SMPTE (0 of 252 at capture time), so ShowMesh can tell an operator on
show day that a clip which should be following timecode is not, and offer to put it
back. That is a readiness check with a remedy attached, and it is a candidate for
the adapter rather than a decision made here. It depends on ShowMesh knowing which
clips are *supposed* to be on SMPTE, which is show configuration that does not
exist yet.

---

## 12. Do object ids survive a content update? Yes, and the composition has no identity at all

This section answers §10.3's follow-on, which the owner named as the disruption
that actually recurs: new content arriving as a new composition version from
another computer.

**Different evidence class from everything above.** `.avc` is plain XML, and object
ids are stored in it. So this was read from **six of the operator's own real
composition files** with no application behaviour exercised at all: no save, no
load, no GUI. It is stronger than reasoning from the id format and weaker than a
controlled save-and-reload, and it is honest about being an observational study of
artifacts that already existed.

### 12.1 Ids survive edits, re-saves, and a year-over-year rebuild

Shared `uniqueId` values between related composition files, broken down by the
element each id belongs to:

| Pair | Clips | Layers | Columns | Decks |
|---|---|---|---|---|
| `Haloween 2024.avc` → `Haloween 2024 pj mapping.avc` | **81** | 39 | 9 | 3 |
| `Christmas 24.avc` → `Christmas 25.avc` | **246** | 10 | 27 | 3 |

`Christmas 24` and `Christmas 25` are a year apart and a substantial rebuild, and
**246 clip ids still carry over.** In neither pair did a single shared id point at
a different *kind* of element in the two files, so an id is never recycled onto
another class of object.

**The failure mode is therefore loud, not silent.** A clip that gets replaced
loses its id, and a stored reference to it produces `404` on
`GET /composition/clips/by-id/{id}` (§2.5). That is a stale reference announcing
itself, which is the outcome this project wants and did not get from FPP.

**So storing a Resolume clip id in ShowMesh configuration is viable.** It was an
open question; it is now answered well enough to build on, with the caveat that a
controlled save-and-reload has still not been run.

### 12.2 Layer ids leak across shows, because shows are built from each other

`Halloween 2025.avc` and `Christmas 25.avc` are different shows and share 24 ids:
one `Composition`, two render/modifier objects, and **21 `Layer`s**. Zero clips.

The shared layers date from October 2025, which is one show having been built from
the other. **A layer id does not tell you which composition you are looking at.**

### 12.3 The composition `uniqueId` is not unique, and REST does not expose it anyway

All six files carry the identical composition-level id:

```
<Composition name="Composition" uniqueId="1669865320189" numDecks="3" numLayers="18" …>
```

`1669865320189` is 2022-12-01, which is when this Arena installation was first set
up. **It identifies the installation, not the composition.** Two different shows
report the same value.

And it is moot for the adapter regardless, because the REST composition object has
**no `id` field at all**:

```
top-level keys: audio bypassed clipbeatsnap cliptarget cliptriggerstyle columns
                crossfader dashboard decks layergroups layers master name selected
                speed tempocontroller video
```

**The only thing identifying a loaded composition over the API is
`name`, a mutable `ParamString`.**

Stacked with §10.1, that is a genuinely awkward readiness problem and it should be
solved deliberately rather than by reaching for the obvious field:

- the name is the only identifier, and it is **not** an identifier;
- during the ~1.2 s load window the name is correct while the composition is not;
- layer ids are shared between the operator's own shows (§12.2), so they do not
  disambiguate either.

What does discriminate, from the data here, is **structure**: layer count, column
count, deck count and names, and the presence of specific clip ids. A readiness
check that asserts the expected clip ids resolve is the only formulation found
that is wrong in none of the three cases above.

## 13. Open items this capture did not close

- **Everything about timecode.** Acquisition, loss, jumps, reacquisition, hold-last-frame, frame rates, offsets, and whether `transport.position` moves with LTC. That is D0 and it needs a generator and a cable.
- **The show host.** Every timing number is from an arm64 laptop on loopback. The deployed Hackintosh, over the show LAN, will differ, and the platform may become Windows.
- **A controlled save-and-reload.** §12 answers the identifier question observationally, from six real composition files, and that is enough to build on. What it does not cover is the live transition: loading a different composition **without restarting Arena** is the disruption the owner says actually recurs (§10.3), there is no REST path to trigger it (§2.3), and what the API reports *during* that swap is unmeasured. Given §10.1's loading window on a restart, assuming the swap is atomic would be unwise.
- **Why clip positions 1 and 2 are identical across all three decks** while every position from 3 upward differs (§9.4).
- **`smpteNquickselect` semantics.** The addresses exist at layer scope; what they select was not determined.
- **The crossfader** as a sixth way to silence a layer that passes every readiness check (§8.1).
- **Whether the `options` list can change at runtime**, which would make any cached enum stale. It varies per object; it was not observed changing on one object.
- **The one 204 that did nothing** (§2.6). Not reproduced; cause unknown.
- **Column, group and layer-clear over OSC input.** Present in the outbound address space; never sent as inputs.
- **The `Previewing` and `Connected & previewing` clip states.** Never observed; only `Empty`, `Disconnected` and `Connected` occurred.
- **Whether the full-composition push can be suppressed.** No mechanism was found; absence of a mechanism was not proven.
- **`targetType="4"`** in `osc.xml`. Meaning unknown.
- **The Avenue/Arena split.** The served spec is titled "Arena & Avenue", so some paths may not exist on Avenue. Not relevant to this deployment and not tested.
