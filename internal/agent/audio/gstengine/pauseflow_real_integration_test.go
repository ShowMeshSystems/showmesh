//go:build cgo

package gstengine

import (
	"context"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"

	"github.com/go-gst/go-gst/pkg/gst"

	agentaudio "github.com/showmeshsystems/showmesh/internal/agent/audio"
	pkgaudio "github.com/showmeshsystems/showmesh/pkg/audio"
)

// countQueueSrcBuffers installs a counting probe on handle's branch's
// queue src pad, the exact point the defect this file proves was
// measured against (a branch's own contribution to the mix, downstream of
// decode, upstream of the channel mixers), and returns a function
// reading the running total. The probe is never removed: the branch is
// released at the end of each test, which tears the pad down with it.
func countQueueSrcBuffers(t *testing.T, e *Engine, handle string) func() int64 {
	t.Helper()
	b, err := e.branchFor(agentaudio.EngineHandle(handle))
	if err != nil {
		t.Fatalf("branchFor %q: %v", handle, err)
	}
	var n atomic.Int64
	pad := b.queue.GetStaticPad("src")
	pad.AddProbe(gst.PadProbeTypeBuffer, func(self gst.Pad, info *gst.PadProbeInfo) gst.PadProbeReturn {
		n.Add(1)
		return gst.PadProbeOK
	})
	return n.Load
}

// trackQueueSrcPosition installs a probe on handle's branch's queue src
// pad, the same point countQueueSrcBuffers counts, and returns a function
// reading the PTS of the most recently seen buffer there: the actual
// content position this branch has most recently contributed to the mix.
func trackQueueSrcPosition(t *testing.T, e *Engine, handle string) func() (time.Duration, bool) {
	t.Helper()
	b, err := e.branchFor(agentaudio.EngineHandle(handle))
	if err != nil {
		t.Fatalf("branchFor %q: %v", handle, err)
	}
	var pos atomic.Int64
	var seen atomic.Bool
	pad := b.queue.GetStaticPad("src")
	pad.AddProbe(gst.PadProbeTypeBuffer, func(self gst.Pad, info *gst.PadProbeInfo) gst.PadProbeReturn {
		if buf := info.GetBuffer(); buf != nil {
			pos.Store(int64(buf.PTS()))
			seen.Store(true)
		}
		return gst.PadProbeOK
	})
	return func() (time.Duration, bool) { return time.Duration(pos.Load()), seen.Load() }
}

// settlePosition polls latest until it stops changing for settleWindow, or
// timeout elapses, and returns the position it settled at. Used to find the
// content position a branch's queue has finished draining to after Pause:
// blockFlow stops new content from entering queue but lets whatever it
// already held keep draining out, so the position right after Pause is not
// yet final.
func settlePosition(latest func() (time.Duration, bool), timeout, settleWindow time.Duration) (time.Duration, bool) {
	deadline := time.Now().Add(timeout)
	const pollInterval = 5 * time.Millisecond
	last, ok := latest()
	lastChanged := time.Now()
	for time.Now().Before(deadline) {
		time.Sleep(pollInterval)
		cur, curOK := latest()
		if curOK && cur != last {
			last, ok = cur, curOK
			lastChanged = time.Now()
			continue
		}
		if curOK && time.Since(lastChanged) >= settleWindow {
			return last, true
		}
	}
	return last, ok
}

