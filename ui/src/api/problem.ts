import type { components } from './generated/schema'

export type Problem = components['schemas']['Problem']

/**
 * The RFC 9457 `type` values api/openapi.yaml's Problem schema declares
 * as its complete enum. A client dispatches on `type`, never on the
 * human-readable `title` or `detail` (spec section 5.4) — those are
 * present for a human reading `curl` output, not for program logic.
 */
export const PROBLEM_TYPE = {
  unsupportedApiVersion: 'https://showmesh.dev/problems/unsupported-api-version',
  resourceNotFound: 'https://showmesh.dev/problems/resource-not-found',
  invalidParameter: 'https://showmesh.dev/problems/invalid-parameter',
  unauthorized: 'https://showmesh.dev/problems/unauthorized',
  methodNotAllowed: 'https://showmesh.dev/problems/method-not-allowed',
  internalError: 'https://showmesh.dev/problems/internal-error',
  // ADR-024's four additions (api/openapi.yaml Problem.type enum, and this
  // document's own top-level description: "Four of the eleven are
  // ADR-024"). forbidden is 403-authenticated-but-missing-scope, distinct
  // from unauthorized's 401-no-credential-at-all; csrfRejected is 403 too,
  // but names a different cause (a cookie-authenticated write missing
  // Sec-Fetch-Site) that a client must explain differently — never dispatch
  // on HTTP status alone for either.
  forbidden: 'https://showmesh.dev/problems/forbidden',
  csrfRejected: 'https://showmesh.dev/problems/csrf-rejected',
  tooManyRequests: 'https://showmesh.dev/problems/too-many-requests',
  credentialInUrl: 'https://showmesh.dev/problems/credential-in-url',
  // Step 7 seam A: PUT /config/fpp.endpoints refused (409) because
  // SHOWMESH_FPP_ENDPOINTS is still set in the coordinator's own process
  // environment (RES-008 D1). No dedicated error class dispatches on this
  // the way ForbiddenError/CSRFRejectedError do — the coordinator's
  // `detail` text is already the full actionable message (names the
  // variable, states the remedy), so the generic ApiError path
  // (describeApiError in app/session.ts) already renders it correctly.
  // This entry exists so PROBLEM_TYPE stays what its own doc comment
  // claims: every class this coordinator currently produces.
  conflict: 'https://showmesh.dev/problems/conflict',
} as const
