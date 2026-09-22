package api

import (
	"net/http"
	"net/http/httptest"
	"testing"
)

// This file is the pairing and brightness-ceiling routes' own
// conformance coverage: every schema they added, validated against a REAL
// response from a real coordinator wiring, never hand-built JSON.

func TestOpenAPIFPPPairingDocumentIsWellFormed(t *testing.T) {
	c := newOpenAPICompiler(t)
	for _, name := range []string{
		"FPPPairingRequest", "FPPPairingResponse", "FPPPairingStateResponse",
		"FPPPairingClaimRequest", "FPPPairingClaimResponse",
		"FPPBrightnessCeilingRequest", "FPPBrightnessCeilingResponse",
	} {
		compileSchema(t, c, name)
	}
}

func TestOpenAPIFPPPairingResponsesMatchRealResponses(t *testing.T) {
	c := newOpenAPICompiler(t)
	api, _, token := pairingAPI(t, fixedClock(testNow))
	secret := pairingSecret(31)
	code := pairingCodeFor(t, secret)

	req := httptest.NewRequest(http.MethodGet, "/api/v1/fpp/bench-fpp/pairing", nil)
	req.Header.Set("Authorization", "Bearer "+token)
	_, none := doRawRequest(t, api.Handler, req)
	assertMatchesSchema(t, c, "FPPPairingStateResponse", none)

	_, started := doRawRequest(t, api.Handler, startPairingRequest(t, "bench-fpp", `{"code":"`+code+`"}`, token))
	assertMatchesSchema(t, c, "FPPPairingResponse", started)

	req = httptest.NewRequest(http.MethodGet, "/api/v1/fpp/bench-fpp/pairing", nil)
	req.Header.Set("Authorization", "Bearer "+token)
	_, waiting := doRawRequest(t, api.Handler, req)
	assertMatchesSchema(t, c, "FPPPairingStateResponse", waiting)

	_, claimed := doRawRequest(t, api.Handler, claimRequest(t, `{"secret":"`+secret+`"}`))
	assertMatchesSchema(t, c, "FPPPairingClaimResponse", claimed)

	_, refused := doRawRequest(t, api.Handler, claimRequest(t, `{"secret":"`+pairingSecret(32)+`"}`))
	assertMatchesSchema(t, c, "Problem", refused)

	_, malformed := doRawRequest(t, api.Handler, startPairingRequest(t, "bench-fpp", `{"code":"nope"}`, token))
	assertMatchesSchema(t, c, "Problem", malformed)
}

func TestOpenAPIFPPBrightnessCeilingResponsesMatchRealResponses(t *testing.T) {
	c := newOpenAPICompiler(t)
	srv, _ := newFakeCeilingFPP(t, nil, true)
	api, _, token := ceilingAPI(t, srv.URL)

	_, written := doRawRequest(t, api.Handler, ceilingRequest(t, "bench-fpp", `{"ceiling":60}`, token))
	assertMatchesSchema(t, c, "FPPBrightnessCeilingResponse", written)

	_, refused := doRawRequest(t, api.Handler, ceilingRequest(t, "bench-fpp", `{"ceiling":101}`, token))
	assertMatchesSchema(t, c, "Problem", refused)

	_, missing := doRawRequest(t, api.Handler, ceilingRequest(t, "no-such-fpp", `{"ceiling":50}`, token))
	assertMatchesSchema(t, c, "Problem", missing)
}

// The three request-body pointers assertMatchesSchema never reads.
func TestOpenAPIFPPPairingRequestBodiesReferenceDocumentedSchemas(t *testing.T) {
	for _, tc := range []struct{ method, path, want string }{
		{"post", "/fpp/{instanceId}/pairing", "FPPPairingRequest"},
		{"post", "/integrations/fpp/pairing/claim", "FPPPairingClaimRequest"},
		{"post", "/fpp/{instanceId}/brightness/ceiling", "FPPBrightnessCeilingRequest"},
	} {
		if got := requestBodySchemaRef(t, tc.method, tc.path); got != tc.want {
			t.Errorf("%s %s requestBody schema = %q, want %q", tc.method, tc.path, got, tc.want)
		}
	}
}
