package api

import (
	"context"
	"database/sql"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/showmeshsystems/showmesh/internal/coordinator/identity"
	"github.com/showmeshsystems/showmesh/internal/coordinator/inventory"
	"github.com/showmeshsystems/showmesh/internal/coordinator/store"
)

// This file tests BUILD-PLAN Step 7 seam B: node discovery and
// declaration (discovery.go). Every test here drives a real
// identity.Service AND a real *store.Store, mirroring auth_test.go's own
// "never a hand-rolled fake identity.Service" rule and extending it to
// DeclarationStore: *store.Store is the real production implementation,
// and discovery.go's handlers are exercised through it exactly as they
// run in the coordinator, not against a fake that could pass or fail
// independently of whether the real store/API composition is correct.

// newTestDiscoveryDeps builds a real identity.Service and a real
// *store.Store sharing one clock and one on-disk database directory
// (returned as storeDir, for a test that needs a second raw connection —
// see installFailAuditTrigger in identity's own audited_write_test.go,
// mirrored here). nodes/fpp are wired as the given fakes so a test
// controls exactly what a discovery run observes without needing a real
// MQTT-fed inventory.Manager.
func newTestDiscoveryDeps(t *testing.T, now func() time.Time, nodes *fakeNodeLister, fpp *fakeFPPLister) (deps Dependencies, st *store.Store, storeDir string) {
	t.Helper()
	dir := t.TempDir()
	storeDir = filepath.Join(dir, "db")
	st, err := store.Open(context.Background(), storeDir, nil, store.WithClock(now))
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(func() { _ = st.Close() })

	svc := identity.NewService(st, now, filepath.Join(dir, "data"), identity.WithLogger(testLogger()))

	if nodes == nil {
		nodes = &fakeNodeLister{}
	}
	if fpp == nil {
		fpp = &fakeFPPLister{}
	}
	deps = Dependencies{
		Nodes: nodes, FPP: fpp, Observations: &fakeObservationLister{},
		Events: &fakeEventReader{}, Collectors: &fakeCollectorStatusLister{},
		Identity: svc, Discovery: st,
	}
	return deps, st, storeDir
}

// liveNodeView is a minimal, but valid (non-zero FirstSeenAt/UpdatedAt —
// mapping.go's mustObservation panics on a zero CollectedAt fallback
// otherwise), online inventory.NodeView: what a discovery run counts as
// "observed" per handleStartDiscoveryRun's Liveness filter.
func liveNodeView(id string) inventory.NodeView {
	return inventory.NodeView{
		NodeID: id, FirstSeenAt: testNow.Add(-time.Hour), UpdatedAt: testNow,
		Liveness: inventory.LivenessOnline,
	}
}

// offlineNodeView is [liveNodeView] with Liveness demoted — what a node
// whose agent has stopped actually looks like in inventory (Step 2: a
// nodes row, once created, is never deleted; only its liveness verdict
// changes), NOT an empty view list. A discovery run must not count this as
// observed; GET /api/v1/nodes/{nodeId} must still find it, exactly as a
// real coordinator's Store.ListNodes would.
func offlineNodeView(id string) inventory.NodeView {
	v := liveNodeView(id)
	v.Liveness = inventory.LivenessOffline
	v.LivenessReason = "last-will evidence reports offline"
	return v
}

// --- B1 acceptance criterion 1: 401 / 403 / accepted ---

