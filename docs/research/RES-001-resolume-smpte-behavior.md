# RES-001: Resolume SMPTE Behavior

[Architecture](../architecture/ARCHITECTURE.md#5-synchronization-model) · [Tracker](README.md) · [Audio research](RES-007-audio-node-architecture.md) · [Failure testing](RES-009-failure-mode-testing.md)

Status: planned (bench) · Risk: critical · Verification: L1 (capabilities) — source verified 2026-08-10; fault behavior remains L0

**This record is day-0 scope as of 2026-08-13**, promoted from "not sequenced". Controlling Resolume is one of the three founding problems ShowMesh exists to solve, alongside virtual-matrix generation and FPP's scheduler, so it cannot be cut to make a date. Its bench work leads [Track D](../build/TRACK-D-resolume.md), because the adapter's error handling would otherwise be a design against behaviour nobody has observed.

**The installation, confirmed 2026-08-13:** Arena **7.23.2** on macOS, and Halloween runs this version. The REST API needs 7.8 or later, so it is available. The host is a Hackintosh on a dying platform and **may move to Windows**, so nothing here may acquire a macOS assumption. A version upgrade is planned for Christmas, which is a revalidation trigger for everything this record establishes.

**Layer activation is a precondition for the timecode path**, not a separate feature: a clip launches from timecode only when its layer is active. That makes an inactive layer a **silent failure**, since timecode arrives, nothing launches, and Resolume reports no error because nothing was asked of it. Layer-active state therefore belongs in pre-show readiness evidence rather than being discovered from the yard.

**The timecode chain runs through the audio node**, which is easy to miss: Arena accepts SMPTE only as audio LTC, so the signal originates on the [Track C](../build/TRACK-C-audio-node.md) node's discrete output and reaches Resolume over a physical cable. That makes the audio interface's output addressing a prerequisite for this record's bench, and it means ShowMesh cannot observe the cable in the middle. Confirmation must rest on Resolume's own reported state, never on LTC having been generated.

## Decision to make

Determine whether Resolume can reliably follow the proposed LTC/SMPTE source through normal playback and timing faults, and define the supported timecode topology.

## Known context

Resolume performs final composition and house mapping in the reference installation. The proposed audio capability may generate LTC on a dedicated physical or Dante channel. Exact Resolume behavior is not yet verified.

## Questions

- Which frame rates, input devices, channel layouts, and timecode offsets are supported?
- Can two timecode inputs be selected or switched safely?
- What happens on acquisition, late start, pause, backward or forward jump, duplicate frames, loss, and reacquisition?
- Does Resolume chase continuously, seek on discontinuity, free-run, stop, or hold the last frame?
- How are individual clips, layers, and compositions configured to follow timecode?
- What observability is available through the UI, API, logs, or OSC?

## Acceptance criteria

- Acquisition and recovery behavior are repeatable across three runs.
- Measured presentation offset and jitter are documented for the intended frame rate.
- Loss and discontinuity produce a defined, operator-visible response.
- A cold-start and a mid-show Resolume restart recover without an undefined composition state.

## Test matrix and method

Record Resolume version, OS, audio interface, driver, LTC generator, frame rate, sample rate, composition, and output refresh rate. Test clean start, late start, 1–10 second loss, noise, frame jumps, source restart, device removal, and Resolume restart. Capture the LTC signal, Resolume output, logs, and a shared visual timing reference.

## Evidence and findings

Desk research 2026-08-10 (Resolume manual, API specs, forum excerpts; no bench work yet). Confidence tags: [doc] official manual/spec, [forum] forum-level evidence (Resolume forum pages are bot-gated; sourced from search excerpts — verify on bench).

### SMPTE input capabilities

- SMPTE input is **Arena only** (not Avenue) and is received **only as audio LTC** on a system audio input; no MTC or network timecode input exists (MTC is a long-standing feature request) ([manual: SMPTE](https://resolume.com/support/en/smpte)). [doc]
- Input device and **frame rate are configured per input in Preferences > Audio**; manual cites 25/29.97 as typical without enumerating the full list; rates above 30 are likely unsupported. Wrong frame rate manifests as a playhead jump every ~1 s — a useful health signature. [doc]
- **Two simultaneous SMPTE inputs** (SMPTE 1/2) are supported. [doc]
- Sync is **per clip** (Timeline dropdown → SMPTE 1/2) with per-clip **Offset** (hour-offset convention per track) and **Delay** compensation in frames; there is **no composition-level timecode transport**. [doc]
- **Clip launch is not driven by SMPTE** — "the clip trigger itself is not sent via SMPTE"; the clip must be connected by the operator or an API. This confirms the ShowMesh model: the adapter (or macro) launches clips; timecode only positions playheads. [doc]
- SMPTE is **not available on clips that have an audio track**. Content for timecode-followed surfaces must be video-only. [doc]
- Chase-on-jump is documented: skips and speed changes in the LTC are followed. **Loss behavior is undocumented**: forum evidence says the clip holds the last frame, with no freewheel/failsafe setting ([t=18457](https://resolume.com/forum/viewtopic.php?t=18457), [t=13632](https://resolume.com/forum/viewtopic.php?t=13632)). [forum — bench-critical]
- LTC over **Dante Virtual Soundcard** reportedly works (ASIO mode, Dante clock master required, matching sample rates), with one report of intermittent dropouts. [forum — bench-critical]

### Control and observability APIs

- **REST API + WebSocket** since 7.8: webserver on port 8080, base `/api/v1`, WS at `ws://host:8080/api/v1` ([REST](https://resolume.com/support/en/restapi), [WebSocket](https://resolume.com/support/en/websocket-api), [OpenAPI spec](https://resolume.com/docs/restapi/swagger.yaml)). Resources addressable by index, **stable by-id** (survives reordering/sessions), or `/selected`. Confirmable operations: full composition read, clip/column `connect`, active-clip query, read-only per-clip `connected` status, any parameter get/set, `/composition/open`, output `snapshot.png`. WebSocket pushes `parameter_update` on subscribed changes — the preferred observed-state channel. [doc]
- Spec limitation: **"Only Timeline and BPM Sync transport types are supported"** — SMPTE transport, offset/delay, and any lock status are **not modeled in REST**. [doc]
- **OSC** (UDP, default port 7000): addresses for every control, including `smpte1quickselect`/`smpte2quickselect` and `transporttype` (so OSC can switch clips onto SMPTE where REST cannot); "Output All OSC Messages" streams clip triggers and playhead position; `"?"` polls any address for read-back ([OSC](https://resolume.com/support/en/osc), [OSC list](https://resolume.com/download/Manual/OSC/OSC%20list.txt)). [doc]
- Recommended split, aligned with ARCHITECTURE §4.6: REST/WebSocket for confirmable management and observed state; OSC for low-latency triggers, high-rate playhead monitoring, and SMPTE transport selection.
- **Timecode lock is not observable via API** per the spec; the SMPTE panel (View menu) is UI-only. Logs exist (`~/Library/Logs/Resolume Arena/`, `%APPDATA%\Resolume Arena\`) but are not documented to contain timecode events. [doc/forum]
- Current version (Aug 2026): **Arena 7.27.1** (2026-07-17); REST requires ≥7.8; REST upgrades + MCP servers landed in 7.26 ([downloads](https://www.resolume.com/download/)). [doc]

### NDI input

- NDI inputs are always enabled and appear in the Sources tab; no hard connection limit; ~150 Mbit/s per 1080p60 stream; NDI cannot pin a NIC by default (disable Wi-Fi on the Resolume host or use Access Manager) ([NDI I/O](https://www.resolume.com/support/en/NDI_inputs_and_outputs)). No official latency figures. [doc]

### Open items for bench (L2) verification

1. **Timecode loss/pause behavior** (highest priority): confirm hold-last-frame, resume/re-chase latency, large-jump behavior on Arena 7.27 with the actual LTC generator.
2. Whether any OSC address or REST field reflects incoming SMPTE value or lock; enumerate `transporttype` choices; test setting SMPTE transport via OSC vs REST.
3. Supported frame-rate list in Preferences > Audio; drop-frame handling at 29.97; behavior at 30 fps.
4. What REST returns for a SMPTE-transport clip's `transport` object.
5. WebSocket vs OSC position update rate; REST `connect` → output latency.
6. LTC over Dante/virtual audio stability over multi-hour runs.
7. NDI input end-to-end latency in the reference topology.

## Decision, fallback, and revalidation

Direction (pending bench): drive Resolume Arena with audio LTC at a per-input-configured frame rate; ShowMesh launches clips via REST (confirmable) with OSC for SMPTE transport selection and high-rate monitoring; treat timecode lock as **unobservable via API** and design readiness checks around an external LTC health measurement plus Resolume playhead movement as indirect evidence. Until fault behavior is verified, HDMI or NDI transport must not be assumed to repair SMPTE faults. Fallback remains manual clip triggering or a locally timed playback path. Revalidate after major Resolume, OS, driver, or audio-interface changes.
