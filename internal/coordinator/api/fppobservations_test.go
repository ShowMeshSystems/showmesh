package api

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"path/filepath"
	"testing"
	"time"

	"github.com/showmeshsystems/showmesh/internal/coordinator/identity"
	"github.com/showmeshsystems/showmesh/internal/coordinator/store"
	"github.com/showmeshsystems/showmesh/pkg/fppidentity"
)

// This file is handler coverage for the FPP playlist-entry observation contract:
// FPP-PLUGIN-COORDINATOR-CONTRACTS.md §1.7's refusal table, plus the
// acceptance criteria the build brief names explicitly (stream visibility,
// idempotent replay storing nothing, every unavailable reason accepted,
// an unavailable observation with no instanceUuid refused, the scope
// boundary, and "ingestion grants no execution authority" asserted by
// absence). Every test drives the real handler through a real store and a
// real identity.Service, this package's own standing rule (CLAUDE.md,
// restated in every test file here).

// fppObservationTestSetup mirrors fppCommandTestSetup's identical
// reasoning (fppcommand_handler_test.go): a real *store.Store backs both
// Identity and FPPObservations, so a principal minted against svc and the
// rows this handler writes are the same store.
type fppObservationTestSetup struct {
	st  *store.Store
	svc identity.Service
}

func newFPPObservationTestSetup(t *testing.T, now func() time.Time) *fppObservationTestSetup {
	t.Helper()
	dir := t.TempDir()
	st, err := store.Open(context.Background(), filepath.Join(dir, "db"), nil, store.WithClock(now))
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(func() { _ = st.Close() })
	svc := identity.NewService(st, now, filepath.Join(dir, "data"), identity.WithLogger(testLogger()))
	return &fppObservationTestSetup{st: st, svc: svc}
}

func (s *fppObservationTestSetup) deps() Dependencies {
	return Dependencies{
		Nodes: &fakeNodeLister{}, FPP: &fakeFPPLister{}, Observations: &fakeObservationLister{},
		Events: &fakeEventReader{}, Collectors: &fakeCollectorStatusLister{},
		Identity: s.svc, FPPObservations: s.st,
	}
}

// fppObservationBody builds a valid, available-identity observation
// request body for instanceUUID/sequence, with the correct entryKey
// pre-derived so a test only overriding one field does not also have to
// re-derive the hash by hand.
func fppObservationBody(t *testing.T, instanceUUID string, sequence int64, playlistName, section string, position int) string {
	t.Helper()
	return fppObservationBodyWithAction(t, instanceUUID, sequence, playlistName, section, position, "playing")
}

// fppObservationBodyWithAction is [fppObservationBody] with an explicit
// action, for tests exercising schemaV18's entry-occurrence computation
// (fppobservations.go's handlePostFPPPlaylistEntryObservation), which
// branches on whether action is "start".
func fppObservationBodyWithAction(t *testing.T, instanceUUID string, sequence int64, playlistName, section string, position int, action string) string {
	t.Helper()
	entryKey, err := fppidentity.DeriveEntryKey(fppidentity.EntryIdentity{
		InstanceUUID: instanceUUID, PlaylistName: playlistName, PlaylistHash: playlistHash64, Section: section, Position: position,
	})
	if err != nil {
		t.Fatalf("derive entry key: %v", err)
	}
	m := map[string]any{
		"schemaVersion":                      1,
		"instanceUuid":                       instanceUUID,
		"playlistName":                       playlistName,
		"playlistHash":                       playlistHash64,
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

// playlistHash64 is a fixed, syntactically valid 64-char lowercase hex
// string standing in for a real SHA-256, this suite never asserts
// anything about ITS derivation, only that a well-formed hash flows
// through unchanged.
const playlistHash64 = "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"

func newFPPObservationRequest(t *testing.T, body, bearerToken string) *http.Request {
	t.Helper()
	req := newJSONRequest(t, http.MethodPost, "/api/v1/integrations/fpp/playlist-entry-observations", body, nil)
	if bearerToken != "" {
		req.Header.Set("Authorization", "Bearer "+bearerToken)
	}
	return req
}

func mustPostObservation(t *testing.T, api *API, body, token string) (*http.Response, map[string]any) {
	t.Helper()
	resp, raw := doRawRequest(t, api.Handler, newFPPObservationRequest(t, body, token))
	return resp, decodeMap(t, raw)
}

// --- authentication and scope ---

func TestFPPObservationPostRefusedUnauthenticated(t *testing.T) {
	setup := newFPPObservationTestSetup(t, fixedClock(testNow))
	api := New(setup.deps(), Options{Clock: fixedClock(testNow), Logger: testLogger(), CloseReads: true})

	body := fppObservationBody(t, "instance-1", 1, "showmesh-test", "main", 0)
	resp, respBody := doRawRequest(t, api.Handler, newFPPObservationRequest(t, body, ""))
	if resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("status = %d, want 401; body: %s", resp.StatusCode, respBody)
	}
}

// TestFPPObservationPostRefusedForbiddenForOperator is the build brief's
// own "a principal holding only show:macro:run is refused 403 naming
// fpp:observe" acceptance criterion. RoleOperator holds show:macro:run
// among its other action scopes and, deliberately, per
// identity.ScopeFPPObserve's own doc comment, never fpp:observe: an
// operator credential must not be able to forge plugin evidence.
func TestFPPObservationPostRefusedForbiddenForOperator(t *testing.T) {
	setup := newFPPObservationTestSetup(t, fixedClock(testNow))
	api := New(setup.deps(), Options{Clock: fixedClock(testNow), Logger: testLogger()})

	operator := mustCreatePrincipal(t, setup.svc, "operator-1", identity.RoleOperator)
	token := mustIssueToken(t, setup.svc, operator.ID)

	body := fppObservationBody(t, "instance-1", 1, "showmesh-test", "main", 0)
	resp, m := mustPostObservation(t, api, body, token)
	if resp.StatusCode != http.StatusForbidden {
		t.Fatalf("status = %d, want 403; body: %v", resp.StatusCode, m)
	}
	if got, want := fmt.Sprint(m["detail"]), string(identity.ScopeFPPObserve); !bytes.Contains([]byte(got), []byte(want)) {
		t.Errorf("detail = %q, want it to name %q", got, want)
	}
}

func TestFPPObservationPostAcceptedForSchedulerRole(t *testing.T) {
	setup := newFPPObservationTestSetup(t, fixedClock(testNow))
	api := New(setup.deps(), Options{Clock: fixedClock(testNow), Logger: testLogger()})

	scheduler := mustCreatePrincipal(t, setup.svc, "scheduler-bot", identity.RoleScheduler)
	token := mustIssueToken(t, setup.svc, scheduler.ID)

	body := fppObservationBody(t, "instance-1", 1, "showmesh-test", "main", 0)
	resp, m := mustPostObservation(t, api, body, token)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200; body: %v", resp.StatusCode, m)
	}
	if accepted, _ := m["accepted"].(bool); !accepted {
		t.Errorf("accepted = %v, want true", m["accepted"])
	}
	if replay, _ := m["replay"].(bool); replay {
		t.Errorf("replay = %v, want false", m["replay"])
	}
}

