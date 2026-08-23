package api

import (
	"context"
	"encoding/json"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/showmeshsystems/showmesh/internal/coordinator/identity"
	"github.com/showmeshsystems/showmesh/internal/coordinator/store"
	"github.com/showmeshsystems/showmesh/pkg/fppidentity"
)

// This file is handler coverage for the playlist definition publication
// contract: FPP-PLUGIN-COORDINATOR-CONTRACTS.md §3.5's refusal table, the
// load-bearing "filed under the coordinator's own hash" property §3.1
// describes, the idempotent-repeat behavior §3.4 step 8 requires, H2
// spec §3's retention rule, and H2 spec §4.1's entries parser against
// real captured fppd output. Mirrors fppobservations_test.go's structure
// and standing rule: every test drives the real handler through a real
// store and a real identity.Service.

type fppPlaylistDefinitionTestSetup struct {
	st  *store.Store
	svc identity.Service
}

func newFPPPlaylistDefinitionTestSetup(t *testing.T, now func() time.Time) *fppPlaylistDefinitionTestSetup {
	t.Helper()
	dir := t.TempDir()
	st, err := store.Open(context.Background(), filepath.Join(dir, "db"), nil, store.WithClock(now))
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(func() { _ = st.Close() })
	svc := identity.NewService(st, now, filepath.Join(dir, "data"), identity.WithLogger(testLogger()))
	return &fppPlaylistDefinitionTestSetup{st: st, svc: svc}
}

func (s *fppPlaylistDefinitionTestSetup) deps() Dependencies {
	return Dependencies{
		Nodes: &fakeNodeLister{}, FPP: &fakeFPPLister{}, Observations: &fakeObservationLister{},
		Events: &fakeEventReader{}, Collectors: &fakeCollectorStatusLister{},
		Identity: s.svc, FPPPlaylistDefinitions: s.st, Config: s.st,
	}
}

// mustBindShowPlaylist writes an active show.playlist revision whose fpp
// binding names (instanceUUID, playlistHash), so tests can exercise the
// "referenced" half of retention and the list response's own Referenced
// column without going through the full show.playlist HTTP surface.
func mustBindShowPlaylist(t *testing.T, st *store.Store, id, instanceUUID, playlistHash string) {
	t.Helper()
	ctx := context.Background()
	if _, err := st.CreateConfigObject(ctx, "show.playlist", id); err != nil {
		t.Fatalf("create show.playlist object: %v", err)
	}
	payload := map[string]any{
		"show": "show-1", "name": id, "runner": "fpp",
		"fpp":     map[string]any{"instanceUuid": instanceUUID, "playlistName": "p", "playlistHash": playlistHash},
		"entries": []any{},
	}
	raw, err := json.Marshal(payload)
	if err != nil {
		t.Fatalf("marshal payload: %v", err)
	}
	rev, err := st.CreateConfigRevision(ctx, store.ConfigRevisionRecord{
		Kind: "show.playlist", ObjectID: id, Revision: 1, PayloadJSON: string(raw), Source: "api",
	})
	if err != nil {
		t.Fatalf("create show.playlist revision: %v", err)
	}
	if _, err := st.ActivateConfigRevision(ctx, "show.playlist", id, rev.Revision); err != nil {
		t.Fatalf("activate show.playlist revision: %v", err)
	}
}

// simpleDefinitionAndHash returns a small, valid playlist definition (as
// raw JSON) and its own canonical SHA-256, so a test can build a request
// body whose declared playlistHash is correct without hand-computing hex.
func simpleDefinitionAndHash(t *testing.T, name string) (json.RawMessage, string) {
	t.Helper()
	def := []byte(`{"name":"` + name + `","mainPlaylist":[{"type":"sequence","sequenceName":"` + name + `.fseq"}],"leadIn":[],"leadOut":[]}`)
	_, hash, err := fppidentity.HashCanonical(def)
	if err != nil {
		t.Fatalf("HashCanonical: %v", err)
	}
	return json.RawMessage(def), hash
}

func fppPlaylistDefinitionPublishBody(t *testing.T, instanceUUID, playlistName string, def json.RawMessage, hash string, capturedAtMillis int64) string {
	t.Helper()
	m := map[string]any{
		"schemaVersion":    1,
		"instanceUuid":     instanceUUID,
		"playlistName":     playlistName,
		"playlistHash":     hash,
		"definition":       def,
		"capturedAtMillis": capturedAtMillis,
	}
	raw, err := json.Marshal(m)
	if err != nil {
		t.Fatalf("marshal publish body: %v", err)
	}
	return string(raw)
}

func newFPPPlaylistDefinitionPublishRequest(t *testing.T, body, bearerToken string) *http.Request {
	t.Helper()
	req := newJSONRequest(t, http.MethodPost, "/api/v1/integrations/fpp/playlist-definitions", body, nil)
	if bearerToken != "" {
		req.Header.Set("Authorization", "Bearer "+bearerToken)
	}
	return req
}

func mustPostPlaylistDefinition(t *testing.T, api *API, body, token string) (*http.Response, map[string]any) {
	t.Helper()
	resp, raw := doRawRequest(t, api.Handler, newFPPPlaylistDefinitionPublishRequest(t, body, token))
	return resp, decodeMap(t, raw)
}

func fppPlaylistDefinitionAuditEntries(t *testing.T, svc identity.Service) []identity.AuditEntry {
	t.Helper()
	entries, err := svc.ListAudit(context.Background(), 0, 100)
	if err != nil {
		t.Fatalf("ListAudit: %v", err)
	}
	var matched []identity.AuditEntry
	for _, e := range entries {
		if e.Action == auditActionFPPPublishPlaylistDefinition {
			matched = append(matched, e)
		}
	}
	return matched
}

// --- authentication and scope ---

func TestFPPPlaylistDefinitionPostRefusedUnauthenticated(t *testing.T) {
	setup := newFPPPlaylistDefinitionTestSetup(t, fixedClock(testNow))
	api := New(setup.deps(), Options{Clock: fixedClock(testNow), Logger: testLogger(), CloseReads: true})

	def, hash := simpleDefinitionAndHash(t, "p1")
	body := fppPlaylistDefinitionPublishBody(t, "instance-1", "p1", def, hash, testNow.UnixMilli())
	resp, _ := doRawRequest(t, api.Handler, newFPPPlaylistDefinitionPublishRequest(t, body, ""))
	if resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("status = %d, want 401", resp.StatusCode)
	}
}

