# shellcheck shell=bash
# Render node: the NDI GStreamer plugin and the operator-supplied NDI runtime.

NDI_SDK_URL="https://ndi.video/for-developers/ndi-sdk/download/"
NDI_SDK_FILE="Install_NDI_SDK_v6_Linux.tar.gz"
NDI_RUNTIME_DIR=/usr/local/lib

gst_plugin_dir() {
  printf '/usr/lib/%s/gstreamer-1.0' "$MULTIARCH"
}

ndi_plugin_installed() {
  [ -f "$(gst_plugin_dir)/libgstndi.so" ]
}

# render_install_ndi_plugin installs the packaged plugin; without one it defers to the NDI step.
render_install_ndi_plugin() {
  local packaged="$AGENT_PKG_DIR/gstreamer/libgstndi.so"
  step "Installing the NDI output plugin"
  if [ -f "$packaged" ]; then
    install -D -m 0644 -o root -g root "$packaged" "$(gst_plugin_dir)/libgstndi.so"
    ok "installed $(gst_plugin_dir)/libgstndi.so from the node agent package"
  elif ndi_plugin_installed; then
    ok "kept the plugin already at $(gst_plugin_dir)/libgstndi.so"
  else
    info "This release has no prebuilt NDI plugin for $ARCH. It is built on this machine when the NDI runtime is added."
  fi
}

# render_build_ndi_plugin compiles the plugin on this machine.
render_build_ndi_plugin() {
  local script="$BUNDLE_DIR/node/ndi-plugin/build-ndi-plugin.sh" out
  [ -x "$script" ] || fail "This installer has no NDI plugin build script." "download a complete installer for version $SHOWMESH_VERSION"
  info "Building the NDI plugin on this machine. This installs a compiler and takes 10 to 30 minutes."
  out="$(mktemp -d)"
  if ! with_default_umask "$script" "$out"; then
    fail "The NDI plugin did not build; the reason is printed above." "sudo $script $out"
  fi
  install -D -m 0644 -o root -g root "$out/libgstndi.so" "$(gst_plugin_dir)/libgstndi.so"
  rm -rf "$out"
  ok "built and installed $(gst_plugin_dir)/libgstndi.so"
}

ndi_explain() {
  cat <<EOF

    NDI output needs the NDI runtime library. The runtime is free, but it is
    licensed by Vizrt NDI AB, and ShowMesh is not allowed to ship it.
    Download the NDI SDK for Linux from:
      $NDI_SDK_URL
    The file is named like $NDI_SDK_FILE.
    Copy it to this machine, then give its path below.
    This node works without NDI; only NDI output is missing until you add it.

EOF
}

# ndi_find_lib DIR prints the libndi.so.6 file for this architecture under DIR.
ndi_find_lib() {
  local dir="$1" sub lib
  local candidates=(x86_64-linux-gnu)
  [ "$ARCH" = "arm64" ] && candidates=(aarch64-linux-gnu aarch64-rpi4-linux-gnueabi aarch64-newtek-linux-gnu)
  for sub in "${candidates[@]}"; do
    lib="$(find "$dir" -path "*/lib/$sub/libndi.so.6*" -type f 2>/dev/null | sort | tail -n 1)"
    [ -n "$lib" ] && { printf '%s' "$lib"; return; }
  done
  if [ "$ARCH" = "arm64" ]; then
    find "$dir" -path '*/lib/aarch64*/libndi.so.6*' -type f 2>/dev/null | sort | tail -n 1
  fi
}

