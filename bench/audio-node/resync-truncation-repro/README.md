# Does a non-live pipeline's resync truncate cue content?

Not part of the ShowMesh product; nothing here is imported by `internal/`
or `pkg/`. This is the artifact set for one measurement against real
hardware, following this repo's own evidence rule: a compiling package and
a passing unit test are not evidence of user-visible behavior. See
[`internal/agent/audio/gstengine/truncationrepro_manual_test.go`](../../../internal/agent/audio/gstengine/truncationrepro_manual_test.go)
for the actual pipeline under test.

## The prediction under test

`Engine.Start`'s seek callback calls `resyncMixerPads`
([`branch.go`](../../../internal/agent/audio/gstengine/branch.go)), which
sets every channel-mixer sink pad's offset to
`pipelineRunningTime() - localRunningTime(atPos)`. Another lane measured
where that offset lands relative to the mixer's own output:

| pipeline shape | anchor lands | median |
|---|---|---|
| live | 18ms in the mixer's future | -18.2ms |
| non-live | 395ms in the mixer's past | +394.6ms |

(constant to within 0.5ms across 100 runs per variant, sd 0.05ms.)

`GstAudioAggregator` (the base class behind `audiomixer` and `interleave`)
discards buffers that arrive entirely before its own output offset. One
inferential step from that documented behavior: in a non-live pipeline, a
cue should not start 395ms *late* -- it should start about 395ms *into*
the file, with the first third of a second of content silently missing.
This repro renders real audio through both pipeline shapes and measures
what actually comes out, rather than stopping at the anchor measurement.

**Do not extend this to a fix before Eric authorizes it from the reported
number.** The candidate fix is one line in `resyncMixerPads`; it is not in
this branch.

## Why this cannot be a container bench

