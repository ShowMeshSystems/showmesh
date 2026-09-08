# Architecture Specification

[Documentation index](../README.md) · [Observability specification](OBSERVABILITY.md) · [Operator UI specification](OPERATOR-UI.md) · [Audio Engine specification](AUDIO-ENGINE.md) · [Research tracker](../research/README.md) · [Decision records](../decisions/README.md)

Status: Draft architecture baseline  
Audience: Maintainers, contributors, show designers, and integration authors

## 1. Vision

ShowMesh is an open-source orchestration and observation layer — a control plane — for advanced synchronized displays. It makes FPP, xLights, Resolume, media nodes, audio devices, projectors, and supporting infrastructure behave like one observable system while allowing each product to retain its established job.

The platform is intended to scale in both directions: from a small display using repurposed Raspberry Pis to a multi-node installation feeding mapped projectors through Resolume. A large reference installation may shape early testing, but it must not become the platform's minimum topology.

The platform is not a sequencer, a replacement scheduler, a new projection mapper, or a requirement to abandon existing show files.

## 2. Principles

### 2.1 Enhance, do not replace

- xLights remains the sequencing and content-authoring environment.
- FPP remains the authoritative scheduler and playlist engine.
- Resolume remains the projection composition and house-mapping environment where deployed.
- The platform connects, observes, validates, and coordinates these systems.

See [ADR-001](../decisions/ADR-001-fpp-is-authoritative.md).

### 2.2 Capabilities over hardware identities

A node advertises what it can do, not what model of computer it is. Workloads are assigned against capabilities such as `video.render`, `matrix.render`, `transport.ndi.send`, `display.hdmi`, `audio.engine`, or `audio.output.ltc`.

This permits reassignment after hardware failure and avoids embedding x86, Raspberry Pi, Windows, or Resolume assumptions into core interfaces. See [ADR-002](../decisions/ADR-002-capability-based-nodes.md).

### 2.3 Desired and observed state are separate

The coordinator records what should be true and compares it with what agents and adapters report. A command is not successful merely because it was sent. Success requires evidence that the target reached the requested state or an explicit statement that confirmation is unavailable.

See [ADR-003](../decisions/ADR-003-desired-and-observed-state.md).

### 2.4 Local autonomy protects the show

The coordinator must not be in the real-time timing or media path. FPP, nodes, and Resolume should continue a running show during coordinator loss. Critical lifecycle commands have a reduced local fallback where practical.

### 2.5 Open interfaces and graceful degradation

Optional commercial or platform-specific integrations must remain behind adapters. Losing an optional component may reduce capability but must not corrupt the rest of the system.

### 2.6 Evidence before show readiness

Normal-operation demos are insufficient. Architecture-critical behavior must be tested in the integrated path, then under restart, packet loss, missing media, clock discontinuity, and partial failure.

### 2.7 Observability is part of operation

The platform evaluates the whole presentation path using timestamped, contextual evidence. Reachability alone is not health, stale evidence becomes unknown, and expected show or maintenance state changes the meaning of an observation. See the [observability and alerting specification](OBSERVABILITY.md) and [ADR-011](../decisions/ADR-011-context-aware-observability.md).

## 3. System architecture

```text
xLights authoring
      |
      | show assets / FPP Connect-compatible delivery
      v
FPP scheduler and playlist engine
      |
      | native FPP commands + timing/MultiSync
      v
Coordinator control plane <----> Operators / integrations
      |       |       |
      |       |       +----> Resolume adapter
      |       +------------> FPP adapter/plugin
      +--------------------> Capability-based node agents
                                  |
                    +-------------+-------------+
                    |             |             |
                 renderer      audio engine   transports
                    |             |           NDI / HDMI
                    +-------------+-------------+
                                  |
                              Resolume and/or
                              direct displays
```

### 3.1 Authority boundaries