# ndi_unpack_sdk FILE WORKDIR runs the SDK's own installer so the operator reads and accepts its licence.
ndi_unpack_sdk() {
  local file="$1" work="$2" script
  case "$file" in
    *.tar.gz|*.tgz) tar -xzf "$file" -C "$work" ;;
    *.sh) cp "$file" "$work/" ;;
    *) fail "$file is not the NDI SDK download." "showmesh-install --ndi /path/to/$NDI_SDK_FILE" ;;
  esac
  script="$(find "$work" -maxdepth 2 -name 'Install_NDI_SDK_*Linux*.sh' -type f | head -n 1)"
  [ -n "$script" ] || fail "$file does not contain the NDI SDK installer script." "showmesh-install --ndi /path/to/$NDI_SDK_FILE"
  if [ ! -t 0 ]; then
    fail "The NDI licence must be read and accepted at a terminal." "showmesh-install --ndi $file (from a terminal)"
  fi
  info "The NDI SDK installer shows its licence next. Read it and answer its question yourself."
  (cd "$(dirname "$script")" && with_default_umask bash "./$(basename "$script")") ||
    fail "The NDI SDK installer stopped, so its licence was not accepted." "showmesh-install --ndi $file"
}

# ndi_install_runtime FILE installs libndi.so.6 from an SDK download, its installer script or its unpacked directory.
ndi_install_runtime() {
  local file="$1" work lib base
  [ -e "$file" ] || fail "There is no file at $file." "showmesh-install --ndi /path/to/$NDI_SDK_FILE"
  if ! ndi_plugin_installed; then
    render_build_ndi_plugin
  fi
  step "Installing the NDI runtime from $file"
  work="$(mktemp -d)"
  if [ -d "$file" ]; then
    lib="$(ndi_find_lib "$file")"
  else
    ndi_unpack_sdk "$file" "$work"
    lib="$(ndi_find_lib "$work")"
  fi
  [ -n "$lib" ] || fail "The NDI SDK has no libndi.so.6 for $ARCH." "download the Linux SDK from $NDI_SDK_URL and run showmesh-install --ndi <file>"
  base="$(basename "$lib")"
  install -m 0644 -o root -g root "$lib" "$NDI_RUNTIME_DIR/$base"
  [ "$base" != "libndi.so.6" ] && ln -sf "$base" "$NDI_RUNTIME_DIR/libndi.so.6"
  ldconfig
  rm -rf "$work"
  ok "installed $NDI_RUNTIME_DIR/$base"
  if gst-inspect-1.0 ndisink >/dev/null 2>&1; then
    ok "GStreamer finds the ndisink element"
  else
    fail "GStreamer does not find the ndisink element after the NDI install." "gst-inspect-1.0 ndisink"
  fi
  if have_systemd && systemctl is-active --quiet showmesh-agent; then
    systemctl restart showmesh-agent
    ok "restarted the agent so it uses NDI"
  fi
}

ndi_skipped() {
  info "Skipped the NDI runtime. This node works without NDI output."
  info "To add it later, run: sudo showmesh-install --ndi /path/to/$NDI_SDK_FILE"
}

render_ndi_runtime() {
  step "NDI runtime"
  if [ -n "$OPT_NDI" ]; then
    ndi_install_runtime "$OPT_NDI"
    return
  fi
  if [ -f "$NDI_RUNTIME_DIR/libndi.so.6" ] && ndi_plugin_installed; then
    ok "kept the NDI runtime already installed at $NDI_RUNTIME_DIR/libndi.so.6"
    return
  fi
  if [ "$OPT_SKIP_NDI" -eq 1 ] || ! can_prompt; then
    ndi_skipped
    return
  fi
  ndi_explain
  local path
  ask path "Path to the downloaded NDI SDK file, or s to skip" "s"
  if [ "$path" = "s" ] || [ "$path" = "S" ]; then
    ndi_skipped
    return
  fi
  ndi_install_runtime "$path"
}

# run_ndi_only is showmesh-install --ndi FILE on an installed render node.
run_ndi_only() {
  [ -x /usr/local/bin/showmesh-agent-native ] ||
    fail "This machine has no ShowMesh node installed yet." "showmesh-install --role render"
  ndi_install_runtime "$OPT_NDI"
}
