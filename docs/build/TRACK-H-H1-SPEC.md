# Track H seam H1: the `show.cue` and `show.playlist` configuration kinds

[Track H](TRACK-H-cues-and-playlists.md) · [ADR-043](../decisions/ADR-043-show-scoped-cues-and-playlist-authority.md) · [ADR-027](../decisions/ADR-027-show-and-surface-model.md) · [ADR-030](../decisions/ADR-030-operator-ui-is-the-authoring-surface.md) · [ADR-039](../decisions/ADR-039-operator-configuration-is-store-backed.md) · [IDENTIFIER-REGISTER](IDENTIFIER-REGISTER.md) · [FPP plugin coordinator contracts](FPP-PLUGIN-COORDINATOR-CONTRACTS.md)

Status: specified 2026-08-22 from the H0 decisions in the track document.
This seam ships the two configuration kinds and nothing that runs them. No
activation, no runner, no catalog deployment, no FPP import: H2 through H5 own
those and are specified against the shapes frozen here.

## 1. What this seam is

`show.cue` and `show.playlist` become revisioned configuration kinds with
store, HTTP API, OpenAPI, `showmeshctl`, export/import, audit, revision
history, and validation parity, following `show.action` and `show.surface`
exactly. The Operator UI is deliberately **not** in this seam; the track
document places it at H6, and the enforced parity gate is the CLI one.

## 2. `show.cue`

Object id is operator-chosen. Payload:

```json
{
  "show": "halloween-2026",
  "name": "Thriller",
  "outputs": {
    "render": { "sequence": "thriller" },
    "audio": { "asset": "thriller-audience", "startOffsetMillis": 0 },
    "ltc": { "startOffsetMillis": 0 },
    "announcement": { "policy": "duck", "duckGainDb": -18, "fadeMillis": 300 }
  }
}
```

- `show` is required and must name an existing `show` object. It is the
  ADR-027 namespace, and this seam enforces that a later write may not change
  it: a `PUT` whose `show` differs from the stored revision's is refused,
  naming both. Moving a Cue to another Show would otherwise convert every
  Playlist entry referencing it into exactly the cross-show reference section
  4 refuses at write time, and it would surface at activation on show night
  rather than at the edit that caused it. An operator who wants this Cue in
  another Show authors one there.

  Checked 2026-08-22: `show.surface` does **not** enforce this, and neither
  does any other show-scoped kind. Its `PUT` is a full replacement that never
  compares the incoming `show` against the stored one. That is a real gap, it
  predates Track H, and fixing it belongs to whoever owns that kind rather
  than to this seam.
- `name` is required, bounded at 200 runes, and is operator-facing only.
- `outputs` is required and must declare at least one output. A Cue that
  declares nothing is an authoring mistake, not an empty-but-valid Cue.
- `outputs.render.sequence` is the **logical** sequence name. It is not an
  FSEQ filename and not an asset id: nodes resolve the target-specific render
  asset from it, which is the whole reason ADR-043 keeps runner and target
  detail off the Cue.
- `outputs.audio.asset` names a same-show audio asset. `startOffsetMillis` is
  where inside that asset the Cue begins, default `0`, and must be `>= 0`.
- `outputs.ltc.startOffsetMillis` is H0.3's single LTC offset. It must be
  `>= 0` and is bounded at 24 hours. Its runtime meaning is
  `Cue LTC start offset + current Cue position`, and this seam ships the field,
  not the arithmetic.
- `outputs.announcement.policy` is one of `duck`, `mix`, `interrupt`.
  `duckGainDb` is required for `duck`, must be negative, and is bounded at
  -60 dB. `fadeMillis` is `>= 0` and bounded at 60000. `duckGainDb` is refused
  on `mix` and `interrupt`: an ignored field reads as an applied one.
- A Cue declaring `announcement` must also declare `audio`. An announcement
  with no audio to play is a policy with no subject. The `audio` output is what
  the announcement plays, and declaring `announcement` alongside it routes that
  audio through the announcement session instead of the exclusive program
  route, which is why such a Cue claims `announcement-session` and not
  `program-audio-route` (H0.5).
- A Cue declaring `ltc` must also declare `audio`. ADR-018 makes LTC and
  program audio one clock domain, so an LTC-only Cue has no clock to emit
  from.

Unknown keys are refused at the top level and inside every nested object, as
`show.surface` does.

## 3. `show.playlist`

Object id is operator-chosen. Payload:

```json
{
  "show": "halloween-2026",
  "name": "Main show",
  "runner": "fpp",
  "mismatchPolicy": "hold",
  "fpp": {
    "instanceUuid": "…",
    "playlistName": "Halloween Main",
    "playlistHash": "<64 lowercase hex>"
  },
  "entries": [
    {
      "id": "e1",
      "cue": "thriller",
      "fpp": {
        "section": "mainPlaylist",
        "position": 0,
        "expectedSequenceFilename": "Thriller.fseq",
        "expectedMediaFilename": "Thriller.mp3"
      }
    }
  ]
}
```

- `runner` is required and is one of `fpp` or `showmesh-audio`. The future
  general `showmesh` runner is reserved by ADR-043 and is **refused** here: a
  runner nothing implements must not be authorable.
- `fpp` is required when `runner` is `fpp` and refused otherwise.
  `showmeshAudio` is permitted only when `runner` is `showmesh-audio`.
- `fpp.playlistHash` is the imported canonical hash defined in
  FPP-PLUGIN-COORDINATOR-CONTRACTS §1.3. It is validated as 64 lowercase hex
  characters. This seam does not compute it; H2's import does.
