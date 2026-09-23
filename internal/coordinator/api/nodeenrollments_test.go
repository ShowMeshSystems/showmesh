package api

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/showmeshsystems/showmesh/internal/coordinator/enrollment"
	"github.com/showmeshsystems/showmesh/internal/coordinator/identity"
	"github.com/showmeshsystems/showmesh/internal/coordinator/store"
)

type movableClock struct {
	mu sync.Mutex
	t  time.Time
}

func (c *movableClock) now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.t
}

func (c *movableClock) advance(d time.Duration) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.t = c.t.Add(d)
}

const enrollTestPublicKey = "dGVzdC1wdWJsaWMta2V5LXRlc3QtcHVibGljLWtleSE="

type enrollHarness struct {
	api       *API
	st        *store.Store
	ids       identity.Service
	clock     *movableClock
	admin     map[string]string
	brokerDir string
}

// newBrokerConfigDir builds a broker config directory the way
// generate-credentials.sh leaves one: the committed acl.conf, a passwd
// holding the fixed roles, and the migration marker.
func newBrokerConfigDir(t *testing.T, extraPasswd string) string {
	t.Helper()
	dir := t.TempDir()
	base, err := os.ReadFile(filepath.Join("..", "..", "..", "deploy", "mosquitto", "acl.conf"))
	if err != nil {
		t.Fatal(err)
	}
	mustWrite := func(name string, b []byte, mode os.FileMode) {
		if err := os.WriteFile(filepath.Join(dir, name), b, mode); err != nil {
			t.Fatal(err)
		}
	}
	mustWrite("acl.conf", base, 0o644)
	mustWrite("passwd", []byte("coordinator:$7$101$a$b\nfpp:$7$101$a$b\nhealthcheck:$7$101$a$b\n"+extraPasswd), 0o640)
	mustWrite(".acl-explicit-agents-v1", nil, 0o600)
	return dir
}

func newEnrollHarness(t *testing.T, cfg enrollment.Config, brokerDir string) *enrollHarness {
	t.Helper()
	clock := &movableClock{t: testNow}
	svc, st, _ := newTestIdentityServiceWithStore(t, clock.now)
	admin := mustCreatePrincipal(t, svc, "admin-1", identity.RoleAdmin)
	token := mustIssueToken(t, svc, admin.ID)
	deps := assetsTestDeps(t, svc, st)
	if cfg.CoordinatorPublicKey == "" {
		cfg.CoordinatorPublicKey = enrollTestPublicKey
	}
	deps.NodeEnrollment = enrollment.NewService(st, svc, enrollment.NewBrokerFiles(brokerDir), cfg)
	api := New(deps, Options{Clock: clock.now, Logger: testLogger()})
	return &enrollHarness{api: api, st: st, ids: svc, clock: clock, admin: map[string]string{"Authorization": "Bearer " + token}, brokerDir: brokerDir}
}

func (h *enrollHarness) do(t *testing.T, method, target, body string, headers map[string]string, host string) (*http.Response, []byte) {
	t.Helper()
	req := newJSONRequest(t, method, target, body, headers)
	if host != "" {
		req.Host = host
	}
	req.RemoteAddr = "192.0.2.10:40000"
	return doRawRequest(t, h.api.Handler, req)
}

type mintedForTest struct {
	ID, NodeID, Code, ExpiresAt, CoordinatorURL string
	Reenroll                                    bool
}

func (h *enrollHarness) mint(t *testing.T, body string, wantStatus int) (mintedForTest, []byte) {
	t.Helper()
	resp, raw := h.do(t, http.MethodPost, "/api/v1/node-enrollments", body, h.admin, "coord.example:8080")
	if resp.StatusCode != wantStatus {
		t.Fatalf("mint %s: status = %d, want %d; body: %s", body, resp.StatusCode, wantStatus, raw)
	}
	var m mintedForTest
	_ = json.Unmarshal(raw, &m)
	return m, raw
}

func (h *enrollHarness) redeem(t *testing.T, code string) (*http.Response, []byte) {
	t.Helper()
	return h.do(t, http.MethodPost, "/api/v1/node-enrollments/redeem", `{"code":"`+code+`","hostname":"n1","arch":"arm64"}`, nil, "coord.example:8080")
}

