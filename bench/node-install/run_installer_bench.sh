#!/usr/bin/env bash
# Host-side driver: builds the bench image and two fake releases, then proves the
# render node and audio node installs in two fresh containers. See README.md.
set -euo pipefail

REPO="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"
IMAGE=inst-node-install:dev
OUT=/repo/dist/inst-bench
CACHE=(-v inst-bench-gomod:/root/go/pkg/mod -v inst-bench-gocache:/root/.cache/go-build)

docker build -q -t "$IMAGE" "$REPO/bench/node-install" >/dev/null
docker run --rm --name inst-bench-build -v "$REPO":/repo "${CACHE[@]}" "$IMAGE" \
  -c "bash /repo/bench/node-install/build_installer_release.sh $OUT"

status=0
for role in render audio; do
  echo
  echo "=== $role node ==="
  docker run --rm --name "inst-bench-$role" -v "$REPO":/repo "$IMAGE" \
    -c "bash /repo/bench/node-install/run_installer_proof.sh $role $OUT" || status=1
done
exit "$status"
