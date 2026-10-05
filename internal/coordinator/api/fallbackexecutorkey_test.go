package api

import (
	"crypto/ed25519"
	"encoding/base64"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/showmeshsystems/showmesh/internal/coordinator/identity"
	"github.com/showmeshsystems/showmesh/internal/coordinator/store"
)

const executorKeyTestInstanceUUID = "22222222-2222-4222-8222-222222222222"

type countingFallbackNudger struct{ nudges int }

func (n *countingFallbackNudger) Nudge() { n.nudges++ }

// executorKeyFor builds a syntactically valid public key from a seed byte.
func executorKeyFor(seed byte) string {
	raw := make([]byte, ed25519.SeedSize)
	for i := range raw {
		raw[i] = seed
	}
	return base64.StdEncoding.EncodeToString(ed25519.NewKeyFromSeed(raw).Public().(ed25519.PublicKey))
}

// executorKeyAPI wires a real store and identity service, a plugin
// principal named as pairing names it, and that player's recorded UUID.
func executorKeyAPI(t *testing.T) (*API, *fppCommandTestSetup, *countingFallbackNudger, string) {
	t.Helper()
	setup := newFPPCommandTestSetup(t, fixedClock(testNow))
	nudger := &countingFallbackNudger{}
	deps := setup.deps()
	deps.FallbackPrograms = setup.st
	deps.FallbackProgramNudger = nudger
	api := New(deps, Options{Clock: fixedClock(testNow), Logger: testLogger()})
	if _, _, err := setup.st.RecordFPPInstanceUUIDObservation(t.Context(), "bench-fpp", executorKeyTestInstanceUUID, testNow); err != nil {
		t.Fatalf("record instance uuid: %v", err)
	}
	plugin, err := setup.svc.CreatePrincipal(t.Context(), fppPairingPrincipalPrefix+"bench-fpp", identity.KindMachine, identity.RoleScheduler, "")
	if err != nil {
		t.Fatalf("create plugin principal: %v", err)
	}
	return api, setup, nudger, mustIssueToken(t, setup.svc, plugin.ID)
}

func putExecutorKey(t *testing.T, api *API, instanceUUID, token, body string) (*http.Response, []byte) {
	t.Helper()
	req := httptest.NewRequest(http.MethodPut, "/api/v1/fallback-programs/"+instanceUUID+"/executor-key", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	return doRawRequest(t, api.Handler, req)
}

type executorKeyResponseForTest struct {
	FPPInstanceUUID string `json:"fppInstanceUuid"`
	PublicKey       string `json:"publicKey"`
	RegisteredAt    string `json:"registeredAt"`
	Changed         bool   `json:"changed"`
}

func decodeExecutorKeyResponse(t *testing.T, body []byte) executorKeyResponseForTest {
	t.Helper()
	var out executorKeyResponseForTest
	if err := json.Unmarshal(body, &out); err != nil {
		t.Fatalf("decode executor key response: %v; body: %s", err, body)
	}
	return out
}

func TestExecutorKeyFirstRegistrationStoresAndRepublishes(t *testing.T) {
	api, setup, nudger, token := executorKeyAPI(t)
	key := executorKeyFor(1)

	resp, body := putExecutorKey(t, api, executorKeyTestInstanceUUID, token, `{"publicKey":"`+key+`"}`)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200; body: %s", resp.StatusCode, body)
	}
	got := decodeExecutorKeyResponse(t, body)
	if !got.Changed || got.PublicKey != key || got.FPPInstanceUUID != executorKeyTestInstanceUUID {
		t.Fatalf("response = %+v, want changed with the registered key", got)
	}
	stored, err := setup.st.GetFallbackExecutorKey(t.Context(), executorKeyTestInstanceUUID)
	if err != nil || stored.PublicKeyB64 != key {
		t.Fatalf("stored key = %+v, %v; want %s", stored, err, key)
	}
	if nudger.nudges != 1 {
		t.Fatalf("a first key asked for %d rebuilds, want 1", nudger.nudges)
	}
	if n := countAuditActions(t, setup, auditActionFallbackExecutorKeyRegister); n != 2 {
		t.Fatalf("a first key wrote %d audit entries, want the dispatch and its outcome", n)
	}
}

func TestExecutorKeyReRegisteringTheSameKeyChangesNothing(t *testing.T) {
	api, setup, nudger, token := executorKeyAPI(t)
	body := `{"publicKey":"` + executorKeyFor(1) + `"}`
	if resp, raw := putExecutorKey(t, api, executorKeyTestInstanceUUID, token, body); resp.StatusCode != http.StatusOK {
		t.Fatalf("first registration status = %d; body: %s", resp.StatusCode, raw)
	}
	audited := countAuditActions(t, setup, auditActionFallbackExecutorKeyRegister)

	resp, raw := putExecutorKey(t, api, executorKeyTestInstanceUUID, token, body)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("second registration status = %d, want 200; body: %s", resp.StatusCode, raw)
	}
	if got := decodeExecutorKeyResponse(t, raw); got.Changed {
		t.Fatalf("re-registering the stored key reported changed: %+v", got)
	}
	if nudger.nudges != 1 {
		t.Fatalf("re-registering the stored key asked for another rebuild: %d nudges", nudger.nudges)
	}
	if n := countAuditActions(t, setup, auditActionFallbackExecutorKeyRegister); n != audited {
		t.Fatalf("re-registering the stored key wrote audit entries: %d, was %d", n, audited)
	}
}

