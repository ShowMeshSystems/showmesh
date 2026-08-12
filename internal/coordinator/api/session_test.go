package api

import (
	"context"
	"net/http"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/showmeshsystems/showmesh/internal/coordinator/identity"
)

// --- POST /api/v1/session (login) ---

func TestLoginSuccessSetsCookieAndReturnsPrincipal(t *testing.T) {
	svc := newTestIdentityService(t, fixedClock(testNow))
	p := mustCreatePrincipal(t, svc, "operator-1", identity.RoleOperator)
	api := New(authTestDeps(svc), Options{Clock: fixedClock(testNow), Logger: testLogger()})

	body := `{"name":"operator-1","password":"` + testPassword + `","deviceLabel":"phone"}`
	req := newJSONRequest(t, http.MethodPost, "/api/v1/session", body, nil)
	resp, respBody := doRawRequest(t, api.Handler, req)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200; body: %s", resp.StatusCode, respBody)
	}

	var found bool
	for _, c := range resp.Cookies() {
		if c.Name == sessionCookieName {
			found = true
			if !c.HttpOnly {
				t.Errorf("session cookie is not HttpOnly")
			}
			if c.Value == "" {
				t.Errorf("session cookie value is empty")
			}
		}
	}
	if !found {
		t.Fatalf("no %s cookie set; Set-Cookie headers: %v", sessionCookieName, resp.Header["Set-Cookie"])
	}

	m := decodeMap(t, respBody)
	if m["authenticated"] != true {
		t.Errorf("authenticated = %v, want true", m["authenticated"])
	}
	principal, _ := m["principal"].(map[string]any)
	if principal["id"] != p.ID {
		t.Errorf("principal.id = %v, want %q", principal["id"], p.ID)
	}
	if principal["role"] != "operator" {
		t.Errorf("principal.role = %v, want \"operator\"", principal["role"])
	}
	scopes, _ := m["scopes"].([]any)
	if len(scopes) == 0 {
		t.Errorf("scopes is empty, want the operator role's bundle")
	}
	if m["credentialForm"] != "session" {
		t.Errorf("credentialForm = %v, want \"session\"", m["credentialForm"])
	}
}

func TestLoginWrongPasswordReturns401NoCookie(t *testing.T) {
	svc := newTestIdentityService(t, fixedClock(testNow))
	mustCreatePrincipal(t, svc, "operator-1", identity.RoleOperator)
	api := New(authTestDeps(svc), Options{Clock: fixedClock(testNow), Logger: testLogger()})

	body := `{"name":"operator-1","password":"wrong","deviceLabel":"phone"}`
	req := newJSONRequest(t, http.MethodPost, "/api/v1/session", body, nil)
	resp, respBody := doRawRequest(t, api.Handler, req)
	if resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("status = %d, want 401; body: %s", resp.StatusCode, respBody)
	}
	for _, c := range resp.Cookies() {
		if c.Name == sessionCookieName && c.Value != "" {
			t.Fatalf("a session cookie was set on a failed login: %v", c)
		}
	}
}

func TestLoginUnknownNameReturns401(t *testing.T) {
	svc := newTestIdentityService(t, fixedClock(testNow))
	api := New(authTestDeps(svc), Options{Clock: fixedClock(testNow), Logger: testLogger()})

	body := `{"name":"nobody","password":"whatever","deviceLabel":"phone"}`
	req := newJSONRequest(t, http.MethodPost, "/api/v1/session", body, nil)
	resp, respBody := doRawRequest(t, api.Handler, req)
	if resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("status = %d, want 401; body: %s", resp.StatusCode, respBody)
	}
}

func TestLoginDisabledPrincipalReturns401(t *testing.T) {
	svc := newTestIdentityService(t, fixedClock(testNow))
	p := mustCreatePrincipal(t, svc, "operator-1", identity.RoleOperator)
	if _, err := svc.SetDisabled(context.Background(), p.ID, true); err != nil {
		t.Fatalf("disable principal: %v", err)
	}
	api := New(authTestDeps(svc), Options{Clock: fixedClock(testNow), Logger: testLogger()})

	body := `{"name":"operator-1","password":"` + testPassword + `","deviceLabel":"phone"}`
	req := newJSONRequest(t, http.MethodPost, "/api/v1/session", body, nil)
	resp, respBody := doRawRequest(t, api.Handler, req)
	if resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("status = %d, want 401; body: %s", resp.StatusCode, respBody)
	}
}

