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
		"action":                             "playing",
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