// --- acceptance, storage, and stream visibility ---

func TestFPPObservationAcceptedIsStoredAndStreamVisible(t *testing.T) {
	setup := newFPPObservationTestSetup(t, fixedClock(testNow))
	api := New(setup.deps(), Options{Clock: fixedClock(testNow), Logger: testLogger()})
	scheduler := mustCreatePrincipal(t, setup.svc, "scheduler-bot", identity.RoleScheduler)
	token := mustIssueToken(t, setup.svc, scheduler.ID)

	body := fppObservationBody(t, "instance-1", 5, "showmesh-test", "main", 2)
	resp, _ := mustPostObservation(t, api, body, token)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200", resp.StatusCode)
	}

	rec, err := setup.st.GetFPPPlaylistEntryObservation(context.Background(), "instance-1")
	if err != nil {
		t.Fatalf("GetFPPPlaylistEntryObservation: %v", err)
	}
	if rec.Sequence != 5 || rec.PlaylistName != "showmesh-test" || rec.Position != 2 {
		t.Errorf("stored record = %+v, want sequence=5 playlistName=showmesh-test position=2", rec)
	}

	hub := newHub(setup.deps().withDefaults(), Options{}.withDefaults(), testLogger())
	hub.render(context.Background())
	hub.mu.Lock()
	_, rendered := hub.lastRendered["fppobs:instance-1"]
	hub.mu.Unlock()
	if !rendered {
		t.Errorf("stream hub did not render fppobs:instance-1 after an accepted observation")
	}
}

// TestFPPObservationEntryOccurrenceSequenceTracksStartNotEveryTick proves
// schemaV18's own contract: EntryOccurrenceSequence is stable across
// ordinary "playing" ticks inside one entry occurrence, and changes on a
// genuine re-entry signalled by action "start" — including a playlist
// looping back to an entry whose EntryKey is otherwise identical to its
// first visit. This is the ingestion-side half of defect 1 (a looping
// playlist never re-activating its Cues): without it,
// [cueactivate.activationID] would either mint a new ActivationID on every
// ordinary tick (the raw wire `sequence`) or never mint a new one on a
// loop (an unconditionally stable per-entry id).
func TestFPPObservationEntryOccurrenceSequenceTracksStartNotEveryTick(t *testing.T) {
	setup := newFPPObservationTestSetup(t, fixedClock(testNow))
	api := New(setup.deps(), Options{Clock: fixedClock(testNow), Logger: testLogger()})
	scheduler := mustCreatePrincipal(t, setup.svc, "scheduler-bot", identity.RoleScheduler)
	token := mustIssueToken(t, setup.svc, scheduler.ID)

	// Entry starts at sequence 1: nothing stored yet, so this event starts
	// its own occurrence.
	start := fppObservationBodyWithAction(t, "instance-1", 1, "showmesh-test", "main", 0, "start")
	if resp, _ := mustPostObservation(t, api, start, token); resp.StatusCode != http.StatusOK {
		t.Fatalf("start post: status = %d, want 200", resp.StatusCode)
	}
	rec1, err := setup.st.GetFPPPlaylistEntryObservation(context.Background(), "instance-1")
	if err != nil {
		t.Fatalf("get after start: %v", err)
	}
	if rec1.EntryOccurrenceSequence != 1 {
		t.Fatalf("EntryOccurrenceSequence after start = %d, want 1", rec1.EntryOccurrenceSequence)
	}

	// A "playing" tick for the SAME entry, at a higher wire sequence (a
	// MultiSync position update, not a new entry): the occurrence must NOT
	// advance, or every ordinary tick would dispatch a fresh activation.
	tick := fppObservationBodyWithAction(t, "instance-1", 2, "showmesh-test", "main", 0, "playing")
	if resp, _ := mustPostObservation(t, api, tick, token); resp.StatusCode != http.StatusOK {
		t.Fatalf("playing tick post: status = %d, want 200", resp.StatusCode)
	}
	rec2, err := setup.st.GetFPPPlaylistEntryObservation(context.Background(), "instance-1")
	if err != nil {
		t.Fatalf("get after playing tick: %v", err)
	}
	if rec2.EntryOccurrenceSequence != rec1.EntryOccurrenceSequence {
		t.Fatalf("EntryOccurrenceSequence changed across a same-entry playing tick: %d -> %d", rec1.EntryOccurrenceSequence, rec2.EntryOccurrenceSequence)
	}

	// The playlist loops back to the SAME entry: FPP reports "start" again
	// for an entry whose EntryKey (derived from instanceUuid/playlistHash/
	// playlistName/section/position) is identical to the first visit. The
	// occurrence MUST advance, or a looping playlist never re-activates.
	loopStart := fppObservationBodyWithAction(t, "instance-1", 3, "showmesh-test", "main", 0, "start")
	if resp, _ := mustPostObservation(t, api, loopStart, token); resp.StatusCode != http.StatusOK {
		t.Fatalf("loop start post: status = %d, want 200", resp.StatusCode)
	}
	rec3, err := setup.st.GetFPPPlaylistEntryObservation(context.Background(), "instance-1")
	if err != nil {
		t.Fatalf("get after loop start: %v", err)
	}
	if rec3.EntryKey != rec1.EntryKey {
		t.Fatalf("test setup error: loop re-entry's EntryKey (%s) differs from the first visit's (%s)", rec3.EntryKey, rec1.EntryKey)
	}
	if rec3.EntryOccurrenceSequence == rec2.EntryOccurrenceSequence {
		t.Fatalf("EntryOccurrenceSequence did not advance on a loop re-entry (same EntryKey, action=start): stayed at %d", rec3.EntryOccurrenceSequence)
	}
	if rec3.EntryOccurrenceSequence != 3 {
		t.Fatalf("EntryOccurrenceSequence after loop start = %d, want 3 (the loop start's own wire sequence)", rec3.EntryOccurrenceSequence)
	}
}

