import { describe, expect, it } from 'vitest'
import { checkNoNulBytes } from '../scripts/check-no-nul-bytes.mjs'

// SM-481: a NUL byte in a text file makes standard search tools classify it
// as binary and skip it silently, which once produced a false "no callers"
// result in a real survey. `npm test` runs this on every push, the same
// gate ui/scripts/check-old-design.mjs already uses for its own lock-out.
describe('no file under ui/src contains a NUL byte', () => {
  it('reports no violations', () => {
    expect(checkNoNulBytes()).toEqual([])
  })
})
