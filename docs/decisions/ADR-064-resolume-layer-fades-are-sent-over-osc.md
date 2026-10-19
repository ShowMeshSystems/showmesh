# ADR-064: Resolume layer fades are sent over OSC

Status: Proposed
Date: 2026-10-19
Amends: the "no OSC anywhere" rule recorded in
[RES-001](../research/RES-001-resolume-smpte-behavior.md) and the
[Resolume control surface bench](../bench/resolume-control-surface.md), for
layer master fades only. Clip, column, deck, bypass and blackout actions stay
on REST.

## Context

The operator needs a Resolume layer to fade over a set time, as a show action
and as a step in the resting to show transitions, in step with the lights fade.

- Arena's REST API sets a layer master to a value. It has no fade time, so a
  fade means many writes over the fade's length.
- Arena's built-in web server is where the crash on the production Arena runs
  (the Beast field read recorded in the bench notes). A ramp of about 25 HTTP
  writes a second for every fading layer is the heaviest load ShowMesh could put
  on that server.
- Arena's OSC input is enabled by default on UDP 7000 and accepts the
  positional address `/composition/layers/<n>/master`. It needs no setup in
  Arena beyond OSC input being on. Art-Net would need each layer's master
  mapped to a DMX channel by hand in Arena.
- OSC is positional and sends no reply (RES-001). Those were the reasons OSC
  was ruled out for clip launches, which need a pinned identity and a
  confirmation. A master fade needs neither: the layer number comes from the
  stored composition (ADR-032, ADR-037), and a fade is a level, not a launch.

## Decision

1. The `fadeLayer` Resolume action ramps one layer's master to a target
   percent over a duration by sending float32 OSC messages to
   `/composition/layers/<layerIndex+1>/master` at about 25 a second.
2. The layer is named, never numbered, by the operator. ShowMesh resolves the
   name to its `layerIndex` through the stored composition at dispatch time.
3. OSC goes to the Resolume instance's own host on its configured OSC port,
   7000 by default. The coordinator sends; it never listens.
4. A sent fade reports `unconfirmable`, never `confirmed`. A newer fade on the
   same layer replaces a running one, starting from the last value sent.
5. In a transition step, the fade time is the step's own fade time, else the
   lights fade for that direction (`lightsFadeOutMs` entering a show or fading
   out the night, `lightsFadeInMs` entering resting), else the action's own.
6. The OSC sender lives in its own package. The Resolume REST collector keeps
   its guards against OSC and UDP.

## Consequences

- A layer fade never touches Arena's web server.
- The fade's result is not observed by ShowMesh. The collector's normal layer
  readings still show the master level when it next surveys.
- A layer reordered in Arena without re-uploading the composition gets the
  fade on the wrong layer, the same exposure the composition file already has.

## Alternatives

- REST ramp: rejected for the crash load above.
- Art-Net: works, but needs per-layer DMX mapping in Arena. Kept as the
  fallback if OSC input proves unreliable on the production Arena.
