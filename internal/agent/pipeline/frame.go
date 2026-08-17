package pipeline

import (
	"context"
	"sync/atomic"
	"time"

	"github.com/showmeshsystems/showmesh/pkg/fseq"
	"github.com/showmeshsystems/showmesh/pkg/multisync"
)

// FrameSource is the subset of *fseq.File the frame writer needs. An
// interface so a test can supply a fake without building a real FSEQ file
// on disk for every test case; [*fseq.File] satisfies it directly.
type FrameSource interface {
	FrameCount() int
	StepTimeMS() byte
	ChannelRange(frame, start, count int, dst []byte) error
}

// TimelineSource is the subset of *multisync.Timeline the frame writer
// needs — an interface for the same fake-without-real-network-I/O reason as
// [FrameSource].
type TimelineSource interface {
	Snapshot() multisync.Snapshot
}

// idleContentStates is the set of [multisync.State] values for which the
// frame writer draws the idle output rather than content — build contract
// ruling 3's table. Playing, Unsynchronized, and Stopping are deliberately
// NOT here: Stopping renders the timeline's own frozen last position (see
// this file's package doc comment on why that needs no special case), and
// Unsynchronized is a confidence statement about the estimate, never a
// playback halt.
var idleContentStates = map[multisync.State]bool{
	multisync.StateOpened:  true,
	multisync.StateStopped: true,
	multisync.StateUnknown: true,
}

// The three values render.settings.idleOutput can carry (build contract
// ruling 3), independently reproduced here rather than importing
// internal/coordinator/config — the same each-side-of-a-wire-boundary-
// decodes-independently convention renderApplyKnownKeys' own doc comment
// (internal/agent/renderops.go) already applies for this exact payload.
const (
	IdleOutputBlack      = "black"
	IdleOutputHold       = "hold"
	IdleOutputDiagnostic = "diagnostic"
)

// diagnosticFillHigh and diagnosticFillLow are the two constant byte values
// [FrameWriter] alternates between for [IdleOutputDiagnostic]. Both
// non-zero (never equal to black) and distinct from each other, so the
// output is visibly not the all-zero idle buffer and visibly changes over
// time — the owner's ruling that a diagnostic output must be GENERATED and
// never a frozen frame, satisfied by picking between two precomputed
// buffers rather than freezing whatever content last played.
const (
	diagnosticFillHigh byte = 0xC0
	diagnosticFillLow  byte = 0x30
)

// diagnosticBlinkPeriod is how long [FrameWriter] holds each of the two
// diagnostic fill values before switching — long enough to read as
// deliberate rather than flicker, short enough that a bystander watching
// for more than a couple of seconds sees it change.
const diagnosticBlinkPeriod = time.Second

