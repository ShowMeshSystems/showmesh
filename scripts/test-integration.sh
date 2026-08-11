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

cleanup() {
  docker rm -f "$CONTAINER_NAME" >/dev/null 2>&1 || true
}
trap cleanup EXIT

# In case a previous run was interrupted before its own cleanup ran.
docker rm -f "$CONTAINER_NAME" >/dev/null 2>&1 || true

echo "test-integration: starting $MOSQUITTO_IMAGE as $CONTAINER_NAME on port $HOST_PORT"
# The shipped config is mounted read-only, UNMODIFIED: this is the exact
# configuration operators get (autosave_interval, retained-message
# persistence semantics, and the anonymous-access/listen-address settings
# already scoped for an isolated show network per the file's own SECURITY
# WARNING). No adaptation is needed for this throwaway container: the
# eclipse-mosquitto image already ships writable /mosquitto/data and
# /mosquitto/log directories, and the shipped config already binds 0.0.0.0
# with anonymous access enabled — unlike the image's own bare default
# (deny anonymous, listen on localhost only inside the container), which is
# exactly the trap the Task E spec calls out by name.
docker run -d --name "$CONTAINER_NAME" \
  -p "${HOST_PORT}:1883" \
  -v "$ROOT_DIR/deploy/mosquitto/mosquitto.conf:/mosquitto/config/mosquitto.conf:ro" \
  "$MOSQUITTO_IMAGE" >/dev/null

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
if ! go test -tags=integration -race -timeout=8m -v ./test/integration/...; then
  echo "test-integration: go test failed; dumping $CONTAINER_NAME logs before teardown" >&2
  docker logs "$CONTAINER_NAME" >&2 || true
  exit 1
fi
