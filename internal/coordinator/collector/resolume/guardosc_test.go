package resolume

import (
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"os/exec"
	"regexp"
	"strconv"
	"strings"
	"testing"
)

// This file guards CLAUDE.md's "no OSC anywhere, in either direction" rule
// for Track D. Neither check below is about the word "position": this
// package legitimately models positional data parsed FROM the composition
// file (idmap.go's layerIndexToID, Clip.LayerIndex — exactly what ADR-032
// asks for), and neither TestPackageNeverImportsOSC nor
// TestPackageNeverUsesUDPTransport inspects identifiers at all, so that
// code is untouched by either guard.

// oscDenyListExact is every known Go OSC library's import path, checked in
// addition to the pattern match below so a library whose path happens not
// to contain the literal substring "osc" as its own path element is still
// caught by name.
var oscDenyListExact = map[string]bool{
	"github.com/hypebeast/go-osc/osc": true,
	"github.com/scgolang/osc":         true,
	"github.com/glaslos/go-osc":       true,
}

// oscPathElement matches a path element that IS "osc" or has "osc" set off
// by a hyphen/underscore or a path boundary (e.g. "go-osc", "osc-go"), but
// not a substring buried in an unrelated word — "kiosk" contains no "osc"
// substring at all, and "oscar" fails because nothing follows its "osc"
// but "ar", not a boundary. Matched on path ELEMENTS (see below), never on
// the whole import string as a substring, so a future dependency like
// "kiosk" cannot trip it.
var oscPathElement = regexp.MustCompile(`(^|[-_])osc([-_]|$)`)

// TestPackageNeverImportsOSC is Track D's import guard: Arena's default
// OSC address space is positional-only (doc.go), so it cannot address a
// pinned clip and its output stream carries no reply this package could
// ever confirm an action against — an OSC send is unconfirmable by
// construction, forever, which ADR-029 treats as worse than no action at
// all. Adding OSC back is an owner decision and a new ADR, never a new
// import: this test fails the moment one lands, deliberately including a
// library this package has never heard of, by matching on path elements
// rather than a name it already knows.
func TestPackageNeverImportsOSC(t *testing.T) {
	out, err := exec.Command("go", "list", "-deps", ".").CombinedOutput()
	if err != nil {
		t.Fatalf("go list -deps . failed: %v\noutput:\n%s", err, out)
	}

	for _, dep := range strings.Split(strings.TrimSpace(string(out)), "\n") {
		if oscDenyListExact[dep] {
			t.Errorf("internal/coordinator/collector/resolume transitively imports %q, a known OSC library — "+
				"OSC is positional-only in Arena's default address space, so it cannot address a pinned clip and "+
				"produces no reply this package could ever confirm an action against. Use the by-id REST path "+
				"through Client instead. Adding OSC support back is an owner decision and a new ADR, not a new "+
				"import.", dep)
			continue
		}
		for _, elem := range strings.Split(dep, "/") {
			if oscPathElement.MatchString(elem) {
				t.Errorf("internal/coordinator/collector/resolume transitively imports %q, whose path element %q "+
					"names an OSC library — OSC is positional-only in Arena's default address space, so it cannot "+
					"address a pinned clip and produces no reply this package could ever confirm an action "+
					"against. Use the by-id REST path through Client instead. Adding OSC support back is an owner "+
					"decision and a new ADR, not a new import.", dep, elem)
				break
			}
		}
	}
}

// forbiddenNetSelectors is every net.X this package has no legitimate use
// for: OSC is a UDP protocol, and this package's only transports are REST
// (client.go) and a WebSocket (watch.go, over the gorilla/websocket
// dialer, never net directly). Bare "Dial" is NOT here — see this test's
// own doc comment for why matching the unqualified identifier would flag
// watch.go's own legitimate websocket.Dialer/DialContext usage.
var forbiddenNetSelectors = map[string]bool{
	"ListenUDP":      true,
	"DialUDP":        true,
	"ResolveUDPAddr": true,
	"ListenPacket":   true,
	"UDPConn":        true,
	"UDPAddr":        true,
	"PacketConn":     true,
	"Dial":           true,
	"DialTimeout":    true,
}

// TestPackageNeverUsesUDPTransport is Track D's second, independent OSC
// guard, at the transport level rather than the import level: OSC over
// TCP exists but Arena does not speak it, so a future builder reaching for
// raw UDP instead of a new dependency would slip past
// TestPackageNeverImportsOSC entirely. This package imports net only for
// net.Error and net.DNSError (client.go) and never dials anything itself
// — client.go's *http.Client and watch.go's websocket.Dialer own their
// own connections — so any net.-QUALIFIED selector naming a UDP
// primitive, or a bare "udp"/"udp4"/"udp6" network-name literal, is
// unconditionally wrong here.
//
// Deliberately keyed on the net.-qualified selector, never the bare
// identifier: watch.go uses "Dial" fourteen times in entirely unrelated,
// legitimate names (websocket.Dialer, DialContext, WatcherStats.DialFailures,
// noteDialFailure, dialFailing, lastDialFailureLogAt) that a bare-identifier
// match would flag every one of. And this test does not forbid importing
// package "net" itself — client.go's net.Error/net.DNSError usage is
// legitimate and unrelated to a transport choice.
func TestPackageNeverUsesUDPTransport(t *testing.T) {
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
			switch x := n.(type) {
			case *ast.SelectorExpr:
				pkg, ok := x.X.(*ast.Ident)
				if ok && pkg.Name == "net" && forbiddenNetSelectors[x.Sel.Name] {
					t.Errorf("%s: %q is a UDP primitive from package net, in REAL CODE, not a comment — OSC is a "+
						"UDP protocol and is positional-only in Arena's default address space, so it cannot "+
						"address a pinned clip and produces no reply this package could ever confirm an action "+
						"against. This package's only transports are REST (client.go's *http.Client) and a "+
						"WebSocket (watch.go's websocket.Dialer); neither needs net.%s. Adding OSC or a raw UDP "+
						"transport back is an owner decision and a new ADR, not a new call.",
						fset.Position(x.Pos()), "net."+x.Sel.Name, x.Sel.Name)
				}
			case *ast.BasicLit:
				if x.Kind != token.STRING {
					return true
				}
				val, err := strconv.Unquote(x.Value)
				if err != nil {
					return true
				}
				if val == "udp" || val == "udp4" || val == "udp6" {
					t.Errorf("%s: string literal %q names a UDP network, in REAL CODE, not a comment — OSC is a "+
						"UDP protocol and is positional-only in Arena's default address space, so it cannot "+
						"address a pinned clip and produces no reply this package could ever confirm an action "+
						"against. This package's only transports are REST and a WebSocket, neither of which is "+
						"UDP. Adding OSC or a raw UDP transport back is an owner decision and a new ADR, not a "+
						"new literal.",
						fset.Position(x.Pos()), val)
				}
			}
			return true
		})
	}
}
