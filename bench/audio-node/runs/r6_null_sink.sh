#!/usr/bin/env bash
# R6: a real ALSA sink path against a userspace null PCM device (see
# asound.conf), no kernel module and no /dev/snd. Proves the element graph
# runs end to end without hardware; says nothing about a physical
# interface's device-loss behaviour (C7 owns that, with real hardware).
set -euo pipefail
LOG="${1:?usage: r6_null_sink.sh <log_file>}"

{
  echo "--- aplay -L (ALSA device list as seen inside the container) ---"
  aplay -L || true
  echo "--- gst pipeline against the ALSA null device ---"
  gst-launch-1.0 -e -v \
    audiotestsrc wave=sine num-buffers=50 ! audioconvert ! audioresample \
    ! alsasink device=default sync=false
  echo "--- pipeline exit code: $? ---"
} > "$LOG" 2>&1
cat "$LOG"
