package api

import (
	"context"
	"crypto/ed25519"
	"encoding/json"
	"net/http"
	"testing"
	"time"

	"github.com/showmeshsystems/showmesh/internal/coordinator/config"
	"github.com/showmeshsystems/showmesh/internal/coordinator/identity"
	"github.com/showmeshsystems/showmesh/pkg/coordsig"
	"github.com/showmeshsystems/showmesh/pkg/weatherdelay"
)

// Covers item 4's own POST /api/v1/weather-delay/presigned-start.

// fakeWeatherDelaySigner signs with a real Ed25519 key so a test can
// verify the minted document, mirroring internal/agent's own key pair
// fixtures rather than a stub that never actually signs anything.
type fakeWeatherDelaySigner struct {
	priv ed25519.PrivateKey
}

func newFakeWeatherDelaySigner(t *testing.T) (*fakeWeatherDelaySigner, ed25519.PublicKey) {
	t.Helper()
	pub, priv, err := ed25519.GenerateKey(nil)
	if err != nil {
		t.Fatalf("GenerateKey: %v", err)
	}
	return &fakeWeatherDelaySigner{priv: priv}, pub
}

func (s *fakeWeatherDelaySigner) Sign(payload []byte) (coordsig.Signature, error) {
	return coordsig.Signature(ed25519.Sign(s.priv, payload)), nil
}

func newWeatherDelayPresignTestAPI(t *testing.T) (*API, string, ed25519.PublicKey, *countingNodeAddrs) {
	t.Helper()
	now := time.Now()
	svc, st, _ := newTestIdentityServiceWithStore(t, fixedClock(now))
	admin := mustCreatePrincipal(t, svc, "admin-1", identity.RoleAdmin)
	signer, pub := newFakeWeatherDelaySigner(t)
	addrs := &countingNodeAddrs{}
	api := New(Dependencies{
		Nodes: &fakeNodeLister{}, Observations: &fakeObservationLister{},
		Events: &fakeEventReader{}, Collectors: &fakeCollectorStatusLister{},
		Identity: svc, Config: st, WeatherDelay: st,
		WeatherDelaySigner: signer, WeatherDelayNodeAddrs: addrs,
	}.withDefaults(), Options{Clock: fixedClock(now), Logger: testLogger()})
	return api, mustIssueToken(t, svc, admin.ID), pub, addrs
}

func TestWeatherDelayPresignRequiresConfigWriteScope(t *testing.T) {
	now := time.Now()
	svc, st, _ := newTestIdentityServiceWithStore(t, fixedClock(now))
	operator := mustCreatePrincipal(t, svc, "operator-1", identity.RoleOperator)
	api := New(Dependencies{
		Nodes: &fakeNodeLister{}, Observations: &fakeObservationLister{},
		Events: &fakeEventReader{}, Collectors: &fakeCollectorStatusLister{},
		Identity: svc, Config: st, WeatherDelay: st,
	}.withDefaults(), Options{Clock: fixedClock(now), Logger: testLogger()})
	auth := map[string]string{"Authorization": "Bearer " + mustIssueToken(t, svc, operator.ID)}

	resp, body := doRawRequest(t, api.Handler, newJSONRequest(t, http.MethodPost, "/api/v1/weather-delay/presigned-start", `{"kind":"delay","validDays":1}`, auth))
	if resp.StatusCode != http.StatusForbidden {
		t.Fatalf("presign as operator: status = %d, want 403; body: %s", resp.StatusCode, body)
	}
}

func TestWeatherDelayPresignMintsAVerifiableSignedStart(t *testing.T) {
	api, token, pub, _ := newWeatherDelayPresignTestAPI(t)
	auth := map[string]string{"Authorization": "Bearer " + token}

	resp, body := doRawRequest(t, api.Handler, newJSONRequest(t, http.MethodPost, "/api/v1/weather-delay/presigned-start", `{"kind":"cancelNight","validDays":30}`, auth))
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("presign: status = %d, want 200; body: %s", resp.StatusCode, body)
	}

	var decoded struct {
		Request weatherdelay.SignedStartRequest `json:"request"`
	}
	if err := json.Unmarshal(body, &decoded); err != nil {
		t.Fatalf("decode %s: %v", body, err)
	}
	if decoded.Request.Request.Kind != "cancelNight" {
		t.Fatalf("kind = %q, want cancelNight", decoded.Request.Request.Kind)
	}
	if err := decoded.Request.Verify(pub); err != nil {
		t.Fatalf("Verify() = %v, want a valid signature under the coordinator's own key", err)
	}
	wantNotAfter := decoded.Request.Request.IssuedAt.AddDate(0, 0, 30)
	if !decoded.Request.Request.NotAfter.Equal(wantNotAfter) {
		t.Fatalf("notAfter = %v, want %v (issuedAt + 30 days)", decoded.Request.Request.NotAfter, wantNotAfter)
	}
}

func TestWeatherDelayPresignReportsNodeURLs(t *testing.T) {
	api, token, _, _ := newWeatherDelayPresignTestAPI(t)
	auth := map[string]string{"Authorization": "Bearer " + token}

	resp, body := doRawRequest(t, api.Handler, newJSONRequest(t, http.MethodPost, "/api/v1/weather-delay/presigned-start", `{"kind":"delay","validDays":1}`, auth))
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("presign: status = %d, want 200; body: %s", resp.StatusCode, body)
	}
	var decoded struct {
		NodeURLs []string `json:"nodeUrls"`
	}
	if err := json.Unmarshal(body, &decoded); err != nil {
		t.Fatalf("decode %s: %v", body, err)
	}
	// countingNodeAddrs answers a listener for every node id, and the
	// alert's own configured node list is empty (every declared audio
	// node), which resolves to none declared in this fixture, so the
	// list is legitimately empty here; the shape itself is what this
	// test checks.
	if decoded.NodeURLs == nil {
		t.Fatal("nodeUrls = nil, want an empty array, never null")
	}
}