func TestFPPPlaylistDefinitionPostRefusedForbiddenForOperator(t *testing.T) {
	setup := newFPPPlaylistDefinitionTestSetup(t, fixedClock(testNow))
	api := New(setup.deps(), Options{Clock: fixedClock(testNow), Logger: testLogger()})
	operator := mustCreatePrincipal(t, setup.svc, "operator-1", identity.RoleOperator)
	token := mustIssueToken(t, setup.svc, operator.ID)

	def, hash := simpleDefinitionAndHash(t, "p1")
	body := fppPlaylistDefinitionPublishBody(t, "instance-1", "p1", def, hash, testNow.UnixMilli())
	resp, m := mustPostPlaylistDefinition(t, api, body, token)
	if resp.StatusCode != http.StatusForbidden {
		t.Fatalf("status = %d, want 403; body: %v", resp.StatusCode, m)
	}
}

func TestFPPPlaylistDefinitionPostAcceptedForSchedulerRole(t *testing.T) {
	setup := newFPPPlaylistDefinitionTestSetup(t, fixedClock(testNow))
	api := New(setup.deps(), Options{Clock: fixedClock(testNow), Logger: testLogger()})
	scheduler := mustCreatePrincipal(t, setup.svc, "scheduler-bot", identity.RoleScheduler)
	token := mustIssueToken(t, setup.svc, scheduler.ID)

	def, hash := simpleDefinitionAndHash(t, "p1")
	body := fppPlaylistDefinitionPublishBody(t, "instance-1", "p1", def, hash, testNow.UnixMilli())
	resp, m := mustPostPlaylistDefinition(t, api, body, token)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200; body: %v", resp.StatusCode, m)
	}
	if stored, _ := m["stored"].(bool); !stored {
		t.Errorf("stored = %v, want true", m["stored"])
	}
	if idempotent, _ := m["idempotent"].(bool); idempotent {
		t.Errorf("idempotent = %v, want false", m["idempotent"])
	}
	if m["playlistHash"] != hash {
		t.Errorf("playlistHash = %v, want %v", m["playlistHash"], hash)
	}
}

// --- §3.5 refusal vocabulary ---

func TestFPPPlaylistDefinitionPostRefusedOversizedBody413(t *testing.T) {
	setup := newFPPPlaylistDefinitionTestSetup(t, fixedClock(testNow))
	api := New(setup.deps(), Options{Clock: fixedClock(testNow), Logger: testLogger()})
	scheduler := mustCreatePrincipal(t, setup.svc, "scheduler-bot", identity.RoleScheduler)
	token := mustIssueToken(t, setup.svc, scheduler.ID)

	filler := make([]byte, maxFPPPlaylistDefinitionRequestBodyBytes+1024)
	for i := range filler {
		filler[i] = 'a'
	}
	body := `{"schemaVersion":1,"instanceUuid":"instance-1","playlistName":"` + string(filler) +
		`","playlistHash":"` + playlistHash64 + `","definition":{},"capturedAtMillis":1}`
	resp, m := mustPostPlaylistDefinition(t, api, body, token)
	if resp.StatusCode != http.StatusRequestEntityTooLarge {
		t.Fatalf("status = %d, want 413; body: %v", resp.StatusCode, m)
	}
	if m["type"] != ProblemTypeResolumeCompositionTooLarge {
		t.Errorf("type = %v, want %v", m["type"], ProblemTypeResolumeCompositionTooLarge)
	}
}

func TestFPPPlaylistDefinitionPostRefusedMalformedBody400(t *testing.T) {
	setup := newFPPPlaylistDefinitionTestSetup(t, fixedClock(testNow))
	api := New(setup.deps(), Options{Clock: fixedClock(testNow), Logger: testLogger()})
	scheduler := mustCreatePrincipal(t, setup.svc, "scheduler-bot", identity.RoleScheduler)
	token := mustIssueToken(t, setup.svc, scheduler.ID)

	resp, m := mustPostPlaylistDefinition(t, api, `{not json`, token)
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400; body: %v", resp.StatusCode, m)
	}
	if m["type"] != ProblemTypeInvalidParameter {
		t.Errorf("type = %v, want %v", m["type"], ProblemTypeInvalidParameter)
	}
}

func TestFPPPlaylistDefinitionPostRefusedDuplicateMemberName400(t *testing.T) {
	setup := newFPPPlaylistDefinitionTestSetup(t, fixedClock(testNow))
	api := New(setup.deps(), Options{Clock: fixedClock(testNow), Logger: testLogger()})
	scheduler := mustCreatePrincipal(t, setup.svc, "scheduler-bot", identity.RoleScheduler)
	token := mustIssueToken(t, setup.svc, scheduler.ID)

	// encoding/json.Decoder with DisallowUnknownFields alone accepts a
	// duplicate top-level member (last value wins); the canonicalizing
	// pass over the raw body is what must catch this one.
	body := `{"schemaVersion":1,"schemaVersion":1,"instanceUuid":"instance-1","playlistName":"p",` +
		`"playlistHash":"` + playlistHash64 + `","definition":{},"capturedAtMillis":1}`
	resp, m := mustPostPlaylistDefinition(t, api, body, token)
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400; body: %v", resp.StatusCode, m)
	}
	if m["type"] != ProblemTypeInvalidParameter {
		t.Errorf("type = %v, want %v", m["type"], ProblemTypeInvalidParameter)
	}
}

