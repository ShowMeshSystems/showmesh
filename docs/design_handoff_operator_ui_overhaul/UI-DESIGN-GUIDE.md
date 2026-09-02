# ShowMesh Operator UI: design guide for implementers

**Audience:** an agent or engineer writing real code in `ui/src`, either changing a screen the
rebuild delivered or adding a new one. **This document is normative.** It states the rules as the
code holds them, not the history. For *why* a decision was made, see
`DESIGN-DECISIONS-AND-API-FACTS.md` (the design-session record) alongside it, and
`docs/ui-rebuild/OPEN-DECISIONS.md` for the owner's rulings, which amend this file.

**Where it lives:** `docs/design_handoff_operator_ui_overhaul/UI-DESIGN-GUIDE.md`, referenced from
`AGENTS.md` and `CLAUDE.md` so it is loaded before any UI work.

Rewritten 2026-09-01 from the code on `feature/operator-ui-overhaul-2`. Every value below was read
out of the file named beside it.

---

## 0. How to use the mocks

Twenty standalone `.dc.html` files sit in `design/`, nineteen screens plus the design-system
specimen, with four revised copies in `design/revision1/`. They are **visual specifications, not code to port**:

- They use **inline styles** on every element. That is a constraint of the mock format (it must
  paint while streaming), **not a rule for this codebase.** In `ui/src`, compose from the kit in
  `ui/src/kit` and its stylesheets in `ui/src/kit/styles`. Lift the *values*, never the `style=`
  attributes.
- They are **static**. Every state they show (selected row, expanded form, error banner) is a real
  state the model must drive. The mock's tweak props name the variants that matter: Session States
  has `state` (signed-out / bootstrap / not-found / connecting) and `signInError`
  (none / proxy / rate-limit), and those are the branches to implement, not decoration.
- Numbers in them are **one coherent scenario** at one instant (21:07 on 2026-08-28). They are
  illustrative. Do not ship them as fixtures without checking the arithmetic still closes, and never
  ship a mock's invented show or asset name as example copy.

The guide wins a tie. The mocks are the pixel tie-breaker where the guide is silent.

---

## 1. Tokens

Defined once on `:root` in `ui/src/kit/styles/tokens.css`, overridden under `[data-theme='light']`
and `[data-theme='contrast']`. The theme selectors are unprefixed, so a nested element can scope a
theme; the specimen page uses that to preview all three. Never hard-code a colour in a component.

| Token | Dark | Light | Contrast |
|---|---|---|---|
| `--bg` | `#080c0e` | `#f2f5f4` | `#000000` |
| `--surface` | `#0c1215` | `#ffffff` | `#000000` |
| `--raised` | `#111a1d` | `#ffffff` | `#0d0d0d` |
| `--sunken` | `#050809` | `#e8edeb` | `#000000` |
| `--border` | `#26343a` | `#ccd7d4` | `#ffffff` |
| `--border-strong` | `#3b4d54` | `#a5b5b1` | `#ffffff` |
| `--text` | `#eef5f3` | `#101a1a` | `#ffffff` |
| `--text-muted` | `#a5b5b9` | `#43514f` | `#e6e6e6` |
| `--text-faint` | `#96a5a2` | `#4f5d5a` | `#cfcfcf` |
| `--hatch` | `#26343a` | `#d4dedb` | `#4d4d4d` |

Accent is hue 181, seven stops:

```
--accent          oklch(0.845 0.112 181)   rest: primary action, links, current
--accent-hover    oklch(0.9   0.115 181)
--accent-active   oklch(0.76  0.105 181)
--accent-bg       oklch(0.26  0.045 181)   selected row / active tab wash
--accent-border   oklch(0.42  0.075 181)   quiet edges in accent context
--on-accent       #05191a
--focus           oklch(0.88  0.09  181)
```

Light: accent `oklch(0.5 0.09 181)`, hover `oklch(0.44 0.09 181)`, active `oklch(0.38 0.08 181)`,
bg `oklch(0.958 0.024 181)`, border `oklch(0.8 0.05 181)`, on-accent `#ffffff`, focus
`oklch(0.52 0.1 181)`.
Contrast: `#66d9ff` / `#9ae7ff` / `#3fc4f0`, bg `#002733`, border `#66d9ff`, on-accent `#000000`,
focus `#ffff00`.

