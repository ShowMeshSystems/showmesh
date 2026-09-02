# ShowMesh Operator UI design-transfer brief

**Status:** local working handoff, 2026-08-28. This is deliberately not a
repository commit: it contains absolute paths to local prototype artifacts so
they can be imported into a design tool without turning machine-specific paths
into product documentation.

## What this work is

ShowMesh Operator is a compact show-control interface for configuring and
operating an installation. It is not a business dashboard. Its job is to make
the current operational truth, the next operator decision, and the safest
direct action legible under show-time conditions.

The implementation on `feature/operator-ui-overhaul` is a useful product and
component reference, but it is a **first visual pass**, not a completed design
system or an exact layout specification. The design-tool work should rebuild
the layouts as a coherent set of desktop, tablet, and phone designs. It should
carry forward the interaction and authority rules below, rather than tracing
the current DOM or copying a static mockup pixel for pixel.

The immediate focus is Settings, but its framing has to remain consistent with
the wider Operator shell, Monitor, Live Control, Show Night, and show-authoring
workspaces.

## Product and authority model

The most important design constraint is that the browser presents coordinator
truth; it must not invent configuration choices or silently turn missing
evidence into a healthy-looking state.

- The coordinator is the configuration authority. Writes are revisioned,
  validated by the server, permission-gated, audited, and can conflict.
- FPP remains authoritative for its own schedule, playlist order, selection,
  and playhead. ShowMesh observes and orchestrates around that evidence.
- Audio choices must come from the currently server-resolved inventory. If an
  output group, route, channel, ownership, or clock fact is unavailable, show
  the literal reason and retain the advanced manual path only where the API
  already supports it.
- `Show Night` is the lifecycle view. `Live Control` owns operational
  commands. `Cue` is an independently revisioned show object; use `Transition
  Step` for Show Night work.
- Unknown, stale, failed, unavailable, unobserved, disconnected, and signed
  out are distinct states. They must not collapse into a generic red error or
  an empty dashboard.

## The visual direction to preserve

The direction is restrained and technical, not decorative:

- Dark graphite is the show-time default. Light mode is a daylight setup mode.
  High contrast is a deliberate operator setting, not an inferred theme.
- Use one blue-green accent for the current selection, links, focus, and the
  one primary action. Reserve green, amber, and red for labelled evidence and
  state.
- Use compact operator density: short headings, direct prose, clear labels,
  32--40 px controls, and 48 px minimum touch targets where the shell needs
  them. Do not turn ordinary form sections into a grid of business-dashboard
  cards.
- Use a sans-serif for readable content and a restrained monospace treatment
  for small uppercase metadata, timestamps, revision IDs, evidence labels, and
  aligned values. Monospace is an accent, not the body typeface.
- Let hairline dividers, alignment, and spacing group related content. Panels
  are for meaningfully different context, state, or confirmation, not every
  field group.
- Keep a single global navigation rail. Settings may have local hierarchy or
  a directory, but it must not create a second competing global rail.
- Wide evidence tables scroll within their own focused container; the page
  never gains horizontal scrolling.

### Existing semantic foundation

The current token file is the source of truth for what is already implemented.
Port these roles into the design-tool library as semantic variables, rather
than carrying raw hex values into individual components.

| Role | Dark default | Light mode | Intended use |
| --- | --- | --- | --- |
| Page background | `#090d0f` | graphite-50 (`#f3f6f5`) | Calm graphite canvas |
| Surface | `#0c1215` | `#ffffff` | Forms, main shell, ordinary content |
| Raised surface | `#0d191b` | `#ffffff` | State blocks and elevated context |
| Border | `#304147` | graphite-300 (`#b7c5c2`) | Hairlines and control edges |
| Primary text | `#eef5f3` | graphite-950 (`#111b1b`) | Headings and body |
| Muted text | `#8fa1a7` | graphite-600 (`#52615f`) | Helper copy and metadata |
| Accent | `#4be0d1` | `#087f78` | Primary action, active, links, focus |
| High-contrast accent | `#66d9ff` | same override | Explicit high-contrast mode |

The actual implemented variables, status pairs, type scale, spacing scale,
control heights, radii, and high-contrast overrides are in:

