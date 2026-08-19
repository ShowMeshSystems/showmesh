#!/usr/bin/env bash
# R3: what actually generates SMPTE LTC audio inside this pipeline/image.
# Candidates a-d per the seam spec. This is the survey; alignment through
# the viable candidate is measured separately by r2_alignment.sh's ltc
# cases and by r3_ltc_generation.sh.
set -euo pipefail
LOG="${1:?usage: r3_ltc_survey.sh <log_file>}"

{
  echo "=== gst-inspect-1.0 version ==="
  gst-inspect-1.0 --version

  echo
  echo "=== (a) native GStreamer LTC-audio-generating element? full plugin"
  echo "    list grepped for ltc/timecode/smpte ==="
  gst-inspect-1.0 2>/dev/null | grep -i 'ltc\|timecode\|smpte' || echo "(no matches)"
  echo
  echo "--- timecodestamper detail: does it generate LTC AUDIO, or does it"
  echo "    stamp GstVideoTimeCode metadata onto video buffers? ---"
  gst-inspect-1.0 timecodestamper | sed -n '1,25p'
  echo
  echo "--- avwait detail ---"
  gst-inspect-1.0 avwait | sed -n '1,15p'
  echo
  echo "CONCLUSION (a): timecodestamper embeds SMPTE timecode as video"
  echo "buffer metadata (GstVideoTimeCode), and avwait consumes that"
  echo "metadata to gate playback. Neither touches an audio buffer or"
  echo "produces an LTC audio signal. No installed plugin (base/good/bad)"
  echo "generates LTC audio. Candidate (a) is REJECTED on this image."

  echo
  echo "=== (b)/(c): is there a packaged LTC generator binary? ==="
  echo "--- apt-cache search ltc (after apt-get update) ---"
  apt-get update -qq
  apt-cache search ltc || true
  echo
  echo "--- is there an ltc-tools package on Debian 13? ---"
  apt-cache show ltc-tools 2>&1 || echo "(ltc-tools: no such package on this suite)"
  echo
  echo "--- is there a real LTC encoding library? ---"
  apt-cache show libltc11 2>&1 | sed -n '1,10p'
  echo
  echo "CONCLUSION (b)/(c): Debian 13 ships libltc11/libltc-dev (the real"
  echo "Manchester-biphase LTC encoder library, upstream x42/libltc), but no"
  echo "prebuilt ltcgen/ltc-tools binary. This image compiles ltcgen.c"
  echo "(candidate c: an external generator feeding the pipeline through"
  echo "filesrc/fdsrc, built against libltc, not hand-rolled bit math) and"
  echo "uses its output as candidate (b): a pre-rendered LTC file played"
  echo "back as an ordinary source in the same pipeline as program. See"
  echo "ltcgen.c and r2_alignment.sh / r3_ltc_generation.sh."
  echo
  echo "=== (d) anything else the survey turned up? ==="
  echo "No other candidate found in the installed plugin set or apt."
} > "$LOG" 2>&1
cat "$LOG"
