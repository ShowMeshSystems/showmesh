/**
 * `crypto.randomUUID()` is defined `[SecureContext]` in the Web Crypto
 * spec: a browser does not expose it at all (not "throws" — simply
 * `undefined`) on any origin that is not itself a secure context. That
 * covers `https://` and `http://localhost`, and excludes everything
 * else — in particular a bare `http://<lan-ip>` or `http://<hostname>`,
 * which is exactly how `deploy/README.md` documents reaching the
 * Operator UI (ADR-022: ShowMesh terminates no TLS) and exactly how the
 * reference installation's operator reaches it from a phone (which can
 * never satisfy "localhost"). Node and jsdom expose `randomUUID`
 * unconditionally, with no concept of secure contexts at all, so a unit
 * suite cannot see this: only a real browser on the real deployment
 * origin can. This is the Step 4 `fetch`-receiver lesson in a new
 * disguise (CLAUDE.md) — a browser restriction the test environment
 * does not enforce.
 *
 * `crypto.getRandomValues()` carries NO such restriction — it is
 * available in any context Web Crypto exists in at all — so it is the
 * fallback here, building a version-4 UUID by hand (RFC 4122 §4.4: set
 * the version nibble to 4 and the variant bits to 10).
 *
 * If neither is available, this throws rather than falling back further
 * to something weaker (e.g. `Math.random()`): an idempotency key is
 * used by the coordinator to distinguish a retry from a genuinely new
 * write (RES-015 §7.3), so a predictable or colliding key does not fail
 * loudly — it silently turns a real command into a replay that
 * dispatches nothing, which is worse than refusing to mint a key at
 * all.
 */
export function randomUUIDv4(): string {
  const cryptoObj: Crypto | undefined = globalThis.crypto
  if (typeof cryptoObj?.randomUUID === 'function') {
    return cryptoObj.randomUUID()
  }
  if (typeof cryptoObj?.getRandomValues === 'function') {
    const bytes = cryptoObj.getRandomValues(new Uint8Array(16))
    // RFC 4122 §4.4: version 4 (random), variant 1 (10xxxxxx).
    bytes[6] = ((bytes[6] ?? 0) & 0x0f) | 0x40
    bytes[8] = ((bytes[8] ?? 0) & 0x3f) | 0x80
    const hex = Array.from(bytes, (b) => b.toString(16).padStart(2, '0')).join('')
    return `${hex.slice(0, 8)}-${hex.slice(8, 12)}-${hex.slice(12, 16)}-${hex.slice(16, 20)}-${hex.slice(20)}`
  }
  throw new Error(
    'no source of cryptographically random bytes is available in this browser ' +
      '(both crypto.randomUUID and crypto.getRandomValues are missing) — refusing ' +
      'to mint a weak or predictable idempotency key rather than risk a silent replay',
  )
}
