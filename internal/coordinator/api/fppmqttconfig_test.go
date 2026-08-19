package api

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"sync"
	"testing"

	"github.com/showmeshsystems/showmesh/internal/coordinator/identity"
	"github.com/showmeshsystems/showmesh/internal/coordinator/store"
)

// This file is Track G seam G-3's own test suite (ADR-039), mirroring
// resolumeinstancesconfig_test.go's shape for the fpp.mqtt kind, plus the
// credential-specific cases ADR-039 decision 7 names explicitly: GET never
// returns the password, and a GET-then-PUT round trip must not erase it.

// fakeFPPMQTTSecretStore is an in-memory [FPPMQTTSecretStore]: the
// filesystem mechanics are already covered by
// internal/coordinator/config's fppmqttsecret_test.go, so this test suite
// only needs a working Has/Set/Clear, not a real file.
type fakeFPPMQTTSecretStore struct {
	mu       sync.Mutex
	password string
	present  bool
}

func (f *fakeFPPMQTTSecretStore) HasFPPMQTTPassword(context.Context) (bool, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.present, nil
}

func (f *fakeFPPMQTTSecretStore) SetFPPMQTTPassword(_ context.Context, password string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.password = password
	f.present = true
	return nil
}

func (f *fakeFPPMQTTSecretStore) ClearFPPMQTTPassword(context.Context) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.password = ""
	f.present = false
	return nil
}

// fppMQTTConfigTestDeps mirrors configTestDeps, additionally wiring
// FPPMQTTSecret (an in-memory fake) and an FPP lister that already knows
// about "player-01" — every test body in this file names that id as an
// fpp.mqtt host, and [config.ValidateFPPMQTTConfigKind] refuses a host id
// that is not a configured fpp.endpoints id.
func fppMQTTConfigTestDeps(svc identity.Service, st *store.Store) (Dependencies, *fakeFPPMQTTSecretStore) {
	deps := configTestDeps(svc, st)
	deps.FPP = &fakeFPPLister{views: []FPPInstanceView{{InstanceID: "player-01", Endpoint: "http://10.0.1.20"}}}
	secret := &fakeFPPMQTTSecretStore{}
	deps.FPPMQTTSecret = secret
	return deps, secret
}

func TestPutFPPMQTTConfigAuthAndScope(t *testing.T) {
	svc, st, _ := newTestIdentityServiceWithStore(t, fixedClock(testNow))
	viewer := mustCreatePrincipal(t, svc, "viewer-1", identity.RoleViewer)
	admin := mustCreatePrincipal(t, svc, "admin-1", identity.RoleAdmin)
	viewerToken := mustIssueToken(t, svc, viewer.ID)
	adminToken := mustIssueToken(t, svc, admin.ID)

	deps, _ := fppMQTTConfigTestDeps(svc, st)
	api := New(deps, Options{Clock: fixedClock(testNow), Logger: testLogger()})

	body := `{"brokerURL":"tcp://10.0.1.5:1883","hosts":{"player-01":"FPP-Player"}}`

	t.Run("unauthenticated", func(t *testing.T) {
		req := newJSONRequest(t, http.MethodPut, "/api/v1/config/fpp.mqtt", body, nil)
		resp, respBody := doRawRequest(t, api.Handler, req)
		if resp.StatusCode != http.StatusUnauthorized {
			t.Fatalf("status = %d, want 401; body: %s", resp.StatusCode, respBody)
		}
	})

	t.Run("viewer forbidden naming config:write", func(t *testing.T) {
		req := newJSONRequest(t, http.MethodPut, "/api/v1/config/fpp.mqtt", body,
			map[string]string{"Authorization": "Bearer " + viewerToken})
		resp, respBody := doRawRequest(t, api.Handler, req)
		if resp.StatusCode != http.StatusForbidden {
			t.Fatalf("status = %d, want 403; body: %s", resp.StatusCode, respBody)
		}
		if !strings.Contains(string(respBody), "config:write") {
			t.Errorf("body = %s, want it to name the missing scope config:write", respBody)
		}
	})

	t.Run("admin accepted", func(t *testing.T) {
		req := newJSONRequest(t, http.MethodPut, "/api/v1/config/fpp.mqtt", body,
			map[string]string{"Authorization": "Bearer " + adminToken})
		resp, respBody := doRawRequest(t, api.Handler, req)
		if resp.StatusCode != http.StatusOK {
			t.Fatalf("status = %d, want 200; body: %s", resp.StatusCode, respBody)
		}
		m := decodeMap(t, respBody)
		if m["revision"] != float64(1) {
			t.Errorf("revision = %v, want 1", m["revision"])
		}
	})
}

