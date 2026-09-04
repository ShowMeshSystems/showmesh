# Changelog

All notable changes to ShowMesh Core (coordinator, operator UI, and node agent) are
recorded here. The format is one bullet per line, grouped under short headings,
newest release first.

This project is pre-1.0. See [`docs/RELEASING.md`](docs/RELEASING.md) for the
versioning scheme; 0.x carries no compatibility promise between releases, and the
release version is not the public API version (`/api/v1` moves independently).

## Unreleased

### Audio

- The resting background bed's fade-down now completes at the show boundary
  when a fade-out is configured, instead of starting there.

### Show authoring and control

- start-night now runs a fresh readiness pass when the stored one has aged
  out, instead of refusing.

## 0.1.0 - 2026-09-03

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
  `showmeshctl`.
- A system-wide setup and show mode.

### Show authoring and control

- Shows, surfaces, cues, playlists, and an active show selection, all as
  configuration objects.
- Show actions, show macros, action bindings, and macro runs with per-run
  status.
- Night sessions, including the night session write path and night commands.
- A three-level emergency stop: stop, stop with power down, and an armed
  hard stop.

### Audio

- Audio node configuration and a GStreamer audio engine on the node agent,
  built natively where CGo and GStreamer are available.
- Audio session lifecycle over the API: prepare, start, pause, resume, seek,
  advance, stop, clear, and apply.
- Per-session gain, gain fades, and output mute and unmute.

### FPP

- Read-only FPP polling normalized into an observation model that carries
  provenance and freshness on every value.
- FPP MQTT ingest, playlist definitions, playlist entry observations,
  playlist readiness, and reconciliation reporting.
- FPP Connect: registering with a player, uploading, and holding state.
- Per-instance fallback programs with acknowledgement.
- A native FPP plugin, released from its own repository.

### Projection and Resolume

- Resolume instances, composition configuration, actions, and recovery with
  restore.
- Render surfaces on a node: apply, clear, restart, and a transport probe.

### Assets

- Asset upload, content retrieval and manifest, per-node asset inventory, and
  cue catalog deploy with acknowledgement.

### Build and test

- CI on Go 1.25 and 1.26 across Linux and macOS with the race detector, a
  CGo-free coordinator build, and a multi-arch container image.
- Real-broker integration tests against a real Mosquitto with the agent as a
  real subprocess.
- A Docker Compose bundle for local and show-network deployment.

There is deliberately no reconciler that closes the gap between desired and
observed state. This is a pre-release whose verification level varies by
subsystem, recorded in the research records and the build log, and no part
of it has been accepted against a full live show run.
