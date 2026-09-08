# Handoff: ShowMesh Operator UI overhaul

## Overview

A full visual and structural overhaul of the ShowMesh operator UI — the browser interface an operator
uses to run a large residential light-and-sound show (FPP sequencers, Resolume video, multi-channel
audio nodes, LTC timecode) from a phone or tablet, outdoors, at night, often wearing gloves.

Two problems drove it:

1. **Navigation.** 25 links crammed into a 3-group rail, split into "primary" and "secondary" where
   secondary was an unlabelled overflow bin holding 18 destinations. Nodes, FPP, Resolume, Events,
   Capabilities and Observations were six separate destinations answering one question.
2. **Density and verbosity.** Show Night stacked fourteen panels, several of which were a badge and a
   sentence. Every one of Live Control's ~25 controls repeated the same three-sentence caveat. Show
   authoring was spread across five global routes reached by tabs that navigated *out* of the show.

The overhaul delivers seven rail destinations, a persistent global bar carrying now-playing, tabs
*inside* consolidated pages, a rebuilt token layer with three verified-contrast themes, and a
disciplined vocabulary for the four different kinds of missing data that this product has to
distinguish.

Sixteen design files are included: fourteen screens, the session/error states, and a design-system
specimen page.

---

## About the design files

The files in `design/` are **design references created in HTML** — prototypes showing intended look
and behavior. **They are not production code to copy.**

The task is to **recreate these designs inside the existing `ui/src` codebase** (React 18 +
TypeScript + Vite + react-router, plain CSS in `src/styles/`), using its established patterns:
`ModelContext` / `useModel` for state, `ScopedButton` for scope-gated actions, the generated
`src/api/generated/schema.d.ts` types, and real CSS classes rather than inline styles.

Two specifics that matter:

- **Every element in the mocks is inline-styled.** That is a constraint of the prototype format (it
  must paint while streaming) and **explicitly not a rule for the codebase.** Lift the *values* —
  hex codes, token names, paddings, heights, radii — into CSS classes in `src/styles/`.
- **The mocks are static.** Every state they show (a selected row, an expanded form, an error
  banner, a broken binding) is a real state the model must drive. Each file's tweak props name the
  variants that were designed and must be implemented — e.g. `Session States.dc.html` has
  `state` (signed-out / bootstrap / not-found / connecting) and `signInError` (none / proxy /
  rate-limit).

To view a mock, open its `.dc.html` file directly in a browser (`support.js` must stay beside them).

---

## Fidelity

**High-fidelity.** Final colors, typography, spacing, control sizes, states and copy. Recreate the
UI to match, using the codebase's own CSS layer. Every token value is listed in
`UI-DESIGN-GUIDE.md` §1 and rendered in all three themes by `design/ShowMesh Design System.dc.html`,
which is the specimen and the tie-breaker for anything ambiguous.

Copy in the mocks is **final copy**, not placeholder. It was written against the copy rules in §5 of
the guide, and several strings (the CSRF/proxy sentence, the rate-limit sentence, the empty-attention
caveat) are deliberate corrections of existing text — use them verbatim.

Numbers in the mocks are one coherent scenario at one instant (**21:07 on 2026-08-28**, Winter Ridge
2026, cycle 3, "Carol of the Bells" at 1:42 / 2:48, `media-garage` offline since 20:41:07). They are
illustrative, not fixtures.

---

## Read these two documents first

| File | What it is |
|---|---|
| `UI-DESIGN-GUIDE.md` | **Normative and self-sufficient.** Tokens, layout, IA, the four absences, state blocks, copy rules, component/a11y rules, screen map, pre-PR checklist, what is deliberately not designed. Suggested repo home: `docs/UI-DESIGN-GUIDE.md`, referenced from `AGENTS.md` / `CLAUDE.md`. |
| `DESIGN-DECISIONS-AND-API-FACTS.md` | The design-session record: why each decision was made, plus **§6, an inventory of every API identifier verified against source** (capability IDs, scopes, night commands and status codes, settings fields and ranges, Resolume signals, cue/playlist/surface constraints, action integrations, macro step enums, binding states, run outcome vocabularies). |

Everything in the sections below is a summary. Where the two disagree with this README, the guide
wins.

---

## Design tokens

Define on `:root`; override under `[data-theme='light']` and `[data-theme='contrast']`. Never
hard-code a color in a component.

### Surfaces and text

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

**Do not lower `--text-faint`.** It carries every 11px uppercase label in the system; outdoor night
legibility is a product constraint. It was raised twice during design for contrast.

### Accent — hue 181, five stops