Status triples, matched in lightness and chroma across hues so amber never reads louder than green:

```
--good-fg  oklch(0.82  0.13  158)   --good-bg  oklch(0.255 0.045 158)   --good-border  oklch(0.42 0.07 158)
--warn-fg  oklch(0.845 0.135 80)    --warn-bg  oklch(0.26  0.05  80)    --warn-border  oklch(0.43 0.08 80)
--bad-fg   oklch(0.755 0.145 24)    --bad-bg   oklch(0.26  0.06  24)    --bad-border   oklch(0.43 0.1  24)
--unk-fg   oklch(0.775 0.012 210)   --unk-bg   oklch(0.255 0.012 210)   --unk-border   oklch(0.4  0.014 210)
```

Light: good `oklch(0.44 0.11 158)` / `oklch(0.958 0.03 158)` / `oklch(0.8 0.06 158)`;
warn `oklch(0.47 0.11 70)` / `oklch(0.962 0.035 80)` / `oklch(0.82 0.07 80)`;
bad `oklch(0.47 0.16 25)` / `oklch(0.958 0.03 25)` / `oklch(0.82 0.08 25)`;
unk `oklch(0.42 0.012 210)` / `oklch(0.94 0.005 210)` / `oklch(0.82 0.008 210)`.
Contrast: good `#4dff8f` / `#003d17`, warn `#ffd23f` / `#4a3300`, bad `#ff6b60` / `#4a0000`,
unk `#ffffff` / `#262626`; every border in contrast is the foreground colour.

**Do not lower `--text-faint`.** It carries every 11px uppercase label in the system, and outdoor
night legibility is a product constraint. It was raised twice for contrast.

### Type

**Archivo** (sans) and **JetBrains Mono** (metadata), in `--sans` and `--mono`. Seven roles, defined
as font shorthands in `tokens.css`:

```
--t-display   700 25px/1.15    page titles only
--t-heading   600 20px/1.2     section h2
--t-subhead   600 15px/1.3     field-group h3, card titles
--t-body      400 14px/1.5     prose, labels, controls
--t-small     400 12.5px/1.45  helper, detail, captions
--t-meta      600 11px/1.35    mono: eyebrows, column heads, state words
--t-data      500 13px/1.4     mono: values, ids, timestamps, hashes
```

**Use the class, not the property.** `letter-spacing`, `text-transform` and
`font-variant-numeric` are not part of the font shorthand, so `ui/src/kit/styles/type.css` bundles
them: `.sm-display` (-0.02em), `.sm-heading` (-0.015em), `.sm-subhead`, `.sm-body`, `.sm-small`,
`.sm-meta` (0.09em, uppercase), `.sm-data` (tabular-nums). `.sm-muted` and `.sm-faint` carry the two
de-emphasis colours; `.sm-truncate` is the one-line ellipsis. A literal identifier inside an eyebrow
or a status keeps its own case: `type.css` un-uppercases `.sm-data` in those two contexts.

Mono is an accent, never the body face. **Anything rendered in `--t-data` or `--t-meta` reads as a
literal API fact.** See section 7.

### Spacing, radii, controls

4px rhythm as `--s-1` through `--s-7`: 4, 8, 12, 16, 24, 32, 48. Radii `--r-chip` 3px (chips and
inner blocks), `--r-panel` 4px (nested panels), `--r-card` 5px (cards and controls).

Control heights: `--ctrl-h` 34px default, `--ctrl-h-compact` 30px, `--ctrl-h-gloved` 48px, `--row-h`
34px. Gloved is for anything touched with gloves: transport, lifecycle commands, macro Run, popover
options, and every field on a sign-in or bootstrap form (they get used on a phone in the cold).
`[data-density='compact']` lowers `--ctrl-h` and `--row-h` to 30px; it is a density axis, not a
theme, and it ships in the kit with no UI switch (D-001).

---

## 2. Layout and chrome

**Global bar** (`.sm-chrome` in `ui/src/kit/styles/shell.css`), on every authenticated screen and
every session state: `position: sticky; top: 0; z-index: 30`, height `--chrome-h` (46px), with the
3px `role="progressbar"` strip sticky beneath it at `top: var(--chrome-h)`. The two together are the
49px the rail offsets against; the rail is sticky at `calc(var(--chrome-h) + 1px)`.