func TestStartDiscoveryRunAuthAndScope(t *testing.T) {
	deps, st, _ := newTestDiscoveryDeps(t, fixedClock(testNow), nil, nil)
	api := New(deps, Options{Clock: fixedClock(testNow), Logger: testLogger()})

	// Unauthenticated: 401.
	req := httptest.NewRequest(http.MethodPost, "/api/v1/discovery/runs", nil)
	resp, body := doRawRequest(t, api.Handler, req)
	if resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("unauthenticated status = %d, want 401; body: %s", resp.StatusCode, body)
	}

	// A viewer holds no config:write scope: 403 naming it.
	viewer := mustCreatePrincipal(t, deps.Identity, "viewer-1", identity.RoleViewer)
	viewerToken := mustIssueToken(t, deps.Identity, viewer.ID)
	viewerReq := httptest.NewRequest(http.MethodPost, "/api/v1/discovery/runs", nil)
	viewerReq.Header.Set("Authorization", "Bearer "+viewerToken)
	resp2, body2 := doRawRequest(t, api.Handler, viewerReq)
	if resp2.StatusCode != http.StatusForbidden {
		t.Fatalf("viewer status = %d, want 403; body: %s", resp2.StatusCode, body2)
	}
	m2 := decodeMap(t, body2)
	if detail, _ := m2["detail"].(string); !strings.Contains(detail, "config:write") {
		t.Errorf("403 detail = %q, want it to name config:write", detail)
	}

	// An admin holds config:write: 200.
	admin := mustCreatePrincipal(t, deps.Identity, "admin-1", identity.RoleAdmin)
	adminToken := mustIssueToken(t, deps.Identity, admin.ID)
	adminReq := httptest.NewRequest(http.MethodPost, "/api/v1/discovery/runs", nil)
	adminReq.Header.Set("Authorization", "Bearer "+adminToken)
	resp3, body3 := doRawRequest(t, api.Handler, adminReq)
	if resp3.StatusCode != http.StatusOK {
		t.Fatalf("admin status = %d, want 200; body: %s", resp3.StatusCode, body3)
	}

	// Sanity: the run actually landed in the store.
	runs, err := st.ListDiscoveryRuns(context.Background(), 10)
	if err != nil {
		t.Fatalf("list discovery runs: %v", err)
	}
	if len(runs) != 1 || !runs[0].Complete {
		t.Fatalf("runs = %+v, want exactly one complete run", runs)
	}
}

// --- B1 proposals ---

func TestStartDiscoveryRunProposesUndeclaredObservedEntities(t *testing.T) {
	nodes := &fakeNodeLister{views: []inventory.NodeView{liveNodeView("shed-01"), liveNodeView("media-03")}}
	fpp := &fakeFPPLister{views: []FPPInstanceView{{InstanceID: "player-01"}}}
	deps, st, _ := newTestDiscoveryDeps(t, fixedClock(testNow), nodes, fpp)
	admin := mustCreatePrincipal(t, deps.Identity, "admin-1", identity.RoleAdmin)
	adminToken := mustIssueToken(t, deps.Identity, admin.ID)
	api := New(deps, Options{Clock: fixedClock(testNow), Logger: testLogger()})

	// Declare media-03 up front; it must NOT appear as a proposal.
	if _, err := st.DeclareNode(context.Background(), store.NodeDeclarationRecord{NodeID: "media-03"}); err != nil {
		t.Fatalf("declare node: %v", err)
	}

	req := httptest.NewRequest(http.MethodPost, "/api/v1/discovery/runs", nil)
	req.Header.Set("Authorization", "Bearer "+adminToken)
	resp, body := doRawRequest(t, api.Handler, req)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200; body: %s", resp.StatusCode, body)
	}

	m := decodeMap(t, body)
	proposals, _ := m["proposals"].([]any)
	if len(proposals) != 2 {
		t.Fatalf("proposals = %+v, want exactly 2 (shed-01/node, player-01/fpp; media-03 already declared)", proposals)
	}
	got := map[string]string{}
	for _, p := range proposals {
		pm := p.(map[string]any)
		got[pm["nodeId"].(string)] = pm["source"].(string)
	}
	if got["shed-01"] != "node" {
		t.Errorf("shed-01 source = %q, want \"node\"", got["shed-01"])
	}
	if got["player-01"] != "fpp" {
		t.Errorf("player-01 source = %q, want \"fpp\"", got["player-01"])
	}
	if _, present := got["media-03"]; present {
		t.Errorf("media-03 (already declared) appeared as a proposal: %+v", got)
	}

	run, _ := m["run"].(map[string]any)
	if run["foundCount"] != float64(3) {
		t.Errorf("run.foundCount = %v, want 3 (shed-01, media-03, player-01)", run["foundCount"])
	}
	if run["complete"] != true {
		t.Errorf("run.complete = %v, want true", run["complete"])
	}
}

// incrementingClock returns start, then start+step, then start+2*step, ...
// on each call — needed wherever a test runs two discovery runs and must
// tell them apart by recency: discovery_runs.started_at is this package's
// own DESC ordering key (ListDiscoveryRuns), and a single fixedClock value
// shared by two runs makes "the most recent run" ambiguous at the SQL
// level with no deterministic secondary key, exactly the flake this
// project's own standing lesson warns against racing.
func incrementingClock(start time.Time, step time.Duration) func() time.Time {
	n := 0
	return func() time.Time {
		t := start.Add(time.Duration(n) * step)
		n++
		return t
	}
}

