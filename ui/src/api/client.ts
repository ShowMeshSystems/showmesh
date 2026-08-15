import {
  ApiError,
  CSRFRejectedError,
  ForbiddenError,
  IncompatibleVersionError,
  TooManyRequestsError,
  UnauthorizedError,
} from './errors'
import { PROBLEM_TYPE, type Problem } from './problem'
import { getStoredToken } from './token'
import { SYSTEM_CLOCK, type Clock, type TimerHandle } from './clock'

/**
 * The API major version this client requires (spec section 5.4: "Send
 * `ShowMesh-API-Version: 1` on every request"). api/openapi.yaml pins
 * the header's response value to the literal "1" today; this constant
 * is this client's own expectation, kept separate from that schema
 * constraint so a future v2 client only has to change this one number.
 */
export const REQUIRED_API_VERSION = 1
const API_VERSION_HEADER = 'ShowMesh-API-Version'

export type FetchLike = typeof fetch

/**
 * The subset of `RequestInit` this client's callers actually need:
 * `method` for `POST`/`DELETE`, and a plain-object `body` this client
 * JSON-encodes itself (see [ApiClient.request]'s own comment) rather than
 * the raw `BodyInit` `fetch` expects — every caller has an object, never a
 * pre-encoded string, so encoding it here is one fewer thing every call
 * site has to get right identically.
 */
export interface JsonRequestInit {
  method?: string
  body?: unknown
}

/**
 * UNMEASURED SHOWMESH HYPOTHESIS: how long a request may take before this
 * client gives up and treats it as failed (retryable, same as any other
 * network error). Nothing has measured a real coordinator's response
 * latency under load; this exists so a wedged TCP connection or a
 * coordinator that accepted the connection but never writes a response
 * doesn't hang the store's connection attempt indefinitely with no
 * visible state change (D2: a half-open connection must not render as
 * live/connecting forever). This bounds getting a response's headers —
 * for `/stream` specifically, once headers arrive this timeout is done;
 * see store.ts's STREAM_IDLE_TIMEOUT_MS for the separate bound on an
 * established stream going quiet.
 */
const DEFAULT_REQUEST_TIMEOUT_MS = 15_000

/**
 * Step 7 seam C review defect 1: the request budget for
 * `POST /fpp/{instanceId}/commands` (store.ts's `stopFPPPlaylist`) must
 * not be [DEFAULT_REQUEST_TIMEOUT_MS] — a command dispatch is a long
 * request BY DESIGN (the coordinator waits out its own confirmation
 * deadline, `internal/coordinator/api`'s `defaultFPPCommandConfirmDeadline`,
 * 20s, before answering) and sharing a snapshot read's 15s budget made
 * the coordinator's own honest "unconfirmed" outcome unreachable: this
 * client aborted first, every time, rendering a transport-timeout error
 * for what was a successful conversation with a healthy coordinator —
 * the exact inverted failure direction ADR-024 decision 7 exists to
 * name.
 *
 * A THIRD independently chosen literal, deliberately: this is
 * TypeScript and cannot import
 * `pkg/command.DefaultFPPCommandConfirmDeadline`/`MinClientTimeoutForConfirmation`
 * (the Go module boundary is absolute here, not a style choice), so — like
 * cmd/showmeshctl's own `minFPPCommandClientTimeout`, chosen
 * independently for the identical reason — this cannot be DERIVED from the
 * server's value, only reconciled against it: `client.test.ts`'s
 * `describe('FPP_COMMAND_REQUEST_TIMEOUT_MS', ...)` block does that
 * reconciliation two ways — a static assertion against
 * [MIN_FPP_COMMAND_CLIENT_TIMEOUT_MS] below that fails the build the day
 * this constant is ever set too small again, and a behavioral test
 * (`'actually waits this long: a response arriving just before the
 * deadline still succeeds'`) that proves this client does not abort a
 * slow-but-healthy response early. Both were verified non-vacuous by
 * setting this constant to 6_000 (a value that previously made 389/389
 * tests pass, typecheck, lint, and build all green — Step 8 client-side
 * review finding 1) and confirming both fail, then restoring it. The
 * acceptance criteria for the ORIGINAL fix (Step 7 seam C) were also
 * verified against the running stack (CLAUDE.md's own standing rule)
 * rather than trusted from three numbers that merely look consistent on
 * paper.
 *
 * 35s = the coordinator's 20s default confirmation deadline + a 15s
 * margin for the round trip itself — the same value and reasoning as
 * cmd/showmeshctl's `minFPPCommandClientTimeout` and
 * `pkg/command.ClientTimeoutMargin`, chosen independently here rather than
 * shared, for the reason above.
 */
