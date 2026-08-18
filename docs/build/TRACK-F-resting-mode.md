# Track F: Resting mode and night-session control

[Build plan](BUILD-PLAN.md) · [Resting Mode specification](../architecture/RESTING-MODE.md) · [ADR-038](../decisions/ADR-038-fpp-authorizes-night-sessions.md) · [Track C](TRACK-C-audio-node.md) · [Track D](TRACK-D-resolume.md) · [Track E](TRACK-E-show-authoring-and-assets.md)

Status: not started. Specified 2026-08-16 from the owner's reference-show workflow; optional site-control/interlock posture clarified 2026-08-17.

## Goal

Replace the reference installation's brittle Background Music plugin loop with a persisted, observable ShowMesh night-session controller. FPP schedules the operating-day intents; the deployed resting FSEQ supplies inter-show duration; ShowMesh coordinates relative audio, lighting, projection, playlist, and announcement transitions, plus optional configured site-power actions.

## Why this is a dedicated controller

The current macro executor is intentionally finite. It has no wait-for-observation step, delay, relative cue, parallel group, condition, or loop; an interrupted macro is never resumed. Extending one macro run across an entire evening would overturn [ADR-031](../decisions/ADR-031-macro-execution-model.md) and make an ordinary transition primitive responsible for durable orchestration.

The night controller instead consumes finite logical actions and FPP observations. It persists only its closed lifecycle, never a general workflow graph and never calendar rules.

## Dependencies and parallel work

- FPP playlist primitives and observations exist, including start, immediate/graceful stop, playlist identity, item position, remaining time, and repeat state.
- FPP playlist definitions remain FPP-owned. The operator supplies a one-item resting playlist; this track validates its expected FSEQ and uses the shipped `startPlaylist` primitive rather than assuming a sequence-start primitive exists.
- Track C must supply background sessions, announcements, loop/resume policy, gain ceilings, fades with observable completion, duck/mix/interrupt policy, audio metadata probing, node-local playback, and command outcomes before the integrated audio path is complete.
- Track D supplies confirmed resting visual and blackout operations. Track E's logical-action wiring must make those adapter operations invocable without protocol paths in the session definition.
- Track E supplies exact asset identity, per-target FSEQ variants, and node-local audio assets. F0 proves FSEQ duration; Track C probes audio duration and decoder metadata rather than treating `mediaType: audio` as evidence.
- Optional power integration may use the existing external MQTT action and response contract. Neither that integration nor a full Home Assistant provider is a prerequisite for the night loop.

These dependencies do not prevent F1–F3 from being built against fakes and recorded observations. They prevent an integrated acceptance claim.

## Deliverables

### F0. Evidence capture and schema proof

- Parse duration from representative deployed resting FSEQ variants.
- Capture the supported read path for an idle FPP playlist definition and prove that ShowMesh can verify its ordered entries and media association before playback. If the FPP API cannot supply it, specify and bench an explicit plugin/config-import path; readiness may not claim playlist contents from running-state observations alone.
- Capture the FPP plugin command path's invocation identity, retry, replay, and delivery-delay behavior. State/preparation guards are sufficient only if old command sequences cannot be durably replayed; if they can, the lifecycle command contract must add and bench an FPP-supplied generation token before F2.
- Measure which FPP observation gives the most reliable start, position, pause, restart, item-end, and playlist-end evidence.
- Measure the cadence required to arm local deadlines within transition tolerance; do not use the ordinary 15-second collector cadence as a cue clock by assumption.
- Prove the same-filename, target-specific FSEQ resolves to the correct duration source.
- Record FPP repeat and one-shot behavior used by inter-show and end-of-night resting.
- Capture the scheduled-brightness command and its observable state, then prove or reject a control path that preserves the FPP ceiling while applying an independent ShowMesh transition multiplier. If FPP exposes only destructive absolute brightness, record that limitation and select a different provider or authored-FSEQ fade before F4.

### F1. Versioned configuration and validation

Add the Night Session configuration object through the public API, `showmeshctl`, export/import, and revision model before adding a UI. It contains FPP resting/show playlist references, asset/action references, relative cue timing, and a background-audio source. That source is either one logical Track E audio slot or an embedded ordered list of logical audio slots plus repeat, resume/restart, and sequential/gapless/crossfade item-transition policy. Each slot identifies the current show/sequence/target audio asset rather than a filename. The Night Session configuration revision is the playlist revision; session activation pins it and resolves every ordered slot to an exact asset id and content hash. No separate audio-playlist configuration kind or undocumented authoring path is introduced.

