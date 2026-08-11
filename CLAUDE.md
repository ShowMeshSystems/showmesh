# ShowMesh

ShowMesh is an open-source orchestration and observation layer for holiday light displays built around FPP (Falcon Player), xLights, and Resolume Arena. It coordinates these systems without replacing their scheduling, sequencing, mapping, or playback roles.

## Current state

Implementation started 2026-08-10. The design package remains authoritative: docs distinguish accepted architecture from unverified hypotheses, and that boundary must be respected in all work, including in code and code comments.

**Read `docs/build/BUILD-LOG.md` first in any new session.** Its "Current state" block says what actually works right now and what the next action is. `docs/build/BUILD-PLAN.md` holds the ordered steps and their status.

Module path is `github.com/showmeshsystems/showmesh`. Steps 0 (foundation), 1 (`pkg/multisync` plus the bench probe), and 2 (control plane skeleton: shared models, agent advertisement with Last Will and heartbeat, coordinator SQLite inventory and liveness) are complete. RES-002 is still L1: nothing has been run against a real FPP player. Step 3 is next.

The repository is pushed to a private remote and CI runs on a real GitHub runner. Two lessons from CI worth carrying: its first run caught a Linux-only `SO_REUSEADDR` behavior that passes on macOS, so a socket claim verified only on macOS is not verified for this project; and `make test-integration` runs the control plane against a real Mosquitto with the agent as a real subprocess, which on its first run caught three defects the unit suite passed over, including one where the unit test asserted the correct ordering against a fake while the real wiring did the opposite. **A test that passes whether or not the bug is present is worse than no test, because it also reports success.**

## Repository map

- `docs/build/BUILD-LOG.md` — running session log and current state. Start here.
- `docs/build/BUILD-PLAN.md` — the ordered implementation steps that deliver the roadmap phases, with status.
- `docs/architecture/ARCHITECTURE.md` — the architecture baseline (vision, components, sync model, state/command models, roadmap phases 0–4; Phase 0 is read-only observability).
- `docs/architecture/OBSERVABILITY.md` — observability/alerting spec: signal model, collectors, dashboard, preview wall, pixel-current diagnostics, readiness evidence, alert model, phases O1–O5. **Owns what the operator surface must display.**
- `docs/architecture/OPERATOR-UI.md` — the browser client architecture: isolation, API contract, real-time updates, staleness, information architecture, controls, responsiveness. **Owns how the client is built, never what it displays** — that split exists to stop the two documents drifting.
- `docs/architecture/AUDIO-ENGINE.md` — the audio subsystem: authority model, playback sessions, drift policy, clock domains, buses/routing, output adapters, mixing, failure behavior. Entirely unverified design intent; RES-007 is the work queue.
- `docs/decisions/` — ADRs. ADR-001..019 are Accepted. New durable constraints require a new ADR; superseding evidence requires a superseding ADR.
- `docs/research/` — research records RES-001..014 with an evidence ladder (L0 assumption → L1 source-verified → L2 bench → L3 integrated → L4 resilient). Empty evidence sections are work queues, not conclusions.
- `docs/reference-installation.md` — the concrete reference show topology that anchors test matrices.
- `deploy/` — the Compose bundle (coordinator plus Mosquitto) and its operator documentation.

## Non-negotiable design constraints (from accepted ADRs)

