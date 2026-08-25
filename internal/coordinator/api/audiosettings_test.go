package api

import (
	"net/http"
	"testing"

	"github.com/showmeshsystems/showmesh/internal/coordinator/identity"
)

// This file is Track C seam C1b's own test suite for the audio.settings
// singleton, mirroring rendersettings_test.go's own shape.

// TestGetAudioSettingsDefaultsBeforeAnyWrite proves GET never 404s: the
// unconfigured state reports the built-in default with revision 0 and
// source "default", matching render.settings' own posture.
func TestGetAudioSettingsDefaultsBeforeAnyWrite(t *testing.T) {
	svc, st, _ := newTestIdentityServiceWithStore(t, fixedClock(testNow))
	admin := mustCreatePrincipal(t, svc, "admin-1", identity.RoleAdmin)
	token := mustIssueToken(t, svc, admin.ID)
	api := New(showConfigTestDeps(svc, st), Options{Clock: fixedClock(testNow), Logger: testLogger()})

	resp, body := doRequest(t, api.Handler, "GET", "/api/v1/config/audio.settings", map[string]string{"Authorization": "Bearer " + token})
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200; body: %s", resp.StatusCode, body)
	}
	if !containsAll(string(body), `"revision":0`) || !containsAll(string(body), `"source":"default"`) {
		t.Fatalf("body missing revision:0/source:default; body: %s", body)
	}
}

const validAudioSettingsBody = `{"driftIgnoreThresholdMs":30,"defaultFadeCurve":"linear","defaultFadeDurationMs":2000,"defaultMaxBackgroundGain":0.4,"duckTargetGain":0.2,"ltcFrameRate":"30","ltcDefaultStartOffset":"00:00:00:00"}`

// TestPutAudioSettingsThenGetReflectsWrittenValue proves the zero-to-one
// transition: an unconfigured kind, one write, and a subsequent GET that
// reflects it rather than the default.
func TestPutAudioSettingsThenGetReflectsWrittenValue(t *testing.T) {
	svc, st, _ := newTestIdentityServiceWithStore(t, fixedClock(testNow))
	admin := mustCreatePrincipal(t, svc, "admin-1", identity.RoleAdmin)
	token := mustIssueToken(t, svc, admin.ID)
	api := New(showConfigTestDeps(svc, st), Options{Clock: fixedClock(testNow), Logger: testLogger()})

	req := newJSONRequest(t, http.MethodPut, "/api/v1/config/audio.settings", validAudioSettingsBody, map[string]string{"Authorization": "Bearer " + token})
	resp, body := doRawRequest(t, api.Handler, req)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("PUT status = %d, want 200; body: %s", resp.StatusCode, body)
	}

	_, getBody := doRequest(t, api.Handler, "GET", "/api/v1/config/audio.settings", map[string]string{"Authorization": "Bearer " + token})
	if !containsAll(string(getBody), `"driftIgnoreThresholdMs":30`) || !containsAll(string(getBody), `"duckTargetGain":0.2`) || !containsAll(string(getBody), `"source":"api"`) {
		t.Fatalf("GET does not reflect written value; body: %s", getBody)
	}
}

// TestPutAudioSettingsRejectsInvalidBody proves an invalid payload is
// refused before activation (ADR-009): no revision is created, so a
// following GET still reports the default.
func TestPutAudioSettingsRejectsInvalidBody(t *testing.T) {
	svc, st, _ := newTestIdentityServiceWithStore(t, fixedClock(testNow))
	admin := mustCreatePrincipal(t, svc, "admin-1", identity.RoleAdmin)
	token := mustIssueToken(t, svc, admin.ID)
	api := New(showConfigTestDeps(svc, st), Options{Clock: fixedClock(testNow), Logger: testLogger()})

	req := newJSONRequest(t, http.MethodPut, "/api/v1/config/audio.settings", `{"defaultFadeCurve":"linear"}`, map[string]string{"Authorization": "Bearer " + token})
	resp, body := doRawRequest(t, api.Handler, req)
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400; body: %s", resp.StatusCode, body)
	}

	_, getBody := doRequest(t, api.Handler, "GET", "/api/v1/config/audio.settings", map[string]string{"Authorization": "Bearer " + token})
	if !containsAll(string(getBody), `"revision":0`) {
		t.Fatalf("a rejected write must leave no revision behind; body: %s", getBody)
	}
}
