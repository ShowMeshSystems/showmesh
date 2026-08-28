import { readFileSync } from 'node:fs'
import path from 'node:path'
import { describe, expect, it } from 'vitest'

const stylesDir = path.resolve(__dirname)

describe('operator shell foundation', () => {
  it('defines graphite and blue-green semantic roles for all supported themes', () => {
    const css = readFileSync(path.join(stylesDir, 'tokens.css'), 'utf8')

    expect(css).toMatch(/--color-graphite-950:\s*#111b1b/)
    expect(css).toMatch(/--color-blue-green-500:\s*#087f78/)
    expect(css).toMatch(/--color-accent:\s*var\(--color-blue-green-700\)/)
    expect(css).toMatch(/@media \(prefers-color-scheme: dark\)/)
    expect(css).toMatch(/:root\[data-contrast='high'\]/)
    expect(css).toMatch(/--color-focus-ring:\s*#ffff00/)
  })

  it('keeps the graphite rail in flow on phones and independently scrollable on desktop', () => {
    const css = readFileSync(path.join(stylesDir, 'global.css'), 'utf8')

    expect(css).toMatch(/\.app-sidebar\s*\{[\s\S]*?--rail-bg:\s*var\(--color-graphite-950\)/)
    expect(css).toMatch(/@media \(max-width: 767px\)[\s\S]*?\.app-sidebar\s*\{[\s\S]*?position:\s*static[\s\S]*?max-height:\s*none[\s\S]*?overflow:\s*visible/)
    expect(css).toMatch(/@media \(min-width: 768px\)[\s\S]*?\.app-sidebar\s*\{[\s\S]*?position:\s*sticky[\s\S]*?overflow-y:\s*auto/)
    expect(css).toMatch(/@media \(min-width: 768px\)[\s\S]*?width:\s*13rem/)
    expect(css).not.toMatch(/22rem/)
  })

  it('keeps the responsive nav compact without a fixed-height chooser', () => {
    const css = readFileSync(path.join(stylesDir, 'global.css'), 'utf8')
    expect(css).toMatch(/\.app-nav\s*\{[\s\S]*?position:\s*static/)
    expect(css).toMatch(/\.app-nav__group\[data-open='false'\]\s+\.app-nav__group-links\s*\{[\s\S]*?display:\s*none/)
    expect(css).toMatch(/@media \(max-width: 767px\)[\s\S]*?\.app-nav__primary-links,[\s\S]*?display:\s*grid/)
    expect(css).not.toMatch(/position:\s*fixed/)
  })
})
