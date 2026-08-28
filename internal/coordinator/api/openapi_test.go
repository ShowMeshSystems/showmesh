package api

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/santhosh-tekuri/jsonschema/v6"
	"gopkg.in/yaml.v3"

	"github.com/showmeshsystems/showmesh/internal/coordinator/identity"
	"github.com/showmeshsystems/showmesh/internal/coordinator/inventory"
	"github.com/showmeshsystems/showmesh/pkg/observation"
)

// This file is the machine check Task D spec section 8 requires: a
// hand-written OpenAPI document drifts from the code it describes unless
// something actually compares them. It drives every endpoint through a
// real [API] and validates the real response body against
// api/openapi.yaml's own schema for that endpoint, in both directions —
// the document lying about a field the server does not send, and the
// server sending something the document does not describe, both fail a
// test here (every object schema in api/openapi.yaml sets
// additionalProperties: false and lists every key as required, so either
// kind of drift is a validation error, not a silent pass).
//
// The validator is github.com/santhosh-tekuri/jsonschema/v6, a pure-Go
// JSON Schema (draft 2020-12) implementation — OpenAPI 3.1's Schema
// Object is JSON Schema 2020-12 verbatim, so this document's
// "components.schemas" entries are valid JSON Schema documents with no
// translation step needed. Chosen over a heavier OpenAPI-specific
// validator (e.g. kin-openapi, libopenapi) specifically to keep this
// dependency small and unambiguously CGo-free: ADR-012 forbids any new
// CGo dependency in the coordinator's build, and this package's own gate
// (see the report) checks that explicitly. yaml.v3 is already in this
// module's dependency graph (pulled in transitively via
// internal/agent's test dependencies) and is used here only to decode
// the document into a generic Go value before handing it to jsonschema.

// loadOpenAPIDocument parses api/openapi.yaml (relative to the repository
// root; this test's working directory is this package's own directory, so
// it walks up three levels) into the `any` shape jsonschema.Compiler wants:
// YAML decoded generically, re-encoded as JSON, then decoded again through
// jsonschema.UnmarshalJSON so integers and floats are distinguished the
// way the "integer" vs "number" JSON Schema types require — a plain
// encoding/json.Unmarshal into `any` collapses every JSON number to
// float64, which would make every "type: integer" check in the document
// either always-fail or silently unenforced depending on how it were
// worked around.
func loadOpenAPIDocument(t *testing.T) any {
	t.Helper()
	path := filepath.Join("..", "..", "..", "api", "openapi.yaml")
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("reading %s: %v", path, err)
	}

	var generic any
	if err := yaml.Unmarshal(raw, &generic); err != nil {
		t.Fatalf("parsing %s as YAML: %v", path, err)
	}

	jsonBytes, err := json.Marshal(generic)
	if err != nil {
		t.Fatalf("re-encoding %s as JSON: %v", path, err)
	}

	doc, err := jsonschema.UnmarshalJSON(bytes.NewReader(jsonBytes))
	if err != nil {
		t.Fatalf("decoding %s for jsonschema: %v", path, err)
	}
	return doc
}

// newOpenAPICompiler builds a [jsonschema.Compiler] with a STRICT overlay of
// api/openapi.yaml (see [strictSchemaOverlay]) registered as a resource
// under a fixed synthetic URL, so "#/components/schemas/X" fragments
// resolve against it.
//
// This is deliberately not the raw document [loadOpenAPIDocument] returns.
// Orchestrator correction (Step 3 review finding 2.1): the PUBLISHED
// api/openapi.yaml sets no "additionalProperties: false" anywhere — a
// closed schema there would reject the coordinator's very next additive
// field, contradicting this document's own info.description ("a client
// MUST ignore fields it does not recognize") and ADR-020 decision 8
// ("within a major version the contract is additive-only"). But an Opus
// review confirmed by mutation that closed schemas are exactly what makes
// this file's tests catch drift in the direction of an undeclared field
// (see TestOpenAPIValidatorRejectsAnUndeclaredField below), so every test
// in this file that exists to catch real drift compiles against the
// strict overlay, not the published document directly — strictness lives
// here, in the test binary, never in the shipped contract.
func newOpenAPICompiler(t *testing.T) *jsonschema.Compiler {
	t.Helper()
	c := jsonschema.NewCompiler()
	c.AssertFormat() // actually validate "format: date-time", not just annotate.
	strict := strictSchemaOverlay(loadOpenAPIDocument(t))
	if err := c.AddResource(openAPIDocumentURL, strict); err != nil {
		t.Fatalf("registering the strict overlay of api/openapi.yaml as a jsonschema resource: %v", err)
	}
	return c
}

// strictSchemaOverlay walks doc (as [loadOpenAPIDocument] returns it —
// nested map[string]any / []any / json.Number / string / bool / nil, per
// [jsonschema.UnmarshalJSON]) and, for every JSON Schema object anywhere
// in the document that declares "properties", injects
// "additionalProperties": false into that same map. It mutates doc in
// place and returns it; every caller (just [newOpenAPICompiler]) loads a
// fresh document on each call, so nothing else holds a reference this
// could corrupt.
//
// This is the test-only overlay finding 2.1 asked for, restricted to
// exactly the one keyword the published document had to give up:
// "required" is left completely untouched, exactly as api/openapi.yaml
// itself authors it. Most schemas in this document list every one of
// their properties as required (Problem's supportedVersions is one
// deliberate exception — present only on an unsupported-api-version
// problem — and forcing it into "required" here would fail every OTHER
// problem response's correctly-absent supportedVersions, which is not
// drift and must not fail). Track H seam H1's ConfigShowCue/
// ConfigShowPlaylist family are the first schemas with genuinely optional
// members of their own (outputs.ltc, mismatchPolicy, fpp, and so on), so
// "required" no longer matches "properties" everywhere by coincidence
// either. Re-deriving "required" from "properties" here would silently
// overwrite all of those deliberate omissions; not touching it at all is
// what keeps this overlay strictly additive to what the document already
// declares.
//
// A schema with no "properties" key (Capability.attributes,
// Event.details — free-form maps the document deliberately leaves
// additionalProperties: true on) is left completely alone: this walk
// only closes a schema that has a known, enumerable shape to close it
// against.
func strictSchemaOverlay(doc any) any {
	switch v := doc.(type) {
	case map[string]any:
		if _, ok := v["properties"]; ok {
			v["additionalProperties"] = false
		}
		for key, val := range v {
			v[key] = strictSchemaOverlay(val)
		}
		return v
	case []any:
		for i, val := range v {
			v[i] = strictSchemaOverlay(val)
		}
		return v
	default:
		return v
	}
}

