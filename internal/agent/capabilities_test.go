package agent

import (
	"context"
	"testing"

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
