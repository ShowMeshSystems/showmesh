# Operator UI rebuild plan

Status: active. Started 2026-08-29 on `feature/operator-ui-overhaul-2`.
Phase 0 (the element kit) is committed. Phase 1 starts at Dashboard.

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

A restyle cannot fix that. This is a wholesale rebuild. The old design is thrown
away, not aliased and not partially kept.

## What "fresh start" means here

Everything under `ui/src` is rebuilt from the kit, with one exception:
`ui/src/api` is generated from `api/openapi.yaml` and is not design. It stays.

Everything else goes: every file under `ui/src/views`, every file under
`ui/src/components`, every stylesheet under `ui/src/styles`, and the app shell
in `ui/src/app`. `kit/styles` becomes the only stylesheet tree in the codebase.

A rebuilt screen is written from the mock and the API contract. It is not
written by reading the old screen and restyling it. The old page is consulted
once, for the control inventory in step 2 below, and then deleted.

`ui/src/domain` holds the non-visual rules a screen needs: sign-in state, the
scope gate and its wording, freshness and age, signal grouping. It started with
`session.ts` only. A module returns to it one at a time, reviewed as it is
restored, when the screen that needs it is rebuilt. Nothing comes back because
it happened to be there before.

**The risk I am carrying, stated once.** Some behaviour lives only in old code:
freshness and absence classification, FPP signal lookup, the derived numbers the
guide's §7 lists, the exact wording of scope-denial reasons. Where the rule is
in `api/openapi.yaml`, `pkg/capability/id.go`, or
`DESIGN-DECISIONS-AND-API-FACTS.md`, I re-derive it from there. Where a rule
exists only in a deleted file and I cannot source it, it goes on the
`OPEN-DECISIONS.md` list rather than being reinvented quietly.

## Ground rules

- Base on `feature/operator-ui-overhaul-2`. Do not merge or rebase `main` into
  this branch; it is deliberately behind. The coordinator and `api/openapi.yaml`
  work already on this branch stays.
- One PR per screen, run in order, sequentially in this worktree. You can reject
  one screen without unwinding the rest.
- No old design element survives. If a rebuilt screen imports a `ui/src/styles`
  stylesheet or an old component, it is not done.
- An element the mocks missed is composed from kit parts so it lands in the
  language. If it cannot be composed, the kit gains the element, with a specimen
  entry, in that PR.

## Phase 0: the element kit (done)

`ui/src/kit` holds every element the design-system specimen demonstrates as a
real component plus a real CSS class, with `/_specimen` rendering the whole kit
in three themes and both densities. That page is the acceptance gate for the
visual language.

## Where the rebuild is

| Screen | State | PR |
|---|---|---|
| Clear the old UI, shell, session bands, not-found | Done | on the branch, `1a1eed1` |
| Dashboard | Done | #196 |
| Live Control | Done | #197 |
| Show Night | Done | #198 |
| Monitor · Fleet, facet tabs, inspector | Done | #199 |
| Apply the D-005 to D-010 rulings | Done | |
| Session states: signed out, bootstrap, connecting, not found | Done | #200 |
| Monitor · Signals, Activity, Capabilities, Manifest | Done | #201 |
| Shows workspace: shell, show list, Identity, Playlists | Done | #202 |
| Shows workspace: Cues, Assets | Done | #203 |
| Shows workspace: Presentation, Automation | Next | |
| Node detail | | |
| Settings, seven tabs | | |
| Access | | |
| Resolume Config | | |
| Delete the old system | | |

Each PR is stacked on the one before it, so its diff is only its own screen.

## Phase 1: rebuild screens, one screen at a time

Fixed procedure. No deviation.

1. Extract the mock's blocks and their order. That becomes the page's blocks and
   order, exactly. No prepended header, no appended section.
2. Inventory every control on the current built page and assign each one to a
   mock block.
3. Anything with no home in a mock block goes on `OPEN-DECISIONS.md` before the
   page is written. Eric rules: fold into an existing block, add a new block in
   the kit language, or drop it. I do not decide this and I do not stall the
   rest of the screen for it: the ruled items land in a follow-up PR for that
   screen.
