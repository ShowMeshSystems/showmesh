package previewgateway

import "os"

// Child processes get only these variables, so the gateway token and any
// other secret in the container's environment stay out of them.
var (
	engineEnvKeys = []string{"PATH", "HOME", "TMPDIR", "LANG"}
	gstEnvKeys    = []string{"PATH", "HOME", "TMPDIR", "LANG", "GST_PLUGIN_PATH", "GST_PLUGIN_SYSTEM_PATH", "GST_REGISTRY", "LD_LIBRARY_PATH"}
)

func minimalEnv(keys ...string) []string {
	out := []string{}
	for _, k := range keys {
		if v, ok := os.LookupEnv(k); ok {
			out = append(out, k+"="+v)
		}
	}
	return out
}
