# Fallback activation fixtures

Plain JSON data files for section 5 of
[`docs/build/FPP-PLUGIN-COORDINATOR-CONTRACTS.md`](../../../docs/build/FPP-PLUGIN-COORDINATOR-CONTRACTS.md),
on the pattern of [`test/fixtures/fpp/`](../fpp/README.md): the node agent
(this repository, Go) and the ShowMesh FPP plugin (C++) implement the same
signing and canonicalization rules independently, and these files are how
that agreement is checked without either repository depending on the other.

Every key pair here is a published RFC 8032 section 7.1 test vector. No file
holds a key anyone uses.

## Files

- `keys.json`: the coordinator public key the programs are signed under, and
  the seed and public key of two executors. `executorSeedHex` is the 32 byte
  Ed25519 seed a plugin test signs with.
- `program.json`: a signed program document, `{"program": ..., "signature":
  ...}`, that carries both members section 5.3 adds: `executorPublicKey` and
  `targets[].address`. It verifies under `coordinatorPublicKey`.
- `program-older.json`: the same FPP player's program compiled one minute
  earlier.
- `program-no-executor-key.json`: a program with no `executorPublicKey`.
- `program-other-host.json`: a program for a second FPP player, carrying the
  other executor's key.
- `program-not-targeted.json`: a program that does not name `node-a`.
- `program-wrong-signer.json`: `program.json`'s content signed by a key that
  is not the coordinator's.
- `cases.json`: the cases, described below.
- `fixtures_test.go`: regenerates every file from the Go implementation with
  `go test ./test/fixtures/fallback-activation -update`, and fails a plain
  run when a file has drifted.

## `cases.json`

| Member | Meaning |
|---|---|
| `nodeId` | The node every case is sent to. |
| `nodeCatalog` | The Cue catalog that node holds: show, generation, catalog revision, and its Cues. |
| `validRequest.canonical` | The RFC 8785 canonical bytes of the valid request's `request` object. |
| `validRequest.signature` | The executor's Ed25519 signature over those bytes, standard base64. Signing `canonical` with `executorSeedHex` must produce exactly this. |
| `activations[]` | One case per answer of `POST /showmesh/v1/fallback/activations`. |
| `programs[]` | One case per answer of `PUT /showmesh/v1/fallback/programs/{fppInstanceUuid}`. |

Each activation case:

| Member | Meaning |
|---|---|
| `installed` | The program files the node holds before the request, each delivered under the FPP player it names. |
| `now` | The node's clock when the request arrives. |
| `body` | The exact request body, as a JSON string. |
| `repeatBody` | When true, `body` is sent once first and must be authorized; the expected answer is for the second send. |
| `expectedStatus`, `expectedOutcome` | The HTTP status and the `outcome` word. |

Each program case has the same `installed`, `now`, `expectedStatus` and
`expectedOutcome`, plus `pathFppInstanceUuid` (the path value) and `document`
(the program file sent as the body).

A plugin test needs `keys.json`, `program.json`, and `validRequest`: build the
request from the program, canonicalize it, and compare against `canonical`;
sign and compare against `signature`. The refusal bodies show what each
refusal looks like on the wire and are checked on the node side by
`internal/agent/fallbackingress_test.go`.
