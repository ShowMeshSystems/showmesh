# Engineering lessons

[Documentation index](../README.md) · [Build plan](BUILD-PLAN.md) · [Build log](BUILD-LOG.md)

Defects this project actually shipped and caught, and the rules that came out of them. Each is recorded in full in the session entry of [BUILD-LOG.md](BUILD-LOG.md); this file collects the ones that generalize past their originating step, so a contributor can read them without reconstructing the log.

These are conventions, not history. They are enforced in review.

## A correct diagnosis of the cause is not a correct diagnosis of the mechanism

**Local toolchain condition, Track D, 2026-08-16.** 82 of 528 UI tests failed locally on every branch checked, including `main` with no Track D code present, all with `RequestInit: Expected signal ("AbortSignal {}") to be an instance of AbortSignal`. The first diagnosis got the cause right and the mechanism wrong. It correctly identified a Node 26.7.0 toolchain as the proximate cause — right, because Track D's own new UI tests passed cleanly on the same run, which correctly ruled out the repository. It concluded from that, incorrectly, that Node 22 was no longer installed locally. It was: Node 22.21.1 was present under nvm the entire time, with nvm's own `default` alias already pointed at it. What had actually happened was narrower — a tool install on 2026-08-16 at 12:54 planted `node`, `npm` and `npx` symlinks in `~/.local/bin` pointing at its own bundled Node 26.7.0, and the shell profile prepends `~/.local/bin` after nvm loads, so those symlinks sat in front of nvm's Node 22 on every interactive shell. Node 22 was shadowed, not missing, and `which node` would have shown that in one command; it was not run before the first diagnosis was written down.

The two readings prescribe different remedies. "Node 22 is not installed" points at installing it, which would have left the shadowing symlinks in place and the failure unchanged. "Node 22 is installed and shadowed" points at removing what sits in front of it, which is what actually fixed it: the repository changed nothing, and the local environment now resolves `node` to the same version CI pins.

**Rule:** ruling out the wrong cause (here, the repository) does not certify the mechanism proposed for the right one. Before prescribing a remedy, run the one command that distinguishes "not installed" from "shadowed" — for a toolchain, that is `which`/`command -v` before anything else — because the two diagnoses share every symptom and only that command tells them apart.

## A test suite cannot tell you that nothing can *reach* the function

**Track D seam D-4, 2026-08-16.** D-3a's built-in `system-resolume-recovery` principal is created at every coordinator startup, and `Store.HasAnyPrincipal` was a bare `SELECT EXISTS(SELECT 1 FROM principals)`. So the reserved principal always existed by the time bootstrap asked, bootstrap always no-opped, the bootstrap file was never written, and no operator could ever claim a fresh deployment. Every test in both seams' suites passed throughout, because nothing in either suite ever exercised bootstrap against a store that already held the reserved principal — D-3a's own tests created it and moved on; D-4's own tests ran against fixtures that never had it.

It surfaced only because building D-4 required standing up a coordinator from scratch and claiming it as an operator would, exactly the step both suites had skipped. That is Step 6's own lesson (CLAUDE.md, and BUILD-LOG's 2026-08-12 entry: `ClaimBootstrap`, `IssueToken`, and `CreatePrincipal` all shipped compiled and tested with no caller that could reach them) reappearing in a sharper form. Step 6's version was "nothing calls the function" — a dead capability, findable in principle by grepping for callers. This one is worse: `HasAnyPrincipal` *is* called, by exactly the code meant to guard it, and returns a wrong but entirely plausible answer. The function is reachable; what is unreachable is the state that would expose it as wrong, because nothing in either seam's test suite ever ran a fresh store forward through both `EnsureReservedPrincipal` and `EnsureBootstrap` in that order.

**Rule:** a test suite cannot tell you that nothing calls the function, and it cannot tell you that nothing can *reach* the function either — not "is this called" but "does anything exercise the sequence of startup side effects a real boot performs, in the order a real boot performs them." Two features can each ship with full unit coverage and still be mutually incompatible the moment both run against one real store from a cold start. The requirement to verify in a real browser against a real, freshly-created stack is what found this; no suite in either seam's own branch was structured to.

## A guard constant that is exported and never asserted is not a guard

**Track D, 2026-08-16, twice in one day.** D-4's review found `MIN_RESOLUME_ACTION_CLIENT_TIMEOUT_MS` and `MIN_RESOLUME_RECOVERY_RESTORE_SERVER_BOUND_MS`, both exported from `ui/src/api/client.ts`, asserted against by nothing. A reviewer set the action budget to 6 seconds against the coordinator's 55-second write deadline and all 596 UI tests, typecheck, lint and build stayed green, because no test compared the two numbers. The file carrying both constants has a doc comment on `FPP_COMMAND_REQUEST_TIMEOUT_MS`, roughly forty lines above, that narrates this exact defect as already paid for: it describes Step 8's client-side review finding a client timeout set to 6,000 ms with 389/389 tests passing, and records the fix as a reconciliation test that fails the build the day the constant is ever set too small again. The comment was read; the pattern it describes was not reapplied to the two constants written right below it.

Hours earlier the same day, D-3a's own review caught the identical shape in its third finding: the recovery-restore client's timeout floor was reconciled against the server bound with `>=` rather than strict `>`, so a client permitted to give up at the exact instant the server might still be answering had a "reconciliation test" that could never fail on that condition, because it accepted equality as sufficient.

**Rule:** an exported constant whose name promises a bound is not a bound until a test fails when it is violated — export alone documents intent, it does not enforce it. And a defect a project has already paid for once, with the payment recorded in the code as a doc comment, still has to be checked for again at every new call site; a comment describing the fix is not the same artifact as a test enforcing it, and only the second one survives a reviewer who reads code faster than comments.