| Concern | Authority |
|---|---|
| Sequence creation and model layout | xLights |
| Calendar, playlist order, and scheduled start | FPP |
| Lifecycle orchestration and reconciliation | Coordinator |
| Audience-facing audio: sessions, routing, mixing, output assignment | Coordinator ([ADR-017](../decisions/ADR-017-showmesh-owns-audience-audio.md)) |
| Audio playback clock, PCM rendering, local audio device state | Active audio node |
| Local media execution and device health | Node agent |
| Projection composition and mapping | Resolume, when present |
| Emergency local fallback | FPP plugin and node policy |

The coordinator may recommend or request actions but must not silently become a second scheduler.

## 4. Components

### 4.1 Coordinator

The coordinator is the management plane. It maintains inventory, capability assignments, desired state, observed state, show macros, configuration revisions, diagnostics, event history, and operator-facing status.

Its internal design should be a modular monolith initially (Go, per [ADR-006](../decisions/ADR-006-go-implementation-language.md)), with adapters for FPP, Resolume, node agents, and future devices, communicating with agents over MQTT per [ADR-008](../decisions/ADR-008-mqtt-control-plane.md). Real-time playback must not depend on a coordinator round trip.

### 4.2 FPP integration

For normal operation, the FPP integration observes FPP playlist progression and
the coordinator resolves that observation to ShowMesh Cues.  During a confirmed
coordinator outage, ADR-048 permits the plugin to execute only a coordinator-
signed, pre-resolved fallback program at safe entry boundaries.  This preserves
FPP's schedule and progression authority and does not make the plugin a general
command authority.

The FPP integration exposes scheduler- and playlist-safe native FPP commands. It forwards commands to the coordinator when available and implements explicitly defined reduced fallbacks for critical actions.

The integration also imports relevant playlist, playback, and timing state through supported FPP interfaces. Compatibility remains subject to [FPP MultiSync research](../research/RES-002-fpp-multisync-compatibility.md) and [xLights/FPP Connect research](../research/RES-003-xlights-fpp-connect-compatibility.md).

### 4.3 Node agent

The node agent runs natively on media hardware. It advertises capabilities and constraints, reports health, supervises local processes, manages a verified media cache, executes allowlisted commands, and preserves the most recent valid fallback configuration.

It should not require containers on machines that need direct GPU, HDMI, audio, EDID, or NDI access.

### 4.4 Renderer

The renderer supports one or more independent logical surfaces, per [ADR-026](../decisions/ADR-026-renderer-surface-model-and-reference-transport.md). FPP Connect uploads the sequence and FSEQ to the renderer node ahead of playback; each surface extracts its assigned FSEQ virtual-matrix channels locally, renders them to its own canvas, and owns an independent output transport or stream. This is not a live matrix stream, and nothing may introduce one: a node holding its own FSEQ renders through a network partition, which is what keeps the renderer out of the show's critical network path. Rendering is executed as agent-supervised GStreamer pipelines per [ADR-007](../decisions/ADR-007-gstreamer-media-engine.md), and rendering and transport remain separate interfaces.

The renderer does not model physical projectors. A logical surface may feed one projector or a combined surface may feed a projector pair downstream; that mapping belongs to deployment configuration, Resolume, and the physical video path. The architecture supports `N` surfaces per node, while v1 implements one (`N=1`); that limit is implementation scope and must not be embedded in schema, wire types, or capability attributes.

**The day-0 reference profile is a target to validate, not a supported profile.** It is one logical surface at 40 fps over NDI on each Dell OptiPlex Micro 7040-class x86 renderer, and **none of it has been measured.** [RES-004](../research/RES-004-virtual-matrix-renderer-performance.md) exists to establish supported renderer profiles by hardware class, resolution, frame rate, and transport, and it is at L0. That record also treats canvas dimensions and pixel count as test parameters rather than fixing a resolution, so 40 fps is not yet a complete claim; it is 40 fps at an unspecified pixel count. ADR-026 decision 5 requires that qualifier to travel with the profile until the bench runs. Multiple surfaces per node and Raspberry Pi 4 / ARM HDMI profiles are deferred, not excluded.

### 4.5 Audio engine

Audio is a first-class node capability rather than a mandatory responsibility of the primary FPP controller. An audio-capable node may provide show playback, background music, announcements, fades, multichannel routing, Dante integration, metering, and LTC generation.

