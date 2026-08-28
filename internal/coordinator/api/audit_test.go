package api

import (
	"context"
	"fmt"
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

// writeAuditEntries appends n admin-kind audit entries.
func writeAuditEntries(t *testing.T, svc identity.Service, n int) {
	t.Helper()
	for i := 0; i < n; i++ {
		if err := svc.WriteAudit(context.Background(), identity.AuditEntry{
			Timestamp: testNow,
			Action:    fmt.Sprintf("test.action-%02d", i),
			Kind:      identity.AuditAdmin,
		}); err != nil {
			t.Fatalf("write audit %d: %v", i, err)
		}
	}
}

// auditGET issues one authenticated GET /api/v1/audit and decodes it.
func auditGET(t *testing.T, h http.Handler, token, query string) (int, map[string]any) {
	t.Helper()
	resp, body := doRequest(t, h, "GET", "/api/v1/audit"+query, map[string]string{
		"Authorization": "Bearer " + token,
	})
	return resp.StatusCode, decodeMap(t, body)
}

// auditIDs pulls the entry ids out of a decoded audit body, in wire order.
func auditIDs(t *testing.T, m map[string]any) []int64 {
	t.Helper()
	raw, _ := m["entries"].([]any)
	out := make([]int64, 0, len(raw))
	for _, e := range raw {
		entry, _ := e.(map[string]any)
		id, ok := entry["id"].(float64)
		if !ok {
			t.Fatalf("entry has no numeric id: %+v", entry)
		}
		out = append(out, int64(id))
	}
	return out
}

// TestAuditEntriesCarryTheirID is what makes any cursor on this endpoint
// honest: without an id per entry, a client has nothing to advance either
// cursor with.
func TestAuditEntriesCarryTheirID(t *testing.T) {
	svc := newTestIdentityService(t, fixedClock(testNow))
	admin := mustCreatePrincipal(t, svc, "admin-1", identity.RoleAdmin)
	api := New(authTestDeps(svc), Options{Clock: fixedClock(testNow), Logger: testLogger()})
	writeAuditEntries(t, svc, 3)

	_, m := auditGET(t, api.Handler, mustIssueToken(t, svc, admin.ID), "")
	ids := auditIDs(t, m)
	if len(ids) < 3 {
		t.Fatalf("ids = %v, want at least the three written entries", ids)
	}
	for i := 1; i < len(ids); i++ {
		if ids[i] <= ids[i-1] {
			t.Fatalf("ids = %v, want strictly increasing on an ascending page", ids)
		}
	}
	if m["order"] != "asc" {
		t.Errorf("order = %v, want \"asc\" echoed on the default page", m["order"])
	}
	if _, present := m["oldestRetainedId"]; !present {
		t.Error("oldestRetainedId missing; the response must report it, never leave it to be inferred")
	}
}

// TestAuditNewestFirstReturnsTheMostRecentPageInOneRequest is the
// operator-facing point of the whole change: the newest entry is on the
// first page, without walking retained history.
func TestAuditNewestFirstReturnsTheMostRecentPageInOneRequest(t *testing.T) {
	svc := newTestIdentityService(t, fixedClock(testNow))
	admin := mustCreatePrincipal(t, svc, "admin-1", identity.RoleAdmin)
	api := New(authTestDeps(svc), Options{Clock: fixedClock(testNow), Logger: testLogger()})
	writeAuditEntries(t, svc, 25)
	token := mustIssueToken(t, svc, admin.ID)

	_, asc := auditGET(t, api.Handler, token, "?limit=500")
	ascIDs := auditIDs(t, asc)
	newest := ascIDs[len(ascIDs)-1]

	_, desc := auditGET(t, api.Handler, token, "?order=desc&limit=3")
	descIDs := auditIDs(t, desc)
	if len(descIDs) != 3 {
		t.Fatalf("descending page had %d entries, want 3", len(descIDs))
	}
	if descIDs[0] != newest {
		t.Errorf("first descending id = %d, want the newest retained id %d", descIDs[0], newest)
	}
	for i := 1; i < len(descIDs); i++ {
		if descIDs[i] >= descIDs[i-1] {
			t.Fatalf("ids = %v, want strictly decreasing on a descending page", descIDs)
		}
	}
	if desc["order"] != "desc" {
		t.Errorf("order = %v, want \"desc\" echoed", desc["order"])
	}
}

// TestAuditNewestFirstWalksBackwardWithoutDuplicatesOrSkips walks the
// whole log backward through the HTTP surface, on real ids, and proves the
// walk covers retained history exactly once and ends at oldestRetainedId.
func TestAuditNewestFirstWalksBackwardWithoutDuplicatesOrSkips(t *testing.T) {
	svc := newTestIdentityService(t, fixedClock(testNow))
	admin := mustCreatePrincipal(t, svc, "admin-1", identity.RoleAdmin)
	api := New(authTestDeps(svc), Options{Clock: fixedClock(testNow), Logger: testLogger()})
	writeAuditEntries(t, svc, 20)
	token := mustIssueToken(t, svc, admin.ID)

	_, asc := auditGET(t, api.Handler, token, "?limit=500")
	ascIDs := auditIDs(t, asc)

	seen := map[int64]bool{}
	var walked []int64
	query := "?order=desc&limit=6"
	var oldestRetained float64
	for page := 0; ; page++ {
		if page > 20 {
			t.Fatalf("backward walk did not terminate after %d pages", page)
		}
		_, m := auditGET(t, api.Handler, token, query)
		if v, ok := m["oldestRetainedId"].(float64); ok {
			oldestRetained = v
		}
		ids := auditIDs(t, m)
		if len(ids) == 0 {
			break
		}
		for _, id := range ids {
			if seen[id] {
				t.Fatalf("id %d returned twice across pages: %v", id, walked)
			}
			seen[id] = true
			walked = append(walked, id)
		}
		query = fmt.Sprintf("?order=desc&limit=6&before=%d", ids[len(ids)-1])
	}

	if len(walked) != len(ascIDs) {
		t.Fatalf("walked %d ids, want %d: the backward walk skipped or duplicated", len(walked), len(ascIDs))
	}
	for i, id := range walked {
		if want := ascIDs[len(ascIDs)-1-i]; id != want {
			t.Fatalf("walked[%d] = %d, want %d", i, id, want)
		}
	}
	if int64(oldestRetained) != walked[len(walked)-1] {
		t.Errorf("walk ended at %d, want the reported oldestRetainedId %d", walked[len(walked)-1], int64(oldestRetained))
	}
}

// TestAuditRefusesContradictoryCursorParameters proves neither cursor is
// ever silently ignored: naming both, or naming the one the chosen order
// does not use, is a 400.
func TestAuditRefusesContradictoryCursorParameters(t *testing.T) {
	svc := newTestIdentityService(t, fixedClock(testNow))
	admin := mustCreatePrincipal(t, svc, "admin-1", identity.RoleAdmin)
	api := New(authTestDeps(svc), Options{Clock: fixedClock(testNow), Logger: testLogger()})
	token := mustIssueToken(t, svc, admin.ID)

	for _, query := range []string{
		"?since=1&before=9",
		"?order=desc&since=1",
		"?before=9",
		"?order=sideways",
		"?order=desc&before=-1",
	} {
		resp, body := doRequest(t, api.Handler, "GET", "/api/v1/audit"+query, map[string]string{
			"Authorization": "Bearer " + token,
		})
		if resp.StatusCode != http.StatusBadRequest {
			t.Errorf("GET /api/v1/audit%s status = %d, want 400; body: %s", query, resp.StatusCode, body)
		}
	}
}

// TestAuditReportsOldestRetainedIDAsARealNumber keeps the two ends of the
// backward walk distinguishable: a log that retains entries reports the
// lowest id it holds, never null and never a placeholder zero.
func TestAuditReportsOldestRetainedIDAsARealNumber(t *testing.T) {
	svc := newTestIdentityService(t, fixedClock(testNow))
	admin := mustCreatePrincipal(t, svc, "admin-1", identity.RoleAdmin)
	api := New(authTestDeps(svc), Options{Clock: fixedClock(testNow), Logger: testLogger()})
	writeAuditEntries(t, svc, 2)
	token := mustIssueToken(t, svc, admin.ID)

	_, m := auditGET(t, api.Handler, token, "?order=desc")
	oldest, ok := m["oldestRetainedId"].(float64)
	if !ok || oldest < 1 {
		t.Fatalf("oldestRetainedId = %v, want the lowest retained id", m["oldestRetainedId"])
	}
	ids := auditIDs(t, m)
	if int64(oldest) > ids[len(ids)-1] {
		t.Errorf("oldestRetainedId %d is above the oldest id on this page (%d)", int64(oldest), ids[len(ids)-1])
	}
}

// TestAuditNeverAppearsOnTheChangeStream is ADR-024 decision 11's
// explicit rule, checked structurally: writing an audit entry must never
// be observable through the SSE stream, which this package's own
// standing design only ever notifies about nodes, FPP instances, and
// events (see [Hub.render]) — this test exists so a future change adding
// an "audit.recorded" stream frame is caught rather than silently
// accepted as a nice addition.
//
// A review finding (mutation-confirmed) caught that the previous version
// of this test asserted on h.lastEventSeq — the EVENT HISTORY cursor,
// which [identity.Service.WriteAudit] was never going to advance
// regardless of whether an "audit.recorded" stream frame existed, because
// that cursor only moves when [Hub.renderNewEvents] reads a NEW event
// record, a completely different code path audit writes never touch. The
// test could not have failed even with the regression it claimed to
// guard against. This version subscribes a real (in-process) subscriber
// the same way [Hub.ServeHTTP] does and inspects its actual frame
// channel — the thing an "audit.recorded" frame would have to be queued
// into to ever reach a client — after an audit write and a render pass.
func TestAuditNeverAppearsOnTheChangeStream(t *testing.T) {
	svc := newTestIdentityService(t, fixedClock(testNow))
	api := newStreamTestAPI(authTestDeps(svc))

	_, sub := api.Hub.subscribe(false, nil)
	defer api.Hub.unsubscribe(0)

	if err := svc.WriteAudit(context.Background(), identity.AuditEntry{
		Timestamp: testNow, Action: "test.action", Kind: identity.AuditAdmin,
	}); err != nil {
		t.Fatalf("write audit: %v", err)
	}

	api.Hub.render(context.Background())

	select {
	case pf := <-sub.frames:
		t.Fatalf("a frame was queued for this subscriber after only an audit write: %+v — audit must never reach the change stream", pf)
	default:
		// Nothing queued — the expected, correct outcome.
	}
}
