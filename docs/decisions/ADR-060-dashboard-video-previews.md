# ADR-060: The Dashboard Shows Low-Rate Video Previews Through a Separate Preview Gateway

Status: Accepted (owner, 2026-10-15)
Date: 2026-10-15

## Context

[RES-010](../research/RES-010-projection-preview-monitoring.md) researched
projection preview monitoring to L1 and
[OBSERVABILITY.md](../architecture/OBSERVABILITY.md) §7 specifies a full
preview wall: projector telemetry on every tile and context-aware black and
frozen detection. BUILD-PLAN cut the preview wall from day-0.

The owner wants a smaller thing before Halloween: a grid of live tiles on the
dashboard that answers "is this output doing the right thing". Resolution and
sync do not matter. The tiles cover the NDI outputs of the render nodes, NDI
outputs of Resolume Arena, and security cameras, the last of which no record
mentions. Compute on the preview host and NDI load on Arena are not concerns
(owner, 2026-10-15).

The coordinator image is distroless and has no GStreamer, so it cannot receive
NDI. Neither go2rtc nor MediaMTX reads NDI (RES-010), so GStreamer must.

## Decisions

1. **Version 1 is a view-only preview.** No content analysis, no black or
   frozen detection, no recording, no sync, no projector telemetry on a tile.
   OBSERVABILITY §7.2 and §7.3 remain the later target and RES-010 keeps its
   open bench items.
2. **A separate preview gateway receives and serves the video.**
   `showmesh-preview-gateway` is its own process and container image: a small
   ShowMesh Go program supervising go2rtc (MIT), with GStreamer and the
   `gst-plugins-rs` NDI plugin in the image. The NDI runtime is supplied by the
   operator and mounted at run time, as on render nodes
   ([ADR-010](ADR-010-apache-2-license.md),
   [ADR-055](ADR-055-one-command-install-and-node-enrollment.md)). The
   coordinator image is unchanged.
3. **The gateway runs beside the coordinator or on its own host.** The
   coordinator's compose file carries it as an opt-in profile, and the same
   image runs standalone with its own compose file. The coordinator reaches it
   by a configured address. On Linux it uses host networking, because NDI
   discovery is mDNS and WebRTC media is UDP.
4. **Two kinds of source.** NDI sources are discovered by the gateway and
   appear on their own; a source that matches a render node's surface output is
   labelled with that node and surface. RTSP cameras are added by the operator
   as a generic URL; a Scrypted rebroadcast URL is the reference case. A camera
   URL is a credential: stored like the `fpp.mqtt` password
   ([ADR-039](ADR-039-operator-configuration-is-store-backed.md) decision 7),
   accepted on write, never returned on read.
5. **Only the gateway's own API is on the network.** go2rtc's API and web UI
   listen on loopback inside the gateway. The gateway's API takes structured
   stream descriptions, never a command line, and builds every GStreamer
   argument itself from validated values. It requires a shared token the
   coordinator holds as a credential.
6. **The browser talks to the coordinator.** WebRTC signalling and a JPEG
   snapshot fallback go through coordinator API routes, so the UI stays
   same-origin ([ADR-022](ADR-022-operator-ui-serves-the-api-same-origin.md)).
   WebRTC media flows directly between the browser and the gateway.
7. **Previews are not on the show path.** Loss of the gateway, the coordinator
   or a source changes only what a tile says. Nothing in the show depends on a
   preview. The dashboard's preview section appears only when a gateway is
   configured.
8. **One arrangement per installation.** The tile order and hidden tiles are
   stored on the coordinator as `dashboard.layout`, so every browser sees the
   same grid. A new source is appended at the end. A later configurable
   dashboard extends this kind rather than replacing it. Arranging it needs
   `dashboard:write`, which the operator role holds; the gateway and
   camera settings stay behind `config:write`. Every preview read, including
   video and snapshots, needs `observation:read`, as OBSERVABILITY §13 requires
   for preview streams.
9. **API first.** Gateway address, cameras and layout are configuration
   objects, and `showmeshctl` can list previews with their state, manage
   cameras and the layout, and save a snapshot to a file.

## Consequences

- A new image to build and release, with the same NDI plugin build the node
  tarball already uses.
- The preview tiles prove only that a source is producing frames the gateway
  can decode. They are not evidence about the projector, the physical output,
  or audience-visible timing.
- go2rtc starts a source only while someone is watching it, so an unwatched
  tile reports idle rather than live.
