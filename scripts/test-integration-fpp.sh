#!/usr/bin/env bash
#
# Starts (if not already running) the bench/fpp-multisync fpp-master
# container — a real fppd, not a fake — and runs the `integration`-tagged
# suite in internal/coordinator/collector/fpp against it. Backs `make
# test-integration-fpp`. Mirrors test-integration.sh's shape (same build
# tag convention, same "never fail for want of the dependency" discipline
# in the Go tests themselves) with one deliberate difference: this script
# does NOT tear the fpp-master container down afterward.
#
# That asymmetry with test-integration.sh's throwaway Mosquitto is
# intentional, not an oversight: bench/fpp-multisync/docker-compose.yml
# builds fppd from FalconChristmas/fpp source (README.md: "a full source
# build"), so destroying and rebuilding it on every run would turn a
# multi-second test into a multi-minute one for no benefit — the container
# is meant to be left running across a session the way the rest of this
# session's work already found it. Run `docker compose -f
# bench/fpp-multisync/docker-compose.yml down` yourself when you are done
# with the bench for the session.
#
# Step 3 review finding 4.2 added a second thing this script runs:
# test/integration's TestFPPSuccessPathThroughRealCoordinator, which points
# a real showmesh-coordinator SUBPROCESS at this same live fppd and proves
# the FPP success path end to end (collector -> sink -> store -> apiwiring
# -> mapping -> JSON), not merely through the collector package alone. That
# test lives in test/integration, which needs an MQTT broker to start a
# coordinator at all — this script now also starts one, throwaway, exactly
# like test-integration.sh's own Mosquitto (same pinned image, same shipped
# config, a different container name and port so the two scripts never
# collide if run at the same time), and tears THAT one down on exit. The
# fpp-master asymmetry above is unchanged: only the broker this script adds
# is throwaway.
#
# SHOWMESH_FPP_TEST_PREBUILT=1 layers docker-compose.prebuilt.yml on top of
# the base compose file, so fpp-master comes from CI's pinned GHCR image
# instead of a source build. That mode recreates the bench container from
# that image, replacing a source-built one if it is running. Unset (the
# default) this script's behavior is unchanged.
set -euo pipefail

ROOT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
cd "$ROOT_DIR"

# This script's entire job is to supply the Go suites' dependencies (a live
# fpp-master, a throwaway broker). SHOWMESH_REQUIRE_TEST_DEPS turns every
# dependency skip in those suites (requireLiveFPP, requireBroker) into a
# hard test failure instead — a missing dependency under THIS script means
# the script itself failed to supply it, which must never read as a quiet,
# green skip (docs/build/LESSONS.md: "a test that guards on a dependency
# must fail, not skip, when a harness whose whole job is to supply that
# dependency is what invoked it"). Run by hand with this unset, the skip
# stays the convenient default an unprepared laptop gets.
# This script starts BOTH a bench fppd and a throwaway broker, so it is
# answerable for both: a skip for either is a hard failure here.
export SHOWMESH_REQUIRE_TEST_DEPS=broker,fpp

COMPOSE_FILE="bench/fpp-multisync/docker-compose.yml"
FPP_URL="${SHOWMESH_TEST_FPP_URL:-http://localhost:8090}"

COMPOSE_ARGS=(-f "$COMPOSE_FILE")
if [ "${SHOWMESH_FPP_TEST_PREBUILT:-}" = "1" ]; then
  PREBUILT_OVERRIDE="$ROOT_DIR/bench/fpp-multisync/docker-compose.prebuilt.yml"
  COMPOSE_ARGS+=(-f "$PREBUILT_OVERRIDE")
  export SHOWMESH_TEST_FPP_COMPOSE_OVERRIDE="$PREBUILT_OVERRIDE"
fi

# docker-compose.yml no longer pins a fixed container_name for fpp-master
# (see its own comment: a fixed name collides across the side-by-side
# 9.5.3/10.0 projects README.md describes), so the actual container name
# now depends on the compose project name. This script never passes -p, so
# it always resolves within compose's own default project for this file,
# the same single instance the CI workflow and every previous run of this
# script already shared. container_id resolves it by service name instead
# of by a literal string; fpp_master_container is a fixed label for log
# messages only, not something passed to docker.
fpp_master_container="fpp-master"
container_id() {
  docker compose "${COMPOSE_ARGS[@]}" ps -a -q fpp-master
}

