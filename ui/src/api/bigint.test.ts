import { describe, expect, it } from 'vitest'
import { parseJsonPreservingBigInts, stringifyJsonPreservingBigInts } from './bigint'

describe('parseJsonPreservingBigInts', () => {
  it('preserves an integer beyond Number.MAX_SAFE_INTEGER as an exact decimal string', () => {
    const parsed = parseJsonPreservingBigInts('{"desired_revision":1788358834726046720}') as {
      desired_revision: unknown
    }
    expect(parsed.desired_revision).toBe('1788358834726046720')
  })

  it('leaves a small integer as a plain JS number, unaffected', () => {
    const parsed = parseJsonPreservingBigInts('{"desired_revision":85}') as { desired_revision: unknown }
    expect(parsed.desired_revision).toBe(85)
    expect(typeof parsed.desired_revision).toBe('number')
  })

  it('leaves the boundary value Number.MAX_SAFE_INTEGER itself as a number', () => {
    const parsed = parseJsonPreservingBigInts('{"v":9007199254740991}') as { v: unknown }
    expect(parsed.v).toBe(9007199254740991)
  })

  it('rewrites the very next integer, one past the boundary, to a decimal string', () => {
    const parsed = parseJsonPreservingBigInts('{"v":9007199254740992}') as { v: unknown }
    expect(parsed.v).toBe('9007199254740992')
  })

  it('never touches an oversized digit run that only appears inside a string value', () => {
    const parsed = parseJsonPreservingBigInts('{"id":"1788358834726046720"}') as { id: unknown }
    expect(parsed.id).toBe('1788358834726046720')
    expect(typeof parsed.id).toBe('string')
  })

  it('leaves a float and an exponent form untouched even past 15 digits', () => {
    const parsed = parseJsonPreservingBigInts('{"a":1.788e21,"b":1234567.5}') as {
      a: unknown
      b: unknown
    }
    expect(parsed.a).toBe(1.788e21)
    expect(parsed.b).toBe(1234567.5)
  })

  it('preserves a negative oversized integer', () => {
    const parsed = parseJsonPreservingBigInts('{"v":-1788358834726046720}') as { v: unknown }
    expect(parsed.v).toBe('-1788358834726046720')
  })

  it('round-trips a whole audio_session observation entry, matching the shape observed live', () => {
    // The revision literal is spliced in as text, never as a JS number
    // literal: 1788358834714000003 itself would already round at parse
    // time of this test file, before parseJsonPreservingBigInts ever saw it.
    const text = JSON.stringify({
      resource: { kind: 'audio_session', id: 'cue-activation:show' },
      signal: 'audio_session.desired_revision',
      value: 0,
      unit: null,
      state: 'current',
    }).replace('"value":0', '"value":1788358834714000003')
    const parsed = parseJsonPreservingBigInts(text) as { value: unknown; signal: unknown }
    expect(parsed.value).toBe('1788358834714000003')
    expect(parsed.signal).toBe('audio_session.desired_revision')
  })
})

describe('stringifyJsonPreservingBigInts', () => {
  it('emits a bigint as a bare JSON number literal, never a quoted string', () => {
    const text = stringifyJsonPreservingBigInts({ revision: 1788358834726046721n, idempotencyKey: 'k-1' })
    expect(text).toBe('{"revision":1788358834726046721,"idempotencyKey":"k-1"}')
  })

  it('is byte-identical to JSON.stringify for a body with no bigint', () => {
    const body = { a: 1, b: 'two', c: [1, 2, 3], d: null, e: true, f: { nested: 'x' } }
    expect(stringifyJsonPreservingBigInts(body)).toBe(JSON.stringify(body))
  })

  it('omits an undefined property, matching JSON.stringify', () => {
    const body = { a: 1, b: undefined }
    expect(stringifyJsonPreservingBigInts(body)).toBe(JSON.stringify(body))
  })

  it('round-trips a bigint revision through parse: the exact digits survive both directions', () => {
    const original = 1788358834726046721n
    const text = stringifyJsonPreservingBigInts({ revision: original })
    const parsed = parseJsonPreservingBigInts(text) as { revision: unknown }
    expect(parsed.revision).toBe(original.toString())
  })
})
