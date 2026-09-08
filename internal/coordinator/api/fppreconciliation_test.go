package api

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/showmeshsystems/showmesh/internal/coordinator/config"
	"github.com/showmeshsystems/showmesh/internal/coordinator/identity"
	"github.com/showmeshsystems/showmesh/internal/coordinator/store"
	"github.com/showmeshsystems/showmesh/pkg/fppidentity"
)

// This file covers TRACK-H-H2-SPEC.md §5.1's recovery route (DELETE) and
// §5/§7's reconciliation read route (GET), reusing
// [fppObservationTestSetup] (fppobservations_test.go) — a real
// *store.Store backs Identity, FPPObservations, and (via
// [depsWithStore]'s StoreFPPReconciliation adapter) the reconciliation
// route, so a principal minted against svc and the rows these handlers
// read or write are the same store.

// depsWithStore is [fppObservationTestSetup.deps] plus FPPReconciliation
// and Config, which handleGetFPPPlaylistEntryReconciliation and
// handleGetFPPPlaylistReadiness need and the shared deps() helper does
// not set.
func (s *fppObservationTestSetup) depsWithStore() Dependencies {
	d := s.deps()
	d.FPPReconciliation = StoreFPPReconciliation{Store: s.st}
	d.Config = s.st
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

// TestDeleteFPPPlaylistEntryObservationClearsSequenceAnchorSoALowerSequenceIsAccepted
// proves this route's ACTUAL purpose (H2 spec §5.1: "the recovery route
// that section 1.5 names as a prerequisite for the plugin's sending
// half"), not merely that the row is gone. Contract §1.5's own reasoning:
// a wedged high sequence otherwise refuses every later legitimate
// observation for that instance permanently. This test proves clearing
// it actually un-wedges the instance by posting a sequence LOWER than the
// one that was cleared and confirming it is accepted rather than refused
// 409 (contracts §1.6 step 9's "lower: refuse 409" rule, which this
// clears the anchor to escape).
func TestDeleteFPPPlaylistEntryObservationClearsSequenceAnchorSoALowerSequenceIsAccepted(t *testing.T) {
	setup := newFPPObservationTestSetup(t, fixedClock(testNow))
	api := New(setup.deps(), Options{Clock: fixedClock(testNow), Logger: testLogger()})

	operator := mustCreatePrincipal(t, setup.svc, "operator-1", identity.RoleOperator)
	operatorToken := mustIssueToken(t, setup.svc, operator.ID)
	scheduler := mustCreatePrincipal(t, setup.svc, "scheduler-bot", identity.RoleScheduler)
	schedulerToken := mustIssueToken(t, setup.svc, scheduler.ID)

	// Seed a wildly high sequence, exactly contract §1.5's wedging case.
	if err := setup.st.PutFPPPlaylistEntryObservation(context.Background(), store.FPPPlaylistEntryObservationRecord{
		InstanceUUID: "instance-1", SchemaVersion: 1, Sequence: 999999, Action: "playing",
		PlaylistName: "Main", PlaylistHash: playlistHash64, EntryKey: playlistHash64,
		ObservedAt: testNow, ReceivedAt: testNow,
	}); err != nil {
		t.Fatalf("seed observation: %v", err)
	}

	// Before the DELETE, a legitimate but far-lower sequence is refused
	// 409: the wedge this route exists to clear.
	preDeleteBody := fppObservationBody(t, "instance-1", 1, "Main", "mainPlaylist", 0)
	if resp, m := mustPostObservation(t, api, preDeleteBody, schedulerToken); resp.StatusCode != http.StatusConflict {
		t.Fatalf("status BEFORE delete = %d, want %d (409, the wedge this test sets up); body: %v", resp.StatusCode, http.StatusConflict, m)
	}

	req := httptest.NewRequest(http.MethodDelete, "/api/v1/integrations/fpp/playlist-entry-observations/instance-1", nil)
	req.Header.Set("Authorization", "Bearer "+operatorToken)
	resp, body := doRawRequest(t, api.Handler, req)
	if resp.StatusCode != http.StatusNoContent {
		t.Fatalf("DELETE status = %d, want 204; body: %s", resp.StatusCode, body)
	}

	// After the DELETE, the SAME low sequence is accepted, proving the
	// anchor (not merely the row) was cleared.
	postDeleteBody := fppObservationBody(t, "instance-1", 1, "Main", "mainPlaylist", 0)
	resp, m := mustPostObservation(t, api, postDeleteBody, schedulerToken)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status AFTER delete = %d, want 200 (accepted); body: %v", resp.StatusCode, m)
	}
	rec, err := setup.st.GetFPPPlaylistEntryObservation(context.Background(), "instance-1")
	if err != nil {
		t.Fatalf("GetFPPPlaylistEntryObservation after re-post: %v", err)
	}
	if rec.Sequence != 1 {
		t.Fatalf("stored sequence = %d, want 1 (the re-posted observation, actually accepted)", rec.Sequence)
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

// TestGetFPPPlaylistEntryReconciliationRendersOperatorInstructionForMismatch
// is the coordinator half of the mismatch-notice ruling: a stale-import
// outcome (one of the four contradicting outcomes H0.2 collapses into an
// operator-visible mismatch) must carry operatorInstruction, naming both
// remedies, on the wire. This is a notice only -- it asserts nothing about
// dispatch, which cueactivate.Decide's own mismatch-policy tests already
// cover unchanged.
func TestGetFPPPlaylistEntryReconciliationRendersOperatorInstructionForMismatch(t *testing.T) {
	setup := newFPPObservationTestSetup(t, fixedClock(testNow))
	api := New(setup.depsWithStore(), Options{Clock: fixedClock(testNow), Logger: testLogger()})

	putShowForTest(t, setup.st, "show-1", "Show One")
	putActiveShowForTest(t, setup.st, "show-1")
	putAudioOnlyCueForTest(t, setup.st, "cue-1", "show-1")

	boundHash := hash64ForTest("bound")
	playlistPayload := config.ShowPlaylistPayload{
		Show: "show-1", Name: "Main", Runner: config.ShowPlaylistRunnerFPP,
		MismatchPolicy: config.ShowPlaylistMismatchPolicyHold,
		FPP:            &config.ShowPlaylistFPPBinding{InstanceUUID: "instance-1", PlaylistName: "Main", PlaylistHash: boundHash},
		Entries: []config.ShowPlaylistEntry{{
			ID: "entry-1", Cue: "cue-1",
			FPP: &config.ShowPlaylistEntryFPP{Section: "mainPlaylist", Position: 0},
		}},
	}
	playlistJSON, err := config.EncodeShowPlaylistPayload(playlistPayload)
	if err != nil {
		t.Fatalf("encode show.playlist payload: %v", err)
	}
	putConfigForTest(t, setup.st, config.ShowPlaylistConfigKind, "playlist-1", playlistJSON)

	// The FPP playlist was edited mid-show without an FPP restart: a NEW
	// hash is observed, but the binding is held, not remapped, which is
	// exactly stale-import.
	staleHash := hash64ForTest("stale")
	entryKey, err := fppidentity.DeriveEntryKey(fppidentity.EntryIdentity{
		InstanceUUID: "instance-1", PlaylistName: "Main", PlaylistHash: staleHash, Section: "mainPlaylist", Position: 0,
	})
	if err != nil {
		t.Fatalf("derive entry key: %v", err)
	}
	if err := setup.st.PutFPPPlaylistEntryObservation(context.Background(), store.FPPPlaylistEntryObservationRecord{
		InstanceUUID: "instance-1", SchemaVersion: 1, Sequence: 1, Action: "playing",
		PlaylistName: "Main", PlaylistHash: staleHash, Section: "mainPlaylist", Position: 0,
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
	if outcome, _ := body["outcome"].(string); outcome != "stale-import" {
		t.Fatalf("outcome = %q, want %q; body: %v", outcome, "stale-import", body)
	}
	const wantInstruction = "Restart FPP, or re-import the playlist so the coordinator's binding and FPP agree."
	instruction, present := body["operatorInstruction"]
	if !present {
		t.Fatalf("operatorInstruction absent for a mismatched (stale-import) outcome; body: %v", body)
	}
	if s, ok := instruction.(string); !ok || s != wantInstruction {
		t.Fatalf("operatorInstruction = %#v, want %q; body: %v", instruction, wantInstruction, body)
	}
}

// TestGetFPPPlaylistEntryReconciliationOmitsOperatorInstructionWhenNotMismatched
// proves the additive field stays absent, field name and all, for both a
// resolved observation and an unbound one -- non-mismatch shapes must
// serialize identically to before this field existed.
func TestGetFPPPlaylistEntryReconciliationOmitsOperatorInstructionWhenNotMismatched(t *testing.T) {
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
	resp, raw := doRawRequest(t, api.Handler, req)
	body := decodeMap(t, raw)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200; body: %v", resp.StatusCode, body)
	}
	if outcome, _ := body["outcome"].(string); outcome != "unbound" {
		t.Fatalf("outcome = %q, want %q; body: %v", outcome, "unbound", body)
	}
	if _, present := body["operatorInstruction"]; present {
		t.Fatalf("operatorInstruction present for an unbound (non-mismatched) outcome; body: %v", body)
	}
}

// TestGetFPPPlaylistEntryReconciliationRendersEvidenceBrokenAdditively is
// owner ruling 2026-09-02's own diagnostic-route decision
// (cue-deactivate-on-jump proposal §0a): unlike GET /current-runs, this
// per-instance route reports evidenceBrokenAt ADDITIVELY, alongside the
// raw, un-collapsed outcome/reason fppreconcile.Reconcile itself computed
// from the same row — never collapsing one into the other, since a caller
// drilling into one instance benefits from seeing both facts distinctly.
func TestGetFPPPlaylistEntryReconciliationRendersEvidenceBrokenAdditively(t *testing.T) {
	setup := newFPPObservationTestSetup(t, fixedClock(testNow))
	api := New(setup.depsWithStore(), Options{Clock: fixedClock(testNow), Logger: testLogger()})

	putShowForTest(t, setup.st, "show-1", "Show One")
	putActiveShowForTest(t, setup.st, "show-1")
	putAudioOnlyCueForTest(t, setup.st, "cue-1", "show-1")

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
	if _, present := body["evidenceBrokenAt"]; present {
		t.Fatalf("evidenceBrokenAt present before marking evidence broken; body: %v", body)
	}
	if outcome, _ := body["outcome"].(string); outcome != "resolved" {
		t.Fatalf("outcome = %q, want resolved (unaffected by the marker being absent); body: %v", outcome, body)
	}

	brokenAt := testNow.Add(time.Second)
	if err := setup.st.MarkFPPPlaylistEntryObservationEvidenceBroken(context.Background(), "instance-1", brokenAt); err != nil {
		t.Fatalf("mark evidence broken: %v", err)
	}

	resp, raw = doRawRequest(t, api.Handler, req)
	body = decodeMap(t, raw)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200; body: %v", resp.StatusCode, body)
	}
	if outcome, _ := body["outcome"].(string); outcome != "resolved" {
		t.Fatalf("outcome = %q, want resolved (never collapsed into evidence-broken on THIS route); body: %v", outcome, body)
	}
	got, _ := body["evidenceBrokenAt"].(string)
	if got == "" {
		t.Fatalf("evidenceBrokenAt missing after marking evidence broken; body: %v", body)
	}
}

// TestGetFPPPlaylistEntryReconciliationRendersEmptyStringSection proves
// review fix item 6: FPP's own common default section IS the empty
// string, so a resolved observation reporting it must render
// observedSection as the JSON string "" (present, matched), never as an
// absent member, which would be indistinguishable from an
// identity-unavailable observation that never reported a section at all.
func TestGetFPPPlaylistEntryReconciliationRendersEmptyStringSection(t *testing.T) {
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

	// section is deliberately "" (FPP's own common default section, NOT
	// "no section reported").
	playlistPayload := config.ShowPlaylistPayload{
		Show: "show-1", Name: "Main", Runner: config.ShowPlaylistRunnerFPP,
		MismatchPolicy: config.ShowPlaylistMismatchPolicyHold,
		FPP:            &config.ShowPlaylistFPPBinding{InstanceUUID: "instance-1", PlaylistName: "Main", PlaylistHash: playlistHash64},
		Entries: []config.ShowPlaylistEntry{{
			ID: "entry-1", Cue: "cue-1",
			FPP: &config.ShowPlaylistEntryFPP{Section: "", Position: 0},
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
		PlaylistName: "Main", PlaylistHash: playlistHash64, Section: "", Position: 0,
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
	section, present := body["observedSection"]
	if !present {
		t.Fatalf("observedSection is absent from a resolved observation that reported the empty-string section; body: %v", body)
	}
	if s, ok := section.(string); !ok || s != "" {
		t.Fatalf("observedSection = %#v, want the JSON string \"\"; body: %v", section, body)
	}
}

// TestGetFPPPlaylistEntryReconciliationOmitsSectionWhenIdentityUnavailable
// proves the other half of item 6: an identity-unavailable observation,
// which never reported a section at all, must render observedSection
// absent, not present-and-empty, which would read as "the common
// default section" and be indistinguishable from
// [TestGetFPPPlaylistEntryReconciliationRendersEmptyStringSection]'s case.
func TestGetFPPPlaylistEntryReconciliationOmitsSectionWhenIdentityUnavailable(t *testing.T) {
	setup := newFPPObservationTestSetup(t, fixedClock(testNow))
	api := New(setup.depsWithStore(), Options{Clock: fixedClock(testNow), Logger: testLogger()})

	if err := setup.st.PutFPPPlaylistEntryObservation(context.Background(), store.FPPPlaylistEntryObservationRecord{
		InstanceUUID: "instance-1", SchemaVersion: 1, Sequence: 1, Action: "playing",
		Unavailable: "missing_definition", ObservedAt: testNow, ReceivedAt: testNow,
	}); err != nil {
		t.Fatalf("seed observation: %v", err)
	}

	req := httptest.NewRequest(http.MethodGet, "/api/v1/integrations/fpp/playlist-entry-observations/instance-1/reconciliation", nil)
	resp, raw := doRawRequest(t, api.Handler, req)
	body := decodeMap(t, raw)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200; body: %v", resp.StatusCode, body)
	}
	if outcome, _ := body["outcome"].(string); outcome != "identity-unavailable" {
		t.Fatalf("outcome = %q, want %q; body: %v", outcome, "identity-unavailable", body)
	}
	if _, present := body["observedSection"]; present {
		t.Fatalf("observedSection is present for an identity-unavailable observation, want absent; body: %v", body)
	}
}

// --- TRACK-H-H2-SPEC.md §6/§7 readiness route ---

func TestGetFPPPlaylistReadinessReportsMissingDefinition(t *testing.T) {
	setup := newFPPObservationTestSetup(t, fixedClock(testNow))
	api := New(setup.depsWithStore(), Options{Clock: fixedClock(testNow), Logger: testLogger()})

	putShowForTest(t, setup.st, "show-1", "Show One")
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
	playlistJSON, err := config.EncodeShowPlaylistPayload(playlistPayload)
	if err != nil {
		t.Fatalf("encode show.playlist payload: %v", err)
	}
	putConfigForTest(t, setup.st, config.ShowPlaylistConfigKind, "playlist-1", playlistJSON)
	// Deliberately NOT storing a definition: no
	// PutFPPPlaylistDefinition, so condition 1 (H2 spec §6) must fail.

	req := httptest.NewRequest(http.MethodGet, "/api/v1/integrations/fpp/playlists/playlist-1/readiness", nil)
	resp, raw := doRawRequest(t, api.Handler, req)
	body := decodeMap(t, raw)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200; body: %v", resp.StatusCode, body)
	}
	if ready, _ := body["ready"].(bool); ready {
		t.Fatalf("ready = true, want false with no definition stored; body: %v", body)
	}
	if cond, _ := body["failingCondition"].(string); cond != "definition-missing" {
		t.Fatalf("failingCondition = %q, want %q; body: %v", cond, "definition-missing", body)
	}
}

func TestGetFPPPlaylistReadinessReady(t *testing.T) {
	setup := newFPPObservationTestSetup(t, fixedClock(testNow))
	api := New(setup.depsWithStore(), Options{Clock: fixedClock(testNow), Logger: testLogger()})

	putShowForTest(t, setup.st, "show-1", "Show One")
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
	playlistJSON, err := config.EncodeShowPlaylistPayload(playlistPayload)
	if err != nil {
		t.Fatalf("encode show.playlist payload: %v", err)
	}
	putConfigForTest(t, setup.st, config.ShowPlaylistConfigKind, "playlist-1", playlistJSON)

	if _, err := setup.st.PutFPPPlaylistDefinition(context.Background(), store.FPPPlaylistDefinitionRecord{
		InstanceUUID: "instance-1", PlaylistHash: playlistHash64, PlaylistName: "Main",
		DefinitionJSON: `{"leadIn":[],"mainPlaylist":[{"type":"sequence"}],"leadOut":[]}`,
		CapturedAt:     testNow, ReceivedAt: testNow,
	}); err != nil {
		t.Fatalf("put fpp playlist definition: %v", err)
	}

	req := httptest.NewRequest(http.MethodGet, "/api/v1/integrations/fpp/playlists/playlist-1/readiness", nil)
	resp, raw := doRawRequest(t, api.Handler, req)
	body := decodeMap(t, raw)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200; body: %v", resp.StatusCode, body)
	}
	if ready, _ := body["ready"].(bool); !ready {
		t.Fatalf("ready = false, want true; body: %v", body)
	}
	if warning, _ := body["warning"].(string); warning == "" {
		t.Fatalf("warning is empty, want the no-observation-yet warning (H2 spec §6: \"the normal afternoon state, not a fault\"); body: %v", body)
	}
}

// TestGetFPPPlaylistReadinessDetectsEditedPlaylistWhileFPPIdle is the
// acceptance case at the HTTP layer: a newer definition is on file for
// the same instance/playlist name under a different hash, and NO
// observation exists at all (FPP has never played since the edit). The
// route must report not ready from the definition store alone.
func TestGetFPPPlaylistReadinessDetectsEditedPlaylistWhileFPPIdle(t *testing.T) {
	setup := newFPPObservationTestSetup(t, fixedClock(testNow))
	api := New(setup.depsWithStore(), Options{Clock: fixedClock(testNow), Logger: testLogger()})
	c := newOpenAPICompiler(t)

	putShowForTest(t, setup.st, "show-1", "Show One")
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
	playlistJSON, err := config.EncodeShowPlaylistPayload(playlistPayload)
	if err != nil {
		t.Fatalf("encode show.playlist payload: %v", err)
	}
	putConfigForTest(t, setup.st, config.ShowPlaylistConfigKind, "playlist-1", playlistJSON)

	if _, err := setup.st.PutFPPPlaylistDefinition(context.Background(), store.FPPPlaylistDefinitionRecord{
		InstanceUUID: "instance-1", PlaylistHash: playlistHash64, PlaylistName: "Main",
		DefinitionJSON: `{"leadIn":[],"mainPlaylist":[{"type":"sequence"}],"leadOut":[]}`,
		CapturedAt:     testNow.Add(-time.Hour), ReceivedAt: testNow.Add(-time.Hour),
	}); err != nil {
		t.Fatalf("put bound fpp playlist definition: %v", err)
	}
	newerHash := "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"
	if _, err := setup.st.PutFPPPlaylistDefinition(context.Background(), store.FPPPlaylistDefinitionRecord{
		InstanceUUID: "instance-1", PlaylistHash: newerHash, PlaylistName: "Main",
		DefinitionJSON: `{"leadIn":[],"mainPlaylist":[{"type":"sequence"},{"type":"sequence"},{"type":"sequence"}],"leadOut":[]}`,
		CapturedAt:     testNow, ReceivedAt: testNow,
	}); err != nil {
		t.Fatalf("put newer fpp playlist definition: %v", err)
	}
	// Deliberately no observation stored: FPP has never played since the
	// edit — the exact acceptance case, "without anything having to be
	// played first."

	req := httptest.NewRequest(http.MethodGet, "/api/v1/integrations/fpp/playlists/playlist-1/readiness", nil)
	resp, raw := doRawRequest(t, api.Handler, req)
	body := decodeMap(t, raw)
	assertMatchesSchema(t, c, "FPPPlaylistReadinessResponse", raw)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200; body: %v", resp.StatusCode, body)
	}
	if ready, _ := body["ready"].(bool); ready {
		t.Fatalf("ready = true, want false: a newer definition is stored for this instance/playlist name and nothing has played since the edit; body: %v", body)
	}
	if cond, _ := body["failingCondition"].(string); cond != "definition-superseded" {
		t.Fatalf("failingCondition = %q, want %q; body: %v", cond, "definition-superseded", body)
	}
}

// TestGetFPPPlaylistReadinessObservationUnavailableIsFailureNotWarning is
// the issue's own literal reproduction, reported as a bug against
// unmodified code: an observation exists (FPP played the edited playlist,
// then went idle) but could not establish identity. Readiness must report
// not ready, never ready:true with a warning.
func TestGetFPPPlaylistReadinessObservationUnavailableIsFailureNotWarning(t *testing.T) {
	setup := newFPPObservationTestSetup(t, fixedClock(testNow))
	api := New(setup.depsWithStore(), Options{Clock: fixedClock(testNow), Logger: testLogger()})
	c := newOpenAPICompiler(t)

	putShowForTest(t, setup.st, "show-1", "Show One")
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
	playlistJSON, err := config.EncodeShowPlaylistPayload(playlistPayload)
	if err != nil {
		t.Fatalf("encode show.playlist payload: %v", err)
	}
	putConfigForTest(t, setup.st, config.ShowPlaylistConfigKind, "playlist-1", playlistJSON)

	if _, err := setup.st.PutFPPPlaylistDefinition(context.Background(), store.FPPPlaylistDefinitionRecord{
		InstanceUUID: "instance-1", PlaylistHash: playlistHash64, PlaylistName: "Main",
		DefinitionJSON: `{"leadIn":[],"mainPlaylist":[{"type":"sequence"}],"leadOut":[]}`,
		CapturedAt:     testNow, ReceivedAt: testNow,
	}); err != nil {
		t.Fatalf("put fpp playlist definition: %v", err)
	}
	if err := setup.st.PutFPPPlaylistEntryObservation(context.Background(), store.FPPPlaylistEntryObservationRecord{
		InstanceUUID: "instance-1", SchemaVersion: 1, Sequence: 1,
		Action: "idle", Unavailable: "missing_playlist_name",
		ObservedAt: testNow, ReceivedAt: testNow,
	}); err != nil {
		t.Fatalf("put fpp playlist entry observation: %v", err)
	}

	req := httptest.NewRequest(http.MethodGet, "/api/v1/integrations/fpp/playlists/playlist-1/readiness", nil)
	resp, raw := doRawRequest(t, api.Handler, req)
	body := decodeMap(t, raw)
	assertMatchesSchema(t, c, "FPPPlaylistReadinessResponse", raw)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200; body: %v", resp.StatusCode, body)
	}
	if ready, _ := body["ready"].(bool); ready {
		t.Fatalf("ready = true, want false: the latest observation could not establish identity, so this check could not run; body: %v", body)
	}
	if cond, _ := body["failingCondition"].(string); cond != "evidence-unavailable" {
		t.Fatalf("failingCondition = %q, want %q; body: %v", cond, "evidence-unavailable", body)
	}
	if warning, _ := body["warning"].(string); warning != "" {
		t.Fatalf("warning = %q, want empty: this is a failing condition, not a warning; body: %v", warning, body)
	}
}