`bench/audio-node`'s existing spike ([`../README.md`](../README.md))
deliberately proves everything against a userspace null ALSA device with
no physical interface, because most of what it measures is graph-level
(channel separation, LTC alignment, controller behavior) and explicitly
says so up front. This measurement is different: **the sound card's own
hardware clock is the entire variable under test.** `snd-aloop` has a
software clock and would silently produce a different, meaningless
result; a null/virtual PCM device is worse. This repro can only mean
anything against a real card, which is why it is a manual, hardware-gated
Go test (`//go:build cgo && showmesh_truncation_repro`), matching this
package's existing `hwdevice_manual_test.go` convention, not a `make
bench-audio` container run.

## Environment verified in the build worktree (dev-02), 2026-09-08

Checked directly rather than assumed, per the task's own instruction:

| claim | checked value |
|---|---|
| `uname -m` | `x86_64` |
| `pkg-config --modversion gstreamer-1.0` | `1.26.2` |
| `ldd --version` | Debian GLIBC 2.41-12+deb13u3 |
| `go version` | go1.26.0 linux/amd64 |
| local `main` | `5469275a` |
| `origin/main` (after `git fetch`) | `5469275a` |
| this worktree's `HEAD` | `5469275a` |

**Correction to the task brief:** local `main` was NOT three commits
behind `origin/main` at the time of this run -- `main`, `origin/main`,
and this branch's own `HEAD` were all already at `5469275a`, the same
commit the brief names as `origin/main`'s tip. No rebase or update was
needed; this is a real discrepancy against the brief, not a stale-base
risk, and is flagged per this repo's rule to say so rather than silently
proceed on the brief's number.

This build machine (`dev-02` / this worktree) has no real sound hardware
-- `aplay` is not even installed, and `/proc/asound/cards` shows only a
software `Dummy` card -- consistent with prior memory that this class of
build VM carries no show hardware. **Nothing in this repro was run
end-to-end here.** The actual live/non-live/truncation measurement can
only happen on node-01.

**What "verified on dev-02" actually means, precisely, after one real bug
was found in that verification itself:** the first version handed off
here only ran `runTruncationArm` up to its own environment-variable skip
check, which sits above every line that does real work -- so "builds and
runs to a clean skip" certified nothing past that check, and a real
ordering bug downstream of it went uncaught. The bug: `buildTruncation
CaptureSink` calls `gst.ParseBinFromDescription` before `New` (which is
what calls `gst.Init`) ever runs, so GStreamer's element registry does
not exist yet and `tee` fails to resolve -- exactly the same hazard this
package's own `enginefdleak_test.go`, `ltclag_real_integration_test.go`,
and `sinkformat_real_integration_test.go` each already guard against with
an explicit `gst.Init()` call and comment, one this file simply didn't
follow. Fixed by adding the same call, in the same place, with the same
comment. Proof it's fixed, run on dev-02 with no real card needed: both
arms, run with `SHOWMESH_TRUNC_DEVICE=plughw:CARD=NOSUCHCARD` (env vars
now genuinely set, so every line executes, including the one that used to
fail before reaching this point) now build the tee/alsasink/wavenc/
filesink bin successfully (confirmed via `GST_DEBUG=alsa:5,GST_ELEMENT_
FACTORY:5`: `creating element "tee"` / `"alsasink"` / `"wavenc"` /
`"filesink"` each followed by `created element ...`) and fail only once
they reach the real ALSA open call: `alsalib error: Cannot get card index
for NOSUCHCARD` / `gst_alsasink_open: Error -19 (No such device) calling
snd_pcm_open` / `Playback open error on device 'plughw:CARD=NOSUCHCARD':
No such device`. That is a genuine device-open failure, not a registry
failure -- confirming the initialization-order bug is gone without
needing a real card to prove it.

## Node-01 run 1: the `gst.Init` fix confirmed, three analyzer bugs found

The `gst.Init` ordering fix above was confirmed on node-01 itself: the
live arm ran to completion, exit 0, genuinely opened card 1, left card 0
(the live show, owned by another agent) `RUNNING` and untouched, and
wrote a real capture file. `analyze` then refused that capture --
`CAPTURE_INVALID`, silent in the first 50ms -- and was *right* to refuse
it, just not for the reason it thought. Three real bugs in `analyze`
followed from looking at why, all fixed in this revision, none of them
touching `gensweep`, the arms, or the test harness:

1. **Format.** The real capture is IEEE float (WAVE format tag 3), 32-bit,
   mono, 48000Hz -- `analyze` only understood PCM integer. It now decodes
   PCM 16/32-bit and IEEE float 32/64-bit, and refuses by name (citing the
   format tag and bit depth) rather than silently misreading an
   unsupported combination as if it were a different one.
2. **The header lies.** `wavenc` wrote a data-chunk size of 2147418112
   (`0x7FFFF000`) -- its own placeholder for "filesink could not seek back
   to patch the real size at EOS," not the real length. The real file was
   1023404 bytes; trusting the header computes over 11184 seconds of
   audio from a 1MB file. `analyze` already clamped to the file's actual
   size, but silently -- it now also prints a `NOTE:` naming the
   discrepancy and, when the claimed size is exactly `wavenc`'s
   placeholder, says so explicitly, rather than clamping without comment.
3. **Onset and truncation are different quantities, and conflating them
   is the real one.** The matched filter assumed the query begins at the
   sweep. A real capture does not: the capture branch starts recording
   when the pipeline is built, and the branch's own content only reaches
   it once `Start` runs, some nonzero wall-clock time later -- pure
   pipeline-startup latency, unrelated to the aggregator-drop question
   this repro exists to answer. `analyze` now finds the signal's onset
   inside the capture first, then asks only from there how much of the
   sweep's own front is missing, and reports the two as separate,
   distinctly named fields (`RESULT_ONSET_MS`, `RESULT_TRUNCATION_MS`) so
   they cannot be added together into a wrong answer that still looks
   plausible.

**Why every control here missed this before a real capture existed:**
every known-bad control up to that point was built by trimming the
*front* of the sweep -- none had leading silence, so the onset-vs-sweep-
start assumption the matched filter made was never exercised. A synthetic
control cannot retire a real one when it was built to look like the wrong
thing. Control 4 below closes that gap: a synthetic file shaped like a
real capture (leading silence, then a cut sweep), not like the sweep
itself.

## Node-01 run 2: the live arm measured, the non-live arm's own control caught a second construction bug

### Live arm: measured

Re-run with the fixed (commit `828410aa`) analyzer against the real live
capture:

```
RESULT_ONSET_MS: 330.438
RESULT_TRUNCATION_MS: 0.021
```

(0.021ms is 1 sample at 48000Hz, correlation 1.0000 -- effectively exact
zero, the same floating-point-of-a-sine-starting-at-zero artifact the
sanity check above documents.) This agrees with an independent hand
measurement of the same capture -- first sample above 0.005 at 330.44ms,
last above threshold at 2330.40ms, a span of exactly 2.000s, peak
0.799988 against the sweep's own configured amplitude 0.8 -- made by a
completely different method (amplitude threshold against matched filter).
Two independent measurements agreeing is what makes this evidence rather
than a plausible-looking number: **the live pipeline renders the whole
cue, with nothing missing from its front.** This matches the prediction
(anchor ~18ms in the mixer's *future* for a live pipeline implies no
truncation) and closes control 1.

The header-size note also fired correctly on this real file: declared
2147418112 bytes (`0x7FFFF000`) against 1023360 bytes actually present,
identified by name as `wavenc`'s own unpatchable-header placeholder.

### Non-live arm: voided by its own control, correctly

The non-live arm (the post-hoc `forceNonLive` property-flip version, at
the time) ran, wrote a capture, and then its own built-in control
refused to let that capture be treated as data:

```
CONTROL: pipeline.IsLive() = true (requested forceNonLive=true)
CONTROL FAILURE: pipeline.IsLive()=true while forceNonLive=true -- the
toggle did not produce the intended pipeline classification. STOP: any
truncation reading from this run is void, not a result.
```

That capture was never analyzed. A file existing is not a result
existing, and a void arm is not data -- this is the harness behaving
exactly as designed, and is the reason the live-arm number above can be
trusted when it agrees independently.

**Diagnosis, confirmed:** GStreamer decides a pipeline's liveness once,
during its own READY->PAUSED preroll (the LATENCY query every element
answers at that point). `gst_bin_recalculate_latency`, called afterward
on an already-PLAYING pipeline, reconfigures timing within that existing
classification -- it does not reopen the classification itself. No
settle-time delay could have fixed this; it isn't a timing race. This
was also independently confirmed on dev-02 with `fakesink` (no card
needed, since liveness is a source-side property): a pipeline built with
`is-live=false` on its only source reads `pipeline.IsLive() == false`
immediately, while the identical graph with a post-hoc flip on an
already-PLAYING pipeline stayed `true` -- see
`TestBuildTestPipelineAchievesConstructionTimeLiveness` in
`truncationreprobuild_test.go`.

**A second, separate finding, worth as much as the measurement:** the
non-live arm is not reachable through production configuration *at all*
today. All three `is-live` sites in this package
(`addMixerKeepAlive`, the silence-channel chain, and the LTC appsrc
chain, all in files under
[`internal/agent/audio/gstengine`](../../../internal/agent/audio/gstengine))
are bare literal `true`, `addMixerKeepAlive` runs unconditionally once
per program channel, and `Config.Validate` rejects an empty
`ProgramChannels` -- so every structurally valid `Config` yields at least
one live source, and production can only ever build a live pipeline.
That is also why the live arm's measurement above is production's own
real, unmodified behavior, not a constructed comparison case.

**The fix, test-local only:** `truncationrepro_manual_test.go`'s
`buildTestPipeline` now constructs the non-live arm's pipeline itself,
reusing production's own private `linkInterleaveToSink` and
`probeSinkChannelPositions` helpers verbatim for everything except the
keep-alive source, whose `is-live` it sets *before* the pipeline's first
state change rather than after. No production file gained a config
field, a flag, or an environment variable; `Engine.buildPipeline` is
untouched and still hardcodes `is-live=true` exactly as shipped -- this
builder is a wholly separate, test-local construction path that exists
only so `pipeline.IsLive() == false` becomes reachable to measure at all.
See that file's own top doc comment for the full design rationale.

**Why this matters beyond this measurement:** the owner has ruled that
the pipeline must be clocked before playback starts, which requires
building it non-live from construction -- so the non-live pipeline this
repro measures is not a hypothetical rejected direction, it is the state
a future fix would put production in. Measuring whether it truncates the
front of a cue is measuring the consequence of that future state before
it ships, not an academic comparison.

## Node-01 run 3: a confound in the instrument itself, and how it's ruled in or out

### The result that prompted this

Three runs each arm, all six controls passing, live always
`pipeline.IsLive()==true` with clock `GstSystemClock`, non-live always
`false` with clock `GstAudioSinkClock`:

| arm | run 1 | run 2 | run 3 | mean |
|---|---|---|---|---|
| live truncation (ms) | 0.021 | 7.208 | 0.021 | -- |
| non-live truncation (ms) | 1000.458 | 999.292 | 1005.000 | 1001.6 (range 5.7) |

The non-live pipeline drops about a second off the front of every cue --
roughly 2.5x the originally measured anchor's own 395ms, and suspiciously
close to a round number.

### The confound

`buildTruncationCaptureSink`'s capture tee originally always inserted
`tee name=t ! queue ! alsasink ...` -- a bare `queue`, whose GStreamer
default `max-size-time` is exactly 1000000000ns, one second. **Production
has no tee and no queue between the format-adaptation chain and the
sink at all** -- `linkInterleaveToSink` (`engine_cgo.go`) links straight
into whatever `newSinkFactoryElement` builds. A buffering element
production does not have was sitting directly upstream of the sink, in
exactly the path whose buffering behavior this repro measures.

**The hypothesis, which predicts both arms, not only the surprising
one:** a non-live source pushes as fast as it can. The sink-branch queue
absorbs up to its own max-size-time of data before backpressure reaches
the mixer, so the mixer's own output running time can run that far ahead
of what the sink has actually rendered. `Start`'s seek then anchors the
branch to that advanced running time, its buffers arrive behind it, and
`GstAudioAggregator` discards whatever lies entirely before its output
offset -- bounded by the queue's own depth. In the live arm the source
paces to real time, the mixer cannot run ahead regardless of any
downstream queue, nothing is discarded, and the measurement reads zero
either way. One mechanism explains both results; an explanation that
only accounts for the surprising one is a guess, not a candidate.

**This is the analyzer's own onset mistake one layer out:** the
instrument was built by the same hands as the experiment, and it
introduced an element production does not have into exactly the path
under test. The tee genuinely cannot affect clock selection (queried and
confirmed via `GST_DEBUG=GST_PIPELINE:5`, section 4) -- that was true,
and the wrong property to have checked. It can affect buffering.

### Two decisive experiments, not one

**Experiment A -- does truncation track queue depth?** Vary only the
sink-branch queue's `max-size-time` via `SHOWMESH_TRUNC_SINK_QUEUE_MS`
(new env var; unset defaults to 1000, reproducing the run above
unchanged) across 100 / 500 / 1000ms, three runs each, nine numbers
total. The capture-branch queue (`captureq`) is never touched by this
variable and keeps GStreamer's own unset defaults throughout -- a tee
blocks every branch when any one blocks, so removing *that* queue's
buffering would let a `filesink`/`wavenc` stall propagate back into the
sink branch, producing a third, different pipeline rather than an answer.
`buildTruncationCaptureSink` also now sets the sink queue's
`max-size-bytes` and `max-size-buffers` explicitly to 0 (unlimited)
whenever a queue is present, so `max-size-time` is the only bound in
effect -- otherwise GStreamer's own default `max-size-buffers=200` could
cap effective depth below whatever the time setting alone implies,
confounding a test whose entire point is varying time. This means even
the 1000ms sweep point is not byte-for-bit identical in configuration to
run 3's own original queue (which had all three GStreamer defaults
active); if its result still lands near 1001.6ms, that itself shows the
byte/buffer bounds were not the dominant factor either.

**Experiment B -- does truncation collapse when the sink-branch queue is
removed entirely?** `SHOWMESH_TRUNC_SINK_QUEUE_MS=0` builds the sink
branch as `tee name=t ! alsasink device=...` directly -- no queue at
all, matching production's own topology exactly at that point. The
capture branch keeps its own queue unchanged, for the same
tee-blocks-every-branch reason as experiment A. Three runs.

Both experiments must be run and reported together, not separately --
one corrected figure, not a third revision.

**Verifying the shape actually built, not the string that was written:**
`buildTruncationCaptureSink` names every element in its own description
(`t`, `sinkq` when present, `captureq`) precisely so
`verifyAssembledSinkShape` can enumerate what GStreamer actually
constructed (`gst_bin_iterate_elements`, not a re-parse of the
description) and log it before any run proceeds, plus read `sinkq`'s
`max-size-time`/`-bytes`/`-buffers` back off the live element rather than
trust the value this test asked for. This is what catches a queue that
survived an edit, or one nobody intended, sitting unnoticed in exactly
the path under measurement -- writing a fresh description string is a
fresh chance to make the same mistake again, so the check has to inspect
the assembled object, not the text. (Building this needed one library
workaround: go-gst v0.0.2's generated `Iterator.ForEach`/`Fold` panic
unconditionally -- an upstream stub explicitly marked "must be
handwritten" -- so the walk uses the separately hand-written
`Iterator.Next` instead, which does not have this problem.) Confirmed on
dev-02 against a bad device (no card needed for this structural check):

```
ASSEMBLED SHAPE [...]: [filesink0(filesink) wavenc0(wavenc) captureq(queue) alsasink0(alsasink) sinkq(queue) t(tee)]
VERIFY [...]: sinkq read back from the live element -- max-size-time=500000000 max-size-bytes=0 max-size-buffers=0
```

for a 500ms setting, and with `sinkq` entirely absent from the list (not
merely zeroed) when `SHOWMESH_TRUNC_SINK_QUEUE_MS=0`.

### How to read the combined result

- **If Experiment A's truncation tracks the queue setting** (roughly
  proportional to 100 / 500 / 1000ms), the constant is this repro's own
  instrument, not evidence about production, and the number reported so
  far is an artefact of the rig -- a good outcome for one round of work,
  reported plainly rather than softened.
- **If Experiment A stays near 1000ms regardless of setting**, the
  sink-branch queue's `max-size-time` is exonerated as the mechanism.
- **If Experiment B (queue removed) collapses toward roughly 200ms** --
  `alsasink`'s own default `buffer-time` -- the hypothesis is confirmed
  in a different, still real form: the bound is the sink's own ring
  buffer, not a GStreamer queue, and that number is closer to what
  production would actually lose.
- **If Experiment B stays near a second even with no queue at all**, the
  sink-branch queue was not the cause, something else is holding that
  much data back, and that is itself worth reporting rather than
  papering over.

The one thing this section does not do is state a corrected truncation
number: that comes only from the nine-plus-three measurements above,
run and reported together.

## What's in this directory

- `gensweep/` -- the deterministic sweep generator (below).
- `analyze/` -- the self-contained truncation analyzer (below).
- `gencapture/` -- builds control 4's capture-shaped fixture (below);
  exists only to build that control, never a stand-in for a real capture.
- This README's own recorded control outputs, captured on dev-02.

The actual test binary lives in the `gstengine` package itself
(`truncationrepro_manual_test.go`), not here, because it needs
package-internal access to `resyncMixerPads`'s own state
(`branchFor`, `channelMixerPads`, `pipelineRunningTime`) to log
diagnostics and to `newSinkFactoryElement`, the package's existing
test-injection seam, to substitute the tee/capture sink. See that file's
own doc comment for the full design rationale, including exactly how the
non-live arm is constructed without touching any committed file's
`is-live` literal.

## 1. The signal: a linear sweep, not a tone or a burst ladder

100Hz to 2kHz, linear, over the first two seconds, starting at sample 0,
generated as a pure analytic function of sample index -- no
`audiotestsrc`, no dependency on wall-clock time, byte-identical on every
regeneration with the same flags. This is deliberate, not merely
convenient: a ladder of tone bursts only bounds the answer to the burst
spacing; a single steady tone can't distinguish a truncated start from an
intact one at all; and silence measures nothing at all --
`audiotestsrc wave=silence` emits GAP buffers a sink can drop without
that showing up as a rendered gap, which cost an earlier arm on this
hardware 35 minutes of measuring nothing. A sweep has energy at every
instant, so none of those failure modes can recur.

Exact command that produced the reference sweep used for every control
below and the two real-hardware arms:

```sh
go run ./bench/audio-node/resync-truncation-repro/gensweep \
    -out sweep_100_2000_2s_48k_s16le_mono.wav \
    -f0 100 -f1 2000 -dur 2.0 -rate 48000 -bits 16 -channels 1 -amp 0.8
