#!/usr/bin/env bash
# Host-side driver: runs showmesh-install --role coordinator for real inside a privileged
# debian:13 container with its own dockerd, using locally built coordinator and UI images
# tagged as the fake releases 0.0.0-bench1 and 0.0.0-bench2. Needs run_installer_bench.sh's
# releases in dist/inst-bench. Publishes no host ports and mounts no host Docker socket.
set -uo pipefail

REPO="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"
NAME=inst-coord
VOLUME=inst-coord-docker
IMAGES="$REPO/dist/inst-bench/images.tar"
COORD_IMAGE=ghcr.io/showmeshsystems/showmesh-coordinator
UI_IMAGE=ghcr.io/showmeshsystems/showmesh-ui
FAILED=0

check() {
  if eval "$2"; then echo "PASS: $1"; else echo "FAIL: $1"; FAILED=$((FAILED + 1)); fi
}
in_box() { docker exec -i "$NAME" bash -c "$1"; }
cleanup() { docker rm -f "$NAME" >/dev/null 2>&1; docker volume rm "$VOLUME" >/dev/null 2>&1; }
[ -n "${INST_KEEP:-}" ] || trap cleanup EXIT

[ -f "$REPO/dist/inst-bench/0.0.0-bench1/get-showmesh.sh" ] || { echo "run run_installer_bench.sh first" >&2; exit 2; }

echo "=== building coordinator and UI images as 0.0.0-bench1 ==="
docker build -q --build-arg VERSION=0.0.0-bench1 --build-arg COMMIT=bench --build-arg BUILD_DATE=2026-09-23T00:00:00Z \
  -t "$COORD_IMAGE:0.0.0-bench1" "$REPO" >/dev/null || exit 1
docker build -q -t "$UI_IMAGE:0.0.0-bench1" "$REPO/ui" >/dev/null || exit 1
docker save -o "$IMAGES" "$COORD_IMAGE:0.0.0-bench1" "$UI_IMAGE:0.0.0-bench1" eclipse-mosquitto:2.0.22 || exit 1

cleanup
docker run -d --privileged --name "$NAME" --hostname inst-coord -v "$REPO":/repo:ro -v "$VOLUME":/var/lib/docker \
  debian:13 sleep infinity >/dev/null || exit 1

echo "=== starting dockerd inside the bench container ==="
in_box 'apt-get update -qq >/dev/null && DEBIAN_FRONTEND=noninteractive apt-get install -y -qq --no-install-recommends docker.io docker-cli docker-compose ca-certificates curl >/dev/null' || exit 1
# shellcheck disable=SC2016
in_box '(dockerd >/var/log/dockerd.log 2>&1 &); for _ in $(seq 1 30); do docker info >/dev/null 2>&1 && exit 0; sleep 1; done; tail -n 20 /var/log/dockerd.log; exit 1' || {
  echo "dockerd did not start inside the bench container"; exit 1; }
in_box "docker load -q -i /repo/dist/inst-bench/images.tar && docker tag $COORD_IMAGE:0.0.0-bench1 $COORD_IMAGE:0.0.0-bench2 && docker tag $UI_IMAGE:0.0.0-bench1 $UI_IMAGE:0.0.0-bench2" || exit 1
in_box 'printf "bench-password\n" > /root/admin-password'

run_install() {
  local v="$1"
  shift
  echo
  echo "--- showmesh-install $v $* ---"
  in_box "cat /repo/dist/inst-bench/$v/get-showmesh.sh | SHOWMESH_RELEASE_BASE=file:///repo/dist/inst-bench/$v bash -s -- $* 2>&1 | tee /tmp/install-$v.log; exit \${PIPESTATUS[1]}"
}