| Token | Dark | Light | Contrast |
|---|---|---|---|
| `--accent` | `oklch(0.845 0.112 181)` | `oklch(0.50 0.09 181)` | `#66d9ff` |
| `--accent-hover` | `oklch(0.90 0.115 181)` | `oklch(0.44 0.09 181)` | `#9ae7ff` |
| `--accent-active` | `oklch(0.76 0.105 181)` | `oklch(0.38 0.08 181)` | `#3fc4f0` |
| `--accent-bg` | `oklch(0.26 0.045 181)` | `oklch(0.958 0.024 181)` | `#002733` |
| `--accent-border` | `oklch(0.42 0.075 181)` | `oklch(0.80 0.05 181)` | `#66d9ff` |
| `--on-accent` | `#05191a` | `#ffffff` | `#000000` |
| `--focus` | `oklch(0.88 0.09 181)` | `oklch(0.52 0.10 181)` | `#ffff00` |

Usage: `--accent` = primary action, links, current/selected. `--accent-bg` = selected row and active
tab wash. `--accent-border` = quiet edges inside accent context.

### Status — matched lightness and chroma across hues

Dark:

```
--good-fg  oklch(0.82  0.13  158)   --good-bg  oklch(0.255 0.045 158)   --good-border  oklch(0.42 0.07 158)
--warn-fg  oklch(0.845 0.135 80)    --warn-bg  oklch(0.26  0.05  80)    --warn-border  oklch(0.43 0.08 80)
--bad-fg   oklch(0.755 0.145 24)    --bad-bg   oklch(0.26  0.06  24)    --bad-border   oklch(0.43 0.10 24)
--unk-fg   oklch(0.775 0.012 210)   --unk-bg   oklch(0.255 0.012 210)   --unk-border   oklch(0.40 0.014 210)
```

Light: good `oklch(0.44 0.11 158)` / `oklch(0.958 0.030 158)` / `oklch(0.80 0.060 158)`;
warn `oklch(0.47 0.11 70)` / `oklch(0.962 0.035 80)` / `oklch(0.82 0.070 80)`;
bad `oklch(0.47 0.16 25)` / `oklch(0.958 0.030 25)` / `oklch(0.82 0.080 25)`;
unk `oklch(0.42 0.012 210)` / `oklch(0.940 0.005 210)` / `oklch(0.82 0.008 210)`.

Contrast: good `#4dff8f` / `#003d17`, warn `#ffd23f` / `#4a3300`, bad `#ff6b60` / `#4a0000`,
unk `#ffffff` / `#262626`; every border in contrast equals its foreground.

The original build's amber read two steps louder than its green. These pairs were retuned so a
warning and a success carry equal visual weight — do not re-tune one hue alone.

### Typography

Google Fonts: **Archivo** (400, 500, 600, 700) and **JetBrains Mono** (400, 500, 700).

```
--sans: Archivo, ui-sans-serif, system-ui, sans-serif;
--mono: 'JetBrains Mono', ui-monospace, SFMono-Regular, Menlo, monospace;
```

Archivo replaced Inter because it holds shape better at 11px in dense tabular rows. Seven roles,
shipped as custom properties — use the property, never a re-declared stack:

| Token | Value | Use |
|---|---|---|
| `--t-display` | `700 25px/1.15`, `letter-spacing: -0.02em` | page titles only |
| `--t-heading` | `600 20px/1.2`, `letter-spacing: -0.015em` | section `h2` |
| `--t-subhead` | `600 15px/1.3` | field-group `h3`, card titles |
| `--t-body` | `400 14px/1.5` | prose, labels, controls |
| `--t-small` | `400 12.5px/1.45` | helper, detail, captions |
| `--t-meta` | `600 11px/1.35` mono, `letter-spacing: .09em`, uppercase | eyebrows, column heads, state words |
| `--t-data` | `500 13px/1.4` mono, `font-variant-numeric: tabular-nums` | values, ids, timestamps, hashes, dB |

Mono is an accent, never the body face. **Anything rendered in `--t-data` or `--t-meta` reads to an
operator as a literal API fact** — see "Facts you may not invent" below.

### Spacing, radii, control heights

- 4px rhythm: **4 · 8 · 12 · 16 · 24 · 32 · 48**.
- Radii: **3px** chips and inner blocks, **4px** nested panels, **5px** cards and controls.
- Control heights: **30px** compact, **34px** default, **44–48px** for anything touched with gloves
  — transport, night lifecycle commands, macro Run, and every field on a sign-in or bootstrap form.
- Focus ring, globally: `outline: 2px solid var(--focus); outline-offset: 2px` on `:focus-visible`.

---

## Global chrome (every screen)

```css
[data-chrome] { position: sticky; top: 0; z-index: 30; }   /* 49px: 8px pad + 30px content + 3px rule */
[data-rail]   { position: sticky; top: 50px; }
```

**Bar**, `display: flex; align-items: center; gap: 14px; padding: 8px 18px; background: var(--surface)`,
left to right:

1. Brand mark — 24×24 grid, `1px solid var(--accent-border)`, radius 4px, `--t-meta`, accent text, "SM".
2. Show picker — 30px button, `1px solid var(--border-strong)`, radius 5px, an uppercase `--t-meta`
   "Show" eyebrow in `--text-faint` then the show name at `--t-body` weight 500, then a ▾ in
   `--text-faint`. Hover: `border-color: var(--accent)`.
