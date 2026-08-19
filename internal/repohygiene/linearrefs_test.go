// Package repohygiene holds repo-wide structural checks that do not
// belong to any single build package — see linearrefs_test.go.
package repohygiene

import (
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

// linearRefPattern matches this workspace's Linear issue key shape: the
// team prefix this workspace's Linear issues use, a hyphen, and a
// number. It deliberately does not match ADR-024, RES-002, TRACK-F, or
// L0..L4: those are this project's own public identifiers, documented in
// CLAUDE.md and docs/decisions, never a private ticket number in
// someone else's tracker.
var linearRefPattern = regexp.MustCompile(`\bSM\x2d[0-9]+\b`)

// linearRefSweepRoots are the directories this test sweeps, relative to
// the repository root: everywhere shipped Go and TypeScript source and
// the public API contract live. docs/ is deliberately excluded — it is
// allowed to cite internal history — and so is ui/src/api/generated,
// which is generated from api/openapi.yaml and inherits that file's own
// cleanliness rather than being swept a second time.
var linearRefSweepRoots = []string{"cmd", "internal", "pkg", "test", "ui/src"}

var linearRefSweepExtraFiles = []string{"api/openapi.yaml"}

var linearRefSkipDirs = map[string]bool{
	"node_modules": true,
	".git":         true,
	"generated":    true, // ui/src/api/generated
}

// repoRoot locates the repository root relative to this package's own
// directory (internal/repohygiene), so the sweep works regardless of the
// working directory `go test` is invoked from.
func repoRoot() string {
	return filepath.Join("..", "..")
}

// TestNoLinearIssueReferencesInShippedSource enforces the rule behind
// the 2026-08-19 comment sweep: a private issue-tracker number must
// never ship in this public repository's source, and must never reach
// api/openapi.yaml, which operators read as generated documentation.
// Cite the durable record instead — an ADR, a research record, or
// docs/build/BUILD-LOG.md — never a tracker id nobody outside this repo
// can resolve.
func TestNoLinearIssueReferencesInShippedSource(t *testing.T) {
	root := repoRoot()

	var offenders []string
	sweep := func(path string) {
		raw, err := os.ReadFile(path)
		if err != nil {
			t.Fatalf("reading %s: %v", path, err)
		}
		rel, _ := filepath.Rel(root, path)
		for _, m := range linearRefPattern.FindAllString(string(raw), -1) {
			offenders = append(offenders, filepath.ToSlash(rel)+": "+m)
		}
	}

	for _, sweepRoot := range linearRefSweepRoots {
		full := filepath.Join(root, sweepRoot)
		err := filepath.WalkDir(full, func(path string, d fs.DirEntry, err error) error {
			if err != nil {
				if os.IsNotExist(err) {
					return nil
				}
				return err
			}
			if d.IsDir() {
				if linearRefSkipDirs[d.Name()] {
					return filepath.SkipDir
				}
				return nil
			}
			ext := filepath.Ext(path)
			if ext != ".go" && ext != ".ts" && ext != ".tsx" {
				return nil
			}
			sweep(path)
			return nil
		})
		if err != nil {
			t.Fatalf("walking %s: %v", full, err)
		}
	}

	for _, f := range linearRefSweepExtraFiles {
		sweep(filepath.Join(root, f))
	}

	if len(offenders) > 0 {
		t.Errorf("found %d Linear issue reference(s) in shipped source, which this public repository "+
			"must never carry (an internal tracker id is meaningless to an operator and, in "+
			"api/openapi.yaml, ships as generated documentation):\n%s\n"+
			"Remove the citation — usually just the parenthetical, not the sentence around it — or, "+
			"if the reasoning is worth keeping, move it to the ADR, research record, or "+
			"docs/build/BUILD-LOG.md entry it belongs in.",
			len(offenders), strings.Join(offenders, "\n"))
	}
}
