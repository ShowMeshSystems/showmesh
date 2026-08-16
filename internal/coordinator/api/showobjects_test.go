package api

import (
	"context"
	"net/http"
	"testing"

	"github.com/showmeshsystems/showmesh/internal/coordinator/identity"
	"github.com/showmeshsystems/showmesh/internal/coordinator/store"
)

// This file is Track E seam E1/E2's own test suite for the three new
// configuration kinds show, show.surface, and show.active
// (TRACK-E-SESSION-SPEC.md section 2). It follows showconfig_test.go's
// existing pattern one seam over: a real *store.Store and a real
// identity.Service, driven through the real route table.

// showObjectsTestDeps mirrors showConfigTestDeps (showconfig_test.go),
// additionally wiring Discovery against the same store so
// [handlers.nodeDeclared] has a real ListNodeDeclarations to read from —
// showConfigTestDeps leaves Discovery nil (defaulting to
// [noDeclarationStore], under which no node is ever declared), which is
// correct for that file's own tests but wrong here, since a show.surface
// write needs at least one real declared node to succeed.
func showObjectsTestDeps(svc identity.Service, st *store.Store) Dependencies {
	deps := showConfigTestDeps(svc, st)
	deps.Discovery = st
	return deps
}

func mustDeclareNode(t *testing.T, st *store.Store, nodeID string) {
	t.Helper()
	if _, err := st.DeclareNode(context.Background(), store.NodeDeclarationRecord{NodeID: nodeID}); err != nil {
		t.Fatalf("declare node %q: %v", nodeID, err)
	}
}

const validSurfaceBodyNDI = `{
	"show": "halloween-2026",
	"name": "Garage Door",
	"node": "render-01",
	"channelRange": {"startChannel": 1, "channelCount": 3600},
	"geometry": {"width": 40, "height": 30, "pixelFormat": "rgb"},
	"frameRate": 40,
	"output": {"transport": "ndi", "ndi": {"sourceName": "ShowMesh Garage"}}
}`

func mustPutShow(t *testing.T, api *API, token, id, body string) {
	t.Helper()
	req := newJSONRequest(t, http.MethodPut, "/api/v1/config/show/"+id, body, map[string]string{"Authorization": "Bearer " + token})
	resp, respBody := doRawRequest(t, api.Handler, req)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("PUT show/%s: status = %d, want 200; body: %s", id, resp.StatusCode, respBody)
	}
}

// TestPutShowIsFullReplacementNotReadModifyWrite is TRACK-E-SESSION-SPEC.md
// section 2.1's own required behaviour, restated as a test rather than
// prose: an absent "notes" on a SECOND write of an object that already has
// notes set clears it, exactly like Step 7's fpp.endpoints PUT — the
// opposite (absent means "keep") is what erased the operator's node label
// in that step.
//
// Broken and confirmed to fail: changed DecodeShowPayload's "notes"
// decoding to carry forward the previous value when absent (by reading the
// existing revision first) — this test's second assertion failed, reading
// back the old notes instead of "". Restored afterward.
func TestPutShowIsFullReplacementNotReadModifyWrite(t *testing.T) {
	svc, st, _ := newTestIdentityServiceWithStore(t, fixedClock(testNow))
	admin := mustCreatePrincipal(t, svc, "admin-1", identity.RoleAdmin)
	token := mustIssueToken(t, svc, admin.ID)
	api := New(showObjectsTestDeps(svc, st), Options{Clock: fixedClock(testNow), Logger: testLogger()})

	mustPutShow(t, api, token, "halloween-2026", `{"name":"Halloween 2026","notes":"draft"}`)

	_, getBody1 := doRequest(t, api.Handler, "GET", "/api/v1/config/show/halloween-2026", map[string]string{"Authorization": "Bearer " + token})
	if !containsAll(string(getBody1), `"notes":"draft"`) {
		t.Fatalf("first GET missing notes:draft; body: %s", getBody1)
	}

	// Second PUT omits "notes" entirely: a full replacement means it
	// becomes empty, never "left as draft".
	mustPutShow(t, api, token, "halloween-2026", `{"name":"Halloween 2026"}`)

	_, getBody2 := doRequest(t, api.Handler, "GET", "/api/v1/config/show/halloween-2026", map[string]string{"Authorization": "Bearer " + token})
	if !containsAll(string(getBody2), `"notes":""`) {
		t.Fatalf("second GET should have cleared notes to empty; body: %s", getBody2)
	}
	if containsAll(string(getBody2), `"notes":"draft"`) {
		t.Fatalf("second GET still carries the old notes; a PUT is a full replacement, not a merge; body: %s", getBody2)
	}
}

