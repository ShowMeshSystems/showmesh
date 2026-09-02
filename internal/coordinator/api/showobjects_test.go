package api

import (
	"bytes"
	"context"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/showmeshsystems/showmesh/internal/coordinator/config"
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

// TestListShowSurfacesFiltersByNode proves the ?node= query filter added
// for the PR #14 review finding actually narrows the list server-side
// rather than being ignored — the same shape as
// TestListShowSurfacesFiltersByShow, but on the node axis, which is what
// RenderSurfacePanel.tsx's useConfiguredSurfaceIds now relies on instead
// of fetching every candidate's full payload.
//
// Broken and confirmed to fail: reverted the nodeFilter check in
// listShowSurfaceSummaries (showobjects.go) back to a no-op — both
// assertions below failed, since the unfiltered list returned both
// "garage" (render-01) and "yard" (render-02).
func TestListShowSurfacesFiltersByNode(t *testing.T) {
	svc, st, _ := newTestIdentityServiceWithStore(t, fixedClock(testNow))
	admin := mustCreatePrincipal(t, svc, "admin-1", identity.RoleAdmin)
	token := mustIssueToken(t, svc, admin.ID)
	api := New(showObjectsTestDeps(svc, st), Options{Clock: fixedClock(testNow), Logger: testLogger()})
	mustPutShow(t, api, token, "halloween-2026", `{"name":"Halloween 2026"}`)
	mustDeclareNode(t, st, "render-01")
	mustDeclareNode(t, st, "render-02")

	garageOnRender01 := validSurfaceBodyNDI
	yardOnRender02 := `{
		"show": "halloween-2026",
		"name": "Front Yard",
		"node": "render-02",
		"channelRange": {"startChannel": 1, "channelCount": 12},
		"geometry": {"width": 1, "height": 3, "pixelFormat": "rgbw"},
		"frameRate": 30,
		"output": {"transport": "hdmi", "hdmi": {"display": "HDMI-1"}}
	}`
	for id, body := range map[string]string{"garage": garageOnRender01, "yard": yardOnRender02} {
		req := newJSONRequest(t, http.MethodPut, "/api/v1/config/show.surface/"+id, body, map[string]string{"Authorization": "Bearer " + token})
		resp, respBody := doRawRequest(t, api.Handler, req)
		if resp.StatusCode != http.StatusOK {
			t.Fatalf("PUT show.surface/%s: status = %d, want 200; body: %s", id, resp.StatusCode, respBody)
		}
	}

	_, filtered := doRequest(t, api.Handler, "GET", "/api/v1/config/show.surface?node=render-02", map[string]string{"Authorization": "Bearer " + token})
	if !containsAll(string(filtered), `"yard"`) {
		t.Fatalf("expected yard in the render-02 filtered list; body: %s", filtered)
	}
	if containsAll(string(filtered), `"garage"`) {
		t.Fatalf("garage (render-01) leaked into a render-02 filtered list; body: %s", filtered)
	}
}

// TestListShowSurfacesFiltersByShowAndNodeTogether proves both filters
// combine as AND, not OR: a surface must match every filter given.
func TestListShowSurfacesFiltersByShowAndNodeTogether(t *testing.T) {
	svc, st, _ := newTestIdentityServiceWithStore(t, fixedClock(testNow))
	admin := mustCreatePrincipal(t, svc, "admin-1", identity.RoleAdmin)
	token := mustIssueToken(t, svc, admin.ID)
	api := New(showObjectsTestDeps(svc, st), Options{Clock: fixedClock(testNow), Logger: testLogger()})
	mustPutShow(t, api, token, "halloween-2026", `{"name":"Halloween 2026"}`)
	mustPutShow(t, api, token, "christmas-2026", `{"name":"Christmas 2026"}`)
	mustDeclareNode(t, st, "render-01")
	mustDeclareNode(t, st, "render-02")

	// Same node, different shows — a request for
	// show=halloween-2026&node=render-01 must return only "garage".
	garageHalloweenRender01 := validSurfaceBodyNDI
	yardChristmasRender01 := `{
		"show": "christmas-2026",
		"name": "Front Yard",
		"node": "render-01",
		"channelRange": {"startChannel": 1, "channelCount": 12},
		"geometry": {"width": 1, "height": 3, "pixelFormat": "rgbw"},
		"frameRate": 30,
		"output": {"transport": "hdmi", "hdmi": {"display": "HDMI-1"}}
	}`
	for id, body := range map[string]string{"garage": garageHalloweenRender01, "yard": yardChristmasRender01} {
		req := newJSONRequest(t, http.MethodPut, "/api/v1/config/show.surface/"+id, body, map[string]string{"Authorization": "Bearer " + token})
		resp, respBody := doRawRequest(t, api.Handler, req)
		if resp.StatusCode != http.StatusOK {
			t.Fatalf("PUT show.surface/%s: status = %d, want 200; body: %s", id, resp.StatusCode, respBody)
		}
	}

	_, filtered := doRequest(t, api.Handler, "GET", "/api/v1/config/show.surface?show=halloween-2026&node=render-01", map[string]string{"Authorization": "Bearer " + token})
	if !containsAll(string(filtered), `"garage"`) {
		t.Fatalf("expected garage in the halloween-2026/render-01 filtered list; body: %s", filtered)
	}
	if containsAll(string(filtered), `"yard"`) {
		t.Fatalf("yard (christmas-2026) leaked into a halloween-2026 filtered list; body: %s", filtered)
	}
}

// TestListShowActionsRejectsNodeFilter and its show.macro/show siblings
// prove the review finding's other half: a "node" filter is meaningful
// only for show.surface, and the other config-object list routes must
// say so with a 400 rather than silently ignoring the parameter and
// returning an unfiltered list, which would let a caller believe a
// response was narrowed when it was not.
func TestListShowActionsRejectsNodeFilter(t *testing.T) {
	svc, st, _ := newTestIdentityServiceWithStore(t, fixedClock(testNow))
	admin := mustCreatePrincipal(t, svc, "admin-1", identity.RoleAdmin)
	token := mustIssueToken(t, svc, admin.ID)
	api := New(showObjectsTestDeps(svc, st), Options{Clock: fixedClock(testNow), Logger: testLogger()})

	resp, body := doRequest(t, api.Handler, "GET", "/api/v1/config/show.action?node=render-01", map[string]string{"Authorization": "Bearer " + token})
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("GET show.action?node=: status = %d, want 400; body: %s", resp.StatusCode, body)
	}
}

