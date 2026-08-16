package api

import (
	"fmt"
	"net/http"
	"strings"
	"testing"

	"github.com/showmeshsystems/showmesh/internal/coordinator/config"
	"github.com/showmeshsystems/showmesh/internal/coordinator/identity"
)

// TestOpenAPIShowActionResolumeResponseMatchesSchema proves the PUT/GET
// response for a resolume target validates against the published
// ConfigShowActionTarget/ShowActionConfigResponse schema — the openapi.yaml
// widening (integration enum plus action/ref) done alongside this seam.
func TestOpenAPIShowActionResolumeResponseMatchesSchema(t *testing.T) {
	c := newOpenAPICompiler(t)

	svc, st, _ := newTestIdentityServiceWithStore(t, fixedClock(testNow))
	admin := mustCreatePrincipal(t, svc, "admin-1", identity.RoleAdmin)
	token := mustIssueToken(t, svc, admin.ID)
	deps := showConfigTestDeps(svc, st)
	deps.ResolumeReferences = newFakeAPIResolumeResolver().withKnown("clip", "Whole House 1")
	api := New(deps, Options{Clock: fixedClock(testNow), Logger: testLogger()})

	req := newJSONRequest(t, http.MethodPut, "/api/v1/config/show.action/launch-main", validShowActionResolumeBody,
		map[string]string{"Authorization": "Bearer " + token})
	resp, body := doRawRequest(t, api.Handler, req)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200; body: %s", resp.StatusCode, body)
	}
	assertMatchesSchema(t, c, "ShowActionConfigResponse", body)
	assertMatchesSchema(t, c, "ConfigShowActionWrite", []byte(validShowActionResolumeBody))
}

// This file is Track D seam C's own end-to-end HTTP coverage for the
// resolume branch of PUT/GET /api/v1/config/show.action/{id}
// (TRACK-D-SEAM-C-MACRO-SPEC.md acceptance criteria 1-4), driven through
// the real route table exactly as showconfig_test.go's own tests are.

// fakeResolumeReferenceResolver mirrors internal/coordinator/config's own
// internal test fake of the identical name — this package cannot import
// config's _test.go helpers, so this is a second, small, independent copy,
// which is fine: neither is more than a dozen lines and both exist to
// implement one small interface.
type fakeResolumeReferenceResolver struct {
	uploaded  bool
	known     map[string]bool
	ambiguous map[string]bool
}

func newFakeAPIResolumeResolver() *fakeResolumeReferenceResolver {
	return &fakeResolumeReferenceResolver{uploaded: true, known: map[string]bool{}, ambiguous: map[string]bool{}}
}

func (f *fakeResolumeReferenceResolver) withKnown(kind, label string) *fakeResolumeReferenceResolver {
	f.known[kind+"|"+label] = true
	return f
}

func (f *fakeResolumeReferenceResolver) resolve(kind, label string) error {
	if !f.uploaded {
		return config.ErrResolumeCompositionNotUploaded
	}
	key := kind + "|" + label
	if f.ambiguous[key] {
		return fmt.Errorf("more than one %s named %q was found (candidate A, candidate B); rename one of them in Resolume to disambiguate", kind, label)
	}
	if f.known[key] {
		return nil
	}
	return fmt.Errorf("no %s in the current composition is named %q", kind, label)
}

func (f *fakeResolumeReferenceResolver) ResolveClip(ref config.ResolumeClipReference) error {
	return f.resolve("clip", ref.Clip)
}
func (f *fakeResolumeReferenceResolver) ResolveLayer(name string) error {
	return f.resolve("layer", name)
}
func (f *fakeResolumeReferenceResolver) ResolveColumn(deck, column string) error {
	return f.resolve("column", column)
}
func (f *fakeResolumeReferenceResolver) ResolveDeck(name string) error {
	return f.resolve("deck", name)
}

const validShowActionResolumeBody = `{
	"show": "halloween-2026",
	"label": "Launch main clip",
	"safetyClass": "none",
	"target": {
		"integration": "resolume",
		"action": "launchClip",
		"ref": {"clip": "Whole House 1", "deck": "Main"}
	}
}`

