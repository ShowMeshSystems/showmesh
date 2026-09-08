#!/usr/bin/env node
// Fails when any file under ui/src contains a NUL byte. Standard text tools
// (grep, ripgrep) classify such a file as binary and skip it silently, so a
// search that should have found a symbol instead returns nothing, and looks
// identical to a search that correctly found no match.
import { readFileSync, readdirSync, statSync } from 'node:fs'
import path from 'node:path'
import { fileURLToPath } from 'node:url'

const __dirname = path.dirname(fileURLToPath(import.meta.url))
export const SRC_ROOT = path.join(__dirname, '..', 'src')

function collectFiles(dir, out) {
  for (const name of readdirSync(dir)) {
    if (name === 'node_modules' || name === '.git') continue
    const full = path.join(dir, name)
    const stat = statSync(full)
    if (stat.isDirectory()) {
      collectFiles(full, out)
      continue
    }
    out.push(full)
  }
  return out
}

/** Every file under `root` containing a NUL byte, one string per file, file-relative. */
export function checkNoNulBytes(root = SRC_ROOT) {
  const violations = []
  for (const file of collectFiles(root, [])) {
    const relative = path.relative(root, file)
    const buffer = readFileSync(file)
    if (buffer.includes(0)) violations.push(`${relative}: contains a NUL byte`)
  }
  return violations
}

const isMain = process.argv[1] !== undefined && path.resolve(process.argv[1]) === path.resolve(fileURLToPath(import.meta.url))
if (isMain) {
  const violations = checkNoNulBytes()
  if (violations.length > 0) {
    console.error('NUL byte check failed:')
    for (const violation of violations) console.error(`  ${violation}`)
    process.exit(1)
  }
  console.log('NUL byte check passed: no file under ui/src contains a NUL byte.')
}
