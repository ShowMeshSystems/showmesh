#!/usr/bin/env bash
#
# Starts a throwaway Mosquitto broker using the exact shipped
# deploy/mosquitto/mosquitto.conf, runs the `integration`-tagged Go test
# suite in test/integration against it, then tears the broker down. Backs
# `make test-integration`; see that target and test/integration's package
# doc comment for what these tests prove and why they need a real broker
# rather than a fake.
#
# Both CI and a developer laptop run this same script, so the two exercise
# the same broker configuration operators actually get (per the Step 2
# round 2 Task E spec) rather than CI covering a bespoke test-only config
# that could drift from deploy/mosquitto/mosquitto.conf unnoticed.
set -euo pipefail

ROOT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
cd "$ROOT_DIR"

# This script's entire job is to supply test/integration's dependencies (a
# real broker, here). SHOWMESH_REQUIRE_TEST_DEPS turns every dependency
# skip in that package (requireBroker et al.) into a hard test failure
# instead — a missing dependency under THIS script means the script itself
# failed to supply it, which must never read as a quiet, green skip. Run
# by hand with this unset, the skip stays the convenient default an
# unprepared laptop gets.
# This script starts a broker and NO fppd, so it declares only "broker".
# A bare "true" here is what broke CI on the first attempt: it made this
# script demand an FPP it never supplies, turning three legitimately
# skipping tests into failures. A harness may only be held to what it
# actually starts.
export SHOWMESH_REQUIRE_TEST_DEPS=broker

# Pinned to the exact version deploy/docker-compose.yml pins (see that
# file's `mosquitto.image`), so this suite never silently drifts onto a
# different Mosquitto build than the reference deployment ships.
MOSQUITTO_IMAGE="eclipse-mosquitto:2.0.22"

CONTAINER_NAME="${SHOWMESH_TEST_MOSQUITTO_CONTAINER:-showmesh-test-mosquitto}"
# A non-production port, deliberately not 1883: deploy/docker-compose.yml's
# bundled broker already listens on 1883 by default, and this script must
# not collide with a developer's already-running reference deployment.
HOST_PORT="${SHOWMESH_TEST_MQTT_PORT:-11883}"

# Compressed timing knobs: see internal/agent/heartbeat.go's
# envHeartbeatIntervalOverride and
# internal/coordinator/inventory/liveness.go's envStalenessWindowOverride.
# The production defaults are 10s/30s, which would make a liveness-focused
# suite like this take minutes; these stay in roughly the same 1:3 ratio as
# the production hypothesis (three missed heartbeats before evidence goes
# stale) rather than picking arbitrary unrelated values.
export SHOWMESH_TEST_HEARTBEAT_INTERVAL="${SHOWMESH_TEST_HEARTBEAT_INTERVAL:-300ms}"
export SHOWMESH_TEST_STALENESS_WINDOW="${SHOWMESH_TEST_STALENESS_WINDOW:-1s}"

export SHOWMESH_TEST_MQTT_BROKER="tcp://localhost:${HOST_PORT}"
export SHOWMESH_TEST_MOSQUITTO_CONTAINER="$CONTAINER_NAME"

# ADR-024 decision 10: the shipped mosquitto.conf now sets
# allow_anonymous false, so this suite's coordinator and agent subprocesses
# need real broker credentials, the exact same way an operator following
# deploy/README.md does. A random coordinator credential is generated here
# and exported for the Go test binary to read (see
# test/integration/harness_test.go's envTestMQTTCoordinatorUsername/
# Password); every agent subprocess gets its own credential provisioned
# dynamically, per node id, by harness_test.go's provisionAgentCredential
# (via `docker exec` against $CONTAINER_NAME, since each test invents its
# own unique node id and no fixed set could be pre-provisioned here).
random_password() {
  head -c 24 /dev/urandom | base64 | tr -d '\n' | tr '+/' '-_'
}
export SHOWMESH_TEST_MQTT_COORDINATOR_USERNAME="coordinator"
export SHOWMESH_TEST_MQTT_COORDINATOR_PASSWORD="$(random_password)"

# api_test.go's publishHelloBurst (shared by TestSlowSSEConsumerGetsResetAndDisconnected
# and TestWatchResnapshotsAfterStreamReset) injects a couple hundred
# synthetic hello messages for distinct, never-before-seen node IDs from
# ONE connection, to stress the coordinator's render path without spinning
# up hundreds of real agent subprocesses. Under allow_anonymous=false plus
# per-agent explicit ACL blocks, that is structurally impossible
# for any single real per-node credential — deliberately: "one client
# publishing hello for arbitrary node IDs" is exactly the deferred item
# ADR-024 decision 10's ACL exists to close (see that decision's context
# section). So this one dedicated, CLEARLY test-only credential exists
# (see the generated-ACL-plus-one-stanza construction below) rather than
# weakening any of the four real principal classes to accommodate it; a
# production deployment's ACL base never contains it.
export SHOWMESH_TEST_MQTT_BURST_PUBLISHER_USERNAME="test-burst-publisher"
export SHOWMESH_TEST_MQTT_BURST_PUBLISHER_PASSWORD="$(random_password)"

cleanup() {
  docker rm -f "$CONTAINER_NAME" >/dev/null 2>&1 || true
}
trap cleanup EXIT

