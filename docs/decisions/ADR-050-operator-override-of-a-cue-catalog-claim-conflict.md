# ADR-050: An Operator May Override a Cue Catalog's Exclusive-Claim Conflict

Status: Accepted (owner, 2026-09-16)
Date: 2026-09-16

## Context

`TRACK-H` section H0.5 gives each Cue output an exclusive resource claim, and
refuses to deploy a Cue catalog to a node when two Cues that could be
concurrently active both hold the same claim (for example two Cues both driving
one node's `program-audio-route`). The refusal is a hard 409 at deploy time
(`internal/coordinator/api/cuecatalogdeploy.go`) and a hard readiness failure
(`ReadinessExclusiveClaimConflict`, Ready=false) at pre-show
(`internal/coordinator/fppreconcile/readiness.go`). It exists so two Cues never
fight one physical output during a live show.

The concurrency test that decides "could be concurrently active" is
deliberately conservative. `sameSinglePlaylist`
(`internal/coordinator/assetsync/cuecatalog.go`) exempts two Cues only when they
share a playlist, and it does not model whether two different playlists could
ever actually run at the same time. Its own doc comment names this residual
gap. As a result a legitimate authoring pattern (for example several versions
of one show as separate FPP playlists, each with its own showmesh-audio
playlist, sharing some Cues) can be refused with no operator recourse, and a
single authoring conflict can block a node's deploy and the whole pre-show
check.

ShowMesh's operating principle is that the operator stays in control and the
show continues. A refusal with no override contradicts that principle when the
operator understands the conflict and accepts the risk.

## Decision

### 1. A cue-catalog deploy accepts an operator override of the claim conflict

`POST /nodes/{nodeId}/cue-catalog/deploy` accepts `override` (default false).
When true, and the only refusal is an H0.5 exclusive-claim conflict, the deploy
proceeds and the response names every condition it overrode. `showmeshctl`
carries the same flag, and the Operator UI offers a "Deploy anyway" action only
after a deploy is refused for an overridable reason, behind an explicit risk
confirmation.

The override bypasses only the exclusive-claim conflict. A missing asset, an
unresolved show, or any other refusal still refuses, because those would deploy
a catalog the node cannot play. The override mechanism is general, but this
record makes only the exclusive-claim conflict overridable; adding another
overridable condition is a separate, explicit decision.

### 2. An override is recorded, revision-scoped, and attributed to an operator

The override is stored per node
(`internal/coordinator/store` `node_cue_catalog_override`), tied to the exact
deployed catalog revision, the conflicts it accepted, and the operator who
accepted them. It is recorded only once the node confirms it holds that
revision.

It never carries forward. A later catalog revision, or a conflict the recorded
override did not name, reads as not covered, so an override can never silently
outlive the conflict it accepted. Every catalog change re-requires an explicit
acceptance. A background or system principal can never be recorded as the
accepting operator; `AutoDeployCueCatalog` always deploys with override false.

### 3. Readiness reports an overridden conflict as an accepted risk, not a failure

Pre-show readiness (`exclusiveClaimReadiness` and `nightCheckNodeCatalogCurrent`)
downgrades an exclusive-claim conflict to a named warning, not `Ready=false`,
only while the recorded override covers the node's currently required revision
and every conflict that revision carries. A conflict that was not overridden, an
override recorded against a different revision, or a new conflict on the same
revision still fails readiness. Both readiness paths read one resolver
(`internal/coordinator/fppreconcile` `CueCatalogOverrideStatus`) so the decision
is made in one place.

### 4. Automatic deploy never overrides

The background reconciler that deploys stale catalogs never sets the override.
It continues to refuse a conflicting catalog and hold. Only an explicit operator
request may accept a conflict.

## Consequences

- The show can run past an exclusive-claim conflict the operator has seen and
  accepted, through the API, `showmeshctl`, and the UI at practical parity.
- Because the override is revision-scoped, editing any Cue or Playlist that
  changes a node's required catalog re-stales every node and re-requires the
  operator to accept the conflict again. This is deliberate: an override that
  survived a content change would accept a conflict the operator never saw.
- Readiness still tells the operator, as a warning, that a node is running under
  an accepted conflict, so an override is visible rather than silent.
- This record does not close H0.5's conservative concurrency test. Two Cues that
  can never actually run together are still reported as a conflict until an
  operator overrides it; modeling which playlists can run concurrently, so that
  case stops being flagged at all, remains a separate decision.

## Alternatives considered

**Downgrade the refusal to a warning for everyone.** Rejected. A genuinely
concurrent conflict (H0.5's blessed FPP-plus-one-audio-playlist case) would then
reach a live show, with two Cues fighting one output and no acceptance recorded.

**Make the concurrency test precise instead of adding an override.** Deferred,
not rejected. Modeling which playlists a show can run at once would remove the
false positives at their source, but it is a larger change to a specified safety
contract and does not by itself give an operator a way past a real conflict they
choose to accept. The override is the smaller, operator-controlled step; the
precision work remains open.

**Record the override per node without a revision.** Rejected. An override that
outlived the revision it was granted for would silently accept a different
catalog's conflicts, exactly the "desired and observed state stay separate"
error [ADR-003](ADR-003-desired-and-observed-state.md) forbids.

## Related decisions

- `TRACK-H` section H0.5: the exclusive-claim refusal this record makes
  operator-overridable.
- [ADR-045](ADR-045-multi-node-audio-and-roles.md) and
  [ADR-049](ADR-049-same-audio-on-several-nodes-at-one-instant.md): the
  multi-node audio targeting that made one node a target of several Cues, which
  is where these conflicts now surface.
- [ADR-014](ADR-014-operator-ui-is-an-api-client.md): the override is API-first
  and reaches the UI as a client, at parity with `showmeshctl`.
- [ADR-003](ADR-003-desired-and-observed-state.md): the revision-scoping rule
  follows its separation of desired and observed state.
