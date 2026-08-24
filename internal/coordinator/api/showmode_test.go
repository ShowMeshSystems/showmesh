package api

import (
	"context"
	"net/http"
	"strings"
	"testing"

	"github.com/showmeshsystems/showmesh/internal/coordinator/config"
	"github.com/showmeshsystems/showmesh/internal/coordinator/identity"
	"github.com/showmeshsystems/showmesh/internal/coordinator/store"
)

const validShowModeBody = `{"mode":"show"}`

// TestGetShowModeConfigUnconfiguredReportsProgram pins the fresh-install
// default (owner ruling): nothing has ever been written, so this answers
// 200 with revision 0, source "default", and mode "program". A fresh
// install is by definition being set up.
func TestGetShowModeConfigUnconfiguredReportsProgram(t *testing.T) {
	svc, st, _ := newTestIdentityServiceWithStore(t, fixedClock(testNow))
	admin := mustCreatePrincipal(t, svc, "admin-1", identity.RoleAdmin)
	adminToken := mustIssueToken(t, svc, admin.ID)
	api := New(configTestDeps(svc, st), Options{Clock: fixedClock(testNow), Logger: testLogger()})

	resp, body := doRequest(t, api.Handler, "GET", "/api/v1/config/show.mode",
		map[string]string{"Authorization": "Bearer " + adminToken})
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200; body: %s", resp.StatusCode, body)
	}
	m := decodeMap(t, body)
	if m["revision"] != float64(0) {
		t.Errorf("revision = %v, want 0", m["revision"])
	}
	if m["source"] != "default" {
		t.Errorf("source = %v, want default", m["source"])
	}
	payload, _ := m["payload"].(map[string]any)
	if payload["mode"] != "program" {
		t.Errorf("payload.mode = %v, want program", payload["mode"])
	}
	// ADR-033 decision 3: the mode's effect is stated where the operator
	// can see it, naming the mode as the reason.
	effect, _ := m["resolumeWebSocketEffect"].(string)
	if !strings.Contains(effect, "program mode") {
		t.Errorf("resolumeWebSocketEffect = %q, want it to name the mode", effect)
	}
}

// TestGetShowModeConfigIsReadableWithoutConfigWrite is the deliberate
// departure from every other configuration singleton, and the reason this
// test exists rather than a copy of
// TestGetRenderSettingsConfigRequiresConfigWriteScope: ADR-033 decision 3
// requires the mode to be persistently visible, and the operator at the
// console does not hold config:write. A viewer, holding only the four read
// scopes, must be able to read the CURRENT value even on a coordinator
// with reads closed.
func TestGetShowModeConfigIsReadableWithoutConfigWrite(t *testing.T) {
	svc, st, _ := newTestIdentityServiceWithStore(t, fixedClock(testNow))
	viewer := mustCreatePrincipal(t, svc, "viewer-1", identity.RoleViewer)
	operator := mustCreatePrincipal(t, svc, "operator-1", identity.RoleOperator)
	viewerToken := mustIssueToken(t, svc, viewer.ID)
	operatorToken := mustIssueToken(t, svc, operator.ID)

	t.Run("open reads", func(t *testing.T) {
		api := New(configTestDeps(svc, st), Options{Clock: fixedClock(testNow), Logger: testLogger()})
		resp, body := doRequest(t, api.Handler, "GET", "/api/v1/config/show.mode", nil)
		if resp.StatusCode != http.StatusOK {
			t.Fatalf("status = %d, want 200; body: %s", resp.StatusCode, body)
		}
	})

	t.Run("closed reads", func(t *testing.T) {
		api := New(configTestDeps(svc, st), Options{Clock: fixedClock(testNow), Logger: testLogger(), CloseReads: true})
		for name, token := range map[string]string{"viewer": viewerToken, "operator": operatorToken} {
			resp, body := doRequest(t, api.Handler, "GET", "/api/v1/config/show.mode",
				map[string]string{"Authorization": "Bearer " + token})
			if resp.StatusCode != http.StatusOK {
				t.Fatalf("%s: status = %d, want 200; body: %s", name, resp.StatusCode, body)
			}
		}
		resp, body := doRequest(t, api.Handler, "GET", "/api/v1/config/show.mode", nil)
		if resp.StatusCode != http.StatusUnauthorized {
			t.Fatalf("unauthenticated with reads closed: status = %d, want 401; body: %s", resp.StatusCode, body)
		}
	})
}

