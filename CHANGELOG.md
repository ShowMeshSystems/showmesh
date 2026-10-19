# Changelog

All notable changes to ShowMesh Core (coordinator, operator UI, and node agent) are
recorded here. The format is one bullet per line, grouped under short headings,
newest release first.

The major version names the show season (`0.x` is the 2026 season). See
[`docs/RELEASING.md`](docs/RELEASING.md) for the versioning scheme; a new season is
a new line with no compatibility promise from the last, and the release version is
not the public API version (`/api/v1` moves independently).

## Unreleased

## 0.5.1 - 2026-10-18

Patch release on the 0.x line. Pre-alpha. FPP plugin 0.3.0 is still the
matching plugin. Upgrade the coordinator, the operator UI and every node agent
together; the plugin needs no change.

### Night and show control

- The first show of the night fades the lights out of pre-show's resting
  sequence, the same as every later show. It used to cut hard from the resting
  look into the first show whenever a lights fade-out was set.
- A changed cue list reaches its node by itself between shows while FPP is
  idle. It used to wait for the operator to press Deploy.
- Show Night shows Sent as soon as a night command goes out, and Run readiness
  shows the readiness result, such as Passed with a warning, in place of
  Accepted.
- Readiness no longer reports Passed with a warning on every night with a
  lights fade. The fade checks name the fade and pass; the fade-length check
  still warns when the fades do not fit in the resting sequence.

### Audio

- A node that plays a cue's audio but renders none of it starts the audio on
  FPP's MultiSync START. It used to start every song late from the
  coordinator's fallback. Night readiness warns when a cue's audio has no FPP
  sequence to start on.

### Clock

- A node acting as its PTP grandmaster no longer reports a stale clock offset
  left over from when it followed another clock.

## 0.5.0 - 2026-10-17

Feature release on the 0.x line. Pre-alpha. FPP plugin 0.3.0 is still the
matching plugin. Upgrade the coordinator and every node agent together,
because the `0.x` line makes no compatibility promise between versions.

### Audio

- A background music track, in a night session or a media playlist, is now
  any audio file in the show. Upload it once, show-wide, and pick it by name.
  It plays on the night session's own nodes, or on the show's audio nodes
  when the session names none; each node uses its own upload of the track if
  it has one, then the show-wide upload, then another node's copy. Items
  already pinned to one node keep working.
- A show-wide audio file is sent only to audio nodes, so it no longer shows
  Missing for FPP players and render nodes.
- The "Background audio files on every node" readiness check now counts a
  show-wide upload.
- Night readiness fails when the show's audio would play nowhere: a
  background track or cue sent to a node with no audio settings, a node whose
  cue list gives a timecode without its audio, or an audio node playing on
  the wrong clock.
- Audio nodes on PipeWire report all of their outputs, and Node routing picks
  the PipeWire output from that list. A new node can get its first audio
  routing from Node routing.

### Clock

- Nodes report their network interfaces, and the coordinator refuses clock
  settings that name an interface the node does not have or that would start
  a second PTP service. A node that refuses its clock settings reports a
  failed clock with the reason.
- Node routing picks the PTP network interface from the node's list, fills
  the PTP hardware clock device, and shows the node's answer after a save.

### Brightness

- The display has one brightness setting, kept by the coordinator and
  applied to every FPP, including one added later. Set it on the new
  Settings > Brightness tab or with `showmeshctl fpp set-brightness-ceiling`.
  Night fades and lighting cue gain write every FPP.
- With FPP plugin 0.3.0 the coordinator sets brightness on every FPP. A
  change made on one FPP, from its plugin page or scheduler, spreads to the
  others only with a plugin release newer than 0.3.0.
- The per-FPP brightness ceiling and transition gain routes are replaced by
  `GET /api/v1/fpp/brightness`, `PUT /api/v1/fpp/brightness/ceiling` and
  `POST /api/v1/fpp/brightness/transition-gain`.

### Render

- Render nodes draw the resting sequence during resting instead of black,
  when they hold the resting sequence's file.
- Editing a running surface's channel range, size, frame rate or output
  re-applies it on the node.
- Re-uploading a sequence redeploys the cue catalog that uses it.

### FPP

- One Update from FPP button brings an edited FPP playlist into the show
  playlist, replacing Re-import and Review changes.
