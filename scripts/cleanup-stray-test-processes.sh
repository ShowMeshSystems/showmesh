#!/usr/bin/env bash
#
# Reaps gst-launch-1.0/showmesh-agent/showmesh-coordinator processes left
# orphaned (reparented to pid 1) by a test run that got killed before its own
# cleanup ran -- e.g. `go test -timeout` force-killing a hung test binary, or
# a developer Ctrl-C landing between test steps. test/integration/harness_test.go's
# startAgent now puts the test agent in its own process group and kills the
# whole group on teardown (see its Setpgid comment), which stops the common
# case at the source; this script is the backstop for the case no in-process
# fix can reach: the test binary itself dying before that teardown code runs
# at all.
#
# ppid==1 is what makes a match a stray rather than a live process: every
# ShowMesh process this repo's tooling starts (a running agent's own
# gst-launch-1.0 child, a bench container's fppd, a developer's own agent
# under a shell) has a real, live parent. Once reparented to init, it is
# provably disconnected from whatever started it.
set -euo pipefail

DRY_RUN=0
if [[ "${1:-}" == "--dry-run" ]]; then
  DRY_RUN=1
fi

PATTERN='gst-launch-1.0|showmesh-agent|showmesh-coordinator'

# ps -axo ...: pid, parent pid, elapsed time, full command -- portable across
# the macOS/BSD and Linux ps this project's contributors and CI actually run.
# read into an array (not mapfile: the macOS-shipped /bin/bash is 3.2 and
# doesn't have it) one line at a time so command output with spaces survives.
STRAYS=()
while IFS= read -r line; do
  STRAYS+=("$line")
done < <(ps -axo pid=,ppid=,etime=,command= | awk -v pat="$PATTERN" \
  '$2 == 1 && $0 ~ pat {print}')

if [[ ${#STRAYS[@]} -eq 0 ]]; then
  echo "cleanup-stray-test-processes: no orphaned test processes found"
  exit 0
fi

echo "cleanup-stray-test-processes: found ${#STRAYS[@]} orphaned process(es):"
printf '  %s\n' "${STRAYS[@]}"

if [[ "$DRY_RUN" -eq 1 ]]; then
  echo "cleanup-stray-test-processes: --dry-run, not killing anything"
  exit 0
fi

for line in "${STRAYS[@]}"; do
  pid="${line%% *}"
  # SIGTERM first so a pipeline that can still catch it exits cleanly;
  # SIGKILL only the ones that don't respond, after a short grace period.
  kill -TERM "$pid" 2>/dev/null || true
done

sleep 2

for line in "${STRAYS[@]}"; do
  pid="${line%% *}"
  if kill -0 "$pid" 2>/dev/null; then
    kill -KILL "$pid" 2>/dev/null || true
  fi
done

echo "cleanup-stray-test-processes: done"