Contents, left to right: brand mark, show picker, mode badge, now-playing (item, state), connection,
principal. The show picker and the mode badge open popovers rather than navigating (section 6).

**The bar must not wrap.** `flex-wrap: nowrap; overflow: hidden`; the now-playing group is
`flex: 1 1 auto; min-width: 0` and truncates with an ellipsis; everything else is `flex: 0 0 auto`.
If it wraps its height changes and the rail's offset puts the first nav group behind it. Fix the
wrap, never the offset.

**One page width, no exceptions.** `.sm-shell-body` is
`grid-template-columns: var(--rail-w) minmax(0, 1fr)` with the rail fixed at 212px, and `.sm-main`
is `max-width: var(--page-max)`, 1200px, set once in `shell.css`. **A screen never overrides the
page width.** Every per-page `.sm-main` cap was deleted in the 2026-09-01 round-three pass, along
with the stylesheet that used `!important` to fight them; the `.sm-main:has(...)` rules that remain
in `blocks.css` adjust padding only. Access is included in the rule: it has no cap of its own.

**Inspectors float, they do not take a column.** The old `[data-panes]` two-column grid is gone.
`.sm-panes` is `display: block`, and `Panes`' `aside` child renders inside the kit `Drawer`
(D-021, D-022). `drawer.css`: fixed to the right under the chrome, full remaining height, scrim at
`z-index: 40` and panel at 41, `width: max-content` capped at 720px for `'content'` and a flat 960px
for `'wide'`, clamped to `100vw - var(--rail-w)` below 1100px and to `100vw` below 720px. Escape
closes it, focus moves to the first focusable element on open and returns to the opener on close,
and `aria-modal="false"` because the page behind stays readable.

**Tables scroll locally and never give the page horizontal scroll.** `.sm-table-wrap` is
`overflow-x: auto` with `overscroll-behavior-inline: contain`; `.sm-table` carries
`min-width: var(--sm-table-min-width, 520px)`, which `Table`'s `minWidth` prop sets per table. Keep
that minimum low enough to fit inside a drawer as well as the page.

**No responsive pass below 1100px exists yet.** The drawer and a handful of block rules have
narrow-viewport clauses; the shell does not. A 390px phone pass is open work, and it must be solved
without re-enabling wrap in the bar.

---

## 3. Information architecture

Seven rail destinations (`ui/src/app/Layout.tsx`). Do not add an eighth without deleting one.

```
Operate   Dashboard · Show Night · Live Control (Resolume as its one sub-link)
Author    Shows · Assets
System    Monitor · Settings
```

- **Monitor** has five facets: **Fleet · Signals · Activity · Capabilities · Manifest**. They are
  organised by axis (by resource, by observation, by event, by capability, by asset manifest), not
  by resource type. Nodes, FPP and Resolume are rows in one Fleet table with Kind as a column.
  Events and audit are one Activity stream (audit rows need an audit-read scope; system events do
  not). Fleet, Signals and Capabilities carry a count; Activity and Manifest do not.
- **Shows** owns a six-tab workspace: **Playlists · Cues · Assets · Presentation · Automation ·
  Night session.** No tab navigates out of the show.
- **Settings** has seven tabs: Connections · Content delivery · Render recovery · Appearance · Audio
  defaults · Node routing · Mode, plus the Resolume configuration routes beneath it. Access sits in
  the same tab row as a leaving link to `/access`, outside the seven and off the rail.

**Rail badges are attention counts, never inventory counts.** A badge means an operator has
something to do. A signed-out or not-yet-read device shows **no badges at all**: an attention count
needs a read, and rendering one without evidence invents it.

**Tab counts are inventory**, of the tab's *primary* object. Automation counts macros (an action
never runs on its own in a show).

---

## 4. The four absences, the most important rule here

This is the rule most often broken, and the one that makes the product trustworthy.

| State | Meaning | Treatment |
|---|---|---|
| **Stale** | Reported before, not recently. Has a last-observed time. | `--warn-fg` label, solid edge |
| **Unavailable** | The source does not support this field. Nothing to retry. | `--text-faint` label, **solid** `--border-strong` edge |
| **Unobserved** | Never collected. No collector ever returned it. | `--text-faint` label, **dashed** edge |
| **Empty** | Definitively zero. A settled state. | `--text-faint` label, solid edge or none |

