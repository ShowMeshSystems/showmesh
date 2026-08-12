/**
 * useModel.ts has this seam's one piece of module-load-time wiring (the
 * singleton `ApiStore` constructed and connected the instant this module
 * is imported) plus six thin pass-throughs to it. Nothing here duplicates
 * store.test.ts's coverage of ApiStore's own behavior — this file's job
 * is only that useModel.ts wires the singleton correctly: the right
 * method gets called, with the right arguments, and the call's actual
 * result (not a fabricated one) is what the caller sees.
 *
 * Two specific regressions named the reason this file exists at all:
 * `logout` silently replaced by `() => Promise.resolve()` (the only
 * session-revocation affordance in the UI going inert with no test
 * noticing), and `claimBootstrap` silently rewired to call `login`
 * instead of `store.claimBootstrap`. Neither changes this module's
 * exported *shape*, so only a test that checks WHICH store method ran,
 * and that this module's return value truly is that call's own promise
 * (not a stand-in), catches either one.
 *
 * `ApiStore` is mocked (not a real store.ts instance): useModel.ts
 * constructs its singleton once, at import time, and store.test.ts
 * already drives the real class end-to-end against a real HTTP server.
 * Mocking it here isolates useModel.ts's OWN wiring from ApiStore's
 * internals, the same split client.test.ts/store.test.ts already draw
 * between the transport and the store built on top of it.
 */
import { afterEach, describe, expect, it, vi } from 'vitest'
import { act, cleanup, renderHook } from '@testing-library/react'
import { claimBootstrap, clearToken, login, logout, submitToken, useModel } from './useModel'
import type { Model } from './domain'

const { connect, subscribe, getSnapshot, submitTokenMock, clearTokenMock, loginMock, logoutMock, claimBootstrapMock, ApiStoreCtor } =
  vi.hoisted(() => {
    const connect = vi.fn()
    const subscribe = vi.fn().mockReturnValue(() => {})
    const getSnapshot = vi.fn()
    const submitTokenMock = vi.fn()
    const clearTokenMock = vi.fn()
    const loginMock = vi.fn()
    const logoutMock = vi.fn()
    const claimBootstrapMock = vi.fn()
    const instance = {
      connect,
      subscribe,
      getSnapshot,
      submitToken: submitTokenMock,
      clearToken: clearTokenMock,
      login: loginMock,
      logout: logoutMock,
      claimBootstrap: claimBootstrapMock,
    }
    const ApiStoreCtor = vi.fn().mockReturnValue(instance)
    return {
      connect,
      subscribe,
      getSnapshot,
      submitTokenMock,
      clearTokenMock,
      loginMock,
      logoutMock,
      claimBootstrapMock,
      ApiStoreCtor,
    }
  })

// `vi.mock` factories are hoisted by Vitest above every import in this
// file regardless of source position (same as SessionPanel.test.tsx),
// so useModel.ts's own `import { ApiStore } from './store'` resolves to
// this mock the instant the import above evaluates it.
vi.mock('./store', () => ({ ApiStore: ApiStoreCtor }))

// Captured as PLAIN values the instant this file finishes evaluating —
// i.e. before vitest.config.ts's `restoreMocks: true` gets its first
// chance to run (it fires as an auto beforeEach, which is after module
// evaluation but before the first `it` body). Reading `.mock.calls`
// directly inside a test body would see it already wiped back to empty:
// restoreMocks clears call history (confirmed empirically: an
// `.mock.calls.length` recorded at module scope is 1, but the same
// mock's `.mock.calls.length` read from inside the first test is 0)
// without touching a `vi.fn(impl)`'s own implementation, so the
// singleton itself (and every method on it) still works throughout this
// file — only the CALL LOG of "did module load call this" is gone by
// the time any test runs, unless captured here first.
const constructedWith = ApiStoreCtor.mock.calls[0]?.[0] as unknown
const connectCallsAtLoad = connect.mock.calls.length

afterEach(() => {
  cleanup()
})

describe('useModel.ts: module-load wiring', () => {
  it('constructs exactly one ApiStore against the same-origin /api/v1 base URL, and connects it immediately', () => {
    expect(constructedWith).toEqual({ baseUrl: '/api/v1' })
    expect(connectCallsAtLoad).toBe(1)
  })
})