func TestGetFPPMQTTConfigRequiresConfigWriteScope(t *testing.T) {
	svc, st, _ := newTestIdentityServiceWithStore(t, fixedClock(testNow))
	viewer := mustCreatePrincipal(t, svc, "viewer-1", identity.RoleViewer)
	admin := mustCreatePrincipal(t, svc, "admin-1", identity.RoleAdmin)
	viewerToken := mustIssueToken(t, svc, viewer.ID)
	adminToken := mustIssueToken(t, svc, admin.ID)

	deps, _ := fppMQTTConfigTestDeps(svc, st)
	api := New(deps, Options{Clock: fixedClock(testNow), Logger: testLogger()})

	for _, path := range []string{"/api/v1/config/fpp.mqtt", "/api/v1/config/fpp.mqtt/revisions"} {
		t.Run(path+"/unauthenticated", func(t *testing.T) {
			resp, body := doRequest(t, api.Handler, "GET", path, nil)
			if resp.StatusCode != http.StatusUnauthorized {
				t.Fatalf("status = %d, want 401; body: %s", resp.StatusCode, body)
			}
		})
		t.Run(path+"/viewer forbidden", func(t *testing.T) {
			resp, body := doRequest(t, api.Handler, "GET", path, map[string]string{"Authorization": "Bearer " + viewerToken})
			if resp.StatusCode != http.StatusForbidden {
				t.Fatalf("status = %d, want 403; body: %s", resp.StatusCode, body)
			}
		})
		t.Run(path+"/admin allowed", func(t *testing.T) {
			resp, body := doRequest(t, api.Handler, "GET", path, map[string]string{"Authorization": "Bearer " + adminToken})
			if path == "/api/v1/config/fpp.mqtt" {
				if resp.StatusCode != http.StatusNotFound {
					t.Fatalf("status = %d, want 404 (scope gate passed, no config exists yet); body: %s", resp.StatusCode, body)
				}
				return
			}
			if resp.StatusCode != http.StatusOK {
				t.Fatalf("status = %d, want 200; body: %s", resp.StatusCode, body)
			}
		})
	}
}

func TestPutFPPMQTTConfigValidatesBeforeActivation(t *testing.T) {
	svc, st, _ := newTestIdentityServiceWithStore(t, fixedClock(testNow))
	admin := mustCreatePrincipal(t, svc, "admin-1", identity.RoleAdmin)
	adminToken := mustIssueToken(t, svc, admin.ID)
	deps, _ := fppMQTTConfigTestDeps(svc, st)
	api := New(deps, Options{Clock: fixedClock(testNow), Logger: testLogger()})

	invalidBody := `{"brokerURL":"tcp://10.0.1.5:1883"}` // broker set, no hosts
	req := newJSONRequest(t, http.MethodPut, "/api/v1/config/fpp.mqtt", invalidBody,
		map[string]string{"Authorization": "Bearer " + adminToken})
	resp, body := doRawRequest(t, api.Handler, req)
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400; body: %s", resp.StatusCode, body)
	}

	revs, err := st.ListConfigRevisions(context.Background(), "fpp.mqtt", "default")
	if err != nil {
		t.Fatalf("ListConfigRevisions: %v", err)
	}
	if len(revs) != 0 {
		t.Fatalf("revisions after a rejected write = %v, want none", revs)
	}
}

