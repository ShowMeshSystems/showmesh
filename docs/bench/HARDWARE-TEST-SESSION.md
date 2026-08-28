# Running a hardware test session without freezing `main`

[Build log](../build/BUILD-LOG.md) · [Build plan](../build/BUILD-PLAN.md) ·
[Contributing](../../CONTRIBUTING.md)

Status: written 2026-08-28 for that day's hardware session, and meant to be
reused for every later one.

A hardware test session needs a build that does not move under it. That is not
the same as needing `main` to stop moving, and the two were being conflated:
finished branches were being held back so the fleet would keep matching `main`.
They do not have to match. The rig follows a session branch in its own
worktree, `main` keeps taking merges, and the two only meet when you choose.

## Setup

Cut the session branch from current `main` in its own worktree, so the ordinary
development checkout stays free for unrelated work:

```sh
git fetch origin
git worktree add -b dev/test-YYYY-MM-DD-hardware \
  ../ShowMesh-worktrees/test-YYYY-MM-DD origin/main
```

Build and deploy the fleet only from that worktree, and stamp the build so a
node can say what it is running:

```sh
make build VERSION=test-YYYY-MM-DD
```

`VERSION` and the short commit are compiled into the binary (`Makefile`,
`LDFLAGS`), so `showmeshctl version` and the agent's own version report identify
the exact tree. Branch HEAD moves as you commit fixes, so **record the commit
each time you deploy to the fleet**. The version string alone does not identify
a build once the session branch has advanced.

## When the session finds a defect

Fix it on its own branch off current `main`, not on the session branch:

```sh
git fetch origin
git switch -c fix/<short-name> origin/main
# fix, gate, commit, push, open the pull request
```

Then merge that fix branch into the session branch so the rig picks the fix up
and testing continues:

```sh
git -C ../ShowMesh-worktrees/test-YYYY-MM-DD merge fix/<short-name>
```

This keeps each fix reviewable and revertable on its own, which is the same
one-seam rule `CONTRIBUTING.md` applies to every other pull request. A session
that finds four unrelated defects owes four pull requests.

## The session branch does not merge into `main`

It is scratch. Alongside real fixes it accumulates instrumentation, throwaway
configuration, and pointed-at-this-rig settings that must never reach `main`.
Every change worth keeping has already gone to `main` through its own pull
request by the time the session ends. Delete the branch and the worktree when
the session is over.

If something on the session branch turns out to be worth keeping and was never
cut as a fix branch, cherry-pick it onto a branch off `main` and give it a
pull request like anything else.

## What this does not change

- **Do not rebase a pushed integration branch onto `main`.** Rebasing published
  history needs a force-push, which the contributor rules prohibit. Merge `main`
  into the branch instead. This is how a long-lived branch such as
  `dev/fpp-connect` stays landable while the session runs.
- **Do not hold finished branches back for the session.** That was the problem
  this arrangement removes.
- **Hardware evidence rules are unchanged.** Only what was actually observed on
  real hardware counts, and it is recorded against the Linear issue that owns
  the check (the `Bench` and `Punch List` labels), never inferred from a passing
  container gate.
