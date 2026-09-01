package api

import (
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"io/fs"
	"strings"
	"testing"
)

// This file enforces, at test time, a convention this package has so far
// only stated in prose (nightsessioncontrol.go's nightCommandOutcome doc
// comment, and the review finding it records): a night-command "decide"
// function, the closure argument named decide in nightRunExempt's and
// nightRunGated's own signatures, must never write to the *store.Tx it
// is handed. Every transactional write belongs inside the two runners
// (nightRunExempt, nightRunGated) themselves, which perform it once,
// after decide has returned, from the nightCommandOutcome data decide
// produced. Before this file, nothing but review caught a decide function
// that reached back into tx and wrote directly (a real defect this
// package's own history already records: nightsessioncontrol.go's
// nightCommandOutcome doc comment describes decide once calling
// tx.CreateNightReadiness itself, invisibly to nightRunGated, and the fix
// that stopped it).
//
// The check below discovers its own subjects rather than naming them: it
// parses this package's own source to find every call site of
// nightRunExempt/nightRunGated, locates the decide argument by the
// parameter NAME "decide" in each runner's own declared signature (not by
// position), and separately parses the store package's source to collect
// every *store.Tx method whose name is not prefixed Get/List/Find (this
// package's own established read-verb convention, see store/*.go's
// method names). From there it walks each decide closure's body,
// transitively following any call that passes the tx parameter into a
// named function this package declares, and flags any call of a
// discovered write method directly on that parameter. A rename or a new
// decide function needs no update here: the walk re-derives its subjects
// from source every run.
//
// [TestDecideClosuresNeverCallTxWriteMethodsDirectly] is the real
// enforcement, run against this package's actual source (read-only, this
// file only parses internal/coordinator/api/night*.go and
// emergencystop.go, it never writes to them).
// [TestTxWriteBoundaryWalkerCatchesADirectWriteOnASyntheticFixture] is this
// walker's positive control: a small, in-memory decide/runner pair,
// parsed from a Go source literal in this test file (never written to
// disk, and never touching internal/coordinator/api/night* or
// emergencystop.go), where the synthetic decide function calls a write
// method on tx directly, proving the walker actually catches that shape
// rather than passing vacuously because it never finds anything to flag.

type txWriteBoundaryViolation struct {
	pos  string
	desc string
}

// parseGoPackage parses every non-test .go file in dir and returns every
// top-level func/method declaration, keyed by name (receiver type is not
// part of the key: this package's decide/runner functions are all methods
// on *handlers, and a same-named free function or method on an unrelated
// type would be a false positive risk this comment accepts rather than
// hides, see [findDecideClosures]'s use of len(decls) == 1 checks on the
// two runner names themselves).
func parseGoPackage(t *testing.T, dir string) (*token.FileSet, map[string][]*ast.FuncDecl) {
	t.Helper()
	fset := token.NewFileSet()
	pkgs, err := parser.ParseDir(fset, dir, func(fi fs.FileInfo) bool {
		return !strings.HasSuffix(fi.Name(), "_test.go")
	}, 0)
	if err != nil {
		t.Fatalf("parse %s: %v", dir, err)
	}
	funcs := map[string][]*ast.FuncDecl{}
	for _, pkg := range pkgs {
		for _, file := range pkg.Files {
			for _, decl := range file.Decls {
				if fd, ok := decl.(*ast.FuncDecl); ok {
					funcs[fd.Name.Name] = append(funcs[fd.Name.Name], fd)
				}
			}
		}
	}
	return fset, funcs
}

// isPointerReceiverNamed reports whether fd is declared with a pointer
// receiver of the given type name, e.g. "(t *Tx)".
func isPointerReceiverNamed(fd *ast.FuncDecl, name string) bool {
	if fd.Recv == nil || len(fd.Recv.List) != 1 {
		return false
	}
	star, ok := fd.Recv.List[0].Type.(*ast.StarExpr)
	if !ok {
		return false
	}
	ident, ok := star.X.(*ast.Ident)
	return ok && ident.Name == name
}

