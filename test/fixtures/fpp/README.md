# FPP plugin coordinator fixtures

This directory holds plain JSON data files consumable by any language. They
freeze the exact bytes that
[`docs/build/FPP-PLUGIN-COORDINATOR-CONTRACTS.md`](../../../docs/build/FPP-PLUGIN-COORDINATOR-CONTRACTS.md)
section 3 promises: the coordinator (this repository, Go) and the ShowMesh
FPP plugin (`showmesh-fpp-plugin`, Apache-2.0, C++) implement the same
canonicalization and hashing rules independently, and these files are how
that agreement is checked without either repository depending on the other.

They are deliberately not a Go package and not a shared module. The plugin
repository has no Go module dependency on this one, and its C++ core links no
third-party JSON library, so a fixture it cannot read with its own minimal
parser is a fixture it cannot use.

## Files

- `canonicalization.json`: RFC 8785 (JCS) canonicalization cases, covering
  `playlistHash` derivation.
- `entry-key.json`: `entryKey` derivation cases, covering the five-field
  identity object contract section 1.3 fixes.
- `ingestion.json`: cases for
  `POST /api/v1/integrations/fpp/playlist-entry-observations`, covering
  contract section 1.7's refusal table and the acceptance behaviors around
  it. This file is consumed only by this coordinator's own Go test
  (`internal/coordinator/api/fppobservations_fixtures_test.go`), because
  exercising it requires a running instance of this coordinator's HTTP
  handler; the plugin repository has no server of this shape to test against
  and has no use for this file.
- `fixtures_test.go`: this coordinator's own test for
  `canonicalization.json` and `entry-key.json`, run alongside the rest of
  this repository's test suite so a fixture that drifts from the Go
  implementation fails here before the plugin ever sees it.

All three data files are frozen contract expectations, not incidental output
of some other test. Every expected value was produced once by running the
Go reference implementation (`pkg/fppidentity`), pasted into these files as a
literal, and then cross-checked against the plugin repository's C++
reference implementation (`native/src/json.cpp`,
`native/src/playlist_identity.cpp`) by compiling a throwaway program against
its sources. A future change to either implementation must keep matching
these files, not the other way around.

## `canonicalization.json` schema

A JSON object with a `description` string and a `cases` array. Each case is
an object:

| Field | Type | Meaning |
|---|---|---|
| `name` | string | Unique within the file. |
| `description` | string | What the case exercises. |
| `input` | string | The raw JSON text to canonicalize, carried as a JSON string so a case can hold deliberately malformed input. |
| `expectedCanonical` | string | Present on a success case: the RFC 8785 canonicalization of `input`, byte for byte. |
| `expectedSha256` | string | Present on a success case: the lowercase-hex SHA-256 of the UTF-8 bytes of `expectedCanonical`. |
| `expectError` | boolean | Present and `true` on a case that must be rejected. `expectedCanonical`/`expectedSha256` are omitted for such a case. |
| `errorKind` | string | Present alongside `expectError`: a short, human-readable label for what kind of error (for example `"malformed-json"`, `"duplicate-member-name"`), not a wire error code. |

A consumer reads `input`, runs its own canonicalizer over it, and compares
the result against `expectedCanonical` byte for byte (not just semantically
equal JSON: insignificant whitespace, member order, and number formatting
are exactly what is under test). It then hashes those canonical bytes with
SHA-256 and compares against `expectedSha256`. For an `expectError` case, the
consumer's canonicalizer must fail; no output comparison applies.

## `entry-key.json` schema

A JSON object with a `description` string and a `cases` array. Each case is
an object:

| Field | Type | Meaning |
|---|---|---|
| `name` | string | Unique within the file. |
| `description` | string | What the case exercises. |
| `identity` | object | The five entry-identity fields: `instanceUuid`, `playlistName`, `playlistHash`, `section` (all strings), `position` (a JSON number, zero-based). |
| `expectedCanonicalKeyObject` | string | The exact RFC 8785 canonical text of the five-member JSON object `{instanceUuid, playlistHash, playlistName, position, section}` built from `identity`, before hashing. |
| `expectedEntryKey` | string | The lowercase-hex SHA-256 of the UTF-8 bytes of `expectedCanonicalKeyObject`. |

`expectedCanonicalKeyObject` is carried separately from `expectedEntryKey`
specifically so a consumer can tell a canonicalization bug apart from a
hashing bug: if the canonical object text matches but the hash does not, the
bug is in the hashing step; if the canonical object text itself is wrong,
the bug is in how the five-field object is built or canonicalized.