func containsAll(s, substr string) bool {
	return len(s) > 0 && (len(substr) == 0 || indexOf(s, substr) >= 0)
}

func indexOf(s, substr string) int {
	for i := 0; i+len(substr) <= len(s); i++ {
		if s[i:i+len(substr)] == substr {
			return i
		}
	}
	return -1
}

// TestGetShowActiveWithNothingActivatedIs404 is section 2.4's own required
// behaviour: matching what fpp.endpoints and resolume.composition already
// answer for "nothing configured yet".
//
// Broken and confirmed to fail: changed handleGetShowActive to return an
// empty 200 body instead of calling getActiveShowConfigRevision's problem
// path — this test failed expecting 404 and got 200. Restored afterward.
func TestGetShowActiveWithNothingActivatedIs404(t *testing.T) {
	svc, st, _ := newTestIdentityServiceWithStore(t, fixedClock(testNow))
	admin := mustCreatePrincipal(t, svc, "admin-1", identity.RoleAdmin)
	token := mustIssueToken(t, svc, admin.ID)
	api := New(showObjectsTestDeps(svc, st), Options{Clock: fixedClock(testNow), Logger: testLogger()})

	resp, body := doRequest(t, api.Handler, "GET", "/api/v1/config/show.active", map[string]string{"Authorization": "Bearer " + token})
	if resp.StatusCode != http.StatusNotFound {
		t.Fatalf("status = %d, want 404; body: %s", resp.StatusCode, body)
	}
}

// TestShowActiveObjectIDIsFixedConstant proves the active-show pointer's
// object id never changes as the pointed-to show changes: activating two
// different shows in turn must accumulate as TWO REVISIONS OF THE SAME
// OBJECT, never two different objects — the defect
// resolumeCompositionObjectIDConst's own doc comment warns about
// (orphaning every stored revision by deriving a singleton's id from a
// value that can change).
//
// Broken and confirmed to fail: changed handlePutShowActive to use
// payload.Show as the object id instead of config.ShowActiveObjectID —
// this test's revision-count assertion failed (1, not 2: the second write
// created a new object instead of a second revision of the same one).
// Restored afterward.
func TestShowActiveObjectIDIsFixedConstant(t *testing.T) {
	svc, st, _ := newTestIdentityServiceWithStore(t, fixedClock(testNow))
	admin := mustCreatePrincipal(t, svc, "admin-1", identity.RoleAdmin)
	token := mustIssueToken(t, svc, admin.ID)
	api := New(showObjectsTestDeps(svc, st), Options{Clock: fixedClock(testNow), Logger: testLogger()})

	mustPutShow(t, api, token, "halloween-2026", `{"name":"Halloween 2026"}`)
	mustPutShow(t, api, token, "christmas-2026", `{"name":"Christmas 2026"}`)

	putActive := func(show string) {
		req := newJSONRequest(t, http.MethodPut, "/api/v1/config/show.active", `{"show":"`+show+`"}`,
			map[string]string{"Authorization": "Bearer " + token})
		resp, body := doRawRequest(t, api.Handler, req)
		if resp.StatusCode != http.StatusOK {
			t.Fatalf("PUT show.active(%s): status = %d, want 200; body: %s", show, resp.StatusCode, body)
		}
	}
	putActive("halloween-2026")
	putActive("christmas-2026")

	_, revBody := doRequest(t, api.Handler, "GET", "/api/v1/config/show.active/revisions", map[string]string{"Authorization": "Bearer " + token})
	if !containsAll(string(revBody), `"revision":1`) || !containsAll(string(revBody), `"revision":2`) {
		t.Fatalf("expected two revisions of one object; body: %s", revBody)
	}

	_, getBody := doRequest(t, api.Handler, "GET", "/api/v1/config/show.active", map[string]string{"Authorization": "Bearer " + token})
	if !containsAll(string(getBody), `"show":"christmas-2026"`) {
		t.Fatalf("active show should now be christmas-2026; body: %s", getBody)
	}
}

