//go:build integration

package integration

import (
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/showmeshsystems/showmesh/internal/agent/pipeline"
)

// TestSurfaceEditReachesRunningPipeline proves a saved frame-rate change reaches
// the agent's running render assignment without a manual apply or a redeploy.
func TestSurfaceEditReachesRunningPipeline(t *testing.T) {
	requireBroker(t)
	coord, token, _ := startAssetCoordinator(t, t.TempDir(), true)

	showID := "show-" + uniqueSuffix()
	nodeID := "node-" + uniqueSuffix()
	surfaceID := "surface-" + uniqueSuffix()

	mustCtl(t, coord, token, []string{"declare", "--label", "surface edit node"}, nodeID)
	agentDir := writeTempDirHelper(t)
	startAgent(t, agentConfig{nodeID: nodeID, assetDir: agentDir})

	mustCtl(t, coord, token, []string{"show", "set", "--name", "Surface Edit E2E"}, showID)
	mustCtl(t, coord, token, []string{"show", "activate"}, showID)
	surfaceArgs := func(frameRate string) []string {
		return []string{
			"surface", "set", "--show", showID, "--name", "Wall", "--node", nodeID,
			"--start-channel", "1", "--channel-count", "12", "--width", "2", "--height", "2",
			"--pixel-format", "rgb", "--frame-rate", frameRate, "--transport", "hdmi", "--hdmi-display", "HDMI-1",
		}
	}
	mustCtl(t, coord, token, surfaceArgs("40"), surfaceID)

	// Retried because a deploy sent before the agent subscribes waits out its whole deadline.
	waitFor(t, 60*time.Second, time.Second, func() bool {
		code, out, _ := runCtl(t, coord, token, []string{"cuecatalog", "deploy"}, nodeID)
		return code == 0 && strings.Contains(out, ": confirmed ")
	}, "cuecatalog deploy to confirm")
	heldFrameRate := func() float64 {
		assignments, err := pipeline.NewAssignmentStore(agentDir).Load()
		if err != nil {
			return 0
		}
		for _, a := range assignments {
			if a.SurfaceID != surfaceID {
				continue
			}
			var params map[string]any
			if json.Unmarshal(a.RawParams, &params) != nil {
				return 0
			}
			v, _ := params["frameRate"].(float64)
			return v
		}
		return 0
	}
	running := func() bool {
		code, out, _ := runCtl(t, coord, token, []string{"render", "status"}, nodeID)
		return code == 0 && strings.Contains(out, surfaceID) && strings.Contains(out, "running")
	}
	waitFor(t, 15*time.Second, 200*time.Millisecond, func() bool { return running() && heldFrameRate() == 40 },
		"surface to be running at 40 fps after the catalog deploy")

	mustCtl(t, coord, token, surfaceArgs("20"), surfaceID)

	waitFor(t, 15*time.Second, 200*time.Millisecond, func() bool { return running() && heldFrameRate() == 20 },
		"running surface to pick up the edited 20 fps frame rate with no manual apply or redeploy")
}
