package agent

import (
	"context"
	"fmt"
	"time"

	"github.com/showmeshsystems/showmesh/internal/agent/config"
	"github.com/showmeshsystems/showmesh/internal/agent/pipeline"
)

// diagnosticPixelFormat is the pixel format the node-local diagnostic
// surface renders in. Fixed rather than configurable: rgb is the only
// format this build can express as a GStreamer raw-video format at all
// (see pipeline.GstVideoFormatForPixelFormat), so an operator-facing knob
// here would offer exactly one legal value and one way to break a
// diagnostic that exists to work when nothing else does.
const diagnosticPixelFormat = "rgb"

// diagnosticBytesPerPixel is diagnosticPixelFormat's stride, stated here
// because pipeline's own bytes-per-pixel table is unexported and the frame
// writer must be sized to exactly what the pipeline spec expects.
const diagnosticBytesPerPixel = 3

// StartDiagnosticSurfaceIfConfigured starts the node-local diagnostic
// surface when this node was configured with one, and does nothing at all
// when it was not: a node nobody asked for a diagnostic surface must not
// invent one and start pushing frames at an output nobody configured.
//
// A failure to start is logged rather than returned, because this runs at
// agent startup and a diagnostic surface that cannot come up must never
// stop the node coming up around it.
func (o *renderOperations) StartDiagnosticSurfaceIfConfigured(d config.DiagnosticSurface, now func() time.Time) {
	if !d.Enabled() {
		return
	}
	if err := o.StartDiagnosticSurface(d, now); err != nil {
		o.logger.Warn("failed to start the node-local diagnostic surface", "surface_id", d.SurfaceID, "error", err)
		return
	}
	o.logger.Info("started the node-local diagnostic surface",
		"surface_id", d.SurfaceID, "width", d.Width, "height", d.Height, "frame_rate", d.FrameRate)
}

// StartDiagnosticSurface brings up this node's locally configured
// diagnostic idle output: a running pipeline and a frame writer that draws
// the diagnostic pattern on every tick, from nothing but this agent's own
// process and configuration.
//
// The owner's ruling on diagnostic idle output is what this exists for.
// Every other path to the diagnostic pattern runs through a coordinator-delivered
// render.surface.apply carrying idleOutput, which needs a broker, a
// coordinator, a resolved asset manifest and an FSEQ file on disk before
// one frame reaches the wall. The operator reaches for the diagnostic
// pattern almost exclusively when those are the things that are down, so
// this path takes none of them: no assignment, no sequence, no timeline, no
// held catalog, nothing persisted to disk.
//
// It refuses rather than overwrites when the surface already has a running
// frame writer, so a persisted assignment resumed at boot (build contract
// ruling 4) keeps the surface it owns and the operator is told why.
func (o *renderOperations) StartDiagnosticSurface(d config.DiagnosticSurface, now func() time.Time) error {
	const action = "render diagnostic surface (node-local)"

	// An empty surface id fails this same check, so a node that was never
	// configured for a diagnostic surface and one that was configured
	// wrongly are refused by one rule rather than two.
	if !surfaceIDPattern.MatchString(d.SurfaceID) {
		return fmt.Errorf("%s: surface id %q is not a valid surface identifier", action, d.SurfaceID)
	}
	if o.hasRunningFrameWriter(d.SurfaceID) {
		return fmt.Errorf("%s: surface %q already has a running frame writer; the diagnostic surface never displaces an assignment", action, d.SurfaceID)
	}

	spec, err := pipeline.FSEQSourceSpec(d.SurfaceID, d.Width, d.Height, diagnosticPixelFormat, d.FrameRate, pipeline.FdsrcSupportsIsLive(nil))
	if err != nil {
		return fmt.Errorf("%s: %w", action, err)
	}
	// The same one applyOutputSink path a real assignment's output takes,
	// so an NDI name configured here and one delivered on an assignment can
	// never build two different sinks. Passing the output block even with
	// an empty source name is deliberate: it makes the fakesink fallback
	// STATED evidence rather than a silent "running" with nothing
	// downstream (ADR-029).
	spec, sinkOutcome, err := applyOutputSink(spec, d.SurfaceID, map[string]any{
		"output": map[string]any{
			"transport": "ndi",
			"ndi":       map[string]any{"sourceName": d.NDISourceName},
		},
	})
	if err != nil {
		return fmt.Errorf("%s: %w", action, err)
	}

	if err := o.sup.Apply(spec); err != nil {
		return fmt.Errorf("%s: %w", action, err)
	}
	o.recordDegradedTransportEvidence(d.SurfaceID, sinkOutcome, now())

	fw, err := pipeline.NewDiagnosticFrameWriter(o.sup, d.SurfaceID, d.Width, d.Height, diagnosticBytesPerPixel, d.FrameRate, o.logger)
	if err != nil {
		return fmt.Errorf("%s: %w", action, err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	go fw.Run(ctx)

	o.mu.Lock()
	o.writers[d.SurfaceID] = &frameWriterHandle{fw: fw, cancel: cancel}
	o.mu.Unlock()
	return nil
}