// txWriteMethodSet parses the store package's own source and returns
// every exported *Tx method whose name is not prefixed Get, List, or
// Find, this package's own established read-verb convention (confirmed
// against every *Tx method store/*.go declares as of this fix). Discovered
// fresh from source on every run, never a maintained list, so a renamed
// or newly added *Tx method is picked up automatically.
func txWriteMethodSet(t *testing.T, storeDir string) map[string]bool {
	t.Helper()
	_, funcs := parseGoPackage(t, storeDir)
	writes := map[string]bool{}
	for name, decls := range funcs {
		for _, fd := range decls {
			if !isPointerReceiverNamed(fd, "Tx") {
				continue
			}
			if !fd.Name.IsExported() {
				continue // unexported *Tx methods (e.g. after) are not reachable from package api at all
			}
			if strings.HasPrefix(name, "Get") || strings.HasPrefix(name, "List") || strings.HasPrefix(name, "Find") {
				continue
			}
			writes[name] = true
		}
	}
	if len(writes) == 0 {
		t.Fatalf("txWriteMethodSet found zero write methods on *store.Tx in %s; "+
			"this almost certainly means the parse itself found nothing (a path or filter bug), "+
			"not that store.Tx genuinely has no write methods, a vacuous set would make every "+
			"enforcement check in this file pass by finding nothing to flag", storeDir)
	}
	return writes
}

// decideParamIndex returns the flattened parameter index of the parameter
// literally named "decide" in fd's own signature, or -1 if none exists.
// Flattened because Go allows grouped parameters (a, b string).
func decideParamIndex(fd *ast.FuncDecl) int {
	idx := 0
	for _, field := range fd.Type.Params.List {
		n := len(field.Names)
		if n == 0 {
			n = 1
		}
		for i := 0; i < n; i++ {
			if len(field.Names) > 0 && field.Names[i].Name == "decide" {
				return idx
			}
			idx++
		}
	}
	return -1
}

// findDecideClosures locates every call site of nightRunExempt/
// nightRunGated in funcs and returns the *ast.FuncLit passed as their
// decide argument. Fails the test outright (rather than silently finding
// zero) if either runner cannot be found by name, is declared more than
// once, or has no parameter literally named "decide", any of those means
// this file's structural assumption about "the two runners" no longer
// matches the source, which must stop the build from certifying a check
// that can no longer see what it claims to check.
func findDecideClosures(t *testing.T, funcs map[string][]*ast.FuncDecl) []*ast.FuncLit {
	t.Helper()
	const (
		runnerExempt = "nightRunExempt"
		runnerGated  = "nightRunGated"
	)
	paramIdx := map[string]int{}
	for _, runner := range []string{runnerExempt, runnerGated} {
		decls, ok := funcs[runner]
		if !ok || len(decls) != 1 {
			t.Fatalf("expected exactly one declaration of %s in internal/coordinator/api, found %d; "+
				"this test's structural discovery depends on that runner existing by this exact name", runner, len(decls))
		}
		idx := decideParamIndex(decls[0])
		if idx < 0 {
			t.Fatalf("%s has no parameter literally named %q; this test locates the decide argument "+
				"by that parameter name, not by position, and cannot proceed without it", runner, "decide")
		}
		paramIdx[runner] = idx
	}

	var closures []*ast.FuncLit
	for _, decls := range funcs {
		for _, fd := range decls {
			if fd.Body == nil {
				continue
			}
			ast.Inspect(fd.Body, func(n ast.Node) bool {
				call, ok := n.(*ast.CallExpr)
				if !ok {
					return true
				}
				sel, ok := call.Fun.(*ast.SelectorExpr)
				if !ok {
					return true
				}
				idx, known := paramIdx[sel.Sel.Name]
				if !known || idx >= len(call.Args) {
					return true
				}
				if lit, ok := call.Args[idx].(*ast.FuncLit); ok {
					closures = append(closures, lit)
				}
				return true
			})
		}
	}
	return closures
}

// isStoreTxType reports whether expr is exactly *store.Tx.
func isStoreTxType(expr ast.Expr) bool {
	star, ok := expr.(*ast.StarExpr)
	if !ok {
		return false
	}
	sel, ok := star.X.(*ast.SelectorExpr)
	if !ok {
		return false
	}
	pkgIdent, ok := sel.X.(*ast.Ident)
	return ok && pkgIdent.Name == "store" && sel.Sel.Name == "Tx"
}

// funcTxParamName returns the name of fn's first parameter typed
// *store.Tx, or "" if it has none.
func funcTxParamName(fields *ast.FieldList) string {
	if fields == nil {
		return ""
	}
	for _, field := range fields.List {
		if !isStoreTxType(field.Type) {
			continue
		}
		if len(field.Names) > 0 {
			return field.Names[0].Name
		}
	}
	return ""
}

