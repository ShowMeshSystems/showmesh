package api

import (
	"context"
	"net/http"
	"testing"
	"time"

	"github.com/showmeshsystems/showmesh/internal/coordinator/identity"
)

// This file tests POST /api/v1/fpp/{instanceId}/instance-uuid/acknowledge
// end to end through the real HTTP handler and a real store, the
// same pattern discovery_test.go's TestPromoteNode* tests use for the
// identical class of write (a coordinator-local state change audited via
// [identity.Service.AuditedWrite]).

func TestAcknowledgeFPPInstanceUUIDChangeClearsPendingConflict(t *testing.T) {
	deps, st, _ := newTestDiscoveryDeps(t, fixedClock(testNow), nil, &fakeFPPLister{
		views: []FPPInstanceView{{InstanceID: "front-yard", Endpoint: "http://10.0.1.20"}},
	})
	admin := mustCreatePrincipal(t, deps.Identity, "admin-1", identity.RoleAdmin)
	adminToken := mustIssueToken(t, deps.Identity, admin.ID)
	h := New(deps, Options{Clock: fixedClock(testNow), Logger: testLogger()})
	ctx := context.Background()

	// Seed a genuine unacknowledged change: front-yard reported uuid-a,
	// then later uuid-b.
	if _, _, err := st.RecordFPPInstanceUUIDObservation(ctx, "front-yard", "uuid-a", testNow.Add(-time.Hour)); err != nil {
		t.Fatalf("record first uuid: %v", err)
	}
	if _, _, err := st.RecordFPPInstanceUUIDObservation(ctx, "front-yard", "uuid-b", testNow); err != nil {
		t.Fatalf("record second uuid: %v", err)
	}
	pending, err := st.GetFPPInstanceUUID(ctx, "front-yard")
	if err != nil {
		t.Fatalf("precondition GetFPPInstanceUUID: %v", err)
	}
	if !pending.HasUnacknowledgedChange() {
		t.Fatalf("precondition: expected an unacknowledged change before the request")
	}

	req := newJSONRequest(t, http.MethodPost, "/api/v1/fpp/front-yard/instance-uuid/acknowledge", "", nil)
	req.Header.Set("Authorization", "Bearer "+adminToken)
	resp, body := doRawRequest(t, h.Handler, req)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200; body: %s", resp.StatusCode, body)
	}

	after, err := st.GetFPPInstanceUUID(ctx, "front-yard")
	if err != nil {
		t.Fatalf("GetFPPInstanceUUID after acknowledge: %v", err)
	}
	if after.HasUnacknowledgedChange() {
		t.Errorf("HasUnacknowledgedChange() = true after acknowledge, want false")
	}
	if after.UUID != "uuid-b" {
		t.Errorf("UUID after acknowledge = %q, want uuid-b (acknowledging must not change the recorded uuid)", after.UUID)
	}
	if after.ChangeAcknowledgedByPrincipalID != admin.ID {
		t.Errorf("ChangeAcknowledgedByPrincipalID = %q, want %q", after.ChangeAcknowledgedByPrincipalID, admin.ID)
	}

	// The audit entry itself, mirroring TestPromoteNodeWithoutFailingAuditSucceeds's
	// own assertion that ADR-024's attribution actually landed, not merely
	// that the write succeeded.
	entries, err := deps.Identity.ListAudit(ctx, 0, 100)
	if err != nil {
		t.Fatalf("ListAudit: %v", err)
	}
	var found bool
	for _, e := range entries {
		if e.Action == "fpp.instance_uuid.acknowledge" && e.Target == "front-yard" {
			found = true
			if e.PrincipalID != admin.ID {
				t.Errorf("audit entry PrincipalID = %q, want %q", e.PrincipalID, admin.ID)
			}
		}
	}
	if !found {
		t.Fatalf("no fpp.instance_uuid.acknowledge audit entry for front-yard found among %d entries", len(entries))
	}
}

