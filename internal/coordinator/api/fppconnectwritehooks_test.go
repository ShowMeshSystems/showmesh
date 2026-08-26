package api

import (
	"net/http"
	"testing"
	"time"

	"github.com/showmeshsystems/showmesh/internal/coordinator/identity"
	"github.com/showmeshsystems/showmesh/internal/coordinator/inventory"
)

// This file proves Track E phase 2 seam FC1a's own write-hook contract
// (ADR-044 decision 5): a write to show.surface, show.active, show, or
// fppconnect.settings triggers exactly one fppconnect.configure push per
// affected node. pushFPPConnectToNode/pushFPPConnectToAllNodes
// (fppconnectsettingsconfig.go) fire their pushes in a detached goroutine
//, matching pushAudioSettingsToAllNodes' identical fire-and-forget shape
// one kind over, so this file uses fakeRenderPublisher's onPublish hook
// (renderdispatch_test.go) to synchronize on the exact moment a publish
// lands, rather than a fixed sleep.

// waitForPublishCount blocks until pub has recorded at least n publishes
// whose decoded action equals action, or fails the test after a bounded
// wait, long enough for a detached goroutine to run on a loaded CI
// machine, short enough that a genuine defect (a push that never happens)
// fails the test instead of hanging it.
func waitForPublishCount(t *testing.T, pub *fakeRenderPublisher, action string, n int) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for {
		count := 0
		pub.mu.Lock()
		for _, env := range pub.payload {
			if env.Payload.Action == action {
				count++
			}
		}
		pub.mu.Unlock()
		if count >= n {
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("timed out waiting for %d %q publishes, saw %d", n, action, count)
		}
		time.Sleep(5 * time.Millisecond)
	}
}

func TestPutShowSurfacePushesOnlyItsOwnNode(t *testing.T) {
	svc, st, _ := newTestIdentityServiceWithStore(t, fixedClock(testNow))
	admin := mustCreatePrincipal(t, svc, "admin-1", identity.RoleAdmin)
	token := mustIssueToken(t, svc, admin.ID)

	deps := showObjectsTestDeps(svc, st)
	pub := &fakeRenderPublisher{}
	deps.RenderPublisher = pub
	api := New(deps, Options{Clock: fixedClock(testNow), Logger: testLogger()})

	mustDeclareNode(t, st, "render-01")
	mustDeclareNode(t, st, "render-02")
	mustPutShow(t, api, token, "halloween-2026", `{"name":"Halloween 2026"}`)

	req := newJSONRequest(t, http.MethodPut, "/api/v1/config/show.surface/garage-door", validSurfaceBodyNDI, map[string]string{"Authorization": "Bearer " + token})
	resp, body := doRawRequest(t, api.Handler, req)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("PUT show.surface status = %d, want 200; body: %s", resp.StatusCode, body)
	}

	waitForPublishCount(t, pub, "fppconnect.configure", 1)

	// Exactly one push total: the surface's own node (render-01), never
	// render-02, which this surface does not reference.
	pub.mu.Lock()
	defer pub.mu.Unlock()
	if len(pub.payload) != 1 {
		t.Fatalf("published %d commands, want exactly 1 (only render-01)", len(pub.payload))
	}
}

