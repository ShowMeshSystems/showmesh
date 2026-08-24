package api

import (
	"context"
	"encoding/json"
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

// TestAcknowledgeFPPInstanceUUIDChangeAuditRecordsPreviousUUID proves the
// audit entry records the uuid that was ACTUALLY acknowledged (the value
// previous_uuid held before the write cleared it), not the post-write
// empty string. Regression test for finding 2.
func TestAcknowledgeFPPInstanceUUIDChangeAuditRecordsPreviousUUID(t *testing.T) {
	deps, st, _ := newTestDiscoveryDeps(t, fixedClock(testNow), nil, &fakeFPPLister{
		views: []FPPInstanceView{{InstanceID: "front-yard", Endpoint: "http://10.0.1.20"}},
	})
	admin := mustCreatePrincipal(t, deps.Identity, "admin-1", identity.RoleAdmin)
	adminToken := mustIssueToken(t, deps.Identity, admin.ID)
	h := New(deps, Options{Clock: fixedClock(testNow), Logger: testLogger()})
	ctx := context.Background()

	if _, _, err := st.RecordFPPInstanceUUIDObservation(ctx, "front-yard", "uuid-a", testNow.Add(-time.Hour)); err != nil {
		t.Fatalf("record first uuid: %v", err)
	}
	if _, _, err := st.RecordFPPInstanceUUIDObservation(ctx, "front-yard", "uuid-b", testNow); err != nil {
		t.Fatalf("record second uuid: %v", err)
	}

	req := newJSONRequest(t, http.MethodPost, "/api/v1/fpp/front-yard/instance-uuid/acknowledge", "", nil)
	req.Header.Set("Authorization", "Bearer "+adminToken)
	resp, body := doRawRequest(t, h.Handler, req)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200; body: %s", resp.StatusCode, body)
	}

	entries, err := deps.Identity.ListAudit(ctx, 0, 100)
	if err != nil {
		t.Fatalf("ListAudit: %v", err)
	}
	var found bool
	for _, e := range entries {
		if e.Action == "fpp.instance_uuid.acknowledge" && e.Target == "front-yard" {
			found = true
			if got, _ := e.Params["previousUuid"].(string); got != "uuid-a" {
				t.Errorf("audit entry Params[previousUuid] = %q, want %q (the uuid actually acknowledged)", got, "uuid-a")
			}
			if got, _ := e.Params["uuid"].(string); got != "uuid-b" {
				t.Errorf("audit entry Params[uuid] = %q, want %q", got, "uuid-b")
			}
		}
	}
	if !found {
		t.Fatalf("no fpp.instance_uuid.acknowledge audit entry for front-yard found among %d entries", len(entries))
	}
}

// TestAcknowledgeFPPInstanceUUIDChangeSucceedsWhenEndpointNoLongerConfigured
// proves that acknowledging a pending change for an endpoint that has since
// been removed from fpp.endpoints still reports success: the write and its
// audit entry committed, so the response must not contradict them, and the
// body's instance key is present and JSON null (not merely absent, and not
// a fabricated zero-valued object) per the OpenAPI contract's "null exactly
// when" wording. Regression test for finding 3.
func TestAcknowledgeFPPInstanceUUIDChangeSucceedsWhenEndpointNoLongerConfigured(t *testing.T) {
	deps, st, _ := newTestDiscoveryDeps(t, fixedClock(testNow), nil, &fakeFPPLister{})
	admin := mustCreatePrincipal(t, deps.Identity, "admin-1", identity.RoleAdmin)
	adminToken := mustIssueToken(t, deps.Identity, admin.ID)
	h := New(deps, Options{Clock: fixedClock(testNow), Logger: testLogger()})
	ctx := context.Background()

	// front-yard has reported uuids before (an observation row exists),
	// but is deliberately absent from the fakeFPPLister's views, standing
	// in for an endpoint an operator has since removed from fpp.endpoints.
	if _, _, err := st.RecordFPPInstanceUUIDObservation(ctx, "front-yard", "uuid-a", testNow.Add(-time.Hour)); err != nil {
		t.Fatalf("record first uuid: %v", err)
	}
	if _, _, err := st.RecordFPPInstanceUUIDObservation(ctx, "front-yard", "uuid-b", testNow); err != nil {
		t.Fatalf("record second uuid: %v", err)
	}

	req := newJSONRequest(t, http.MethodPost, "/api/v1/fpp/front-yard/instance-uuid/acknowledge", "", nil)
	req.Header.Set("Authorization", "Bearer "+adminToken)
	resp, body := doRawRequest(t, h.Handler, req)
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		t.Fatalf("status = %d, want 2xx (the write and its audit entry committed); body: %s", resp.StatusCode, body)
	}

	// Decode into raw JSON, not a typed struct, so a fabricated
	// zero-valued instance object would fail this assertion instead of
	// decoding away to its own zero value.
	var raw map[string]json.RawMessage
	if err := json.Unmarshal(body, &raw); err != nil {
		t.Fatalf("unmarshal response body: %v; body: %s", err, body)
	}
	instance, ok := raw["instance"]
	if !ok {
		t.Fatalf("response body has no instance key; body: %s", body)
	}
	if string(instance) != "null" {
		t.Errorf("instance = %s, want JSON null", instance)
	}

	after, err := st.GetFPPInstanceUUID(ctx, "front-yard")
	if err != nil {
		t.Fatalf("GetFPPInstanceUUID after acknowledge: %v", err)
	}
	if after.HasUnacknowledgedChange() {
		t.Errorf("HasUnacknowledgedChange() = true after acknowledge, want false")
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
