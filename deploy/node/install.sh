#!/usr/bin/env bash
# Idempotent installer for the ShowMesh native node agent. Safe to re-run:
# a second run upgrades the binary and unit in place and restarts the
# service, without touching /etc/showmesh/agent.env or any state under the
# asset directory (assignments.json, audio-sessions/*.json, asset payload
# files). Must be run as root (it creates a system user, writes to /etc
# and /var/lib, and manages a systemd unit).
#
# Usage: install.sh <path-to-showmesh-agent-native-binary>

set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"

SERVICE_USER=showmesh
SERVICE_GROUP=showmesh
ETC_DIR=/etc/showmesh
ENV_FILE="$ETC_DIR/agent.env"
STATE_DIR=/var/lib/showmesh
BIN_DEST=/usr/local/bin/showmesh-agent-native
UNIT_SRC="$SCRIPT_DIR/showmesh-agent.service"
UNIT_DEST=/etc/systemd/system/showmesh-agent.service
UNIT_NAME=showmesh-agent.service

if [ "$(id -u)" -ne 0 ]; then
  echo "install.sh: must be run as root" >&2
  exit 1
fi

if [ $# -ne 1 ]; then
  echo "usage: $0 <path-to-showmesh-agent-native-binary>" >&2
  exit 2
fi
BIN_SRC="$1"
if [ ! -f "$BIN_SRC" ]; then
  echo "install.sh: $BIN_SRC does not exist" >&2
  exit 1
fi

# --- Refuse plainly rather than half-install on an unsupported platform ---
if [ -r /etc/os-release ]; then
  # shellcheck disable=SC1091
  . /etc/os-release
  if [ "${ID:-}" = "debian" ]; then
    major="${VERSION_ID%%.*}"
    if [ -z "${major:-}" ] || ! [ "$major" -ge 13 ] 2>/dev/null; then
      echo "install.sh: refusing to install on Debian ${VERSION_ID:-unknown}. The ShowMesh agent's cgo build requires Debian 13 (trixie) or newer (measured: it does not build against Debian 12's GLib 2.74). Install onto a Debian 13+ host." >&2
      exit 1
    fi
  else
    echo "install.sh: WARNING: /etc/os-release reports ID=${ID:-unknown}, not debian. This installer has only been verified on Debian 13. Proceeding, but this platform is unverified: press Ctrl-C now to abort." >&2
  fi
else
  echo "install.sh: WARNING: /etc/os-release not found; cannot verify the Debian 13 floor. Proceeding, but this platform is unverified." >&2
fi

# install.sh always installs an already-built binary, so it checks only
# what the agent needs to RUN. A host that will build the agent itself
# runs ./preflight.sh with no arguments to get the build-time checks
# (C compiler, -dev packages, pkg-config) as well.
echo "install.sh: running runtime preflight checks..."
if ! "$SCRIPT_DIR/preflight.sh" --runtime-only; then
  echo "install.sh: preflight failed; refusing to install until the checks above pass." >&2
  exit 1
fi

# --- System user/group (idempotent) ---
if ! getent group "$SERVICE_GROUP" >/dev/null 2>&1; then
  echo "install.sh: creating group $SERVICE_GROUP"
  groupadd --system "$SERVICE_GROUP"
else
  echo "install.sh: group $SERVICE_GROUP already exists"
fi

if ! getent passwd "$SERVICE_USER" >/dev/null 2>&1; then
  echo "install.sh: creating user $SERVICE_USER"
  useradd --system --gid "$SERVICE_GROUP" --home-dir "$STATE_DIR" \
    --no-create-home --shell /usr/sbin/nologin \
    --comment "ShowMesh node agent" "$SERVICE_USER"
  # useradd needs the group audio for ALSA device access on most Debian
  # installs. Do this even for a freshly created user; not fatal if the
  # group doesn't exist (e.g. a container with no ALSA at all).
  usermod -aG audio "$SERVICE_USER" 2>/dev/null || \
    echo "install.sh: WARNING: could not add $SERVICE_USER to the 'audio' group (group may not exist on this host); ALSA device access may need manual attention."
else
  # A pre-existing "showmesh" account is only safe to adopt as the agent's
  # service account if it has the exact shape this installer creates: a
  # system uid, a nologin-equivalent shell, and its home field pointing at
  # STATE_DIR. Anything else (a human login account that happens to share
  # this name) must be refused, not silently run as: it would hand the
  # agent that human's uid, supplementary groups, and home directory.
  existing_uid="$(id -u "$SERVICE_USER")"
  existing_shell="$(getent passwd "$SERVICE_USER" | cut -d: -f7)"
  existing_home="$(getent passwd "$SERVICE_USER" | cut -d: -f6)"
  sys_uid_max=999
  if [ -r /etc/login.defs ]; then
    configured_max="$(awk '$1 == "SYS_UID_MAX" { print $2 }' /etc/login.defs)"
    if [ -n "$configured_max" ]; then
      sys_uid_max="$configured_max"
    fi
  fi
  shell_ok=0
  case "$existing_shell" in
    */nologin|*/false) shell_ok=1 ;;
  esac
  uid_ok=1
  [ "$existing_uid" -le "$sys_uid_max" ] || uid_ok=0
  home_ok=1
  [ "$existing_home" = "$STATE_DIR" ] || home_ok=0
  if [ "$uid_ok" -ne 1 ] || [ "$shell_ok" -ne 1 ] || [ "$home_ok" -ne 1 ]; then
    mismatches=""
    [ "$uid_ok" -eq 1 ] || mismatches="${mismatches}uid=$existing_uid (expected <= $sys_uid_max); "
    [ "$shell_ok" -eq 1 ] || mismatches="${mismatches}shell=$existing_shell (expected a nologin-equivalent shell); "
    [ "$home_ok" -eq 1 ] || mismatches="${mismatches}home=$existing_home (expected $STATE_DIR); "
    echo "install.sh: refusing to adopt existing account '$SERVICE_USER' (uid=$existing_uid, shell=$existing_shell, home=$existing_home) as the agent's service account: it does not look like the locked-down system account this installer creates (expected uid<=$sys_uid_max, a nologin shell, and home=$STATE_DIR). Mismatched: $mismatches This is either a different account that happens to share this name, or this installer's own account modified since it was created (for example by a manual usermod). If it is a different account: rename or remove it, or edit SERVICE_USER/SERVICE_GROUP in $SCRIPT_DIR/install.sh to use a different service account name, then re-run. If it is this installer's own account that was modified: restore the expected shell and home instead of removing the account (recreating it would leave any state file directly under $STATE_DIR, outside assets/, owned by an orphaned uid, since only $STATE_DIR/assets is chowned recursively) -- for example: usermod --shell /usr/sbin/nologin --home $STATE_DIR $SERVICE_USER" >&2
    exit 1
  fi
  echo "install.sh: user $SERVICE_USER already exists (system account, matches the shape this installer creates)"
