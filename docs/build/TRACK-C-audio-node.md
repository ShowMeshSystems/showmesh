# Track C: Audio node

[Build plan](BUILD-PLAN.md) · [Audio engine spec](../architecture/AUDIO-ENGINE.md) · [RES-007](../research/RES-007-audio-node-architecture.md) · [RES-016](../research/RES-016-third-party-synchronized-audio-output.md) · [ADR-017](../decisions/ADR-017-showmesh-owns-audience-audio.md) · [ADR-018](../decisions/ADR-018-program-and-ltc-share-a-clock-domain.md) · [ADR-019](../decisions/ADR-019-audio-device-loss-fails-silent.md) · [ADR-028](../decisions/ADR-028-show-asset-store-and-identity.md)

Status: **ALL ELEVEN SEAMS BUILT — C0a-1, C0a-2, C1a, C1b, C2, C3, C4, C5, C6, C7, C8 — on `track-c/audio-node`, not merged to `main`.** Specified 2026-08-13; dependency-complete review 2026-08-17. Pipeline/graph behaviour measured in C0a-1, C1a's real-device probe test, and C2's discoverer-versus-decode measurement is L2 (see RES-007); everything hardware- or device-dependent, and every seam's behaviour against a real pipeline, remains L0 design intent until the benches or seams below run for real. **C4 is built** (mix policy, gain, fades, mute, and the announcement primitives) against the same fake backend the session layer uses, so its semantics are specified and tested and none of it has ever produced sound. **C5 is built** (`f7743c5`): live LTC generation to the owner's ruling on Linear SM-69/SM-83. **C8 is built** (`cb8a45a`): the generic synchronized-remote-output mock against a deterministic fake. **No seam ships an audio engine**: the shipped fake reports unavailable everywhere and every outcome is forced to `unconfirmable` except the session layer's own `refused`/`failed`. **C0b — per-device commissioning — is the only remaining deliverable in this track, and it cannot start until an interface is selected** (RES-007's 2026-08-14 note stands). See "Track C, end state" below for what is, and is not, true across the whole track in one place.

## Track C, end state

This section states plainly, in one place, what the eleven built seams do and do not establish. Everything below is drawn from the seam sections that follow, the two commit messages (`git show f7743c5`, `git show cb8a45a`), and the gates the orchestrator actually ran; it restates nothing that isn't evidenced elsewhere in this document.

**Built.** All eleven seams — C0a-1, C0a-2, C1a, C1b, C2, C3, C4, C5, C6, C7, C8 — are built on `track-c/audio-node`. Every one is gated with `go build ./...`, `go test -race ./...`, `make check`, and an isolated `make test-integration` run:

- C0a-1 through C4, C6, C7 (through `84c4865`): `go build ./...` exit 0, `go test -race ./...` exit 0, `make check` exit 0, `make test-integration` exit 0, 75 tests, `ok github.com/showmeshsystems/showmesh/test/integration 309.808s`, broker isolated.
- C5 (`f7743c5`): `go build ./...` exit 0, `go test -race ./...` exit 0, `make check` exit 0, `make test-integration` exit 0, 75 tests, `ok github.com/showmeshsystems/showmesh/test/integration 320.719s`, broker isolated.
- C8 (`cb8a45a`): `go build ./...` exit 0, `go test -race ./...` exit 0, `make check` exit 0 (run by the builder), `make test-integration` exit 0, 75 tests, `ok github.com/showmeshsystems/showmesh/test/integration 300.739s`, broker isolated, run by the orchestrator.

