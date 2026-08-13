# Engineering lessons

[Documentation index](../README.md) · [Build plan](BUILD-PLAN.md) · [Build log](BUILD-LOG.md)

Defects this project actually shipped and caught, and the rules that came out of them. Each is recorded in full in the session entry of [BUILD-LOG.md](BUILD-LOG.md); this file collects the ones that generalize past their originating step, so a contributor can read them without reconstructing the log.

These are conventions, not history. They are enforced in review.

## A test environment that differs from the deployment environment reports success on exactly that difference

**Step 4.** The Operator UI's client invoked `fetch` as `this.fetchImpl(...)`, so its receiver was the client instance. A browser's `fetch` is a WebIDL operation on `Window` and answers any other receiver with `Illegal invocation`; Node's does not check. The app could not make a single request in Chrome while 99 unit tests passed — including the ones driving a real `node:http` server with real SSE bytes. Three reviews and a build of the shipped image did not find it. Loading the page did, immediately.

The closer a harness gets to real, the more convincing its false success looks.

**Rule:** acceptance criteria get verified against the running stack, not against the suite.

**Step 7 produced the same defect again, in the same file, by a different mechanism.** The Operator UI minted its idempotency key with `crypto.randomUUID()`. That method is **secure-context gated**: it exists on `localhost` and over HTTPS, and is `undefined` over plain `http://` to a bare IP. ShowMesh terminates no TLS, `deploy/README.md` documents the UI at `http://<host>:8081`, and the reference installation's operator uses a phone, which cannot reach it as `localhost`. So on the deployment this step targets, the only write control in the browser would throw `TypeError` and the command would never leave the page. Node and jsdom expose `randomUUID` unconditionally, so all 331 tests passed.

Verified by loading the real UI over the machine's LAN address in Chrome and reading `window.isSecureContext` (`false`) and `typeof crypto.randomUUID` (`undefined`), which is the condition the suite cannot reproduce.

**The corollary, found while writing the regression test:** `delete globalThis.crypto.randomUUID` does **not** remove it, because Node defines it on `Crypto.prototype` rather than as an own property, so `delete` on the instance silently no-ops. The first version of the test passed without ever exercising the fallback. A test double that fails to take effect is a test that passes for the wrong reason, and it is invisible unless you assert the fallback actually ran.

**Rule:** before shipping a browser API, check whether it is gated on a secure context, and check what the deployment's real origin is. When you mock a built-in to prove a fallback, assert that the fallback ran, not merely that the call returned something.

## A test can report success while never having run at all

**Step 7.** `make test-integration-fpp` had been silently skipping its main test on every single run since Step 6 landed `allow_anonymous false`. The script never seeded a `password_file` or an `acl_file`, so its broker exited immediately on start; the wait loop timed out quietly; and `TestFPPSuccessPathThroughRealCoordinator` hit its own "no broker reachable" guard and **skipped rather than failed**. Two reviewers and the orchestrator each read that skip as expected. A missing `-count=1` then let a cached skip replay over the fix.

So the FPP integration path, the one thing that could not be proven any other way, was effectively unverified from Step 6 until Step 7 found it, by running the script rather than reading it.