describe('useModel(): subscribes to the singleton and returns its live snapshot', () => {
  it('returns getSnapshot()\'s value and re-renders when the store notifies its listener', () => {
    const modelA = { marker: 'A' } as unknown as Model
    const modelB = { marker: 'B' } as unknown as Model
    getSnapshot.mockReturnValue(modelA)

    let notify: (() => void) | undefined
    subscribe.mockImplementationOnce((listener: () => void) => {
      notify = listener
      return () => {}
    })

    const { result, unmount } = renderHook(() => useModel())
    expect(result.current).toBe(modelA)
    expect(subscribe).toHaveBeenCalled()

    getSnapshot.mockReturnValue(modelB)
    act(() => {
      notify?.()
    })
    expect(result.current).toBe(modelB)
    unmount()
  })
})

describe('useModel.ts: action pass-throughs call the RIGHT store method with the RIGHT arguments', () => {
  it('submitToken(token) calls store.submitToken with exactly that token', () => {
    submitToken('the-token')
    expect(submitTokenMock).toHaveBeenCalledExactlyOnceWith('the-token')
  })

  it('clearToken() calls store.clearToken with no arguments', () => {
    clearToken()
    expect(clearTokenMock).toHaveBeenCalledExactlyOnceWith()
  })

  it('login(name, password, deviceLabel) forwards all three arguments to store.login and returns ITS promise (rejection included), not a fabricated one', async () => {
    loginMock.mockRejectedValueOnce(new Error('invalid name or password'))
    await expect(login('alice', 'secret123', 'porch tablet')).rejects.toThrow('invalid name or password')
    expect(loginMock).toHaveBeenCalledExactlyOnceWith('alice', 'secret123', 'porch tablet')
  })

  it('login() resolves when store.login resolves', async () => {
    loginMock.mockResolvedValueOnce(undefined)
    await expect(login('alice', 'secret123', 'porch tablet')).resolves.toBeUndefined()
  })

  // The regression this guards: logout() silently replaced by
  // `() => Promise.resolve()`, which would make this the one
  // session-revocation affordance in the UI a permanent no-op. A test
  // that only checked `logoutMock` was called (without also checking
  // what this module's OWN return value does) would not catch a stub
  // that calls logout() and then discards the result in favor of its
  // own resolved promise — asserting the REJECTION propagates rules
  // that out, since a hand-written `Promise.resolve()` cannot reject.
  it('logout(sessionId) forwards the id to store.logout and propagates its rejection, rather than being a no-op that always resolves', async () => {
    logoutMock.mockRejectedValueOnce(new Error('revoke failed'))
    await expect(logout('s-1')).rejects.toThrow('revoke failed')
    expect(logoutMock).toHaveBeenCalledExactlyOnceWith('s-1')
  })

  it('logout() with no sessionId calls store.logout with undefined (revokes the calling session)', async () => {
    logoutMock.mockResolvedValueOnce(undefined)
    await logout()
    expect(logoutMock).toHaveBeenCalledExactlyOnceWith(undefined)
  })

  // The regression this guards: claimBootstrap() silently rewired to
  // call store.login instead of store.claimBootstrap. Both accept
  // string/string/string-shaped arguments, so a mutation swapping the
  // call target is otherwise easy to miss — asserting BOTH which mock
  // ran AND that the other one did not is what catches it, since
  // checking only "claimBootstrapMock was called with these arguments"
  // would still pass if login was ALSO (or instead) called with a
  // superset/reordering of them.
  it('claimBootstrap(code, name, password, deviceLabel) forwards all four arguments to store.claimBootstrap, and never calls store.login', async () => {
    claimBootstrapMock.mockResolvedValueOnce(undefined)
    await claimBootstrap('the-code', 'root', 'secret123', 'porch tablet')
    expect(claimBootstrapMock).toHaveBeenCalledExactlyOnceWith('the-code', 'root', 'secret123', 'porch tablet')
    expect(loginMock).not.toHaveBeenCalled()
  })

  it('claimBootstrap() propagates a rejection from store.claimBootstrap, rather than being a no-op that always resolves', async () => {
    claimBootstrapMock.mockRejectedValueOnce(new Error('invalid or claimed bootstrap code'))
    await expect(claimBootstrap('bad-code', 'root', 'secret123', 'porch tablet')).rejects.toThrow(
      'invalid or claimed bootstrap code',
    )
  })
})
