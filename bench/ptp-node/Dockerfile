# Debian 13 base for proving install-ptp-audio.sh / verify-ptp-audio.sh.
# install-ptp-audio.sh itself installs linuxptp/pipewire/wireplumber/
# gstreamer1.0-pipewire; this image only adds what the bench harness needs
# around that (Python for pw-dump parsing, basic net tools, gstreamer
# runtime/tools for the playback check, sudo for running steps as the
# unprivileged service accounts install-ptp-audio.sh creates).
FROM debian:trixie

RUN apt-get update && apt-get install -y --no-install-recommends \
    python3 iproute2 procps sudo \
    gstreamer1.0-tools gstreamer1.0-plugins-base gstreamer1.0-plugins-good \
  && rm -rf /var/lib/apt/lists/*

WORKDIR /repo
CMD ["/bin/bash"]
