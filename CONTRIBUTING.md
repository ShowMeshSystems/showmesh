# Contributing to ShowMesh

Thanks for looking. This document covers how to build and test, and — more importantly — the two conventions that make this repository work: **durable constraints change by ADR**, and **claims are never written above the evidence that supports them**.

## Getting set up

Requirements: Go 1.25+, Node 22+, Docker (for the Compose bundle and integration tests), `make`.

```sh
git clone https://github.com/ShowMeshSystems/showmesh.git
cd showmesh
make build     # all four binaries → ./bin
make check     # what CI runs on the fast path
```

`make check` is `fmt-check`, `vet`, `lint`, Go unit tests, and the UI's lint, typecheck, tests, build, and generated-types check. Run it before opening a PR.

### Test tiers

| Command | What it needs | What it proves |
|---|---|---|
| `make test` | nothing | Go unit tests |
| `make test-integration` | Docker | Control plane against a real Mosquitto with the agent as a real subprocess. Caught three defects the unit suite passed over on its first run, including one where the unit test asserted the correct ordering against a fake while the real wiring did the opposite. |
| `make test-integration-fpp` | Docker, patience | Collector against a real containerized `fppd`. The image is a full source build, so it is deliberately not in CI. |
| `cd ui && npm test` | Node | UI unit tests |

Both integration targets sit behind the `integration` build tag and never run as part of `make test` or `make check`.

CI runs on Go 1.25.0 and 1.26.5 across Linux and macOS with the race detector, builds the coordinator CGo-free, and builds the multi-arch image. A behavior verified only on macOS is **not** verified for this project — CI's first run caught a Linux-only `SO_REUSEADDR` difference that is now recorded in ADR-013.

## Before you write code

Read [`docs/build/BUILD-LOG.md`](docs/build/BUILD-LOG.md) first. Its "Current state" block is the latest build narrative and records what the most recent session observed. [`docs/build/BUILD-PLAN.md`](docs/build/BUILD-PLAN.md) holds roadmap order. Linear is the internal source of truth for the current work queue and next action.

Then check whether an accepted ADR already binds the work. [`docs/decisions/README.md`](docs/decisions/README.md) lists ADR-001 through ADR-022, all Accepted. The summary of the non-negotiable ones is in [`CLAUDE.md`](CLAUDE.md); the ADRs themselves are the authority.

## Reporting a problem or requesting a feature

