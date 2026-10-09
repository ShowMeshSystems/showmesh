# FPP plugin coordinator contracts

> **ADR-048 amendment, 2026-08-30.** Sections 1 through 4 remain the normal
> coordinator-facing contract.  The plugin has no general execution authority
> under those sections.  [Track J](TRACK-J-fpp-fallback.md) adds a separately
> versioned signed fallback-program contract: it maps the existing deterministic
> entry key to an already authorized Cue and named node targets during confirmed
> coordinator loss.  Its schema is frozen by J1 before plugin or node work
> begins; it does not widen any existing observation route into a command route.

> **2026-10-05 amendment, fallback activation.** §5 is new. It fixes the wire
> shapes for [Track J](TRACK-J-fpp-fallback.md) step J3: the plugin registers
> an executor public key with the coordinator, the signed fallback program
> carries that key and each target node's address, and a node accepts a
> fallback activation only when it is signed by that key. §1 through §4 are
> unchanged.

> **2026-10-05 amendment, fallback cutoff and hand-back.** §5.12 through §5.17
> are new. They fix the wire for [Track J](TRACK-J-fpp-fallback.md) step J5:
> how long a program stays valid and how often the plugin refetches it, the
> plugin's three states, what the cutoff does, the state report the plugin
> sends the coordinator, and what the coordinator holds back while a plugin is
> the executor. §5.5 and §5.6 are unchanged: no request shape and no signed
> byte in them moves, and the signed program gains no member. Two routes are
> new, both on the coordinator: `PUT` and `DELETE` on
> `/api/v1/fallback-programs/{fppInstanceId}/fallback-state`. §5.12 and §5.13
> change three things plugin step J4 built, and say which.

> **2026-09-29 amendment, brightness state read.** §2 gains §2.6, the
> plugin's read-only brightness state route, which the coordinator's
> `fppbrightness` collector polls and which no earlier revision of this file
> recorded. Plugin code comments that cite "section 3" for it mean this
> section; §3 is playlist definition publication. §2.6 records what the plugin
> serves today, including `weatherGateClosed`, which the collector does not
> decode yet.

[RES-018](../research/RES-018-fpp-brightness-control.md) · [ADR-043](../decisions/ADR-043-show-scoped-cues-and-playlist-authority.md) · [ADR-024](../decisions/ADR-024-identity-authorization-and-audit.md) · [Track F](TRACK-F-resting-mode.md) · [Track H](TRACK-H-cues-and-playlists.md) · [SM-63 handoff](SM-63-FPP-PLUGIN-HANDOFF.md)

Status: frozen 2026-08-21, extended 2026-08-22, corrected 2026-08-23,
corrected 2026-08-26. This record fixes the wire contracts the ShowMesh FPP
plugin runtime shares with the coordinator: playlist-entry observation
ingestion, the coordinator-owned brightness transition gain, and playlist
definition publication. RES-018 decided the design; this file fixes the
exact bytes so the plugin and the coordinator can be built independently and
still meet.

**2026-09-08 amendment:** §1.2 gains an optional `playlistLoop`, and §1.8 is
new. FPP 10 never delivers the playlist `start` callback action to a plugin
(`Playlist::PlayImpl()` sets status to PLAYING before `Start()` reads it, so its
fresh-start test always evaluates to "playing"; verified against the pinned FPP
10.0 source at `370e62ed7e8c8318da6ee5b01312b8b75082d952`). Without `start`, a
playlist looping back into an entry could not be told from its first visit,
because a loop's second visit derives the identical entry key, so a repeating
playlist stopped re-firing its Cue. `playlistLoop` carries FPP's own pass
counter, which both majors already hand the plugin, and which the plugin
previously discarded. Per owner ruling (2026-09-08): the fleet is moving to FPP
10, so this is fixed rather than accepted as a limitation. Entry identity is
unchanged; see §1.8 for why the `start` term stays and why the upgrade order
matters.

**2026-09-08 amendment, definition republish:** §3 gains §3.9, a frozen shape
for a coordinator-triggered republish of playlist definitions, served by the
plugin as an inbound route. §3.8 and the section now numbered §3.10 are
corrected where §3.9 makes them false. The plugin has served an inbound route
since it shipped section 2.2's brightness path, so §3.8's "no inbound HTTP
route" clause was already stale before this amendment; the second-listener and
callback-thread constraints beside it are unchanged and stay.

**2026-08-26 correction:** sections 1.2 and 1.3 left the canonical spelling
of `section` implicit — described as "FPP playlist section" with no fixed
vocabulary. Two independently correct implementations each read that as a
different, defensible spelling: the plugin hashed FPP's own runtime section
string (`LeadIn`, `MainPlaylist`, `LeadOut`, `New`), the coordinator derived
from the playlist definition's JSON member names (`leadIn`, `mainPlaylist`,
`leadOut`). The shared fixtures could not catch it, because `entry-key.json`
supplies an already-canonical `section` and checks only derivation. Section
1.2 now names the canonical spelling explicitly and gives the mapping table
from FPP's runtime strings; section 1.3 states that `entryKey`'s `section`
input is that same canonical value. `test/fixtures/fpp/section-mapping.json`
pins the mapping itself. See [TRACK-H-CHAIN](../bench/TRACK-H-CHAIN.md) for
the failure this closes; the plugin fix that shipped first is SM-275, and
this correction is SM-278.

**2026-08-23 correction:** section 3.1 previously claimed the plugin "has no
HTTP server and deliberately registers no route" and cited ADR-013 for that
claim. That contradicted section 2.2, which has always required the plugin to
serve an inbound brightness route, and ADR-013 is about UDP 32320 MultiSync
port sharing, not HTTP routing; it says nothing that forbids a route. Per
owner ruling (2026-08-23): the plugin opens no listening socket of its own,
but it may register narrow, idempotent, evidence-returning routes on fppd's
own web server, and section 2.2's brightness route is specified to be
unauthenticated when it is built, matching the unauthenticated-by-default
posture SECURITY.md and RES-015 §7.4 already record for fppd's own web UI and
API. A bearer credential is deferred to a future season as a separate tracked
item. See sections 2.2, 2.3, and 3.1.

Nothing here has run against a real FPP host. The contract is verified by unit
tests and by the shared fixtures in
[`test/fixtures/fpp/`](../../test/fixtures/fpp/README.md), on both sides.

**Every numbered section below carries a build status on its own heading, and
that status is the only thing in this document a reader may treat as a claim
about what exists.** A frozen shape and a shipped behavior read identically in
prose, so prose here describes the CONTRACT and never implies an
implementation. A section that says what the plugin "does" is saying what this
contract requires of it, not reporting what is deployed. Keep the status lines
true when either side ships.

## 1. Playlist-entry observation ingestion

**Status: coordinator BUILT, plugin BUILT,** except §1.8, which carries its own
status.

Coordinator anchor: `handlePostFPPPlaylistEntryObservation`. The plugin's half is
that repository's own assertion and is not verified from here.

### 1.1 Endpoint and authorization

```text
POST /api/v1/integrations/fpp/playlist-entry-observations
```

Guarded by the `fpp:observe` scope. The installed plugin principal holds
`show:macro:run` and `fpp:observe`; the `scheduler` role is that principal and
its bundle grows by exactly this one scope. `fpp:command` and the human
operator scopes are not prerequisites, and `fpp:observe` is deliberately not in
the `operator` bundle: an operator credential must not be able to forge plugin
evidence.

The request authenticates as a bearer token, so the ADR-024 decision 6 CSRF
header requirement does not apply to it. Authentication and the scope check run
**before** the body is parsed.

Reads of the ingested state are open under `observation:read`, matching every
other FPP read surface:

```text
GET /api/v1/integrations/fpp/playlist-entry-observations
```

It returns the latest accepted observation for every known instance. It exists
so a client that sees the change-stream event has an authoritative state to
re-fetch, per ADR-020's non-resumable stream rule.

### 1.2 Request body, schema version 1

Content type `application/json`. The body is bounded at **16384 bytes**; a
larger body is refused with `413` before it is parsed. The complete playlist
definition never travels in this body, only its hash, so the bound is
generous for every legitimate observation.

| Field | Type | Required | Meaning |
|---|---|---|---|
| `schemaVersion` | integer | yes | Currently `1`. Any other value is refused. |
| `instanceUuid` | string | yes | Persistent FPP UUID, from FPP's `SystemUUID` setting. |
| `playlistName` | string | when available | FPP playlist name. |
| `playlistHash` | string | when available | SHA-256, lowercase hex. See §1.3. |
| `section` | string | when available | Canonical section: the playlist definition's member name (`leadIn`, `mainPlaylist`, `leadOut`) for those three, or FPP's own runtime string unchanged for any other value. May be empty. See below. |
| `position` | integer | when available | Zero-based position within the section. |
| `entryKey` | string | when available | SHA-256, lowercase hex. See §1.3. |
| `sequenceFilename` | string | no | Absent when the entry has none. |
| `mediaFilename` | string | no | Absent when the entry has none. |
| `action` | string | yes | One of `start`, `playing`, `stop`, `query_next`, `unknown`. |
| `sequence` | integer | yes | Monotonic per-instance event sequence. See §1.5. |
| `observedAtMillis` | integer | yes | Plugin observation time, epoch milliseconds. |
| `coalescedSincePreviousAcknowledged` | integer | yes | Gap evidence, `0` when none. |
| `playlistLoop` | integer | no | FPP's own mainPlaylist pass counter for the running playlist, `0` on the first pass. Absent when the callback did not supply one. See §1.8. |
| `unavailable` | string | no | Absent, or one of the §1.4 reasons. |

`instanceUuid`, `sequence`, `observedAtMillis`, `action`, `schemaVersion`, and
`coalescedSincePreviousAcknowledged` are present on every observation,
available or not.

The identity fields divide into two groups, and the split is load bearing:

- **`playlistHash` and `entryKey` are derived identity.** They are required
  when `unavailable` is absent and **must be absent** when it is present.
  Neither can exist without the playlist definition, so a value in either
  field on an unavailable observation is a claim nothing computed. The
  coordinator refuses it rather than storing an entry key it never verified,
  because Track H consumes the stored observation and an unverified key there
  is an unverified key in the Cue binding.
- **`playlistName`, `section`, and `position` are corroborating evidence.**
  They are required when `unavailable` is absent, and are permitted but not
  required when it is present. `section` may legitimately be an empty string;
  `playlistName` may not.

"Permitted to be absent" never means "permitted to be arbitrary". Every field
present on the wire is validated whether or not `unavailable` is set.

**The canonical spelling of `section`.** The wire value of `section` is the
playlist definition's own section member name — `leadIn`, `mainPlaylist`, or
`leadOut` — for any of the three sections FPP's playlist callback has a
known runtime spelling for, and is FPP's own runtime string, passed through
**unchanged**, for any other value FPP might report. It is never FPP's
runtime spelling for those three known sections specifically: the plugin
maps it **before** placing it in this field or hashing it into `entryKey`
(§1.3), and only for a runtime string it does not recognize does the runtime
spelling itself reach the wire. This was previously left implicit, and the
two sides read it two different, individually defensible ways, which is the
failure [TRACK-H-CHAIN](../bench/TRACK-H-CHAIN.md) records. FPP 10.0's
runtime strings, and what each maps to on the wire:

| FPP runtime section string | Canonical wire value |
|---|---|
| `LeadIn` | `leadIn` |
| `MainPlaylist` | `mainPlaylist` |
| `LeadOut` | `leadOut` |

Any runtime section string outside this table — including FPP's `New`, which
names no member of the playlist definition — is passed through **unchanged**.
It is not mapped to a fourth canonical spelling and it does not become an
§1.4 `unavailable` reason: minting either would be a new wire value this
contract does not otherwise need. An entry key built from it will not match
any entry the coordinator parsed out of `leadIn`, `mainPlaylist`, or
`leadOut`, so the visible failure is the coordinator's existing
unknown-entry outcome, on the observation carrying that section, not a
protocol-level refusal.

### 1.3 The two hashes

These are not negotiable and are not reinterpreted by either side. The
reference implementation is `deriveEntryKey()` in the plugin repository's
`native/src/playlist_identity.cpp`; the coordinator's `pkg/fppidentity`
matches it byte for byte, proven by the shared fixtures. A disagreement is a
blocker raised against both, never a unilateral change on either side.

