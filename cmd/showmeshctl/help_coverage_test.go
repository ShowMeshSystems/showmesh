package main

import (
	"bytes"
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"testing"
)

// This file is a regression guard: "showmeshctl --help" silently
// stopped naming "cuecatalog deploy" while still naming its two siblings
// "cuecatalog get" and "cuecatalog acknowledge" — the one command that lets
// an operator get a node a Cue catalog from the command line at all was
// missing from the one place task spec §3 promises "a script wrapping this
// tool should not have to grep stderr" for. It was found only by reading
// cmd_cuecatalog.go's own source, not by anything showmeshctl itself said.
//
// TestEveryRegisteredSubcommandIsInTopLevelHelp closes the blind spot
// rather than the single symptom: it enumerates every subcommand this
// program actually registers, straight from the dispatch code in
// main.go's run() and every cmd_*.go file's own switch statement — never
// from a hand-written list that could drift the identical way the
// Commands: block itself just did — and fails naming every one that
// printTopLevelUsage's Commands: section does not mention.

// helpAliases are switch case values that select help output for the
// CURRENT command rather than naming a child subcommand; every dispatcher
// in this package carries one such case and it must never be treated as a
// registered subcommand of its own.
var helpAliases = map[string]bool{
	"-h": true, "-help": true, "--help": true, "help": true,
}

// helpCoverageDispatchExtras documents every dispatcher function in this
// package that routes on args[0] by some mechanism OTHER than a
// `switch sub { case "x": ... }` — a map literal or a slice iterated in a
// loop — so subSwitchChildren's AST walk, which only recognizes the
// switch shape, does not silently skip the subcommands it dispatches.
// Every entry names its own source. Mirrors writeparity_test.go's
// dynamicWritePathCoverage for the identical reason stated there: a
// generic walker cannot follow every possible Go control-flow shape, but
// an unlisted new one must fail this test loudly rather than be silently
// skipped — which is exactly how "cuecatalog deploy" went missing in the
// first place, just one level up (a hand-written help list rather than a
// hand-written coverage list).
var helpCoverageDispatchExtras = map[string][]string{
	// cmdFPP (cmd_fpp.go) dispatches its 8 write verbs through the
	// fppWriteSubcommands map and its 3 read-only families through
	// "if args[0] == ..." checks — no switch statement at all.
	"cmdFPP": {
		"stop-playlist", "start-playlist", "stop-playlist-gracefully",
		"pause-playlist", "resume-playlist", "next-playlist-item",
		"prev-playlist-item", "set-volume",
		"reset-observation-sequence", "acknowledge-instance-uuid-change",
		"playlist-definitions", "playlist-entry-observations", "playlist-readiness",
	},
	// cmdAudioSession (cmd_audio_session.go) dispatches its nine
	// operations by iterating the audioSessionOps slice in a loop.
	"cmdAudioSession": {
		"apply", "prepare", "start", "pause", "resume", "seek", "advance", "stop", "clear",
	},
}

// helpCoverageDispatchExtraCallees names, for a helpCoverageDispatchExtras
// entry that itself dispatches further, the handler function to recurse
// into so its own children are enumerated too. An entry not listed here is
// a leaf as far as this walk is concerned.
var helpCoverageDispatchExtraCallees = map[string]string{
	"playlist-definitions":        "cmdFPPPlaylistDefinitions",
	"playlist-entry-observations": "cmdFPPPlaylistEntryObservations",
}

// parseShowmeshctlFuncs parses every non-test .go file in this directory
// and returns every top-level, non-method function declaration by name.
func parseShowmeshctlFuncs(t *testing.T) map[string]*ast.FuncDecl {
	t.Helper()
	entries, err := os.ReadDir(".")
	if err != nil {
		t.Fatalf("reading cmd/showmeshctl: %v", err)
	}
	fset := token.NewFileSet()
	funcs := map[string]*ast.FuncDecl{}
	for _, e := range entries {
		name := e.Name()
		if e.IsDir() || !strings.HasSuffix(name, ".go") || strings.HasSuffix(name, "_test.go") {
			continue
		}
		f, err := parser.ParseFile(fset, filepath.Join(".", name), nil, 0)
		if err != nil {
			t.Fatalf("parsing %s: %v", name, err)
		}
		for _, decl := range f.Decls {
			fd, ok := decl.(*ast.FuncDecl)
			if !ok || fd.Recv != nil || fd.Body == nil {
				continue
			}
			funcs[fd.Name.Name] = fd
		}
	}
	return funcs
}