**NOT true, and this is the load-bearing half of this section.** No audio hardware has been touched. No interface is selected (RES-007's 2026-08-14 note stands). No drift has been measured. No real show has run. **Nothing has produced sound.** Every seam is built against a fake engine that reports unavailable, and every session command outcome is `unconfirmable` except the session layer's own `refused`/`failed`. A real pipeline backend is an open owner decision, Linear SM-68, and nothing in this track's own code is what blocks it — every seam here is written against a fake in its place.

**What the bench did establish, at L2 and in a container only.** C0a-1's seven runs plus C1a's and C2's real-device/real-decoder measurements answered the GStreamer graph questions from RES-007's first bench item — pipeline construction from runtime-discovered capabilities, channel mapping, decode, sessions, gain/duck/fade behaviour, discoverer-versus-full-decode metadata — against real GStreamer and ALSA in a Linux container, never against a physical interface. See RES-007 for the full record. This is confidence about GStreamer's own behaviour, never about a show.

**What is owed, and to whom:**

- **C0b commissioning and every hardware measurement** — per-device channel independence, click-free output, full-show program-to-LTC alignment, drift, hot-plug, sample-rate change, and the installed-path alignment acquisition method — is owner/hardware work, tracked as **Linear SM-74**, blocking **SM-15, SM-75, SM-76**; the LTC-generation graph's channel-discreteness claim is **SM-77**.
- **The Operator UI controls** for `audio.settings`, `audio.node`, and the C6/C7 telemetry this track publishes do not exist — **Linear SM-73**.
- **Open owner decisions**: the real pipeline-control backend (**SM-68**), whether announcements ever interrupt show audio (**SM-70**), and three more owner decisions this track has raised and not resolved (**SM-71, SM-72, SM-78, SM-84**).

## C5, complete 2026-08-18

`f7743c5`. LTC generated live by a supervised external `libltc`-based process, program on channels 1–2 and LTC on a discrete channel 3. Built to the owner's ruling, Linear SM-69 and SM-83.

- **LTC is generated live by an external `libltc` process the agent supervises, never played from a pre-rendered file** — the owner's ruling (SM-69), because each sequence needs its own configurable start offset and a rendered file cannot carry one without a file per sequence. The bench had measured the pre-rendered path and the orchestrator recommended it on that evidence; the owner ruled against it on stronger grounds, and the objection to live generation — that a generator process inside the media path can die while everything around it looks healthy, and silent timecode loss looks exactly like a show sitting between cues — is engineered around here rather than re-argued.
- **Frame rate is a closed vocabulary of 24, 25, 29.97, and 30** (SM-83), what Resolume supports.
- **Non-drop ships explicitly at every rate, including 29.97, with the reason recorded in the configuration field's own description**: RES-001 leaves Resolume's drop-frame expectation at 29.97 unresearched, and 29.97 non-drop and 29.97 drop-frame are different timecodes that drift against each other. The justification sits where an operator sets the value rather than in a document they will not have open.
- **Generator liveness is observed from a heartbeat the generator emitted after the current attempt started, never from the OS process still existing** — a process can be alive and producing nothing. The builder scoped that claim honestly: with no pipeline backend yet there is no pipeline to infer liveness from, so the distinction actually enforced today is narrower than the rule's eventual form, and the code says so.
- **The graph places program on channels 1–2 and LTC on a discrete channel 3, and its own tests state what they prove: which channel carries what, and nothing about whether those channels are electrically discrete on any interface.** That stays a C0b commissioning check.
- **The start offset is the generating half of RES-001 section 54's per-clip Offset convention**, whose receiving half was documented and whose generating half was not until now. Default lives in `audio.settings`, with a per-session override in the apply payload.

Still no audio engine: the generator's output reaches a fake pipeline, and nothing here has produced timecode on a wire.

## C8, complete 2026-08-18

`cb8a45a`. The `AUDIO-ENGINE.md` §8.1 generic synchronized-remote-output boundary, implemented against a deterministic fake destination. No product is named anywhere in the code, the tests, or this report.

- **Provisioning and playout are separate interfaces**, so a component holding only the playout side cannot call `Provision` — "start triggered a transfer" is now a compile error rather than a runtime check.
- **The evidence vocabulary is exactly the six members section 8.1 names, with no `ready` anywhere**: an upload attempt is never readiness. A destination with no status interface stays attempted forever rather than being promoted, because absence of a readiness API is supported behaviour, not an adapter defect.
- **Capability support is three-state, and unknown is refused identically to unsupported** — nothing resolves an unknown into an assumption on the adapter's behalf.
- **Required coverage needs every exact content hash in the pinned revision.** One manually verified item beside one unverified item in a two-item playlist stays unsatisfied, the exact case an operator hits and the case the test names.

Passing the C8 mock proves ShowMesh's side of the AUDIO-ENGINE section 8.1 boundary and nothing else. It proves no real service accepts any format, completes an upload, finishes processing, stays synchronized, or plays on a phone. RES-016 stays L0.

## C4, complete 2026-08-18

`7f79d0d`. The program mix, gain, fades, mute/unmute, and the announcement primitives Track F's cues invoke. Built against the same fake backend as the session layer, so the semantics are specified and tested and **none of it has produced sound**.

- **`interrupt` is refused with a named reason**, never silently downgraded to `duck`. Whether announcements ever interrupt show audio is an owner decision (Linear SM-70); quietly doing something adjacent is how an operator discovers mid-show that the policy is not what they configured.
- **Fade completion comes from the engine reporting the gain actually reached**, never from the fade's duration elapsing. Track F builds a transition barrier on that outcome, so a fade reported complete on a stalled pipeline advances a show early.
- **An announcement restores the prior mix exactly once**, across a crash on either side of the restore boundary. A stuck duck is what an audience hears for the rest of the night.
- **The gain ceiling is enforced wherever a gain can be set**, and a clamp is reported rather than applied silently.

Review found the fade path had done the hard half and dropped the other: completion was derived correctly from a `FadeActive` transition and then written into session state no surface exposed, so `OutcomeFadeComplete` was a vocabulary member nothing could produce and the barrier Track F was promised did not exist. Every test passed, because they asserted what the code did rather than what a caller could observe. Completion now resolves onto the fade's own invocation result, gated like every other outcome, so it becomes reachable the moment a backend exists. Testing it at all required giving the fake the ability to report available, which is its own small lesson: a fake that cannot express the outcome under test cannot verify it.

The orchestrator then found the comparison itself was exact float equality, which no fake-based test could reach; see [LESSONS.md](LESSONS.md).

## C6 and C7, complete 2026-08-18

**C6 (telemetry, readiness, and command outcomes) and C7 (failure containment and manual recovery)** are complete, built together and reviewed on `9e613d7`, with one product-bug fix and two test-defect fixes on top on `84c4865`. Full record: this section, LESSONS.md, and the two commit messages (`git show 9e613d7`, `git show 84c4865`).

**Why built together:** C7's faults are only observable through C6's evidence, so the commit shipped the eighteen `audio_session.*` signals and the fault vocabulary in one pass.

**WHAT SHIPPED, on `9e613d7`:**

- **Eighteen `audio_session.*` signals** (`internal/coordinator/collector/nodeaudio/signals.go`, `SessionSignalIDs`), one per session under the reserved `audio_session` resource kind: source role, playlist revision/item id/item index, position, reference position, drift, state, state reason, desired revision, effective gain, gain ceiling, fade state, mix duck-by, readiness state/reason, and fault kind/reason.
- **One new node-level signal, `node.audio.clock.alignment`**, the twelfth signal under the existing `node` resource kind. **It reports `not_collected` with a reason, always** (`noAlignmentMeasurementReason`, `internal/coordinator/collector/nodeaudio/collector.go`): nothing in this seam measures program-to-LTC alignment, and the signal is never derived from the program and LTC buses both reporting usable, and never from `node.audio.clock.domain`/`.provenance` declaring a shared clock. `TestClockAlignmentAlwaysNotCollected` (`internal/coordinator/collector/nodeaudio/session_observations_test.go`) enforces this by failing if the signal is ever made to look measured — the value of a green alignment light is that it means measured, not configured.
- **A fault vocabulary that never collapses into `stopped`** (`pkg/audio/fault.go`, `SessionFault`): originally six named classes (`pipeline_crash`, `freeze`, `decode_failure`, `media_disappeared`, `route_changed`, `timing_authority_lost`) plus `none` and the catch-all `other`, eight reserved values total. `TestSessionFaultSignalsDistinguishAllSix` and `TestSixFaultsStayDistinct` proved none collapses into another or into a session that stopped on its own or was operator-stopped.
- **A fault never self-clears.** It clears only on a successful `prepare` — the one choke point every `prepare`, `start`, `advance`, and restore-time revalidation passes through (`internal/agent/audio/session.go`, `restore.go`) — so a session reporting `route_changed` keeps reporting it through unrelated commands until something actually revalidates. `TestFaultClearsOnlyOnRevalidation` and `TestRestartRevalidatesRatherThanBlindlyCarryingAFault` cover this, the second because a restart counts as revalidation (a real engine rebuilds its pipeline from nothing) and the fault must be re-derived, not carried forward blindly.
- **The manual recovery procedure** ADR-019 made a required deliverable when it removed automatic fallback: [AUDIO-ENGINE.md §11.4](../architecture/AUDIO-ENGINE.md), "The procedure" — six steps (identify the failed component from `audio_session.fault.kind` and the `node.audio.*.state` signals; confirm the failed output is silent rather than misrouted; revalidate route, channel separation, clock relationship, every pinned asset, and the current playlist item; restart or reassign; treat reversion to FPP audio as a deliberate installation change, never a command; confirm recovery from the fault kind returning to `none`, not from a command's own reported outcome). Not restated here — read it there.
- **Two defects fixed beyond the seam's own stated scope, both in `internal/agent/audio/mix.go`:** the fade-completion read's error path was previously unclassified, so a real backend surfacing an error there produced no fault at all; it now runs the same `ClassifyFault` path `Manager.watchTick` already uses. And fade completion compared the engine's reported gain to its target with exact float equality — a real backend computing a ramp reports `0.19999999999999998` for a target of `0.2`, which would have reported a completed fade as `unconfirmable` forever and made Track F's transition barrier permanently unreachable the day a real engine arrived. The fake stores exact values, so no test written against only the fake could reach this. The comparison now uses a `1e-6` tolerance (`gainsEqual`), mutation-verified by `TestFadeCompletionToleratesFloatingPointDrift`, which also asserts the tolerance does not accept a gain that genuinely missed its target (`gainsEqual(0.3, 0.2)` must stay false).

**WHAT THE FOLLOW-UP FIXED, on `84c4865`:**

C6/C7's own new broker-loss integration test failed reproducibly. Chasing it found one product bug and two defects in the test itself, plus a third, unrelated fault-classification defect found in the same pass.

- **A single-media session never completed.** `advanceLocked` (`internal/agent/audio/session.go`) refused unconditionally when a session had no playlist, and the natural-completion watcher calls that same method, so a session playing one asset with no playlist reported `state=playing` forever after the asset finished, position pinned at the asset's full duration — measured at `posMs=2000` on a 2.0s asset, unchanged for 25+ seconds. Track F anchors night-loop transitions on observed completion, so this is a background track a night loop would wait for indefinitely. An unforced advance now completes a single-media session the way a playlist running off its end with `RepeatNone` does; a forced advance still refuses, because there genuinely is no next item. `TestNaturalCompletionOfASingleMediaSession` (`internal/agent/audio/advance_test.go`) is the new coverage; the existing natural-completion test had only ever exercised a two-item playlist.
- **`media_mismatch` is now its own fault, distinct from `media_disappeared`.** A content-hash mismatch (the asset is present but no longer matches its pinned hash) had been classified as `media_disappeared` (the asset is absent). C2's probe already distinguishes the two; sending an operator to hunt for a missing file that is actually present with different content is the wrong direction during a show. `pkg/audio/fault.go` now carries `FaultMediaMismatch` as a ninth reserved value (seven named classes plus `none` and `other`), `TestMediaMismatchFaultDistinctFromDisappeared` fails if the two are ever conflated again, and [AUDIO-ENGINE.md §11.4](../architecture/AUDIO-ENGINE.md) is updated to name both.
- **Two test defects in the broker-loss integration test itself** (`test/integration/audio_broker_loss_test.go`): it dispatched commands before the agent's MQTT subscribe completed, where a command is silently dropped rather than delayed — every other test in that package calls the documented warm-up barrier first, and this one didn't; and its own observer had no reconnect, so once the broker container stopped for the test it could never see anything published after the broker came back, no matter how long it waited.

**WHAT THIS IS NOT, still true:**

- **No audio engine exists.** The shipped fake reports `Available()` false everywhere, every outcome is forced to `unconfirmable` except the session layer's own `refused`/`failed`, and nothing advertises playback capability. A real pipeline backend stays blocked on the pipeline-control decision (Linear SM-68), which is what every seam here is written against a fake in place of.
- **Program-to-LTC alignment is not measured.** `node.audio.clock.alignment` reports `not_collected` with a reason on every poll; nothing in this codebase produces a measured value, and a test fails if that ever changes without a real measurement behind it.
- **No hardware, no interface, no real show.** Every fault class above is exercised by injecting the corresponding sentinel error into the fake; none has been triggered by a real device, a real decode failure, or a real route change.

**Verification gates.** The orchestrator ran the full gate suite against `84c4865` (C6/C7 plus the follow-up fixes, the current branch HEAD): `go build ./...` exit 0; `go test -race ./...` exit 0; `make check` exit 0; `make test-integration` exit 0, **75 tests**, `ok github.com/showmeshsystems/showmesh/test/integration 309.808s`, broker isolated to its own container and port. **One caveat travels with this run**: an earlier `make check` on the same tree failed at `TestFollowMacroRunNeverIdlesOutWhileTheCoordinatorKeepsAnswering` in `cmd/showmeshctl` — a package Track C never touched. That test passed 5 of 5 in isolation; the failure text was `"no update on run run-1 in 50ms"` while a 78-second suite ran concurrently alongside it, consistent with the project's own documented shared-host load sensitivity for `make test-integration` rather than a defect in this test or in Track C's own code. It is tracked as **Linear SM-97**, and the subsequent `make check` run on a quiet machine passed clean. It is reported here as a load-sensitive pre-existing test, not as a Track C failure, and it was not silently dropped.

## C3, complete 2026-08-18

**C3 (authoritative playback sessions)** is complete, built and reviewed on `0b307b7`. Full record: this section, LESSONS.md, and the commit message (`git show 0b307b7`).

**WHAT IS REAL:**

- **The session state machine, revisions, and idempotency** (`internal/agent/audio/session.go`, `manager.go`, `pkg/audio`'s `RevisionState`/`Field[T]` from C0a-2): a session tracks `pkg/audio`'s `State` vocabulary, a stale revision is refused (`stale_revision`), an empty invocation id is refused (`invalid_invocation`), and a replayed invocation with the *same* requested revision returns its original decision unchanged rather than re-executing. `internal/agent/audio/restore.go` and `persistence.go` rehydrate this from persisted state after a restart, so the anti-rewind guarantee (a stale desired revision cannot overwrite newer state) survives a crash rather than resetting to zero — this is `RestoreRevisionState` from C0a-2 actually wired to a caller for the first time.
- **Playlist advancement with both crash sides covered.** `internal/agent/audio/manager.go` persists the new current item *before* telling the engine, so a crash on either side of that write recovers to one unambiguous item rather than replaying the completed item or skipping/double-starting the next one. `advance_test.go` exercises both crash sides, and per the commit message the builder found its own two crash-side tests would still have passed with the real persist call deleted — a version of the "test proves nothing about reachability" lesson applied to a test's own fixture — and added a third test that reads the store from inside `Engine.Load` and fails if the persist call is removed.
- **Coordinator dispatch behind `audio:command`** (`internal/coordinator/api/audiodispatch.go`, `internal/coordinator/api/v1/audiocommand.go`): nine routes — `session.apply`, `session.prepare`, `session.start`, `session.pause`, `session.resume`, `session.seek`, `session.advance`, `session.stop`, `session.clear` — behind one shared dispatch core, gated by the `identity.ScopeAudioCommand` scope reserved in IDENTIFIER-REGISTER.md. Confirmation waits on the dispatched command's own MQTT result topic (`mqttproto.ResultTopic`) rather than polling a collector, because a fake engine's transitions are synchronous; the code comment in `audiodispatch.go` flags that a genuinely asynchronous real backend would need this reconsidered.
- **`showmeshctl audio session`** (`cmd/showmeshctl/cmd_audio_session.go`): CLI coverage for all nine operations, shipped in the same commit per ADR-030/ADR-039's parity rule, not deferred.
- **Schema v9** (`internal/coordinator/store/migrations.go`, `audiosessions.go`), reserved in IDENTIFIER-REGISTER.md for "audio session desired state," now applied.
- **The Engine interface** (`internal/agent/audio/engine.go`), the piece the commit message calls out as the deliverable that outlives the fake: nine methods, deliberately with no gain or fade method among them, so either eventual pipeline backend can implement it without reshaping the session layer above it.
- **The contract audit that found two real defects.** `api/openapi.yaml` initially described none of the nine registered endpoints. Writing the entries (`internal/coordinator/api/openapi_audiodispatch_test.go` conformance-tests them against a real handler, not a hand-built fixture) surfaced that a reused idempotency key was answered as a replay without checking whether the existing command's action and params actually matched the new request, and that replay responses read the outcome from the generic `commands` table rather than the session-specific outcome actually dispatched. Both are fixed on this commit. The contract documents no `404` for these paths, because nothing produces one.

**WHAT THIS IS NOT, prominently:**

- **This is not an audio engine.** How the agent controls a running GStreamer pipeline is an open owner decision, tracked as **Linear SM-68**: `gst-launch` cannot be controlled after it starts, which works for a renderer pushing frames one way and does not work for audio, whose graph changes on every duck, fade, and media swap. Everything in C3 is built against the `Engine` interface with `internal/agent/audio/fakeengine.go` behind it.
- **The fake's `Available()` returns false everywhere.** This is a deliberate visibility choice, not an oversight: unreachability is made structural rather than left implicit.
- **Every outcome is forced to `unconfirmable`**, except the session layer's own `refused` and `failed`, which carry the backend's reason. No command dispatched through this path can report `started`, `stopped`, or `completed` as a real playback outcome.
- **The node advertises no playback capability.** C3 adds nothing to what a node claims it can do; C1a/C1b's `audio.engine`/`audio.output.*` advertisement is unchanged and unextended by this seam.
- **Tests prove both of the above** (`internal/agent/audiocapabilities_test.go`, session/manager tests) — the absence of playback capability and the forced-unconfirmable behavior are asserted, not just true by omission.
- **This stays true until the pipeline-control decision is made.** Every seam here is built against the fake, so a real backend (Linear SM-68) is what turns any of it into audio; nothing in this track's own code is what blocks it.

**Two review findings, both fixed on `0b307b7` and folded into a new LESSONS.md entry:** the two openapi-contract defects described above (idempotency replay not checking action/params match; replay reading the generic table instead of the session-specific outcome). The same missing action/params check is still open on render dispatch — **Linear SM-60** — so this is recorded as a shape, not a one-off.

## C2, complete 2026-08-18

**C2 (asset resolution and the audio media probe)** is complete, built and reviewed on `93ff42c`. Full record: this section, the commit message (`git show 93ff42c`), and RES-007's new C2 subsection for the container measurement.

**What shipped:**

- **`audio.media.probe`** (`internal/agent/audio/mediaprobe.go`, `internal/agent/audiomediaprobe.go`): answers whether an assigned Track E asset is actually playable before a session admits it. Track E owns bytes, hashing, and distribution; C2 adds the audio-specific question that `mediaType: audio` and a familiar extension cannot answer.
- **A six-outcome fault vocabulary that never collapses into one**: `missing`, `hash_mismatch` (size or content hash disagrees with the assigned `MediaRef`, checked before any decode is attempted), `undecodable`, `unsupported_format`, `duration_unknown`, and `ready`. **`unknown` is separate again and is explicitly not a fault**: a probe that could not run says so rather than blaming the asset, matching the standing constraint that `unknown`, `unsupported`, `unconfirmable`, `not_ready`, and `failed` must never collapse into each other or into success.
- **Playlist readiness covers every item.** A multi-item playlist is not satisfied by one verified item; C2's playlist-readiness path probes each pinned item and reports per-item results, per AUDIO-ENGINE's readiness requirement.
- **Metadata comes from `gst-discoverer-1.0`, with a bounded decode still gating decodability.** The first implementation decoded each file in full to a temp PCM file and inferred duration from the resulting byte count. Measured by the orchestrator on a ten-minute FLAC in a Debian container with real GStreamer 1.26.2: full decode took 658 ms and wrote 110 MB; `gst-discoverer-1.0` took 11 ms, wrote nothing, and reported `Duration: 0:10:00.000000000` exactly rather than by inference. At playlist scale the full-decode approach was roughly ten seconds and 1.6 GB of temp writes per readiness run, on a node whose storage also holds the show assets it was writing over — and the decode carried a 20-second timeout, so a long file on a slow node could report `duration_unknown` and fail readiness for a perfectly good asset, at showtime. Metadata now comes from `gst-discoverer-1.0`; the decode check remains, bounded to a few buffers into a `fakesink`, writing nothing. **A discoverer success never overrides the bounded decode's verdict** — a truncated FLAC whose header still parses is the concrete case this catches, and `decode_test.go`/`mediaprobe_test.go` construct exactly that fixture: a FLAC truncated inside `STREAMINFO` makes `gst-discoverer` report both an error and a fully intact ten-second duration in the same result, and the probe must not trust the duration.
- **Evidence provenance on every result**, per ADR-011: each probe result carries which method produced it, because "the container says ten minutes" and "we decoded it" are different claims. Where `gst-discoverer-1.0` is absent, the probe falls back and says so — the absence of a tool is recorded as a tool-availability fact, never as a fault in the asset.

## C1a and C1b, complete 2026-08-18

**C1a (audio capability discovery and advertisement) and C1b (the retained node audio report, the `nodeaudio` collector, and the two configuration kinds)** are complete, built and reviewed together on one combined diff (`597e967`), with `showmeshctl` coverage shipped in the same commit rather than deferred. Full review-finding record: this section and [LESSONS.md](LESSONS.md); commit message: `git show 597e967`.

**What shipped:**

- **Probe-evidence-based capability advertisement** (`internal/agent/audio/`, `internal/agent/audiocapabilities.go`): the agent enumerates ALSA candidates (deduplicated by hardware card, not by alias — an earlier version let four aliases of one card fill every probe slot and the real device go unprobed), probes each with a real GStreamer pipeline, and advertises `audio.engine`, `audio.output.local`, and `audio.output.ltc` only from what the probe actually observed. The LTC capability now probes explicitly at the channel count that matters rather than accepting whatever the sink negotiates by default, and states that a probed third channel is not evidence it is physically discrete — that stays a C0b commissioning check. The probe is silent by two independent guards (it no longer builds `audiotestsrc wave=sine` at its default 0.8 volume) and runs once at agent startup or on explicit request; a periodic report loop republishes the cached evidence under its own observation time rather than re-running discovery. Enumeration failure is a real third state (`not_collected` with a reason), not an asserted-absent "no ALSA hardware card found" masquerading as current evidence.
- **The retained node audio report** (`internal/agent/audioreport.go`, `pkg/mqttproto.AudioPayload` on `showmesh.node.audio/v1`): published retained to `showmesh/nodes/<node-id>/observed/audio`.
- **The `nodeaudio` collector** (`internal/coordinator/collector/nodeaudio/`, source id `node-audio`): push-to-poll off `internal/coordinator/inventory`'s ingestion of that retained topic, turning the report into `observation.Observation` values under the `node` resource kind. Eleven signals ship (`node.audio.engine.state`/`.reason`, `node.audio.device.state`/`.reason`, `node.audio.outputs.count`/`.enumerated`/`.truncated`, `node.audio.program.state`, `node.audio.ltc.state`, `node.audio.clock.domain`, `node.audio.clock.provenance`), reaching `GET /api/v1/observations` and the SSE stream through the paths those already have. `ObservedAt` is stamped from the node's own probe time, never coordinator receipt time.
- **Two configuration kinds, both store-backed per ADR-039, with API paths and `showmeshctl` verbs:**
  - `audio.settings` — the engine-wide singleton (drift-ignore threshold, default fade curve/duration, default background gain ceiling). `GET/PUT /api/v1/config/audio.settings`, `GET /api/v1/config/audio.settings/revisions`; `showmeshctl audio settings get|set|revisions`.
  - `audio.node` — per-node physical binding (which discovered route carries program, which carries LTC, the operator-declared clock domain). `GET /api/v1/config/audio.node` (list), `GET/PUT /api/v1/config/audio.node/{id}`, `GET /api/v1/config/audio.node/{id}/revisions`; `showmeshctl audio node list|get|set|revisions`.
- **Placement refusal reads live probe evidence, never the operator's claim alone.** `config.ValidateAudioNodePlacement` is fed the target node's currently-advertised `audio.output.local`/`audio.output.ltc` capability `routes` attributes, read live from inventory on every write (not cached from the request or from anywhere else) — a node cannot be declared as carrying LTC on a route it never advertised. A node with no Hello, or a Hello advertising neither capability, refuses with `ErrAudioNodeNoEvidence` rather than a route list silently reading empty.

**Thirteen findings came out of review; three mattered enough to change behaviour, all above.** The rest, also fixed on `597e967`: the CLI previously round-tripped the operator's file through a typed struct, so an omitted drift threshold marshalled as `0` and was accepted, making the server's own `field_required` refusal unreachable through the CLI path — the emergency path FPP-down operators depend on; `NodeAudioObservations` was dead code, called by nothing; the capability-detection budget was sized for three probes and had eight more appended, so a slow host's timeout cancelled exactly the audio probes and the node advertised no audio at all; and the API accepted a `resourceKind` the OpenAPI contract did not list, with nothing that could have caught it — a test now exists. Five existing tests passed with the behaviour they named removed, including the one integration test that skipped whenever the probe reported unavailable for any reason; all five are fixed.

**What is OWED and NOT done, plain:**

- **No Operator UI control exists for either `audio.settings` or `audio.node`.** Both are reachable by API and by `showmeshctl` only. This is an ADR-039 parity gap neither of ADR-039's own enforcement tests can catch — those tests cover the `SHOWMESH_*` environment boundary and `api/openapi.yaml`'s non-`GET` paths, and neither can reach the UI (ADR-039's own stated asymmetry).
- **`audio.settings`'s four fields have no consumer.** Drift-ignore threshold, default fade curve, default fade duration, and default background gain ceiling are stored, revisioned, and audited, but nothing in this codebase reads them — no session engine with a real backend exists yet to apply them.
- **No configuration kind in this codebase can be deleted.** A mis-declared `audio.node` object, or one for a node that is later decommissioned, has no removal path — only new revisions. Queued for the owner.
- **`audio.node` names a route, not a channel.** `ProgramRoute` and `LTCRoute` identify which discovered output route carries which signal; neither field says which specific channel within a route carries LTC when a route exposes more than the minimum count. Queued for the owner.

## C0a-1 and C0a-2, complete 2026-08-18

**C0a-1 (the device-independent GStreamer bench)** is complete: seven runs against real GStreamer 1.26.2 on Debian 13.6 in a container. Capture record: [docs/bench/TRACK-C-AUDIO-BENCH.md](../bench/TRACK-C-AUDIO-BENCH.md); evidence folded into [RES-007](../research/RES-007-audio-node-architecture.md). Gates on this tree: `go build ./...` exit 0, `go test -race ./...` exit 0 (34 packages, zero failures), `make check` exit 0. The bench adds no Go and stays off the default path behind its own `make bench-audio` target; `make test-integration` was not run and this seam does not touch the broker, the coordinator, or the agent.

**C0a-2 (`pkg/audio`) is complete**: it pins the session command contract the coordinator, the audio agent, and **Track F's F5 seam** all bind to. Types only — no GStreamer, no MQTT, no HTTP, no import of anything under `internal/`. Same gates as above. The contract, at a level a Track F builder can write a fake against without reading the Go:

- **Vocabularies** (`pkg/audio/vocab.go`, each a closed set validated against a value; unknown members are rejected, never silently accepted): `SourceRole` (`show`, `background`, `announcement`, `manual`); `State` (`preparing`, `ready`, `playing`, `paused`, `stopping`, `stopped`, `completed`, `failed`, `unknown` — `completed` and `stopped` are permanently distinct, and `unknown` must never be treated as `stopped`/`completed`/`ready`); `RepeatMode` (`none`, `item`, `playlist`); `ItemTransition` (`sequential`, `gapless`, `crossfade` — a required `gapless`/`crossfade` is refused rather than silently downgraded to `sequential` when the output can't confirm it); `MixPolicy` (`mix`, `duck`, `interrupt`, `unsupported`); `ResumePolicy` (`resume`, `restart`); `FadeCurve` (`linear` only — additional curves are added from bench output, not guessed at; `equal_power` was proposed and deliberately rejected as unevidenced).
- **Identity and revision rules**: `SessionID` and `InvocationID` are caller-minted stable identities. `Revision` is a per-session monotonic counter; `RevisionState.Apply` refuses a requested revision that is not strictly greater than current (`stale_revision`), refuses an empty invocation id (`invalid_invocation`), replays an already-seen invocation's original decision unchanged when replayed with the same requested revision, and refuses a replay with a *different* requested revision (`invocation_revision_mismatch`). `RestoreRevisionState` rehydrates this from persisted state after a restart, so the anti-rewind guarantee survives a crash rather than resetting to zero. `MediaRef` identity is `AssetID` + `ContentHash` together (ADR-028); `RuntimeFilename` is preserved but never part of identity. A `Bookmark` resolves only by exact `ItemID` against a playlist whose `OwnerRevision` still matches the bookmark's pinned revision — never by index or filename fallback.
- **The fifteen reserved operation names** (`pkg/audio/ops.go`, `audio.` prefixed, closed set): `session.apply`, `session.prepare`, `session.start`, `session.pause`, `session.resume`, `session.seek`, `session.advance`, `session.stop`, `session.clear`, `gain.set`, `gain.fade`, `output.mute`, `output.unmute`, `device.probe`, `media.probe`. Media/playlist selection, loop policy, announcements, and ducking mint no operation of their own — they are fields of the `SessionDesiredState` a `session.apply` merges in (an announcement is `session.apply` with `SourceRoleAnnouncement` and a `MixPolicy`, then `session.start`).
- **The tri-state write rule** (`pkg/audio/field.go`, `Field[T]`): every mutable field on a write payload (`ApplyRequest`) is unset (key absent — leave unchanged), explicit JSON `null` (clear it), or set (replace it) — three distinguishable states, never collapsed to two. `Field[T].MarshalJSON` is now an **error** for an unset field, forcing every wire side (coordinator, agent, CLI) to build its own optional-shaped payload rather than risk marshalling an unmentioned field as `null` and silently clearing it. `ApplyRequest.Merge` validates the result (a session's source is one exact asset **or** one playlist, never both — `ErrSessionHasBothMediaAndPlaylist`) and, when a gain and a ceiling are both in effect after merge, clamps and reports the clamp via `ApplyCeiling` rather than storing an unclamped value silently.
- **Outcomes** (`pkg/audio/outcome.go`, closed set): `started`, `position`, `gain`, `fade_complete`, `stopped`, `completed`, `refused`, `failed`, `unconfirmable`. `refused`, `failed`, and `unconfirmable` require a non-empty reason; an effect that cannot be observed reports `unconfirmable`, never `started`.

**C0b (per-device commissioning) is owner hardware work**, tracked on `docs/private/PUNCH-LIST.md`, and is **not** claimed complete or in progress here — no interface is selected yet (see RES-007's 2026-08-14 note).

**Three things this bench hands to later seams**, recorded so they aren't rediscovered:

- **C1's capability discovery cannot call `gst-device-monitor-1.0`** on this package set — it is not installed by `gstreamer1.0-tools` on Debian 13 (R7). C1 needs `aplay -L`/ALSA APIs directly, or the image needs the package added.
- **C4's fade-completion verification needs a windowed envelope step, not a raw sample delta.** A raw delta cannot tell a fade from an abrupt cut at all (R4: both read `max_delta: 3421`); the envelope metric separates them (158 vs. 20978).
- **A controller driving a gain property needs `DirectControlBinding.new_absolute`, not `new()`.** `new()` maps its 0..1 control value onto the property's full range, which turned a requested gain of 1.0 into a 10x boost on `volume`'s 0..10 range.

## Goal and milestone boundary

ShowMesh owns audience-facing audio: a Linux node plays exact node-local show assets on its own clock, renders background music and announcements into the program bus, generates LTC on a discrete output in the same clock domain, exposes honest readiness and playback evidence, and fails silent when the device goes away.

This track is also Resolume's timecode source. Track D follows the show over physical LTC from this node; a session saying `playing` is not evidence that Resolume received or locked to the signal.

**Day-0 is the mid-September real-show path.** It requires local/FM program audio, same-clock LTC, the background and announcement primitives Track F consumes, exact local asset readiness, telemetry, fail-silent behavior, and manual recovery. A real synchronized third-party output, its upload protocol, server-side processing status, and playback on a listener device are **not Day-0 gates**.

The generic synchronized-remote-output boundary is specified now so the engine does not grow around one product. Its deterministic mock is built only after the core session engine emits real media changes, start/stop state, position, and timing. A real adapter remains later integration research.

## No specific interface gates this track

ADR-018 names a property, not a product. Program and LTC leave one interface in one clock domain; LTC lands on a discrete output that program never touches; program on USB with LTC on an independent Dante clock remains forbidden.

The engine builds against what the Linux audio stack reports. It reads output count and addressing at run time and contains no device-model branch. Capability advertisement carries the reported channel count and clock domain so the coordinator can refuse placement that cannot satisfy ADR-018.

Software discovery is necessary and insufficient. Some interfaces report four outputs while mirroring channels 1/2 onto 3/4 downstream of anything ALSA can observe. Commissioning therefore sends a tone to the intended LTC output alone, confirms that nothing appears on the program pair, and records the device, host, audio stack, sample rate, route, and result in RES-007. Failure blocks that interface, not this track.

## Standing constraints

- Nodes play complete local files against their own audio clock. The coordinator and broker never carry the real-time PCM path.
- Audio aligns to the FPP timeline at start and explicit correction points. It measures drift and never continuously slews program audio to chase small differences.
- Program and LTC share one clock domain. LTC never enters the program bus.
- FPP's audience-audio output is unused during ShowMesh operation.
- Device loss fails silent, preserves session state, alerts critically, and never hands audio back to FPP automatically.
- Track E owns source asset identity, bytes, hashing, and node distribution. Track C consumes exact content hashes and probes whether audio is actually decodable.
- `unknown`, `unsupported`, `unconfirmable`, `not_ready`, and `failed` are different states. No implementation may collapse them into success.

## Session and command contract

The engine implements the semantic session states and operations in `AUDIO-ENGINE.md` §3 and §14. The minimum Day-0 vocabulary is:

- select or replace media, including the same filename with a new hash;
- select a pinned ordered playlist, advance each item exactly once, and expose current item identity/index;
- prepare and probe an asset;
- start at an authoritative position;
- pause, resume, seek, restart, stop, and observe natural completion;
- set loop and resume-versus-restart policy;
- set and fade gain, including a maximum background gain;
- create an announcement and apply its configured duck, mix, or interrupt policy;
- mute and unmute an output without changing session authority.

Every state-changing command carries a stable invocation identity, target session, desired revision, deadline, and confirmation contract through the standard command envelope. Repeating one invocation is idempotent. A stale revision cannot overwrite newer desired state. Receipt confirms only receipt; Track F transition barriers require the named observed effect such as `started`, `fade-complete`, `stopped`, or `completed`.

Pause, seek, restart, media replacement, and loss or reacquisition of authoritative FPP timing are discontinuities. They trigger an explicit realignment or visible failure; they are not ordinary drift. Natural completion remains distinct from commanded stop because Track F uses observed completion as a transition anchor.

## Assets and readiness

Track E supplies the exact current ShowMesh asset id, content hash, size, and node-local runtime filename. Track C probes the local file and reports duration plus detectable container, codec, channel count, and sample rate before admitting it to a session. `mediaType: audio` and a familiar extension are not decoder evidence. An ordered background playlist pins its own revision and every item's exact asset identity; readiness covers the whole playlist rather than only its first item.

Audio authority requires fresh evidence that:

- the exact assigned content hash exists on the node and still matches;
- the media probe succeeded and any required duration is known;
- the selected program and LTC routes are present and share the required clock domain;
- physical output separation was commissioned for the installed interface;
- every operation required by the active show and resting configuration is supported;
- the active engine and output observations are fresh.

If a file disappears, changes, or fails decoding after readiness but before playback, that session fails visibly and the output remains silent. Show start never initiates an asset transfer.

## Consumers this track must satisfy

### Resting Mode and Track F

Track F owns when cues occur. Track C owns the audio effects and observations those cues invoke:

- background sessions with source or playlist, looping, resume/restart, maximum gain, and fade curve/duration;
- ordered playlist advancement, pinned resume bookmarks, and explicit behavior when the next item is missing or changed;
- show-over-background replace or duck behavior;
- announcement sessions over background or show audio, using declared mix, duck, or interrupt policy;
- independently scheduled gain fades and an observable `fade-complete` barrier outcome;
- natural completion, commanded stop, and failure as distinct observations;
- idempotent replay of the same durable cue invocation after a crash;
- current media, active sources, mix/duck state, effective gain, cue result, and recovery guidance for the operator surface.

F1–F3 may use fakes before Track C exists. Track F is not integrated until the same cases pass against the real engine.

### Track D

Track C generates LTC from the same pipeline clock as program audio and keeps start, stop, seek, restart, and discontinuity behavior explicit. RES-001 decides the configured frame rate and records destination-side lock and playhead behavior. Generation is not proof that the cable, input, or Resolume received it.

### Track E

Track C uses Track E's content-addressed store and ahead-of-show node sync. It does not invent another source store or fetch during playback. Audio-specific probing is Track C's responsibility; generic storage remains Track E's.

### Observability and operator recovery

Track C publishes the complete telemetry set in `AUDIO-ENGINE.md` §15 through the desired-versus-observed model. Stale evidence reads `unknown`. A `playing` session proves engine state only, not that the FM audience, physical speakers, timecode receiver, or remote listener heard anything.

ADR-019 requires a documented manual recovery path because automatic fallback was rejected. The operator identifies the failed device or pipeline and keeps it silent; revalidates route, gain, channel separation, clock relationship, every exact asset and probe in the pinned source/playlist, and the current playlist item/bookmark; then restarts the output or deliberately reassigns to an eligible node satisfying the same checks. Deliberately returning a deployment to FPP audio is an installation change with its own route and gain checks, never an automatic fallback command.

## Deliverables and order

### Day-0 core

**C0. RES-007 viability and commissioning bench, in two non-blocking stages.**

- **C0a, available immediately:** build the minimum N-output GStreamer pipeline on a representative Linux host using virtual/null/loopback audio devices and any available Linux-supported interface. Exercise pipeline construction from runtime-discovered capabilities, channel mapping, program/LTC separation in the graph, decode, sessions, gain changes, ducking, playlists, gap/loop behavior, telemetry, and failure injection that does not claim physical-device behavior. C0a, C1–C4, C6, and the non-hardware portions of C7 do not wait for the eventual show interface.
- **C0b, per-device commissioning when hardware is available:** on the actual candidate interface, send tone to the intended LTC output and prove it does not appear on program, then record program audio, LTC, and a visual reference together. Measure physical channel independence, click-free output, full-show program-to-LTC alignment, drift, hot-plug, sample-rate changes, and the installed-path alignment acquisition method. Failure blocks that interface's placement and Day-0 readiness; it does not invalidate the device-agnostic engine or stop work on the track.

**C1. Audio capability and placement.** Advertise engine/output capabilities, reported output count, route identity, and clock domain. Refuse a placement that lacks a discrete same-clock LTC output or required session operation. Capability evidence is discovered, not hand-entered.

**C2. Asset resolution and probe.** Resolve exact Track E content to a safe local path, recheck hash/size at the required boundary, probe media metadata, and expose readiness faults. Exercise missing, corrupt, changed, unsupported, and duration-unknown files.

**C3. Authoritative playback sessions.** Implement state, revisions, idempotent commands, media changes, start/stop, pause/resume, seek/restart, natural completion, loop/resume policy, drift observation, explicit boundary correction, and timing-authority loss/reacquisition. A playlist pins an ordered revision, current item identity/index/position, and repeat mode; advancement occurs exactly once, and a stale bookmark or missing/corrupt next item fails visibly. Persist the completion/advance boundary with a stable item invocation identity so a crash cannot replay the prior item or skip/double-start the next one.

**C4. Program mix and Resting Mode primitives.** Implement background, show, announcement, and manual sources; multi-item background playlists; gain ceilings; fades with observable completion; duck/mix/interrupt policy; and clean restoration of the prior source after an announcement. Default playlist item changes promise neither overlap nor gapless playback; requested gapless or crossfade behavior requires matching output capability and measurement.

**C5. LTC and physical outputs.** Generate LTC on the discrete same-clock output, route stereo program to local/FM outputs, and expose LTC state. Exercise start, stop, seek, restart, and signal loss against Track D's destination-side bench.

**C6. Telemetry, readiness, and command outcomes.** Publish all session/playlist, asset, device, route, mix, gain, timing, drift, LTC, freshness, and operation evidence required by the Audio Engine, Resting Mode, Observability, and Operator UI specifications. Program-to-LTC evidence includes current measured offset, method/provenance, measured-at time, freshness, threshold, and verdict; the pre-show readiness run obtains a fresh installed-path measurement rather than reusing commissioning history.

**C7. Failure containment and manual recovery.** Exercise device unplug/replug, pipeline crash/freeze, decode failure, asset loss, sample-rate or route change, timing-authority loss, coordinator loss, broker loss, process restart, and both crash sides of natural item completion/playlist advancement. Failed output stays silent; unaffected outputs continue; already-playing local media survives loss of the coordinator and broker; later unavailable transitions fail visibly. Publish and execute the manual restoration procedure.

### After the core session engine; not a Day-0 gate

**C8. Generic synchronized-remote-output mock.** Implement the `AUDIO-ENGINE.md` §8 contract against a deterministic fake destination. It accepts advance provisioning keyed by destination-configuration revision plus ShowMesh asset id/content hash and consumes the engine's real media/playlist selection, item advancement, start, stop, position, source-role, loop, item-transition, and gain-intent events.

The fake provides configurable capability profiles and evidence quality. At minimum it exercises:

- full mix reproduction versus one-session-only output;
- playlists, sequential/gapless/crossfade item transitions, mixing, announcements, ducking, looping, gain fades, seeking, and position reporting as independently supported, unsupported, or unknown;
- provisioning before playback, duplicate provisioning, same filename with a replaced hash, failed transfer, acknowledgement without readiness, and no status interface;
- optional-output warnings versus a required-output policy whose current evidence covers every exact required playlist and announcement hash; prove that one manually verified item plus one unverified item remains unsatisfied;
- delayed, duplicate, and out-of-order commands without a stale command overwriting newer desired state.

The FPP-recognized audio formats form the initial **L0 test corpus** for adapter work. Passing the mock proves only ShowMesh's boundary. It proves no real service accepts those formats, uploads a file, completes processing, stays synchronized, or plays on a phone.

## Acceptance criteria

Day-0 is not accepted until all of the following are observed on the assembled local path:

1. The installed interface carries stereo program and discrete LTC with no LTC on the program pair, proven by measurement.
2. A node or route that cannot satisfy the same-clock discrete-output constraint is refused before playback.
3. Exact node-local assets start at the requested authoritative position; missing, changed, corrupt, and undecodable content is refused explicitly.
4. A real show media change, start, pause/resume where observable, seek/restart, natural completion, and stop produce the documented session transitions and fresh position/drift evidence.
5. Background audio loops, resumes or restarts as configured, never exceeds its maximum gain, fades on independently timed cues, and reports fade completion.
6. A real multi-item background playlist advances each item once, repeats at the configured scope, resumes from the pinned item/position, completes correctly, measures its inter-item gap, and fails visibly when the next item is missing or corrupt. Required gapless or crossfade configuration is refused when unsupported.
7. Show audio replaces or ducks background as configured; an announcement over background and over show audio applies its declared policy and restores the prior mix once.
8. Replaying the same cue invocation after a crash does not duplicate the audible effect; a stale desired revision cannot reverse a newer state.
9. Crashes immediately before and after persisted natural item completion recover to one unambiguous current item without replaying the completed item or skipping/double-starting the next one.
10. Program-to-LTC alignment and drift are measured over a 30–60 minute show with host, interface, sample rate, audio stack, and results recorded in RES-007.
11. A pre-show readiness run measures the installed program-to-LTC offset freshly, records method/provenance and threshold, and returns `within_tolerance`, `out_of_tolerance`, or `unknown` without treating stale commissioning evidence as current.
12. Pulling the interface and the other C7 faults produce the specified silent/degraded behavior, critical evidence, and no automatic FPP-audio fallback.
13. The documented manual recovery procedure is executed before sound resumes, including route, gain, channel separation, clock relationship, every pinned asset/probe, and current playlist item/bookmark validation.
14. Coordinator and broker loss do not stop media already playing from local storage; transitions needing unavailable authority fail visibly.
15. Track F's background, announcement, fade, barrier, and recovery cases pass against the real engine rather than only its fakes.

C8 is accepted separately when its mock trace proves advance provisioning and every capability/evidence profile above without making a real third-party integration or Day-0 claim. C8 is built (`cb8a45a`); see "C8, complete 2026-08-18" above.

## Decisions still open

- PipeWire or raw ALSA, decided by RES-007 evidence rather than preference.
- The tolerable drift threshold and whether discrete correction is audibly acceptable.
- LTC frame-rate configuration is ruled (closed vocabulary 24/25/29.97/30, non-drop at every rate — Linear SM-69/SM-83, C5); Resolume's drop-frame expectation at 29.97 itself remains unresearched (RES-001 §9).
- The installed-path program-to-LTC measurement mechanism. C0 must select and bench a repeatable acquisition method that measures the physical outputs and records provenance; until then pre-show alignment is `unknown`, never inferred from shared-clock configuration alone.
- Per-show announcement policy: mix, duck, interrupt, or unsupported for each output.
- Real third-party upload, acknowledgement, processing, playback, format, authentication, retention, privacy, and timing behavior in RES-016.

**Bound by:** ADR-007, ADR-008, ADR-011, ADR-017, ADR-018, ADR-019, ADR-028, `AUDIO-ENGINE.md`, and the command envelope in ARCHITECTURE §8.1.

**Out of Day-0 scope:** a real synchronized third-party destination, Dante as a required transport, real-time PCM streaming between ShowMesh nodes, automatic or sample-transparent failover, multi-zone audio, dynamic clock-rate correction, and automatic fallback to FPP audio.
