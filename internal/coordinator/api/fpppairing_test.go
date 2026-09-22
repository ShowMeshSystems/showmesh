package api

import (
	"crypto/sha256"
	"encoding/base32"
	"encoding/hex"
	"encoding/json"
	"fmt"
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
	for _, code := range []string{"", "ABCD123", "ABCI-LOUV", "ABCD-EFGH-IJKL"} {
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

// TestStartPairingAcceptsEveryWrittenFormOfOneCode: the code is read off
// a screen and typed by hand, so case, the dash and stray spaces must not
// decide whether pairing works.
func TestStartPairingAcceptsEveryWrittenFormOfOneCode(t *testing.T) {
	seed := byte(12)
	for _, form := range []func(code string) string{
		func(c string) string { return c },
		strings.ToLower,
		func(c string) string { return strings.ReplaceAll(c, "-", "") },
		func(c string) string { return strings.ToLower(strings.ReplaceAll(c, "-", "")) },
		func(c string) string { return " " + strings.ReplaceAll(c, "-", " ") + " " },
	} {
		api, _, token := pairingAPI(t, fixedClock(testNow))
		secret := pairingSecret(seed)
		seed++
		code := pairingCodeFor(t, secret)

		typed := form(code)
		resp, body := doRawRequest(t, api.Handler,
			startPairingRequest(t, "bench-fpp", `{"code":"`+typed+`"}`, token))
		if resp.StatusCode != http.StatusOK {
			t.Fatalf("code %q status = %d, want 200; body: %s", typed, resp.StatusCode, body)
		}
		if !strings.Contains(string(body), `"code":"`+code+`"`) {
			t.Fatalf("code %q was not normalized to %s: %s", typed, code, body)
		}
		if resp, _ := doRawRequest(t, api.Handler, claimRequest(t, `{"secret":"`+secret+`"}`)); resp.StatusCode != http.StatusOK {
			t.Fatalf("a pairing started as %q was not claimable: status %d", typed, resp.StatusCode)
		}
	}
}

// TestReplacingAPairingRevokesTheUnclaimedToken: the first pairing's
// token was minted and never handed out, so nothing must still accept it.
func TestReplacingAPairingRevokesTheUnclaimedToken(t *testing.T) {
	api, setup, token := pairingAPI(t, fixedClock(testNow))

	doRawRequest(t, api.Handler, startPairingRequest(t, "bench-fpp", `{"code":"`+pairingCodeFor(t, pairingSecret(40))+`"}`, token))
	first := onlyPairingTokenID(t, setup)

	doRawRequest(t, api.Handler, startPairingRequest(t, "bench-fpp", `{"code":"`+pairingCodeFor(t, pairingSecret(41))+`"}`, token))

	if live := livePairingTokenIDs(t, setup); len(live) != 1 || live[0] == first {
		t.Fatalf("live pairing tokens = %v, want only the replacement (not %s)", live, first)
	}
}

// TestAnExpiredPairingRevokesItsUnclaimedToken: the sweep every pairing
// route runs must clear a pairing nobody finished, not only forget it.
func TestAnExpiredPairingRevokesItsUnclaimedToken(t *testing.T) {
	now := testNow
	api, setup, token := pairingAPI(t, func() time.Time { return now })

	doRawRequest(t, api.Handler, startPairingRequest(t, "bench-fpp", `{"code":"`+pairingCodeFor(t, pairingSecret(42))+`"}`, token))
	if len(livePairingTokenIDs(t, setup)) != 1 {
		t.Fatal("the pairing did not mint a token")
	}

	now = testNow.Add(fppPairingTTL + time.Second)
	doRawRequest(t, api.Handler, claimRequest(t, `{"secret":"`+pairingSecret(43)+`"}`))

	if live := livePairingTokenIDs(t, setup); len(live) != 0 {
		t.Fatalf("live pairing tokens after expiry = %v, want none", live)
	}
}

// TestAPairingTokenCarriesItsOwnExpiry bounds the leak if a revoke ever
// fails: the credential dies on its own.
func TestAPairingTokenCarriesItsOwnExpiry(t *testing.T) {
	api, setup, token := pairingAPI(t, fixedClock(testNow))
	doRawRequest(t, api.Handler, startPairingRequest(t, "bench-fpp", `{"code":"`+pairingCodeFor(t, pairingSecret(44))+`"}`, token))

	tokens := pairingTokens(t, setup)
	if len(tokens) != 1 {
		t.Fatalf("got %d pairing tokens, want 1", len(tokens))
	}
	if tokens[0].ExpiresAt == nil {
		t.Fatal("the pairing token never expires; an unclaimed pairing would leave a live credential behind")
	}
	if want := testNow.Add(fppPairingTTL + fppPairingTokenMargin); !tokens[0].ExpiresAt.Equal(want) {
		t.Fatalf("token expiry = %v, want %v", tokens[0].ExpiresAt, want)
	}
}

// TestShutdownRevokesEveryUnclaimedPairingToken.
func TestShutdownRevokesEveryUnclaimedPairingToken(t *testing.T) {
	api, setup, token := pairingAPI(t, fixedClock(testNow))
	doRawRequest(t, api.Handler, startPairingRequest(t, "bench-fpp", `{"code":"`+pairingCodeFor(t, pairingSecret(45))+`"}`, token))

	api.RevokeUnclaimedFPPPairings(t.Context())

	if live := livePairingTokenIDs(t, setup); len(live) != 0 {
		t.Fatalf("live pairing tokens after shutdown = %v, want none", live)
	}
}

// TestAClaimedPairingKeepsItsToken: the revoke sweeps must never touch a
// credential a plugin is actually using.
func TestAClaimedPairingKeepsItsToken(t *testing.T) {
	api, setup, token := pairingAPI(t, fixedClock(testNow))
	secret := pairingSecret(46)
	doRawRequest(t, api.Handler, startPairingRequest(t, "bench-fpp", `{"code":"`+pairingCodeFor(t, secret)+`"}`, token))
	_, body := doRawRequest(t, api.Handler, claimRequest(t, `{"secret":"`+secret+`"}`))

	var claimed pairingClaimForTest
	if err := json.Unmarshal(body, &claimed); err != nil {
		t.Fatalf("decode claim: %v", err)
	}
	api.RevokeUnclaimedFPPPairings(t.Context())

	if _, err := setup.svc.AuthenticateToken(t.Context(), claimed.Token); err != nil {
		t.Fatalf("the claimed token stopped working: %v", err)
	}
}

// TestStartPairingRefusesADisabledOrRepurposedPrincipal: a name match is
// not enough to mint a credential against.
func TestStartPairingRefusesADisabledOrRepurposedPrincipal(t *testing.T) {
	for _, tc := range []struct {
		name    string
		prepare func(t *testing.T, setup *fppCommandTestSetup, id string)
	}{
		{"disabled", func(t *testing.T, setup *fppCommandTestSetup, id string) {
			if _, err := setup.svc.SetDisabled(t.Context(), id, true); err != nil {
				t.Fatalf("disable: %v", err)
			}
		}},
		{"role changed", func(t *testing.T, setup *fppCommandTestSetup, id string) {
			if _, err := setup.svc.SetRole(t.Context(), id, identity.RoleViewer); err != nil {
				t.Fatalf("set role: %v", err)
			}
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			api, setup, token := pairingAPI(t, fixedClock(testNow))
			existing := mustCreatePrincipal(t, setup.svc, "fpp-plugin-bench-fpp", identity.RoleScheduler)
			tc.prepare(t, setup, existing.ID)

			resp, body := doRawRequest(t, api.Handler,
				startPairingRequest(t, "bench-fpp", `{"code":"`+pairingCodeFor(t, pairingSecret(47))+`"}`, token))
			if resp.StatusCode != http.StatusConflict {
				t.Fatalf("status = %d, want 409; body: %s", resp.StatusCode, body)
			}
			if len(livePairingTokenIDs(t, setup)) != 0 {
				t.Fatal("a refused pairing still minted a token")
			}
		})
	}
}

// TestClaimLimiterForgetsASourceThatWentQuiet: this map is keyed by an
// address anyone on the network can supply, so it must not grow forever.
func TestClaimLimiterForgetsASourceThatWentQuiet(t *testing.T) {
	l := newFPPPairingClaimLimiter()
	now := testNow

	for i := 0; i < 50; i++ {
		l.allow(fmt.Sprintf("198.51.100.%d", i), now)
	}
	if len(l.hits) != 50 {
		t.Fatalf("tracked %d sources, want 50", len(l.hits))
	}

	l.allow("203.0.113.1", now.Add(2*time.Minute))
	if len(l.hits) != 1 {
		t.Fatalf("tracked %d sources after the window passed, want only the current one", len(l.hits))
	}
}

// pairingTokens returns every token the pairing principal currently
// holds, revoked ones excluded.
func pairingTokens(t *testing.T, setup *fppCommandTestSetup) []identity.TokenInfo {
	t.Helper()
	principals, err := setup.svc.ListPrincipals(t.Context())
	if err != nil {
		t.Fatalf("list principals: %v", err)
	}
	for _, p := range principals {
		if p.Name != "fpp-plugin-bench-fpp" {
			continue
		}
		tokens, err := setup.svc.ListTokens(t.Context(), p.ID)
		if err != nil {
			t.Fatalf("list tokens: %v", err)
		}
		return tokens
	}
	return nil
}

func livePairingTokenIDs(t *testing.T, setup *fppCommandTestSetup) []string {
	t.Helper()
	var out []string
	for _, tok := range pairingTokens(t, setup) {
		out = append(out, tok.ID)
	}
	return out
}

func onlyPairingTokenID(t *testing.T, setup *fppCommandTestSetup) string {
	t.Helper()
	ids := livePairingTokenIDs(t, setup)
	if len(ids) != 1 {
		t.Fatalf("got %d live pairing tokens, want 1", len(ids))
	}
	return ids[0]
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
