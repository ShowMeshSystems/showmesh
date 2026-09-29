#!/bin/sh
#
# Runs Mosquitto and reloads it when the broker logins change, so a node
# enrolled through the coordinator can connect without a broker restart.
# Mosquitto rereads passwd and acl.generated.conf on SIGHUP. This polls both
# files every two seconds and sends SIGHUP when either one is replaced or
# edited. A SIGHUP sent to this script (docker compose kill -s HUP, which
# generate-credentials.sh uses) is passed on to Mosquitto.
set -u

CONFIG_DIR="${MOSQUITTO_CONFIG_DIR:-/mosquitto/config}"
CONFIG_FILE="${MOSQUITTO_CONFIG_FILE:-$CONFIG_DIR/mosquitto.conf}"
POLL_SECONDS="${MOSQUITTO_RELOAD_POLL_SECONDS:-2}"

fingerprint() {
  for f in "$CONFIG_DIR/passwd" "$CONFIG_DIR/acl.generated.conf"; do
    stat -c '%i %Y %s' "$f" 2>/dev/null || echo missing
  done
}

/usr/sbin/mosquitto -c "$CONFIG_FILE" &
child=$!

trap 'kill -HUP "$child" 2>/dev/null' HUP
trap 'kill -TERM "$child" 2>/dev/null; wait "$child"; exit $?' TERM INT

last="$(fingerprint)"
while kill -0 "$child" 2>/dev/null; do
  sleep "$POLL_SECONDS" &
  wait $! 2>/dev/null
  now="$(fingerprint)"
  if [ "$now" != "$last" ]; then
    last="$now"
    echo "run-with-reload: broker login files changed; reloading mosquitto." >&2
    kill -HUP "$child" 2>/dev/null
  fi
done

wait "$child"
exit $?