export const FPP_COMMAND_REQUEST_TIMEOUT_MS = 35_000

/**
 * The reconciliation TARGET for [FPP_COMMAND_REQUEST_TIMEOUT_MS] — never
 * itself the value a request uses. A FOURTH independently chosen literal,
 * for the identical module-boundary reason that constant's own doc
 * comment gives: this is `pkg/command.MinClientTimeoutForConfirmation(
 * pkg/command.DefaultFPPCommandConfirmDeadline)` reconciled by hand, not
 * imported, deliberately kept as two named, commented components (rather
 * than one opaque `35_000`) so a reviewer can check each half against the
 * Go source directly instead of trusting that two unrelated-looking
 * numbers happen to add up.
 *
 * Exists ONLY so `client.test.ts` has something fixed, independent of
 * [FPP_COMMAND_REQUEST_TIMEOUT_MS] itself, to assert that constant is
 * still at least as large as — see that constant's own doc comment for
 * why a self-referential check would not have caught Finding 1 (Step 8
 * client-side review): the constant under test was set to 6_000 and the
 * whole suite still passed, because nothing compared it to anything.
 */
const SERVER_FPP_COMMAND_CONFIRM_DEADLINE_MS = 20_000 // pkg/command.DefaultFPPCommandConfirmDeadline
const CLIENT_TIMEOUT_MARGIN_MS = 15_000 // pkg/command.ClientTimeoutMargin
export const MIN_FPP_COMMAND_CLIENT_TIMEOUT_MS =
  SERVER_FPP_COMMAND_CONFIRM_DEADLINE_MS + CLIENT_TIMEOUT_MARGIN_MS

/**
 * Thin transport layer: builds the request (version header, bearer
 * token if one is stored), and turns a non-2xx response into a typed
 * error by dispatching on the RFC 9457 problem document's `type` field
 * — never on `title` or `detail` (spec section 5.4). Every method here
 * is used by both plain JSON endpoints and the initial handshake of the
 * `/stream` connection, since both share this same error-classification
 * contract (api/openapi.yaml: every route documents the same problem
 * response set).
 */
export class ApiClient {
  private readonly baseUrl: string
  private readonly fetchImpl: FetchLike
  private readonly requestTimeoutMs: number
  private readonly clock: Clock

  constructor(
    baseUrl: string,
    fetchImpl: FetchLike = fetch,
    requestTimeoutMs: number = DEFAULT_REQUEST_TIMEOUT_MS,
    clock: Clock = SYSTEM_CLOCK,
  ) {
    this.baseUrl = baseUrl
    this.clock = clock
    // Bound to the global, and this is load-bearing rather than defensive.
    // `this.fetchImpl(...)` invokes with `this` set to the ApiClient
    // instance. A real browser's `fetch` is a WebIDL operation on Window
    // and rejects any other receiver with
    // "Failed to execute 'fetch' on 'Window': Illegal invocation", so
    // without this bind the client cannot make a single request in a
    // browser. Node's fetch does not check its receiver, so the entire
    // unit suite, including the tests that drive a real node:http server,
    // passes either way. This was found only by loading the built image
    // in a browser, and it is the reason the acceptance criteria are
    // verified against a running stack rather than against the suite.
    this.fetchImpl = fetchImpl.bind(globalThis)
    this.requestTimeoutMs = requestTimeoutMs
  }

