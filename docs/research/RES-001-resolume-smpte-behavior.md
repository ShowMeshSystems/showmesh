# RES-001: Resolume SMPTE Behavior

[Architecture](../architecture/ARCHITECTURE.md#5-synchronization-model) · [Tracker](README.md) · [Audio research](RES-007-audio-node-architecture.md) · [Failure testing](RES-009-failure-mode-testing.md)

Status: planned (bench) · Risk: critical · Verification: **control and observability APIs L2** (bench-verified 2026-08-14 on Arena 7.23.2); SMPTE input capabilities L1 (source verified 2026-08-10); **fault behavior remains L0**

> **The control and observability half of this record was benched on 2026-08-14** and is captured in [docs/bench/resolume-control-surface.md](../bench/resolume-control-surface.md). **Four claims in §Control and observability APIs were wrong** and are corrected in place below, each marked. The timecode half is untouched: no LTC source, no interface, no cable, so **§SMPTE input capabilities stays L1 and fault behaviour stays L0.** The bench ran on the owner's arm64 laptop rather than the deployed Hackintosh, so protocol and schema findings travel and **timing numbers do not**.

**This record is day-0 scope as of 2026-08-13**, promoted from "not sequenced". Controlling Resolume is one of the three founding problems ShowMesh exists to solve, alongside virtual-matrix generation and FPP's scheduler, so it cannot be cut to make a date. Its timecode-fault bench (D0) is outstanding and bounds the adapter's timecode-loss handling specifically; it does not lead or gate the rest of [Track D](../build/TRACK-D-resolume.md), which built from the 2026-08-14 control-surface capture.

**The installation, confirmed 2026-08-13:** Arena **7.23.2** on macOS, and Halloween runs this version. The REST API needs 7.8 or later, so it is available. The host is a Hackintosh on a dying platform and **may move to Windows**, so nothing here may acquire a macOS assumption. A version upgrade is planned for Christmas, which is a revalidation trigger for everything this record establishes.

**Layer activation is a precondition for the timecode path**, not a separate feature: a clip launches from timecode only when its layer is active. That makes an inactive layer a **silent failure**, since timecode arrives, nothing launches, and Resolume reports no error because nothing was asked of it. Layer-active state therefore belongs in pre-show readiness evidence rather than being discovered from the yard.

**The timecode chain runs through the audio node**, which is easy to miss: Arena accepts SMPTE only as audio LTC, so the signal originates on the [Track C](../build/TRACK-C-audio-node.md) node's discrete output and reaches Resolume over a physical cable. The bench does not wait on that interface, per Track D's dated correction: D0 needs LTC, not ShowMesh's LTC, and any off-the-shelf generator answers the open question. The audio node is the show's LTC source, not the bench's. What remains true is that ShowMesh cannot observe the cable in the middle. Confirmation must rest on Resolume's own reported state, never on LTC having been generated.

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

**This subsection is now L2. Corrections from the 2026-08-14 bench are marked inline; every unmarked claim was confirmed.**

- **REST API + WebSocket** since 7.8, base `/api/v1`, WS on the same port. ~~port 8080~~ **corrected: the port is configuration, not a constant** — the reference installation runs **9080**, read from `Preferences/server.xml`, which also binds `0.0.0.0` with **no authentication and `Access-Control-Allow-Origin: *`**. Resources addressable by index, **stable by-id**, or `/selected` — **confirmed, and the stability now has a boundary**: object ids survive reordering *and* restart (14/14 clip ids identical across a restart) and survive edits and re-saves (246 clip ids carried from `Christmas 24` to `Christmas 25`), but **parameter ids are session-scoped and change on every restart** (0/14 identical), which invalidates every WebSocket subscription silently. [L2]
- ~~Confirmable operations include `/composition/open` and output `snapshot.png`.~~ **Corrected: neither exists in 7.23.2.** There is no path that loads a composition and no rendered-output image endpoint; only per-clip thumbnails. Also absent: any collection endpoint, so `/composition/layers` 404s and the only way to enumerate is the **2.26 MB** full composition read, roughly half of which is a byte-identical duplicate of the layers array nested inside `layergroups`. [L2]
- **The application serves its own OpenAPI 3.0.1 spec** at `/api/docs/rest/swagger.yaml` (216 KB), version-matched to the running binary, plus Swagger UI and two example apps. This was not previously recorded and is the best contract available for any generated client. [L2]
- ~~Spec limitation: **"Only Timeline and BPM Sync transport types are supported"** — SMPTE transport, offset/delay, and any lock status are **not modeled in REST**.~~ **Corrected: this record misread the spec's own scope.** That line describes the **`transport` sub-object's** schema. `transporttype` *is* a first-class `ParamChoice` in REST with options `Timeline, BPM Sync, SMPTE 1, SMPTE 2, Denon DJ, Pioneer DJ`, and **REST sets it**: `PUT /parameter/by-id/{id}` with `"SMPTE 1"` returned 204 and read back. Option lists vary per clip — **244 of 252 clips offer SMPTE, 8 offer only `Timeline, BPM Sync`**, consistent with the audio-track restriction. This closes open item 2. [L2]
- **What a SMPTE-transport clip's `transport` object returns** (open item 4, closed): `position` is retained with the clip's own duration bounds and **`controls` becomes JSON `null`**. No offset, delay, lock, or incoming timecode value. That `null` is the shape this project was already caught by once on FPP's `"ma": null`; a decoder must not map it to a zero-valued control set. **`transport.position` remaining readable under SMPTE is the indirect evidence this record's decision section hoped for** — whether it *tracks* incoming LTC is still L0 and is exactly what the bench must measure. [L2]
- ~~**OSC** addresses for every control … so OSC can switch clips onto SMPTE where REST cannot … `"?"` polls any address for read-back.~~ **Corrected, and this is the largest reversal.** OSC input on UDP 7000 works, but **its default address space is positional only**: a message to a pinned `objects/<id>` address does nothing, verified against five spellings from a disconnected baseline. Pinning is a **shortcut-system** feature (DMX and MIDI honour it) living in a preset file **no API exposes**. **OSC produces no reply of any kind** — not for a working address, not for a failing one, and not for the `"?"` query form; Arena's outbound OSC goes to a single preference-configured target unrelated to any sender, at **236 datagrams/s idle and 481 with one clip playing**. `smpte1quickselect`/`smpte2quickselect` do exist and are **layer-scoped**, which is a different thing from a clip's `transporttype`; their semantics were not determined. [L2]
- ~~Recommended split: REST/WebSocket for confirmable management and observed state; OSC for low-latency triggers, high-rate playhead monitoring, and SMPTE transport selection.~~ **Superseded 2026-08-14.** A REST connect is observable in **4–64 ms**, which removes the latency argument, and OSC can neither name the right clip nor report arrival. The split is now REST to act and REST to confirm, with one WebSocket held purely as a change signal. See [TRACK-D-ADAPTER-SPEC.md](../build/TRACK-D-ADAPTER-SPEC.md) §3.1.
- **Timecode lock is not observable via API** per the spec; the SMPTE panel (View menu) is UI-only. **Consistent with the bench**: the word `timecode` does not appear anywhere in composition state, and `SMPTE` appears only as option strings. Logs exist but are not documented to contain timecode events. [doc/forum, and L2 for the state search]
- **Two behaviours the desk research could not have found, both bearing on ADR-003.** A **disconnect is not observable until the layer's transition completes**, proven causal at 0.0 s → 75 ms, 0.5 s → 531 ms, 2.5 s → 2,527 ms, 5.0 s → 4,068 ms, so a confirmation deadline must be derived from `layers[i].transition.duration` rather than fixed. And **a clip on a bypassed layer, or on a layer at zero master, still reports `connected: "Connected"` with `active_clip` present**, so `connected` is not evidence that anything reached the output. [L2]
- Current version (Aug 2026): **Arena 7.27.1** (2026-07-17); REST requires ≥7.8; REST upgrades + MCP servers landed in 7.26 ([downloads](https://www.resolume.com/download/)). [doc]

### NDI input

- NDI inputs are always enabled and appear in the Sources tab; no hard connection limit; ~150 Mbit/s per 1080p60 stream; NDI cannot pin a NIC by default (disable Wi-Fi on the Resolume host or use Access Manager) ([NDI I/O](https://www.resolume.com/support/en/NDI_inputs_and_outputs)). No official latency figures. [doc]

### Open items for bench (L2) verification

1. **Timecode loss/pause behavior** (highest priority): confirm hold-last-frame, resume/re-chase latency, large-jump behavior with the actual LTC generator. **Still open. Untouched by the 2026-08-14 bench, which had no LTC source.**
2. ~~Whether any OSC address or REST field reflects incoming SMPTE value or lock; enumerate `transporttype` choices; test setting SMPTE transport via OSC vs REST.~~ **Closed 2026-08-14 except for the lock half.** Choices enumerated; REST sets SMPTE transport; no field anywhere in composition state reflects an incoming SMPTE value or lock. Setting it over OSC was not tested and no longer matters, since REST does it.
3. Supported frame-rate list in Preferences > Audio; drop-frame handling at 29.97; behavior at 30 fps. **Still open.**
4. ~~What REST returns for a SMPTE-transport clip's `transport` object.~~ **Closed 2026-08-14:** `position` retained, `controls: null`.
5. ~~WebSocket vs OSC position update rate; REST `connect` → output latency.~~ **Partly closed 2026-08-14.** REST `connect` is *observable in state* at 4–64 ms; **connect-to-photons was not measured** and needs a camera or a capture card, so it stays open. WebSocket narrow update 15–35 ms, full composition push 102–134 ms at 2.27 MB per structural change; OSC output 236–481 datagrams/s. All timings are from an arm64 laptop on loopback, not the show host.
6. LTC over Dante/virtual audio stability over multi-hour runs. **Still open.**
7. NDI input end-to-end latency in the reference topology. **Still open.**
8. **New, 2026-08-14: what the API reports while a composition is swapped without a restart.** A restart produces a ~1.2 s window in which REST answers `200 OK` with a composition that is not the show, carrying the correct name for the last 0.7 s. The swap case is the one the owner says actually recurs, and assuming it is atomic would be unwise.
9. **New, 2026-08-14: whether the crossfader can silence a layer that passes every other readiness term.**

## Decision, fallback, and revalidation

Direction (timecode half still pending bench): drive Resolume Arena with audio LTC at a per-input-configured frame rate; ShowMesh launches clips via REST (confirmable) ~~with OSC for SMPTE transport selection and high-rate monitoring~~ **and uses no OSC at all, corrected 2026-08-14: REST sets SMPTE transport, and OSC can neither name a pinned clip nor report arrival**; treat timecode lock as **unobservable via API**, which the bench confirmed, and design readiness checks around an external LTC health measurement plus Resolume playhead movement as indirect evidence — `transport.position` is confirmed readable under SMPTE transport, so that evidence exists, though whether it moves with LTC is still L0. Until fault behavior is verified, HDMI or NDI transport must not be assumed to repair SMPTE faults. Fallback remains manual clip triggering or a locally timed playback path. Revalidate after major Resolume, OS, driver, or audio-interface changes.
