package api

import (
	"net/http"
	"testing"

	"github.com/showmeshsystems/showmesh/internal/coordinator/identity"
)

// This file is the playlist-entry observation conformance check, following
// openapi_fppcommand_test.go's own scaffolding: real responses from a
// real [API], validated against api/openapi.yaml's own schemas for
// POST/GET /integrations/fpp/playlist-entry-observations.

func TestOpenAPIFPPObservationAcceptedResponseMatchesRealResponse(t *testing.T) {
	c := newOpenAPICompiler(t)
	setup := newFPPObservationTestSetup(t, fixedClock(testNow))
	api := New(setup.deps(), Options{Clock: fixedClock(testNow), Logger: testLogger()})
	scheduler := mustCreatePrincipal(t, setup.svc, "scheduler-bot", identity.RoleScheduler)
	token := mustIssueToken(t, setup.svc, scheduler.ID)

	body := fppObservationBody(t, "instance-1", 1, "showmesh-test", "main", 0)
	assertMatchesSchema(t, c, "FPPPlaylistEntryObservationRequest", []byte(body))

	resp, respBody := doRawRequest(t, api.Handler, newFPPObservationRequest(t, body, token))
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200; body: %s", resp.StatusCode, respBody)
	}
	assertMatchesSchema(t, c, "FPPPlaylistEntryObservationResponse", respBody)
}

func TestOpenAPIFPPObservationReplayResponseMatchesRealResponse(t *testing.T) {
	c := newOpenAPICompiler(t)
	setup := newFPPObservationTestSetup(t, fixedClock(testNow))
	api := New(setup.deps(), Options{Clock: fixedClock(testNow), Logger: testLogger()})
	scheduler := mustCreatePrincipal(t, setup.svc, "scheduler-bot", identity.RoleScheduler)
	token := mustIssueToken(t, setup.svc, scheduler.ID)

	body := fppObservationBody(t, "instance-1", 1, "showmesh-test", "main", 0)
	if resp, _ := mustPostObservation(t, api, body, token); resp.StatusCode != http.StatusOK {
		t.Fatalf("seed post: status = %d, want 200", resp.StatusCode)
	}

	resp, respBody := doRawRequest(t, api.Handler, newFPPObservationRequest(t, body, token))
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("replay: status = %d, want 200; body: %s", resp.StatusCode, respBody)
	}
	assertMatchesSchema(t, c, "FPPPlaylistEntryObservationResponse", respBody)
}

func TestOpenAPIFPPObservationEntryKeyMismatchResponseMatchesRealResponse(t *testing.T) {
	c := newOpenAPICompiler(t)
	setup := newFPPObservationTestSetup(t, fixedClock(testNow))
	api := New(setup.deps(), Options{Clock: fixedClock(testNow), Logger: testLogger()})
	scheduler := mustCreatePrincipal(t, setup.svc, "scheduler-bot", identity.RoleScheduler)
	token := mustIssueToken(t, setup.svc, scheduler.ID)

	wrongKey := "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"
	body := `{"schemaVersion":1,"instanceUuid":"instance-1","playlistName":"showmesh-test",` +
		`"playlistHash":"` + playlistHash64 + `","section":"main","position":0,"entryKey":"` + wrongKey + `",` +
		`"action":"playing","sequence":1,"observedAtMillis":1,"coalescedSincePreviousAcknowledged":0}`

	resp, respBody := doRawRequest(t, api.Handler, newFPPObservationRequest(t, body, token))
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400; body: %s", resp.StatusCode, respBody)
	}
	assertMatchesSchema(t, c, "Problem", respBody)

	m := decodeMap(t, respBody)
	if m["type"] != ProblemTypeObservationEntryKeyMismatch {
		t.Errorf("type = %v, want %v", m["type"], ProblemTypeObservationEntryKeyMismatch)
	}
}

func TestOpenAPIFPPObservationSequenceRegressionResponseMatchesRealResponse(t *testing.T) {
	c := newOpenAPICompiler(t)
	setup := newFPPObservationTestSetup(t, fixedClock(testNow))
	api := New(setup.deps(), Options{Clock: fixedClock(testNow), Logger: testLogger()})
	scheduler := mustCreatePrincipal(t, setup.svc, "scheduler-bot", identity.RoleScheduler)
	token := mustIssueToken(t, setup.svc, scheduler.ID)

	if resp, _ := mustPostObservation(t, api, fppObservationBody(t, "instance-1", 5, "showmesh-test", "main", 0), token); resp.StatusCode != http.StatusOK {
		t.Fatalf("seed post: status = %d, want 200", resp.StatusCode)
	}
	resp, respBody := doRawRequest(t, api.Handler, newFPPObservationRequest(t, fppObservationBody(t, "instance-1", 3, "showmesh-test", "main", 0), token))
	if resp.StatusCode != http.StatusConflict {
		t.Fatalf("status = %d, want 409; body: %s", resp.StatusCode, respBody)
	}
	assertMatchesSchema(t, c, "Problem", respBody)

	m := decodeMap(t, respBody)
	if m["type"] != ProblemTypeConflict {
		t.Errorf("type = %v, want %v", m["type"], ProblemTypeConflict)
	}
}

func TestOpenAPIFPPObservationListResponseMatchesRealResponse(t *testing.T) {
	c := newOpenAPICompiler(t)
	setup := newFPPObservationTestSetup(t, fixedClock(testNow))
	api := New(setup.deps(), Options{Clock: fixedClock(testNow), Logger: testLogger()})
	scheduler := mustCreatePrincipal(t, setup.svc, "scheduler-bot", identity.RoleScheduler)
	token := mustIssueToken(t, setup.svc, scheduler.ID)

	if resp, _ := mustPostObservation(t, api, fppObservationBody(t, "instance-1", 1, "showmesh-test", "main", 0), token); resp.StatusCode != http.StatusOK {
		t.Fatalf("seed post: status = %d, want 200", resp.StatusCode)
	}

	resp, respBody := doRequest(t, api.Handler, http.MethodGet, "/api/v1/integrations/fpp/playlist-entry-observations", nil)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200; body: %s", resp.StatusCode, respBody)
	}
	assertMatchesSchema(t, c, "FPPPlaylistEntryObservationsResponse", respBody)
}