const openAPIDocumentURL = "mem://showmesh/openapi.yaml"

// compileSchema compiles the named component schema (e.g. "NodeResponse")
// from api/openapi.yaml.
func compileSchema(t *testing.T, c *jsonschema.Compiler, name string) *jsonschema.Schema {
	t.Helper()
	sch, err := c.Compile(openAPIDocumentURL + "#/components/schemas/" + name)
	if err != nil {
		t.Fatalf("compiling schema %s: %v", name, err)
	}
	return sch
}

// assertMatchesSchema validates body (a raw JSON response) against the
// named component schema, failing with the full validation error
// (jsonschema's errors are hierarchical and name the exact failing
// path/keyword) on mismatch.
func assertMatchesSchema(t *testing.T, c *jsonschema.Compiler, schemaName string, body []byte) {
	t.Helper()
	sch := compileSchema(t, c, schemaName)

	instance, err := jsonschema.UnmarshalJSON(bytes.NewReader(body))
	if err != nil {
		t.Fatalf("decoding response body for validation: %v\nbody: %s", err, body)
	}
	if err := sch.Validate(instance); err != nil {
		t.Errorf("response does not match api/openapi.yaml schema %q:\n%v\nbody: %s", schemaName, err, body)
	}
}

// requestBodySchemaRef resolves api/openapi.yaml's own
// paths[path][method].requestBody.content["application/json"].schema.$ref
// and returns the schema name it names (the fragment after the final
// "/"). This is document-pointer resolution, deliberately distinct from
// [compileSchema]/[assertMatchesSchema], which both take a schema NAME
// directly and never look at how — or whether — any operation actually
// references it. A test built only from those two can validate a fixture
// against "ConfigShowActionWrite" by name forever while the PUT operation
// itself points somewhere else entirely; this is what closes that gap.
func requestBodySchemaRef(t *testing.T, method, path string) string {
	t.Helper()
	doc := loadOpenAPIDocument(t)

	get := func(node any, key string) any {
		m, ok := node.(map[string]any)
		if !ok {
			t.Fatalf("resolving requestBody ref for %s %s: expected an object while looking for %q, got %T", method, path, key, node)
		}
		v, ok := m[key]
		if !ok {
			t.Fatalf("resolving requestBody ref for %s %s: %q is missing", method, path, key)
		}
		return v
	}

	node := get(doc, "paths")
	node = get(node, path)
	node = get(node, method)
	node = get(node, "requestBody")
	node = get(node, "content")
	node = get(node, "application/json")
	node = get(node, "schema")
	refAny := get(node, "$ref")
	ref, ok := refAny.(string)
	if !ok {
		t.Fatalf("resolving requestBody ref for %s %s: $ref is not a string: %v", method, path, refAny)
	}
	i := strings.LastIndex(ref, "/")
	if i < 0 {
		t.Fatalf("resolving requestBody ref for %s %s: %q has no \"/\"", method, path, ref)
	}
	return ref[i+1:]
}

// TestOpenAPIDocumentIsWellFormed proves the document itself at least
// parses as YAML and every schema referenced by a path in this test file
// compiles — a cheap, fast sanity check that fails clearly (not with a Go
// panic three tests later) if the document is broken outright.
func TestOpenAPIDocumentIsWellFormed(t *testing.T) {
	c := newOpenAPICompiler(t)
	for _, name := range []string{
		"ServiceDescriptor", "NodesResponse", "NodeResponse", "FPPResponse",
		"FPPInstanceResponse", "ObservationsResponse", "EventsResponse",
		"Snapshot", "Problem", "StreamStart", "StreamReset",
		"NodeChangedEvent", "FPPChangedEvent", "EventRecordedEvent",
		"FPPObservationsChangedEvent",
		"SessionResponse", "AuditResponse", "AuditEntry",
		"PrincipalSummary", "SessionInfo", "BootstrapRequest",
		"ConfigFPPEndpoint", "ConfigFPPEndpointsPayload",
		"FPPEndpointsConfigResponse", "ConfigRevisionMeta", "ConfigRevisionsResponse",
		"FPPCommandRequest", "StartPlaylistCommandRequest",
		"StopPlaylistGracefullyCommandRequest", "SetVolumeCommandRequest",
		"NoParamsFPPCommandRequest", "FPPCommandResponse", "FPPCommandResult",
		"ResolumeActionParam", "ResolumeAction", "ResolumeActionsResponse",
		"ResolumeActionRequest", "ResolumeLaunchClipActionRequest",
		"ResolumeClearLayerActionRequest", "ResolumeLaunchColumnActionRequest",
		"ResolumeSelectDeckActionRequest",
		"ResolumeBlackoutActionRequest", "ResolumeSetLayerBypassActionRequest",
		"ResolumeSetLayerMasterActionRequest", "ResolumeActionResponse",
		"ResolumeActionResult",
		"ResolumeInstanceComposition", "ResolumeInstance",
		"ResolumeInstancesResponse", "ResolumeInstanceResponse",
		"ResolumeChangedEvent",
		"ResolumeRecoveryRecordEntry", "ResolumeRecoveryRestoreLayer",
		"ResolumeRecoveryRestoreReport", "ResolumeRecoveryResponse",
		"ResolumeRecoveryRestoreResponse", "ResolumeRecoveryChangedEvent",
		"ConfigResolumeRecoveryPayload", "ResolumeRecoveryConfigResponse",
	} {
		compileSchema(t, c, name)
	}
}

