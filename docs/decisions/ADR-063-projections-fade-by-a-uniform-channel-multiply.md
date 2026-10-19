# ADR-063: Projections fade by a uniform channel multiply

Status: Proposed
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
   full level.
3. A held blackout, from `render.surface.blackout` or a weather delay, still
   wins and stays instant.
4. The coordinator sends the fade with the same target and end time as each
   night lights step. `renderFadeOutMs` and `renderFadeInMs` on the night
   session override the timing and default to the lights times. Projections
   fade only on nights whose lights fade.
5. Everything else ADR-040 decision 2 forbids stays forbidden: per-pixel or
   per-channel scaling, colour conversion, compositing, encoding and pacing.

## Consequences

- The frame writer touches every byte once more per frame while a level below
  1 is in effect. That is linear in the byte count, the same cost class as the
  copy ADR-040 already allows.
- A fade-out with its own time cannot start before the lights fade window
  opens. A longer `renderFadeOutMs` than `lightsFadeOutMs` is cut to the
  lights window.
