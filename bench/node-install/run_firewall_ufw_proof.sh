#!/usr/bin/env bash
# Proves showmesh-install's firewall hook against a real ufw, through the real installer: a
# first render-node install opens every port the role needs, each rule commented as ShowMesh's,
# and an in-place upgrade adds nothing twice. Runs inside the node-install bench container with
# the releases from build_installer_release.sh (run_installer_bench.sh builds them) and needs
# NET_ADMIN so ufw can touch this container's own netfilter tables.
set -uo pipefail

RELEASES="${1:?usage: run_firewall_ufw_proof.sh RELEASES_DIR}"
NODE_ID=bench-fw-render
PORT=18090
URL="http://127.0.0.1:$PORT"
SERVER_LOG=/tmp/fake-coordinator.log
FAILED=0

check() {
  if eval "$2"; then echo "PASS: $1"; else echo "FAIL: $1"; FAILED=$((FAILED + 1)); fi
}

# run_install VERSION LOG ARGS... runs the release's bootstrap the way curl | sudo bash does.
run_install() {
  local v="$1" log="$2"
  shift 2
  echo
  echo "--- showmesh-install $v $* ---"
  cat "$RELEASES/$v/get-showmesh.sh" | SHOWMESH_RELEASE_BASE="file://$RELEASES/$v" SHOWMESH_REPORT_TIMEOUT=30 \
    bash -s -- "$@" > "$log" 2>&1
  local rc=$?
  cat "$log"
  echo "--- exit $rc ---"
  return "$rc"
}

apt-get update -qq >/dev/null && apt-get install -y -qq --no-install-recommends ufw >/dev/null 2>&1
ufw --force enable >/dev/null

python3 /repo/bench/node-install/fake_enrollment_server.py "$PORT" GOOD-C0DE "$NODE_ID" "$SERVER_LOG" &
for _ in $(seq 1 20); do curl -fsS "$URL/healthz" >/dev/null 2>&1 && break; sleep 0.3; done

run_install 0.0.0-bench1 /tmp/install.log --role render --coordinator "$URL" --code good-c0de --yes --skip-ndi
check "the render node installs" "[ $? -eq 0 ]"
check "ShowMesh's ports are opened in ufw" "grep -q \"Opening ShowMesh's ports in ufw\" /tmp/install.log"
for p in 'tcp 80' 'udp 32320' 'udp 319' 'udp 320' 'udp 5004' 'udp 9875' 'udp 5353' 'tcp 5959-5999' 'udp 5959-5999'; do
  check "the installer opened $p" "grep -qF \"ok: $p (\" /tmp/install.log"
done
check "ufw carries every rule, each commented as ShowMesh's" "[ \"\$(ufw status | grep -c 'ShowMesh:')\" = 18 ]"
rules_before="$(ufw status numbered)"

run_install 0.0.0-bench1 /tmp/upgrade.log --yes
check "the upgrade succeeds" "[ $? -eq 0 ]"
check "the upgrade reopens the same ports" "grep -q \"Opening ShowMesh's ports in ufw\" /tmp/upgrade.log"
check "a re-run adds nothing twice" "[ \"\$(ufw status numbered)\" = \"$rules_before\" ]"

echo
if [ "$FAILED" -gt 0 ]; then
  echo "run_firewall_ufw_proof: $FAILED check(s) failed"
  exit 1
fi
echo "run_firewall_ufw_proof: all checks passed"
