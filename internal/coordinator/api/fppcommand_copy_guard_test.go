package api

import (
	"go/ast"
	"go/parser"
	"go/token"
	"regexp"
	"testing"
)

// This file is the regression guard for the defect the owner found by
// hand in the real UI: an outcome reason and a Problem detail both leaked
// an internal citation ("docs/bench/fpp-command-vocabulary.md section
// 3.5", "docs/bench/fpp-command-vocabulary.md section 3.3") onto the
// wire, where the Operator UI and showmeshctl render it verbatim to the
// operator. That reasoning belongs in a code comment, never in a string
// this package hands to a caller — see CLAUDE.md and this task's own
// report for the full accounting.
//
// This test parses the SOURCE of every file in this step's own seam —
// never runs them, never guesses which scenario would exercise which
// message — and inspects every string literal that appears in CODE (an
// *ast.BasicLit of kind STRING, walked via the expression tree
// go/parser builds). A comment is not part of that tree: go/parser
// discards comments from the nodes ast.Inspect walks unless asked to
// retain them as *ast.CommentGroup, and this walk never asks for or
// visits those, so a citation left in a `//` comment (exactly where this
// codebase's own convention already puts it) never trips this test.
// Every remaining string literal is exactly the class of string this
// package can hand to a caller: a v1.Problem Title/Detail/Type, an
// outcomeReason, a validation message, or a literal fed into fmt.Sprintf
// to build one of those — there is no OTHER kind of user-visible string
// literal in these files, so scanning the whole file is precise, not a
// heuristic that happens to work today.
//
// forbiddenCopyPattern is deliberately broader than "the two strings the
// owner happened to hit": a repo path, a doc/spec file reference, an ADR
// or research-record number, or the word "section" followed by a digit
// are ALL internal citations that must never reach an operator, per
// CLAUDE.md's own rule restated in this task's brief. Any one of these
// appearing in a new string literal added to this seam later fails this
// test immediately, rather than waiting for another owner bug report.
//
// Track D seam D-2a added `resolumecomp:` — a SECOND defect class, found
// the same way (the owner loading the real UI): a rejected composition
// upload rendered "...: resolumecomp: root element is not <Composition>:
// found <NotAComposition>" verbatim, where "resolumecomp:" is
// pkg/resolumecomp's own sentinel-error prefix, a Go package name with no
// meaning to an operator. That fix moved the translation into
// resolumecomposition.go (a switch over the sentinel errors, never
// err.Error()), so this alternative exists only to catch a REGRESSION
// that reintroduces the package name as a literal (e.g. a hardcoded
// string copied from a code comment or a wrapped-error example) — it
// cannot, by itself, catch a revived `fmt.Sprintf("...: %v", err)`, since
// the offending text would only exist at runtime, in err's own value,
// never in a string literal this AST walk can see; the runtime half of
// this regression class is guarded separately, by
// TestResolumeCompositionUploadRejectionDetailNamesNoGoPackage in
// resolumecomposition_test.go, which asserts on a REAL response body.
//
// This is deliberately the one package name this seam's files actually
// wrap errors from, not a general `\w+:` rule: a general rule would match
// ordinary English prose like "Note: " or "Example: " (each a lowercase
// word followed by a colon-space, indistinguishable from a Go package
// prefix by shape alone) and make this guard too noisy to keep. Widen
// this alternation, package name by package name, if a future seam wraps
// another internal package's sentinel errors the same way.
var forbiddenCopyPattern = regexp.MustCompile(
	`docs/|\.md\b|ADR-\d+|RES-\d{3}|(?i)\bsection\s+\d|api/openapi\.yaml|\bresolumecomp:`,
)

// fppCommandCopyGuardFiles is every source file this step's operator-
// facing strings live in — the same seam named in this task's own brief
// (fppcommand_evidence.go, fppcommand_primitives.go, problem.go in this
// package, plus internal/coordinator/fppcommand/validation.go, read by
// relative path exactly like loadOpenAPIDocument in openapi_test.go
// already reads api/openapi.yaml from this same test working directory).
var fppCommandCopyGuardFiles = []string{
	"fppcommand_evidence.go",
	"fppcommand_primitives.go",
	"problem.go",
	"../fppcommand/validation.go",

	// fppcommand_dispatch.go: Step 9's exported in-process dispatch core.
	// It builds *v1.Problem values through the same constructors this
	// seam always has (invalidParameterProblem, resourceNotFoundProblem,
	// ...), so it is exactly the class of file this guard exists to
	// cover — added in the wave that adds it, not left for a later one to
	// discover uncovered.
	"fppcommand_dispatch.go",

	// resolumecomposition.go: Track D seam D-2a's own operator-facing
	// strings (the composition upload/read handlers' Problem
	// Title/Detail). Added alongside the `resolumecomp:` alternative in
	// forbiddenCopyPattern above, for the identical reason
	// fppcommand_dispatch.go was added in the wave that added it.
	"resolumecomposition.go",
}

// TestOperatorFacingStringsCarryNoInternalCitation is this task's own
// regression guard. Verified per this project's standing rule ("break the
// behavior, confirm the test fails, restore"): temporarily reintroducing
// the owner's own original string ("...docs/bench/fpp-command-vocabulary.md
// section 3.5)") into fppcommand_evidence.go's
// evaluateFPPStopGracefullyEvidence and rerunning this test turns it from
// passing to failing, naming the exact file, line, and offending
// substring — confirmed by hand during this task, then reverted.
func TestOperatorFacingStringsCarryNoInternalCitation(t *testing.T) {
	for _, path := range fppCommandCopyGuardFiles {
		path := path
		t.Run(path, func(t *testing.T) {
			fset := token.NewFileSet()
			file, err := parser.ParseFile(fset, path, nil, 0)
			if err != nil {
				t.Fatalf("parsing %s: %v", path, err)
			}
			ast.Inspect(file, func(n ast.Node) bool {
				lit, ok := n.(*ast.BasicLit)
				if !ok || lit.Kind != token.STRING {
					return true
				}
				if loc := forbiddenCopyPattern.FindString(lit.Value); loc != "" {
					pos := fset.Position(lit.Pos())
					t.Errorf(
						"%s:%d: string literal carries an internal citation (%q matched by forbiddenCopyPattern): %s\n"+
							"move the citation into a // comment; the string itself must read as if no internal doc, "+
							"ADR, or research record existed",
						path, pos.Line, loc, lit.Value)
				}
				return true
			})
		})
	}
}
