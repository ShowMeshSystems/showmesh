# Open decisions for Eric

Questions raised by the operator UI rebuild that I will not answer on my own.
Answer inline under each entry, in any later session. I read this file at the
start of every screen rebuild.

Format: each entry states what is unresolved, why it matters now, the options,
my recommendation, and what the answer unblocks. Answered entries move to
"Settled" at the bottom with the ruling and the date.

---

## Open

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