type redeemedForTest struct {
	NodeID, BrokerURL, MQTTUsername, MQTTPassword, APIToken, CoordinatorURL, CoordinatorPublicKey string
}

func decodeRedeemed(t *testing.T, raw []byte) redeemedForTest {
	t.Helper()
	var r redeemedForTest
	if err := json.Unmarshal(raw, &r); err != nil {
		t.Fatalf("decode redeem body: %v\n%s", err, raw)
	}
	return r
}

func problemDetail(t *testing.T, raw []byte) string {
	t.Helper()
	var p struct{ Detail string }
	_ = json.Unmarshal(raw, &p)
	return p.Detail
}

func (h *enrollHarness) listStates(t *testing.T) map[string]string {
	t.Helper()
	resp, raw := h.do(t, http.MethodGet, "/api/v1/node-enrollments", "", h.admin, "")
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("list: status = %d; body: %s", resp.StatusCode, raw)
	}
	var body struct {
		Enrollments []struct{ ID, State, CreatedBy string }
	}
	if err := json.Unmarshal(raw, &body); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(raw), `"code"`) {
		t.Fatalf("listing carries a code field: %s", raw)
	}
	out := map[string]string{}
	for _, e := range body.Enrollments {
		out[e.ID] = e.State
	}
	return out
}

func TestRedeemNodeEnrollmentReturnsEveryFieldAndMarksUsed(t *testing.T) {
	h := newEnrollHarness(t, enrollment.Config{}, newBrokerConfigDir(t, ""))
	minted, _ := h.mint(t, `{"nodeId":"render-01"}`, http.StatusCreated)
	if !regexp.MustCompile(`^[0-9A-HJKMNP-TV-Z]{4}-[0-9A-HJKMNP-TV-Z]{4}$`).MatchString(minted.Code) {
		t.Fatalf("code %q is not XXXX-XXXX in Crockford base32", minted.Code)
	}
	if minted.ExpiresAt != formatTime(testNow.Add(15*time.Minute)) || minted.CoordinatorURL != "" {
		t.Fatalf("mint response = %+v", minted)
	}

	resp, raw := h.redeem(t, strings.ToLower(strings.ReplaceAll(minted.Code, "-", "")))
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("redeem: status = %d; body: %s", resp.StatusCode, raw)
	}
	got := decodeRedeemed(t, raw)
	want := redeemedForTest{
		NodeID: "render-01", BrokerURL: "tcp://coord.example:1883", MQTTUsername: "render-01",
		MQTTPassword: got.MQTTPassword, APIToken: got.APIToken,
		CoordinatorURL: "http://coord.example:8080", CoordinatorPublicKey: enrollTestPublicKey,
	}
	if got != want || got.MQTTPassword == "" || !strings.HasPrefix(got.APIToken, identity.TokenPrefix) {
		t.Fatalf("redeem body = %+v, want %+v with a password and a token", got, want)
	}

	passwd, _ := os.ReadFile(filepath.Join(h.brokerDir, "passwd"))
	if !strings.Contains(string(passwd), "\nrender-01:$7$101$") {
		t.Fatalf("passwd has no render-01 entry:\n%s", passwd)
	}
	acl, _ := os.ReadFile(filepath.Join(h.brokerDir, "acl.generated.conf"))
	if !strings.Contains(string(acl), "user render-01\ntopic write showmesh/nodes/render-01/hello\n") {
		t.Fatalf("acl.generated.conf has no render-01 block:\n%s", acl)
	}

	auth, err := h.ids.AuthenticateToken(context.Background(), got.APIToken)
	if err != nil || auth.Principal.Role != identity.RoleNode || auth.Principal.Name != "render-01 agent" || auth.Principal.Kind != identity.KindMachine {
		t.Fatalf("token authenticates as %+v, %v; want the machine principal \"render-01 agent\" with role node", auth.Principal, err)
	}
	if states := h.listStates(t); states[minted.ID] != enrollment.StateRedeemed {
		t.Fatalf("state after redeem = %q, want redeemed", states[minted.ID])
	}

	entries, err := h.ids.ListAudit(context.Background(), 0, 100)
	if err != nil {
		t.Fatal(err)
	}
	var sawCreate, sawRedeem bool
	for _, e := range entries {
		switch e.Action {
		case enrollment.AuditActionCreate:
			sawCreate = e.PrincipalName == "admin-1" && e.Target == "render-01"
		case enrollment.AuditActionRedeem:
			sawRedeem = e.PrincipalName == "admin-1" && e.Target == "render-01" && e.CredentialID == minted.ID
		}
	}
	if !sawCreate || !sawRedeem {
		t.Fatalf("audit create=%v redeem=%v, want both attributed to admin-1; entries: %+v", sawCreate, sawRedeem, entries)
	}
}