// TestPutShowActionResolumeAcceptedReadBackNoObjectID is acceptance
// criterion 1 at the HTTP layer: a resolume show.action is accepted,
// stored, and read back with the names intact and no object id anywhere
// in the response body.
func TestPutShowActionResolumeAcceptedReadBackNoObjectID(t *testing.T) {
	svc, st, _ := newTestIdentityServiceWithStore(t, fixedClock(testNow))
	admin := mustCreatePrincipal(t, svc, "admin-1", identity.RoleAdmin)
	token := mustIssueToken(t, svc, admin.ID)
	deps := showConfigTestDeps(svc, st)
	deps.ResolumeReferences = newFakeAPIResolumeResolver().withKnown("clip", "Whole House 1")
	api := New(deps, Options{Clock: fixedClock(testNow), Logger: testLogger()})

	req := newJSONRequest(t, http.MethodPut, "/api/v1/config/show.action/launch-main", validShowActionResolumeBody,
		map[string]string{"Authorization": "Bearer " + token})
	resp, body := doRawRequest(t, api.Handler, req)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200; body: %s", resp.StatusCode, body)
	}
	if !strings.Contains(string(body), `"launchClip"`) || !strings.Contains(string(body), `"Whole House 1"`) {
		t.Fatalf("response missing the resolved action/ref: %s", body)
	}
	// The top-level "id" is this show.action's own CONFIG object id
	// ("launch-main"), which is expected and correct. What must never
	// appear is a Resolume object id inside target.ref itself (ADR-037).
	if strings.Contains(string(body), `"ref":{"id"`) || strings.Contains(string(body), `"ref": {"id"`) {
		t.Fatalf("response leaked a Resolume object id inside target.ref: %s", body)
	}

	_, getBody := doRequest(t, api.Handler, "GET", "/api/v1/config/show.action/launch-main", map[string]string{"Authorization": "Bearer " + token})
	if !strings.Contains(string(getBody), `"launchClip"`) || !strings.Contains(string(getBody), `"Whole House 1"`) {
		t.Fatalf("read-back response missing the resolved action/ref: %s", getBody)
	}
}

// TestPutShowActionResolumeClipNotFoundRefused is acceptance criterion 2
// at the HTTP layer.
func TestPutShowActionResolumeClipNotFoundRefused(t *testing.T) {
	svc, st, _ := newTestIdentityServiceWithStore(t, fixedClock(testNow))
	admin := mustCreatePrincipal(t, svc, "admin-1", identity.RoleAdmin)
	token := mustIssueToken(t, svc, admin.ID)
	deps := showConfigTestDeps(svc, st)
	deps.ResolumeReferences = newFakeAPIResolumeResolver() // nothing known
	api := New(deps, Options{Clock: fixedClock(testNow), Logger: testLogger()})

	req := newJSONRequest(t, http.MethodPut, "/api/v1/config/show.action/launch-main", validShowActionResolumeBody,
		map[string]string{"Authorization": "Bearer " + token})
	resp, body := doRawRequest(t, api.Handler, req)
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400; body: %s", resp.StatusCode, body)
	}
	if !strings.Contains(string(body), "Whole House 1") {
		t.Fatalf("response must name the clip: %s", body)
	}
}

// TestGetShowActionListDoesNotLeakObjectIDForResolumeAction is a narrow
// extra check that the LIST route (which renders only show/label/
// currentRevision, never the payload) still works for a resolume action —
// listConfigObjectSummaries decodes only "show"/"label" from the stored
// JSON head, and a resolume payload's own extra fields must not break
// that decode.
func TestGetShowActionListDoesNotLeakObjectIDForResolumeAction(t *testing.T) {
	svc, st, _ := newTestIdentityServiceWithStore(t, fixedClock(testNow))
	admin := mustCreatePrincipal(t, svc, "admin-1", identity.RoleAdmin)
	token := mustIssueToken(t, svc, admin.ID)
	deps := showConfigTestDeps(svc, st)
	deps.ResolumeReferences = newFakeAPIResolumeResolver().withKnown("clip", "Whole House 1")
	api := New(deps, Options{Clock: fixedClock(testNow), Logger: testLogger()})

	req := newJSONRequest(t, http.MethodPut, "/api/v1/config/show.action/launch-main", validShowActionResolumeBody,
		map[string]string{"Authorization": "Bearer " + token})
	if resp, body := doRawRequest(t, api.Handler, req); resp.StatusCode != http.StatusOK {
		t.Fatalf("PUT: status = %d, want 200; body: %s", resp.StatusCode, body)
	}

	resp, body := doRequest(t, api.Handler, "GET", "/api/v1/config/show.action", map[string]string{"Authorization": "Bearer " + token})
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200; body: %s", resp.StatusCode, body)
	}
	if !strings.Contains(string(body), "launch-main") {
		t.Fatalf("list missing the object id: %s", body)
	}
}