func TestPutFPPMQTTConfigRejectsUnknownField(t *testing.T) {
	svc, st, _ := newTestIdentityServiceWithStore(t, fixedClock(testNow))
	admin := mustCreatePrincipal(t, svc, "admin-1", identity.RoleAdmin)
	adminToken := mustIssueToken(t, svc, admin.ID)
	deps, _ := fppMQTTConfigTestDeps(svc, st)
	api := New(deps, Options{Clock: fixedClock(testNow), Logger: testLogger()})

	req := newJSONRequest(t, http.MethodPut, "/api/v1/config/fpp.mqtt", `{"brokrURL":"tcp://x:1883"}`,
		map[string]string{"Authorization": "Bearer " + adminToken})
	resp, body := doRawRequest(t, api.Handler, req)
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400; body: %s", resp.StatusCode, body)
	}
}

func TestPutFPPMQTTConfigNullStringFieldRejected(t *testing.T) {
	svc, st, _ := newTestIdentityServiceWithStore(t, fixedClock(testNow))
	admin := mustCreatePrincipal(t, svc, "admin-1", identity.RoleAdmin)
	adminToken := mustIssueToken(t, svc, admin.ID)
	deps, _ := fppMQTTConfigTestDeps(svc, st)
	api := New(deps, Options{Clock: fixedClock(testNow), Logger: testLogger()})

	req := newJSONRequest(t, http.MethodPut, "/api/v1/config/fpp.mqtt", `{"brokerURL":null}`,
		map[string]string{"Authorization": "Bearer " + adminToken})
	resp, body := doRawRequest(t, api.Handler, req)
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400 (null is not a valid brokerURL); body: %s", resp.StatusCode, body)
	}
}

func TestPutFPPMQTTConfigNullHostsRejected(t *testing.T) {
	svc, st, _ := newTestIdentityServiceWithStore(t, fixedClock(testNow))
	admin := mustCreatePrincipal(t, svc, "admin-1", identity.RoleAdmin)
	adminToken := mustIssueToken(t, svc, admin.ID)
	deps, _ := fppMQTTConfigTestDeps(svc, st)
	api := New(deps, Options{Clock: fixedClock(testNow), Logger: testLogger()})

	req := newJSONRequest(t, http.MethodPut, "/api/v1/config/fpp.mqtt", `{"hosts":null}`,
		map[string]string{"Authorization": "Bearer " + adminToken})
	resp, body := doRawRequest(t, api.Handler, req)
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400 (null hosts is refused, {} is the way to clear); body: %s", resp.StatusCode, body)
	}
}

