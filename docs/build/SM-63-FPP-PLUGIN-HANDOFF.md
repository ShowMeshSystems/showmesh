# SM-63 FPP Plugin Implementation Handoff

[SM-63](https://linear.app/showmesh/issue/SM-63/build-the-fpp-910-hybrid-plugin-runtime-and-three-repository-release) · [RES-018](../research/RES-018-fpp-brightness-control.md) · [RES-015](../research/RES-015-fpp-plugin-distribution-model.md) · [ADR-043](../decisions/ADR-043-show-scoped-cues-and-playlist-authority.md) · [SM-14](https://linear.app/showmesh/issue/SM-14/first-real-host-hybrid-fpp-plugin-install-res-015res-018)

Use this brief to start a fresh Claude session responsible for the plugin runtime. The product and architecture decisions are frozen in the linked records. The executor implements them; it does not create another architecture pass or replace the Linear decomposition.

## Claude prompt

You are implementing the ShowMesh FPP plugin runtime tracked by Linear SM-63. Start by reading SM-63 and all six children, then read the governing records on current `ShowMeshSystems/showmesh` `main`:

- `docs/research/RES-015-fpp-plugin-distribution-model.md`
- `docs/research/RES-018-fpp-brightness-control.md`
- `docs/decisions/ADR-010-apache-2-license.md`
- `docs/decisions/ADR-013-no-fpp-control-port-sharing.md`
- `docs/decisions/ADR-024-identity-authorization-and-audit.md`
- `docs/decisions/ADR-038-fpp-authorizes-night-sessions.md`
- `docs/decisions/ADR-043-show-scoped-cues-and-playlist-authority.md`
- `docs/architecture/RESTING-MODE.md`
- `docs/build/STEP-9-SPEC.md`
- `docs/build/TRACK-H-cues-and-playlists.md`
- `SECURITY.md`

The Linear work breakdown is authoritative:

- SM-148 — bootstrap the Apache-2.0 runtime repository and extract the Go helper
- SM-150 — freeze coordinator brightness and playlist-observation contracts; this is main-repository work and may proceed in parallel
- SM-149 — host-neutral native brightness and playlist-identity core; blocked by SM-148 and SM-150
- SM-151 — FPP 9/latest-beta native adapters; blocked by SM-148, SM-149, and SM-150
- SM-152 — locked hybrid packaging in `fpp-showmesh`; blocked by SM-148 and SM-151
- SM-153 — automated matrix and private candidates; blocked by all implementation children
- SM-14 — first real-host install and public-release approval; do not absorb it into SM-63

If this session owns only the plugin repository, begin with SM-148. Do not implement SM-150 in the plugin repository; consume its frozen fixtures and contract. Stop with a precise contract mismatch if SM-150 is not ready rather than inventing a parallel wire shape.

### Bootstrap `ShowMeshSystems/showmesh-fpp-plugin`

Create the fresh repository as private. Do not publish a release.

The initial repository must contain:

1. Apache-2.0 `LICENSE`.
2. A human-readable `README.md` explaining:
   - what the plugin does and why it exists;
   - the Go helper versus resident C++ component;
   - the responsibilities of `showmesh`, `showmesh-fpp-plugin`, and `fpp-showmesh`;
   - supported FPP 9.4–9.x and latest-published-beta FPP 10 policy;
   - exact local build, test, lint, and cross-build commands;
   - artifact and packaging flow;
   - security/credential posture;
   - current verification limits and the SM-14 real-host gate.
3. A canonical cross-agent `AGENTS.md` with the execution rules below.
4. A short `CLAUDE.md` supplement for Claude-specific operation, with no private tracker dependency.
5. `docs/upstream/showmesh/UPSTREAM.md` recording:
   - source repository URL;
   - exact ShowMesh source commit;
   - snapshot date;
   - every copied source path;
   - the rule that snapshots are read-only evidence and refreshes are deliberate commits, never silent edits.
6. Provenance-preserving copies under `docs/upstream/showmesh/` of every governing record listed at the top of this prompt. Preserve their relative paths beneath that directory so internal links remain intelligible.
7. CI and local commands that exercise the extracted Go helper independently of the ShowMesh monorepo.

Do not copy the main repository's general `CLAUDE.md`; write instructions for this repository and its narrower responsibility.

### Frozen implementation rules

- Apache-2.0 is decided. Upstream GPL code is behavioral/source evidence only and is not copied or linked unless a separate compatibility review explicitly allows it.
- Support FPP 9.4–9.x and the latest published FPP 10 beta available at build/verification time. Pin the exact tags and commits in CI evidence; do not make builds float silently.
- The Go helper remains a forked command helper. The C++ component is resident and uses isolated FPP 9/libhttpserver and FPP 10/Drogon adapters around host-neutral cores.
- Use `playlistCallback`, never `eventCallback` or `MultiSyncPlugin`, for playlist identity.
- The callback copies bounded evidence and returns. No HTTP, retry, hashing, playlist fetch, persistence, or sleep runs on FPP's callback thread.
- The resident worker implements RES-018 §6 exactly: authenticated HTTP, `fpp:observe`, RFC 8785 canonical playlist hash, deterministic entry key, persistent monotonic sequence, bounded latest-state coalescing, and explicit gap evidence.
- Never bind a second UDP 32320 listener.
- Preserve the existing Go helper's handwritten HTTP behavior and 2xx-only prior-failure flush.
- `fpp-showmesh` pins exact artifact filenames and SHA-256 hashes in committed `artifacts.lock.json`; a checksum fetched beside a mutable artifact is not the trust anchor.
- Candidate artifacts remain private until SM-14 passes and the owner approves publication.
- Hardware-only evidence stays open. A unit test, container, or fake is not a real-host result.

### Linear discipline

Linear is the work ledger. Work one assigned child issue at a time unless the dependency graph explicitly permits parallel work.

- Read the issue and its parents/relations before editing code.
- Move the child to In Progress when work actually starts.
- Put decisions and acceptance evidence on the child, not only in terminal output.
- Do not create replacement issues, broaden scope, or reopen frozen decisions. If a real gap appears, post one concise blocker with the exact conflicting citations and stop that seam.
- Do not post running narration. Add a Linear comment only for a genuine blocker, a contract change that affects another child, or completion.
- Keep each comment to at most four short sentences plus links or a compact gate block.
- On completion, attach the PR, report the observed gates, name what remains unverified, and move the child to In Review. Do not mark the parent or SM-14 complete from a child session.

### Brevity and code-comment discipline

Be direct. Do not overthink settled choices, restate the entire issue, or produce essay-length plans and progress reports. Lead with the outcome, then only the evidence needed to review it.

Code comments explain a non-obvious invariant, safety boundary, or external-system trap. Keep them to one or two sentences. Do not write narrative histories, duplicate the code, leave speculative design essays, or add untracked TODOs. A necessary TODO names its Linear issue.

README prose should be human-readable and complete, but still edited for repetition. Test names and failure messages should state behavior precisely without commentary.

### Completion standard

Passing unit tests is not completion by itself. Each child closes only when its stated acceptance behavior is observed. Before calling a child done, run its complete relevant gate, inspect the diff for copied secrets or generated artifacts, and try the failure cases named in the issue. Report exact commands and results; use `should` for anything not observed.

Start with SM-148 unless the operator explicitly assigns a different child.

## Coordinator note

SM-150 may run in parallel in the main ShowMesh repository. Its output is a frozen schema and shared fixture set, not a shared Go SDK. The plugin session must not block repository bootstrap or the brightness-only host-neutral core while that contract is being completed, but it must not merge the identity publisher against an invented contract.