## Verification that only exercises the success path misses the defects that live on the failure path

**Track D seam D-4, 2026-08-16.** The build's own real-Arena browser check drove two actions and both succeeded: a live dispatch and a real ambiguous-clip disambiguation in the launchClip picker. Every one of D-4's four object-id leaks — action-outcome text, the restore report, the crash-recovery record, and the ambiguous-clips-adjacent copy, all capable of embedding a raw Arena id via `deckSelectionRefusal`'s `formatRef` or a bare `by-id/<id>` path segment inside a Go error string — sits on a refusal path, and the refusal is the one an operator hits first, because a clip id resolves only while its own deck is selected (ADR-032 decision 6). None of them could have been found by a check that only exercises actions that work.

The gap was not unique to the manual browser pass: the automated test that should have caught the leak did not, because its fixture used a hand-written paraphrase of the refusal string instead of the string the coordinator's own `resolumeaction` package actually produces. A paraphrase cannot leak what it never contained. The review-fix commit closed both gaps together: it drove a real `launchClip` against an unselected deck on the operator's real Arena to capture a genuine refusal string, then proved the new sanitizer strips it losslessly — the fixture that finally caught the defect was the server's own text, not a description of it.

**Rule:** a success-only pass through a feature — driving actions that work, on fixtures that assert paraphrased strings — structurally cannot find defects that only appear when something is refused, is missing, or is wrong. Refusal paths, error paths, and degraded-evidence paths need their own pass, with fixtures built from the real strings the system under test actually emits, not a maintainer's summary of what those strings probably say.

## A survey destroyed the restore target, and its own tests were green

**Track D seam D-3a, 2026-08-16.** The crash-recovery record kept, per layer, the most
recent thing ShowMesh knew was playing there: a confirmed `launchClip` wrote `{clip,
action}`, and any later survey overwrote that entry survey-sourced. The reader then
reclassified every survey-sourced entry to `unknown` before a restore could use it. So any
survey that ran between a confirmed launch and a crash silently erased the very entry the
restore exists to read.

The production sequence made this certain rather than unlucky, and that is what makes it
worth keeping. A survey runs on WebSocket connect; a crash-restart of the coordinator's
connection to Arena always reconnects the socket; the socket returns roughly 1.5 seconds
after Arena launches, while the liveness poll that gates the restore notices the return on
its own ten-second cadence. So the survey always landed first, always overwrote the record,
and the gate always read a wiped entry. The reviewer reproduced it directly rather than
inferring it.

The test suite that shipped with the first version of this seam was green throughout,
because every test that exercised the transition drove it directly — set the record, fire
the reachable-transition handler, assert the restore — with no WebSocket, no survey, and no
reconnect in the loop. The shorter path the test took was also the path that never
triggered the defect.

**Rule:** when a test drives a state transition directly, ask what else fires on that same
transition in production and is not in the test's call graph. This is related to but
distinct from the existing rule that a test environment differing from the deployment
environment reports success on exactly that difference: here the harness was not a
different environment, it was a shorter causal chain through the identical one.

## File ownership prevents conflicts, not incompatibilities

**Track D, four seams merged 2026-08-16.** Git merged every seam cleanly — no path
collision, no textual conflict — and the combined tree still broke twice, caught only by
the post-fold gate suite rather than by the merge itself.

First: seam E added `TestPackageNeverImportsACollector`, an AST guard asserting
`internal/coordinator/api` imports no collector package, while seam B's own work needed
`internal/coordinator/api/resolumecomposition.go` and `resolumeaction.go` to call label and
ambiguity helpers that lived in `internal/coordinator/collector/resolume`. Both seams were
specified by the same orchestrator and the two specifications contradicted each other; nothing
in either branch's own gates could have found it, because each branch only had its own half.
The fix moved the shared vocabulary into `pkg/resolumecomp`, which both the `api` package and
the collector package already import legitimately, so there is exactly one implementation of
each label rather than two that could drift.

Second: seam D-3a added a parameter to `newResolumeWiring` and updated every call site it
could see in its own branch. Seam C created a new test file, `resolumemacro_e2e_test.go`,
calling that same constructor in parallel. Neither branch's own test suite covered the
other's new file, so both branches individually built and tested clean, and git merged them
into a tree that did not compile.

**Rule:** parallel seams need a shared contract for anything they both touch by name — a
signature, an import boundary, a registry — stated once and referenced by both specs rather
than derived independently by each. Beyond that, the fold's own gate run is the only thing
that catches what the shared contract missed; a clean git merge is evidence of no textual
conflict and no evidence of compatibility.

## `make test-integration` is not concurrency-safe, and its failures look like regressions

**Multi-track session, 2026-08-16.** Four integration fixtures use fixed, non-namespaced
container names: `showmesh-test-mosquitto` (port 11883), `showmesh-bench-fpp-master` (port
8090), `showmesh-test-mosquitto-fpp` (11893), `showmesh-test-mosquitto-fppmqtt` (11894).
`showmesh-test-mosquitto`'s harness registers `trap cleanup EXIT`, and `cleanup` removes the
container **by name**. Two concurrent runs of `make test-integration` therefore share one
container: whichever run exits first tears down the broker the still-running second run is
using, mid-test.

Two builders working different worktrees hit this from opposite directions the same night —
one saw its own broker vanish mid-suite, the other saw a "port already in use" failure
starting its own — and both correctly diagnosed contention between concurrent runs rather
than reporting a regression in the code either of them had touched.

**Rule:** a shared fixture failure presents as a red test in code the diff never touched.
Before chasing a red integration gate as a regression, check whether another run of the same
suite is using the same fixed container name or port at the same time. This project's
worktree-per-track model makes concurrent runs routine, not an edge case, so the fixtures
need namespacing to actually match that model; until they do, treat a same-night red run
against otherwise-unrelated code as contention first.