ShowMesh is authoritative for audience-facing audio, and audio nodes play complete local media files against their own audio clock rather than following a sample stream ([ADR-017](../decisions/ADR-017-showmesh-owns-audience-audio.md)). Program audio and LTC share one clock domain ([ADR-018](../decisions/ADR-018-program-and-ltc-share-a-clock-domain.md)), and audio device loss fails silent with no automatic fallback to FPP audio ([ADR-019](../decisions/ADR-019-audio-device-loss-fails-silent.md)). Rendering — mixing, fades, ducking, interleave, LTC generation — is agent-supervised GStreamer per [ADR-007](../decisions/ADR-007-gstreamer-media-engine.md); ShowMesh code owns session state, sync policy, supervision, and health.

The full contract is [AUDIO-ENGINE.md](AUDIO-ENGINE.md). All of it is design intent: viability, the drift budget, and achieved program-to-LTC alignment are open bench items in [audio-node research](../research/RES-007-audio-node-architecture.md), which remains critical-risk at L0.

### 4.6 Resolume adapter

The adapter controls and observes composition state without entering the frame path. Management operations should use confirmable interfaces where available; operational triggers may use lower-latency interfaces. SMPTE acquisition, loss, jumps, and reacquisition require direct testing described in [Resolume SMPTE research](../research/RES-001-resolume-smpte-behavior.md).

### 4.7 Transport adapters

Media transport is pluggable ([ADR-005](../decisions/ADR-005-pluggable-media-transport.md)). NDI is the v1 and reference renderer transport and HDMI with capture remains a supported alternate and fallback, per [ADR-026](../decisions/ADR-026-renderer-surface-model-and-reference-transport.md) decision 4, which **narrows ADR-005 by naming a reference without superseding its rule that nothing may assume one transport.** Transport is selected per node and surface from advertised capabilities rather than as one global system choice. A node may advertise NDI send, HDMI output, or both, and support for one is never evidence for the other: each capability is reported independently with its own evidence. Because bench effort concentrates on the reference path, the HDMI fallback is deliberately less proven than the primary, which [failure-mode testing](../research/RES-009-failure-mode-testing.md) must carry rather than assume parity. See [transport research](../research/RES-005-ndi-vs-hdmi-transport.md).

### 4.8 Observability subsystem

Collectors normalize state and telemetry from FPP, Resolume, nodes, controllers, projectors, transports, network equipment, power systems, and environmental sensors. The coordinator correlates those observations with topology, lifecycle state, sequence position, diagnostics, commands, and maintenance windows.

The operator surface includes a central overview, physical/logical house map, resource drill-down, projection preview wall, readiness results, active alerts, and historical events. Alert evaluation is context-aware and preserves the evidence behind suppressed, acknowledged, and resolved conditions. The detailed contract is defined in [OBSERVABILITY.md](OBSERVABILITY.md).

### 4.9 Operator UI

The Operator UI is the browser-based operator surface, deployed as its own container and consuming the coordinator's versioned public control API and real-time state stream. It is an optional client: it holds no orchestration behavior of its own, and a running show must continue correctly with the UI crashed, upgraded, unreachable, or entirely absent. See [ADR-014](../decisions/ADR-014-operator-ui-is-an-api-client.md), [ADR-015](../decisions/ADR-015-typescript-spa-frontend.md), and the client contract in [OPERATOR-UI.md](OPERATOR-UI.md).

Because the UI is one client among possible others, the control API is a public contract designed to be usable without it — by a CLI, an automation system, or an alternate client.

### 4.10 Controlled devices and control providers

Projectors, displays, amplifiers, AV receivers, smart relays, power controllers, and network-controlled audio devices run no ShowMesh code and cannot advertise capabilities the way nodes do. They are modeled as **controlled devices**: a distinct coordinator resource class with a persistent definition, desired state, and observed state, driven by **control providers** that declare their own configuration, actions, and telemetry as machine-readable metadata.

