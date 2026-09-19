# ADR-053: Weather Delay

Status: Accepted (owner, 2026-09-19)
Date: 2026-09-19

> **THIS IS NOT A LIFE-SAFETY SYSTEM.**
>
> **Weather delay is not an emergency-alert receiver, it is not a substitute
> for one, and it guarantees nothing. Any part of it can fail. It must never be
> the only thing standing between a person and a hazard. It exists so that the
> display does its best not to be the hazard, and the operator remains
> responsible for the safety of the site and the audience.**

## Context

An outdoor display puts tall metal structures, energized lighting and an
audience in the same place. When a storm arrives the operator needs one action
that makes the display dark, keeps it dark, and tells the audience what is
happening, and that action has to work when parts of the system do not.

ShowMesh has the first third of that. Emergency stop level 1 sends FPP "Stop
Now", `audio.node.silence` and a Resolume blackout at the same time. Nothing
after that keeps the display stopped: the night loop starts the next playlist,
FPP's own scheduler starts the next scheduled item, the cue activation loop
follows FPP, and a node starts cue audio from a MultiSync packet with no
coordinator in the path ([ADR-051](ADR-051-cue-audio-starts-on-the-multisync-start-packet.md)).
An announcement cannot be relied on either. It is refused by the offline
fallback program, it has a single delivery path, and a hand-fired one waits for
a MultiSync packet that a stopped player never sends.

[RESTING-MODE](../architecture/RESTING-MODE.md) section 8 left "automated
public-safety interruption of all playout" as a separate future safety
design. This record is that design.