func TestLoginMissingFieldsReturns400(t *testing.T) {
	svc := newTestIdentityService(t, fixedClock(testNow))
	api := New(authTestDeps(svc), Options{Clock: fixedClock(testNow), Logger: testLogger()})

	req := newJSONRequest(t, http.MethodPost, "/api/v1/session", `{"name":"","password":""}`, nil)
	resp, body := doRawRequest(t, api.Handler, req)
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400; body: %s", resp.StatusCode, body)
	}
}

// TestLoginConcurrencyLimitRejectsWithRetryAfter proves ADR-024 decision
// 8's real mechanism, not merely tooManyRequestsProblem's shape (which
// openapi_test.go already checks against the schema): a login held past
// its concurrency slot is rejected with 429 and a Retry-After header,
// while the holder itself still succeeds once released.
func TestLoginConcurrencyLimitRejectsWithRetryAfter(t *testing.T) {
	svc := newTestIdentityService(t, fixedClock(testNow))
	mustCreatePrincipal(t, svc, "operator-1", identity.RoleOperator)

	blocking := &blockingAuthenticateService{Service: svc, entered: make(chan struct{}), release: make(chan struct{})}
	api := New(authTestDeps(blocking), Options{
		Clock: fixedClock(testNow), Logger: testLogger(),
		LoginConcurrency: 1, LoginQueueWait: 20 * time.Millisecond,
	})

	firstDone := make(chan *http.Response, 1)
	go func() {
		body := `{"name":"operator-1","password":"` + testPassword + `","deviceLabel":"first"}`
		req := newJSONRequest(t, http.MethodPost, "/api/v1/session", body, nil)
		resp, _ := doRawRequest(t, api.Handler, req)
		firstDone <- resp
	}()

	select {
	case <-blocking.entered:
	case <-time.After(5 * time.Second):
		t.Fatalf("timed out waiting for the first login to occupy the concurrency slot")
	}

	body := `{"name":"operator-1","password":"` + testPassword + `","deviceLabel":"second"}`
	req := newJSONRequest(t, http.MethodPost, "/api/v1/session", body, nil)
	resp, respBody := doRawRequest(t, api.Handler, req)
	if resp.StatusCode != http.StatusTooManyRequests {
		t.Fatalf("second (queued) login status = %d, want 429; body: %s", resp.StatusCode, respBody)
	}
	if resp.Header.Get("Retry-After") == "" {
		t.Errorf("Retry-After header is missing on a 429")
	}
	m := decodeMap(t, respBody)
	if m["type"] != ProblemTypeTooManyRequests {
		t.Errorf("type = %v, want %v", m["type"], ProblemTypeTooManyRequests)
	}

	close(blocking.release)
	select {
	case first := <-firstDone:
		if first.StatusCode != http.StatusOK {
			t.Errorf("first (holding) login status = %d, want 200", first.StatusCode)
		}
	case <-time.After(5 * time.Second):
		t.Fatalf("timed out waiting for the first login to complete after release")
	}
}

// blockingAuthenticateService wraps a real identity.Service and blocks
// its FIRST AuthenticatePassword call until release is closed, signaling
// entered exactly once so a test can deterministically know the call has
// begun (and therefore holds loginLimiter's one concurrency slot) before
// issuing a second, concurrent request — this is not a race against
// wall-clock time; the second request's rejection depends only on the
// first genuinely still holding the slot, which this synchronization
// guarantees rather than hopes for.
type blockingAuthenticateService struct {
	identity.Service
	entered chan struct{}
	release chan struct{}
}

func (b *blockingAuthenticateService) AuthenticatePassword(ctx context.Context, name, password string) (identity.Principal, error) {
	close(b.entered)
	<-b.release
	return b.Service.AuthenticatePassword(ctx, name, password)
}

