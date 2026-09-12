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

// TestGetAlignmentRunUnderWrongNodeReturns404 is the cross-node read
// check: a run started under node-a must not be readable through node-b's
// path, and must answer identically to an unknown run id (no leak that the
// id exists elsewhere).
func TestGetAlignmentRunUnderWrongNodeReturns404(t *testing.T) {
	deps, _ := newTestAlignmentRunDeps(t, fixedClock(testNow))
	auth := adminAuthHeader(t, deps)
	api := New(deps, Options{Clock: fixedClock(testNow), Logger: testLogger()})

	start := httptest.NewRequest(http.MethodPost, "/api/v1/nodes/node-a/audio/alignment-runs", nil)
	start.Header.Set("Authorization", auth)
	_, startBody := doRawRequest(t, api.Handler, start)
	runID, _ := decodeMap(t, startBody)["run"].(map[string]any)["id"].(string)

	getReq := httptest.NewRequest(http.MethodGet, "/api/v1/nodes/node-b/audio/alignment-runs/"+runID, nil)
	getReq.Header.Set("Authorization", auth)
	getResp, getBody := doRawRequest(t, api.Handler, getReq)
	if getResp.StatusCode != http.StatusNotFound {
		t.Fatalf("get under wrong node status = %d, want 404; body: %s", getResp.StatusCode, getBody)
	}
}

// TestStopAlignmentRunUnderWrongNodeReturns404AndRunStaysActive is the
// cross-node write check: a run started under node-a must not be
// stoppable through node-b's path, and must remain active afterward.
func TestStopAlignmentRunUnderWrongNodeReturns404AndRunStaysActive(t *testing.T) {
	deps, _ := newTestAlignmentRunDeps(t, fixedClock(testNow))
	auth := adminAuthHeader(t, deps)
	api := New(deps, Options{Clock: fixedClock(testNow), Logger: testLogger()})

	start := httptest.NewRequest(http.MethodPost, "/api/v1/nodes/node-a/audio/alignment-runs", nil)
	start.Header.Set("Authorization", auth)
	_, startBody := doRawRequest(t, api.Handler, start)
	runID, _ := decodeMap(t, startBody)["run"].(map[string]any)["id"].(string)

	stop := httptest.NewRequest(http.MethodPost, "/api/v1/nodes/node-b/audio/alignment-runs/"+runID+"/stop", nil)
	stop.Header.Set("Authorization", auth)
	stopResp, stopBody := doRawRequest(t, api.Handler, stop)
	if stopResp.StatusCode != http.StatusNotFound {
		t.Fatalf("stop under wrong node status = %d, want 404; body: %s", stopResp.StatusCode, stopBody)
	}

	getReq := httptest.NewRequest(http.MethodGet, "/api/v1/nodes/node-a/audio/alignment-runs/"+runID, nil)
	getReq.Header.Set("Authorization", auth)
	_, getBody := doRawRequest(t, api.Handler, getReq)
	gm := decodeMap(t, getBody)["run"].(map[string]any)
	if gm["stoppedAt"] != nil {
		t.Errorf("run.stoppedAt = %v after a wrong-node stop attempt, want nil (run stays active)", gm["stoppedAt"])
	}
}

