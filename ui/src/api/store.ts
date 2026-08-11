/**
 * The live model store: owns the connection state machine, the
 * snapshot+stream protocol from ADR-020, and the `Model` seam C reads
 * through `useModel()`. Framework-free — testable and (per spec section
 * 5) actually tested with no React present, driven directly against a
 * real `node:http` server.
 *
 * Connection algorithm, in outline (ADR-020, spec section 5.1):
 *
 *   1. Open `GET /stream`. Read frames as they arrive.
 *   2. The first frame is always `stream.start`. On it — and on any
 *      later `stream.reset` — fetch `GET /snapshot` (then `GET /events`
 *      for initial history) and apply them, BEFORE processing any frame
 *      that arrived after it. This is enforced structurally, not just by
 *      convention: frame processing is a single sequential `await` loop
 *      (runConnectionAttempt), so the loop simply does not advance to
 *      the next frame until the snapshot fetch it is waiting on
 *      resolves. There is no separate delta buffer to keep in sync,
 *      because nothing reads ahead while a snapshot fetch is in flight.
 *   3. `node.changed` / `fpp.changed` / `event.recorded` frames update
 *      the model in place once a snapshot has been applied.
 *   4. Any interruption — the stream closing, a network error, a
 *      `stream.reset` whose connection then closes — is handled the
 *      same way: reconnect (with backoff) and start over from step 1.
 *      Nothing here tries to distinguish a clean coordinator shutdown
 *      from a network fault, because the wire protocol does not let it
 *      (api/openapi.yaml's /stream description) and OPERATOR-UI section
 *      7 does not ask it to.
 *
 * What this store deliberately does NOT do: apply a delete. The stream
 * carries no deletions in v1 (api/openapi.yaml's /stream description,
 * spec section 5.1) — a node or FPP instance dropped from the
 * coordinator's inventory produces no frame, so `model.nodes` and
 * `model.fpp` can only ever be replaced wholesale by the next snapshot,
 * never shrunk by a delta. If a future contributor is tempted to add a
 * `node.removed` handler here: don't, until the wire contract adds the
 * event that would drive it.
 */
import { ApiClient, type FetchLike } from './client'
import { computeBackoffMs, DEFAULT_BACKOFF, type BackoffConfig } from './backoff'
import { SYSTEM_CLOCK, type Clock, type TimerHandle } from './clock'
import {
  asEventSeq,
  initialModel,
  type ConnectionState,
  type Event as ModelEvent,
  type EventSeq,
  type FPPInstance,
  type Model,
} from './domain'
import { IncompatibleVersionError, UnauthorizedError, isAbortError } from './errors'
import { SSEParser, type SSEFrame } from './sse'
import { sleep, waitUntilAborted } from './async-utils'
import { clearStoredToken, setStoredToken } from './token'
import type { components } from './generated/schema'

type SchemaSnapshot = components['schemas']['Snapshot']
type SchemaEventsResponse = components['schemas']['EventsResponse']
type SchemaNode = components['schemas']['Node']
type SchemaFPPInstance = components['schemas']['FPPInstance']
type SchemaEvent = components['schemas']['Event']

/**
 * UNMEASURED SHOWMESH HYPOTHESIS: how many events the in-browser model
 * keeps before trimming the oldest. Nothing has measured an operator's
 * actual working set during a show; this exists so a long-running tab
 * has a bounded memory footprint rather than growing without limit. It
 * bounds the *client-side* model only — it has no effect on the
 * coordinator's own retention (`oldestRetainedSeq`, `gap`), which is a
 * server-side policy this client just reports.
 */
const MAX_RETAINED_EVENTS = 500

/**
 * UNMEASURED SHOWMESH HYPOTHESIS: how many of the most recent events the
 * initial (and every re-snapshot's) `GET /events` fetch asks for. Chosen
 * to match the endpoint's own default `limit` (api/openapi.yaml: "100...
 * this coordinator's own pagination choice, not a measured or
 * contractually significant value"), not because 100 is independently
 * validated as the right operator working set.
 */
const INITIAL_EVENTS_WINDOW = 100