# In case a previous run was interrupted before its own cleanup ran.
docker rm -f "$CONTAINER_NAME" >/dev/null 2>&1 || true

echo "test-integration: starting $MOSQUITTO_IMAGE as $CONTAINER_NAME on port $HOST_PORT"
# mosquitto.conf is mounted read-only, UNMODIFIED: this is the exact
# configuration operators get (autosave_interval, retained-message
# persistence semantics, allow_anonymous false, and the
# password_file/acl_file wiring), so this suite never silently drifts onto
# a bespoke test-only broker posture on that front. The eclipse-mosquitto
# image already ships writable /mosquitto/data and /mosquitto/log
# directories, so no adaptation is needed for it either.
#
# acl.generated.conf is NOT bind-mounted from the repo directly — see the
# SHOWMESH_TEST_MQTT_BURST_PUBLISHER_USERNAME comment above for why one
# extra, clearly-labeled test-only stanza is appended to a COPY of it
# below, the same "seed via docker cp before start" technique passwd
# already needs (see the next paragraph): every real ADR-024 decision 10
# rule from the committed file is still loaded verbatim and unedited, in
# the same order; nothing here removes or narrows anything production
# gets, and broker_auth_test.go's own tests prove the four real principal
# classes directly against this exact broker, so this addition is not the
# only thing standing between this suite and a false sense of security.
#
# password_file is now required, and mosquitto refuses to start at all if
# it does not exist yet (see deploy/README.md's "generate-credentials.sh
# is not optional" note) — so the container is created first, WITHOUT
# starting it; both seed files are copied in via `docker cp` (which works
# against a created-but-not-started container), and only then is it
# started. This is the throwaway-suite equivalent of deploy/README.md's
# real first-run step; it exists here rather than shelling out to that
# script because this suite deliberately never uses a host bind mount for
# passwd (see provisionAgentCredential's doc comment in harness_test.go
# for why: every per-node credential added later goes through `docker
# exec` against this container's own writable layer instead).
docker create --name "$CONTAINER_NAME" \
  -p "${HOST_PORT}:1883" \
  -v "$ROOT_DIR/deploy/mosquitto/mosquitto.conf:/mosquitto/config/mosquitto.conf:ro" \
  "$MOSQUITTO_IMAGE" >/dev/null

TMP_SEED_PASSWD="$(mktemp)"
docker run --rm -v "$TMP_SEED_PASSWD:/out/passwd" "$MOSQUITTO_IMAGE" \
  mosquitto_passwd -b -c /out/passwd "$SHOWMESH_TEST_MQTT_COORDINATOR_USERNAME" "$SHOWMESH_TEST_MQTT_COORDINATOR_PASSWORD" >/dev/null
docker run --rm -v "$TMP_SEED_PASSWD:/out/passwd" "$MOSQUITTO_IMAGE" \
  mosquitto_passwd -b /out/passwd "$SHOWMESH_TEST_MQTT_BURST_PUBLISHER_USERNAME" "$SHOWMESH_TEST_MQTT_BURST_PUBLISHER_PASSWORD" >/dev/null
docker cp "$TMP_SEED_PASSWD" "$CONTAINER_NAME:/mosquitto/config/passwd"
rm -f "$TMP_SEED_PASSWD"

TMP_SEED_ACL="$(mktemp)"
cat "$ROOT_DIR/deploy/mosquitto/acl.conf" > "$TMP_SEED_ACL"
cat >> "$TMP_SEED_ACL" <<EOF

# --- TEST-ONLY, appended by scripts/test-integration.sh, never part of ---
# --- the committed deploy/mosquitto/acl.conf and never shipped ----------
# See this script's SHOWMESH_TEST_MQTT_BURST_PUBLISHER_USERNAME comment
# above for why this exists: api_test.go's publishHelloBurst needs to
# publish hello messages for many distinct synthetic node IDs from one
# connection, which no real per-node credential may do under the ACL
# above.
user $SHOWMESH_TEST_MQTT_BURST_PUBLISHER_USERNAME
topic write showmesh/nodes/+/hello
EOF
docker cp "$TMP_SEED_ACL" "$CONTAINER_NAME:/mosquitto/config/acl.generated.conf"
rm -f "$TMP_SEED_ACL"

docker start "$CONTAINER_NAME" >/dev/null

echo "test-integration: waiting for the broker to accept connections"
ready=0
for _ in $(seq 1 30); do
  if (exec 3<>"/dev/tcp/localhost/${HOST_PORT}") 2>/dev/null; then
    exec 3<&- 3>&- || true
    ready=1
    break
  fi
  sleep 1
done
if [ "$ready" -ne 1 ]; then
  echo "test-integration: mosquitto did not start listening on port ${HOST_PORT} within 30s" >&2
  docker logs "$CONTAINER_NAME" >&2 || true
  exit 1
fi

echo "test-integration: running against $SHOWMESH_TEST_MQTT_BROKER (container $CONTAINER_NAME)"
if ! go test -tags=integration -race -count=1 -timeout=8m -v ./test/integration/...; then
  echo "test-integration: go test failed; dumping $CONTAINER_NAME logs before teardown" >&2
  docker logs "$CONTAINER_NAME" >&2 || true
  exit 1
fi
