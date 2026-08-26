import { readFileSync, readdirSync, statSync } from 'node:fs'
import path from 'node:path'
import { describe, expect, it } from 'vitest'

// `ui/src/api/index.ts` documents itself as the fixed public surface of
// `src/api` (OPERATOR-UI spec sections 5.4/5.5): every other file is
// meant to reach the wire schema only through the types and functions
// that barrel re-exports, never through `api/generated/schema` directly.
// That boundary held by convention alone from this codebase's start
// until a real component (FPPResetObservationSequenceControl.tsx)
// imported `components` from `api/generated/schema` straight past it,
// unnoticed until an explicit audit found it: proof review alone does
// not catch this, so it is enforced here instead.
//
// `ui/src/api/` itself is exempt: `domain.ts`, `store.ts`, `problem.ts`,
// `useModel.ts`, and `api/test-support/fixtures.ts` are the layer whose
// entire job is translating the generated wire schema into the domain
// types the rest of the app sees, so they import `generated/schema`
// legitimately. Test files are exempt for the same reason
// fppCommandCopyGuard.test.ts exempts them: a test may need to construct
// a raw wire fixture directly.
const importFromGeneratedSchema = /from\s+['"][^'"]*api\/generated\/schema['"]/

const EXEMPT_DIR = path.join(__dirname, 'api')

function collectTargetFiles(dir: string, out: string[]): string[] {
  for (const name of readdirSync(dir)) {
    if (name === 'node_modules' || name === '.git') continue
    const full = path.join(dir, name)
    if (path.resolve(full) === path.resolve(EXEMPT_DIR)) continue
    const stat = statSync(full)
    if (stat.isDirectory()) {
      collectTargetFiles(full, out)
      continue
    }
    if (!name.endsWith('.ts') && !name.endsWith('.tsx')) continue
    if (name.endsWith('.test.ts') || name.endsWith('.test.tsx')) continue
    out.push(full)
  }
  return out
}

describe('code outside ui/src/api never imports api/generated/schema directly', () => {
  const files = collectTargetFiles(__dirname, [])
  for (const filePath of files.sort()) {
    const relative = path.relative(__dirname, filePath)
    it(relative, () => {
      const source = readFileSync(filePath, 'utf-8')
      expect(importFromGeneratedSchema.test(source)).toBe(false)
    })
  }
})
