package agent

import (
	"strings"
	"testing"

	"github.com/showmeshsystems/showmesh/internal/agent/pipeline"
)

func TestBuildSurfaceSpecNDIOutputReplacesSinkWithNDISink(t *testing.T) {
	params := map[string]any{
		"output": map[string]any{
			"transport": "ndi",
			"ndi":       map[string]any{"sourceName": "garage-window"},
		},
	}
	spec, outcome, err := applyOutputSink(pipeline.DefaultTestPatternSpec("surface-1"), "surface-1", params)
	if err != nil {
		t.Fatalf("applyOutputSink: %v", err)
	}
	if !outcome.Configured || !outcome.RealSink || outcome.Reason != "" {
		t.Errorf("outcome = %+v, want Configured=true RealSink=true Reason=\"\"", outcome)
	}
	if spec.OutputDegradedReason != "" {
		t.Errorf("spec.OutputDegradedReason = %q, want empty for a real ndi sink", spec.OutputDegradedReason)
	}

	sawQueue := false
	var sinkFactory, ndiName string
	for _, st := range spec.Stages {
		if st.Label == "queue-sink" {
			sawQueue = true
		}
		if st.Label == "sink" {
			// videoconvert then ndisink: ndisink advertises no packed
			// 24-bit RGB, so the conversion is required to negotiate.
			if len(st.Elements) != 2 {
				t.Fatalf("sink stage has %d elements, want 2 (videoconvert, ndisink)", len(st.Elements))
			}
			if got := st.Elements[0].Factory; got != "videoconvert" {
				t.Fatalf("sink stage element 0 = %q, want videoconvert", got)
			}
			sinkFactory = st.Elements[1].Factory
			for _, p := range st.Elements[1].Properties {
				if p.Key == "ndi-name" {
					ndiName = p.Value
				}
			}
		}
	}
	if !sawQueue {
		t.Errorf("no queue-sink stage found: build contract ruling 5 requires a queue before the sink regardless of which sink is chosen")
	}
	if sinkFactory != "ndisink" {
		t.Errorf("sink factory = %q, want ndisink", sinkFactory)
	}
	if ndiName != "garage-window" {
		t.Errorf("ndi-name = %q, want garage-window (from params.output.ndi.sourceName)", ndiName)
	}
}

func TestBuildSurfaceSpecFallsBackToTestPatternWhenOutputAbsent(t *testing.T) {
	spec, outcome, err := applyOutputSink(pipeline.DefaultTestPatternSpec("surface-1"), "surface-1", map[string]any{})
	if err != nil {
		t.Fatalf("applyOutputSink: %v", err)
	}
	// No output was ever requested — this is NOT a degradation to report
	// (nothing was asked for), unlike the hdmi/empty-sourceName cases
	// below, which DID ask for a transport and did not get one.
	if outcome.Configured {
		t.Errorf("outcome.Configured = true for a bare apply with no output block, want false")
	}
	if spec.OutputDegradedReason != "" {
		t.Errorf("spec.OutputDegradedReason = %q, want empty when output was never configured", spec.OutputDegradedReason)
	}
	assertFakesink(t, spec)
}

// TestBuildSurfaceSpecFallsBackToTestPatternForHDMI is this seam's own
// regression test for the defect the review found: falling back to a
// fakesink must never look like a plain, silent "running" — the returned
// outcome and Spec.OutputDegradedReason must both carry a real,
// actionable reason naming hdmi specifically.
func TestBuildSurfaceSpecFallsBackToTestPatternForHDMI(t *testing.T) {
	params := map[string]any{
		"output": map[string]any{
			"transport": "hdmi",
			"hdmi":      map[string]any{"display": "HDMI-1"},
		},
	}
	spec, outcome, err := applyOutputSink(pipeline.DefaultTestPatternSpec("surface-1"), "surface-1", params)
	if err != nil {
		t.Fatalf("applyOutputSink: %v", err)
	}
	if !outcome.Configured || outcome.RealSink {
		t.Fatalf("outcome = %+v, want Configured=true RealSink=false", outcome)
	}
	if outcome.Transport != "hdmi" {
		t.Errorf("outcome.Transport = %q, want hdmi", outcome.Transport)
	}
	if !strings.Contains(outcome.Reason, "hdmi") {
		t.Errorf("outcome.Reason = %q, want it to name hdmi specifically", outcome.Reason)
	}
	if spec.OutputDegradedReason != outcome.Reason {
		t.Errorf("spec.OutputDegradedReason = %q, want it to match outcome.Reason %q", spec.OutputDegradedReason, outcome.Reason)
	}
	assertFakesink(t, spec)
}

func TestBuildSurfaceSpecFallsBackWhenNDISourceNameEmpty(t *testing.T) {
	params := map[string]any{
		"output": map[string]any{
			"transport": "ndi",
			"ndi":       map[string]any{"sourceName": ""},
		},
	}
	spec, outcome, err := applyOutputSink(pipeline.DefaultTestPatternSpec("surface-1"), "surface-1", params)
	if err != nil {
		t.Fatalf("applyOutputSink: %v", err)
	}
	if !outcome.Configured || outcome.RealSink {
		t.Fatalf("outcome = %+v, want Configured=true RealSink=false", outcome)
	}
	if !strings.Contains(outcome.Reason, "sourceName") {
		t.Errorf("outcome.Reason = %q, want it to name the missing sourceName specifically", outcome.Reason)
	}
	if spec.OutputDegradedReason == "" {
		t.Errorf("spec.OutputDegradedReason is empty, want a real reason")
	}
	assertFakesink(t, spec)
}

func assertFakesink(t *testing.T, spec interface {
	BuildArgv() ([]string, error)
}) {
	t.Helper()
	argv, err := spec.BuildArgv()
	if err != nil {
		t.Fatalf("BuildArgv: %v", err)
	}
	found := false
	for _, a := range argv {
		if a == "fakesink" {
			found = true
		}
	}
	if !found {
		t.Errorf("argv = %v, want the fakesink fallback sink", argv)
	}
}