## A flaky test that was a test defect, diagnosed by falsification

**Multi-track session, 2026-08-16.** `TestRetainedHeartbeatReplayNeverReadsHealthy` failed
about 4% of the time. Reproduced alone on an exclusively-owned broker at roughly 1-in-25,
which ruled out the contention lesson above before it was even written down.

The polling loop made two independent HTTP round trips per iteration: one `getRaw` call
captured the raw response body for a later assertion, and a separate `findNode` call decoded
the gate condition the loop was waiting on. Both calls hit a live coordinator answering
correctly at every instant, but the two requests were not atomic with each other. When the
`not_collected` to `unknown_age` transition landed in the gap between them, the raw body
(request one) still read `not_collected` while the decoded gate (request two) already read
`unknown_age`, so the loop exited on a body that was one HTTP round trip stale relative to
the condition it exited on.

The fix decodes the gate from the same `getRaw` response that captures the body, removing
the second round trip entirely. Verified 50 passes out of 50 across two separate foreground
batches, and then — because a fix that removes a flake is not by itself evidence the test
still catches anything — a deliberate mutation (making `inventory.classify` return a
non-nil timestamp on a retained delivery, which the test exists to forbid) turned the fixed
test red 3 times out of 3 before the mutation was reverted.

**Rule:** a flake that survives isolation from concurrent runs is a test defect, not
infrastructure noise, and the usual shape is two requests standing in for one atomic read.
And fixing a flake without proving the test still bites can leave it permanently green and
useless — mutate the behavior it names and confirm the fixed version still fails.
Separately, this is a fourth instance of a rule this project keeps needing: `not_collected`
and `unknown_age` are different claims about evidence, and a test straddling the transition
between them has to read both halves from one instant, not two.

## The measured shape of the operator's own composition beat the record of it

**Track D seam B, 2026-08-16.** [ADR-037](../decisions/ADR-037-resolume-references-are-names-not-ids.md)'s
Context section states the operator's real composition has 13 of 18 layers named and 5
unnamed, and lists the 13 by name. Seam B's parser, run against the same `.avc` file, finds
**12** named and **6** unnamed — and the ADR's own bulleted list of names has 12 entries, one
fewer than its own prose claims. The record and the data it describes had already drifted by
the time the seam that implements the record started.

The more consequential gap is in the decision itself. ADR-037 decision 3 assumed a reference
scoped by deck plus layer name would be enough to be unambiguous. Measured against the same
real composition: **seven groups covering 16 of the 36 clips remain ambiguous** under deck
plus layer plus name, and every one of them is on `Main`, the deck the show runs from. The
vocabulary was not wrong to require scoping; it was optimistic about how far deck-plus-name
scoping alone would go, and the gap surfaced only once the parser ran against the operator's
own file rather than against the record's summary of it.

**Rule:** when a decision rests on the shape of real data, parse the real data before
building the decision's vocabulary around it, and re-check after — a record that summarizes
external data is itself a measurement with its own error, not a substitute for reading the
source again at build time.

## A branch that is never pushed is never linted

**Step 9's close-out.** The `step-9-wave-3` branch ran every local gate before merging: `gofmt`, `go vet`, `go test -race`, the UI suite, the FPP integration suite. All green, honestly reported, and the claim was believed. But `golangci-lint` runs only in CI, and the branch was never pushed, so the one gate that would have failed was the one gate the branch never met: the merge landed on `main` with 63 lint findings, and CI had already been red since the day before on Track D's own findings, so the new ones arrived invisibly behind the old ones.

Two halves generalize. A branch's "all gates green" silently excludes every gate that only exists somewhere the branch has not been. And a red gate on `main` is not background noise; it hides every new failure merged in behind it, because red-plus-more is still just red.

**Rule:** before claiming a branch passes the gates, enumerate the gates that run only elsewhere (CI-only linters, scheduled suites) and either run them locally or state that they were not run. And treat a red gate on `main` as an outage to fix, not a condition to work around, for exactly as long as it takes to make new failures visible again.

## A refusal is not a null action

**Track D, seam D-3, three times in one diff.** A guard that cannot read its evidence refused the action. Applied to `launchClip` that is right: refusing a start costs only that the clip does not start. Applied to `blackout` it is the inversion this project has now caught four times. Three instances shipped together: a pre-dispatch baseline read that failed refused every action; an identity reading older than fifteen minutes refused every action, and because surveys are event-driven and [ADR-033](../decisions/ADR-033-show-mode.md) Show Mode closes the WebSocket, that refusal was permanent until something else happened to trigger a survey; and a coordinator sitting quietly overnight would have hit both.

Each was written by someone applying "do not act on evidence you do not have" correctly. What the rule misses is that refusing is itself a decision about the show: it leaves the wall lit, and per [ADR-024](../decisions/ADR-024-identity-authorization-and-audit.md) decision 7 a refusal fires no fallback, so the operator is worse off than if the coordinator had been switched off. The distinction that resolves it: **staleness is a fact about our own evidence pipeline, and an identity of `unknown` or `false` is a fact about the world.** The first must not refuse a stop. The second may refuse anything.

**Rule:** before writing a guard, name what the refusal leaves running, and check whether the thing being refused is the operator's way of stopping it. "We could not confirm it was safe" and "we confirmed it was unsafe" are different findings and must not produce the same behaviour.

## An exported bound is only a bound where every phase enforces it

**Track D, seam D-3.** A constant was exported and documented as the upper bound on how long a dispatch could take, precisely so a caller could size an HTTP write deadline before knowing which action was about to run. The caller did exactly that. The constant bounded only the confirmation poll: the pre-dispatch baseline reads ran outside it, one in-flight confirmation check ran outside it, and two bookkeeping writes ran outside it. Measured at 2.256 seconds against a 1.1 second deadline on three layers, scaling linearly, so eighteen layers exceeded both the server's write deadline and the client's budget.