/**
 * UNMEASURED SHOWMESH HYPOTHESIS: how long an established `/stream`
 * connection may go without receiving ANY bytes — including a `:
 * keepalive` comment — before this client gives up on it as half-open
 * and reconnects (D2). The coordinator writes `: keepalive` on its own
 * fixed interval, which api/openapi.yaml itself calls "a ShowMesh-chosen
 * hypothesis, not a measured or contractually significant value" (15s as
 * of this writing — internal/coordinator/api/api.go's
 * defaultStreamKeepaliveInterval — but that default is not part of the
 * wire contract and this client must not assume it). This value is
 * deliberately not derived from that number; it is simply picked
 * comfortably above any plausible keepalive interval so an occasional
 * scheduling delay never produces a false disconnect, while still being
 * short enough that a genuinely dead connection (a partition, a Wi-Fi
 * handoff, a dropped NAT mapping) does not read as "Live" for long.
 */
const STREAM_IDLE_TIMEOUT_MS = 45_000

export interface ApiStoreOptions {
  /** Default '/api/v1' — same-origin per ADR-022. */
  baseUrl?: string
  fetchImpl?: FetchLike
  now?: () => number
  /** Override for tests only; production code should rely on the DEFAULT_BACKOFF hypothesis. */
  backoff?: BackoffConfig
  /** Override for tests only; production code relies on STREAM_IDLE_TIMEOUT_MS above. */
  streamIdleTimeoutMs?: number
  /** Override for tests only; production code relies on client.ts's DEFAULT_REQUEST_TIMEOUT_MS. */
  requestTimeoutMs?: number
  /**
   * Override for tests only (see clock.ts). Drives BOTH the stream
   * idle-deadline below and, via the ApiClient this store constructs,
   * the request timeout — production code always uses SYSTEM_CLOCK.
   */
  clock?: Clock
}

type Listener = () => void

export class ApiStore {
  private readonly client: ApiClient
  private readonly now: () => number
  private readonly backoffConfig: BackoffConfig
  private readonly streamIdleTimeoutMs: number
  private readonly clock: Clock

  private model: Model = initialModel()
  private readonly listeners = new Set<Listener>()

  private running = false
  private disposed = false
  private generation = 0
  private attempt = 0
  private lastError: string | null = null
  private loopAbort: AbortController | null = null

  constructor(options: ApiStoreOptions = {}) {
    this.clock = options.clock ?? SYSTEM_CLOCK
    this.client = new ApiClient(
      options.baseUrl ?? '/api/v1',
      options.fetchImpl,
      options.requestTimeoutMs,
      this.clock,
    )
    this.now = options.now ?? (() => Date.now())
    this.backoffConfig = options.backoff ?? DEFAULT_BACKOFF
    this.streamIdleTimeoutMs = options.streamIdleTimeoutMs ?? STREAM_IDLE_TIMEOUT_MS
  }

  // -- useSyncExternalStore surface ------------------------------------

  subscribe = (listener: Listener): (() => void) => {
    this.listeners.add(listener)
    return () => {
      this.listeners.delete(listener)
    }
  }

  getSnapshot = (): Model => this.model

  // -- lifecycle --------------------------------------------------------

  connect(): void {
    if (this.disposed || this.running) return
    this.running = true
    void this.runLoop()
  }

  dispose(): void {
    this.disposed = true
    this.loopAbort?.abort()
  }

  /**
   * Called after a `401` prompt (spec section 5.6) is answered. Stores
   * the token, then interrupts whatever the loop is currently doing —
   * a backoff wait, an indefinite `unauthorized` pause, or an in-flight
   * request — so the next attempt uses it immediately rather than
   * waiting out a stale backoff schedule.
   */
  submitToken(token: string): void {
    setStoredToken(token)
    this.attempt = 0
    this.loopAbort?.abort()
  }

  /** The only "logout": discard the stored secret and retry unauthenticated. */
  clearToken(): void {
    clearStoredToken()
    this.attempt = 0
    this.loopAbort?.abort()
  }

  // -- the loop -----------------------------------------------------------

  private setModel(next: Model): void {
    this.model = next
    for (const listener of this.listeners) listener()
  }

