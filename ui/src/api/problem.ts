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
} as const
