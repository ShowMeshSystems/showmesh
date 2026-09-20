package agent

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
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
	ops    *weatherDelayOperations
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

	return &weatherDelayHTTPTestFixture{srvURL: srv.URL, pub: pub, priv: priv, holder: holder, mgr: mgr, ops: ops, dir: dir}
}

// sign builds a valid SignedStartRequest for kind, issued at issuedAt, with
// no notAfter.
func (f *weatherDelayHTTPTestFixture) sign(t *testing.T, kind string, issuedAt time.Time) weatherdelay.SignedStartRequest {
	t.Helper()
	return f.signWithNotAfter(t, kind, issuedAt, time.Time{})
}

// signWithNotAfter builds a valid SignedStartRequest carrying an explicit
// notAfter; the zero time.Time leaves it unset.
func (f *weatherDelayHTTPTestFixture) signWithNotAfter(t *testing.T, kind string, issuedAt, notAfter time.Time) weatherdelay.SignedStartRequest {
	t.Helper()
	req := weatherdelay.StartRequest{Kind: kind, IssuedAt: issuedAt.UTC(), Nonce: "nonce-1"}
	if !notAfter.IsZero() {
		req.NotAfter = notAfter.UTC()
	}
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
// valid signed start runs the dispatched operation's effect with no broker
// and with fppconnect.settings.enabled false.
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

// TestWeatherDelayHTTPNotAfterAcceptedPast24Hours proves a request older
// than the fixed 24 hour rule still verifies when its own notAfter has not
// passed.
func TestWeatherDelayHTTPNotAfterAcceptedPast24Hours(t *testing.T) {
	f := newWeatherDelayHTTPTestFixture(t, true)
	hash := writeAssetFixture(t, f.dir, "alert.wav", []byte("alert audio content"))
	f.holder.rec.Plan = weatherDelayTestPlan(weatherdelay.KindDelay, "alert-asset", hash, "alert.wav", 3)

	issuedAt := time.Now().Add(-48 * time.Hour)
	signed := f.signWithNotAfter(t, weatherdelay.KindDelay, issuedAt, time.Now().Add(24*time.Hour))
	body, err := json.Marshal(signed)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}

	resp := postStart(t, f.srvURL, body)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200 for a request past 24 hours old but before its own notAfter", resp.StatusCode)
	}
}

// TestWeatherDelayHTTPNotAfterExpiredRejected proves a request whose own
// notAfter has passed is refused, even though it is fresh.
func TestWeatherDelayHTTPNotAfterExpiredRejected(t *testing.T) {
	f := newWeatherDelayHTTPTestFixture(t, true)
	signed := f.signWithNotAfter(t, weatherdelay.KindDelay, time.Now().Add(-time.Hour), time.Now().Add(-time.Minute))
	body, err := json.Marshal(signed)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}

	resp := postStart(t, f.srvURL, body)
	if resp.StatusCode == http.StatusOK {
		t.Fatal("a request past its own notAfter was accepted")
	}
	if f.holder.Current().Active {
		t.Fatal("holder went active from an expired-notAfter request")
	}
}

