package resolume

import (
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"strconv"
	"strings"
	"testing"
)

// positionalCollections is every Resolume composition collection name a
// path segment can name. "objects" is included deliberately: the bare
// "/composition/objects/{id}/..." form is Arena's OSC shortcut-binding
// address space, and an id there can never satisfy the by-id requirement
// below — so including "objects" here is an unconditional ban on that
// whole address space, not an oversight.
var positionalCollections = map[string]bool{
	"layers":      true,
	"clips":       true,
	"columns":     true,
	"decks":       true,
	"layergroups": true,
	"groups":      true,
	"objects":     true,
}

// TestNoNonTestFileBuildsAPositionalCompositionPath is this package's AST
// guard against positional addressing (CLAUDE.md, Track D: "object ids
// only, including internally"). It parses every non-test .go file's string
// literals and, for every "composition" path segment, requires that if the
// NEXT segment names a collection, the segment after THAT is "by-id" — the
// only address form this package's Client is allowed to build. A
// positional form like "/composition/layers/3/clips/2/connect" names a
// slot in a list, which moves under drag-reorder in Resolume's own UI;
// "/composition/layers/by-id/{id}" names the object itself, and only that
// form survives a reorder.
//
// AST-based on BasicLit, never a byte grep: doc.go's own prose names these
// exact forbidden path shapes while documenting this rule, and a grep
// would fail the build on the comment that documents it — the identical
// reasoning guardfullcomposition_test.go already states for its own check.
func TestNoNonTestFileBuildsAPositionalCompositionPath(t *testing.T) {
	entries, err := os.ReadDir(".")
	if err != nil {
		t.Fatalf("reading package directory: %v", err)
	}

	fset := token.NewFileSet()
	for _, entry := range entries {
		name := entry.Name()
		if entry.IsDir() || !strings.HasSuffix(name, ".go") || strings.HasSuffix(name, "_test.go") {
			continue
		}
		f, err := parser.ParseFile(fset, name, nil, 0)
		if err != nil {
			t.Fatalf("parsing %s: %v", name, err)
		}
		ast.Inspect(f, func(n ast.Node) bool {
			lit, ok := n.(*ast.BasicLit)
			if !ok || lit.Kind != token.STRING {
				return true
			}
			val, err := strconv.Unquote(lit.Value)
			if err != nil {
				return true
			}
			checkNoPositionalSegment(t, fset, lit, val)
			return true
		})
	}
}

// checkNoPositionalSegment implements the rule stated on
// TestNoNonTestFileBuildsAPositionalCompositionPath's own doc comment for
// one already-unquoted string literal.
func checkNoPositionalSegment(t *testing.T, fset *token.FileSet, lit *ast.BasicLit, val string) {
	segs := strings.Split(val, "/")
	for i, seg := range segs {
		if seg != "composition" {
			continue
		}
		if i+1 >= len(segs) || !positionalCollections[segs[i+1]] {
			// Not addressing a collection at all (e.g. "/composition/disconnect-all",
			// or "composition" as a bare word in an unrelated string) — not
			// what this rule forbids.
			continue
		}
		if i+2 < len(segs) && segs[i+2] == "by-id" {
			continue
		}
		t.Errorf("%s: string literal %q addresses \"%s/%s\" positionally, in REAL CODE, not a comment — "+
			"the only address form this package's Client may build is \"/composition/%s/by-id/{id}\". "+
			"A positional path names a slot in a list that moves under drag-reorder in Resolume's own UI; "+
			"by-id names the object itself. If this is genuinely a different address space, resolve the id "+
			"through the stored id map first and build the by-id path from that.",
			fset.Position(lit.Pos()), val, seg, segs[i+1], segs[i+1])
	}
}
