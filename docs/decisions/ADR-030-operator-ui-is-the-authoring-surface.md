# ADR-030: The Operator UI Becomes the Authoring Surface, Without Becoming the System

Status: Accepted  
Date: 2026-08-13

## Context

The Operator UI has been an observation surface with, since Step 7, three narrow write controls. [ADR-027](ADR-027-show-and-surface-model.md), [ADR-028](ADR-028-show-asset-store-and-identity.md), and [ADR-029](ADR-029-logical-actions-and-integration-bindings.md) change that: shows, surfaces, model mappings, node assignments, output configuration, asset upload, action bindings, and macro composition all need somewhere to be authored, and the browser is where an operator will do it.

That is a large expansion, and it puts pressure on the constraint the UI was built under. [ADR-014](ADR-014-operator-ui-is-an-api-client.md) says the UI is **one client, not the system**, with the test that if every browser vanished the show continues correctly. Observation made that easy to honour: nothing important lived in the browser because the browser only read. Authoring makes it hard, because authoring is where a UI naturally accumulates behaviour the API never gets.

The failure mode is specific and this project has already had it. Step 6 shipped three features that compiled, passed tests, and could not be reached by anything, and each was found by someone trying to use the system. Authoring built UI-first produces the mirror image: capabilities reachable **only** through the browser, at which point the public contract has quietly stopped being public and nobody decided that.

## Decision

### 1. Every authoring capability exists in the API first, and the CLI can drive it

No authoring capability ships in the UI without the API endpoint it calls, and `showmeshctl` must be able to perform the same operation.

The CLI half is the part that will feel like overhead, and it is the part that works. `showmeshctl` is forbidden by an enforced import-graph test from importing any coordinator package, so it cannot share the server's types and quietly agree with the server about a field. That property has already caught contract defects in Step 3 that a shared-type client would have missed entirely.

**The ADR-014 test, restated for authoring:** if every browser disappeared, could an operator still configure a show from scratch? The answer must be yes.

**And the owner's reason is stronger than the contract one, so it replaces it as the primary justification.** The CLI is the *"oh no, we have to fix the show and the UI is down"* tool. That is not contract hygiene, it is the emergency path, and it changes what "the API supports it" has to mean:

- **The CLI is fully tested, not merely present.** A path that exists and has never been exercised is not an emergency path; it is an untested assumption that will be discovered under the worst possible conditions. Every authoring capability's CLI verb is covered, and covered against a running coordinator rather than only in a unit test.
- **It has to be usable by a stressed person at night.** Legible errors, no interactive-only flows, no dependence on remembering an opaque identifier. Ordinary CLI usability, which stops being ordinary when it is the thing standing between an operator and a dark house.
- **It must work when the UI container is down**, which follows from [ADR-022](ADR-022-operator-ui-serves-the-api-same-origin.md): the UI proxies the API, so an operator whose UI is gone needs to reach the coordinator directly. That path is already required to work and this makes it load-bearing rather than theoretical.

The contract argument still holds and is why `showmeshctl` cannot import a coordinator package. It is now the second reason rather than the first.

### 1a. Parity runs both ways: the UI is a full control surface

Added 2026-08-13 on the owner's instruction, and it is the mirror of decision 1 rather than a restatement.

Decision 1 stops the **UI outrunning the API**. This stops the **CLI outrunning the UI**. Anything an operator can do with `showmeshctl` they can also do in the browser, allowing for the small set of things that genuinely cannot be mirrored, such as streaming raw output or piping to another tool. The UI is a full control surface, not a curated subset of one.

The relationship is therefore `API ⊇ CLI ≡ UI`: the API may carry more than either client exposes, and the two clients stay level with each other.

Why it matters enough to write down: a CLI-only capability is as much a private contract as a UI-only one, just with a different audience, and it pushes the operator to a terminal for something they are doing at a laptop with a mouse. The failure is quieter than the UI-first one because the capability is technically reachable, so nobody notices the UI has become the easy 80% of a system whose remaining 20% requires a different tool and a different mental model.

