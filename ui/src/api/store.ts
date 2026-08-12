/**
 * The live model store: owns the connection state machine, the
 * snapshot+stream protocol from ADR-020, and the `Model` seam C reads
 * through `useModel()`. Framework-free — testable and (per spec section
 * 5) actually tested with no React present, driven directly against a
 * real `node:http` server.
 *
 * Connection algorithm, in outline (ADR-020, spec section 5.1):
 *
 *   1. Open `GET /stream?deltas=1` — this store always opts into
 *      observation delta frames (ADR-023 decision 1). A coordinator that
 *      has never heard of `?deltas=1` serves exactly what it always did;
 *      only the literal query value `1` changes anything server-side, so
 *      this is safe to send unconditionally. Read frames as they arrive.
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
 *      `fpp.observations.changed` frames also update the model in place,
 *      but with OPPOSITE merge semantics from `fpp.changed` — see
 *      `applyFppChanged` (replace) versus `applyFppObservationsChanged`
 *      (merge `changed`, delete `removed`) below, and ADR-023 decision 3a,
 *      which names confusing the two as the entire failure mode this
 *      feature can introduce that a full-frame-only client could not have.
 *   4. Any interruption — the stream closing, a network error, a
 *      `stream.reset` whose connection then closes — is handled the
 *      same way: reconnect (with backoff) and start over from step 1.
 *      Nothing here tries to distinguish a clean coordinator shutdown
 *      from a network fault, because the wire protocol does not let it
 *      (api/openapi.yaml's /stream description) and OPERATOR-UI section
 *      7 does not ask it to. This is also, structurally, why deltas
 *      introduce no resume logic: ADR-023 decision 4 is that a delta
 *      applies only to the baseline this store already holds, and step 2
 *      already guarantees that baseline is re-fetched from scratch on
 *      every reconnect — there is no cursor anywhere in this file, and
 *      none should ever be added; a gap in the stream is handled by
 *      re-snapshotting, never by reconciling.
 *
 * What this store deliberately does NOT do: apply a whole-RESOURCE
 * delete. The stream carries no node/FPP-instance deletions in v1
 * (api/openapi.yaml's /stream description, spec section 5.1) — a node or
 * FPP instance dropped from the coordinator's inventory produces no
 * frame, so `model.nodes` and the membership of `model.fpp` can only ever
 * be replaced wholesale by the next snapshot, never shrunk by a delta. If
 * a future contributor is tempted to add a `node.removed` handler here:
 * don't, until the wire contract adds the event that would drive it. This
 * is distinct from `fpp.observations.changed`'s `removed` list, which
 * this store DOES apply as a deletion — but only of individual
 * observations within an FPP instance that remains present, never of the
 * instance itself (ADR-023's own scoping of that field).
 */