Validation rejects calendar fields, a manual duplicate rest duration, a resting playlist that is not exactly the expected one-item FSEQ playlist, an empty or duplicate background-audio item list, missing asset/action references, impossible offsets, configured unsafe power grouping, assets without usable duration metadata, and a required item transition unsupported by any selected output. Power, climate, and interlock blocks are optional; their absence is valid. When present, validation enforces unique rule names, the closed lifecycle-phase and posture/unavailable enums, exactly one guarded phase per rule, power domain and provenance metadata, and an explicit complete removal policy for every presentation power-off binding. Prerequisite lists and delays are bounded and cannot recurse into their own power-off binding.

Configuration pins revisions for an active session. Editing a show at 8 PM cannot silently change the controller running that night.

### F2. Persisted lifecycle controller

Implement the closed state machine and command vocabulary from `RESTING-MODE.md`. `prepare-site` opens the operating-day preparation epoch; readiness and pre-show attach to it; `start-night` consumes it only with fresh same-epoch evidence. Persist preparation epoch, readiness identity/freshness, session identity, state, final intent, admission-closed flag, cycle, content anchor, armed-show identity, `showCommitted`, durable cue outbox records, stable cue invocation identities, cue outcomes, and pending fade/shutdown. Shutdown intents are monotonic and cancel any uncommitted next-show boundary. Submission is asynchronous; the API and change stream expose state rather than holding a request open.

This controller must not import or reuse the desired-state reconciler as a loop, and it must not resume a generic macro after restart.

### F3. FPP timeline and playlist integration

- Start the configured FPP-owned one-item resting playlist and confirm the exact FSEQ item.
- Derive its boundary from asset duration plus observed FPP position.
- Arm monotonic relative cue deadlines and invalidate them on contradictory evidence.
- Launch the configured show playlist only after transition barriers.
- Detect actual playlist completion rather than graceful-stop acceptance.
- Start repeating end-of-night resting after the final show.
- Preserve FPP busy-start protections and never replace an unrelated running playlist silently.

### F4. Transition action runner

Run independently offset lighting, projection, audio, announcement, and other-media cues through named logical actions. Support parallel dispatch where cues share an offset and barriers where show launch requires their outcomes. Record completion and confirmation separately.

Persist `showCommitted` and the first pending cue outbox record atomically before dispatch. Every cue pins its action revision and receives a stable session/cycle/cue invocation identity. On recovery, observe first; retry only an idempotent action with the same end-to-end identity; otherwise mark it ambiguous and stop before the show-launch barrier. Reject a non-idempotent, unconfirmable action as the first outward-facing cue. Exercise both crash sides of the transaction/send boundary rather than relying on timing luck.

Implement the brightness seam selected by F0: a provider must observe the FPP-scheduled ceiling and apply transition gain independently. If the bench proves no such FPP seam exists, the accepted implementation is a different logical lighting provider or an authored-FSEQ fade with the limitation surfaced; an absolute ShowMesh write that can restore an old ceiling is forbidden.

This is a purpose-built relative cue runner inside the night controller, not a general scheduler or a replacement macro language.

### F5. Audio integration

Create and control the background and announcement sessions defined by Track C. Enforce maximum resting gain, fade curves with observable completion, exact local asset readiness, loop/resume policy, and the configured duck/mix/interrupt policy. Carry the night controller's stable cue invocation identity and desired revision through the audio command so recovery cannot duplicate an effect or let stale work reverse a newer state.

Validate every output against the formats and reproduction capabilities it honestly declares without narrowing the generic asset store. This includes playlist selection/advancement and the requested sequential, gapless, or crossfade item transition. For an optional synchronized third-party output, missing provisioning/readiness evidence warns and does not block local/FM audio. If configuration marks it required and no status API exists, readiness may accept current attributed operator attestations pinned to the immutable destination-configuration revision or fingerprint and **every** exact audio/announcement content hash required by the pinned Night Session revision. One verified playlist item is insufficient; an attempted or acknowledged upload alone is not `ready`.

### F6. Optional power, thermal, and interlock integration

Add the optional rule mechanism with `observe`, `block`, and `disabled` postures, the required unavailable-source behavior for blocking rules, and phase-filtered evaluation. A rule may withhold only the lifecycle phase it declares; for example, a cooldown rule remains visible but cannot gate `start-night`.

Bind any configured presentation power and Home Assistant thermal-profile requests through logical actions. Every power binding declares `presentation`, `environmental`, `mixed`, or `unknown` domain and whether that classification is provider-supplied or operator-declared. Generic MQTT and Home Assistant actions are operator-declared because ShowMesh cannot inspect their physical targets. `power-down-presentation` rejects every domain except `presentation`.