  /**
   * Performs the request and returns the raw Response on success (2xx).
   * Throws a typed error otherwise.
   *
   * `init.method`/`init.body` exist for `POST /session`, `DELETE
   * /session`, and `POST /bootstrap` (ADR-024) — every other route this
   * client calls is a bare `GET`, so `init` defaults to that and every
   * existing call site (store.ts) is unaffected. `init.body`, when
   * present, is JSON-encoded and paired with `Content-Type:
   * application/json`; a body-less request (every `GET`, and `DELETE
   * /session` with no `sessionId`) sends neither.
   *
   * `credentials: 'same-origin'` is set explicitly on every request
   * rather than relied on as `fetch`'s default. The default already
   * matches (browsers omit credentials only for `'omit'`/cross-origin
   * cases), but ADR-024 decision 5's whole session model depends on the
   * `showmesh_session` cookie actually attaching to same-origin requests,
   * and Step 4's own lesson (client.ts's constructor comment, `this`
   * receiver binding) is that an assumption about `fetch` unverified in a
   * browser is not a fact about `fetch` in a browser. Being explicit also
   * makes this an assertion a test can read off the constructed request
   * rather than a behavior only a browser can confirm.
   *
   * timeoutMs overrides `this.requestTimeoutMs` for this ONE call — Step 7
   * seam C review defect 1: `POST /fpp/{instanceId}/commands` is a long
   * request by design and must not share every other route's short
   * snapshot-read budget (see [FPP_COMMAND_REQUEST_TIMEOUT_MS]). Defaults
   * to the instance-wide value, so every existing call site is unaffected.
   */
  async request(
    path: string,
    signal: AbortSignal,
    init: JsonRequestInit = {},
    timeoutMs: number = this.requestTimeoutMs,
  ): Promise<Response> {
    const token = getStoredToken()
    const tokenWasPresent = token !== null
    const headers: Record<string, string> = {
      [API_VERSION_HEADER]: String(REQUIRED_API_VERSION),
    }
    // ADR-024 decision 6: "an Authorization header, if present at all, is
    // the only credential path considered for this request." A stored
    // break-glass token therefore always wins over the cookie the browser
    // would otherwise attach automatically — this client does not choose
    // between them, it just decides whether to ADD the header, and the
    // coordinator's own resolveCredential (internal/coordinator/api/auth.go)
    // is what enforces the precedence this comment names.
    if (token !== null) {
      headers.Authorization = `Bearer ${token}`
    }
    // `null`, not `undefined`: `fetch`'s own `body` option type is
    // `BodyInit | null` under `exactOptionalPropertyTypes` — see
    // JsonRequestInit's doc comment for why every caller passes a plain
    // object instead of a pre-encoded BodyInit in the first place.
    let body: string | null = null
    if (init.body !== undefined) {
      headers['Content-Type'] = 'application/json'
      body = JSON.stringify(init.body)
    }

    // Combine the caller's signal (deliberate interruption: dispose(),
    // submitToken()/clearToken(), generation supersession — must still
    // surface as an AbortError so runLoop's isAbortError() branch keeps
    // treating it as "retry immediately, no backoff") with our own timer
    // (a genuine timeout — must NOT look like an AbortError, or runLoop
    // would retry it instantly with no backoff instead of as a normal
    // retryable failure). Built manually rather than with
    // `AbortSignal.any` so the two causes stay distinguishable by the
    // reason each one attaches.
    const combined = new AbortController()
    const onOuterAbort = (): void => combined.abort(signal.reason)
    if (signal.aborted) combined.abort(signal.reason)
    else signal.addEventListener('abort', onOuterAbort, { once: true })
    const timeoutErr = new ApiError(`request to ${path} timed out after ${timeoutMs}ms with no response`)
    const timer: TimerHandle = this.clock.setTimeout(() => combined.abort(timeoutErr), timeoutMs)

    let response: Response
    try {
      response = await this.fetchImpl(`${this.baseUrl}${path}`, {
        method: init.method ?? 'GET',
        headers,
        body,
        // See this method's own doc comment: explicit rather than relied
        // on as fetch's default, so ADR-024's cookie actually attaches
        // and a test can assert this was asked for rather than hoped for.
        credentials: 'same-origin',
        signal: combined.signal,
      })
    } catch (err) {
      if (err instanceof Error && err.name === 'AbortError') throw err
      if (err instanceof ApiError) throw err
      throw new ApiError(
        `network error requesting ${path}: ${err instanceof Error ? err.message : String(err)}`,
      )
    } finally {
      // Only the TIMER is scoped to "getting a response" (store.ts's
      // STREAM_IDLE_TIMEOUT_MS is the separate, longer-lived bound on an
      // established `/stream` body going quiet). The outer-abort
      // forwarding listener is deliberately NOT removed here: `combined`
      // is what the returned Response's body reader keeps reading
      // against for `/stream`, so a caller aborting `signal` (dispose(),
      // submitToken()/clearToken(), generation supersession) after
      // headers have already arrived must still be able to cancel an
      // in-progress body read. `{ once: true }` on the listener already
      // guarantees it fires at most once; it is discarded along with
      // `signal` itself (a fresh AbortController per connection attempt
      // — see runLoop in store.ts) once that attempt ends.
      this.clock.clearTimeout(timer)
    }

    if (!response.ok) {
      await this.throwForFailedResponse(response, tokenWasPresent, path)
    }

    this.checkVersionHeader(response)
    return response
  }