// TestGetAlignmentRunLimitTruncatesButSummaryCoversFullSeries mirrors
// TestGetAlignmentRunSummaryMatchesHandComputedValues with a limit smaller
// than the sample count: the samples page is bounded, truncated is set,
// and the summary (max excursion, sample count) still reflects every
// sample, not just the returned page.
func TestGetAlignmentRunLimitTruncatesButSummaryCoversFullSeries(t *testing.T) {
	deps, st := newTestAlignmentRunDeps(t, fixedClock(testNow))
	auth := adminAuthHeader(t, deps)
	api := New(deps, Options{Clock: fixedClock(testNow), Logger: testLogger()})

	start := httptest.NewRequest(http.MethodPost, "/api/v1/nodes/node-a/audio/alignment-runs", nil)
	start.Header.Set("Authorization", auth)
	_, startBody := doRawRequest(t, api.Handler, start)
	runID, _ := decodeMap(t, startBody)["run"].(map[string]any)["id"].(string)

	base := testNow
	offsets := []float64{0, 2, 4, 6, 8}
	for i, off := range offsets {
		if err := st.AppendAlignmentSample(context.Background(), store.AlignmentSampleRecord{
			RunID: runID, SampledAt: base.Add(time.Duration(i) * time.Second), OffsetMs: off, SessionID: "sess-1",
		}); err != nil {
			t.Fatalf("append sample %d: %v", i, err)
		}
	}

	getReq := httptest.NewRequest(http.MethodGet, "/api/v1/nodes/node-a/audio/alignment-runs/"+runID+"?limit=2", nil)
	getReq.Header.Set("Authorization", auth)
	getResp, getBody := doRawRequest(t, api.Handler, getReq)
	if getResp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200; body: %s", getResp.StatusCode, getBody)
	}
	gm := decodeMap(t, getBody)

	samples, _ := gm["samples"].([]any)
	if len(samples) != 2 {
		t.Fatalf("samples = %d, want 2 (limited)", len(samples))
	}
	if gm["truncated"] != true {
		t.Errorf("truncated = %v, want true", gm["truncated"])
	}
	summary, _ := gm["summary"].(map[string]any)
	if summary["sampleCount"] != float64(5) {
		t.Errorf("summary.sampleCount = %v, want 5 (the full series, not the limited page)", summary["sampleCount"])
	}
	if summary["maxExcursionOffsetMs"] != float64(8) {
		t.Errorf("summary.maxExcursionOffsetMs = %v, want 8 (from a sample beyond the limit)", summary["maxExcursionOffsetMs"])
	}
}

// alignmentRunTestDeps mirrors configTestDeps for the alignment-run audit
// tests below, which need direct access to storeDir to install a real
// SQLite trigger (installFailAuditTrigger, config_test.go).
func alignmentRunTestDeps(svc identity.Service, st *store.Store) Dependencies {
	return Dependencies{Identity: svc, AlignmentRuns: st}
}

// TestStartAlignmentRunProducesAuditEntry and
// TestStopAlignmentRunProducesAuditEntry are this seam's own version of
// TestPutFPPEndpointsConfigWithoutFailingAuditSucceeds (config_test.go):
// asserting the audit entry's actual content, not merely a 200 status,
// per ADR-024 decision 11.
func TestStartAlignmentRunProducesAuditEntry(t *testing.T) {
	svc, st, _ := newTestIdentityServiceWithStore(t, fixedClock(testNow))
	admin := mustCreatePrincipal(t, svc, "admin-1", identity.RoleAdmin)
	adminToken := mustIssueToken(t, svc, admin.ID)
	api := New(alignmentRunTestDeps(svc, st), Options{Clock: fixedClock(testNow), Logger: testLogger()})

	req := httptest.NewRequest(http.MethodPost, "/api/v1/nodes/node-a/audio/alignment-runs", nil)
	req.Header.Set("Authorization", "Bearer "+adminToken)
	resp, body := doRawRequest(t, api.Handler, req)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200; body: %s", resp.StatusCode, body)
	}
	runID, _ := decodeMap(t, body)["run"].(map[string]any)["id"].(string)

	entries, err := svc.ListAudit(context.Background(), 0, 100)
	if err != nil {
		t.Fatalf("ListAudit: %v", err)
	}
	var found bool
	for _, e := range entries {
		if e.Action != "audio.alignment_run.start" || e.Target != runID {
			continue
		}
		found = true
		if e.PrincipalID != admin.ID {
			t.Errorf("audit entry PrincipalID = %q, want %q", e.PrincipalID, admin.ID)
		}
	}
	if !found {
		t.Fatalf("no audio.alignment_run.start audit entry for run %q found among %d entries", runID, len(entries))
	}
}

