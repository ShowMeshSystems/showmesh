package api

import (
	"crypto/sha256"
	"encoding/base32"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/showmeshsystems/showmesh/internal/coordinator/identity"
)

// This file drives all three pairing routes through a real [API] with a
// real identity service, so the principal, the token and the audit
// entries these tests assert on are the real ones.

// pairingSecret builds a syntactically valid secret from a seed byte, so
// a test can name two different plugins without a fixture file.
func pairingSecret(seed byte) string {
	raw := make([]byte, 32)
	for i := range raw {
		raw[i] = seed + byte(i)
	}
	return hex.EncodeToString(raw)
}

// pairingCodeFor derives a secret's code independently of the code under
// test, so a broken derivation cannot agree with itself.
func pairingCodeFor(t *testing.T, secretHex string) string {
	t.Helper()
	raw, err := hex.DecodeString(secretHex)
	if err != nil {
		t.Fatalf("decode secret: %v", err)
	}
	sum := sha256.Sum256(raw)
	enc := base32.NewEncoding("0123456789ABCDEFGHJKMNPQRSTVWXYZ").WithPadding(base32.NoPadding).EncodeToString(sum[:5])
	return enc[:4] + "-" + enc[4:8]
}

// pairingAPI wires a real coordinator with one configured FPP endpoint
// and an admin token, which is what the operator half of pairing needs.
func pairingAPI(t *testing.T, clock func() time.Time) (*API, *fppCommandTestSetup, string) {
	t.Helper()
	setup := newFPPCommandTestSetup(t, clock)
	setup.fppLister.views = []FPPInstanceView{{InstanceID: "bench-fpp", Endpoint: "http://fpp.invalid"}}
	api := New(setup.deps(), Options{Clock: clock, Logger: testLogger()})
	admin := mustCreatePrincipal(t, setup.svc, "admin-1", identity.RoleAdmin)
	return api, setup, mustIssueToken(t, setup.svc, admin.ID)
}

func startPairingRequest(t *testing.T, instanceID, body, token string) *http.Request {
	t.Helper()
	req := httptest.NewRequest(http.MethodPost, "/api/v1/fpp/"+instanceID+"/pairing", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	return req
}

func claimRequest(t *testing.T, body string) *http.Request {
	t.Helper()
	req := httptest.NewRequest(http.MethodPost, "/api/v1/integrations/fpp/pairing/claim", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	req.RemoteAddr = "192.0.2.10:54321"
	return req
}

type pairingStartForTest struct {
	InstanceID  string `json:"instanceId"`
	Code        string `json:"code"`
	PrincipalID string `json:"principalId"`
	State       string `json:"state"`
	ExpiresAt   string `json:"expiresAt"`
}

type pairingClaimForTest struct {
	Token       string `json:"token"`
	PrincipalID string `json:"principalId"`
	InstanceID  string `json:"instanceId"`
}

// TestPairingHandsTheTokenOnlyToThePluginHoldingTheSecret is the whole
// point of this design: the operator opens the pairing and never sees a
// credential; the plugin presents the secret and receives one that
// actually authenticates.
func TestPairingHandsTheTokenOnlyToThePluginHoldingTheSecret(t *testing.T) {
	api, setup, token := pairingAPI(t, fixedClock(testNow))
	secret := pairingSecret(1)
	code := pairingCodeFor(t, secret)

	resp, body := doRawRequest(t, api.Handler, startPairingRequest(t, "bench-fpp", `{"code":"`+code+`"}`, token))
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("start pairing status = %d, want 200; body: %s", resp.StatusCode, body)
	}
	if strings.Contains(string(body), identity.TokenPrefix) {
		t.Fatalf("the start-pairing response carried a token: %s", body)
	}
	var started pairingStartForTest
	if err := json.Unmarshal(body, &started); err != nil {
		t.Fatalf("decode start pairing: %v; body: %s", err, body)
	}
	if started.State != fppPairingStateWaiting || started.Code != code {
		t.Fatalf("start pairing = %+v, want state waiting and code %s", started, code)
	}

	resp, body = doRawRequest(t, api.Handler, claimRequest(t, `{"secret":"`+secret+`"}`))
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("claim status = %d, want 200; body: %s", resp.StatusCode, body)
	}
	var claimed pairingClaimForTest
	if err := json.Unmarshal(body, &claimed); err != nil {
		t.Fatalf("decode claim: %v; body: %s", err, body)
	}
	if claimed.PrincipalID != started.PrincipalID || claimed.InstanceID != "bench-fpp" {
		t.Fatalf("claim = %+v, want principal %s and instance bench-fpp", claimed, started.PrincipalID)
	}

	auth, err := setup.svc.AuthenticateToken(t.Context(), claimed.Token)
	if err != nil {
		t.Fatalf("the claimed token does not authenticate: %v", err)
	}
	if auth.Principal.Name != "fpp-plugin-bench-fpp" || auth.Principal.Role != identity.RoleScheduler {
		t.Fatalf("claimed principal = %q/%q, want fpp-plugin-bench-fpp/scheduler", auth.Principal.Name, auth.Principal.Role)
	}
}