// --- GET /api/v1/session ---

func TestGetSessionUnauthenticatedReturnsAuthenticatedFalse(t *testing.T) {
	api := testAPI(t, Options{})
	resp, body := doRequest(t, api.Handler, "GET", "/api/v1/session", nil)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200; body: %s", resp.StatusCode, body)
	}
	m := decodeMap(t, body)
	if m["authenticated"] != false {
		t.Errorf("authenticated = %v, want false", m["authenticated"])
	}
	if m["principal"] != nil {
		t.Errorf("principal = %v, want null", m["principal"])
	}
	if m["scopesState"] != "not_applicable" {
		t.Errorf("scopesState = %v, want \"not_applicable\"", m["scopesState"])
	}
	scopes, ok := m["scopes"].([]any)
	if !ok || len(scopes) != 0 {
		t.Errorf("scopes = %v, want an empty array (never null)", m["scopes"])
	}
}

func TestGetSessionWithCookieReturnsPrincipalRoleAndScopes(t *testing.T) {
	svc := newTestIdentityService(t, fixedClock(testNow))
	p := mustCreatePrincipal(t, svc, "admin-1", identity.RoleAdmin)
	api := New(authTestDeps(svc), Options{Clock: fixedClock(testNow), Logger: testLogger()})
	cookie := loginAndGetCookie(t, api.Handler, p.Name, testPassword)

	resp, body := doRequest(t, api.Handler, "GET", "/api/v1/session", map[string]string{
		"Cookie": sessionCookieName + "=" + cookie,
	})
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200; body: %s", resp.StatusCode, body)
	}
	m := decodeMap(t, body)
	if m["authenticated"] != true {
		t.Fatalf("authenticated = %v, want true; body: %s", m["authenticated"], body)
	}
	principal, _ := m["principal"].(map[string]any)
	if principal["role"] != "admin" {
		t.Errorf("principal.role = %v, want \"admin\"", principal["role"])
	}
	scopes, _ := m["scopes"].([]any)
	wantScopeCount := len(identity.RoleAdmin.Scopes())
	if len(scopes) != wantScopeCount {
		t.Errorf("len(scopes) = %d, want %d (every scope role admin holds)", len(scopes), wantScopeCount)
	}
	if m["scopesState"] != "current" {
		t.Errorf("scopesState = %v, want \"current\"", m["scopesState"])
	}
}

// TestGetSessionCookieSlidesLastUsedAt proves ADR-024 decision 5's
// "sliding happens on ANY cookie-bearing request including a read": a
// GET (not a write) with the session cookie must advance the session's
// LastUsedAt, proven by driving [identity.Service.AuthenticateSession]
// directly against the persisted store state right after the request, at
// a moment far enough in the future that idle expiry would otherwise have
// been reached had sliding not occurred.
func TestGetSessionCookieSlidesLastUsedAt(t *testing.T) {
	clockTime := testNow
	clock := func() time.Time { return clockTime }
	svc := newTestIdentityService(t, clock)
	p := mustCreatePrincipal(t, svc, "operator-1", identity.RoleOperator)
	api := New(authTestDeps(svc), Options{Clock: clock, Logger: testLogger()})
	cookie := loginAndGetCookie(t, api.Handler, p.Name, testPassword)

	// Advance past most of the idle window, then read GET /api/v1/session
	// with the cookie — this is the "read", not a write.
	clockTime = clockTime.Add(identity.SessionMaxIdle - time.Hour)
	resp, body := doRequest(t, api.Handler, "GET", "/api/v1/session", map[string]string{
		"Cookie": sessionCookieName + "=" + cookie,
	})
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200; body: %s", resp.StatusCode, body)
	}

	// Advance PAST the idle window measured from session creation, but
	// well within it measured from the slide above. If the read did not
	// slide LastUsedAt, this credential is now idle-expired; if it did,
	// it is still valid.
	clockTime = clockTime.Add(identity.SessionMaxIdle - time.Hour)
	if _, err := svc.AuthenticateSession(context.Background(), cookie, clockTime); err != nil {
		t.Fatalf("session no longer authenticates after a read within the slid window: %v — GET /api/v1/session did not slide LastUsedAt", err)
	}
}