**The dashed edge is reserved for never-collected.** An empty list, a stale value and an unsupported
field are all settled facts and must not borrow the shape of absent evidence. A settled state that
has not happened yet is the kit's `pending` tone, not a dashed edge.

Every status is a **labelled pair**: a state *word* plus a colour, never colour alone.

Two more absences that map onto this table and are easy to get wrong:

- **`unknown`** on a binding check, and `scopesState: 'unknown'`, mean *the check could not be
  performed*. Never a soft `ok`, and never rendered as `broken`. Treat an unknown scope list exactly
  like an empty one: **unknown is never permissive.**
- **`unconfirmable`** on a macro-run step is **unavailable**, not a failure: an action that expects
  no response reports it on every run, forever. Say so at *authoring* time so the run history is
  pre-explained.

### The two state blocks

- **Ruled strip** (`RuledStrip`), the default. A fixed mono state lane on the left, fact and action
  on the right, hairline top and bottom. Sits in the row or field where the content would have been.
  A chip in that lane is `width: max-content` and never wraps; prose there wraps instead of pushing
  into the fact.
- **Blanking plate** (`BlankingPlate`), for a whole region that cannot render. A hatched gutter
  (`repeating-linear-gradient(135deg, transparent 0 6px, var(--hatch) 6px 12px)`) with a stamp, then
  copy on clean surface, never behind the hatch: eyebrow, `h2`, explanation, recovery actions. The
  stamp's border carries the absence class, dashed for unobserved and solid otherwise. A plate
  replaces the region it covers; it never renders on top of content that is still drawn underneath.

Rejected, do not reinvent: a grid of rounded cards with coloured left borders. It made *absence of
data* look like *a card containing data*.

---

## 5. Copy rules

- **Fact first, at value weight. Caveat second, at helper size.** Never the reverse.
- Label the outcome of a choice, not the field. "Program output group", not "Audio route selection
  mode".
- Helper text earns its line or it goes. If it repeats the label, delete it. **A helper line is one
  sentence.**
- **Never explain the architecture next to an input.** No namespace, ownership or storage-model
  prose in a page lede or beside a field; ADR reasoning belongs in docs. The 2026-09-01 copy pass
  removed these from Shows, ShowDraft, Show Night, Settings and Node detail.
- **No fabricated example names.** Invented show, playlist or asset names from the mocks are not
  example copy. A made-up number has no honest form at all: drop it rather than stamping it.
- **One caveat per screen at most, and only where it is load-bearing.** A caveat repeated in two
  places is deleted from the weaker one: Live Control's lede lost its accepted-never-done sentence
  because the lifecycle section already states it.
- Cut a "by design" aside. State the behaviour and its consequence instead.
- State the reason literally, then the action: "Agent did not answer in 5 s. Run discovery again."
- Say what a command *does*, not what it is called: "Arrives mid-show, so this show becomes final
  and the fade waits for it to finish."
- Name the consequence of a destructive action, including what it orphans.
- An empty attention list keeps its caveat: it is the one screen an operator trusts without reading,
  so it says that nothing has asked for them, not that the show looks right.
- Never claim success the server did not report. Night commands answer **202** with no downstream
  confirmation loop: the UI says *accepted*, never *done*.

---

## 6. Component rules

Compose from `ui/src/kit`. If an element the mocks need does not exist there, add it to the kit with
a specimen entry rather than writing a screen-local variant.

- **If it changes a value it is a `<button>`. If it names a section it is a heading.** A styled
  `<span>` or `<p>` is neither. Before calling a screen done, check
  `document.querySelectorAll('main h2, main h3')` returns the section labels you meant.
- **`Section`** owns the section shape: `id` plus `title` render the `h2` the section's
  `aria-labelledby` points at, `eyebrow` is legal above an `h2` and never above the page `h1`,
  `detail` is the muted line under the head, and `aside` sits on the heading row, right-aligned.
  `PageTitle` is the page `h1` and one muted line; nothing goes above or beside it.
- **`Drawer`** is the inspector (section 2). It never renders its own heading: pass `labelledBy` the
  id of a heading inside `children`. `width` is `'content'`, `'wide'`, or an exact pixel number.
