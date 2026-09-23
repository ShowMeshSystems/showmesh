#!/usr/bin/env bash
# Builds two fake releases, 0.0.0-bench1 and 0.0.0-bench2, into OUT_DIR, each laid out
# as a GitHub release: the node agent tarball, the installer bundle, get-showmesh.sh and one SHA256SUMS.
# bench1's node tarball carries a stand-in libgstndi.so; bench2's has none.
set -euo pipefail

OUT_DIR="${1:?usage: build_installer_release.sh OUT_DIR}"
cd /repo
export GOFLAGS=-buildvcs=false
arch="$(go env GOARCH)"

for v in 0.0.0-bench1 0.0.0-bench2; do
  rel="$OUT_DIR/$v"
  rm -rf "$rel" /tmp/na /tmp/inst
  mkdir -p "$rel"
  make --no-print-directory package-node-agent NODE_AGENT_VERSION="$v" NODE_AGENT_DIST=/tmp/na >/dev/null
  make --no-print-directory package-installer INSTALLER_VERSION="$v" INSTALLER_DIST=/tmp/inst >/dev/null
  tarball="showmesh-node-agent_${v}_linux_${arch}.tar.gz"
  if [ "$v" = "0.0.0-bench1" ]; then
    work="$(mktemp -d)"
    tar -xzf "/tmp/na/$tarball" -C "$work"
    mkdir -p "$work/showmesh-node-agent/gstreamer"
    printf 'int showmesh_bench_stub(void) { return 0; }\n' > "$work/stub.c"
    gcc -shared -fPIC -o "$work/showmesh-node-agent/gstreamer/libgstndi.so" "$work/stub.c"
    tar -czf "$rel/$tarball" -C "$work" showmesh-node-agent
    rm -rf "$work"
  else
    cp "/tmp/na/$tarball" "$rel/"
  fi
  cp "/tmp/inst/showmesh-installer_${v}.tar.gz" /tmp/inst/get-showmesh.sh "$rel/"
  (cd "$rel" && sha256sum ./*.tar.gz get-showmesh.sh | sed 's#  \./#  #' > SHA256SUMS)
  echo "built release $v in $rel:"
  sed 's/^/  /' "$rel/SHA256SUMS"
done
