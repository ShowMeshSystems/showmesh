package api

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/showmeshsystems/showmesh/internal/coordinator/config"
	"github.com/showmeshsystems/showmesh/internal/coordinator/identity"
	"github.com/showmeshsystems/showmesh/internal/coordinator/store"
)

// This file covers TRACK-H-H2-SPEC.md §5.1's recovery route (DELETE) and
// §5/§7's reconciliation read route (GET), reusing
// [fppObservationTestSetup] (fppobservations_test.go) — a real
// *store.Store backs both Identity and FPPObservations/AssetManifests, so
// a principal minted against svc and the rows these handlers read or
// write are the same store.

// depsWithStore is [fppObservationTestSetup.deps] plus AssetManifests,
// which handleGetFPPPlaylistEntryReconciliation needs (fppreconcile.
// Reconcile takes a *store.Store directly) and the shared deps() helper
// does not set.
func (s *fppObservationTestSetup) depsWithStore() Dependencies {
	d := s.deps()
	d.AssetManifests = s.st
	return d
}

func putConfigForTest(t *testing.T, st *store.Store, kind, id, payload string) {
	t.Helper()
	ctx := context.Background()
	if _, err := st.CreateConfigRevision(ctx, store.ConfigRevisionRecord{
		Kind: kind, ObjectID: id, Revision: 1, PayloadJSON: payload, Source: "api",
	}); err != nil {
		t.Fatalf("create config revision %s/%s: %v", kind, id, err)
	}
	if _, err := st.ActivateConfigRevision(ctx, kind, id, 1); err != nil {
		t.Fatalf("activate config revision %s/%s: %v", kind, id, err)
	}
}

func putShowForTest(t *testing.T, st *store.Store, id, name string) {
	t.Helper()
	payload, err := config.EncodeShowPayload(config.ShowPayload{Name: name})
	if err != nil {
		t.Fatalf("encode show payload: %v", err)
	}
	putConfigForTest(t, st, config.ShowConfigKind, id, payload)
}

func putActiveShowForTest(t *testing.T, st *store.Store, showID string) {
	t.Helper()
	payload, err := config.EncodeShowActivePayload(config.ShowActivePayload{Show: showID})
	if err != nil {
		t.Fatalf("encode show.active payload: %v", err)
	}
	putConfigForTest(t, st, config.ShowActiveConfigKind, config.ShowActiveObjectID, payload)
}

// --- DELETE /integrations/fpp/playlist-entry-observations/{instanceUuid} ---

func TestDeleteFPPPlaylistEntryObservationRefusedUnauthenticated(t *testing.T) {
	setup := newFPPObservationTestSetup(t, fixedClock(testNow))
	api := New(setup.deps(), Options{Clock: fixedClock(testNow), Logger: testLogger(), CloseReads: true})

	req := httptest.NewRequest(http.MethodDelete, "/api/v1/integrations/fpp/playlist-entry-observations/instance-1", nil)
	resp, body := doRawRequest(t, api.Handler, req)
	if resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("status = %d, want 401; body: %s", resp.StatusCode, body)
	}
}

// TestDeleteFPPPlaylistEntryObservationRefusedForbiddenForScheduler proves
// this route is fpp:command, NOT fpp:observe: the scheduler role (the
// plugin's own principal, RoleScheduler) holds fpp:observe but not
// fpp:command, and must be refused — an operator credential must be able
// to clear evidence, but the plugin credential must not, the exact
// inverse of the observation POST's own scope boundary.
func TestDeleteFPPPlaylistEntryObservationRefusedForbiddenForScheduler(t *testing.T) {
	setup := newFPPObservationTestSetup(t, fixedClock(testNow))
	api := New(setup.deps(), Options{Clock: fixedClock(testNow), Logger: testLogger()})

	scheduler := mustCreatePrincipal(t, setup.svc, "scheduler-bot", identity.RoleScheduler)
	token := mustIssueToken(t, setup.svc, scheduler.ID)

	req := httptest.NewRequest(http.MethodDelete, "/api/v1/integrations/fpp/playlist-entry-observations/instance-1", nil)
	req.Header.Set("Authorization", "Bearer "+token)
	resp, body := doRawRequest(t, api.Handler, req)
	if resp.StatusCode != http.StatusForbidden {
		t.Fatalf("status = %d, want 403; body: %s", resp.StatusCode, body)
	}
}

