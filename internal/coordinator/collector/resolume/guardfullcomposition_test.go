package resolume

import (
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
)

// forbiddenFullCompositionPath is the exact request path a full-composition
// read builds. It is an EXACT match, not a substring check: a targeted
// by-id read (e.g. "/composition/clips/by-id/") is a different literal
// entirely and must never trip this guard — only the bare path a caller
// would hand to doGET to fetch the whole document is forbidden.
const forbiddenFullCompositionPath = "/composition"

// TestNoNonTestFileConstructsTheFullCompositionReadPath is this package's
// own structural proof of the rule its doc comment states in prose: no
// runtime path may call GET /composition. A comment saying so is not
// self-enforcing — the D-1 collector shipped with exactly that call still
// present despite the rule already being written down elsewhere, and it
// crashed the operator's real Arena less than a minute after its own read.
// This test is the mechanical version, mirroring
// internal/coordinator/collector/fpp/importgraph_test.go's own
// AST-based precedent (TestPackageSourceNeverConstructsACommandPath) for
// enforcing a "this package must never do X" rule with a test rather than
// a comment nobody re-reads before adding the next feature.
//
// It parses every non-test .go file in this package's directory and fails
// if any of them contains the string literal "/composition", as real
// compiled code rather than a comment (go/parser's AST never treats
// comment text as a BasicLit). That literal is the one and only thing a
// full-composition read needs to build: doGET concatenates it onto
// c.baseURL+apiPrefix and hands the result to Resolume. A targeted by-id
// read (a later seam's job) builds a DIFFERENT, longer literal — e.g.
// "/composition/clips/by-id/" — so this check does not, and must not,
// block that path forward.
//
// Before trusting this test: it was run against a deliberately broken
// working tree with Client.Composition (the exact method this package
// used to ship, reading GET /composition directly from a []byte via
// json.Decoder) pasted back into client.go, and failed as expected — see
// this task's own report for the exact diff exercised and the failure
// output it produced.
func TestNoNonTestFileConstructsTheFullCompositionReadPath(t *testing.T) {
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
			if val == forbiddenFullCompositionPath {
				t.Errorf("%s: string literal %q is the exact request path a full-composition read builds, present in REAL CODE, not a comment — "+
					"this call is known to crash the target Arena build (seven reproduced SIGSEGVs, including one from curl alone with no ShowMesh "+
					"process running), and the fix that removed it exists specifically so nothing in this package can rebuild that path by accident. "+
					"A targeted by-id read is a different, longer literal and is not what this check forbids.",
					fset.Position(lit.Pos()), val)
			}
			return true
		})
	}
}

// TestNoNonTestFileDefinesAFullCompositionDecodeMethod is a second,
// independent check on the same rule, at the symbol level rather than the
// string-literal level: this package's directory must never again define
// an exported method literally named "Composition" on any receiver — the
// exact name and shape the deleted Client.Composition method had. This
// catches a hypothetical reintroduction that builds the forbidden path
// from concatenated pieces (so no single literal equals
// forbiddenFullCompositionPath) but still names its method the way every
// caller of a "read the whole composition" method would expect to find
// it.
func TestNoNonTestFileDefinesAFullCompositionDecodeMethod(t *testing.T) {
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
		path := filepath.Join(".", name)
		f, err := parser.ParseFile(fset, path, nil, 0)
		if err != nil {
			t.Fatalf("parsing %s: %v", path, err)
		}
		for _, decl := range f.Decls {
			fn, ok := decl.(*ast.FuncDecl)
			if !ok || fn.Recv == nil {
				continue
			}
			if fn.Name.Name == "Composition" {
				t.Errorf("%s: a method named Composition is declared here — this is the exact name (and, historically, the exact job) of the "+
					"deleted full-composition-read method. GET /composition is forbidden at runtime for this package (it is known to crash the "+
					"target Arena build); if this is legitimately unrelated, rename it so it does not read as the reintroduction this test exists "+
					"to catch.",
					fset.Position(fn.Pos()))
			}
		}
	}
}