// --- DELETE /api/v1/session ---

// TestDeleteSessionRequiresSecFetchSiteForCookie is ADR-024 decision 6's
// core CSRF rule, and BUILD-PLAN Step 6's own acceptance criterion: "A
// cookie-authenticated write is rejected when Sec-Fetch-Site is absent."
func TestDeleteSessionRequiresSecFetchSiteForCookie(t *testing.T) {
	svc := newTestIdentityService(t, fixedClock(testNow))
	p := mustCreatePrincipal(t, svc, "operator-1", identity.RoleOperator)
	api := New(authTestDeps(svc), Options{Clock: fixedClock(testNow), Logger: testLogger()})
	cookie := loginAndGetCookie(t, api.Handler, p.Name, testPassword)

	resp, body := doRequest(t, api.Handler, "DELETE", "/api/v1/session", map[string]string{
		"Cookie": sessionCookieName + "=" + cookie,
		// Sec-Fetch-Site deliberately absent.
	})
	if resp.StatusCode != http.StatusForbidden {
		t.Fatalf("status = %d, want 403; body: %s", resp.StatusCode, body)
	}
	m := decodeMap(t, body)
	if m["type"] != ProblemTypeCSRFRejected {
		t.Errorf("type = %v, want %v", m["type"], ProblemTypeCSRFRejected)
	}
}

// TestDeleteSessionSucceedsWithSameOriginAndRevokesSession is the other
// half of the same acceptance criterion — "a bearer-authenticated write
// from curl succeeds with no such header" is covered by
// [TestDeleteSessionBearerWithOwnedSessionIdSucceeds]; this is the
// cookie-authenticated success path, and proves the session is genuinely
// revoked afterward (not merely that the endpoint answered 204).
func TestDeleteSessionSucceedsWithSameOriginAndRevokesSession(t *testing.T) {
	svc := newTestIdentityService(t, fixedClock(testNow))
	p := mustCreatePrincipal(t, svc, "operator-1", identity.RoleOperator)
	api := New(authTestDeps(svc), Options{Clock: fixedClock(testNow), Logger: testLogger()})
	cookie := loginAndGetCookie(t, api.Handler, p.Name, testPassword)

	resp, body := doRequest(t, api.Handler, "DELETE", "/api/v1/session", map[string]string{
		"Cookie":         sessionCookieName + "=" + cookie,
		"Sec-Fetch-Site": "same-origin",
	})
	if resp.StatusCode != http.StatusNoContent {
		t.Fatalf("status = %d, want 204; body: %s", resp.StatusCode, body)
	}

	if _, err := svc.AuthenticateSession(context.Background(), cookie, testNow); err == nil {
		t.Fatalf("session still authenticates after DELETE /api/v1/session revoked it")
	}
}

func TestDeleteSessionBearerWithNoBodyReturns400(t *testing.T) {
	svc := newTestIdentityService(t, fixedClock(testNow))
	p := mustCreatePrincipal(t, svc, "operator-1", identity.RoleOperator)
	token := mustIssueToken(t, svc, p.ID)
	api := New(authTestDeps(svc), Options{Clock: fixedClock(testNow), Logger: testLogger()})

	resp, body := doRequest(t, api.Handler, "DELETE", "/api/v1/session", map[string]string{
		"Authorization": "Bearer " + token,
	})
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400; body: %s", resp.StatusCode, body)
	}
}

