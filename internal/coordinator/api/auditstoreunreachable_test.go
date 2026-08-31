package api

import (
	"context"
	"net/http"
	"path/filepath"
	"testing"

	"github.com/showmeshsystems/showmesh/internal/coordinator/identity"
	"github.com/showmeshsystems/showmesh/internal/coordinator/store"
)

// TestSnapshotSurvivesAuditStoreEntirelyUnreachable is this PR's own rule
// inverted onto itself: ADR-024 decision 11's amendment says an audit
// store outage must never block an action, and AuditWriteStatus's live
// probe (identity/audit.go) is itself a write against that same store on
// every GET /api/v1/snapshot request. If a hard failure of that probe
// could fail the snapshot request, audit trouble would have become a way
// to block visibility of everything else in the snapshot (the exact
// shape this task removed from five other request paths), reintroduced on
// the one surface that is supposed to report the outage.
//
// The failure here is real, not injected via the fail_audit trigger (a
// constraint failure other tests already cover): the underlying
// *store.Store is closed outright before the request, so ProbeAuditWrite's
// own BeginTx fails with a real database/sql "connection is already
// closed" error, the same shape a genuinely unreachable store produces.
func TestSnapshotSurvivesAuditStoreEntirelyUnreachable(t *testing.T) {
	dir := t.TempDir()
	st, err := store.Open(context.Background(), filepath.Join(dir, "db"), nil)
	if err != nil {
		t.Fatalf("store.Open: %v", err)
	}
	svc := identity.NewService(st, fixedClock(testNow), filepath.Join(dir, "identity"), identity.WithLogger(testLogger()))

	if err := st.Close(); err != nil {
		t.Fatalf("close store to simulate it being unreachable: %v", err)
	}

	deps := Dependencies{
		Nodes: &fakeNodeLister{}, FPP: &fakeFPPLister{}, Observations: &fakeObservationLister{},
		Events: &fakeEventReader{}, Collectors: &fakeCollectorStatusLister{},
		Identity: svc,
	}
	api := New(deps, Options{Clock: fixedClock(testNow), Logger: testLogger()})

	resp, body := doRequest(t, api.Handler, "GET", "/api/v1/snapshot", nil)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200 even though the audit store is entirely unreachable; body: %s", resp.StatusCode, body)
	}
	m := decodeMap(t, body)
	auditStore, _ := m["auditStore"].(map[string]any)
	if auditStore["state"] != "unusable" {
		t.Errorf("auditStore.state = %v, want %q", auditStore["state"], "unusable")
	}
	reason, _ := auditStore["reason"].(string)
	if reason == "" {
		t.Error("auditStore.reason = empty, want a reason naming the failure")
	}
	if _, hasNodes := m["nodes"]; !hasNodes {
		t.Error("snapshot body is missing \"nodes\": the rest of the payload must stay intact when only the audit probe fails")
	}
}
