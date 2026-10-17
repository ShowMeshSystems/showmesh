import { describe, expect, it } from 'vitest'
import { diffPayloads } from './diffPayloads'

describe('diffPayloads', () => {
  it('reports a changed value with before and after', () => {
    expect(diffPayloads({ a: 1 }, { a: 2 })).toEqual([{ path: 'a', kind: 'changed', before: '1', after: '2' }])
  })

  it('reports an added and a removed field', () => {
    expect(diffPayloads({ gone: 'x' }, { fresh: true })).toEqual([
      { path: 'fresh', kind: 'added', before: null, after: 'true' },
      { path: 'gone', kind: 'removed', before: 'x', after: null },
    ])
  })

  it('names nested paths and array positions', () => {
    const changes = diffPayloads({ net: { port: 80 }, steps: [{ n: 'a' }, { n: 'b' }] }, { net: { port: 81 }, steps: [{ n: 'a' }, { n: 'c' }] })
    expect(changes.map((c) => c.path)).toEqual(['net.port', 'steps[1].n'])
  })

  it('reports nothing when the payloads match', () => {
    expect(diffPayloads({ a: { b: [1, 2] } }, { a: { b: [1, 2] } })).toEqual([])
  })

  it('cuts long values', () => {
    const [change] = diffPayloads({ a: 'x' }, { a: 'y'.repeat(200) })
    expect(change?.after).toHaveLength(81)
    expect(change?.after?.endsWith('…')).toBe(true)
  })
})
