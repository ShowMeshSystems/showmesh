package api

import (
	"net/http"
	"os"
	"strings"
	"testing"

	"github.com/showmeshsystems/showmesh/internal/coordinator/identity"
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

// --- Auth (ADR-024; see auth_test.go for the shared real-identity.Service
// scaffolding, and session_test.go/audit_test.go for the endpoint-level
// coverage of login, sessions, and audit). This section covers what
// middleware.go/auth.go own directly: read closure, and that the retired
// ADR-021 shared-secret mechanism is actually gone. ---

// TestReadsOpenByDefault proves ADR-024 decision 2's carried-forward
// ADR-021 posture: with no [Options.CloseReads] set (the zero value) and
// no credential of any kind presented, a v1 read route still answers 200
// — exactly the property every pre-Step-6 client depends on continuing to
// work.
func TestReadsOpenByDefault(t *testing.T) {
	api := testAPI(t, Options{})
	resp, body := doRequest(t, api.Handler, "GET", "/api/v1/", nil)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200 with reads open and no credential; body: %s", resp.StatusCode, body)
	}
}

// TestCloseReadsRejectsRequestWithNoCredential proves the other half:
// [Options.CloseReads] true closes a real read route (not GET /api/v1/
// or GET /api/v1/session, which stay open regardless — see readGuard's
// doc comment) to a caller presenting nothing.
func TestCloseReadsRejectsRequestWithNoCredential(t *testing.T) {
	api := testAPI(t, Options{CloseReads: true})
	resp, body := doRequest(t, api.Handler, "GET", "/api/v1/nodes", nil)
	if resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("status = %d, want 401; body: %s", resp.StatusCode, body)
	}
	m := decodeMap(t, body)
	if m["type"] != ProblemTypeUnauthorized {
		t.Errorf("type = %v, want %v", m["type"], ProblemTypeUnauthorized)
	}
}

// TestCloseReadsAcceptsValidBearerToken proves a real token (minted
// through internal/coordinator/identity.Service, not a shared secret)
// authenticates a closed-reads request holding the resource's scope.
func TestCloseReadsAcceptsValidBearerToken(t *testing.T) {
	svc := newTestIdentityService(t, fixedClock(testNow))
	p := mustCreatePrincipal(t, svc, "viewer-1", identity.RoleViewer)
	token := mustIssueToken(t, svc, p.ID)

	api := New(authTestDeps(svc), Options{CloseReads: true, Clock: fixedClock(testNow), Logger: testLogger()})
	resp, body := doRequest(t, api.Handler, "GET", "/api/v1/nodes", map[string]string{
		"Authorization": "Bearer " + token,
	})
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200 with a valid token; body: %s", resp.StatusCode, body)
	}
}

// TestCloseReadsRejectsWrongToken proves an invalid bearer value never
// authenticates, unlike ADR-021's constant-string comparison this
// replaces — this is now a real digest lookup against
// internal/coordinator/identity's store, which the wrong value simply
// does not match.
func TestCloseReadsRejectsWrongToken(t *testing.T) {
	svc := newTestIdentityService(t, fixedClock(testNow))
	p := mustCreatePrincipal(t, svc, "viewer-1", identity.RoleViewer)
	_ = mustIssueToken(t, svc, p.ID)

	api := New(authTestDeps(svc), Options{CloseReads: true, Clock: fixedClock(testNow), Logger: testLogger()})
	resp, _ := doRequest(t, api.Handler, "GET", "/api/v1/nodes", map[string]string{
		"Authorization": "Bearer " + identity.TokenPrefix + "wrong-value-entirely",
	})
	if resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("status = %d, want 401", resp.StatusCode)
	}
}

// TestCloseReadsEnforcedOnStreamEndpoint proves GET /api/v1/stream is not
// a hole in read closure — it is gated by readGuardAll, exactly like
// every other read route, per auth.go's readAllScopes.
func TestCloseReadsEnforcedOnStreamEndpoint(t *testing.T) {
	api := testAPI(t, Options{CloseReads: true})
	resp, body := doRequest(t, api.Handler, "GET", "/api/v1/stream", nil)
	if resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("status = %d, want 401 on /api/v1/stream with reads closed and no credential; body: %s", resp.StatusCode, body)
	}
}