A consumer builds the five-member object from `identity` (member names
exactly `instanceUuid`, `playlistHash`, `playlistName`, `position`,
`section`; RFC 8785 member-name sorting happens to put them in this order
already, but a consumer should sort rather than assume), canonicalizes it
with its own canonicalizer, compares against `expectedCanonicalKeyObject`,
hashes those bytes, and compares against `expectedEntryKey`.

## `ingestion.json` schema

A JSON object with a `description` string and a `cases` array. Each case is
an object:

| Field | Type | Meaning |
|---|---|---|
| `name` | string | Unique within the file. |
| `description` | string | What the case exercises. |
| `scope` | string | One of `"fpp:observe"` (a principal holding the plugin's own scope), `"show:macro:run"` (an operator principal that deliberately does not hold `fpp:observe`, the wrong-scope case), or `"none"` (no credential at all). |
| `body` | object | The request body to POST, as a JSON object. Omitted when `rawBodyOverride` or `oversizedBodyBytes` is present instead. |
| `rawBodyOverride` | string | Present only for a case whose wire content is not valid JSON (for example the malformed-body case): the literal bytes to POST. |
| `oversizedBodyBytes` | integer | Present only for the oversized-body case: see below. |
| `expectedStatus` | integer | The HTTP status code the endpoint must return. |
| `expectedProblemType` | string | Present on a refusal: the problem type's suffix after this API's fixed base URI, `https://showmesh.dev/problems/`. Omitted on success. |
| `expectedAccepted` | boolean | Present on a `200` case: the response body's `accepted` field. |
| `expectedReplay` | boolean | Present on a `200` case: the response body's `replay` field. |
| `priorCases` | array of strings | Names of earlier cases in this same file whose bodies must be POSTed first, in order, against a freshly created store, before this case's own body is posted. Omitted or empty when this case needs no prior state. |

### Oversized-body synthesis

The 16384-byte body bound (contract section 1.2) cannot be exercised with a
literal JSON value that size without bloating this file. A case that needs
one carries `oversizedBodyBytes` instead of `body`; a consumer synthesizes a
syntactically valid observation body padded to at least that many bytes
(for example, by filling `playlistName` with filler characters until the
JSON-encoded body reaches the target length) and posts that.

### What a consumer does with a case

1. Resolve a bearer credential per `scope` (or none, for `"none"`).
2. POST each of `priorCases`' bodies, in order, against a fresh backing
   store, and confirm each POST's own `expectedStatus` before continuing.
   A case's `priorCases` are themselves complete cases in this file, not a
   separate abbreviated form.
3. POST this case's own body (from `body`, `rawBodyOverride`, or the
   synthesized oversized body).
4. Assert the response status against `expectedStatus`.
5. On a refusal, assert the problem's `type` equals the fixed base URI plus
   `expectedProblemType`.
6. On success, assert `accepted` and `replay` against `expectedAccepted` and
   `expectedReplay`.

## Regenerating after a deliberate contract change

A change to canonicalization, entry-key derivation, or ingestion behavior
that is an intentional, reviewed amendment to
`FPP-PLUGIN-COORDINATOR-CONTRACTS.md` (not a bug fix on one side) is
regenerated, never hand-edited to a guess:

1. Amend the contract document first; these fixtures follow it, not the
   other way around.
2. Update the Go reference implementation (`pkg/fppidentity`,
   `internal/coordinator/api/fppobservations.go`) to match the amended
   contract.
3. Run the implementation to produce the new expected values (canonical
   text, hashes, entry keys, response fields) and paste them into the
   affected fixture cases as literals. Do not write a test, or a fixture,
   that computes its own expectation from the same code it is meant to
   check.
4. Re-run this repository's tests (`go test ./test/... ./internal/...
   ./pkg/fppidentity/...`) to confirm the pasted literals match.
5. Cross-check against the plugin repository's C++ implementation: update
   `native/src/json.cpp` / `native/src/playlist_identity.cpp` to match the
   amended contract, then build and run a throwaway program against those
   sources over the updated fixture files, the same way this fixture set's
   own values were originally verified.
6. If any case disagrees between the two implementations, that is a
   blocker raised against both repositories, never a unilateral edit to
   whichever fixture value is inconvenient on one side.

## Related reading

[`docs/build/FPP-PLUGIN-COORDINATOR-CONTRACTS.md`](../../../docs/build/FPP-PLUGIN-COORDINATOR-CONTRACTS.md)
fixes the wire contract these fixtures verify. `pkg/fppidentity` is this
repository's reference implementation of canonicalization and the two
hashes; its own package doc comment explains why `encoding/json` cannot be
used for this purpose.