func TestFPPPlaylistDefinitionPostRefusedUnsupportedSchemaVersion400(t *testing.T) {
	setup := newFPPPlaylistDefinitionTestSetup(t, fixedClock(testNow))
	api := New(setup.deps(), Options{Clock: fixedClock(testNow), Logger: testLogger()})
	scheduler := mustCreatePrincipal(t, setup.svc, "scheduler-bot", identity.RoleScheduler)
	token := mustIssueToken(t, setup.svc, scheduler.ID)

	def, hash := simpleDefinitionAndHash(t, "p1")
	m2 := map[string]any{
		"schemaVersion": 2, "instanceUuid": "instance-1", "playlistName": "p1",
		"playlistHash": hash, "definition": def, "capturedAtMillis": 1,
	}
	raw, _ := json.Marshal(m2)
	resp, m := mustPostPlaylistDefinition(t, api, string(raw), token)
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400; body: %v", resp.StatusCode, m)
	}
	if m["type"] != ProblemTypeUnsupportedDefinitionSchemaVersion {
		t.Errorf("type = %v, want %v", m["type"], ProblemTypeUnsupportedDefinitionSchemaVersion)
	}
}

func TestFPPPlaylistDefinitionPostRefusedMissingIdentityField400(t *testing.T) {
	setup := newFPPPlaylistDefinitionTestSetup(t, fixedClock(testNow))
	api := New(setup.deps(), Options{Clock: fixedClock(testNow), Logger: testLogger()})
	scheduler := mustCreatePrincipal(t, setup.svc, "scheduler-bot", identity.RoleScheduler)
	token := mustIssueToken(t, setup.svc, scheduler.ID)

	def, hash := simpleDefinitionAndHash(t, "p1")
	m2 := map[string]any{
		"schemaVersion": 1, "instanceUuid": "", "playlistName": "p1",
		"playlistHash": hash, "definition": def, "capturedAtMillis": 1,
	}
	raw, _ := json.Marshal(m2)
	resp, m := mustPostPlaylistDefinition(t, api, string(raw), token)
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400; body: %v", resp.StatusCode, m)
	}
	if m["type"] != ProblemTypeInvalidParameter {
		t.Errorf("type = %v, want %v", m["type"], ProblemTypeInvalidParameter)
	}
}

func TestFPPPlaylistDefinitionPostRefusedMalformedHashShape400(t *testing.T) {
	setup := newFPPPlaylistDefinitionTestSetup(t, fixedClock(testNow))
	api := New(setup.deps(), Options{Clock: fixedClock(testNow), Logger: testLogger()})
	scheduler := mustCreatePrincipal(t, setup.svc, "scheduler-bot", identity.RoleScheduler)
	token := mustIssueToken(t, setup.svc, scheduler.ID)

	def, _ := simpleDefinitionAndHash(t, "p1")
	body := fppPlaylistDefinitionPublishBody(t, "instance-1", "p1", def, "not-a-hash", 1)
	resp, m := mustPostPlaylistDefinition(t, api, body, token)
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400; body: %v", resp.StatusCode, m)
	}
	if m["type"] != ProblemTypeInvalidParameter {
		t.Errorf("type = %v, want %v", m["type"], ProblemTypeInvalidParameter)
	}
}

// TestFPPPlaylistDefinitionPostRefusedHashMismatch400 is contract §3.4
// step 7, the load-bearing refusal: a declared playlistHash that
// disagrees with the coordinator's own re-canonicalization.
func TestFPPPlaylistDefinitionPostRefusedHashMismatch400(t *testing.T) {
	setup := newFPPPlaylistDefinitionTestSetup(t, fixedClock(testNow))
	api := New(setup.deps(), Options{Clock: fixedClock(testNow), Logger: testLogger()})
	scheduler := mustCreatePrincipal(t, setup.svc, "scheduler-bot", identity.RoleScheduler)
	token := mustIssueToken(t, setup.svc, scheduler.ID)

	def, _ := simpleDefinitionAndHash(t, "p1")
	body := fppPlaylistDefinitionPublishBody(t, "instance-1", "p1", def, playlistHash64, 1)
	resp, m := mustPostPlaylistDefinition(t, api, body, token)
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400; body: %v", resp.StatusCode, m)
	}
	if m["type"] != ProblemTypeDefinitionHashMismatch {
		t.Errorf("type = %v, want %v", m["type"], ProblemTypeDefinitionHashMismatch)
	}

	if _, err := setup.st.GetFPPPlaylistDefinition(context.Background(), "instance-1", playlistHash64); err == nil {
		t.Error("a hash-mismatched definition must not be stored under the caller's declared hash")
	}
}

// TestFPPPlaylistDefinitionPostRefusedNegativeCapturedAtMillis400 is
// review fix item 8's first untested §3.4 step 6 case.
func TestFPPPlaylistDefinitionPostRefusedNegativeCapturedAtMillis400(t *testing.T) {
	setup := newFPPPlaylistDefinitionTestSetup(t, fixedClock(testNow))
	api := New(setup.deps(), Options{Clock: fixedClock(testNow), Logger: testLogger()})
	scheduler := mustCreatePrincipal(t, setup.svc, "scheduler-bot", identity.RoleScheduler)
	token := mustIssueToken(t, setup.svc, scheduler.ID)

	def, hash := simpleDefinitionAndHash(t, "p1")
	body := fppPlaylistDefinitionPublishBody(t, "instance-1", "p1", def, hash, -1)
	resp, m := mustPostPlaylistDefinition(t, api, body, token)
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400; body: %v", resp.StatusCode, m)
	}
	if m["type"] != ProblemTypeInvalidParameter {
		t.Errorf("type = %v, want %v", m["type"], ProblemTypeInvalidParameter)
	}
}

// TestFPPPlaylistDefinitionPostRefusedDefinitionNotAnObject400 is review
// fix item 8's second case: definition present but a JSON array, not an
// object.
func TestFPPPlaylistDefinitionPostRefusedDefinitionNotAnObject400(t *testing.T) {
	setup := newFPPPlaylistDefinitionTestSetup(t, fixedClock(testNow))
	api := New(setup.deps(), Options{Clock: fixedClock(testNow), Logger: testLogger()})
	scheduler := mustCreatePrincipal(t, setup.svc, "scheduler-bot", identity.RoleScheduler)
	token := mustIssueToken(t, setup.svc, scheduler.ID)

	body := `{"schemaVersion":1,"instanceUuid":"instance-1","playlistName":"p1",` +
		`"playlistHash":"` + playlistHash64 + `","definition":[1,2,3],"capturedAtMillis":1}`
	resp, m := mustPostPlaylistDefinition(t, api, body, token)
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400; body: %v", resp.StatusCode, m)
	}
	if m["type"] != ProblemTypeInvalidParameter {
		t.Errorf("type = %v, want %v", m["type"], ProblemTypeInvalidParameter)
	}
}

