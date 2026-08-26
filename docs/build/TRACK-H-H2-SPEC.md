# Track H seam H2: FPP import, reconciliation, and entry identity

[Track H](TRACK-H-cues-and-playlists.md) · [H1 spec](TRACK-H-H1-SPEC.md) · [FPP plugin coordinator contracts](FPP-PLUGIN-COORDINATOR-CONTRACTS.md) · [ADR-043](../decisions/ADR-043-show-scoped-cues-and-playlist-authority.md) · [ADR-024](../decisions/ADR-024-identity-authorization-and-audit.md) · [RES-018](../research/RES-018-fpp-brightness-control.md) · [SM-63 handoff](SM-63-FPP-PLUGIN-HANDOFF.md)

Status: specified 2026-08-22. The plugin-facing half is frozen in
[FPP plugin coordinator contracts](FPP-PLUGIN-COORDINATOR-CONTRACTS.md)
section 3 and can be built in the plugin repository independently of this
document. This file is the coordinator's half and the authoring behavior on
top of it.

## 1. What this seam is

An operator picks an FPP playlist, sees its real entries, and binds each one
to a same-show Cue. From then on ShowMesh can say which Cue an FPP playlist
entry is, and can say so again after the operator edits that playlist in FPP,
without ever guessing from a filename.

H2 ships no activation. Resolving an observation to a bound entry is where it
stops; making rendering, audio, and LTC act on that is H3 and H4.

## 2. The decision this seam turns on

The canonical playlist hash is computed by the plugin over the definition the
plugin read, which today is the on-disk playlist JSON file on the FPP host.
Section 3.1 of the contracts record explains at length why the coordinator
must not compute that hash from FPP's REST API instead: it would be
canonicalizing a second read of a second representation, and the failure would
surface on show night as a permanent mismatch rather than as the wrong import
path it was.

So the definition arrives the same way the observation does, by the plugin
posting it. The coordinator verifies every definition by re-canonicalizing it
and re-hashing, and files it under the hash it computed itself. The import
flow below reads only from that store.

This also means H2 has no dependency on FPP's REST API at all. Nothing here
calls `GET /api/playlist/{name}`, and nothing needs FPP reachable from the
coordinator. What is needed is the plugin installed and posting.

## 3. Definition storage

A new store table holds one row per `(instanceUuid, playlistHash)`:

| Column | Meaning |
|---|---|
| `instance_uuid` | FPP instance the definition came from. |
| `playlist_hash` | The hash the coordinator computed, not the one the caller declared. |
| `playlist_name` | Name at first report. |
| `definition_json` | The canonical bytes, as the coordinator canonicalized them. |
| `captured_at` | The plugin's read time. |
| `received_at` | Coordinator receipt time. |

Storing the canonical form rather than the received bytes is deliberate: it is
what the hash is over, it is what a later re-verification must reproduce, and
it removes any question of which of two byte sequences the row represents.

**Retention.** A definition referenced by any stored `show.playlist` binding
is never evicted. Beyond those, the newest 16 per instance are kept and older
unreferenced rows are removed. The bound exists because the plugin re-scans
and re-posts, so an unbounded table would grow with every playlist edit the
operator ever made.

## 4. The import and authoring flow

1. **List.** `GET /api/v1/integrations/fpp/playlist-definitions` returns the
   metadata for every stored definition, newest first, so the operator can see
   which playlists this coordinator knows about and which revision of each.
2. **Preview.** `GET /api/v1/integrations/fpp/playlist-definitions/{instanceUuid}/{playlistHash}/entries`
   returns the parsed entry list: section, zero-based position within that
   section, sequence filename, media filename, and item type, in FPP's own
   order. The three sections are read in FPP's order, `leadIn`, `mainPlaylist`,
   `leadOut`, and each is positioned from zero independently, because that is
   what the plugin's callback reports and what the entry key is derived from.
3. **Bind.** The operator writes a `show.playlist` whose `runner` is `fpp`,
   whose `fpp` object carries the instance UUID, playlist name, and that
   playlist hash, and whose entries carry the section, position, and expected
   filenames from the preview, each pointing at a same-show Cue. That write is
   the H1 configuration path, unchanged and already validated there.

Import proposes; it does not write configuration on its own. The operator
chooses which Cue each entry becomes, and a playlist position previewed by
step 2 may be left with no entry authored for it at all: H1 requires `cue` on
every entry a `show.playlist` actually carries, so there is no such thing as
an entry with no Cue. An FPP playlist may legitimately contain items ShowMesh
has nothing to do; the operator simply never authors an entry for that
position.