Search the existing [ShowMesh issues](https://github.com/ShowMeshSystems/showmesh/issues) before opening a new report. Use the issue form that best matches the problem and include the smallest useful example. Bug reports should include the affected component, version or commit, environment, reproduction steps, expected and actual behavior, and operational impact. Feature requests should explain the user problem and desired outcome; an implementation design is not required.

Documentation issues belong in the [showmesh-docs issue tracker](https://github.com/ShowMeshSystems/showmesh-docs/issues/new/choose). Security vulnerabilities must follow the private process in [`SECURITY.md`](SECURITY.md), not a public issue. General support questions should use the project's established support channel when one is provided; do not put credentials or other secrets in any public report.

Public GitHub issues are used for contributor discussion and intake. They may be mirrored into the project's private Linear tracker for internal planning and ownership. Internal Linear identifiers, links, and discussion must not be copied into public issues, pull requests, or documentation.

After submission, a maintainer will check whether the report has enough information, look for duplicates, and route it to the appropriate internal work queue. A report may be asked for clarification, linked to a fix, or closed as a duplicate, out of scope, or already resolved. Creating an issue does not promise a particular implementation or release date.

## The two conventions

### 1. Durable constraints change by ADR, not by edit

If your change contradicts an accepted decision, the change is a **new ADR that supersedes the old one**, not a quiet edit to the architecture spec. This applies even when the new evidence is obviously right. The value of the record is that it says what was believed, when, and on what basis — silently editing it destroys exactly that.

Adding a new durable constraint works the same way: write the ADR.

The architecture specifications have owned scopes and are not allowed to drift into each other. `OBSERVABILITY.md` owns *what the operator surface must display*; `OPERATOR-UI.md` owns *how the client is built*, never what it displays.

### 2. Claims never outrun evidence

Research records in [`docs/research/`](docs/research/README.md) use an explicit ladder:

| Rung | Meaning |
|---|---|
| **L0** | Assumption. Written down so it can be attacked. |
| **L1** | Source-verified — vendor docs, protocol spec, source code. Needs a URL and an access date. |
| **L2** | Bench-verified against something real, with recorded versions and topology. |
| **L3** | Verified integrated in the actual system. **Required before adoption.** |
| **L4** | Verified resilient under failure injection. **Required before show readiness.** |

Rules that follow from this:

- **Unit tests never raise a record above L1.** Only a capture against real hardware does.
- **Never write a doc comment, log line, commit message, or document that claims verification that has not happened.** This is the single most important convention here.
- An empty evidence section is a **work queue**, not a conclusion.
- Facts, assumptions, and hypotheses stay visibly separate. Unmeasured timeouts, intervals, and thresholds are labelled as ShowMesh hypotheses in code and linked to the research record that owns them (usually RES-009 or RES-013).
- Statuses move `unresearched` → `planned` → `testing` → `verified`/`rejected`/`blocked`. A material environment change moves a conclusion to `stale`.
- Keep the tracker table in `docs/research/README.md` in sync with the individual records.

Normative language follows RFC-2119 spirit: **must**, **should**, **may**.

## Writing tests

**A test's name is a claim.** Before you trust a new test, break the behavior it names and confirm the test fails. A review pass on this repo did that and found three tests that passed with the asserted behavior removed from production code, one of them sitting on an acceptance criterion. A test that passes whether or not the bug is present is worse than no test, because it also reports success.

**Verify acceptance criteria against the running stack**, not against the suite. The suite's job is to catch regressions; it is not evidence that the thing works.

**Don't race a scheduler.** If a test needs an overflow, a timeout, or a back-pressure condition, construct it structurally. Do not grow a burst until it usually passes — that produces a test that is a coin flip on someone else's platform.

These rules came from real defects. [`docs/build/LESSONS.md`](docs/build/LESSONS.md) has the cases behind each one, including the 99 passing tests that shipped a UI which could not issue a single request in a browser.

## Pull requests

### Human documentation lives in `showmesh-docs`

Operator guides, tutorials, integration usage, troubleshooting, public reference, and other human-facing documentation belong in the separate [`ShowMeshSystems/showmesh-docs`](https://github.com/ShowMeshSystems/showmesh-docs) repository. This repository remains authoritative for implementation, `api/openapi.yaml`, tests, engineering specifications, ADRs, research evidence, build plans, and agent/contributor guidance.

Do not copy this repository's `docs/` tree into the public site. Verify human-facing claims against code, tests that constrain the behavior, the OpenAPI contract, compiled CLI help, and captured running-system evidence; engineering prose can lag implementation. Public docs may summarize architecture but never supersede its engineering source.

There is no linked-PR requirement, documentation release gate, or automated docs-update workflow yet. Those mechanisms are intentionally deferred until the first release process is defined.

Use the repository's pull-request template and keep its verification, review,
and acceptance sections distinct. Run `make pr-ready-check` after pushing the
final commit and after GitHub checks finish; it verifies that the local commit
matches the PR, every reported check passed, and GitHub reports a clean merge
state. It does not lint the PR body, run tests, or perform a review.

- One coherent change per PR, scoped to one seam (or a comparably small independent unit), not a whole delivery track. A track's seams merge as separate pull requests rather than one bundle, so each PR stays a size a reviewer, human or agent, can actually hold in their head. Combine seams into one PR only when they are trivially small or genuinely inseparable (e.g. one shared migration file both depend on), and say why in the PR body. Name the ADRs and research records it is bound by or touches.
- If it changes a wire type, update `api/openapi.yaml` — the conformance test runs in both directions and will fail otherwise.
- If it changes an API payload consumed by the UI, regenerate the types (`make ui-gen-check` tells you) — CI fails on any diff.
- Nothing in `cmd/showmeshctl` may import a coordinator package. An import-graph test enforces this; it exists so a JSON tag rename breaks the build instead of silently renaming the field on both sides.
- The coordinator must stay CGo-free. Pure-Go dependencies only — `modernc.org/sqlite`, never `mattn/go-sqlite3`.
- Don't add `docs/private/` to version control. It is deliberately untracked local working notes and nothing moves out of it into tracked documentation.

### Required checks before merge

Merges to `main` require these checks from the CI workflow to pass, matched
by exact job name: `lint`, `vuln`, `ui`, `docker`, and `test-gate`.
`test-gate` needs the whole `test` go-version matrix and fails unless every
leg succeeded; its own name stays stable across a matrix version bump, so
bumping `test`'s Go versions cannot silently rename the required check the
way requiring `test (1.25.0)` and `test (1.26.6)` directly would have. These
are deterministic: the same commit produces the same result, which is what
makes it safe to block merges on them.

The CI workflow's `integration`, `integration-fppmqtt`, and
`integration-broker` jobs, plus `test-integration-fpp` from the separate
"FPP Integration (bench fppd)" workflow, stay advisory. They still run and
report on every pull request, but a failure does not block merge, because
their current flakiness would otherwise block `main` on failures unrelated
to the change under review.

The CI workflow's `fpp-plugin-release` job is not on the required list.

A repository administrator may bypass a required check, but only as a
deliberate, recorded exception, never a routine override. Record what was
bypassed and why in the pull request before merging.

Branch protection enforcing this list is Eric's to apply; as of this
writing it is pending, not live.

## Scope notes

There are no write operations in this repository yet, and adding the first one is blocked by [ADR-021](docs/decisions/ADR-021-read-api-authentication-posture.md) rule 5 until a superseding ADR settles authenticated identities, authorization by target and action, audit attribution, the MQTT control plane's own authorization, and a browser session model. If you want to work on that, the ADR is the deliverable and it comes before the code.

Available without it: FPP MQTT ingestion, RES-008's configuration model, and `pkg/pjlink` as a protocol library at L1.

## License

By contributing you agree your contributions are licensed under [Apache-2.0](LICENSE). Do not vendor or link NDI runtime binaries — `dlopen` only.