// The HISTORY keeps the ordinary config:write gate. The open read is the
// current value, which is what decision 3 asks to be visible; revision
// metadata carries principal names and is audit-adjacent.
func TestGetShowModeConfigRevisionsRequiresConfigWrite(t *testing.T) {
	svc, st, _ := newTestIdentityServiceWithStore(t, fixedClock(testNow))
	viewer := mustCreatePrincipal(t, svc, "viewer-1", identity.RoleViewer)
	admin := mustCreatePrincipal(t, svc, "admin-1", identity.RoleAdmin)
	viewerToken := mustIssueToken(t, svc, viewer.ID)
	adminToken := mustIssueToken(t, svc, admin.ID)
	api := New(configTestDeps(svc, st), Options{Clock: fixedClock(testNow), Logger: testLogger()})

	resp, body := doRequest(t, api.Handler, "GET", "/api/v1/config/show.mode/revisions", nil)
	if resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("unauthenticated: status = %d, want 401; body: %s", resp.StatusCode, body)
	}
	resp, body = doRequest(t, api.Handler, "GET", "/api/v1/config/show.mode/revisions",
		map[string]string{"Authorization": "Bearer " + viewerToken})
	if resp.StatusCode != http.StatusForbidden {
		t.Fatalf("viewer: status = %d, want 403; body: %s", resp.StatusCode, body)
	}
	resp, body = doRequest(t, api.Handler, "GET", "/api/v1/config/show.mode/revisions",
		map[string]string{"Authorization": "Bearer " + adminToken})
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("admin: status = %d, want 200; body: %s", resp.StatusCode, body)
	}
}

func TestPutShowModeConfigAuthAndScope(t *testing.T) {
	svc, st, _ := newTestIdentityServiceWithStore(t, fixedClock(testNow))
	viewer := mustCreatePrincipal(t, svc, "viewer-1", identity.RoleViewer)
	operator := mustCreatePrincipal(t, svc, "operator-1", identity.RoleOperator)
	admin := mustCreatePrincipal(t, svc, "admin-1", identity.RoleAdmin)
	viewerToken := mustIssueToken(t, svc, viewer.ID)
	operatorToken := mustIssueToken(t, svc, operator.ID)
	adminToken := mustIssueToken(t, svc, admin.ID)

	api := New(configTestDeps(svc, st), Options{Clock: fixedClock(testNow), Logger: testLogger()})

	t.Run("unauthenticated", func(t *testing.T) {
		req := newJSONRequest(t, http.MethodPut, "/api/v1/config/show.mode", validShowModeBody, nil)
		resp, body := doRawRequest(t, api.Handler, req)
		if resp.StatusCode != http.StatusUnauthorized {
			t.Fatalf("status = %d, want 401; body: %s", resp.StatusCode, body)
		}
	})

	// The read being open must not have opened the write. A viewer and an
	// operator can SEE the mode and cannot set it.
	for name, token := range map[string]string{"viewer": viewerToken, "operator": operatorToken} {
		t.Run(name+" forbidden naming config:write", func(t *testing.T) {
			req := newJSONRequest(t, http.MethodPut, "/api/v1/config/show.mode", validShowModeBody,
				map[string]string{"Authorization": "Bearer " + token})
			resp, body := doRawRequest(t, api.Handler, req)
			if resp.StatusCode != http.StatusForbidden {
				t.Fatalf("status = %d, want 403; body: %s", resp.StatusCode, body)
			}
			if !strings.Contains(string(body), "config:write") {
				t.Errorf("body = %s, want it to name the missing scope config:write", body)
			}
		})
	}

	t.Run("admin accepted", func(t *testing.T) {
		req := newJSONRequest(t, http.MethodPut, "/api/v1/config/show.mode", validShowModeBody,
			map[string]string{"Authorization": "Bearer " + adminToken})
		resp, body := doRawRequest(t, api.Handler, req)
		if resp.StatusCode != http.StatusOK {
			t.Fatalf("status = %d, want 200; body: %s", resp.StatusCode, body)
		}
		m := decodeMap(t, body)
		if m["revision"] != float64(1) {
			t.Errorf("revision = %v, want 1", m["revision"])
		}
		if m["source"] != "api" {
			t.Errorf("source = %v, want api", m["source"])
		}
		payload, _ := m["payload"].(map[string]any)
		if payload["mode"] != "show" {
			t.Errorf("payload.mode = %v, want show", payload["mode"])
		}
		effect, _ := m["resolumeWebSocketEffect"].(string)
		if !strings.Contains(effect, "show mode") {
			t.Errorf("resolumeWebSocketEffect = %q, want it to name show mode", effect)
		}
	})
}

