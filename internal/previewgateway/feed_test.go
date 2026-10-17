package previewgateway

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"
)

func newFeedHarness(t *testing.T, script string) (*httptest.Server, *fakeSources, string) {
	t.Helper()
	return newFeedHarnessFor(t, script, StreamSpec{Key: "ndi:A", Kind: KindNDI, NDIName: "A (x)"})
}

func newFeedHarnessFor(t *testing.T, script string, spec StreamSpec) (*httptest.Server, *fakeSources, string) {
	t.Helper()
	src := &fakeSources{ok: true, names: map[string]bool{"A (x)": true}}
	m := newTestManager(newFakeEngine(), src)
	m.Set(context.Background(), []StreamSpec{spec})
	f := NewFeed("sec", m, src, quietLog())
	f.Command = func(ctx context.Context, name, address string) (*exec.Cmd, error) {
		if name != spec.NDIName || address != spec.NDIAddress {
			t.Errorf("command built for %q at %q", name, address)
		}
		return exec.CommandContext(ctx, "sh", "-c", script), nil
	}
	ts := httptest.NewServer(f.Handler())
	t.Cleanup(ts.Close)
	return ts, src, "/ndi/sec/" + streamID(spec.Key)
}

func TestFeedStreamsVideoAndStopsPipelineOnDisconnect(t *testing.T) {
	pidFile := t.TempDir() + "/pid"
	ts, _, path := newFeedHarness(t, "echo $$ > "+pidFile+"; printf 'TSDATA'; exec sleep 60")
	ctx, cancel := context.WithCancel(context.Background())
	req, _ := http.NewRequestWithContext(ctx, "GET", ts.URL+path, nil)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	buf := make([]byte, 6)
	if _, err := io.ReadFull(resp.Body, buf); err != nil || string(buf) != "TSDATA" {
		t.Fatalf("read %q, %v", buf, err)
	}
	if resp.Header.Get("Content-Type") != "video/mp2t" {
		t.Fatalf("content type %q", resp.Header.Get("Content-Type"))
	}
	raw, _ := os.ReadFile(pidFile)
	pid, _ := strconv.Atoi(strings.TrimSpace(string(raw)))
	if pid == 0 || syscall.Kill(pid, 0) != nil {
		t.Fatalf("pipeline not running: pid %d", pid)
	}
	cancel()
	_ = resp.Body.Close()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if syscall.Kill(pid, 0) != nil {
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatal("pipeline kept running after the viewer left")
}

func TestFeedRefusals(t *testing.T) {
	ts, src, path := newFeedHarness(t, "true")
	get := func(p string) int {
		resp, err := http.Get(ts.URL + p)
		if err != nil {
			t.Fatal(err)
		}
		_ = resp.Body.Close()
		return resp.StatusCode
	}
	if c := get("/ndi/wrong/" + streamID("ndi:A")); c != 404 {
		t.Errorf("wrong secret: %d", c)
	}
	if c := get("/ndi/sec/kdeadbeef"); c != 404 {
		t.Errorf("unknown stream: %d", c)
	}
	src.names = map[string]bool{}
	if c := get(path); c != 404 {
		t.Errorf("unannounced source must not start a pipeline: %d", c)
	}
	src.ok = false
	if c := get(path); c != 503 {
		t.Errorf("no NDI runtime: %d", c)
	}
}

func TestFeedReachesASourceByAddressWithoutDiscovery(t *testing.T) {
	ts, src, path := newFeedHarnessFor(t, "printf TS", StreamSpec{Key: "ndi:B", Kind: KindNDI, NDIName: "B (y)", NDIAddress: "10.0.20.15:5961"})
	src.ok, src.names = false, map[string]bool{}
	resp, err := http.Get(ts.URL + path)
	if err != nil {
		t.Fatal(err)
	}
	body, _ := io.ReadAll(resp.Body)
	_ = resp.Body.Close()
	if resp.StatusCode != http.StatusOK || string(body) != "TS" {
		t.Fatalf("status %d body %q", resp.StatusCode, body)
	}
}