# In prebuilt mode the container is recreated whether or not one is already
# running: an already-running container may have been source-built, and the
# destructive test's own --force-recreate would then swap it for the pinned
# image mid-suite. Recreating up front makes provenance unambiguous, and it
# is why prebuilt mode replaces whatever bench container is running.
if [ "${SHOWMESH_FPP_TEST_PREBUILT:-}" = "1" ]; then
  echo "test-integration-fpp: prebuilt mode; recreating $fpp_master_container from the pinned image"
  docker compose "${COMPOSE_ARGS[@]}" up -d --force-recreate fpp-master
else
  echo "test-integration-fpp: checking for a running $fpp_master_container"
  if [ -z "$(docker compose "${COMPOSE_ARGS[@]}" ps -q fpp-master)" ]; then
    # --force-recreate, not a plain `up -d`: verified live, this image
    # crash-loops (an apache/php pid file left in the container's own
    # writable layer trips up its entrypoint on a second start of the SAME
    # container) if it is merely started/recreated-in-place after having
    # been stopped rather than removed. --force-recreate discards that
    # writable-layer state while keeping the already-built image (no
    # rebuild) and the named media volume (so a previously-set
    # MultiSyncEnabled value survives). See
    # internal/coordinator/collector/fpp/integration_test.go's cleanup for
    # the same finding, hit the same way, during this collector's own
    # development.
    echo "test-integration-fpp: not running; starting it (a first build is a full FPP source build and can take several minutes)"
    docker compose "${COMPOSE_ARGS[@]}" up -d --force-recreate fpp-master
  fi
fi

CONTAINER_ID="$(container_id)"

# Prebuilt mode asserts what is RUNNING, not what is configured: a dropped
# or misspelled SHOWMESH_FPP_TEST_PREBUILT would otherwise source-build and
# still report green.
if [ "${SHOWMESH_FPP_TEST_PREBUILT:-}" = "1" ]; then
  pinned_ref="$(docker compose "${COMPOSE_ARGS[@]}" config --images fpp-master)"
  pinned_id="$(docker image inspect -f '{{.Id}}' "$pinned_ref")"
  running_id="$(docker inspect -f '{{.Image}}' "$CONTAINER_ID")"
  if [ "$running_id" != "$pinned_id" ]; then
    echo "test-integration-fpp: $fpp_master_container is running image $running_id, not the pinned $pinned_ref ($pinned_id)" >&2
    exit 1
  fi
  echo "test-integration-fpp: $fpp_master_container confirmed running the pinned image $pinned_ref"
fi

echo "test-integration-fpp: waiting for $FPP_URL to answer /api/fppd/status"
ready=0
for _ in $(seq 1 60); do
  # -f (--fail): a non-2xx response makes curl exit nonzero instead of
  # exiting 0 on ANY HTTP response. Without it, a wedged fppd whose web
  # server still answers with a 404 (or any other non-2xx) on every path
  # made this loop declare readiness immediately — proven by pointing this
  # at a server answering 404 on every path and watching the script print
  # its normal progress, skip the main test, and exit 0.
  if curl -fsS -o /dev/null --max-time 2 "${FPP_URL}/api/fppd/status"; then
    ready=1
    break
  fi
  sleep 1
done
if [ "$ready" -ne 1 ]; then
  echo "test-integration-fpp: ${FPP_URL}/api/fppd/status did not answer within 60s" >&2
  docker logs "$CONTAINER_ID" --tail 50 >&2 || true
  exit 1
fi