// TestCloseReadsRejectsTokenMissingAReadAllScope closes review finding
// 12's "read-API scope authorization has no test at all": nothing in this
// package's suite ever exercised [handlers.readGuardAll]'s per-scope
// denial loop, because every BUILT-IN role that holds any read scope
// holds all four together (see identity.Role.Scopes) — so a mutation
// reducing that loop to `if false {}` (never denying anything) would have
// passed every existing test in this package unmodified. RoleScheduler
// holds NONE of the four ([identity.ScopeShowMacroRun] only), which is
// enough to prove the loop actually runs: a scheduler token must be
// rejected 403 from a readGuardAll route (GET /api/v1/snapshot) with
// reads closed, naming the FIRST scope readAllScopes checks
// (identity.ScopeNodeRead) — under the "if false" mutation this request
// would incorrectly succeed with 200.
func TestCloseReadsRejectsTokenMissingAReadAllScope(t *testing.T) {
	svc := newTestIdentityService(t, fixedClock(testNow))
	p := mustCreatePrincipal(t, svc, "scheduler-1", identity.RoleScheduler)
	token := mustIssueToken(t, svc, p.ID)

	api := New(authTestDeps(svc), Options{CloseReads: true, Clock: fixedClock(testNow), Logger: testLogger()})
	resp, body := doRequest(t, api.Handler, "GET", "/api/v1/snapshot", map[string]string{
		"Authorization": "Bearer " + token,
	})
	if resp.StatusCode != http.StatusForbidden {
		t.Fatalf("status = %d, want 403; body: %s", resp.StatusCode, body)
	}
	m := decodeMap(t, body)
	if m["type"] != ProblemTypeForbidden {
		t.Errorf("type = %v, want %v", m["type"], ProblemTypeForbidden)
	}
	detail, _ := m["detail"].(string)
	if !strings.Contains(detail, string(identity.ScopeNodeRead)) {
		t.Errorf("detail = %q, want it to name %q (the first scope readAllScopes checks)", detail, identity.ScopeNodeRead)
	}
}

// TestGetSessionAlwaysOpenEvenWithClosedReads proves ADR-024 decision 5's
// "being signed out is a persistent, readable state": GET /api/v1/session
// answers 200 (never 401) with no credential, regardless of
// [Options.CloseReads] — a client must be able to learn it is
// unauthenticated without itself needing a credential to ask.
func TestGetSessionAlwaysOpenEvenWithClosedReads(t *testing.T) {
	api := testAPI(t, Options{CloseReads: true})
	resp, body := doRequest(t, api.Handler, "GET", "/api/v1/session", nil)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200; body: %s", resp.StatusCode, body)
	}
	m := decodeMap(t, body)
	if m["authenticated"] != false {
		t.Errorf("authenticated = %v, want false", m["authenticated"])
	}
}

// TestHealthzReadyzVersionOutsideThisPackage documents, rather than
// tests (they are not routes this package registers at all — see doc.go
// and internal/coordinator/httpapi), that /healthz, /readyz, and /version
// are unreachable through api.New's mux under any path this package
// controls: a request for any of them 404s through this package's own
// catch-all exactly like any other unknown path, which is what proves
// this package never re-implements or intercepts them. ADR-021 rule 3,
// carried forward by ADR-024 decision 3, requires they stay OUTSIDE this
// package's middleware entirely, mounted directly on the coordinator's
// HTTP server by a different file this task does not own
// (internal/coordinator/httpapi) — this test exists so a future change
// that accidentally registers one of these three paths INSIDE this
// package's mux is caught immediately, rather than only being noticed
// when it collides with httpapi's own registration at wiring time.
func TestHealthzReadyzVersionOutsideThisPackage(t *testing.T) {
	api := testAPI(t, Options{CloseReads: true})
	for _, path := range []string{"/healthz", "/readyz", "/version"} {
		resp, body := doRequest(t, api.Handler, "GET", path, nil)
		if resp.StatusCode == http.StatusOK {
			t.Errorf("%s: this package's mux answered 200 for a path it must never register — internal/coordinator/httpapi owns these three; body: %s", path, body)
		}
	}
}

