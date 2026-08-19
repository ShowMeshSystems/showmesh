#!/usr/bin/env bash
# Seekable source for r2_seek.py. A pure sine tone repeats exactly every
# 48 samples at 1kHz/48kHz, so any 200-sample window matches infinitely
# many other windows and a byte-match search cannot locate a real seek
# landing point (found by trying it -- see LESSONS). white-noise never
# repeats, so a match is unambiguous.
set -euo pipefail

OUT="${1:?usage: r2_seek_source.sh <out.wav>}"
RATE=48000
SPB=1600
DURATION_S=3
NUM_BUFS=$((RATE * DURATION_S / SPB))

gst-launch-1.0 -e -v \
  audiotestsrc wave=white-noise samplesperbuffer=$SPB num-buffers=$NUM_BUFS \
  ! audioconvert ! "audio/x-raw,format=S16LE,channels=1,rate=$RATE" \
  ! wavenc ! filesink location="$OUT"