// TestFPPPlaylistDefinitionPostRefusedDefinitionAbsent400 is review fix
// item 8's third case: definition missing entirely, not merely empty.
func TestFPPPlaylistDefinitionPostRefusedDefinitionAbsent400(t *testing.T) {
	setup := newFPPPlaylistDefinitionTestSetup(t, fixedClock(testNow))
	api := New(setup.deps(), Options{Clock: fixedClock(testNow), Logger: testLogger()})
	scheduler := mustCreatePrincipal(t, setup.svc, "scheduler-bot", identity.RoleScheduler)
	token := mustIssueToken(t, setup.svc, scheduler.ID)

	body := `{"schemaVersion":1,"instanceUuid":"instance-1","playlistName":"p1",` +
		`"playlistHash":"` + playlistHash64 + `","capturedAtMillis":1}`
	resp, m := mustPostPlaylistDefinition(t, api, body, token)
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400; body: %v", resp.StatusCode, m)
	}
	if m["type"] != ProblemTypeInvalidParameter {
		t.Errorf("type = %v, want %v", m["type"], ProblemTypeInvalidParameter)
	}
}

// TestFPPPlaylistDefinitionPostRefusedInvalidUTF8IsAudited is review fix
// items 4 and 8 together: invalid UTF-8 inside "definition" is refused by
// the whole-body canonicalize check that runs before schemaVersion is
// even checked (item 4's deliberate deviation), and, unlike contract
// §3.4's own "step 5 onward" audit line, IS audited anyway.
func TestFPPPlaylistDefinitionPostRefusedInvalidUTF8IsAudited(t *testing.T) {
	setup := newFPPPlaylistDefinitionTestSetup(t, fixedClock(testNow))
	api := New(setup.deps(), Options{Clock: fixedClock(testNow), Logger: testLogger()})
	scheduler := mustCreatePrincipal(t, setup.svc, "scheduler-bot", identity.RoleScheduler)
	token := mustIssueToken(t, setup.svc, scheduler.ID)

	body := "{\"schemaVersion\":1,\"instanceUuid\":\"instance-1\",\"playlistName\":\"p1\"," +
		"\"playlistHash\":\"" + playlistHash64 + "\",\"definition\":{\"mainPlaylist\":[{\"sequenceName\":\"a\xffb\"}]}," +
		"\"capturedAtMillis\":1}"
	resp, m := mustPostPlaylistDefinition(t, api, body, token)
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400; body: %v", resp.StatusCode, m)
	}
	if m["type"] != ProblemTypeInvalidParameter {
		t.Errorf("type = %v, want %v", m["type"], ProblemTypeInvalidParameter)
	}
	entries := fppPlaylistDefinitionAuditEntries(t, setup.svc)
	if len(entries) != 1 {
		t.Fatalf("audit entries for %s = %d, want 1 (item 4's deliberate deviation)", auditActionFPPPublishPlaylistDefinition, len(entries))
	}
}

// TestFPPPlaylistDefinitionPostRefusedExcessiveNestingIsAudited is review
// fix items 4 and 8's excessive-nesting case, and proves item 4's other
// claim: a definition array nested to the exact depth limit is a BODY one
// level deeper (because it sits inside the request object's "definition"
// member), so step 7's own per-definition canonicalize/hash check never
// gets a chance to be the one that refuses it: the whole-body check
// above (audited) always gets there first.
func TestFPPPlaylistDefinitionPostRefusedExcessiveNestingIsAudited(t *testing.T) {
	setup := newFPPPlaylistDefinitionTestSetup(t, fixedClock(testNow))
	api := New(setup.deps(), Options{Clock: fixedClock(testNow), Logger: testLogger()})
	scheduler := mustCreatePrincipal(t, setup.svc, "scheduler-bot", identity.RoleScheduler)
	token := mustIssueToken(t, setup.svc, scheduler.ID)

	// 250 levels comfortably exceeds fppidentity's own 200-level maxDepth
	// (pkg/fppidentity/canonical_test.go's TestNestingDepthBoundary), and
	// does so however the embedding is counted.
	nestedArray := strings.Repeat("[", 250) + "1" + strings.Repeat("]", 250)
	body := `{"schemaVersion":1,"instanceUuid":"instance-1","playlistName":"p1",` +
		`"playlistHash":"` + playlistHash64 + `","definition":` + nestedArray + `,"capturedAtMillis":1}`
	resp, m := mustPostPlaylistDefinition(t, api, body, token)
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400; body: %v", resp.StatusCode, m)
	}
	if m["type"] != ProblemTypeInvalidParameter {
		t.Errorf("type = %v, want %v", m["type"], ProblemTypeInvalidParameter)
	}
	entries := fppPlaylistDefinitionAuditEntries(t, setup.svc)
	if len(entries) != 1 {
		t.Fatalf("audit entries for %s = %d, want 1 (item 4's deliberate deviation)", auditActionFPPPublishPlaylistDefinition, len(entries))
	}
}

// TestFPPPlaylistDefinitionPostRefusesUnauthenticatedBeforeParsingBody is
// review fix item 8's last case: a missing credential refuses before an
// oversized or malformed body is ever read, let alone parsed; steps 1-2
// run before step 3's size bound and step 4's decode.
func TestFPPPlaylistDefinitionPostRefusesUnauthenticatedBeforeParsingBody(t *testing.T) {
	setup := newFPPPlaylistDefinitionTestSetup(t, fixedClock(testNow))
	api := New(setup.deps(), Options{Clock: fixedClock(testNow), Logger: testLogger(), CloseReads: true})

	// Both oversized (over maxFPPPlaylistDefinitionRequestBodyBytes) AND
	// malformed (not valid JSON): if either check ran before
	// authentication, this body would trip it instead of 401.
	oversizedMalformed := strings.Repeat("{not json", maxFPPPlaylistDefinitionRequestBodyBytes)
	resp, m := mustPostPlaylistDefinition(t, api, oversizedMalformed, "")
	if resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("status = %d, want 401 (before body is parsed); body: %v", resp.StatusCode, m)
	}
}

