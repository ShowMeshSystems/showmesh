# RES-004: Virtual-Matrix Renderer Performance

[Architecture](../architecture/ARCHITECTURE.md#44-renderer) · [Tracker](README.md) · [Transport research](RES-005-ndi-vs-hdmi-transport.md)

Status: planned (reference profile decided; bench pending) · Risk: critical · Verification: L0

## Decision to validate

A renderer node supports one or more independent logical surfaces. Each surface extracts its assigned virtual-matrix channels from a local FSEQ file, renders them to its own canvas, and owns an independent output transport or stream. FPP Connect uploads the sequence and FSEQ to the renderer node ahead of playback; the renderer does not consume a live matrix stream.

The renderer models logical surfaces, not physical projectors. A surface may feed one projector, or a single combined surface may feed a projector pair downstream in Resolume or the physical video path. That mapping is deployment configuration and does not enter the renderer object model.

**"Day-0" is mid-September 2026, not Halloween, and the two are six weeks apart.** The operator's dates, recorded 2026-08-13: ShowMesh must be able to control a real show from **mid-September**, deliberately early so bugs surface with slack before it matters; the Halloween show opens **17 October 2026**. Day-0 is the constraint and Halloween is the show it protects. Earlier drafts wrote "day-0/Halloween" as a single phrase, which reads as a late-October deadline and is wrong in the dangerous direction.

The day-0 reference profile is:

- one logical surface per x86 renderer node (`N=1`);
- 40 frames per second;
- NDI output; and
- Dell OptiPlex Micro 7040-class hardware.

The architecture supports eventual `N` independent surfaces per node, but v1 implements `N=1`. Multiple surfaces per node and Raspberry Pi 4 / ARM HDMI profiles are deferred, not excluded.

## Questions

- What pixel throughput can the reference profile sustain at 40 fps without visible pacing artifacts?
- Which CPU, GPU, memory, conversion, copy, and NDI encode costs dominate as canvas dimensions and pixel count change?
- What frame-time distribution, output jitter, and missed-frame behavior does the reference profile exhibit?
- Does the renderer remain stable through a representative full-show soak and ordinary sender/receiver restart?

## Acceptance criteria

Treat canvas width, height, and pixel count as test parameters rather than selecting one universal resolution. For each tested layout, record pixel throughput, achieved frame rate, missed deadlines, frame-time distribution, output jitter, CPU/GPU load, and memory growth.

The day-0 profile must sustain 40 fps with stable frame pacing and no visible pacing faults over a representative full-show soak. Report jitter and missed frames as observed results; do not hide them behind an average frame rate.

## Test matrix

Start with one logical surface on a Dell OptiPlex Micro 7040-class x86 node, local FSEQ virtual-matrix extraction, and NDI output. Exercise representative layout dimensions and pixel counts, including low- and high-motion content, then record the parameters with every result.

Additional logical surfaces, Raspberry Pi 4 / ARM nodes, and HDMI output are later profiles. They require their own measured capability claims before use but are not prerequisites for closing the day-0 profile.

## Evidence and findings

### First renderer measurements, 2026-08-17 (Track B seam B3)

The first numbers produced by a real renderer rather than by `videotestsrc`. Captured on the **development laptop** (macOS, arm64), not on renderer hardware, so they bound the algorithm and say nothing about the OptiPlex.

| Measurement | Value |
|---|---|
| Extraction cost, warm | **272 µs** for a 45,241-byte buffer |
| Extraction cost, cold (block decompress) | 1.49 ms |
| Implied extraction ceiling | ~3,670 fps at that buffer size |
| Achieved frame rate | **38.97 fps** against a 40 fps target |
| Late frames | 0 |
| Dropped frames | 0 |
| Buffer size in that run | 30,000 bytes per frame |
| Run length | 1 second |

Real components in that run: the owner's real 303 MB show FSEQ, a real `multisync.Listener` and `Timeline` driven by real wire-format packets over a real loopback UDP socket, and a real `gst-launch-1.0` 1.28.6 subprocess reaching `PLAYING` and receiving frames over a real stdin pipe.

**What this establishes.** Extraction is not the bottleneck, by roughly two orders of magnitude against a 25 ms frame period. That is the question [ADR-040](../decisions/ADR-040-renderer-extracts-channels-gstreamer-owns-frames.md) decision 2 needed answered, and it is why that boundary is defensible.

**What it does not establish, and this record stays L0 for the profile.** The run was one second, on the wrong hardware, at a 30,000-byte buffer rather than a show-sized one, into a `fakesink` rather than an NDI sink, and **no real `fppd` was involved** — the MultiSync packets were synthesized with this project's own codec, so the test shares any misunderstanding that codec has. None of section 3's acceptance criteria is a one-second run on a development machine. ADR-026 decision 5's profile remains **intended rather than supported**.

**The 38.97 fps is close to a tautology and the 272 µs is the real measurement.** A frame writer pacing itself to a 40 fps target prints something near 40 fps almost regardless of whether extraction is cheap. This is B0's lesson in a second form: ask which number in a bench result had the freedom to come out differently.

---

The renderer and reference-profile decisions above were settled by the project owner on 2026-08-13. They are architecture intent, not performance evidence, so this record remains L0 until the physical renderer-to-Resolume bench runs.

These decisions are recorded as durable constraints in [ADR-026](../decisions/ADR-026-renderer-surface-model-and-reference-transport.md), which also fixes the rule that the reference profile is described as intended rather than supported until this record's bench runs.

### The 2026-08-16 transport soak is not renderer evidence, and this record stays L0

The [Track B spike](../bench/TRACK-B-NDI-SPIKE.md) sustained 1920x1080 at 40.00 fps with zero dropped frames for 6 h 49 min on a Debian 13 OptiPlex Micro 7050 ([RES-005](RES-005-ndi-vs-hdmi-transport.md), [RES-006](RES-006-linux-ndi-support.md)). It is tempting to read that as the reference profile validated. It is not, and the reason is the frame source: `videotestsrc` produces frames at almost no cost, while the renderer this record is about must extract virtual-matrix channels from a local FSEQ and paint a canvas for every one of those 982,100 frames.

So the spike establishes that **the transport can carry the reference profile**, and leaves open the question this record exists to answer, which is whether the node can *generate* it in the CPU budget the SpeedHQ encode leaves behind.

**That budget was measured on 2026-08-16 and the shape of it is the finding: 86% of one core.** The machine as a whole was nearly idle, but the cost is concentrated on a single thread, and that single thread is close to full.

The pipeline the spike ran was `videotestsrc ! video/x-raw,format=UYVY,... ! fpsdisplaysink video-sink=ndisink` with **no `queue` between source and sink**, so frame generation, SpeedHQ encode and network send all executed on one streaming thread. 86% of a core is what that combined thread cost at 1920x1080 UYVY at 40 fps, and since the source was a test pattern, very nearly all of it is encode and send.

**Three consequences for the renderer, and they are design inputs rather than observations.**

**The ceiling is per-core, not per-machine.** A box reporting itself 85% idle will still start dropping frames when that one thread passes 100%, and the spike sat at 86% of it. Anything that raises per-frame cost, more pixels or a higher frame rate, spends a budget with roughly 14% left rather than the whole machine's. This is the concrete form of this record's standing rule that pixel count is the parameter: the reference dimensions fit, and the headroom above them on one thread is thin.

**The renderer must not be added to that thread.** Naively extending the spike's pipeline shape by putting FSEQ extraction and canvas painting upstream of the sink puts them on the same streaming thread as the encode, which is already at 86%. A `queue` between the renderer and the NDI sink decouples them onto separate threads and is what makes the idle cores reachable. That is a Track B B2 and B3 concern and is recorded here because the measurement is what implies it.

**Produce UYVY natively if the extraction can.** [RES-006](RES-006-linux-ndi-support.md)'s desk research records that submitting UYVY avoids a conversion cost inside the send path, and the spike took that path by construction because `videotestsrc` emitted UYVY directly. A real renderer that paints RGB and then relies on `videoconvert` adds a colorspace conversion that this measurement does not include, on a thread budget that has 14% spare.

**What the load averages do and do not say.** They were reported as `0.58 0.30 0.38`. The one-minute figure sitting *above* the five and fifteen minute figures means load was still climbing when the sample was taken, so **these are not soak-steady-state numbers** and the 0.38 must not be read as a fifteen-minute average of the run. The 86% single-core figure is the one to carry.

The source was `videotestsrc` bars plus snow. No further characterisation of the encode against synthetic patterns is planned: B0 passed, and the figure that actually decides this record is the one B3 produces rendering real content through the same path.

Two smaller notes for whoever runs the renderer bench. The bench hardware was a 7050 where ADR-026 decision 5 names 7040-class, so the transport headroom figure is measured on newer silicon than the stated target. And **1920x1080 is a tested dimension rather than a ceiling**: the owner's day-0 intent is roughly one 1080p-equivalent surface per node across three nodes, portrait 1080x1920 in practice at the same pixel count, and this record's standing treatment of pixel count as a test parameter is unchanged. Hardware determines the dimensions a given show can run, and that number is produced by the renderer bench, not by this one.

## Decision, fallback, and revalidation

Implement v1 as `N=1` without embedding that limit into the architecture or data model. An unsupported layout receives an explicit reduced capability profile rather than a best-effort claim. Revalidate a profile after material renderer, driver, kernel, runtime, transport, or hardware changes.
