package api

import (
	"bytes"
	"context"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"
	"time"

	"github.com/showmeshsystems/showmesh/internal/coordinator/identity"
	"github.com/showmeshsystems/showmesh/internal/coordinator/store"
)

// newTestAlignmentRunDeps builds a real identity.Service and a real
// *store.Store, mirroring newTestDiscoveryDeps' own "no hand-rolled fake" rule.
func newTestAlignmentRunDeps(t *testing.T, now func() time.Time) (deps Dependencies, st *store.Store) {
	t.Helper()
	dir := t.TempDir()
	st, err := store.Open(context.Background(), filepath.Join(dir, "db"), nil, store.WithClock(now))
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(func() { _ = st.Close() })

	svc := identity.NewService(st, now, filepath.Join(dir, "data"), identity.WithLogger(testLogger()))
	deps = Dependencies{Identity: svc, AlignmentRuns: st}
	return deps, st
}

func adminAuthHeader(t *testing.T, deps Dependencies) string {
	t.Helper()
	admin := mustCreatePrincipal(t, deps.Identity, "admin-1", identity.RoleAdmin)
	return "Bearer " + mustIssueToken(t, deps.Identity, admin.ID)
}

func TestStartAlignmentRunThenGet(t *testing.T) {
	deps, _ := newTestAlignmentRunDeps(t, fixedClock(testNow))
	auth := adminAuthHeader(t, deps)
	api := New(deps, Options{Clock: fixedClock(testNow), Logger: testLogger()})

	req := httptest.NewRequest(http.MethodPost, "/api/v1/nodes/node-a/audio/alignment-runs", nil)
	req.Header.Set("Authorization", auth)
	resp, body := doRawRequest(t, api.Handler, req)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("start status = %d, want 200; body: %s", resp.StatusCode, body)
	}
	m := decodeMap(t, body)
	run, _ := m["run"].(map[string]any)
	runID, _ := run["id"].(string)
	if runID == "" {
		t.Fatalf("run.id missing in response: %s", body)
	}
	if run["nodeId"] != "node-a" {
		t.Errorf("run.nodeId = %v, want node-a", run["nodeId"])
	}
	if run["stoppedAt"] != nil {
		t.Errorf("run.stoppedAt = %v, want nil", run["stoppedAt"])
	}

	getReq := httptest.NewRequest(http.MethodGet, "/api/v1/nodes/node-a/audio/alignment-runs/"+runID, nil)
	getReq.Header.Set("Authorization", auth)
	getResp, getBody := doRawRequest(t, api.Handler, getReq)
	if getResp.StatusCode != http.StatusOK {
		t.Fatalf("get status = %d, want 200; body: %s", getResp.StatusCode, getBody)
	}
	gm := decodeMap(t, getBody)
	summary, _ := gm["summary"].(map[string]any)
	if summary["sampleCount"] != float64(0) {
		t.Errorf("summary.sampleCount = %v, want 0", summary["sampleCount"])
	}
	if summary["driftRateMsPerHour"] != nil {
		t.Errorf("summary.driftRateMsPerHour = %v, want nil with no samples", summary["driftRateMsPerHour"])
	}
	if summary["driftRateUnavailableReason"] == "" {
		t.Errorf("summary.driftRateUnavailableReason missing with no samples")
	}
}

func TestStartAlignmentRunTwiceConflicts(t *testing.T) {
	deps, _ := newTestAlignmentRunDeps(t, fixedClock(testNow))
	auth := adminAuthHeader(t, deps)
	api := New(deps, Options{Clock: fixedClock(testNow), Logger: testLogger()})

	first := httptest.NewRequest(http.MethodPost, "/api/v1/nodes/node-a/audio/alignment-runs", nil)
	first.Header.Set("Authorization", auth)
	resp1, body1 := doRawRequest(t, api.Handler, first)
	if resp1.StatusCode != http.StatusOK {
		t.Fatalf("first start status = %d, want 200; body: %s", resp1.StatusCode, body1)
	}

	second := httptest.NewRequest(http.MethodPost, "/api/v1/nodes/node-a/audio/alignment-runs", nil)
	second.Header.Set("Authorization", auth)
	resp2, body2 := doRawRequest(t, api.Handler, second)
	if resp2.StatusCode != http.StatusConflict {
		t.Fatalf("second start status = %d, want 409; body: %s", resp2.StatusCode, body2)
	}
}