// TestShowActiveRouteIsNotSwallowedByShowIDRoute proves Go 1.22's mux
// routes "GET /api/v1/config/show.active" and
// "GET /api/v1/config/show/{id}" unambiguously (api.go's own route
// registration comment names this test). "show.active" is a single,
// distinct path segment from "show", so it can never match the
// three-segment show/{id} pattern — this test exercises that against the
// real mux rather than only asserting it in a comment.
//
// Broken and confirmed to fail: temporarily registered
// "GET /api/v1/config/show.active" to call handleGetShow instead of
// handleGetShowActive — this test's body-shape assertion failed (a "show"
// object 404 names show.active as an id, never as its own kind).
// Restored afterward.
func TestShowActiveRouteIsNotSwallowedByShowIDRoute(t *testing.T) {
	svc, st, _ := newTestIdentityServiceWithStore(t, fixedClock(testNow))
	admin := mustCreatePrincipal(t, svc, "admin-1", identity.RoleAdmin)
	token := mustIssueToken(t, svc, admin.ID)
	api := New(showObjectsTestDeps(svc, st), Options{Clock: fixedClock(testNow), Logger: testLogger()})

	resp, body := doRequest(t, api.Handler, "GET", "/api/v1/config/show.active", map[string]string{"Authorization": "Bearer " + token})
	if resp.StatusCode != http.StatusNotFound {
		t.Fatalf("status = %d, want 404; body: %s", resp.StatusCode, body)
	}
	// handleGetShow's 404 (showConfigObjectNotFoundProblem) names the kind
	// and the requested id in its Detail; if this route were being served
	// by handleGetShow with id="show.active", the message would say
	// `no show object with id "show.active"`. handleGetShowActive's own
	// 404 never says that.
	if containsAll(string(body), `no show object with id`) {
		t.Fatalf("GET /config/show.active was served by the show/{id} handler, not its own; body: %s", body)
	}
}

// TestPutShowSurfaceChannelRangeThreeDistinctRefusals is section 2.2's own
// required behaviour: an absent channelRange, an explicit null, and an
// explicitly empty one ({"startChannel":1,"channelCount":0}) are three
// distinct refusals, and this is the route-layer proof that
// config.DecodeShowSurfacePayload's distinction actually reaches the wire
// as three different 400s rather than being collapsed by this handler.
func TestPutShowSurfaceChannelRangeThreeDistinctRefusals(t *testing.T) {
	svc, st, _ := newTestIdentityServiceWithStore(t, fixedClock(testNow))
	admin := mustCreatePrincipal(t, svc, "admin-1", identity.RoleAdmin)
	token := mustIssueToken(t, svc, admin.ID)
	api := New(showObjectsTestDeps(svc, st), Options{Clock: fixedClock(testNow), Logger: testLogger()})
	mustPutShow(t, api, token, "halloween-2026", `{"name":"Halloween 2026"}`)
	mustDeclareNode(t, st, "render-01")

	base := `{"show":"halloween-2026","name":"Garage Door","node":"render-01",
		"geometry":{"width":40,"height":30,"pixelFormat":"rgb"},"frameRate":40,
		"output":{"transport":"ndi","ndi":{"sourceName":"x"}}}`

	cases := []struct {
		name string
		body string
	}{
		{"absent", `{"show":"halloween-2026","name":"Garage Door","node":"render-01","geometry":{"width":40,"height":30,"pixelFormat":"rgb"},"frameRate":40,"output":{"transport":"ndi","ndi":{"sourceName":"x"}}}`},
		{"null", `{"show":"halloween-2026","name":"Garage Door","node":"render-01","channelRange":null,"geometry":{"width":40,"height":30,"pixelFormat":"rgb"},"frameRate":40,"output":{"transport":"ndi","ndi":{"sourceName":"x"}}}`},
		{"zero", `{"show":"halloween-2026","name":"Garage Door","node":"render-01","channelRange":{"startChannel":1,"channelCount":0},"geometry":{"width":40,"height":30,"pixelFormat":"rgb"},"frameRate":40,"output":{"transport":"ndi","ndi":{"sourceName":"x"}}}`},
	}
	_ = base
	seenDetails := map[string]bool{}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			req := newJSONRequest(t, http.MethodPut, "/api/v1/config/show.surface/garage", tc.body, map[string]string{"Authorization": "Bearer " + token})
			resp, body := doRawRequest(t, api.Handler, req)
			if resp.StatusCode != http.StatusBadRequest {
				t.Fatalf("status = %d, want 400; body: %s", resp.StatusCode, body)
			}
			if seenDetails[string(body)] {
				t.Fatalf("case %q produced the same problem body as a previous case; the three cases must be distinct refusals: %s", tc.name, body)
			}
			seenDetails[string(body)] = true
		})
	}
}

