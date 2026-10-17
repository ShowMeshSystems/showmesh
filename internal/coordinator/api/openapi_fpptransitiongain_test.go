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
	badSrv, _ := newFakeTransitionGainPlugin(t, http.StatusInternalServerError, `{"error":"nope"}`)
	api, _, token := transitionGainAPI(t, srv.URL, badSrv.URL)

	_, mixed := doRawRequest(t, api.Handler,
		transitionGainRequest(t, `{"targetPercent":75,"fadeSeconds":30,"requestId":"req-1"}`, token))
	assertMatchesSchema(t, c, "FPPTransitionGainResponse", mixed)

	_, refused := doRawRequest(t, api.Handler, transitionGainRequest(t, `{"targetPercent":101,"fadeSeconds":0}`, token))
	assertMatchesSchema(t, c, "Problem", refused)

	emptyAPI, _, emptyToken := transitionGainAPI(t)
	_, none := doRawRequest(t, emptyAPI.Handler, transitionGainRequest(t, `{"targetPercent":50,"fadeSeconds":0}`, emptyToken))
	assertMatchesSchema(t, c, "Problem", none)
}

// TestOpenAPIFPPTransitionGainRequestBodyReferencesDocumentedSchema
// resolves the document pointer assertMatchesSchema never reads.
func TestOpenAPIFPPTransitionGainRequestBodyReferencesDocumentedSchema(t *testing.T) {
	if got := requestBodySchemaRef(t, "post", "/fpp/brightness/transition-gain"); got != "FPPTransitionGainRequest" {
		t.Errorf("POST /fpp/brightness/transition-gain requestBody schema = %q, want FPPTransitionGainRequest", got)
	}
}