# --- Seed the playlists the primitive-command suite starts by name -------
#
# fpp_command_primitives_test.go starts two playlists by name,
# "showmesh-test" and "showmesh-bench-3item", neither of which anything in
# this repo creates: both were written by hand into this container's media
# volume during the Step 8 capture, so this developer's long-lived
# container has always had them while a fresh container (every CI run
# since 2026-08-14) does not, and every startPlaylist-dependent case fails
# there with fpp.status idle instead of playing. Both playlists are
# pause-only main-playlist items, so they need no media file underneath
# them. Seeded idempotently: a file already present is left untouched, so
# this warm local container's own copies (and any hand-edits made to them)
# are never overwritten and the block is a no-op on every re-run here.
# Skipped entirely when SHOWMESH_TEST_FPP_URL was set by the caller, for
# the same reason the export a few lines down is conditional: a set
# SHOWMESH_TEST_FPP_URL means an operator pointed this script at something
# other than our own bench container, and this script never writes to
# that.
if [ -z "${SHOWMESH_TEST_FPP_URL:-}" ]; then
  seed_playlist() {
    local name="$1" json="$2" path="/home/fpp/media/playlists/${1}.json"
    if docker exec "$CONTAINER_ID" test -f "$path"; then
      return 0
    fi
    echo "test-integration-fpp: seeding playlist ${name}.json (absent in $fpp_master_container)"
    docker exec "$CONTAINER_ID" mkdir -p /home/fpp/media/playlists
    docker exec -i "$CONTAINER_ID" sh -c "cat > '$path'" <<EOF
$json
EOF
  }

  seed_playlist "showmesh-test" '{
    "name": "showmesh-test",
    "leadIn": [],
    "mainPlaylist": [
        {"type": "pause", "duration": 120, "enabled": 1}
    ],
    "leadOut": [],
    "playlistInfo": {"leadIn_items": 0, "leadIn_duration": 0, "mainPlaylist_items": 1, "mainPlaylist_duration": 120, "leadOut_items": 0, "leadOut_duration": 0, "total_items": 1, "total_duration": 120},
    "repeat": 0,
    "loopCount": 0,
    "empty": false,
    "desc": "showmesh bench test playlist",
    "version": 4
}'

  seed_playlist "showmesh-bench-3item" '{
    "name": "showmesh-bench-3item",
    "desc": "Step 8 capture: 3 pause items",
    "version": 4,
    "repeat": 0,
    "loopCount": 0,
    "leadIn": [],
    "leadOut": [],
    "mainPlaylist": [
        {"type": "pause", "duration": 60, "enabled": 1},
        {"type": "pause", "duration": 60, "enabled": 1},
        {"type": "pause", "duration": 60, "enabled": 1}
    ],
    "playlistInfo": {"leadIn_items": 0, "leadIn_duration": 0, "mainPlaylist_items": 3, "mainPlaylist_duration": 180, "leadOut_items": 0, "leadOut_duration": 0, "total_items": 3, "total_duration": 180}
}'
fi

echo "test-integration-fpp: running against $FPP_URL (container $fpp_master_container)"
# Only exported when the caller actually overrode it: the Go suite's
# destructive container-recreate test (integration_test.go) treats a SET
# SHOWMESH_TEST_FPP_URL as "an operator pointed this at something other
# than our own bench container, never touch it" and skips itself — setting
# it here unconditionally, even to the same default value, would trip that
# guard and silently skip the one test that most needs to run.
if [ -n "${SHOWMESH_TEST_FPP_URL:-}" ]; then
  export SHOWMESH_TEST_FPP_URL="$FPP_URL"
fi
go test -tags=integration -race -count=1 -timeout=5m -v ./internal/coordinator/collector/fpp/...

# --- test/integration's FPP-through-the-coordinator test -----------------
#
# Pinned to the exact eclipse-mosquitto version and shipped config
# test-integration.sh uses, so this throwaway broker is not a bespoke
# stand-in — see that script's own comments for why that matters. A
# different container name and host port than test-integration.sh's own
# defaults, so the two scripts can run concurrently (a developer running
# `make test-integration` in one terminal and `make test-integration-fpp`
# in another) without colliding.
MOSQUITTO_IMAGE="eclipse-mosquitto:2.0.22"
FPP_MOSQUITTO_CONTAINER="${SHOWMESH_TEST_FPP_MOSQUITTO_CONTAINER:-showmesh-test-mosquitto-fpp}"
FPP_MOSQUITTO_PORT="${SHOWMESH_TEST_FPP_MQTT_PORT:-11893}"

FPP_BIN_DIR="$(mktemp -d)"

cleanup_fpp_mosquitto() {
  docker rm -f "$FPP_MOSQUITTO_CONTAINER" >/dev/null 2>&1 || true
  rm -rf "$FPP_BIN_DIR"
}
trap cleanup_fpp_mosquitto EXIT

# In case a previous run of this script was interrupted before its own
# cleanup ran. Only the container: calling cleanup_fpp_mosquitto here
# would delete the bin dir this run just created, leaving the builds below
# to recreate it with different permissions than mktemp chose.
docker rm -f "$FPP_MOSQUITTO_CONTAINER" >/dev/null 2>&1 || true