func TestStopAlignmentRunProducesAuditEntry(t *testing.T) {
	svc, st, _ := newTestIdentityServiceWithStore(t, fixedClock(testNow))
	admin := mustCreatePrincipal(t, svc, "admin-1", identity.RoleAdmin)
	adminToken := mustIssueToken(t, svc, admin.ID)
	api := New(alignmentRunTestDeps(svc, st), Options{Clock: fixedClock(testNow), Logger: testLogger()})

	start := httptest.NewRequest(http.MethodPost, "/api/v1/nodes/node-a/audio/alignment-runs", nil)
	start.Header.Set("Authorization", "Bearer "+adminToken)
	_, startBody := doRawRequest(t, api.Handler, start)
	runID, _ := decodeMap(t, startBody)["run"].(map[string]any)["id"].(string)

	stop := httptest.NewRequest(http.MethodPost, "/api/v1/nodes/node-a/audio/alignment-runs/"+runID+"/stop", nil)
	stop.Header.Set("Authorization", "Bearer "+adminToken)
	stopResp, stopBody := doRawRequest(t, api.Handler, stop)
	if stopResp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200; body: %s", stopResp.StatusCode, stopBody)
	}

	entries, err := svc.ListAudit(context.Background(), 0, 100)
	if err != nil {
		t.Fatalf("ListAudit: %v", err)
	}
	var found bool
	for _, e := range entries {
		if e.Action != "audio.alignment_run.stop" || e.Target != runID {
			continue
		}
		found = true
		if e.PrincipalID != admin.ID {
			t.Errorf("audit entry PrincipalID = %q, want %q", e.PrincipalID, admin.ID)
		}
	}
	if !found {
		t.Fatalf("no audio.alignment_run.stop audit entry for run %q found among %d entries", runID, len(entries))
	}
}

// TestStartAlignmentRunFailsClosedOnAuditFailure and
// TestStopAlignmentRunFailsClosedOnAuditFailure are this seam's own
// version of TestPutFPPEndpointsConfigFailsClosedOnAuditFailure
// (config_test.go): a failed audit write fails the request and leaves no
// trace of the state change it would have made, via a REAL SQLite
// trigger, matching that test's own rule.
func TestStartAlignmentRunFailsClosedOnAuditFailure(t *testing.T) {
	svc, st, storeDir := newTestIdentityServiceWithStore(t, fixedClock(testNow))
	admin := mustCreatePrincipal(t, svc, "admin-1", identity.RoleAdmin)
	adminToken := mustIssueToken(t, svc, admin.ID)
	api := New(alignmentRunTestDeps(svc, st), Options{Clock: fixedClock(testNow), Logger: testLogger()})

	installFailAuditTrigger(t, storeDir)

	req := httptest.NewRequest(http.MethodPost, "/api/v1/nodes/node-a/audio/alignment-runs", nil)
	req.Header.Set("Authorization", "Bearer "+adminToken)
	resp, body := doRawRequest(t, api.Handler, req)
	if resp.StatusCode != http.StatusInternalServerError {
		t.Fatalf("status = %d, want 500 (write refused: the audit store is failing); body: %s", resp.StatusCode, body)
	}

	runs, err := st.ListAlignmentRuns(context.Background(), "node-a")
	if err != nil {
		t.Fatalf("ListAlignmentRuns: %v", err)
	}
	if len(runs) != 0 {
		t.Fatalf("runs after a start whose audit entry failed = %v, want none, same-transaction rule violated", runs)
	}
}

func TestStopAlignmentRunFailsClosedOnAuditFailure(t *testing.T) {
	svc, st, storeDir := newTestIdentityServiceWithStore(t, fixedClock(testNow))
	admin := mustCreatePrincipal(t, svc, "admin-1", identity.RoleAdmin)
	adminToken := mustIssueToken(t, svc, admin.ID)
	api := New(alignmentRunTestDeps(svc, st), Options{Clock: fixedClock(testNow), Logger: testLogger()})

	start := httptest.NewRequest(http.MethodPost, "/api/v1/nodes/node-a/audio/alignment-runs", nil)
	start.Header.Set("Authorization", "Bearer "+adminToken)
	_, startBody := doRawRequest(t, api.Handler, start)
	runID, _ := decodeMap(t, startBody)["run"].(map[string]any)["id"].(string)

	installFailAuditTrigger(t, storeDir)

	stop := httptest.NewRequest(http.MethodPost, "/api/v1/nodes/node-a/audio/alignment-runs/"+runID+"/stop", nil)
	stop.Header.Set("Authorization", "Bearer "+adminToken)
	stopResp, stopBody := doRawRequest(t, api.Handler, stop)
	if stopResp.StatusCode != http.StatusInternalServerError {
		t.Fatalf("status = %d, want 500 (write refused: the audit store is failing); body: %s", stopResp.StatusCode, stopBody)
	}

	rec, _, _, _, err := st.GetAlignmentRun(context.Background(), runID, 1)
	if err != nil {
		t.Fatalf("GetAlignmentRun: %v", err)
	}
	if rec.StoppedAt != nil {
		t.Fatalf("run stopped after a stop whose audit entry failed, same-transaction rule violated")
	}
}
