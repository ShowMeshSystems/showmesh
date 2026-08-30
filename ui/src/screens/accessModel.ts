/**
 * Pure helpers for the Access screen. No endpoint reports a role's scope
 * bundle or a principal's last activity, so this file derives only what the
 * coordinator's own objects (PrincipalObject, TokenObject, SessionResponse)
 * actually carry - never a role-to-scope mapping, which lives on the
 * coordinator.
 */
import type { PrincipalObject, SessionResponse, TokenObject } from '../api'
import { ageMs } from '../domain/time'

/** A principal's own token read either succeeded (possibly empty) or failed. */
export type TokenRead = TokenObject[] | null

/** Picked once so the warning threshold has one name instead of a magic number. */
export const UNUSED_CREDENTIAL_WARNING_DAYS = 21

const DAY_MS = 24 * 60 * 60 * 1000

/** The newest `lastUsedAt` across a principal's tokens, or null if none was ever used. */
export function latestTokenUse(tokens: TokenRead): string | null {
  if (tokens === null) return null
  let latest: string | null = null
  for (const token of tokens) {
    if (token.lastUsedAt !== null && (latest === null || token.lastUsedAt > latest)) latest = token.lastUsedAt
  }
  return latest
}

export function credentialCount(tokens: TokenRead): number | null {
  return tokens === null ? null : tokens.length
}

/** True once a principal's newest token use is older than the named threshold. */
export function isLongUnused(lastUsedIso: string | null, nowIso: string | null): boolean {
  const age = ageMs(lastUsedIso, nowIso)
  if (age === null) return false
  return age >= UNUSED_CREDENTIAL_WARNING_DAYS * DAY_MS
}

export function daysUnused(lastUsedIso: string | null, nowIso: string | null): number | null {
  const age = ageMs(lastUsedIso, nowIso)
  return age === null ? null : Math.floor(age / DAY_MS)
}

export type PrincipalStateLabel = { label: string; tone: 'good' | 'warn' | 'bad' }

/** Disabled and unused-warning are the only per-row states this file will assert; both are reported or derived from a reported field. */
export function principalStateLabel(principal: PrincipalObject, unused: boolean): PrincipalStateLabel {
  if (principal.disabled) return { label: 'Disabled', tone: 'bad' }
  if (unused) return { label: 'Consider revoking', tone: 'warn' }
  return { label: 'Active', tone: 'good' }
}

export function isSignedInPrincipal(session: SessionResponse | null, principal: PrincipalObject): boolean {
  return session !== null && session.authenticated && session.principal !== null && session.principal.id === principal.id
}

/**
 * Whether `token` is the credential this request is using right now. Only
 * `credentialForm: 'session'` reports an id (`session.session.id`) at all -
 * a bearer-token request names no token id anywhere in SessionResponse, so
 * this can never match one and correctly leaves that row ordinary.
 */
/**
 * Nothing reports which issued token, if any, is the credential in use.
 * `SessionResponse` reports only the form and its own session id, which is a
 * different id space from `TokenObject.id`, so no row is ever marked.
 */
export function currentCredentialIsUnreported(session: SessionResponse | null): boolean {
  return session !== null && session.credentialForm === 'token'
}
