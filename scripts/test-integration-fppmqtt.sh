#!/usr/bin/env bash
#
# Starts a throwaway Mosquitto broker using the exact shipped
# deploy/mosquitto/mosquitto.conf, runs the `integration`-tagged Go test
# suite in internal/coordinator/collector/fppmqtt against it, then tears the
# broker down. Backs `make test-integration-fppmqtt`; see that target and
# internal/coordinator/collector/fppmqtt/integration_test.go's package doc
# comment for what this suite proves and why it needs a real broker rather
# than a fake: it is the one place that exercises Collector.Run's actual
# autopaho wiring (connect, subscribeAll, the OnPublishReceived handler) end
# to end, as opposed to the rest of that package's unit suite, which drives
# Collector.Poll by calling the publish handler directly.
#
# Mirrors test-integration.sh's shape closely (same pinned Mosquitto image,
# same shipped config, same throwaway-container discipline) with a
# different container name and port so the two scripts — and
# test-integration-fpp.sh's own second Mosquitto — can all run concurrently
# without colliding. Unlike test-integration-fpp.sh, this suite needs
# nothing else running first: internal/coordinator/collector/fppmqtt is a
# pure MQTT client, never an HTTP one, so there is no bench fppd to start
# here.
#
# Per this package's doc.go and the Step 5 spec section 0's absolute rule,
# this suite must NEVER be pointed at the reference installation's broker, or any other live
# broker — only a throwaway local Mosquitto, exactly like this script
# starts. The suite itself is the one place in this package's tests that
# publishes anything (see integration_test.go's testPublisher), which is
# safe here only because the broker under test is disposable.
set -euo pipefail

ROOT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
cd "$ROOT_DIR"

# Pinned to the exact version deploy/docker-compose.yml pins, same as
# test-integration.sh and test-integration-fpp.sh, so this suite never
# silently drifts onto a different Mosquitto build than the reference
# deployment ships.
MOSQUITTO_IMAGE="eclipse-mosquitto:2.0.22"

CONTAINER_NAME="${SHOWMESH_TEST_FPPMQTT_MOSQUITTO_CONTAINER:-showmesh-test-mosquitto-fppmqtt}"
# A port distinct from test-integration.sh's default (11883) and
# test-integration-fpp.sh's second broker (11893), so all three can run at
# once.
HOST_PORT="${SHOWMESH_TEST_FPPMQTT_MQTT_PORT:-11894}"

export SHOWMESH_TEST_MQTT_BROKER="tcp://localhost:${HOST_PORT}"

cleanup() {
  docker rm -f "$CONTAINER_NAME" >/dev/null 2>&1 || true
}
trap cleanup EXIT

# In case a previous run was interrupted before its own cleanup ran.
docker rm -f "$CONTAINER_NAME" >/dev/null 2>&1 || true

echo "test-integration-fppmqtt: starting $MOSQUITTO_IMAGE as $CONTAINER_NAME on port $HOST_PORT"
# The shipped config is mounted read-only, unmodified — see
# test-integration.sh's identical line for why no adaptation is needed.
docker run -d --name "$CONTAINER_NAME" \
  -p "${HOST_PORT}:1883" \
  -v "$ROOT_DIR/deploy/mosquitto/mosquitto.conf:/mosquitto/config/mosquitto.conf:ro" \
  "$MOSQUITTO_IMAGE" >/dev/null

echo "test-integration-fppmqtt: waiting for the broker to accept connections"
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
  echo "test-integration-fppmqtt: mosquitto did not start listening on port ${HOST_PORT} within 30s" >&2
  docker logs "$CONTAINER_NAME" >&2 || true
  exit 1
fi

echo "test-integration-fppmqtt: running against $SHOWMESH_TEST_MQTT_BROKER (container $CONTAINER_NAME)"
if ! go test -tags=integration -race -timeout=5m -v ./internal/coordinator/collector/fppmqtt/...; then
  echo "test-integration-fppmqtt: go test failed; dumping $CONTAINER_NAME logs before teardown" >&2
  docker logs "$CONTAINER_NAME" >&2 || true
  exit 1
fi
