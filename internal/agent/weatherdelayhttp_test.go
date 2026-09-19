package agent

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/showmeshsystems/showmesh/internal/agent/audio"
	pkgaudio "github.com/showmeshsystems/showmesh/pkg/audio"
	"github.com/showmeshsystems/showmesh/pkg/weatherdelay"
)

// This file covers ADR-053 decision 9's one HTTP route (weatherdelayhttp.go).

// weatherDelayHTTPTestFixture bundles a running test server, its signing
// key pair, and the holder/manager the route drives, so each test below
// only states what it changes.
type weatherDelayHTTPTestFixture struct {
	srvURL string
	pub    ed25519.PublicKey
	priv   ed25519.PrivateKey
	holder *WeatherDelayHolder
	mgr    *audio.Manager
	dir    string
}

func newWeatherDelayHTTPTestFixture(t *testing.T, enabled bool) *weatherDelayHTTPTestFixture {
	t.Helper()
	dir := t.TempDir()
	pub, priv, err := ed25519.GenerateKey(nil)
	if err != nil {
		t.Fatalf("GenerateKey: %v", err)
	}
	clock := &fakeClock{t: time.Date(2026, 9, 19, 20, 0, 0, 0, time.UTC)}
	mgr, _ := newWeatherDelayTestManager(t, dir, clock)
	holder := &WeatherDelayHolder{store: newWeatherDelayStore(dir)}
	ops := &weatherDelayOperations{holder: holder, audioMgr: mgr, assetDir: dir}

	view := fakeFPPConnectView{enabled: enabled}
	srv := startFPPConnectTestServerWithWeatherDelay(t, view, "node-1", nil, weatherDelayHTTPConfig{ops: ops, publicKey: pub})

	return &weatherDelayHTTPTestFixture{srvURL: srv.URL, pub: pub, priv: priv, holder: holder, mgr: mgr, dir: dir}
}

// sign builds a valid SignedStartRequest for kind, issued at issuedAt.
func (f *weatherDelayHTTPTestFixture) sign(t *testing.T, kind string, issuedAt time.Time) weatherdelay.SignedStartRequest {
	t.Helper()
	req := weatherdelay.StartRequest{Kind: kind, IssuedAt: issuedAt.UTC(), Nonce: "nonce-1"}
	payload, err := req.CanonicalBytes()
	if err != nil {
		t.Fatalf("CanonicalBytes: %v", err)
	}
	sig := ed25519.Sign(f.priv, payload)
	return weatherdelay.SignedStartRequest{Request: req, Signature: sig}
}

func postStart(t *testing.T, url string, body []byte) *http.Response {
	t.Helper()
	resp, err := http.Post(url+weatherDelayStartPath, "application/json", bytes.NewReader(body))
	if err != nil {
		t.Fatalf("POST %s: %v", weatherDelayStartPath, err)
	}
	t.Cleanup(func() { _ = resp.Body.Close() })
	return resp
}

// TestWeatherDelayHTTPValidSignedStartWorksWithNoOtherDependency proves a
// valid signed start succeeds and runs the same effect as the dispatched
// operation, entirely independent of MQTT (this test never constructs a
// broker connection at all) and independent of fppconnect.settings.enabled
// (the fixture below is built with enabled=false).
func TestWeatherDelayHTTPValidSignedStartWorksWithNoOtherDependency(t *testing.T) {
	f := newWeatherDelayHTTPTestFixture(t, false)

	hash := writeAssetFixture(t, f.dir, "alert.wav", []byte("alert audio content"))
	f.holder.rec.Plan = weatherDelayTestPlan(weatherdelay.KindDelay, "alert-asset", hash, "alert.wav", 3)

	signed := f.sign(t, weatherdelay.KindDelay, time.Now())
	body, err := json.Marshal(signed)
	if err != nil {
		t.Fatalf("marshal signed request: %v", err)
	}

	resp := postStart(t, f.srvURL, body)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200", resp.StatusCode)
	}
	if !f.holder.Current().Active {
		t.Fatal("holder not active after a valid signed start")
	}

	deadline := time.Now().Add(5 * time.Second)
	for {
		snaps := f.mgr.Snapshot(t.Context())
		var playing bool
		for _, s := range snaps {
			if s.ID == weatherDelayAlertSessionID && s.State == pkgaudio.StatePlaying {
				playing = true
			}
		}
		if playing {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("alert never started after a valid signed HTTP start")
		}
		time.Sleep(time.Millisecond)
	}
}

