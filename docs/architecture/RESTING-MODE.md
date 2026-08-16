# Resting Mode and Night Session Specification

[Documentation index](../README.md) · [Architecture specification](ARCHITECTURE.md) · [Audio Engine](AUDIO-ENGINE.md) · [ADR-038](../decisions/ADR-038-fpp-authorizes-night-sessions.md) · [Track F](../build/TRACK-F-resting-mode.md)

Status: Accepted architecture baseline — specified, not implemented or integrated  
Audience: Maintainers, operators, and agents implementing the night-session controller

## 1. Purpose

Resting mode is the coordinated presentation between shows, not a background-audio toggle. It may contain:

- a one-shot xLights/FSEQ lighting sequence;
- background music;
- one or more resting projection layers;
- a countdown until the next show;
- moving-head or other device state;
- announcements;
- an end-of-night variant that repeats without starting another show.

This document defines the operating-day commands, the repeated rest/show loop, transition timing, audio and projection behavior, power and thermal boundaries, persistence, recovery, configuration, and evidence required to implement it.

It does not add a calendar scheduler to ShowMesh. [ADR-038](../decisions/ADR-038-fpp-authorizes-night-sessions.md) is the authority rule.

## 2. Authority and time

Authority is split three ways:

1. **FPP owns calendar time.** Every dated or wall-clock action is an FPP schedule entry.
2. **Authored content owns duration.** The deployed resting FSEQ variant defines the inter-show rest length.
3. **ShowMesh owns relative choreography.** It advances an FPP-authorized session and schedules offsets around observed media boundaries.

ShowMesh configuration may contain durations that are intrinsic to command execution: fade duration, lead time, blackout hold, announcement delay, confirmation deadline, retry backoff, cooldown, and safety timeout. It must not contain dates, weekdays, time zones, cron expressions, or a wall-clock show schedule.

## 3. Lifecycle vocabulary

Night-session state is installation-wide and distinct from [Program/Show Mode](../decisions/ADR-033-show-mode.md). Program/Show Mode changes operational footprint; it is not derived from playback. Night-session state describes the active presentation lifecycle.

```text
inactive/stopped -> preparing -> preshow -> transition-to-show -> live
                                  \-> resting-intershow -/

live -> transition-to-resting -> resting-intershow -> transition-to-show
                              \-> end-of-night-resting -> fading-out -> stopped
```

`blocked` and `degraded` are outcomes/evidence attached to a state, not alternate lifecycle states. `blackout` remains an emergency presentation state and does not mean equipment is powered down.

## 4. FPP-scheduled lifecycle commands

All commands are asynchronous, idempotent by invocation identity where available and by state where not, and visible in the audit/event surfaces.

### 4.1 `prepare-site`

Opens a new persisted preparation epoch and enters `preparing` when the prior session is `inactive` or `stopped`, then requests the configured show-day infrastructure profile through named logical actions. A deployment may use it for presentation power and for requesting a Home Assistant thermal mode. It does not directly cycle a heater, exhaust fan, or thermostat.

Every later preparation command attaches to this epoch. A duplicate within the same preparation or active session is an idempotent no-op. It is rejected during finalization or fade-out. The epoch becomes eligible for readiness and pre-show only after fresh evidence that presentation power-on preparation occurred after the prior stop; a delayed command from the prior operating day cannot reopen a presentation from state alone.

### 4.2 `run-readiness`

Starts the full readiness evaluation for the current preparation epoch: required nodes, exact asset variants, FPP state, projection bindings, audio outputs, controlled devices, environmental conditions, and any installation-specific interlocks. It is rejected when no preparation epoch is open.

The result records the epoch, completion time, and evidence times. `start-night` requires a completed result from the same epoch within a configured maximum age and re-evaluates safety interlocks at execution. Readiness from a prior operating day is never adopted into a new epoch.

### 4.3 `start-preshow`

Enters the configured pre-show presentation. It may start resting projections, background music, countdowns, and the configured resting playlist. Pre-show is a distinct presentation profile but uses the same exit-transition machinery as inter-show resting.

`start-preshow` requires the current preparation epoch and attaches the presentation to it. A duplicate inside the same prepared or active session is an idempotent no-op; it does not manufacture a second epoch. A stale `start-preshow` after shutdown is rejected because no unconsumed preparation epoch exists.

### 4.4 `start-night`

