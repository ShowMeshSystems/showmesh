# Bench-only image for SM-43's install-flow proof. See README.md. Not part
# of the ShowMesh product, not imported by internal/ or pkg/, exactly like
# bench/audio-node.
#
# Debian 13 (trixie): the platform floor deploy/node/install.sh and
# preflight.sh assert, and the only platform the cgo agent build has been
# measured to work on.
FROM debian:13-slim

RUN apt-get update && apt-get install -y --no-install-recommends \
    ca-certificates curl git \
    build-essential pkg-config \
    libgstreamer1.0-dev libgstreamer-plugins-base1.0-dev libltc-dev \
    alsa-utils gstreamer1.0-tools \
    gstreamer1.0-plugins-base gstreamer1.0-plugins-good gstreamer1.0-plugins-bad \
    gstreamer1.0-plugins-base-apps gstreamer1.0-alsa libltc11 \
    # systemd-analyze is the only piece of systemd this image needs: it
    # validates a unit file's syntax and directive names without systemd
    # running as PID 1. This container never runs systemd itself (a plain
    # `docker run` is not a systemd machine); see README.md for exactly
    # what that does and does not prove.
    systemd \
    && rm -rf /var/lib/apt/lists/*

# Go toolchain, pinned to go.mod's own `go 1.25.0` directive rather than
# whatever golang-go happens to package on trixie.
#
# BuildKit sets TARGETARCH from the platform actually being built, so the
# toolchain matches the image on an amd64 host and an arm64 host alike. A
# hardcoded default would silently install the wrong toolchain on whichever
# architecture it did not name. `go version` runs here so a mismatch fails
# the build instead of surfacing as an exec-format error mid-bench.
ARG TARGETARCH
ARG GOARCH=${TARGETARCH}
RUN test -n "${GOARCH}" || { echo "GOARCH is empty: BuildKit did not set TARGETARCH. Pass --build-arg GOARCH=amd64|arm64." >&2; exit 1; } \
    && curl -fsSL "https://go.dev/dl/go1.25.0.linux-${GOARCH}.tar.gz" -o /tmp/go.tar.gz \
    && tar -C /usr/local -xzf /tmp/go.tar.gz \
    && rm /tmp/go.tar.gz \
    && /usr/local/go/bin/go version
ENV PATH="/usr/local/go/bin:${PATH}"

WORKDIR /work
ENTRYPOINT ["/bin/bash"]
