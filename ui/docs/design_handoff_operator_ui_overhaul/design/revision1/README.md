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

**The mock files are not in this folder yet.** They could not be fetched: `DesignSync`
requires an interactive `/design-login`, and the updated bundle had not been downloaded when
this was written. The four features remain mounted and working at their pre-overhaul routes
so no control is unreachable in the meantime.

To land them: run `/design-login`, or unzip the updated bundle here.