func TestExecutorKeyRotationReplacesTheKeyAndRepublishes(t *testing.T) {
	api, setup, nudger, token := executorKeyAPI(t)
	if resp, raw := putExecutorKey(t, api, executorKeyTestInstanceUUID, token, `{"publicKey":"`+executorKeyFor(1)+`"}`); resp.StatusCode != http.StatusOK {
		t.Fatalf("first registration status = %d; body: %s", resp.StatusCode, raw)
	}
	rotated := executorKeyFor(2)

	resp, raw := putExecutorKey(t, api, executorKeyTestInstanceUUID, token, `{"publicKey":"`+rotated+`"}`)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("rotation status = %d, want 200; body: %s", resp.StatusCode, raw)
	}
	if got := decodeExecutorKeyResponse(t, raw); !got.Changed || got.PublicKey != rotated {
		t.Fatalf("rotation response = %+v, want changed with the new key", got)
	}
	stored, err := setup.st.GetFallbackExecutorKey(t.Context(), executorKeyTestInstanceUUID)
	if err != nil || stored.PublicKeyB64 != rotated {
		t.Fatalf("stored key after rotation = %+v, %v; want %s", stored, err, rotated)
	}
	if nudger.nudges != 2 {
		t.Fatalf("a rotated key asked for %d rebuilds in total, want 2", nudger.nudges)
	}
}

func TestExecutorKeyRefusesEveryCallerButThePairedPlugin(t *testing.T) {
	api, setup, nudger, _ := executorKeyAPI(t)
	body := `{"publicKey":"` + executorKeyFor(1) + `"}`

	admin := mustCreatePrincipal(t, setup.svc, "admin-1", identity.RoleAdmin)
	otherPlugin, err := setup.svc.CreatePrincipal(t.Context(), fppPairingPrincipalPrefix+"other-fpp", identity.KindMachine, identity.RoleScheduler, "")
	if err != nil {
		t.Fatalf("create second plugin principal: %v", err)
	}
	unseenPlugin, err := setup.svc.CreatePrincipal(t.Context(), fppPairingPrincipalPrefix+"unseen-fpp", identity.KindMachine, identity.RoleScheduler, "")
	if err != nil {
		t.Fatalf("create third plugin principal: %v", err)
	}
	if _, _, err := setup.st.RecordFPPInstanceUUIDObservation(t.Context(), "other-fpp", "33333333-3333-4333-8333-333333333333", testNow); err != nil {
		t.Fatalf("record second instance uuid: %v", err)
	}
	viewer := mustCreatePrincipal(t, setup.svc, "viewer-1", identity.RoleViewer)

	cases := []struct {
		name   string
		token  string
		status int
	}{
		{"no credential", "", http.StatusUnauthorized},
		{"an administrator", mustIssueToken(t, setup.svc, admin.ID), http.StatusForbidden},
		{"a viewer", mustIssueToken(t, setup.svc, viewer.ID), http.StatusForbidden},
		{"a plugin paired as another player", mustIssueToken(t, setup.svc, otherPlugin.ID), http.StatusForbidden},
		{"a plugin whose player has reported no identity", mustIssueToken(t, setup.svc, unseenPlugin.ID), http.StatusForbidden},
	}
	for _, tc := range cases {
		resp, raw := putExecutorKey(t, api, executorKeyTestInstanceUUID, tc.token, body)
		if resp.StatusCode != tc.status {
			t.Errorf("%s: status = %d, want %d; body: %s", tc.name, resp.StatusCode, tc.status, raw)
		}
	}
	if _, err := setup.st.GetFallbackExecutorKey(t.Context(), executorKeyTestInstanceUUID); err != store.ErrFallbackExecutorKeyNotFound {
		t.Fatalf("a refused caller stored a key: %v", err)
	}
	if nudger.nudges != 0 {
		t.Fatalf("a refused caller asked for %d rebuilds", nudger.nudges)
	}
}

func TestExecutorKeyRefusesAMalformedKey(t *testing.T) {
	api, _, _, token := executorKeyAPI(t)
	for name, body := range map[string]string{
		"not base64":      `{"publicKey":"not a key"}`,
		"wrong length":    `{"publicKey":"` + base64.StdEncoding.EncodeToString([]byte("short")) + `"}`,
		"unknown member":  `{"publicKey":"` + executorKeyFor(1) + `","privateKey":"x"}`,
		"missing":         `{}`,
		"not json at all": `publicKey`,
	} {
		resp, raw := putExecutorKey(t, api, executorKeyTestInstanceUUID, token, body)
		if resp.StatusCode != http.StatusBadRequest {
			t.Errorf("%s: status = %d, want 400; body: %s", name, resp.StatusCode, raw)
		}
	}
}

func countAuditActions(t *testing.T, setup *fppCommandTestSetup, action string) int {
	t.Helper()
	entries, err := setup.svc.ListAudit(t.Context(), 0, 500)
	if err != nil {
		t.Fatalf("list audit: %v", err)
	}
	n := 0
	for _, e := range entries {
		if e.Action == action {
			n++
		}
	}
	return n
}
