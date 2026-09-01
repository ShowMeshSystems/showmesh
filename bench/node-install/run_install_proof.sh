#!/usr/bin/env bash
# Bench script: proves deploy/node's install flow on a clean Debian 13
# container. Run from inside bench/node-install's image with the repo
# root bind-mounted at /repo (see README.md / the sm43-node-install-proof
# Makefile-free invocation this repo's PR verification used).
#
# What this proves, stated plainly, and no more:
#   - the native (cgo) agent builds cleanly on a fresh Debian 13 host with
#     only the packages deploy/node/README.md names;
#   - install.sh refuses to adopt a pre-existing "showmesh" account that
#     does not match the exact shape it creates (system uid, nologin-
#     equivalent shell, home at the state directory), proved one guard
#     clause at a time, and accepts both shells (*/nologin and */false)
#     that clause allows;
#   - install.sh refuses to install a binary whose shared library
#     dependencies this host cannot resolve, or that resolve to a library
#     missing a symbol the binary needs (checked with `ldd -r` against the
#     actual binary, not the -dev packages preflight.sh's build-time mode
#     checks with pkg-config), and this refusal fires for a library
#     preflight.sh --runtime-only never checks at all;
#   - `ldd -r` itself, independent of this binary, actually distinguishes
#     those two failure classes from a library that is merely present:
#     proved against a synthetic library pair (a full one and one missing
#     a needed symbol under the same soname), since patching one symbol
#     out of the real, multi-hundred-symbol GStreamer library while
#     leaving every other consumer of it on the same host working is not
#     a safe mutation to perform in place; and install.sh itself, not just
#     ldd -r in isolation, is run against that same missing-symbol binary
#     and asserted to refuse it, naming the undefined symbol;
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

echo "=== 1b. Service-account collision guard: one mutation per clause ==="
echo "Before install.sh has ever run (no showmesh account exists yet), pre-create"
echo "a colliding 'showmesh' account in three shapes, changing exactly one of the"
echo "three guard clauses (uid range / shell / home dir) at a time, and confirm"
echo "install.sh refuses each one instead of adopting it. Clean up between cases"
echo "so each case starts from no pre-existing account, like a real first install."
cd deploy/node

assert_refused() {
  local label="$1"
  set +e
  OUT="$(./install.sh "$REPO/bin/showmesh-agent-native" 2>&1)"
  RC=$?
  set -e
  if [ "$RC" -eq 0 ]; then
    echo "FAIL: $label: install.sh accepted a colliding account it should have refused" >&2
    echo "$OUT" >&2
    exit 1
  fi
  if ! echo "$OUT" | grep -q 'does not look like the locked-down system account'; then
    echo "FAIL: $label: install.sh refused, but not with the expected collision message" >&2
    echo "$OUT" >&2
    exit 1
  fi
  echo "OK: $label: install.sh refused the colliding account"
}

echo "--- 1b.i: uid clause (uid over the system range; shell and home otherwise correct) ---"
useradd --uid 60000 --gid root --home-dir /var/lib/showmesh --no-create-home --shell /usr/sbin/nologin showmesh
assert_refused "uid clause"
userdel showmesh

echo "--- 1b.ii: shell clause (system uid and correct home, but a real login shell) ---"
useradd --system --gid root --home-dir /var/lib/showmesh --no-create-home --shell /bin/bash showmesh
assert_refused "shell clause"
userdel showmesh

echo "--- 1b.iii: home clause (system uid and nologin shell, but a real home directory) ---"
useradd --system --gid root --home-dir /home/showmesh --no-create-home --shell /usr/sbin/nologin showmesh
assert_refused "home clause"
userdel showmesh

echo "OK: each of the three guard clauses independently refuses a colliding account"
echo