// TestPutFPPMQTTConfigAbsentFieldsKeepStoredValue is ADR-039 decision 5,
// applied per-field: a PUT naming only one field must leave every other
// field exactly as it was.
func TestPutFPPMQTTConfigAbsentFieldsKeepStoredValue(t *testing.T) {
	svc, st, _ := newTestIdentityServiceWithStore(t, fixedClock(testNow))
	admin := mustCreatePrincipal(t, svc, "admin-1", identity.RoleAdmin)
	adminToken := mustIssueToken(t, svc, admin.ID)
	deps, _ := fppMQTTConfigTestDeps(svc, st)
	api := New(deps, Options{Clock: fixedClock(testNow), Logger: testLogger()})
	auth := map[string]string{"Authorization": "Bearer " + adminToken}

	first := `{"brokerURL":"tcp://10.0.1.5:1883","username":"showmesh","topicPrefix":"falcon/player","hosts":{"player-01":"FPP-Player"}}`
	req := newJSONRequest(t, http.MethodPut, "/api/v1/config/fpp.mqtt", first, auth)
	resp, body := doRawRequest(t, api.Handler, req)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("first PUT status = %d, want 200; body: %s", resp.StatusCode, body)
	}

	// Second PUT only touches topicPrefix; everything else must survive.
	second := `{"topicPrefix":"custom/prefix"}`
	req = newJSONRequest(t, http.MethodPut, "/api/v1/config/fpp.mqtt", second, auth)
	resp, body = doRawRequest(t, api.Handler, req)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("second PUT status = %d, want 200; body: %s", resp.StatusCode, body)
	}

	m := decodeMap(t, body)
	payload := m["payload"].(map[string]any)
	if payload["brokerURL"] != "tcp://10.0.1.5:1883" {
		t.Errorf("brokerURL = %v, want it unchanged by a PUT that never named it", payload["brokerURL"])
	}
	if payload["username"] != "showmesh" {
		t.Errorf("username = %v, want it unchanged", payload["username"])
	}
	if payload["topicPrefix"] != "custom/prefix" {
		t.Errorf("topicPrefix = %v, want the new value", payload["topicPrefix"])
	}
	hosts := payload["hosts"].(map[string]any)
	if hosts["player-01"] != "FPP-Player" {
		t.Errorf("hosts = %v, want it unchanged", hosts)
	}
}

// TestGetFPPMQTTConfigNeverReturnsPassword is ADR-039 decision 7, verbatim.
func TestGetFPPMQTTConfigNeverReturnsPassword(t *testing.T) {
	svc, st, _ := newTestIdentityServiceWithStore(t, fixedClock(testNow))
	admin := mustCreatePrincipal(t, svc, "admin-1", identity.RoleAdmin)
	adminToken := mustIssueToken(t, svc, admin.ID)
	deps, _ := fppMQTTConfigTestDeps(svc, st)
	api := New(deps, Options{Clock: fixedClock(testNow), Logger: testLogger()})
	auth := map[string]string{"Authorization": "Bearer " + adminToken}

	body := `{"brokerURL":"tcp://10.0.1.5:1883","hosts":{"player-01":"FPP-Player"},"password":"s3cret-value"}`
	req := newJSONRequest(t, http.MethodPut, "/api/v1/config/fpp.mqtt", body, auth)
	resp, respBody := doRawRequest(t, api.Handler, req)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("PUT status = %d, want 200; body: %s", resp.StatusCode, respBody)
	}
	if strings.Contains(string(respBody), "s3cret-value") {
		t.Fatalf("PUT response body contains the raw password: %s", respBody)
	}
	m := decodeMap(t, respBody)
	payload := m["payload"].(map[string]any)
	if _, present := payload["password"]; present {
		t.Errorf("payload has a %q key at all; ADR-039 decision 7 says the value never appears on the wire", "password")
	}
	if passwordSet, _ := payload["passwordSet"].(bool); !passwordSet {
		t.Errorf("passwordSet = %v, want true", payload["passwordSet"])
	}

	getResp, getBody := doRequest(t, api.Handler, "GET", "/api/v1/config/fpp.mqtt", auth)
	if getResp.StatusCode != http.StatusOK {
		t.Fatalf("GET status = %d, want 200; body: %s", getResp.StatusCode, getBody)
	}
	if strings.Contains(string(getBody), "s3cret-value") {
		t.Fatalf("GET response body contains the raw password: %s", getBody)
	}
	getM := decodeMap(t, getBody)
	getPayload := getM["payload"].(map[string]any)
	if _, present := getPayload["password"]; present {
		t.Errorf("GET payload has a %q key at all", "password")
	}
	if passwordSet, _ := getPayload["passwordSet"].(bool); !passwordSet {
		t.Errorf("GET passwordSet = %v, want true", getPayload["passwordSet"])
	}
}

