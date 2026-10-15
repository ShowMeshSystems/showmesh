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

Then give the coordinator the gateway address and the same token
(`showmeshctl preview gateway set --url http://<host>:8095 --token-file <file>`).

## The NDI runtime

The image does not contain the NDI runtime ([ADR-010](../../docs/decisions/ADR-010-apache-2-license.md)).
Download the NDI SDK for Linux from NDI, copy `libndi.so.6` for the host's
architecture into a directory such as `/opt/ndi`, and set
`SHOWMESH_NDI_RUNTIME_DIR` to that directory. Without it the gateway still
serves RTSP cameras, and NDI streams report that NDI is not available.

## Settings

| Variable | Default | Meaning |
|---|---|---|
| `SHOWMESH_PREVIEW_GATEWAY_TOKEN` | none, required | Bearer token every request must carry. The gateway refuses to start without it. |
| `SHOWMESH_PREVIEW_GATEWAY_ADDR` | `:8095` | Address of the gateway API. |
| `SHOWMESH_PREVIEW_GATEWAY_WEBRTC_PORT` | `8555` | WebRTC port, TCP and UDP. |

## Network

The container uses host networking: NDI discovery is mDNS and WebRTC media is
UDP. Open 8095/tcp to the coordinator and 8555 (TCP and UDP) to browsers.
go2rtc's own API listens on 127.0.0.1:1984 only and also requires a password
the gateway generates on each start. The go2rtc RTSP and SRTP listeners are
turned off.

## What the image contains

go2rtc v1.9.14 (MIT) with its checksum verified at build, GStreamer with the
NDI plugin built by `deploy/node/ndi-plugin/build-ndi-plugin.sh`, x264 for
encoding, and ffmpeg, which go2rtc uses to cut JPEG snapshots from H.264.
The image carries GPL components (x264, ffmpeg builds from Debian); the
Apache-2.0 ShowMesh code is separate from them.

## Gateway API

The coordinator is the only client. The routes are in
[`docs/build/TRACK-PREVIEW-WALL.md`](../../docs/build/TRACK-PREVIEW-WALL.md).
`GET /healthz` needs no token and is the container health check.
