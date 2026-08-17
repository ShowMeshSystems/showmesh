package agent

import (
	"encoding/json"
	"fmt"

	"github.com/showmeshsystems/showmesh/internal/agent/pipeline"
)

// surfaceOutputParams mirrors internal/coordinator/config.ShowSurfaceOutput's
// JSON shape (transport, ndi.sourceName, hdmi.display) — this package's own
// independent redeclaration, per this codebase's "each side of a wire
// boundary decodes independently" convention (renderApplyKnownKeys' own doc
// comment already applies this once for the same object).
type surfaceOutputParams struct {
	Transport string `json:"transport"`
	NDI       *struct {
		SourceName string `json:"sourceName"`
	} `json:"ndi"`
	HDMI *struct {
		Display string `json:"display"`
	} `json:"hdmi"`
}

// outputSinkOutcome is applyOutputSink's second return value: whether the
// spec it built ends in a real, transport-backed sink, and — when it does
// not — an actionable reason a caller must surface as evidence rather than
// silently accept. Never invented after the fact: this package's own
// knowledge of which sinks it can actually build is the sole source of
// truth for RealSink, never a probe or a guess.
type outputSinkOutcome struct {
	// Configured is true when params.output was present at all. false
	// means no output was requested yet (e.g. B2a's bare test-pattern
	// apply with no output block), which is not a degradation to report —
	// there was nothing to report evidence about.
	Configured bool

	// Transport is params.output.transport when Configured, else "".
	Transport string

	// RealSink is true only for the one sink this build can actually
	// attach to a real transport (today: ndi with a resolved sourceName).
	// Never true for the fakesink diagnostic fallback.
	RealSink bool

	// Reason is set whenever Configured && !RealSink: why the requested
	// transport could not get a real sink. Empty in every other case.
	Reason string
}

// applyOutputSink swaps base's "sink" stage for the one named by
// params.output.transport, leaving every other stage (including build
// contract ruling 5's queue-before-sink) untouched. base is whichever
// pipeline the caller already built — [pipeline.DefaultTestPatternSpec] for
// a surface with no FSEQ information, or [pipeline.FSEQSourceSpec]'s real
// extraction pipeline for one with — so a real assignment's output choice
// and a diagnostic assignment's go through this one path.
//
// An absent or unrecognized output, HDMI (this seam builds no HDMI sink —
// ADR-026 decision 4: NDI support is never evidence for HDMI support), or
// an NDI output with no sourceName all fall back to base's own fakesink
// unchanged, rather than refusing the apply outright — a surface not yet
// fully configured for output still gets a runnable diagnostic pipeline
// with real debugging value. But that pipeline reaching PLAYING must never
// be confused with a working output: the returned [outputSinkOutcome]
// names the gap, and the caller (renderops.go) is responsible for making
// it visible rather than reporting a plain, silent "running" (ADR-029: an
// action whose effect cannot be observed reports as unconfirmable with a
// reason, never as success).
func applyOutputSink(base pipeline.Spec, surfaceID string, params map[string]any) (pipeline.Spec, outputSinkOutcome, error) {
	raw, ok := params["output"]
	if !ok {
		return base, outputSinkOutcome{}, nil
	}

	b, err := json.Marshal(raw)
	if err != nil {
		return pipeline.Spec{}, outputSinkOutcome{}, fmt.Errorf("render.surface.apply: re-encoding params.output: %w", err)
	}
	var out surfaceOutputParams
	if err := json.Unmarshal(b, &out); err != nil {
		return pipeline.Spec{}, outputSinkOutcome{}, fmt.Errorf("render.surface.apply: params.output: %w", err)
	}

	if out.Transport == "ndi" && out.NDI != nil && out.NDI.SourceName != "" {
		stages := make([]pipeline.Stage, 0, len(base.Stages))
		for _, st := range base.Stages {
			if st.Label == "sink" {
				continue
			}
			stages = append(stages, st)
		}
		stages = append(stages, pipeline.NDISinkStage(out.NDI.SourceName))
		spec := pipeline.Spec{SurfaceID: surfaceID, PixelFormat: base.PixelFormat, Stages: stages}
		return spec, outputSinkOutcome{Configured: true, Transport: out.Transport, RealSink: true}, nil
	}

	reason := degradedOutputReason(out)
	base.OutputDegradedReason = reason
	return base, outputSinkOutcome{Configured: true, Transport: out.Transport, RealSink: false, Reason: reason}, nil
}

// degradedOutputReason names, specifically, why params.output could not
// get a real sink — never a bare "unavailable": the operator reading this
// on a dashboard or a CLI needs to know whether the fix is "install NDI",
// "set a sourceName", or "this build has no HDMI sink yet".
func degradedOutputReason(out surfaceOutputParams) string {
	switch out.Transport {
	case "ndi":
		return "ndi output has no sourceName; the pipeline is running a diagnostic fallback sink and is not sending NDI"
	case "hdmi":
		return "transport hdmi has no output sink in this build (Track B seam B4 implements NDI only); the pipeline is running a diagnostic fallback sink"
	case "":
		return "output.transport is empty; the pipeline is running a diagnostic fallback sink"
	default:
		return fmt.Sprintf("output.transport %q is not recognized; the pipeline is running a diagnostic fallback sink", out.Transport)
	}
}