// TestDeleteSessionBearerWithOwnedSessionIdSucceeds is this step's one
// genuine bearer-authenticated write: BUILD-PLAN Step 6's acceptance
// criterion ("a bearer-authenticated write from curl succeeds with no
// [Sec-Fetch-Site] header") has no other endpoint to prove it against, so
// this is the load-bearing test for that criterion.
func TestDeleteSessionBearerWithOwnedSessionIdSucceeds(t *testing.T) {
	svc := newTestIdentityService(t, fixedClock(testNow))
	p := mustCreatePrincipal(t, svc, "operator-1", identity.RoleOperator)
	api := New(authTestDeps(svc), Options{Clock: fixedClock(testNow), Logger: testLogger()})
	cookie := loginAndGetCookie(t, api.Handler, p.Name, testPassword) // the session to be revoked, by ID
	token := mustIssueToken(t, svc, p.ID)                             // the bearer credential authenticating the DELETE

	sessions, err := svc.ListSessions(context.Background(), p.ID)
	if err != nil || len(sessions) != 1 {
		t.Fatalf("list sessions: %v (n=%d)", err, len(sessions))
	}

	body := `{"sessionId":` + strconv.Quote(sessions[0].ID) + `}`
	req := newJSONRequest(t, http.MethodDelete, "/api/v1/session", body, map[string]string{
		"Authorization": "Bearer " + token,
		// Deliberately NO Sec-Fetch-Site header: a bearer write is exempt.
	})
	resp, respBody := doRawRequest(t, api.Handler, req)
	if resp.StatusCode != http.StatusNoContent {
		t.Fatalf("status = %d, want 204; body: %s", resp.StatusCode, respBody)
	}

	if _, err := svc.AuthenticateSession(context.Background(), cookie, testNow); err == nil {
		t.Fatalf("session still authenticates after being revoked by id via a bearer-authenticated DELETE")
	}
}

func TestDeleteSessionRejectsSessionIdNotOwnedByPrincipal(t *testing.T) {
	svc := newTestIdentityService(t, fixedClock(testNow))
	p1 := mustCreatePrincipal(t, svc, "operator-1", identity.RoleOperator)
	p2 := mustCreatePrincipal(t, svc, "operator-2", identity.RoleOperator)
	api := New(authTestDeps(svc), Options{Clock: fixedClock(testNow), Logger: testLogger()})

	p2Cookie := loginAndGetCookie(t, api.Handler, p2.Name, testPassword)
	otherSessions, err := svc.ListSessions(context.Background(), p2.ID)
	if err != nil || len(otherSessions) != 1 {
		t.Fatalf("list sessions for p2: %v (n=%d)", err, len(otherSessions))
	}

	token := mustIssueToken(t, svc, p1.ID)
	body := `{"sessionId":` + strconv.Quote(otherSessions[0].ID) + `}`
	req := newJSONRequest(t, http.MethodDelete, "/api/v1/session", body, map[string]string{
		"Authorization": "Bearer " + token,
	})
	resp, respBody := doRawRequest(t, api.Handler, req)
	if resp.StatusCode != http.StatusNotFound {
		t.Fatalf("status = %d, want 404 (a principal must not revoke another principal's session); body: %s", resp.StatusCode, respBody)
	}

	// The other principal's session must still be intact — the rejected
	// attempt must not have revoked it anyway.
	if _, err := svc.AuthenticateSession(context.Background(), p2Cookie, testNow); err != nil {
		t.Fatalf("p2's session no longer authenticates after p1's rejected cross-principal revoke attempt: %v", err)
	}
}

func TestDeleteSessionRequiresAuthentication(t *testing.T) {
	api := testAPI(t, Options{})
	resp, body := doRequest(t, api.Handler, "DELETE", "/api/v1/session", nil)
	if resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("status = %d, want 401; body: %s", resp.StatusCode, body)
	}
}

// --- ADR-024 decision 6: the bearer exemption is keyed on the credential
// that ACTUALLY authenticated, never on header presence ---

