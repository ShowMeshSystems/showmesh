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

// StartDiagnosticSurfaceIfConfigured gives this node its configured
// diagnostic idle output, by whichever of the two routes applies, and does
// nothing at all when no diagnostic surface was configured.
//
// Called AFTER the boot resume, so the two routes divide cleanly. If that
// surface already has a writer, a persisted assignment resumed onto it and
// startFrameWriter already built that writer with the diagnostic idle
// output (renderops.go's idleOutputFor): the surface keeps its assignment,
// still draws content whenever the timeline plays, and draws the diagnostic
// pattern instead of black whenever it is idle. Nothing more to do here.
// Otherwise this node holds no assignment for it and the standalone
// diagnostic surface below is started from nothing.
//
// A failure is logged rather than returned, because this runs at agent
// startup and a diagnostic surface that cannot come up must never stop the
// node coming up around it.
func (o *renderOperations) StartDiagnosticSurfaceIfConfigured(d config.DiagnosticSurface, now func() time.Time) {
	if !d.Enabled() {
		return
	}

	if mode, ok := o.idleOutputOfRunningWriter(d.SurfaceID); ok {
		if mode == pipeline.IdleOutputDiagnostic {
			o.logger.Info("an assignment already holds this node's diagnostic surface; its idle output is the diagnostic pattern",
				"surface_id", d.SurfaceID)
			return
		}
		// The override is applied where a writer is built, so reaching
		// here means one was built for this surface without it: the
		// configuration arrived after the writer, or a caller bypassed
		// idleOutputFor. Never displace the assignment to force it.
		o.logger.Warn("an assignment holds this node's diagnostic surface and its writer was not built with the diagnostic idle output; leaving the assignment alone",
			"surface_id", d.SurfaceID, "idle_output", mode)
		return
	}

	if err := o.StartDiagnosticSurface(d, now); err != nil {
		o.logger.Warn("failed to start the node-local diagnostic surface", "surface_id", d.SurfaceID, "error", err)
		return
	}
	o.logger.Info("started the node-local diagnostic surface",
		"surface_id", d.SurfaceID, "width", d.Width, "height", d.Height, "frame_rate", d.FrameRate)
}

// idleOutputOfRunningWriter reports the idle output surfaceID's current
// frame writer draws, and whether it has one at all.
func (o *renderOperations) idleOutputOfRunningWriter(surfaceID string) (string, bool) {
	o.mu.Lock()
	defer o.mu.Unlock()
	h, ok := o.writers[surfaceID]
	if !ok {
		return "", false
	}
	return h.fw.IdleOutput(), true
}

// StartDiagnosticSurface brings up a standalone diagnostic surface: a
// running pipeline and a frame writer that draws the diagnostic pattern on
// every tick, from nothing but this agent's own process and configuration.
// No assignment, no sequence, no timeline, no held catalog, nothing
// persisted. See TRACK-B-BUILD-CONTRACT.md ruling 3's node-local amendment
// for why it must depend on none of them.
//
// It never displaces a surface that already has a running frame writer;
// that surface is served by the idle-output override instead (see
// [renderOperations.StartDiagnosticSurfaceIfConfigured]).
func (o *renderOperations) StartDiagnosticSurface(d config.DiagnosticSurface, now func() time.Time) error {
	const action = "render diagnostic surface (node-local)"

	// An empty surface id fails this same check, so a node that was never
	// configured for a diagnostic surface and one that was configured
	// wrongly are refused by one rule rather than two.
	if !surfaceIDPattern.MatchString(d.SurfaceID) {
		return fmt.Errorf("%s: surface id %q is not a valid surface identifier", action, d.SurfaceID)
	}
	if o.hasRunningFrameWriter(d.SurfaceID) {
		return fmt.Errorf("%s: surface %q already has a running frame writer; the override applies there instead of a second writer", action, d.SurfaceID)
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
