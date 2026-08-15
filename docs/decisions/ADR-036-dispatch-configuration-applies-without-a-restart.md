# ADR-036: Configuration that governs dispatch applies without a restart

Status: Accepted
Date: 2026-08-14

Narrows [ADR-009](ADR-009-sqlite-configuration-storage.md)'s revision model with a delivery requirement, and closes the gap [STEP-9-SPEC](../build/STEP-9-SPEC.md) §5.6 described but did not have.

Originally issued as ADR-034 on the `step-9-wave-3` branch, and renumbered on 2026-08-15 when that branch merged, alongside [ADR-035](ADR-035-a-run-always-runs-every-step.md) whose number collided with Track D's show mode record. Nothing about this record's content changed.

## Context

Step 9's acceptance criterion 20 requires that removing an FPP endpoint while a run is in flight resolves the affected step `failed`, naming the instance. Measured against a running coordinator, the `PUT` returned `200` and the in-flight run's next step dispatched to the removed endpoint and **confirmed**.

The cause was not a missing check. The check existed and was correct: dispatch resolved the instance id against a list, refused an unknown one with a reason naming it, and the acceptance run proved that path works by restarting the coordinator and watching the same step fail properly. The list itself was captured once, at process start, into a plain slice. Only a restart moved it.

**The coordinator was not lying about this.** Every `fpp.endpoints` response carried `restartRequired: true` with a sentence explaining that the collector polls the list it read at startup. The information an operator needed was in the response.

**That is what makes this worth an ADR rather than a bug fix.** Being honest in the response is not the same as being safe. The failure it produces is: an operator removes a dead or repurposed host, sees `200`, and ShowMesh keeps sending that host commands until someone happens to restart the process. The owner's decision, 2026-08-14:

> we cant have that kind of failure, it shouldnt blindly be sending commands and only having the info needed to fix the problem in the response or log. So how about we follow the spec and make it work immediately.

## Decision

### 1. Configuration that decides where a command goes is resolved per dispatch, never captured at startup

`fpp.endpoints` is read from the active revision on every call that needs it. A removed endpoint stops receiving commands the moment its removal is the active revision, with no restart and no window.

### 2. The collector set follows the same configuration while the process runs

Removing an endpoint stops its collector. Adding one starts a collector for it. Changing an existing endpoint's URL is a stop and a start, never a no-op, because "same id, different host" is exactly the case an id-only comparison sails past while every later poll goes to the old host.

**Both halves are required, and this is the part worth writing down.** Live resolution alone fixes removal and breaks addition: a newly added endpoint would become dispatchable while nothing polled it, so every command sent to it would dispatch and then fail to confirm. That failure is *worse* than the one being fixed, because it looks like broken hardware rather than like configuration that has not landed, and it would send an operator to the wrong place at showtime. A decision to make removal immediate is therefore also a decision to make addition immediate.

### 3. Reconciliation is polled, and the interval is chosen against a deadline

The collector set is reconciled on a ten second interval. The number is not "small enough to feel fast": a command's confirmation deadline is twenty seconds, so a newly added endpoint is being polled well inside the window of the first command an operator could plausibly send after adding it. Measured on the bench: an endpoint added at `T` had a running collector and a completed first poll at `T+3s`.

Removal does not depend on this interval at all. Dispatch is live, so a removed endpoint stops receiving commands immediately; reconciliation only stops the now-pointless polling afterwards.

### 4. A configuration read that fails returns the last known list, and says so

A transient store error must not empty the fleet. An empty endpoint list is indistinguishable, to everything downstream, from an operator having deliberately removed every endpoint, so manufacturing one on a failed read would turn a database hiccup into a fleet-wide outage that looks like a deliberate change. The last successfully read list is returned and the failure is logged.

This is [ADR-011](ADR-011-context-aware-observability.md)'s rule in a fifth subsystem: absence of evidence is not evidence of absence.

### 5. Removing an endpoint deletes none of its observations

A collector that stops running leaves its last observations in place to age out of `current` on their own, rather than having them deleted. A reader must see stale evidence become `unknown`, not see it vanish, which is the same rule Step 5's pruning decision and ADR-011 already settled.

### 6. `restartRequired` stays on the wire and now reports `false`

The field is not removed, because the control API is additive-only within a major version ([ADR-020](ADR-020-control-api-shape-and-change-stream.md)) and a client reading it must keep working. It now says what is true, which is that nothing is pending.

## Consequences

**This is scoped to `fpp.endpoints` and to what it governs.** It is not a general hot-reload mechanism and does not claim one. Other configuration keeps whatever delivery model it already has; a kind that genuinely needs a restart should say so in its own response, truthfully, exactly as this one did.

**The collector `Runner` gained a dynamic registry.** `Add` after `Run` used to be silently ignored, which its own doc comment described as intentional ("a dynamic registry this package has no current need for"). The need arrived. `Add` is now idempotent by collector id and `Remove` cancels the collector's poll loop, so a reconcile pass can call `Add` for the whole desired set on every tick without restarting healthy collectors.

**The gap between an active revision and a running poll set is bounded but real.** For up to one reconcile interval, an added endpoint is dispatchable while not yet polled. A command sent inside that window dispatches and confirms late or reports unconfirmed. The interval is chosen so that window sits inside the confirmation deadline, but the window exists and is not zero.

## Alternatives considered

**Leave it restart-scoped and document it harder.** Rejected by the owner in the words quoted above. The response already documented it; that was not enough, because the operator only reads the response at the moment they make the change, and the consequence arrives hours later.

**Notify the collector set from the configuration write path instead of polling.** Rejected for now as more coupling for a smaller win. A callback from the API handler into the collector runner makes the write path responsible for a subsystem it otherwise knows nothing about, and the measured three second latency of the polled version is already well inside the deadline that matters. If a future kind needs sub-second application, that is the point to revisit this.

**Make only removal live, since removal is the dangerous direction.** Rejected, and this is the argument that most needed writing down: see decision 2. Half of this change is not a smaller version of it, it is a different and worse bug.
