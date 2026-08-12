package api

import (
	"context"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/showmeshsystems/showmesh/internal/coordinator/identity"
	"github.com/showmeshsystems/showmesh/internal/coordinator/store"
)

// This file tests POST /api/v1/bootstrap (bootstrap.go) and
// SessionResponse.bootstrapRequired (session.go), the two pieces that
// close what the Step 6 task spec calls "a hole in the middle of the
// feature": identity.Service.ClaimBootstrap and HasAnyPrincipal already
// exist and are fully tested at the identity package's own level; nothing
// here re-tests THEIR behavior, only that this package's HTTP handler
// calls them correctly, enforces the same login-cost bound POST /session
// does, and never leaks the bootstrap code.

// newTestIdentityServiceWithDataDir mirrors auth_test.go's
// newTestIdentityService exactly, except it also returns dataDir — needed
// here (and nowhere else in this package's existing suite) to read the
// real bootstrap code off disk the way an operator would, per
// internal/coordinator/identity's own readBootstrapCode test helper. Not
// added to auth_test.go itself: every other test in this package's suite
// creates its principal directly (mustCreatePrincipal), bypassing
// bootstrap entirely, so no other caller needs dataDir back.
func newTestIdentityServiceWithDataDir(t *testing.T, now func() time.Time) (identity.Service, string) {
	t.Helper()
	dir := t.TempDir()
	st, err := store.Open(context.Background(), filepath.Join(dir, "db"), nil, store.WithClock(now))
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(func() { _ = st.Close() })
	dataDir := filepath.Join(dir, "data")
	return identity.NewService(st, now, dataDir, identity.WithLogger(testLogger())), dataDir
}

// readBootstrapCode reads the real bootstrap code off disk — the exact
// artifact ADR-024 decision 9 says an operator (or, here, a UI's
// POST /api/v1/bootstrap caller) reads to claim it. Mirrors
// internal/coordinator/identity's own private test helper of the same
// name and purpose; duplicated here for the identical reason
// newTestIdentityServiceWithDataDir is: this package cannot import
// another package's _test.go helpers.
func readBootstrapCode(t *testing.T, dataDir string) string {
	t.Helper()
	content, err := os.ReadFile(filepath.Join(dataDir, identity.BootstrapFileName))
	if err != nil {
		t.Fatalf("read bootstrap file: %v", err)
	}
	return strings.TrimSpace(string(content))
}

// mustEnsureBootstrap calls HasAnyPrincipal once, discarding the boolean,
// purely for its documented side effect of generating a bootstrap
// code/file when none exists yet — identical to every ClaimBootstrap test
// in internal/coordinator/identity's own suite calling HasAnyPrincipal
// before reading the code off disk.
func mustEnsureBootstrap(t *testing.T, svc identity.Service) {
	t.Helper()
	if _, err := svc.HasAnyPrincipal(context.Background()); err != nil {
		t.Fatalf("ensure bootstrap: %v", err)
	}
}

func TestClaimBootstrapSuccessCreatesAdminAndSetsCookie(t *testing.T) {
	svc, dataDir := newTestIdentityServiceWithDataDir(t, fixedClock(testNow))
	mustEnsureBootstrap(t, svc)
	code := readBootstrapCode(t, dataDir)
	api := New(authTestDeps(svc), Options{Clock: fixedClock(testNow), Logger: testLogger()})

	body := `{"code":` + strconv.Quote(code) + `,"name":"first-admin","password":"a-strong-password-1","deviceLabel":"laptop"}`
	req := newJSONRequest(t, http.MethodPost, "/api/v1/bootstrap", body, nil)
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
		t.Fatalf("no %s cookie set on a successful bootstrap claim", sessionCookieName)
	}

	m := decodeMap(t, respBody)
	if m["authenticated"] != true {
		t.Errorf("authenticated = %v, want true", m["authenticated"])
	}
	principal, _ := m["principal"].(map[string]any)
	if principal["name"] != "first-admin" {
		t.Errorf("principal.name = %v, want \"first-admin\"", principal["name"])
	}
	if principal["role"] != "admin" {
		t.Errorf("principal.role = %v, want \"admin\" (ADR-024 decision 9: bootstrap always creates an admin)", principal["role"])
	}
	if principal["kind"] != "human" {
		t.Errorf("principal.kind = %v, want \"human\"", principal["kind"])
	}
	if m["bootstrapRequired"] != false {
		t.Errorf("bootstrapRequired = %v, want false immediately after a successful claim", m["bootstrapRequired"])
	}

	// The bootstrap file itself must be gone (identity package's own
	// guarantee); asserting it here too proves this HANDLER actually
	// called ClaimBootstrap and not some other path that only looks
	// similar.
	if _, err := os.Stat(filepath.Join(dataDir, identity.BootstrapFileName)); !os.IsNotExist(err) {
		t.Errorf("bootstrap file still exists after a successful claim via the HTTP endpoint: err = %v", err)
	}
}

