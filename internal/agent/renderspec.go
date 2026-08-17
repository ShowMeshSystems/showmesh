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

// buildSurfaceSpec assembles surfaceID's pipeline spec for
// render.surface.apply: [pipeline.DefaultTestPatternSpec]'s source and
// queue stages (source extraction is not this seam's job) with the sink
// stage selected by params.output.transport. Build contract ruling 5's
// queue stays exactly where DefaultTestPatternSpec already puts it, before
// whichever sink is chosen, so the thread boundary is never lost by
// swapping sinks.
//
// An absent or unrecognized output, HDMI (this seam builds no HDMI sink),
// or an NDI output with no sourceName yet all fall back to the same
// fakesink test-pattern spec seam B2a always used, rather than refusing the
// apply outright — a surface not yet fully configured for output still
// gets a runnable diagnostic pipeline.
func buildSurfaceSpec(surfaceID string, params map[string]any) (pipeline.Spec, error) {
	base := pipeline.DefaultTestPatternSpec(surfaceID)

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