- An FPP whose ShowMesh plugin is not reporting shows as degraded, and says
  whether the plugin is not paired, silent, or stopped.

### Dashboard and Monitor

- Operators can add NDI sources by hand, by name and an optional address,
  for senders discovery cannot reach. `showmeshctl preview ndi` covers them.
- A preview gateway with a missing or misnamed NDI runtime says so on the
  Dashboard and in `showmeshctl preview list`.
- A Dashboard preview tile can open large in a popup window.
- The Dashboard shows lifecycle, playback, audio sync, timecode and
  temperature blocks. Values the API does not serve yet are marked Not wired.
- The Resolume tile on the Dashboard and on Monitor says how many instances
  are not healthy.
- Monitor Activity keeps its columns aligned, loads older operator actions,
  and filters by type and text.

### Show editing

- Resolume action fields pick decks, clips, columns and layers from the
  stored composition.
- View version shows which fields changed from the version before.
- Fire hard stop stays usable while a Stop is in progress.
- Messages on Live Control, Monitor, Node detail, the show editors, Settings
  and sign-in read as plain sentences that state the fact and then the
  action.

### Installer

- The installer can bridge the built-in broker to a Home Assistant broker
  with `--home-assistant`, and the bridge survives upgrades. The FPP broker
  login can now read every topic and write all of FPP's own topics.
- A new "Audio and render node" role installs both on one machine.
- A stray Enter no longer answers the next installer prompt.

## 0.4.1 - 2026-10-16

Patch release on the 0.x line. Pre-alpha. FPP plugin 0.3.0 is still the
matching plugin. Upgrade the coordinator; nodes and the plugin need no change.

### FPP

- The coordinator keeps the current definition of every playlist on an FPP.
  It used to keep only the newest sixteen rows per player across all
  playlists, so a player with more than sixteen playlists lost the rest on
  every plugin sweep, and an edit to one of those playlists never arrived.
  The bound of sixteen now applies only to superseded versions of a playlist.
  After upgrading, reload the plugin on each FPP once so it posts every
  playlist again.

## 0.4.0 - 2026-10-15

Feature release on the 0.x line. Pre-alpha. FPP plugin 0.3.0 is still the
matching plugin, and ShowMesh Companion 0.2.0 is the first companion that
uploads crash reports. Upgrade the coordinator and every node agent together,
because the `0.x` line makes no compatibility promise between versions.

### Companion

- A ShowMesh Companion can upload Resolume Arena crash reports to the
  coordinator using a token from a principal with the new `companion` role,
  which can do nothing else.
- An operator can list and read uploaded crash reports through the API and
  with `showmeshctl companion-reports list` and `get`, without logging into
  the render computer. The coordinator keeps the newest 200 per companion.

### Dashboard

- The Dashboard can show low-rate live previews of render node NDI output,
  Arena NDI output and RTSP cameras, through a new preview gateway that runs
  beside the coordinator or on its own host. Nothing in the show depends on
  it, and losing it only changes what the tiles say.
- An operator can reorder and hide preview tiles from the Dashboard or with
  `showmeshctl preview layout`. A new source is added at the end of the grid.
- Settings has a new Dashboard tab for the gateway address and token and the
  camera list, and `showmeshctl preview` covers the same settings.
- A tile whose picture has stopped updating says how old its last frame is
  and is marked Stale after 10 seconds.
- The preview gateway is published as its own image,
  `ghcr.io/showmeshsystems/showmesh-preview-gateway`, for amd64 and arm64.
  The NDI runtime is never in the image; mount it from the host.

### Installer

- A render node that also plays audio can be installed in one run: the
  render role sets up the PTP audio clock when `--ptp-interface` is given or
  the operator says yes at the new prompt. Render-only installs are unchanged.
- The coordinator installer can serve the operator UI on port 80 with
  `--ui-port 80` or at the new prompt, so a browser opens the coordinator with
  no port. The choice is kept on later runs and can be changed.

### Releases

- Every merge to `main` publishes an edge build: coordinator, UI and preview
  gateway images tagged `edge`, and node agent and installer files on the
  `edge` GitHub prerelease, installed with the same one-line installer from
  that release.
  Each build reports its version as the last release plus the commits since.

## 0.3.1 - 2026-10-14

Fix release on the 0.3 line. Pre-alpha. FPP plugin 0.3.0 is still the
matching plugin; upgrade the coordinator and every node agent together.