func TestRedeemExpiredCodeIs410(t *testing.T) {
	h := newEnrollHarness(t, enrollment.Config{}, newBrokerConfigDir(t, ""))
	minted, _ := h.mint(t, `{"nodeId":"render-01","expiresInSeconds":60}`, http.StatusCreated)
	h.clock.advance(61 * time.Second)
	resp, raw := h.redeem(t, minted.Code)
	if resp.StatusCode != http.StatusGone || !strings.Contains(problemDetail(t, raw), "expired") {
		t.Fatalf("expired redeem: status = %d, body %s; want 410 saying it expired", resp.StatusCode, raw)
	}
	passwd, _ := os.ReadFile(filepath.Join(h.brokerDir, "passwd"))
	if strings.Contains(string(passwd), "render-01") {
		t.Fatal("an expired redeem wrote a broker login")
	}
}

func TestRedeemReusedCodeIs410(t *testing.T) {
	h := newEnrollHarness(t, enrollment.Config{}, newBrokerConfigDir(t, ""))
	minted, _ := h.mint(t, `{"nodeId":"render-01"}`, http.StatusCreated)
	if resp, raw := h.redeem(t, minted.Code); resp.StatusCode != http.StatusOK {
		t.Fatalf("first redeem: %d %s", resp.StatusCode, raw)
	}
	resp, raw := h.redeem(t, minted.Code)
	if resp.StatusCode != http.StatusGone || !strings.Contains(problemDetail(t, raw), "already been used") {
		t.Fatalf("second redeem: status = %d, body %s; want 410 saying it was used", resp.StatusCode, raw)
	}
}

func TestRedeemWrongCodeIs404AndSixthFailureInAMinuteIs429(t *testing.T) {
	h := newEnrollHarness(t, enrollment.Config{}, newBrokerConfigDir(t, ""))
	minted, _ := h.mint(t, `{"nodeId":"render-01"}`, http.StatusCreated)
	for i := 0; i < 5; i++ {
		resp, raw := h.redeem(t, "ZZZZ-ZZZZ")
		if resp.StatusCode != http.StatusNotFound {
			t.Fatalf("wrong code %d: status = %d; body %s", i+1, resp.StatusCode, raw)
		}
		h.clock.advance(time.Second)
	}
	resp, raw := h.redeem(t, minted.Code)
	if resp.StatusCode != http.StatusTooManyRequests || resp.Header.Get("Retry-After") == "" {
		t.Fatalf("sixth attempt: status = %d, Retry-After %q; body %s; want 429 with Retry-After", resp.StatusCode, resp.Header.Get("Retry-After"), raw)
	}
	h.clock.advance(time.Minute)
	if resp, raw := h.redeem(t, minted.Code); resp.StatusCode != http.StatusOK {
		t.Fatalf("after the window: status = %d; body %s; the refused attempt must leave the code valid", resp.StatusCode, raw)
	}
}

func TestSuccessfulRedeemsDoNotCountTowardTheLimit(t *testing.T) {
	h := newEnrollHarness(t, enrollment.Config{}, newBrokerConfigDir(t, ""))
	for i := 0; i < 7; i++ {
		minted, _ := h.mint(t, `{"nodeId":"node-`+string(rune('a'+i))+`"}`, http.StatusCreated)
		if resp, raw := h.redeem(t, minted.Code); resp.StatusCode != http.StatusOK {
			t.Fatalf("redeem %d: status = %d; body %s", i, resp.StatusCode, raw)
		}
	}
}

