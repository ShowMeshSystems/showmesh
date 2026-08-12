# Build Log

[Documentation index](../README.md) · [Build plan](BUILD-PLAN.md) · [Architecture specification](../architecture/ARCHITECTURE.md)

## Format

This is a chronological session log so a future session (or a different contributor) can resume work without reconstructing context from scratch. It is not a substitute for the ADRs, the research records, or BUILD-PLAN.md; it links to them rather than restating their content.

The **Current state** block at the top of this file is overwritten each session: it always describes the repository as of the most recent session, not a history of how it got there. History lives in the dated entries below it, appended in reverse chronological order (newest first).

### Session entry template

```
## YYYY-MM-DD

**Goal:** what this session set out to do.

**Completed:** what was actually finished, with file paths.

**Decisions made:** any decision recorded, and where (ADR number, research record, or this log if it doesn't rise to either).

**Questions raised with the owner:** question and answer, if any came up.

**Deferred:** anything deliberately left for later, and why.

**Verification gates:** state of tests, CI, and any acceptance criteria from BUILD-PLAN.md touched this session. Only report gates actually run this session.
```

---

## Current state

Steps 0 (Foundation), 1 (`pkg/multisync`), 2 (control plane skeleton), 3 (read-only FPP observability plus the versioned public API), 4 (the read-only Operator UI), and 5 (real FPP signals on the dashboard) are complete.

**Step 5 is the first work in this project exercised against real show hardware.** The coordinator was run read-only against all three deployed FPP hosts and the operator's live MQTT broker, and every acceptance criterion was demonstrated there rather than only in a fixture. What that does and does not license is in the Step 5 entry below; the short version is that it proves shape, type and modelling against *these* versions, and proves nothing about a running show. RES-002 stays L2 for protocol semantics and L1 for anything hardware-dependent, unchanged. **RES-011 is not raised**: every `ma` on the fleet reads 0 because the display is de-energized.

There are now two collectors behind one interface. `internal/coordinator/collector/fpp` polls the REST API of each configured instance; `internal/coordinator/collector/fppmqtt` subscribes read-only to FPP's own MQTT status topics on the operator's existing broker. They report overlapping signals on purpose, and `internal/coordinator/api/precedence.go` resolves them by a documented rule at read time. Storage is schema v4, whose primary key gained `source` so neither collector can silently overwrite the other's evidence.

**Four things a future session should know before touching this code.**

**GET-only is not read-only.** The collector never issues a non-GET, and that guarantee turned out to be nearly worthless on its own: FPP invokes commands over GET at `/api/command/...`, and neither HTTP client set `CheckRedirect`, so a 302 from a spoofed or compromised FPP would have walked the coordinator's own GET onto another host's command endpoint. Reproduced against a recorder before it was fixed. `CheckRedirect` is now forced onto a defensive copy of whatever client is in play, including a caller-supplied one, and a 3xx surfaces as `collection_failed`.

**A JSON `null` is not an absent key, and every extractor treated it as one.** `encoding/json` unmarshals `null` as a no-op returning no error, so `"ma": null` produced a measured `0 milliamps` on a smart-receiver position, `"powerBad": null` produced `false` and contributed *healthy*, and `"warnings": null` fabricated "no warnings". The blind-spot rule had been implemented for a missing key and not for a key present as null. Every extractor now rejects an explicit null.

**Health had to change before these signals could land, and it hid a fault while it was at it.** Every observation was a critical member of the instance verdict, so the many legitimately `unsupported` signals this step adds would have pinned all three real hosts at `unknown` forever. Worse, the mapper never looked at a value, only at whether the state was `current`, so `fpp.power.bad == true` contributed *healthy*. Now `unsupported` contributes nothing, only `fpp.reachable`, `fpp.fppd.state` and `fpp.power.bad` are health-critical, and a `healthy` verdict additionally requires `fpp.fppd.state` to be present and reading `running`, because "the HTTP server answered" is not evidence that the player is well.

**The change stream re-sends an entire instance to report a four-field delta, and that is not fixed.** Measured against the live fleet at roughly 57 KB/s for three hosts. See the Step 5 entry; it is recorded as measured, unresolved, and deliberately out of scope.

Step 4 detail follows.

There is now an operator-facing surface. `ui/` is a React and Vite TypeScript SPA served from its own `nginx:alpine` container, which also forwards `/api/*` to the coordinator so the browser sees one origin ([ADR-022](../decisions/ADR-022-operator-ui-serves-the-api-same-origin.md)). It renders the dashboard, node and capability views, FPP instances, and event history, all composed from advertised capabilities rather than fixed node classes. Its API types are generated from `api/openapi.yaml` and CI fails on any diff, which makes ADR-015's "generated from or verified against the Go types" a gate rather than an intention: the OpenAPI document is already conformance-tested against real handler responses in both directions.

**Three things a future session should know before touching the UI.**

**A test environment that differs from the deployment environment reports success on exactly that difference.** The client invoked `fetch` as `this.fetchImpl(...)`, so its receiver was the client instance. A browser's `fetch` is a WebIDL operation on `Window` and answers any other receiver with `Illegal invocation`; Node's does not check. The app could not make a single request in Chrome while 99 unit tests passed, including the ones driving a real `node:http` server. Three reviews and a build of the shipped image did not find it. Loading the page did, immediately.

**Ages must keep advancing when responses stop.** Evidence ages were computed against the last response's `serverTime` as a fixed "now", so with the coordinator stopped for 100 seconds the banner and the last-updated notice correctly reported the disconnection while the evidence panel still read `current` and "observed just now". Ages now advance from the last `serverTime` by real elapsed browser time, which keeps clock skew visible while making elapsed time true, and evidence carries an explicit as-of qualifier while disconnected. The coordinator's `state` verdict is deliberately left untouched: it has provenance, and the UI inventing its own is what ADR-011 forbids.

**`env_file` loads every variable in the file, not the ones you named.** The UI service declared only two environment variables and inherited `SHOWMESH_API_TOKEN` and the broker password alongside them, readable through `docker inspect`. Three separate comments in the bundle asserted that the container never holds the token. ADR-022 decision 2 forbids *holding* it, not merely injecting it as a header, because holding it makes reaching the UI equivalent to reaching the API. Compose still interpolates `${VAR}` from `.env` without `env_file`, so removing it cost nothing.

Timeouts, backoff bounds, idle deadlines, retained-event caps, and the clock-skew warning threshold in the UI are unmeasured ShowMesh hypotheses, labelled as such in code, and belong to RES-009 and RES-013 along with their coordinator-side counterparts.

Step 3 detail follows.

The coordinator now polls configured FPP instances read-only over REST, normalizes what it finds into the OBSERVABILITY §4.1 observation model with provenance and freshness, persists observations and an event history in SQLite, and serves all of it through a versioned public API at `/api/v1` with a Server-Sent Events change stream. `showmeshctl` is a second, deliberately independent client of that API: an enforced import-graph test forbids it from importing any coordinator package, so a JSON tag rename breaks it rather than silently renaming the field on both sides. `api/openapi.yaml` is the machine-readable contract and is conformance-tested against real handler responses in both directions.

There are still no commands, no write operations of any kind, and no show logic. Step 4 supplied the UI, and its version negotiation is now exercised for real against a coordinator that refuses the client's version.

