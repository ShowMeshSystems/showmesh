# ShowMesh Operator — UI design guide and session handoff

> **Implementing in `ui/src`?** Read **`UI-DESIGN-GUIDE.md`** instead — it is the normative,
> self-contained guide for building and extending the real UI (tokens, layout, IA, the four
> absences, component rules, screen map, pre-PR checklist). Suggested repo home:
> `docs/UI-DESIGN-GUIDE.md`. **This** document is the design-session record: the decisions, the
> reasoning, and the verified API-fact inventory that the guide points back to.

**Status:** design round 1 complete, 2026-08-28. Twelve `.dc.html` screens plus a design-system
specimen page. Nothing here has been applied to `ui/src` yet.

This document is written for the next session — human or model — picking up this work. Read it
before touching any screen. It records the decisions, the exact token values, the API facts that
were verified against source, and the mistakes that cost the most time.

---

## 1. What was done and why

The starting point was `feature/operator-ui-overhaul`: a working product with a first visual pass,
a token layer worth keeping, and two structural problems.

**Navigation.** 25 links crammed into a 3-group rail, split into "primary" and "secondary" where
secondary was an unlabelled overflow bin holding 18 destinations. Nodes, FPP, Resolume, Events,
Capabilities and Observations were six separate destinations answering one question.

**Density and verbosity.** Show Night stacked fourteen panels, several of which were a badge and a
sentence. Every one of Live Control's ~25 controls repeated *"Capability: X. Freshness: Y.
Permission and outcomes are reported by each command."* Show authoring was spread across five
global routes reached by tabs that navigated *out* of the show.

The overhaul answers both: seven rail destinations, a persistent top bar, tabs inside consolidated
pages, and copy that states the fact first.

---

## 2. Design system

The specimen page is **`ShowMesh Design System.dc.html`** — it renders every token, state and
control in all three themes. Treat it as the source of truth; these are the same values.

### Colour tokens

Defined on `:root`, overridden by `[data-theme='light']` and `[data-theme='contrast']`.

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

Accent is hue 181 held from the original build, rebuilt as five stops:

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
border `oklch(0.80 0.05 181)`, on-accent `#ffffff`. Contrast: `#66d9ff` / `#9ae7ff` / `#3fc4f0`,
bg `#002733`, on-accent `#000`, focus `#ff0`.

Status pairs, retuned to matched lightness and chroma (the original amber read two steps louder
than the green):

```
--good-fg  oklch(0.82  0.13  158)   --good-bg  oklch(0.255 0.045 158)   --good-border  oklch(0.42 0.07 158)
--warn-fg  oklch(0.845 0.135 80)    --warn-bg  oklch(0.26  0.05  80)    --warn-border  oklch(0.43 0.08 80)
--bad-fg   oklch(0.755 0.145 24)    --bad-bg   oklch(0.26  0.06  24)    --bad-border   oklch(0.43 0.10 24)
--unk-fg   oklch(0.775 0.012 210)   --unk-bg   oklch(0.255 0.012 210)   --unk-border   oklch(0.40 0.014 210)
```

`--text-faint` was raised twice for contrast. Do not lower it: it carries every 11px uppercase
label in the system, and outdoor night legibility is a stated product constraint.

### Type

**Archivo** (sans) + **JetBrains Mono** (metadata). Archivo replaced Inter because it holds shape
better at 11px in dense tabular rows. Seven roles, as shorthand custom properties:

```
--t-display   700 25px/1.15   letter-spacing -0.02em    page titles only
--t-heading   600 20px/1.2    letter-spacing -0.015em   section h2
--t-subhead   600 15px/1.3                              field-group h3, card titles
--t-body      400 14px/1.5                              prose, labels, controls
--t-small     400 12.5px/1.45                           helper, detail, captions
--t-meta      600 11px/1.35   mono, .09em, uppercase    eyebrows, column heads, state words
--t-data      500 13px/1.4    mono, tabular-nums        values, ids, timestamps, hashes
```

Mono is an accent, never the body face. Anything in `--t-data` or `--t-meta` reads as an API fact —
see §6.

### Spacing, radii, controls

