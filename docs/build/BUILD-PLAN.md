# Build Plan

[Documentation index](../README.md) · [Architecture specification](../architecture/ARCHITECTURE.md) · [Operator UI specification](../architecture/OPERATOR-UI.md) · [Research tracker](../research/README.md)

## How this relates to the roadmaps

ARCHITECTURE §12 defines outcome phases (Phase 0 through Phase 4). OBSERVABILITY §14 defines observability delivery phases (O1 through O5). Neither document commits to an implementation order within itself; both describe what must be true when a phase is reached, not the sequence of engineering steps that gets there.

This document is that sequence. It follows CLAUDE.md's walking-skeleton build order: protocol and timeline first, then the control-plane skeleton, then read-only observability, each step producing something runnable before the next step starts.

This is a working document, not an architectural contract. It records intent and status for implementation planning, and it must never contradict an accepted ADR. When a step's design turns out to conflict with an ADR, the ADR wins; either the step is redesigned or a superseding ADR is written first.

Status vocabulary: `not started`, `in progress`, `blocked`, `complete`.

## Delivery tracks and the day-0 schedule

Added 2026-08-13, when the owner supplied dates and scope that the numbered steps alone cannot express. **The numbered steps below remain correct and are not renumbered**; this section says which of them run at the same time, and against what deadline.

### The dates

| Date | What it is |
|---|---|
| **Mid-September 2026** | **Day-0.** ShowMesh must be able to control a real show. Chosen deliberately early so faults surface with slack. |
| **Week of 21 September 2026** | Projector setup, testing, and mapping begins. Not a show, but the projection path wants to be usable by then. |
| **Early October 2026** | "One button runs the show" is acceptable here rather than at day-0, by the owner's call. |
| **17 October 2026** | Halloween show opens. |
| **31 October 2026** | Halloween. |

**Scope at day-0 is the whole show**, not an FPP slice: FPP control, audio, media, projection, and Resolume.

**Three founding problems, recorded 2026-08-13 because they decide what may be cut.** ShowMesh exists because three separate things went wrong last season and all pointed the same way.

1. **Generating virtual matrix data did not work**, which is why there is a video node. Track B.
2. **Controlling Resolume did not work**, which is why LTC and the Resolume API and OSC are in scope. Track D.
3. **FPP's scheduler was the wrong tool**, which is why macros and commands exist. Track A.

Each one alone is a reason this project was started, so **none of the three may be cut to make a date.** What made ShowMesh a system rather than three workarounds was noticing that all three were the same problem, and bringing the whole media path in. Anything else is negotiable; these are not.

### The tracks

Four tracks run in parallel. They are independent enough to build simultaneously, and the dependency that is *not* obvious is called out below.

- **Track A, FPP control.** Step 8 then Step 9. Sequential within itself. Specified.
- **[Track B, nodes and projection](TRACK-B-nodes-and-projection.md).** The node agent is a stub today, so this starts close to zero: a real agent, GStreamer pipeline supervision, local FSEQ virtual-matrix extraction, NDI output, into Resolume. Governed by [ADR-026](../decisions/ADR-026-renderer-surface-model-and-reference-transport.md). **The largest and riskiest track**, and the one carrying reason two for the project. Opens with the transport spike in [`docs/bench/TRACK-B-NDI-SPIKE.md`](../bench/TRACK-B-NDI-SPIKE.md).
- **[Track C, audio node](TRACK-C-audio-node.md).** The interface is purchased (Behringer U-Phoria UMC204HD, 2026-08-13). Design work can start now; the first bench action is the twenty-minute output-addressing check recorded in [RES-007](../research/RES-007-audio-node-architecture.md), because if outputs 3/4 mirror 1/2 then [ADR-018](../decisions/ADR-018-program-and-ltc-share-a-clock-domain.md) is violated inaudibly and the interface is the wrong one.
- **[Track D, Resolume control and timecode](TRACK-D-resolume.md).** The Resolume adapter over REST, WebSocket and OSC, plus the LTC timecode path. **Day-0**, promoted 2026-08-13. Its timecode arrives as audio LTC over a physical cable from Track C, which puts Track C on this track's critical path.
- **Projector power is out.** It stays on Home Assistant and Node-RED, driven by MQTT the way it was from FPP. ShowMesh supplies an arbitrary MQTT publish step type in Step 9 instead, which is deliberately unconfirmable and must say so. `pkg/pjlink` and ADR-016's provider model are deferred, not cancelled.

### The dependency that will bite, and how it is handled

**Step 9's macros orchestrate all four tracks, and three of them do not exist yet.** Left alone that makes Step 9 block on everything and land in late October, after the show opens.

So Step 9 builds the macro **mechanism** against the step types that exist, and the step vocabulary grows as tracks B, C, and D land. A macro ships early with a small vocabulary rather than late with a complete one. This is the same shape as Step 7 shipping one command rather than waiting for a family.

### What is deliberately cut from day-0

Recorded so each is a decision rather than an omission discovered in October.

- **Reduced local fallback for macros.** [ADR-004](../decisions/ADR-004-layered-commands-and-fallback.md) requires every critical macro to define one, and the RES-008 re-survey established there is no delivery path: no agent cache ([ADR-025](../decisions/ADR-025-agent-fallback-cache-is-signed.md) decided the trust model and nothing built it) and no configuration-distribution topic in ADR-008's v1 set. **The first macros ship with no critical fallback and say so**, which answers the open question in Step 9 below. Revisit after Halloween.
- **YAML configuration export and import.** ADR-009's portable-bundle half, still entirely unbuilt. Not day-0.
- **Preview wall.** RES-010. Cut.
- **Any further authentication or authorization work.** ADR-024 shipped and that is the end of it. This is a local application on an isolated show network, target scoping is already declined in Step 8, and nothing may grow the security surface without being asked for.

### How this work is verified, which changed on 2026-08-13

**The containerized `fppd` is sufficient evidence for the core build.** The owner's decision: FPP's core behaves the same in the container as on the deployed Pis, the differences are real but not disqualifying, and per-step live-fleet verification costs schedule that day-0 does not have.

The owner tests **top to bottom once a track is fully delivered**, producing a punch list to work through. "Fully delivered" means a complete vertical slice that runs, not a partial build needing hand-holding. Node work gets tested as it lands, because nodes are physical.

**This supersedes the earlier standing rule** that every acceptance criterion is re-proved against real show hardware before a step closes. It does **not** change the rule that criteria are proved against a *running binary* rather than against the test suite, which is a different claim and still holds: Step 7's three worst defects were all found by running the thing rather than by reading or testing it. **The one exception is [RES-002](../research/RES-002-fpp-multisync-compatibility.md)**, which the owner benches personally against the physical rig, because the MultiSync timing path has to be right.

**The live-fleet write prohibition is unchanged.** No write, no command, no restart, no settings change, and no MQTT publish against the deployed FPP hosts during the build. When that changes it will be the owner doing it deliberately, in his own testing pass.

## Step 0: Foundation

Status: complete (2026-08-10)

**Goal:** a running skeleton that other steps can build on: repository layout, tooling, packaging, and CI, plus a coordinator binary that does nothing show-related yet but proves the deployment shape works.

**Deliverables:**

- Repo scaffold under module path `github.com/showmeshsystems/showmesh`, Go layout per CLAUDE.md (`cmd/showmesh-coordinator/`, `cmd/showmesh-agent/`, `internal/`, `pkg/...`).
- Lint and test tooling.
- Multi-arch (`linux/amd64`, `linux/arm64`) Docker image and a Compose bundle with Mosquitto.
- GitHub Actions CI.
- [ADR-012](../decisions/ADR-012-docker-coordinator-deployment.md).
- These build tracking documents (BUILD-PLAN.md, BUILD-LOG.md).
- A minimal coordinator binary that loads configuration, connects to the broker with indefinite retry, serves `/healthz`, `/readyz`, and `/version`, and shuts down cleanly.

**Acceptance criteria:**

- `make check` passes.
- The multi-arch image builds.
- The Compose stack comes up.
- The coordinator survives an unreachable broker without exiting.

**Bound by:** ADR-006, ADR-008, ADR-012.

**Verified 2026-08-10:** `make check` (fmt, vet, lint, race tests) passes; the image builds and runs, and the `linux/amd64` plus `linux/arm64` legs cross compile from the build platform; the Compose stack comes up and reaches `/readyz` 200 against the bundled broker; the coordinator serves `/healthz` 200 and `/readyz` 503 with the broker unreachable, survives the broker being stopped and restarted, and exits cleanly on SIGTERM in both states.

**Known follow-up, carried into Step 2:** `internal/coordinator` currently holds configuration, the HTTP server, and the MQTT client in one flat package. That is acceptable at three files and will not survive Step 2 adding the SQLite store, inventory, and reconciliation. Split it into focused packages as the first task of Step 2, before new concerns land on top of it.

## Step 1: pkg/multisync

Status: complete (2026-08-10), L1 only

**Goal:** the FPP MultiSync wire protocol and a timeline model, since RES-002 is the critical-risk record this project depends on most and is still only L1 (source-verified).

**Deliverables:**

