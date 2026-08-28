import { readFileSync } from 'node:fs'
import path from 'node:path'
import { describe, expect, it } from 'vitest'

// Regression guard for the defect the owner found by hand: the readability
// pass that added `.evidence__age--inline` also demoted `.evidence__age`
// and `.evidence__reason` (the lines EvidenceValue.tsx renders for every
// non-"current" evidence state -- stale, unknown_age, not_collected,
// collection_failed, unsupported) from --font-size-sm to --font-size-xs.
// Net effect: a stale/failed reading's freshness and reason text rendered
// SMALLER than the same freshness line on a healthy ("current") signal,
// exactly backwards from ADR-011 ("nothing that is not 'current' is ever
// collapsed, hidden, quieted, or gated behind a toggle").
//
// jsdom performs no layout, so a component test (EvidenceValue.test.tsx)
// cannot assert computed pixel size -- it can only prove which class name
// lands on which element. This file instead reads the stylesheet source
// directly and compares font-size *tokens*, following the precedent set
// by fppCommandCopyGuard.test.ts (also a plain source-text/AST read
// rather than a rendered-DOM assertion, for the same reason: the property
// under test does not exist in jsdom's rendering model).
//
// This does not re-verify EvidenceValue.tsx's render logic (which class
// goes on which element) -- that is EvidenceValue.test.tsx's job. This
// only pins the CSS token comparison between the two classes so neither
// can be silently re-demoted.

const STATUS_CSS_PATH = path.join(__dirname, 'status.css')

const FONT_SIZE_STEPS = [
  '--font-size-micro',
  '--font-size-small',
  '--font-size-body',
  '--font-size-heading',
  '--font-size-display',
]

function stepIndex(token: string): number {
  const index = FONT_SIZE_STEPS.indexOf(token)
  if (index === -1) {
    throw new Error(`unrecognized font-size token ${JSON.stringify(token)}; update FONT_SIZE_STEPS`)
  }
  return index
}

// Extracts the `font-size: var(--font-size-*)` value from the first rule
// block whose selector matches `selector` exactly (not a substring, so
// `.evidence__age` does not also match `.evidence__age--inline`). The
// lookbehind/lookahead (rather than requiring a preceding `}`) is needed
// because most rules in this file are preceded by a `/* ... */` comment,
// not directly by another rule's closing brace.
function fontSizeTokenFor(css: string, selector: string): string {
  const escaped = selector.replace(/[.*+?^${}()|[\]\\]/g, '\\$&')
  const pattern = new RegExp(`(?<![\\w-])${escaped}(?![\\w-])\\s*\\{([^}]*)\\}`)
  const match = pattern.exec(css)
  if (!match) {
    throw new Error(`could not find a rule block for selector ${JSON.stringify(selector)} in ${STATUS_CSS_PATH}`)
  }
  const body = match[1] ?? ''
  const fontSizeMatch = /font-size:\s*var\((--font-size-[a-z]+)\)/.exec(body)
  const token = fontSizeMatch?.[1]
  if (token === undefined) {
    throw new Error(`selector ${JSON.stringify(selector)} has no font-size: var(--font-size-*) declaration`)
  }
  return token
}

describe('evidence non-current text is never quieted below the current-state treatment (ADR-011)', () => {
  const css = readFileSync(STATUS_CSS_PATH, 'utf-8')

  it('.evidence__age--inline is the current-state (compact) baseline', () => {
    // Sanity check that the baseline selector itself still exists with a
    // real token, so a rename of this class silently passes this file.
    expect(() => fontSizeTokenFor(css, '.evidence__age--inline')).not.toThrow()
  })

  it.each(['.evidence__age', '.evidence__reason'])(
    '%s is not smaller than .evidence__age--inline',
    (selector) => {
      const inlineStep = stepIndex(fontSizeTokenFor(css, '.evidence__age--inline'))
      const nonCurrentStep = stepIndex(fontSizeTokenFor(css, selector))
      expect(nonCurrentStep).toBeGreaterThanOrEqual(inlineStep)
    },
  )

  it('.evidence__reason--violation does not override font-size to something smaller', () => {
    const pattern = /(?<![\w-])\.evidence__reason--violation(?![\w-])\s*\{([^}]*)\}/
    const match = pattern.exec(css)
    if (!match) {
      throw new Error('could not find a rule block for .evidence__reason--violation')
    }
    // The violation modifier is expected to override color/weight only,
    // inheriting font-size from .evidence__reason. If it ever sets its
    // own font-size, that size must not regress below the baseline.
    const fontSizeMatch = /font-size:\s*var\((--font-size-[a-z]+)\)/.exec(match[1] ?? '')
    const token = fontSizeMatch?.[1]
    if (token === undefined) {
      return
    }
    const inlineStep = stepIndex(fontSizeTokenFor(css, '.evidence__age--inline'))
    expect(stepIndex(token)).toBeGreaterThanOrEqual(inlineStep)
  })
})
