package api

import (
	"encoding/json"
	"net/http"
	"testing"

	"github.com/showmeshsystems/showmesh/internal/coordinator/config"
	"github.com/showmeshsystems/showmesh/internal/coordinator/identity"
	"github.com/showmeshsystems/showmesh/pkg/fppidentity"
)

// This file covers POST /integrations/fpp/playlist-entry-observations now
// carrying the SAME reconciliation verdict GET .../reconciliation renders
// (fppreconciliation.go's mapFPPPlaylistEntryReconciliation), reusing that
// route's resolution rather than a second one that could disagree.

// putBoundPlaylistForTest wires a show.playlist bound to instanceUUID at
// boundHash, mirroring fppreconciliation_test.go's identical setup for the
// GET reconciliation route, so a POST observation reported against a
// different hash resolves to stale-import (the coordinator's own binding
// disagrees with what FPP just reported).
func putBoundPlaylistForTest(t *testing.T, setup *fppObservationTestSetup, instanceUUID, boundHash string) {
	t.Helper()
	putShowForTest(t, setup.st, "show-1", "Show One")
	putActiveShowForTest(t, setup.st, "show-1")
	putAudioOnlyCueForTest(t, setup.st, "cue-1", "show-1")

	playlistPayload := config.ShowPlaylistPayload{
		Show: "show-1", Name: "Main", Runner: config.ShowPlaylistRunnerFPP,
		MismatchPolicy: config.ShowPlaylistMismatchPolicyHold,
		FPP:            &config.ShowPlaylistFPPBinding{InstanceUUID: instanceUUID, PlaylistName: "Main", PlaylistHash: boundHash},
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
}

// fppObservationBodyForHash is [fppObservationBodyWithAction] against an
// explicit playlistHash rather than the fixture's fixed playlistHash64, so
// a test can post an observation that disagrees with a bound playlist.
func fppObservationBodyForHash(t *testing.T, instanceUUID string, sequence int64, playlistName, playlistHash, section string, position int, action string) string {
	t.Helper()
	entryKey, err := fppidentity.DeriveEntryKey(fppidentity.EntryIdentity{
		InstanceUUID: instanceUUID, PlaylistName: playlistName, PlaylistHash: playlistHash, Section: section, Position: position,
	})
	if err != nil {
		t.Fatalf("derive entry key: %v", err)
	}
	m := map[string]any{
		"schemaVersion":                      1,
		"instanceUuid":                       instanceUUID,
		"playlistName":                       playlistName,
		"playlistHash":                       playlistHash,
		"section":                            section,
		"position":                           position,
		"entryKey":                           entryKey,
		"action":                             action,
		"sequence":                           sequence,
		"observedAtMillis":                   testNow.UnixMilli(),
		"coalescedSincePreviousAcknowledged": 0,
	}
	raw, err := json.Marshal(m)
	if err != nil {
		t.Fatalf("marshal observation body: %v", err)
	}
	return string(raw)
}

// TestFPPObservationPostRendersMismatchReconciliation proves an accepted
// observation that contradicts the coordinator's own binding (a stale
// FPP import, mid-show) carries both additive fields, asserted on the
// exact wire values: the mismatch outcome fppreconcile.Reconcile computes,
// and the operator-facing instruction fppreconcile.Outcome.IsMismatch and
// fppreconcile.OperatorMismatchInstruction name for it.
func TestFPPObservationPostRendersMismatchReconciliation(t *testing.T) {
	setup := newFPPObservationTestSetup(t, fixedClock(testNow))
	api := New(setup.deps(), Options{Clock: fixedClock(testNow), Logger: testLogger()})
	scheduler := mustCreatePrincipal(t, setup.svc, "scheduler-bot", identity.RoleScheduler)
	token := mustIssueToken(t, setup.svc, scheduler.ID)

	const instanceUUID = "instance-1"
	boundHash := hash64ForTest("bead")
	putBoundPlaylistForTest(t, setup, instanceUUID, boundHash)

	// FPP reports a different hash than the one bound: the playlist was
	// edited without an FPP restart, exactly stale-import.
	staleHash := hash64ForTest("dead")
	body := fppObservationBodyForHash(t, instanceUUID, 1, "Main", staleHash, "mainPlaylist", 0, "playing")

	resp, m := mustPostObservation(t, api, body, token)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200; body: %v", resp.StatusCode, m)
	}

	const wantOutcome = "stale-import"
	if reconciliation, _ := m["reconciliation"].(string); reconciliation != wantOutcome {
		t.Fatalf("reconciliation = %q, want %q; body: %v", reconciliation, wantOutcome, m)
	}
	const wantInstruction = "Restart FPP, or re-import the playlist so the coordinator's binding and FPP agree."
	instruction, present := m["operatorInstruction"]
	if !present {
		t.Fatalf("operatorInstruction absent for a mismatched (stale-import) reconciliation; body: %v", m)
	}
	if s, ok := instruction.(string); !ok || s != wantInstruction {
		t.Fatalf("operatorInstruction = %#v, want %q; body: %v", instruction, wantInstruction, m)
	}
}

