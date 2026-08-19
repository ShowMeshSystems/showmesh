#!/usr/bin/env bash
# R4 ducking control case: an abrupt step (no ramp) via two `volume`-static
# segments joined by `concat`, for comparison against the real ramp in
# r4_duck_ramped.py.
set -euo pipefail

OUT="${1:?usage: r4_duck_abrupt.sh <out.wav> <duck_level> <total_secs>}"
DUCK_LEVEL="${2:?}"
TOTAL_SECS="${3:?}"
RATE=48000
SPB=1600
HALF_BUFS=$(python3 -c "print(int($TOTAL_SECS * $RATE / 2 / $SPB))")

gst-launch-1.0 -e -v \
  concat name=c ! wavenc ! filesink location="$OUT" \
  audiotestsrc wave=sine freq=1000 samplesperbuffer=$SPB num-buffers=$HALF_BUFS \
    ! audioconvert ! volume volume=1.0 \
    ! audioconvert ! "audio/x-raw,format=S16LE,channels=1,rate=$RATE" ! c. \
  audiotestsrc wave=sine freq=1000 samplesperbuffer=$SPB num-buffers=$HALF_BUFS \
    ! audioconvert ! volume volume=$DUCK_LEVEL \
    ! audioconvert ! "audio/x-raw,format=S16LE,channels=1,rate=$RATE" ! c.
