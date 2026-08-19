# Audio Engine Specification

[Documentation index](../README.md) · [Architecture specification](ARCHITECTURE.md) · [Observability specification](OBSERVABILITY.md) · [ADR-017](../decisions/ADR-017-showmesh-owns-audience-audio.md) · [ADR-018](../decisions/ADR-018-program-and-ltc-share-a-clock-domain.md) · [ADR-019](../decisions/ADR-019-audio-device-loss-fails-silent.md)

Status: Draft architecture baseline — design intent, not implemented and not verified  
Audience: Maintainers, audio-node contributors, show operators

Nothing in this document has been prototyped. The central viability questions — whether GStreamer delivers click-free ducking and sample-aligned LTC from one clock, and what drift a free-running audio node actually accumulates over a show — are open bench items in [RES-007](../research/RES-007-audio-node-architecture.md), which is critical-risk and currently at L0.

## 1. Purpose and scope

The Audio Engine is the playback and routing system for all audience-facing audio: show music, background and intermission music, announcements, and the LTC that drives Resolume.

It owns how audio is rendered and distributed. It does not own what the show is doing — FPP remains the scheduler and sequence authority ([ADR-001](../decisions/ADR-001-fpp-is-authoritative.md)). The Audio Engine acts on show state rather than producing it.

This document owns the audio subsystem's architecture. [RESTING-MODE](RESTING-MODE.md) owns when background and announcement sessions are created and faded around the operating-night lifecycle. Health presentation and alerting for audio resources belong to [OBSERVABILITY](OBSERVABILITY.md); how any of it is displayed belongs to [OPERATOR-UI](OPERATOR-UI.md).

## 2. Authority model

Authority is deliberately split three ways, and the split is the point of the design.

**FPP is authoritative for** scheduler state, playlist and show selection, sequence execution, and the sequence timeline.

**ShowMesh is authoritative for** audience-facing audio sessions, routing, output assignment, background audio, announcements, mixing and ducking, audio-node placement, and audio health. This is a change from FPP's default arrangement, where the primary FPP controller plays program audio itself; see [ADR-017](../decisions/ADR-017-showmesh-owns-audience-audio.md).

**The active audio node is authoritative for** its local media playback clock, PCM rendering, and local device state.

Show synchronization and audio clock synchronization are therefore separate problems, solved separately. That separation is what makes it possible for the coordinator to stay out of the timing path ([ADR-008](../decisions/ADR-008-mqtt-control-plane.md)) while audio still starts in the right place.

## 3. Playback model

Audio is represented as logical **playback sessions**. A session carries media identity, source type, start time, current position, playback state, priority, gain, loop behavior, and output targets.

Initial source types are `show`, `background`, `announcement`, and `manual`. Higher-priority sessions may duck, pause, replace, or mix with lower-priority ones according to policy. Indicative default priorities are background 10, show 50, announcement 100; these are starting values for tuning, not normative constants.

A session source is either one exact asset or an ordered playlist revision whose items each resolve to an exact asset id and content hash. Playlist state carries playlist revision, current item identity and index, item position, and repeat mode (`none`, `item`, or `playlist`). Natural item completion advances exactly once to the next item; the last item completes the session unless playlist repeat is enabled. Resume bookmarks pin the playlist revision, item identity, index, and position. If the revision changed, the pinned item no longer exists, or the next item is missing, corrupt, or undecodable, resume or advancement fails visibly instead of guessing by filename or index.

The Audio Engine consumes playlists; it does not own a playlist authoring store. A caller supplies an immutable owner kind, owner id, owner revision, ordered logical Track E audio slots, repeat policy, resume/restart policy, and requested item transition. An audio slot identifies the intended current show/sequence/target asset without embedding a mutable filename or content hash in authoring configuration. Resting Mode embeds that list in its versioned Night Session configuration, whose normal API, CLI, export/import, and revision path is the reachable authoring surface. When a session starts, it pins that Night Session revision and resolves every slot to its exact current asset id and content hash; later configuration edits or asset supersession do not alter the active playlist.

