/**
 * ADR-024 view-layer helpers: turning `Model.session`/`sessionFetchFailed`
 * into what the persistent sign-in banner and a scope-gated control
 * actually render, and turning a thrown request error into operator-facing
 * text. Pure functions only (no React here), matching this project's
 * existing split between app/*.ts (logic) and components/*.tsx (markup) —
 * see evidenceState.ts and time.ts for the same pattern applied to
 * observation freshness.
 */
import {
  ApiError,
  CSRFRejectedError,
  ForbiddenError,
  TooManyRequestsError,
  UnauthorizedError,
} from '../api'
import type { SessionResponse } from './types'

// ---------------------------------------------------------------------
// The persistent sign-in banner (ADR-024 decision 5 / OPERATOR-UI
// section 14): "being signed out is a persistent state, never a modal at
// the moment of use," covering three cases — an invalidated session, an
// expiring one, and a device that has never authenticated at all.
//
// All three collapse to the SAME signal from the wire: `session !==
// null && session.authenticated === false`. There is deliberately no
// separate "reason" here distinguishing the three: SessionResponse
// carries no field that WOULD distinguish them (no expiresAt/lastUsedAt —
// see api/openapi.yaml's SessionResponse schema), and a client guessing
// at a reason it cannot know would be inventing evidence, exactly what
// this project's evidence-provenance rules (ADR-011) forbid. What matters
// is that this function never special-cases any of the three away: it is
// driven purely by the latest fetch, so a brand-new device (case 3, no
// prior `session` at all) reads exactly the same as a revoked one (case
// 1) the instant its own GET /session lands — neither one waits for the
// operator to press a button first.
// ---------------------------------------------------------------------

export type SignInState =
  | { kind: 'loading' } // no /session response has arrived yet
  | { kind: 'bootstrap_required' } // zero principals exist (ADR-024 decision 9)
  | { kind: 'signed_out' }
  | { kind: 'signed_in'; session: SessionResponse }

/**
 * `bootstrapRequired` takes priority over `signed_out`: on a fresh
 * install there is nobody to sign in AS, so "sign in" is the wrong call
 * to action even though `authenticated` is also false in that state —
 * see SessionResponse's own doc comment ("bootstrapRequired is true
 * whenever this coordinator currently holds zero principals ... computed
 * and returned regardless of whether this particular request
 * authenticated").
 */
export function describeSignInState(session: SessionResponse | null): SignInState {
  if (session === null) return { kind: 'loading' }
  if (session.bootstrapRequired) return { kind: 'bootstrap_required' }
  if (!session.authenticated) return { kind: 'signed_out' }
  return { kind: 'signed_in', session }
}

// ---------------------------------------------------------------------
// Scope-driven controls (ADR-024 decision 12 / OPERATOR-UI section 14):
// "a control the principal may not use is rendered disabled with a
// stated reason rather than hidden." Step 6 shipped this with no caller,
// since it added no write endpoint of its own (BUILD-PLAN Step 6). Step 7
// seam A is the first real caller: views/Configuration.tsx gates its
// "Save configuration" button through ScopedButton (which calls this),
// and gates the whole page's data fetch on the identical result — see
// that view's own comment for why a page whose every request requires one
// scope treats "not currently vouched for" as a reason not to even try,
// not merely a reason to disable one button.
// ---------------------------------------------------------------------

export type ScopeGateResult =
  | { allowed: true }
  | { allowed: false; reason: string }

/**
 * `sessionFetchFailed`: ADR-024 decision 12, "a stale or unavailable
 * [scope list] renders as unknown, never as permissive" — a failed
 * refresh forces `allowed: false` regardless of what the last
 * successful fetch said, exactly like `session === null` or
 * `scopesState !== 'current'` below. All four ways this coordinator (or
 * this browser's ability to reach it) can fail to currently vouch for a
 * scope list — no fetch yet, the fetch errored, the server itself
 * reports it computed the list unreliably (`scopesState: 'unknown'`), or
 * the session is not authenticated at all (`scopesState:
 * 'not_applicable'`) — are treated identically: not permissive. A missing
 * scope on an otherwise-healthy, authenticated, `current` scope list is
 * the one case with an operator-actionable message naming the scope and
 * the principal's role, per OPERATOR-UI section 11 ("an operator
 * debugging at nine at night needs to know the difference between
 * 'ShowMesh cannot do this' and 'you may not do this'").
 */
export function evaluateScope(
  session: SessionResponse | null,
  sessionFetchFailed: boolean,
  requiredScope: string,
): ScopeGateResult {
  if (session === null) {
    return { allowed: false, reason: 'Waiting to hear from the coordinator what this device may do.' }
  }
  if (sessionFetchFailed) {
    return {
      allowed: false,
      reason: 'This device’s permissions could not be confirmed just now; treating this control as not permitted until they can be.',
    }
  }
  if (!session.authenticated) {
    return { allowed: false, reason: 'Sign in to use this control.' }
  }
  if (session.scopesState !== 'current') {
    return {
      allowed: false,
      reason: 'This device’s permissions are unknown right now; treating this control as not permitted until they can be confirmed.',
    }
  }
  if (!session.scopes.includes(requiredScope)) {
    const role = session.principal?.role ?? 'this principal’s role'
    return {
      allowed: false,
      reason: `${role} does not include "${requiredScope}". Ask an administrator to grant it, or sign in as a principal that holds it.`,
    }
  }
  return { allowed: true }
}