func TestFPPObservationIdempotentReplayStoresNothingAndIsNotAccepted(t *testing.T) {
	setup := newFPPObservationTestSetup(t, fixedClock(testNow))
	api := New(setup.deps(), Options{Clock: fixedClock(testNow), Logger: testLogger()})
	scheduler := mustCreatePrincipal(t, setup.svc, "scheduler-bot", identity.RoleScheduler)
	token := mustIssueToken(t, setup.svc, scheduler.ID)

	body := fppObservationBody(t, "instance-1", 1, "showmesh-test", "main", 0)
	if resp, _ := mustPostObservation(t, api, body, token); resp.StatusCode != http.StatusOK {
		t.Fatalf("first post: status = %d, want 200", resp.StatusCode)
	}
	first, err := setup.st.GetFPPPlaylistEntryObservation(context.Background(), "instance-1")
	if err != nil {
		t.Fatalf("get after first post: %v", err)
	}

	// Identical sequence, identical canonical body: an idempotent replay.
	resp, m := mustPostObservation(t, api, body, token)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("replay post: status = %d, want 200; body: %v", resp.StatusCode, m)
	}
	if accepted, _ := m["accepted"].(bool); accepted {
		t.Errorf("accepted = %v, want false for a replay", m["accepted"])
	}
	if replay, _ := m["replay"].(bool); !replay {
		t.Errorf("replay = %v, want true", m["replay"])
	}

	second, err := setup.st.GetFPPPlaylistEntryObservation(context.Background(), "instance-1")
	if err != nil {
		t.Fatalf("get after replay: %v", err)
	}
	if second.ReceivedAt != first.ReceivedAt || second.BodyHash != first.BodyHash {
		t.Errorf("replay changed the stored row: before=%+v after=%+v", first, second)
	}
}

func TestFPPObservationSequenceRegressionRefused409(t *testing.T) {
	setup := newFPPObservationTestSetup(t, fixedClock(testNow))
	api := New(setup.deps(), Options{Clock: fixedClock(testNow), Logger: testLogger()})
	scheduler := mustCreatePrincipal(t, setup.svc, "scheduler-bot", identity.RoleScheduler)
	token := mustIssueToken(t, setup.svc, scheduler.ID)

	if resp, _ := mustPostObservation(t, api, fppObservationBody(t, "instance-1", 5, "showmesh-test", "main", 0), token); resp.StatusCode != http.StatusOK {
		t.Fatalf("seed post: status = %d, want 200", resp.StatusCode)
	}
	resp, m := mustPostObservation(t, api, fppObservationBody(t, "instance-1", 3, "showmesh-test", "main", 0), token)
	if resp.StatusCode != http.StatusConflict {
		t.Fatalf("status = %d, want 409; body: %v", resp.StatusCode, m)
	}
	if m["type"] != ProblemTypeConflict {
		t.Errorf("type = %v, want %q", m["type"], ProblemTypeConflict)
	}

	rec, err := setup.st.GetFPPPlaylistEntryObservation(context.Background(), "instance-1")
	if err != nil || rec.Sequence != 5 {
		t.Errorf("stored sequence = %v (err=%v), want untouched at 5", rec.Sequence, err)
	}
}

func TestFPPObservationSequenceConflictDifferentBodyRefused409(t *testing.T) {
	setup := newFPPObservationTestSetup(t, fixedClock(testNow))
	api := New(setup.deps(), Options{Clock: fixedClock(testNow), Logger: testLogger()})
	scheduler := mustCreatePrincipal(t, setup.svc, "scheduler-bot", identity.RoleScheduler)
	token := mustIssueToken(t, setup.svc, scheduler.ID)

	if resp, _ := mustPostObservation(t, api, fppObservationBody(t, "instance-1", 5, "showmesh-test", "main", 0), token); resp.StatusCode != http.StatusOK {
		t.Fatalf("seed post: status = %d, want 200", resp.StatusCode)
	}
	// Same sequence, different section: a different canonical body.
	resp, m := mustPostObservation(t, api, fppObservationBody(t, "instance-1", 5, "showmesh-test", "other", 0), token)
	if resp.StatusCode != http.StatusConflict {
		t.Fatalf("status = %d, want 409; body: %v", resp.StatusCode, m)
	}
	if m["type"] != ProblemTypeConflict {
		t.Errorf("type = %v, want %q", m["type"], ProblemTypeConflict)
	}
}

// --- validation refusals ---

func TestFPPObservationOversizedBodyRefused413(t *testing.T) {
	setup := newFPPObservationTestSetup(t, fixedClock(testNow))
	api := New(setup.deps(), Options{Clock: fixedClock(testNow), Logger: testLogger()})
	scheduler := mustCreatePrincipal(t, setup.svc, "scheduler-bot", identity.RoleScheduler)
	token := mustIssueToken(t, setup.svc, scheduler.ID)

	huge := make([]byte, maxFPPObservationRequestBodyBytes+1024)
	for i := range huge {
		huge[i] = 'a'
	}
	body := `{"schemaVersion":1,"instanceUuid":"instance-1","action":"playing","sequence":1,` +
		`"observedAtMillis":1,"coalescedSincePreviousAcknowledged":0,"playlistName":"` + string(huge) + `"}`
	resp, m := mustPostObservation(t, api, body, token)
	if resp.StatusCode != http.StatusRequestEntityTooLarge {
		t.Fatalf("status = %d, want 413; body: %v", resp.StatusCode, m)
	}
	if m["type"] != ProblemTypeResolumeCompositionTooLarge {
		t.Errorf("type = %v, want the shared payload-too-large type", m["type"])
	}
}

func TestFPPObservationMalformedJSONRefused400(t *testing.T) {
	setup := newFPPObservationTestSetup(t, fixedClock(testNow))
	api := New(setup.deps(), Options{Clock: fixedClock(testNow), Logger: testLogger()})
	scheduler := mustCreatePrincipal(t, setup.svc, "scheduler-bot", identity.RoleScheduler)
	token := mustIssueToken(t, setup.svc, scheduler.ID)

	resp, m := mustPostObservation(t, api, `{not valid json`, token)
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400; body: %v", resp.StatusCode, m)
	}
	if m["type"] != ProblemTypeInvalidParameter {
		t.Errorf("type = %v, want %q", m["type"], ProblemTypeInvalidParameter)
	}
}

func TestFPPObservationUnknownFieldRefused400(t *testing.T) {
	setup := newFPPObservationTestSetup(t, fixedClock(testNow))
	api := New(setup.deps(), Options{Clock: fixedClock(testNow), Logger: testLogger()})
	scheduler := mustCreatePrincipal(t, setup.svc, "scheduler-bot", identity.RoleScheduler)
	token := mustIssueToken(t, setup.svc, scheduler.ID)

	body := `{"schemaVersion":1,"instanceUuid":"instance-1","action":"playing","sequence":1,` +
		`"observedAtMillis":1,"coalescedSincePreviousAcknowledged":0,"somethingElse":true}`
	resp, m := mustPostObservation(t, api, body, token)
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400; body: %v", resp.StatusCode, m)
	}
	if m["type"] != ProblemTypeInvalidParameter {
		t.Errorf("type = %v, want %q", m["type"], ProblemTypeInvalidParameter)
	}
}