// TestFPPPlaylistDefinitionRefusalsAreAuditedWithReason exercises §3.4's
// "every refusal from step 5 onward is audited," mirroring
// fppobservations_test.go's identical structure for its own sibling rule.
func TestFPPPlaylistDefinitionRefusalsAreAuditedWithReason(t *testing.T) {
	setup := newFPPPlaylistDefinitionTestSetup(t, fixedClock(testNow))
	api := New(setup.deps(), Options{Clock: fixedClock(testNow), Logger: testLogger()})
	scheduler := mustCreatePrincipal(t, setup.svc, "scheduler-bot", identity.RoleScheduler)
	token := mustIssueToken(t, setup.svc, scheduler.ID)

	def, _ := simpleDefinitionAndHash(t, "p1")
	body := fppPlaylistDefinitionPublishBody(t, "instance-audit-1", "p1", def, playlistHash64, 1)
	resp, m := mustPostPlaylistDefinition(t, api, body, token)
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400; body: %v", resp.StatusCode, m)
	}

	entries := fppPlaylistDefinitionAuditEntries(t, setup.svc)
	if len(entries) != 1 {
		t.Fatalf("audit entries = %d, want exactly 1: %+v", len(entries), entries)
	}
	if entries[0].Kind != identity.AuditOutcome {
		t.Errorf("audit Kind = %v, want %v", entries[0].Kind, identity.AuditOutcome)
	}
	if entries[0].OutcomeReason == "" {
		t.Error("audit OutcomeReason is empty, want a stated reason")
	}
}

// --- storage, idempotency, and hash-filing ---

// TestFPPPlaylistDefinitionStoredUnderRecomputedHashNotRawBytes checks
// H2 spec §3's "files it under the hash it computed itself" property
// concretely: the stored row's definition_json is the CANONICAL form
// (sorted members, no insignificant whitespace), not whatever byte
// sequence happened to arrive on the wire, even though the two hash to
// the same value.
func TestFPPPlaylistDefinitionStoredUnderRecomputedHashNotRawBytes(t *testing.T) {
	setup := newFPPPlaylistDefinitionTestSetup(t, fixedClock(testNow))
	api := New(setup.deps(), Options{Clock: fixedClock(testNow), Logger: testLogger()})
	scheduler := mustCreatePrincipal(t, setup.svc, "scheduler-bot", identity.RoleScheduler)
	token := mustIssueToken(t, setup.svc, scheduler.ID)

	// Deliberately unsorted member order and insignificant whitespace in
	// the wire definition; its OWN canonicalization is what the declared
	// hash must be computed from (as a real plugin would do).
	rawDef := json.RawMessage(`{  "leadOut": [], "name":   "p1", "mainPlaylist":[{"sequenceName":"p1.fseq","type":"sequence"}], "leadIn":[] }`)
	_, hash, err := fppidentity.HashCanonical(rawDef)
	if err != nil {
		t.Fatalf("HashCanonical: %v", err)
	}
	body := fppPlaylistDefinitionPublishBody(t, "instance-1", "p1", rawDef, hash, 1)
	resp, m := mustPostPlaylistDefinition(t, api, body, token)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200; body: %v", resp.StatusCode, m)
	}

	rec, err := setup.st.GetFPPPlaylistDefinition(context.Background(), "instance-1", hash)
	if err != nil {
		t.Fatalf("GetFPPPlaylistDefinition: %v", err)
	}
	canonical, _, err := fppidentity.HashCanonical(rawDef)
	if err != nil {
		t.Fatalf("HashCanonical: %v", err)
	}
	if rec.DefinitionJSON != string(canonical) {
		t.Errorf("stored DefinitionJSON = %q, want the canonical form %q (not the raw wire bytes)", rec.DefinitionJSON, string(canonical))
	}
	if rec.DefinitionJSON == string(rawDef) {
		t.Error("stored DefinitionJSON equals the raw wire bytes verbatim; the fixture should have exercised a real reordering")
	}
}

// TestFPPPlaylistDefinitionIdempotentRepeatStoresNothingAndKeepsFirstProvenance
// is contract §3.4 step 8.
func TestFPPPlaylistDefinitionIdempotentRepeatStoresNothingAndKeepsFirstProvenance(t *testing.T) {
	setup := newFPPPlaylistDefinitionTestSetup(t, fixedClock(testNow))
	api := New(setup.deps(), Options{Clock: fixedClock(testNow), Logger: testLogger()})
	scheduler := mustCreatePrincipal(t, setup.svc, "scheduler-bot", identity.RoleScheduler)
	token := mustIssueToken(t, setup.svc, scheduler.ID)

	def, hash := simpleDefinitionAndHash(t, "p1")
	first := fppPlaylistDefinitionPublishBody(t, "instance-1", "First Name", def, hash, 1000)
	resp, m := mustPostPlaylistDefinition(t, api, first, token)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("first post: status = %d, want 200; body: %v", resp.StatusCode, m)
	}
	if stored, _ := m["stored"].(bool); !stored {
		t.Fatalf("first post: stored = %v, want true", m["stored"])
	}

	repeat := fppPlaylistDefinitionPublishBody(t, "instance-1", "Second Name", def, hash, 2000)
	resp, m = mustPostPlaylistDefinition(t, api, repeat, token)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("repeat post: status = %d, want 200; body: %v", resp.StatusCode, m)
	}
	if stored, _ := m["stored"].(bool); stored {
		t.Errorf("repeat post: stored = %v, want false", m["stored"])
	}
	if idempotent, _ := m["idempotent"].(bool); !idempotent {
		t.Errorf("repeat post: idempotent = %v, want true", m["idempotent"])
	}

	rec, err := setup.st.GetFPPPlaylistDefinition(context.Background(), "instance-1", hash)
	if err != nil {
		t.Fatalf("GetFPPPlaylistDefinition: %v", err)
	}
	if rec.PlaylistName != "First Name" {
		t.Errorf("PlaylistName = %q, want %q (first report keeps provenance)", rec.PlaylistName, "First Name")
	}

	entries := fppPlaylistDefinitionAuditEntries(t, setup.svc)
	if len(entries) != 1 {
		t.Fatalf("audit entries = %d, want exactly 1 (idempotent repeat is not audited): %+v", len(entries), entries)
	}
}

