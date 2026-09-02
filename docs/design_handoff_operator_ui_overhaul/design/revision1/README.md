# Revision 1 mocks

Answers to the four items that had no home in the round-1 design. The owner took each
back to the design tool; this folder holds the results.

| Owner question | Mock file |
|---|---|
| 2. Night session definition editor, list, and activation | `Show Night Sessions.dc.html` |
| 3. Asset manifest gaps and extra | `Show Assets.dc.html` |
| 4. Audio node list and create | `Settings.dc.html` |
| 6. FPP playlist definitions inventory | `Monitor.dc.html` |

## What the design tool reported building

- **Show Night Session** is a NEW SIXTH workspace tab. Its default view is the definitions
  list (three definitions, the active pointer, and activation history including a cleared
  revision, meaning two days with nothing to run). The `view` tweak flips to `edit` for the
  definition editor, covering identity and show playlist, resting (end-of-night playlist,
  timeline asset, background audio with crossfade and stable item ids), enter-show and
  enter-resting as a timeline plus an editable table with barriers drawn as full-height
  rules, and site control and interlocks with the immediate/after-actions either-or and the
  nine interlock phases. It states that a definition carries no schedule.
- **Shows › Assets** gains a sequence-coverage roll-up: `yard-arch` uncovered on both judged
  nodes, `garage-wash` short on one. It says out loud that `media-garage` was not judged
  rather than counting it as clean.
- **Settings › Node routing** becomes list-then-detail with a derived "Not declared yet"
  group (`media-side` advertises `audio.engine` and `audio.output.local` with no object) and
  a Declare action that reuses the agent's node id.
- **Monitor › Fleet** gains "Playlist definitions received". This turns out to explain
  `barn-player`'s held bindings: WR26 Main Show arrived again at 20:54:03 while the show is
  still bound to the 18:02 hash. Plus a capture-drift row on Garage Loop.

## Status

The four mock files are in this folder. Checked against `ui/src` on 2026-09-01,
one of the four features is built as drawn and three are not:

- **Show Night Session** is built. It is the sixth Shows workspace tab, at
  `/shows/:id/night-session`, with the definitions editor in a wide drawer.
- **Shows › Assets** has no sequence-coverage roll-up. The tab renders the same
  asset surface the `/assets` library uses.
- **Settings › Node routing** picks one audio node from a `<select>` and edits
  it. It is not list-then-detail, and it has no derived not-declared group or
  Declare action.
- **Monitor › Fleet** has no "Playlist definitions received" block.

The three unbuilt items are drawn work with no home in the build. They are not
rulings, and nothing in `ui/src` claims they exist.
