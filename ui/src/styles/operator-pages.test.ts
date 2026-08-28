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

  it('bounds the shared status grid inside the Live Control section instead of letting its intrinsic width widen the page', () => {
    const region = ruleBodyFor(css, '.live-control-status-region')
    const strip = ruleBodyFor(css, '.live-control-status-region .shared-status-strip')

    expect(region).toMatch(/min-width:\s*0/)
    expect(region).toMatch(/max-width:\s*100%/)
    expect(strip).toMatch(/width:\s*100%/)
    expect(strip).toMatch(/min-width:\s*0/)
    expect(strip).toMatch(/max-width:\s*100%/)
  })

  it('contains configured render evidence inside each Live Control group and scrolls its table locally', () => {
    const group = ruleBodyFor(css, '.live-control-group')
    const surface = ruleBodyFor(css, '.live-control-group .render-surface')
    const tableScroll = ruleBodyFor(css, '.live-control-group .table-scroll')

    expect(group).toMatch(/min-width:\s*0/)
    expect(group).toMatch(/max-width:\s*100%/)
    expect(surface).toMatch(/min-width:\s*0/)
    expect(surface).toMatch(/max-width:\s*100%/)
    expect(tableScroll).toMatch(/width:\s*100%/)
    expect(tableScroll).toMatch(/max-width:\s*100%/)
    expect(tableScroll).toMatch(/overflow-x:\s*auto/)
  })

  it('reserves full operator targets for Live Control fields without changing compact forms elsewhere', () => {
    const select = ruleBodyFor(css, '.live-control-page select')
    const textFieldLabel = ruleBodyFor(
      css,
      ".live-control-page .fpp-command-control > label:not(:has(> input[type='checkbox'])):not(:has(> input[type='radio']))",
    )

    expect(css).toMatch(
      /\.live-control-page input:not\(\[type='checkbox'\]\):not\(\[type='radio'\]\),\s*\.live-control-page select\s*\{[^}]*min-height:\s*var\(--touch-target-min\)/,
    )
    expect(select).toMatch(/min-height:\s*var\(--touch-target-min\)/)
    expect(textFieldLabel).toMatch(/display:\s*grid/)
    expect(textFieldLabel).toMatch(/min-width:\s*0/)
  })

  it('keeps checkbox and radio label targets centered and able to wrap on Live Control only', () => {
    expect(css).toMatch(
      /\.live-control-page label:has\(> input\[type='checkbox'\]\),\s*\.live-control-page label:has\(> input\[type='radio'\]\)\s*\{[^}]*align-items:\s*center[^}]*flex-wrap:\s*wrap[^}]*min-height:\s*var\(--touch-target-min\)/,
    )
    expect(css).not.toMatch(/(^|\n)input:not\(\[type='checkbox'\]\):not\(\[type='radio'\]\),\s*\nselect\s*\{[^}]*--touch-target-min/)
  })

  it('does not restore multi-column controls below the narrow breakpoint', () => {
    const narrow = css.slice(css.indexOf('@media (max-width: 620px)'))

    expect(narrow).not.toMatch(/\.live-control-command-rack/)
    expect(narrow).not.toMatch(/\.live-control-groups/)
    expect(narrow).not.toMatch(/\.live-control-unavailable/)
  })
})

describe('shared layout overflow constraints', () => {
  const css = readFileSync(CSS_PATH, 'utf8')

  it('keeps wide evidence in a bounded local scroll region with a visible keyboard focus treatment', () => {
    const evidence = ruleBodyFor(css, '.shared-evidence-table')
    const focus = ruleBodyFor(css, '.shared-evidence-table:focus-visible')

    expect(evidence).toMatch(/max-width:\s*100%/)
    expect(evidence).toMatch(/overflow-x:\s*auto/)
    expect(evidence).toMatch(/overscroll-behavior-inline:\s*contain/)
    expect(focus).toMatch(/outline:\s*2px solid var\(--color-focus-ring\)/)
  })

  it('lets overview and detail stack on narrow screens instead of forcing page-level overflow', () => {
    const workspace = ruleBodyFor(css, '.shared-overview-detail')
    const narrow = css.slice(css.lastIndexOf('@media (max-width: 620px)'))

    expect(workspace).toMatch(/min-width:\s*0/)
    expect(narrow).toMatch(/\.shared-overview-detail\s*\{\s*grid-template-columns:\s*1fr/)
  })
})
