package pipeline

import (
	"os"
	"os/exec"
)

// envGstLaunchOverride names the environment variable an operator can set
// to point at a gst-launch-1.0 binary outside PATH.
const envGstLaunchOverride = "SHOWMESH_GST_LAUNCH"

// lookPathFunc and lookupEnvFunc are package-level vars (matching
// internal/agent/assets.go's assetHTTPClient/readBackAssetFunc convention)
// so tests can prove ResolveGstLaunch actually consults PATH and the
// override, without depending on what is or isn't installed on the test
// machine.
var (
	lookPathFunc  = exec.LookPath
	lookupEnvFunc = os.LookupEnv
)

// ResolveGstLaunch locates the gst-launch-1.0 binary: SHOWMESH_GST_LAUNCH
// when set (used as-is, not re-validated against PATH — an operator who set
// it explicitly gets exactly what they asked for), otherwise "gst-launch-1.0"
// resolved via PATH. ok is false when neither yields a usable path; reason
// then explains why, for [Supervisor]'s "unsupported" degradation — an
// absent binary must never stop the agent (ADR-026 decision 6's rule,
// generalized).
func ResolveGstLaunch() (path string, ok bool, reason string) {
	if override, set := lookupEnvFunc(envGstLaunchOverride); set && override != "" {
		return override, true, ""
	}
	resolved, err := lookPathFunc("gst-launch-1.0")
	if err != nil {
		return "", false, "gst-launch-1.0 not found on PATH and " + envGstLaunchOverride + " is not set: " + err.Error()
	}
	return resolved, true, ""
}