Network-reachable providers may run in the coordinator; providers requiring physical attachment run in a node agent and are advertised as a node capability. Providers never enter the timing or media path, but device control is show-affecting: because a controlled device holds no fallback of its own, any macro step depending on a coordinator-hosted provider must be labelled coordinator-required per [ADR-004](../decisions/ADR-004-layered-commands-and-fallback.md). See [ADR-016](../decisions/ADR-016-controlled-devices-and-control-providers.md); the metadata contract and the viability of metadata-generated operator surfaces are open in [RES-014](../research/RES-014-control-provider-model.md), and the normalized telemetry side is [RES-012](../research/RES-012-device-telemetry-adapters.md).

## 5. Synchronization model

### 5.1 Timing authority

FPP owns scheduled start and playlist position. Nodes must derive presentation position from an authoritative show timeline rather than free-running indefinitely from receipt time.

Potential timing inputs include FPP MultiSync data, media timestamps, and LTC/SMPTE for Resolume. The coordinator observes timing health but does not generate per-frame timing decisions.

### 5.2 Offset and latency

Every media path has latency. The system should model stable latency as a measurable offset and treat variance as jitter. Per-path offsets may be configured only after measurement and must include provenance, environment, and last verification date.

### 5.3 Discontinuities

Nodes must define behavior for start, pause, seek, restart, late join, packet loss, clock jumps, and loss of timing authority. No component may silently continue on an unbounded local clock while reporting itself synchronized.

### 5.4 Readiness

Before a live set, the platform should verify media presence, renderer readiness, output availability, audio route, timecode lock, transport health, and acceptable clock offset. Exact thresholds are research outputs rather than assumptions.

### 5.5 Clock domains

The show timeline clock, a node's audio playback and device clock, and any network audio clock are distinct and must never be assumed identical. Signals whose phase relationship matters must originate in one domain: program audio and LTC always qualify, and [ADR-018](../decisions/ADR-018-program-and-ltc-share-a-clock-domain.md) makes sharing a domain a requirement rather than a preference.

Timing coupling is not one policy across the system. The lighting timeline follows FPP remote semantics, correcting continuously by slew and jump; program audio aligns at start and is corrected discretely, because slews and seeks are audible ([ADR-017](../decisions/ADR-017-showmesh-owns-audience-audio.md)); an audio interface whose node holds a locked shared PTP clock may additionally be rate-trimmed at ppm scale to that clock, never to the position feed ([ADR-046](../decisions/ADR-046-rate-lock-to-a-shared-clock-is-not-chasing.md)). Both satisfy §5.1 — neither runs unbounded and unmeasured — and the difference between them is deliberate.

## 6. Node capabilities

Capabilities use namespaced identifiers and include metadata rather than bare booleans.

```yaml
node_id: media-03
platform: linux-amd64
capabilities:
  - id: matrix.render
    version: 1
    attributes:
      max_width: 1920
      max_height: 1080
      tested_fps: 40
  - id: transport.ndi.send
    version: 1
  - id: display.hdmi
    version: 1
    attributes:
      outputs: 2
```

Initial vocabulary:

- `matrix.render`
- `video.playback`
- `media.cache`
- `display.hdmi`
- `transport.ndi.send`
- `transport.ndi.receive`
- `audio.engine`
- `audio.output.local`
- `audio.output.fm`
- `audio.output.ltc`
- `audio.output.dante`
- `timecode.ltc.observe`
- `process.supervise`

