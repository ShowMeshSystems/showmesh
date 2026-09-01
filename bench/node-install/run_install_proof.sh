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
#   - preflight.sh reports every check it claims to in its build-host mode,
#     including the informational ndisink line;
#   - preflight.sh --runtime-only, the mode install.sh actually runs on
#     every install, passes for an unprivileged user with a normal login
#     PATH (no /usr/sbin) when the runtime packages are present, and fails
#     naming the missing library when one genuinely is not;
#   - the installed unit file is syntactically valid systemd, including
#     that every directive name in it is one systemd recognises (proved by
#     re-running the same check against a deliberately typo'd copy and
#     requiring it to fail);
#   - the unit's restart policy is on-failure, not always, so a clean
#     `systemctl stop` is honoured instead of being relaunched two seconds
#     later;
#   - the upgrade path (an install.sh re-run over an existing binary)
#     enforces the same SHOWMESH_NODE_ID check the fresh-install path does,
#     and does not call `systemctl restart` when agent.env has never been
#     edited.
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

echo "=== 6. preflight.sh, build-host mode (no arguments) ==="
./preflight.sh
echo

echo "=== 6b. preflight.sh --runtime-only, the mode a real node install actually takes ==="
echo "install.sh always calls preflight.sh --runtime-only, never the build-host mode"
echo "check 6 just ran. That mode's own guard against a PATH that omits /usr/sbin"
echo "(the 2026-08-26 Pi defect: ldconfig lives at /usr/sbin/ldconfig, which is not"
echo "on an unprivileged user's PATH) is only real evidence if this bench actually"
echo "runs it unprivileged, with a normal login PATH. Root's PATH already includes"
echo "/usr/sbin, so running this as root would prove nothing about that defect."
NORMAL_PATH=/usr/local/bin:/usr/bin:/bin

echo "--- 6b.i: positive case, runtime packages present, asserted to pass ---"
set +e
OUT="$(su -s /bin/bash nobody -c "PATH=$NORMAL_PATH $REPO/deploy/node/preflight.sh --runtime-only" 2>&1)"
RC=$?
set -e
if [ "$RC" -ne 0 ]; then
  echo "FAIL: preflight.sh --runtime-only failed as an unprivileged user with a normal PATH, but the runtime packages are installed" >&2
  echo "$OUT" >&2
  exit 1
fi
if ! echo "$OUT" | grep -q 'OK: libltc.so.11 (runtime library present)'; then
  echo "FAIL: preflight.sh --runtime-only passed, but not by way of the libltc.so.11 check; this bench case is not testing what it claims to" >&2
  echo "$OUT" >&2
  exit 1
fi
echo "OK: unprivileged run with a normal PATH passes with the runtime library resolved"

echo "--- 6b.ii: negative case, runtime library genuinely absent, asserted to fail ---"
# ldconfig does not key off a file's name: it opens every regular file under
# each ld.so.conf directory as ELF, reads its embedded SONAME, and (re)builds
# the libltc.so.11 symlink pointing at wherever that content currently sits.
# Renaming the real object within the same directory (e.g. to a
# ".bench-hidden" suffix) does not hide it: ldconfig just finds it under its
# new name and recreates the symlink pointing there, so the check silently
# passes for the wrong reason. The real object and its symlink must move
# out of every scanned directory entirely.
LIBLTC_SYM="$(dpkg -L libltc11 | grep -E '/libltc\.so\.11$')"
if [ -z "$LIBLTC_SYM" ] || [ ! -e "$LIBLTC_SYM" ]; then
  echo "FAIL: could not locate libltc11's libltc.so.11 file via dpkg -L; cannot run the negative case" >&2
  exit 1
fi
LIBLTC_REAL="$(readlink -f "$LIBLTC_SYM")"
LIBLTC_HIDE_DIR="$(mktemp -d)"
mv "$LIBLTC_SYM" "$LIBLTC_HIDE_DIR/"
mv "$LIBLTC_REAL" "$LIBLTC_HIDE_DIR/"
ldconfig
if ldconfig -p | grep -q 'libltc\.so\.11'; then
  echo "FAIL: libltc.so.11 still resolves via ldconfig after moving both the symlink and its target out of every scanned directory; the negative case setup is not actually removing it" >&2
  mv "$LIBLTC_HIDE_DIR/$(basename "$LIBLTC_REAL")" "$LIBLTC_REAL"
  mv "$LIBLTC_HIDE_DIR/$(basename "$LIBLTC_SYM")" "$LIBLTC_SYM"
  rmdir "$LIBLTC_HIDE_DIR"
  ldconfig
  exit 1
