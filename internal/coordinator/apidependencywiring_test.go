package coordinator

import (
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"testing"
)

// unwiredDependencyExemptions is the explicit, reasoned exception list for
// TestEveryRefusingDependencyIsWired: a field this test's derivation (see
// that test's own doc comment) finds to have a nil-safe default whose
// write path refuses with a "not wired in"/"not configured" style internal
// error, but which internal/coordinator/coordinator.go deliberately (or,
// for the "not yet investigated" entries below, not yet knowingly) leaves
// unassigned in its api.Dependencies wiring.
//
// Every entry needs a stated reason, exactly like
// cmd/showmeshctl/writeparity_test.go's exemptWritePaths.
var unwiredDependencyExemptions = map[string]string{}

// dependencyRefusalVarPattern matches this package's naming convention for
// a sentinel "dependency not wired in" error: a package-level
// errors.New/fmt.Errorf-initialized var in internal/coordinator/api whose
// name ends in "NotConfigured" (errCommandStoreNotConfigured,
// errRenderPublisherNotConfigured, errIdentityNotConfigured, and so on;
// see api.go, audiodispatch.go, auth.go, mqttactiondispatch.go,
// resolumeaction.go, resolumerecovery_interfaces.go). Every one of these
// vars this test has checked by hand is returned unconditionally from a
// no-op default type's write method, never from any other code path.
var dependencyRefusalVarPattern = regexp.MustCompile(`(?i)^err[A-Za-z0-9]*NotConfigured$`)

// dependencyRefusalInlinePattern is the second, narrower shape a few no-op
// defaults use instead of a named sentinel var: an inline
// errors.New(...)/fmt.Errorf(...) call whose message contains "wired",
// for example noFPPObservationStore's "api: fpp observation store not wired in"
// and noFPPMQTTSecretStore's "api: no fpp.mqtt secret store wired in".
var dependencyRefusalInlinePattern = regexp.MustCompile(`(?i)wired`)

// apiPackageDir is internal/coordinator/api, relative to this package's
// own directory (internal/coordinator): this test's working directory.
const apiPackageDir = "api"

// collectAPINonTestFiles parses every non-test .go file directly under
// apiPackageDir (no recursion: the api package has no subpackages that
// matter here) and returns their ASTs.
func collectAPINonTestFiles(t *testing.T) (*token.FileSet, []*ast.File) {
	t.Helper()

	entries, err := os.ReadDir(apiPackageDir)
	if err != nil {
		t.Fatalf("reading %s: %v", apiPackageDir, err)
	}

	fset := token.NewFileSet()
	var files []*ast.File
	for _, entry := range entries {
		name := entry.Name()
		if entry.IsDir() || !strings.HasSuffix(name, ".go") || strings.HasSuffix(name, "_test.go") {
			continue
		}
		f, err := parser.ParseFile(fset, filepath.Join(apiPackageDir, name), nil, 0)
		if err != nil {
			t.Fatalf("parsing %s: %v", name, err)
		}
		files = append(files, f)
	}
	return fset, files
}

// stringLitValue returns the string a *ast.BasicLit string literal holds,
// or "" and false if expr is not one.
func stringLitValue(expr ast.Expr) (string, bool) {
	lit, ok := expr.(*ast.BasicLit)
	if !ok || lit.Kind != token.STRING {
		return "", false
	}
	s, err := strconv.Unquote(lit.Value)
	if err != nil {
		return "", false
	}
	return s, true
}

// isErrorConstructorCall reports whether call is errors.New(...) or
// fmt.Errorf(...), and returns its first (message) argument's string
// value if that argument is a plain string literal.
func isErrorConstructorCall(call *ast.CallExpr) (string, bool) {
	sel, ok := call.Fun.(*ast.SelectorExpr)
	if !ok {
		return "", false
	}
	pkg, ok := sel.X.(*ast.Ident)
	if !ok {
		return "", false
	}
	isErrorsNew := pkg.Name == "errors" && sel.Sel.Name == "New"
	isFmtErrorf := pkg.Name == "fmt" && sel.Sel.Name == "Errorf"
	if !isErrorsNew && !isFmtErrorf {
		return "", false
	}
	if len(call.Args) == 0 {
		return "", true
	}
	msg, _ := stringLitValue(call.Args[0])
	return msg, true
}

