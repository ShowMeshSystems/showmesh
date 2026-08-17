package api

import (
	"net/http"
	"testing"

	"github.com/showmeshsystems/showmesh/internal/coordinator/identity"
)

// TestOpenAPIPrincipalsResponsesMatchRealResponses is Track G seam G-5's
// own conformance test, following TestOpenAPIConfigResponsesMatchRealResponses'
// established pattern: every response this surface can produce, validated
// against a REAL handler response, not hand-built JSON.
func TestOpenAPIPrincipalsResponsesMatchRealResponses(t *testing.T) {
	c := newOpenAPICompiler(t)

	svc := newTestIdentityService(t, fixedClock(testNow))
	admin := mustCreatePrincipal(t, svc, "admin-1", identity.RoleAdmin)
	token := mustIssueToken(t, svc, admin.ID)
	api := New(authTestDeps(svc), Options{Clock: fixedClock(testNow), Logger: testLogger()})
	auth := map[string]string{"Authorization": "Bearer " + token}

	// POST /principals: create a second principal this test can mutate
	// freely without risking the lockout guard against admin-1.
	createReq := newJSONRequest(t, http.MethodPost, "/api/v1/principals",
		`{"name":"operator-1","kind":"human","role":"operator","password":"a-strong-password-2"}`, auth)
	createResp, createBody := doRawRequest(t, api.Handler, createReq)
	if createResp.StatusCode != http.StatusOK {
		t.Fatalf("POST /principals: status = %d, want 200; body: %s", createResp.StatusCode, createBody)
	}
	assertMatchesSchema(t, c, "PrincipalResponse", createBody)

	var created struct {
		Principal struct {
			ID string `json:"id"`
		} `json:"principal"`
	}
	mustDecodeJSON(t, createBody, &created)
	operatorID := created.Principal.ID

	// GET /principals
	_, listBody := doRequest(t, api.Handler, "GET", "/api/v1/principals", auth)
	assertMatchesSchema(t, c, "PrincipalsResponse", listBody)

	// GET /principals/{id}
	_, getBody := doRequest(t, api.Handler, "GET", "/api/v1/principals/"+operatorID, auth)
	assertMatchesSchema(t, c, "PrincipalResponse", getBody)

	// PUT /principals/{id}/role
	roleReq := newJSONRequest(t, http.MethodPut, "/api/v1/principals/"+operatorID+"/role", `{"role":"viewer"}`, auth)
	roleResp, roleBody := doRawRequest(t, api.Handler, roleReq)
	if roleResp.StatusCode != http.StatusOK {
		t.Fatalf("PUT .../role: status = %d, want 200; body: %s", roleResp.StatusCode, roleBody)
	}
	assertMatchesSchema(t, c, "PrincipalResponse", roleBody)

	// POST /principals/{id}/disable
	disableResp, disableBody := doRequest(t, api.Handler, http.MethodPost, "/api/v1/principals/"+operatorID+"/disable", auth)
	if disableResp.StatusCode != http.StatusOK {
		t.Fatalf("POST .../disable: status = %d, want 200; body: %s", disableResp.StatusCode, disableBody)
	}
	assertMatchesSchema(t, c, "PrincipalResponse", disableBody)

	// POST /principals/{id}/enable
	enableResp, enableBody := doRequest(t, api.Handler, http.MethodPost, "/api/v1/principals/"+operatorID+"/enable", auth)
	if enableResp.StatusCode != http.StatusOK {
		t.Fatalf("POST .../enable: status = %d, want 200; body: %s", enableResp.StatusCode, enableBody)
	}
	assertMatchesSchema(t, c, "PrincipalResponse", enableBody)

	// POST /principals/{id}/password
	pwReq := newJSONRequest(t, http.MethodPost, "/api/v1/principals/"+operatorID+"/password", `{"password":"a-new-password-3"}`, auth)
	pwResp, pwBody := doRawRequest(t, api.Handler, pwReq)
	if pwResp.StatusCode != http.StatusOK {
		t.Fatalf("POST .../password: status = %d, want 200; body: %s", pwResp.StatusCode, pwBody)
	}
	assertMatchesSchema(t, c, "PrincipalResponse", pwBody)

	// POST /principals/{id}/tokens
	issueReq := newJSONRequest(t, http.MethodPost, "/api/v1/principals/"+operatorID+"/tokens", `{"label":"ci"}`, auth)
	issueResp, issueBody := doRawRequest(t, api.Handler, issueReq)
	if issueResp.StatusCode != http.StatusOK {
		t.Fatalf("POST .../tokens: status = %d, want 200; body: %s", issueResp.StatusCode, issueBody)
	}
	assertMatchesSchema(t, c, "IssueTokenResponse", issueBody)

	var issued struct {
		Token struct {
			ID string `json:"id"`
		} `json:"token"`
	}
	mustDecodeJSON(t, issueBody, &issued)
	tokenID := issued.Token.ID

	// GET /principals/{id}/tokens
	_, tokensBody := doRequest(t, api.Handler, "GET", "/api/v1/principals/"+operatorID+"/tokens", auth)
	assertMatchesSchema(t, c, "TokensResponse", tokensBody)

	// DELETE /principals/{id}/tokens/{tokenId}
	deleteResp, deleteBody := doRequest(t, api.Handler, http.MethodDelete, "/api/v1/principals/"+operatorID+"/tokens/"+tokenID, auth)
	if deleteResp.StatusCode != http.StatusNoContent {
		t.Fatalf("DELETE .../tokens/%s: status = %d, want 204; body: %s", tokenID, deleteResp.StatusCode, deleteBody)
	}
	if len(deleteBody) != 0 {
		t.Errorf("DELETE .../tokens/%s: body = %q, want empty (204 No Content)", tokenID, deleteBody)
	}
}

// TestOpenAPIPrincipalLockoutProblemMatchesSchema is requirement 3's own
// conformance check: disabling the coordinator's LAST enabled
// administrator is refused with a 409 whose body still validates against
// the shared Problem schema, exactly like every other refusal class.
func TestOpenAPIPrincipalLockoutProblemMatchesSchema(t *testing.T) {
	c := newOpenAPICompiler(t)

	svc := newTestIdentityService(t, fixedClock(testNow))
	admin := mustCreatePrincipal(t, svc, "only-admin", identity.RoleAdmin)
	token := mustIssueToken(t, svc, admin.ID)
	api := New(authTestDeps(svc), Options{Clock: fixedClock(testNow), Logger: testLogger()})
	auth := map[string]string{"Authorization": "Bearer " + token}

	resp, body := doRequest(t, api.Handler, http.MethodPost, "/api/v1/principals/"+admin.ID+"/disable", auth)
	if resp.StatusCode != http.StatusConflict {
		t.Fatalf("POST .../disable on the last admin: status = %d, want 409; body: %s", resp.StatusCode, body)
	}
	assertMatchesSchema(t, c, "Problem", body)
}