3. Mode badge — 30px, filled `--accent` / `--on-accent`, `--t-meta` uppercase, radius 5px.
4. 1px × 22px `--border` divider.
5. **Now-playing group**, `flex: 1 1 auto; min-width: 0`: an accent dot + "NOW" in `--t-meta`; the
   item title at `--t-body` weight 600 truncating with an ellipsis; position `1:42 / 2:48` in
   `--t-data`; then `cycle 3 · next in 1:06` at `--t-data` 11px `--text-faint`.
6. Right group, `margin-left: auto; flex: 0 0 auto`: connection state (dot + `--t-meta` uppercase in
   `--good-fg` / `--warn-fg` / `--unk-fg`), divider, principal name at `--t-small`.

Beneath the bar, a full-width **3px `role="progressbar"`** on `--sunken` with a `--accent` fill and
`border-bottom: 1px solid var(--border)`; it carries `aria-valuemin/max/now` and a human
`aria-valuetext`. Session states that have no playback render a plain 3px `--sunken` strip with no
`role`, never a zeroed progress bar.

**The bar must not wrap.** `flex-wrap: nowrap; overflow: hidden`; now-playing is the only
`flex: 1 1 auto; min-width: 0` child. If it wraps, its height changes and the rail's 50px offset
puts the first nav group behind it — fix the wrap, never the offset. (The coordinator build string
was moved out of the bar into Settings to pay for this space.)

**Rail** — fixed 212px column: page grid is `grid-template-columns: 212px minmax(0,1fr); align-items: start`.
`padding: 12px 0; border-right: 1px solid var(--border); background: var(--surface); min-height: calc(100vh - 50px)`.
Group labels are `--t-meta` uppercase `--text-faint`, `padding: 0 14px`. Items are 32px min-height
links, `padding: 0 14px`, `border-left: 2px solid transparent`; hover adds
`background: var(--raised)` and `border-left-color: var(--border-strong)`; the current item is
`border-left-color: var(--accent); background: var(--accent-bg); color: var(--accent); font-weight: 600`.

---

## Information architecture

Seven destinations. Do not add an eighth without deleting one.

```
Operate   Dashboard · Show Night · Live Control
Author    Shows · Assets
System    Monitor · Settings
```

Consolidations:

- **Monitor** absorbed Nodes, FPP, Resolume, Events, Capabilities, Observations and Audit into four
  facets — **Fleet · Signals · Activity · Capabilities** — organised by *axis* (by resource, by
  observation, by event, by capability), not by resource type. Nodes/FPP/Resolume are rows in one
  Fleet table with **Kind** as a column. Events and audit are one Activity stream; audit rows need
  an audit-read scope, system events do not.
- **Shows** owns a five-tab workspace: **Playlists · Cues · Assets · Presentation · Automation.**
  No tab navigates out of the show.
- **Settings** has seven tabs in the same horizontal tab language: Connections · Content delivery ·
  Render recovery · Access (leaves the screen — mark it ↗) · Appearance · Audio defaults ·
  Node routing · Mode.

**Rail badges are attention counts, never inventory counts.** A badge means the operator has
something to do. A signed-out or not-yet-read device shows **no badges at all** — an attention count
requires a read, and rendering one without evidence invents it.

**Tab counts are inventory** — of the tab's *primary* object. Automation counts macros, because an
action never runs on its own in a show.

Tab bar: `padding: 0 28px; border-bottom: 1px solid var(--border)`; items 38px high,
`padding: 0 14px`, `border-bottom: 2px solid transparent`, `--text-muted`, with the count beside the
label in `--t-data` 11px `--text-faint`. Active: `border-bottom-color: var(--accent)`, accent text,
weight 600, count in accent. Hover: `color: var(--text); border-bottom-color: var(--border-strong)`.

---

## The four absences — the core rule of this design

The single most important rule, and the one most often broken. Four different kinds of "no value",
four distinct treatments:

| State | Meaning | Treatment |
|---|---|---|
| **Stale** | Reported before, not recently. Has a last-observed time. | `--warn-fg` label, **solid** edge |
| **Unavailable** | The source does not support this field. Nothing to retry. | `--text-faint` label, **solid** `--border-strong` edge |
| **Unobserved** | Never collected. No collector ever returned it. | `--text-faint` label, **dashed** edge |
| **Empty** | Definitively zero. A settled state. | `--text-faint` label, solid edge or none |

**The dashed edge is reserved for never-collected.** An empty list, a stale value and an unsupported
field are all settled facts and must not borrow the shape of absent evidence.

**Every status is a labelled pair** — a state *word* plus a color, never color alone.

Two API states map onto this table and are easy to get wrong:

- **`unknown`** on a binding check, and `scopesState: 'unknown'`, mean *the check could not be
  performed* — never a soft `ok`, never rendered as `broken`. Treat an unknown scope list exactly
  like an empty one: **unknown is never permissive.**
