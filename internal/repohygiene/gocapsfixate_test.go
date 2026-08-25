package repohygiene

import (
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"testing"
)

// capsFixateCall matches a call to go-gst's Caps.Fixate method. The
// receiver name is not constrained because it is a local variable.
var capsFixateCall = regexp.MustCompile(`\.Fixate\(`)

// TestNoGoGstCapsFixate keeps go-gst's Caps.Fixate out of shipped source.
// go-gst v0.0.2 passes the caps to gst_caps_fixate as transfer-none while
// that function is transfer-full, so the call drops a reference the caller
// still owns. The result is a corrupted refcount and a non-deterministic
// crash somewhere later, never a compile error and never a failure at the
// call site, which is why a reader cannot be expected to catch it in
// review. Build the fixed caps directly, or ask the pad with a filtered
// CAPS query, instead.
func TestNoGoGstCapsFixate(t *testing.T) {
	root := repoRoot()

	var offenders []string
	for _, sweepRoot := range []string{"cmd", "internal", "pkg", "test"} {
		full := filepath.Join(root, sweepRoot)
		err := filepath.WalkDir(full, func(path string, d fs.DirEntry, err error) error {
			if err != nil {
				if os.IsNotExist(err) {
					return nil
				}
				return err
			}
			if d.IsDir() {
				if d.Name() == "node_modules" || d.Name() == ".git" {
					return filepath.SkipDir
				}
				return nil
			}
			if filepath.Ext(path) != ".go" {
				return nil
			}
			raw, err := os.ReadFile(path)
			if err != nil {
				return err
			}
			if capsFixateCall.Match(raw) {
				rel, _ := filepath.Rel(root, path)
				offenders = append(offenders, filepath.ToSlash(rel))
			}
			return nil
		})
		if err != nil {
			t.Fatalf("walking %s: %v", full, err)
		}
	}

	if len(offenders) > 0 {
		t.Errorf("Caps.Fixate is unsafe with go-gst v0.0.2 (transfer-full argument passed as transfer-none, corrupting the refcount); found in: %v", offenders)
	}
}
