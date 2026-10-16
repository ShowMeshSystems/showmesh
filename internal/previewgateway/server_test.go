package previewgateway

import (
	"context"
	"errors"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
)

const testToken = "s3cret-token"

type harness struct {
	srv *httptest.Server
	e   *fakeEngine
	src *fakeSources
	m   *Manager
}

func newHarness(t *testing.T) *harness {
	t.Helper()
	e := newFakeEngine()
	src := &fakeSources{ok: true, names: map[string]bool{"A (x)": true},
		list: []Source{{Name: "A (x)", Address: "10.0.0.5:5961"}}}
	m := newTestManager(e, src)
	ts := httptest.NewServer(NewServer(testToken, m, src, e).Handler())
	t.Cleanup(ts.Close)
	return &harness{srv: ts, e: e, src: src, m: m}
}

func (h *harness) do(t *testing.T, method, path, token, body string) (*http.Response, string) {
	t.Helper()
	req, _ := http.NewRequest(method, h.srv.URL+path, strings.NewReader(body))
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = resp.Body.Close() }()
	var sb strings.Builder
	buf := make([]byte, 4096)
	for {
		n, err := resp.Body.Read(buf)
		sb.Write(buf[:n])
		if err != nil {
			break
		}
	}
	return resp, sb.String()
}

func TestAuthRequiredOnEveryV1Route(t *testing.T) {
	h := newHarness(t)
	routes := [][2]string{
		{"GET", "/v1/sources"}, {"PUT", "/v1/streams"}, {"GET", "/v1/streams"},
		{"POST", "/v1/streams/k/webrtc"}, {"GET", "/v1/streams/k/snapshot"},
	}
	for _, r := range routes {
		for _, tok := range []string{"", "wrong", testToken + "x", "Bearer"} {
			resp, body := h.do(t, r[0], r[1], tok, "[]")
			if resp.StatusCode != http.StatusUnauthorized {
				t.Errorf("%s %s token %q: status %d", r[0], r[1], tok, resp.StatusCode)
			}
			if strings.Contains(body, testToken) {
				t.Error("response leaks the token")
			}
		}
	}
	if len(h.e.puts) != 0 {
		t.Fatal("an unauthenticated request changed the stream set")
	}
	req, _ := http.NewRequest("GET", h.srv.URL+"/v1/sources", nil)
	req.Header.Set("Authorization", "Basic "+testToken)
	resp, _ := http.DefaultClient.Do(req)
	if resp.StatusCode != http.StatusUnauthorized {
		t.Errorf("non-bearer scheme: %d", resp.StatusCode)
	}
}

func TestHealthzNeedsNoToken(t *testing.T) {
	h := newHarness(t)
	if resp, _ := h.do(t, "GET", "/healthz", "", ""); resp.StatusCode != 200 {
		t.Fatalf("status %d", resp.StatusCode)
	}
}

func TestSources(t *testing.T) {
	h := newHarness(t)
	resp, body := h.do(t, "GET", "/v1/sources", testToken, "")
	if resp.StatusCode != 200 || !strings.Contains(body, `"name":"A (x)"`) || !strings.Contains(body, `"address":"10.0.0.5:5961"`) {
		t.Fatalf("%d %s", resp.StatusCode, body)
	}
	h.src.list = nil
	if _, body := h.do(t, "GET", "/v1/sources", testToken, ""); strings.TrimSpace(body) != "[]" {
		t.Fatalf("empty list must be [], got %q", body)
	}
}

func TestPutThenGetStreams(t *testing.T) {
	h := newHarness(t)
	put := `[{"key":"ndi:A","kind":"ndi","ndiName":"A (x)"},{"key":"camera:b","kind":"rtsp","url":"rtsp://u:pw@h:554/s"}]`
	if resp, _ := h.do(t, "PUT", "/v1/streams", testToken, put); resp.StatusCode != 204 {
		t.Fatalf("put status %d", resp.StatusCode)
	}
	resp, body := h.do(t, "GET", "/v1/streams", testToken, "")
	if resp.StatusCode != 200 || !strings.Contains(body, `"key":"ndi:A"`) || !strings.Contains(body, `"state":"idle"`) {
		t.Fatalf("%d %s", resp.StatusCode, body)
	}
	if strings.Contains(body, "pw") || strings.Contains(body, "rtsp://") {
		t.Fatalf("GET /v1/streams leaks a camera address: %s", body)
	}
}

