# ADR-063: Projections fade by a uniform channel multiply

Status: Accepted (owner, 2026-10-19)
Date: 2026-10-19
Amends: [ADR-040](ADR-040-renderer-extracts-channels-gstreamer-owns-frames.md)
decision 2 only.

## Context

The lights fade at every show and resting transition through the FPP
transition gain ([ADR-062](ADR-062-brightness-is-one-setting-for-every-fpp.md)).
Rendered projections did not: a surface cut straight to black or straight to
content when its sequence stopped or started, while the lights beside it
faded.

ADR-040 decision 2 forbids ShowMesh from scaling or blending, which a fade
toward black is. The alternatives were to move each surface's GStreamer
pipeline in process so a GStreamer element could ramp, which rebuilds the
render supervisor, or to fade in Resolume, which only covers surfaces routed
through Arena.

FPP brightness is itself a per-byte multiply on channel data, done without
knowing what a byte means.

## Decision

1. The frame writer may multiply every channel byte of a surface by one level
   from 0 to 1. The same level applies to every byte of the frame. At level 1
   no multiply runs, so output is byte-identical to before.
2. The level comes from a `render.surface.fade` command: a target percent and
   an end time, with an optional start time. The level ramps linearly from
   where it is to the target. It lives in memory, so a restarted node draws at
   full level. A surface left below full level two minutes after its fade
   ends, with no newer fade, returns to full level on its own, so losing the
   coordinator or broker mid-transition never leaves a projection dark.
3. A held blackout, from `render.surface.blackout` or a weather delay, still
   wins and stays instant.
4. The coordinator sends the fade at the same moments as the night lights
   steps: a fade-out ending at the show boundary, a fade-in starting with the
   resting playlist, Fade Out Night, and a return to full level for a show
   and when a night ends or degrades. `renderFadeOutMs` and `renderFadeInMs`
   on the night session set the projection lengths and default to the lights
   lengths; either one alone makes projections fade when the lights do not.
   Fade Out Night uses `fadeOutNightMs` when it is set. A fade command that
   does not reach the broker is sent again until a newer step replaces it.
5. Everything else ADR-040 decision 2 forbids stays forbidden: per-pixel or
   per-channel scaling, colour conversion, compositing, encoding and pacing.

## Consequences

- The frame writer touches every byte once more per frame while a level below
  1 is in effect. That is linear in the byte count, the same cost class as the
  copy ADR-040 already allows.
- A projection fade-out longer than the lights fade-out starts earlier, and
  the transition into a show begins early enough for the longer of the two.
- The level also dims anything else the surface draws, such as the Program
  Mode alert frame.
