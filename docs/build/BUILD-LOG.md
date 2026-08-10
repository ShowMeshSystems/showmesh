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

Steps 0 (Foundation) and 1 (`pkg/multisync`) are complete. Both were verified to the limit of what unit tests and local runs can establish; neither has been exercised against real show hardware.

`pkg/multisync` holds the MultiSync wire codec, a listener that receives multicast, broadcast, and unicast on UDP 32320, a timeline state machine implementing FPP remote semantics on an injectable clock, and an opt-in discover-ping responder. `cmd/showmesh-multisync-probe` is the bench instrument built to close RES-002's five open items; it has not been run against a real FPP player yet, so RES-002 remains at L1 and its status is still `planned`. Changing that status is the owner's call once real captures exist, per `docs/bench/RES-002-capture-procedure.md`.

Step 0 detail follows.

Step 0 (Foundation) is complete and verified. The repository builds, tests, and lints clean; the coordinator image builds for `linux/amd64` and `linux/arm64` and runs; the Compose bundle brings up Mosquitto and the coordinator together and the coordinator reaches ready. The coordinator survives an unreachable broker, a broker stopped and restarted underneath it, and SIGTERM in every one of those states. There is no show logic, no MQTT topic work, and no persistence yet: `/healthz`, `/readyz`, and `/version` are the entire surface.

The repository is pushed to `github.com/ShowMeshSystems/showmesh`, currently private, and CI has run on a real GitHub runner. It earned its keep on the first run by failing a test that passes on macOS, which is what exposed the `SO_REUSEADDR` behavior difference recorded in ADR-013.

No UI code exists. The Operator UI was specified on 2026-08-10 (ADR-014..016, `docs/architecture/OPERATOR-UI.md`, RES-014) and sequenced as build Step 4, behind the read-only observability API in Step 3. Everything in that package is design intent; none of it has been implemented or verified.

No audio code exists either. The Audio Engine was specified in the same session (ADR-017..019, `docs/architecture/AUDIO-ENGINE.md`, RES-007 rewritten) and deliberately left unsequenced: RES-007 is critical-risk at L0, the multichannel interface has not been purchased, and every load-bearing claim is a bench fact. The next audio action is the RES-007 prototype, not implementation.

The immediate next action is Step 2, the control plane skeleton. Its first task is splitting `internal/coordinator` into focused packages before the SQLite store, inventory, and reconciliation land on top of the current flat one. Nothing in Step 2 is waiting on the owner.

Separately, the probe is ready to run against the real FPP player whenever the owner has bench time. That is what moves RES-002 from L1 to L2, and RES-002 is the highest-risk research record in the project.

One housekeeping item is open. The third-party product name discussed under "Conflicts found" in the audio session entry below was removed from the working copy of `docs/reference-installation.md`, but it remains in the git history of the initial commit and therefore on the private remote. Removing it from the working tree does not remove it from history. This is inert while the repository stays private and unshared, and it must be resolved by a history rewrite before the repository is made public or shared with anyone outside the project.

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
