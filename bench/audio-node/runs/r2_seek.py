#!/usr/bin/env python3
"""R2 "seek" case. Plays back a previously-captured multichannel WAV
(r2_baseline.sh's output) through a seekable file pipeline, issues a real
GStreamer flushing SEEK event to a target time once PAUSED/pre-rolled, then
lets it play to EOS while writing a second WAV. This exercises GStreamer's
own seek machinery (not just "start reading from a byte offset"), which is
the dynamic control the seam spec allows a small supervisor for. No sample
is touched by this script; wavparse/audioconvert/wavenc do all of it.
"""
import sys

import gi

gi.require_version("Gst", "1.0")
from gi.repository import GLib, Gst  # noqa: E402


def run(src_path, out_path, seek_target_s):
    Gst.init(None)
    desc = (
        f"filesrc location={src_path} ! wavparse ! audioconvert "
        f"! wavenc ! filesink location={out_path}"
    )
    pipeline = Gst.parse_launch(desc)

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
    pipeline.set_state(Gst.State.PAUSED)
    # get_state(CLOCK_TIME_NONE) blocks synchronously until preroll
    # actually completes, so the seek below is issued deterministically
    # once pre-rolled -- no race against an ASYNC_DONE bus message. An
    # earlier version seeked from the ASYNC_DONE handler instead, which
    # intermittently produced a 0-byte output file (filesink never wrote
    # anything, not even the WAV header): the message and this blocking
    # call are not the same event, and storage-backend I/O speed (a Docker
    # named volume reproduced it consistently; a host bind mount mostly
    # hid it) changed which one "won".
    pipeline.get_state(Gst.CLOCK_TIME_NONE)
    ok = pipeline.seek_simple(
        Gst.Format.TIME,
        Gst.SeekFlags.FLUSH | Gst.SeekFlags.ACCURATE,
        int(seek_target_s * Gst.SECOND),
    )
    print(f"seek_simple to {seek_target_s}s returned {ok}", file=sys.stderr)
    pipeline.set_state(Gst.State.PLAYING)
    loop.run()
    pipeline.set_state(Gst.State.NULL)
    # set_state() is asynchronous; without blocking here the process can
    # exit before filesink/wavenc actually flush and close the output
    # file. Found by this exact failure: intermittent (storage-backend
    # dependent -- a Docker named volume reproduced it far more readily
    # than a host bind mount) truncated/empty WAVs downstream, never
    # reported as a pipeline error because the write itself never failed,
    # it just hadn't happened yet when the script returned.
    pipeline.get_state(Gst.CLOCK_TIME_NONE)
    if state["error"]:
        print(f"pipeline error: {state['error']}", file=sys.stderr)
        sys.exit(1)


if __name__ == "__main__":
    if len(sys.argv) != 4:
        print(f"usage: {sys.argv[0]} <src.wav> <out.wav> <seek_target_seconds>", file=sys.stderr)
        sys.exit(2)
    run(sys.argv[1], sys.argv[2], float(sys.argv[3]))
