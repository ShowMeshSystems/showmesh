# Operator UI Specification

[Documentation index](../README.md) · [Architecture specification](ARCHITECTURE.md) · [Observability specification](OBSERVABILITY.md) · [ADR-014](../decisions/ADR-014-operator-ui-is-an-api-client.md) · [ADR-015](../decisions/ADR-015-typescript-spa-frontend.md)

Status: Draft architecture baseline — design intent, not implemented and not verified  
Audience: Operators, maintainers, frontend and API contributors

## 1. Purpose and scope

The Operator UI is the browser-based surface an operator uses to answer whether the show is healthy, what it is doing, whether desired and observed state agree, and whether intervention is required.

This document owns the **client architecture**: isolation, the API contract, real-time transport, connection and staleness behavior, information architecture, safety of controls, responsiveness, authorization posture, and versioning.

It does **not** own what the dashboard displays. The required content of the operator dashboard — overview fields, the house topology map, detail views, the preview wall, readiness evidence, and alert presentation — is defined in [OBSERVABILITY §6](OBSERVABILITY.md#6-operator-dashboard) through §11 and must not be restated here. Where the two documents both apply, OBSERVABILITY defines *what must be shown* and this document defines *how the client that shows it may be built*. Duplicating either list guarantees drift.

### 1.1 The governing test

If every browser running ShowMesh disappeared at this instant, the show must continue correctly. Every design question in this document resolves against that test first.

## 2. Principles

### 2.1 The UI is a client, not the system

The Operator UI is one client of the ShowMesh control plane, not its owner. A future alternate UI, mobile application, CLI, automation system, or additional coordinator must be able to consume the same interfaces and reach the same operational outcomes. See [ADR-014](../decisions/ADR-014-operator-ui-is-an-api-client.md).

### 2.2 The control API is the product surface

Because the UI is a client, the coordinator's control API is a public, versioned contract rather than an internal convenience. It is designed, documented, and versioned deliberately, and it must be usable without the UI.

### 2.3 No unique behavior in the browser

The UI must contain no orchestration behavior that cannot be performed through the control API. Sequencing, reconciliation, retry, macro expansion, timing, and failover decisions belong to the coordinator and agents. A browser holds a presentation model; the coordinator remains authoritative.

### 2.4 Unknown is not healthy

The UI must never present stale state as current. Data age, provenance, and freshness are first-class display concerns per [ADR-011](../decisions/ADR-011-context-aware-observability.md) and [OBSERVABILITY §4.1](OBSERVABILITY.md#41-observations). Absence of evidence renders as `unknown`, never as healthy.

### 2.5 Surfaces derive from capabilities

The UI derives available monitoring and control surfaces from registered nodes and their advertised capabilities per [ADR-002](../decisions/ADR-002-capability-based-nodes.md). It must not assume fixed node classes such as "audio node", "projection node", or "FPP node".

### 2.6 One responsive surface

There is one Operator UI across desktop, laptop, tablet, and phone. A separate mobile interface must not be required, and the phone view is a primary operational surface rather than a read-only companion.

## 3. Isolation contract

No UI failure may interrupt show operation. The show must continue correctly through UI process crashes, container restarts, frontend runtime errors, bad UI deployments, lost browser connections, failed API requests, network loss between an operator device and the coordinator, and complete removal of the UI container. In each case the coordinator and nodes continue operating from existing desired state.

This obligation is stronger than it appears. A running show already survives coordinator loss and broker loss (ARCHITECTURE §2.4, [ADR-008](../decisions/ADR-008-mqtt-control-plane.md)), so "the UI cannot take down the show" is largely inherited rather than earned. The obligation that actually has teeth is §2.3: the UI must hold no behavior that the show or the operator depends on, because a component that is architecturally unable to affect the show can still become operationally load-bearing if it is the only place a required action exists.

## 4. Deployment shape

The Operator UI is deployed as its own container alongside the coordinator in the Compose bundle defined by [ADR-012](../decisions/ADR-012-docker-coordinator-deployment.md), and is independently deployable and upgradeable. Updating the UI must not require restarting show execution, restarting nodes, or interrupting audio or projection. Rationale, alternatives, and costs are recorded in [ADR-014](../decisions/ADR-014-operator-ui-is-an-api-client.md).

The expected stack is coordinator, Operator UI, and a broker, with supporting coordinator services added as required. Offline operation must not regress: the UI must load and function with no internet access once images are present, which forbids runtime dependencies on external CDNs, fonts, map tiles, or telemetry endpoints.

Open at this stage and to be settled when the UI is built: whether the UI container serves the API through a reverse proxy or the browser addresses the coordinator directly with explicit cross-origin configuration, and where TLS terminates.

## 5. Control API contract

The UI communicates with ShowMesh exclusively through documented coordinator APIs and real-time state interfaces. It must not read or write the coordinator SQLite database, node-local state, configuration files, or the MQTT broker, and must not depend on internal implementation details.

The API must provide, at minimum:

- an authoritative snapshot of inventory, capabilities, assignments, desired state, observed state, reconciliation status, and freshness;
- a subscribable stream of state changes and events;
- once write operations exist — the first release is read-only — command submission carrying the fields required by ARCHITECTURE §8.1, including an idempotency key, so a retried submission after a lost response cannot execute twice;
- explicit statements of unavailable evidence rather than omission, so the client can distinguish "not supported" from "not collected" from "collection failed".

### 5.1 Versioning and compatibility

The API is versioned. The UI declares the API version it requires and must detect an incompatible coordinator, presenting a clear, actionable error rather than degrading unpredictably or rendering partial state that looks authoritative. Because the UI and coordinator are separately deployable, a version skew window exists by construction and must be handled as a normal condition, not an exceptional one.

## 6. Real-time updates

The UI must receive state changes in near real time and must not depend solely on aggressive polling. The transport is not yet chosen; WebSocket and Server-Sent Events are both candidates, and the choice belongs with the API work in the build plan.

Relevant change classes include node online and offline, capability changes, desired-state changes, observed-state changes, health changes, sequence and playback changes, synchronization changes, and alerts. Capability reassignment and controlled-device state changes join the list as those capabilities land; neither exists yet.

The stream carries updates to a model the client obtained by snapshot. After any interruption the client must re-fetch an authoritative snapshot rather than resuming from its local model, because a gap in the stream is indistinguishable from a quiet system.

### 6.1 Rejected: MQTT directly to the browser

Mosquitto can serve MQTT over WebSocket, and the control plane already publishes retained state topics ([ADR-008](../decisions/ADR-008-mqtt-control-plane.md)). Connecting the browser to the broker directly is nonetheless rejected: it makes every browser a control-plane participant with broker credentials, couples the UI to internal topic structure so topic changes become breaking UI changes, exposes retained-topic and Last Will semantics that the coordinator exists to interpret, and gives the browser a data path the coordinator cannot authorize per request. It also contradicts §5. The coordinator remains the only thing the UI talks to.

## 7. Connection state and staleness

Loss of API connectivity must be obvious. When connectivity is lost the UI must display a clear disconnected or stale indicator, retain last known state where it remains useful, show when that state was last updated, disable actions that cannot safely be performed, and reconnect automatically with bounded backoff.

Two failure modes must be distinguished, because they mean different things to an operator: the UI cannot reach the coordinator, and the coordinator cannot reach the thing being displayed. The first is a browser problem; the second is a show problem. Presenting them identically would make the UI's own network trouble look like a show fault, and a show fault look like a browser glitch.

## 8. Information architecture

The UI separates three concerns, and configuration must not dominate the operational view.

**Monitor** — operational visibility into current behavior: health, show state, active nodes and assignments, synchronization, output state, faults, warnings, and reassignments.

**Control** — safe operational actions: starting and stopping applicable workloads, enabling and disabling outputs, moving between operational states (ARCHITECTURE §7.1) through show macros, executing configured device commands, and applying temporary overrides. Every item here is beyond the first release, which is read-only, and each becomes available only once the corresponding backend capability exists.

**Configure** — persistent desired configuration: controlled-device definitions, output mappings, capability assignments, integration configuration, and system preferences.

### 8.1 Views

- **Dashboard.** The primary operational view. Content and default prioritization per [OBSERVABILITY §6.2](OBSERVABILITY.md#62-main-overview).
- **Node views.** A node is a resource, so its content is the resource detail view defined in [OBSERVABILITY §6.4](OBSERVABILITY.md#64-detail-views). The node-specific additions this document asserts are connection state, software and version information, advertised capabilities with their individual status, and last contact time — the fields that exist because a node runs an agent and a projector does not.
- **Capability views.** Navigation by function rather than by machine, so an operator can reason about audio, projection, or synchronization without remembering which host currently owns each. Grouping is derived from the advertised capability identifiers in the ARCHITECTURE §6 vocabulary, which does not yet cover every function an operator thinks in — FPP integration, synchronization, and device control have no identifiers there today, and extending that vocabulary is backend work, not a UI workaround.
- **Controlled-device views.** Rendered from control-provider metadata rather than from device-specific frontend code; see §9.1 and [RES-014](../research/RES-014-control-provider-model.md).
- **Events and faults.** Recent history without requiring access to raw service logs, distinguishing routine events from actionable faults per [OBSERVABILITY §4.3](OBSERVABILITY.md#43-events) and §11.
- **Overrides.** See §12.

## 9. Capability-driven composition

Modules appear when the relevant capability exists. A node advertising `audio.engine` may surface audio controls and health; a node advertising `matrix.render` or a transport capability may surface projection status; FPP integration may surface sequence, player, and controller telemetry; an environmental source may surface temperature and enclosure health.

The UI must present three distinguishable things, because operators confuse them under pressure:

1. capabilities present in the system;
2. capabilities currently assigned to a workload;
3. what is actually happening now.

An unrecognized capability identifier or version must degrade to a generic panel showing raw normalized fields. It must never blank the view or fail the render — a node advertising something the UI has not been taught is the expected condition in a project where capabilities are versioned and nodes upgrade independently.

### 9.1 Provider-driven configuration and control

Where a control provider describes its own configuration fields, actions, and telemetry, the UI constructs the corresponding surfaces from that metadata rather than hard-coding each device type. Device-specific components remain permitted where generated forms become unreasonable, but provider metadata stays the source of truth for what a device supports.

Whether self-describing metadata is sufficient to generate usable operator surfaces is an open hypothesis, not an established pattern. It is tracked in [RES-014](../research/RES-014-control-provider-model.md) and must not be treated as settled while building the first release.

## 10. Desired versus observed state

The UI makes the [ADR-003](../decisions/ADR-003-desired-and-observed-state.md) model a first-class concept. Where relevant it shows desired state and revision, observed state and timestamp, reconciliation status from the ARCHITECTURE §7.2 vocabulary (`converged`, `progressing`, `degraded`, `unknown`, `conflicted`), health, current assignment, and any active fault or degraded condition.

Discrepancies must be obvious without inspecting logs: a projector expected on and reported off, an audio route expected active and unavailable, an assignment that has moved to a different node, a sequence expected running and currently stopped, an output expected healthy and reporting degraded.

A command that has been sent is not a command that has succeeded. The UI must not render a requested state as an achieved one; a command in flight is `progressing` until evidence arrives or the deadline expires.

## 11. Controls and safety

Actions capable of interrupting a live show must be clearly distinguishable from monitoring interactions, and confirmation must be proportional to consequence. Powering off a projector during a live show should not be as easy to trigger accidentally as opening its status panel; requiring the same ceremony for routine actions trains operators to dismiss it.

Consequence is contextual. The same action carries different weight in `resting`, `pre-show`, `live`, and `maintenance` (ARCHITECTURE §7.1), and the UI should use lifecycle state to scale confirmation rather than applying one fixed policy, consistent with [ADR-011](../decisions/ADR-011-context-aware-observability.md).

Authorization is enforced by the coordinator API, never by hiding a control. The UI is not a security boundary.

## 12. Manual overrides

Active manual overrides must be visible, and an override must never silently replace desired-state configuration. Where the information exists the UI shows what is overridden, the normal desired state, the overridden desired state, the identity that created it, when it was created, and whether it expires automatically.

An override that outlives the operator's memory of creating it is a fault source, not a convenience. Timed and automatically expiring overrides should be considered when the override model is built.

## 13. Responsive and mobile operation

Responsive behavior may change layout, density, navigation, and control presentation, but all important operational functionality must remain available on a phone. Typical mobile tasks include checking system health from outside, identifying which device has failed, checking projector and audio state, reviewing recent events, and — once the corresponding write operations exist — starting and stopping show-related functions where permitted, acknowledging faults, and applying a permitted override.

Controls used during active operation must remain usable without excessive zooming, horizontal scrolling, or small touch targets. The operating environment is a design input: outdoors, after dark, in cold weather, plausibly with gloves, on a phone screen at night-adapted brightness. The show-time high-contrast mode required by [OBSERVABILITY §6.1](OBSERVABILITY.md#61-global-behavior) applies to the mobile layout, not only the desktop one.

## 14. Authentication and authorization

Commands require authenticated identities and authorization by target and action (ARCHITECTURE §10.4). Authorization must ultimately be enforced by the coordinator API.

The initial deployment may use a simple authentication model appropriate to an isolated show VLAN, but the mechanism is an explicit decision to be recorded when the control API gains write operations — not a default that arrives by omission. A UI that can stop a show with no authentication is a defensible choice on a private VLAN and an indefensible accident otherwise.

Future role-based access may include viewer, operator, and administrator. The API and UI architecture must not block that, which in practice means authorization decisions are expressed server-side and returned to the client rather than inferred by it.

## 15. High-availability compatibility

High availability is not implemented and is not in scope. [ADR-009](../decisions/ADR-009-sqlite-configuration-storage.md) makes the coordinator a single-writer store today, so multi-coordinator operation would require its own decision and is not promised by this document.

The obligation here is only to avoid gratuitously blocking it, which reduces to four concrete requirements and no speculative abstraction beyond them:

- UI sessions do not depend on process-local coordinator state;
- API endpoints expose authoritative state independently of any UI session;
- show state remains authoritative in ShowMesh, never in the browser;
- the UI recovers cleanly after coordinator or API loss, per §7.

Building coordinator-selection machinery, client-side quorum awareness, or multi-endpoint failover into the first release is explicitly out of scope. Those are HA design decisions and must not be pre-empted by frontend guesses.

## 16. First release scope

The first Operator UI release prioritizes:

1. the system dashboard;
2. node health and capability inventory;
3. desired versus observed state;
4. current show and sequence visibility;
5. fault and event visibility;
6. capability-specific operational views;
7. basic safe controls;
8. configuration for the devices and integrations the initial implementation requires;
9. responsive desktop and mobile operation;
10. reliable disconnect and reconnect behavior.

Read-only comes first ([OBSERVABILITY §2.5](OBSERVABILITY.md#25-read-only-monitoring-comes-first), under [ADR-011](../decisions/ADR-011-context-aware-observability.md)), which places items 1 through 6, 9, and 10 ahead of 7 and 8 in the build plan.

### 16.1 Non-goals for the first release

Independent show orchestration logic, browser-side scheduling, direct node communication, a full metrics or observability replacement, arbitrary dashboard customization, complete historical analytics, native mobile applications, and any high-availability implementation. These must not be architecturally prevented, but none are required.

## 17. Open questions and unresolved conflicts

These are recorded rather than resolved. Several arise from applying the brainstorm specification to the accepted roadmap and finding disagreement.

- **Failover and reassignment.** ARCHITECTURE §12 defers automatic failover during a live set until evidence supports it, and places reassignable capability workloads in Phase 3. The UI cannot show or initiate something the system does not do, so first-release scope is displaying current capability assignment and nothing more. Both automatic-failover display and operator-initiated reassignment follow their features, not the other way around.
- **Audio authority.** The authority model this alert presumed now exists: [ADR-017](../decisions/ADR-017-showmesh-owns-audience-audio.md) makes ShowMesh authoritative for sessions, routing, and placement while the active node owns its own playback clock. What is still missing is what would make the alert actionable — the drift threshold at which authority should be considered lost, and the detection latency that threshold implies. Both are open bench items in [RES-007](../research/RES-007-audio-node-architecture.md), so the alert stays unspecified for a reason that has changed.
- **Control-provider metadata.** The shape of provider descriptors and whether they can drive usable generated forms is [RES-014](../research/RES-014-control-provider-model.md), currently unresearched.
- **Real-time transport.** WebSocket versus Server-Sent Events, to be decided with the API work.
- **Initial authentication mechanism.** Undecided; see §14.
- **Preview wall in the UI.** Delivery mechanism is blocked on [RES-010](../research/RES-010-projection-preview-monitoring.md); the preview wall is not part of the first UI release.
- **Origin, proxying, and TLS termination** between the UI container and the coordinator; see §4.
