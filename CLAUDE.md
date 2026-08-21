# ShowMesh Claude guidance

## Scope

`AGENTS.md` is the public, cross-agent contributor contract. Follow it first.
This file adds only Claude-specific operating guidance. Repository architecture,
safety rules, and public contribution rules override any private workflow or
personal preference supplied outside the repository.

ShowMesh is an open-source orchestration and observation layer for displays
built around FPP, xLights, and Resolume Arena. Its overriding system property is
that the show continues, degrades safely, recovers cleanly, and remains manually
controllable when individual components fail.

## Work from the repository, not from remembered status

- Use `README.md` and `CONTRIBUTING.md` for public project and contribution
  guidance.
- Use `docs/build/BUILD-LOG.md` for the latest recorded build state and
  `docs/build/BUILD-PLAN.md` for delivery order. Treat either as recorded
  history until the relevant code, branch, or pull request is checked.
- Use `docs/decisions/README.md` and the accepted ADRs for durable architecture.
  Do not relitigate an accepted decision during implementation. New evidence
  may justify a superseding ADR; it does not justify silently contradicting one.
- Use `docs/research/README.md` and individual RES records for researched facts,
  hypotheses, evidence levels, and remaining experiments.
- Use `api/openapi.yaml` as the public API contract and verify it against the
  implementation when an API surface changes.
- Do not require a public contributor to know about or access a private tracker.
  GitHub issues and pull requests are the public collaboration surface.

## Use judgment and keep the process proportional

These instructions are guardrails, not a checklist to perform on every task.

- Implement a small, well-scoped change directly. Do not create a plan,
  subagent, issue, ADR, or broad review pass unless the work actually needs it.
- For multi-file, architectural, ambiguous, or risky work, inspect the affected
  code and present a concrete plan before editing.
- Treat settled decisions as settled. Ask only when a genuine contradiction,
  destructive action, missing credential, or scope-changing choice cannot be
  resolved from the repository and the user request.
- Decide reversible implementation details and continue. Do not stop merely
  because several reasonable implementations exist.
- Preserve unrelated worktree changes. Never use destructive Git operations or
  force-push unless the user explicitly authorizes the exact action.
- Stop after two materially similar failed attempts. Change the approach or
  report the observed blocker; do not loop.

## Evidence and verification

Claims must match the evidence actually obtained.

- Say what was observed, what is inferred, and what remains unverified.
- A sent command, successful HTTP response, compiling package, or passing unit
  test is not automatically evidence that the user-visible behavior worked.
- When behavior changes, exercise the affected path at the closest practical
  level and try the original failure and a relevant error path.
- Keep facts, assumptions, and hypotheses distinct in research records. Unit
  tests do not raise a research record above L1; use the evidence ladder defined
  in `docs/research/README.md`.
- Do not claim hardware, browser, deployment, third-party runtime, or live-show
  verification that was not performed.
- Never write to the deployed show fleet, publish on its broker, change a live
  service, publish a release, or expose a credential without explicit authority.

## Choose gates from the change, not from habit

Classify the final diff before choosing verification.

### Documentation-only changes

A change is documentation-only when every changed file is prose or a research,
decision, build-history, contributor, or agent-guidance Markdown file and the
diff does not change executable code, tests, generated artifacts, dependency or
build metadata, workflows, scripts, deployment configuration, or
`api/openapi.yaml`.

- Review the rendered prose, links, internal consistency, evidence wording, and
  any indexes or status tables the document requires.
- Run a repository documentation or link checker if one exists and applies.
- Do not run `make check`, integration suites, hardware gates, or unrelated code
  tests merely because they exist.
- Do not wait for CI before pushing. CI can evaluate only commits that have
  already been pushed.
- When the user has asked to merge the documentation change, merge after the
  applicable documentation review and any checks GitHub actually requires. If
  required checks are still pending, enable auto-merge when available and
  authorized; otherwise report the real repository gate. Do not invent a build
  risk or an additional waiting period for a prose-only RES update.

Files that look like documentation but define executable contracts or tooling,
including `api/openapi.yaml`, GitHub workflow files, issue-form YAML, generated
artifacts, and configuration examples consumed by tests or deployment, are not
documentation-only for this rule.

### Code or executable-contract changes

- Run the narrow tests that exercise the changed behavior.
- Run `make check` before delivery when the change can affect the build, tests,
  generated output, API contract, or shipped binaries.
- Run integration targets only when the changed subsystem or acceptance
  criteria require them. Do not run `make test-integration` concurrently across
  worktrees sharing its broker or container resources.
