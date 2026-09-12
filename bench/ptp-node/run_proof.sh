#!/usr/bin/env bash
# Host-side orchestrator for bench/ptp-node: builds the bench image, starts
# a grandmaster and a follower container on their own docker network, and
# runs in-container-proof.sh in each. See README.md for exactly what this
# does and does not prove.
#
# Usage: run_proof.sh   (run from the repository root)

set -euo pipefail

REPO_ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"
IMAGE=showmesh-bench-ptp-node:dev
NETWORK=showmesh-bench-ptp-net
DOMAIN=44
GM_NAME=showmesh-bench-ptp-gm
FOLLOWER_NAME=showmesh-bench-ptp-follower

cleanup() {
  docker rm -f "$GM_NAME" "$FOLLOWER_NAME" >/dev/null 2>&1 || true
  docker network rm "$NETWORK" >/dev/null 2>&1 || true
}
trap cleanup EXIT

echo "=== building $IMAGE ==="
docker build -t "$IMAGE" "$REPO_ROOT/bench/ptp-node"

cleanup
docker network create "$NETWORK" >/dev/null

echo ""
echo "=== starting grandmaster container ==="
docker run -d --name "$GM_NAME" --network "$NETWORK" --cap-add=SYS_TIME \
  -v "$REPO_ROOT":/repo \
  "$IMAGE" sleep infinity >/dev/null
docker exec "$GM_NAME" /repo/bench/ptp-node/in-container-proof.sh grandmaster "$DOMAIN" \
  > /tmp/showmesh-bench-ptp-gm.log 2>&1 &
GM_PID=$!

# Give the grandmaster a head start so it is already announcing before the
# follower's ptp4l starts listening; not required for correctness (BMCA
# handles either order) but keeps the follower's own settle loop shorter.
sleep 3

echo "=== starting follower container ==="
docker run -d --name "$FOLLOWER_NAME" --network "$NETWORK" --cap-add=SYS_TIME \
  -v "$REPO_ROOT":/repo \
  "$IMAGE" sleep infinity >/dev/null
docker exec "$FOLLOWER_NAME" /repo/bench/ptp-node/in-container-proof.sh follower "$DOMAIN" "$GM_NAME" \
  > /tmp/showmesh-bench-ptp-follower.log 2>&1 &
FOLLOWER_PID=$!

GM_RC=0
FOLLOWER_RC=0
wait "$GM_PID" || GM_RC=$?
wait "$FOLLOWER_PID" || FOLLOWER_RC=$?

echo ""
echo "=== grandmaster container output ==="
cat /tmp/showmesh-bench-ptp-gm.log
echo ""
echo "=== follower container output ==="
cat /tmp/showmesh-bench-ptp-follower.log

echo ""
if [ "$GM_RC" -eq 0 ] && [ "$FOLLOWER_RC" -eq 0 ]; then
  echo "=== run_proof.sh: PASS (grandmaster exit=$GM_RC, follower exit=$FOLLOWER_RC) ==="
  exit 0
else
  echo "=== run_proof.sh: FAIL (grandmaster exit=$GM_RC, follower exit=$FOLLOWER_RC) ==="
  exit 1
fi
