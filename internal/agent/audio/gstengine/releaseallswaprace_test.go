//go:build cgo

package gstengine

import (
	"context"
	"fmt"
	"path/filepath"
	"testing"
	"time"

	"github.com/go-gst/go-gst/pkg/gst"

	agentaudio "github.com/showmeshsystems/showmesh/internal/agent/audio"
)

// TestReleaseAllOvertakesAnInFlightSwap proves an emergency stop that
// lands while a Resume's swap is still building its replacement still
// ends with nothing live and nothing playing. Before the fix, ReleaseAll
// never saw the replacement at all (tracked only in
// inFlightReplacements, invisible through e.handles), and commitSwap
// republished it unconditionally once the swap finished building,
// undoing the stop that had already run.
//
// Several trials sweep a small staggered delay before calling ReleaseAll,
// to land at least one of them inside the real (reported ~9ms) window a
// Resume swap's own build takes on real GStreamer. The assertion holds
// on every trial regardless of whether that particular one landed inside
// the window: a ReleaseAll that lands clearly before or clearly after
// the swap is the ordinary case this already handled correctly.
func TestReleaseAllOvertakesAnInFlightSwap(t *testing.T) {
	// generateWAV's own pipeline needs the plugin registry gst.Init()
	// builds; every other fixture-generating test in this package calls
	// it indirectly by building an [Engine] first. This test's fixture
	// is shared across every delay trial below, built before any of
	// them, so it must init explicitly instead.
	gst.Init()

	dir := t.TempDir()
	wav := filepath.Join(dir, "fixture.wav")
	generateWAV(t, wav, 6)

	delays := []time.Duration{
		0, 200 * time.Microsecond, 500 * time.Microsecond, 1 * time.Millisecond,
		2 * time.Millisecond, 3 * time.Millisecond, 5 * time.Millisecond, 7 * time.Millisecond,
	}
	for i, delay := range delays {
		delay := delay
		t.Run(fmt.Sprintf("delay=%s", delay), func(t *testing.T) {
			e := newTestEngine(t)
			ctx, cancel := context.WithTimeout(context.Background(), engineOpTimeout)
			defer cancel()

			handle := agentaudio.EngineHandle(fmt.Sprintf("releaseallrace-%d", i))
			if _, err := e.Load(ctx, handle, mediaRef(wav), 6*time.Second); err != nil {
				t.Fatalf("Load: %v", err)
			}
			if _, err := e.Start(ctx, handle, 0); err != nil {
				t.Fatalf("Start: %v", err)
			}
			if _, err := e.Pause(ctx, handle); err != nil {
				t.Fatalf("Pause: %v", err)
			}

			// Resume always swaps in a replacement (see [Engine.Resume]'s
			// own doc comment): this is the in-flight window under test.
			resumeDone := make(chan struct{})
			go func() {
				defer close(resumeDone)
				_, _ = e.Resume(ctx, handle)
			}()
			if delay > 0 {
				time.Sleep(delay)
			}
			if _, err := e.ReleaseAll(ctx); err != nil {
				t.Fatalf("ReleaseAll: %v", err)
			}
			<-resumeDone

			live, err := e.LiveHandles(context.Background())
			if err != nil {
				t.Fatalf("LiveHandles: %v", err)
			}
			if len(live) != 0 {
				t.Fatalf("LiveHandles = %v, want empty: ReleaseAll racing an in-flight swap left something live", live)
			}
			if _, err := e.Observe(context.Background(), handle); err == nil {
				t.Fatalf("Observe(%s) after ReleaseAll raced its swap returned no error; "+
					"the swap republished a branch the stop had already released", handle)
			}
		})
	}
}
