#!/usr/bin/env python3
"""R4 ducking, ramped case. Binds a real GstController
InterpolationControlSource to a `volume` element's `volume` property --
this is a genuinely dynamic run per the seam spec ("a small ... supervisor
if a run genuinely needs dynamic control"), and it never touches a sample:
GStreamer's own controller subsystem drives the ramp and `volume` mixes it,
exactly as ADR-007 requires. This script only builds the graph and binds
the control points.
"""
import sys

import gi

gi.require_version("Gst", "1.0")
gi.require_version("GstController", "1.0")
from gi.repository import GLib, Gst, GstController  # noqa: E402

RATE = 48000


def build_and_run(out_path, fade_ms, duck_level, total_secs):
    Gst.init(None)
    spb = 1600
    num_buffers = int(total_secs * RATE / spb)
    desc = (
        f"audiotestsrc wave=sine freq=1000 is-live=false "
        f"samplesperbuffer={spb} num-buffers={num_buffers} "
        f"! audioconvert ! volume name=vol volume=1.0 "
        f"! audioconvert ! audio/x-raw,format=S16LE,channels=1,rate={RATE} "
        f"! wavenc ! filesink location={out_path}"
    )
    pipeline = Gst.parse_launch(desc)
    vol = pipeline.get_by_name("vol")

    cs = GstController.InterpolationControlSource()
    cs.set_property("mode", GstController.InterpolationMode.LINEAR)
    # new() (not new_absolute()) treats the control source's 0..1 output as
    # a *normalized position* across the property's full min..max range --
    # for `volume`'s 0..10 range, a control value of 1.0 became an actual
    # volume of 10.0 and clipped hard square. new_absolute() takes the
    # control value as the literal property value, which is what a real
    # gain ramp means. Found by inspecting the captured WAV, not assumed.
    binding = GstController.DirectControlBinding.new_absolute(vol, "volume", cs)
    vol.add_control_binding(binding)

    ramp_start_s = total_secs * 0.3
    ramp_end_s = ramp_start_s + (fade_ms / 1000.0)
    cs.set(0, 1.0)
    cs.set(int(ramp_start_s * Gst.SECOND), 1.0)
    cs.set(int(ramp_end_s * Gst.SECOND), duck_level)
    cs.set(int(total_secs * Gst.SECOND), duck_level)

    loop = GLib.MainLoop()
    bus = pipeline.get_bus()
    bus.add_signal_watch()

    state = {"error": None}

    def on_message(_bus, msg):
        t = msg.type
        if t == Gst.MessageType.EOS:
            loop.quit()
        elif t == Gst.MessageType.ERROR:
            err, dbg = msg.parse_error()
            state["error"] = f"{err}: {dbg}"
            loop.quit()

    bus.connect("message", on_message)
    pipeline.set_state(Gst.State.PLAYING)
    loop.run()
    pipeline.set_state(Gst.State.NULL)
    # Block until the NULL transition completes -- see r2_seek.py's
    # comment on the same call for the truncated-file failure this avoids.
    pipeline.get_state(Gst.CLOCK_TIME_NONE)
    if state["error"]:
        print(f"pipeline error: {state['error']}", file=sys.stderr)
        sys.exit(1)
    print(
        f"ramp: start={ramp_start_s}s end={ramp_end_s}s "
        f"fade_ms={fade_ms} duck_level={duck_level}",
        file=sys.stderr,
    )


if __name__ == "__main__":
    if len(sys.argv) != 5:
        print(
            f"usage: {sys.argv[0]} <out.wav> <fade_ms> <duck_level 0..1> <total_secs>",
            file=sys.stderr,
        )
        sys.exit(2)
    out_path, fade_ms, duck_level, total_secs = sys.argv[1:5]
    build_and_run(out_path, float(fade_ms), float(duck_level), float(total_secs))
