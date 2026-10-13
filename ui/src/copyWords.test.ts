import { describe, expect, it } from 'vitest'
import { checkCopyWords, findBannedWords } from '../scripts/check-copy-words.mjs'

const words = ['revision', 'armed', 'evidence']
const found = (source: string) => findBannedWords(source, 'Example.tsx', words).map((use) => use.word)

describe('the copy word check', () => {
  it('flags a banned word in a string, a template and JSX text', () => {
    expect(found(`const a = 'Accepted at this revision.'`)).toEqual(['revision'])
    expect(found('const a = `Armed for ${seconds}s.`')).toEqual(['armed'])
    expect(found(`const a = <p title="No evidence yet">The show is armed.</p>`)).toEqual(['evidence', 'armed'])
  })

  it('does not flag identifiers, import paths, property names, API field names or class names', () => {
    expect(found(`import { evidence } from './evidence'\nconst revision = evidence.revision`)).toEqual([])
    expect(found(`const a = { 'armed state': 1 }\nconst b = row['armed state']\ntype C = 'armed state'`)).toEqual([])
    expect(found(`const a = state === 'armed' ? fields.get('revision') : '/api/v1/evidence'`)).toEqual([])
    expect(found('const a = <p className={`sm-evidence sm-evidence--${tone}`} data-testid="armed row" />')).toEqual([])
  })

  it('allows what the baseline lists and fails on one use more or one use fewer', () => {
    const counts = new Map([['screens/A.tsx\tarmed', { count: 2, uses: ['screens/A.tsx:1: Armed.', 'screens/A.tsx:2: Armed.'] }]])
    expect(checkCopyWords(counts, new Map([['screens/A.tsx\tarmed', 2]]))).toEqual([])
    expect(checkCopyWords(counts, new Map([['screens/A.tsx\tarmed', 1]]))).toHaveLength(1)
    expect(checkCopyWords(counts, new Map([['screens/A.tsx\tarmed', 3]]))).toHaveLength(1)
    expect(checkCopyWords(new Map(), new Map([['screens/A.tsx\tarmed', 1]]))).toHaveLength(1)
  })

  it('finds nothing under ui/src beyond the checked-in baseline', () => {
    expect(checkCopyWords()).toEqual([])
  })
})
