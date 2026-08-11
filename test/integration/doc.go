//go:build integration

// Package integration holds Step 2 round 2 Task E's integration test suite:
// the machine-checked proof, against a real Mosquitto broker and a real
// showmesh-agent subprocess, of the three acceptance criteria in
// docs/build/BUILD-PLAN.md's Step 2 entry, plus the retained-message
// freshness rule the Step 2 round 2 shared design contract calls out as
// mattering more than any one of them:
//
//  1. An agent appears in coordinator inventory after start
//     (TestAgentAppearsInInventoryWithCapabilities).
//  2. The agent disappears into unknown/offline after an unclean kill, via
//     Last Will (TestAgentUncleanKillGoesOffline).
//  3. The coordinator restores state from retained topics after its own
//     restart (TestCoordinatorRestartRestoresInventoryFromRetainedTopics).
//
// Plus, not on that list but the reason this package exists at all: a dead
// node's retained heartbeat must never replay into a fresh coordinator
// subscription and read as healthy
// (TestRetainedHeartbeatReplayNeverReadsHealthy), a clean shutdown must go
// offline promptly rather than waiting out the staleness window
// (TestAgentCleanShutdownGoesOfflinePromptly), and a restarted broker's
// subscriptions must actually come back
// (TestBrokerRestartResubscribesAndObservesSubsequentChanges).
//
// # Why this cannot be a unit test
//
// Every test above turns on the MQTT RETAIN flag on an inbound publish:
// live (RETAIN=0) is proof of life, retained-store replay (RETAIN=1) is
// not (see internal/coordinator/broker.Message's doc comment and
// internal/coordinator/inventory's classify). A unit test can only ever
// set that flag itself, on a broker.Message it constructs by hand — which
// proves the coordinator's classification logic is internally consistent,
// but proves nothing about whether a real broker actually sets the flag
// the way this whole design assumes it does. Only a real Mosquitto
// connection can prove that. Criterion 2 has the same shape one level
// down: an unclean kill's Last Will is registered on the MQTT CONNECT
// packet by the client library, and only SIGKILLing a real process proves
// it fires; an in-process fake that "simulates" a disconnect never
// actually registered a Will with anything.
//
// # What runs as what
//
// The agent runs as a real subprocess: TestMain builds the
// cmd/showmesh-agent binary once and every test execs it (see
// buildAgentBinary and startAgent). This is not incidental — see above.
//
// The coordinator side is wired in-process, directly from
// internal/coordinator/{store,inventory,broker}, rather than exec'ing the
// showmesh-coordinator binary (see startCoordinator). This is a
// deliberate, spec-directed difference from the shipped binary, recorded
// here rather than assumed away: there is no read API to observe the
// coordinator process through before Step 3 lands one, so this package
// exercises the coordinator's *components* wired together the same way
// internal/coordinator.Run wires them, not the shipped binary's process
// boundary, CLI flags, or config-loading path. A "coordinator restart" in
// this package is therefore a teardown and rebuild of those components
// against the same SQLite file and the same broker, not a process restart.
//
// # What this proves and what it does not
//
// These tests establish behavior against Mosquitto on a developer machine
// or a CI runner. They say nothing about real show hardware, and nothing
// here raises any research record above L1 (see CLAUDE.md's evidence
// ladder). RES-009 failure testing is expected to reuse this harness for
// exactly that reason.
//
// # Running
//
// `make test-integration` starts a pinned eclipse-mosquitto container from
// the shipped deploy/mosquitto/mosquitto.conf, runs this package, and
// tears the broker down; see scripts/test-integration.sh. Running `go test
// -tags=integration ./test/integration/...` directly against a broker you
// started yourself also works — see envBrokerURL's doc comment in
// harness.go for the environment variables involved — and every test in
// this package skips cleanly, rather than failing, when no broker is
// reachable at the configured address.
package integration
