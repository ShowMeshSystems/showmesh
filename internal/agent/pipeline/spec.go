// Package pipeline supervises one gst-launch-1.0 child process per surface
// assignment (ADR-007 ruling 1: a supervised subprocess, never go-gst or any
// in-process GStreamer binding, so CGO_ENABLED=0 stays true for the agent).
// This package builds no frame, extracts no channel data, and touches no
// pixel: it starts, watches, restarts and reports on a process whose argv it
// assembled from a structured [Spec].
package pipeline

import (
	"fmt"
	"strings"
)

// Property is one gst-launch element property ("key=value" on the argv
// line). Kept as an ordered slice on [Element], not a map, so the built
// argv is deterministic run to run — a map's iteration order is not, which
// would make every test assert against a moving target and would make a
// captured argv un-diffable across runs.
type Property struct {
	Key   string
	Value string
}

// Element is one gst-launch element: a factory name, an optional element
// name (gst-launch's "name=" — set when a later stage or a report needs to
// refer back to this element), and its properties in argv order.
type Element struct {
	Factory    string
	Name       string
	Properties []Property
}

// Stage is one labelled link in the pipeline chain. Label is never emitted
// to argv; it exists so the supervisor and its reports can attribute cost
// or failure to a named part of the pipeline ("source", "queue", "encode",
// "sink") without parsing the argv back apart. A [Spec] is a sequence of
// Stages chained with "!"; a Stage with more than one Element chains those
// elements with "!" too, so an inter-stage queue is expressed as its own
// single-element Stage rather than as a special case.
type Stage struct {
	Label    string
	Elements []Element
}

// Spec structurally describes one surface's pipeline. It is never a
// pre-joined command string: B3 inserts a source Stage, B4 inserts a sink
// Stage, and neither should need string surgery on the other's output.
//
// Thread boundaries are explicit and chosen, not implied. The B0 spike
// measured a pipeline with NO queue encoding at 86% of one core with the
// rest of the machine idle — the ceiling GStreamer hits here is per-core,
// not per-machine — so a Stage whose Elements is a single "queue" element
// is how this Spec marks a deliberate thread handoff, and [DefaultTestPatternSpec]
// carries one before its sink so that discipline exists from this seam
// onward rather than being retrofitted once B4 needs it.
type Spec struct {
	// SurfaceID is the show.surface config object id this pipeline renders.
	SurfaceID string

	// PixelFormat is the raw video format this pipeline's stages should
	// negotiate (e.g. "UYVY", "I420"), or empty for unconstrained caps. It
	// is an explicit field, never hardcoded to RGB anywhere in this
	// package: the B0 spike's 86%-of-one-core figure was measured on a
	// zero-conversion path, so which format (and where, if anywhere, a
	// conversion or scale happens) is a placement decision left to whoever
	// builds the stage that needs it, not a default buried in argv
	// construction.
	PixelFormat string

	// Stages is the ordered, "!"-chained pipeline. See the Stage and
	// [DefaultTestPatternSpec] doc comments.
	Stages []Stage

	// OutputDegradedReason is "" when Stages' sink is a real, transport-
	// backed output; non-empty when the caller (internal/agent/renderspec.go's
	// applyOutputSink) fell back to a diagnostic fakesink because the
	// requested output.transport has no real sink in this build, or was
	// misconfigured (e.g. an ndi output with no sourceName). The runner
	// carries this into every "running" state report for as long as this
	// spec stays applied, INCLUDING across a restart — see
	// setRunningIfCurrent. ADR-029: an action whose effect cannot be
	// observed reports as unconfirmable with a reason, never as plain
	// success, so a pipeline that genuinely reaches PLAYING with nothing
	// downstream able to see its output must still say so.
	OutputDegradedReason string
}

// CapsFilterStage builds a single-element "queue"-shaped Stage wrapping a
// capsfilter for format, so a caller that needs to pin PixelFormat at a
// specific point in the chain (B3/B4's job, not this seam's) can do so
// without hand-building the element. Returns a zero Stage (no Elements) when
// format is empty, since an empty caps string is not a valid capsfilter
// property and "no constraint" should mean "no stage," not "a stage with an
// empty caps string."
func CapsFilterStage(label, format string) Stage {
	if format == "" {
		return Stage{Label: label}
	}
	return Stage{
		Label: label,
		Elements: []Element{{
			Factory: "capsfilter",
			Properties: []Property{
				{Key: "caps", Value: "video/x-raw,format=" + format},
			},
		}},
	}
}

