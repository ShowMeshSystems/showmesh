# RES-007: Audio-Node Architecture

[Architecture](../architecture/ARCHITECTURE.md#45-audio-engine) · [Audio Engine specification](../architecture/AUDIO-ENGINE.md) · [Tracker](README.md) · [ADR-017](../decisions/ADR-017-showmesh-owns-audience-audio.md) · [ADR-018](../decisions/ADR-018-program-and-ltc-share-a-clock-domain.md) · [ADR-019](../decisions/ADR-019-audio-device-loss-fails-silent.md) · [Resolume SMPTE research](RES-001-resolume-smpte-behavior.md)

Status: planned (bench) · Risk: critical · Verification: **split — L2 for GStreamer graph/pipeline behaviour in a container (C0a-1, 2026-08-18); L0 for everything hardware- or device-dependent**

## Decision to make

Establish whether the audio architecture decided on 2026-08-10 is achievable on real hardware, and produce the numbers it depends on but does not contain.

Three ADRs settled the questions this record originally asked about clock ownership and playback model: ShowMesh owns audience-facing audio and nodes play local media against their own clock (ADR-017), program and LTC share one clock domain (ADR-018), and audio device loss fails silent with no automatic FPP fallback (ADR-019). Those decisions are architectural intent at L0. **Nothing below has been prototyped**, and this record is now the work queue that either confirms them or forces a superseding ADR. The physical questions block commissioning claims for a particular interface and Day-0 readiness on that device; they do **not** block building the device-agnostic engine, sessions, routing, mixing, telemetry, or runtime capability model.

## Required use cases

- Show audio synchronized to the FPP timeline.
- Background and ambient music outside scheduled shows.
- Deterministic crossfades into pre-show, live, intermission, and post-show states.
- Announcements ducked over show or background audio.
- Independent LTC output without consuming the stereo program pair, from the same clock as program.
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

**R3 answers the record's own "can LTC be generated within the same pipeline" question, and the answer is substantive: no installed GStreamer plugin (base/good/bad, 1.26.2) generates LTC audio.** `timecodestamper`/`avwait` operate on video-buffer timecode metadata and never touch an audio sample. The working approach is a pre-rendered LTC file produced by an external generator (`bench/audio-node/ltcgen.c`, built against Debian 13's real `libltc11`/`libltc-dev`, upstream x42/libltc) played back as an ordinary file source in the same pipeline as program — never a native pipeline element and never sample generation inside Go. Every R2 alignment number above is measured through this candidate. The timecode midnight wrap (`23:59:58`→`00:00:00`) showed no dropout or discontinuity.

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

### C5 implements live LTC generation, and this is a design decision, not new evidence (2026-08-18)

Track C's C5 seam (`f7743c5`) implements the owner's ruling (Linear SM-69, SM-83) that LTC is generated live by a supervised external `libltc`-based process rather than played from a pre-rendered file, with a closed frame-rate vocabulary of 24/25/29.97/30 and non-drop shipped at every rate. That ruling settles *what ShowMesh builds*; it settles nothing this record measures. C5's generator output reaches a fake pipeline, program on channels 1–2 and LTC on a discrete channel 3 is proven only as a graph-routing fact by its own tests, and no physical output, alignment, or drift number exists behind it. **This does not raise this record's verification split above L0 for anything hardware- or device-dependent**: the mirror problem, physical channel independence, real-device alignment, drift, hot-plug, and PipeWire-versus-ALSA-on-real-hardware remain exactly where the 2026-08-14 note above left them, unreached, and C0b commissioning is still gated on an interface that is not yet selected.

## Decision, fallback, and revalidation

The architecture is decided (ADR-017, ADR-018, ADR-019) and unverified. If bench work shows the node path cannot meet the acceptance criteria — most plausibly if GStreamer cannot hold LTC alignment to program — the correct response is a superseding ADR, not moving sample generation into ShowMesh code, which [ADR-006](../decisions/ADR-006-go-implementation-language.md) and [ADR-007](../decisions/ADR-007-gstreamer-media-engine.md) forbid.

FPP-hosted stereo playback is no longer the automatic fallback; ADR-019 replaced it with fail-silent plus a documented operator procedure. FPP-hosted audio remains available as a deliberate operator choice and as the position to return to if ADR-017 is superseded.

Revalidate after audio engine, kernel, driver, interface, PipeWire, or timing-source changes.
