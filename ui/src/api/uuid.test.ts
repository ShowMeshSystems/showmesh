import { afterEach, describe, expect, it, vi } from 'vitest'
import { randomUUIDv4 } from './uuid'

// A valid RFC 4122 v4 UUID: 8-4-4-4-12 hex, version nibble "4", variant
// nibble one of 8/9/a/b.
const V4_UUID = /^[0-9a-f]{8}-[0-9a-f]{4}-4[0-9a-f]{3}-[89ab][0-9a-f]{3}-[0-9a-f]{12}$/i

/**
 * `delete globalThis.crypto.randomUUID` is NOT how this is simulated
 * below, and that is deliberate, not an oversight: Node's `Crypto`
 * defines `randomUUID`/`getRandomValues` on its PROTOTYPE, not as own
 * properties on `globalThis.crypto` itself, so `delete` on the instance
 * silently no-ops (`delete` on a non-existent own property returns
 * `true` and changes nothing) and the real method keeps answering right
 * through the "removed" API — a test written with `delete` here would
 * pass while exercising the ORIGINAL code path, not the fallback, the
 * exact "test can be a coin flip" shape CLAUDE.md warns about generally.
 * Assigning `undefined` instead creates a shadowing OWN property, which
 * is what a real secure-context browser's absence of the method
 * observably looks like to `typeof x === 'function'` either way.
 */
function withoutRandomUUID<T>(fn: () => T): T {
  const original = globalThis.crypto.randomUUID
  // @ts-expect-error deliberately simulating a secure-context-gated
  // absence, which TypeScript's lib.dom.d.ts does not model as optional.
  globalThis.crypto.randomUUID = undefined
  try {
    return fn()
  } finally {
    globalThis.crypto.randomUUID = original
  }
}

describe('randomUUIDv4', () => {
  afterEach(() => {
    vi.restoreAllMocks()
  })

  it('uses crypto.randomUUID() when the runtime exposes it, and returns a valid v4 UUID', () => {
    const spy = vi.spyOn(globalThis.crypto, 'randomUUID')
    const id = randomUUIDv4()
    expect(id).toMatch(V4_UUID)
    expect(spy).toHaveBeenCalledOnce()
  })

  // The actual defect this function exists to fix (CLAUDE.md DEFECT 1):
  // `crypto.randomUUID` is `[SecureContext]`-gated and is simply ABSENT
  // (not thrown) on the plain http:// origin this UI deploys to
  // (ADR-022, deploy/README.md's http://<host>:8081). Node/jsdom expose
  // it unconditionally with no concept of secure contexts, so shadowing
  // it away (see withoutRandomUUID's own comment) is the only way to
  // exercise this path from a unit test at all. The `getRandomValues`
  // spy is what makes this test actually PROVE the fallback ran, rather
  // than merely proving *some* v4-shaped string came back — a v4 UUID
  // regex match alone cannot distinguish "used the fallback" from "the
  // shadow didn't take and the real randomUUID answered anyway."
  it('falls back to crypto.getRandomValues() and still produces a valid v4 UUID when randomUUID is unavailable', () => {
    const grvSpy = vi.spyOn(globalThis.crypto, 'getRandomValues')
    const id = withoutRandomUUID(() => randomUUIDv4())
    expect(id).toMatch(V4_UUID)
    expect(grvSpy).toHaveBeenCalledOnce()
  })

  it('mints a distinct key on each call via the fallback path, never repeating one', () => {
    const [a, b] = withoutRandomUUID(() => [randomUUIDv4(), randomUUIDv4()] as const)
    expect(a).not.toBe(b)
  })

  it('throws a clear error rather than minting a weak key when no random source is available at all', () => {
    const originalGetRandomValues = globalThis.crypto.getRandomValues
    // @ts-expect-error deliberately simulating both being absent, same
    // shadowing reasoning as withoutRandomUUID above.
    globalThis.crypto.getRandomValues = undefined
    try {
      expect(() => withoutRandomUUID(() => randomUUIDv4())).toThrow(
        /no source of cryptographically random bytes/,
      )
    } finally {
      globalThis.crypto.getRandomValues = originalGetRandomValues
    }
  })
})
