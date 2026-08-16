# RES-016: PulseMesh Audio Integration

[Research tracker](README.md) · [Audio Engine](../architecture/AUDIO-ENGINE.md) · [Resting Mode](../architecture/RESTING-MODE.md) · [ADR-017](../decisions/ADR-017-showmesh-owns-audience-audio.md)

Status: planned · Risk: **high** · Verification: **L0, owner-reported developer statement; not source- or bench-verified**

## 1. Why this record exists

PulseMesh was removed from tracked architecture on 2026-08-10 because the entire evidence base was one uncertain operator recollection. The architectural adapter categories were retained, but naming a product against them would have turned guesses into design.

On 2026-08-16 the owner reported a direct confirmation from Bob, identified as PulseMesh's developer: PulseMesh accepts uploaded MP3 files and uploads/transcodes them on its side. That is materially better context and is relevant to resting music, but it remains a relayed statement with no API documentation, capture, or working integration. This record preserves exactly that boundary.

## 2. Current evidence

**Owner-reported developer statement, 2026-08-16:** PulseMesh requires MP3 input for this music path, accepts file uploads, and transcodes the uploaded files within PulseMesh's service.

This establishes an implementation constraint for a candidate PulseMesh output binding: assets presented to it must be MP3. It does not establish the control API, timing behavior, upload protocol, playback ownership, mixing behavior, announcement support, latency, synchronization, or compatibility with FPP audio being disabled.

No source, command, capture, endpoint, payload, version, or successful playback was supplied. The record therefore remains L0.

## 3. Architectural posture pending evidence

- The generic ShowMesh asset store remains codec-agnostic.
- Local audio playback remains node-local under ADR-017.
- An eventual PulseMesh adapter declares MP3 as an output-specific asset constraint.
- Show readiness rejects or offers an explicit prepared derivative when the selected PulseMesh asset is not MP3; it never silently narrows every other output.
- The adapter must reconcile with ADR-017's requirement that FPP's own audience-audio output is unused.
- PulseMesh is not added to the capability vocabulary until the integration surface and runtime placement are established.

## 4. Questions for source verification

1. What product/version and service component accept the upload?
2. What API, SDK, filesystem, plugin, or UI performs it?
3. Does PulseMesh transcode only MP3 input, or does it accept other formats and produce a required internal representation?
4. Does playback require PulseMesh to run on the primary FPP host?
5. Can it operate while FPP's own audio output is disabled?
6. Does it receive media plus playback state, a rendered mix, or some other source?
7. Can it reproduce background/show transitions, maximum gain, ducking, and announcements?
8. What clock or timeline does it follow, and what happens on pause, seek, restart, or coordinator loss?
9. What end-to-end listener latency and variance exist relative to the local/FM path?
10. What authentication, licensing, network, retention, and privacy constraints apply to uploaded files?

## 5. Test matrix

- Upload a known MP3 and preserve request/response, resulting asset identity, and transcode status.
- Reject or characterize WAV and FLAC rather than infer behavior.
- Start, stop, seek, loop, fade, and resume a background item.
- Transition background to show and back with measured timing.
- Play an announcement during background and during show audio.
- Restart PulseMesh, FPP, and ShowMesh independently during playback.
- Measure latency and variance against the local/FM program output.
- Repeat with FPP audience-audio output disabled.
- Verify missing, corrupt, duplicate-name, and replaced assets.

## 6. Acceptance criteria

- The integration surface and supported product versions are source-verified.
- A real MP3 upload and transcode are captured end to end.
- ShowMesh can identify the resulting media unambiguously and control or observe its playback honestly.
- Background/show transitions meet the resting specification's configured tolerance.
- Unsupported mixing, announcements, or timing behavior appear as explicit adapter limitations.
- Operation with FPP audience-audio output disabled is either proven or the conflict with ADR-017 is escalated as an architecture decision.

