package main

import (
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"gopkg.in/yaml.v3"
)

// exemptWritePaths is the explicit, reasoned exception list ADR-039
// decision 9 and CLAUDE.md's CLI-parity constraint both call for: a
// non-GET api/openapi.yaml path this program deliberately never calls,
// with a stated reason. Every entry here is an existing, already-decided
// architectural boundary, not a new judgment call — see cmd_session.go's
// own doc comment ("this CLI is bearer-only").
//
// Path keys match api/openapi.yaml verbatim (no /api/v1 prefix, {param}
// placeholders as written there).
var exemptWritePaths = map[string]string{
	"/bootstrap": "mints a browser session cookie (ADR-024's session credential form); this CLI is " +
		"bearer-only (cmd_session.go's own doc comment) and never holds or presents a cookie. The " +
		"equivalent break-glass path for a coordinator with no reachable admin is the coordinator " +
		"BINARY's own claim-bootstrap subcommand (ADR-024 decision 9), a different program from this one.",
	"/session": "POST mints and DELETE destroys a browser session cookie; this CLI is bearer-only and " +
		"never holds one. GET /session (whoami against whatever --token resolves to) IS covered — see " +
		"cmd_session.go — only the cookie-minting/destroying half is exempt.",
	"/integrations/fpp/playlist-entry-observations": "POST is evidence ingestion from the installed FPP " +
		"plugin, not an operator capability: the body is machine-derived identity (a canonical playlist " +
		"hash and a derived entry key over a definition only the plugin holds) plus a per-instance " +
		"monotonic sequence the coordinator refuses to let go backwards. A hand-typed observation is " +
		"either refused or a forged claim about what FPP played, so a verb for it would be a way to lie " +
		"to the coordinator rather than a way to operate it, which is also why fpp:observe is granted to " +
		"scheduler and admin and deliberately not to operator (FPP-PLUGIN-COORDINATOR-CONTRACTS.md 1.1). " +
		"The READ half, GET on the same path, is an ordinary read this CLI should grow a verb for when " +
		"Track H gives an operator a reason to look at it.",
	"/integrations/fpp/playlist-definitions": "POST is the installed FPP plugin's own evidence-publication " +
		"route (FPP-PLUGIN-COORDINATOR-CONTRACTS.md 3.2), not an operator capability: the body is a complete " +
		"playlist definition the coordinator only ever accepts after re-canonicalizing it and confirming its " +
		"SHA-256 equals the caller's own declared playlistHash. A hand-typed definition is either refused by " +
		"that hash check or a forged claim about what is on the FPP host, the identical reasoning the " +
		"playlist-entry-observations POST exemption above states for its own sibling route, and POST shares " +
		"fpp:observe with that route for the same reason. The READ half — GET on this path and on " +
		"/integrations/fpp/playlist-definitions/{instanceUuid}/{playlistHash} — IS covered: " +
		"cmd_fpp_playlist_definition.go (showmeshctl fpp playlist-definitions list|get). TRACK-H-H2-SPEC.md " +
		"section 7 is the stated reason to grow it, exactly as this file's own comment anticipated.",
}

// pathSegment is one "/"-delimited piece of a URL path as this test sees
// it: either fixed text, or a variable this test cannot resolve further
// (an openapi {param} placeholder, or CLI text built from something other
// than a string literal — a function argument, a loop variable, etc).
type pathSegment struct {
	text     string
	variable bool
}

func splitPathSegments(p string) []string {
	trimmed := strings.Trim(p, "/")
	if trimmed == "" {
		return nil
	}
	return strings.Split(trimmed, "/")
}

// openAPISegments converts one api/openapi.yaml path into pathSegments,
// treating "{...}" as a variable segment. api/openapi.yaml's path keys
// omit the version prefix every actual request (and every literal in
// this program's own source) carries, so "v1" is prepended before
// splitting — see api/openapi.yaml's own servers block.
func openAPISegments(p string) []pathSegment {
	segs := []pathSegment{{text: "api"}, {text: "v1"}}
	for _, s := range splitPathSegments(p) {
		segs = append(segs, pathSegment{text: s, variable: strings.HasPrefix(s, "{") && strings.HasSuffix(s, "}")})
	}
	return segs
}