fi

# --- /etc/showmesh and the env file (never overwrite an existing one) ---
mkdir -p "$ETC_DIR"
chmod 0755 "$ETC_DIR"

if [ -f "$ENV_FILE" ]; then
  echo "install.sh: $ENV_FILE already exists; leaving it untouched"
else
  echo "install.sh: writing $ENV_FILE from the template (edit it before first start)"
  install -m 0600 -o root -g root "$SCRIPT_DIR/agent.env.example" "$ENV_FILE"
fi

# --- State directory, owned by the service user; never wiped ---
mkdir -p "$STATE_DIR"
chown "$SERVICE_USER:$SERVICE_GROUP" "$STATE_DIR"
chmod 0750 "$STATE_DIR"
mkdir -p "$STATE_DIR/assets"
chown -R "$SERVICE_USER:$SERVICE_GROUP" "$STATE_DIR/assets"

# --- Binary: install to a temp name, then atomically replace ---
UPGRADE=0
if [ -f "$BIN_DEST" ]; then
  UPGRADE=1
fi
install -m 0755 -o root -g root "$BIN_SRC" "$BIN_DEST.new"
mv -f "$BIN_DEST.new" "$BIN_DEST"

# --- systemd unit ---
install -m 0644 -o root -g root "$UNIT_SRC" "$UNIT_DEST"

