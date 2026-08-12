/**
 * token.ts's own header comment states the contract: `sessionStorage`
 * only — "never `localStorage` (it must not outlive the tab)". Nothing
 * previously asserted the storage BACKEND specifically: every existing
 * caller (client.ts, store.test.ts's afterEach) only ever exercises
 * `getStoredToken`/`setStoredToken`/`clearStoredToken` through this
 * module's own functions, which would keep behaving identically —
 * reading back whatever was just written — even if every `sessionStorage`
 * reference in token.ts were changed to `localStorage`. ADR-022 decision
 * 4 chose `sessionStorage` deliberately (a shared/borrowed device must
 * not retain the credential after the tab closes), and that reasoning
 * survives unchanged into ADR-024's break-glass path (its own decision 5:
 * "the bearer-paste field survives as break-glass"). These tests read
 * `sessionStorage`/`localStorage` directly, bypassing token.ts entirely,
 * so a change to which `Storage` object this module writes to is the one
 * thing they can actually see.
 */
import { beforeEach, describe, expect, it } from 'vitest'
import { clearStoredToken, getStoredToken, setStoredToken } from './token'

function allValues(storage: Storage): string[] {
  const values: string[] = []
  for (let i = 0; i < storage.length; i++) {
    const key = storage.key(i)
    if (key !== null) values.push(storage.getItem(key) ?? '')
  }
  return values
}

beforeEach(() => {
  sessionStorage.clear()
  localStorage.clear()
})

describe('token.ts: storage backend is sessionStorage, never localStorage', () => {
  it('setStoredToken writes into sessionStorage and leaves localStorage untouched', () => {
    setStoredToken('secret-value-1')
    expect(allValues(sessionStorage)).toContain('secret-value-1')
    expect(localStorage.length).toBe(0)
  })

  it('getStoredToken reads sessionStorage, not localStorage, even when localStorage holds a value under the same key', () => {
    setStoredToken('right-value')
    // Poison localStorage under whatever key this module actually used,
    // so a getStoredToken() that accidentally read the wrong Storage
    // object would return THIS instead of the real value.
    for (let i = 0; i < sessionStorage.length; i++) {
      const key = sessionStorage.key(i)
      if (key !== null) localStorage.setItem(key, 'wrong-value-from-localStorage')
    }
    expect(getStoredToken()).toBe('right-value')
  })

  it('clearStoredToken removes the value from sessionStorage and never touches localStorage', () => {
    setStoredToken('to-be-cleared')
    clearStoredToken()
    expect(allValues(sessionStorage)).not.toContain('to-be-cleared')
    expect(sessionStorage.length).toBe(0)
    expect(localStorage.length).toBe(0)
  })

  it('a value written directly into localStorage under this module\'s own key is invisible to getStoredToken', () => {
    // Belt-and-braces on the same property from the opposite direction:
    // even with sessionStorage genuinely empty, a value sitting in
    // localStorage (however it got there) must not surface as "the
    // stored token".
    setStoredToken('placeholder') // establishes the real key name, then...
    const realKey = sessionStorage.key(0)
    expect(realKey).not.toBeNull()
    sessionStorage.clear()
    if (realKey !== null) localStorage.setItem(realKey, 'should-not-be-read')
    expect(getStoredToken()).toBeNull()
  })
})