// segmentsCompatible reports whether a CLI-derived path shape addresses
// an openapi path: same segment count, an openapi {param} accepts any CLI
// segment, and an openapi LITERAL segment must be matched by a CLI
// literal with the same text. An unresolved CLI segment (a runtime
// expression) never matches an openapi literal: a CLI call that builds a
// literal position dynamically (cmd_principal.go's enable/disable verb)
// must instead be declared in dynamicWritePathCoverage, so it cannot
// silently "cover" every same-shaped endpoint the API grows later.
func segmentsCompatible(cli, api []pathSegment) bool {
	if len(cli) != len(api) {
		return false
	}
	for i := range cli {
		if api[i].variable {
			continue
		}
		if cli[i].variable || cli[i].text != api[i].text {
			return false
		}
	}
	return true
}

// dynamicWritePathCoverage declares, per concrete operation
// ("METHOD /openapi/path" exactly as api/openapi.yaml writes the path),
// which openapi operations the package's dynamically built CLI paths
// cover. Every entry names its call site. An entry whose operation no
// longer exists in api/openapi.yaml fails the test, so this map cannot
// go stale in either direction.
var dynamicWritePathCoverage = map[string]string{
	"POST /principals/{id}/enable": "cmd_principal.go cmdPrincipalSetDisabled builds " +
		"/api/v1/principals/<id>/<verb> with verb=enable (showmeshctl principal enable)",
	"POST /principals/{id}/disable": "cmd_principal.go cmdPrincipalSetDisabled builds " +
		"/api/v1/principals/<id>/<verb> with verb=disable (showmeshctl principal disable)",
	"POST /nodes/{nodeId}/render/surfaces/{surfaceId}/apply": "cmd_render_command.go dispatchRenderCommand builds " +
		"/api/v1/nodes/<nodeId>/render/surfaces/<surfaceId>/<verb> with verb=apply (showmeshctl render apply)",
	"POST /nodes/{nodeId}/render/surfaces/{surfaceId}/clear": "cmd_render_command.go dispatchRenderCommand builds " +
		"/api/v1/nodes/<nodeId>/render/surfaces/<surfaceId>/<verb> with verb=clear (showmeshctl render clear)",
	"POST /nodes/{nodeId}/render/surfaces/{surfaceId}/restart": "cmd_render_command.go dispatchRenderCommand builds " +
		"/api/v1/nodes/<nodeId>/render/surfaces/<surfaceId>/<verb> with verb=restart (showmeshctl render restart)",
	"POST /nodes/{nodeId}/render/surfaces/{surfaceId}/transport-probe": "cmd_render_command.go dispatchRenderCommand " +
		"builds /api/v1/nodes/<nodeId>/render/surfaces/<surfaceId>/<verb> with verb=transport-probe " +
		"(showmeshctl render probe)",
	"POST /night/commands/{command}": "cmd_night_lifecycle.go nightLifecycleCommand builds " +
		"/api/v1/night/commands/<command> with command a literal passed through " +
		"runSimpleNightLifecycleCommand from each of the eight cmdNight* wrappers in the same file " +
		"(showmeshctl night prepare-site|readiness|preshow|start|final-show|fade-out|power-down)",
	"POST /nodes/{nodeId}/audio/sessions/{sessionId}/apply": "cmd_audio_session.go cmdAudioSessionDispatch builds " +
		"/api/v1/nodes/<nodeId>/audio/sessions/<sessionId>/<op> with op=apply (showmeshctl audio session apply)",
	"POST /nodes/{nodeId}/audio/sessions/{sessionId}/prepare": "cmd_audio_session.go cmdAudioSessionDispatch builds " +
		"/api/v1/nodes/<nodeId>/audio/sessions/<sessionId>/<op> with op=prepare (showmeshctl audio session prepare)",
	"POST /nodes/{nodeId}/audio/sessions/{sessionId}/start": "cmd_audio_session.go cmdAudioSessionDispatch builds " +
		"/api/v1/nodes/<nodeId>/audio/sessions/<sessionId>/<op> with op=start (showmeshctl audio session start)",
	"POST /nodes/{nodeId}/audio/sessions/{sessionId}/pause": "cmd_audio_session.go cmdAudioSessionDispatch builds " +
		"/api/v1/nodes/<nodeId>/audio/sessions/<sessionId>/<op> with op=pause (showmeshctl audio session pause)",
	"POST /nodes/{nodeId}/audio/sessions/{sessionId}/resume": "cmd_audio_session.go cmdAudioSessionDispatch builds " +
		"/api/v1/nodes/<nodeId>/audio/sessions/<sessionId>/<op> with op=resume (showmeshctl audio session resume)",
	"POST /nodes/{nodeId}/audio/sessions/{sessionId}/seek": "cmd_audio_session.go cmdAudioSessionDispatch builds " +
		"/api/v1/nodes/<nodeId>/audio/sessions/<sessionId>/<op> with op=seek (showmeshctl audio session seek)",
	"POST /nodes/{nodeId}/audio/sessions/{sessionId}/advance": "cmd_audio_session.go cmdAudioSessionDispatch builds " +
		"/api/v1/nodes/<nodeId>/audio/sessions/<sessionId>/<op> with op=advance (showmeshctl audio session advance)",
	"POST /nodes/{nodeId}/audio/sessions/{sessionId}/stop": "cmd_audio_session.go cmdAudioSessionDispatch builds " +
		"/api/v1/nodes/<nodeId>/audio/sessions/<sessionId>/<op> with op=stop (showmeshctl audio session stop)",
	"POST /nodes/{nodeId}/audio/sessions/{sessionId}/clear": "cmd_audio_session.go cmdAudioSessionDispatch builds " +
		"/api/v1/nodes/<nodeId>/audio/sessions/<sessionId>/<op> with op=clear (showmeshctl audio session clear)",
	"POST /nodes/{nodeId}/audio/sessions/{sessionId}/gain": "cmd_audio_gain.go cmdAudioGain calls " +
		"cmdAudioSessionLikeDispatch with pathSuffix=gain (showmeshctl audio gain set)",
	"POST /nodes/{nodeId}/audio/sessions/{sessionId}/gain/fade": "cmd_audio_gain.go cmdAudioGain calls " +
		"cmdAudioSessionLikeDispatch with pathSuffix=gain/fade (showmeshctl audio gain fade)",
	"POST /nodes/{nodeId}/audio/sessions/{sessionId}/output/mute": "cmd_audio_gain.go cmdAudioOutput calls " +
		"cmdAudioSessionLikeDispatch with pathSuffix=output/mute (showmeshctl audio output mute)",
	"POST /nodes/{nodeId}/audio/sessions/{sessionId}/output/unmute": "cmd_audio_gain.go cmdAudioOutput calls " +
		"cmdAudioSessionLikeDispatch with pathSuffix=output/unmute (showmeshctl audio output unmute)",
}

