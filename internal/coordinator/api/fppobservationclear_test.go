package api

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/showmeshsystems/showmesh/internal/coordinator/identity"
	"github.com/showmeshsystems/showmesh/internal/coordinator/store"
)

// These tests cover the two ways a stored observation is cleared besides the
// per-instance reset: a confirmed re-pair and the bulk clear of refused rows.

type observationClearRig struct {
	api       *API
	setup     *fppCommandTestSetup
	admin     string
	scheduler string
}

func newObservationClearRig(t *testing.T, endpoints ...string) *observationClearRig {
	t.Helper()
	setup := newFPPCommandTestSetup(t, fixedClock(testNow))
	for _, ep := range endpoints {
		setup.fppLister.views = append(setup.fppLister.views, FPPInstanceView{InstanceID: ep, Endpoint: "http://" + ep + ".invalid"})
	}
	deps := setup.deps()
	deps.FPPObservations = setup.st
	deps.FPPReconciliation = StoreFPPReconciliation{Store: setup.st}
	api := New(deps, Options{Clock: fixedClock(testNow), Logger: testLogger()})
	admin := mustCreatePrincipal(t, setup.svc, "admin-1", identity.RoleAdmin)
	scheduler := mustCreatePrincipal(t, setup.svc, "scheduler-bot", identity.RoleScheduler)
	return &observationClearRig{
		api: api, setup: setup,
		admin:     mustIssueToken(t, setup.svc, admin.ID),
		scheduler: mustIssueToken(t, setup.svc, scheduler.ID),
	}
}

func (r *observationClearRig) mapUUID(t *testing.T, endpointID, uuid string) {
	t.Helper()
	if _, _, err := r.setup.st.RecordFPPInstanceUUIDObservation(context.Background(), endpointID, uuid, testNow); err != nil {
		t.Fatalf("map %s to %s: %v", endpointID, uuid, err)
	}
}

func (r *observationClearRig) post(t *testing.T, uuid string, sequence int64) (int, map[string]any) {
	t.Helper()
	resp, m := mustPostObservation(t, r.api, fppObservationBody(t, uuid, sequence, "showmesh-test", "main", 0), r.scheduler)
	return resp.StatusCode, m
}

// refuse stores sequence 47 for uuid, then posts 13 so the row carries the refusal marker.
func (r *observationClearRig) refuse(t *testing.T, uuid string) {
	t.Helper()
	if code, m := r.post(t, uuid, 47); code != http.StatusOK {
		t.Fatalf("seed %s: status %d, body %v", uuid, code, m)
	}
	if code, m := r.post(t, uuid, 13); code != http.StatusConflict {
		t.Fatalf("regress %s: status %d, body %v", uuid, code, m)
	}
}

func (r *observationClearRig) pair(t *testing.T, endpointID string, seed byte) {
	t.Helper()
	secret := pairingSecret(seed)
	resp, body := doRawRequest(t, r.api.Handler, startPairingRequest(t, endpointID, `{"code":"`+pairingCodeFor(t, secret)+`"}`, r.admin))
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("start pairing %s: status %d, body %s", endpointID, resp.StatusCode, body)
	}
	resp, body = doRawRequest(t, r.api.Handler, claimRequest(t, `{"secret":"`+secret+`"}`))
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("claim %s: status %d, body %s", endpointID, resp.StatusCode, body)
	}
}

func (r *observationClearRig) hasRow(t *testing.T, uuid string) bool {
	t.Helper()
	_, err := r.setup.st.GetFPPPlaylistEntryObservation(context.Background(), uuid)
	switch {
	case err == nil:
		return true
	case errors.Is(err, store.ErrFPPPlaylistEntryObservationNotFound):
		return false
	default:
		t.Fatalf("get observation %s: %v", uuid, err)
		return false
	}
}

func (r *observationClearRig) newestPairAudit(t *testing.T) identity.AuditEntry {
	t.Helper()
	entries, err := r.setup.svc.ListAuditNewestFirst(t.Context(), 0, 20)
	if err != nil {
		t.Fatalf("read audit: %v", err)
	}
	for _, e := range entries {
		if e.Action == auditActionFPPPair && e.Kind == identity.AuditOutcome {
			return e
		}
	}
	t.Fatal("no fpp.pair outcome audit entry")
	return identity.AuditEntry{}
}

func TestRePairClearsTheStaleObservationSoALowerSequenceIsAccepted(t *testing.T) {
	r := newObservationClearRig(t, "fpp-a")
	r.mapUUID(t, "fpp-a", "uuid-a")
	r.refuse(t, "uuid-a")

	r.pair(t, "fpp-a", 10)

	if r.hasRow(t, "uuid-a") {
		t.Fatal("stored observation for uuid-a survived a confirmed re-pair")
	}
	if code, m := r.post(t, "uuid-a", 13); code != http.StatusOK {
		t.Fatalf("post sequence 13 after re-pair: status %d, body %v", code, m)
	}
	if reason := r.newestPairAudit(t).OutcomeReason; !strings.Contains(reason, "cleared the stored playlist entry observation for instance uuid-a") {
		t.Errorf("pair audit OutcomeReason = %q, want it to record the cleared observation", reason)
	}
}

func TestRePairWithNoMappingOrNoRowSucceedsUnchanged(t *testing.T) {
	r := newObservationClearRig(t, "fpp-a", "fpp-b")
	r.mapUUID(t, "fpp-b", "uuid-b")

	r.pair(t, "fpp-a", 20) // no mapped uuid
	r.pair(t, "fpp-b", 30) // mapped, but no stored row

	if reason := r.newestPairAudit(t).OutcomeReason; strings.Contains(reason, "cleared") {
		t.Errorf("pair audit OutcomeReason = %q, want no clearing recorded", reason)
	}
}

