# RES-006: Linux NDI Support

[Architecture](../architecture/ARCHITECTURE.md#47-transport-adapters) · [Tracker](README.md) · [Transport comparison](RES-005-ndi-vs-hdmi-transport.md)

Status: planned (bench) · Risk: high · Verification: L1 — source verified 2026-08-10

## Decision to make

Determine whether an open-source Linux node can ship an operationally reliable, legally distributable NDI sender across required architectures.

## Questions

- Which official Linux SDK/runtime versions and CPU architectures are supported?
- May required runtime components be redistributed, downloaded at install time, or only supplied by users?
- Does headless discovery work across the intended VLAN topology?
- Which pixel formats, frame rates, metadata, audio, and timestamp features are available?
- What hardware acceleration exists, and which operations remain CPU-bound?
- How are runtime loading, version conflicts, packaging, and upgrades handled?
- What diagnostic data can an agent expose?

## Acceptance criteria

- A clean supported Linux installation can send to the target Resolume version after a documented setup.
- Licensing and redistribution requirements are reviewed and recorded.
- `amd64` behavior is bench verified; `arm64` is either verified or explicitly unsupported.
- Headless start, discovery, reconnect, runtime upgrade, and runtime absence have defined behavior.

## Test method

Use authoritative SDK and license materials, then build the smallest dynamically isolated sender. Test supported distributions and architectures, multiple NICs, routed and local discovery, receiver restart, sender restart, missing runtime, and version mismatch. Record CPU/GPU use and timestamps.

## Evidence and findings

Desk research 2026-08-10 (official docs + open-source project experience; no SDK download or bench work yet). Confidence tags: [doc] official documentation, [proj] established project practice, [anec] community anecdote.

### Platform support

- The official NDI SDK fully supports Linux; the library depends on `libavahi-common.so.3`/`libavahi-client.so.3` and a running `avahi-daemon` ([platform considerations](https://docs.ndi.video/all/developing-with-ndi/sdk/platform-considerations)). x86_64 and aarch64 are first-class; NDI 6.2 shipped ARM-specific fixes. Current line as of Aug 2026: **NDI 6.3.2** (2026-04-13) ([release notes](https://docs.ndi.video/all/developing-with-ndi/sdk/release-notes)). [doc]
- Board-specific/embedded ARM builds and hardware-encoded NDI HX come via the separate **Advanced SDK**. [doc][proj]
- Download is registration-gated; exact contents of the current Linux tarball (ARM triples, glibc floor) must be confirmed at download time.

### Licensing and redistribution — the decisive constraint

- **Headers are MIT-licensed for open-source projects**, explicitly to allow in-repo headers plus **dynamic loading** of the NDI libraries at runtime ([software distribution](https://docs.ndi.video/all/developing-with-ndi/sdk/software-distribution)). [doc]
- Redistributing runtime binaries requires your EULA to cover the NDI SDK EULA; the sanctioned alternative is pointing users at NDI's runtime installer (`ndi.link/NDIRedistV5`; env vars `NDI_RUNTIME_DIR_V5`/`V6`). [doc]
- Branding obligations: link to ndi.video wherever NDI appears; trademark attribution; do not redistribute NDI Tools; keep NDI libs out of system paths; permission required to use "NDI" in a product name ([licensing](https://docs.ndi.video/all/developing-with-ndi/sdk/licensing)). [doc]
- SDK terms **tightened in 6.2.1 (Aug 2025)** with an updated exclusions list; the current EULA text must be read from the actual 6.3 download before committing to a distribution mechanism. [doc]
- Ecosystem precedent: **DistroAV** (ex obs-ndi) ships GPL plugin code with no bundled runtime and requires users to install the NDI runtime separately ([DistroAV](https://github.com/DistroAV/DistroAV)); **GStreamer's Rust NDI plugin** dlopens the runtime honoring `NDI_RUNTIME_DIR_V6` ([teltek/gst-plugin-ndi](https://github.com/teltek/gst-plugin-ndi), upstream in [gst-plugins-rs](https://github.com/GStreamer/gst-plugins-rs)); **FFmpeg removed NDI in 2019** after a GPL dispute ([ffmpeg-devel](https://ffmpeg.org/pipermail/ffmpeg-devel/2018-December/237097.html)). [proj]
- No production-grade clean-room open implementation of full-bandwidth NDI exists; everything practical wraps the official runtime. [proj]

### Headless operation and discovery

- Discovery uses Avahi mDNS — a daemon dependency, not a desktop one; headless works ([platform considerations](https://docs.ndi.video/all/developing-with-ndi/sdk/platform-considerations)). [doc]
- Cross-VLAN/routed topologies: **Discovery Server** replaces mDNS with a central registry, ships as a standalone Linux service since 6.2/6.3, supports redundant servers ([discovery server](https://docs.ndi.video/all/getting-started/white-paper/discovery-and-registration/discovery-server)). Headless client config via `$HOME/.ndi/ndi-config.v1.json`. [doc]
- NDI 6.3 added programmatic sender discovery/monitoring APIs on Linux — useful for agent health reporting. [doc]

### Send API features relevant to ShowMesh sync

- Pixel formats UYVY/UYVA/P216/PA16/YV12/I420/NV12/BGRA/BGRX/RGBA/RGBX; arbitrary rational frame rates; planar float32 audio; per-frame XML metadata; tally ([send API](https://docs.ndi.video/all/developing-with-ndi/sdk/ndi-send), [frame types](https://docs.ndi.video/all/developing-with-ndi/sdk/frame-types)). [doc]
- `clock_video` rate-limits a tight render loop to the declared framerate; async send offloads convert/compress; `timecode` (int64, 100 ns) may be self-assigned or synthesized (UTC-based, coherent across senders on a host, and "very high precision" across NTP-disciplined machines); SDK-filled `timestamp` (~1 µs) is documented for cross-machine sync. Recipe: NTP/PTP-disciplined hosts + synthesized or show-clock timecodes + clocked async send. [doc]

### Performance expectations

- Full-bandwidth encode (SpeedHQ) is CPU/SIMD-bound (AVX2/NEON); no GPU offload in the standard SDK; hardware H.264/HEVC is Advanced-SDK/HX territory (with separate codec patent responsibility). Submit UYVY to avoid conversion cost; Linux reliable-UDP send wants kernel ≥4.18 GSO ([performance](https://docs.ndi.video/all/developing-with-ndi/sdk/performance-and-implementation)). [doc]
- Raspberry Pi class: workable but modest — ~5–6 concurrent 1080p30/60 UYVY connections reported on Pi 4-class hardware via [V4L2-to-NDI](https://github.com/lplassman/V4L2-to-NDI). [anec]

### Open items for bench (L2) verification

1. Download SDK 6.3; record tarball contents, glibc floor, ARM triples; read the full current EULA (post-6.2.1 exclusions).
2. SpeedHQ encode headroom at target resolutions on reference x86 and arm64 hardware, with/without async send and GSO.
3. Discovery Server behavior across the reference VLANs with Resolume as receiver; Avahi behavior if the agent ships in a container.
4. Achieved inter-node alignment at Resolume with NTP vs PTP discipline.
5. Missing-runtime, version-mismatch, and runtime-upgrade behavior of the dlopen path.

## Decision, fallback, and revalidation

Direction (pending EULA read and bench work): vendor MIT headers, `dlopen("libndi.so.6")` honoring `NDI_RUNTIME_DIR_V6`, never bundle the runtime, degrade gracefully with an install pointer when absent, and carry the required branding attributions. Repo license choice must account for this pattern (see FFmpeg precedent). NDI remains optional behind the transport adapter; HDMI/capture is the fallback. Revalidate on SDK, license, distribution, architecture, or Resolume changes.