# Built here rather than left to test/integration/harness_test.go's own
# TestMain, for the identical reason test-integration.sh now does this:
# the -timeout below covers everything TestMain does, and a cold-cache
# `go build` for these three binaries competes with the tests for that
# budget. CGO_ENABLED is pinned per binary rather than inherited, matching
# buildEnvWithCGO's own reasoning in harness_test.go: ADR-042 requires the
# agent link the real cgo GStreamer/libltc engine, and ADR-012 requires the
# coordinator (and showmeshctl, which needs no cgo either) build CGo-free.
echo "test-integration-fpp: prebuilding showmesh-agent, showmesh-coordinator, and showmeshctl"
CGO_ENABLED=1 go build -o "$FPP_BIN_DIR/showmesh-agent" ./cmd/showmesh-agent
CGO_ENABLED=0 go build -o "$FPP_BIN_DIR/showmesh-coordinator" ./cmd/showmesh-coordinator
CGO_ENABLED=0 go build -o "$FPP_BIN_DIR/showmeshctl" ./cmd/showmeshctl
export SHOWMESH_TEST_AGENT_BIN="$FPP_BIN_DIR/showmesh-agent"
export SHOWMESH_TEST_COORDINATOR_BIN="$FPP_BIN_DIR/showmesh-coordinator"
export SHOWMESH_TEST_SHOWMESHCTL_BIN="$FPP_BIN_DIR/showmeshctl"

# ADR-024 decision 10 (Step 6): the shipped mosquitto.conf sets
# allow_anonymous false and requires both password_file and acl_file to
# exist before it will even start — mosquitto refuses to start at all
# without them (see test-integration.sh's own comment on the identical
# requirement, and deploy/README.md's "generate-credentials.sh is not
# optional" note). This script's own broker predates that decision (it
# was added under Step 3, before Step 6 landed) and was never updated to
# seed either file, which this seam found by actually running this
# script rather than by reading it: the container exited immediately on
# every run ("Unable to open pwfile"), the wait loop below silently timed
# out, and TestFPPSuccessPathThroughRealCoordinator skipped itself via its
# own "no MQTT broker reachable" guard — never failing, so nothing before
# this fix ever reported the gap. Fixed the same way test-integration.sh
# already does it: create (not run) the container, seed password_file
# with the coordinator credential test/integration/harness_test.go reads
# (envTestMQTTCoordinatorUsername/Password) and acl_file from the
# committed deploy/mosquitto/acl.conf verbatim (this script needs no
# per-node or burst-publisher credential: the FPP e2e test launches only
# a coordinator subprocess, never an agent), then start it.
random_password() {
  head -c 24 /dev/urandom | base64 | tr -d '\n' | tr '+/' '-_'
}
export SHOWMESH_TEST_MQTT_COORDINATOR_USERNAME="coordinator"
export SHOWMESH_TEST_MQTT_COORDINATOR_PASSWORD="$(random_password)"

echo "test-integration-fpp: starting $MOSQUITTO_IMAGE as $FPP_MOSQUITTO_CONTAINER on port $FPP_MOSQUITTO_PORT (for TestFPPSuccessPathThroughRealCoordinator's coordinator subprocess)"
docker create --name "$FPP_MOSQUITTO_CONTAINER" \
  -p "${FPP_MOSQUITTO_PORT}:1883" \
  -v "$ROOT_DIR/deploy/mosquitto/mosquitto.conf:/mosquitto/config/mosquitto.conf:ro" \
  "$MOSQUITTO_IMAGE" >/dev/null

TMP_FPP_SEED_PASSWD="$(mktemp)"
docker run --rm -v "$TMP_FPP_SEED_PASSWD:/out/passwd" "$MOSQUITTO_IMAGE" \
  mosquitto_passwd -b -c /out/passwd "$SHOWMESH_TEST_MQTT_COORDINATOR_USERNAME" "$SHOWMESH_TEST_MQTT_COORDINATOR_PASSWORD" >/dev/null
docker cp "$TMP_FPP_SEED_PASSWD" "$FPP_MOSQUITTO_CONTAINER:/mosquitto/config/passwd"
rm -f "$TMP_FPP_SEED_PASSWD"
docker cp "$ROOT_DIR/deploy/mosquitto/acl.conf" "$FPP_MOSQUITTO_CONTAINER:/mosquitto/config/acl.generated.conf"

docker start "$FPP_MOSQUITTO_CONTAINER" >/dev/null

echo "test-integration-fpp: waiting for the broker to accept connections"
broker_ready=0
for _ in $(seq 1 30); do
  if (exec 3<>"/dev/tcp/localhost/${FPP_MOSQUITTO_PORT}") 2>/dev/null; then
    exec 3<&- 3>&- || true
    broker_ready=1
    break
  fi
  sleep 1
done
if [ "$broker_ready" -ne 1 ]; then
  echo "test-integration-fpp: mosquitto did not start listening on port ${FPP_MOSQUITTO_PORT} within 30s" >&2
  docker logs "$FPP_MOSQUITTO_CONTAINER" >&2 || true
  exit 1
fi


