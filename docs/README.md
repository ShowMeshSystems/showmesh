# Documentation

## Core documents

- [Architecture specification](architecture/ARCHITECTURE.md) — vision, system boundaries, components, synchronization, state, commands, deployment, and roadmap.
- [Observability specification](architecture/OBSERVABILITY.md) — signal model, collectors, dashboard, preview monitoring, diagnostics, readiness evidence, and alerting.
- [Operator UI specification](architecture/OPERATOR-UI.md) — the browser client architecture: isolation, API contract, real-time updates, staleness handling, information architecture, and responsiveness. What the dashboard displays stays in the observability specification.
- [Audio Engine specification](architecture/AUDIO-ENGINE.md) — authority model, playback sessions, drift policy, clock domains, buses and routing, output adapters, mixing, and failure behavior for audience-facing audio.
- [Reference installation](reference-installation.md) — the concrete hardware, network, and timing topology that anchors research test matrices.
- [Research tracker](research/README.md) — open technical questions and the evidence required to resolve them.
- [Architecture decision records](decisions/README.md) — durable decisions, their context, and consequences.
- [Build plan](build/BUILD-PLAN.md) — the ordered implementation sequence that delivers the roadmap phases, with status tracking.
- [Build log](build/BUILD-LOG.md) — the chronological session record of implementation work.
- [Engineering lessons](build/LESSONS.md) — defects this project has shipped and caught, and the conventions that came out of them.

## Reading order

1. Read the architecture specification for the intended system.
2. Read the decision records to understand which constraints are settled.
3. Use the research tracker to distinguish verified behavior from assumptions and to plan experiments.
4. Read the build plan and build log to see where implementation currently stands.

## Document conventions

Normative terms such as **must**, **should**, and **may** describe project requirements. Research documents use explicit lifecycle states: `unresearched`, `planned`, `testing`, `verified`, `rejected`, `blocked`, and `stale`.

Architecture-critical claims should reach integrated verification before adoption and failure-injection verification before show readiness.
