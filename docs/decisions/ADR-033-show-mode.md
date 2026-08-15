# ADR-033: Program Mode and Show Mode are One System-Wide Operating Mode, Not a Per-Subsystem Flag

Status: Accepted
Date: 2026-08-14

Related: [ADR-004](ADR-004-primitives-macros-local-fallback.md) (reduced local fallback),
[ADR-011](ADR-011-context-aware-observability.md) (lifecycle context changes alert meaning),
[ADR-016](ADR-016-controlled-devices-and-control-providers.md) (controlled devices hold no fallback),
[ADR-024](ADR-024-identity-authorization-and-audit.md) (writes need a principal; degradation must not cost control),
[ADR-027](ADR-027-show-and-surface-model.md) (the active show is configuration),
[ADR-032](ADR-032-resolume-composition-configuration-from-file.md) (the case that raised this).

## Context

**Owner's decision, 2026-08-14, from his own experience of the product category rather than from this project's evidence.** The immediate question was narrow: should ShowMesh hold a WebSocket open to Resolume by default, given that the WebSocket is the leading remaining suspect in a crash whose mechanism is a use-after-free in Arena's HTTP response serialiser?

The answer he gave rejected the framing:

> Off for the show and on for setup is the industry correct way to do this. 99% of these show control systems have a "show mode" that makes it safer to run, less edit surface, smoother automations. The whole system should have this feature, not just Resolume. What we build for this toggle should be reusable across the entire app.

That is a statement about a category of system, and it is one this architecture had already half-derived without naming. ADR-011 says "lifecycle/maintenance context changes alert meaning" and never says what supplies the context. ADR-004's reduced local fallback is a behaviour change conditioned on circumstance. The Resolume adapter was about to grow a private boolean that means "we are running a show now," and the FPP collector's poll cadence, the audio engine's device-loss policy and the asset sync timer each have a version of the same condition waiting to be invented separately.

**Three subsystems inventing the same flag independently is how a system ends up unable to answer "are we in show mode" with one value.** This ADR names it once.

## Decision

### 1. Show mode is one installation-wide value, and it is configuration

There is exactly **one** mode value for the installation, held as configuration with the revision and audit semantics ADR-024 and ADR-027 already define for the active show. It is not per-node, not per-device, and not per-subsystem.

The vocabulary is a closed enum, and a Go zero value that fails the build rather than defaulting to a mode nobody chose. The initial members are **`program`** and **`show`**. Additional members require an amendment to this record, because a mode is a contract every subsystem reads.

**The wire value and the operator-facing label are the same word, deliberately.** Owner's naming, 2026-08-14: the modes are **Program Mode** and **Show Mode**. The obvious alternative, an internal `setup` with a "Program" label on top, was rejected before it could exist: two names for one state is a drift the API contract cannot enforce, it makes every log line and every support conversation require a translation step, and ADR-014 already treats the API as a public contract rather than a UI convenience. Nothing had been built yet, so this cost nothing.

**Changing mode is an auditable action requiring a principal**, like any other write.

### 2. Subsystems declare mode-dependent behaviour; they never each define the mode

A subsystem may read the mode and change what it does. It may **not** hold its own notion of whether a show is running, derive one from playback state, or accept a mode as a parameter on an unrelated call.

The Resolume adapter's WebSocket is the first consumer: held open in `program`, closed in `show`. The mechanism is TRACK-D-D2-SPEC.md §3.3's runtime switch, and this ADR is what drives it.

**The reusable part is the mode and the way behaviour is bound to it, not a boolean threaded through call signatures.**

### 3. The operator always knows which mode they are in

A mode that is not visible is a trap, because every surface behaves differently and nothing says why. The mode appears on the Operator UI persistently, not on a settings page, and `showmeshctl` reports it. A refusal or a degraded behaviour caused by the mode **states the mode as its reason**.

### 4. Mode never gates stopping the show