// TestMalformedAuthorizationHeaderNeverFallsThroughToCookie is decision
// 6's most dangerous failure mode, proven directly: a request carrying
// BOTH a non-Bearer Authorization header (the URL-userinfo shape a
// browser attaches automatically to a top-level navigation) AND a valid
// session cookie AND a same-origin Sec-Fetch-Site header (everything an
// implementation that "falls through to cookie auth on a bad
// Authorization header" would need to let this write silently succeed)
// must still answer 401, never 204. An implementation reading "if an
// Authorization header is present, skip the CSRF check" — or one that
// merely ignores a bad Authorization header and looks at the cookie
// anyway — fails this test.
func TestMalformedAuthorizationHeaderNeverFallsThroughToCookie(t *testing.T) {
	svc := newTestIdentityService(t, fixedClock(testNow))
	p := mustCreatePrincipal(t, svc, "operator-1", identity.RoleOperator)
	api := New(authTestDeps(svc), Options{Clock: fixedClock(testNow), Logger: testLogger()})
	cookie := loginAndGetCookie(t, api.Handler, p.Name, testPassword)

	resp, body := doRequest(t, api.Handler, "DELETE", "/api/v1/session", map[string]string{
		"Cookie":         sessionCookieName + "=" + cookie,
		"Authorization":  "Basic dXNlcjpwYXNz", // non-Bearer; the URL-userinfo shape
		"Sec-Fetch-Site": "same-origin",
	})
	if resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("status = %d, want 401 (must never fall through to the cookie behind a malformed Authorization header); body: %s", resp.StatusCode, body)
	}

	// The session must still be intact — a 401 that nonetheless revoked
	// it would be its own defect.
	if _, err := svc.AuthenticateSession(context.Background(), cookie, testNow); err != nil {
		t.Fatalf("session no longer authenticates after a request that should have been rejected outright: %v", err)
	}
}

// TestEmptyBearerTokenNeverFallsThroughToCookie is the same property
// against the simplest malformed shape: "Authorization: Bearer" with no
// token at all.
func TestEmptyBearerTokenNeverFallsThroughToCookie(t *testing.T) {
	svc := newTestIdentityService(t, fixedClock(testNow))
	p := mustCreatePrincipal(t, svc, "operator-1", identity.RoleOperator)
	api := New(authTestDeps(svc), Options{Clock: fixedClock(testNow), Logger: testLogger()})
	cookie := loginAndGetCookie(t, api.Handler, p.Name, testPassword)

	resp, body := doRequest(t, api.Handler, "DELETE", "/api/v1/session", map[string]string{
		"Cookie":         sessionCookieName + "=" + cookie,
		"Authorization":  "Bearer",
		"Sec-Fetch-Site": "same-origin",
	})
	if resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("status = %d, want 401; body: %s", resp.StatusCode, body)
	}
}

// --- ADR-024 decision 1: no credential in the URL ---

func TestQueryStringWithCredentialPrefixRejectedAndAudited(t *testing.T) {
	svc := newTestIdentityService(t, fixedClock(testNow))
	api := New(authTestDeps(svc), Options{Clock: fixedClock(testNow), Logger: testLogger()})

	resp, body := doRequest(t, api.Handler, "GET", "/api/v1/nodes?token="+identity.TokenPrefix+"leaked-secret-value", nil)
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400; body: %s", resp.StatusCode, body)
	}
	m := decodeMap(t, body)
	if m["type"] != ProblemTypeCredentialInURL {
		t.Errorf("type = %v, want %v", m["type"], ProblemTypeCredentialInURL)
	}
	if strings.Contains(string(body), "leaked-secret-value") {
		t.Fatalf("problem body echoes the query string's credential value: %s", body)
	}

	entries, err := svc.ListAudit(context.Background(), 0, 10)
	if err != nil {
		t.Fatalf("list audit: %v", err)
	}
	var found bool
	for _, e := range entries {
		if e.Kind == identity.AuditAuthFail && e.Action == "credential_in_url" {
			found = true
			if strings.Contains(e.Target, "leaked-secret-value") {
				t.Errorf("audit entry target contains the credential value: %q", e.Target)
			}
		}
	}
	if !found {
		t.Fatalf("no credential_in_url audit entry was recorded; entries: %+v", entries)
	}
}

func TestQueryStringWithCredentialPrefixNeverLogged(t *testing.T) {
	svc := newTestIdentityService(t, fixedClock(testNow))
	var logBuf strings.Builder
	logger := slogTestLogger(&logBuf)
	api := New(authTestDeps(svc), Options{Clock: fixedClock(testNow), Logger: logger})

	doRequest(t, api.Handler, "GET", "/api/v1/nodes?token="+identity.TokenPrefix+"leaked-secret-value", nil)

	if strings.Contains(logBuf.String(), "leaked-secret-value") {
		t.Fatalf("log output contains the credential value:\n%s", logBuf.String())
	}
}