```

Mono, 48000Hz, 16-bit: matches the test topology's single program channel
(`ProgramChannels: []int{1}, ChannelCount: 1`) and its `SampleRate`.
Regenerating with the same flags reproduces the file byte-for-byte (the
signal is `sin(2*pi*(F0*t + k*t^2/2))`, `k=(F1-F0)/dur`, no RNG, no clock
read).

## 2. The analyzer

`analyze` answers two separate questions about a captured file, in order:

**Stage 1 -- onset.** Where inside the capture does signal actually
start? Found by scanning for the first sample whose amplitude exceeds a
threshold (default 0.005) *and* stays elevated for a following sustain
window (default 5ms), so a single click or numeric noise sample can't be
mistaken for the real onset. Reported as `RESULT_ONSET_MS`. This is
capture/pipeline-startup timing -- how long after the capture file starts
recording the branch's content actually arrives -- and has nothing to do
with the aggregator-drop question this repro exists to answer.

**Stage 2 -- truncation.** Starting from that onset, how much of the
reference sweep's own front is missing from the content that begins
there, measured by two independent methods that must agree:

1. **Matched filter (primary, exact)** -- cross-correlates a window
   starting at the onset against every candidate offset into the
   reference and reports the offset that maximizes normalized
   correlation, refined to sub-sample precision by parabolic
   interpolation around the peak. Exact for a synthetic, noise-free copy
   (see the controls below); on a real captured file, the correlation
   score itself (printed) is the confidence signal.
2. **Instantaneous frequency (cross-check)** -- estimates the frequency
   actually present just after onset via zero-crossing period counting
   (window grown until enough crossings are captured), then inverts the
   sweep's own linear law to recover elapsed time. This is the literal
   "frequency of the first rendered sample" framing the task described;
   it's reported as a cross-check because a chirp's changing frequency
   within any nonzero window biases this estimate toward the window's
   mean rather than its exact first instant (see the controls below --
   it disagrees with the exact matched-filter number by a few ms, which
   is expected and documented in the tool's own comment).

Reported as `RESULT_TRUNCATION_MS`. **These two numbers are never added
together or otherwise combined** -- see the node-01 section above for
exactly what conflating them produces.

Sample formats: PCM 16/32-bit integer and IEEE float 32/64-bit are
decoded; an unrecognized format tag or bit depth is refused by name, not
silently misread. The data chunk's declared size is cross-checked against
the file's actual size -- see the node-01 section above.

It refuses to report a result for a capture it could not actually
analyze: a missing/unreadable file, an empty data chunk, no sustained
signal found anywhere (silent throughout), or too little content
surviving after onset to correlate all print `CAPTURE_INVALID` to stderr
and exit 1 -- never a 0ms reading standing in for "did not run." This is
what makes control 3 below possible.

## 3. Controls, run on dev-02

All four ran against `sweep_100_2000_2s_48k_s16le_mono.wav` from step 1.

### Control 1 -- comparison arm (live pipeline)

Not runnable on dev-02 (no card 1). This is the control the two
real-hardware commands in section 4 exist to satisfy: the live arm's
`pipeline.IsLive()` must read `true` and its capture must show no
truncation. **If both arms truncate, or neither does, the instrument is
wrong and the result is void** -- the test binary itself enforces the
`IsLive()` half of this as a hard `t.Fatalf`, so a broken toggle fails
loudly rather than producing a misleading number.

### Control 2 -- known-bad, no leading silence (expect truncation ~395.000ms, onset ~0)

A copy of the reference sweep with exactly 395ms (18960 samples at
48000Hz) trimmed from its start -- content begins immediately, so onset
should read ~0:

```
$ go run ./bench/audio-node/resync-truncation-repro/analyze \
    -ref sweep_100_2000_2s_48k_s16le_mono.wav \
    -query known_bad_trimmed_395ms.wav -f0 100 -f1 2000 -dur 2.0