// QueueStage builds a single-element Stage wrapping a "queue" element,
// marking a deliberate thread boundary between the Stages on either side of
// it. maxSizeBuffers and leaky are exposed as parameters rather than
// constants because the right sizing is a per-placement measurement (see
// the B0 amendment referenced on [Spec]), not a value this package can
// justify picking once for every caller.
func QueueStage(label string, maxSizeBuffers int, leaky string) Stage {
	props := []Property{}
	if maxSizeBuffers > 0 {
		props = append(props, Property{Key: "max-size-buffers", Value: fmt.Sprintf("%d", maxSizeBuffers)})
	}
	if leaky != "" {
		props = append(props, Property{Key: "leaky", Value: leaky})
	}
	return Stage{
		Label:    label,
		Elements: []Element{{Factory: "queue", Name: label, Properties: props}},
	}
}

// defaultQueueMaxSizeBuffers and defaultQueueLeaky are
// SHOWMESH HYPOTHESIS, NOT MEASURED: the B0 spike confirmed a queue belongs
// between the encode/render stage and the sink, but never measured a queue
// depth. A small, leaky-downstream queue decouples this seam's two stages
// (videotestsrc and fakesink both run in-process with no external I/O) without
// letting an unbounded queue mask a stalled sink; B3/B4 own re-measuring this
// once real content and a real sink are in the pipeline.
const (
	defaultQueueMaxSizeBuffers = 4
	defaultQueueLeaky          = "downstream"
)

// DefaultTestPatternSpec builds the B2a pipeline: videotestsrc, a queue
// marking the thread boundary before the sink, and fakesink. Per the seam's
// scope, the source and sink stages are the only two stages a later seam
// (B3 replaces the source, B4 replaces the sink) should ever need to swap;
// the queue between them is deliberately part of every Spec this package
// builds, not something bolted on later.
func DefaultTestPatternSpec(surfaceID string) Spec {
	return Spec{
		SurfaceID: surfaceID,
		Stages: []Stage{
			{
				Label: "source",
				Elements: []Element{{
					Factory: "videotestsrc",
					Properties: []Property{
						{Key: "is-live", Value: "true"},
					},
				}},
			},
			QueueStage("queue-sink", defaultQueueMaxSizeBuffers, defaultQueueLeaky),
			{
				Label: "sink",
				Elements: []Element{{
					Factory: "fakesink",
					Properties: []Property{
						{Key: "sync", Value: "false"},
					},
				}},
			},
		},
	}
}

// gstVideoFormat carries both GStreamer spellings of one raw-video format:
// GStreamer names a format two different ways depending on where it is
// used. A caps string (video/x-raw,format=...) wants the upper-case
// GstVideoFormat enum name; an element property of GstVideoFormat type
// wants the enum's lower-case nick. MEASURED, ubuntu:24.04's
// gst-launch-1.0 1.24.2: rawvideoparse's "format" property rejects
// format=RGB ("could not set property") and accepts format=rgb; 1.28.6
// tolerates both, which is why this went undetected until CI ran on an
// older GStreamer. bytesPerPixel is carried alongside for
// [ProbeFSEQSourceFormat], which needs to hand the real pipeline a
// correctly sized dummy frame. Every format this package supports is
// listed exactly once here — adding one (e.g. rgbw) means filling in all
// three fields, not picking one spelling and hoping it is also the other.
type gstVideoFormat struct {
	caps          string
	propertyNick  string
	bytesPerPixel int
}

var gstVideoFormatsByPixelFormat = map[string]gstVideoFormat{
	"rgb": {caps: "RGB", propertyNick: "rgb", bytesPerPixel: 3},
}

// GstVideoFormatForPixelFormat maps a show.surface.geometry.pixelFormat
// value (config.ShowSurfacePixelFormatRGB / ...RGBW) to a GStreamer
// video/x-raw caps "format" string, for [CapsFilterStage] and
// [ProbeVideoFormat]'s caps-based probe. ok is false for an unrecognized
// input, including rgbw — GStreamer's raw-video format registry has no
// packed 4-byte-per-pixel RGBW format, so B3 does not invent a mapping for
// it; see FSEQSourceSpec's doc comment for the consequence.
//
// SHOWMESH HYPOTHESIS, NOT MEASURED: "RGB" is chosen because it is a
// direct, unconverted byte-for-byte copy of what an rgb-format FSEQ frame
// already contains (RES-017: a channel is one byte, and rgb order is
// channel order) — matching ADR-040 decision 3's "emit the sink's native
// format where possible" guidance is a placement decision for whoever adds
// the sink stage (B4), not this function's job.
func GstVideoFormatForPixelFormat(pixelFormat string) (format string, ok bool) {
	m, ok := gstVideoFormatsByPixelFormat[pixelFormat]
	return m.caps, ok
}

