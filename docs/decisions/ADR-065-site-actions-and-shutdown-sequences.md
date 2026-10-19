# ADR-065: Site actions and shutdown sequences

Status: Proposed (2026-10-19)
Date: 2026-10-19
Supersedes: the `siteControl` block of
[RESTING-MODE.md](../architecture/RESTING-MODE.md) §10.2, §10.3's
"request a thermal profile" binding, §10.4, and the §12 `siteControl` example.
Amends [ADR-053](ADR-053-weather-delay.md) and
[ADR-054](ADR-054-level-1-stop-holds-the-night.md) for what emergency stop
levels 2 and 3 do with power. Leaves ADR-053 decision 10 (a weather delay never
switches power) unchanged.

## Context

`night.session.siteControl` had three fields. `requestThermalProfile` and
`presentationPowerOn` were validated and stored, but nothing ever ran them.
`presentationPowerOff` ran one action, optionally after a list of prerequisites,
when the night was powered down. Each field was tied to one purpose, and the
editor showed them as free-text boxes.

An installation's power hardware rarely changes between shows. The reference
installation runs the same projectors, LED controllers and FM transmitter for
Halloween and Christmas, and wants the same shutdown order for both. It also
wants an emergency stop to choose what it cuts: leave the FM transmitter on, or
turn on a work light on its own circuit.

Emergency stop follow-up lists (`show.emergencystop`) were editable only through
the API and `showmeshctl`.

## Decision

1. **Shutdown sequences are global.** A new singleton config kind,
   `show.sitepower`, holds `shutdownSequences` and `powerOnActions`. A shutdown
   sequence has an `id`, a `label` and ordered `steps`. A step is one of:
   - an action, optionally with `requireConfirmation`;
   - a delay, `delayMs`.

   Actions may belong to any show, like emergency stop follow-ups.

2. **A step that removes power says so.** A power-removing action step declares
   `powerDomain` and `domainProvenance`. Only `presentation` is accepted, as
   §10.2 already required. A step without `powerDomain` is an ordinary action.
   The `removalPolicy` and `immediateSafeAttestation` fields are dropped: a
   sequence whose only step removes power is the immediate case, and a sequence
   with earlier steps is the after-actions case.

3. **The night session has three site action sections.** `night.session.siteActions`
   replaces `siteControl`:
   - `prepareSite`: an ordered action list;
   - `powerDown`: `skipSequences` (global sequence ids not run for this night)
     and `additionalSequences` (night-only sequences, same shape);
   - `endSession`: an ordered action list.

   Night-only actions must belong to the session's own show. Every section is
   optional. Skipping is recorded as a skip list, so a sequence added later runs
   everywhere by default.

4. **Prepare-site actions run once per preparation.** They run after any
   blocking `prepare-site` interlocks pass and the new preparation is
   committed. A repeated `prepare-site` on an open night creates no new
   preparation and sends nothing. Each dispatch carries an identity derived from
   the preparation and the action, so a retried request does not send twice.
   Ending the session and preparing again is a new preparation and runs the list
   again, which is how a show test or reset re-runs readiness. A prepare-site
   action must therefore set a state ("thermal mode: show"), never toggle one.

5. **Power down runs the sequences side by side.** When the night is powered
   down and playback has stopped, every included sequence starts at once. The
   global sequences are read as saved at that moment. Steps inside a sequence
   run in order, honouring delays and confirmation waits. A failed, refused,
   ambiguous or unconfirmed-when-required step stops that sequence only, so
   power is never removed after a device that did not confirm it was off. An
   action deleted since the configuration was saved counts as a failed step.
   Progress is persisted in the night cue outbox, so a coordinator restart
   resumes a wait instead of restarting or skipping it.

6. **End session actions run when the night reaches `stopped`.** That is after
   the power down finishes, or when the night is ended without a power down
   being asked for, such as an operator `end-session`. A night that only faded
   out waits for its power down. A failed action is reported and the
   remaining actions still run. They do not run on a hard stop (decision 8).

7. **Emergency stop level 2 (`stop-power-down`) uses the sequences.** Its
   `show.emergencystop` level gains `skipSequences`. With a night open, it
   forces the night's power down as today, and that power down uses level 2's
   skip list in place of the night's, plus the night's own sequences; the end
   session actions follow it. With no night open, level 2 runs its included
   global sequences itself, with their waits. Its follow-ups run straight
   after the immediate stop, alongside the sequences.

8. **Emergency stop level 3 (`hard-stop`) ignores every wait.** Its level also
   gains `skipSequences`. After the immediate stop it sends every action step of
   every included sequence at once, in step order, skipping delays and
   confirmation waits, alongside its follow-ups. The open night's own
   sequences are included. It runs no end session actions. Hard stop exists
   for an electrical emergency, where cutting power now matters more than
   projector lamp cooldown. Follow-ups are not domain-checked, so an
   installation may add a work light or a standby mode there.

9. **System power on and off are test commands.** `POST /api/v1/site-power/on`
   runs `powerOnActions` in order. `POST /api/v1/site-power/off` runs every
   global sequence as in decision 5. Both need `show:emergencystop:invoke`,
   the scope that can already cut power, are refused while a night session is
   open or another run is going, and are reached in the UI from a Testing
   popup on Live Control and Show Night with a second confirmation. Both
   answer at once and write their results to the audit log when they finish.
   Their progress is held in memory: a coordinator restart abandons the
   remaining steps, and the run then has no result entry.

10. **Editing lives in Settings.** A Settings › Emergency stop tab edits the
    three follow-up lists and the two skip lists. A Settings › Site settings
    tab edits `show.sitepower`. `showmeshctl sitepower get|set|revisions|on|off`
    gives the same reach.

11. **Old configuration is read and refused for write.** A stored `night.session`
    revision that still has `siteControl` is read as `siteActions`:
    `requestThermalProfile` becomes the one `prepareSite` action,
    `presentationPowerOff` becomes one additional sequence, and
    `presentationPowerOn` is dropped. A write containing `siteControl` is refused
    with a message to use the site actions instead.

## Consequences

- The thermal profile, power-on, power-off, prerequisite and removal-policy
  fields leave the night session API, the OpenAPI document and the Show Night
  editor.
- A hard stop with sequences included now removes presentation power, which it
  never did before. An installation that includes no sequence keeps today's
  behaviour.
- Interlocks keep their phases. `presentation-power-on` still gates nothing,
  because system power on is a test command outside a night.
- A sequence's restart safety depends on where it runs: a night's power down,
  including one level 2 forced, survives a coordinator restart; system power
  off, level 2 without a night, and hard stop do not.

## Alternatives considered

- **Extend `show.macro` with a delay step.** Rejected: a macro belongs to one
  show, and macro failure and confirmation defaults are tuned for the show path.
- **Keep one named field per purpose.** Rejected: it tells the operator which
  action must go in each box, and the reference installation needs more than
  one thermal and power action.
- **Run end session actions on a hard stop.** Rejected by the owner: a hard stop
  runs exactly its own list.