// subDispatchChild is one subcommand name a dispatcher function's own
// `switch sub { case "x": ... }` recognizes, plus the function it hands
// off to (empty if the case does not delegate to another dispatcher).
type subDispatchChild struct {
	name   string
	callee string
}

// subSwitchChildren finds fn's own `x, y := args[0], args[1:]` (or
// `x := args[0]`) followed by a `switch x { case "...": ... }` — the
// shape every hand-written dispatcher in this package uses (main.go's
// run(), cmd_cuecatalog.go's cmdCueCatalog, and so on) — and returns each
// case's string literal(s) (excluding helpAliases) together with the
// function called by that case's own `return someFunc(...)`, if any.
// Returns nil for a function that does not use this shape at all (a leaf
// command, or a dispatcher covered instead by
// helpCoverageDispatchExtras).
func subSwitchChildren(fn *ast.FuncDecl) []subDispatchChild {
	subVar := ""
	ast.Inspect(fn.Body, func(n ast.Node) bool {
		if subVar != "" {
			return false
		}
		assign, ok := n.(*ast.AssignStmt)
		if !ok || assign.Tok != token.DEFINE {
			return true
		}
		for i, rhs := range assign.Rhs {
			idx, ok := rhs.(*ast.IndexExpr)
			if !ok {
				continue
			}
			base, ok := idx.X.(*ast.Ident)
			if !ok || base.Name != "args" {
				continue
			}
			lit, ok := idx.Index.(*ast.BasicLit)
			if !ok || lit.Kind != token.INT || lit.Value != "0" {
				continue
			}
			if i >= len(assign.Lhs) {
				continue
			}
			if id, ok := assign.Lhs[i].(*ast.Ident); ok && id.Name != "_" {
				subVar = id.Name
			}
		}
		return subVar == ""
	})
	if subVar == "" {
		return nil
	}

	var sw *ast.SwitchStmt
	ast.Inspect(fn.Body, func(n ast.Node) bool {
		if sw != nil {
			return false
		}
		s, ok := n.(*ast.SwitchStmt)
		if !ok {
			return true
		}
		tagID, ok := s.Tag.(*ast.Ident)
		if ok && tagID.Name == subVar {
			sw = s
			return false
		}
		return true
	})
	if sw == nil {
		return nil
	}

	var children []subDispatchChild
	for _, stmt := range sw.Body.List {
		clause, ok := stmt.(*ast.CaseClause)
		if !ok {
			continue
		}
		var literals []string
		for _, expr := range clause.List {
			lit, ok := expr.(*ast.BasicLit)
			if !ok || lit.Kind != token.STRING {
				continue
			}
			v, err := strconv.Unquote(lit.Value)
			if err != nil || helpAliases[v] {
				continue
			}
			literals = append(literals, v)
		}
		if len(literals) == 0 {
			continue
		}
		callee := ""
		for _, s := range clause.Body {
			if callee != "" {
				break
			}
			ast.Inspect(s, func(n ast.Node) bool {
				if callee != "" {
					return false
				}
				ret, ok := n.(*ast.ReturnStmt)
				if !ok || len(ret.Results) != 1 {
					return true
				}
				call, ok := ret.Results[0].(*ast.CallExpr)
				if !ok {
					return true
				}
				if id, ok := call.Fun.(*ast.Ident); ok {
					callee = id.Name
				}
				return false
			})
		}
		for _, lit := range literals {
			children = append(children, subDispatchChild{name: lit, callee: callee})
		}
	}
	return children
}