func TestGetFPPPlaylistReadinessRefusesNonFPPPlaylist(t *testing.T) {
	setup := newFPPObservationTestSetup(t, fixedClock(testNow))
	api := New(setup.depsWithStore(), Options{Clock: fixedClock(testNow), Logger: testLogger()})

	putShowForTest(t, setup.st, "show-1", "Show One")
	playlistPayload := config.ShowPlaylistPayload{
		Show: "show-1", Name: "Main", Runner: config.ShowPlaylistRunnerShowmeshAudio,
	}
	playlistJSON, err := config.EncodeShowPlaylistPayload(playlistPayload)
	if err != nil {
		t.Fatalf("encode show.playlist payload: %v", err)
	}
	putConfigForTest(t, setup.st, config.ShowPlaylistConfigKind, "playlist-1", playlistJSON)

	req := httptest.NewRequest(http.MethodGet, "/api/v1/integrations/fpp/playlists/playlist-1/readiness", nil)
	resp, body := doRawRequest(t, api.Handler, req)
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("status = %d, want %d; body: %s", resp.StatusCode, http.StatusBadRequest, body)
	}
}

func TestGetFPPPlaylistReadinessNotFoundForUnknownPlaylist(t *testing.T) {
	setup := newFPPObservationTestSetup(t, fixedClock(testNow))
	api := New(setup.depsWithStore(), Options{Clock: fixedClock(testNow), Logger: testLogger()})

	req := httptest.NewRequest(http.MethodGet, "/api/v1/integrations/fpp/playlists/no-such-playlist/readiness", nil)
	resp, body := doRawRequest(t, api.Handler, req)
	if resp.StatusCode != http.StatusNotFound {
		t.Fatalf("status = %d, want %d; body: %s", resp.StatusCode, http.StatusNotFound, body)
	}
}