// TestOpenAPISchemasMatchRealResponses drives every endpoint through a
// real [API] (the same fixtures and dependencies handlers_test.go's
// golden tests use) and validates each actual response body against
// api/openapi.yaml's schema for it.
func TestOpenAPISchemasMatchRealResponses(t *testing.T) {
	c := newOpenAPICompiler(t)
	api := buildTestAPI(t)

	tests := []struct {
		method, target, schema string
	}{
		{"GET", "/api/v1/", "ServiceDescriptor"},
		{"GET", "/api/v1/nodes", "NodesResponse"},
		{"GET", "/api/v1/nodes/media-03", "NodeResponse"},
		{"GET", "/api/v1/fpp", "FPPResponse"},
		{"GET", "/api/v1/fpp/player-01", "FPPInstanceResponse"},
		{"GET", "/api/v1/observations", "ObservationsResponse"},
		{"GET", "/api/v1/events", "EventsResponse"},
		{"GET", "/api/v1/snapshot", "Snapshot"},
		{"GET", "/api/v1/session", "SessionResponse"},
		{"GET", "/api/v1/resolume/instances", "ResolumeInstancesResponse"},
		{"GET", "/api/v1/resolume/instances/resolume", "ResolumeInstanceResponse"},
	}

	for _, tt := range tests {
		t.Run(tt.schema, func(t *testing.T) {
			resp, body := doRequest(t, api.Handler, tt.method, tt.target, nil)
			if resp.StatusCode != http.StatusOK {
				t.Fatalf("status = %d, want 200; body: %s", resp.StatusCode, body)
			}
			assertMatchesSchema(t, c, tt.schema, body)
		})
	}
}

// TestOpenAPIAuthenticatedResponsesMatchRealResponses is
// [TestOpenAPISchemasMatchRealResponses]'s ADR-024 sibling: the response
// shapes that only exist once a real identity.Service is wired and a
// principal has actually authenticated — POST /api/v1/session's success
// body (a session cookie freshly minted, principal/session both
// non-null) and GET /api/v1/audit's body (behind audit:read) — validated
// against real responses from a real coordinator wiring, not buildTestAPI's
// no-op Identity default.
func TestOpenAPIAuthenticatedResponsesMatchRealResponses(t *testing.T) {
	c := newOpenAPICompiler(t)

	svc := newTestIdentityService(t, fixedClock(testNow))
	admin := mustCreatePrincipal(t, svc, "admin-1", identity.RoleAdmin)
	api := New(authTestDeps(svc), Options{Clock: fixedClock(testNow), Logger: testLogger()})

	body := `{"name":"admin-1","password":` + `"` + testPassword + `"` + `,"deviceLabel":"laptop"}`
	req := httptest.NewRequest(http.MethodPost, "/api/v1/session", strings.NewReader(body))
	req.Header.Set("Sec-Fetch-Site", "same-origin") // login CSRF (S0-2): required, rejected when absent
	rec := httptest.NewRecorder()
	api.Handler.ServeHTTP(rec, req)
	resp := rec.Result()
	loginBody, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("login: status = %d, want 200; body: %s", resp.StatusCode, loginBody)
	}
	assertMatchesSchema(t, c, "SessionResponse", loginBody)

	token := mustIssueToken(t, svc, admin.ID)
	_, auditBody := doRequest(t, api.Handler, "GET", "/api/v1/audit", map[string]string{"Authorization": "Bearer " + token})
	assertMatchesSchema(t, c, "AuditResponse", auditBody)

	// The newest-first page is a second real response shape (order echoed
	// "desc", the same required id per entry), so it is validated rather
	// than assumed to match the ascending one.
	_, descBody := doRequest(t, api.Handler, "GET", "/api/v1/audit?order=desc&limit=2", map[string]string{"Authorization": "Bearer " + token})
	assertMatchesSchema(t, c, "AuditResponse", descBody)
}

// TestOpenAPIConfigResponsesMatchRealResponses is Step 7 seam A's own
// conformance test: PUT /config/fpp.endpoints' success body, GET
// /config/fpp.endpoints' body, and GET /config/fpp.endpoints/revisions'
// body, each validated against a REAL response from a real coordinator
// wiring (not hand-built JSON) — BUILD-PLAN Step 7's "api/openapi.yaml
// grows additively, stays conformance-tested in both directions."
func TestOpenAPIConfigResponsesMatchRealResponses(t *testing.T) {
	c := newOpenAPICompiler(t)

	svc, st, _ := newTestIdentityServiceWithStore(t, fixedClock(testNow))
	admin := mustCreatePrincipal(t, svc, "admin-1", identity.RoleAdmin)
	token := mustIssueToken(t, svc, admin.ID)
	api := New(configTestDeps(svc, st), Options{Clock: fixedClock(testNow), Logger: testLogger()})

	putReq := newJSONRequest(t, http.MethodPut, "/api/v1/config/fpp.endpoints", validFPPEndpointsBody,
		map[string]string{"Authorization": "Bearer " + token})
	putResp, putBody := doRawRequest(t, api.Handler, putReq)
	if putResp.StatusCode != http.StatusOK {
		t.Fatalf("PUT: status = %d, want 200; body: %s", putResp.StatusCode, putBody)
	}
	assertMatchesSchema(t, c, "FPPEndpointsConfigResponse", putBody)

	_, getBody := doRequest(t, api.Handler, "GET", "/api/v1/config/fpp.endpoints", map[string]string{"Authorization": "Bearer " + token})
	assertMatchesSchema(t, c, "FPPEndpointsConfigResponse", getBody)

	_, revBody := doRequest(t, api.Handler, "GET", "/api/v1/config/fpp.endpoints/revisions", map[string]string{"Authorization": "Bearer " + token})
	assertMatchesSchema(t, c, "ConfigRevisionsResponse", revBody)
}