func TestMintForEnrolledNodeWithoutReenrollIs409(t *testing.T) {
	h := newEnrollHarness(t, enrollment.Config{}, newBrokerConfigDir(t, "script-node:$7$101$a$b\n"))
	minted, _ := h.mint(t, `{"nodeId":"render-01"}`, http.StatusCreated)
	if resp, raw := h.redeem(t, minted.Code); resp.StatusCode != http.StatusOK {
		t.Fatalf("redeem: %d %s", resp.StatusCode, raw)
	}
	_, raw := h.mint(t, `{"nodeId":"render-01"}`, http.StatusConflict)
	if !strings.Contains(problemDetail(t, raw), "already enrolled") {
		t.Fatalf("409 detail = %q", problemDetail(t, raw))
	}
	h.mint(t, `{"nodeId":"script-node"}`, http.StatusConflict)
	h.mint(t, `{"nodeId":"script-node","reenroll":true}`, http.StatusCreated)
}

func TestMintRefusesReservedAndMalformedNodeIDs(t *testing.T) {
	h := newEnrollHarness(t, enrollment.Config{}, newBrokerConfigDir(t, ""))
	for _, id := range []string{"coordinator", "fpp", "healthcheck", "observer", "Bad_ID", ""} {
		h.mint(t, `{"nodeId":"`+id+`"}`, http.StatusBadRequest)
	}
	h.mint(t, `{"nodeId":"render-01","expiresInSeconds":59}`, http.StatusBadRequest)
	h.mint(t, `{"nodeId":"render-01","expiresInSeconds":86401}`, http.StatusBadRequest)
}

func TestReenrollRotatesPasswordAndRevokesOldToken(t *testing.T) {
	h := newEnrollHarness(t, enrollment.Config{}, newBrokerConfigDir(t, ""))
	first, _ := h.mint(t, `{"nodeId":"render-01"}`, http.StatusCreated)
	_, raw := h.redeem(t, first.Code)
	old := decodeRedeemed(t, raw)
	oldPasswd, _ := os.ReadFile(filepath.Join(h.brokerDir, "passwd"))

	second, _ := h.mint(t, `{"nodeId":"render-01","reenroll":true}`, http.StatusCreated)
	resp, raw := h.redeem(t, second.Code)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("re-enroll redeem: %d %s", resp.StatusCode, raw)
	}
	fresh := decodeRedeemed(t, raw)
	if fresh.MQTTPassword == old.MQTTPassword || fresh.APIToken == old.APIToken {
		t.Fatal("re-enrollment returned the same password or token")
	}
	newPasswd, _ := os.ReadFile(filepath.Join(h.brokerDir, "passwd"))
	if string(newPasswd) == string(oldPasswd) || strings.Count(string(newPasswd), "render-01:") != 1 {
		t.Fatalf("passwd not rotated in place:\nold %s\nnew %s", oldPasswd, newPasswd)
	}
	if _, err := h.ids.AuthenticateToken(context.Background(), old.APIToken); err == nil {
		t.Fatal("the previous enrollment's token still authenticates")
	}
	if _, err := h.ids.AuthenticateToken(context.Background(), fresh.APIToken); err != nil {
		t.Fatalf("the new token does not authenticate: %v", err)
	}
}

func TestMintingANewCodeCancelsTheEarlierUnusedOne(t *testing.T) {
	h := newEnrollHarness(t, enrollment.Config{}, newBrokerConfigDir(t, ""))
	first, _ := h.mint(t, `{"nodeId":"render-01"}`, http.StatusCreated)
	second, _ := h.mint(t, `{"nodeId":"render-01"}`, http.StatusCreated)
	states := h.listStates(t)
	if states[first.ID] != enrollment.StateCancelled || states[second.ID] != enrollment.StatePending {
		t.Fatalf("states = %v, want first cancelled and second pending", states)
	}
	if resp, _ := h.redeem(t, first.Code); resp.StatusCode != http.StatusGone {
		t.Fatalf("redeeming the cancelled code: status = %d, want 410", resp.StatusCode)
	}
}

