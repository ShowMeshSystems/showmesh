import { readFileSync } from 'node:fs'
import path from 'node:path'
import { describe, expect, it } from 'vitest'

const CSS_PATH = path.join(__dirname, 'operator-pages.css')

function ruleBodyFor(css: string, selector: string): string {
  const escaped = selector.replace(/[.*+?^${}()|[\]\\]/g, '\\$&')
  const match = new RegExp(`(?<![\\w-])${escaped}(?![\\w-])\\s*\\{([^}]*)\\}`).exec(css)
  if (!match) throw new Error(`could not find ${selector} in ${CSS_PATH}`)
  return match[1] ?? ''
}

describe('Live Control layout constraints', () => {
  const css = readFileSync(CSS_PATH, 'utf8')

  it('lets the node and Resolume sections stack before a fixed side column can overflow', () => {
    const columns = ruleBodyFor(css, '.live-control-columns')

    expect(columns).toMatch(/grid-template-columns:\s*repeat\(auto-fit, minmax\(min\(100%, 28rem\), 1fr\)\)/)
    expect(columns).not.toMatch(/18rem/)
  })

  it('keeps each command and its outcome in a bounded, wrapping rack', () => {
    const rack = ruleBodyFor(css, '.live-control-command-rack')
    const cell = ruleBodyFor(css, '.live-control-command-rack > *')

    expect(rack).toMatch(/display:\s*grid/)
    expect(rack).toMatch(/grid-template-columns:\s*repeat\(auto-fit, minmax\(min\(100%, 14rem\), 1fr\)\)/)
    expect(rack).toMatch(/min-width:\s*0/)
    expect(cell).toMatch(/min-width:\s*0/)
    expect(cell).toMatch(/align-content:\s*start/)
  })

  it('does not restore multi-column controls below the narrow breakpoint', () => {
    const narrow = css.slice(css.indexOf('@media (max-width: 620px)'))

    expect(narrow).not.toMatch(/\.live-control-command-rack/)
    expect(narrow).not.toMatch(/\.live-control-groups/)
    expect(narrow).not.toMatch(/\.live-control-unavailable/)
  })
})