# Step 7 seam C review: this filter used to name only
# TestFPPSuccessPathThroughRealCoordinator, so when the review's own
# fpp_command_test.go added TestFPPCommandAgainstRealCoordinatorAndBenchFPP
# and TestCLIStopPlaylistTimeoutSurvivesServerConfirmDeadline (the write
# path's own "verified against the running stack" acceptance criterion),
# neither was reachable from this script — the exact defect this project
# already shipped once, recorded in docs/build/LESSONS.md as "a test can
# report success while running at all": a script's -run pattern silently
# excluding a real test is invisible in green CI output, because
# `go test -run <pattern>` exits 0 when a pattern matches nothing, the
# identical shape as a skip nobody reads. Broadened to an alternation
# naming all three FPP-write tests explicitly (never a bare wildcard,
# which would silently start matching an unrelated future test this
# script was never meant to gate), and RAN_COUNT below turns "matched
# nothing" from a silent, exit-0 no-op into a loud failure.
#
# Step 8 extended this the identical way: fpp_command_test.go gained one
# more real-stack test (the parameterized-command replay/conflict
# criterion), and fpp_command_primitives_test.go — new in this step —
# gained eight, one per BUILD-PLAN acceptance criterion for the primitive
# command vocabulary (docs/bench/fpp-command-vocabulary.md section 4).
# Named explicitly here for the identical reason the seven above already
# are.
#
# Step 9's macro_run_submit_timeout_test.go is fppd-dependent too and is
# named here because this script is the only harness that supplies an
# fppd: left out, the workflow's path filter would trigger a run that
# never executes the file that triggered it.
FPP_RUN_PATTERN='^(TestFPPSuccessPathThroughRealCoordinator|TestFPPCommandAgainstRealCoordinatorAndBenchFPP|TestCLIStopPlaylistTimeoutSurvivesServerConfirmDeadline|TestFPPCommandReplayOnParameterizedCommandDispatchesNothingAuditsAsReplayAndRefusesParamConflict|TestRemainingFPPPrimitivesConfirmAgainstBenchFPP|TestStopPlaylistGracefullyConfirmsWhileShowStillRunning|TestNextPlaylistItemAtLastItemEndsPlaylistAndConfirms|TestUnconfirmedFPPCommandReportsStatedReasonNeverSuccessful|TestStartAndStopPlaylistConfirmOnlyOnPostDispatchEvidenceTimed|TestIfBusyRefusesReplacesAndAllowsSamePlaylist|TestFPPCommandParamsAbsentNullEmptyDistinctionEndToEnd|TestCollectorReadOnlyPostureUnchangedByCommandSurface|TestCLIMacroRunSubmitTimeoutFloorCoversRealSubmissionLatency)$'

# Step 8's own additions each make at least one real, unshrunk wait against
# the bench fppd's actual behavior (a confirmation deadline running to its
# full 20s default, an FPP collector poll cadence up to 15s, several
# primitives dispatched in sequence each paying that cost) — this task's
# own standing rule against racing a kernel or shrinking a timeout to make
# a test pass faster. -timeout is widened accordingly; 5m covered the
# three pre-Step-8 tests comfortably but would not cover this file's own
# worst case.
echo "test-integration-fpp: running $FPP_RUN_PATTERN against $FPP_URL and tcp://localhost:${FPP_MOSQUITTO_PORT}"
FPP_TEST_LOG="$(mktemp)"
set +e
SHOWMESH_TEST_MQTT_BROKER="tcp://localhost:${FPP_MOSQUITTO_PORT}" \
  go test -tags=integration -race -count=1 -timeout=20m -v ./test/integration/... -run "$FPP_RUN_PATTERN" | tee "$FPP_TEST_LOG"
FPP_TEST_STATUS="${PIPESTATUS[0]}"
set -e

# The silent-zero guard itself: a pattern that matched nothing produces NO
# "=== RUN" lines at all and `go test` still exits 0 — this is what turns
# that into a loud failure instead of a green run that proved nothing.
RAN_COUNT="$(grep -c '^=== RUN' "$FPP_TEST_LOG" || true)"
rm -f "$FPP_TEST_LOG"
if [ "$RAN_COUNT" -eq 0 ]; then
  echo "test-integration-fpp: -run '$FPP_RUN_PATTERN' matched ZERO tests — this is the silent-skip shape LESSONS.md " \
       "already recorded once; treating a no-op match as a hard failure rather than a quiet, misleadingly-green exit 0" >&2
  exit 1
fi
if [ "$FPP_TEST_STATUS" -ne 0 ]; then
  exit "$FPP_TEST_STATUS"
fi
echo "test-integration-fpp: ran $RAN_COUNT top-level/subtest cases matching '$FPP_RUN_PATTERN'"