// TestOpenAPIAssetsSettingsConfigResponsesMatchRealResponses is Track G
// seam G-4's own conformance coverage, mirroring
// TestOpenAPIConfigResponsesMatchRealResponses exactly for the
// assets.settings kind — including a SECOND, partial PUT, since this
// kind's own PUT payload schema (ConfigAssetsSettingsPutPayload) is
// deliberately different from every field being present.
func TestOpenAPIAssetsSettingsConfigResponsesMatchRealResponses(t *testing.T) {
	c := newOpenAPICompiler(t)

	svc, st, _ := newTestIdentityServiceWithStore(t, fixedClock(testNow))
	admin := mustCreatePrincipal(t, svc, "admin-1", identity.RoleAdmin)
	token := mustIssueToken(t, svc, admin.ID)
	api := New(configTestDeps(svc, st), Options{Clock: fixedClock(testNow), Logger: testLogger()})

	putReq := newJSONRequest(t, http.MethodPut, "/api/v1/config/assets.settings", validAssetsSettingsBody,
		map[string]string{"Authorization": "Bearer " + token})
	putResp, putBody := doRawRequest(t, api.Handler, putReq)
	if putResp.StatusCode != http.StatusOK {
		t.Fatalf("PUT: status = %d, want 200; body: %s", putResp.StatusCode, putBody)
	}
	assertMatchesSchema(t, c, "AssetsSettingsConfigResponse", putBody)

	partialReq := newJSONRequest(t, http.MethodPut, "/api/v1/config/assets.settings", `{"syncIntervalSeconds":600}`,
		map[string]string{"Authorization": "Bearer " + token})
	partialResp, partialBody := doRawRequest(t, api.Handler, partialReq)
	if partialResp.StatusCode != http.StatusOK {
		t.Fatalf("partial PUT: status = %d, want 200; body: %s", partialResp.StatusCode, partialBody)
	}
	assertMatchesSchema(t, c, "AssetsSettingsConfigResponse", partialBody)

	_, getBody := doRequest(t, api.Handler, "GET", "/api/v1/config/assets.settings", map[string]string{"Authorization": "Bearer " + token})
	assertMatchesSchema(t, c, "AssetsSettingsConfigResponse", getBody)

	_, revBody := doRequest(t, api.Handler, "GET", "/api/v1/config/assets.settings/revisions", map[string]string{"Authorization": "Bearer " + token})
	assertMatchesSchema(t, c, "ConfigRevisionsResponse", revBody)
}

// TestOpenAPIDiscoveryResponsesMatchRealResponses is finding 15's own
// regression test: seam B's three routes (POST /discovery/runs, POST
// /nodes/{nodeId}/declaration, DELETE /nodes/{nodeId}/declaration) had NO
// conformance coverage at all, following exactly the pattern
// TestOpenAPIConfigResponsesMatchRealResponses already established for
// seam A. Without it, renaming a field on the wire — or the actual defect
// this test's own construction found, api/openapi.yaml documenting these
// two POST/DELETE operations under `/nodes/{nodeId}` rather than the real
// route `/nodes/{nodeId}/declaration` — broke no test in this package.
func TestOpenAPIDiscoveryResponsesMatchRealResponses(t *testing.T) {
	c := newOpenAPICompiler(t)

	nodes := &fakeNodeLister{views: []inventory.NodeView{liveNodeView("shed-01")}}
	deps, _, _ := newTestDiscoveryDeps(t, fixedClock(testNow), nodes, nil)
	admin := mustCreatePrincipal(t, deps.Identity, "admin-1", identity.RoleAdmin)
	token := mustIssueToken(t, deps.Identity, admin.ID)
	api := New(deps, Options{Clock: fixedClock(testNow), Logger: testLogger()})

	runReq := httptest.NewRequest(http.MethodPost, "/api/v1/discovery/runs", nil)
	runReq.Header.Set("Authorization", "Bearer "+token)
	runResp, runBody := doRawRequest(t, api.Handler, runReq)
	if runResp.StatusCode != http.StatusOK {
		t.Fatalf("POST /discovery/runs: status = %d, want 200; body: %s", runResp.StatusCode, runBody)
	}
	assertMatchesSchema(t, c, "DiscoveryRunResponse", runBody)

	declareReq := newJSONRequest(t, http.MethodPost, "/api/v1/nodes/shed-01/declaration", `{"label":"Shed controller"}`, nil)
	declareReq.Header.Set("Authorization", "Bearer "+token)
	declareResp, declareBody := doRawRequest(t, api.Handler, declareReq)
	if declareResp.StatusCode != http.StatusOK {
		t.Fatalf("POST /nodes/shed-01/declaration: status = %d, want 200; body: %s", declareResp.StatusCode, declareBody)
	}
	assertMatchesSchema(t, c, "NodeDeclarationResponse", declareBody)

	deleteReq := newJSONRequest(t, http.MethodDelete, "/api/v1/nodes/shed-01/declaration", `{"confirm":true}`, nil)
	deleteReq.Header.Set("Authorization", "Bearer "+token)
	deleteResp, deleteBody := doRawRequest(t, api.Handler, deleteReq)
	if deleteResp.StatusCode != http.StatusNoContent {
		t.Fatalf("DELETE /nodes/shed-01/declaration: status = %d, want 204; body: %s", deleteResp.StatusCode, deleteBody)
	}
	if len(deleteBody) != 0 {
		t.Errorf("DELETE /nodes/shed-01/declaration: body = %q, want empty (204 No Content)", deleteBody)
	}
}