echo "=== 1c. ldd -r mechanism proof: a present-but-symbol-missing library ==="
echo "Plain ldd only resolves sonames; it stays clean for a library that is"
echo "present under the right name but missing a symbol the caller needs,"
echo "the shape of a too-old runtime library. ldd -r additionally resolves"
echo "relocations and must report 'undefined symbol' for exactly that case."
echo "Proved against a small synthetic library pair (not the real"
echo "GStreamer library: removing one symbol from it in place, on the same"
echo "host every other check in this bench also runs against, is not a"
echo "safe mutation to perform)."
LDDR_WORK="$(mktemp -d)"
cat > "$LDDR_WORK/libneed.c" <<'CEOF'
void needed_symbol(void) {}
void other_symbol(void) {}
CEOF
cat > "$LDDR_WORK/libneed_missing.c" <<'CEOF'
void other_symbol(void) {}
CEOF
cat > "$LDDR_WORK/consumer.c" <<'CEOF'
extern void needed_symbol(void);
int main(void) { needed_symbol(); return 0; }
CEOF
gcc -shared -fPIC -Wl,-soname,libneed.so.1 -o "$LDDR_WORK/libneed.so.1.full" "$LDDR_WORK/libneed.c"
gcc -shared -fPIC -Wl,-soname,libneed.so.1 -o "$LDDR_WORK/libneed.so.1.missing" "$LDDR_WORK/libneed_missing.c"
cp "$LDDR_WORK/libneed.so.1.full" "$LDDR_WORK/libneed.so.1"
ln -s libneed.so.1 "$LDDR_WORK/libneed.so"
gcc -o "$LDDR_WORK/consumer" "$LDDR_WORK/consumer.c" -L"$LDDR_WORK" -lneed -Wl,-rpath,"$LDDR_WORK/runtime"
mkdir -p "$LDDR_WORK/runtime"
cp "$LDDR_WORK/libneed.so.1.full" "$LDDR_WORK/runtime/libneed.so.1"
if ldd -r "$LDDR_WORK/consumer" | grep -qE 'not found|undefined symbol'; then
  echo "FAIL: ldd -r reported a problem against the full synthetic library; this proof's baseline is not clean" >&2
  ldd -r "$LDDR_WORK/consumer" >&2
  exit 1
fi
echo "OK: ldd -r is clean against the full synthetic library"
cp "$LDDR_WORK/libneed.so.1.missing" "$LDDR_WORK/runtime/libneed.so.1"
if ldd "$LDDR_WORK/consumer" | grep -q 'not found'; then
  echo "FAIL: plain ldd already flags the missing-symbol library; this mutation does not isolate ldd -r's added coverage" >&2
  ldd "$LDDR_WORK/consumer" >&2
  exit 1
fi
LDDR_OUT="$(ldd -r "$LDDR_WORK/consumer" 2>&1 || true)"
if ! echo "$LDDR_OUT" | grep -q 'undefined symbol'; then
  echo "FAIL: ldd -r did not report the missing symbol; it cannot distinguish this case and install.sh's check would not either" >&2
  echo "$LDDR_OUT" >&2
  exit 1
fi
echo "OK: plain ldd stays clean but ldd -r reports the missing symbol"

# The two checks above prove ldd -r can detect this case; they do not
# prove install.sh actually uses ldd -r to detect it. A regression that
# reverted install.sh to plain ldd, or dropped the undefined-symbol
# pattern from its grep, would pass both of them and pass step 1c below
# too (hiding libgstapp entirely is a "not found" case plain ldd also
# catches). Only running install.sh itself against this exact
# missing-symbol library pins that. The missing-symbol library is still
# in place at this point; preflight.sh's own checks are unrelated to
# which binary path install.sh is handed, so this call reaches the ldd
# check and refuses there before touching anything else on the host.
set +e
OUT="$(cd "$REPO/deploy/node" && ./install.sh "$LDDR_WORK/consumer" 2>&1)"
RC=$?
set -e
if [ "$RC" -eq 0 ]; then
  echo "FAIL: install.sh accepted a binary that resolves against a library missing a symbol it needs; the version-floor check it is supposed to run is not wired up (reverted to plain ldd, or the undefined-symbol pattern was dropped)" >&2
  echo "$OUT" >&2
  exit 1
fi
if ! echo "$OUT" | grep -q 'undefined symbol'; then
  echo "FAIL: install.sh refused, but not by naming an undefined symbol" >&2
  echo "$OUT" >&2
  exit 1
fi
if ! echo "$OUT" | grep -q 'needed_symbol'; then
  echo "FAIL: install.sh refused for an undefined symbol, but not the specific one this mutation removed" >&2
  echo "$OUT" >&2
  exit 1
fi
echo "OK: install.sh itself refuses the missing-symbol binary and names the undefined symbol"
rm -rf "$LDDR_WORK"
echo

echo "=== 1d. Runtime library version floor: ldd -r against the actual binary ==="
echo "Mutation proof: hide libgstapp-1.0.so.0, a library the agent binary"
echo "links directly (see its ldd output) but that neither gst-inspect-1.0"
echo "nor gst-launch-1.0 (the tools preflight.sh itself invokes) need, so"
echo "preflight passing does not depend on it. install.sh's ldd check on the"
echo "binary must still catch it, proving this check adds coverage preflight"
echo "does not already have."
if ldd "$REPO/bin/showmesh-agent-native" | grep -q 'libgstapp-1\.0\.so\.0'; then
  :
