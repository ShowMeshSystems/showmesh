# Changelog

All notable changes to ShowMesh Core (coordinator, operator UI, and node agent) are
recorded here. The format is one bullet per line, grouped under short headings,
newest release first.

The major version names the show season (`0.x` is the 2026 season). See
[`docs/RELEASING.md`](docs/RELEASING.md) for the versioning scheme; a new season is
a new line with no compatibility promise from the last, and the release version is
not the public API version (`/api/v1` moves independently).

## Unreleased

## 0.1.0 - 2026-09-29

First pre-release. Pre-alpha.

### Control plane

- Coordinator and node agents over MQTT with capability advertisement, Last
  Will liveness, and a SQLite inventory.
- A versioned public REST API at `/api/v1` with an SSE change stream, plus
  `showmeshctl` and a browser operator UI as independent clients of that API.
- Authenticated principals, roles as scope bundles, audit attribution on
  every write, and a bootstrap code in the data volume for the first claim.
- Versioned configuration objects with optimistic concurrency and revision
  history across roughly twenty kinds, usable from both the API and
  `showmeshctl`; every config kind now supports a tombstoned delete, and
  `showmeshctl` sends the optimistic-concurrency precondition by default on
  every write.
- A system-wide setup and show mode.
- A refused config write now says which field was rejected and why, including
  an audio setting or an attempt to move a show object into a different show.
- `showmeshctl` gained an audio session show command and an
  `audio.node.silence` command, so the CLI can read and act on audio state at
  parity with the operator UI.

### Show authoring and control

- Shows, surfaces, cues, playlists, and an active show selection, all as
  configuration objects.
- Show actions, show macros, action bindings, and macro runs with per-run
  status; a show cue now fires its show actions as soon as its activation
  starts.
- Night sessions, including the night session write path and night commands;
  each night cycle's outcome is now recorded and shown on the Show Night
  screen.
- A three-level emergency stop: stop, stop with power down, and a hard
  stop. Every level blacks every render surface and silences every audio
  node. A level 1 stop holds the night session instead of ending it, and
  Resume starts the show playlist from its first song.
- A new media playlist configuration kind, with API routes, CLI verbs, and a
  Playlists screen in the operator UI; a night session can reference a media
  playlist for its background audio, with a picker on the Night screen.
  Deleting a media playlist still in use by a running night is refused.
- start-night reruns a stale readiness check instead of refusing outright,
  and readiness can report a ready-with-warnings outcome instead of only
  ready or not ready.
- Cue catalog deploy runs automatically and warns on a stale node catalog
  before and during a night, does not stall waiting on an FPP run that is
  positively unavailable, and an operator can now override an exclusive
  deploy conflict from the UI.
- A dangling macro step or playlist entry is reported before it runs, and
  saving a Show Night audio-asset reference that resolves to nothing is
  refused up front.
- Each node and FPP or Resolume instance now reports whether it takes part in
  tonight's show, shown on attention rows in the operator UI and in
  `showmeshctl`.
- An operator can delete a show, action, macro, cue, playlist, surface, night
  session, or audio node from the operator UI, not only through the API.
- An announcement cue can be fired directly from Live Control.

### Weather delay

- A weather delay: hold every player dark, alert and report the status of
  each power group, start the delay over a signed request, and resume the
  night afterward; a trigger can also cancel the night outright.
- Operator UI controls for starting, resuming, and canceling a weather delay,
  a banner shown on every screen while one is active, and per power group
  status; any screen can answer a weather trigger's question, and the cancel
  alert plays in full even when it replaces an in-progress delay alert.

### Audio

- Audio node configuration and a GStreamer audio engine on the node agent,
  built natively where CGo and GStreamer are available.
- Audio session lifecycle over the API: prepare, start, pause, resume, seek,
  advance, stop, clear, and apply.
- Per-session gain, gain fades, and output mute and unmute.
- A cue's audio and announcement outputs can now target several nodes at
  once, all started from one shared clock instant; the Cue editor,
  `showmeshctl`, and the Night screen let an operator pick the target nodes,
  and a node that cannot start aligned or is missing the audio asset is
  named in the response.
- A cue's audio starts on every target node directly from FPP's own
  MultiSync START packet, with a coordinator-triggered fallback if a node
  misses it; the audio asset is delivered to every listed target node ahead
  of time.
- A show now has one audio node list, with individual outputs excluding
  themselves from it rather than opting in; show detail, cues, the night
  bed, and automation all inherit that list.
- Program-to-LTC alignment is measured and reported per node, with a
  long-run drift recording and a warning before showtime when drift crosses
  the configured threshold.
- An audio node can discipline PipeWire's clock from PTP and run the audio
  pipeline on the node's own hardware clock, with calibrated per-output
  latency applied at the scheduled start instant.
- The resting background bed's fade-down now completes when the show segment
  ends, instead of starting there, and the bed's clear is withheld until its
  own fade has settled.
