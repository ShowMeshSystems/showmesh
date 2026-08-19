# Track C seam C0a-1 bench: the device-independent GStreamer audio bench

Bench scaffolding for
[RES-007](../../docs/research/RES-007-audio-node-architecture.md), not part
of the ShowMesh product. Nothing here is imported by `internal/` or `pkg/`.
It answers RES-007's first bench item: can agent-supervised GStreamer
deliver click-free ducking, reliable gapless playback, multichannel
interleave, and LTC generated inside the pipeline and sample-aligned to
program, measured rather than assumed. See
`docs/private/seam-specs/TRACK-C-C0a1-BENCH-SPIKE.md` for the seam this
bench was built against.

## What this bench proves, and what it cannot

**Proves**: real GStreamer 1.26.2 element and pipeline behaviour on Debian
13, measured against captured files, for every question R1-R7 pose:
channel separation, program/LTC sample alignment across start, track
change, seek and restart, LTC generation and its timecode wrap, click-free
ducking versus an abrupt step, the true cost of an item transition, a real
ALSA sink path with no hardware, and runtime capability discovery. Every
number in `results/*.json` came from a captured file or a captured log
line, never from listening or from an element's own claim of success.

**Cannot prove, and does not claim to**: anything about a physical audio
interface. Every run here uses a userspace null ALSA PCM device
(`asound.conf`) or a synthetic file/appsrc source; there is no `/dev/snd`,
no kernel sound module, and no real driver in this container. A physical
interface's channel independence (the "mirror problem" RES-007 names), its
actual clock, its hot-plug behaviour, and its true achievable sample rates
are **per-device commissioning** questions (C0b), owned by the owner's own
hardware, and this bench is structurally incapable of answering them --
see RES-007 for why a driver reporting N channels does not mean N
independently-addressable outputs.

## Layout

```
bench/audio-node/
  Dockerfile              Debian 13 + GStreamer 1.26 + a compiled ltcgen
  asound.conf              Userspace null ALSA PCM device
  ltcgen.c                 R3's external LTC generator, built against libltc
  run_all.sh                Entry point: runs R1-R7, writes results/
  runs/                    One script (or two, ramped/abrupt) per run, plus
                            analyze.py, the shared stdlib-only WAV analyzer
  results/                 Committed: one JSON per run, plus raw logs.
                            Never a captured WAV -- see "Evidence output"
                            in the seam spec; the scripts regenerate them.
```

## Running it

```
make bench-audio
```

built and run entirely inside the image; `results/` is written back to the
host via a bind mount. Nothing here runs as part of `make check` or CI --
see the Makefile target's own comment, same convention as
`bench/fpp-multisync`.

## The seven runs, and what was actually found

**R1 -- channel separation.** `interleave` (not `audiomixmatrix`) combines
three independently-generated mono streams into one 3-channel stream, one
sink pad per channel. `interleave` was chosen because it assembles
discrete channels with no coefficient matrix in the path at all -- there is
no crosstalk to introduce by construction, which is what "graph-level
separation" should mean, whereas `audiomixmatrix` exists for weighted
summing and would leave open the question of whether a matrix coefficient
could ever leak between channels. Measured: exact zero RMS/peak on the
program pair with a live 1kHz tone on the LTC-designated channel. This is a
graph property; see the top of this file for why it says nothing about a
physical interface.

**R2 -- program/LTC alignment**, five separate cases, each its own
measurement:
- baseline (start): **1 sample** (~0.02ms) offset between a program impulse
  and the LTC channel's first non-silent sample, both derived from one
  `concat`-built pipeline.
- restart: three independent `gst-launch-1.0` process invocations of the
  identical pipeline produced the identical 1-sample offset every time --
  deterministic, not measured once and assumed.
- track change: a mid-timeline program item switch (via `concat`) landed
  its new item's onset 2 samples after the arithmetically expected sample,
  with the LTC branch continuous and untouched by the switch.
- seek: a real GStreamer flushing+accurate `SEEK` event (not a byte-offset
  read), issued deterministically once `get_state()` confirms PAUSED
  preroll has actually completed (see `runs/r2_seek.py`), landed
  **exactly on the requested sample, 0 samples of error, across three
  different targets** (0.5s, 1.0s, 2.0s) and reproduced identically across
  two independent full `make bench-audio` runs. Two things had to be found
  and fixed to get a trustworthy number here, both left documented in
  `runs/r2_seek.py` and `runs/r2_seek_source.sh` as worked examples of the
  project's own recurring lessons:
  - an earlier version issued the seek from an `ASYNC_DONE` bus-message
    handler instead of after the blocking `get_state()` call, which is a
    *different event*; on a fast storage backend (a Docker named volume
    reproduced this consistently, a host bind mount mostly hid it) this
    intermittently produced a 0-byte output file with no reported pipeline
    error at all -- the seek raced the pipeline's own teardown.
  - before that fix existed to expose it, the very first attempt at this
    measurement used a pure 1kHz tone as the seek source and reported a
    plausible-looking but meaningless offset, because a tone that is an
    exact integer number of samples per cycle repeats bit-for-bit every
    period, so a byte-match search cannot locate a unique landing point in
    periodic content. Switching the seek source to `wave=white-noise`
    (never repeats) fixed it.