**`playlistHash`** is SHA-256 over the [RFC 8785](https://www.rfc-editor.org/rfc/rfc8785)
JSON Canonicalization Scheme serialization of the complete playlist definition
FPP returned. No runtime field is removed before hashing.

**`entryKey`** is SHA-256 over the RFC 8785 canonicalization of a JSON object
with exactly these five members. JCS sorts member names by their UTF-16 code
units, so the canonical member order is:

```json
{"instanceUuid":"...","playlistHash":"...","playlistName":"...","position":0,"section":"..."}
```

`position` is a JSON number, not a string. The key is an object rather than a
delimited string specifically so a playlist name or section containing a
separator character cannot collide with a different entry.

The `section` member is the same canonical value §1.2 fixes — the playlist
definition's member name, or an unrecognized runtime string passed through
unchanged — never FPP's runtime spelling for a section §1.2's table maps. An
implementation that hashes FPP's runtime string here instead produces an
`entryKey` that never matches the coordinator's, which is the failure
[TRACK-H-CHAIN](../bench/TRACK-H-CHAIN.md) records; `entry-key.json`'s cases
alone could not catch it, because they start from an already-canonical
`section` rather than from FPP's runtime spelling. `test/fixtures/fpp/`'s
`section-mapping.json` pins the mapping itself for exactly this reason.

The canonicalization rules both sides implement:

- Object member names sorted by UTF-16 code unit, duplicates rejected.
- No insignificant whitespace.
- ECMAScript `Number::toString` number formatting.
- Minimal string escaping: `"`, `\`, `\b`, `\f`, `\n`, `\r`, `\t`, and
  `\u00xx` for any other code point below `0x20`. Nothing else is escaped.
- UTF-8 output.
- Container nesting is bounded at 200. Only objects and arrays count toward
  the depth; a scalar does not.
- Invalid UTF-8 in a string or a member name is **refused**, not passed
  through. A definition containing it is `unsupported_definition_shape`.

The last two rules are stated because they are exactly where two independent
implementations drift without either one looking wrong. Sorting member names
by UTF-16 code unit is only a total order over well-formed UTF-8: map two
byte-distinct malformed names onto the same code unit and the sort has a tie
it cannot break, at which point the canonical bytes depend on which sort
algorithm each side happens to use. Refusing invalid UTF-8 removes the tie
instead of papering over it, and it fails in the visible direction: a refused
definition becomes an explicit unavailable observation, never a hash two sides
disagree about.

**Known open divergence, 2026-08-21.** The plugin's C++ currently counts
nesting depth per container while an earlier draft of the coordinator's Go
counted it per value, an off-by-one at the boundary, and the C++ does not yet
refuse invalid UTF-8. The coordinator now implements both rules as written
above. The plugin must adopt them before the two are byte-identical on
malformed input. Well-formed definitions, which is every definition FPP itself
produces, are unaffected and are covered by the shared fixtures.

**2026-09-01 correction to the depth divergence's stated cause.** The
paragraph above is wrong about *why* the two sides disagree at the depth
boundary. Both implementations count only containers; neither ever counted
per value. `pkg/fppidentity/canonical.go`'s depth check has counted only
containers since the file's own introduction (commit `73f3f07`, 2026-08-21,
"Freeze the FPP playlist-entry observation and brightness contracts", #38)
and has not been changed since: there is no earlier coordinator draft that
counted per value to find in this repository's history.

The real difference is **where** the bound is read. The coordinator's Go
parser (`parseArray`/`parseObject` in `pkg/fppidentity/canonical.go`) checks
`depth > maxDepth` immediately after incrementing, on the container's own
entry, before it looks at what the container holds. The plugin's C++ parser
(`native/src/json.cpp`) checks `depth_ > kMaxDepth` at the top of
`parseValue`, which runs once per element about to be parsed, not once per
container entered; `parseArray`/`parseObject` increment `depth_` themselves
without checking it. So a container's depth is only evaluated by the
`parseValue` call that parses its *first element*; a container with no
elements is never checked at all, at any depth.

As of this correction: the coordinator refuses 201 nested containers for
every document, whether the innermost container is empty or holds a value
(`test/fixtures/fpp/canonicalization.json`'s `depth-201-nested-arrays-empty-innermost`
and `depth-201-nested-arrays-scalar-innermost`). The plugin's C++, read
directly from `native/src/json.cpp` for this correction, accepts 201 nested
containers when the innermost container is empty and refuses when it is not,
matching the mechanism above. Invalid UTF-8 remains unrefused on the plugin
side; the coordinator refuses it, pinned by
`test/fixtures/fpp/canonicalization.json`'s `invalid-utf8-string-value` and
`invalid-utf8-member-name` cases, carried via that file's `inputHex` field
because a JSON string cannot hold a byte that is not valid UTF-8. Neither
divergence is closed by this correction or its fixtures alone: the plugin fix
for both is a separate, later change.

### 1.4 Unavailable observations

Identity never silently degrades to filename matching. When the plugin cannot
establish identity it sends an explicit unavailable observation, and the
coordinator stores and streams it rather than refusing it. The wire spellings
are:

| Wire value | Plugin `IdentityUnavailable` |
|---|---|
| `missing_instance_uuid` | `kMissingInstanceUuid` |
| `missing_playlist_name` | `kMissingPlaylistName` |
| `missing_definition` | `kMissingDefinition` |
| `unsupported_definition_shape` | `kUnsupportedDefinitionShape` |
| `negative_position` | `kNegativePosition` |
| `truncated_identity_field` | `kTruncatedIdentityField` |

The C++ enum's human strings are not wire values. Any other value in
`unavailable` is refused as an invalid parameter.

An unavailable observation carrying a `playlistHash` or an `entryKey` is
refused. See §1.2 for why those two fields, and not the corroborating ones.

An unavailable observation whose `instanceUuid` is itself missing cannot be
attributed to an instance and is refused: `missing_instance_uuid` is
reportable only when some other identity input failed, never as the reason a
body arrived with no instance at all.

### 1.5 The sequence must survive a plugin restart

**The per-instance `sequence` is required to be persistent and monotonic
across plugin and host restarts.** This is a requirement this contract places
on the plugin, and the plugin owns writing the file that satisfies it. The
plugin's `SequenceState` already refuses to move backwards in memory and
already exposes `restore()`; nothing currently calls it, so today's sequence
resets to `0` on every `fppd` start. Under the rule below, a restart would
then make every subsequent observation a refusal, so the follow-up plugin
issue must persist the sequence before the ingestion path can be exercised
end to end.

What the coordinator does with a genuine regression:

- It refuses the observation with `409`, audits it, and leaves the stored
  latest observation untouched. It does not accept a lower sequence, and it
  does not silently re-anchor: silently accepting a regression is
  indistinguishable from accepting a replayed or forged observation.
- The refusal names the last accepted sequence for that instance, so an
  operator reading it can tell a restart apart from a genuine reorder.
- The stored per-instance sequence is cleared only by an explicit,
  authenticated operator action. That action does not exist yet and is
  deliberately out of scope here; it is specified in
  [TRACK-H-H2-SPEC](TRACK-H-H2-SPEC.md) section 5.1. The store method exists
  and no route reaches it, so today the only way to clear a row is direct
  database access on the coordinator host.

**That recovery route is a prerequisite for the plugin's sending half, not a
nice-to-have.** Nothing posts to this endpoint yet, so nothing can be wedged
today. The moment a plugin does post, a single observation carrying a wildly
high sequence, from a misconfigured host or a compromised credential, refuses
every later legitimate observation for that instance permanently. There is
also no binding between the authenticated principal and the `instanceUuid` it
reports for: `fpp:observe` authorizes reporting for any instance, because
ADR-024 decision 4 delivers action scoping and states plainly that target
scoping is not implemented. Both facts are recorded here so the follow-up work
inherits them rather than rediscovering them.

### 1.6 Ingestion behavior

In order:

1. Authenticate; refuse `401` when no credential resolves.
2. Check `fpp:observe`; refuse `403` naming the scope.
3. Bound the body at 16384 bytes; refuse `413` on overflow.
4. Decode, and canonicalize the raw body. Refuse `400` on malformed JSON,
   trailing content after the object, or a duplicate member name. A duplicate
   member name matters here and not merely as pedantry: a permissive decoder
   keeps the last `sequence` while a reader of the same bytes sees the first.
   A member the coordinator does not know is **ignored**, not refused. Its
   name is returned in the response's `ignoredFields` array, sorted, capped at
   eight, and absent when there were none. Refusing an unknown member made
   upgrade order fatal rather than merely wrong: a plugin sending a field its
   coordinator predates had every observation rejected, so the coordinator saw
   no entries and fired no Cues, and the symptom looked like a broken plugin.
   Reporting the names keeps what strict decoding bought, which is that a
   misspelled member is visible rather than silently dropped. **A plugin must
   not treat `ignoredFields` as a failure**: the observation was accepted.
5. Refuse `400` when `schemaVersion` is not `1`.
6. Refuse `400` when `instanceUuid` is absent or empty.
7. Refuse `400` when `action` is outside the fixed vocabulary, when
   `unavailable` is outside the §1.4 vocabulary, when `position` is negative,
   when a hash is present and is not 64 lowercase hex characters, or when
   `coalescedSincePreviousAcknowledged` or `sequence` is negative. Refuse
   `400` when `unavailable` is absent and any of `playlistName`,
   `playlistHash`, `position`, or `entryKey` is missing, and when
   `unavailable` is present and either `playlistHash` or `entryKey` is
   present.
8. When `unavailable` is absent, re-derive the entry key from
   `instanceUuid`, `playlistName`, `playlistHash`, `section`, and `position`
   and refuse `400` when it disagrees with the submitted `entryKey`.
9. Compare `sequence` against the last accepted sequence for that instance:
   - greater: accept.
   - equal and the canonical body is identical: **idempotent replay**, `200`,
     nothing stored, no change-stream update.
   - equal and the body differs: refuse `409`.
   - lower: refuse `409` (§1.5).
10. Store the complete observation as the latest state for that instance. The
    change stream renders the stored latest observation on its own pass and
    emits `fppPlaylistEntry.changed` when it differs from what it last sent,
    so several observations accepted between two passes collapse into one
    frame. That is current-state convergence, not a lost event: ADR-020 makes
    the stream non-resumable, and a client re-fetches the authoritative state
    rather than reconstructing it from frames.

Accepted observations are not written to the audit log; a per-entry audit
entry would flood it during an ordinary show. Every refusal from step 5
onward **is** audited, under the action `fpp.observe_playlist_entry`, with the
refusal reason. Steps 1 through 3 keep the coordinator's existing generic auth
and size-refusal behavior, which means a `401` or a `403` here is recorded
exactly as it is on every other write route and gets no ingestion-specific
audit entry of its own.

**Ingestion grants no execution authority.** Accepting plugin evidence says
only that FPP reported an entry. Track H applies Show, Playlist, Cue, and
active-show authorization after ingestion, and an accepted observation for a
non-active show must never activate anything.

### 1.7 Refusal vocabulary

| Case | Status | Problem type |
|---|---|---|
| No credential | 401 | `unauthorized` |
| Missing `fpp:observe` | 403 | `forbidden` |
| Body over 16384 bytes | 413 | `payload-too-large` |
| Malformed body, trailing content, duplicate member | 400 | `invalid-parameter` |
| Unsupported `schemaVersion` | 400 | `unsupported-observation-schema-version` |
| Missing `instanceUuid` | 400 | `invalid-parameter` |
| Invalid enum, hash, or position | 400 | `invalid-parameter` |
| Identity field missing with no `unavailable` | 400 | `invalid-parameter` |
| Derived identity present with `unavailable` | 400 | `invalid-parameter` |
| Derived entry key mismatch | 400 | `observation-entry-key-mismatch` |
| Reused sequence, different body | 409 | `conflict` |
| Sequence regression | 409 | `conflict` |

### 1.8 Entry occurrence and `playlistLoop`

**Status: coordinator BUILT, plugin NOT BUILT.** The coordinator accepts
`playlistLoop` and uses it as the third term of the occurrence rule. No plugin
sends it yet, so §1's blanket "both sides are built" does not cover this
section.

Coordinator anchor: `playlistLoopChanged`. The plugin's half is that
repository's own assertion and is not verified from here.

An entry OCCURRENCE is one visit to one playlist entry. Repeat ticks inside a
visit belong to the same occurrence; a later visit to the same entry is a new
one. Track H derives its Cue activation identity from the occurrence, so two
ticks inside one visit dedup to a single dispatch while a second visit
dispatches again.

The entry key alone cannot separate those two cases. A playlist looping back
into an entry derives the identical key on its second visit, because the key is
a function of the entry's identity and not of when it was reached.

`playlistLoop` is what separates them. It carries FPP's own mainPlaylist pass
counter for the running playlist, verbatim, from the same callback that
supplies every other field in §1.2. It is `0` on the first pass and increments
once per completed pass. It is corroborating evidence and never identity: it is
not an input to either hash in §1.3, so an entry's key is unchanged by it.

The coordinator begins a new occurrence when the action is `start`, OR the
entry key differs from the last accepted one, OR `playlistLoop` differs from
the last accepted one. Any of the three is sufficient.

Three properties of that rule are load bearing:

- **Absent compares equal to absent.** A plugin that sends no `playlistLoop`
  behaves exactly as it did before this field existed, so no deployed plugin
  changes behavior when a coordinator that understands the field is installed
  in front of it.
- **`0` is a value, not an absence.** A plugin reporting the first pass and a
  plugin reporting nothing must not compare equal, which is why the field is
  omitted rather than sent as zero when the callback did not supply it.
- **The `start` term stays.** FPP 9 fires the playlist `start` action and the
  first term alone is sufficient there. FPP 10 never fires it
  (`Playlist::PlayImpl()` flips status before `Start()` reads it), so on that
  major the third term is what provides the property. Removing a correct term
  because another one now covers the common case would break the major the
  fleet currently runs.

**Upgrade order is not free: the coordinator goes first.** A coordinator that
predates this section refuses a body carrying an unknown field, and that
refusal rejects the whole observation rather than ignoring the member. So a
plugin that sends `playlistLoop` to such a coordinator has every observation
refused with `400`, and a coordinator receiving no observations activates no
Cues at all. Measured rather than argued: the identical body posts `200` without
the member and `400` with it, `json: unknown field "playlistLoop"`. Upgrading
the coordinator first is safe in both directions, because the field is optional
and an older plugin simply never sends it.

§1.6 step 4 no longer refuses an unknown member: it ignores it and names it in
the response's `ignoredFields`. That does **not** retire the ordering above for
this field. It changes what a coordinator carrying that change accepts, and
every coordinator built before it still refuses, so the coordinator-first order
stands until no coordinator predating that change is left in the fleet.

## 2. Brightness transition gain

**Status: coordinator BUILT, plugin BUILT.** Both sides serve this. Built is
not proven: nothing here claims it has run against a real FPP host. See 2.4 for
what remains.

Coordinator anchor: `handleFPPTransitionGain`. The plugin's half is that
repository's own assertion and is not verified from here.

### 2.1 The composition

RES-018 §1, restated so implementers do not have to re-derive it, now with
the weather gate term section 2.5 adds:

```text
effective output = weather gate closed ? 0 : round(ceiling * transition_gain / 100)
```

`ceiling` is 0–100 and is written by FPP's own schedule and operator command
path, through the plugin's registered `ShowMesh: Set Brightness Ceiling` FPP
Action. It is already implemented in the plugin.

`transition_gain` is 0–100 and is the coordinator's alone. It is **not**
reachable from any FPP Action, MQTT setter, or relative brighten/dim command.
Adding any second writer to it defeats the seam.

The weather gate is a third, independent term: open or closed, written only
by ADR-053's weather delay state. See section 2.5.

### 2.2 The coordinator-facing write

The night-session controller writes the transition gain by POSTing to the
resident plugin component, on the FPP host, at:

```text
POST /api/plugin-apis/showmesh/brightness/transition-gain
```

**That is the address. It is not the path the plugin registers**, and the two
are different strings on purpose. The plugin registers
`/showmesh/brightness/transition-gain` with its own major's web server; the
`/api/plugin-apis` prefix is what FPP's Apache requires to reach it.

The distinction is load bearing rather than cosmetic, because a route can
register successfully, appear in the host's own route table, and still answer
`404` to every real caller. Three facts make the single address above
trustworthy on both majors:

- **The plugin's server is unreachable from the LAN.** FPP 10's drogon binds
  `127.0.0.1:32322`. FPP 9's libhttpserver binds the same way, and
  `APIServer::Init` says so in its own comment ("so we only allow access via
  127.0.0.1"). Apache is not optional; it is the only way in.
- **Apache proxies plugin routes under exactly one prefix, and both majors
  carry the identical rule.** `RewriteRule ^plugin-apis/(.*)$
  http://localhost:32322/$1 [P]`. Everything else under `/api/` falls through
  to FPP's own PHP API, which answers `404` for an unknown path. A registered
  path that itself begins with `/api` still works, but only at an address
  carrying `/api` twice.
- **Both majors register on the server that prefix reaches**, despite sharing
  no registration API. FPP 10 uses Plugin API 6's `registerPluginApi()`. FPP 9
  uses `registerApis(httpserver::webserver*)`, and FPP calls it with the same
  `m_ws` that carries `/fppd`, `/commands` and `/models`. Two registration
  APIs, one address, and that is a fact about where each one lands rather than
  a coincidence of matching rewrite rules.

FPP 10 additionally forbids the internal namespace through the proxy
(`RewriteRule ^plugin-apis/internal(/.*)?$ - [F,L]`); FPP 9 has no such rule.
Nothing here goes near `internal`, but the difference is recorded so it is not
read as universal.

The plugin opens no listening socket of its own to serve this. See section
3.1 for the general rule this instance of a plugin-served route follows.

**This route is unauthenticated.** It accepts any caller reachable on the
show LAN. The accepted posture it matches is FPP's own: SECURITY.md and
RES-015 §7.4 record that fppd's web UI and API are unauthenticated by
default, and list that among the FPP exposures ShowMesh designs around rather
than files as its own defect. SECURITY.md's separate acceptance of cleartext
commands is a transport rule about what crosses the show LAN in the clear; it
does not by itself say a route may go unauthenticated, so it is not the
citation for this decision. A deferred bearer credential would refuse a
casual or accidental LAN caller, but the plugin would have to hold the
expected secret on the FPP host to check one, and RES-015 §7.4 records that
any ShowMesh credential placed on an FPP host must be treated as readable by
anyone who can reach that host's web UI, not only by someone with a shell on
it. See section 2.3 for what this season leaves unenforced.

Body:

| Field | Type | Required | Meaning |
|---|---|---|---|
| `schemaVersion` | integer | yes | Currently `1`. |
| `targetPercent` | integer | yes | 0–100 inclusive. Out of range is rejected, never clamped. |
| `fadeSeconds` | integer | yes | 0–86400 inclusive. `0` applies immediately. |
| `requestId` | string | yes | Caller-minted idempotency key; a repeat of the same id is a no-op. |

These are exactly `BrightnessEngine::setGain(targetPercent, fadeSeconds, now)`'s
inputs plus a version and an idempotency key. The plugin rejects out-of-range
input rather than clamping it, so a mistyped value is visible instead of
silently rounded into range.

The response reports the applied state so the caller has evidence rather than
an HTTP 200: `{"schemaVersion":1,"applied":true,"gainStart":100,"gainTarget":75,"fadeSeconds":30,"ceiling":60,"effectiveOutput":45}`.

### 2.3 What this contract forbids

- No absolute-brightness write. A ShowMesh write that could restore an old
  ceiling is forbidden by Track F and by RESTING-MODE §7.3.
- No relative brighten/dim. A relative adjustment applied twice is a
  different value; MultiSync carries full state for the same reason.
- No FPP Action, MQTT topic, or command binding for the gain.
- A ceiling change during a gain fade takes effect immediately, and a later
  gain of 100 reveals the current ceiling, never a cached earlier one.

**Accepted limitation:** section 2.2's route is unauthenticated, so this
single-writer rule is a contract the night-session controller alone is
expected to honor, not one the route can enforce against another caller on
the show LAN this season. Any host on that LAN can POST a competing value.
This is an accepted risk for this season, not an oversight: a bearer
credential that would let the route refuse other callers is deferred to a
future season as a separate tracked item, per owner ruling (2026-08-23). The
rule stays written here as the intended contract; only its enforceability is
what this season gives up.

### 2.4 What remains unbuilt

Both sides are built and neither is proven. The coordinator serves
`POST /fpp/{instanceId}/brightness/transition-gain` (`handleFPPTransitionGain`)
and reaches it from `showmeshctl fpp set-transition-gain`. Track F's readiness
check no longer refuses a cue requiring compositional brightness: it warns
instead, which is why it is now `nightCheckBrightnessCompositionUnverified`
rather than a name asserting the composition is unbuilt.

What remains is evidence, not code. RES-018 §8's decisive mid-fade case has not
been observed against a real FPP host, so nothing here reports that the
composition behaves correctly on hardware. The readiness check warns for exactly
that reason and stays until that observation exists.

### 2.5 The weather gate

**Status: coordinator BUILT, plugin not built from here.** The coordinator
serves its half and dispatches against the address below. The plugin's half
is that repository's own assertion and is not verified from here.

Coordinator anchor: `WeatherDelayEnforcer`. Its own client is
`internal/coordinator/fppcommand`'s `SetWeatherGate`/`ReadWeatherGate`.

[ADR-053](../decisions/ADR-053-weather-delay.md) decision 5: the plugin's
brightness engine gains a third term beside the ceiling and the transition
gain, a gate that is either open or closed, written only by the weather delay
state. Closed forces every channel to zero, including channels outside the
configured apply and exclude ranges. The gate's own write ignores both,
unlike a transition-gain fade, which respects them. See section 2.1's updated
composition line.

The route, same address shape as section 2.2's and reached the same way, for
both the write and the read:

```text
POST /api/plugin-apis/showmesh/brightness/weather-gate
GET  /api/plugin-apis/showmesh/brightness/weather-gate
```

Write body: `{"closed": bool, "revision": integer}`, revision non-negative.
**A coordinator write always applies.** Unlike section 2.2's requestId, this
route accepts no idempotency key and refuses nothing: the stored revision
becomes `max(stored + 1, given)`, so a write from a coordinator whose own
revision counter is behind the plugin's still lands, and one from a
coordinator that has moved ahead sets the plugin to match it. The read takes
no body.

Both routes answer with the plugin's own full brightness state document,
carrying three fields this contract adds to it:

- `weatherGateClosed` (bool)
- `weatherGateRevision` (integer)
- `effectiveOutputPercent` (0–100), the composition in section 2.1: 0
  whenever the gate is closed, `round(ceiling * transition_gain / 100)`
  otherwise.

A peer's own reported gate state is adopted only under this rule: a
peer-reported **closed** gate is adopted at a strictly greater revision than
the plugin's own stored one; a peer-reported **open** gate is never adopted
from a peer at all, only from a coordinator write. A delay may reach a player
by more than one path (ADR-053 decision 8); a stale, replayed, or
out-of-order closed report must never darken a player after a resume, and an
open report from a source that is not the coordinator's own write must never
end a delay a player is still supposed to be holding.

Only a resume opens a gate. The coordinator's enforcement loop closes gates
while a delay is active and never opens one. A gate can be closed by a start
the coordinator never saw, so a gate that reads closed while no delay is
active is reported in `heldPlayers` on `GET /api/v1/weather-delay` and stays
closed until `POST /api/v1/weather-delay/resume`, which opens every player's
gate each time it is called.

Persistence: the gate is written to both of the plugin's own record
generations on every gate change, so the two agree. If a crash leaves them
disagreeing, a readable primary wins. If both records are unreadable, the gate **restarts open**, the same posture every
other unconfigured or freshly-initialized brightness value takes, and the
coordinator's own enforcement loop, running unconditionally, closes it again
within one tick of finding the stored weather-delay state active. This is
why the loop exists rather than a one-time close on start: persistence on
the FPP host is not guaranteed, and the loop is what makes that gap survive
a corrupted or missing record without an operator noticing a lit player.

An FPP host whose plugin predates this section answers `404` to both routes.
The coordinator reports that instance as unable to be held dark by ShowMesh;
it is never counted as either open or closed.

Related: [ADR-053](../decisions/ADR-053-weather-delay.md).

### 2.6 The brightness state read

The coordinator's `fppbrightness` collector polls this route every five
seconds to fill the four `fpp.brightness.*` signals. It changes nothing on the
plugin.

```text
GET /api/plugin-apis/showmesh/brightness
```

The plugin registers `/showmesh/brightness`; the address above is the one a
caller uses, for the reasons section 2.2 gives. The route takes no body and no
credential, on the same accepted posture as section 2.2's route. It never
refuses a request, so a well-formed call always gets `200` with
`application/json`.

Response body, every field always present:

| Field | Type | Meaning |
|---|---|---|
| `schemaVersion` | integer | Currently `1`. |
| `ceiling` | integer | The ceiling now, 0 to 100, rounded, as any fade in progress has moved it. |
| `transitionGain` | integer | The transition gain now, 0 to 100, rounded, as any fade in progress has moved it. |
| `effectiveOutput` | integer | The percentage actually applied to channel data: 0 whenever the weather gate is closed, `round(ceiling * transitionGain / 100)` otherwise. |
| `fadeActive` | boolean | True while the ceiling or the transition gain is still fading. |
| `weatherGateClosed` | boolean | True while the weather gate holds output at zero (section 2.5). `effectiveOutput` is then 0 whatever the ceiling and gain read. |
| `updatedAtMillis` | number | The plugin's clock, in milliseconds, at the moment the snapshot was taken. |

Example: `{"schemaVersion":1,"ceiling":60,"transitionGain":75,"effectiveOutput":45,"fadeActive":false,"weatherGateClosed":false,"updatedAtMillis":1790000000000}`

Coordinator behavior:

- A `200` fills `fpp.brightness.ceiling`, `fpp.brightness.transition_gain` and
  `fpp.brightness.effective_output` with the integers (unit percent) and
  `fpp.brightness.fade_active` with the boolean.
- A `404` means the plugin predates this route or is not installed. Each of the
  four signals then reads unsupported, with the reason "This player does not
  report brightness. Install or update the ShowMesh plugin to 0.2 or later."
- Any other status, a transport error, a body over 64 KiB, or a body that is
  not JSON reads collection failed for all four signals, with the cause as the
  reason.
- A field missing from an otherwise valid body reads not collected for that
  signal alone.
- A plugin that cannot render the document must answer `500`, never `200` with
  zeros. The collector also treats a `200` whose `updatedAtMillis` is `0` as
  collection failed for all four signals, with the reason "This player's
  ShowMesh plugin could not read its brightness. Check the plugin on that
  player.", so a plugin that still sends the all-zero fallback is not read as
  a real ceiling of 0. A body with no `updatedAtMillis` is read as before.
- The collector decodes `ceiling`, `transitionGain`, `effectiveOutput`,
  `fadeActive` and `updatedAtMillis` only. `schemaVersion` and `weatherGateClosed`
  are informational for now: nothing in the coordinator reads them, and the
  UI does not show `weatherGateClosed`. The weather delay's own gate routes
  in section 2.5 remain how the coordinator reads and writes the gate.

## 3. Playlist definition publication

**Status: coordinator BUILT, plugin BUILT,** except §3.9, which is built on both
sides as well but not on the same evidence, so it carries its own status line.
Read that line for §3.9 rather than inferring anything about it from this one.

Coordinator anchor: `handlePostFPPPlaylistDefinition`. The plugin's half is that
repository's own assertion and is not verified from here.

Frozen 2026-08-22 for Track H seam H2. Section 1 gives the coordinator a
playlist hash and an entry key. Neither says what the playlist contains, so
neither can be authored against: an operator binding a ShowMesh Cue to FPP
entry 3 needs to see that entry 3 is `Thriller.fseq` before the show, not
discover it when FPP plays it.

This section fixes how the definition behind a hash reaches the coordinator.
**The plugin posts it.** The same principal, the same credential, and the same
`fpp:observe` scope as section 1.

### 3.1 Why the plugin sends it rather than the coordinator fetching it

The hash in section 1.3 is SHA-256 over the RFC 8785 canonicalization of the
definition the plugin read. Today that read is a local file: the resident
worker opens `FPP_DIR_PLAYLIST/<name>.json` and hashes those bytes. It is not
FPP's REST API, and it is not the `Json::Value` FPP hands the playlist
callback, which the plugin deliberately mines for three bounded fields and
otherwise discards.

If the coordinator fetched the definition from FPP's REST API instead, it
would be canonicalizing a different read of a different representation. FPP's
`GET /api/playlist/{name}` re-serializes through FPP's own JSON layer, and
whether it adds, drops, or recomputes a member relative to the stored file is
not measured anywhere in either repository. Canonicalization removes
formatting differences; it does not remove a field FPP's API injects.

Every binding in Track H is keyed on the hash. A hash the coordinator computed
from a second source is a hash no observation will ever match, and the failure
would arrive on show night looking like a permanent unexplained mismatch
rather than like the wrong import path it actually was. So the rule is: **the
bytes the plugin hashed are the bytes the coordinator imports.**

The plugin publishes rather than serving a read for one further reason, and it
is narrower than it once looked. The plugin opens no listening socket of its
own; the general rule is that it **may** register narrow, idempotent,
evidence-returning inbound routes on fppd's own web server — on FPP 10
through Plugin API 6's `registerPluginApi`, on FPP 9 through the
libhttpserver adapter. That is the mechanism section 2.2's brightness route is
DESIGNED to use, and which nothing in the plugin uses yet (see 2.4). It
never opens a second listener, never proxies FPP, and never serves a value
the coordinator has not verified. That general permission does not, by
itself, favor a read route for the definition: the plugin already needs an
outbound client for section 1's observations, and one more outbound POST is a
small addition to work already required, while a definition-read route would
still leave the coordinator fetching through a representation the plugin does
not control between hash and read. Push keeps the bytes the plugin hashed and
the bytes the coordinator imports identical without adding that risk; it is
not something the plugin is forced into by an inability to serve routes at
all.

### 3.2 The route

```text
POST /api/v1/integrations/fpp/playlist-definitions
```

Guarded by `fpp:observe`. Authentication and the scope check run before the
body is parsed, as in section 1.1. The body is bounded at **1048576 bytes**
and a larger body is refused with `413` before parsing. This bound is two
orders of magnitude above section 1.2's, because unlike an observation this
body does carry the complete definition.

### 3.3 Request body, schema version 1

```json
{
  "schemaVersion": 1,
  "instanceUuid": "M4-7840e12f81da4191c0d00fbb6a889314",
  "playlistName": "Halloween Main",
  "playlistHash": "<64 lowercase hex>",
  "definition": { },
  "capturedAtMillis": 1755900000000
}
```

| Field | Type | Required | Meaning |
|---|---|---|---|
| `schemaVersion` | integer | yes | Currently `1`. Any other value is refused. |
| `instanceUuid` | string | yes | The same persistent FPP UUID section 1.2 reports, from FPP's `SystemUUID` setting. |
| `playlistName` | string | yes | FPP playlist name. |
| `playlistHash` | string | yes | SHA-256 over the canonicalization of `definition`, section 1.3. Lowercase hex. |
| `definition` | object | yes | The complete playlist definition, as a parsed JSON value. No member removed. |
| `capturedAtMillis` | integer | yes | When the plugin read this definition, epoch milliseconds. |

`definition` is the JSON value itself, not a string holding JSON. The
coordinator canonicalizes what it received and refuses the request when the
result does not hash to the declared `playlistHash`.

That check is the load-bearing one. It makes the transport irrelevant, since a
proxy that reformats the body still canonicalizes to the same bytes, and it
makes the store self-verifying: every definition is filed under a hash the
coordinator computed itself. A caller cannot install a definition under
someone else's hash, so the worst a forged post can do is add an entry nothing
references.

### 3.4 Ingestion behavior

In order:

1. Authenticate; refuse `401` when no credential resolves.
2. Check `fpp:observe`; refuse `403` naming the scope.
3. Bound the body at 1048576 bytes; refuse `413` on overflow.
4. Decode. Refuse `400` on malformed JSON, trailing content after the object,
   or a duplicate member name, for section 1.6's reasons. A member the
   coordinator does not know is **ignored**, not refused, mirroring section
   1.6 step 4's identical fix for the identical hazard: refusing it made
   upgrade order fatal, since a plugin sending a new field to a coordinator
   that predates it would have every definition rejected, and the
   coordinator would then see observations referencing definitions that
   never landed. Its name is returned in the response's `ignoredFields`
   array, sorted, capped at eight, and absent when there were none. **A
   plugin must not treat `ignoredFields` as a failure**: the definition was
   accepted.
5. Refuse `400` when `schemaVersion` is not `1`.
6. Refuse `400` when `instanceUuid` or `playlistName` is absent or empty, when
   `playlistHash` is not 64 lowercase hex characters, when `definition` is
   absent or is not an object, or when `capturedAtMillis` is negative.
7. Canonicalize `definition` and refuse `400` with
   `definition-hash-mismatch` when its SHA-256 disagrees with `playlistHash`.
   A definition the coordinator's own canonicalizer refuses, for invalid UTF-8
   or excessive nesting, fails here too and is the same refusal: the
   coordinator never stores a definition it could not canonicalize.
8. Store under the key `(instanceUuid, playlistHash)`. A repeat of a key
   already held is idempotent `200` and stores nothing, because the key is the
   content. `playlistName` and `capturedAtMillis` on a repeat are ignored
   rather than overwriting the stored ones: the first report of a given
   content is the one with provenance.

There is no sequence and no ordering. Content addressing removes the need for
one, which also removes section 1.5's wedging hazard from this route entirely:
a definition carrying a wildly wrong value cannot refuse later legitimate
posts, because there is no counter for it to poison.

A store that actually inserted is audited, under the action
`fpp.publish_playlist_definition`. Unlike an accepted observation, this
happens once per playlist revision rather than once per entry, so it does not
flood, and it gives an operator a dated record that the FPP playlist changed.
An idempotent repeat is not audited. Every refusal from step 5 onward is
audited with its reason.

### 3.5 Refusal vocabulary

| Case | Status | Problem type |
|---|---|---|
| No credential | 401 | `unauthorized` |
| Missing `fpp:observe` | 403 | `forbidden` |
| Body over 1048576 bytes | 413 | `payload-too-large` |
| Malformed body, trailing content, duplicate member | 400 | `invalid-parameter` |
| Unsupported `schemaVersion` | 400 | `unsupported-definition-schema-version` |
| Missing or malformed identity field | 400 | `invalid-parameter` |
| Definition does not hash to `playlistHash` | 400 | `definition-hash-mismatch` |

### 3.6 Reading definitions back

```text
GET /api/v1/integrations/fpp/playlist-definitions
GET /api/v1/integrations/fpp/playlist-definitions/{instanceUuid}/{playlistHash}
```

Both under `observation:read`, matching every other FPP read surface. The list
returns metadata only (instance, playlist name, hash, captured and received
times, entry count, and whether a stored `show.playlist` references it); the
second returns the stored definition. The list exists so an operator can
choose a playlist to import without downloading every definition on the host.

### 3.7 When the plugin posts

The plugin posts a definition whenever it holds one whose hash it has not
already posted successfully, on all three of these occasions:

1. **When it resolves an entry identity.** The definition behind that hash
   must reach the coordinator before, or alongside, the first observation
   citing it. An observation is still accepted when the definition has not
   arrived, per section 1 unchanged; Track H holds that binding as having no
   definition rather than activating it.
2. **At worker start, for every playlist definition on the host.** The
   coordinator otherwise holds nothing until FPP plays something, and
   authoring happens in the afternoon with FPP idle. The plugin already reads
   this directory; enumerating it is the same read.
3. **On a bounded re-scan, no more often than every 60 seconds.** An operator
   who edits a playlist and does not play it would otherwise leave the
   coordinator holding the previous revision until the next plugin restart.
   Skipping a file whose size and modification time are unchanged keeps this
   cheap; no filesystem watch is required.

The plugin tracks which hashes it has posted successfully in memory only. A
restart re-posts, and the coordinator answers idempotently, so nothing is lost
by not persisting that set. This is deliberately weaker than section 1.5's
requirement on the observation sequence, and it is safe for the same reason
the route needs no sequence: the key is the content.

Retries use the same bounded backoff and the same visible local status as
section 1's observation delivery.

### 3.8 What the plugin had to add

None of this existed when this section was frozen on 2026-08-22. All of it
shipped afterwards; verified against the plugin runtime on 2026-09-08 and
listed here with where it landed, so the next reader does not re-derive it:

- Retain the canonical definition past the hash call.
  `resolveEntryIdentity()` computes `IdentityResolution::canonicalDefinition`,
  which now reaches `publishDefinition()` rather than being discarded.
- The outbound HTTP client section 1 already requires, carrying this second
  route. `CoordinatorClient::publishDefinition()` posts to `kDefinitionPath`.
- Enumeration of the playlist directory at start, and the bounded re-scan.
  The start-up sweep and `kDefinitionRescanIntervalMillis` (60 s) in the
  runtime's worker.
- The posted-hash set section 3.7 describes is `heldDefinitions_`, keyed on
  instance UUID and playlist hash, in memory only, so a restart re-posts.

Still true, and still a constraint rather than a task: no second listener, and
no change to the callback thread's bounded copy-and-return. The
no-inbound-route half of that sentence no longer holds, independently of
anything in this section: the plugin serves section 2.2's brightness route on
both majors, and §3.9 specifies a second inbound route for this section. Both
register on fppd's own web server, which is exactly what section 3.1 permits;
neither opens a listener of its own, and neither runs on the callback
thread.

### 3.9 The coordinator-triggered republish

**Status: coordinator BUILT, plugin BUILT, NOT PROVEN ON HARDWARE.** The plugin serves
this route and the coordinator calls it: a client for the plugin address
below, an operator route
(`POST /api/v1/fpp/{instanceId}/playlist-definitions/republish`, behind
`fpp:command`), its OpenAPI entry, and a `showmeshctl` verb. Built is not
proven: the evidence on both sides is bench evidence, unit tests against HTTP
fakes, and neither half has run against a real FPP host. Read the prose below
as the contract it has always been, not as a report of observed behavior.

Coordinator anchor: `handleFPPDefinitionRepublish`. The plugin's half is that
repository's own assertion and is not verified from here.

Be precise about the size of the win, because it is bounded. Section 3.7's
re-scan already recovers an edited playlist by itself: the sweep re-reads the
host's playlist definitions, and the skip that suppresses a repeat of one is
content addressed, so a playlist the operator just edited hashes
differently, is not suppressed, and reaches the coordinator within the 60
second interval with nobody asking. **This route makes no previously
impossible thing possible.** For an edit it collapses a bounded wait to now,
and that is the whole of it: the operator who has just changed a playlist on
the FPP host, wants ShowMesh to show the new revision before they carry on
authoring, and today waits.

There is a second case, and that one does not self-heal. If the coordinator
loses, or never durably stored, a definition it once accepted, the plugin
still holds that hash in `heldDefinitions_`, the content-addressed skip fires
on every later sweep because the content has not changed, and only a plugin
restart clears it. No action available from the coordinator repairs that
today. That is the repair this route exists for; the faster edit is the
convenience it also buys.

**The address.** The coordinator posts to the resident plugin component, on
the FPP host, at:

```text
POST /api/plugin-apis/showmesh/playlists/republish
```

**That is the address. It is not the path the plugin registers**, and the two
are different strings for the reason section 2.2 sets out in full. The plugin
registers `/showmesh/playlists/republish` with its own major's web server; the
`/api/plugin-apis` prefix is what FPP's Apache requires to reach it. Section
2.2's three facts apply here unchanged: both majors bind their own HTTP server
to `127.0.0.1` only, Apache proxies plugin routes under exactly one prefix,
and both majors register on the server that prefix reaches. A registered path
that itself begins with `/api` still works, but only at an address carrying
`/api` twice. The distinction is load bearing rather than cosmetic, because a
route can register successfully, appear in the host's own route table, and
still answer `404` to every real caller.

The plugin opens no listening socket of its own to serve this. It is the
second route to use the general permission section 3.1 states, not an
exception to it.

**This route is unauthenticated,** on section 2.2's accepted posture and for
its reasons. Any host on the show LAN can post to it. What such a caller can
cause is narrow: a republish sends the plugin's own definitions, read from the
host's own playlist files, under hashes the coordinator verifies for itself
(§3.4 step 7). The worst an accidental or hostile caller achieves is a sweep
that arrives early and a set of idempotent `200`s.

Body:

| Field | Type | Required | Meaning |
|---|---|---|---|
| `schemaVersion` | integer | yes | Currently `1`. Any other value is refused. |
| `requestId` | string | yes | Caller-minted idempotency key, non-empty. A repeat of the same id applies nothing and answers with the state as it stands. |

The body is bounded at **4096 bytes**; a larger body is refused before it is
parsed. Two small fields never need more, and the bound exists because the
route is unauthenticated: an unbounded read on an FPP host during a show is a
risk no body size justifies. The plugin remembers only the last applied
`requestId`, exactly as section 2.2's route does, so a caller that alternates
between two ids applies both; the key defends a retried request, not a
replayed one. The key is checked after the body validates, not before, because
a malformed body carrying an already-seen id is still malformed.

**What an applied request does.** Exactly three things, and the third is a
prohibition.

1. **It records that a sweep is owed,** and that record is the whole of its
   effect on the sweep. The worker observes it on its next pass and sweeps
   regardless of how recently it last swept, so the definitions go out now
   rather than at the end of `kDefinitionRescanIntervalMillis`. **The route
   writes no cadence state of its own.** The cadence variables belong to the
   worker thread, which is the only thread that has ever written or read
   them, and the inbound handler does not touch them: an HTTP handler that
   reaches in to reset the last-sweep time is a data race on state that has
   never needed a lock, in a plugin whose route-safety argument rests on the
   handler being synchronous and self-contained. The owed-sweep record is the
   one piece of state the two threads share for this, it is the same record
   `sweepPending` reports, and it is the worker that clears it when the sweep
   it caused has completed.

   The route does not run the sweep on the HTTP thread either: the sweep
   reads the playlist directory, hashes every definition, and posts each one
   with the retry policy's full backoff budget, which is unbounded work to
   hold an inbound request open across, and it must not run twice
   concurrently. The route makes a sweep due; the worker performs it.
2. **It clears `heldDefinitions_`,** synchronously, on the HTTP thread, under
   the lock that set already has. This is the deliberate difference from item
   1, and the line between them is which state is already guarded: the held
   set is shared state with a mutex around every read and write of it, so one
   more writer is a use of that lock rather than a new hazard, and clearing it
   in place is what lets the response count what it dropped. The cadence is
   unguarded worker-thread state, so it is reached only through the owed-sweep
   record. Clearing the held set sends a definition the coordinator lost again
   even though its hash has not changed. This is the whole of the repair.
   Without it the route would accelerate only the case that already recovers
   on its own, and would leave the case that cannot recover exactly where it
   was. Any other suppression the sweep carries is cleared with it:
   if an implementation takes section 3.7's option to skip a file whose size
   and modification time are unchanged, a republish must clear that too, or
   the definition the coordinator lost is skipped before its hash is ever
   reconsidered.
3. **It MUST NOT clear `refusedDefinitions_`.** A terminal refusal is one
   whose reason cannot change until the plugin restarts: the plugin's own JSON
   for that definition was unusable, or the coordinator rejected the hash it
   declared, §3.5's `definition-hash-mismatch`. Re-sending those bytes gets
   the identical answer. Clearing the set would turn one operator action into
   a retry loop against a condition that will not improve, spending the retry
   policy's backoff ahead of the observations and definitions that would
   succeed. An implementation that clears both sets because they sit beside
   each other is wrong, and this is the sentence it is wrong against.

**The response.** It reports what the plugin did and what it holds at the
moment it answers, never a bare `200` and never an echo of the request:

```json
{"schemaVersion":1,"applied":true,"definitionsCleared":6,"definitionsHeld":0,"definitionsRefusedTerminally":1,"sweepPending":true}
```

Every field is knowable when the response is written, and each is there
because it answers a question the operator actually has:

| Field | Type | What it is, and why the plugin can know it |
|---|---|---|
| `schemaVersion` | integer | Always `1`. |
| `applied` | boolean | `true` when this request cleared state, `false` on a repeat of the last applied `requestId`. The plugin holds that id itself. |
| `definitionsCleared` | integer | How many `(instanceUuid, playlistHash)` pairs this request dropped from `heldDefinitions_`, counted in the same critical section that clears the set. `0` on a repeat, which cleared nothing. |
| `definitionsHeld` | integer | How many pairs that set holds as the answer is written, read in that same critical section. `0` immediately after an applied clear. On a repeat it is how many the worker has already re-sent and had accepted, so a repeat reports progress rather than an echo. |
| `definitionsRefusedTerminally` | integer | How many pairs are held in `refusedDefinitions_` and were deliberately not cleared. This is the count of playlists the republish will not re-send, which is what an operator needs when the playlist they were chasing is still missing afterwards. |
| `sweepPending` | boolean | Whether a sweep is owed and has not yet completed since this republish. Always `true` on an applied answer. On a repeat it is the current value, so polling the same `requestId` is how a caller learns the sweep finished. It must be readable by the HTTP handler without touching worker-thread-only state. |

**What the response deliberately does not carry is a count of definitions the
coordinator accepted.** The sweep is asynchronous and runs on the worker
thread; when this route answers, not one of its posts has been attempted, so
any acceptance count would be a guess or a number from the previous sweep
presented as this one's. `sweepPending` is the strongest honest claim
available at response time: the sweep is owed, not done. What actually arrived
is read from the coordinator through section 3.6, which is authoritative
because the coordinator computed those hashes itself, and `definitionsHeld` on
a later repeat is the plugin's own view of the same progress.

**Refusals** answer `400` with `{"schemaVersion":1,"applied":false,"error":"..."}`,
matching the refusal shape section 2.2's route already serves: a body that is
not valid JSON or not an object, a `schemaVersion` other than `1`, a missing,
non-string, or empty `requestId`, a body over the bound, and a plugin not
configured to publish definitions at all. This route has no `413` and no
problem-type vocabulary, unlike sections 1 and 3.2. That is a deliberate
difference rather than an omission: those are coordinator routes and answer in
the coordinator's own refusal vocabulary, while this one is served by the
plugin and answers in the plugin's, which is one shape for every refusal.

Nothing here grants execution authority. The route writes nothing to FPP,
alters no definition, and cannot cause the plugin to publish a definition it
did not itself read from the host and hash.

### 3.10 What this section does not do

- It does not let the coordinator write anything to FPP. §3.9 does give the
  coordinator one write to the plugin, and the whole of its effect is to make
  the plugin re-send definitions it has already read from the host and hashed:
  it sets no FPP state, alters no definition, and adds no value the plugin did
  not derive from the host's own files.
- It does not give the coordinator a second source of entry identity, and
  §3.9 does not add one either. The
  entry key still comes from section 1.3, derived from the same five fields.
- It does not make a definition an observation. It carries no sequence, is not
  ordered against anything, and grants no execution authority. Track H applies
  Show, Playlist, Cue, and active-show authorization to a binding built from
  it, exactly as section 1.6 requires for an observation.

## 4. Shared fixtures

**Status: coordinator BUILT, plugin BUILT.** The files exist and both sides
consume them.

Coordinator anchor: none. This section names no coordinator symbol; it describes
files consumed by both repositories.

`test/fixtures/fpp/` holds plain JSON data files, consumable by any language.
They are deliberately not a Go package and not a shared module: the plugin
repository is Apache-2.0 with no Go module dependency on the coordinator and a
C++ core that links no third-party library, so a fixture it cannot copy as
data is a fixture it cannot use.

See [`test/fixtures/fpp/README.md`](../../test/fixtures/fpp/README.md) for the
file format and the case list. The coordinator's own tests consume the same
files, so a fixture that drifts from the implementation fails on this side
before the plugin ever sees it.

## 5. Fallback activation

**Status: §5.1 through §5.11 coordinator BUILT, node BUILT, plugin NOT BUILT,
NOT PROVEN ON HARDWARE. §5.12 through §5.17 coordinator BUILT, plugin NOT
BUILT, NOT PROVEN ON HARDWARE. §5.18 coordinator BUILT, plugin NOT BUILT,
NOT PROVEN ON A REAL FPP PLAYER.** This section is the frozen shape for
[Track J](TRACK-J-fpp-fallback.md) step J3 and the wire the plugin's step J4
builds against. §5.12 through §5.17 are the shape for step J5, with the
owner's rulings of 2026-10-05 written in. They ask nothing new of a node.

Coordinator anchors: `handlePutFallbackExecutorKey`, and for §5.15 and §5.16
`handlePutFallbackState` and the `fallbackhold` package. Node anchor:
`handleFallbackActivation`. The plugin's half is that repository's own
assertion and is not verified from here.

[ADR-048](../decisions/ADR-048-signed-fpp-fallback-program.md) decision 3
gives a node a narrow route that accepts one thing: an activation that a
coordinator-signed fallback program already authorized, delivered by the FPP
host that program was built for. Per owner ruling (2026-10-05) pairing is the
enrollment. There is no separate authorize step and no secret is ever sent to
a node:

1. A paired plugin creates an Ed25519 key pair and registers the public key
   with the coordinator over its paired connection (§5.2).
2. The coordinator puts that public key in the signed fallback program it
   publishes for that FPP host (§5.3).
3. The plugin hands its installed program to each node the program names
   (§5.5). The node verifies it with the coordinator key it pinned at
   enrollment and keeps it.
4. During confirmed coordinator loss the plugin signs one activation request
   per matched entry and target node (§5.6). The node accepts it only when the
   signature verifies against the key inside the program it holds.

### 5.1 Key algorithm and encodings

One algorithm and one construction, the same ones §4's fixtures and the
program signature already use, so the plugin needs no new primitive beyond
signing:

| Item | Value |
|---|---|
| Algorithm | Ed25519 (RFC 8032), pure, no prehash and no context |
| Public key | The raw 32 bytes, RFC 4648 standard base64 with padding (44 characters) |
| Signature | The raw 64 bytes, RFC 4648 standard base64 with padding (88 characters) |
| Signed bytes | The RFC 8785 (JCS) canonical UTF-8 bytes of one JSON object, named per route below |

The verifier canonicalizes the JSON object it received and checks the
signature over those bytes. It never re-serializes from its own types first.
A signer therefore signs the canonical bytes of exactly the object it sends,
and any member order or whitespace on the wire is acceptable.

The private key never leaves the FPP host. The plugin stores it beside its
pairing token with the same file permissions and never sends it anywhere.

### 5.2 Executor key registration

```
PUT /api/v1/fallback-programs/{fppInstanceId}/executor-key
Authorization: Bearer <pairing token>
Content-Type: application/json
```

`fppInstanceId` is the FPP instance UUID the plugin already uses on
`GET /api/v1/fallback-programs/{fppInstanceId}`.

**Authorization.** The token must carry `fpp:fallback`, which the pairing
token already does, and the caller must be the plugin paired as the FPP player
whose instance UUID is `fppInstanceId`. The coordinator knows that link from
its own reads of the player: pairing names the plugin by the player's
configured id, and the coordinator records the instance UUID it reads from
that player. Any other caller, including an administrator and a plugin paired
as a different player, is refused with `403`. A plugin whose player the
coordinator has not yet read an instance UUID from is refused with `409`:
it may be the right plugin, pairing again would not help, and the cause is
that the coordinator has not reached the player. An unauthenticated caller
gets `401`.

**Body.** At most 4 KiB. Unknown members are refused.

| Member | Type | Meaning |
|---|---|---|
| `publicKey` | string, required | The executor public key, encoded per §5.1 |

**Responses.**

| Status | When | Body |
|---|---|---|
| `200` | The key is stored, or was already stored | See below |
| `400` | The body is malformed, has an unknown member, or `publicKey` is not 32 bytes of standard base64 | Problem document, `invalid-parameter` |
| `401` | No valid token | Problem document |
| `403` | The token lacks `fpp:fallback`, or the caller is not the plugin paired as the player with this instance UUID | Problem document, `forbidden` |
| `409` | The caller is a paired plugin, and the coordinator has not yet read an instance UUID from its player | Problem document, `conflict`. Detail: "The coordinator has not read this FPP player's identity yet, so it cannot accept the plugin's key. Check that the coordinator can reach the player." |

The `200` body:

| Member | Type | Meaning |
|---|---|---|
| `serverTime` | string | RFC 3339 |
| `fppInstanceUuid` | string | The path value |
| `publicKey` | string | The key now stored |
| `registeredAt` | string | RFC 3339, when this key was first stored |
| `changed` | boolean | `true` when this call stored a first key or replaced a different one |

**Behavior.** The route is idempotent. Registering the stored key again
changes nothing and answers `changed: false`. Registering a different key
replaces the stored one: there is one executor key per FPP host, and rotation
is a second registration. A first or changed key makes the coordinator rebuild
and republish that host's program at once, because the key is program content
(§5.3). The coordinator writes one audit entry, action
`fallback.executor_key.register`, for a first or changed key. A `409`
refusal also leaves a trace on the coordinator: a warning in its log naming
the player's configured id, and an audit entry under the same action whose
outcome reason starts with `refused:`.

**What the plugin does.** It creates the key pair once, when it holds a
pairing token and no key pair. It registers on every start and after every
pairing, because the call is idempotent. On a `409` it tries again each time
it next fetches its program, because the coordinator will accept the key once
it has read the player's instance UUID. A `403` does not clear by waiting. A plugin that was paired before this
contract existed registers on its next start and is not paired again. It
treats its executor key as usable only when the installed program's
`executorPublicKey` equals its own public key. Until then it does not send an
activation.

### 5.3 What the signed program gains

The program stays `schemaVersion` 1 and gains two optional members. Both are
covered by the program's `revision`, so a change to either publishes a new
revision with a new `packageId`.

| Member | Type | Meaning |
|---|---|---|
| `program.executorPublicKey` | string | The executor public key registered for this FPP host, encoded per §5.1. Absent when no key is registered |
| `program.entries[].targets[].address` | string | `host:port` of that node's inbound listener, as the node last reported it. Absent when the coordinator has no reported address for the node |

A program with no `executorPublicKey` is still published, so a plugin that
never registers keeps reporting an installed, verified program exactly as it
does today. A node refuses every activation under such a program (§5.7,
`executor-not-enrolled`). Nothing else about that program looks wrong, so
two signals say it for an operator: `fallback_program.executor_key_present`
on the coordinator, per FPP player, and
`node.fallback.unenrolled_fpp_instance_uuids` on each node that holds such a
program. Night readiness does not check either.

A target with no `address` cannot be reached by the plugin. The plugin records
that it had no address for the target and sends nothing. It never guesses an
address and never takes one from anywhere but the installed program.

**Plugin 0.2.0's verifier accepts a program that carries these members, and
its resolver matches an entry in one.** That is what was run (§5.9). The
0.2.0 fetch and install path was read, not run: by that reading it looks
members up by name, has no list of permitted members, and does not read
`schemaVersion`, so it should install such a program, and nothing here has
observed it do so.

### 5.4 The node ingress

The two routes below are served by the node's one inbound HTTP listener, the
address in `targets[].address`. The listener's default port is 80. They are
not xLights routes, they are not part of the coordinator API, and they stay
out of `api/openapi.yaml`. They are answered whether or not the node's FPP
Connect upload surface is enabled, and they add no other route.

Every response from either route is a JSON object with at least these members:

| Member | Type | Meaning |
|---|---|---|
| `accepted` | boolean | `true` only when the node did what was asked |
| `outcome` | string | One word from the vocabulary in §5.7 |
| `reason` | string | One plain sentence for a log or an operator. Never parsed |

A caller decides on `outcome`, never on the HTTP status and never on `reason`.

A node that holds no pinned coordinator key refuses both routes with `503`
and `no-coordinator-key`. That node cannot verify any program, so it has no
fallback path, and the refusal says so.

### 5.5 Handing a node its program

```
PUT /showmesh/v1/fallback/programs/{fppInstanceUuid}
Content-Type: application/json
```

No credential. The body is self-authenticating: it is the signed program
document exactly as the plugin installed it, at most 1 MiB.

```json
{"program": { ... }, "signature": "<base64>"}
```

The signed bytes are the JCS bytes of the `program` object and the signature
is the coordinator's, as in `pkg/fallbackprogram`.

The node installs the program for that FPP host when all of these hold, and
checks them in this order:

1. the signature verifies against the node's pinned coordinator key;
2. `program.schemaVersion` is 1;
3. `program.fppInstanceUuid` equals the path value;
4. the node's own id appears in at least one entry's `targets`;
5. `program.expiresAt` is in the future; and
6. `program.compiledAt` is not earlier than the `compiledAt` of the program
   the node already holds for that FPP host.

The node keeps one program per FPP host, on disk, and a restart keeps it. Rule
6 is what makes the held program the current one: a node never goes back to an
older copy. Sending the copy the node already holds is accepted again and
changes nothing.

On success the response is `200` with `accepted: true`, `outcome:
"installed"`, and `packageId`, `revision`, and `expiresAt` of the held program.

**What the plugin does.** After it installs a program, and after every later
refresh, it sends that program to every distinct `address` the program names.
It also sends it when a node answers `program-not-installed` or
`program-not-current` (§5.8). It sends nothing to a node the program does not
name.

### 5.6 The activation request

```
POST /showmesh/v1/fallback/activations
Content-Type: application/json
```

The body is at most 4 KiB:

```json
{"request": { ... }, "signature": "<base64>"}
```

The signed bytes are the JCS bytes of the `request` object and the signature
is the executor key's. `request` has exactly these members. Every one is
required, and an unknown member is refused:

| Member | Type | Meaning |
|---|---|---|
| `schemaVersion` | integer | `1` |
| `executionId` | string | A UUID in its 36 character form with lowercase hex digits, unique per entry occurrence and target node (§5.8) |
| `fppInstanceUuid` | string | The FPP host sending the request |
| `packageId` | string | `program.packageId` of the plugin's installed program |
| `packageRevision` | string | `program.revision` of that program |
| `programExpiresAt` | string | `program.expiresAt` of that program, copied as written |
| `generation` | integer | `program.generation` |
| `catalogRevision` | string | `program.catalogRevisions[nodeId]` |
| `entryKey` | string | The playing entry's key, as §1.3 derives it |
| `cueId` | string | `cueId` of the program entry with that `entryKey` |
| `cueRevision` | integer | `cueRevision` of that entry |
| `nodeId` | string | `nodeId` of the target this request is sent to |

The request names a program entry and nothing else. It carries no action, no
macro, no file name, no offset, and no command name. What the node does comes
from the Cue catalog the node already holds, never from the request.

`programExpiresAt` ties the request to one published copy of the program. The
coordinator refreshes an unchanged program with a later `expiresAt` under the
same `packageId` and `revision`, so those two alone would leave a captured
request acceptable for as long as the show does not change. With the expiry in
the signed request, a request stops being acceptable when its copy of the
program expires or is replaced on the node.

The node accepts the request only when every step below passes, in this
order. The first failing step decides the outcome.

| Step | Check | Outcome when it fails |
|---|---|---|
| 1 | The caller's address is under the route's rate limit | `rate-limited` |
| 2 | The body is within size, is valid JSON, has exactly `request` and `signature`, every `request` member is present with the right type, there is no unknown member, and `schemaVersion` is 1 | `malformed-request` |
| 3 | `request.nodeId` is this node's id | `wrong-target` |
| 4 | The node holds a pinned coordinator key | `no-coordinator-key` |
| 5 | The node holds a program for `request.fppInstanceUuid` | `program-not-installed` |
| 6 | That program carries an `executorPublicKey` | `executor-not-enrolled` |
| 7 | The signature verifies against that key | `signature-invalid` |
| 8 | `packageId`, `packageRevision`, and `programExpiresAt` equal the held program's | `program-not-current` |
| 9 | The held program has not expired | `program-expired` |
| 10 | The held program has an entry with `request.entryKey` | `unknown-entry` |
| 11 | `cueId` and `cueRevision` equal that entry's | `cue-not-authorized` |
| 12 | This node is one of that entry's `targets` | `wrong-target` |
| 13 | `generation` equals the program's | `stale-generation` |
| 14 | `catalogRevision` equals the program's value for this node | `stale-catalog` |
| 14a | The node's Cue activation is available | `not-ready` |
| 14b | The node's record of handled execution ids opened without damage | `storage-unavailable` |
| 15 | `executionId` has not been processed | `replayed-execution` |
| 15a | The node wrote `executionId` to disk | `storage-unavailable` |

Steps 14a, 14b and 15a are about the node, not the request. `not-ready` is
what a node answers in the moment between starting to listen and finishing
its start, and also what a node that cannot run Cues at all answers every
time; `reason` says which. A node whose record of handled ids is damaged
answers `storage-unavailable` to every activation until an operator repairs
it, because a damaged record cannot say which requests already ran.

A request that passes step 15a is recorded in the node's replay fence and
then handed to the same Cue activation the coordinator's normal dispatch
uses. That
step checks the node's own held catalog against the request (show,
generation, catalog revision, Cue, Cue revision, and the files on disk) and
applies the Cue's outputs. Its result is the response's `outcome`:
`authorized` with `accepted: true`, or one of the normal activation refusals
in §5.7 with `accepted: false`. When the node cannot make that check at all,
for example because its held catalog cannot be read, the outcome is
`cue-check-failed`: the Cue was never allowed and nothing was started.