- **`unconfirmable`** on a macro-run step is **unavailable**, not a failure: an action that expects
  no response reports it on every run, forever, by design. Say so at *authoring* time so the run
  history is pre-explained rather than alarming.

### Two state blocks

**Ruled strip** (default, inline) — `display: grid; grid-template-columns: 154px minmax(0,1fr); gap: 14px`,
`padding: 12px 0`, hairline `border-top` / `border-bottom` in `--border`. Left column is the state
word in `--t-meta` uppercase, colored by class. Right column is the fact at `--t-body`, then the
explanation at `--t-small` `--text-muted`, then 30px action buttons. It sits in the row or field
where the content would have been.

**Blanking plate** — for a whole region that cannot render:

```
display: grid; grid-template-columns: 76px minmax(0,1fr);
border: 1px solid var(--border-strong);   /* or --warn-border for a permission plate */
background: var(--surface);
```

The 76px gutter is `place-items: center`, `border-right` matching, and
`background: repeating-linear-gradient(135deg, transparent 0 6px, var(--hatch) 6px 12px)`. Inside it
sits a stamp: `padding: 3px 6px`, `--t-meta` 10px uppercase, `background: var(--surface)`, with a
**dashed `--text-faint` border for unobserved** and a solid border otherwise. The right cell is
`padding: 20px 22px` on clean surface — **never behind the hatch** — carrying an eyebrow
(`--t-meta` uppercase), an `h2` at `--t-heading`, an explanation at `--t-body` `--text-muted`
(`max-width: 64ch`), and 34px recovery buttons.

Rejected, do not reinvent: a grid of rounded cards with colored left borders. It made *absence of
data* look like *a card containing data*.

---

## Screens

Each entry gives purpose, layout and the notable components. **For element-level detail, open the
listed file** — it is the pixel source of truth, and the shared primitives above (chrome, rail, tabs,
tables, state blocks, buttons, fields) account for most of every screen.

### Shared primitives used throughout

- **Card / table container**: `border: 1px solid var(--border); border-radius: 5px; background: var(--surface); overflow: hidden`.
- **Table**: `width: 100%; border-collapse: collapse; font: var(--t-body)`, inside a wrapper with
  `overflow-x: auto; overscroll-behavior-inline: contain`. `min-width` on the table — ~596px with an
  inspector pane, ~1012px without — so a table scrolls locally and **never gives the page horizontal
  scroll**. `thead th`: `padding: 8px 10px`, `background: var(--raised)`,
  `border-bottom: 1px solid var(--border)`, `--t-meta` uppercase `--text-faint`, left-aligned.
  `tbody td`: `padding: 10px`, `border-bottom: 1px solid var(--border)`; first cell pads to 14px on
  the left, last to 14px on the right. Clickable rows: `cursor: pointer`, hover `background: var(--raised)`.
- **Primary button**: 32–34px (48px gloved), `border: 1px solid var(--accent)`, radius 5px,
  `background: var(--accent)`, `color: var(--on-accent)`, `--t-body` weight 600; hover swaps both to
  `--accent-hover`.
- **Secondary button**: same metrics, `border: 1px solid var(--border-strong)`,
  `background: var(--surface)`, `color: var(--text)`, weight 500; hover `border-color: var(--accent)`.
- **Quiet button**: transparent border and background, `--text-muted`; hover `color: var(--text); background: var(--raised)`.
- **Text field**: 32–34px (44px gloved), `padding: 0 10px`, `border: 1px solid var(--border-strong)`,
  radius 5px, `background: var(--surface)`, `--t-body` — or `--t-data` when the value is an
  identifier, timecode or number.
- **Segmented control**: `display: flex; gap: 1px; padding: 1px; background: var(--border); border-radius: 5px; width: max-content`.
  Each option is a `<button>` with `aria-pressed`, `padding: 6px 12px`, radius 4px; selected is
  `--accent` / `--on-accent` weight 600, unselected `--surface` / `--text-muted`.
- **Two-pane screens**: `[data-panes] { display: grid; grid-template-columns: minmax(0,1fr) minmax(320px,420px); align-items: start }`,
  `aside` sticky at `top: 0` with `padding: 20px 28px 0 0`; under `max-width: 1100px` it collapses to
  one column and the aside goes static with symmetric padding.
- **Reorderable rows**: a `⠿` (U+283F) handle in the index cell, `--t-data` `--text-faint`,
  `cursor: grab`, followed by the position number; "Drag to reorder" appears once in the container
  footer.
- **Page header block**: `padding: 20px 28px 0` — breadcrumb at `--t-small` `--text-muted`, then an
  `h1` at `--t-display`, then a metadata line at `--t-small`, with page actions right-aligned in the
  same row.

### 1. `ShowMesh Design System.dc.html` — specimen

