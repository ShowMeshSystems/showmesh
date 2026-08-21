# Agent issue-tracking rules

When creating or updating ShowMesh issues, follow
[`docs/ISSUE-TRACKING.md`](docs/ISSUE-TRACKING.md). That guide is canonical;
do not copy its templates into agent-specific instructions.

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