4px rhythm: 4 · 8 · 12 · 16 · 24 · 32 · 48. Radii 3px (chips, inner blocks), 4px (nested panels),
5px (cards, controls). Control heights: 30px compact, 34px default, **44–48px for anything touched
with gloves** (transport, lifecycle commands).

### The four absences — do not blur these

This is the single most important rule in the system, and the one most often broken.

| State | Meaning | Treatment |
|---|---|---|
| **Stale** | Reported before, not recently. Has a last-observed time. | `--warn-fg` label, solid edge |
| **Unavailable** | Source does not support the field. Nothing to retry. | `--text-faint` label, **solid** `--border-strong` edge |
| **Unobserved** | Never collected. No collector ever returned it. | `--text-faint` label, **dashed** edge |
| **Empty** | Definitively zero. A settled state. | `--text-faint` label, solid edge or none |

**The dashed edge is reserved for never-collected.** An empty list, a stale value, and an
unsupported field are all settled facts and must not borrow the shape of absent evidence. Every
status is a labelled pair — a state *word* plus colour, never colour alone.

### State blocks

Two treatments, from design-system §06:

- **Ruled strip** (default) — a 150–158px mono state label in a left column, fact and action on the
  right, hairline top and bottom. Sits in the row or field where the content would have been. Use
  for all eight inline states.
- **Blanking plate** — for a whole region that cannot render. A 76px hatched gutter with a stamp,
  copy on clean surface (never behind the hatch), heading, explanation and recovery actions.

The rejected third option was a grid of rounded cards with coloured left borders. It made *absence
of data* look like *a card containing data*.

---

## 3. Information architecture

Seven rail destinations, grouped by operator intent:

```
Operate   Dashboard · Show Night · Live Control
Author    Shows · Assets
System    Monitor · Settings
```

Rail badges are **attention counts, never inventory counts** — a badge means an operator has
something to do.

Consolidation that happened:

- **Monitor** absorbed Nodes, FPP, Resolume, Events, Capabilities, Observations and Audit. Four
  facets: Fleet · Signals · Activity · Capabilities. Nodes/FPP/Resolume are rows in one Fleet table
  with Kind as a column. Events and Audit merged into one Activity stream (audit rows need an
  audit-read scope; system events do not).
- **Shows** owns a workspace with five tabs — Playlists · Cues · Assets · Presentation ·
  Automation — none of which navigate out of the show.
- **Settings** has seven tabs in the same horizontal tab language as everywhere else: Connections ·
  Content delivery · Render recovery · Access (leaves the screen, marked ↗) · Appearance · Audio
  defaults · Node routing · Mode.

### Global chrome

Pinned at the top of every screen, 49px, `[data-chrome] { position: sticky; top: 0; z-index: 30 }`,
with the rail at `[data-rail] { position: sticky; top: 50px }`.

Contents: brand mark · show picker · mode badge · **now-playing** (title, position, cycle, time to
next transition) · connection · user. Beneath it a full-width 3px `role="progressbar"` showing the
current item's position.

**The bar must not wrap.** It is `flex-wrap: nowrap; overflow: hidden`, the now-playing group is
`flex: 1 1 auto; min-width: 0` and truncates with an ellipsis, everything else is `flex: 0 0 auto`.
If it wraps, its height changes and the rail's 50px offset puts the first nav group behind it. Do
not "fix" that by bumping the offset.

The coordinator build string was moved out of the bar into Settings to pay for the space.

---

## 4. Screen inventory

