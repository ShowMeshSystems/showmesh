import { describe, expect, it } from 'vitest'
import { describeApiError, describeSignInState, evaluateScope } from './session'
import {
  ApiError,
  CSRFRejectedError,
  ForbiddenError,
  TooManyRequestsError,
  UnauthorizedError,
} from '../api'
import type { SessionResponse } from './types'

const NOW = '2026-08-12T00:00:00.000Z'

function signedOut(overrides: Partial<SessionResponse> = {}): SessionResponse {
  return {
    serverTime: NOW,
    authenticated: false,
    principal: null,
    session: null,
    credentialForm: null,
    scopes: [],
    scopesState: 'not_applicable',
    bootstrapRequired: false,
    ...overrides,
  }
}

function signedIn(overrides: Partial<SessionResponse> = {}): SessionResponse {
  return signedOut({
    authenticated: true,
    principal: { id: 'p1', name: 'alice', kind: 'human', role: 'operator' },
    session: { id: 's1', deviceLabel: 'porch tablet', createdAt: NOW },
    credentialForm: 'session',
    scopes: ['node:read', 'show:macro:run'],
    scopesState: 'current',
    ...overrides,
  })
}

describe('describeSignInState', () => {
  it('is loading before any /session response has arrived', () => {
    expect(describeSignInState(null)).toEqual({ kind: 'loading' })
  })

  it('is bootstrap_required whenever bootstrapRequired is true, even if this device happens to be authenticated', () => {
    // Case named explicitly: SessionResponse's own doc comment says
    // bootstrapRequired is "computed and returned regardless of whether
    // this particular request authenticated" — a break-glass token could
    // in principle authenticate a request against a coordinator that
    // otherwise has zero PASSWORD principals. If this branch were ever
    // reordered to check `authenticated` first, this is the test that
    // would start failing.
    expect(describeSignInState(signedIn({ bootstrapRequired: true }))).toEqual({ kind: 'bootstrap_required' })
  })

  it('is bootstrap_required (never signed_out) for the ordinary fresh-install case: unauthenticated AND bootstrapRequired', () => {
    expect(describeSignInState(signedOut({ bootstrapRequired: true }))).toEqual({ kind: 'bootstrap_required' })
  })

  it('is signed_out for every one of ADR-024 decision 5\'s three cases, because all three produce the identical wire signal', () => {
    // Case 1: an invalidated (revoked) session.
    expect(describeSignInState(signedOut())).toEqual({ kind: 'signed_out' })
    // Case 3: a device that has never authenticated at all — the SAME
    // shape as case 1 on the wire; this function must not need to know
    // which one it is, because SessionResponse carries no field that
    // would tell it (see this file's own module comment).
    expect(describeSignInState(signedOut())).toEqual({ kind: 'signed_out' })
  })

  it('is signed_in with the session attached when authenticated and not requiring bootstrap', () => {
    const session = signedIn()
    expect(describeSignInState(session)).toEqual({ kind: 'signed_in', session })
  })
})

describe('evaluateScope', () => {
  const SCOPE = 'show:macro:run'

  it('is not allowed while the session has never been fetched', () => {
    const result = evaluateScope(null, false, SCOPE)
    expect(result.allowed).toBe(false)
  })

  it('is not allowed when the last session fetch failed, even if a scope list is cached from before', () => {
    // ADR-024 decision 12: "a stale or unavailable [scope list] renders
    // as unknown, never as permissive." This is the test that would pass
    // even with the bug present if it only checked `session === null` —
    // it deliberately supplies a session that WOULD grant the scope, so
    // only the `sessionFetchFailed` check can be what makes it fail.
    const result = evaluateScope(signedIn({ scopes: [SCOPE] }), true, SCOPE)
    expect(result.allowed).toBe(false)
  })

  it('is not allowed while signed out', () => {
    const result = evaluateScope(signedOut(), false, SCOPE)
    expect(result.allowed).toBe(false)
    if (!result.allowed) expect(result.reason).toMatch(/sign in/i)
  })

  it('is not allowed when scopesState is not "current", even if authenticated and the scope is listed', () => {
    // Same trap as the sessionFetchFailed case above: supply a session
    // that WOULD pass if this check were skipped.
    const result = evaluateScope(signedIn({ scopesState: 'unknown', scopes: [SCOPE] }), false, SCOPE)
    expect(result.allowed).toBe(false)
  })

  it('is not allowed when authenticated, current, but the scope is missing — and names the scope', () => {
    const result = evaluateScope(signedIn({ scopes: ['node:read'] }), false, SCOPE)
    expect(result.allowed).toBe(false)
    if (!result.allowed) {
      expect(result.reason).toContain(SCOPE)
      expect(result.reason).toContain('operator') // the role, so the operator can tell WHOSE grant is short
    }
  })

  it('is allowed only when authenticated, current, AND the scope is present', () => {
    const result = evaluateScope(signedIn({ scopes: [SCOPE] }), false, SCOPE)
    expect(result.allowed).toBe(true)
  })
})

describe('describeApiError', () => {
  it('explains a CSRFRejectedError as a browser-capability fact, not a bug or a permissions failure', () => {
    const text = describeApiError(new CSRFRejectedError('missing Sec-Fetch-Site'))
    expect(text).toMatch(/16\.4|Safari/i)
    expect(text).toMatch(/token/i) // must point at the break-glass workaround
  })

  it('explains a TooManyRequestsError as transient, never as a lockout', () => {
    const text = describeApiError(new TooManyRequestsError('too many attempts', 12))
    expect(text).toContain('12s')
    // ADR-024 decision 8: "never a lockout." The message must actively say
    // this is not one (assertion below), and must never claim the
    // principal itself is locked out (as opposed to this source being
    // rate-limited) — "locked out"/"you are locked" would be that wrong claim.
    expect(text.toLowerCase()).not.toContain('locked out')
    expect(text.toLowerCase()).toContain('not a lockout')
  })

  it('falls back to a generic wait when Retry-After was not parseable', () => {
    const text = describeApiError(new TooManyRequestsError('too many attempts', null))
    expect(text).not.toContain('null')
  })

  it('passes through a ForbiddenError\'s own message (already names the missing scope)', () => {
    const text = describeApiError(new ForbiddenError('this principal does not hold the required scope: audit:read'))
    expect(text).toContain('audit:read')
  })

  it('passes through an UnauthorizedError\'s own message (already an operator-safe detail from the coordinator)', () => {
    const text = describeApiError(new UnauthorizedError(true, 'invalid name or password'))
    expect(text).toBe('invalid name or password')
  })

  it('falls back to the message for a generic ApiError', () => {
    expect(describeApiError(new ApiError('the coordinator is unreachable'))).toBe('the coordinator is unreachable')
  })

  it('falls back to a generic sentence for a non-Error throw', () => {
    expect(describeApiError('a plain string')).toBe('Something went wrong. Try again.')
  })
})
