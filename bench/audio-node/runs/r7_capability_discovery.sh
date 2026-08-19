#!/usr/bin/env bash
# R7: how the audio stack reports outputs, channel maps, and sample rates
# inside this container, and by what call. Input to C1's capability
# advertisement -- must be discovered, never hand-entered. This is the
# container's virtual/null device stack only; it says nothing about a
# physical interface's advertised capabilities (C0b/commissioning owns
# that).
set -euo pipefail
LOG="${1:?usage: r7_capability_discovery.sh <log_file>}"

{
  echo "=== aplay -L: PCM device names ALSA exposes ==="
  aplay -L || true
  echo
  echo "=== aplay -l: card/device enumeration ==="
  aplay -l || true
  echo
  echo "=== gst-device-monitor-1.0: NOT present in this Debian 13 package set"
  echo "    (gstreamer1.0-tools ships gst-inspect/gst-launch/gst-stats/"
  echo "    gst-tester/gst-typefind only -- confirmed via dpkg -L). C1's"
  echo "    capability discovery cannot assume this binary exists on the"
  echo "    node image without adding a further package; unresearched here. ==="
  echo
  echo "=== alsasink GObject properties (device, device-name defaults) ==="
  gst-inspect-1.0 alsasink | grep -A2 -E '^\s+device(-name)?\s+:'
  echo
  echo "=== capsfilter negotiation: rates/formats alsasink itself will accept"
  echo "    against the null device, probed by letting audioconvert/"
  echo "    audioresample negotiate freely and reporting the chosen caps ==="
  gst-launch-1.0 -v audiotestsrc num-buffers=5 ! audioconvert ! audioresample \
    ! alsasink device=default sync=false 2>&1 | grep -E 'caps = audio/x-raw' | tail -1
} > "$LOG" 2>&1
cat "$LOG"