func TestRePairNeverClearsAnotherEndpointsObservation(t *testing.T) {
	r := newObservationClearRig(t, "fpp-a", "fpp-b")
	r.mapUUID(t, "fpp-a", "uuid-a")
	r.mapUUID(t, "fpp-b", "uuid-b")
	r.refuse(t, "uuid-a")
	r.refuse(t, "uuid-b")

	r.pair(t, "fpp-a", 40)

	if r.hasRow(t, "uuid-a") {
		t.Error("uuid-a's observation survived its own endpoint's re-pair")
	}
	if !r.hasRow(t, "uuid-b") {
		t.Error("re-pairing fpp-a cleared fpp-b's observation")
	}
}

func TestRePairLeavesAUUIDSharedWithAnotherEndpointAlone(t *testing.T) {
	r := newObservationClearRig(t, "fpp-a", "fpp-b")
	r.mapUUID(t, "fpp-a", "uuid-shared")
	r.mapUUID(t, "fpp-b", "uuid-shared")
	r.refuse(t, "uuid-shared")

	r.pair(t, "fpp-a", 50)

	if !r.hasRow(t, "uuid-shared") {
		t.Error("re-pairing fpp-a cleared an observation fpp-b also reports")
	}
}

func clearMismatched(t *testing.T, r *observationClearRig, token string) (*http.Response, []byte) {
	t.Helper()
	req := httptest.NewRequest(http.MethodPost, "/api/v1/integrations/fpp/playlist-entry-observations/clear-mismatched", nil)
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	return doRawRequest(t, r.api.Handler, req)
}

func TestClearMismatchedRemovesOnlyRefusedRowsAndReportsThem(t *testing.T) {
	r := newObservationClearRig(t)
	r.refuse(t, "uuid-b")
	r.refuse(t, "uuid-a")
	if code, m := r.post(t, "uuid-healthy", 5); code != http.StatusOK {
		t.Fatalf("seed healthy: status %d, body %v", code, m)
	}

	resp, body := clearMismatched(t, r, r.admin)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200; body %s", resp.StatusCode, body)
	}
	var got struct {
		Cleared []string `json:"clearedInstanceUuids"`
	}
	if err := json.Unmarshal(body, &got); err != nil {
		t.Fatalf("decode: %v; body %s", err, body)
	}
	if strings.Join(got.Cleared, ",") != "uuid-a,uuid-b" {
		t.Errorf("clearedInstanceUuids = %v, want [uuid-a uuid-b]", got.Cleared)
	}
	if r.hasRow(t, "uuid-a") || r.hasRow(t, "uuid-b") {
		t.Error("a refused row survived the bulk clear")
	}
	if !r.hasRow(t, "uuid-healthy") {
		t.Error("the bulk clear removed a healthy row")
	}

	entries, err := r.setup.svc.ListAuditNewestFirst(t.Context(), 0, 5)
	if err != nil || len(entries) == 0 || entries[0].Action != auditActionFPPClearMismatchedPlaylistEntryObservations {
		t.Fatalf("newest audit entry = %+v (err %v), want the bulk clear", entries, err)
	}
}

func TestClearMismatchedWithNothingRefusedReturnsAnEmptyList(t *testing.T) {
	r := newObservationClearRig(t)
	resp, body := clearMismatched(t, r, r.admin)
	if resp.StatusCode != http.StatusOK || !strings.Contains(string(body), `"clearedInstanceUuids":[]`) {
		t.Fatalf("status %d body %s, want 200 with an empty list", resp.StatusCode, body)
	}
}

func TestClearMismatchedRequiresFPPCommand(t *testing.T) {
	r := newObservationClearRig(t)
	r.refuse(t, "uuid-a")
	if resp, _ := clearMismatched(t, r, ""); resp.StatusCode != http.StatusUnauthorized {
		t.Errorf("unauthenticated status = %d, want 401", resp.StatusCode)
	}
	if resp, _ := clearMismatched(t, r, r.scheduler); resp.StatusCode != http.StatusForbidden {
		t.Errorf("scheduler status = %d, want 403", resp.StatusCode)
	}
	if !r.hasRow(t, "uuid-a") {
		t.Error("a refused bulk clear removed a row")
	}
}

func TestObservationConflictsUsePlainWording(t *testing.T) {
	r := newObservationClearRig(t)
	if code, _ := r.post(t, "uuid-a", 47); code != http.StatusOK {
		t.Fatal("seed failed")
	}
	_, regressed := r.post(t, "uuid-a", 13)
	resp, reused := mustPostObservation(t, r.api, fppObservationBody(t, "uuid-a", 47, "showmesh-test", "other", 0), r.scheduler)
	if resp.StatusCode != http.StatusConflict {
		t.Fatalf("reused status = %d, want 409", resp.StatusCode)
	}
	for name, m := range map[string]map[string]any{"regressed": regressed, "reused": reused} {
		if m["title"] != "FPP observations do not match" {
			t.Errorf("%s title = %v", name, m["title"])
		}
		if m["detail"] != "This FPP's playlist observations do not match what ShowMesh has stored. Clear them on the ShowMesh Monitor screen." {
			t.Errorf("%s detail = %v", name, m["detail"])
		}
		if m["type"] != ProblemTypeConflict {
			t.Errorf("%s type = %v, want %q", name, m["type"], ProblemTypeConflict)
		}
	}
}
