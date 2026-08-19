#!/usr/bin/env bash
# R3 timecode-wrap case. ltcgen starts at 23:59:58 and runs long enough to
# cross 00:00:00. Captures the LTC channel alone (no program bus) so
# analyze.py can check for a dropout or discontinuity in signal energy
# across the wrap, independent of anything on the program side.
set -euo pipefail

OUT="${1:?usage: r3_ltc_wrap.sh <out.wav>}"
RATE=48000
FPS=25
DURATION_S=4   # crosses 00:00:00 at the 2s mark, starting from 23:59:58

WORKDIR="$(dirname "$OUT")"
LTC_RAW="$WORKDIR/$(basename "$OUT" .wav)-ltc.raw"
ltcgen "$RATE" "$FPS" "$DURATION_S" 23 59 58 > "$LTC_RAW"
echo "wrap boundary expected at sample: $((2 * RATE))" 1>&2

gst-launch-1.0 -e -v \
  filesrc location="$LTC_RAW" \
    ! rawaudioparse use-sink-caps=false format=pcm pcm-format=u8 sample-rate=$RATE num-channels=1 \
    ! audioconvert ! "audio/x-raw,format=S16LE,channels=1,rate=$RATE" \
    ! wavenc ! filesink location="$OUT"