// unresolved marks a CLI path fragment this test could not reduce to a
// string literal (a call, an identifier it could not trace to a literal,
// etc). It is distinct from an ordinary "\x00"-free literal string only
// in that it always renders as a single variable segment, however long
// the underlying expression actually was.
const unresolved = "\x00"

// flattenPathExpr renders expr as a string, standing in "\x00" for any
// piece that is not itself a string literal. resolve maps a local
// identifier (a `const apiPath = "..."` or `apiPath := "..."` seen
// earlier in the SAME FILE) to its assigned expression, resolved one
// level at a time up to a small depth bound as a safety valve against a
// pathological chain; every real case in this package resolves in one
// hop.
func flattenPathExpr(expr ast.Expr, resolve map[string]ast.Expr, depth int) string {
	if depth > 4 {
		return unresolved
	}
	switch e := expr.(type) {
	case *ast.BasicLit:
		if e.Kind == token.STRING {
			if s, err := strconv.Unquote(e.Value); err == nil {
				return s
			}
		}
		return unresolved
	case *ast.BinaryExpr:
		if e.Op == token.ADD {
			return flattenPathExpr(e.X, resolve, depth) + flattenPathExpr(e.Y, resolve, depth)
		}
		return unresolved
	case *ast.Ident:
		if rhs, ok := resolve[e.Name]; ok {
			return flattenPathExpr(rhs, resolve, depth+1)
		}
		return unresolved
	case *ast.ParenExpr:
		return flattenPathExpr(e.X, resolve, depth)
	default:
		return unresolved
	}
}

// pathShapeFromFlat turns flattenPathExpr's output into pathSegments: a
// segment containing the unresolved marker anywhere is a variable
// segment, matching openAPISegments' {param} convention.
func pathShapeFromFlat(flat string) []pathSegment {
	var segs []pathSegment
	for _, s := range splitPathSegments(flat) {
		segs = append(segs, pathSegment{text: s, variable: strings.Contains(s, unresolved)})
	}
	return segs
}

