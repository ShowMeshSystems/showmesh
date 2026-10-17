# Dashboard video previews: build specification

[ADR-060](../decisions/ADR-060-dashboard-video-previews.md) ·
[RES-010](../research/RES-010-projection-preview-monitoring.md) ·
[Identifier register](IDENTIFIER-REGISTER.md)

Integration branch: `dev/preview-wall`. Each part below is its own PR into that
branch. This file is the shared contract between the three parts. A builder
that needs to change a shape here asks the orchestrator first.

## Parts and file ownership

| Part | Owns | Must not edit |
|---|---|---|
| G, gateway | `cmd/showmesh-preview-gateway/`, `internal/previewgateway/`, `deploy/preview-gateway/`, the `preview` profile in `deploy/docker-compose.yml`, its Makefile target | `internal/coordinator/`, `api/openapi.yaml`, `ui/` |
| C, coordinator | `internal/coordinator/config/` (three new kinds), `internal/coordinator/preview/`, the API handlers, `api/openapi.yaml`, `cmd/showmeshctl/` | `ui/`, `deploy/`, the gateway packages |
| U, UI | `ui/` only | everything outside `ui/` |

Merge order into `dev/preview-wall`: C, then G, then U.

## Configuration kinds (part C)

All three follow the existing store-backed pattern (`fpp.mqtt` is the model,
including its credential handling in `fppmqttsecret.go`).

**`preview.gateway`**, `default` singleton. Absent or with an empty `url`
means previews are off.

```json
{ "url": "http://192.168.130.176:8095", "tokenSet": true }
```

The token is a credential field `token`: accepted on PUT, never returned,
reported only as `tokenSet`.

**`preview.camera`**, operator-chosen id.

```json
{ "name": "Driveway", "enabled": true, "urlSet": true,
  "urlDisplay": "rtsp://192.168.1.20:37379" }
```

The full RTSP URL is a credential field `url`. `urlDisplay` is scheme, host
and port only, computed by the coordinator at write time. A URL must be
`rtsp://` or `rtsps://`.

**`dashboard.layout`**, `default` singleton.

```json
{ "previews": { "order": ["ndi:NODE-01 (matrix-main)", "camera:driveway"],
                "hidden": ["ndi:ARENA (Composition)"] } }
```

## Preview keys

A tile key is `ndi:<full NDI source name>` or `camera:<preview.camera id>`.
Keys are URL-path-escaped wherever they appear in a path.

## Coordinator API (part C)

`GET /api/v1/previews` returns the gateway state and every tile, in layout
order. A source not named in the layout is appended at the end, in the order
the coordinator first saw it (alphabetical only among sources first seen in
the same pass, such as at startup):

```json
{
  "gateway": { "state": "reachable", "message": "", "observedAt": "..." },
  "tiles": [
    { "key": "ndi:NODE-01 (matrix-main)", "kind": "ndi",
      "label": "node-01 · matrix-main", "nodeId": "node-01",
      "surfaceId": "matrix-main", "state": "idle", "message": "",
      "hidden": false, "observedAt": "..." },
    { "key": "camera:driveway", "kind": "camera", "label": "Driveway",
      "cameraId": "driveway", "state": "live", "message": "",
      "hidden": false, "observedAt": "..." }
  ]
}
```

- `gateway.state`: `not_configured`, `connecting` (an address is set and the
  coordinator has not yet heard from it), `reachable`, `unreachable`. With
  `not_configured`, `tiles` is empty. An observation made for another address
  or token counts as none.
- `gateway.ndiDiscovery`: `{"state": "running|not_running", "message": "..."}`
  from the gateway's `GET /v1/discovery`. Omitted while the gateway is not
  reachable or is too old to report it, never filled in as running.
- `tile.state`: `live` (frames arriving), `idle` (nobody watching, so the
  source is not started), `connecting`, `not_found` (the NDI source is no
  longer announced, or the camera refused), `disabled` (camera disabled),
  `error`. `message` states the fact and then the action, per the operator
  copy rule in CLAUDE.md.
- An NDI tile is labelled with a node and surface when the part of its name in
  parentheses equals a `show.surface` `output.ndi.sourceName`. When more than
  one node renders that surface, the host part of the name picks the node,
  matched case-insensitively against the node id or the node's label. Otherwise the label
  is the raw NDI name.

