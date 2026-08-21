//go:build cgo

package gstengine

import (
	"go/ast"
	"go/parser"
	"go/token"
	"testing"
)

// TestLTCFeedPathNeverCallsTimeSleep guards ADR-042's no-Go-sleep rule at
// the source: a Go timer once paced the LTC channel by pushing a buffer
// then sleeping for that buffer's duration, and measurably dragged all
// program audio to 88% of real time, because appsrc's own block=true
// backpressure is what paces the feeder correctly and a sleep on top of
// it only adds latency interleave has no way to recover. waitBeforeRetry
// is the named, reviewed exception the doc comments on ltcSilenceChunk
// and ltcAppSrcLeadSeconds carry — it already uses time.After inside a
// select, not time.Sleep, so this test forbids time.Sleep in ltc.go with
// no exception clause to punch through.
func TestLTCFeedPathNeverCallsTimeSleep(t *testing.T) {
	fset := token.NewFileSet()
	f, err := parser.ParseFile(fset, "ltc.go", nil, 0)
	if err != nil {
		t.Fatalf("parsing ltc.go: %v", err)
	}
	ast.Inspect(f, func(n ast.Node) bool {
		call, ok := n.(*ast.CallExpr)
		if !ok {
			return true
		}
		sel, ok := call.Fun.(*ast.SelectorExpr)
		if !ok {
			return true
		}
		pkg, ok := sel.X.(*ast.Ident)
		if ok && pkg.Name == "time" && sel.Sel.Name == "Sleep" {
			t.Errorf("%s: time.Sleep in ltc.go, in REAL CODE, not a comment — ADR-042 forbids a Go-side sleep "+
				"pacing the LTC feed path; a Go timer pacing the channel once dragged all program audio to 88%% "+
				"of real time. Pace through appsrc's own block=true backpressure (PushBuffer itself), or through "+
				"time.After inside a select on stopFeed the way waitBeforeRetry already does.",
				fset.Position(call.Pos()))
		}
		return true
	})
}
