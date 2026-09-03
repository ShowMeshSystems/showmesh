# ShowMesh

**An orchestration and observation layer for large holiday light displays**, built around Falcon Player (FPP), xLights, and Resolume Arena, coordinating them without replacing their scheduling, sequencing, mapping, or playback roles.

A modern animated display is not one system. It is an FPP player driving pixel controllers on a hard real-time timeline, a video engine rendering to projection surfaces, an audio chain feeding an FM transmitter, a pile of networked devices with no common control surface, and a switch that has to carry multicast correctly at 40 frames per second. Each part works. Nothing tells you whether the whole thing is *ready*, and nothing coordinates across the seams.

ShowMesh is the layer that does. And, critically, it is the layer that is **never in the timing path**, so that when it fails the show keeps running.

[![License](https://img.shields.io/badge/license-Apache--2.0-blue.svg)](LICENSE)
![Go](https://img.shields.io/badge/go-1.25-00ADD8.svg)
![Status](https://img.shields.io/badge/status-pre--alpha-orange.svg)

---

## Project status

**Pre-alpha. Not run against a live show.**

ShowMesh is under active development toward a first public pre-release. What ships is checked against [`api/openapi.yaml`](api/openapi.yaml) and the accepted decisions in [`docs/decisions/`](docs/decisions/README.md), not pinned to a date; if this table and the contract ever disagree, the contract wins.

| | |
|---|---|
| **Works today** | Coordinator and agent over MQTT with capability advertisement, Last Will liveness, and SQLite inventory. Read-only FPP polling normalized into an observation model with provenance and freshness. A versioned public REST API with an SSE change stream. Authenticated principals, roles as scope bundles, and audit attribution. Show configuration (shows, surfaces, cues, playlists, macros, logical actions) with macro and action execution tracked to a run. A three-level emergency stop: immediate stop, stop with a forced graceful shutdown, and an armed hard stop. Multi-node audio sessions (prepare, start, pause, resume, seek, stop, gain and fade, mute) on ShowMesh's own GStreamer audio engine. GStreamer-based render surfaces (apply, clear, restart, transport probe). Resolume Arena instance control, composition configuration, and crash recovery. FPP Connect playlist integration with reconciliation and readiness. A signed asset store with node and cue-catalog deployment, and a signed FPP fallback program. A CLI (`showmeshctl`) and a browser SPA, both as independent clients of that API. Docker Compose bundle. CI on Go 1.25/1.26, Linux and macOS, race detector, multi-arch image build, real-broker integration tests. |
| **Does not exist** | The general controlled-device provider model for third-party hardware such as projectors, amplifiers, or relays. Resolume is a dedicated media-engine integration, not an instance of that model. Any reconciler that automatically closes the gap between desired and observed show state, deliberately, because that loop is ShowMesh becoming a second scheduler. |
| **Not verified** | Almost everything hardware- or network-dependent, with one narrow exception: a prebuilt arm64 build of the node agent has been installed and run on a real Raspberry Pi 3B+, which proves the install path and the ABI, not any show behavior on that node. The audio engine ran once against a real M4 interface, found defects that were fixed at the unit and loopback level only, and none of those fixes has been re-verified against real audio hardware since; multi-node audio has never run with more than one physical audio node. The MultiSync wire protocol is proven against a containerized `fppd`, not against a real player, a real clock, or a real switch. No render surface, GStreamer output, or Resolume instance has been exercised against real display or projection hardware, and no write has ever been pointed at the deployed fleet. |

That gap is deliberate and documented, not a backlog that got away. The project uses an explicit **evidence ladder**: L0 assumption, L1 source-verified, L2 bench, L3 integrated, L4 resilient, and no claim is written down at a level it has not earned. Each research record in [`docs/research/`](docs/research/README.md) carries its current rung and the specific experiment that would raise it.

### Reporting an issue

Found a bug or have a feature idea? Start with the [issue templates](https://github.com/ShowMeshSystems/showmesh/issues/new/choose), and search existing issues first. Report documentation problems in the [showmesh-docs issue tracker](https://github.com/ShowMeshSystems/showmesh-docs/issues/new/choose); report security vulnerabilities privately using [`SECURITY.md`](SECURITY.md). Never include secrets in a public issue. GitHub intake may be mirrored into private Linear for internal tracking, but private tracker details stay private.

### What it took to earn the first write

Every remaining roadmap item that *does* something rather than *shows* something is a write operation, and [ADR-021](docs/decisions/ADR-021-read-api-authentication-posture.md) barred the first one until a superseding record settled authenticated identities, authorization by target and action, audit attribution, the MQTT control plane's own authorization, and a browser session model. That record is [ADR-024](docs/decisions/ADR-024-identity-authorization-and-audit.md). It lifted the bar and deliberately added no write endpoint of its own; the next step spent it, on three.

Two of its decisions say most of what the project is like. Writes always require an authenticated principal and **reads stay open by default**, because a credential problem must never cost an operator visibility of a running show: the failure is scoped to "you cannot act", not to a blank screen indistinguishable from a dead coordinator. And an authorization refusal is **not** treated as equivalent to coordinator loss. A coordinator outage is a transport failure, which is what fires a local fallback; a `403` is a successful conversation with a healthy coordinator, which fires nothing, so a stale token on a scheduler host would otherwise leave a macro's local steps unrun as well as its remote ones. Both corrections came from review, not from drafting.

The first command is FPP's own `Stop Now`, and it is **not** reported successful because FPP answered `200`. [ADR-003](docs/decisions/ADR-003-desired-and-observed-state.md) requires evidence that observed state actually moved, against an explicit deadline, and an outcome that never arrives carries a state and a reason rather than a blank. The first implementation of that got it subtly wrong in a way worth knowing about: it compared the current observation to the desired value without checking the evidence post-dated the dispatch, so a command reported `confirmed` 179 microseconds after being sent. Since FPP starts playlists on its own schedule, that could have reported "stopped" over a running show.

---

## Architecture at a glance

```
                    ┌─────────────────────────────────────┐
   browser ───────► │  Operator UI container (nginx)      │   static SPA +
                    │  serves assets, proxies /api/*      │   same-origin proxy
                    └──────────────┬──────────────────────┘   (ADR-022)
                                   │
   showmeshctl ────────────────────┤   versioned REST /api/v1  +  SSE change stream
   your script  ───────────────────┤   (ADR-014, ADR-020)
                                   ▼
                    ┌─────────────────────────────────────┐
                    │  Coordinator (Go, distroless)       │  SQLite (WAL), observation
                    │  inventory · observations · events  │  model, event history
                    └───────┬───────────────────┬─────────┘
                            │ MQTT (ADR-008)    │ read-only REST poll
                            │ retained · LWT    │
                    ┌───────▼──────┐     ┌──────▼────────────────┐
                    │  Agent (Go)  │     │  FPP instances        │
                    │  native host │     │  (authoritative       │
                    │  GPU/HDMI/   │     │   scheduler, ADR-001) │
                    │  audio/NDI   │     └───────────────────────┘
                    └──────────────┘
                            ╎
                            ╎  MultiSync UDP 32320: the timing path.
                            ╎  Never MQTT. Never through the coordinator.
```

**Stack:** Go 1.25 (coordinator, agent, CLI), TypeScript/React/Vite (Operator UI), MQTT/Mosquitto (control plane), SQLite via `modernc.org/sqlite` (storage: pure Go, because the coordinator must build CGo-free for a static distroless multi-arch image), GStreamer (media, when media lands), Apache-2.0.

---

## Quick start

Requires Docker and Docker Compose.

```sh
git clone https://github.com/ShowMeshSystems/showmesh.git
cd showmesh/deploy
cp .env.example .env
docker compose up -d --build
```

Then:

```sh
curl -s localhost:8080/api/v1/nodes | jq   # inventory
curl -s localhost:8080/api/v1/fpp   | jq   # observed FPP state
curl -N localhost:8080/api/v1/stream       # watch changes live
open http://localhost:8081                 # Operator UI
```

Or use the CLI, built from this repo: `showmeshctl nodes`, `showmeshctl fpp`, `showmeshctl events`, `showmeshctl watch`.

> **Security posture, stated plainly:** by default the API's **reads** are open to anyone who can reach the port, and the coordinator logs a warning saying so at startup. Its **writes** are not: every write requires an authenticated principal holding the named scope ([ADR-024](docs/decisions/ADR-024-identity-authorization-and-audit.md)), and there are now writes across configuration, show control, audio, and render surfaces. Reads are open deliberately, so that a credential problem never costs an operator sight of their show; close them with `SHOWMESH_API_CLOSE_READS=true`. See [SECURITY.md](SECURITY.md) and [`deploy/README.md`](deploy/README.md) before exposing it beyond an isolated show VLAN.

### Building from source

```sh
make build     # coordinator, agent, multisync probe, showmeshctl → ./bin
make check     # fmt-check, vet, lint, Go tests, UI lint/test/build/codegen check
make test      # Go unit tests only
```

`make test-integration` runs the control plane against a real Mosquitto with the agent as a real subprocess. `make test-integration-fpp` runs the collector against a containerized `fppd`; its image is a full source build, which is why it stays off the fast path and out of default CI.

---

## Repository layout

| Path | What's there |
|---|---|
| `cmd/` | `showmesh-coordinator`, `showmesh-agent`, `showmeshctl`, `showmesh-multisync-probe` |
| `internal/coordinator/api/` | Versioned wire types, handlers, SSE hub |
| `internal/coordinator/collector/` | Source-neutral collector shape + the FPP REST collector |
| `pkg/multisync/` | FPP MultiSync wire codec, listener, and remote-semantics timeline state machine |
| `pkg/observation/` | Provenance, freshness, the six-state evidence vocabulary, health states |
| `pkg/capability/`, `pkg/command/`, `pkg/mqttproto/` | Shared models and MQTT topic conventions |
| `ui/` | TypeScript SPA, its nginx image, and API types generated from the OpenAPI document |
| `api/openapi.yaml` | The machine-readable public contract, conformance-tested against real handler responses in both directions |
| `deploy/` | Compose bundle (coordinator + Mosquitto + UI) and its operator documentation |
| `bench/fpp-multisync/` | Bench scaffolding: a real containerized `fppd`. Never the product. |
| `docs/` | Architecture, ADRs, research records, build plan and log |

---

## Documentation

The design package is authoritative and predates the code.

- **[Documentation index](docs/README.md)**: start here, with a suggested reading order
- [Architecture specification](docs/architecture/ARCHITECTURE.md): components, sync model, state and command models, roadmap phases 0–4
- [Observability specification](docs/architecture/OBSERVABILITY.md): signal model, collectors, readiness evidence, alerting. Owns *what the operator surface must display*.
- [Operator UI specification](docs/architecture/OPERATOR-UI.md): owns *how the client is built*, never what it displays. The split exists to stop the two documents drifting.
- [Audio Engine specification](docs/architecture/AUDIO-ENGINE.md): entirely unverified design intent, labelled as such
- [Architecture decision records](docs/decisions/README.md) · [Research tracker](docs/research/README.md)
- [Build plan](docs/build/BUILD-PLAN.md) · [Build log](docs/build/BUILD-LOG.md): ordered steps with status, and the chronological session record
- [Engineering lessons](docs/build/LESSONS.md): defects this project has actually shipped and caught, and the rules that came out of them

---

## Design constraints

These are the decisions that shape everything else. Each links to its ADR.

**The coordinator is never in the real-time timing or media path** ([ADR-008](docs/decisions/ADR-008-mqtt-control-plane.md)). A running show must survive coordinator loss *and* broker loss. Timing never traverses MQTT; FPP MultiSync is the timing path. The test for whether the Operator UI is correctly scoped is the same shape: if every browser disappeared right now, the show continues ([ADR-014](docs/decisions/ADR-014-operator-ui-is-an-api-client.md)).

**FPP remains the authoritative scheduler** ([ADR-001](docs/decisions/ADR-001-fpp-is-authoritative.md)). ShowMesh never becomes a second scheduler. Lifecycle actions are exposed as native FPP commands.

**Desired and observed state are separate** ([ADR-003](docs/decisions/ADR-003-desired-and-observed-state.md)). A command is not successful because it was sent; success requires evidence.

**Absent evidence is stated, never omitted** ([ADR-011](docs/decisions/ADR-011-context-aware-observability.md), [ADR-020](docs/decisions/ADR-020-control-api-shape-and-change-stream.md)). Every observation carries provenance and freshness. A value the system cannot see reports *why*: never collected, collection failed, source doesn't support it, or gone stale, because a missing field renders as blank and blank reads as fine. Stale is `unknown`, never healthy.

**Nodes are modeled by capabilities, not hardware types** ([ADR-002](docs/decisions/ADR-002-capability-based-nodes.md)). Namespaced, versioned capability IDs with attributes. The UI composes views from advertised capabilities rather than fixed node classes.

**The control API is a public contract, not a UI convenience** ([ADR-014](docs/decisions/ADR-014-operator-ui-is-an-api-client.md), [ADR-020](docs/decisions/ADR-020-control-api-shape-and-change-stream.md)). Versioned REST plus an SSE change stream: SSE deliberately, because a contract you cannot inspect with `curl` drifts towards private. The stream is *not* resumable by design: any interruption forces an authoritative snapshot re-fetch, because a gap in a stream is indistinguishable from a quiet system.

**Never share UDP 32320 with a running `fppd`** ([ADR-013](docs/decisions/ADR-013-no-fpp-control-port-sharing.md)). `SO_REUSEPORT` load-balances unicast datagrams by 4-tuple hash, so a co-located listener can silently steal FPP's own unicast sync stream and desync a live show. Port sharing defaults to off; a bind conflict must fail loudly.

**Audio deliberately does not follow the MultiSync slew/jump model** ([ADR-017](docs/decisions/ADR-017-showmesh-owns-audience-audio.md)–[ADR-019](docs/decisions/ADR-019-audio-device-loss-fails-silent.md)). Nodes play complete local files on their own audio clock, never a sample-position stream; drift is corrected discretely at track boundaries, never by continuous rate manipulation. Audio device loss fails *silent*: a recorded exception to the local-fallback rule, because uncontrolled routing and gain into an FM transmitter is worse than silence.

Full set: [ADR-001 through ADR-024](docs/decisions/README.md), all Accepted except ADR-021, superseded by ADR-024. New durable constraints require a new ADR; superseding evidence requires a superseding ADR. The architecture spec is never silently edited to match new findings.

---

## Contributing

See [CONTRIBUTING.md](CONTRIBUTING.md). In short: durable constraints change by ADR, research records change by evidence, and no doc comment, log line, or document ever claims verification that has not happened.

## License

[Apache-2.0](LICENSE). Note that the NDI runtime is never vendored or linked: `dlopen` only ([ADR-010](docs/decisions/ADR-010-apache-2-license.md), [RES-006](docs/research/RES-006-linux-ndi-support.md)).
