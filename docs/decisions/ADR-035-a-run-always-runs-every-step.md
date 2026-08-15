# ADR-035: A run always runs every step

Status: Accepted
Date: 2026-08-14

Supersedes [ADR-031](ADR-031-macro-execution-model.md) decision 2's default, and narrows [ADR-024](ADR-024-identity-authorization-and-audit.md) decision 11 so that it does not apply inside a macro run.

Originally issued as ADR-033 on the `step-9-wave-3` branch, and renumbered on 2026-08-15 when that branch merged. Track D issued [ADR-033](ADR-033-show-mode.md) on `main` the same day and neither session knew of the other; show mode kept the number because it had more references. Nothing about this record's content changed.

## Context

Step 9's acceptance run exercised two of this project's own rules against a running coordinator, a bench `fppd`, and a genuinely unwritable `audit_log`, and both produced a macro run that dropped a command it had been asked to send.

**The audit gate.** With `audit_log` unwritable, a macro of `[stopPlaylist, startPlaylist]` was refused in its entirety at submission with `503`. The predicate was "every step's safety class is one of ADR-024 decision 11's three exempt actions", so one non-exempt step made the whole run refusable, and the stop did not run.

**The abort default.** A step that failed suppressed every step after it. The remaining steps were recorded `skipped`, which is honest, and they did not happen, which is the problem.

Neither behaviour was an oversight. Both were specified, both were reviewed, and both have arguments behind them that read well on paper. The audit gate is fail-closed, which is the correct instinct almost everywhere. The abort default gives an operator one clean failure instead of a cascade, which is a real benefit when a human is reading the result.

**What both arguments have in common is that they were made against the wrong failure direction**, which is the error [ADR-024](ADR-024-identity-authorization-and-audit.md) itself was written to correct, and which this project has now made four times in four subsystems. The question is never "is refusing safer than proceeding". It is "safer for whom, and against what". A refusal protects the operator from an unaccountable actor. In a macro run there is no actor to hold accountable: the run was already authorized, the principal is already known, the steps are already pinned. What the refusal removes is not an attacker's ability to act, it is the show.

## Decision

### 1. A macro run never withholds a command because the audit store cannot be written

Every step of a run dispatches, whatever its safety class, whether or not the audit entry lands.

Attribution is not abandoned. It is downgraded and stated: the step carries `attributionDegraded`, the run carries it, both clients render it, and the coordinator logs the cause. The operator loses the audit trail for that run and keeps the show. That is the trade, made explicitly, in that direction.

The owner's own words are the rule, and they are recorded here because the paraphrase is weaker than the original:

> the run needs to always RUN all steps, no matter what. If something doesnt confirm, or we cant record it, that doesnt matter. it should still send the command. we cannot risk the show because a logging or audit system is down, thats not how show critical infrastructure works.

**ADR-024 decision 11 survives outside a run.** The single-command HTTP path still fails closed for a non-exempt action, because an operator is present, sees the `503`, and can retry deliberately. It also survives unchanged for `config:write` and `principal:write`: nothing in a running show depends on rewriting configuration mid-show, so refusing those costs nothing a show notices.

### 2. A failed step does not stop the run

`onFailure` now defaults to `continue`. A failed step marks the run not completed, names itself in the run's reason, and the sequence carries on.

**`abort` survives as an explicit per-step choice.** An operator writing it into a macro is making that call themselves, with the reason visible in the definition where a second person can read it. That is a different thing from ShowMesh making it for them by default, which is what ADR-031 decision 2 did.

### 3. `completed` is keyed on what happened to the steps, never on whether the run stopped

A failed step sets `completed: false` even though the run continues. Those two conditions used to be the same flag because a failure always aborted; they are not the same condition any more, and keying `completed` on abort would have made a run whose first step failed and whose later steps succeeded report `completed: true`.

That is the failure [ADR-029](ADR-029-logical-actions-and-integration-bindings.md) decision 4 names by hand: a step that always reports success is worse than no step, because the operator stops reading it.

## Consequences

**A run can now do partial damage where it previously did nothing.** A `[stop, start]` macro with a broken stop will now attempt the start. This is the intended trade and it is not free: the operator recovers with primitives, exactly as ADR-031 decision 2 already specified for the no-automatic-compensation case.

**A run is more likely to report `completed: false` than before**, because failures no longer hide behind an abort that suppressed later steps from ever being tried. That is more honest reporting, not a regression.

**The word "exempt" leaves the macro vocabulary.** The owner's reaction to it was that it "makes me think we're ignoring if it hit an error or whatever", and inside a run there is no longer an exemption to name: every step dispatches. The safety class remains on `show.action` because ADR-024 decision 11 still uses it outside a run.

**This does not weaken the audit trail where the audit store works.** Nothing about attribution changes on a healthy coordinator. The change is entirely in what happens when the audit store is broken, which was previously "the show stops" and is now "the show runs and says it could not be recorded".

## Alternatives considered

**Keep the all-exempt submission gate, and tell operators to write stop-only macros.** Rejected. It makes the correctness of a show depend on an operator knowing that mixing a stop with anything else changes what happens when a disk fills. Nobody will know that at 17:00.

**Refuse only the non-exempt steps mid-run, letting exempt ones through.** This was the reviewer's per-step rule and it is strictly better than the gate it would have replaced, which is why the specification contained both. It was still rejected: it keeps ShowMesh deciding that some commands are worth dropping to protect a log file, and the owner's rule does not admit that category.

**Make `onFailure: abort` unavailable.** Rejected as over-reach. An operator explicitly asking for a sequence to stop is not ShowMesh deciding to stop it.