// TestPausedBranchStopsBufferFlow is the acceptance evidence the defect
// demands: flow, not position. A buffer probe on the paused branch's own
// queue src pad must count exactly zero buffers for the duration of the
// pause, with the branch's own reported position unchanged across the
// same window and a live GStreamer query proving it, not merely a
// software bookmark.
func TestPausedBranchStopsBufferFlow(t *testing.T) {
	e := newTestEngine(t)
	dir := t.TempDir()
	wav := filepath.Join(dir, "fixture.wav")
	generateWAV(t, wav, 5)

	ctx, cancel := context.WithTimeout(context.Background(), engineOpTimeout)
	defer cancel()

	if _, err := e.Load(ctx, "pf1", mediaRef(wav), 5*time.Second); err != nil {
		t.Fatalf("Load: %v", err)
	}
	if _, err := e.Start(ctx, "pf1", 0); err != nil {
		t.Fatalf("Start: %v", err)
	}
	waitForPosition(t, e, "pf1", 200*time.Millisecond, 5*time.Second)

	count := countQueueSrcBuffers(t, e, "pf1")

	// Sanity: the probe itself must observe real flow before Pause, or a
	// zero count after Pause would prove nothing.
	time.Sleep(300 * time.Millisecond)
	beforePause := count()
	if beforePause == 0 {
		t.Fatalf("buffer probe observed zero buffers while pf1 was playing; probe is not wired to real flow")
	}

	pauseObs, err := e.Pause(ctx, "pf1")
	if err != nil {
		t.Fatalf("Pause: %v", err)
	}
	if pauseObs.State != pkgaudio.StatePaused {
		t.Fatalf("after Pause: state = %q, want paused", pauseObs.State)
	}
	frozenAt := pauseObs.Position

	// The block sits upstream of queue (see blockFlow's doc comment), so
	// up to one queue's worth of already-buffered content, bounded by
	// queueMaxSizeTime, still drains out its src pad immediately after
	// Pause. Settle past that bound before taking the baseline the zero
	// window is measured from; the drain itself is not the defect this
	// test is proving.
	time.Sleep(4 * queueMaxSizeTime)
	atPause := count()
	livePosBaseline, ok := queryBranchLivePosition(t, e, "pf1")
	if !ok {
		t.Fatalf("live position query failed while pf1 was paused")
	}

	// Sample across a settle window, not at one instant: sustained zero
	// flow across a multi-second hold is the evidence, not a single read.
	const holdWindow = 2 * time.Second
	deadline := time.Now().Add(holdWindow)
	for time.Now().Before(deadline) {
		time.Sleep(200 * time.Millisecond)
		if got := count(); got != atPause {
			t.Fatalf("paused branch's queue src pad received a buffer during the hold: count went from %d to %d", atPause, got)
		}
		livePos, ok := queryBranchLivePosition(t, e, "pf1")
		if !ok {
			t.Fatalf("live position query failed while pf1 was paused")
		}
		if livePos != livePosBaseline {
			t.Fatalf("paused branch's own live GStreamer position moved after settling: was %s, now %s (frozen bookmark says %s)", livePosBaseline, livePos, frozenAt)
		}
		obs, err := e.Observe(ctx, "pf1")
		if err != nil {
			t.Fatalf("Observe: %v", err)
		}
		if obs.Position != frozenAt {
			t.Fatalf("paused branch position moved: was %s, now %s", frozenAt, obs.Position)
		}
	}

	// Resume must let flow resume. A joined branch's own Resume never
	// flush-seeks it in place (see doc.go / methods.go's swapToPosition):
	// "pf1" now names a freshly built replacement branch with its own
	// queue, so the probe that proved silence above cannot observe it:
	// re-arm it against whatever branch the handle resolves to now.
	if _, err := e.Resume(ctx, "pf1"); err != nil {
		t.Fatalf("Resume: %v", err)
	}
	resumedCount := countQueueSrcBuffers(t, e, "pf1")
	deadline = time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) && resumedCount() == 0 {
		time.Sleep(50 * time.Millisecond)
	}
	if resumedCount() == 0 {
		t.Fatalf("no buffers flowed on the resumed branch's queue src pad within 2s of Resume")
	}

	_ = e.Release(context.Background(), "pf1")
}

// TestStoppedBranchStopsBufferFlow is TestPausedBranchStopsBufferFlow's
// counterpart for Stop: the defect this file proves describes Pause and
// Stop as the same bug (Manager.Stop was only saved from it by always
// calling Release immediately afterwards), so Stop needs its own direct
// flow evidence rather than relying on Pause's coverage of the shared
// blockFlow mechanism.
func TestStoppedBranchStopsBufferFlow(t *testing.T) {
	e := newTestEngine(t)
	dir := t.TempDir()
	wav := filepath.Join(dir, "fixture.wav")
	generateWAV(t, wav, 5)

	ctx, cancel := context.WithTimeout(context.Background(), engineOpTimeout)
	defer cancel()

	if _, err := e.Load(ctx, "sf1", mediaRef(wav), 5*time.Second); err != nil {
		t.Fatalf("Load: %v", err)
	}
	if _, err := e.Start(ctx, "sf1", 0); err != nil {
		t.Fatalf("Start: %v", err)
	}
	waitForPosition(t, e, "sf1", 200*time.Millisecond, 5*time.Second)

	count := countQueueSrcBuffers(t, e, "sf1")
	time.Sleep(300 * time.Millisecond)
	if count() == 0 {
		t.Fatalf("buffer probe observed zero buffers while sf1 was playing; probe is not wired to real flow")
	}

	stopObs, err := e.Stop(ctx, "sf1")
	if err != nil {
		t.Fatalf("Stop: %v", err)
	}
	if stopObs.State != pkgaudio.StateStopped {
		t.Fatalf("after Stop: state = %q, want stopped", stopObs.State)
	}
	frozenAt := stopObs.Position

	time.Sleep(4 * queueMaxSizeTime)
	atStop := count()

	const holdWindow = 2 * time.Second
	deadline := time.Now().Add(holdWindow)
	for time.Now().Before(deadline) {
		time.Sleep(200 * time.Millisecond)
		if got := count(); got != atStop {
			t.Fatalf("stopped branch's queue src pad received a buffer during the hold: count went from %d to %d", atStop, got)
		}
		obs, err := e.Observe(ctx, "sf1")
		if err != nil {
			t.Fatalf("Observe: %v", err)
		}
		if obs.Position != frozenAt {
			t.Fatalf("stopped branch position moved: was %s, now %s", frozenAt, obs.Position)
		}
		if obs.State != pkgaudio.StateStopped {
			t.Fatalf("stopped branch state changed to %q", obs.State)
		}
	}

	_ = e.Release(context.Background(), "sf1")
}