func TestListShowMacrosRejectsNodeFilter(t *testing.T) {
	svc, st, _ := newTestIdentityServiceWithStore(t, fixedClock(testNow))
	admin := mustCreatePrincipal(t, svc, "admin-1", identity.RoleAdmin)
	token := mustIssueToken(t, svc, admin.ID)
	api := New(showObjectsTestDeps(svc, st), Options{Clock: fixedClock(testNow), Logger: testLogger()})

	resp, body := doRequest(t, api.Handler, "GET", "/api/v1/config/show.macro?node=render-01", map[string]string{"Authorization": "Bearer " + token})
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("GET show.macro?node=: status = %d, want 400; body: %s", resp.StatusCode, body)
	}
}

func TestListShowsRejectsNodeFilter(t *testing.T) {
	svc, st, _ := newTestIdentityServiceWithStore(t, fixedClock(testNow))
	admin := mustCreatePrincipal(t, svc, "admin-1", identity.RoleAdmin)
	token := mustIssueToken(t, svc, admin.ID)
	api := New(showObjectsTestDeps(svc, st), Options{Clock: fixedClock(testNow), Logger: testLogger()})

	resp, body := doRequest(t, api.Handler, "GET", "/api/v1/config/show?node=render-01", map[string]string{"Authorization": "Bearer " + token})
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("GET show?node=: status = %d, want 400; body: %s", resp.StatusCode, body)
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

// erroringConfigStore is a [ConfigStore] whose GetConfigRevision fails
// with a genuine (non-ErrConfigObjectNotFound) error for one specific
// (kind, id) pair, and otherwise defers to an embedded real store, for
// TestPreviousShowSurfaceNodeLogsARealReadFailureDistinctFromNotFound,
// which needs a failure previousShowSurfaceNode (showobjects.go) must
// treat differently from "nothing stored yet".
type erroringConfigStore struct {
	ConfigStore
	failKind, failID string
}

func (e *erroringConfigStore) GetConfigRevision(ctx context.Context, kind, id string, revision int64) (store.ConfigRevisionRecord, error) {
	if kind == e.failKind && id == e.failID {
		return store.ConfigRevisionRecord{}, errors.New("simulated transient store failure")
	}
	return e.ConfigStore.GetConfigRevision(ctx, kind, id, revision)
}

// TestPreviousShowSurfaceNodeLogsARealReadFailureDistinctFromNotFound
// proves the review finding this closes: a genuine store read failure
// while resolving a show.surface's previous node degrades safely
// (returns ok=false, never panics or propagates) exactly like the
// expected "never written yet" case, BUT is logged as a warning naming
// the surface id, distinct from the silent, expected
// store.ErrConfigObjectNotFound case, so a transient failure here is
// visible rather than indistinguishable from "this surface has never
// moved."
func TestPreviousShowSurfaceNodeLogsARealReadFailureDistinctFromNotFound(t *testing.T) {
	svc, st, _ := newTestIdentityServiceWithStore(t, fixedClock(testNow))
	admin := mustCreatePrincipal(t, svc, "admin-1", identity.RoleAdmin)
	token := mustIssueToken(t, svc, admin.ID)
	deps := showObjectsTestDeps(svc, st)
	api := New(deps, Options{Clock: fixedClock(testNow), Logger: testLogger()})

	mustDeclareNode(t, st, "render-01")
	mustPutShow(t, api, token, "halloween-2026", `{"name":"Halloween 2026"}`)

	// Create the surface for real first, so its config object genuinely
	// exists with an active revision; the failure this test injects is
	// on the SECOND read (the one previousShowSurfaceNode performs ahead
	// of a later PUT), not on its creation.
	req := newJSONRequest(t, http.MethodPut, "/api/v1/config/show.surface/garage-door", validSurfaceBodyNDI, map[string]string{"Authorization": "Bearer " + token})
	if resp, body := doRawRequest(t, api.Handler, req); resp.StatusCode != http.StatusOK {
		t.Fatalf("PUT show.surface status = %d, want 200; body: %s", resp.StatusCode, body)
	}

	var logBuf bytes.Buffer
	capturing := slog.New(slog.NewTextHandler(&logBuf, nil))
	h := &handlers{
		deps:   Dependencies{Config: &erroringConfigStore{ConfigStore: deps.Config, failKind: config.ShowSurfaceConfigKind, failID: "garage-door"}},
		clock:  fixedClock(testNow),
		logger: capturing,
	}

	node, ok := h.previousShowSurfaceNode(context.Background(), "garage-door")
	if ok {
		t.Errorf("ok = true, want false on a real read failure")
	}
	if node != "" {
		t.Errorf("node = %q, want empty", node)
	}
	if !strings.Contains(logBuf.String(), "garage-door") {
		t.Errorf("log output missing the surface id; got: %s", logBuf.String())
	}
	if !strings.Contains(logBuf.String(), "simulated transient store failure") {
		t.Errorf("log output missing the underlying error; got: %s", logBuf.String())
	}
}

// --- config write precondition (If-Match / If-None-Match) ---
//
// This block is this task's own test suite for the opt-in revision
// precondition writeShowConfigRevision now enforces (showconfig.go). It
// exercises kind "show" as the representative full behavioural suite
// (every other kind sharing writeShowConfigRevision gets a smaller smoke
// test in its own file, since the check itself is the same shared code
// reached from ten call sites - what differs per kind is only whether
// that call site was wired correctly). Manager-D's build authorization
// ruled the guarantee opt-in, not mandatory: a PUT with neither header
// stays accepted, unprotected, exactly as before this task.

func putShowWithHeaders(t *testing.T, api *API, token, id, body string, extraHeaders map[string]string) (*http.Response, []byte) {
	t.Helper()
	headers := map[string]string{"Authorization": "Bearer " + token}
	for k, v := range extraHeaders {
		headers[k] = v
	}
	req := newJSONRequest(t, http.MethodPut, "/api/v1/config/show/"+id, body, headers)
	return doRawRequest(t, api.Handler, req)
}

// TestPutShowRevisionPreconditionOptInDefault pins the ruled default: a
// PUT that sends neither If-Match nor If-None-Match is accepted
// unconditionally, exactly as before this task added the precondition -
// a future move toward a mandatory guarantee has to touch this named
// test rather than pass silently.
func TestPutShowRevisionPreconditionOptInDefault(t *testing.T) {
	svc, st, _ := newTestIdentityServiceWithStore(t, fixedClock(testNow))
	admin := mustCreatePrincipal(t, svc, "admin-1", identity.RoleAdmin)
	token := mustIssueToken(t, svc, admin.ID)
	api := New(showObjectsTestDeps(svc, st), Options{Clock: fixedClock(testNow), Logger: testLogger()})

	mustPutShow(t, api, token, "unprotected-show", `{"name":"Unprotected","notes":"v1"}`)
	// A second, unconditional write, never having read revision 1 back,
	// still succeeds: this is exactly the D-014 hazard this task narrows
	// rather than closes, reproduced deliberately so its absence-of-header
	// behaviour is pinned rather than assumed.
	resp, body := putShowWithHeaders(t, api, token, "unprotected-show", `{"name":"Unprotected","notes":"v2, no precondition sent"}`, nil)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200 (no precondition means unconditional accept); body: %s", resp.StatusCode, body)
	}
}

