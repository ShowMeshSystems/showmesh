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

// TestFSEQSourceSpecUsesFdsrcAndRawvideoparse proves B3's real source stage
// reads from this process's own stdin (fd=0) rather than generating content
// itself (B2a's videotestsrc), and that geometry/format/frameRate reach
// rawvideoparse's argv untouched.
func TestFSEQSourceSpecUsesFdsrcAndRawvideoparse(t *testing.T) {
	spec, err := FSEQSourceSpec("surface-1", 64, 32, "rgb", 40)
	if err != nil {
		t.Fatalf("FSEQSourceSpec: %v", err)
	}
	argv, err := spec.BuildArgv()
	if err != nil {
		t.Fatalf("BuildArgv: %v", err)
	}
	got := strings.Join(argv, " ")
	want := "fdsrc fd=0 is-live=true ! rawvideoparse width=64 height=32 format=RGB framerate=40/1"
	if !strings.HasPrefix(got, want) {
		t.Fatalf("argv = %q, want prefix %q", got, want)
	}
	if spec.PixelFormat != "RGB" {
		t.Fatalf("spec.PixelFormat = %q, want RGB", spec.PixelFormat)
	}
}

// TestFSEQSourceSpecCarriesQueueBeforeSink is FSEQSourceSpec's own version
// of TestDefaultTestPatternSpecContainsQueueBeforeSink: B3's source stage
// must not lose the B0-amendment thread-boundary discipline just because it
// replaced videotestsrc.
func TestFSEQSourceSpecCarriesQueueBeforeSink(t *testing.T) {
	spec, err := FSEQSourceSpec("surface-1", 64, 32, "rgb", 40)
	if err != nil {
		t.Fatalf("FSEQSourceSpec: %v", err)
	}
	queueIndex, sinkIndex := -1, -1
	for i, st := range spec.Stages {
		if len(st.Elements) == 1 && st.Elements[0].Factory == "queue" {
			queueIndex = i
		}
		if st.Label == "sink" {
			sinkIndex = i
		}
	}
	if queueIndex == -1 || sinkIndex == -1 || queueIndex >= sinkIndex {
		t.Fatalf("queue (index %d) must precede sink (index %d)", queueIndex, sinkIndex)
	}
}

// TestFSEQSourceSpecRejectsUnknownPixelFormat proves rgbw (which has no
// GStreamer raw-video mapping today — see GstVideoFormatForPixelFormat's
// doc comment) is refused rather than silently building an invalid or
// guessed caps string.
func TestFSEQSourceSpecRejectsUnknownPixelFormat(t *testing.T) {
	if _, err := FSEQSourceSpec("surface-1", 64, 32, "rgbw", 40); err == nil {
		t.Fatalf("FSEQSourceSpec with pixelFormat=rgbw: want error, got nil")
	}
}

// TestFSEQSourceSpecRejectsInvalidGeometry proves a zero or negative
// width/height/frameRate is refused before it ever reaches gst-launch-1.0's
// argv, where it would fail far less legibly.
func TestFSEQSourceSpecRejectsInvalidGeometry(t *testing.T) {
	cases := []struct {
		name          string
		width, height int
		frameRate     int
	}{
		{"zero width", 0, 32, 40},
		{"zero height", 64, 0, 40},
		{"zero frame rate", 64, 32, 0},
		{"negative frame rate", 64, 32, -1},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if _, err := FSEQSourceSpec("surface-1", c.width, c.height, "rgb", c.frameRate); err == nil {
				t.Fatalf("want error, got nil")
			}
		})
	}
}