Authorizes the night session and begins the first pre-show/resting-to-show transition. It is accepted only from a prepared epoch with fresh readiness from that same epoch and the expected pre-show/resting presentation observed. The scheduled command begins the transition; the show playlist begins after the configured fades, blackout, and optional announcement. If an operator wants the playlist itself to begin at an exact wall-clock time, the FPP command is scheduled earlier by the known transition duration.

State handling is closed:

| State | `start-night` behavior |
|---|---|
| prepared `preshow` | Consume the epoch and start a new night session. |
| `resting-intershow`, either transition, or `live` | Idempotent no-op returning the active session. |
| `end-of-night-resting` | Reject; finalization is monotonic. |
| `fading-out` or `stopped` | Reject; a new `prepare-site` epoch followed by readiness and pre-show is required. |
| `preparing` or `inactive` | Reject as not ready for night start; operator recovery completes readiness and invokes `start-preshow` first. |

This guard and the `prepare-site` epoch distinguish the next day's intentional start from delayed `start-preshow`/`start-night` commands after shutdown without adding a calendar field to ShowMesh.

### 4.5 `request-final-show`

Closes admission after one final complete show:

| Observed state | Required behavior |
|---|---|
| `live` | Mark the current playlist as final and let it finish. |
| `resting-intershow` | Finish the current resting FSEQ normally; the next show is final. |
| `preshow` | The first show is final. |
| transition to show | The show being entered is final. |
| transition to resting | Enter end-of-night resting when the transition completes. |
| end-of-night or later | Idempotent no-op with evidence. |

The command never starts a second show after the final playlist.

### 4.6 `fade-out-night`

Fades the active non-live presentation to stopped. Receipt closes admission immediately and cancels any armed next-show boundary. In `preshow`, it fades the active pre-show/resting elements without launching a show. During inter-show resting before another show is committed, it does the same. When received during live playback or an already-committed transition into a show, that show becomes final and the fade remains pending until observed completion. A late `request-final-show` cannot reopen admission. A separate explicit emergency action is required to interrupt live playback.

### 4.7 `power-down-presentation`

Requests safe presentation shutdown after ShowMesh has observed that playback stopped and the fade completed. Receipt also closes admission and implies `fade-out-night` if it has not occurred. It performs device-specific shutdown and cooldown before removing presentation power. It never removes environmental power.

## 5. Reference operating day

Times below document the reference installation's shape. They are examples configured in FPP and are not product defaults.

| Example time | FPP or external action | ShowMesh responsibility |
|---|---|---|
| 4:00 PM | `prepare-site` | Open the operating-day preparation epoch; optionally request a show-day thermal profile if the enclosure is not already maintained continuously. |
| 4:10 PM | Presentation power-on intent | Power LEDs, moving heads, render/audio equipment, and safely strike projectors once thermal requirements are met. |
| 4:15 PM | `run-readiness` | Begin verification as devices become observable. |
| 4:30 PM | `start-preshow` | Start projections, background music, countdowns, and configured resting presentation. |
| 5:00 PM | `start-night` | Begin the first transition and then launch the first show playlist. |
| 10:00 PM | Direct FPP brightness command | Observe the new ceiling where available; do not overwrite it. |
| 11:00 PM | Direct FPP brightness command | Observe the lower ceiling where available; do not overwrite it. |
| 11:30 PM | `request-final-show` | Finish the current show or allow the next normally timed show, then close admission. |
| After final show | Event-driven | Enter end-of-night resting; background music and projections return and the resting FSEQ repeats. |
| Christmas 12:00 AM; Halloween 1:00 AM | `fade-out-night` | Fade presentation elements to stopped. |
| Example five minutes later | `power-down-presentation` | Safely shut down and remove presentation power after cooldown. |

The Christmas and Halloween fade times illustrate per-show FPP schedules. ShowMesh stores neither time.

## 6. Inter-show timing authority

### 6.1 Exact asset variant

The configuration references both an FPP-owned resting playlist and a logical resting timeline asset. For v1, the playlist contains exactly one FSEQ item and no FPP audio item; its item must resolve to the exact FSEQ variant deployed to that FPP target, using the asset identity rules in [ADR-028](../decisions/ADR-028-show-asset-store-and-identity.md). Filename alone is insufficient.

FPP owns creation and item ordering for the playlist. Track F references and validates it; it does not create an undocumented FPP object or invent a sequence-start primitive. A future authoring surface may manage the same FPP playlist through a separately specified integration.

ShowMesh extracts and records the duration from that artifact. A hand-entered duplicate `restDuration` is forbidden.

### 6.2 Playback anchor

