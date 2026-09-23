package api

import (
	"net/http"
	"testing"

	"github.com/showmeshsystems/showmesh/internal/coordinator/config"
	"github.com/showmeshsystems/showmesh/internal/coordinator/identity"
)

func TestPutShowCueActionsReferenceChecks(t *testing.T) {
	svc, st, _ := newTestIdentityServiceWithStore(t, fixedClock(testNow))
	admin := mustCreatePrincipal(t, svc, "admin-1", identity.RoleAdmin)
	token := mustIssueToken(t, svc, admin.ID)
	api := New(showObjectsTestDeps(svc, st), Options{Clock: fixedClock(testNow), Logger: testLogger()})
	mustPutShow(t, api, token, "halloween-2026", `{"name":"Halloween 2026"}`)
	mustPutShow(t, api, token, "christmas-2026", `{"name":"Christmas 2026"}`)
	mustPutAction(t, api, token, "blackout-now", validShowActionResolumeBlackoutBody)
	mustPutAction(t, api, token, "sleigh-blackout", `{"show":"christmas-2026","label":"Blackout","safetyClass":"blackout","target":{"integration":"resolume","action":"blackout"}}`)

	cases := []struct {
		name, actions string
		wantCode      string
	}{
		{"unknown action", `["ghost"]`, config.ValidationCodeFieldUnknownReference},
		{"other show's action", `["sleigh-blackout"]`, config.ValidationCodeCrossShowReference},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			body := `{"show":"halloween-2026","name":"Song one","outputs":{"actions":` + c.actions + `}}`
			req := newJSONRequest(t, http.MethodPut, "/api/v1/config/show.cue/song-one", body, map[string]string{"Authorization": "Bearer " + token})
			resp, respBody := doRawRequest(t, api.Handler, req)
			if resp.StatusCode != http.StatusBadRequest {
				t.Fatalf("status = %d, want 400; body: %s", resp.StatusCode, respBody)
			}
			if got, want := decodeMap(t, respBody)["type"], showConfigValidationProblemTypes[c.wantCode]; got != want {
				t.Fatalf("problem.type = %v, want %v", got, want)
			}
		})
	}

	mustPutCue(t, api, token, "song-one", `{"show":"halloween-2026","name":"Song one","outputs":{"actions":["blackout-now"]}}`)
}