The default transition between background items is no overlap and no promised gapless seam. An output may advertise gapless or crossfade support and a playlist may request it, but configuration is refused when that behavior is required and the selected output cannot confirm it. Track C measures the actual inter-item gap; it does not call ordinary sequential playback gapless.

The semantic session states are `preparing`, `ready`, `playing`, `paused`, `stopping`, `stopped`, `completed`, `failed`, and `unknown`. `unknown` means observation is absent or stale; it is never treated as stopped, completed, or ready. A media change creates or revises a session before playback is dispatched, so an output never has to discover and provision media inside `start`.

The engine must represent these transitions even when a particular output cannot reproduce all of them:

- select or replace media, including the same runtime filename with a new content hash;
- select a pinned ordered playlist and advance its current item exactly once;
- prepare and validate the resolved asset;
- start at an authoritative position;
- pause and resume where the source authority exposes that distinction;
- seek or restart after an authoritative discontinuity;
- stop, fade to stop, or observe natural completion;
- loop, resume from a prior position, or restart according to the session policy;
- fail or become unknown without manufacturing a successful completion.

Every state-changing request carries a stable invocation identity, target session, desired revision, deadline, and confirmation contract through the command envelope in ARCHITECTURE §8.1. Repeating the same invocation is idempotent. A later revision supersedes an earlier desired state; delayed commands must not rewind a newer session. Confirmation states what was observed (`started`, `position`, `gain`, `fade-complete`, `stopped`, or `completed`) or says the operation is structurally unconfirmable. Receipt alone is not confirmation.

## 4. Timing and synchronization

### 4.1 Nodes play local media, not a sample stream

Each eligible audio node holds local copies of show media and plays them locally. ShowMesh supplies authoritative playback state — media identity, start and stop, desired position, show start time, and current authoritative show position — and the node starts or seeks its matching local file and then plays it on its own stable audio clock.

The sequence is:

1. FPP starts the sequence or show.
2. ShowMesh receives current media and timeline state.
3. ShowMesh creates or updates the authoritative audio session.
4. The audio node starts the matching local asset at the requested position.
5. The node continues playback against its own audio clock.
6. ShowMesh compares node position against show position as telemetry.

Continuous PCM streaming would put the network and the coordinator inside the real-time audio path, which the standing constraints forbid, and would import MultiSync-rate correction artifacts into program audio. Real-time streaming may exist later as a separate capability; it is not the synchronized show-audio architecture. See [ADR-017](../decisions/ADR-017-showmesh-owns-audience-audio.md).

### 4.2 Drift policy

The Audio Engine must not continuously discipline its audio clock against show timing. Policy:

- align accurately at playback start;
- measure drift continuously and report it;
- ignore drift below a configurable threshold;
- prefer correction at track boundaries;
- allow a discrete seek when drift becomes operationally significant;
- avoid audible playback-rate manipulation;
- never chase small timing differences continuously.

This is a deliberate divergence from the FPP remote sync semantics that `pkg/multisync` implements for the lighting timeline. Those semantics slew by up to four frames and jump beyond half a second, which is correct for pixels and wrong for program audio, where rate manipulation is audible and a seek is a defect the audience hears. The two models are not required to match, and the divergence must not be "fixed" by making audio behave like a MultiSync remote.

ARCHITECTURE §5.1 requires nodes to derive presentation position from an authoritative show timeline rather than free-running indefinitely from receipt time. An audio node satisfies that by aligning to the show timeline at start and at every correction point, and by remaining continuously measured against it. It is not free-running: it is loosely coupled, with the coupling interval set by policy rather than by the frame rate.

The tolerable drift threshold is unknown. It depends on what the audience can perceive between audio and lighting, which is a bench and field question in [RES-007](../research/RES-007-audio-node-architecture.md), not something to assert here.