- A codec for the `FPPD` header and packet types 0x01 (sync), 0x03 (blank), 0x04 (ping and discover), and 0x06 (FPP command), honoring the endianness split recorded in RES-002 (sync fields little-endian, ping version fields big-endian).
- A listener joining multicast 239.70.80.80 on UDP 32320 that also accepts broadcast and unicast.
- A timeline model implementing FPP remote semantics: free-run through sync silence, slew of four frames or fewer, skip when moderately behind, jump when more than 0.5 seconds behind, STOP followed by a blank delay of about five frames, tolerate START without a preceding OPEN and a bare SYNC for an unstarted sequence, and transition to `unsynchronized` after a defined silence interval.
- A discover-ping responder following the non-FPP device etiquette documented in RES-002.
- Table-driven tests against hand-built packets, a fuzz target on the decoder, and timeline tests on a synthetic clock.
- A `showmesh-multisync-probe` command that records captures for bench evidence.

**Acceptance criteria:**

What unit tests can establish (L1, this is what Step 1 completing actually proves):

- The codec round-trips correctly against hand-built packets for the `FPPD` header and packet types 0x01, 0x03, 0x04, and 0x06, honoring the little-endian/big-endian split recorded in RES-002.
- The timeline state machine, driven by a synthetic clock, follows start, stop, restart, and late join; free-runs through sync silence; slews, skips, or jumps per the recorded thresholds; and transitions to `unsynchronized` within a defined silence interval.
- The discover-ping responder answers correctly per the documented non-FPP device etiquette.

What only a capture against a real FPP 9.x or 10.x player can establish (L2, these are the five open bench items in RES-002 and are explicitly out of reach for unit tests): pause and seek packet behavior and whether OPEN reliably precedes START across master versions and xSchedule; STOP/BLANK combinations at playlist end, manual stop, and fppd shutdown; clock-drift accumulation over a 30-60 minute show; multicast IGMP behavior and discover-ping participation on the reference switch; and compatibility measured across the supported FPP versions and network modes. The probe built in this step is what collects that bench evidence; it does not itself constitute the evidence.

**Bound by:** ADR-001, ADR-006, [ADR-013](../decisions/ADR-013-no-fpp-control-port-sharing.md), RES-002. ADR-013 was written out of this step.

Step 1 completing does not move RES-002 past L1. Unit tests raise nothing above L1; only a capture against a real FPP 9.x or 10.x player moves RES-002 to L2.

**Verified 2026-08-10:** `make check` passes; the package builds for `darwin/arm64`, `linux/amd64`, `linux/arm64`, and `windows/amd64`; `FuzzDecode` ran clean across roughly 17 million total executions; the probe was exercised end to end against a synthetic sender over loopback. The byte-offset table in `pkg/multisync/doc.go` was independently re-verified field by field against FPP's `ControlProtocol.txt`, `MultiSync.h`, and `MultiSync.cpp` during review, including the ping body layout and the little-endian/big-endian split.

**Known follow-up:** the probe's discover-ping responder has never been answered by a real FPP instance, so whether ShowMesh actually appears in the FPP MultiSync UI is unverified. That is part of RES-002 open item 5 and is what the first real capture should check.

**Operational hazard recorded during this step:** running a MultiSync listener on the FPP player host with port sharing enabled can intercept fppd's own unicast sync stream, because the kernel load balances unicast datagrams by 4-tuple hash rather than fanning them out. That would place ShowMesh inside the timing path, which ADR-001 and the standing constraints forbid. This was first attributed to `SO_REUSEPORT` alone; CI on a Linux runner then showed that on Linux `SO_REUSEADDR` by itself is sufficient to share a UDP port and reproduce the same interception, so both options are now gated behind `AllowPortSharing` and the default path sets neither. Port sharing is off by default in the listener configuration, and the bench capture procedure warns against running the probe on the player host during a show. See [ADR-013](../decisions/ADR-013-no-fpp-control-port-sharing.md) and `docs/bench/RES-002-capture-procedure.md`.

## Step 2: Control plane skeleton

Status: complete (2026-08-10)

**Goal:** the coordinator and an agent can see each other and agree on state, with no show logic yet.

**Deliverables:**

- `pkg/mqttproto` with the ADR-008 topic conventions and versioned JSON envelopes.
- `pkg/capability` with the ARCHITECTURE §6 model.
- Node agent hello with retained capability advertisement, Last Will, and health heartbeat.
- Coordinator SQLite store per ADR-009 holding inventory and observed state.

**Acceptance criteria:**

- An agent appears in coordinator inventory after start.
- The agent resolves to `offline` after an unclean kill, via Last Will.
- The coordinator restores state from retained topics after its own restart.
- A retained heartbeat replayed into a fresh subscription never reads as healthy.

**Two corrections to the original criteria, made when they were first proven:**

The second criterion said `unknown`. That was wrong in a way worth recording rather than silently editing. A Last Will is positive evidence that the session dropped, so `offline` is the accurate verdict and `unknown` is for absence or contradiction of evidence. `unknown` would have been an under-claim.

But `offline` here means **the control-plane connection is gone, not that the node is dead or the show has stopped**. Under the standing constraint that a running show survives coordinator and broker loss, a node can be control-plane-offline while continuing to run a show correctly. Anything rendering this word to an operator must not imply otherwise, because "offline" on a dashboard reads as "dead".

The fourth criterion is new. It is not a nicety: it is the only one that exercises the rule the whole liveness design rests on, and it is invisible to unit tests, because there the retained flag is set by the test rather than by a broker. It was added so that deleting the test would visibly remove a criterion rather than quietly remove a guard.

**Bound by:** ADR-002, ADR-003, ADR-008, ADR-009, ADR-011, ADR-012, RES-008.

**Round 1, complete 2026-08-10:** `pkg/mqttproto` (topic conventions, versioned envelope, hello/health/last-will payloads, exported delivery policy), `pkg/capability` (identifier syntax, sets, canonical encoding), and the `internal/coordinator` split into `config`, `broker`, `httpapi`, and `readiness` with the wiring moved out of `main.go`. No MQTT client code, no persistence, and no agent behavior: none of this step's acceptance criteria are met yet, because all three require a broker.

**Round 2, complete 2026-08-10:** the coordinator's SQLite store, inventory, and liveness derivation; the agent's hello, Last Will, and heartbeat; and the integration harness. All four acceptance criteria pass against a real Mosquitto with the agent running as a real subprocess, in CI on every push and via `make test-integration` locally, both driving one script against the shipped broker configuration rather than a stand-in.

**Verified 2026-08-10:** `make check` with lint at 0 issues, race tests, a CGo-free build with zero cgo packages in the coordinator's dependency graph per ADR-012, cross compiles for `linux/amd64`, `linux/arm64`, `darwin/arm64`, and `windows/amd64`, and all six integration tests against Mosquitto 2.0.22. Not verified: anything on real show hardware. Nothing in this step raises any research record above L1.

**Why the harness was a deliverable and not a follow-up.** It caught three defects on its first run that the unit suite passed over, including one where the unit test asserting the correct ordering passed continuously while the runtime did the opposite, because the assertion was made against a fake connection rather than the real wiring. RES-009 failure testing reuses this harness.

**Known follow-up:** the heartbeat interval and staleness window are unmeasured ShowMesh hypotheses, labelled as such in code. They determine how quickly a failed node is noticed during a show, which is operator-visible, and nothing has measured what is appropriate. That belongs in [RES-009](../research/RES-009-failure-mode-testing.md).

**Why `readiness` is its own package:** health evidence is a coordinator concern rather than an HTTP one. Step 3 adds the SQLite store and the FPP collectors as readiness contributors, and had the report type stayed in `httpapi`, each of them would import the HTTP package to describe its own health. `readiness` depends on neither `broker` nor `httpapi`.

## Step 3: Read-only FPP observability

Status: complete (2026-08-11)

**Goal:** the first slice of real observability value, and the start of ARCHITECTURE Phase 0 / OBSERVABILITY Phase O1.

**Deliverables:**

