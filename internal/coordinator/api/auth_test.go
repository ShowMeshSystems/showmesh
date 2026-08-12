package api

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/showmeshsystems/showmesh/internal/coordinator/identity"
	"github.com/showmeshsystems/showmesh/internal/coordinator/store"
)

// This file is shared ADR-024 test scaffolding: a REAL
// internal/coordinator/identity.Service over a real, throwaway SQLite
// store (never a hand-rolled fake identity.Service), matching this
// package's own standing rule (contract section 1, restated in every test
// file here) that a test drives real production code and asserts on what
// it actually did. auth_test.go, session_test.go, audit_test.go, and
// middleware_test.go's CSRF cases all build on these helpers.
//
// A cost note repeated from CLAUDE.md because it shapes how these helpers
// are used, not just how they are built: argon2id at ADR-024 decision 1's
// FIXED parameters (64 MiB, time 2, parallelism 1 — identity/password.go
// exposes no override, and this task does not own that package) costs
// real wall time on every password verification, with no cheap-mode
// escape hatch available from this package. Every test in this package's
// suite therefore prefers [mustIssueToken] (a SHA-256 digest lookup, no
// KDF) over a real password login wherever it only needs SOME
// authenticated principal and is not itself testing the session-cookie or
// CSRF path; [loginAndGetCookie] is reserved for tests that must mint a
// real cookie.

// newTestIdentityService builds a real [identity.Service] over a
// throwaway store sharing clock's notion of "now" — the same pattern
// internal/coordinator/identity's own test suite uses (newTestService in
// identity_test.go), duplicated here in miniature because this package
// cannot import another package's _test.go helpers.
func newTestIdentityService(t *testing.T, now func() time.Time) identity.Service {
	t.Helper()
	dir := t.TempDir()
	st, err := store.Open(context.Background(), filepath.Join(dir, "db"), nil, store.WithClock(now))
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(func() { _ = st.Close() })
	return identity.NewService(st, now, filepath.Join(dir, "data"), identity.WithLogger(testLogger()))
}

// testPassword is fixed and shared across every principal this file's
// helpers create: no test in this package asserts anything about
// password CONTENT, only about what a correct vs. incorrect one does at
// the HTTP layer, so there is no reason for it to vary per principal.
const testPassword = "correct horse battery staple 42"

// mustCreatePrincipal creates a principal directly against svc, bypassing
// the bootstrap flow (no test in this package exercises bootstrap; that
// is internal/coordinator/identity's own suite's job), and returns it.
func mustCreatePrincipal(t *testing.T, svc identity.Service, name string, role identity.Role) identity.Principal {
	t.Helper()
	p, err := svc.CreatePrincipal(context.Background(), name, identity.KindHuman, role, testPassword)
	if err != nil {
		t.Fatalf("create principal %q: %v", name, err)
	}
	return p
}

// mustIssueToken mints a real bearer token for principalID with no argon2
// cost — see this file's doc comment for why this is the preferred way
// for most tests here to reach an authenticated route.
func mustIssueToken(t *testing.T, svc identity.Service, principalID string) string {
	t.Helper()
	tok, err := svc.IssueToken(context.Background(), principalID, "test", nil)
	if err != nil {
		t.Fatalf("issue token for %q: %v", principalID, err)
	}
	return tok.Value
}

// loginAndGetCookie drives the real POST /api/v1/session handler — the
// one path in this package's tests that legitimately pays argon2id's
// cost — and returns the minted session cookie's raw value.
func loginAndGetCookie(t *testing.T, h http.Handler, name, password string) string {
	t.Helper()
	body := `{"name":` + strconv.Quote(name) + `,"password":` + strconv.Quote(password) + `,"deviceLabel":"test device"}`
	req := httptest.NewRequest(http.MethodPost, "/api/v1/session", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	resp := rec.Result()
	if resp.StatusCode != http.StatusOK {
		b, _ := io.ReadAll(resp.Body)
		t.Fatalf("login for %q failed: status %d, body %s", name, resp.StatusCode, b)
	}
	for _, c := range resp.Cookies() {
		if c.Name == sessionCookieName {
			return c.Value
		}
	}
	t.Fatalf("login response for %q set no %s cookie", name, sessionCookieName)
	return ""
}

// fastLoginOptions holds every ADR-024 login-cost knob at values a test
// can afford to wait out for real (this package's testing standard
// otherwise forbids a real sleep in an assertion — these are short enough
// that they are not one): a tiny per-source delay/cap so
// [TestLoginConcurrencyLimitRejectsWithRetryAfter] and any per-source
// delay test complete in milliseconds, not [defaultLoginPerSourceDelay]'s
// production 250ms-per-failure.
func fastLoginOptions(o *Options) {
	o.LoginConcurrency = 1
	o.LoginQueueWait = 20 * time.Millisecond
	o.LoginPerSourceDelay = time.Millisecond
	o.LoginMaxDelay = 5 * time.Millisecond
}

// authTestDeps returns a [Dependencies] with every non-Identity field set
// to an empty fake, matching testAPI/buildTestAPI's own convention
// elsewhere in this package, plus svc wired as Identity.
func authTestDeps(svc identity.Service) Dependencies {
	return Dependencies{
		Nodes: &fakeNodeLister{}, FPP: &fakeFPPLister{}, Observations: &fakeObservationLister{},
		Events: &fakeEventReader{}, Collectors: &fakeCollectorStatusLister{},
		Identity: svc,
	}
}