A request for another FPP host has no distinct outcome. It fails step 5 when
the node holds no program for the named host, and step 7 when it does, because
that program carries a different executor key.

### 5.7 Outcome vocabulary

| `outcome` | Status | Route | Meaning |
|---|---|---|---|
| `installed` | 200 | program | The node holds this program for this FPP host |
| `authorized` | 200 | activation | The Cue's outputs were applied |
| `malformed-request` | 400 | both | The body is not the shape this section fixes |
| `too-large` | 413 | both | The body is over the route's size limit |
| `rate-limited` | 429 | both | Too many requests from this address |
| `no-coordinator-key` | 503 | both | The node has no pinned coordinator key and cannot verify a program |
| `storage-unavailable` | 503 | both | The node could not write the program or the execution id to its disk, or its record of handled execution ids is damaged, so it did nothing |
| `not-ready` | 503 | activation | The node is still starting, or it cannot run Cues at all. `reason` says which |
| `program-signature-invalid` | 403 | program | The coordinator signature does not verify |
| `program-unsupported` | 409 | program | `schemaVersion` is not 1 |
| `wrong-fpp-host` | 403 | program | The program is for a different FPP host than the path names |
| `wrong-target` | 403 | both | This node is not a target of the program, the entry, or the request |
| `program-expired` | 409 | both | The program is past `expiresAt` |
| `program-superseded` | 409 | program | The node already holds a newer copy for this FPP host |
| `program-not-installed` | 409 | activation | The node holds no program for this FPP host |
| `program-not-current` | 409 | activation | The request names a different copy than the node holds |
| `executor-not-enrolled` | 403 | activation | The held program carries no executor key |
| `signature-invalid` | 403 | activation | The request signature does not verify against the program's executor key |
| `unknown-entry` | 409 | activation | The program has no entry with this key |
| `cue-not-authorized` | 403 | activation | The entry maps to a different Cue or Cue revision |
| `stale-generation` | 409 | activation | The generation differs from the program's, or from the node's held catalog |
| `stale-catalog` | 409 | activation | The catalog revision differs from the program's, or from the node's held catalog |
| `replayed-execution` | 409 | activation | This `executionId` was already processed. The response also carries `firstOutcome`, which is `unknown` when the node restarted before it recorded the first answer |
| `cross-show`, `unknown-generation`, `unknown-cue`, `stale-cue`, `asset-missing` | 409 | activation | The normal Cue activation refused, with the meaning it has on a normal dispatch |
| `weather-delay-active` | 409 | activation | The node is holding a weather delay and starts no Cue |
| `apply-failed` | 409 | activation | The Cue was authorized and an output could not be applied. The response also carries `reasons`, an array of sentences |
| `cue-check-failed` | 409 | activation | The node could not check the Cue against its own held catalog. The Cue was never authorized and nothing was started. The `executionId` is consumed |

