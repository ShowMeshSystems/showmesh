package agent

import (
	"context"

	"github.com/showmeshsystems/showmesh/internal/agent/pipeline"
	"github.com/showmeshsystems/showmesh/pkg/capability"
)

// candidatePixelFormats are the raw video formats [detectRenderSurfaceFormats]
// checks for a real PLAYING transition. Not an exhaustive GStreamer format
// list — the two this project's pipeline spec actually names (see
// pipeline.Spec.PixelFormat's doc comment: UYVY per the B0 zero-conversion
// measurement, I420 as the common fallback).
var candidatePixelFormats = []string{"UYVY", "I420"}

// capabilityDetector is [detectCapabilities], overridable by tests (a
// package-level var, matching this codebase's assetHTTPClient/
// ProcessStarter injection convention) so advertise_test.go can prove
// publishHello's wiring without shelling out to a real gst-launch-1.0 —
// this seam's own detection is proven separately, and for real, by
// capabilities_test.go and the agent run captured in this seam's report.
var capabilityDetector = detectCapabilities

// detectCapabilities probes this node's real GStreamer/NDI state and
// returns exactly the capability set that evidence supports — see
// [pipeline.ProbeNDISend]'s doc comment for why element existence is never
// enough evidence on its own. Called fresh from publishHello before every
// hello publish (every connect, including every reconnect), never cached
// at boot, so a runtime installed after this process started is advertised
// on the node's next broker reconnect with no agent restart required.
//
// Every probe here shells out to a real gst-launch-1.0 subprocess; ctx
// should carry the same deadline publishHello's own caller already bounds
// this whole call by (advertise.go's advertiseTimeout), so a hung probe
// degrades to "nothing detected this pass" rather than blocking hello
// indefinitely — see runProbe's ctx.Done() branch in pipeline/probe.go.
func detectCapabilities(ctx context.Context) capability.Set {
	set := capability.Set{}

	if _, ok, _ := pipeline.ResolveGstLaunch(); !ok {
		// No gst-launch-1.0 at all: every probe below shells out to it, so
		// there is nothing left to evidence.
		return set
	}

	set = append(set, capability.Capability{ID: "process.supervise", Version: 1})

	if formats := detectRenderSurfaceFormats(ctx); len(formats) > 0 {
		set = append(set, capability.Capability{
			ID:         "render.surface",
			Version:    1,
			Attributes: map[string]any{"pixelFormats": formats},
		})
	}

	if probe := pipeline.ProbeNDISend(ctx, nil); probe.Available {
		set = append(set, capability.Capability{ID: "transport.ndi.send", Version: 1})
	}
	// transport.ndi.send is never advertised on a failed probe — support
	// for one transport is never evidence for another (ADR-026 decision 4),
	// and an absent capability is this project's existing vocabulary for
	// "not usable," not a value this function invents a reason string for.

	return set
}

// detectRenderSurfaceFormats runs a real, throwaway videotestsrc ->
// capsfilter -> fakesink pipeline per candidate format and returns exactly
// the formats that reached PLAYING.
func detectRenderSurfaceFormats(ctx context.Context) []string {
	var ok []string
	for _, format := range candidatePixelFormats {
		if pipeline.ProbeVideoFormat(ctx, nil, format).Available {
			ok = append(ok, format)
		}
	}
	return ok
}
