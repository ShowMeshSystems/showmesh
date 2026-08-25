//go:build integration

package integration

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/eclipse/paho.golang/paho"

	"github.com/showmeshsystems/showmesh/pkg/fseq/fseqtest"
	"github.com/showmeshsystems/showmesh/pkg/mqttproto"
	"github.com/showmeshsystems/showmesh/pkg/multisync"
)

// This file is the running-system gate for the owner ruling that a render
// coverage gap must fail visibly: an unmistakable alert field in Program
// Mode, black in Show Mode, black when the node has never been told the
// mode, and a mode flip that changes a LIVE surface with no re-apply.
//
// It proves the OUTPUT BYTES, never a colour anybody claims to have seen:
// the agent's SHOWMESH_GST_LAUNCH points at a capture script that copies
// its stdin to a file, so what is asserted is exactly what the frame writer
// wrote to the pipeline. No physical display is involved.

const (
	// gateSurfacePixels x gateBytesPerPixel is the surface's channel count.
	// A 4-pixel rgb surface keeps a whole frame short enough to paste into
	// a report and still exercises the real per-pixel alert layout.
	gateSurfacePixels   = 4
	gateBytesPerPixel   = 3
	gateChannelCount    = gateSurfacePixels * gateBytesPerPixel
	gateStepTimeMS      = 25
	gateFramesOnDisk    = 10
	gateFramesInHeader  = 1000
	gateSyncSeconds     = 20.0 // frame 800 of the header, far past the data
	gateSurfaceID       = "gate-surface"
	gateFSEQName        = "gate.fseq"
	gateRenderInterval  = "SHOWMESH_RENDER_REPORT_INTERVAL=1s"
	gateCaptureBasename = "frames"
)

// truncatedFSEQ is the induced fault: a real FSEQ whose header claims
// gateFramesInHeader frames while only gateFramesOnDisk frames of channel
// data exist. Frame 0 reads, which is what lets the assignment be accepted,
// and any frame past the data runs off the end of the file, which is the
// timeline running past the end of its own sequence.
func truncatedFSEQ() []byte {
	full := fseqtest.Build(gateChannelCount, gateFramesInHeader, gateStepTimeMS)
	headerLen := len(full) - gateChannelCount*gateFramesInHeader
	return full[:headerLen+gateChannelCount*gateFramesOnDisk]
}

func writeGateFSEQ(t *testing.T, assetDir string) (filename, contentHash string) {
	t.Helper()
	if err := os.MkdirAll(assetDir, 0o755); err != nil {
		t.Fatalf("mkdir asset dir: %v", err)
	}
	data := truncatedFSEQ()
	path := filepath.Join(assetDir, gateFSEQName)
	if err := os.WriteFile(path, data, 0o644); err != nil {
		t.Fatalf("write fseq: %v", err)
	}
	sum := sha256.Sum256(data)
	return gateFSEQName, "sha256:" + hex.EncodeToString(sum[:])
}

// writeCaptureScript writes the stand-in for gst-launch-1.0: it prints the
// exact stdout marker the supervisor watches for to decide a pipeline
// reached PLAYING, then copies every byte the frame writer sends it into
// its own per-process file.
func writeCaptureScript(t *testing.T, dir string) (scriptPath, captureDir string) {
	t.Helper()
	captureDir = filepath.Join(dir, "capture")
	if err := os.MkdirAll(captureDir, 0o755); err != nil {
		t.Fatalf("mkdir capture dir: %v", err)
	}
	scriptPath = filepath.Join(dir, "capture-gst.sh")
	script := "#!/bin/sh\necho \"New clock: capture\"\nexec cat > \"" +
		filepath.Join(captureDir, gateCaptureBasename) + "-$$.bin\"\n"
	if err := os.WriteFile(scriptPath, []byte(script), 0o755); err != nil {
		t.Fatalf("write capture script: %v", err)
	}
	return scriptPath, captureDir
}

// newestCapture returns the most recently modified capture file's path and
// bytes. The frame writer's own pipeline is whichever process is currently
// being fed, so its file is the one still growing.
func newestCapture(t *testing.T, captureDir string) (string, []byte) {
	t.Helper()
	entries, err := os.ReadDir(captureDir)
	if err != nil {
		t.Fatalf("read capture dir: %v", err)
	}
	type cap struct {
		path string
		mod  time.Time
	}
	var caps []cap
	for _, e := range entries {
		info, err := e.Info()
		if err != nil || info.Size() == 0 {
			continue
		}
		caps = append(caps, cap{filepath.Join(captureDir, e.Name()), info.ModTime()})
	}
	if len(caps) == 0 {
		return "", nil
	}
	sort.Slice(caps, func(i, j int) bool { return caps[i].mod.After(caps[j].mod) })
	b, err := os.ReadFile(caps[0].path)
	if err != nil {
		t.Fatalf("read capture file: %v", err)
	}
	return caps[0].path, b
}