| File | Covers | Tweaks |
|---|---|---|
| `ShowMesh Design System.dc.html` | Tokens, type, controls, states, chrome, copy rules | theme, density |
| `Dashboard.dc.html` | Now bar, readiness, needs-you, system health | attention (items/clear), theme |
| `Show Night.dc.html` | Two-row lifecycle, now/next, lifecycle commands, run of show, evidence | theme |
| `Live Control.dc.html` | Transport, per-output detail, night lifecycle, macros/announcements/actions | theme |
| `Monitor.dc.html` | Fleet table, needs-an-operator, activity, node evidence pane | theme |
| `Node.dc.html` | Per-node identity, capabilities, surfaces, local assets, remove | theme |
| `Resolume Config.dc.html` | Stored composition, ambiguous clips, recovery, observations | theme |
| `Shows.dc.html` | Show list and show details | view (list/detail), theme |
| `Show Authoring.dc.html` | Playlists — FPP bindings vs authored audio order | runner (fpp/showmesh-audio), theme |
| `Show Cues.dc.html` | Cue library and the new-cue composer | theme |
| `Show Assets.dc.html` | Assets grouped by logical sequence, rollback history, upload | pane (history/upload), theme |
| `Show Presentation.dc.html` | Surfaces, virtual matrix map, derived channel count | theme |
| `Show Automation.dc.html` | Macros, their steps, derived binding readiness, actions, run detail | pane (step/run), canAuthor, theme |
| `Session States.dc.html` | Signed out, unclaimed bootstrap, not-found, first-connect | state (4), signInError (3), theme |
| `Access.dc.html` | Principals, scopes, credentials, attribution | theme |
| `Settings.dc.html` | Seven settings pages | page (7 values), theme |

Scenario is one coherent instant across every screen: **21:07 on 2026-08-28**, Winter Ridge 2026
active, cycle 3 live, "Carol of the Bells" at 1:42 / 2:48, `media-garage` offline since 20:41:07,
`barn-player` bindings held since 20:54. All dates fall in 2–26 August. **If you change a number,
grep the other screens for it.**

Automation's own numbers, added this round: the workspace tab reads **Automation · 2**, counting
**macros** — the tab count is the tab's primary object, and an action "never appears on its own in a
running show." Behind it: 2 macros (Preshow Lights Up, 4 steps, revision 3; Weather Hold, 5 steps,
revision 5) and 10 actions — 9 referenced by a macro (4 + 5, no sharing) and 1 not. Bindings swept
`21:06:41`: 8 `ok`, 1 `broken` (`Hold Card` matches clips 4114 and 4193), 1 `unknown` (targets
`media-garage`, offline since `20:41:07`). Preshow Lights Up ran at `20:31:14`–`20:31:19` tonight,
trigger `ui`: completed, **not** confirmed, because step 3 expects no response. Weather Hold has
never been run.

---

## 5. Design decisions worth keeping

Ideas that solved a real problem, not just a layout:

1. **Derive, don't ask.** Where the server requires two values to agree, show one and compute the
   other as evidence. Surface channel count is `width × height × channels-per-pixel` shown as
   `32 × 32 × 4 = 4,096`. FPP entry position comes from row order. The LTC route mirrors the program
   route because the coordinator requires them equal. Each removes a whole class of save-time refusal.
2. **The virtual matrix map.** One band across 32,768 channels showing each surface's range plus the
   unallocated remainder, with an explicit "No overlaps" verdict. Overlapping ranges are a real
   authoring error that three separate forms cannot reveal.
3. **Clips that cannot be named.** Resolume actions reference clips by name; two clips sharing a
   name on the same layer and deck are unresolvable, and the coordinator refuses to guess. The panel
   names the offenders with clip id and column so "rename one" is actionable, and the recovery report
   shows the same defect biting again as a skipped layer.
4. **Rollback as a first-class flow.** Re-uploading superseded bytes makes that version current
   again. History reads as *events* because a row can become current more than once, and the upload
   pane says "This will be a rollback" *before* you commit, with the button relabelled.
5. **Filename belongs to the group.** Assets group by logical sequence because one xLights sequence
   produces a different file per target, all with the same name. The ambiguity becomes the
   explanation instead of the confusion.
6. **Two timelines on Show Night.** The night (a sequence of cycles) and the cycle (phases within
   it) are different clocks. One flattened row made a repeating loop look one-way.
7. **Readiness split in two.** "Running ✓" and "Next start gated ⚠" — mid-show, a single verdict has
   to imply something is wrong with a show that is playing fine.
8. **Macro readiness is derived from its steps' bindings.** The binding sweep needs no credential
   and runs continuously, so the UI can answer "if I press this, will it work?" *without firing the
   macro*. The screen's headline case is the macro you never run — Weather Hold has never been run,
   and its step 2 clip is ambiguous, so the only thing that could ever have told you is the sweep.
   Roll it up on the macro header (`4 of 4 bindings ok` / `Step 2 broken`) and repeat it on the step.