func TestCancelPendingCodeThenCancelAgainIs409(t *testing.T) {
	h := newEnrollHarness(t, enrollment.Config{}, newBrokerConfigDir(t, ""))
	minted, _ := h.mint(t, `{"nodeId":"render-01"}`, http.StatusCreated)
	resp, raw := h.do(t, http.MethodDelete, "/api/v1/node-enrollments/"+minted.ID, "", h.admin, "")
	if resp.StatusCode != http.StatusOK || !strings.Contains(string(raw), `"state":"cancelled"`) {
		t.Fatalf("cancel: %d %s", resp.StatusCode, raw)
	}
	if resp, raw := h.do(t, http.MethodDelete, "/api/v1/node-enrollments/"+minted.ID, "", h.admin, ""); resp.StatusCode != http.StatusConflict {
		t.Fatalf("second cancel: %d %s, want 409", resp.StatusCode, raw)
	}
	if resp, _ := h.do(t, http.MethodDelete, "/api/v1/node-enrollments/no-such-id", "", h.admin, ""); resp.StatusCode != http.StatusNotFound {
		t.Fatalf("cancel unknown id: %d, want 404", resp.StatusCode)
	}
}

func TestNodeRoleTokenCanRegisterFPPConnectUploadAndCannotWriteConfig(t *testing.T) {
	h := newEnrollHarness(t, enrollment.Config{}, newBrokerConfigDir(t, ""))
	mustDeclareNode(t, h.st, "render-01")
	mustPutShow(t, h.api, strings.TrimPrefix(h.admin["Authorization"], "Bearer "), "halloween-2026", `{"name":"Halloween 2026","notes":""}`)
	minted, _ := h.mint(t, `{"nodeId":"render-01"}`, http.StatusCreated)
	_, raw := h.redeem(t, minted.Code)
	node := map[string]string{"Authorization": "Bearer " + decodeRedeemed(t, raw).APIToken}

	resp, body := doAssetUpload(t, h.api.Handler, validAssetFields(), "Thriller.fseq", []byte("fseq-bytes"), node)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("FPP Connect registration with the node token: status = %d; body %s", resp.StatusCode, body)
	}
	resp, body = h.do(t, http.MethodPut, "/api/v1/config/show.mode", `{"mode":"show"}`, node, "")
	if resp.StatusCode != http.StatusForbidden {
		t.Fatalf("config write with the node token: status = %d, want 403; body %s", resp.StatusCode, body)
	}
	if resp, body := h.do(t, http.MethodPost, "/api/v1/node-enrollments", `{"nodeId":"x1"}`, node, ""); resp.StatusCode != http.StatusForbidden {
		t.Fatalf("mint with the node token: status = %d, want 403; body %s", resp.StatusCode, body)
	}
}

func TestMintIsRefusedWhenTheCoordinatorCannotManageBrokerLogins(t *testing.T) {
	h := newEnrollHarness(t, enrollment.Config{}, "")
	_, raw := h.mint(t, `{"nodeId":"render-01"}`, http.StatusServiceUnavailable)
	if !strings.Contains(problemDetail(t, raw), "SHOWMESH_BROKER_CONFIG_DIR") {
		t.Fatalf("detail %q does not say what to set", problemDetail(t, raw))
	}
}

func TestRedeemLeavesCodePendingWhenBrokerFilesCannotBeWritten(t *testing.T) {
	dir := newBrokerConfigDir(t, "")
	h := newEnrollHarness(t, enrollment.Config{}, dir)
	minted, _ := h.mint(t, `{"nodeId":"render-01"}`, http.StatusCreated)
	if err := os.Chmod(dir, 0o555); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(dir, 0o755) })
	if os.Geteuid() == 0 {
		t.Skip("root ignores directory permissions")
	}
	resp, raw := h.redeem(t, minted.Code)
	if resp.StatusCode != http.StatusServiceUnavailable || !strings.Contains(problemDetail(t, raw), "still valid") {
		t.Fatalf("redeem with a read-only broker dir: %d %s; want 503 saying the code is still valid", resp.StatusCode, raw)
	}
	if states := h.listStates(t); states[minted.ID] != enrollment.StatePending {
		t.Fatalf("state = %q, want pending", states[minted.ID])
	}
	principals, _ := h.ids.ListPrincipals(context.Background())
	for _, p := range principals {
		if p.Name == "render-01 agent" {
			t.Fatal("a failed redeem left a principal behind")
		}
	}
}