**Visual design is explicitly not settled by this record**, and is deferred to a dedicated design session. What is settled is the structural rule above and the constraints below. The information architecture that falls out of Track E's screens is a design problem to be solved then, not now.

### 2. The UI holds no authoring logic

Validation rules, defaults, name resolution, channel-range derivation, and binding checks live server-side. The UI may **mirror** a validation to give immediate feedback, and the server remains the only place a rule is enforced.

This is the same rule ADR-024 decision 4 already set for authorization, where enforcement is a single check at the coordinator's API boundary and never at the UI or the proxy. Authoring rules follow it for the same reason: a rule implemented in the browser is a rule that does not apply to `curl`.

### 3. Manual configuration is a first-class path in the interface

[ADR-027](ADR-027-show-and-surface-model.md) decision 4 makes manual channel-range configuration permanently supported and, until FPP Connect compatibility lands, **the only path that exists.**

So the UI must not present it as a degraded mode. Named-model selection is preferred **when xLights metadata is present**, and where it is absent the manual path is the normal path, presented without apology. A design that treats it as a fallback would ship day-0 on an interface built to discourage the only workflow available.

### 4. The interface exposes logical concepts; raw protocol is deliberately inconvenient

Operators bind a `Countdown` action to a clip. They do not type OSC addresses. Raw protocol entry exists per [ADR-029](ADR-029-logical-actions-and-integration-bindings.md) decision 3 and is presented as explicitly advanced, never as the first or easiest way to build a step.

### 5. The UI moves bytes now, and must be honest while doing it

Asset upload is new: this UI has never transferred a file. Uploads are large, slow, and failure-prone in ways a JSON POST is not.

Three requirements follow from rules this project already holds. **Progress and failure are stated, never inferred**, because an upload that silently stalls is indistinguishable from one that is working. **A partial or failed upload never registers an asset**, since a half-written FSEQ that resolves as present is worse than one that is absent. And **target selection is mandatory rather than defaulted** for node-specific assets, because ADR-028 decision 1 makes the target part of the asset's identity, and a defaulted target produces a confidently mislabelled artifact.

### 6. The UI stays optional, and stays out of the show

Unchanged from ADR-014 and restated because authoring makes it tempting to forget: the UI ships as its own container, holds no credential per [ADR-022](ADR-022-operator-ui-serves-the-api-same-origin.md), holds no orchestration behaviour, and can be removed entirely with the show unaffected. Authoring is configuration, and configuration is not the show.

## Consequences

- **Every authoring feature costs an API endpoint, a CLI verb, and an OpenAPI entry.** That is the ADR-014 tax, paid deliberately, and it is the single largest cost in this record.
- Asset upload gives the API its first large-body endpoint, which brings size limits, timeouts, and resumability questions that a JSON API never had.
- The UI's surface grows enough that its own information architecture becomes a real design problem rather than a layout one. The whole-UI styling pass, deferred since Step 4, now has authoring screens waiting behind it.
- Mirrored client-side validation can drift from the server's. Where they disagree, the server is right, and the mirror is a convenience with no authority.

## Alternatives considered

**Authoring in the UI, with the API catching up later.** Fastest to build and it is what happens by default. Rejected because "later" is where ADR-014 goes to die: once the UI is the only way to configure a show, the contract is private and the decision was never made by anyone.

**Configuration files instead of an authoring UI.** Rejected by [RES-008](../research/RES-008-configuration-model.md) decision D1's reasoning, which is not about tidiness: a non-technical operator cannot be asked to edit a file, and "it is only a file" is no defence when the person changing it is not the person who deployed it.

**A separate authoring application.** Rejected: two clients, two contracts, twice the drift, and no benefit while there is one operator.

## Related research

[Configuration model](../research/RES-008-configuration-model.md) · [xLights and FPP Connect](../research/RES-003-xlights-fpp-connect-compatibility.md)

## Supersession

Supersedes nothing. It extends [ADR-014](ADR-014-operator-ui-is-an-api-client.md) and [ADR-015](ADR-015-typescript-spa-frontend.md) to a role neither anticipated, and every constraint in both remains in force.
