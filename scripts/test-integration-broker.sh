#!/usr/bin/env bash
#
# Runs the `integration`-tagged Go test suite in internal/coordinator/broker
# against real, throwaway Mosquitto containers. Backs `make
# test-integration-broker`.
#
# Review finding 5 on commit 9dcab74: this package's own retained-message
# trap (RetainAsPublished/RETAIN handling — see response.go's package doc
# comment and response_integration_test.go's TestIntegrationRetainedResponseDoesNotConfirm
# and TestIntegrationLiveAnswerPublishedWithRetainTrueStillConfirms) and its
# reconnect-resubscribe behavior (TestIntegrationReconnectResubscribesResponseWaiterTopic)
# are proved only against a real broker, and until this script and its
# Makefile/CI wiring existed, nothing ever invoked them: not `make check`,
# not any of the other `test-integration-*` scripts, not CI. A `go test
# -tags=integration` command sitting in a doc comment is not the same thing
# as a command anyone actually runs.
#
# Unlike test-integration.sh / test-integration-fpp.sh /
# test-integration-fppmqtt.sh, this script does NOT start a broker itself:
# internal/coordinator/broker/response_integration_test.go's own
# startTestMosquitto (and the reconnect test's equivalent inline logic)
# start and tear down a fresh, throwaway Mosquitto container per test, via
# `docker` directly, and skip (not fail) if docker is unavailable — see that
# file's requireDocker. This script's job is narrower: prove docker actually
# is available before handing off to `go test`, so a laptop or CI runner
# missing docker fails loudly here rather than the suite quietly skipping
# everything and this script still reporting success.
set -euo pipefail

ROOT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
cd "$ROOT_DIR"

if ! command -v docker >/dev/null 2>&1; then
  echo "test-integration-broker: docker not found on PATH" >&2
  exit 1
fi
if ! docker info >/dev/null 2>&1; then
  echo "test-integration-broker: docker daemon not reachable" >&2
  exit 1
fi

echo "test-integration-broker: running against throwaway Mosquitto containers this suite manages itself"
TEST_LOG="$(mktemp)"
set +e
go test -tags=integration -race -count=1 -timeout=10m -v ./internal/coordinator/broker/... | tee "$TEST_LOG"
TEST_STATUS="${PIPESTATUS[0]}"
set -e

# Same silent-skip guard test-integration-fpp.sh uses (LESSONS.md: "a test
# that guards on a dependency must fail, not skip, when a harness whose
# whole job is to supply that dependency is what invoked it" — this
# script's own job is to prove docker is present before go test ever runs,
# so a zero-match run means something upstream of the tests themselves (a
# build tag typo, an empty package match) ate the suite silently, and that
# must fail loudly rather than exit 0).
RAN_COUNT="$(grep -c '^=== RUN' "$TEST_LOG" || true)"
rm -f "$TEST_LOG"
if [ "$RAN_COUNT" -eq 0 ]; then
  echo "test-integration-broker: matched ZERO tests — this is the silent-skip shape LESSONS.md already recorded once; treating a no-op match as a hard failure rather than a quiet, misleadingly-green exit 0" >&2
  exit 1
fi

if [ "$TEST_STATUS" -ne 0 ]; then
  exit "$TEST_STATUS"
fi
echo "test-integration-broker: ran $RAN_COUNT top-level/subtest cases"
