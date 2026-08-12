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
   */
  async request(path: string, signal: AbortSignal, init: JsonRequestInit = {}): Promise<Response> {
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
    const timeoutErr = new ApiError(
      `request to ${path} timed out after ${this.requestTimeoutMs}ms with no response`,
    )
    const timer: TimerHandle = this.clock.setTimeout(() => combined.abort(timeoutErr), this.requestTimeoutMs)

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

  /** `POST` with a JSON body, expecting a JSON response — `POST /session`, `POST /bootstrap`. */
  async postJson<T>(path: string, body: unknown, signal: AbortSignal): Promise<T> {
    const response = await this.request(path, signal, { method: 'POST', body })
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
    const header = response.headers.get(API_VERSION_HEADER)
    // Every /api/v1 response carries this header with no exception
    // (api/openapi.yaml contract section 6.2). Its absence on an
    // otherwise-OK response is itself a sign this isn't really talking
    // to a ShowMesh coordinator's v1 API; treat it the same as a
    // reported mismatch rather than assuming "1" silently.
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

    if (problem?.type === PROBLEM_TYPE.unauthorized) {
      throw new UnauthorizedError(tokenWasPresent, problem.detail)
    }
    if (problem?.type === PROBLEM_TYPE.unsupportedApiVersion) {
      throw new IncompatibleVersionError(
        REQUIRED_API_VERSION,
        problem.supportedVersions ?? [],
        problem.detail,
      )
    }
    // ADR-024's three new dispatchable problem types — never inferred from
    // the `403`/`429` HTTP status alone (both statuses cover more than one
    // `type`: `403` is also plain `forbidden`, and this client must not
    // guess which). See errors.ts for why forbidden and csrfRejected stay
    // separate classes despite sharing a status code.
    if (problem?.type === PROBLEM_TYPE.forbidden) {
      throw new ForbiddenError(problem.detail)
    }
    if (problem?.type === PROBLEM_TYPE.csrfRejected) {
      throw new CSRFRejectedError(problem.detail)
    }
    if (problem?.type === PROBLEM_TYPE.tooManyRequests) {
      throw new TooManyRequestsError(problem.detail, parseRetryAfter(response))
    }

    throw new ApiError(
      problem?.detail ?? `${path} failed with status ${response.status}`,
      response.status,
      problem?.type,
    )
  }
}

/**
 * `Retry-After` (ADR-024 decision 8, `components.responses.TooManyRequests`
 * in api/openapi.yaml) as whole seconds, or null when absent or not a
 * plain integer — this API never sends the HTTP-date form of the header,
 * so that form is deliberately not parsed here rather than half-supported.
 */
function parseRetryAfter(response: Response): number | null {
  const header = response.headers.get('Retry-After')
  if (header === null) return null
  const seconds = Number(header)
  return Number.isInteger(seconds) && seconds >= 0 ? seconds : null
}