// --- B1/B3/RES-008 D6 acceptance criterion 2: discovery never deletes ---
//
// This is the criterion the spec singles out for teeth: "must fail if the
// never-delete rule is removed." Verified by hand (recorded in this
// task's report, not committed here) by temporarily adding a
// tx.DeleteNodeDeclaration call to handleStartDiscoveryRun's "entity not
// observed" path, confirming this exact test then fails, and reverting.

func TestDiscoveryRunNeverDeletesAnAbsentDeclaration(t *testing.T) {
	nodes := &fakeNodeLister{views: []inventory.NodeView{liveNodeView("shed-01")}}
	// Two discovery runs happen in this test, and "the most recent run"
	// (ListDiscoveryRuns' started_at DESC ordering) must be unambiguous
	// between them — see incrementingClock's own doc comment.
	clock := incrementingClock(testNow, time.Minute)
	deps, st, _ := newTestDiscoveryDeps(t, clock, nodes, nil)
	admin := mustCreatePrincipal(t, deps.Identity, "admin-1", identity.RoleAdmin)
	adminToken := mustIssueToken(t, deps.Identity, admin.ID)
	api := New(deps, Options{Clock: clock, Logger: testLogger()})

	if _, err := st.DeclareNode(context.Background(), store.NodeDeclarationRecord{NodeID: "shed-01", Label: "Shed controller"}); err != nil {
		t.Fatalf("declare node: %v", err)
	}

	// First run: shed-01 is observed (its agent is up) -> present.
	runDiscovery := func() {
		t.Helper()
		req := httptest.NewRequest(http.MethodPost, "/api/v1/discovery/runs", nil)
		req.Header.Set("Authorization", "Bearer "+adminToken)
		resp, body := doRawRequest(t, api.Handler, req)
		if resp.StatusCode != http.StatusOK {
			t.Fatalf("discovery run status = %d, want 200; body: %s", resp.StatusCode, body)
		}
	}
	runDiscovery()

	decl, err := st.GetNodeDeclaration(context.Background(), "shed-01")
	if err != nil {
		t.Fatalf("get node declaration: %v", err)
	}
	if decl.LastDiscoveryRunID == "" {
		t.Fatalf("declaration.LastDiscoveryRunID is empty after a run that observed it")
	}

	// Stop the agent. Its nodes row persists (Step 2 never deletes one —
	// see offlineNodeView's own doc comment), demoted to
	// LivenessOffline, and a fresh discovery run must not count it as
	// observed. The declaration must survive, completely unmodified in
	// its own identity, and merely be flagged not_seen on read.
	nodes.setViews([]inventory.NodeView{offlineNodeView("shed-01")})
	runDiscovery()

	stillDeclared, err := st.GetNodeDeclaration(context.Background(), "shed-01")
	if err != nil {
		t.Fatalf("get node declaration after agent stopped: %v (a discovery run must never delete a declaration)", err)
	}
	if stillDeclared.Label != "Shed controller" {
		t.Errorf("declaration.Label = %q after agent stopped, want unchanged \"Shed controller\"", stillDeclared.Label)
	}

	all, err := st.ListNodeDeclarations(context.Background())
	if err != nil {
		t.Fatalf("list node declarations: %v", err)
	}
	if len(all) != 1 {
		t.Fatalf("declared node count = %d after agent stopped, want 1 (never deleted)", len(all))
	}

	// And the read side renders it flagged, not silently "still present".
	getReq := httptest.NewRequest(http.MethodGet, "/api/v1/nodes/shed-01", nil)
	getReq.Header.Set("Authorization", "Bearer "+adminToken)
	getResp, getBody := doRawRequest(t, api.Handler, getReq)
	if getResp.StatusCode != http.StatusOK {
		t.Fatalf("GET node status = %d, want 200; body: %s", getResp.StatusCode, getBody)
	}
	node := decodeMap(t, getBody)["node"].(map[string]any)
	declaration := node["declaration"].(map[string]any)
	if declaration["declared"] != true {
		t.Fatalf("declaration.declared = %v after agent stopped, want true (never deleted)", declaration["declared"])
	}
	if declaration["discoveryState"] != "not_seen" {
		t.Errorf("declaration.discoveryState = %v, want \"not_seen\"", declaration["discoveryState"])
	}
}

// --- B2 acceptance criterion 3: delete requires confirmation and is audited ---