// TestWeatherDelayHTTPNoKeyConfiguredAnswers503 proves ADR-025 decision
// 7's own valid degraded state: no key configured, the route answers 503
// and does nothing.
func TestWeatherDelayHTTPNoKeyConfiguredAnswers503(t *testing.T) {
	view := fakeFPPConnectView{enabled: true}
	srv := startFPPConnectTestServerWithWeatherDelay(t, view, "node-1", nil, weatherDelayHTTPConfig{})

	resp := postStart(t, srv.URL, []byte(`{}`))
	if resp.StatusCode != http.StatusServiceUnavailable {
		t.Fatalf("status = %d, want 503", resp.StatusCode)
	}
}

// TestWeatherDelayHTTPTamperedRequestRejected proves a tampered payload
// (a nonce that no longer matches the signature) is refused.
func TestWeatherDelayHTTPTamperedRequestRejected(t *testing.T) {
	f := newWeatherDelayHTTPTestFixture(t, true)
	signed := f.sign(t, weatherdelay.KindDelay, time.Now())
	signed.Request.Nonce = "tampered"
	body, err := json.Marshal(signed)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}

	resp := postStart(t, f.srvURL, body)
	if resp.StatusCode == http.StatusOK {
		t.Fatal("tampered request was accepted")
	}
	if f.holder.Current().Active {
		t.Fatal("holder went active from a tampered request")
	}
}

// TestWeatherDelayHTTPWrongKeyRejected proves a request signed by a key
// other than this node's configured coordinator public key is refused.
func TestWeatherDelayHTTPWrongKeyRejected(t *testing.T) {
	f := newWeatherDelayHTTPTestFixture(t, true)
	_, otherPriv, err := ed25519.GenerateKey(nil)
	if err != nil {
		t.Fatalf("GenerateKey: %v", err)
	}
	req := weatherdelay.StartRequest{Kind: weatherdelay.KindDelay, IssuedAt: time.Now().UTC(), Nonce: "n1"}
	payload, err := req.CanonicalBytes()
	if err != nil {
		t.Fatalf("CanonicalBytes: %v", err)
	}
	signed := weatherdelay.SignedStartRequest{Request: req, Signature: ed25519.Sign(otherPriv, payload)}
	body, err := json.Marshal(signed)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}

	resp := postStart(t, f.srvURL, body)
	if resp.StatusCode == http.StatusOK {
		t.Fatal("a request signed by the wrong key was accepted")
	}
	if f.holder.Current().Active {
		t.Fatal("holder went active from a wrong-key request")
	}
}

// TestWeatherDelayHTTPOversizeBodyRejected proves the 4 KiB cap.
func TestWeatherDelayHTTPOversizeBodyRejected(t *testing.T) {
	f := newWeatherDelayHTTPTestFixture(t, true)
	oversize := bytes.Repeat([]byte("a"), weatherDelayHTTPMaxBodyBytes+1)

	resp := postStart(t, f.srvURL, oversize)
	if resp.StatusCode == http.StatusOK {
		t.Fatal("an oversize body was accepted")
	}
}

// TestWeatherDelayHTTPStaleRequestRejected proves a request older than
// 24 hours is refused.
func TestWeatherDelayHTTPStaleRequestRejected(t *testing.T) {
	f := newWeatherDelayHTTPTestFixture(t, true)
	signed := f.sign(t, weatherdelay.KindDelay, time.Now().Add(-25*time.Hour))
	body, err := json.Marshal(signed)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}

	resp := postStart(t, f.srvURL, body)
	if resp.StatusCode == http.StatusOK {
		t.Fatal("a stale request was accepted")
	}
	if f.holder.Current().Active {
		t.Fatal("holder went active from a stale request")
	}
}

// TestWeatherDelayHTTPFutureRequestRejected proves a request more than 5
// minutes in the future is refused.
func TestWeatherDelayHTTPFutureRequestRejected(t *testing.T) {
	f := newWeatherDelayHTTPTestFixture(t, true)
	signed := f.sign(t, weatherdelay.KindDelay, time.Now().Add(10*time.Minute))
	body, err := json.Marshal(signed)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}

	resp := postStart(t, f.srvURL, body)
	if resp.StatusCode == http.StatusOK {
		t.Fatal("a request 10 minutes in the future was accepted")
	}
}

// TestWeatherDelayHTTPReplayOfAValidStartIsAccepted proves ADR-053
// decision 8: replaying a valid start on purpose is accepted, not refused.
func TestWeatherDelayHTTPReplayOfAValidStartIsAccepted(t *testing.T) {
	f := newWeatherDelayHTTPTestFixture(t, true)
	hash := writeAssetFixture(t, f.dir, "alert.wav", []byte("alert audio content"))
	f.holder.rec.Plan = weatherDelayTestPlan(weatherdelay.KindDelay, "alert-asset", hash, "alert.wav", 3)

	signed := f.sign(t, weatherdelay.KindDelay, time.Now())
	body, err := json.Marshal(signed)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}

	first := postStart(t, f.srvURL, body)
	if first.StatusCode != http.StatusOK {
		t.Fatalf("first send status = %d, want 200", first.StatusCode)
	}
	second := postStart(t, f.srvURL, body)
	if second.StatusCode != http.StatusOK {
		t.Fatalf("replayed send status = %d, want 200 (a replay must be accepted, not refused)", second.StatusCode)
	}
}

