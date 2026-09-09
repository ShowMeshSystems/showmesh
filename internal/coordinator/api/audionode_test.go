package api

import (
	"net/http"
	"testing"

	"github.com/showmeshsystems/showmesh/internal/coordinator/identity"
	"github.com/showmeshsystems/showmesh/internal/coordinator/inventory"
	"github.com/showmeshsystems/showmesh/internal/coordinator/store"
	"github.com/showmeshsystems/showmesh/pkg/capability"
)

// This file is Track C seam C1b's own test suite for the audio.node
// collection kind: the four routes (mirroring showobjects_test.go's own
// pattern for show.surface) and the placement refusal (seam spec ruling
// 3), which is this kind's one behaviour with no precedent elsewhere in
// this package to copy from.

func nodeViewWithAudioCapabilities(nodeID string, programRoutes, ltcRoutes []string) inventory.NodeView {
	caps := capability.Set{}
	if len(programRoutes) > 0 {
		routes := make([]any, len(programRoutes))
		for i, r := range programRoutes {
			routes[i] = r
		}
		caps = append(caps, capability.Capability{ID: "audio.output.local", Version: 1, Attributes: map[string]any{"routes": routes}})
	}
	if len(ltcRoutes) > 0 {
		routes := make([]any, len(ltcRoutes))
		for i, r := range ltcRoutes {
			routes[i] = r
		}
		caps = append(caps, capability.Capability{ID: "audio.output.ltc", Version: 1, Attributes: map[string]any{"routes": routes}})
	}
	return inventory.NodeView{
		NodeID: nodeID,
		Hello:  &store.HelloRecord{Capabilities: caps},
	}
}

const validAudioNodeBody = `{"programRoute":"hw:0,0","ltcRoute":"hw:0,0","programChannels":[1,2],"ltcChannel":3,` +
	`"clockDomain":"single-interface","clockDomainProvenance":"one physical interface, both routes on it"}`

func mustPutAudioNode(t *testing.T, api *API, token, id, body string) (int, string) {
	t.Helper()
	req := newJSONRequest(t, http.MethodPut, "/api/v1/config/audio.node/"+id, body, map[string]string{"Authorization": "Bearer " + token})
	resp, respBody := doRawRequest(t, api.Handler, req)
	return resp.StatusCode, string(respBody)
}

// TestPutAudioNodeRejectsWithNoAdvertisedEvidence proves a node that has
// never advertised any audio capability is refused, never accepted on the
// operator's claim alone.
//
// Broken and confirmed to fail: removed the ValidateAudioNodePlacement
// call from handlePutAudioNode — this test's status assertion failed
// (200, not 400/409), proving the evidence check is load-bearing rather
// than a no-op. Restored afterward.
func TestPutAudioNodeRejectsWithNoAdvertisedEvidence(t *testing.T) {
	svc, st, _ := newTestIdentityServiceWithStore(t, fixedClock(testNow))
	admin := mustCreatePrincipal(t, svc, "admin-1", identity.RoleAdmin)
	token := mustIssueToken(t, svc, admin.ID)
	deps := showConfigTestDeps(svc, st)
	api := New(deps, Options{Clock: fixedClock(testNow), Logger: testLogger()})

	status, body := mustPutAudioNode(t, api, token, "render-01", validAudioNodeBody)
	if status != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400; body: %s", status, body)
	}
}

// TestPutAudioNodeRejectsUnevidencedRoute proves the refusal names the
// SPECIFIC route that has no evidence, not merely "something is wrong":
// the node has advertised routes, but not the one named in the payload.
func TestPutAudioNodeRejectsUnevidencedRoute(t *testing.T) {
	svc, st, _ := newTestIdentityServiceWithStore(t, fixedClock(testNow))
	admin := mustCreatePrincipal(t, svc, "admin-1", identity.RoleAdmin)
	token := mustIssueToken(t, svc, admin.ID)
	deps := showConfigTestDeps(svc, st)
	deps.Nodes.(*fakeNodeLister).setViews([]inventory.NodeView{
		nodeViewWithAudioCapabilities("render-01", []string{"hw:1,0"}, []string{"hw:1,0"}),
	})
	api := New(deps, Options{Clock: fixedClock(testNow), Logger: testLogger()})

	status, body := mustPutAudioNode(t, api, token, "render-01", validAudioNodeBody)
	if status != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400; body: %s", status, body)
	}
	if !containsAll(body, "hw:0,0") {
		t.Fatalf("body does not name the offending route; body: %s", body)
	}
}

