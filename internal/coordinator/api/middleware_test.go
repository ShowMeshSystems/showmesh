package api

import (
	"crypto/subtle"
	"net/http"
	"os"
	"strings"
	"testing"
)

func testAPI(t *testing.T, opts Options) *API {
	t.Helper()
	if opts.Clock == nil {
		opts.Clock = fixedClock(testNow)
	}
	if opts.Logger == nil {
		opts.Logger = testLogger()
	}
	deps := Dependencies{
		Nodes: &fakeNodeLister{}, FPP: &fakeFPPLister{}, Observations: &fakeObservationLister{},
		Events: &fakeEventReader{}, Collectors: &fakeCollectorStatusLister{},
	}
	return New(deps, opts)
}

// --- Version negotiation (contract section 6.6; Task D spec section 6) ---

func TestVersionHeaderPresentOnEveryResponse(t *testing.T) {
	api := testAPI(t, Options{})
	for _, target := range []string{"/api/v1/", "/api/v1/nodes/does-not-exist", "/api/v2/nodes"} {
		resp, body := doRequest(t, api.Handler, "GET", target, nil)
		if got := resp.Header.Get(apiVersionHeaderName); got != "1" {
			t.Errorf("%s: %s header = %q, want \"1\"; status %d, body %s", target, apiVersionHeaderName, got, resp.StatusCode, body)
		}
	}
}

func TestVersionNegotiationRequestHeader(t *testing.T) {
	tests := []struct {
		name       string
		header     string // "" means omit the header entirely
		wantStatus int
	}{
		{name: "absent header", header: "", wantStatus: http.StatusOK},
		{name: "matching version", header: "1", wantStatus: http.StatusOK},
		{name: "unsupported version", header: "2", wantStatus: http.StatusBadRequest},
		{name: "garbage", header: "banana", wantStatus: http.StatusBadRequest},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			api := testAPI(t, Options{})
			headers := map[string]string{}
			if tt.header != "" {
				headers[apiVersionHeaderName] = tt.header
			}
			resp, body := doRequest(t, api.Handler, "GET", "/api/v1/", headers)
			if resp.StatusCode != tt.wantStatus {
				t.Fatalf("status = %d, want %d; body: %s", resp.StatusCode, tt.wantStatus, body)
			}
			if tt.wantStatus != http.StatusOK {
				m := decodeMap(t, body)
				if m["type"] != ProblemTypeUnsupportedAPIVersion {
					t.Errorf("type = %v, want %v", m["type"], ProblemTypeUnsupportedAPIVersion)
				}
				if resp.Header.Get(apiVersionHeaderName) != "1" {
					t.Errorf("%s header missing on error response", apiVersionHeaderName)
				}
			}
		})
	}
}

// TestUnknownPathVersionProducesExplicitError is the path-based mirror of
// TestVersionNegotiationRequestHeader: hitting /api/v2/... must produce
// the same explicit, machine-readable problem, never a bare 404 page —
// Task D spec section 6 and OPERATOR-UI section 5.1.
func TestUnknownPathVersionProducesExplicitError(t *testing.T) {
	api := testAPI(t, Options{})
	resp, body := doRequest(t, api.Handler, "GET", "/api/v2/nodes", nil)
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400; body: %s", resp.StatusCode, body)
	}
	if resp.Header.Get("Content-Type") != "application/problem+json" {
		t.Errorf("Content-Type = %q, want application/problem+json", resp.Header.Get("Content-Type"))
	}
	m := decodeMap(t, body)
	if m["type"] != ProblemTypeUnsupportedAPIVersion {
		t.Errorf("type = %v, want %v", m["type"], ProblemTypeUnsupportedAPIVersion)
	}
}

func TestUnknownV1RouteIsResourceNotFound(t *testing.T) {
	api := testAPI(t, Options{})
	resp, body := doRequest(t, api.Handler, "GET", "/api/v1/this-does-not-exist", nil)
	if resp.StatusCode != http.StatusNotFound {
		t.Fatalf("status = %d, want 404; body: %s", resp.StatusCode, body)
	}
	m := decodeMap(t, body)
	if m["type"] != ProblemTypeResourceNotFound {
		t.Errorf("type = %v, want %v", m["type"], ProblemTypeResourceNotFound)
	}
}

// --- Auth (contract section 6.8) ---

func TestAuthDisabledByDefault(t *testing.T) {
	api := testAPI(t, Options{})
	resp, body := doRequest(t, api.Handler, "GET", "/api/v1/", nil)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200 with no token configured; body: %s", resp.StatusCode, body)
	}
}

func TestAuthRejectsMissingToken(t *testing.T) {
	api := testAPI(t, Options{AuthToken: "s3cret"})
	resp, body := doRequest(t, api.Handler, "GET", "/api/v1/", nil)
	if resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("status = %d, want 401; body: %s", resp.StatusCode, body)
	}
	if resp.Header.Get("WWW-Authenticate") != "Bearer" {
		t.Errorf("WWW-Authenticate = %q, want \"Bearer\"", resp.Header.Get("WWW-Authenticate"))
	}
	m := decodeMap(t, body)
	if m["type"] != ProblemTypeUnauthorized {
		t.Errorf("type = %v, want %v", m["type"], ProblemTypeUnauthorized)
	}
	if strings.Contains(string(body), "s3cret") {
		t.Fatalf("problem body leaks the configured token: %s", body)
	}
}

