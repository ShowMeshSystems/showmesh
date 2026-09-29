# The NDI GStreamer plugin build

`build-ndi-plugin.sh` builds `libgstndi.so`, the `ndisink`/`ndisrc`
GStreamer plugin, from `gst-plugins-rs`. It ships no NDI SDK code: it
`dlopen`s the NDI runtime at run time, so building and shipping it does
not redistribute the runtime (ADR-010, RES-006).

## The pinned revision

`gst-plugins-rs` tag `gstreamer-1.26.2`.

Debian 13 (trixie) packages GStreamer 1.26.2 (`apt-cache policy
libgstreamer1.0-dev`, checked inside `debian:trixie`, 2026-09-23). Each
`gst-plugins-rs` release tag is named for the GStreamer version it targets,
so this tag is the exact match rather than a newer tag whose minimum
GStreamer version trixie's package would not satisfy.

## What the script does

Run as root with only `apt`, `git`, and network access, on a Debian 13
host or inside a `debian:trixie` container:

```sh
./build-ndi-plugin.sh <output-directory>
```

It installs the GStreamer dev headers and a build toolchain via `apt`,
installs a pinned Rust toolchain with `rustup` if `cargo` is not already
on `PATH`, clones the pinned tag, builds only the `net/ndi` crate in
release mode, and writes `libgstndi.so` into `<output-directory>`.

The release workflow (`.github/workflows/release.yml`) runs this once per
architecture and ships the result in the node agent tarball
(`make package-node-agent NDI_PLUGIN_SO=<path>`). The installer runs the
same script as its fallback on an architecture with no prebuilt plugin.

An optional second argument, `<work-directory>`, reuses an existing clone
and `cargo` target directory instead of a fresh temporary one, which is
how CI caches the build across runs; the fallback path omits it and
cleans up after itself.

## Updating the revision

1. Check trixie's current GStreamer version:
   `docker run --rm debian:trixie sh -c "apt-get update -qq && apt-cache policy libgstreamer1.0-dev"`.
2. Pick the `gst-plugins-rs` tag named for that version (or the closest
   released tag whose `rust-version` and GStreamer minimum trixie's
   package satisfies): `git ls-remote --tags
   https://gitlab.freedesktop.org/gstreamer/gst-plugins-rs.git`.
3. Update `GST_PLUGINS_RS_TAG` in `build-ndi-plugin.sh` and, if the new
   tag raises the `rust-version` in `net/ndi/Cargo.toml` (or the
   workspace `Cargo.toml`), update `RUST_TOOLCHAIN` to a stable release
   at or above it.
4. Rebuild for both architectures and re-run the acceptance checks this
   plugin's build PR recorded.