// collectRefusalVarNames returns the set of package-level var names across
// files matching dependencyRefusalVarPattern whose initializer is an
// errors.New/fmt.Errorf call: the api package's "dependency not wired in"
// sentinel naming convention.
func collectRefusalVarNames(files []*ast.File) map[string]bool {
	refusal := map[string]bool{}
	for _, f := range files {
		for _, decl := range f.Decls {
			gd, ok := decl.(*ast.GenDecl)
			if !ok || gd.Tok != token.VAR {
				continue
			}
			for _, spec := range gd.Specs {
				vs, ok := spec.(*ast.ValueSpec)
				if !ok || len(vs.Names) != 1 || len(vs.Values) != 1 {
					continue
				}
				call, ok := vs.Values[0].(*ast.CallExpr)
				if !ok {
					continue
				}
				if _, isCtor := isErrorConstructorCall(call); !isCtor {
					continue
				}
				name := vs.Names[0].Name
				if dependencyRefusalVarPattern.MatchString(name) {
					refusal[name] = true
				}
			}
		}
	}
	return refusal
}

// exprIsRefusal reports whether expr is a use of a known refusal sentinel
// (an Ident in refusalVarNames) or an inline errors.New/fmt.Errorf call
// whose message contains "wired".
func exprIsRefusal(expr ast.Expr, refusalVarNames map[string]bool) bool {
	switch e := expr.(type) {
	case *ast.Ident:
		return refusalVarNames[e.Name]
	case *ast.CallExpr:
		msg, isCtor := isErrorConstructorCall(e)
		return isCtor && dependencyRefusalInlinePattern.MatchString(msg)
	default:
		return false
	}
}

// methodRefuses reports whether fn (a method on a no-op default type)
// contains at least one return statement returning a refusal error
// (exprIsRefusal), which this test treats as "this method's write path
// refuses with a not-wired-in style internal error": the shape every
// no-op default's write method in the api package uses (see this test's
// own doc comment for the two concrete forms).
func methodRefuses(fn *ast.FuncDecl, refusalVarNames map[string]bool) bool {
	if fn.Body == nil {
		return false
	}
	found := false
	ast.Inspect(fn.Body, func(n ast.Node) bool {
		ret, ok := n.(*ast.ReturnStmt)
		if !ok {
			return true
		}
		for _, r := range ret.Results {
			if exprIsRefusal(r, refusalVarNames) {
				found = true
			}
		}
		return true
	})
	return found
}

// collectRefusingNoOpTypes returns the set of no-op default type names
// (e.g. "noCommandStore") that declare at least one method whose write
// path refuses with a refusal error.
func collectRefusingNoOpTypes(files []*ast.File, refusalVarNames map[string]bool) map[string]bool {
	refusing := map[string]bool{}
	for _, f := range files {
		for _, decl := range f.Decls {
			fn, ok := decl.(*ast.FuncDecl)
			if !ok || fn.Recv == nil || len(fn.Recv.List) != 1 {
				continue
			}
			recvType := fn.Recv.List[0].Type
			// Receivers here are always plain value types (no-op defaults
			// are all `struct{}`), never pointers.
			ident, ok := recvType.(*ast.Ident)
			if !ok || !strings.HasPrefix(ident.Name, "no") {
				continue
			}
			if methodRefuses(fn, refusalVarNames) {
				refusing[ident.Name] = true
			}
		}
	}
	return refusing
}

