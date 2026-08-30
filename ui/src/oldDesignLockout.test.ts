import { describe, expect, it } from 'vitest'
import { checkOldDesign } from '../scripts/check-old-design.mjs'

// Phase 2 (REBUILD-PLAN.md): nothing under ui/src may reference a retired
// token name or import a stylesheet outside kit/styles. `npm run build`
// runs the same check via scripts/check-old-design.mjs, so a violation
// fails both gates rather than only this one.
describe('the old design stays deleted', () => {
  it('no file under ui/src references a retired token or imports a stylesheet outside kit/styles', () => {
    expect(checkOldDesign()).toEqual([])
  })
})