// TestOpenAPIBootstrapResponseMatchesRealResponse is this file's ADR-024
// decision 9 sibling: POST /api/v1/bootstrap's success body is the same
// SessionResponse shape POST /api/v1/session's own conformance test
// above already validates against a login response, but bootstrap is its
// own code path (bootstrap.go, not session.go) and BUILD-PLAN Step 6
// requires api/openapi.yaml stay conformance-green "in both directions"
// for the endpoint this step adds — this is the real response that
// proves it, not an inference from a different endpoint's passing test.
func TestOpenAPIBootstrapResponseMatchesRealResponse(t *testing.T) {
	c := newOpenAPICompiler(t)

	svc, dataDir := newTestIdentityServiceWithDataDir(t, fixedClock(testNow))
	mustEnsureBootstrap(t, svc)
	code := readBootstrapCode(t, dataDir)
	api := New(authTestDeps(svc), Options{Clock: fixedClock(testNow), Logger: testLogger()})

	body := `{"code":"` + code + `","name":"first-admin","password":"a-strong-password-1","deviceLabel":"laptop"}`
	req := httptest.NewRequest(http.MethodPost, "/api/v1/bootstrap", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Sec-Fetch-Site", "same-origin") // login CSRF (S0-2): required, rejected when absent
	rec := httptest.NewRecorder()
	api.Handler.ServeHTTP(rec, req)
	resp := rec.Result()
	respBody, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200; body: %s", resp.StatusCode, respBody)
	}
	assertMatchesSchema(t, c, "SessionResponse", respBody)
}

// TestOpenAPIProblemSchemaMatchesEveryClass validates a real problem
// response from EVERY class this API produces against the shared Problem
// schema — four from Step 3, method-not-allowed (finding 2.8), and all
// four of ADR-024's additions (unauthorized is reused, not duplicated:
// decision 4's 401 and ADR-021's are the identical wire class). Step 8's
// two additions (fpp-command-refused-audit-unavailable,
// fpp-start-playlist-evidence-not-current) are NOT in this table — they
// are verified against the shared Problem schema in
// openapi_fppcommand_test.go's own dedicated tests instead
// (TestOpenAPIFPPCommandAuditUnavailableResponseMatchesRealResponse,
// TestOpenAPIFPPStartPlaylistEvidenceNotCurrentResponseMatchesRealResponse),
// because each needs its own real dispatch setup (an installed
// fail-audit trigger; stale/absent evidence) this table's shared
// `api.Handler` cannot produce. Named here, rather than silently
// omitted, so this comment's own "EVERY class" claim stays checkable
// against where each one actually is.
//
// forbidden and csrf-rejected are exercised here directly, against a real
// identity.Service-backed principal, rather than cited as "covered
// elsewhere": a review finding caught that this comment used to point at
// a test named TestWriteEndpointForbiddenNamesMissingScope in
// session_test.go, which did not exist anywhere in this package — a
// citation for coverage this file never actually had. forbidden's real
// end-to-end 403 behavior (a viewer denied audit:read, naming the missing
// scope) is still its own, more detailed test in audit_test.go
// (TestAuditForbiddenForViewerNamesMissingScope); csrf-rejected's is
// session_test.go's TestDeleteSessionRequiresSecFetchSiteForCookie. What
// THIS test adds on top of both is the one thing neither of them checks:
// that the actual response body validates against api/openapi.yaml's
// shared Problem schema, the same way every other class in this table
// does.
func TestOpenAPIProblemSchemaMatchesEveryClass(t *testing.T) {
	c := newOpenAPICompiler(t)
	api := buildTestAPI(t)
	closedReadsAPI := New(Dependencies{
		Nodes: &fakeNodeLister{}, FPP: &fakeFPPLister{}, Observations: &fakeObservationLister{},
		Events: &fakeEventReader{}, Collectors: &fakeCollectorStatusLister{},
	}, Options{CloseReads: true, Clock: fixedClock(testNow), Logger: testLogger()})

	svc := newTestIdentityService(t, fixedClock(testNow))
	viewer := mustCreatePrincipal(t, svc, "viewer-1", identity.RoleViewer)
	viewerToken := mustIssueToken(t, svc, viewer.ID)
	scopedAPI := New(authTestDeps(svc), Options{Clock: fixedClock(testNow), Logger: testLogger()})
	scopedCookie := loginAndGetCookie(t, scopedAPI.Handler, viewer.Name, testPassword)

	tests := []struct {
		name    string
		handler http.Handler
		method  string
		target  string
		headers map[string]string
		// wantStatus and wantType are checked only when non-zero/non-empty
		// — see the "login-csrf-rejected" entry below and its comment for
		// why this row in particular needs them: a subtest named for a
		// specific problem CLASS must fail when that class stops being
		// what the response actually is, not merely when the response
		// stops being Problem-SHAPED.
		wantStatus int
		wantType   string
	}{
		{"resource-not-found", api.Handler, "GET", "/api/v1/nodes/nonexistent", nil, 0, ""},
		{"invalid-parameter", api.Handler, "GET", "/api/v1/nodes/Not_Valid!", nil, 0, ""},
		{"unsupported-api-version", api.Handler, "GET", "/api/v2/nodes", nil, 0, ""},
		{"unauthorized", closedReadsAPI.Handler, "GET", "/api/v1/nodes", nil, 0, ""},
		{"method-not-allowed", api.Handler, "POST", "/api/v1/nodes", nil, 0, ""},
		{"credential-in-url", api.Handler, "GET", "/api/v1/nodes?tok=" + identity.TokenPrefix + "leaked", nil, 0, ""},
		// forbidden: a real, authenticated viewer (holds every read scope
		// but not audit:read) denied GET /api/v1/audit.
		{"forbidden", scopedAPI.Handler, "GET", "/api/v1/audit", map[string]string{"Authorization": "Bearer " + viewerToken}, 0, ""},
		// csrf-rejected: a real, cookie-authenticated DELETE with no
		// Sec-Fetch-Site header.
		{"csrf-rejected", scopedAPI.Handler, "DELETE", "/api/v1/session", map[string]string{"Cookie": sessionCookieName + "=" + scopedCookie}, 0, ""},
		// login-csrf-rejected (Step 7 seam 0, S0-2): POST /api/v1/session
		// with no Sec-Fetch-Site header at all — unauthenticated by
		// construction, so this checks before any credential or body is
		// even considered. api/openapi.yaml's LoginCSRFRejected response
		// conformance-checked here in the "response matches its documented
		// Problem shape" direction; TestLoginCSRFRejectedWhenHeaderAbsent
		// in session_test.go checks the other direction (the real 403
		// status code and the real predicate).
		//
		// F5 review finding: this row used to assert ONLY the Problem
		// shape, which a lax mutation confirmed passes even with login
		// CSRF checking removed entirely (the endpoint then answers 401
		// instead of 403, still Problem-shaped) — a subtest whose name
		// says "login CSRF" must fail when login CSRF is gone. wantStatus/
		// wantType close that: both the status code AND the problem type
		// are asserted below, not merely "some Problem-shaped body".
		{"login-csrf-rejected", api.Handler, "POST", "/api/v1/session", nil, http.StatusForbidden, ProblemTypeCSRFRejected},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			resp, body := doRequest(t, tt.handler, tt.method, tt.target, tt.headers)
			assertMatchesSchema(t, c, "Problem", body)
			if tt.wantStatus != 0 && resp.StatusCode != tt.wantStatus {
				t.Errorf("status = %d, want %d", resp.StatusCode, tt.wantStatus)
			}
			if tt.wantType != "" {
				m := decodeMap(t, body)
				if m["type"] != tt.wantType {
					t.Errorf("type = %v, want %v", m["type"], tt.wantType)
				}
			}
		})
	}

	// too-many-requests is checked against the schema directly, through
	// this package's own writeProblem — the same function every response
	// above went through — rather than by racing loginLimiter's real
	// concurrency bound here (session_test.go's
	// TestLoginConcurrencyLimitRejectsWithRetryAfter exercises the real
	// mechanism end to end with real goroutines; duplicating that
	// orchestration here would only be for a schema check this simpler
	// path already gives).
	t.Run("too-many-requests", func(t *testing.T) {
		rec := httptest.NewRecorder()
		writeProblem(rec, testLogger(), testNow, tooManyRequestsProblem("too many concurrent login attempts"))
		assertMatchesSchema(t, c, "Problem", rec.Body.Bytes())
	})
}