ONSET_MS: 0.000 (signal within the capture begins this far in; this is capture/pipeline-startup timing, NOT sweep truncation)
MATCHED_FILTER: truncation = 395.000 ms (18960.000 samples at 48000 Hz, correlation score 1.0000)
FREQ_CROSSCHECK: first-sample-after-onset instantaneous frequency ~477.5 Hz -> t=397.39ms into the sweep
AGREEMENT: methods differ by 2.387 ms
RESULT_ONSET_MS: 0.000
RESULT_TRUNCATION_MS: 395.000
```

Exit code 0. Matched filter is exact; the frequency cross-check's small,
expected, and documented bias (~2.4ms here) does not change the primary
result.

### Control 3 -- deliberately broken capture (expect CAPTURE_INVALID, exit 1)

A WAV with a valid header but a zero-length data chunk, standing in for
"the capture branch produced nothing":

```
$ go run ./bench/audio-node/resync-truncation-repro/analyze \
    -ref sweep_100_2000_2s_48k_s16le_mono.wav \
    -query empty_broken_capture.wav -f0 100 -f1 2000 -dur 2.0
CAPTURE_INVALID: query file problem: empty_broken_capture.wav: data chunk is empty (0 bytes of audio)
exit status 1
```

Exit code 1, not a false "0ms". A fully silent (but full-length,
non-empty) capture -- the `audiotestsrc wave=silence`-shaped failure this
task specifically warned about -- was also tried and produces the same
class of refusal (`CAPTURE_INVALID: no sustained signal found ...`),
never a 0ms result.

### Control 4 -- capture-shaped: leading silence + a known cut, in IEEE float (expect onset ~300.000ms, truncation ~210.000ms, reported separately)

The control that closes the gap every earlier control missed: built to
look like a real capture (startup silence, then truncated content), in
the same IEEE-float format a real capture uses, with a cut magnitude
neither this repro nor node-01's own run had used before (395ms and
137.5ms respectively; this one is 210ms). Built with `gencapture`:

```sh
$ go run ./bench/audio-node/resync-truncation-repro/gencapture \
    -ref sweep_100_2000_2s_48k_s16le_mono.wav \
    -out capture_shaped_control_300silence_210cut_f32.wav \
    -silence-ms 300 -cut-ms 210 -format f32
