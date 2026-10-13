#!/usr/bin/env node
// Fails when copy under ui/src uses a word from the operator copy standard's
// banned list more often than scripts/copy-words-baseline.txt allows. The
// list is internal/copycheck/banned-words.txt, the file the Go check embeds.
// Run with --update after removing uses, so the baseline only shrinks.
import { readFileSync, readdirSync, statSync, writeFileSync } from 'node:fs'
import path from 'node:path'
import { fileURLToPath } from 'node:url'
import ts from 'typescript'

const __dirname = path.dirname(fileURLToPath(import.meta.url))
export const SRC_ROOT = path.join(__dirname, '..', 'src')
export const WORDS_FILE = path.join(__dirname, '..', '..', 'internal', 'copycheck', 'banned-words.txt')
export const BASELINE_FILE = path.join(__dirname, 'copy-words-baseline.txt')

export function readBannedWords(file = WORDS_FILE) {
  return readFileSync(file, 'utf8')
    .split('\n')
    .map((line) => line.split('#')[0].trim().toLowerCase())
    .filter((word) => word !== '')
}

// Attributes whose value is a name for the program, never text for a person.
const NON_COPY_ATTRIBUTES = /^(className|id|key|to|href|src|htmlFor|name|type|role|rel|target|method|data-.*)$/
const COMPARISONS = new Set([
  ts.SyntaxKind.EqualsEqualsEqualsToken,
  ts.SyntaxKind.ExclamationEqualsEqualsToken,
  ts.SyntaxKind.EqualsEqualsToken,
  ts.SyntaxKind.ExclamationEqualsToken,
])

// A string in one of these positions is an import path, a property or API
// field name, a type, or an enum value being compared.
function isNameNotCopy(node) {
  const parent = node.parent
  if (ts.isImportDeclaration(parent) || ts.isExportDeclaration(parent) || ts.isExternalModuleReference(parent)) return true
  if (ts.isCallExpression(parent) && parent.expression.kind === ts.SyntaxKind.ImportKeyword) return true
  if (ts.isLiteralTypeNode(parent) || ts.isImportTypeNode(parent)) return true
  if (ts.isElementAccessExpression(parent) && parent.argumentExpression === node) return true
  if ((ts.isPropertyAssignment(parent) || ts.isPropertySignature(parent) || ts.isPropertyDeclaration(parent) || ts.isMethodDeclaration(parent) || ts.isEnumMember(parent)) && parent.name === node) return true
  if (ts.isCaseClause(parent)) return true
  if (ts.isBinaryExpression(parent) && COMPARISONS.has(parent.operatorToken.kind)) return true
  return insideNonCopyAttribute(node)
}

function insideNonCopyAttribute(node) {
  for (let ancestor = node.parent; ancestor !== undefined; ancestor = ancestor.parent) {
    if (ts.isJsxAttribute(ancestor)) return NON_COPY_ATTRIBUTES.test(ancestor.name.getText())
    if (ts.isJsxElement(ancestor) || ts.isJsxFragment(ancestor) || ts.isFunctionLike(ancestor) || ts.isStatement(ancestor)) return false
  }
  return false
}

// A string with no space that starts lowercase is a key, a path or an enum
// value, such as 'revision' or '/api/v1/revisions', and a run of sm- words is
// a class list. Copy has a space or a capital.
function looksLikeName(text) {
  if (!/\s/.test(text)) return !/^[A-Z]/.test(text)
  return text.trim().split(/\s+/).every((token) => token.startsWith('sm-') || token === '${}')
}

function templateText(node) {
  return [node.head.text, ...node.templateSpans.map((span) => span.literal.text)].join('${}')
}

/** Every banned word in the copy of one source file: [{ word, line, text }]. */
export function findBannedWords(source, fileName, words = readBannedWords()) {
  const pattern = new RegExp(`\\b(${words.join('|')})\\b`, 'gi')
  const sourceFile = ts.createSourceFile(fileName, source, ts.ScriptTarget.Latest, true, fileName.endsWith('x') ? ts.ScriptKind.TSX : ts.ScriptKind.TS)
  const found = []
  const scan = (node, text) => {
    const line = sourceFile.getLineAndCharacterOfPosition(node.getStart()).line + 1
    for (const match of text.matchAll(pattern)) found.push({ word: match[1].toLowerCase(), line, text: text.trim() })
  }
  const visit = (node) => {
    if (ts.isJsxText(node)) {
      scan(node, node.text)
    } else if (ts.isStringLiteral(node) || ts.isNoSubstitutionTemplateLiteral(node)) {
      if (!isNameNotCopy(node) && !looksLikeName(node.text)) scan(node, node.text)
    } else if (ts.isTemplateExpression(node)) {
      const text = templateText(node)
      if (!ts.isTaggedTemplateExpression(node.parent) && !isNameNotCopy(node) && !looksLikeName(text)) scan(node, text)
    }
    ts.forEachChild(node, visit)
  }
  visit(sourceFile)
  return found
}

