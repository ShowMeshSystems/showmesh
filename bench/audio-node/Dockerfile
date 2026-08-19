# Bench-only image for Track C seam C0a-1. See README.md.
#
# Debian 13 (trixie) for a current GStreamer 1.24+ stack over Debian 12's
# older packages, since the LTC survey (R3) depends on whatever plugin set
# actually ships.
FROM debian:13-slim

RUN apt-get update && apt-get install -y --no-install-recommends \
    # gst-launch-1.0, gst-inspect-1.0: build and introspect every run's
    # pipeline from the command line, per the spec's "no Go sample path".
    gstreamer1.0-tools \
    # core elements: interleave, audiomixmatrix, capsfilter, audioconvert,
    # audioresample, wavenc/wavparse, filesink/filesrc.
    gstreamer1.0-plugins-base \
    # volume (R4 ducking), audiotestsrc extras, level, and the wav tooling
    # R1/R2/R5's pipelines build on.
    gstreamer1.0-plugins-good \
    # audiomixmatrix lives here in some GStreamer versions; also carries
    # candidate LTC-adjacent elements (timecodestamper) surveyed by R3.
    gstreamer1.0-plugins-bad \
    # ALSA plugin: gives R6 a real ALSA element (alsasink) to run against
    # a userspace null device inside the container.
    gstreamer1.0-alsa \
    # gst-discoverer-1.0: Track C seam C2's primary media-metadata source
    # (duration, codec, channels, rate from container metadata, MEASURED at
    # 11 ms against a 10-minute FLAC — orders of magnitude cheaper than a
    # full decode).
    gstreamer1.0-plugins-base-apps \
    libasound2t64 \
    alsa-utils \
    # R4's click-free ramp is driven by the GstController API from Python
    # via GObject Introspection, not by a Go or Python sample loop -- the
    # samples are still produced and mixed entirely inside GStreamer.
    python3 \
    python3-gi \
    gir1.2-gstreamer-1.0 \
    gir1.2-gst-plugins-base-1.0 \
    && rm -rf /var/lib/apt/lists/*

# R3's survey (see results/r3-ltc-survey.json) found no native GStreamer LTC
# element and no packaged ltcgen/ltc-tools binary on Debian 13 -- only the
# library. libltc-dev/build-essential compile the tiny external generator
# in ltcgen.c against the real encoder (candidate (c): "an external
# generator ... feeding the pipeline through fdsrc/filesrc"); only the
# runtime .so (libltc11) is needed after that, so the dev/build packages
# are removed in the same layer.
COPY ltcgen.c /tmp/ltcgen.c
RUN apt-get update && apt-get install -y --no-install-recommends \
    libltc11 libltc-dev build-essential \
    && gcc -O2 -o /usr/local/bin/ltcgen /tmp/ltcgen.c -lltc \
    && apt-get purge -y --auto-remove libltc-dev build-essential \
    && rm -rf /var/lib/apt/lists/* /tmp/ltcgen.c

# ASOUND_CONF selects a userspace null PCM device so R6 and every other
# run's ALSA-path elements never need a host sound device or /dev/snd.
COPY asound.conf /etc/asound.conf

WORKDIR /work

ENTRYPOINT ["/bin/bash"]