// TestPutFPPMQTTConfigGetThenPutRoundTripPreservesPassword is the exact
// scenario ADR-039 decision 7 names: "since GET never returns the
// password, a naive round trip of GET then PUT would otherwise erase a
// credential the operator has never seen and cannot retype." This test
// proves it does not.
func TestPutFPPMQTTConfigGetThenPutRoundTripPreservesPassword(t *testing.T) {
	svc, st, _ := newTestIdentityServiceWithStore(t, fixedClock(testNow))
	admin := mustCreatePrincipal(t, svc, "admin-1", identity.RoleAdmin)
	adminToken := mustIssueToken(t, svc, admin.ID)
	deps, secret := fppMQTTConfigTestDeps(svc, st)
	api := New(deps, Options{Clock: fixedClock(testNow), Logger: testLogger()})
	auth := map[string]string{"Authorization": "Bearer " + adminToken}

	// 1. Configure with a password.
	setup := `{"brokerURL":"tcp://10.0.1.5:1883","hosts":{"player-01":"FPP-Player"},"password":"original-secret"}`
	req := newJSONRequest(t, http.MethodPut, "/api/v1/config/fpp.mqtt", setup, auth)
	resp, body := doRawRequest(t, api.Handler, req)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("setup PUT status = %d, want 200; body: %s", resp.StatusCode, body)
	}
	if secret.password != "original-secret" {
		t.Fatalf("secret store password = %q after setup, want %q", secret.password, "original-secret")
	}

	// 2. GET the current configuration — the client never sees the password.
	getResp, getBody := doRequest(t, api.Handler, "GET", "/api/v1/config/fpp.mqtt", auth)
	if getResp.StatusCode != http.StatusOK {
		t.Fatalf("GET status = %d, want 200; body: %s", getResp.StatusCode, getBody)
	}
	getM := decodeMap(t, getBody)
	getPayload := getM["payload"].(map[string]any)

	// 3. PUT the GET response's payload straight back (a client editing,
	// say, only topicPrefix in a UI form and re-submitting the whole
	// object it fetched — the exact naive round trip decision 7 warns
	// about), WITHOUT a "password" key, since GET never gave it one.
	roundTrip := map[string]any{
		"brokerURL":   getPayload["brokerURL"],
		"username":    getPayload["username"],
		"topicPrefix": getPayload["topicPrefix"],
		"hosts":       getPayload["hosts"],
	}
	roundTripRaw, err := json.Marshal(roundTrip)
	if err != nil {
		t.Fatalf("marshal round-trip body: %v", err)
	}
	roundTripReq := newJSONRequest(t, http.MethodPut, "/api/v1/config/fpp.mqtt", string(roundTripRaw), auth)
	roundTripResp, roundTripBody := doRawRequest(t, api.Handler, roundTripReq)
	if roundTripResp.StatusCode != http.StatusOK {
		t.Fatalf("round-trip PUT status = %d, want 200; body: %s", roundTripResp.StatusCode, roundTripBody)
	}

	// 4. The password must have survived, unchanged, in the secret store.
	if secret.password != "original-secret" {
		t.Fatalf("secret store password = %q after a GET-then-PUT round trip with no password key, want it unchanged at %q", secret.password, "original-secret")
	}
	present, err := secret.HasFPPMQTTPassword(context.Background())
	if err != nil {
		t.Fatalf("HasFPPMQTTPassword: %v", err)
	}
	if !present {
		t.Fatalf("HasFPPMQTTPassword = false after the round trip, want true")
	}

	rtM := decodeMap(t, roundTripBody)
	rtPayload := rtM["payload"].(map[string]any)
	if passwordSet, _ := rtPayload["passwordSet"].(bool); !passwordSet {
		t.Errorf("passwordSet after round trip = %v, want true", rtPayload["passwordSet"])
	}
}

