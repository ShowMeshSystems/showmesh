package api

import (
	"net/http"
	"testing"
)

// This file is the operator definition-republish route's own conformance
// coverage, following openapi_fpptransitiongain_test.go's pattern: every
// schema this route added is validated against a REAL response from a real
// coordinator wiring, never hand-built JSON.

func TestOpenAPIFPPDefinitionRepublishDocumentIsWellFormed(t *testing.T) {
	c := newOpenAPICompiler(t)
	for _, name := range []string{
		"FPPDefinitionRepublishRequest", "FPPDefinitionRepublishResponse", "FPPDefinitionRepublishResult",
	} {
		compileSchema(t, c, name)
	}
}

func TestOpenAPIFPPDefinitionRepublishResponsesMatchRealResponses(t *testing.T) {
	c := newOpenAPICompiler(t)
	srv, _ := newFakeRepublishPlugin(t, http.StatusOK, appliedRepublishPluginBody)
	api, _, token := definitionRepublishAPI(t, srv.URL)

	_, applied := doRawRequest(t, api.Handler,
		definitionRepublishRequest(t, "bench-fpp", `{"requestId":"req-1"}`, token))
	assertMatchesSchema(t, c, "FPPDefinitionRepublishResponse", applied)

	_, refused := doRawRequest(t, api.Handler,
		definitionRepublishRequest(t, "bench-fpp", `{"requestId":""}`, token))
	assertMatchesSchema(t, c, "Problem", refused)

	_, missing := doRawRequest(t, api.Handler,
		definitionRepublishRequest(t, "no-such-fpp", `{}`, token))
	assertMatchesSchema(t, c, "Problem", missing)

	badSrv, _ := newFakeRepublishPlugin(t, http.StatusInternalServerError, `{"error":"nope"}`)
	badAPI, _, badToken := definitionRepublishAPI(t, badSrv.URL)
	_, upstream := doRawRequest(t, badAPI.Handler,
		definitionRepublishRequest(t, "bench-fpp", `{}`, badToken))
	assertMatchesSchema(t, c, "Problem", upstream)
}

// TestOpenAPIFPPDefinitionRepublishRequestBodyReferencesDocumentedSchema
// resolves the document pointer assertMatchesSchema never reads.
func TestOpenAPIFPPDefinitionRepublishRequestBodyReferencesDocumentedSchema(t *testing.T) {
	if got := requestBodySchemaRef(t, "post", "/fpp/{instanceId}/playlist-definitions/republish"); got != "FPPDefinitionRepublishRequest" {
		t.Errorf("POST /fpp/{instanceId}/playlist-definitions/republish requestBody schema = %q, want FPPDefinitionRepublishRequest", got)
	}
}
