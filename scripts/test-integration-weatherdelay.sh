#!/usr/bin/env bash
#
# Starts a throwaway, uniquely-named Mosquitto broker and runs the
# `integration`-tagged weather delay tests (test/integration/weatherdelay_test.go)
# against it, then tears the broker and every temp file down. Backs `make
# test-integration-weatherdelay`.
#
# Modeled on scripts/test-integration.sh (same build tag convention, same
# "never fail for want of the dependency" discipline in the Go tests
# themselves, same pre-build-then-run shape). Deliberately its own script,
# not a `-run` flag added to that one: it starts its own broker container so
# this suite never touches, stops, or reuses a container another
# scripts/test-integration*.sh (or a developer's own running dev stack) is
# using, and it can run concurrently with them on the same laptop.
set -euo pipefail

ROOT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
cd "$ROOT_DIR"

# This script's entire job is to supply test/integration's broker
# dependency, exactly like scripts/test-integration.sh's own identical
# comment: a missing broker under this script means the script itself
# failed to supply it, which must never read as a quiet, green skip. This
# suite talks to its own in-process stand-in FPP player and node agent
# subprocesses, never a real fppd, so it declares only "broker".
export SHOWMESH_REQUIRE_TEST_DEPS=broker

MOSQUITTO_IMAGE="eclipse-mosquitto:2.0.22"

# A per-run PID+random suffix, per this suite's own isolation requirement:
# never a fixed name that could collide with scripts/test-integration.sh's
# own "showmesh-test-mosquitto" container, another concurrent run of this
# same script, or a developer's already-running dev stack.
CONTAINER_NAME="showmesh-test-mosquitto-weatherdelay-$$-${RANDOM}"

random_password() {
  head -c 24 /dev/urandom | base64 | tr -d '\n' | tr '+/' '-_'
}
export SHOWMESH_TEST_MQTT_COORDINATOR_USERNAME="coordinator"
export SHOWMESH_TEST_MQTT_COORDINATOR_PASSWORD="$(random_password)"

BIN_DIR="$(mktemp -d)"
TMP_SEED_PASSWD=""
TMP_SEED_ACL=""

# Runs on normal exit, a failure (set -e), AND an interrupt: the isolation
# requirement this script's own task names explicitly. Never stops or
# removes a container this script did not itself start.
cleanup() {
  local status=$?
  docker rm -f "$CONTAINER_NAME" >/dev/null 2>&1 || true
  rm -rf "$BIN_DIR"
  [ -n "$TMP_SEED_PASSWD" ] && rm -f "$TMP_SEED_PASSWD"
  [ -n "$TMP_SEED_ACL" ] && rm -f "$TMP_SEED_ACL"
  exit "$status"
}
trap cleanup EXIT INT TERM

echo "test-integration-weatherdelay: prebuilding showmesh-agent, showmesh-coordinator, and showmeshctl"
# CGO_ENABLED pinned per binary, matching test-integration.sh's identical
# reasoning: ADR-042 requires the agent link the real cgo GStreamer/libltc
# engine (this suite runs it against the "fakesink" backend via
# SHOWMESH_GST_AUDIO_SINK_FACTORY, never a separate fake-engine build), and
# ADR-012 requires the coordinator (and showmeshctl) build CGo-free.
CGO_ENABLED=1 go build -o "$BIN_DIR/showmesh-agent" ./cmd/showmesh-agent
CGO_ENABLED=0 go build -o "$BIN_DIR/showmesh-coordinator" ./cmd/showmesh-coordinator
CGO_ENABLED=0 go build -o "$BIN_DIR/showmeshctl" ./cmd/showmeshctl
export SHOWMESH_TEST_AGENT_BIN="$BIN_DIR/showmesh-agent"
export SHOWMESH_TEST_COORDINATOR_BIN="$BIN_DIR/showmesh-coordinator"
export SHOWMESH_TEST_SHOWMESHCTL_BIN="$BIN_DIR/showmeshctl"

echo "test-integration-weatherdelay: starting $MOSQUITTO_IMAGE as $CONTAINER_NAME"
# No fixed host port: -p 127.0.0.1::1883 asks the kernel for a free one at
# creation time (this script's own "bind to :0 and read it back" for a
# container, mirroring findFreePort's identical technique for the
# coordinator/agent processes in test/integration/harness_test.go), so this
# script never collides with test-integration.sh's own fixed 11883, a
# concurrent run of this same script, or a developer's running dev stack on
# 1883/8080/etc.
docker create --name "$CONTAINER_NAME" \
  -p "127.0.0.1::1883" \
  -v "$ROOT_DIR/deploy/mosquitto/mosquitto.conf:/mosquitto/config/mosquitto.conf:ro" \
  "$MOSQUITTO_IMAGE" >/dev/null

TMP_SEED_PASSWD="$(mktemp)"
docker run --rm -v "$TMP_SEED_PASSWD:/out/passwd" "$MOSQUITTO_IMAGE" \
  mosquitto_passwd -b -c /out/passwd "$SHOWMESH_TEST_MQTT_COORDINATOR_USERNAME" "$SHOWMESH_TEST_MQTT_COORDINATOR_PASSWORD" >/dev/null
docker cp "$TMP_SEED_PASSWD" "$CONTAINER_NAME:/mosquitto/config/passwd"

# The committed acl.conf, unedited: this suite only ever authenticates as
# the fixed "coordinator" role and its own provisioned per-node agent
# credentials (harness_test.go's provisionAgentCredential), so it needs no
# test-only ACL stanza the way scripts/test-integration.sh's burst
# publisher does.
TMP_SEED_ACL="$(mktemp)"
cp "$ROOT_DIR/deploy/mosquitto/acl.conf" "$TMP_SEED_ACL"
docker cp "$TMP_SEED_ACL" "$CONTAINER_NAME:/mosquitto/config/acl.generated.conf"

docker start "$CONTAINER_NAME" >/dev/null

HOST_PORT="$(docker port "$CONTAINER_NAME" 1883/tcp | head -n1 | cut -d: -f2)"
if [ -z "$HOST_PORT" ]; then
  echo "test-integration-weatherdelay: could not read back the host port docker assigned to $CONTAINER_NAME" >&2
  docker logs "$CONTAINER_NAME" >&2 || true
  exit 1
fi
export SHOWMESH_TEST_MQTT_BROKER="tcp://localhost:${HOST_PORT}"
export SHOWMESH_TEST_MOSQUITTO_CONTAINER="$CONTAINER_NAME"

echo "test-integration-weatherdelay: waiting for the broker to accept connections"
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
  echo "test-integration-weatherdelay: mosquitto did not start listening on port ${HOST_PORT} within 30s" >&2
  docker logs "$CONTAINER_NAME" >&2 || true
  exit 1
fi

# Generous but bounded: seven scenarios, each polling a real 5s enforcement
# tick and a real signing/HTTP/audio round trip at least once.
echo "test-integration-weatherdelay: running against $SHOWMESH_TEST_MQTT_BROKER (container $CONTAINER_NAME)"
if ! go test -tags=integration -race -count=1 -timeout=20m -v -run '^TestWeatherDelay' ./test/integration/...; then
  echo "test-integration-weatherdelay: go test failed; dumping $CONTAINER_NAME logs before teardown" >&2
  docker logs "$CONTAINER_NAME" >&2 || true
  exit 1
fi
