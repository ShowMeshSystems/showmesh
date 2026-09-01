package pipeline

import (
	"context"
	"fmt"
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

// ShowModeSource answers, at the instant it is asked, whether this node
// must behave as Show Mode. ADR-036 decision 1: the answer is read AT THE
// POINT OF DECISION, never captured into a field at construction, so a mode
// flip changes what a running frame writer draws without anything tearing
// the writer down and rebuilding it.
//
// An interface, not internal/agent's holder type, because this package is
// imported by that one; internal/agent's ShowModeHolder satisfies it
// directly. A nil source behaves as Show Mode, which is the same answer
// ADR-033 decision 5 gives a node that has never been told the mode.
type ShowModeSource interface {
	BehavesAsShow() bool
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

// diagnosticBackgroundFill and diagnosticBarFill are the two constant byte
// values [IdleOutputDiagnostic] draws: a constant non-black field across
// the whole surface, and a single vertical bar swept across it. Both
// non-zero (never equal to black) and distinct from each other, so the bar
// reads against the background instead of vanishing into it. A prior
// version of this mode alternated the WHOLE surface between two flat grey
// fills once a second; on a dark wall in a dark yard that reads as "is the
// cable connected?" rather than as a diagnostic, because a viewer has to
// already be staring at the exact moment it changes to notice anything.
// The moving bar's own position is a clock a bystander can read against the
// pipeline's frame period, which a synchronized whole-field blink is not.
const (
	diagnosticBackgroundFill byte = 0x30
	diagnosticBarFill        byte = 0xFF
)

// alertPixelFill is what [FailureOutputAlert] paints on every pixel of a
// surface whose channel stride this writer can resolve: full red, every
// other channel of the pixel off. Red is the choice because no healthy
// state of this renderer ever produces it, and because the failure it
// reports is essentially only reachable while an operator is configuring
// the show: black would be indistinguishable from a correct idle, and a
// dead cable or an unpowered surface is also black, never red.
//
// On an RGBW pixel the fourth channel stays 0, so the white element is off
// and the pixel is red rather than a washed-out pink. On a surface whose
// channel count is not a whole number of pixels (resolveGeometry's
// degenerate case, where the stride is unknown) every byte is set to
// alertUnknownStrideFill instead, since "red" is not expressible without a
// stride: full-on is still maximally distant from the black a healthy idle
// draws, which is the property that matters.
var alertPixelFill = [4]byte{0xFF, 0x00, 0x00, 0x00}

// alertUnknownStrideFill is [FailureOutputAlert]'s fallback byte for a
// surface with no resolvable pixel stride. See [alertPixelFill].
const alertUnknownStrideFill byte = 0xFF

// diagnosticBarWidthDivisor sets [IdleOutputDiagnostic]'s moving bar width
// to the surface's own width divided by this, clamped to at least one
// pixel — wide enough to read on a large canvas-resolution surface, narrow
// enough on a small virtual-matrix surface that it stays a moving mark
// rather than a second flat field.
const diagnosticBarWidthDivisor = 32

// diagnosticPixelPeriod is how long [IdleOutputDiagnostic]'s bar holds each
// column before advancing exactly one pixel. RES-004/the build contract's
// B0 spike records the reference profile at 40 fps, a 25 ms frame period;
// one pixel per reference frame period makes the bar advance visibly on
// every tick at that rate without outrunning what a bystander can track by
// eye, and ties the bar's speed to the profile this project actually
// measured rather than to an arbitrary "looks nice" duration.
const diagnosticPixelPeriod = 25 * time.Millisecond

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

	// sequenceFilename is the bare runtime filename this writer opened
	// source from — buildFSEQAssignment's already-validated fseqFilename
	// (internal/agent/renderops.go), carried through unchanged. Compared
	// exact-string against the timeline's own reported filename
	// (multisync.Snapshot.Filename) on every content tick to catch FPP
	// having moved on to a sequence this writer was never told about; see
	// writeOneFrame. "" for a writer with no real sequence ([unknownTimeline]
	// always reports "" too, and is permanently an idle content state, so
	// the comparison never runs for those writers).
	//
	// The comparison itself is plain exact-string equality, not a path or
	// case normalization: internal/agent/cueactivationrender.go's
	// activateSurfaceRender already compares multisync.Snapshot.Filename
	// against a Cue's resolved runtime filename the same way (ADR-043
	// decision 6, "Filename is corroboration and mismatch evidence only"),
	// treating a match/mismatch as exact strings with no normalization.
	// This reuses that established comparison rather than inventing a new
	// one for this second call site.
	sequenceFilename string

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

	// showMode is read fresh on every failed extraction, never at
	// construction (ADR-036 decision 1); see [ShowModeSource]. nil means
	// this writer was built with no mode source at all and behaves as Show
	// Mode, the same conservative answer an unknown mode gets.
	showMode ShowModeSource

	// buf, idleBuf, and diagBuf are all reused every frame (never
	// (re)allocated on the hot path) — see build contract's "avoid an
	// allocation per frame" rule. idleBuf is all-zero and never written to
	// after construction. buf is overwritten by every successful
	// ChannelRange call and, deliberately, NOT overwritten while idle —
	// which is what lets [IdleOutputHold] draw it directly as "the last
	// successfully extracted content frame" with no separate hold buffer
	// and no copy. diagBuf is filled once at construction with the
	// background constant and then mutated IN PLACE, one bar column at a
	// time, by idleOutputFor — never reallocated and never rewritten in
	// full (see idleOutputFor's own comment). alertBuf is filled once at
	// construction with [FailureOutputAlert]'s pattern and never written
	// to again. Every byte diagBuf ever holds
	// is one of two constants written at a computed offset: a fill, never a
	// render, which is what keeps this on ShowMesh's side of ADR-040's line
	// (it locates and copies bytes; it never reads, scales, or blends one).
	buf      []byte
	idleBuf  []byte
	diagBuf  []byte
	alertBuf []byte

	// diagWidth, diagHeight, diagRowBytes, and diagBarWidthBytes are
	// [IdleOutputDiagnostic]'s geometry, resolved once at construction from
	// the surface's own width/height/channelCount (see resolveGeometry) and
	// never touched again. diagRowBytes is one row's stride in diagBuf;
	// diagBarWidthBytes is the moving bar's width in bytes. diagLastCol is
	// the column (in pixels) the bar currently occupies, -1 until the first
	// diagnostic tick has drawn one — touched only by idleOutputFor, which
	// (like currentRate below) runs exclusively on Run's own goroutine.
	diagWidth         int
	diagHeight        int
	diagRowBytes      int
	diagBarWidthBytes int
	diagBytesPerPixel int
	diagLastCol       int

	// Atomic because Counts reads them from a caller's goroutine while
	// writeOneFrame is incrementing them on the frame loop's.
	written, late, dropped atomic.Int64

	// rateWindowStart and rateWindowWritten anchor the achieved-rate
	// measurement (see writeOneFrame's rate-sampling block): the wall-clock
	// time and written-count at the start of the current sampling window.
	// currentRate is the most recently completed window's frames/second,
	// nil until one full window has elapsed. framesObservedAt is the
	// wall-clock instant that window closed at: the evidence timestamp
	// reported for FramesWritten/FramesLate/FramesDropped/FramesRate alike
	// (see sampleRate's doc comment for why one shared, window-close
	// stamp covers all four rather than each counter getting the instant
	// it happened to be read). All four are touched only from
	// writeOneFrame, which runs exclusively on Run's own goroutine,
	// same as reportCounts, called at the end of the same tick, reading
	// them on that same goroutine, so nothing here needs a lock or an atomic.
	rateWindowStart   time.Time
	rateWindowWritten int64
	currentRate       *float64
	framesObservedAt  time.Time

	// loggedRangeErrOnce, loggedWriteErrOnce, and loggedStale keep this loop
	// from log-spamming at up to 40Hz when a condition (e.g. the process is
	// down, the assigned range is not covered by this frame, or the
	// timeline is reporting a filename this writer never opened) is
	// persistent — counted every tick regardless, logged once until it
	// clears.
	loggedRangeErr bool
	loggedWriteErr bool
	loggedStale    bool

	// lastTickTime anchors finding 13's ticker-drop detection: Go's
	// time.Ticker silently drops ticks when the receiver falls behind
	// (never queues them), so a gap between consecutive tickTime values
	// wider than one stepTime means one or more ticks never arrived at all.
	// Touched only by writeOneFrame, which runs exclusively on Run's own
	// goroutine — same single-goroutine rule as currentRate below.
	lastTickTime time.Time

	// timelineState, timelinePositionMS, drawing, and idleModeNow are this
	// tick's draw evidence (finding 7 — see [Supervisor.SetDrawState]),
	// touched only by writeOneFrame for the same single-goroutine reason as
	// currentRate.
	timelineState      string
	timelinePositionMS *int64
	drawing            string
	idleModeNow        string
	failureOutputNow   string

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
// is defense in depth, never the place that rule is enforced. The
// coercion is LOGGED rather than silent: it changes what this surface
// draws for the whole life of the writer, and a surface quietly drawing
// black because one string upstream was wrong is the failure this seam
// exists to stop.
//
// showMode may be nil, which behaves as Show Mode; see [ShowModeSource].
//
// width and height are the surface's own show.surface.geometry — used only
// to give [IdleOutputDiagnostic]'s moving bar row/column coordinates (see
// resolveGeometry); every other idle mode and the content path ignore them
// entirely and keep working from channelStart/channelCount alone exactly as
// before.
//
// sequenceFilename is the bare runtime filename source was opened from —
// see [FrameWriter.sequenceFilename] for what it is compared against and
// why.
func NewFrameWriter(sup *Supervisor, surfaceID string, source FrameSource, timeline TimelineSource, sequenceFilename string, channelStart, channelCount, width, height int, idleOutput string, showMode ShowModeSource, logger Logger) (*FrameWriter, error) {
	// A source with no frames cannot cover any channel range, so the
	// construction-time probe below has nothing to prove and every tick
	// this writer would ever run is already known to fail. Refused here,
	// with the count stated, rather than started so it can discover the
	// same thing 40 times a second: skipping the probe for this case is
	// how a broken assignment used to reach the runtime fallback instead
	// of being refused up front.
	if source.FrameCount() <= 0 {
		return nil, fmt.Errorf("pipeline: surface %q: the assigned sequence has %d frames, so channels %d..%d can never be extracted from it",
			surfaceID, source.FrameCount(), channelStart+1, channelStart+channelCount)
	}
	probe := make([]byte, channelCount)
	if err := source.ChannelRange(0, channelStart, channelCount, probe); err != nil {
		return nil, err
	}

	stepTime := time.Duration(source.StepTimeMS()) * time.Millisecond
	if stepTime <= 0 {
		stepTime = multisync.DefaultStepTime
	}

	if idleOutput != IdleOutputHold && idleOutput != IdleOutputDiagnostic {
		if idleOutput != IdleOutputBlack {
			logger.Warn("frame writer: unrecognized idle output; this surface will draw black whenever it is idle",
				"surface_id", surfaceID, "requested_idle_output", idleOutput, "using", IdleOutputBlack)
		}
		idleOutput = IdleOutputBlack
	}

	return newFrameWriter(sup, surfaceID, source, timeline, sequenceFilename, channelStart, channelCount, width, height, stepTime, idleOutput, showMode, logger), nil
}

// unknownTimeline is the [TimelineSource] a writer with no timeline of its
// own reads: permanently [multisync.StateUnknown], which [idleContentStates]
// already treats as idle. A real value rather than a nil check on the frame
// loop, so writeOneFrame keeps one invariant (timeline is never nil) instead
// of branching on absence every tick.
type unknownTimeline struct{}

func (unknownTimeline) Snapshot() multisync.Snapshot {
	return multisync.Snapshot{State: multisync.StateUnknown}
}

// emptyFrameSource is the [FrameSource] a writer with no sequence reads: it
// covers no frame at all and says so. A writer holding this must be in an
// idle state on every tick (see [NewDiagnosticFrameWriter]); if it somehow
// is not, the extraction fails loudly into the failure output rather than
// dereferencing nil.
type emptyFrameSource struct{}

func (emptyFrameSource) FrameCount() int  { return 0 }
func (emptyFrameSource) StepTimeMS() byte { return 0 }
func (emptyFrameSource) ChannelRange(frame, start, count int, dst []byte) error {
	return fmt.Errorf("pipeline: this writer holds no sequence, so frame %d channels %d..%d cannot be extracted", frame, start+1, start+count)
}

// NewDiagnosticFrameWriter builds a frame writer that draws
// [IdleOutputDiagnostic] and nothing else, from a node's own local
// configuration: no sequence, no timeline, no assignment, and therefore no
// coordinator, broker, FPP master or asset manifest anywhere in its path.
// See TRACK-B-BUILD-CONTRACT.md ruling 3's node-local amendment for why
// that independence is the requirement rather than a convenience.
//
// Its timeline is [unknownTimeline], permanently an idle state, so every
// tick it ever runs draws the diagnostic pattern: no upstream state can
// turn it off, because it reads no upstream state.
//
// width, height and bytesPerPixel are the surface's own geometry, and the
// caller is responsible for having built a pipeline [Spec] that expects
// exactly width*height*bytesPerPixel bytes per frame. frameRate sets the
// tick period, since there is no FSEQ file to read a step time from.
func NewDiagnosticFrameWriter(sup *Supervisor, surfaceID string, width, height, bytesPerPixel, frameRate int, logger Logger) (*FrameWriter, error) {
	if width < 1 || height < 1 || bytesPerPixel < 1 {
		return nil, fmt.Errorf("pipeline: surface %q: diagnostic geometry %dx%d at %d bytes per pixel is invalid", surfaceID, width, height, bytesPerPixel)
	}
	if frameRate < 1 {
		return nil, fmt.Errorf("pipeline: surface %q: diagnostic frame rate %d is invalid", surfaceID, frameRate)
	}
	channelCount := width * height * bytesPerPixel
	stepTime := time.Second / time.Duration(frameRate)
	return newFrameWriter(sup, surfaceID, emptyFrameSource{}, unknownTimeline{}, "", 0, channelCount, width, height, stepTime, IdleOutputDiagnostic, nil, logger), nil
}

// NewIdleFrameWriter builds a frame writer for a real show surface whose
// assignment carries no usable FSEQ content (internal/agent/renderops.go's
// buildFSEQAssignment "ok" doc comment: an established assignment with no
// content resolved yet). Its shape mirrors [NewDiagnosticFrameWriter]
// exactly ([emptyFrameSource], [unknownTimeline], no assignment), for the
// identical reason: there is no sequence to extract a frame from, so this
// writer must be permanently idle, every tick, and say so honestly through
// [Snapshot.Drawing]/IdleMode rather than a silent content-free pipeline
// with no frame writer and therefore no draw-state evidence at all.
//
// Unlike the fixed-rgb, fixed-diagnostic-pattern standalone diagnostic
// surface, a real show surface may be rgb or rgbw and carries its own
// operator-configured idle output (black, hold, or diagnostic), so this
// constructor takes pixelFormat and idleOutput directly rather than a
// pre-computed bytesPerPixel and a hardcoded pattern.
func NewIdleFrameWriter(sup *Supervisor, surfaceID string, width, height int, pixelFormat string, frameRate int, idleOutput string, logger Logger) (*FrameWriter, error) {
	bytesPerPixel, ok := gstBytesPerPixelForPixelFormat(pixelFormat)
	if !ok {
		return nil, fmt.Errorf("pipeline: surface %q: pixel format %q is not recognized", surfaceID, pixelFormat)
	}
	if width < 1 || height < 1 {
		return nil, fmt.Errorf("pipeline: surface %q: idle geometry %dx%d is invalid", surfaceID, width, height)
	}
	if frameRate < 1 {
		return nil, fmt.Errorf("pipeline: surface %q: idle frame rate %d is invalid", surfaceID, frameRate)
	}
	channelCount := width * height * bytesPerPixel
	stepTime := time.Second / time.Duration(frameRate)
	return newFrameWriter(sup, surfaceID, emptyFrameSource{}, unknownTimeline{}, "", 0, channelCount, width, height, stepTime, idleOutput, nil, logger), nil
}

// newFrameWriter allocates the writer both constructors above share: every
// per-frame buffer, the diagnostic bar's geometry, and the alert pattern.
// It validates nothing, since each exported constructor owns the checks its
// own inputs need, but source and timeline must both be non-nil: the frame
// loop dereferences them every tick and has no absence branch (see
// [emptyFrameSource] and [unknownTimeline] for what a writer with neither
// of its own passes instead).
func newFrameWriter(sup *Supervisor, surfaceID string, source FrameSource, timeline TimelineSource, sequenceFilename string, channelStart, channelCount, width, height int, stepTime time.Duration, idleOutput string, showMode ShowModeSource, logger Logger) *FrameWriter {
	diagW, diagH, diagBPP := resolveGeometry(width, height, channelCount)
	alertBuf := make([]byte, channelCount)
	fillAlert(alertBuf, diagBPP)
	diagBuf := make([]byte, channelCount)
	for i := range diagBuf {
		diagBuf[i] = diagnosticBackgroundFill
	}
	barWidthPx := diagW / diagnosticBarWidthDivisor
	if barWidthPx < 1 {
		barWidthPx = 1
	}

	return &FrameWriter{
		surfaceID:         surfaceID,
		sup:               sup,
		logger:            logger,
		source:            source,
		timeline:          timeline,
		sequenceFilename:  sequenceFilename,
		channelStart:      channelStart,
		channelCount:      channelCount,
		stepTime:          stepTime,
		idleOutput:        idleOutput,
		buf:               make([]byte, channelCount),
		idleBuf:           make([]byte, channelCount), // zero-valued: black for rgb/rgbw alike
		diagBuf:           diagBuf,
		alertBuf:          alertBuf,
		showMode:          showMode,
		diagWidth:         diagW,
		diagHeight:        diagH,
		diagRowBytes:      diagW * diagBPP,
		diagBarWidthBytes: barWidthPx * diagBPP,
		diagBytesPerPixel: diagBPP,
		diagLastCol:       -1,
		stop:              make(chan struct{}),
		done:              make(chan struct{}),
	}
}

// fillAlert paints [FailureOutputAlert]'s pattern across dst once, at
// construction: one red pixel per bytesPerPixel-wide stride when that
// stride carries a colour (3 or 4 bytes: rgb, or rgbw with the white
// element left off), every byte full-on otherwise, including
// resolveGeometry's degenerate case where the surface's channel count is
// not a whole number of pixels and "red" has no expressible layout. A
// trailing partial pixel is filled with the leading bytes of the same
// pattern rather than left black, so the surface has no correct-looking
// stripe along its last pixel.
func fillAlert(dst []byte, bytesPerPixel int) {
	if bytesPerPixel < 3 || bytesPerPixel > len(alertPixelFill) {
		for i := range dst {
			dst[i] = alertUnknownStrideFill
		}
		return
	}
	for i := range dst {
		dst[i] = alertPixelFill[i%bytesPerPixel]
	}
}

// resolveGeometry turns a surface's own width/height/channelCount into the
// row/column geometry [IdleOutputDiagnostic]'s moving bar draws against.
// The normal case is exact: show.surface validation
// (internal/coordinator/config/showsurface.go) already enforces
// width*height*channelsPerPixel == channelCount, so bytesPerPixel is
// recovered by division with no separate pixel-format lookup needed here.
//
// The degenerate case (width or height absent/non-positive, or the counts
// do not divide evenly — should not occur for a validated assignment, but
// this is the diagnostic path and must never panic on a bad one) falls back
// to a single row of 1-byte "pixels" spanning the whole channel range, so
// the bar still has something to sweep across rather than the constructor
// failing over a display mode that exists specifically to keep reporting
// something under a misconfiguration.
func resolveGeometry(width, height, channelCount int) (w, h, bytesPerPixel int) {
	if width > 0 && height > 0 && channelCount > 0 && channelCount%(width*height) == 0 {
		return width, height, channelCount / (width * height)
	}
	if channelCount <= 0 {
		return 1, 1, 1
	}
	return channelCount, 1, 1
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

// IdleOutput reports which idle output this writer draws. Fixed at
// construction, so no lock: a caller changing it replaces the writer.
func (fw *FrameWriter) IdleOutput() string { return fw.idleOutput }

// Counts returns the writer's cumulative written/late/dropped counts.
func (fw *FrameWriter) Counts() (written, late, dropped int64) {
	return fw.written.Load(), fw.late.Load(), fw.dropped.Load()
}

// writeOneFrame is one tick's worth of work: detect any ticks the scheduler
// itself dropped, sample the timeline, select content or idle output, write
// it to the pipeline's stdin, and record what actually happened as evidence
// (finding 7) — regardless of whether the write succeeded.
func (fw *FrameWriter) writeOneFrame(tickTime time.Time) {
	start := time.Now()

	fw.countTickerDrops(tickTime)

	snap := fw.timeline.Snapshot()

	var outBuf []byte
	var positionMS *int64
	drawing := DrawingContent
	idleMode := ""
	failureOutput := ""
	if idleContentStates[snap.State] {
		drawing = DrawingIdle
		idleMode = fw.idleOutput
		outBuf = fw.idleOutputFor(tickTime)
	} else if snap.Filename != "" && snap.Filename != fw.sequenceFilename {
		// The timeline says something is playing, but not the sequence
		// this writer holds: FPP has moved on to a sequence this surface
		// was never assigned, and sync is still arriving. Before this
		// check, Playing plus "whatever FSEQ this surface holds" was
		// drawn unconditionally, with nothing ever re-checking that the
		// held file was still the right one.
		//
		// An empty snap.Filename is deliberately NOT a mismatch: it means
		// MultiSync has not reported a filename yet, the same "nothing to
		// compare against" reading internal/agent/cueactivationrender.go's
		// activateSurfaceRender already gives it (ADR-043 decision 6) — a
		// writer must never blank a surface for lack of evidence, only for
		// evidence that actually disagrees.
		//
		// This deliberately does NOT fall through to idleOutputFor: a
		// [IdleOutputHold] writer's fw.buf is the last frame extracted
		// from the WRONG sequence, and drawing it here would reproduce the
		// exact bug being fixed under a different name. Black is the one
		// output this writer can put on the wall that is not a
		// content claim about anything, forced the same way a coverage-gap
		// extraction failure already forces its own output regardless of
		// the operator's configured idle mode.
		//
		// Not counted as a dropped frame: extraction never even ran, and a
		// real frame (idleBuf) reaches the pipeline's stdin just as
		// successfully as an ordinary idle tick's does. dropped means a
		// frame failed to reach the pipeline at all (build contract
		// ruling 3); this frame reaches it, it is just deliberately not
		// content.
		if !fw.loggedStale {
			fw.loggedStale = true
			fw.logger.Warn("frame writer: timeline is reporting a different sequence than this surface holds; this surface has stopped drawing content until it recovers",
				"surface_id", fw.surfaceID, "held_sequence", fw.sequenceFilename, "timeline_sequence", snap.Filename)
		}
		drawing = DrawingStale
		idleMode = ""
		outBuf = fw.idleBuf
	} else {
		fw.loggedStale = false
		pos := snap.PositionMS
		frameIdx := fw.frameIndexFor(pos)
		if err := fw.source.ChannelRange(frameIdx, fw.channelStart, fw.channelCount, fw.buf); err != nil {
			fw.dropped.Add(1)
			if !fw.loggedRangeErr {
				fw.loggedRangeErr = true
				fw.logger.Warn("frame writer: channel range extraction failed; this surface is drawing the failure output until it recovers",
					"surface_id", fw.surfaceID, "frame", frameIdx,
					"failure_output", failureOutputFor(fw.behavesAsShow()), "error", err)
			}
			// A coverage-gap fallback is an extraction FAILURE, not a
			// deliberate idle transition, and it is reported as its own
			// drawing state so a broken assignment can never be
			// mislabelled as a normal operator-chosen idle cycle. It used
			// to report idle-with-black for that reason, which kept the
			// EVIDENCE honest and left the WALL saying nothing was wrong:
			// black is exactly what a healthy idle looks like to the
			// person standing in front of it.
			//
			// So the mode decides what reaches the wall, read HERE, on the
			// failing frame, never captured at construction (ADR-036
			// decision 1): an unmistakable alert field in Program Mode,
			// black in Show Mode. Red in front of an audience is worse
			// than black; black in front of an operator who is
			// programming is worse than useless, and this failure is
			// essentially only reachable at configuration time. A node
			// that has never been told the mode reads unknown, which
			// behaves as show (ADR-033 decision 5), so the conservative
			// side is also the default.
			drawing = DrawingFailure
			idleMode = ""
			failureOutput = failureOutputFor(fw.behavesAsShow())
			if failureOutput == FailureOutputAlert {
				outBuf = fw.alertBuf
			} else {
				outBuf = fw.idleBuf
			}
		} else {
			fw.loggedRangeErr = false
			outBuf = fw.buf
			positionMS = &pos
		}
	}

	fw.timelineState = string(snap.State)
	fw.timelinePositionMS = positionMS
	fw.drawing = drawing
	fw.idleModeNow = idleMode
	fw.failureOutputNow = failureOutput

	w, err := fw.sup.Stdin(fw.surfaceID)
	if err != nil {
		fw.dropped.Add(1)
		fw.recordTick(start, tickTime, false)
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
		fw.recordTick(start, tickTime, false)
		return
	}
	fw.loggedWriteErr = false
	fw.written.Add(1)

	fw.recordTick(start, tickTime, true)
}

// failureOutputFor names the fallback a failing frame draws for one answer
// to "must this node behave as Show Mode", so the log line and the
// evidence cannot disagree about what reached the wall.
func failureOutputFor(behavesAsShow bool) string {
	if behavesAsShow {
		return FailureOutputBlack
	}
	return FailureOutputAlert
}

// behavesAsShow is this writer's point-of-decision read of the node's
// operating mode (ADR-036 decision 1): called on the failing frame itself,
// never cached, so an operator switching modes changes what a LIVE surface
// draws with no re-apply and no rebuilt writer. A writer built with no mode
// source behaves as Show Mode, the same answer ADR-033 decision 5 gives an
// unknown mode.
func (fw *FrameWriter) behavesAsShow() bool {
	if fw.showMode == nil {
		return true
	}
	return fw.showMode.BehavesAsShow()
}

// countTickerDrops detects ticks Run's time.Ticker itself silently dropped
// (finding 13): a Go ticker never queues a missed tick for a slow receiver,
// it simply never sends it, so a writer running behind schedule produces NO
// call to writeOneFrame for the ticks it missed — nothing to measure lateness
// on. Comparing this tick's own scheduled instant (tickTime) against the
// previous one recovers that: a gap of more than one stepTime means at
// least one tick never arrived. Counted as dropped frames, the same counter
// a failed write or a missing process uses — a ticker-dropped tick never got
// a chance to render or write either, so it belongs in the same evidence.
func (fw *FrameWriter) countTickerDrops(tickTime time.Time) {
	if !fw.lastTickTime.IsZero() {
		if gap := tickTime.Sub(fw.lastTickTime); gap > fw.stepTime {
			if missed := int64(gap/fw.stepTime) - 1; missed > 0 {
				fw.dropped.Add(missed)
			}
		}
	}
	fw.lastTickTime = tickTime
}

// recordTick finishes one tick: lateness (delivered ticks only), the
// achieved-rate sample, and the draw-state/counter report — called on
// every path through writeOneFrame, success or failure, so a stalled
// pipeline's evidence keeps moving instead of freezing at its last good
// value (finding 8).
func (fw *FrameWriter) recordTick(start, tickTime time.Time, delivered bool) {
	if delivered {
		// Scheduling lateness (finding 13): how far behind THIS TICK'S OWN
		// scheduled instant delivery actually finished — not merely how
		// long this call itself took to run. time.Since(start) alone is
		// blind to a writer running at half rate, because the ticks it
		// never got a chance to process (see countTickerDrops) never
		// generated a call to measure in the first place; tickTime is the
		// real scheduling reference and is handed in for exactly this.
		if lateness := time.Since(tickTime); lateness > fw.stepTime {
			fw.late.Add(1)
		}
	}
	// Always sampled from the CUMULATIVE written counter, on every tick,
	// not only on a successful one: a stalled pipeline stops incrementing
	// fw.written, so the window's frame delta naturally falls to zero and
	// the achieved rate converges to a real, measured 0.0 within one
	// frameRateWindow — never the stale last-good value this counter used
	// to freeze at forever (finding 8).
	fw.sampleRate(start, fw.written.Load())
	fw.reportCounts()
}

// idleOutputFor selects (and, for diagnostic, updates) which buffer to draw
// for one idle tick, per fw.idleOutput.
//
//   - [IdleOutputBlack]: idleBuf, all-zero, never written after
//     construction.
//   - [IdleOutputHold]: buf, the last successfully extracted content frame
//     (buf is never touched while idle — see the FrameWriter struct's own
//     doc comment on buf).
//   - [IdleOutputDiagnostic]: diagBuf, a constant background with one
//     vertical bar column swept across it, position derived from tick
//     (wall-clock time), never from a per-call counter — so the position is
//     itself a clock a bystander can check, and so a missed or delayed tick
//     can never leave the bar in the wrong place. On the hot path this
//     touches only the bar's own O(height) worth of bytes, never the whole
//     buffer: at a 1080p-equivalent matrix diagBuf is ~6.2MB, and RES-004's
//     day-0 budget leaves roughly 14% of one core spare at the reference 40
//     fps profile (build contract, ruling 5) — that is not room to spend
//     rewriting an idle screen every tick when erasing the old column and
//     drawing the new one is column-width * height bytes, independent of
//     the surface's width.
func (fw *FrameWriter) idleOutputFor(tick time.Time) []byte {
	switch fw.idleOutput {
	case IdleOutputHold:
		return fw.buf
	case IdleOutputDiagnostic:
		col := diagnosticBarColumn(tick, fw.diagWidth)
		if col != fw.diagLastCol {
			fw.drawDiagnosticBarColumn(col)
		}
		return fw.diagBuf
	default:
		return fw.idleBuf
	}
}

// diagnosticBarColumn is the pure function of wall-clock time behind
// [IdleOutputDiagnostic]'s bar: which pixel column it occupies at tick, on
// a surface diagWidth pixels wide. A pure function of (tick, diagWidth)
// rather than a counter incremented per call, so a paused writer, a missed
// tick, or two independent callers all agree on where the bar is without
// coordinating — the same property that makes it "generated," never state
// that can drift out of sync with itself.
func diagnosticBarColumn(tick time.Time, diagWidth int) int {
	if diagWidth <= 0 {
		return 0
	}
	return int((tick.UnixNano() / int64(diagnosticPixelPeriod)) % int64(diagWidth))
}

// drawDiagnosticBarColumn moves [IdleOutputDiagnostic]'s bar to col:
// erases the previously drawn column back to the background fill (skipped
// on the very first call, fw.diagLastCol == -1), then draws col. Both steps
// touch exactly diagHeight rows of diagBarWidthBytes each — O(height), not
// O(width*height) — because a vertical bar's column bytes are the only
// bytes that changed; every other byte in diagBuf is still whatever the
// background fill (or a previous bar position, now erased) left there. See
// idleOutputFor's own comment for why that difference is the point.
func (fw *FrameWriter) drawDiagnosticBarColumn(col int) {
	if fw.diagLastCol >= 0 {
		fw.fillDiagnosticColumn(fw.diagLastCol, diagnosticBackgroundFill)
	}
	fw.fillDiagnosticColumn(col, diagnosticBarFill)
	fw.diagLastCol = col
}

// fillDiagnosticColumn writes fill across the bar's width (diagBarWidthBytes,
// clamped to the surface's own edge) in every row of diagBuf, at col — the
// one primitive both erasing and drawing the bar reduce to.
func (fw *FrameWriter) fillDiagnosticColumn(col int, fill byte) {
	if fw.diagBytesPerPixel <= 0 || fw.diagRowBytes <= 0 {
		return
	}
	startByte := col * fw.diagBytesPerPixel
	endByte := startByte + fw.diagBarWidthBytes
	if rowLimit := fw.diagRowBytes; endByte > rowLimit {
		endByte = rowLimit
	}
	if endByte <= startByte {
		return
	}
	for row := 0; row < fw.diagHeight; row++ {
		base := row*fw.diagRowBytes + startByte
		end := row*fw.diagRowBytes + endByte
		if end > len(fw.diagBuf) {
			break
		}
		for i := base; i < end; i++ {
			fw.diagBuf[i] = fill
		}
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
//
// framesObservedAt is stamped here, at window close, to now, and this is
// the evidence timestamp reportCounts later attaches to ALL FOUR counters
// (FramesWritten/FramesLate/FramesDropped/FramesRate), not only FramesRate.
// FramesWritten/Late/Dropped are cumulative and actually change on every
// tick, so a window-close stamp can lag their true sample instant by up to
// frameRateWindow (5s), well inside the 45s DefaultValidFor a stale
// pipeline.Supervisor.Snapshot.ObservedAt this issue is fixing gets judged
// against. A per-tick stamp would be more precise, but it would also be
// indistinguishable from a heartbeat: this issue is about the counters
// carrying a REAL measurement instant, and the rate window is the only
// place in this writer that already represents one. Sharing it is a
// deliberate choice, not an oversight.
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
	fw.framesObservedAt = now
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
	fw.sup.SetFrameCounts(fw.surfaceID, fw.written.Load(), fw.late.Load(), fw.dropped.Load(), fw.currentRate, fw.framesObservedAt)
	fw.sup.SetDrawState(fw.surfaceID, DrawState{
		TimelineState: fw.timelineState,
		PositionMS:    fw.timelinePositionMS,
		Drawing:       fw.drawing,
		IdleMode:      fw.idleModeNow,
		FailureOutput: fw.failureOutputNow,
	})
}

// Compile-time check that *fseq.File satisfies FrameSource.
var _ FrameSource = (*fseq.File)(nil)

// Compile-time check that *multisync.Timeline satisfies TimelineSource.
var _ TimelineSource = (*multisync.Timeline)(nil)