This is the sharpest form of the project's recurring lesson. [A test's name is a claim](#a-tests-name-is-a-claim) assumes the test executes; this one did not, and reported success anyway. The reason it survived is that a skip *looks* like a considered decision, and a dependency guard is exactly the kind of considered decision a reviewer nods past.

**Rule:** a test that guards on a dependency must fail, not skip, when a harness whose whole job is to supply that dependency is what invoked it. A script that starts a dependency must fail loudly when the dependency does not start. And a suite's skip count is a number somebody has to actually look at.

**The first attempt at that rule was itself wrong, and CI caught it in one push.** The mechanism was a boolean: every `scripts/test-integration*.sh` exported `SHOWMESH_REQUIRE_TEST_DEPS=true`, and any dependency skip became a hard failure. But `make test-integration` starts a broker and no `fppd`, so it began demanding an FPP it never supplies, and three legitimately-skipping tests turned red. The variable now names which dependencies the invoking harness actually guarantees (`broker`, or `broker,fpp`), and a guard is fatal only for a dependency on that list.

**Why it passed locally and failed on CI is the oldest lesson in this file.** A bench `fppd` happened to be running on the developer's own `localhost:8090`, left over from earlier work, so the FPP guards found a live FPP and never fired at all. [A test environment that differs from the deployment environment reports success on exactly that difference](#a-test-environment-that-differs-from-the-deployment-environment-reports-success-on-exactly-that-difference), and here the difference was a stray container.

**Rule:** a harness may only be held to the dependencies it actually starts. And when you change skip behaviour, verify it by *removing* the dependency, not by running where it happens to be present.

## An unbounded write on a failure path evicts the evidence it exists to preserve

**Step 7, seam 0.** The credential-resolution failure path wrote an audit row on **every request on every route**, with no bound. A single browser holding a stale session cookie therefore generated a steady stream of rows that quietly pushed genuine attribution history out through retention.

Nothing failed. Nothing logged. The audit log stayed the right size and stopped containing the answer. It was found by accident during a browser check, not by a test, because there is no assertion shaped like "the interesting rows are still here".

**Rule:** before writing a record on a failure path, ask what an attacker, or a stuck client, can make that path do a million times. An append-only log under a retention policy is a fixed-size window, so an unbounded low-value write is an eviction primitive aimed at your own evidence.

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

**Step 7 produced two more, in one step, on two different surfaces.** A configuration `PUT` whose body had no `endpoints` key wiped every configured FPP endpoint and answered `200`, because an absent key decoded to a nil slice and a nil slice validated as "zero endpoints, fine." A second `declare` with no `--label` erased the operator's existing label, because an absent field decoded to `""` and `""` overwrote. Same defect as `"ma": null` reading as a measured 0 mA, two layers up and pointing at data loss instead of a fabricated reading.

The sharpest part: the first one was reachable by following the CLI's own documented workflow, which piped `config get --output json` back into `config set`. That round trip did not compose, so the recommended operator action was a silent wipe that then bricked the coordinator on its next restart.

**Rule:** absent, `null`, and explicitly empty are three different things, and a write surface must distinguish all three. "Configure nothing" has to be something the operator can only say on purpose.

## Absence of evidence is not evidence of absence, and this project has now decided it four times

**Steps 3, 4, 5, and 7.** The same rule, rediscovered in four subsystems:

- **Telemetry:** a missing `ma` key must not decode to a measured 0 mA.
- **Observations:** only a complete poll may prune, because deleting a source's evidence the first time it goes quiet is far worse than a stale ghost.
- **Inventory:** a discovery run must never delete a node it did not see, because powered-off equipment is normal outside display hours.
- **Discovery completeness (Step 7):** `complete: true` is the *licence* to assert absence, so it must be earned. A run treated "no evidence yet, or evidence too old to trust" as "not observed" and finished complete anyway, so a 40 second broker outage, or a coordinator restart before retained state arrived, would flip every declared node in the installation to `not_seen`.

That last one is the subtle version, and worth the extra sentence: the code correctly refused to delete anything, and still manufactured absence, because it asserted a negative on a run whose evidence source was not working.

**Rule:** before a subsystem reports that something is gone, ask what it would report if its own source of truth were down. If the answer is the same, it is not reporting absence, it is reporting its own blindness.

## A test can be a coin flip, and platform is the usual disguise

**Step 4.** `TestSlowSSEConsumerGetsResetAndDisconnected` failed 15% of the time on Linux and never on macOS. The cause was neither slowness nor socket buffers: it was **frames per render pass**. An MQTT burst arrives one message at a time, each poke of the hub renders separately, and the two kernels schedule that differently.

Worth knowing before designing any back-pressure test: **"the client stops reading" barely creates back-pressure at all.** Measured at 4.0 MB into the kernel on Linux and 1.5 MB on macOS before a single write blocks.

**Rule:** when a test needs an overflow, construct it structurally. Do not race a kernel, and do not grow the burst until it usually works.

## A client that gives up before the server answers deletes an outcome from existence

**Step 7.** The coordinator holds an unconfirmed command response for its full 20 second confirmation deadline. `showmeshctl` defaulted to a 10 second timeout and the browser to 15. So the `unconfirmed` outcome, and the CLI's own exit code for it, were **unreachable at default settings**: the code compiled, its tests passed against instant fakes, and no operator could ever see it.

The second half is worse than the dead code. The operator was told a **transport failure** ("coordinator unreachable, timeout") for what was a successful conversation with a healthy coordinator that answered honestly. That is [ADR-024](../decisions/ADR-024-identity-authorization-and-audit.md) decision 7's inversion, arriving somewhere nobody was looking for it, and it is the fourth recorded variant of the same shape.

**Rule:** when one side of a contract waits and the other side times out, the two numbers are a single design decision and must be written down as one. Derive the client budget from the server's deadline, and put a test on the relationship, not on either number.

## Fail-closed protects the operator from an unaccountable actor, so where there is no actor it protects nobody

**Step 7, fixed 2026-08-13.** The `SHOWMESH_FPP_ENDPOINTS` to store migration writes its config revision and its audit entry in one transaction, per [ADR-024](../decisions/ADR-024-identity-authorization-and-audit.md) decision 11. When the audit append failed, the whole transaction rolled back and the coordinator **exited 1**. The shipped bundle sets `restart: unless-stopped`, so an unwritable `audit_log` was a restart loop: no API, no change stream, no dashboard, no sight of the show.

Three things made it the wrong direction, and the second is the one that generalizes.

- The same function already made the opposite call twice, eight lines above, for a zero `current_revision` and a dangling revision pointer, with a comment saying a store-integrity question must not become an availability outage.
- **Decision 11's fail-closed rule exists so an operator cannot act without a trace. A startup migration has no principal.** There is no actor to hold accountable, so refusing the write protects nobody while costing everything. Fail-closed was applied by shape (this is an audited write) rather than by purpose (someone might act unaccountably).
- Constraint 23 and decision 7 scope an identity or audit failure to "you cannot act", never "you cannot see". Exiting costs the reads too, at the widest possible scope. And the branch is reachable **only on the first boot after an existing deployment upgrades into Step 7**, so the operator reads it as the upgrade having broken their coordinator.

The fix logs at ERROR, persists nothing, returns the environment's endpoint list so the boot collects exactly as it did before, and retries next boot. It exempts nothing from decision 11: the transaction still rolls back, so the unattributable write still does not proceed. Only the process-level response changes, and that belongs to constraint 13. Writing the revision anyway with degraded attribution, the way the blackout/stop/power-off safety class does, *would* have been a second exemption and would have needed an ADR.

**Rule:** before applying a fail-closed rule, name the actor it holds accountable. If there is not one, the rule is not doing its job, and the cost of applying it anyway is paid by the operator. Then check what the refusal actually removes: this project degrades toward the show continuing and toward the operator keeping sight of it, and a refusal that takes out the read surface is pointed the wrong way whatever it protects.

## Softening a hard failure creates a state other surfaces were written assuming could not exist

**Step 7's deferral fix, 2026-08-13, found by the review of the fix itself.** Making the migration non-fatal made a combination reachable that had been impossible: coordinator serving, `SHOWMESH_FPP_ENDPOINTS` in effect, store holding no configuration. Two surfaces already had answers written under the old invariant, and both became false the moment the exit was removed.

The read handler reported that nothing had ever been configured, while the dashboard listed every host being polled from the list that failed to persist. And the write refusal's remedy inverted: *"remove SHOWMESH_FPP_ENDPOINTS and restart once"* is correct after a migration lands and, before one lands, discards the only copy of the endpoint list. The operator follows the API's own written instruction and loses every configured endpoint, with no mistake at any step. The original bug was a loud restart loop; the incomplete fix would have replaced it with a quiet, self-inflicted outage.

**Rule:** when you turn a fatal path into a survivable one, enumerate what becomes reachable that was not, then go read what every existing surface says about it. A remedy written for the normal case is not automatically safe in the case you just invented, and remedies are the most dangerous text in a system because operators execute them.

## Confirming that a value equals what you wanted is not evidence that anything happened

**Step 7.** The first FPP command implementation confirmed by asking "does the current observation equal the desired value?" It never asked whether that observation post-dated the dispatch, and observations stay `current` for 45 seconds. Measured against a live coordinator: a command reported `confirmed` **179 microseconds** after its own `dispatchedAt`, which is far too fast to have collected anything.

FPP is the authoritative scheduler and starts playlists on its own ([ADR-001](../decisions/ADR-001-fpp-is-authoritative.md)), so this is not a contrived race. Collector records `idle`; FPP's schedule starts the show; the operator presses Stop; FPP answers `200` and playback continues; confirmation reads the stale `idle` and reports success while the show is running.

[ADR-003](../decisions/ADR-003-desired-and-observed-state.md) asks for evidence that observed state **moved**. A reading that happens to agree is a coincidence, and the two are indistinguishable unless you compare timestamps.

**Rule:** confirmation by evidence means evidence obtained after the thing you are confirming. Compare the evidence's collection time to the dispatch instant, and treat an already-satisfied desired state as still needing a fresh observation.

## Resolving a rule "in one place" only works if every reader goes through it

**Step 7.** `precedence.go` holds this project's rule for reconciling two collector sources reporting the same signal, and its doc comment says that resolving there rather than at each call site is what makes the rule "impossible to apply inconsistently between endpoints." The FPP command's confirmation path, one file over, iterated the raw observation list and returned on the first matching row.

Since schema v4 the observations key includes `source`, so that list legitimately holds more than one row per signal, in unspecified order. A retained MQTT delivery carries no valid observation time and reads `unknown_age`; if it came first, a genuinely confirmed command reported unconfirmed for the whole deadline. If an MQTT row read `idle` while REST read `playing`, it confirmed falsely. Which one won was **nondeterministic**.

**Rule:** a shared rule is only shared where it is called. When you write "this is the one place X is decided," add the test that fails when a second place decides it.

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
