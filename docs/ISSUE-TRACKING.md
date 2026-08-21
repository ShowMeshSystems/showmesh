# ShowMesh issue tracking

This guide defines how ShowMesh records, plans, and closes work. Linear is the
source of truth for internal issues and build tracking. GitHub is the public
front door for contributor reports; the GitHub-to-Linear integration may mirror
those reports, but internal decisions and ownership belong in Linear.

The goal is an issue that a person can understand quickly and an agent can act
on safely. An issue is not a transcript of an agent session, an architecture
essay, or a place to hide an unresolved decision.

## The writing standard

Use the [Google Developer Documentation Style Guide](https://developers.google.com/style)
as the general language baseline. In practice, ShowMesh issues should:

- Lead with the user, operator, or release impact.
- Say what is wrong or what outcome is needed before explaining internals.
- Use short sentences and familiar words. Define a ShowMesh-specific term the
  first time it is necessary.
- Prefer concrete verbs: “save the mapping” is better than “facilitate mapping
  persistence.”
- Use code locations, logs, and protocol details as evidence, not as a
  substitute for explaining the problem.
- Write for a reader who did not follow the current agent session.
- Keep the description focused. Put chronological investigation notes in
  comments; keep durable facts, scope, and completion conditions in the issue.

Normal issues should be readable in about two minutes. A parent release gate
or a forensic defect may be longer, but it still needs a short outcome summary
at the top.

## Before creating an issue

1. Search Linear for an existing issue, including closed issues and likely
   synonyms.
2. Decide whether this is durable work. A transient thought, command, or
   question does not need a ticket.
3. Choose one independently verifiable outcome. Split unrelated outcomes into
   child issues and relate them to a parent when useful.
4. Record the evidence that justifies the issue. Do not turn the description
   into a complete investigation diary.
5. Choose the status, priority, project, milestone, labels, owner, and
   relations using the rules below. Leave a field unset when its meaning is
   not known; do not guess.
6. Check that the title says what a human needs to understand, without an
   agent/session prefix.

## Issue templates

Use the smallest template that makes the work executable.

### Bug

**Title:** `Concise statement of the observable problem`

```text
## Problem
What happens, and who or what is affected?

## Expected behavior
What should happen instead?

## Evidence
Version or commit, environment/topology, reproduction steps, and relevant
logs or links. Redact credentials and private data.

## Acceptance criteria
- [ ] The problem no longer occurs in the stated scenario.
- [ ] A regression check or explicit verification is recorded.

## Non-goals
Anything intentionally left out of this fix.
```

### Implementation task or improvement

**Title:** `Action and intended outcome`

```text
## Outcome
What will be different for a user, operator, maintainer, or release?

## Scope
What is included, and what is explicitly not included?

## Acceptance criteria
- [ ] Observable condition one.
- [ ] Observable condition two.

## Evidence or context
Relevant source, prior issue, design note, or reproduction. Keep this concise.
```

### Decision

Decision issues must make the exact question unmistakable. A reader should be
able to answer “What am I deciding?” from the first paragraph without knowing
ShowMesh’s internal jargon.

The opening two sentences must say, in ordinary language:

1. **You are deciding whether...** Name the alternatives or behavior being
   chosen.
2. **This decision changes...** Name the operator-visible result, blocked work,
   release gate, cost, or risk affected by the answer.

If two questions can be answered independently, create two decision issues.
Do not bury the choice below the investigation that produced it.

**Title:** `Choose whether/how [plain-language decision]`

Do not prefix titles with `OWNER DECISION`, `OPERATOR DECISION`, or similar
labels. The `Needs decision` label handles routing; the title must describe the
decision itself.

```text
## Decision needed
Choose one specific behavior, policy, or owner action. State it as a direct
question or an imperative choice.

Example: “Should the coordinator keep retrying when the FPP connection drops,
or stop after three attempts and require operator action?”

## Why this matters now
Name the blocked work, user impact, release gate, or safety concern.

## Options
1. Option A — plain-language behavior and its main tradeoff.
2. Option B — plain-language behavior and its main tradeoff.

## Recommendation
State the recommendation and the evidence behind it, or say why no
recommendation is appropriate.

## Decision record
Leave blank until decided. Record the chosen option, decision owner, and date.

## Unblocked work
Name the issue(s) or next action that can proceed after the decision.
```

Do not create artificial options or use terms such as “control-plane
semantics,” “operational posture,” or “lifecycle convergence” without defining
them in ordinary language.

For example, replace “Choose the audit-store failure posture for
safety-class-none macro dispatch” with “When the audit database is unavailable,
should a macro run the action or stop without running it?” Replace “Choose LTC
re-anchoring semantics across playlist boundaries” with “After the next
playlist item starts, should timecode restart at the configured offset or
continue from the elapsed show time?”

### Hardware or owner verification

State the exact action and the evidence required to close it. Identify the
hardware, setup, person, and observable result. A request for one real-device
check uses the `Bench` label; it does not automatically become a punch list.

### Parent gate

Use a parent issue for a bounded release, commissioning, or multi-step outcome.
Keep it short: purpose, child issues, final gate, and known non-goals. Put each
independently verifiable action in a child issue and link it with `parent` or
the appropriate Linear relation.

## Statuses

Keep these distinctions; they describe different kinds of readiness:

| Status | Meaning | Agent rule |
| --- | --- | --- |
| Backlog | Captured work that has not been reviewed or scheduled. | Agents may create issues here. |
| Backlog - Reviewed | Reviewed, valid work that is not yet staged. Rename `Backlog - HOLD` to this when changing the workspace. | Do not treat it as ready. |
| Todo | Eric’s up-next staging area for people or agents. | Do not pull work from it unless asked. |
| Ready for work | Scope, decisions, dependencies, and acceptance criteria are settled. | Agents may claim and start it. |
| In Progress | Work is actively being performed. | Keep the next action and owner clear. |
| In Review | Implementation or evidence is waiting for review. | Link the PR or review artifact. |
| On Hold | Progress is intentionally stopped by a recorded external condition. | State the unblock condition in the issue. |
| Done | Acceptance criteria and required verification are complete. | Add completion evidence. |
| Canceled | The work will not be done. | Record why when the reason is not obvious. |
| Duplicate | Another issue is authoritative. | Link the surviving issue. |

Do not add a second status to express a label, priority, project, or ownership
category. `Todo` is staging; `Ready for work` is agent-claimable readiness.

## Priority, ownership, and delegation

- **Urgent:** safety, data integrity, a live deployment, or the active release
  gate is blocked. This interrupts planned work.
- **High:** required for the current milestone or committed delivery.
- **Medium:** planned work that is not currently gating delivery.
- **Low:** worthwhile work without a current commitment.
- **No priority:** temporary triage only.

Assignment means who owns the next action. Leave an issue unassigned when it is
awaiting triage or a decision. An agent may use Linear’s delegate field while
actively working, but must not assign Eric merely to keep an issue visible.
Assign Eric when the next action genuinely requires his decision, review, or
hardware. Use `Needs decision` for an actual choice, not for general agent
uncertainty.

## Labels and relations

Use one work-type label where applicable (`Bug`, `Feature`, or `Improvement`),
one or two component labels, and only the handling labels that change the next
action. Useful handling labels include `Needs decision`, `Needs Review`,
`Bench`, `Punch List`, and `After Day-0`.

Labels must not duplicate status, priority, project, or milestone. `Bench`
means closure evidence must come from Eric using real ShowMesh hardware or the
deployed show environment, including a real device, host, browser connected to
a live service, or physical show chain. Use it for a one-off or a larger task,
not for an automated test against a fake or local fixture.
`Punch List` means a substantial multi-step operator commissioning or
acceptance task. Use both for a multi-step hardware gate; use only `Bench` for
one real-hardware check. Keep `Bench` until the required evidence is recorded.

Link duplicates, blockers, dependencies, and parent/child work. A relation
should explain how the issues affect each other; do not add links merely to
make a ticket look connected.

## Projects, milestones, and cycles

- A **project** is a bounded body of related work with an outcome, status,
  ownership, and completion point.
- A **milestone** is a meaningful checkpoint inside a project.
- A **label** identifies a component or handling category.
- An **issue** is one independently verifiable piece of work.
- **Cycles** are optional planning windows. Do not introduce them until the
  team has a real weekly or biweekly planning cadence.

For the FPP plugin, the planned shape is:

- Project: `ShowMesh FPP Plugin — Initial Release`
- Milestones: `Contracts frozen`, `Private candidate ready`, `Bench verified`,
  and `Release ready`

The existing `FPP Plugin` milestone should not duplicate that project. Keep it
only if it represents a narrower ShowMesh Core integration gate. For docs, a
bounded `Public Documentation — First Release` project is appropriate through
the first public release; routine docs defects can use a `Public Docs` label
afterward.

## Completion and build history

Every completed issue needs a concise final comment:

- Outcome: what changed or was decided.
- Verification: what was actually run or observed, including hardware when
  required.
- Remaining limitations or follow-up issue.
- Pull request or other artifact, when applicable.

The build log remains active through the first pre-release. It is a historical,
chronological record, not a single-session status board. Older entries may say
“in progress” and later entries may say “done”; that is expected history. When
a later entry explicitly supersedes an older snapshot, do not flag the older
wording as a current inconsistency during review. A dated **CORRECTION** that
explicitly names and invalidates older text works the same way. Still flag a
contradiction in Current state or the latest relevant entry when nothing
explicitly supersedes it. Linear owns current work, ownership, dependencies,
and status; the build log records what happened over time and continues to be
updated under its existing instructions.

Durable, actionable deferred work mentioned in the build log still needs a
Linear issue. At the first pre-release, review whether the log should be
distilled or frozen; do not rewrite its historical entries merely to make them
look like a current snapshot.

## Linear mutation gate

Before changing Linear configuration or existing issues, prepare a draft for
human approval. The draft must show:

1. Statuses to retain, rename, create, or archive, including the destination
   for affected issues.
2. Label changes with names, meanings, and affected-issue counts.
3. Project and milestone changes, including the plugin and documentation
   examples above.
4. Assignment, delegation, and priority changes.
5. Before-and-after samples covering a human issue, a Claude issue, and a Codex
   issue.
6. The order of operations, readback checks, and a rollback approach.

Do not make Linear mutations until that draft is explicitly approved. Apply
approved changes in small batches and read back the result after each batch.

## Public GitHub intake

GitHub issue forms should collect enough information for triage without asking
contributors to design the fix. Bug reports should request component, version
or commit, topology, reproduction steps, expected and actual behavior,
regression status, logs, impact, and confirmation that secrets are redacted.
Feature reports should request the user problem, desired outcome, concrete use
case, current workaround, and impact.

Security reports follow `SECURITY.md` and must use the private security route.
Public forms should not expose private Linear links or internal decision
context.