func TestDeleteNodeDeclarationRequiresExplicitConfirmation(t *testing.T) {
	deps, st, _ := newTestDiscoveryDeps(t, fixedClock(testNow), nil, nil)
	admin := mustCreatePrincipal(t, deps.Identity, "admin-1", identity.RoleAdmin)
	adminToken := mustIssueToken(t, deps.Identity, admin.ID)
	api := New(deps, Options{Clock: fixedClock(testNow), Logger: testLogger()})

	if _, err := st.DeclareNode(context.Background(), store.NodeDeclarationRecord{NodeID: "shed-01"}); err != nil {
		t.Fatalf("declare node: %v", err)
	}

	// confirm: false is rejected, and nothing is deleted.
	falseReq := newJSONRequest(t, http.MethodDelete, "/api/v1/nodes/shed-01/declaration", `{"confirm":false}`, nil)
	falseReq.Header.Set("Authorization", "Bearer "+adminToken)
	falseResp, falseBody := doRawRequest(t, api.Handler, falseReq)
	if falseResp.StatusCode != http.StatusBadRequest {
		t.Fatalf("confirm:false status = %d, want 400; body: %s", falseResp.StatusCode, falseBody)
	}
	if _, err := st.GetNodeDeclaration(context.Background(), "shed-01"); err != nil {
		t.Fatalf("declaration missing after a REJECTED (confirm:false) delete attempt: %v", err)
	}

	// A bare DELETE with no body is also rejected (required: true — no
	// body means no "confirm":true was ever presented).
	bareReq := httptest.NewRequest(http.MethodDelete, "/api/v1/nodes/shed-01/declaration", nil)
	bareReq.Header.Set("Authorization", "Bearer "+adminToken)
	bareResp, bareBody := doRawRequest(t, api.Handler, bareReq)
	if bareResp.StatusCode != http.StatusBadRequest {
		t.Fatalf("bare DELETE status = %d, want 400; body: %s", bareResp.StatusCode, bareBody)
	}

	// confirm: true succeeds and is audited.
	trueReq := newJSONRequest(t, http.MethodDelete, "/api/v1/nodes/shed-01/declaration", `{"confirm":true}`, nil)
	trueReq.Header.Set("Authorization", "Bearer "+adminToken)
	trueResp, trueBody := doRawRequest(t, api.Handler, trueReq)
	if trueResp.StatusCode != http.StatusNoContent {
		t.Fatalf("confirm:true status = %d, want 204; body: %s", trueResp.StatusCode, trueBody)
	}
	if _, err := st.GetNodeDeclaration(context.Background(), "shed-01"); err == nil {
		t.Fatalf("declaration still present after a confirmed delete")
	}

	entries, err := deps.Identity.ListAudit(context.Background(), 0, 100)
	if err != nil {
		t.Fatalf("list audit: %v", err)
	}
	var found bool
	for _, e := range entries {
		if e.Action == "node.declaration.delete" && e.Target == "shed-01" {
			found = true
		}
	}
	if !found {
		t.Errorf("no node.declaration.delete audit entry for shed-01 found in %+v", entries)
	}
}

// --- B2 acceptance criterion 4: audit-store failure refuses promote, leaves declaration absent ---

// installFailAuditTrigger lives in config_test.go in this package. Seam A,
// seam B and seam C each needed it and each wrote their own copy, which is
// how the merge found the triplicate. One copy, because three definitions
// of "the audit store is failing" are three things that can drift while
// all three tests keep passing.

func TestPromoteNodeWithFailingAuditIsRefusedAndDeclarationIsAbsent(t *testing.T) {
	deps, st, storeDir := newTestDiscoveryDeps(t, fixedClock(testNow), nil, nil)
	admin := mustCreatePrincipal(t, deps.Identity, "admin-1", identity.RoleAdmin)
	adminToken := mustIssueToken(t, deps.Identity, admin.ID)
	api := New(deps, Options{Clock: fixedClock(testNow), Logger: testLogger()})

	installFailAuditTrigger(t, storeDir)

	req := newJSONRequest(t, http.MethodPost, "/api/v1/nodes/shed-01/declaration", `{"label":"Shed controller"}`, nil)
	req.Header.Set("Authorization", "Bearer "+adminToken)
	resp, body := doRawRequest(t, api.Handler, req)
	if resp.StatusCode != http.StatusInternalServerError {
		t.Fatalf("status with a failing audit store = %d, want 500; body: %s", resp.StatusCode, body)
	}

	if _, err := st.GetNodeDeclaration(context.Background(), "shed-01"); err == nil {
		t.Fatalf("declaration exists after a promote whose audit entry failed to write (ADR-024 decision 11's same-transaction rule)")
	}
}

