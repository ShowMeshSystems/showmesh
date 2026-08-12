/**
 * The client-facing domain model: the shape `useModel()` (useModel.ts)
 * exposes to the rest of the UI, and the types the store (store.ts)
 * maintains internally.
 *
 * Resource shapes (Node, FPPInstance, CollectorStatus, Event) are aliased
 * directly from the generated wire schema (generated/schema.d.ts) rather
 * than hand-copied, per ADR-015's "generated from or verified against the
 * Go types" requirement — a hand-maintained second copy would drift.
 */
import type { components } from './generated/schema'

// ---------------------------------------------------------------------
// Branded sequence numbers.
//
// api/openapi.yaml is explicit that these are two different things that
// happen to both be called "seq": Event.seq (this file's EventSeq) is a
// durable, strictly increasing cursor into GET /events history. The `seq`
// on an SSE stream frame (StreamSeq) is per-connection, starts at 1 on
// every reconnect, and ADR-020 decision 3 forbids ever treating it as a
// cursor. They are structurally both `number` on the wire, so nothing
// stops a client from accidentally comparing or persisting the wrong one
// — except the type system, if the two are distinct types. That's what
// this branding buys: an EventSeq and a StreamSeq are not mutually
// assignable, even though both erase to `number` at runtime.
// ---------------------------------------------------------------------
declare const eventSeqBrand: unique symbol
export type EventSeq = number & { readonly [eventSeqBrand]: true }

declare const streamSeqBrand: unique symbol
export type StreamSeq = number & { readonly [streamSeqBrand]: true }

export function asEventSeq(value: number): EventSeq {
  return value as EventSeq
}

export function asStreamSeq(value: number): StreamSeq {
  return value as StreamSeq
}

// ---------------------------------------------------------------------
// Resource shapes, aliased from the generated schema.
// ---------------------------------------------------------------------
export type Node = components['schemas']['Node']
export type FPPInstance = components['schemas']['FPPInstance']
export type CollectorStatus = components['schemas']['CollectorStatus']
export type Capability = components['schemas']['Capability']
export type ControlPlane = components['schemas']['ControlPlane']
export type NodeEvidence = components['schemas']['NodeEvidence']
export type EvidenceState = components['schemas']['Evidence']['state']
export type Evidence = components['schemas']['Evidence']
export type ResourceRef = components['schemas']['ResourceRef']

// ADR-024: the session/identity shapes, aliased from the generated schema
// for the same reason as every type above (ADR-015: generated from or
// verified against the Go types, never hand-copied a second time).
export type SessionResponse = components['schemas']['SessionResponse']
export type PrincipalSummary = components['schemas']['PrincipalSummary']
export type SessionInfo = components['schemas']['SessionInfo']

// BUILD-PLAN Step 7 seam B (RES-008 D2/D6): node discovery and
// declaration. NodeDeclaration is also reachable as Node['declaration'],
// aliased here separately for call sites (DomainBadges.tsx,
// NodesList.tsx) that only ever need the declaration block on its own.
export type NodeDeclaration = components['schemas']['NodeDeclaration']
export type DiscoveryRun = components['schemas']['DiscoveryRun']
export type DiscoveryProposal = components['schemas']['DiscoveryProposal']

/**
 * One recorded event, as held in the model. Identical to the wire
 * `Event` schema except `seq` is branded EventSeq rather than a bare
 * `number` — see the brand comment above.
 */
export interface Event extends Omit<components['schemas']['Event'], 'seq'> {
  seq: EventSeq
}

// ---------------------------------------------------------------------
// Connection state machine (spec section 5.4).
// ---------------------------------------------------------------------
export type ConnectionState =
  | { kind: 'connecting' }
  | { kind: 'live'; connectedAt: number }
  | {
      kind: 'reconnecting'
      attempt: number
      nextAttemptAt: number
      lastError: string
    }
  // Deviation from the spec section 5.4 code block, recorded here rather
  // than silently: that block shows `{ kind: 'unauthorized' }` with no
  // further fields, but section 5.6 requires distinguishing "no token
  // supplied yet" from "the supplied token was rejected" ("a wrong
  // secret does not present as a missing one"). A bare `unauthorized`
  // cannot express that distinction, so this adds `reason`. Reported to
  // the orchestrator per spec section 9.
  | { kind: 'unauthorized'; reason: 'missing' | 'rejected' }
  | {
      kind: 'incompatible'
      requiredVersion: number
      supportedVersions: number[]
      detail: string
    }
  | { kind: 'failed'; detail: string }