// TestPutShowRevisionPreconditionMatchingIfMatchSucceeds proves the
// protected-update path: a client that read revision 1 back and sends it
// as If-Match gets its write accepted.
func TestPutShowRevisionPreconditionMatchingIfMatchSucceeds(t *testing.T) {
	svc, st, _ := newTestIdentityServiceWithStore(t, fixedClock(testNow))
	admin := mustCreatePrincipal(t, svc, "admin-1", identity.RoleAdmin)
	token := mustIssueToken(t, svc, admin.ID)
	api := New(showObjectsTestDeps(svc, st), Options{Clock: fixedClock(testNow), Logger: testLogger()})

	mustPutShow(t, api, token, "matching-show", `{"name":"Matching","notes":"v1"}`)
	resp, body := putShowWithHeaders(t, api, token, "matching-show", `{"name":"Matching","notes":"v2"}`, map[string]string{"If-Match": `"1"`})
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200; body: %s", resp.StatusCode, body)
	}
	if decodeMap(t, body)["revision"] != float64(2) {
		t.Errorf("revision = %v, want 2", decodeMap(t, body)["revision"])
	}
}

// TestPutShowRevisionPreconditionStaleIfMatchIsRefusedAndPreservesTheOtherWrite
// is the actual claim this feature makes, proved on stored content rather
// than only a status code: a writer holding a stale revision is refused
// 409, and the write it would have clobbered is still there afterward.
func TestPutShowRevisionPreconditionStaleIfMatchIsRefusedAndPreservesTheOtherWrite(t *testing.T) {
	svc, st, _ := newTestIdentityServiceWithStore(t, fixedClock(testNow))
	admin := mustCreatePrincipal(t, svc, "admin-1", identity.RoleAdmin)
	token := mustIssueToken(t, svc, admin.ID)
	api := New(showObjectsTestDeps(svc, st), Options{Clock: fixedClock(testNow), Logger: testLogger()})

	mustPutShow(t, api, token, "stale-show", `{"name":"Stale","notes":"v1"}`)
	// A second writer, who never sends a precondition, lands revision 2 -
	// the exact unprotected write the first writer's stale read cannot see.
	mustPutShow(t, api, token, "stale-show", `{"name":"Stale","notes":"v2-from-second-writer"}`)

	resp, body := putShowWithHeaders(t, api, token, "stale-show", `{"name":"Stale","notes":"v3-lost-update-attempt"}`, map[string]string{"If-Match": `"1"`})
	if resp.StatusCode != http.StatusConflict {
		t.Fatalf("status = %d, want 409; body: %s", resp.StatusCode, body)
	}
	problem := decodeMap(t, body)
	if problem["type"] != ProblemTypeConfigRevisionPreconditionFailed {
		t.Errorf("problem.type = %v, want %v", problem["type"], ProblemTypeConfigRevisionPreconditionFailed)
	}

	_, getBody := doRequest(t, api.Handler, "GET", "/api/v1/config/show/stale-show", map[string]string{"Authorization": "Bearer " + token})
	if !containsAll(string(getBody), `"notes":"v2-from-second-writer"`) {
		t.Fatalf("the second writer's payload should have survived the refused stale write; body: %s", getBody)
	}
	if containsAll(string(getBody), "lost-update-attempt") {
		t.Fatalf("the refused write's payload must never have been persisted; body: %s", getBody)
	}
}

