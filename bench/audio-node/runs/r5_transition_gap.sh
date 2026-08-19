#!/usr/bin/env bash
# R5: inter-item gap, measured in samples. Two variants:
#  - "sequential": two independent gst-launch-1.0 processes, each writing
#    its own file, played back to back by a *supervisor* starting the
#    second only after the first's process exits -- ordinary sequential
#    playback, deliberately not called gapless.
#  - "concat": both items joined by the `concat` element inside one
#    pipeline, GStreamer's own gapless mechanism.
# Both variants render to a single WAV per run so the gap is measurable
# directly from the file.
set -euo pipefail

MODE="${1:?usage: r5_transition_gap.sh <sequential|concat> <out.wav>}"
OUT="${2:?usage: r5_transition_gap.sh <sequential|concat> <out.wav>}"
RATE=48000
SPB=1600
ITEM_SECS=1
ITEM_BUFS=$((RATE * ITEM_SECS / SPB))

case "$MODE" in
  concat)
    gst-launch-1.0 -e -v \
      concat name=c ! audioconvert ! "audio/x-raw,format=S16LE,channels=1,rate=$RATE" ! wavenc ! filesink location="$OUT" \
      audiotestsrc wave=sine freq=800 samplesperbuffer=$SPB num-buffers=$ITEM_BUFS \
        ! audioconvert ! "audio/x-raw,format=S16LE,channels=1,rate=$RATE" ! c. \
      audiotestsrc wave=sine freq=1600 samplesperbuffer=$SPB num-buffers=$ITEM_BUFS \
        ! audioconvert ! "audio/x-raw,format=S16LE,channels=1,rate=$RATE" ! c.
    ;;
  sequential)
    # "Ordinary sequential playback" here means what it actually is: two
    # independent gst-launch-1.0 processes, the second started only after
    # the first exits, with nothing pre-negotiated between them. The gap
    # this produces is wall-clock process/pipeline-restart latency, not a
    # sample-domain splice, so it is measured in wall-clock time (written
    # to the *.timing file next to $OUT) and reported by analyze.py as an
    # equivalent sample count at $RATE for comparability with the concat
    # case, explicitly labelled as such.
    WORKDIR="$(dirname "$OUT")"
    TIMING="$WORKDIR/$(basename "$OUT" .wav).timing"
    A="$WORKDIR/$(basename "$OUT" .wav)-a.raw"
    B="$WORKDIR/$(basename "$OUT" .wav)-b.raw"
    gst-launch-1.0 -e audiotestsrc wave=sine freq=800 samplesperbuffer=$SPB num-buffers=$ITEM_BUFS \
      ! audioconvert ! "audio/x-raw,format=S16LE,channels=1,rate=$RATE" ! filesink location="$A"
    T0=$(date +%s.%N)
    gst-launch-1.0 -e audiotestsrc wave=sine freq=1600 samplesperbuffer=$SPB num-buffers=$ITEM_BUFS \
      ! audioconvert ! "audio/x-raw,format=S16LE,channels=1,rate=$RATE" ! filesink location="$B"
    T1=$(date +%s.%N)
    # Neither pipeline is a live source, so GStreamer renders as fast as
    # it can rather than pacing to wall-clock time -- "nominal duration"
    # is not a real quantity here. What IS real: the wall-clock time from
    # item1's process exiting to item2's process exiting, i.e. the cost of
    # starting, negotiating, and running a second independent
    # gst-launch-1.0 process for the next item. That is what ordinary
    # sequential playback actually pays, on this host, for this pipeline.
    python3 -c "print($T1 - $T0)" > "$TIMING"
    cat "$A" "$B" > "$WORKDIR/$(basename "$OUT" .wav)-cat.raw"
    gst-launch-1.0 -e filesrc location="$WORKDIR/$(basename "$OUT" .wav)-cat.raw" \
      ! rawaudioparse use-sink-caps=false format=pcm pcm-format=s16le sample-rate=$RATE num-channels=1 \
      ! wavenc ! filesink location="$OUT"
    ;;
  *)
    echo "unknown mode $MODE" >&2
    exit 2
    ;;
esac
