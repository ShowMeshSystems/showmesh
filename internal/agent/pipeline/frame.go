package pipeline

import (
	"context"
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

	// idleOutput is which buffer to draw for [idleContentStates]. Always
	// "black" today: render.settings' idleOutput field exists coordinator-
	// side (ADR-039) but nothing distributes it to a node yet (build
	// contract ruling 4 — there is no config-push path to a node beyond a
	// render.surface.apply assignment), so "hold" and "diagnostic" are not
	// implemented and this is hardcoded rather than pretending it is wired.
	idleOutput string

	// buf and idleBuf are reused every frame (never reallocated on the hot
	// path) — see build contract's "avoid an allocation per frame" rule.
	// idleBuf is all-zero and never written to after construction; buf is
	// overwritten by every successful ChannelRange call.
	buf     []byte
	idleBuf []byte

	written, late, dropped int64

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
func NewFrameWriter(sup *Supervisor, surfaceID string, source FrameSource, timeline TimelineSource, channelStart, channelCount int, logger Logger) (*FrameWriter, error) {
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

	fw := &FrameWriter{
		surfaceID:    surfaceID,
		sup:          sup,
		logger:       logger,
		source:       source,
		timeline:     timeline,
		channelStart: channelStart,
		channelCount: channelCount,
		stepTime:     stepTime,
		idleOutput:   "black",
		buf:          make([]byte, channelCount),
		idleBuf:      make([]byte, channelCount), // zero-valued: black for rgb/rgbw alike
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
	return fw.written, fw.late, fw.dropped
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
		outBuf = fw.idleBuf
	} else {
		frameIdx := fw.frameIndexFor(snap.PositionMS)
		if err := fw.source.ChannelRange(frameIdx, fw.channelStart, fw.channelCount, fw.buf); err != nil {
			fw.dropped++
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
		fw.dropped++
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
		fw.dropped++
		if !fw.loggedWriteErr {
			fw.loggedWriteErr = true
			fw.logger.Warn("frame writer: stdin write failed; pipeline process likely restarting",
				"surface_id", fw.surfaceID, "error", werr, "bytes_written", n, "bytes_wanted", len(outBuf))
		}
		fw.reportCounts()
		return
	}
	fw.loggedWriteErr = false
	fw.written++

	if elapsed := time.Since(start); elapsed > fw.stepTime {
		fw.late++
	}

	fw.reportCounts()
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
	fw.sup.SetFrameCounts(fw.surfaceID, fw.written, fw.late, fw.dropped)
}

// Compile-time check that *fseq.File satisfies FrameSource.
var _ FrameSource = (*fseq.File)(nil)

// Compile-time check that *multisync.Timeline satisfies TimelineSource.
var _ TimelineSource = (*multisync.Timeline)(nil)