- **`Popover`** is the chrome bar's picker (D-020). It portals into `document.body`, because the
  sticky chrome is its own stacking context and clipped it, and is positioned `fixed` from the
  anchor's rect at `z-index: 80` so it clears both the chrome and any drawer. Its options are 44px
  show-time controls, the current one marked. Apply is disabled until the pick differs, and a
  `window.confirm` naming the exact change is the second gate before the write.
- **`LifecycleCommands`** is the one night-lifecycle element, rendered by **both** Show Night and
  Live Control from the same spec builder. Cell order comes from the mock, not from the caller;
  a group with a `title` renders a titled subsection, a group without one renders a flat grid. A
  command's extra option, such as Start night's skip-the-enter-show-lead checkbox, renders as
  `options` **inside that command's own cell**, under its consequence line, never beside the button.
  Each cell is a gloved `Button` plus a one-line consequence, which the disabled reason replaces
  while the command is disabled.
- **`ReorderButtons`** is the one reorderable-row pattern: a `⠿` (U+283F) grab handle in the row's
  index column, then icon buttons for up, down and remove, each with an `aria-label` naming the row
  ("Move step 2 up"). No full-text Move up / Move down / Remove buttons. A defined reason prop
  disables the button and states why.
- **Inline status chips** (`StatusPair`, `.sm-status`) are `inline-flex` with
  `vertical-align: middle` and `line-height: 1` so they sit on the text baseline, and
  `white-space: nowrap`. A chip in a ruled lane is `width: max-content`. **A chip never repeats the
  name of the row it sits in.** Where the surrounding row already supplies the treatment, use
  `appearance="word"` rather than a second chip.
- **Button rows** are `ButtonRow`: one primary first, and a destructive control at the end of the
  same row at the same height, separated by `ButtonRule`, never stacked on its own line. Do not
  leave a single orphan button on a line of its own where it belongs to the row above it.
- **A control the principal may not use renders disabled with a stated reason, never hidden.**
  `.sm-btn:disabled` keeps a visible edge; **a quiet button stays borderless when disabled**, colour
  alone signalling it. The two documented exceptions to disable-rather-than-hide are the
  "New action" and "New macro" links, hidden outright without `config:write`.
- **`NotWiredBanner` and `NotWired`** mark a control the API cannot serve (D-010): built to the
  shape the mock draws, forced inert, and labelled. `missing` is a `ReactNode` and the banner does
  not wrap it, so pass `<code className="sm-data">POST /cues/{'{id}'}/fire</code>` for a known path
  and plain prose when there is none. Only a **missing endpoint** earns this treatment: a control
  inert for any other reason is an ordinary disabled control with its reason beside it. There is no
  `PlannedFeature` component in `ui/src`; these two are the mechanism.
- Segmented controls need `aria-pressed`. Selected rows need `aria-current`. Disclosure buttons need
  `aria-expanded`.
- Read gates that accept either of two scopes must say which is missing, and must not gate content
  that a different scope already permits.
- A poll or refresh failure **retains the last known state and says when it was read**. It never
  blanks the region it was refreshing.
- Prefer flex or grid with `gap` over margins for sibling groups.
- Focus is always visible: `:focus-visible { outline: 2px solid var(--focus); outline-offset: 2px }`.

### Session states are not walls

`GET /session` answers **200 with `authenticated: false`**: being signed out is a persistent,
readable state, not an error. Signed-out and bootstrap-required render as **bands in the document
flow that push content down**; the chrome and rail stay, and the main region carries a blanking
plate. **Do not build a full-screen login page.** `bootstrapRequired` outranks signed-out and is
computed whether or not the request authenticated. Not being able to read something is never the
same fact as that thing being absent, stopped or empty: the chrome bar says so explicitly while
signed out rather than reporting a stopped show.

---

## 7. Facts you may not invent

Anything in `--t-data` or `--t-meta` is a literal identifier and needs a file behind it. Fabricated
capability IDs, enum members and config field names were the single most expensive defect class in
the design round: three times, when the real value was one grep away.

