import { readFileSync, readdirSync } from 'node:fs'
import path from 'node:path'
import ts from 'typescript'
import { describe, expect, it } from 'vitest'

// This file is the UI-side half of the regression guard for the defect
// the owner found by hand in the real browser: an FPP command control
// rendered an internal citation directly to the operator ("...pressing
// Next Item will END the show, the same way Stop does, not skip within
// it (docs/bench/fpp-command-vocabulary.md section 3.5)."). That
// reasoning belongs in a code comment — which this codebase's own
// convention already uses heavily — never in text a browser renders. See
// fppcommand_copy_guard_test.go in internal/coordinator/api for this
// guard's Go-side sibling, whose shape this file now follows exactly.
//
// This walks the real TypeScript AST (via the `typescript` package this
// repo already depends on for `tsc`) rather than grepping raw source
// text, for the same reason the Go sibling uses go/parser instead of a
// text regex: a comment is not a node this walk visits (TypeScript's
// parser attaches comments as trivia on tokens, never as part of the
// tree `forEachChild` descends), so a citation left in a `//` or `{/* */}`
// comment — exactly where this codebase's own convention already puts it
// — never trips this test, while a StringLiteral, template literal
// segment, or JsxText node (the raw text between JSX tags, which is NOT a
// StringLiteral and would be invisible to a check that only looked for
// string literals) always does.
//
// Inverted 2026-08-14 (Step 9 wave 3, this wave's own brief "Deliverable
// 0"): this test used to walk a HARDCODED file list
// (fppCommandCopyGuardFiles below, now deleted), which meant every file
// this wave — and every wave after it — adds to components/ or views/
// was unchecked by default, exactly backwards from what a guard against a
// defect this project has already shipped once should do. It now walks
// ui/src/components/ AND ui/src/views/ (this wave adds the first files
// under views/ that carry dense operator-facing macro-run copy) and
// checks every non-test .ts/.tsx file, carrying [copyGuardExemptions] as
// an explicit, narrow, STRING-LEVEL (never file-level) exemption list —
// mirroring the Go sibling's copyGuardExemptions exactly, and for the
// identical reason: a file-level exemption would remove coverage of
// every genuinely operator-facing string in the same file, which is a
// net loss precisely where a file mixes internal-only strings (an
// aria-label test id, a console.error, a code comment reused as a
// runtime string) with real operator copy — exactly the shape STEP-9-SPEC.md
// section 13 warns about.
//
// forbiddenCopyPattern is unchanged from the pre-inversion version: a
// repo path, a doc/spec file reference, an ADR or research-record
// number, the word "section" followed by a digit, or a literal reference
// to the OpenAPI spec file are all internal citations that must never
// reach an operator.
const forbiddenCopyPattern = /docs\/|\.md\b|ADR-\d+|RES-\d{3}|section\s+\d|api\/openapi\.yaml/i

// Extended 2026-08-14 (this task's finding 3, second half) to include
// src/app/: `app/session.ts` holds the scope-refusal copy that four of
// this wave's five new views (Macros, ShowActions, ShowActionDetail,
// MacroDetail, MacroRunView) render verbatim via evaluateScope/
// evaluateAnyScope, and nothing under src/app/ was walked at all before
// this fix. `readdirSync` (see collectTargetFiles below) is NOT
// recursive, so this only adds app/'s own top-level .ts/.tsx files —
// app/test-support/ is a subdirectory and stays out of this walk exactly
// the way components/ and views/ never had a subdirectory pulled in
// either; that is this walk's existing, unchanged shape, not a new carve
// -out for app/.
const TARGET_DIRS = [path.join(__dirname), path.join(__dirname, '..', 'views'), path.join(__dirname, '..', 'app')]

/**
 * One (file basename, exact string/JsxText VALUE) pair this guard does
 * not fail on. Matched against the trimmed text this walk already
 * extracts (see [collectViolations]'s own `textOf`), not a substring, so
 * an exemption cannot accidentally also cover some other, unrelated
 * string that happens to contain the same words — identical matching
 * discipline to the Go sibling's copyGuardExemption.
 *
 * Empty at inversion time: every file this walk currently covers passed
 * with no exemption needed (see this task's own report for what the
 * inversion actually surfaced). Kept as a real, typed list rather than
 * deleted, so the FIRST genuinely-internal-only string this project adds
 * under components/ or views/ has a place to go that is scoped to that
 * one string, not to the file it lives in.
 */
interface CopyGuardExemption {
  file: string
  text: string
}
const copyGuardExemptions: CopyGuardExemption[] = []

function isExempt(file: string, text: string): boolean {
  return copyGuardExemptions.some((e) => e.file === file && e.text === text)
}

function collectTargetFiles(): string[] {
  const files: string[] = []
  for (const dir of TARGET_DIRS) {
    for (const name of readdirSync(dir)) {
      if (!name.endsWith('.ts') && !name.endsWith('.tsx')) continue
      if (name.endsWith('.test.ts') || name.endsWith('.test.tsx')) continue
      files.push(path.join(dir, name))
    }
  }
  return files.sort()
}

function collectViolations(filePath: string, sourceText: string): string[] {
  const scriptKind = filePath.endsWith('.tsx') ? ts.ScriptKind.TSX : ts.ScriptKind.TS
  const sourceFile = ts.createSourceFile(filePath, sourceText, ts.ScriptTarget.Latest, true, scriptKind)
  const violations: string[] = []
  const baseName = path.basename(filePath)

  function textOf(node: ts.Node): string | undefined {
    if (ts.isStringLiteralLike(node)) return node.text
    if (ts.isJsxText(node)) return node.getText(sourceFile)
    // Fixed 2026-08-14 (this task's finding 3): `ts.isStringLiteralLike`
    // is ONLY `StringLiteral | NoSubstitutionTemplateLiteral` — a template
    // literal WITH a substitution (`` `See ${x}` ``) is neither. Its own
    // text lives split across a TemplateHead, zero or more
    // TemplateMiddles, and a TemplateTail instead (TemplateExpression's
    // own children, all visited independently by this walk's own
    // `forEachChild` recursion below), every one of which was previously
    // invisible here — a citation embedded in ANY segment tripped nothing.
    // Proved live with a throwaway probe file (this task's own report):
    // before this fix, a template literal reading `` `See ADR-999 for
    // ${label}` `` in JSX passed this guard with zero violations.
    if (ts.isTemplateHead(node) || ts.isTemplateMiddle(node) || ts.isTemplateTail(node)) return node.text
    return undefined
  }

  function visit(node: ts.Node) {
    const text = textOf(node)
    if (text !== undefined) {
      const match = forbiddenCopyPattern.exec(text)
      if (match && !isExempt(baseName, text.trim())) {
        const { line } = sourceFile.getLineAndCharacterOfPosition(node.getStart(sourceFile))
        violations.push(
          `${filePath}:${line + 1}: carries an internal citation (${JSON.stringify(match[0])}): ${JSON.stringify(text.trim())}`,
        )
      }
    }
    ts.forEachChild(node, visit)
  }
  visit(sourceFile)
  return violations
}

describe('operator-facing strings under components/ and views/ carry no internal citation', () => {
  for (const filePath of collectTargetFiles()) {
    const relative = path.relative(path.join(__dirname, '..'), filePath)
    it(relative, () => {
      const source = readFileSync(filePath, 'utf-8')
      const violations = collectViolations(filePath, source)
      expect(violations, violations.join('\n')).toEqual([])
    })
  }
})