Pause, seek, restart, a changed media identity, and loss or reacquisition of the authoritative FPP timeline are discontinuities rather than ordinary drift. The engine freezes or fails the affected desired transition according to policy, reports timing `unknown`, and realigns explicitly when authority returns. It must not extrapolate indefinitely and call the result synchronized. Natural media completion and an authoritative stop are distinct observations because Resting Mode uses completion as a transition barrier.

## 5. Clock domains

Three clocks are explicitly distinguished and must not be assumed identical:

1. the show timeline clock;
2. the audio playback and device clock;
3. the network audio clock, where one exists.

Program audio and LTC must share a clock domain wherever their timing relationship matters, which for a show driving Resolume from LTC is always ([ADR-018](../decisions/ADR-018-program-and-ltc-share-a-clock-domain.md)). The preferred deployment puts program and LTC on one hardware interface and therefore one hardware clock.

The arrangement to avoid is program audio on a USB interface and LTC on a Dante interface, or any other pairing that places two signals whose phase relationship matters on independent hardware clocks. If both eventually run over Dante they belong in the same Dante clock domain.

## 6. Buses and routing

Logical buses are defined independently of physical outputs, and channel counts are never hard-coded.

**Program bus** — the stereo audience-facing mix, containing show music, background audio, announcements, and any other audience-facing source.

**LTC bus** — mono timecode. Never mixed into program audio, on its own discrete physical output.

The minimum physical output requirement for an initial deployment is therefore channels 1–2 for stereo program and channel 3 for LTC, with a fourth channel reserved where practical for future routing or monitoring.

Future optional buses the architecture must permit without redesign: independent local speaker zones, an independent FM mix, monitor and cue outputs, alternate language or program feeds, additional timecode, and Dante destinations.

One bus may feed several outputs simultaneously rather than requiring duplicate mixes. An FM transmitter needs no separate logical mix unless it later needs different processing or content, and announcements need no dedicated channels — they are sources mixed into program.

## 7. Rendering

Mixing, gain, fades, ducking, gapless playback, multichannel interleave, and LTC generation are performed by agent-supervised GStreamer pipelines, per [ADR-007](../decisions/ADR-007-gstreamer-media-engine.md). ShowMesh code owns session state, sync policy, routing decisions, supervision, and health — never sample generation. This is the same boundary the renderer observes, applied to audio.

Whether GStreamer delivers click-free ducking, reliable gapless playback, and LTC sample-aligned to program on a shared clock is unverified and is the first bench item in [RES-007](../research/RES-007-audio-node-architecture.md). A negative result is an architectural finding requiring a superseding ADR, not something to work around by moving sample generation into Go.

## 8. Output adapters

Outputs are adapters over logical session state. The semantic contract is: initialize; resolve capabilities; prepare media where applicable; apply media selection and revision; start, pause, resume, seek, stop, loop, gain, fade, duck, mute and unmute where supported; and report health, observation freshness, current position, latency, and operation outcomes. The eventual wire schema may group or rename these operations, but it must preserve these meanings, invocation identity, ordering, deadlines, and confirmation. **An adapter must declare what it does not support rather than pretending to support it.**

Three adapter classes behave differently and must not be flattened into one:

**PCM outputs** receive rendered audio — USB interfaces, ALSA and PipeWire devices, analog speakers, FM transmitter feeds, HDMI, Dante interfaces.

**Synchronized remote outputs** independently reproduce media from media assets plus playback state, receiving logical playback information rather than PCM. Mobile or web listener applications are of this class.

**Stream outputs** carry real-time encoded or PCM audio — RTP, WebRTC, Icecast, NDI audio, AES67-style transports. These are secondary and must never be required for basic show playback.

Adapter capability metadata is honest about asymmetry rather than lowest-common-denominator. A synchronized remote output that cannot reproduce the local mix says so, and ShowMesh surfaces the limitation instead of hiding it:

```yaml
name: example-remote-listener
capabilities:
  synchronized_media: true
  pcm_stream: false
  accepted_formats: unknown
  advance_provisioning: true
  provisioning_acknowledgement: unknown
  readiness_observation: unsupported
  mixing: unknown
  announcements: unknown
  ducking: unsupported
  gain_fades: unknown
  looping: true
  playlists: true
  sequential: true
  gapless: unknown
  crossfade: unsupported
  seeking: unknown
  position_reporting: unknown
```

`unknown` is a legitimate value. An adapter whose behavior has not been established says so, and ShowMesh must not resolve that into an assumption on the adapter's behalf.

### 8.1 Synchronized remote-output boundary

A synchronized remote output has two independent responsibilities:

1. **Advance media provisioning.** It makes a ShowMesh asset available to the destination before playback, keyed by the ShowMesh asset id and content hash, and records whatever evidence the destination actually supplies.
2. **Logical playout.** It receives single-media or pinned-playlist selection/change, playlist revision, exact current item identity/index/content hash, item advancement and requested transition, start, stop, position, loop, gain intent, and source-role state for the sessions it can reproduce. It does not receive the locally rendered PCM mix.

Provisioning never begins because `start` arrived. It runs on asset ingestion, destination assignment, configuration change, and retry, with enough lead time for opaque server-side processing. A third-party copy is a delivery representation of the authoritative ShowMesh asset, not a second source asset. Its record is keyed by destination instance, immutable destination-configuration revision or fingerprint, and ShowMesh content hash, and may include a remote media identifier when one is exposed.

The generic evidence vocabulary is deliberately weaker than node-local readiness:

- `not_attempted` — no provisioning action has been made for this destination and content hash;
- `attempted` — ShowMesh dispatched or completed its side of the transfer, but received no durable acknowledgement;
- `acknowledged` — the destination acknowledged receipt or registration, without implying transcoding or playback readiness;
- `manually_verified` — an operator recorded that the expected content was audibly reproduced on a named listener destination;
- `failed` — the transfer or destination reported a failure;
- `unknown` — evidence is absent, stale, or cannot be related unambiguously to the current content hash and destination configuration.

An adapter may add a destination-reported `ready` or processing state only when the integration actually exposes it. Absence of such an API is supported behavior, not an adapter defect. Manual verification records the destination, immutable destination-configuration revision or fingerprint, content hash, operator identity, timestamp, and optional note. A record with no comparable configuration revision is not eligible to satisfy required-output policy after restart. Verification expires when the asset hash or relevant destination configuration changes. It proves that one listener reproduced that asset at that time, not that future playback, synchronization, mixing, or every listener works.

The initial format baseline for a candidate third-party adapter is the set of formats FPP recognizes as audio. This is an **L0 owner assumption and a test corpus**, not evidence that any destination accepts those formats and not a constraint on the generic asset store. An adapter declares accepted formats as supported, unsupported, or unknown from its own evidence.

### 8.2 Output adapters are not control providers

An output adapter carries audio and sits in the media path. A control provider ([ADR-016](../decisions/ADR-016-controlled-devices-and-control-providers.md)) is management-plane only and explicitly never in the timing path. They share a discipline — declare capabilities honestly, never fake support — and nothing else, and must not be unified.

The distinction is visible in the reference installation: the FM transmitter and the BSS BLU-DAN Dante master are *controlled devices* addressed by providers, while the interface output feeding the transmitter and the Dante transmit path are *output adapters*. The same physical signal chain crosses both models, on purpose.

## 9. Mixing and priority

Minimum required behavior: show audio can replace or duck background audio; announcements can duck show or background; gain transitions use configurable fades; background playback can optionally resume from its previous position; and output adapters may implement reduced functionality where they cannot reproduce the full local mix.

```text
background playing
       |  show begins
       v
fade/duck background, play show audio
       |  announcement
       v
duck program, mix announcement
       |
       v
restore program
```

## 10. Media management

Each eligible audio node maintains local copies of required media. ShowMesh tracks media ID, path, hash, size, duration, version, and per-node availability.