Sending `start` does not establish time zero. The anchor is post-dispatch evidence from FPP that the intended resting item is playing, including its identity and position. For a confirmed start at position `p` with duration `D`, the expected content boundary is derived from the observation, not from request receipt.

ShowMesh continually compares the armed boundary with FPP's observed playlist, item index, elapsed time, remaining time, repeat state, pause state, and playback state. A restart, seek, pause, delayed start, or item mismatch moves or invalidates the boundary.

Ordinary low-rate observability polling is not a cue clock. The controller arms a local monotonic deadline from authoritative duration/position evidence and uses later observations to correct or invalidate it. Bench work must establish the event or poll cadence needed to keep errors inside the configured cue tolerance.

### 6.3 One-shot and repeat modes

- `resting-intershow` starts the configured one-item resting playlist with repeat disabled. Its FSEQ end drives the next show transition.
- `end-of-night-resting` starts the same or separately configured one-item resting playlist with repeat enabled. It has no show-transition deadline.

FPP and ShowMesh must never independently repeat the same inter-show item.

## 7. Transition choreography

Transitions are timelines of named logical actions, not raw MQTT topics, Resolume paths, or FPP protocol commands. Each cue declares its offset, duration where applicable, confirmation contract, failure policy, and fallback class.

### 7.1 Resting to show

The content boundary `E` is the end of the resting FSEQ. Cues may begin before or after it:

```text
E - lighting lead       begin lighting fade
E - projection lead     begin projection fade
E - audio lead          begin audio fade
E                       resting FSEQ ends; blackout barrier begins
E + announcement offset optional announcement
E + blackout duration   launch show playlist
```

This supports theater-style staging where lights and projections reach black first, music continues briefly in darkness, and audio then fades before an announcement or show start.

The show playlist starts only after all required pre-show cues have reached their declared barrier outcome. The policy for a failed or unconfirmed non-safety cue is explicit per cue; absence of evidence is never silently treated as success.

### 7.1.1 Show-commit boundary

A future show is **armed** while its boundary is known but no outward-facing enter-show cue has been dispatched. It becomes **committed** in one database transaction that writes `showCommitted=true` and a durable pending outbox record for the first outward-facing enter-show cue, normally the earliest lighting, projection, or audio fade. The outbox record pins the action revision and carries a stable cue invocation identity derived from session, cycle, and cue identity.

If `fade-out-night` wins the ordering before commit, the controller cancels the armed boundary and fades the current resting/preshow presentation without launching a show. If the first cue wins and commits the show, a concurrent or later fade request makes that show final and waits for it to complete. The controller never attempts to reverse a partially visible transition.

Dispatch happens from the durable outbox after commit. Crash recovery follows evidence and idempotency:

- if post-dispatch target evidence proves the cue took effect, record its outcome and do not send it again;
- if no effect is observed and the action supports the same stable idempotency identity end to end, retry that outbox record with the same identity;
- if the action is non-idempotent or structurally unconfirmable, mark the cue ambiguous, do not retry it automatically, and do not cross the show-launch barrier without operator recovery.

Configuration rejects a non-idempotent, unconfirmable action as the first outward-facing cue. A crash before the send therefore cannot leave the controller choosing between an unsafe duplicate and pretending a transition occurred.

### 7.2 Show to resting

The return transition begins only from observed evidence that the show playlist completed. Acceptance of FPP's graceful-stop command or entry into `stopping gracefully` is not completion evidence.

The default order is:

1. Observe the final show playlist end.
2. Hold post-show blackout for the configured duration.
3. Optionally play a thank-you announcement.
4. Start the next one-shot resting playlist, or the repeating end-of-night resting playlist after the final show.
5. Fade up resting lights, projections, audio, and other media on independently configured offsets and durations.
6. Limit background audio to its configured maximum gain.

The ordering of steps 2–5 is configurable through relative cues, but show completion remains the authoritative anchor.

### 7.3 Brightness composition

FPP may change the installation brightness ceiling on its own schedule. ShowMesh transition fades are a multiplier, not a replacement:

```text
effective output = current FPP brightness ceiling * ShowMesh transition gain
```

A fade-up returns to the current ceiling, not a cached earlier ceiling. This requires a lighting action/provider that can both observe the scheduled ceiling and apply a separate transition multiplier. The current shipped FPP collector and command vocabulary do not yet provide that seam; Track F F0 must capture it and F4 must implement it before this behavior can be claimed.

If the ceiling cannot be observed or the control path exposes only one destructive absolute brightness value, the configuration is unsupported: readiness must say so and ShowMesh must not perform a fade that could overwrite the scheduled limit. The fallback is an authored FSEQ fade or a provider that supplies the missing compositional control, not pretending an unknown ceiling was preserved.