**This is the non-negotiable clause and it is ADR-024 decision 7's lesson in a new place.** No mode may refuse, delay, or degrade blackout, stop, or power-off. A mode exists to reduce the ways an operator can break a running show; an operator who needs to stop the show is not breaking it.

The failure this forbids is concrete and would read as reasonable while being written: "in show mode, configuration writes are refused" quietly covering a command path, so the one moment the operator most needs control is the moment the system withholds it. Every degradation in this architecture points at the show continuing. Show mode must point at the operator staying in control, and those are different arguments.

### 5. Mode is held through coordinator loss, never re-derived

A node or adapter that cannot reach the coordinator **keeps the last mode it knew** and says the value is held rather than current. It never falls back to a default.

Reverting to `program` because the coordinator went away would turn a coordinator outage into a live behaviour change mid-show, which is precisely the class of failure ADR-004 and ADR-008 exist to prevent. The mode is part of the ADR-025 signed fallback cache for the same reason the rest of it is.

**A mode that cannot be read is `unknown`, and `unknown` behaves as `show`.** Show is the conservative side: smaller footprint, fewer edit surfaces, and per decision 4 nothing safety-critical is withheld by it.

### 6. Show mode is not a lock, an authorization scope, or a schedule

Three things it is deliberately not, because each is a plausible next step that would make it worse:

- **Not authorization.** ADR-024 owns who may act. Mode changes what the system does, never who is permitted to do it. A subsystem must never consult mode in place of a scope check.
- **Not a lock.** It does not make configuration immutable. It may make an edit louder, slower, or warned. It never makes it impossible, because an operator fixing something at 17:00 is the case this system exists for.
- **Not a scheduler.** FPP is the authoritative scheduler (ADR-001). Mode does not change on a clock, and nothing derives it from a playlist running. An operator sets it.

## Consequences

**One value answers "are we in show mode", and every subsystem reads the same one.** That was the point.

**Show mode is a real deliverable with its own surface**, and it is not built by the seam that first needs it. D-2 ships the Resolume WebSocket as a runtime switch shaped so a mode can drive it later, and does not build the mode. The mode's own build is scheduled separately.

**This grows the test matrix.** Every mode-dependent behaviour is two behaviours, and the interesting bugs live in the transition rather than in either steady state, particularly a transition that happens while a macro is mid-flight. Nothing in this ADR claims that is solved.

**Decision 5 puts the mode in the agent's fallback cache**, which means the mode is signed content under ADR-025 and a cache that fails verification leaves the mode `unknown`, which behaves as `show`. That composes correctly and is worth stating because the alternative reading, an unverifiable cache leaving a node in `program` mid-show, is the failure decision 5 exists to prevent.

**The category argument is the evidence here, and that is a weaker footing than this project usually accepts.** No measurement in this repository establishes that show mode reduces incidents. It rests on the owner's experience of how show control systems are built and on the fact that three subsystems were about to invent it separately. That is recorded plainly rather than dressed up: this is an L0 design decision, adopted because the alternative is the same concept implemented four times inconsistently.

## Alternatives considered

**A per-subsystem flag, which is what was about to be built.** Rejected: the Resolume WebSocket, FPP poll cadence, audio device-loss policy and asset sync timer each need the same condition, and four private booleans cannot answer one question.

**Derive it from playback state.** Rejected by decision 6. It makes the mode unpredictable exactly when it matters, it puts ShowMesh in the business of inferring the scheduler's intent, and an operator programming against a live playlist would be silently in `show`.

**Put it on a schedule.** Rejected for the same reason plus ADR-001: FPP owns the clock, and a second thing that changes system behaviour on a timer is a second scheduler wearing a different hat.

**Do nothing until a second subsystem needs it.** This is the honest minimal option and it was rejected on the owner's instruction that what gets built for the Resolume toggle be reusable across the app. Naming it now costs one ADR; naming it after four implementations costs a migration.