func TestWeatherDelayPresignRejectsBadKindAndValidDays(t *testing.T) {
	api, token, _, _ := newWeatherDelayPresignTestAPI(t)
	auth := map[string]string{"Authorization": "Bearer " + token}
	cases := []string{
		`{"kind":"resume","validDays":1}`,
		`{"kind":"delay","validDays":0}`,
		`{"kind":"delay","validDays":401}`,
		`{"kind":"delay"}`,
		`{}`,
	}
	for _, body := range cases {
		resp, respBody := doRawRequest(t, api.Handler, newJSONRequest(t, http.MethodPost, "/api/v1/weather-delay/presigned-start", body, auth))
		if resp.StatusCode != http.StatusBadRequest {
			t.Errorf("body %q: status = %d, want 400; body: %s", body, resp.StatusCode, respBody)
		}
	}
}

// TestWeatherDelayPresignNodeRuleAcceptsAcrossDaylightSaving proves a 400
// day document minted on a local clock that crosses a daylight saving
// change still passes the node's own 400 day rule.
func TestWeatherDelayPresignNodeRuleAcceptsAcrossDaylightSaving(t *testing.T) {
	chicago, err := time.LoadLocation("America/Chicago")
	if err != nil {
		t.Skipf("no time zone data: %v", err)
	}
	now := time.Date(2026, 10, 30, 12, 0, 0, 0, chicago)
	svc, st, _ := newTestIdentityServiceWithStore(t, fixedClock(now))
	admin := mustCreatePrincipal(t, svc, "admin-1", identity.RoleAdmin)
	signer, pub := newFakeWeatherDelaySigner(t)
	api := New(Dependencies{
		Nodes: &fakeNodeLister{}, Observations: &fakeObservationLister{},
		Events: &fakeEventReader{}, Collectors: &fakeCollectorStatusLister{},
		Identity: svc, Config: st, WeatherDelay: st,
		WeatherDelaySigner: signer, WeatherDelayNodeAddrs: &countingNodeAddrs{},
	}.withDefaults(), Options{Clock: fixedClock(now), Logger: testLogger()})
	auth := map[string]string{"Authorization": "Bearer " + mustIssueToken(t, svc, admin.ID)}

	resp, body := doRawRequest(t, api.Handler, newJSONRequest(t, http.MethodPost, "/api/v1/weather-delay/presigned-start", `{"kind":"delay","validDays":400}`, auth))
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("presign 400 days: status = %d, want 200; body: %s", resp.StatusCode, body)
	}
	var decoded struct {
		Request weatherdelay.SignedStartRequest `json:"request"`
	}
	if err := json.Unmarshal(body, &decoded); err != nil {
		t.Fatalf("decode %s: %v", body, err)
	}
	if err := decoded.Request.Verify(pub); err != nil {
		t.Fatalf("Verify() = %v, want the node to accept a 400 day document", err)
	}
}

// TestWeatherDelayPresignReportsDeclaredNodeURLsAndAudits proves the node
// URLs come from each declared node's reported listener and the mint is
// audited as show.weatherdelay.presign.
func TestWeatherDelayPresignReportsDeclaredNodeURLsAndAudits(t *testing.T) {
	now := time.Now()
	svc, st, _ := newTestIdentityServiceWithStore(t, fixedClock(now))
	admin := mustCreatePrincipal(t, svc, "admin-1", identity.RoleAdmin)
	signer, _ := newFakeWeatherDelaySigner(t)
	nodePayload, err := config.EncodeAudioNodePayload(config.AudioNodePayload{
		ProgramRoute: "usb-interface", ProgramChannels: []int{1, 2},
	})
	if err != nil {
		t.Fatalf("encode audio node payload: %v", err)
	}
	putConfigForTest(t, st, config.AudioNodeConfigKind, "audio-01", nodePayload)
	api := New(Dependencies{
		Nodes: &fakeNodeLister{}, Observations: &fakeObservationLister{},
		Events: &fakeEventReader{}, Collectors: &fakeCollectorStatusLister{},
		Identity: svc, Config: st, WeatherDelay: st,
		WeatherDelaySigner: signer, WeatherDelayNodeAddrs: &countingNodeAddrs{},
	}.withDefaults(), Options{Clock: fixedClock(now), Logger: testLogger()})
	auth := map[string]string{"Authorization": "Bearer " + mustIssueToken(t, svc, admin.ID)}

	resp, body := doRawRequest(t, api.Handler, newJSONRequest(t, http.MethodPost, "/api/v1/weather-delay/presigned-start", `{"kind":"delay","validDays":7}`, auth))
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("presign: status = %d, want 200; body: %s", resp.StatusCode, body)
	}
	var decoded struct {
		NodeURLs []string `json:"nodeUrls"`
	}
	if err := json.Unmarshal(body, &decoded); err != nil {
		t.Fatalf("decode %s: %v", body, err)
	}
	if len(decoded.NodeURLs) != 1 || decoded.NodeURLs[0] != "http://127.0.0.1:1/showmesh/v1/weather-delay/start" {
		t.Fatalf("nodeUrls = %v, want the one declared node's start URL", decoded.NodeURLs)
	}
	entries, err := st.ListAuditEntries(context.Background(), 0, 0)
	if err != nil {
		t.Fatalf("ListAuditEntries: %v", err)
	}
	for _, e := range entries {
		if e.Action == identity.AuditActionShowWeatherDelayPresign && e.Target == "delay" {
			return
		}
	}
	t.Fatalf("no %s audit entry was written", identity.AuditActionShowWeatherDelayPresign)
}
