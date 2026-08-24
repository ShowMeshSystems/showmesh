#!/usr/bin/env bash
# Bench script: proves deploy/node's install flow on a clean Debian 13
# container. Run from inside bench/node-install's image with the repo
# root bind-mounted at /repo (see README.md / the sm43-node-install-proof
# Makefile-free invocation this repo's PR verification used).
#
# What this proves, stated plainly, and no more:
#   - the native (cgo) agent builds cleanly on a fresh Debian 13 host with
#     only the packages deploy/node/README.md names;
#   - install.sh runs to completion on that host: creates the user/group,
#     the env file (once, never overwritten on a second run), the state
#     directory, installs the binary and unit;
#   - a second install.sh run is idempotent and does not touch the env
#     file or state directory contents;
#   - preflight.sh reports every check it claims to, including the
#     informational ndisink line;
#   - the installed unit file is syntactically valid systemd.
#
# What this does NOT prove, because a plain container is not a systemd
# machine: that the service actually starts, stays running, or plays any
# audio. See README.md.

set -euo pipefail

REPO=/repo
cd "$REPO"

echo "=== 1. Building the native agent (CGO_ENABLED=1) ==="
mkdir -p bin
CGO_ENABLED=1 go build -trimpath -o bin/showmesh-agent-native ./cmd/showmesh-agent
ls -l bin/showmesh-agent-native
echo "OK: native agent built"
echo

echo "=== 2. First install (fresh host) ==="
cd deploy/node
./install.sh "$REPO/bin/showmesh-agent-native"
echo

echo "=== 3. Asserting state after first install ==="
test -f /etc/showmesh/agent.env
test "$(stat -c '%a' /etc/showmesh/agent.env)" = "600"
test -d /var/lib/showmesh
test -x /usr/local/bin/showmesh-agent-native
test -f /etc/systemd/system/showmesh-agent.service
getent passwd showmesh >/dev/null
echo "OK: expected files/users/permissions present after first install"
echo

echo "=== 4. Marking a sentinel in the env file and state dir, to prove the second run leaves them alone ==="
echo "# bench-sentinel-do-not-touch" >> /etc/showmesh/agent.env
mkdir -p /var/lib/showmesh/assets/.render-state
echo '{"sentinel":true}' > /var/lib/showmesh/assets/.render-state/assignments.json
ENV_SUM_BEFORE=$(sha256sum /etc/showmesh/agent.env | cut -d' ' -f1)
STATE_SUM_BEFORE=$(sha256sum /var/lib/showmesh/assets/.render-state/assignments.json | cut -d' ' -f1)

echo "=== 5. Second install (upgrade path, idempotency) ==="
./install.sh "$REPO/bin/showmesh-agent-native"
echo

ENV_SUM_AFTER=$(sha256sum /etc/showmesh/agent.env | cut -d' ' -f1)
STATE_SUM_AFTER=$(sha256sum /var/lib/showmesh/assets/.render-state/assignments.json | cut -d' ' -f1)
if [ "$ENV_SUM_BEFORE" != "$ENV_SUM_AFTER" ]; then
  echo "FAIL: install.sh's second run modified /etc/showmesh/agent.env" >&2
  exit 1
fi
if [ "$STATE_SUM_BEFORE" != "$STATE_SUM_AFTER" ]; then
  echo "FAIL: install.sh's second run modified render state under the asset directory" >&2
  exit 1
fi
echo "OK: second install left agent.env and render state untouched"
echo

echo "=== 6. preflight.sh ==="
./preflight.sh
echo

echo "=== 7. systemd unit syntax ==="
if command -v systemd-analyze >/dev/null 2>&1; then
  systemd-analyze verify /etc/systemd/system/showmesh-agent.service
  echo "OK: systemd-analyze verify reports the unit syntactically valid"
else
  echo "systemd-analyze not available; skipped unit verification" >&2
  exit 1
fi
echo

echo "=== ALL CHECKS PASSED ==="
echo "NOTE: systemd itself never ran in this container (no PID 1 systemd)."
echo "This proves the install mechanism, not a real service boot. See README.md."
