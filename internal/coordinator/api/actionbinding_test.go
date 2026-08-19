package api

import (
	"errors"
	"net/http"
	"strings"
	"testing"

	"github.com/showmeshsystems/showmesh/internal/coordinator/config"
	"github.com/showmeshsystems/showmesh/internal/coordinator/identity"
	"github.com/showmeshsystems/showmesh/internal/coordinator/store"
)

// TestActionBindingFPPOKAndBroken proves the fpp branch of the binding
// check: broken naming the missing instance, ok once it resolves again,
// and no network call is ever made (showConfigTestDeps' fppLister is
// static; a network call would need a live server to answer it).
func TestActionBindingFPPOKAndBroken(t *testing.T) {
	svc, st, _ := newTestIdentityServiceWithStore(t, fixedClock(testNow))
	admin := mustCreatePrincipal(t, svc, "admin-1", identity.RoleAdmin)
	token := mustIssueToken(t, svc, admin.ID)
	deps := showConfigTestDeps(svc, st)
	api := New(deps, Options{Clock: fixedClock(testNow), Logger: testLogger()})
	mustPutShow(t, api, token, "halloween-2026", `{"name":"halloween-2026"}`)

	req := newJSONRequest(t, http.MethodPut, "/api/v1/config/show.action/start-main", validShowActionFPPBody,
		map[string]string{"Authorization": "Bearer " + token})
	resp, body := doRawRequest(t, api.Handler, req)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("PUT show.action: status = %d; body: %s", resp.StatusCode, body)
	}

	_, bindBody := doRequest(t, api.Handler, "GET", "/api/v1/actions/start-main/binding", nil)
	m := decodeMap(t, bindBody)
	binding, _ := m["binding"].(map[string]any)
	if binding["state"] != "ok" {
		t.Fatalf("binding state = %v, want ok; body: %s", binding["state"], bindBody)
	}
	if reason, _ := binding["reason"].(string); reason == "" {
		t.Fatalf("binding reason must be non-empty for state ok")
	}

	// Removing the FPP endpoint (deployment reconfigured, action left
	// stale) must turn the binding broken and name the missing instance —
	// with no write refused (ADR-009: stored payloads are never
	// re-validated).
	deps.FPP = &fakeFPPLister{views: nil}
	api2 := New(deps, Options{Clock: fixedClock(testNow), Logger: testLogger()})
	_, brokenBody := doRequest(t, api2.Handler, "GET", "/api/v1/actions/start-main/binding", nil)
	m2 := decodeMap(t, brokenBody)
	binding2, _ := m2["binding"].(map[string]any)
	if binding2["state"] != "broken" {
		t.Fatalf("binding state = %v, want broken; body: %s", binding2["state"], brokenBody)
	}
	if reason, _ := binding2["reason"].(string); !strings.Contains(reason, "player-01") {
		t.Errorf("broken reason = %q, want it to name the missing instance id", reason)
	}
}

// TestActionBindingResolumeStates proves all three resolume states: ok
// (resolves), broken (does not resolve, naming the clip label), and
// unknown (no composition uploaded — never reported as ok or broken).
func TestActionBindingResolumeStates(t *testing.T) {
	svc, st, _ := newTestIdentityServiceWithStore(t, fixedClock(testNow))
	admin := mustCreatePrincipal(t, svc, "admin-1", identity.RoleAdmin)
	token := mustIssueToken(t, svc, admin.ID)
	deps := showConfigTestDeps(svc, st)
	resolver := newFakeAPIResolumeResolver().withKnown("clip", "Whole House 1")
	deps.ResolumeReferences = resolver
	api := New(deps, Options{Clock: fixedClock(testNow), Logger: testLogger()})
	mustPutShow(t, api, token, "halloween-2026", `{"name":"halloween-2026"}`)

	req := newJSONRequest(t, http.MethodPut, "/api/v1/config/show.action/launch-main", validShowActionResolumeBody,
		map[string]string{"Authorization": "Bearer " + token})
	resp, body := doRawRequest(t, api.Handler, req)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("PUT show.action: status = %d; body: %s", resp.StatusCode, body)
	}

	_, okBody := doRequest(t, api.Handler, "GET", "/api/v1/actions/launch-main/binding", nil)
	if m := decodeMap(t, okBody); m["binding"].(map[string]any)["state"] != "ok" {
		t.Fatalf("state = %v, want ok; body: %s", m["binding"], okBody)
	}

	// Re-authoring the composition (the clip renamed or removed) — the
	// stored reference no longer resolves. The resolver call is fresh
	// every check, so this needs no new write.
	resolver.known = map[string]bool{}
	_, brokenBody := doRequest(t, api.Handler, "GET", "/api/v1/actions/launch-main/binding", nil)
	m := decodeMap(t, brokenBody)
	binding := m["binding"].(map[string]any)
	if binding["state"] != "broken" {
		t.Fatalf("state = %v, want broken; body: %s", binding["state"], brokenBody)
	}
	if reason, _ := binding["reason"].(string); !strings.Contains(reason, "Whole House 1") {
		t.Errorf("broken reason = %q, want it to name the clip label", reason)
	}

	// No composition ever uploaded: unknown, never broken.
	resolver.uploaded = false
	_, unknownBody := doRequest(t, api.Handler, "GET", "/api/v1/actions/launch-main/binding", nil)
	m2 := decodeMap(t, unknownBody)
	binding2 := m2["binding"].(map[string]any)
	if binding2["state"] != "unknown" {
		t.Fatalf("state = %v, want unknown; body: %s", binding2["state"], unknownBody)
	}
}