- `/Users/erbartos/orca/workspaces/ShowMesh/UI-Overhaul/ui/src/styles/tokens.css`
- `/Users/erbartos/orca/workspaces/ShowMesh/UI-Overhaul/ui/src/styles/global.css`

Use the implemented 4 px spacing rhythm: 4, 8, 12, 16, 24, 32, and 48 px.
The current compact type roles are 11 px metadata, 12 px small text, 14 px
body, 20 px headings, and 25 px display title. In the design-tool library,
document line height, weight, and responsive behavior alongside those roles.

## Information architecture

The global navigation is grouped by operator intent, then Settings provides
direct destinations. Legacy routes stay compatible; the future design should
not imply that a direct URL has disappeared just because the visual structure
becomes cleaner.

```text
Operate: Dashboard, Show Night, Live Control
Author:  Shows, Assets
System:  Monitor, Settings
  Settings
    Connections              /config/connections
    Content delivery         /config/content-delivery
    Render recovery          /config/render-recovery
    Access                   /config/access
    Appearance               /config/appearance
    Audio directory          /config/audio
      Audio defaults         /config/audio.settings
      Audio routing          /config/audio.node
    Mode                     /config/mode
```

The current router and retained fragment redirects are the route source of
truth:

- `/Users/erbartos/orca/workspaces/ShowMesh/UI-Overhaul/ui/src/app/App.tsx`

The direct Settings wrappers and their page-level title/safety context are in:

- `/Users/erbartos/orca/workspaces/ShowMesh/UI-Overhaul/ui/src/views/SettingsPages.tsx`
- `/Users/erbartos/orca/workspaces/ShowMesh/UI-Overhaul/ui/src/views/settings-pages.css`

## Ideal Settings page anatomy

Use this as the baseline layout to design, refine, and prototype. It is a
pattern, not a mandate that every page look identical.

1. A compact page header: `Settings / subsection`, one operator-task title,
   and one-sentence safety or authority context.
2. A direct working area. Fields have visible labels above inputs; labels name
   the outcome of a selection. Helper text appears only when it changes the
   decision.
3. Related fields form a clearly separated section using heading, short
   purpose line, and divider. Do not require an inspect overlay before editing.
4. Conditional controls appear immediately after the condition that reveals
   them. A hidden dependent control must not leave unexplained spacing or a
   hidden validation error.
5. One primary `Save`, `Apply`, or equivalent action per page. Keep Cancel,
   test, remove, or revision controls visually secondary and physically apart
   from destructive choices.
6. Put revision, audit, current-server version, and last-change evidence below
   or adjacent to the save path at secondary visual weight. They are vital
   context, but should not compete with the edit task.
7. Preserve a visible state block when the form cannot truthfully render:
   loading, empty, signed-out, permission denied, stale permission evidence,
   unavailable capability, validation failure, conflict, retry, or failed
   save. State copy must say what is unavailable and what the operator can do.
8. On a dirty form, attempting to leave opens a deliberate unsaved-navigation
   confirmation. Destructive changes have their own confirmation; do not
   reuse the navigation dialog.

### Page-specific content direction

| Destination | Operator task and ideal layout |
| --- | --- |
| Connections | Maintain FPP, Resolume, and event-feed reachability. Stack direct connection sections with named endpoints, address fields, small test/secondary actions, and one save path. Show connectivity outcomes near the specific endpoint, not in a generic banner. |
| Content delivery | Configure asset-store delivery. Use a short form and literal unavailable state if the coordinator cannot provide usable data. Avoid a metric dashboard. |
| Render recovery | Set idle output and bounded recovery behavior. Separate the safety explanation from the actionable controls; destructive/restart implications need clear confirmation. |
| Access | Manage principals and credentials in an audited area. Keep credentials, token errors, and permission evidence explicit. Destructive revoke/remove actions need a distinct confirmation. |
| Appearance | Explain that high contrast is browser-local and does not create a coordinator revision. Keep it small, direct, and visually distinct from server-backed settings. |
| Audio | Start with a choice between installation defaults and per-node routing. The routing page selects a node first, then server-advertised output route/group choices, with an advanced manual path where supported. |
| Mode | A direct, installation-wide picker. The active state and consequence must be immediately visible; it is never merely a link to a buried settings anchor. |

