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

# --- Runtime library version floor, via the actual binary ---
# preflight.sh's version floors on GStreamer 1.26 and GLib 2.80 are only
# ever checked with pkg-config against the -dev packages, which is the
# build-time branch this installer never runs (it installs a prebuilt
# binary; requiring a compiler toolchain to install one would be wrong).
# On Debian this floor is covered transitively (the Debian 13 check above
# implies trixie's GStreamer/GLib versions), but on any other platform
# nothing here has ever checked that the runtime libraries the binary
# actually links are new enough, only that libltc.so.11 is present.
# Plain `ldd` only resolves sonames and reports a library it cannot find
# at all; a library that is present but too old, same soname, missing a
# symbol the binary needs, resolves cleanly under plain ldd and only fails
# later at load. `ldd -r` asks the dynamic linker to also resolve data and
# function relocations, so it reports "undefined symbol" for exactly that
# case (verified: a stub library built with one needed symbol removed
# resolves cleanly under plain ldd but ldd -r reports it). Optional, like
# the rest of this installer's checks that depend on a tool that might not
# be present: if ldd itself is missing, warn and proceed rather than refuse.
if command -v ldd >/dev/null 2>&1; then
  LDD_OUT="$(ldd -r "$BIN_SRC" 2>&1 || true)"
  if echo "$LDD_OUT" | grep -qE 'not found|undefined symbol'; then
    echo "install.sh: refusing to install: $BIN_SRC depends on shared libraries this host cannot resolve, or resolves against a library missing a symbol it needs (a too-old or missing runtime library fails loudly, at load, rather than misbehaving quietly):" >&2
    echo "$LDD_OUT" | grep -E 'not found|undefined symbol' >&2
    echo "install.sh: install the runtime packages named in deploy/node/README.md and re-run." >&2
    exit 1
  fi
  echo "install.sh: OK: every shared library $BIN_SRC links resolves on this host, symbols included"
