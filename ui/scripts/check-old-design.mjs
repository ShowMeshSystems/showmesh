#!/usr/bin/env node
// Phase 2 lock-out (REBUILD-PLAN.md): fails when any file under ui/src
// references a retired token name or imports a stylesheet outside
// ui/src/kit/styles. Shared by the npm test suite and the build script,
// so neither can drift from the other.
import { readFileSync, readdirSync, statSync } from 'node:fs'
import path from 'node:path'
import { fileURLToPath } from 'node:url'

const __dirname = path.dirname(fileURLToPath(import.meta.url))
export const SRC_ROOT = path.join(__dirname, '..', 'src')
const KIT_STYLES = path.join(__dirname, '..', 'src', 'kit', 'styles')

/**
 * Every custom property `ui/src/styles/tokens.css`'s alias block mapped onto
 * the new system, recovered from `git show 1a1eed1^:ui/src/styles/tokens.css`
 * (that file was deleted by 1a1eed1, "Delete the old operator UI and stand
 * the kit shell up"). None of these names exist in the new token system.
 */
export const OLD_TOKEN_NAMES = [
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
]

const STYLESHEET_REFERENCE = /(?:@import\s+['"]([^'"]+)['"])|(?:\bfrom\s+['"]([^'"]+\.css)['"])|(?:\bimport\s+['"]([^'"]+\.css)['"])/g

// Test files are exempt: kitIsolation.test.ts lists these same retired
// names as fixture data to test against, which is not a use of them.
function collectFiles(dir, out) {
  for (const name of readdirSync(dir)) {
    if (name === 'node_modules' || name === '.git') continue
    const full = path.join(dir, name)
    const stat = statSync(full)
    if (stat.isDirectory()) {
      collectFiles(full, out)
      continue
    }
    if (name.endsWith('.test.ts') || name.endsWith('.test.tsx')) continue
    if (/\.(ts|tsx|css)$/.test(name)) out.push(full)
  }
  return out
}

function isInsideKitStyles(resolved) {
  const relative = path.relative(KIT_STYLES, resolved)
  return relative !== '' && !relative.startsWith('..') && !path.isAbsolute(relative)
}

/** Every offense found under `root`, one string per offense, file-relative. */
export function checkOldDesign(root = SRC_ROOT) {
  const violations = []
  for (const file of collectFiles(root, [])) {
    const relative = path.relative(root, file)
    const source = readFileSync(file, 'utf8')

    for (const token of OLD_TOKEN_NAMES) {
      if (source.includes(token)) violations.push(`${relative}: references retired token ${token}`)
    }

    for (const match of source.matchAll(STYLESHEET_REFERENCE)) {
      const target = match[1] ?? match[2] ?? match[3]
      if (target === undefined || !target.startsWith('.')) continue
      const resolved = path.resolve(path.dirname(file), target)
      if (!isInsideKitStyles(resolved)) violations.push(`${relative}: imports stylesheet outside kit/styles (${target})`)
    }
  }
  return violations
}

const isMain = process.argv[1] !== undefined && path.resolve(process.argv[1]) === path.resolve(fileURLToPath(import.meta.url))
if (isMain) {
  const violations = checkOldDesign()
  if (violations.length > 0) {
    console.error('Old design lock-out check failed:')
    for (const violation of violations) console.error(`  ${violation}`)
    process.exit(1)
  }
  console.log('Old design lock-out check passed: no retired token and no stylesheet outside kit/styles.')
}