```

```
$ go run ./bench/audio-node/resync-truncation-repro/analyze \
    -ref sweep_100_2000_2s_48k_s16le_mono.wav \
    -query capture_shaped_control_300silence_210cut_f32.wav -f0 100 -f1 2000 -dur 2.0
ONSET_MS: 300.000 (signal within the capture begins this far in; this is capture/pipeline-startup timing, NOT sweep truncation)
MATCHED_FILTER: truncation = 210.000 ms (10080.000 samples at 48000 Hz, correlation score 1.0000)
FREQ_CROSSCHECK: first-sample-after-onset instantaneous frequency ~302.8 Hz -> t=213.48ms into the sweep
AGREEMENT: methods differ by 3.478 ms
RESULT_ONSET_MS: 300.000
RESULT_TRUNCATION_MS: 210.000
```

Exit code 0. Both numbers exact and correctly separated -- had onset and
truncation been conflated (300 + 210 = 510, or the matched filter run
from sample 0 instead of from onset), this control would have caught it,
which is exactly why it exists.

A second run patched this same file's data-chunk size to `wavenc`'s exact
0x7FFFF000 placeholder (byte-identical audio, only the header lies) and
confirmed the `NOTE:` fires while `RESULT_ONSET_MS`/`RESULT_TRUNCATION_MS`
are unchanged -- the header-size cross-check does not depend on the
header being right.

### Sanity check (not one of the four required controls, run anyway)

Reference against itself:

```
$ go run ./bench/audio-node/resync-truncation-repro/analyze \
    -ref sweep_100_2000_2s_48k_s16le_mono.wav \
    -query sweep_100_2000_2s_48k_s16le_mono.wav -f0 100 -f1 2000 -dur 2.0