// TestMethodNotAllowedHasAllowHeaderAndCorrectType proves finding 2.8's
// fix end to end through a real [API], not just that the body matches
// Problem's schema (TestOpenAPIProblemSchemaMatchesEveryClass above
// already does that): a POST to a route this API only serves as GET must
// come back 405, with a non-empty Allow header naming GET, and a
// problem+json body whose type is exactly [ProblemTypeMethodNotAllowed]
// — not merely "some Problem-shaped body", which the schema check alone
// would already accept even if a caller returned the wrong problem class
// entirely.
func TestMethodNotAllowedHasAllowHeaderAndCorrectType(t *testing.T) {
	api := buildTestAPI(t)

	resp, body := doRequest(t, api.Handler, "POST", "/api/v1/nodes", nil)
	if resp.StatusCode != http.StatusMethodNotAllowed {
		t.Fatalf("status = %d, want 405; body: %s", resp.StatusCode, body)
	}
	if allow := resp.Header.Get("Allow"); allow == "" {
		t.Fatalf("Allow header is empty; a 405 without it does not tell a client what it may do instead")
	} else if !strings.Contains(allow, "GET") {
		t.Fatalf("Allow header = %q, want it to name GET", allow)
	}
	if ct := resp.Header.Get("Content-Type"); ct != "application/problem+json" {
		t.Fatalf("Content-Type = %q, want application/problem+json — net/http's own default 405 body must not leak through", ct)
	}

	var p struct {
		Type string `json:"type"`
	}
	if err := json.Unmarshal(body, &p); err != nil {
		t.Fatalf("decoding 405 body: %v\nbody: %s", err, body)
	}
	if p.Type != ProblemTypeMethodNotAllowed {
		t.Fatalf("problem type = %q, want %q", p.Type, ProblemTypeMethodNotAllowed)
	}
}

// TestOpenAPIValidatorRejectsAMismatch proves the validator is not
// decorative: a response missing a field the schema requires must fail.
// Without a test like this, a validator that always reports success would
// pass every test above for the wrong reason.
func TestOpenAPIValidatorRejectsAMismatch(t *testing.T) {
	c := newOpenAPICompiler(t)
	sch := compileSchema(t, c, "NodeResponse")

	// A NodeResponse missing serverTime entirely — exactly the shape of
	// defect the orchestrator's correction (section 6.2's serverTime
	// requirement) was about.
	bad := []byte(`{"node":{"nodeId":"x","label":null,"platform":null,"agentVersion":null,"bootId":null,"startedAt":null,"firstSeenAt":"2026-01-01T00:00:00Z","updatedAt":"2026-01-01T00:00:00Z","capabilities":[],"controlPlane":{"state":"unknown","reason":"x"},"evidence":{"hello":{},"lastWill":{},"heartbeat":{}}}}`)
	instance, err := jsonschema.UnmarshalJSON(bytes.NewReader(bad))
	if err != nil {
		t.Fatalf("decoding deliberately-bad instance: %v", err)
	}
	if err := sch.Validate(instance); err == nil {
		t.Fatalf("validator accepted a NodeResponse missing serverTime; the schema/validator pairing is not catching real drift")
	}
}

// TestOpenAPIValidatorRejectsAnUndeclaredField proves the OTHER direction
// finding 2.1's fix must not lose. api/openapi.yaml on disk no longer sets
// additionalProperties: false anywhere (finding 2.1: a closed published
// schema would reject the coordinator's very next additive field, which
// ADR-020 decision 8 makes a normal, permitted event). That means the
// PUBLISHED document, compiled on its own, would happily accept a
// response carrying a field it never declared — this test proves that
// [newOpenAPICompiler]'s strict overlay (additionalProperties: false,
// injected only for this test binary — see [strictSchemaOverlay]) still
// catches it, which is the specific strength the Step 3 review confirmed
// by mutation and asked to be preserved rather than quietly dropped
// along with the published strictness.
func TestOpenAPIValidatorRejectsAnUndeclaredField(t *testing.T) {
	c := newOpenAPICompiler(t)
	sch := compileSchema(t, c, "NodeResponse")

	// Every field NodeResponse/Node actually declare, plus one this
	// schema does not: "unexpectedField".
	extra := []byte(nodeResponseJSONWithExtraField)
	instance, err := jsonschema.UnmarshalJSON(bytes.NewReader(extra))
	if err != nil {
		t.Fatalf("decoding deliberately-extra instance: %v", err)
	}
	if err := sch.Validate(instance); err == nil {
		t.Fatalf("validator accepted a NodeResponse with an undeclared field; the strict test-only overlay (finding 2.1) is not being applied — check that newOpenAPICompiler still calls strictSchemaOverlay")
	}
}