An activation response also carries `executionId` whenever the request got
far enough to read one.

The node records every decision on both routes, accepted or refused, in its
own log and in its fallback report: a retained message on
`showmesh/nodes/<id>/observed/fallback`, schema `showmesh.node.fallback/v1`,
carrying the programs the node holds and whether each carries an executor key,
a count of every answer since the agent started, its 50 most recent answers,
and, while the node cannot record handled execution ids, a sentence saying
why. The coordinator turns the report into
`node.fallback.*` signals on the node. A run of `rate-limited` refusals on one
route within a minute is one entry with a count, and the node publishes at
most one report a second, so the report cannot itself be used to flood the
broker. The broker is usually unreachable during the outage this path exists
for, so the report reaches the coordinator when the node reconnects.

### 5.8 Execution ids, retries, and rate limits

- The plugin creates one `executionId` per entry occurrence and target node.
  Two nodes targeted by the same entry get two ids. A loop back into the same
  entry is a new occurrence and gets new ids.
- A retry carries the same `executionId` and the same signed body.
- The node processes an `executionId` at most once. The id is consumed when
  the request passes step 15a, whatever the Cue activation then answers,
  `cue-check-failed` included. The
  node persists the id before it applies anything, so a restart cannot make it
  apply the same request twice.
- A refusal at any step up to and including 15a consumes nothing. The same `executionId` may
  be sent again after the cause is fixed.
