package api

import (
	"net/http"
	"testing"
)

// This file is the operator transition-gain route's own conformance
// coverage, following openapi_fallbackprograms_test.go's pattern: every
// schema this route added is validated against a REAL response from a
// real coordinator wiring, never hand-built JSON.

func TestOpenAPIFPPTransitionGainDocumentIsWellFormed(t *testing.T) {
	c := newOpenAPICompiler(t)
	for _, name := range []string{
		"FPPTransitionGainRequest", "FPPTransitionGainResponse", "FPPTransitionGainResult",
	} {
		compileSchema(t, c, name)
	}
}

func TestOpenAPIFPPTransitionGainResponsesMatchRealResponses(t *testing.T) {
	c := newOpenAPICompiler(t)
	srv, _ := newFakeTransitionGainPlugin(t, http.StatusOK, appliedGainBody)
	api, _, token := transitionGainAPI(t, srv.URL)

	_, applied := doRawRequest(t, api.Handler,
		transitionGainRequest(t, "bench-fpp", `{"targetPercent":75,"fadeSeconds":30,"requestId":"req-1"}`, token))
	assertMatchesSchema(t, c, "FPPTransitionGainResponse", applied)

	_, refused := doRawRequest(t, api.Handler,
		transitionGainRequest(t, "bench-fpp", `{"targetPercent":101,"fadeSeconds":0}`, token))
	assertMatchesSchema(t, c, "Problem", refused)

	_, missing := doRawRequest(t, api.Handler,
		transitionGainRequest(t, "no-such-fpp", `{"targetPercent":50,"fadeSeconds":0}`, token))
	assertMatchesSchema(t, c, "Problem", missing)

	badSrv, _ := newFakeTransitionGainPlugin(t, http.StatusInternalServerError, `{"error":"nope"}`)
	badAPI, _, badToken := transitionGainAPI(t, badSrv.URL)
	_, upstream := doRawRequest(t, badAPI.Handler,
		transitionGainRequest(t, "bench-fpp", `{"targetPercent":50,"fadeSeconds":0}`, badToken))
	assertMatchesSchema(t, c, "Problem", upstream)
}

// TestOpenAPIFPPTransitionGainRequestBodyReferencesDocumentedSchema
// resolves the document pointer assertMatchesSchema never reads.
func TestOpenAPIFPPTransitionGainRequestBodyReferencesDocumentedSchema(t *testing.T) {
	if got := requestBodySchemaRef(t, "post", "/fpp/{instanceId}/brightness/transition-gain"); got != "FPPTransitionGainRequest" {
		t.Errorf("POST /fpp/{instanceId}/brightness/transition-gain requestBody schema = %q, want FPPTransitionGainRequest", got)
	}
}
