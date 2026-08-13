# RES-006: Linux NDI Support

[Architecture](../architecture/ARCHITECTURE.md#47-transport-adapters) · [Tracker](README.md) · [Transport comparison](RES-005-ndi-vs-hdmi-transport.md)

Status: planned (distribution resolved; sender bench pending) · Risk: high · Verification: L1 — source verified 2026-08-10

## Decision and validation scope

Validate an operationally reliable Linux NDI sender on the required architectures. The licensing and redistribution architecture is resolved: ShowMesh does not redistribute the NDI runtime.

## Resolved architecture

- ShowMesh may vendor the MIT-licensed headers but does not redistribute the proprietary NDI runtime.
- The NDI adapter dynamically loads a runtime installed separately by the user and honors the runtime's documented location override.
- If the runtime is absent, the node stays operational, does not advertise a usable NDI sender, preserves its other capabilities, and reports an actionable installation pointer.
- Runtime, discovery, and diagnostic details remain adapter concerns; they do not reopen the distribution decision or expand closure into an exhaustive environment matrix.

## Acceptance criteria

- A documented `amd64` installation with a user-installed NDI runtime sends the reference surface to the target Resolume version.
- A documented `arm64` installation with a user-installed NDI runtime sends a representative surface to the target Resolume version.
- The test record captures sender and receiver versions, architecture, canvas dimensions, pixel count, achieved frame rate, frame pacing, and observed stability.

## Test method

Build the smallest dynamically loaded sender and exercise it against Resolume on `amd64` and `arm64`. Use the practical documented installation path and record CPU/GPU use, timestamps, pacing, and stability. This closure work is a sender-to-Resolume validation on the two architectures, not an exhaustive survey of distributions, NIC arrangements, discovery topologies, runtime versions, or theoretical failure combinations.

## Evidence and findings

Desk research 2026-08-10 (official docs + open-source project experience; no SDK download or bench work yet). Confidence tags: [doc] official documentation, [proj] established project practice, [anec] community anecdote.

### Platform support

- The official NDI SDK fully supports Linux; the library depends on `libavahi-common.so.3`/`libavahi-client.so.3` and a running `avahi-daemon` ([platform considerations](https://docs.ndi.video/all/developing-with-ndi/sdk/platform-considerations)). x86_64 and aarch64 are first-class; NDI 6.2 shipped ARM-specific fixes. Current line as of Aug 2026: **NDI 6.3.2** (2026-04-13) ([release notes](https://docs.ndi.video/all/developing-with-ndi/sdk/release-notes)). [doc]
- Board-specific/embedded ARM builds and hardware-encoded NDI HX come via the separate **Advanced SDK**. [doc][proj]
- Download is registration-gated; exact contents of the current Linux tarball (ARM triples, glibc floor) are test parameters to record during the two architecture validations, not unresolved distribution architecture.

### Licensing and redistribution — the decisive constraint

- **Headers are MIT-licensed for open-source projects**, explicitly to allow in-repo headers plus **dynamic loading** of the NDI libraries at runtime ([software distribution](https://docs.ndi.video/all/developing-with-ndi/sdk/software-distribution)). [doc]
- Redistributing runtime binaries requires your EULA to cover the NDI SDK EULA; the sanctioned alternative is pointing users at NDI's runtime installer (`ndi.link/NDIRedistV5`; env vars `NDI_RUNTIME_DIR_V5`/`V6`). [doc]
- Branding obligations: link to ndi.video wherever NDI appears; trademark attribution; do not redistribute NDI Tools; keep NDI libs out of system paths; permission required to use "NDI" in a product name ([licensing](https://docs.ndi.video/all/developing-with-ndi/sdk/licensing)). [doc]
- SDK terms **tightened in 6.2.1 (Aug 2025)** with an updated exclusions list. That reinforces the decision not to redistribute the runtime: ShowMesh uses the documented user-installed-runtime path and does not make its distribution mechanism contingent on runtime redistribution rights. A future material license change is a revalidation trigger. [doc]
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

1. Validate the dynamically loaded sender into the target Resolume version on `amd64`, recording the reference-profile parameters and frame-pacing results.
2. Validate the same sender-to-Resolume path on `arm64` with a representative surface and recorded parameters.

The `arm64` item validates the NDI adapter and runtime path. It does not establish a Raspberry Pi / ARM renderer performance profile, which remains deferred in RES-004.

### Node distribution: Debian 13 recommended, unverified (2026-08-13)

**Recommended and not yet checked**, which is the whole reason it is written here rather than asserted in the architecture. The reasoning is media-stack friction, since that is what threatens the day-0 schedule rather than stability or lifecycle.

RHEL-family distributions do not carry the GStreamer Rust plugin set in their own repositories, and EPEL does not fill the gap, so a Rocky node means building the NDI plugin from cargo and owning that build; the RHEL media stack is also deliberately conservative about codecs. Those are correct tradeoffs for a server and wrong ones for a show appliance rebuilt between seasons.

Debian is preferred over Ubuntu on structural grounds rather than taste. **FPP is Debian-based**, so nodes match the platform this project already integrates with, and Step 9's plugin targets that same family. **Raspberry Pi OS is Debian**, so RES-004's deferred ARM profile becomes a continuation rather than a second port, which also makes open item 2 above cheaper. Ubuntu 24.04 LTS is the fallback if a packaging gap appears, and carries the densest community documentation for NDI on Linux because the OBS and DistroAV ecosystem lives there.

**What must be verified before this is built on**, and it is ten minutes: that the NDI GStreamer element is actually present and loadable on the chosen distribution, confirmed from `apt-cache search` and `gst-inspect-1.0` rather than from recollection of packaging status. That check belongs to the [Track B spike](../bench/TRACK-B-NDI-SPIKE.md), and its result is recorded here. The recommendation above is reasoned from packaging behaviour **recalled rather than observed**, and this project's standing rule is that an external system's behaviour is named only from that system's own output.

The coordinator is unaffected: it ships distroless under [ADR-012](../decisions/ADR-012-docker-coordinator-deployment.md), so this is a node decision rather than a fleet decision.

## Decision, fallback, and revalidation

Resolved: ShowMesh may vendor the MIT headers but never redistributes the NDI runtime. The NDI adapter dynamically loads a user-installed runtime with `dlopen("libndi.so.6")`, honors `NDI_RUNTIME_DIR_V6`, and degrades gracefully with an installation pointer when the runtime is absent. Required branding attributions remain part of product surfaces. NDI is the v1/reference transport behind the adapter; HDMI/capture remains supported as an alternate/fallback. Revalidate the integration after a material SDK, license, architecture, or Resolume change.

These decisions are recorded as durable constraints in [ADR-026](../decisions/ADR-026-renderer-surface-model-and-reference-transport.md). Its decision 6 makes the absent-runtime behaviour binding rather than adapter-local: the node **starts**, keeps every other capability, does not advertise a usable NDI sender, and reports an installation pointer. That is the same rule [ADR-025](../decisions/ADR-025-agent-fallback-cache-is-signed.md) settled for a failed cache verification, and it is recorded because a missing optional dependency is exactly the kind of thing an implementer turns into a fatal startup check without noticing they have converted a degradation into an outage.
