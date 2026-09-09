package api

import (
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
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
// This test parses the SOURCE of every non-test .go file in this
// package's own directory — never runs them, never guesses which
// scenario would exercise which message — and inspects every string
// literal that appears in CODE (an *ast.BasicLit of kind STRING, walked
// via the expression tree go/parser builds). A comment is not part of
// that tree: go/parser discards comments from the nodes ast.Inspect walks
// unless asked to retain them as *ast.CommentGroup, and this walk never
// asks for or visits those, so a citation left in a `//` comment (exactly
// where this codebase's own convention already puts it) never trips this
// test.
//
// Inverted 2026-08-14 (Step 9 wave 2, this wave's own brief section 8,
// STEP-9-SPEC.md section 13): this test used to walk a HARDCODED file
// list (fppcommand_evidence.go, fppcommand_primitives.go, problem.go,
// fppcommand_dispatch.go, plus one file in a sibling package), which meant
// every file this step adds — showconfig.go, macroruns.go, and the rest —
// was unchecked by default, exactly backwards from what a guard against a
// defect this project has already shipped once should do. It now walks
// this package's own directory and checks every non-test .go file,
// carrying [copyGuardExemptions] as an explicit, narrow, STRING-LEVEL
// (never file-level) exemption list — see that var's own doc comment for
// why a file-level exemption would be a net loss exactly where this
// inversion needs coverage most.
//
// forbiddenCopyPattern is deliberately broader than "the two strings the
// owner happened to hit": a repo path, a doc/spec file reference, an ADR
// or research-record number, or the word "section" followed by a digit
// are ALL internal citations that must never reach an operator, per
// CLAUDE.md's own rule restated in this task's brief. Any one of these
// appearing in a new string literal added to this package later fails
// this test immediately, rather than waiting for another owner bug
// report.
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

// copyGuardAdditionalFiles is every file OUTSIDE this package's own
// directory that must still be walked — internal/coordinator/fppcommand
// is a sibling package this package's HTTP surface builds
// [v1.Problem]-shaped strings out of (fppcommand_handler.go's own
// resolveFPPCommandReplay path reads its ValidationError text verbatim
// into a Detail), so a citation added there is exactly as reachable by an
// operator as one added in this directory, and the pre-inversion guard
// already covered it for that reason — carried forward unchanged.
var copyGuardAdditionalFiles = []string{
	"../fppcommand/validation.go",
}

// copyGuardExemption is one (file, exact string literal VALUE) pair this
// guard does not fail on. Matched against [ast.BasicLit.Value] — the raw
// source text of the literal, including its surrounding quotes — not a
// substring, so an exemption cannot accidentally also cover some other,
// unrelated string that happens to contain the same words.
type copyGuardExemption struct {
	file  string
	value string
}

// copyGuardExemptions is this guard's ENTIRE exemption list, and every
// entry is a STRING this file's own inversion pass (2026-08-14) verified
// by hand is genuinely server-side-only, never rendered to an operator —
// never a whole file. STEP-9-SPEC.md section 13 / this wave's brief
// section 8 are explicit that a file-level exemption "removes coverage of
// every genuinely operator-facing string in the same file... exactly
// where the text density is highest", which is precisely backwards for a
// file like fppcommand_handler.go that carries BOTH kinds of string.
//
// Both entries here are [degradedAttributionReasonSafetyClassExemption]/
// [degradedAttributionReasonPostDispatch] (fppcommand_handler.go): fed
// exclusively into [handlers.reportDegradedAttribution], whose own doc
// comment states plainly it produces "the best-effort, human-readable
// stderr line" — never a v1.Problem field, an outcomeReason, or any other
// path to the wire. Confirmed by reading every call site of both
// constants (fppcommand_dispatch.go) before exempting either.
var copyGuardExemptions = []copyGuardExemption{
	{"fppcommand_handler.go", `"ADR-024 decision 11's blackout/stop/power-off safety class exemption (pre-dispatch write)"`},
	{"fppcommand_handler.go", `"the event this entry records already happened and cannot be un-recorded; refusing to answer would only deny the operator the record of it (ADR-024: \"you cannot see\", never acceptable), not protect them from anything"`},
	// Third entry, added 2026-08-14 with the owner decision that a macro
	// run never withholds a command for an audit failure. Same disposition
	// as the two above, verified the same way: it is fed only into
	// [handlers.reportDegradedAttribution], which writes to this process's
	// own stderr and reaches no client, and it names the superseded
	// decision deliberately so whoever reads that log line can find what
	// made this branch reachable.
	{"fppcommand_handler.go", `"this dispatch belongs to a macro run, which never withholds a command for an audit failure (owner decision 2026-08-14, superseding ADR-024 decision 11's fail-closed default inside a run)"`},
	// Fourth entry, added with ADR-024 decision 11's 2026-08-26 amendment
	// (audit-store unavailability never blocks an action). Same
	// disposition as the three above, verified the same way: fed only
	// into [handlers.reportDegradedAttribution], which writes to this
	// process's own stderr and reaches no client.
	{"fppcommand_handler.go", `"ADR-024 decision 11's audit-unavailability-never-blocks rule (owner ruling 2026-08-26): this action is not a member of the blackout/stop/power-off safety class and does not belong to a macro run, and still proceeds without a durable pre-dispatch audit entry"`},
}

func copyGuardExemptionSet() map[copyGuardExemption]bool {
	set := make(map[copyGuardExemption]bool, len(copyGuardExemptions))
	for _, e := range copyGuardExemptions {
		set[e] = true
	}
	return set
}

// copyGuardTargetFiles walks this package's own directory (".") for every
// non-test .go file, and appends [copyGuardAdditionalFiles]. Sorted, so
// t.Run subtest names — and any failure output — are stable across runs.
func copyGuardTargetFiles(t *testing.T) []string {
	t.Helper()
	entries, err := os.ReadDir(".")
	if err != nil {
		t.Fatalf("reading this package's own directory: %v", err)
	}
	var files []string
	for _, e := range entries {
		name := e.Name()
		if e.IsDir() || !strings.HasSuffix(name, ".go") || strings.HasSuffix(name, "_test.go") {
			continue
		}
		files = append(files, name)
	}
	files = append(files, copyGuardAdditionalFiles...)
	sort.Strings(files)
	return files
}

// TestOperatorFacingStringsCarryNoInternalCitation is this task's own
// regression guard. Verified per this project's standing rule ("break the
// behavior, confirm the test fails, restore"): temporarily reintroducing
// the owner's own original string ("...docs/bench/fpp-command-vocabulary.md
// section 3.5)") into fppcommand_evidence.go's
// evaluateFPPStopGracefullyEvidence and rerunning this test turns it from
// passing to failing, naming the exact file, line, and offending
// substring. ALSO verified for the inversion itself (2026-08-14): removing
// [copyGuardExemptions] entirely and rerunning turns the two genuinely
// internal-log-only strings in fppcommand_handler.go into failures too,
// confirming the walk actually reaches them now that it is directory-wide
// rather than a hardcoded list. Both checks confirmed by hand during this
// task, then reverted/restored.
func TestOperatorFacingStringsCarryNoInternalCitation(t *testing.T) {
	exemptions := copyGuardExemptionSet()
	for _, path := range copyGuardTargetFiles(t) {
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
				if exemptions[copyGuardExemption{file: filepath.Base(path), value: lit.Value}] {
					return true
				}
				if loc := forbiddenCopyPattern.FindString(lit.Value); loc != "" {
					pos := fset.Position(lit.Pos())
					t.Errorf(
						"%s:%d: string literal carries an internal citation (%q matched by forbiddenCopyPattern): %s\n"+
							"move the citation into a // comment; the string itself must read as if no internal doc, "+
							"ADR, or research record existed. If this string is genuinely server-log-only (never "+
							"reaches a client), add a copyGuardExemption naming this exact string and the reason.",
						path, pos.Line, loc, lit.Value)
				}
				return true
			})
		})
	}
}