// lastFrame is the newest whole frame in a capture: what the surface is
// showing right now.
func lastFrame(b []byte) []byte {
	if len(b) < gateChannelCount {
		return nil
	}
	return b[len(b)-gateChannelCount:]
}

func alertFrameBytes() []byte {
	out := make([]byte, gateChannelCount)
	for i := 0; i < gateChannelCount; i += gateBytesPerPixel {
		out[i] = 0xFF
	}
	return out
}

func blackFrameBytes() []byte { return make([]byte, gateChannelCount) }

func frameEquals(got, want []byte) bool {
	if len(got) != len(want) {
		return false
	}
	for i := range got {
		if got[i] != want[i] {
			return false
		}
	}
	return true
}

// awaitFrame waits until the newest capture file's newest whole frame
// equals want, and returns the capture file path so a caller can prove the
// same pipeline process kept running across a mode flip.
func awaitFrame(t *testing.T, captureDir string, want []byte, what string) string {
	t.Helper()
	var (
		lastPath string
		lastSeen []byte
	)
	deadline := time.Now().Add(30 * time.Second)
	for time.Now().Before(deadline) {
		path, b := newestCapture(t, captureDir)
		if f := lastFrame(b); f != nil {
			lastPath, lastSeen = path, f
			if frameEquals(f, want) {
				t.Logf("%s: capture %s, %d bytes, newest frame % x", what, filepath.Base(path), len(b), f)
				return path
			}
		}
		time.Sleep(100 * time.Millisecond)
	}
	t.Fatalf("%s: never saw frame % x; last capture %s newest frame % x", what, want, lastPath, lastSeen)
	return ""
}

// multiSyncSender drives the node's timeline the way an FPP master does:
// real MultiSync sync packets over UDP, at a position far past the end of
// the sequence's own data.
func startMultiSyncSender(t *testing.T, addr string) {
	t.Helper()
	conn, err := net.Dial("udp", addr)
	if err != nil {
		t.Fatalf("dial multisync %s: %v", addr, err)
	}
	pkt, err := multisync.EncodeSync(multisync.SyncPacket{
		Action:         multisync.SyncActionSync,
		FileType:       multisync.SyncFileTypeSequence,
		FrameNumber:    gateSyncSeconds * 1000 / gateStepTimeMS,
		SecondsElapsed: gateSyncSeconds,
		Filename:       gateFSEQName,
	})
	if err != nil {
		t.Fatalf("EncodeSync: %v", err)
	}
	stop := make(chan struct{})
	var once sync.Once
	t.Cleanup(func() {
		once.Do(func() { close(stop) })
		_ = conn.Close()
	})
	go func() {
		ticker := time.NewTicker(250 * time.Millisecond)
		defer ticker.Stop()
		for {
			select {
			case <-stop:
				return
			case <-ticker.C:
				_, _ = conn.Write(pkt)
			}
		}
	}()
}

func gateApplyParams(filename, contentHash string) map[string]any {
	return map[string]any{
		"surfaceId":       gateSurfaceID,
		"fseqFilename":    filename,
		"fseqContentHash": contentHash,
		"channelRange":    map[string]any{"startChannel": 1, "channelCount": gateChannelCount},
		"geometry":        map[string]any{"width": gateSurfacePixels, "height": 1, "pixelFormat": "rgb"},
		"frameRate":       40,
		"idleOutput":      "black",
	}
}

func setShowMode(t *testing.T, coord *testCoordinator, token, mode string) {
	t.Helper()
	status, body := putRawWithToken(t, coord, "/api/v1/config/show.mode", token, map[string]any{"mode": mode})
	if status != 200 && status != 201 {
		t.Fatalf("PUT /api/v1/config/show.mode %s: status %d, body %s", mode, status, body)
	}
	t.Logf("show mode set to %s: HTTP %d %s", mode, status, strings.TrimSpace(string(body)))
}