// TestDeleteFPPPlaylistEntryObservationClearsRowForOperator proves the
// positive case: an operator credential (fpp:command) clears a stored
// observation, and a subsequent GET no longer finds it.
func TestDeleteFPPPlaylistEntryObservationClearsRowForOperator(t *testing.T) {
	setup := newFPPObservationTestSetup(t, fixedClock(testNow))
	api := New(setup.deps(), Options{Clock: fixedClock(testNow), Logger: testLogger()})

	operator := mustCreatePrincipal(t, setup.svc, "operator-1", identity.RoleOperator)
	token := mustIssueToken(t, setup.svc, operator.ID)

	if err := setup.st.PutFPPPlaylistEntryObservation(context.Background(), store.FPPPlaylistEntryObservationRecord{
		InstanceUUID: "instance-1", SchemaVersion: 1, Sequence: 5, Action: "playing",
		PlaylistName: "Main", PlaylistHash: playlistHash64, EntryKey: playlistHash64,
		ObservedAt: testNow, ReceivedAt: testNow,
	}); err != nil {
		t.Fatalf("seed observation: %v", err)
	}

	req := httptest.NewRequest(http.MethodDelete, "/api/v1/integrations/fpp/playlist-entry-observations/instance-1", nil)
	req.Header.Set("Authorization", "Bearer "+token)
	resp, body := doRawRequest(t, api.Handler, req)
	if resp.StatusCode != http.StatusNoContent {
		t.Fatalf("status = %d, want 204; body: %s", resp.StatusCode, body)
	}

	if _, err := setup.st.GetFPPPlaylistEntryObservation(context.Background(), "instance-1"); err != store.ErrFPPPlaylistEntryObservationNotFound {
		t.Fatalf("GetFPPPlaylistEntryObservation after delete: err = %v, want ErrFPPPlaylistEntryObservationNotFound", err)
	}
}

// TestDeleteFPPPlaylistEntryObservationIdempotentWhenNoRowExists proves
// this route succeeds even when nothing is actually wedged: the
// post-condition (no stored row) is the same either way.
func TestDeleteFPPPlaylistEntryObservationIdempotentWhenNoRowExists(t *testing.T) {
	setup := newFPPObservationTestSetup(t, fixedClock(testNow))
	api := New(setup.deps(), Options{Clock: fixedClock(testNow), Logger: testLogger()})

	operator := mustCreatePrincipal(t, setup.svc, "operator-1", identity.RoleOperator)
	token := mustIssueToken(t, setup.svc, operator.ID)

	req := httptest.NewRequest(http.MethodDelete, "/api/v1/integrations/fpp/playlist-entry-observations/never-seen", nil)
	req.Header.Set("Authorization", "Bearer "+token)
	resp, body := doRawRequest(t, api.Handler, req)
	if resp.StatusCode != http.StatusNoContent {
		t.Fatalf("status = %d, want 204; body: %s", resp.StatusCode, body)
	}
}

// TestDeleteFPPPlaylistEntryObservationIsAudited proves §5.1's own "is
// audited" requirement.
func TestDeleteFPPPlaylistEntryObservationIsAudited(t *testing.T) {
	setup := newFPPObservationTestSetup(t, fixedClock(testNow))
	api := New(setup.deps(), Options{Clock: fixedClock(testNow), Logger: testLogger()})

	operator := mustCreatePrincipal(t, setup.svc, "operator-1", identity.RoleOperator)
	token := mustIssueToken(t, setup.svc, operator.ID)

	req := httptest.NewRequest(http.MethodDelete, "/api/v1/integrations/fpp/playlist-entry-observations/instance-1", nil)
	req.Header.Set("Authorization", "Bearer "+token)
	resp, body := doRawRequest(t, api.Handler, req)
	if resp.StatusCode != http.StatusNoContent {
		t.Fatalf("status = %d, want 204; body: %s", resp.StatusCode, body)
	}

	entries, err := setup.st.ListAuditEntries(context.Background(), 0, 20)
	if err != nil {
		t.Fatalf("list audit entries: %v", err)
	}
	found := false
	for _, e := range entries {
		if e.Action == auditActionFPPResetPlaylistEntryObservationSequence && e.Target == "instance-1" {
			found = true
		}
	}
	if !found {
		t.Fatalf("no audit entry with action %q target %q found among %d entries", auditActionFPPResetPlaylistEntryObservationSequence, "instance-1", len(entries))
	}
}

// --- GET /integrations/fpp/playlist-entry-observations/{instanceUuid}/reconciliation ---

func TestGetFPPPlaylistEntryReconciliationNotFoundWhenNoObservation(t *testing.T) {
	setup := newFPPObservationTestSetup(t, fixedClock(testNow))
	api := New(setup.depsWithStore(), Options{Clock: fixedClock(testNow), Logger: testLogger()})

	req := httptest.NewRequest(http.MethodGet, "/api/v1/integrations/fpp/playlist-entry-observations/instance-1/reconciliation", nil)
	resp, body := doRawRequest(t, api.Handler, req)
	if resp.StatusCode != http.StatusNotFound {
		t.Fatalf("status = %d, want 404; body: %s", resp.StatusCode, body)
	}
}