## Layout work that should get much stronger in the design tool

The current implementation establishes components and behavior; the next
design pass should deliberately solve these layout questions from scratch.

- **Desktop:** establish a stable rail width, main content max-widths, a
  comfortable form measure, and a wider evidence/table measure. Avoid the
  visual grid background from early concept screens unless it earns its place
  through actual operational meaning; it is not part of the implementation
  direction.
- **Tablet:** preserve the rail or a clear compact directory without creating
  a second navigation layer. Reflow two-column form rows thoughtfully, rather
  than just shrinking inputs.
- **390 px phone:** all destinations and actions must stay reachable. The
  current implementation uses a visible/mobile navigation arrangement; the
  next pass should design its hierarchy, selected state, expansion behavior,
  focus sequence, and safe area intentionally. Form fields become one column.
- **Form density:** create a reusable section rhythm and a two-column field
  pattern with a tested one-column fallback. Make labels, help, required state,
  errors, and disabled explanation line up cleanly.
- **Tables:** define the local overflow affordance, sticky or non-sticky
  headers based on use, truncation rules, numeric alignment, and an explicit
  narrow-screen column priority. Do not rely on shrinking all columns.
- **State blocks:** design one family with distinct loading, empty,
  unavailable, stale, failed, signed-out, and insufficient-permission variants.
  Their layout should preserve the page title and context so a blocked form
  does not feel like a broken route.
- **Safety flows:** design server validation near the field, conflicts with a
  clear revision comparison/reload path, retry near the failed resource, a
  dedicated unsaved-navigation dialog, and separate destructive confirmation.

## Component inventory to recreate in the design tool

Start with variants and documented states rather than individual screen-only
artifacts.

| Component | Variants that need designs |
| --- | --- |
| Application shell | desktop rail, tablet rail/directory, phone navigation; selected, collapsed, keyboard focus; light, dark, high contrast |
| Page header | eyebrow, title, safety lede, primary action, secondary metadata |
| Form section | heading, purpose line, divider, single/two-column fields, conditional row |
| Controls | text, URL, select, checkbox/toggle where warranted, primary/secondary/destructive button; default, hover, active, focus-visible, disabled, invalid |
| State block | loading, empty, unavailable with literal reason, signed out, insufficient permission, stale, failed/retry, unobserved |
| Revision/audit detail | current revision, last changed, source/evidence freshness, conflict presentation |
| Confirmation | unsaved navigation and destructive action as separate dialog patterns |
| Evidence table | wide desktop, local horizontal overflow, sparse/empty, stale, row action, narrow-screen priority |
| Audio route picker | known inventory, no compatible choice, discovery stale/failed, program only, LTC enabled, manual advanced fallback |

The shared implementation primitives to inspect when mapping components are:

- `/Users/erbartos/orca/workspaces/ShowMesh/UI-Overhaul/ui/src/components/SharedLayouts.tsx`
- `/Users/erbartos/orca/workspaces/ShowMesh/UI-Overhaul/ui/src/styles/operator-pages.css`
- `/Users/erbartos/orca/workspaces/ShowMesh/UI-Overhaul/ui/src/app/Layout.tsx`
- `/Users/erbartos/orca/workspaces/ShowMesh/UI-Overhaul/ui/src/app/UnsavedChanges.tsx`

## Full prototype and mockup index

All material below is local, external visual reference. It does not ship in
the application. The HTML files are editable static prototypes; `-preview`
files are their preview wrappers; PNG files are captured renders. Import the
HTML or the most representative PNG into the design tool, then rebuild as
components rather than treating any capture as a final production screen.

### Primary direction screens

