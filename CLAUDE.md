# ShowMesh

ShowMesh is an open-source orchestration and observation layer for holiday light displays built around FPP (Falcon Player), xLights, and Resolume Arena. It coordinates these systems without replacing their scheduling, sequencing, mapping, or playback roles.

## Current state

Implementation started 2026-08-10. The design package remains authoritative: docs distinguish accepted architecture from unverified hypotheses, and that boundary must be respected in all work, including in code and code comments.

**Read `docs/build/BUILD-LOG.md` first in any new session.** Its "Current state" block says what actually works right now and what the next action is. `docs/build/BUILD-PLAN.md` holds the ordered steps and their status.

Module path is `github.com/showmeshsystems/showmesh`. Step 0 (foundation: scaffold, Docker image, Compose bundle, CI, minimal coordinator binary) is complete and verified locally. Nothing is pushed yet; there is no remote and CI has never run on a real runner.

## Repository map

- `docs/build/BUILD-LOG.md` — running session log and current state. Start here.
- `docs/build/BUILD-PLAN.md` — the ordered implementation steps that deliver the roadmap phases, with status.
- `docs/architecture/ARCHITECTURE.md` — the architecture baseline (vision, components, sync model, state/command models, roadmap phases 0–4; Phase 0 is read-only observability).
- `docs/architecture/OBSERVABILITY.md` — observability/alerting spec: signal model, collectors, dashboard, preview wall, pixel-current diagnostics, readiness evidence, alert model, phases O1–O5.
- `docs/decisions/` — ADRs. ADR-001..012 are Accepted. New durable constraints require a new ADR; superseding evidence requires a superseding ADR.
- `docs/research/` — research records RES-001..013 with an evidence ladder (L0 assumption → L1 source-verified → L2 bench → L3 integrated → L4 resilient). Empty evidence sections are work queues, not conclusions.
- `docs/reference-installation.md` — the concrete reference show topology that anchors test matrices.
- `deploy/` — the Compose bundle (coordinator plus Mosquitto) and its operator documentation.

## Non-negotiable design constraints (from accepted ADRs)

1. **FPP is the authoritative scheduler** (ADR-001). ShowMesh never becomes a second scheduler; lifecycle actions are exposed as native FPP commands.
2. **Nodes are modeled by capabilities**, not hardware types (ADR-002). Namespaced, versioned capability IDs with attributes.
3. **Desired and observed state are separate** (ADR-003). A command is not successful because it was sent; success requires evidence.
4. **Primitives + show macros + reduced local fallback** (ADR-004). Every critical macro defines what runs locally when the coordinator is unreachable.
5. **Media transport is pluggable** (ADR-005). NDI and HDMI/capture are adapters; nothing in the core may assume one transport.
6. **The coordinator is never in the real-time timing or media path.** A running show must survive coordinator loss — and broker loss (ADR-008).
7. **Go everywhere** (ADR-006). Coordinator and agent are Go in one repo; keep Go out of the per-frame media path.
8. **Media runs in GStreamer** (ADR-007). The agent builds/supervises pipelines; ShowMesh code owns sync, supervision, and health — never codecs or per-frame rendering.
9. **Control plane is MQTT/Mosquitto** (ADR-008). Versioned JSON payloads; retained topics for state; LWT for liveness; QoS 1 + idempotency keys for commands. Timing never traverses MQTT — MultiSync is the timing path.
10. **Storage is SQLite (WAL) with YAML export bundles** (ADR-009). Revisions immutable; agents cache a checksummed JSON fallback subset; stale nodes never overwrite coordinator state.
11. **License is Apache-2.0** (ADR-010). Never vendor or link NDI runtime binaries; dlopen only.
12. **Health and alerts are context-aware** (ADR-011). Evidence has provenance and freshness; stale = `unknown`, never healthy; lifecycle/maintenance context changes alert meaning; monitoring is read-only before it controls anything.
13. **Docker is the primary coordinator deployment** (ADR-012). Coordinator only; agents stay native because they need GPU/HDMI/audio/EDID/NDI access. The coordinator must build CGo-free (static, distroless, clean arm64 cross-compile), so pure-Go dependencies only: `modernc.org/sqlite`, never `mattn/go-sqlite3`. Nothing at the deployment layer may reintroduce a dependency the architecture forbids; the coordinator must start and stay up with no broker reachable.

## Working conventions

- Architecture-critical claims need L3 (integrated) verification before adoption and L4 (resilient) before show readiness.
- When research changes a durable constraint, write or supersede an ADR — don't silently edit the architecture spec.
- Research records must keep facts, assumptions, and hypotheses separate, with citations (URL + access date) for L1 claims and recorded versions/topology for L2+.
- Statuses: `unresearched` → `planned` → `testing` → `verified`/`rejected`/`blocked`; material environment changes move conclusions to `stale`.
- Normative language: **must**/**should**/**may** per RFC-2119 spirit.
- Keep the research tracker table (`docs/research/README.md`) in sync with individual record statuses.

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

Stack is decided (ADR-006..010, ADR-012): Go, GStreamer, MQTT/Mosquitto, SQLite, Apache-2.0, Docker for the coordinator. Do not relitigate these; supersede with a new ADR if evidence demands it.

Repo layout (now in place, do not reorganize without reason):

- `cmd/showmesh-coordinator/`, `cmd/showmesh-agent/` — binaries.
- `internal/` — coordinator and agent internals.
- `pkg/multisync/` — FPP MultiSync wire protocol (see RES-002 evidence for exact packet layout; port 32320, 'FPPD' header, little-endian sync fields).
- `pkg/capability/`, `pkg/command/`, `pkg/mqttproto/` — shared models matching ARCHITECTURE §6–8 and ADR-008 topic conventions.

Build order (walking skeleton first). `docs/build/BUILD-PLAN.md` is the authoritative, status-tracked version of this list:

1. Foundation: scaffold, Docker image, Compose bundle, CI, minimal coordinator binary. **Done 2026-08-10.**
2. `pkg/multisync` listener parsing START/STOP/SYNC/OPEN + ping/discover, with unit tests against hand-built packets from the RES-002 byte layout. Bench-verify against the real FPP player (RES-002 open items).
3. Agent: capability advertisement over MQTT (retained hello, LWT), health heartbeat; coordinator inventory + desired/observed state in SQLite.
4. Read-only FPP observability: collector, observation model with provenance and freshness, event history.
5. Agent: GStreamer pipeline supervision for a test pattern → NDI sink into Resolume (RES-004/006 bench).

Standing rules while building: unit tests never raise a research record above L1, only a bench capture against real hardware does. Never write a doc comment, log line, or document that claims verification that has not happened.

Follow FPP remote sync semantics: free-run through sync silence, slew ≤4 frames, jump when >0.5 s behind, STOP then ~5-frame blank delay.
