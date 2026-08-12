package api

import (
	"context"
	"net/http"
	"os"
	"regexp"
	"strings"
	"testing"

	"github.com/showmeshsystems/showmesh/internal/coordinator/identity"
)

// This file closes review finding 12's "nothing enforces 'no state-changing
// GET' as a route inventory" — ADR-024 decision 6's own rule ("No endpoint
// that changes state may be reachable by GET or HEAD... If a mechanism for
// triggering a macro cannot issue a method other than GET, it must not be
// an HTTP endpoint") had no test checking it at all. Two complementary
// checks, neither sufficient alone:
//
//   - A source-level route inventory (mirroring TestSharedSecretMechanismIsRetired's
//     existing technique of reading this package's own source rather than
//     only its runtime behavior): every mux.HandleFunc registration whose
//     handler is one of this package's three write handlers must be
//     registered under an explicit POST or DELETE method prefix, never a
//     bare or GET-prefixed pattern. This catches the mistake at the exact
//     place it would be made — api.go's route table — rather than only
//     inferring it from behavior.
//   - An HTTP-level behavioral check: GET against the exact paths those
//     write handlers own must never perform the mutation, proven by
//     checking real state (principals created) before and after, not only
//     the response status.

// writeHandlerNames is every handler function in this package whose job is
// to change state, matched against api.go's own mux.HandleFunc call sites
// below. Kept as an explicit list (not derived from reflection) so this
// test fails loudly, by compiler-checkable literal, the day a new one is
// added and not accounted for here — see this file's own report entry for
// why an automatically-derived list would defeat the point of a
// human-reviewed inventory.
var writeHandlerNames = []string{"handleCreateSession", "handleDeleteSession", "handleClaimBootstrap"}

// TestNoWriteHandlerIsRegisteredUnderAGETPattern reads api.go's own source
// and checks every mux.HandleFunc(...) registration line: any line naming
// one of writeHandlerNames must have a pattern beginning "POST " or
// "DELETE ", never "GET " and never a bare pattern with no method prefix
// (which net/http.ServeMux would match against every method, GET
// included).
func TestNoWriteHandlerIsRegisteredUnderAGETPattern(t *testing.T) {
	src, err := os.ReadFile("api.go")
	if err != nil {
		t.Fatalf("reading api.go: %v", err)
	}

	handleFuncLine := regexp.MustCompile(`mux\.HandleFunc\("([^"]*)",\s*([^)]*)\)`)
	matches := handleFuncLine.FindAllStringSubmatch(string(src), -1)
	if len(matches) == 0 {
		t.Fatalf("found zero mux.HandleFunc(...) registrations in api.go — this test's own regexp likely no longer matches this file's shape")
	}

	foundWriteHandler := map[string]bool{}
	for _, m := range matches {
		pattern, handlerExpr := m[1], m[2]
		for _, name := range writeHandlerNames {
			if !strings.Contains(handlerExpr, name) {
				continue
			}
			foundWriteHandler[name] = true
			if strings.HasPrefix(pattern, "GET ") {
				t.Errorf("%s is registered under pattern %q, which starts with GET — ADR-024 decision 6 forbids any state-changing endpoint being reachable by GET", name, pattern)
			}
			if !strings.HasPrefix(pattern, "POST ") && !strings.HasPrefix(pattern, "DELETE ") {
				t.Errorf("%s is registered under pattern %q, which names no explicit method — net/http.ServeMux would match this pattern against EVERY method including GET", name, pattern)
			}
		}
	}
	for _, name := range writeHandlerNames {
		if !foundWriteHandler[name] {
			t.Errorf("%s was never found in any mux.HandleFunc registration in api.go — this test's own writeHandlerNames list or regexp is out of sync with the real route table", name)
		}
	}
}

// TestGetBootstrapNeverClaimsIt is the behavioral half: bootstrap.go's
// mutation is reachable only at POST /api/v1/bootstrap. A GET to the exact
// same path must never create a principal — proven by checking real store
// state before and after, not only the HTTP status net/http.ServeMux
// happens to answer with for an unmatched method.
func TestGetBootstrapNeverClaimsIt(t *testing.T) {
	svc, dataDir := newTestIdentityServiceWithDataDir(t, fixedClock(testNow))
	mustEnsureBootstrap(t, svc)
	code := readBootstrapCode(t, dataDir)
	api := New(authTestDeps(svc), Options{Clock: fixedClock(testNow), Logger: testLogger()})

	resp, body := doRequest(t, api.Handler, "GET",
		"/api/v1/bootstrap?code="+code+"&name=attacker&password=whatever", nil)
	if resp.StatusCode == http.StatusOK {
		t.Fatalf("GET /api/v1/bootstrap answered 200; body: %s", body)
	}

	principals, err := svc.ListPrincipals(context.Background())
	if err != nil {
		t.Fatalf("list principals: %v", err)
	}
	if len(principals) != 0 {
		t.Fatalf("a GET request to /api/v1/bootstrap created %d principal(s), want 0", len(principals))
	}
}

// TestGetSessionPathNeverRevokesAnExistingSession is the behavioral
// counterpart for session.go's revoke mutation: a session created via a
// real login must survive a GET to the exact same path,
// /api/v1/session — proven by re-authenticating the real cookie
// afterward, not only by inspecting the response of the GET itself (GET
// /api/v1/session is intentionally a read-only route in its own right;
// this test's job is to confirm it never ALSO reaches the revoke code
// path).
func TestGetSessionPathNeverRevokesAnExistingSession(t *testing.T) {
	svc := newTestIdentityService(t, fixedClock(testNow))
	p := mustCreatePrincipal(t, svc, "operator-1", identity.RoleOperator)
	api := New(authTestDeps(svc), Options{Clock: fixedClock(testNow), Logger: testLogger()})
	cookie := loginAndGetCookie(t, api.Handler, p.Name, testPassword)

	doRequest(t, api.Handler, "GET", "/api/v1/session", map[string]string{
		"Cookie": sessionCookieName + "=" + cookie,
	})

	if _, err := svc.AuthenticateSession(context.Background(), cookie, testNow); err != nil {
		t.Fatalf("session no longer authenticates after a GET to /api/v1/session: %v", err)
	}
}