## 8. Audio

Resting audio is a ShowMesh `background` playback session. It uses node-local assets, loop behavior, gain, and the Audio Engine's fade machinery. Configuration includes source or playlist, loop policy, fade curve, per-transition offsets, maximum resting gain, and whether a source resumes or restarts where that distinction applies.

Announcements are separate higher-priority sessions. A normal transition announcement is placed after the configured background fade or uses an explicitly configured duck/interrupt policy. Public-safety interruption of all playout is a separate future safety design; this document does not represent ShowMesh as an emergency-alert receiver.

The generic asset store remains codec-agnostic. A PulseMesh output requiring MP3 validates that output-specific constraint without prohibiting WAV, FLAC, or other formats for local outputs. Current PulseMesh evidence and open integration questions are recorded in [RES-016](../research/RES-016-pulsemesh-audio-integration.md).

## 9. Projections and other presentation systems

Resting projections, countdowns, pre-show text, blackout, and resting layers are explicit ShowMesh logical actions. Timecode remains the authority for timeline-driven show content. A transition uses stable named action bindings and the Resolume adapter's confirmation evidence; it never embeds REST paths or object identifiers.

Every additional presentation system joins the same cue model by providing named actions with honest confirmation and fallback metadata.

## 10. Power, enclosure climate, and Home Assistant

### 10.1 Separate power domains

Presentation and environmental power are different groups:

```text
presentation:
  projectors, LED controllers, moving heads, audio/render equipment

environmental:
  enclosure heater, thermostat, temperature sensors,
  exhaust and circulation fans
```

`power-down-presentation` must never remove environmental power.

### 10.2 Home Assistant owns the thermal loop

Home Assistant owns continuous freeze protection, thermostat cycling, exhaust control, and protection against over-cooling. In severe weather it may heat continuously for days before a show. While projectors run, it may need exhaust fans to remove their heat while maintaining a minimum safe enclosure temperature.

ShowMesh does not implement that thermostat. It may invoke a named Home Assistant mode such as `show-preheat`, `projectors-running`, or `standby`, and may observe temperature, mode, and fan/heater state as readiness evidence. Names are installation configuration, not product constants.

Projector strike is gated by the configured safe temperature when current evidence is available. An unsafe or stale measurement blocks automated strike and alerts the operator; it does not cause ShowMesh to take over thermostat control.

### 10.3 Safe shutdown

Normal presentation shutdown:

1. waits for live playback and the presentation fade to finish;
2. requests proper projector shutdown;
3. observes shutdown where supported;
4. waits for configured or observed projector cooldown;
5. removes presentation power through named MQTT/Home Assistant actions;
6. requests the normal environmental standby profile without disabling protection.

A separate manual `force-power-off` action is required for immediate removal. Normal scheduled shutdown never silently escalates to it.

For the initial installation, the existing MQTT/Node-RED response-contract action is sufficient. A full Home Assistant control provider may replace the binding later without changing the lifecycle contract. A much later direct FPP/Home Assistant hard cutoff may exist as a final fallback, scheduled beyond the maximum possible show and cooldown window.

## 11. Persistence, restart, and loss of coordination

The night-session record persists at minimum:

- session identity and pinned configuration revision;
- preparation epoch, readiness result identity, and evidence freshness;
- lifecycle state and state-entered time;
- final-show request and pending fade/shutdown intents;
- current resting asset and FPP playlist/item identity;
- last authoritative playback position and derived boundary;
- cues dispatched and their outcomes;
- durable cue outbox records, stable cue invocation identities, and pinned action revisions;
- current cycle number;
- armed-show identity and persisted `showCommitted` marker;
- last transition evidence and degradation reason.

The controller is event-driven and does not continuously reissue commands to force desired state.

On coordinator restart:

- if FPP is live, observe without disturbing playback, mark the show final if that intent was persisted, and resume only the post-show transition after observed completion;
- if the exact one-shot resting playlist/item is playing and its position is trustworthy, reconstruct the boundary and arm only future cues;
- if end-of-night repeat is observed and the persisted state agrees, restore observation without starting a show;
- if evidence is ambiguous, hold the current presentation, mark the session degraded, and require operator recovery rather than guessing or launching a playlist.

Pending cue outbox records are resolved before ordinary boundary reconstruction: observe first, retry only with the same end-to-end idempotency identity, and otherwise stop at an explicit ambiguous outcome. A pending show-launch barrier never becomes satisfied merely because the coordinator restarted.