// TestGetSessionBootstrapRequiredTrueThenFalse is the other end-to-end
// half of ADR-024 decision 9's "loud... signal": GET /api/v1/session with
// NO credential at all must report bootstrapRequired: true while zero
// principals exist, and false the moment one does — proving
// [v1.SessionResponse.BootstrapRequired] is wired to a real, current
// identity.Service read rather than a value that only ever reads false
// (which every OTHER test in this package's suite, all of which create a
// principal before touching GET /session, would never catch).
func TestGetSessionBootstrapRequiredTrueThenFalse(t *testing.T) {
	svc, dataDir := newTestIdentityServiceWithDataDir(t, fixedClock(testNow))
	api := New(authTestDeps(svc), Options{Clock: fixedClock(testNow), Logger: testLogger()})

	resp, body := doRequest(t, api.Handler, "GET", "/api/v1/session", nil)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200; body: %s", resp.StatusCode, body)
	}
	m := decodeMap(t, body)
	if m["bootstrapRequired"] != true {
		t.Fatalf("bootstrapRequired = %v, want true with zero principals; body: %s", m["bootstrapRequired"], body)
	}

	code := readBootstrapCode(t, dataDir)
	claimBody := `{"code":` + strconv.Quote(code) + `,"name":"first-admin","password":"a-strong-password-1","deviceLabel":"laptop"}`
	claimReq := newJSONRequest(t, http.MethodPost, "/api/v1/bootstrap", claimBody, nil)
	claimResp, claimRespBody := doRawRequest(t, api.Handler, claimReq)
	if claimResp.StatusCode != http.StatusOK {
		t.Fatalf("bootstrap claim status = %d, want 200; body: %s", claimResp.StatusCode, claimRespBody)
	}

	resp2, body2 := doRequest(t, api.Handler, "GET", "/api/v1/session", nil)
	if resp2.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200; body: %s", resp2.StatusCode, body2)
	}
	m2 := decodeMap(t, body2)
	if m2["bootstrapRequired"] != false {
		t.Errorf("bootstrapRequired = %v, want false once a principal exists; body: %s", m2["bootstrapRequired"], body2)
	}
}

func TestClaimBootstrapWrongCodeReturns401AndDoesNotCreateAPrincipal(t *testing.T) {
	svc, _ := newTestIdentityServiceWithDataDir(t, fixedClock(testNow))
	mustEnsureBootstrap(t, svc)
	api := New(authTestDeps(svc), Options{Clock: fixedClock(testNow), Logger: testLogger()})

	body := `{"code":"definitely-the-wrong-code","name":"attacker","password":"whatever12345","deviceLabel":"phone"}`
	req := newJSONRequest(t, http.MethodPost, "/api/v1/bootstrap", body, nil)
	resp, respBody := doRawRequest(t, api.Handler, req)
	if resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("status = %d, want 401; body: %s", resp.StatusCode, respBody)
	}
	for _, c := range resp.Cookies() {
		if c.Name == sessionCookieName && c.Value != "" {
			t.Fatalf("a session cookie was set on a failed bootstrap claim: %v", c)
		}
	}

	principals, err := svc.ListPrincipals(context.Background())
	if err != nil {
		t.Fatalf("list principals: %v", err)
	}
	if len(principals) != 0 {
		t.Fatalf("principals = %+v, want none created after a failed bootstrap claim", principals)
	}
}