**Purpose:** the tie-breaker. Renders every token, type role, control, status pair, state block,
chrome variant and copy before/after, in all three themes. **Read this first and keep it open while
implementing.** Tweaks: `theme`, `density`.

### 2. `Dashboard.dc.html` — `/`

**Purpose:** at a glance, is tonight working, and is anything waiting for me?

Sections: the **now bar** (now-playing with progress in the global chrome), a **lifecycle
double-timeline** (tonight's cycles vs. the current cycle's resting loop — two different clocks,
because one flattened row made a repeating loop look one-way), **readiness split in two**
("Running ✓" and "Next start gated ⚠" — mid-show, a single verdict has to imply something is wrong
with a show that is playing fine), a **needs-you** list, and **system health**.

The empty needs-you state keeps its caveat: *"That is not proof the show looks right — only that
nothing has asked for you."* Tweaks: `attention` (items / clear), `theme`.

### 3. `Show Night.dc.html` — show night

**Purpose:** run the night's lifecycle.

Replaces fourteen stacked panels. Two-row lifecycle timeline, now/next, the **night lifecycle
commands** at gloved size, run of show, and evidence. Lifecycle commands live here even though Live
Control also needs them.

Night commands answer **202, never 200** — accepted, or an idempotent duplicate, with no downstream
confirmation loop. **The UI must never claim success.** Interlocks withhold with
`409 night-not-ready` naming the rule. Tweak: `theme`.

### 4. `Live Control.dc.html` — live control

**Purpose:** direct control during the show.

Transport, per-output detail, night lifecycle, macros, announcements and actions. The ~25 controls no
longer repeat *"Capability: X. Freshness: Y. Permission and outcomes are reported by each command."*
— each control states its own fact once, and only when it differs from the default.

This screen **states the absence of an installation-wide E-Stop** rather than implying one exists
(see "Deliberately not designed"). Tweak: `theme`.

### 5. `Monitor.dc.html` — `/monitor`, four facets

**Purpose:** the state of every resource, on one destination.

Fleet table (Kind as a column), a needs-an-operator block, the activity stream, and a node evidence
pane. **Health is only what the resource reported** — a ShowMesh-side binding problem is a separate
signal and is never rendered as FPP's health. Tweak: `theme`.

### 6. `Node.dc.html` — node detail

Per-node identity, capabilities, surfaces, local assets, and removal (whose copy names what the
removal orphans). Tweak: `theme`.

### 7. `Resolume Config.dc.html` — Resolume configuration

Stored composition, **ambiguous clips**, recovery, observations.

The key idea: **clips that cannot be named.** Resolume actions reference clips by name; two clips
sharing a name on the same layer and deck are unresolvable and the coordinator refuses to guess. The
panel names the offenders with clip id and column so "rename one" is actionable, and the recovery
report shows the same defect biting again as a skipped layer. Tweak: `theme`.

### 8. `Shows.dc.html` — `/shows`

Show list and show details. Tweaks: `view` (list / detail), `theme`.

### 9. `Show Authoring.dc.html` — workspace › Playlists

FPP bindings vs. authored audio order. Two runners exist and behave differently, so the table splits:
`fpp` (imported entries you bind to cues; you cannot reorder — it mirrors FPP) and `showmesh-audio`
(an authored, reorderable list with `⠿` handles).

**FPP imports are reconciled, not typed.** Entry position comes from row order rather than a number
field. Tweaks: `runner` (fpp / showmesh-audio), `theme`.

### 10. `Show Cues.dc.html` — workspace › Cues

Cue library grouped by reachability — *In a playlist* / *Not in any playlist* (authored but
unreachable) / *Directly activatable* (announcements, fired from Live Control) — plus a two-pane
new-cue composer.

**Cues are shared across playlists**, so the inspector warns: "Editing a cue changes every playlist
that uses it." The composer derives the cue id from the name, offers the four outputs as checkboxes
with at least one required, and closes with a plain-language summary of what activation will do.
Tweak: `theme`.

### 11. `Show Assets.dc.html` — workspace › Assets

Assets grouped by **logical sequence**, because one xLights sequence produces a different file per
target, all with the same filename — so filename belongs to the group and the ambiguity becomes the
explanation instead of the confusion.

**Rollback is a first-class flow:** re-uploading superseded bytes makes that version current again.
History reads as *events* because a row can become current more than once, and the upload pane says
"This will be a rollback" *before* you commit, with the button relabelled. Tweaks: `pane`
(history / upload), `theme`.

### 12. `Show Presentation.dc.html` — workspace › Presentation

Surfaces, the **virtual matrix map**, and derived channel count.

Two derivations to keep: channel count is shown as `32 × 32 × 4 = 4,096` rather than typed, and the
matrix map is one band across 32,768 channels showing each surface's range plus the unallocated
remainder, with an explicit **"No overlaps"** verdict. Overlapping ranges are a real authoring error
that three separate forms cannot reveal. Tweak: `theme`.

### 13. `Show Automation.dc.html` — workspace › Automation

