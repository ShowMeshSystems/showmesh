package api

import (
	"net/http"
	"sort"
	"testing"

	"github.com/showmeshsystems/showmesh/internal/coordinator/identity"
	"github.com/showmeshsystems/showmesh/internal/coordinator/inventory"
	"github.com/showmeshsystems/showmesh/pkg/observation"
)

// This file is the audio configuration kinds' own OpenAPI conformance
// suite, mirroring openapi_rendersettings_test.go's pattern of driving a
// REAL [API] and validating its actual response body against
// api/openapi.yaml's own schema for that endpoint — never a hand-built
// JSON fixture.

func TestOpenAPIAudioDocumentIsWellFormed(t *testing.T) {
	c := newOpenAPICompiler(t)
	for _, name := range []string{
		"ConfigAudioSettingsPayload", "AudioSettingsConfigResponse",
		"ConfigAudioNode", "AudioNodeConfigResponse",
	} {
		compileSchema(t, c, name)
	}
}

// TestOpenAPIAudioSettingsConfigResponsesMatchRealResponses covers GET
// (unconfigured and configured), PUT, and revisions for audio.settings.
func TestOpenAPIAudioSettingsConfigResponsesMatchRealResponses(t *testing.T) {
	c := newOpenAPICompiler(t)
	svc, st, _ := newTestIdentityServiceWithStore(t, fixedClock(testNow))
	admin := mustCreatePrincipal(t, svc, "admin-1", identity.RoleAdmin)
	token := mustIssueToken(t, svc, admin.ID)
	api := New(showConfigTestDeps(svc, st), Options{Clock: fixedClock(testNow), Logger: testLogger()})
	authHeader := map[string]string{"Authorization": "Bearer " + token}

	_, unconfiguredBody := doRequest(t, api.Handler, "GET", "/api/v1/config/audio.settings", authHeader)
	assertMatchesSchema(t, c, "AudioSettingsConfigResponse", unconfiguredBody)

	putReq := newJSONRequest(t, http.MethodPut, "/api/v1/config/audio.settings", validAudioSettingsBody, authHeader)
	putResp, putBody := doRawRequest(t, api.Handler, putReq)
	if putResp.StatusCode != http.StatusOK {
		t.Fatalf("PUT: status = %d, want 200; body: %s", putResp.StatusCode, putBody)
	}
	assertMatchesSchema(t, c, "AudioSettingsConfigResponse", putBody)

	_, getBody := doRequest(t, api.Handler, "GET", "/api/v1/config/audio.settings", authHeader)
	assertMatchesSchema(t, c, "AudioSettingsConfigResponse", getBody)

	_, revBody := doRequest(t, api.Handler, "GET", "/api/v1/config/audio.settings/revisions", authHeader)
	assertMatchesSchema(t, c, "ConfigRevisionsResponse", revBody)
}

// TestOpenAPIAudioNodeConfigResponsesMatchRealResponses covers list, GET,
// PUT, and revisions for audio.node, with real advertised evidence so the
// PUT succeeds (a 400 problem body is covered separately).
func TestOpenAPIAudioNodeConfigResponsesMatchRealResponses(t *testing.T) {
	c := newOpenAPICompiler(t)
	svc, st, _ := newTestIdentityServiceWithStore(t, fixedClock(testNow))
	admin := mustCreatePrincipal(t, svc, "admin-1", identity.RoleAdmin)
	token := mustIssueToken(t, svc, admin.ID)
	deps := showConfigTestDeps(svc, st)
	deps.Nodes.(*fakeNodeLister).setViews([]inventory.NodeView{
		nodeViewWithAudioCapabilities("render-01", []string{"hw:0,0"}, []string{"hw:0,0"}),
	})
	api := New(deps, Options{Clock: fixedClock(testNow), Logger: testLogger()})
	authHeader := map[string]string{"Authorization": "Bearer " + token}

	_, listBody := doRequest(t, api.Handler, "GET", "/api/v1/config/audio.node", authHeader)
	assertMatchesSchema(t, c, "ConfigObjectsListResponse", listBody)

	putReq := newJSONRequest(t, http.MethodPut, "/api/v1/config/audio.node/render-01", validAudioNodeBody, authHeader)
	putResp, putBody := doRawRequest(t, api.Handler, putReq)
	if putResp.StatusCode != http.StatusOK {
		t.Fatalf("PUT: status = %d, want 200; body: %s", putResp.StatusCode, putBody)
	}
	assertMatchesSchema(t, c, "AudioNodeConfigResponse", putBody)
	if !containsAll(string(putBody), `"programChannels":[1,2]`) || !containsAll(string(putBody), `"ltcChannel":3`) {
		t.Fatalf("PUT response missing programChannels/ltcChannel; body: %s", putBody)
	}

	_, getBody := doRequest(t, api.Handler, "GET", "/api/v1/config/audio.node/render-01", authHeader)
	assertMatchesSchema(t, c, "AudioNodeConfigResponse", getBody)
	if !containsAll(string(getBody), `"programChannels":[1,2]`) || !containsAll(string(getBody), `"ltcChannel":3`) {
		t.Fatalf("GET response missing programChannels/ltcChannel; body: %s", getBody)
	}

	_, revBody := doRequest(t, api.Handler, "GET", "/api/v1/config/audio.node/render-01/revisions", authHeader)
	assertMatchesSchema(t, c, "ConfigRevisionsResponse", revBody)
}

