# Build Plan

[Documentation index](../README.md) · [Architecture specification](../architecture/ARCHITECTURE.md) · [Research tracker](../research/README.md)

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

Status: not started

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

**Bound by:** ADR-001, ADR-006, RES-002.

Step 1 completing does not move RES-002 past L1. Unit tests raise nothing above L1; only a capture against a real FPP 9.x or 10.x player moves RES-002 to L2.

## Step 2: Control plane skeleton

Status: not started

**Goal:** the coordinator and an agent can see each other and agree on state, with no show logic yet.

**Deliverables:**

- `pkg/mqttproto` with the ADR-008 topic conventions and versioned JSON envelopes.
- `pkg/capability` with the ARCHITECTURE §6 model.
- Node agent hello with retained capability advertisement, Last Will, and health heartbeat.
- Coordinator SQLite store per ADR-009 holding inventory and observed state.

**Acceptance criteria:**

- An agent appears in coordinator inventory after start.
- The agent disappears into `unknown` after an unclean kill via Last Will.
- The coordinator restores state from retained topics after its own restart.

**Bound by:** ADR-002, ADR-003, ADR-008, ADR-009, ADR-012, RES-008.

## Step 3: Read-only FPP observability

Status: not started

**Goal:** the first slice of real observability value, and the start of ARCHITECTURE Phase 0 / OBSERVABILITY Phase O1.

**Deliverables:**

- FPP collector over REST and MQTT.
- The observation model from OBSERVABILITY §4.1 with provenance, freshness, and expiry so stale evidence becomes `unknown`.
- Event history.
- A first read-only status API.

**Bound by:** ADR-003, ADR-011, RES-012, RES-013.

## Not yet sequenced

These deliberately come later, and why:

- **Web dashboard stack.** ARCHITECTURE §4.1 leaves the frontend stack open on purpose; it is not chosen until UI work begins.
- **Resolume adapter.** Blocked on RES-001 bench work (Resolume SMPTE and clip-launch behavior is still L0/L1).
- **GStreamer pipeline supervision and NDI transport.** Blocked on RES-004 (renderer performance), RES-005 (NDI vs. HDMI transport), and RES-006 (Linux NDI support), all unresearched or L1 only.
- **Preview delivery.** Blocked on RES-010.
- **Pixel-current diagnostics.** Blocked on RES-011.

## Standing constraints for every step

- FPP stays authoritative; ShowMesh never becomes a second scheduler.
- The coordinator is never in the timing or media path.
- A running show survives coordinator loss and broker loss.
- Commands need evidence of effect, not acknowledgement of receipt.
- Stale evidence is `unknown`, never healthy.
- New durable constraints require a new ADR, not an edit to the architecture spec.
