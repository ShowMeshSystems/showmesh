//go:build integration

package integration

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"

	v1 "github.com/showmeshsystems/showmesh/internal/coordinator/api/v1"
	"github.com/showmeshsystems/showmesh/internal/coordinator/collector/noderender"
	"github.com/showmeshsystems/showmesh/pkg/fseq/fseqtest"
)

// This file is Track B acceptance criterion 4's own proof (TRACK-B doc:
// "Killing the pipeline underneath the agent is detected, reported, and
// restarted, and the restart is visible as an event rather than silent"),
// following render_dispatch_test.go's real-subprocess pattern exactly: a
// REAL showmesh-coordinator, a REAL showmesh-agent, a REAL gst-launch-1.0
// pipeline (hdmi transport's fakesink+queue fallback — this development
// machine has no NDI runtime, same reasoning render_dispatch_test.go's own
// comment gives), and the real showmeshctl binary. Nothing here dispatches
// render.pipeline.restart: the whole point is a pipeline dying out from
// under the agent with no operator involved at all, which
// inventory.Manager.observeRenderSurfaces (internal/coordinator/inventory/
// inventory.go) turns into a store.EventRecord by diffing each render
// report against its own last-seen baseline. That diff is proven today only
// by unit tests against synthetic reports; this is the first proof against
// a process a real SIGKILL actually reached.
//
// The agent's render report ticker runs at
// SHOWMESH_RENDER_REPORT_INTERVAL (harness_test.go's envRenderReportInterval
// forwards whatever scripts/test-integration.sh exports, 100ms by default)
// rather than the 15s production default, specifically so the supervisor's
// transient "restarting" state — held for defaultRestartPolicy's
// initialBackoff, 500ms — is structurally guaranteed to land in at least one
// report: a 500ms window sampled every 100ms cannot be missed by an
// unlucky phase alignment, so this is a bounded wait on a real condition,
// never a race against a kernel or a scheduler (CLAUDE.md's own "a test can
// be a coin flip" lesson).

// findAgentPipelinePid polls for exactly one gst-launch-1.0 process that is
// a DIRECT child of agentPid, so it can only ever find the surface this
// test itself applied — never an orphaned process from an unrelated run
// (which would have ppid 1, not this agent's pid).
func findAgentPipelinePid(t *testing.T, agentPid int) int {
	t.Helper()
	var pid int
	waitFor(t, 20*time.Second, 100*time.Millisecond, func() bool {
		out, err := exec.Command("pgrep", "-P", strconv.Itoa(agentPid), "gst-launch-1.0").Output()
		if err != nil {
			return false // pgrep exits 1 with no output when nothing matches yet
		}
		fields := strings.Fields(string(out))
		if len(fields) != 1 {
			// This test applies exactly one surface, so exactly one
			// gst-launch-1.0 child is ever expected; zero means not
			// started yet and more than one is a test-construction bug
			// this must not silently paper over.
			return false
		}
		p, err := strconv.Atoi(fields[0])
		if err != nil {
			return false
		}
		pid = p
		return true
	}, fmt.Sprintf("agent subprocess (pid %d) to have exactly one running gst-launch-1.0 child", agentPid))
	return pid
}

// findRenderSignal fetches nodeID's current render evidence and returns
// surfaceID's entry for signal, or ok=false if the node isn't reachable yet
// or has not reported that signal for that surface at all. Tolerant of a
// transient non-200/decode failure so it is safe to call from inside
// waitFor's cond — a real failure that never resolves still surfaces via
// waitFor's own timeout message.
func findRenderSignal(t *testing.T, coord *testCoordinator, nodeID, surfaceID, signal string) (v1.ObservationEntry, bool) {
	t.Helper()
	status, body := coord.getRaw(t, "/api/v1/nodes/"+url.PathEscape(nodeID))
	if status != http.StatusOK {
		return v1.ObservationEntry{}, false
	}
	var resp v1.NodeResponse
	if err := json.Unmarshal(body, &resp); err != nil {
		return v1.ObservationEntry{}, false
	}
	for _, e := range resp.Node.Render {
		if e.Resource.ID == surfaceID && e.Signal == signal {
			return e, true
		}
	}
	return v1.ObservationEntry{}, false
}

// ctlEventsResp mirrors GET /api/v1/events' wire shape (v1.EventsResponse),
// decoded directly from showmeshctl's own --output json, which marshals the
// identical wire types — see assets_test.go's identical convention for why
// this file defines its own copy rather than importing cmd/showmeshctl
// (unexported types in package main) or leaning on the CLI's own struct
// (also unexported).
type ctlEventsResp struct {
	Events []v1.Event `json:"events"`
}