// TestRenderCoverageGapFailsVisiblyByMode is gate cases 1, 2 and 3: alert
// in Program Mode, black in Show Mode, and a flip that changes a live
// surface with no re-apply and no new pipeline process.
func TestRenderCoverageGapFailsVisiblyByMode(t *testing.T) {
	dataDir := t.TempDir()
	token := createAdminAndIssueToken(t, dataDir, "gate-admin", "gate-admin-password-1")
	coord := startCoordinatorWithConfig(t, coordinatorConfig{
		dataDir:     dataDir,
		clientID:    "coord-" + uniqueSuffix(),
		bearerToken: token,
	})

	workDir := t.TempDir()
	assetDir := filepath.Join(workDir, "assets")
	filename, contentHash := writeGateFSEQ(t, assetDir)
	scriptPath, captureDir := writeCaptureScript(t, workDir)
	msPort := findFreePort(t)
	msAddr := fmt.Sprintf("127.0.0.1:%d", msPort)

	const nodeID = "gate-node-01"
	agent := startAgent(t, agentConfig{
		nodeID:   nodeID,
		label:    "Gate node",
		assetDir: assetDir,
		extraEnv: []string{
			"SHOWMESH_GST_LAUNCH=" + scriptPath,
			"SHOWMESH_MULTISYNC_LISTEN_ADDR=" + msAddr,
			gateRenderInterval,
		},
	})
	defer agent.sigkill(t)

	setShowMode(t, coord, token, "program")

	cli, w := startCmdClient(t, nodeID)
	defer func() { _ = cli.Disconnect(&paho.Disconnect{ReasonCode: 0}) }()
	awaitAgentReceivingCommands(t, cli, w, nodeID)

	startMultiSyncSender(t, msAddr)

	applyCmd := echoCmd(nodeID, "cmd-gate-apply-1", "idem-gate-apply-1", "")
	applyCmd.Action = "render.surface.apply"
	applyCmd.Params = gateApplyParams(filename, contentHash)
	dispatchCmd(t, cli, nodeID, applyCmd)
	result := waitForResult(t, w, applyCmd.CommandID, 30*time.Second)
	t.Logf("render.surface.apply outcome=%s reason=%q", result.Outcome, result.Reason)
	if result.Outcome != "confirmed" {
		t.Fatalf("render.surface.apply outcome = %q, want confirmed; agent logs:\n%s", result.Outcome, agent.logs.String())
	}

	// --- case 1: Program Mode draws the unmistakable failure output ---
	programCapture := awaitFrame(t, captureDir, alertFrameBytes(), "case 1 program mode")
	requireSurfaceEvidence(t, coord, "failure", "alert")
	logSurfaceEvidence(t, coord, "case 1 program mode")

	// --- case 3: the same LIVE surface changes when the mode flips ---
	setShowMode(t, coord, token, "show")
	showCapture := awaitFrame(t, captureDir, blackFrameBytes(), "case 2 show mode after a live flip")
	if showCapture != programCapture {
		t.Fatalf("the capture file changed from %s to %s across a mode flip: the pipeline process was restarted, so this was not a point-of-decision read",
			filepath.Base(programCapture), filepath.Base(showCapture))
	}
	t.Logf("case 3 live flip: same pipeline process throughout (capture file %s), no re-apply issued", filepath.Base(showCapture))

	// --- case 2: Show Mode still reports a failure, not an idle ---
	requireSurfaceEvidence(t, coord, "failure", "black")
	logSurfaceEvidence(t, coord, "case 2 show mode")

	// And back, so this is a live read rather than a one-way latch.
	setShowMode(t, coord, token, "program")
	backCapture := awaitFrame(t, captureDir, alertFrameBytes(), "case 3 flipped back to program mode")
	if backCapture != programCapture {
		t.Fatalf("capture file changed to %s when flipping back; the pipeline must not restart", filepath.Base(backCapture))
	}

	clearCmd := echoCmd(nodeID, "cmd-gate-clear-1", "idem-gate-clear-1", "")
	clearCmd.Action = "render.surface.clear"
	clearCmd.Params = map[string]any{"surfaceId": gateSurfaceID}
	dispatchCmd(t, cli, nodeID, clearCmd)
	if r := waitForResult(t, w, clearCmd.CommandID, 20*time.Second); r.Outcome != "confirmed" {
		t.Errorf("render.surface.clear outcome = %q, want confirmed", r.Outcome)
	}
}

// surfaceObservationValue pulls one signal's value for the gate surface out
// of the coordinator's own observations endpoint.
func surfaceObservationValue(t *testing.T, coord *testCoordinator, signal string) (string, bool) {
	t.Helper()
	status, body := coord.getRaw(t, "/api/v1/observations?resourceKind=surface&resourceId="+gateSurfaceID)
	if status != 200 {
		t.Fatalf("GET observations: status %d, body %s", status, body)
	}
	var page struct {
		Observations []struct {
			Signal string `json:"signal"`
			Value  any    `json:"value"`
		} `json:"observations"`
	}
	if err := json.Unmarshal(body, &page); err != nil {
		t.Fatalf("decode observations: %v (body %s)", err, body)
	}
	for _, it := range page.Observations {
		if it.Signal == signal {
			s, ok := it.Value.(string)
			return s, ok
		}
	}
	return "", false
}