Before assigning audio authority to a node, ShowMesh verifies that the required assets are present and match. Media synchronization is a separate concern from playback synchronization and must not be conflated with it: a node with stale media is not a node that has drifted, and the two failures need different responses.

Track E remains authoritative for generic asset identity, bytes, hashing, and distribution. Track C probes audio media before it is admitted to a playback session and reports at least duration and the detectable container, codec, channel count, and sample rate. `mediaType: audio` is a caller-supplied category, not proof that a file can be decoded. Missing or corrupt media, an unreadable duration where duration is required, or a hash mismatch blocks authority for that asset and names the fault.

Node-local and third-party evidence remain separate:

- a local audio node is ready only when every exact asset in each required single source or pinned playlist revision exists locally and probes successfully, the required route and clock relationship are healthy, and the session operations the show needs are supported;
- an optional synchronized remote output may remain `unknown` or merely `acknowledged` without blocking the local/FM path, while the operator receives a warning;
- an installation may mark a remote output required. Its evidence must cover every exact audio and announcement content hash required by the pinned session revision, including every playlist item. When no machine-readable readiness exists, that policy may accept a set of current `manually_verified` attestations, each pinned to the immutable destination-configuration revision or fingerprint and one required content hash. One verified item cannot satisfy a multi-item playlist. ShowMesh must never translate an upload attempt into `ready` silently.

## 11. Failure behavior

### 11.1 Output device loss

Loss of an audio device fails silent and reports critical degradation. The engine stops sending to the failed output, keeps session state alive, marks the output critical, notifies the coordinator, and continues unaffected outputs.

ShowMesh must never automatically hand audio back to FPP's own output. The reasoning and the consequences for [ADR-004](../decisions/ADR-004-layered-commands-and-fallback.md) are in [ADR-019](../decisions/ADR-019-audio-device-loss-fails-silent.md). Clean silence on a failed output is preferable to an uncontrolled path into an FM transmitter.

### 11.2 Individual output failure

Other outputs continue, the session remains active, ShowMesh reports degraded audio state, and the failed output can be restarted or recovered independently.

### 11.3 Node failure

If the active audio node fails, ShowMesh detects the loss, identifies eligible standby nodes, verifies required media and output capabilities and physical routing and health, and a replacement node may assume audio authority, starting at the current authoritative show position.

Recovery prioritizes safe and deterministic behavior over seamless sample-accurate continuity. Audio will be interrupted; the goal is that it resumes correctly and audibly once, not that the seam is inaudible.

Scope note: ARCHITECTURE §12 defers automatic failover during a live set until evidence supports it, and reassignable capability workloads are Phase 3. Until then this is an operator-initiated procedure with ShowMesh performing the eligibility verification, not an automatic one. Making it automatic requires the deferred roadmap item, the verification gates above, and RES-009 failure-injection evidence.

### 11.4 Other engine failures and manual recovery

A pipeline crash or freeze, decode failure, media disappearance or corruption, sample-rate or route change, timing-authority loss, and a device reappearing after loss are distinct faults with distinct observations. None may be collapsed into `stopped` or silently restart into an unknown position. A reappearing device remains unavailable until its route, channel separation, clock relationship, and current asset are revalidated; resumption is an explicit operator action during the initial release.

ADR-019 deliberately removes automatic fallback to FPP audio, so the release documentation and Track C bench must include a manual restoration procedure. It identifies the failed component, keeps failed output silent, verifies the intended route and gain, offers restart or eligible-node reassignment, and documents deliberate reversion to FPP audio as an installation change rather than an automatic command. A running local session must continue through coordinator or broker loss for media already present; later transitions that require unavailable authority fail visibly rather than guessing.

#### The procedure

ADR-019 removed automatic fallback, which makes this procedure the replacement rather than a supplement to it. It is written as steps because an operator runs it while a show is degraded.

