# Track C phase 1 spike: go-gst against a running audiomixer

Throwaway exploratory code, not part of ShowMesh. It answers the five
questions the phase 1 engine design (`internal/agent/audio/gstengine`)
depends on, by running real GStreamer 1.28.6 (Homebrew, macOS/arm64)
through go-gst rather than by reasoning about the C API. `main.go` has one
function per question, run with `./spike <a|b|c|d|e>`. `out/` holds the
captured stdout of the run each finding below cites.

This machine has no `/dev/snd` and no ALSA; every pipeline here uses
`fakesink`/`autoaudiosink`-free graphs so the questions answered are
graph-level GStreamer behavior, not physical-device behavior. That
boundary is the same one `bench/audio-node/README.md` draws for the
existing Debian bench, and it applies here too: nothing in this spike is
evidence about a real audio interface.

## a. Adding and removing a source branch on a running audiomixer

`spikeA_DynamicAddRemove` starts a pipeline with one branch into
`audiomixer`, lets it run, then builds a second branch
(`audiotestsrc ! audioconvert ! audioresample`), links it to a
`request_pad_simple("sink_%u")` on the running mixer, and calls
`SyncStateWithParent()` on each new element. It counts buffers reaching
the final sink via a pad probe throughout. To remove the branch cleanly it
installs an idle+block probe on the branch's src pad, waits for the probe
to actually fire (proving the pad is genuinely idle before touching it),
sets the branch elements to NULL, unlinks, calls
`ReleaseRequestPad`, then `Remove`s the elements from the bin.

Observed (`out/spike_a.log`): buffer count strictly increased across all
three phases (before second branch, after add, after remove), with the
mixed rate visibly higher while both branches were live (about 2x
buffers/second) and dropping back down after removal. The pipeline never
stalled, errored, or required a state change on the parent bin.

**a: confirmed working as the architecture assumes.**

## b. Pausing one branch while the rest of the mix keeps playing

`spikeB_PauseOneBranch` runs two branches into one `audiomixer` with
`ignore-inactive-pads=true`. It counts buffers separately on branch 2's
own pad and on the mixer's final output. After a baseline period, it
installs a `PadProbeTypeBlockDownstream` probe on branch 2's src pad (the
mechanism that "pauses" a branch: no new buffers can pass) and holds it,
then samples both counters again after a wait window.

Observed (`out/spike_b.log`): branch 2's buffer count froze exactly
(22 before, 22 after 700ms) while the mixer's total kept climbing (+72)
during that same window. `ignore-inactive-pads=true` on `audiomixer` is
what keeps the mix from stalling waiting on a blocked pad; without it the
mixer would time out waiting for the blocked pad's next buffer.

Position for a paused branch is what the engine already tracks itself
(the last position observed before the block probe engaged), not
something read back from the pad; GStreamer has no query that reports "how
far did this specific sink pad get" independent of the element's own
position query, and `audiomixer` doesn't expose per-pad position. The
phase 1 engine computes a paused handle's position the same way
`FakeEngine` does: freeze it at pause time, not by querying anything.

**b: confirmed working. `ignore-inactive-pads=true` plus a block probe on
the branch's own src pad is the mechanism; per-branch position is
engine-tracked, not pad-queried.**

## c. GstController-driven per-branch fades, read back accurately

`spikeC_GstControllerFade` builds a `GstController.InterpolationControlSource`
with two timed control points (1.0 at t=0, 0.3 at t=500ms, linear mode),
binds it to a `volume` element's `volume` property with
`NewDirectControlBindingAbsolute` (not the non-absolute constructor), and
reads the `volume` property back at three points in wall-clock time.