- The node forgets an `executionId` 24 hours after the program copy it was
  sent under has expired. Step 8 or step 9 refuses the request from the
  moment of expiry, so the id is no longer needed by then.

What the plugin does with each answer:

| Answer | Plugin action |
|---|---|
| No response, a transport error, `storage-unavailable`, `not-ready`, or any other `5xx` except `no-coordinator-key` | Retry the same body, at most 3 attempts in total, at least 250 ms apart |
| `rate-limited` | Retry the same body once after 1 second |
| `program-not-installed`, `program-not-current` | Send the installed program (§5.5), then retry the same body once |
| `replayed-execution` | Final. Take `firstOutcome` as the result of this execution |
| `authorized` | Final |
| Anything else | Final. Record the outcome and do nothing else for this target. Never send a different Cue, entry, or program in its place |

Rate limits are per caller address over a rolling minute: 120 activation
requests and 30 program deliveries. One FPP host driving one node needs one
activation per entry change, so the limit only bounds a caller that is
misbehaving.

### 5.9 Finding: plugin 0.2.0 and the new program members

Checked against the plugin repository at its `v0.2.0` tag
(`faefea4c284a7f310e36ccb01b3fe86aaa8aa1b7`) by reading
`native/adapters/shared/fallback_program_verifier.h`:

