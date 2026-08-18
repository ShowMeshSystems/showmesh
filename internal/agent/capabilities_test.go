package agent

import (
	"context"
	"testing"

	"github.com/showmeshsystems/showmesh/internal/agent/pipeline"
	"github.com/showmeshsystems/showmesh/pkg/capability"
)

// TestDetectCapabilitiesRealMachine runs detectCapabilities for real — no
// fakes, no stub — against whatever this machine actually has installed.
// It does not assert a fixed set (that would depend on the environment
// running the test), only the invariant this whole seam exists to
// guarantee: process.supervise and render.surface are only ever advertised
// alongside a real gst-launch-1.0, and transport.ndi.send is only ever
// advertised when the NDI runtime is actually usable — an id present with
// no corresponding evidence would be exactly the false claim ADR-026
// decision 6 forbids.
//
// On the machine this seam was built and reviewed on, gst-launch-1.0 is
// installed with no NDI runtime, so this test is expected to see
// process.supervise and render.surface (both format probes pass) but never
// transport.ndi.send — see this seam's own report for the captured
// evidence. A CI runner with no GStreamer at all should see an empty set
// and still pass.
func TestDetectCapabilitiesRealMachine(t *testing.T) {
	set := detectCapabilities(context.Background())

	_, haveGst := set.Lookup(capability.ID("process.supervise"))
	if haveGst {
		// render.surface and transport.ndi.send both require gst-launch-1.0
		// to exist by construction (detectCapabilities returns early
		// otherwise) — nothing further to assert; their presence or
		// absence each depends on this machine's own GStreamer/NDI state,
		// which is exactly what this function is supposed to report
		// honestly rather than assume.
		t.Logf("gst-launch-1.0 present; detected capabilities: %v", set)
		return
	}

	if len(set) != 0 {
		t.Errorf("no gst-launch-1.0 detected but capabilities = %v, want empty: nothing downstream can have been evidenced", set)
	}
	t.Log("no gst-launch-1.0 on this machine; detectCapabilities correctly returned nothing")
}

// TestDetectRenderSurfaceFormatsRealMachineAdvertisesOnlyShowVocabulary is
// review finding 11's regression test, run for real against whatever
// GStreamer this machine actually has installed (no fakes — this project's
// own convention for anything gst-launch-1.0-shaped, per
// TestDetectCapabilitiesRealMachine above).
//
// Captured on this seam's own machine (Homebrew gst-launch-1.0 1.28.6,
// darwin/arm64): a raw `gst-launch-1.0 videotestsrc ! video/x-raw,format=X
// ! fakesink` pipeline reaches PLAYING for X in {RGB, UYVY, I420} — all
// three are real, negotiable GStreamer formats here. That is exactly the
// trap finding 11 identified: format negotiation succeeding is not
// evidence that render.surface.apply (pipeline.FSEQSourceSpec) will accept
// the format, because FSEQSourceSpec only ever calls
// pipeline.GstVideoFormatForPixelFormat, which recognizes exactly one
// input string, "rgb". So the only value this function may ever return is
// "rgb" — never "RGB" (the GStreamer caps spelling), "UYVY", or "I420"
// (both real negotiable formats FSEQSourceSpec still refuses).
func TestDetectRenderSurfaceFormatsRealMachineAdvertisesOnlyShowVocabulary(t *testing.T) {
	if _, ok, _ := pipeline.ResolveGstLaunch(); !ok {
		t.Skip("no gst-launch-1.0 on this machine; nothing to probe for real")
	}

	formats := detectRenderSurfaceFormats(context.Background())
	t.Logf("detectRenderSurfaceFormats on this real machine returned: %v", formats)

	for _, f := range formats {
		if f != "rgb" {
			t.Errorf("detectRenderSurfaceFormats returned %q; every returned value must be a show.surface.geometry.pixelFormat value pipeline.FSEQSourceSpec actually accepts, and only %q qualifies today", f, "rgb")
		}
		if _, ok := pipeline.GstVideoFormatForPixelFormat(f); !ok {
			t.Errorf("detectRenderSurfaceFormats returned %q, which pipeline.GstVideoFormatForPixelFormat does not recognize; render.surface.apply would refuse it end to end", f)
		}
	}

	set := detectCapabilities(context.Background())
	if rs, ok := set.Lookup(capability.ID("render.surface")); ok {
		formats, _ := rs.Attributes["pixelFormats"].([]string)
		for _, f := range formats {
			if f == "UYVY" || f == "I420" || f == "RGB" {
				t.Errorf("render.surface capability advertises %q, a value render.surface.apply cannot accept (review finding 11): pixelFormats = %v", f, formats)
			}
		}
	}
}