Observed (`out/spike_c.log`): 0.9998 at t=0, 0.6747 at t=250ms (roughly
mid-ramp, consistent with linear interpolation from 1.0 toward 0.3), 0.3000
at t=750ms (250ms after the ramp's nominal end). No 10x scale defect: the
prior recorded measurement (`new()` maps a 0..1 control value onto the
property's full 0..10 range) does not reproduce with
`NewDirectControlBindingAbsolute`, which is the constructor this spike
used throughout.

**c: confirmed working, including the specific prior finding about
`new_absolute` vs `new()` not reproducing here.**

## d. Distinguishing natural per-branch completion from a commanded stop

`spikeD_PerBranchEOS` runs two branches into one `audiomixer`
(`ignore-inactive-pads=true`); branch 1 is bounded (`num-buffers=20`) so it
reaches EOS on its own while branch 2 keeps running indefinitely. An event
probe on the mixer's `sink_0` pad watches for an EOS event landing on that
specific pad, separately from the pipeline bus's own EOS message.

Observed (`out/spike_d.log`): the branch-level EOS event was observed on
`mix.sink_0` well before any pipeline-wide EOS bus message (none arrived
at all in the run), and the mixer's output kept growing afterward (+40
buffers) on the strength of the surviving branch. This is exactly the
distinction the engine needs: an `EventEOS` probe on a branch's own sink
pad into the mixer fires when that branch specifically completes, and does
not require or wait for every other branch to also finish.

**d: confirmed working. A per-pad EOS event probe on the branch's mixer
sink pad is the mechanism; it is structurally distinct from the
pipeline-wide EOS message a commanded pipeline-level stop would produce.**

## e. `interleave` onto chosen channel indices of a multichannel output

`spikeE_InterleaveChannelIndices` builds four mono `audiotestsrc` branches
into `interleave`'s `sink_0..sink_3` pads (in the order requested) with
`channel-positions-from-input=false`, forcing a 4-channel output. Program
(sine 220Hz) is on `sink_0`, silence on `sink_1`, a second tone (2000Hz,
standing in for what a real LTC branch would carry) on `sink_2`, silence
on `sink_3`.

Observed (`out/spike_e.log`): the pipeline negotiated a real 4-channel
`audio/x-raw` output, ran every branch to its own EOS, and delivered 30
buffers to the final sink. `interleave`'s sink pad request order is what
places content on a given output channel index (`sink_0` becomes
output channel 1, `sink_1` channel 2, and so on) — there is no separate
"channel index" property to set per pad; channel placement is entirely a
function of which sink pad number a branch is linked to.

**e: confirmed working. The engine places the program bus by connecting
it to `interleave`'s sink pad matching its 1-based output channel index
minus one, and reserves a specific sink pad number for the LTC seam the
same way, without generating any LTC content itself.**

## Summary

All five items (a through e) were observed working as the phase 1 engine
design assumes, on this machine's GStreamer 1.28.6. None required a
workaround that would move sample generation into Go (which would violate
ADR-007); every mechanism here is native GStreamer element/pipeline
behavior driven through go-gst bindings that map directly onto the C API
(pad probes, `GstController`, request pads, `interleave`).

What this spike does not and cannot establish: anything about a real ALSA
device, real multichannel hardware, whether N reported channels are N
independently addressable outputs on a physical interface, or Linux
behavior generally (this machine is macOS/arm64). Those remain per-device
commissioning and Linux-host questions, same as the existing
`bench/audio-node` Debian bench already states for its own scope.

## Two implementation-detail spikes (f, g), run while building the engine

Not part of the required a-e set; kept because they answer two questions
the engine implementation actually hit and needed real evidence for,
rather than assumption.

**f.** `dec.Connect("pad-added", func(self gst.Element, pad gst.Pad) {...})`
is the correct closure signature for `decodebin`'s `pad-added` signal —
confirmed via `gst-inspect-1.0 decodebin`'s own signal signature
(`void user_function (GstElement*, GstPad*, gpointer)`) and by linking a
real `filesrc ! decodebin` branch into a running `audiomixer` at runtime
(`out/spike_f.log`). A wrong parameter count panics immediately at
`Connect` time with "callback function has N parameters, but M were
provided" — found the hard way on `fakesink`'s three-parameter `handoff`
signal (element, buffer, pad) before switching every buffer-counting
probe in this spike to a pad probe instead.

**g.** An undecodable file's error surfaces as a `GstMessageError` whose
source is `typefind` (not `decodebin` or `filesrc` directly), with a
parent chain of `[typefind, decodebin-name, pipeline-name]`
(`out/spike_g.log`). The real engine's bus dispatcher walks
`msg.Source().GetParent()` up that chain looking for a name it recognizes
as one of a branch's own elements — this only works if the branch's
element names are registered for lookup *before* the branch is
considered loaded, since the error can arrive during that unconfirmed
window. The first implementation registered names only after a
successful load and the undecodable-file test hung until its context
deadline; `out/spike_g.log` is what showed the actual bus message shape
needed to fix it.
