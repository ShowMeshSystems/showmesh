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

// linearRefPatterns match a private tracker reference in any form that
// has actually shipped: the issue key, and a bare tracker URL. Matching is
// case-insensitive. ADR-024, RES-002, TRACK-F and L0..L4 are deliberately
// not matched — those are this project's own public identifiers.
var linearRefPatterns = []*regexp.Regexp{
	regexp.MustCompile(`(?i)\bSM\x2d[0-9]+\b`),
	regexp.MustCompile(`(?i)\blinear\.app/`),
}

// commentWrap matches a line break and the comment marker that continues
// it. Content is matched twice, once as written and once with these
// removed, because an issue key wrapped across two comment lines is the
// evasion nobody types on purpose and every 72-column reflow produces.
var commentWrap = regexp.MustCompile(`\r?\n[ \t]*(?://+|\*|#)?[ \t]*`)

// wrapCandidate is the cheap precondition for that second pass: a key can
// only be split if the prefix and hyphen survive on the first line.
var wrapCandidate = regexp.MustCompile(`(?i)SM\x2d`)

// linearRefSweepRoots are the directories this test sweeps, relative to
// the repository root: everywhere shipped Go and TypeScript source and
// the public API contract live. docs/ is deliberately excluded — it is
// allowed to cite internal history — and so is ui/src/api/generated,
// which is generated from api/openapi.yaml and inherits that file's own
// cleanliness rather than being swept a second time.
var linearRefSweepRoots = []string{"cmd", "internal", "pkg", "test", "ui/src"}

var linearRefSweepExtraFiles = []string{"api/openapi.yaml"}

// linearRefSweptExts is wider than the shipped-source extensions because a
// reference is just as public in a fixture or a stylesheet.
var linearRefSweptExts = map[string]bool{
	".go": true, ".ts": true, ".tsx": true, ".js": true,
	".json": true, ".css": true, ".sql": true, ".sh": true,
	".yaml": true, ".yml": true,
}

// linearRefSkipDirs are matched as repo-relative paths, not basenames, so
// skipping the generated API types cannot silently exempt any other
// directory that happens to be named "generated".
var linearRefSkipDirs = map[string]bool{
	"node_modules":         true,
	".git":                 true,
	"ui/src/api/generated": true,
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
		seen := map[string]bool{}
		texts := []string{string(raw)}
		// The de-wrapping pass is quadratic on large generated files, so it
		// only runs where a split key could exist at all.
		if wrapCandidate.Match(raw) {
			texts = append(texts, commentWrap.ReplaceAllString(string(raw), ""))
		}
		for _, text := range texts {
			for _, pat := range linearRefPatterns {
				for _, m := range pat.FindAllString(text, -1) {
					if seen[m] {
						continue
					}
					seen[m] = true
					offenders = append(offenders, filepath.ToSlash(rel)+": "+m)
				}
			}
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
			relDir, _ := filepath.Rel(root, path)
			if d.IsDir() {
				if linearRefSkipDirs[filepath.ToSlash(relDir)] || linearRefSkipDirs[d.Name()] {
					return filepath.SkipDir
				}
				return nil
			}
			if !linearRefSweptExts[filepath.Ext(path)] {
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
