# ShowMesh Operator UI — design guide for implementers

**Audience:** an agent or engineer writing real code in `ui/src`, either applying the round-1
overhaul or adding a screen after it. **This document is normative.** It states the rules, not the
history — for *why* a decision was made, see `DESIGN-GUIDE-AND-HANDOFF.md` (the design-session
record) alongside it.

**Suggested home in the repo:** `docs/UI-DESIGN-GUIDE.md`, referenced from `docs/README.md` and
from `AGENTS.md` / `CLAUDE.md` so it is loaded before any UI work.

---

## 0. How to use the mocks

Fourteen reference screens exist as standalone `.dc.html` files. They are **visual specifications,
not code to port**:

- They use **inline styles** on every element. That is a constraint of the mock format (it must
  paint while streaming), **not a rule for this codebase.** In `ui/src`, write real CSS classes in
  `src/styles/`. Lift the *values* — hex codes, token names, font stacks, paddings, heights,
  radii — never the `style=` attributes.
- They are **static**. Every state they show (selected row, expanded form, error banner) is a real
  state the model must drive. The mock's tweak props name the variants that matter: e.g. Session
  States has `state` (signed-out / bootstrap / not-found / connecting) and `signInError`
  (none / proxy / rate-limit) — those are the branches to implement, not decoration.
- Numbers in them are **one coherent scenario** at one instant (21:07 on 2026-08-28). They are
  illustrative. Do not ship them as fixtures without checking the arithmetic still closes.

---

## 1. Tokens

Define once on `:root` in `src/styles/`, override under `[data-theme='light']` and
`[data-theme='contrast']`. Never hard-code a colour in a component.

| Token | Dark | Light | Contrast |
|---|---|---|---|
| `--bg` | `#080c0e` | `#f2f5f4` | `#000` |
| `--surface` | `#0c1215` | `#ffffff` | `#000` |
| `--raised` | `#111a1d` | `#ffffff` | `#0d0d0d` |
| `--sunken` | `#050809` | `#e8edeb` | `#000` |
| `--border` | `#26343a` | `#ccd7d4` | `#fff` |
| `--border-strong` | `#3b4d54` | `#a5b5b1` | `#fff` |
| `--text` | `#eef5f3` | `#101a1a` | `#fff` |
| `--text-muted` | `#a5b5b9` | `#43514f` | `#e6e6e6` |
| `--text-faint` | `#96a5a2` | `#4f5d5a` | `#cfcfcf` |
| `--hatch` | `#26343a` | `#d4dedb` | `#4d4d4d` |

Accent is hue 181, five stops:

```
--accent          oklch(0.845 0.112 181)   rest: primary action, links, current
--accent-hover    oklch(0.90  0.115 181)
--accent-active   oklch(0.76  0.105 181)
--accent-bg       oklch(0.26  0.045 181)   selected row / active tab wash
--accent-border   oklch(0.42  0.075 181)   quiet edges in accent context
--on-accent       #05191a
--focus           oklch(0.88  0.09  181)
```

Light: accent `oklch(0.50 0.09 181)`, hover `0.44`, active `0.38`, bg `oklch(0.958 0.024 181)`,
border `oklch(0.80 0.05 181)`, on-accent `#ffffff`, focus `oklch(0.52 0.10 181)`.
Contrast: `#66d9ff` / `#9ae7ff` / `#3fc4f0`, bg `#002733`, border `#66d9ff`, on-accent `#000`,
focus `#ff0`.

Status pairs — matched lightness and chroma across hues, so amber never reads louder than green:

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
Contrast: good `#4dff8f`/`#003d17`, warn `#ffd23f`/`#4a3300`, bad `#ff6b60`/`#4a0000`,
unk `#fff`/`#262626`; every border in contrast is the foreground colour.

**Do not lower `--text-faint`.** It carries every 11px uppercase label in the system, and outdoor
night legibility is a product constraint. It was raised twice for contrast.

### Type

**Archivo** (sans) + **JetBrains Mono** (metadata). Seven roles; ship them as custom properties and
use the property, not a re-declared font stack:

```
--t-display   700 25px/1.15   letter-spacing -0.02em    page titles only
--t-heading   600 20px/1.2    letter-spacing -0.015em   section h2
--t-subhead   600 15px/1.3                              field-group h3, card titles
--t-body      400 14px/1.5                              prose, labels, controls
--t-small     400 12.5px/1.45                           helper, detail, captions
--t-meta      600 11px/1.35   mono, .09em, uppercase    eyebrows, column heads, state words
--t-data      500 13px/1.4    mono, tabular-nums        values, ids, timestamps, hashes
```

