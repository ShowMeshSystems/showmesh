# Build Plan

[Documentation index](../README.md) · [Architecture specification](../architecture/ARCHITECTURE.md) · [Operator UI specification](../architecture/OPERATOR-UI.md) · [Research tracker](../research/README.md)

## How this relates to the roadmaps

ARCHITECTURE §12 defines outcome phases (Phase 0 through Phase 4). OBSERVABILITY §14 defines observability delivery phases (O1 through O5). Neither document commits to an implementation order within itself; both describe what must be true when a phase is reached, not the sequence of engineering steps that gets there.

This document is that sequence. It follows CLAUDE.md's walking-skeleton build order: protocol and timeline first, then the control-plane skeleton, then read-only observability, each step producing something runnable before the next step starts.

This is a working document, not an architectural contract. It records intent and status for implementation planning, and it must never contradict an accepted ADR. When a step's design turns out to conflict with an ADR, the ADR wins; either the step is redesigned or a superseding ADR is written first.

Status vocabulary: `not started`, `in progress`, `blocked`, `complete`.

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

## Not yet sequenced

These deliberately come later, and why:

- **Operator UI write operations.** Controls, overrides, and macro invocation follow the read-only release, and the initial authentication mechanism must be decided before the API gains write endpoints (OPERATOR-UI §14).
- **Controlled devices and control providers.** [ADR-016](../decisions/ADR-016-controlled-devices-and-control-providers.md) settles the model; the metadata contract and the metadata-generated-surface hypothesis are unresearched in [RES-014](../research/RES-014-control-provider-model.md), and the first provider (projectors, `pkg/pjlink`) also depends on RES-012 bench work.
- **Audio engine and audio node.** The architecture is decided ([ADR-017](../decisions/ADR-017-showmesh-owns-audience-audio.md), [ADR-018](../decisions/ADR-018-program-and-ltc-share-a-clock-domain.md), [ADR-019](../decisions/ADR-019-audio-device-loss-fails-silent.md), [AUDIO-ENGINE.md](../architecture/AUDIO-ENGINE.md)) and entirely unverified. It is not sequenced because [RES-007](../research/RES-007-audio-node-architecture.md) is critical-risk at L0, the multichannel interface the design depends on has not been purchased, and nothing here can be raised above L0 by unit tests: whether GStreamer holds LTC sample-aligned to program, and what drift a free-running node accumulates over a show, are bench facts. The first task is the RES-007 prototype on the intended host and interface, and sequencing follows its result. ADR-018 is also a purchasing constraint — at least three output channels from one clock — and should inform the interface selection before it happens.
- **Resolume adapter.** Blocked on RES-001 bench work (Resolume SMPTE and clip-launch behavior is still L0/L1).
- **GStreamer pipeline supervision and NDI transport.** Blocked on RES-004 (renderer performance), RES-005 (NDI vs. HDMI transport), and RES-006 (Linux NDI support), all unresearched or L1 only.
- **Preview delivery.** Blocked on RES-010.
- **Pixel-current diagnostics.** Blocked on RES-011.

## Standing constraints for every step

- FPP stays authoritative; ShowMesh never becomes a second scheduler.
- The coordinator is never in the timing or media path.
- A running show survives coordinator loss and broker loss. If every browser running ShowMesh disappeared at this instant, the show continues correctly.
- Commands need evidence of effect, not acknowledgement of receipt.
- Stale evidence is `unknown`, never healthy.
- New durable constraints require a new ADR, not an edit to the architecture spec.
