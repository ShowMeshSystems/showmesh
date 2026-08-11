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