// TestFPPObservationDuplicateMemberNameRefused400 is finding 7's
// regression test: encoding/json.Decoder keeps the LAST value for a
// duplicate member name and never notices the duplicate itself, so
// without canonicalizing the raw body at step 4, this would silently
// decode using sequence 9 and never be refused at all.
func TestFPPObservationDuplicateMemberNameRefused400(t *testing.T) {
	setup := newFPPObservationTestSetup(t, fixedClock(testNow))
	api := New(setup.deps(), Options{Clock: fixedClock(testNow), Logger: testLogger()})
	scheduler := mustCreatePrincipal(t, setup.svc, "scheduler-bot", identity.RoleScheduler)
	token := mustIssueToken(t, setup.svc, scheduler.ID)

	body := `{"schemaVersion":1,"instanceUuid":"instance-1","action":"playing","sequence":1,` +
		`"sequence":9,"observedAtMillis":1,"coalescedSincePreviousAcknowledged":0}`
	resp, m := mustPostObservation(t, api, body, token)
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400; body: %v", resp.StatusCode, m)
	}
	if m["type"] != ProblemTypeInvalidParameter {
		t.Errorf("type = %v, want %q", m["type"], ProblemTypeInvalidParameter)
	}
}

// TestFPPObservationTrailingContentRefused400 is finding 7's other
// regression test: dec.More() must be checked, or trailing content after
// the JSON value silently passes decode.
func TestFPPObservationTrailingContentRefused400(t *testing.T) {
	setup := newFPPObservationTestSetup(t, fixedClock(testNow))
	api := New(setup.deps(), Options{Clock: fixedClock(testNow), Logger: testLogger()})
	scheduler := mustCreatePrincipal(t, setup.svc, "scheduler-bot", identity.RoleScheduler)
	token := mustIssueToken(t, setup.svc, scheduler.ID)

	body := `{"schemaVersion":1,"instanceUuid":"instance-1","action":"playing","sequence":1,` +
		`"observedAtMillis":1,"coalescedSincePreviousAcknowledged":0} trailing`
	resp, m := mustPostObservation(t, api, body, token)
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400; body: %v", resp.StatusCode, m)
	}
	if m["type"] != ProblemTypeInvalidParameter {
		t.Errorf("type = %v, want %q", m["type"], ProblemTypeInvalidParameter)
	}
}

func TestFPPObservationUnsupportedSchemaVersionRefused400(t *testing.T) {
	setup := newFPPObservationTestSetup(t, fixedClock(testNow))
	api := New(setup.deps(), Options{Clock: fixedClock(testNow), Logger: testLogger()})
	scheduler := mustCreatePrincipal(t, setup.svc, "scheduler-bot", identity.RoleScheduler)
	token := mustIssueToken(t, setup.svc, scheduler.ID)

	body := `{"schemaVersion":2,"instanceUuid":"instance-1","action":"playing","sequence":1,` +
		`"observedAtMillis":1,"coalescedSincePreviousAcknowledged":0}`
	resp, m := mustPostObservation(t, api, body, token)
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400; body: %v", resp.StatusCode, m)
	}
	if m["type"] != ProblemTypeUnsupportedObservationSchemaVersion {
		t.Errorf("type = %v, want %q", m["type"], ProblemTypeUnsupportedObservationSchemaVersion)
	}
}

func TestFPPObservationMissingInstanceUUIDRefused400(t *testing.T) {
	setup := newFPPObservationTestSetup(t, fixedClock(testNow))
	api := New(setup.deps(), Options{Clock: fixedClock(testNow), Logger: testLogger()})
	scheduler := mustCreatePrincipal(t, setup.svc, "scheduler-bot", identity.RoleScheduler)
	token := mustIssueToken(t, setup.svc, scheduler.ID)

	body := `{"schemaVersion":1,"instanceUuid":"","action":"playing","sequence":1,` +
		`"observedAtMillis":1,"coalescedSincePreviousAcknowledged":0}`
	resp, m := mustPostObservation(t, api, body, token)
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400; body: %v", resp.StatusCode, m)
	}
	if m["type"] != ProblemTypeInvalidParameter {
		t.Errorf("type = %v, want %q", m["type"], ProblemTypeInvalidParameter)
	}
}

func TestFPPObservationInvalidActionRefused400(t *testing.T) {
	setup := newFPPObservationTestSetup(t, fixedClock(testNow))
	api := New(setup.deps(), Options{Clock: fixedClock(testNow), Logger: testLogger()})
	scheduler := mustCreatePrincipal(t, setup.svc, "scheduler-bot", identity.RoleScheduler)
	token := mustIssueToken(t, setup.svc, scheduler.ID)

	body := `{"schemaVersion":1,"instanceUuid":"instance-1","action":"bogus","sequence":1,` +
		`"observedAtMillis":1,"coalescedSincePreviousAcknowledged":0}`
	resp, m := mustPostObservation(t, api, body, token)
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400; body: %v", resp.StatusCode, m)
	}
	if m["type"] != ProblemTypeInvalidParameter {
		t.Errorf("type = %v, want %q", m["type"], ProblemTypeInvalidParameter)
	}
}

func TestFPPObservationMalformedHashRefused400(t *testing.T) {
	setup := newFPPObservationTestSetup(t, fixedClock(testNow))
	api := New(setup.deps(), Options{Clock: fixedClock(testNow), Logger: testLogger()})
	scheduler := mustCreatePrincipal(t, setup.svc, "scheduler-bot", identity.RoleScheduler)
	token := mustIssueToken(t, setup.svc, scheduler.ID)

	body := `{"schemaVersion":1,"instanceUuid":"instance-1","action":"playing","sequence":1,` +
		`"observedAtMillis":1,"coalescedSincePreviousAcknowledged":0,"playlistHash":"not-a-hash"}`
	resp, m := mustPostObservation(t, api, body, token)
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400; body: %v", resp.StatusCode, m)
	}
	if m["type"] != ProblemTypeInvalidParameter {
		t.Errorf("type = %v, want %q", m["type"], ProblemTypeInvalidParameter)
	}
}

