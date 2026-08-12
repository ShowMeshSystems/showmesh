package api

import (
	"context"
	"net/http"
	"strings"
	"testing"

	"github.com/showmeshsystems/showmesh/internal/coordinator/identity"
)

// TestAuditRequiresAuthentication proves GET /api/v1/audit is never open,
// unlike the four pre-existing v1 read resources — [Options.CloseReads]
// is left at its zero value (false) here specifically to prove audit is
// gated independently of that setting.
func TestAuditRequiresAuthentication(t *testing.T) {
	api := testAPI(t, Options{})
	resp, body := doRequest(t, api.Handler, "GET", "/api/v1/audit", nil)
	if resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("status = %d, want 401 with reads open but no credential; body: %s", resp.StatusCode, body)
	}
}

// TestAuditForbiddenForViewerNamesMissingScope proves ADR-024 decision
// 4's "403 ... names the missing scope": a real, authenticated viewer
// (every read scope, but not audit:read) is rejected, and the problem
// detail names audit:read specifically, not a generic "forbidden".
func TestAuditForbiddenForViewerNamesMissingScope(t *testing.T) {
	svc := newTestIdentityService(t, fixedClock(testNow))
	p := mustCreatePrincipal(t, svc, "viewer-1", identity.RoleViewer)
	token := mustIssueToken(t, svc, p.ID)
	api := New(authTestDeps(svc), Options{Clock: fixedClock(testNow), Logger: testLogger()})

	resp, body := doRequest(t, api.Handler, "GET", "/api/v1/audit", map[string]string{
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
	if !strings.Contains(detail, string(identity.ScopeAuditRead)) {
		t.Errorf("detail = %q, want it to name %q (ADR-024 decision 4)", detail, identity.ScopeAuditRead)
	}
}

func TestAuditForbiddenForOperator(t *testing.T) {
	// Operator holds the show/device/FPP action scopes but not
	// audit:read (ADR-024 decision 4's table) — this is the case most
	// likely to be mistakenly granted by a "any write scope implies
	// audit" shortcut, so it gets its own test rather than relying on
	// the viewer case alone.
	svc := newTestIdentityService(t, fixedClock(testNow))
	p := mustCreatePrincipal(t, svc, "operator-1", identity.RoleOperator)
	token := mustIssueToken(t, svc, p.ID)
	api := New(authTestDeps(svc), Options{Clock: fixedClock(testNow), Logger: testLogger()})

	resp, body := doRequest(t, api.Handler, "GET", "/api/v1/audit", map[string]string{
		"Authorization": "Bearer " + token,
	})
	if resp.StatusCode != http.StatusForbidden {
		t.Fatalf("status = %d, want 403; body: %s", resp.StatusCode, body)
	}
}

// TestAuditSucceedsForAdminAndListsAWrite proves the positive path end to
// end: an admin token reads the audit log after a real write (a session
// creation), and the resulting entry is attributable — the whole point of
// ARCHITECTURE §8.1's "a command carries an issuer", concretized by
// ADR-024 decision 11.
func TestAuditSucceedsForAdminAndListsAWrite(t *testing.T) {
	svc := newTestIdentityService(t, fixedClock(testNow))
	operator := mustCreatePrincipal(t, svc, "operator-1", identity.RoleOperator)
	admin := mustCreatePrincipal(t, svc, "admin-1", identity.RoleAdmin)
	api := New(authTestDeps(svc), Options{Clock: fixedClock(testNow), Logger: testLogger()})

	loginAndGetCookie(t, api.Handler, operator.Name, testPassword) // produces a session.create audit entry

	adminToken := mustIssueToken(t, svc, admin.ID)
	resp, body := doRequest(t, api.Handler, "GET", "/api/v1/audit", map[string]string{
		"Authorization": "Bearer " + adminToken,
	})
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200; body: %s", resp.StatusCode, body)
	}

	m := decodeMap(t, body)
	entries, _ := m["entries"].([]any)
	var found bool
	for _, raw := range entries {
		e, _ := raw.(map[string]any)
		if e["action"] == "session.create" && e["principalId"] == operator.ID {
			found = true
			if e["kind"] != "admin" {
				t.Errorf("kind = %v, want \"admin\"", e["kind"])
			}
		}
	}
	if !found {
		t.Fatalf("no session.create entry attributed to %s found in: %s", operator.ID, body)
	}
}

// TestAuditNeverAppearsOnTheChangeStream is ADR-024 decision 11's
// explicit rule, checked structurally: writing an audit entry must never
// be observable through the SSE stream, which this package's own
// standing design only ever notifies about nodes, FPP instances, and
// events (see [Hub.render]) — this test exists so a future change adding
// an "audit.recorded" stream frame is caught rather than silently
// accepted as a nice addition. It writes an audit entry directly (the
// same call any handler in this package makes) and confirms the hub's
// own render pass, poked immediately after, produces nothing derived
// from it.
func TestAuditNeverAppearsOnTheChangeStream(t *testing.T) {
	svc := newTestIdentityService(t, fixedClock(testNow))
	if err := svc.WriteAudit(context.Background(), identity.AuditEntry{
		Timestamp: testNow, Action: "test.action", Kind: identity.AuditAdmin,
	}); err != nil {
		t.Fatalf("write audit: %v", err)
	}

	api := newStreamTestAPI(authTestDeps(svc))
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go api.Hub.Run(ctx)

	// render() only ever consults deps.Nodes/FPP/Events (see stream.go);
	// asserting that it produces zero pending frames when nothing else
	// changed is the structural proof that an audit write has no path
	// into the hub at all, short of reading stream.go's own source.
	api.Hub.render(ctx)
	api.Hub.mu.Lock()
	lastEventSeq := api.Hub.lastEventSeq
	api.Hub.mu.Unlock()
	if lastEventSeq != 0 {
		t.Fatalf("hub's lastEventSeq advanced to %d after only an audit write; audit must never reach event history or the stream", lastEventSeq)
	}
}
