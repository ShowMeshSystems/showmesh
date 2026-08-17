package agent

import (
	"testing"
)

func TestBuildSurfaceSpecNDIOutputReplacesSinkWithNDISink(t *testing.T) {
	params := map[string]any{
		"output": map[string]any{
			"transport": "ndi",
			"ndi":       map[string]any{"sourceName": "garage-window"},
		},
	}
	spec, err := buildSurfaceSpec("surface-1", params)
	if err != nil {
		t.Fatalf("buildSurfaceSpec: %v", err)
	}

	sawQueue := false
	var sinkFactory, ndiName string
	for _, st := range spec.Stages {
		if st.Label == "queue-sink" {
			sawQueue = true
		}
		if st.Label == "sink" {
			if len(st.Elements) != 1 {
				t.Fatalf("sink stage has %d elements, want 1", len(st.Elements))
			}
			sinkFactory = st.Elements[0].Factory
			for _, p := range st.Elements[0].Properties {
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
	spec, err := buildSurfaceSpec("surface-1", map[string]any{})
	if err != nil {
		t.Fatalf("buildSurfaceSpec: %v", err)
	}
	assertFakesink(t, spec)
}

func TestBuildSurfaceSpecFallsBackToTestPatternForHDMI(t *testing.T) {
	params := map[string]any{
		"output": map[string]any{
			"transport": "hdmi",
			"hdmi":      map[string]any{"display": "HDMI-1"},
		},
	}
	spec, err := buildSurfaceSpec("surface-1", params)
	if err != nil {
		t.Fatalf("buildSurfaceSpec: %v", err)
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
	spec, err := buildSurfaceSpec("surface-1", params)
	if err != nil {
		t.Fatalf("buildSurfaceSpec: %v", err)
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