// walkForTxWrites walks body looking for calls of shape
// "<txVar>.<WriteMethod>(...)" and reports every one it finds. It also
// follows two kinds of indirection, transitively: a call to another
// function declared in funcs that receives txVar as one of its own
// arguments (recursing into that function's body with its matching
// parameter's name), and a nested function literal within body (recursing
// with its own *store.Tx parameter's name if it declares one, or txVar
// itself if the literal instead captures it by closure). visited guards
// against infinite recursion on a call cycle.
func walkForTxWrites(
	body *ast.BlockStmt, txVar string,
	funcs map[string][]*ast.FuncDecl, writeMethods map[string]bool,
	fset *token.FileSet, visited map[string]bool,
	out *[]txWriteBoundaryViolation,
) {
	if body == nil || txVar == "" {
		return
	}
	ast.Inspect(body, func(n ast.Node) bool {
		if lit, ok := n.(*ast.FuncLit); ok {
			inner := txVar
			if pn := funcTxParamName(lit.Type.Params); pn != "" {
				inner = pn
			}
			walkForTxWrites(lit.Body, inner, funcs, writeMethods, fset, visited, out)
			return false // already recursed manually; do not also let Inspect descend into it
		}

		call, ok := n.(*ast.CallExpr)
		if !ok {
			return true
		}

		var calleeName string
		switch fn := call.Fun.(type) {
		case *ast.SelectorExpr:
			if ident, ok := fn.X.(*ast.Ident); ok && ident.Name == txVar {
				if writeMethods[fn.Sel.Name] {
					*out = append(*out, txWriteBoundaryViolation{
						pos:  fset.Position(call.Pos()).String(),
						desc: fmt.Sprintf("calls tx.%s(...) directly, which belongs inside the two runners, not a decide function", fn.Sel.Name),
					})
				}
				return true
			}
			calleeName = fn.Sel.Name
		case *ast.Ident:
			calleeName = fn.Name
		default:
			return true
		}

		argIdx := -1
		for i, arg := range call.Args {
			if ident, ok := arg.(*ast.Ident); ok && ident.Name == txVar {
				argIdx = i
				break
			}
		}
		if argIdx < 0 || calleeName == "" {
			return true
		}
		for _, callee := range funcs[calleeName] {
			if callee.Body == nil {
				continue
			}
			paramName := flattenedParamName(callee.Type.Params, argIdx)
			if paramName == "" {
				continue
			}
			key := calleeName + "#" + paramName
			if visited[key] {
				continue
			}
			visited[key] = true
			walkForTxWrites(callee.Body, paramName, funcs, writeMethods, fset, visited, out)
		}
		return true
	})
}

// flattenedParamName returns the name of the parameter at flattened index
// idx in fields, or "" if idx is out of range or that parameter is
// unnamed.
func flattenedParamName(fields *ast.FieldList, idx int) string {
	if fields == nil {
		return ""
	}
	pos := 0
	for _, field := range fields.List {
		n := len(field.Names)
		if n == 0 {
			n = 1
		}
		for i := 0; i < n; i++ {
			if pos == idx {
				if len(field.Names) > 0 {
					return field.Names[i].Name
				}
				return ""
			}
			pos++
		}
	}
	return ""
}

// checkDecideClosuresWriteBoundary is the shared body behind the real
// enforcement test and its synthetic positive control below: given a
// package's parsed functions and the store package's write-method set, it
// finds every decide closure and returns every violation the walk found.
func checkDecideClosuresWriteBoundary(t *testing.T, fset *token.FileSet, funcs map[string][]*ast.FuncDecl, writeMethods map[string]bool) []txWriteBoundaryViolation {
	t.Helper()
	closures := findDecideClosures(t, funcs)
	if len(closures) == 0 {
		t.Fatal("findDecideClosures found zero decide closures; a vacuous search would make this test pass " +
			"by finding nothing to check, which is not the same as the rule holding")
	}
	var violations []txWriteBoundaryViolation
	for _, lit := range closures {
		txVar := funcTxParamName(lit.Type.Params)
		visited := map[string]bool{}
		walkForTxWrites(lit.Body, txVar, funcs, writeMethods, fset, visited, &violations)
	}
	return violations
}