// TestPutShowRevisionPreconditionIfMatchAgainstNonexistentIdIsRefused
// proves If-Match against an id with no active revision (current
// revision implicitly 0) is refused, never silently treated as a create.
func TestPutShowRevisionPreconditionIfMatchAgainstNonexistentIdIsRefused(t *testing.T) {
	svc, st, _ := newTestIdentityServiceWithStore(t, fixedClock(testNow))
	admin := mustCreatePrincipal(t, svc, "admin-1", identity.RoleAdmin)
	token := mustIssueToken(t, svc, admin.ID)
	api := New(showObjectsTestDeps(svc, st), Options{Clock: fixedClock(testNow), Logger: testLogger()})

	resp, body := putShowWithHeaders(t, api, token, "never-created", `{"name":"Never Created"}`, map[string]string{"If-Match": `"1"`})
	if resp.StatusCode != http.StatusConflict {
		t.Fatalf("status = %d, want 409; body: %s", resp.StatusCode, body)
	}
}

// TestPutShowRevisionPreconditionCreateGuard proves If-None-Match: "*"
// succeeds against a brand-new id (protected create) and is refused 409
// against an id that already has an active revision - the create-guard
// half of the carrier this task's design proposal recommended getting
// "for free" from the standard header rather than an invented sentinel.
func TestPutShowRevisionPreconditionCreateGuard(t *testing.T) {
	svc, st, _ := newTestIdentityServiceWithStore(t, fixedClock(testNow))
	admin := mustCreatePrincipal(t, svc, "admin-1", identity.RoleAdmin)
	token := mustIssueToken(t, svc, admin.ID)
	api := New(showObjectsTestDeps(svc, st), Options{Clock: fixedClock(testNow), Logger: testLogger()})

	t.Run("brand new id succeeds", func(t *testing.T) {
		resp, body := putShowWithHeaders(t, api, token, "brand-new", `{"name":"Brand New"}`, map[string]string{"If-None-Match": "*"})
		if resp.StatusCode != http.StatusOK {
			t.Fatalf("status = %d, want 200; body: %s", resp.StatusCode, body)
		}
	})

	t.Run("existing id is refused 409", func(t *testing.T) {
		mustPutShow(t, api, token, "already-exists", `{"name":"Already Exists"}`)
		resp, body := putShowWithHeaders(t, api, token, "already-exists", `{"name":"Already Exists","notes":"racing create"}`, map[string]string{"If-None-Match": "*"})
		if resp.StatusCode != http.StatusConflict {
			t.Fatalf("status = %d, want 409; body: %s", resp.StatusCode, body)
		}
	})
}

