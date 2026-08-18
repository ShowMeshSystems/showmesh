package api

import (
	"net/http"
	"testing"

	"github.com/showmeshsystems/showmesh/internal/coordinator/identity"
)

// This file is Track F seam F1's own conformance coverage, following
// openapi_showobjects_test.go's exact pattern one file over: every schema
// this seam added is validated against a REAL response from a real
// coordinator wiring, never hand-built JSON.

func TestOpenAPINightSessionDocumentIsWellFormed(t *testing.T) {
	c := newOpenAPICompiler(t)
	for _, name := range []string{
		"ConfigNightSessionFPPPlaylist", "ConfigNightSessionAssetRef",
		"ConfigNightSessionBackgroundAudioItem", "ConfigNightSessionBackgroundAudio",
		"ConfigNightSessionResting", "ConfigNightSessionCue",
		"ConfigNightSessionEnterShow", "ConfigNightSessionEnterResting",
		"ConfigNightSession", "NightSessionConfigResponse",
		"ConfigNightSessionActive", "NightSessionActiveConfigResponse",
		// The WRITE-side schemas (review finding 5): a separate shape from
		// the fully-resolved read schemas above, because repeat, barrier,
		// onFailure, and endOfNightPlaylist are all defaulted on write.
		"ConfigNightSessionCueWrite", "ConfigNightSessionEnterShowWrite",
		"ConfigNightSessionEnterRestingWrite", "ConfigNightSessionBackgroundAudioWrite",
		"ConfigNightSessionRestingWrite", "ConfigNightSessionWrite",
	} {
		compileSchema(t, c, name)
	}
}