| What it shows | Full path | How to use it |
| --- | --- | --- |
| Preferred operator Dashboard: readiness, four-part status strip, presentation path, attention item, activity table, compact rail, direct Mode control | `/Users/erbartos/.codex/visualizations/2026/08/26/01a03e7a-6672-7ec3-807a-c79c61a421d0/dashboard-operator-overview-prototype.html` | Primary Dashboard layout direction. |
| Preview wrapper for the preferred Dashboard | `/Users/erbartos/.codex/visualizations/2026/08/26/01a03e7a-6672-7ec3-807a-c79c61a421d0/dashboard-operator-overview-prototype-preview.html` | Openable preview. |
| Render of the preferred Dashboard | `/Users/erbartos/.codex/visualizations/2026/08/26/01a03e7a-6672-7ec3-807a-c79c61a421d0/dashboard-operator-overview-desktop.png` | Visual import/reference. |
| Preferred Show Night Run of Show with a live current row and FPP transition evidence | `/Users/erbartos/.codex/visualizations/2026/08/26/01a03e7a-6672-7ec3-807a-c79c61a421d0/show-night-plan-prototype.html` | Primary Show Night direction. |
| Preview wrapper for preferred Show Night plan | `/Users/erbartos/.codex/visualizations/2026/08/26/01a03e7a-6672-7ec3-807a-c79c61a421d0/show-night-plan-prototype-preview.html` | Openable preview. |
| Render of Show Night plan | `/Users/erbartos/.codex/visualizations/2026/08/26/01a03e7a-6672-7ec3-807a-c79c61a421d0/show-night-run-desktop.png` | Visual import/reference. |
| Node detail organized by node, render, and audio observation meaning and freshness | `/Users/erbartos/.codex/visualizations/2026/08/26/01a03e7a-6672-7ec3-807a-c79c61a421d0/node-observations-prototype.html` | Primary evidence-detail direction. |
| Preview wrapper for node observations | `/Users/erbartos/.codex/visualizations/2026/08/26/01a03e7a-6672-7ec3-807a-c79c61a421d0/node-observations-prototype-preview.html` | Openable preview. |
| Desktop node-observations render | `/Users/erbartos/.codex/visualizations/2026/08/26/01a03e7a-6672-7ec3-807a-c79c61a421d0/node-observations-desktop.png` | Visual import/reference. |
| First mobile node-observations render | `/Users/erbartos/.codex/visualizations/2026/08/26/01a03e7a-6672-7ec3-807a-c79c61a421d0/node-observations-mobile.png` | Comparison only; later v2 is the preferred narrow reference. |
| Revised mobile node-observations render | `/Users/erbartos/.codex/visualizations/2026/08/26/01a03e7a-6672-7ec3-807a-c79c61a421d0/node-observations-mobile-v2.png` | Preferred narrow evidence reference. |
| Direct labelled cue form with dependent choices | `/Users/erbartos/.codex/visualizations/2026/08/26/01a03e7a-6672-7ec3-807a-c79c61a421d0/cue-editor-prototype.html` | Primary form-pattern reference. |
| Preview wrapper for cue editor | `/Users/erbartos/.codex/visualizations/2026/08/26/01a03e7a-6672-7ec3-807a-c79c61a421d0/cue-editor-prototype-preview.html` | Openable preview. |
| Direct Settings Connections page with left-rail Settings subsections, labelled fields, test actions, one save, and safety note | `/Users/erbartos/.codex/visualizations/2026/08/26/01a03e7a-6672-7ec3-807a-c79c61a421d0/settings-prototype.html` | Primary Settings layout reference. |
| Preview wrapper for Settings | `/Users/erbartos/.codex/visualizations/2026/08/26/01a03e7a-6672-7ec3-807a-c79c61a421d0/settings-prototype-preview.html` | Openable preview. |
| Desktop Settings capture | `/Users/erbartos/.codex/visualizations/2026/08/26/01a03e7a-6672-7ec3-807a-c79c61a421d0/settings-desktop.png` | Strong visual reference for the direct-form layout. |
| Mobile Settings capture | `/Users/erbartos/.codex/visualizations/2026/08/26/01a03e7a-6672-7ec3-807a-c79c61a421d0/settings-mobile.png` | Narrow layout reference. |
| Preferred server-inventory audio routing picker, separate program/LTC choices, clock gap called out, save/revision hierarchy | `/Users/erbartos/.codex/visualizations/2026/08/26/01a03e7a-6672-7ec3-807a-c79c61a421d0/audio-node-picker-prototype.html` | Primary audio routing direction. |
| Preview wrapper for audio route picker | `/Users/erbartos/.codex/visualizations/2026/08/26/01a03e7a-6672-7ec3-807a-c79c61a421d0/audio-node-picker-prototype-preview.html` | Openable preview. |
| Desktop audio picker capture | `/Users/erbartos/.codex/visualizations/2026/08/26/01a03e7a-6672-7ec3-807a-c79c61a421d0/audio-node-picker-desktop.png` | Strong visual reference for audio form hierarchy. |
| First mobile audio picker capture | `/Users/erbartos/.codex/visualizations/2026/08/26/01a03e7a-6672-7ec3-807a-c79c61a421d0/audio-node-picker-mobile.png` | Comparison only. |
| Revised mobile audio picker capture | `/Users/erbartos/.codex/visualizations/2026/08/26/01a03e7a-6672-7ec3-807a-c79c61a421d0/audio-node-picker-mobile-v2.png` | Comparison only. |
| Revised mobile audio picker capture | `/Users/erbartos/.codex/visualizations/2026/08/26/01a03e7a-6672-7ec3-807a-c79c61a421d0/audio-node-picker-mobile-v3.png` | Comparison only. |
| Latest captured mobile audio picker pass | `/Users/erbartos/.codex/visualizations/2026/08/26/01a03e7a-6672-7ec3-807a-c79c61a421d0/audio-node-picker-mobile-v4.png` | Preferred narrow audio reference. |

