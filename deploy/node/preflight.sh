#!/usr/bin/env bash
# Verifies a node host has what the ShowMesh native agent needs before
# install.sh installs it, and again on every restart-check. Exits non-zero
# and names exactly what is missing plus the apt package that provides it.
# Never writes anything; read-only checks only.
#
# Usage: preflight.sh [--runtime-only]
#   --runtime-only  skip build-time-only checks (dev headers, pkg-config
#                   for ltc); use this on a node where the binary was
#                   built elsewhere and only needs to RUN.

set -u

RUNTIME_ONLY=0
for arg in "$@"; do
  case "$arg" in
    --runtime-only) RUNTIME_ONLY=1 ;;
    *) echo "preflight.sh: unknown argument: $arg" >&2; exit 2 ;;
  esac
done

FAILURES=0
fail() {
  echo "MISSING: $1" >&2
  echo "  install with: $2" >&2
  FAILURES=$((FAILURES + 1))
}

info() {
  echo "INFO: $1"
}

ok() {
  echo "OK: $1"
}

# --- Platform floor: Debian 13 (trixie) or newer ---
# Measured fact (not asserted here as opinion): the agent does not build
# on Debian 12 because GLib 2.74 is missing symbols go-gst/go-glib need
# (2.80+). This check reads /etc/os-release rather than /etc/debian_version
# because it needs VERSION_ID, which is what actually orders "or newer".
if [ -r /etc/os-release ]; then
  # shellcheck disable=SC1091
  . /etc/os-release
  if [ "${ID:-}" = "debian" ]; then
    major="${VERSION_ID%%.*}"
    if [ -n "${major:-}" ] && [ "$major" -ge 13 ] 2>/dev/null; then
      ok "platform: Debian $VERSION_ID (>= 13)"
    else
      fail "platform: Debian ${VERSION_ID:-unknown} (need Debian 13 or newer; the agent's cgo build does not build against Debian 12's GLib 2.74)" \
        "install onto a Debian 13 (trixie) or newer host; there is no supported workaround on Debian 12"
    fi
  else
    info "platform: ID=${ID:-unknown}, not Debian; this preflight only asserts the Debian 13 floor this project has actually measured. Proceeding with the remaining checks, but the platform itself is unverified."
  fi
else
  info "platform: /etc/os-release not found; cannot verify the Debian 13 floor. Proceeding with the remaining checks."
fi

# --- pkg-config-based version floors (build-time) ---
if [ "$RUNTIME_ONLY" -eq 0 ]; then
  if command -v pkg-config >/dev/null 2>&1; then
    if pkg-config --atleast-version=1.26.0 gstreamer-1.0 2>/dev/null; then
      ok "GStreamer $(pkg-config --modversion gstreamer-1.0 2>/dev/null) (>= 1.26)"
    else
      fail "GStreamer development package (need >= 1.26)" \
        "apt-get install -y libgstreamer1.0-dev libgstreamer-plugins-base1.0-dev"
    fi

    if pkg-config --atleast-version=2.80 glib-2.0 2>/dev/null; then
      ok "GLib $(pkg-config --modversion glib-2.0 2>/dev/null) (>= 2.80)"
    else
      fail "GLib development package (need >= 2.80; go-gst/go-glib call symbols added through 2.80)" \
        "apt-get install -y libgstreamer1.0-dev libgstreamer-plugins-base1.0-dev"
    fi

    if ltc_version=$(pkg-config --modversion ltc 2>/dev/null); then
      ok "libltc $ltc_version (pkg-config)"
    else
      fail "libltc development package (libltc-dev, needed to build the agent)" \
        "apt-get install -y libltc-dev"
    fi
  else
    fail "pkg-config" "apt-get install -y pkg-config"
  fi

  for tool in gcc cc; do
    if command -v "$tool" >/dev/null 2>&1; then
      ok "C compiler ($tool)"
      break
    fi
  done
  if ! command -v gcc >/dev/null 2>&1 && ! command -v cc >/dev/null 2>&1; then
    fail "C compiler (gcc or cc, needed to build the agent's cgo backend)" \
      "apt-get install -y build-essential"
  fi