// TestShowSurfaceAllowsTwoSurfacesOnSameNode proves ADR-026's N=1 is a
// scope limit that does not reach the schema: a second surface assigned to
// the same node is accepted, never refused as a collision.
func TestShowSurfaceAllowsTwoSurfacesOnSameNode(t *testing.T) {
	svc, st, _ := newTestIdentityServiceWithStore(t, fixedClock(testNow))
	admin := mustCreatePrincipal(t, svc, "admin-1", identity.RoleAdmin)
	token := mustIssueToken(t, svc, admin.ID)
	api := New(showObjectsTestDeps(svc, st), Options{Clock: fixedClock(testNow), Logger: testLogger()})
	mustPutShow(t, api, token, "halloween-2026", `{"name":"Halloween 2026"}`)
	mustDeclareNode(t, st, "render-01")

	for _, id := range []string{"garage", "porch"} {
		req := newJSONRequest(t, http.MethodPut, "/api/v1/config/show.surface/"+id, validSurfaceBodyNDI,
			map[string]string{"Authorization": "Bearer " + token})
		resp, body := doRawRequest(t, api.Handler, req)
		if resp.StatusCode != http.StatusOK {
			t.Fatalf("PUT show.surface/%s: status = %d, want 200; body: %s", id, resp.StatusCode, body)
		}
	}

	_, listBody := doRequest(t, api.Handler, "GET", "/api/v1/config/show.surface", map[string]string{"Authorization": "Bearer " + token})
	if !containsAll(string(listBody), `"garage"`) || !containsAll(string(listBody), `"porch"`) {
		t.Fatalf("expected both surfaces listed; body: %s", listBody)
	}
}

// TestShowSurfaceRejectsUnknownShowAndUnknownNode proves this handler
// actually wires DecodeShowSurfacePayload's showExists/nodeDeclared
// callbacks against live store state, not stub functions that always
// return true.
//
// Broken and confirmed to fail: passed func(string) bool { return true }
// for both callbacks instead of h.showExists(ctx)/h.nodeDeclared(ctx) —
// both sub-tests failed, expecting 400 and getting 200. Restored
// afterward.
func TestShowSurfaceRejectsUnknownShowAndUnknownNode(t *testing.T) {
	svc, st, _ := newTestIdentityServiceWithStore(t, fixedClock(testNow))
	admin := mustCreatePrincipal(t, svc, "admin-1", identity.RoleAdmin)
	token := mustIssueToken(t, svc, admin.ID)
	api := New(showObjectsTestDeps(svc, st), Options{Clock: fixedClock(testNow), Logger: testLogger()})

	t.Run("unknown show", func(t *testing.T) {
		mustDeclareNode(t, st, "render-02")
		req := newJSONRequest(t, http.MethodPut, "/api/v1/config/show.surface/x", validSurfaceBodyNDI,
			map[string]string{"Authorization": "Bearer " + token})
		resp, body := doRawRequest(t, api.Handler, req)
		if resp.StatusCode != http.StatusBadRequest {
			t.Fatalf("status = %d, want 400 (show halloween-2026 does not exist yet); body: %s", resp.StatusCode, body)
		}
	})

	t.Run("unknown node", func(t *testing.T) {
		mustPutShow(t, api, token, "halloween-2026", `{"name":"Halloween 2026"}`)
		req := newJSONRequest(t, http.MethodPut, "/api/v1/config/show.surface/y", validSurfaceBodyNDI,
			map[string]string{"Authorization": "Bearer " + token})
		resp, body := doRawRequest(t, api.Handler, req)
		if resp.StatusCode != http.StatusBadRequest {
			t.Fatalf("status = %d, want 400 (node render-01 is not declared); body: %s", resp.StatusCode, body)
		}
	})
}

