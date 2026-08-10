# ADR-016: Externally Controlled Devices Are Modeled as Controlled Devices Driven by Control Providers

Status: Accepted  
Date: 2026-08-10

## Context

[ADR-002](ADR-002-capability-based-nodes.md) models *nodes* by capability. A node runs an agent, advertises what it can do, holds fallback configuration, and reports liveness through MQTT Last Will ([ADR-008](ADR-008-mqtt-control-plane.md)).

Projectors, amplifiers, AV receivers, smart relays, power controllers, network-controlled audio devices, and displays fit none of that. They run no ShowMesh code, cannot advertise anything, cannot hold fallback state, and produce no Last Will. Their liveness is whatever a poll last returned. Treating them as degenerate nodes would corrupt the node model's meaning, and every health rule that assumes an agent would need an exception.

[RES-012](../research/RES-012-device-telemetry-adapters.md) has already established the read side at L1: PJLink for projectors, UniFi for network, NUT for UPS, MQTT-native ingestion for environmental sensors, with proposed normalized field sets. It says nothing about the write side, about where control definitions are persisted, or about how an operator surface learns what a given device supports. The Operator UI work forces those questions, because a UI that hard-codes each device type requires a frontend change for every new piece of show equipment.

## Decision

**Controlled devices are a first-class coordinator resource class, distinct from nodes.** The coordinator persists a definition per device carrying at minimum: name, device type, location or logical grouping, associated node or capability where one is required to reach it, control transport, network address where applicable, provider, supported actions, optional health mechanism, desired state, and observed state. Persistence follows [ADR-009](ADR-009-sqlite-configuration-storage.md) with the same revision and export semantics as the rest of the configuration.

**Control and telemetry are implemented by providers.** A provider is a driver that declares, as machine-readable metadata, the configuration it requires, the actions it supports, and the telemetry it produces. Operator surfaces and configuration forms are constructed from that metadata rather than from device-specific frontend code. Device-specific components remain permitted where generated surfaces become unreasonable, but provider metadata remains the source of truth for what a device supports.

**A provider runs wherever it can reach the device.** Network-reachable providers (PJLink, HTTP, vendor APIs, OSC) may run in the coordinator. Providers requiring physical attachment (serial, relay, GPIO) run in a node agent and are advertised as a node capability, which is how the controlled-device model and the ADR-002 capability model join: the device is a controlled device, and the node's ability to reach it is a capability.

**Controlled devices inherit the existing state and evidence rules.** Desired and observed state stay separate ([ADR-003](ADR-003-desired-and-observed-state.md)); a sent command is not a successful one. Evidence carries provenance and freshness, and a device whose telemetry has gone stale is `unknown`, never healthy ([ADR-011](ADR-011-context-aware-observability.md)). A device that supports no status query is `unknown` with a provenance reason recording that confirmation is structurally unavailable rather than merely missing — no new health state is introduced, because OBSERVABILITY §4.2's five states already cover it and "insufficient evidence" is exactly this case.

**Providers are never in the timing or media path.** Nothing in this model may become a per-frame or per-beat dependency. Device control is nonetheless show-affecting: a projector that fails to power on stops the show as surely as a decoder that stalls. Management-plane means outside the frame clock, not unimportant.

**Macro steps that touch controlled devices must be labelled coordinator-required.** [ADR-004](ADR-004-layered-commands-and-fallback.md) requires every critical macro to define what runs locally when the coordinator is unreachable. A controlled device holds no fallback of its own and, for coordinator-hosted providers, is unreachable exactly when the coordinator is. Macros such as `Blackout` and `Enter Pre-Show Mode` must therefore state explicitly which of their steps cannot execute without the coordinator, and an operator procedure must exist for those steps. Where a device's action is show-critical enough that this is unacceptable, the provider belongs on a node rather than in the coordinator, which is one of the reasons provider placement is a per-device decision.

## Consequences

- A new shared package defines the provider interface and its metadata; the projector provider (`pkg/pjlink`, already anticipated in RES-012) is the first implementation and the first test of whether the abstraction is correct.
- Adding a device type becomes a backend provider plus metadata, with no frontend change in the common case. Whether that actually holds is the central open question, tracked in [RES-014](../research/RES-014-control-provider-model.md), and it is a hypothesis at L0 today.
- RES-012's normalized telemetry field sets become the observed-state side of this model rather than a parallel structure. The two must be reconciled when RES-012 reaches bench, not maintained separately.
- Per-device credentials (PJLink passwords, API keys) become configuration secrets and must stay out of portable exports per ARCHITECTURE §10.4.
- Coordinator loss now has a device-shaped consequence it did not have before, bounded by the labelling rule above. This does not weaken the standing constraint that a *running* show survives coordinator loss — playback, timing, and media are untouched — but it does mean a lifecycle transition that depends on a coordinator-hosted provider will not complete, and that must be visible in the macro definition rather than discovered during a show. [RES-009](../research/RES-009-failure-mode-testing.md) must cover it.
- Metadata that describes actions is not a security model. Authorization by target and action stays in the coordinator API; a provider advertising `power_off` does not make `power_off` permitted.
- Provider metadata is versioned, and an operator surface that encounters an unknown provider or version must degrade to raw normalized fields rather than fail.

## Alternatives considered

**Modeling each device type directly in coordinator and UI code** was rejected because it makes every new piece of show equipment a full-stack change, which is the wrong cost curve for a project whose users own arbitrary hardware.

**Modeling controlled devices as nodes** was rejected for the reasons in the context: no agent, no advertisement, no fallback, no Last Will, and different health semantics. Forcing them into the node model would weaken the guarantees the node model provides.

**Delegating device control to Home Assistant** was rejected as a requirement — it would put a large external dependency in the path of projector control and make ShowMesh's own state model derivative of another system's. It remains attractive as a *provider*: Home Assistant is already a candidate outbound alert destination (OBSERVABILITY §11.5), and a Home Assistant provider would let operators reuse existing integrations without ShowMesh depending on it.

**Deferring the whole model until projector control is built** was rejected because the read-only status API and the first Operator UI would then be shaped without it, and retrofitting a resource class into a published API contract is the expensive version of this decision.

## Related research

[Device telemetry adapters](../research/RES-012-device-telemetry-adapters.md) · [Control-provider model](../research/RES-014-control-provider-model.md) · [Configuration model](../research/RES-008-configuration-model.md)

RES-014 must validate the metadata shape and the generated-surface hypothesis before the pattern is applied beyond the first provider. A negative result narrows the decision to a provider interface with hand-written surfaces; it does not restore device-specific coordinator code.
