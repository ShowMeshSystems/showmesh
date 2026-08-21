# FPP plugin coordinator contracts

[RES-018](../research/RES-018-fpp-brightness-control.md) · [ADR-043](../decisions/ADR-043-show-scoped-cues-and-playlist-authority.md) · [ADR-024](../decisions/ADR-024-identity-authorization-and-audit.md) · [Track F](TRACK-F-resting-mode.md) · [Track H](TRACK-H-cues-and-playlists.md) · [SM-63 handoff](SM-63-FPP-PLUGIN-HANDOFF.md)

Status: frozen 2026-08-21. This record fixes the two wire contracts the
ShowMesh FPP plugin runtime shares with the coordinator: playlist-entry
observation ingestion, and the coordinator-owned brightness transition gain.
RES-018 decided the design; this file fixes the exact bytes so the plugin and
the coordinator can be built independently and still meet.

Nothing here has run against a real FPP host. The contract is verified by unit
tests and by the shared fixtures in
[`test/fixtures/fpp/`](../../test/fixtures/fpp/README.md), on both sides.

## 1. Playlist-entry observation ingestion

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
| `section` | string | when available | FPP playlist section. May be empty. |
| `position` | integer | when available | Zero-based position within the section. |
| `entryKey` | string | when available | SHA-256, lowercase hex. See §1.3. |
| `sequenceFilename` | string | no | Absent when the entry has none. |
| `mediaFilename` | string | no | Absent when the entry has none. |
| `action` | string | yes | One of `start`, `playing`, `stop`, `query_next`, `unknown`. |
| `sequence` | integer | yes | Monotonic per-instance event sequence. See §1.5. |
| `observedAtMillis` | integer | yes | Plugin observation time, epoch milliseconds. |
| `coalescedSincePreviousAcknowledged` | integer | yes | Gap evidence, `0` when none. |
| `unavailable` | string | no | Absent, or one of the §1.4 reasons. |

`instanceUuid`, `sequence`, `observedAtMillis`, `action`, `schemaVersion`, and
`coalescedSincePreviousAcknowledged` are present on every observation,
available or not. The identity fields (`playlistName`, `playlistHash`,
`section`, `position`, `entryKey`) are required when `unavailable` is absent
and are permitted to be absent when it is present.

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

The canonicalization rules both sides implement:

- Object member names sorted by UTF-16 code unit, duplicates rejected.
- No insignificant whitespace.
- ECMAScript `Number::toString` number formatting.
- Minimal string escaping: `"`, `\`, `\b`, `\f`, `\n`, `\r`, `\t`, and
  `\u00xx` for any other code point below `0x20`. Nothing else is escaped.
- UTF-8 output.

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
  deliberately out of scope here; until it does, a plugin that loses its
  persisted sequence is recovered by clearing the coordinator's stored row.

### 1.6 Ingestion behavior

In order:

1. Authenticate; refuse `401` when no credential resolves.
2. Check `fpp:observe`; refuse `403` naming the scope.
3. Bound the body at 16384 bytes; refuse `413` on overflow.
4. Decode. Refuse `400` on malformed JSON or unknown fields.
5. Refuse `400` when `schemaVersion` is not `1`.
6. Refuse `400` when `instanceUuid` is absent or empty.
7. Refuse `400` when `action` is outside the fixed vocabulary, when
   `unavailable` is outside the §1.4 vocabulary, when `position` is negative,
   when a hash is not 64 lowercase hex characters, or when
   `coalescedSincePreviousAcknowledged` or `sequence` is negative.
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
| Malformed body, unknown field | 400 | `invalid-parameter` |
| Unsupported `schemaVersion` | 400 | `unsupported-observation-schema-version` |
| Missing `instanceUuid` | 400 | `invalid-parameter` |
| Invalid enum, hash, or position | 400 | `invalid-parameter` |
| Derived entry key mismatch | 400 | `observation-entry-key-mismatch` |
| Reused sequence, different body | 409 | `conflict` |
| Sequence regression | 409 | `conflict` |

## 2. Brightness transition gain

### 2.1 The composition

RES-018 §1, restated so implementers do not have to re-derive it:

```text
effective output = round(ceiling * transition_gain / 100)
```

`ceiling` is 0–100 and is written by FPP's own schedule and operator command
path, through the plugin's registered `ShowMesh: Set Brightness Ceiling` FPP
Action. It is already implemented in the plugin.

`transition_gain` is 0–100 and is the coordinator's alone. It is **not**
reachable from any FPP Action, MQTT setter, or relative brighten/dim command.
Adding any second writer to it defeats the seam.

### 2.2 The coordinator-facing write

The night-session controller writes the transition gain by POSTing to the
resident plugin component, on the FPP host, at the plugin's own HTTP path:

```text
POST /api/plugin/showmesh/brightness/transition-gain
```

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

### 2.4 What remains unbuilt

The plugin does not yet serve this path; `BrightnessEngine::setGain()` has no
caller, so the gain is pinned at 100 on any real host and the compositional
seam cannot be exercised end to end. The coordinator does not yet call it
either: Track F's readiness still rejects any cue that requires compositional
brightness (`nightCheckNoUnbuiltBrightnessComposition`), and that check stays
in place until the plugin serves this contract and RES-018 §8's decisive
mid-fade case is observed on a real host.

This section is the frozen shape that unblocks those two implementations. It
is not a claim that either exists.

## 3. Shared fixtures

`test/fixtures/fpp/` holds plain JSON data files, consumable by any language.
They are deliberately not a Go package and not a shared module: the plugin
repository is Apache-2.0 with no Go module dependency on the coordinator and a
C++ core that links no third-party library, so a fixture it cannot copy as
data is a fixture it cannot use.

See [`test/fixtures/fpp/README.md`](../../test/fixtures/fpp/README.md) for the
file format and the case list. The coordinator's own tests consume the same
files, so a fixture that drifts from the implementation fails on this side
before the plugin ever sees it.
