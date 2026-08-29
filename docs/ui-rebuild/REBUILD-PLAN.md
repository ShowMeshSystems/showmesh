# Operator UI rebuild plan

Status: active. Started 2026-08-29 on `feature/operator-ui-overhaul-2`.
Phase 0 (element kit) is committed. Phase 1 begins at seam 1 below.

Normative sources, in this order:

1. `docs/design_handoff_operator_ui_overhaul/UI-DESIGN-GUIDE.md` (the rules)
2. `docs/design_handoff_operator_ui_overhaul/design/ShowMesh Design System.dc.html` (the element vocabulary)
3. `docs/design_handoff_operator_ui_overhaul/design/*.dc.html` (the fifteen screen mocks)
4. `docs/design_handoff_operator_ui_overhaul/DESIGN-DECISIONS-AND-API-FACTS.md` (verified identifiers)
5. `OPEN-DECISIONS.md` in this directory (Eric's rulings; they amend the guide)

## Why this is a rebuild and not a fix

The first pass lifted the token values correctly and then kept the old design
alive underneath them. Two things prove it:

- `ui/src/styles/tokens.css` carried an alias block mapping every old token name
  (`--color-bg`, `--font-size-body`, `--space-1`, `--radius-sm`, and about forty
  more) onto a new value, so the old stylesheets kept painting.
- `ui/src/styles/global.css` (1443 lines) was the pre-overhaul base stylesheet,
  still imported from `index.css`, mobile-first with 768px and 1024px
  breakpoints. The guide specifies a 1280 spine with a single break at 1100px.

On top of that, screens kept their old sections and had the mock's sections
added around them. Dashboard rendered ten blocks where the mock has three, with
"Needs you" demoted from full width into a third of a grid. Live Control matched
the mock for six sections and then appended four more from the old page.

A restyle cannot fix that. The old system is deleted, not aliased.

## What is already true, and does not get redone

- **The element kit exists.** `ui/src/kit` holds every element the design-system
  specimen demonstrates, with its own stylesheet tree under `kit/styles`, and a
  `/_specimen` route rendering the whole kit in three themes and both densities.
  Screens are composed from it. A screen that needs an element the kit lacks
  adds it to the kit, in that seam, with a specimen entry.
- **The information architecture already moved.** The route table is already
  seven rail destinations, four Monitor facets, five Shows tabs and seven
  Settings tabs. Phase 1 rebuilds what each route renders. It does not re-plumb
  navigation, except for the four folds ruled in `OPEN-DECISIONS.md` D-003.

## Which layer is fresh and which is kept

The rebuild is a presentation rebuild. Data access, domain derivation and
command semantics are load-bearing and verified; their markup is not.

**Kept as is.** `ui/src/api/**` (generated schema and client). In `ui/src/app`:
`ModelContext`, `types`, `session`, `time`, `evidenceState`, `fppSignals`,
`fppDashboard`, `observationPresentation`, `resolumeComposition`,
`UnsavedChanges`, `ScrollToTop`, and the `use*` hooks. In `ui/src/components`:
`useShowList`, `showWorkspacePaths`, `capabilityPanelRegistry`,
`fppCommandCopyGuard`. Their tests stay.

**Rebuilt.** Every file under `ui/src/views`. Every presentational component
(`ChromeBar`, `NavRail`, `SharedLayouts`, `StatusBadge`, `DomainBadges`,
`EvidenceValue`, `DataFreshnessNotice`, `ConnectionBanner`, `SessionPanel`,
`SignInForm`, `BootstrapClaimForm`, `TokenPrompt`, `PortGrid`,
`FleetSignalBadge`, `ShowModeIndicator`, `ShowModePanel`, `ShowWorkspace`,
`CapabilityPanel`). Every stylesheet under `ui/src/styles`.

**Split.** The command controls (`FPP*Control`, `Resolume*`, `NightCommandButton`,
`RunMacroButton`, `ActionInvokeButton`, `ScopedButton`, `AssetUpload`,
`MacroRun*`, `ActionBindingCheck`, `PanelErrorBoundary`) carry real request,
scope and outcome semantics. Keep the logic, including its tests. Replace the
markup with kit parts, in the seam that owns the screen the control appears on.
A control rebuilt in one seam and reused in a later one is not rebuilt twice.

## Ground rules

- Base on `feature/operator-ui-overhaul-2`. Do not merge or rebase `main` into
  this branch; it is deliberately behind.
- The rebuild is confined to `ui/src`. The coordinator and `api/openapi.yaml`
  work already on this branch stays.
- One PR per seam, run sequentially in this worktree. Every seam touches the
  route table and the shared kit, so parallel branches would collide there.
- No old design element survives a rebuilt screen. If a rebuilt screen still
  imports a `ui/src/styles` stylesheet or an old component, the seam is not done.
- Anything with no home in a mock goes to `OPEN-DECISIONS.md` for Eric, not into
  the page on my judgment. Never drop a control to make a screen match a mock.

## Seam list

Each row is one PR. The order is a dependency order: the shell first, then the
screens an operator uses during a show, then authoring, then administration.

| # | Seam | Routes | Mock |
|---|---|---|---|
| 1 | App shell and session states | layout, `*`, signed-out, bootstrap, connecting | `Session States` + guide §2 |
| 2 | Dashboard | `/` | `Dashboard` |
| 3 | Live Control | `/control` | `Live Control` |
| 4 | Show Night | `/night`, `shows/:id/night-sessions*` | `Show Night` |
| 5 | Monitor shell and Fleet | `/monitor/fleet` | `Monitor` |
| 6 | Monitor Signals | `/monitor/signals` | `Monitor` (Signals facet) |
| 7 | Monitor Activity | `/monitor/activity` | `Monitor` (Activity facet) |
| 8 | Monitor Capabilities | `/monitor/capabilities` | `Monitor` (Capabilities facet) |
| 9 | Monitor Manifest (new facet) | `/monitor/manifest` | composed from kit |
| 10 | Node detail | `/monitor/fleet/node/:id`, `/fpp/:id` | `Node` |
| 11 | Resolume Config | `/monitor/fleet/resolume*` | `Resolume Config` |
| 12 | Shows list | `/shows`, `/shows/new`, `/shows/:id` | `Shows` |
| 13 | Shows › Playlists | `shows/:id/playlists*`, readiness, playlist definitions | `Show Authoring` |
| 14 | Shows › Cues | `shows/:id/cues*` | `Show Cues` |
| 15 | Assets | `shows/:id/assets`, `/assets` | `Show Assets` |
| 16 | Shows › Presentation | `shows/:id/presentation*` | `Show Presentation` |
| 17 | Shows › Automation | `shows/:id/automation*` | `Show Automation` |
| 18 | Settings, seven tabs | `/settings/*` | `Settings` |
| 19 | Access | `/access` | `Access` |
| 20 | Delete the old system | — | — |

### Seam 1 detail

The shell is its own seam because every later screen renders inside it.

- `Layout` composed from the kit's `ChromeBar`, `ChromeProgress`, `Rail`,
  `RailGroup`, `RailLink`, `RailBadge`, `ShellBody`.
- Theme and density land on the shell root as `data-theme` and `data-density`.
- Signed-out and bootstrap render as **bands in the document flow** with the
  chrome and rail intact and a blanking plate in main. Not a full-screen login.
  `bootstrapRequired` outranks signed-out.
- Rail badges are attention counts. A signed-out or unread device shows none.
- `NotFound` carries the old-address → new-address map. Old addresses are not
  redirected.
- Deletes `components/ChromeBar`, `NavRail`, `ConnectionBanner`, `SessionPanel`,
  `SignInForm`, `BootstrapClaimForm`, `TokenPrompt`, and `styles/session.css`.

### The four folds

Ruled in `OPEN-DECISIONS.md` D-003. These routes had no mock; they fold into a
mocked screen rather than getting invented layout.

- **Playlist readiness** folds into the playlist configuration page (seam 13),
  not Show Night. Readiness is an authoring-time verdict about a playlist.
- **FPP playlist definitions** fold into the same playlist configuration page.
- **Night sessions** are Show Night. The list and detail routes fold into seam 4.
- **Asset manifest** becomes a fifth Monitor facet (seam 9). This amends the
  guide's four-facet list in §3; the guide's rule is about the seven rail
  destinations, and Monitor gains a facet, not the rail a destination.

## Per-seam procedure

Fixed. No deviation.

1. Read `OPEN-DECISIONS.md`. Rulings there override the guide.
2. Extract the mock's block list and order. That becomes the page's block list
   and order, exactly. No prepended header, no appended section.
3. Inventory every control on the current built page and assign each to a mock
   block. A control with no home goes to `OPEN-DECISIONS.md` with options and a
   recommendation, and stays rendered on the old page until Eric rules.
4. Trace every mono string to a file before typing it (`DESIGN-DECISIONS-AND-API-FACTS.md`,
   `pkg/capability/id.go`, `api/openapi.yaml`, `src/api/generated/schema.d.ts`).
5. Write the page as a new file composed from kit parts. Delete the old view,
   its stylesheet, and its now-invalid tests.
6. Run the seam's gates (below).
7. Commit, push, open the PR for that seam.

## Definition of done for a screen seam

- Block list and order match the mock.
- Every old control is either placed or recorded in `OPEN-DECISIONS.md`.
- No import of `ui/src/styles/*` and no old component import remains in the
  rebuilt file.
- `document.querySelectorAll('main h2, main h3')` returns the section labels the
  mock names, and they are real headings, not styled spans.
- Every status renders as a labelled pair. The dashed edge appears only for
  never-collected.
- Every control the principal may not use renders `disabled={true}` with a
  stated reason, and is actually inert.
- Night commands report *accepted*, never *done*.
- Tests: the new screen's test file covers each absence branch it can reach,
  each scope-disabled control and its reason, the headings assertion, and any
  202-wording path. Old view tests are deleted with their view; they assert the
  old DOM and are not worth porting.
- `npm run build` and the ui test suite pass. `make check` runs when the seam
  touches anything outside `ui/src`.
- Visual check at 1280 in dark, light and contrast, against the mock. When the
  dev server is not available in the session, this is recorded as unverified in
  the PR, not claimed.

## Phase 2: delete the old system

Seam 20. Remove every orphaned stylesheet and component under `ui/src/styles`
and `ui/src/components`, then add a test that fails the build if any file under
`ui/src` imports a stylesheet outside `kit/styles`, or references an old token
name. The old design is gone only when it is unreachable and a test says so.

## Standing rules carried from the design guide

- The four absences stay distinct. The dashed edge means never-collected, only.
- Every status is a labelled pair, a state word plus a colour, never colour alone.
- Anything in `--t-data` or `--t-meta` is a literal identifier and needs a file
  behind it. Check `DESIGN-DECISIONS-AND-API-FACTS.md` before typing a mono string.
- A control the principal may not use renders disabled with a stated reason,
  never hidden.
- Night commands answer 202. The UI says accepted, never done.
- Rail badges are attention counts. Tab counts are inventory counts.
- Signed-out and bootstrap are bands in the document flow, not a full-screen
  login page.
- A poll or refresh failure retains the last known state and says when it was
  read. It never blanks the region it was refreshing.
- No responsive pass below 1100px. Do not solve it by re-enabling bar wrap.
