//go:build cgo && showmesh_truncation_repro

package gstengine

import (
	"testing"

	"github.com/go-gst/go-gst/pkg/gst"
)

// TestBuildTestPipelineAchievesConstructionTimeLiveness guards the one
// mechanism this whole experiment depends on: that buildTestPipeline's
// is-live parameter, set before the pipeline's first state change,
// actually determines pipeline.IsLive(), unlike the post-hoc property
// flip this file's own top doc comment records as measurably not
// working. Runs against fakesink -- liveness is a source-side property,
// so no real card is needed to check it, and this must never regress
// silently between here and a real hardware run.
func TestBuildTestPipelineAchievesConstructionTimeLiveness(t *testing.T) {
	gst.Init() // this test never calls New, which is what would otherwise do it
	for _, live := range []bool{true, false} {
		sink := gst.ElementFactoryMake("fakesink", "")
		if sink == nil {
			t.Fatalf("could not construct fakesink")
		}
		sink.SetObjectProperty("sync", true)
		e, err := buildTestPipeline(truncationReproConfig(), sink, live)
		if err != nil {
			t.Fatalf("buildTestPipeline(live=%v): %v", live, err)
		}
		if got := e.pipeline.IsLive(); got != live {
			t.Errorf("constructed with live=%v but pipeline.IsLive()=%v -- construction-time approach did not work", live, got)
		} else {
			t.Logf("constructed with live=%v -> pipeline.IsLive() = %v", live, got)
		}
		_ = e.Close()
	}
}