- `VerifyFallbackProgram` parses the document, canonicalizes the parsed
  `program` object with the plugin's own JCS implementation, and verifies the
  signature over those bytes. An added member is part of what it canonicalizes,
  exactly as it is part of what the coordinator signed.
- After verifying, it reads `packageId`, `revision`, `fppInstanceUuid`, and
  `expiresAt` by name. It has no allow-list of members and does not read
  `schemaVersion`.
- `fallback_program_fetch.h` and `fallback_activation_resolver.h` read
  `program`, `entries`, `entryKey`, `cueId`, `cueRevision`, `targets`,
  `nodeId`, `render`, and `audio` by name in the same way and ignore any other
  member.

So a program that carries `executorPublicKey` and `targets[].address`
verifies and installs on 0.2.0 by source reading.

It was also run. A throwaway program compiled the plugin's unmodified
`fallback_program_verifier.h`, `fallback_activation_resolver.h`, and
`native/src/json.cpp` at that tag and linked the host's OpenSSL 3 `libcrypto`.
Against `test/fixtures/fallback-activation/program.json`, which carries both
new members, `VerifyFallbackProgram` accepted the document and
`ResolveActivationFromDocument` returned `kMatch` for `entry-0`. Against
`program-wrong-signer.json` it refused with "signature does not verify". The
build host had no OpenSSL development headers, so the six `EVP_*`
declarations the verifier uses were written out by hand to match OpenSSL 3;
the library that did the verifying was the real one. This is a build-host
run of the verifier and resolver functions, not a run of the installed
plugin on an FPP host.