  async getJson<T>(path: string, signal: AbortSignal): Promise<T> {
    const response = await this.request(path, signal)
    return (await response.json()) as T
  }

  /**
   * `POST` with a JSON body, expecting a JSON response — `POST /session`,
   * `POST /bootstrap`, `POST /fpp/{instanceId}/commands`. timeoutMs
   * overrides the instance-wide default for this one call — see
   * [ApiClient.request]'s own doc comment (Step 7 seam C review defect 1).
   */
  async postJson<T>(path: string, body: unknown, signal: AbortSignal, timeoutMs?: number): Promise<T> {
    const response = await this.request(path, signal, { method: 'POST', body }, timeoutMs)
    return (await response.json()) as T
  }

  /**
   * `PUT` with a JSON body, expecting a JSON response — Step 7 seam A's
   * `PUT /config/fpp.endpoints`, this application's first write endpoint
   * besides the session/bootstrap pair above. No special Sec-Fetch-Site
   * handling is needed here (ADR-024 decision 6): that header is set by
   * the BROWSER itself on same-origin `fetch` calls, never something a
   * script can set or needs to set — this method sends nothing beyond
   * what `request()` already does for every other call.
   */
  async putJson<T>(path: string, body: unknown, signal: AbortSignal): Promise<T> {
    const response = await this.request(path, signal, { method: 'PUT', body })
    return (await response.json()) as T
  }

  /**
   * `DELETE`, optionally with a JSON body — `DELETE /session`. Returns
   * `undefined` for a `204 No Content` (this route's success response,
   * api/openapi.yaml) rather than calling `response.json()` on an empty
   * body, which throws. Typed `T | undefined` rather than always-`void`
   * so a future `DELETE` route that does return a body is not forced
   * through a second method.
   */
  async deleteJson<T>(path: string, body: unknown, signal: AbortSignal): Promise<T | undefined> {
    const response = await this.request(path, signal, { method: 'DELETE', body })
    if (response.status === 204) return undefined
    return (await response.json()) as T
  }

  private checkVersionHeader(response: Response): void {
    checkApiVersionHeaderValue(response.headers.get(API_VERSION_HEADER))
  }

  private async throwForFailedResponse(
    response: Response,
    tokenWasPresent: boolean,
    path: string,
  ): Promise<never> {
    let problem: Problem | null = null
    try {
      problem = (await response.json()) as Problem
    } catch {
      // Malformed or absent problem body — fall through to the generic
      // ApiError below rather than crashing the classification step.
    }
    throw classifyProblemResponse(
      response.status,
      problem,
      tokenWasPresent,
      path,
      response.headers.get('Retry-After'),
    )
  }
}