func TestExternalBrokerModeReturnsSharedLoginAndWritesNoFiles(t *testing.T) {
	h := newEnrollHarness(t, enrollment.Config{
		BrokerMode: enrollment.BrokerModeExternal, NodeBrokerURL: "tcp://broker.lan:1883",
		PublicURL: "http://showmesh.lan:8080/", NodeMQTTUsername: "shared", NodeMQTTPassword: "pw",
	}, "")
	minted, _ := h.mint(t, `{"nodeId":"render-01"}`, http.StatusCreated)
	if minted.CoordinatorURL != "http://showmesh.lan:8080" {
		t.Fatalf("mint coordinatorUrl = %q", minted.CoordinatorURL)
	}
	_, raw := h.redeem(t, minted.Code)
	got := decodeRedeemed(t, raw)
	if got.MQTTUsername != "shared" || got.MQTTPassword != "pw" || got.BrokerURL != "tcp://broker.lan:1883" || got.CoordinatorURL != "http://showmesh.lan:8080" {
		t.Fatalf("external redeem = %+v", got)
	}

	anon := newEnrollHarness(t, enrollment.Config{BrokerMode: enrollment.BrokerModeExternal}, "")
	minted, _ = anon.mint(t, `{"nodeId":"render-02"}`, http.StatusCreated)
	_, raw = anon.redeem(t, minted.Code)
	if !strings.Contains(string(raw), `"mqttUsername":""`) || !strings.Contains(string(raw), `"mqttPassword":""`) {
		t.Fatalf("anonymous external broker must return empty strings: %s", raw)
	}
}

func TestRedeemMalformedCodeIs400(t *testing.T) {
	h := newEnrollHarness(t, enrollment.Config{}, newBrokerConfigDir(t, ""))
	if resp, raw := h.redeem(t, "not-a-code"); resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("malformed code: %d %s", resp.StatusCode, raw)
	}
	resp, raw := h.do(t, http.MethodPost, "/api/v1/node-enrollments/redeem", `{`, nil, "")
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("malformed body: %d %s", resp.StatusCode, raw)
	}
}

// TestRedeemIsTheOnlyUnauthenticatedWrite reads api.go's route table: every
// non-GET route goes through writeGuard, except the two sign-in routes,
// which run loginCSRFGuard, and the one named exception, redeem.
func TestRedeemIsTheOnlyUnauthenticatedWrite(t *testing.T) {
	src, err := os.ReadFile("api.go")
	if err != nil {
		t.Fatal(err)
	}
	re := regexp.MustCompile(`mux\.HandleFunc\("([A-Z]+) ([^"]*)",\s*([^\n]*)`)
	matches := re.FindAllStringSubmatch(string(src), -1)
	if len(matches) < 50 {
		t.Fatalf("found only %d routes in api.go; the pattern no longer matches the route table", len(matches))
	}
	signIn := map[string]bool{"POST /api/v1/session": true, "POST /api/v1/bootstrap": true}
	var unguarded []string
	for _, m := range matches {
		method, path, handler := m[1], m[2], m[3]
		if method == http.MethodGet {
			continue
		}
		route := method + " " + path
		switch {
		case strings.HasPrefix(handler, "h.writeGuard("):
		case signIn[route] && strings.HasPrefix(handler, "h.loginCSRFGuard("):
		default:
			unguarded = append(unguarded, route)
		}
	}
	if len(unguarded) != 1 || unguarded[0] != "POST /api/v1/node-enrollments/redeem" {
		t.Fatalf("unauthenticated writes = %v, want exactly [POST /api/v1/node-enrollments/redeem]", unguarded)
	}
}

func TestRedeemAcceptsNoSameOriginHeaderAndNoCredential(t *testing.T) {
	h := newEnrollHarness(t, enrollment.Config{}, newBrokerConfigDir(t, ""))
	minted, _ := h.mint(t, `{"nodeId":"render-01"}`, http.StatusCreated)
	req := httptest.NewRequest(http.MethodPost, "/api/v1/node-enrollments/redeem", strings.NewReader(`{"code":"`+minted.Code+`"}`))
	req.Header.Set("Origin", "http://evil.example")
	resp, raw := doRawRequest(t, h.api.Handler, req)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("curl-style redeem: %d %s", resp.StatusCode, raw)
	}
}