func TestFPPObservationNegativePositionRefused400(t *testing.T) {
	setup := newFPPObservationTestSetup(t, fixedClock(testNow))
	api := New(setup.deps(), Options{Clock: fixedClock(testNow), Logger: testLogger()})
	scheduler := mustCreatePrincipal(t, setup.svc, "scheduler-bot", identity.RoleScheduler)
	token := mustIssueToken(t, setup.svc, scheduler.ID)

	body := `{"schemaVersion":1,"instanceUuid":"instance-1","action":"playing","sequence":1,` +
		`"observedAtMillis":1,"coalescedSincePreviousAcknowledged":0,"position":-1}`
	resp, m := mustPostObservation(t, api, body, token)
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400; body: %v", resp.StatusCode, m)
	}
	if m["type"] != ProblemTypeInvalidParameter {
		t.Errorf("type = %v, want %q", m["type"], ProblemTypeInvalidParameter)
	}
}

func TestFPPObservationNegativeSequenceRefused400(t *testing.T) {
	setup := newFPPObservationTestSetup(t, fixedClock(testNow))
	api := New(setup.deps(), Options{Clock: fixedClock(testNow), Logger: testLogger()})
	scheduler := mustCreatePrincipal(t, setup.svc, "scheduler-bot", identity.RoleScheduler)
	token := mustIssueToken(t, setup.svc, scheduler.ID)

	body := `{"schemaVersion":1,"instanceUuid":"instance-1","action":"playing","sequence":-1,` +
		`"observedAtMillis":1,"coalescedSincePreviousAcknowledged":0}`
	resp, m := mustPostObservation(t, api, body, token)
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400; body: %v", resp.StatusCode, m)
	}
	if m["type"] != ProblemTypeInvalidParameter {
		t.Errorf("type = %v, want %q", m["type"], ProblemTypeInvalidParameter)
	}
}

func TestFPPObservationEntryKeyMismatchRefused400(t *testing.T) {
	setup := newFPPObservationTestSetup(t, fixedClock(testNow))
	api := New(setup.deps(), Options{Clock: fixedClock(testNow), Logger: testLogger()})
	scheduler := mustCreatePrincipal(t, setup.svc, "scheduler-bot", identity.RoleScheduler)
	token := mustIssueToken(t, setup.svc, scheduler.ID)

	wrongKey := "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"
	body := `{"schemaVersion":1,"instanceUuid":"instance-1","playlistName":"showmesh-test",` +
		`"playlistHash":"` + playlistHash64 + `","section":"main","position":0,"entryKey":"` + wrongKey + `",` +
		`"action":"playing","sequence":1,"observedAtMillis":1,"coalescedSincePreviousAcknowledged":0}`
	resp, m := mustPostObservation(t, api, body, token)
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400; body: %v", resp.StatusCode, m)
	}
	if m["type"] != ProblemTypeObservationEntryKeyMismatch {
		t.Errorf("type = %v, want %q", m["type"], ProblemTypeObservationEntryKeyMismatch)
	}
}

// --- finding 1: unavailable observations must not carry derived identity ---

func TestFPPObservationUnavailableWithPlaylistHashRefused400(t *testing.T) {
	setup := newFPPObservationTestSetup(t, fixedClock(testNow))
	api := New(setup.deps(), Options{Clock: fixedClock(testNow), Logger: testLogger()})
	scheduler := mustCreatePrincipal(t, setup.svc, "scheduler-bot", identity.RoleScheduler)
	token := mustIssueToken(t, setup.svc, scheduler.ID)

	body := `{"schemaVersion":1,"instanceUuid":"instance-1","action":"playing","sequence":1,` +
		`"observedAtMillis":1,"coalescedSincePreviousAcknowledged":0,"unavailable":"missing_playlist_name",` +
		`"playlistHash":"` + playlistHash64 + `"}`
	resp, m := mustPostObservation(t, api, body, token)
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400; body: %v", resp.StatusCode, m)
	}
	if m["type"] != ProblemTypeInvalidParameter {
		t.Errorf("type = %v, want %q", m["type"], ProblemTypeInvalidParameter)
	}
}

func TestFPPObservationUnavailableWithEntryKeyRefused400(t *testing.T) {
	setup := newFPPObservationTestSetup(t, fixedClock(testNow))
	api := New(setup.deps(), Options{Clock: fixedClock(testNow), Logger: testLogger()})
	scheduler := mustCreatePrincipal(t, setup.svc, "scheduler-bot", identity.RoleScheduler)
	token := mustIssueToken(t, setup.svc, scheduler.ID)

	body := `{"schemaVersion":1,"instanceUuid":"instance-1","action":"playing","sequence":1,` +
		`"observedAtMillis":1,"coalescedSincePreviousAcknowledged":0,"unavailable":"missing_playlist_name",` +
		`"entryKey":"bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"}`
	resp, m := mustPostObservation(t, api, body, token)
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400; body: %v", resp.StatusCode, m)
	}
	if m["type"] != ProblemTypeInvalidParameter {
		t.Errorf("type = %v, want %q", m["type"], ProblemTypeInvalidParameter)
	}
}

// --- finding 2: available-identity observations must carry the whole identity ---

// TestFPPObservationAvailableWithPrecomputedEmptyIdentityHashRefused400 is
// the exact bypass finding 2 describes: unavailable is absent, every
// identity field except entryKey is absent, and entryKey carries the
// correctly re-derived hash of the all-empty five-member object, so the
// mismatch check alone would have accepted it. Presence is now checked
// before re-derivation ever runs.
func TestFPPObservationAvailableWithPrecomputedEmptyIdentityHashRefused400(t *testing.T) {
	setup := newFPPObservationTestSetup(t, fixedClock(testNow))
	api := New(setup.deps(), Options{Clock: fixedClock(testNow), Logger: testLogger()})
	scheduler := mustCreatePrincipal(t, setup.svc, "scheduler-bot", identity.RoleScheduler)
	token := mustIssueToken(t, setup.svc, scheduler.ID)

	entryKey, err := fppidentity.DeriveEntryKey(fppidentity.EntryIdentity{InstanceUUID: "instance-1"})
	if err != nil {
		t.Fatalf("derive entry key: %v", err)
	}
	body := `{"schemaVersion":1,"instanceUuid":"instance-1","entryKey":"` + entryKey + `",` +
		`"action":"playing","sequence":1,"observedAtMillis":1,"coalescedSincePreviousAcknowledged":0}`
	resp, m := mustPostObservation(t, api, body, token)
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400; body: %v", resp.StatusCode, m)
	}
	if m["type"] != ProblemTypeInvalidParameter {
		t.Errorf("type = %v, want %q", m["type"], ProblemTypeInvalidParameter)
	}
}

func TestFPPObservationAvailableWithEmptySectionAccepted(t *testing.T) {
	setup := newFPPObservationTestSetup(t, fixedClock(testNow))
	api := New(setup.deps(), Options{Clock: fixedClock(testNow), Logger: testLogger()})
	scheduler := mustCreatePrincipal(t, setup.svc, "scheduler-bot", identity.RoleScheduler)
	token := mustIssueToken(t, setup.svc, scheduler.ID)

	body := fppObservationBody(t, "instance-1", 1, "showmesh-test", "", 0)
	resp, m := mustPostObservation(t, api, body, token)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200; body: %v", resp.StatusCode, m)
	}
	if accepted, _ := m["accepted"].(bool); !accepted {
		t.Errorf("accepted = %v, want true", m["accepted"])
	}
}

