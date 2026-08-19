#!/usr/bin/env bash
# R2 "track change" case. Program bus: [A silence][B tone1][C tone2],
# built with `concat` so a mid-timeline item switch happens at sample A+B.
# LTC bus (ch2) is untouched by the switch, continuous across it: [A
# silence][ltcgen for B+C]. Measures whether an item boundary on the
# program branch disturbs the alignment established at start -- program's
# tone2 onset is expected at exactly A+B.
set -euo pipefail

OUT="${1:?usage: r2_trackchange.sh <out.wav>}"
RATE=48000
FPS=25
SPB=1600
A_BUFS=15                 # 0.5s lead-in silence
B_SECS=1
C_SECS=1
B_BUFS=$((RATE * B_SECS / SPB))
C_BUFS=$((RATE * C_SECS / SPB))
TOTAL_LTC_SECS=$((B_SECS + C_SECS))

WORKDIR="$(dirname "$OUT")"
LTC_RAW="$WORKDIR/$(basename "$OUT" .wav)-ltc.raw"
ltcgen "$RATE" "$FPS" "$TOTAL_LTC_SECS" > "$LTC_RAW"

echo "expected tone2 onset sample (A+B): $(( (A_BUFS + B_BUFS) * SPB ))" 1>&2

gst-launch-1.0 -e -v \
  interleave name=i ! wavenc ! filesink location="$OUT" \
  concat name=progc ! tee name=progtee \
  progtee. ! queue ! audioconvert ! "audio/x-raw,format=S16LE,channels=1,rate=$RATE" ! queue ! i.sink_0 \
  progtee. ! queue ! audioconvert ! "audio/x-raw,format=S16LE,channels=1,rate=$RATE" ! queue ! i.sink_1 \
  audiotestsrc wave=silence samplesperbuffer=$SPB num-buffers=$A_BUFS \
    ! audioconvert ! "audio/x-raw,format=S16LE,channels=1,rate=$RATE" ! progc. \
  audiotestsrc wave=sine freq=800 volume=0.2 samplesperbuffer=$SPB num-buffers=$B_BUFS \
    ! audioconvert ! "audio/x-raw,format=S16LE,channels=1,rate=$RATE" ! progc. \
  audiotestsrc wave=sine freq=1600 volume=0.9 samplesperbuffer=$SPB num-buffers=$C_BUFS \
    ! audioconvert ! "audio/x-raw,format=S16LE,channels=1,rate=$RATE" ! progc. \
  concat name=ltcc ! queue ! audioconvert ! "audio/x-raw,format=S16LE,channels=1,rate=$RATE" ! queue ! i.sink_2 \
  audiotestsrc wave=silence samplesperbuffer=$SPB num-buffers=$A_BUFS \
    ! audioconvert ! "audio/x-raw,format=S16LE,channels=1,rate=$RATE" ! ltcc. \
  filesrc location="$LTC_RAW" \
    ! rawaudioparse use-sink-caps=false format=pcm pcm-format=u8 sample-rate=$RATE num-channels=1 \
    ! ltcc.