// TestPutShowRevisionPreconditionMalformedRequestsAreRejected covers the
// 400 paths: both headers on one request, a zero revision (the
// orchestrator's own ruling - an undocumented second spelling of the
// create guard is refused rather than silently accepted), a non-quoted
// value, and an If-None-Match value other than the literal "*".
func TestPutShowRevisionPreconditionMalformedRequestsAreRejected(t *testing.T) {
	svc, st, _ := newTestIdentityServiceWithStore(t, fixedClock(testNow))
	admin := mustCreatePrincipal(t, svc, "admin-1", identity.RoleAdmin)
	token := mustIssueToken(t, svc, admin.ID)
	api := New(showObjectsTestDeps(svc, st), Options{Clock: fixedClock(testNow), Logger: testLogger()})

	cases := []struct {
		name    string
		headers map[string]string
	}{
		{"both headers on one request", map[string]string{"If-Match": `"1"`, "If-None-Match": "*"}},
		{"If-Match zero is rejected, not treated as a create guard", map[string]string{"If-Match": `"0"`}},
		{"If-Match unquoted", map[string]string{"If-Match": "7"}},
		{"If-Match negative", map[string]string{"If-Match": `"-1"`}},
		{"If-Match non-numeric", map[string]string{"If-Match": `"abc"`}},
		{"If-None-Match not the literal *", map[string]string{"If-None-Match": `"1"`}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			resp, body := putShowWithHeaders(t, api, token, "malformed-target", `{"name":"Malformed"}`, tc.headers)
			if resp.StatusCode != http.StatusBadRequest {
				t.Fatalf("status = %d, want 400; body: %s", resp.StatusCode, body)
			}
		})
	}
}