**Purpose:** author and fire macros; keep their steps' targets honest.

Layout: two-pane. Left, top to bottom — filter + kind chips + New action / New macro; two lines of
orientation; a **Needs you** ruled strip; a **Macros** section where each macro is a card (header
with name, id, revision, step count, a derived readiness roll-up chip and a 44px **Run**; a
consequence line; a ruled step list; a footer with the last run or "Never run"); then an **Actions**
section split *In a macro* / *Not in a macro*. Right, the inspector: either the step editor or a run
detail.

Three ideas to preserve:

- **Macro readiness is derived from its steps' binding checks.** The binding sweep needs no
  credential and runs continuously, so the UI answers "if I press this, will it work?" *without
  firing the macro*. Roll it up on the header (`4 of 4 bindings ok` / `Step 2 broken`) and repeat it
  on the step. The designed case is the macro that has *never been run* and whose step 2 clip is
  ambiguous — the sweep is the only thing that could have told you.
- **A macro's consequence is derived from its steps' safety classes.** A macro containing a `stop`
  step says "running this ends the current playlist" — the operator learns what the button does from
  what it is made of, not from its name.
- **Unconfirmable is stated at authoring time**, not discovered in run history.

Step rows show a policy line **only when the step deviates from the default** (both `onFailure` and
`onUnconfirmed` default to `continue`). Selection is explicit: `aria-current`, an `--accent-bg` wash,
and a **▸ Editing** chip, with the defect's red left bar and `BROKEN` state word still reading
separately. The inspector ends with a derived preview of what a run will actually do tonight.
Tweaks: `pane` (step / run), `canAuthor`, `theme`.

### 14. `Settings.dc.html` — `/settings`, seven tabs

Seven settings pages in one destination. Field names, enums and ranges are all verified — see
`DESIGN-DECISIONS-AND-API-FACTS.md` §6 before touching any of them. Tweaks: `page` (7 values),
`theme`.

### 15. `Access.dc.html` — `/access`

Principals, scopes, credentials, attribution. Device labels from sign-in appear here, which is how an
administrator revokes one session without touching the others. Tweak: `theme`.

### 16. `Session States.dc.html` — signed-out / bootstrap / not-found / first-connect

**Purpose:** the four states with no usable session.

**The load-bearing rule: signed out is a state, not a wall.** `GET /session` answers **200 with
`authenticated: false`** — being signed out is a persistent, readable state, not an error a caller
must catch. So all four states **keep the chrome and the rail**; the sign-in and bootstrap-claim
forms are **bands in the document flow that push content down, never modals**, and the main region
carries a blanking plate. **Do not build a full-screen login page.**

