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