Mono is an accent, never the body face. **Anything rendered in `--t-data` or `--t-meta` reads as a
literal API fact** — see §7.

### Spacing, radii, controls

4px rhythm: 4 · 8 · 12 · 16 · 24 · 32 · 48. Radii 3px (chips, inner blocks), 4px (nested panels),
5px (cards, controls).

Control heights: **30px** compact, **34px** default, **44–48px for anything touched with gloves** —
transport, lifecycle commands, macro Run, and every field on a sign-in or bootstrap form (they get
used on a phone in the cold).

---

## 2. Layout and chrome

**Global bar**, on every authenticated screen and every session state:

```css
[data-chrome] { position: sticky; top: 0; z-index: 30; }   /* 49px tall */
[data-rail]   { position: sticky; top: 50px; }
```

Contents, left to right: brand mark · show picker · mode badge · now-playing (title, position,
cycle, time to next transition) · connection · principal. Beneath it a full-width 3px
`role="progressbar"` for the current item's position.

**The bar must not wrap.** `flex-wrap: nowrap; overflow: hidden`; the now-playing group is
`flex: 1 1 auto; min-width: 0` and truncates with an ellipsis; everything else is `flex: 0 0 auto`.
If it wraps its height changes and the rail's 50px offset puts the first nav group behind it — fix
the wrap, never the offset.

Rail is a fixed **212px** column: `grid-template-columns: 212px minmax(0,1fr)`.

**Two-pane screens** (list + inspector):

```css
[data-panes] { display: grid; grid-template-columns: minmax(0,1fr) minmax(320px,420px); align-items: start; }
[data-panes] > aside { position: sticky; top: 0; }
@media (max-width: 1100px) { [data-panes] { grid-template-columns: minmax(0,1fr); } }
```

**Tables scroll locally and never give the page horizontal scroll:** `overflow-x: auto` +
`overscroll-behavior-inline: contain` on the wrapper, `min-width` on the table. Keep `min-width` low
enough to fit the 1280 spine — ~596px with an inspector pane, ~1012px without.

**No responsive pass below 1100px exists yet.** A 390px phone pass is open work; it must be solved
without re-enabling wrap in the bar.

---

## 3. Information architecture

Seven rail destinations. Do not add an eighth without deleting one.

```
Operate   Dashboard · Show Night · Live Control
Author    Shows · Assets
System    Monitor · Settings
```

- **Monitor** has four facets: **Fleet · Signals · Activity · Capabilities**. They are organised by
  *axis* — by resource, by observation, by event, by capability — not by resource type. Nodes, FPP
  and Resolume are rows in one Fleet table with Kind as a column. Events and audit are one Activity
  stream (audit rows need an audit-read scope; system events do not).
- **Shows** owns a five-tab workspace: **Playlists · Cues · Assets · Presentation · Automation.**
  No tab navigates out of the show.
- **Settings** has seven tabs: Connections · Content delivery · Render recovery · Access (leaves the
  screen, mark it ↗) · Appearance · Audio defaults · Node routing · Mode.

**Rail badges are attention counts, never inventory counts.** A badge means an operator has
something to do. A signed-out or not-yet-read device shows **no badges at all** — an attention count
needs a read, and rendering one without evidence invents it.

**Tab counts are inventory** — of the tab's *primary* object. Automation counts macros (an action
never runs on its own in a show).

---

## 4. The four absences — the most important rule here

This is the rule most often broken, and the one that makes the product trustworthy.

| State | Meaning | Treatment |
|---|---|---|
| **Stale** | Reported before, not recently. Has a last-observed time. | `--warn-fg` label, solid edge |
| **Unavailable** | The source does not support this field. Nothing to retry. | `--text-faint` label, **solid** `--border-strong` edge |
| **Unobserved** | Never collected. No collector ever returned it. | `--text-faint` label, **dashed** edge |
| **Empty** | Definitively zero. A settled state. | `--text-faint` label, solid edge or none |

**The dashed edge is reserved for never-collected.** An empty list, a stale value and an unsupported
field are all settled facts and must not borrow the shape of absent evidence.

Every status is a **labelled pair** — a state *word* plus a colour, never colour alone.

Two more absences that map onto this table and are easy to get wrong:

- **`unknown`** on a binding check, and `scopesState: 'unknown'`, mean *the check could not be
  performed* — never a soft `ok`, and never rendered as `broken`. Treat an unknown scope list
  exactly like an empty one: **unknown is never permissive.**