// TestWeatherDelayHTTPNoResumeRouteAndGetOnStartIs404Or405 proves item 5's
// own acceptance line: no resume route exists, and GET on the start path
// is refused.
func TestWeatherDelayHTTPNoResumeRouteAndGetOnStartIs404Or405(t *testing.T) {
	f := newWeatherDelayHTTPTestFixture(t, true)

	resumeResp, err := http.Post(f.srvURL+"/showmesh/v1/weather-delay/resume", "application/json", bytes.NewReader([]byte(`{}`)))
	if err != nil {
		t.Fatalf("POST resume: %v", err)
	}
	_ = resumeResp.Body.Close()
	if resumeResp.StatusCode != http.StatusNotFound && resumeResp.StatusCode != http.StatusMethodNotAllowed {
		t.Fatalf("POST .../resume status = %d, want 404 or 405", resumeResp.StatusCode)
	}

	getResp, err := http.Get(f.srvURL + weatherDelayStartPath)
	if err != nil {
		t.Fatalf("GET start: %v", err)
	}
	_ = getResp.Body.Close()
	if getResp.StatusCode != http.StatusNotFound && getResp.StatusCode != http.StatusMethodNotAllowed {
		t.Fatalf("GET %s status = %d, want 404 or 405", weatherDelayStartPath, getResp.StatusCode)
	}
}

// ctxHonoringEngine fails Start once its context is done, as the real
// engine does.
type ctxHonoringEngine struct{ activationAvailableEngine }

func (e ctxHonoringEngine) Start(ctx context.Context, handle audio.EngineHandle, position time.Duration) (audio.EngineObservation, error) {
	if err := ctx.Err(); err != nil {
		return audio.EngineObservation{}, err
	}
	return e.activationAvailableEngine.Start(ctx, handle, position)
}

// TestWeatherDelayHTTPAlertSurvivesTheCallerHangingUp proves a signed
// start whose client disconnects still plays the alert.
func TestWeatherDelayHTTPAlertSurvivesTheCallerHangingUp(t *testing.T) {
	dir := t.TempDir()
	pub, priv, err := ed25519.GenerateKey(nil)
	if err != nil {
		t.Fatalf("GenerateKey: %v", err)
	}
	clock := &fakeClock{t: time.Date(2026, 9, 19, 20, 0, 0, 0, time.UTC)}
	engine := ctxHonoringEngine{activationAvailableEngine{audio.NewFakeEngine(clock.now)}}
	mgr := audio.NewManager(engine, audio.NewFileSessionStore(dir), dir, fixedAudioDecoder{}, clock.now, nil)
	mgr.SetSettings(audio.Settings{DefaultFadeCurve: pkgaudio.FadeCurveLinear, DefaultFadeDurationMs: 500})

	hash := writeAssetFixture(t, dir, "alert.wav", []byte("alert audio content"))
	holder := &WeatherDelayHolder{store: newWeatherDelayStore(dir)}
	holder.rec.Plan = weatherDelayTestPlan(weatherdelay.KindDelay, "alert-asset", hash, "alert.wav", 3)
	ops := &weatherDelayOperations{holder: holder, audioMgr: mgr, assetDir: dir}
	handler := newFPPConnectHandler(fakeFPPConnectView{enabled: true}, "node-1", newTestFPPConnectHeldStore(t), weatherDelayHTTPConfig{ops: ops, publicKey: pub}, time.Now, discardLogger())

	req := weatherdelay.StartRequest{Kind: weatherdelay.KindDelay, IssuedAt: time.Now().UTC(), Nonce: "n1"}
	payload, err := req.CanonicalBytes()
	if err != nil {
		t.Fatalf("CanonicalBytes: %v", err)
	}
	body, err := json.Marshal(weatherdelay.SignedStartRequest{Request: req, Signature: ed25519.Sign(priv, payload)})
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	r := httptest.NewRequest(http.MethodPost, weatherDelayStartPath, bytes.NewReader(body)).WithContext(ctx)
	w := httptest.NewRecorder()
	handler.ServeHTTP(w, r)

	var got struct {
		AlertPlaying bool   `json:"alertPlaying"`
		AlertReason  string `json:"alertReason"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &got); err != nil {
		t.Fatalf("decode response %q: %v", w.Body.String(), err)
	}
	if !got.AlertPlaying {
		t.Fatalf("alert did not play after the caller hung up: %s", got.AlertReason)
	}
}