func TestPutRejectsBadSetWithoutChangingAnything(t *testing.T) {
	h := newHarness(t)
	good := `[{"key":"ndi:A","kind":"ndi","ndiName":"A (x)"}]`
	h.do(t, "PUT", "/v1/streams", testToken, good)
	bad := `[{"key":"ndi:A","kind":"ndi","ndiName":"A (x)"},{"key":"c","kind":"rtsp","url":"http://h"}]`
	resp, body := h.do(t, "PUT", "/v1/streams", testToken, bad)
	if resp.StatusCode != 400 || !strings.Contains(body, "error") {
		t.Fatalf("%d %s", resp.StatusCode, body)
	}
	if _, _, ok := h.m.Lookup("ndi:A"); !ok || len(h.e.deletes) != 0 {
		t.Fatal("a rejected set changed the desired set")
	}
}

func TestSnapshot(t *testing.T) {
	h := newHarness(t)
	h.do(t, "PUT", "/v1/streams", testToken, `[{"key":"ndi:A/b c","kind":"ndi","ndiName":"A (x)"}]`)
	path := "/v1/streams/" + url.PathEscape("ndi:A/b c") + "/snapshot"
	if resp, _ := h.do(t, "GET", path, testToken, ""); resp.StatusCode != 502 {
		t.Fatalf("no frame: %d", resp.StatusCode)
	}
	h.e.frame, h.e.frameErr = []byte{0xFF, 0xD8, 0x01, 0x02, 0xFF, 0xD9}, nil
	resp, body := h.do(t, "GET", path, testToken, "")
	if resp.StatusCode != 200 || resp.Header.Get("Content-Type") != "image/jpeg" || body != string(h.e.frame) {
		t.Fatalf("%d %q %q", resp.StatusCode, resp.Header.Get("Content-Type"), body)
	}
	if resp, _ := h.do(t, "GET", "/v1/streams/unknown/snapshot", testToken, ""); resp.StatusCode != 404 {
		t.Fatalf("unknown key: %d", resp.StatusCode)
	}
}

func TestWebRTC(t *testing.T) {
	h := newHarness(t)
	h.do(t, "PUT", "/v1/streams", testToken, `[{"key":"camera:b","kind":"rtsp","url":"rtsp://h/s"}]`)
	path := "/v1/streams/camera:b/webrtc"
	if resp, _ := h.do(t, "POST", path, testToken, "garbage"); resp.StatusCode != 400 {
		t.Fatalf("garbage offer: %d", resp.StatusCode)
	}
	if resp, _ := h.do(t, "POST", path, testToken, ""); resp.StatusCode != 400 {
		t.Fatalf("empty offer: %d", resp.StatusCode)
	}
	if resp, _ := h.do(t, "POST", path, testToken, "v=0\r\n"+strings.Repeat("a", maxSDPBytes)); resp.StatusCode != 400 {
		t.Fatalf("oversize offer: %d", resp.StatusCode)
	}
	h.e.answerErr = errors.New("x")
	if resp, _ := h.do(t, "POST", path, testToken, "v=0\r\n"); resp.StatusCode != 502 {
		t.Fatalf("engine failure: %d", resp.StatusCode)
	}
	h.e.answerErr = nil
	h.e.answer = []byte("v=0\r\nm=video 9 UDP/TLS/RTP/SAVPF 96\r\n")
	resp, body := h.do(t, "POST", path, testToken, "v=0\r\nm=video\r\n")
	if resp.StatusCode != 200 || resp.Header.Get("Content-Type") != "application/sdp" || body != string(h.e.answer) {
		t.Fatalf("%d %q %q", resp.StatusCode, resp.Header.Get("Content-Type"), body)
	}
	if string(h.e.lastOffer) != "v=0\r\nm=video\r\n" {
		t.Fatalf("offer not forwarded: %q", h.e.lastOffer)
	}
	if resp, _ := h.do(t, "POST", "/v1/streams/nope/webrtc", testToken, "v=0\r\n"); resp.StatusCode != 404 {
		t.Fatalf("unknown key: %d", resp.StatusCode)
	}
	_ = context.Background()
}

func TestConfigFromEnv(t *testing.T) {
	env := func(m map[string]string) func(string) string { return func(k string) string { return m[k] } }
	if _, err := ConfigFromEnv(env(nil)); err == nil {
		t.Fatal("a missing token must be refused")
	}
	if _, err := ConfigFromEnv(env(map[string]string{"SHOWMESH_PREVIEW_GATEWAY_TOKEN": "  "})); err == nil {
		t.Fatal("a blank token must be refused")
	}
	c, err := ConfigFromEnv(env(map[string]string{"SHOWMESH_PREVIEW_GATEWAY_TOKEN": "t"}))
	if err != nil || c.Addr != ":8095" || c.WebRTCPort != "8555" {
		t.Fatalf("%+v %v", c, err)
	}
	if _, err := ConfigFromEnv(env(map[string]string{"SHOWMESH_PREVIEW_GATEWAY_TOKEN": "t", "SHOWMESH_PREVIEW_GATEWAY_ADDR": "nonsense"})); err == nil {
		t.Fatal("a bad address must be refused")
	}
}

