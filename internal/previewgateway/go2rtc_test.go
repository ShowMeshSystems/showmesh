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
	"sync/atomic"
	"syscall"
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

func TestFreeLoopbackAddrPicksAFreePort(t *testing.T) {
	seen := map[string]bool{}
	for range 5 {
		a, err := freeLoopbackAddr()
		if err != nil || !strings.HasPrefix(a, "127.0.0.1:") {
			t.Fatalf("addr %q, %v", a, err)
		}
		ln, err := net.Listen("tcp", a)
		if err != nil {
			t.Fatalf("the picked address %s is not free: %v", a, err)
		}
		_ = ln.Close()
		seen[a] = true
	}
}

func writeEngineScript(t *testing.T) (bin, pidFile, cfgFile string) {
	t.Helper()
	dir := t.TempDir()
	bin, pidFile, cfgFile = filepath.Join(dir, "go2rtc"), filepath.Join(dir, "pid"), filepath.Join(dir, "cfg")
	script := "#!/bin/sh\necho $$ > " + pidFile + "\necho \"$2\" > " + cfgFile + "\nexec sleep 300\n"
	if err := os.WriteFile(bin, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	return bin, pidFile, cfgFile
}

func waitForFile(t *testing.T, path string) string {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		if b, err := os.ReadFile(path); err == nil && strings.TrimSpace(string(b)) != "" {
			return strings.TrimSpace(string(b))
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatalf("%s never appeared", path)
	return ""
}

func TestRunStopsTheEngineAndRemovesItsConfigBeforeReturning(t *testing.T) {
	bin, pidFile, cfgFile := writeEngineScript(t)
	ln, err := net.Listen("tcp", ":0")
	if err != nil {
		t.Skip(err)
	}
	port := strconv.Itoa(ln.Addr().(*net.TCPAddr).Port)
	_ = ln.Close()
	cfg := Config{Addr: "127.0.0.1:0", Token: "t", Go2RTCBin: bin, WebRTCPort: port}
	ctx, cancel := context.WithCancel(context.Background())
	errc := make(chan error, 1)
	go func() { errc <- Run(ctx, cfg, quietLog()) }()
	pid, _ := strconv.Atoi(waitForFile(t, pidFile))
	cfgPath := waitForFile(t, cfgFile)
	cancel()
	select {
	case err := <-errc:
		if err != nil {
			t.Fatalf("Run = %v", err)
		}
	case <-time.After(20 * time.Second):
		t.Fatal("Run did not return")
	}
	if syscall.Kill(pid, 0) == nil {
		_ = syscall.Kill(pid, syscall.SIGKILL)
		t.Fatal("the engine process outlived Run")
	}
	if _, err := os.Stat(filepath.Dir(cfgPath)); !os.IsNotExist(err) {
		t.Fatalf("the engine's config directory still exists: %v", err)
	}
}

func TestEngineClientIsResetOnlyAfterTheReadinessWorkStops(t *testing.T) {
	bin, _, _ := writeEngineScript(t)
	sup, client := NewSupervisor(bin, "8555", quietLog())
	sup.prove = func(context.Context, string, string) bool { return true }
	var running atomic.Bool
	entered := make(chan struct{})
	sup.OnReady = func(ctx context.Context) {
		running.Store(true)
		close(entered)
		<-ctx.Done()
		time.Sleep(100 * time.Millisecond)
		running.Store(false)
	}
	ctx, cancel := context.WithCancel(context.Background())
	errc := make(chan error, 1)
	go func() { errc <- sup.runOnce(ctx) }()
	<-entered
	if !client.Ready() {
		t.Fatal("the client was not pointed at the engine once it was proven")
	}
	cancel()
	<-errc
	if running.Load() {
		t.Fatal("runOnce returned while the readiness work was still running")
	}
	if client.Ready() {
		t.Fatal("the client still points at an engine that has stopped")
	}
}