This is the Step 7 client-timeout lesson one level up. There, two numbers on opposite sides of one contract were a single decision. Here there were **three** sides, and the middle one was sizing itself off a number the first one was not honouring, which no test on either pair could catch.

**Rule:** a number is a bound only if the code that computes it is the code that enforces it, in every phase it claims to cover. If you export one for a caller to size against, write the test that measures the real end-to-end duration against it, not the test that compares two constants.

## A decorative assertion can hide inside the fix for a decorative assertion

**Track D, seam D-3.** A review found that two of criterion 1's three tests passed with the post-dispatch evidence fence removed, and the fix was to add the missing `ConfirmedAt.After(DispatchedAt)` assertion to both. Re-running the mutation showed one of them **still** passed: the fake's own delay had advanced the simulated clock enough that the assertion held whether or not the fence existed. It only became load-bearing after the delay was set to zero.

**Rule:** run the mutation again after fixing a test that failed to catch one. The fix is a claim of the same kind as the original test's name, and it has the same failure mode.

## The thing you resolve on a transport event is resolved before the thing exists

**Track D, seam D-1.** The Resolume adapter re-resolves object and parameter ids from a fresh composition read every time its WebSocket connects, because parameter ids are minted at composition load and 0 of 14 survive a restart. Correct, and it fires at the wrong moment: the socket is accepted about 1.5 seconds after Arena launches, and the composition takes about 4.9 seconds to load. Caught live on a real restart — the resolver held `layer_count 3, column_count 9, deck "empty"`, which is Arena's default empty composition, and ninety seconds later Arena held the real 18-layer show while the resolver still held every id of a composition that no longer existed. Nothing corrected it, because Arena does not drop the socket when loading finishes.

The bench capture had already measured that window and the specification had already named it the sharpest hazard in the step. Both framed it as a rule about **acting**: no command may be dispatched on reachability alone. Nobody wrote the rule for **resolving**, and a resolver keyed to a transport event is the same defect with no command in sight.

**Rule:** when a cache, index, or resolver refreshes on a connect, ask what the peer is doing 1.5 seconds after it accepts a socket. A transport being ready is not the subject being ready, and the gap between them is where a well-formed, fresh, wrong answer gets stored and kept. Converging on the truth later is a different guarantee from knowing you were wrong, and only one of them is worth claiming.

## Your own reads can be the thing that breaks the device

**Track D, seam D-1.** `GET /api/v1/composition` crashes Arena 7.23.2 — four `SIGSEGV`s with byte-identical faulting frames. The fourth was produced by `curl` alone with no ShowMesh process running, which is what turned "our adapter is unstable" into "this call crashes Arena." Controls mattered as much as the result: 7 minutes idle, 30 `/product` polls over 5 minutes, and a WebSocket held open for 5 minutes all survived, so the finding is about one endpoint rather than about reading Resolume.

Two things generalize. **A crash in the target looks exactly like a defect in your own new code**, and the only way to tell is to reproduce it without your code in the picture. **And the read you cannot avoid is the one worth costing**: the same API offers no collection endpoints, so this call is the only way to enumerate anything, which turned a bandwidth question into a design constraint.

**The controls are what decided the design, not the crash.** Targeted `by-id` reads survived 209,916 requests and 6.5 GB over ten minutes, with the layer probe alone moving more bytes than the run that crashed. So the hazard was one endpoint rather than the API, which is the difference between "bound how often we enumerate" and ADR-032's "never enumerate over the API at all, the id map is in a file on disk." Bounding the call was the first answer and it was wrong: two reads crashed Arena, so a bound left a segfault on the show's critical path.

**Rule:** when a device misbehaves while your new code is attached, reproduce it with `curl` before you believe either explanation, then run the controls. "Reading it is dangerous" and "this one endpoint is dangerous" build very different systems, and only the controls tell you which one you are in.

**The sequel, recorded 2026-08-15: the controls were still not wide enough.** Every crash occurred on the development laptop, while the playout machine ran the same composition for a month without incident. "Which host" sat in the capture's open-items list and got quoted away. A control that varies your code is not a control that varies the environment.

## A single confirming observation proves the rule only for the case it sampled

**Track D.** The bench capture tested whether a Resolume clip id resolves regardless of which deck is selected. One request, one clip, correct answer, written up as "**REST `by-id` is immune**" and used as a load-bearing property of the addressing model.

It is false. Measured against the same installation: 30 of 30 selected-deck clip ids resolved, and **0 of 10 non-selected-deck ids did.** The one clip the capture happened to test was a `PersistentClip`, one of exactly four in that composition that live outside any deck and therefore resolve always. The test sampled the exception and generalised it into the rule.

Two things make this worth keeping. The wrong conclusion **left a second mystery unexplained in the same document**: the capture also recorded, as an open item, that clip positions 1 and 2 returned identical ids on all three decks. That was the same four persistent clips seen from the other side. Both entries sat there for a day, each holding the other's answer.

And the consequence was not cosmetic. A stored clip id for an unselected deck returns `404`, which the adapter specification's rule reads as "the composition changed underneath us", so an action would have reported a stale reference and marked the composition unidentified because the operator switched decks.

**Rule:** when one observation establishes a general property, ask what class the sample belonged to before writing it down as a rule. Confirming that nothing varies with X requires a case that would have varied.

**Rule:** when a device misbehaves while your new code is attached, reproduce it with `curl` before you believe either explanation. Then run the controls, because "reading it is dangerous" and "this one endpoint is dangerous" lead to very different systems.

## A test environment that differs from the deployment environment reports success on exactly that difference

