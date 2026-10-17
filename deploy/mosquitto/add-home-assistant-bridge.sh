#!/bin/sh
#
# Bridges the built-in broker to a Home Assistant broker so FPP's MQTT topics
# flow both ways: Home Assistant sees FPP status, and commands Home Assistant
# publishes reach FPP. The bridge is a Mosquitto bridge, so no second client,
# container or credential on the ShowMesh side is needed.
#
# What it writes, under $COORDINATOR_DIR/mosquitto (default
# /opt/showmesh/coordinator/mosquitto):
#   conf.d/home-assistant-bridge.conf   the bridge, readable by the broker only
#   mosquitto.conf                      gains one `include_dir conf.d` line
#
# Until the installer carries this setting, an upgrade rewrites mosquitto.conf
# and drops the include_dir line; the bridge file itself is kept. Run this
# script again after an upgrade, with the same arguments, to restore it.
#
# The bridge changes topic access for nobody: Mosquitto does not apply the
# local ACL to its own bridges, and the FPP login keeps the grants in acl.conf.
set -eu

COORDINATOR_DIR="${COORDINATOR_DIR:-/opt/showmesh/coordinator}"
MQ_DIR="$COORDINATOR_DIR/mosquitto"
CONF_D="$MQ_DIR/conf.d"
BRIDGE_NAME="home-assistant"
BRIDGE_FILE="$CONF_D/$BRIDGE_NAME-bridge.conf"
INCLUDE_LINE="include_dir /mosquitto/config/conf.d"

HA_HOST=""
HA_PORT=1883
HA_USER=""
HA_PASSWORD=""
PASSWORD_FILE=""
SHARE_SHOWMESH=no
REMOVE=no
RESTART=yes

usage() {
  cat <<USAGE
usage: $0 --host <home-assistant-broker> --user <login> [--password-file <file>] [options]
       $0 --remove

Connects the built-in ShowMesh broker to a Home Assistant broker and carries
falcon/player/# in both directions.

  --host <host>            Home Assistant broker address (required)
  --port <port>            Home Assistant broker port (default 1883)
  --user <login>           login the bridge presents to Home Assistant (required)
  --password-file <file>   file holding that login's password; without it the
                           script prompts, or reads HA_MQTT_PASSWORD from the
                           environment when there is no terminal
  --share-showmesh         also mirror showmesh/# to Home Assistant, read-only
  --no-restart             write the files but do not restart the broker
  --remove                 delete the bridge and restart the broker
USAGE
}

say() { printf '%s\n' "$*"; }
die() { printf 'add-home-assistant-bridge: %s\n' "$*" >&2; exit 1; }

while [ $# -gt 0 ]; do
  case "$1" in
    --host) HA_HOST="${2:-}"; shift 2 ;;
    --port) HA_PORT="${2:-}"; shift 2 ;;
    --user) HA_USER="${2:-}"; shift 2 ;;
    --password-file) PASSWORD_FILE="${2:-}"; shift 2 ;;
    --share-showmesh) SHARE_SHOWMESH=yes; shift ;;
    --no-restart) RESTART=no; shift ;;
    --remove) REMOVE=yes; shift ;;
    -h|--help) usage; exit 0 ;;
    *) usage >&2; die "unknown argument: $1" ;;
  esac
done

[ -f "$MQ_DIR/mosquitto.conf" ] || die "no broker configuration at $MQ_DIR/mosquitto.conf. Set COORDINATOR_DIR if the coordinator lives elsewhere."
[ "$(id -u)" -eq 0 ] || [ -w "$MQ_DIR" ] || die "run as root: $MQ_DIR is owned by the coordinator."

group_of() { ls -ldn "$1" | awk '{print $4}'; }
MQ_GID="$(group_of "$MQ_DIR")"

compose() {
  files="-f docker-compose.yml"
  [ -f "$COORDINATOR_DIR/docker-compose.published.yml" ] && files="$files -f docker-compose.published.yml"
  (cd "$COORDINATOR_DIR" && docker compose $files "$@")
}

# restart_broker restarts only the broker container; nodes and the coordinator reconnect on their own.
restart_broker() {
  [ "$RESTART" = yes ] || { say "Broker not restarted (--no-restart). The change applies at the next broker restart."; return 0; }
  command -v docker >/dev/null 2>&1 || die "docker is not on PATH; restart the broker yourself to apply the change."
  say "Restarting the broker. Nodes show offline for a few seconds and reconnect on their own."
  compose restart mosquitto >/dev/null
}