### Audio

- An audio node that loads a song too slowly to make the shared start time
  joins the song where the other nodes are, after a short gap, instead of
  playing behind them for the whole song.
- Every audio node has its next song prepared before it starts, including
  a node that sits out the current song or the opening song of the show.

## 0.3.0 - 2026-10-14

Third pre-release. Pre-alpha. FPP plugin 0.3.0 is the matching plugin: it
ships the brightness excluded channel ranges and the Prepare site stop
option. Upgrade the coordinator, the node agents and the plugin together,
because the `0.x` line makes no compatibility promise between versions.

### FPP

- An operator can list the channel ranges a player's brightness ceiling
  never changes, so fixture channels such as pan, tilt, gobo and speed keep
  their own values. The list is set from the FPP instance page, the API or
  `showmeshctl`, and reads as saved only once the plugin reports it back.
- Prepare site can also stop whatever FPP is playing on the night's players.
  Show Night and Live Control check the stop by default; the API and
  `showmeshctl` stop only when asked.
- An FPP playlist can be created before any cue exists, and can be moved to
  FPP's newest copy of the playlist from the Playlists screen or with
  `showmeshctl playlist move-to-newer-fpp`, after a preview of what happens
  to each cue.
- A playlist cue saved without a sequence name follows the sequence it was
  on when entries are added or moved, instead of staying at the same
  position.
- When a plugin runs a show from its local fallback, the coordinator leaves
  that player alone until the playlist ends and says so on Show Night and in
  `showmeshctl`.
- A ShowMesh node fills its row on an FPP player's MultiSync page with its
  status, sequence, elapsed time and agent version.
- A song uploaded through xLights FPP Connect with Media ticked becomes a
  show-wide audio asset named after its sequence, and reaches every audio
  node.

### Audio

- Audio no longer drifts late on a node whose agent has been up for days.
- An audio node whose agent restarts mid-session rejoins at the scheduled
  position when its clock is locked, instead of at its stored position.
- Background music follows Start pre-show sooner, and every node still
  starts it at the same instant.
- A night that stops on its schedule clears its background music on every
  node it was sent to, and one unreachable node no longer holds up the rest.
- A missing-file fault clears once the file lands, and an audio interface
  that disappears stops reading Available and raises a readiness warning.
- A node that can send timecode but has no LTC channel set gets a readiness
  warning.

### Night and show control

- The night fades the lights out into each show and back into resting,
  using two optional fade times on the night session.
- A weather delay started on a node while the coordinator was down is taken
  up when the coordinator returns, and a weather trigger that times out
  starts the delay instead of crashing the coordinator.
- A second emergency stop confirms straight away even while an earlier
  stop's cleanup is stuck.
- The emergency stop is on the top bar on every screen.

### Render

- A render command the node failed or refused is reported not confirmed
  with the node's reason, and `showmeshctl render apply` exits non-zero.
- Live Control says what each render surface is putting out, and Node
  Detail says when a node holds no assignment for a render surface.
- A cue catalog redeploy re-establishes a render surface that lost its
  assignment and reports nothing, instead of replaying the first
  confirmation. The show editor's Cues page can deploy the active show's
  cue catalog.

### Operator UI

- Operator messages across the coordinator, node agent, `showmeshctl` and
  the UI are one short line that states the fact and then the action, and
  audio outputs show a short name.
- Playlist readiness lists each warning on its own line with a count.
- Weather delay settings have their own tab and the banner stays on
  screen. Access moves under System, Settings tabs share one layout, and
  activating a show happens in an inspector opened from the Shows page.
- The cue form picks its sequence from a list of uploaded sequences and
  warns when a node will play an announcement cue as show audio. The night
  session editor picks background audio with one choice: none, inline items
  or a media playlist.
- The Signals tile counts every signal and shows failed ones, and a signal
  with nothing to measure reads N/A instead of unobserved.
- At phone width the top bar keeps only the brand, now playing and the
  connection.

### Install and versions

- A fresh install saves the asset delivery address and, with the built-in
  broker, the FPP event feed, so neither needs a manual step. Settings >
  Content delivery opens as a form with defaults.
- The coordinator reports the version every component runs, warns about
  each node it does not accept, and says when a newer release is available,
  on Monitor and in `showmeshctl system versions`.

### API and CLI

