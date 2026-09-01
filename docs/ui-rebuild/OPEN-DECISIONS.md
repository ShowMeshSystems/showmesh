# Open decisions for Eric

Questions raised by the operator UI rebuild that I will not answer on my own.
Answer inline under each entry, in any later session. I read this file at the
start of every screen rebuild.

Format: each entry states what is unresolved, why it matters now, the options,
my recommendation, and what the answer unblocks. Answered entries move to
"Settled" at the bottom with the ruling and the date.

Every entry D-001 to D-019 is ruled. D-018 and D-019 were both raised during
a build, self-ruled so the screen could ship, and Eric later ruled on each.
The index below is the working list; the entries keep their full reasoning.

## Ruling index

| # | Ruling | What it obliges the rebuild to do |
|---|---|---|
| D-001 | Density ships in the kit, no UI switch | Done |
| D-002 | No build string in the chrome bar | Place it on Settings or Monitor · Capabilities |
| D-003 | Fold the five mock-less routes into mocked screens | `/assets` is a rail destination built from `Show Assets.dc.html` |
| D-004 | Sequential, one PR per screen, one worktree | Standing |
| D-005 | Dashboard keeps the mock's three blocks | Done |
| D-006 | "N items" alone; never claim nothing is stopping the show | Standing; B only when `CurrentRun.targets` is real-hardware verified |
| D-007 | Commands stay enabled, report the coordinator's refusal | Open an issue for per-command `withheldReason` |
| D-008 | List announcement cues, no fire button; disable the undeliverable-asset card from its reported fact | Open an issue for a fire endpoint |
| D-009 | Rail states what the session reports | Open an issue for per-cycle history |
| D-010 | A control the API cannot serve is built, inert, `NotWired` | Standing |
| D-011 | **B.** Build show and playlist creation | `Object Creation.dc.html` sections 1 and 2 |
| D-012 | Drop the "Resume where it left off" checkbox | `ShowsPlaylists.tsx:628` |
| D-013 | **A.** FPP rebind stays inert | Open a backlog issue |
| D-014 | **B.** Re-read the revision before a config write and refuse a stale save | Every config editor, plus a retrofit of the shipped ones. Open an issue for C |
| D-015 | **A.** Mismatch control stays inert, with a note beside it saying it is not wired | `ShowsPlaylists.tsx:475` |
| D-016 | **B.** Add the missing facts to the API | Leaves UI-only scope. Last track, see the plan |
| D-017 | **B.** Build action creation, action editing and macro creation | `Object Creation.dc.html` sections 3, 4 and 5 |
| D-018 | **B.** A playlist draft asks for the first entry the contract requires | `Object Creation.dc.html` section 2, amended by `api/openapi.yaml` |
| D-019 | **B.** Add an Administration group to the selected principal's section, with typed confirmation | `Access.tsx` Administration group |

`Object Creation.dc.html` (added 2026-08-30) is the drawn answer to D-011 and
D-017 and is normative for every creation surface, Settings and Access included.

---

## Raised 2026-08-30 while building Access

### D-019 Access has no control for four principal endpoints the API serves

**What.** `Access.dc.html` draws a principals table with a name, its
credentials, a last-used time and a state. It draws no control for changing a
principal's role, disabling one, enabling one, or resetting a password. All
four exist on the API: `POST /principals/{id}/role`, `/disable`, `/enable`,
and `/password`. The rebuilt screen ships what the mock draws, so those four
endpoints have no UI at all.

**Why now.** The screen ships either way, but it means an operator can create a
principal from the UI and then never change its role or take it out of service
from the UI. On a load-in day the remedy is `showmeshctl` or nothing.

**Options.**

- A. Ship as drawn. Four endpoints stay CLI-only.
- B. Add the four controls to the principals table, designed to match, and
  wire them. Disabling a principal is a sharp action and would need the
  typed-confirmation treatment the node-removal block uses.
- C. Draw them into the mock first, then build from the drawing.

**Recommendation and self-ruling: A, and record B for a design pass.** Adding
four undrawn controls to the one screen that governs credentials is exactly the
invented-design pattern this rebuild exists to stop, and a disable control in
particular has real failure modes worth drawing before building. C is the
right order if the capability is wanted.

Taken so the screen ships. Say if the gap matters more than the drawing does.

**Unblocks.** Nothing. Access ships either way.

**Ruling:**

**Option B**, re-ruled by Eric on 2026-09-01, superseding the 2026-08-30
self-ruling of A. Built from the drawing in `E-block-designs.md` section 5:
an "Administration" group in the selected principal's section, with role
change (Segmented over the five roles, Apply, typed confirmation), Enable
and Disable (Disable is the sharp control, typed confirmation, and the
signed-in principal cannot disable its own row), and Reset password (typed
confirmation, and the documented session/token-invalidation consequence).
`hasPassword` and `createdAt` render as detail rows in that group, not as
table columns, and "Read only" is now derived from `role === 'viewer'`
rather than left unbuilt for want of per-principal scope evidence.

