package coordinator

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/showmeshsystems/showmesh/internal/coordinator/api"
	"github.com/showmeshsystems/showmesh/internal/coordinator/broker"
	"github.com/showmeshsystems/showmesh/internal/coordinator/collector/audioalignment"
	"github.com/showmeshsystems/showmesh/internal/coordinator/collector/nodeaudio"
	"github.com/showmeshsystems/showmesh/internal/coordinator/identity"
	"github.com/showmeshsystems/showmesh/internal/coordinator/inventory"
	"github.com/showmeshsystems/showmesh/internal/coordinator/store"
	"github.com/showmeshsystems/showmesh/pkg/mqttproto"
)

// TestAlignmentRunBenchProof is compressed: 30 synthetic samples, 1s
// apart, offset rising 2 ms/sample, through the real ingest path, then
// read back through the real HTTP API and the real showmeshctl binary.
func TestAlignmentRunBenchProof(t *testing.T) {
	ctx := context.Background()
	logger := discardLogger()

	st, err := store.Open(ctx, t.TempDir(), logger)
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(func() { _ = st.Close() })

	// The exact wiring coordinator.go's own New uses: audioStore is the
	// live push cache, alignmentRecorder wraps it so a sample lands on
	// the node's active run without audioStore itself knowing about runs.
	audioStore := nodeaudio.NewStore()
	alignmentRecorder := audioalignment.NewRecorder(audioStore, st, logger)
	inv := inventory.New(st, logger, inventory.WithAudioSink(alignmentRecorder))

	svc := identity.NewService(st, time.Now, t.TempDir(), identity.WithLogger(logger))
	admin, err := svc.CreatePrincipal(ctx, "bench-admin", identity.KindHuman, identity.RoleAdmin, "not-a-real-secret-01")
	if err != nil {
		t.Fatalf("create principal: %v", err)
	}
	tok, err := svc.IssueToken(ctx, admin.ID, "bench-test", nil)
	if err != nil {
		t.Fatalf("issue token: %v", err)
	}

	apiInst := api.New(api.Dependencies{Identity: svc, AlignmentRuns: st}, api.Options{Logger: logger})
	server := httptest.NewServer(apiInst.Handler)
	t.Cleanup(server.Close)

	const nodeID = "bench-node-01"
	client := &benchHTTPClient{base: server.URL, token: tok.Value}

	// Start the run through the real HTTP API, exactly as an operator
	// would.
	var startResp struct {
		Run struct {
			ID string `json:"id"`
		} `json:"run"`
	}
	client.post(t, "/api/v1/nodes/"+nodeID+"/audio/alignment-runs", nil, &startResp)
	runID := startResp.Run.ID
	if runID == "" {
		t.Fatalf("start response carried no run id")
	}

	// Feed 30 samples, 1s apart, offset rising 2 ms/sample, through the
	// REAL decode path: inventory.Manager.HandleMessage, exactly what a
	// live MQTT delivery would call.
	base := time.Date(2026, 9, 11, 0, 0, 0, 0, time.UTC)
	const sampleCount = 30
	const stepMs = 2.0
	for i := 0; i < sampleCount; i++ {
		sampledAt := base.Add(time.Duration(i) * time.Second)
		payload := mqttproto.AudioPayload{
			EngineAvailable:    true,
			HardwareEnumerated: true,
			DeviceAvailable:    true,
			OutputsCount:       1,
			ProgramAvailable:   true,
			LTCAvailable:       false,
			LTCReason:          "no route achieved 3 or more channels",
			Routes: []mqttproto.AudioRouteReport{
				{Device: "hw:CARD=PCH,DEV=0", Available: true, Channels: 2, Rate: 48000, Format: "S16LE"},
			},
			DiscoveredAt:       &sampledAt,
			ObservedAt:         &sampledAt,
			Sessions:           []mqttproto.AudioSessionReport{},
			LTCGeneratorState:  "stopped",
			LTCGeneratorReason: "no generator has ever been started on this node",
			AlignmentMeasured:  true,
			AlignmentOffsetMs:  int64(float64(i) * stepMs),
			AlignmentSampledAt: &sampledAt,
			AlignmentSessionID: "bench-session",
		}
		env, err := mqttproto.NewAudioEnvelope(func() time.Time { return sampledAt }, nodeID, payload)
		if err != nil {
			t.Fatalf("build audio envelope %d: %v", i, err)
		}
		raw, err := json.Marshal(env)
		if err != nil {
			t.Fatalf("marshal envelope %d: %v", i, err)
		}
		topic, err := mqttproto.ObservedTopic(nodeID, "audio")
		if err != nil {
			t.Fatalf("audio topic: %v", err)
		}
		inv.HandleMessage(broker.Message{Topic: topic, Payload: raw, Retained: false})
	}

	// Stop the run through the real HTTP API.
	var stopResp struct {
		Run struct {
			StoppedAt *string `json:"stoppedAt"`
		} `json:"run"`
	}
	client.post(t, "/api/v1/nodes/"+nodeID+"/audio/alignment-runs/"+runID+"/stop",
		map[string]string{"reason": "bench proof complete"}, &stopResp)
	if stopResp.Run.StoppedAt == nil {
		t.Fatalf("run did not report stoppedAt after stop")
	}

	// Read the run back through the real HTTP API.
	var getResp struct {
		Summary struct {
			SampleCount          int      `json:"sampleCount"`
			MaxExcursionOffsetMs *float64 `json:"maxExcursionOffsetMs"`
			DriftRateMsPerHour   *float64 `json:"driftRateMsPerHour"`
		} `json:"summary"`
	}
	client.get(t, "/api/v1/nodes/"+nodeID+"/audio/alignment-runs/"+runID, &getResp)

	wantMaxExcursion := float64(sampleCount-1) * stepMs
	wantRate := stepMs * 3600 // 2 ms/s -> 7200 ms/hour

	if getResp.Summary.SampleCount != sampleCount {
		t.Fatalf("HTTP API sampleCount = %d, want %d", getResp.Summary.SampleCount, sampleCount)
	}
	if getResp.Summary.MaxExcursionOffsetMs == nil || *getResp.Summary.MaxExcursionOffsetMs != wantMaxExcursion {
		t.Fatalf("HTTP API maxExcursionOffsetMs = %v, want %v", getResp.Summary.MaxExcursionOffsetMs, wantMaxExcursion)
	}
	if getResp.Summary.DriftRateMsPerHour == nil {
		t.Fatalf("HTTP API driftRateMsPerHour = nil, want ~%v", wantRate)
	}
	if diff := *getResp.Summary.DriftRateMsPerHour - wantRate; diff < -1 || diff > 1 {
		t.Fatalf("HTTP API driftRateMsPerHour = %v, want ~%v", *getResp.Summary.DriftRateMsPerHour, wantRate)
	}

	// Read the SAME run back through the real showmeshctl binary,
	// against the same live server, over --output json.
	binPath := buildShowmeshctl(t)
	out := runShowmeshctl(t, binPath, server.URL, tok.Value,
		"audio", "alignment-run", "get", "--node", nodeID, "--run", runID, "--output", "json")

	var cliResp struct {
		Summary struct {
			SampleCount          int      `json:"sampleCount"`
			MaxExcursionOffsetMs *float64 `json:"maxExcursionOffsetMs"`
			DriftRateMsPerHour   *float64 `json:"driftRateMsPerHour"`
		} `json:"summary"`
	}
	if err := json.Unmarshal([]byte(out), &cliResp); err != nil {
		t.Fatalf("decode showmeshctl output: %v\noutput:\n%s", err, out)
	}
	if cliResp.Summary.SampleCount != sampleCount {
		t.Errorf("showmeshctl sampleCount = %d, want %d", cliResp.Summary.SampleCount, sampleCount)
	}
	if cliResp.Summary.MaxExcursionOffsetMs == nil || *cliResp.Summary.MaxExcursionOffsetMs != wantMaxExcursion {
		t.Errorf("showmeshctl maxExcursionOffsetMs = %v, want %v", cliResp.Summary.MaxExcursionOffsetMs, wantMaxExcursion)
	}
	if cliResp.Summary.DriftRateMsPerHour == nil {
		t.Fatalf("showmeshctl driftRateMsPerHour = nil, want ~%v", wantRate)
	}
	if diff := *cliResp.Summary.DriftRateMsPerHour - wantRate; diff < -1 || diff > 1 {
		t.Errorf("showmeshctl driftRateMsPerHour = %v, want ~%v", *cliResp.Summary.DriftRateMsPerHour, wantRate)
	}
}