// renderPipelineEventsFor drives "showmeshctl events --output json" (the
// real CLI path, exactly like every other acceptance assertion in this
// package) and returns every category=render_pipeline event recorded
// against surfaceID.
func renderPipelineEventsFor(t *testing.T, coord *testCoordinator, token, surfaceID string) []v1.Event {
	t.Helper()
	out := mustCtl(t, coord, token, []string{"events", "--limit", "500", "--output", "json"})
	var resp ctlEventsResp
	if err := json.Unmarshal([]byte(out), &resp); err != nil {
		t.Fatalf("decode events json: %v\noutput:\n%s", err, out)
	}
	var matches []v1.Event
	for _, e := range resp.Events {
		if e.Category == "render_pipeline" && e.Resource.Kind == "surface" && e.Resource.ID == surfaceID {
			matches = append(matches, e)
		}
	}
	return matches
}

// TestKillMinusNinePipelineIsDetectedReportedRestartedAndEventedEndToEnd is
// Track B acceptance criterion 4, driven against a real killed process
// rather than the render.pipeline.restart command.
func TestKillMinusNinePipelineIsDetectedReportedRestartedAndEventedEndToEnd(t *testing.T) {
	requireBroker(t)
	dataDir := t.TempDir()
	coord, token, _ := startAssetCoordinator(t, dataDir, true)

	showID := "show-" + uniqueSuffix()
	nodeID := "node-" + uniqueSuffix()
	surfaceID := "surface-" + uniqueSuffix()

	mustCtl(t, coord, token, []string{"declare", "--label", "kill test node"}, nodeID)
	agentDir := writeTempDirHelper(t)
	agent := startAgent(t, agentConfig{nodeID: nodeID, assetDir: agentDir})
	agentPid := agent.cmd.Process.Pid

	mustCtl(t, coord, token, []string{"show", "set", "--name", "Kill E2E"}, showID)
	mustCtl(t, coord, token, []string{"show", "activate"}, showID)
	mustCtl(t, coord, token, []string{
		// hdmi, not ndi: no real hdmi sink exists yet (Track B seam B4's
		// scope is NDI only) so applyOutputSink falls back to the same
		// fakesink+queue pipeline render_dispatch_test.go's own
		// TestRenderApplyClearRestartAgainstRealAgent proved reliable —
		// this test needs a genuine long-running gst-launch-1.0 process to
		// kill, not NDI transport reachability.
		"surface", "set", "--show", showID, "--name", "Wall", "--node", nodeID,
		"--start-channel", "1", "--channel-count", "12", "--width", "2", "--height", "2",
		"--pixel-format", "rgb", "--frame-rate", "40", "--transport", "hdmi", "--hdmi-display", "HDMI-1",
	}, surfaceID)

	filePath := writeTempFile(t, "opener.fseq", fseqtest.Build(24, 40, 25))
	uploadNodeAsset(t, coord, token, showID, "opener", "fseq", nodeID, filePath)

	waitFor(t, 20*time.Second, 200*time.Millisecond, func() bool {
		return apiNodeManifestState(t, coord, nodeID) == "ready"
	}, "node asset manifest to become ready so the FSEQ is on the node before apply")

	applyOut := mustCtl(t, coord, token, []string{"render", "apply"}, nodeID, surfaceID, "opener")
	if !strings.Contains(applyOut, "confirmed:") || !strings.Contains(applyOut, `"running"`) {
		t.Fatalf("render apply stdout = %q, want a confirmed running outcome", applyOut)
	}

	// --- negative assertion: no restart event exists yet. The apply above
	// already produced this surface's FIRST render report (renderTrigger
	// fires an immediate publish on every render.* command), which
	// observeRenderSurfaces uses only to establish its baseline — never to
	// emit an event, per that function's own doc comment. An event firing
	// here would mean the coordinator manufactures a restart out of
	// nothing, which is worse than reporting nothing at all. ---
	if pre := renderPipelineEventsFor(t, coord, token, surfaceID); len(pre) != 0 {
		t.Fatalf("render_pipeline events for surface %q before any kill = %+v, want none", surfaceID, pre)
	}

	// --- locate and kill the real gst-launch-1.0 process out from under
	// the agent: a direct child of the agent's own OS process, found by
	// pgrep -P, then SIGKILL'd exactly as an operator's `kill -9` would.
	// No render.pipeline.restart command is ever dispatched. ---
	pipelinePid := findAgentPipelinePid(t, agentPid)
	killDispatch := time.Now()
	if err := syscall.Kill(pipelinePid, syscall.SIGKILL); err != nil {
		t.Fatalf("SIGKILL gst-launch-1.0 pid %d: %v", pipelinePid, err)
	}

	// --- (1) detected + (2) reported: the supervisor observes the death
	// and the agent's next render report (well within the 100ms test
	// cadence) carries surface.pipeline.state = "restarting" — a state
	// distinct from "running", never silently unreported. ---
	waitFor(t, 15*time.Second, 50*time.Millisecond, func() bool {
		e, ok := findRenderSignal(t, coord, nodeID, surfaceID, string(noderender.SignalSurfacePipelineState))
		if !ok {
			return false
		}
		v, _ := e.Value.(string)
		return v == "restarting"
	}, "surface.pipeline.state to report \"restarting\" after the real pipeline was killed")

	// --- (2) reported, continued: restart_count moved forward. ---
	var restartCountAfter float64
	waitFor(t, 20*time.Second, 50*time.Millisecond, func() bool {
		e, ok := findRenderSignal(t, coord, nodeID, surfaceID, string(noderender.SignalSurfaceRestartCount))
		if !ok {
			return false
		}
		n, ok := e.Value.(float64)
		if !ok || n < 1 {
			return false
		}
		restartCountAfter = n
		return true
	}, "surface.pipeline.restart_count to report >= 1 after the kill")
	if restartCountAfter < 1 {
		t.Fatalf("restart count after kill = %v, want >= 1", restartCountAfter)
	}

	// --- (3) restarted: a fresh process comes up and the surface returns
	// to running, with evidence dated strictly after the kill — the
	// project's own standing rule (ADR-003, and Step 7's 179-microsecond
	// lesson) that confirmation must be evidence which POST-DATES the
	// event it confirms, not a pre-kill "running" reading replayed. ---
	var runningAfter v1.ObservationEntry
	waitFor(t, 20*time.Second, 50*time.Millisecond, func() bool {
		e, ok := findRenderSignal(t, coord, nodeID, surfaceID, string(noderender.SignalSurfacePipelineState))
		if !ok {
			return false
		}
		v, _ := e.Value.(string)
		if v != "running" {
			return false
		}
		if e.ObservedAt == nil {
			return false
		}
		observedAt, err := time.Parse(time.RFC3339, *e.ObservedAt)
		if err != nil {
			return false
		}
		if !observedAt.After(killDispatch) {
			return false
		}
		runningAfter = e
		return true
	}, "surface.pipeline.state to return to \"running\" with evidence dated after the kill")
	if runningAfter.State != "current" && runningAfter.State != "stale" {
		t.Fatalf("post-restart running evidence state = %q, want current/stale (real evidence, not absence)", runningAfter.State)
	}

	// A fresh gst-launch-1.0 process must actually exist (a distinct PID
	// from the one just killed) — the wire evidence above proves the agent
	// SAYS it restarted; this confirms the OS agrees.
	newPid := findAgentPipelinePid(t, agentPid)
	if newPid == pipelinePid {
		t.Fatalf("gst-launch-1.0 pid after restart = %d, same as the killed pid %d: no new process was actually started", newPid, pipelinePid)
	}

	// --- (4) visible as an event, through the real API and the real CLI,
	// never by reading SQLite. ---
	events := renderPipelineEventsFor(t, coord, token, surfaceID)
	if len(events) == 0 {
		t.Fatalf("no render_pipeline events recorded for surface %q after a real kill -9 and restart", surfaceID)
	}
	found := false
	for _, e := range events {
		if e.Resource.Kind != "surface" {
			t.Fatalf("event resource kind = %q, want \"surface\" (ADR-026: the surface is the observed thing, not the node)", e.Resource.Kind)
		}
		if e.Resource.ID != surfaceID {
			t.Fatalf("event resource id = %q, want %q", e.Resource.ID, surfaceID)
		}
		if e.Severity != "warning" {
			// A plain restart (not a transition into the failed lockout)
			// is "warning" severity per appendRenderEvent's own call site.
			continue
		}
		gotNodeID, _ := e.Details["nodeId"].(string)
		gotRestartCount, _ := e.Details["restartCount"].(float64)
		if gotNodeID == nodeID && gotRestartCount >= 1 {
			found = true
		}
	}
	if !found {
		t.Fatalf("no warning-severity render_pipeline event for surface %q named node %q with restartCount >= 1; events: %+v",
			surfaceID, nodeID, events)
	}

	// The CLI's own "render status" (non-JSON) surface too, exactly as an
	// operator with no UI would see it: the same evidence, in the human
	// path, not only the JSON one.
	statusOut := mustCtl(t, coord, token, []string{"render", "status"}, nodeID)
	if !strings.Contains(statusOut, surfaceID) || !strings.Contains(statusOut, "running") {
		t.Fatalf("render status after kill+restart = %q, want it to report surface %q running again", statusOut, surfaceID)
	}
}

// writeTempDirHelper creates and returns a fresh empty directory under
// t.TempDir(), matching declareAndStartAgent's own agentDir setup — needed
// here as a standalone step (rather than declareAndStartAgent itself)
// because this test needs the *testAgent handle declareAndStartAgent
// discards, in order to read its real OS pid.
func writeTempDirHelper(t *testing.T) string {
	t.Helper()
	dir := filepath.Join(t.TempDir(), "assets")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatalf("mkdir agent asset dir %s: %v", dir, err)
	}
	return dir
}