Every presentation power-off binding explicitly selects `immediate` with an operator safety attestation, or `after-actions` with ordered prerequisites and confirmations. There is no default, and partial configurations are invalid. Gate automated projector strike on fresh safe-temperature evidence only where a blocking rule requests it, supervise shutdown/cooldown only where those steps are selected by the removal policy, and never remove enclosure heating, thermostat, sensors, or exhaust/circulation control with presentation power.

If a deployment configures `force-power-off`, ship it only as an explicit operator action with separate authorization and audit presentation. F6 is not a prerequisite for installations that omit site control.

### F7. Operator surfaces

Add CLI coverage first, then UI configuration and operation. Show lifecycle state, final-cycle status, content-derived boundary, next cue, pending intents, per-cue evidence, audio gain, brightness ceiling/multiplier, configured interlock/site-control state, and recovery guidance. Optional sections render as `not_configured`, not warnings. The UI contains no orchestration logic.

### F8. Integrated and failure verification

Run every acceptance scenario in `RESTING-MODE.md` first with no site-control/interlock configuration, then against the reference installation's configured MQTT/Home Assistant path. Exercise every posture, both unavailable-source choices, phase isolation, override authorization, domain/provenance rejection, both removal policies, partial-configuration rejection, overnight repetition, and failure injection. Update RES-007 and [RES-016](../research/RES-016-third-party-synchronized-audio-output.md) only to the evidence level actually reached; a mock destination changes no claim about a real service.

## Safety and failure direction

- A running show continues through coordinator or broker loss.
- An ambiguous restart never launches a show by guess.
- Missing evidence is `unknown`, not success and not automatically failure.
- A configured blocking interlock with an unsafe outcome blocks only its declared phase; observe-only, disabled, absent, and other-phase rules do not.
- Configured ordinary fade and power-down defer behind an unexpectedly live show.
- Only an explicitly configured and invoked emergency/force operation may interrupt playback or remove power immediately.
- Where power control is configured, loss of ShowMesh may leave presentation equipment on longer; it must not make an unsafe hard-off more likely.

## Acceptance criteria

- The reference operating day is driven entirely by FPP calendar entries and ShowMesh lifecycle intents; no ShowMesh object contains a wall-clock schedule.
- Changing the resting FSEQ from five to seven minutes changes the next transition without changing a duration field.
- Independent lighting, projection, and audio fade offsets produce the configured theater-style blackout sequence.
- A final-show request in either live or resting state results in exactly one final complete show and then repeating end-of-night resting.
- A fade, or a configured power-down, arriving without, before, or after the final-show request closes admission monotonically and never permits an additional show.
- Readiness from a prior preparation epoch, and delayed pre-show/start commands without a new valid epoch, cannot start a night.
- Christmas midnight and Halloween 1:00 AM fade schedules use the same ShowMesh session definition.
- FPP brightness ceilings survive transition fade-down/fade-up through a bench-proven independent transition-gain seam; unsupported configurations are rejected rather than approximated.
- Restart and loss cases preserve live playback and never double-start a playlist.
- Crashes before and after the first outward cue dispatch resolve from the durable outbox without duplicate non-idempotent effects or a falsely satisfied launch barrier.
- A configuration with no power, climate, or interlocks completes the full night loop without degraded status.
- Observe rules never withhold action, disabled rules never evaluate, blocking rules honor their explicit unavailable-source and override policies, and a shutdown-only rule cannot gate night startup.
- Presentation power-off rejects environmental, mixed, unknown, and unclassified targets; operator-declared MQTT provenance is exposed honestly rather than presented as verified wiring.
- Immediate removal requires an explicit all-target safety attestation; after-actions removal completes its ordered prerequisites; missing or partial policy configuration is rejected.
- Where presentation shutdown is configured, it leaves environmental heating/cooling control active.
- CLI operation remains possible with the UI container absent.
- The entire cycle is observed end to end; unit tests alone cannot complete this track.

**Bound by:** ADR-001 as narrowed by ADR-038, ADR-003, ADR-004, ADR-011, ADR-016, ADR-017, ADR-019, ADR-020, ADR-024, ADR-028, ADR-029, ADR-031, ADR-033, and `RESTING-MODE.md`.

**Out of scope:** calendar scheduling in ShowMesh, timeline or FSEQ authoring, Home Assistant thermostat implementation, certified emergency-alert behavior, and automatic interruption of a live show by an ordinary shutdown command.