// --- retention (H2 spec §3) ---

// TestFPPPlaylistDefinitionRetentionNeverEvictsAReferencedDefinition also
// covers review fix item 10: retention orders by received_at DESC (store's
// pruneFPPPlaylistDefinitions), so a fixed clock gives every unreferenced
// row the SAME received_at and this test's old count-only assertion
// ("17 survive") would pass however SQLite happened to break that tie;
// it would never actually prove the NEWEST 16 are the ones kept. An
// incrementing clock gives each POST a distinct, later received_at, so
// the test can name exactly which hashes must survive (the 16 posted
// LAST) and which must not (the 4 posted first, after the referenced
// seed).
func TestFPPPlaylistDefinitionRetentionNeverEvictsAReferencedDefinition(t *testing.T) {
	setup := newFPPPlaylistDefinitionTestSetup(t, fixedClock(testNow))
	tick := 0
	incrementingClock := func() time.Time {
		tick++
		return testNow.Add(time.Duration(tick) * time.Second)
	}
	api := New(setup.deps(), Options{Clock: incrementingClock, Logger: testLogger()})
	scheduler := mustCreatePrincipal(t, setup.svc, "scheduler-bot", identity.RoleScheduler)
	token := mustIssueToken(t, setup.svc, scheduler.ID)

	def0, hash0 := simpleDefinitionAndHash(t, "p0")
	body0 := fppPlaylistDefinitionPublishBody(t, "instance-1", "p0", def0, hash0, 1)
	if resp, m := mustPostPlaylistDefinition(t, api, body0, token); resp.StatusCode != http.StatusOK {
		t.Fatalf("seed referenced post: status = %d, want 200; body: %v", resp.StatusCode, m)
	}
	mustBindShowPlaylist(t, setup.st, "playlist-1", "instance-1", hash0)

	// 20 more unreferenced definitions, posted in order, each with a
	// strictly later received_at than the last (incrementingClock above),
	// exceeding the 16-newest bound.
	hashes := make([]string, 20)
	for i := 0; i < 20; i++ {
		name := "p" + string(rune('A'+i))
		def, hash := simpleDefinitionAndHash(t, name)
		hashes[i] = hash
		body := fppPlaylistDefinitionPublishBody(t, "instance-1", name, def, hash, int64(1000+i))
		if resp, m := mustPostPlaylistDefinition(t, api, body, token); resp.StatusCode != http.StatusOK {
			t.Fatalf("post %d: status = %d, want 200; body: %v", i, resp.StatusCode, m)
		}
	}

	if _, err := setup.st.GetFPPPlaylistDefinition(context.Background(), "instance-1", hash0); err != nil {
		t.Errorf("referenced definition was evicted: %v", err)
	}

	// The 4 posted FIRST (oldest) must be pruned; the 16 posted LAST
	// (newest) must survive: the actual claim "newest 16," not merely
	// "16 survive."
	for i := 0; i < 4; i++ {
		if _, err := setup.st.GetFPPPlaylistDefinition(context.Background(), "instance-1", hashes[i]); err == nil {
			t.Errorf("oldest unreferenced definition %d (hash %s) survived retention, want pruned", i, hashes[i])
		}
	}
	for i := 4; i < 20; i++ {
		if _, err := setup.st.GetFPPPlaylistDefinition(context.Background(), "instance-1", hashes[i]); err != nil {
			t.Errorf("newest unreferenced definition %d (hash %s) was pruned, want kept: %v", i, hashes[i], err)
		}
	}

	all, err := setup.st.ListFPPPlaylistDefinitionsByInstance(context.Background(), "instance-1")
	if err != nil {
		t.Fatalf("ListFPPPlaylistDefinitionsByInstance: %v", err)
	}
	// 16 newest unreferenced + the 1 referenced (however old) = 17.
	if len(all) != 17 {
		t.Errorf("len(all) = %d, want 17 (16 newest unreferenced + 1 referenced)", len(all))
	}
}

// --- reads ---

func TestFPPPlaylistDefinitionListReportsMetadataNewestFirstAndReferenced(t *testing.T) {
	setup := newFPPPlaylistDefinitionTestSetup(t, fixedClock(testNow))
	api := New(setup.deps(), Options{Clock: fixedClock(testNow), Logger: testLogger()})
	scheduler := mustCreatePrincipal(t, setup.svc, "scheduler-bot", identity.RoleScheduler)
	token := mustIssueToken(t, setup.svc, scheduler.ID)

	defA, hashA := simpleDefinitionAndHash(t, "pA")
	defB, hashB := simpleDefinitionAndHash(t, "pB")
	if resp, m := mustPostPlaylistDefinition(t, api, fppPlaylistDefinitionPublishBody(t, "instance-1", "pA", defA, hashA, 1), token); resp.StatusCode != http.StatusOK {
		t.Fatalf("post A: status = %d; body %v", resp.StatusCode, m)
	}
	if resp, m := mustPostPlaylistDefinition(t, api, fppPlaylistDefinitionPublishBody(t, "instance-1", "pB", defB, hashB, 2), token); resp.StatusCode != http.StatusOK {
		t.Fatalf("post B: status = %d; body %v", resp.StatusCode, m)
	}
	mustBindShowPlaylist(t, setup.st, "playlist-b", "instance-1", hashB)

	req := newJSONRequest(t, http.MethodGet, "/api/v1/integrations/fpp/playlist-definitions", "", nil)
	req.Header.Set("Authorization", "Bearer "+token)
	resp, raw := doRawRequest(t, api.Handler, req)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200; body: %s", resp.StatusCode, raw)
	}
	var listResp struct {
		Definitions []struct {
			PlaylistHash string `json:"playlistHash"`
			EntryCount   int    `json:"entryCount"`
			Referenced   bool   `json:"referenced"`
		} `json:"definitions"`
	}
	if err := json.Unmarshal(raw, &listResp); err != nil {
		t.Fatalf("decode: %v; body: %s", err, raw)
	}
	if len(listResp.Definitions) != 2 {
		t.Fatalf("len(Definitions) = %d, want 2", len(listResp.Definitions))
	}
	if listResp.Definitions[0].PlaylistHash != hashB {
		t.Errorf("Definitions[0].PlaylistHash = %q, want %q (newest received first)", listResp.Definitions[0].PlaylistHash, hashB)
	}
	if !listResp.Definitions[0].Referenced {
		t.Error("Definitions[0].Referenced = false, want true (bound by playlist-b)")
	}
	if listResp.Definitions[1].Referenced {
		t.Error("Definitions[1].Referenced = true, want false (unbound)")
	}
	for _, d := range listResp.Definitions {
		if d.EntryCount != 1 {
			t.Errorf("EntryCount for %s = %d, want 1", d.PlaylistHash, d.EntryCount)
		}
	}
}

