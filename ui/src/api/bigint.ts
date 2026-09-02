/**
 * Exact JSON integers, for wire fields like `audio_session.desired_revision`
 * (api/openapi.yaml: `int64`) that legitimately exceed
 * Number.MAX_SAFE_INTEGER. `response.json()` and `JSON.stringify` both
 * round such a value the moment they touch it, which is what let a
 * cue-activation session's UnixNano-scale revision get refused as
 * `stale_revision` even though the operator's command was newer.
 *
 * This repo's installed TypeScript (5.7) ships no types for the
 * source-access `JSON.parse` reviver or `JSON.rawJSON` (both landed in
 * V8/Node runtime terms, neither in `lib.es*.d.ts` here), so this is a
 * bounded, explicitly-scoped text transform instead: it only ever touches
 * a JSON number token that sits outside a string literal, and only when
 * that token is a plain integer (no `.`, no exponent) whose magnitude
 * exceeds Number.MAX_SAFE_INTEGER. Every other byte passes through
 * `JSON.parse`/`JSON.stringify` exactly as before.
 */

const MAX_SAFE_INTEGER_DIGITS = '9007199254740991'

function exceedsSafeInteger(digits: string): boolean {
  const magnitude = digits.startsWith('-') ? digits.slice(1) : digits
  if (magnitude.length < MAX_SAFE_INTEGER_DIGITS.length) return false
  if (magnitude.length > MAX_SAFE_INTEGER_DIGITS.length) return true
  return magnitude > MAX_SAFE_INTEGER_DIGITS
}

// JSON number grammar (RFC 8259 section 6), captured whole so a float or
// an exponent is recognized and left untouched rather than partially matched.
const JSON_NUMBER_TOKEN = /^-?(0|[1-9]\d*)(\.\d+)?([eE][+-]?\d+)?/

/**
 * Rewrites every oversized bare integer in `text` (a plain integer token
 * outside a string, magnitude > Number.MAX_SAFE_INTEGER) into a quoted
 * decimal string, then parses the result with the platform's own
 * `JSON.parse`. A small integer, a float, or digits inside a string
 * value are returned exactly as `JSON.parse` would already return them;
 * only an oversized bare integer changes shape, from a rounded `number`
 * to an exact decimal `string`.
 */
export function parseJsonPreservingBigInts(text: string): unknown {
  return JSON.parse(quoteOversizedIntegers(text))
}

function quoteOversizedIntegers(text: string): string {
  let out = ''
  let inString = false
  let escaped = false
  let i = 0
  while (i < text.length) {
    const ch = text[i] as string
    if (inString) {
      out += ch
      if (escaped) escaped = false
      else if (ch === '\\') escaped = true
      else if (ch === '"') inString = false
      i += 1
      continue
    }
    if (ch === '"') {
      inString = true
      out += ch
      i += 1
      continue
    }
    if (ch === '-' || (ch >= '0' && ch <= '9')) {
      const match = JSON_NUMBER_TOKEN.exec(text.slice(i))
      if (match === null) {
        out += ch
        i += 1
        continue
      }
      const token = match[0]
      const isPlainInteger = match[2] === undefined && match[3] === undefined
      out += isPlainInteger && exceedsSafeInteger(token) ? JSON.stringify(token) : token
      i += token.length
      continue
    }
    out += ch
    i += 1
  }
  return out
}

/**
 * `JSON.stringify`, except a `bigint` anywhere in `value` (nested) is
 * emitted as its own bare decimal digits — a JSON number literal, never a
 * quoted string, matching `int64` wire fields like an audio session
 * command's `revision`. Every non-bigint value defers to the platform's
 * own `JSON.stringify`, so this produces byte-identical output for any
 * body that carries no bigint.
 */
export function stringifyJsonPreservingBigInts(value: unknown): string {
  return stringifyValue(value) ?? 'null'
}

function stringifyValue(value: unknown): string | undefined {
  if (value === undefined) return undefined
  if (value === null) return 'null'
  if (typeof value === 'bigint') return value.toString()
  if (typeof value === 'number' || typeof value === 'boolean' || typeof value === 'string') {
    return JSON.stringify(value)
  }
  if (typeof (value as { toJSON?: unknown }).toJSON === 'function') {
    return stringifyValue((value as { toJSON: () => unknown }).toJSON())
  }
  if (Array.isArray(value)) {
    return `[${value.map((item) => stringifyValue(item) ?? 'null').join(',')}]`
  }
  if (typeof value === 'object') {
    const parts: string[] = []
    for (const [key, item] of Object.entries(value as Record<string, unknown>)) {
      const encoded = stringifyValue(item)
      if (encoded !== undefined) parts.push(`${JSON.stringify(key)}:${encoded}`)
    }
    return `{${parts.join(',')}}`
  }
  return undefined
}
