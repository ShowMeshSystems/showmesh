import { describe, expect, it } from 'vitest'
import { describeApiError, describeSignInRefusal, describeSignInState, evaluateAnyScope, evaluateScope } from './session'
import {
  ApiError,
  CSRFRejectedError,
  ForbiddenError,
  TooManyRequestsError,
  UnauthorizedError,
} from '../api'
import type { SessionResponse } from '../api'

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
    // this particular request authenticated", a break-glass token could
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
    // Case 3: a device that has never authenticated at all, the SAME
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
    // even with the bug present if it only checked `session === null`,
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

  it('is not allowed when authenticated, current, but the scope is missing, and names the scope', () => {
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
  it('explains a CSRFRejectedError as an origin/host disagreement, not a bug or a permissions failure', () => {
    const text = describeApiError(new CSRFRejectedError('neither Sec-Fetch-Site nor a matching Origin'))
    // The coordinator now accepts an Origin header when the browser sends
    // no Sec-Fetch-Site (auth.go, 2026-08-14), so reaching this branch no
    // longer means an old browser, and the text must not say it does:
    // naming a browser version here sent an operator to update Chrome 151
    // over a reverse-proxy misconfiguration.
    expect(text).not.toMatch(/Safari|16\.4/i)
    expect(text).toMatch(/host/i) // the cause an operator can actually act on
    expect(text).toMatch(/token/i) // must point at the break-glass workaround
  })

  it('explains a TooManyRequestsError as transient, never as a lockout', () => {
    const text = describeApiError(new TooManyRequestsError('too many attempts', 12))
    expect(text).toContain('12s')
    // ADR-024 decision 8: "never a lockout." The message must actively say
    // this is not one (assertion below), and must never claim the
    // principal itself is locked out (as opposed to this source being
    // rate-limited), "locked out"/"you are locked" would be that wrong claim.
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

describe('describeSignInRefusal', () => {
  it('gives the cross-site refusal a headline and a separate explanation, matching the Session States mock', () => {
    const refusal = describeSignInRefusal(new CSRFRejectedError('neither Sec-Fetch-Site nor a matching Origin'))
    expect(refusal).not.toBeNull()
    expect(refusal?.kind).toBe('crossSite')
    expect(refusal?.headline).toBe(
      'This page and the coordinator disagree about which host you are on, so the sign-in was refused as a cross-site request.',
    )
    expect(refusal?.explanation).toBe(
      'Usually a proxy in front of ShowMesh rewriting the Host header. Check that, or use a token instead.',
    )
  })

  it('gives the rate-limit refusal its wait value in the headline, matching the mock', () => {
    const refusal = describeSignInRefusal(new TooManyRequestsError('too many attempts', 30))
    expect(refusal).not.toBeNull()
    expect(refusal?.kind).toBe('rateLimit')
    expect(refusal?.headline).toBe('Too many attempts from this network right now. Wait 30s and try again.')
    if (refusal?.kind === 'rateLimit') expect(refusal.waitLabel).toBe('30s')
    expect(refusal?.explanation).toBe(
      'This is a rate limit on the network you are on, not a lockout on your account. Nothing is disabled.',
    )
  })

  it('is null for any refusal other than the two the mock special-cases', () => {
    expect(describeSignInRefusal(new UnauthorizedError(true, 'invalid name or password'))).toBeNull()
    expect(describeSignInRefusal(new ApiError('the coordinator is unreachable'))).toBeNull()
  })
})

// Step 9 (STEP-9-SPEC.md section 5.5): the first read surface gated by
// MORE THAN ONE scope, the exact criterion 21 concern: "an operator-role
// principal can list, read and run a macro... this is the criterion that
// catches the UI rendering an empty list for the role the actual operator
// signs in as."
describe('evaluateAnyScope', () => {
  const SCOPES = ['show:macro:run', 'config:write']

  it('is not allowed while the session has never been fetched', () => {
    expect(evaluateAnyScope(null, false, SCOPES).allowed).toBe(false)
  })

  it('is not allowed when the last session fetch failed, even if a scope list is cached from before', () => {
    const result = evaluateAnyScope(signedIn({ scopes: SCOPES }), true, SCOPES)
    expect(result.allowed).toBe(false)
  })

  it('is not allowed while signed out', () => {
    const result = evaluateAnyScope(signedOut(), false, SCOPES)
    expect(result.allowed).toBe(false)
  })

  it('is not allowed when scopesState is not "current", even if the scopes are listed', () => {
    const result = evaluateAnyScope(signedIn({ scopesState: 'unknown', scopes: SCOPES }), false, SCOPES)
    expect(result.allowed).toBe(false)
  })

  // This is the exact shape of the "operator" role: show:macro:run held,
  // config:write NOT held. The list must render for this principal, a
  // regression here is a 403/empty macro list for the role the actual
  // operator signs in as.
  it('is allowed when the principal holds ONLY the first of the two scopes (the operator-role shape)', () => {
    const result = evaluateAnyScope(signedIn({ scopes: ['show:macro:run'] }), false, SCOPES)
    expect(result.allowed).toBe(true)
  })

  it('is allowed when the principal holds ONLY the second of the two scopes (an admin who never runs macros)', () => {
    const result = evaluateAnyScope(signedIn({ scopes: ['config:write'] }), false, SCOPES)
    expect(result.allowed).toBe(true)
  })

  it('is allowed when the principal holds both', () => {
    const result = evaluateAnyScope(signedIn({ scopes: SCOPES }), false, SCOPES)
    expect(result.allowed).toBe(true)
  })

  it('is not allowed and names both scopes when neither is held', () => {
    const result = evaluateAnyScope(signedIn({ scopes: ['node:read'] }), false, SCOPES)
    expect(result.allowed).toBe(false)
    if (!result.allowed) {
      expect(result.reason).toContain('show:macro:run')
      expect(result.reason).toContain('config:write')
    }
  })
})