// collectWithDefaultsFieldTypes parses Dependencies.withDefaults
// (api.go) and returns, for every `if d.<Field> == nil { d.<Field> =
// no<Type>{} }` block, Field -> no<Type>. This is deliberately derived
// from withDefaults's own source rather than hand-copied from the
// Dependencies struct, so a newly added nil-safe field is picked up
// automatically the next time this test runs.
func collectWithDefaultsFieldTypes(t *testing.T, files []*ast.File) map[string]string {
	t.Helper()

	var withDefaults *ast.FuncDecl
	for _, f := range files {
		for _, decl := range f.Decls {
			fn, ok := decl.(*ast.FuncDecl)
			if !ok || fn.Name.Name != "withDefaults" || fn.Recv == nil || len(fn.Recv.List) != 1 {
				continue
			}
			// api.go also declares an unrelated Options.withDefaults,
			// match on receiver type name so that one is never picked up
			// instead of Dependencies.withDefaults.
			recvIdent, ok := fn.Recv.List[0].Type.(*ast.Ident)
			if !ok || recvIdent.Name != "Dependencies" {
				continue
			}
			withDefaults = fn
		}
	}
	if withDefaults == nil {
		t.Fatal("found no Dependencies.withDefaults method in internal/coordinator/api: " +
			"this test's derivation is broken, or that method was renamed/removed")
	}

	fieldTypes := map[string]string{}
	ast.Inspect(withDefaults.Body, func(n ast.Node) bool {
		ifStmt, ok := n.(*ast.IfStmt)
		if !ok {
			return true
		}
		// Condition shape: d.<Field> == nil
		cond, ok := ifStmt.Cond.(*ast.BinaryExpr)
		if !ok || cond.Op != token.EQL {
			return true
		}
		sel, ok := cond.X.(*ast.SelectorExpr)
		if !ok {
			return true
		}
		if _, isNil := cond.Y.(*ast.Ident); !isNil || cond.Y.(*ast.Ident).Name != "nil" {
			return true
		}
		field := sel.Sel.Name

		// Body shape: exactly `d.<Field> = no<Type>{}`
		if len(ifStmt.Body.List) != 1 {
			return true
		}
		assign, ok := ifStmt.Body.List[0].(*ast.AssignStmt)
		if !ok || assign.Tok != token.ASSIGN || len(assign.Lhs) != 1 || len(assign.Rhs) != 1 {
			return true
		}
		composite, ok := assign.Rhs[0].(*ast.CompositeLit)
		if !ok {
			return true
		}
		typeIdent, ok := composite.Type.(*ast.Ident)
		if !ok {
			return true
		}
		fieldTypes[field] = typeIdent.Name
		return true
	})
	return fieldTypes
}

// dependenciesCompositeLitVarName finds the local variable name
// coordinator.go's Run function assigns an api.Dependencies{...} composite
// literal to (currently "apiDeps"), returning that name plus the set of
// field names assigned directly inside the literal.
func dependenciesCompositeLitVarName(f *ast.File) (string, map[string]bool) {
	varName := ""
	assigned := map[string]bool{}
	ast.Inspect(f, func(n ast.Node) bool {
		assign, ok := n.(*ast.AssignStmt)
		if !ok || len(assign.Lhs) != 1 || len(assign.Rhs) != 1 {
			return true
		}
		composite, ok := assign.Rhs[0].(*ast.CompositeLit)
		if !ok {
			return true
		}
		sel, ok := composite.Type.(*ast.SelectorExpr)
		if !ok || sel.Sel.Name != "Dependencies" {
			return true
		}
		pkgIdent, ok := sel.X.(*ast.Ident)
		if !ok || pkgIdent.Name != "api" {
			return true
		}
		lhsIdent, ok := assign.Lhs[0].(*ast.Ident)
		if !ok {
			return true
		}
		varName = lhsIdent.Name
		for _, elt := range composite.Elts {
			kv, ok := elt.(*ast.KeyValueExpr)
			if !ok {
				continue
			}
			keyIdent, ok := kv.Key.(*ast.Ident)
			if !ok {
				continue
			}
			assigned[keyIdent.Name] = true
		}
		return true
	})
	return varName, assigned
}

// collectPostLiteralFieldAssignments finds every `<varName>.<Field> = ...`
// assignment anywhere in f: coordinator.go's wiring for a field (like
// Macros) that cannot be known at the point the api.Dependencies{...}
// literal is built, and so is filled in afterward.
func collectPostLiteralFieldAssignments(f *ast.File, varName string) map[string]bool {
	assigned := map[string]bool{}
	if varName == "" {
		return assigned
	}
	ast.Inspect(f, func(n ast.Node) bool {
		assign, ok := n.(*ast.AssignStmt)
		if !ok {
			return true
		}
		for _, lhs := range assign.Lhs {
			sel, ok := lhs.(*ast.SelectorExpr)
			if !ok {
				continue
			}
			ident, ok := sel.X.(*ast.Ident)
			if !ok || ident.Name != varName {
				continue
			}
			assigned[sel.Sel.Name] = true
		}
		return true
	})
	return assigned
}

