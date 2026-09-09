package repohygiene

import (
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

// contractDocPath is the cross-repository contract whose status lines this
// test guards, relative to the repository root.
const contractDocPath = "docs/build/FPP-PLUGIN-COORDINATOR-CONTRACTS.md"

// contractStatusPattern matches a section's status line. The coordinator's
// half is captured; the plugin's half is deliberately not, because nothing
// in this repository can verify it.
var contractStatusPattern = regexp.MustCompile(`(?m)^\*\*Status: coordinator (BUILT|NOT BUILT)`)

// contractAnchorPattern matches the line that names the coordinator symbol a
// section's status is a claim about, or records that it names none.
var contractAnchorPattern = regexp.MustCompile("(?m)^Coordinator anchor: (?:`([A-Za-z_][A-Za-z0-9_]*)`|none)")

// contractStatusClaim is one section's coordinator-side claim: a status and
// the symbol whose presence or absence in this repository decides it.
type contractStatusClaim struct {
	line   int
	built  bool
	symbol string // empty when the section names no coordinator symbol
}

// parseContractStatusClaims pairs every coordinator status line with the
// anchor line that must follow it. An unpaired status line is an error
// rather than a skipped row: a status nobody can check is the defect this
// test exists to close, so it must not be expressible.
func parseContractStatusClaims(doc string) ([]contractStatusClaim, error) {
	lines := strings.Split(doc, "\n")
	var claims []contractStatusClaim
	for i, line := range lines {
		m := contractStatusPattern.FindStringSubmatch(line)
		if m == nil {
			continue
		}
		claim := contractStatusClaim{line: i + 1, built: m[1] == "BUILT"}
		// The anchor line follows within a short window, after the
		// status sentence and a blank line.
		found := false
		for j := i + 1; j < len(lines) && j <= i+12; j++ {
			if contractStatusPattern.MatchString(lines[j]) {
				break
			}
			if a := contractAnchorPattern.FindStringSubmatch(lines[j]); a != nil {
				claim.symbol = a[1]
				found = true
				break
			}
		}
		if !found {
			return nil, fmt.Errorf("line %d: a coordinator status line with no \"Coordinator anchor:\" line within 12 lines; every status must name the symbol that decides it, or say none", i+1)
		}
		claims = append(claims, claim)
	}
	if len(claims) == 0 {
		return nil, fmt.Errorf("found no coordinator status lines; the status wording or this pattern has changed")
	}
	return claims, nil
}

// checkContractStatusClaims reports one message per claim the tree
// contradicts. symbolExists decides presence, so the rule is testable
// without touching the repository.
func checkContractStatusClaims(claims []contractStatusClaim, symbolExists func(string) bool) []string {
	var violations []string
	for _, c := range claims {
		if c.symbol == "" {
			continue
		}
		switch {
		case c.built && !symbolExists(c.symbol):
			violations = append(violations, fmt.Sprintf(
				"line %d says the coordinator has BUILT this, but its anchor %s exists nowhere under internal/ or cmd/: either the section is a claim about code that was never written, or the symbol was renamed and the status line was not.",
				c.line, c.symbol))
		case !c.built && symbolExists(c.symbol):
			violations = append(violations, fmt.Sprintf(
				"line %d says the coordinator has NOT BUILT this, but its anchor %s exists: the code shipped and this status did not follow it. A reader is being told a route is unavailable while it is serving.",
				c.line, c.symbol))
		}
	}
	return violations
}

func TestParseContractStatusClaimsRequiresAnAnchor(t *testing.T) {
	_, err := parseContractStatusClaims("**Status: coordinator BUILT, plugin BUILT.** No anchor follows this.\n")
	if err == nil {
		t.Fatal("expected an error for a status line with no anchor line, got nil")
	}
}

func TestParseContractStatusClaimsRejectsEmptyParse(t *testing.T) {
	_, err := parseContractStatusClaims("## A section\n\nProse with no status line.\n")
	if err == nil {
		t.Fatal("expected an error for a document with no coordinator status lines, got nil")
	}
}

func TestCheckContractStatusClaimsBuiltWithMissingAnchor(t *testing.T) {
	claims := []contractStatusClaim{{line: 7, built: true, symbol: "handleThing"}}
	got := checkContractStatusClaims(claims, func(string) bool { return false })
	if len(got) != 1 || !strings.Contains(got[0], "handleThing") {
		t.Fatalf("want one violation naming handleThing, got %v", got)
	}
}

func TestCheckContractStatusClaimsNotBuiltWithPresentAnchor(t *testing.T) {
	claims := []contractStatusClaim{{line: 9, built: false, symbol: "handleThing"}}
	got := checkContractStatusClaims(claims, func(string) bool { return true })
	if len(got) != 1 || !strings.Contains(got[0], "serving") {
		t.Fatalf("want one violation about a route that is serving, got %v", got)
	}
}

// TestCheckContractStatusClaimsIgnoresPluginSymbols is the reason this test
// checks anchors rather than every identifier the document names. Of the 38
// Go-shaped identifiers in that file, 18 exist only in the plugin
// repository, so an existence check over all of them reports 18 false
// offenders against a correct tree. The anchor line is the ownership
// marking that makes the coordinator's half checkable on its own.
func TestCheckContractStatusClaimsIgnoresPluginSymbols(t *testing.T) {
	claims := []contractStatusClaim{{line: 11, built: true, symbol: ""}}
	if got := checkContractStatusClaims(claims, func(string) bool { return false }); len(got) != 0 {
		t.Fatalf("want no violation for a section that names no coordinator symbol, got %v", got)
	}
}

// TestContractStatusMatchesCoordinatorTree checks
// docs/build/FPP-PLUGIN-COORDINATOR-CONTRACTS.md's coordinator-side status
// claims against this repository. That document is read by people who
// cannot see the other side's code, which is why the status lines exist at
// all and why deleting them would remove the fact rather than move it. What
// no human reliably does is update one when the code it describes changes,
// in either direction: the section can ship and keep saying unbuilt, or be
// written from inside a branch and say pending forever. Only the
// coordinator's half is checked here. The plugin's half is that
// repository's to assert, and nothing in this tree can see it.
func TestContractStatusMatchesCoordinatorTree(t *testing.T) {
	root := repoRoot()
	raw, err := os.ReadFile(filepath.Join(root, contractDocPath))
	if err != nil {
		t.Fatalf("reading %s: %v", contractDocPath, err)
	}
	claims, err := parseContractStatusClaims(string(raw))
	if err != nil {
		t.Fatalf("parsing %s: %v", contractDocPath, err)
	}

	exists := func(symbol string) bool {
		for _, dir := range []string{"internal", "cmd"} {
			found := false
			_ = filepath.WalkDir(filepath.Join(root, dir), func(path string, d os.DirEntry, err error) error {
				if err != nil || found || d.IsDir() || filepath.Ext(path) != ".go" {
					return nil
				}
				b, rerr := os.ReadFile(path)
				if rerr == nil && strings.Contains(string(b), symbol) {
					found = true
				}
				return nil
			})
			if found {
				return true
			}
		}
		return false
	}

	if violations := checkContractStatusClaims(claims, exists); len(violations) > 0 {
		t.Errorf("%s disagrees with this repository:\n  %s", contractDocPath, strings.Join(violations, "\n  "))
	}
}
