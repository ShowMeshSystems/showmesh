#!/usr/bin/env bash
# Proves Ctrl-C during run_step stops the step's command in both layouts: a harness runs a
# 30-second step, SIGINT goes to its process group, and the step's command must be gone.
set -uo pipefail

REPO="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"
WORK="$(mktemp -d)"
trap 'rm -rf "$WORK"' EXIT
set -m
FAILED=0

check_layout() {
  local layout="$1" pidfile="$WORK/child-$1" harness rc child
  SHOWMESH_INSTALL_LOG="$WORK/install-$layout.log" bash -c '
    . "$1/deploy/install/lib/common.sh"
    UI_LAYOUT=$2; UI_COLS=80
    run_step "sleeping" "$3.out" sh -c "echo \$\$ > $3; exec sleep 30"
    echo "run_step returned" >&2
    exit 0' harness "$REPO" "$layout" "$pidfile" >/dev/null 2>&1 &
  harness=$!
  for _ in $(seq 50); do [ -s "$pidfile" ] && break; sleep 0.1; done
  child="$(cat "$pidfile" 2>/dev/null)"
  if [ -z "$child" ]; then echo "FAIL: layout $layout: the step never started"; FAILED=1; return; fi
  kill -INT -- "-$harness"
  rc=0
  wait "$harness" || rc=$?
  sleep 0.3
  if [ "$rc" -ne 130 ]; then echo "FAIL: layout $layout: the installer exited $rc, not 130"; FAILED=1; fi
  if kill -0 "$child" 2>/dev/null; then
    echo "FAIL: layout $layout: the step's command $child is still running after Ctrl-C"
    kill "$child" 2>/dev/null
    FAILED=1
  else
    echo "PASS: layout $layout: Ctrl-C stopped the step's command and the installer exited 130"
  fi
}

check_layout 1
check_layout 0
exit "$FAILED"