// queryBranchLivePosition bypasses the branch's own frozen bookmark and reads
// the volume element's position directly, the same live GStreamer query
// queryPosition uses when not frozen. Proving the underlying decode
// position itself has not moved is stronger evidence than proving the
// bookkeeping variable reads unchanged.
func queryBranchLivePosition(t *testing.T, e *Engine, handle string) (time.Duration, bool) {
	t.Helper()
	b, err := e.branchFor(agentaudio.EngineHandle(handle))
	if err != nil {
		t.Fatalf("branchFor %q: %v", handle, err)
	}
	ns, ok := b.volume.QueryPosition(gst.FormatTime)
	if !ok || ns < 0 {
		return 0, false
	}
	return time.Duration(ns), true
}

// TestPausedBranchDoesNotReachEOS proves a branch paused well before its
// file's natural end never reports Completed while held past that point,
// the defect's third measurement: a paused session completing itself with
// nobody having resumed it.
func TestPausedBranchDoesNotReachEOS(t *testing.T) {
	e := newTestEngine(t)
	dir := t.TempDir()
	wav := filepath.Join(dir, "short.wav")
	generateWAV(t, wav, 1.2)

	ctx, cancel := context.WithTimeout(context.Background(), engineOpTimeout)
	defer cancel()

	if _, err := e.Load(ctx, "pe1", mediaRef(wav), 1200*time.Millisecond); err != nil {
		t.Fatalf("Load: %v", err)
	}
	if _, err := e.Start(ctx, "pe1", 0); err != nil {
		t.Fatalf("Start: %v", err)
	}
	waitForPosition(t, e, "pe1", 400*time.Millisecond, 5*time.Second)

	pauseObs, err := e.Pause(ctx, "pe1")
	if err != nil {
		t.Fatalf("Pause: %v", err)
	}
	if pauseObs.State != pkgaudio.StatePaused {
		t.Fatalf("after Pause: state = %q, want paused", pauseObs.State)
	}
	frozenAt := pauseObs.Position

	// Hold well past the file's total duration (1.2s) measured from wall
	// clock, which is exactly the scenario the defect describes: a branch
	// paused at 0.5s reaching EOS at wall-clock 4.0s for a 4s file.
	const hold = 2500 * time.Millisecond
	deadline := time.Now().Add(hold)
	for time.Now().Before(deadline) {
		time.Sleep(200 * time.Millisecond)
		obs, err := e.Observe(ctx, "pe1")
		if err != nil {
			t.Fatalf("Observe: %v", err)
		}
		if obs.State == pkgaudio.StateCompleted {
			t.Fatalf("paused branch reached Completed on its own, %s into a %s hold; nobody resumed it", time.Since(deadline.Add(-hold)), hold)
		}
		if obs.State != pkgaudio.StatePaused {
			t.Fatalf("paused branch state changed to %q while held", obs.State)
		}
		if obs.Position != frozenAt {
			t.Fatalf("paused branch position moved from %s to %s while held", frozenAt, obs.Position)
		}
	}

	_ = e.Release(context.Background(), "pe1")
}

