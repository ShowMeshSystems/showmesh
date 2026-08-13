# Track D: Projector power and control

[Build plan](BUILD-PLAN.md) · [ADR-016](../decisions/ADR-016-controlled-devices-and-control-providers.md) · [RES-012](../research/RES-012-device-telemetry-adapters.md) · [RES-014](../research/RES-014-control-provider-model.md)

Status: not started. Specified 2026-08-13. Smallest of the four tracks.

## Goal

A showtime macro can turn the projectors on, select their input, and see whether they actually came up. A shutdown macro can turn them off.

## The scope decision this track exists to make

[ADR-016](../decisions/ADR-016-controlled-devices-and-control-providers.md) settles that external devices are **controlled devices driven by providers**: a resource class distinct from nodes, with no agent, no advertisement, no local fallback, and no Last Will. It also requires providers to declare their configuration, actions, and telemetry as **metadata**, with operator surfaces built from that metadata rather than from per-device frontend code.

**That metadata contract is unresearched.** [RES-014](../research/RES-014-control-provider-model.md) is at L0 with no evidence, and the metadata-generated-surface hypothesis is exactly that, a hypothesis.

So there are two ways to build this, and the choice must be deliberate:

- **Build the provider framework first**, meaning the metadata contract, the generated surfaces, and projectors as its first consumer. This is the architecturally correct order and it is a research record plus a framework plus a device, on a five-week clock, for a track whose day-0 requirement is "turn the projectors on."
- **Build `pkg/pjlink` and one deliberately narrow projector provider**, hardcoded rather than metadata-driven, and let RES-014 be informed by having actually built one.

**The recommendation is the second**, and the reason generalizes: ADR-016's metadata contract is a design for a plurality of device types that do not exist yet. Building the abstraction before its second consumer is how you get an abstraction shaped like exactly one device with extra ceremony. Building one concrete provider first gives RES-014 something real to generalize from.

**What this costs, stated plainly:** the first projector surface is per-device frontend code, which ADR-016 says surfaces should not be. That is a deviation and it should be recorded as one in this track's build log entry, along with the intent to fold it into the metadata model once RES-014 has evidence. It is not licence to keep doing it for the next device.

## Deliverables

**D0. Probe the deployed projectors.** All of them support PJLink and were purchased for it, but the per-model class is unknown: `CLSS?` has never been run against them. **Class 1 covers power, input, mute, errors, and lamp; Class 2 adds freeze and resolution.** Day-0 needs Class 1 only, so this probe most likely confirms the track is easy rather than changing it. Run it first anyway, because "all support PJLink" is a purchasing fact and not a measurement, and the models are mixed.

**D1. `pkg/pjlink`.** PJLink is TCP 4352 with a simple line protocol, and there is no mature Go library, so this is written here. Class 1 commands: power on and off, power query, input select, input query, error status, lamp hours. Authentication is part of the protocol and some projectors have it enabled, so it is in scope rather than assumed off.

**D2. A projector controlled-device provider**, narrow and concrete, under ADR-016's resource model even though its surface is not yet metadata-generated. Devices are configuration objects, which needs no migration: `config_objects` is keyed `(kind, id)` with a JSON payload, so a projector is a new `kind`.

**D3. Telemetry as observations.** Power state, input, errors, and lamp hours flow through the same observation model as everything else, with provenance and freshness, so stale projector state reads `unknown` rather than healthy. Lamp hours in particular are the kind of thing an operator wants before a season, not during one.

**D4. The macro step type**, so Track A's Step 9 can sequence projector power alongside FPP actions. **This is a coordinator-hosted provider, so any macro step touching it is labelled coordinator-required** per ADR-016 and ADR-004's narrowing. A controlled device holds no local fallback; if the coordinator is gone, the projectors do not turn on, and the macro must say so rather than appearing to succeed.

## Decisions this track must make

- **Whether power-off needs a confirmation or a guard.** Turning projectors off mid-show is the destructive direction, and lamp-based projectors dislike rapid power cycling. Some models refuse commands while cooling, which is a real state the provider has to model rather than treat as an error.
- **What "on" means for confirmation.** [ADR-003](../decisions/ADR-003-desired-and-observed-state.md) wants evidence the state moved, and a projector reports powering-on as a distinct state from on, with a warm-up period. Confirming on the first non-off reading would repeat Step 7's confirmation defect in a new subsystem.
- **Whether the projectors are reachable from the coordinator's network at all**, which is a topology question worth answering before writing the client.

## Acceptance criteria

- `CLSS?` results for every deployed projector model are recorded in RES-012.
- Power on, power off, input select, and error query work against a real projector.
- A power-on is confirmed on evidence that the projector reached on, not on the command being accepted and not on the first reading that is merely not-off.
- Projector telemetry appears in the Operator UI with provenance and freshness, and goes `unknown` when stale.
- A macro step controlling a projector is labelled coordinator-required.
- With the coordinator stopped, the projector step fails visibly rather than silently.

**Bound by:** ADR-003, ADR-004 as narrowed by ADR-016, ADR-011, ADR-016.

**Out of scope:** the RES-014 metadata contract and metadata-generated surfaces, every non-projector controlled device, UniFi PoE control, UPS and NUT integration, and environmental sensors. Those are RES-012's broader queue and none of them is day-0.
