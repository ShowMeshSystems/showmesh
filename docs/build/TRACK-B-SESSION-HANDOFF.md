# Track B session handoff: the render node is the last founding problem

Written 2026-08-16 by the orchestration session that merged Tracks D and E. This is the starting brief for a new Track B orchestration session; point the session goal here. It supplements [TRACK-B-nodes-and-projection.md](TRACK-B-nodes-and-projection.md), which remains the track's specification — read that first, then this for what has changed underneath it since it was written.

## Why Track B is next

Of the three founding problems (FPP control, Resolume control, virtual-matrix generation), the first two closed with Track A and Track D. Track B is the third, it is BUILD-PLAN's "largest and riskiest track", and day-0 is mid-September. Everything Track B needed from other tracks landed on `main` on 2026-08-16:

- **Track E built B3's coordinator half.** A `show.surface` configuration object already exists (kind `show.surface`, seam E2) carrying the manual channel range, geometry, node assignment, and NDI/HDMI output selection. Track B must NOT re-model surfaces; it consumes the existing object. What remains of B3 is entirely node-side: reading the assigned surface, extracting its channel range from the local FSEQ at the frame the timeline reports.
- **Track E built the asset path B3 depends on.** The content-addressed store, `asset.fetch` on the agent, hash-verified node-local copies, the readiness manifest, and the sync service are all live. A render node resolves its FSEQ variant by show, sequence, target, and content hash — never by filename (ADR-028). Track B consumes the resolved local file and must not touch the store from any playback path.
- **Track D closed the downstream end.** Frames arriving in Resolume as a usable NDI source is Track B's finish line; what Resolume does with them is Track D's already-built territory. The boundary is written in the track doc's "Where Track B ends" decision and it held; keep it.
- **B1 (the real agent) merged 2026-08-14**: command topic subscription, allowlisted operations, outcome reporting. The allowlist grows per operation; `asset.fetch` was its second entry, Track B's pipeline operations are next.

## What exists that the track doc predates

- **B0 ran on 2026-08-16 and passed.** 982,100 frames at 1920x1080 UYVY, 0 dropped, 0 late, 0 errors, 40.00 fps for 6 h 49 min, Debian 13 OptiPlex Micro 7050 over wired Ethernet into Arena plus a second NDI receiver, sink `sync=true`. **B4's design is settled as NDI.** Evidence in RES-006, coverage limits in RES-005, caveats in the Track B doc's B0 entry. Note that the owner ran the harness pipelines by hand rather than through `run-spike.sh`, so there is no `bench/ndi-spike/results/` directory to go looking for; the numbers above are the record.
- A local test stack is running on the development laptop: coordinator (:8080), UI (:8081), authenticated Mosquitto (:1883), the bench `fppd` (`host.docker.internal:8090`, configured as `bench-fpp`), and a native `dev-node-01` agent from `~/showmesh-dev-node/`. A Track B session can develop against this stack; `deploy/mosquitto/add-agent-credential.sh <node-id>` provisions additional agent credentials.
- `pkg/multisync` remains the prebuilt hard half: wire codec, listener, and the FPP-remote timeline state machine on an injectable clock. This track is what it was written for. RES-002 is L2 for protocol semantics; clock drift and switch behavior stay open until the owner's physical bench run.

## Governing constraints (read the ADRs, not summaries)

ADR-002 (capabilities, versioned and attributed), ADR-005/ADR-026 (surfaces not projectors; NDI reference transport; N=1 is a scope limit that must not reach schema or wire; missing runtime degrades and never stops the node; dlopen only, never bundle), ADR-007 (GStreamer owns media; ShowMesh owns supervision and health, never per-frame work), ADR-008 (timing never traverses MQTT), ADR-011 (stale is `unknown`, never healthy), ADR-013 (does not constrain the render node — it runs no `fppd`, so it binds 32320 normally), ADR-025 (the signed fallback cache rules if B2 touches the cache), ADR-028 (playback never reaches the asset store). Above all: **the coordinator is never in the timing or media path.** The node renders; the coordinator watches.

## Suggested seam structure

