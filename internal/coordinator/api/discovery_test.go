package api

import (
	"context"
	"database/sql"
	"io"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"sync"
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

// polledFPPView is a minimal FPPInstanceView with real, successful
// collection evidence — what [fppInstanceObserved] (DEFECT 3) requires to
// count an FPP instance as currently observed: LastPollAt set, LastPollError
// nil.
func polledFPPView(id string) FPPInstanceView {
	pollAt := testNow
	return FPPInstanceView{InstanceID: id, LastPollAt: &pollAt}
}

// neverPolledFPPView is what a freshly configured (or freshly restarted
// coordinator's) FPP instance looks like BEFORE its first poll cycle
// completes: LastPollAt nil. This is DEFECT 2's ambiguous-evidence case
// applied to the FPP branch (fppInstanceEvidenceAmbiguous), never
// DEFECT 3's "counts as observed" case.
func neverPolledFPPView(id string) FPPInstanceView {
	return FPPInstanceView{InstanceID: id}
}

// failedPollFPPView is what a genuinely unreachable (unplugged, network
// down) FPP instance looks like: a poll DID complete, and it reported
// collection_failed. This is real NEGATIVE evidence — the LivenessOffline
// analog — and must remain free to support not_seen once a run completes
// (DEFECT 3's whole point), unlike neverPolledFPPView above.
func failedPollFPPView(id string) FPPInstanceView {
	pollAt := testNow
	errStr := "dial tcp: connection refused"
	return FPPInstanceView{InstanceID: id, LastPollAt: &pollAt, LastPollError: &errStr}
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
	// DEFECT 3: player-01 needs REAL collection evidence (a completed poll
	// with no error) to count as observed — mere configuration (a bare
	// FPPInstanceView{InstanceID: ...} with LastPollAt nil) no longer does.
	fpp := &fakeFPPLister{views: []FPPInstanceView{polledFPPView("player-01")}}
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

	// ADR-003 / this file's own doc comment: a discovery run PROPOSES what
	// it observes and NEVER declares on its own — only handlePromoteNode
	// does. Assert the set of declarations is exactly what it was before
	// this run (media-03 alone): a handler that declared shed-01 and
	// player-01 as a side effect of proposing them would still pass every
	// assertion above, since those only inspect the response body.
	decls, err := st.ListNodeDeclarations(context.Background())
	if err != nil {
		t.Fatalf("list node declarations: %v", err)
	}
	if len(decls) != 1 || decls[0].NodeID != "media-03" {
		t.Errorf("declarations after run = %+v, want only the pre-existing media-03 declaration (a discovery run proposes, it never declares)", decls)
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

// --- DEFECT 1: a node promoted from a LIVE proposal must render present immediately ---

// TestDeclareAfterDiscoveryRendersPresentImmediately drives the PRIMARY
// workflow the review found broken: run discovery, THEN declare one of its
// proposals — the reverse ordering from every OTHER test in this file
// (which all declare first, then run discovery), and the ordering the UI
// and CLI actually push an operator toward (NodesList.tsx's "Run
// discovery" panel proposes, then "Declare"; `showmeshctl discover` then
// `showmeshctl declare`). Confirmed to fail before the fix: with
// handlePromoteNode's discoveryObservedNow re-check removed (or with
// DeclareNode's INSERT hardcoding last_discovery_run_id to ” as it did
// before DEFECT 1's store-layer fix), this test's own assertion on the
// declare RESPONSE fails immediately — discoveryState is "not_seen", not
// "present" — which is exactly the contradiction the review reported: a
// node that discovery is the SOLE reason this coordinator knows about
// renders "not seen by the most recent discovery run" the instant it is
// declared.
func TestDeclareAfterDiscoveryRendersPresentImmediately(t *testing.T) {
	nodes := &fakeNodeLister{views: []inventory.NodeView{liveNodeView("shed-01")}}
	deps, _, _ := newTestDiscoveryDeps(t, fixedClock(testNow), nodes, nil)
	admin := mustCreatePrincipal(t, deps.Identity, "admin-1", identity.RoleAdmin)
	adminToken := mustIssueToken(t, deps.Identity, admin.ID)
	api := New(deps, Options{Clock: fixedClock(testNow), Logger: testLogger()})

	runReq := httptest.NewRequest(http.MethodPost, "/api/v1/discovery/runs", nil)
	runReq.Header.Set("Authorization", "Bearer "+adminToken)
	runResp, runBody := doRawRequest(t, api.Handler, runReq)
	if runResp.StatusCode != http.StatusOK {
		t.Fatalf("discovery run status = %d, want 200; body: %s", runResp.StatusCode, runBody)
	}
	runResult := decodeMap(t, runBody)
	proposals, _ := runResult["proposals"].([]any)
	if len(proposals) != 1 {
		t.Fatalf("proposals = %+v, want exactly 1 (shed-01)", proposals)
	}

	declareReq := newJSONRequest(t, http.MethodPost, "/api/v1/nodes/shed-01/declaration", `{"label":"Shed controller"}`, nil)
	declareReq.Header.Set("Authorization", "Bearer "+adminToken)
	declareResp, declareBody := doRawRequest(t, api.Handler, declareReq)
	if declareResp.StatusCode != http.StatusOK {
		t.Fatalf("declare status = %d, want 200; body: %s", declareResp.StatusCode, declareBody)
	}
	declaration := decodeMap(t, declareBody)["declaration"].(map[string]any)
	if declaration["discoveryState"] != "present" {
		t.Fatalf(`declaration.discoveryState = %v immediately after declaring a node discovery just proposed, want "present" `+
			`(got %v — a node this coordinator knows about SOLELY because discovery just proposed it must never render "not seen by the most recent discovery run")`,
			declaration["discoveryState"], declaration)
	}
	if declaration["lastDiscoveryRunId"] != runResult["run"].(map[string]any)["id"] {
		t.Errorf("declaration.lastDiscoveryRunId = %v, want the run just performed (%v)",
			declaration["lastDiscoveryRunId"], runResult["run"].(map[string]any)["id"])
	}
}

// TestDeclareWithNoDiscoveryHistoryRendersUnknownNeverPresentOrNotSeen is
// the guard case the review named explicitly: a promotion made with NO
// discovery run in history at all must render "unknown" with a stated
// reason — never a fabricated "present" (nothing has ever confirmed this
// node) and never "not_seen" (nothing has ever DISconfirmed it either).
func TestDeclareWithNoDiscoveryHistoryRendersUnknownNeverPresentOrNotSeen(t *testing.T) {
	deps, _, _ := newTestDiscoveryDeps(t, fixedClock(testNow), nil, nil)
	admin := mustCreatePrincipal(t, deps.Identity, "admin-1", identity.RoleAdmin)
	adminToken := mustIssueToken(t, deps.Identity, admin.ID)
	api := New(deps, Options{Clock: fixedClock(testNow), Logger: testLogger()})

	declareReq := newJSONRequest(t, http.MethodPost, "/api/v1/nodes/shed-01/declaration", `{}`, nil)
	declareReq.Header.Set("Authorization", "Bearer "+adminToken)
	declareResp, declareBody := doRawRequest(t, api.Handler, declareReq)
	if declareResp.StatusCode != http.StatusOK {
		t.Fatalf("declare status = %d, want 200; body: %s", declareResp.StatusCode, declareBody)
	}
	declaration := decodeMap(t, declareBody)["declaration"].(map[string]any)
	if declaration["discoveryState"] != "unknown" {
		t.Fatalf("declaration.discoveryState = %v with no discovery run ever performed, want \"unknown\"", declaration["discoveryState"])
	}
	if declaration["discoveryReason"] == nil || declaration["discoveryReason"] == "" {
		t.Errorf("declaration.discoveryReason = %v, want a stated reason", declaration["discoveryReason"])
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

	// Assert the audit entry's own content, not merely that a write
	// succeeded: ADR-024's same-transaction rule is about attribution
	// landing correctly, and a garbage action, wrong target, or dropped
	// principal/form/credential/client address would all leave every
	// assertion above passing.
	entries, err := deps.Identity.ListAudit(context.Background(), 0, 100)
	if err != nil {
		t.Fatalf("ListAudit: %v", err)
	}
	var found bool
	for _, e := range entries {
		if e.Action != "node.declare" || e.Target != "shed-01" {
			continue
		}
		found = true
		if e.PrincipalID != admin.ID {
			t.Errorf("audit entry PrincipalID = %q, want %q", e.PrincipalID, admin.ID)
		}
		if e.PrincipalName != admin.Name {
			t.Errorf("audit entry PrincipalName = %q, want %q", e.PrincipalName, admin.Name)
		}
		if e.Form != identity.FormToken {
			t.Errorf("audit entry Form = %q, want %q (this request authenticated via bearer token)", e.Form, identity.FormToken)
		}
		if e.CredentialID == "" {
			t.Errorf("audit entry CredentialID is empty, want the token's credential id recorded")
		}
	}
	if !found {
		t.Fatalf("no node.declare audit entry for shed-01 found among %d entries", len(entries))
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

// TestAllDiscoveryHistoryPrunedRendersUnknownNeverBlank exercises the
// "no discovery run history is available" branch (mapNodeDeclaration's
// latestRun == nil case) — the ONE outcome that deliberately covers BOTH
// "no run has ever been performed" AND "every run has been pruned"
// identically, per that function's own doc comment ("this API cannot and
// must not guess which"). TWO runs happen here, not one, specifically so
// this is distinguishable from "this store coincidentally only ever ran
// discovery once": the review's own finding was that a single-run setup
// proves nothing that a "discovery has literally never run" setup would
// not ALSO prove, since deleting the only row that has ever existed is
// indistinguishable, at the DB level, from it never having existed. Two
// runs plus a full prune closes that gap: the declaration's own last-known
// evidence (declBefore) is real, non-trivial history built from an ACTUAL
// run, not a zero value that happened to look the same either way.
//
// The genuinely different scenario — a declaration pointing at an OLDER,
// now-pruned run while a NEWER complete run still exists — renders
// not_seen, not unknown (mapNodeDeclaration never checks whether
// LastDiscoveryRunID resolves to a live row; it only compares it against
// the CURRENT latestRun.ID), and is covered separately by
// TestNotSeenPreservesDanglingEvidenceWhileNamingTheRunThatMissedIt below.
func TestAllDiscoveryHistoryPrunedRendersUnknownNeverBlank(t *testing.T) {
	nodes := &fakeNodeLister{views: []inventory.NodeView{liveNodeView("shed-01")}}
	clock := incrementingClock(testNow, time.Minute)
	deps, st, storeDir := newTestDiscoveryDeps(t, clock, nodes, nil)
	admin := mustCreatePrincipal(t, deps.Identity, "admin-1", identity.RoleAdmin)
	adminToken := mustIssueToken(t, deps.Identity, admin.ID)
	api := New(deps, Options{Clock: clock, Logger: testLogger()})
	ctx := context.Background()

	if _, err := st.DeclareNode(ctx, store.NodeDeclarationRecord{NodeID: "shed-01"}); err != nil {
		t.Fatalf("declare node: %v", err)
	}

	runDiscovery := func() {
		t.Helper()
		req := httptest.NewRequest(http.MethodPost, "/api/v1/discovery/runs", nil)
		req.Header.Set("Authorization", "Bearer "+adminToken)
		resp, body := doRawRequest(t, api.Handler, req)
		if resp.StatusCode != http.StatusOK {
			t.Fatalf("discovery run status = %d, want 200; body: %s", resp.StatusCode, body)
		}
	}
	runDiscovery() // run 1: sees shed-01.
	runDiscovery() // run 2: also sees shed-01 — decl now points at run 2.

	declBefore, err := st.GetNodeDeclaration(ctx, "shed-01")
	if err != nil {
		t.Fatalf("get node declaration: %v", err)
	}
	if declBefore.LastDiscoveryRunID == "" {
		t.Fatalf("declaration has no LastDiscoveryRunID after two runs that observed it")
	}

	// Simulate discovery_runs retention pruning EVERY row (both runs), not
	// merely the one this declaration happens to point at — exactly as
	// migrations.go's schemaV6 doc comment describes ("discovery_runs is
	// pruned by retention and node_declarations is not"). A second raw
	// connection, matching installFailAuditTrigger's own pattern, since
	// this package has no other way to reach the underlying *sql.DB.
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
		t.Fatalf("discoveryState = %v, want \"unknown\" once every discovery run has been pruned", declaration["discoveryState"])
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

// TestNotSeenPreservesDanglingEvidenceWhileNamingTheRunThatMissedIt is the
// review's own "genuine dangling case": a declaration pointing at a PRUNED
// run while a NEWER, COMPLETE run exists and did not see it. This renders
// not_seen (never unknown — a newer complete run's absence claim is real
// evidence), and is DEFECT 8's own regression coverage: lastDiscoveryRunId/
// lastDiscoveredAt must keep reporting the declaration's OWN (now-dangling,
// pruned) last-seen bookkeeping, never the identity of the run that just
// failed to see it — that run is named in notSeenAsOfRunId/
// notSeenAsOfRunFinishedAt instead. Confirmed to fail before the fix: the
// old code overwrote out.LastDiscoveryRunID/out.LastDiscoveredAt with
// latestRun.ID/latestRun.FinishedAt in the not_seen branch, which this
// test's lastDiscoveryRunId assertion below catches immediately (it would
// report run 2's id, not run 1's).
func TestNotSeenPreservesDanglingEvidenceWhileNamingTheRunThatMissedIt(t *testing.T) {
	nodes := &fakeNodeLister{views: []inventory.NodeView{liveNodeView("shed-01")}}
	clock := incrementingClock(testNow, time.Minute)
	deps, st, storeDir := newTestDiscoveryDeps(t, clock, nodes, nil)
	admin := mustCreatePrincipal(t, deps.Identity, "admin-1", identity.RoleAdmin)
	adminToken := mustIssueToken(t, deps.Identity, admin.ID)
	api := New(deps, Options{Clock: clock, Logger: testLogger()})
	ctx := context.Background()

	if _, err := st.DeclareNode(ctx, store.NodeDeclarationRecord{NodeID: "shed-01"}); err != nil {
		t.Fatalf("declare node: %v", err)
	}

	// Run 1: shed-01 is observed. decl.LastDiscoveryRunID = run1.ID.
	run1Req := httptest.NewRequest(http.MethodPost, "/api/v1/discovery/runs", nil)
	run1Req.Header.Set("Authorization", "Bearer "+adminToken)
	run1Resp, run1Body := doRawRequest(t, api.Handler, run1Req)
	if run1Resp.StatusCode != http.StatusOK {
		t.Fatalf("run 1 status = %d, want 200; body: %s", run1Resp.StatusCode, run1Body)
	}
	run1ID, _ := decodeMap(t, run1Body)["run"].(map[string]any)["id"].(string)
	declAfterRun1, err := st.GetNodeDeclaration(ctx, "shed-01")
	if err != nil {
		t.Fatalf("get node declaration after run 1: %v", err)
	}
	if declAfterRun1.LastDiscoveryRunID != run1ID {
		t.Fatalf("declaration.LastDiscoveryRunID = %q after run 1, want %q", declAfterRun1.LastDiscoveryRunID, run1ID)
	}

	// Prune run 1's row specifically — its id is now dangling — BEFORE
	// run 2 happens, mirroring retention pruning an old run while newer
	// ones keep landing.
	dbPath := filepath.Join(storeDir, "showmesh.db")
	raw, err := sql.Open("sqlite", "file:"+dbPath)
	if err != nil {
		t.Fatalf("open raw connection: %v", err)
	}
	defer func() { _ = raw.Close() }()
	if _, err := raw.ExecContext(ctx, `DELETE FROM discovery_runs WHERE id = ?`, run1ID); err != nil {
		t.Fatalf("prune run 1: %v", err)
	}

	// shed-01 goes offline; run 2 does NOT observe it, so decl's own
	// LastDiscoveryRunID stays at the now-dangling run1ID.
	nodes.setViews([]inventory.NodeView{offlineNodeView("shed-01")})
	run2Req := httptest.NewRequest(http.MethodPost, "/api/v1/discovery/runs", nil)
	run2Req.Header.Set("Authorization", "Bearer "+adminToken)
	run2Resp, run2Body := doRawRequest(t, api.Handler, run2Req)
	if run2Resp.StatusCode != http.StatusOK {
		t.Fatalf("run 2 status = %d, want 200; body: %s", run2Resp.StatusCode, run2Body)
	}
	run2ID, _ := decodeMap(t, run2Body)["run"].(map[string]any)["id"].(string)

	getReq := httptest.NewRequest(http.MethodGet, "/api/v1/nodes/shed-01", nil)
	getReq.Header.Set("Authorization", "Bearer "+adminToken)
	getResp, getBody := doRawRequest(t, api.Handler, getReq)
	if getResp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200; body: %s", getResp.StatusCode, getBody)
	}
	declaration := decodeMap(t, getBody)["node"].(map[string]any)["declaration"].(map[string]any)

	if declaration["discoveryState"] != "not_seen" {
		t.Fatalf("discoveryState = %v, want \"not_seen\" (run 2 is complete and did not see shed-01)", declaration["discoveryState"])
	}
	// DEFECT 8: lastDiscoveryRunId/lastDiscoveredAt are THIS DECLARATION's
	// own (dangling, pruned) evidence — run 1 — never run 2's identity.
	if declaration["lastDiscoveryRunId"] != run1ID {
		t.Errorf("lastDiscoveryRunId = %v, want the declaration's OWN dangling run id %q, never the run that just missed it",
			declaration["lastDiscoveryRunId"], run1ID)
	}
	if declaration["lastDiscoveredAt"] == nil {
		t.Errorf("lastDiscoveredAt = nil, want the declaration's own (dangling) last-seen time, never blank")
	}
	// The run that DID fail to see it is named separately.
	if declaration["notSeenAsOfRunId"] != run2ID {
		t.Errorf("notSeenAsOfRunId = %v, want the run that just missed it (%q)", declaration["notSeenAsOfRunId"], run2ID)
	}
	if declaration["notSeenAsOfRunFinishedAt"] == nil {
		t.Errorf("notSeenAsOfRunFinishedAt = nil, want run 2's finish time")
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

// --- DEFECT 2: ambiguous evidence (a broker outage) must never manufacture absence ---

// TestAmbiguousLivenessDuringARunLeavesItIncompleteRatherThanManufacturingAbsence
// is the review's own broker-outage scenario: a node's evidence ages past
// its staleness window (LivenessUnknown — NO evidence either way), not a
// last-will "offline" (LivenessOffline — real evidence of absence). Before
// the fix, this test's run.complete assertion fails (the old code treated
// LivenessUnknown identically to LivenessOffline and finished complete=true
// unconditionally), and the declaration's discoveryState assertion
// ALSO fails once that complete=true run is read back — it renders
// not_seen for equipment nobody has any evidence is actually gone.
func TestAmbiguousLivenessDuringARunLeavesItIncompleteRatherThanManufacturingAbsence(t *testing.T) {
	nodes := &fakeNodeLister{views: []inventory.NodeView{liveNodeView("shed-01")}}
	clock := incrementingClock(testNow, time.Minute)
	deps, st, _ := newTestDiscoveryDeps(t, clock, nodes, nil)
	admin := mustCreatePrincipal(t, deps.Identity, "admin-1", identity.RoleAdmin)
	adminToken := mustIssueToken(t, deps.Identity, admin.ID)
	api := New(deps, Options{Clock: clock, Logger: testLogger()})
	ctx := context.Background()

	if _, err := st.DeclareNode(ctx, store.NodeDeclarationRecord{NodeID: "shed-01"}); err != nil {
		t.Fatalf("declare node: %v", err)
	}

	// Run 1: shed-01 is genuinely online — establish real prior evidence.
	run1Req := httptest.NewRequest(http.MethodPost, "/api/v1/discovery/runs", nil)
	run1Req.Header.Set("Authorization", "Bearer "+adminToken)
	run1Resp, run1Body := doRawRequest(t, api.Handler, run1Req)
	if run1Resp.StatusCode != http.StatusOK {
		t.Fatalf("run 1 status = %d, want 200; body: %s", run1Resp.StatusCode, run1Body)
	}

	// A broker outage: shed-01's heartbeat ages past staleness with NO
	// fresh last-will either way — LivenessUnknown, not LivenessOffline.
	unknownView := liveNodeView("shed-01")
	unknownView.Liveness = inventory.LivenessUnknown
	unknownView.LivenessReason = "health evidence is past the staleness window (broker outage simulation)"
	nodes.setViews([]inventory.NodeView{unknownView})

	run2Req := httptest.NewRequest(http.MethodPost, "/api/v1/discovery/runs", nil)
	run2Req.Header.Set("Authorization", "Bearer "+adminToken)
	run2Resp, run2Body := doRawRequest(t, api.Handler, run2Req)
	if run2Resp.StatusCode != http.StatusOK {
		t.Fatalf("run 2 status = %d, want 200; body: %s", run2Resp.StatusCode, run2Body)
	}
	run2 := decodeMap(t, run2Body)["run"].(map[string]any)
	if run2["complete"] != false {
		t.Fatalf("run 2 (all evidence ambiguous) complete = %v, want false — an ambiguous-evidence run must never claim it may assert absence", run2["complete"])
	}
	reason, _ := run2["reason"].(string)
	if reason == "" {
		t.Errorf("run 2 reason = %q, want a stated reason", reason)
	}

	getReq := httptest.NewRequest(http.MethodGet, "/api/v1/nodes/shed-01", nil)
	getReq.Header.Set("Authorization", "Bearer "+adminToken)
	getResp, getBody := doRawRequest(t, api.Handler, getReq)
	if getResp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200; body: %s", getResp.StatusCode, getBody)
	}
	declaration := decodeMap(t, getBody)["node"].(map[string]any)["declaration"].(map[string]any)
	if declaration["discoveryState"] != "unknown" {
		t.Fatalf(`discoveryState = %v after a broker-outage run, want "unknown" (NEVER "not_seen" — nothing has confirmed shed-01 is actually gone)`,
			declaration["discoveryState"])
	}
}

// TestGenuinelyOfflineNodeStillRendersNotSeenAfterTheFix confirms DEFECT 2's
// fix did not overcorrect: a node with REAL evidence of absence (a last-will
// "online: false" — LivenessOffline, not LivenessUnknown) must still
// support not_seen once a run completes. This is Step 5's pruning rule's
// entire point ("a genuinely powered-off node must still report not_seen"),
// restated here as the control for the ambiguous-evidence test above.
func TestGenuinelyOfflineNodeStillRendersNotSeenAfterTheFix(t *testing.T) {
	nodes := &fakeNodeLister{views: []inventory.NodeView{liveNodeView("shed-01")}}
	clock := incrementingClock(testNow, time.Minute)
	deps, st, _ := newTestDiscoveryDeps(t, clock, nodes, nil)
	admin := mustCreatePrincipal(t, deps.Identity, "admin-1", identity.RoleAdmin)
	adminToken := mustIssueToken(t, deps.Identity, admin.ID)
	api := New(deps, Options{Clock: clock, Logger: testLogger()})
	ctx := context.Background()

	if _, err := st.DeclareNode(ctx, store.NodeDeclarationRecord{NodeID: "shed-01"}); err != nil {
		t.Fatalf("declare node: %v", err)
	}

	runDiscovery := func() map[string]any {
		t.Helper()
		req := httptest.NewRequest(http.MethodPost, "/api/v1/discovery/runs", nil)
		req.Header.Set("Authorization", "Bearer "+adminToken)
		resp, body := doRawRequest(t, api.Handler, req)
		if resp.StatusCode != http.StatusOK {
			t.Fatalf("discovery run status = %d, want 200; body: %s", resp.StatusCode, body)
		}
		return decodeMap(t, body)["run"].(map[string]any)
	}
	runDiscovery()

	nodes.setViews([]inventory.NodeView{offlineNodeView("shed-01")})
	run2 := runDiscovery()
	if run2["complete"] != true {
		t.Fatalf("run 2 (genuinely offline, real evidence) complete = %v, want true", run2["complete"])
	}

	getReq := httptest.NewRequest(http.MethodGet, "/api/v1/nodes/shed-01", nil)
	getReq.Header.Set("Authorization", "Bearer "+adminToken)
	getResp, getBody := doRawRequest(t, api.Handler, getReq)
	if getResp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200; body: %s", getResp.StatusCode, getBody)
	}
	declaration := decodeMap(t, getBody)["node"].(map[string]any)["declaration"].(map[string]any)
	if declaration["discoveryState"] != "not_seen" {
		t.Errorf("discoveryState = %v, want \"not_seen\" for equipment with real evidence of being offline", declaration["discoveryState"])
	}
}

// --- DEFECT 3: an FPP instance counts as observed only on actual collection evidence ---

// TestNeverPolledFPPInstanceDoesNotCountAsObserved is DEFECT 3's own
// regression: apiwiring's fppInstanceLister returns a view for every
// CONFIGURED FPP endpoint, synthesizing not_collected placeholders when the
// store holds nothing — before the fix, that alone was enough to count as
// "observed" on every run. neverPolledFPPView (LastPollAt nil) is exactly
// that placeholder shape. Confirmed to fail before the fix: player-01 would
// appear as a proposal and inflate foundCount to 1.
func TestNeverPolledFPPInstanceDoesNotCountAsObserved(t *testing.T) {
	fpp := &fakeFPPLister{views: []FPPInstanceView{neverPolledFPPView("player-01")}}
	deps, _, _ := newTestDiscoveryDeps(t, fixedClock(testNow), nil, fpp)
	admin := mustCreatePrincipal(t, deps.Identity, "admin-1", identity.RoleAdmin)
	adminToken := mustIssueToken(t, deps.Identity, admin.ID)
	api := New(deps, Options{Clock: fixedClock(testNow), Logger: testLogger()})

	req := httptest.NewRequest(http.MethodPost, "/api/v1/discovery/runs", nil)
	req.Header.Set("Authorization", "Bearer "+adminToken)
	resp, body := doRawRequest(t, api.Handler, req)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200; body: %s", resp.StatusCode, body)
	}
	result := decodeMap(t, body)
	proposals, _ := result["proposals"].([]any)
	if len(proposals) != 0 {
		t.Fatalf("proposals = %+v, want none — an instance that has never been successfully polled is configuration, not evidence", proposals)
	}
	run := result["run"].(map[string]any)
	if run["foundCount"] != float64(0) {
		t.Errorf("run.foundCount = %v, want 0", run["foundCount"])
	}
	// player-01 is not DECLARED here, so its ambiguous evidence blocks no
	// absence claim (nothing renders not_seen for an undeclared entity
	// either way) — this run is free to finish complete=true. See
	// TestDeclaredNeverPolledFPPInstanceLeavesRunIncomplete below for the
	// declared case, where ambiguous evidence DOES have an absence claim
	// riding on it and must block completeness.
	if run["complete"] != true {
		t.Errorf("run.complete = %v, want true (nothing declared has ambiguous evidence)", run["complete"])
	}
}

// TestDeclaredNeverPolledFPPInstanceLeavesRunIncomplete is DEFECT 2's own
// extension to the FPP branch, named in this seam's own report rather than
// the review's numbered findings: a DECLARED FPP instance with no poll
// evidence yet (e.g. immediately after a coordinator restart, before the
// FPP collector's first cycle completes) must not be able to render
// not_seen off a run that never actually checked it either way — the exact
// same failure DEFECT 2 fixes for nodes, reachable here through DEFECT 3's
// own fix (fppInstanceObserved requiring real evidence) once ambiguous
// evidence is distinguished from negative evidence.
func TestDeclaredNeverPolledFPPInstanceLeavesRunIncomplete(t *testing.T) {
	fpp := &fakeFPPLister{views: []FPPInstanceView{neverPolledFPPView("player-01")}}
	deps, st, _ := newTestDiscoveryDeps(t, fixedClock(testNow), nil, fpp)
	admin := mustCreatePrincipal(t, deps.Identity, "admin-1", identity.RoleAdmin)
	adminToken := mustIssueToken(t, deps.Identity, admin.ID)
	api := New(deps, Options{Clock: fixedClock(testNow), Logger: testLogger()})

	if _, err := st.DeclareNode(context.Background(), store.NodeDeclarationRecord{NodeID: "player-01"}); err != nil {
		t.Fatalf("declare node: %v", err)
	}

	req := httptest.NewRequest(http.MethodPost, "/api/v1/discovery/runs", nil)
	req.Header.Set("Authorization", "Bearer "+adminToken)
	resp, body := doRawRequest(t, api.Handler, req)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200; body: %s", resp.StatusCode, body)
	}
	run := decodeMap(t, body)["run"].(map[string]any)
	if run["complete"] != false {
		t.Fatalf("run.complete = %v, want false — player-01 is declared and has never been polled, so this run has no evidence to assert its absence with", run["complete"])
	}
}

// TestUnreachableFPPInstanceRendersNotSeenOnceConfirmed is DEFECT 3's
// control: a genuinely unreachable FPP instance (a poll DID complete and
// DID fail — real negative evidence) must still support not_seen, exactly
// like an unplugged FPP that the review's own example named. This is what
// distinguishes fppInstanceEvidenceAmbiguous (LastPollAt nil) from a
// failed poll (LastPollAt set, LastPollError set): only the former blocks
// a run's completeness.
func TestUnreachableFPPInstanceRendersNotSeenOnceConfirmed(t *testing.T) {
	fpp := &fakeFPPLister{views: []FPPInstanceView{polledFPPView("player-01")}}
	clock := incrementingClock(testNow, time.Minute)
	deps, st, _ := newTestDiscoveryDeps(t, clock, nil, fpp)
	admin := mustCreatePrincipal(t, deps.Identity, "admin-1", identity.RoleAdmin)
	adminToken := mustIssueToken(t, deps.Identity, admin.ID)
	api := New(deps, Options{Clock: clock, Logger: testLogger()})
	ctx := context.Background()

	if _, err := st.DeclareNode(ctx, store.NodeDeclarationRecord{NodeID: "player-01"}); err != nil {
		t.Fatalf("declare node: %v", err)
	}

	runDiscovery := func() map[string]any {
		t.Helper()
		req := httptest.NewRequest(http.MethodPost, "/api/v1/discovery/runs", nil)
		req.Header.Set("Authorization", "Bearer "+adminToken)
		resp, body := doRawRequest(t, api.Handler, req)
		if resp.StatusCode != http.StatusOK {
			t.Fatalf("discovery run status = %d, want 200; body: %s", resp.StatusCode, body)
		}
		return decodeMap(t, body)["run"].(map[string]any)
	}
	runDiscovery() // player-01 is reachable and confirmed.

	fpp.views = []FPPInstanceView{failedPollFPPView("player-01")}
	run2 := runDiscovery()
	if run2["complete"] != true {
		t.Fatalf("run 2 (a completed, failed poll — real negative evidence) complete = %v, want true", run2["complete"])
	}

	getReq := httptest.NewRequest(http.MethodGet, "/api/v1/nodes/player-01", nil)
	getReq.Header.Set("Authorization", "Bearer "+adminToken)
	getResp, getBody := doRawRequest(t, api.Handler, getReq)
	if getResp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200; body: %s", getResp.StatusCode, getBody)
	}
	declaration := decodeMap(t, getBody)["node"].(map[string]any)["declaration"].(map[string]any)
	if declaration["discoveryState"] != "not_seen" {
		t.Errorf("discoveryState = %v, want \"not_seen\" for an FPP instance confirmed unreachable", declaration["discoveryState"])
	}
}

// --- DEFECT 4: a declared node with no inventory row still renders, and is deletable ---

// TestDeclaredNodeNeverObservedAppearsInListingsAndCanBeDeleted covers the
// review's reachability finding: promoting an FPP-sourced (or any)
// proposal that has no corresponding [NodeLister]/[FPPLister] entry used
// to vanish from every surface with no way back except a direct
// DELETE/curl call. ghost-01 here is declared directly (bypassing
// discovery, matching how an FPP instance id declaration behaves — it is
// never returned by [NodeLister] at all) and must still appear on both
// GET /api/v1/nodes and GET /api/v1/nodes/{nodeId}, honestly flagged as
// never observed, and remain deletable through the ordinary endpoint.
func TestDeclaredNodeNeverObservedAppearsInListingsAndCanBeDeleted(t *testing.T) {
	deps, st, _ := newTestDiscoveryDeps(t, fixedClock(testNow), nil, nil)
	admin := mustCreatePrincipal(t, deps.Identity, "admin-1", identity.RoleAdmin)
	adminToken := mustIssueToken(t, deps.Identity, admin.ID)
	api := New(deps, Options{Clock: fixedClock(testNow), Logger: testLogger()})

	if _, err := st.DeclareNode(context.Background(), store.NodeDeclarationRecord{NodeID: "ghost-01", Label: "Never said hello"}); err != nil {
		t.Fatalf("declare node: %v", err)
	}

	listReq := httptest.NewRequest(http.MethodGet, "/api/v1/nodes", nil)
	listReq.Header.Set("Authorization", "Bearer "+adminToken)
	listResp, listBody := doRawRequest(t, api.Handler, listReq)
	if listResp.StatusCode != http.StatusOK {
		t.Fatalf("list status = %d, want 200; body: %s", listResp.StatusCode, listBody)
	}
	nodesList := decodeMap(t, listBody)["nodes"].([]any)
	var found map[string]any
	for _, n := range nodesList {
		nm := n.(map[string]any)
		if nm["nodeId"] == "ghost-01" {
			found = nm
		}
	}
	if found == nil {
		t.Fatalf("ghost-01 (declared, never observed) is absent from GET /api/v1/nodes: %+v", nodesList)
	}
	decl := found["declaration"].(map[string]any)
	if decl["declared"] != true {
		t.Errorf("ghost-01 declaration.declared = %v, want true", decl["declared"])
	}
	cp := found["controlPlane"].(map[string]any)
	if cp["state"] != "unknown" {
		t.Errorf("ghost-01 controlPlane.state = %v, want \"unknown\" (never a fabricated \"offline\")", cp["state"])
	}
	if cp["reason"] == nil || cp["reason"] == "" {
		t.Errorf("ghost-01 controlPlane.reason = %v, want a stated reason", cp["reason"])
	}

	getReq := httptest.NewRequest(http.MethodGet, "/api/v1/nodes/ghost-01", nil)
	getReq.Header.Set("Authorization", "Bearer "+adminToken)
	getResp, getBody := doRawRequest(t, api.Handler, getReq)
	if getResp.StatusCode != http.StatusOK {
		t.Fatalf("GET ghost-01 status = %d, want 200; body: %s", getResp.StatusCode, getBody)
	}

	// And it remains reachable by the ordinary delete endpoint — the "no
	// way back" half of the defect.
	delReq := newJSONRequest(t, http.MethodDelete, "/api/v1/nodes/ghost-01/declaration", `{"confirm":true}`, nil)
	delReq.Header.Set("Authorization", "Bearer "+adminToken)
	delResp, delBody := doRawRequest(t, api.Handler, delReq)
	if delResp.StatusCode != http.StatusNoContent {
		t.Fatalf("delete status = %d, want 204; body: %s", delResp.StatusCode, delBody)
	}
	if _, err := st.GetNodeDeclaration(context.Background(), "ghost-01"); err == nil {
		t.Fatalf("ghost-01's declaration still exists after a confirmed delete")
	}
}

// --- DEFECT 6: absent label/notes must never overwrite what is already declared ---

func TestReDeclareWithAbsentFieldsPreservesExistingLabelAndNotes(t *testing.T) {
	deps, _, _ := newTestDiscoveryDeps(t, fixedClock(testNow), nil, nil)
	admin := mustCreatePrincipal(t, deps.Identity, "admin-1", identity.RoleAdmin)
	adminToken := mustIssueToken(t, deps.Identity, admin.ID)
	api := New(deps, Options{Clock: fixedClock(testNow), Logger: testLogger()})

	first := newJSONRequest(t, http.MethodPost, "/api/v1/nodes/roof-01/declaration",
		`{"label":"Roof controller — 2 strings","notes":"west side"}`, nil)
	first.Header.Set("Authorization", "Bearer "+adminToken)
	firstResp, firstBody := doRawRequest(t, api.Handler, first)
	if firstResp.StatusCode != http.StatusOK {
		t.Fatalf("first declare status = %d, want 200; body: %s", firstResp.StatusCode, firstBody)
	}

	// A second declare with NEITHER field present — confirmed to fail
	// before the fix: the old handler decoded a missing "label"/"notes"
	// key to Go's zero value "" indistinguishably from an explicit "",
	// and unconditionally wrote it, erasing the label set above.
	second := newJSONRequest(t, http.MethodPost, "/api/v1/nodes/roof-01/declaration", `{}`, nil)
	second.Header.Set("Authorization", "Bearer "+adminToken)
	secondResp, secondBody := doRawRequest(t, api.Handler, second)
	if secondResp.StatusCode != http.StatusOK {
		t.Fatalf("second declare status = %d, want 200; body: %s", secondResp.StatusCode, secondBody)
	}
	decl := decodeMap(t, secondBody)["declaration"].(map[string]any)
	if decl["label"] != "Roof controller — 2 strings" {
		t.Errorf("label after a re-declare with no --label = %v, want the ORIGINAL label preserved", decl["label"])
	}
	if decl["notes"] != "west side" {
		t.Errorf("notes after a re-declare with no --notes = %v, want the ORIGINAL notes preserved", decl["notes"])
	}

	// An explicit null is the same as absent.
	third := newJSONRequest(t, http.MethodPost, "/api/v1/nodes/roof-01/declaration", `{"label":null}`, nil)
	third.Header.Set("Authorization", "Bearer "+adminToken)
	thirdResp, thirdBody := doRawRequest(t, api.Handler, third)
	if thirdResp.StatusCode != http.StatusOK {
		t.Fatalf("third declare status = %d, want 200; body: %s", thirdResp.StatusCode, thirdBody)
	}
	declThird := decodeMap(t, thirdBody)["declaration"].(map[string]any)
	if declThird["label"] != "Roof controller — 2 strings" {
		t.Errorf("label after an explicit null = %v, want the ORIGINAL label preserved", declThird["label"])
	}

	// An explicit empty string DOES clear it — the distinction this fix exists to make representable.
	fourth := newJSONRequest(t, http.MethodPost, "/api/v1/nodes/roof-01/declaration", `{"label":""}`, nil)
	fourth.Header.Set("Authorization", "Bearer "+adminToken)
	fourthResp, fourthBody := doRawRequest(t, api.Handler, fourth)
	if fourthResp.StatusCode != http.StatusOK {
		t.Fatalf("fourth declare status = %d, want 200; body: %s", fourthResp.StatusCode, fourthBody)
	}
	declFourth := decodeMap(t, fourthBody)["declaration"].(map[string]any)
	if declFourth["label"] != nil {
		t.Errorf("label after an explicit empty string = %v, want cleared (null on the wire — nonEmptyStrPtr)", declFourth["label"])
	}
	if declFourth["notes"] != "west side" {
		t.Errorf("notes after a label-only request = %v, want unchanged", declFourth["notes"])
	}
}

// --- DEFECT 7a: concurrent discovery runs are refused, never queued ---

// blockingNodeLister is [NodeLister] whose Snapshot blocks until released,
// closing entered exactly once on first entry — used to guarantee a
// discovery run is genuinely IN FLIGHT (past the concurrency guard, deep
// inside handleStartDiscoveryRun) before this test issues a second,
// overlapping request, rather than racing goroutine scheduling the way
// CLAUDE.md's own standing lesson warns against.
type blockingNodeLister struct {
	entered chan struct{}
	release chan struct{}
	once    sync.Once
}

func (b *blockingNodeLister) Snapshot(context.Context, time.Time) ([]inventory.NodeView, error) {
	b.once.Do(func() { close(b.entered) })
	<-b.release
	return nil, nil
}

// TestConcurrentDiscoveryRunsAreRefusedNotQueued is confirmed to fail
// before the fix: with handleStartDiscoveryRun's discoveryRunInFlight
// guard removed, the second, overlapping request proceeds normally and
// this test's 409 assertion fails (it observes 200 instead).
func TestConcurrentDiscoveryRunsAreRefusedNotQueued(t *testing.T) {
	dir := t.TempDir()
	st, err := store.Open(context.Background(), filepath.Join(dir, "db"), nil, store.WithClock(fixedClock(testNow)))
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(func() { _ = st.Close() })
	svc := identity.NewService(st, fixedClock(testNow), filepath.Join(dir, "data"), identity.WithLogger(testLogger()))
	blocker := &blockingNodeLister{entered: make(chan struct{}), release: make(chan struct{})}
	deps := Dependencies{
		Nodes: blocker, FPP: &fakeFPPLister{}, Observations: &fakeObservationLister{},
		Events: &fakeEventReader{}, Collectors: &fakeCollectorStatusLister{},
		Identity: svc, Discovery: st,
	}
	admin := mustCreatePrincipal(t, svc, "admin-1", identity.RoleAdmin)
	adminToken := mustIssueToken(t, svc, admin.ID)
	api := New(deps, Options{Clock: fixedClock(testNow), Logger: testLogger()})

	// The first request runs in its own goroutine and blocks inside
	// Nodes.Snapshot until released below. httptest.NewRecorder plus a
	// manual body read here, deliberately NOT doRawRequest/t.Fatalf: a
	// background goroutine must never call a *testing.T failure method
	// (see the testing package's own documented restriction), so any
	// assertion on the first response happens after this goroutine hands
	// its result back over firstStatus.
	firstStatus := make(chan int, 1)
	go func() {
		req := httptest.NewRequest(http.MethodPost, "/api/v1/discovery/runs", nil)
		req.Header.Set("Authorization", "Bearer "+adminToken)
		rec := httptest.NewRecorder()
		api.Handler.ServeHTTP(rec, req)
		_, _ = io.ReadAll(rec.Result().Body)
		firstStatus <- rec.Result().StatusCode
	}()

	select {
	case <-blocker.entered:
	case <-time.After(5 * time.Second):
		t.Fatal("first discovery run never reached Nodes.Snapshot")
	}

	// Second request, issued while the first is provably still in flight.
	secondReq := httptest.NewRequest(http.MethodPost, "/api/v1/discovery/runs", nil)
	secondReq.Header.Set("Authorization", "Bearer "+adminToken)
	secondResp, secondBody := doRawRequest(t, api.Handler, secondReq)
	if secondResp.StatusCode != http.StatusConflict {
		t.Fatalf("second (overlapping) discovery run status = %d, want 409; body: %s", secondResp.StatusCode, secondBody)
	}
	problem := decodeMap(t, secondBody)
	if problem["type"] != ProblemTypeConflict {
		t.Errorf("second run problem type = %v, want %q", problem["type"], ProblemTypeConflict)
	}

	close(blocker.release)
	select {
	case status := <-firstStatus:
		if status != http.StatusOK {
			t.Fatalf("first discovery run status = %d, want 200", status)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("first discovery run never finished after being released")
	}

	// A THIRD run, issued only after the first has fully finished, must
	// succeed — proving the guard releases rather than wedging the
	// endpoint permanently.
	thirdReq := httptest.NewRequest(http.MethodPost, "/api/v1/discovery/runs", nil)
	thirdReq.Header.Set("Authorization", "Bearer "+adminToken)
	thirdResp, thirdBody := doRawRequest(t, api.Handler, thirdReq)
	if thirdResp.StatusCode != http.StatusOK {
		t.Fatalf("run after the first finished status = %d, want 200; body: %s", thirdResp.StatusCode, thirdBody)
	}
}

// --- DEFECT 7c: ListDiscoveryRuns' latest-run ordering has a deterministic tiebreaker ---

// TestLatestDiscoveryRunTiebreaksOnInsertionOrder confirms
// store.Store.ListDiscoveryRuns' `ORDER BY started_at DESC, rowid DESC`
// resolves an exact started_at tie deterministically: the run inserted
// SECOND (rowid is strictly insertion-ordered) is "the most recent run",
// regardless of what an identical clock reading would otherwise leave
// unspecified. Drives the real store directly — this is a SQL ordering
// guarantee, not something an HTTP round trip through this package would
// exercise any more precisely.
func TestLatestDiscoveryRunTiebreaksOnInsertionOrder(t *testing.T) {
	dir := t.TempDir()
	// A clock that never advances: every run in this test shares the exact
	// same started_at, so ONLY the tiebreaker can distinguish them.
	frozen := fixedClock(testNow)
	st, err := store.Open(context.Background(), filepath.Join(dir, "db"), nil, store.WithClock(frozen))
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(func() { _ = st.Close() })
	ctx := context.Background()

	if _, err := st.StartDiscoveryRun(ctx, store.DiscoveryRunRecord{ID: "run-a"}); err != nil {
		t.Fatalf("start run-a: %v", err)
	}
	if err := st.FinishDiscoveryRun(ctx, "run-a", true, "", 0); err != nil {
		t.Fatalf("finish run-a: %v", err)
	}
	if _, err := st.StartDiscoveryRun(ctx, store.DiscoveryRunRecord{ID: "run-b"}); err != nil {
		t.Fatalf("start run-b: %v", err)
	}
	if err := st.FinishDiscoveryRun(ctx, "run-b", true, "", 0); err != nil {
		t.Fatalf("finish run-b: %v", err)
	}

	runs, err := st.ListDiscoveryRuns(ctx, 1)
	if err != nil {
		t.Fatalf("list discovery runs: %v", err)
	}
	if len(runs) != 1 {
		t.Fatalf("ListDiscoveryRuns(limit=1) returned %d rows, want 1", len(runs))
	}
	if runs[0].ID != "run-b" {
		t.Errorf("latest run = %q, want %q (inserted second, identical started_at)", runs[0].ID, "run-b")
	}
}
