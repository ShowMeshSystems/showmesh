#!/usr/bin/env bash
#
# One-time, per-deployment broker credential generation (ADR-024 decision
# 10). Run this once from deploy/, BEFORE the first `docker compose up`:
#
#   cd deploy
#   ./mosquitto/generate-credentials.sh
#
# It creates deploy/mosquitto/passwd (bcrypt-hashed via mosquitto_passwd,
# gitignored, never committed) with three fixed broker roles this bundle
# itself needs — coordinator, fpp, healthcheck — each a fresh random
# password. It is idempotent: if deploy/mosquitto/passwd already exists,
# this script does nothing and exits 0, so re-running it (e.g. from a setup
# script that always calls it) never silently rotates credentials an
# operator did not ask to rotate.
#
# Credentials are NEVER authored into this repository: a password file
# checked into version control would be identical in every ShowMesh
# installation, which is precisely the failure ADR-021 named when it
# rejected a mandatory shared secret with no distribution mechanism (see
# ADR-024 decision 10, "generated per deployment at first run, never
# shipped in deploy/"). This script's whole job is to be the "generated at
# first run" half of that sentence.
#
# What this script does NOT do: provision a credential for any ShowMesh
# agent (each node's own credential is provisioned individually, when that
# node is set up, via add-agent-credential.sh in this same directory — see
# that script and deploy/README.md's migration section), and does not
# configure FPP itself (FPP's own MQTT settings live on the FPP host, not
# in this repository; this script only creates the broker-side account FPP
# will authenticate as, and prints it once for you to copy over).
set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
DEPLOY_DIR="$(cd "$SCRIPT_DIR/.." && pwd)"
PASSWD_FILE="$SCRIPT_DIR/passwd"
ENV_FILE="$DEPLOY_DIR/.env"

# Pinned to the exact image docker-compose.yml uses for the mosquitto
# service, so the passwd file's hash format is guaranteed compatible with
# whatever actually reads it — mosquitto_passwd's default hashing has
# changed across major versions in the past, and generating the file with
# a different version than the one that will serve it is exactly the kind
# of mismatch that only shows up as an inexplicable auth failure later.
MOSQUITTO_IMAGE="eclipse-mosquitto:2.0.22"

if [ -f "$PASSWD_FILE" ]; then
  echo "generate-credentials: $PASSWD_FILE already exists; doing nothing." >&2
  echo "generate-credentials: to rotate a credential, use add-agent-credential.sh for a node," >&2
  echo "generate-credentials: or delete $PASSWD_FILE and re-run this script to regenerate every fixed role (this invalidates every existing agent credential too)." >&2
  exit 0
fi

random_password() {
  # 24 random bytes, base64-encoded: comfortably above the entropy a
  # bcrypt-style hash can even make use of, and shell/URL-safe enough to
  # pass on a docker run command line for this one-shot local
  # bootstrapping step (see this file's own note on why that is a
  # different exposure than the long-lived broker healthcheck's command
  # line, which docker-compose.yml's comment addresses separately).
  head -c 24 /dev/urandom | base64 | tr -d '\n' | tr '+/' '-_'
}

COORDINATOR_PASSWORD="$(random_password)"
FPP_PASSWORD="$(random_password)"
HEALTHCHECK_PASSWORD="$(random_password)"

echo "generate-credentials: creating $PASSWD_FILE with coordinator/fpp/healthcheck credentials" >&2

# -c creates (or overwrites) the file with its first entry; every entry
# after that uses -b without -c to append. mosquitto_passwd ships in the
# eclipse-mosquitto image itself, so this needs nothing installed locally
# beyond Docker, and always uses the exact hashing this deployment's
# broker version expects.
docker run --rm -v "$SCRIPT_DIR:/out" "$MOSQUITTO_IMAGE" \
  mosquitto_passwd -b -c /out/passwd coordinator "$COORDINATOR_PASSWORD" >/dev/null
docker run --rm -v "$SCRIPT_DIR:/out" "$MOSQUITTO_IMAGE" \
  mosquitto_passwd -b /out/passwd fpp "$FPP_PASSWORD" >/dev/null
docker run --rm -v "$SCRIPT_DIR:/out" "$MOSQUITTO_IMAGE" \
  mosquitto_passwd -b /out/passwd healthcheck "$HEALTHCHECK_PASSWORD" >/dev/null

chmod 600 "$PASSWD_FILE"

# The coordinator's and the healthcheck's credentials are what THIS bundle
# consumes (SHOWMESH_MQTT_USERNAME/PASSWORD for the coordinator service,
# MOSQUITTO_HEALTHCHECK_USERNAME/PASSWORD for the mosquitto service's own
# healthcheck — see docker-compose.yml), so write them into deploy/.env,
# upserting rather than blindly appending in case this is a re-run after
# deploy/.env already has stale lines from a previous, deleted passwd file.
upsert_env() {
  local key="$1" value="$2"
  touch "$ENV_FILE"
  if grep -q "^${key}=" "$ENV_FILE" 2>/dev/null; then
    # BSD sed (macOS) and GNU sed (Linux) disagree on -i's argument
    # syntax; write to a temp file and move it instead of relying on
    # either's in-place flag behavior.
    awk -v k="$key" -v v="$value" -F'=' 'BEGIN{OFS="="} $1==k{$0=k"="v} {print}' "$ENV_FILE" > "$ENV_FILE.tmp"
    mv "$ENV_FILE.tmp" "$ENV_FILE"
  else
    printf '%s=%s\n' "$key" "$value" >> "$ENV_FILE"
  fi
}

upsert_env "SHOWMESH_MQTT_USERNAME" "coordinator"
upsert_env "SHOWMESH_MQTT_PASSWORD" "$COORDINATOR_PASSWORD"
upsert_env "MOSQUITTO_HEALTHCHECK_USERNAME" "healthcheck"
upsert_env "MOSQUITTO_HEALTHCHECK_PASSWORD" "$HEALTHCHECK_PASSWORD"
chmod 600 "$ENV_FILE"

cat >&2 <<EOF

generate-credentials: done. $ENV_FILE now has the coordinator and
healthcheck credentials (never printed here — read that file if you need
them again).

generate-credentials: FPP publisher-role credential (this repository does
not configure FPP itself, so this is printed ONCE for you to copy into
each FPP instance you point at this broker, per FPP's own MQTT settings —
System Configuration -> MQTT in FPP's UI):

  username: fpp
  password: $FPP_PASSWORD

generate-credentials: to add a credential for a ShowMesh agent (a node),
use ./mosquitto/add-agent-credential.sh <node-id> instead of this script.
EOF