// TestResumeDoesNotDiscardTheHeldDuration proves Resume from a
// multi-second Pause plays out the remainder of the file in real time
// rather than dropping the held duration: GstAudioAggregator keeps
// advancing its own output clock through the hold, so an offset-only
// re-anchor lands post-hold buffers in its past and it silently
// discards them, reaching EOS far sooner than the remaining file's own
// length. Resume's flushing seek gives the branch a fresh segment the
// aggregator accepts instead, so EOS should land close to when the
// remaining audio actually ends.
func TestResumeDoesNotDiscardTheHeldDuration(t *testing.T) {
	const fileDuration = 10 * time.Second
	const holdDuration = 3 * time.Second

	e := newTestEngine(t)
	dir := t.TempDir()
	wav := filepath.Join(dir, "fixture.wav")
	generateWAV(t, wav, fileDuration.Seconds())

	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	if _, err := e.Load(ctx, "rc1", mediaRef(wav), fileDuration); err != nil {
		t.Fatalf("Load: %v", err)
	}
	if _, err := e.Start(ctx, "rc1", 0); err != nil {
		t.Fatalf("Start: %v", err)
	}
	waitForPosition(t, e, "rc1", 400*time.Millisecond, 5*time.Second)

	pauseObs, err := e.Pause(ctx, "rc1")
	if err != nil {
		t.Fatalf("Pause: %v", err)
	}
	pausedAt := pauseObs.Position

	time.Sleep(holdDuration)

	resumedAt := time.Now()
	resumeObs, err := e.Resume(ctx, "rc1")
	if err != nil {
		t.Fatalf("Resume: %v", err)
	}
	if resumeObs.Position < pausedAt-100*time.Millisecond || resumeObs.Position > pausedAt+100*time.Millisecond {
		t.Fatalf("position at Resume = %s, want close to the paused position %s (no drift while held)", resumeObs.Position, pausedAt)
	}

	remaining := fileDuration - resumeObs.Position
	deadline := time.Now().Add(45 * time.Second)
	var last agentaudio.EngineObservation
	for time.Now().Before(deadline) {
		obs, err := e.Observe(ctx, "rc1")
		if err != nil {
			t.Fatalf("Observe: %v", err)
		}
		last = obs
		if obs.State == pkgaudio.StateCompleted {
			break
		}
		time.Sleep(50 * time.Millisecond)
	}
	if last.State != pkgaudio.StateCompleted {
		t.Fatalf("branch never reached Completed after Resume; last observed state %q position %s", last.State, last.Position)
	}
	elapsed := time.Since(resumedAt)

	t.Logf("paused at %s, resumed at %s, %s of file remained; EOS reached %s of wall clock after Resume",
		pausedAt, resumeObs.Position, remaining, elapsed)

	// A generous tolerance for real-time playback plus scheduling jitter,
	// never a fraction of remaining: the discard defect this proves
	// compressed roughly 9.5s of remaining file into roughly 6.8s of wall
	// clock, a ratio no jitter tolerance this wide would let through. It
	// is not tight enough to catch the much smaller per-resume seam loss
	// TestResumeSeekLossAcrossManyResumes measures and bounds instead.
	minElapsed := remaining - 500*time.Millisecond
	maxElapsed := remaining + 3*time.Second
	if elapsed < minElapsed || elapsed > maxElapsed {
		t.Fatalf("AUDIO DISCARDED: %s of file remained but EOS reached %s of wall clock after Resume, want close to %s",
			remaining, elapsed, remaining)
	}

	_ = e.Release(context.Background(), "rc1")
}

// TestStartAfterStopResumesFlow proves Start clears any flow block a
// prior Stop left behind. Start's own contract promises playback, not
// only that it requires a Resume first, so a Start that reports Playing
// while producing nothing would be exactly the kind of stale claim this
// package must not make.
func TestStartAfterStopResumesFlow(t *testing.T) {
	e := newTestEngine(t)
	dir := t.TempDir()
	wav := filepath.Join(dir, "fixture.wav")
	generateWAV(t, wav, 5)

	ctx, cancel := context.WithTimeout(context.Background(), engineOpTimeout)
	defer cancel()

	if _, err := e.Load(ctx, "sr1", mediaRef(wav), 5*time.Second); err != nil {
		t.Fatalf("Load: %v", err)
	}
	if _, err := e.Start(ctx, "sr1", 0); err != nil {
		t.Fatalf("Start: %v", err)
	}
	waitForPosition(t, e, "sr1", 200*time.Millisecond, 5*time.Second)

	if _, err := e.Stop(ctx, "sr1"); err != nil {
		t.Fatalf("Stop: %v", err)
	}

	startObs, err := e.Start(ctx, "sr1", 0)
	if err != nil {
		t.Fatalf("Start after Stop: %v", err)
	}
	if startObs.State != pkgaudio.StatePlaying {
		t.Fatalf("after Start following Stop: state = %q, want playing", startObs.State)
	}

	count := countQueueSrcBuffers(t, e, "sr1")
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) && count() == 0 {
		time.Sleep(50 * time.Millisecond)
	}
	t.Logf("after Start-following-Stop: state=%s pos=%s buffers=%d", startObs.State, startObs.Position, count())
	if count() == 0 {
		t.Fatalf("SILENT RESTART: Start after Stop reported success but no buffers flowed in 3s")
	}

	_ = e.Release(context.Background(), "sr1")
}