// TestFPPObservationPostOmitsOperatorInstructionWhenResolved proves the
// additive operatorInstruction field stays wholly ABSENT, not merely
// empty, when the observation resolves cleanly against the coordinator's
// own binding: the byte-identical-response requirement for the
// non-mismatch case.
func TestFPPObservationPostOmitsOperatorInstructionWhenResolved(t *testing.T) {
	setup := newFPPObservationTestSetup(t, fixedClock(testNow))
	api := New(setup.deps(), Options{Clock: fixedClock(testNow), Logger: testLogger()})
	scheduler := mustCreatePrincipal(t, setup.svc, "scheduler-bot", identity.RoleScheduler)
	token := mustIssueToken(t, setup.svc, scheduler.ID)

	const instanceUUID = "instance-1"
	putBoundPlaylistForTest(t, setup, instanceUUID, playlistHash64)

	body := fppObservationBodyForHash(t, instanceUUID, 1, "Main", playlistHash64, "mainPlaylist", 0, "playing")

	resp, m := mustPostObservation(t, api, body, token)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200; body: %v", resp.StatusCode, m)
	}

	const wantOutcome = "resolved"
	if reconciliation, _ := m["reconciliation"].(string); reconciliation != wantOutcome {
		t.Fatalf("reconciliation = %q, want %q; body: %v", reconciliation, wantOutcome, m)
	}
	if _, present := m["operatorInstruction"]; present {
		t.Fatalf("operatorInstruction present for a resolved reconciliation, want absent; body: %v", m)
	}
}

// TestFPPObservationReplayCarriesReconciliation proves a replay response
// carries the same verdict an accepted observation does: the plugin polls
// by re-posting, so a replay that omitted it would leave the plugin blind
// exactly when nothing is changing.
func TestFPPObservationReplayCarriesReconciliation(t *testing.T) {
	setup := newFPPObservationTestSetup(t, fixedClock(testNow))
	api := New(setup.deps(), Options{Clock: fixedClock(testNow), Logger: testLogger()})
	scheduler := mustCreatePrincipal(t, setup.svc, "scheduler-bot", identity.RoleScheduler)
	token := mustIssueToken(t, setup.svc, scheduler.ID)

	const instanceUUID = "instance-1"
	boundHash := hash64ForTest("bead")
	putBoundPlaylistForTest(t, setup, instanceUUID, boundHash)

	staleHash := hash64ForTest("dead")
	body := fppObservationBodyForHash(t, instanceUUID, 1, "Main", staleHash, "mainPlaylist", 0, "playing")

	if resp, m := mustPostObservation(t, api, body, token); resp.StatusCode != http.StatusOK {
		t.Fatalf("seed post: status = %d, want 200; body: %v", resp.StatusCode, m)
	}

	// Identical sequence, identical body: an idempotent replay.
	resp, m := mustPostObservation(t, api, body, token)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("replay post: status = %d, want 200; body: %v", resp.StatusCode, m)
	}
	if replay, _ := m["replay"].(bool); !replay {
		t.Fatalf("replay = %v, want true; body: %v", m["replay"], m)
	}

	const wantOutcome = "stale-import"
	if reconciliation, _ := m["reconciliation"].(string); reconciliation != wantOutcome {
		t.Fatalf("reconciliation on replay = %q, want %q; body: %v", reconciliation, wantOutcome, m)
	}
	if _, present := m["operatorInstruction"]; !present {
		t.Fatalf("operatorInstruction absent on a mismatched replay; body: %v", m)
	}
}
