# RES-006: Linux NDI Support

[Architecture](../architecture/ARCHITECTURE.md#47-transport-adapters) · [Tracker](README.md) · [Transport comparison](RES-005-ndi-vs-hdmi-transport.md)

Status: testing (amd64 sender benched 2026-08-16; arm64 and Ubuntu open) · Risk: high · Verification: **L2 for the amd64 sender-to-Resolume path**; L1 for everything else

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

### Bench: amd64 sender to Resolume, 2026-08-16 (L2)

Open item 1 below is discharged. The [Track B spike](../bench/TRACK-B-NDI-SPIKE.md) ran on the owner's bench overnight 2026-08-16, and this is the first NDI evidence in the repository that is a measurement rather than an expectation.

**Result: 982,100 frames rendered, 0 dropped, 0 late, 0 errors, sustained 40.00 fps over 6 h 49 min.** The sink ran `sync=true`, so QoS was active and a frame the pipeline could not deliver on the clock would have been counted; the zero is measured rather than structural. Two receivers were attached for the duration, Resolume Arena and NDI Video Monitor on a second machine, and neither reported a drop or an error either.

| Parameter | Value |
| --- | --- |
| Renderer node | Dell OptiPlex Micro 7050, Core i7, 16 GB RAM, 256 GB M.2 |
| OS | Debian 13, bare metal |
| Canvas | 1920x1080, UYVY, 40 fps requested and achieved |
| GStreamer NDI plugin | `gst-plugins-rs`, **compiled from source on the node** |
| Network | Wired Ethernet to the core switch; UniFi fabric with NDI/mDNS enabled |
| Receivers | Resolume Arena (attached, displaying) plus NDI Video Monitor on a second machine |
| Duration | 6 h 49 min continuous (982,100 frames at 40 fps) |
| Sink QoS | `sync=true` |
| CPU | 86% of one core, single-threaded pipeline (generate, encode and send on one streaming thread); machine otherwise near idle, no thermal throttling |

**What this establishes.** The NDI transport assumption in [ADR-026](../decisions/ADR-026-renderer-surface-model-and-reference-transport.md) survives contact with reference-class hardware on the wired path, at the canvas dimensions intended for day-0, for longer than a show runs. [ADR-026](../decisions/ADR-026-renderer-surface-model-and-reference-transport.md)'s day-0 profile named "OptiPlex 7040-class" hardware and the bench ran a 7050, so the bar was cleared on newer silicon than the bar assumed; that is stated here so the result is never read as a floor.

**What it does not establish, and the distinction is the whole point.** The frame source was `videotestsrc`, which costs almost nothing to produce. The real renderer has to generate every one of those frames out of FSEQ channel data first, and this run says nothing about whether the CPU budget left over after SpeedHQ encode is enough to do that. **ADR-026 decision 5's profile is renderer plus transport, and only the transport half now has evidence.** The renderer half is [RES-004](RES-004-virtual-matrix-renderer-performance.md) and remains L0. It also says nothing about `arm64`, Ubuntu, HDMI, two simultaneous surfaces, or recovery behaviour (spike run 5, not run).

### Packaging check: the element is not in Debian 13, answered 2026-08-16

The check this record asked for below was run and **the answer is no**. The GStreamer NDI element is not available as a Debian 13 package in a form the owner could locate, and the plugin was built from `gst-plugins-rs` on the node instead. The build was four steps.

**This does not overturn the Debian recommendation, and it does remove one of that recommendation's three arguments.** The reasoning below preferred Debian over RHEL-family partly because "a Rocky node means building the NDI plugin from cargo and then owning that build." ShowMesh now owns that build on Debian too, so that argument is spent. The two structural arguments are untouched and are why Debian stands: FPP is Debian-based and Step 9's plugin targets that family, and Raspberry Pi OS is Debian, which keeps open item 2 a continuation rather than a second port.

**Owner's decision, 2026-08-16: compile from source, script it, and ship it for day-0.** The downsides are understood and accepted: the node carries a cargo toolchain, the resulting `.so` has no distribution provenance, and a node rebuilt between seasons has to reproduce the build. The mitigation is a scripted build rather than a packaged dependency. Node build mechanics are owner work outside the build sessions.

The reproducibility risk is real and is on the punch list rather than closed: the plugin version, the `gst-plugins-rs` revision built from, and the exact build commands are not yet recorded anywhere, and the bench node is currently the only artifact of them.

### Open items for bench (L2) verification

1. ~~Validate the dynamically loaded sender into the target Resolume version on `amd64`, recording the reference-profile parameters and frame-pacing results.~~ **Done 2026-08-16, above.**
2. Validate the same sender-to-Resolume path on `arm64` with a representative surface and recorded parameters.
3. Validate the Ubuntu 24.04 release target, which the owner's 2026-08-13 decision makes a release gate rather than a day-0 gate. The Debian packaging answer above makes it likely Ubuntu needs the same source build, which is worth knowing before release rather than at it.
4. Record the plugin build recipe: `gst-plugins-rs` revision, plugin version, and build commands, so the node is reproducible without the bench machine.

The `arm64` item validates the NDI adapter and runtime path. It does not establish a Raspberry Pi / ARM renderer performance profile, which remains deferred in RES-004.

### Node distribution: Debian 13 recommended 2026-08-13, benched 2026-08-16

**The reasoning below is preserved as written, and one of its three arguments did not survive the packaging check above.** Read it with that section. Debian 13 stands as the node distribution and the bench ran on it successfully; what changed is that ShowMesh compiles the NDI plugin itself rather than installing it.

The reasoning is media-stack friction, since that is what threatens the day-0 schedule rather than stability or lifecycle.

RHEL-family distributions do not carry the GStreamer Rust plugin set in their own repositories, and EPEL does not fill the gap, so a Rocky node means building the NDI plugin from cargo and owning that build; the RHEL media stack is also deliberately conservative about codecs. Those are correct tradeoffs for a server and wrong ones for a show appliance rebuilt between seasons.

Debian is preferred over Ubuntu on structural grounds rather than taste. **FPP is Debian-based**, so nodes match the platform this project already integrates with, and Step 9's plugin targets that same family. **Raspberry Pi OS is Debian**, so RES-004's deferred ARM profile becomes a continuation rather than a second port, which also makes open item 2 above cheaper. Ubuntu 24.04 LTS is the fallback if a packaging gap appears, and carries the densest community documentation for NDI on Linux because the OBS and DistroAV ecosystem lives there.

**What must be verified before this is built on**, and it is ten minutes: that the NDI GStreamer element is actually present and loadable on the chosen distribution, confirmed from `apt-cache search` and `gst-inspect-1.0` rather than from recollection of packaging status. That check belongs to the [Track B spike](../bench/TRACK-B-NDI-SPIKE.md), and its result is recorded here. The recommendation above is reasoned from packaging behaviour **recalled rather than observed**, and this project's standing rule is that an external system's behaviour is named only from that system's own output.

**That check ran on 2026-08-16 and returned no.** See the packaging section above. The recalled claim that Debian ships a usable NDI element was wrong, which is exactly the failure mode this paragraph was written to catch.

The coordinator is unaffected: it ships distroless under [ADR-012](../decisions/ADR-012-docker-coordinator-deployment.md), so this is a node decision rather than a fleet decision.

**Debian 13 is primary; Ubuntu is a supported release target.** Owner's decision 2026-08-13. The reference installation runs Debian, and that is what gets built and tested against first. **Ubuntu must also work by release**, because it is where most people adopting this will be comfortable, and a node distribution that only ever ran on the maintainer's choice is a distribution nobody else can use. That makes Ubuntu a second validation target rather than a second first-class platform: it does not gate day-0, and it does gate calling this releasable. Both belong in RES-006's open items once the packaging check confirms what each one actually ships.

## Decision, fallback, and revalidation

Resolved: ShowMesh may vendor the MIT headers but never redistributes the NDI runtime. The NDI adapter dynamically loads a user-installed runtime with `dlopen("libndi.so.6")`, honors `NDI_RUNTIME_DIR_V6`, and degrades gracefully with an installation pointer when the runtime is absent. Required branding attributions remain part of product surfaces. NDI is the v1/reference transport behind the adapter; HDMI/capture remains supported as an alternate/fallback. Revalidate the integration after a material SDK, license, architecture, or Resolume change.

These decisions are recorded as durable constraints in [ADR-026](../decisions/ADR-026-renderer-surface-model-and-reference-transport.md). Its decision 6 makes the absent-runtime behaviour binding rather than adapter-local: the node **starts**, keeps every other capability, does not advertise a usable NDI sender, and reports an installation pointer. That is the same rule [ADR-025](../decisions/ADR-025-agent-fallback-cache-is-signed.md) settled for a failed cache verification, and it is recorded because a missing optional dependency is exactly the kind of thing an implementer turns into a fatal startup check without noticing they have converted a degradation into an outage.