Coordinator or broker loss never stops an already-running FPP playlist or already-playing node-local audio. It may prevent a later transition. The v1 FPP plugin provides no local night-loop fallback, and the system must say so plainly.

## 12. Conceptual configuration

Names are illustrative; the implementation track owns the versioned schema.

```yaml
nightSession:
  preshowProfile: christmas-preshow
  showPlaylist: christmas-show
  resting:
    playlist: christmas-resting
    timelineAsset: christmas-resting-fseq
    backgroundAudio: christmas-resting-music
    endOfNightRepeat: true
    maxAudioGainDb: -10

  enterShow:
    lighting:
      startBeforeTimelineEnd: 20s
      fadeDuration: 15s
    projections:
      startBeforeTimelineEnd: 20s
      fadeDuration: 15s
    audio:
      startBeforeTimelineEnd: 3s
      fadeDuration: 3s
    blackoutAfterTimeline: 6s
    announcement: show-starts-now

  enterResting:
    blackoutAfterShow: 6s
    announcement: thank-you
    lightingFadeIn: 10s
    projectionFadeIn: 10s
    audioFadeIn: 8s

  actions:
    requestThermalProfile: home-assistant-show-preheat
    presentationPowerOn: site-presentation-on
    presentationPowerOff: site-presentation-off
    projectorShutdown: projectors-safe-off
```

No field contains a wall-clock time.

## 13. Readiness and validation

Before `start-night`, ShowMesh must report at least:

- exact resting FSEQ variant present on the correct target;
- configured FPP resting playlist exists, contains exactly the expected FSEQ item, contains no FPP audio item, and can run one-shot or repeated as requested;
- parseable non-zero duration and cue offsets within the usable timeline;
- show playlist present and not unexpectedly busy;
- required audio and announcement assets local and hash-current;
- background output and configured maximum gain available;
- required projection actions resolve uniquely and are ready;
- required logical actions have valid confirmation/fallback declarations;
- environmental evidence is safe enough for automated projector strike, or explicitly unavailable with operator handling;
- presentation power actions cannot target the environmental group;
- final-show and fade-out commands exist in the FPP-facing vocabulary;
- where scheduled brightness and ShowMesh fades affect the same output, an observed ceiling plus an independent transition-gain control exists; otherwise that fade configuration is rejected.

Readiness is evidence, not a refusal to continue by default. Safety interlocks such as unsafe projector strike are the explicit exceptions.

## 14. Observability

The API, CLI, UI, audit log, and change stream expose:

- active session and pinned revision;
- lifecycle state and time in state;
- current cycle and whether it is final;
- resting asset duration, observed FPP position, and derived next boundary;
- next cue and its monotonic deadline;
- pending final-show, fade, or shutdown intent;
- per-cue dispatched/completed/confirmed outcome;
- current FPP brightness ceiling and transition gain where observable;
- active background and announcement sessions and effective gain;
- thermal readiness and presentation/environmental power state;
- recovery/degradation reason and required operator action.

Post-show evidence must distinguish playlist completion, blackout activation, resting FSEQ activation, projections restored, background audio restored, and end-of-night repeat state.

## 15. Acceptance scenarios

Implementation is not complete until observed end-to-end behavior covers:

1. a five-minute resting FSEQ whose lighting/projection fades begin before the asset ends and whose audio continues briefly in blackout;
2. show completion followed by blackout, thank-you announcement, and independently timed resting fade-up;
3. repeated cycles with no accumulated timing drift from a duplicated manual rest duration;
4. `request-final-show` during both live and resting states;
5. final show followed by repeating end-of-night resting and no next show;
6. Christmas and Halloween FPP schedules with different fade-out times and identical ShowMesh configuration shape;
7. a direct FPP brightness reduction preserved through later ShowMesh fade-down/fade-up;
8. a coordinator restart during live playback, during resting, and during a transition;
9. a missing/mismatched FSEQ, paused FPP, unexpected playlist, and stale position observation;
10. a missing `start-preshow`, duplicate `start-night`, stale start after shutdown, reordered final/fade commands, and a fade command received before a final-show request;
11. a crash before the first cue send, after the send but before outcome persistence, and while the show-launch barrier is pending;
12. cold-enclosure strike refusal, projector-generated heat requiring exhaust, and environmental protection surviving presentation power-off;
13. an unexpectedly late live show deferring ordinary fade and power-off;
14. PulseMesh MP3 validation without restricting local-output asset formats.