// TestBlockFlowNoopsOnceReleased proves blockFlow refuses to install a
// probe on a branch teardown has already claimed, closing the race where
// a concurrent Pause or Stop lands between teardown marking the branch
// released and its own unblockFlow call: without this guard, a probe
// installed into that window is never released, and the state change
// that follows waits forever on a streaming thread parked inside it.
func TestBlockFlowNoopsOnceReleased(t *testing.T) {
	e := newTestEngine(t)
	dir := t.TempDir()
	wav := filepath.Join(dir, "fixture.wav")
	generateWAV(t, wav, 2)

	ctx, cancel := context.WithTimeout(context.Background(), engineOpTimeout)
	defer cancel()

	if _, err := e.Load(ctx, "bf1", mediaRef(wav), 2*time.Second); err != nil {
		t.Fatalf("Load: %v", err)
	}
	b, err := e.branchFor("bf1")
	if err != nil {
		t.Fatalf("branchFor: %v", err)
	}

	// Simulate the exact window teardown opens: released is already
	// true, but nothing has removed a block yet.
	b.mu.Lock()
	b.released = true
	b.mu.Unlock()

	b.blockFlow()

	b.mu.Lock()
	id := b.blockProbeID
	b.mu.Unlock()
	if id != 0 {
		t.Fatalf("blockFlow installed a probe (id=%d) on a branch already marked released", id)
	}

	// released was flipped directly rather than through a real teardown,
	// so the engine still owns this branch's elements; release it for
	// real so Close does not also have to.
	b.mu.Lock()
	b.released = false
	b.mu.Unlock()
	_ = e.Release(context.Background(), "bf1")
}

// resumeContentSkipTolerance bounds how much program material a single
// resume may skip before TestResumeSeekLossAcrossManyResumes fails it: one
// generateWAV buffer period (1024 samples at 44100Hz) plus margin for
// scheduler jitter in the settle poll that finds where a branch's queue
// actually finished draining. Justified by this package's own measured
// after-numbers (see the PR this constant landed with), not a guess: real
// resumes measured well under this on the same VM the seam-loss test runs
// on. var, not const: shrunk by a test exercising the bound itself.
var resumeContentSkipTolerance = 30 * time.Millisecond