Broadcast engineering practice is that an alerting system has more than one
path by which an alert can arrive, and depends on none of them. United States
Emergency Alert System rules require a participant to monitor two sources
([47 CFR 11.52(d)(1)](https://www.ecfr.gov/current/title-47/section-11.52)) so
that the loss of one does not silence an alert. ShowMesh is not an Emergency
Alert System participant and claims no compliance with those rules; it borrows
the practice. An alert that arrives on one path is acted on exactly as an alert
that arrives on every path, and no path waits for another. An earlier decision
that leaves this alert with a single delivery path is superseded for this
alert.

## Decision

1. **Two operator actions, one mechanism.** *Weather delay* means the show will
   resume tonight. *Cancel night* means it will not. Both take one press with no
   confirmation step, both are available in the API, `showmeshctl` and the
   Operator UI, and a delay can be changed to a cancel while it is active. The
   only difference is the alert that plays and what happens after it.

2. **A delay is a stored, system-wide state.** The coordinator persists it, so
   it survives a coordinator restart, and publishes it to nodes as a retained
   message. The state is written before any stop is sent.

3. **While the state is set, nothing starts output.** The night loop advances
   nothing. The cue activation loop dispatches nothing. A node refuses cue
   activation and ignores MultiSync start packets. These are checks on the
   state, not on the mode, the show or the caller.

4. **The coordinator enforces dark for as long as the state is set.** On a
   short interval it sends "Stop Now" to any FPP instance it observes playing
   and re-asserts the output gate of decision 5. This is a loop that pushes
   playback state, which ShowMesh otherwise does not have. It is allowed for
   this state only, it can only stop, and it never starts anything. FPP remains
   the scheduler ([ADR-001](ADR-001-fpp-is-authoritative.md)); ShowMesh refuses
   to let that schedule light the display during a delay.

5. **The FPP plugin forces output to zero.** The plugin's brightness engine
   gains a third term beside the ceiling and the transition gain: a gate that
   is either open or closed, written only by this state. Closed means every
   channel is zero, including channels outside the configured apply ranges. The
   gate is persisted, so a player that restarts during a delay comes back dark.
   The plugin reports its effective output level, and that report is how dark
   is confirmed for a player.

6. **Projection is cleared, not faded.** Every Resolume layer is cleared, and
   every render surface is cleared. Emergency stop gains the render surface
   clear as well.

7. **The alert does not wait.** A node runs one command in this order: set
   every other session's mixer level to zero, start the alert, then stop the
   other sessions. The alert therefore never waits on a session that is slow to
   stop, and other audio is inaudible before the alert's first sample. The
   alert plays a configured number of times, ten by default, and resume ends it
   early. It does not wait for a MultiSync packet, a timing probe, or the
   result of any other device's stop.

8. **Starting is accepted from anywhere. Resuming is accepted from one place.**
   A false start costs a dark display and an alert. A false resume relights a
   display in a storm. So a start is delivered over every path available, in
   parallel and not as fallbacks: the MQTT command, the retained state, a
   direct signed HTTP request from the coordinator to each node agent and each
   FPP plugin, and a pre-signed start request that an external system may hold
   and send when the coordinator is down. Replaying a start can only cause
   darkness. Resume is accepted only by the authenticated coordinator API, and
   a node that was started without the coordinator stays delayed until the
   coordinator returns.

9. **[ADR-044](ADR-044-agent-inbound-http-listener.md) decision 3 is superseded
   for one endpoint.** The node agent's inbound listener accepts a signed
   weather delay start, verified with the coordinator key the node already
   holds ([ADR-025](ADR-025-agent-fallback-cache-is-signed.md)). It accepts
   nothing else new, and it never accepts a resume. ADR-044's reasoning stands
   for every other capability.

10. **Dark is confirmed per power group, and cutting power is never ShowMesh's
    job.** A power group is an operator-defined set of devices that share a
    switched supply. An installation defines as many groups as it has, with
    whatever membership it has: lighting only, lighting and projection, moving
    lights on their own, or none at all. For each group the coordinator reports
    whether it is confirmed dark (players idle with gates reporting zero,
    layers and surfaces reporting clear) and, when configured to, publishes
    that as a heartbeat. ShowMesh never switches power as part of a delay. An
    installation that wants a cutoff points its own power controller at the
    heartbeat: a controller that stops hearing a group's heartbeat during a
    delay cuts that group and no other, on timeouts the installation sets. An
    installation with its own power system, or none, publishes nothing and
    loses nothing else. Where a cutoff is used, the coordinator, the alert
    audio path and any transmitter must be supplied from outside every group.

11. **Resume starts the show from the top.** Resume clears the state, opens the
    gate, and returns the night session to its transition into the show, which
    starts the show playlist from its first entry. It never continues from the
    middle. Cancel night plays its own alert, then runs the normal graceful
    power-down. Its state stays set until an operator clears it, and a night
    session that tries to start while it is set is refused and reported.

12. **Automatic triggers may start a delay and may never end one.** A trigger
    source is a weather warning feed or a lightning distance feed. ShowMesh
    reads an alert's type, severity and expiry and never relays its text. A
    trigger first asks the operator. With no answer inside a short window it
    starts a delay. When too little of the night would remain after the
    warning expires, the question is delay or cancel, the window is longer, and
    no answer means cancel. Official warnings do not cover ordinary lightning,
    so a warning feed is never the only trigger an installation relies on.

13. **The feature is optional. An active delay is not.** An installation that
    does not want it, such as an indoor venue with its own alerting, leaves it
    unconfigured. Every part is independently optional: the alert, the power
    groups, the triggers, the external controller. No part requires Home
    Assistant or any other product; ShowMesh publishes MQTT messages and calls
    a configured webhook, and anything may consume them. But once a delay is
    active, no configuration value, mode, scope, flag or API call disables
    decision 3, 4 or 5, shortens them, or exempts a device, and the feature
    cannot be turned off while a delay is active. Changing that takes a
    superseding record.

## Consequences

- ShowMesh gains its one loop that closes a gap between desired and observed
  playback state. The README's statement that no such loop exists needs the
  exception named.
- The node agent's inbound listener is no longer xLights-only. It needs request
  signing, which the xLights surface never had.
- The plugin's brightness contract changes, in the plugin repository and in the
  coordinator's side of it.
- Show mode is unaffected and does not affect this state
  ([ADR-033](ADR-033-show-mode.md) decision 4 already forbids a mode from
  delaying a stop).
- With the coordinator down, the Operator UI control does nothing. What remains
  is the pre-signed start held by an external system, and the devices' own
  interfaces. That gap is accepted and stated, not hidden.
- Whether "Stop Now" plus a closed gate leaves real pixels dark, and the time
  from the press to the first alert sample, are hardware observations that no
  container bench can supply.

## Alternatives

- **A macro.** A macro can stop FPP, clear Resolume and start audio today. It
  runs its steps in order, so the alert waits on every stop, it holds nothing
  stopped afterward, and it has one delivery path.
- **Emergency stop with a follow-up action.** Same two faults: follow-ups run
  after every stop has reported, and nothing prevents a restart.
- **Taking over FPP's scheduler.** Disabling the schedule during a delay would
  remove one restart source, but it makes ShowMesh responsible for putting the
  schedule back, and the gate already makes a scheduled start harmless.
- **MQTT only.** Rejected outright. One broker is one point of failure between
  the operator and the audience.

## Related

- [ADR-001](ADR-001-fpp-is-authoritative.md): FPP stays the scheduler.
- [ADR-025](ADR-025-agent-fallback-cache-is-signed.md): the key a node verifies
  a start with.
- [ADR-033](ADR-033-show-mode.md): no mode may delay a stop.
- [ADR-044](ADR-044-agent-inbound-http-listener.md): decision 3 superseded for
  one endpoint.
- [ADR-051](ADR-051-cue-audio-starts-on-the-multisync-start-packet.md): the
  node-local audio start that a delayed node ignores.
- [RESTING-MODE](../architecture/RESTING-MODE.md) section 8: the deferred
  safety design this record supplies.