Audio is split by output rather than by aggregate ability, because output is what actually drives placement: a node either has the interface, the routing, and the media or it does not. This replaces the earlier `audio.playback`, `audio.multichannel`, `audio.dante`, and `timecode.ltc.generate` identifiers, which are withdrawn — nothing advertises them yet. Channel count and device detail belong in attributes. See [AUDIO-ENGINE §13](AUDIO-ENGINE.md#13-capabilities).

Capability placement must respect clock relationships as a hard constraint: [ADR-018](../decisions/ADR-018-program-and-ltc-share-a-clock-domain.md) requires `audio.output.ltc` and the program output to resolve to the same interface, which can pin both to one node.

Assignments include required capabilities, preferred node, fallback candidates, and resource constraints. A node may provide any compatible combination.

## 7. State model

### 7.1 Operational states

The initial lifecycle is:

```text
offline -> resting -> pre-show -> live -> intermission -> live
                                      \-> post-show -> resting
any state -> maintenance
any state -> blackout
```

`blackout` is an emergency presentation state, not proof that equipment is powered down. `maintenance` suppresses expected alerts but does not disable safety checks.

The repeated operating-night lifecycle, its distinction from Program/Show Mode, and the content-derived transition clock are specified in [RESTING-MODE](RESTING-MODE.md). Per [ADR-038](../decisions/ADR-038-fpp-authorizes-night-sessions.md), FPP owns the calendar and authorizes the session while ShowMesh advances individual rest/show cycles inside it.

### 7.2 Resource state

Each managed resource records:

- desired state and revision;
- observed state and timestamp;
- reconciliation status;
- last successful transition;
- evidence supporting the observation;
- active fault or degraded condition.

Useful reconciliation states include `converged`, `progressing`, `degraded`, `unknown`, and `conflicted`.

### 7.3 State transitions

Transitions are idempotent, persisted, timed, and observable. Each transition declares preconditions, steps, confirmation, timeout, retry policy, compensating action, and locally safe behavior if coordination is lost.

## 8. Command model

The platform exposes two layers. See [ADR-004](../decisions/ADR-004-layered-commands-and-fallback.md).

### 8.1 Primitive commands

Primitive commands perform one bounded operation, such as setting a composition, starting a renderer, adjusting a measured offset, fading an audio bus, reloading media, or running diagnostics.

Every command carries an identifier, target, parameters, idempotency key, deadline, issuer, requested revision, confirmation method, and result.

### 8.2 Show macros

Show macros are named operational transitions composed from primitives. Examples include `Enter Resting Mode`, `Enter Pre-Show Mode`, `Begin Set`, `Enter Intermission`, `Enter Post-Show Mode`, `Blackout`, and `Run Readiness Check`.

FPP schedules the macro through a native command. The coordinator expands and supervises it. A macro definition must label which reduced steps may be executed locally when the coordinator is unavailable.

Two classes of step cannot carry a local fallback at all, and a macro definition must say so rather than leaving the omission to be discovered during an outage:

- A step driving a controlled device through a coordinator-hosted provider is **coordinator-required** ([ADR-016](../decisions/ADR-016-controlled-devices-and-control-providers.md)). If the step is genuinely show-critical, the provider belongs on a node instead of in the coordinator.
- A step depending on an audio output device declares **silence** as its reduced local behavior ([ADR-019](../decisions/ADR-019-audio-device-loss-fails-silent.md)). There is no handover to FPP's own audio path, and a macro must not imply one.

### 8.3 Example

`Begin Set` may:

1. Confirm readiness and media availability.
2. Fade and stop background audio.
3. Arm show audio and LTC.
4. Select the Resolume show composition.
5. Enable presentation layers.
6. Confirm timing lock and desired state.

Steps 2 and 3 are audio steps, so this macro's definition must state that their reduced local behavior is silence. `Blackout` and `Enter Pre-Show Mode` will each touch controlled devices, so their device steps must be labelled coordinator-required unless the provider is hosted on a node.

Partial failure must produce an explicit degraded state and execute a defined compensating or safe action.

## 9. Configuration model

The active configuration requires revisions, schema validation, transactional changes, secret separation, export, import, dry runs, and rollback. The coordinator holds the authoritative configuration while agents retain only the signed or verified subset required for current assignments and fallback.

Controlled-device definitions (§4.10) are configuration and follow the same revision, validation, export, and rollback rules as everything else; per-device credentials are secrets and stay out of exported bundles (§10.4).

Portable YAML bundles support backup, review, and migration; the runtime source of truth is embedded SQLite per [ADR-009](../decisions/ADR-009-sqlite-configuration-storage.md). Schema shape, merge semantics, and stale-node reconciliation remain tracked in [configuration research](../research/RES-008-configuration-model.md).

## 10. Deployment

### 10.1 Coordinator appliance

The preferred early deployment is a documented container bundle on Linux with persistent storage, a message broker where required, health checks, backups, and upgrade/rollback support. The default should run on `linux/amd64` and `linux/arm64`.

The bundle contains the coordinator, a broker, and the Operator UI as separate containers per [ADR-012](../decisions/ADR-012-docker-coordinator-deployment.md) and [ADR-014](../decisions/ADR-014-operator-ui-is-an-api-client.md). The UI is independently upgradeable, and the stack must remain fully functional with the UI container absent.

### 10.2 Media nodes

Agents run natively under the platform service manager. Packaging should begin with the platforms required by the reference show, then expand without changing the protocol or capability model.

Linux is the reference and supported platform for the audio node ([AUDIO-ENGINE §12](AUDIO-ENGINE.md#12-platform)). Windows is a platform exception for specific hardware or software dependencies, not a baseline, and no output technology may dictate the audio node's operating system: where a Dante implementation requires Windows, the Dante output capability may move to a separate node rather than the Audio Engine moving with it — subject to the clock-domain and transport costs recorded in AUDIO-ENGINE §12.

### 10.3 Network

The system should function on an isolated show network or VLAN. Control and media traffic must be distinguishable, observable, and documented. Deployments with NDI should budget aggregate bandwidth and validate switch behavior rather than relying only on nominal link speed.

### 10.4 Security

Commands require authenticated identities and authorization by target and action. Agents accept only allowlisted operations. Secrets are not included in portable configuration exports by default. Offline show operation must not require an internet service.

## 11. Failure handling

Safe behavior is defined per capability and operational state. At minimum, the system must address coordinator loss, network partition, node reboot, missing or corrupt media, frozen decoder, transport loss, SMPTE discontinuity, audio-device loss, Resolume restart, conflicting commands, stale configuration, disk exhaustion, and power restoration.

Detection, degraded operation, autonomous recovery, operator notification, and proof artifacts are defined in [failure-mode research](../research/RES-009-failure-mode-testing.md).

## 12. Roadmap

### Phase 0 — Read-only observability baseline (complete)

- Inventory, topology, and timestamped health collection.
- FPP, Resolume, controller, projector, and media-node status.
- Versioned read-only control API, published as a public contract rather than a UI-private interface.
- Central dashboard, active faults, and event history, delivered by a read-only Operator UI over that API.
- Six low-resolution projection previews for the reference installation.
- Dashboard and Discord notification path.

### Phase 1 — Show-critical coordination

- Native FPP lifecycle commands.
- Resting/background audio with deterministic fades into and out of shows, on a ShowMesh audio node rather than FPP's own output.
- FPP-authorized night-session control whose inter-show duration comes from the exact deployed resting FSEQ, with graceful final-show and end-of-night behavior.
- Documented manual audio recovery, which ADR-019 makes mandatory rather than optional.
- Stable projection through Resolume using at least one proven transport.
- Readiness checks, blackout, and reduced coordinator-loss fallback.
- Documented manual recovery procedures.

### Phase 2 — Coordinated reference installation

- Coordinator inventory and desired/observed state.
- Node agent, capability advertisement, media cache, and renderer supervision.
- Dedicated audio capability and LTC path on a shared clock domain.
- Initial Resolume adapter.
- Controlled-device definitions and the first control provider, for projectors.
- Pixel-current baselines and known-load readiness diagnostics.
- Lifecycle-aware alerts and post-show evidence.
- Integrated failure and restart tests.

### Phase 3 — Portable ecosystem workflow

- Compatible xLights export and FPP Connect workflow.
- Reassignable capability workloads.
- Configuration import/export, revision history, and rollback.
- NDI and HDMI transport profiles.
- Reproducible installers and upgrades.

### Phase 4 — Community hardware

- Raspberry Pi and other ARM capability profiles.
- Low-power renderer benchmarks and reduced feature profiles.
- Image-based or similarly simple node installation.
- Broader audio, display, and transport adapters.

### Deferred until evidence supports them

- Automatic failover during a live set.
- A requirement for NDI in the base installation.
- A single universal renderer profile for all hardware.
- Replacing FPP, xLights, or Resolume responsibilities.

## 13. Open research

The authoritative list is the [research tracker](../research/README.md). Research conclusions that change a durable constraint must result in a new or superseding architecture decision record.
