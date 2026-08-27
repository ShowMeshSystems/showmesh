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

  it('keeps the phone chooser fixed and the larger directory independently scrollable', () => {
    const css = readFileSync(path.join(stylesDir, 'global.css'), 'utf8')

    expect(css).toMatch(/@media \(max-width: 767px\)[\s\S]*?\.app-sidebar\s*\{[\s\S]*?position:\s*fixed/)
    expect(css).toMatch(/@media \(min-width: 768px\)[\s\S]*?\.app-sidebar\s*\{[\s\S]*?overflow-y:\s*auto/)
    expect(css).toMatch(/\.app-nav__directory\s*\{\s*display:\s*none/)
    expect(css).toMatch(/\.app-nav__directory\s*> summary\s*\{[\s\S]*?cursor:\s*pointer/)
  })

  it('reserves clearance for the wrapped two-row phone chooser at 360px', () => {
    const css = readFileSync(path.join(stylesDir, 'global.css'), 'utf8')
    const phoneBlock = css.match(/@media \(max-width: 767px\) \{([\s\S]*?)\n\}/)
    expect(phoneBlock).not.toBeNull()
    const body = phoneBlock?.[1] ?? ''
    expect(body).toMatch(/padding-bottom:\s*calc\(22rem \+ env\(safe-area-inset-bottom, 0px\)\)/)
    expect(body).toMatch(/\.app-sidebar\s*\{[\s\S]*?max-height:\s*min\(50vh, 22rem\)/)
    expect(css).toMatch(/\.app-nav__primary-links\s*\{[\s\S]*?flex-wrap:\s*wrap/)
    expect(css).toMatch(/\.app-nav__primary-link\s*\{[\s\S]*?flex:\s*0 0 50%/)
  })
})