// TestGetFPPPlaylistEntryReconciliationRendersUnbound proves the read
// route renders [fppreconcile.Reconcile]'s own answer end to end: no
// show.playlist binds this instance, so the outcome is "unbound".
func TestGetFPPPlaylistEntryReconciliationRendersUnbound(t *testing.T) {
	setup := newFPPObservationTestSetup(t, fixedClock(testNow))
	api := New(setup.depsWithStore(), Options{Clock: fixedClock(testNow), Logger: testLogger()})

	if err := setup.st.PutFPPPlaylistEntryObservation(context.Background(), store.FPPPlaylistEntryObservationRecord{
		InstanceUUID: "instance-1", SchemaVersion: 1, Sequence: 1, Action: "playing",
		PlaylistName: "Main", PlaylistHash: playlistHash64, EntryKey: playlistHash64,
		ObservedAt: testNow, ReceivedAt: testNow,
	}); err != nil {
		t.Fatalf("seed observation: %v", err)
	}

	req := httptest.NewRequest(http.MethodGet, "/api/v1/integrations/fpp/playlist-entry-observations/instance-1/reconciliation", nil)
	resp, m := doRawRequest(t, api.Handler, req)
	body := decodeMap(t, m)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200; body: %v", resp.StatusCode, body)
	}
	if outcome, _ := body["outcome"].(string); outcome != "unbound" {
		t.Fatalf("outcome = %q, want %q; body: %v", outcome, "unbound", body)
	}
}

// TestGetFPPPlaylistEntryReconciliationRendersResolved proves the happy
// path renders the resolved Playlist/entry/Cue.
func TestGetFPPPlaylistEntryReconciliationRendersResolved(t *testing.T) {
	setup := newFPPObservationTestSetup(t, fixedClock(testNow))
	api := New(setup.depsWithStore(), Options{Clock: fixedClock(testNow), Logger: testLogger()})

	putShowForTest(t, setup.st, "show-1", "Show One")
	putActiveShowForTest(t, setup.st, "show-1")

	cuePayload, err := config.EncodeShowCuePayload(config.ShowCuePayload{
		Show: "show-1", Name: "cue-1",
		Outputs: config.ShowCueOutputs{Render: &config.ShowCueRenderOutput{Sequence: "seq-1"}},
	})
	if err != nil {
		t.Fatalf("encode show.cue payload: %v", err)
	}
	putConfigForTest(t, setup.st, config.ShowCueConfigKind, "cue-1", cuePayload)

	playlistPayload := config.ShowPlaylistPayload{
		Show: "show-1", Name: "Main", Runner: config.ShowPlaylistRunnerFPP,
		MismatchPolicy: config.ShowPlaylistMismatchPolicyHold,
		FPP:            &config.ShowPlaylistFPPBinding{InstanceUUID: "instance-1", PlaylistName: "Main", PlaylistHash: playlistHash64},
		Entries: []config.ShowPlaylistEntry{{
			ID: "entry-1", Cue: "cue-1",
			FPP: &config.ShowPlaylistEntryFPP{Section: "mainPlaylist", Position: 0},
		}},
	}
	entryKey, err := config.DerivePlaylistEntryKey(playlistPayload, "entry-1")
	if err != nil {
		t.Fatalf("derive entry key: %v", err)
	}
	playlistJSON, err := config.EncodeShowPlaylistPayload(playlistPayload)
	if err != nil {
		t.Fatalf("encode show.playlist payload: %v", err)
	}
	putConfigForTest(t, setup.st, config.ShowPlaylistConfigKind, "playlist-1", playlistJSON)

	if err := setup.st.PutFPPPlaylistEntryObservation(context.Background(), store.FPPPlaylistEntryObservationRecord{
		InstanceUUID: "instance-1", SchemaVersion: 1, Sequence: 1, Action: "playing",
		PlaylistName: "Main", PlaylistHash: playlistHash64, Section: "mainPlaylist", Position: 0,
		EntryKey: entryKey, ObservedAt: testNow, ReceivedAt: testNow,
	}); err != nil {
		t.Fatalf("seed observation: %v", err)
	}

	req := httptest.NewRequest(http.MethodGet, "/api/v1/integrations/fpp/playlist-entry-observations/instance-1/reconciliation", nil)
	resp, raw := doRawRequest(t, api.Handler, req)
	body := decodeMap(t, raw)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200; body: %v", resp.StatusCode, body)
	}
	if outcome, _ := body["outcome"].(string); outcome != "resolved" {
		t.Fatalf("outcome = %q, want %q; body: %v", outcome, "resolved", body)
	}
	if entryID, _ := body["entryId"].(string); entryID != "entry-1" {
		t.Fatalf("entryId = %q, want %q; body: %v", entryID, "entry-1", body)
	}
	if cueID, _ := body["cueId"].(string); cueID != "cue-1" {
		t.Fatalf("cueId = %q, want %q; body: %v", cueID, "cue-1", body)
	}
}