**Import never makes a filename the Cue identity.** The filenames are carried
into the binding as validation evidence and are compared at reconciliation.
They select nothing.

### 4.1 Parsing the definition

The entry parser reads `leadIn`, `mainPlaylist`, and `leadOut` as arrays of
objects, and reports `type`, `sequenceName`, and `mediaName` when present. It
does not require any other member and does not fail on members it does not
recognize, because the definition is FPP's shape and not ShowMesh's, and a
future FPP field must not make an existing playlist unimportable.

An entry with neither a sequence nor a media filename is still an entry with a
position, and it is listed. A `pause` item is the ordinary case.

Nothing in this parser feeds the hash. The hash is over the whole definition
and was computed before this parser ever ran.

## 5. Reconciliation

Given an accepted observation for instance `U`, the coordinator resolves it in
this order and stops at the first refusal:

1. **Locate the binding.** Find every `show.playlist` object, in any Show,
   whose runner is `fpp` and whose `fpp.instanceUuid` is `U`. When there is
   none, the observation is `unbound`: no ShowMesh output was ever authorized
   by this instance, so there is nothing to hold.

   Searching every Show rather than only the active one is deliberate, and an
   earlier draft of this document had it wrong. Restricting the search to the
   active Show makes step 5 unreachable by construction, and turns the exact
   case Track H exists to catch into the blandest possible answer: Christmas
   active while FPP plays the Halloween playlist would report `unbound`,
   which reads as "nothing here concerns ShowMesh", when what actually
   happened is that FPP is playing another Show's content. It must report
   `cross-show`.
2. **Compare the hash.** When the observation's `playlistHash` differs from
   the binding's, the state is `stale-import`. The old binding is held, not
   remapped. This is the FPP-playlist-edited case, and it is the one the entry
   key exists to make visible.
3. **Derive and match the entry key.** For each entry of the binding, derive
   the key from the binding's instance UUID, playlist name, and hash plus that
   entry's section and position, and compare it to the observation's
   `entryKey`. No match is `unknown-entry`.
4. **Check the corroborating evidence.** When the entry carries an expected
   sequence or media filename and the observation reports a different one, the
   state is `evidence-mismatch`. The filenames never select the entry; they
   only contradict it.
5. **Check the Show.** A binding whose Show is not the active Show is
   `cross-show`. The active Show is re-read here rather than carried from
   step 1, so a binding that was in the active Show when the search ran and
   is not by the time this step runs is still caught.
6. **Resolved.** The observation names exactly one Playlist, entry, and Cue,
   pinned to the Playlist and Cue revisions stored at that moment.

An `unavailable` observation, section 1.4 of the contracts record, resolves to
`identity-unavailable` and never to an entry. It carries no hash and no entry
key by contract, so there is nothing to match; treating its filenames as
identity is exactly the fallback the contract exists to forbid.

An observation whose `playlistHash` has no stored definition is never its own
terminal outcome: a resolution is terminal, and "the binding may still match
by entry key" and "this is a distinct outcome called `definition-unavailable`"
cannot both hold. Instead the coordinator carries whether a definition is
stored as an annotation (`definitionAvailable`) alongside whichever of steps 3
through 6 the observation actually reaches (`unknown-entry`,
`evidence-mismatch`, `cross-show`, or `resolved`), because the key needs only
the five identity fields, so a missing definition is not fatal to matching by
entry key. It is fatal to readiness, since the operator cannot be shown what
the entry contains; section 6 is where that shows up.

Every non-resolved outcome is a state with the observed evidence attached, and
every one of them behaves under H0.2's `mismatchPolicy` when the Playlist is
running. None of them is ever a silent fallback to another entry, another
Playlist, or another Show.

### 5.1 Event-sequence regression

Sequence monotonicity is already enforced at ingestion, contracts section 1.5,
where a regression is refused with `409` and never stored. H2 adds nothing to
that rule and must not re-implement it: a regressed observation never reaches
reconciliation because it was never accepted.

What H2 does add is the recovery route that section 1.5 names as a
prerequisite for the plugin's sending half and deliberately left out of scope:

```text
DELETE /api/v1/integrations/fpp/playlist-entry-observations/{instanceUuid}
```

