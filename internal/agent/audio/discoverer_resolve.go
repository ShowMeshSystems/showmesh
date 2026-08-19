package audio

import (
	"os"
	"os/exec"
)

// envGstDiscovererOverride names the environment variable an operator can
// set to point at a gst-discoverer-1.0 binary outside PATH, matching
// internal/agent/pipeline's SHOWMESH_GST_LAUNCH convention.
const envGstDiscovererOverride = "SHOWMESH_GST_DISCOVERER"

// resolveGstDiscoverer locates the gst-discoverer-1.0 binary: the override
// when set, otherwise PATH. ok is false when neither yields a usable path
// — ruling 5: that is never a fault, only a fallback to a bounded decode.
// A package-level var (matching resolveGstLaunch's own convention) so
// tests can prove the fallback path without depending on what is or is not
// installed on the test host.
var resolveGstDiscoverer = func() (path string, ok bool, reason string) {
	if override, set := os.LookupEnv(envGstDiscovererOverride); set && override != "" {
		return override, true, ""
	}
	resolved, err := exec.LookPath("gst-discoverer-1.0")
	if err != nil {
		return "", false, "gst-discoverer-1.0 not found on PATH and " + envGstDiscovererOverride + " is not set: " + err.Error()
	}
	return resolved, true, ""
}
