# RES-010: Projection Preview Monitoring

[Observability](../architecture/OBSERVABILITY.md#7-projection-preview-monitoring) · [Tracker](README.md) · [Transport research](RES-005-ndi-vs-hdmi-transport.md) · [Failure testing](RES-009-failure-mode-testing.md)

Status: planned (bench) · Risk: critical · Verification: L1 — source verified 2026-08-10

## Decision to make

Choose the preview acquisition, analysis, and browser-delivery path, then establish reliable detection rules for missing, degraded, black, solid, and frozen projection feeds.

## Questions

- Can Resolume expose one preview per physical output or projection surface?
- Where should previews be sampled: renderer, transport input, Resolume output, or several layers?
- Which gateway provides reliable NDI or capture input and WebRTC or fallback browser delivery?
- Are 320×180 or 480×270 at 5–10 fps sufficient for diagnosis and affordable for six simultaneous feeds?
- Which hash, luminance, variance, motion, frame-arrival, and resolution metrics are robust?
- How is `motion_expected` derived from sequence, composition, or operator configuration?
- How do we distinguish failure of the preview path from failure of the primary projection path?

## Acceptance criteria

- Six reference previews remain viewable for a full-show soak within a documented resource and bandwidth budget.
- Detection deadlines and false-positive rates are measured for missing, frozen, black, solid, low-frame-rate, and resolution-mismatch cases.
- Intentional stills, blackout, resting scenes, and slow content do not generate critical frozen-feed alerts under configured context.
- Sender, gateway, browser, and Resolume restarts recover without manual reconfiguration.
- Primary-output and preview-path failures are distinguishable where available evidence permits.

## Test matrix and method

Record Resolume, NDI/runtime, gateway, browser, OS, GPU, resolution, and network versions. Use still, slow, high-motion, dark, bright, and solid-color content. Inject source loss, duplicate frames, frame-rate collapse, resolution changes, congestion, gateway restart, browser sleep, and main-output loss. Preserve timed screen recordings, metrics, logs, and expected classifications.

## Evidence and findings

Desk research 2026-08-10 (official docs, project docs, source reads; no bench work). Confidence tags: [doc] official documentation, [proj] project docs/source, [anec] community.

### Preview acquisition

- **NDI proxy streams solve the bandwidth problem by design.** Every NDI sender publishes a full-quality stream and a low-bandwidth proxy; any receiver may request the proxy (`NDIlib_recv_bandwidth_lowest`), which is ~**640 px wide** ("medium quality", 640×360 at 16:9) — at or above the 480×270 tile target. NDI's own documented use case for proxy receive is exactly preview monitors ([bandwidth white paper](https://docs.ndi.video/all/getting-started/white-paper/bandwidth/ndi-proxy-and-bandwidth-optimization), [recv API](https://docs.ndi.video/all/developing-with-ndi/sdk/ndi-recv)). [doc]
- GStreamer's `ndisrc` exposes a `bandwidth` property ([element doc](https://gstreamer.freedesktop.org/documentation/ndi/ndisrc.html)); gst-plugins-rs source confirms `bandwidth=0` maps to `NDIlib_recv_bandwidth_lowest` ([ndisys.rs](https://raw.githubusercontent.com/GStreamer/gst-plugins-rs/main/net/ndi/src/ndisys.rs)). Proxy frame rate and ndisink sender-side proxy generation need a bench sanity check. [proj]
- **Resolume Arena emits NDI at two granularities**: the composition output, and — Arena only — **per Advanced-Output screen**, each screen a separate NDI source with its own resolution/slices ([NDI I/O doc](https://resolume.com/support/NDI_inputs_and_outputs)). Per-projector previews therefore need no capture hardware. [doc]
- **Resolume 7.26 added a REST `monitors` endpoint** returning PNG/JPEG snapshots of outputs ([7.26 blog](https://resolume.com/blog/34484)) — a low-rate independent cross-check tap. Polling-rate impact undocumented (the 7.26.1 fixlist includes a REST-overhead fix — measure, don't assume). Note: reference installation currently runs Arena 7.23.2, which predates this endpoint. [doc]
- Syphon/Spout are same-machine GPU texture sharing — not applicable to remote dashboards. [doc]

### Browser delivery

- **Neither MediaMTX nor go2rtc ingests NDI natively** ([mediamtx#1014](https://github.com/bluenviron/mediamtx/issues/1014), [go2rtc#975](https://github.com/AlexxIT/go2rtc/issues/975)); GStreamer must terminate NDI — consistent with ADR-007 anyway. [proj]
- **MediaMTX** (Go, MIT, single binary): ingests WHIP/RTSP/RTMP/SRT, serves WebRTC (WHEP)/RTSP/LL-HLS; GStreamer publishes directly via `whipsink` ([webrtchttp plugin](https://gstreamer.freedesktop.org/documentation/webrtchttp/index.html)). [proj]
- **go2rtc** (Go, single binary): RTSP/WebRTC/`exec:` GStreamer ingest; WebRTC + MSE + MJPEG + JPEG snapshot outputs with automatic client negotiation ([repo](https://github.com/AlexxIT/go2rtc)). [proj]
- Latency ordering for a LAN ops dashboard: WebRTC best; MSE acceptable; HLS/LL-HLS wrong tool. [proj]
- **MJPEG fallback math**: six 480×270@10fps tiles ≈ 5–15 Mbit/s total — trivial on the show LAN. The real limit is the browser's ~6 connections/host on HTTP/1.1 ([MDN](https://developer.mozilla.org/en-US/docs/Web/HTTP/Guides/Connection_management_in_HTTP_1.x)); six always-open MJPEG streams exhaust it. Mitigation: JPEG frames over **one WebSocket** painted to canvas, or HTTP/2 (needs TLS). [doc]

### Frame analysis

- No GStreamer freeze-detect element exists (FFmpeg's [`freezedetect`](https://ffmpeg.org/ffmpeg-filters.html#freezedetect) is the behavioral reference). `videoanalyse` posts per-frame `luma-average`/`luma-variance` (black-frame detection) ([doc](https://gstreamer.freedesktop.org/documentation/videosignal/videoanalyse.html)); `videocompare` does perceptual-hash but against a reference pad ([doc](https://gstreamer.freedesktop.org/documentation/rsvideofx/videocompare.html)). [doc]
- Practical approach: tee the decoded preview to `videoscale`→64×36 gray @1–2 fps→`appsink`; hash/diff in Go; N identical frames = frozen candidate, near-zero mean = black. Publishes as observed-state evidence over MQTT (ADR-003 fit). NDI timestamp progression from a live sender helps distinguish "frozen content" from "dead pipeline". [proj/inference]

### Prior art

No open-source "NDI-in, web-multiview-out" product exists; building blocks are mature ([awesome-ndi](https://github.com/florisporro/awesome-ndi), Tractus Multiview closed-source, [Eyevinn ott-multiview](https://github.com/Eyevinn/ott-multiview) front-end pattern, [raspberry_ninja](https://github.com/steveseguin/raspberry_ninja) GStreamer→WebRTC analog). The glue is the novel part. [proj]

### Candidate architectures for bench

- **A (primary):** per feed `ndisrc bandwidth=0 → scale/rate 480×270@8 → x264 zerolatency → whipsink → MediaMTX → WHEP tiles`. Preview node may be the coordinator host (monitoring is not the show media path).
- **B (fallback/phase-O1):** same front half → `jpegenc → appsink` → Go agent fans JPEG over one WebSocket. Zero new dependencies; keep as reduced fallback per ADR-004 spirit.
- **A′:** go2rtc instead of MediaMTX where MJPEG/snapshot outputs from one binary are wanted.
- In all: tee to the analysis branch; Resolume REST snapshots (after upgrade ≥7.26) as independent cross-check.

### Open items for bench (L2)

1. `ndisrc bandwidth=0` caps/fps against Arena composition output, Advanced-Output NDI screens, and gst `ndisink` senders.
2. CPU on the preview node: 6 × (proxy decode + scale + encode) on reference hardware.
3. Resolume `monitors` endpoint latency and render impact at 0.5/1/2 Hz (requires Arena ≥7.26 — currently 7.23.2).
4. Sender-side load added by proxy-stream receivers on the Resolume host.
5. End-to-end glass latency and restart recovery (preview node, MediaMTX, browser) mid-show.
6. Freeze-detector false-positive rate on intentionally static holiday content; context thresholds (`motion_expected`).

## Decision, fallback, and revalidation

Direction (pending bench): NDI proxy receive via GStreamer as the single acquisition path for both Resolume outputs and media-node senders; WebRTC via MediaMTX (or go2rtc) as primary delivery; JPEG-over-WebSocket as the reduced fallback; appsink hash analysis for black/frozen candidates gated by ADR-011 context. Fallback remains signal/device-state monitoring without content analysis. Revalidate after Resolume, gateway, runtime, browser, GPU, or network changes.
