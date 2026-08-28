# Operator UI overhaul session handoff

Written 2026-08-28 by the first implementation orchestration session. This is
the starting brief for the next Operator UI session. The first pass is
published on `feature/operator-ui-overhaul` at `c4e8029`; continue that branch
and do not develop or push directly on `main`.

## Outcome of the first session

The feature branch now provides the new Operator UI foundation and moves the
main operator workflows into it:

- a responsive Operate, Author, and System shell with light, dark, and
  high-contrast themes;
- a runner-neutral current-runs REST and server-sent-events API;
- Dashboard, Live Control, and Show Night operator pages;
- Monitor overview, node, FPP player, and observation destinations;
- direct Settings navigation and a first-pass audio-node routing picker;
- a Show-local authoring workspace covering Overview, Run of Show, Cues,
  Assets, Automation, Presentation, Show Night, and Readiness;
- explicit unavailable states where the current coordinator API cannot yet
  supply the planned data.

This is a broad first pass, not final visual or bench acceptance. The branch is
intended to make the current system usable enough for review while the API
continues to move.

## Governing product decisions

- The UI is a show-control application, not a business dashboard.
- Live Control owns every operational command. Show Night is the detailed
  lifecycle and Run of Show view.
- FPP remains authoritative for its schedule, playlist order, selection, and
  playhead, but FPP is an optional runner integration rather than a ShowMesh
  prerequisite.
- The coordinator owns the combined current-runs projection. The browser never
  chooses authority or reconciles runners locally.
- Use `Transition Step` for Show Night lifecycle work. Reserve `Cue` for the
  independently revisioned `show.cue` object.
- Unknown, stale, failed, unavailable, and unobserved are different states and
  must remain visibly distinct.
- Keep every destination and action reachable at narrow widths. Do not replace
  the navigation with a hidden or horizontally scrolling destination list.
- The Mode control is a direct picker, not a link to Settings.
- Settings sections remain under Settings in the global navigation.
- Prefer direct forms and full edit pages. Do not add inspect-then-change modal
  chains.
- Known Show, node, route, asset, and FPP data should populate or constrain
  choices. Do not ask operators to copy opaque identities or channel numbers
  when the server can provide valid options.

## Implementation map

The main branch delta is concentrated in these areas:

- `api/openapi.yaml`, `internal/coordinator/currentrun/`, and
  `internal/coordinator/api/currentruns.go` define and project current runs.
- `ui/src/app/App.tsx`, `ui/src/app/Layout.tsx`, `ui/src/styles/tokens.css`, and
  `ui/src/styles/global.css` define routing, the global shell, themes, and
  responsive navigation.
- `ui/src/views/Dashboard.tsx`, `LiveControl.tsx`, and the `NightSession*` views
  implement the operator run-state pages.
- `ui/src/views/Monitor.tsx`, `Observations.tsx`, `NodesList.tsx`,
  `NodeDetail.tsx`, `FPPList.tsx`, and `FPPDetail.tsx` implement the evidence
  workspace.
- `ui/src/views/Configuration.tsx`, `AudioSettings.tsx`, `AudioNodes.tsx`, and
  `AudioNodeDetail.tsx` implement the first Settings and audio-routing pass.
- `ui/src/components/ShowWorkspace.tsx` and the Show-scoped views implement the
  authoring workspace while preserving the existing independently revisioned
  resource URLs.

## Review corrections already integrated

Fresh reviews found and the branch corrected these material defects:

1. Dashboard originally treated macro execution as the current show run and
   did not consume the new current-runs endpoint.
2. Successful Show Night commands disappeared immediately instead of leaving
   an operator-visible outcome.
3. Wrapped phone navigation could cover the final page content.
4. An unknown current-run status used the healthy color.
5. The production audio projection could associate an announcement session
   with the active Show's background playlist.

The final audio correction only resolves a ShowMesh audio playlist when the
node reports a current background source role and current matching playlist
revision evidence. Announcement, manual, show, missing, stale, unknown, and
mismatched evidence remain non-authoritative. A fresh adversarial review found
no path around that gate.

## Verification observed at `c4e8029`

- `make ui-gen-check` passed.
- `go test ./internal/coordinator/api ./internal/coordinator/currentrun`
  passed.
- The complete UI suite passed under Node 22: 95 test files and 1,154 tests.
- UI TypeScript checks and the Vite production build passed.
- `git diff --check origin/main...HEAD` passed.
- Docker image `showmesh-ui-overhaul:checkpoint-6` built successfully.
- Container `showmesh-ui-overhaul` serves the UI on host port `18082` and
  returns HTTP 200 for `/`, `/healthz`, and the proxied coordinator snapshot.

