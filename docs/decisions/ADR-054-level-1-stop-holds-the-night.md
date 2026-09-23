# ADR-054: A level 1 emergency stop holds the night session

Status: Accepted (owner, 2026-09-23)
Date: 2026-09-23

## Context

Emergency stop has three levels. Level 2 (`stop-power-down`) forces the active
night session into its graceful shutdown, and level 3 (`hard-stop`) ends the
session with no wait. Both change the session before they stop the targets, so
the night loop cannot read the stopped player as a finished show.

Level 1 (`stop`) stopped FPP, silenced the audio nodes, and blacked out
Resolume and the render surfaces, and did nothing to the night session. Its own
doc comment in `internal/coordinator/api/emergencystop.go` and the OpenAPI
description of `POST /emergency-stop/stop` both said so: "No night-session
interaction of any kind."

That left the night loop running. On its next tick it saw an idle player,
recorded the cycle as interrupted, returned the session to resting, and started
the resting playlist and the background audio bed again. The operator pressed
Stop and the display came back on by itself a few seconds later.

What an operator expects instead is what FPP does after "stop all": nothing
plays until someone selects a playlist and starts it, and then it starts from
the first entry.

## Decision

1. **Level 1 holds the night session.** When a night session is active (any
   state other than `inactive` or `stopped`), level 1 sets a hold on the
   session record before it dispatches to any target: `stopHold`, carrying the
   reason, the time, and the name of the principal who pressed Stop. The hold
   is a field on the record, not a lifecycle state; the session keeps the state
   it was in. With no active session, level 1 behaves exactly as before. A
   failure to set the hold is reported in the response and the audit entry and
   never stops the stop from proceeding. A live cycle is closed as stopped by
   the operator once the targets have been stopped.

2. **While held, nothing starts.** The night loop runs a reduced tick that
   starts no playlist, background audio bed, cue or announcement, on the same
   model as the weather delay tick in [ADR-053](ADR-053-weather-delay.md). It
   still advances what only removes output: a pending fade-out or power-down
   proceeds, and a still-playing bed is stopped. The cue activation loop
   refuses output-starting work while held, the way it does during a weather
   delay.

3. **Resume starts the show from its first entry.** A new night command,
   `resume-show`, under the existing `night:command` scope, clears the hold and
   moves the session to `transition-to-show` with its launch due immediately.
   The night loop then starts the show playlist from its first entry through
   the same launch path `start-night` uses, including its busy and stale
   evidence checks. The session never returns to resting first. Each resume
   starts a new cycle. It is audited as `show.night.resume_show`.

4. **When resume is refused.** `resume-show` is refused when no hold stands,
   while a weather delay is active, while the session is degraded, and in
   `preparing`, `fading-out` and `end-of-night-resting`, where no show can
   start. It is also refused in `preshow`, because `start-night` has not run
   and its readiness result, age and gate checks have never been applied; the
   operator is told "The night has not started yet. Press Start Night to start
   the show." In `preshow`, `start-night` clears the hold when it runs, after
   those checks pass. The hold stays in place after any refusal.

5. **Precedence.** A weather delay that starts while the night is held takes
   precedence: the loop runs the weather delay tick. When the weather delay
   resumes, the hold is cleared too, so the operator resumes once, not twice.
   Levels 2 and 3, `fade-out-night`, `power-down-presentation` and
   `end-session` keep their existing behaviour while held; each ends the
   session or moves it to `fading-out`, and the hold ends with it.

6. **One contract for every client.** `stopHold` is on the night session read
   shape and in `api/openapi.yaml`. `showmeshctl night resume-show` sends the
   command and `showmeshctl night status` prints the hold. The operator UI
   shows a top-bar alert on every screen while held and a Resume button on Live
   Control and on Show Night, each disabled with a hint when no hold stands.

This supersedes the level 1 "no night-session interaction" rule stated in
`emergencystop.go`'s own doc comment and in the OpenAPI description of
`POST /emergency-stop/stop`. No accepted ADR stated that rule; the context of
[ADR-053](ADR-053-weather-delay.md) described it as the behaviour at the time.

## Consequences

- Stop now means stopped until an operator acts. A display no longer restarts
  itself after level 1.
- The show restarts from the top, never mid-show. A show interrupted halfway
  plays again from its first sequence.
- The night session record gains three columns (schema v44). Existing rows read
  back with no hold.
- A hold survives a coordinator restart, because it is on the record the loop
  reads every tick.
- `resume-show` does not run a fresh readiness pass. It is accepted only after
  `start-night` has run and applied its readiness checks; the launch path's own
  busy and evidence checks still apply. A night held in `preshow` continues
  only through `start-night`, which applies those checks and clears the hold.
- FPP's own scheduler is outside this decision. A schedule entry configured on
  the player can still start something after level 1, exactly as before.

## Alternatives

- **Resume to resting first.** Resume would return the session to its resting
  presentation and let the next cycle start the show. Rejected by the owner
  ruling: the operator who pressed Stop and then Resume wants the show, and
  resting first repeats the experience this decision exists to remove.
- **Level 1 ends the session.** Level 1 would behave like level 3 for the
  night. Rejected because it removes the difference between the levels and
  forces a fresh prepare-site and readiness pass to continue the night after
  an ordinary stop.

## Related

- [ADR-038](ADR-038-fpp-authorizes-night-sessions.md): the night session
  lifecycle this hold sits on.
- [ADR-053](ADR-053-weather-delay.md): the weather delay hold this decision
  follows, and which takes precedence over it.