// TestPutShowRevisionPreconditionSerializesUnderConcurrency is this
// task's test of the transaction boundary rather than only the handler.
// A sequential handler-then-handler test only proves the SECOND HTTP
// request observes the first one's effect; it says nothing about actual
// concurrency, which is the property this whole change exists for.
//
// This is deliberately NOT billed as a race test: the store package this
// coordinator uses already forbids that race by construction - store.go's
// DSN comment confirms every [store.Store.InTx]/AuditedWrite transaction
// issues BEGIN IMMEDIATE (the write lock is taken at transaction START,
// not first write), and tx.go's own InTx doc comment confirms the
// connection pool is capped at exactly one connection, so only one
// AuditedWrite closure ever runs at a time regardless of how many
// goroutines call this handler concurrently. What this test actually
// proves is that the precondition check correctly observes whichever of
// two goroutine-concurrent, serialized writers commits first: exactly one
// of the two receives 200 and the other 409, never both, never neither,
// and the store ends with exactly one new revision (no gap, no double
// create) holding the winning writer's payload, never a mix of the two.
func TestPutShowRevisionPreconditionSerializesUnderConcurrency(t *testing.T) {
	svc, st, _ := newTestIdentityServiceWithStore(t, fixedClock(testNow))
	admin := mustCreatePrincipal(t, svc, "admin-1", identity.RoleAdmin)
	token := mustIssueToken(t, svc, admin.ID)
	api := New(showObjectsTestDeps(svc, st), Options{Clock: fixedClock(testNow), Logger: testLogger()})

	mustPutShow(t, api, token, "race-show", `{"name":"Race","notes":"v1"}`)

	writerNotes := [2]string{"writer-a-wins-or-loses", "writer-b-wins-or-loses"}
	statuses := [2]int{}
	bodies := [2][]byte{}

	var wg sync.WaitGroup
	for i := 0; i < 2; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			req := httptest.NewRequest(http.MethodPut, "/api/v1/config/show/race-show",
				strings.NewReader(`{"name":"Race","notes":"`+writerNotes[i]+`"}`))
			req.Header.Set("Content-Type", "application/json")
			req.Header.Set("Authorization", "Bearer "+token)
			req.Header.Set("If-Match", `"1"`)
			rec := httptest.NewRecorder()
			api.Handler.ServeHTTP(rec, req)
			resp := rec.Result()
			body, _ := io.ReadAll(resp.Body)
			statuses[i] = resp.StatusCode
			bodies[i] = body
		}(i)
	}
	wg.Wait()

	okCount, conflictCount, winner := 0, 0, -1
	for i, s := range statuses {
		switch s {
		case http.StatusOK:
			okCount++
			winner = i
		case http.StatusConflict:
			conflictCount++
		default:
			t.Fatalf("unexpected status %d among concurrent writers (want only 200/409); bodies: %s / %s", s, bodies[0], bodies[1])
		}
	}
	if okCount != 1 || conflictCount != 1 {
		t.Fatalf("want exactly one 200 and one 409 among two concurrent writers holding the same stale-or-current If-Match, got statuses %v; bodies: %s / %s",
			statuses, bodies[0], bodies[1])
	}

	revResp, revBody := doRequest(t, api.Handler, "GET", "/api/v1/config/show/race-show/revisions", map[string]string{"Authorization": "Bearer " + token})
	if revResp.StatusCode != http.StatusOK {
		t.Fatalf("REVISIONS: status = %d; body: %s", revResp.StatusCode, revBody)
	}
	revs := decodeMap(t, revBody)["revisions"].([]any)
	if len(revs) != 2 {
		t.Fatalf("want exactly 2 revisions (the setup write plus exactly ONE winning concurrent write, no gap and no double-create), got %d; body: %s", len(revs), revBody)
	}

	_, getBody := doRequest(t, api.Handler, "GET", "/api/v1/config/show/race-show", map[string]string{"Authorization": "Bearer " + token})
	if !containsAll(string(getBody), `"notes":"`+writerNotes[winner]+`"`) {
		t.Fatalf("stored payload should be the ONE winning writer's (%s), never a mix; body: %s", writerNotes[winner], getBody)
	}
}