**Step 4.** The Operator UI's client invoked `fetch` as `this.fetchImpl(...)`, so its receiver was the client instance. A browser's `fetch` is a WebIDL operation on `Window` and answers any other receiver with `Illegal invocation`; Node's does not check. The app could not make a single request in Chrome while 99 unit tests passed — including the ones driving a real `node:http` server with real SSE bytes. Three reviews and a build of the shipped image did not find it. Loading the page did, immediately.

The closer a harness gets to real, the more convincing its false success looks.

**Rule:** acceptance criteria get verified against the running stack, not against the suite.

**Step 7 produced the same defect again, in the same file, by a different mechanism.** The Operator UI minted its idempotency key with `crypto.randomUUID()`. That method is **secure-context gated**: it exists on `localhost` and over HTTPS, and is `undefined` over plain `http://` to a bare IP. ShowMesh terminates no TLS, `deploy/README.md` documents the UI at `http://<host>:8081`, and the reference installation's operator uses a phone, which cannot reach it as `localhost`. So on the deployment this step targets, the only write control in the browser would throw `TypeError` and the command would never leave the page. Node and jsdom expose `randomUUID` unconditionally, so all 331 tests passed.

Verified by loading the real UI over the machine's LAN address in Chrome and reading `window.isSecureContext` (`false`) and `typeof crypto.randomUUID` (`undefined`), which is the condition the suite cannot reproduce.

**The corollary, found while writing the regression test:** `delete globalThis.crypto.randomUUID` does **not** remove it, because Node defines it on `Crypto.prototype` rather than as an own property, so `delete` on the instance silently no-ops. The first version of the test passed without ever exercising the fallback. A test double that fails to take effect is a test that passes for the wrong reason, and it is invisible unless you assert the fallback actually ran.

**Rule:** before shipping a browser API, check whether it is gated on a secure context, and check what the deployment's real origin is. When you mock a built-in to prove a fallback, assert that the fallback ran, not merely that the call returned something.

**Step 9's close-out found the inverse case: state the test environment has, rather than a capability it lacks.** `make test-integration-fpp` was green locally and red in CI from the day the Step 8 primitives suite landed. The difference was two playlists, `showmesh-test` and `showmesh-bench-3item`, created by hand in the local bench container during the Step 8 capture and seeded by nothing in the repository, so every `startPlaylist`-dependent test passed on the one machine that had them and failed on every fresh runner. The build log first recorded the red suite as "container state, not chased", which reads as warm-up luck; it was a permanently red gate, and the project's most load-bearing integration proof rested on fixture state exactly one machine possessed. The fix seeds the playlists idempotently in the harness that starts the container.

**Rule:** when a suite depends on state inside a long-lived test container, the harness that starts the container seeds that state. A fixture made by hand exists on exactly one machine, and a green run against it is a claim about that machine, not about the code.

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

## A comment is a claim too, and a comment claiming a test is the worst kind

**Step 8.** Two instances, found in the same review pass, both of them the *fix* for an earlier defect rather than the original defect.

`ui/src/api/client.ts` carried a doc comment saying "client.test.ts proves this client actually waits this long when given a slow response." That test did not exist. The constant it guarded, the browser's command timeout, is the one Step 7 shipped too low, making `unconfirmed` unreachable in the browser. Proved vacuous by lowering it from 35 s to 6 s, below the coordinator's own 20 s confirmation deadline: 389 tests, typecheck, lint and build all passed.

`internal/coordinator/api/fppcommand_handler.go`'s doc comment stated that a primitive "in ADR-024 decision 11's safety class (stopPlaylist is the only member today) proceeds regardless" on an audit-write failure. The struct had no safety-class field at all and the branch fired for all eight primitives, so `startPlaylist` dispatched with degraded attribution.

Both comments were written by someone who understood the rule correctly. Neither was enforced by anything. A reviewer reading the comment sees the rule, ticks it off, and moves on, so an accurate-sounding comment is *more* dangerous than no comment where the code disagrees.

**Rule:** a rule stated only in a comment is a suggestion. If a constant matters, a test asserts it; if a policy has members, the type makes "forgot to decide" impossible to express. And never write that a test proves something without opening the test.

## The rule's author is not exempt from the rule

**Step 8.** The step existed partly to enforce "confirmation rests on evidence that post-dates dispatch and shows the state moved." Its own specification then introduced a violation of exactly that rule.

`nextPlaylistItem` was specified to accept `fpp.status == "idle"` as confirmation, correctly, because the capture measured Next at the last item ending the playlist, which is the command's largest possible effect. What the specification omitted was a pre-dispatch baseline proving the host was not *already* idle. So the input the same capture had already measured two sections earlier — Next while idle, `200 "Next Item Playing"`, nothing happens — reported `confirmed`.

It was found by dispatching it against the bench and watching FPP not move. Not by review, and not by the test named for that branch, which was itself a coin flip: it passed on a race between bcrypt principal creation and a fixed 60 ms sleep, so it could not have caught this.

Two things generalize. Writing the rule down does not confer immunity from breaking it, and the person who most recently wrote it is if anything the likeliest to assume the instance in front of them is covered. And a defect introduced by a specification is invisible to a reviewer who trusts the specification, so it survives exactly the process meant to catch it.

**Rule:** when a predicate has more than one accepted branch, ask what each branch alone would confirm if the command had done nothing. Then verify against the system, because that is the only place this class shows up.

## What the operator reads and what a maintainer needs are different documents

**Step 8.** The first shipped strings cited repo paths at the operator. A warning in the UI read `"This is the last item in the current playlist (1/1) — pressing Next Item will END the show, the same way Stop does, not skip within it (docs/bench/fpp-command-vocabulary.md section 3.5)."` A confirmed graceful stop rendered a four-line paragraph ending in another section reference.

