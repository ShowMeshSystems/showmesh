import { readFileSync, readdirSync } from 'node:fs'
import path from 'node:path'
import { describe, expect, it } from 'vitest'

const STYLES = path.join(__dirname, 'styles')
const files = readdirSync(STYLES).filter((name) => name.endsWith('.css'))
const sources = files.map((name) => ({ name, css: readFileSync(path.join(STYLES, name), 'utf8') }))

/*
 * The kit replaces the pre-overhaul design rather than repainting it. The old
 * system stayed alive the first time because tokens.css aliased every old name
 * onto a new value, so the old stylesheets kept working unnoticed. These names
 * are the alias block; none of them may appear in the kit.
 */
const RETIRED_TOKENS = [
  '--color-',
  '--font-size-',
  '--font-family',
  '--font-weight-',
  '--line-height-',
  '--status-',
  '--radius-sm',
  '--radius-md',
  '--radius-control',
  '--control-height',
  '--touch-target-min',
  '--shadow-nav',
  '--connection-',
  '--notice-',
]

describe('kit isolation', () => {
  it('has stylesheets to check', () => {
    expect(files.length).toBeGreaterThan(0)
  })

  it.each(sources)('$name references no retired token name', ({ css }) => {
    for (const token of RETIRED_TOKENS) expect(css).not.toContain(token)
  })

  it.each(sources)('$name imports nothing from outside the kit', ({ css }) => {
    const imports = [...css.matchAll(/@import\s+['"]([^'"]+)['"]/g)].map((match) => match[1] ?? '')
    for (const target of imports) expect(target.startsWith('./')).toBe(true)
  })

  it('defines every space, radius and control token the kit uses', () => {
    const tokens = readFileSync(path.join(STYLES, 'tokens.css'), 'utf8')
    const declared = new Set([...tokens.matchAll(/^\s*(--[a-z0-9-]+):/gm)].map((match) => match[1] ?? ''))
    const used = new Set(sources.flatMap(({ css }) => [...css.matchAll(/var\((--[a-z0-9-]+)\)/g)].map((match) => match[1] ?? '')))
    const missing = [...used].filter((token) => !declared.has(token))
    expect(missing).toEqual([])
  })

  it('keeps the dashed edge for never-collected evidence only', () => {
    const status = sources.find((entry) => entry.name === 'status.css')?.css ?? ''
    const dashed = [...status.matchAll(/\.sm-status--([a-z]+)\s*\{([^}]*)\}/g)]
      .filter(([, , body]) => (body ?? '').includes('dashed'))
      .map(([, tone]) => tone)
    expect(dashed).toEqual([])
  })

  it('gives the gloved control the 48px height the guide requires', () => {
    const tokens = readFileSync(path.join(STYLES, 'tokens.css'), 'utf8')
    expect(tokens).toMatch(/--ctrl-h-gloved:\s*48px/)
  })
})