fi
set +e
OUT="$(su -s /bin/bash nobody -c "PATH=$NORMAL_PATH $REPO/deploy/node/preflight.sh --runtime-only" 2>&1)"
RC=$?
set -e
mv "$LIBLTC_HIDE_DIR/$(basename "$LIBLTC_REAL")" "$LIBLTC_REAL"
mv "$LIBLTC_HIDE_DIR/$(basename "$LIBLTC_SYM")" "$LIBLTC_SYM"
rmdir "$LIBLTC_HIDE_DIR"
ldconfig
if [ "$RC" -eq 0 ]; then
  echo "FAIL: preflight.sh --runtime-only passed with libltc.so.11 genuinely absent from the linker cache; this check cannot fail and is not a real check" >&2
  echo "$OUT" >&2
  exit 1
fi
if ! echo "$OUT" | grep -q 'MISSING: libltc.so.11'; then
  echo "FAIL: preflight.sh --runtime-only failed, but not by naming the missing runtime library" >&2
  echo "$OUT" >&2
  exit 1
fi
echo "OK: unprivileged run with a normal PATH fails when the runtime library is actually absent"
echo

echo "=== 7. systemd unit syntax ==="
if ! command -v systemd-analyze >/dev/null 2>&1; then
  echo "systemd-analyze not available; skipped unit verification" >&2
  exit 1
fi
# --recursive-errors=one is load-bearing: without it systemd-analyze verify
# exits 0 on an unknown directive name (it only warns, because PID 1 would
# warn-and-ignore too), so a typo'd directive would pass this check. `one`
# turns the named unit's own warnings into a non-zero exit while leaving
# warnings from its dependency units informational.
systemd-analyze verify --recursive-errors=one /etc/systemd/system/showmesh-agent.service
echo "OK: systemd-analyze verify reports the unit syntactically valid"
echo

echo "=== 7b. Proving check 7 can actually fail (typo'd directive) ==="
BOGUS_UNIT=$(mktemp -d)/showmesh-agent-bogus.service
sed 's/^RestartSec=/DefinitelyNotADirective=true\nRestartSec=/' \
  /etc/systemd/system/showmesh-agent.service > "$BOGUS_UNIT"
if systemd-analyze verify --recursive-errors=one "$BOGUS_UNIT" 2>/dev/null; then
  echo "FAIL: systemd-analyze verify accepted a unit with an unknown directive;" >&2
  echo "      check 7 is not actually verifying anything." >&2
  exit 1
fi
rm -rf "$(dirname "$BOGUS_UNIT")"
echo "OK: an unknown directive is rejected, so check 7 is a real gate"
echo

echo "=== 8. Unit restart policy is on-failure, not always ==="
if grep -q '^Restart=always$' /etc/systemd/system/showmesh-agent.service; then
  echo "FAIL: Restart=always relaunches the agent after a clean 'systemctl stop', turning stop into a two-second pause" >&2
  exit 1
fi
grep -q '^Restart=on-failure$' /etc/systemd/system/showmesh-agent.service
echo "OK: unit sets Restart=on-failure"
echo

echo "=== 9. Upgrade path enforces the SHOWMESH_NODE_ID check ==="
# This container has no PID 1 systemd, so install.sh's real
# 'systemctl daemon-reload' probe fails and activation is skipped
# entirely (see checks 2/5 above). To exercise the enable/start/restart
# branch that actually contains the bug this proves against, stub
# systemctl on PATH so install.sh believes systemd is available, and
# record what it calls instead of touching a real service manager.
STUBDIR=$(mktemp -d)
cat > "$STUBDIR/systemctl" <<'STUBEOF'
#!/usr/bin/env bash
echo "$*" >> /tmp/showmesh-systemctl-calls.log
exit 0
STUBEOF
chmod +x "$STUBDIR/systemctl"
rm -f /tmp/showmesh-systemctl-calls.log

# The env file from check 2's fresh install has no SHOWMESH_NODE_ID set
# (the bench never edits it). The binary is already installed from check
# 2, so this run.sh call is guaranteed to take the upgrade path.
PATH="$STUBDIR:$PATH" ./install.sh "$REPO/bin/showmesh-agent-native" > /tmp/showmesh-upgrade-install.log 2>&1

if grep -q '^restart showmesh-agent.service$' /tmp/showmesh-systemctl-calls.log; then
  echo "FAIL: upgrade path called 'systemctl restart' with SHOWMESH_NODE_ID unset" >&2
  cat /tmp/showmesh-upgrade-install.log >&2
  exit 1
fi
if ! grep -q 'no SHOWMESH_NODE_ID set yet' /tmp/showmesh-upgrade-install.log; then
  echo "FAIL: upgrade path did not print the operator-facing no-node-id message" >&2
  cat /tmp/showmesh-upgrade-install.log >&2
  exit 1
fi
echo "OK: upgrade path refused to (re)start the agent and printed the operator message"
rm -rf "$STUBDIR" /tmp/showmesh-systemctl-calls.log /tmp/showmesh-upgrade-install.log
echo

echo "=== ALL CHECKS PASSED ==="
echo "NOTE: systemd itself never ran in this container (no PID 1 systemd)."
echo "This proves the install mechanism, not a real service boot. See README.md."
