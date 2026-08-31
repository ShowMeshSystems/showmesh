package api

import (
	"context"
	"errors"
	"net/http"
	"testing"

	"github.com/showmeshsystems/showmesh/internal/coordinator/config"
	"github.com/showmeshsystems/showmesh/internal/coordinator/identity"
	"github.com/showmeshsystems/showmesh/internal/coordinator/store"
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

const validAudioSettingsBody = `{"driftIgnoreThresholdMs":30,"defaultFadeCurve":"linear","defaultFadeDurationMs":2000,"defaultMaxBackgroundGainDb":-7.96,"duckTargetGainDb":-13.98,"duckFadeDurationMs":150,"duckRestoreFadeDurationMs":700,"ltcFrameRate":"30","ltcDefaultStartOffset":"00:00:00:00"}`

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
	if !containsAll(string(getBody), `"driftIgnoreThresholdMs":30`) || !containsAll(string(getBody), `"duckTargetGainDb":-13.98`) || !containsAll(string(getBody), `"source":"api"`) {
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

// seedUndecodableAudioSettingsRevision writes payloadJSON directly to the
// store as the audio.settings singleton's active revision, bypassing the
// API's own validating PUT — the only way to reproduce a revision that
// was legal when written and stopped decoding later, the same way a
// migration or a tightened bound would leave one behind.
func seedUndecodableAudioSettingsRevision(t *testing.T, st *store.Store, payloadJSON string) {
	t.Helper()
	ctx := context.Background()
	rec, err := st.CreateConfigRevision(ctx, store.ConfigRevisionRecord{
		Kind: config.AudioSettingsConfigKind, ObjectID: config.AudioSettingsConfigObjectID,
		Revision: 1, PayloadJSON: payloadJSON, Source: config.AudioSettingsSourceAPI,
	})
	if err != nil {
		t.Fatalf("seed revision: %v", err)
	}
	if _, err := st.ActivateConfigRevision(ctx, config.AudioSettingsConfigKind, config.AudioSettingsConfigObjectID, rec.Revision); err != nil {
		t.Fatalf("activate seeded revision: %v", err)
	}
}

// TestSnapshotReportsAudioConfigPushUnusableForAnUndecodableRevision is
// this issue's own acceptance: a stored revision that fails to decode (here,
// the reachable defaultMaxBackgroundGainDb = -66.02 dB left behind by
// schemaV19's ceiling-only clamp of a legal pre-decibel 0.0005) must
// produce operator-visible evidence naming the reason, not just a Warn log
// line. GET /api/v1/snapshot is what an operator actually reads.
func TestSnapshotReportsAudioConfigPushUnusableForAnUndecodableRevision(t *testing.T) {
	svc, st, _ := newTestIdentityServiceWithStore(t, fixedClock(testNow))
	admin := mustCreatePrincipal(t, svc, "admin-1", identity.RoleAdmin)
	token := mustIssueToken(t, svc, admin.ID)
	api := New(showConfigTestDeps(svc, st), Options{Clock: fixedClock(testNow), Logger: testLogger()})

	seedUndecodableAudioSettingsRevision(t, st,
		`{"driftIgnoreThresholdMs":20,"defaultFadeCurve":"linear","defaultFadeDurationMs":1000,`+
			`"defaultMaxBackgroundGainDb":-66.02,"duckTargetGainDb":-12.04,"ltcFrameRate":"30","ltcDefaultStartOffset":"00:00:00:00"}`)

	resp, body := doRequest(t, api.Handler, "GET", "/api/v1/snapshot", map[string]string{"Authorization": "Bearer " + token})
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200; body: %s", resp.StatusCode, body)
	}
	if !containsAll(string(body), `"audioConfigPush":{"state":"unusable"`) {
		t.Fatalf("snapshot does not report the push as unusable; body: %s", body)
	}
	if !containsAll(string(body), "defaultMaxBackgroundGainDb") || !containsAll(string(body), "-60") {
		t.Fatalf("snapshot's reason does not name the failing field and bound; body: %s", body)
	}
	t.Logf("what an operator reads: %s", body)
}

// TestSnapshotReportsAudioConfigPushUsableWhenNothingEverWritten proves
// the unconfigured coordinator (falls back to the built-in default,
// exactly as [pushSettings] does) is not mistakenly reported unusable.
func TestSnapshotReportsAudioConfigPushUsableWhenNothingEverWritten(t *testing.T) {
	svc, st, _ := newTestIdentityServiceWithStore(t, fixedClock(testNow))
	admin := mustCreatePrincipal(t, svc, "admin-1", identity.RoleAdmin)
	token := mustIssueToken(t, svc, admin.ID)
	api := New(showConfigTestDeps(svc, st), Options{Clock: fixedClock(testNow), Logger: testLogger()})

	_, body := doRequest(t, api.Handler, "GET", "/api/v1/snapshot", map[string]string{"Authorization": "Bearer " + token})
	if !containsAll(string(body), `"audioConfigPush":{"state":"usable","reason":null}`) {
		t.Fatalf("unconfigured coordinator must report usable/null; body: %s", body)
	}
}

// TestSnapshotDegradesAudioConfigPushOnConfigStoreFailure is this review
// round's own acceptance: a config-store failure while reading
// audio.settings (a dangling current_revision pointer is one real cause —
// [errInjectingConfigStore]'s own doc comment names transient SQLite
// failures as another) must degrade only the audioConfigPush field, not
// the whole snapshot. On the pre-fix code this same setup returned 500
// for the entire response, costing the operator the node/FPP/collector
// evidence the snapshot also carries — resolumeCompositionDegradeOnError
// already established the "you cannot act, never you cannot see"
// resolution for this exact handler and this exact dependency; this
// applies it here too.
func TestSnapshotDegradesAudioConfigPushOnConfigStoreFailure(t *testing.T) {
	svc, st, _ := newTestIdentityServiceWithStore(t, fixedClock(testNow))
	admin := mustCreatePrincipal(t, svc, "admin-1", identity.RoleAdmin)
	token := mustIssueToken(t, svc, admin.ID)

	// A real, decodable revision — this proves the field is reported as
	// "unknown" (a store failure) rather than "unusable" (a decode
	// failure): the two must not be conflated, since a store failure says
	// nothing about whether the stored revision itself is usable.
	seedUndecodableAudioSettingsRevision(t, st, validAudioSettingsBody)

	deps := showConfigTestDeps(svc, st)
	deps.Config = &errInjectingConfigStore{Store: st, getConfigRevisionErr: errors.New("simulated transient sqlite error")}
	api := New(deps, Options{Clock: fixedClock(testNow), Logger: testLogger()})

	resp, body := doRequest(t, api.Handler, "GET", "/api/v1/snapshot", map[string]string{"Authorization": "Bearer " + token})
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200 — a config-store failure reading audioConfigPush must degrade that field, not fail the snapshot; body: %s", resp.StatusCode, body)
	}
	if !containsAll(string(body), `"audioConfigPush":{"state":"unknown"`) {
		t.Fatalf("snapshot does not report the push status as unknown on a store failure; body: %s", body)
	}
	if containsAll(string(body), `"audioConfigPush":{"state":"unusable"`) {
		t.Fatalf("a store failure must not be reported as a decode failure (unusable); body: %s", body)
	}
	t.Logf("what an operator reads when the coordinator itself cannot read audio.settings: %s", body)
}