- **`unconfirmable`** on a macro-run step is **unavailable**, not a failure: an action that expects
  no response reports it on every run, forever, by design. Say so at *authoring* time so the run
  history is pre-explained.

### The two state blocks

- **Ruled strip** (default) — a 150–158px mono state label in a left column, fact and action on the
  right, hairline top and bottom. Sits in the row or field where the content would have been.
- **Blanking plate** — for a whole region that cannot render. A 76px hatched gutter
  (`repeating-linear-gradient(135deg, transparent 0 6px, var(--hatch) 6px 12px)`) with a stamp,
  then copy on **clean surface** (never behind the hatch): eyebrow, `h2`, explanation, recovery
  actions. The stamp's border carries the absence class — dashed for unobserved, solid otherwise.

Rejected, do not reinvent: a grid of rounded cards with coloured left borders. It made *absence of
data* look like *a card containing data*.

---

## 5. Copy rules

- **Fact first, at value weight. Caveat second, at helper size.** Never the reverse.
- Label the outcome of a choice, not the field. "Program output group", not "Audio route selection
  mode".
- Helper text earns its line or it goes. If it repeats the label, delete it.
- Never explain the architecture next to an input. ADR reasoning belongs in docs.
- State the reason literally, then the action: "Agent did not answer in 5 s. Run discovery again."
- Say what a command *does*, not what it is called: "Arrives mid-show, so this show becomes final
  and the fade waits for it to finish."
- Name the consequence of a destructive action, **including what it orphans**.
- An empty attention list keeps its caveat: "That is not proof the show *looks* right — only that
  nothing has asked for you." It is the most dangerous screen in the product, because it is the one
  an operator trusts without reading.
- Never claim success the server did not report. Night commands answer **202** with no downstream
  confirmation loop: the UI says *accepted*, never *done*.

---

## 6. Component rules

- **If it changes a value it is a `<button>`. If it names a section it is a heading.** A styled
  `<span>` or `<p>` is neither. Before calling a screen done, check
  `document.querySelectorAll('main h2, main h3')` returns the section labels you meant.
- Sections get `aria-labelledby` pointing at their real heading. Small uppercase mono labels are
  *styled* headings, not `<span>`s.
- Segmented controls need `aria-pressed`. Selected rows need `aria-current`. Disclosure buttons need
  `aria-expanded`.
- **A control the principal may not use renders disabled with a stated reason, never hidden** —
  the `ScopedButton` posture. The two documented exceptions are the "New action" / "New macro"
  links, hidden outright without `config:write`.
- Read gates that accept either of two scopes must say which is missing, and must not gate content
  that a different scope already permits.
- A poll or refresh failure **retains the last known state and says when it was read** — it never
  blanks the region it was refreshing.
- Reorderable lists get a `⠿` (U+283F) handle in the index column with `cursor: grab`, and say
  "Drag to reorder" once in the container footer.
- Prefer flex/grid + `gap` over margins for sibling groups.
- Focus is always visible: `:focus-visible { outline: 2px solid var(--focus); outline-offset: 2px }`.

### Session states are not walls

`GET /session` answers **200 with `authenticated: false`** — being signed out is a persistent,
readable state, not an error. Signed-out and bootstrap-required render as **bands in the document
flow that push content down**; the chrome and rail stay; the main region carries a blanking plate.
**Do not build a full-screen login page.** `bootstrapRequired` outranks signed-out and is computed
whether or not the request authenticated.

---

## 7. Facts you may not invent

Anything in `--t-data` or `--t-meta` is a literal identifier and needs a file behind it. Fabricated
capability IDs, enum members and config field names were the single most expensive defect class in
the design round — three times, when the real value was one grep away.

Sources of truth: `pkg/capability/id.go` · `api/openapi.yaml` · `src/api/generated/schema.d.ts` ·
the panel component that owns the field (`RenderSettingsPanel.tsx`, `AudioSettings.tsx`,
`AudioNodeDetail.tsx`, `ShowCueDetail.tsx`, `ShowPlaylistDetail.tsx`, `ShowSurfaceDetail.tsx`,
`ShowActionDetail.tsx`, `MacroDetail.tsx`, `resolumeComposition.ts`).

`DESIGN-GUIDE-AND-HANDOFF.md` §6 holds the verified inventory as of 2026-08-28 — capability IDs and
which are withdrawn, scopes, night commands and their status codes, render/audio settings fields and
ranges, Resolume signals and action names, cue/playlist/surface constraints, the four action
integrations (**including `audio`**, which the schema has and the current authoring form does not
offer), macro step policy enums and defaults, binding-check states, and macro-run state/outcome
vocabularies. **Read it before typing a mono string.** If a fact is not on that list, grep for it;
if it does not exist, show the manual path as live and the picker as a labelled future state —
an empty `<select>` or a picker fed by a nonexistent field is worse than a text input.