// --- unavailable observations: accepted, not refused ---

func TestFPPObservationUnavailableReasonsAreAcceptedAndStored(t *testing.T) {
	reasons := []string{
		"missing_instance_uuid", // reportable only alongside another failure; instanceUuid is present here
		"missing_playlist_name",
		"missing_definition",
		"unsupported_definition_shape",
		"negative_position",
		"truncated_identity_field",
	}
	for i, reason := range reasons {
		t.Run(reason, func(t *testing.T) {
			setup := newFPPObservationTestSetup(t, fixedClock(testNow))
			api := New(setup.deps(), Options{Clock: fixedClock(testNow), Logger: testLogger()})
			scheduler := mustCreatePrincipal(t, setup.svc, "scheduler-bot", identity.RoleScheduler)
			token := mustIssueToken(t, setup.svc, scheduler.ID)

			instanceID := fmt.Sprintf("instance-unavail-%d", i)
			body := `{"schemaVersion":1,"instanceUuid":"` + instanceID + `","action":"playing","sequence":1,` +
				`"observedAtMillis":1,"coalescedSincePreviousAcknowledged":0,"unavailable":"` + reason + `"}`
			resp, m := mustPostObservation(t, api, body, token)
			if resp.StatusCode != http.StatusOK {
				t.Fatalf("status = %d, want 200; body: %v", resp.StatusCode, m)
			}
			if accepted, _ := m["accepted"].(bool); !accepted {
				t.Errorf("accepted = %v, want true", m["accepted"])
			}

			rec, err := setup.st.GetFPPPlaylistEntryObservation(context.Background(), instanceID)
			if err != nil {
				t.Fatalf("GetFPPPlaylistEntryObservation: %v", err)
			}
			if rec.Unavailable != reason {
				t.Errorf("stored unavailable = %q, want %q", rec.Unavailable, reason)
			}
		})
	}
}

func TestFPPObservationUnavailableWithNoInstanceUUIDRefused400(t *testing.T) {
	setup := newFPPObservationTestSetup(t, fixedClock(testNow))
	api := New(setup.deps(), Options{Clock: fixedClock(testNow), Logger: testLogger()})
	scheduler := mustCreatePrincipal(t, setup.svc, "scheduler-bot", identity.RoleScheduler)
	token := mustIssueToken(t, setup.svc, scheduler.ID)

	body := `{"schemaVersion":1,"instanceUuid":"","action":"playing","sequence":1,` +
		`"observedAtMillis":1,"coalescedSincePreviousAcknowledged":0,"unavailable":"missing_instance_uuid"}`
	resp, m := mustPostObservation(t, api, body, token)
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400; body: %v", resp.StatusCode, m)
	}
	if m["type"] != ProblemTypeInvalidParameter {
		t.Errorf("type = %v, want %q", m["type"], ProblemTypeInvalidParameter)
	}
}

// --- ingestion grants no execution authority ---

// TestFPPObservationIngestionWritesNoExecutionSideEffects is the "grants
// no execution authority" acceptance criterion (contract §1.6's closing
// rule), asserted by absence: after an accepted observation, this
// coordinator's own command and macro-run tables, the only two dispatch
// surfaces anything in this package can reach a cue or a show action
// through, hold exactly as many rows as before this handler ran (zero).
func TestFPPObservationIngestionWritesNoExecutionSideEffects(t *testing.T) {
	setup := newFPPObservationTestSetup(t, fixedClock(testNow))
	api := New(setup.deps(), Options{Clock: fixedClock(testNow), Logger: testLogger()})
	scheduler := mustCreatePrincipal(t, setup.svc, "scheduler-bot", identity.RoleScheduler)
	token := mustIssueToken(t, setup.svc, scheduler.ID)

	body := fppObservationBody(t, "instance-1", 1, "showmesh-test", "main", 0)
	if resp, _ := mustPostObservation(t, api, body, token); resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200", resp.StatusCode)
	}

	cmds, err := setup.st.ListCommands(context.Background(), 100)
	if err != nil {
		t.Fatalf("ListCommands: %v", err)
	}
	if len(cmds) != 0 {
		t.Errorf("ListCommands = %d rows, want 0, ingestion must dispatch nothing", len(cmds))
	}
	runs, err := setup.st.ListRunningMacroRuns(context.Background())
	if err != nil {
		t.Fatalf("ListRunningMacroRuns: %v", err)
	}
	if len(runs) != 0 {
		t.Errorf("ListRunningMacroRuns = %d rows, want 0, ingestion must run no macro", len(runs))
	}
}

// --- audit: every refusal from step 5 onward is audited under
// fpp.observe_playlist_entry, step 4 and an accepted observation are not
// (contract §1.6, closing paragraph) ---

// fppAuditEntriesForObservation returns every audit entry svc holds under
// this endpoint's fixed action, so a test can assert on count and
// OutcomeReason without also having to keep this file's refusal prose in
// sync with the handler's.
func fppAuditEntriesForObservation(t *testing.T, svc identity.Service) []identity.AuditEntry {
	t.Helper()
	entries, err := svc.ListAudit(context.Background(), 0, 100)
	if err != nil {
		t.Fatalf("ListAudit: %v", err)
	}
	var matched []identity.AuditEntry
	for _, e := range entries {
		if e.Action == auditActionFPPObservePlaylistEntry {
			matched = append(matched, e)
		}
	}
	return matched
}