- Run the FPP integration suite when FPP behavior is changed and that suite is
  the applicable running-system gate.
- Hardware, deployed-environment, browser, and third-party runtime checks are
  separate acceptance evidence. Record them as unverified when unavailable;
  their absence does not block unrelated implementation or branch publication.

## Delivery is part of implementation

For a task that changes repository files, local completion is not delivery.

- Commit coherent completed work after the applicable gates pass.
- Push the current task branch in the same session. This is standing authority
  for ordinary task-branch publication; do not ask again merely because pushing
  is outward-facing.
- If the branch has no upstream, use a safe current-branch push such as
  `git push -u origin HEAD`.
- Never claim delivery while session-created commits remain only local.
- If push is rejected, authentication fails, the branch diverged, or repository
  policy blocks publication, report the exact failure and reconcile only with a
  non-destructive approach. Never force-push as a shortcut.
- Do not develop directly on `main`. When a requested integration or merge must
  update `main`, use the repository's pull-request or merge-queue policy. Merge
  only when the task includes that authority.
- Do not wait silently for optional checks. Push first, then observe required
  checks. If a requested merge is waiting only on required CI, use auto-merge
  when available and authorized.

## Pull-request reporting

Use `.github/pull_request_template.md` exactly. Keep the PR body as a compact
handoff, not an investigation log.

- State the outcome in two or three sentences and list no more than five
  concrete changes.
- Record only verification run against the final pushed commit. Include the
  exact command and observed result; otherwise write `Not run` and why.
- Keep verification, review, and acceptance evidence separate. A passing test
  is not a completed review, and neither is hardware or deployment evidence.
- Before marking merge-readiness items complete, run `make pr-ready-check`
  against the final pushed commit and report its commit identifier.
- Never include review-pass narration, investigation history, a defect diary,
  or repeated design rationale. Link the owning ADR, research record, or build
  log when that detail matters.
- Add a PR comment only for a blocker, a decision, a failed check, or a direct
  response to review feedback. Do not post a running work diary.

## Durable repository rules

- The API is a public contract, not a UI implementation detail. Operator
  capabilities are API-first and must remain usable through `showmeshctl` at
  practical parity with the UI.
- The coordinator and UI are not the real-time timing or media path. A running
  show must survive their loss and broker loss as specified by the accepted
  ADRs.
- Desired and observed state remain separate. Report success only from evidence
  that post-dates the action.
- Preserve manual and reduced local fallback paths. Degradation must not turn a
  control-plane failure into a stopped show or remove operator visibility.
- Do not put private tracker identifiers, URLs, owner-only priorities, private
  notes, credentials, or deployment secrets in committed code, tests, API
  descriptions, documentation, commits, issues, or pull requests.
- Human-facing operator and integration documentation belongs in
  `ShowMeshSystems/showmesh-docs`. This repository owns code, tests, engineering
  specifications, ADRs, research evidence, build state, the OpenAPI contract,
  and contributor-agent guidance.
- Implementing another project's protocol is interoperability. Shipping content
  that falsely identifies ShowMesh as another vendor's product is prohibited.
- ShowMesh is Apache-2.0. Do not vendor or link NDI runtime binaries; use the
  runtime-loading boundary established by the accepted licensing decision.

## Code and documentation conventions

- Prefer names, types, small functions, and tests over explanatory comments.
  Comments state a non-obvious invariant, caller-visible contract, or safety
  reason that code cannot express; they do not preserve issue history, review
  narration, or implementation chronology.
- Put durable design rationale in ADRs, measurements in research records,
  current build history in the build log, and user-facing behavior in the
  appropriate documentation.
- When research changes a durable constraint, add or supersede an ADR instead
  of silently editing the architecture.
- Keep relevant ADR and research indexes synchronized when their records change.
- Keep `docs/private/` untracked. Nothing from a private overlay becomes public
  fact until it is independently suitable and verified for the public record.

## Private maintainer overlays

A private overlay may add internal issue workflow, current priorities, owner
decisions, private hardware context, and personal operating preferences. It must
live outside the repository and must not be required for a public contribution.

The overlay may narrow personal workflow, but it may not override repository
architecture, safety, security, evidence standards, contribution rules, or the
prohibition on publishing private information. Never copy private identifiers
or private tracker context into a public artifact.

When asked to change a local override, follow `AGENTS.md`'s local-override
contract and the maintenance file in the override's resolved canonical root.
The explicit request authorizes the narrow external edit and loader update, not
replacement of unrelated user configuration or publication of private content.