// TestOpenAPINightSessionResponsesMatchRealResponses proves every route
// this seam added against a real coordinator wiring, including the
// background-audio block, an explicit crossfade item transition, the
// dangling-reference refusal, and the active pointer's
// zero-to-one-and-back-to-zero transition.
func TestOpenAPINightSessionResponsesMatchRealResponses(t *testing.T) {
	c := newOpenAPICompiler(t)

	svc, st, _ := newTestIdentityServiceWithStore(t, fixedClock(testNow))
	admin := mustCreatePrincipal(t, svc, "admin-1", identity.RoleAdmin)
	token := mustIssueToken(t, svc, admin.ID)
	api := New(nightSessionTestDeps(svc, st), Options{Clock: fixedClock(testNow), Logger: testLogger()})
	auth := map[string]string{"Authorization": "Bearer " + token}

	mustPutShowAction(t, api, token, "lighting-fade-out", validShowActionFPPBody)
	mustCreateNightSessionAsset(t, st, "halloween-2026", "resting-loop", "player-01")
	mustCreateNightSessionAsset(t, st, "halloween-2026", "bg-track-1", "player-01")

	// --- kind "night.session", with a full backgroundAudio block ---
	bodyWithBackgroundAudio := `{
		"show": "halloween-2026",
		"label": "Halloween main loop",
		"showPlaylist": {"fppInstanceId": "player-01", "playlist": "halloween-show"},
		"resting": {
			"fppInstanceId": "player-01",
			"playlist": "halloween-resting",
			"timelineAsset": {"show": "halloween-2026", "sequence": "resting-loop", "target": "player-01"},
			"endOfNightRepeat": true,
			"backgroundAudio": {
				"items": [{"itemId": "track-1", "show": "halloween-2026", "sequence": "bg-track-1", "target": "player-01"}],
				"repeat": "playlist",
				"resume": "resume",
				"itemTransition": "crossfade",
				"crossfadeMs": 500,
				"maxGainDb": -10
			}
		},
		"enterShow": {
			"cues": [{"name": "lighting-fade", "role": "lighting", "action": "lighting-fade-out", "offsetMs": -20000, "barrier": true}],
			"blackoutHoldMs": 6000
		},
		"enterResting": {"cues": [], "blackoutAfterShowMs": 6000}
	}`
	// The REQUEST body validates against the WRITE schema (review finding
	// 5): this is the direction the old single shared schema got wrong
	// (repeat/barrier/onFailure/endOfNightPlaylist marked required when
	// the server accepts them absent), and the conformance test only ever
	// checked responses. validNightSessionBody (nightsession_test.go)
	// omits endOfNightPlaylist and every cue's onFailure/barrier, which is
	// exactly the minimal shape ConfigNightSession (the OLD, still-shared
	// schema) would have rejected outright.
	assertMatchesSchema(t, c, "ConfigNightSessionWrite", []byte(validNightSessionBody))
	assertMatchesSchema(t, c, "ConfigNightSessionWrite", []byte(bodyWithBackgroundAudio))

	putReq := newJSONRequest(t, http.MethodPut, "/api/v1/config/night.session/halloween-main", bodyWithBackgroundAudio, auth)
	putResp, putBody := doRawRequest(t, api.Handler, putReq)
	if putResp.StatusCode != http.StatusOK {
		t.Fatalf("PUT night.session: status = %d, want 200; body: %s", putResp.StatusCode, putBody)
	}
	assertMatchesSchema(t, c, "NightSessionConfigResponse", putBody)

	_, getBody := doRequest(t, api.Handler, "GET", "/api/v1/config/night.session/halloween-main", auth)
	assertMatchesSchema(t, c, "NightSessionConfigResponse", getBody)

	_, listBody := doRequest(t, api.Handler, "GET", "/api/v1/config/night.session", auth)
	assertMatchesSchema(t, c, "ConfigObjectsListResponse", listBody)

	_, revBody := doRequest(t, api.Handler, "GET", "/api/v1/config/night.session/halloween-main/revisions", auth)
	assertMatchesSchema(t, c, "ConfigRevisionsResponse", revBody)

	_, revOneBody := doRequest(t, api.Handler, "GET", "/api/v1/config/night.session/halloween-main/revisions/1", auth)
	assertMatchesSchema(t, c, "NightSessionConfigResponse", revOneBody)

	nodeFilterRejectedResp, nodeFilterRejectedBody := doRequest(t, api.Handler, "GET", "/api/v1/config/night.session?node=player-01", auth)
	if nodeFilterRejectedResp.StatusCode != http.StatusBadRequest {
		t.Fatalf("GET night.session?node=: status = %d, want 400; body: %s", nodeFilterRejectedResp.StatusCode, nodeFilterRejectedBody)
	}
	assertMatchesSchema(t, c, "Problem", nodeFilterRejectedBody)

	// A validation-error sample: a calendar field, proving Problem's shape
	// on this seam's own refusal path.
	badReq := newJSONRequest(t, http.MethodPut, "/api/v1/config/night.session/halloween-main",
		`{"show":"halloween-2026","label":"x","at":"20:00","showPlaylist":{},"resting":{},"enterShow":{},"enterResting":{}}`, auth)
	badResp, badBody := doRawRequest(t, api.Handler, badReq)
	if badResp.StatusCode != http.StatusBadRequest {
		t.Fatalf("PUT night.session (calendar field): status = %d, want 400; body: %s", badResp.StatusCode, badBody)
	}
	assertMatchesSchema(t, c, "Problem", badBody)

	// --- kind "night.session.active" ---
	putActiveReq := newJSONRequest(t, http.MethodPut, "/api/v1/config/night.session.active", `{"session":"halloween-main"}`, auth)
	putActiveResp, putActiveBody := doRawRequest(t, api.Handler, putActiveReq)
	if putActiveResp.StatusCode != http.StatusOK {
		t.Fatalf("PUT night.session.active: status = %d, want 200; body: %s", putActiveResp.StatusCode, putActiveBody)
	}
	assertMatchesSchema(t, c, "NightSessionActiveConfigResponse", putActiveBody)

	_, getActiveBody := doRequest(t, api.Handler, "GET", "/api/v1/config/night.session.active", auth)
	assertMatchesSchema(t, c, "NightSessionActiveConfigResponse", getActiveBody)

	_, revActiveBody := doRequest(t, api.Handler, "GET", "/api/v1/config/night.session.active/revisions", auth)
	assertMatchesSchema(t, c, "ConfigRevisionsResponse", revActiveBody)

	// The zero-to-one-and-back-to-zero transition (ADR-039 rule 4): clear
	// the pointer with an explicit empty session and confirm the response
	// still matches the schema (session: "").
	clearActiveReq := newJSONRequest(t, http.MethodPut, "/api/v1/config/night.session.active", `{"session":""}`, auth)
	clearActiveResp, clearActiveBody := doRawRequest(t, api.Handler, clearActiveReq)
	if clearActiveResp.StatusCode != http.StatusOK {
		t.Fatalf("PUT night.session.active (clear): status = %d, want 200; body: %s", clearActiveResp.StatusCode, clearActiveBody)
	}
	assertMatchesSchema(t, c, "NightSessionActiveConfigResponse", clearActiveBody)
}
