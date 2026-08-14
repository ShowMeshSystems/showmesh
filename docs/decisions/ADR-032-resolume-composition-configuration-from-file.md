# ADR-032: Resolume Composition Configuration Comes From the Composition File, Not the API

Status: Accepted
Date: 2026-08-14

Bench evidence: [docs/bench/resolume-control-surface.md](../bench/resolume-control-surface.md) §14, §15, §16.
Related: [ADR-027](ADR-027-show-and-surface-model.md) (xLights is an authoring-time dependency, never a runtime one), [ADR-029](ADR-029-logical-actions-and-integration-bindings.md) (the adapter owns the protocol), [ADR-030](ADR-030-operator-ui-is-the-authoring-surface.md) (API first, `showmeshctl` must drive it), [ADR-003](ADR-003-desired-and-observed-state.md), [ADR-011](ADR-011-context-aware-observability.md).

## Context

Every Resolume object ShowMesh needs to name — a clip to launch, a layer to check for readiness, a deck to select — is identified by a Resolume object id. Those ids are stable across restarts, edits, and a year-over-year show rebuild, which is what made them safe to store as configuration.

Discovering them was the problem. Resolume exposes **no collection endpoints**: `/composition/layers`, `/composition/columns`, `/composition/decks` and `/composition/layergroups` all return 404. The only way to learn what exists is `GET /composition`, the full 2.26 MB read.

**That call crashes Arena 7.23.2.** Seven `SIGSEGV`s with byte-identical faulting frames, one reproduced from `curl` with no ShowMesh process running, which is what establishes it as a fact about Arena rather than a defect in the adapter. It is neither a request count nor an elapsed time: 2 reads, 9 reads and 2,046 reads all ended the same way. Controls that did not crash it were 7 minutes idle, 30 `/product` polls, and a WebSocket held open for 5 minutes.

Seam D-1 bounded the call to a connect plus a 120-second convergence window. That reduced exposure and did not eliminate the crash: the seventh crash came 26 seconds after the **second** composition read, in a run where the bounded adapter did exactly what it was designed to do.

Two further measurements decided the shape of the fix.

**Targeted reads are safe.** 127,128 clip `by-id` reads and 82,788 layer `by-id` reads, 209,916 requests and 6.5 GB over ten minutes, no crash. The layer probe alone moved more bytes than the back-to-back full-read run moved before crashing. So the hazard is one endpoint, not reading Resolume.

**The composition file holds the id map.** `.avc` is plain XML. Parsing the operator's `Christmas 25.avc` yields every clip, layer, layergroup, column and deck id, with names, positions, group membership, per-clip source media path and `TransportType`. It is 407 KB against 2.26 MB, and it carries two things the API does not expose at all: the composition canvas size, and the `versionInfo` of the Arena that wrote it. It also carries **all three decks' clip grids**, where `/composition` returns only the selected deck's.

Verified against the live Arena using `by-id` reads only: 18 of 18 layer ids and 30 of 30 non-empty selected-deck clip ids resolve.

Resolume 7.26 carries "#25086 REST-API Overhead on large compositions" alongside a broader API overhaul, and the owner acquires 7.26 after Thanksgiving. That is a performance note rather than a stated crash fix, and it arrives roughly six weeks after the Halloween show. It is not a plan.

## Decision

### 1. The composition id map is configuration, sourced from the `.avc` file

The operator uploads a composition file. ShowMesh parses it, extracts the id map, and stores it as a configuration object with the existing revision and audit semantics. Every ShowMesh reference to a Resolume object resolves through that stored map.

### 2. No runtime path may call `GET /composition`

Not on connect, not on a timer, not on a change signal, not to verify anything. The seam D-1 convergence window is removed rather than tuned, because the file makes the call unnecessary rather than merely expensive.

This is deliberately stronger than "call it rarely". A bound that still permits the call leaves a crash on the show's critical path, and the two-read crash measured that the bound does not make it safe.

### 3. Live state is read by id, never by enumeration

`/composition/clips/by-id/{id}`, `/composition/layers/by-id/{id}` and their siblings are the observation path, and `/product` remains the reachability probe. Both were exercised as controls and neither crashed Arena.

### 4. The file is an authoring-time dependency and never a runtime one

This is [ADR-027](ADR-027-show-and-surface-model.md)'s xLights rule applied to a second vendor, for the same reason. No node and no runtime path parses a `.avc`. The coordinator parses one when the operator uploads it, and nothing downstream ever sees the file again, only the stored id map.

ShowMesh **never reads the file from the Resolume host's disk**, and never writes a `.avc`. Upload is the only ingestion path. A composition file is show content the operator owns, and reaching into the render host's filesystem would make ShowMesh's configuration depend on a machine being reachable and a path being stable, which is the runtime dependency this decision exists to remove.

### 5. Composition identity is asserted by resolving stored clip ids

