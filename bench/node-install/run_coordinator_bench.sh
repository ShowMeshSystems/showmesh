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
in_box 'apt-get update -qq >/dev/null && DEBIAN_FRONTEND=noninteractive apt-get install -y -qq --no-install-recommends docker.io docker-cli docker-compose ca-certificates curl sqlite3 python3 >/dev/null' || exit 1
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

VOL=/var/lib/docker/volumes/showmesh_showmesh-data/_data
DB="$(in_box "ls $VOL/*.db | head -n 1")"
in_box "sqlite3 $DB \"PRAGMA busy_timeout=5000; INSERT INTO nodes(node_id, agent_version, hello_observed_at, hello_provenance, first_seen_at, updated_at) VALUES ('bench-old', '0.0.0-bench1', '2026-09-01T00:00:00.000000000Z', 'agent_report', '2026-09-01T00:00:00.000000000Z', '2026-09-01T00:00:00.000000000Z'), ('bench-cur', '0.0.0-bench2', '2026-09-01T00:00:00.000000000Z', 'agent_report', '2026-09-01T00:00:00.000000000Z', '2026-09-01T00:00:00.000000000Z');\"" >/dev/null
check "the bench seeded two node rows" "[ \"\$(in_box \"sqlite3 $DB 'SELECT count(*) FROM nodes'\")\" = 2 ]"

# A night is running: the upgrade must refuse and change nothing.
in_box 'cp /etc/showmesh/showmeshctl.env /root/ctl.env.orig
  (python3 /repo/bench/node-install/fake_night_server.py 18081 live >/dev/null 2>&1 &)
  sleep 1
  sed -i "s#^SHOWMESH_SERVER=.*#SHOWMESH_SERVER=http://127.0.0.1:18081#" /etc/showmesh/showmeshctl.env'
run_install 0.0.0-bench2 --yes
check "an upgrade during a night is refused" "[ $? -ne 0 ]"
check "the refusal names the night session and its state" "in_box 'grep -q \"A night session is running (state: live)\" /tmp/install-0.0.0-bench2.log'"
check "the refusal gives the showmeshctl command that ends the night" "in_box 'grep -q \"showmeshctl night power-down-presentation\" /tmp/install-0.0.0-bench2.log'"
check "the refusal changed nothing: no backup, still on bench1" "in_box '[ ! -d /var/backups/showmesh ] || [ -z \"\$(ls /var/backups/showmesh)\" ]; grep -qx SHOWMESH_RELEASE_VERSION=0.0.0-bench1 /opt/showmesh/coordinator/.env'"
check "the coordinator is still running after the refusal" "in_box 'curl -fsS http://127.0.0.1:8080/healthz >/dev/null'"
in_box 'cp /root/ctl.env.orig /etc/showmesh/showmeshctl.env
  sed -i "s#^SHOWMESH_SERVER=.*#SHOWMESH_SERVER=http://127.0.0.1:1#" /etc/showmesh/showmeshctl.env'
run_install 0.0.0-bench2 --yes
check "a coordinator that does not answer refuses the upgrade" "[ $? -ne 0 ] && in_box 'grep -q \"cannot tell whether a night is running\" /tmp/install-0.0.0-bench2.log'"
in_box 'cp /root/ctl.env.orig /etc/showmesh/showmeshctl.env'

run_install 0.0.0-bench2 --yes
check "upgrade succeeds with no options" "[ $? -eq 0 ]"
check "upgrade runs the bench2 images" "in_box 'cd /opt/showmesh/coordinator && docker compose -f docker-compose.yml -f docker-compose.published.yml ps --format \"{{.Image}}\" | grep -c 0.0.0-bench2 | grep -qx 2'"
check "upgrade keeps the administrator token" "in_box 'grep -q \"showmeshctl already signs in as an administrator\" /tmp/install-0.0.0-bench2.log'"
check "upgrade keeps the broker login" "[ \"\$(in_box 'grep ^SHOWMESH_MQTT_PASSWORD= /opt/showmesh/coordinator/.env')\" = \"$pw_before\" ]"
check "the address given on the first install survives a rerun without it" "in_box 'grep -qx SHOWMESH_PUBLIC_URL=http://192.0.2.44:8080 /opt/showmesh/coordinator/.env'"
check "no world-readable secret after the upgrade either" "in_box '$no_readable_secret'"
check "upgrade saw the broker accept the login again" "in_box 'grep -q \"ok: the broker accepts the coordinator.s login\" /tmp/install-0.0.0-bench2.log'"

check "the upgrade made a dated backup named for the old version" "in_box 'ls -d /var/backups/showmesh/*-0.0.0-bench1 | grep -q .'"
check "the upgrade printed where the backup is and how to restore it" "in_box 'grep -q \"backup saved in /var/backups/showmesh/\" /tmp/install-0.0.0-bench2.log && grep -q \"RESTORE.txt\" /tmp/install-0.0.0-bench2.log'"
check "the backup holds the settings and the broker files" "in_box 'b=\$(ls -d /var/backups/showmesh/*-0.0.0-bench1 | tail -n 1); grep -qx SHOWMESH_RELEASE_VERSION=0.0.0-bench1 \$b/coordinator.env && [ -f \$b/mosquitto/passwd ]'"
# Restore the backup into a scratch volume and read the seeded rows back.
in_box 'b=$(ls -d /var/backups/showmesh/*-0.0.0-bench1 | tail -n 1)
  docker run --rm -v inst-scratch:/data -v $b:/backup:ro --entrypoint sh eclipse-mosquitto:2.0.22 -c "find /data -mindepth 1 -delete && cp -a /backup/data/. /data/"'
check "the backup restores into a scratch volume and the seeded nodes read back" "in_box 'sqlite3 /var/lib/docker/volumes/inst-scratch/_data/\$(basename $DB) \"SELECT group_concat(node_id) FROM (SELECT node_id FROM nodes ORDER BY node_id)\" | grep -qx bench-cur,bench-old'"
check "the restored database passes an integrity check" "in_box '[ \"\$(sqlite3 /var/lib/docker/volumes/inst-scratch/_data/\$(basename $DB) \"PRAGMA integrity_check\")\" = ok ]'"
check "the upgrade prints the command for a node on the old version" "in_box 'grep -q \"bench-old (0.0.0-bench1): curl -fsSL file:///repo/dist/inst-bench/0.0.0-bench2/get-showmesh.sh | sudo bash -s -- --yes\" /tmp/install-0.0.0-bench2.log'"
check "a node already on the new version is listed as current" "in_box 'grep -q \"Already on 0.0.0-bench2: bench-cur\" /tmp/install-0.0.0-bench2.log && ! grep -q \"bench-cur (\" /tmp/install-0.0.0-bench2.log'"

# --force upgrades through a running night with a one-line warning.
in_box 'sed -i "s#^SHOWMESH_SERVER=.*#SHOWMESH_SERVER=http://127.0.0.1:18081#" /etc/showmesh/showmeshctl.env'
run_install 0.0.0-bench2 --yes --force
check "--force upgrades through a running night" "[ $? -eq 0 ]"
check "--force prints a warning naming the night" "in_box 'grep -q \"warning: A night session is running (state: live).*Continuing because --force was given\" /tmp/install-0.0.0-bench2.log'"
in_box 'cp /root/ctl.env.orig /etc/showmesh/showmeshctl.env'
in_box 'ls -d /var/backups/showmesh/*/ | wc -l | grep -qx 2' && echo "two backups after two upgrades"
in_box 'for i in 1 2 3 4 5 6 7; do mkdir /var/backups/showmesh/2000010${i}T000000Z-old; done'
run_install 0.0.0-bench2 --yes
check "only the newest five backups are kept" "in_box '[ \$(ls /var/backups/showmesh | wc -l) -eq 5 ]'"

echo
if [ "$FAILED" -gt 0 ]; then
  echo "run_coordinator_bench: $FAILED check(s) failed"
  exit 1
fi
echo "run_coordinator_bench: all checks passed"
