# Track H seam H3: active-show generation and resolved Cue catalogs

[Track H](TRACK-H-cues-and-playlists.md) · [H1 spec](TRACK-H-H1-SPEC.md) · [H2 spec](TRACK-H-H2-SPEC.md) · [ADR-043](../decisions/ADR-043-show-scoped-cues-and-playlist-authority.md) · [ADR-027](../decisions/ADR-027-show-and-surface-model.md) · [ADR-028](../decisions/ADR-028-show-asset-store-and-identity.md)

Status: specified 2026-08-22. Depends on H1's two configuration kinds. Does
not depend on H2, on the plugin, or on a reachable FPP: a Cue catalog can be
resolved, deployed, and acknowledged with nothing playing.

## 1. What this seam is

Authority to execute a Cue becomes a thing a node holds, checks, and can lose.
Before this seam, a node's only defence against running the wrong content is
whether the file happens to be on its disk, which ADR-043 decision 6 says is
no defence at all.

Three pieces: a generation that identifies the current grant of authority, a
catalog that says what the active Show may execute, and a refusal path that
rejects anything not matching both.

## 2. The active-show generation

**Decision: the generation is `show.active`'s config revision number. Nothing
new is minted.**

The requirement in ADR-043 decision 3 is a value that changes whenever
`show.active` changes or its authorization is deliberately reissued.
`show.active` is already a revisioned singleton configuration object, so its
revision number changes on every write by construction, and
`writeShowConfigRevision` computes the next revision unconditionally rather
than deduplicating an identical payload. Re-writing `show.active` with the
same show is therefore already a deliberate reissue, and it is already
audited as a `config.write` with its revision in the audit parameters.

A second counter would have to be kept consistent with that revision, and the
day it drifts is the day a node accepts authority the coordinator thinks it
revoked. `assetsync.ActiveShow` grows a `Generation` field carrying the
revision it read, and every consumer of `ResolveActiveShow` gets it for free.

An unconfigured `show.active` has no generation and authorizes nothing. This
is the existing honest-absence case, not generation zero.

## 3. The resolved Cue catalog

A catalog is what one node is allowed to execute for the active Show at one
generation. It is derived, never authored.

Resolution, for a node:

1. Read the active Show and its generation.
2. Collect every `show.cue` in that Show that some `show.playlist` in that
   Show references, plus every Cue named by a `mismatchPolicy.safeCueRef`.
   A Cue no Playlist references and no policy names is not deployed: it is a
   draft, and deploying drafts is how a half-authored Cue reaches a node.
3. For each Cue, resolve the outputs that concern this node: the render
   sequence for surfaces assigned to it, the audio asset, the LTC offset, and
   the announcement policy.
4. Attach the asset identities those outputs need, reusing Track E's
   `ExpectedAssetsForNode` rather than a second expectation model.

The catalog carries, for each Cue: Cue id, Cue revision, the resolved outputs
for this node, and the content hashes of the assets they need.

### 3.1 Catalog revision

The catalog revision is a SHA-256 over a canonical serialization of the
resolved catalog, using `pkg/fppidentity`'s canonicalizer so the project has
one canonicalization rather than two. It covers the Show id, the generation,
the node id, and every Cue id, Cue revision, resolved output, and asset hash
in the catalog.

Content addressing is deliberate. A counter would need a home, an owner, and a
migration; a hash is reproducible from the inputs, so a node and the
coordinator can each compute it and disagreement is detectable rather than
assumed away. Two nodes with identical catalogs share a revision, which is
correct: the revision identifies the content, not the delivery.

Changing a Cue changes its revision, which changes the catalog revision, which
invalidates every outstanding authorization built on the old one.

## 4. Deployment and acknowledgement

```text
GET  /api/v1/nodes/{nodeId}/cue-catalog
POST /api/v1/nodes/{nodeId}/cue-catalog/acknowledge
```

The read is under `observation:read` and returns the resolved catalog with its
revision, generation, and Show id. The acknowledgement is the node reporting
which catalog revision it now holds, and is stored beside the node's asset
report, on the same shape Track E already uses for the per-node asset
manifest.

