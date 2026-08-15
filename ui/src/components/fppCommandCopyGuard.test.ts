import { readFileSync } from 'node:fs'
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
// guard's Go-side sibling, which covers the server strings the same
// defect leaked from.
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
// Track D seam D-2a added `resolumecomp:` — a SECOND defect class, found
// the same way (the owner loading the real UI, this time on a rejected
// composition upload): the server used to send
// "...: resolumecomp: root element is not <Composition>: found
// <NotAComposition>" as the problem detail, and this component rendered
// it verbatim — "resolumecomp:" is a Go package name (pkg/resolumecomp's
// own sentinel-error prefix), meaningless to an operator. That fix moved
// the translation server-side (internal/coordinator/api/resolumecomposition.go
// now maps the sentinel error to an operator sentence before it ever
// reaches the wire), so this component holds no such literal today and
// this alternative exists only to catch a regression that hardcodes one
// here — the UI holds no parsing or validation logic of its own, so if
// this string ever appears in this file at all, something worth stopping
// on has already gone wrong at this layer. See fppcommand_copy_guard_test.go's
// Go-side sibling for the identical rule and why it stays narrow to this
// one package name rather than a general `\w+:` pattern.
const forbiddenCopyPattern = /docs\/|\.md\b|ADR-\d+|RES-\d{3}|section\s+\d|resolumecomp:/i

// fppCommandCopyGuardFiles is every component this step's operator-facing
// command copy lives in, plus Configuration.tsx (Step 7's own configuration
// write surface, fixed by the same task alongside this step's own
// controls once the same defect class was found there too).
const fppCommandCopyGuardFiles = [
  'FPPPlaylistTransportControls.tsx',
  'FPPSetVolumeControl.tsx',
  'FPPStartPlaylistControl.tsx',
  'FPPStopPlaylistControl.tsx',
  'FPPStopPlaylistGracefullyControl.tsx',
  'FPPCommandOutcome.tsx',
  // Track D seam D-2a (ADR-032 decision 8): the composition upload
  // control, added to this guard's file list for the identical reason as
  // every entry above — operator-facing copy here must never cite this
  // repository's own paths, ADR numbers, or doc sections. See that
  // component's own comments (which this guard's AST walk never visits —
  // this file's own header comment explains why) for where the citations
  // actually live instead.
  'ResolumeCompositionUpload.tsx',
]

function collectViolations(filePath: string, sourceText: string): string[] {
  const sourceFile = ts.createSourceFile(filePath, sourceText, ts.ScriptTarget.Latest, true, ts.ScriptKind.TSX)
  const violations: string[] = []

  function textOf(node: ts.Node): string | undefined {
    if (ts.isStringLiteralLike(node)) return node.text
    if (ts.isJsxText(node)) return node.getText(sourceFile)
    return undefined
  }

  function visit(node: ts.Node) {
    const text = textOf(node)
    if (text !== undefined) {
      const match = forbiddenCopyPattern.exec(text)
      if (match) {
        const { line } = sourceFile.getLineAndCharacterOfPosition(node.getStart(sourceFile))
        violations.push(`${filePath}:${line + 1}: carries an internal citation (${JSON.stringify(match[0])}): ${JSON.stringify(text.trim())}`)
      }
    }
    ts.forEachChild(node, visit)
  }
  visit(sourceFile)
  return violations
}

describe('operator-facing FPP command copy carries no internal citation', () => {
  for (const file of fppCommandCopyGuardFiles) {
    it(file, () => {
      const filePath = path.join(__dirname, file)
      const source = readFileSync(filePath, 'utf-8')
      const violations = collectViolations(file, source)
      expect(violations, violations.join('\n')).toEqual([])
    })
  }
})