// FrameWriter extracts one surface's channel range from a local FSEQ file,
// frame by frame, at the position [multisync.Timeline] reports, and writes
// each frame's raw buffer to the supervised pipeline's stdin. One
// FrameWriter per surface assignment, for the life of that assignment (a
// fresh render.surface.apply replaces it with a new one — see
// internal/agent/renderops.go).
//
// FrameWriter never starts, stops, or restarts the pipeline process itself
// (build contract ruling 3: sync loss changes what is drawn, never the
// sender). A write failure — the pipeline process having died — is counted
// as a dropped frame and left for [Supervisor]'s own crash detection (via
// the process's Wait) to notice and restart; the very next tick simply
// tries again, picking up whatever process is current then.
type FrameWriter struct {
	surfaceID string
	sup       *Supervisor
	logger    Logger

	source   FrameSource
	timeline TimelineSource

	// channelStart is 0-BASED, already converted from the operator-facing
	// 1-based show.surface.channelRange.startChannel — see
	// internal/agent/renderops.go's buildFSEQAssignment, the one place that
	// conversion happens (RES-017's own rule: do not scatter "-1").
	channelStart int
	channelCount int

	stepTime time.Duration

	// idleOutput is which buffer to draw for [idleContentStates]: one of
	// [IdleOutputBlack] (default), [IdleOutputHold], or
	// [IdleOutputDiagnostic]. Carried from the coordinator on the
	// render.surface.apply assignment (build contract ruling 4) — see
	// internal/agent/renderops.go's parseIdleOutput.
	idleOutput string

	// buf, idleBuf, diagBufHigh, and diagBufLow are all reused every frame
	// (never (re)allocated on the hot path) — see build contract's "avoid
	// an allocation per frame" rule. idleBuf is all-zero and never written
	// to after construction. buf is overwritten by every successful
	// ChannelRange call and, deliberately, NOT overwritten while idle —
	// which is what lets [IdleOutputHold] draw it directly as "the last
	// successfully extracted content frame" with no separate hold buffer
	// and no copy. diagBufHigh/diagBufLow are filled once at construction
	// with distinct constant values and picked between by wall-clock time
	// (see idleOutputFor) — a fill, never a render (ADR-040).
	buf         []byte
	idleBuf     []byte
	diagBufHigh []byte
	diagBufLow  []byte

	// Atomic because Counts reads them from a caller's goroutine while
	// writeOneFrame is incrementing them on the frame loop's.
	written, late, dropped atomic.Int64

	// rateWindowStart and rateWindowWritten anchor the achieved-rate
	// measurement (see writeOneFrame's rate-sampling block): the wall-clock
	// time and written-count at the start of the current sampling window.
	// currentRate is the most recently completed window's frames/second,
	// nil until one full window has elapsed. All three are touched only
	// from writeOneFrame, which runs exclusively on Run's own goroutine —
	// reportCounts, called at the end of the same tick, reads currentRate
	// on that same goroutine, so nothing here needs a lock or an atomic.
	rateWindowStart   time.Time
	rateWindowWritten int64
	currentRate       *float64

	// loggedRangeErrOnce and loggedWriteErrOnce keep this loop from log-
	// spamming at up to 40Hz when a condition (e.g. the process is down, or
	// the assigned range is not covered by this frame) is persistent —
	// counted every tick regardless, logged once until it clears.
	loggedRangeErr bool
	loggedWriteErr bool

	stop chan struct{}
	done chan struct{}
}

// NewFrameWriter constructs a FrameWriter and validates, once, that
// channelStart/channelCount is actually covered by source at frame 0 —
// rather than discovering a coverage gap only after 40 silent failed
// frames, this fails the assignment up front with the real error (build
// contract: "refuse, with the numbers stated, when the surface's channel
// range is not fully covered"). It does not start the writer; call Run.
//
// idleOutput selects the surface's idle behaviour (build contract ruling
// 3). Any value other than [IdleOutputHold] or [IdleOutputDiagnostic] —
// including "" — is treated as [IdleOutputBlack], the safe default:
// callers that resolve a concrete value (internal/agent/renderops.go)
// already validate it against the known set before reaching here, so this
// is defense in depth, never the place that rule is enforced.
func NewFrameWriter(sup *Supervisor, surfaceID string, source FrameSource, timeline TimelineSource, channelStart, channelCount int, idleOutput string, logger Logger) (*FrameWriter, error) {
	probe := make([]byte, channelCount)
	if source.FrameCount() > 0 {
		if err := source.ChannelRange(0, channelStart, channelCount, probe); err != nil {
			return nil, err
		}
	}

	stepTime := time.Duration(source.StepTimeMS()) * time.Millisecond
	if stepTime <= 0 {
		stepTime = multisync.DefaultStepTime
	}

	if idleOutput != IdleOutputHold && idleOutput != IdleOutputDiagnostic {
		idleOutput = IdleOutputBlack
	}

	diagHigh := make([]byte, channelCount)
	diagLow := make([]byte, channelCount)
	for i := range diagHigh {
		diagHigh[i] = diagnosticFillHigh
		diagLow[i] = diagnosticFillLow
	}

	fw := &FrameWriter{
		surfaceID:    surfaceID,
		sup:          sup,
		logger:       logger,
		source:       source,
		timeline:     timeline,
		channelStart: channelStart,
		channelCount: channelCount,
		stepTime:     stepTime,
		idleOutput:   idleOutput,
		buf:          make([]byte, channelCount),
		idleBuf:      make([]byte, channelCount), // zero-valued: black for rgb/rgbw alike
		diagBufHigh:  diagHigh,
		diagBufLow:   diagLow,
		stop:         make(chan struct{}),
		done:         make(chan struct{}),
	}
	return fw, nil
}

