# ADR-027: The Show and Surface Model, and Who Owns Authoring

Status: Accepted  
Date: 2026-08-13

## Context

Through Step 7 this project had no concept of a show. It had FPP instances, nodes, observations, and one configuration object holding a list of endpoints. Everything an operator would recognise as *their show* lived in xLights, in FPP's scheduler, and in Resolume's composition.

Day-0 changes that. ShowMesh drives projection, audio, and Resolume, which means something has to say which surface renders which channels, which node hosts it, which clip a countdown maps to, and which of those change between Halloween and Christmas. None of that exists.

The risk is obvious and worth naming before the decisions: **the easy version of this is ShowMesh growing a sequence editor.** It must not. xLights is a mature timeline editor the operator already knows, and reimplementing it would be both enormous and worse.

## Decision

### 1. xLights owns timeline authoring; ShowMesh owns system configuration

The split, stated so it can be checked against later:

- **xLights** owns sequence and timeline content. Creative authoring happens there.
- **FPP** stays the authoritative scheduler and sequence playback authority, per [ADR-001](ADR-001-fpp-is-authoritative.md).
- **ShowMesh** owns system configuration: surfaces, logical actions, macros, asset storage and distribution, orchestration, and runtime state.
- **Resolume** and other external systems are execution targets driven through adapters.

ShowMesh is the layer that describes **how authored content maps onto the physical and logical system**, and nothing more. If a proposed feature would let an operator change what the show *looks like* rather than where it *runs*, it belongs in xLights.

### 2. A Show is a namespace, not a container

A `Show` is a first-class object grouping everything needed to operate one authored show: surfaces, actions, macros, asset requirements, node assignments, and model mappings.

**Surfaces, actions, and macros are their own configuration objects carrying a show reference. They are not nested inside a Show's payload.** Both shapes were considered and the namespace wins on revision history: [ADR-009](ADR-009-sqlite-configuration-storage.md) makes configuration revisions immutable, and with a container shape, editing one clip binding creates a new revision of the entire show. A history where every entry is "the show changed" cannot answer what actually changed, which is the only question a revision history exists to answer.

This costs a reference on each object and buys granular, meaningful revisions. It also needs no schema work: configuration is already keyed `(kind, id)` with a JSON payload, so each of these is a new `kind`.

### 3. The active show is configuration, not runtime state

Exactly one show is active at a time, and which one is a **configuration object**, revisioned and audited like any other.

It was tempting to model this as runtime state, because it drives runtime behaviour. It is recorded as configuration because switching from Halloween to Christmas is an infrequent, deliberate operator decision with large consequences, and [ADR-024](ADR-024-identity-authorization-and-audit.md) decision 11 wants exactly that kind of state change attributed and auditable. Runtime state carries no audit trail and no history.

**The concrete reason, in the owner's words, is better than the architectural one:** it stops you accidentally breaking Halloween because you were programming Christmas. Two shows exist at once in November, one of them is running for an audience, and the other is being edited. An explicit, audited, revisioned pointer between them is what keeps an edit from reaching the wrong one. This is the case the decision exists for, and it is six weeks after day-0 rather than hypothetical.

**Switching the active show changes what every node is required to hold.** That must surface as readiness evidence, showing nodes missing assets for the newly active show, rather than appearing to succeed and failing at showtime. Absence is stated, never omitted, which is the same rule the observation model already follows.

### 4. Surfaces are mapped from named xLights models, with manual channel ranges permanently supported

A `Surface` is the logical canvas defined in [ADR-026](ADR-026-renderer-surface-model-and-reference-transport.md) decision 1. It carries a name, its source mapping, pixel dimensions, channel layout, its assigned render node, and its output configuration.

**The preferred mapping is `xLights model → ShowMesh surface`, not `FSEQ channels 184321-512320 → surface`.** Named mapping survives ordinary channel-layout changes; a hardcoded range silently renders the wrong thing after any reorder upstream, which is the same class of defect as addressing a Resolume clip by position rather than by pin.

**Manual channel-range configuration is a permanent supported feature, not an escape hatch.** Three reasons, and the first is the one that matters most right now:

- Named-model mapping depends on xLights metadata reaching ShowMesh, which needs the FPP Connect compatibility work. Until that lands, **manual configuration is the only path that exists**, so treating it as a fallback would mean shipping day-0 on a path the design considers second-class.
- ShowMesh must remain usable with sequence-generation systems other than xLights, and requiring xLights would contradict this record's own non-goals.
- It is the recovery path when automatic discovery is wrong, which is the case where an operator most needs to be able to fix something.

The UI should prefer named-model selection **whenever xLights metadata is available**, and must not degrade the manual path to make that preference visible.

### 5. xLights is an authoring-time dependency and never a runtime one

Authoring configuration is resolved into runtime state before or during deployment. **A running node never parses an xLights project, and never needs xLights reachable.** Model metadata is translated into ShowMesh-native surface configuration at the authoring boundary.

This is the same property the whole architecture rests on: a running show survives the loss of everything that is not in its path, and adding a runtime dependency on a desktop application would be the largest violation of that principle available.

## Consequences

- **Every authoring capability must exist in the API before or alongside the UI**, per [ADR-014](ADR-014-operator-ui-is-an-api-client.md). Configuration is now a large surface, and building it UI-first is how the API quietly stops being usable on its own. See [ADR-030](ADR-030-operator-ui-is-the-authoring-surface.md).
- The Show namespace means **an orphan is possible**: a surface referencing a deleted show. Deletion needs to state what it will orphan rather than cascade silently.
- Manual configuration being first-class means **two mapping paths must be maintained**, and the named-model path must degrade to the manual one rather than failing when metadata is absent.
- Multiple shows accumulate across seasons, so assets and configuration both need a retention answer eventually. Not day-0.

## Alternatives considered

**A Show as a single container object.** Rejected per decision 2, on revision history.

**No Show object at all, with surfaces and macros as a flat global set.** Genuinely viable for one season and briefly attractive for that reason. Rejected because the second show is November, not next year, and retrofitting a namespace onto objects that already exist means migrating live configuration during the run-up to Christmas.

**Requiring xLights.** Rejected: it contradicts the non-goals, and it would make ShowMesh unusable for anyone with a different authoring tool for no architectural gain.

**Deriving surfaces automatically with no manual path.** Rejected because it would make day-0 depend on FPP Connect compatibility work whose scope is still unknown.

## Related research

[Configuration model](../research/RES-008-configuration-model.md) · [xLights and FPP Connect](../research/RES-003-xlights-fpp-connect-compatibility.md) · [Renderer performance](../research/RES-004-virtual-matrix-renderer-performance.md)

## Supersession

This record supersedes nothing. It builds on [ADR-026](ADR-026-renderer-surface-model-and-reference-transport.md), which fixed the renderer's surface model, by adding the configuration layer that describes surfaces to the system.