func TestStopAlignmentRunThenGetShowsStoppedAt(t *testing.T) {
	deps, _ := newTestAlignmentRunDeps(t, fixedClock(testNow))
	auth := adminAuthHeader(t, deps)
	api := New(deps, Options{Clock: fixedClock(testNow), Logger: testLogger()})

	start := httptest.NewRequest(http.MethodPost, "/api/v1/nodes/node-a/audio/alignment-runs", nil)
	start.Header.Set("Authorization", auth)
	_, startBody := doRawRequest(t, api.Handler, start)
	runID, _ := decodeMap(t, startBody)["run"].(map[string]any)["id"].(string)

	stop := httptest.NewRequest(http.MethodPost, "/api/v1/nodes/node-a/audio/alignment-runs/"+runID+"/stop",
		bytes.NewBufferString(`{"reason":"operator stop"}`))
	stop.Header.Set("Authorization", auth)
	stopResp, stopBody := doRawRequest(t, api.Handler, stop)
	if stopResp.StatusCode != http.StatusOK {
		t.Fatalf("stop status = %d, want 200; body: %s", stopResp.StatusCode, stopBody)
	}
	sm := decodeMap(t, stopBody)["run"].(map[string]any)
	if sm["stoppedAt"] == nil {
		t.Errorf("run.stoppedAt = nil after stop, want set")
	}
	if sm["stopReason"] != "operator stop" {
		t.Errorf("run.stopReason = %v, want \"operator stop\"", sm["stopReason"])
	}
}

func TestStopUnknownAlignmentRunReturns404(t *testing.T) {
	deps, _ := newTestAlignmentRunDeps(t, fixedClock(testNow))
	auth := adminAuthHeader(t, deps)
	api := New(deps, Options{Clock: fixedClock(testNow), Logger: testLogger()})

	req := httptest.NewRequest(http.MethodPost, "/api/v1/nodes/node-a/audio/alignment-runs/missing-run/stop", nil)
	req.Header.Set("Authorization", auth)
	resp, body := doRawRequest(t, api.Handler, req)
	if resp.StatusCode != http.StatusNotFound {
		t.Fatalf("status = %d, want 404; body: %s", resp.StatusCode, body)
	}
}

func TestGetUnknownAlignmentRunReturns404(t *testing.T) {
	deps, _ := newTestAlignmentRunDeps(t, fixedClock(testNow))
	auth := adminAuthHeader(t, deps)
	api := New(deps, Options{Clock: fixedClock(testNow), Logger: testLogger()})

	req := httptest.NewRequest(http.MethodGet, "/api/v1/nodes/node-a/audio/alignment-runs/missing-run", nil)
	req.Header.Set("Authorization", auth)
	resp, body := doRawRequest(t, api.Handler, req)
	if resp.StatusCode != http.StatusNotFound {
		t.Fatalf("status = %d, want 404; body: %s", resp.StatusCode, body)
	}
}

func TestGetAlignmentRunSummaryMatchesHandComputedValues(t *testing.T) {
	deps, st := newTestAlignmentRunDeps(t, fixedClock(testNow))
	auth := adminAuthHeader(t, deps)
	api := New(deps, Options{Clock: fixedClock(testNow), Logger: testLogger()})

	start := httptest.NewRequest(http.MethodPost, "/api/v1/nodes/node-a/audio/alignment-runs", nil)
	start.Header.Set("Authorization", auth)
	_, startBody := doRawRequest(t, api.Handler, start)
	runID, _ := decodeMap(t, startBody)["run"].(map[string]any)["id"].(string)

	// Five samples, one second apart, offset rising 2 ms/sample: a
	// synthetic series with a hand-computable slope and a known max.
	base := testNow
	offsets := []float64{0, 2, 4, 6, 8}
	for i, off := range offsets {
		if err := st.AppendAlignmentSample(context.Background(), store.AlignmentSampleRecord{
			RunID: runID, SampledAt: base.Add(time.Duration(i) * time.Second), OffsetMs: off, SessionID: "sess-1",
		}); err != nil {
			t.Fatalf("append sample %d: %v", i, err)
		}
	}

	getReq := httptest.NewRequest(http.MethodGet, "/api/v1/nodes/node-a/audio/alignment-runs/"+runID, nil)
	getReq.Header.Set("Authorization", auth)
	_, getBody := doRawRequest(t, api.Handler, getReq)
	gm := decodeMap(t, getBody)
	summary, _ := gm["summary"].(map[string]any)

	if summary["sampleCount"] != float64(5) {
		t.Errorf("sampleCount = %v, want 5", summary["sampleCount"])
	}
	if summary["maxExcursionOffsetMs"] != float64(8) {
		t.Errorf("maxExcursionOffsetMs = %v, want 8", summary["maxExcursionOffsetMs"])
	}
	// Slope: 2ms per 1s = 2ms * 3600 = 7200 ms/hour.
	rate, ok := summary["driftRateMsPerHour"].(float64)
	if !ok {
		t.Fatalf("driftRateMsPerHour missing or not a number: %v", summary["driftRateMsPerHour"])
	}
	if diff := rate - 7200; diff < -0.5 || diff > 0.5 {
		t.Errorf("driftRateMsPerHour = %v, want ~7200", rate)
	}

	samples, _ := gm["samples"].([]any)
	if len(samples) != 5 {
		t.Fatalf("samples = %d, want 5", len(samples))
	}
	for i, s := range samples {
		sm := s.(map[string]any)
		if sm["offsetMs"] != offsets[i] {
			t.Errorf("samples[%d].offsetMs = %v, want %v (order not preserved)", i, sm["offsetMs"], offsets[i])
		}
	}
}