import { ApiClient, type FetchLike } from './client'
import { computeBackoffMs, DEFAULT_BACKOFF, type BackoffConfig } from './backoff'
import { SYSTEM_CLOCK, type Clock, type TimerHandle } from './clock'
import {
  asEventSeq,
  initialModel,
  type ConnectionState,
  type Evidence,
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
type SchemaSessionResponse = components['schemas']['SessionResponse']
// BUILD-PLAN Step 7 seam B (RES-008 D2/D6).
type SchemaDiscoveryRunResponse = components['schemas']['DiscoveryRunResponse']
type SchemaNodeDeclarationResponse = components['schemas']['NodeDeclarationResponse']

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

  /**
   * ADR-024: independent, short-lived requests this store makes outside
   * the `/stream` read loop above — `GET /session` on connect(), and
   * `login`/`logout`/`claimBootstrap`. Tracked in a set (rather than one
   * field) because more than one can be in flight at once (the
   * connect()-time session fetch and the first reconnect's own refresh
   * race deliberately — see applySessionResponse's ordering guard) and so
   * dispose() can abort every one of them, the same guarantee loopAbort
   * already gives the read loop.
   */
  private readonly sideControllers = new Set<AbortController>()

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
    // ADR-024: "GET /api/v1/session is always open ... so the banner
    // costs one request." Fired independently of the read loop above,
    // not as part of it, because it must reach a state even when the
    // loop never does — a coordinator running with reads closed answers
    // `GET /stream` with a `401` before ever reaching reloadSnapshot's
    // own session refresh (see that method), and an anonymous device on
    // a coordinator with reads open still needs this on first paint to
    // learn it has never authenticated at all (decision 5's third case).
    const controller = this.beginSideCall()
    void this.fetchSession(controller.signal).finally(() => this.endSideCall(controller))
  }

  dispose(): void {
    this.disposed = true
    this.loopAbort?.abort()
    for (const controller of this.sideControllers) controller.abort()
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

  // -- ADR-024: sessions ------------------------------------------------
  //
  // These three are deliberately NOT folded into the read loop's ERROR
  // handling above: a wrong password submitted at the sign-in form is a
  // fact about ONE request, to be shown next to that form, and must never
  // flip the whole app's connection banner to "unauthorized". Each method
  // here lets its error propagate to the caller — the form component —
  // to render; only fetchSession (the background refresh) swallows its
  // own failures, because nothing is waiting synchronously on it.
  //
  // A SUCCESSFUL call is different: login/logout/claimBootstrap all end
  // by calling wakeReadLoop(), deliberately reaching into the read loop's
  // state — see that method's own comment for the real-browser-only
  // defect this exists to fix.

  private beginSideCall(): AbortController {
    const controller = new AbortController()
    this.sideControllers.add(controller)
    return controller
  }

  private endSideCall(controller: AbortController): void {
    this.sideControllers.delete(controller)
  }

  /**
   * Found only by loading this in a real browser against a coordinator
   * running with reads closed (`SHOWMESH_API_CLOSE_READS=true`) — the
   * exact class of defect this project's own standing lesson names: a
   * unit suite exercising `login()` in isolation has no read loop sitting
   * in `unauthorized` to fail to wake, so it cannot see this.
   *
   * Without this, a cookie-authenticated `login()`/`claimBootstrap()`
   * success updates `model.session` correctly, but the READ loop
   * (runLoop) — if it was already parked in `{ kind: 'unauthorized' }`
   * because reads are closed and this browser had no credential yet — has
   * no way to learn a credential now exists: it is sitting in
   * `waitUntilAborted(signal)`, and the only two things that have ever
   * aborted that wait are `submitToken`/`clearToken`. The persistent
   * session banner would then show "Signed in as ..." while the rest of
   * the dashboard stayed stuck on "Waiting for the first response from
   * the coordinator" indefinitely — an operator who just proved their
   * identity, looking at a UI that still won't show them anything.
   *
   * Mirrors submitToken/clearToken's own unconditional `attempt = 0` +
   * abort exactly, including firing even when the loop was NOT parked
   * waiting (the ordinary case: reads open, the stream never needed a
   * credential at all) — that costs one harmless extra reconnect
   * (isAbortError's "retry immediately, no backoff" branch), the same
   * accepted cost submitToken already pays on every call, and is
   * simpler and more robust than trying to detect which case applies.
   */
  private wakeReadLoop(): void {
    this.attempt = 0
    this.loopAbort?.abort()
  }

  /** `POST /api/v1/session` (ADR-024 decisions 1, 5, 8). Throws on invalid credentials, rate limiting, etc — the caller renders the failure. */
  async login(name: string, password: string, deviceLabel: string): Promise<void> {
    const controller = this.beginSideCall()
    try {
      const resp = await this.client.postJson<SchemaSessionResponse>(
        '/session',
        { name, password, deviceLabel },
        controller.signal,
      )
      this.applySessionResponse(resp)
      this.clearShadowingToken()
      this.wakeReadLoop()
    } finally {
      this.endSideCall(controller)
    }
  }

  /**
   * `DELETE /api/v1/session` (ADR-024 decisions 5, 6). No `sessionId`
   * revokes the session that authenticated THIS request — which must be
   * the cookie itself (server-enforced; see api/openapi.yaml) — the
   * ordinary "sign out this device" action. A `204` carries no body, so
   * the authoritative post-logout state (signed out, or — for a
   * `sessionId` naming some OTHER session — still signed in) is learned
   * by re-fetching rather than guessed at client-side.
   */
  async logout(sessionId?: string): Promise<void> {
    const controller = this.beginSideCall()
    try {
      await this.client.deleteJson<SchemaSessionResponse>(
        '/session',
        sessionId === undefined ? undefined : { sessionId },
        controller.signal,
      )
      await this.fetchSession(controller.signal)
      // See wakeReadLoop's own comment. Symmetrical with login: if reads
      // are closed and this was the credential the read loop was living
      // on, that loop must re-attempt now and land on 'unauthorized'
      // promptly, rather than an already-live stream (revalidated on its
      // own schedule — ADR-024 decision 5) being the only thing that
      // eventually notices.
      this.wakeReadLoop()
    } finally {
      this.endSideCall(controller)
    }
  }

  /** `POST /api/v1/bootstrap` (ADR-024 decision 9). Throws on an invalid/claimed/expired code — the caller renders the failure. */
  async claimBootstrap(code: string, name: string, password: string, deviceLabel: string): Promise<void> {
    const controller = this.beginSideCall()
    try {
      const resp = await this.client.postJson<SchemaSessionResponse>(
        '/bootstrap',
        { code, name, password, deviceLabel },
        controller.signal,
      )
      this.applySessionResponse(resp)
      this.clearShadowingToken()
      this.wakeReadLoop()
    } finally {
      this.endSideCall(controller)
    }
  }

  // -- BUILD-PLAN Step 7 seam B: node discovery and declaration ---------
  //
  // Unlike login/logout/claimBootstrap above, these three do NOT call
  // wakeReadLoop() or touch `this.model` directly: a discovery run or a
  // declaration change is node/config data, not identity, so its effect
  // reaches the model through the ordinary path every other node change
  // does — the next SSE `node.changed` frame or hub tick (contract
  // section 6.5) — never a client-side guess at what the coordinator now
  // holds. Each still uses beginSideCall/endSideCall so an in-flight
  // request is tracked and aborted on dispose() exactly like the session
  // calls above.

  /**
   * `POST /api/v1/discovery/runs` (RES-008 D2/D6). Reads what the
   * coordinator already observes and proposes what is not currently
   * declared; never creates, modifies, or deletes a declaration by
   * itself (ADR-003). Throws on 401/403/500 — the caller renders the
   * failure.
   */
  async runDiscovery(): Promise<SchemaDiscoveryRunResponse> {
    const controller = this.beginSideCall()
    try {
      return await this.client.postJson<SchemaDiscoveryRunResponse>('/discovery/runs', undefined, controller.signal)
    } finally {
      this.endSideCall(controller)
    }
  }

  /**
   * `POST /api/v1/nodes/{nodeId}/declaration`. Promotes nodeId to
   * declared, or — idempotently — updates its label/notes. Throws on
   * 401/403/500, including when the audit store fails and ADR-024
   * decision 11's same-transaction rule refuses the whole write.
   */
  async declareNode(nodeId: string, label: string, notes: string): Promise<SchemaNodeDeclarationResponse> {
    const controller = this.beginSideCall()
    try {
      return await this.client.postJson<SchemaNodeDeclarationResponse>(
        `/nodes/${encodeURIComponent(nodeId)}/declaration`,
        { label, notes },
        controller.signal,
      )
    } finally {
      this.endSideCall(controller)
    }
  }

  /**
   * `DELETE /api/v1/nodes/{nodeId}/declaration`. The server itself
   * requires `{"confirm":true}` in the body (BUILD-PLAN Step 7 seam B
   * B2) — this method always sends it, so the UI-level confirmation
   * dialog (NodesList.tsx) is what actually gates the call from ever
   * being made, in addition to, never instead of, the server's own
   * check.
   */
  async deleteNodeDeclaration(nodeId: string): Promise<void> {
    const controller = this.beginSideCall()
    try {
      await this.client.deleteJson(
        `/nodes/${encodeURIComponent(nodeId)}/declaration`,
        { confirm: true },
        controller.signal,
      )
    } finally {
      this.endSideCall(controller)
    }
  }

  /**
   * ADR-024 decision 6 / client.ts's `request()`: "an Authorization
   * header, if present at all, is the only credential path considered
   * for this request" — a stored break-glass token always wins over the
   * cookie the browser would otherwise attach, on EVERY request,
   * including the ones this store makes right after a successful
   * cookie-authenticated login. Left alone, a token that was revoked (or
   * simply wrong) permanently shadows a session this operator just
   * proved is valid: every subsequent request keeps presenting the dead
   * token, the coordinator keeps answering as if uncredentialed, and
   * `GET /session` keeps reporting `authenticated: false` forever — the
   * persistent sign-in banner (SessionPanel.tsx) would show "signed out"
   * even immediately after a login this method just confirmed succeeded.
   * A successful login or bootstrap claim is exactly the signal that the
   * cookie path works again, so this clears whatever token was stored
   * rather than leaving it to keep winning silently. (The operator also
   * has a direct way to do this without signing in first — see
   * SessionPanel.tsx's "Clear stored token" affordance, which calls
   * `clearToken()` below.)
   */
  private clearShadowingToken(): void {
    clearStoredToken()
  }

  /**
   * `GET /api/v1/session`, silently. Never throws: this is the background
   * refresh fired at connect() and on every stream reconnect
   * (reloadSnapshot below), and nothing is synchronously waiting on it to
   * report a failure to. A failure instead sets `sessionFetchFailed`,
   * which app/session.ts's scope-gate treats as "cannot currently vouch
   * for this" — the ADR-024 decision 12 degradation — while leaving the
   * last-known `session` in place rather than discarding it (a momentary
   * blip must not flash "signed out" over a session that is actually
   * fine).
   */
  private async fetchSession(signal: AbortSignal): Promise<void> {
    try {
      const resp = await this.client.getJson<SchemaSessionResponse>('/session', signal)
      this.applySessionResponse(resp)
    } catch (err) {
      if (isAbortError(err)) return
      this.setModel({ ...this.model, sessionFetchFailed: true })
    }
  }

  /**
   * Applies a `SessionResponse` from any of the four call sites above.
   * Guarded by `serverTime` rather than arrival order: connect()'s
   * independent fetch and the first reconnect's own reloadSnapshot
   * refresh are fired close together on purpose (see connect()'s
   * comment), and either may resolve second — this store's existing
   * posture (store.ts's header comment; computeClockSkewMs) is that the
   * coordinator's own clock, not receipt order, is authoritative, so a
   * response that is (by serverTime) OLDER than what's already held is
   * dropped rather than allowed to overwrite fresher data with stale.
   * An unparseable serverTime is treated as "not older" (applied
   * anyway) rather than silently discarded — this mirrors
   * IncompatibleVersionError's own posture of surfacing a malformed
   * server response instead of pretending it didn't happen.
   */
  private applySessionResponse(resp: SchemaSessionResponse): void {
    const incomingMs = Date.parse(resp.serverTime)
    const currentMs = this.model.session === null ? -Infinity : Date.parse(this.model.session.serverTime)
    if (!Number.isNaN(incomingMs) && !Number.isNaN(currentMs) && incomingMs < currentMs) return
    this.setModel({
      ...this.model,
      session: resp,
      sessionReceivedAt: this.now(),
      sessionFetchFailed: false,
    })
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
          // ADR-024 item 7: "a closed change stream may mean a revoked
          // session ... the client's existing reconnect and snapshot path
          // then hits a 401, which must surface as an explicit
          // authentication state." That's the `setConnection` call below,
          // unchanged since Step 4 — this refresh is what ALSO updates
          // the persistent sign-in banner (app/session.ts,
          // model.session), which is a separate piece of state from
          // `connection` and would otherwise still read "signed in" from
          // whatever /session last reported, stale, until the operator
          // happened to trigger another read of it. Fire-and-forget: the
          // 'unauthorized' connection state below must render immediately
          // regardless of how long this fetch takes, and fetchSession
          // never throws.
          void this.fetchSession(signal)
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
    // ADR-023 decision 1: `?deltas=1` is the ONLY literal value that opts a
    // connection into `fpp.observations.changed` frames; sent
    // unconditionally here rather than behind a flag because a coordinator
    // that predates ADR-023 (or any other value it might see) serves this
    // connection exactly what it always served — additive by construction,
    // nothing for this client to negotiate.
    const response = await this.client.request('/stream?deltas=1', signal)
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
      case 'fpp.observations.changed': {
        // ADR-023 decision 3a: this frame's payload shape (a bag of
        // `changed`/`removed`, no whole-`instance` field at all) is
        // structurally incapable of being handed to applyFppChanged's
        // replacement path — there is nothing here to replace WITH. That
        // is deliberate: it is what stops this call site from being able
        // to confuse the two merge semantics, not just a naming
        // convention.
        const payload = tryParse<{
          serverTime: string
          instanceId: string
          changed: Evidence[]
          removed: string[]
        }>(frame.data)
        if (payload === null || gen !== this.generation) return
        this.applyFppObservationsChanged(payload.instanceId, payload.changed, payload.removed, payload.serverTime)
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

    // ADR-024 decision 12: a reconnect is exactly the moment the coordinator
    // may have closed the PREVIOUS connection over a generation bump (decision
    // 5's "closes open streams and forces a re-fetch, so the stale window is
    // bounded") — refreshing session/scopes here, on every reconnect, is what
    // makes that bound real on the client side rather than only asserted
    // coordinator-side. Swallows its own errors (see fetchSession) so a
    // /session hiccup never blocks reaching `live`.
    await this.fetchSession(signal)
    if (gen !== this.generation) return

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

  /**
   * `fpp.changed` carries an FPP instance's COMPLETE current
   * representation (ADR-023 decision 3a) and is therefore always a
   * REPLACEMENT of whatever this store held for that instanceId — never a
   * merge. This is the same posture this method has always had, since
   * before deltas existed; it stays a plain whole-object replace so that a
   * connection that never asked for deltas (and, per decision 3, a
   * delta-subscribed connection's OWN structural-change frames) observes
   * no difference from before ADR-023.
   */
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

  /**
   * `fpp.observations.changed` carries only what moved (ADR-023 decision
   * 3a) and is therefore always a MERGE onto the instance's existing
   * `observations` — `changed` entries are folded in by `Evidence.signal`
   * (updating an existing signal in place or appending a genuinely new
   * one) and every signal named in `removed` is deleted outright. This is
   * the opposite operation from `applyFppChanged` immediately above, on
   * purpose: see `mergeFppObservations`'s own doc comment for how the two
   * are kept from being confused at a call site.
   *
   * If this connection has no existing instance for `instanceId` at all,
   * the frame is dropped rather than synthesizing a partial instance from
   * a delta alone: ADR-023 decision 4 says a delta applies to a baseline
   * this client already has, and the snapshot-before-delta ordering this
   * store enforces (see this file's header comment, step 2) is exactly
   * what is supposed to guarantee that baseline exists before any delta
   * for it can arrive. An unseen instanceId here would mean that
   * guarantee was violated somewhere upstream, and the correct response to
   * an unmergeable delta is to do nothing and wait for the next
   * authoritative snapshot or fpp.changed to supply the instance in full —
   * not to guess at a shape the wire contract never sent.
   */
  private applyFppObservationsChanged(
    instanceId: string,
    changed: readonly Evidence[],
    removed: readonly string[],
    serverTime: string,
  ): void {
    const idx = this.model.fpp.findIndex((i) => i.instanceId === instanceId)
    if (idx === -1) return

    const instance = this.model.fpp[idx]
    // Unreachable: idx came from findIndex over this exact array two
    // lines above, so the -1 branch already returned. noUncheckedIndexedAccess
    // still types this access as possibly undefined; this guard satisfies
    // the type checker rather than papering over a real gap.
    if (instance === undefined) return
    const nextInstance: FPPInstance = {
      ...instance,
      observations: mergeFppObservations(instance.observations, changed, removed),
    }
    const fpp = replaceAt(this.model.fpp, idx, nextInstance)
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

/**
 * Applies ADR-023's delta merge semantics: `changed` is folded onto
 * `existing` by `Evidence.signal` (an existing signal is updated in
 * place, preserving `existing`'s relative order; a signal `existing`
 * never had is appended, in `changed`'s own order — already sorted
 * server-side, see internal/coordinator/api's sortObservations), and every
 * signal named in `removed` is deleted from the result outright.
 *
 * This function's signature is the actual mechanism that keeps this merge
 * from ever being confused with `applyFppChanged`'s replacement at a call
 * site: it takes an observations ARRAY plus a small delta, never a whole
 * `FPPInstance` to replace one with, so there is no value of this
 * function's parameters that could accidentally perform a replace, and no
 * caller can pass this function's result where a full instance is
 * expected — the return type is `Evidence[]`, not `FPPInstance`.
 */
function mergeFppObservations(
  existing: readonly Evidence[],
  changed: readonly Evidence[],
  removed: readonly string[],
): Evidence[] {
  const removedSignals = new Set(removed)
  const bySignal = new Map<string, Evidence>()
  for (const o of existing) {
    if (!removedSignals.has(o.signal)) bySignal.set(o.signal, o)
  }
  for (const o of changed) {
    bySignal.set(o.signal, o)
  }

  // Preserve `existing`'s own relative order for every signal that
  // survives (untouched or updated in place), then append any genuinely
  // new signal `changed` introduced, in the order `changed` listed it.
  const order: string[] = []
  const seen = new Set<string>()
  for (const o of existing) {
    if (bySignal.has(o.signal) && !seen.has(o.signal)) {
      order.push(o.signal)
      seen.add(o.signal)
    }
  }
  for (const o of changed) {
    if (!seen.has(o.signal)) {
      order.push(o.signal)
      seen.add(o.signal)
    }
  }

  return order.map((signal) => {
    const evidence = bySignal.get(signal)
    if (evidence === undefined) {
      // Unreachable: `order` is built exclusively from signals already
      // confirmed present in `bySignal` above (either by the `bySignal.has`
      // guard in the first loop, or by construction in the second, since
      // every `changed` entry is unconditionally written into `bySignal`
      // before this point). A non-null assertion would silence the same
      // guarantee less legibly; this throws instead, matching this
      // package's existing posture (see stream.go's mustObservation and
      // its "internal invariant violation" panics) of preferring a loud
      // failure over trusting an assertion nothing here re-checks.
      throw new Error(`mergeFppObservations: internal invariant violated for signal "${signal}"`)
    }
    return evidence
  })
}

function tryParse<T>(data: string): T | null {
  try {
    return JSON.parse(data) as T
  } catch {
    return null
  }
}
