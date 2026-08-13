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

# This script's entire job is to supply the Go suite's dependencies (a
# throwaway broker and credentials). SHOWMESH_REQUIRE_TEST_DEPS turns its
# dependency skip (requireTestBroker) into a hard test failure instead — a
# missing dependency under THIS script means the script itself failed to
# supply it, which must never read as a quiet, green skip. Run by hand
# with this unset, the skip stays the convenient default an unprepared
# laptop gets.
# This script starts a throwaway broker and no fppd.
export SHOWMESH_REQUIRE_TEST_DEPS=broker

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

# Keep the disposable ACL seed aligned with the exact shipped config. The
# deploy credential tooling may render its effective ACL under a different
# filename than the committed base (currently acl.generated.conf).
ACL_CONTAINER_PATH="$(awk '$1 == "acl_file" { print $2; exit }' "$ROOT_DIR/deploy/mosquitto/mosquitto.conf")"
if [ -z "$ACL_CONTAINER_PATH" ]; then
  echo "test-integration-fppmqtt: deploy/mosquitto/mosquitto.conf has no acl_file" >&2
  exit 1
fi

# The FPP collector and the simulated FPP publisher deliberately use
# different principals. The publisher can write only the FPP telemetry topic
# suffixes the collector models; the collector can read only those suffixes.
# Neither gets access to any showmesh/ topic or to FPP command topics.
random_password() {
  head -c 24 /dev/urandom | base64 | tr -d '\n' | tr '+/' '-_'
}
export SHOWMESH_TEST_FPPMQTT_COLLECTOR_USERNAME="fppmqtt-test-collector"
export SHOWMESH_TEST_FPPMQTT_COLLECTOR_PASSWORD="$(random_password)"
export SHOWMESH_TEST_FPPMQTT_PUBLISHER_USERNAME="fppmqtt-test-publisher"
export SHOWMESH_TEST_FPPMQTT_PUBLISHER_PASSWORD="$(random_password)"

cleanup() {
  docker rm -f "$CONTAINER_NAME" >/dev/null 2>&1 || true
  rm -f "${TMP_SEED_PASSWD:-}" "${TMP_SEED_ACL:-}"
}
trap cleanup EXIT

# In case a previous run was interrupted before its own cleanup ran.
docker rm -f "$CONTAINER_NAME" >/dev/null 2>&1 || true

echo "test-integration-fppmqtt: starting $MOSQUITTO_IMAGE as $CONTAINER_NAME on port $HOST_PORT"
# The shipped mosquitto.conf is mounted read-only and unmodified. Its
# password_file and acl_file are required because allow_anonymous is false,
# so create the container first, copy disposable seed files into its writable
# config directory, then start it. This mirrors test-integration.sh without
# borrowing any production credentials or modifying deploy files.
docker create --name "$CONTAINER_NAME" \
  -p "${HOST_PORT}:1883" \
  -v "$ROOT_DIR/deploy/mosquitto/mosquitto.conf:/mosquitto/config/mosquitto.conf:ro" \
  "$MOSQUITTO_IMAGE" >/dev/null

TMP_SEED_PASSWD="$(mktemp)"
docker run --rm -v "$TMP_SEED_PASSWD:/out/passwd" "$MOSQUITTO_IMAGE" \
  mosquitto_passwd -b -c /out/passwd "$SHOWMESH_TEST_FPPMQTT_COLLECTOR_USERNAME" "$SHOWMESH_TEST_FPPMQTT_COLLECTOR_PASSWORD" >/dev/null
docker run --rm -v "$TMP_SEED_PASSWD:/out/passwd" "$MOSQUITTO_IMAGE" \
  mosquitto_passwd -b /out/passwd "$SHOWMESH_TEST_FPPMQTT_PUBLISHER_USERNAME" "$SHOWMESH_TEST_FPPMQTT_PUBLISHER_PASSWORD" >/dev/null
docker cp "$TMP_SEED_PASSWD" "$CONTAINER_NAME:/mosquitto/config/passwd"
rm -f "$TMP_SEED_PASSWD"
unset TMP_SEED_PASSWD

TMP_SEED_ACL="$(mktemp)"
cp "$ROOT_DIR/deploy/mosquitto/acl.conf" "$TMP_SEED_ACL"
cat >> "$TMP_SEED_ACL" <<EOF

# --- TEST-ONLY FPP MQTT integration principals ---------------------------
# Appended to a disposable copy only. These are intentionally separate:
# one read-only collector and one write-only simulated FPP publisher.
user $SHOWMESH_TEST_FPPMQTT_COLLECTOR_USERNAME
topic read falcon/player/+/fppd_status
topic read falcon/player/+/port_status
topic read falcon/player/+/warnings
topic read falcon/player/+/version
topic read falcon/player/+/branch
topic read falcon/player/+/status
topic read falcon/player/+/ready
topic read falcon/player/+/playlist/repeat/status
topic read falcon/player/+/playlist/position/status

user $SHOWMESH_TEST_FPPMQTT_PUBLISHER_USERNAME
topic write falcon/player/+/fppd_status
topic write falcon/player/+/port_status
topic write falcon/player/+/warnings
topic write falcon/player/+/version
topic write falcon/player/+/branch
topic write falcon/player/+/status
topic write falcon/player/+/ready
topic write falcon/player/+/playlist/repeat/status
topic write falcon/player/+/playlist/position/status
EOF
docker cp "$TMP_SEED_ACL" "$CONTAINER_NAME:$ACL_CONTAINER_PATH"
rm -f "$TMP_SEED_ACL"
unset TMP_SEED_ACL

docker start "$CONTAINER_NAME" >/dev/null

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

# A listening TCP port is not readiness for an authenticated broker. Prove
# both test principals can perform their only permitted operation before Go
# starts: publisher writes a retained FPP status value and collector reads it.
ready_topic="falcon/player/FPP-AuthReady/status"
if ! docker exec "$CONTAINER_NAME" mosquitto_pub -h 127.0.0.1 -p 1883 \
  -u "$SHOWMESH_TEST_FPPMQTT_PUBLISHER_USERNAME" -P "$SHOWMESH_TEST_FPPMQTT_PUBLISHER_PASSWORD" \
  -t "$ready_topic" -m ready -r; then
  echo "test-integration-fppmqtt: authenticated publisher readiness probe failed" >&2
  docker logs "$CONTAINER_NAME" >&2 || true
  exit 1
fi
if ! docker exec "$CONTAINER_NAME" mosquitto_sub -h 127.0.0.1 -p 1883 \
  -u "$SHOWMESH_TEST_FPPMQTT_COLLECTOR_USERNAME" -P "$SHOWMESH_TEST_FPPMQTT_COLLECTOR_PASSWORD" \
  -t "$ready_topic" -C 1 -W 5 >/dev/null; then
  echo "test-integration-fppmqtt: authenticated collector readiness probe failed" >&2
  docker logs "$CONTAINER_NAME" >&2 || true
  exit 1
fi

echo "test-integration-fppmqtt: running against $SHOWMESH_TEST_MQTT_BROKER (container $CONTAINER_NAME)"
if ! go test -tags=integration -race -count=1 -timeout=5m -v ./internal/coordinator/collector/fppmqtt/...; then
  echo "test-integration-fppmqtt: go test failed; dumping $CONTAINER_NAME logs before teardown" >&2
  docker logs "$CONTAINER_NAME" >&2 || true
  exit 1
fi