else
  echo "install.sh: WARNING: ldd not found; cannot verify the binary's runtime libraries resolve. Proceeding without this check." >&2
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
  # A pre-existing "showmesh" account is adopted, not refused outright: an
  # ordinary login shell and an ordinary home directory hand the agent
  # nothing it doesn't already get from being the systemd unit's User=, so
  # neither is checked here. Two things stay a hard refusal regardless of
  # intent because they are unsafe no matter who the account belongs to:
  # uid 0, and a home directory this install's own chown/chmod of
  # STATE_DIR would damage. Short of those, adoption is confirmed (this
  # host's installed unit already runs the agent as this account, so there
  # is no name collision left to guard against) or requires an explicit
  # operator opt-in, never a silent guess.
  existing_uid="$(id -u "$SERVICE_USER")"
  existing_shell="$(getent passwd "$SERVICE_USER" | cut -d: -f7)"
  existing_home="$(getent passwd "$SERVICE_USER" | cut -d: -f6)"

  if [ "$existing_uid" -eq 0 ]; then
    echo "install.sh: refusing to adopt existing account '$SERVICE_USER' (uid=0) as the agent's service account: this installer will not run the agent as root. Use a different account name: edit SERVICE_USER in $SCRIPT_DIR/install.sh, then re-run." >&2
    exit 1
  fi

  already_adopted=0
  if [ -f "$UNIT_DEST" ] && grep -qE "^User=$SERVICE_USER\$" "$UNIT_DEST" 2>/dev/null; then
    already_adopted=1
  fi

  if [ "$already_adopted" -eq 1 ]; then
    echo "install.sh: user $SERVICE_USER already exists and is already this host's agent service account (per $UNIT_DEST); adopting it as-is (uid=$existing_uid, shell=$existing_shell, home=$existing_home)"
  else
    # A home directory equal to STATE_DIR means one of two different
    # things, and they must not be told apart by the unit file alone: it
    # is either this installer's own account (safe to adopt) or a human
    # account that happens to collide with STATE_DIR (unsafe). Recognise
    # the installer's own shape first -- system uid, nologin-equivalent
    # shell, and home == STATE_DIR, all three together -- the same test
    # main uses to accept a pre-existing "showmesh" account outright. uid
    # and shell are a positive-recognition rule for that one combination,
    # never a general requirement: an ordinary login account whose home
    # sits somewhere else is still adopted below with no such test.
    sys_uid_max=999
    if [ -r /etc/login.defs ]; then
      configured_max="$(awk '$1 == "SYS_UID_MAX" { print $2 }' /etc/login.defs)"
      if [ -n "$configured_max" ]; then
        sys_uid_max="$configured_max"
      fi
    fi
    shell_is_nologin=0
    case "$existing_shell" in
      */nologin|*/false) shell_is_nologin=1 ;;
    esac
    installer_own_shape=0
    if [ "$existing_uid" -le "$sys_uid_max" ] && [ "$shell_is_nologin" -eq 1 ] \
      && [ "$existing_home" = "$STATE_DIR" ]; then
      installer_own_shape=1
    fi

    # STATE_DIR is chowned and chmod 0750'd by this script below. An
    # account whose home directory equals STATE_DIR, or whose home is an
    # ancestor directory of it, would have that install step take
    # ownership of part of the account's real home tree rather than of the
    # agent's own state directory -- unless that account is this
    # installer's own, in which case STATE_DIR is exactly what its home is
    # supposed to be.
    home_hazard=0
    if [ "$installer_own_shape" -ne 1 ]; then
      case "$STATE_DIR" in
        "$existing_home"|"$existing_home"/*) home_hazard=1 ;;
      esac
    fi
    if [ "$installer_own_shape" -eq 1 ]; then
      echo "install.sh: user $SERVICE_USER already exists (system account, matches the shape this installer creates: uid=$existing_uid, shell=$existing_shell, home=$existing_home); adopting it"
    elif [ "$home_hazard" -eq 1 ]; then
      echo "install.sh: refusing to adopt existing account '$SERVICE_USER' (home=$existing_home) as the agent's service account: installing chowns and chmod 0750s $STATE_DIR, which is that account's home directory or an ancestor of it. Use a different account name: edit SERVICE_USER in $SCRIPT_DIR/install.sh, then re-run." >&2
      exit 1
    elif [ "${SHOWMESH_ADOPT_EXISTING_ACCOUNT:-}" = "1" ]; then
      echo "install.sh: SHOWMESH_ADOPT_EXISTING_ACCOUNT=1 set; adopting existing account '$SERVICE_USER' (uid=$existing_uid, shell=$existing_shell, home=$existing_home) as the agent's service account"
    else
      echo "install.sh: refusing to adopt existing account '$SERVICE_USER' (uid=$existing_uid, shell=$existing_shell, home=$existing_home) as the agent's service account: this is a fresh install (no systemd unit on this host currently names it as the agent's account), so it may be an unrelated account that happens to share this name. If it should run the agent, re-run with SHOWMESH_ADOPT_EXISTING_ACCOUNT=1 set. If it should not, edit SERVICE_USER in $SCRIPT_DIR/install.sh to use a different account name, then re-run." >&2
      exit 1
    fi
  fi
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
# Templated rather than copied verbatim: the unit must run the agent as
# whatever account this script actually created or adopted (SERVICE_USER /
# SERVICE_GROUP), not the shipped default. The already-adopted check above
# greps UNIT_DEST for "^User=$SERVICE_USER$", so this substitution must keep
# producing exactly that line.
UNIT_TMP="$(mktemp)"
sed -e "s/^User=.*/User=$SERVICE_USER/" -e "s/^Group=.*/Group=$SERVICE_GROUP/" \
  "$UNIT_SRC" > "$UNIT_TMP"
install -m 0644 -o root -g root "$UNIT_TMP" "$UNIT_DEST"
rm -f "$UNIT_TMP"

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