func TestFPPPlaylistDefinitionGetReturns404ForUnknownKey(t *testing.T) {
	setup := newFPPPlaylistDefinitionTestSetup(t, fixedClock(testNow))
	api := New(setup.deps(), Options{Clock: fixedClock(testNow), Logger: testLogger()})
	scheduler := mustCreatePrincipal(t, setup.svc, "scheduler-bot", identity.RoleScheduler)
	token := mustIssueToken(t, setup.svc, scheduler.ID)

	req := newJSONRequest(t, http.MethodGet, "/api/v1/integrations/fpp/playlist-definitions/instance-x/"+playlistHash64, "", nil)
	req.Header.Set("Authorization", "Bearer "+token)
	resp, raw := doRawRequest(t, api.Handler, req)
	if resp.StatusCode != http.StatusNotFound {
		t.Fatalf("status = %d, want 404; body: %s", resp.StatusCode, raw)
	}
	m := decodeMap(t, raw)
	if m["type"] != ProblemTypeResourceNotFound {
		t.Errorf("type = %v, want %v", m["type"], ProblemTypeResourceNotFound)
	}
}

func TestFPPPlaylistDefinitionGetReturnsStoredDefinition(t *testing.T) {
	setup := newFPPPlaylistDefinitionTestSetup(t, fixedClock(testNow))
	api := New(setup.deps(), Options{Clock: fixedClock(testNow), Logger: testLogger()})
	scheduler := mustCreatePrincipal(t, setup.svc, "scheduler-bot", identity.RoleScheduler)
	token := mustIssueToken(t, setup.svc, scheduler.ID)

	def, hash := simpleDefinitionAndHash(t, "p1")
	if resp, m := mustPostPlaylistDefinition(t, api, fppPlaylistDefinitionPublishBody(t, "instance-1", "p1", def, hash, 1), token); resp.StatusCode != http.StatusOK {
		t.Fatalf("post: status = %d; body %v", resp.StatusCode, m)
	}

	req := newJSONRequest(t, http.MethodGet, "/api/v1/integrations/fpp/playlist-definitions/instance-1/"+hash, "", nil)
	req.Header.Set("Authorization", "Bearer "+token)
	resp, raw := doRawRequest(t, api.Handler, req)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200; body: %s", resp.StatusCode, raw)
	}
	m := decodeMap(t, raw)
	if m["playlistName"] != "p1" || m["playlistHash"] != hash {
		t.Errorf("body = %v, want playlistName=p1 playlistHash=%s", m, hash)
	}
}

// --- entries preview (H2 spec §4 step 2 / §4.1) ---

func TestFPPPlaylistDefinitionEntriesEmptySections(t *testing.T) {
	setup := newFPPPlaylistDefinitionTestSetup(t, fixedClock(testNow))
	api := New(setup.deps(), Options{Clock: fixedClock(testNow), Logger: testLogger()})
	scheduler := mustCreatePrincipal(t, setup.svc, "scheduler-bot", identity.RoleScheduler)
	token := mustIssueToken(t, setup.svc, scheduler.ID)

	def, hash := simpleDefinitionAndHash(t, "p1")
	if resp, m := mustPostPlaylistDefinition(t, api, fppPlaylistDefinitionPublishBody(t, "instance-1", "p1", def, hash, 1), token); resp.StatusCode != http.StatusOK {
		t.Fatalf("post: status = %d; body %v", resp.StatusCode, m)
	}

	req := newJSONRequest(t, http.MethodGet, "/api/v1/integrations/fpp/playlist-definitions/instance-1/"+hash+"/entries", "", nil)
	req.Header.Set("Authorization", "Bearer "+token)
	resp, raw := doRawRequest(t, api.Handler, req)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200; body: %s", resp.StatusCode, raw)
	}
	var entriesResp v1FPPPlaylistDefinitionEntriesResponseShadow
	if err := json.Unmarshal(raw, &entriesResp); err != nil {
		t.Fatalf("decode: %v; body: %s", err, raw)
	}
	if len(entriesResp.Entries) != 1 {
		t.Fatalf("len(Entries) = %d, want 1", len(entriesResp.Entries))
	}
	e := entriesResp.Entries[0]
	if e.Section != "mainPlaylist" || e.Position != 0 || e.SequenceName != "p1.fseq" {
		t.Errorf("Entries[0] = %+v, want section=mainPlaylist position=0 sequenceName=p1.fseq", e)
	}
}

// v1FPPPlaylistDefinitionEntriesResponseShadow mirrors
// v1.FPPPlaylistDefinitionEntriesResponse for tests in this package that
// decode a raw response body without importing the v1 package purely for
// its own struct tags (this file already avoids that import elsewhere).
type v1FPPPlaylistDefinitionEntriesResponseShadow struct {
	Entries []struct {
		Section      string `json:"section"`
		Position     int    `json:"position"`
		Type         string `json:"type"`
		SequenceName string `json:"sequenceName"`
		MediaName    string `json:"mediaName"`
	} `json:"entries"`
}

