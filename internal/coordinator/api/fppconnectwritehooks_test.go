package api

import (
	"net/http"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/showmeshsystems/showmesh/internal/coordinator/identity"
	"github.com/showmeshsystems/showmesh/internal/coordinator/inventory"
)

// This file proves Track E phase 2 seam FC1a's own write-hook contract
// (ADR-044 decision 5): a write to show.surface, show.active, show,
// fppconnect.settings, or (FC3, ADR-028 decision 8) assets.settings
// triggers exactly one fppconnect.configure push per affected node.
// pushFPPConnectToNode/pushFPPConnectToAllNodes
// (fppconnectsettingsconfig.go) fire their pushes in a detached goroutine,
// matching pushAudioSettingsToAllNodes' identical fire-and-forget shape
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

// fppConnectConfigureTargetNodeIDs returns the target node id of every
// fppconnect.configure publish pub recorded, in publish order, decoded
// from each entry's own topic (mqttproto.CmdTopic's own
// "showmesh/nodes/<node-id>/cmd" shape, matched directly here rather than
// through an mqttproto import, following this file's own established
// avoid-importing-the-wire-type convention, see fakeRenderPublisher's own
// mqttCmdEnvelopeForTest).
func fppConnectConfigureTargetNodeIDs(t *testing.T, pub *fakeRenderPublisher) []string {
	t.Helper()
	pub.mu.Lock()
	defer pub.mu.Unlock()
	const prefix, suffix = "showmesh/nodes/", "/cmd"
	var ids []string
	for i, env := range pub.payload {
		if env.Payload.Action != "fppconnect.configure" {
			continue
		}
		topic := pub.topics[i]
		if !strings.HasPrefix(topic, prefix) || !strings.HasSuffix(topic, suffix) {
			t.Fatalf("fppconnect.configure publish %d has an unexpected topic shape: %q", i, topic)
		}
		ids = append(ids, strings.TrimSuffix(strings.TrimPrefix(topic, prefix), suffix))
	}
	return ids
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
	total := len(pub.payload)
	pub.mu.Unlock()
	if total != 1 {
		t.Fatalf("published %d commands, want exactly 1 (only render-01)", total)
	}

	// review round 8 finding 4: assert the target node id itself, not
	// only the count, so a push wrongly aimed at render-02 would fail
	// this test even if the total count still happened to match.
	if got := fppConnectConfigureTargetNodeIDs(t, pub); len(got) != 1 || got[0] != "render-01" {
		t.Fatalf("fppconnect.configure target node ids = %v, want exactly [render-01]", got)
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

	// review round 8 finding 4: assert the actual target node ids, not
	// only the count, so a defect that pushed the wrong node (or the
	// right node the wrong number of times) would fail this test even
	// though the total count of 3 still matched by coincidence. render-01
	// is targeted twice (the first PUT, then vacating the surface);
	// render-02 is targeted once (the surface's new node).
	got := fppConnectConfigureTargetNodeIDs(t, pub)
	sort.Strings(got)
	want := []string{"render-01", "render-01", "render-02"}
	if len(got) != len(want) {
		t.Fatalf("fppconnect.configure target node ids = %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("fppconnect.configure target node ids = %v, want %v", got, want)
		}
	}
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

// TestPutAssetsSettingsPushesEveryInventoryNode proves a write to
// assets.settings is the fifth write-hook trigger (FC3, ADR-028 decision
// 8): a node's own coordinatorBaseUrl comes from this same push, so a
// changed contentBaseUrl must reach every inventory node exactly like a
// show/show.active/fppconnect.settings write already does, not only on
// that node's next hello.
func TestPutAssetsSettingsPushesEveryInventoryNode(t *testing.T) {
	svc, st, _ := newTestIdentityServiceWithStore(t, fixedClock(testNow))
	admin := mustCreatePrincipal(t, svc, "admin-1", identity.RoleAdmin)
	token := mustIssueToken(t, svc, admin.ID)

	deps := configTestDeps(svc, st)
	pub := &fakeRenderPublisher{}
	deps.RenderPublisher = pub
	nodes := &fakeNodeLister{}
	nodes.setViews([]inventory.NodeView{{NodeID: "render-01"}, {NodeID: "render-02"}, {NodeID: "render-03"}})
	deps.Nodes = nodes
	api := New(deps, Options{Clock: fixedClock(testNow), Logger: testLogger()})

	req := newJSONRequest(t, http.MethodPut, "/api/v1/config/assets.settings", validAssetsSettingsBody, map[string]string{"Authorization": "Bearer " + token})
	resp, body := doRawRequest(t, api.Handler, req)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("PUT assets.settings status = %d, want 200; body: %s", resp.StatusCode, body)
	}

	waitForPublishCount(t, pub, "fppconnect.configure", 3)

	pub.mu.Lock()
	defer pub.mu.Unlock()
	if len(pub.payload) != 3 {
		t.Fatalf("published %d commands, want exactly 3 (one per inventory node)", len(pub.payload))
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
