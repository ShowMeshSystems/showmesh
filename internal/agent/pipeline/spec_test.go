package pipeline

import (
	"strings"
	"testing"
)

// TestDefaultTestPatternSpecContainsQueueBeforeSink is the B0-amendment
// requirement made concrete: the default spec must carry a queue stage
// before its sink so a thread boundary exists from this seam onward.
// Mutation check: removing QueueStage from [DefaultTestPatternSpec] makes
// this test fail, confirmed by hand.
func TestDefaultTestPatternSpecContainsQueueBeforeSink(t *testing.T) {
	spec := DefaultTestPatternSpec("surface-1")

	var labels []string
	queueIndex, sinkIndex := -1, -1
	for i, st := range spec.Stages {
		labels = append(labels, st.Label)
		if len(st.Elements) == 1 && st.Elements[0].Factory == "queue" {
			queueIndex = i
		}
		if st.Label == "sink" {
			sinkIndex = i
		}
	}
	if queueIndex == -1 {
		t.Fatalf("no queue stage found in default spec; stages: %v", labels)
	}
	if sinkIndex == -1 {
		t.Fatalf("no sink stage found in default spec; stages: %v", labels)
	}
	if queueIndex >= sinkIndex {
		t.Fatalf("queue stage (index %d) is not before sink stage (index %d); stages: %v", queueIndex, sinkIndex, labels)
	}
}

// TestSpecBuildArgvChainsWithBang proves the built argv links every element
// with gst-launch's "!" syntax and preserves property order deterministically.
func TestSpecBuildArgvChainsWithBang(t *testing.T) {
	spec := Spec{
		SurfaceID: "s1",
		Stages: []Stage{
			{Label: "source", Elements: []Element{{Factory: "videotestsrc", Properties: []Property{{Key: "is-live", Value: "true"}}}}},
			{Label: "queue", Elements: []Element{{Factory: "queue", Name: "q1", Properties: []Property{{Key: "max-size-buffers", Value: "4"}}}}},
			{Label: "sink", Elements: []Element{{Factory: "fakesink"}}},
		},
	}
	argv, err := spec.BuildArgv()
	if err != nil {
		t.Fatalf("BuildArgv: %v", err)
	}
	got := strings.Join(argv, " ")
	want := "videotestsrc is-live=true ! queue name=q1 max-size-buffers=4 ! fakesink"
	if got != want {
		t.Fatalf("argv = %q, want %q", got, want)
	}
}

// TestSpecBuildArgvIsDeterministic proves the same Spec always builds the
// same argv — required so a captured argv is diffable across runs, per this
// package's own doc comment on why Property is a slice, not a map.
func TestSpecBuildArgvIsDeterministic(t *testing.T) {
	spec := DefaultTestPatternSpec("surface-1")
	first, err := spec.BuildArgv()
	if err != nil {
		t.Fatalf("BuildArgv: %v", err)
	}
	for i := 0; i < 20; i++ {
		again, err := spec.BuildArgv()
		if err != nil {
			t.Fatalf("BuildArgv (iteration %d): %v", i, err)
		}
		if strings.Join(first, " ") != strings.Join(again, " ") {
			t.Fatalf("argv is not deterministic: %q != %q", first, again)
		}
	}
}

// TestSpecBuildArgvRejectsEmptySpec proves a spec with no stages/elements
// is rejected rather than silently producing an empty argv that gst-launch
// would run as a no-op.
func TestSpecBuildArgvRejectsEmptySpec(t *testing.T) {
	if _, err := (Spec{SurfaceID: "s1"}).BuildArgv(); err == nil {
		t.Fatalf("BuildArgv on an empty spec: want error, got nil")
	}
	if _, err := (Spec{SurfaceID: "s1", Stages: []Stage{{Label: "empty"}}}).BuildArgv(); err == nil {
		t.Fatalf("BuildArgv on a spec with a stage but no elements: want error, got nil")
	}
}

// TestCapsFilterStageEmptyFormatIsNoStage proves the documented zero-format
// behaviour: an empty PixelFormat contributes nothing to the built argv,
// rather than an invalid empty caps string.
func TestCapsFilterStageEmptyFormatIsNoStage(t *testing.T) {
	stage := CapsFilterStage("caps", "")
	if len(stage.Elements) != 0 {
		t.Fatalf("CapsFilterStage with empty format produced %d elements, want 0", len(stage.Elements))
	}
}

// TestCapsFilterStagePinsPixelFormat proves PixelFormat is honored when
// set, as an explicit capsfilter stage rather than something hardcoded to
// RGB anywhere in argv construction.
func TestCapsFilterStagePinsPixelFormat(t *testing.T) {
	stage := CapsFilterStage("caps", "UYVY")
	spec := Spec{
		SurfaceID: "s1",
		Stages: []Stage{
			{Label: "source", Elements: []Element{{Factory: "videotestsrc"}}},
			stage,
			{Label: "sink", Elements: []Element{{Factory: "fakesink"}}},
		},
	}
	argv, err := spec.BuildArgv()
	if err != nil {
		t.Fatalf("BuildArgv: %v", err)
	}
	got := strings.Join(argv, " ")
	if !strings.Contains(got, "capsfilter caps=video/x-raw,format=UYVY") {
		t.Fatalf("argv %q does not contain the expected capsfilter stage", got)
	}
}
