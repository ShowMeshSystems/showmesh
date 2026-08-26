package main

import (
	"bytes"
	"fmt"
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
// printTopLevelUsage's Commands: section does not mention. It also fails,
// separately and by name, on a dispatcher whose own routing shape this
// walk cannot enumerate at all (dispatchesOnArgsZero) and on a
// helpCoverageDispatchExtras entry that no longer names a real function
// (the stale-key loop below) — both cases where children would otherwise
// go uncollected and be missed by the Commands: comparison entirely.

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
// switch shape, still has each one's subcommands to work from. Every
// entry names its own source. Mirrors writeparity_test.go's
// dynamicWritePathCoverage for the identical reason stated there: a
// generic walker cannot follow every possible Go control-flow shape.
// Unlike a plain switch dispatcher, this walk cannot enumerate an unlisted
// entry's own children by itself — so a new one left off this list, or an
// existing key that stops matching a real function (renamed or removed),
// is instead caught by dispatchesOnArgsZero below and by the
// helpCoverageDispatchExtras stale-key check in the test itself: both
// fail this test loudly, naming the function, rather than let its
// subcommands silently drop out of the enumeration — which is exactly how
// "cuecatalog deploy" went missing in the first place, just one level up
// (a hand-written help list rather than a hand-written coverage list).
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
// Returns nil for a function that does not use this shape at all — which
// is true of both a genuine leaf command and a dispatcher this walk
// cannot read (one covered instead by helpCoverageDispatchExtras, or one
// covered by neither). Callers must not treat nil as "leaf": use
// dispatchesOnArgsZero to tell the two apart.
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

// dispatchesOnArgsZero reports whether fn's body reads args[0] anywhere at
// all, by any means — an assignment (`sub := args[0]`), a map index
// (`someMap[args[0]]`), a comparison (`args[0] == "x"`), whatever. Every
// dispatcher in this package reads args[0] to pick which child to hand
// off to; no genuine leaf command (cmdVersion, cmdNodes, and so on) ever
// does, since a leaf has no child to pick. subSwitchChildren returning nil
// is ambiguous between the two; this is the second signal that resolves
// it — see collectRegisteredCommandPaths.
func dispatchesOnArgsZero(fn *ast.FuncDecl) bool {
	found := false
	ast.Inspect(fn.Body, func(n ast.Node) bool {
		if found {
			return false
		}
		idx, ok := n.(*ast.IndexExpr)
		if !ok {
			return true
		}
		base, ok := idx.X.(*ast.Ident)
		if !ok || base.Name != "args" {
			return true
		}
		lit, ok := idx.Index.(*ast.BasicLit)
		if !ok || lit.Kind != token.INT || lit.Value != "0" {
			return true
		}
		found = true
		return false
	})
	return found
}

// collectRegisteredCommandPaths recursively walks every dispatcher
// reachable from fn (a switch-based one via subSwitchChildren, an
// otherwise-shaped one via helpCoverageDispatchExtras), appending the
// space-joined path of every subcommand it finds — parent commands
// included, e.g. both "audio" is implied and "audio session" and
// "audio session apply" are all recorded as this program actually
// registers all three as reachable command words.
//
// A dispatcher that is neither switch-shaped (subSwitchChildren found
// children) nor listed in helpCoverageDispatchExtras, but still reads
// args[0] (dispatchesOnArgsZero), is a dispatcher this walk cannot read —
// not a leaf, whatever subSwitchChildren returned for it. Its own name is
// appended to unrecognized instead of being silently treated as childless,
// so the caller fails the test naming it rather than dropping its
// subcommands out of the enumeration unnoticed.
func collectRegisteredCommandPaths(funcs map[string]*ast.FuncDecl, fn *ast.FuncDecl, prefix []string, out, unrecognized *[]string, visiting map[string]bool) {
	name := fn.Name.Name
	if visiting[name] {
		return
	}
	visiting[name] = true
	defer delete(visiting, name)

	children := subSwitchChildren(fn)
	for _, c := range children {
		childPath := append(append([]string{}, prefix...), c.name)
		*out = append(*out, strings.Join(childPath, " "))
		if c.callee != "" {
			if next, ok := funcs[c.callee]; ok {
				collectRegisteredCommandPaths(funcs, next, childPath, out, unrecognized, visiting)
			}
		}
	}

	extras, isExtra := helpCoverageDispatchExtras[name]
	for _, extraName := range extras {
		childPath := append(append([]string{}, prefix...), extraName)
		*out = append(*out, strings.Join(childPath, " "))
		if callee, ok := helpCoverageDispatchExtraCallees[extraName]; ok {
			if next, ok := funcs[callee]; ok {
				collectRegisteredCommandPaths(funcs, next, childPath, out, unrecognized, visiting)
			}
		}
	}

	if !isExtra && len(children) == 0 && dispatchesOnArgsZero(fn) {
		where := name
		if len(prefix) > 0 {
			where = fmt.Sprintf("%s (reached as %q)", name, strings.Join(prefix, " "))
		}
		*unrecognized = append(*unrecognized, where)
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
// list, must be named in "showmeshctl --help"'s Commands: section. It also
// fails, by name, on a dispatcher whose routing shape this walk cannot
// read at all (see dispatchesOnArgsZero) and on a helpCoverageDispatchExtras
// entry that no longer names a real function.
func TestEveryRegisteredSubcommandIsInTopLevelHelp(t *testing.T) {
	funcs := parseShowmeshctlFuncs(t)
	runFn, ok := funcs["run"]
	if !ok {
		t.Fatal("could not find run() in main.go; this test's own AST walk is broken, not the command it checks")
	}

	// A helpCoverageDispatchExtras key is a function name spelled as a
	// string literal, so renaming or deleting that function does not
	// produce a compile error — it just makes the key stop matching
	// anything, and that entry's whole subtree of subcommands silently
	// stops being collected below. Mirrors writeparity_test.go:438-452's
	// stale dynamicWritePathCoverage-entry check for the same reason.
	keys := make([]string, 0, len(helpCoverageDispatchExtras))
	for key := range helpCoverageDispatchExtras {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	for _, key := range keys {
		if _, ok := funcs[key]; !ok {
			t.Errorf("helpCoverageDispatchExtras names %q but no top-level function by that name exists in "+
				"cmd/showmeshctl — it was renamed or removed; update or delete the stale entry", key)
		}
	}

	var paths, unrecognized []string
	collectRegisteredCommandPaths(funcs, runFn, nil, &paths, &unrecognized, map[string]bool{})
	if len(unrecognized) > 0 {
		sort.Strings(unrecognized)
		t.Fatalf("%d dispatcher(s) route on args[0] (dispatchesOnArgsZero) in a shape subSwitchChildren does not "+
			"recognize, and are not listed in helpCoverageDispatchExtras, so their own subcommands cannot be "+
			"enumerated at all and would otherwise be silently missing from this test's coverage rather than "+
			"from showmeshctl --help: add a helpCoverageDispatchExtras entry naming each one's subcommands:\n  %s",
			len(unrecognized), strings.Join(unrecognized, "\n  "))
	}
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