---

## Raised and ruled 2026-08-30 while building the creation pattern

### D-018 A playlist cannot be created the way the mock draws it

**What.** `Object Creation.dc.html` section 2 says a new playlist is "Created
empty, then bound on its own page - a playlist with no entries is a valid
object and reads as UNBOUND in the list." `PUT /config/show.playlist/{id}` says
the opposite, in `api/openapi.yaml`: "`fpp` is required iff `runner` is `fpp`"
and "`entries` is required and non-empty". A draft built exactly as drawn is
refused by the coordinator every time, so the fpp draft also needs an instance
and an imported FPP playlist before it can be written at all.

**Why now.** It is the gate case the mock uses to teach the whole pattern, and
it is the first creation surface built.

**Options.**

- A. Build the draft exactly as drawn and let the coordinator refuse it. The
  pattern's rule 4 already says a refused PUT keeps the draft open with the
  reason. Faithful to the drawing, but creation never succeeds.
- B. Ask for the minimum the contract requires inside the same draft: the fpp
  runner picks an instance and an imported FPP playlist (from
  `GET /integrations/fpp/playlists/definitions`) and binds one first entry;
  the showmesh-audio runner picks one first cue. Everything else stays as
  drawn.
- C. Do not build playlist creation; leave the button disabled.

**Recommendation: B.** The mock makes this exact argument
itself one section later, for macros: "A macro with no steps is refused, so
step 1 is part of creation rather than something to add afterwards." A playlist
with no entries is refused for the same reason, so step 1 of a playlist is part
of its creation too. The "created empty" note is the one line of the mock the
API contradicts, and the mock's own macro rule is the tie-breaker.

**Unblocks.** Playlist creation, D-011 B.

**Ruling:**

**Option B**, ruled by Eric on 2026-08-30 while the screen was being built.
The draft asks for the first entry, and for the fpp runner the instance and
imported FPP playlist, because the contract refuses a playlist without them.

---

## Settled 2026-08-30

Ruled by Eric on 2026-08-30. The entries below keep their original wording;
each one's **Ruling** is his.

### D-017 Automation: creation and action-authoring have no drawn form

**What.** `Show Automation.dc.html` draws "New action" and "New macro" buttons,
and its action table rows carry `cursor:pointer` suggesting a click opens
something. The mock's own component script (`data-props`) exposes exactly two
aside states, `step` and `run` — there is no third `action` state captured
anywhere in the mock, so what an action row's click would open is not drawn.
Unlike a step's editor (fully drawn: action reference, step id, on-failure and
on-unconfirmed segments, local-fallback class and reason, Save macro), there
is no evidence of what fields a new action's form asks for before its
integration-specific target (`fpp`/`mqtt`/`resolume`/`audio`, each with a
different required shape) could even be chosen, and no evidence of what a new
macro's form asks for before it has a first step. `PUT /config/show.action/{id}`
and `PUT /config/show.macro/{id}` can both serve the write once a client has
an id and a payload; the gap is a UI design one, matching D-011's reading for
show/playlist creation, not a missing endpoint, so this is not the D-010
not-wired treatment.

**Why now.** All three controls (New action, New macro, and an action row's
implied click target) are drawn or implied in the block this change builds.
Per the rebuild plan step 3, a control with no home in a mock block goes here
before the page ships, rather than getting invented layout.

**Options.**