4. Write the page as a new file composed from the kit. Delete the old view, its
   stylesheet, and its now-invalid tests.
5. Verify by screenshot against the mock at 1280 in dark, light and contrast.

Order, as agreed:

0. Clear the old UI and stand up the shell (chrome bar, rail, theme and density
   root, session bands, not-found map, a blanking plate on every route the
   rebuild has not reached)
1. Dashboard
2. Live Control
3. Show Night, including the night-session list and detail
4. Monitor and its facets: Fleet, Signals, Activity, Capabilities, Manifest
5. Shows workspace, five tabs: Playlists, Cues, Assets, Presentation, Automation
6. Node detail
7. Settings, seven tabs
8. Access
9. Resolume Config
10. Session states: signed out, bootstrap, not found, connecting

The chrome bar's show picker, mode badge, cycle and time to next transition
arrive with the screens that own their data: Shows, Settings › Mode, and Show
Night. The bar renders what the model already carries until then.

`OperatorPageHeader` dies at the first screen. No mock has an eyebrow or a header
button cluster; every mock is `h1` plus one muted subtitle line.

### Routes with no mock

Ruled 2026-08-29 in `OPEN-DECISIONS.md` D-003. Each folds into a mocked screen
rather than getting invented layout.

- **Playlist readiness** folds into the playlist configuration page, not Show
  Night. It is an authoring-time verdict about a playlist.
- **FPP playlist definitions** fold into the same playlist configuration page.
- **Night sessions** are Show Night.
- **Asset manifest** becomes a Monitor facet, amending the guide's four-facet
  list in §3.
- **Top-level `/assets`** stays a rail destination per the guide's §3 Author
  group, rebuilt from the `Show Assets` mock.

## Phase 2: delete the old system and make it unreturnable

Remove the alias block, delete `global.css` and every orphaned old stylesheet
and component, then add a check that fails the build if any file under `ui/src`
references an old token name or imports a stylesheet outside `kit/styles`.
Nothing proves the old design is gone except the old design being deleted and
unreachable.

## Done, per screen

- Blocks and order match the mock.
- Every old control is placed, or listed in `OPEN-DECISIONS.md` for a ruling.
- No `ui/src/styles` import and no old component import remains.
- `document.querySelectorAll('main h2, main h3')` returns the section labels the
  mock names, and they are real headings, not styled spans.
- Every status is a labelled pair. The dashed edge appears only for
  never-collected.
- Every control the principal may not use renders `disabled={true}` with a
  stated reason, and is actually inert.
- Night commands report *accepted*, never *done*.
- New tests cover the screen's absence branches, its scope-denied controls and
  their reasons, the headings assertion, and any 202-wording path. Old view
  tests are deleted with their view.
- `npm run build` and the ui test suite pass.
- Screenshot check at 1280 in dark, light and contrast. When the dev server is
  not available in the session, the PR records it as unverified rather than
  claiming it.

## Standing rules carried from the design guide

- The four absences stay distinct. The dashed edge means never-collected, only.
- Every status is a labelled pair, a state word plus a colour, never colour alone.
- Anything in `--t-data` or `--t-meta` is a literal identifier and needs a file
  behind it. Check `DESIGN-DECISIONS-AND-API-FACTS.md` before typing a mono string.
- A control the principal may not use renders disabled with a stated reason,
  never hidden.
- A control the API cannot serve is still built, to the shape the mock draws,
  inert, and marked with the kit's `NotWiredBanner` and `NotWired` (D-010).
  Data the coordinator never reported is still never invented: the element is
  drawn and its unknown entries are marked unreported.
- Night commands answer 202. The UI says accepted, never done.
- Rail badges are attention counts. Tab counts are inventory counts.
- Signed-out and bootstrap are bands in the document flow, not a full-screen
  login page.
- A poll or refresh failure retains the last known state and says when it was
  read. It never blanks the region it was refreshing.
- No responsive pass below 1100px. Do not solve it by re-enabling bar wrap.
