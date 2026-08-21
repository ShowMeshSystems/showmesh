# RES-007: Audio-Node Architecture

[Architecture](../architecture/ARCHITECTURE.md#45-audio-engine) · [Audio Engine specification](../architecture/AUDIO-ENGINE.md) · [Tracker](README.md) · [ADR-017](../decisions/ADR-017-showmesh-owns-audience-audio.md) · [ADR-018](../decisions/ADR-018-program-and-ltc-share-a-clock-domain.md) · [ADR-019](../decisions/ADR-019-audio-device-loss-fails-silent.md) · [Resolume SMPTE research](RES-001-resolume-smpte-behavior.md)

Status: bench partially run · Risk: critical · Verification: **split: L2 for recorded GStreamer graph/pipeline behaviour, on two platforms (C0a-1 et al., a Debian container, 2026-08-18; the `gstengine` phase 1 spike and the libltc-cgo LTC branch, macOS arm64, 2026-08-20); L0 for everything hardware- or device-dependent**

## Decision to make

Establish whether the audio architecture decided on 2026-08-10 is achievable on real hardware, and produce the numbers it depends on but does not contain.

Three ADRs settled the questions this record originally asked about clock ownership and playback model: ShowMesh owns audience-facing audio and nodes play local media against their own clock (ADR-017), program and LTC share one clock domain (ADR-018), and audio device loss fails silent with no automatic FPP fallback (ADR-019). Container work below prototyped graph construction, captured routing, fades, seeks, transitions, and decode behavior. It did not prototype the production engine or the owner-selected live libltc path at the time it ran; the live-libltc path is now built and measured on macOS arm64 (branch `track-c/sm-69-ltc`, 2026-08-20, not yet merged — see the subsection below), and it still cannot answer physical questions. Those remain the work queue that either confirms the architecture or forces a superseding ADR.

## Required use cases

- Show audio synchronized to the FPP timeline.
- Background and ambient music outside scheduled shows.
- Deterministic crossfades into pre-show, live, intermission, and post-show states.
- Announcements ducked over show or background audio.
- Independent LTC output outside the configured program channel set, from the same clock as program. The reference program layout is stereo, while a mono installation may use program channel 1 and LTC channel 2.
- Multichannel USB interfaces, with optional Dante as an additional output.
- Metering, device health, underrun reporting, and local recovery.

## Questions

**Rendering (bench evidence required to validate ADR-007 applied to audio; AUDIO-ENGINE §7)**

- Can agent-supervised GStreamer pipelines deliver click-free ducking, reliable gapless playback, and multichannel interleave on the target Linux host?
- Can LTC be generated within the same pipeline and remain sample-aligned to program audio, or does it require an element or approach that does not exist? What is the actual alignment achieved, in samples?
- Does the pipeline hold alignment across track changes, seeks, gain transitions, and process restarts?

**Drift (bench evidence required to validate ADR-017's correction policy)**

- What drift does a free-running audio node accumulate against the FPP show timeline over a 30–60 minute show, on the intended hardware?
- What audio-to-lighting offset is perceptible to an audience, and therefore what threshold should the ignore band use?
- Is correction at track boundaries sufficient in practice, or does a show reach the significant-drift threshold mid-track?
- Is a discrete seek correction audibly acceptable when it does occur?

**Clock domains (per-device evidence required for ADR-018 placement and commissioning)**

- What program-to-LTC alignment is achieved on the selected interface, and how does it behave over a full show?
- How many usable output channels from one clock does the candidate interface actually provide, and does the driver expose them as one device?
- What happens to alignment on device hot-plug, sample-rate change, and PipeWire versus raw ALSA paths?

**Failure behavior (bench evidence required to validate ADR-019)**

- How quickly is device loss detected, and how long is the unreported silent window?
- Does session and show state survive device loss, device return, and engine process restart?
- Does anything reach the FM transmitter from a path ShowMesh did not intend during any failure case?
- What must be verified on a standby node, and can that verification be maintained continuously rather than performed at failover time?

**Interaction with the FPP show timeline**

- [ADR-017](../decisions/ADR-017-showmesh-owns-audience-audio.md) requires FPP's own audio output to be disabled or unused on the primary controller. "Disabled or unused" is ambiguous between muting the output device and not loading the media at all, and the difference may not be inert: FPP derives the `SecondsElapsed` field that MultiSync carries from its own media playback, and that field is the show timeline ShowMesh aligns audio *to*. Which interpretation preserves the timeline unchanged, and whether either alters it, is unverified and must be settled by capture rather than reasoning. Relates to [RES-002](RES-002-fpp-multisync-compatibility.md).

**Media and platform**

- Media distribution, hashing, and per-node availability verification: what is sufficient before a node may hold audio authority?
- Can useful ideas be reused from BackgroundMusicFPP without making it a hard dependency?
- What breaks first if the audio node is a Raspberry Pi-class device rather than the Dell Micro 7040?

## Acceptance criteria

- Transitions are click-free, repeatable, and measurable.
- LTC remains valid and aligned with program audio, with the achieved alignment recorded as a number rather than as an impression.
- Drift over a full-length show is measured and a threshold is set from that measurement.
- Device loss is detected within a recorded interval, session state survives it, and no unintended audio path activates.
- Coordinator loss does not interrupt current playback.
- Show start, correction, and recovery behavior are defined and reproducible.
- An overnight background-and-show cycle soak shows no leaks and no accumulating offset.

## Test method

The bench has two stages and the first begins without the final interface.

**Device-independent software bench:** prototype an N-output engine on a representative Linux host using GStreamer virtual/null sinks, ALSA loopback or PipeWire virtual devices where useful, and any available Linux-supported interface. Drive capabilities and channel maps from runtime discovery. Exercise decode, program/LTC graph separation, every session transition, playlists, gain/duck/fade behavior, telemetry, coordinator and FPP timing loss, process restart, missing media, hash mismatch, and injected device errors. This validates ShowMesh behavior and pipeline construction; it makes no claim about a future interface's physical channel independence, clock, driver, latency, or hot-plug behavior.

**Per-device commissioning:** when a candidate show interface is available, run the physical tone-separation check and record program audio, LTC, and a visual reference together so alignment is measurable rather than judged live. Exercise device unplug/replug, sample-rate change, physical routing, driver behavior, power restoration, and Dante interruption where applicable. Measure alignment in samples, drift in milliseconds against elapsed show time, detection latency in seconds, and underrun counts. A failure rejects that interface or route; it does not send the implementation back to the beginning.

## Evidence and findings

### C0a-1: the device-independent GStreamer bench, run 2026-08-18 (L2 for graph behaviour)

Full capture record: [docs/bench/TRACK-C-AUDIO-BENCH.md](../bench/TRACK-C-AUDIO-BENCH.md). Seven runs, real GStreamer 1.26.2 on Debian 13.6 in a container on an arm64 Docker VM on a macOS laptop, against a userspace null ALSA PCM device (no `/dev/snd`, no kernel sound module, no real driver). Every number below came from a captured WAV analyzed by `bench/audio-node/runs/analyze.py` or a captured log line, never from listening or from an element's own claim of success.

**This moves the rendering questions this record's own §"Rendering" asks — pipeline construction, graph-level program/LTC separation, alignment across restart/track-change/seek, click-free ducking, item transition, and a real ALSA sink path — to L2: safe to keep building against, not trustworthy in a live show.** It does **not** move any hardware- or device-dependent question off L0: physical channel independence (the mirror problem), real-device alignment, drift over a real show, hot-plug, sample-rate change, device-loss detection latency, or PipeWire versus ALSA on real hardware are all untouched by this run and stay L0 pending C0b (per-device commissioning) and later Track C seams (C5–C7) run against a real interface.

**Channel separation (R1) is a graph property and is explicitly not evidence about the mirror problem.** `interleave` produced exact-zero RMS/peak crosstalk on the program pair with a live tone on the LTC channel — a fact about the GStreamer graph having no coefficient matrix in the path, not a fact about whether a physical interface's own driver exposes a mirrored second output pair downstream of anything ALSA can observe. That question is unreachable by this bench by construction and remains a C0b commissioning check.

**Program/LTC alignment (R2), measured in samples at 48 kHz:** baseline start offset 1 sample (~0.02 ms); three independent process restarts each reproduced the identical 1-sample offset; a mid-timeline track change landed onset 2 samples after the arithmetic expectation; a flushing+accurate seek landed exactly on the requested sample (0 samples of error) across three targets.

**R3 answers the record's own "can LTC be generated within the same pipeline" question, and the answer is substantive: no installed GStreamer plugin (base/good/bad, 1.26.2) generates LTC audio.** `timecodestamper`/`avwait` operate on video-buffer timecode metadata and never touch an audio sample. This bench's own candidate was a pre-rendered LTC file produced by an external generator (`bench/audio-node/ltcgen.c`, built against Debian 13's real `libltc11`/`libltc-dev`, upstream x42/libltc) played back as an ordinary file source in the same pipeline as program. Every R2 alignment number above is measured through that candidate. The timecode midnight wrap (`23:59:58`→`00:00:00`) showed no dropout or discontinuity. **Superseded as the production design 2026-08-19/20 (see the C5 subsection below): the owner instead ruled for libltc linked into the agent through cgo, generating PCM live into an `appsrc` inside the running pipeline, with no pre-rendered file and no external generator process.** The "no installed GStreamer plugin generates LTC audio" fact stands; it is libltc via cgo that fills the gap, not a GStreamer element and not a file.

**Click-free ducking (R4):** a raw sample-to-sample delta does not distinguish a 200 ms linear fade from an abrupt step at all (both measured `max_delta: 3421`) — the periodic test tone's own per-sample slope dominates either transition. The windowed-envelope metric does distinguish them: ramped 158, abrupt 20978, about 133x. Also found: `GstController.DirectControlBinding.new()` maps a 0..1 control value onto a property's *full range* rather than 0..1 directly — on `volume`'s 0..10 range this turned a requested gain of 1.0 into a 10x boost that clipped into a square wave; `new_absolute()` is the correct binding for a controller driving a gain-like property.

**Item transition (R5):** `concat` splices with exactly zero inserted/dropped samples. Ordinary sequential playback (two independent processes) showed no sample-domain gap either, but that is not evidence real sequential playback is gapless, since neither pipeline is a live wall-clock-paced source; the real, measured cost of starting a second independent process on this host is ~9.4 ms wall-clock, which never appears in the sample domain.

**R6/R7:** a real `alsasink` reached PLAYING and EOS against the null device with no hardware present (exit 0). `gst-device-monitor-1.0` is **not** in this Debian 13 package set (`gstreamer1.0-tools` ships `gst-inspect`/`gst-launch`/`gst-stats`/`gst-tester`/`gst-typefind` only) — a fact for whoever builds C1's capability discovery, not an assumption. Free negotiation against the null sink settled on 44100 Hz/F64LE/stereo, a fact about this container's virtual stack only.

**Not reached by this run, and still open:** every physical-interface question in this record's §"Clock domains" and §"Drift" (channel count/addressing on a real driver, PipeWire vs. ALSA, hot-plug, sample-rate change, actual drift over 30–60 minutes, device-loss detection latency, whether anything reaches an FM transmitter unintended). Interface selection itself remains open per the 2026-08-14 note below.

### C1a: real-device probe test, run 2026-08-18 in a Linux container with real GStreamer and ALSA (L2 for graph behaviour)

Two measurements, made by the orchestrator personally inside a Linux container (image: golang trixie plus `gstreamer1.0-{tools,plugins-base,plugins-good,alsa}`), not by the builder and not on any bench host.

**`TestProbeOutputAgainstRealNullDevice` passes against real GStreamer and ALSA, and fails when the code under test is mutated to lie.** Run unmodified: pass. Run against `ProbeOutput` mutated to return the *requested* channel count and sample rate instead of the values the pipeline actually negotiated: fails, `"Channels = 0, want at least 1"` and `"Rate = 0, want a positive negotiated rate"`. This is the same discipline the project's own LESSONS entry above ("a test's name is a claim") asks for, applied to this seam: the test does not merely pass, it demonstrably fails when the property it claims to check is broken, so it is evidence the achieved channel count and sample rate in a capability advertisement come from the real pipeline's negotiation rather than from echoing the request back unexamined.

**The fixed probe pipeline is measured silent; the pre-fix one measured an audible tone.** Fixed pipeline: peak absolute sample 0 across 51,200 frames (1.07 s at 48 kHz). Pre-fix pipeline (`audiotestsrc wave=sine`, default `volume=0.8`): peak absolute sample 26,213, roughly -2 dBFS. Both numbers are container measurements of a real GStreamer pipeline running against real ALSA (a null-capable software device inside the container, not a physical interface), and both fold into this record's existing L2-for-graph-behaviour finding above — they are further evidence about GStreamer graph and pipeline behaviour, not about a physical interface. **This does not move the mirror problem, physical channel independence, real-device alignment, drift, hot-plug, sample-rate change, or PipeWire-versus-ALSA-on-real-hardware off L0.** Those remain C0b commissioning questions, unreached by this run for the same structural reason C0a-1 could not reach them: a null/virtual device makes no sound and exposes no physical routing, regardless of what the pipeline is asked to emit. See LESSONS.md, "A diagnostic that exercises a real output is itself show-affecting," for the full finding this measurement supports.

### C2: discoverer versus full decode, measured 2026-08-18 in a Debian container with real GStreamer 1.26.2 (L2 for graph behaviour)

One measurement, made by the orchestrator personally, not by the builder and not on any bench host: a ten-minute FLAC probed two ways. Full decode to a temp PCM file (the media probe's first implementation): 658 ms, 110 MB written. `gst-discoverer-1.0` against the same file: 11 ms, nothing written, `Duration: 0:10:00.000000000` reported exactly rather than inferred from a decoded byte count. This is a further container measurement of GStreamer graph/tool behaviour and folds into this record's existing L2-for-graph-behaviour finding; it says nothing about a physical interface and moves nothing off L0. It is the evidence behind `internal/agent/audio/mediaprobe.go`'s choice to source metadata from `gst-discoverer-1.0` while keeping a separate bounded decode (a few buffers into a fakesink, writing nothing) as the actual decodability gate, since a discoverer success is not proof a file decodes — see TRACK-C-audio-node.md's C2 section for the truncated-FLAC case this split is built to catch.

### Interface selection is reopened, and is no longer a prerequisite (2026-08-14)

**Superseding the 2026-08-13 note that recorded the Behringer U-Phoria UMC204HD as purchased.** That order turned out to be a backorder, the unit is out of stock widely, and the owner will reselect against requirements beyond this project. Preserved below because the reasoning it contains still applies to whatever is chosen.

**The occasion was the backorder; the decision is structural.** [ADR-018](../decisions/ADR-018-program-and-ltc-share-a-clock-domain.md) names a property, not a product, and gating the audio track on one consumer unit behaving as advertised was the wrong shape regardless of stock levels. **ADR-018 is unchanged and needs no supersession**: program and LTC still leave one interface in one clock domain, LTC still lands on a discrete output that program never touches, and program on USB with LTC on Dante is still forbidden.

**What changes is where the property is checked.** The engine is built against the channel count and output addressing the audio stack reports at run time, so any multichannel interface with working Linux support is a candidate and no device model appears in the code. The ADR-018 placement constraint is then enforced from advertised capability attributes, meaning a node whose interface cannot supply a discrete output outside the program pair is refused placement by the coordinator. Recorded in [Track C](../build/TRACK-C-audio-node.md), which owns the delivery.

**What may proceed before the show interface exists:** the GStreamer graph builder, runtime capability discovery, N-output routing, local asset decode, playback sessions, background playlists, announcements, gain/fade/duck behavior, command idempotency, telemetry, mock failures, and the generic output-adapter contract. Virtual devices and any available Linux-supported interface are valid tools for that work. They do not raise the physical-interface questions above L0; only per-device commissioning can do that.

**The mirror problem is real and stays on the test list, as a per-device commissioning check.** This record's own clock-domain question asks how many usable output channels from one clock an interface provides *and whether the driver exposes them as one device*. On consumer interfaces a second output pair is sometimes a **hardware mirror** of the main pair rather than an independently addressable one, and that is only partly visible to software: a driver can report four outputs on a unit that routes the second pair from the first downstream of anything ALSA can observe. Where it is true, LTC sums into program audio, which is close to inaudible on a casual listen, corrupts timecode, and would be discovered during a show.

So for **whichever interface is in use**: confirm under Linux that a signal sent to the intended LTC output alone appears there and **not** on the program pair. That is a signal generator, a cable, and twenty minutes. A failure blocks that interface rather than the track, and the answer is a different interface rather than a software workaround. Record the result **with the device named**, since the result does not generalise to another unit.

**Unverified and deliberately not asserted for any candidate:** Linux class-compliance behaviour, channel map under ALSA versus PipeWire, achievable sample rates, and whether a mode switch affects output addressing. These are test parameters, recorded as questions rather than guessed at, per this project's rule that an external system's behaviour is named only from that system's own output.

**One candidate already on hand, recorded rather than recommended.** The owner has a Behringer X Air mixer, which is a multichannel USB device and is sufficient to generate LTC for [RES-001](RES-001-resolume-smpte-behavior.md)'s bench. Nothing about its Linux behaviour, channel map, or suitability as the show's audio interface has been checked, and this note is not a selection.

### gstengine phase 1 spike, run 2026-08-20 (L2 for graph behaviour, macOS only)

Committed at `bench/audio-node/spike-phase1/` as its own Go module with captured logs, run against real GStreamer **1.28.6** through **`go-gst`** on **macOS arm64**, on the bare development machine with no container and no ALSA. This is a **different platform from C0a-1/C1a/C2 above**, which are Debian-container measurements, and the two must not be merged into one claim. See `docs/bench/TRACK-C-AUDIO-BENCH.md`'s phase 1 section for the full record.

Five questions the `internal/agent/audio/gstengine` package's topology depends on, each answered by observing a real running pipeline rather than by reasoning about GStreamer's documented behaviour:

1. **A branch can be added to and removed from a running `audiomixer` with no stall.** Buffer count observed 51 → 101 → 150 across an add/remove cycle.
2. **One branch can be paused while the mix continues**, using `ignore-inactive-pads=true` plus a pad-blocking probe: the blocked branch froze at 22 buffers while the mixer's own count continued to 72.
3. **A `GstController` ramp reads back accurately when bound with `new_absolute()`**: 1.0 to 0.3 over 500 ms read 0.9998 near the start, 0.6747 mid-ramp, 0.3000 after. This bench's earlier `new()`-versus-`new_absolute()` 10x hazard (R4, C0a-1 above) did not reproduce under the absolute binding.
4. **Per-branch end of media is distinguishable from pipeline EOS**: a branch's own `EventEOS` fired while the pipeline bus never emitted `MessageEOS`.
5. **`interleave` places content on chosen channel indices by sink pad request order, not by any per-pad property.** This is the fact `gstengine`'s "request sink pads in ascending 1-based order" topology rule rests on.

**This is further evidence about GStreamer graph and pipeline behaviour, on a second platform, and it moves nothing above L2.** It says nothing about a physical audio interface, ALSA, Linux driver behaviour, real multichannel hardware, channel discreteness, or program-to-LTC alignment, none of which is reachable from a synthetic pipeline on any platform. **No sound was produced or heard.** Because the platform is macOS arm64 with no ALSA, this evidence is not Linux evidence for the `gstengine` Go package specifically; that remains open until the package runs on the audio node's actual target stack.

### C5 live-LTC decision has no production evidence (reconciled 2026-08-19)

Track C's `f7743c5` commit implemented a supervised external-process contract, but review proved it had no production generator, lifecycle caller, or pipeline consumer. The owner-updated SM-69 design instead links libltc into the native agent through cgo and feeds C-generated PCM into the go-gst engine. Neither design has production or physical evidence here. The closed frame-rate vocabulary of 24/25/29.97/30 is resolved, with non-drop explicit. **Nothing hardware-dependent moves above L0**: the mirror problem, physical channel independence, real-device alignment, drift, hot-plug, and PipeWire-versus-ALSA behavior remain unreached, and C0b commissioning is still gated on an interface that is not yet selected.

### Live-LTC-via-cgo built and measured, branch `track-c/sm-69-ltc`, 2026-08-20, macOS arm64, not yet merged to `main` (L2 for graph behaviour)

**Two pipeline constants were chosen against a non-hardware sink and are not validated on a device.** The branch `queue` is bounded so decode cannot bank ahead of the mixer, and the measured run-ahead is the cap plus a fixed 99 ms, linear and constant across caps: about 219 ms at the shipped 100 ms cap, 299 ms at 200 ms, and 1.10 s with the default limits that produced the original defect. The LTC channel's liveness timeout is 200 ms against an appsrc queue lead of the same size, sampled at a worst confirmation age of 76 ms idle and 59 ms under load on this machine.

Both numbers are set by how fast a `fakesink` drains, not by how a real ALSA device pulls on a period boundary, and the failure mode of getting the queue bound wrong on hardware is the reason this belongs here rather than in a comment: a branch that underruns is skipped by the mixer's own inactive-pad handling, filled with silence from the keep-alive pad, and produces no bus message, no fault, and no change in any observation. Nothing re-anchors it afterwards, so a scheduling hiccup longer than the bound is plausibly permanent, silent loss of that branch's audio. **Commissioning must measure both on the selected interface before either value is treated as settled.**

The SM-69 design named in the subsection above is now implemented: `internal/agent/audio/ltcgen` links libltc into the agent through cgo, and `internal/agent/audio/gstengine` feeds its PCM into an `appsrc` on a dedicated `interleave` sink pad in the same pipeline as program audio. Every measurement below is real GStreamer 1.28.6 through `go-gst`, on the bare macOS arm64 development machine, no container, no ALSA — the same platform and the same caveat as the phase 1 spike above, and not to be merged into any Linux or physical-hardware claim.

**libltc's encoder was round-tripped through libltc's own decoder, not merely asserted to produce plausible-looking bytes.** All four closed-vocabulary frame rates (24, 25, 29.97, 30) were encoded at a zero and a non-zero start offset and decoded back, asserting an exact consecutive frame sequence. Non-drop is measured, not merely configured: a wall-clock hour at 29.97 advances the timecode 107,892 frames, 108 short of an hour, matching non-drop's known behaviour, and every decoded frame's drop-frame bit at 29.97 was asserted 0 — libltc sets that bit automatically near 29.97, so a C shim clears it and recomputes parity, and the round-trip decode is what proves the shim's correction actually lands in the emitted bitstream rather than only in the Go-side struct.

**A wrong-but-plausible first implementation was measured, not merely coded around.** A Go loop that pushed one LTC buffer then slept for that buffer's nominal duration ran the LTC pad 4.3% behind and pulled program audio down to 88% of real time — measured before any LTC run was even requested, because `interleave` blocks on every sink pad and a slow producer drags the whole pipeline. The fix (pacing by the pipeline's own backpressure through a permanently-producing `appsrc`, per ADR-042 §3) was verified by the same class of measurement that found the defect: three consecutive full-package `-race` runs of `internal/agent/audio/gstengine` at 70.8s, 70.7s, and 71.1s, with no repeat of the slowdown.

**Two further real-time-rate defects were found and measured the same way the R4 fade-tolerance and R5 gap findings above were: by running the pipeline and reading numbers back, not by reasoning about GStreamer's documented behaviour.** A permanent silent keep-alive pad added to keep `interleave` from stalling with no branch connected caused every later-joining branch to start mid-file, because the shared aggregator clock runs from pipeline start: measured at 25s of engine uptime, a `Start` at position 0 read back a position of 29.4s. A fade anchored to that same shared clock, rather than to the branch's own pad offset, then completed with the wrong gain and the wrong timing: a 400ms fade to 0.25 left the gain at 1.0, completing 14.5s late on a 15s-old engine. Both are fixed with per-branch pad offsets; both are recorded here because they are facts about `interleave`'s clock behaviour under a late-joining branch, independent of LTC, and would recur in any future branch type added to this pipeline shape.

**This moves nothing hardware-dependent off L0.** Every gate behind these measurements runs against `fakesink`; no sound was produced or heard. Real ALSA, real multichannel hardware, channel discreteness, drift, and program-to-LTC alignment on a physical interface remain untouched, and interface selection (SM-74) is still open. This evidence is further confidence about the GStreamer/libltc graph on macOS arm64, not Linux evidence and not physical-interface evidence, and it does not supersede or extend the C0a-1/C1a/C2 Debian-container measurements above, which stay on their own platform.

## Decision, fallback, and revalidation

The architecture is decided (ADR-017, ADR-018, ADR-019) and unverified. If bench work shows the node path cannot meet the acceptance criteria — most plausibly if GStreamer cannot hold LTC alignment to program — the correct response is a superseding ADR, not moving sample generation into ShowMesh code, which [ADR-006](../decisions/ADR-006-go-implementation-language.md) and [ADR-007](../decisions/ADR-007-gstreamer-media-engine.md) forbid.

FPP-hosted stereo playback is no longer the automatic fallback; ADR-019 replaced it with fail-silent plus a documented operator procedure. FPP-hosted audio remains available as a deliberate operator choice and as the position to return to if ADR-017 is superseded.

Revalidate after audio engine, kernel, driver, interface, PipeWire, or timing-source changes.