- Fixed: Pause now freezes at the buffer actually rendered instead of the
  start of the buffer, so resuming no longer replays about 40ms of audio; a
  scheduled resume is now prepared ahead of its instant instead of after it.
- Fixed: a scheduled single-media session no longer crashes the node agent
  when it ends, and stopping an audio node now releases every engine branch
  cleanly.
- An announcement now ducks the background bed before it starts playing,
  instead of after.
- Uploaded audio is transcoded to a 48kHz, 16-bit, stereo WAV rendition for
  playback, and duration is read from the file's own header when it cannot
  otherwise be determined.
- Node routing keeps LTC on its configured output channel under PipeWire,
  and the node list and Node Detail screen report each node's PTP frequency
  steering and clock sync status.

### FPP

- Read-only FPP polling normalized into an observation model that carries
  provenance and freshness on every value.
- FPP MQTT ingest, playlist definitions, playlist entry observations,
  playlist readiness, and reconciliation reporting; FPP MQTT and playlist
  observations are now delivered as they are published instead of waiting
  for the next poll, and an unrecognized field on the observation or
  playlist-definition route is now reported instead of silently dropped.
- FPP Connect: registering with a player, uploading, and holding state; an
  operator can trigger a playlist-definition republish, and see which show
  playlists bind each FPP playlist definition.
- Per-instance fallback programs with acknowledgement.
- A native FPP plugin, released from its own repository, now at 0.1.5. It
  adds seven FPP commands, from preparing the site to powering down the
  presentation, so an FPP schedule entry can run the whole night.
- The coordinator states plainly when it refuses an FPP instance's playlist
  reports.
- Fixed: a show start that FPP never confirms no longer holds the night
  session forever.

### Projection and Resolume

- Resolume instances, composition configuration, actions, and recovery with
  restore.
- Render surfaces on a node: apply, clear, restart, and a transport probe.
  Surfaces now black out between songs and on a stop while keeping their
  assignment, and the emergency blackout no longer waits on a composition
  check first.
- The release now builds the NDI plugin for the render pipeline and ships it
  in the node package, and the installer's preflight names the install
  command when a render node is missing it.

### Assets

- Asset upload, content retrieval and manifest, per-node asset inventory, and
  cue catalog deploy with acknowledgement.
- An operator can see and remove a node's unused assets, and delete a
  registered asset entirely, both from the API and the operator UI.
- The Assets page shows every file's state on each node and why that node
  holds it.
- Asset upload now infers its media type from the chosen file.
- An operator-invoked node resync route triggers repair from a fresh
  inventory report instead of a stale one.

### Nodes and install

- A single command installs a coordinator, a render node, an audio node, or
  both, on Debian 13; a node joins the coordinator with a one-time
  enrollment code instead of manual configuration.
- The node list reports each audio node's program and LTC channel placement,
  and the node inspector gained a button to declare a new node.
- The node deploy script adopts an existing service account instead of
  failing when one is already present.

### Operator UI

- Operator-facing messages across the coordinator, CLI, UI, and FPP
  integration were rewritten to plain, fact-then-action language.
- A phone-width responsive pass, several narrow-viewport overflow fixes, and
  a switch from a fixed page width to one that fills the available width.
- The dashboard can filter out unselected FPP and Resolume instances, and
  Node Detail now shows drift recording, PTP sync status, and each audio
  session's now-playing status.
- The chrome bar shows the FPP run's real name instead of an internal item
  id, and its progress bar now tracks FPP's actual elapsed position.
- Monitor's FPP reconciliation verdict now refreshes as playlist entries
  advance instead of only on load.

### Build and test

- CI on Go 1.25 and 1.26 across Linux and macOS with the race detector, a
  CGo-free coordinator build, and a multi-arch container image.
- Real-broker integration tests against a real Mosquitto with the agent as a
  real subprocess.
- A Docker Compose bundle for local and show-network deployment.
- Pushing a release tag now builds and publishes the coordinator and UI
  images, the node agent packages, the one-command installer, and a matching
  pre-release entry, with no manual step.

There is deliberately no reconciler that closes the gap between desired and
observed state. This is a pre-release whose verification level varies by
subsystem, recorded in the research records and the build log, and no part
of it has been accepted against a full live show run.

### Known limitations

- There is no armv7 node agent package yet. Release assets are amd64 and
  arm64 only.
- The FPP plugin (0.1.5, versioned separately in
  `ShowMeshSystems/fpp-showmesh`) needs its coordinator URL and credential
  written by hand on the player. Setting them from the plugin page arrives
  in a later release.
- The FPP plugin's local fallback refuses to run when
  `/etc/showmesh-fpp-plugin` is not owned by root.
- The end-of-night resting playlist does not repeat unless the night session
  sets `endOfNightRepeat`.
- Pre-alpha: there is no compatibility or migration promise before 1.0.