// TestPromoteNodeWithoutFailingAuditSucceeds is the control for the test
// above: identical setup, no trigger installed, where the declaration
// MUST exist afterward — proving the 500 above is caused by the audit
// failure specifically, not some unrelated defect that would fail every
// promote regardless.
func TestPromoteNodeWithoutFailingAuditSucceeds(t *testing.T) {
	deps, st, _ := newTestDiscoveryDeps(t, fixedClock(testNow), nil, nil)
	admin := mustCreatePrincipal(t, deps.Identity, "admin-1", identity.RoleAdmin)
	adminToken := mustIssueToken(t, deps.Identity, admin.ID)
	api := New(deps, Options{Clock: fixedClock(testNow), Logger: testLogger()})

	req := newJSONRequest(t, http.MethodPost, "/api/v1/nodes/shed-01/declaration", `{"label":"Shed controller"}`, nil)
	req.Header.Set("Authorization", "Bearer "+adminToken)
	resp, body := doRawRequest(t, api.Handler, req)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200; body: %s", resp.StatusCode, body)
	}

	if _, err := st.GetNodeDeclaration(context.Background(), "shed-01"); err != nil {
		t.Fatalf("declaration missing after a successful promote: %v", err)
	}
}

// --- B3 acceptance criterion 5: an incomplete run renders unknown, never not_seen ---

func TestIncompleteRunRendersUnknownNeverNotSeen(t *testing.T) {
	nodes := &fakeNodeLister{views: []inventory.NodeView{liveNodeView("shed-01")}}
	deps, st, _ := newTestDiscoveryDeps(t, fixedClock(testNow), nodes, nil)
	admin := mustCreatePrincipal(t, deps.Identity, "admin-1", identity.RoleAdmin)
	adminToken := mustIssueToken(t, deps.Identity, admin.ID)
	api := New(deps, Options{Clock: fixedClock(testNow), Logger: testLogger()})
	ctx := context.Background()

	if _, err := st.DeclareNode(ctx, store.NodeDeclarationRecord{NodeID: "shed-01"}); err != nil {
		t.Fatalf("declare node: %v", err)
	}

	// A run that started and failed partway, exactly as
	// handleStartDiscoveryRun leaves one on a mid-run error: complete=0,
	// a stated reason, never a missing row.
	rec, err := st.StartDiscoveryRun(ctx, store.DiscoveryRunRecord{ID: "run-incomplete"})
	if err != nil {
		t.Fatalf("start discovery run: %v", err)
	}
	if err := st.FinishDiscoveryRun(ctx, rec.ID, false, "disk full while listing FPP instances", 0); err != nil {
		t.Fatalf("finish discovery run: %v", err)
	}

	req := httptest.NewRequest(http.MethodGet, "/api/v1/nodes/shed-01", nil)
	req.Header.Set("Authorization", "Bearer "+adminToken)
	resp, body := doRawRequest(t, api.Handler, req)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200; body: %s", resp.StatusCode, body)
	}
	declaration := decodeMap(t, body)["node"].(map[string]any)["declaration"].(map[string]any)
	if declaration["discoveryState"] != "unknown" {
		t.Fatalf("discoveryState = %v, want \"unknown\" (never \"not_seen\") for an incomplete run", declaration["discoveryState"])
	}
	if declaration["discoveryReason"] != "disk full while listing FPP instances" {
		t.Errorf("discoveryReason = %v, want the incomplete run's own reason", declaration["discoveryReason"])
	}
}

// --- B3 acceptance criterion 6: a dangling last_discovery_run_id renders unknown, never blank ---

