package agent

import (
	"context"
	"sync"

	"github.com/showmeshsystems/showmesh/internal/agent/pipeline"
	"github.com/showmeshsystems/showmesh/pkg/capability"
)

// candidateShowPixelFormats are the show.surface.geometry.pixelFormat
// vocabulary values [detectRenderSurfaceFormats] checks for a real PLAYING
// transition — not an exhaustive GStreamer format list, and deliberately
// not the same list as gst-launch-1.0's raw-video format registry.
// pipeline.FSEQSourceSpec (the only thing render.surface.apply ever
// builds) only ever calls pipeline.GstVideoFormatForPixelFormat, which
// maps exactly "rgb" and refuses everything else, so this is the
// intersection review finding 11 asked for: what the FSEQ extraction path
// can produce, restricted further to what actually reaches PLAYING on
// this node. Advertising "UYVY" or "I420" here (this package used to)
// measured GStreamer's own format negotiation and published it under a
// name meaning "render.surface.apply will accept this," which it will
// not — GstVideoFormatForPixelFormat has no case for either string.
var candidateShowPixelFormats = []string{"rgb"}

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
// enough evidence on its own. Run fresh on every connect (including every
// reconnect), never cached at boot, so a runtime installed after this
// process started is advertised with no agent restart required.
//
// Every probe here shells out to a real gst-launch-1.0 subprocess and can
// take up to three probeTimeouts (pipeline/probe.go) end to end. Since
// review finding 14, this function is never called on the same context
// publishHello's own publish is bounded by: advertise.go runs it in the
// background, on its own capabilityDetectionTimeout, and republishes hello
// when it finishes, so a hung probe here degrades this pass's advertised
// set rather than risking the hello publish itself. ctx still bounds the
// call — a hang past its deadline still resolves to "nothing detected this
// pass" — see runProbe's ctx.Done() branch in pipeline/probe.go.
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

// detectRenderSurfaceFormats runs [pipeline.ProbeFSEQSourceFormat] per
// candidate show.surface pixel format — the EXACT pipeline
// render.surface.apply builds via [pipeline.FSEQSourceSpec], fdsrc and
// rawvideoparse included — and returns exactly the show-vocabulary values
// that reached PLAYING. This used to probe a standalone
// videotestsrc->capsfilter->fakesink pipeline instead, which measures a
// different GStreamer vocabulary (a caps string, not rawvideoparse's
// "format" property — see spec.go's gstVideoFormat) and, on GStreamer
// 1.24, succeeded while the real FSEQSourceSpec pipeline was rejected at
// construction: a value in the list was not actually evidence the apply
// path worked, contrary to what this comment used to claim. Probing the
// real Spec is what makes the claim true.
func detectRenderSurfaceFormats(ctx context.Context) []string {
	fdsrcIsLive := pipeline.FdsrcSupportsIsLive(nil)
	var ok []string
	for _, showFormat := range candidateShowPixelFormats {
		if pipeline.ProbeFSEQSourceFormat(ctx, nil, showFormat, fdsrcIsLive).Available {
			ok = append(ok, showFormat)
		}
	}
	return ok
}

// detectedCapabilityCache holds the most recent real detectCapabilities
// result, across connects. It exists so that a reconnect's immediate hello
// publish (advertise.go's publishAdvertisement, bounded by
// advertiseTimeout) never has to wait on a fresh detection run to have
// something better than an empty set to advertise — see review finding 14.
// The zero value (have == false) is the honest "detection has not
// completed even once yet" state, distinct from "detection ran and found
// nothing," and callers must not conflate the two.
var detectedCapabilityCache = &capabilityCache{}

type capabilityCache struct {
	mu   sync.Mutex
	set  capability.Set
	have bool
}

// snapshot returns the cache's current contents and whether detection has
// ever completed. Safe for concurrent use with store.
func (c *capabilityCache) snapshot() (capability.Set, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.set, c.have
}

// store replaces the cache's contents with a fresh detection result.
func (c *capabilityCache) store(set capability.Set) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.set = set
	c.have = true
}

// reset clears the cache back to its zero value (have == false). A pointer
// var with a lock-then-mutate reset, rather than a value var reassigned
// wholesale, so nothing ever copies the embedded sync.Mutex — tests use
// this between runs since detectedCapabilityCache is process-lifetime
// state shared across every test in this package.
func (c *capabilityCache) reset() {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.set = nil
	c.have = false
}