// gstVideoFormatPropertyNickForPixelFormat maps a
// show.surface.geometry.pixelFormat value to the lower-case nick
// rawvideoparse's GstVideoFormat-typed "format" property wants — see
// [gstVideoFormat]'s doc comment for why this is not [GstVideoFormatForPixelFormat].
// Unexported: FSEQSourceSpec is the only caller that ever emits this
// property, and [ProbeFSEQSourceFormat] reaches it by building the same
// Spec rather than by calling this function a second time, so a probe can
// never drift from what the real pipeline sends.
func gstVideoFormatPropertyNickForPixelFormat(pixelFormat string) (nick string, ok bool) {
	m, ok := gstVideoFormatsByPixelFormat[pixelFormat]
	return m.propertyNick, ok
}

// gstBytesPerPixelForPixelFormat is how many bytes one pixel occupies in the
// tightly packed buffer the frame writer produces, for pinning
// rawvideoparse's stride.
func gstBytesPerPixelForPixelFormat(pixelFormat string) (n int, ok bool) {
	m, ok := gstVideoFormatsByPixelFormat[pixelFormat]
	return m.bytesPerPixel, ok
}

// FSEQSourceSpec builds a surface's real pipeline: a source stage reading
// raw frame buffers from this process's own stdin (fed by B3's frame
// writer — see frame.go) instead of B2a's videotestsrc, chained through the
// same queue-before-sink discipline [DefaultTestPatternSpec] established,
// into a placeholder fakesink (B4 replaces this with the real NDI sink
// stage; see build contract seam B4).
//
// width/height/pixelFormat/frameRate come from the surface's own
// show.surface.geometry and .frameRate — this function performs no
// scaling or conversion of its own (ADR-040 decision 2: ShowMesh may
// locate/decompress/copy bytes, never scale or convert), so the buffer
// this pipeline receives on stdin must already be exactly
// width*height*channelsPerPixel(pixelFormat) bytes, raw, per frame.
//
// Returns an error for a pixel format this package cannot express as a
// GStreamer raw-video format (see [GstVideoFormatForPixelFormat]) — rgbw,
// today — rather than silently guessing one.
//
// fdsrcIsLive is the caller's own evidence (see [FdsrcSupportsIsLive]) for
// whether this node's GStreamer accepts fdsrc's is-live property; this
// function never probes for it itself, so a unit test can exercise both
// argv shapes with no gst-launch-1.0 involved.
func FSEQSourceSpec(surfaceID string, width, height int, pixelFormat string, frameRate int, fdsrcIsLive bool) (Spec, error) {
	gstFormat, ok := GstVideoFormatForPixelFormat(pixelFormat)
	if !ok {
		return Spec{}, fmt.Errorf("pipeline: surface %q: pixel format %q has no known GStreamer raw-video mapping", surfaceID, pixelFormat)
	}
	formatNick, ok := gstVideoFormatPropertyNickForPixelFormat(pixelFormat)
	if !ok {
		// Unreachable while both maps are built from the same table above,
		// but this function never trusts that invariant silently — see the
		// identical guard's reasoning in detectRenderSurfaceFormats.
		return Spec{}, fmt.Errorf("pipeline: surface %q: pixel format %q has no known GStreamer property-nick mapping", surfaceID, pixelFormat)
	}
	bytesPerPixel, ok := gstBytesPerPixelForPixelFormat(pixelFormat)
	if !ok {
		return Spec{}, fmt.Errorf("pipeline: surface %q: pixel format %q has no known bytes-per-pixel", surfaceID, pixelFormat)
	}
	if width < 1 || height < 1 {
		return Spec{}, fmt.Errorf("pipeline: surface %q: geometry %dx%d is invalid", surfaceID, width, height)
	}
	if frameRate < 1 {
		return Spec{}, fmt.Errorf("pipeline: surface %q: frameRate %d is invalid", surfaceID, frameRate)
	}

	fdsrcProps := []Property{{Key: "fd", Value: "0"}}
	if fdsrcIsLive {
		fdsrcProps = append(fdsrcProps, Property{Key: "is-live", Value: "true"})
	}

	return Spec{
		SurfaceID:   surfaceID,
		PixelFormat: gstFormat,
		Stages: []Stage{
			{
				Label: "source",
				Elements: []Element{
					{
						// fdsrc reading fd 0 (this process's own stdin) is
						// how frames arrive without linking appsrc/go-gst
						// (build contract ruling 1 — a supervised
						// subprocess, no in-process GStreamer binding).
						//
						// fdsrc's is-live property does not exist before
						// GStreamer 1.26 (MEASURED: gst-launch-1.0 1.24.2
						// rejects the whole pipeline at construction, never
						// a state-change failure) — see fdsrcIsLive's doc
						// comment. Without it, fdsrc genuinely PREROLLs
						// (blocks PAUSED until one buffer reaches the
						// sink), which completes as soon as the writer's
						// first frame arrives; every caller in this
						// codebase starts that writer immediately after
						// applying this spec, so the wait is one frame
						// period, not indefinite.
						Factory:    "fdsrc",
						Properties: fdsrcProps,
					},
					{
						// rawvideoparse turns the undelimited byte stream
						// on stdin into framed video/x-raw buffers at
						// exactly this surface's geometry; it does no
						// scaling or conversion of its own.
						//
						// "format" below is an element PROPERTY, not a caps
						// string, so it takes formatNick (the lower-case
						// GstVideoFormat nick) rather than gstFormat -- see
						// gstVideoFormat above for the measured GStreamer
						// 1.24.2 rejection this distinction fixes.
						Factory: "rawvideoparse",
						Properties: []Property{
							{Key: "width", Value: fmt.Sprintf("%d", width)},
							{Key: "height", Value: fmt.Sprintf("%d", height)},
							{Key: "format", Value: formatNick},
							{Key: "framerate", Value: fmt.Sprintf("%d/1", frameRate)},
							// GStreamer pads a raw video row to a 4-byte
							// boundary by default; the frame writer emits
							// rows packed tight. Any width whose row is not
							// already 4-aligned (250x3 = 750, off by 2)
							// otherwise shears the picture diagonally and
							// rotates the colour channels once per row.
							{Key: "plane-strides", Value: fmt.Sprintf("<%d>", width*bytesPerPixel)},
						},
					},
				},
			},
			QueueStage("queue-sink", defaultQueueMaxSizeBuffers, defaultQueueLeaky),
			{
				Label: "sink",
				Elements: []Element{{
					Factory: "fakesink",
					Properties: []Property{
						{Key: "sync", Value: "false"},
					},
				}},
			},
		},
	}, nil
}