# A real node host runs systemd as PID 1; a container used only to prove
# this script's file/user/permission behavior (bench/node-install) does
# not. Detect that up front rather than letting `set -e` abort the whole
# install partway through on the first systemctl call: the unit file is
# still installed either way, only its activation is skipped, with a
# clear warning naming exactly what was not done.
SYSTEMD_AVAILABLE=1
if ! systemctl daemon-reload 2>/tmp/showmesh-install-systemctl-err; then
  SYSTEMD_AVAILABLE=0
  echo "install.sh: WARNING: systemctl daemon-reload failed ($(tr -d '\n' < /tmp/showmesh-install-systemctl-err)). This host is not running systemd as PID 1 (expected inside a plain container; not expected on a real node). The unit file is installed at $UNIT_DEST but NOT enabled or started. Run 'systemctl daemon-reload && systemctl enable --now $UNIT_NAME' once this host is running under systemd." >&2
  rm -f /tmp/showmesh-install-systemctl-err
fi

if [ "$SYSTEMD_AVAILABLE" -eq 1 ]; then
  systemctl enable "$UNIT_NAME" >/dev/null
  if [ "$UPGRADE" -eq 1 ]; then
    echo "install.sh: existing binary found; upgrading in place (state directory contents are not touched)"
    ACTIVATE_VERB="restart"
  else
    echo "install.sh: fresh install"
    ACTIVATE_VERB="start"
  fi
  # Enforce the SHOWMESH_NODE_ID check on both paths: an upgrade with an
  # unedited agent.env must not (re)start the agent any more than a fresh
  # install would, or the agent falls back to the hostname as its node id
  # and crash-loops against the broker.
  if [ -s "$ENV_FILE" ] && grep -q '^SHOWMESH_NODE_ID=.\+' "$ENV_FILE" 2>/dev/null; then
    # The FPP Connect HTTP listener binds unconditionally on every node
    # (see showmesh-agent.service's CAP_NET_BIND_SERVICE grant), so any
    # node can be an xLights upload target regardless of whether an
    # operator ever intended it to be. Registering an upload is a WRITE
    # (asset:write), unrelated to the coordinator's own read policy: a
    # missing SHOWMESH_AGENT_API_TOKEN here means every upload this node
    # ever receives will accept, assemble, and hash correctly, then fail
    # to register, permanently, with nothing visible until an operator
    # goes looking. Warned here, not refused: a node with no upload
    # credential still serves reads, renders assigned content, and plays
    # audio correctly, and this install must not turn that gap into a
    # stopped agent.
    if ! grep -q '^SHOWMESH_AGENT_API_TOKEN=.\+' "$ENV_FILE" 2>/dev/null; then
      echo "install.sh: WARNING: $ENV_FILE has no SHOWMESH_AGENT_API_TOKEN set. This node's FPP Connect HTTP listener accepts uploads unconditionally, but every uploaded sequence will fail to register, permanently, with no visible error at upload time. Provision a machine principal and an admin-role token from the coordinator (showmeshctl principal create ...; showmeshctl token issue <principalId>), set SHOWMESH_AGENT_API_TOKEN in $ENV_FILE, then: systemctl restart $UNIT_NAME" >&2
    fi
    systemctl "$ACTIVATE_VERB" "$UNIT_NAME"
  else
    echo "install.sh: $ENV_FILE has no SHOWMESH_NODE_ID set yet. Edit it (at minimum SHOWMESH_NODE_ID, SHOWMESH_MQTT_BROKER, SHOWMESH_MQTT_USERNAME, SHOWMESH_MQTT_PASSWORD), then run: systemctl $ACTIVATE_VERB $UNIT_NAME"
  fi
  echo "install.sh: done. Check status with: systemctl status $UNIT_NAME"
else
  echo "install.sh: done (unit installed but not activated; see the systemd warning above)."
fi
