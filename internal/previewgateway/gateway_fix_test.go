package previewgateway

import (
	"context"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"
)

func TestRedactHidesPasswordsWithAtSignsAndPathTokens(t *testing.T) {
	cases := []struct{ in, gone, kept string }{
		{`dial rtsp://admin:p@ss:w0rd@10.0.0.5:554/live failed`, "p@ss", "rtsp://***@10.0.0.5:554/*** failed"},
		{`rtsps://cam.local:322/h264?token=abc123&u=x`, "abc123", "rtsps://cam.local:322/***"},
		{`rtsp://admin:pa/ss@10.0.0.5/stream`, "pa/ss", "rtsp://***@10.0.0.5/***"},
		{`RTSP://10.0.0.5:554/secretpath`, "secretpath", "RTSP://10.0.0.5:554/***"},
		{`source "rtsp://u:p@h:554/s"`, "u:p", `"rtsp://***@h:554/***"`},
	}
	for _, c := range cases {
		got := redact(c.in)
		if strings.Contains(got, c.gone) || !strings.Contains(got, c.kept) {
			t.Errorf("redact(%q) = %q, want it to hold %q and not %q", c.in, got, c.kept, c.gone)
		}
	}
	if got := redact("no url here"); got != "no url here" {
		t.Fatalf("got %q", got)
	}
}

func TestProveEngineNeedsRefusalWithoutPasswordAndAcceptanceWithIt(t *testing.T) {
	const pw = "secret"
	ours := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if _, p, ok := r.BasicAuth(); !ok || p != pw {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		_, _ = w.Write([]byte("{}"))
	}))
	defer ours.Close()
	open := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { _, _ = w.Write([]byte("{}")) }))
	defer open.Close()
	closed := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusUnauthorized) }))
	defer closed.Close()
	host := func(s *httptest.Server) string { return strings.TrimPrefix(s.URL, "http://") }

	ctx := context.Background()
	if !proveEngine(ctx, host(ours), pw) {
		t.Error("an engine that knows the password was refused")
	}
	if proveEngine(ctx, host(ours), "wrong") {
		t.Error("a wrong password was accepted")
	}
	if proveEngine(ctx, host(open), pw) {
		t.Error("a foreign service that answers everyone was accepted")
	}
	if proveEngine(ctx, host(closed), pw) {
		t.Error("a foreign service that refuses everyone was accepted")
	}
	if proveEngine(ctx, "127.0.0.1:1", pw) {
		t.Error("nothing listening was accepted")
	}
}

func TestFreeLoopbackAddrAvoidsAnOccupiedPort(t *testing.T) {
	busy, err := net.Listen("tcp", "127.0.0.1:1984")
	if err == nil {
		defer func() { _ = busy.Close() }()
	}
	seen := map[string]bool{}
	for range 5 {
		a, err := freeLoopbackAddr()
		if err != nil || !strings.HasPrefix(a, "127.0.0.1:") || a == "127.0.0.1:1984" {
			t.Fatalf("addr %q, %v", a, err)
		}
		seen[a] = true
	}
}

func TestEngineLineFaults(t *testing.T) {
	if engineLineFault(`19:31:56.870 ERR [api] listen error="listen tcp 127.0.0.1:1984: bind: address already in use"`) == nil {
		t.Error("an API listen failure must end the run")
	}
	if engineLineFault(`19:32:14.329 INF [api] listen addr=127.0.0.1:41999`) != nil {
		t.Error("a healthy line was treated as a fault")
	}
}