// TestStrictSchemaOverlayLeavesPublishedDocumentPermissive is the other
// half of finding 2.1's proof: the PUBLISHED document — what
// [loadOpenAPIDocument] returns with no overlay applied — must accept
// exactly the same undeclared-field instance
// [TestOpenAPIValidatorRejectsAnUndeclaredField] proves the strict
// overlay rejects. If this test ever fails, api/openapi.yaml has grown a
// closed schema again (a hand-edit re-adding additionalProperties: false,
// most likely), which would break every real client the moment this
// coordinator adds a field — exactly the defect finding 2.1 was raised
// against.
func TestStrictSchemaOverlayLeavesPublishedDocumentPermissive(t *testing.T) {
	c := jsonschema.NewCompiler()
	c.AssertFormat()
	if err := c.AddResource(openAPIDocumentURL, loadOpenAPIDocument(t)); err != nil {
		t.Fatalf("registering the unmodified api/openapi.yaml as a jsonschema resource: %v", err)
	}
	sch, err := c.Compile(openAPIDocumentURL + "#/components/schemas/NodeResponse")
	if err != nil {
		t.Fatalf("compiling schema NodeResponse: %v", err)
	}

	extra := []byte(nodeResponseJSONWithExtraField)
	instance, err := jsonschema.UnmarshalJSON(bytes.NewReader(extra))
	if err != nil {
		t.Fatalf("decoding deliberately-extra instance: %v", err)
	}
	if err := sch.Validate(instance); err != nil {
		t.Fatalf("the published api/openapi.yaml document rejected a response with an additional field it does not declare: %v\n"+
			"this document must stay additive-only-compatible (ADR-020 decision 8) — the closed-schema check belongs only in the strict test overlay, see strictSchemaOverlay", err)
	}
}

// validEvidenceJSON is a minimal, but fully schema-valid, Evidence
// envelope literal — every property Evidence's schema requires, with
// values chosen only to satisfy types (e.g. collectedAt null pairs with
// state "not_collected" the way mapping.go's collectedAtForWire actually
// produces it, but this JSON is hand-built, not server output; nothing
// here claims to be a real response).
const validEvidenceJSON = `{"signal":"node.hello","value":null,"unit":null,"state":"not_collected","reason":"no hello observed yet","observedAt":null,"collectedAt":null,"source":"mqtt-inventory","quality":"direct","validForSeconds":null}`

// nodeResponseJSONWithExtraField is a NodeResponse carrying every field
// its own schema (NodeResponse -> Node -> NodeEvidence -> Evidence)
// declares, with valid values throughout, plus exactly one field none of
// those schemas declare: "unexpectedField" at the top level. Shared
// between [TestOpenAPIValidatorRejectsAnUndeclaredField] and
// [TestStrictSchemaOverlayLeavesPublishedDocumentPermissive], which
// validate this same instance against the strict overlay and the
// published document respectively and must see opposite outcomes.
// validDeclarationJSON is a minimal, but fully schema-valid,
// NodeDeclaration envelope literal for an undeclared node ("declared":
// false, every other field null, "discoveryState": "not_applicable" —
// see mapNodeDeclaration's own doc comment in discovery.go for why this is
// exactly what an undeclared node renders), used the same way
// validEvidenceJSON is: hand-built, not server output.
const validDeclarationJSON = `{"declared":false,"label":null,"notes":null,"declaredAt":null,"declaredByPrincipalId":null,"declaredByPrincipalName":null,"discoveryState":"not_applicable","discoveryReason":null,"lastDiscoveryRunId":null,"lastDiscoveredAt":null,"notSeenAsOfRunId":null,"notSeenAsOfRunFinishedAt":null}`

var nodeResponseJSONWithExtraField = `{"serverTime":"2026-01-01T00:00:00Z","node":{"nodeId":"x","label":null,"platform":null,"agentVersion":null,"bootId":null,"startedAt":null,"firstSeenAt":"2026-01-01T00:00:00Z","updatedAt":"2026-01-01T00:00:00Z","capabilities":[],"controlPlane":{"state":"unknown","reason":"x"},"evidence":{"hello":` +
	validEvidenceJSON + `,"lastWill":` + validEvidenceJSON + `,"heartbeat":` + validEvidenceJSON + `},"declaration":` + validDeclarationJSON + `,"render":[],"audio":[],"clock":[]},"unexpectedField":"surprise"}`

// TestOpenAPIStreamEventSchemasMatchRealFrames validates one real
// stream.start frame's JSON payload — obtained the same way
// stream_test.go does, over a real SSE connection — against the
// StreamStart schema, so the stream's documented shapes are checked
// against something a real connection actually sent, not merely
// hand-typed examples.
func TestOpenAPIStreamEventSchemasMatchRealFrames(t *testing.T) {
	c := newOpenAPICompiler(t)

	nodes := &fakeNodeLister{}
	testAPI := newStreamTestAPI(Dependencies{
		Nodes: nodes, FPP: &fakeFPPLister{}, Observations: &fakeObservationLister{},
		Events: &fakeEventReader{}, Collectors: &fakeCollectorStatusLister{},
	})

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go testAPI.Hub.Run(ctx)

	srv := httptest.NewServer(testAPI.Handler)
	defer srv.Close()

	resp, err := http.Get(srv.URL + "/api/v1/stream")
	if err != nil {
		t.Fatalf("GET /api/v1/stream: %v", err)
	}
	defer func() { _ = resp.Body.Close() }()
	r := bufio.NewReader(resp.Body)

	event, data := readEventWithTimeout(t, r, 5*time.Second)
	if event != "stream.start" {
		t.Fatalf("event = %q, want stream.start", event)
	}
	assertMatchesSchema(t, c, "StreamStart", []byte(data))

	nodes.setViews([]inventory.NodeView{onlineNodeFixture(t)})
	testAPI.Hub.Notify()

	event, data = readEventWithTimeout(t, r, 5*time.Second)
	if event != "node.changed" {
		t.Fatalf("event = %q, want node.changed", event)
	}
	assertMatchesSchema(t, c, "NodeChangedEvent", []byte(data))
}