// Run drives the frame loop on the calling goroutine until Stop is called
// or ctx is done. Intended to be started with `go fw.Run(ctx)`.
func (fw *FrameWriter) Run(ctx context.Context) {
	defer close(fw.done)

	ticker := time.NewTicker(fw.stepTime)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case <-fw.stop:
			return
		case tick := <-ticker.C:
			fw.writeOneFrame(tick)
		}
	}
}

// Stop signals Run to return and blocks until it has. Safe to call more
// than once? No — matching [Supervisor.Shutdown]'s single-close-per-runner
// convention, callers own calling this exactly once per FrameWriter.
func (fw *FrameWriter) Stop() {
	close(fw.stop)
	<-fw.done
}

// Counts returns the writer's cumulative written/late/dropped counts.
func (fw *FrameWriter) Counts() (written, late, dropped int64) {
	return fw.written.Load(), fw.late.Load(), fw.dropped.Load()
}

// writeOneFrame is one tick's worth of work: sample the timeline, select
// content or idle output, and write it to the pipeline's stdin. Lateness is
// measured as this call itself taking longer than one frame period —
// extraction plus write should be a small fraction of stepTime; a value at
// or beyond it means this tick's frame missed its slot.
func (fw *FrameWriter) writeOneFrame(tickTime time.Time) {
	start := time.Now()

	snap := fw.timeline.Snapshot()

	var outBuf []byte
	if idleContentStates[snap.State] {
		outBuf = fw.idleOutputFor(tickTime)
	} else {
		frameIdx := fw.frameIndexFor(snap.PositionMS)
		if err := fw.source.ChannelRange(frameIdx, fw.channelStart, fw.channelCount, fw.buf); err != nil {
			fw.dropped.Add(1)
			if !fw.loggedRangeErr {
				fw.loggedRangeErr = true
				fw.logger.Warn("frame writer: channel range extraction failed; drawing idle output until this recovers",
					"surface_id", fw.surfaceID, "frame", frameIdx, "error", err)
			}
			outBuf = fw.idleBuf
		} else {
			fw.loggedRangeErr = false
			outBuf = fw.buf
		}
	}

	w, err := fw.sup.Stdin(fw.surfaceID)
	if err != nil {
		fw.dropped.Add(1)
		fw.reportCounts()
		return
	}

	n, werr := w.Write(outBuf)
	if werr != nil || n != len(outBuf) {
		// A short write or a write error means the pipeline process is
		// gone or its stdin pipe is closed. This is handed to the
		// supervisor only in the sense that the supervisor's own exit
		// detection (watching the process's Wait) independently notices
		// the same death and restarts per its policy — this loop never
		// calls Restart/Clear itself (ruling 3). The next tick simply
		// tries again against whatever process is current then.
		fw.dropped.Add(1)
		if !fw.loggedWriteErr {
			fw.loggedWriteErr = true
			fw.logger.Warn("frame writer: stdin write failed; pipeline process likely restarting",
				"surface_id", fw.surfaceID, "error", werr, "bytes_written", n, "bytes_wanted", len(outBuf))
		}
		fw.reportCounts()
		return
	}
	fw.loggedWriteErr = false
	written := fw.written.Add(1)

	if elapsed := time.Since(start); elapsed > fw.stepTime {
		fw.late.Add(1)
	}

	fw.sampleRate(start, written)
	fw.reportCounts()
}

