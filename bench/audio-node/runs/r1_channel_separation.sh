#!/usr/bin/env bash
# R1: graph-level channel separation. Interleave a stereo program bus
# (silence) and a mono LTC-designated channel (1kHz tone) into one
# 3-channel stream, per AUDIO-ENGINE section 6. This is a graph property,
# not a physical-channel claim -- see README.md.
set -euo pipefail

OUT="${1:?usage: r1_channel_separation.sh <out.wav>}"
RATE=48000
DURATION_S=1
SAMPLES_PER_BUF=1600
NUM_BUFS=$((RATE * DURATION_S / SAMPLES_PER_BUF))

gst-launch-1.0 -e -v \
  interleave name=i ! wavenc ! filesink location="$OUT" \
  audiotestsrc wave=silence samplesperbuffer=$SAMPLES_PER_BUF num-buffers=$NUM_BUFS \
    ! audioconvert ! "audio/x-raw,format=S16LE,channels=1,rate=$RATE" ! queue ! i.sink_0 \
  audiotestsrc wave=silence samplesperbuffer=$SAMPLES_PER_BUF num-buffers=$NUM_BUFS \
    ! audioconvert ! "audio/x-raw,format=S16LE,channels=1,rate=$RATE" ! queue ! i.sink_1 \
  audiotestsrc wave=sine freq=1000 samplesperbuffer=$SAMPLES_PER_BUF num-buffers=$NUM_BUFS \
    ! audioconvert ! "audio/x-raw,format=S16LE,channels=1,rate=$RATE" ! queue ! i.sink_2