// TestPutShowSurfaceRevisionPreconditionWiring is a smoke test proving
// handlePutShowSurface actually threads the shared precondition check
// (showconfig.go's parseRevisionPrecondition/writeShowConfigRevision)
// through to its own call site, rather than the wiring having been
// dropped on this one handler among the ten that share it. The full
// behavioural matrix (stale-write content preservation, malformed
// headers, the concurrency proof) lives once, on kind "show" above; every
// other kind sharing writeShowConfigRevision is the same shared code
// reached from a different call site, so what is worth re-proving per
// kind is only that the call site itself was wired, not the check's own
// logic a second time.
func TestPutShowSurfaceRevisionPreconditionWiring(t *testing.T) {
	svc, st, _ := newTestIdentityServiceWithStore(t, fixedClock(testNow))
	admin := mustCreatePrincipal(t, svc, "admin-1", identity.RoleAdmin)
	token := mustIssueToken(t, svc, admin.ID)
	api := New(showObjectsTestDeps(svc, st), Options{Clock: fixedClock(testNow), Logger: testLogger()})
	mustDeclareNode(t, st, "render-01")
	mustPutShow(t, api, token, "halloween-2026", `{"name":"Halloween 2026"}`)

	putSurface := func(headers map[string]string) (*http.Response, []byte) {
		h := map[string]string{"Authorization": "Bearer " + token}
		for k, v := range headers {
			h[k] = v
		}
		req := newJSONRequest(t, http.MethodPut, "/api/v1/config/show.surface/garage-door", validSurfaceBodyNDI, h)
		return doRawRequest(t, api.Handler, req)
	}

	if resp, body := putSurface(nil); resp.StatusCode != http.StatusOK {
		t.Fatalf("unconditional create: status = %d, want 200; body: %s", resp.StatusCode, body)
	}
	if resp, body := putSurface(map[string]string{"If-Match": `"1"`}); resp.StatusCode != http.StatusOK {
		t.Fatalf("matching If-Match: status = %d, want 200; body: %s", resp.StatusCode, body)
	}
	if resp, body := putSurface(map[string]string{"If-Match": `"1"`}); resp.StatusCode != http.StatusConflict {
		t.Fatalf("stale If-Match: status = %d, want 409; body: %s", resp.StatusCode, body)
	}
	if resp, body := putSurface(map[string]string{"If-None-Match": "*"}); resp.StatusCode != http.StatusConflict {
		t.Fatalf("If-None-Match against an already-created surface: status = %d, want 409; body: %s", resp.StatusCode, body)
	}
}

// TestPutShowActiveRevisionPreconditionWiring is the same wiring smoke
// test as TestPutShowSurfaceRevisionPreconditionWiring, for the singleton
// kind show.active, which has no {id} path segment - proving the
// precondition works identically for a fixed-id singleton, not only a
// caller-chosen id.
func TestPutShowActiveRevisionPreconditionWiring(t *testing.T) {
	svc, st, _ := newTestIdentityServiceWithStore(t, fixedClock(testNow))
	admin := mustCreatePrincipal(t, svc, "admin-1", identity.RoleAdmin)
	token := mustIssueToken(t, svc, admin.ID)
	api := New(showObjectsTestDeps(svc, st), Options{Clock: fixedClock(testNow), Logger: testLogger()})
	mustPutShow(t, api, token, "halloween-2026", `{"name":"Halloween 2026"}`)

	putActive := func(headers map[string]string) (*http.Response, []byte) {
		h := map[string]string{"Authorization": "Bearer " + token}
		for k, v := range headers {
			h[k] = v
		}
		req := newJSONRequest(t, http.MethodPut, "/api/v1/config/show.active", `{"show":"halloween-2026"}`, h)
		return doRawRequest(t, api.Handler, req)
	}

	if resp, body := putActive(map[string]string{"If-None-Match": "*"}); resp.StatusCode != http.StatusOK {
		t.Fatalf("protected create: status = %d, want 200; body: %s", resp.StatusCode, body)
	}
	if resp, body := putActive(map[string]string{"If-Match": `"1"`}); resp.StatusCode != http.StatusOK {
		t.Fatalf("matching If-Match: status = %d, want 200; body: %s", resp.StatusCode, body)
	}
	if resp, body := putActive(map[string]string{"If-Match": `"1"`}); resp.StatusCode != http.StatusConflict {
		t.Fatalf("stale If-Match: status = %d, want 409; body: %s", resp.StatusCode, body)
	}
}