// TestFPPObservationStepFiveThroughNineRefusalsAreAuditedWithReason is
// finding 1's own regression test: the handler has fifteen auditRefusal
// call sites and, before this test, none of them was asserted, so
// dropping any one of them kept every test in this file green. Each case
// removes ONE call site's worth of coverage if deleted; the self-check in
// this task's own final report proves that by commenting one out and
// watching this test (not some other one) fail.
func TestFPPObservationStepFiveThroughNineRefusalsAreAuditedWithReason(t *testing.T) {
	wrongKey := "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"
	cases := []struct {
		name       string
		body       func(uuid string) string
		wantReason string
	}{
		{
			name: "unsupported schemaVersion",
			body: func(uuid string) string {
				return `{"schemaVersion":2,"instanceUuid":"` + uuid + `","action":"playing","sequence":1,` +
					`"observedAtMillis":1,"coalescedSincePreviousAcknowledged":0}`
			},
			wantReason: "unsupported schemaVersion",
		},
		{
			name: "missing instanceUuid",
			body: func(string) string {
				return `{"schemaVersion":1,"instanceUuid":"","action":"playing","sequence":1,` +
					`"observedAtMillis":1,"coalescedSincePreviousAcknowledged":0}`
			},
			wantReason: "missing instanceUuid",
		},
		{
			name: "invalid action",
			body: func(uuid string) string {
				return `{"schemaVersion":1,"instanceUuid":"` + uuid + `","action":"bogus","sequence":1,` +
					`"observedAtMillis":1,"coalescedSincePreviousAcknowledged":0}`
			},
			wantReason: "invalid action",
		},
		{
			name: "invalid unavailable reason",
			body: func(uuid string) string {
				return `{"schemaVersion":1,"instanceUuid":"` + uuid + `","action":"playing","sequence":1,` +
					`"observedAtMillis":1,"coalescedSincePreviousAcknowledged":0,"unavailable":"bogus"}`
			},
			wantReason: "invalid unavailable reason",
		},
		{
			name: "negative position",
			body: func(uuid string) string {
				return `{"schemaVersion":1,"instanceUuid":"` + uuid + `","action":"playing","sequence":1,` +
					`"observedAtMillis":1,"coalescedSincePreviousAcknowledged":0,"position":-1}`
			},
			wantReason: "negative position",
		},
		{
			name: "malformed playlistHash",
			body: func(uuid string) string {
				return `{"schemaVersion":1,"instanceUuid":"` + uuid + `","action":"playing","sequence":1,` +
					`"observedAtMillis":1,"coalescedSincePreviousAcknowledged":0,"playlistHash":"not-a-hash"}`
			},
			wantReason: "malformed playlistHash",
		},
		{
			name: "malformed entryKey",
			body: func(uuid string) string {
				return `{"schemaVersion":1,"instanceUuid":"` + uuid + `","action":"playing","sequence":1,` +
					`"observedAtMillis":1,"coalescedSincePreviousAcknowledged":0,"entryKey":"not-a-hash"}`
			},
			wantReason: "malformed entryKey",
		},
		{
			name: "negative coalescedSincePreviousAcknowledged",
			body: func(uuid string) string {
				return `{"schemaVersion":1,"instanceUuid":"` + uuid + `","action":"playing","sequence":1,` +
					`"observedAtMillis":1,"coalescedSincePreviousAcknowledged":-1}`
			},
			wantReason: "negative coalescedSincePreviousAcknowledged",
		},
		{
			name: "negative sequence",
			body: func(uuid string) string {
				return `{"schemaVersion":1,"instanceUuid":"` + uuid + `","action":"playing","sequence":-1,` +
					`"observedAtMillis":1,"coalescedSincePreviousAcknowledged":0}`
			},
			wantReason: "negative sequence",
		},
		{
			name: "derived identity present with unavailable set",
			body: func(uuid string) string {
				return `{"schemaVersion":1,"instanceUuid":"` + uuid + `","action":"playing","sequence":1,` +
					`"observedAtMillis":1,"coalescedSincePreviousAcknowledged":0,"unavailable":"missing_playlist_name",` +
					`"playlistHash":"` + playlistHash64 + `"}`
			},
			wantReason: "derived identity present with unavailable set",
		},
		{
			name: "missing identity field with unavailable absent",
			body: func(uuid string) string {
				return `{"schemaVersion":1,"instanceUuid":"` + uuid + `","action":"playing","sequence":1,` +
					`"observedAtMillis":1,"coalescedSincePreviousAcknowledged":0}`
			},
			wantReason: "missing identity field with unavailable absent",
		},
		{
			name: "entry key mismatch",
			body: func(uuid string) string {
				return `{"schemaVersion":1,"instanceUuid":"` + uuid + `","playlistName":"showmesh-test",` +
					`"playlistHash":"` + playlistHash64 + `","section":"main","position":0,"entryKey":"` + wrongKey + `",` +
					`"action":"playing","sequence":1,"observedAtMillis":1,"coalescedSincePreviousAcknowledged":0}`
			},
			wantReason: "entry key mismatch",
		},
	}

	for i, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			setup := newFPPObservationTestSetup(t, fixedClock(testNow))
			api := New(setup.deps(), Options{Clock: fixedClock(testNow), Logger: testLogger()})
			scheduler := mustCreatePrincipal(t, setup.svc, "scheduler-bot", identity.RoleScheduler)
			token := mustIssueToken(t, setup.svc, scheduler.ID)

			uuid := fmt.Sprintf("instance-audit-%d", i)
			resp, m := mustPostObservation(t, api, tc.body(uuid), token)
			if resp.StatusCode != http.StatusBadRequest {
				t.Fatalf("status = %d, want 400; body: %v", resp.StatusCode, m)
			}

			entries := fppAuditEntriesForObservation(t, setup.svc)
			if len(entries) != 1 {
				t.Fatalf("audit entries for %q = %d, want exactly 1: %+v", auditActionFPPObservePlaylistEntry, len(entries), entries)
			}
			if entries[0].OutcomeReason != tc.wantReason {
				t.Errorf("audit OutcomeReason = %q, want %q", entries[0].OutcomeReason, tc.wantReason)
			}
			if entries[0].Kind != identity.AuditOutcome {
				t.Errorf("audit Kind = %v, want %v", entries[0].Kind, identity.AuditOutcome)
			}
		})
	}
}

// TestFPPObservationSequenceRegressionIsAuditedWithReason is the step 9
// half finding 1's table above cannot cover in one request: the refusal
// reason names the last accepted sequence, which only exists once a prior
// observation has been accepted.
func TestFPPObservationSequenceRegressionIsAuditedWithReason(t *testing.T) {
	setup := newFPPObservationTestSetup(t, fixedClock(testNow))
	api := New(setup.deps(), Options{Clock: fixedClock(testNow), Logger: testLogger()})
	scheduler := mustCreatePrincipal(t, setup.svc, "scheduler-bot", identity.RoleScheduler)
	token := mustIssueToken(t, setup.svc, scheduler.ID)

	uuid := "instance-audit-seq-regression"
	seed := fppObservationBody(t, uuid, 5, "showmesh-test", "main", 0)
	if resp, _ := mustPostObservation(t, api, seed, token); resp.StatusCode != http.StatusOK {
		t.Fatalf("seed post: want 200")
	}
	lower := fppObservationBody(t, uuid, 3, "showmesh-test", "main", 0)
	resp, m := mustPostObservation(t, api, lower, token)
	if resp.StatusCode != http.StatusConflict {
		t.Fatalf("status = %d, want 409; body: %v", resp.StatusCode, m)
	}

	entries := fppAuditEntriesForObservation(t, setup.svc)
	if len(entries) != 1 {
		t.Fatalf("audit entries for %q = %d, want exactly 1 (the seed accept must not audit): %+v",
			auditActionFPPObservePlaylistEntry, len(entries), entries)
	}
	const wantReason = "sequence regression: last accepted sequence was 5"
	if entries[0].OutcomeReason != wantReason {
		t.Errorf("audit OutcomeReason = %q, want %q", entries[0].OutcomeReason, wantReason)
	}
}