Sources of truth: `pkg/capability/id.go`, `api/openapi.yaml`,
`ui/src/api/generated/schema.d.ts`, and the screen module that owns the field under `ui/src/screens`
(`showsModel.ts`, `monitorModel.ts`, `liveControlModel.ts`, `settingsModel.ts`, `resolumeModel.ts`,
`accessModel.ts`).

`DESIGN-DECISIONS-AND-API-FACTS.md` section 6 holds the verified inventory as of 2026-08-28:
capability IDs and which are withdrawn, scopes, night commands and their status codes, render and
audio settings fields and ranges, Resolume signals and action names, cue, playlist and surface
constraints, the four action integrations, macro step policy enums and defaults, binding-check
states, and macro-run state and outcome vocabularies. **Read it before typing a mono string.** If a
fact is not on that list, grep for it; if it does not exist, show the manual path as live and the
picker as a labelled future state. An empty `<select>`, or a picker fed by a field the API does not
return, is worse than a text input.

`docs/ui-rebuild/HANDOFF.md` records the places where a mock and the coordinator disagree and the
coordinator won. Follow the code, never the drawing and never the published description.

### Derive, do not ask

Where the server requires two values to agree, show one and compute the other as visible evidence.
Established cases, keep them: surface channel count as `32 × 32 × 4 = 4,096`; FPP entry position
from row order; the LTC route mirroring the program route; macro readiness rolled up from its steps'
binding checks; a macro's consequence rolled up from its steps' safety classes; an action's safety
class derived from its integration's registered table. Each removes a whole class of save-time
refusal.

---

## 8. Screen map

Routes as `ui/src/app/App.tsx` defines them.

| Route | Screen | Notes |
|---|---|---|
| `/` | `Dashboard` | Readiness, Needs you, System health |
| `/night` | `ShowNight` | Night lifecycle, one `LifecycleCommands` element |
| `/control` | `LiveControl` | Transport, outputs, Resolume, night lifecycle, audio sessions, macros, announcements, actions |
| `/control/resolume` | `ResolumeControl` | The rail's one sub-link |
| `/monitor` | redirect | to `/monitor/fleet` |
| `/monitor/fleet` | `Monitor` | Nodes, FPP and Resolume in one table with Kind as a column |
| `/monitor/fleet/node/:nodeId` | `Monitor` with the node drawer open | 960px `Drawer`; deep links work, closing returns to Fleet |
| `/monitor/fleet/fpp/:instanceId` | redirect | to `/monitor/fleet?resource=fpp:<id>` |
| `/monitor/fleet/resolume`, `/monitor/fleet/resolume/:instanceId` | redirect | to the Settings Resolume routes |
| `/monitor/signals` | `MonitorSignals` | |
| `/monitor/activity` | `MonitorActivity` | Events and audit in one stream |
| `/monitor/capabilities` | `MonitorCapabilities` | |
| `/monitor/manifest` | `MonitorManifest` | |
| `/shows` | `Shows` | Show list |
| `/shows/new` | `ShowDraft` | Creation pattern |
| `/shows/:id` | `ShowDetail` | The show's identity page |
| `/shows/:id/playlists` `…/cues` `…/assets` `…/presentation` `…/automation` `…/night-session` | the six tabs, inside `ShowsWorkspace` | No tab navigates out of the show |
| `/assets` | `Assets` | The library, a rail destination (D-003) |
| `/settings` | `Settings` | Indexes to `/settings/connections` |
| `/settings/connections` `…/delivery` `…/recovery` `…/appearance` `…/audio-defaults` `…/node-routing` `…/mode` | the seven tabs | |
| `/settings/resolume`, `/settings/resolume/:instanceId` | `ResolumeConfig` | The index redirects to the first instance, or states that none is configured |
| `/access` | `Access` | List plus a principal drawer, including the D-019 Administration group |
| `/_specimen` | `Specimen` | Every token, state and control in three themes |
| `*` | `NotFound` | |

Old addresses are **not redirected**, apart from the redirects listed above. The not-found page
is the migration guide: it names the address, says the show is running normally, and maps the old
address to its new home.

---

## 9. Pre-PR checklist

### Defect classes that recurred

1. **Invented API identifiers.** Every mono string traced to a file (section 7).
2. **Numbers that do not reconcile.** Derive every displayed number from a value already on screen
   or in a file, and check the arithmetic closes, including across screens.