  private setConnection(connection: ConnectionState): void {
    this.setModel({ ...this.model, connection })
  }

  private async runLoop(): Promise<void> {
    while (!this.disposed) {
      this.loopAbort = new AbortController()
      const signal = this.loopAbort.signal

      if (this.attempt > 0) {
        const delay = computeBackoffMs(this.attempt, this.backoffConfig)
        this.setConnection({
          kind: 'reconnecting',
          attempt: this.attempt,
          nextAttemptAt: this.now() + delay,
          lastError: this.lastError ?? 'unknown error',
        })
        try {
          await sleep(delay, signal)
        } catch {
          if (this.disposed) break
          continue // interrupted by submitToken/clearToken: retry now
        }
      } else {
        this.setConnection({ kind: 'connecting' })
      }
      if (this.disposed) break

      const gen = ++this.generation
      try {
        await this.runConnectionAttempt(gen, signal)
        // runConnectionAttempt only resolves normally if the read loop
        // exits without throwing, which its own implementation never
        // does (every exit path below throws). Treat an unexpected
        // normal return defensively as a retryable failure rather than
        // silently looping forever on an assumption that turned out to
        // be wrong.
        this.attempt += 1
        this.lastError = 'stream ended unexpectedly'
        continue
      } catch (err) {
        if (isAbortError(err)) {
          if (this.disposed) break
          continue // interrupted deliberately: retry now, per submitToken/clearToken
        }

        if (err instanceof UnauthorizedError) {
          this.attempt = 0
          if (err.tokenWasPresent) {
            // Spec section 5.6: "clear the stored token and return to
            // unauthorized with a distinguishable ... detail, so a
            // wrong secret does not present as a missing one." Clearing
            // it is what makes the NEXT attempt (whether a manual
            // submitToken or an operator reload) present as "missing"
            // rather than silently retrying the same rejected secret.
            clearStoredToken()
          }
          this.setConnection({
            kind: 'unauthorized',
            reason: err.tokenWasPresent ? 'rejected' : 'missing',
          })
          // No backoff retry storm (spec section 5.4/5.6): pause here
          // until submitToken/clearToken aborts this signal, then retry
          // immediately with whatever token is now stored.
          try {
            await waitUntilAborted(signal)
          } catch {
            /* woken by submitToken/clearToken, or disposed */
          }
          if (this.disposed) break
          continue
        }

        if (err instanceof IncompatibleVersionError) {
          // Terminal (spec section 5.4): retrying against a coordinator
          // that will never serve this version is a loop, not a
          // recovery. Stop the loop entirely.
          this.setConnection({
            kind: 'incompatible',
            requiredVersion: err.requiredVersion,
            supportedVersions: err.supportedVersions,
            detail: err.message,
          })
          break
        }

        if (err instanceof Error) {
          // Every other classified error (network failure, a plain
          // ApiError from a non-2xx response, the stream closing) is
          // treated as retryable.
          this.attempt += 1
          this.lastError = err.message
          continue
        }

        // Defensive catch-all: something that isn't even an Error was
        // thrown. This should not be reachable by any code path in this
        // module; if it happens, retrying blindly is more likely to
        // mask a real bug than fix a transient condition, so this is
        // the one truly terminal, non-retryable state.
        this.setConnection({ kind: 'failed', detail: `unexpected error: ${String(err)}` })
        break
      }
    }
    this.running = false
  }

  // -- one connection attempt -------------------------------------------

  private async runConnectionAttempt(gen: number, signal: AbortSignal): Promise<void> {
    const response = await this.client.request('/stream', signal)
    if (response.body === null) {
      throw new Error('stream response had no body')
    }
    const reader = response.body.getReader()
    const parser = new SSEParser()
    try {
      for (;;) {
        const { value, done } = await this.readWithIdleTimeout(reader)
        if (gen !== this.generation) throw signal.reason ?? new Error('superseded')
        if (done) {
          // "A stream closes with a clean end-of-response and no
          // terminating frame of any kind" (api/openapi.yaml) —
          // indistinguishable from a network fault by design. Reconnect
          // and re-snapshot either way, exactly like any other
          // interruption.
          throw new Error('stream closed')
        }
        const frames = parser.push(value)
        for (const frame of frames) {
          await this.handleFrame(frame, gen, signal)
          if (gen !== this.generation) throw signal.reason ?? new Error('superseded')
        }
      }
    } finally {
      reader.cancel().catch(() => {
        /* connection is going away regardless; nothing to react to */
      })
    }
  }

