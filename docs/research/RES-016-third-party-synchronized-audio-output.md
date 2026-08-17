# RES-016: Third-Party Synchronized Audio Output

[Research tracker](README.md) · [Audio Engine](../architecture/AUDIO-ENGINE.md) · [Resting Mode](../architecture/RESTING-MODE.md) · [Track C](../build/TRACK-C-audio-node.md) · [ADR-017](../decisions/ADR-017-showmesh-owns-audience-audio.md)

Status: planned · Risk: **high** · Verification: **L0, written developer guidance and owner assumptions; not source- or bench-verified**

## 1. Why this record exists

ShowMesh may eventually feed a third-party listener system that reproduces media from uploaded assets and logical playback state rather than receiving the locally rendered PCM program bus. The Audio Engine retains this generic adapter class without designing around an undocumented product interface.

On 2026-08-17 the owner supplied a written response from the developer of a candidate third-party system. The developer recommended defining the synchronized-remote-output interface inside ShowMesh, exercising it against a mock destination, and revisiting a real adapter after the Audio Engine emits media changes, start/stop state, position, and timing information. The developer also recommended providing all audio files to the service in advance of playback so it has time to transcode them.

That guidance establishes a useful integration boundary. It does **not** establish an upload API, accepted source formats, storage topology, acknowledgement, processing or readiness status, playback control surface, timing behavior, or mix fidelity.

## 2. Current evidence and assumptions

**Written developer guidance, supplied 2026-08-17:** a candidate integration should receive all required audio files before playback so server-side transcoding can occur; its adapter should be designed only after ShowMesh's generic audio session boundary is producing real state.

**Owner assumption, 2026-08-17:** the first compatibility corpus should contain the audio formats FPP recognizes as audio in its user interface. This is a practical baseline and remains L0. It is not evidence that the destination accepts every such format, and it must not narrow the codec-agnostic ShowMesh asset store.

**Owner-observed product limitation, 2026-08-17:** the existing FPP-side plugin does not expose an obvious transcode-ready or media-ready status. The future integration must therefore support an honest no-status path rather than making a machine-readable readiness result mandatory.

No source, product documentation, command, endpoint, payload, capture, version, successful transfer, or synchronized playback has been supplied. This record remains L0.

## 3. Architectural posture pending evidence

- The generic ShowMesh asset store remains codec-agnostic and authoritative for source identity and content hash.
- Local/FM/LTC playback remains the Day-0 path under ADR-017 and Track C. A real third-party adapter is not a Day-0 gate.
- The synchronized-remote-output boundary has separate advance-provisioning and logical-playout responsibilities as specified in `AUDIO-ENGINE.md` §8.1.
- A third-party copy is a delivery representation keyed by destination, immutable destination-configuration revision or fingerprint, and ShowMesh content hash, not a new source asset.
- Accepted formats, acknowledgement, processing state, readiness observation, mixing, ducking, announcements, seeking, position reporting, and latency are adapter capabilities with `unknown` as a valid value.
- An upload attempt or acknowledgement never becomes `ready` implicitly. Where no destination status exists, optional-output readiness remains warning/unknown; a required-output policy may use current operator attestations pinned to the exact destination-configuration revision or fingerprint and **every** audio/announcement content hash required by the pinned session revision. One verified playlist item is insufficient.
- The generic interface and deterministic mock are built after core Track C sessions emit real state. No product name enters the capability vocabulary.

## 4. Questions for source verification

1. What product versions and service components receive media and playout state?
2. What API, SDK, filesystem, plugin, UI, or other mechanism transfers a file?
3. Which formats, codecs, sizes, durations, channel layouts, and sample rates are accepted?
4. What acknowledgement exists for transfer, registration, transcoding, and actual readiness, if any?
5. How is a ShowMesh asset id or content hash related to any remote media identifier?
6. Can an existing remote asset be queried, replaced, deduplicated, retained, or deleted?
7. Does the service receive independent sessions plus mix intent, a selected single media item, or some other representation?
8. Can it reproduce background/show transitions, maximum gain, fades, ducking, mixing, looping, resume/restart, and announcements?
9. What clock or timeline does it follow, and what happens on pause, seek, restart, late join, network loss, or coordinator loss?
10. What end-to-end listener latency and variance exist relative to the local/FM path?
11. Can it operate while FPP's own audience-audio output is disabled?
12. What authentication, licensing, network, quota, retention, and privacy constraints apply to uploaded files?

## 5. Test matrix

### 5.1 Generic mock, after the Track C core

- Provision every asset before its first playback command; refuse a mock mode that tries to upload on `start`.
- Exercise duplicate provisioning, interrupted transfer, acknowledgement without readiness, no acknowledgement, no status API, and explicit failure.
- Replace an asset with the same runtime filename and a different hash; retain unambiguous remote identity.
- Run full-mix and one-session-only capability profiles through single assets and multi-item playlists, item advancement, sequential/gapless/crossfade transitions, background, show, announcement, duck, fade, loop, media-change, start, stop, seek, and resume cases.
- Deliver delayed, duplicate, and out-of-order commands; prove a stale revision cannot overwrite newer desired state.
- Exercise optional warning behavior and a required-output policy whose current evidence covers every exact required playlist and announcement hash. Include one verified plus one unverified item and prove the result remains unsatisfied.

### 5.2 Real integration, when an interface exists

- Attempt each format in the recorded FPP-recognized baseline and preserve the exact input, request path, response or absence of response, and result. Characterize failures instead of generalizing from one success.
- Relate each destination object to the ShowMesh content hash; verify duplicate name, replacement, missing, and corrupt cases.
- Record every available transfer or processing observation. Where none exists, record that absence rather than inventing a status.
- On the intended listener device, select the expected asset and record attributed, time-stamped audible verification for that destination, immutable destination-configuration revision or fingerprint, and content hash.
- Repeat audible verification after content replacement or destination-configuration change; the earlier attestation no longer applies.
- Start, stop, seek, loop, fade, and resume background and show items; advance a multi-item playlist under every declared item-transition mode; exercise an announcement during background and show audio.
- Restart the third-party service, FPP, and ShowMesh independently during provisioning and playback.
- Measure latency and variance against the local/FM program path.
- Repeat with FPP audience-audio output disabled.

## 6. Acceptance criteria

### Generic ShowMesh boundary

- The deterministic mock consumes real Track C media/playlist changes, item advancement, start/stop state, position, timing, source-role, loop, item-transition, and gain intent.
- Provisioning always precedes playback and is keyed by destination, destination-configuration revision or fingerprint, and ShowMesh content hash.
- Unsupported and unknown capabilities remain distinct, and absence of a readiness API is represented without manufacturing success.
- Optional third-party uncertainty cannot block the local/FM path; a required policy evaluates coverage across all exact content hashes in the pinned session revision and can use current manual attestations explicitly where no status API exists.
- Passing the mock makes no claim about a real service.

### Real adapter

- The integration surface and supported product versions are source- or bench-verified.
- At least one real asset transfer and listener playback are captured end to end, with each evidence boundary identified honestly.
- ShowMesh can identify the destination media unambiguously and control or observe only the behavior the integration actually exposes.
- Background/show transitions meet the configured tolerance for supported operations; unsupported mixing, announcements, timing, or readiness appear as explicit limitations.
- Operation with FPP audience-audio output disabled is either proven or the conflict with ADR-017 is escalated as an architecture decision.

An operator hearing the expected asset on a phone is useful manual evidence. It proves that asset was reproduced on that destination at that time; it does not prove server-side transcode completion as a distinct step, synchronization tolerance, future availability, or equivalent playback on every listener.
