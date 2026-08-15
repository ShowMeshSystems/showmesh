# ADR-031: The Macro Execution Model

Status: Accepted  
Date: 2026-08-14

**Decisions 2 and 5 were rewritten before adoption**, after a review of the Step 9 specification found both wrong in ways this project has recorded before. The drafts are quoted inside each decision rather than removed, because in both cases the error is the instructive part. A first draft was briefly marked Accepted while still under review; that was itself a mistake, since an accepted record has to be superseded rather than revised, and revising was plainly the right move here.

## Context

[ADR-004](ADR-004-layered-commands-and-fallback.md) decided that ShowMesh exposes primitive commands and composes them into named show macros, and it listed four properties macro execution must have: persisted, idempotent, observable, and compensatable. It decided nothing about what a run *is*.

Step 9 is the first step that has to answer, and every question it forces has a plausible wrong answer that this project has already paid for once in a different subsystem.

A macro is a sequence, so a run has duration measured in minutes rather than the sub-second of a primitive. It has partial outcomes, because step 3 can fail after steps 1 and 2 succeeded. It has steps that are structurally unconfirmable, because [ADR-029](ADR-029-logical-actions-and-integration-bindings.md) decision 4 permits an action whose effect ShowMesh cannot observe. And it is fired by FPP's scheduler, which per [RES-015](../research/RES-015-fpp-plugin-distribution-model.md) §7.3 carries no invocation identity and no retry, so duplicate firings come from the operator, from overlapping schedule entries, and from the MultiSync command fan-out.

## Decision

### 1. A macro run is asynchronous

Submitting a run returns a run identifier and the run's initial state. It never returns a completed result. Run state is read back and announced on the change stream; a client learns the outcome by watching rather than by waiting.

This is forced rather than chosen. Step 7 shipped a server holding an unconfirmed response for 20 seconds against a CLI that gave up at 10 and a browser at 15, which made `unconfirmed` unreachable by both shipped clients and reported a transport failure for a successful conversation with a healthy coordinator. A macro legitimately runs for minutes, so a synchronous run reproduces that defect at a timescale where it is certain rather than possible. **Two timeouts on opposite sides of one contract are a single decision, and the way to stop making it wrong is to stop having a long-held response.**

### 2. A failed step aborts the remainder. An unconfirmed step does not

> **SUPERSEDED IN PART, 2026-08-14, by [ADR-035](ADR-035-a-run-always-runs-every-step.md).** The `onFailure` default is now `continue`, not `abort`: a run always runs every step, and a failure is recorded rather than allowed to suppress the sequence. Everything below about the two axes being *independent*, about `unconfirmed` never stopping a show, and about no automatic compensation, stands unchanged and is if anything strengthened. What ADR-035 removes is only this section's choice of which direction the failure axis defaults to, on the grounds that a show control system must not drop commands it was asked to send. `abort` remains available as an explicit per-step choice.

A step that **fails** stops the run, and the run records which step stopped it. A step that resolves **unconfirmed** does not stop the run; it marks the run not confirmed, naming the step. Each behaviour is overridable per step, and each default is the safe direction for its own axis.

**The draft said "fails, or resolves unconfirmed within its deadline", and collapsing those two was wrong.** `failed` means something answered and it was not what the operator declared. `unconfirmed` means evidence was expected and did not arrive. The first is a statement about the show; the second is a statement about ShowMesh's own evidence pipeline. Treating the second as failure is absence of evidence read as evidence of absence, which this project has decided correctly in four other subsystems and got wrong here at the only point that consumes the distinction.

It also points the degradation the wrong way, which is the specific error [ADR-024](ADR-024-identity-authorization-and-audit.md) was written to correct. **This architecture degrades toward the show continuing.** An abort-on-unconfirmed rule degrades toward the show not starting, and the measured numbers arm it: a second same-instance step resolves at roughly 15 s against a 20 s confirmation deadline, so a slow poll at 17:00 would abort a working show start and leave the display dark, with the cause being that ShowMesh could not watch rather than that anything failed. A monitoring gap must never stop a show.

The default is abort for failure, continue for unconfirmed, and either override must be written down. An operator who wants the show to start even though the projector power step reported an actual failure can say so, and then that intent is in the definition where a second person can read it, rather than being a property of the executor nobody chose.

