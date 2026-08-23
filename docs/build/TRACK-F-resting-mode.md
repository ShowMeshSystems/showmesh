# Track F: Resting mode and night-session control

[Build plan](BUILD-PLAN.md) · [Resting Mode specification](../architecture/RESTING-MODE.md) · [ADR-038](../decisions/ADR-038-fpp-authorizes-night-sessions.md) · [Track C](TRACK-C-audio-node.md) · [Track D](TRACK-D-resolume.md) · [Track E](TRACK-E-show-authoring-and-assets.md)

Status: F0 through F7 are merged to `main`. F0 through F4 and F7's API and CLI half landed as PR #23 (`8367436`); F7's UI half as PR #45 (`1e839f3`); F5, audio integration, as PR #51 (`2f35412`); F6, optional site control, as PR #53 (`bd40073`). `main` at `bd40073` was verified in a clean worktree: `make check` exit 0 and `make test-integration` exit 0 in 141.851s. **`pkg/audio`, which F5 consumes, is present on `main`** at commit `12ce0d8` via merge `6b0eb38` (2026-08-19); this document previously recorded it as absent, which was checked before that merge landed and is corrected here. F8 cannot be attempted on this machine at all, because every one of its scenarios requires a real FPP and the deployed installation. Specified 2026-08-16 from the owner's reference-show workflow; optional site-control/interlock posture clarified 2026-08-17; FPP brightness provider selected in RES-018 on 2026-08-18 but not implemented or real-host verified. See the 2026-08-18 dated entry in [BUILD-LOG.md](BUILD-LOG.md) for F0 through F4 gate evidence and review findings, and the 2026-08-22 dated entry for F5, F6, and F7's UI half. Nothing here has run against a real FPP, real audio hardware, a real site-control target, a browser, or the deployed fleet.

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

### F0. Evidence capture and schema proof — built (`369ad34`, 2026-08-18)

Captured against a dedicated bench `fppd` 9.5.3, never the shared bench container and never the deployed fleet. Found that stock FPP has no brightness command at all (owner decision RES-018/SM-49); see the 2026-08-18 BUILD-LOG entry for the full capture summary and the four other measurements that shaped F3.

- Parse duration from representative deployed resting FSEQ variants.
- Capture the supported read path for an idle FPP playlist definition and prove that ShowMesh can verify its ordered entries and media association before playback. If the FPP API cannot supply it, specify and bench an explicit plugin/config-import path; readiness may not claim playlist contents from running-state observations alone.
- Capture the FPP plugin command path's invocation identity, retry, replay, and delivery-delay behavior. State/preparation guards are sufficient only if old command sequences cannot be durably replayed; if they can, the lifecycle command contract must add and bench an FPP-supplied generation token before F2.
- Measure which FPP observation gives the most reliable start, position, pause, restart, item-end, and playlist-end evidence.
- Measure the cadence required to arm local deadlines within transition tolerance; do not use the ordinary 15-second collector cadence as a cue clock by assumption.
- Prove the same-filename, target-specific FSEQ resolves to the correct duration source.
- Record FPP repeat and one-shot behavior used by inter-show and end-of-night resting.
- Capture the target percentages, fade durations, and participating hosts for the confirmed 10 PM/11 PM `fpp-brightness` scheduler entries. Prove that the replacement `ShowMesh: Set Brightness Ceiling(targetPercent, fadeSeconds)` FPP Action is schedulable, interpolates the observable ceiling without a jump, and changes only ceiling, while the coordinator-facing path changes only transition gain, before F4 can accept the provider.

### F1. Versioned configuration and validation — built (`e48660f`, 2026-08-18)

Built against fakes; `make check` and `make test-integration` (287.957s, zero failures) green on this tree. No integrated acceptance is claimed and nothing here has run against a real FPP. Nine review findings, two blocking, are recorded in the 2026-08-18 BUILD-LOG entry.