1. **B2, pipeline supervision.** The agent builds, starts, watches, restarts a GStreamer pipeline (test pattern first), reports health as observations with provenance and freshness. Independent of B0's answer. Needs new allowlisted agent operations and new observation signals — request identifiers from the orchestrator before building. **Two constraints from B0's measurement**: the NDI encode costs 86% of one core and the spike ran it single-threaded, so pipelines this seam constructs put a `queue` between the renderer and the NDI sink rather than inheriting the spike's shape, and sender/receiver restart recovery is unverified (spike run 5 unrun) so supervision must not assume either side comes back on its own.
2. **B3 node-side, FSEQ extraction.** Read the surface assignment, extract the channel range from the node-local FSEQ at the timeline frame. The FSEQ format work should cite RES-008/the capture rather than re-deriving. MultiSync drives the frame clock; sync-loss behavior is a named owner decision (below).
3. **B4, NDI output.** **No longer blocked**: B0 answered it on 2026-08-16 and the answer is NDI. Build the adapter seam so a missing runtime degrades the node with a stated reason, and note that "missing" is a state this will genuinely meet, because the GStreamer NDI element is not packaged on Debian 13 and is a source build on the node.
4. **B5, capability advertisement.** `render.surface` and per-transport capabilities, advertised independently (support for one transport is never evidence for the other).

Two acceptance criteria in the track doc need real hardware or FPP playing a sequence (timeline-following, sync-loss behavior); everything else is provable against the bench `fppd` and the test stack. What cannot be proven locally goes to `docs/private/PUNCH-LIST.md`, not marked done.

## Decisions to put to the owner early

- **Sync-loss output behavior** (hold last frame / black / diagnostic): a product decision the track doc already flags. Queue it in `docs/private/DECISION-QUEUE.md` with the options if the owner is absent.
- ~~**Render-node OS** (Debian 13 recommended pending the spike's packaging check).~~ **Settled 2026-08-16: Debian 13**, benched successfully. The packaging check returned no, so the NDI plugin is compiled from `gst-plugins-rs` on the node; the owner accepted that for day-0 with a scripted build and owns node build mechanics outside the build sessions.
- **B0 scheduling**: ~~the spike~~ **done 2026-08-16**; the RES-002 physical bench remains owner bench work. Name what a result would change before asking for bench time. **Spike run 5, recovery, is still unrun** and is on the punch list; B2's supervision design should treat sender/receiver restart recovery as unknown rather than assume it works.

## Parallel small seams available to any spare builder

Non-blocking findings from the 2026-08-16 pre-merge reviews, safe as independent worktrees:

- `showmeshctl watch` has no case for `resolume.changed`/`resolumeRecovery.changed`, so routine Resolume telemetry triggers a spurious sequence-gap reset in the emergency-path tool; `refetchSnapshot` also never prints Resolume.
- `showmeshctl resolume status` renders raw Arena object ids where the UI renders names; the CLI action output also drops `resolvedId` and `selectedDeckChanged` while claiming field parity.
- Recovery surfaces: CLI output never renders the generated-name markers it decodes; the UI recovery panel shows no age/as-of and drops `startedAt`/`finishedAt`; the recovery toggle shows no revision/audit metadata in the UI.
- The `/config` nav label still claims the Resolume upload lives there; it moved to `/resolume`.
- Per-option ambiguity marking in the clip pickers (today only a blanket note says some clip is ambiguous).
- The no-process-supervision AST guard scans only `resolume*`-prefixed files in `internal/coordinator`; widen to cover `coordinator.go`, `api/`, `macro/`, `cmd/`.
- `TRACK-D-D3A-BUILD-CONTRACT.md` is cited 88 times across 16 files and is not in the repository (preserved at `docs/private/seam-specs/`); commit it or repoint the citations.
- Track E: the deploy bundle documents none of the five `SHOWMESH_ASSET_*` variables and ships with sync off and nothing saying so; the sync-disabled note only reaches `not_ready` nodes; `assets manifest --require-ready` exits 0 with zero declared nodes; a missing blob answers 500 rather than a distinct problem type; an idempotent re-upload skips the `Nudge`.
- Owner rulings queued, not builder work: open reads vs `readAnyGuard` on the asset manifest surface; superseded-bytes re-upload behavior; one-filename-two-assets per node; renaming the 16 ambiguous clips in the live composition; `deploy/README.md` still stating a restart is required for endpoint changes (stale since ADR-036).

## Standing session rules

The multi-track orchestration rules in CLAUDE.md apply unchanged: worktree per track, orchestrator mints all scarce identifiers (`showmeshctl` exit codes 16–19 remain Track D headroom to reassign deliberately, 22+ free; check registers before assigning), docs agent drafts documentation from evidence, builders never write the build log, unattended sessions never author ADRs, and the live-fleet prohibition is absolute — `deploy/.env.live-fleet-run.bak` exists locally and must never be combined with a write-capable stack.