// TestDecideClosuresNeverCallTxWriteMethodsDirectly is the real
// enforcement: parses this package's own source (read-only) and the store
// package's source, and asserts the walk above finds zero decide closures
// that call a *store.Tx write method directly.
func TestDecideClosuresNeverCallTxWriteMethodsDirectly(t *testing.T) {
	fset, funcs := parseGoPackage(t, ".")
	writeMethods := txWriteMethodSet(t, "../store")

	violations := checkDecideClosuresWriteBoundary(t, fset, funcs, writeMethods)
	if len(violations) != 0 {
		var b strings.Builder
		for _, v := range violations {
			fmt.Fprintf(&b, "\n  %s: %s", v.pos, v.desc)
		}
		t.Fatalf("a night-command decide function writes to its transaction directly, which is nightRunExempt's/"+
			"nightRunGated's job alone (see this file's own doc comment):%s", b.String())
	}
}

// syntheticDecideWriteSource is a minimal, self-contained decide/runner
// pair, structurally identical in shape to nightsessioncontrol.go's real
// one, with ONE deliberate defect: its decide closure calls a write
// method on tx directly. It is parsed from this string literal only,
// never written to disk and never touching internal/coordinator/api/
// night* or emergencystop.go.
const syntheticDecideWriteSource = `package api

import "context"

type fixtureOutcome struct{}

func (h *fixtureHandlers) fixtureRunGated(
	ctx context.Context,
	decide func(ctx context.Context, tx *store.Tx, current *store.FixtureRecord) (fixtureOutcome, error),
) (fixtureOutcome, error) {
	return h.deps.Fixture.InTx(ctx, func(ctx context.Context, tx *store.Tx) (fixtureOutcome, error) {
		return decide(ctx, tx, nil)
	})
}

func (h *fixtureHandlers) fixtureCommand(ctx context.Context) (fixtureOutcome, error) {
	return h.fixtureRunGated(ctx, func(ctx context.Context, tx *store.Tx, current *store.FixtureRecord) (fixtureOutcome, error) {
		if _, err := tx.CreateFixtureRow(ctx, store.FixtureRecord{}); err != nil {
			return fixtureOutcome{}, err
		}
		return fixtureOutcome{}, nil
	})
}
`

// syntheticDecideNoWriteSource is the same shape, minus the defect: the
// decide closure only reads through tx and describes the write as data
// instead, matching the real fix's own pattern.
const syntheticDecideNoWriteSource = `package api

import "context"

type fixtureOutcome struct{ shouldCreate bool }

func (h *fixtureHandlers) fixtureRunGated(
	ctx context.Context,
	decide func(ctx context.Context, tx *store.Tx, current *store.FixtureRecord) (fixtureOutcome, error),
) (fixtureOutcome, error) {
	return h.deps.Fixture.InTx(ctx, func(ctx context.Context, tx *store.Tx) (fixtureOutcome, error) {
		out, err := decide(ctx, tx, nil)
		if err != nil {
			return out, err
		}
		if out.shouldCreate {
			if _, err := tx.CreateFixtureRow(ctx, store.FixtureRecord{}); err != nil {
				return fixtureOutcome{}, err
			}
		}
		return out, nil
	})
}

func (h *fixtureHandlers) fixtureCommand(ctx context.Context) (fixtureOutcome, error) {
	return h.fixtureRunGated(ctx, func(ctx context.Context, tx *store.Tx, current *store.FixtureRecord) (fixtureOutcome, error) {
		if _, err := tx.GetFixtureRow(ctx, "id"); err != nil {
			return fixtureOutcome{}, err
		}
		return fixtureOutcome{shouldCreate: true}, nil
	})
}
`

// syntheticRunnerNames lets the positive/negative control tests reuse
// findDecideClosures/checkDecideClosuresWriteBoundary against the
// fixture's own runner name instead of the real nightRunExempt/
// nightRunGated, by temporarily standing in as this test's "the two
// runners", see [findDecideClosuresNamed].
func findDecideClosuresNamed(t *testing.T, funcs map[string][]*ast.FuncDecl, runnerNames ...string) []*ast.FuncLit {
	t.Helper()
	paramIdx := map[string]int{}
	for _, runner := range runnerNames {
		decls, ok := funcs[runner]
		if !ok || len(decls) != 1 {
			t.Fatalf("fixture setup error: expected exactly one declaration of %s, found %d", runner, len(decls))
		}
		idx := decideParamIndex(decls[0])
		if idx < 0 {
			t.Fatalf("fixture setup error: %s has no parameter named %q", runner, "decide")
		}
		paramIdx[runner] = idx
	}
	var closures []*ast.FuncLit
	for _, decls := range funcs {
		for _, fd := range decls {
			if fd.Body == nil {
				continue
			}
			ast.Inspect(fd.Body, func(n ast.Node) bool {
				call, ok := n.(*ast.CallExpr)
				if !ok {
					return true
				}
				sel, ok := call.Fun.(*ast.SelectorExpr)
				if !ok {
					return true
				}
				idx, known := paramIdx[sel.Sel.Name]
				if !known || idx >= len(call.Args) {
					return true
				}
				if lit, ok := call.Args[idx].(*ast.FuncLit); ok {
					closures = append(closures, lit)
				}
				return true
			})
		}
	}
	return closures
}