// --- parser unit tests against real captured fppd output ---

func mustReadCapturedFixture(t *testing.T, name string) string {
	t.Helper()
	b, err := os.ReadFile(filepath.Join("..", "..", "..", "bench", "fpp-multisync", "captures", "trackf-f0", name))
	if err != nil {
		t.Fatalf("read fixture %s: %v", name, err)
	}
	return string(b)
}

func TestParseFPPPlaylistDefinitionEntriesAgainstRealCapturedSequenceOnly(t *testing.T) {
	def := mustReadCapturedFixture(t, "06-playlist-def.json")
	entries, err := parseFPPPlaylistDefinitionEntries(def)
	if err != nil {
		t.Fatalf("parseFPPPlaylistDefinitionEntries: %v", err)
	}
	if len(entries) != 1 {
		t.Fatalf("len(entries) = %d, want 1: %+v", len(entries), entries)
	}
	want := struct {
		section, seqName, mediaName, typ string
		position                         int
	}{"mainPlaylist", "trackf-resting.fseq", "", "sequence", 0}
	got := entries[0]
	if got.Section != want.section || got.Position != want.position || got.SequenceName != want.seqName ||
		got.MediaName != want.mediaName || got.Type != want.typ {
		t.Errorf("entries[0] = %+v, want section=%s position=%d type=%s sequenceName=%s mediaName=%q",
			got, want.section, want.position, want.typ, want.seqName, want.mediaName)
	}
}

func TestParseFPPPlaylistDefinitionEntriesAgainstRealCapturedWithAudio(t *testing.T) {
	def := mustReadCapturedFixture(t, "09-playlist-with-audio-def.json")
	entries, err := parseFPPPlaylistDefinitionEntries(def)
	if err != nil {
		t.Fatalf("parseFPPPlaylistDefinitionEntries: %v", err)
	}
	if len(entries) != 1 {
		t.Fatalf("len(entries) = %d, want 1: %+v", len(entries), entries)
	}
	got := entries[0]
	if got.Section != "mainPlaylist" || got.Position != 0 || got.Type != "both" ||
		got.SequenceName != "trackf-resting.fseq" || got.MediaName != "trackf-fake.mp3" {
		t.Errorf("entries[0] = %+v, want section=mainPlaylist position=0 type=both "+
			"sequenceName=trackf-resting.fseq mediaName=trackf-fake.mp3", got)
	}
}

func TestParseFPPPlaylistDefinitionEntriesAgainstRealCapturedVariant(t *testing.T) {
	def := mustReadCapturedFixture(t, "80-variantA-playlist-def.json")
	entries, err := parseFPPPlaylistDefinitionEntries(def)
	if err != nil {
		t.Fatalf("parseFPPPlaylistDefinitionEntries: %v", err)
	}
	if len(entries) != 1 || entries[0].SequenceName != "trackf-resting-variantA.fseq" {
		t.Errorf("entries = %+v, want exactly one entry naming trackf-resting-variantA.fseq", entries)
	}
}

func TestParseFPPPlaylistDefinitionEntriesLeadInAndLeadOut(t *testing.T) {
	def := `{"name":"x","leadIn":[{"type":"sequence","sequenceName":"in.fseq"}],` +
		`"mainPlaylist":[{"type":"sequence","sequenceName":"main.fseq"}],` +
		`"leadOut":[{"type":"sequence","sequenceName":"out.fseq"}]}`
	entries, err := parseFPPPlaylistDefinitionEntries(def)
	if err != nil {
		t.Fatalf("parseFPPPlaylistDefinitionEntries: %v", err)
	}
	if len(entries) != 3 {
		t.Fatalf("len(entries) = %d, want 3: %+v", len(entries), entries)
	}
	// H2 spec §4 step 2: leadIn, mainPlaylist, leadOut, in that order,
	// each positioned from zero independently.
	wantOrder := []struct {
		section, name string
	}{{"leadIn", "in.fseq"}, {"mainPlaylist", "main.fseq"}, {"leadOut", "out.fseq"}}
	for i, w := range wantOrder {
		if entries[i].Section != w.section || entries[i].Position != 0 || entries[i].SequenceName != w.name {
			t.Errorf("entries[%d] = %+v, want section=%s position=0 sequenceName=%s", i, entries[i], w.section, w.name)
		}
	}
}

// TestParseFPPPlaylistDefinitionEntriesDoesNotRequireOtherMembersOrFailOnUnrecognizedOnes
// is H2 spec §4.1's own text: "does not require any other member and
// does not fail on members it does not recognize."
func TestParseFPPPlaylistDefinitionEntriesDoesNotRequireOtherMembersOrFailOnUnrecognizedOnes(t *testing.T) {
	def := `{"name":"x","mainPlaylist":[{"someFutureField":{"nested":true},"enabled":1,"playOnce":0}]}`
	entries, err := parseFPPPlaylistDefinitionEntries(def)
	if err != nil {
		t.Fatalf("parseFPPPlaylistDefinitionEntries: %v", err)
	}
	if len(entries) != 1 {
		t.Fatalf("len(entries) = %d, want 1", len(entries))
	}
	// An entry with no filenames is still an entry and is listed (§4.1).
	if entries[0].Section != "mainPlaylist" || entries[0].Position != 0 || entries[0].Type != "" ||
		entries[0].SequenceName != "" || entries[0].MediaName != "" {
		t.Errorf("entries[0] = %+v, want an entry at position 0 with every recognized field empty", entries[0])
	}
}

func TestParseFPPPlaylistDefinitionEntriesMissingSectionIsEmptyNotAnError(t *testing.T) {
	def := `{"name":"x","mainPlaylist":[{"type":"sequence","sequenceName":"main.fseq"}]}`
	entries, err := parseFPPPlaylistDefinitionEntries(def)
	if err != nil {
		t.Fatalf("parseFPPPlaylistDefinitionEntries: %v", err)
	}
	if len(entries) != 1 {
		t.Fatalf("len(entries) = %d, want 1 (leadIn/leadOut absent, not an error)", len(entries))
	}
}