run_install 0.0.0-bench1 --role coordinator --yes --admin-name bench-admin --admin-password-file /root/admin-password --address 192.0.2.44
check "coordinator install succeeds" "[ $? -eq 0 ]"
check "the API answers" "in_box 'grep -q \"ok: the coordinator answers on port 8080\" /tmp/install-0.0.0-bench1.log'"
check "the broker accepts the coordinator's login" "in_box 'grep -q \"ok: the broker accepts the coordinator.s login\" /tmp/install-0.0.0-bench1.log'"
check "Docker Compose is at least 2.24" "in_box 'grep -Eq \"with Compose 2\\.(2[4-9]|[3-9][0-9])\" /tmp/install-0.0.0-bench1.log'"
check ".env carries the release and broker settings" "in_box 'cd /opt/showmesh/coordinator && grep -qx SHOWMESH_RELEASE_VERSION=0.0.0-bench1 .env && grep -qx SHOWMESH_BROKER_MODE=builtin .env && grep -q ^SHOWMESH_NODE_BROKER_URL=tcp://.*:1883\$ .env && grep -q ^SHOWMESH_PUBLIC_URL=http://.*:8080\$ .env && [ \$(stat -c %a .env) = 600 ]'"
check "the broker files belong to the coordinator's uid" "in_box '[ \$(stat -c %u:%g:%a /opt/showmesh/coordinator/mosquitto/passwd) = 65532:1883:640 ] && [ \$(stat -c %a /opt/showmesh/coordinator/mosquitto) = 2755 ]'"
check "showmeshctl signs in as the administrator" "in_box 'showmeshctl principal list | grep -q bench-admin'"
check "the coordinator is announced as _showmesh._tcp" "in_box 'grep -q _showmesh._tcp /etc/avahi/services/showmesh.service && grep -q \"<port>8080</port>\" /etc/avahi/services/showmesh.service'"
check "the given address is what nodes are told" "in_box 'grep -qx SHOWMESH_PUBLIC_URL=http://192.0.2.44:8080 /opt/showmesh/coordinator/.env && grep -qx SHOWMESH_NODE_BROKER_URL=tcp://192.0.2.44:1883 /opt/showmesh/coordinator/.env'"
# shellcheck disable=SC2016
no_readable_secret='for f in /opt/showmesh/coordinator/.env /etc/showmesh/showmeshctl.env; do sed -n "s/^SHOWMESH_\(MQTT_PASSWORD\|CTL_TOKEN\)=//p" "$f"; done | grep . > /tmp/secrets
  hits="$(find /etc/showmesh /opt/showmesh/coordinator -type f -perm -o=r -exec grep -lF -f /tmp/secrets {} + 2>/dev/null)"
  [ -s /tmp/secrets ] && [ -z "$hits" ] || { echo "world-readable: $hits"; false; }'
check "no world-readable file under /etc/showmesh or /opt/showmesh/coordinator holds a secret" "in_box '$no_readable_secret'"
check "the installer left nothing in /tmp named for ShowMesh" "in_box '! ls /tmp/showmesh-* >/dev/null 2>&1'"
pw_before="$(in_box 'grep ^SHOWMESH_MQTT_PASSWORD= /opt/showmesh/coordinator/.env')"

run_install 0.0.0-bench2 --yes
check "upgrade succeeds with no options" "[ $? -eq 0 ]"
check "upgrade runs the bench2 images" "in_box 'cd /opt/showmesh/coordinator && docker compose -f docker-compose.yml -f docker-compose.published.yml ps --format \"{{.Image}}\" | grep -c 0.0.0-bench2 | grep -qx 2'"
check "upgrade keeps the administrator token" "in_box 'grep -q \"showmeshctl already signs in as an administrator\" /tmp/install-0.0.0-bench2.log'"
check "upgrade keeps the broker login" "[ \"\$(in_box 'grep ^SHOWMESH_MQTT_PASSWORD= /opt/showmesh/coordinator/.env')\" = \"$pw_before\" ]"
check "the address given on the first install survives a rerun without it" "in_box 'grep -qx SHOWMESH_PUBLIC_URL=http://192.0.2.44:8080 /opt/showmesh/coordinator/.env'"
check "no world-readable secret after the upgrade either" "in_box '$no_readable_secret'"
check "upgrade saw the broker accept the login again" "in_box 'grep -q \"ok: the broker accepts the coordinator.s login\" /tmp/install-0.0.0-bench2.log'"

echo
if [ "$FAILED" -gt 0 ]; then
  echo "run_coordinator_bench: $FAILED check(s) failed"
  exit 1
fi
echo "run_coordinator_bench: all checks passed"