The citations were not sloppiness. They were traceability, put there deliberately so a maintainer could find the evidence behind a behaviour, and they ended up on the wire because the seam specifications cited sources and the implementers put the citations in the strings rather than the comments. It read as diligence the whole way.

Found by the owner loading the real page. No test could have objected: every assertion was on `role` and `textContent`, and both were correct.

**Rule:** an operator-facing string states what happened and what to do about it, in one or two short sentences, with provenance compact and last. The reasoning belongs in the comment beside it. Enforce it with a guard test rather than intention — this project now has two, one walking the Go AST and one the TypeScript AST, failing on any repo path, `.md` reference, ADR number, or `section N` in a user-visible string.

## A plan may name an external system's vocabulary only from that system's own output

**Step 8**, which is the step created to pay off Step 7's version of this. Step 7's plan named the FPP command `Stop Playlist`, which FPP does not have, and named the confirmation signal `fpp.status.player_state`, which the collector does not emit. Both read as entirely plausible and both cost implementation time.

So Step 8 made the capture a deliverable, taken before any command was named. It overturned four more assumptions that were equally plausible:

- **An external system's `200` means its dispatcher ran, and nothing more.** `Start Playlist` against a playlist that does not exist answers `200 "Playlist Starting"` and the host stays idle. Pause, Resume, Next and Prev all answer cheerfully while idle and do nothing. Reading the implementation shows why: the success string is constructed unconditionally, after a call whose failure is never consulted.
- **An argument's name is not its specification.** `ifNotRunning` reads as "only start if nothing is running." It means "if *this* playlist is not the one already running", so it does nothing whatsoever to protect a different running show.
- **A command's obvious confirmation may be unreachable.** A graceful stop's terminal state is bounded by the currently playing item's runtime, so it cannot confirm on `idle` within any deadline the coordinator can choose.
- **A command can be a stop button under another name.** `Next Playlist Item` at the last item ends the playlist, and FPP answers `Next Item Playing` either way.

None of these is discoverable by reading, and each would have shipped as a defect.

**Rule:** capture the vocabulary from the system, then write the plan. Where a command's effect is not observable through a signal already collected, it does not ship as a confirmed command: it may ship reporting unconfirmable with a stated reason, or with an operator-supplied observation contract, as Step 9's external MQTT step does under BUILD-PLAN's recorded exemption. What may never ship is a step that reports success it did not verify, and the exclusion or exemption is recorded with its reason rather than omitted.

## A distinction the operator must see cannot be tested by asserting text

**Step 8.** Every visual state in the new command controls was a CSS class with no rule in any stylesheet: the warning that pressing Next will end the show, and the difference between confirmed and unconfirmed. So in a browser the warning rendered as ordinary body text and the two outcomes were identical paragraphs differing by one leading word. `text-muted` *was* styled, so the de-emphasized states were the only visually distinguished ones, exactly backwards.

Every test passed, because jsdom has no computed style and the tests asserted `role` and `textContent`, which were correct.