  /**
   * `reader.read()`, but raced against an idle deadline (D2:
   * STREAM_IDLE_TIMEOUT_MS) so a half-open socket — one where the
   * connection accepted at the TCP level but nothing is arriving, ever,
   * not even a `: keepalive` comment — does not leave this loop awaiting
   * `read()` forever with the connection state stuck reporting `live`.
   * The deadline is naturally reset on every call: as soon as one read
   * resolves with ANY bytes (a real frame or just a keepalive comment,
   * both arrive as raw bytes to `reader.read()` before this class ever
   * parses them), the loop calls this method again and a fresh timer
   * starts. On expiry this throws a plain Error, which the caller treats
   * exactly like a socket drop or a parse failure: reconnect (with
   * backoff) and re-snapshot, per store.ts's header comment — this
   * function does not attempt to distinguish "idle timeout" from any
   * other interruption once it's outside this method.
   */
  private async readWithIdleTimeout(
    reader: ReadableStreamDefaultReader<Uint8Array>,
  ): Promise<ReadableStreamReadResult<Uint8Array>> {
    let timer: TimerHandle | undefined
    const idleDeadline = new Promise<never>((_resolve, reject) => {
      timer = this.clock.setTimeout(() => {
        reject(
          new Error(
            `stream received no bytes (not even a keepalive comment) for over ${this.streamIdleTimeoutMs}ms — treating the connection as dead`,
          ),
        )
      }, this.streamIdleTimeoutMs)
    })
    try {
      return await Promise.race([reader.read(), idleDeadline])
    } finally {
      if (timer !== undefined) this.clock.clearTimeout(timer)
    }
  }

  private async handleFrame(frame: SSEFrame, gen: number, signal: AbortSignal): Promise<void> {
    switch (frame.event) {
      case 'stream.start':
      case 'stream.reset': {
        // Both require an authoritative resnapshot before anything else
        // is applied (ADR-020 decision 3). This is deliberately
        // unconditional on successfully parsing the frame's own JSON:
        // even if that parse fails, resnapshotting is still correct —
        // for stream.start it always was, and a stream.reset whose body
        // failed to parse must not be read as "no reset happened".
        await this.reloadSnapshot(gen, signal)
        return
      }
      case 'node.changed': {
        const payload = tryParse<{ serverTime: string; node: SchemaNode }>(frame.data)
        if (payload === null || gen !== this.generation) return
        this.applyNodeChanged(payload.node, payload.serverTime)
        return
      }
      case 'fpp.changed': {
        const payload = tryParse<{ serverTime: string; instance: SchemaFPPInstance }>(frame.data)
        if (payload === null || gen !== this.generation) return
        this.applyFppChanged(payload.instance, payload.serverTime)
        return
      }
      case 'event.recorded': {
        const payload = tryParse<{ serverTime: string; event: SchemaEvent }>(frame.data)
        if (payload === null || gen !== this.generation) return
        this.applyEventRecorded(payload.event, payload.serverTime)
        return
      }
      default:
        // Unknown event: name — ignored, not an error. v1 is
        // additive-only (api/openapi.yaml's /stream description).
        return
    }
  }