// ---------------------------------------------------------------------
// The model (spec section 5.5).
// ---------------------------------------------------------------------
export interface Model {
  connection: ConnectionState
  serverTime: string | null
  clockSkewMs: number | null
  /** Browser clock, for "last updated" — see spec section 5.5. */
  snapshotReceivedAt: number | null
  /**
   * Browser clock (`Date.now()`) at the moment the current `serverTime`
   * value was captured — paired with `serverTime` the same way
   * `snapshotReceivedAt` pairs with "last updated" above. Every code path
   * that sets `serverTime` (store.ts's applySnapshot, applyInitialEvents,
   * applyNodeChanged, applyFppChanged, applyEventRecorded) sets this
   * alongside it. This is what lets a view derive an effective "now" that
   * keeps advancing between responses — most importantly while
   * disconnected, when nothing updates `serverTime` itself — without ever
   * computing an age against the raw browser clock (see app/time.ts's
   * effectiveServerTimeIso and this file's header comment on why
   * `serverTime`, not the browser clock, is the reference). Null exactly
   * when `serverTime` is: before the first response.
   */
  serverTimeReceivedAt: number | null
  /** Ordered by nodeId, as the API guarantees. */
  nodes: Node[]
  fpp: FPPInstance[]
  collectors: CollectorStatus[]
  /** Newest first, bounded — see MAX_RETAINED_EVENTS in store.ts. */
  events: Event[]
  /**
   * `true` once any fetched page of event history reported `gap: true`
   * (history permanently lost to retention). Never cleared by a retry:
   * the events that made it true no longer exist anywhere in this
   * system (api/openapi.yaml's top-level description), so retrying
   * cannot un-set it — see store.ts's applyInitialEvents.
   */
  eventsGap: boolean
  oldestRetainedSeq: number | null

  /**
   * The last `GET/POST /api/v1/session` response this store received, or
   * `null` before the first one arrives (spec section 5.5's "before the
   * first response" pattern, reused here). `null` must never be read as
   * "signed out": that is `session !== null && session.authenticated ===
   * false`, a distinct, positively-known state — see
   * app/session.ts's `describeSignInState`, the one place this
   * distinction is turned into what the persistent banner (ADR-024
   * decision 5's "signed out is a persistent state") actually shows.
   */
  session: SessionResponse | null
  /** Browser clock, paired with `session` the same way `snapshotReceivedAt` pairs with the model's data. */
  sessionReceivedAt: number | null
  /**
   * `true` when the MOST RECENT attempt to fetch `/session` failed (network
   * error, non-2xx, or a body that failed to parse) — `session` above is
   * then a stale, possibly-wrong last-known value rather than freshly
   * confirmed. ADR-024 decision 12: "a stale or unavailable [scope list]
   * renders as unknown, never as permissive" — this is the flag
   * `app/session.ts`'s scope-gate reads to force that degradation,
   * exactly as `eventsGap` above is a flag a view reads rather than the
   * store silently discarding the events it already has. Cleared back to
   * `false` by the next fetch that succeeds, unlike `eventsGap` (which is
   * sticky for a different, permanent reason — see that field's comment);
   * this one is a transient "can we currently vouch for this" bit, not a
   * record of data permanently lost.
   */
  sessionFetchFailed: boolean
}

export function initialModel(): Model {
  return {
    connection: { kind: 'connecting' },
    serverTime: null,
    clockSkewMs: null,
    snapshotReceivedAt: null,
    serverTimeReceivedAt: null,
    nodes: [],
    fpp: [],
    collectors: [],
    events: [],
    eventsGap: false,
    oldestRetainedSeq: null,
    session: null,
    sessionReceivedAt: null,
    sessionFetchFailed: false,
  }
}