/**
 * `Every /api/v1 response carries the ShowMesh-API-Version header with no
 * exception` (api/openapi.yaml contract section 6.2) — extracted as a
 * pure function, taking the header VALUE rather than a `Response`, so a
 * transport that is not `fetch` (resolumeCompositionUpload.ts's
 * `XMLHttpRequest`-based upload, chosen there specifically for real
 * `upload.onprogress` byte counts — see that file's own header comment)
 * can run the identical check against `XMLHttpRequest.getResponseHeader`
 * without this client needing a `Response` object to exist. Its absence
 * on an otherwise-OK response is itself a sign this isn't really talking
 * to a ShowMesh coordinator's v1 API; treated the same as a reported
 * mismatch rather than assuming "1" silently.
 */
export function checkApiVersionHeaderValue(header: string | null): void {
  if (header === null || header !== String(REQUIRED_API_VERSION)) {
    throw new IncompatibleVersionError(
      REQUIRED_API_VERSION,
      [],
      header === null
        ? 'response carried no ShowMesh-API-Version header'
        : `coordinator reported version ${header}`,
    )
  }
}

/**
 * Turns a failed response's status + parsed RFC 9457 problem body into
 * the typed error this client's callers dispatch on — extracted as a
 * pure function (taking `status`/`problem`/a `retryAfterHeader` value
 * rather than a `Response`) for the identical reason
 * [checkApiVersionHeaderValue] above is: resolumeCompositionUpload.ts's
 * `XMLHttpRequest`-based upload has no `Response` object to hand this,
 * only `xhr.status`, `xhr.responseText` (parsed into `problem` by the
 * caller), and `xhr.getResponseHeader('Retry-After')`. Kept as the
 * SINGLE place this dispatch table exists, so the fetch-based
 * `ApiClient.request` and the XHR-based upload can never disagree about
 * which problem `type` produces which error class.
 */
export function classifyProblemResponse(
  status: number,
  problem: Problem | null,
  tokenWasPresent: boolean,
  path: string,
  retryAfterHeader: string | null,
): ApiError {
  if (problem?.type === PROBLEM_TYPE.unauthorized) {
    return new UnauthorizedError(tokenWasPresent, problem.detail)
  }
  if (problem?.type === PROBLEM_TYPE.unsupportedApiVersion) {
    return new IncompatibleVersionError(REQUIRED_API_VERSION, problem.supportedVersions ?? [], problem.detail)
  }
  // ADR-024's three new dispatchable problem types — never inferred from
  // the `403`/`429` HTTP status alone (both statuses cover more than one
  // `type`: `403` is also plain `forbidden`, and this client must not
  // guess which). See errors.ts for why forbidden and csrfRejected stay
  // separate classes despite sharing a status code.
  if (problem?.type === PROBLEM_TYPE.forbidden) {
    return new ForbiddenError(problem.detail)
  }
  if (problem?.type === PROBLEM_TYPE.csrfRejected) {
    return new CSRFRejectedError(problem.detail)
  }
  if (problem?.type === PROBLEM_TYPE.tooManyRequests) {
    return new TooManyRequestsError(problem.detail, parseRetryAfterValue(retryAfterHeader))
  }

  // conflictingRunId is additive on every Problem, so it is read for any
  // failure rather than only the three macro-run 409s that populate it.
  return new ApiError(
    problem?.detail ?? `${path} failed with status ${status}`,
    status,
    problem?.type,
    problem?.conflictingRunId,
  )
}

/**
 * `Retry-After` (ADR-024 decision 8, `components.responses.TooManyRequests`
 * in api/openapi.yaml) as whole seconds, or null when absent or not a
 * plain integer — this API never sends the HTTP-date form of the header,
 * so that form is deliberately not parsed here rather than half-supported.
 * Takes the header VALUE (not a `Response`) for the same reason
 * [classifyProblemResponse] does.
 */
function parseRetryAfterValue(header: string | null): number | null {
  if (header === null) return null
  const seconds = Number(header)
  return Number.isInteger(seconds) && seconds >= 0 ? seconds : null
}
