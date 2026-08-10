# RES-014: Control-Provider Model and Metadata-Driven Operator Surfaces

[Architecture](../architecture/ARCHITECTURE.md#410-controlled-devices-and-control-providers) · [Operator UI](../architecture/OPERATOR-UI.md#91-provider-driven-configuration-and-control) · [Tracker](README.md) · [ADR-016](../decisions/ADR-016-controlled-devices-and-control-providers.md) · [Device telemetry adapters](RES-012-device-telemetry-adapters.md)

Status: unresearched · Risk: medium · Verification: L0 — assumption

## Decision to make

Define the control-provider metadata contract: what a provider declares about its configuration, actions, and telemetry, and whether that declaration is sufficient to generate usable operator configuration and control surfaces without device-specific frontend code.

[ADR-016](../decisions/ADR-016-controlled-devices-and-control-providers.md) commits to the provider abstraction. It does not commit to the metadata shape, and the generated-surface claim in [OPERATOR-UI §9.1](../architecture/OPERATOR-UI.md#91-provider-driven-configuration-and-control) is a hypothesis, not a result. Self-describing driver metadata driving generated forms is a pattern with a long history of collapsing back into per-device components once real devices arrive; whether it survives contact with PJLink projectors, relays, and a vendor HTTP API is exactly what this record exists to find out.

## Questions

- What must a provider declare so that a configuration form can be generated: field types, units, defaults, validation, secrets, conditional fields, and grouping? Which of those are actually needed by the first three providers rather than anticipated?
- Can action declarations carry the information an operator surface needs — parameters, preconditions, expected effect, confirmation weight, expected confirmation latency, and what evidence counts as success?
- How does a provider express that an action's effect cannot be confirmed, and how does that render without looking like either health or failure?
- How do provider metadata versions evolve without breaking existing device definitions or an older UI?
- Where is the boundary past which a generated surface becomes worse than a hand-written one, and can that boundary be described in advance rather than discovered per device?
- How do provider actions map onto the ARCHITECTURE §8.1 command envelope, including idempotency keys and deadlines, for devices that are not idempotent in practice?
- How do node-hosted providers (serial, relay, GPIO) advertise themselves through the ADR-002 capability vocabulary, and what does a device definition reference — the node, the capability, or both?
- What happens to a controlled device's desired state during coordinator loss, given that the device holds no fallback of its own?

## Acceptance criteria

- Three providers of genuinely different shape are implemented against one interface without changing it: a network protocol provider (PJLink), a node-hosted physical provider (relay or serial), and a vendor HTTP API provider.
- Each provider's configuration and control surface is generated from metadata alone, or the specific reason it could not be is recorded.
- Adding a fourth provider requires no frontend change, or the required change is documented as a bounded and expected category rather than an ad-hoc fix.
- Unknown provider identifiers and unknown metadata versions degrade to raw normalized fields in the operator surface without failing the view.
- Observed state for controlled devices reconciles with the RES-012 normalized field sets rather than duplicating them.
- A device with no status query renders as `unknown` carrying a provenance reason that distinguishes structurally unavailable confirmation from stale or failed collection, per ADR-016 and OBSERVABILITY §4.2.

## Test matrix and method

Implement the projector provider first against real deployed hardware, since RES-012 records the operator's confirmation that all deployed projectors support PJLink — an operator assertion, not a source-verified or bench claim, and RES-012's own bench item 1 is probing each model's PJLink class. Build the generated surface from its metadata, then add the second and third providers and record every change the interface or the metadata schema required. The count and character of those changes is the result; a stable interface across three dissimilar providers is the evidence, and interface churn is the negative finding.

Exercise: device unreachable at definition time, credentials wrong, action accepted but ineffective, action with no confirmation mechanism, slow confirmation crossing the command deadline, provider version older than the UI expects and newer than the UI expects, and coordinator restart with a device mid-transition.

## Evidence and findings

None. This record is a work queue, not a conclusion.

## Decision, fallback, and revalidation

Fallback if the generated-surface hypothesis fails: the provider interface and the controlled-device resource class survive unchanged, and operator surfaces become hand-written per provider with metadata retained as the declaration of what a device supports. That is a narrower outcome, not a reversal of ADR-016 — device-specific coordinator code does not return.

Revalidate when a provider is added whose shape differs materially from the first three, or when the command envelope or capability vocabulary changes.