- **signed-out** — a `role="status"` band (`h1` "Signed out on this device", `--raised`) with
  Sign in / Use a token instead / Clear stored token; an expanded sign-in form (Name, Password,
  This device's name — all required, all 44px, submit 48px); then an **unobserved** blanking plate
  (dashed stamp) reading "Nothing here has ever been collected". The rail shows **no badges**.
- **bootstrap** — a `role="alert"` band with a `--bad-fg` left rule: "No administrator exists on this
  coordinator", the four-field claim form, and an **empty** blanking plate (solid stamp) reading
  "No shows, no nodes, no principals". Signed-out is *unobserved*; an unclaimed coordinator is
  *empty*. That pairing is the clearest demonstration of the four-absences rule in the system.
- **not-found** — full live chrome (the show is running; the bar above is the proof). Names the
  requested path in `--t-data` on `--sunken`, then **"Where it probably went"**: a table mapping old
  address → new destination. Eighteen routes collapsed into seven, so a 404 is usually an old
  bookmark, not a typo. It says plainly that old addresses are **not** redirected.
- **connecting** — no band (the session state is still `loading`, so the panel renders nothing).
  Two ruled strips (Session, Live updates) and the line "Nothing below is stale, because nothing has
  been read yet." No spinner, no zeroed numbers.

Tweaks: `state` (4 values), `signInError` (none / proxy / rate-limit), `theme`.

---

## Interactions & behavior

- **Navigation.** Rail selects a destination; tabs select a page *within* it and never navigate out.
  Settings › Access is the one tab that leaves the screen and is marked ↗.
- **Two-pane selection.** Clicking a row loads it into the inspector. The selected row carries
  `aria-current`, an `--accent-bg` wash and (where a row can be both selected and defective) an
  explicit "Editing" marker, so selection and status never share a channel.
- **Disclosure.** Sign-in, token and bootstrap forms expand *in place* and push content down; buttons
  carry `aria-expanded`. Nothing steals focus. Nothing is a modal.
- **Reordering.** Drag by the `⠿` handle. Order is the authored value — position numbers are derived
  from row order, never typed.
- **Segmented controls** are buttons with `aria-pressed`, not radios styled as tabs.
- **Commands.** Night commands and macro runs answer **202**. Show *accepted*, then the run's own
  evidence as it arrives — never a success claim the server did not make. A command whose evidence
  never arrives reads `unconfirmed` (amber: no evidence) or `unconfirmable` (faint: none was ever
  expected), never red.
- **Loading.** A first load shows what is outstanding and asserts nothing. A **refresh failure
  retains the last known state, says when it was read, and adds the failure alongside** — it never
  blanks the region it was refreshing.
- **Scope gating.** A control the principal may not use renders **disabled with a stated reason**,
  never hidden. The two documented exceptions are the "New action" / "New macro" links, hidden
  without `config:write`.
- **Destructive actions** name their consequence including what they orphan. There is no confirm
  dialog for lifecycle commands; the gloved 44–48px target and the stated consequence are the
  safety.
- **Animation** is minimal by design: no transitions on state colors (an operator must read a state,
  not watch it), no spinners as content. Hover and focus changes are instantaneous.
- **Responsive.** Two-pane collapses to one column at `max-width: 1100px`. Tables scroll locally.
  **No pass below 1100px exists yet** — see "Deliberately not designed".

---

## State management

Use the existing model layer; do not add a second one.

- `ModelContext` / `useModel` (`src/api/useModel.ts`, `store.ts`) is the single shared model:
  session, snapshot, observations, macro runs, connection state. Screens read from it; they do not
  own copies.
- **Session.** `GET /session` returns `authenticated`, `principal`, `session`, `scopes`,
  `scopesState`, `credentialForm`, `bootstrapRequired`. `describeSignInState` collapses it to
  `loading | bootstrap_required | signed_out | signed_in`; **`bootstrapRequired` outranks
  signed-out** and is computed whether or not the request authenticated.
- **Scopes.** `evaluateScope` / `evaluateAnyScope` gate controls and produce the *reason* string a
  disabled control displays. `scopesState: 'unknown'` is treated exactly like an empty list.
- **Live updates** arrive on one stream connection; per-step macro-run detail is deliberately
  *fetched*, not streamed (a 32-step run must not put 32 events on every client's stream), and a view
  polling a running run stops polling once `state === 'finished'`.
- **Binding checks** are a separate, credential-free fetch. They must not be gated on a page's read
  scope, and their failure must not blank the list they annotate.
- **Server validates; the client mirrors.** Client-side validation exists only to save a round trip
  and must never be stricter than the server. Render the server's own refusal text
  (`describeApiError`) rather than substituting a guess.
- **Local UI state** (which row is selected, which pane is open, which step is being edited) belongs
  to the screen. Where a name can be ambiguous (two Resolume clips or decks sharing one name), keep
  the **id** in UI state for the picker and the **name** in the payload, and resolve the
  disambiguator from the id — never by re-matching the name.

---

## Facts you may not invent

Anything rendered in `--t-data` or `--t-meta` is a literal identifier and needs a file behind it.
Fabricated capability IDs, enum members and config field names were the most expensive defect class
in the design round — three times, when the real value was one grep away.

Sources of truth: `pkg/capability/id.go` · `api/openapi.yaml` ·
`ui/src/api/generated/schema.d.ts` · the panel that owns the field (`RenderSettingsPanel.tsx`,
`AudioSettings.tsx`, `AudioNodeDetail.tsx`, `ShowCueDetail.tsx`, `ShowPlaylistDetail.tsx`,
`ShowSurfaceDetail.tsx`, `ShowActionDetail.tsx`, `MacroDetail.tsx`, `resolumeComposition.ts`).

`DESIGN-DECISIONS-AND-API-FACTS.md` **§6 is the verified inventory** as of 2026-08-28. If a fact is
not on that list, grep for it; if it does not exist, show the manual path as live and the picker as a
labelled future state. **An empty `<select>`, or a picker fed by a field the API does not return, is
worse than a text input.**

One known drift: `ConfigShowActionTarget.integration` has **four** members —
`'fpp' | 'mqtt' | 'resolume' | 'audio'` — and the current `ShowActionDetail.tsx` form offers only
three. Show Automation designs `audio` in (it carries `audioNodeId`, `audioSessionId`, `audioAction`,
and dB-scale params: `audio.gain.set` → `params.gainDb`, `audio.gain.fade` → `params.targetGainDb`).
The design checkout has drifted from main generally, so expect more exposed facts on application —
several honest-fallback blocks should become real pickers.

---

## Assets

**None.** No images, icons or illustrations. Every glyph is a Unicode character or a CSS shape:
`⠿` U+283F (drag handle), `▸` U+25B8 (editing marker), `▾` U+25BE (picker chevron), `↻` (repeats),
`›` (breadcrumb / tab path), `✓ ⚠ ✕ ⏭ ?` in status badges. Hatching is
`repeating-linear-gradient(135deg, transparent 0 6px, var(--hatch) 6px 12px)`.

Fonts are Google Fonts (Archivo, JetBrains Mono) — self-host them if the installation must work
without internet access, which is likely for a show network.

---

## Files in this bundle

```
design_handoff_operator_ui_overhaul/
├── README.md                              this document
├── UI-DESIGN-GUIDE.md                     normative implementation guide — read first
├── DESIGN-DECISIONS-AND-API-FACTS.md      reasoning + §6 verified API-fact inventory
└── design/
    ├── support.js                         runtime for the .dc.html files — keep beside them
    ├── ShowMesh Design System.dc.html     specimen: every token, state, control, 3 themes
    ├── Dashboard.dc.html
    ├── Show Night.dc.html
    ├── Live Control.dc.html
    ├── Monitor.dc.html
    ├── Node.dc.html
    ├── Resolume Config.dc.html
    ├── Shows.dc.html
    ├── Show Authoring.dc.html
    ├── Show Cues.dc.html
    ├── Show Assets.dc.html
    ├── Show Presentation.dc.html
    ├── Show Automation.dc.html
    ├── Settings.dc.html
    ├── Access.dc.html
    └── Session States.dc.html
```

Open any `.dc.html` directly in a browser. Each file's tweakable variants are declared in its
`data-props` JSON at the bottom; to see a variant without a host, change the `??` default in that
file's `renderVals()`.

---

## Screen map for implementation

| Mock | Route(s) | Replaces / consolidates |
|---|---|---|
| `Dashboard.dc.html` | `/` | dashboard panels |
| `Show Night.dc.html` | show night | fourteen stacked panels |
| `Live Control.dc.html` | live control | ~25 controls with repeated caveats |
| `Monitor.dc.html` | `/monitor` + 4 facets | `/nodes` `/fpp` `/resolume` `/observations` `/events` `/audit` `/capabilities` |
| `Node.dc.html` | node detail | node detail |
| `Resolume Config.dc.html` | Resolume config | `ResolumeView` |
| `Shows.dc.html` | `/shows` | show list + detail |
| `Show Authoring.dc.html` | workspace › Playlists | `ShowPlaylistDetail` |
| `Show Cues.dc.html` | › Cues | `ShowCueDetail` + cue list |
| `Show Assets.dc.html` | › Assets | asset list + upload + history |
| `Show Presentation.dc.html` | › Presentation | `ShowSurfaceDetail` |
| `Show Automation.dc.html` | › Automation | `ShowActions` + `Macros` + `MacroDetail` + `MacroRunView` |
| `Settings.dc.html` | `/settings` × 7 tabs | scattered settings panels |
| `Access.dc.html` | `/access` | principals, scopes, credentials |
| `Session States.dc.html` | signed-out / bootstrap / `*` / first-connect | `SessionPanel`, `SignInForm`, `BootstrapClaimForm`, `NotFound` |

---

## Pre-PR checklist

The six defect classes that recurred during design. Each is cheap to avoid and expensive to find.

1. **Invented API identifiers.** Every mono string traced to a file.
2. **Numbers that do not reconcile.** Derive every displayed number from a value already on screen or
   in a file, and check the arithmetic closes — including across screens.
3. **Cross-screen contradictions.** The UI is one installation at one instant. A fact stated twice is
   stated the same way. Never attribute a ShowMesh-side problem to the resource.
4. **The four absences blurred.** Dashed edge = never-collected, only.
5. **Headings authored as `<span>` or `<p>`.** Check `document.querySelectorAll('main h2, main h3')`
   returns the section labels you meant. This happened four times *after* the rule was written down.
6. **Fake pickers.** No empty `<select>`; no picker fed by a field the API does not return.

Plus: **verify disabled controls are actually inert.** In the mocks, a bare `disabled` attribute
compiled to a button that *looked* unavailable and was fully clickable — it shipped twice before it
was caught. In TSX write `disabled={true}` / `disabled={!editable}`, and confirm both the inertness
and the stated reason.

---

## Deliberately not designed

Do not invent these. State their absence instead — several screens already do.

1. **Installation-wide E-Stop.** Owner's spec: top bar, instant, no confirmation dialog, double-press
   to arm. Not mocked because the coordinator advertises no such capability yet; Live Control states
   its absence rather than implying one exists.
2. **390px phone pass.** Every screen assumes the fixed 212px rail; at very narrow widths the bar
   clips its right-hand group — correct for keeping a fixed 49px chrome, but the phone pass must
   solve it **without re-enabling wrap**.
3. **Mode → mismatch-policy wiring.** Settings › Mode and Show Authoring both state that mismatch
   handling is expected to follow Show vs. Program mode and that the wiring does not exist, so the
   per-playlist control is what takes effect today.
4. **Output groups.** The picker is a labelled future state pending `outputGroups` on
   `audio.output.local`; the manual comma-separated channel field is the live path.
5. **Clock domain evidence.** Currently a manual declaration with recorded provenance. Owner reports
   real evidence is in progress on main.
6. **Audio action authoring.** `integration: 'audio'` is in the schema and not in the form. Wire it
   when you touch `ShowActionDetail.tsx`.
