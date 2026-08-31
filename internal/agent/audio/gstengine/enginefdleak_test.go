//go:build cgo

package gstengine

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/go-gst/go-gst/pkg/gst"

	agentaudio "github.com/showmeshsystems/showmesh/internal/agent/audio"
)

// countOpenSockets returns the number of this process's open file
// descriptors whose /proc/self/fd target begins with "socket:".
func countOpenSockets(t *testing.T) int {
	t.Helper()
	entries, err := os.ReadDir("/proc/self/fd")
	if err != nil {
		t.Skipf("skipping: could not read /proc/self/fd: %v", err)
	}
	n := 0
	for _, e := range entries {
		target, err := os.Readlink("/proc/self/fd/" + e.Name())
		if err == nil && strings.HasPrefix(target, "socket:") {
			n++
		}
	}
	return n
}

// TestCloseReleasesEverySocketItsOwnConstructionOpened proves a
// completed Close leaves no open socket behind. Each engine construction
// costs the pipeline bus references awaitSustainedPlaying and watchBus
// each take via GetBus, plus the messages TimedPop pops off that bus
// while settling into PLAYING -- none of which go-gst's own finalizer
// reclaims in practice (measured: still present after a forced GC, an
// explicit pipeline unref alone, and OS-thread pinning). Three rebuild
// cycles, not one: the very first release attempted here only dropped
// the bus reference and still leaked, because the pipeline's own
// internal reference on its bus kept it alive regardless -- a leak that
// a single before/after pair around one construction can miss if the
// two candidate causes partially compensate for each other on the first
// cycle alone.
func TestCloseReleasesEverySocketItsOwnConstructionOpened(t *testing.T) {
	gst.Init() // ParseLaunch below (via generateWAV) needs GStreamer initialized before the first New would otherwise do it
	dir := t.TempDir()
	wav := filepath.Join(dir, "fixture.wav")
	generateWAV(t, wav, 2)
	ctx, cancel := context.WithTimeout(context.Background(), engineOpTimeout)
	defer cancel()

	// One warm-up cycle outside the measurement: GStreamer's own
	// process-wide registry/clock/device-monitor init costs sockets the
	// first time anything in this package touches it, once, regardless
	// of this fix -- not what this guard is asserting.
	warm := newTestEngine(t)
	if _, err := warm.Load(ctx, "warm", mediaRef(wav), 2*time.Second); err != nil {
		t.Fatalf("warm Load: %v", err)
	}
	if err := warm.Release(ctx, "warm"); err != nil {
		t.Fatalf("warm Release: %v", err)
	}
	if err := warm.Close(); err != nil {
		t.Fatalf("warm Close: %v", err)
	}
	time.Sleep(200 * time.Millisecond)

	before := countOpenSockets(t)
	const rebuilds = 3
	for i := 1; i <= rebuilds; i++ {
		e, err := New(testConfig(resolveByRuntimeFilename))
		if err != nil {
			t.Fatalf("iter %d New: %v", i, err)
		}
		if ok, reason := e.Available(); !ok {
			t.Skipf("skipping: gstengine unavailable in this environment: %s", reason)
		}
		handle := agentaudio.EngineHandle(fmt.Sprintf("guard-%d", i))
		if _, err := e.Load(ctx, handle, mediaRef(wav), 2*time.Second); err != nil {
			t.Fatalf("iter %d Load: %v", i, err)
		}
		if err := e.Release(ctx, handle); err != nil {
			t.Fatalf("iter %d Release: %v", i, err)
		}
		if err := e.Close(); err != nil {
			t.Fatalf("iter %d Close: %v", i, err)
		}
		time.Sleep(200 * time.Millisecond)
	}

	after := countOpenSockets(t)
	if after != before {
		t.Fatalf("open sockets went from %d to %d across %d rebuild cycles, want unchanged: each engine construction is leaking sockets its own Close does not release", before, after, rebuilds)
	}
}
