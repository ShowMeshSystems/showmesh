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
- A first read-only status API, designed as the versioned public contract required by [ADR-014](../decisions/ADR-014-operator-ui-is-an-api-client.md): documented, usable without the UI, distinguishing unsupported from uncollected from failed, and carrying provenance and freshness on every observation.
- A subscribable change stream alongside the snapshot API, since [OPERATOR-UI §6](../architecture/OPERATOR-UI.md#6-real-time-updates) forbids depending *solely* on aggressive polling. Transport (WebSocket or Server-Sent Events) is chosen here.

**Acceptance criteria:**

- The API is exercised end to end by a non-UI client, to prove it is usable without a browser rather than only believed to be.
- An interrupted change stream is followed by an authoritative snapshot re-fetch, not a resumed local model.

**Bound by:** ADR-003, ADR-011, ADR-014, RES-012, RES-013.

**Why the contract work lands here and not in Step 4:** if the API is designed alongside the UI that consumes it, behavior settles in whichever layer is easier to change and the API quietly stops being independently usable. Step 3 finishing before UI work starts is what keeps ADR-014 real.

## Step 4: Read-only Operator UI

Status: not started

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