// TestOpenAPIFPPObservationsChangedEventSchemaMatchesRealFrame is
// [TestOpenAPIStreamEventSchemasMatchRealFrames]'s ADR-023 sibling: a real
// fpp.observations.changed frame — obtained over a live `?deltas=1`
// connection, from a genuine observation-level value change — validated
// against its own schema, so the conformance guarantee covers the new
// frame kind in both directions exactly like every other event this
// package emits.
func TestOpenAPIFPPObservationsChangedEventSchemaMatchesRealFrame(t *testing.T) {
	c := newOpenAPICompiler(t)

	fpp := &mutableFPPLister{}
	pollAt := testNow
	fpp.setViews([]FPPInstanceView{{
		InstanceID: "player-01", Endpoint: "http://10.0.1.20",
		Observations: []observation.Observation{deltaObs(t, sigUptime, int64(1), pollAt, time.Minute)},
		LastPollAt:   &pollAt,
	}})

	testAPI := newStreamTestAPI(Dependencies{
		Nodes: &fakeNodeLister{}, FPP: fpp, Observations: &fakeObservationLister{},
		Events: &fakeEventReader{}, Collectors: &fakeCollectorStatusLister{},
	})

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go testAPI.Hub.Run(ctx)

	srv := httptest.NewServer(testAPI.Handler)
	defer srv.Close()

	resp, err := http.Get(srv.URL + "/api/v1/stream?deltas=1")
	if err != nil {
		t.Fatalf("GET /api/v1/stream?deltas=1: %v", err)
	}
	defer func() { _ = resp.Body.Close() }()
	r := bufio.NewReader(resp.Body)

	if event, _ := readEventWithTimeout(t, r, 5*time.Second); event != "stream.start" {
		t.Fatalf("event = %q, want stream.start", event)
	}

	testAPI.Hub.Notify()
	if event, _ := readEventWithTimeout(t, r, 5*time.Second); event != "fpp.changed" {
		t.Fatalf("first Notify: event = %q, want fpp.changed", event)
	}

	// A genuine observation-only value change — see this file's own
	// deltas_test.go for why this is what produces
	// fpp.observations.changed rather than fpp.changed on a
	// delta-subscribed connection.
	pollAt2 := pollAt.Add(15 * time.Second)
	fpp.setViews([]FPPInstanceView{{
		InstanceID: "player-01", Endpoint: "http://10.0.1.20",
		Observations: []observation.Observation{deltaObs(t, sigUptime, int64(2), pollAt2, time.Minute)},
		LastPollAt:   &pollAt2,
	}})
	testAPI.Hub.Notify()

	event, data := readEventWithTimeout(t, r, 5*time.Second)
	if event != "fpp.observations.changed" {
		t.Fatalf("event = %q, want fpp.observations.changed", event)
	}
	assertMatchesSchema(t, c, "FPPObservationsChangedEvent", []byte(data))
}

// TestOpenAPIResolumeInstancesResponsesMatchRealResponses is Track D seam
// E's own conformance test: the list route, the single-instance route, and
// a real resolume.changed stream frame, each validated against a REAL
// response from a real coordinator wiring (not hand-built JSON).
func TestOpenAPIResolumeInstancesResponsesMatchRealResponses(t *testing.T) {
	c := newOpenAPICompiler(t)
	testAPI := buildTestAPI(t)

	_, listBody := doRequest(t, testAPI.Handler, "GET", "/api/v1/resolume/instances", nil)
	assertMatchesSchema(t, c, "ResolumeInstancesResponse", listBody)

	_, oneBody := doRequest(t, testAPI.Handler, "GET", "/api/v1/resolume/instances/resolume", nil)
	assertMatchesSchema(t, c, "ResolumeInstanceResponse", oneBody)

	resolume := &mutableResolumeLister{}
	resolume.setViews([]ResolumeInstanceView{{
		InstanceID:   "resolume",
		Observations: []observation.Observation{deltaObs(t, "resolume.reachable", true, testNow, 30*time.Second)},
	}})
	streamAPI := newStreamTestAPI(Dependencies{
		Nodes: &fakeNodeLister{}, FPP: &fakeFPPLister{}, Observations: &fakeObservationLister{},
		Events: &fakeEventReader{}, Collectors: &fakeCollectorStatusLister{}, Resolume: resolume,
	})
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go streamAPI.Hub.Run(ctx)

	srv := httptest.NewServer(streamAPI.Handler)
	defer srv.Close()

	resp, err := http.Get(srv.URL + "/api/v1/stream")
	if err != nil {
		t.Fatalf("GET /api/v1/stream: %v", err)
	}
	defer func() { _ = resp.Body.Close() }()
	r := bufio.NewReader(resp.Body)

	if event, _ := readEventWithTimeout(t, r, 5*time.Second); event != "stream.start" {
		t.Fatalf("event = %q, want stream.start", event)
	}
	streamAPI.Hub.Notify()
	event, data := readEventWithTimeout(t, r, 5*time.Second)
	if event != "resolume.changed" {
		t.Fatalf("event = %q, want resolume.changed", event)
	}
	assertMatchesSchema(t, c, "ResolumeChangedEvent", []byte(data))
}