func logSurfaceEvidence(t *testing.T, coord *testCoordinator, what string) {
	t.Helper()
	status, body := coord.getRaw(t, "/api/v1/observations?resourceKind=surface&resourceId="+gateSurfaceID)
	if status != 200 {
		t.Fatalf("GET observations: status %d", status)
	}
	var page struct {
		Observations []struct {
			Signal string  `json:"signal"`
			Value  any     `json:"value"`
			State  string  `json:"state"`
			Reason *string `json:"reason"`
		} `json:"observations"`
	}
	if err := json.Unmarshal(body, &page); err != nil {
		t.Fatalf("decode observations: %v", err)
	}
	for _, it := range page.Observations {
		if strings.HasPrefix(it.Signal, "surface.output.") || it.Signal == "surface.frames.dropped" {
			reason := ""
			if it.Reason != nil && *it.Reason != "" {
				reason = ", reason: " + *it.Reason
			}
			t.Logf("%s evidence: %s = %v (state %s%s)", what, it.Signal, it.Value, it.State, reason)
		}
	}
}

func requireSurfaceEvidence(t *testing.T, coord *testCoordinator, wantMode, wantFailure string) {
	t.Helper()
	waitFor(t, 20*time.Second, 250*time.Millisecond, func() bool {
		mode, ok := surfaceObservationValue(t, coord, "surface.output.mode")
		if !ok || mode != wantMode {
			return false
		}
		failure, ok := surfaceObservationValue(t, coord, "surface.output.failure")
		return ok && failure == wantFailure
	}, fmt.Sprintf("surface.output.mode=%s with surface.output.failure=%s", wantMode, wantFailure))

	idle, hasIdle := surfaceObservationValue(t, coord, "surface.output.idle_mode")
	if hasIdle && idle != "" {
		t.Fatalf("surface.output.idle_mode = %q during a failure: a failure must not be reported as an idle mode", idle)
	}
}

// TestRenderCoverageGapDrawsBlackWithNoModeEverReceived is gate case 4: a
// node nobody has told the mode reads unknown, and unknown behaves as show,
// so the failure reaches the wall as black rather than as red in front of
// an audience. There is no coordinator in this test at all, and the
// retained mode topic is cleared first, so the node genuinely never hears
// one.
func TestRenderCoverageGapDrawsBlackWithNoModeEverReceived(t *testing.T) {
	clearRetainedShowMode(t)

	workDir := t.TempDir()
	assetDir := filepath.Join(workDir, "assets")
	filename, contentHash := writeGateFSEQ(t, assetDir)
	scriptPath, captureDir := writeCaptureScript(t, workDir)
	msPort := findFreePort(t)
	msAddr := fmt.Sprintf("127.0.0.1:%d", msPort)

	const nodeID = "gate-node-02"
	agent := startAgent(t, agentConfig{
		nodeID:   nodeID,
		label:    "Gate node, no coordinator",
		assetDir: assetDir,
		extraEnv: []string{
			"SHOWMESH_GST_LAUNCH=" + scriptPath,
			"SHOWMESH_MULTISYNC_LISTEN_ADDR=" + msAddr,
			gateRenderInterval,
		},
	})
	defer agent.sigkill(t)

	cli, w := startCmdClient(t, nodeID)
	defer func() { _ = cli.Disconnect(&paho.Disconnect{ReasonCode: 0}) }()
	awaitAgentReceivingCommands(t, cli, w, nodeID)

	startMultiSyncSender(t, msAddr)

	applyCmd := echoCmd(nodeID, "cmd-gate-apply-2", "idem-gate-apply-2", "")
	applyCmd.Action = "render.surface.apply"
	applyCmd.Params = gateApplyParams(filename, contentHash)
	dispatchCmd(t, cli, nodeID, applyCmd)
	if r := waitForResult(t, w, applyCmd.CommandID, 30*time.Second); r.Outcome != "confirmed" {
		t.Fatalf("render.surface.apply outcome = %q, want confirmed; agent logs:\n%s", r.Outcome, agent.logs.String())
	}

	awaitFrame(t, captureDir, blackFrameBytes(), "case 4 no mode ever received")

	report := awaitRenderReport(t, nodeID)
	var surface mqttproto.RenderSurfaceReport
	for _, s := range report.Surfaces {
		if s.SurfaceID == gateSurfaceID {
			surface = s
		}
	}
	t.Logf("case 4 evidence from the node's own retained render report: drawing=%q idleMode=%q failureOutput=%q framesDropped=%d",
		surface.Drawing, surface.IdleMode, surface.FailureOutput, surface.FramesDropped)
	if surface.Drawing != mqttproto.RenderDrawingFailure {
		t.Fatalf("drawing = %q, want %q even though the wall is black", surface.Drawing, mqttproto.RenderDrawingFailure)
	}
	if surface.FailureOutput != mqttproto.RenderFailureOutputBlack {
		t.Fatalf("failureOutput = %q, want %q", surface.FailureOutput, mqttproto.RenderFailureOutputBlack)
	}

	clearCmd := echoCmd(nodeID, "cmd-gate-clear-2", "idem-gate-clear-2", "")
	clearCmd.Action = "render.surface.clear"
	clearCmd.Params = map[string]any{"surfaceId": gateSurfaceID}
	dispatchCmd(t, cli, nodeID, clearCmd)
	if r := waitForResult(t, w, clearCmd.CommandID, 20*time.Second); r.Outcome != "confirmed" {
		t.Errorf("render.surface.clear outcome = %q, want confirmed", r.Outcome)
	}
}

