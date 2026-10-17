# Preview gateway

The preview gateway receives NDI sources and RTSP cameras and serves them to
the dashboard as low-rate video ([ADR-060](../../docs/decisions/ADR-060-dashboard-video-previews.md)).
It runs go2rtc with GStreamer beside it. Nothing in the show depends on it.

## Run it

Beside the coordinator, with the `preview` profile of `deploy/docker-compose.yml`:

```sh
cd deploy
echo 'SHOWMESH_PREVIEW_GATEWAY_TOKEN=<a long random token>' >> .env
echo 'SHOWMESH_NDI_RUNTIME_DIR=/opt/ndi' >> .env
docker compose --profile preview up -d --build preview-gateway
```

On its own host, with this directory's compose file:

```sh
cd deploy/preview-gateway
export SHOWMESH_PREVIEW_GATEWAY_TOKEN=<a long random token>
export SHOWMESH_NDI_RUNTIME_DIR=/opt/ndi
docker compose up -d --build
```

To run a released version instead of building, add the published override:

```sh
SHOWMESH_RELEASE_VERSION=0.4.0 docker compose \
  -f docker-compose.yml -f docker-compose.published.yml up -d
```

With the coordinator's stack, add `-f docker-compose.published.yml` from
`deploy/` and `--profile preview`.

Then give the coordinator the gateway address and the same token
(`showmeshctl preview gateway set --url http://<host>:8095 --token-file <file>`).

## The NDI runtime

The image does not contain the NDI runtime ([ADR-010](../../docs/decisions/ADR-010-apache-2-license.md)).
Download the NDI SDK for Linux from NDI, copy `libndi.so.6` for the host's
architecture into a directory such as `/opt/ndi`, and set
`SHOWMESH_NDI_RUNTIME_DIR` to that directory. Without it the gateway still
serves RTSP cameras, and NDI streams report that NDI is not available. The
GStreamer NDI plugin opens only `libndi.so.6` or `libndi.so.5`. If the file
has another name, such as `libndi.so`, the Dashboard says so: rename it or add
a link with `ln -s libndi.so libndi.so.6` in that directory.

## Settings

| Variable | Default | Meaning |
|---|---|---|
| `SHOWMESH_PREVIEW_GATEWAY_TOKEN` | none, required | Bearer token every request must carry. The gateway refuses to start without it. |
| `SHOWMESH_PREVIEW_GATEWAY_ADDR` | `:8095` | Address of the gateway API. |
| `SHOWMESH_PREVIEW_GATEWAY_WEBRTC_PORT` | `8555` | WebRTC port, TCP and UDP. |
| `SHOWMESH_PREVIEW_GATEWAY_WEBRTC_CANDIDATES` | this host's IPv4 addresses | Comma-separated addresses to offer browsers. Set it when browsers reach the gateway through another address. |

## Network

The container uses host networking: NDI discovery is mDNS and WebRTC media is
UDP. Open 8095/tcp to the coordinator and 8555 (TCP and UDP) to browsers.
go2rtc's own API listens on a free loopback port the gateway picks on each
start and also requires a password it generates on each start, so another
service on the host, such as Frigate or Home Assistant on 1984, cannot collide
with it or receive camera addresses. The gateway refuses to start if the WebRTC
port is taken; set `SHOWMESH_PREVIEW_GATEWAY_WEBRTC_PORT` to a free one. The go2rtc RTSP and SRTP listeners are
turned off, and WebRTC does not use a STUN server, so previews stay on the
local network. Address changes after start need a gateway restart.

## NDI discovery

The NDI runtime finds sources through avahi-daemon over D-Bus. The image runs
its own browse-only copies as root and drops to an unprivileged user for the
gateway, so the host needs no Avahi. If Avahi stops, NDI streams report that
discovery is not running, RTSP cameras keep working, and the container stays
healthy; restart the container to bring NDI discovery back. An NDI source that stops without saying
goodbye can stay in the source list until its mDNS record expires. Discovery
does not reach a sender on another subnet or VLAN; add it in Settings >
Dashboard with its NDI name and `host:port` address.

## Cameras

A camera snapshot waits for the next keyframe. A camera that sends a keyframe
every few seconds answers quickly; one that sends it every 25 seconds or more
makes snapshots slow.

## What the image contains

go2rtc v1.9.14 (MIT) with its checksum verified at build, GStreamer with the
NDI plugin built by `deploy/node/ndi-plugin/build-ndi-plugin.sh`, x264 for
encoding, and ffmpeg, which go2rtc uses to cut JPEG snapshots from H.264.
The image carries GPL components (x264, ffmpeg builds from Debian); the
Apache-2.0 ShowMesh code is separate from them.

## Gateway API

The coordinator is the only client. The routes are in
[`docs/build/TRACK-PREVIEW-WALL.md`](../../docs/build/TRACK-PREVIEW-WALL.md).
`GET /healthz` needs no token and is the container health check. It fails
while the video engine (go2rtc) is not running or has not answered yet.