// collectFileLocalAssignments builds flattenPathExpr's resolve map for
// one file: every `const NAME = <expr>` (function-local or file-scope)
// and every `NAME := <expr>` short variable declaration with a single
// name on the left, PLUS every plain `NAME = <expr>` reassignment. An
// identifier assigned more than once anywhere in the file — e.g.
// cmd_principal.go's `verb := "enable"` then conditionally `verb =
// "disable"`, the CLI's own dynamic-verb path builder for both
// /principals/{id}/enable and /principals/{id}/disable — is deliberately
// left OUT of the map rather than resolved to whichever assignment this
// scan saw first: a governance test that confidently resolves a mutable
// variable to one of its two possible values would treat the OTHER value
// as uncovered, which is a false positive this test must not produce.
// Left out, that identifier instead flattens to the unresolved marker
// (flattenPathExpr's *ast.Ident default case), which segmentsCompatible
// treats as "could be anything" — correct for a variable this scan
// cannot pin down to one value.
//
// Best-effort and file-scoped only (not block-scoped), which is enough
// for this package: the one indirection actually used for a PATH
// (cmd_resolume_composition.go's local `const apiPath = ...`, identical
// in both functions that declare it) is a const, never reassigned, and
// never collides with a same-named identifier holding a different value
// in the same file.
func collectFileLocalAssignments(f *ast.File) map[string]ast.Expr {
	resolve := map[string]ast.Expr{}
	seen := map[string]bool{}
	ambiguous := map[string]bool{}
	assign := func(name string, expr ast.Expr) {
		if ambiguous[name] {
			return
		}
		if seen[name] {
			// Two assignments to the same name: fine if they are
			// textually the identical string literal (the
			// cmd_resolume_composition.go `const apiPath = "..."`
			// case, declared once per function with the same value);
			// anything else — including two DIFFERENT literals, or
			// either side not being a plain string literal — makes
			// this identifier's value genuinely ambiguous, so it is
			// removed rather than resolved to whichever assignment was
			// seen first.
			prevLit, prevOK := resolve[name].(*ast.BasicLit)
			newLit, newOK := expr.(*ast.BasicLit)
			if prevOK && newOK && prevLit.Kind == token.STRING && newLit.Kind == token.STRING && prevLit.Value == newLit.Value {
				return
			}
			ambiguous[name] = true
			delete(resolve, name)
			return
		}
		seen[name] = true
		resolve[name] = expr
	}

	ast.Inspect(f, func(n ast.Node) bool {
		switch decl := n.(type) {
		case *ast.GenDecl:
			if decl.Tok != token.CONST {
				return true
			}
			for _, spec := range decl.Specs {
				vs, ok := spec.(*ast.ValueSpec)
				if !ok || len(vs.Names) != len(vs.Values) {
					continue
				}
				for i, name := range vs.Names {
					assign(name.Name, vs.Values[i])
				}
			}
		case *ast.AssignStmt:
			if (decl.Tok != token.DEFINE && decl.Tok != token.ASSIGN) || len(decl.Lhs) != len(decl.Rhs) {
				return true
			}
			for i, lhs := range decl.Lhs {
				if id, ok := lhs.(*ast.Ident); ok {
					assign(id.Name, decl.Rhs[i])
				}
			}
		}
		return true
	})
	return resolve
}

// writeCallArgIndex names every function this program routes a non-GET
// request through, and which argument position carries the API path.
// client.go's own doc comments name these as the request core every
// write in this program goes through — see putJSON/postJSON/deleteJSON's
// doc comments there, and postAssetMultipart's/postComposition's for the
// two raw-body uploads that do not fit that shape.
var writeCallArgIndex = map[string]int{
	"putJSON":            1,
	"postJSON":           1,
	"deleteJSON":         1,
	"postAssetMultipart": 2,
	"postComposition":    2,
}

// collectCLIWritePathShapes parses every non-test .go file in this
// package's directory and returns the path shape (see pathShapeFromFlat)
// of every call this program makes through one of writeCallArgIndex's
// functions.
func collectCLIWritePathShapes(t *testing.T) [][]pathSegment {
	t.Helper()

	entries, err := os.ReadDir(".")
	if err != nil {
		t.Fatalf("reading package directory: %v", err)
	}

	var shapes [][]pathSegment
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

		resolve := collectFileLocalAssignments(f)

		ast.Inspect(f, func(n ast.Node) bool {
			call, ok := n.(*ast.CallExpr)
			if !ok {
				return true
			}

			var funcName string
			switch fn := call.Fun.(type) {
			case *ast.SelectorExpr:
				funcName = fn.Sel.Name
			case *ast.Ident:
				funcName = fn.Name
			default:
				return true
			}

			argIdx, known := writeCallArgIndex[funcName]
			if !known || argIdx >= len(call.Args) {
				return true
			}

			flat := flattenPathExpr(call.Args[argIdx], resolve, 0)
			shapes = append(shapes, pathShapeFromFlat(flat))
			return true
		})
	}

	return shapes
}