// TestPairingIsSingleUse: a second claim with the same secret gets the
// same refusal a wrong secret does.
func TestPairingIsSingleUse(t *testing.T) {
	api, _, token := pairingAPI(t, fixedClock(testNow))
	secret := pairingSecret(2)

	doRawRequest(t, api.Handler, startPairingRequest(t, "bench-fpp", `{"code":"`+pairingCodeFor(t, secret)+`"}`, token))
	if resp, body := doRawRequest(t, api.Handler, claimRequest(t, `{"secret":"`+secret+`"}`)); resp.StatusCode != http.StatusOK {
		t.Fatalf("first claim status = %d, want 200; body: %s", resp.StatusCode, body)
	}
	resp, body := doRawRequest(t, api.Handler, claimRequest(t, `{"secret":"`+secret+`"}`))
	if resp.StatusCode != http.StatusNotFound {
		t.Fatalf("second claim status = %d, want 404; body: %s", resp.StatusCode, body)
	}
}

// TestPairingExpires: ten minutes and one second after it opened, the
// pairing is gone and the state reads none again.
func TestPairingExpires(t *testing.T) {
	now := testNow
	api, _, token := pairingAPI(t, func() time.Time { return now })
	secret := pairingSecret(3)

	doRawRequest(t, api.Handler, startPairingRequest(t, "bench-fpp", `{"code":"`+pairingCodeFor(t, secret)+`"}`, token))

	now = testNow.Add(fppPairingTTL + time.Second)
	resp, body := doRawRequest(t, api.Handler, claimRequest(t, `{"secret":"`+secret+`"}`))
	if resp.StatusCode != http.StatusNotFound {
		t.Fatalf("expired claim status = %d, want 404; body: %s", resp.StatusCode, body)
	}
	if state := pairingState(t, api, token); state != fppPairingStateNone {
		t.Fatalf("state after expiry = %q, want none", state)
	}
}

// TestPairingReplacesTheEarlierPendingPairingForOneInstance: an operator
// who mistypes a code and starts again must not leave the first attempt
// claimable.
func TestPairingReplacesTheEarlierPendingPairingForOneInstance(t *testing.T) {
	api, _, token := pairingAPI(t, fixedClock(testNow))
	first, second := pairingSecret(4), pairingSecret(5)

	doRawRequest(t, api.Handler, startPairingRequest(t, "bench-fpp", `{"code":"`+pairingCodeFor(t, first)+`"}`, token))
	doRawRequest(t, api.Handler, startPairingRequest(t, "bench-fpp", `{"code":"`+pairingCodeFor(t, second)+`"}`, token))

	if resp, _ := doRawRequest(t, api.Handler, claimRequest(t, `{"secret":"`+first+`"}`)); resp.StatusCode != http.StatusNotFound {
		t.Fatalf("the replaced pairing was still claimable: status %d", resp.StatusCode)
	}
	if resp, body := doRawRequest(t, api.Handler, claimRequest(t, `{"secret":"`+second+`"}`)); resp.StatusCode != http.StatusOK {
		t.Fatalf("the current pairing was not claimable: status %d; body: %s", resp.StatusCode, body)
	}
}