- gain change: not measured as a separate R2 case; R4's ramp is the same
  GstController mechanism a live duck/gain change would use, and R4
  measures its behaviour directly.

**R3 -- LTC generation survey**, the one that could have come back
negative. No installed GStreamer plugin (base/good/bad, 1.26.2) generates
LTC audio: `timecodestamper`/`avwait` embed or consume SMPTE timecode as
*video buffer metadata*, never touching an audio sample. Debian 13 ships
`libltc11`/`libltc-dev` -- the real Manchester-biphase encoder library,
upstream x42/libltc -- but no prebuilt `ltcgen` binary and no `ltc-tools`
package. `ltcgen.c` is a ~50-line program built against that real library
(candidate c: "an external generator ... feeding the pipeline through
fdsrc/filesrc"), and its output is played back as an ordinary
`filesrc`/`rawaudioparse` source in the same pipeline as program (candidate
b). **The answer is b and c together**, not a native element (a). Alignment
through this candidate is everything in R2 above, since every R2 pipeline
plays a `ltcgen`-produced file. The timecode-wrap case (`23:59:58` crossing
`00:00:00`) showed no dropout or discontinuity: RMS and max sample-delta in
a 1000-sample window straddling the wrap matched a reference window
elsewhere in the same file almost exactly (22765 vs 22819 vs 22772 RMS).

**R4 -- click-free ducking.** A real `GstController.InterpolationControlSource`
bound to a `volume` element's `volume` property via
`DirectControlBinding.new_absolute` (not `new()`, which maps a 0..1 control
value onto the *property's full range* -- `volume`'s range is 0..10, so
`new()` turned a requested gain of 1.0 into an actual 10x boost and clipped
the signal into a square wave; found by inspecting the captured WAV, not
assumed) drove a 200ms linear fade from 1.0 to 0.2. Raw sample-to-sample
delta turned out not to distinguish a fade from a step at all -- a
periodic tone's own per-sample slope can exceed either transition's
contribution -- so the real metric is windowed-envelope discontinuity
(`envelope_max_step` in `analyze.py`, ~1 cycle windows). Ramped: **158**.
Abrupt step control (two `concat`-joined segments with static, different
`volume`): **20978**. About 133x.

**R5 -- item transition gap.** `concat` splices two items with exactly
zero inserted or dropped samples (verified: total length equals the exact
sum of the two 1-second items, 96000 samples). Ordinary sequential
playback -- two independent `gst-launch-1.0` processes, second started
only after the first exits -- has no sample-domain gap either in this
capture (the two raw files are just concatenated), which is **not**
evidence that real sequential playback is gapless: the real cost is
wall-clock process/pipeline-restart latency, measured directly at
**~9.4ms** for a second independent `gst-launch-1.0` process on this host,
and that cost never appears in the sample domain at all because neither
source pipeline is a live, wall-clock-paced source.

**R6 -- ALSA sink path with no hardware.** A real `alsasink` negotiated,
prerolled, played, and reached EOS against the userspace `null` PCM device
declared in `asound.conf`, with no `/dev/snd` and no kernel module in the
container. Exit code 0.

**R7 -- runtime capability discovery.** `aplay -L` reports exactly `null`
and `default` (aliased to `null`); `aplay -l` finds no hardware cards, as
expected. `gst-device-monitor-1.0`, the tool C1's capability advertisement
would most naturally call, **is not present in this Debian 13 package set**
(`gstreamer1.0-tools` ships `gst-inspect`/`gst-launch`/`gst-stats`/
`gst-tester`/`gst-typefind` only, confirmed via `dpkg -L`) -- C1 cannot
assume it exists without adding a further package, and that is now a known
fact rather than an assumption for whoever builds C1. Free negotiation
against the null sink settled on 44100Hz/F64LE/stereo, which is a fact
about this container's virtual stack and, per this file's opening section,
says nothing about a physical interface's negotiated rate.

## Open questions only the owner or C0b can settle

- Whether `gst-device-monitor-1.0` should be added to the shipped audio
  node image, or whether C1's capability advertisement should be built
  against `aplay -L`/ALSA APIs directly instead.
- The exact-zero seek error and the 1-2 sample start/restart/track-change
  offsets are all comfortably sub-millisecond (most are zero or one sample)
  on this container image; C0b needs to confirm whether a real audio
  interface's own driver latency changes that picture, since none of it
  was measured against real hardware.
- Whether `new_absolute()` (this bench's fix) is the binding convention the
  eventual C3 engine should standardize on everywhere a controller drives
  a `volume` (or similar 0..N range) property, to avoid re-discovering the
  same clipping defect.