ONSET_MS: 0.021 (signal within the capture begins this far in; this is capture/pipeline-startup timing, NOT sweep truncation)
MATCHED_FILTER: truncation = 0.021 ms (1.000 samples at 48000 Hz, correlation score 1.0000)
RESULT_ONSET_MS: 0.021
RESULT_TRUNCATION_MS: 0.021
```

The 0.021ms (1 sample) reading, not an exact 0, is the sweep's own first
sample being `sin(0)=0` -- below the onset amplitude threshold by
construction, so onset lands on sample 1. Not a defect; noted so it isn't
mistaken for one.

## 4. Building and running on node-01

node-01 has no Go toolchain; the test binary is built here and copied
over. **This repo's binding (`go-gst`) links against the system
GStreamer/glibc rather than embedding them**, so the target machine's
shared libraries must match this build machine's, which the task brief
asserted and this run independently confirmed above (x86_64, GStreamer
1.26.2, glibc 2.41 -- all checked, not assumed). `ldd` against the built
binary on dev-02 shows dynamic links to `libgstreamer-1.0`,
`libgstaudio-1.0`, `libgstcontroller-1.0`, `libgstapp-1.0`,
`libgsttag-1.0`, `liborc-0.4`, `libglib-2.0`/`libgobject-2.0`, `libc.so.6`,
and a handful of their own transitive deps -- confirm node-01 carries a
GStreamer 1.26.2 install with the same plugin set (`gst-inspect-1.0
alsasink interleave wavenc audiotestsrc` should each print factory
details, not an error) before running.

Build (run on dev-02 / this worktree):

```sh
CGO_ENABLED=1 go test -c -tags showmesh_truncation_repro \
    -o truncation_repro.test ./internal/agent/audio/gstengine
