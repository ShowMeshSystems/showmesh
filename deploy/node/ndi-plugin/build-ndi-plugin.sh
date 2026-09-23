#!/bin/sh
# Builds the GStreamer NDI plugin (net/ndi in gst-plugins-rs) from the
# pinned revision below and writes libgstndi.so into the output directory.
# Runs as root on a Debian 13 host (or a debian:trixie container) with
# nothing but apt and network access: the release workflow runs it to
# produce the plugin shipped in the node agent tarball, and the installer
# runs it as the fallback on an architecture with no prebuilt plugin.
set -eu

GST_PLUGINS_RS_TAG="gstreamer-1.26.2"
GST_PLUGINS_RS_REPO="https://gitlab.freedesktop.org/gstreamer/gst-plugins-rs.git"
RUST_TOOLCHAIN="1.98.1"

usage() {
	echo "usage: $0 <output-directory> [work-directory]" >&2
	exit 1
}

[ $# -ge 1 ] && [ $# -le 2 ] || usage
OUT_DIR=$1
mkdir -p "$OUT_DIR"
OUT_DIR=$(cd "$OUT_DIR" && pwd)

if [ $# -eq 2 ]; then
	WORK_DIR=$2
	mkdir -p "$WORK_DIR"
	CLEANUP_WORK_DIR=false
else
	WORK_DIR=$(mktemp -d)
	CLEANUP_WORK_DIR=true
fi
WORK_DIR=$(cd "$WORK_DIR" && pwd)
if [ "$CLEANUP_WORK_DIR" = "true" ]; then
	trap 'rm -rf "$WORK_DIR"' EXIT
fi

echo "build-ndi-plugin: building gst-plugins-rs $GST_PLUGINS_RS_TAG (net/ndi) into $OUT_DIR"

if ! command -v apt-get >/dev/null 2>&1; then
	echo "build-ndi-plugin: apt-get not found; this script only runs on a Debian host." >&2
	exit 1
fi

export DEBIAN_FRONTEND=noninteractive
apt-get update -o Acquire::Retries=2
apt-get install -y --no-install-recommends \
	ca-certificates curl git build-essential pkg-config \
	libgstreamer1.0-dev libgstreamer-plugins-base1.0-dev

if ! command -v cargo >/dev/null 2>&1; then
	echo "build-ndi-plugin: cargo not found; installing Rust $RUST_TOOLCHAIN with rustup."
	curl --proto '=https' --tlsv1.2 -fsSL https://sh.rustup.rs \
		| sh -s -- -y --profile minimal --default-toolchain "$RUST_TOOLCHAIN"
	# shellcheck disable=SC1091
	. "$HOME/.cargo/env"
fi

CLONE_DIR="$WORK_DIR/gst-plugins-rs"
if [ -d "$CLONE_DIR/.git" ]; then
	echo "build-ndi-plugin: reusing existing clone in $CLONE_DIR"
	git -C "$CLONE_DIR" fetch --depth 1 origin "tag" "$GST_PLUGINS_RS_TAG"
	git -C "$CLONE_DIR" checkout --detach FETCH_HEAD
else
	git clone --depth 1 --branch "$GST_PLUGINS_RS_TAG" "$GST_PLUGINS_RS_REPO" "$CLONE_DIR"
fi

(cd "$CLONE_DIR" && cargo build --release --locked -p gst-plugin-ndi)

BUILT_SO="$CLONE_DIR/target/release/libgstndi.so"
if [ ! -f "$BUILT_SO" ]; then
	echo "build-ndi-plugin: cargo build reported success but $BUILT_SO does not exist; refusing to report success." >&2
	exit 1
fi
install -m 0644 "$BUILT_SO" "$OUT_DIR/libgstndi.so"

if [ ! -f "$OUT_DIR/libgstndi.so" ]; then
	echo "build-ndi-plugin: install reported success but $OUT_DIR/libgstndi.so does not exist; refusing to report success." >&2
	exit 1
fi
echo "build-ndi-plugin: wrote $OUT_DIR/libgstndi.so"
