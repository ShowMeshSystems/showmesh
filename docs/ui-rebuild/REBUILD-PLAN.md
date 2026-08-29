# Operator UI rebuild plan

Status: active. Started 2026-08-29 on `feature/operator-ui-overhaul-2`.

Normative sources, in this order:

1. `docs/design_handoff_operator_ui_overhaul/UI-DESIGN-GUIDE.md` (the rules)
2. `docs/design_handoff_operator_ui_overhaul/design/ShowMesh Design System.dc.html` (the element vocabulary)
3. `docs/design_handoff_operator_ui_overhaul/design/*.dc.html` (the fifteen screen mocks)
4. `docs/design_handoff_operator_ui_overhaul/DESIGN-DECISIONS-AND-API-FACTS.md` (verified identifiers)

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

## Ground rules

- Base on `feature/operator-ui-overhaul-2`. Do not merge or rebase `main` into
  this branch; it is deliberately behind.
- The rebuild is confined to `ui/src`. The coordinator and `api/openapi.yaml`
  work already on this branch stays.
- One PR per seam. Phase 0 is one seam. Each screen after it is one seam.
- No old design element survives. If a rebuilt screen still imports an old
  stylesheet or component, the seam is not done.
- Anything with no home in a mock goes to `OPEN-DECISIONS.md` for Eric, not into
  the page on my judgment.

## Phase 0: the element kit (this seam)

Build every element the design-system specimen demonstrates as a real component
plus a real CSS class, and a `/_specimen` route that renders the whole kit in
all three themes.

Kit contents, from the specimen's nine sections:

| # | Element | Notes |
|---|---|---|
| 01 | Four surface depths, hairline dividers | `--sunken` `--bg` `--surface` `--raised`, `--border` / `--border-strong` |
| 02 | Accent ramp, five stops, real interaction states | no `brightness()` filter hover |
| 03 | Status pair: glyph + word + fill, four tones | dashed edge on unknown only |
| 04 | Seven type roles as utility classes | letter-spacing and text-transform bundled |
| 05 | Buttons (primary, secondary, quiet, danger, disabled-with-reason), text field, select, checkbox, radio, segmented control | 30 / 34 / 48px heights |
| 06 | Ruled strip (158px mono label column) and blanking plate (76px hatched gutter, copy on clean surface) | the two state blocks |
| 07 | Chrome bar and 212px rail, with current / hover / focus / unavailable states and attention badges | grid `212px minmax(0,1fr)` |
| 08 | Evidence table: dense rows, tabular numerals, local overflow | freshness rides in the row |
| 09 | Callout, eyebrow, definition strip, vertical rule | supporting pieces the mocks reuse |

The specimen route is the acceptance gate for the visual language. It is also
what makes adding a missing element safe: a new element is composed from kit
parts, or it does not ship.

## Phase 1: screens, one seam each

Fixed procedure. No deviation.

1. Extract the mock's block list and order. That becomes the page's block list
   and order, exactly. No prepended header, no appended section.
2. Inventory every control on the current built page and assign each to a mock
   block.
3. Anything with no home goes to `OPEN-DECISIONS.md` with options and a
   recommendation. Do not guess.
4. Write the page as a new file composed from the kit. Delete the old view, its
   stylesheet, and its now-invalid tests.
5. Verify against the mock at 1280 in dark, light and contrast.

Order:

1. Dashboard
2. Live Control
3. Show Night
4. Monitor and its four facets (Fleet, Signals, Activity, Capabilities)
5. Shows workspace, five tabs (Playlists, Cues, Assets, Presentation, Automation)
6. Node detail
7. Settings, seven tabs
8. Access
9. Resolume Config
10. Session States (signed out, bootstrap, not found, connecting)

## Phase 2: delete the old system

Remove every orphaned old stylesheet and component, then add a check that fails
the build if any file references an old token name or an old stylesheet. The old
design is gone only when it is unreachable and a test says so.

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