function collectFiles(dir, out) {
  for (const name of readdirSync(dir).sort()) {
    const full = path.join(dir, name)
    if (statSync(full).isDirectory()) {
      if (name !== 'node_modules' && name !== 'generated' && name !== 'test-support') collectFiles(full, out)
      continue
    }
    if (/\.test\.tsx?$/.test(name) || name.endsWith('.d.ts')) continue
    if (/\.tsx?$/.test(name)) out.push(full)
  }
  return out
}

/** Uses per file and word under root: Map of "file\tword" to { count, uses }. */
export function countBannedWords(root = SRC_ROOT, words = readBannedWords()) {
  const counts = new Map()
  for (const file of collectFiles(root, [])) {
    const relative = path.relative(root, file).split(path.sep).join('/')
    for (const use of findBannedWords(readFileSync(file, 'utf8'), file, words)) {
      const key = `${relative}\t${use.word}`
      const entry = counts.get(key) ?? { count: 0, uses: [] }
      entry.count += 1
      entry.uses.push(`${relative}:${use.line}: ${use.text}`)
      counts.set(key, entry)
    }
  }
  return counts
}

export function readBaseline(file = BASELINE_FILE) {
  const baseline = new Map()
  for (const line of readFileSync(file, 'utf8').split('\n')) {
    if (line === '' || line.startsWith('#')) continue
    const [relative, word, count] = line.split('\t')
    baseline.set(`${relative}\t${word}`, Number(count))
  }
  return baseline
}

function renderBaseline(counts) {
  const lines = [
    '# Banned words still in operator UI copy: file, word, number of uses.',
    '# A use beyond this list fails. A line this list overstates is only reported;',
    '# drop it with',
    '# node scripts/check-copy-words.mjs --update',
  ]
  for (const key of [...counts.keys()].sort()) lines.push(`${key}\t${counts.get(key).count}`)
  return lines.join('\n') + '\n'
}

/**
 * One string per problem: a use the baseline does not allow. A baseline line that
 * overstates the code goes to stale, and becomes a problem only when
 * SHOWMESH_COPYCHECK_STALE=fail.
 */
export function checkCopyWords(counts = countBannedWords(), baseline = readBaseline(), stale = []) {
  const problems = []
  const staleTarget = process.env.SHOWMESH_COPYCHECK_STALE === 'fail' ? problems : stale
  for (const [key, entry] of counts) {
    const allowed = baseline.get(key) ?? 0
    const [relative, word] = key.split('\t')
    if (entry.count > allowed) {
      problems.push(`${relative}: "${word}" is used ${entry.count} times in copy, the baseline allows ${allowed}:\n${entry.uses.map((use) => `      ${use}`).join('\n')}`)
    } else if (entry.count < allowed) {
      staleTarget.push(`${relative}: "${word}" is used ${entry.count} times, fewer than the baseline's ${allowed}. Run with --update.`)
    }
  }
  for (const [key, allowed] of baseline) {
    if (!counts.has(key)) staleTarget.push(`${key.replace('\t', ': "')}" is no longer used, but the baseline allows ${allowed}. Run with --update.`)
  }
  return problems
}

const isMain = process.argv[1] !== undefined && path.resolve(process.argv[1]) === path.resolve(fileURLToPath(import.meta.url))
if (isMain) {
  const counts = countBannedWords()
  if (process.argv.includes('--update')) {
    writeFileSync(BASELINE_FILE, renderBaseline(counts))
    console.log(`Copy word baseline rewritten: ${counts.size} entries.`)
  } else {
    const stale = []
    const problems = checkCopyWords(counts, readBaseline(), stale)
    if (stale.length > 0) {
      console.warn('Copy word check: not a failure. These baseline lines overstate the code. Run with --update to drop them.')
      for (const line of stale) console.warn(`  ${line}`)
    }
    if (problems.length > 0) {
      console.error('Copy word check failed:')
      for (const problem of problems) console.error(`  ${problem}`)
      process.exit(1)
    }
    console.log(`Copy word check passed: no banned word beyond the ${counts.size} baseline entries.`)
  }
}
