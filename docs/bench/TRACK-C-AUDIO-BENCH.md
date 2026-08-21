# Track C seam C0a-1: the device-independent GStreamer audio bench

[Track C](../build/TRACK-C-audio-node.md) · [RES-007](../research/RES-007-audio-node-architecture.md) · [AUDIO-ENGINE](../architecture/AUDIO-ENGINE.md) · [ADR-017](../decisions/ADR-017-showmesh-owns-audience-audio.md) · [ADR-018](../decisions/ADR-018-program-and-ltc-share-a-clock-domain.md)

Status: **run 2026-08-18**, on `track-c/audio-node` at `526c32a`. This is the capture record for C0a-1, RES-007's first bench item. A second, distinct bench (the `gstengine` phase 1 spike, real GStreamer on macOS arm64, 2026-08-20) is recorded in its own section below; the two must not be conflated.

The LTC measurements in this record use a pre-rendered file produced by the one-shot `bench/audio-node/ltcgen.c` helper. They validate that candidate's captured graph behavior only. The production decision now recorded in SM-69 is live libltc generation linked into the agent through cgo and fed into the go-gst engine; this bench did not exercise that design and supplies no evidence for it.

## Environment

- Container built from `bench/audio-node/Dockerfile`: **Debian 13.6**.
- GStreamer **1.26.2** (`gst-inspect-1.0 version 1.26.2`), packages: `gstreamer1.0-alsa` 1.26.2-1+deb13u1, `gstreamer1.0-plugins-bad` 1.26.2-3+deb13u2, `gstreamer1.0-plugins-base` 1.26.2-1+deb13u1, `gstreamer1.0-plugins-good` 1.26.2-1+deb13u2, `libltc11` 1.3.2-1+b2.
- Default sample rate **48000 Hz** (per-run rate is also recorded in each result JSON; R7's negotiated default differs — see below).
- **Host: an arm64 Docker VM on a macOS laptop.** Not the reference installation's hardware, not Linux bare metal, and not the eventual audio node's OS/kernel/driver stack.
- Audio device: the userspace `null` ALSA PCM device declared in `bench/audio-node/asound.conf`. No `/dev/snd`, no kernel sound module, no real driver anywhere in this container.
- Full manifest: `bench/audio-node/results/manifest.json`. Raw logs: `bench/audio-node/results/logs/*.log`.

## What this bench proves, and what it cannot

**Proves:** real GStreamer 1.26.2 element and pipeline behaviour on Debian 13, in a container, measured against captured files and captured log output — never against listening or against an element's own claim of success.

**Cannot prove, and does not claim to:**

- **Anything about a physical audio interface.** Every run uses the null ALSA PCM device or a synthetic file/`appsrc` source. A physical interface's channel independence (the "mirror problem" RES-007 names), its actual clock, its driver's latency, its hot-plug behaviour, and its true achievable sample rates are per-device commissioning questions (Track C's C0b), owned by hardware the owner has not yet selected, and this bench is structurally incapable of answering them.
- **Drift over a real show.** Nothing here ran for 30–60 minutes against a live timeline; the longest single measured artifact is R3's wrap file at 192,100 samples (~4 seconds).
- **R1 in particular is a graph property, not a mirror-problem answer.** R1 shows that `interleave` assembles three independently-generated mono streams into one multichannel stream with exact-zero crosstalk *in the graph*. It says nothing about whether a physical interface's second output pair is independently addressable downstream of the driver — RES-007 names that as invisible to software by construction, and only a signal-generator-and-cable check on the real interface (C0b) can answer it.

## R1 — channel separation

`interleave` (not `audiomixmatrix`) combines three independently-generated mono streams into a 3-channel stream, one sink pad per channel — chosen because it assembles discrete channels with no coefficient matrix in the path, so there is no crosstalk to introduce by construction.

Measured (`results/r1_channel_separation.json`): with a live 1 kHz tone on the LTC-designated channel, the program pair read **exact zero** RMS and peak (`per_channel_rms: [0.0, 0.0, 18535.32]`, `per_channel_peak: [0, 0, 26213]`).

## R2 — program/LTC alignment (five cases)

- **Baseline (start):** program edge sample 24001, LTC edge sample 24000 — **1 sample offset (~0.02 ms)**. (`results/r2_baseline.json`)
- **Restart:** three independent `gst-launch-1.0` process invocations of the identical pipeline each reproduced the identical **1-sample offset**. (`results/r2_restart.json`)
- **Track change:** a mid-timeline program item switch landed its new item's onset **2 samples** after the arithmetically expected sample (expected 72000, measured 72002), with the LTC branch continuous and untouched. (`results/r2_trackchange.json`)
- **Seek:** a real GStreamer flushing+accurate `SEEK` event landed **exactly on the requested sample, 0 samples of error**, across three targets (0.5 s → 24000, 1.0 s → 48000, 2.0 s → 96000). (`results/r2_seek.json`) The committed results are from one full `make bench-audio` run; the builder reported a second run agreeing with it, and that second run's output was not captured, so it is not claimed here. Getting a trustworthy number here required fixing two defects, documented as worked examples in `bench/audio-node/runs/r2_seek.py` and `r2_seek_source.sh`: an early version issued the seek from an `ASYNC_DONE` bus-message handler instead of after a blocking `get_state()` call and intermittently raced the pipeline's own teardown into a silent 0-byte output; and the first attempt used a pure 1 kHz tone as the seek source, which repeats bit-for-bit every period and so cannot be located by a byte-match search — switching to `wave=white-noise` fixed it.
- **Gain change:** not measured as a separate R2 case; R4 measures the same `GstController` mechanism directly.

## R3 — LTC generation survey (the run that could have come back negative, and partly did)

**No installed GStreamer plugin (base/good/bad, 1.26.2) generates LTC audio.** `timecodestamper` and `avwait` embed or consume SMPTE timecode as *video buffer metadata* and never touch an audio sample. (`results/r3_ltc_survey.json`)

Debian 13 ships `libltc11`/`libltc-dev` — the real Manchester-biphase encoder library, upstream x42/libltc — but no prebuilt `ltcgen` binary and no `ltc-tools` package. `bench/audio-node/ltcgen.c` is a ~50-line program built against that real library.

**The working approach is candidates b and c together**: a pre-rendered LTC file (b), produced by an external generator built against the real libltc encoder (c), played back as an ordinary `filesrc`/`rawaudioparse` source in the same pipeline as program. This keeps sample generation outside Go and outside ShowMesh's runtime. Every R2 alignment number above is measured through this candidate, since every R2 pipeline plays a `ltcgen`-produced file.

**Timecode midnight wrap** (`23:59:58` crossing `00:00:00`): no dropout or discontinuity. RMS in a 1000-sample window straddling the wrap (22765.5) and a reference window elsewhere in the same file (22772.2, 22818.8) matched closely; max sample-delta across the wrap window and the reference window were identical (54271). (`results/r3_ltc_wrap.json`)

## R4 — click-free ducking

A real `GstController.InterpolationControlSource` bound to a `volume` element's `volume` property via `DirectControlBinding.new_absolute` — **not** `new()`, which maps a 0..1 control value onto the property's *full range* (0..10 for `volume`), which was found by inspecting the captured WAV to turn a requested gain of 1.0 into an actual 10x boost that clipped the signal into a square wave — drove a 200 ms linear fade from 1.0 to 0.2.

**A raw sample-to-sample delta does not distinguish the fade from an abrupt step at all**: ramped and abrupt both read `max_delta: 3421` (`results/r4_ducking.json`), because a periodic tone's own per-sample slope exceeds either transition's own contribution. The metric that does distinguish them is windowed-envelope discontinuity (`envelope_max_step` in `bench/audio-node/runs/analyze.py`, ~1-cycle windows): ramped **158**, abrupt step (two `concat`-joined segments at static, different volumes) **20978** — about 133x.

## R5 — item transition gap

`concat` splices two 1-second/48kHz items with **exactly zero** inserted or dropped samples: total length equals the exact sum of the two items, 96000 samples. (`results/r5_transition_gap.json`)

Ordinary sequential playback (two independent `gst-launch-1.0` processes, the second started only after the first exits) has no sample-domain gap in this capture either, but that is **not** evidence that real sequential playback is gapless — neither source pipeline is a live, wall-clock-paced source, so the real cost never appears in the sample domain. The measured wall-clock cost of starting a second independent `gst-launch-1.0` process on this host is **~9.4 ms** (0.00953 s).

## R6 — ALSA sink path with no hardware

A real `alsasink` negotiated, prerolled, played, and reached EOS against the userspace `null` PCM device declared in `asound.conf`, with no `/dev/snd` and no kernel module present. Exit code 0. (`results/r6_null_sink.json`)

## R7 — runtime capability discovery

`aplay -L` reports exactly `null` and `default` (aliased to `null`); `aplay -l` finds no hardware cards. **`gst-device-monitor-1.0` is not present in this Debian 13 package set** — `gstreamer1.0-tools` ships `gst-inspect`, `gst-launch`, `gst-stats`, `gst-tester`, and `gst-typefind` only, confirmed via `dpkg -L`. Free negotiation against the null sink settled on **44100 Hz / F64LE / stereo** — a fact about this container's virtual stack, and, per the opening section above, not a claim about a physical interface's negotiated rate. (`results/r7_capability_discovery.json`)

## Open questions only the owner or C0b can settle

- Whether `gst-device-monitor-1.0` should be added to the shipped audio node image, or whether C1's capability advertisement should be built against `aplay -L`/ALSA APIs directly instead.
- Whether the sub-millisecond start/restart/track-change/seek offsets measured here hold once a real interface's own driver latency is in the path — none of it was measured against real hardware.
- Whether `new_absolute()` (this bench's fix) is the binding convention the eventual C3 engine should standardize on everywhere a controller drives a `volume`-or-similar 0..N range property.

## Phase 1 spike (`internal/agent/audio/gstengine`), run 2026-08-20, distinct from the run above

This is a **separate bench, on a separate platform**, and the two must not be conflated. The record above is real GStreamer **1.26.2 on Debian 13.6, in a container**, against a null ALSA PCM device. This section is real GStreamer **1.28.6 on macOS arm64, on the bare development machine, through the `go-gst` bindings**, with no container involved. **Neither is hardware**, and neither is Linux evidence for the `gstengine` Go package specifically: this spike's platform is not the audio node's target OS, kernel, or audio stack, and it has no ALSA at all.

**Purpose:** answer five design questions for `internal/agent/audio/gstengine`'s topology (one continuously running pipeline, one `audiomixer` per program channel into one `interleave`, a permanent silence branch per unclaimed channel, and a per-session decode branch) before writing the production package against them. Committed as its own Go module at `bench/audio-node/spike-phase1/`, with captured logs.

**Environment:** macOS arm64, GStreamer 1.28.6, `go-gst` (pinned at the `v0.0.2` tag in the production package; the spike module may differ). No container, no ALSA, no `/dev/snd` equivalent: this is the development laptop's native GStreamer install.

**Findings, each answered by observation against a real running pipeline:**

1. **A branch can be added to and removed from a running `audiomixer` with no stall.** Buffer count observed climbing 51 → 101 → 150 across an add/remove cycle.
2. **One branch can be paused while the mix continues**, using `ignore-inactive-pads=true` on the mixer plus a pad-blocking probe on the branch to pause. Observed: the blocked branch froze at 22 buffers while the mixer's own buffer count continued climbing, reaching 72.
3. **A `GstController` ramp reads back accurately**, and the bench's own earlier `new()`-versus-`new_absolute()` hazard (recorded above, R4) did not reproduce when the absolute binding is used: a ramp from 1.0 to 0.3 over 500 ms read 0.9998 near the start, 0.6747 mid-ramp, and 0.3000 after completion.
4. **Per-branch end of media is distinguishable from pipeline EOS.** A branch's own `EventEOS` fired while the pipeline bus never emitted `MessageEOS`, confirming a finished session does not need to (and must not) end the shared output pipeline.
5. **`interleave` places content on chosen channel indices by sink pad request order, not by any per-pad property.** Sink pads must be requested in the exact order intended for the output channel layout; there is no property that fixes the mapping independently of request order. This is the fact `gstengine`'s "interleave sink pad requested in ascending 1-based order" topology rule is built on.

**What this spike does not answer, and does not claim to:** anything about a physical audio interface, ALSA, Linux driver behavior, real multichannel hardware, or sound actually produced. No sound was produced or heard in this spike; every measurement came from buffer counts, event names, and read-back property values on a synthetic pipeline.
