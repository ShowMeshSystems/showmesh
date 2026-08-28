import { readFileSync } from 'node:fs'
import path from 'node:path'
import { describe, expect, it } from 'vitest'

const stylesDir = path.resolve(__dirname)

describe('operator shell foundation', () => {
  it('defines semantic surfaces, contrast-safe action text, and a five-role type scale for all supported themes', () => {
    const css = readFileSync(path.join(stylesDir, 'tokens.css'), 'utf8')

    expect(css).toMatch(/--color-graphite-950:\s*#111b1b/)
    expect(css).toMatch(/--color-blue-green-500:\s*#087f78/)
    expect(css).toMatch(/--color-accent:\s*var\(--color-blue-green-700\)/)
    expect(css).toMatch(/--color-on-accent:\s*#071817/)
    expect(css).toMatch(/--color-nav-surface:\s*var\(--color-surface-raised\)/)
    expect(css).toMatch(/--font-size-micro:\s*0\.6875rem/)
    expect(css).toMatch(/--font-size-small:\s*0\.75rem/)
    expect(css).toMatch(/--font-size-body:\s*0\.875rem/)
    expect(css).toMatch(/--font-size-heading:\s*1\.25rem/)
    expect(css).toMatch(/--font-size-display:\s*1\.5625rem/)
    expect(css).toMatch(/@media \(prefers-color-scheme: dark\)/)
    expect(css).toMatch(/:root\[data-contrast='high'\]/)
    expect(css).toMatch(/--color-focus-ring:\s*#ffff00/)
  })

  it('keeps the semantic rail in flow on phones and independently scrollable on desktop', () => {
    const css = readFileSync(path.join(stylesDir, 'global.css'), 'utf8')

    expect(css).toMatch(/\.app-sidebar\s*\{[\s\S]*?--rail-bg:\s*var\(--color-nav-surface\)/)
    expect(css).toMatch(/@media \(max-width: 767px\)[\s\S]*?\.app-sidebar\s*\{[\s\S]*?position:\s*static[\s\S]*?max-height:\s*none[\s\S]*?overflow:\s*visible/)
    expect(css).toMatch(/@media \(min-width: 768px\)[\s\S]*?\.app-sidebar\s*\{[\s\S]*?position:\s*sticky[\s\S]*?overflow-y:\s*auto/)
    expect(css).toMatch(/@media \(min-width: 768px\)[\s\S]*?width:\s*13rem/)
    expect(css).not.toMatch(/22rem/)
  })

  it('keeps the responsive nav compact without a fixed-height chooser', () => {
    const css = readFileSync(path.join(stylesDir, 'global.css'), 'utf8')
    expect(css).toMatch(/\.app-nav\s*\{[\s\S]*?position:\s*static/)
    expect(css).toMatch(/\.app-nav__group\[data-open='false'\]\s+\.app-nav__group-links\s*\{[\s\S]*?display:\s*none/)
    expect(css).toMatch(/\.app-nav__secondary-links/)
    expect(css).not.toMatch(/\.app-nav__legacy-group/)
    expect(css).not.toMatch(/position:\s*fixed/)
  })

  it('keeps shared action and choice targets at the outdoor 48px minimum', () => {
    const css = readFileSync(path.join(stylesDir, 'global.css'), 'utf8')

    expect(css).toMatch(/button,[\s\S]*?\.icon-button\s*\{[\s\S]*?min-height:\s*var\(--touch-target-min\)/)
    expect(css).toMatch(/\.icon-button\s*\{[\s\S]*?min-width:\s*var\(--touch-target-min\)/)
    expect(css).toMatch(/label:has\(> input\[type='checkbox'\]\),[\s\S]*?label:has\(> input\[type='radio'\]\)\s*\{[\s\S]*?min-height:\s*var\(--touch-target-min\)/)
    expect(css).toMatch(/label:has\(> input\[type='checkbox'\]\),[\s\S]*?cursor:\s*pointer/)
  })

  it('centers shared button content and makes table links distinct from row dividers', () => {
    const css = readFileSync(path.join(stylesDir, 'global.css'), 'utf8')

    expect(css).toMatch(/button,[\s\S]*?\.icon-button\s*\{[\s\S]*?display:\s*inline-flex[\s\S]*?align-items:\s*center[\s\S]*?justify-content:\s*center[\s\S]*?gap:\s*var\(--control-content-gap\)/)
    expect(css).toMatch(/\.config-table a\s*\{[\s\S]*?color:\s*var\(--color-link\)[\s\S]*?font-weight:\s*var\(--font-weight-medium\)[\s\S]*?text-underline-offset:\s*0\.24em/)
    expect(css).toMatch(/\.config-actions,[\s\S]*?\.config-save-row\s*\{[\s\S]*?gap:\s*var\(--control-content-gap\)/)
  })
})
