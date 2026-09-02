# Agent issue-tracking rules

When creating or updating ShowMesh issues, follow
[`docs/ISSUE-TRACKING.md`](docs/ISSUE-TRACKING.md). That guide is canonical;
do not copy its templates into agent-specific instructions.

File at most one follow-up issue per task, and only when the thing you found
breaks the work you were assigned. Everything else goes in your response as a
sentence for Eric to rule on. Never open an issue to record a review pass, a
retry, a plan, or something you noticed while you were in the file.

Keep a comment to one screen. Gate output goes in a fenced block or a link,
never as prose.

Before creating an issue:

1. Search Linear for a duplicate.
2. Write one independently verifiable outcome in plain language.
3. Make any decision unmistakably clear: say exactly what Eric must choose or
   do. Never use `OWNER DECISION` or `OPERATOR DECISION` title prefixes.
4. Put new or uncertain work in `Backlog`. Do not move work into `Todo` unless
   Eric asks. Use `Ready for work` only when scope, decisions, dependencies,
   and acceptance criteria are settled.
5. Set only metadata whose meaning is known. Assign the person responsible for
   the next action; use delegation while actively working.
6. Use `Bench` when closure evidence must come from Eric using real ShowMesh
   hardware or the deployed show environment. Add `Punch List` only for
   substantial multi-step commissioning or acceptance work.
7. Keep descriptions concise and put investigation history in comments.

Before changing Linear configuration or existing issues, stop and present the
draft required by the guide’s **Linear mutation gate**. Wait for explicit
human approval. The build log remains an active historical record through the
first pre-release; do not rewrite or flag superseded historical statuses as
current inconsistencies.

# Operator UI

Before changing anything under `ui/`, read
[`docs/design_handoff_operator_ui_overhaul/UI-DESIGN-GUIDE.md`](docs/design_handoff_operator_ui_overhaul/UI-DESIGN-GUIDE.md).
It is normative for the operator UI: tokens, layout, the four absences, copy,
and the component rules. `ui/src/kit` is the only stylesheet tree, and every
screen composes from it. A control the mocks have no home for goes to
`docs/ui-rebuild/OPEN-DECISIONS.md` for a ruling rather than being dropped or
invented. Verify the change in a browser against a running coordinator; the
unit suite does no layout.

# Local agent overrides

Contributors and maintainers may keep private or user-specific agent guidance
outside the repository. A local override must not be required for a public
contribution, copied into a commit, or allowed to override repository
architecture, safety, security, evidence, or contribution rules.

When a user asks to install, update, repair, or remove a local override:

1. Locate its active loader in the user's agent configuration and resolve the
   canonical external source. Do not edit an injected copy, cache, stale draft,
   or file inside the repository.
2. Read the override's maintenance contract when it has one. Back up only the
   affected files and preserve unrelated hooks, settings, plugins, and global
   instructions.
3. Keep the mechanism reusable. User, organization, repository, tracker,
   hardware, and workflow details stay in private instance files.
4. Verify the loader against both an intended repository and a non-matching
   repository, then read back the live configuration and permissions.
5. Never store credentials in prompt-visible guidance or work around a local
   permission boundary by copying private material into the repository.
