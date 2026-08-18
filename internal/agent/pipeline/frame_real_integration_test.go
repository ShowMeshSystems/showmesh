package pipeline

import (
	"context"
	"net"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/showmeshsystems/showmesh/pkg/fseq"
	"github.com/showmeshsystems/showmesh/pkg/multisync"
)

// realFSEQSamplePath is the owner's real show file, per the seam spec.
// Never committed to the repo (it is the owner's own show content); tests
// using it must skip cleanly, with a printed reason, when it is absent —
// which is every environment except the owner's own machine.
func realFSEQSamplePath(t *testing.T) string {
	t.Helper()
	home, err := os.UserHomeDir()
	if err != nil {
		t.Skipf("skipping: could not determine home directory: %v", err)
	}
	path := filepath.Join(home, "showmesh-fseq-samples", "kpop 2026 MH Test.fseq")
	if _, err := os.Stat(path); err != nil {
		t.Skipf("skipping: real FSEQ sample not present at %s (%v) — this test only runs on the owner's machine", path, err)
	}
	return path
}

// TestFrameWriterAgainstRealFSEQRealMultiSyncRealGStreamer is this seam's
// end-to-end proof against real components, not fakes:
//
//   - a real *fseq.File opened against the owner's actual show file,
//     extracting the real matrix-1 channel range RES-017 §13.2 verified
//     (1-based start channel 25410, i.e. 0-based 25409);
//   - a real pkg/multisync.Listener bound to a real (non-32320) loopback
//     UDP socket, fed real wire-format START/SYNC packets built with
//     pkg/multisync's own EncodeSync — NOT a real fppd (none was
//     available to this test run; see the build contract's item 2, which
//     explicitly allows this as an alternative to the bench container);
//   - a real gst-launch-1.0 subprocess (skipped if the binary is not on
//     PATH), receiving frames over its real stdin pipe via fdsrc.
//
// It measures and logs (via t.Logf, never written into RES-004 by this
// test — that record is for the orchestrator to fold in from a run's
// actual output) the per-frame buffer size and the frame writer's own
// written/late/dropped counts over a real wall-clock window.
func TestFrameWriterAgainstRealFSEQRealMultiSyncRealGStreamer(t *testing.T) {
	if _, ok, _ := ResolveGstLaunch(); !ok {
		t.Skip("skipping: gst-launch-1.0 not found on PATH")
	}
	fseqPath := realFSEQSamplePath(t)

	f, err := fseq.Open(fseqPath)
	if err != nil {
		t.Fatalf("fseq.Open: %v", err)
	}
	defer func() { _ = f.Close() }()

	t.Logf("real FSEQ: frames=%d channelsPerFrame=%d stepTimeMS=%d compression=%v",
		f.FrameCount(), f.ChannelCount(), f.StepTimeMS(), f.Compression())

	// Matrix 1's real, owner-supplied 1-based start channel is 25410; the
	// ONE 1-based-to-0-based conversion for this test happens here, exactly
	// as renderops.go's buildFSEQAssignment does it for a real assignment.
	const startChannel1Based = 25410
	const channelStart0 = startChannel1Based - 1
	// 100x100 rgb = 30000 channels, comfortably inside matrix 1's real
	// 45241-channel sparse range (RES-017 §13.2), and divisible by 3 so a
	// real rawvideoparse can frame it as RGB without this test needing a
	// pixel format that doesn't exist (see GstVideoFormatForPixelFormat).
	const width, height = 100, 100
	const channelCount = width * height * 3

	// Real MultiSync listener on a real (non-32320) loopback UDP socket —
	// see runMultiSyncListener's own doc comment in internal/agent for why
	// AllowPortSharing is never set true; this test binds ":0" so it can
	// never collide with a real fppd on this machine either.
	listener, err := multisync.NewListener(multisync.ListenerConfig{ListenAddr: "127.0.0.1:0", Logger: nil})
	if err != nil {
		t.Fatalf("multisync.NewListener: %v", err)
	}
	defer func() { _ = listener.Close() }()

	timeline := multisync.NewTimeline(time.Now, multisync.Config{})
	timeline.SetStepTime(time.Duration(f.StepTimeMS()) * time.Millisecond)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	listenerDone := make(chan struct{})
	go func() {
		defer close(listenerDone)
		_ = listener.Run(ctx, func(rec multisync.Received) {
			if rec.DecodeErr != nil {
				return
			}
			if sync, ok := rec.Payload.(multisync.SyncPacket); ok {
				timeline.Observe(sync, "127.0.0.1")
			}
		})
	}()
	t.Cleanup(func() {
		cancel()
		<-listenerDone
	})

	// Send real, wire-encoded MultiSync packets — no real fppd is involved
	// in producing them (this environment has none reachable), but the
	// bytes on the wire are pkg/multisync's own real encoder output,
	// decoded by this listener's real Decode path exactly as a genuine
	// fppd's packets would be.
	conn, err := net.Dial("udp4", listener.LocalAddr().String())
	if err != nil {
		t.Fatalf("dial listener: %v", err)
	}
	defer func() { _ = conn.Close() }()

	sendSync := func(action multisync.SyncAction, frameNumber uint32) {
		b, err := multisync.EncodeSync(multisync.SyncPacket{
			Action:      action,
			FileType:    multisync.SyncFileTypeSequence,
			FrameNumber: frameNumber,
			Filename:    "kpop-2026-mh-test.fseq",
		})
		if err != nil {
			t.Fatalf("EncodeSync: %v", err)
		}
		if _, err := conn.Write(b); err != nil {
			t.Fatalf("send sync packet: %v", err)
		}
	}

	sendSync(multisync.SyncActionStart, 0)
	waitForCond(t, func() bool { return timeline.Snapshot().State == multisync.StatePlaying })

	// A few SYNC packets advancing frame position, mimicking a real
	// master's cadence closely enough to exercise the timeline's
	// free-run-plus-correction path rather than a single START alone.
	for i := 1; i <= 3; i++ {
		time.Sleep(20 * time.Millisecond)
		sendSync(multisync.SyncActionSync, uint32(i*4))
	}

	// Real gst-launch-1.0 subprocess: FSEQSourceSpec's fdsrc/rawvideoparse
	// source stage into the existing B2a placeholder fakesink (B4 replaces
	// this with the real NDI sink stage). fdsrcIsLive is this machine's own
	// real, probed answer (see FdsrcSupportsIsLive), not hardcoded true —
	// on GStreamer < 1.26 fdsrc has no is-live property at all, and this is
	// this package's one test against a real subprocess for both shapes.
	fdsrcIsLive := FdsrcSupportsIsLive(nil)
	spec, err := FSEQSourceSpec("surface-real-1", width, height, "rgb", 40, fdsrcIsLive)
	if err != nil {
		t.Fatalf("FSEQSourceSpec: %v", err)
	}
	sup := NewSupervisor(time.Now, nil, testLogger{}) // nil starter = real startRealProcess
	t.Cleanup(func() {
		shutdownCtx, shutdownCancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer shutdownCancel()
		sup.Shutdown(shutdownCtx)
	})
	if err := sup.Apply(spec); err != nil {
		t.Fatalf("Apply: %v", err)
	}

	// The frame writer starts BEFORE awaiting Running, matching production
	// (internal/agent/renderops.go's applySurface): without fdsrcIsLive, a
	// real fdsrc genuinely PREROLLs and needs a first buffer to ever reach
	// PLAYING, so awaiting Running before anything feeds its stdin would
	// deadlock this test on exactly the platforms this field exists for.
	fw, err := NewFrameWriter(sup, "surface-real-1", f, timeline, channelStart0, channelCount, width, height, IdleOutputBlack, testLogger{})
	if err != nil {
		t.Fatalf("NewFrameWriter against real FSEQ: %v", err)
	}
	fwCtx, fwCancel := context.WithCancel(context.Background())
	go fw.Run(fwCtx)

	awaitCtx, awaitCancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer awaitCancel()
	snap, ok := sup.AwaitState(awaitCtx, "surface-real-1", []State{StateRunning}, time.Time{}, -1, 20*time.Millisecond)
	if !ok {
		t.Fatalf("real gst-launch-1.0 pipeline never reached Running (fdsrcIsLive=%v); last snapshot: %+v", fdsrcIsLive, snap)
	}

	runStart := time.Now()
	const runFor = 1 * time.Second
	time.Sleep(runFor)
	elapsed := time.Since(runStart)

	fwCancel()
	fw.Stop()

	written, late, dropped := fw.Counts()
	achievedFPS := float64(written) / elapsed.Seconds()

	// Measurements for RES-004 (reported here, not written there — the
	// orchestrator folds real run output into that record per this
	// project's own rule against a builder claiming verification).
	t.Logf("REAL RUN MEASUREMENTS: buffer_bytes_per_frame=%d target_fps=40 elapsed=%s written=%d late=%d dropped=%d achieved_fps=%.2f",
		channelCount, elapsed, written, late, dropped, achievedFPS)

	if written == 0 {
		t.Fatalf("frame writer wrote 0 frames against a real running gst-launch-1.0 pipeline over %s", elapsed)
	}

	finalSnap, ok := sup.Snapshot("surface-real-1")
	if !ok || finalSnap.State != StateRunning {
		t.Fatalf("pipeline state after the run = %+v (ok=%v), want still Running (a real gst-launch-1.0 process must not have crashed while receiving real frames)", finalSnap, ok)
	}
}

func waitForCond(t *testing.T, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if cond() {
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatalf("condition never became true within 5s")
}