[The adapter specification](../build/TRACK-D-ADAPTER-SPEC.md) §3.8 requires composition identity to be asserted structurally rather than by name, and left the check expensive because it implied enumeration. It is now cheap: take a sample of the uploaded composition's clip ids and resolve them by id.

The sample must be drawn from clips of **one deck**, plus persistent clips, for the reason in decision 6.

### 6. A `404` on a stored clip id does not by itself mean a stale reference

Measured: a clip id resolves only while its own deck is selected. 30 of 30 selected-deck ids resolved; 0 of 10 non-selected-deck ids did. Layer ids are genuinely deck-independent.

So the specification's §6.4 rule — a `404` aborts the action and marks the composition unidentified — is **wrong as written** for clips, because it cannot distinguish "this clip was replaced" from "this clip's deck is not showing". Every stored clip reference carries its deck, and a `404` on a clip whose deck is not currently selected is reported as a **deck mismatch with the deck named**, never as a stale reference and never as an identity failure.

`PersistentClips` are the exception and are modelled as such: they live outside any deck and resolve regardless of selection.

### 7. The upload states what it found, and a rejected file changes nothing

Per [ADR-030](ADR-030-operator-ui-is-the-authoring-surface.md): the capability exists in the API first, `showmeshctl` must be able to drive it, validation is server-side, and a partial or failed upload registers nothing. The parse result is reported as counts and names the operator can recognise, not as a success flag.

The stored map records the `versionInfo` of the Arena that wrote the file, because the format is undocumented and a version mismatch is the first thing to suspect when a parse looks wrong.

### 8. The Operator UI ships a working upload control, not a documented intention

Owner's instruction, 2026-08-14. Uploading the composition is the **only** way configuration enters this subsystem, so a capability with no reachable control is a subsystem the operator cannot use. That is the Step 6 lesson in a new place: three features shipped there that compiled, passed tests, and could not be reached by anything, and every one was found by someone trying to use the system rather than by a test.

The control obeys ADR-030's upload rules: it states progress and failure rather than inferring them, a partial or rejected upload registers nothing, and the result is rendered as what was parsed — composition name, deck names, layer and clip counts, and the Arena version that wrote the file — so the operator can recognise their own show rather than read a green tick.

The UI holds no parsing and no validation logic. It posts the file and renders what the server reports.

## Consequences

**The crash leaves ShowMesh's critical path.** The only remaining way to reach it is an operator or another tool calling `/composition` by hand.

**Configuration covers more than the API could.** All decks rather than the selected one, plus canvas size and per-clip `TransportType`, which is what the SMPTE drift check in the specification's §8 needed and did not have.

**The file can go stale, and that is the accepted cost.** Arena never writes the `.avc` on exit and discards in-session changes, so an operator who edits live without saving leaves the stored map behind. The owner's assessment, 2026-08-14: once a composition is built, that is essentially it; timing changes are either a source video file overwritten in place, which moves no id, or trigger timing, which is ShowMesh's own configuration. The realistic failure is forgetting to re-upload while still building the show, and decision 5's check catches it.

**A composition swap without an Arena restart no longer has a detection story at all**, and previously had a poor one. It is an open item rather than a solved problem.

**The `.avc` format is undocumented and this decision depends on it.** Everything parsed was written by Arena 7.23.2 or earlier. 7.26's API overhaul may or may not touch the file format. Decision 7's recorded `versionInfo` is the tripwire, not a guarantee.

**This decision is reversible in the direction the owner wants.** If 7.26 fixes the crash, API enumeration returns as an additional ingestion path feeding the same stored id map, and nothing downstream changes. The owner's stated intent, 2026-08-14, is to revisit for the Christmas show after upgrading. Decisions 3, 5 and 6 hold either way, because they are about how live state is read rather than about where configuration comes from.

## Alternatives considered

**Keep enumerating over the API, bounded.** This is what seam D-1 shipped, and the measurement rejected it: two reads crashed Arena. A bound that still permits the call leaves a segfault on the day-0 critical path for one of the three founding problems.

**Wait for 7.26.** It arrives about six weeks after the Halloween show, and "#25086 REST-API Overhead on large compositions" is a performance note, not a stated crash fix. Designing on it would be designing on a hope with the wrong delivery date.

**Read the `.avc` from the Resolume host over SMB or a share.** Rejected by decision 4. It reintroduces a runtime dependency on a remote path and a reachable host, which is most of what this decision removes, and it makes ShowMesh's configuration silently follow an artifact the operator did not choose to publish.

**Have the operator type clip ids in by hand.** This is the honest fallback and it does work, since ids are stable. It is rejected as the primary path because a 13-digit integer transcribed by hand at 17:00 is a defect waiting to happen, and the file already holds every one of them alongside the names the operator recognises. Manual entry remains available, per ADR-027's rule that manual configuration is a permanent first-class path rather than an escape hatch.