// The write is audited with the principal that made it (ADR-024), and the
// audit entry names the mode that was set.
func TestPutShowModeConfigWritesAnAuditEntryNamingThePrincipal(t *testing.T) {
	svc, st, _ := newTestIdentityServiceWithStore(t, fixedClock(testNow))
	admin := mustCreatePrincipal(t, svc, "admin-1", identity.RoleAdmin)
	adminToken := mustIssueToken(t, svc, admin.ID)
	api := New(configTestDeps(svc, st), Options{Clock: fixedClock(testNow), Logger: testLogger()})

	req := newJSONRequest(t, http.MethodPut, "/api/v1/config/show.mode", validShowModeBody,
		map[string]string{"Authorization": "Bearer " + adminToken})
	if resp, body := doRawRequest(t, api.Handler, req); resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200; body: %s", resp.StatusCode, body)
	}

	entries, err := svc.ListAudit(context.Background(), 0, 100)
	if err != nil {
		t.Fatalf("ListAudit: %v", err)
	}
	for _, e := range entries {
		if e.Action == "config.write" && e.Target == "show.mode" {
			if e.PrincipalID != admin.ID {
				t.Fatalf("audit entry principal = %q, want %q", e.PrincipalID, admin.ID)
			}
			return
		}
	}
	t.Fatalf("no config.write audit entry targeting show.mode in %v", entries)
}

// ADR-009: a rejected write leaves no revision behind, and the closed enum
// is enforced on the real HTTP path rather than only in the decoder's own
// unit tests. "unknown" is refused specifically: it is a node-side state,
// never a value an operator can write.
func TestPutShowModeConfigRejectsNonMembersBeforeActivation(t *testing.T) {
	svc, st, _ := newTestIdentityServiceWithStore(t, fixedClock(testNow))
	admin := mustCreatePrincipal(t, svc, "admin-1", identity.RoleAdmin)
	adminToken := mustIssueToken(t, svc, admin.ID)
	api := New(configTestDeps(svc, st), Options{Clock: fixedClock(testNow), Logger: testLogger()})
	headers := map[string]string{"Authorization": "Bearer " + adminToken}

	for _, body := range []string{`{"mode":"unknown"}`, `{"mode":"setup"}`, `{"mode":""}`} {
		req := newJSONRequest(t, http.MethodPut, "/api/v1/config/show.mode", body, headers)
		resp, respBody := doRawRequest(t, api.Handler, req)
		if resp.StatusCode != http.StatusBadRequest {
			t.Fatalf("PUT %s: status = %d, want 400; body: %s", body, resp.StatusCode, respBody)
		}
	}

	revs, err := st.ListConfigRevisions(context.Background(), "show.mode", "default")
	if err != nil {
		t.Fatalf("ListConfigRevisions: %v", err)
	}
	if len(revs) != 0 {
		t.Fatalf("revisions after rejected writes = %v, want none", revs)
	}
}

// An absent key is refused by name, never treated as "leave it as it was".
func TestPutShowModeConfigRejectsAbsentModeKey(t *testing.T) {
	svc, st, _ := newTestIdentityServiceWithStore(t, fixedClock(testNow))
	admin := mustCreatePrincipal(t, svc, "admin-1", identity.RoleAdmin)
	adminToken := mustIssueToken(t, svc, admin.ID)
	api := New(configTestDeps(svc, st), Options{Clock: fixedClock(testNow), Logger: testLogger()})

	req := newJSONRequest(t, http.MethodPut, "/api/v1/config/show.mode", `{}`,
		map[string]string{"Authorization": "Bearer " + adminToken})
	resp, respBody := doRawRequest(t, api.Handler, req)
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400; body: %s", resp.StatusCode, respBody)
	}
	if !strings.Contains(string(respBody), "mode") {
		t.Errorf("body = %s, want it to name mode", respBody)
	}
}