// idleOutputFor selects which precomputed buffer to draw for one idle
// tick, per fw.idleOutput — no allocation, no per-byte write, just a
// buffer reference chosen from what NewFrameWriter already built.
//
//   - [IdleOutputBlack]: idleBuf, all-zero.
//   - [IdleOutputHold]: buf, the last successfully extracted content frame
//     (buf is never touched while idle — see the FrameWriter struct's own
//     doc comment on buf).
//   - [IdleOutputDiagnostic]: diagBufHigh or diagBufLow, alternated by
//     wall-clock time so the output visibly changes rather than freezing.
func (fw *FrameWriter) idleOutputFor(tick time.Time) []byte {
	switch fw.idleOutput {
	case IdleOutputHold:
		return fw.buf
	case IdleOutputDiagnostic:
		if (tick.UnixNano()/int64(diagnosticBlinkPeriod))%2 == 0 {
			return fw.diagBufHigh
		}
		return fw.diagBufLow
	default:
		return fw.idleBuf
	}
}

// frameRateWindow bounds how long writeOneFrame accumulates successful
// writes before turning them into an achieved-frames/second measurement.
//
// SHOWMESH HYPOTHESIS, NOT MEASURED: no bench data exists for the right
// window length. Long enough that one slow frame does not dominate the
// average (at the reference ~40fps this is dozens of samples), short
// enough that a genuine sustained slowdown shows up on the dashboard within
// a few seconds rather than minutes.
const frameRateWindow = 5 * time.Second

// sampleRate updates the achieved-rate measurement from a successful
// write's own timestamp and cumulative written count. The first call after
// construction (or after a window closes) only anchors the window; a rate
// is reported starting from the window after that, computed strictly from
// wall-clock elapsed time and frames actually written in it — never from
// fw.stepTime or any other configured/target value, which is what keeps
// this an achieved measurement rather than the target echoed back.
func (fw *FrameWriter) sampleRate(now time.Time, written int64) {
	if fw.rateWindowStart.IsZero() {
		fw.rateWindowStart = now
		fw.rateWindowWritten = written
		return
	}
	elapsed := now.Sub(fw.rateWindowStart)
	if elapsed < frameRateWindow {
		return
	}
	frames := written - fw.rateWindowWritten
	rate := float64(frames) / elapsed.Seconds()
	fw.currentRate = &rate
	fw.rateWindowStart = now
	fw.rateWindowWritten = written
}

// frameIndexFor converts a timeline position (ms) to a frame index via
// this surface's own file's step time, clamped to [0, FrameCount()-1] —
// including holding the last frame once the file's own duration is
// exceeded (e.g. the master is playing a longer sequence, or free-running
// past this file's end), rather than erroring or wrapping.
//
// SHOWMESH HYPOTHESIS, NOT MEASURED: holding the last frame past end-of-
// file has no measured evidence behind it; it is a defensive choice (never
// crash, never index out of range) rather than a specified behaviour.
func (fw *FrameWriter) frameIndexFor(positionMS int64) int {
	frameCount := fw.source.FrameCount()
	if frameCount <= 0 {
		return 0
	}
	stepMS := fw.stepTime.Milliseconds()
	if stepMS <= 0 {
		stepMS = 1
	}
	idx := positionMS / stepMS
	if idx < 0 {
		idx = 0
	}
	if idx >= int64(frameCount) {
		idx = int64(frameCount) - 1
	}
	return int(idx)
}

func (fw *FrameWriter) reportCounts() {
	fw.sup.SetFrameCounts(fw.surfaceID, fw.written.Load(), fw.late.Load(), fw.dropped.Load(), fw.currentRate)
}

// Compile-time check that *fseq.File satisfies FrameSource.
var _ FrameSource = (*fseq.File)(nil)

// Compile-time check that *multisync.Timeline satisfies TimelineSource.
var _ TimelineSource = (*multisync.Timeline)(nil)