Add the Night Session configuration object through the public API, `showmeshctl`, export/import, and revision model before adding a UI. It contains FPP resting/show Playlist references, asset/action references, relative cue timing, and a reference to one same-show Track H `show.playlist` for background audio. The Night Session revision and referenced Playlist revision are pinned together at activation; Track H resolves every Cue output to exact asset ids and content hashes. Night Session does not embed a competing ordered audio list or create another Playlist authoring path.

Validation rejects calendar fields, a manual duplicate rest duration, a resting Playlist that is not exactly the expected one-item FSEQ Playlist, a missing or cross-show background Playlist, a background Playlist whose runner is not `showmesh-audio`, missing asset/action references, impossible offsets, configured unsafe power grouping, assets without usable duration metadata, and a required item transition unsupported by any selected output. Power, climate, and interlock blocks are optional; their absence is valid. When present, validation enforces unique rule names, the closed lifecycle-phase and posture/unavailable enums, exactly one guarded phase per rule, power domain and provenance metadata, and an explicit complete removal policy for every presentation power-off binding. Prerequisite lists and delays are bounded and cannot recurse into their own power-off binding.

Configuration pins revisions for an active session. Editing a show at 8 PM cannot silently change the controller running that night.

### F2. Persisted lifecycle controller — built (`49189d7`, 2026-08-18)

Built against fakes and recorded observations; `make check` and `make test-integration` (292.492s, 74 passed, zero failures) green on this tree. No integrated acceptance is claimed and nothing here has run against a real FPP. Thirteen review findings are recorded in the 2026-08-18 BUILD-LOG entry, including a degraded session with no recovery path (fixed) and an untransacted read-decide-write (fixed).

Implement the closed state machine and command vocabulary from `RESTING-MODE.md`. `prepare-site` opens the operating-day preparation epoch; readiness and pre-show attach to it; `start-night` consumes it only with fresh same-epoch evidence. Persist preparation epoch, readiness identity/freshness, session identity, state, final intent, admission-closed flag, cycle, content anchor, armed-show identity, `showCommitted`, durable cue outbox records, stable cue invocation identities, cue outcomes, and pending fade/shutdown. Shutdown intents are monotonic and cancel any uncommitted next-show boundary. Submission is asynchronous; the API and change stream expose state rather than holding a request open.

This controller must not import or reuse the desired-state reconciler as a loop, and it must not resume a generic macro after restart.

### F3. FPP timeline and playlist integration — built (`63f4770`, 2026-08-18)

Built against fakes, a throwaway bench `fppd`, and F0's recorded observations; `make check`, `make test-integration` (298.666s, zero failures), and `make test-integration-fpp` (176.682s, 29 cases) green on this tree. No integrated acceptance is claimed and nothing here has run against the deployed fleet. Ten review findings across two rounds are recorded in the 2026-08-18 BUILD-LOG entry, including a silently re-arming invalidated boundary and a show-launch replacement guard reachable without positive identification.

- Start the configured FPP-owned one-item resting playlist and confirm the exact FSEQ item.
- Derive its boundary from asset duration plus observed FPP position.
- Arm monotonic relative cue deadlines and invalidate them on contradictory evidence.
- Launch the configured show playlist only after transition barriers.
- Detect actual playlist completion rather than graceful-stop acceptance.
- Start repeating end-of-night resting after the final show.
- Preserve FPP busy-start protections and never replace an unrelated running playlist silently.

### F4. Transition action runner — built and gated (`506c6c0`, 2026-08-18)

Two review rounds found sixteen defects after the seam's own gates were green, all fixed; the 2026-08-18 BUILD-LOG entry carries them. `make check` exit 0 and `make test-integration` `ok ... 303.774s` with zero failures, run by the orchestrating session once host load was quiet. **The cue outbox has no operator surface**, which is F7's work and is tracked as Linear SM-98; the ambiguous outcome names the recovery that exists today rather than promising one that does not.

Run independently offset lighting, projection, audio, announcement, and other-media cues through named logical actions. Support parallel dispatch where cues share an offset and barriers where show launch requires their outcomes. Record completion and confirmation separately.