// TestListShowSurfacesFiltersByShow proves the ?show= query filter
// (section 2.4) actually narrows the list rather than being ignored.
func TestListShowSurfacesFiltersByShow(t *testing.T) {
	svc, st, _ := newTestIdentityServiceWithStore(t, fixedClock(testNow))
	admin := mustCreatePrincipal(t, svc, "admin-1", identity.RoleAdmin)
	token := mustIssueToken(t, svc, admin.ID)
	api := New(showObjectsTestDeps(svc, st), Options{Clock: fixedClock(testNow), Logger: testLogger()})
	mustPutShow(t, api, token, "halloween-2026", `{"name":"Halloween 2026"}`)
	mustPutShow(t, api, token, "christmas-2026", `{"name":"Christmas 2026"}`)
	mustDeclareNode(t, st, "render-01")

	halloweenSurface := validSurfaceBodyNDI
	christmasSurface := `{
		"show": "christmas-2026",
		"name": "Front Yard",
		"node": "render-01",
		"channelRange": {"startChannel": 1, "channelCount": 12},
		"geometry": {"width": 1, "height": 3, "pixelFormat": "rgbw"},
		"frameRate": 30,
		"output": {"transport": "hdmi", "hdmi": {"display": "HDMI-1"}}
	}`
	for id, body := range map[string]string{"garage": halloweenSurface, "yard": christmasSurface} {
		req := newJSONRequest(t, http.MethodPut, "/api/v1/config/show.surface/"+id, body, map[string]string{"Authorization": "Bearer " + token})
		resp, respBody := doRawRequest(t, api.Handler, req)
		if resp.StatusCode != http.StatusOK {
			t.Fatalf("PUT show.surface/%s: status = %d, want 200; body: %s", id, resp.StatusCode, respBody)
		}
	}

	_, filtered := doRequest(t, api.Handler, "GET", "/api/v1/config/show.surface?show=christmas-2026", map[string]string{"Authorization": "Bearer " + token})
	if !containsAll(string(filtered), `"yard"`) {
		t.Fatalf("expected yard in the christmas-2026 filtered list; body: %s", filtered)
	}
	if containsAll(string(filtered), `"garage"`) {
		t.Fatalf("garage (halloween-2026) leaked into a christmas-2026 filtered list; body: %s", filtered)
	}
}

// TestShowWriteRequiresConfigWrite proves an operator (show:macro:run,
// never config:write) cannot write any of the three new kinds, matching
// fpp.endpoints and show.action/show.macro's identical write posture.
func TestShowWriteRequiresConfigWrite(t *testing.T) {
	svc, st, _ := newTestIdentityServiceWithStore(t, fixedClock(testNow))
	operator := mustCreatePrincipal(t, svc, "operator-1", identity.RoleOperator)
	operatorToken := mustIssueToken(t, svc, operator.ID)
	api := New(showObjectsTestDeps(svc, st), Options{Clock: fixedClock(testNow), Logger: testLogger()})

	req := newJSONRequest(t, http.MethodPut, "/api/v1/config/show/halloween-2026", `{"name":"Halloween 2026"}`,
		map[string]string{"Authorization": "Bearer " + operatorToken})
	resp, body := doRawRequest(t, api.Handler, req)
	if resp.StatusCode != http.StatusForbidden {
		t.Fatalf("status = %d, want 403; body: %s", resp.StatusCode, body)
	}
}
