import { readFileSync } from 'node:fs'
import path from 'node:path'
import { describe, expect, it } from 'vitest'

const stylesDir = path.resolve(__dirname)

describe('operator shell foundation', () => {
  it('defines the design-guide token set on :root, with light and contrast theme overrides', () => {
    const css = readFileSync(path.join(stylesDir, 'tokens.css'), 'utf8')

    // Surfaces, text, and status roles from UI-DESIGN-GUIDE.md section 1.
    for (const token of [
      '--bg',
      '--surface',
      '--raised',
      '--sunken',
      '--border',
      '--border-strong',
      '--text',
      '--text-muted',
      '--text-faint',
      '--hatch',
      '--accent',
      '--accent-hover',
      '--accent-active',
      '--accent-bg',
      '--accent-border',
      '--on-accent',
      '--focus',
    ]) {
      expect(css).toMatch(new RegExp(`${token}:`))
    }
    for (const status of ['good', 'warn', 'bad', 'unk']) {
      expect(css).toMatch(new RegExp(`--${status}-fg:`))
      expect(css).toMatch(new RegExp(`--${status}-bg:`))
      expect(css).toMatch(new RegExp(`--${status}-border:`))
    }

    // Dark is the default palette on bare :root, not a media-query fallback.
    expect(css).toMatch(/:root\s*\{[\s\S]*?--bg:\s*#080c0e/)

    // Explicit theme overrides, plus the OS-preference fallback guarded so
    // an explicit choice always wins.
    expect(css).toMatch(/:root\[data-theme='light'\]\s*\{[\s\S]*?--bg:\s*#f2f5f4/)
    expect(css).toMatch(/:root\[data-theme='contrast'\]\s*\{[\s\S]*?--bg:\s*#000000/)
    expect(css).toMatch(/@media \(prefers-color-scheme: light\)[\s\S]*?:root:not\(\[data-theme\]\)/)

    // Accent, focus, and on-accent match the design guide's dark values.
    expect(css).toMatch(/--accent:\s*oklch\(0\.845 0\.112 181\)/)
    expect(css).toMatch(/--focus:\s*oklch\(0\.88 0\.09 181\)/)
    expect(css).toMatch(/--on-accent:\s*#05191a/)

    // Type faces and the seven type roles.
    expect(css).toMatch(/--sans:\s*Archivo/)
    expect(css).toMatch(/--mono:\s*'JetBrains Mono'/)
    for (const role of ['display', 'heading', 'subhead', 'body', 'small', 'meta', 'data']) {
      expect(css).toMatch(new RegExp(`--t-${role}:`))
    }
  })

  it('keeps every old tokens.css custom property resolvable through the transition alias block', () => {
    const css = readFileSync(path.join(stylesDir, 'tokens.css'), 'utf8')

    expect(css).toMatch(/Transition aliases: deleted at the end of the overhaul\./)

    for (const oldToken of [
      '--color-graphite-50',
      '--color-graphite-100',
      '--color-graphite-300',
      '--color-graphite-600',
      '--color-graphite-800',
      '--color-graphite-950',
      '--color-blue-green-500',
      '--color-blue-green-700',
      '--color-bg',
      '--color-surface',
      '--color-surface-raised',
      '--color-border',
      '--color-text',
      '--color-text-muted',
      '--color-accent',
      '--color-focus-ring',
      '--color-link',
      '--color-on-accent',
      '--color-nav-surface',
      '--color-nav-border',
      '--color-nav-text',
      '--color-nav-text-muted',
      '--color-nav-hover',
      '--shadow-nav',
      '--status-good-bg',
      '--status-good-fg',
      '--status-warn-bg',
      '--status-warn-fg',
      '--status-bad-bg',
      '--status-bad-fg',
      '--status-unknown-bg',
      '--status-unknown-fg',
      '--connection-problem-bg',
      '--connection-problem-fg',
      '--connection-problem-border',
      '--connection-live-fg',
      '--notice-info-bg',
      '--notice-info-fg',
      '--notice-info-border',
      '--space-1',
      '--space-2',
      '--space-3',
      '--space-4',
      '--space-5',
      '--space-6',
      '--space-7',
      '--control-height-sm',
      '--control-height',
      '--control-height-lg',
      '--control-content-gap',
      '--font-family',
      '--font-family-mono',
      '--font-size-micro',
      '--font-size-small',
      '--font-size-body',
      '--font-size-heading',
      '--font-size-display',
      '--font-size-3xs',
      '--font-size-2xs',
      '--font-size-xs',
      '--font-size-sm',
      '--font-size-md',
      '--font-size-lg',
      '--font-size-xl',
      '--font-size-2xl',
      '--font-weight-normal',
      '--font-weight-medium',
      '--font-weight-bold',
      '--line-height-normal',
      '--line-height-tight',
      '--touch-target-min',
      '--radius-sm',
      '--radius-md',
      '--radius-control',
    ]) {
      expect(css).toMatch(new RegExp(`${oldToken}:`))
    }
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