// TestActionBindingFPPInventoryFailureLeavesResolumeBindingAnswering
// proves a Resolume-only binding does not consult FPP at all, so an FPP
// inventory load failure must not turn its check into a 500.
func TestActionBindingFPPInventoryFailureLeavesResolumeBindingAnswering(t *testing.T) {
	svc, st, _ := newTestIdentityServiceWithStore(t, fixedClock(testNow))
	admin := mustCreatePrincipal(t, svc, "admin-1", identity.RoleAdmin)
	token := mustIssueToken(t, svc, admin.ID)
	deps := showConfigTestDeps(svc, st)
	resolver := newFakeAPIResolumeResolver().withKnown("clip", "Whole House 1")
	deps.ResolumeReferences = resolver
	api := New(deps, Options{Clock: fixedClock(testNow), Logger: testLogger()})
	mustPutShow(t, api, token, "halloween-2026", `{"name":"halloween-2026"}`)

	req := newJSONRequest(t, http.MethodPut, "/api/v1/config/show.action/launch-main", validShowActionResolumeBody,
		map[string]string{"Authorization": "Bearer " + token})
	if resp, body := doRawRequest(t, api.Handler, req); resp.StatusCode != http.StatusOK {
		t.Fatalf("PUT show.action: status = %d; body: %s", resp.StatusCode, body)
	}

	deps.FPP = &fakeFPPLister{err: errors.New("fpp inventory unavailable")}
	api2 := New(deps, Options{Clock: fixedClock(testNow), Logger: testLogger()})

	resp, body := doRequest(t, api2.Handler, "GET", "/api/v1/actions/launch-main/binding", nil)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200 because this binding is Resolume-only; body: %s", resp.StatusCode, body)
	}
	m := decodeMap(t, body)
	if state := m["binding"].(map[string]any)["state"]; state != "ok" {
		t.Fatalf("state = %v, want ok; body: %s", state, body)
	}

	respList, listBody := doRequest(t, api2.Handler, "GET", "/api/v1/actions/bindings", nil)
	if respList.StatusCode != http.StatusOK {
		t.Fatalf("list status = %d, want 200; body: %s", respList.StatusCode, listBody)
	}
}

// TestActionBindingFPPInventoryFailureReportsFPPBindingUnknown proves an
// FPP-target binding reports "unknown" with a stated reason when FPP
// inventory itself failed to load — never a 500, never "broken".
func TestActionBindingFPPInventoryFailureReportsFPPBindingUnknown(t *testing.T) {
	svc, st, _ := newTestIdentityServiceWithStore(t, fixedClock(testNow))
	admin := mustCreatePrincipal(t, svc, "admin-1", identity.RoleAdmin)
	token := mustIssueToken(t, svc, admin.ID)
	deps := showConfigTestDeps(svc, st)
	api := New(deps, Options{Clock: fixedClock(testNow), Logger: testLogger()})
	mustPutShow(t, api, token, "halloween-2026", `{"name":"halloween-2026"}`)

	req := newJSONRequest(t, http.MethodPut, "/api/v1/config/show.action/start-main", validShowActionFPPBody,
		map[string]string{"Authorization": "Bearer " + token})
	if resp, body := doRawRequest(t, api.Handler, req); resp.StatusCode != http.StatusOK {
		t.Fatalf("PUT show.action: status = %d; body: %s", resp.StatusCode, body)
	}

	deps.FPP = &fakeFPPLister{err: errors.New("fpp inventory unavailable")}
	api2 := New(deps, Options{Clock: fixedClock(testNow), Logger: testLogger()})

	resp, body := doRequest(t, api2.Handler, "GET", "/api/v1/actions/start-main/binding", nil)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200; body: %s", resp.StatusCode, body)
	}
	m := decodeMap(t, body)
	binding := m["binding"].(map[string]any)
	if binding["state"] != "unknown" {
		t.Fatalf("state = %v, want unknown; body: %s", binding["state"], body)
	}
	if reason, _ := binding["reason"].(string); reason == "" {
		t.Errorf("reason is empty, want a stated reason naming the fpp inventory failure")
	}
}

