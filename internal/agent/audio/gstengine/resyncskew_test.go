//go:build cgo

package gstengine

import (
	"go/ast"
	"go/parser"
	"go/token"
	"testing"
)

// TestResumeReanchorsViaFlushingSeek guards Resume against going back to
// an offset-only re-anchor (resyncMixerPads/resyncMixerPadsToLivePosition
// called on their own, without a seek): GstAudioAggregator keeps
// advancing its own output clock for the whole hold, so buffers still
// carrying pre-hold timestamps land in its past and are discarded
// outright rather than played back late. Only a real seek gives the
// branch a fresh segment the aggregator will actually accept; see
// engine_real_integration_test.go's resume-continuity tests for the
// flow-level proof.
func TestResumeReanchorsViaFlushingSeek(t *testing.T) {
	fset := token.NewFileSet()
	f, err := parser.ParseFile(fset, "methods.go", nil, 0)
	if err != nil {
		t.Fatalf("parsing methods.go: %v", err)
	}

	var fn *ast.FuncDecl
	ast.Inspect(f, func(n ast.Node) bool {
		d, ok := n.(*ast.FuncDecl)
		if ok && d.Name.Name == "Resume" {
			fn = d
			return false
		}
		return true
	})
	if fn == nil {
		t.Fatal("could not find func (e *Engine) Resume in methods.go")
	}

	callsSeekTo := false
	ast.Inspect(fn.Body, func(n ast.Node) bool {
		call, ok := n.(*ast.CallExpr)
		if !ok {
			return true
		}
		sel, ok := call.Fun.(*ast.SelectorExpr)
		if !ok {
			return true
		}
		if sel.Sel.Name == "seekTo" {
			callsSeekTo = true
		}
		return true
	})

	if !callsSeekTo {
		t.Fatal("Resume no longer calls seekTo: an offset-only re-anchor discards the entire held duration once " +
			"GstAudioAggregator's own output clock has moved past the branch's pre-hold segment")
	}
}