// TestOpenAPIAudioNodePlacementRefusalMatchesProblemSchema covers the
// placement-refusal 400 body against the shared Problem schema.
func TestOpenAPIAudioNodePlacementRefusalMatchesProblemSchema(t *testing.T) {
	c := newOpenAPICompiler(t)
	svc, st, _ := newTestIdentityServiceWithStore(t, fixedClock(testNow))
	admin := mustCreatePrincipal(t, svc, "admin-1", identity.RoleAdmin)
	token := mustIssueToken(t, svc, admin.ID)
	api := New(showConfigTestDeps(svc, st), Options{Clock: fixedClock(testNow), Logger: testLogger()})
	authHeader := map[string]string{"Authorization": "Bearer " + token}

	putReq := newJSONRequest(t, http.MethodPut, "/api/v1/config/audio.node/render-01", validAudioNodeBody, authHeader)
	putResp, putBody := doRawRequest(t, api.Handler, putReq)
	if putResp.StatusCode != http.StatusBadRequest {
		t.Fatalf("PUT: status = %d, want 400; body: %s", putResp.StatusCode, putBody)
	}
	assertMatchesSchema(t, c, "Problem", putBody)
}

// TestShowConfigProblemTypesAreDeclaredInOpenAPI is the contract half
// TestShowConfigValidationCodesAllMapToDistinctProblemTypes
// (showconfig_test.go) does not cover: that test proves every
// [config.ValidationError.Code] maps to its own distinct URI in Go; this
// one proves every URI showConfigValidationProblemTypes actually produces
// is declared in api/openapi.yaml's own Problem.type enum. Reading the
// enum from the document rather than asserting a fixed string list means
// a URI added to the map without a matching enum entry fails here, and
// a stale enum entry left behind after a code is renamed does not — the
// review finding this closes was exactly the code and the contract
// silently drifting apart, so the test has to read both sides live.
func TestShowConfigProblemTypesAreDeclaredInOpenAPI(t *testing.T) {
	doc := loadOpenAPIDocument(t)
	docMap, ok := doc.(map[string]any)
	if !ok {
		t.Fatalf("openapi document root is %T, want map[string]any", doc)
	}
	schemas := navMap(t, docMap, "components", "schemas")
	problem := navMap(t, schemas, "Problem")
	properties := navMap(t, problem, "properties")
	typeProp := navMap(t, properties, "type")
	enum := stringSlice(t, typeProp["enum"], "components.schemas.Problem.properties.type.enum")

	declared := make(map[string]bool, len(enum))
	for _, uri := range enum {
		declared[uri] = true
	}

	for code, uri := range showConfigValidationProblemTypes {
		if !declared[uri] {
			t.Errorf("showConfigValidationProblemTypes[%q] = %q, not declared in api/openapi.yaml's Problem.type enum", code, uri)
		}
	}
}

// canonicalResourceKinds is [observation.ResourceKind]'s own complete
// vocabulary — the single source of truth
// TestResourceKindAcceptedSetMatchesOpenAPIEnum cross-checks the handler
// and api/openapi.yaml against, so a new resource kind added to one and
// not the other fails a test instead of silently diverging (finding 9).
var canonicalResourceKinds = []observation.ResourceKind{
	observation.ResourceNode, observation.ResourceFPP, observation.ResourceCoordinator,
	observation.ResourceResolume, observation.ResourceSurface, observation.ResourceAudioSession,
}

