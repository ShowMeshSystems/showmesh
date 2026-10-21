# ADR-067: Sequence testing

Status: Proposed
Date: 2026-10-21
Amends: [ADR-043](ADR-043-show-scoped-cues-and-playlist-authority.md)'s rule
that only Cues a show playlist references reach a node's catalog, and
[ADR-051](ADR-051-cue-audio-starts-on-the-multisync-start-packet.md) decision
2's trigger source, while no night session is open.

## Context

- A Cue reaches nodes only when a `show.playlist` entry, or a `safeCue`
  mismatch policy, references it. Any other Cue is a draft and is left out of
  the catalog.
- An operator testing a new sequence plays it from FPP outside the show
  playlist. On an FPP bound to the active show that is a playlist mismatch,
  and the playlist's mismatch policy (hold, black and silence, or safe cue)
  applies, so the Cue under test never runs.

## Decision

1. Each show has an optional sequence test list, the `show.sequencetest`
   configuration kind, object id = show id: rows of FPP sequence filename to
   one Cue of that show. It is edited on the show workspace's Sequence testing
   tab, through `GET`/`PUT /api/v1/config/show.sequencetest/{id}`, and with
   `showmeshctl seqtest get|set`.
2. The list runs only while its show is the active show and no night session
   is open (a current night session in any state other than `inactive` or
   `stopped`). It stays editable at any time.
3. While it runs, a test mapping is a second way a Cue reaches nodes: the
   catalog includes the mapped Cue and adds the mapped filenames to that Cue's
   triggers, so nodes start its audio on the MultiSync START packet as for any
   playlist Cue. A test-only Cue takes no part in catalog claim conflicts,
   because FPP plays one sequence at a time and the test Cue plays instead of
   a playlist entry, never beside one.
4. While it runs, when FPP reports a mapped sequence filename and no playlist
   entry resolves, the coordinator activates the mapped Cue in full (render,
   audio, LTC and actions) instead of applying the mismatch policy, also when
   the FPP instance has no binding to the show. A resolved playlist entry
   always wins. The activation clears the same way a playlist entry's does
   when FPP stops or moves on.
5. While a night session is open the catalog and the activation decision are
   exactly what they were before this ADR. Nodes hold a catalog without the
   test triggers, so losing the coordinator mid-night cannot start a test Cue.
   The catalog revision changes when a night opens or stops, so the existing
   catalog redeploy brings nodes up to date, and night readiness already waits
   for nodes to acknowledge the current catalog.
6. A filename that a playlist entry of the same show already plays for a
   different Cue, or that another row maps to a different Cue, is refused at
   save. The same Cue named twice is accepted.

The gate reads the night session, which the operator starts explicitly. It is
not a playback-derived "is a show running" flag, which
[ADR-033](ADR-033-show-mode.md) decision 2 forbids.

## Consequences

- Testing a sequence needs no prepare, readiness or pre-show step and no edit
  to the show playlist.
- When the active show has test mappings, starting or stopping a night
  changes the catalog of every node a mapped Cue reaches. A node whose
  redeploy is held because FPP is playing shows as stale in night readiness
  until it is redeployed.
- In show mode the coordinator pins catalogs; the pin is renewed when the
  night gate changes. Editing the test list while show mode holds a pin
  behaves like any other catalog edit made then.