// TestPutAudioNodeAcceptsEvidencedPlacement proves the happy path: the
// node has advertised exactly the routes the payload names, and the write
// succeeds and round-trips through GET.
func TestPutAudioNodeAcceptsEvidencedPlacement(t *testing.T) {
	svc, st, _ := newTestIdentityServiceWithStore(t, fixedClock(testNow))
	admin := mustCreatePrincipal(t, svc, "admin-1", identity.RoleAdmin)
	token := mustIssueToken(t, svc, admin.ID)
	deps := showConfigTestDeps(svc, st)
	deps.Nodes.(*fakeNodeLister).setViews([]inventory.NodeView{
		nodeViewWithAudioCapabilities("render-01", []string{"hw:0,0"}, []string{"hw:0,0"}),
	})
	api := New(deps, Options{Clock: fixedClock(testNow), Logger: testLogger()})

	status, body := mustPutAudioNode(t, api, token, "render-01", validAudioNodeBody)
	if status != http.StatusOK {
		t.Fatalf("status = %d, want 200; body: %s", status, body)
	}

	_, getBody := doRequest(t, api.Handler, "GET", "/api/v1/config/audio.node/render-01", map[string]string{"Authorization": "Bearer " + token})
	if !containsAll(string(getBody), `"programRoute":"hw:0,0"`) {
		t.Fatalf("GET missing programRoute; body: %s", getBody)
	}
	if !containsAll(string(getBody), `"programChannels":[1,2]`) || !containsAll(string(getBody), `"ltcChannel":3`) {
		t.Fatalf("GET missing programChannels/ltcChannel; body: %s", getBody)
	}
}

// TestPutAudioNodeRejectsRouteMismatch proves programRoute and ltcRoute
// naming different routes is refused at decode time, before placement
// evidence is even consulted.
func TestPutAudioNodeRejectsRouteMismatch(t *testing.T) {
	svc, st, _ := newTestIdentityServiceWithStore(t, fixedClock(testNow))
	admin := mustCreatePrincipal(t, svc, "admin-1", identity.RoleAdmin)
	token := mustIssueToken(t, svc, admin.ID)
	deps := showConfigTestDeps(svc, st)
	deps.Nodes.(*fakeNodeLister).setViews([]inventory.NodeView{
		nodeViewWithAudioCapabilities("render-01", []string{"hw:0,0"}, []string{"hw:1,0"}),
	})
	api := New(deps, Options{Clock: fixedClock(testNow), Logger: testLogger()})

	body := `{"programRoute":"hw:0,0","ltcRoute":"hw:1,0","programChannels":[1,2],"ltcChannel":3,` +
		`"clockDomain":"single-interface","clockDomainProvenance":"one physical interface, both routes on it"}`
	status, respBody := mustPutAudioNode(t, api, token, "render-01", body)
	if status != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400; body: %s", status, respBody)
	}
	if !containsAll(respBody, "audio-node-route-mismatch") {
		t.Fatalf("body does not name the route-mismatch code; body: %s", respBody)
	}
}

// TestPutAudioNodeRejectsLTCChannelOverlappingProgramChannels proves the
// overlap refusal is reachable through the real handler, not only the
// config package's own decode test.
func TestPutAudioNodeRejectsLTCChannelOverlappingProgramChannels(t *testing.T) {
	svc, st, _ := newTestIdentityServiceWithStore(t, fixedClock(testNow))
	admin := mustCreatePrincipal(t, svc, "admin-1", identity.RoleAdmin)
	token := mustIssueToken(t, svc, admin.ID)
	deps := showConfigTestDeps(svc, st)
	deps.Nodes.(*fakeNodeLister).setViews([]inventory.NodeView{
		nodeViewWithAudioCapabilities("render-01", []string{"hw:0,0"}, []string{"hw:0,0"}),
	})
	api := New(deps, Options{Clock: fixedClock(testNow), Logger: testLogger()})

	body := `{"programRoute":"hw:0,0","ltcRoute":"hw:0,0","programChannels":[1,2],"ltcChannel":2,` +
		`"clockDomain":"single-interface","clockDomainProvenance":"one physical interface, both routes on it"}`
	status, respBody := mustPutAudioNode(t, api, token, "render-01", body)
	if status != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400; body: %s", status, respBody)
	}
	if !containsAll(respBody, "audio-node-channel-overlap") {
		t.Fatalf("body does not name the channel-overlap code; body: %s", respBody)
	}
}

// TestPutAudioNodeRejectsLTCRouteWithoutDiscreteEvidence proves a route
// that is program-capable but never evidenced as LTC-capable (fewer than
// 3 channels achieved) is refused as an LTC route, even though it is a
// real, evidenced route — the "discrete route outside program pair"
// requirement (ADR-018) is enforced against the SPECIFIC capability, not
// against "the node has some audio evidence".
func TestPutAudioNodeRejectsLTCRouteWithoutDiscreteEvidence(t *testing.T) {
	svc, st, _ := newTestIdentityServiceWithStore(t, fixedClock(testNow))
	admin := mustCreatePrincipal(t, svc, "admin-1", identity.RoleAdmin)
	token := mustIssueToken(t, svc, admin.ID)
	deps := showConfigTestDeps(svc, st)
	deps.Nodes.(*fakeNodeLister).setViews([]inventory.NodeView{
		nodeViewWithAudioCapabilities("render-01", []string{"hw:0,0"}, nil),
	})
	api := New(deps, Options{Clock: fixedClock(testNow), Logger: testLogger()})

	status, body := mustPutAudioNode(t, api, token, "render-01", validAudioNodeBody)
	if status != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400; body: %s", status, body)
	}
}

