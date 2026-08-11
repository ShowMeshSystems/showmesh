/**
 * The shared-secret token, per ADR-022 decision 4: held in
 * `sessionStorage` only — never `localStorage` (it must not outlive the
 * tab), never a cookie (no automatic attachment, no CSRF surface), never
 * a URL, and never logged. There is deliberately no expiry or refresh:
 * "discard it" is the entire logout model ADR-022 chose.
 *
 * A module-level singleton rather than store-instance state on purpose:
 * `sessionStorage` already is browser/tab-global, and the real
 * application only ever runs one store (useModel.ts). Tests that need
 * isolation clear it between cases.
 */

const STORAGE_KEY = 'showmesh.apiToken'

export function getStoredToken(): string | null {
  try {
    return sessionStorage.getItem(STORAGE_KEY)
  } catch {
    // Storage can throw (private browsing quirks, disabled storage).
    // Treat that as "no token" rather than crashing the client — the
    // practical effect is just that the operator gets prompted again.
    return null
  }
}

export function setStoredToken(token: string): void {
  try {
    sessionStorage.setItem(STORAGE_KEY, token)
  } catch {
    // See getStoredToken: degrade to "the token won't persist," not a crash.
  }
}

export function clearStoredToken(): void {
  try {
    sessionStorage.removeItem(STORAGE_KEY)
  } catch {
    // See getStoredToken.
  }
}
