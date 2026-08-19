#!/usr/bin/env bash
# R2 baseline + R3's "start" case, and the building block for the
# track-change and restart cases below.
#
# Program bus (mono, duplicated to ch0/ch1 via tee): [A samples silence]
# [B samples 1kHz tone] built with `concat`, so the tone's onset sample is
# known exactly (=A). LTC bus (ch2): [A samples silence][ltcgen output for
# the same B-sample duration], via the same `concat` element, so the LTC
# signal's first non-silent sample is also expected at exactly A -- if the
# pipeline preserves sample alignment between the two concat chains and
# `interleave`, analyze.py's r2 command should measure offset_samples≈0.
# Any nonzero value is a real measurement of this pipeline's alignment,
# not an assumption.
set -euo pipefail

OUT="${1:?usage: r2_baseline.sh <out.wav> [start_hh] [start_mm] [start_ss]}"
START_HH="${2:-0}"
START_MM="${3:-0}"
START_SS="${4:-0}"

RATE=48000
FPS=25
SPB=1600          # samples per buffer
A_BUFS=15         # 0.5s silence lead-in
B_SECS=1
B_BUFS=$((RATE * B_SECS / SPB))

WORKDIR="$(dirname "$OUT")"
LTC_RAW="$WORKDIR/$(basename "$OUT" .wav)-ltc.raw"
ltcgen "$RATE" "$FPS" "$B_SECS" "$START_HH" "$START_MM" "$START_SS" > "$LTC_RAW"
LTC_BYTES=$(wc -c < "$LTC_RAW")

echo "lead-in samples (A): $((A_BUFS * SPB))" 1>&2
echo "ltc raw bytes for ${B_SECS}s @ ${RATE}Hz U8 mono: $LTC_BYTES" 1>&2

gst-launch-1.0 -e -v \
  interleave name=i ! wavenc ! filesink location="$OUT" \
  concat name=progc ! tee name=progtee \
  progtee. ! queue ! audioconvert ! "audio/x-raw,format=S16LE,channels=1,rate=$RATE" ! queue ! i.sink_0 \
  progtee. ! queue ! audioconvert ! "audio/x-raw,format=S16LE,channels=1,rate=$RATE" ! queue ! i.sink_1 \
  audiotestsrc wave=silence samplesperbuffer=$SPB num-buffers=$A_BUFS \
    ! audioconvert ! "audio/x-raw,format=S16LE,channels=1,rate=$RATE" ! progc. \
  audiotestsrc wave=sine freq=1000 samplesperbuffer=$SPB num-buffers=$B_BUFS \
    ! audioconvert ! "audio/x-raw,format=S16LE,channels=1,rate=$RATE" ! progc. \
  concat name=ltcc ! queue ! audioconvert ! "audio/x-raw,format=S16LE,channels=1,rate=$RATE" ! queue ! i.sink_2 \
  audiotestsrc wave=silence samplesperbuffer=$SPB num-buffers=$A_BUFS \
    ! audioconvert ! "audio/x-raw,format=S16LE,channels=1,rate=$RATE" ! ltcc. \
  filesrc location="$LTC_RAW" \
    ! rawaudioparse use-sink-caps=false format=pcm pcm-format=u8 sample-rate=$RATE num-channels=1 \
    ! ltcc.
