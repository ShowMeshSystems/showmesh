#!/usr/bin/env bash
# Host-side driver: proves deploy/install/lib/firewall.sh against every firewall precondition it
# detects, one throwaway container per case. ufw is proved end to end through the real installer
# (run_firewall_ufw_proof.sh); firewalld, a raw nftables ruleset with a drop policy, no firewall,
# --firewall and --no-firewall are proved by sourcing lib/firewall.sh directly
# (run_firewall_lib_proof.sh), since they need nothing else showmesh-install itself would
# exercise. Needs run_installer_bench.sh's releases in dist/inst-bench. See README.md.
set -uo pipefail

REPO="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"
IMAGE=inst-node-install:dev
STATUS=0

[ -f "$REPO/dist/inst-bench/0.0.0-bench1/get-showmesh.sh" ] || { echo "run run_installer_bench.sh first" >&2; exit 2; }

echo
echo "=== ufw, through the real installer ==="
docker run --rm --cap-add=NET_ADMIN --name inst-fw-ufw -v "$REPO":/repo "$IMAGE" \
  -c "bash /repo/bench/node-install/run_firewall_ufw_proof.sh /repo/dist/inst-bench" || STATUS=1

run_lib_case() {
  local case="$1" caps="$2"
  echo
  echo "=== $case ==="
  # shellcheck disable=SC2086
  docker run --rm $caps --name "inst-fw-$case" -v "$REPO":/repo:ro debian:13 \
    bash -c "apt-get update -qq >/dev/null 2>&1 && bash /repo/bench/node-install/run_firewall_lib_proof.sh $case" || STATUS=1
}

run_lib_case firewalld        "--cap-add=NET_ADMIN --cap-add=NET_RAW"
run_lib_case nftables-drop    "--cap-add=NET_ADMIN"
run_lib_case nftables-forward-drop "--cap-add=NET_ADMIN"
run_lib_case ssh-port         "--cap-add=NET_ADMIN"
run_lib_case none             ""
run_lib_case firewall-flag    "--privileged"
run_lib_case no-firewall-flag "--cap-add=NET_ADMIN"

exit "$STATUS"