- `mismatchPolicy` is H0.2's decision. Default `hold`. Permitted only when
  `runner` is `fpp`, because a ShowMesh-run playlist has no external authority
  to contradict it. `safeCueRef` is required when the policy is `safeCue`,
  refused otherwise, and must name a same-show Cue.
- `showmeshAudio.repeat` is one of `none` or `all`, default `none`.
- `entries` is required and non-empty. Each entry has:
  - `id`, required, operator-chosen, unique within the Playlist, bounded and
    validated like every other object id;
  - `cue`, required, naming a Cue that exists and belongs to the same Show;
  - `fpp`, required when `runner` is `fpp` and refused otherwise, carrying
    `section` (may be empty), `position` (`>= 0`), and the optional
    `expectedSequenceFilename` / `expectedMediaFilename` validation evidence.

### 3.1 The entry key is derived, never authored

The deterministic entry key of FPP-PLUGIN-COORDINATOR-CONTRACTS §1.3 is a
function of `fpp.instanceUuid`, `fpp.playlistName`, `fpp.playlistHash`,
`entry.fpp.section`, and `entry.fpp.position`. All five already live in this
payload, so storing the key alongside them would store a value that can
disagree with its own inputs.

This seam therefore adds an exported derivation over the stored payload,
reusing `pkg/fppidentity` rather than re-implementing RFC 8785, and stores
nothing. H2 searches every Show for candidate bound Playlists naming the
observed instance, then narrows to the single candidate binding for that
instance, checks its playlist hash against the observation's, and only then
derives the key for each entry of that one binding and compares it to the
observation's entry key. Whether the matched binding belongs to the active
Show is checked separately, after the entry match.

Validation refuses two entries with the same `(section, position)` pair,
because they derive the same key and no runtime evidence could ever tell them
apart. Two entries referencing the same Cue at different positions is
legitimate and must be accepted: that is exactly the duplicate-filename case
the entry key exists to resolve.

## 4. Validation, and what is deliberately left to readiness

Refused at write time:

- a `show` that does not exist, and any cross-show reference (`entries[].cue`,
  `mismatchPolicy.safeCueRef`);
- a `cue` that does not exist;
- duplicate entry ids, and duplicate `(section, position)` pairs;
- a runner-specific object present for the wrong runner, and any unknown key;
- a negative or out-of-bounds LTC offset, audio offset, duck gain, or fade;
- an empty `entries` list, an empty `outputs` object, and the unimplemented
  `showmesh` runner.

Left to H6 readiness, deliberately, and recorded here so it is a decision
rather than an omission:

- **whether the selected nodes can execute a Cue's outputs.** That needs node
  declarations and surface assignments, which are a deployment fact rather than
  a property of the payload, and a stale node list must not make a stored
  Playlist retroactively invalid;
- **conflicting exclusive claims across concurrently runnable Playlists.**
  Two entries of one Playlist are never concurrently active, so the conflict
  only arises between Playlists, and that is a readiness question about what a
  Show can run at once.

H1 does ship the claim derivation itself: an exported pure function mapping a
Cue payload to its H0.5 claim set, so readiness, activation, and dispatch all
answer the question the same way instead of each deriving it again.

## 5. Deletion and referential integrity

**There is no delete surface for any configuration kind, so there is nothing
to guard yet.** Checked 2026-08-22: the only `DELETE` routes the coordinator
serves are the browser session, a node declaration, and a principal token.
`show`, `show.surface`, `show.action`, and `show.macro` have never been
deletable, and neither is a Cue or a Playlist.

That is why this seam ships no delete guard. It is not a gap this seam is
leaving open: a dangling `entries[].cue` or `safeCueRef` cannot be created by
deletion when deletion does not exist, and every other way of creating one is
already refused at write time by section 4.

The rule for whoever adds configuration-object deletion, whenever that
happens: deleting a Cue that a Playlist entry or a `safeCueRef` still
references is refused, and the refusal names the referring Playlist and entry.
A dangling entry would otherwise fail at activation time on show night rather
than at the edit that caused it.

## 6. Surfaces this seam must ship

- Store persistence, revision history, and audit through the existing config
  object path. No new store table.
- `GET`/`PUT` collection and object routes plus revision history, on the
  `show.action` route shape, under `config:write` for writes.
- `api/openapi.yaml` schemas and paths, and the generated UI types regenerated
  so `make ui-gen-check` passes even though no UI view is added.
- `showmeshctl cue` and `showmeshctl playlist` verbs covering every non-GET
  path. `cmd/showmeshctl/writeparity_test.go` fails the build otherwise, and
  that gate is the point of Track G seam G-7.
- `IDENTIFIER-REGISTER` rows flipped from `reserved` to `shipped`.

Two surfaces named in the track document turn out not to exist for any
configuration kind, and this seam does not invent them:

- **there is no export/import subsystem for configuration objects.** The
  portable-bundle half of ADR-009 is unbuilt for every kind, and BUILD-PLAN
  lists it as deliberately cut from day 0. The copy guard the track document
  asks for is the cross-show reference validation in section 4, which is real
  and is shipped here.
- **configuration writes emit no change-stream event.** The change stream
  carries observations, health, and macro-run telemetry; no shipped kind
  publishes a config event, and inventing one for these two would make Track H
  the only kind with a different contract. When H3's activation needs runtime
  telemetry, that is an observation with provenance, not a config-write echo.

## 7. What this seam must not do

- No Operator UI view. H6 owns it.
- No FPP import, no reconciliation, no observation consumption. H2 owns those.
- No activation, catalog, or active-show generation. H3 and H4 own those.
- No new configuration kind beyond the two reserved ones, and no new scope:
  `config:write` already guards every configuration write.