The Vite build still reports the existing warning that the main JavaScript
chunk is larger than 500 kB. One audit-view test also emits React's existing
missing-key warning while passing. Neither warning was treated as a successful
browser observation.

## What remains open

### Real-browser review

No browser surface was available to the first session. Desktop, tablet, 390 px
phone, light, dark, high-contrast, keyboard, focus, dialogs, dense data, and
stream-interruption behavior have not been observed in a real browser. Start
the next session by opening `http://localhost:18082` and checking the active
container before drawing UI conclusions; an older preview port may be stale.

### Audio routing API

The first-pass picker uses current node-advertised route and channel evidence,
prevents visible program/LTC overlap, supports program-only operation, and
retains an advanced manual path. The planned server-resolved output groups,
free-channel choices, and structured clock-verification choices are not all in
the API yet. Do not invent those choices from browser state. Add them through
the coordinator contract, OpenAPI, generated UI types, validation, and tests.

### API checkpoint rebase

The wider coordinator contract is still moving. After the pending API work
lands, rebase this feature branch, regenerate the UI types, rerun parity, and
update affected forms and placeholders. Do not remove an honest placeholder
until the new server evidence exists.

### Operator and bench acceptance

The Docker checkpoint uses the current development coordinator. That
coordinator may not include the feature branch's current-runs endpoint until a
matching coordinator build is run. Real deployed-node, FPP, audio, permission,
disconnect/reconnect, and safe command behavior remains a human bench gate.
Do not report those behaviors as verified from unit tests or the local static
container.

## Next-session execution order

1. Fetch and check out `feature/operator-ui-overhaul`; confirm the branch and
   worktree are clean before editing.
2. Read this handoff, the repository agent instructions, the accepted
   architecture records relevant to the changed area, and the internal Linear
   plan and current issue comments.
3. Confirm the Docker checkpoint on port `18082` and perform the real-browser
   desktop, tablet, and phone review. Record observed defects before broadening
   implementation.
4. Give each correction or independent API task its own worktree. Use a builder
   for implementation, a fresh reviewer for adversarial verification, and the
   coordinator as the final integration gate.
5. Finish the audio-routing server choices that are already planned. Keep the
   FPP plugin and unrelated coordinator systems out of scope.
6. Rebase after the pending API checkpoint lands, regenerate types, resolve UI
   drift, and rerun the complete root gate.
7. Refresh the isolated Docker checkpoint and keep the internal issue tracker
   synchronized with observed progress and remaining acceptance gaps.

## Standing session rules

- UI work is the scope. Only the small planned current-runs and audio-routing
  coordinator contracts may be added for the UI; do not change unrelated
  systems.
- Worktrees are required because other agents are working in this repository.
- Work only on the feature branch or isolated task branches. Never push to
  `main`, force-push, or merge without explicit authority.
- Push coherent task-branch and integrated feature-branch commits so review is
  not dependent on one local worktree.
- Prefer a cost-conscious builder model and a fresh independent reviewer. The
  orchestration session owns integration, final review, and claims of evidence.
- Monitor every supervised worker until it sends its terminal completion
  report. A heartbeat, idle prompt, or timeout is not completion.
- Preserve unrelated worktree changes. Stage explicit paths and inspect every
  diff for accidental files and private information before committing.
- Keep the UI review container isolated on its own name and port. Do not stop,
  rebuild, or write to the live fleet.
- Keep unavailable capabilities visible and literal. A placeholder is better
  than fabricated browser-side data.
- Update the internal issue tracker as work changes state. Keep implementation,
  browser review, and deployed bench acceptance as separate evidence.
- Use observed language: automated tests prove automated behavior; they do not
  prove browser layout, hardware, FPP, audio output, or deployed show behavior.

## Fresh-session prompt

Continue the ShowMesh Operator UI overhaul from
`docs/build/OPERATOR-UI-OVERHAUL-SESSION-HANDOFF.md` on
`feature/operator-ui-overhaul`. Follow the repository and applicable local
instructions, use isolated worktrees for every delegated task, keep work off
`main`, and monitor supervised workers through their completion reports. Begin
with the real-browser review of the Docker checkpoint on port `18082`, then
correct confirmed findings and finish the already-planned audio-routing API
choices. Keep the internal issue tracker synchronized, preserve explicit
placeholders for unavailable server data, rebase after the pending API
checkpoint lands, and do not claim browser or bench behavior without observing
it.