func TestRedactAndConfig(t *testing.T) {
	if got := redact(`dial rtsp://admin:hunter2@10.0.0.5:554/x failed`); strings.Contains(got, "hunter2") || strings.Contains(got, "admin") {
		t.Fatalf("credentials survive: %s", got)
	}
	cfg := go2rtcConfig("127.0.0.1:41984", "pw", "8555", []string{"192.168.1.5:8555"})
	for _, want := range []string{`listen: "127.0.0.1:41984"`, "local_auth: true", "rtsp:\n  listen: \"\"", "srtp:\n  listen: \"\"", `listen: ":8555"`, "ice_servers: []", `- "192.168.1.5:8555"`} {
		if !strings.Contains(cfg, want) {
			t.Errorf("config lacks %q:\n%s", want, cfg)
		}
	}
	if strings.Contains(go2rtcConfig("127.0.0.1:41984", "pw", "8555", nil), "candidates") {
		t.Error("an empty candidate list must be left out")
	}
	if !isJPEG([]byte{0xFF, 0xD8, 0, 0xFF, 0xD9}) || isJPEG([]byte{}) || isJPEG([]byte("<html>")) {
		t.Fatal("isJPEG")
	}
}

func TestWebRTCCandidates(t *testing.T) {
	got := candidatesFrom([]ifaceAddr{
		{"lo", net.ParseIP("127.0.0.1")},
		{"eth0", net.ParseIP("192.168.130.176")},
		{"eth0", net.ParseIP("fe80::1")},
		{"eth0", net.ParseIP("169.254.3.4")},
		{"docker0", net.ParseIP("172.17.0.1")},
		{"br-abc", net.ParseIP("172.20.0.1")},
		{"wlan0", net.ParseIP("10.0.0.7")},
	}, "8555")
	if len(got) != 2 || got[0] != "192.168.130.176:8555" || got[1] != "10.0.0.7:8555" {
		t.Fatalf("got %v", got)
	}
	if o := WebRTCCandidates("8555", " 1.2.3.4 , host.example "); len(o) != 2 || o[0] != "1.2.3.4:8555" || o[1] != "host.example:8555" {
		t.Fatalf("override: %v", o)
	}
}

func TestConfigRejectsABadWebRTCPort(t *testing.T) {
	env := map[string]string{"SHOWMESH_PREVIEW_GATEWAY_TOKEN": "t", "SHOWMESH_PREVIEW_GATEWAY_WEBRTC_PORT": "9000"}
	c, err := ConfigFromEnv(func(k string) string { return env[k] })
	if err != nil || c.WebRTCPort != "9000" {
		t.Fatalf("%+v %v", c, err)
	}
	for _, bad := range []string{"http", "0", "70000"} {
		env["SHOWMESH_PREVIEW_GATEWAY_WEBRTC_PORT"] = bad
		if _, err := ConfigFromEnv(func(k string) string { return env[k] }); err == nil {
			t.Errorf("port %q accepted", bad)
		}
	}
}

type gatedEngine struct {
	*fakeEngine
	ready bool
}

func (g *gatedEngine) Ready() bool { return g.ready }

func TestHealthzFollowsTheVideoEngineNotAvahi(t *testing.T) {
	eng := &gatedEngine{fakeEngine: newFakeEngine()}
	d := NewDiscovery(quietLog())
	d.Avahi = func() error { return errNoAvahi }
	srv := NewServer("t", NewManager(eng, d, "http://127.0.0.1:9/x", quietLog()), d, eng)
	get := func() *httptest.ResponseRecorder {
		rec := httptest.NewRecorder()
		srv.Handler().ServeHTTP(rec, httptest.NewRequest("GET", "/healthz", nil))
		return rec
	}
	if rec := get(); rec.Code != http.StatusServiceUnavailable || !strings.Contains(rec.Body.String(), "video engine is not running") {
		t.Fatalf("engine down: %d %q", rec.Code, rec.Body.String())
	}
	eng.ready = true
	if rec := get(); rec.Code != http.StatusOK {
		t.Fatalf("engine up with Avahi down: %d %q", rec.Code, rec.Body.String())
	}
}