// clearRetainedShowMode removes any retained installation mode a previous
// test's coordinator left on the broker, so a node started afterwards
// really has never been told one.
func clearRetainedShowMode(t *testing.T) {
	t.Helper()
	cli := rawConnect(t, testMQTTCoordinatorUsername, testMQTTCoordinatorPassword)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if _, err := cli.Publish(ctx, &paho.Publish{
		QoS:     1,
		Retain:  true,
		Topic:   mqttproto.ShowModeTopic(),
		Payload: nil,
	}); err != nil {
		t.Fatalf("clear retained show mode: %v", err)
	}
	_ = cli.Disconnect(&paho.Disconnect{ReasonCode: 0})
}

// awaitRenderReport reads the node's own retained render report straight
// off the broker, which is the only evidence surface available with no
// coordinator running, waiting for this file's own gate surface to report
// a failure drawing.
func awaitRenderReport(t *testing.T, nodeID string) mqttproto.RenderPayload {
	t.Helper()
	return awaitRenderReportMatching(t, nodeID, func(s mqttproto.RenderSurfaceReport) bool {
		return s.SurfaceID == gateSurfaceID && s.Drawing == mqttproto.RenderDrawingFailure
	})
}

// awaitRenderReportMatching is awaitRenderReport for any surface condition:
// it returns the first retained render report containing a surface match
// accepts. Parameterized because more than one running-system gate needs
// this same broker read for a different surface and a different condition.
func awaitRenderReportMatching(t *testing.T, nodeID string, match func(mqttproto.RenderSurfaceReport) bool) mqttproto.RenderPayload {
	t.Helper()
	topic, err := mqttproto.ObservedTopic(nodeID, "render")
	if err != nil {
		t.Fatalf("ObservedTopic: %v", err)
	}

	cli := rawConnect(t, testMQTTCoordinatorUsername, testMQTTCoordinatorPassword)
	defer func() { _ = cli.Disconnect(&paho.Disconnect{ReasonCode: 0}) }()

	var (
		mu     sync.Mutex
		latest mqttproto.RenderPayload
		got    bool
	)
	cli.AddOnPublishReceived(func(pr paho.PublishReceived) (bool, error) {
		if pr.Packet == nil || pr.Packet.Topic != topic {
			return false, nil
		}
		var env mqttproto.Envelope
		if err := json.Unmarshal(pr.Packet.Payload, &env); err != nil {
			return true, nil
		}
		p, err := mqttproto.DecodeRenderPayload(env)
		if err != nil {
			return true, nil
		}
		mu.Lock()
		latest, got = p, true
		mu.Unlock()
		return true, nil
	})

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if _, err := cli.Subscribe(ctx, &paho.Subscribe{
		Subscriptions: []paho.SubscribeOptions{{Topic: topic, QoS: 1}},
	}); err != nil {
		t.Fatalf("SUBSCRIBE %s: %v", topic, err)
	}

	deadline := time.Now().Add(20 * time.Second)
	for time.Now().Before(deadline) {
		mu.Lock()
		p, ok := latest, got
		mu.Unlock()
		if ok {
			for _, s := range p.Surfaces {
				if match(s) {
					return p
				}
			}
		}
		time.Sleep(200 * time.Millisecond)
	}
	t.Fatalf("no matching render report ever arrived on %s", topic)
	return mqttproto.RenderPayload{}
}