// TestPutFPPMQTTConfigNullPasswordClears proves the explicit-clear path:
// a PUT naming "password": null (or "") removes the stored credential —
// distinct from omitting the key entirely.
func TestPutFPPMQTTConfigNullPasswordClears(t *testing.T) {
	svc, st, _ := newTestIdentityServiceWithStore(t, fixedClock(testNow))
	admin := mustCreatePrincipal(t, svc, "admin-1", identity.RoleAdmin)
	adminToken := mustIssueToken(t, svc, admin.ID)
	deps, secret := fppMQTTConfigTestDeps(svc, st)
	api := New(deps, Options{Clock: fixedClock(testNow), Logger: testLogger()})
	auth := map[string]string{"Authorization": "Bearer " + adminToken}

	setup := `{"brokerURL":"tcp://10.0.1.5:1883","hosts":{"player-01":"FPP-Player"},"password":"original-secret"}`
	req := newJSONRequest(t, http.MethodPut, "/api/v1/config/fpp.mqtt", setup, auth)
	if resp, body := doRawRequest(t, api.Handler, req); resp.StatusCode != http.StatusOK {
		t.Fatalf("setup PUT status = %d, want 200; body: %s", resp.StatusCode, body)
	}

	clear := `{"password":null}`
	req = newJSONRequest(t, http.MethodPut, "/api/v1/config/fpp.mqtt", clear, auth)
	resp, body := doRawRequest(t, api.Handler, req)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("clear PUT status = %d, want 200; body: %s", resp.StatusCode, body)
	}
	m := decodeMap(t, body)
	payload := m["payload"].(map[string]any)
	if passwordSet, _ := payload["passwordSet"].(bool); passwordSet {
		t.Errorf("passwordSet after clearing = %v, want false", payload["passwordSet"])
	}
	present, err := secret.HasFPPMQTTPassword(context.Background())
	if err != nil {
		t.Fatalf("HasFPPMQTTPassword: %v", err)
	}
	if present {
		t.Fatalf("HasFPPMQTTPassword = true after an explicit null clear, want false")
	}
}

// TestPutFPPMQTTConfigEmptyStringPasswordClears proves the second half of
// decodeFPPMQTTConfigPutBody's documented policy: an explicit "" is the
// scalar-string equivalent of null (JSON has no third way to say "make
// this field explicitly nothing" for a plain string), so it clears exactly
// like null, distinctly from an absent key which keeps the stored value.
func TestPutFPPMQTTConfigEmptyStringPasswordClears(t *testing.T) {
	svc, st, _ := newTestIdentityServiceWithStore(t, fixedClock(testNow))
	admin := mustCreatePrincipal(t, svc, "admin-1", identity.RoleAdmin)
	adminToken := mustIssueToken(t, svc, admin.ID)
	deps, secret := fppMQTTConfigTestDeps(svc, st)
	api := New(deps, Options{Clock: fixedClock(testNow), Logger: testLogger()})
	auth := map[string]string{"Authorization": "Bearer " + adminToken}

	setup := `{"brokerURL":"tcp://10.0.1.5:1883","hosts":{"player-01":"FPP-Player"},"password":"original-secret"}`
	req := newJSONRequest(t, http.MethodPut, "/api/v1/config/fpp.mqtt", setup, auth)
	if resp, body := doRawRequest(t, api.Handler, req); resp.StatusCode != http.StatusOK {
		t.Fatalf("setup PUT status = %d, want 200; body: %s", resp.StatusCode, body)
	}

	clear := `{"password":""}`
	req = newJSONRequest(t, http.MethodPut, "/api/v1/config/fpp.mqtt", clear, auth)
	resp, body := doRawRequest(t, api.Handler, req)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("clear PUT status = %d, want 200; body: %s", resp.StatusCode, body)
	}
	m := decodeMap(t, body)
	payload := m["payload"].(map[string]any)
	if passwordSet, _ := payload["passwordSet"].(bool); passwordSet {
		t.Errorf("passwordSet after clearing with \"\" = %v, want false", payload["passwordSet"])
	}
	present, err := secret.HasFPPMQTTPassword(context.Background())
	if err != nil {
		t.Fatalf("HasFPPMQTTPassword: %v", err)
	}
	if present {
		t.Fatalf("HasFPPMQTTPassword = true after an explicit \"\" clear, want false")
	}
}