**No automatic compensation.** ADR-004's word "compensatable" is not implemented and is not implied. A partially-run macro leaves the system in a state the operator recovers from with primitives, which is what they have. An executor that automatically undid steps would be making show decisions on its own, which is the direction [ADR-001](ADR-001-fpp-is-authoritative.md) points away from.

### 3. `completed` and `confirmed` are separate facts and are never collapsed

A finished run reports both:

- **`completed`** — every step dispatched and none aborted the run.
- **`confirmed`** — every step produced post-dispatch evidence that its effect occurred.

Whenever either is false the run carries a reason naming the step and the cause.

They differ constantly and legitimately, which is the whole argument for keeping them apart. A macro containing an action that declares no expected response is unconfirmable by construction and reports `completed: true, confirmed: false` every time it runs perfectly. Collapsing the pair forces a choice between calling that run a success, which teaches the operator that the indicator means nothing, and calling it a failure, which teaches them the same thing faster.

ADR-029 decision 4 already established that a step which always reports success is worse than no step. This is that rule applied to the run, and it carries an obligation onto the surfaces: **the two must be visually distinct.** A run that completed without confirmation may not render as a run that confirmed.

### 4. A run interrupted by a coordinator restart is never resumed

On startup, a run left in flight is finished as not completed, with a reason, and its remaining steps are not dispatched.

The show has moved on and ShowMesh does not know how. Resuming would mean a coordinator that restarted at 03:00 dispatching the second half of a show-start macro at 03:00. This is the same shape as the existing startup sweep for unresolved commands, which resolves them rather than retrying them, and the same instinct as [ADR-011](ADR-011-context-aware-health.md)'s rule that stale evidence is `unknown` rather than healthy: **when the system cannot know, it must not act as though it does.**

### 5. The audit exemption applies per step, and it is declared on the action

[ADR-024](ADR-024-identity-authorization-and-audit.md) decision 11 says blackout, stop and power-off are never refused for want of an audit write, because every other degradation in this architecture points at the show continuing while an unqualified audit gate points at the operator being unable to stop it.

**A step's own action decides whether that step is exempt.** A refused non-exempt step fails and aborts the run under decision 2, with a reason. An exempt step dispatches with degraded attribution, recorded on the step and raised onto the run.

**The draft said the whole run is exempt if any step is, and that was the same defect Step 8's review had already closed one level down**, where the exemption had been applied to all eight primitives rather than the two stops, so a start would have dispatched unaccountably when the audit store failed. At macro level it is worse: adding a stop step to any macro makes every start in it unattributable on a full disk. **A stop step becomes a laundering mechanism**, and the draft's own consequences saw that hazard and called it "the correct direction".

The draft's supporting argument was also made against the wrong failure direction, which is the error ADR-024 exists to name. It said the alternative "produces a half-run, which is worse for an operator trying to stop a show". But an operator trying to stop a show runs a stop, not a stop-then-start macro. Under the per-step rule the stop still runs and the start is refused. **The half-run being feared is the case where the safety-critical half already succeeded.**

**The exemption is declared, because otherwise it inverts.** Safety class is a property of the FPP primitives only, and an external integration action has none. Day-0 projector power is an external MQTT action, so under a naive reading a power-off macro would fail closed with the audit store down, and power-off is one of the three actions decision 11 names by hand as never refusable. So an action carries a required safety class from a closed enum that matches decision 11's list exactly and adds no members: none, blackout, stop, power-off. For an FPP action it must agree with the primitive's registered class and is rejected if it does not; for an external action the operator declares it, which is the only place the information exists.

**The posture is evaluated at submission.** Decision 1 makes the run asynchronous, so once the run is accepted there is no response left to carry a refusal. A run whose steps are not all exempt is refused at submission when the audit store is unwritable. A store that becomes unwritable mid-run resolves each subsequent step under the per-step rule.

### 6. An overlapping run of the same macro is refused

A run submitted while another run of the same macro is in flight is refused, and the refusal names the in-flight run.