`POST /api/v1/previews/{key}/webrtc`: request body `application/sdp` (the
browser's offer), response `application/sdp` (the answer). Proxied to the
gateway.

`GET /api/v1/previews/{key}/snapshot`: `image/jpeg`, the latest frame.
Proxied to the gateway. The UI falls back to polling this every 2 s when
WebRTC fails.

All three are reads. Every one needs `observation:read`, including video and snapshots. Writes to the three configuration kinds stay allowed in show mode, because previews are not show configuration.

The coordinator sends the gateway the full desired stream set whenever any of
the three kinds changes, and again every 10 s. Every NDI source the gateway
reports plus every enabled camera is in the set.

## showmeshctl (part C)

```text
showmeshctl preview list
showmeshctl preview snapshot <key> -o <file.jpg>
showmeshctl preview gateway get
showmeshctl preview gateway set --url <url> --token-file <path>
showmeshctl preview gateway revisions
showmeshctl preview camera list
showmeshctl preview camera get <id>
showmeshctl preview camera revisions <id>
showmeshctl preview camera add <id> --name <name> --url-file <path>
showmeshctl preview camera remove <id>
showmeshctl preview layout get
showmeshctl preview layout revisions
showmeshctl preview layout move <key> --to <index>
showmeshctl preview layout hide|show <key>
```

Secrets come from a file or stdin, never a flag value.

## Gateway API (part G; coordinator to gateway only, not in `api/openapi.yaml`)

Every request carries `Authorization: Bearer <token>`. The token comes from
`SHOWMESH_PREVIEW_GATEWAY_TOKEN`. The gateway listens on
`SHOWMESH_PREVIEW_GATEWAY_ADDR`, default `:8095`. go2rtc's API listens on a
random free loopback port chosen on every start and needs a per-run password;
the gateway sends it camera addresses only after the process on that port has
shown it knows the password. Its WebRTC port is `8555` by default, TCP and UDP,
set with `SHOWMESH_PREVIEW_GATEWAY_WEBRTC_PORT`. The gateway exits with an
error if that port is taken.

- `GET /v1/sources`: NDI sources currently announced,
  `[{"name": "NODE-01 (matrix-main)", "address": "192.168.130.22:5961"}]`.
- `GET /v1/discovery`: whether NDI discovery works, `{"running": false,
  "message": "..."}`. `message` states why not and what to do, and is empty
  while it runs. Discovery is not running while the runtime directory has
  neither `libndi.so.6` nor `libndi.so.5`, or while the device monitor keeps
  failing at start.
- `PUT /v1/streams`: replaces the desired set. The body is an array of
  `{"key": "...", "kind": "ndi", "ndiName": "..."}` or
  `{"key": "...", "kind": "rtsp", "url": "rtsp://..."}`. This is idempotent,
  and an unchanged entry is not restarted.
- `GET /v1/streams`: `[{"key": "...", "state": "live|idle|connecting|not_found|error", "message": "...", "consumers": 0}]`.
- `POST /v1/streams/{key}/webrtc`: SDP offer in, SDP answer out.
- `GET /v1/streams/{key}/snapshot`: `image/jpeg`.

An NDI stream is a GStreamer pipeline the gateway builds itself:
`ndisrc ndi-name=<name> bandwidth=lowest`, scaled to 640×360, rated to
10 fps, encoded H.264 (`x264enc tune=zerolatency speed-preset=ultrafast`).
The NDI name is passed as one argument after validation and is never
interpolated into a shell string. A camera stream is go2rtc's own RTSP source.

## Verification

- Part G: a container bench on a VM. A test NDI sender container
  (`videotestsrc ! ndisink`), a test RTSP source, the gateway image, and
  `curl` against every gateway route, including a real WebRTC answer and a
  JPEG snapshot. The NDI runtime is downloaded on the VM and mounted. It is
  never committed or baked into an image.
- Part C: unit and handler tests against a fake gateway, OpenAPI conformance,
  and `make check` on the VM gate.
- Part U: unit tests, then a real browser against a coordinator and gateway on
  the container bench.
- Real nodes, Arena and cameras are the owner's check.