1. **FPP is the authoritative scheduler** (ADR-001). ShowMesh never becomes a second scheduler; lifecycle actions are exposed as native FPP commands.
2. **Nodes are modeled by capabilities**, not hardware types (ADR-002). Namespaced, versioned capability IDs with attributes.
3. **Desired and observed state are separate** (ADR-003). A command is not successful because it was sent; success requires evidence.
4. **Primitives + show macros + reduced local fallback** (ADR-004). Every critical macro defines what runs locally when the coordinator is unreachable. Narrowed twice: a controlled device holds no fallback, so steps touching a coordinator-hosted provider are labelled coordinator-required (ADR-016), and audio output failure's reduced local behavior is silence, not a handover (ADR-019).
5. **Media transport is pluggable** (ADR-005). NDI and HDMI/capture are adapters; nothing in the core may assume one transport.
6. **The coordinator is never in the real-time timing or media path.** A running show must survive coordinator loss — and broker loss (ADR-008).
7. **Go for coordinator and agent** (ADR-006). One repo, shared packages; keep Go out of the per-frame media path. The Operator UI is the one exception and is TypeScript (ADR-015) — this is not a licence for a polyglot backend.
8. **Media runs in GStreamer** (ADR-007). The agent builds/supervises pipelines; ShowMesh code owns sync, supervision, and health — never codecs or per-frame rendering. This includes audio: mixing, fades, ducking, interleave, and LTC generation are GStreamer, never a Go sample path.
9. **Control plane is MQTT/Mosquitto** (ADR-008). Versioned JSON payloads; retained topics for state; LWT for liveness; QoS 1 + idempotency keys for commands. Timing never traverses MQTT — MultiSync is the timing path.
10. **Storage is SQLite (WAL) with YAML export bundles** (ADR-009). Revisions immutable; agents cache a checksummed JSON fallback subset; stale nodes never overwrite coordinator state.
11. **License is Apache-2.0** (ADR-010). Never vendor or link NDI runtime binaries; dlopen only.
12. **Health and alerts are context-aware** (ADR-011). Evidence has provenance and freshness; stale = `unknown`, never healthy; lifecycle/maintenance context changes alert meaning; monitoring is read-only before it controls anything.
13. **Docker is the primary coordinator deployment** (ADR-012). Agents stay native because they need GPU/HDMI/audio/EDID/NDI access; the Operator UI is its own container per ADR-014. The coordinator must build CGo-free (static, distroless, clean arm64 cross-compile), so pure-Go dependencies only: `modernc.org/sqlite`, never `mattn/go-sqlite3`. Nothing at the deployment layer may reintroduce a dependency the architecture forbids; the coordinator must start and stay up with no broker reachable.
14. **Never share UDP 32320 with a running fppd** (ADR-013). `SO_REUSEPORT` load-balances unicast datagrams by 4-tuple hash, so a co-located listener can silently steal FPP's own unicast sync stream and desync a live show. Port sharing defaults to off and a bind conflict must fail loudly. Where co-location is unavoidable, use FPP's plugin callback boundary, not a second socket.
15. **The Operator UI is one client, not the system** (ADR-014). The control API is a public versioned contract usable without any UI; the UI holds no orchestration behavior, reaches ShowMesh only through that API and its change stream — never SQLite, MQTT, config files, or node-local state — ships as its own container, and is optional. The test: if every browser disappeared right now, the show continues correctly.
16. **The Operator UI is a TypeScript SPA** (ADR-015). Static assets, no runtime CDN dependency, offline-capable. API payload types are generated from or verified against the Go types, never hand-maintained twice.
17. **External devices are controlled devices driven by providers** (ADR-016). Projectors, relays, amps and the like are a resource class distinct from nodes — no agent, no advertisement, no local fallback, no LWT. Providers declare their configuration, actions, and telemetry as metadata; surfaces are built from that, not from per-device frontend code. Providers never enter the timing path, but device control *is* show-affecting: because a controlled device holds no fallback, any macro step touching a coordinator-hosted provider must be labelled coordinator-required per constraint 4. The metadata contract itself is unresearched (RES-014).
18. **ShowMesh owns audience-facing audio; nodes play local media** (ADR-017). FPP stays the scheduler and sequence authority, but ShowMesh owns audio sessions, routing, mixing, and placement, and FPP's own audio output goes unused. Nodes play complete local files on their own audio clock — never a PCM/sample-position stream. Drift is measured and corrected discretely at track boundaries, never by continuous rate manipulation: audio deliberately does NOT follow the MultiSync slew/jump model, and that divergence must not be "fixed".
19. **Program audio and LTC share one clock domain** (ADR-018). Minimum 3 output channels from one interface: 1–2 program, 3 LTC on a discrete output, never mixed into program. Never program on USB with LTC on Dante. This is a hardware purchasing constraint and a hard capability-placement constraint.
20. **Audio device loss fails silent** (ADR-019). Stop the failed output, keep session state, mark critical, alert, continue other outputs. NEVER auto-fall back to FPP audio — uncontrolled routing/gain into an FM transmitter is worse than silence. Node failover only after verifying media, output capabilities, physical routing, and health; operator-initiated until the roadmap's deferred failover item lands. This is a deliberate, recorded exception to constraint 4.

## Working conventions

- Architecture-critical claims need L3 (integrated) verification before adoption and L4 (resilient) before show readiness.
- When research changes a durable constraint, write or supersede an ADR — don't silently edit the architecture spec.
- Research records must keep facts, assumptions, and hypotheses separate, with citations (URL + access date) for L1 claims and recorded versions/topology for L2+.
- Statuses: `unresearched` → `planned` → `testing` → `verified`/`rejected`/`blocked`; material environment changes move conclusions to `stale`.
- Normative language: **must**/**should**/**may** per RFC-2119 spirit.
- Keep the research tracker table (`docs/research/README.md`) in sync with individual record statuses.
- `docs/private/` holds local working notes that are deliberately untracked and gitignored. Nothing moves out of it into tracked documentation, and nothing it discusses is published as established until there is something source-verified to record. Do not add it to version control.

## Build workflow

Implementation sessions are orchestrated rather than written end to end by one context:

- The orchestrating session writes the step specification, naming the ADRs and standing constraints the work is bound by, and does not implement.
- Implementation is delegated to subagents on the faster model, one per independent seam, running in parallel where they touch disjoint files.
- Review is delegated to subagents on the stronger model, given the diff plus the named ADRs, and instructed to hunt for constraint violations rather than style.
- Findings are folded back by the orchestrator, which also owns every edit under `docs/`. Builders do not write the build log.