// ADR-024 decision 11's same-transaction rule, against a REAL SQLite
// trigger.
func TestPutShowModeConfigFailsClosedOnAuditFailure(t *testing.T) {
	svc, st, storeDir := newTestIdentityServiceWithStore(t, fixedClock(testNow))
	admin := mustCreatePrincipal(t, svc, "admin-1", identity.RoleAdmin)
	adminToken := mustIssueToken(t, svc, admin.ID)
	api := New(configTestDeps(svc, st), Options{Clock: fixedClock(testNow), Logger: testLogger()})

	installFailAuditTrigger(t, storeDir)

	req := newJSONRequest(t, http.MethodPut, "/api/v1/config/show.mode", validShowModeBody,
		map[string]string{"Authorization": "Bearer " + adminToken})
	resp, body := doRawRequest(t, api.Handler, req)
	if resp.StatusCode != http.StatusInternalServerError {
		t.Fatalf("status = %d, want 500; body: %s", resp.StatusCode, body)
	}

	revs, err := st.ListConfigRevisions(context.Background(), "show.mode", "default")
	if err != nil {
		t.Fatalf("ListConfigRevisions: %v", err)
	}
	if len(revs) != 0 {
		t.Fatalf("revisions after a failed-audit write = %v, want none", revs)
	}
}

func TestGetShowModeConfigRevisionsListsNewestFirst(t *testing.T) {
	svc, st, _ := newTestIdentityServiceWithStore(t, fixedClock(testNow))
	admin := mustCreatePrincipal(t, svc, "admin-1", identity.RoleAdmin)
	adminToken := mustIssueToken(t, svc, admin.ID)
	api := New(configTestDeps(svc, st), Options{Clock: fixedClock(testNow), Logger: testLogger()})
	headers := map[string]string{"Authorization": "Bearer " + adminToken}

	for _, body := range []string{`{"mode":"show"}`, `{"mode":"program"}`, `{"mode":"show"}`} {
		req := newJSONRequest(t, http.MethodPut, "/api/v1/config/show.mode", body, headers)
		resp, respBody := doRawRequest(t, api.Handler, req)
		if resp.StatusCode != http.StatusOK {
			t.Fatalf("PUT status = %d, want 200; body: %s", resp.StatusCode, respBody)
		}
	}

	resp, body := doRequest(t, api.Handler, "GET", "/api/v1/config/show.mode/revisions", headers)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200; body: %s", resp.StatusCode, body)
	}
	m := decodeMap(t, body)
	if m["kind"] != "show.mode" {
		t.Errorf("kind = %v, want show.mode", m["kind"])
	}
	revs, _ := m["revisions"].([]any)
	if len(revs) != 3 {
		t.Fatalf("revisions count = %d, want 3", len(revs))
	}
	first, _ := revs[0].(map[string]any)
	if first["revision"] != float64(3) {
		t.Errorf("revisions[0].revision = %v, want 3 (newest first)", first["revision"])
	}
	if first["active"] != true {
		t.Errorf("revisions[0].active = %v, want true", first["active"])
	}
}

// interleavedConfigStore is [ConfigStore] with one deliberate seam: after
// its FIRST GetConfigRevision call returns, it runs afterFirstRevision,
// which lands a second write directly against the same underlying store.
// That first call is resolveShowMode's own revision read, so the write
// lands exactly between it and anything handleGetShowModeConfig does
// afterward, with no sleep and no goroutine race.
type interleavedConfigStore struct {
	*store.Store
	afterFirstRevision func()
	revisionCalls      int
}

func (s *interleavedConfigStore) GetConfigRevision(ctx context.Context, kind, id string, revision int64) (store.ConfigRevisionRecord, error) {
	rec, err := s.Store.GetConfigRevision(ctx, kind, id, revision)
	s.revisionCalls++
	if s.revisionCalls == 1 && s.afterFirstRevision != nil {
		s.afterFirstRevision()
	}
	return rec, err
}