func TestDanglingDiscoveryRunIDRendersUnknownNeverBlank(t *testing.T) {
	nodes := &fakeNodeLister{views: []inventory.NodeView{liveNodeView("shed-01")}}
	deps, st, storeDir := newTestDiscoveryDeps(t, fixedClock(testNow), nodes, nil)
	admin := mustCreatePrincipal(t, deps.Identity, "admin-1", identity.RoleAdmin)
	adminToken := mustIssueToken(t, deps.Identity, admin.ID)
	api := New(deps, Options{Clock: fixedClock(testNow), Logger: testLogger()})
	ctx := context.Background()

	if _, err := st.DeclareNode(ctx, store.NodeDeclarationRecord{NodeID: "shed-01"}); err != nil {
		t.Fatalf("declare node: %v", err)
	}

	runReq := httptest.NewRequest(http.MethodPost, "/api/v1/discovery/runs", nil)
	runReq.Header.Set("Authorization", "Bearer "+adminToken)
	runResp, runBody := doRawRequest(t, api.Handler, runReq)
	if runResp.StatusCode != http.StatusOK {
		t.Fatalf("discovery run status = %d, want 200; body: %s", runResp.StatusCode, runBody)
	}

	declBefore, err := st.GetNodeDeclaration(ctx, "shed-01")
	if err != nil {
		t.Fatalf("get node declaration: %v", err)
	}
	if declBefore.LastDiscoveryRunID == "" {
		t.Fatalf("declaration has no LastDiscoveryRunID after a run that observed it")
	}

	// Simulate discovery_runs retention pruning every row — this
	// declaration's own last_discovery_run_id becomes dangling by
	// construction, exactly as migrations.go's schemaV6 doc comment
	// describes ("discovery_runs is pruned by retention and
	// node_declarations is not"). A second raw connection, matching
	// installFailAuditTrigger's own pattern, since this package has no
	// other way to reach the underlying *sql.DB.
	dbPath := filepath.Join(storeDir, "showmesh.db")
	raw, err := sql.Open("sqlite", "file:"+dbPath)
	if err != nil {
		t.Fatalf("open raw connection: %v", err)
	}
	defer func() { _ = raw.Close() }()
	if _, err := raw.ExecContext(ctx, `DELETE FROM discovery_runs`); err != nil {
		t.Fatalf("prune discovery_runs: %v", err)
	}

	getReq := httptest.NewRequest(http.MethodGet, "/api/v1/nodes/shed-01", nil)
	getReq.Header.Set("Authorization", "Bearer "+adminToken)
	getResp, getBody := doRawRequest(t, api.Handler, getReq)
	if getResp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200; body: %s", getResp.StatusCode, getBody)
	}
	declaration := decodeMap(t, getBody)["node"].(map[string]any)["declaration"].(map[string]any)
	if declaration["discoveryState"] != "unknown" {
		t.Fatalf("discoveryState = %v, want \"unknown\" once the referenced run has been pruned", declaration["discoveryState"])
	}
	if declaration["discoveryReason"] == nil || declaration["discoveryReason"] == "" {
		t.Errorf("discoveryReason = %v, want a stated reason, never blank", declaration["discoveryReason"])
	}
	// "Never blank": this declaration's own last-known positive sighting
	// is still reported even though DiscoveryState itself is unknown.
	if declaration["lastDiscoveryRunId"] != declBefore.LastDiscoveryRunID {
		t.Errorf("lastDiscoveryRunId = %v, want the (now-dangling) run id %q reported rather than blank",
			declaration["lastDiscoveryRunId"], declBefore.LastDiscoveryRunID)
	}
}

// --- undeclared node renders not_applicable ---

func TestUndeclaredNodeRendersNotApplicable(t *testing.T) {
	nodes := &fakeNodeLister{views: []inventory.NodeView{liveNodeView("shed-01")}}
	deps, _, _ := newTestDiscoveryDeps(t, fixedClock(testNow), nodes, nil)
	admin := mustCreatePrincipal(t, deps.Identity, "admin-1", identity.RoleAdmin)
	adminToken := mustIssueToken(t, deps.Identity, admin.ID)
	api := New(deps, Options{Clock: fixedClock(testNow), Logger: testLogger()})

	req := httptest.NewRequest(http.MethodGet, "/api/v1/nodes/shed-01", nil)
	req.Header.Set("Authorization", "Bearer "+adminToken)
	resp, body := doRawRequest(t, api.Handler, req)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200; body: %s", resp.StatusCode, body)
	}
	declaration := decodeMap(t, body)["node"].(map[string]any)["declaration"].(map[string]any)
	if declaration["declared"] != false {
		t.Errorf("declared = %v, want false for a node nobody has ever declared", declaration["declared"])
	}
	if declaration["discoveryState"] != "not_applicable" {
		t.Errorf("discoveryState = %v, want \"not_applicable\"", declaration["discoveryState"])
	}
}