### Earlier and comparison concepts

| What it shows | Full path | Status |
| --- | --- | --- |
| Earlier, thinner system-overview Dashboard | `/Users/erbartos/.codex/visualizations/2026/08/26/01a03e7a-6672-7ec3-807a-c79c61a421d0/dashboard-prototype.html` | Comparison only; prefer operator overview. |
| Preview wrapper for thinner Dashboard | `/Users/erbartos/.codex/visualizations/2026/08/26/01a03e7a-6672-7ec3-807a-c79c61a421d0/dashboard-prototype-preview.html` | Comparison only. |
| Desktop capture for thinner Dashboard | `/Users/erbartos/.codex/visualizations/2026/08/26/01a03e7a-6672-7ec3-807a-c79c61a421d0/dashboard-desktop.png` | Comparison only. |
| Dashboard run-focused capture | `/Users/erbartos/.codex/visualizations/2026/08/26/01a03e7a-6672-7ec3-807a-c79c61a421d0/dashboard-run-desktop.png` | Comparison only. |
| Earlier Show Night overview | `/Users/erbartos/.codex/visualizations/2026/08/26/01a03e7a-6672-7ec3-807a-c79c61a421d0/show-night-prototype.html` | Comparison only; prefer the Run of Show plan. |
| Preview wrapper for earlier Show Night | `/Users/erbartos/.codex/visualizations/2026/08/26/01a03e7a-6672-7ec3-807a-c79c61a421d0/show-night-prototype-preview.html` | Comparison only. |
| Desktop capture for earlier Show Night | `/Users/erbartos/.codex/visualizations/2026/08/26/01a03e7a-6672-7ec3-807a-c79c61a421d0/show-night-desktop.png` | Comparison only. |
| Earlier audio form using manual text entry | `/Users/erbartos/.codex/visualizations/2026/08/26/01a03e7a-6672-7ec3-807a-c79c61a421d0/audio-node-config-prototype.html` | Comparison only; prefer server-resolved picker. |
| Preview wrapper for earlier audio form | `/Users/erbartos/.codex/visualizations/2026/08/26/01a03e7a-6672-7ec3-807a-c79c61a421d0/audio-node-config-prototype-preview.html` | Comparison only. |
| Desktop capture for earlier audio form | `/Users/erbartos/.codex/visualizations/2026/08/26/01a03e7a-6672-7ec3-807a-c79c61a421d0/audio-node-config-desktop.png` | Comparison only. |
| Cue-sheet style Run of Show editor | `/Users/erbartos/.codex/visualizations/2026/08/26/01a03e7a-6672-7ec3-807a-c79c61a421d0/show-editor-cue-sheet.html` | Useful authoring comparison; prefer direct editing over nested overlays. |
| Preview wrapper for cue-sheet editor | `/Users/erbartos/.codex/visualizations/2026/08/26/01a03e7a-6672-7ec3-807a-c79c61a421d0/show-editor-cue-sheet-preview.html` | Openable preview. |
| Direct-flow authoring option | `/Users/erbartos/.codex/visualizations/2026/08/26/01a03e7a-6672-7ec3-807a-c79c61a421d0/show-editor-direct-flow.html` | Useful authoring comparison. |
| Preview wrapper for direct-flow editor | `/Users/erbartos/.codex/visualizations/2026/08/26/01a03e7a-6672-7ec3-807a-c79c61a421d0/show-editor-direct-flow-preview.html` | Openable preview. |
| Show-local workspace structure | `/Users/erbartos/.codex/visualizations/2026/08/26/01a03e7a-6672-7ec3-807a-c79c61a421d0/show-workspace-prototype.html` | Reference for show-scoped hierarchy. |
| Preview wrapper for show workspace | `/Users/erbartos/.codex/visualizations/2026/08/26/01a03e7a-6672-7ec3-807a-c79c61a421d0/show-workspace-prototype-preview.html` | Openable preview. |
| Early overall Operator direction | `/Users/erbartos/.codex/visualizations/2026/08/26/01a03e7a-6672-7ec3-807a-c79c61a421d0/showmesh-operator-direction.html` | Broad comparison/reference. |
| Preview wrapper for early Operator direction | `/Users/erbartos/.codex/visualizations/2026/08/26/01a03e7a-6672-7ec3-807a-c79c61a421d0/showmesh-operator-direction-preview.html` | Openable preview. |
| Original pre-overhaul Dashboard visual reference | `/Users/erbartos/.codex/visualizations/2026/08/17/01a011d5-d7db-7bb1-9dc2-8951bce52282/showmesh-operator-concept.html` | Origin of the graphite/blue-green/compact-rail direction; do not treat as current screen spec. |

