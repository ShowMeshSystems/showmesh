//go:build integration

// Package integration is the machine-checked proof, against a real
// Mosquitto broker, a real showmesh-agent subprocess, a real
// showmesh-coordinator subprocess, and (for two tests) a real showmeshctl
// subprocess, of:
//
//   - BUILD-PLAN.md's Step 2 acceptance criteria: an agent appears in
//     coordinator inventory after start
//     (TestAgentAppearsInInventoryWithCapabilities); the agent disappears
//     into unknown/offline after an unclean kill, via Last Will
//     (TestAgentUncleanKillGoesOffline); the coordinator restores state
//     from retained topics after its own restart
//     (TestCoordinatorRestartRestoresInventoryFromRetainedTopics).
//   - BUILD-PLAN.md's Step 3 acceptance criteria: the versioned control API
//     is exercised end to end by a real, independent, non-UI client
//     (TestShowmeshctlSubcommandsAgainstRealCoordinator); an interrupted
//     change stream is followed by an authoritative snapshot re-fetch, not
//     a resumed local model, for each of the three interruption shapes that
//     fail differently — the connection dropping
//     (TestWatchResnapshotsAfterConnectionDrop), the coordinator restarting
//     underneath a connected client
//     (TestWatchResnapshotsAfterCoordinatorRestart), and a stream.reset
//     from subscriber buffer overflow (TestWatchResnapshotsAfterStreamReset).
//
// Plus, not on either list but the reason this package's Step 2 half exists
// at all: a dead node's retained heartbeat must never replay into a fresh
// coordinator subscription and read as healthy
// (TestRetainedHeartbeatReplayNeverReadsHealthy), a clean shutdown must go
// offline promptly rather than waiting out the staleness window
// (TestAgentCleanShutdownGoesOfflinePromptly), and a restarted broker's
// subscriptions must actually come back
// (TestBrokerRestartResubscribesAndObservesSubsequentChanges). The Step 3
// half adds: a retained heartbeat renders on the wire as observedAt: null
// and state: "unknown_age", asserted on raw JSON bytes, never on a
// re-decoded struct (assertRawHeartbeatIsUnknownAge, in restart_test.go);
// SHOWMESH_API_CLOSE_READS with a real per-principal bearer token (minted
// via the ADR-024 decision 9 host-level create-admin/issue-token
// subcommands) is enforced end to end including on the stream
// (TestAPICloseReadsEnforcedWhenSet, TestAPIStreamOpensSuccessfullyWithCloseReadsEnabled
// in api_test.go — this package's ADR-024 replacement for the retired
// ADR-021 shared SHOWMESH_API_TOKEN secret, which a coordinator carrying
// ADR-024 now refuses to start with set at all); version negotiation,
// event-history gaplessness, and a slow SSE consumer actually getting
// disconnected (api_test.go); ADR-024 decision 10's broker ACL boundary,
// including the fpp role's write access being confined to enumerated
// status topics rather than the whole falcon/player/# subtree
// (broker_auth_test.go); and the whole stack surviving an unreachable
// broker or an unreachable configured FPP endpoint without ever 500ing
// (resilience_test.go).
//
// # Why this cannot be a unit test
//
// Every Step 2 test above turns on the MQTT RETAIN flag on an inbound
// publish: live (RETAIN=0) is proof of life, retained-store replay
// (RETAIN=1) is not (see internal/coordinator/broker.Message's doc comment
// and internal/coordinator/inventory's classify). A unit test can only ever
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
// Step 3's tests turn on the identical principle one layer up the stack:
// they exercise the real process boundary (config loading, HTTP listener
// startup, SIGTERM shutdown ordering, the real net/http.Server this
// coordinator's own WriteTimeout is configured on) that no in-process
// wiring of packages ever passes through. This is not hypothetical —
// this session's own wiring pass found two defects that only a real
// process and a real socket could have surfaced: an SSE stream silently
// killed a few seconds after connecting by the coordinator's own
// WriteTimeout (invisible to any httptest.Server-based unit test, which
// never configures one), and a synthesized evidence field
// (node.hello/lastWill/heartbeat's CollectedAt) that was re-stamped from
// "now" on every render, defeating the SSE hub's change detection —
// invisible to every existing unit test because they all use a frozen test
// clock, under which the bug is indistinguishable from correct behavior.
//
// # What runs as what
//
// The agent, the coordinator, and (for the two watch tests and the
// subcommand test) showmeshctl all run as real subprocesses: TestMain
// builds all three binaries once and every test execs them (see
// buildBinary, startAgent, startCoordinator/startCoordinatorWithConfig, and
// runShowmeshctl/startWatch in cli_test.go). This is not incidental — see
// above. A "coordinator restart" in this package is a real SIGTERM, a real
// process exit, and (where a test needs one) a real new process — never a
// teardown/rebuild of in-process Go values standing in for it. The
// coordinator is observed exclusively through /healthz, /readyz, and
// /api/v1 — the same surface any real external client gets — never through
// direct access to its internal packages.
//
// # What this proves and what it does not
//
// These tests establish behavior against Mosquitto and this binary on a
// developer machine or a CI runner. They say nothing about real show
// hardware, and nothing here raises any research record above L1 (see
// CLAUDE.md's evidence ladder). RES-009 failure testing is expected to
// reuse this harness for exactly that reason.
//
// # Running
//
// `make test-integration` starts a pinned eclipse-mosquitto container from
// the shipped deploy/mosquitto/mosquitto.conf, runs this package, and
// tears the broker down; see scripts/test-integration.sh. Running `go test
// -tags=integration ./test/integration/...` directly against a broker you
// started yourself also works — see envBrokerURL's doc comment in
// harness_test.go for the environment variables involved — and every test
// in this package skips cleanly, rather than failing, when no broker is
// reachable at the configured address.
package integration