- Playlist readiness returns `warnings` as a list. The old `warning` field
  stays, marked deprecated, holding the warnings joined.

### Build and release

- Built with Go 1.26.9 and golang.org/x/net 0.60.0 to clear published
  vulnerabilities.

### Known limitations

- There is no armv7 node agent package yet. Release assets are amd64 and
  arm64 only.
- Pre-alpha: there is no compatibility or migration promise before 1.0.

## 0.2.0 - 2026-09-30

Second pre-release. Pre-alpha. Upgrade the coordinator and the FPP plugin
together: plugin 0.2.0 pairs by code, which needs this coordinator, and the
`0.x` line makes no compatibility promise between versions.

### FPP

- An FPP plugin pairs with the coordinator by the code it shows on its own
  page, and receives its own scheduler credential without the operator ever
  handling it. The coordinator URL and credential no longer have to be
  written by hand on the player.
- The coordinator can set a player's brightness ceiling through the plugin
  and reports the ceiling only once the plugin reads it back; each player's
  brightness is polled and shown on Live Control and Settings > Connections.
  `showmeshctl fpp pair` and `showmeshctl fpp set-brightness-ceiling` do the
  same from the CLI.
- The Playlists tab Re-import button asks the FPP host to send a playlist
  again and reports whether it was sent, landed, or could not be confirmed.

### Audio

- An audio node that loses the background bed mid-night, after an agent
  restart or a host reboot, is given it again and rejoins at the track and
  position the other speakers are playing. An operator's own stop of the bed
  is respected and not undone.
- A node that missed the background bed because its file had not arrived yet
  gets it once the file lands, instead of staying silent for the night.
- Show Night and `showmeshctl night status` list the background audio a night
  will play before it starts, and name every speaker that should be playing
  the bed and is not, with the reason.
- Audio nodes report their output channels, and Settings > Node routing offers
  program and timecode channels from what the node actually has, with manual
  entry kept for older agents. `showmeshctl audio node choices` shows the same
  choices.
- A refused audio setting shows as a warning in words on Settings Audio and on
  Node Detail.

### Projection

- A planned sequence change draws the next sequence with no black gap: the
  render node opens the next playlist entry's sequence ahead of time and
  switches on the first frame MultiSync names it.
- A second render surface on the same node that shares output channels or an
  NDI name with an existing one is refused, and the message names the
  conflict.
- A render node refuses a surface whose frame timing differs from its other
  surfaces. A cue that cannot move every surface together holds the
  mismatched surface black and says why.
- Node Detail can add, edit and remove a render node's surfaces with the same
  editor the Shows screen uses.

### Nodes and install

- Operators mint, list and cancel node enrollment codes from Settings > Node
  enrollment, so adding a node no longer needs a shell on the coordinator.
- The installer opens the ports ShowMesh needs in an active ufw or firewalld.
  A hand-written nftables drop policy is left alone with the exact rules
  printed; `--firewall` installs a ShowMesh nftables table and
  `--no-firewall` skips firewall handling.
- The installer keeps a log at `/var/log/showmesh-install.log`, prints the
  underlying error when a step or check fails, and draws the approved layout
  by default. Ctrl-C now stops the running step's command.
- Upgrading a coordinator backs it up first, refuses while a night is running,
  leaves the old coordinator running if the upgrade fails, and prints each
  node's upgrade command.
- Fixed: every coordinator install from the 0.1.0 bundle stopped at the
  broker login check because the bundle left out the broker's start script.

### API and CLI

- `GET /macro-runs` with a show or state filter now returns matching runs
  older than the newest 500.

### Build and release

- The release publishes the operator UI image for arm64 as well as amd64.
- The NDI plugin build works when CI has restored a cached build directory,
  which broke the first 0.1.0 tag run.
- The FPP plugin releases from its own repository,
  `ShowMeshSystems/showmesh-fpp-plugin`, so every tag in this repository is a
  Core release.

### Known limitations

- There is no armv7 node agent package yet. Release assets are amd64 and
  arm64 only.
- The FPP plugin's local fallback refuses to run when
  `/etc/showmesh-fpp-plugin` is not owned by root.
- The end-of-night resting playlist does not repeat unless the night session
  sets `endOfNightRepeat`.
- Pre-alpha: there is no compatibility or migration promise before 1.0.

## 0.1.0 - 2026-09-28

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