// TestClaimWithAWrongSecretIsAGeneric404: the refusal must not say which
// part of the guess was wrong.
func TestClaimWithAWrongSecretIsAGeneric404(t *testing.T) {
	api, _, token := pairingAPI(t, fixedClock(testNow))
	doRawRequest(t, api.Handler, startPairingRequest(t, "bench-fpp", `{"code":"`+pairingCodeFor(t, pairingSecret(6))+`"}`, token))

	for _, body := range []string{
		`{"secret":"` + pairingSecret(7) + `"}`,
		`{"secret":"not-hex"}`,
		`{}`,
		`not json at all`,
	} {
		resp, got := doRawRequest(t, api.Handler, claimRequest(t, body))
		if resp.StatusCode != http.StatusNotFound {
			t.Fatalf("claim %q status = %d, want 404", body, resp.StatusCode)
		}
		if !strings.Contains(string(got), "no pairing is waiting for this plugin") {
			t.Fatalf("claim %q body = %s, want the one generic refusal", body, got)
		}
	}
}

// TestClaimRateLimitRefusesPastThirtyPerMinute, and the refusal names a
// wait rather than the pairing's own state.
func TestClaimRateLimitRefusesPastThirtyPerMinute(t *testing.T) {
	api, _, _ := pairingAPI(t, fixedClock(testNow))

	for i := 0; i < fppPairingClaimsPerMinute; i++ {
		if resp, _ := doRawRequest(t, api.Handler, claimRequest(t, `{"secret":"`+pairingSecret(byte(i))+`"}`)); resp.StatusCode != http.StatusNotFound {
			t.Fatalf("claim %d status = %d, want 404 before the limit", i, resp.StatusCode)
		}
	}
	resp, body := doRawRequest(t, api.Handler, claimRequest(t, `{"secret":"`+pairingSecret(99)+`"}`))
	if resp.StatusCode != http.StatusTooManyRequests {
		t.Fatalf("claim past the limit status = %d, want 429; body: %s", resp.StatusCode, body)
	}
	if resp.Header.Get("Retry-After") == "" {
		t.Error("a 429 from the claim route carried no Retry-After")
	}
}

// TestClaimRateLimitIsPerClientAddress: one noisy plugin must not lock
// out every other plugin on the network.
func TestClaimRateLimitIsPerClientAddress(t *testing.T) {
	api, _, _ := pairingAPI(t, fixedClock(testNow))

	for i := 0; i <= fppPairingClaimsPerMinute; i++ {
		doRawRequest(t, api.Handler, claimRequest(t, `{"secret":"`+pairingSecret(byte(i))+`"}`))
	}
	other := claimRequest(t, `{"secret":"`+pairingSecret(120)+`"}`)
	other.RemoteAddr = "192.0.2.11:5000"
	if resp, _ := doRawRequest(t, api.Handler, other); resp.StatusCode != http.StatusNotFound {
		t.Fatalf("a second client address was rate limited by the first: status %d", resp.StatusCode)
	}
}

// TestClaimBodyOverTheLimitIs413.
func TestClaimBodyOverTheLimitIs413(t *testing.T) {
	api, _, _ := pairingAPI(t, fixedClock(testNow))
	big := `{"secret":"` + strings.Repeat("a", maxFPPPairingRequestBodyBytes+64) + `"}`
	resp, body := doRawRequest(t, api.Handler, claimRequest(t, big))
	if resp.StatusCode != http.StatusRequestEntityTooLarge {
		t.Fatalf("oversized claim status = %d, want 413; body: %s", resp.StatusCode, body)
	}
}

// TestStartPairingRefusesAMalformedCode.
func TestStartPairingRefusesAMalformedCode(t *testing.T) {
	api, _, token := pairingAPI(t, fixedClock(testNow))
	for _, code := range []string{"", "ABCD1234", "ABCD-123", "ABCI-LOUV", "ABCD-EFGH-IJKL"} {
		resp, body := doRawRequest(t, api.Handler, startPairingRequest(t, "bench-fpp", `{"code":"`+code+`"}`, token))
		if resp.StatusCode != http.StatusBadRequest {
			t.Fatalf("code %q status = %d, want 400; body: %s", code, resp.StatusCode, body)
		}
	}
}