```

Copy `truncation_repro.test` and `sweep_100_2000_2s_48k_s16le_mono.wav` to
node-01. **Card 1 is two-channel, 44100 or 48000, S16 or S32 -- since this
repro's topology is single-channel (mono), use `plughw:CARD=PCH`, never a
raw `hw:` route: `hw:` demands an exact format/channel-count match, while
`plughw:` (ALSA's own software conversion layer, entirely inside
`libasound`, beneath `alsasink`, adding no pipeline element and therefore
unable to affect which clock the pipeline selects) accepts the mono
stream and converts it.** Replace `PCH` if node-01's card 1 has a
different ALSA card name (confirm with `aplay -l` there first).

**Live arm** (comparison/control 1):

```sh
SHOWMESH_TRUNC_DEVICE=plughw:CARD=PCH \
SHOWMESH_TRUNC_SWEEP=/path/to/sweep_100_2000_2s_48k_s16le_mono.wav \
SHOWMESH_TRUNC_CAPTURE_LIVE=/path/to/live_capture.wav \
    ./truncation_repro.test -test.run TestTruncationRepro_LiveArm -test.v
```

**Non-live arm** (experimental):

```sh
SHOWMESH_TRUNC_DEVICE=plughw:CARD=PCH \
SHOWMESH_TRUNC_SWEEP=/path/to/sweep_100_2000_2s_48k_s16le_mono.wav \
SHOWMESH_TRUNC_CAPTURE_NONLIVE=/path/to/nonlive_capture.wav \
    ./truncation_repro.test -test.run TestTruncationRepro_NonLiveArm -test.v