### 5.10 Fixtures

`test/fixtures/fallback-activation/` holds JSON data files on the §4 pattern:
signed programs that carry the new members, the executor and coordinator test
keys, one valid activation request with its canonical bytes and its signature,
and one request per refusal. `cases.json` states, for each case, which program
files the node holds, the node's clock, the request body, and the expected
status and outcome. `fixtures_test.go` regenerates every file from the Go
implementation and fails when one has drifted. The key pairs are the published
RFC 8032 section 7.1 test vectors, never a real key. Ed25519 signatures are
deterministic, so a plugin signer that produces a different signature for the
valid request's canonical bytes has a defect.

### 5.11 What this section does not do

- §5.1 through §5.10 do not decide the cutoff, the rest or hold behavior, or
  the hand-back at the next scheduled-show boundary. §5.12 through §5.17 do.
- It does not decide when the plugin declares the coordinator lost. That
  detector is the plugin's own, with its own settings.
- It does not let a node fetch a program from the coordinator, and it gives the
  coordinator no way to push one.
- It adds no route to the xLights compatibility surface and changes none.

### 5.12 Program validity and the plugin's refetch

A published program is valid for 24 hours from `compiledAt`. The length is a
named constant on the coordinator and a hypothesis, not a measurement. The
coordinator publishes an unchanged program again, under the same `packageId`
and `revision` with a later `compiledAt` and `expiresAt`, once less than 12
hours of the stored copy's validity remain. It never does so during an
outage, because it is not running. A copy a plugin holds when the coordinator
is lost therefore has at least 12 hours left, less the refetch interval below
and the coordinator's own two minutes between passes.

Validity is not what carries a change to a player. The plugin's refetch
interval is fixed here and does not depend on `expiresAt`:

- While the plugin is in `normal` (§5.13) and its last health probe succeeded,
  it sends `GET /api/v1/fallback-programs/{fppInstanceId}` every 60 seconds.
- When the fetched `packageId`, `revision`, and `expiresAt` all equal the
  installed copy's, the plugin does nothing more: no install, no
  acknowledgement, no delivery to a node. The one exception is the fetch at
  a hand-back (§5.13), which always acknowledges.
- Otherwise it verifies, installs, acknowledges, and hands the copy to every
  node address the program names, as §5.5 already requires.
- It never fetches while loss is confirmed, and never in `fallback` or
  `resting`.

**This replaces what plugin step J4 built**, which refetches after one third
of the program's own validity. Under a 24 hour validity that rule would leave
a changed program unfetched for up to 8 hours.

What a longer validity changes: a player cut off from the coordinator keeps
using the copy it holds for up to 24 hours instead of 15 minutes, and a node
that was never handed a newer copy keeps accepting activations under the old
one for as long. A node still refuses when its own held Cue catalog has moved
on (`stale-generation`, `stale-catalog`, `cross-show`) and when it holds a
newer copy (`program-not-current`). A replaced executor key stays acceptable
to a node that holds only the older copy until that copy expires or is
replaced. A node keeps handled execution ids for 24 hours after the copy
expires (§5.8), so it now keeps them for up to 48 hours.

### 5.13 The plugin's three states

The plugin is always in exactly one of three states. The words are the wire
values §5.15 carries.

| State | The plugin |
|---|---|
| `normal` | Posts playlist-entry observations (§1), keeps its key registered and its program current (§5.2, §5.12), and sends no activation |
| `fallback` | Posts no observation and fetches no program. At each entry boundary it sends the activations the installed program maps for that entry (§5.6, §5.8) |
| `resting` | Posts no observation, fetches no program, and sends nothing to any node |

**An entry boundary** is FPP's `playing` callback for a new entry occurrence,
as §1.8 defines an occurrence. A `query_next`, a callback for the occurrence
already handled, and FPP resuming the same entry are not boundaries. This is
what the program's `rules.fallbackBoundary` value `safe-playback-boundary`
means: the plugin acts at an entry boundary and never inside an entry.

**A usable program** is an installed copy that verified against the pinned
coordinator key, is before its `expiresAt`, carries an `executorPublicKey`
equal to the plugin's own public key, and whose `rules` object has exactly
these four values:

| Member | Value |
|---|---|
| `fallbackBoundary` | `safe-playback-boundary` |
| `restHold` | `hold` |
| `localShutdown` | `local-shutdown` |
| `recoveryBoundary` | `next-scheduled-show-boundary` |

A program with any other value in `rules` is not usable. The plugin does not
guess what an unknown rule asks of it.

**`normal` to `fallback`.** The plugin enters `fallback` at an entry boundary
when all of these hold at that boundary:

1. its detector has confirmed loss of the coordinator;
2. it holds a usable program; and
3. the program has an entry whose `entryKey` is the playing entry's key.

It then records the name of the playing playlist as the playlist it entered
under, and sends that entry's activations. Without all three it stays in
`normal` and sends nothing to a node. A plugin with no usable program, or
playing a playlist the program does not map, never enters `fallback`, so the
coordinator takes that player up again as soon as it is reachable, exactly as
it did before fallback existed.

**This narrows plugin step J4** wherever that step enters fallback on an
entry the program does not map, or without a usable program.

**Inside `fallback`.** At every later entry boundary of the playlist it
entered under, the plugin looks the entry key up in the installed program. A
mapped entry gets its activations. An unmapped entry gets nothing, and the
plugin records why. The plugin stays in `fallback` when its health probe
succeeds again. It does not fetch, install, or hand out a program, and it
keeps using the copy it entered with.

**`fallback` to `resting`** is the cutoff (§5.14).

**`fallback` or `resting` to `normal`** is the hand-back. The boundary is the
moment FPP stops playing the playlist the plugin entered under. The plugin
sees it as FPP's playlist `stop` callback for that playlist, or as any
callback that names a different playlist. A playlist that repeats is still
the same playlist: a new pass is a new occurrence of its entries and not a
boundary. This is what the program's `rules.recoveryBoundary` value
`next-scheduled-show-boundary` means.

At the boundary the plugin does these in order:

1. It goes to `normal`.
2. If its last health probe succeeded, it sends the state report (§5.15) and
   waits for the answer, for at most 5 seconds.
3. If its last health probe succeeded, it fetches its program at once
   (§5.12) instead of waiting out the interval, and sends the acknowledgement
   for the copy it then holds, whether or not the fetch changed it. The
   coordinator keeps holding the player until that acknowledgement names the
   published copy (§5.16), so this step is what ends the hold.
4. It posts observations again, starting with the next callback FPP delivers.

It never posts an observation for an entry that began while it was in
`fallback` or `resting`, and it never sends later an observation it skipped or
could not deliver. A plugin in `normal` whose last health probe failed, and
whose detector has not confirmed loss, may post an observation before its next
successful probe, exactly as §1 has it do. The coordinator acts on such an
observation as soon as it is not holding the player (§5.16). When loss is still confirmed at the boundary, the plugin is
in `normal` with a lost coordinator, and the three conditions above decide the
next entry boundary on their own: a second outage, or one that outlasts a
playlist, enters `fallback` again with the installed copy for as long as that
copy is usable. When the probe later succeeds with the plugin in `normal`, it
sends the report first (§5.15 rule 4) and then does step 3.

**A plugin restart is not a hand-back.** **This replaces what plugin step J4
built**, which leaves fallback when the plugin restarts. If it did, a plugin
that restarts in the middle of a playlist would come back in `normal`, post an
observation for the entry already playing, and the coordinator would start a
Cue the plugin already started. So the plugin saves its state and decides
after a restart from what FPP then does.

**What is saved, and where.** Before it sends the first activation of an
entry occurrence, the plugin writes one file, atomically and one writer at a
time. The file carries a file version, the state, the playlist the plugin
entered under, the time the state was entered, the package id, revision, and
expiry of the program copy it was entered with, and that occurrence's entry
key, playlist pass counter, and execution ids. The file sits beside the
pairing token, with the same file permissions. The plugin rewrites it on
every state change and removes it on hand-back. A file that is missing or
cannot be read means `normal`. A file with another version is set aside and
never resumed: the plugin starts in `normal` with the hand-back steps owed.

**Undecided.** A plugin that starts with a saved `fallback` or `resting` state
whose program expiry has not passed is undecided. FPP loads its plugins before
it starts any playlist, so the plugin cannot decide at start. For everything
the coordinator sees, an undecided plugin is in the saved state. It:

- posts no observation;
- reports the saved state and the saved playlist (§5.15) after its first
  successful probe, so the coordinator keeps holding the player;
- probes at least every 10 seconds (§5.15); and
- does not fetch a program, acknowledge, or hand back.

When the saved program expiry has passed, the plugin is not undecided: it
starts in `normal` and does the hand-back steps.

**The decision comes from FPP.**

- The first callback that names the saved playlist resumes the saved state.
  If the callback that resumes is for the recorded entry key, that entry gets
  no activation, whatever its pass counter says, because FPP's counter starts
  over with `fppd`. An entry the restart interrupted is not sent again.
- A callback that names another playlist is the boundary.
- A stop is the boundary.
- 30 seconds with no callback naming a playlist is the boundary. The 30
  seconds are a named hypothesis in the plugin, not a measurement, and are
  counted on a monotonic clock.

At the boundary the four hand-back steps follow.

The playlist name cannot tell the run fallback was entered in from a later
run of that playlist that started while the plugin was down, so a plugin can
resume `fallback` past the boundary. The program's expiry bounds a resume:
past it the plugin starts nothing (§5.14).

The coordinator needs nothing more for this. An undecided plugin reports the
saved state, and §5.16 holds the player on that report as on any other.

### 5.14 The cutoff, the hold, and local shutdown

ADR-048 names a cutoff, a rest or hold, and a local shutdown without saying
what each does. Per owner ruling (2026-10-05) they mean the following, which
adds no behavior the ADR does not name and no new node behavior.

**The cutoff is the `expiresAt` of the copy the plugin entered `fallback`
with.** It is signed, the node enforces the same instant on its own (§5.6
step 9), and the program needs no new member for it. The plugin checks it at
every entry boundary and at least as often as it probes the coordinator.

At the first check at or after the cutoff, a plugin in `fallback` goes to
`resting`. From then until the hand-back:

- **`restHold: hold`.** The plugin sends no activation and no program to any
  node. It does not stop, black out, or silence anything. Every output a node
  already started keeps running to the end of its own media.
- **`localShutdown: local-shutdown`.** Stopping is left to what is local to
  each device. FPP's own schedule ends the playlist. A node ends a Cue's
  output when its media ends. The plugin sends FPP no command, and it has no
  request that could stop a node, because the node ingress accepts an
  activation and nothing else (§5.6).

**Nothing is sent to a node after the cutoff.** That includes a retry of an
activation already queued under §5.8 and a program hand-off under §5.5: at
the cutoff the plugin drops both. A copy that has expired is never
acknowledged as `verified`, and is never handed to a node.

The plugin does not leave `resting` because a newer program exists or because
the coordinator answers. Only the hand-back (§5.13) ends it.

A node does nothing at the cutoff except refuse later activations under that
copy with `program-expired`.

### 5.15 The state report

```
PUT /api/v1/fallback-programs/{fppInstanceId}/fallback-state
Authorization: Bearer <pairing token>
Content-Type: application/json
```

`fppInstanceId` is the FPP instance UUID, as in §5.2.

**Authorization** is §5.2's, word for word: the token must carry
`fpp:fallback`, and the caller must be the plugin paired as the FPP player
whose instance UUID is `fppInstanceId`. `401`, `403`, and `409` mean what
they mean there. The body is not signed.

**Body.** At most 4 KiB. A member the coordinator does not know is ignored,
not refused, so a newer plugin keeps working against an older coordinator.

| Member | Type | Meaning |
|---|---|---|
| `schemaVersion` | integer, required | `1` |
| `bootId` | string, required | A UUID in its 36 character lowercase form that the plugin creates each time it starts |
| `sequence` | integer, required | At least 1, and greater than in every earlier report with this `bootId`. A retry of one report repeats its `sequence` and its body |
| `state` | string, required | `normal`, `fallback`, or `resting` |
| `since` | string, required | RFC 3339, the plugin's clock when it entered `state`. For a `normal` state that began at plugin start, the start time |
| `playlistName` | string | The playlist the plugin entered under, as FPP's own status names it (`current_playlist.playlist`): the bare name, no directory and no `.json`. The coordinator compares it with its own reading of the player (§5.16). Required in `fallback` and `resting`, absent in `normal` |
| `packageId` | string | `program.packageId` of the copy the plugin entered with. Required in `fallback` and `resting`, absent in `normal` |
| `packageRevision` | string | `program.revision` of that copy. Required and absent likewise |
| `cutoffAt` | string | `program.expiresAt` of that copy, copied as written. Required and absent likewise |

**When the plugin sends it.** A report always carries the state at the moment
it is sent. A report that fails is not queued and not replayed.

1. On its own start, as soon as it holds a pairing token and its first health
   probe has succeeded. It does not send one before any probe.
2. On every state change, at once.
3. Every 10 seconds, whatever its state, while its last health probe
   succeeded.
4. At once after any health probe that succeeds following one that failed.
   The report is then the first request the plugin sends the coordinator,
   before a program fetch and before an observation.

It sends no report while its last health probe failed.