// TestActionBindingUnknownActionIs404 proves the GET-one route 404s for a
// nonexistent action id rather than reporting any binding state.
func TestActionBindingUnknownActionIs404(t *testing.T) {
	svc, st, _ := newTestIdentityServiceWithStore(t, fixedClock(testNow))
	deps := showConfigTestDeps(svc, st)
	api := New(deps, Options{Clock: fixedClock(testNow), Logger: testLogger()})

	resp, body := doRequest(t, api.Handler, "GET", "/api/v1/actions/nonexistent/binding", nil)
	if resp.StatusCode != http.StatusNotFound {
		t.Fatalf("status = %d, want 404; body: %s", resp.StatusCode, body)
	}
}

// TestActionBindingRequiresNoCredential proves ADR-024 constraint 23: the
// binding check is a read, open by default, with no scope gate.
func TestActionBindingRequiresNoCredential(t *testing.T) {
	svc, st, _ := newTestIdentityServiceWithStore(t, fixedClock(testNow))
	admin := mustCreatePrincipal(t, svc, "admin-1", identity.RoleAdmin)
	token := mustIssueToken(t, svc, admin.ID)
	deps := showConfigTestDeps(svc, st)
	api := New(deps, Options{Clock: fixedClock(testNow), Logger: testLogger()})
	mustPutShow(t, api, token, "halloween-2026", `{"name":"halloween-2026"}`)
	req := newJSONRequest(t, http.MethodPut, "/api/v1/config/show.action/start-main", validShowActionFPPBody,
		map[string]string{"Authorization": "Bearer " + token})
	if resp, body := doRawRequest(t, api.Handler, req); resp.StatusCode != http.StatusOK {
		t.Fatalf("PUT show.action: status = %d; body: %s", resp.StatusCode, body)
	}

	resp, body := doRequest(t, api.Handler, "GET", "/api/v1/actions/bindings", nil)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200 with no credential; body: %s", resp.StatusCode, body)
	}
	m := decodeMap(t, body)
	bindings, _ := m["bindings"].([]any)
	if len(bindings) != 1 {
		t.Fatalf("bindings = %v, want exactly 1", bindings)
	}
}

// TestActionBindingsListFiltersByShow proves the ?show= filter narrows
// the list, and an unknown show id returns an empty list, not a refusal.
func TestActionBindingsListFiltersByShow(t *testing.T) {
	svc, st, _ := newTestIdentityServiceWithStore(t, fixedClock(testNow))
	admin := mustCreatePrincipal(t, svc, "admin-1", identity.RoleAdmin)
	token := mustIssueToken(t, svc, admin.ID)
	deps := showConfigTestDeps(svc, st)
	api := New(deps, Options{Clock: fixedClock(testNow), Logger: testLogger()})
	mustPutShow(t, api, token, "halloween-2026", `{"name":"halloween-2026"}`)

	req := newJSONRequest(t, http.MethodPut, "/api/v1/config/show.action/start-main", validShowActionFPPBody,
		map[string]string{"Authorization": "Bearer " + token})
	if resp, body := doRawRequest(t, api.Handler, req); resp.StatusCode != http.StatusOK {
		t.Fatalf("PUT show.action: status = %d; body: %s", resp.StatusCode, body)
	}

	_, matchBody := doRequest(t, api.Handler, "GET", "/api/v1/actions/bindings?show=halloween-2026", nil)
	m := decodeMap(t, matchBody)
	if bindings, _ := m["bindings"].([]any); len(bindings) != 1 {
		t.Fatalf("bindings = %v, want exactly 1", bindings)
	}

	_, emptyBody := doRequest(t, api.Handler, "GET", "/api/v1/actions/bindings?show=christmas-2026", nil)
	m2 := decodeMap(t, emptyBody)
	if bindings, _ := m2["bindings"].([]any); len(bindings) != 0 {
		t.Fatalf("bindings = %v, want empty for an unmatched show, not a refusal", bindings)
	}
}