func TestPutShowSurfaceMoveToNewNodePushesBothNodes(t *testing.T) {
	svc, st, _ := newTestIdentityServiceWithStore(t, fixedClock(testNow))
	admin := mustCreatePrincipal(t, svc, "admin-1", identity.RoleAdmin)
	token := mustIssueToken(t, svc, admin.ID)

	deps := showObjectsTestDeps(svc, st)
	pub := &fakeRenderPublisher{}
	deps.RenderPublisher = pub
	api := New(deps, Options{Clock: fixedClock(testNow), Logger: testLogger()})

	mustDeclareNode(t, st, "render-01")
	mustDeclareNode(t, st, "render-02")
	mustPutShow(t, api, token, "halloween-2026", `{"name":"Halloween 2026"}`)

	req1 := newJSONRequest(t, http.MethodPut, "/api/v1/config/show.surface/garage-door", validSurfaceBodyNDI, map[string]string{"Authorization": "Bearer " + token})
	if resp, body := doRawRequest(t, api.Handler, req1); resp.StatusCode != http.StatusOK {
		t.Fatalf("first PUT status = %d, want 200; body: %s", resp.StatusCode, body)
	}
	waitForPublishCount(t, pub, "fppconnect.configure", 1)

	movedBody := `{
		"show": "halloween-2026",
		"name": "Garage Door",
		"node": "render-02",
		"channelRange": {"startChannel": 1, "channelCount": 3600},
		"geometry": {"width": 40, "height": 30, "pixelFormat": "rgb"},
		"frameRate": 40,
		"output": {"transport": "ndi", "ndi": {"sourceName": "ShowMesh Garage"}}
	}`
	req2 := newJSONRequest(t, http.MethodPut, "/api/v1/config/show.surface/garage-door", movedBody, map[string]string{"Authorization": "Bearer " + token})
	if resp, body := doRawRequest(t, api.Handler, req2); resp.StatusCode != http.StatusOK {
		t.Fatalf("second PUT status = %d, want 200; body: %s", resp.StatusCode, body)
	}

	// One push for the first PUT (render-01), then two more for the move
	// (render-02, the new node, and render-01, the vacated one) = 3 total.
	waitForPublishCount(t, pub, "fppconnect.configure", 3)
}

func TestPutShowActivePushesEveryInventoryNode(t *testing.T) {
	svc, st, _ := newTestIdentityServiceWithStore(t, fixedClock(testNow))
	admin := mustCreatePrincipal(t, svc, "admin-1", identity.RoleAdmin)
	token := mustIssueToken(t, svc, admin.ID)

	deps := showObjectsTestDeps(svc, st)
	pub := &fakeRenderPublisher{}
	deps.RenderPublisher = pub
	nodes := &fakeNodeLister{}
	deps.Nodes = nodes
	api := New(deps, Options{Clock: fixedClock(testNow), Logger: testLogger()})

	// Created with no inventory nodes yet, so the "show" write's own
	// all-nodes push (handlePutShow) contributes nothing here, populate
	// inventory only after, so every publish this test counts comes from
	// the show.active write under test.
	mustPutShow(t, api, token, "halloween-2026", `{"name":"Halloween 2026"}`)
	nodes.setViews([]inventory.NodeView{{NodeID: "render-01"}, {NodeID: "render-02"}})

	req := newJSONRequest(t, http.MethodPut, "/api/v1/config/show.active", `{"show":"halloween-2026"}`, map[string]string{"Authorization": "Bearer " + token})
	resp, body := doRawRequest(t, api.Handler, req)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("PUT show.active status = %d, want 200; body: %s", resp.StatusCode, body)
	}

	waitForPublishCount(t, pub, "fppconnect.configure", 2)

	pub.mu.Lock()
	defer pub.mu.Unlock()
	if len(pub.payload) != 2 {
		t.Fatalf("published %d commands, want exactly 2 (one per inventory node)", len(pub.payload))
	}
}

func TestPutFPPConnectSettingsPushesEveryInventoryNode(t *testing.T) {
	svc, st, _ := newTestIdentityServiceWithStore(t, fixedClock(testNow))
	admin := mustCreatePrincipal(t, svc, "admin-1", identity.RoleAdmin)
	token := mustIssueToken(t, svc, admin.ID)

	deps := showConfigTestDeps(svc, st)
	pub := &fakeRenderPublisher{}
	deps.RenderPublisher = pub
	nodes := &fakeNodeLister{}
	nodes.setViews([]inventory.NodeView{{NodeID: "render-01"}, {NodeID: "render-02"}, {NodeID: "render-03"}})
	deps.Nodes = nodes
	api := New(deps, Options{Clock: fixedClock(testNow), Logger: testLogger()})

	req := newJSONRequest(t, http.MethodPut, "/api/v1/config/fppconnect.settings", validFPPConnectSettingsBody, map[string]string{"Authorization": "Bearer " + token})
	resp, body := doRawRequest(t, api.Handler, req)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("PUT fppconnect.settings status = %d, want 200; body: %s", resp.StatusCode, body)
	}

	waitForPublishCount(t, pub, "fppconnect.configure", 3)

	pub.mu.Lock()
	defer pub.mu.Unlock()
	if len(pub.payload) != 3 {
		t.Fatalf("published %d commands, want exactly 3 (one per inventory node)", len(pub.payload))
	}
}