```

Each run logs (to `-test.v` output): `pipeline.IsLive()` (must read
`true` for the live arm, `false` for the non-live arm -- a mismatch is a
hard test failure, not a soft warning, exactly because a silently-failed
toggle would make the two arms measure the same thing), the pipeline's
selected clock name, the assembled sink-branch shape and (when present)
`sinkq`'s properties read back from the live element (see run 3 above),
and the diagnostic `pipelineRunningTime`/pad-offset pair `resyncMixerPads`
actually computed for that run.

**Confirming the clock provider independently of the Go log line**: the
tee this repro inserts sits *after* interleave and *before* the real
`alsasink`, so it cannot itself change which element the pipeline selects
its clock from -- but per the task's own instruction, confirm this by
reading the selected clock rather than assuming it, not by predicting
what it should be. Re-run either arm once with
`GST_DEBUG=GST_PIPELINE:5 ./truncation_repro.test ...` and grep the
output for `"selected clock"` (`gst_pipeline_auto_clock`'s own debug
line). Observed on node-01: live selected `GstSystemClock`, non-live
selected `GstAudioSinkClock` -- the reverse of a naive expectation that
the real device's own hardware clock would win in the live arm. Both
arms' own `pipeline.GetPipelineClock() != nil` control passed in both
cases (a pipeline with no selected clock at all is the actual control
failure this guards, not a specific clock name), so neither run was
voided by it, but this pairing is not yet explained and is recorded here
rather than silently accepted -- a candidate confound in its own right,
separate from the queue investigation above, that a future round should
chase rather than this one.

### Node-01 run 3's experiments: exact commands

Nine sweep runs (Experiment A: `SHOWMESH_TRUNC_SINK_QUEUE_MS` in
`100 500 1000`, three repeats each) plus three queue-deleted runs
(Experiment B: `SHOWMESH_TRUNC_SINK_QUEUE_MS=0`), all against the
**non-live** arm only, each to its own capture file so none overwrite
each other:

```sh
for ms in 100 500 1000; do
  for run in 1 2 3; do
    SHOWMESH_TRUNC_DEVICE=plughw:CARD=PCH \
    SHOWMESH_TRUNC_SWEEP=/path/to/sweep_100_2000_2s_48k_s16le_mono.wav \
    SHOWMESH_TRUNC_CAPTURE_NONLIVE=/path/to/nonlive_q${ms}_run${run}.wav \
    SHOWMESH_TRUNC_SINK_QUEUE_MS=${ms} \
        ./truncation_repro.test -test.run TestTruncationRepro_NonLiveArm -test.v \
        | tee /path/to/log_q${ms}_run${run}.txt
  done
done

for run in 1 2 3; do
  SHOWMESH_TRUNC_DEVICE=plughw:CARD=PCH \
  SHOWMESH_TRUNC_SWEEP=/path/to/sweep_100_2000_2s_48k_s16le_mono.wav \
  SHOWMESH_TRUNC_CAPTURE_NONLIVE=/path/to/nonlive_qdel_run${run}.wav \
  SHOWMESH_TRUNC_SINK_QUEUE_MS=0 \
      ./truncation_repro.test -test.run TestTruncationRepro_NonLiveArm -test.v \
      | tee /path/to/log_qdel_run${run}.txt
done
```

Each saved log's `ASSEMBLED SHAPE`/`VERIFY` lines are part of the
evidence, not just its `RESULT_TRUNCATION_MS` -- confirm `sinkq` is
present with the intended `max-size-time` (experiment A) or absent
entirely (experiment B) for every one of the twelve runs before trusting
its number.

## 5. Analyzing a completed capture

```sh
go run ./bench/audio-node/resync-truncation-repro/analyze \
    -ref sweep_100_2000_2s_48k_s16le_mono.wav \
    -query live_capture.wav -f0 100 -f1 2000 -dur 2.0

go run ./bench/audio-node/resync-truncation-repro/analyze \
    -ref sweep_100_2000_2s_48k_s16le_mono.wav \
    -query nonlive_capture.wav -f0 100 -f1 2000 -dur 2.0
```

Report `RESULT_ONSET_MS` and `RESULT_TRUNCATION_MS` **separately** for
each arm -- never combined -- plus the correlation score, whether the
frequency cross-check agrees within a few ms, any `NOTE:` about the
header's declared size, and each arm's logged `pipeline.IsLive()` value
and clock name from step 4. `RESULT_TRUNCATION_MS` is the number the
prediction is about; `RESULT_ONSET_MS` is pipeline-startup timing and is
expected to differ between runs for reasons unrelated to this repro. If
the live arm shows nonzero truncation, or the non-live arm shows none,
stop and report that rather than a truncation number -- per control 1,
that means the instrument is wrong, not that the prediction is refuted.