Persist `showCommitted` and the first pending cue outbox record atomically before dispatch. Every cue pins its action revision and receives a stable session/cycle/cue invocation identity. On recovery, observe first; retry only an idempotent action with the same end-to-end identity; otherwise mark it ambiguous and stop before the show-launch barrier. Reject a non-idempotent, unconfirmable action as the first outward-facing cue. Exercise both crash sides of the transaction/send boundary rather than relying on timing luck.

Implement the RES-018 brightness contract through the ShowMesh FPP component: observe the FPP-scheduled ceiling and apply transition gain independently. The coordinator-facing transition-gain write is frozen in [FPP plugin coordinator contracts](FPP-PLUGIN-COORDINATOR-CONTRACTS.md) section 2; neither side serves it yet, so the readiness refusal below stays in place. The shared engine will use isolated FPP 9 and FPP 10 adapters and remain outside the Go coordinator-client/SDK seam. Until installation and the decisive mid-fade ceiling-change case are proven, readiness rejects this provider. An absolute ShowMesh write that can restore an old ceiling remains forbidden.

This is a purpose-built relative cue runner inside the night controller, not a general scheduler or a replacement macro language.

### F5. Audio integration: merged to `main` (PR #51, `2f35412`)

Built against Track C's `pkg/audio`, present on `main` since `6b0eb38` (2026-08-19). Adds `audio` as a fourth `show.action` target integration, applies the whole pinned `PlaylistRef` and lets the engine own item advancement, repeat, and resume per AUDIO-ENGINE section 3, and gives announcement ducking one owner, the node.

Two independent review passes, run after the seam's own `make check` and `make test-integration` were already green, found and fixed: a wrong wire field name that silenced resting music after exactly one track; a compounded coordinator/node duck whose restore replayed a superseded value and stranded the bed at a quarter gain for the rest of the night; and an announcement path that could never duck, because ducking policy was declared on the one action shape that cannot resolve it. See the 2026-08-22 entry in [BUILD-LOG.md](BUILD-LOG.md) for the full list.

**Unverified:** no audio has been heard on this branch. `FakeEngine` reports itself unavailable by design. No hardware, node, deployment, or browser evidence exists for any of this.

Create and control the background and announcement sessions defined by Track C. Enforce maximum resting gain, fade curves with observable completion, exact local asset readiness, loop/resume policy, and the configured duck/mix/interrupt policy. Carry the night controller's stable cue invocation identity and desired revision through the audio command so recovery cannot duplicate an effect or let stale work reverse a newer state.

Validate every output against the formats and reproduction capabilities it honestly declares without narrowing the generic asset store. This includes playlist selection/advancement and the requested sequential, gapless, or crossfade item transition. For an optional synchronized third-party output, missing provisioning/readiness evidence warns and does not block local/FM audio. If configuration marks it required and no status API exists, readiness may accept current attributed operator attestations pinned to the immutable destination-configuration revision or fingerprint and **every** exact audio/announcement content hash required by the pinned Night Session revision. One verified playlist item is insufficient; an attempted or acknowledged upload alone is not `ready`.

### F6. Optional power, thermal, and interlock integration: merged to `main` (PR #53, `bd40073`), optional

Implements RESTING-MODE section 10.1's closed behavior matrix over `observe`, `block`, and `disabled` postures, phase-filtered evaluation, power-domain/provenance declarations, the two removal policies, and the `night:override` scope. The finding that mattered: the stored readiness gate discarded evidence older than 30 minutes, which could let `end-session` reach `stopped` without ever issuing the FPP stop, leaving the show running with ShowMesh no longer tracking it; fixed by having shutdown-phase rules evaluate live evidence rather than the stored result, and by refusing at write time a blocking shutdown-phase rule with `overridePolicy: none`. See the 2026-08-22 entry in [BUILD-LOG.md](BUILD-LOG.md) for the full list.