### Derive, don't ask

Where the server requires two values to agree, show one and compute the other as visible evidence.
Established cases, keep them: surface channel count as `32 × 32 × 4 = 4,096`; FPP entry position
from row order; the LTC route mirroring the program route; macro readiness rolled up from its steps'
binding checks; a macro's consequence rolled up from its steps' safety classes. Each removes a whole
class of save-time refusal.

---

## 8. Screen map

| Mock | Route(s) | Replaces / consolidates |
|---|---|---|
| `Dashboard.dc.html` | `/` | dashboard panels |
| `Show Night.dc.html` | show-night | fourteen stacked panels |
| `Live Control.dc.html` | live control | ~25 controls with repeated caveats |
| `Monitor.dc.html` | `/monitor` + 4 facets | `/nodes` `/fpp` `/resolume` `/observations` `/events` `/audit` `/capabilities` |
| `Node.dc.html` | node detail | node detail |
| `Resolume Config.dc.html` | Resolume config | `ResolumeView` |
| `Shows.dc.html` | `/shows` | show list + detail |
| `Show Authoring.dc.html` | show workspace › Playlists | `ShowPlaylistDetail` |
| `Show Cues.dc.html` | › Cues | `ShowCueDetail` + cue list |
| `Show Assets.dc.html` | › Assets | asset list + upload + history |
| `Show Presentation.dc.html` | › Presentation | `ShowSurfaceDetail` |
| `Show Automation.dc.html` | › Automation | `ShowActions` + `Macros` + `MacroDetail` + `MacroRunView` |
| `Settings.dc.html` | `/settings` × 7 tabs | scattered settings panels |
| `Access.dc.html` | `/access` | principals, scopes, credentials |
| `Session States.dc.html` | signed-out / bootstrap / `*` / first-connect | `SessionPanel`, `SignInForm`, `BootstrapClaimForm`, `NotFound` |
| `ShowMesh Design System.dc.html` | — | the specimen; every token, state and control in three themes |

Old addresses are **not redirected**. The not-found page maps old → new and says so.

---

## 9. Pre-PR checklist

The six defect classes that recurred. Each is cheap to avoid and expensive to find.

1. **Invented API identifiers.** Every mono string traced to a file. (§7)
2. **Numbers that do not reconcile.** Derive every displayed number from a value already on screen
   or in a file, and check the arithmetic closes — including across screens.
3. **Cross-screen contradictions.** The UI is one installation at one instant. A fact stated twice
   is stated the same way. Never attribute a ShowMesh-side problem to the resource
   (a binding failure is not FPP's health).
4. **The four absences blurred.** Re-read §4 before writing any state label. Dashed edge =
   never-collected, only.
5. **Headings authored as `<span>` or `<p>`.** Happened four times *after* the rule was written.
6. **Fake pickers.** No empty `<select>`; no picker fed by a field the API does not return.

Plus: bare boolean attributes. In the mocks, `disabled` without a value compiled to a button that
*looked* unavailable and was fully clickable — it shipped twice. In TSX this is `disabled={true}`;
still verify the rendered control is actually inert, and that its reason is stated.

---

## 10. Deliberately not designed

Do not invent these; state their absence instead.

1. **Installation-wide E-Stop.** Owner's spec: top bar, instant, no confirmation dialog,
   double-press to arm. Not mocked because the coordinator advertises no such capability. Live
   Control states its absence rather than implying one exists.
2. **390px phone pass.** Every screen assumes the fixed 212px rail; at very narrow widths the bar
   clips its right-hand group. Solve without re-enabling wrap.
3. **Mode → mismatch-policy wiring.** Settings › Mode and Show Authoring both state that mismatch
   handling is expected to follow Show vs Program mode and that the wiring does not exist, so the
   per-playlist control is what takes effect today.
4. **Output groups.** The picker is a labelled future state pending `outputGroups` on
   `audio.output.local`; the manual comma-separated channel field is the live path.
5. **Clock domain evidence.** Currently a manual declaration with recorded provenance. Owner reports
   real evidence is in progress on main.
6. **Audio action authoring.** `integration: 'audio'` is in the schema; `ShowActionDetail.tsx` does
   not offer it. Show Automation designs it in — wire it when you touch that form.
