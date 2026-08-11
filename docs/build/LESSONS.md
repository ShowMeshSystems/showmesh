# Engineering lessons

[Documentation index](../README.md) · [Build plan](BUILD-PLAN.md) · [Build log](BUILD-LOG.md)

Defects this project actually shipped and caught, and the rules that came out of them. Each is recorded in full in the session entry of [BUILD-LOG.md](BUILD-LOG.md); this file collects the ones that generalize past their originating step, so a contributor can read them without reconstructing the log.

These are conventions, not history. They are enforced in review.

## A test environment that differs from the deployment environment reports success on exactly that difference

**Step 4.** The Operator UI's client invoked `fetch` as `this.fetchImpl(...)`, so its receiver was the client instance. A browser's `fetch` is a WebIDL operation on `Window` and answers any other receiver with `Illegal invocation`; Node's does not check. The app could not make a single request in Chrome while 99 unit tests passed — including the ones driving a real `node:http` server with real SSE bytes. Three reviews and a build of the shipped image did not find it. Loading the page did, immediately.

The closer a harness gets to real, the more convincing its false success looks.

**Rule:** acceptance criteria get verified against the running stack, not against the suite.

## A test's name is a claim

**Step 3.** The review pass broke production code deliberately to check which tests noticed, and found three that still passed with the behavior they asserted removed — one of them sitting on an acceptance criterion.

A test that passes whether or not the bug is present is worse than no test, because it also reports success.

**Rule:** before trusting a test, break the behavior it names and confirm it fails.

## Absent evidence is stated, never omitted

**Step 3.** A field the system cannot report carries a state and a reason. A missing field renders as blank, and blank reads as fine.

This is [ADR-011](../decisions/ADR-011-context-aware-observability.md) and [ADR-020](../decisions/ADR-020-control-api-shape-and-change-stream.md) in their operational form: never collected, collection failed, source does not support it, and gone stale are four different answers, and none of them is an empty string.

## The same defect returns in new disguises

**Steps 2, 3, and 4.** Defaulting an unknown observation time to the collection time has been introduced and caught three separate times, each time looking like a different bug.

A retained MQTT delivery carries no valid observation time. `ObservedAt` is therefore a pointer, `nil` means the time is genuinely unknown, the state is `unknown_age`, and it is never treated as fresh.

**Rule:** when a defect recurs, make the wrong thing unrepresentable rather than fixing the instance. There is now a test that panics if the wrong code path is ever taken.

## A test can be a coin flip, and platform is the usual disguise

**Step 4.** `TestSlowSSEConsumerGetsResetAndDisconnected` failed 15% of the time on Linux and never on macOS. The cause was neither slowness nor socket buffers: it was **frames per render pass**. An MQTT burst arrives one message at a time, each poke of the hub renders separately, and the two kernels schedule that differently.

Worth knowing before designing any back-pressure test: **"the client stops reading" barely creates back-pressure at all.** Measured at 4.0 MB into the kernel on Linux and 1.5 MB on macOS before a single write blocks.

**Rule:** when a test needs an overflow, construct it structurally. Do not race a kernel, and do not grow the burst until it usually works.

## A behavior verified only on macOS is not verified for this project

**Step 0.** CI's first run on a real GitHub runner failed a socket test that passes on macOS, exposing a Linux-only `SO_REUSEADDR` behavior difference now recorded in [ADR-013](../decisions/ADR-013-no-fpp-control-port-sharing.md).

## Integration tests catch what unit tests are structurally blind to

**Step 2.** `make test-integration` — the control plane against a real Mosquitto with the agent as a real subprocess — caught three defects on its first run that the unit suite passed over. In one, the unit test asserted the correct ordering against a fake while the real wiring did the opposite.

## Review is where the value has landed

The build workflow delegates review to a separate pass with the diff plus the named ADRs, instructed to hunt for constraint violations rather than style. It has caught defects unit tests could not: broker health exposed as a bare boolean against ADR-011, a Compose `depends_on` that reintroduced the broker dependency [ADR-008](../decisions/ADR-008-mqtt-control-plane.md) forbids, and a discover-ping responder that replied to an ephemeral source port and so could never have worked.

## Configuration mechanisms do what they do, not what you meant

**Step 4.** Compose's `env_file` loads **every** variable in the file, not the ones the service names. The UI service declared two environment variables and inherited `SHOWMESH_API_TOKEN` and the broker password alongside them, readable through `docker inspect` — while three separate comments in the bundle asserted the container never holds the token.

[ADR-022](../decisions/ADR-022-operator-ui-serves-the-api-same-origin.md) forbids *holding* the token, not merely injecting it as a header, because holding it makes reaching the UI equivalent to reaching the API. Compose still interpolates `${VAR}` from `.env` without `env_file`, so removing it cost nothing.

## Removing a line from the working tree does not remove it from history

**Step 0.** A third-party product name was removed from a working copy but remained in the initial commit, and therefore on the remote. History was rewritten, every reachable object re-scanned, and the result force-pushed. All commit hashes changed at that point.
