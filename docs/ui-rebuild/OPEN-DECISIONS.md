# Open decisions for Eric

Questions raised by the operator UI rebuild that I will not answer on my own.
Answer inline under each entry, in any later session. I read this file at the
start of every screen rebuild.

Format: each entry states what is unresolved, why it matters now, the options,
my recommendation, and what the answer unblocks. Answered entries move to
"Settled" at the bottom with the ruling and the date.

---

## Open

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

**Data freshness → no change.** The lede (`Snapshot 0.4 s ago · Winter Ridge
2026 is the active show`) is the whole treatment. No banner.

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
- `Withheld by interlock <code>projector-cooldown</code> — lamp above 40 °C.
  Needs an authorized override.`

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