The original prototype-package notes, preferred-vs-comparison decisions, and
known backend gaps are in:

- `/Users/erbartos/.codex/visualizations/2026/08/26/01a03e7a-6672-7ec3-807a-c79c61a421d0/showmesh-ui-overhaul-handoff.md`

## Implementation references by responsibility

These paths describe the real current behavior. They are not a demand to mimic
their markup in a design tool; use them to avoid designing an interaction the
application cannot honestly support.

| Responsibility | Full path |
| --- | --- |
| Routes, legacy fragment compatibility, page titles, scroll behavior | `/Users/erbartos/orca/workspaces/ShowMesh/UI-Overhaul/ui/src/app/App.tsx` |
| Global navigation, connection/session chrome, high-contrast toggle | `/Users/erbartos/orca/workspaces/ShowMesh/UI-Overhaul/ui/src/app/Layout.tsx` |
| Tokens and light/dark/high-contrast palettes | `/Users/erbartos/orca/workspaces/ShowMesh/UI-Overhaul/ui/src/styles/tokens.css` |
| Mobile-first reset, responsive shell/navigation, focus treatment | `/Users/erbartos/orca/workspaces/ShowMesh/UI-Overhaul/ui/src/styles/global.css` |
| Shared page header, state blocks, table wrapper, status strip | `/Users/erbartos/orca/workspaces/ShowMesh/UI-Overhaul/ui/src/components/SharedLayouts.tsx` |
| Shared page/table/state layout rules | `/Users/erbartos/orca/workspaces/ShowMesh/UI-Overhaul/ui/src/styles/operator-pages.css` |
| Direct Settings wrappers and permission states | `/Users/erbartos/orca/workspaces/ShowMesh/UI-Overhaul/ui/src/views/SettingsPages.tsx` |
| Settings-local max widths and layout | `/Users/erbartos/orca/workspaces/ShowMesh/UI-Overhaul/ui/src/views/settings-pages.css` |
| Existing connections/content-delivery configuration sections | `/Users/erbartos/orca/workspaces/ShowMesh/UI-Overhaul/ui/src/views/Configuration.tsx` |
| Existing Render recovery editor | `/Users/erbartos/orca/workspaces/ShowMesh/UI-Overhaul/ui/src/components/RenderSettingsPanel.tsx` |
| Existing direct Mode picker | `/Users/erbartos/orca/workspaces/ShowMesh/UI-Overhaul/ui/src/components/ShowModePanel.tsx` |
| Access, credentials, audit behavior, and permission-specific states | `/Users/erbartos/orca/workspaces/ShowMesh/UI-Overhaul/ui/src/views/Access.tsx` |
| Audio defaults, revision/save behavior | `/Users/erbartos/orca/workspaces/ShowMesh/UI-Overhaul/ui/src/views/AudioSettings.tsx` |
| Audio routing directory/discovery, loading, retry, and unavailable states | `/Users/erbartos/orca/workspaces/ShowMesh/UI-Overhaul/ui/src/views/AudioNodes.tsx` |
| Per-node audio route form, program/LTC separation, manual fallback | `/Users/erbartos/orca/workspaces/ShowMesh/UI-Overhaul/ui/src/views/AudioNodeDetail.tsx` |
| Dirty-form ownership and leave-page confirmation behavior | `/Users/erbartos/orca/workspaces/ShowMesh/UI-Overhaul/ui/src/app/UnsavedChanges.tsx` |
| Current operator UI decisions and next-session boundary | `/Users/erbartos/orca/workspaces/ShowMesh/UI-Overhaul/docs/build/OPERATOR-UI-OVERHAUL-SESSION-HANDOFF.md` |
| Operator UI architecture | `/Users/erbartos/orca/workspaces/ShowMesh/UI-Overhaul/docs/architecture/OPERATOR-UI.md` |
| Configuration store/authority decision | `/Users/erbartos/orca/workspaces/ShowMesh/UI-Overhaul/docs/decisions/ADR-039-operator-configuration-is-store-backed.md` |
| Browser API-client boundary | `/Users/erbartos/orca/workspaces/ShowMesh/UI-Overhaul/docs/decisions/ADR-014-operator-ui-is-an-api-client.md` |
| FPP authority boundary | `/Users/erbartos/orca/workspaces/ShowMesh/UI-Overhaul/docs/decisions/ADR-001-fpp-is-authoritative.md` |
| Audio program/LTC clock-domain decision | `/Users/erbartos/orca/workspaces/ShowMesh/UI-Overhaul/docs/decisions/ADR-018-program-and-ltc-share-a-clock-domain.md` |