1. **Identify the failed component.** Read `audio_session.fault.kind` for the affected sessions and `node.audio.device.state`, `node.audio.program.state` and `node.audio.ltc.state` for the node. The fault kind names what broke: `pipeline_crash` or `freeze` is the engine, `decode_failure` or `media_disappeared` is the asset, `route_changed` is the physical output, `timing_authority_lost` is the clock relationship.
2. **Confirm the failed output is silent rather than misrouted.** Verify it; do not assume it. A failed output must never be left sending ungated signal anywhere, and least of all toward an FM transmitter, which is the specific outcome ADR-019 exists to prevent.
3. **Revalidate before touching playback.** In order: the intended route is still the one physically connected; program and LTC are still on separate correctly assigned channels, neither mixed nor swapped; the program-to-LTC clock relationship is back within tolerance where a measurement exists; every pinned asset still resolves by content hash, re-probed rather than trusted from a stale result; and the current playlist item is the one expected.
4. **Then act, choosing one.** Restart the session on the same node, `audio.session.prepare` followed by `audio.session.start`: a successful prepare is what clears a recorded fault, and a start without one neither clears it nor confirms anything. Or reassign to an eligible standby node, verifying its media, output capabilities and physical routing first (§11.3); this is operator-initiated, not automatic.
5. **Reverting to FPP's own audio output is never a command.** Where it is genuinely necessary it is a deliberate installation change made outside ShowMesh's control surface. ShowMesh neither offers it as a recovery action nor performs it on its own.
6. **Confirm recovery from evidence, not from the command.** Check that `audio_session.fault.kind` has returned to `none`. Every session command's confirmation is gated while no pipeline backend exists, so the cleared fault is the durable evidence and the command's own reported outcome is not.

## 12. Platform

Linux is the reference and supported platform for the audio node: FPP ecosystem compatibility, predictable headless operation, the existing agent deployment model, GStreamer, PipeWire and ALSA, straightforward service supervision, and no dependence on desktop-session behavior.

Windows audio nodes may be supported later where a specific hardware or software dependency requires it. Windows is a platform exception, not the baseline.

Dante must not dictate the audio node's operating system. Where a Dante implementation requires Windows, the Dante *output capability* may be placed on a separate Windows node rather than moving the authoritative Audio Engine onto Windows.

That bridge has a cost the spec it came from did not state: carrying program audio from a Linux audio node to a Windows Dante node is real-time audio transport between ShowMesh nodes, which §4.1 excludes from the show-audio path and §16 defers, and it introduces the second independent clock domain that §5 exists to avoid. The Windows Dante bridge is therefore not a free deployment option. It is a deferred configuration requiring its own transport decision and its own clock-domain analysis, and it is not part of the initial scope. A Linux-native Dante path, if one becomes viable, removes the problem rather than solving it.

## 13. Capabilities

Audio capabilities are advertised independently, so that placement follows what a node can actually do:

```text
audio.engine
audio.output.local
audio.output.fm
audio.output.ltc
audio.output.dante
```

A node provides any subset. One node may run the engine with local, FM, and LTC outputs while another provides only Dante. Additional `audio.output.*` identifiers are added when an output adapter exists to advertise them.

ShowMesh determines placement from hardware, required clock relationships, media availability, physical routing, node health, and output dependencies. Clock relationships are a placement input, not an afterthought: ADR-018 means `audio.output.ltc` and the program output must land on the same interface, which can force both onto one node regardless of what other nodes could otherwise provide.

These identifiers replace the earlier audio entries in the ARCHITECTURE §6 vocabulary.

## 14. Control surface

Required semantic operations are `select_media`, `select_playlist`, `prepare`, `play`, `pause`, `resume`, `advance`, `stop`, `seek`, `set_loop`, `announce`, `set_gain`, `fade_gain`, `duck`, `mute`, and `unmute`. Implementations may combine operations when their confirmation and idempotency remain unambiguous. Every operation names the session and output set, carries the stable invocation identity and desired revision from §3, and reports an observed outcome or `unconfirmable`.

