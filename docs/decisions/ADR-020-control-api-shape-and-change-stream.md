# ADR-020: The Control API Is Versioned REST With a Server-Sent Events Change Stream

Status: Accepted  
Date: 2026-08-10

## Context

[ADR-014](ADR-014-operator-ui-is-an-api-client.md) decided *what the API is*, a
public, versioned contract usable without any UI, and deliberately left *what
it looks like* open, because there was nothing to shape it against yet. Build
Step 3 is where the first read-only surface is actually built, ahead of any UI,
which is the ordering ADR-014 exists to protect.

Three questions had to be answered before a line of it was written, and all
three were explicitly deferred to this point:

1. The real-time transport. [OPERATOR-UI §6](../architecture/OPERATOR-UI.md#6-real-time-updates)
   forbids depending solely on aggressive polling, records WebSocket and
   Server-Sent Events as the candidates, and defers the choice "with the API
   work in the build plan". OPERATOR-UI §17 lists it as an open question.
2. How a client learns it is talking to an incompatible coordinator, since
   ADR-014 makes version skew a permanent condition rather than an exceptional
   one.
3. What the API says when it has no evidence: the difference between "not
   supported", "not collected", "collection failed", and "collected but stale",
   which BUILD-PLAN Step 3 names as a requirement and
   [ADR-011](ADR-011-context-aware-observability.md) makes a correctness
   concern rather than a presentation one.

## Decision

**The control API is JSON over HTTP, with the major version in the path
(`/api/v1`), and a Server-Sent Events change stream at `/api/v1/stream`.**

1. **REST + JSON, major version in the path.** `/healthz`, `/readyz`, and
   `/version` remain outside the contract as infrastructure probes; they are
   what the container healthcheck uses and they are not versioned.

2. **Server-Sent Events, not WebSocket**, for the change stream. Three reasons,
   in order of weight:

   - **A contract that needs tooling to inspect drifts towards private.** BUILD-PLAN
     Step 3 requires the API be exercised by a non-UI client. With SSE that is
     `curl -N .../api/v1/stream` and you are reading the contract directly. With
     WebSocket, looking at it at all requires a client library first.
   - **The stream is strictly server to client.** Every command in the eventual
     write API is an addressable HTTP request carrying an idempotency key
     (ARCHITECTURE §8.1). WebSocket's bidirectionality is not a feature here; it
     is an invitation to put control semantics into a socket instead of into the
     versioned contract, which is precisely what ADR-014 forbids.
   - **No new dependency on either side.** `http.Flusher` in Go, a `fetch`
     streaming reader in the SPA.

   The accepted cost is HTTP/1.1's six-connections-per-origin limit. The UI
   opens one stream, and HTTP/2 removes the limit entirely.

3. **The snapshot is authoritative and the stream is never resumable.** A client
   fetches `/api/v1/snapshot`, then applies stream events. After *any*
   interruption it re-fetches the snapshot rather than resuming from its local
   model, per OPERATOR-UI §6. This is enforced structurally rather than
   documented:

   - The server emits **no `id:` field**, so a browser's `EventSource` never
     sends `Last-Event-ID`, and a `Last-Event-ID` request header is ignored.
     Emitting an `id:` would teach clients to ask for resume, and the first
     server that honours it has built the thing OPERATOR-UI forbids.
   - The per-event `seq` is **per connection**, starting at 1 on every
     connection, so it is useless as a global cursor and cannot quietly become
     one.
   - Every connection opens with a `stream.start` event carrying
     `snapshotRequired: true`.

4. **Dropped events are announced, never silent.** Each subscriber has a bounded
   buffer. A subscriber that overflows it receives a `stream.reset` event and is
   disconnected; it must re-snapshot. A slow client may never block the
   producer, grow the buffer without bound, or lose an event quietly. OPERATOR-UI §6
   observes that a gap in the stream is indistinguishable from a quiet system;
   this is what makes it distinguishable.

5. **Absence of evidence is stated, never omitted.** Every observation-bearing
   field carries a `state` from a fixed six-value vocabulary (`current`,
   `stale`, `unknown_age`, `not_collected`, `collection_failed`, `unsupported`)
   plus a `reason` whenever the state is not `current`. Omitting a field the
   coordinator cannot report is forbidden, because omission is how a client
   comes to believe a thing is fine.

   `unknown_age` exists because of a specific, already-costly fact: a retained
   MQTT delivery carries no valid observation time, so `observedAt` is `null`
   and must never be filled in from the collection time.

6. **Payloads carry absolute timestamps and a `serverTime`; never a precomputed
   age.** A client computes ages against `serverTime`, so clock skew is visible
   instead of silently wrong. This also keeps change detection honest: an age
   field would differ on every evaluation and turn the change stream into a
   firehose carrying no information.

7. **Version negotiation is explicit and machine-readable.** Every `/api/v1`
   response carries `ShowMesh-API-Version: 1`. A client may send the same header;
   if it names a version this coordinator does not serve, the response is an
   RFC 9457 `application/problem+json` document naming the supported versions.
   An unknown path version produces the same class of error rather than a bare
   404. This is what OPERATOR-UI §5.1 requires and what Step 4's acceptance
   criterion tests.

8. **Within a major version the contract is additive-only.** Fields may be
   added; never removed, renamed, or retyped. Clients must ignore unknown
   fields, and the published contract says so. A breaking change is `/api/v2`.

9. **`api/openapi.yaml` (OpenAPI 3.1) is the machine-readable contract, and it
   is machine-checked against real responses.** A hand-written specification
   drifts from its implementation, and a specification that lies is worse than
   none, so conformance is a test rather than a promise.

## Consequences

- Step 4's SPA reads the stream with `fetch` and a streaming reader rather than
  with `EventSource`. This is required anyway, because `EventSource` cannot set
  request headers, which both the API version header and
  [ADR-021](ADR-021-read-api-authentication-posture.md)'s bearer token need. It
  also means the browser never gets `EventSource`'s automatic `Last-Event-ID`
  resume behaviour, which decision 3 forbids.
- The coordinator must re-evaluate derived state on a timer, not only when
  evidence arrives. An observation transitions `current → stale` with no new
  input, and an operator must be told; a purely event-driven hub would leave a
  dashboard reading `current` indefinitely.
- The wire types are a separate layer from the domain types and are mapped
  between. This is duplication on purpose: if domain structs carried the JSON
  tags, an ordinary refactor would silently change a public contract and the
  version in the path would be a lie.
- Anything the coordinator cannot yet report costs a field with a `state` and a
  `reason` rather than an omission, so responses are larger and more verbose
  than a minimal status endpoint would be. That verbosity is the feature.
- Desired state, capability assignments, and reconciliation status are named in
  OPERATOR-UI §5 as part of the API's eventual minimum and are **not** in v1,
  because the coordinator does not model them. They arrive additively with the
  behaviour behind them. Shipping a `reconciliationStatus` that no code computes
  would be worse than its absence: a UI would render it and an operator would
  read it as a verdict.
- Per-connection sequence numbers make a global "what did I miss" query
  impossible by construction. Event *history* remains queryable by its own
  persistent sequence through `/api/v1/events?since=`; the two sequences are
  deliberately different things and are named differently.

## Alternatives considered

**WebSocket** was the serious competitor and is the more common choice. It was
rejected for the reasons in decision 2. Worth recording so it is not re-argued
from the wrong premise: the usual argument for WebSocket is bidirectionality,
and in ShowMesh bidirectionality is a liability rather than a benefit, because
commands must be individually addressable, authorizable, and idempotency-keyed.
If a future requirement genuinely needs client-to-server streaming, that is a
superseding ADR, not an extension of this one.

**Long polling** was rejected as strictly worse than SSE at the same cost.

**Resumable streams with a durable cursor**, the obvious "improvement" over
decision 3, were rejected because they make correctness depend on the server's
retention window matching the client's disconnection length, and they fail
silently when it does not. Re-snapshotting is cheap at this system's scale and
is always correct.

**MQTT over WebSocket direct to the browser** was already rejected in
OPERATOR-UI §6.1 and ADR-014, and nothing here reopens it.

**Omitting fields with no evidence** was rejected under ADR-011: a missing field
renders as blank, and blank reads as fine.

**GraphQL and gRPC** were not seriously considered. Both would make `curl` an
inadequate client, which decision 2's first reason rules out.

## Related research

[Telemetry storage and alerting](../research/RES-013-telemetry-storage-and-alerting.md) ·
[Device telemetry adapters](../research/RES-012-device-telemetry-adapters.md) ·
[Failure testing](../research/RES-009-failure-mode-testing.md)

Failure testing must include a stream interrupted mid-show, a subscriber that
stops reading, a coordinator restarted underneath a connected client, and a
client whose clock is materially skewed from the coordinator's.