// BuildArgv turns Spec into gst-launch-1.0's argv (excluding the binary
// itself), one string per token, joined by gst-launch's own "!" link
// syntax between elements. An empty Stage (see [CapsFilterStage]'s
// zero-format case) contributes no element and no extra "!".
func (s Spec) BuildArgv() ([]string, error) {
	if len(s.Stages) == 0 {
		return nil, fmt.Errorf("pipeline: spec for surface %q has no stages", s.SurfaceID)
	}

	var argv []string
	elementCount := 0
	for _, stage := range s.Stages {
		for _, el := range stage.Elements {
			if el.Factory == "" {
				return nil, fmt.Errorf("pipeline: spec for surface %q has an element with no factory in stage %q", s.SurfaceID, stage.Label)
			}
			if elementCount > 0 {
				argv = append(argv, "!")
			}
			argv = append(argv, el.Factory)
			if el.Name != "" {
				argv = append(argv, "name="+el.Name)
			}
			for _, p := range el.Properties {
				argv = append(argv, p.Key+"="+p.Value)
			}
			elementCount++
		}
	}
	if elementCount == 0 {
		return nil, fmt.Errorf("pipeline: spec for surface %q has stages but no elements", s.SurfaceID)
	}
	return argv, nil
}

// String renders the argv as one space-joined line, for logging only —
// never re-parsed, never fed back into anything that executes it.
func (s Spec) String() string {
	argv, err := s.BuildArgv()
	if err != nil {
		return fmt.Sprintf("<invalid spec: %v>", err)
	}
	return strings.Join(argv, " ")
}
