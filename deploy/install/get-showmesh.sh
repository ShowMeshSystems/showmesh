#!/usr/bin/env bash
# Downloads, checks and runs the ShowMesh installer for one release:
#   curl -fsSL https://github.com/ShowMeshSystems/showmesh/releases/download/v<version>/get-showmesh.sh | sudo bash
# Every argument after "bash -s --" is passed to showmesh-install.
set -euo pipefail

SHOWMESH_VERSION="@SHOWMESH_VERSION@"
RELEASE_BASE="${SHOWMESH_RELEASE_BASE:-https://github.com/ShowMeshSystems/showmesh/releases/download/v$SHOWMESH_VERSION}"
BUNDLE="showmesh-installer_${SHOWMESH_VERSION}.tar.gz"
INSTALL_ROOT=/opt/showmesh/installer

stop() {
  printf '\nShowMesh install stopped. %s\n' "$1" >&2
  [ -n "${2:-}" ] && printf 'To fix it, run: %s\n' "$2" >&2
  exit 1
}

if [ "$(id -u)" -ne 0 ]; then
  stop "The installer must run as root." "curl -fsSL $RELEASE_BASE/get-showmesh.sh | sudo bash"
fi
for tool in curl sha256sum tar; do
  command -v "$tool" >/dev/null 2>&1 || stop "This machine has no $tool command." "apt-get install -y curl coreutils tar"
done

work="$(mktemp -d)"
trap 'rm -rf "$work"' EXIT

printf 'Downloading the ShowMesh %s installer.\n' "$SHOWMESH_VERSION"
curl -fsSL --retry 3 -o "$work/SHA256SUMS" "$RELEASE_BASE/SHA256SUMS" ||
  stop "The release's SHA256SUMS could not be downloaded from $RELEASE_BASE." "curl -fsSLO $RELEASE_BASE/SHA256SUMS"
curl -fsSL --retry 3 -o "$work/$BUNDLE" "$RELEASE_BASE/$BUNDLE" ||
  stop "The installer $BUNDLE could not be downloaded from $RELEASE_BASE." "curl -fsSLO $RELEASE_BASE/$BUNDLE"

want="$(awk -v n="$BUNDLE" '$2 == n || $2 == "*" n {print $1}' "$work/SHA256SUMS" | head -n 1)"
[ -n "$want" ] || stop "The release's SHA256SUMS does not list $BUNDLE, so it cannot be checked." "check that version $SHOWMESH_VERSION is a complete release"
got="$(sha256sum "$work/$BUNDLE" | awk '{print $1}')"
[ "$want" = "$got" ] || stop "The downloaded installer does not match the release's checksum." "run the install command again; if it repeats, the download is being altered on its way here"
printf 'The installer matches the release checksum.\n'

dest="$INSTALL_ROOT/$SHOWMESH_VERSION"
mkdir -p "$INSTALL_ROOT"
rm -rf "$dest.new"
mkdir -p "$dest.new"
tar -xzf "$work/$BUNDLE" -C "$dest.new" --strip-components=1
rm -rf "$dest"
mv "$dest.new" "$dest"
ln -sfn "$dest/showmesh-install" /usr/local/bin/showmesh-install

export SHOWMESH_RELEASE_BASE="$RELEASE_BASE"
# stdin is the curl pipe, so questions go to the terminal when there is one.
if { : </dev/tty; } 2>/dev/null; then
  exec "$dest/showmesh-install" "$@" </dev/tty
fi
exec "$dest/showmesh-install" "$@" </dev/null