// TestResumeSeekLossAcrossManyResumes measures two different things across
// repeated pause/resume cycles and must not conflate them:
//
//  1. Content skip: does Resume ever start playing later in the file than
//     where the paused branch's own queue actually stopped draining? This
//     is asserted on directly, in stream position, using trackQueueSrcPosition
//     (the branch's contribution to the mix, downstream of decode) and
//     heldPosition (the post-seek position the replacement branch actually
//     holds, read race-free once prepare confirms it). Aggregator discards
//     are verified separately with an external GST_DEBUG=audioaggregator:6
//     capture (see the PR body): go-gst has no binding to remove the
//     default stderr log function, so raising the category threshold
//     in-process to watch for discards here would flood test output with
//     every other message that category logs.
//  2. Wall-clock SEAM LOSS: this is left as a logged, non-asserted number.
//     It also counts the wall time a paused branch's queue spends audibly
//     draining its own already-buffered content into the explicit hold
//     window (see blockFlow's doc comment: that drain is intended, not a
//     defect), so it is not a measurement of skipped content and must never
//     be asserted on as if it were.
func TestResumeSeekLossAcrossManyResumes(t *testing.T) {
	const fileDuration = 8 * time.Second
	const numCycles = 6
	const playSlice = 700 * time.Millisecond
	const holdPerCycle = 300 * time.Millisecond
	// settleWindow must exceed one buffer period (generateWAV's fixture
	// produces buffers roughly 40ms apart at this queue's steady drain
	// rate) or the poll below mistakes the gap between two draining
	// buffers for the drain having actually finished.
	const settleWindow = 80 * time.Millisecond

	e := newTestEngine(t)
	dir := t.TempDir()

	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	var skips []time.Duration

	runToEOS := func(handle string, wav string, cycle bool) time.Duration {
		if _, err := e.Load(ctx, agentaudio.EngineHandle(handle), mediaRef(wav), fileDuration); err != nil {
			t.Fatalf("Load %s: %v", handle, err)
		}
		start := time.Now()
		if _, err := e.Start(ctx, agentaudio.EngineHandle(handle), 0); err != nil {
			t.Fatalf("Start %s: %v", handle, err)
		}
		if cycle {
			latest := trackQueueSrcPosition(t, e, handle)
			for i := 0; i < numCycles; i++ {
				time.Sleep(playSlice)
				pauseTime := time.Now()
				if _, err := e.Pause(ctx, agentaudio.EngineHandle(handle)); err != nil {
					t.Fatalf("Pause %s cycle %d: %v", handle, i, err)
				}
				settleTimeout := holdPerCycle - 10*time.Millisecond
				if settleTimeout < 0 {
					settleTimeout = 0
				}
				lastBeforePTS, ok := settlePosition(latest, settleTimeout, settleWindow)
				if !ok {
					t.Fatalf("cycle %d: queue src position never settled after Pause", i)
				}
				if remaining := holdPerCycle - time.Since(pauseTime); remaining > 0 {
					time.Sleep(remaining)
				}
				if _, err := e.Resume(ctx, agentaudio.EngineHandle(handle)); err != nil {
					t.Fatalf("Resume %s cycle %d: %v", handle, i, err)
				}
				nb, err := e.branchFor(agentaudio.EngineHandle(handle))
				if err != nil {
					t.Fatalf("branchFor %s after resume cycle %d: %v", handle, i, err)
				}
				firstAfterPTS := nb.heldPosition()
				skip := firstAfterPTS - lastBeforePTS
				skips = append(skips, skip)
				t.Logf("cycle %d: last content before pause=%s, first content after resume=%s, skip=%s", i, lastBeforePTS, firstAfterPTS, skip)
				latest = trackQueueSrcPosition(t, e, handle)
			}
		}
		deadline := time.Now().Add(45 * time.Second)
		for time.Now().Before(deadline) {
			obs, err := e.Observe(ctx, agentaudio.EngineHandle(handle))
			if err != nil {
				t.Fatalf("Observe %s: %v", handle, err)
			}
			if obs.State == pkgaudio.StateCompleted {
				break
			}
			time.Sleep(20 * time.Millisecond)
		}
		wall := time.Since(start)
		_ = e.Release(context.Background(), agentaudio.EngineHandle(handle))
		return wall
	}

	controlWAV := filepath.Join(dir, "control.wav")
	generateWAV(t, controlWAV, fileDuration.Seconds())
	controlWall := runToEOS("seam-control", controlWAV, false)

	cycledWAV := filepath.Join(dir, "cycled.wav")
	generateWAV(t, cycledWAV, fileDuration.Seconds())
	cycledWall := runToEOS("seam-cycled", cycledWAV, true)

	heldTotal := numCycles * holdPerCycle
	cycledActive := cycledWall - heldTotal
	loss := controlWall - cycledActive
	perResume := loss / numCycles

	t.Logf("control (no pause): wall to EOS = %s", controlWall)
	t.Logf("%d pause/resume cycles: wall to EOS excluding %s held = %s", numCycles, heldTotal, cycledActive)
	// SEAM LOSS also counts intended post-Pause queue drain time inside the
	// explicit hold window (see this test's own doc comment); it is logged
	// for historical comparability only and is never asserted on here.
	t.Logf("SEAM LOSS: %s over %d cycles = %s per resume", loss, numCycles, perResume)

	var maxSkip time.Duration
	var totalSkip time.Duration
	for i, s := range skips {
		if s > maxSkip {
			maxSkip = s
		}
		totalSkip += s
		if s < 0 {
			t.Errorf("cycle %d: resume started %s before the paused branch's own last content, a repeat rather than a skip", i, -s)
		}
	}
	t.Logf("content skip: max=%s total=%s over %d resumes", maxSkip, totalSkip, len(skips))
	if maxSkip > resumeContentSkipTolerance {
		t.Errorf("resume skipped %s of program content, want at most %s", maxSkip, resumeContentSkipTolerance)
	}
}