// TestEveryRefusingDependencyIsWired guards against a gap this codebase
// actually shipped: FPPObservations had a complete, contract-frozen POST
// route at /integrations/fpp/playlist-entry-observations behind a
// Dependencies field internal/coordinator/coordinator.go never assigned,
// so every real coordinator answered a 500 naming the missing wiring
// while every test passed, because each test built its own
// api.Dependencies by hand and set the field it cared about.
//
// It derives, from internal/coordinator/api's own source (not a
// hand-maintained list), every Dependencies field whose nil-safe default
// (Dependencies.withDefaults) is a no-op type with at least one write
// method that unconditionally refuses with a "not wired in"/"not
// configured" style internal error (see dependencyRefusalVarPattern and
// dependencyRefusalInlinePattern above for the two message shapes this
// package actually uses). It then checks that internal/coordinator/
// coordinator.go's own api.Dependencies{...} literal: or a subsequent
// apiDeps.<Field> = ... assignment in the same file, the shape
// Dependencies.Macros needs because the macro executor does not exist yet
// at the point the literal is built: assigns every one of them, unless
// the field is named in unwiredDependencyExemptions with a stated reason.
//
// Derivation confidence and what would make it drift silently: this
// depends on internal/coordinator/api continuing to name every "dependency
// not wired in" sentinel error with the "...NotConfigured" suffix (or, for
// the two inline exceptions, a message containing "wired"), which is
// checked by hand today (see this test's own doc comment above) but not
// enforced by any other test. A future no-op default that refuses through
// a differently-named or differently-worded error would silently NOT be
// added to the derived list, and so could ship unwired exactly like
// FPPObservations did, without this test catching it. The
// withDefaults-parsing half (which field maps to which no-op type) has no
// such gap: it reads every nil-check in that method directly, so it cannot
// miss a field withDefaults itself defaults.
func TestEveryRefusingDependencyIsWired(t *testing.T) {
	apiFset, apiFiles := collectAPINonTestFiles(t)
	_ = apiFset

	refusalVarNames := collectRefusalVarNames(apiFiles)
	if len(refusalVarNames) < 5 {
		t.Fatalf("found only %d NotConfigured-style refusal sentinels in internal/coordinator/api: "+
			"the AST scan is almost certainly broken", len(refusalVarNames))
	}

	refusingTypes := collectRefusingNoOpTypes(apiFiles, refusalVarNames)
	if len(refusingTypes) == 0 {
		t.Fatal("found zero refusing no-op default types in internal/coordinator/api: the AST scan is " +
			"almost certainly broken, not that this package has none")
	}

	fieldTypes := collectWithDefaultsFieldTypes(t, apiFiles)
	if len(fieldTypes) < 20 {
		t.Fatalf("Dependencies.withDefaults only nil-defaults %d fields (expected at least 20): "+
			"the AST scan is almost certainly broken", len(fieldTypes))
	}

	var refusingFields []string
	for field, typ := range fieldTypes {
		if refusingTypes[typ] {
			refusingFields = append(refusingFields, field)
		}
	}
	if len(refusingFields) == 0 {
		t.Fatal("found zero Dependencies fields whose nil-safe default refuses writes: the AST scan is " +
			"almost certainly broken, not that this struct has none")
	}

	coordinatorFset := token.NewFileSet()
	coordFile, err := parser.ParseFile(coordinatorFset, "coordinator.go", nil, 0)
	if err != nil {
		t.Fatalf("parsing coordinator.go: %v", err)
	}

	varName, literalAssigned := dependenciesCompositeLitVarName(coordFile)
	if varName == "" {
		t.Fatal("found no api.Dependencies{...} composite literal in coordinator.go: the AST scan is " +
			"almost certainly broken, or the coordinator's own API wiring moved to another file")
	}
	postAssigned := collectPostLiteralFieldAssignments(coordFile, varName)

	for name, reason := range unwiredDependencyExemptions {
		if reason == "" {
			t.Errorf("unwiredDependencyExemptions[%q] has no stated reason", name)
		}
	}

	for _, field := range refusingFields {
		if literalAssigned[field] || postAssigned[field] {
			continue
		}
		if reason, exempt := unwiredDependencyExemptions[field]; exempt {
			if reason == "" {
				t.Errorf("Dependencies.%s is on unwiredDependencyExemptions with no stated reason", field)
			}
			continue
		}
		t.Errorf("Dependencies.%s has a nil-safe default whose write path refuses with an internal "+
			"error naming the missing wiring, but coordinator.go's api.Dependencies{...} never assigns "+
			"it (neither in the literal nor in a later %s.%s = ... assignment): the route(s) backed by "+
			"this field will answer an internal error on a real coordinator, not merely in a hand-built "+
			"test Dependencies value. Either wire it in coordinator.go, or add a reasoned entry to "+
			"unwiredDependencyExemptions in apidependencywiring_test.go.", field, varName, field)
	}
}