// TestSharedSecretMechanismIsRetired is a structural regression guard,
// not a behavioral one: it proves the retired ADR-021 shared-secret
// comparison (an AuthToken field, compared via subtle.ConstantTimeCompare
// against every request) is actually gone from this package's source,
// not merely unreachable. ADR-024 decision 2 requires
// SHOWMESH_API_TOKEN's coordinator-side refusal-to-start check (a
// wiring-layer concern outside this package — see this package's report);
// what this package itself must never do is silently keep the old
// comparison alive as dead code that a future edit could accidentally
// re-wire.
func TestSharedSecretMechanismIsRetired(t *testing.T) {
	for _, name := range []string{"api.go", "middleware.go", "auth.go"} {
		src, err := os.ReadFile(name)
		if err != nil {
			t.Fatalf("reading %s: %v", name, err)
		}
		if strings.Contains(string(src), "AuthToken") {
			t.Fatalf("%s still references AuthToken — ADR-024 decision 2 retires the ADR-021 shared secret entirely", name)
		}
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
	api := testAPI(t, Options{CloseReads: true, AllowedOrigins: []string{"https://ui.example.com"}})
	resp, body := doRequest(t, api.Handler, "OPTIONS", "/api/v1/nodes", map[string]string{
		"Origin":                        "https://ui.example.com",
		"Access-Control-Request-Method": "GET",
	})
	if resp.StatusCode != http.StatusNoContent {
		t.Fatalf("status = %d, want 204 for a CORS preflight, even with reads closed; body: %s", resp.StatusCode, body)
	}
}

// TestCORSAdvertisesWriteMethods proves the fix to BUILD-PLAN Step 6's
// first recorded implementation note: the CORS preflight response must
// advertise POST and DELETE now that this package has write routes, not
// only the pre-ADR-024 "GET, OPTIONS".
func TestCORSAdvertisesWriteMethods(t *testing.T) {
	api := testAPI(t, Options{AllowedOrigins: []string{"https://ui.example.com"}})
	resp, _ := doRequest(t, api.Handler, "OPTIONS", "/api/v1/session", map[string]string{
		"Origin":                        "https://ui.example.com",
		"Access-Control-Request-Method": "POST",
	})
	allow := resp.Header.Get("Access-Control-Allow-Methods")
	for _, method := range []string{"GET", "POST", "DELETE"} {
		if !strings.Contains(allow, method) {
			t.Errorf("Access-Control-Allow-Methods = %q, want it to contain %q", allow, method)
		}
	}
}

// TestCORSPreflightAllowsContentType closes review finding 11's second
// smaller item: the preflight response used to advertise POST and DELETE
// in Access-Control-Allow-Methods (TestCORSAdvertisesWriteMethods above)
// but never named Content-Type in Access-Control-Allow-Headers, even
// though every write this package's own bearer-token exemption advertises
// as cross-origin-reachable (POST/DELETE /api/v1/session) sends
// "Content-Type: application/json" — a header outside CORS's "simple
// request" set. A browser refuses to send that header on the actual
// request unless the preflight named it, so the cross-origin bearer write
// this package's own docs describe as legitimate silently failed at the
// browser's preflight step, before ever reaching this coordinator.
func TestCORSPreflightAllowsContentType(t *testing.T) {
	api := testAPI(t, Options{AllowedOrigins: []string{"https://ui.example.com"}})
	resp, _ := doRequest(t, api.Handler, "OPTIONS", "/api/v1/session", map[string]string{
		"Origin":                        "https://ui.example.com",
		"Access-Control-Request-Method": "POST",
	})
	allow := resp.Header.Get("Access-Control-Allow-Headers")
	if !strings.Contains(allow, "Content-Type") {
		t.Errorf("Access-Control-Allow-Headers = %q, want it to contain %q", allow, "Content-Type")
	}
}

// TestCORSDoesNotExemptCookieWriteFromSameOrigin is ADR-024 decision 3's
// interlock, proven directly: an allow-listed CORS origin on a
// cookie-authenticated write must NOT bypass decision 6's Sec-Fetch-Site
// requirement. This is the mutation-style check for the exact regression
// this decision exists to name — an implementation that special-cased "if
// Origin is in the CORS allow-list, skip the CSRF check" would pass every
// other test in this file while failing only this one.
func TestCORSDoesNotExemptCookieWriteFromSameOrigin(t *testing.T) {
	svc := newTestIdentityService(t, fixedClock(testNow))
	p := mustCreatePrincipal(t, svc, "operator-1", identity.RoleOperator)

	api := New(authTestDeps(svc), Options{
		AllowedOrigins: []string{"https://ui.example.com"},
		Clock:          fixedClock(testNow), Logger: testLogger(),
	})
	cookie := loginAndGetCookie(t, api.Handler, p.Name, testPassword)

	resp, body := doRequest(t, api.Handler, "DELETE", "/api/v1/session", map[string]string{
		"Cookie": sessionCookieName + "=" + cookie,
		"Origin": "https://ui.example.com", // allow-listed, and irrelevant to the check
		// Sec-Fetch-Site deliberately omitted.
	})
	if resp.StatusCode != http.StatusForbidden {
		t.Fatalf("status = %d, want 403 (CSRF rejected) even with an allow-listed Origin; body: %s", resp.StatusCode, body)
	}
	m := decodeMap(t, body)
	if m["type"] != ProblemTypeCSRFRejected {
		t.Errorf("type = %v, want %v", m["type"], ProblemTypeCSRFRejected)
	}
}
