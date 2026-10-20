# ADR-066: Resolume can be controlled over OSC only

Status: Accepted (owner, 2026-10-20)
Date: 2026-10-20
Amends: [ADR-064](ADR-064-resolume-layer-fades-are-sent-over-osc.md)'s rule
that clip, column, deck, bypass and blackout actions stay on REST, for a
Resolume instance set to OSC control only. An instance set to REST control,
or with no control setting, behaves exactly as before.

## Context

- The production Arena (7.23.2) crashes inside its own web server: the
  Boost.Beast response serialisation use-after-free described in the
  [bench notes](../bench/resolume-control-surface.md) sections 14 and 14.6,
  faulting at `Arena+0x200D5CE`.
- It crashed on 2026-10-14 and again on 2026-10-19 and 2026-10-20, ten times
  in three hours on the last night, with the same fault address every time.
  It crashed in program mode and in show mode, so closing the WebSocket in
  show mode (ADR-033) is not enough.
- ShowMesh's REST polls, composition survey, WebSocket, action reads and
  writes, and automatic recovery all reach that web server. Arena cannot be
  upgraded before Halloween.
- Arena's OSC input is a separate UDP listener, already used for layer fades
  (ADR-064). It accepts positional addresses and never replies (RES-001).

## Decision

1. A Resolume instance has a `control` setting: `rest` (the default, also
   when absent) or `osc`. It is set through the configuration API,
   `showmeshctl resolume instance set --control`, and the Resolume instance
   form.
2. With `osc`, the coordinator sends no HTTP or WebSocket request to Arena.
   No collector, poll, survey, WebSocket, identity check or automatic
   recovery runs for that instance.
3. Every Resolume action is sent over OSC to the instance's host on its OSC
   port (7000 by default). Positions come from the uploaded composition
   (ADR-032, ADR-037), converted to Arena's 1-based OSC numbering:

   | Action | Messages |
   |---|---|
   | launchClip | `/composition/decks/{D}/select` 1, then `/composition/layers/{L}/clips/{C}/connect` 1 |
   | selectDeck | `/composition/decks/{D}/select` 1 |
   | launchColumn | `/composition/decks/{D}/select` 1, then `/composition/columns/{N}/connect` 1 |
   | clearLayer | `/composition/layers/{L}/clear` 1 |
   | setLayerBypass | `/composition/layers/{L}/bypassed` 1 or 0 |
   | setLayerMaster | `/composition/layers/{L}/master` float 0 to 1 |
   | fadeLayer | ADR-064's ramp on `/composition/layers/{L}/master` |
   | blackout | one `/composition/layers/{L}/clear` 1 per layer; `/composition/disconnectall` 1 when no composition is uploaded |

   `{D}` is the deck's position in the composition file, `{C}` and `{N}` the
   column within its deck, `{L}` the layer index.
4. Arena's clip and column addresses count within the selected deck, which
   ShowMesh cannot read in this mode. So a clip or column launch selects its
   own deck first, on the same socket, every time.
5. A sent action reports `unconfirmable`. A name not in the uploaded
   composition, a persistent clip, and anything but a blackout with no
   composition uploaded are refused and send nothing.
6. `fadeLayer` starts from the last level ShowMesh sent that layer, else from
   an optional `fromPercent` param, else from full. It never reads Arena.
7. Status reads as not observed, never as down and never as healthy: every
   Resolume signal is `not_collected`, saying Arena is controlled over OSC,
   and night readiness lists the instance as not verifiable instead of
   failing or warning.

## Consequences

- No action result is confirmed, and ShowMesh shows nothing of Arena's live
  state. The operator checks Arena on its own screen.
- An OSC launch moves Arena's selected deck to the launched clip's deck,
  which a REST launch never did.
- A layer, clip, column or deck reordered in Arena without re-uploading the
  composition reaches the wrong target, with no composition check to catch it.
- After an Arena crash nothing is restored automatically; the operator
  relaunches by hand or replays the show actions. A manual restore request
  is refused with a plain reason.
- An emergency stop or weather delay sends its blackout over OSC and reports
  the Resolume part as sent but unchecked. While a weather delay is active,
  an OSC instance does not hold its power group back from reading dark,
  because ShowMesh can never read it.
- Persistent clips cannot be launched in this mode.
- Only clip launch and deck select are confirmed on a real Arena (bench
  section 6.4). Column connect, layer clear, layer bypassed, layer master,
  disconnect-all, and deck numbering with closed decks follow Arena's
  documented address space and are the rig check's targets.

## Alternatives

- Keep REST and reduce its load further: rejected, the crash happens in show
  mode with the WebSocket already closed.
- Art-Net for every action: needs per-layer DMX mapping in Arena and cannot
  launch a named clip.