  private async reloadSnapshot(gen: number, signal: AbortSignal): Promise<void> {
    const snapshot = await this.client.getJson<SchemaSnapshot>('/snapshot', signal)
    if (gen !== this.generation) return
    this.applySnapshot(snapshot)

    // D1 fix: `GET /events` with no `since` defaults to `since=0`, an
    // EXCLUSIVE lower bound over ascending seq (api/openapi.yaml, and
    // internal/coordinator/store/events.go's `WHERE seq > ? ORDER BY seq
    // ASC LIMIT ?`) — that returns the OLDEST retained page, not the
    // newest, once history exceeds one page. The snapshot's own
    // `latestEventSeq` is exactly the anchor api/openapi.yaml describes
    // for this ("fetch this snapshot once and then request exactly the
    // event history after it via GET /events?since="), so derive a
    // recent window from it instead of taking the endpoint's bare
    // default.
    //
    // `seq` is a durable but non-contiguous cursor — retention pruning
    // (pruneEvents) deletes rows, so `since` values do not correspond
    // 1:1 with row counts. `since = latestEventSeq - INITIAL_EVENTS_WINDOW`
    // can therefore return FEWER than INITIAL_EVENTS_WINDOW events (some
    // seqs in that range were pruned) but never MORE — that is the
    // correct, accepted behavior of an exclusive lower bound and is not
    // worked around here.
    const since = Math.max(0, snapshot.latestEventSeq - INITIAL_EVENTS_WINDOW)
    const events = await this.client.getJson<SchemaEventsResponse>(
      `/events?since=${since}&limit=${INITIAL_EVENTS_WINDOW}`,
      signal,
    )
    if (gen !== this.generation) return
    this.applyInitialEvents(events)

    this.attempt = 0
    this.lastError = null
    if (this.model.connection.kind !== 'live') {
      this.setConnection({ kind: 'live', connectedAt: this.now() })
    }
  }

  // -- model mutation -----------------------------------------------------

  private computeClockSkewMs(serverTimeIso: string, nowMs: number): number {
    // Positive means the coordinator's clock reads ahead of the
    // browser's. See domain.ts's Model.clockSkewMs and spec section 5.3:
    // this is raw bookkeeping for seam C to surface past a threshold,
    // not itself a judgement about which clock is "right". `nowMs` is
    // taken as a parameter (rather than calling `this.now()` internally)
    // so every caller below captures exactly one browser-clock reading
    // per mutation and reuses it for `serverTimeReceivedAt` too — the
    // skew figure and the effective-now anchor (app/time.ts's
    // effectiveServerTimeIso) must agree on what "the browser clock said
    // when this serverTime arrived" was.
    return Date.parse(serverTimeIso) - nowMs
  }

  private applySnapshot(snapshot: SchemaSnapshot): void {
    const receivedAt = this.now()
    this.setModel({
      ...this.model,
      serverTime: snapshot.serverTime,
      clockSkewMs: this.computeClockSkewMs(snapshot.serverTime, receivedAt),
      snapshotReceivedAt: receivedAt,
      serverTimeReceivedAt: receivedAt,
      // The API guarantees nodes ascending by nodeId already
      // (api/openapi.yaml Snapshot.nodes); re-sorting is a cheap,
      // defensive no-op against that guarantee, not a correction of it.
      nodes: [...snapshot.nodes].sort(compareByNodeId),
      fpp: snapshot.fpp.instances,
      collectors: snapshot.collectors,
    })
  }

  private applyInitialEvents(resp: SchemaEventsResponse): void {
    // D1 fix: this fires on EVERY re-snapshot (initial connect, and
    // every reconnect/stream.reset), not just the first one. Replacing
    // `model.events` wholesale with just this fetched page would discard
    // any event this store already applied live via `event.recorded`
    // frames since the last snapshot — those events are real and were
    // already shown to the operator; a later re-snapshot's window not
    // happening to cover them again is not evidence they didn't happen.
    // Reconcile instead: union the freshly fetched page with whatever is
    // already held, deduplicating by the durable `Event.seq`, so a
    // reconnect can only ever ADD to what the operator has already seen
    // (bounded by MAX_RETAINED_EVENTS), never silently drop it.
    const fetched = resp.events.map(toModelEvent)
    const events = mergeEventsBySeq(this.model.events, fetched)
    const receivedAt = this.now()
    this.setModel({
      ...this.model,
      serverTime: resp.serverTime,
      clockSkewMs: this.computeClockSkewMs(resp.serverTime, receivedAt),
      serverTimeReceivedAt: receivedAt,
      events,
      // Sticky true: gap means history was permanently lost to
      // retention (api/openapi.yaml's top-level description), so a
      // later fetch reporting gap:false — whether because retention
      // moved on or because this fetch's `since` window no longer
      // reaches the gap — cannot mean the lost events came back; they
      // cannot, by construction. Never clear a gap once seen.
      eventsGap: this.model.eventsGap || resp.gap,
      oldestRetainedSeq: resp.oldestRetainedSeq,
    })
  }