**Unverified:** no real projector, relay, thermostat, Home Assistant instance, or MQTT broker was exercised. The removal-policy runtime described below is not built.

F1 rejects the `siteControl` and `interlocks` configuration blocks with a problem naming this deliverable, rather than accepting configuration nothing enforces. A deployment that omits site control, which is what the reference installation does, runs the full night loop without it.

Add the optional rule mechanism with `observe`, `block`, and `disabled` postures, the required unavailable-source behavior for blocking rules, and phase-filtered evaluation. A rule may withhold only the lifecycle phase it declares; for example, a cooldown rule remains visible but cannot gate `start-night`.

Bind any configured presentation power and Home Assistant thermal-profile requests through logical actions. Every power binding declares `presentation`, `environmental`, `mixed`, or `unknown` domain and whether that classification is provider-supplied or operator-declared. Generic MQTT and Home Assistant actions are operator-declared because ShowMesh cannot inspect their physical targets. `power-down-presentation` rejects every domain except `presentation`.

Every presentation power-off binding explicitly selects `immediate` with an operator safety attestation, or `after-actions` with ordered prerequisites and confirmations. There is no default, and partial configurations are invalid. Gate automated projector strike on fresh safe-temperature evidence only where a blocking rule requests it, supervise shutdown/cooldown only where those steps are selected by the removal policy, and never remove enclosure heating, thermostat, sensors, or exhaust/circulation control with presentation power.

If a deployment configures `force-power-off`, ship it only as an explicit operator action with separate authorization and audit presentation. F6 is not a prerequisite for installations that omit site control.

### F7. Operator surfaces: merged to `main`, API and CLI half in `50772b3`, UI half in PR #45 (`1e839f3`)

The API and `showmeshctl` now carry lifecycle state, final-cycle status, the content-derived boundary, pending intents, per-cue evidence (state, outcome, reason, pinned action revision, dispatch and resolution times), the reason a transition is held, and recovery guidance naming the recovery that exists. Optional phases render `not_configured`.

**Not yet on any surface**: audio gain (F5), brightness ceiling and multiplier (no provider exists, RES-018), and interlock/site-control state (F6). Those three are absent because the capability behind each is absent, not because the surface was skipped.

**F7's UI half**: the night operating view (the `night.session` list, create, and detail with revision history and per-revision read), the `night.session.active` pointer screen, all eight lifecycle commands behind `night:command` rendered disabled with a stated reason rather than hidden, and the three 409 problem types plus the 503 each distinguishable. It also documents `nightSession.changed` in the `/stream` event table with a `NightSessionChangedEvent` schema and handles the frame in the UI store, which had been dropping it through its default branch, and adds the `fppPlaylistEntry.changed` row the same table was already missing. An independent reviewer found 13 defects plus 2 confirmed suspicions, fixed at head `0658d5e`; two matter beyond style: a stale `GET /night/session` response could overwrite a newer stream frame until the next real transition, fixed by ordering on `updatedAt` rather than the per-connection, non-durable stream `seq`; and a failed reload discarded the loaded state entirely rather than preserving what was already shown, which is ADR-024 constraint 23's failure shape. See the 2026-08-22 entry in [BUILD-LOG.md](BUILD-LOG.md) for the full list.

**Unverified:** proven only under jsdom against a mock HTTP and stream server. No browser run has occurred.

Add CLI coverage first, then UI configuration and operation. Show lifecycle state, final-cycle status, content-derived boundary, next cue, pending intents, per-cue evidence, audio gain, brightness ceiling/multiplier, configured interlock/site-control state, and recovery guidance. Optional sections render as `not_configured`, not warnings. The UI contains no orchestration logic.

### F8. Integrated and failure verification — not started, and not startable on a development machine

Every scenario needs a real FPP and the deployed installation. Tracked as Linear SM-66 (the whole loop end to end), SM-50 (the real deployed resting FSEQ) and SM-51 (real per-installation cue timing).

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