// TestResourceKindAcceptedSetMatchesOpenAPIEnum proves two things stay in
// lockstep with [canonicalResourceKinds]: parseObservationFilter's real,
// live accept/reject decision (exercised through GET /observations, not
// read off the switch statement's source text), and api/openapi.yaml's
// "resourceKind" query parameter and ResourceRef.kind enums, parsed from
// the document itself.
func TestResourceKindAcceptedSetMatchesOpenAPIEnum(t *testing.T) {
	svc, st, _ := newTestIdentityServiceWithStore(t, fixedClock(testNow))
	api := New(showConfigTestDeps(svc, st), Options{Clock: fixedClock(testNow), Logger: testLogger()})

	for _, kind := range canonicalResourceKinds {
		resp, body := doRequest(t, api.Handler, "GET", "/api/v1/observations?resourceKind="+string(kind), nil)
		if resp.StatusCode != http.StatusOK {
			t.Errorf("GET /observations?resourceKind=%s: status = %d, want 200; body: %s", kind, resp.StatusCode, body)
		}
	}
	if resp, body := doRequest(t, api.Handler, "GET", "/api/v1/observations?resourceKind=bogus", nil); resp.StatusCode != http.StatusBadRequest {
		t.Errorf("GET /observations?resourceKind=bogus: status = %d, want 400; body: %s", resp.StatusCode, body)
	}

	doc := loadOpenAPIDocument(t)
	docMap, ok := doc.(map[string]any)
	if !ok {
		t.Fatalf("openapi document root is %T, want map[string]any", doc)
	}
	assertEnumMatchesCanonical(t, resourceRefKindEnum(t, docMap), "components.schemas.ResourceRef.properties.kind.enum")
	assertEnumMatchesCanonical(t, observationsResourceKindEnum(t, docMap), "paths./observations.get.parameters[resourceKind].schema.enum")
}

func resourceRefKindEnum(t *testing.T, docMap map[string]any) []string {
	t.Helper()
	schemas := navMap(t, docMap, "components", "schemas")
	resourceRef := navMap(t, schemas, "ResourceRef")
	properties := navMap(t, resourceRef, "properties")
	kind := navMap(t, properties, "kind")
	return stringSlice(t, kind["enum"], "components.schemas.ResourceRef.properties.kind.enum")
}

func observationsResourceKindEnum(t *testing.T, docMap map[string]any) []string {
	t.Helper()
	paths := navMap(t, docMap, "paths")
	observations := navMap(t, paths, "/observations")
	get := navMap(t, observations, "get")
	params, ok := get["parameters"].([]any)
	if !ok {
		t.Fatalf("paths./observations.get.parameters is %T, want []any", get["parameters"])
	}
	for _, p := range params {
		pm, ok := p.(map[string]any)
		if !ok {
			continue
		}
		if pm["name"] != "resourceKind" {
			continue
		}
		schema := navMap(t, pm, "schema")
		return stringSlice(t, schema["enum"], "paths./observations.get.parameters[resourceKind].schema.enum")
	}
	t.Fatal("no \"resourceKind\" query parameter found on GET /observations")
	return nil
}

func navMap(t *testing.T, m map[string]any, keys ...string) map[string]any {
	t.Helper()
	cur := m
	for _, k := range keys {
		next, ok := cur[k]
		if !ok {
			t.Fatalf("openapi document: key %q not found while navigating %v", k, keys)
		}
		nextMap, ok := next.(map[string]any)
		if !ok {
			t.Fatalf("openapi document: %q is %T, want map[string]any (navigating %v)", k, next, keys)
		}
		cur = nextMap
	}
	return cur
}

func stringSlice(t *testing.T, v any, path string) []string {
	t.Helper()
	arr, ok := v.([]any)
	if !ok {
		t.Fatalf("openapi document: %s is %T, want []any", path, v)
	}
	out := make([]string, 0, len(arr))
	for _, e := range arr {
		s, ok := e.(string)
		if !ok {
			t.Fatalf("openapi document: %s has a non-string entry %v", path, e)
		}
		out = append(out, s)
	}
	return out
}

func assertEnumMatchesCanonical(t *testing.T, got []string, path string) {
	t.Helper()
	want := make([]string, 0, len(canonicalResourceKinds))
	for _, k := range canonicalResourceKinds {
		want = append(want, string(k))
	}
	sort.Strings(got)
	sort.Strings(want)
	if len(got) != len(want) {
		t.Fatalf("%s = %v, want %v", path, got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("%s = %v, want %v", path, got, want)
			return
		}
	}
}