A node is `catalog-current` only when its acknowledged revision equals the one
the coordinator resolves now. Anything else is `catalog-stale` with both
revisions named. There is no partial state: a catalog is one object and a node
either holds this one or it does not.

**Acknowledging a catalog is not readiness.** A node can hold the right
catalog and still be missing an asset it names, and Track E's manifest remains
the authority on that. Readiness needs both, and H6 states the combination.

## 5. The authorization tuple

Every Cue activation and every dispatch derived from one carries, end to end:

| Field | From |
|---|---|
| `show` | the active Show |
| `generation` | section 2 |
| `catalogRevision` | section 3.1 |
| `playlist` and `playlistRevision` | the Playlist that selected the Cue |
| `entryId` | the Playlist entry, absent for a directly activated announcement |
| `cue` and `cueRevision` | the Cue |

H4 defines the envelope this travels in. H3 defines what is in it and what
checks it, so H4 has one thing to carry rather than a shape to invent.

## 6. Refusal, at both boundaries

The coordinator refuses to dispatch, and the node refuses to execute,
independently. Both refuse the same set, and the node's refusal is the one
that matters, because the coordinator can be absent.

| Case | Refusal |
|---|---|
| Show is not the node's authorized Show | `cross-show` |
| Generation is older than the node's | `stale-generation` |
| Generation is newer than the node's | `unknown-generation`, and the node re-fetches |
| Catalog revision does not match | `stale-catalog` |
| Cue id is not in the held catalog | `unknown-cue` |
| Cue revision differs from the held one | `stale-cue` |
| Asset named by the Cue is absent locally | `asset-missing` |

**A present file is never a reason to execute.** `asset-missing` is a refusal
about the catalog's expectation, and its converse is not a permission: a node
holding `Thriller.fseq` from last year's Show refuses a Halloween Cue under
`cross-show` without ever looking at its disk.

A refusal is a state with evidence, never a silent no-op, and never a
fallback to a different Cue, Playlist, or Show.

## 7. Losing and regaining authority

**Switching the active Show** applies H0.7: ShowMesh-owned audio stops, LTC
stops with it, rendering holds and reports `superseded`, FPP is untouched. The
previous catalog is invalid the moment the generation changes, so no new
activation on it can succeed even if a stale message is still in flight.

**A node restart does not restore held output.** The render agent resumes its
last persisted assignment at boot today. Under this seam a resumed assignment
whose authorization tuple does not match the node's current authorized Show,
generation, and catalog revision is discarded rather than applied, and the
node comes up cleared. Without this rule, rebooting a node in November puts
Halloween back on the wall.

**Coordinator or broker loss after deployment changes nothing.** The node
already holds its catalog and its authorization, which is the point of
deploying before playback: it keeps following the runner's position within
what it was already authorized to execute. It cannot be granted anything new
while the coordinator is gone, which is correct, and it does not lose what it
had, which is what keeps the show running.

**Recovery does not replay a stale Cue over a newer one.** An activation
carrying an older generation or an older Cue revision than the node's current
state is refused as stale rather than applied late. This is the same
full-state, position-carrying discipline MultiSync already uses, for the same
reason a relative adjustment applied twice is a different value.

## 8. Surfaces

- The two routes above, their OpenAPI, and regenerated UI types.
- `showmeshctl` verbs for both, including the acknowledgement, which the
  write-parity gate requires.
- `assetsync.ActiveShow.Generation`, and the catalog resolver beside the
  existing manifest builder.
- Node-side storage of the held catalog and its authorization tuple, on the
  agent's existing assignment-store shape.
- No Operator UI. H6 owns it.

## 9. What this seam does not do

- It does not activate anything. H4 does.
- It does not put the coordinator in the frame-rate timing path. A catalog is
  deployed before playback and read from local state during it.
- It does not replace Track E's asset manifest, and it does not become a
  second asset expectation model.