This follows Step 8's answer to the same question about starting a playlist against a busy host, and RES-015 §7.3 supplies the reason it is not theoretical: FPP will not duplicate a command on its own, but the operator, an overlapping schedule entry, and the MultiSync command fan-out all will. A double-fired 17:00 schedule must not start the show twice. Idempotency keys remain required and still deduplicate a repeated submission; this guard covers two genuinely different submissions arriving close together.

## Consequences

- Every client is a watcher. `showmeshctl` needs an explicit follow mode and the Operator UI needs a run view; neither can be a request that blocks until done.
- The change stream grows a run event kind, additively under [ADR-020](ADR-020-control-api-shape-and-change-stream.md), and remains non-resumable, so a client that misses frames re-fetches the run rather than reconstructing it.
- A run pins the macro revision and each action revision at submission, so editing a macro at 16:58 cannot change what the 17:00 run does halfway through.
- Decision 3 puts a real obligation on design work that has not happened yet. The Operator UI's command surface is already recorded as working and not designed; two booleans that must not be confused makes that debt slightly more expensive and considerably more important.
- Decision 5 makes safety class a required, validated field on every action, including actions for integrations that do not exist yet. Each new integration must answer "can this action be one of decision 11's three?" at the point it is added, which is the right moment to ask and a cost every future integration pays.
- Decision 5 also means a macro can be partly refused when the audit store fails: the stop runs, the start does not, the run aborts and says so. An operator reading that has to understand why one step was allowed and the next was not, which is a real explanatory burden the surfaces have to carry rather than hide.
- Decisions 2 and 5 together mean the run's two booleans and its abort behaviour are driven by four independent per-step properties: failure policy, unconfirmed policy, safety class, and local fallback class. That is more schema than a first draft wanted, and each one exists because collapsing it produced a specific wrong behaviour rather than merely a less expressive one.

## Alternatives considered

**A synchronous run.** Rejected under decision 1. It is simpler for exactly as long as no macro takes longer than a client timeout.

**Continue past a failed step by default.** Rejected: it matches how an FPP playlist treats an unresolvable command, which FPP's own source comment describes as letting a show silently skip a step forever with nothing but a log line. A macro that always runs to the end reads as success-shaped.

**One collapsed outcome.** Rejected under decision 3. Every collapsing rule considered either lied about unconfirmable steps or made correct runs look broken.

**Resuming an interrupted run.** Rejected under decision 4.

**Run-wide audit exemption, where any exempt step exempts the whole run.** Considered, drafted, and rejected under decision 5. It makes a stop step into a mechanism for laundering unattributable starts, and it was defended with an argument about an operator trying to stop a show, which is a scenario the per-step rule serves at least as well.

**An unconfirmed step aborting the run.** Considered, drafted, and rejected under decision 2. It is the safest-sounding option and it means a monitoring gap stops a show.

**Queueing an overlapping run instead of refusing it.** Rejected: a queued show-start that fires when the first one finishes is a second unwanted show start, delayed. Refusing is loud, and loud is what a double-firing schedule needs.

## Related research

[Configuration model](../research/RES-008-configuration-model.md) §4, which enumerates the decisions a show macro forces · [FPP plugin distribution model](../research/RES-015-fpp-plugin-distribution-model.md) §7.3, on the absence of any invocation identity in FPP

## Supersession

Supersedes nothing. It answers questions [ADR-004](ADR-004-layered-commands-and-fallback.md) left open and does not narrow it, except that ADR-004's "compensatable" is explicitly not implemented and decision 2 records that as a scope limit rather than a disagreement.

**Decision 5 leaves [ADR-024](ADR-024-identity-authorization-and-audit.md) decision 11 exactly as it stands.** This is worth stating because the draft did not: it widened decision 11's closed list of three actions to "any run containing one", which is a change to an accepted ADR's durable constraint made in a document whose supersession section said it changed nothing. A future reader of ADR-024 would have read its closed list and not learned it had been opened. The rewritten decision 5 applies the same three actions to the same granularity ADR-024 uses, and the required safety-class field is the mechanism by which a non-FPP action can be one of those three rather than a fourth member.

**Decision 2 leaves ADR-003 as it stands** and depends on it: a step is confirmed on evidence that its effect occurred, and an absent confirmation is recorded as absent rather than reinterpreted as failure.
