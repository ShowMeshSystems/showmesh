# Kickoff prompt for the next session

Paste everything below the line into a fresh session in `/Users/erbartos/Documents/code/ShowMesh`.

This file is overwritten at the end of each session. It is a convenience, not a source of truth: `BUILD-LOG.md` and `BUILD-PLAN.md` are authoritative, and if this file disagrees with them, they win.

---

Continuing the ShowMesh build. Start by reading `docs/build/BUILD-LOG.md` (the Current state block first) and `docs/build/BUILD-PLAN.md` Step 3. Steps 0, 1, and 2 are complete, committed, and green in CI including the integration job.

**We're starting Step 3: read-only FPP observability, plus the versioned public API and change stream that ADR-014 requires.** Read the Step 3 section of BUILD-PLAN for the deliverables and acceptance criteria before planning anything.

## How we work

You orchestrate, you don't implement. This is recorded in CLAUDE.md under "Build workflow":

- You write the step specification naming the ADRs and constraints the work is bound by, then delegate.
- Implementation goes to **Sonnet** subagents, one per independent seam, running in parallel where they touch disjoint files.
- Review goes to **Opus** subagents, given the diff plus the named ADRs, told to hunt constraint violations rather than style.
- You fold findings back yourself and you own every edit under `docs/`. Builders never write the build log.
- Overturn a builder's design decision when it's wrong, and say why in the message, not just what to change. Several of Step 2's best outcomes came from a builder pushing back on a spec I got wrong, so treat that as the system working.

Read the Step 2 specs in the scratchpad pattern if you want the shape: a shared contract binding all builders, then per-task specs referencing it.

## What Step 3 must not repeat

Step 2 shipped three defects that every unit test passed over, all found by an integration test against a real broker:

- A unit test asserted the correct shutdown ordering against a fake connection while the real wiring did the opposite. It passed the whole time.
- A liveness rule keyed on freshness instead of ordering, so the coordinator ignored a node's own announcement of its death.
- A forged message could permanently wedge a node using an ID readable from a public retained topic.

The standing rule that came out of it is in CLAUDE.md: **a test that passes whether or not the bug is present is worse than no test, because it also reports success.** Apply that to the API and change stream, which have exactly the same shape of risk.

The integration harness (`make test-integration`, Mosquitto plus a real agent subprocess) exists and should be extended rather than worked around.

## Carry these into the design

**`MultiSyncEnabled` defaults to off in FPP.** With it off, `fppd` plays sequences completely normally and emits zero MultiSync packets, logging nothing at default verbosity. Verified on the containerized bench. The collector and the readiness evidence must be able to distinguish "MultiSync is disabled" from "MultiSync is enabled but nothing is arriving", because they look identical from the wire and send an operator chasing switch configuration for an evening. See RES-002's evidence section.

**`offline` means the control-plane connection is gone, not that the node is dead.** A running show survives coordinator and broker loss. Anything the API exposes must not let a client render that as "dead". Recorded in BUILD-PLAN's Step 2 criteria.

**Retained MQTT deliveries carry no valid observation time.** The whole liveness model rests on this. If the API surfaces observation freshness, it must not invent a timestamp for evidence that has none.

**Unmeasured timing values.** The heartbeat interval (10s) and staleness window (30s) are labelled ShowMesh hypotheses in code and belong to RES-009. Don't let the API bake them in as though they were settled.

## Decisions Step 3 has to make

- **WebSocket versus SSE** for the change stream. Deferred from the UI specification explicitly so it would be decided with the API work. OPERATOR-UI section 6 forbids depending solely on aggressive polling.
- **Authentication.** OPERATOR-UI section 14 requires the mechanism be decided before the API gains write endpoints. Step 3 is read-only, so the decision can be scoped, but note that there is currently no authorization at all: any client with broker publish rights can create unbounded node rows on arbitrary valid node IDs. ARCHITECTURE section 10.4 governs.
- Whether the API surface warrants an ADR, or is an implementation detail behind ADR-014.

## Acceptance criterion worth respecting rather than satisfying

BUILD-PLAN requires the API be exercised end to end by a non-UI client, to prove it is usable without a browser rather than only believed to be. That is what keeps ADR-014 real. If the only thing that ever calls it is the UI we build in Step 4, the contract has quietly stopped being public and nobody decided that.

## Housekeeping

- `docs/private/` holds untracked notes on a third-party product. Never name it in tracked docs, code, comments, or commit messages, and never move anything out of that directory. Git history was already scrubbed of it once.
- There may be a stray `showmesh-agent.exe` in the repo root from a cross-compile. It is gitignored. Don't delete files without asking.
- RES-002 is L2 for protocol semantics and L1 for hardware and network behavior. That split is deliberate, it is the owner's call, and L2 does not license any claim about live-show behavior.
- The FPP bench (`bench/fpp-multisync/`) is built and validated in contained mode. Its macvlan mode is untested and needs the owner's Linux Docker host.