// TestFPPObservationSequenceReusedWithDifferentBodyIsAuditedWithReason is
// the step 9 sibling of the regression case above: the same sequence
// reused with a different canonical body.
func TestFPPObservationSequenceReusedWithDifferentBodyIsAuditedWithReason(t *testing.T) {
	setup := newFPPObservationTestSetup(t, fixedClock(testNow))
	api := New(setup.deps(), Options{Clock: fixedClock(testNow), Logger: testLogger()})
	scheduler := mustCreatePrincipal(t, setup.svc, "scheduler-bot", identity.RoleScheduler)
	token := mustIssueToken(t, setup.svc, scheduler.ID)

	uuid := "instance-audit-seq-reused"
	seed := fppObservationBody(t, uuid, 5, "showmesh-test", "main", 0)
	if resp, _ := mustPostObservation(t, api, seed, token); resp.StatusCode != http.StatusOK {
		t.Fatalf("seed post: want 200")
	}
	// Same sequence, different section: a different canonical body.
	reused := fppObservationBody(t, uuid, 5, "showmesh-test", "other", 0)
	resp, m := mustPostObservation(t, api, reused, token)
	if resp.StatusCode != http.StatusConflict {
		t.Fatalf("status = %d, want 409; body: %v", resp.StatusCode, m)
	}

	entries := fppAuditEntriesForObservation(t, setup.svc)
	if len(entries) != 1 {
		t.Fatalf("audit entries for %q = %d, want exactly 1 (the seed accept must not audit): %+v",
			auditActionFPPObservePlaylistEntry, len(entries), entries)
	}
	const wantReason = "sequence 5 reused with a different body"
	if entries[0].OutcomeReason != wantReason {
		t.Errorf("audit OutcomeReason = %q, want %q", entries[0].OutcomeReason, wantReason)
	}
}

// TestFPPObservationDecodeRefusalsWriteNoAuditEntry is finding 1's other
// half: contract §1.6 is explicit that step 4 (malformed JSON, an unknown
// field, trailing content, or a duplicate member name) is NOT audited.
// Without this, someone could "fix" the classification later by auditing
// everything, and nothing here would notice.
func TestFPPObservationDecodeRefusalsWriteNoAuditEntry(t *testing.T) {
	cases := []struct {
		name string
		body string
	}{
		{
			name: "malformed JSON",
			body: `{not valid json`,
		},
		{
			name: "unknown field",
			body: `{"schemaVersion":1,"instanceUuid":"instance-1","action":"playing","sequence":1,` +
				`"observedAtMillis":1,"coalescedSincePreviousAcknowledged":0,"somethingElse":true}`,
		},
		{
			name: "trailing content",
			body: `{"schemaVersion":1,"instanceUuid":"instance-1","action":"playing","sequence":1,` +
				`"observedAtMillis":1,"coalescedSincePreviousAcknowledged":0} trailing`,
		},
		{
			name: "duplicate member name",
			body: `{"schemaVersion":1,"instanceUuid":"instance-1","action":"playing","sequence":1,` +
				`"sequence":9,"observedAtMillis":1,"coalescedSincePreviousAcknowledged":0}`,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			setup := newFPPObservationTestSetup(t, fixedClock(testNow))
			api := New(setup.deps(), Options{Clock: fixedClock(testNow), Logger: testLogger()})
			scheduler := mustCreatePrincipal(t, setup.svc, "scheduler-bot", identity.RoleScheduler)
			token := mustIssueToken(t, setup.svc, scheduler.ID)

			resp, m := mustPostObservation(t, api, tc.body, token)
			if resp.StatusCode != http.StatusBadRequest {
				t.Fatalf("status = %d, want 400; body: %v", resp.StatusCode, m)
			}

			if entries := fppAuditEntriesForObservation(t, setup.svc); len(entries) != 0 {
				t.Errorf("audit entries for %q = %d, want 0 (step 4 is not audited): %+v",
					auditActionFPPObservePlaylistEntry, len(entries), entries)
			}
		})
	}
}

// TestFPPObservationAcceptedWritesNoAuditEntry is the third rule in
// contract §1.6's closing paragraph: "a per-entry audit entry would flood
// it during an ordinary show," so an accepted observation writes none.
func TestFPPObservationAcceptedWritesNoAuditEntry(t *testing.T) {
	setup := newFPPObservationTestSetup(t, fixedClock(testNow))
	api := New(setup.deps(), Options{Clock: fixedClock(testNow), Logger: testLogger()})
	scheduler := mustCreatePrincipal(t, setup.svc, "scheduler-bot", identity.RoleScheduler)
	token := mustIssueToken(t, setup.svc, scheduler.ID)

	body := fppObservationBody(t, "instance-1", 1, "showmesh-test", "main", 0)
	resp, m := mustPostObservation(t, api, body, token)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200; body: %v", resp.StatusCode, m)
	}

	if entries := fppAuditEntriesForObservation(t, setup.svc); len(entries) != 0 {
		t.Errorf("audit entries for %q = %d, want 0 (accepted observations are not audited): %+v",
			auditActionFPPObservePlaylistEntry, len(entries), entries)
	}
}

// --- GET ---

func TestFPPObservationListReturnsLatestPerInstance(t *testing.T) {
	setup := newFPPObservationTestSetup(t, fixedClock(testNow))
	api := New(setup.deps(), Options{Clock: fixedClock(testNow), Logger: testLogger()})
	scheduler := mustCreatePrincipal(t, setup.svc, "scheduler-bot", identity.RoleScheduler)
	token := mustIssueToken(t, setup.svc, scheduler.ID)

	if resp, _ := mustPostObservation(t, api, fppObservationBody(t, "instance-1", 1, "showmesh-test", "main", 0), token); resp.StatusCode != http.StatusOK {
		t.Fatalf("post: status = %d, want 200", resp.StatusCode)
	}
	resp, body := doRequest(t, api.Handler, http.MethodGet, "/api/v1/integrations/fpp/playlist-entry-observations", nil)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200; body: %s", resp.StatusCode, body)
	}
	m := decodeMap(t, body)
	obs, _ := m["observations"].([]any)
	if len(obs) != 1 {
		t.Fatalf("observations = %d, want 1; body: %s", len(obs), body)
	}
}