// nonGETOpenAPIOperations reads api/openapi.yaml (relative to the
// repository root — this test's working directory is cmd/showmeshctl, so
// it walks up two levels, mirroring internal/coordinator/api/
// openapi_test.go's loadOpenAPIDocument for the identical file) and
// returns, per path declaring at least one non-GET operation, the set of
// its non-GET methods (upper-cased).
func nonGETOpenAPIOperations(t *testing.T) map[string]map[string]bool {
	t.Helper()

	path := filepath.Join("..", "..", "api", "openapi.yaml")
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("reading %s: %v", path, err)
	}

	var doc struct {
		Paths map[string]map[string]any `yaml:"paths"`
	}
	if err := yaml.Unmarshal(raw, &doc); err != nil {
		t.Fatalf("parsing %s as YAML: %v", path, err)
	}

	methodNames := map[string]bool{"get": true, "put": true, "post": true, "delete": true, "patch": true}

	out := map[string]map[string]bool{}
	for p, methods := range doc.Paths {
		for method := range methods {
			lower := strings.ToLower(method)
			if !methodNames[lower] || lower == "get" {
				continue
			}
			if out[p] == nil {
				out[p] = map[string]bool{}
			}
			out[p][strings.ToUpper(lower)] = true
		}
	}
	return out
}

// TestEveryWritePathHasACLIVerb is ADR-039 decision 9's second enforced
// test: every non-GET path in api/openapi.yaml must be reachable through
// this program's own write call sites (putJSON/postJSON/deleteJSON/
// postAssetMultipart/postComposition), matched structurally by path
// shape (see segmentsCompatible), or explicitly exempted in
// exemptWritePaths with a stated reason. A new write endpoint shipped
// with no CLI verb — the G-6 gap this decision exists to stop recurring —
// fails this test by construction rather than by someone remembering to
// update a checklist.
//
// This test cannot see the API's own Go handler types (cmd/showmeshctl
// must never import a coordinator package — importgraph_test.go forbids
// the IMPORT, and reading api/openapi.yaml as plain data plus parsing
// this program's own source as plain Go syntax imports nothing).
func TestEveryWritePathHasACLIVerb(t *testing.T) {
	cliShapes := collectCLIWritePathShapes(t)
	if len(cliShapes) == 0 {
		t.Fatal("found zero write call sites in cmd/showmeshctl — the AST scan is almost certainly broken, " +
			"not that this program issues no writes")
	}

	ops := nonGETOpenAPIOperations(t)
	// Floor: a YAML-shape drift that stops the parser seeing paths must
	// fail loudly, not pass over an empty (or nearly empty) set.
	if len(ops) < 20 {
		t.Fatalf("found only %d non-GET paths in api/openapi.yaml (expected at least 20) — "+
			"the YAML scan is almost certainly broken", len(ops))
	}

	// Every dynamicWritePathCoverage entry must name an operation that
	// still exists, with a stated call site.
	dynamicallyCoveredPaths := map[string]bool{}
	for op, callSite := range dynamicWritePathCoverage {
		if callSite == "" {
			t.Errorf("dynamicWritePathCoverage[%q] has no stated call site", op)
		}
		method, p, ok := strings.Cut(op, " ")
		if !ok {
			t.Errorf("dynamicWritePathCoverage key %q is not \"METHOD /path\"", op)
			continue
		}
		if !ops[p][method] {
			t.Errorf("dynamicWritePathCoverage names %q but api/openapi.yaml declares no such "+
				"operation — remove the stale entry", op)
			continue
		}
		dynamicallyCoveredPaths[p] = true
	}

	for p := range ops {
		if reason, exempt := exemptWritePaths[p]; exempt {
			if reason == "" {
				t.Errorf("%s is on exemptWritePaths with no stated reason", p)
			}
			continue
		}
		if dynamicallyCoveredPaths[p] {
			continue
		}

		apiSegs := openAPISegments(p)
		covered := false
		for _, cliSegs := range cliShapes {
			if segmentsCompatible(cliSegs, apiSegs) {
				covered = true
				break
			}
		}
		if !covered {
			t.Errorf("non-GET path %q has no matching showmeshctl write call, no entry in "+
				"dynamicWritePathCoverage, and no entry in exemptWritePaths — add a CLI verb for it "+
				"(ADR-030, CLAUDE.md's CLI-parity constraint), or add a reasoned exemption", p)
		}
	}
}