**The probe is bounded, because the coordinator relies on it.** While the
plugin holds the coordinator as lost, in any state, and whenever the plugin is
not in `normal` (in `fallback`, in `resting`, or undecided after a restart,
§5.13), it probes at least every 10 seconds and never backs that probe off,
however long that lasts. A
coordinator that restarts holds a player for only 45 seconds while it waits
for the first report (§5.16). A plugin in `fallback` that probed less often
could miss that window, and the coordinator would then act on a show the
plugin is running.

**Responses.**

| Status | When | Body |
|---|---|---|
| `200` | The report was read | See below |
| `400` | The body is malformed or over 4 KiB, `schemaVersion` is not 1, a required member is missing or has the wrong type, `bootId` is not a lowercase UUID, `sequence` is below 1, `state` is not one of the three words, `since` or `cutoffAt` is not RFC 3339, a member required for the state is missing, or `normal` carries one of the four members it must not | Problem document, `invalid-parameter` |
| `401`, `403`, `409` | As §5.2 | Problem document |

The `200` body:

| Member | Type | Meaning |
|---|---|---|
| `serverTime` | string | RFC 3339 |
| `fppInstanceUuid` | string | The path value |
| `recorded` | boolean | `false` when the coordinator already holds a report with this `bootId` and an equal or higher `sequence`, and kept that one |
| `state` | string | The state the coordinator now holds for this player |

**No answer changes the plugin's state.** The plugin never enters or leaves a
state because of what this route returns, or because it returns nothing. A
`404` means the coordinator is older than this contract; the plugin keeps
reporting on its cadence and otherwise behaves as §5.13 says. The same holds
for `403`, `409`, and any `5xx`: the plugin stays in the state it is in and
keeps reporting. To the coordinator a plugin whose reports are all refused is
silent, and what §5.16 says of a silent plugin then applies: a hold on a
plugin in `fallback` or `resting` stands until the coordinator itself reads
the playlist as over, and a wait for an acknowledgement ends after 45
seconds.

**An acknowledgement stays owed until it succeeds.** This is the program
acknowledgement of §5.12 and of hand-back step 3, not the state report. One
that got no success answer is sent again on the probe cadence, after each
probe that succeeds, until the coordinator answers it with success. A
timeout, a `408`, a `429`, and any `5xx` keep that retry going. An
acknowledgement answered with any other `4xx` is not retried until the next
pairing or the next newly installed copy, because sending the same body again
would be refused again. The coordinator's hold after a hand-back waits for a
success answer (§5.16).

**What the coordinator keeps.** One report per FPP player, the latest, with
the time it arrived. A report with a `bootId` it has not seen replaces the
stored one. A report with the stored `bootId` replaces it only with a higher
`sequence`. The coordinator writes one audit entry, action
`fallback.player_state.report`, when the stored `state` changes, and none for
a repeat of the same state.

### 5.16 What the coordinator holds back

This subsection states coordinator behavior so that a plugin author and a
tester can predict it. It asks nothing more of the plugin.

A player whose plugin has never reported is never **held**, so a plugin built
before this contract is treated exactly as it is today. For a player whose
plugin has reported, the latest report decides:

| Latest report | The player is |
|---|---|
| `fallback` or `resting`, of any age | Held, until the plugin reports `normal` or the coordinator itself reads the playlist as over (below). A plugin in fallback that goes quiet is most likely cut off again and still running the show |
| `normal`, after a `fallback` or `resting` report | Held until this player's program acknowledgement names the copy the coordinator has published for it, for as long as the plugin keeps reporting. Not held at all when nothing is published for it, when the acknowledgement already names that copy, or once the plugin has been silent for more than 45 seconds, counted as in point 1 below: a silent plugin cannot be waited on |
| `normal`, at most 45 seconds old | Not held |
| `normal`, older than 45 seconds | Not held. A dead or hung plugin must not stop a night from advancing. The coordinator raises a signal that the plugin has stopped reporting |
| Any state, from before this run of the coordinator, and its loops and HTTP listener have been up for at most 45 seconds | Held until the first report of this run arrives, or the 45 seconds end. This row comes first: it applies to every stored report, whatever it says |

The last row is what covers a coordinator that restarts while a plugin runs
the show: the plugin's first report after its probe recovers says `fallback`,
and the first row then applies. It depends on the probe bound in §5.15. The
45 seconds count from when the coordinator can first be reached, not from
when its process started.

The second row is ADR-048 decision 4: the coordinator resumes normal Cue
resolution only after its package acknowledgement is current. It is why
§5.13 makes the plugin fetch and acknowledge at once at the hand-back, so
this hold normally lasts seconds. The Cue catalog half of that sentence is
enforced where it already was, on every activation, by the coordinator's own
check and by the node. No hold waits for every node's catalog
acknowledgement, because one offline node must not hold a whole show.

**A hold ends without the plugin only when the coordinator is sure the show
it protects is over.** All three of these must hold:

1. The plugin has been silent for more than 45 seconds, counted from the
   later of its latest report and the moment this run of the coordinator
   could first be reached. The coordinator's own downtime is never the
   plugin's silence.
2. The coordinator's own current reading of the player says the playlist is
   over. That reading is the `fpp.status` and `fpp.playlist.name` signals,
   resolved exactly as the night loop resolves them, from readings taken after
   the latest report, and never from the rows built from the plugin's own
   posts. Over means one of two things only: the player is `idle`, or it is
   `playing` and the playlist name differs from the reported `playlistName`
   after a directory and a trailing `.json` are stripped from both. A player
   that is `paused`, `stopping gracefully`, `stopping gracefully after loop`,
   or `unknown`, a reading with no value, and a reading that is no longer
   current are all not over.
3. The reading has said over for more than 45 seconds without a break. When a
   link comes back, the coordinator's poll of the player can land before the
   plugin's report; a plugin that is alive reports well inside that time. Two
   readings more than 15 seconds apart are not one standing reading: the count
   starts again, so the rule does not depend on how often the coordinator
   looks.

The coordinator then clears the stored report, exactly as an operator's clear
does (below), so the hold cannot come back when the player next plays a
playlist of that name. It writes a warning to its log, one event of category
`fallback.hold_ended_without_plugin`, and one audit entry, action
`fallback.player_state.auto_clear`. A plugin that reports `fallback` again
later is held again from that report. A plugin that is still reporting is
never overruled this way. When a newer report arrives while the coordinator
is ending a hold, nothing is cleared and the newer report decides.

**The limit of this rule.** The coordinator cannot tell a dead plugin from one
that can no longer reach it. If the plugin cannot reach the coordinator while
the coordinator can still read the player, and FPP moves to another playlist,
or sits idle for more than 45 seconds, the hold ends and the stored report is
cleared although the plugin may still be running the show. What follows:

- The plugin goes on as §5.13 says. It stays in `fallback`, or enters it
  again under the next playlist, and keeps sending activations to the nodes.
  Nothing tells it the coordinator has let go.
- The coordinator treats the player as its own again. It starts no Cue
  twice: it receives no observation from that plugin, and acts on nothing it
  received before the clear. Its night loop does advance, so it can start or
  replace a playlist on that player, and its automatic Cue catalog deploy can
  hand the nodes a new catalog, after which they refuse the plugin's
  activations.
- When the plugin reaches the coordinator again, its first request is the
  state report, and the player is held again from that report.

This is accepted as built. A plugin cut off one way before its first
`fallback` report is never held at all, so the design has this limit either
way.

**Two more things end any hold without the plugin's say.** A playlist-entry
observation that arrives more than 45 seconds after the player's latest
report comes from a plugin that does not report, a plugin replaced by an
older one for example, and the player is not held until a report arrives
again. A plugin that follows §5.13 never does this, because it reports before
it observes. And an operator can clear what the coordinator stored:

```
DELETE /api/v1/fallback-programs/{fppInstanceId}/fallback-state
Authorization: Bearer <an operator's token carrying fpp:command>
```

It answers `204` whether or not anything was stored, and writes one audit
entry, action `fallback.player_state.clear`. It is the manual way out of a
wait for an acknowledgement that never comes. It does not take a show away
from a plugin that is running it: that plugin reports `fallback` again within
10 seconds and is held again, and in between the coordinator acts on nothing
the player observed before the clear. After a clear the player reads as one
whose plugin has never reported, until the plugin's next report.

While a player is held, the coordinator:

- starts, stops, and clears no Cue from that player's playlist-entry
  observations;
- does not advance a night session that uses that player: it starts no
  playlist on it, sends it no transition, and starts or stops no background
  audio for that session; and
- does not send any node a new Cue catalog on its own. For this one, only a
  player that is configured now counts, under the instance UUID it has now.
  A stored report for an instance no configured player has, a replaced
  player's for example, holds nothing here.

It still publishes fallback programs, records observations and node reports,
answers every read, and carries out what a person or FPP's schedule asks for
through the API: a command to a player or a node, an emergency stop, and a
weather delay are never held. **A shutdown is never held.** Once
`fade-out-night`, `power-down-presentation`, or `end-session` has been asked
for, the session advances as if no player were held, including the wait for
a live show to finish and the fade that follows it. The operator wins over
the hold, with one limit: a held player is never sent a start. A session
with a shutdown asked for, committed to a show it has not started, on a
player that is held, fades out at once and drops that show. That holds
whether the hold began before the shutdown was asked for or after it,
including the 45 seconds after a coordinator start. A night command that starts
something, `start-night` for example, is accepted and takes effect when the
hold ends. `request-final-show` records the request and takes effect at the
hand-back: it is not a shutdown and the session stays held.

When a hold ends, the coordinator acts on that player's observations again,
with one floor:

- After a hand-back, it acts on nothing it received at or before the report
  that ended the plugin's time as executor. What the player was doing before
  the hand-back is not replayed. An observation that arrives after the
  `normal` report and before the acknowledgement is acted on as soon as the
  acknowledgement lands.
- After the 45 second hold that follows a coordinator start, it acts on
  nothing it received before this run of the coordinator, and on everything
  it received in this run. A `normal` report that ends that hold ended no
  time as executor, so an observation the plugin posted in this run before
  its first report is acted on as soon as that report arrives.

### 5.17 What an operator can read

| Where | What |
|---|---|
| `GET /api/v1/fallback-programs` and `GET /api/v1/fallback-programs/{fppInstanceId}` | An added `playerState` object, absent when the plugin has never reported: `state`, `since`, `playlistName`, `packageId`, `packageRevision`, `cutoffAt`, `reportedAt`, `pluginReporting`, `held`, `holdReason` (`running-from-fallback`, `waiting-for-acknowledgement`, or `coordinator-starting`), `acknowledgementWaitSeconds`, and a one or two sentence `message` |
| `GET /api/v1/fallback-programs` | An added `playerStatesWithoutProgram` array: every stored report for an FPP instance that has no published program, so none goes unlisted |
| The current night session, `GET /api/v1/night/session` | An added `fallbackHold` object while the session does not advance because a player it uses is held: `fppInstanceId`, `fppInstanceUuid`, `reason`, and `message`. Absent once a shutdown has been asked for and while the session is fading out or stopped, because the session then advances regardless. Show Night renders it above the lifecycle commands |
| `fallback_program.player_state` | The reported state. It goes stale 45 seconds after the last report |
| `fallback_program.coordinator_holding` | Whether the coordinator is holding the player |
| `fallback_program.plugin_reporting` | `false` when a plugin that reported before has been silent for more than 45 seconds |
| `fallback_program.acknowledgement_wait_seconds` | How long the coordinator has waited for the acknowledgement after a `normal` report. `0` when it is not waiting |
| `showmeshctl fallback list`, `show`, `clear` | The same fields as the listing, including a stored report with no program, and the clear route |
| `showmeshctl night status` | The held player, the `message`, and the clear command to run |
| The audit log | `fallback.player_state.report` for each change of reported state, `fallback.player_state.clear` for each operator clear, `fallback.player_state.auto_clear` for each hold the coordinator ended itself |
| The event list and the coordinator's log | One event, category `fallback.hold_ended_without_plugin`, and one warning when a hold ends because the coordinator saw the playlist over. One line at information level when the night loop, or the automatic Cue catalog deploy, begins and ends a wait for a held player |

For a held player whose plugin has stopped reporting, `message` says that the
plugin has stopped reporting and that the operator can clear its fallback
state, not that the player is running the show.

The four signals are on the FPP player's fallback program, the resource
`fallback_program.executor_key_present` (§5.3) is on, and are rewritten every
5 seconds. Night readiness reads none of this.

### 5.18 The coordinator's public key reaches the plugin at pairing

The plugin accepts a fallback program only when it verifies against the
coordinator's Ed25519 public key (§5.13). Pairing delivers that key
([ADR-025](../decisions/ADR-025-agent-fallback-cache-is-signed.md) decision 8).

- The 200 answer of `POST /api/v1/integrations/fpp/pairing/claim` carries one
  more member, `coordinatorPublicKey`: a string, always present, the raw 32-byte
  key encoded per §5.1. It is the same value, from the same source, that node
  enrollment returns as `coordinatorPublicKey`, and it is the key that signs the
  published fallback programs.
- The plugin stores it in
  `/etc/showmesh-fpp-plugin-trust/coordinator-fallback-public-key`: owned by
  root, mode 0644, one line holding the same base64 text and a newline. It
  reads the key only from there and refuses a file or directory that is not
  owned by root.
- Only a successful pairing claim writes the file, and a later pairing replaces
  it. A refused claim carries no key. No start, restart, or fallback path
  fetches the key from the coordinator.
- The player holds one key, and a later pairing replaces it.
- A plugin paired before this change has no key until it pairs again. Until
  then it has no key to verify a program against.
- The route, the request, the refusals, the rate limit, and the audit entry of
  the claim are unchanged. Night readiness does not read the key.