RES-002 stays **L2 for protocol semantics** and **L1 for anything hardware- or network-dependent**, unchanged by this step. Step 3 added REST-level evidence about FPP (recorded in RES-002's evidence section) but nothing about the MultiSync wire protocol, the hardware clock, or the reference switch. L2 licenses further building, not a live show.

Three things a future session should know before touching this code. **`MultiSyncEnabled` defaults to off in FPP**, and the endpoint that looks like it reports that setting returns the setting's *schema*, which decodes to `false` without error: correct today by accident, wrong the moment MultiSync is enabled, and its test passes either way. The collector reads the running daemon's own `multisync` flag instead, and a test panics if anything ever calls the settings endpoint. **A retained MQTT delivery carries no valid observation time**, so `observedAt` is `null` with state `unknown_age` and must never be filled in from the collection time; that defect has now been introduced and caught three times in different disguises. And **`offline` means the control-plane connection is gone**, not that the node is dead, which is why the wire field is `node.controlPlane.state` and nothing else.

The heartbeat interval, staleness window, poll cadences, backoff bounds, SSE keepalive interval, and event retention bounds are all unmeasured ShowMesh hypotheses, labelled as such in code. They belong to RES-009 and RES-013.

`pkg/multisync` holds the MultiSync wire codec, a listener that receives multicast, broadcast, and unicast on UDP 32320, a timeline state machine implementing FPP remote semantics on an injectable clock, and an opt-in discover-ping responder. `cmd/showmesh-multisync-probe` is the bench instrument built to close RES-002's five open items; it has not been run against a real FPP player yet, so RES-002 remains at L1 and its status is still `planned`. Changing that status is the owner's call once real captures exist, per `docs/bench/RES-002-capture-procedure.md`.

Step 0 detail follows.

Step 0 (Foundation) is complete and verified. The repository builds, tests, and lints clean; the coordinator image builds for `linux/amd64` and `linux/arm64` and runs; the Compose bundle brings up Mosquitto and the coordinator together and the coordinator reaches ready. The coordinator survives an unreachable broker, a broker stopped and restarted underneath it, and SIGTERM in every one of those states. There is no show logic, no MQTT topic work, and no persistence yet: `/healthz`, `/readyz`, and `/version` are the entire surface.

The repository is pushed to `github.com/ShowMeshSystems/showmesh`, currently private, and CI has run on a real GitHub runner. It earned its keep on the first run by failing a test that passes on macOS, which is what exposed the `SO_REUSEADDR` behavior difference recorded in ADR-013.

No audio code exists. The Audio Engine was specified in the same session (ADR-017..019, `docs/architecture/AUDIO-ENGINE.md`, RES-007 rewritten) and deliberately left unsequenced: RES-007 is critical-risk at L0, the multichannel interface has not been purchased, and every load-bearing claim is a bench fact. The next audio action is the RES-007 prototype, not implementation.

**The next action is the identity and authorization ADR.** Nothing else of consequence can start without it, and the reason is structural rather than a matter of preference.

Beyond that the sequencing has one dominant feature worth stating plainly. **Every remaining roadmap item that does something rather than shows something is a write operation, and ADR-021 rule 5 bars the first write endpoint** until a superseding record decides authenticated identities, authorization by target and action, audit attribution, the MQTT control plane's own authorization, and a browser session model. ARCHITECTURE Phase 1 is entirely write work. So the identity and authorization ADR is the critical path out of Phase 0, not a chore to fit in later. Other work available without it: RES-008's configuration model, and `pkg/pjlink` as a protocol library at L1.

Separately, the probe is ready to run against the real FPP player whenever the owner has bench time. That is what moves RES-002 from L1 to L2, and RES-002 is the highest-risk research record in the project.

The third-party product name discussed under "Conflicts found" in the audio session entry below had been removed from the working copy of `docs/reference-installation.md` but remained in the git history of the initial commit, and therefore on the remote, because removing a line from the working tree does not remove it from history. History was rewritten on 2026-08-10 to carry the neutral wording from the initial commit onward, every reachable object was re-scanned to confirm no blob or commit message still contains it, and the result was force-pushed. All commit hashes changed at that point; anything referencing a pre-rewrite hash is stale.

---

## 2026-08-11 (Step 5)

**Goal:** fill the Step 4 surface with the four signal groups an operator actually looks at, collected read-only from the deployed fleet rather than from a container. Still read-only; ADR-021 rule 5 continues to bar every write endpoint.

**Completed:**

- `internal/coordinator/collector/fpp` extended to `/api/fppd/ports` and `/api/system/info`, with the full playback, controller-health, platform and per-port signal vocabulary. Its decoders are exported as pure `SignalValue` functions that know no clock.
- `internal/coordinator/collector/fppmqtt`: a second collector behind the same interface, subscribing read-only to FPP's own MQTT status topics on the operator's existing broker. Push-to-poll: a subscription callback keeps the latest message per topic with its retain flag, and `Poll` renders it.
- Store schema v4: `source` added to the observations primary key.
- `internal/coordinator/api/precedence.go`: read-time resolution of multi-source evidence.
- `deriveInstanceHealth` rewritten; see the decisions below.
- `ui/`: signal grouping, a port grid distinguishing measured current from a smart-receiver blind spot from a collection failure, warnings, version skew, and four new dashboard panels.
- `make test-integration-fppmqtt` against a throwaway local broker.

**The live probe came first, and it corrected the specification before any code was written.** BUILD-PLAN's Step 5 was written from an earlier partial probe. A fresh read-only pass over all three hosts found five things it did not have, each of which changed the work:

- **Three different `/api/fppd/ports` shapes on one fleet.** FPP-Main returns `[]`, FPP-remote-01 returns 32 elements (16 with `ma`, 16 smart-receiver), FPP-remote-04 returns 48 (16 and 32). An empty array is a Pi with no output cape and is a measured zero, not a failure and not an absence.
- **Player and remote report structurally different status documents.** `current_playlist`, `next_playlist`, `scheduler` and `repeat_mode` are absent on both remotes, replaced by `playlist`, `sequence_filename`, `media_filename`, `seconds_elapsed`. The collector had been reporting those absences as `collection_failed`, which is false: nothing failed.
- **The fleet was already on a broker, and not ours.** All three report `MQTT: {configured: true, connected: true}` against the operator's existing home-automation broker. BUILD-PLAN recorded `connected: false`. Repointing them is a settings write to a live show host, so the collector subscribes to the foreign broker instead.
- **A fourth FPP exists only as retained state.** `FPP-01`, branch v9.2, is not in the reference installation. Every one of its topics arrived retained and it published nothing live in a 60-second capture: a complete, plausible, healthy-looking status document of entirely unknown age. It became this step's best acceptance test.
- **`GET /api/settings/MQTTPrefix` returns a schema with no `value` key at all**, while `MQTTHost` and `MQTTUsername` have one. The RES-002 trap, live, on two more settings.

**Decisions made:**

- **The FPP MQTT collector subscribes to the operator's existing broker** rather than requiring FPP to be repointed at ShowMesh's. Chosen because the alternative is a settings write to a live display. It is a collector source and must never be merged with the ADR-008 control plane, which is stated in the package doc comment because merging them later would look like a tidy-up.
- **The connection is subscribe-only by construction**, not by convention: the client sits behind an interface with no publish method, no Last Will is registered, and the subscription filters are an explicit per-suffix list rather than a host-scoped `#`. That last one was a builder's deliberate deviation from the specification and it was right: `falcon/player/<host>/#` would still have matched `command/run`, which FPP acts on.
- **Both sources emit the same `SignalID` for the same fact, and the store keeps both rows.** Resolution happens once, at read: a value with a known observation time beats a value of unknown age, which beats an absence; within the first tier the later observation wins; ties break toward `fpp-rest`, because a REST value came from a round trip this coordinator initiated while an untimed MQTT value is a replay. Adding `source` to the primary key rather than arbitrating at write time is an ADR-011 consequence: discarding evidence on the way in destroys provenance and makes the rule untestable from outside the process.
- **Only three signals are health-critical**, and `fpp.warnings.*` is deliberately not among them. FPP's own list mixes "A Log Level is set to Debug" with "Cannot Ping ArtNet Channel Data Target"; classifying those strings would be ShowMesh inventing a verdict from text it does not understand. Warnings are surfaced prominently and never colour the badge.
- **A `healthy` verdict requires `fpp.fppd.state` to be present and running.** `fpp.reachable` alone was sufficient, which means an instance could report healthy on the strength of its HTTP server answering. Not reachable in today's code, but one modelling decision away, and this step made exactly that kind of decision five times.
- **An unreachable instance reports `unknown`, never `failed`.** The Step 5 specification's own table said `failed`; the code was right and the table was wrong, because Step 3 decided this deliberately and nothing here supplies the lifecycle context that would justify a more confident verdict.
- **Observation changes still produce no `/api/v1/events` entries.** Which transitions deserve durable history is an OBSERVABILITY question this step did not answer, and inventing one here would fill the event log with playlist churn.

**One piece of L1 verification, because absence and emptiness are different claims.** FPP-remote-04 omits `warnings` from its REST status while its MQTT topic publishes `[]`. Rather than guess which FPP means, the builder read `src/httpAPI.cpp` at commit `7e3c6acb0`, which is the `RemoteGitVersion` FPP-Main itself reports, and found the field built only inside `for (auto& warn : WarningHolder::GetWarnings())`. The key is never created when the list is empty. Independently re-verified from the FPP repository during fold-in, at lines 120-124 of that file. Recorded in RES-002's evidence section.

**What the live read-only run actually produced**, against three hosts and the broker:

| Instance | Health | Observations | Note |
|---|---|---|---|
| `fpp-main` | healthy | 51 | `fpp.ports.count` = 0, measured |
| `fpp-remote-01` | healthy | 253 | 32 port elements |
| `fpp-remote-04` | healthy | 351 | 48 port elements |
| `fpp-01` | unknown | 61 | retained-only ghost, every signal `unknown_age`, none with a non-null `observedAt` |

On FPP-remote-04 all 48 `current_ma` signals resolved correctly: 16 `current` at 0 mA with unit `milliamps`, and 32 `unsupported` carrying no value at all. Precedence ran live and mixed per signal: MQTT won `fpp.power.bad`, `fpp.fppd.state` and `fpp.ports.count` on freshness at roughly 1 Hz against a 15 s REST poll, while REST won `fpp.reachable` because MQTT has no such signal.

**Findings from review that mattered.** Two Opus reviews ran, on constraints and on test honesty, both instructed to break the code rather than read it. Both independently found the same top defect.

- **GET-only is not read-only.** Neither HTTP client set `CheckRedirect`, so Go's default followed up to ten hops to any host and any path. A server answering `/api/fppd/status` with a 302 to another host's `/api/command/Start%20Playlist/Christmas?repeat=1` had all four of the collector's requests land there, reproduced against a recorder. Since FPP invokes commands over GET, the "never a non-GET" guarantee this step was built around protected nothing on its own. The fix must live inside the package because `Options.HTTPClient` lets a caller supply the transport; a reviewer proved a custom `RoundTripper` could rewrite the method to POST.
- **A JSON `null` fabricated a measured value**, including 0 mA on a blind port and `powerBad: false` contributing healthy. The realistic trigger is not FPP but the broker, since any publisher there can put that payload on a `port_status` topic.
- **The precedence rule was never proven to be applied.** Deleting `ResolveObservations` from `mapFPPInstance` left the entire Go suite green, and so did deleting it from `handleObservations`. The function was tested exhaustively in isolation and the store was tested for coexistence; nothing tested the layer where resolution has to happen. That is acceptance criterion 4, tested one layer away from where it holds, which is the Step 3 and Step 4 shape exactly.
- **A tier of the precedence rule could be deleted with no test failing.** Making `absenceRank` a constant, removing "unsupported beats collection_failed beats not_collected" entirely, left the package green: all three cases for that tier put the winner on `fpp-rest`, so the static source tie-break alone produced the right answer and the named rule was never the deciding factor.
- **`TestSubscriptionFiltersNeverIncludeCommandTopics` could not fail.** It substring-matched filter strings, so `falcon/player/FPP-Main/#` passed while subscribing to the live command topic. It now resolves each filter against concrete forbidden topics using real MQTT wildcard semantics.
- **`RetainAsPublished: false` was load-bearing and untested.** Flipping it green-lit the suite. Since this fleet publishes every topic retained, `true` would set the retain flag on live forwards too and every signal on every healthy host would read `unknown_age` forever: the ghost rule applied to the whole fleet, on one boolean.
- **A present-but-undecodable field on a remote-mode host reported `unsupported`** with the reason "host is in remote mode; FPP does not report a repeat mode", when FPP did report it. Criterion 3 failing in the honest-looking direction.
- **The MQTT password leaked when supplied inside the broker URL.** `SHOWMESH_FPP_MQTT_PASSWORD` was redacted; the same secret by another door was echoed into three validation errors and logged verbatim at startup. The ordering of the fix is load-bearing: a value that is both malformed and credentialed reaches `url.Parse`, whose `*url.Error` embeds the URL in its own message, so the userinfo check must run before the parse. Mutating the fix printed the full password back out, which is how that was confirmed.
- **The UI rendered the ghost's version as a confident bare string**, ignoring its `unknown_age` state, in three places that reached for `.value` without `.state`.
- **`trapPrefix` could never fail a test.** Its panic fires inside an `http.Handler`, which `net/http` recovers, so a regression into `/api/settings` would have surfaced only as `collection_failed`, looking exactly like an unreachable FPP.

**What the reviews tried hardest to break and could not:** the retained-versus-live rule. Verified against a real broker that a retained replay yields `unknown_age`, that a retained publish arriving while subscribed correctly yields `current`, and that polling again does not move `observedAt`. The fourth attempt at this project's three-times-caught defect did not land. The precedence resolver is also order-independent across 20,000 random groups in six permutations each, the schema v4 migration genuinely builds a v3 database and proves preservation, and the OpenAPI change is additive.

**Duplication found the bug in the code that replaced it.** The MQTT collector was built in parallel with the REST collector and, not yet having its decoders, wrote its own. Unifying them was ordered as a defect fix, and the throwaway copy turned out to be the correct one: `fpp.PortSignals` leaked a duplicate-key port element's signals before detecting the collision, violating its own doc comment, while the copy had independently got it right. A quieter divergence surfaced too, `fpp.position.*` typed `float64` on one path and `int64` on the other.

**The SSE finding, measured twice and deliberately not closed.** A first live run showed 860 KB in 20 seconds, about 43 KB/s per connected browser, on an idle system with the display de-energized. The obvious hypothesis, that timestamp refreshes were firing the hub, was **refuted** by measurement: an MQTT poll with no new message produces byte-identical JSON. Three real causes were found instead. One is an ADR-020 violation dating to Step 3: the stale-evidence reason was built with `fmt.Sprintf("value is %s old, ...")`, a precomputed age inside a payload, which re-renders differently every tick and defeats the diff outright. That is fixed, and the diff key is now state-aware. A second live run then confirmed the suppression works perfectly, **zero no-op frames in 17 frame-pairs**, and that stream volume did not improve, at roughly 57 KB/s for three hosts.

The reason is the part worth carrying forward. Every frame was triggered by a genuine value change, always from the same few signals: `fpp.uptime.seconds` increments every second by definition, and the K16 voltage and temperature sensors jitter continuously. So four to nine signals genuinely change and the coordinator re-sends all 349 observations, about 90 KB for FPP-remote-04, to report it. **The problem is not churn, it is that the change stream has no delta granularity.** Fixing it means per-observation delta frames, which changes the SSE contract and belongs to ADR-020 rather than to a patch at the end of a step. Measured consequence, from Step 4's back-pressure numbers: a phone with roughly 20 KB/s of usable wifi falls the 64-frame buffer behind in about five minutes, is reset, re-fetches a snapshot, and repeats.

**Questions raised with the owner:** how to ingest FPP MQTT given the fleet is on a foreign broker (answer: subscribe to it read-only, since repointing FPP is a write to a live display); how to structure the live fleet run (answer: a one-off recorded manual run, so no committed test can ever be pointed at the display by accident); and how the collector should get broker credentials (answer: reuse the existing account, with the shared-credential exposure noted).

**Deferred, with reasons:**

- **Per-observation delta frames on the change stream.** Measured above. Needs an ADR-020 decision, not a patch.
- **Observation rows are never pruned.** A 48-element `port_status` followed by `[]` leaves 288 per-port rows behind forever, aging to `stale` and rendering as ghost ports of a cape that is no longer installed. Same for a renamed port, a removed sensor, or an instance dropped from configuration. RES-013 owns retention and the fix has real semantic content, since absence from one poll is not deletion.
- **Write amplification.** Each MQTT poll re-upserts every signal, roughly 140 SQLite writes per second across the fleet at idle, almost all unchanged. Same owner.
- **`SHOWMESH_MQTT_BROKER` is redacted in logs but not rejected for carrying userinfo**, unlike the FPP MQTT one, because it is the ADR-008 control-plane broker and an existing deployment may legitimately be configured that way.
- Alerting, metric history, the preview wall, controlled devices, and every write operation.

**Verification gates:** `gofmt` clean; `go vet ./...` clean; `make lint` at 0 issues; `go test -race -count=1 ./...` passing across all 19 packages; `CGO_ENABLED=0 go build ./...` clean; UI typecheck, lint, and 207 tests passing; a production UI build. Go test functions now total 552. The test-honesty review ran the Go suite at `-count=20`, then `-count=30` at two CPU counts under deliberate load, and the UI suite five times, finding no nondeterminism; no new test uses a real sleep, timer, or wall clock for an assertion. `make test-integration` passing against real Mosquitto; `make test-integration-fppmqtt` passing against a throwaway broker.

Verified against the real fleet, read-only: all three hosts plus the broker, with every acceptance criterion demonstrated on live data rather than a fixture. **Not verified:** anything about a running show, anything about the reference switch, and anything requiring an energized display. No non-GET request and no MQTT publish was issued to any live host or the operator's broker at any point in this session.

**What this step does not claim.** Every `ma` on the fleet reads 0 because the display is de-energized. That confirms shape and type and proves nothing about whether current telemetry works. RES-011 is not raised. Everything here is verified against FPP 9.4, `9.x-master-822-g56515e4d`, and these OS builds only; the intended fleet-wide move to a 9.x release or the FPP 10 beta is a material environment change that will make these conclusions stale.

---

## 2026-08-11 (Step 4)

**Goal:** the first operator-facing surface, and the two decisions ADR-021 and OPERATOR-UI §4 deferred to the step that builds it.

**Completed:**

- [ADR-022](../decisions/ADR-022-operator-ui-serves-the-api-same-origin.md), settling UI-to-API topology and browser token handling before any UI code was written.
- `ui/`: a React and Vite TypeScript SPA. API client with a hand-written `text/event-stream` parser, a connection state machine with bounded backoff and jitter, version negotiation, and a framework-free model exposed to React through `useSyncExternalStore`. Dashboard, node list and detail, capability grouping, FPP list and detail, and event history. Plain CSS with custom properties, no framework, no component library, no runtime fetch of anything.
- `ui/Dockerfile` and `ui/nginx.conf`: a static-asset container that also forwards `/api/*`, and the `ui` service in the Compose bundle.
- API types generated from `api/openapi.yaml`, committed, with a CI check that fails on any diff.
- 111 unit tests, including a real `node:http` server driving the store rather than a mocked `fetch`.

**Decisions made:**

- **The UI container serves the API same-origin** (ADR-022 decision 1). Not chosen on architectural purity, which favored the direct alternative, but on the two operator-facing costs it removes: a runtime base-URL document written at container start, and a CORS allow-list whose misconfiguration is indistinguishable from an outage.
- **The proxy forwards credentials and never holds or mints them** (decision 2). This is the load-bearing rule and it outlives the shared-secret posture that motivated it. Its test is that removing the proxy and pointing a client straight at the coordinator changes nothing except the origin.
- **The browser holds the ADR-021 secret in `sessionStorage`** (decision 4), prompted by a `401`, with no login, identity, expiry, or logout. ADR-021 feared that answering early would force a session model the superseding ADR must unwind; the answer chosen is small enough to delete.
- **The dashboard renders only subsystems the coordinator models.** No empty audio, SMPTE, projector, or weather panels. An empty panel asserts that a subsystem exists and is not reporting, which is a false statement about the system. Deliberately not the same rule as evidence absence within a modeled subsystem.
- **The UI does not recompute the coordinator's health verdict**, even when disconnected and the evidence is provably old. The verdict has provenance; the UI inventing its own is what ADR-011 forbids. What was corrected was the age claim attached to it.

**Questions raised with the owner:** the ADR-021 topology and token questions above, the framework choice ADR-015 left open (React and Vite), and whether a disconnected client should degrade the coordinator's evidence badge itself (answer: no, keep the verdict, fix the age).

**What only a running browser could catch, and why it matters more than the fixes:**

- **The client could not make a single request in Chrome.** `fetch` was invoked as `this.fetchImpl(...)`, so its receiver was the client instance rather than `Window`, which a browser rejects with `Illegal invocation` and Node does not check. This survived 99 passing unit tests, three independent reviews, and a clean build of the shipped image. The generalizable lesson is not "run the app": it is that **a test environment differing from the deployment environment in one detail will report success on exactly that detail**, and the closer the harness gets to real (a real HTTP server, real SSE bytes) the more convincing that false success looks.
- **Evidence ages froze while the freshness notice advanced.** Stopping the coordinator for 100 seconds left the evidence panel reading `current` and "observed just now" while the banner correctly reported the disconnection. Ages were anchored to the last response's `serverTime` as a fixed "now", so they stopped moving exactly when an operator most needs them. Fourth appearance of this project's recurring defect: a time presented more favorably than the evidence supports.

**Findings from review that mattered:**

Three reviews ran: constraints and ADRs, test honesty, and a fix-verification pass. The test-honesty review applied 87 mutations to production code and attacked all 51 tests then present.

- **`env_file` put `SHOWMESH_API_TOKEN` and the broker password into the UI container.** Only two variables were named under `environment:`, and `env_file` loads the whole file. Three comments in the bundle asserted the container never holds the token. Confirmed by resolving the config with a real `.env` present, and confirmed fixed the same way.
- **The client loaded the hundred *oldest* retained events and labelled them "Recent events".** `since` defaults to 0 and is an exclusive lower bound in ascending order, so the initial fetch returned the beginning of history, which was then reversed and rendered as newest-first. `latestEventSeq` sits in the snapshot for exactly this and was unused. Confirmed by driving the real store against a server implementing the coordinator's own `/events` semantics with 250 events.
- **A half-open connection rendered as "Live" indefinitely.** No idle deadline on the stream read and no request timeout, while the coordinator emits `: keepalive` precisely so a quiet connection can be told from a dead one.
- **Collector state and reason were dropped entirely**, rendering a failed collector as the number `1`.
- **The dashboard never surfaced FPP `unknown` health**, so a system that knows nothing reported "nothing needs attention".
- **An empty `supportedVersions` rendered as a positive claim** about what the coordinator supports, made from no information.
- **Five tests passed with the behavior they named removed.** The worst was the only test in the suite that sent an `event.recorded` frame: its dedupe assertion was "the duplicate does not appear", which is also what happens when the feature is absent, so live event delivery had no test that failed when broken. Two others were guaranteed by their own fixtures, and one carried a comment describing an assertion that did not exist.
- The Step 3 defect shape did **not** recur: deleting the snapshot fetch outright fails five tests.

**A flaky test, fixed rather than tolerated:** the full suite failed roughly one run in ten under load, from a real 15ms keepalive write interval racing a real 40ms idle deadline. Fixed with an injectable clock seam so the production path is exercised against time the test controls, not by widening the timeout, which would have discarded the claim. Verified 30 consecutive clean runs under deliberate CPU load.

**Deferred:**

- **Overall visual styling.** The operator's assessment is that it works and looks unfinished, and that a styling pass belongs at the end rather than now. Deliberately deferred, not overlooked.
- No browser-driving end-to-end suite. The five acceptance criteria were verified by hand against the running stack; automating them needs a browser in CI and its own decision.
- Desired state, assignments, and reconciliation status, which the API deliberately does not ship.
- Alerting, the preview wall, the house topology map, controlled devices, and every write operation.

**Two things found after the step first landed, both worth more than the fixes:**

- **The verification gap I recorded was the wrong one.** The phone layout went in as "unverified" because the tooling could not force a phone-width viewport. The operator checked it on a real phone: the phone view was fine, and the **desktop sidebar** was the broken surface, with `flex: 1` inherited from the bottom tab bar stretching each nav link to a fifth of the viewport height. The surface I flagged was correct and the surface I never doubted was not. An emulated viewport and a real device are different claims, and so are "I could not check this" and "this is probably wrong".
- **A CI test that was a coin flip, and passed or failed regardless of correctness.** `TestSlowSSEConsumerGetsResetAndDisconnected` failed twice on Linux while passing 6/6 on macOS in 1.6 s against a 20 s bound. Measurement refuted the obvious hypothesis: the handler wrote the entire 200-frame burst with **no write exceeding 20 ms**, and the overflow branch never executed, so no reset was correctly ever sent. Two findings came out of instrumenting it. The real variable is **frames per render pass**, because an MQTT burst arrives one hello at a time and each poke of the hub renders separately: Linux produced 165 passes of one frame where macOS produced 109 of one, 49 of two, and 29 of three, so a buffer of 2 survives one distribution and not the other. And **"the client is not reading" is nearly useless as back-pressure**: a probe measured 4.0 MB into the kernel on Linux and 1.5 MB on macOS before a single write blocked, against a burst whose entire wire volume is roughly 120 KB. The test now creates the overflow structurally, by starting a second coordinator over a database already holding 200 nodes so its first render pass hands the hub all 200 at once, with no broker attached so another test's retained messages cannot consume that pass. Verified on Linux at 0 failures in 25 runs, from 3 in 20 before, and both mutations of the production behaviour it names make it fail. The coordinator also now logs when it queues a reset for a full buffer, because it was disconnecting clients and keeping no record that it had.

**Verification gates:** `gofmt` clean; `go vet ./...` clean; `make lint` at 0 issues; `go test -race -count=1 ./...` passing; `CGO_ENABLED=0 go build ./...` clean; `make test-integration` passing against real Mosquitto with real subprocesses; UI typecheck, lint, and 111 tests passing, the suite run 30 times under load without a failure; generated API types byte-identical to a fresh generation from `api/openapi.yaml`; both images building. All five acceptance criteria verified against running containers with two real agent subprocesses advertising over the bundled broker, and a fake version-2 coordinator for the version-negotiation criterion. Not verified: anything on real show hardware, and the phone layout.

---

## 2026-08-11 (Step 3)

**Goal:** the first slice of real observability, and the versioned public control API and change stream that ADR-014 requires, designed and shipped before any UI exists.

**Completed:**

- `pkg/observation`: the OBSERVABILITY §4.1 model. Provenance, freshness, a six-state evidence vocabulary (`current`, `stale`, `unknown_age`, `not_collected`, `collection_failed`, `unsupported`), and the five health states. Constructors make it impossible to build a value with a fabricated observation time, and `SignalID` syntax is validated rather than merely documented.
- `internal/coordinator/collector` plus `collector/fpp`: a source-neutral collector shape and the FPP REST collector, with bounded timeouts, non-overlapping polls, and backoff with jitter.
- `internal/coordinator/store`: observation persistence (latest-only) and an append-only event history with retention bounds by age and row count.
- `internal/coordinator/api` and `api/openapi.yaml`: the `/api/v1` surface, the SSE change stream, version negotiation, RFC 9457 problem documents, optional bearer authentication, and CORS.
- `cmd/showmeshctl`: the independent non-UI client.
- The integration harness now execs the **real coordinator binary** and observes it through the new API, instead of wiring the components in-process.
- [ADR-020](../decisions/ADR-020-control-api-shape-and-change-stream.md) and [ADR-021](../decisions/ADR-021-read-api-authentication-posture.md).

**Decisions made:**

- **Server-Sent Events, not WebSocket** ([ADR-020](../decisions/ADR-020-control-api-shape-and-change-stream.md)). The deciding argument was the acceptance criterion itself: with SSE, exercising the contract without a browser is `curl -N`, and a contract that needs a client library before anyone can inspect it drifts towards private. The stream is deliberately not resumable, which is enforced structurally rather than documented: no `id:` field so browsers never ask for resume, per-connection sequence numbers so no cursor can become global, and a `stream.start` frame demanding a snapshot on every connection.
- **Optional shared-secret authentication, off by default** ([ADR-021](../decisions/ADR-021-read-api-authentication-posture.md)), with a startup warning when unset. The ADR records bluntly that one shared secret is not an identity and does not satisfy ARCHITECTURE §10.4, and it bars the first write endpoint until superseded. It is expected to be superseded, and it says so.
- **The API warranted its own ADR** rather than sitting behind ADR-014. ADR-014 settled what the API is; the transport, the non-resumability rule, the evidence-absence vocabulary, and additive-only compatibility are durable constraints a future contributor could otherwise improve away one at a time.
- **Wire types are a separate layer from domain types**, mapped between. Duplication on purpose: if domain structs carried the JSON tags, an ordinary refactor would silently change a public contract and the version in the path would be a lie.
- **The published contract is permissive; strictness is a test-only overlay.** `additionalProperties: false` on 26 schemas made the conformance test catch drift in both directions, and simultaneously told every generated client to reject the additive changes v1 explicitly permits. Both properties were wanted, so they were separated.
- **No desired state, assignments, or reconciliation status in v1.** OPERATOR-UI §5 lists them as the API's eventual minimum; the coordinator does not model them, and a `reconciliationStatus` that no code computes would be rendered by a UI and read by an operator as a verdict.
- **`FPPInstance.health` stays `unknown` for a persistently unreachable configured instance** rather than `failed`. OBSERVABILITY §4.2 arguably supports `failed`, but Step 3 has no concept of an instance being required and no lifecycle context to judge against; inventing one to reach a more confident verdict is what ADR-011 forbids.
- **Version negotiation runs before authentication**, so version skew stays diagnosable without credentials. Recorded as a choice rather than left to be rediscovered.

**Questions raised with the owner:** whether FPP MQTT ingestion belonged in this step (answer: defer it, record the narrowing, since REST supplies the whole signal set and FPP MQTT depends on operator-side configuration that cannot be verified here), and what authentication posture ships (answer: the optional shared secret above).

**What the FPP bench caught before a line of collector code was written:** `GET /api/settings/MultiSyncEnabled` returns the setting's *schema*, not its value. A Go struct with a `value` field decodes that body without error and yields `false`. Since `MultiSyncEnabled` defaults to off, the wrong implementation produces the right answer today and its test passes; it breaks the moment MultiSync is enabled. Worse, once the setting has been written even once the endpoint *does* gain a `value` key, as a string rather than a boolean, so two FPP hosts behave differently and are indistinguishable without knowing this. The usable signal is the running daemon's own `multisync` flag in `/api/fppd/status`, which flips only after an `fppd` restart because the setting carries `restart: 2`. Verified end to end against FPP 9.5.3. Also recorded: several numeric-looking status fields arrive as JSON strings, so a struct declaring an integer fails to unmarshal the whole document and the collector reports the FPP unreachable, a decoding bug wearing a network fault's clothes. All of this is in RES-002's evidence section.

**Findings from review that mattered:**

Three independent reviews ran: constraints and ADRs, test honesty, and the public contract. Two of the three verified by running the real binary or by breaking production code and confirming a test failed.

- **Three tests passed with the behavior they named removed.** The worst asserted that the change-stream client never applies a delta before fetching an authoritative snapshot, and stayed green with the snapshot fetch deleted entirely, because it accepted "connected" or "snapshot" as the first line and the client prints "connected" first. It sat on this step's own acceptance criterion. A second claimed a slow consumer receives `stream.reset`, and only checked that the stream ended. A third claimed shutdown closes streams, and passed with the shutdown path deleted, because the test closed the client connections itself. This is the same shape as Step 2's fake-connection shutdown test, twice over, and it is why the reviewer was told to break the code rather than read it.
- **A wedged subscriber could never be told it was wedged.** Clearing the connection write deadline was the correct fix for a 10 second `WriteTimeout` killing every stream, but clearing it with nothing in its place meant a client that stops reading blocks its own handler forever, so the buffer-overflow reset the hub queued was never written, the connection never closed, and the goroutine and its buffers were pinned until process exit. Precisely the silent drop ADR-020 exists to forbid, reachable by anyone on the show VLAN with authentication off by default.
- **The published contract told clients to ignore unknown fields while declaring every schema closed.** A client generating types from the document breaks the first time a field is added additively, which ADR-014 makes a permanent condition rather than an edge case.
- **`/api/v1/observations` could never return node evidence while documenting that it did**, because node evidence is synthesized on read and only collector output is persisted. Fixed by unioning at read time rather than by persisting node evidence, which would have created two sources of truth for the same facts.
- **The snapshot read its event cursor after its resources**, so the "no gap" guarantee in its own doc comment was false: a transition landing between the two reads is invisible to a client that follows the documented snapshot-then-events sequence.
- **Event retention did not do what its comment claimed.** Pruning fired on every hundredth append within one process lifetime, with an in-memory counter, so a coordinator that records a few transitions per week and restarts between shows never pruned at all and the documented 30 day age bound was fiction. Two comments asserted as fact things that were false.
- **Staleness-driven offline transitions never reached the event history**, because transitions were only recorded when a message arrived. A node whose heartbeats simply stop is the transition an operator cares most about, and the durable history said it had been online since its last message.
- **`collectedAt` was populated on evidence that was never collected**, which is the `observedAt` fabrication this project already guards against, one field over.
- **Two doc comments asserted behavior nothing implemented**, and one asserted a guarantee about the OpenAPI document that did not exist. This project treats that as a defect rather than untidiness, and it has now been found in three consecutive steps.
- The API's own `POST` on a real route answered "no route matches", which is false, and is exactly where a client discovers that a deliberately read-only API is read-only.

**Deferred:**

- FPP MQTT ingestion, with the reason recorded in BUILD-PLAN.
- Metric history, retention tiers, and downsampling. Observations are latest-only; RES-013 owns the design and guessing here would pre-empt it.
- Full de-typing of the integration suite, which still decodes some assertions through the server's own wire types. Raw-key assertions were added for the load-bearing cases.
- Alerting, the preview wall, controlled devices, and every write operation.
- Running the multisync probe against the real FPP player, and everything else RES-002 tier 2 needs.

**A harness gap found while proving a fix, worth its own line:** `SHOWMESH_TEST_STALENESS_WINDOW` was never forwarded into the coordinator subprocess's environment, so every coordinator in the integration package had silently been running with the production 30 second window regardless of the override. Nothing failed because of it, which is the point: a test knob that is quietly ignored makes a suite slower and its timing assumptions wrong without ever reporting anything. Found only because a new test needed the window to actually be short.

**Verification gates:** `gofmt` clean; `go vet ./...` clean; `make lint` at 0 issues; `go test -race -count=1 ./...` passing; `CGO_ENABLED=0 go build ./...` clean with zero CGo packages in the coordinator's dependency graph after adding a pure-Go JSON Schema validator; cross compiles clean for `linux/amd64`, `linux/arm64`, `darwin/arm64`, and `windows/amd64`; the coordinator image builds at 13.4 MB.

`make test-integration` runs 28 tests against Mosquitto 2.0.22 with the agent **and now the coordinator** as real subprocesses, all passing on a cleared test cache. `make test-integration-fpp` passes against the containerized FPP 9.5.3, including a coordinator subprocess pointed at the live daemon that proves the whole chain from collector through store and mapping to JSON, and it skips cleanly rather than failing when no FPP is reachable.

Verified by hand against the shipped image with the broker unreachable and an FPP endpoint refusing connections: `/healthz` 200, `/readyz` 503, `ShowMesh-API-Version: 1` on every response, an unreachable FPP rendering `state: "collection_failed"` with a reason and `observedAt: null` rather than a 500 or a fabricated value, an unsupported version request answering `application/problem+json` naming the supported versions, an unknown path answering the same way, and SIGTERM exiting 0.

Not verified: anything on real show hardware, anything about the reference switch, and anything about live-show behavior. Nothing in this step raises any research record above its current level.

---

## 2026-08-10 (Step 2 round 2)

**Goal:** finish the control plane skeleton, and prove Step 2's acceptance criteria against a real broker rather than by hand.

**Completed:**

- `internal/agent`: retained hello and online state republished on every connect and reconnect, a Last Will registered at CONNECT, a health heartbeat, and a clean shutdown that publishes its own offline state before disconnecting.
- `internal/coordinator/store`: SQLite via `modernc.org/sqlite`, transactional migrations, a newer-than-known schema refused, and an observation model carrying provenance and freshness as columns.
- `internal/coordinator/inventory`: subscription, message handling, and liveness derivation.
- `test/integration` plus `scripts/test-integration.sh`, a `make test-integration` target, and a CI job, all sharing one script and exercising the shipped `deploy/mosquitto/mosquitto.conf` rather than a stand-in.

**Decisions made:**

- **A retained MQTT delivery is not evidence of the present.** The broker replays every retained message when a subscriber connects, so stamping receipt time as the observation time would make an hours-old heartbeat from a node that has since lost power read as perfectly fresh. A retained delivery records values with no observation time and can never produce a healthy verdict. A node reading `unknown` immediately after a coordinator restart is the correct behavior, resolving within one heartbeat, and the code says so to stop a future contributor "fixing" it.
- **Liveness requires two signals.** If a node loses power and the broker is then killed uncleanly before writing its persistence file, the retained will still reads online. So `online` requires the will saying online *and* a live heartbeat inside the staleness window.
- **Contradictory evidence resolves to `unknown`, and the rule turns on ordering.** The first version keyed on freshness, which was wrong: a clean shutdown publishes an offline will *after* its last heartbeat, which is a sequence of events rather than a conflict, and the freshness rule made the coordinator ignore a node's own announcement of its death in favour of stale history for the whole staleness window. A contradiction exists only when a live heartbeat is observed no older than the offline will.
- **The agent advertises an empty capability set,** because it can do nothing yet and advertising otherwise would be a false claim. The environment override exists for tests, not because the agent has capabilities.
- **`offline` means the control-plane connection is gone, not that the node is dead.** Recorded in BUILD-PLAN alongside the corrected acceptance criterion, because a running show survives coordinator and broker loss, and "offline" on an operator surface reads as "dead".

**Findings from review and integration that mattered:**

- **Clean shutdown never published its offline message.** `Run` passed the signal context to the connection manager, so SIGTERM made autopaho send a normal DISCONNECT, which discards the will, before the explicit publish could run. Every planned stop left the broker claiming the node was up. The unit test asserting publish-before-disconnect ordering passed throughout, because it asserted ordering inside one function against a fake connection while the real wiring did the opposite. Its name claimed more than it verified.
- **A forged health message permanently wedged a node.** A node's boot ID is readable on its retained hello topic, so any client with publish rights could send one payload with that boot ID and the maximum sequence, after which every genuine heartbeat from that node was dropped at debug level until the agent process restarted. A sequence with the high bit set also could not be bound by `database/sql` at all, a type mismatch between the wire model and the column that nothing validated.
- **Retained last-will evidence stored the coordinator's receipt time,** so after a restart an operator would have been shown six-hour-old state described as seconds old. Today's verdict was unaffected because the derivation never reads that field, which is exactly why it would have survived to Step 3's read API.
- **`LWTDeliveryPolicy` shipped as `Retain: false` in round 1.** Correct for a will in the abstract, wrong here, where the topic is presence state read on subscribe: non-retained, a dead node and a never-seen node are indistinguishable. Caught only because a builder refused to follow a specification it judged wrong, and now pinned by a test carrying its own rationale.
- **A doc comment asserted a dependency's dispatch mechanics as fact and had them wrong.** The standing rule against claiming unverified behavior applies to dependencies, not only to hardware.
- **One integration test initially passed for the wrong reason.** Reusing the same database across a coordinator restart meant the replayed retained heartbeat carried an identical boot ID and sequence, so the anti-replay logic correctly ignored it and the path under test never ran. Caught by its author.

**Questions raised with the owner:** none. The two open decisions from round 1 were settled in round 1.

**Deferred:**

- The heartbeat interval and staleness window remain unmeasured hypotheses, and they determine how quickly a failed node is noticed during a show. Belongs in RES-009.
- Retention and pruning for observed-state history, which ADR-009 names as a consequence and this step does not address; the store keeps latest evidence only.
- Any client with publish rights can create unbounded node rows by publishing on arbitrary syntactically valid node IDs. Bounded payload sizes landed; authorization did not, and ARCHITECTURE §10.4 still governs.
- Running the probe against the real FPP player. RES-002 stays at L1 with status `planned`.

**Verification gates:** `make check` with lint at 0 issues; `go test -race ./...` passing; `CGO_ENABLED=0 go build ./...` clean with zero cgo packages in the coordinator's dependency graph; builds clean for `linux/amd64`, `linux/arm64`, `darwin/arm64`, and `windows/amd64`; the integration build tag confirmed to exclude the tagged suite from an untagged `go test ./...`; and all six integration tests passing against Mosquitto 2.0.22 with the agent as a real subprocess, including SIGKILL, SIGTERM, coordinator restart, and broker restart. Not verified: anything on real show hardware, and anything about FPP.

---

## 2026-08-10 (Step 2 round 1)

**Goal:** commit the pending documentation package, then build the two independent seams of the control-plane skeleton that need no broker: the shared models, and the `internal/coordinator` split that Step 0 deferred to here.

**Completed:**

- Reviewed and committed the Operator UI and Audio Engine documentation package.
- `pkg/mqttproto`: ADR-008 v1 topic builders and parser, versioned JSON envelope with an opaque payload, hello/health/last-will payloads, and the per-kind retain and QoS policy exported as data.
- `pkg/capability`: identifier syntax validation, sets, and a canonical encoding.
- `internal/coordinator` split into `config`, `broker`, `httpapi`, and `readiness`, with the run loop moved out of `cmd/showmesh-coordinator/main.go`.

**Decisions made:**

- **The envelope is the sole carrier of node identity and send time.** The first implementation duplicated both into the hello and health payloads on a self-describing-payload argument. Rejected: payloads never travel apart from their envelopes, the coordinator records observations with its own receipt time and provenance per ADR-011 rather than storing bare payloads, and a duplicated send time inside a payload invites exactly the freshness misuse the envelope's own doc comment warns against. It also left the last-will payload following a different rule than its two siblings for no stated reason.
- **`readiness` is its own package so the transport layer does not depend on the HTTP layer.** The first implementation had `broker` importing `httpapi` to return its report type. In Step 3 the SQLite store and the FPP collectors also report readiness, and that shape would have every one of them importing the HTTP package to describe its own health.
- **Unknown capability identifiers are accepted, not rejected.** ADR-002 exists so hardware support expands without core changes, and OPERATOR-UI requires an unrecognized capability to render as a generic panel. Syntax is validated; the known vocabulary is informational only.
- **Subagent build workflow recorded in CLAUDE.md.** Specification and review folding stay with the orchestrating session; implementation is delegated per independent seam; review is delegated with the binding ADRs named.

**Findings from review that mattered:**

- **ADR-011 freshness had degraded from a structural guarantee to a convention.** Before the split, the HTTP handler computed the observation age itself, so a readiness response could not omit it. After, the typed observation timestamp was set by the broker and read by nothing, and the age reaching the body was a hand-built map key. Every current source happens to set it; a Step 3 source that did not would emit a health verdict carrying no freshness at all, which is the bare-boolean defect a Step 0 review already caught once in this project, reintroduced one layer up. The HTTP layer now derives the age from the typed field.
- **A null payload decoded as a valid capability advertisement.** Unmarshalling JSON `null` is a no-op that returns no error, so a retained hello with a null payload was accepted as genuine with an empty boot ID, and boot ID is the only signal distinguishing an agent restart from a continuous session. The envelope had explicit required-field validation precisely because strict decoding was rejected as the mechanism, and that principle had been dropped one level down, at the layer where data arrives from an untrusted node.
- **The canonical capability encoding was not canonical.** Two sets differing only in the ordering of a duplicate identifier produced different bytes while the doc promised byte equality for logically identical sets, so a checksum-based change detector would report a capability change from pure reordering, which ADR-003 makes consequential.
- The keepalive derived from the staleness window through a runtime float-to-integer conversion that would wrap silently rather than fail, the same never-panics numeric shape a Step 1 review found. It is now a constant conversion that does not compile on overflow.
- `Disconnect` never joined the broker probe goroutine, so its wait group read as synchronization while being decoration.
- The review confirmed the split's no-behavior-change claim by running the pre-split and post-split binaries side by side rather than by reading the diff, and could not defeat the node-identifier validation across roughly a dozen injection attempts including unicode lookalikes and line terminators.

**Questions raised with the owner:** how far to automate Step 2's acceptance criteria (answer: a Mosquitto service container in CI, so they are re-proven on every push); whether the RES-002 bench run happens alongside Step 2 (answer: no bench time yet, so RES-002 stays at L1 and the probe is untouched); and how to handle a third-party product name left in git history (answer: rewrite history and force-push while the repository is private and has five commits).

**Deferred:**

- All of Step 2 round 2: the SQLite store, inventory, and the agent's hello, Last Will, and heartbeat, plus the CI broker harness they need.
- Running the probe against the real FPP player. RES-002 stays at L1 with status `planned`.

**History rewrite:** the git history was rewritten in this session to remove a third-party product name that survived in the initial commit after being taken out of the working tree, and the result was force-pushed. Every commit hash changed. A pre-rewrite bundle was taken first and verified complete. GitHub may retain the pre-rewrite objects internally for some period; that was accepted rather than deleting and recreating the repository, which would have discarded the CI history including the run that caught the `SO_REUSEADDR` bug.

**Verification gates:** `make check` passing with lint at 0 issues; `go test -race ./...` passing; `CGO_ENABLED=0 go build ./...` clean; builds clean for `linux/amd64`, `linux/arm64`, `darwin/arm64`, and `windows/amd64`; `FuzzDecodeEnvelope` clean; the coordinator serving `/healthz` 200 and `/readyz` 503 against an unreachable broker, with an identical response body before and after the split, and exiting cleanly on SIGTERM. Not verified: anything involving a real broker, a real agent, or real hardware. Step 2's own acceptance criteria are all unmet, because all three require a broker.

---

## 2026-08-10 (Audio Engine specification, no code)

**Goal:** fold the owner's brainstorm specification for the Audio Engine into the docs package. Unlike the UI package, this one changes an authority boundary, so the ADRs matter more than the specification does.

**Completed:**

- `docs/architecture/AUDIO-ENGINE.md`: authority model, playback sessions, timing and drift policy, clock domains, buses and routing, rendering boundary, output adapters, mixing and priority, media management, failure behavior, platform, capabilities, control surface, telemetry, scope, open questions.
- [ADR-017](../decisions/ADR-017-showmesh-owns-audience-audio.md): ShowMesh is authoritative for audience-facing audio, and nodes play complete local media against their own audio clock rather than following a sample stream.
- [ADR-018](../decisions/ADR-018-program-and-ltc-share-a-clock-domain.md): program audio and LTC share one clock domain.
- [ADR-019](../decisions/ADR-019-audio-device-loss-fails-silent.md): audio device loss fails silent with no automatic fallback to FPP audio.
- [RES-007](../research/RES-007-audio-node-architecture.md) rewritten as the work queue for the decisions, with measurable acceptance criteria replacing impressions.
- ARCHITECTURE §3.1 authority table, §4.5, a new §5.5 on clock domains, the §6 audio capability vocabulary, §10.2 platform, and Phase 1 and Phase 2 roadmap bullets. Build plan, indexes, and CLAUDE.md updated.

**Decisions made:**

- **Rendering stays in GStreamer**, so ADR-007 applies unchanged to audio: mixing, fades, ducking, interleave, and LTC generation are pipeline work, never a Go sample path. This is the constraint most likely to be breached accidentally, because writing an LTC generator in Go is easy and wrong. Whether GStreamer can actually hold LTC sample-aligned to program is now RES-007's first bench item, and a negative result requires a superseding ADR rather than a workaround.
- **Fail silent on audio device loss**, superseding RES-007's previous "FPP-hosted stereo playback remains the conservative fallback". The reference installation's FM transmitter is what settles it: an automatic handover to a path ShowMesh does not control produces unknown audio at unknown gain into a transmitter, which is worse than silence. Recorded in ADR-019 as an explicit, narrow exception to ADR-004, with the obligation that macro definitions state it and that a tested manual recovery procedure exists.
- **The §6 audio capability vocabulary was migrated rather than extended.** `audio.playback`, `audio.multichannel`, `audio.dante`, and `timecode.ltc.generate` are withdrawn in favor of `audio.engine` plus `audio.output.*`. Nothing advertises capabilities yet, so this cost nothing today and would have been expensive after the agent ships.
- **Audio stays unsequenced.** Every load-bearing claim is a bench fact, and the interface is unpurchased. ADR-018 is a purchasing constraint — at least three output channels from one clock — and should reach the owner before the interface is bought, not after.

**Conflicts found between the brainstorm specification and the accepted package:**

- **Audio deliberately diverges from the MultiSync sync model.** CLAUDE.md's standing rule to follow FPP remote semantics — slew four frames, jump beyond half a second — is correct for pixels and audible in program audio. The specification is right to reject it; the risk was that a future contributor would read the divergence as an inconsistency and "fix" it. Recorded in ADR-017, ARCHITECTURE §5.5, and CLAUDE.md so the divergence is visibly intentional.
- **Free-running versus ARCHITECTURE §5.1.** §5.1 forbids a node free-running indefinitely from receipt time while reporting itself synchronized. An audio node aligning at start and correcting discretely satisfies that only because it stays continuously measured against the show timeline. AUDIO-ENGINE §4.2 states the reconciliation explicitly rather than leaving the two documents in apparent contradiction.
- **The Windows Dante bridge contradicts the specification's own deferrals.** Bridging program audio from a Linux audio node to a Windows Dante node is real-time audio transport between ShowMesh nodes, which the specification excludes from show audio and defers, and it introduces the second independent clock domain ADR-018 exists to prevent. Recorded as a deferred configuration with a stated cost, not a free deployment option.
- **A third-party audio integration on the FPP host may conflict with ADR-017.** One line in the reference installation questionnaire suggests an existing listener application runs on the FPP host and takes over its audio output, which would be the position ADR-017 vacates. That single hedged operator note was not enough to justify publishing a conflict as though it were established, so on the owner's instruction every mention was moved out of the tracked docs to local untracked notes under `docs/private/` while the integration is discussed with the product's developer. Nothing about it belongs in ARCHITECTURE, AUDIO-ENGINE, the ADRs, or the research tracker until there is something source-verified to record. The architectural concepts it had motivated survived on their own merit: synchronized remote outputs remain an adapter class, and adapters still declare unsupported functionality rather than pretending.
- **Failover, again.** Same shape as the UI package: ARCHITECTURE §12 defers automatic failover during a live set and puts reassignable workloads in Phase 3, so audio-node failover is operator-initiated with ShowMesh performing eligibility verification, not automatic.
- **Output adapters are not control providers.** Structurally similar to ADR-016's providers and fundamentally different — adapters are in the media path, providers are management-plane only. Stated explicitly in AUDIO-ENGINE §8.1 to prevent a later refactor unifying them. In the reference installation the FM transmitter and the BSS BLU-DAN are controlled devices while the interface output and Dante transmit path are adapters, and the same signal chain crosses both models on purpose.

**Questions raised with the owner:** rendering engine (answer: GStreamer per ADR-007), device-loss fallback (answer: fail silent, ADR it), capability vocabulary (answer: adopt the new scheme and migrate §6), and sequencing (answer: unsequenced, blocked on RES-007).

**Deferred:** everything in AUDIO-ENGINE §16's deferred list, plus the audio interface selection itself, the LTC frame rate configuration question that depends on RES-001, and whether announcements ever interrupt show audio or only duck it.

**Verification gates:** none run. No code, no build, no bench. The entire audio package is unimplemented design intent at L0.

---

## 2026-08-10 (Operator UI specification, no code)

**Goal:** fold the owner's brainstorm specification for the Operator UI into the docs package before Step 2 starts, so that the API work in Step 3 is designed knowing a UI will consume it.

**Completed:**

- `docs/architecture/OPERATOR-UI.md`: the client contract — isolation, API contract, real-time transport, connection and staleness behavior, information architecture, capability-driven composition, desired-versus-observed rendering, control safety, overrides, responsiveness, authorization posture, HA compatibility, first-release scope, and open questions.
- [ADR-014](../decisions/ADR-014-operator-ui-is-an-api-client.md): the UI is an optional client of a versioned public control API, deployed as its own container.
- [ADR-015](../decisions/ADR-015-typescript-spa-frontend.md): the UI is a TypeScript SPA. This closes the frontend clause ADR-006 deliberately deferred; a pointer was added to ADR-006 rather than editing its decision.
- [ADR-016](../decisions/ADR-016-controlled-devices-and-control-providers.md): controlled devices are a resource class distinct from nodes, driven by self-describing control providers.
- [RES-014](../research/RES-014-control-provider-model.md): the provider metadata contract and the metadata-generated-surface hypothesis, at L0.
- ARCHITECTURE §4.9, §4.10, §9, §10.1, and the Phase 0 and Phase 2 roadmap bullets; an ownership note at the head of OBSERVABILITY §6; BUILD-PLAN Step 3 amendments, a new Step 4, revised "not yet sequenced" entries, and a new standing constraint; index and CLAUDE.md updates.

**Decisions made:**

- **Document ownership was split explicitly.** OBSERVABILITY §6 already specifies the dashboard's required content in detail, and the brainstorm specification restated most of it in different words. OBSERVABILITY now owns what the operator surface must show; OPERATOR-UI owns how the client is built. Keeping both lists would have guaranteed drift, and the drift would have surfaced as a UI that satisfies one document and violates the other.
- **The API contract work moved into Step 3, ahead of any UI.** If the API is designed alongside the UI that consumes it, behavior settles in whichever layer is easier to change and the API stops being independently usable without anyone deciding that. Step 3 now has an acceptance criterion that a non-UI client exercises it.
- **Separate UI container over embedding**, chosen by the owner. Recorded honestly in ADR-014: the isolation argument usually given for this is weak in ShowMesh, because a coordinator restart already does not interrupt a show. The real justification is contract discipline and independent release cadence, and the costs — second image, CORS, TLS termination, permanent version skew — are recorded rather than glossed.

**Conflicts found between the brainstorm specification and the accepted package:**

- **Failover.** The specification asks the dashboard to show failover state and let an operator initiate failover. ARCHITECTURE §12 explicitly defers automatic failover during a live set until evidence supports it. A UI cannot display a capability the system does not have, so first-release scope was narrowed to showing current capability assignment and operator-initiated reassignment. This is the kind of item that silently becomes a commitment if it is copied into a spec unexamined.
- **Audio authority.** "Primary audio authority lost" as a critical alert presumes a clock-ownership model that RES-007 has not researched. Recorded as an open question rather than specified.
- **HA compatibility.** The specification asks the design not to block high availability. ADR-009 makes the coordinator single-writer today, so the obligation was reduced to four concrete requirements with an explicit prohibition on speculative multi-coordinator machinery in the frontend.
- **Terminology.** The specification's "roles and workloads" is the existing capability-assignment model, and its reconciliation vocabulary was aligned to ARCHITECTURE §7.2 (`converged`, `progressing`, `degraded`, `unknown`, `conflicted`) rather than introducing a parallel set.
- **MQTT to the browser.** Not proposed by the specification, but it is the obvious shortcut given ADR-008 and it violates the specification's own rule against the UI touching message queues. Recorded as an explicitly rejected alternative with reasons, so it is not rediscovered as a clever idea later.
- **Controlled devices versus ADR-004.** Found in review, not in the specification. ADR-004 requires every critical macro to define what runs locally without the coordinator, and ADR-016 creates a resource class with no local fallback whose network-reachable providers may live in the coordinator — meaning a `Blackout` or `Enter Pre-Show Mode` step touching a projector could be unavailable exactly when the coordinator is. ADR-016 now requires such steps to be labelled coordinator-required, and pushes genuinely show-critical device control onto a node instead. This does not weaken the guarantee that a *running* show survives coordinator loss; it constrains lifecycle transitions, and RES-009 must cover it.
- **A sixth health state.** The first draft of ADR-016 introduced `unconfirmable` for devices with no status query. OBSERVABILITY §4.2 already defines five states and already assigns insufficient evidence to `unknown`. Replaced with `unknown` plus a provenance reason rather than growing the vocabulary.

**Questions raised with the owner:** frontend stack (answer: TypeScript SPA), UI deployment shape (answer: separate container), and whether the controlled-device model belongs inside the UI specification (answer: split it out into ARCHITECTURE plus its own research record).

**Deferred:**

- Real-time transport choice (WebSocket versus SSE), to be made with the Step 3 API work.
- The initial authentication mechanism, which must be decided before the API gains write endpoints.
- Origin, proxying, and TLS termination between the UI container and the coordinator.
- The preview wall, blocked on RES-010, and controlled-device control, blocked on RES-014 and RES-012 bench work.

**Verification gates:** none run. This session produced no code and touched no build; nothing here has been verified, and the entire UI package is unimplemented design intent.

---

## 2026-08-10 (Step 1, later in the same session)

**Goal:** implement `pkg/multisync`, the FPP MultiSync wire protocol and timeline model, plus the bench probe that can close RES-002's five open items.

**Completed:**

- `pkg/multisync/packet.go`: codec for the `FPPD` header and packet types 0x01 sync, 0x03 blank, 0x04 ping and discover, and 0x06 FPP command. The byte-offset table in `doc.go` was derived from FPP's `ControlProtocol.txt`, `MultiSync.h`, and `MultiSync.cpp`, then independently re-verified field by field during review.
- `pkg/multisync/timeline.go`: the state machine on an injectable clock. Free-runs through sync silence (RES-002 is emphatic that silence is not a teardown trigger), classifies corrections, holds a blank delay after STOP, tolerates START without OPEN and a bare SYNC, and ages into `unsynchronized` while continuing to run.
- `pkg/multisync/listener.go`: multicast, broadcast, and unicast on one socket, per-interface join reporting, and an opt-in discover-ping responder.
- `cmd/showmesh-multisync-probe`: the bench instrument, with JSONL evidence output and a summary organized explicitly by RES-002's five open items.
- `docs/bench/RES-002-capture-procedure.md`: the operator procedure for running the captures.

**Findings from review that mattered:**

- The discover-ping responder replied to the datagram's source port. FPP's send sockets are unbound, so its pings arrive from an ephemeral port and the reply went nowhere. As written the responder could never have worked, and the failure would have looked like a protocol or device-type problem rather than a delivery one.
- Competing-master detection was keyed on `ip:port`. A single FPP master fans out over several unbound sockets and therefore appears under several ports, so the timeline treated one master as many, dropped 40 consecutive syncs, and wedged with a frozen position until the filename changed.
- A non-finite or absurd `SecondsElapsed` float poisoned the position estimate through an out-of-range float to int conversion, producing a large negative position on `linux/amd64` and a large positive one on `darwin/arm64`, with no state change to signal it. Fuzzing could not find this because it never panics.
- `SO_REUSEPORT` on port 32320 load balances unicast datagrams by 4-tuple hash rather than fanning them out, so a co-located listener can intercept fppd's own unicast sync stream. Verified on Linux. See the decision note below.

**Decisions made:**

- Port sharing (`SO_REUSEPORT`) defaults to OFF in the listener configuration. With it off, a bind conflict against a running fppd fails loudly, which is the correct outcome. With it on, ShowMesh could silently take FPP's unicast sync traffic, which would put ShowMesh inside the timing path in violation of ADR-001 and standing constraint 6. Recorded here and in the capture procedure. Whether this rises to an ADR is an open question for the owner.
- Callers supply the timeline's source identity and must key it on IP, never `ip:port`. The contract is documented on `Observe` rather than enforced by stripping ports inside the package, because the package should not silently reinterpret what a caller passes.
- Several thresholds are ShowMesh hypotheses rather than FPP-derived values, and are labelled as such in the code: the silence interval before `unsynchronized`, the no-correction band, the slew ramp fraction, and the accepted bound on `SecondsElapsed`. The FPP-derived values (slew at four frames or fewer, jump beyond 0.5 seconds, roughly five frames of blank delay) are marked separately with their RES-002 provenance. That separation is deliberate and should be preserved.

**Questions raised with the owner:** whether the port 32320 sharing constraint becomes ADR-013 (answer: yes, and it is now Accepted), and whether the `showmeshsystems` organization exists yet so this can be pushed (answer: yes; the repository was pushed private and CI ran, which is what produced the finding below).

**Deferred:**

- Running the probe against the real FPP player. Nothing here has been seen by real FPP traffic, so RES-002 stays at L1 with status `planned`.
- Whether ShowMesh actually appears in the FPP MultiSync UI once discover responses are enabled. Unverified, and part of RES-002 open item 5.
- Splitting `internal/coordinator`, still carried into Step 2.

**What the first CI run caught, and why it matters:** the repository was pushed to `github.com/ShowMeshSystems/showmesh` (private) and CI ran on a real runner for the first time. It failed, on a test that passes on macOS. `SO_REUSEADDR` was being set unconditionally on the assumption that for UDP it cannot by itself let two processes share a port. That is true on BSD and false on Linux. Verified in a Linux container: two sockets setting only `SO_REUSEADDR` both bind the same UDP port, and 20 unicast datagrams went 20 to one socket and 0 to the other, reproducing the exact interception hazard ADR-013 exists to prevent.

The fix gates both options behind `AllowPortSharing` rather than only `SO_REUSEPORT`, so the default path sets nothing. That also removed a dependency the decision should never have had: previously ShowMesh was protected from binding alongside fppd only because fppd sets `SO_REUSEPORT` and not `SO_REUSEADDR`, so the mismatch failed. That is an accident of FPP's implementation, not a property of ours, and a future FPP release adding `SO_REUSEADDR` would have silently removed it. ADR-013 was updated to record both the finding and the reasoning.

The general lesson worth keeping: a socket-semantics claim verified only on macOS is not verified for a project that deploys on Linux. This is the same L1 versus L2 discipline the research records apply, applied to platform behavior.

**Verification gates:** `make check` passing; `go test -race ./...` passing on macOS and, in a Linux container, on Linux; builds clean for `darwin/arm64`, `linux/amd64`, `linux/arm64`, and `windows/amd64`; `make lint` reporting 0 issues; `FuzzDecode` clean across roughly 17 million total executions. The probe was exercised end to end against a synthetic loopback sender, including the two-source-port case that reproduced the competing-master wedge, which now applies both ports under one identity and reaches `stopped` correctly. CI ran on a real GitHub runner for the first time in this session and is green on `main` as of the last commit. Not verified: anything involving real FPP traffic.

---

## 2026-08-10

**Goal:** move from design package to implementation start: settle the deployment mechanism the architecture had left open, choose a module path, and lay out the build sequence so concurrent implementation work has an unambiguous order to follow.

**Completed:**

- Reviewed the design package (CLAUDE.md, ARCHITECTURE.md, OBSERVABILITY.md, the ADR set, and the research tracker) to confirm the packaging decision and build order would not contradict any accepted constraint.
- Recorded Docker as the primary coordinator deployment method in [ADR-012](../decisions/ADR-012-docker-coordinator-deployment.md) and added it to the [decisions index](../decisions/README.md).
- Wrote [BUILD-PLAN.md](BUILD-PLAN.md), sequencing Steps 0-3 and naming what is deliberately not yet sequenced.
- Started this log.
- Built all of Step 0: Go scaffold under `github.com/showmeshsystems/showmesh`, the coordinator binary (`internal/coordinator` plus `cmd/showmesh-coordinator`), an agent placeholder, package stubs under `pkg/`, Makefile, `.golangci.yml`, the multi-stage Dockerfile, the `deploy/` Compose bundle with Mosquitto, and `.github/workflows/ci.yml`.
- Ran a full review pass over the result and fixed everything it found. The review is worth summarizing because several findings were architectural rather than cosmetic:
  - `BrokerManager` originally exposed connection state as a bare boolean, which is precisely the shape ADR-011 rejects for health evidence. It now returns a `BrokerState` carrying `Connected`, `Since`, and `ObservedAt`, a probe goroutine re-stamps freshness every 5 seconds, and `/readyz` returns 503 with the observation age when evidence goes stale rather than reporting healthy on unrefreshed evidence. Partition detection remains bounded by the MQTT keepalive, now set to 15 seconds; that bound is documented in the code as a floor on confidence, not a guarantee.
  - `deploy/docker-compose.yml` used `depends_on` with `condition: service_healthy`, which reintroduced at the deployment layer exactly the broker dependency ADR-008 forbids: a failing Mosquitto healthcheck left the coordinator in state `Created`, never started. Changed to `service_started`.
  - `mosquitto.conf` set no `autosave_interval`, so retained state did not survive an unclean broker stop, while the file's comments claimed it did. ADR-008 depends on retained topics for state recovery, so a power cut would have produced a silently stale inventory. Now set to 30 seconds and verified by killing the broker with SIGKILL and confirming the retained message returned.
  - `deploy/README.md` documented a backup command using the wrong volume name. Compose prefixes volume names with the project name, so the command silently created a new empty volume and wrote an empty tarball with exit code 0. An operator's only disaster recovery artifact would have been empty with no error.
  - The documented rollback procedure did not roll back: with `image:` commented out and `build:` active, changing the version variable rebuilds the working tree and stamps it with an old version string, so `/version` would actively lie. The README now describes what is true today (check out the ref and rebuild) and marks the tag-based workflow as not yet available. ADR-012's matching consequence was corrected to state the same sequencing.
  - `.editorconfig` trimmed trailing whitespace on all files, which would have silently collapsed the trailing-double-space hard breaks used in all 11 ADRs plus ARCHITECTURE.md and OBSERVABILITY.md on the first editor save.
  - Lint found an unchecked error that CI would have caught and local gates would not, because `make check` did not run `lint`. Both fixed; `check` now runs lint.
  - The image shipped an Apache-2.0 binary with no license text, which section 4(a) requires. `LICENSE` is now copied into the image.

**Decisions made:**

- Go module path is `github.com/showmeshsystems/showmesh`. Recorded here; not architecture-critical enough for an ADR, but binding for scaffold work in Step 0.
- Docker is the primary and supported coordinator deployment method, recorded as ADR-012. This decision had been made verbally by the project owner previously and was formalized in this session.
- Build order follows CLAUDE.md's walking-skeleton sequence rather than starting with observability collectors, even though observability is the nearer-term product goal. Rationale: RES-002 (FPP MultiSync compatibility) is the project's only critical-risk research record still at L1, and building `pkg/multisync` first produces the `showmesh-multisync-probe` instrument needed to close RES-002's open bench items. Sequencing observability first would defer the highest-risk unknown instead of retiring it.
- MQTT client library is `github.com/eclipse/paho.golang/autopaho` speaking MQTT v5, chosen over the legacy `eclipse/paho.mqtt.golang` (in maintenance mode) for genuine reconnect handling and reason codes on results. Recorded here rather than as an ADR: it is a reversible implementation choice behind ADR-008's transport decision, not a durable constraint. Revisit if FPP's own MQTT output or a broker in a real deployment turns out to interoperate badly with a v5 client.
- SQLite driver will be `modernc.org/sqlite` (pure Go) rather than `mattn/go-sqlite3`. This follows from ADR-012's CGo-free requirement rather than being an independent choice, and is recorded in ADR-012's consequences.
- Only the coordinator is containerized. Node agents stay native per ARCHITECTURE §10.2 because they need direct GPU, HDMI, audio, EDID, and NDI access. Stated in ADR-012 so that "Docker first" is not later read as a project-wide mandate.

**Questions raised with the owner:**

- Deployment mechanism for the coordinator (Docker vs. native packages vs. Kubernetes vs. build-from-source). Answer: Docker Compose with a bundled Mosquitto broker, with support for pointing at an external broker; native and other packaging remain possible later but are not the supported path now. Recorded in ADR-012.
- Go module path. Answer: `github.com/showmeshsystems/showmesh`.
- Whether to commit and push. Answer: commit locally now, push once the `showmeshsystems` organization has been created.

**Deferred:**

- The host-networking question for coordinator-side multicast observation (flagged as a known constraint in ADR-012) is deferred to when coordinator-side MultiSync-observing collectors are built.
- Native/systemd packaging for the coordinator, considered and rejected as the *primary* path in ADR-012, remains open as a possible future addition.
- Splitting `internal/coordinator` into focused packages is deferred to the start of Step 2. At three files (config, HTTP server, MQTT client) the split would be ceremony; once the SQLite store, inventory, and reconciliation land it becomes necessary. Recorded as a known follow-up in BUILD-PLAN.md's Step 0 so it is not forgotten.
- The MQTT Last Will and session-expiry settings are deliberately unset. ADR-008 makes LWT the liveness mechanism, but it belongs to the agent in Step 2 and should be set deliberately there rather than guessed at now.
- No release or image-publishing workflow. CI builds the multi-arch image and does not push. This is why the rollback documentation had to describe rebuilding from a git ref.

**Verification gates:**

Run at the end of the session, all passing:

- `gofmt -l .` clean, `go vet ./...` clean, `go test -race ./...` passing, `CGO_ENABLED=0 go build ./...` clean, `make lint` reporting 0 issues (golangci-lint v2.6.2, pinned identically in the Makefile fallback and in CI), `make check` passing.
- `docker build` succeeds; the resulting image is roughly 8.8 MB, runs as UID 65532, and carries the license text.
- `docker compose config` validates with no `.env` file present, and the full stack comes up with the coordinator reaching `/readyz` 200 against the bundled broker.
- Coordinator behavior under broker failure verified by hand: unreachable broker at startup gives `/healthz` 200 and `/readyz` 503 with no exit and no crash loop; broker stopped for an extended period gives no exit; broker restarted returns `/readyz` to 200; SIGTERM exits cleanly both connected and never-connected.
- Retained-message survival across an unclean broker kill verified after setting `autosave_interval`.
- Not verified: CI has never run on a GitHub runner, because there is no remote yet. The workflow is unproven. The `linux/arm64` image leg is proven by cross compilation locally, not by running an arm64 container.