func TestPutFPPMQTTConfigEnvVarSetRefusesWith409(t *testing.T) {
	svc, st, _ := newTestIdentityServiceWithStore(t, fixedClock(testNow))
	admin := mustCreatePrincipal(t, svc, "admin-1", identity.RoleAdmin)
	adminToken := mustIssueToken(t, svc, admin.ID)
	deps, _ := fppMQTTConfigTestDeps(svc, st)
	deps.FPPMQTTEnvVarSet = true
	api := New(deps, Options{Clock: fixedClock(testNow), Logger: testLogger()})

	req := newJSONRequest(t, http.MethodPut, "/api/v1/config/fpp.mqtt", `{"brokerURL":"tcp://x:1883","hosts":{"a":"b"}}`,
		map[string]string{"Authorization": "Bearer " + adminToken})
	resp, body := doRawRequest(t, api.Handler, req)
	if resp.StatusCode != http.StatusConflict {
		t.Fatalf("status = %d, want 409; body: %s", resp.StatusCode, body)
	}
	if !strings.Contains(string(body), "SHOWMESH_FPP_MQTT_BROKER_URL") {
		t.Errorf("body = %s, want it to name SHOWMESH_FPP_MQTT_BROKER_URL", body)
	}
}

func TestPutFPPMQTTConfigMigrationDeferredRefusesWithDifferentRemedy(t *testing.T) {
	svc, st, _ := newTestIdentityServiceWithStore(t, fixedClock(testNow))
	admin := mustCreatePrincipal(t, svc, "admin-1", identity.RoleAdmin)
	adminToken := mustIssueToken(t, svc, admin.ID)
	deps, _ := fppMQTTConfigTestDeps(svc, st)
	deps.FPPMQTTEnvVarSet = true
	deps.FPPMQTTMigrationDeferred = true
	api := New(deps, Options{Clock: fixedClock(testNow), Logger: testLogger()})

	req := newJSONRequest(t, http.MethodPut, "/api/v1/config/fpp.mqtt", `{"brokerURL":"tcp://x:1883","hosts":{"a":"b"}}`,
		map[string]string{"Authorization": "Bearer " + adminToken})
	resp, body := doRawRequest(t, api.Handler, req)
	if resp.StatusCode != http.StatusConflict {
		t.Fatalf("status = %d, want 409; body: %s", resp.StatusCode, body)
	}
	if !strings.Contains(string(body), "Do NOT remove") {
		t.Errorf("body = %s, want the deferred-migration remedy, not the standard one", body)
	}
}

// TestPutFPPMQTTConfigNullBodyRejected: a literal null body decodes into a
// nil map with no error, which would read as "every field absent" and mint
// a no-op revision — refused with the same 400 the per-field null rules
// use.
func TestPutFPPMQTTConfigNullBodyRejected(t *testing.T) {
	svc, st, _ := newTestIdentityServiceWithStore(t, fixedClock(testNow))
	admin := mustCreatePrincipal(t, svc, "admin-1", identity.RoleAdmin)
	adminToken := mustIssueToken(t, svc, admin.ID)
	deps, _ := fppMQTTConfigTestDeps(svc, st)
	api := New(deps, Options{Clock: fixedClock(testNow), Logger: testLogger()})

	req := newJSONRequest(t, http.MethodPut, "/api/v1/config/fpp.mqtt", `null`,
		map[string]string{"Authorization": "Bearer " + adminToken})
	resp, body := doRawRequest(t, api.Handler, req)
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400 (a null body is not an object); body: %s", resp.StatusCode, body)
	}
}