This is [the deployment-environment lesson](#a-test-environment-that-differs-from-the-deployment-environment-reports-success-on-exactly-that-difference) in its quietest form: nothing was broken, something was merely invisible.

**Rule:** if the operator is meant to notice a difference, assert it somewhere a stylesheet exists, or check the built artifact. Correct text in an unstyled element is not a delivered distinction.

## A client that branches on the server's prose has left the contract

**Step 8.** The command endpoint returns two different `409`s from one guard: a different playlist is playing, or the evidence needed to decide is not current. Both carried the same problem `type` and differed only in their human-readable detail, so the Operator UI told them apart by matching a substring of the server's English.

Reword that sentence and the UI silently offers "Start anyway (replace what is currently playing)" for the case where the coordinator just said it does not know what is playing. No test would notice, because the prose the test asserts is the prose the code matches.

**Rule:** if a client must distinguish two responses, they differ in a machine-readable field. Prose is for the operator, never for control flow. Adding a problem type is additive under [ADR-020](../decisions/ADR-020-control-api-shape-and-change-stream.md), so the cost of doing it properly is close to zero.

## Absent evidence is stated, never omitted

**Step 3.** A field the system cannot report carries a state and a reason. A missing field renders as blank, and blank reads as fine.

This is [ADR-011](../decisions/ADR-011-context-aware-observability.md) and [ADR-020](../decisions/ADR-020-control-api-shape-and-change-stream.md) in their operational form: never collected, collection failed, source does not support it, and gone stale are four different answers, and none of them is an empty string.

## A JSON round trip is not the identity function, and the type that survives it is not the type you stored

**Step 9 wave 2.** A macro `setVolume` step dispatched volume **0** and reported `confirmed`.

The configuration write path normalizes an FPP action's params to `string`, `bool` and `int64`, which is exactly the shape the dispatch seam documents needing. Storing a revision marshals that `map[string]any` to JSON. Resolving the pinned revision unmarshals it back into `map[string]any`, and `encoding/json` has one number type, so `int64(50)` returns as `float64(50)`.

Every integer-valued primitive then reads its own parameter through `params["volume"].(int64)` with the `ok` deliberately discarded, because at the command endpoint the value cannot be anything else: it was decoded from the wire by the same registry two lines earlier. Through the macro path it can. So the dispatch sent 0, the desired state recorded 0, and the confirmation predicate compared observed volume against 0, which meant a muted show reported a green `confirmed: true`.

Three things made it invisible. The write path was correct. The store was correct. The doc comment on the resolver said the params were "already normalized, natively-typed Go values", which was true of what went in and false of what came out, and it was written by someone who had read the seam's contract carefully. Bools and strings survive the round trip, so `setVolume` was the only live instance and the next integer parameter would have inherited it silently.

This is [confirming a value equals what you wanted](#confirming-that-a-value-equals-what-you-wanted-is-not-evidence-that-anything-happened) with the polarity reversed: the evidence was genuinely post-dispatch and genuinely showed the state moved, to the value the coordinator had asked for, which was the wrong one.

**Rule:** when a normalized, natively-typed map crosses a serialization boundary, re-derive it through the same normalizer on the way back, never by coercing the type you find. A second coercion rule is a second place the vocabulary can drift. And when an assertion discards its `ok` because the value provably cannot be another type, that proof belongs to one call path, so write down which one.

## Vacuous truth is not evidence of presence

**Step 9 wave 2.** The startup reconciler computed a run's `confirmed` by starting from `true` and skipping every step that had not resolved. A coordinator that restarted one second after accepting a run therefore finished it with every step reading `skipped` and the run itself reading **confirmed**.

The loop was written to answer "did any resolved step fail to confirm", which is a different question from "did every step confirm". With no resolved steps the first is vacuously false and the second is plainly false, and the code returned the first. ADR-031 requires the surfaces to render `completed` and `confirmed` distinctly, which makes a green tick on a run that never ran worse than a merged indicator would have been.

This is the mirror of [absence of evidence is not evidence of absence](#absence-of-evidence-is-not-evidence-of-absence-and-this-project-has-now-decided-it-four-times), and it is easier to miss, because an empty loop that never falsifies looks like a loop that checked.

**Rule:** an all-of predicate over a possibly-empty set needs the empty case decided on purpose. Ask what the function returns when it examines nothing, and make the test supply that case rather than a populated one.

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

And running it is where more has landed since. Step 6's three unreachable features, Step 7's 179-microsecond confirmation, Step 8's `nextPlaylistItem`, and Step 9's endpoint-removed-mid-run were each found by exercising the assembled system, not by review or tests. Review is cheap insurance on a diff; integration finds what the diff was wrong about. When they compete for time, integrate.

## Configuration mechanisms do what they do, not what you meant

**Step 4.** Compose's `env_file` loads **every** variable in the file, not the ones the service names. The UI service declared two environment variables and inherited `SHOWMESH_API_TOKEN` and the broker password alongside them, readable through `docker inspect` — while three separate comments in the bundle asserted the container never holds the token.

[ADR-022](../decisions/ADR-022-operator-ui-serves-the-api-same-origin.md) forbids *holding* the token, not merely injecting it as a header, because holding it makes reaching the UI equivalent to reaching the API. Compose still interpolates `${VAR}` from `.env` without `env_file`, so removing it cost nothing.

## Extending one half of a timeout pair is not extending the timeout

**Track E, seams E3/E4.** The upload handler extended its read deadline so an upload slower than the coordinator's 10-second server `ReadTimeout` would not be killed mid-transfer. Go arms the *write* deadline the moment request headers are read, not when the handler starts writing a response, so it kept ticking against the wall clock for the whole time the body streamed in. The upload was staged, hashed, registered and audited — genuinely, fully successful — and then failed on the response flush, so the operator read a transport error for a request that had already succeeded, reproducibly, and a retry reproduced it again. The test named for this behaviour left `WriteTimeout` at zero and passed whether or not the fix existed.

This is the fifth time in this project that two timeouts on opposite sides of one contract turned out to be a single decision, and the first time the session's own specification named the trap explicitly and still under-specified the fix: it required extending the read deadline and cited an existing precedent for extending a *write* deadline elsewhere, without saying this handler needed both.

**Rule:** when one operation is guarded by two deadlines, check both sides of the *same* request, not only the side the known trap points at. A server request has a read deadline and a write deadline, armed at different moments in the request lifecycle, and "extend the timeout" is not a complete instruction until you say which one.

## A function with a no-op default and no caller is an unreachable capability wearing a type

**Track E, seam E6.** `Nudge()` existed, was unit-tested, and was documented in its own doc comment as the upload handler's hook for triggering an immediate sync rather than waiting out the interval. Nothing called it. An upload, and separately an activation of a new active show, sat out a full `SHOWMESH_ASSET_SYNC_INTERVAL` before anything happened, silently, because the function that existed specifically to prevent that wait had no call site.

This is the third time this project has shipped a function nothing invokes, and the first time the interface's own doc comment described the caller that did not exist — which reads as more diligent than silence, and is more dangerous for it: a reviewer who reads the comment sees the intended design and moves on rather than going looking for the call site.

**Rule:** a capability is not shipped until something in the running system actually calls it. Check for a real call site with the scrutiny given the function itself, and treat a doc comment naming an intended caller as a claim to verify, never as evidence the caller exists.

## A "replace" that runs on an incomplete report converts absence of evidence into evidence of absence, in the store

**Track E, seams E5/E6.** The agent reports an empty asset list whenever it could not finish enumerating its own directory — a transiently unreadable mount, for instance. The coordinator's inventory store treated every report as authoritative and replaced the prior inventory wholesale on receipt, so an agent that could not read its disk this one time told the coordinator, correctly by the letter of "replace means replace," that the node now holds nothing. The manifest correctly rendered `unknown`. But the sync service was not consulting the manifest: it compared its own held-asset map directly against expectations, saw nothing held, and re-dispatched every asset on every tick until the mount returned — a fleet-wide re-fetch storm triggered by a report that was honestly reporting that it did not know anything.

The fix needed two halves, and either alone would still have shipped a live defect. The store now leaves the existing inventory alone whenever a report is incomplete, because "we could not check" is not "we checked and found nothing." And the dispatcher was rerouted through the same readiness function the read API renders, so there is exactly one place a node's readiness gets decided, and dispatch now stops whenever that function reads `unknown` for any reason — not only when a second, locally-built map happens to agree with it.

**Rule:** a subsystem that overwrites what it knows on receipt of a report must distinguish "the report says empty" from "the report could not be produced." And every consumer of derived state — not only the one that renders it to an operator — must go through the same readiness decision rather than keep a shadow copy that can disagree with it.

## Strengthening a decorative test can leave it decorative

**Track E, seams E3/E4.** Review found the agent's post-write read-back was dead code as far as the test suite could tell: the value it produced could be replaced with the already-known in-memory download hash and the whole package stayed green, even though the comment beside it claimed the value rested on a genuine re-read of the file just written to disk. The first fix — asserting the field's value more precisely, and separately asserting the on-disk file size — both still passed against the same mutation. Neither pinned what the code actually *did*; both pinned values the mutation happened to preserve, because the download hash and the disk hash agree in every reachable happy path. Only asserting that the read-back function was *called* caught it.

**Rule:** when you strengthen a test that failed to catch a mutation, run the same mutation against the strengthened version before trusting it. A better assertion on the wrong thing is still decorative.

## A test can catch its mutation for the wrong reason

**Track E, seams E3/E4.** Two tests in one diff appeared to catch their intended defect for a reason unrelated to the rule they were named for. A refusal test asserted only that the response was non-2xx; a review mutation made the handler fail with a `500` instead of the `400` the test's name promised, and the test never noticed, because "not 2xx" is equally true of both. A path-traversal test drove its fixture through the store's public `Open` method, where a coincidental file-not-found produced the same outward failure a correctly rejected traversal path would — so the test could not distinguish "we refused this path" from "this path happened not to exist" underneath an unrelated error.

**Rule:** a test that asserts only that something failed, without asserting *how*, will pass against a mutation that breaks the mechanism but happens to fail for an unrelated reason. Assert the specific failure — the exact status, the exact error — and where a guard is under test, exercise it directly rather than through a caller where an unrelated failure can stand in for it.

## Integration found what neither review did, and one failure was a fix working

**Track E, acceptance run.** Two failures surfaced only once the acceptance suite ran real processes against each other, and neither review pass had found either. The store-unreachable test simulated the store going away by closing its proxy's listener; the agent's HTTP client kept its already-open connection alive, so the proxy went on forwarding fetches over the connection it already had, the node fetched the asset it was meant to be unable to reach, and the test sat out its full timeout waiting for a `not_ready` that could never arrive, because the one path that mattered was still open. `stop()` now closes every live connection, not just the listener.

The second was not a defect. The active-show-change test asserted `not_ready` immediately after activation, with a comment arguing the assertion was deterministic rather than a race. That was true only while sync ran on a fixed timer; the `Nudge` fix (above) made activation trigger sync within milliseconds, so the node was sometimes already ready by the time the assertion ran, and the comment's own argument for safety had quietly become false underneath it. The test now stops the content endpoint before activating, so `not_ready` is a resting state the test controls rather than a window it was racing to observe.

**Rule:** a mock that only closes a listener does not remove a connection a real client is keeping open; any test simulating "the peer went away" has to close what a real client holds. And when a fix changes system timing, re-read every test whose passing depends on an argument about *when* something happens — a comment defending a race as "actually deterministic" is a claim about the system as it existed when written, not a property of the assertion itself.

## Verify a reviewer's finding before acting on it

**Track E, review fold.** Two independent reviews returned eleven findings together. One flagged a test as decorative — an `ETag` assertion said to pass regardless of the header's value. Checking it before changing anything found the assertion present and precise: it failed correctly under the mutation. Nine of the other ten findings held up and were fixed; this one did not, and no change was made for it.

**Rule:** a review finding is a hypothesis, however specific it sounds, not a fact. Reproduce it — run the mutation, read the assertion, check the call site — before spending a fix on it. A fix for a finding that was wrong is a pointless change to code that was already correct.

## Removing a line from the working tree does not remove it from history

**Step 0.** A third-party product name was removed from a working copy but remained in the initial commit, and therefore on the remote. History was rewritten, every reachable object re-scanned, and the result force-pushed. All commit hashes changed at that point.

## A capability with zero surfaces is in perfect parity

**Track G audit, 2026-08-17.** The owner tried to connect Resolume Arena to a running coordinator and could not, from any surface. No UI control, no `showmeshctl` verb, no API endpoint: the host is `SHOWMESH_RESOLUME_URL`, read once at startup, so connecting Arena meant editing a file in the deployment bundle and restarting the container. Resolume control is one of three founding problems, and its observability, action vocabulary, crash recovery and UI had all shipped.

Every rule passed. CLAUDE.md requires every API capability to get CLI coverage in the step that adds it; ADR-030 requires API first, then CLI, then UI. Both are conditioned on an endpoint existing. Zero endpoints means zero obligations, so the subsystem shipped unconfigurable while fully compliant. The audit found the same shape in the FPP MQTT collector, the asset store settings, and identity administration, where seven subcommands live on a distroless coordinator image with no shell, making a token revocation require container exec access.

The deeper cause is worse than an oversight. **FPP had already been rescued from this exact pattern**: `SHOWMESH_FPP_ENDPOINTS` was an environment variable until Step 7 promoted it to a store-backed kind with revisions, a migration, and eventually ADR-036's no-restart apply. Track D shipped *after* that migration completed and reproduced the pattern FPP had just been rescued from, because the promotion was recorded as work done to FPP and never as a rule about configuration.

The audit also found `principal:write` declared, bundled into the admin role since Step 6, and checked by no handler. That is the fourth capability this project has shipped that nothing can reach.

**Rule:** a rule that enforces consistency between surfaces cannot detect a capability that is missing from all of them, so state separately that the capability must exist. And when a defect is fixed in one subsystem, ask whether the fix is a rule; if it is, write it as one, because a correction recorded only as work done is a correction the next subsystem does not inherit. ADR-039 is that rule, and its two enforcement tests exist because this one had been honoured by discipline alone.
