# Documentation

## Core documents

- [Architecture specification](architecture/ARCHITECTURE.md) — vision, system boundaries, components, synchronization, state, commands, deployment, and roadmap.
- [Observability specification](architecture/OBSERVABILITY.md) — signal model, collectors, dashboard, preview monitoring, diagnostics, readiness evidence, and alerting.
- [Operator UI specification](architecture/OPERATOR-UI.md) — the browser client architecture: isolation, API contract, real-time updates, staleness handling, information architecture, and responsiveness. What the dashboard displays stays in the observability specification.
- [Audio Engine specification](architecture/AUDIO-ENGINE.md) — authority model, playback sessions, drift policy, clock domains, buses and routing, output adapters, mixing, and failure behavior for audience-facing audio.
- [Resting Mode and Night Session specification](architecture/RESTING-MODE.md) — the FPP-authorized show loop, content-derived inter-show timing, transition choreography, final-show behavior, and optional configurable power/thermal interlocks.
- [Reference installation](reference-installation.md) — the concrete hardware, network, and timing topology that anchors research test matrices.
- [Research tracker](research/README.md) — open technical questions and the evidence required to resolve them.
- [Architecture decision records](decisions/README.md) — durable decisions, their context, and consequences.
- [Build plan](build/BUILD-PLAN.md) — the ordered implementation sequence that delivers the roadmap phases, with status tracking.
- [Build log](build/BUILD-LOG.md) — the chronological session record of implementation work.
- [Engineering lessons](build/LESSONS.md) — defects this project has shipped and caught, and the conventions that came out of them.

Work is organised into parallel delivery tracks as well as numbered build steps. The track documents live alongside the build plan: [Track B](build/TRACK-B-nodes-and-projection.md) nodes and projection, [Track C](build/TRACK-C-audio-node.md) the audio node, [Track D](build/TRACK-D-resolume.md) Resolume control and timecode, [Track E](build/TRACK-E-show-authoring-and-assets.md) show authoring and assets, and [Track F](build/TRACK-F-resting-mode.md) resting mode and night-session control.

## Bench captures

What an external system actually does, captured from a running instance before ShowMesh names anything in a specification. These are **evidence, not design**: no capture proposes an interface, and each one records the version, host, and parameters its results are only valid for.

- [FPP command vocabulary](bench/fpp-command-vocabulary.md) — FPP's real command list, argument encoding, and per-command behaviour, plus the exclusion register for every captured command Step 8 did not ship. Established that FPP's `200` means only that its dispatcher ran.
- [Resolume control surface](bench/resolume-control-surface.md) — Arena 7.23.2's REST, WebSocket, and OSC surfaces: what each exposes, how a clip and a layer are addressed in all three, what confirmation costs in latency and bytes, and what a restart does. Established that OSC cannot address a pinned clip, and that a disconnect confirms one layer transition after a connect does.
- [RES-002 capture procedure](bench/RES-002-capture-procedure.md) — the method for the FPP MultiSync wire capture.
- [Track B NDI spike](bench/TRACK-B-NDI-SPIKE.md) — NDI transport spike notes.

A capture supersedes desk research on the same question. Where one contradicts a research record, the research record is corrected by the orchestrating session rather than by the capture editing it in place.

## Reading order

1. Read the architecture specification for the intended system.
2. Read the decision records to understand which constraints are settled.
3. Use the research tracker to distinguish verified behavior from assumptions and to plan experiments.
4. Read the bench captures for any external system you are about to integrate with, before reading what this project has assumed about it.
5. Read the build plan and build log to see where implementation currently stands.

## Document conventions

Normative terms such as **must**, **should**, and **may** describe project requirements. Research documents use explicit lifecycle states: `unresearched`, `planned`, `testing`, `verified`, `rejected`, `blocked`, and `stale`.

The verification ladder measures confidence attained, not permission to proceed: building against an unverified claim is normal, and what must never happen is claiming verification that has not occurred. Only records explicitly named as blocking research (RES-002 on MultiSync, RES-006's Linux NDI question) gate build work, because their result decides an architecture question. Show readiness needs failure-injection (L4) verification.