// TestPutAudioNodeRejectsInvalidObjectID proves the object id is validated
// as a node id BEFORE the request body is even read, mirroring
// handlePutShow/handlePutShowSurface's own ValidateShowObjectID guard.
func TestPutAudioNodeRejectsInvalidObjectID(t *testing.T) {
	svc, st, _ := newTestIdentityServiceWithStore(t, fixedClock(testNow))
	admin := mustCreatePrincipal(t, svc, "admin-1", identity.RoleAdmin)
	token := mustIssueToken(t, svc, admin.ID)
	api := New(showConfigTestDeps(svc, st), Options{Clock: fixedClock(testNow), Logger: testLogger()})

	status, body := mustPutAudioNode(t, api, token, "not_valid", validAudioNodeBody)
	if status != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400; body: %s", status, body)
	}
}

// TestListAudioNodesReturnsConfiguredObjects proves the list route surfaces
// the programRoute as its Label, exercising the zero-to-one transition
// ADR-039 requires: no audio.node objects at all, then one after a write.
func TestListAudioNodesReturnsConfiguredObjects(t *testing.T) {
	svc, st, _ := newTestIdentityServiceWithStore(t, fixedClock(testNow))
	admin := mustCreatePrincipal(t, svc, "admin-1", identity.RoleAdmin)
	token := mustIssueToken(t, svc, admin.ID)
	deps := showConfigTestDeps(svc, st)
	deps.Nodes.(*fakeNodeLister).setViews([]inventory.NodeView{
		nodeViewWithAudioCapabilities("render-01", []string{"hw:0,0"}, []string{"hw:0,0"}),
	})
	api := New(deps, Options{Clock: fixedClock(testNow), Logger: testLogger()})

	_, listBody0 := doRequest(t, api.Handler, "GET", "/api/v1/config/audio.node", map[string]string{"Authorization": "Bearer " + token})
	if containsAll(string(listBody0), `"id":"render-01"`) {
		t.Fatalf("zero state already lists render-01; body: %s", listBody0)
	}

	if status, body := mustPutAudioNode(t, api, token, "render-01", validAudioNodeBody); status != http.StatusOK {
		t.Fatalf("PUT status = %d, want 200; body: %s", status, body)
	}

	_, listBody1 := doRequest(t, api.Handler, "GET", "/api/v1/config/audio.node", map[string]string{"Authorization": "Bearer " + token})
	if !containsAll(string(listBody1), `"id":"render-01"`) || !containsAll(string(listBody1), `"label":"hw:0,0"`) {
		t.Fatalf("list missing render-01/hw:0,0; body: %s", listBody1)
	}
}

// TestPutAudioNodeRevisionPreconditionWiring is a smoke test proving
// handlePutAudioNode threads the shared precondition check (showconfig.go's
// parseRevisionPrecondition/writeShowConfigRevision) through to its own
// call site. The full behavioural matrix lives once, on kind "show" in
// showobjects_test.go's own precondition tests.
func TestPutAudioNodeRevisionPreconditionWiring(t *testing.T) {
	svc, st, _ := newTestIdentityServiceWithStore(t, fixedClock(testNow))
	admin := mustCreatePrincipal(t, svc, "admin-1", identity.RoleAdmin)
	token := mustIssueToken(t, svc, admin.ID)
	deps := showConfigTestDeps(svc, st)
	deps.Nodes.(*fakeNodeLister).setViews([]inventory.NodeView{
		nodeViewWithAudioCapabilities("render-01", []string{"hw:0,0"}, []string{"hw:0,0"}),
	})
	api := New(deps, Options{Clock: fixedClock(testNow), Logger: testLogger()})

	putNode := func(headers map[string]string) (*http.Response, []byte) {
		h := map[string]string{"Authorization": "Bearer " + token}
		for k, v := range headers {
			h[k] = v
		}
		req := newJSONRequest(t, http.MethodPut, "/api/v1/config/audio.node/render-01", validAudioNodeBody, h)
		return doRawRequest(t, api.Handler, req)
	}

	if resp, body := putNode(nil); resp.StatusCode != http.StatusOK {
		t.Fatalf("unconditional create: status = %d, want 200; body: %s", resp.StatusCode, body)
	}
	if resp, body := putNode(map[string]string{"If-Match": `"1"`}); resp.StatusCode != http.StatusOK {
		t.Fatalf("matching If-Match: status = %d, want 200; body: %s", resp.StatusCode, body)
	}
	if resp, body := putNode(map[string]string{"If-Match": `"1"`}); resp.StatusCode != http.StatusConflict {
		t.Fatalf("stale If-Match: status = %d, want 409; body: %s", resp.StatusCode, body)
	}
	if resp, body := putNode(map[string]string{"If-None-Match": "*"}); resp.StatusCode != http.StatusConflict {
		t.Fatalf("If-None-Match against an already-created node: status = %d, want 409; body: %s", resp.StatusCode, body)
	}
}
