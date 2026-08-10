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

Steps 0 (Foundation) and 1 (`pkg/multisync`) are complete and verified at L1.

`pkg/multisync` holds the MultiSync wire codec, a listener that receives multicast, broadcast, and unicast on UDP 32320, a timeline state machine implementing FPP remote semantics on an injectable clock, and an opt-in discover-ping responder. `cmd/showmesh-multisync-probe` is the bench instrument built to close RES-002's five open items; it has not been run against a real FPP player yet, so RES-002 remains at L1 and its status is still `planned`. Changing that status is the owner's call once real captures exist, per `docs/bench/RES-002-capture-procedure.md`.

Step 0 detail follows.

Step 0 (Foundation) is complete and verified. The repository builds, tests, and lints clean; the coordinator image builds for `linux/amd64` and `linux/arm64` and runs; the Compose bundle brings up Mosquitto and the coordinator together and the coordinator reaches ready. The coordinator survives an unreachable broker, a broker stopped and restarted underneath it, and SIGTERM in every one of those states. There is no show logic, no MQTT topic work, and no persistence yet: `/healthz`, `/readyz`, and `/version` are the entire surface.

Nothing has been pushed. The `showmeshsystems` GitHub organization does not exist yet, so commits are local only and CI has never run on a real runner. Creating the org and pushing is the first thing to do when that is possible, because the CI workflow is unproven until then.

The immediate next action is Step 2, the control plane skeleton. Its first task is splitting `internal/coordinator` into focused packages before the SQLite store, inventory, and reconciliation land on top of the current flat one. Two items are waiting on the owner: whether the port 32320 sharing constraint becomes an ADR, and whether the GitHub organization exists so this can be pushed.

Separately, the probe is ready to run against the real FPP player whenever the owner has bench time. That is what moves RES-002 from L1 to L2, and RES-002 is the highest-risk research record in the project.

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

**Questions raised with the owner:** whether the port 32320 sharing constraint becomes ADR-013, and whether the `showmeshsystems` organization exists yet so this can be pushed. Both open at the end of the session.

**Deferred:**

- Running the probe against the real FPP player. Nothing here has been seen by real FPP traffic, so RES-002 stays at L1 with status `planned`.
- Whether ShowMesh actually appears in the FPP MultiSync UI once discover responses are enabled. Unverified, and part of RES-002 open item 5.
- Splitting `internal/coordinator`, still carried into Step 2.

**Verification gates:** `make check` passing; `go test -race ./...` passing; builds clean for `darwin/arm64`, `linux/amd64`, `linux/arm64`, and `windows/amd64`; `make lint` reporting 0 issues; `FuzzDecode` clean across roughly 17 million total executions. The probe was exercised end to end against a synthetic loopback sender, including the two-source-port case that reproduced the competing-master wedge, which now applies both ports under one identity and reaches `stopped` correctly. Not verified: anything involving real FPP traffic, and CI, which still has never run on a real runner.

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