  private applyNodeChanged(node: SchemaNode, serverTime: string): void {
    const idx = this.model.nodes.findIndex((n) => n.nodeId === node.nodeId)
    const nodes =
      idx === -1
        ? [...this.model.nodes, node].sort(compareByNodeId)
        : replaceAt(this.model.nodes, idx, node)
    const receivedAt = this.now()
    this.setModel({
      ...this.model,
      serverTime,
      clockSkewMs: this.computeClockSkewMs(serverTime, receivedAt),
      serverTimeReceivedAt: receivedAt,
      nodes,
    })
  }

  private applyFppChanged(instance: SchemaFPPInstance, serverTime: string): void {
    const idx = this.model.fpp.findIndex((i) => i.instanceId === instance.instanceId)
    // FPPInstance ordering is "exactly as configured" (api/openapi.yaml
    // FPPResponse.instances), which a delta cannot know for an instance
    // it has never seen before — appending is a best-effort fallback for
    // that case, not a claim about configuration order.
    const fpp: FPPInstance[] =
      idx === -1 ? [...this.model.fpp, instance] : replaceAt(this.model.fpp, idx, instance)
    const receivedAt = this.now()
    this.setModel({
      ...this.model,
      serverTime,
      clockSkewMs: this.computeClockSkewMs(serverTime, receivedAt),
      serverTimeReceivedAt: receivedAt,
      fpp,
    })
  }

  private applyEventRecorded(event: SchemaEvent, serverTime: string): void {
    const branded = toModelEvent(event)
    const alreadyPresent = this.model.events.some((e) => e.seq === branded.seq)
    const events = alreadyPresent
      ? this.model.events
      : [branded, ...this.model.events].slice(0, MAX_RETAINED_EVENTS)
    const receivedAt = this.now()
    this.setModel({
      ...this.model,
      serverTime,
      clockSkewMs: this.computeClockSkewMs(serverTime, receivedAt),
      serverTimeReceivedAt: receivedAt,
      events,
    })
  }
}

export function createApiStore(options?: ApiStoreOptions): ApiStore {
  return new ApiStore(options)
}

// -- free functions ---------------------------------------------------------

function compareByNodeId(a: { nodeId: string }, b: { nodeId: string }): number {
  return a.nodeId.localeCompare(b.nodeId)
}

function replaceAt<T>(list: readonly T[], index: number, value: T): T[] {
  const next = list.slice()
  next[index] = value
  return next
}

function toModelEvent(event: SchemaEvent): ModelEvent {
  return { ...event, seq: asEventSeq(event.seq) }
}

/**
 * Unions two event lists by the durable `Event.seq` (a fetched page
 * winning over an in-memory entry with the same seq, since the server is
 * always the authoritative copy), sorts newest-first by seq — `seq` is a
 * strictly increasing durable cursor (domain.ts), so numeric descending
 * order on it IS recency order, with no timestamp collation involved —
 * and bounds the result to MAX_RETAINED_EVENTS. Used by
 * applyInitialEvents (D1) to reconcile a re-snapshot's fetched window
 * with whatever this store already holds, instead of replacing it.
 */
function mergeEventsBySeq(existing: readonly ModelEvent[], incoming: readonly ModelEvent[]): ModelEvent[] {
  const bySeq = new Map<EventSeq, ModelEvent>()
  for (const event of existing) bySeq.set(event.seq, event)
  for (const event of incoming) bySeq.set(event.seq, event)
  return [...bySeq.values()].sort((a, b) => b.seq - a.seq).slice(0, MAX_RETAINED_EVENTS)
}

function tryParse<T>(data: string): T | null {
  try {
    return JSON.parse(data) as T
  } catch {
    return null
  }
}