- A. Ship "New action" and "New macro" disabled with a stated reason, as
built. Action rows stay read-only (label, target summary, kind, used-by,
binding). Step editing (the mock's own drawn `step` pane) is built and
live. Honest, but creating or re-authoring an action, and creating a macro,
is not usable from the UI yet, even though the API could serve both.
- B. Design a minimal action-creation form (label, safety class, an
integration picker that then asks that integration's own required fields)
and a minimal macro-creation form (label plus a first step), and an
action-editing pane matching the step editor's own shape, none drawn in
the mock, and wire all three to the real PUTs.
- C. Drop the buttons and treat action rows as permanently read-only.

**Recommendation.** A now, matching D-011's precedent exactly. B is real,
well-scoped follow-up work once a creation-form pattern and an
integration-specific target editor exist somewhere in the kit (Settings and
Access will likely need a similar shape for their own object creation). C
loses the visibility the mock intends for the buttons.

**Unblocks.** Nothing; the tab ships at the mock's drawn state either way —
macro step editing, binding checks, run, and invoke are all live.

**Ruling:**  


**Option B**

---

### D-016 Cues and Assets: mock elements with no evidence behind them

**What.** `Show Cues.dc.html` and `Show Assets.dc.html` each draw an element
with no coordinator-reported fact behind it, distinct from D-010's
missing-endpoint controls (every item here is data, not a control):

1. **A cue's "Sequence changed in FPP" badge.** The Cues mock marks one cue
   row (`Rooftop Finale`) with a warning that its render sequence changed in
   FPP. Nothing in `api/openapi.yaml` ties a `show.cue`'s
   `outputs.render.sequence` to FPP's imported playlist definitions the way
   `ConfigShowPlaylistFPPBinding.playlistHash` does for a playlist; there is
   no captured-hash history for a bare sequence name to compare against. The
   rebuilt tab omits this badge rather than inventing a staleness signal.
2. **The Assets tab's "On node" column and the top summary strip** (`On
   target` / `Hash mismatch` / `Not synced` / `Last sync`). This is exactly
   the asset-manifest concept D-003 already ruled onto Monitor as its own
   facet (`MonitorManifest.tsx`, built). `GET /assets/manifest` reports
   `missing` / `gaps` / `extra` per node, not a per-asset "matches / mismatch
   / not synced" verdict, and `ExtraAsset` carries no `assetId`: joining it
   to a specific asset row would mean matching on `runtimeFilename`, which
   `Asset`'s own description says is not identity. The rebuilt tab reports
   the asset store's own facts (identity, hash, current/superseded, size,
   history) and links to Monitor › Manifest for node-by-node sync state
   instead of re-deriving it here with a join the schema itself warns
   against.
3. **The History pane's "Rolled back" badge on a group header.** The mock
   shows this as a standing badge on an asset's group row. `Asset` carries no
   "this became current via a rollback" flag; only the `AssetResponse` from
   the specific `POST /assets` call that performed the rollback reports
   `rolledBack`. The rebuilt tab reports `rolledBack` honestly at upload time
   (the moment the coordinator states it) and does not persist it as a
   standing badge on rows loaded from `GET /assets`, which carries no such
   field.

**Why now.** All three are drawn in blocks this change builds. Per the
rebuild plan step 3, a mock element with no home in reported evidence is
recorded here rather than invented.

**Options.**

- A. Ship the tabs without these three elements, as built.
- B. Add the missing facts to the API: an FPP-sequence staleness signal for
cues, a per-asset sync verdict (or a `wasRolledBack` flag) for the asset
store.
- C. Infer them client-side from filename or timestamp correlation.

**Recommendation.** A now. B is real work with its own design questions (does
the FPP staleness check need showmesh-node evidence per section/position, the
way a playlist binding does, since a cue's `render.sequence` has no per-entry
hash the way an imported FPP playlist entry does). C is refused: it would
mean joining on `runtimeFilename`, which ADR-028's own identity model says is
not identity, or on wall-clock proximity, which is a guess dressed as
evidence.

**Unblocks.** Nothing. Both tabs ship at this state either way.

**Ruling:** 

Add the missing facts to the API.

---

### D-014 A config write can silently erase another writer's work

**What.** `PUT /config/show.playlist/{id}` and `PUT /config/show/{id}` are both
full replacements with no concurrency control: no revision parameter, no
`If-Match`, and no `409` or `412` in either operation's responses in
`api/openapi.yaml`. If two writers edit the same object, the second write wins
and the first is gone. Neither writer is told.

**Why now.** The UI is about to become the easy way to edit configuration, and
`showmeshctl` keeps parity by design. Two people on a load-in day, or one person
and a script, is not an exotic case. The window is the whole time a form sits
open, which on a settings page is minutes.

**Options.**

- A. Leave it. Last write wins, silently. What ships today.
- B. **UI-side detection.** Re-read the object immediately before writing and
refuse the save when `currentRevision` moved since load, showing what changed.
Buildable today with no coordinator change. It narrows the window to the
round trip rather than closing it, and cannot help `showmeshctl` or any other
client.
- C. **Contract-side prevention.** Accept the read revision on the write and
answer 409 when it has moved. Closes the window for every client, not just
this one. Needs a coordinator change and an `api/openapi.yaml` change.

**Recommendation.** B now, because it is honest and cheap and turns a silent
loss into a visible refusal. C is the real fix and deserves its own issue: the
API is the public contract, and a client that cannot detect a lost update is a
contract gap, not a UI gap. A is only defensible while the UI is the sole
writer, which it is not.

**Unblocks.** Nothing. Every config editor ships either way, and every one of
them carries the hazard until this is ruled.

**Ruling:**

Build B now, open a backlog linear issue for C

---

### D-015 Playlists: should the mismatch policy be settable per playlist

**What.** `Show Authoring.dc.html` draws the "On mismatch" control (Hold, Black
and silence, Safe cue) and annotates it: "Not wired yet, this is expected to
follow Show vs Program mode rather than being set per playlist." But
`ConfigShowPlaylist` has `mismatchPolicy` today, permitted when the runner is
`fpp`, with `safeCueRef` required when it is `safeCue`. So the API supports per
playlist and the design note says it should not be.

**Why now.** The control is on a screen that shipped. It is currently drawn
inert with the mock's own note, and the stored value is written back unchanged
so nothing set by `showmeshctl` is lost.

**Options.**

- A. Inert, as built. Matches the mock exactly, including its note. An operator
cannot set the policy from the UI, but the coordinator's default applies and
`showmeshctl` can still set it.
- B. Make it live per playlist. The schema serves it today, and it is a real
show-safety behaviour: what happens when FPP plays something the playlist
cannot resolve. Contradicts the design note.
- C. Move it to Show vs Program mode, as the note intends, and remove it from
this screen. Needs that mode to exist and needs the per-playlist field
deprecated or ignored.

**Recommendation.** A now. C is what the note describes and it is a design
decision, not an implementation one. B only if you want the capability before
Show vs Program mode exists, and it is worth knowing that a value set per
playlist now may stop being honoured when C lands.

**Unblocks.** Nothing. The screen shipped.

**Ruling: **  

A now, note that it doesnt work yet nearby.

---

### D-011 Shows/Playlists: creation flows the mocks draw a button for but not a form

**What.** `Shows.dc.html` draws a "New show" button and `Show Authoring.dc.html`
draws a "New playlist" button. Neither mock draws the form behind it: what
fields it asks for, how a show's id is chosen (it is immutable once created,
per the Identity tab's own copy), or how a playlist's runner is picked before
anything else about it can be configured. `PUT /config/show/{id}` and
`PUT /config/show.playlist/{id}` can both serve the write once a client has an
id and a payload; the gap is a UI design one, not an API one, so this is not
the D-010 not-wired treatment.

**Why now.** Both buttons are drawn controls in blocks this change builds. Per
the rebuild plan step 3, a control with no home in a mock block goes here
before the page ships, rather than getting invented layout.

**Options.**

- A. Ship both buttons disabled with a stated reason ("needs a form the mock
does not draw"), as built. Honest, but creation is not usable from the UI
yet, even though the API could serve it.
- B. Design a minimal creation form (id, name for a show; runner, name for a
playlist) not drawn in either mock, and wire it to the real PUT.
- C. Drop the buttons.

**Recommendation.** A now. B is a small, well-scoped follow-up once a
creation-form pattern exists somewhere in the kit (Settings and Access will
likely need the same shape). C loses the visibility the mock intends.

**Unblocks.** Nothing; both screens ship at the mock's drawn state either way.

**Ruling:**  
  
**Option B**

---

### D-012 Show Authoring: "Resume where it left off" has no field behind it

**What.** `Show Authoring.dc.html` draws a checkbox beside the ShowMesh-audio
playlist's Repeat toggle, labelled "Resume where it left off". `showmeshAudio`
(`ConfigShowPlaylistShowmeshAudio`) has exactly one property, `repeat`
(`none`/`all`). There is no stored field for resuming a playhead position
across a restart, so this checkbox has nothing to write to.

**Why now.** The rest of the ShowMesh-audio playlist editor (repeat, entries)
is being wired in this change; per the rebuild plan and D-010, a drawn control
the coordinator cannot serve is built to its final shape and rendered inert
rather than silently dropped.

**Options.**

- A. Ship the checkbox disabled with a stated reason ("no stored field"), as
built. Honest, but resuming a playhead position is not settable from the
UI, and was never settable from the API either.
- B. Add a field for it to `ConfigShowPlaylistShowmeshAudio` (a new API
surface) and wire the checkbox.
- C. Drop the checkbox.

**Recommendation.** A now. B is a real feature (playhead resume across a
coordinator restart) that needs its own design, not something to back into
from a UI control found by inspection.

**Unblocks.** Nothing; the playlist editor ships at the mock's drawn state
either way.

**Ruling:**  
  
Drop the checkbox

---

### D-013 Show Authoring: rebinding an FPP playlist to a different imported definition

**What.** The FPP-runner playlist editor draws an "Instance" select, an "FPP
playlist" select, and a "Re-import" button beside the "Hash changed" warning.
Read together with the strip's own copy ("every binding below is held until
you re-import and reconcile"), these three controls describe a reconciliation
flow: pick a different captured FPP definition (by instance and playlist
name, or the newer hash under the same name), and carry the existing
section/position cue bindings forward onto it where they still apply. The
mock does not draw what happens to a binding whose FPP entry does not exist
in the new definition, or how a partial carry-over is shown before save.
`PUT /config/show.playlist/{id}` can store any `fpp.instanceUuid` /
`fpp.playlistName` / `fpp.playlistHash` triple; the gap is the reconciliation
behavior, not the write.

**Why now.** All three controls are drawn in the block this change builds.
Per the rebuild plan, a control with no fully specified behavior goes here
before the page ships, rather than getting an invented reconciliation rule.

**Options.**

- A. Ship all three disabled with a stated reason ("needs a reconciliation
flow the mock does not fully specify"), as built. Per-entry cue binding
and the mismatch policy stay editable; only the source rebind is inert.
- B. Design the carry-over rule (e.g., match on unchanged
section/position, drop the rest, flag what was dropped) and wire it.
- C. Wire only "Re-import" to reload the currently bound instance/playlist
at its newest captured hash (no instance/playlist change), dropping any
binding whose section/position is no longer present.

**Recommendation.** A now. C is the smallest real version of this and a
likely next step once the drop behavior in B has an answer; today it would
silently discard bindings with no confirmation, which the mock does not show
either.

**Unblocks.** Nothing; the playlist editor ships at the mock's drawn state
either way.

**Ruling:**  
  
Option A, and file a linear backlog issue on this.

---

### D-005 Dashboard: the five blocks the mock does not have

**What.** `Dashboard.dc.html` has three blocks: Readiness, Needs you, System
health. The built Dashboard had five more: Presentation path, Recent activity,
Tonight's lifecycle, a clock-skew warning, and a data-freshness notice. The
rebuilt screen ships the mock's three. The other five are not deleted, they are
parked here.

**Why now.** The screen is shipped either way. This decides whether anything
comes back and where.

**Options, one per item.**

- **Presentation path** (which surfaces are carrying the show). A. Fold into
System health as a fifth tile. B. Give it to Monitor › Fleet, which already
owns per-surface state. C. Drop it.
- **Recent activity** (the last few events). A. Drop it here; Monitor › Activity
is the stream and the rail badge is the attention path. B. Fold three lines
into Needs you. C. Restore as a fourth block.
- **Tonight's lifecycle** (the night session's phase strip). A. Drop it here;
Show Night owns the lifecycle and Readiness already names the state.
B. Restore as a fourth block.
- **Clock-skew warning.** A. Move it to the chrome bar, where it affects every
age on every screen. B. Keep it on Dashboard. C. Monitor › Fleet.
- **Data-freshness notice.** Already folded in: the page lede reads "Snapshot
1.1 s ago". No decision needed unless you want the old banner back.

**Recommendation.** Presentation path B, Recent activity A, Tonight's lifecycle
A, clock skew A. That leaves Dashboard at the mock's three blocks and puts each
fact on the screen that owns it.

**Unblocks.** Nothing. Dashboard shipped without them.

**Ruling:**

**Presentation path → B.** Monitor › Fleet. It already carries per-surface state
per row; a second copy on Dashboard is the same fact twice.

**Recent activity → A.** Drop from Dashboard. Monitor › Activity is the stream
and the rail badge is the attention path.

**Tonight's lifecycle → A.** Drop from Dashboard. Show Night owns the lifecycle
strip, and Readiness already names the state in words.

**Clock skew → A.** Chrome bar. It invalidates every age on every screen, so it
belongs to the chrome that renders those ages, not to one screen.

**Data freshness → no change.** The lede (`Snapshot 0.4 s ago · Winter Ridge 2026 is the active show`) is the whole treatment. No banner.

Dashboard stays at the mock's three blocks. That was already the intent of
drawing three blocks.

**What each ruling cost to build, checked 2026-08-29.**

- *Presentation path.* Nothing to build. Monitor Fleet's resource table already
carries every row the old block had, and more: one row per FPP instance, node
and Resolume instance, each with its health, its last report, and the same
`/monitor/fleet/<kind>/<id>` destination the old block linked to
(`screens/monitorModel.ts` `fleetRows`). The old block's render-endpoint count
survives as the node row's `render · N surfaces` detail.
- *Recent activity, Tonight's lifecycle, data freshness.* Already as ruled.
- *Clock skew.* Built into the chrome as a strip under the bar. It is not an
item inside the bar: the bar must not wrap, and D-002 protects the
now-playing group's horizontal room.

---

### D-006 "Needs you": may the count claim an item is not stopping the show

**What.** The mock's heading aside reads "2 items · neither is stopping
tonight's show". The rebuilt screen renders "2 items" only.

**Why now.** It is the most-trusted line on the most-trusted screen. To say an
item is not stopping the show, the UI has to know which resources the running
show depends on. The coordinator reports each resource's own health; it does not
report that dependency, so the claim cannot be derived today.

**Options.**

- A. Keep "2 items". The caveat lives in the empty state, where it already does.
- B. Say it only when the night session reports `degraded: false` and no item
names a resource the current run targets. This is derivable from
`CurrentRun.targets`, but only for the FPP runner, and only while a run is
in progress.
- C. Say it whenever the night session is live and not degraded.

**Recommendation.** A now, B when Show Night is rebuilt and the targets list has
been read against real hardware. C is the version that lies on the night it
matters.

**Unblocks.** The Dashboard aside, and the same line on Monitor's "Needs an
operator".

**Ruling:**

**A now, B when it is derivable. Never C.**

Render `2 items` alone. The "neither is stopping tonight's show" clause is a
claim about dependency, and the coordinator does not report dependency — so the
UI does not get to say it. When `CurrentRun.targets` can be read against real
hardware, add the clause under B's exact condition (`degraded: false` **and** no
item names a targeted resource), and only while a run is in progress. Outside a
run, no clause.

Same rule on Monitor's "Needs an operator" aside. C is the version that lies on
the one night it matters; it is off the table permanently, not deferred.

---

### D-007 Night lifecycle: the UI cannot say which command is valid

**What.** The Live Control mock disables `Start preshow` and `Start night` with
"Not valid from `live`", and disables `Power down presentation` with the
interlock that is withholding it. The rebuilt screen leaves all of them enabled
and reports the refusal the coordinator returns.

**Why.** Predicting validity means reimplementing the coordinator's own state
table in the browser: preparation epochs, monotonic finalization, interlock
overrides, degraded-session ambiguity. `nightsessioncontrol.go` spreads those
guards across many branches, and a second copy in the UI would drift. What the
UI can honestly disable today is only what `NightSessionState` reports directly.

**Options.**

- A. Leave as built. Every command is enabled; a refusal is shown with the
coordinator's own reason. Honest, but the operator finds out by pressing.
- B. Add `allowedCommands` (or per-command `withheldReason`) to
`NightSessionState`, and disable from that. One source of truth, and the mock
becomes buildable exactly as drawn. Needs a coordinator change and an
OpenAPI change.
- C. Reimplement the state table client-side. Cheapest today, wrong later.

**Recommendation.** A now, B when the coordinator work can be scheduled. Not C.

**Unblocks.** Live Control's disabled reasons, and the same treatment on Show
Night.

**Ruling:**

**A now. B is the target — open it as an issue. Not C.**

Ship every command enabled and report the coordinator's refusal verbatim. Do
not build a second state table in the browser.

The mock's disabled states are not a request to predict validity — they are the
drawing of what B looks like once `NightSessionState` reports it. So keep the
mock's copy as the string template, ready to take real data:

- `Not valid from <code>live</code>.`
- `Not valid from <code>live</code>. The night is already running.`
- `Withheld by interlock <code>projector-cooldown</code> — lamp above 40 °C. Needs an authorized override.`

When B lands it should be a data swap into those strings, not a layout change.
Prefer per-command `withheldReason` over a bare `allowedCommands` list: the
reason is the whole value of the treatment.

---

### D-008 Announcements have no way to fire

**What.** The mock gives each announcement cue a Fire button. The API has no
endpoint that fires a cue outside a Show Night transition: no
`POST /cues/{id}/fire` or equivalent in `api/openapi.yaml`. The rebuilt screen
lists the cues with their duck or interrupt policy and states the absence.

**Why now.** This is one of the mock's own controls that cannot be built. The
previous build shipped a disabled Fire button with a "planned" stamp, which is
the invented-design pattern you rejected.

**Options.**

- A. Ship the list plus the stated absence, as built. No control that cannot work.
- B. Add a fire endpoint to the coordinator and build the button.
- C. Drop the section from Live Control entirely.

**Recommendation.** A until you decide on B. Firing an announcement mid-show is
a real operator need, so B is worth its own issue; C loses the visibility.

**Unblocks.** Nothing. The screen shipped.

**Ruling:**

**A now, and open B as its own issue.**

List the cues with their duck/interrupt policy and state the absence. No
disabled Fire stamped "planned".

Two things the mock is already saying, keep them straight:

1. The **enabled** announcement buttons are the target shape for when a fire
   endpoint exists (`POST /cues/{id}/fire` or equivalent). Firing must not
   advance the run — that sentence is in the mock's lede and it is a
   requirement, not flavour.
2. The **disabled** third card ("Its audio asset has not been uploaded") is
   disabled from a *reported* fact — asset deliverability — not from a missing
   endpoint. That one is buildable today and should be built: a cue whose asset
   is undeliverable is disabled with that reason and an Upload link, regardless
   of what happens with B.

C is refused. Firing an announcement mid-show is a real operator need; losing
the visibility is worse than shipping a list.



---

### D-009 Show Night's "Tonight" rail has no per-cycle history

**What.** The mock's top rail lists the whole night: Preshow 16:30, Cycle 1
19:52, Cycle 2 20:27, Cycle 3 21:02 now. `NightSessionState` reports the cycle
the session is in, and nothing about earlier ones. The rebuilt rail shows the
current cycle, whether more cycles are open, and whether the end of night has
been requested, and says in the footnote that earlier cycles are not listed.

**Why now.** It is the first thing an operator looks at, and the mock's version
reads as a timeline of the night.

**Options.**

- A. Leave as built. The rail states what the session reports.
- B. Reconstruct earlier cycles from the event stream (`night.session` events
carry each state change). Possible today, but the stream is bounded by
retention, so an early-evening cycle can be gone by midnight and the rail
would silently show a partial night.
- C. Add per-cycle history to `NightSessionState` or a `GET /night/session/ cycles`, and build the rail as drawn.

**Recommendation.** A now. C if the timeline matters to you on the night; B
only with the retention gap shown, never silently.

**Unblocks.** Nothing. The screen shipped.

**Ruling:**

**A now. C is the fix. B only with the gap shown, and only if C is not coming.**

The rail as built is correct: current cycle, whether more cycles are open,
whether end of night has been requested, plus the footnote that earlier cycles
are not listed.

The mock's full-night rail is what we want, so C — per-cycle history on
`NightSessionState` or `GET /night/session/cycles` — is the real answer; open it
as an issue. If B is ever used as a stopgap, the retention boundary must be
drawn in the rail itself ("earlier than 18:40 not retained"), never a silently
partial night. A rail that looks complete and is not is worse than the honest
one shipped today.

---

## D-010 The standing rule for a control the API cannot serve

**Ruled 2026-08-29 by Eric, and it supersedes the rebuild plan's earlier rule.**

Where a mock draws a control or a design element the coordinator cannot serve,
**build it to its final drawn shape, render it inert, and warn loudly that it
does nothing yet.** Do not state the absence in prose instead of drawing the
control, and do not quietly leave the mock's element out.

The kit carries the treatment, so it is the same everywhere:

- `NotWiredBanner` sits above the section. It names what the control would do
and the endpoint that does not exist, verbatim.
- `NotWired` wraps a single control, forces `disabled` on it, and tags it in
place so the warning cannot be scrolled away from the button it describes.
- Amber, not red. Nothing has failed; the coordinator is simply not finished.

Two limits on the rule, both from Eric's own standing rules and neither in
tension with it:

1. **It covers controls, not data.** A number, a time or a row the coordinator
   never reported is still never invented. Where a mock draws a timeline the API
   cannot fill, the timeline is built and its unknown entries are marked
   unreported. They are not filled in with plausible values.
2. **A control that is disabled from a reported fact is not "not wired".** It is
   an ordinary disabled control with its real reason. Only a missing endpoint
   earns the not-wired treatment.

No coordinator or `api/openapi.yaml` work is in scope for the UI rebuild. The
missing endpoints are recorded here and get their own issues.

**Correction to D-009 as originally written.** Its option B claimed
`night.session` events carry each state change and that earlier cycles could be
reconstructed from the event stream. That is wrong. The night-session controller
never calls `store.AppendEvent`; the only event categories that exist are
`asset_sync`, `control_plane`, `render_pipeline` and `macro.run.prior_failure`.
There is no night-session history in `GET /events`, so B was never available.

---

## Also settled, unasked

The **Show Night Session** definition editor, the **Show Assets** coverage
roll-up, **Settings** node routing as list+detail, and **Monitor**'s playlist
definitions drill-down are drawn and in the mocks as of this round. They are not
open questions.

---

## Settled

### D-001 Density switch: ship it or drop it — 2026-08-29

**Ruling: A.** The density axis ships in the kit. Every control and table row
reads its height from `--ctrl-h` / `--row-h`, with `[data-density='compact']`
swapping 34px for 30px. No UI switches it until Eric asks for one; the specimen
exposes it for inspection.

### D-002 Where the coordinator build string lives — 2026-08-29

**Ruling: A.** The guide's §2 list is the chrome bar's contents. No build string
in the bar. It goes on Settings or the Monitor Capabilities facet, decided in
that screen. The now-playing group keeps its horizontal room.

### D-003 The five routed screens with no mock — 2026-08-29

**Ruling: fold each into the mocked screen it belongs to.** Do not invent
layout for them and do not leave them on the old stylesheets.

- **Playlist readiness** (`/monitor/readiness`) folds into the playlist
configuration page, not Show Night. It is an authoring-time verdict about a
playlist.
- **FPP playlist definitions** (`/monitor/fleet/playlist-definitions/...`) fold
into the same playlist configuration page.
- **Night sessions** (`shows/:id/night-sessions*`) are Show Night. The list and
detail routes fold into the Show Night screen.
- **Asset manifest** (`/assets/manifest`) becomes a new Monitor facet. This
amends the guide's four-facet list in §3.
- **Top-level `/assets`** stays a rail destination, per the guide's §3 Author
group, rebuilt from the `Show Assets` mock. (Not raised in the question; I am
recording the reading I am building to. Say so if it is wrong.)

### D-004 Execution shape — 2026-08-29

**Ruling: sequential, one PR per screen, in this worktree.** No parallel screen
worktrees; every screen touches the route table and the shared kit.
---

## Ruled by Eric 2026-09-01

### D-020 The show pill and the mode badge open a popover, not a modal or a Link

**What.** The rebuild guide's "nothing is a modal" rule, and the handoff's
original reading of the Mode control as a plain link to Settings, both applied
to the chrome bar's show pill (`ShowPicker`) and mode badge (`sm-mode-badge`
in `Layout.tsx`). Eric overrode both for these two controls specifically:
clicking either one opens a popover anchored to the control, listing its
options with the current one marked; the operator picks a different option and
presses Apply; a browser `window.confirm` then asks a final "yes I want to do
this" naming the exact change; on OK the write is sent.

**The three-step gesture, exactly.**

1. Click the show pill or the mode badge. A popover opens under it
   (`role="dialog"`, `aria-labelledby`), listing the options (shows from
   `listConfigObjects('show')`; program and show from the show.mode schema),
   the current one marked.
2. Pick a different option, then press Apply (a primary `Button`, disabled
   until the pick differs from current).
3. A `window.confirm` states the exact change in plain language (old and new
   show id; the mode change, plus SettingsMode's own leaving-show-mode-during-
   a-live-cycle warning sentence when that applies). Only OK sends the write:
   `putShowActive` / `putShowModeConfig`, through the same `guardedSave`
   stale-write guard every other config editor uses.

The browser `confirm` is the second safety layer, independent of the popover's
own Apply gate: even an operator who fat-fingers Apply gets one more plain-
language checkpoint naming the change before anything is written.

**Ruling:** as stated above, ruled directly, no options weighed. This overrides
the guide's "nothing is a modal" for these two controls only, and restores the
handoff's original design intent for the mode control (a picker, not a link)
over the interim reading that shipped it as a `Link` to Settings.

**Unblocks.** `Popover` (new kit element, `ui/src/kit/Popover.tsx`) and the
rebuilt `ShowPicker` / `ShellMode` in `ui/src/app/Layout.tsx`.

### D-021 The two-pane inspector becomes a floating right-side drawer, not a `Panes` column

**What.** The `Panes` two-pane layout (`ui/src/kit/Shell.tsx`, `.sm-panes` in
`shell.css`) is used across `AssetsSurface`, `Monitor`, `MonitorManifest`,
`ShowsAutomation`, `ShowsCues`, and `ShowsPresentation` to put a list on the
left and an inspector in a sticky right column, capped at 320-420px, collapsing
to one column below 1100px. Eric ruled this is too narrow: the inspector
should instead float over the page as a right-side drawer, the way UniFi's
device panels do, sized to its content rather than cramming it into a column.

**Ruling:** build a `Drawer` kit element now (`ui/src/kit/Drawer.tsx`,
`ui/src/kit/styles/drawer.css`). It renders through a portal into
`document.body`, `role="dialog"`, `aria-modal="false"` (the page stays
readable and scrollable behind it), slides in from the right under the chrome
bar, full remaining height, with internal scroll and a light scrim that closes
on click without blocking reading. Width is `'content'` (min-content, capped
at 720px), `'wide'` (960px), or an exact pixel value, clamped to
`100vw - rail width` at narrow viewports and full width below 720px. Escape
closes it; focus moves to the first focusable element on open and returns to
the opener on close.

`Panes` itself does not change in this task. Screens adopt `Drawer` for their
inspector in a following pass, one at a time; `Panes` is retired only once
nothing still uses it.

**Unblocks.** `Drawer` (new kit element). Follow-up: migrate
`AssetsSurface`, `Monitor`, `MonitorManifest`, `ShowsAutomation`, `ShowsCues`,
and `ShowsPresentation` off `Panes` onto `Drawer`, one screen at a time.

### D-022 `Panes` changes once, every screen adopts `Drawer` in one pass, and the cue editor moves into it

**What.** D-021 says `Panes` itself does not change and screens adopt `Drawer`
one at a time. Eric ruled the follow-up in full instead: `Panes` changes once
so every screen picks it up, and the cue editor on Shows &rsaquo; Cues moves
into the drawer too.

**Ruling:** `Panes` (`ui/src/kit/Shell.tsx`) keeps its list as the page body
and now renders its `aside` child inside `Drawer` instead of a column, only
while the screen has a selection. New props: `inspectorOpen`,
`onInspectorClose`, `inspectorLabelledBy`, `inspectorWidth` (`'content'`
default, `'wide'` where the editor is form-heavy). Selection stays each
screen's own state (selected row, "Editing" marker, `aria-current`); closing
the drawer clears it; picking another row swaps the drawer's content without
closing it. The old two-column grid and sticky-aside CSS are gone from
`.sm-panes`; the list keeps full width with nothing selected.

Every screen that used `Panes` adopted the drawer in this pass: `Monitor`,
`MonitorManifest`, `AssetsSurface`, `ShowsAutomation` (`'wide'` for the step
and action editors), `ShowsCues` (`'wide'`; "New cue" opens the drawer with
the draft the same way a row does), `ShowsPresentation`, and the night-session
definitions editor in `ShowsNightSession`/`ShowNight.tsx`'s
`NightSessionDefinitions` (`'wide'`). `ShowsPlaylists` and `NodeDetail` were
checked and do not use `Panes`; nothing there changed. `Panes` itself is
retired as a two-column layout as of this pass.

**Unblocks.** Nothing further; the D-021 follow-up is closed.