// TestStartPairingReusesTheInstancePrincipal: pairing twice must not
// leave two principals for one FPP host.
func TestStartPairingReusesTheInstancePrincipal(t *testing.T) {
	api, setup, token := pairingAPI(t, fixedClock(testNow))

	doRawRequest(t, api.Handler, startPairingRequest(t, "bench-fpp", `{"code":"`+pairingCodeFor(t, pairingSecret(8))+`"}`, token))
	doRawRequest(t, api.Handler, startPairingRequest(t, "bench-fpp", `{"code":"`+pairingCodeFor(t, pairingSecret(9))+`"}`, token))

	principals, err := setup.svc.ListPrincipals(t.Context())
	if err != nil {
		t.Fatalf("list principals: %v", err)
	}
	count := 0
	for _, p := range principals {
		if p.Name == "fpp-plugin-bench-fpp" {
			count++
		}
	}
	if count != 1 {
		t.Fatalf("found %d principals named fpp-plugin-bench-fpp, want exactly 1", count)
	}
}

// TestPairingStateReadsPairedAfterAClaim.
func TestPairingStateReadsPairedAfterAClaim(t *testing.T) {
	api, _, token := pairingAPI(t, fixedClock(testNow))
	secret := pairingSecret(10)

	if state := pairingState(t, api, token); state != fppPairingStateNone {
		t.Fatalf("initial state = %q, want none", state)
	}
	doRawRequest(t, api.Handler, startPairingRequest(t, "bench-fpp", `{"code":"`+pairingCodeFor(t, secret)+`"}`, token))
	if state := pairingState(t, api, token); state != fppPairingStateWaiting {
		t.Fatalf("state after starting = %q, want waiting", state)
	}
	doRawRequest(t, api.Handler, claimRequest(t, `{"secret":"`+secret+`"}`))
	if state := pairingState(t, api, token); state != fppPairingStatePaired {
		t.Fatalf("state after claiming = %q, want paired", state)
	}
}

// TestStartPairingRequiresPrincipalWrite: an operator token holds
// fpp:command but not principal:write, and opening a pairing mints a
// credential.
func TestStartPairingRequiresPrincipalWrite(t *testing.T) {
	setup := newFPPCommandTestSetup(t, fixedClock(testNow))
	setup.fppLister.views = []FPPInstanceView{{InstanceID: "bench-fpp", Endpoint: "http://fpp.invalid"}}
	api := New(setup.deps(), Options{Clock: fixedClock(testNow), Logger: testLogger()})
	operator := mustCreatePrincipal(t, setup.svc, "operator-1", identity.RoleOperator)
	opToken := mustIssueToken(t, setup.svc, operator.ID)

	resp, body := doRawRequest(t, api.Handler,
		startPairingRequest(t, "bench-fpp", `{"code":"`+pairingCodeFor(t, pairingSecret(11))+`"}`, opToken))
	if resp.StatusCode != http.StatusForbidden {
		t.Fatalf("operator start pairing status = %d, want 403; body: %s", resp.StatusCode, body)
	}
}

// TestStartPairingAcceptsALowerCaseCode: an operator typing the code by
// hand should not be refused over letter case.
func TestStartPairingAcceptsALowerCaseCode(t *testing.T) {
	api, _, token := pairingAPI(t, fixedClock(testNow))
	secret := pairingSecret(12)
	code := pairingCodeFor(t, secret)

	resp, body := doRawRequest(t, api.Handler,
		startPairingRequest(t, "bench-fpp", `{"code":"`+strings.ToLower(code)+`"}`, token))
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("lower-case code status = %d, want 200; body: %s", resp.StatusCode, body)
	}
	if !strings.Contains(string(body), `"code":"`+code+`"`) {
		t.Fatalf("response did not normalize the code to upper case: %s", body)
	}
	if resp, _ := doRawRequest(t, api.Handler, claimRequest(t, `{"secret":"`+secret+`"}`)); resp.StatusCode != http.StatusOK {
		t.Fatalf("a pairing started with a lower-case code was not claimable: status %d", resp.StatusCode)
	}
}

func pairingState(t *testing.T, api *API, token string) string {
	t.Helper()
	req := httptest.NewRequest(http.MethodGet, "/api/v1/fpp/bench-fpp/pairing", nil)
	req.Header.Set("Authorization", "Bearer "+token)
	_, body := doRawRequest(t, api.Handler, req)
	var got struct {
		State string `json:"state"`
	}
	if err := json.Unmarshal(body, &got); err != nil {
		t.Fatalf("decode pairing state: %v; body: %s", err, body)
	}
	return got.State
}