/**
 * Step 9 (STEP-9-SPEC.md section 5.5): "reading show.macro and
 * show.action requires show:macro:run OR config:write" — this project's
 * FIRST read surface gated by more than one scope (every prior gate,
 * including [evaluateScope] above, checks exactly one). A DIFFERENT
 * function rather than a loop over [evaluateScope] because every
 * "not currently vouched for" branch (no session yet, a failed refresh,
 * signed out, a stale scope list) means the identical thing regardless
 * of WHICH scope is being asked about, so calling evaluateScope twice
 * would only ever differ in its final branch — and even there, "missing
 * scope A" and "missing scope B" are each individually true but neither
 * alone is the honest message once EITHER would have worked; this
 * states both, matching evaluateScope's "name what's missing" contract
 * for the OR case specifically.
 */
export function evaluateAnyScope(
  session: SessionResponse | null,
  sessionFetchFailed: boolean,
  requiredScopes: readonly string[],
): ScopeGateResult {
  if (session === null) {
    return { allowed: false, reason: 'Waiting to hear from the coordinator what this device may do.' }
  }
  if (sessionFetchFailed) {
    return {
      allowed: false,
      reason: 'This device’s permissions could not be confirmed just now; treating this control as not permitted until they can be.',
    }
  }
  if (!session.authenticated) {
    return { allowed: false, reason: 'Sign in to use this control.' }
  }
  if (session.scopesState !== 'current') {
    return {
      allowed: false,
      reason: 'This device’s permissions are unknown right now; treating this control as not permitted until they can be confirmed.',
    }
  }
  if (requiredScopes.some((scope) => session.scopes.includes(scope))) {
    return { allowed: true }
  }
  const role = session.principal?.role ?? 'this principal’s role'
  const scopeList = requiredScopes.map((s) => `"${s}"`).join(' or ')
  return {
    allowed: false,
    reason: `${role} does not include ${scopeList}. Ask an administrator to grant one, or sign in as a principal that holds one.`,
  }
}

// ---------------------------------------------------------------------
// Turning a thrown request error into what a sign-in/sign-out/bootstrap
// form shows the operator. Dispatches on error TYPE, never on `.message`
// text, matching this project's "never dispatch on title/detail" posture
// for RFC 9457 problems (problem.ts) — the underlying `.message` IS
// `Problem.detail` (client.ts), which is operator-safe to show as
// supporting detail, but the CHOICE of which sentence wraps it must not
// depend on parsing that text.
// ---------------------------------------------------------------------

export function describeApiError(err: unknown): string {
  if (err instanceof CSRFRejectedError) {
    // ADR-024 decision 6, corrected 2026-08-14. The previous text named
    // "Safari older than 16.4" as the cause, and the Step 9 wave 3
    // acceptance run proved that wrong in the deployment this project
    // actually has: the coordinator now accepts an Origin header when the
    // browser sends no Sec-Fetch-Site, so reaching this branch no longer
    // means an old browser. It means neither signal arrived, which in
    // practice is a proxy rewriting the Host header so the origin the
    // browser used and the host the coordinator was addressed as no
    // longer agree. Naming a browser version here sent an operator to
    // update Chrome 151 over a reverse-proxy misconfiguration.
    return (
      'This page and the coordinator disagree about which host you are on, ' +
      'so the sign-in was refused as a cross-site request. This is usually a ' +
      'proxy in front of ShowMesh rewriting the Host header. Check that, or ' +
      'use the "Use a token instead" option below.'
    )
  }
  if (err instanceof TooManyRequestsError) {
    // ADR-024 decision 8: never a lockout, always transient — the
    // message must say so explicitly rather than reading like a ban.
    const wait = err.retryAfterSeconds === null ? 'a few seconds' : `${err.retryAfterSeconds}s`
    return `Too many attempts from this network right now. Wait ${wait} and try again — this is a rate limit, not a lockout.`
  }
  if (err instanceof ForbiddenError) {
    return err.message
  }
  if (err instanceof UnauthorizedError) {
    // Covers both POST /session's "invalid name or password" and
    // POST /bootstrap's "invalid/claimed/expired code" — both already
    // carry an operator-safe `detail` (session.go/bootstrap.go), so
    // there is nothing generic to add on top.
    return err.message
  }
  if (err instanceof ApiError) {
    return err.message
  }
  return err instanceof Error ? err.message : 'Something went wrong. Try again.'
}