# wait_for_bridge reads the broker's own retained bridge state with the healthcheck login, never showing the password to ps.
wait_for_bridge() {
  env_file="$COORDINATOR_DIR/.env"
  user="$(sed -n 's/^MOSQUITTO_HEALTHCHECK_USERNAME=//p' "$env_file" 2>/dev/null | tail -1)"
  pass="$(sed -n 's/^MOSQUITTO_HEALTHCHECK_PASSWORD=//p' "$env_file" 2>/dev/null | tail -1)"
  if [ -z "$user" ] || [ -z "$pass" ]; then
    say "Could not read the healthcheck login from $env_file; check the broker log for the bridge yourself:"
    say "  cd $COORDINATOR_DIR && docker compose logs --tail 40 mosquitto"
    return 0
  fi
  state="$(compose exec -T -e MQ_USER="$user" -e MQ_PASS="$pass" mosquitto sh -c '
    umask 077; d="$(mktemp -d)"; printf -- "-u %s\n-P %s\n" "$MQ_USER" "$MQ_PASS" > "$d/mosquitto_sub"
    XDG_CONFIG_HOME="$d" mosquitto_sub -h localhost -p 1883 -i showmesh-bridge-check -t "\$SYS/broker/connection/'"$BRIDGE_NAME"'/state" -C 1 -W 20; rm -rf "$d"' 2>/dev/null || true)"
  case "$state" in
    1) say "The bridge to $HA_HOST:$HA_PORT is connected." ;;
    0) say "The broker started but the bridge to $HA_HOST:$HA_PORT is not connected yet. Check the address and login, then read:"
       say "  cd $COORDINATOR_DIR && docker compose logs --tail 40 mosquitto" ;;
    *) say "The bridge state did not arrive within 20 seconds. Read the broker log:"
       say "  cd $COORDINATOR_DIR && docker compose logs --tail 40 mosquitto" ;;
  esac
}

if [ "$REMOVE" = yes ]; then
  if [ -f "$BRIDGE_FILE" ]; then
    rm -f "$BRIDGE_FILE"
    say "Removed $BRIDGE_FILE."
    restart_broker
  else
    say "No bridge to remove at $BRIDGE_FILE."
  fi
  exit 0
fi

[ -n "$HA_HOST" ] || { usage >&2; die "--host is required."; }
[ -n "$HA_USER" ] || { usage >&2; die "--user is required."; }
case "$HA_PORT" in ''|*[!0-9]*) die "--port must be a number." ;; esac
case "$HA_HOST$HA_USER" in *[[:space:]]*) die "the host and user may not contain spaces." ;; esac

if [ -n "$PASSWORD_FILE" ]; then
  [ -r "$PASSWORD_FILE" ] || die "cannot read $PASSWORD_FILE."
  HA_PASSWORD="$(head -n 1 "$PASSWORD_FILE")"
elif [ -n "${HA_MQTT_PASSWORD:-}" ]; then
  HA_PASSWORD="$HA_MQTT_PASSWORD"
elif [ -t 0 ]; then
  printf 'Password for %s on %s: ' "$HA_USER" "$HA_HOST"
  stty -echo; read -r HA_PASSWORD; stty echo; printf '\n'
else
  die "no password: pass --password-file or set HA_MQTT_PASSWORD."
fi
[ -n "$HA_PASSWORD" ] || die "the password is empty."
nl="$(printf '\nx')"; nl="${nl%x}"
case "$HA_PASSWORD" in *"$nl"*) die "the password may not contain a newline." ;; esac

umask 027
mkdir -p "$CONF_D"
chgrp "$MQ_GID" "$CONF_D" 2>/dev/null || true
chmod 0750 "$CONF_D"

tmp="$(mktemp "$CONF_D/.bridge.XXXXXX")"
{
  cat <<CONF
# Written by add-home-assistant-bridge.sh. Edit by running it again.
connection $BRIDGE_NAME
address $HA_HOST:$HA_PORT
remote_username $HA_USER
remote_password $HA_PASSWORD
remote_clientid showmesh-bridge-$(hostname -s 2>/dev/null || hostname)
cleansession true
start_type automatic
restart_timeout 5 30
notifications true
notifications_local_only true
notification_topic \$SYS/broker/connection/$BRIDGE_NAME/state
try_private true
bridge_protocol_version mqttv311

# FPP status and FPP commands, in both directions.
topic falcon/player/# both 0
# FPP's Home Assistant discovery announcements, when FPP publishes them here.
topic homeassistant/# out 0
CONF
  if [ "$SHARE_SHOWMESH" = yes ]; then
    printf '\n# ShowMesh node state, read-only for Home Assistant dashboards.\ntopic showmesh/# out 0\n'
  fi
} > "$tmp"
chgrp "$MQ_GID" "$tmp" 2>/dev/null || true
chmod 0640 "$tmp"
mv -f "$tmp" "$BRIDGE_FILE"
say "Wrote $BRIDGE_FILE."

if ! grep -qx "$INCLUDE_LINE" "$MQ_DIR/mosquitto.conf"; then
  printf '\n# Drop-in settings, such as the Home Assistant bridge. Added by add-home-assistant-bridge.sh.\n%s\n' "$INCLUDE_LINE" >> "$MQ_DIR/mosquitto.conf"
  say "Added '$INCLUDE_LINE' to mosquitto.conf."
fi

restart_broker
[ "$RESTART" = yes ] && wait_for_bridge
say "To take the bridge down again: $0 --remove"