// TestClaimBootstrapAlreadyClaimedReturns401 proves the code is genuinely
// single-use through the HTTP endpoint, not merely at the identity
// package's own level (already proven there).
func TestClaimBootstrapAlreadyClaimedReturns401(t *testing.T) {
	svc, dataDir := newTestIdentityServiceWithDataDir(t, fixedClock(testNow))
	mustEnsureBootstrap(t, svc)
	code := readBootstrapCode(t, dataDir)
	api := New(authTestDeps(svc), Options{Clock: fixedClock(testNow), Logger: testLogger()})

	firstBody := `{"code":` + strconv.Quote(code) + `,"name":"first-admin","password":"a-strong-password-1","deviceLabel":"laptop"}`
	firstReq := newJSONRequest(t, http.MethodPost, "/api/v1/bootstrap", firstBody, nil)
	firstResp, firstRespBody := doRawRequest(t, api.Handler, firstReq)
	if firstResp.StatusCode != http.StatusOK {
		t.Fatalf("first claim status = %d, want 200; body: %s", firstResp.StatusCode, firstRespBody)
	}

	secondBody := `{"code":` + strconv.Quote(code) + `,"name":"second-admin","password":"another-strong-pw","deviceLabel":"laptop"}`
	secondReq := newJSONRequest(t, http.MethodPost, "/api/v1/bootstrap", secondBody, nil)
	secondResp, secondRespBody := doRawRequest(t, api.Handler, secondReq)
	if secondResp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("second claim (same code) status = %d, want 401; body: %s", secondResp.StatusCode, secondRespBody)
	}
}

func TestClaimBootstrapMissingFieldsReturns400(t *testing.T) {
	svc, _ := newTestIdentityServiceWithDataDir(t, fixedClock(testNow))
	mustEnsureBootstrap(t, svc)
	api := New(authTestDeps(svc), Options{Clock: fixedClock(testNow), Logger: testLogger()})

	req := newJSONRequest(t, http.MethodPost, "/api/v1/bootstrap", `{"code":"","name":"","password":""}`, nil)
	resp, body := doRawRequest(t, api.Handler, req)
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400; body: %s", resp.StatusCode, body)
	}
}

// TestClaimBootstrapSharesLoginLimiterWithSession is the Step 6 task
// spec's own requirement, proven directly: "It must be bounded by the
// same login limiter as POST /session, for the same reason." A queued
// login attempt that would exceed the concurrency bound occupied by
// POST /session must ALSO reject a concurrent POST /bootstrap attempt —
// if this package had accidentally given bootstrap its own, separate
// loginLimiter instance, this test would pass a bootstrap request through
// with 200/401 instead of 429, and fail.
func TestClaimBootstrapSharesLoginLimiterWithSession(t *testing.T) {
	svc, _ := newTestIdentityServiceWithDataDir(t, fixedClock(testNow))
	mustCreatePrincipal(t, svc, "operator-1", identity.RoleOperator)
	// A principal already exists, so bootstrap is unavailable regardless
	// (ErrBootstrapNotAvailable) — irrelevant here, since this test only
	// cares that the concurrent bootstrap attempt never gets far enough to
	// even ask ClaimBootstrap anything: it must be rejected by the shared
	// limiter itself, with 429, before that.

	blocking := &blockingAuthenticateService{Service: svc, entered: make(chan struct{}), release: make(chan struct{})}
	api := New(authTestDeps(blocking), Options{
		Clock: fixedClock(testNow), Logger: testLogger(),
		LoginConcurrency: 1, LoginQueueWait: 20 * time.Millisecond,
	})

	firstDone := make(chan *http.Response, 1)
	go func() {
		loginBody := `{"name":"operator-1","password":"` + testPassword + `","deviceLabel":"first"}`
		req := newJSONRequest(t, http.MethodPost, "/api/v1/session", loginBody, nil)
		resp, _ := doRawRequest(t, api.Handler, req)
		firstDone <- resp
	}()

	select {
	case <-blocking.entered:
	case <-time.After(5 * time.Second):
		t.Fatalf("timed out waiting for the first login to occupy the concurrency slot")
	}

	bootstrapBody := `{"code":"whatever-code","name":"second-admin","password":"another-strong-pw","deviceLabel":"second"}`
	bootstrapReq := newJSONRequest(t, http.MethodPost, "/api/v1/bootstrap", bootstrapBody, nil)
	bootstrapResp, bootstrapRespBody := doRawRequest(t, api.Handler, bootstrapReq)
	if bootstrapResp.StatusCode != http.StatusTooManyRequests {
		t.Fatalf("concurrent bootstrap claim status = %d, want 429 (shared limiter with POST /session); body: %s",
			bootstrapResp.StatusCode, bootstrapRespBody)
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
