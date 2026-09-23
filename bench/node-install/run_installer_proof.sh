#!/usr/bin/env bash
# Runs showmesh-install for one node role (render or audio) against the fake enrollment server,
# unattended: a wrong code, an expired code, a first install, then an in-place upgrade.
# Runs inside the node-install bench container with the releases from build_installer_release.sh.
set -uo pipefail

ROLE="${1:?usage: run_installer_proof.sh render|audio RELEASES_DIR}"
RELEASES="${2:?usage: run_installer_proof.sh render|audio RELEASES_DIR}"
NODE_ID="bench-$ROLE"
PORT=18080
URL="http://127.0.0.1:$PORT"
SERVER_LOG=/tmp/fake-coordinator.log
ENV_FILE=/etc/showmesh/agent.env
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

python3 /repo/bench/node-install/fake_enrollment_server.py "$PORT" GOOD-C0DE "$NODE_ID" "$SERVER_LOG" &
for _ in $(seq 1 20); do curl -fsS "$URL/healthz" >/dev/null 2>&1 && break; sleep 0.3; done

extra=()
[ "$ROLE" = "render" ] && extra=(--skip-ndi)

run_install 0.0.0-bench1 /tmp/wrong.log --role "$ROLE" --coordinator "$URL" --code ZZZZ-9999 --yes "${extra[@]}"
check "a wrong code stops the install" "[ $? -ne 0 ]"
check "a wrong code prints the coordinator's reason" "grep -q 'No enrollment code matches' /tmp/wrong.log"
check "a wrong code names the fix" "grep -q 'showmeshctl node enroll' /tmp/wrong.log"

run_install 0.0.0-bench1 /tmp/expired.log --role "$ROLE" --coordinator "$URL" --code EXPD-0000 --yes "${extra[@]}"
check "an expired code stops the install" "[ $? -ne 0 ]"
check "an expired code prints the coordinator's reason" "grep -q 'has expired or was already used' /tmp/expired.log"
check "no enrollment was written after refused codes" "! grep -q '^SHOWMESH_NODE_ID=.' $ENV_FILE 2>/dev/null"

run_install 0.0.0-bench1 /tmp/install.log --role "$ROLE" --coordinator "$URL" --code good-c0de --yes "${extra[@]}"
check "first install succeeds" "[ $? -eq 0 ]"
check "first install saw the node report in" "grep -q 'is online, agent started at' /tmp/install.log"
check "agent.env is mode 600" "[ \"\$(stat -c %a $ENV_FILE)\" = 600 ]"
check "agent.env has the node ID" "grep -qx 'SHOWMESH_NODE_ID=$NODE_ID' $ENV_FILE"
check "agent.env has the broker address" "grep -qx 'SHOWMESH_MQTT_BROKER=tcp://192.0.2.10:1883' $ENV_FILE"
check "agent.env has the broker login" "grep -qx 'SHOWMESH_MQTT_USERNAME=$NODE_ID' $ENV_FILE && grep -q '^SHOWMESH_MQTT_PASSWORD=.' $ENV_FILE"
check "agent.env has the API token" "grep -qx 'SHOWMESH_AGENT_API_TOKEN=bench-api-token-$NODE_ID' $ENV_FILE"
check "agent.env points at the coordinator key" "grep -qx 'SHOWMESH_WEATHERDELAY_COORDINATOR_PUBLIC_KEY_PATH=/etc/showmesh/coordinator-public.key' $ENV_FILE"
check "the coordinator key is written as one base64 line" "[ \"\$(cat /etc/showmesh/coordinator-public.key)\" = AAECAwQFBgcICQoLDA0ODxAREhMUFRYXGBkaGxwdHh8= ]"
check "agent.env keeps the template's other lines" "grep -q '^SHOWMESH_ASSET_DIR=/var/lib/showmesh/assets' $ENV_FILE && grep -q '^# SHOWMESH_NODE_ID identifies' $ENV_FILE"
check "each key appears once" "[ \"\$(grep -c '^SHOWMESH_MQTT_USERNAME=' $ENV_FILE)\" = 1 ]"
check "the agent binary is bench1" "/usr/local/bin/showmesh-agent-native -version | grep -q 0.0.0-bench1"
check "showmesh-install is on PATH" "[ \"\$(readlink /usr/local/bin/showmesh-install)\" = /opt/showmesh/installer/0.0.0-bench1/showmesh-install ]"
check "the code was redeemed once" "[ \"\$(grep -c 'POST /api/v1/node-enrollments/redeem' $SERVER_LOG)\" = 3 ]"
multiarch="$(gcc -dumpmachine | sed 's/-unknown//')"
if [ "$ROLE" = "render" ]; then
  check "the packaged NDI plugin is installed" "[ -f /usr/lib/$multiarch/gstreamer-1.0/libgstndi.so ]"
  check "the skipped NDI runtime names the later command" "grep -q 'showmesh-install --ndi' /tmp/install.log"
else
  check "the unattended audio node skips PTP and names the later command" "grep -q 'install-ptp-audio.sh <interface>' /tmp/install.log"
fi

echo 'SHOWMESH_LOG_LEVEL=debug' >> "$ENV_FILE"
echo sentinel > /var/lib/showmesh/assets/bench-sentinel
env_sum="$(sha256sum "$ENV_FILE")"

run_install 0.0.0-bench2 /tmp/upgrade.log --yes "${extra[@]}"
check "upgrade succeeds with no role, coordinator or code given" "[ $? -eq 0 ]"
check "upgrade keeps the enrollment" "grep -q 'already enrolled as $NODE_ID' /tmp/upgrade.log"
check "upgrade saw the node report in again" "grep -q 'is online, agent started at' /tmp/upgrade.log"
check "upgrade leaves agent.env byte for byte" "[ \"\$(sha256sum $ENV_FILE)\" = \"$env_sum\" ]"
check "upgrade keeps node state" "[ \"\$(cat /var/lib/showmesh/assets/bench-sentinel)\" = sentinel ]"
check "the agent binary is bench2" "/usr/local/bin/showmesh-agent-native -version | grep -q 0.0.0-bench2"
check "the installer records bench2" "grep -qx 'VERSION=0.0.0-bench2' /etc/showmesh/installer.env"
check "upgrade did not redeem again" "[ \"\$(grep -c 'POST /api/v1/node-enrollments/redeem' $SERVER_LOG)\" = 3 ]"
if [ "$ROLE" = "render" ]; then
  check "upgrade keeps the NDI plugin when the new package has none" "grep -q 'kept the plugin already at' /tmp/upgrade.log && [ -f /usr/lib/$multiarch/gstreamer-1.0/libgstndi.so ]"
fi

echo
if [ "$FAILED" -gt 0 ]; then
  echo "run_installer_proof $ROLE: $FAILED check(s) failed"
  exit 1
fi
echo "run_installer_proof $ROLE: all checks passed"
