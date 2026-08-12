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
# password. It also builds acl.generated.conf from acl.conf and the actual
# passwd usernames. It is idempotent: an existing password file is never
# rotated, but its effective ACL is rebuilt so an upgrade from the former
# global-pattern ACL is safe and complete.
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
ACL_BASE_FILE="$SCRIPT_DIR/acl.conf"
ACL_FILE="$SCRIPT_DIR/acl.generated.conf"
ACL_MIGRATION_MARKER="$SCRIPT_DIR/.acl-explicit-agents-v1"
ENV_FILE="$DEPLOY_DIR/.env"

# Pinned to the exact image docker-compose.yml uses for the mosquitto
# service, so the passwd file's hash format is guaranteed compatible with
# whatever actually reads it — mosquitto_passwd's default hashing has
# changed across major versions in the past, and generating the file with
# a different version than the one that will serve it is exactly the kind
# of mismatch that only shows up as an inexplicable auth failure later.
MOSQUITTO_IMAGE="eclipse-mosquitto:2.0.22"

is_fixed_role() {
  case "$1" in
    coordinator|fpp|healthcheck) return 0 ;;
    *) return 1 ;;
  esac
}

is_valid_node_id() {
  [[ "$1" =~ ^[a-z0-9]([a-z0-9-]{0,62}[a-z0-9])?$ ]]
}

reload_if_current_compose_broker_is_running() {
  # acl.generated.conf is atomically replaced. The Compose directory mount
  # makes that new name visible to the container; a single-file bind mount
  # would not. Do not HUP an old container that still loads acl.conf: report
  # the required one-time recreate instead of pretending this migration took
  # effect.
  local container_id config_directory_mount
  if ! command -v docker >/dev/null 2>&1; then
    return
  fi
  container_id="$(docker compose -f "$DEPLOY_DIR/docker-compose.yml" ps -q mosquitto 2>/dev/null || true)"
  if [ -z "$container_id" ]; then
    return
  fi
  config_directory_mount="$(docker inspect -f '{{range .Mounts}}{{if eq .Destination "/mosquitto/config"}}{{.Type}}{{end}}{{end}}' "$container_id" 2>/dev/null || true)"
  if [ "$config_directory_mount" = "bind" ] && docker compose -f "$DEPLOY_DIR/docker-compose.yml" exec -T mosquitto \
    sh -ec 'grep -Fx "acl_file /mosquitto/config/acl.generated.conf" /mosquitto/config/mosquitto.conf >/dev/null' \
    >/dev/null 2>&1; then
    docker compose -f "$DEPLOY_DIR/docker-compose.yml" kill -s HUP mosquitto >/dev/null
    echo "generate-credentials: reloaded running mosquitto so the rebuilt ACL is active." >&2
  else
    echo "generate-credentials: rebuilt $ACL_FILE, but the running mosquitto container has the pre-generated-ACL configuration; run 'docker compose up -d --force-recreate mosquitto' once to migrate it." >&2
  fi
}

render_acl() {
	local allow_first_migration="${1:-false}"
	if [ ! -f "$PASSWD_FILE" ]; then
    echo "generate-credentials: cannot build an ACL because $PASSWD_FILE does not exist." >&2
    return 1
  fi
	if [ ! -f "$ACL_BASE_FILE" ]; then
    echo "generate-credentials: ACL base $ACL_BASE_FILE does not exist." >&2
    return 1
	fi
	if [ ! -f "$ACL_MIGRATION_MARKER" ] && [ "$allow_first_migration" != "true" ]; then
		echo "generate-credentials: refusing to infer fixed-role ownership while creating $ACL_FILE for the first time." >&2
		echo "generate-credentials: older tooling allowed agent node IDs named coordinator, fpp, or healthcheck; those names are now reserved and are indistinguishable from the fixed accounts in passwd." >&2
		echo "generate-credentials: confirm those three usernames are fixed roles (rename any legacy agent using one first), then run: $0 --migrate-existing" >&2
		return 1
	fi

  local tmp username _hash
  tmp="$(mktemp "$SCRIPT_DIR/.acl.generated.XXXXXX")"
  trap 'rm -f "$tmp"' RETURN
  cat "$ACL_BASE_FILE" > "$tmp"

  while IFS=: read -r username _hash; do
    [ -n "$username" ] || continue
    if is_fixed_role "$username"; then
      continue
    fi
    if ! is_valid_node_id "$username"; then
      echo "generate-credentials: cannot safely migrate username '$username': non-fixed broker usernames must be valid ShowMesh node IDs. Rename or remove that passwd entry before generating the explicit ACL." >&2
      return 1
    fi
    cat >> "$tmp" <<EOF

# Provisioned ShowMesh agent: $username
user $username
topic write showmesh/nodes/$username/hello
topic write showmesh/nodes/$username/lwt
topic write showmesh/nodes/$username/observed/#
topic write showmesh/nodes/$username/result/+
topic read  showmesh/nodes/$username/cmd
EOF
  done < "$PASSWD_FILE"

  chmod 644 "$tmp"
	mv "$tmp" "$ACL_FILE"
	if [ "$allow_first_migration" = "true" ]; then
		: > "$ACL_MIGRATION_MARKER"
		chmod 600 "$ACL_MIGRATION_MARKER"
	fi
  trap - RETURN
  echo "generate-credentials: rebuilt $ACL_FILE from $PASSWD_FILE." >&2
  reload_if_current_compose_broker_is_running
}

if [ "${1:-}" = "--render-acl" ]; then
  if [ "$#" -ne 1 ]; then
    echo "usage: $0 [--render-acl]" >&2
    exit 1
  fi
	render_acl false
	exit 0
fi
if [ "${1:-}" = "--migrate-existing" ]; then
	if [ "$#" -ne 1 ]; then
		echo "usage: $0 [--render-acl|--migrate-existing]" >&2
		exit 1
	fi
	render_acl true
	exit 0
fi
if [ "$#" -ne 0 ]; then
	echo "usage: $0 [--render-acl|--migrate-existing]" >&2
  exit 1
fi

if [ -f "$PASSWD_FILE" ]; then
  echo "generate-credentials: $PASSWD_FILE already exists; preserving credentials and rebuilding its explicit ACL." >&2
	render_acl false
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
render_acl true

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