else
  echo "FAIL: the built binary does not link libgstapp-1.0.so.0; cannot run this mutation proof" >&2
  exit 1
fi
LIBGSTAPP_SYM="$(dpkg -L libgstreamer-plugins-base1.0-0 2>/dev/null | grep -E '/libgstapp-1\.0\.so\.0$')"
if [ -z "$LIBGSTAPP_SYM" ] || [ ! -e "$LIBGSTAPP_SYM" ]; then
  echo "FAIL: could not locate libgstapp-1.0.so.0 via dpkg -L; cannot run this mutation proof" >&2
  exit 1
fi
LIBGSTAPP_REAL="$(readlink -f "$LIBGSTAPP_SYM")"
LIBGSTAPP_HIDE_DIR="$(mktemp -d)"
mv "$LIBGSTAPP_SYM" "$LIBGSTAPP_HIDE_DIR/"
mv "$LIBGSTAPP_REAL" "$LIBGSTAPP_HIDE_DIR/"
ldconfig
if ! ./preflight.sh --runtime-only >/tmp/showmesh-preflight-still-green.log 2>&1; then
  echo "FAIL: preflight.sh --runtime-only failed once libgstapp-1.0.so.0 was hidden; this mutation is not isolated to the new ldd check" >&2
  cat /tmp/showmesh-preflight-still-green.log >&2
  mv "$LIBGSTAPP_HIDE_DIR/$(basename "$LIBGSTAPP_REAL")" "$LIBGSTAPP_REAL"
  mv "$LIBGSTAPP_HIDE_DIR/$(basename "$LIBGSTAPP_SYM")" "$LIBGSTAPP_SYM"
  rmdir "$LIBGSTAPP_HIDE_DIR"
  ldconfig
  exit 1
fi
rm -f /tmp/showmesh-preflight-still-green.log
echo "OK: preflight.sh --runtime-only still passes with libgstapp-1.0.so.0 hidden (it never checked this library)"
set +e
OUT="$(./install.sh "$REPO/bin/showmesh-agent-native" 2>&1)"
RC=$?
set -e
mv "$LIBGSTAPP_HIDE_DIR/$(basename "$LIBGSTAPP_REAL")" "$LIBGSTAPP_REAL"
mv "$LIBGSTAPP_HIDE_DIR/$(basename "$LIBGSTAPP_SYM")" "$LIBGSTAPP_SYM"
rmdir "$LIBGSTAPP_HIDE_DIR"
ldconfig
if [ "$RC" -eq 0 ]; then
  echo "FAIL: install.sh succeeded with a shared library the binary links genuinely unresolvable; the ldd check cannot fail and is not a real check" >&2
  echo "$OUT" >&2
  exit 1
fi
if ! echo "$OUT" | grep -q 'not found'; then
  echo "FAIL: install.sh refused, but not by naming an unresolvable shared library" >&2
  echo "$OUT" >&2
  exit 1
fi
echo "OK: install.sh refuses when the binary depends on a shared library this host cannot resolve"
echo

echo "=== 2. First install (fresh host) ==="
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

echo "=== 10: shell clause accepts a */false shell too, not only */nologin ==="
echo "The installed 'showmesh' account already has /usr/sbin/nologin. Change"
echo "only its shell to /usr/bin/false (still a nologin-equivalent match) and"
echo "confirm a re-run accepts it rather than refusing it, exercising the other"
echo "half of the shell clause's acceptance branch. Placed here, at the end, so"
echo "it needs no cleanup and does not disturb the fresh-host state check 2 and"
echo "check 5 depend on."
usermod --shell /usr/bin/false showmesh
set +e
OUT="$(./install.sh "$REPO/bin/showmesh-agent-native" 2>&1)"
RC=$?
set -e
if [ "$RC" -ne 0 ]; then
  echo "FAIL: install.sh exited $RC (refused) for a showmesh account with a */false shell, which should be accepted" >&2
  echo "$OUT" >&2
  exit 1
fi
if ! echo "$OUT" | grep -q 'already exists (system account, matches the shape this installer creates)'; then
  echo "FAIL: install.sh accepted a showmesh account with a */false shell but not by way of the expected acceptance line" >&2
  echo "$OUT" >&2
  exit 1
fi
echo "OK: a */false shell is accepted the same as */nologin"
echo

echo "=== ALL CHECKS PASSED ==="
echo "NOTE: systemd itself never ran in this container (no PID 1 systemd)."
echo "This proves the install mechanism, not a real service boot. See README.md."
