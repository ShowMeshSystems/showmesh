# Architecture Decision Records

[Documentation index](../README.md) · [Architecture specification](../architecture/ARCHITECTURE.md) · [Research tracker](../research/README.md)

ADRs record durable choices and their consequences. They do not replace research evidence. When new evidence invalidates a decision, add a superseding ADR and update the old record's status.

| ADR | Decision | Status |
|---|---|---|
| [ADR-001](ADR-001-fpp-is-authoritative.md) | FPP is the authoritative scheduler | Accepted (scheduled-start scope narrowed by ADR-038) |
| [ADR-002](ADR-002-capability-based-nodes.md) | Nodes are modeled by capabilities | Accepted |
| [ADR-003](ADR-003-desired-and-observed-state.md) | Desired and observed state remain separate | Accepted |
| [ADR-004](ADR-004-layered-commands-and-fallback.md) | Use primitives, macros, and reduced local fallback | Accepted (narrowed by ADR-016, ADR-019) |
| [ADR-005](ADR-005-pluggable-media-transport.md) | Media transport is pluggable | Accepted (narrowed by ADR-026: NDI named as reference) |
| [ADR-006](ADR-006-go-implementation-language.md) | Go is the implementation language | Accepted (frontend clause closed by ADR-014, ADR-015) |
| [ADR-007](ADR-007-gstreamer-media-engine.md) | Node agent supervises GStreamer as media engine | Accepted |
| [ADR-008](ADR-008-mqtt-control-plane.md) | MQTT is the coordinator↔agent transport | Accepted |
| [ADR-009](ADR-009-sqlite-configuration-storage.md) | SQLite authoritative store; YAML portable bundles | Accepted (agent-cache `checksummed` superseded by ADR-025; asset bytes excluded by ADR-028) |
| [ADR-010](ADR-010-apache-2-license.md) | Apache-2.0 license | Accepted |
| [ADR-011](ADR-011-context-aware-observability.md) | Health and alerts are context-aware | Accepted |
| [ADR-012](ADR-012-docker-coordinator-deployment.md) | Docker is the primary coordinator deployment | Accepted (bundle extended by ADR-014) |
| [ADR-013](ADR-013-no-fpp-control-port-sharing.md) | ShowMesh must not share the FPP control port with a running fppd | Accepted |
| [ADR-014](ADR-014-operator-ui-is-an-api-client.md) | The Operator UI is an optional client of a versioned public control API | Accepted (extended to authoring by ADR-030) |
| [ADR-015](ADR-015-typescript-spa-frontend.md) | The Operator UI is a TypeScript single-page application | Accepted |
| [ADR-016](ADR-016-controlled-devices-and-control-providers.md) | Externally controlled devices are driven by control providers | Accepted |
| [ADR-017](ADR-017-showmesh-owns-audience-audio.md) | ShowMesh owns audience audio; nodes play local media | Accepted (narrowed by ADR-046, 2026-08-28: rate trim against a locked shared clock is permitted; slews, seeks, and position-feed chasing are not) |
| [ADR-018](ADR-018-program-and-ltc-share-a-clock-domain.md) | Program audio and LTC share one clock domain | Accepted |
| [ADR-019](ADR-019-audio-device-loss-fails-silent.md) | Audio device loss fails silent, no automatic FPP fallback | Accepted |
| [ADR-020](ADR-020-control-api-shape-and-change-stream.md) | Control API is versioned REST with a Server-Sent Events change stream | Accepted |
| [ADR-021](ADR-021-read-api-authentication-posture.md) | Read API ships optional shared-secret auth and no authorization | Superseded by ADR-024 (rules 3, 4 and CORS carried forward) |
| [ADR-022](ADR-022-operator-ui-serves-the-api-same-origin.md) | Operator UI serves the API same-origin and never holds a credential | Accepted (decision 4 superseded by ADR-024) |
| [ADR-023](ADR-023-change-stream-observation-deltas.md) | Change stream carries observation deltas, opt-in per connection | Accepted |
| [ADR-024](ADR-024-identity-authorization-and-audit.md) | Identity, authorization, and audit for the write surface | Accepted (decision 6 amended 2026-08-14: Origin fallback; decision 11 narrowed by ADR-035) |
| [ADR-025](ADR-025-agent-fallback-cache-is-signed.md) | Agent fallback cache is signed; verifying key pinned at enrollment | Accepted |
| [ADR-026](ADR-026-renderer-surface-model-and-reference-transport.md) | Renderer models logical surfaces; NDI is the reference transport | Accepted (L0 design intent; narrows ADR-005) |
| [ADR-027](ADR-027-show-and-surface-model.md) | Show and surface model; xLights owns authoring, ShowMesh owns configuration | Accepted |
| [ADR-028](ADR-028-show-asset-store-and-identity.md) | Show asset store; a filename is not an asset identity | Accepted (decision 10 amended in 2026-08-17: re-uploading superseded bytes is a rollback; implemented 2026-08-18 in PR #17, not yet merged) |
| [ADR-029](ADR-029-logical-actions-and-integration-bindings.md) | Macros invoke logical actions, never protocol commands | Accepted (decisions 1, 2 and 4 implemented; **decision 3, the raw protocol escape hatch, has never been built** and is an open owner question, Linear SM-61) |
| [ADR-030](ADR-030-operator-ui-is-the-authoring-surface.md) | The Operator UI becomes the authoring surface | Accepted (extends ADR-014, ADR-015) |
| [ADR-031](ADR-031-macro-execution-model.md) | The macro execution model | Accepted (decision 2's default and decision 5 superseded by ADR-035) |
| [ADR-032](ADR-032-resolume-composition-configuration-from-file.md) | Resolume composition configuration comes from the composition file, not the API | Accepted (narrows the adapter specification's §3.8 and §6.4) |
| [ADR-033](ADR-033-show-mode.md) | Program Mode and Show Mode are one system-wide operating mode | Accepted |
| [ADR-035](ADR-035-a-run-always-runs-every-step.md) | A run always runs every step | Accepted (supersedes ADR-031 decision 2's default; narrows ADR-024 decision 11) |
| [ADR-036](ADR-036-dispatch-configuration-applies-without-a-restart.md) | Configuration that governs dispatch applies without a restart | Accepted |
| [ADR-037](ADR-037-resolume-references-are-names-not-ids.md) | A Resolume reference is a name, not an object id | Accepted (owner, 2026-08-15; not yet implemented) |
| [ADR-038](ADR-038-fpp-authorizes-night-sessions.md) | FPP authorizes night sessions; ShowMesh advances them | Accepted (owner, 2026-08-16; not yet implemented) |
| [ADR-039](ADR-039-operator-configuration-is-store-backed.md) | Operator configuration is store-backed; the environment holds only what precedes it | Accepted (owner, 2026-08-17; extends ADR-030 and CLAUDE.md's CLI-parity constraint; implemented by Track G) |
| ADR-040 | The renderer extracts channels; GStreamer owns every frame the audience sees | Accepted (owner, 2026-08-17; **narrows** ADR-007's per-frame prohibition without superseding it, the way ADR-026 narrowed ADR-005. Implemented by Track B seams B2 and B3; the record itself lands on `main` with the Track B fold. Creates a measurement obligation, not a guard: achieved frame rate at the configured geometry must be reported as evidence.) |
| [ADR-041](ADR-041-operator-recovery-is-not-a-calendar-intent.md) | Operator recovery is not a calendar intent | Accepted (owner, 2026-08-19; **narrows** ADR-038 decision 2's "closed command vocabulary" to mean the set of lifecycle intents FPP's calendar invokes, leaving that set closed and placing `end-session` alongside it. Carries a rule that outlives the verb: a recovery action may never obtain an outcome the lifecycle commands would have refused. Implemented by Track F seam F2.) |
| [ADR-042](ADR-042-cgo-in-the-native-agent.md) | cgo lives in the native agent, and C generates every sample | Accepted (owner, pre-approved 2026-08-19 with the go-gst backend, issued 2026-08-20; **narrows** ADR-006 and restates ADR-007's per-sample line for audio without superseding either. ADR-012's CGo-free rule is unchanged and still governs the coordinator image. Authorizes exactly two C libraries, go-gst and libltc.) |
| [ADR-043](ADR-043-show-scoped-cues-and-playlist-authority.md) | Show-scoped Cues and pluggable Playlist authority | Accepted (owner, 2026-08-20; **narrows** ADR-001 to FPP-backed Playlists, makes runner authority explicit, and requires active-show isolation across Cue activation and execution.) |
| [ADR-044](ADR-044-agent-inbound-http-listener.md) | The agent serves one inbound HTTP listener, for xLights ingestion only | Accepted (owner, 2026-08-25; adds the agent's first inbound HTTP surface as a bounded xLights compatibility shim that is deliberately not part of the public API. Unauthenticated by accepted risk with three bounds in code. Supersedes and narrows nothing; answers the open decisions in TRACK-E-FPP-CONNECT.md.) |
| ADR-045 | Multiple audio nodes, each with a role; cue and night-mode outputs name a target node | Reserved (Lane 20 seam SM-311 on `dev/multi-audio`; carries SM-308's five owner decisions. The record lands with the SM-311 fold; the number is reserved here so Lanes 16 and 17 cannot mint it.) |
| [ADR-046](ADR-046-rate-lock-to-a-shared-clock-is-not-chasing.md) | Rate-locking an audio interface to a shared PTP clock is not chasing | Accepted (owner, 2026-08-28; **narrows** ADR-017's rate rule to slews and seeks and clarifies ADR-018's rejected alternative. Permits a bounded ppm-scale rate trim of a whole interface against a locked shared PTP clock, never against the FPP position feed. Amends AUDIO-ENGINE §4.2 and §16 and ARCHITECTURE §5. Ships only through RES-019's staged plan.) |

**There is no ADR-034, and that is deliberate.** ADR-035 and ADR-036 were issued on the `step-9-wave-3` branch as 033 and 034, colliding with show mode, which Track D issued on `main` the same day. They were renumbered when that branch merged on 2026-08-15; 034 was left unused rather than reassigned, so that the number in an older reference is never ambiguous.

## Record template

Each ADR contains status, context, decision, consequences, alternatives, related research, and supersession information. Status values are `Proposed`, `Accepted`, `Deprecated`, and `Superseded`.