// collectRegisteredCommandPaths recursively walks every dispatcher
// reachable from fn (a switch-based one via subSwitchChildren, an
// otherwise-shaped one via helpCoverageDispatchExtras), appending the
// space-joined path of every subcommand it finds — parent commands
// included, e.g. both "audio" is implied and "audio session" and
// "audio session apply" are all recorded as this program actually
// registers all three as reachable command words.
func collectRegisteredCommandPaths(funcs map[string]*ast.FuncDecl, fn *ast.FuncDecl, prefix []string, out *[]string, visiting map[string]bool) {
	name := fn.Name.Name
	if visiting[name] {
		return
	}
	visiting[name] = true
	defer delete(visiting, name)

	for _, c := range subSwitchChildren(fn) {
		childPath := append(append([]string{}, prefix...), c.name)
		*out = append(*out, strings.Join(childPath, " "))
		if c.callee != "" {
			if next, ok := funcs[c.callee]; ok {
				collectRegisteredCommandPaths(funcs, next, childPath, out, visiting)
			}
		}
	}

	for _, extraName := range helpCoverageDispatchExtras[name] {
		childPath := append(append([]string{}, prefix...), extraName)
		*out = append(*out, strings.Join(childPath, " "))
		if callee, ok := helpCoverageDispatchExtraCallees[extraName]; ok {
			if next, ok := funcs[callee]; ok {
				collectRegisteredCommandPaths(funcs, next, childPath, out, visiting)
			}
		}
	}
}

// commandsBlock extracts the "Commands:" section of showmeshctl --help's
// output — the part task spec §3 and this bug's own report both mean by
// "showmeshctl --help lists": not the leading prose, and not the trailing
// exit-code appendix, either of which can accidentally CONTAIN a command
// string in unrelated running text (the exit-code appendix mentions
// "showmeshctl night end-session" and, two lines later but NOT
// contiguously, "prepare-site", which must not be allowed to satisfy a
// check for the actual, separate command "night prepare-site").
func commandsBlock(t *testing.T, help string) string {
	t.Helper()
	start := strings.Index(help, "Commands:\n")
	if start == -1 {
		t.Fatal("showmeshctl --help output has no \"Commands:\" section")
	}
	rest := help[start+len("Commands:\n"):]
	end := strings.Index(rest, "\nGlobal flags")
	if end == -1 {
		t.Fatal("showmeshctl --help output's \"Commands:\" section has no terminating \"Global flags\" line")
	}
	return rest[:end]
}

// TestEveryRegisteredSubcommandIsInTopLevelHelp is the reproduction and
// regression test for a missing "cuecatalog deploy" entry: every subcommand
// this program's own dispatch code
// registers, enumerated from that code rather than from any hand-written
// list, must be named in "showmeshctl --help"'s Commands: section.
func TestEveryRegisteredSubcommandIsInTopLevelHelp(t *testing.T) {
	funcs := parseShowmeshctlFuncs(t)
	runFn, ok := funcs["run"]
	if !ok {
		t.Fatal("could not find run() in main.go; this test's own AST walk is broken, not the command it checks")
	}

	var paths []string
	collectRegisteredCommandPaths(funcs, runFn, nil, &paths, map[string]bool{})
	if len(paths) == 0 {
		t.Fatal("collected zero registered subcommand paths from run()'s own dispatch code; this test's own AST walk is broken, not the command it checks")
	}
	sort.Strings(paths)

	var stdout bytes.Buffer
	printTopLevelUsage(&stdout)
	block := commandsBlock(t, stdout.String())

	var missing []string
	for _, p := range paths {
		if !strings.Contains(block, p) {
			missing = append(missing, p)
		}
	}
	if len(missing) > 0 {
		t.Errorf("showmeshctl --help's Commands: section does not mention %d registered subcommand(s) "+
			"(enumerated from run()'s own dispatch code and each cmd_*.go dispatcher, not a hand-written list):\n  %s",
			len(missing), strings.Join(missing, "\n  "))
	}
}
