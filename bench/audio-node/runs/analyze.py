#!/usr/bin/env python3
"""Stdlib-only WAV analysis for Track C seam C0a-1's measurement runs.

No numpy, no pip, per the seam spec. Every function here reads bytes a
GStreamer pipeline already wrote to a file; nothing here generates or
mixes a sample -- that would violate ADR-007 as surely as Go would.
"""
import array
import json
import sys
import wave


def read_wav_channels(path):
    """Return (n_channels, rate, sampwidth, [array per channel])."""
    with wave.open(path, "rb") as w:
        n_channels = w.getnchannels()
        rate = w.getframerate()
        sampwidth = w.getsampwidth()
        n_frames = w.getnframes()
        raw = w.readframes(n_frames)
    if sampwidth != 2:
        raise ValueError(f"expected 16-bit samples, got {sampwidth*8}-bit")
    samples = array.array("h")
    samples.frombytes(raw)
    if sys.byteorder == "big":
        samples.byteswap()
    channels = [array.array("h") for _ in range(n_channels)]
    for i in range(n_channels):
        channels[i].extend(samples[i::n_channels])
    return n_channels, rate, sampwidth, channels


def rms(chan):
    if not chan:
        return 0.0
    total = sum(s * s for s in chan)
    return (total / len(chan)) ** 0.5


def peak_abs(chan):
    return max((abs(s) for s in chan), default=0)


def max_sample_delta(chan):
    """Largest |x[n] - x[n-1]|, the click-free-ness discontinuity metric."""
    m = 0
    for i in range(1, len(chan)):
        d = abs(chan[i] - chan[i - 1])
        if d > m:
            m = d
    return m


def envelope_max_step(chan, window):
    """Largest jump between consecutive windowed peak-amplitudes.

    A gain/volume change on a periodic tone does not show up as a large
    raw sample-to-sample delta (the waveform is still smooth from sample
    to sample; only its envelope changed), so max_sample_delta cannot see
    it. This is the metric that actually distinguishes a click (envelope
    jumps in under one window) from a fade (envelope moves gradually
    across many windows) -- confirmed by inspecting a real abrupt-step
    capture where max_sample_delta was identical to steady state despite a
    5x peak-amplitude change at the splice.
    """
    if window <= 0 or len(chan) < 2 * window:
        return None
    peaks = []
    for i in range(0, len(chan) - window, window):
        peaks.append(max(abs(s) for s in chan[i:i + window]))
    return max((abs(peaks[i] - peaks[i - 1]) for i in range(1, len(peaks))), default=0)


def first_rising_edge(chan, threshold, start=0):
    """Index of first sample at/after `start` where |value| crosses
    threshold from below. `start` lets a caller skip past an earlier,
    already-known edge (e.g. a track-change boundary further into the
    signal) instead of re-finding it."""
    start = max(start, 1)
    for i in range(start, len(chan)):
        if abs(chan[i - 1]) < threshold <= abs(chan[i]):
            return i
    return None


def first_silence_end(chan, threshold, min_run):
    """Index of the first sample after an initial run of >=min_run samples
    at/under threshold -- used to find where a gap ends."""
    run = 0
    for i, s in enumerate(chan):
        if abs(s) <= threshold:
            run += 1
        else:
            if run >= min_run:
                return i
            return None
    return None


def cmd_r1(args):
    path = args[0]
    n_channels, rate, sampwidth, channels = read_wav_channels(path)
    result = {
        "run": "r1_channel_separation",
        "file": path,
        "sample_rate": rate,
        "n_channels": n_channels,
        "per_channel_rms": [rms(c) for c in channels],
        "per_channel_peak": [peak_abs(c) for c in channels],
    }
    print(json.dumps(result))