// TestActionBindingResolumeBlackoutOKReasonNeverClaimsAResolution proves
// blackout's "ok" reason never says a reference resolved: blackout has no
// reference to resolve, even with no composition ever uploaded.
func TestActionBindingResolumeBlackoutOKReasonNeverClaimsAResolution(t *testing.T) {
	svc, st, _ := newTestIdentityServiceWithStore(t, fixedClock(testNow))
	admin := mustCreatePrincipal(t, svc, "admin-1", identity.RoleAdmin)
	token := mustIssueToken(t, svc, admin.ID)
	deps := showConfigTestDeps(svc, st)
	resolver := newFakeAPIResolumeResolver()
	resolver.uploaded = false // no composition uploaded at all
	deps.ResolumeReferences = resolver
	api := New(deps, Options{Clock: fixedClock(testNow), Logger: testLogger()})
	mustPutShow(t, api, token, "halloween-2026", `{"name":"halloween-2026"}`)

	req := newJSONRequest(t, http.MethodPut, "/api/v1/config/show.action/blackout-now", validShowActionResolumeBlackoutBody,
		map[string]string{"Authorization": "Bearer " + token})
	if resp, body := doRawRequest(t, api.Handler, req); resp.StatusCode != http.StatusOK {
		t.Fatalf("PUT show.action: status = %d; body: %s", resp.StatusCode, body)
	}

	_, body := doRequest(t, api.Handler, "GET", "/api/v1/actions/blackout-now/binding", nil)
	m := decodeMap(t, body)
	binding := m["binding"].(map[string]any)
	if binding["state"] != "ok" {
		t.Fatalf("state = %v, want ok; body: %s", binding["state"], body)
	}
	reason, _ := binding["reason"].(string)
	if strings.Contains(reason, "resolves unambiguously against the currently stored composition") {
		t.Errorf("reason = %q, want it to not claim a reference resolved (blackout resolves nothing, and no "+
			"composition was even uploaded)", reason)
	}
}

// TestActionBindingResolumeUnrecognizedActionIsUnknownNotBroken proves a
// stored action naming a resolume action this build's resolution switch
// does not recognize reports "unknown", never "broken". The revision is
// written directly into the store to simulate a pre-existing or
// hand-edited row bypassing normal write-time validation.
func TestActionBindingResolumeUnrecognizedActionIsUnknownNotBroken(t *testing.T) {
	svc, st, _ := newTestIdentityServiceWithStore(t, fixedClock(testNow))
	deps := showConfigTestDeps(svc, st)
	api := New(deps, Options{Clock: fixedClock(testNow), Logger: testLogger()})

	staleAction := config.ShowActionPayload{
		Show: "halloween-2026", Label: "Some future action", SafetyClass: config.ShowSafetyClassNone,
		Target: config.ShowActionTarget{Integration: config.ShowActionIntegrationResolume, Action: "someFutureAction"},
	}
	payloadJSON, err := config.EncodeShowActionPayload(staleAction)
	if err != nil {
		t.Fatalf("encode: %v", err)
	}
	if _, err := st.CreateConfigObject(t.Context(), config.ShowActionConfigKind, "future-action"); err != nil {
		t.Fatalf("CreateConfigObject: %v", err)
	}
	rev, err := st.CreateConfigRevision(t.Context(), store.ConfigRevisionRecord{
		Kind: config.ShowActionConfigKind, ObjectID: "future-action", Revision: 1,
		PayloadJSON: payloadJSON, Source: "api",
	})
	if err != nil {
		t.Fatalf("CreateConfigRevision: %v", err)
	}
	if _, err := st.ActivateConfigRevision(t.Context(), config.ShowActionConfigKind, "future-action", rev.Revision); err != nil {
		t.Fatalf("ActivateConfigRevision: %v", err)
	}

	_, body := doRequest(t, api.Handler, "GET", "/api/v1/actions/future-action/binding", nil)
	m := decodeMap(t, body)
	binding := m["binding"].(map[string]any)
	if binding["state"] != "unknown" {
		t.Fatalf("state = %v, want unknown; body: %s", binding["state"], body)
	}
	if reason, _ := binding["reason"].(string); reason == "" {
		t.Errorf("reason is empty, want a stated reason")
	}
}
