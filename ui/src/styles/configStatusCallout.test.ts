import { readFileSync } from 'node:fs'
import path from 'node:path'
import { describe, expect, it } from 'vitest'

// Regression guard for the defect the owner found by hand: the readability
// pass on Configuration.tsx (#116) changed the active-revision block from
// `<p className="panel" role="status">` to `<div className="config-status">`.
// `.config-status` (below) defines only margins -- no border, background,
// or padding -- so the restart-required notice (kept bold via <strong>)
// lost the bordered callout treatment that made it read as a distinct,
// unmissable block rather than ordinary paragraph text.
//
// The fix pairs the existing `.panel` class back onto the element
// (Configuration.tsx renders `className="config-status panel"`), reusing
// this codebase's one shared bordered-panel treatment instead of adding a
// new one. Configuration.test.tsx's own
// "renders the active-revision block with the shared bordered callout
// treatment" proves the class actually lands on the rendered element; this
// file instead reads the stylesheet source directly (jsdom performs no
// layout, so a component test cannot assert computed border/background)
// and pins that `.panel` itself still declares a border and a background,
// following the precedent set by fppCommandCopyGuard.test.ts and
// evidenceLoudnessGuard.test.ts (plain source-text reads rather than
// rendered-DOM assertions, for properties jsdom cannot compute).

const GLOBAL_CSS_PATH = path.join(__dirname, 'global.css')

function ruleBodyFor(css: string, selector: string): string {
  const escaped = selector.replace(/[.*+?^${}()|[\]\\]/g, '\\$&')
  const pattern = new RegExp(`(?<![\\w-])${escaped}(?![\\w-])\\s*\\{([^}]*)\\}`)
  const match = pattern.exec(css)
  if (!match) {
    throw new Error(`could not find a rule block for selector ${JSON.stringify(selector)} in ${GLOBAL_CSS_PATH}`)
  }
  return match[1] ?? ''
}

describe('the active-revision callout keeps a bordered treatment (defect follow-up to #116)', () => {
  const css = readFileSync(GLOBAL_CSS_PATH, 'utf-8')

  it('.config-status defines margins only, and relies on a companion class for its box treatment', () => {
    const body = ruleBodyFor(css, '.config-status')
    expect(body).not.toMatch(/border/)
    expect(body).not.toMatch(/background/)
  })

  it('.panel still declares a border and a background using existing tokens, no literal colour values', () => {
    const body = ruleBodyFor(css, '.panel')
    expect(body).toMatch(/border:\s*1px solid var\(--color-border\)/)
    expect(body).toMatch(/background:\s*var\(--color-surface\)/)
    // No literal colour values (hex/rgb/hsl) introduced for this treatment.
    expect(body).not.toMatch(/#[0-9a-fA-F]{3,8}\b/)
    expect(body).not.toMatch(/rgba?\(|hsla?\(/)
  })
})
