//go:build cgo

package gstengine

import (
	"context"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"
)

// captureStderr redirects the process's real fd 2 (not just os.Stderr,
// since GStreamer/GLib write their own warnings straight to the C
// runtime's stderr, bypassing the Go os.Stderr value entirely) to a
// pipe for the duration of fn, and returns everything written to it.
// This is how this test observes GStreamer's own defense catching a
// double release, without adding any counting hook to production code.
// The restore (close the write end, dup fd 2 back, close the saved
// duplicate, then read whatever fn wrote) runs in a single deferred
// closure so a panic or t.Fatal inside fn still leaves fd 2 pointed at
// the real stderr for every later test in the package, rather than
// leaking the redirect.
func captureStderr(t *testing.T, fn func()) (out string) {
	t.Helper()
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatalf("os.Pipe: %v", err)
	}
	origFd, err := syscall.Dup(2)
	if err != nil {
		t.Fatalf("dup(2): %v", err)
	}
	if err := dupOnto(int(w.Fd()), 2); err != nil {
		t.Fatalf("dup2 onto fd 2: %v", err)
	}

	captured := make(chan string, 1)
	go func() {
		buf, _ := io.ReadAll(r)
		captured <- string(buf)
	}()

	defer func() {
		if err := w.Close(); err != nil {
			t.Logf("closing capture pipe write end: %v", err)
		}
		if err := dupOnto(origFd, 2); err != nil {
			t.Logf("restoring fd 2: %v", err)
		}
		if err := syscall.Close(origFd); err != nil {
			t.Logf("closing saved fd 2: %v", err)
		}
		out = <-captured
		if err := r.Close(); err != nil {
			t.Logf("closing capture pipe read end: %v", err)
		}
	}()

	fn()
	return
}

// TestConcurrentTeardownNeverRunsDoTeardownTwice forces the exact
// interleaving teardown must prevent: two callers both observe
// b.released == false and both attempt the real teardown at once.
//
// The interleaving is forced, not hoped for: pendingStateChanges is
// held elevated (standing in for a state change genuinely in flight,
// exactly as the existing withShrunkTeardownTimeout tests in
// closeincomplete_test.go do) so both teardown calls are parked inside
// awaitNoElementRace's wait before the hold is released, guaranteeing
// both are already past the b.released fast-path check and race to
// enter the real attempt together.
//
// Without the gate, doTeardown carries no exclusion of its own: both
// goroutines pass awaitNoElementRace and both call
// engine.channelMixers[k].ReleaseRequestPad(pad) on the very same
// already-released request pad, which GStreamer's own defensive
// assertion in gst_element_release_request_pad catches and logs as a
// GStreamer-CRITICAL to the process's real stderr: this package's own
// double-free-class hazard, observed rather than merely reasoned about.
// With the gate, only one goroutine's doTeardown ever runs for a
// branch: the loser blocks on teardownGate and, once the winner
// finishes, re-checks b.released and returns nil without calling
// doTeardown at all, so that critical never fires.
func TestConcurrentTeardownNeverRunsDoTeardownTwice(t *testing.T) {
	e := newTestEngine(t)
	dir := t.TempDir()
	wav := filepath.Join(dir, "fixture.wav")
	generateWAV(t, wav, 3)

	withShrunkTeardownTimeout(t, 2*time.Second)

	ctx, cancel := context.WithTimeout(context.Background(), engineOpTimeout)
	defer cancel()
	const handle = "concurrentteardown1"
	if _, err := e.Load(ctx, handle, mediaRef(wav), 3*time.Second); err != nil {
		t.Fatalf("Load: %v", err)
	}
	b, err := e.branchFor(handle)
	if err != nil {
		t.Fatalf("branchFor: %v", err)
	}

	// Artificial hold standing in for a state change genuinely in
	// flight: parks both teardown calls inside awaitNoElementRace's wait
	// loop rather than letting either race ahead of the other on its
	// own.
	b.pendingStateChanges.Add(1)

	var results [2]error
	stderrOutput := captureStderr(t, func() {
		var wg sync.WaitGroup
		start := make(chan struct{})
		wg.Add(2)
		for i := 0; i < 2; i++ {
			go func(i int) {
				defer wg.Done()
				<-start
				results[i] = b.teardown(context.Background())
			}(i)
		}
		close(start)

		// Give both goroutines time to reach awaitNoElementRace's wait
		// loop (they are blocked there by the artificial hold above)
		// before releasing the hold, so they proceed as close to
		// simultaneously as the Go scheduler allows.
		time.Sleep(100 * time.Millisecond)
		b.pendingStateChanges.Add(-1)

		wg.Wait()
	})

	for i, err := range results {
		if err != nil {
			t.Fatalf("teardown[%d] = %v, want nil", i, err)
		}
	}

	if strings.Contains(stderrOutput, "GStreamer-CRITICAL") {
		t.Fatalf("two concurrent teardown calls produced a GStreamer-CRITICAL warning, evidence of a double release/remove "+
			"against this branch's elements or request pads:\n%s", stderrOutput)
	}
}
