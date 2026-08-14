# Research Tracker

[Documentation index](../README.md) · [Architecture specification](../architecture/ARCHITECTURE.md) · [Decision records](../decisions/README.md)

Research records separate verified product behavior from architectural intent. Empty evidence sections are deliberate: they are work queues, not implied conclusions.

## Status

| ID | Topic | Status | Risk | Depends on |
|---|---|---|---|---|
| [RES-001](RES-001-resolume-smpte-behavior.md) | Resolume SMPTE behavior | planned (control/observability APIs **L2** 2026-08-14; SMPTE capabilities L1; fault behavior L0) | critical | RES-007 |
| [RES-002](RES-002-fpp-multisync-compatibility.md) | FPP MultiSync compatibility | planned (bench; protocol L2, hardware/network L1) | critical | — |
| [RES-003](RES-003-xlights-fpp-connect-compatibility.md) | xLights/FPP Connect compatibility | planned (compat surface L1 2026-08-13; integration L0) | high | RES-002 |
| [RES-004](RES-004-virtual-matrix-renderer-performance.md) | Virtual-matrix renderer performance | planned (reference profile decided; bench L0) | critical | RES-002 |
| [RES-005](RES-005-ndi-vs-hdmi-transport.md) | NDI versus HDMI transport | planned (transport roles decided; bench L0) | critical | RES-004, RES-006 |
| [RES-006](RES-006-linux-ndi-support.md) | Linux NDI support | planned (distribution resolved at L1; sender bench pending) | high | — |
| [RES-007](RES-007-audio-node-architecture.md) | Audio-node architecture | planned (bench, L0) | critical | RES-002 |
| [RES-008](RES-008-configuration-model.md) | Configuration model | planned (constraint survey L1, re-run 2026-08-13 at schema v6; D1–D6 decided, D1/D2 shipped in Step 7; §4 macro decisions L0) | high | — |
| [RES-009](RES-009-failure-mode-testing.md) | Failure-mode testing | planned | critical | all records |
| [RES-010](RES-010-projection-preview-monitoring.md) | Projection preview monitoring | planned (L1) | critical | RES-005, RES-006 |
| [RES-011](RES-011-pixel-current-diagnostics.md) | Pixel-current diagnostics | planned (L1) | critical | — |
| [RES-012](RES-012-device-telemetry-adapters.md) | Device telemetry adapters | planned (L1) | high | — |
| [RES-013](RES-013-telemetry-storage-and-alerting.md) | Telemetry storage and alerting | planned | high | RES-010, RES-011, RES-012 |
| [RES-014](RES-014-control-provider-model.md) | Control-provider model | unresearched | medium | RES-012 |
| [RES-015](RES-015-fpp-plugin-distribution-model.md) | FPP plugin repository, distribution, and on-host integration | planned (L1) | high | — |

## Verification levels

- **L0 — Assumption:** no supporting evidence.
- **L1 — Source verified:** confirmed in authoritative documentation or source code.
- **L2 — Bench verified:** reproduced in isolation with recorded versions and conditions.
- **L3 — Integrated:** reproduced across the intended show path.
- **L4 — Resilient:** survives soak, restart, recovery, and injected faults.

Architecture-critical claims should reach L3 before adoption and L4 before show readiness.

## Research workflow

1. Record versions, hardware, topology, and media inputs.
2. Keep facts, assumptions, and hypotheses separate.
3. Define measurable acceptance criteria before testing.
4. Preserve commands, configurations, logs, captures, and timestamps.
5. Record negative findings and limitations.
6. Publish a decision with fallback and revalidation triggers.
7. Add or supersede an ADR when a durable architectural constraint changes.

## Status lifecycle

`unresearched` → `planned` → `testing` → `verified`, `rejected`, or `blocked`. A material software, hardware, network, or topology change moves a previous conclusion to `stale` until revalidated.