9. **Unconfirmable is a design property, not a defect.** An MQTT action with `expect.kind: none`
   reports `unconfirmable` on every run, forever. Say so *at authoring time* ("Expects no response,
   so it never confirms — by design") so the run history reading `unconfirmable` is pre-explained.
   In the four-absences vocabulary it is **unavailable**: `--text-faint`, solid `--border-strong`
   edge, never dashed, never amber.
10. **A macro's consequence is derived from its steps' safety classes.** Weather Hold contains one
   `stop` step, so its header states "running this ends the current playlist" — the operator learns
   what the button does from what it is made of, not from its name.
12. **Signed out is a state, not a wall.** The band sits in the flow, the rail stays, and the main
   region carries a blanking plate. The rail shows **no badges** — an attention count needs a read,
   and asserting one without a credential would be inventing it.
13. **The two empty installations are different absences.** Signed-out is **unobserved** (dashed
   stamp, "never collected"); an unclaimed coordinator is **empty** (solid stamp, "a settled zero").
   The pair is the clearest demonstration of §2's rule in the system — keep them side by side.
14. **The 404 is the IA migration guide.** Eighteen routes collapsed into seven destinations, so a
   not-found is usually an old bookmark, not a typo. The page maps old address → new place and says
   plainly that nothing is redirected. It also states that the show is fine, with the live now-bar
   above it as the proof.
15. **Health is only what the resource reported.** A ShowMesh-side binding problem is a separate
   signal, never FPP's health.

---

## 6. API facts verified against source

Everything in `--t-data` or `--t-meta` reads as a literal identifier. These were checked; anything
not on this list should be checked before it ships.

**Capability IDs** — `pkg/capability/id.go`:
`matrix.render` · `video.playback` · `media.cache` · `display.hdmi` · `transport.ndi.send` ·
`transport.ndi.receive` · `audio.engine` · `audio.output.local` · `audio.output.fm` ·
`audio.output.ltc` · `audio.output.dante` · `timecode.ltc.observe` · `process.supervise`.
Withdrawn: `audio.playback`, `audio.multichannel`, `audio.dante`, `timecode.ltc.generate`.
`display.hdmi` carries an **outputs count, not display names** — which is why the surface's HDMI
display field must be typed by hand.

**Scopes** — confirmed: `config:write`, `asset:write`, `show:macro:run`, `night:command`. An
audit-read scope exists in behaviour but its literal string was never sourced; it is written as
prose, not mono. Do not invent `access:write`.

**Night commands** — `schema.d.ts`: `prepare-site`, `run-readiness`, `start-preshow`, `start-night`,
`request-final-show`, `fade-out-night`, `power-down-presentation`, plus provisional `end-session`.
They answer **202, never 200** — accepted or an idempotent duplicate, with no downstream
confirmation loop, so the UI must never claim success. `fade-out-night`,
`power-down-presentation`, `request-final-show` and `end-session` are direction-safe and exempt from
the degraded-session gate; the other four fail closed on an unwritable audit store (503). Interlocks
withhold with `409 night-not-ready` naming the rule.

**Render settings** — `RenderSettingsPanel.tsx`: `idleOutput` is `'black' | 'hold' | 'diagnostic'`.
Restart policy is three fields: `initialDelaySeconds` (1–60), `maxDelaySeconds` (1–300),
`maxConsecutiveFastFailures` (1–20).

**Audio settings** (`audio.settings` singleton) — `AudioSettings.tsx`: `driftIgnoreThresholdMs`,
`defaultFadeCurve` (`FADE_CURVES = ['linear']` — one value only), `defaultFadeDurationMs`,
`defaultMaxBackgroundGainDb` (floor −60, max 12), `duckTargetGainDb` (floor −60, must be < 0),
`ltcFrameRate` (`'24' | '25' | '29.97' | '30'`), `ltcDefaultStartOffset` (`^\d{2}:\d{2}:\d{2}:\d{2}$`).

**Audio node** (`audio.node`) — `AudioNodeDetail.tsx`: `programRoute`, `programChannels[]`,
`ltcRoute?`, `ltcChannel?`, `clockDomain`, `clockDomainProvenance`. LTC route and channel are
optional **together**. Program and LTC must name the same route. Routes come from the capability's
`routes` attribute; **the agent advertises routes only, not channel inventories**, so the manual
comma-separated channel field is the live path and the output-group picker is a labelled future
state. Clock domain has no API evidence at all.

**Resolume** — `resolumeComposition.ts`: composition is an uploaded id map of decks, layers,
layerGroups, columns, clips, persistentClips. Signals `resolume.reachable`,
`resolume.layer.<id>.ready`, `resolume.layer.<id>.active_clip`, `resolume.clip.<id>.connected`,
`resolume.clip.<id>.transporttype`, `resolume.composition.identified`,
`resolume.composition.selected_deck`. An `ambiguous` clip shares a (deck-or-persistent, layer,
label) triple with another. `ResolumeRecoveryRestoreLayer.result` is
`'restored' | 'skipped' | 'failed'` (`schema.d.ts:3678`).

**Cue** (`show.cue`) — `ShowCueDetail.tsx`: `show`, `name`, `outputs.{render,audio,ltc,announcement}`.
At least one output required. LTC and announcement each require audio. Announcement policy is
`'duck' | 'mix' | 'interrupt'`; duck gain required for duck only, below 0 and ≥ −60; fade 0–60000ms.

**Show action** (`show.action`) — `openapi.yaml` `ConfigShowActionTarget`: `integration` is
`'fpp' | 'mqtt' | 'resolume' | 'audio'` — **four members, not three**. The checked-out
`ShowActionDetail.tsx` form only offers the first three, so `audio` is a real, schema-verified
integration with no authoring UI yet; the Automation screen designs it in. Audio targets carry
`audioNodeId`, `audioSessionId`, `audioAction` (a reserved `audio.session.*` / `audio.gain.*` /
`audio.output.*` operation name) and pass `params` through undecoded, except that `audio.gain.set`
requires `params.gainDb` and `audio.gain.fade` requires `params.targetGainDb`, **in decibels**
(0 dB unity, −60 dB silence, +12 dB ceiling). The pre-decibel `params.gain` / `params.targetGain`
are refused at authoring time. `safetyClass` is `'none' | 'blackout' | 'stop' | 'powerOff'` and is
never defaulted. FPP primitives are the eight Step 8 registered: `startPlaylist`, `stopPlaylist`,
`stopPlaylistGracefully`, `pausePlaylist`, `resumePlaylist`, `nextPlaylistItem`, `prevPlaylistItem`,
`setVolume`. Resolume actions are the seven Track D D-3 names: `launchClip`, `clearLayer`,
`blackout`, `launchColumn`, `selectDeck`, `setLayerBypass`, `setLayerMaster`. MQTT `expect.kind` is
`'none' | 'boolean' | 'number' | 'text' | 'match'`, deadline 1–120 s; `match`'s value key must be
present but **may be the empty string**.

**Show macro** (`show.macro`) — `MacroDetail.tsx` + `ConfigShowMacroStep`: at most **32 steps**,
each `{id, action, onFailure, onUnconfirmed, localFallback}`. `onFailure` is `'abort' | 'continue'`,
`onUnconfirmed` is `'continue' | 'abort'`, **both defaulting to `continue`** (owner decision
2026-08-14: a macro run always runs every step) — so a step row only needs to state its policy when
it deviates. `localFallback.class` is `'none' | 'coordinator-required' | 'silence'`;
`localFallback.reason` is required and non-empty **even when the class is `none`**. A `reduced`
class is deliberately not offered — no delivery path exists — and must never be rendered as a
pickable option. Every `resolume` action forces `coordinator-required`. A step may only reference an
action in the same show.

**Action binding** (`GET /actions/bindings`) — `ActionBinding`: `state` is `'ok' | 'broken' |
'unknown'` with an always-non-empty `reason`, **including for `ok`**. `unknown` means the check
could not be performed at all — never a soft `ok`, and never `broken` for a check the coordinator
simply could not run. The sweep **requires no credential**, so it must not be gated on the page's
read scope and its failure must never blank the list.

**Macro run** — `MacroRunSummary` / `MacroRunStep`: run `state` is `'running' | 'finished'`;
`trigger` is `'api' | 'plugin' | 'cli' | 'ui'`; `completed` and `confirmed` are **null while
running**, never defaulted to false. Step `state` is `'pending' | 'resolved' | 'skipped'` (no
`dispatched`); step `outcome` is `'confirmed' | 'unconfirmed' | 'unconfirmable' | 'failed' |
'skipped'`, or **null exactly while pending**. `outcomeState` is opaque and deliberately
un-enumerated — never compare it across fields; render `outcomeReason` for humans.
`attributionDegraded` on a run or step means the audit write failed.

**Scopes for automation** — read is `show:macro:run` **OR** `config:write`; running a macro requires
`show:macro:run` specifically (never satisfied by `config:write`); authoring requires
`config:write`.

**Session and bootstrap** — `session.ts` / `SessionPanel.tsx` / `schema.d.ts`: `GET /session`
answers **200 with `authenticated: false`**, never 401 — "being signed out is a persistent,
readable state, not an error a caller must catch." `bootstrapRequired` is true whenever the
coordinator holds **zero principals**, is computed regardless of whether the request authenticated,
and **takes priority over signed-out** in `describeSignInState`. `scopesState` is `'current'` |
`'unknown'` | `'not_applicable'`; a client must treat `unknown` exactly like an empty scope list —
never as permissive. Signed-out and bootstrap are **bands in the document flow that push content
down, never modals** (ADR-024 decision 5 / OPERATOR-UI §14) — do not design a full-screen login
wall. `POST /session` takes `name`, `password`, `deviceLabel` (all required; the label is how a
session appears in Access). `POST /bootstrap` takes `code`, `name`, `password`, `deviceLabel`; the
code is readable only from a file in the coordinator's data volume, is single-use, and a successful
claim creates one `kind: human` / `role: admin` principal, deletes the code and mints a session.
Both endpoints share one per-source rate limit — **never a per-principal lockout**. A stored
break-glass bearer token is checked ahead of any cookie with no fallthrough, so a dead token keeps
a device signed out; "Clear stored token" is offered **only when one is actually stored**. A
`CSRFRejectedError` means neither `Sec-Fetch-Site` nor `Origin` arrived — in practice a proxy
rewriting the Host header, **not an old browser** (that copy was corrected 2026-08-14; do not
reintroduce a browser version).

**Playlist** (`show.playlist`) — `ShowPlaylistDetail.tsx`: runner `'fpp' | 'showmesh-audio'`,
mismatch policy `'hold' | 'blackAndSilence' | 'safeCue'` (fpp only), repeat `'none' | 'all'`
(showmesh-audio only), entries with FPP section/position bindings and a 64-hex playlist hash.

**Surface** (`show.surface`) — `ShowSurfaceDetail.tsx`: `channelRange.{startChannel,channelCount}`,
`geometry.{width,height,pixelFormat}` with pixel format `'rgb' | 'rgbw'`, `frameRate` 1–120,
`output.transport` `'ndi' | 'hdmi'` with exactly one of `ndi.sourceName` / `hdmi.display`.
`width × height × channels-per-pixel` must equal `channelCount` exactly.

---

## 7. Copy rules

Design-system §09 has the before/after. In short:

- **Fact first, at value weight. Caveat second, at helper size.** Never the reverse.
- Label the outcome of a choice, not the field. "Program output group", not "Audio route selection mode".
- Helper text earns its line or it goes. If it repeats the label, delete it.
- Never explain the architecture next to an input. ADR reasoning belongs in docs.
- State the reason literally, then the action. "Agent did not answer in 5 s. Run discovery again."
- Say what a command *does*, not what it is called. "Arrives mid-show, so this show becomes final
  and the fade waits for it to finish."
- Name the consequence of a destructive action, including what it orphans.
- An empty attention list keeps its caveat: "That is not proof the show *looks* right — only that
  nothing has asked for you." It is the most dangerous screen in the product because it is the one
  an operator trusts without reading.

---

## 8. Authoring conventions

Every screen is a single `.dc.html` Design Component, written with `dc_write` /
`dc_html_str_replace` / `dc_js_str_replace` / `dc_set_props`.

- **Inline styles only.** Tokens live in one `<style>` in `<helmet>`; everything else is a `style=`
  attribute so the design paints as it streams. The only other legal helmet CSS is font loading,
  body reset, `[data-chrome]` / `[data-rail]` stickiness, media queries (which inline styles cannot
  express), and `[data-page]`-keyed active-tab rules.
- **Bare boolean attributes do not survive compilation.** Write `disabled="{{ true }}"`,
  `defaultChecked="{{ true }}"`. A bare `disabled` renders a button that looks unavailable and is
  fully clickable — this shipped twice before it was caught.
- **Variants use `sc-if` + a prop**, with `hint-placeholder-val="{{ true }}"` on the default branch
  so it paints during streaming.
- **If it changes a value it is a `<button>`; if it names a section it is a heading.** A styled
  `<span>` or `<p>` is neither. Segmented controls need `aria-pressed`. Section labels need `h2`/`h3`
  inside `section aria-labelledby` — check `document.querySelectorAll('main h3')` before calling a
  page done.
- Tables scroll locally (`overflow-x: auto` on the wrapper, `min-width` on the table) and never give
  the page horizontal scroll. Keep `min-width` low enough to fit the 1280 spine (~596–1012px
  depending on whether there is an inspector pane).
- Two-pane screens use `[data-panes]` with `minmax(320px,420px)` for the inspector, collapsing to
  one column under 1100px.

---

## 9. Carried forward — not designed yet

1. **Installation-wide E-Stop.** Owner's spec: lives in the top bar, instant, no confirmation
   dialog, double-press to arm. Deliberately not mocked because the coordinator advertises no such
   capability yet; Live Control states its absence rather than implying one exists.
2. **390px phone pass.** Every screen assumes a fixed 212px rail. At very narrow widths the pinned
   bar clips its right-hand group — correct for keeping a fixed 49px chrome, but the phone pass must
   solve it *without* re-enabling wrap. The handoff's tablet/phone questions are still open.
3. **Mode → mismatch-policy wiring.** Both Settings → Mode and Show Authoring state that mismatch
   handling is expected to follow Show vs Program mode, and that the wiring does not exist, so the
   per-playlist control is what takes effect today.
4. **Output groups.** The picker exists as a labelled future state pending `outputGroups` on
   `audio.output.local`.
5. **Clock domain evidence.** Owner reports this is in progress on main; the current design is a
   manual declaration with recorded provenance.
6. ~~Signed-out / bootstrap / not-found states.~~ Built — `Session States.dc.html` (also covers
   first-connect). The Automation tab is built too — `Show Automation.dc.html`. **Round 1 is
   complete; the remaining work is applying all fourteen screens to `ui/src`.**
7. This checkout has drifted from main. Expect more exposed facts when this is applied — several
   honest-fallback blocks should become real pickers.

---

## 10. Lessons — the defects that recurred

Five of these were caught more than once. They are cheap to avoid and expensive to find.

1. **Invented API identifiers.** Capability IDs, enum members and config field names were fabricated
   three times when the real ones were one grep away (`pkg/capability/id.go`,
   `RenderSettingsPanel.tsx`, `AudioSettings.tsx`). If it renders in mono, it needs a file behind it.
2. **Numbers that do not reconcile.** Six separate contradictions: a cue count of 11 vs 10, "6 steps
   armed" against 8 recorded, 4h46m vs 4h39m, "12 stale" while one offline node held 47 signals,
   38.2 GB of assets that are 19–45 MB each, and a *last saved* date two months in the future.
   Derive every displayed number from a value already on screen or in a file, and check the
   arithmetic closes.
3. **Cross-file contradictions.** "✓ Hash current" beside a changed FPP definition; a health badge
   attributing a ShowMesh binding problem to FPP; two targets sharing one content hash. The screens
   are one installation at one instant — a fact stated twice must be stated the same way.
4. **The four absences blurred.** Unobserved used for stale, dashed edges on unavailable and on
   empty. Re-read §2 before writing any state label.
5. **Headings authored as `<span>` or `<p>`.** Four times, in files written *after* the rule was
   written down. Small mono labels look like eyebrows and get reached for reflexively.
6. **Fake pickers.** An empty `<select>`, or a picker fed by an API fact that does not exist, is
   worse than a text field. When the fact is missing, show the manual path as live and the picker as
   labelled-future — never the reverse.