func TestAuthRejectsWrongToken(t *testing.T) {
	api := testAPI(t, Options{AuthToken: "s3cret"})
	resp, _ := doRequest(t, api.Handler, "GET", "/api/v1/", map[string]string{
		"Authorization": "Bearer wrong-token",
	})
	if resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("status = %d, want 401", resp.StatusCode)
	}
}

func TestAuthAcceptsCorrectToken(t *testing.T) {
	api := testAPI(t, Options{AuthToken: "s3cret"})
	resp, body := doRequest(t, api.Handler, "GET", "/api/v1/", map[string]string{
		"Authorization": "Bearer s3cret",
	})
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200; body: %s", resp.StatusCode, body)
	}
}

func TestAuthEnforcedOnStreamEndpoint(t *testing.T) {
	api := testAPI(t, Options{AuthToken: "s3cret"})
	resp, body := doRequest(t, api.Handler, "GET", "/api/v1/stream", nil)
	if resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("status = %d, want 401 on /api/v1/stream with no token; body: %s", resp.StatusCode, body)
	}
}

// TestAuthUsesConstantTimeCompare is the structural check Task D spec
// section 7 asks for in preference to a flaky timing test ("assert subtle
// is used, or test that response timing does not correlate with prefix
// length if you can do it without flakiness; prefer the structural
// check"). It does two things: proves crypto/subtle.ConstantTimeCompare
// itself behaves as withAuth depends on (equal-vs-differing byte slices),
// and inspects middleware.go's own source to confirm withAuth's
// implementation actually calls it — a source-level structural fact a
// wall-clock timing measurement could never prove as reliably, and would
// only prove flakily.
func TestAuthUsesConstantTimeCompare(t *testing.T) {
	if subtle.ConstantTimeCompare([]byte("abc"), []byte("abc")) != 1 {
		t.Fatalf("sanity check failed: ConstantTimeCompare(equal) != 1")
	}
	if subtle.ConstantTimeCompare([]byte("abc"), []byte("abz")) != 0 {
		t.Fatalf("sanity check failed: ConstantTimeCompare(differing) != 0")
	}

	src, err := os.ReadFile("middleware.go")
	if err != nil {
		t.Fatalf("reading middleware.go: %v", err)
	}
	if !strings.Contains(string(src), "subtle.ConstantTimeCompare") {
		t.Fatalf("middleware.go no longer calls subtle.ConstantTimeCompare for the bearer token comparison")
	}
}

// --- CORS (contract section 6.8) ---

func TestCORSNoHeadersWhenNoOriginsConfigured(t *testing.T) {
	api := testAPI(t, Options{})
	resp, _ := doRequest(t, api.Handler, "GET", "/api/v1/", map[string]string{"Origin": "https://evil.example.com"})
	if v := resp.Header.Get("Access-Control-Allow-Origin"); v != "" {
		t.Errorf("Access-Control-Allow-Origin = %q, want empty when no origins are configured", v)
	}
}

func TestCORSAllowsConfiguredOriginOnly(t *testing.T) {
	api := testAPI(t, Options{AllowedOrigins: []string{"https://ui.example.com"}})

	resp, _ := doRequest(t, api.Handler, "GET", "/api/v1/", map[string]string{"Origin": "https://ui.example.com"})
	if got := resp.Header.Get("Access-Control-Allow-Origin"); got != "https://ui.example.com" {
		t.Errorf("Access-Control-Allow-Origin = %q, want the exact configured origin echoed back", got)
	}
	if resp.Header.Get("Access-Control-Allow-Origin") == "*" {
		t.Fatalf("Access-Control-Allow-Origin must never be \"*\"")
	}

	resp2, _ := doRequest(t, api.Handler, "GET", "/api/v1/", map[string]string{"Origin": "https://evil.example.com"})
	if got := resp2.Header.Get("Access-Control-Allow-Origin"); got != "" {
		t.Errorf("Access-Control-Allow-Origin = %q, want empty for an origin not in the allowed list", got)
	}
}

func TestCORSPreflightDoesNotRequireAuth(t *testing.T) {
	api := testAPI(t, Options{AuthToken: "s3cret", AllowedOrigins: []string{"https://ui.example.com"}})
	resp, body := doRequest(t, api.Handler, "OPTIONS", "/api/v1/nodes", map[string]string{
		"Origin":                        "https://ui.example.com",
		"Access-Control-Request-Method": "GET",
	})
	if resp.StatusCode != http.StatusNoContent {
		t.Fatalf("status = %d, want 204 for a CORS preflight, even with auth configured; body: %s", resp.StatusCode, body)
	}
}