def cmd_r2(args):
    """args: wavpath program_channel ltc_channel threshold label
    [program_search_start] [ltc_search_start]"""
    path, program_ch, ltc_ch, threshold, label = args[:5]
    program_ch = int(program_ch)
    ltc_ch = int(ltc_ch)
    threshold = int(threshold)
    prog_start = int(args[5]) if len(args) > 5 else 0
    ltc_start = int(args[6]) if len(args) > 6 else 0
    n_channels, rate, sampwidth, channels = read_wav_channels(path)
    prog_edge = first_rising_edge(channels[program_ch], threshold, prog_start)
    ltc_edge = first_rising_edge(channels[ltc_ch], threshold, ltc_start)
    offset_samples = None
    if prog_edge is not None and ltc_edge is not None:
        offset_samples = prog_edge - ltc_edge
    result = {
        "run": "r2_alignment",
        "label": label,
        "file": path,
        "sample_rate": rate,
        "program_edge_sample": prog_edge,
        "ltc_edge_sample": ltc_edge,
        "offset_samples": offset_samples,
        "offset_ms": (offset_samples / rate * 1000.0) if offset_samples is not None and rate else None,
    }
    print(json.dumps(result))


def cmd_r4(args):
    """args: rampedwav ramped_center ramped_radius abruptwav abrupt_center
    abrupt_radius fade_ms

    Windows are required, not optional: a sine tone's own per-sample slope
    can exceed the discontinuity a fade or a step adds, so a whole-file
    max-delta scan is dominated by the waveform itself rather than by the
    transition. Only the window immediately around the known transition
    sample isolates the transition's own contribution; each file's window
    also carries its own baseline (a same-size window taken from
    undisturbed, constant-volume signal in the same file) so the
    transition's delta can be compared against that file's own steady-state
    noise floor for that metric, not against the other file's.
    """
    (ramped_path, ramped_center, ramped_radius,
     abrupt_path, abrupt_center, abrupt_radius, fade_ms) = args
    ramped_center = int(ramped_center)
    ramped_radius = int(ramped_radius)
    abrupt_center = int(abrupt_center)
    abrupt_radius = int(abrupt_radius)
    _, rate, _, ramped_chans = read_wav_channels(ramped_path)
    _, _, _, abrupt_chans = read_wav_channels(abrupt_path)
    rc = ramped_chans[0]
    ac = abrupt_chans[0]

    def window(chan, center, radius):
        return chan[max(0, center - radius):center + radius]

    def baseline(chan, radius):
        # A same-size window from early in the file, well before any
        # transition, as this file's own undisturbed-waveform reference.
        return chan[radius:2 * radius] if len(chan) > 2 * radius else chan[:radius]

    envelope_window = 50  # ~1 cycle of a 1kHz tone at 48kHz
    result = {
        "run": "r4_ducking",
        "ramped_file": ramped_path,
        "abrupt_file": abrupt_path,
        "sample_rate": rate,
        "fade_ms_requested": float(fade_ms),
        "ramped_transition_max_delta": max_sample_delta(window(rc, ramped_center, ramped_radius)),
        "ramped_baseline_max_delta": max_sample_delta(baseline(rc, ramped_radius)),
        "abrupt_transition_max_delta": max_sample_delta(window(ac, abrupt_center, abrupt_radius)),
        "abrupt_baseline_max_delta": max_sample_delta(baseline(ac, abrupt_radius)),
        "ramped_envelope_max_step": envelope_max_step(window(rc, ramped_center, ramped_radius), envelope_window),
        "abrupt_envelope_max_step": envelope_max_step(window(ac, abrupt_center, abrupt_radius), envelope_window),
    }
    print(json.dumps(result))