// benchHTTPClient is a minimal authenticated JSON client for this test
// only; other tests in this package drive handlers.ServeHTTP directly.
type benchHTTPClient struct {
	base  string
	token string
}

func (c *benchHTTPClient) do(t *testing.T, method, path string, body, out any) {
	t.Helper()
	var reqBody io.Reader
	if body != nil {
		raw, err := json.Marshal(body)
		if err != nil {
			t.Fatalf("marshal request body: %v", err)
		}
		reqBody = bytes.NewReader(raw)
	}
	req, err := http.NewRequest(method, c.base+path, reqBody)
	if err != nil {
		t.Fatalf("build request %s %s: %v", method, path, err)
	}
	req.Header.Set("Authorization", "Bearer "+c.token)
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("do request %s %s: %v", method, path, err)
	}
	defer func() { _ = resp.Body.Close() }()
	raw, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatalf("read response body from %s %s: %v", method, path, err)
	}
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("%s %s: status %d, body: %s", method, path, resp.StatusCode, raw)
	}
	if out != nil {
		if err := json.Unmarshal(raw, out); err != nil {
			t.Fatalf("decode response from %s %s: %v\nbody: %s", method, path, err, raw)
		}
	}
}

func (c *benchHTTPClient) post(t *testing.T, path string, body, out any) {
	t.Helper()
	c.do(t, http.MethodPost, path, body, out)
}

func (c *benchHTTPClient) get(t *testing.T, path string, out any) {
	t.Helper()
	c.do(t, http.MethodGet, path, nil, out)
}

// buildShowmeshctl builds the real showmeshctl binary once into a temp
// directory, so this bench proof exercises the actual CLI program rather
// than calling its internal functions directly.
func buildShowmeshctl(t *testing.T) string {
	t.Helper()
	binPath := filepath.Join(t.TempDir(), "showmeshctl")
	cmd := exec.Command("go", "build", "-o", binPath, "github.com/showmeshsystems/showmesh/cmd/showmeshctl")
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("build showmeshctl: %v\n%s", err, out)
	}
	return binPath
}

// runShowmeshctl runs the built binary against server, returning stdout.
func runShowmeshctl(t *testing.T, binPath, server, token string, args ...string) string {
	t.Helper()
	fullArgs := append(append([]string{}, args...), "--server", server, "--token", token)
	cmd := exec.Command(binPath, fullArgs...)
	var stdout, stderr strings.Builder
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		t.Fatalf("run showmeshctl %s: %v\nstdout:\n%s\nstderr:\n%s", strings.Join(args, " "), err, stdout.String(), stderr.String())
	}
	return stdout.String()
}