Representative session state:

```json
{
  "session": "show-1234",
  "source": "show",
  "media": "songs/song01.flac",
  "state": "playing",
  "position": 42.183,
  "reference_position": 42.179,
  "drift_ms": 4
}
```

The JSON is illustrative rather than a frozen wire schema. The semantics above are normative. The actual command envelope follows ARCHITECTURE §8.1 and the ADR-008 topic conventions, and any operator-facing exposure follows the public API contract in [ADR-014](../decisions/ADR-014-operator-ui-is-an-api-client.md).

## 15. Telemetry

The engine reports current source and playlist revision, current item identity/index/content hash, item position, repeat mode, playback position, reference show position, drift, state and desired revision, active clock source and domain, audio device and device health, output state, output latency, measured inter-item gap, underruns, media availability and probe result, active sources, mix state, effective output gain, fade/duck state and completion, LTC state, command outcome, observation freshness, and failover eligibility. Synchronized remote outputs additionally report provisioning evidence, acknowledgement or destination status where available, last manual verification with its destination-configuration revision, and the capabilities that prevent exact reproduction of the local mix.

Program-to-LTC alignment is its own readiness signal, not inferred from the two outputs being active. The engine reports the currently measured program-to-LTC offset, measurement method and provenance, measured-at time, freshness, configured threshold, and verdict (`within_tolerance`, `out_of_tolerance`, or `unknown`). Commissioning establishes long-run behavior; the pre-show readiness run obtains a fresh observation at the installed outputs and refuses to call stale commissioning data current.

This feeds the desired-versus-observed model directly. Two rules from [ADR-011](../decisions/ADR-011-context-aware-observability.md) apply with particular force here: stale telemetry is `unknown` rather than healthy, and a session reported as `playing` is a claim about the engine's state, not proof that an audience heard anything. Evidence that audio reached an output is a different signal from evidence that a session exists.

## 16. Initial scope

**Day-0 / mid-September:** a Linux audio node; local-file playback from exact Track E assets; FPP show-state synchronization and discontinuity handling; a stable independent playback clock; the stereo program bus and mono LTC bus; physical multichannel output and FM transmitter routing; background and announcement sessions; loop/resume policy; gain ceilings, ducking and fades with observable completion; playback/readiness telemetry; and safe audio-device-loss handling plus manual recovery. These are required because Day-0 controls a real show and supplies Track D's LTC and Track F's audio primitives.

**After the core session engine, not a Day-0 gate:** the generic synchronized-remote-output contract exercised against a deterministic mock destination, including advance provisioning and absent-readiness behavior. A real third-party adapter, its upload protocol, remote processing status, and phone playback are integration research and do not block the local/FM/LTC show path.

Deferred beyond the initial release: Dante as a required transport, real-time PCM streaming between ShowMesh nodes (including the Windows Dante bridge in §12), sample-transparent node failover, multi-zone audio, and dynamic clock-rate correction.

## 17. Open questions

- **GStreamer viability** for click-free ducking, gapless playback, and LTC sample-aligned to program on one clock. [RES-007](../research/RES-007-audio-node-architecture.md), first bench item.
- **Tolerable drift**, both the perceptual threshold against lighting and what a free-running node actually accumulates over a show. RES-007.
- **Audio interface commissioning.** No specific product gates Track C. The selected interface must expose the channel and clock properties ADR-018 requires and pass the physical separation test; software-reported channel count alone is insufficient.
- **LTC frame rate configuration** and its relationship to the Resolume input configuration ([RES-001](../research/RES-001-resolume-smpte-behavior.md)).
- **Announcement policy** — whether announcements ever interrupt show audio or only duck it, which is a show-design decision with a technical consequence.
- **Third-party provisioning and playout surfaces.** The generic contract is fixed above, but upload protocols, acknowledgements, processing status, format support, timing, and mix reproduction remain integration-specific research in [RES-016](../research/RES-016-third-party-synchronized-audio-output.md).