def cmd_r5(args):
    """args: wavpath channel threshold min_silence_samples"""
    path, channel, threshold, min_silence = args
    channel = int(channel)
    threshold = int(threshold)
    min_silence = int(min_silence)
    n_channels, rate, sampwidth, channels = read_wav_channels(path)
    chan = channels[channel]
    # Find the end of the first tone, the run of silence after it (the
    # inter-item gap), and the start of the second tone.
    first_end = None
    for i, s in enumerate(chan):
        if abs(s) > threshold:
            continue
        # candidate silence start; require it holds for min_silence
        run_ok = all(abs(x) <= threshold for x in chan[i:i + min_silence])
        if run_ok and i > 0:
            first_end = i
            break
    gap_end = None
    if first_end is not None:
        for i in range(first_end, len(chan)):
            if abs(chan[i]) > threshold:
                gap_end = i
                break
    gap_samples = (gap_end - first_end) if (first_end is not None and gap_end is not None) else None
    result = {
        "run": "r5_transition_gap",
        "file": path,
        "sample_rate": rate,
        "item1_end_sample": first_end,
        "item2_start_sample": gap_end,
        "gap_samples": gap_samples,
        "gap_ms": (gap_samples / rate * 1000.0) if gap_samples is not None and rate else None,
    }
    print(json.dumps(result))


def cmd_r2trackchange(args):
    """args: wavpath program_channel threshold search_start expected_onset_sample"""
    path, program_ch, threshold, search_start, expected = args
    program_ch = int(program_ch)
    threshold = int(threshold)
    search_start = int(search_start)
    expected = int(expected)
    n_channels, rate, sampwidth, channels = read_wav_channels(path)
    measured = first_rising_edge(channels[program_ch], threshold, search_start)
    result = {
        "run": "r2_trackchange",
        "file": path,
        "sample_rate": rate,
        "expected_onset_sample": expected,
        "measured_onset_sample": measured,
        "detection_error_samples": (measured - expected) if measured is not None else None,
    }
    print(json.dumps(result))


def cmd_r3wrap(args):
    """args: wavpath wrap_boundary_sample window_samples"""
    path, boundary, window = args
    boundary = int(boundary)
    window = int(window)
    n_channels, rate, sampwidth, channels = read_wav_channels(path)
    chan = channels[0]
    before = chan[max(0, boundary - window):boundary]
    after = chan[boundary:boundary + window]
    far_from_wrap = chan[window:2 * window] if len(chan) > 2 * window else chan[:window]
    result = {
        "run": "r3_ltc_wrap",
        "file": path,
        "sample_rate": rate,
        "total_samples": len(chan),
        "wrap_boundary_sample": boundary,
        "rms_before_wrap": rms(before),
        "rms_after_wrap": rms(after),
        "rms_far_from_wrap_reference": rms(far_from_wrap),
        "max_delta_across_wrap_window": max_sample_delta(chan[max(0, boundary - window):boundary + window]),
        "max_delta_far_from_wrap_reference": max_sample_delta(far_from_wrap),
    }
    print(json.dumps(result))


def cmd_r2seek(args):
    """args: src_wav out_wav requested_target_sample search_radius"""
    src_path, out_path, target, radius = args
    target = int(target)
    radius = int(radius)
    _, rate, _, src_chans = read_wav_channels(src_path)
    _, _, _, out_chans = read_wav_channels(out_path)
    src = src_chans[0]
    out = out_chans[0]
    n = 200
    first = list(out[:n])
    match_offset = None
    for off in range(max(0, target - radius), target + radius):
        if list(src[off:off + n]) == first:
            match_offset = off
            break
    result = {
        "run": "r2_seek",
        "src_file": src_path,
        "out_file": out_path,
        "sample_rate": rate,
        "requested_target_sample": target,
        "measured_landing_sample": match_offset,
        "seek_error_samples": (match_offset - target) if match_offset is not None else None,
    }
    print(json.dumps(result))


COMMANDS = {
    "r1": cmd_r1, "r2": cmd_r2, "r2trackchange": cmd_r2trackchange,
    "r2seek": cmd_r2seek, "r3wrap": cmd_r3wrap, "r4": cmd_r4, "r5": cmd_r5,
}

if __name__ == "__main__":
    if len(sys.argv) < 2 or sys.argv[1] not in COMMANDS:
        print(f"usage: {sys.argv[0]} <{'|'.join(COMMANDS)}> [args...]", file=sys.stderr)
        sys.exit(2)
    COMMANDS[sys.argv[1]](sys.argv[2:])