// TestWeatherDelayHTTPNotAfterBeyond400DaysRejected proves signed.Verify's
// own [weatherdelay.StartRequest.Validate] refuses a notAfter more than
// 400 days past issuedAt, since the route calls Verify before any age
// check of its own.
func TestWeatherDelayHTTPNotAfterBeyond400DaysRejected(t *testing.T) {
	f := newWeatherDelayHTTPTestFixture(t, true)
	issuedAt := time.Now()
	signed := f.signWithNotAfter(t, weatherdelay.KindDelay, issuedAt, issuedAt.Add(401*24*time.Hour))
	body, err := json.Marshal(signed)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}

	resp := postStart(t, f.srvURL, body)
	if resp.StatusCode == http.StatusOK {
		t.Fatal("a request with notAfter more than 400 days past issuedAt was accepted")
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

// TestWeatherDelayHTTPRefusalBodyNamesNoFileOrSession proves the signed
// start route says only what the coordinator needs. A caller here may be
// replaying a captured request, so each refusal answers one fixed sentence
// and the body names no file, asset, session id or path, and no longer
// lists the sessions that could not be muted.
func TestWeatherDelayHTTPRefusalBodyNamesNoFileOrSession(t *testing.T) {
	cases := []struct {
		name    string
		setUp   func(t *testing.T, f *weatherDelayHTTPTestFixture)
		kind    string
		want    string
		playing bool
	}{
		{
			name:  "no alert sound set for this kind",
			setUp: func(*testing.T, *weatherDelayHTTPTestFixture) {},
			kind:  weatherdelay.KindDelay,
			want:  weatherDelayPublicNoAlertSet,
		},
		{
			name: "the alert sound is not on this node",
			setUp: func(t *testing.T, f *weatherDelayHTTPTestFixture) {
				f.holder.rec.Plan = weatherDelayTestPlan(weatherdelay.KindDelay, "alert-asset", "sha256:deadbeef", "alert.wav", 3)
			},
			kind: weatherdelay.KindDelay,
			want: weatherDelayPublicAlertNotHere,
		},
		{
			name: "this node has no audio engine",
			setUp: func(t *testing.T, f *weatherDelayHTTPTestFixture) {
				hash := writeAssetFixture(t, f.dir, "alert.wav", []byte("alert audio content"))
				f.holder.rec.Plan = weatherDelayTestPlan(weatherdelay.KindDelay, "alert-asset", hash, "alert.wav", 3)
				f.ops.audioMgr = nil
			},
			kind: weatherdelay.KindDelay,
			want: weatherDelayPublicNoAudioEngine,
		},
		{
			name: "the night is already cancelled",
			setUp: func(t *testing.T, f *weatherDelayHTTPTestFixture) {
				hash := writeAssetFixture(t, f.dir, "alert.wav", []byte("alert audio content"))
				f.holder.rec.Plan = weatherDelayTestPlan(weatherdelay.KindCancelNight, "alert-asset", hash, "alert.wav", 3)
				if err := f.holder.SetActiveLocal(weatherdelay.KindCancelNight, time.Now(), "test"); err != nil {
					t.Fatalf("SetActiveLocal: %v", err)
				}
			},
			kind:    weatherdelay.KindDelay,
			want:    weatherDelayPublicNightCancelled,
			playing: true,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			f := newWeatherDelayHTTPTestFixture(t, true)
			t.Cleanup(weatherDelayBackground.Wait)
			tc.setUp(t, f)

			body, err := json.Marshal(f.sign(t, tc.kind, time.Now()))
			if err != nil {
				t.Fatalf("marshal: %v", err)
			}
			resp := postStart(t, f.srvURL, body)
			if resp.StatusCode != http.StatusOK {
				t.Fatalf("status = %d, want 200", resp.StatusCode)
			}
			raw, err := io.ReadAll(resp.Body)
			if err != nil {
				t.Fatalf("read body: %v", err)
			}

			var decoded map[string]any
			if err := json.Unmarshal(raw, &decoded); err != nil {
				t.Fatalf("decode %q: %v", raw, err)
			}
			for _, key := range []string{"kind", "alertPlaying", "alertReason"} {
				if _, ok := decoded[key]; !ok {
					t.Fatalf("body %s is missing %q", raw, key)
				}
			}
			if len(decoded) != 3 {
				t.Fatalf("body %s carries %d fields, want exactly kind, alertPlaying and alertReason", raw, len(decoded))
			}
			if got := decoded["alertReason"]; got != tc.want {
				t.Fatalf("alertReason = %q, want %q", got, tc.want)
			}
			if got := decoded["alertPlaying"]; got != tc.playing {
				t.Fatalf("alertPlaying = %v, want %v", got, tc.playing)
			}
			for _, secret := range []string{"alert.wav", "alert-asset", "weatherdelay:alert", f.dir} {
				if strings.Contains(string(raw), secret) {
					t.Fatalf("the response body %s names %q; a replaying caller must learn no file, asset, session id or path", raw, secret)
				}
			}
		})
	}
}