// TestGetShowModeConfigDoesNotTearReadAcrossAConcurrentActivation guards
// the pairing invariant of GET /api/v1/config/show.mode: the revision
// number and metadata the response reports and the payload it reports must
// always describe the SAME revision, even when a PUT activates a new
// revision between the handler's store reads.
func TestGetShowModeConfigDoesNotTearReadAcrossAConcurrentActivation(t *testing.T) {
	svc, st, _ := newTestIdentityServiceWithStore(t, fixedClock(testNow))
	admin := mustCreatePrincipal(t, svc, "admin-1", identity.RoleAdmin)
	adminToken := mustIssueToken(t, svc, admin.ID)

	// Revision 1, mode "show", is the value in place when the GET starts.
	putReq := newJSONRequest(t, http.MethodPut, "/api/v1/config/show.mode", `{"mode":"show"}`,
		map[string]string{"Authorization": "Bearer " + adminToken})
	setupAPI := New(configTestDeps(svc, st), Options{Clock: fixedClock(testNow), Logger: testLogger()})
	if resp, body := doRawRequest(t, setupAPI.Handler, putReq); resp.StatusCode != http.StatusOK {
		t.Fatalf("setup PUT status = %d, want 200; body: %s", resp.StatusCode, body)
	}

	racyStore := &interleavedConfigStore{Store: st}
	deps := configTestDeps(svc, st)
	deps.Config = racyStore
	api := New(deps, Options{Clock: fixedClock(testNow), Logger: testLogger()})

	// Simulates a concurrent PUT landing after resolveShowMode's revision
	// read: revision 2, mode "program", activated directly against the
	// same store - the same two writes handlePutShowModeConfig itself
	// makes, just outside this GET's own request lifecycle.
	racyStore.afterFirstRevision = func() {
		ctx := context.Background()
		rec, err := st.CreateConfigRevision(ctx, store.ConfigRevisionRecord{
			Kind: config.ShowModeConfigKind, ObjectID: config.ShowModeConfigObjectID,
			Revision: 2, PayloadJSON: `{"mode":"program"}`,
			CreatedByPrincipalID: admin.ID, CreatedByPrincipalName: admin.Name,
			Source: config.ShowModeSourceAPI,
		})
		if err != nil {
			t.Fatalf("interleaved CreateConfigRevision: %v", err)
		}
		if _, err := st.ActivateConfigRevision(ctx, rec.Kind, rec.ObjectID, rec.Revision); err != nil {
			t.Fatalf("interleaved ActivateConfigRevision: %v", err)
		}
	}

	resp, body := doRequest(t, api.Handler, "GET", "/api/v1/config/show.mode",
		map[string]string{"Authorization": "Bearer " + adminToken})
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200; body: %s", resp.StatusCode, body)
	}
	m := decodeMap(t, body)
	revision := m["revision"]
	payload, _ := m["payload"].(map[string]any)
	mode := payload["mode"]

	// Whichever revision the response names, its payload must be THAT
	// revision's own payload: revision 1 paired with "show", or revision 2
	// paired with "program". Revision 2 paired with "show" is the tear.
	switch revision {
	case float64(1):
		if mode != "show" {
			t.Fatalf("revision = 1 but payload.mode = %v, want show (torn read)", mode)
		}
	case float64(2):
		if mode != "program" {
			t.Fatalf("revision = 2 but payload.mode = %v, want program (torn read)", mode)
		}
	default:
		t.Fatalf("revision = %v, want 1 or 2", revision)
	}
}

// interleavedShowModeObjectStore is [ConfigStore] with one deliberate seam:
// after its FIRST GetConfigObject OR ListConfigRevisions call returns
// (whichever the handler under test makes first), it runs afterFirstRead,
// which lands a second write directly against the same underlying store.
// handleGetShowModeConfigRevisions makes exactly one of these two calls, so
// hooking both lets this store reproduce the tear regardless of which read
// the handler's current implementation happens to make.
type interleavedShowModeObjectStore struct {
	*store.Store
	afterFirstRead func()
	readCalls      int
}

func (s *interleavedShowModeObjectStore) fireAfterFirstRead() {
	s.readCalls++
	if s.readCalls == 1 && s.afterFirstRead != nil {
		s.afterFirstRead()
	}
}

func (s *interleavedShowModeObjectStore) GetConfigObject(ctx context.Context, kind, id string) (store.ConfigObjectRecord, error) {
	rec, err := s.Store.GetConfigObject(ctx, kind, id)
	s.fireAfterFirstRead()
	return rec, err
}

func (s *interleavedShowModeObjectStore) ListConfigRevisions(ctx context.Context, kind, id string) ([]store.ConfigRevisionRecord, error) {
	recs, err := s.Store.ListConfigRevisions(ctx, kind, id)
	s.fireAfterFirstRead()
	return recs, err
}