This split exists because review is where the value has landed so far. The Step 0 and Step 1 passes caught defects unit tests could not: broker health exposed as a bare boolean against ADR-011, a Compose `depends_on` that reintroduced the broker dependency ADR-008 forbids, and a discover-ping responder that replied to an ephemeral source port and so could never have worked.

## Key external facts (L1, researched 2026-08-10 — see research records for citations)

- FPP MultiSync is an open, stable UDP protocol (port 32320, multicast 239.70.80.80), documented in `docs/ControlProtocol.txt` in the FPP repo; third-party listeners (ESPixelStick, xSchedule, Falcon firmware) interoperate without modifying FPP.
- The NDI SDK officially supports Linux amd64/arm64; open-source projects use MIT headers + `dlopen` of the user-installed runtime (never bundle/link the proprietary lib). Licensing details in RES-006.
- Resolume **Arena** (not Avenue) accepts SMPTE only as audio LTC, configured per clip; clip launch is NOT driven by timecode. REST API (7.8+, port 8080, `/api/v1`) + WebSocket give confirmable state; OSC gives low-latency triggers. Timecode-loss behavior is undocumented (holds last frame per forums) — bench test before relying on it.
- Previews: every NDI sender publishes a ~640-wide proxy stream; `ndisrc bandwidth=0` requests it, making six preview tiles cheap. Arena outputs NDI per Advanced-Output screen. Delivery candidates: GStreamer → WHIP → MediaMTX (WebRTC), or JPEG-over-one-WebSocket fallback. See RES-010.
- Pixel current: both primary deployed Kulp boards (K16-Max and the eFuse-variant K16A-B, operator-confirmed) report per-string current via eFuses; the standby K16-Pro is blade-fused only. FPP exposes `GET /api/fppd/ports` (`ma`, `status`, `pixelCount`), MQTT `port_status`, and an `EFUSE_TRIGGERED` preset hook. Deployed smart receivers are pre-V5 (no per-branch telemetry — treat as blind spots until upgraded). Normalize on FPP's port schema; keep current telemetry an optional capability. See RES-011.
- Projectors: all deployed projectors support PJLink (purchased for it; per-model class still to be probed via `CLSS?`). PJLink = TCP 4352, Class 1 covers power/input/mute/errors/lamp, Class 2 adds freeze/resolution; no mature Go lib — write `pkg/pjlink`. UniFi has an official API-key Integration API, but per-port PoE/error data still comes from the classic API (unpoller's Go lib). UPS = NUT. Env sensors arrive via MQTT (ESPHome/Zigbee2MQTT/ecowitt2mqtt). See RES-012.

## Licensing caution

NDI runtime redistribution is restricted; any repo license choice (GPL vs Apache/MIT) must account for the dlopen pattern. FFmpeg removed NDI over a GPL dispute. Do not vendor NDI binaries.

## Build phase guidance

Stack is decided (ADR-006..010, ADR-012, ADR-014..019): Go, GStreamer, MQTT/Mosquitto, SQLite, Apache-2.0, Docker for the coordinator, a TypeScript SPA Operator UI in its own container over a public versioned control API, and a Linux audio node owning audience audio. Do not relitigate these; supersede with a new ADR if evidence demands it.

Repo layout (now in place, do not reorganize without reason):

- `cmd/showmesh-coordinator/`, `cmd/showmesh-agent/` — binaries.
- `internal/` — coordinator and agent internals.
- `pkg/multisync/` — FPP MultiSync wire protocol (see RES-002 evidence for exact packet layout; port 32320, 'FPPD' header, little-endian sync fields).
- `pkg/capability/`, `pkg/command/`, `pkg/mqttproto/` — shared models matching ARCHITECTURE §6–8 and ADR-008 topic conventions.

Build order (walking skeleton first). `docs/build/BUILD-PLAN.md` is the authoritative, status-tracked version of this list:

1. Foundation: scaffold, Docker image, Compose bundle, CI, minimal coordinator binary. **Done 2026-08-10.**
2. `pkg/multisync` listener parsing START/STOP/SYNC/OPEN + ping/discover, with unit tests against hand-built packets from the RES-002 byte layout. Bench-verify against the real FPP player (RES-002 open items).
3. Agent: capability advertisement over MQTT (retained hello, LWT), health heartbeat; coordinator inventory + desired/observed state in SQLite.
4. Read-only FPP observability: collector, observation model with provenance and freshness, event history, plus the versioned public read API and change stream that ADR-014 requires. Design the API before the UI exists, not alongside it.
5. Read-only Operator UI: TypeScript SPA in its own container, dashboard, node and capability views, desired vs. observed, disconnect/staleness handling, responsive down to a phone.
6. Agent: GStreamer pipeline supervision for a test pattern → NDI sink into Resolume (RES-004/006 bench).

Standing rules while building: unit tests never raise a research record above L1, only a bench capture against real hardware does. Never write a doc comment, log line, or document that claims verification that has not happened.

Follow FPP remote sync semantics **for the lighting timeline**: free-run through sync silence, slew ≤4 frames, jump when >0.5 s behind, STOP then ~5-frame blank delay. Audio deliberately does not use this model — see constraint 18.