// fixtureWriteMethods is the synthetic sources' own tiny write-method
// set, standing in for the real txWriteMethodSet(t, "../store") result:
// the fixtures above never import the real store package, so this names
// exactly the one write method (CreateFixtureRow) and one read method
// (GetFixtureRow) the fixtures use.
var fixtureWriteMethods = map[string]bool{"CreateFixtureRow": true}

// TestTxWriteBoundaryWalkerCatchesADirectWriteOnASyntheticFixture is this
// walker's positive control (mutation evidence's own required
// "claims of absence need a positive control" rule, applied to
// [TestDecideClosuresNeverCallTxWriteMethodsDirectly]'s zero-violations
// assertion): proves the walk actually flags a decide function that
// writes to tx directly, using [syntheticDecideWriteSource], a fixture
// with the exact defect this whole file exists to catch, never real
// night code.
func TestTxWriteBoundaryWalkerCatchesADirectWriteOnASyntheticFixture(t *testing.T) {
	fset := token.NewFileSet()
	file, err := parser.ParseFile(fset, "fixture.go", syntheticDecideWriteSource, 0)
	if err != nil {
		t.Fatalf("parse synthetic fixture source: %v", err)
	}
	funcs := map[string][]*ast.FuncDecl{}
	for _, decl := range file.Decls {
		if fd, ok := decl.(*ast.FuncDecl); ok {
			funcs[fd.Name.Name] = append(funcs[fd.Name.Name], fd)
		}
	}

	closures := findDecideClosuresNamed(t, funcs, "fixtureRunGated")
	if len(closures) != 1 {
		t.Fatalf("findDecideClosuresNamed(fixtureRunGated) = %d closures, want 1", len(closures))
	}
	var violations []txWriteBoundaryViolation
	for _, lit := range closures {
		txVar := funcTxParamName(lit.Type.Params)
		visited := map[string]bool{}
		walkForTxWrites(lit.Body, txVar, funcs, fixtureWriteMethods, fset, visited, &violations)
	}
	if len(violations) != 1 {
		t.Fatalf("violations = %+v, want exactly 1 (the fixture's own tx.CreateFixtureRow call); "+
			"a walker that found none here would also find none in real night code for the wrong reason", violations)
	}
	if !strings.Contains(violations[0].desc, "CreateFixtureRow") {
		t.Errorf("violation desc = %q, want it to name CreateFixtureRow", violations[0].desc)
	}
}

// TestTxWriteBoundaryWalkerAllowsAReadOnlyDecideOnASyntheticFixture is the
// negative control alongside the positive one above: the same walker,
// against [syntheticDecideNoWriteSource] (the fixed shape: decide only
// reads through tx and reports the write as data), must find zero
// violations, proving the walker does not simply flag every call on tx,
// only write ones.
func TestTxWriteBoundaryWalkerAllowsAReadOnlyDecideOnASyntheticFixture(t *testing.T) {
	fset := token.NewFileSet()
	file, err := parser.ParseFile(fset, "fixture.go", syntheticDecideNoWriteSource, 0)
	if err != nil {
		t.Fatalf("parse synthetic fixture source: %v", err)
	}
	funcs := map[string][]*ast.FuncDecl{}
	for _, decl := range file.Decls {
		if fd, ok := decl.(*ast.FuncDecl); ok {
			funcs[fd.Name.Name] = append(funcs[fd.Name.Name], fd)
		}
	}

	closures := findDecideClosuresNamed(t, funcs, "fixtureRunGated")
	if len(closures) != 1 {
		t.Fatalf("findDecideClosuresNamed(fixtureRunGated) = %d closures, want 1", len(closures))
	}
	var violations []txWriteBoundaryViolation
	for _, lit := range closures {
		txVar := funcTxParamName(lit.Type.Params)
		visited := map[string]bool{}
		walkForTxWrites(lit.Body, txVar, funcs, fixtureWriteMethods, fset, visited, &violations)
	}
	if len(violations) != 0 {
		t.Fatalf("violations = %+v, want none: this fixture's decide function only reads through tx", violations)
	}
}