// TestGetShowModeConfigRevisionsDoesNotTearReadAcrossAConcurrentActivation
// guards the same pairing invariant as
// TestGetShowModeConfigDoesNotTearReadAcrossAConcurrentActivation, applied
// to GET /api/v1/config/show.mode/revisions: whichever revision the
// response marks "active" must actually still be current given the
// revisions the response itself lists, even when a PUT activates a new
// revision between the handler's own store reads.
func TestGetShowModeConfigRevisionsDoesNotTearReadAcrossAConcurrentActivation(t *testing.T) {
	svc, st, _ := newTestIdentityServiceWithStore(t, fixedClock(testNow))
	admin := mustCreatePrincipal(t, svc, "admin-1", identity.RoleAdmin)
	adminToken := mustIssueToken(t, svc, admin.ID)

	// Revision 1, mode "show", is the value in place when the GET starts.
	putReq := newJSONRequest(t, http.MethodPut, "/api/v1/config/show.mode", `{"mode":"show"}`,
		map[string]string{"Authorization": "Bearer " + adminToken})
	setupAPI := New(configTestDeps(svc, st), Options{Clock: fixedClock(testNow), Logger: testLogger()})
	if resp, body := doRawRequest(t, setupAPI.Handler, putReq); resp.StatusCode != http.StatusOK {
		t.Fatalf("setup PUT status = %d, want 200; body: %s", resp.StatusCode, body)
	}

	racyStore := &interleavedShowModeObjectStore{Store: st}
	deps := configTestDeps(svc, st)
	deps.Config = racyStore
	api := New(deps, Options{Clock: fixedClock(testNow), Logger: testLogger()})

	// Simulates a concurrent PUT landing after the handler's own first
	// store read: revision 2, mode "program", activated directly against
	// the same store - the same two writes handlePutShowModeConfig itself
	// makes, just outside this GET's own request lifecycle.
	racyStore.afterFirstRead = func() {
		ctx := context.Background()
		rec, err := st.CreateConfigRevision(ctx, store.ConfigRevisionRecord{
			Kind: config.ShowModeConfigKind, ObjectID: config.ShowModeConfigObjectID,
			Revision: 2, PayloadJSON: `{"mode":"program"}`,
			CreatedByPrincipalID: admin.ID, CreatedByPrincipalName: admin.Name,
			Source: config.ShowModeSourceAPI,
		})
		if err != nil {
			t.Fatalf("interleaved CreateConfigRevision: %v", err)
		}
		if _, err := st.ActivateConfigRevision(ctx, rec.Kind, rec.ObjectID, rec.Revision); err != nil {
			t.Fatalf("interleaved ActivateConfigRevision: %v", err)
		}
	}

	resp, body := doRequest(t, api.Handler, "GET", "/api/v1/config/show.mode/revisions",
		map[string]string{"Authorization": "Bearer " + adminToken})
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200; body: %s", resp.StatusCode, body)
	}
	m := decodeMap(t, body)
	revs, _ := m["revisions"].([]any)
	if len(revs) == 0 {
		t.Fatalf("revisions = %v, want at least one", revs)
	}

	// Whichever revision is marked active, and whatever set of revisions
	// the response actually lists, the active one must be the highest
	// revision number IN THAT SAME LIST. A torn read produces a list that
	// already contains a newer revision (created by the concurrent PUT)
	// while "active" still points at the older one read before it landed -
	// exactly what the unmodified handler does here, marking revision 1
	// active even though revision 2 is also present in the list.
	var maxRevision float64
	activeCount := 0
	var activeRevision float64 = -1
	for _, rv := range revs {
		rm, _ := rv.(map[string]any)
		revNum, _ := rm["revision"].(float64)
		if revNum > maxRevision {
			maxRevision = revNum
		}
		if rm["active"] == true {
			activeCount++
			activeRevision = revNum
		}
	}
	if activeCount > 1 {
		t.Fatalf("more than one revision marked active: %v", revs)
	}
	if activeCount == 1 && activeRevision != maxRevision {
		t.Fatalf("revision %v marked active (torn read); want the highest listed revision, %v: %v", activeRevision, maxRevision, revs)
	}
}

// net/http.ServeMux matches by segment, so "show.mode" is a distinct
// literal that can never be swallowed by "GET /api/v1/config/show/{id}",
// the same guard show.active already carries.
func TestShowModeRouteIsNotSwallowedByShowIDRoute(t *testing.T) {
	svc, st, _ := newTestIdentityServiceWithStore(t, fixedClock(testNow))
	admin := mustCreatePrincipal(t, svc, "admin-1", identity.RoleAdmin)
	adminToken := mustIssueToken(t, svc, admin.ID)
	api := New(configTestDeps(svc, st), Options{Clock: fixedClock(testNow), Logger: testLogger()})

	resp, body := doRequest(t, api.Handler, "GET", "/api/v1/config/show.mode",
		map[string]string{"Authorization": "Bearer " + adminToken})
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200; body: %s", resp.StatusCode, body)
	}
	m := decodeMap(t, body)
	if m["kind"] != "show.mode" {
		t.Fatalf("kind = %v, want show.mode (the show/{id} route answered instead)", m["kind"])
	}
}