- FPP collector over REST and MQTT.
- The observation model from OBSERVABILITY §4.1 with provenance, freshness, and expiry so stale evidence becomes `unknown`.
- Event history.
- A first read-only status API, designed as the versioned public contract required by [ADR-014](../decisions/ADR-014-operator-ui-is-an-api-client.md): documented, usable without the UI, distinguishing unsupported from uncollected from failed, and carrying provenance and freshness on every observation.
- A subscribable change stream alongside the snapshot API, since [OPERATOR-UI §6](../architecture/OPERATOR-UI.md#6-real-time-updates) forbids depending *solely* on aggressive polling. Transport (WebSocket or Server-Sent Events) is chosen here.

**Acceptance criteria:**

- The API is exercised end to end by a non-UI client, to prove it is usable without a browser rather than only believed to be.
- An interrupted change stream is followed by an authoritative snapshot re-fetch, not a resumed local model.

**Bound by:** ADR-003, ADR-011, ADR-012, ADR-013, ADR-014, [ADR-020](../decisions/ADR-020-control-api-shape-and-change-stream.md), [ADR-021](../decisions/ADR-021-read-api-authentication-posture.md), RES-012, RES-013. ADR-020 and ADR-021 were written out of this step.

**Why the contract work lands here and not in Step 4:** if the API is designed alongside the UI that consumes it, behavior settles in whichever layer is easier to change and the API quietly stops being independently usable. Step 3 finishing before UI work starts is what keeps ADR-014 real.

**Verified 2026-08-11:** `make check` with lint at 0 issues, race tests, a CGo-free build with zero CGo packages in the coordinator's dependency graph, cross compiles for all four supported targets, and the coordinator image building. `make test-integration` runs 28 tests against Mosquitto 2.0.22 with the agent **and the coordinator** as real subprocesses. `make test-integration-fpp` passes against a containerized FPP 9.5.3 and skips cleanly when none is reachable. Both acceptance criteria are proven: `showmeshctl` is exercised as a real subprocess against a real coordinator, and three separate interruption shapes (connection drop, coordinator restart, buffer-overflow reset) each prove an authoritative snapshot re-fetch. Verified by hand against the shipped image: absence states render honestly with an unreachable FPP, version negotiation answers `application/problem+json`, and SIGTERM exits 0 with the broker unreachable. Not verified: anything on real show hardware.

**Why the harness now execs the real coordinator binary.** Step 2's suite wired the coordinator's components in process, justified at the time by there being no read API to observe the process through. This step landed one, and the justification expired. That change is what caught two defects here that component wiring could not see: a 10 second HTTP write timeout that killed every change stream a few seconds after it connected, and a hub that re-broadcast every node on every tick because a collection timestamp was restamped on each render. Both are invisible to a test that never runs `Run`.

**On the acceptance criterion that the API be exercised by a non-UI client.** `showmeshctl` satisfies it, and the part that gives it teeth is an enforced import-graph test forbidding it from importing any coordinator package. Writing its decoders independently is what surfaced two contract defects during the build: single-resource endpoints returning bare objects with no `serverTime`, and an events response carrying fields the pinned wire shape did not mention. A client sharing the server's types would have agreed with the server about both.

**Known follow-up:** the integration suite still decodes some assertions through the server's own wire types, which is the pattern the step's own contract forbids. Raw-key assertions were added for the load-bearing cases (control-plane state, the evidence envelope's state and `observedAt`, and the events response's `gap`), and full de-typing is deferred.

### Decisions this step was required to make

All three were deferred to here by name, and all three are now recorded rather than left to emerge from implementation.

- **Change-stream transport: Server-Sent Events**, not WebSocket ([ADR-020](../decisions/ADR-020-control-api-shape-and-change-stream.md)). The deciding argument is the acceptance criterion below: with SSE, exercising the contract without a browser is `curl -N`, and a contract that needs a client library before anyone can look at it drifts towards private. The stream is also deliberately non-resumable: no `id:` field, per-connection sequence numbers, an authoritative snapshot after every interruption.
- **Authentication: an optional shared secret, off by default** ([ADR-021](../decisions/ADR-021-read-api-authentication-posture.md)), with a startup warning when unset. That ADR records plainly that one shared secret is not an identity and does not satisfy ARCHITECTURE §10.4, and it bars the first write endpoint until a superseding ADR decides a real mechanism.
- **The API warranted its own ADR**, rather than sitting behind ADR-014 as an implementation detail. ADR-014 settled what the API *is*; the transport choice, the non-resumability rule, the evidence-absence vocabulary, and the additive-only compatibility rule are durable constraints a future contributor could otherwise "improve" one at a time.

### Four narrowings, recorded rather than silently dropped

- **FPP MQTT ingestion is deferred.** This step's deliverable named REST and MQTT. REST supplies the whole signal set; FPP's own MQTT publishes largely the same status, and only when an operator has configured FPP to point at ShowMesh's broker, which cannot be verified here and which the containerized bench does not exercise. The collector is built behind a source interface so a second source slots in without reshaping the model. Owner's decision.
- **No MultiSync listener in the coordinator.** The collector reads FPP's REST report of its own MultiSync state instead of opening a socket. Coordinator-side multicast observation needs host networking, which ADR-012 recorded as deferred, and running a second listener near a player is what [ADR-013](../decisions/ADR-013-no-fpp-control-port-sharing.md) exists to prevent.
- **Observations are stored latest-only.** Event history is this step's deliverable; metric history, retention tiers, and downsampling belong to [RES-013](../research/RES-013-telemetry-storage-and-alerting.md), and guessing at them here would pre-empt that record. Event history does get bounded retention, because an unbounded table on an appliance disk is a fault, and those bounds are labelled as hypotheses.
- **No desired state, assignments, or reconciliation status in the API.** OPERATOR-UI §5 lists them as part of the API's eventual minimum; the coordinator does not model them, and shipping placeholder fields would let a client render a verdict no code computes. They arrive additively with the behavior behind them.

## Step 4: Read-only Operator UI

Status: complete (2026-08-11)

**Goal:** the first operator-facing surface, delivering the dashboard portion of ARCHITECTURE Phase 0 and OBSERVABILITY Phase O1. Read-only, per [OBSERVABILITY §2.5](../architecture/OBSERVABILITY.md#25-read-only-monitoring-comes-first) under [ADR-011](../decisions/ADR-011-context-aware-observability.md): monitoring is read-only before it controls anything.

**Deliverables:**

- A TypeScript SPA per [ADR-015](../decisions/ADR-015-typescript-spa-frontend.md), built to static assets, in its own container in the Compose bundle per [ADR-014](../decisions/ADR-014-operator-ui-is-an-api-client.md).
- Dashboard with content per OBSERVABILITY §6.2, scoped to the signals Step 3 actually collects.
- Node views and capability views, composed from advertised capabilities rather than fixed node classes.
- Desired versus observed state with reconciliation status and freshness on every panel.
- Event and fault history.
- Connection-state handling per OPERATOR-UI §7: visible disconnection, last-updated timestamps, no stale state presented as current, bounded-backoff reconnect, authoritative resnapshot on reconnect.
- Responsive layout including the phone as a primary surface, with the show-time high-contrast mode.
- API version negotiation with an explicit, actionable error on incompatibility.
- Generated API types derived from or verified against the coordinator's Go types.

**Acceptance criteria:**

- The full stack runs correctly with the UI container stopped and with it removed entirely.
- Killing the coordinator underneath a connected browser produces a visible disconnected state within a bounded interval, never a stale-looking healthy one.
- Restarting the UI container does not restart or disturb the coordinator.
- An unrecognized capability identifier renders as a generic panel rather than blanking or failing the view.
- A coordinator with an incompatible API version produces the explicit error, not a partial render.

**Bound by:** ADR-002, ADR-003, ADR-011, ADR-014, ADR-015.

**Out of scope here:** all write operations, the preview wall (blocked on RES-010), controlled-device configuration and control (ADR-016 and RES-014), authentication mechanism selection, and anything HA-related.

### Decisions this step was required to make

Both were left open by name and are now recorded in
[ADR-022](../decisions/ADR-022-operator-ui-serves-the-api-same-origin.md)
rather than left to emerge from implementation.

- **The UI container serves the API same-origin**, closing the question OPERATOR-UI §4 marked "open at this stage and to be settled when the UI is built". The deciding argument was operator-facing rather than architectural: the direct alternative needs a runtime base-URL document written at container start *and* a CORS allow-list whose misconfiguration is indistinguishable from an outage, and ADR-021 already names a reflected-origin misconfiguration on an unauthenticated API as a real exposure. The load-bearing rule attached to it is that **the proxy forwards credentials and never holds or mints them**, which is what stops the proxy becoming the security boundary that OPERATOR-UI §11 and ADR-014 both forbid.
- **The browser holds the ADR-021 shared secret in `sessionStorage`**, prompted by a `401`, with no login, identity, expiry, or logout. ADR-021 deferred this because answering it early might force a session model the superseding identity ADR would then have to unwind; the answer chosen is small enough to delete.

### What only a running browser could catch

Two defects survived 99 passing unit tests, three independent reviews, and a build of the shipped image.

- **The client could not make a single request in a browser.** `fetch` was invoked as `this.fetchImpl(...)`, so its receiver was the client instance. A real browser's `fetch` is a WebIDL operation on `Window` and answers any other receiver with `Illegal invocation`. Node's `fetch` does not check its receiver, so the entire suite passed either way, *including* the tests that drive a real `node:http` server. The lesson is narrower than "run the app": a test environment that differs from the deployment environment in one detail will report success on exactly that detail.
- **Evidence ages froze while the freshness notice advanced.** With the coordinator stopped for 100 seconds and the page untouched, the banner and the last-updated notice correctly reported the disconnection, while the evidence panel still read `current` and "observed just now". Ages were computed against the last response's `serverTime` as a fixed "now", so they stopped moving exactly when the operator most needs them to. This is the same family as the `observedAt` defect this project has now caught four times: a time presented more favorably than the evidence supports.

The fix keeps the coordinator's `state` verdict untouched, because that verdict has provenance and the UI inventing its own is what ADR-011 forbids, and corrects the age claim, which is what was actually false.

### Narrowings, recorded rather than silently dropped

- **No browser-driving end-to-end suite.** The five acceptance criteria were verified by hand against the running stack, with two real agent subprocesses advertising over the bundled broker and a fake version-2 coordinator for the version-negotiation criterion. Automating them needs a browser in CI and belongs to its own decision.
- **The phone layout is operator-verified; the desktop one needed the fix.** The gap recorded when this step first landed was the phone, because the development tooling could not force a phone-width viewport. Checking it on a real phone inverted the finding: the phone view was fine, and the **desktop sidebar was wrong**. `flex: 1` on a nav link is correct for the phone's bottom tab bar, where five links share the width evenly, but it was inherited into the desktop column and stretched each link to a fifth of the viewport height, so the links sat evenly spaced from top to bottom with no structure at all. Replaced with an OPERATOR-UI §8 group ("Monitor", the only one of Monitor/Control/Configure that exists) with intrinsically sized links stacked from the top. Control and Configure are deliberately not rendered as empty groups, for the same reason the dashboard renders no panel for a subsystem the coordinator does not model.

  Worth keeping: the one verification I could not perform was the one I assumed was broken, and the surface I never doubted was the broken one. Emulated viewports and a real device are different claims.
- **The dashboard renders only subsystems the coordinator models.** No empty audio, SMPTE, projector, weather, or preview panels, because an empty panel asserts that a subsystem exists and is not reporting, which is a false statement about the system. This is deliberately *not* the same rule as evidence absence within a modeled subsystem, which is always rendered with its state and reason.
- **The change stream carries no deletions in v1**, so a client's model can only shrink at a snapshot. Handled, and commented where a future contributor would otherwise add a delete handler.

## Step 5: Real FPP signals on the dashboard

Status: **complete, 2026-08-11.** The first work in this project exercised against real show hardware: the coordinator was run read-only against all three deployed FPP hosts and the operator's live broker, and every acceptance criterion below was demonstrated there rather than only in a fixture. See the [build log](BUILD-LOG.md) for what that does and does not license, the five ways the live probe corrected this specification before any code was written, and the SSE finding that is measured and deliberately left open.

Three narrowings, recorded rather than silently dropped: the change stream still re-sends a whole instance to report a single changed observation, which needs an ADR-020 decision on delta frames; observation rows are never pruned, so a removed cape leaves ghost port rows behind (RES-013 owns retention); and observation changes still produce no event-history entries.

**Goal:** Step 4 built a surface with almost nothing to display. Step 5 fills it with the four signal groups the operator actually looks at, collected from the real deployed fleet rather than from a container. Still read-only; ADR-021 rule 5 continues to bar any write endpoint.

**The fleet, probed read-only 2026-08-11.** Addresses, hardware, and versions are in [reference installation](../reference-installation.md). These hosts are reachable from the development machine. **Never issue a write, command, restart, or settings change against any of them.** The display is the operator's property and this step is read-only by design. The fleet is deliberately non-uniform and is expected to move to a 9.x release or the FPP 10 beta before the season, so anything verified here is verified against these versions only.

**Deliverables:**

- **Pixel-current collection against the real schema.** `GET /api/fppd/ports` returns a **heterogeneous array**: real ports carrying `{bank, col, enabled, ma, name, row, status}`, and smart-receiver positions carrying only `{col, name, row, smartReceivers}`. A missing `ma` must never decode to `0`, because zero milliamps is a plausible reading indistinguishable from a dark port; model absence as `unsupported` with a reason. `pixelCount` is optional and was absent on both boards. Element counts differ per board and must not be hard-coded. See [RES-011](../research/RES-011-pixel-current-diagnostics.md)'s live-probe section, which is the authority over the documented schema.
- **Playback state**: playlist, sequence, song, position, repeat mode, scheduler status, next scheduled playlist. `seconds_played`, `seconds_remaining`, `repeat_mode`, and `current_playlist.count`/`.index` arrive as JSON **strings** on the real player; a struct declaring integers fails to unmarshal the whole document and the collector reports the FPP unreachable, which is a decoding bug wearing a network fault's clothes. `next_playlist.playlist` reads `"No playlist scheduled."`, a human sentence in a data field.
- **Controller and network health**: `fppd` state, mode, `multisync`, bridging, channel I/O enabled, `powerBad`, `sensors[]` (model generically from `label`/`valueType`, do not hard-code "CPU temp"), `warnings[]`/`warningInfo[]`, `Utilization`, and per-host version, branch, OS, platform, and uuid. **Version skew across the fleet is itself a signal** and should be visible.
- **FPP MQTT ingestion**, the Step 3 deferral. A second source behind the existing collector interface, not a reshaping of the observation model. Retained deliveries carry no valid observation time: `observedAt` is `null` with state `unknown_age`, never filled from collection time. MQTT and REST will report overlapping signals, so precedence must be decided and documented rather than left to arrival order. FPP's own MQTT connection state is an observable signal; the player currently reports `configured: true, connected: false`. Pointing FPP at ShowMesh's broker is operator-side work.
- **API and UI surfacing.** New signals arrive additively under `/api/v1`; update `api/openapi.yaml` and keep the conformance test green in both directions; regenerate the UI types. Step 4's rule holds: a subsystem the coordinator does not model gets no panel, and these four groups are now modeled.

**Acceptance criteria:**

- Unit tests run against fixtures **captured from the real hosts**, including both port-array shapes and the string-typed numerics, not hand-written from this document.
- A missing `ma` never renders as a current reading, and a smart-receiver blind spot is visibly distinct from a measured zero.
- A collector decoding failure is distinguishable from an unreachable FPP, in the API and in the UI.
- MQTT-sourced and REST-sourced values for the same `SignalID` resolve by a documented precedence rule, verified by a test.
- A live read-only run against all three hosts, reporting what each signal actually resolved to.

**Bound by:** ADR-001, ADR-003, ADR-008, ADR-011, ADR-013, ADR-020, ADR-021.

**What this step may not claim.** Every `ma` currently reads `0` because the display is de-energized. That confirms shape and type and proves nothing about whether current telemetry works. **No code comment, log line, test name, or document may state that pixel-current monitoring is verified.** Raising RES-011 needs readings from an energized display with a known load.

**Out of scope:** every write operation; alerting and notification paths, metric history, and retention tiers (RES-013); the preview wall (RES-010); controlled devices (ADR-016, RES-014); projectors, UPS, and switch telemetry (RES-012); the house topology map; anything requiring the identity ADR.

## Step 6: Identity, authorization, and audit

Status: **complete, 2026-08-12.** [ADR-024](../decisions/ADR-024-identity-authorization-and-audit.md) was written and reviewed on 2026-08-11 as a design session, then built by five parallel seams and attacked by three reviews. See the [build log](BUILD-LOG.md) for what the reviews found, including a login denial-of-service that defeated the record's own governing constraint and was reproduced against the real binary before and after the fix.

**Four narrowings, recorded rather than silently dropped:**

- **Token issuance and non-admin principal creation are host-level subcommands, not HTTP endpoints.** ADR-024 decision 0 defines no write endpoint and this step adds no show write, so putting principal management on the API would have been a surface the record never specified. `issue-token`, `list-tokens`, `revoke-token`, `create-principal`, and `invalidate-all-sessions` are all local to the data volume, which also matches decision 9's posture that filesystem access is the proof of authority.
- **Decision 7 is not implemented**, and cannot be here: there is no macro format, no FPP plugin, and no node policy to carry it. It becomes an obligation on the step that adds the first consumer of `show:macro:run`, recorded in "Not yet sequenced" below.
- **Decision 11's same-transaction audit rule is not achieved.** The API package cannot reach the transaction boundary that identity and store own. It must close before the first fail-closed write endpoint.
- **Login CSRF at `POST /api/v1/session` is unaddressed by the record itself** and is recorded in ADR-024 rather than fixed here.

**Three gaps found only because someone tried to use the thing**, each implemented and reachable by nothing: `ClaimBootstrap` had no caller, so no first principal could be created; `IssueToken` had no caller, so no machine credential could exist and the FPP and CLI story was unusable; and `CreatePrincipal`'s only caller was hardcoded to `admin`, so the `scheduler` principal that decision 7 is written around could not be provisioned. A feature that compiles, tests green, and cannot be reached is the failure mode this step produced three times.

**Goal:** the critical path out of Phase 0. Every remaining roadmap item that does something rather than shows something is a write operation, ADR-021 rule 5 barred the first write endpoint, and ADR-024 lifts that bar. Nothing in ARCHITECTURE Phase 1 can start until this ships. This step adds **no write endpoint of its own**: it builds the mechanism that permits one, and the first actual write operation is the step after.

**Deliverables:**

- **Principals, roles, and scopes.** SQLite-backed principals with argon2id passwords at the parameters ADR-024 decision 1 fixes, API tokens stored as SHA-256 digests with an identifiable prefix and one-time display, and the scope check at the API boundary. Kind does not restrict credential form and role is independent of kind, so a human can mint a token for `showmeshctl`.
- **Sessions.** `POST /api/v1/session` minting an HttpOnly cookie, `GET /api/v1/session` returning principal, role, and effective scopes with a freshness, per-principal generation counter, device labels, and revocation. Sliding on any cookie-bearing request including reads.
- **CSRF, methods, and the bearer exemption**, exactly as ADR-024 decision 6 specifies: no state change reachable by `GET` or `HEAD`, `Sec-Fetch-Site: same-origin` required and rejected when absent for cookie writes, no `Origin`-versus-`Host` comparison, and the exemption keyed on the credential that actually authenticated rather than on header presence.
- **Login cost bounds**: a concurrency limit on argon2 verification and a per-source increasing delay. Never a per-principal lockout.
- **Bootstrap and recovery**: a single-use code written only to a file in the data volume, invalidated and deleted on first admin creation, with a loud persistent unclaimed-bootstrap banner. A host-level recovery subcommand unreachable over the API.
- **Audit.** Append-only, dispatch and outcome as separate correlated entries, replays recorded as replays, `/api/v1/audit` behind `audit:read`, retention bounded **in this step** rather than deferred to RES-013, and the blackout/stop/power-off safety class that proceeds with degraded attribution rather than being refused.
- **MQTT authorization.** `allow_anonymous false`, per-agent credentials with generated explicit `user` ACL blocks, a coordinator credential, an FPP publisher role, and a healthcheck principal with a `$SYS` grant. The first implementation used `%u` pattern ACLs and was wrong because Mosquitto applies them to every authenticated username, including the three fixed roles; the 2026-08-12 review repair replaced them with explicit agent-only grants and a real-broker regression. Credentials are generated per deployment at first run, never shipped in `deploy/`. Agents distinguish CONNACK `0x87` from unreachability and continue on cached fallback.
- **Fallback trigger.** ADR-024 decision 7: `401` and `403` are coordinator-unavailable-to-this-caller for fallback purposes, and distinguishable in evidence. This is the one deliverable with no code in the coordinator, and it must not be skipped for that reason.
- **Documentation that becomes wrong on the day this ships**: `api/openapi.yaml`'s `bearerAuth` description, `SECURITY.md`, and `deploy/README.md`. All three are accurate today and describe the retired posture. Update them with the implementation, not before.

**Acceptance criteria:**

- A write endpoint added temporarily for the test is refused with `401` unauthenticated, refused with `403` naming the missing scope for a `viewer`, and accepted for an `operator`, verified against the running stack rather than in a handler test.
- Revoking a session closes an open change stream and the client re-fetches a snapshot and surfaces an explicit authentication state. Verified in a real browser, which is the defect class Step 4 was burned by.
- A stale or unavailable scope list renders controls as unknown, never as enabled.
- `/healthz`, `/readyz`, and `/version` answer with reads closed and a container healthcheck stays green.
- A coordinator started with `SHOWMESH_API_TOKEN` still set refuses to start and names the migration.
- The full Compose bundle comes up with `allow_anonymous false`, including a green broker healthcheck and two real agent subprocesses, and an agent given a wrong credential reports evidence distinguishable from an unreachable broker.
- Blackout succeeds with the audit store failing; `config:write` is refused under the same condition.
- A cookie-authenticated write is rejected when `Sec-Fetch-Site` is absent, and a bearer-authenticated write from `curl` succeeds with no such header.

**Bound by:** ADR-024 above all, plus ADR-001, ADR-004, ADR-008, ADR-009, ADR-011, ADR-012, ADR-014, ADR-016, ADR-020, ADR-022, ADR-023.

**Two implementation notes surfaced by review, recorded so they are not rediscovered:** the API's CORS middleware advertises `GET, OPTIONS` and the catch-all route is registered as `GET`, so `POST /api/v1/session` answers `405` until both are changed. And `ui/nginx.conf` forwards `Cookie` and `Set-Cookie` correctly today and needs no change, but adding `proxy_cookie_path` or `proxy_cookie_domain` later breaks login in a way that presents as a session that does not stick.

**What this step may not claim.** Nothing here verifies anything about a running show. ADR-024's survivability argument is an argument from requirements, and RES-009 is unresearched. The failure cases ADR-024's consequences list are RES-009's work, not this step's.

**Out of scope:** every actual write endpoint; target-scoped authorization; message-level command authentication; OIDC and forward-authentication; node enrollment automation.

## Step 7: The first write endpoint

Status: **complete, 2026-08-12; one finding repaired 2026-08-13.** All four seams landed, were reviewed, and were repaired: seam 0 (the `identity.Service` atomic audit variant, all of schema v6, and login CSRF), seam A (the configuration write surface), seam B (node discovery and declaration), and seam C (the FPP command path). Six commits, then two review passes per seam on the stronger model, then two fix rounds. See the build log for what the reviews found, which is the part worth reading.

**The headline for a future session: every acceptance criterion below was re-verified against one running coordinator after the fixes, not against the test suite.** That distinction earned its keep three times in this step. A command that reported `confirmed` 179 microseconds after dispatch, an operator workflow that silently destroyed the configuration it was editing, and a coordinator that then refused to start, were all found by running the thing rather than by reading it or by testing it.

**The 2026-08-13 repair, recorded here because this section is the status authority and previously read as though nothing had changed since the 08-12 verification.** The `SHOWMESH_FPP_ENDPOINTS` migration failed closed on its audit write, so an unwritable `audit_log` exited the coordinator, which under the bundle's `restart: unless-stopped` is a restart loop with no API and no dashboard. A startup migration has no principal, so fail-closed protected nobody while costing everything. The migration is now deferred rather than fatal. The review of that fix then found its own sharper half: making the path survivable made a state reachable that had been impossible, and two surfaces still asserted things that were false in it, one of them a `409` whose documented remedy would have discarded the operator's only copy of the endpoint list. Both fixed, both re-proved against a real coordinator with a real administrator and token. This changes no acceptance criterion below and required no ADR change, because `AuditedWrite` still rolls the transaction back and only the process-level response moved. Full detail, including a test that asserted the right thing against the wrong string and passed under mutation, is in the [build log](BUILD-LOG.md) entry for that date.

**Goal:** the first write operation in the project, and with it the command model's first appearance in code. ARCHITECTURE Phase 1 opens with native FPP lifecycle commands. This step delivers exactly one of them, end to end, from an operator control through authorization, audit, dispatch, and confirmation by evidence. Step 6 built the mechanism that permits a write and deliberately added none; this step spends it once, on the smallest command that exercises every layer.

**Scope decision, made by the owner on 2026-08-12, then widened the same day once [RES-008](../research/RES-008-configuration-model.md) reported.** The step covers **three writes, not one**: a primitive FPP lifecycle command, the configuration write surface that RES-008 decision D1 requires, and node discovery per D2. Still **no macro**.

The no-macro line is what keeps the step bounded, and it is worth stating why rather than discovering it later. [ADR-024](../decisions/ADR-024-identity-authorization-and-audit.md) decision 7's fallback trigger attaches by name to the first consumer of `show:macro:run`, so it does not fire here. [RES-015](../research/RES-015-fpp-plugin-distribution-model.md) therefore also stays out, which matters more than it used to: that record now establishes that decision 7 cannot be discharged without shipping ShowMesh code onto an FPP host, on two independent grounds, so the macro step carries a plugin with it.

**Why three writes rather than one, and why this is not scope creep.** The three exercise genuinely different paths, and two of them are the only way to close a Step 6 obligation. ADR-024 decision 11 states two distinct audit rules: a **coordinator-local state change** writes its audit entry *in the same transaction*, and a **command dispatched outward** writes its entry *before dispatch*. The same-transaction rule is the one that is currently unachievable and must close before any endpoint fails closed on it. **A device command never exercises it. Discovery and configuration writes are exactly its shape.** So the coordinator-local writes prove the atomicity fix and the FPP command proves ADR-003 confirmation by evidence, and each covers a rule the other cannot reach.

**This step is larger than "add a handler," and the reason is worth recording.** `pkg/command` is a nine-line doc stub. There is no desired-state table in schema v5. Step 3 deliberately kept desired state, assignments, and reconciliation out of the API on the grounds that shipping placeholder fields lets a client render a verdict no code computes. So the first write endpoint is the first appearance of the ARCHITECTURE §8.1 command envelope, of [ADR-003](../decisions/ADR-003-desired-and-observed-state.md)'s desired/observed split in storage, and of confirmation by evidence. The endpoint is the smallest part of it.

### Two Step 6 obligations that must close before the endpoint, not alongside it

- **Decision 11's same-transaction audit rule.** `identity.Service` exposes `WriteAudit` as a standalone call today and the API sequences two operations, so an audit failure can leave a state change with no record that it happened. ADR-024 states this must close before the first endpoint that fails closed on it, and the layering is why it cannot be fixed in the API package: the transaction boundary belongs to `identity` and `store`. `identity.Service` grows an atomic variant.
- **Login CSRF at `POST /api/v1/session`.** ADR-024 records it as unaddressed by the record itself and names the step covering the first write endpoint as the one that must decide it deliberately. `SameSite=Lax` governs whether a cookie is *sent*, not whether one is *set*, so a cross-site form post to the login endpoint makes the victim's browser hold the attacker's principal, and makes the audit log attribute to it. If the answer is a durable constraint, amend ADR-024 rather than settling it in code.

### Deliverables

- **`pkg/command`, for real.** ARCHITECTURE §8.1 fixes the envelope: identifier, target, parameters, idempotency key, deadline, issuer, requested revision, confirmation method, and result. The idempotency key is required on every command, not on some of them.
- **Desired state and command records in the store, as schema v6 appended**, never edited in place. Editing v5 in place was safe in Step 6 only because v5 had never been pushed. It has been pushed now.
- **The first configuration table, and `SHOWMESH_FPP_ENDPOINTS` migrated into it** (RES-008 D1). Which FPP hosts exist and where they are is an operator-authored inventory decision that today lives in `.env` with no revision, validation history, export, or rollback. The deciding argument is not architectural: **a non-technical operator cannot be asked to edit an environment file**, and "it is only a file" is no defence when the person changing it is not the person who deployed it. The class rule is that anything an operator may reasonably need to change during the life of an installation belongs in the store behind a surface. Deployment **secrets** are the deliberate exception and stay in the environment. Migration must handle an existing deployment whose endpoints are still in `.env`, and must not lose them.
- **Node discovery, and the declaration it produces** (RES-008 D2). `nodes` rows are observations from agent hellos today, so a powered-off node is indistinguishable from a decommissioned one, and nothing can be validated against the inventory. An explicit discover action promotes discovered nodes to **declared** rows. This threads ADR-003 rather than crossing it: that ADR rejects *discovery as authoritative desired configuration*, and what happens here is discovery **proposing** while an operator action **promotes**. Authority stays with the operator; discovery only removes the typing.
- **Discovery never deletes, and this is the same rule Step 5 already established.** A discovery run that does not see a node **flags it in the UI and changes nothing**. Deletion is a separate operator action with a confirmation and a warning. The reason is concrete rather than theoretical: powered-off equipment is normal outside display hours for most installations, so auto-deletion on a discovery run would remove healthy nodes for being asleep. This is Step 5's pruning decision applied to inventory instead of observations, where only a complete poll may prune and a backed-off poll claims nothing, because deleting a source's evidence the first time it goes quiet is far worse than a stale ghost. Absence of evidence is not evidence of absence, in either subsystem.
- **One write endpoint**, consuming `identity.ScopeFPPCommand`, which Step 6 defined with no consumer for exactly this moment. Behind the whole Step 6 surface: authentication, a `403` naming the missing scope, CSRF for cookie writes with the bearer exemption keyed on the credential that actually authenticated, and an atomic audit write.
- **A separate FPP command client, and proof it does not weaken the collector's guarantee.** FPP invokes commands over GET at `/api/command/...`, which is why the collector forces `CheckRedirect` onto a defensive copy of whatever client it is handed and surfaces a 3xx as `collection_failed`. The command path deliberately issues that request. It must be a distinct client in a distinct package with its own posture, and the collector's read-only guarantee must be proven still intact rather than assumed, including a test that fails if the two are ever merged.
- **Confirmation by evidence, per ADR-003.** A command is not successful because FPP answered `200`. Dispatch, then confirm through the collector that observed state actually moved, against an explicit deadline. A confirmation that never arrives carries a state and a reason from [ADR-020](../decisions/ADR-020-control-api-shape-and-change-stream.md)'s vocabulary, never a null that renders as blank. Dispatch and outcome are separate correlated audit entries per decision 11, and an idempotent replay writes its own entry marked as a replay that dispatched nothing.
- **A `showmeshctl` subcommand and a UI control.** The CLI keeps the contract honest under its enforced import-graph test. The control is gated on the scope list, which renders `unknown` when stale or unavailable and never permissive (decision 12), and an action the principal may not perform renders disabled with a stated reason rather than being omitted.
- **`api/openapi.yaml` additively**, conformance-tested in both directions, UI types regenerated, CI diff check green.

### The target problem, which shapes how this step is verified

Every previous FPP step was verified against the deployed fleet because reads are safe there. **This step may never be pointed at it.** The Step 5 rule stands unchanged: no write, no command, no restart, no settings change, and no MQTT publish against any live host, because `falcon/player/<host>/command/run` is a topic FPP acts on. The first write path is therefore developed and demonstrated against `bench/fpp-multisync/`'s containerized `fppd`, which makes `make test-integration-fpp` load-bearing for the first time. It is deliberately out of the per-push path because its image is a full FPP source build; see the CI decision below.

### Decisions settled before the step starts

- **The primitive command is the zero-argument member of FPP's stop family**, decided 2026-08-12. Its effect is observable through signals Step 5 already collects, so confirmation by evidence is real rather than deferred; a command whose effect nothing observes would ship the dispatch half of ADR-003 and call it done. It needs no target parameter, which keeps it clear of ADR-024 decision 4's deliberate absence of target scoping. It is the safe direction, which is the right posture for the first write this project has ever shipped. And because it is a member of ADR-024 decision 11's blackout, stop and power-off safety class, **it exercises the audit exemption while the configuration and discovery writes exercise fail-closed**, so decision 11 gets both halves covered by one step instead of one.

  **Two corrections to this decision as originally written, both found by running the command against the bench `fppd` rather than by reading about it, and both recorded rather than quietly edited because they are the kind of detail a plan invents and an implementation then has to work around.** This paragraph named the command **`Stop Playlist`**, and *FPP has no such command*. Its stop family's zero-argument member is **`Stop Now`**; `Stop Gracefully` takes a `loop` argument and is therefore not the no-parameter command this decision described. `Stop Now` is what ShowMesh dispatches; `stopPlaylist` remains ShowMesh's own action name on its own wire, which is a deliberate distinction rather than an inconsistency. And this paragraph named the confirmation signal **`fpp.status.player_state`**, which the collector does not emit: the collected signal is **`fpp.status`**, carrying FPP's `status_name` value. The generalizable point is small but real: **a plan may name an external system's vocabulary only from that system's own output**, and both of these read as plausible until something ran.

  Rejected: `Volume Set`, despite `fpp.volume` being collected and giving an exact expected value rather than a state transition, because ADR-017 retires FPP's own audio output and a control for it would be built to be deleted. `Start Playlist`, because it needs a target, it is the higher-consequence direction, and it sits nearest the ADR-001 line about never becoming a second scheduler.

- **`make test-integration-fpp` does not enter the per-push path**, decided 2026-08-12. It becomes its own workflow, triggered on changes to the collector and command packages plus a nightly run. Its image is a full FPP source build, and on every push it would either slow the fast path or teach people to skip it. This step is the first work that genuinely depends on it, so the decision is made here rather than deferred again.

- **The agent fallback cache's trust model** is [ADR-025](../decisions/ADR-025-agent-fallback-cache-is-signed.md): signed rather than checksummed, keypair per deployment, verifying key pinned at enrollment and never fetched at boot, which is what keeps the mechanism working during the outage it exists for. **This step implements nothing of it**; there is no cache yet and no ADR-008 topic that carries configuration. It is named here because ADR-025 came out of this step's planning, and because its decision 4, that a verifying key the agent could rewrite is checksum-level protection wearing a signature's clothes, is the kind of thing implemented the easy way by someone who never read the record.

- **Login CSRF is strict**, decided by the owner on 2026-08-12 as seam 0 was specified, and amended into [ADR-024](../decisions/ADR-024-identity-authorization-and-audit.md) decision 6 rather than settled in code, because which requests may set a session cookie is a durable constraint. `POST /api/v1/session` and `POST /api/v1/bootstrap` require `Sec-Fetch-Site: same-origin` and are refused when the header is absent, which is the identical predicate every other write already carries.

  The deciding argument is that the alternative, rejecting the header only when it is present and wrong, protects the same browsers in practice and costs a second cross-site rule that reads almost like the first. A near-duplicate that diverges quietly is this project's recurring defect, and a security predicate is the worst place to keep one. The named cost: a `curl` login must pass the header, and a browser sending no `Sec-Fetch-Site`, meaning Safari before 16.4, cannot log in. Decision 6 already bars that population from cookie writes, so the cookie being refused could not have performed one.

**Nothing in this step's decision list is still open.**

### Build ordering

**These three writes are not as disjoint as they look, and treating them as parallel would collide.** All three add tables to schema v6, which is one append-only file. All three register routes in `internal/coordinator/api`. All three add UI. Step 6 ran five parallel seams successfully because they were genuinely disjoint; these are not, so the shape is one foundation seam alone, then three that are actually parallel.

**Seam 0, alone, blocking everything.** The `identity.Service` atomic audit variant, and the whole of schema v6 in one pass: configuration tables, the declared-versus-observed split on `nodes`, the command journal, and desired state. One builder owns the migration file and lands every table the other seams need; nobody else touches it. The atomicity fix belongs here rather than in a seam because two of the three writes fail closed on it, which makes it a prerequisite rather than a peer. Login CSRF lands here too.

Then, in parallel:

- **Seam A, configuration write surface.** `config:write` endpoints over seam 0's tables, `SHOWMESH_FPP_ENDPOINTS` migrated out of `.env` with a path for deployments that already have it set, and the UI. **This seam proves decision 11's same-transaction rule.**
- **Seam B, node discovery and declaration.** The discover action, promotion of observed nodes to declared, the flag-never-delete rendering, and deletion as a separate confirmed operator action.
- **Seam C, the command path.** `pkg/command`'s envelope, the FPP command client in its own package with its own HTTP posture, the `Stop Playlist` endpoint behind `fpp:command`, confirmation by evidence against the collector, the `showmeshctl` subcommand, and the UI control. The only seam touching FPP, and the only one needing the bench container.

Review then runs as it did for Step 6: a constraint review and a test-honesty review on the stronger model, given the diff and the named ADRs, instructed to break the work rather than read it.

**The failure mode to watch for is Step 6's, which it produced three times: a capability that compiles, tests green, and has no caller.** Each of these three writes needs its end-to-end invocation path exercised by hand before the step is called done, and for seam C that means against the bench `fppd` and never the live fleet.

### Acceptance criteria

- The endpoint is refused `401` unauthenticated, refused `403` naming the missing scope for a `viewer`, and accepted for an `operator`, verified against the running stack rather than in a handler test. Step 6 proved this with a temporary endpoint; this one is real.
- A `Stop Playlist` whose FPP request succeeds while observed state does not move is reported as unconfirmed with a reason, never as successful. Verified by issuing it against a bench `fppd` that accepts the command without changing state.
- `Stop Playlist` succeeds with the audit store failing, per ADR-024 decision 11's safety class, while a `config:write` under the same condition is refused. Both halves in one step.
- A replayed idempotency key dispatches nothing, returns the original result, and writes an audit entry marked as a replay.
- The state change and its audit entry fail together: with the audit store failing, a **configuration or discovery** write is refused and its state change is absent afterwards. This is decision 11's same-transaction rule, and it must be proven on a coordinator-local write, because that is the only shape that reaches it.
- **A discovery run that does not see a previously declared node leaves it declared**, flags it in the UI, and deletes nothing. Verified by declaring a node, stopping its agent, and re-running discovery.
- Deleting a declared node requires an explicit operator action and a confirmation, and is audited.
- An existing deployment with `SHOWMESH_FPP_ENDPOINTS` set in `.env` migrates into the configuration table without losing an endpoint, and the migrated deployment still collects from every host it collected from before.
- The collector's read-only posture is unchanged by the command path's existence, proven rather than asserted.
- The control renders `unknown` rather than enabled when the scope list is stale, verified in a real browser, which is the defect class Step 4 was burned by.
- The full stack still behaves correctly with the UI container removed, per the standing ADR-014 test.

**Bound by:** [ADR-001](../decisions/ADR-001-fpp-is-authoritative.md) above all, because this is ShowMesh invoking FPP's own command rather than becoming a second scheduler; plus ADR-003, ADR-004, ADR-009, ADR-011, ADR-014, ADR-020, ADR-022, ADR-023, ADR-024.

**What this step may not claim.** Nothing here verifies anything about a running show, and nothing here raises a research record. RES-009 gains rows from this step rather than losing them.

**Out of scope:** show macros and everything they force, including ADR-024 decision 7's fallback trigger; controlled devices; audio; and every command beyond the one.

## Step 8: The primitive command vocabulary

Status: not started. Specified 2026-08-13.

**Goal:** Step 7 shipped one command and proved the whole path around it. This step fills out the primitive vocabulary, so that the macro step has something real to sequence and so that ARCHITECTURE Phase 1's "native FPP lifecycle commands" means more than one of them.

**Why this comes before macros, decided 2026-08-13 after the alternative was raised by the owner.** [ADR-004](../decisions/ADR-004-layered-commands-and-fallback.md)'s formulation is primitives, then show macros, then reduced local fallback. A macro is a sequence of primitives and there is currently exactly one, the deliberately safe zero-argument stop. The first macro would therefore be either a sequence of one, or the primitives would get built inside the macro step. **The second is what actually happens, and Step 7 is the evidence**: it was specified as one write and shipped three, because the writes exercised rules that could not cover each other. The macro step already carries the FPP plugin, [RES-015](../research/RES-015-fpp-plugin-distribution-model.md)'s distribution question, ADR-024 decision 7, ADR-004's per-macro fallback definitions, and macro definitions as configuration objects. Adding the primitive vocabulary on top of that makes it unreviewable, which is the one thing this project's review-heavy build workflow cannot absorb.

**The cost of this ordering, stated so it is not discovered later:** [ADR-024](../decisions/ADR-024-identity-authorization-and-audit.md) decision 7's fallback trigger stays outstanding for one more step. That is tolerable only because it attaches by name to the first consumer of `show:macro:run`, so it cannot drift silently; it is Step 9's first-class obligation rather than a loose end.

### Deliverables

- **FPP's actual command list and its argument encoding, captured from the bench `fppd` before any command is named in this specification.** This is a deliverable, not a preliminary. Step 7's plan named the command `Stop Playlist`, and FPP has no command by that name; it named the confirmation signal `fpp.status.player_state`, and the collector does not emit that. Both read as entirely plausible until something ran, and both cost implementation time to work around. **A plan may name an external system's vocabulary only from that system's own output.** The command list this step ships follows from the capture; the capture does not follow from the list.
- **Argument support in `internal/coordinator/fppcommand`.** `Client.Invoke` today takes a command name and nothing else, building `/api/command/{name}` from a path-escaped name. Every command beyond the zero-argument stop needs argument encoding, and that encoding must match what the capture shows rather than what seems reasonable.
- **The primitives themselves**, chosen from the capture under the observability filter below.
- **A confirmation predicate and deadline per primitive.** These are not one rule. Stop confirms on a transition to idle; a start confirms on a transition into playing, and arguably on the playlist identity as well. Each is its own evidence question.
- **Idempotency and replay for parameterized commands.** Step 7 proved replay for a command carrying no parameters. A replayed key whose parameters differ from the original is a new question this step has to answer rather than inherit.
- **A `showmeshctl` subcommand and a UI control per primitive**, under the existing import-graph test and the existing scope-list gating that renders `unknown` rather than enabled.
- **`api/openapi.yaml` additively**, conformance-tested in both directions, UI types regenerated, CI diff check green.

### The filter on which primitives ship

**Only a command whose effect is observable through a signal the collector already collects.** Anything else ships the dispatch half of [ADR-003](../decisions/ADR-003-desired-and-observed-state.md) and calls it done, which Step 7 explicitly refused to do and which this step must not quietly reintroduce under the pressure of a longer command list. Where a command's effect is not currently visible, the honest options are that the collector grows the signal inside this step, or the command waits for a step that grows it. A command shipped without a confirmation path is not a smaller version of this step's work; it is a different and worse thing.

**The filter's exclusions are a deliverable, not a silent omission**, by the owner's decision on 2026-08-13. Every command in the capture that this step does **not** ship must be listed with the reason it was excluded, split into the two cases that have different owners:

- **Excluded because ShowMesh does not collect the signal yet.** These are ShowMesh's own work and are candidates for a later step, so each one records which signal would confirm it and where that signal would come from.
- **Excluded because FPP does not expose the effect at all.** These need upstream FPP work and are **out of scope for the foreseeable future**, which is the owner's standing position. Recording them is what makes that a decision rather than an oversight.

This exists because the filter is a rule about *absence*, and this project's recurring defect is absence stated as nothing at all. The same argument that makes an unsupported observation carry a state and a reason rather than being omitted from the API applies to a command the operator might reasonably expect to find and does not. The list belongs in the step's own record and should be readable by someone deciding whether a future step is worth it.

### The direction reverses in this step

Every write this project has shipped points at the show stopping, or at configuration. **A start command is the first thing ShowMesh can do that makes the show do something**, and it is the first command whose failure mode is the display running when it should not be.

This is where Step 7's confirmation lesson bites hardest. A stop dispatched into a running show could report success off a pre-dispatch `idle` reading; a start has the exact mirror, because FPP is the authoritative scheduler and may start a playlist on its own between the dispatch and the poll. Confirmation must rest on evidence that post-dates the dispatch and shows the state *moved*, never on the current value matching the desired one. The same rule, one file over, is why confirmation reads through `ResolveObservations` rather than the first matching row.

A second question this step must answer rather than discover: what a start means when issued against a host already playing something else. That is a real operator situation and the answer is a decision, not an implementation detail.

### Decisions settled before the step starts

Both were taken by the owner on 2026-08-13, and both are recorded here rather than amended into ADR-024, because neither changes a durable constraint: one re-affirms an existing deferral and the other declines to add a distinction the record already permits.

- **Target-scoped authorization is not implemented, and the deferral is no longer waiting on a consumer.** ADR-024 decision 4 delivers authorization by action and explicitly not by target, leaving ARCHITECTURE §10.4 partially satisfied, and its supersession clause says a future record must revisit this "once there is a consumer to design its taxonomy against." The owner's decision is that this installation has no such need and will not acquire one: it is one operator on hardware they own, and a second person helping would be trusted with full control rather than issued a narrowed grant. **The trigger is therefore declined rather than unmet.** It is revisited only if a genuinely multi-crew deployment asks for it, which is a feature request rather than an architectural gap. Recording it this way matters because Step 9 fires ADR-024's own stated trigger, and a future session reading that clause would otherwise reopen a question that has been answered.
- **`fpp:command` stays a single scope covering every primitive**, rather than splitting the safe direction from the show-affecting one. The deciding argument is that the role name already carries the meaning: `operator` means the principal operates the show, and starting it is operating it. The scope is held only by `operator` and `admin`, both of which should hold every command; `viewer` holds no write scope and `scheduler` holds only `show:macro:run`, so nothing gains a capability its role does not already imply. The concern this was weighed against, that adding a start command silently widens an existing grant, does not survive that check.

### Acceptance criteria

- Every primitive shipped is confirmed through a collector signal against the bench `fppd`, and a primitive whose effect does not appear within its deadline is reported unconfirmed with a stated reason, never as successful.
- **A start issued against a host already in the target state is confirmed only on post-dispatch evidence.** This is the mirror of Step 7's 179 microsecond defect and is the criterion most likely to pass falsely, so it is verified by timing the confirmation against the dispatch rather than by reading the code.
- FPP's argument encoding is proven against the bench `fppd` rather than asserted, and the command list in the shipped code matches the capture recorded in this step.
- A replayed idempotency key on a parameterized command dispatches nothing, returns the original result, and writes an audit entry marked as a replay. A replay whose parameters differ from the original is handled by a stated rule rather than by whichever branch happens to run.
- The collector's read-only posture is unchanged by the widened command surface, proven rather than assumed, including the existing test that fails if the two clients are ever merged.
- Every criterion above is verified against a running coordinator, not against the test suite.

**Bound by:** [ADR-001](../decisions/ADR-001-fpp-is-authoritative.md) above all, since every primitive here is FPP's own command invoked by ShowMesh rather than ShowMesh scheduling anything; plus ADR-003, ADR-011, ADR-014, ADR-020, ADR-023, ADR-024.

**The target problem is unchanged from Step 7 and is not relaxed by familiarity.** No write, no command, no restart, no settings change, and no MQTT publish against the deployed fleet. Every primitive in this step is developed and demonstrated against `bench/fpp-multisync/`'s containerized `fppd`. `make test-integration-fpp` stays load-bearing.

**Out of scope:** show macros and everything they force, including ADR-024 decision 7; the FPP plugin; controlled devices; audio; target-scoped authorization, per the decision above.

## Step 9: Show macros and the FPP plugin

Status: not started, and **fully unblocked as of 2026-08-13**. Its RES-008 prerequisite was discharged that day, and the open question below was answered the same day by the owner: **the first macros ship with no critical local fallback**, stated in the macro definition rather than left implicit, because the delivery path does not exist and building it is cut from day-0. Revisit after Halloween. **This step is day-0 scope**, not optional: show macros are one of the reasons the project was started.

**Goal:** the first show macro, and with it the first ShowMesh code running on an FPP host. This is where ADR-004's three-part model is finally complete: primitives from Steps 7 and 8, macros here, and the reduced local fallback that every critical macro must define.

**The RES-008 prerequisite is done, and it changed this step's shape twice.** [RES-008](../research/RES-008-configuration-model.md) section 3 was re-surveyed against schema v6 on 2026-08-13. Two findings matter here.

**The good one: macro storage is already solved.** `config_objects` and `config_revisions` are keyed by `(kind, id)` with the payload held as JSON, so a new configuration type is a new `kind` string and a payload schema, **not a schema migration**. A macro definition is already storable. Revisions are monotonic integers per object, which satisfies ADR-008's requirement that the `cmd` envelope's `revision` be short, stable, and JSON-safe. So this step's configuration work is a payload schema, a validator, and a surface. Note that exactly one `kind` exists today, `fpp.endpoints`, so the generic mechanism has been exercised against one shape and proven against no second one; this step is that second shape.

**The blocking one: there is no way to deliver a fallback to the node that would run it.** ADR-004 requires every critical macro to define what runs locally when the coordinator is unreachable. The survey establishes that the agent has **no cached fallback subset** (ADR-025 decided its trust model and Step 7 built none of it) and that **ADR-008's v1 topic set contains no configuration-distribution topic**. So a fallback definition has no path to a node. Under [ADR-020](../decisions/ADR-020-control-api-shape-and-change-stream.md) a macro carrying a fallback field that provably cannot be delivered is a field no code computes, which is forbidden.

**Answered by the owner on 2026-08-13: the second option.** The first macros are deliberately scoped to carry **no critical fallback**, and that limitation is stated in the step and in each macro definition rather than left for someone to discover when the coordinator drops mid-show. Building the distribution path, meaning the ADR-025 agent cache with its pinned verifying key plus an ADR-008 topic to carry it, is a large piece of work in its own right and is cut from day-0.

This is the shape the project has used before: Step 7 shipped the safe direction first and said so. The reason it had to be decided rather than defaulted into is that "the first macro has no fallback" is fine as a decision and dangerous as an accident, and the difference between the two is entirely whether it is written down.

### Deliverables

- **Macro definitions as versioned configuration objects**, under RES-008 section 4's decisions and ADR-009's revision, validation, export, and rollback rules.
- **`show:macro:run`**, the scope ADR-024 decision 4 defined with no consumer for exactly this moment.
- **ADR-004's reduced local fallback, defined per critical macro**, with any step touching a coordinator-hosted provider labelled coordinator-required per [ADR-016](../decisions/ADR-016-controlled-devices-and-control-providers.md).
- **[ADR-024](../decisions/ADR-024-identity-authorization-and-audit.md) decision 7's fallback trigger, discharged.** A `401` or `403` from a healthy coordinator fires no ADR-004 fallback, because a fallback detects a transport failure and an authorization refusal is a successful conversation. So the first macro definition must specify behaviour for a refusal, the node policy must treat it as coordinator-unavailable-to-this-caller, and it must be distinguishable in evidence from a network fault. This obligation has been outstanding since Step 7 and attaches here by name.
- **The FPP plugin**, per RES-015. That record establishes on two independent grounds that FPP's native command mechanism cannot discharge decision 7, so there is no version of this step that does not ship ShowMesh-authored code onto an FPP host.
- **An arbitrary MQTT publish step type**, added 2026-08-13 when the owner cut projector control from day-0. A macro step publishes an operator-authored payload to an operator-authored topic, so Home Assistant and Node-RED keep driving the projectors exactly as they did from FPP. This replaces `pkg/pjlink` and the ADR-016 provider model for day-0 and buys back the time both would have cost.

  **This step type cannot be confirmed, and that must be stated rather than hidden.** ShowMesh publishes and observes nothing back: there is no evidence the message was acted on, so [ADR-003](../decisions/ADR-003-desired-and-observed-state.md) confirmation is unavailable rather than merely slow. It reports as unconfirmable with a reason from [ADR-020](../decisions/ADR-020-control-api-shape-and-change-stream.md)'s vocabulary, never as success, because **a macro step that always reports success is worse than no step at all**: the operator stops reading it, and then it is silently useless on the night it matters. It is also the one step type that deliberately fails Step 8's rule about only shipping commands whose effect is visible, which is why it is called out here rather than slipped in.

### Developed against the container, by owner's decision 2026-08-13

The plugin is developed and demonstrated against `bench/fpp-multisync/`'s containerized `fppd`, not against the deployed fleet. The reasoning is the owner's and is the same one that has governed the bench since Step 1: a working system in a controlled environment first, and differences between the container and real hardware addressed when the plugin lands on hardware.

**What that licenses, and what it does not.** It licenses the plugin mechanism, the callback boundary, the refusal semantics, the macro path end to end, and decision 7's fallback behaviour, all at bench level. It does **not** license the on-host install path, filesystem permissions, packaging, or FPP version compatibility across the fleet: RES-015 records that every one of its acceptance criteria still needs the bench Pi, and the deployed fleet is not uniform, running 9.4 on two hosts and a master-branch build on the third. **This step therefore completes without raising RES-015 above L1**, and the on-host criteria are a stated deferral carried into whichever step first installs the plugin on real hardware, rather than something discovered at that point.

### Accepted risk, recorded rather than mitigated

The plugin needs a ShowMesh credential on an FPP host, and RES-015 section 7.4 establishes that **an FPP host cannot keep a secret from anyone who can reach it.** That credential holds `fpp:command` in full, because Step 8 declined both target scoping and a scope split. So anyone with access to any FPP host has the command authority of that credential across the fleet.

This is accepted, not overlooked. The installation is single-operator on owned hardware on an isolated show network, which is the same reasoning `SECURITY.md` already records for accepting cleartext on the show LAN for commands and telemetry. It is written here so that the day this software runs somewhere with a crew, the accepted risk is findable rather than reconstructed. **This is the point at which ADR-024's supersession trigger for target-scoped authorization fires, and it is deliberately declined** under Step 8's recorded decision.

**Bound by:** ADR-001, ADR-003, ADR-004, ADR-009, ADR-011, ADR-014, ADR-016, ADR-020, ADR-024, RES-008, RES-015.

**Out of scope:** controlled devices and providers; audio; the deployed fleet.

## Not yet sequenced

These deliberately come later, and why:

- **Show macros are now Step 9**, sequenced on 2026-08-13 and no longer in this list. ADR-024 decision 7 remains outstanding and is that step's first-class obligation rather than a floating one.
- **Operator UI write operations beyond the first.** Controls, overrides, and macro invocation arrive with the steps that supply the behaviour behind them: per-primitive controls in Step 8, macro invocation in Step 9. Nothing renders a control for a capability no code implements, which is the Step 3 rule about placeholder fields applied to the write surface.
- **Controlled devices and control providers.** [ADR-016](../decisions/ADR-016-controlled-devices-and-control-providers.md) settles the model; the metadata contract and the metadata-generated-surface hypothesis are unresearched in [RES-014](../research/RES-014-control-provider-model.md), and the first provider (projectors, `pkg/pjlink`) also depends on RES-012 bench work.
- **Audio engine and audio node.** The architecture is decided ([ADR-017](../decisions/ADR-017-showmesh-owns-audience-audio.md), [ADR-018](../decisions/ADR-018-program-and-ltc-share-a-clock-domain.md), [ADR-019](../decisions/ADR-019-audio-device-loss-fails-silent.md), [AUDIO-ENGINE.md](../architecture/AUDIO-ENGINE.md)) and entirely unverified. It is not sequenced because [RES-007](../research/RES-007-audio-node-architecture.md) is critical-risk at L0, the multichannel interface the design depends on has not been purchased, and nothing here can be raised above L0 by unit tests: whether GStreamer holds LTC sample-aligned to program, and what drift a free-running node accumulates over a show, are bench facts. The first task is the RES-007 prototype on the intended host and interface, and sequencing follows its result. ADR-018 is also a purchasing constraint — at least three output channels from one clock — and should inform the interface selection before it happens.
- **Resolume adapter.** Blocked on RES-001 bench work (Resolume SMPTE and clip-launch behavior is still L0/L1).
- **GStreamer pipeline supervision and NDI transport.** The v1 renderer profile, transport roles, and NDI distribution architecture are settled in RES-004, RES-005, and RES-006. Sequencing still waits for their practical bench evidence: sustained 40 fps and frame pacing on the reference x86 renderer, observed NDI alignment/stability into Resolume, and sender-to-Resolume validation on `amd64` and `arm64`.
- **Preview delivery.** Blocked on RES-010.
- **Pixel-current diagnostics.** Blocked on RES-011.

## Standing constraints for every step

- FPP stays authoritative; ShowMesh never becomes a second scheduler.
- The coordinator is never in the timing or media path.
- A running show survives coordinator loss and broker loss. If every browser running ShowMesh disappeared at this instant, the show continues correctly.
- Commands need evidence of effect, not acknowledgement of receipt.
- Stale evidence is `unknown`, never healthy.
- New durable constraints require a new ADR, not an edit to the architecture spec.