3. **Cross-screen contradictions.** The UI is one installation at one instant. A fact stated twice
   is stated the same way. Never attribute a ShowMesh-side problem to the resource (a binding
   failure is not FPP's health).
4. **The four absences blurred.** Re-read section 4 before writing any state label. Dashed edge
   means never-collected, only.
5. **Headings authored as `<span>` or `<p>`.** This happened four times *after* the rule was
   written.
6. **Fake pickers.** No empty `<select>`; no picker fed by a field the API does not return.
7. **Bare boolean attributes.** In the mocks, `disabled` without a value compiled to a button that
   *looked* unavailable and was fully clickable, and it shipped twice. In TSX this is
   `disabled={true}`; still verify the rendered control is actually inert and that its reason is
   stated.

### Defect classes that pass jsdom and fail in a browser

The 2026-08-29 and 2026-09-01 review sessions found five of these. Each passed the unit suite.

1. **A blanking plate rendered over the rail** with the dashboard still drawn underneath, so the
   page asserted "nothing collected" and showed data at the same time. jsdom does no layout.
2. **Route parameter names that disagree with what the view reads.** Every test file defined its own
   route pattern, so the mismatch was invisible to all of them.
3. **Per-page width overrides**, including one stylesheet using `!important` to beat another.
   Nothing in the suite asserts a computed width.
4. **Unscoped CSS `order` rules on shared elements**, which reordered controls on screens that were
   never the rule's target.
5. **An empty-string playlist name that skipped a `??` fallback**, so the field rendered blank
   instead of saying no playlist was reported. `??` catches null and undefined, never `''`.

**So the rule is: run the screen in a browser against a real coordinator before calling it done.**
Playwright against the running app, not jsdom alone.

- Sign in by injecting the token into `sessionStorage['showmesh.apiToken']` with `addInitScript`,
  before the first navigation.
- Wait on `{ waitUntil: 'load' }` plus an explicit wait for the content. **`networkidle` never fires
  here**: the model holds an open SSE stream for the life of the page.
- `ui/package.json` declares a `visual:review` script for this. Its runner,
  `ui/scripts/capture-visual.mjs`, is not committed, so write or restore it before relying on the
  script name.
- Check at 1280 in dark, light and contrast. When the browser cannot be driven in the session,
  record the check as unverified rather than claiming it.

A screenshot of the fixture coordinator is never hardware, deployment or live-show evidence. Say so
in the PR. `docs/ui-rebuild/HANDOFF.md` holds the working instructions for both the fixture and a
real coordinator, including the traps that cost time.

---

## 10. Deliberately not designed

Do not invent these; state their absence instead. Checked against the code on 2026-09-01.

1. **Installation-wide emergency stop.** The owner's spec is a top-bar control, instant, no
   confirmation dialog, double-press to arm. It is not mocked, because the coordinator advertises no
   such capability, and it is held for the chrome bar until the responsive pass. Live Control offers
   no emergency-stop control today; its "Stop now" helper line says that the control halts this
   player only, and Resolume blackout is the separate, real, per-instance path.
2. **390px phone pass.** Every screen assumes the fixed 212px rail; at very narrow widths the bar
   clips its right-hand group. The drawer has narrow-viewport clauses at 1100px and 720px; the shell
   has none. Solve it without re-enabling wrap in the bar.
3. **Mode to mismatch-policy wiring.** Settings › Mode and Shows › Playlists both state that
   mismatch handling is expected to follow Show versus Program mode and that the wiring does not
   exist, so the per-playlist control is disabled and the stored policy is what takes effect today
   (D-015).
4. **Output groups.** Settings › Node routing draws the picker as a labelled future state pending an
   `outputGroups` attribute on the `audio.output.local` capability; the manual channel field above
   it is the live path, and nothing sends `outputGroups`.
5. **Clock domain evidence.** Settings › Node routing takes a manual `clockDomain` declaration with
   its `clockDomainProvenance` recorded beside it. Real evidence is separate work.
6. **Fallback program authoring.** Monitor › Fleet shows read-only fallback-program readiness
   evidence for an FPP host. There is no control that writes or acknowledges one.

Audio action authoring is no longer on this list: Shows › Automation offers `integration: 'audio'`
in both the new-action and edit-action forms, with its own target editor.