## Recommended design-tool work plan

1. Create variables for the semantic colors, spacing, typography, radii,
   borders, focus ring, and status pairs. Build light, dark, and high-contrast
   modes before polishing individual pages.
2. Build the shell and navigation at desktop, tablet, and 390 px first. Decide
   the exact responsive rail/directory behavior and write it down.
3. Build the reusable page header, form section, field, action group, state
   block, revision detail, dialog, and evidence table components with all
   required states.
4. Design the complete Settings route set using those components. Start with
   Connections, Mode, and Audio routing because together they exercise direct
   forms, a direct picker, server-derived choices, safety language, and
   revision/conflict states.
5. Design the audio picker in two tracks: current supported inventory/manual
   fallback and the future server-resolved group/clock experience. Mark every
   future selector with its required API fact rather than inventing options.
6. Bring the Dashboard, Show Night, Monitor evidence detail, and show
   workspace into the same system. This tests whether Settings feels native to
   the product instead of like an admin annex.
7. Review every interaction for focus order, field errors, disabled reasons,
   keyboard reachability, destructive confirmation, narrow overflow, and all
   three themes. Treat browser/bench acceptance as separate from static design
   review.

## Open questions to fill in with the next pass

- What exact tablet and phone navigation composition best preserves every
  destination without duplicating the rail or hiding settings hierarchy?
- Which Settings pages deserve a local directory, breadcrumb, or neither once
  the direct-route collection is fully designed?
- What is the final information hierarchy for audit/revision detail on each
  kind of server-backed form?
- Which audio inventory facts will the coordinator expose for output groups,
  free channels, route ownership, clock groups, and verification evidence?
- Which tables need compact row disclosure versus local horizontal overflow?
- What are the final content rules for operator-facing safety, stale, conflict,
  and unavailable messages?

## Evidence boundary

The current feature branch passed focused automated UI tests, TypeScript,
lint, and production build checks. That proves automated behavior only. It
does not prove the current browser layout, accessibility, hardware discovery,
audio output, FPP behavior, or deployed show safety. The preview was left
running for human review at `http://127.0.0.1:18090/config`, pointed at the
local coordinator on port 8180; it is an implementation reference, not a
design-tool acceptance result.