func TestCheckWebRTCPortRefusesATakenPort(t *testing.T) {
	ln, err := net.Listen("tcp", ":0")
	if err != nil {
		t.Skip(err)
	}
	defer func() { _ = ln.Close() }()
	port := ln.Addr().(*net.TCPAddr).Port
	if err := CheckWebRTCPort(strconv.Itoa(port)); err == nil || !strings.Contains(err.Error(), "already in use") {
		t.Fatalf("err = %v", err)
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

func TestSupervisorRestartsWhenTheAPIPortCannotBeOpened(t *testing.T) {
	bin := filepath.Join(t.TempDir(), "go2rtc")
	script := "#!/bin/sh\necho 'ERR [api] listen error=\"address already in use\"'\nexec sleep 30\n"
	if err := os.WriteFile(bin, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	sup, _ := NewSupervisor(bin, "8555", quietLog())
	start := time.Now()
	err := sup.runOnce(context.Background())
	if err == nil || time.Since(start) > 10*time.Second {
		t.Fatalf("err = %v after %v", err, time.Since(start))
	}
}

func TestSupervisorStopsWhenTheWebRTCPortIsTaken(t *testing.T) {
	ln, err := net.Listen("tcp", ":0")
	if err != nil {
		t.Skip(err)
	}
	defer func() { _ = ln.Close() }()
	port := strconv.Itoa(ln.Addr().(*net.TCPAddr).Port)
	sup, _ := NewSupervisor("/nonexistent/go2rtc", port, quietLog())
	err = sup.Run(context.Background())
	if err == nil || !strings.Contains(err.Error(), "SHOWMESH_PREVIEW_GATEWAY_WEBRTC_PORT") {
		t.Fatalf("err = %v", err)
	}
}

func TestClientIsUnconfiguredUntilProvenAndCallsTimeOut(t *testing.T) {
	c := NewGo2RTCClient("", "")
	if _, err := c.List(context.Background()); err == nil {
		t.Fatal("an unconfigured client must not send anything")
	}
	release := make(chan struct{})
	slow := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { <-release }))
	defer slow.Close()
	defer close(release)
	c.Configure(strings.TrimPrefix(slow.URL, "http://"), "pw")
	start := time.Now()
	if _, err := c.List(context.Background()); err == nil || time.Since(start) > engineCallWait+2*time.Second {
		t.Fatalf("err = %v after %v", err, time.Since(start))
	}
}

func TestMinimalEnvLeavesOutTheGatewayToken(t *testing.T) {
	t.Setenv("SHOWMESH_PREVIEW_GATEWAY_TOKEN", "s3cret-token")
	t.Setenv("PATH", "/usr/bin")
	t.Setenv("GST_PLUGIN_PATH", "/plug")
	for name, env := range map[string][]string{"engine": minimalEnv(engineEnvKeys...), "gstreamer": minimalEnv(gstEnvKeys...)} {
		for _, e := range env {
			if strings.Contains(e, "s3cret") || strings.HasPrefix(e, "SHOWMESH_") {
				t.Errorf("%s environment holds %q", name, e)
			}
		}
	}
	if got := minimalEnv(gstEnvKeys...); !contains(got, "GST_PLUGIN_PATH=/plug") || !contains(got, "PATH=/usr/bin") {
		t.Errorf("gstreamer lost what it needs: %v", got)
	}
	if env := minimalEnv("NOT_SET_ANYWHERE"); env == nil {
		t.Error("an empty environment must not be nil, which would inherit everything")
	}
	cmd, err := gstreamerCommand(context.Background(), "A (x)")
	if err != nil || contains(cmd.Env, "SHOWMESH_PREVIEW_GATEWAY_TOKEN=s3cret-token") || cmd.Env == nil {
		t.Errorf("gst-launch environment: %v %v", cmd.Env, err)
	}
}

type blockingEngine struct {
	*fakeEngine
	enter   chan struct{}
	release chan struct{}
}

func (b *blockingEngine) Put(ctx context.Context, id, src string) error {
	b.enter <- struct{}{}
	<-b.release
	return b.fakeEngine.Put(ctx, id, src)
}

func TestSlowEngineDoesNotBlockLookupsOrStatus(t *testing.T) {
	e := &blockingEngine{fakeEngine: newFakeEngine(), enter: make(chan struct{}, 1), release: make(chan struct{})}
	m := NewManager(e, &fakeSources{ok: true}, "http://127.0.0.1:9/ndi/sec", quietLog())
	var wg sync.WaitGroup
	wg.Add(1)
	go func() { defer wg.Done(); m.Set(context.Background(), []StreamSpec{camB}) }()
	<-e.enter
	done := make(chan struct{})
	go func() {
		m.Lookup("camera:b")
		m.NDIName("x")
		m.NoteResult("camera:b", true)
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("lookups wait for the engine call")
	}
	close(e.release)
	wg.Wait()
}

func TestChangedSpecUnderTheSameKeyIsReplaced(t *testing.T) {
	e := newFakeEngine()
	m := newTestManager(e, &fakeSources{ok: true})
	ctx := context.Background()
	m.Set(ctx, []StreamSpec{ndiA})
	renamed := ndiA
	renamed.NDIName = "B (y)"
	m.Set(ctx, []StreamSpec{renamed})
	id := streamID(ndiA.Key)
	if len(e.deletes) != 1 || e.deletes[0] != id || len(e.puts) != 2 {
		t.Fatalf("a changed ndiName must be deleted then put: deletes=%v puts=%v", e.deletes, e.puts)
	}
	if name, _ := m.NDIName(id); name != "B (y)" {
		t.Fatalf("feed would still serve %q", name)
	}
}

func TestUnavailableDiscoveryStatesWhyAndWhatToDo(t *testing.T) {
	e := newFakeEngine()
	m := newTestManager(e, &fakeSources{ok: false, msg: msgNoAvahi})
	m.Set(context.Background(), []StreamSpec{ndiA})
	st := m.Streams(context.Background())
	if st[0].State != StateError || st[0].Message != msgNoAvahi {
		t.Fatalf("%+v", st[0])
	}
}

func TestDiscoveryReportsAvahiDown(t *testing.T) {
	d := NewDiscovery(quietLog())
	d.Avahi = func() error { return errNoAvahi }
	if err := d.Available(context.Background()); err != errNoAvahi {
		t.Fatalf("Available = %v", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	go d.Loop(ctx)
	deadline := time.Now().Add(2 * time.Second)
	for d.Unavailable() == "" && time.Now().Before(deadline) {
		time.Sleep(10 * time.Millisecond)
	}
	cancel()
	if d.Unavailable() != msgNoAvahi || d.AvahiProblem() != msgNoAvahi {
		t.Fatalf("Unavailable = %q", d.Unavailable())
	}
	srv := NewServer("t", NewManager(newFakeEngine(), d, "http://127.0.0.1:9/x", quietLog()), d, newFakeEngine())
	rec := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rec, httptest.NewRequest("GET", "/healthz", nil))
	if rec.Code != http.StatusServiceUnavailable || !strings.Contains(rec.Body.String(), "Restart the gateway container") {
		t.Fatalf("healthz %d %q", rec.Code, rec.Body.String())
	}
}
