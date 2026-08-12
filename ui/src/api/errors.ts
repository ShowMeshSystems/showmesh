/**
 * Typed errors the store's request/connection machinery throws, so
 * runLoop (store.ts) can classify a failure by type rather than by
 * inspecting a message string.
 */

/** Any request that failed for a reason not covered by a more specific class below. */
export class ApiError extends Error {
  readonly status: number | undefined
  readonly problemType: string | undefined

  constructor(message: string, status?: number, problemType?: string) {
    super(message)
    this.name = 'ApiError'
    this.status = status
    this.problemType = problemType
  }
}

/**
 * A `401` — either the RFC 9457 `unauthorized` problem, or (defensively)
 * any other malformed `401` response. `tokenWasPresent` records whether
 * this request already carried an `Authorization` header, which is what
 * lets the store distinguish "no token supplied yet" from "the supplied
 * token was rejected" (spec section 5.6) rather than presenting a wrong
 * secret as a missing one.
 */
export class UnauthorizedError extends ApiError {
  readonly tokenWasPresent: boolean

  constructor(tokenWasPresent: boolean, detail: string) {
    super(detail, 401, 'https://showmesh.dev/problems/unauthorized')
    this.name = 'UnauthorizedError'
    this.tokenWasPresent = tokenWasPresent
  }
}

/**
 * A `403` carrying the `forbidden` problem type (ADR-024 decision 4): a
 * valid credential authenticated, but the principal does not hold the
 * scope this operation requires. `detail` names the missing scope, per
 * decision 4's "its RFC 9457 problem document names the missing scope" —
 * this class exists so a caller can render that sentence directly rather
 * than a generic failure, and so this case is never confused with a `401`
 * (ApiClient never presented a credential at all, or it was rejected).
 */
export class ForbiddenError extends ApiError {
  constructor(detail: string) {
    super(detail, 403, 'https://showmesh.dev/problems/forbidden')
    this.name = 'ForbiddenError'
  }
}

/**
 * A `403` carrying the `csrf-rejected` problem type (ADR-024 decision 6):
 * a cookie-authenticated write with no `Sec-Fetch-Site: same-origin`
 * request header. The coordinator sets this header requirement itself —
 * this client sends nothing to satisfy or violate it — so seeing this
 * error means the BROWSER never attached the header, which decision 6
 * names as Safari older than 16.4. Kept as its own class (not folded into
 * ForbiddenError) because the two need different user-facing text: a
 * missing scope is "you may not do this," this is "your browser can't
 * prove this request came from this page — use the token field instead."
 */
export class CSRFRejectedError extends ApiError {
  constructor(detail: string) {
    super(detail, 403, 'https://showmesh.dev/problems/csrf-rejected')
    this.name = 'CSRFRejectedError'
  }
}

/**
 * A `429` from `POST /api/v1/session` or `POST /api/v1/bootstrap`
 * (ADR-024 decision 8's login concurrency bound — never a per-principal
 * lockout, so this is always transient and always says "try again," never
 * "you are locked out"). `retryAfterSeconds` is null when the response
 * carried no parseable `Retry-After` header, which a caller should treat
 * as "wait a little and retry" rather than assuming any particular delay.
 */
export class TooManyRequestsError extends ApiError {
  readonly retryAfterSeconds: number | null

  constructor(detail: string, retryAfterSeconds: number | null) {
    super(detail, 429, 'https://showmesh.dev/problems/too-many-requests')
    this.name = 'TooManyRequestsError'
    this.retryAfterSeconds = retryAfterSeconds
  }
}

/**
 * Either the `unsupported-api-version` problem, or a response whose
 * `ShowMesh-API-Version` header did not match the version this client
 * requires. Terminal per spec section 5.4: the store must stop
 * reconnecting rather than loop against a coordinator that will never
 * serve this version.
 */
export class IncompatibleVersionError extends ApiError {
  readonly requiredVersion: number
  readonly supportedVersions: number[]

  constructor(requiredVersion: number, supportedVersions: number[], detail: string) {
    super(detail, 400, 'https://showmesh.dev/problems/unsupported-api-version')
    this.name = 'IncompatibleVersionError'
    this.requiredVersion = requiredVersion
    this.supportedVersions = supportedVersions
  }
}

/** Thrown by the abortable wait helpers in async-utils.ts when their signal fires. */
export class AbortError extends Error {
  constructor() {
    super('aborted')
    this.name = 'AbortError'
  }
}

export function isAbortError(err: unknown): err is AbortError {
  if (err instanceof AbortError) return true
  // A `fetch`/reader rejection from an aborted AbortSignal surfaces as a
  // native DOMException named "AbortError" — but NOT necessarily one
  // that satisfies `instanceof Error` in the caller's realm. Aborting a
  // `fetch` while its response body is actively being read (client.ts's
  // `combined` controller, cancelled by dispose()/submitToken() while
  // `reader.read()` is pending) was found, empirically, in this
  // project's own jsdom test environment, to reject with a DOMException
  // constructed by the fetch implementation's own internal realm —
  // `.name === 'AbortError'` and a correct `.toString()`, but
  // `instanceof Error` false. Duck-typing on `.name` alone (rather than
  // requiring `instanceof Error` first) is what makes this
  // classification survive that. store.test.ts's "dispose() actually
  // stops the loop" test is what caught this: without this fallback, a
  // disposed store could fall through to the terminal `failed` state
  // instead of cleanly stopping.
  return (
    typeof err === 'object' &&
    err !== null &&
    'name' in err &&
    (err as { name: unknown }).name === 'AbortError'
  )
}
