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
// fully configured for output still gets a runnable diagnostic pipeline.
func applyOutputSink(base pipeline.Spec, surfaceID string, params map[string]any) (pipeline.Spec, error) {
	raw, ok := params["output"]
	if !ok {
		return base, nil
	}

	b, err := json.Marshal(raw)
	if err != nil {
		return pipeline.Spec{}, fmt.Errorf("render.surface.apply: re-encoding params.output: %w", err)
	}
	var out surfaceOutputParams
	if err := json.Unmarshal(b, &out); err != nil {
		return pipeline.Spec{}, fmt.Errorf("render.surface.apply: params.output: %w", err)
	}

	if out.Transport != "ndi" || out.NDI == nil || out.NDI.SourceName == "" {
		return base, nil
	}

	stages := make([]pipeline.Stage, 0, len(base.Stages))
	for _, st := range base.Stages {
		if st.Label == "sink" {
			continue
		}
		stages = append(stages, st)
	}
	stages = append(stages, pipeline.NDISinkStage(out.NDI.SourceName))

	return pipeline.Spec{SurfaceID: surfaceID, PixelFormat: base.PixelFormat, Stages: stages}, nil
}
