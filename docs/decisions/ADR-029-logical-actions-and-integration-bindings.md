# ADR-029: Macros Invoke Logical Actions, Never Protocol Commands

Status: Accepted  
Date: 2026-08-13

## Context

Day-0 has ShowMesh driving Resolume over OSC, publishing MQTT to Node-RED for projector power, and commanding FPP over its own API. Macros sequence all three.

The easy implementation puts the protocol in the macro. A macro step holds an OSC address, or an MQTT topic and payload, or an FPP command name, and the executor sends it. It works immediately and it is what a first pass naturally produces.

It is also how the configuration becomes unmaintainable in one season. An OSC address embedded in six macros has to be corrected in six places when the composition changes, and nothing relates those six occurrences to each other or tells the operator which ones they missed.

## Decision

### 1. A macro step invokes a named logical action

Operators define logical actions such as `Countdown`, `Resting Visual`, `Pre-Show Visual`, and `Blackout`. Each action binds to an integration target:

```
Action: Countdown
Target:
  integration: Resolume
  clip: <pinned clip reference>
```

Macros and UI controls invoke the **action**. The runtime path is:

```
logical action → integration adapter → protocol command → external system
```

**No ordinary macro definition contains an OSC address, an MQTT topic, or a raw protocol path.** When the composition changes, one binding changes and every macro referencing it is correct.

### 2. The adapter owns the protocol, including its confirmation

Translating an action into a protocol operation is the adapter's job, and so is knowing how that protocol reports success. This matters because the protocols disagree about whether they report anything at all: OSC is fire-and-forget UDP with no reply, FPP answers over HTTP, and an MQTT publish is answered only if something chooses to answer.

[ADR-003](ADR-003-desired-and-observed-state.md) requires evidence that observed state moved, and that evidence is found in different places per integration. The Resolume adapter acts over OSC and confirms over REST. Keeping that knowledge in the adapter is what stops a macro executor from growing per-protocol special cases.

### 3. Raw protocol access exists, and never inside an ordinary macro

An advanced escape hatch for sending a raw protocol command is supported, because an operator will eventually need something the action vocabulary does not cover, and being unable to do it at all is worse than doing it bluntly.

It is deliberately kept out of the ordinary path: available as an explicitly advanced operation, never the default way to build a macro step, and never what the UI offers first. **The named-action layer is only worth having if the raw layer is inconvenient enough that nobody reaches for it by habit.**

### 4. An action whose effect cannot be observed says so

Actions differ in how confirmable they are, and the difference must be visible rather than averaged away.

The external MQTT command step is the sharp case: it carries an operator-declared response contract, so ShowMesh confirms against a response Node-RED sends when the projector actually comes on. Configured with no expected response, it is genuinely unconfirmable and **reports as unconfirmable with a reason** from [ADR-020](ADR-020-control-api-shape-and-change-stream.md)'s vocabulary, never as success.

The rule generalises past MQTT: **a macro step that always reports success is worse than no step**, because the operator learns to ignore it, and then it is silently useless on the night it matters.

## Consequences

- Every integration needs an action vocabulary before macros can use it, which is a small design step per integration rather than an afterthought.
- **An action bound to something that no longer exists is now possible**, such as a Resolume clip deleted from the composition. That is a readiness concern: bindings should be validated against the integration's actual state before a show, not discovered at showtime.
- The indirection costs a lookup and one more concept for the operator to learn. Both are cheap against correcting an address in six macros.
- Since actions are configuration objects under [ADR-027](ADR-027-show-and-surface-model.md), they are revisioned and audited, so a changed binding has a history.

## Alternatives considered

**Protocol commands directly in macro steps.** Rejected above. It is faster to build and it fails on the first composition change.

**Actions with no raw escape hatch.** Rejected: the vocabulary will be incomplete, and an operator who cannot express what they need will work around ShowMesh entirely, which is worse than an ugly step.

**Per-integration macro step types instead of a shared action concept.** Rejected because it pushes protocol knowledge back into the macro executor, which is what decision 2 exists to prevent.

## Related research

[Resolume SMPTE behavior](../research/RES-001-resolume-smpte-behavior.md) · [Control-provider model](../research/RES-014-control-provider-model.md) · [Configuration model](../research/RES-008-configuration-model.md)

## Supersession

Supersedes nothing. It is the general form of a rule [ADR-016](ADR-016-controlled-devices-and-control-providers.md) already applied to controlled devices, extended to every integration a macro can reach.
