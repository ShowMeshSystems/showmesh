# Open decisions for Eric

Questions raised by the operator UI rebuild that I will not answer on my own.
Answer inline under each entry, in any later session. I read this file at the
start of every screen rebuild.

Format: each entry states what is unresolved, why it matters now, the options,
my recommendation, and what the answer unblocks. Answered entries move to
"Settled" at the bottom with the ruling and the date.

---

## Open

(none)

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