It clears the stored observation and its sequence anchor for one instance, is
audited, and is guarded by `fpp:command`. That is the operator-held FPP
authority scope, and clearing wedged evidence is an operator recovery action
of the same class as commanding FPP. It is deliberately not `fpp:observe`,
which stays out of the operator bundle so an operator credential cannot forge
plugin evidence. Clearing evidence and manufacturing it are different powers.

The route exists because a single observation carrying a wildly high sequence,
from a misconfigured host or a compromised credential, otherwise refuses every
later legitimate observation for that instance permanently, with direct
database access on the coordinator host as the only remedy.

## 6. Readiness

An FPP-backed Playlist is ready when all of these hold, and reports the exact
failing one when not:

- a definition is stored for its `(instanceUuid, playlistHash)`;
- no NEWER stored definition exists for the same instance and playlist name
  under a different hash (`definition-superseded`, SM-290 — see below);
- every entry's `(section, position)` exists in that definition;
- every entry's expected filenames match the definition at that position;
- every referenced Cue exists, belongs to the same Show, and passes its own
  readiness;
- the latest accepted observation for that instance, when one exists, carries
  the same `playlistHash`.

The last one is what makes a playlist edit visible before showtime rather than
at the transition. It is a warning rather than a hard failure when no
observation has been received at all, because an FPP host that has not played
anything since the coordinator started is the normal afternoon state, not a
fault.

This is H2's own list, frozen to this seam's original scope. §6's vocabulary
was later opened and has taken four more conditions; the current,
authoritative list lives in
`internal/coordinator/fppreconcile/readiness.go`'s own doc comment, with the
full account of what was added and why in
[TRACK-H-cues-and-playlists.md](TRACK-H-cues-and-playlists.md) section H6.

**Amendment (2026-08-26).** The last condition above only ever compares
against the latest PLAYBACK observation, and FPP's own `Playlist::GetInfo()`
legitimately returns no identity once a playlist goes idle, so the last
observation of every run erases the only evidence that check reads. From the
moment a playlist finishes until the next one starts, an edited-but-never-
played FPP playlist read `ready: true`, which is exactly the state an operator
checks readiness in. Two changes close this:

1. `definition-superseded` compares the bound hash against the definition
   store directly (the plugin re-scans and re-posts on its own schedule, not
   only on play), so it can fail readiness with FPP idle and nothing played
   since the edit, with no observation needed at all.
2. An observation that exists but could not establish identity (contracts
   section 1.4) is no longer folded into a `ready: true` warning. It is its
   own failing condition, `evidence-unavailable`: a required check that DID
   have evidence to look at and could not conclude anything from it, which
   must never render the same as "checked and fine." The one remaining
   warning case is "no observation has been received at all": genuinely
   nothing has happened yet to check, the normal afternoon state, not a
   fault.

## 7. Surfaces

- `POST` and the two `GET` routes of contracts section 3, plus the entries
  preview route named in section 4 step 2.
- The `DELETE` recovery route of section 5.1.
- `showmeshctl` verbs for every one of them. The write-parity test requires a
  verb for both non-GET routes, and the existing exemption comment for the
  observation POST already says the read half should grow a verb "when Track H
  gives an operator a reason to look at it". This seam is that reason.
  The definition `POST` is the plugin's route, not an operator capability, and
  takes the same reasoned exemption the observation `POST` has: a hand-typed
  definition is either refused by the hash check or a forged claim about what
  is on the FPP host.
- OpenAPI for all of them, and the generated UI types regenerated.
- No Operator UI view. H6 owns it.

## 8. What cannot be closed here

The plugin does not implement any of contracts section 3 yet, and does not
implement section 1's sending half either. Until it does, this seam is proven
against fixtures and a fake poster, and that is exactly the level to claim:
software behavior closed, real-host behavior open.

Two facts stay open and must not be quietly assumed closed:

- **Whether FPP's REST playlist JSON equals the on-disk file the plugin
  hashes is unmeasured, and this seam is designed so that it does not matter.**
  Nothing here reads the REST path. If a later seam wants it, it owes the
  measurement first.
- **The canonicalizer divergence recorded in contracts section 1.3 is still
  open.** The plugin counts nesting depth per container where the coordinator
  counts per value, and the plugin does not yet refuse invalid UTF-8. Well
  formed definitions, which is every definition FPP itself writes, are
  unaffected. A definition that trips either rule would hash differently on
  the two sides and would fail this seam's verification check, which is the
  visible direction.