else
  # Runtime-only: libltc.so.11 itself must still be resolvable, even
  # without libltc-dev's pkg-config file. ldconfig lives in /usr/sbin,
  # which is not on an unprivileged user's PATH on Debian 13, so look
  # there directly rather than reporting the library missing.
  LDCONFIG=""
  if command -v ldconfig >/dev/null 2>&1; then
    LDCONFIG=ldconfig
  elif [ -x /usr/sbin/ldconfig ]; then
    LDCONFIG=/usr/sbin/ldconfig
  elif [ -x /sbin/ldconfig ]; then
    LDCONFIG=/sbin/ldconfig
  fi
  if [ -n "$LDCONFIG" ] && "$LDCONFIG" -p 2>/dev/null | grep -q 'libltc\.so\.11'; then
    ok "libltc.so.11 (runtime library present)"
  else
    fail "libltc.so.11 (runtime library the agent links against)" \
      "apt-get install -y libltc11"
  fi
fi

# --- Runtime executables ---
check_bin() {
  local bin="$1" pkg="$2"
  if command -v "$bin" >/dev/null 2>&1; then
    ok "$bin on PATH"
  else
    fail "$bin executable" "apt-get install -y $pkg"
  fi
}

check_bin gst-launch-1.0 gstreamer1.0-tools
check_bin gst-discoverer-1.0 gstreamer1.0-plugins-base-apps
check_bin aplay alsa-utils

# --- GStreamer elements the agent actually instantiates ---
# Matches the element list CI itself asserts (.github/workflows/ci.yml).
# ndisink is intentionally checked separately below, as informational: it
# is not part of this list, per ADR-010 (the NDI runtime and the
# gst-plugins-rs NDI element are never shipped by this repo).
if command -v gst-inspect-1.0 >/dev/null 2>&1; then
  declare -A element_pkg=(
    [rawvideoparse]="gstreamer1.0-plugins-bad"
    [videotestsrc]="gstreamer1.0-plugins-base"
    [wavparse]="gstreamer1.0-plugins-good"
    [flacdec]="gstreamer1.0-plugins-good"
    [audiomixer]="gstreamer1.0-plugins-bad"
    [interleave]="gstreamer1.0-plugins-base"
    [alsasink]="gstreamer1.0-alsa"
  )
  for element in rawvideoparse videotestsrc wavparse flacdec audiomixer interleave alsasink; do
    if gst-inspect-1.0 "$element" >/dev/null 2>&1; then
      ok "GStreamer element: $element"
    else
      fail "GStreamer element: $element" "apt-get install -y ${element_pkg[$element]}"
    fi
  done

  # NDI output element: informational only. A render node needs it; an
  # audio-only node does not, and this repo never builds or ships it (the
  # gst-plugins-rs NDI element is SM-9's separately owned build recipe;
  # see deploy/node/README.md).
  if gst-inspect-1.0 ndisink >/dev/null 2>&1; then
    info "ndisink resolves: this host can drive NDI output. (Not verified against real hardware or a real NDI receiver by this script.)"
  else
    info "ndisink does NOT resolve. This is expected on a fresh install and on any audio-only node. A render node that needs NDI output must build the gst-plugins-rs NDI element separately (see deploy/node/README.md) and set GST_PLUGIN_PATH in /etc/showmesh/agent.env, then re-run this preflight."
  fi
else
  fail "gst-inspect-1.0 (needed to check GStreamer elements)" "apt-get install -y gstreamer1.0-tools"
fi

echo
if [ "$FAILURES" -gt 0 ]; then
  echo "preflight.sh: $FAILURES check(s) failed." >&2
  exit 1
fi
echo "preflight.sh: all checks passed."
exit 0
