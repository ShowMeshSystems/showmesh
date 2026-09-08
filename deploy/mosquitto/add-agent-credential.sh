#!/usr/bin/env bash
#
# Provisions one ShowMesh agent's broker credential (ADR-024 decision 10):
#
#   cd deploy
#   ./mosquitto/add-agent-credential.sh <node-id>
#
# Prints the generated password once. Set it on the node itself as
# SHOWMESH_MQTT_USERNAME=<node-id> and SHOWMESH_MQTT_PASSWORD=<what this
# printed> (the agent's own environment/config, not anything in this
# deploy/ bundle — agents run natively on media-node hosts, per
# deploy/README.md).
#
# WHY THE USERNAME MUST EQUAL THE NODE ID, ENFORCED HERE:
# The generated ACL gives every agent an explicit `user <node-id>` block.
# That block is safe only because a broker username is constrained to the
# same character class
# pkg/mqttproto validates a ShowMesh node id against before an agent will
# even start — see ValidateNodeID / nodeIDPattern in
# pkg/mqttproto/topic.go:
#   ^[a-z0-9]([a-z0-9-]{0,62}[a-z0-9])?$
# (1-64 characters, lowercase letters/digits/interior hyphens only — no
# '+', '#', '/', uppercase, or anything else). That validator runs when an
# AGENT loads its own SHOWMESH_NODE_ID; it does NOT run against a broker
# username typed by hand into mosquitto/passwd. This script is what closes
# that gap for the broker side: it enforces the identical pattern below
# before it writes a credential or generated ACL entry.
set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
PASSWD_FILE="$SCRIPT_DIR/passwd"
MOSQUITTO_IMAGE="eclipse-mosquitto:2.0.22"

NODE_ID="${1:-}"
if [ -z "$NODE_ID" ]; then
  echo "usage: $0 <node-id>" >&2
  exit 1
fi

# Mirrors pkg/mqttproto's nodeIDPattern exactly (see this file's header
# comment for why that match matters, not just why the syntax looks
# familiar). If that pattern is ever widened in pkg/mqttproto, widen this
# one to match — the two are duplicated for the same reason
# internal/agent/mqtt.go's and internal/coordinator/broker/broker.go's own
# isAuthReasonCode copies are duplicated rather than shared (see those
# files): this deploy/ bundle does not import Go packages, but the
# character class itself must still not drift.
if ! [[ "$NODE_ID" =~ ^[a-z0-9]([a-z0-9-]{0,62}[a-z0-9])?$ ]]; then
  printf 'add-agent-credential: %q is not a valid ShowMesh node id: must be 1-64 characters of [a-z0-9-], not starting or ending with '"'"'-'"'"' (pkg/mqttproto.ValidateNodeID'"'"'s exact rule).\n' "$NODE_ID" >&2
  exit 1
fi

case "$NODE_ID" in
  coordinator|fpp|healthcheck|observer)
    echo "add-agent-credential: '$NODE_ID' is reserved for a fixed broker role and cannot be used as an agent node id." >&2
    exit 1
    ;;
esac

if [ ! -f "$PASSWD_FILE" ]; then
  echo "add-agent-credential: $PASSWD_FILE does not exist yet; run ./mosquitto/generate-credentials.sh first (it creates the file and the fixed broker-role credentials this bundle itself needs)." >&2
  exit 1
fi

random_password() {
  head -c 24 /dev/urandom | base64 | tr -d '\n' | tr '+/' '-_'
}

PASSWORD="$(random_password)"

echo "add-agent-credential: adding/updating credential for node id '$NODE_ID' in $PASSWD_FILE" >&2
docker run --rm -v "$SCRIPT_DIR:/out" "$MOSQUITTO_IMAGE" \
  mosquitto_passwd -b /out/passwd "$NODE_ID" "$PASSWORD" >/dev/null

# Rebuild first, then reload. The renderer writes the effective ACL via an
# atomic rename and only HUPs a Compose broker that is known to load it.
# If a legacy container is still mounted on the old ACL path, it prints the
# one-time recreate command instead of claiming the new credential is live.
"$SCRIPT_DIR/generate-credentials.sh" --render-acl

cat >&2 <<EOF

add-agent-credential: done. Set on the '$NODE_ID' node itself (not in this
deploy/ bundle):

  SHOWMESH_MQTT_USERNAME=$NODE_ID
  SHOWMESH_MQTT_PASSWORD=$PASSWORD

add-agent-credential: the generated ACL was rebuilt. When the current
Compose configuration is running, the script reloaded Mosquitto for you.
Mosquitto does NOT re-authenticate clients already connected: a credential
you rotate remains valid until that agent reconnects, or until the broker
restarts. A one-time upgrade from the old configuration requires:

  docker compose up -d --force-recreate mosquitto

EOF