// TestAcknowledgeFPPInstanceUUIDChangeRefusesWithNothingPending proves
// acknowledging an endpoint with no pending change is refused with 409,
// never silently accepted as a no-op, the same "refuse a stale or
// mistargeted request" posture store.AcknowledgeFPPInstanceUUIDChange
// itself enforces.
func TestAcknowledgeFPPInstanceUUIDChangeRefusesWithNothingPending(t *testing.T) {
	deps, st, _ := newTestDiscoveryDeps(t, fixedClock(testNow), nil, &fakeFPPLister{
		views: []FPPInstanceView{{InstanceID: "front-yard", Endpoint: "http://10.0.1.20"}},
	})
	admin := mustCreatePrincipal(t, deps.Identity, "admin-1", identity.RoleAdmin)
	adminToken := mustIssueToken(t, deps.Identity, admin.ID)
	h := New(deps, Options{Clock: fixedClock(testNow), Logger: testLogger()})
	ctx := context.Background()

	if _, _, err := st.RecordFPPInstanceUUIDObservation(ctx, "front-yard", "uuid-a", testNow); err != nil {
		t.Fatalf("record uuid: %v", err)
	}

	req := newJSONRequest(t, http.MethodPost, "/api/v1/fpp/front-yard/instance-uuid/acknowledge", "", nil)
	req.Header.Set("Authorization", "Bearer "+adminToken)
	resp, body := doRawRequest(t, h.Handler, req)
	if resp.StatusCode != http.StatusConflict {
		t.Fatalf("status = %d, want 409; body: %s", resp.StatusCode, body)
	}
}

// TestAcknowledgeFPPInstanceUUIDChangeUnknownInstanceIs404 proves
// acknowledging an instance that has never reported a uuid at all is a
// 404, distinct from the 409 "nothing pending" case.
func TestAcknowledgeFPPInstanceUUIDChangeUnknownInstanceIs404(t *testing.T) {
	deps, _, _ := newTestDiscoveryDeps(t, fixedClock(testNow), nil, &fakeFPPLister{})
	admin := mustCreatePrincipal(t, deps.Identity, "admin-1", identity.RoleAdmin)
	adminToken := mustIssueToken(t, deps.Identity, admin.ID)
	h := New(deps, Options{Clock: fixedClock(testNow), Logger: testLogger()})

	req := newJSONRequest(t, http.MethodPost, "/api/v1/fpp/nowhere/instance-uuid/acknowledge", "", nil)
	req.Header.Set("Authorization", "Bearer "+adminToken)
	resp, body := doRawRequest(t, h.Handler, req)
	if resp.StatusCode != http.StatusNotFound {
		t.Fatalf("status = %d, want 404; body: %s", resp.StatusCode, body)
	}
}

// TestAcknowledgeFPPInstanceUUIDChangeRequiresConfigWrite proves this
// route is behind config:write, not fpp:command, a scheduler-role
// credential (which holds fpp:observe/fpp:command for plugin traffic)
// must not be able to clear this operator-facing conflict marker.
func TestAcknowledgeFPPInstanceUUIDChangeRequiresConfigWrite(t *testing.T) {
	deps, st, _ := newTestDiscoveryDeps(t, fixedClock(testNow), nil, &fakeFPPLister{
		views: []FPPInstanceView{{InstanceID: "front-yard", Endpoint: "http://10.0.1.20"}},
	})
	scheduler := mustCreatePrincipal(t, deps.Identity, "scheduler-1", identity.RoleScheduler)
	schedulerToken := mustIssueToken(t, deps.Identity, scheduler.ID)
	h := New(deps, Options{Clock: fixedClock(testNow), Logger: testLogger()})
	ctx := context.Background()

	if _, _, err := st.RecordFPPInstanceUUIDObservation(ctx, "front-yard", "uuid-a", testNow.Add(-time.Hour)); err != nil {
		t.Fatalf("record first uuid: %v", err)
	}
	if _, _, err := st.RecordFPPInstanceUUIDObservation(ctx, "front-yard", "uuid-b", testNow); err != nil {
		t.Fatalf("record second uuid: %v", err)
	}

	req := newJSONRequest(t, http.MethodPost, "/api/v1/fpp/front-yard/instance-uuid/acknowledge", "", nil)
	req.Header.Set("Authorization", "Bearer "+schedulerToken)
	resp, body := doRawRequest(t, h.Handler, req)
	if resp.StatusCode != http.StatusForbidden {
		t.Fatalf("status = %d, want 403 (scheduler lacks config:write); body: %s", resp.StatusCode, body)
	}
}
