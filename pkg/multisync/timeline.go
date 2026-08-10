package multisync

import (
	"sync"
	"time"
)

// Timeline tracks the estimated presentation position of one file (a
// sequence or a media file) as driven by MultiSync sync packets from a
// master, mirroring how FPP's own remote behaves. It is pure logic: no
// network I/O, no goroutines, no direct calls to time.Now. Every method
// that needs the current time takes it from the injected clock function so
// tests can drive the state machine with a fake clock instead of real
// sleeps.
//
// The semantics implemented here come from docs/research/RES-002-fpp-
// multisync-compatibility.md, "Semantics a listener must implement" and
// "Loss policy", which in turn read FPP's own channeloutputthread.cpp and
// Sequence.cpp. RES-002 is still only source-verified (L1); nothing in this
// file, and no unit test built on top of it, raises that above L1. Only a
// packet capture and bench run against a real FPP player can do that (see
// RES-002's open bench items and docs/build/BUILD-PLAN.md Step 1).
type Timeline struct {
	// mu guards every mutable field below. The *Locked method naming
	// convention in this file used to describe an aspiration, not a fact:
	// nothing actually enforced it, and Snapshot itself calls settleLocked,
	// which can mutate t.state. A Step 3 consumer calling Snapshot from an
	// HTTP handler while the listener goroutine calls Observe would have
	// been a straight, unenforced data race. Observe, Snapshot, and
	// SetStepTime take mu; every other method in this file assumes the
	// caller already holds it, per the *Locked suffix.
	mu sync.Mutex

	// now is the clock Timeline reads for every time-dependent decision.
	// Never call time.Now directly anywhere else in this file.
	now func() time.Time

	cfg Config

	// stepTime is the live step time used to convert FrameNumber to a
	// position. It starts at cfg.StepTime and can be changed with
	// SetStepTime when a new file starts. See Config.StepTime for why this
	// cannot be learned from the wire.
	stepTime time.Duration

	state State
	// stateSince is when state was last observed to change value; it does
	// not move while the same state is merely reconfirmed. Set only through
	// setStateLocked. Follows the precedent set by
	// internal/coordinator/broker.go's BrokerState.Since.
	stateSince time.Time
	filename   string
	fileType   SyncFileType

	// anchorAt/anchorPosition define the timeline's position function.
	// While the timeline is free-running (state Playing or
	// Unsynchronized), the estimated position at any later time t is
	// anchorPosition + (t - anchorAt). In every other state, output is not
	// advancing (opened but not started, stopping, or stopped), so the
	// position is frozen at anchorPosition and anchorAt is not read.
	anchorAt       time.Time
	anchorPosition time.Duration

	// lastSyncAt is the time of the most recently received sync packet of
	// any action (OPEN, START, SYNC, or STOP all count as "a sync packet"
	// per RES-002's packet definition). lastSyncAtValid distinguishes
	// "never received one" from the zero time.Time, which is itself a
	// valid instant.
	lastSyncAt      time.Time
	lastSyncAtValid bool

	// stopRequestedAt is when the most recent STOP was received; it is
	// only meaningful while state is Stopping, and is what settleLocked
	// compares against the blank delay.
	stopRequestedAt time.Time

	lastCorrection                               CorrectionKind
	slewCount, skipCount, jumpCount, onTimeCount int

	// lastDrift is the signed drift (master's reported position minus this
	// timeline's free-run estimate) computed on the most recently observed
	// SYNC correction, zero before any correction has been computed. This
	// is the magnitude RES-002 bench item 4 (clock drift aggressiveness
	// over a 30 to 60 minute show) needs; the *Count fields alone only say
	// how many times each class fired, not by how much.
	lastDrift time.Duration

	// source is the opaque identity of whoever is currently driving this
	// timeline (typically the packet's source address), supplied by the
	// caller of Observe. conflict/conflictCount record, without
	// arbitrating, that packets for the same file have also arrived from a
	// different source. See Observe's doc comment.
	source        string
	conflict      bool
	conflictCount int
}

// Config holds every threshold and interval the timeline's correction and
// lifecycle logic depends on. Each field's doc comment states its
// provenance: taken from FPP source via RES-002, or a ShowMesh hypothesis
// still awaiting RES-002 bench verification. Do not blur the two.
type Config struct {
	// StepTime is the playback step time (frame duration) used to convert
	// a packet's FrameNumber into a position when SecondsElapsed is not
	// usable (see positionFromPacket). Default 25ms (40fps).
	//
	// OPERATIONAL TRAP: RES-002 records that MultiSync does NOT carry
	// rate in its packets. FPP's own remote derives step time from its
	// local copy of the .fseq file being played, on the assumption that
	// both sides hold the same file. This field is therefore
	// configuration, not protocol data: the wire gives Timeline no way to
	// verify it. Getting it wrong does not produce an error; it produces a
	// confidently wrong position estimate every time FrameNumber fallback
	// is used. Callers MUST set this to match the actual file about to
	// play, and MUST call SetStepTime whenever the file changes if the new
	// file's step time differs. The 25ms default is a common FPP sequence
	// step time, not a protocol guarantee.
	StepTime time.Duration

	// NoCorrectionThresholdFrames bounds how many frames of drift, converted
	// to a duration via the live step time, are treated as no meaningful
	// adjustment at all (CorrectionOnTime) rather than counted as a slew.
	// Without this band, a perfectly synced stream reports every single
	// sync packet as a "slew" of zero drift, which is not usable evidence
	// for RES-002 bench item 4: the review that added this field reproduced
	// 10 exactly-on-time syncs producing SlewCount=10, SkipCount=0,
	// JumpCount=0, indistinguishable from a stream that is only barely
	// inside the slew band on every single packet.
	//
	// SHOWMESH HYPOTHESIS, NOT MEASURED: neither RES-002 nor FPP's own
	// channeloutputthread.cpp define an "on time, no correction needed"
	// band; FPP's real mechanism is a continuously adjusted output rate,
	// which this package's discrete, per-sync classification cannot
	// reproduce exactly. 1 frame is a starting guess for what counts as
	// negligible drift, not an FPP-derived value. Must be smaller than
	// SlewThresholdFrames to have any effect; not enforced. Default 1.
	NoCorrectionThresholdFrames int

	// SlewThresholdFrames bounds how many frames of drift, converted to a
	// duration via the live step time, are corrected by slewing (gradual
	// adjustment) rather than skipping or jumping. FPP-derived: RES-002
	// cites channeloutputthread.cpp as slewing within <=4 frames. Default
	// 4.
	SlewThresholdFrames int

	// JumpThreshold is the drift duration beyond which the correction
	// jumps directly to the master's reported position instead of
	// skipping. FPP-derived: RES-002 cites channeloutputthread.cpp as
	// jumping when more than 0.5s behind. Default 500ms.
	JumpThreshold time.Duration

	// BlankDelayFrames is how many frames (converted via the live step
	// time) the timeline waits after a STOP before resolving from
	// Stopping to Stopped. FPP-derived: RES-002 cites FPP's Sequence.cpp
	// as waiting "~5 frames" before blanking, specifically so a back to
	// back stop and start does not blink. Default 5.
	BlankDelayFrames int

	// SilenceInterval is how long the timeline can go without receiving
	// any sync packet before it reports itself Unsynchronized (while
	// continuing to free-run; silence is never a teardown trigger).
	//
	// SHOWMESH HYPOTHESIS, NOT MEASURED: RES-002 records master cadence as
	// roughly 2 to 4 sync packets per second for sequences and every 0.5s
	// for media, and the only FPP-side watchdog it found is
	// RemoteSyncedMediaIdleTimeout, a 10s timeout that only applies after
	// media ends. Neither of those establishes 5s as correct; it is a
	// starting guess picked to sit comfortably above normal cadence
	// without being so long it hides real loss. This default must be
	// revisited once RES-002's bench work (packet capture, real cadence
	// and jitter) lands, per docs/research/README.md's evidence ladder.
	SilenceInterval time.Duration

	// SlewFractionPerSync is the fraction of a small (within
	// SlewThresholdFrames) drift that is applied at each sync event,
	// rather than corrected in a single snap. This is what makes slewing
	// gradual: the remaining drift closes over subsequent syncs plus
	// free-run, instead of an instant position jump.
	//
	// SHOWMESH HYPOTHESIS: RES-002 and FPP's channeloutputthread.cpp
	// establish the <=4 frame threshold for when to slew, but describe
	// FPP's own mechanism as continuously adjusting output rate, which
	// this discrete, sync-event-driven model cannot reproduce exactly.
	// This fraction controls only the shape of the correction; the
	// threshold that decides slew vs. skip vs. jump is the FPP-derived
	// value above. Default 0.5.
	//
	// NewTimeline clamps any value above 1.0 down to 1.0 rather than
	// accepting it: a fraction above 1.0 would overshoot the master's
	// reported position on every slew and oscillate around it on the next
	// one. This clamp, like the fraction itself, is a ShowMesh choice, not
	// something FPP's own (fraction-free) slewing mechanism could violate.
	SlewFractionPerSync float64
}

// Provenance-labeled defaults for Config. See each Config field's doc
// comment for which of these come from FPP source (via RES-002) and which
// are ShowMesh hypotheses awaiting bench verification.
const (
	// DefaultStepTime: ShowMesh default, common FPP sequence step time
	// (25ms/40fps), NOT protocol-verified. See Config.StepTime.
	DefaultStepTime = 25 * time.Millisecond

	// DefaultNoCorrectionThresholdFrames: ShowMesh hypothesis, not measured.
	// See Config.NoCorrectionThresholdFrames.
	DefaultNoCorrectionThresholdFrames = 1

	// DefaultSlewThresholdFrames: FPP-derived (RES-002, channeloutputthread.cpp).
	DefaultSlewThresholdFrames = 4

	// DefaultJumpThreshold: FPP-derived (RES-002, channeloutputthread.cpp).
	DefaultJumpThreshold = 500 * time.Millisecond

	// DefaultBlankDelayFrames: FPP-derived (RES-002, Sequence.cpp ~695).
	DefaultBlankDelayFrames = 5

	// DefaultSilenceInterval: ShowMesh hypothesis, not measured. See
	// Config.SilenceInterval.
	DefaultSilenceInterval = 5 * time.Second

	// DefaultSlewFractionPerSync: ShowMesh hypothesis about ramp shape.
	// See Config.SlewFractionPerSync.
	DefaultSlewFractionPerSync = 0.5
)

// DefaultConfig returns a Config with every field set to its documented
// default. Callers typically start here and override only what their show
// needs (most commonly StepTime, per its operational-trap warning above).
func DefaultConfig() Config {
	return Config{
		StepTime:                    DefaultStepTime,
		NoCorrectionThresholdFrames: DefaultNoCorrectionThresholdFrames,
		SlewThresholdFrames:         DefaultSlewThresholdFrames,
		JumpThreshold:               DefaultJumpThreshold,
		BlankDelayFrames:            DefaultBlankDelayFrames,
		SilenceInterval:             DefaultSilenceInterval,
		SlewFractionPerSync:         DefaultSlewFractionPerSync,
	}
}

// State names the observable lifecycle position of a Timeline. Values are
// deliberately distinct so that "never seen a packet" (Unknown),
// "free-running but the silence window has elapsed" (Unsynchronized), and
// "actively playing and recently synced" (Playing) cannot be confused with
// one another; ADR-011 requires evidence to distinguish staleness from a
// confident positive.
type State string

const (
	// StateUnknown is the zero state: no OPEN, START, or SYNC packet has
	// ever been observed for this Timeline. settleLocked also returns here
	// from StateOpened once SilenceInterval elapses with no further packet:
	// per ADR-011, stale evidence must read as unknown, never as a
	// lingering confident positive, so a master that sent OPEN and then
	// died does not report `opened` forever.
	StateUnknown State = "unknown"

	// StateOpened means an OPEN was received: a file has been identified
	// and readied, but playback has not started, so position is frozen.
	StateOpened State = "opened"

	// StatePlaying means the timeline is free-running and has heard a
	// sync packet within the configured SilenceInterval.
	StatePlaying State = "playing"

	// StateUnsynchronized means the timeline is still free-running (it
	// never stops, blanks, or resets on silence per RES-002) but no sync
	// packet has arrived within SilenceInterval. This is a confidence
	// statement about the estimate, not a playback halt.
	StateUnsynchronized State = "unsynchronized"

	// StateStopping means a STOP was received and the timeline is holding
	// its last position for BlankDelayFrames before resolving to Stopped,
	// so a back to back stop and start does not blink.
	StateStopping State = "stopping"

	// StateStopped means the blank delay has elapsed since STOP with no
	// intervening START; position is frozen.
	StateStopped State = "stopped"
)

// CorrectionKind names which class of correction, per RES-002's
// channeloutputthread.cpp bounds, was applied on the most recent sync.
type CorrectionKind string

const (
	// CorrectionNone means no correction has been computed yet (e.g.
	// immediately after START, before the first SYNC arrives).
	CorrectionNone CorrectionKind = "none"

	// CorrectionOnTime means a SYNC correction WAS computed, and the drift
	// was within Config.NoCorrectionThresholdFrames of zero: close enough
	// that applying any adjustment would not be meaningful, so none was.
	// Distinct from CorrectionNone, which means no SYNC has been observed
	// at all yet. See NoCorrectionThresholdFrames's doc comment for why
	// this exists: without it, a perfectly synced stream's every packet
	// reports as CorrectionSlew, which is not usable evidence of anything.
	CorrectionOnTime CorrectionKind = "on-time"

	// CorrectionSlew: drift was within SlewThresholdFrames (and outside
	// NoCorrectionThresholdFrames) and was closed gradually (see
	// Config.SlewFractionPerSync).
	CorrectionSlew CorrectionKind = "slew"

	// CorrectionSkip: drift was moderate, more than SlewThresholdFrames
	// but not more than JumpThreshold, and was applied immediately.
	CorrectionSkip CorrectionKind = "skip"

	// CorrectionJump: drift exceeded JumpThreshold and was applied
	// immediately.
	CorrectionJump CorrectionKind = "jump"
)

// Snapshot is a timestamped, provenance-carrying observation of a
// Timeline, taken at a single instant. Per ADR-011, evidence must carry
// freshness and provenance rather than being presented as unconditionally
// current; ObservedAt lets a caller judge staleness independently of
// LastSyncAt, and Source/Conflict/ConflictCount record where the evidence
// came from. This follows the precedent set by
// internal/coordinator/broker.go's BrokerState (value plus Since plus
// ObservedAt).
type Snapshot struct {
	// State is the timeline's lifecycle state at ObservedAt.
	State State

	// StateSince is when State's current value was first observed: it does
	// not move while the same state is merely reconfirmed, only when state
	// actually transitions. Combined with ObservedAt, a caller can answer
	// "how long has it been in this state" (for example, Step 3's "how
	// long have we been unsynchronized") the same way
	// internal/coordinator/broker.go's BrokerState.Since answers the
	// analogous question for the broker connection; this follows that
	// precedent.
	StateSince time.Time

	// Filename and FileType identify what is being tracked. FileType is
	// only meaningful once State is past StateUnknown.
	Filename string
	FileType SyncFileType

	// PositionMS is the estimated presentation position in milliseconds
	// at ObservedAt: free-running if State is Playing or Unsynchronized,
	// frozen otherwise.
	PositionMS int64

	// LastSyncAt is the time of the most recently received sync packet of
	// any action, and LastSyncAtValid distinguishes "never received one"
	// from the zero time.Time. This is the "time of the last sync packet"
	// evidence field, distinct from ObservedAt below.
	LastSyncAt      time.Time
	LastSyncAtValid bool

	// ObservedAt is when this snapshot was taken. It always advances,
	// even if nothing else changed, so staleness can be computed as
	// (now - ObservedAt) by a caller without needing a separate probe.
	ObservedAt time.Time

	// LastCorrection is which correction class was applied on the most
	// recent sync (CorrectionNone if none has happened yet).
	LastCorrection CorrectionKind

	// OnTimeCount, SlewCount, SkipCount, and JumpCount are running counts of
	// each correction class applied over the current file's session
	// (OnTimeCount for a SYNC whose drift was negligible; see
	// CorrectionOnTime). Together with LastDriftMS below, they are the raw
	// material RES-002 bench item 4 (clock drift over a 30 to 60 minute
	// show) needs to evaluate slew aggressiveness; the counts by
	// themselves are not that evidence, only unit-test-level plumbing for
	// it.
	OnTimeCount int
	SlewCount   int
	SkipCount   int
	JumpCount   int

	// LastDriftMS is the signed drift (the master's reported position minus
	// this timeline's free-run estimate, in milliseconds) computed on the
	// most recent SYNC correction; positive means the master is ahead of
	// the local estimate. Zero before any correction has been computed.
	// The *Count fields above only say how many times each correction
	// class fired; this is the magnitude bench item 4 actually needs.
	LastDriftMS int64

	// Source is the opaque identity currently driving this timeline
	// (typically the packet source address), as supplied to Observe.
	Source string

	// Conflict is true if a packet for the same Filename has been
	// observed from a different source than Source. ConflictCount is how
	// many times that has happened for the current file. Timeline does
	// not arbitrate between competing masters; this is only the
	// observability RES-002's test method calls for and that ADR-003
	// names as the evidence behind a `conflicted` reconciliation state.
	Conflict      bool
	ConflictCount int
}

// NewTimeline constructs a Timeline. now is the clock function used for
// every time-dependent decision; pass time.Now in production and a fake
// clock's method in tests. If now is nil, time.Now is used. Zero-valued
// fields in cfg are replaced with DefaultConfig's values field by field, so
// callers can pass a partially filled Config.
func NewTimeline(now func() time.Time, cfg Config) *Timeline {
	if now == nil {
		now = time.Now
	}

	d := DefaultConfig()
	if cfg.StepTime <= 0 {
		cfg.StepTime = d.StepTime
	}
	if cfg.NoCorrectionThresholdFrames <= 0 {
		cfg.NoCorrectionThresholdFrames = d.NoCorrectionThresholdFrames
	}
	if cfg.SlewThresholdFrames <= 0 {
		cfg.SlewThresholdFrames = d.SlewThresholdFrames
	}
	if cfg.JumpThreshold <= 0 {
		cfg.JumpThreshold = d.JumpThreshold
	}
	if cfg.BlankDelayFrames <= 0 {
		cfg.BlankDelayFrames = d.BlankDelayFrames
	}
	if cfg.SilenceInterval <= 0 {
		cfg.SilenceInterval = d.SilenceInterval
	}
	if cfg.SlewFractionPerSync <= 0 {
		cfg.SlewFractionPerSync = d.SlewFractionPerSync
	}
	if cfg.SlewFractionPerSync > 1.0 {
		// A fraction above 1.0 would overshoot the master's reported
		// position on every slew instead of gradually closing on it; see
		// Config.SlewFractionPerSync.
		cfg.SlewFractionPerSync = 1.0
	}

	initAt := now()

	return &Timeline{
		now:            now,
		cfg:            cfg,
		stepTime:       cfg.StepTime,
		state:          StateUnknown,
		stateSince:     initAt,
		lastCorrection: CorrectionNone,
	}
}

// SetStepTime updates the step time used to derive position from
// FrameNumber for packets observed from now on. Callers MUST call this
// whenever a new file starts and its step time differs from the current
// one; see Config.StepTime for why this cannot be learned from the wire
// itself. Passing a non-positive value resets to DefaultStepTime rather
// than silently corrupting position math with a zero or negative divisor
// equivalent.
func (t *Timeline) SetStepTime(stepTime time.Duration) {
	t.mu.Lock()
	defer t.mu.Unlock()

	if stepTime <= 0 {
		stepTime = DefaultStepTime
	}
	t.stepTime = stepTime
}

// Observe applies one incoming sync packet to the timeline. source is an
// opaque identifier for the packet's sender, typically its source address,
// supplied by the caller; Timeline does not parse or interpret it beyond
// equality comparison for competing-master detection.
//
// SOURCE IDENTITY CONTRACT: source must be stable for one logical master
// across every socket that master sends from, and across that master's own
// restarts. RES-002 records that FPP's SendControlPacket fans a single
// master's traffic out over several independent, unbound sockets: one for
// unicast, one for broadcast, and one per network interface for multicast,
// and every one of them gets a fresh ephemeral source port on every fppd
// restart. A single legitimate master can, and does, appear under several
// different source ports at once, and under a different set again after a
// restart.
//
// The source IP address is the right identity to pass. An "ip:port" string
// is specifically WRONG: it makes one real master look like several
// different, conflicting masters to Observe. A review of this package
// reproduced exactly that failure mode: 40 consecutive SYNC packets from a
// second source port on the same master were all dropped as conflicts, the
// timeline going stale for 10+ seconds with even the following STOP
// dropped, recovering only once the filename happened to change. Timeline
// does not strip a port for you; it is not told which part of an opaque
// string might be one, and guessing would be exactly the kind of silent
// behavior this package avoids elsewhere (see, for example, DecodeSync's
// filename handling). Get the identity right before calling Observe.
//
// Lifecycle tolerance (RES-002): OPEN then START then SYNC then STOP is the
// normal path, but a robust listener must also accept START with no
// preceding OPEN, and a bare SYNC for a file that was never started. Both
// are handled here by treating any SYNC that does not match an active
// Playing/Unsynchronized session for the same filename as an implicit
// START.
//
// Competing masters: if a packet's filename matches the file this timeline
// is currently tracking but its source differs from the established
// driving source, Observe records the conflict (Conflict flag and
// ConflictCount in Snapshot) and returns without applying the packet's
// data. This is a deliberate judgment call: RES-002's test method lists
// competing masters as a failure case to make observable, and explicitly
// says not to arbitrate between them. Applying whichever conflicting
// packet arrives last would itself be a form of arbitration by recency, so
// instead the first-seen source keeps driving the timeline and the
// conflict is surfaced as evidence for an operator (or a future
// `conflicted` reconciliation state per ADR-003) to act on.
func (t *Timeline) Observe(pkt SyncPacket, source string) {
	t.mu.Lock()
	defer t.mu.Unlock()

	now := t.now()
	t.settleLocked(now)

	if t.isConflictingLocked(pkt.Filename, source) {
		t.conflict = true
		t.conflictCount++
		return
	}

	switch pkt.Action {
	case SyncActionOpen:
		t.applyOpenLocked(pkt, source, now)
	case SyncActionStart:
		t.applyStartLocked(pkt, source, now)
	case SyncActionSync:
		t.applySyncLocked(pkt, source, now)
	case SyncActionStop:
		t.applyStopLocked(pkt, source, now)
	}
}

// Snapshot returns a fresh, timestamped observation of the timeline,
// resolving any pending time-based transition (Stopping to Stopped,
// Playing to Unsynchronized) against the clock first.
func (t *Timeline) Snapshot() Snapshot {
	t.mu.Lock()
	defer t.mu.Unlock()

	now := t.now()
	t.settleLocked(now)

	return Snapshot{
		State:           t.state,
		StateSince:      t.stateSince,
		Filename:        t.filename,
		FileType:        t.fileType,
		PositionMS:      t.estimatePositionLocked(now).Milliseconds(),
		LastSyncAt:      t.lastSyncAt,
		LastSyncAtValid: t.lastSyncAtValid,
		ObservedAt:      now,
		LastCorrection:  t.lastCorrection,
		OnTimeCount:     t.onTimeCount,
		SlewCount:       t.slewCount,
		SkipCount:       t.skipCount,
		JumpCount:       t.jumpCount,
		LastDriftMS:     t.lastDrift.Milliseconds(),
		Source:          t.source,
		Conflict:        t.conflict,
		ConflictCount:   t.conflictCount,
	}
}

// settleLocked resolves the two purely time-driven transitions: Stopping
// to Stopped once BlankDelayFrames worth of time has elapsed since STOP,
// and Playing to Unsynchronized once SilenceInterval has elapsed since the
// last sync packet. Timeline has no goroutine or timer of its own; these
// transitions only become visible when Observe or Snapshot next runs and
// asks the clock what time it is.
func (t *Timeline) settleLocked(now time.Time) {
	if t.state == StateStopping {
		blankDelay := time.Duration(t.cfg.BlankDelayFrames) * t.stepTime
		if !t.stopRequestedAt.IsZero() && now.Sub(t.stopRequestedAt) >= blankDelay {
			t.setStateLocked(StateStopped, now)
		}
	}

	if t.state == StatePlaying && t.lastSyncAtValid && now.Sub(t.lastSyncAt) >= t.cfg.SilenceInterval {
		t.setStateLocked(StateUnsynchronized, now)
	}

	// StateOpened does not age on its own the way StatePlaying does above:
	// FPP's remote has no separately documented silence-versus-degrade
	// behavior for "opened but never started", but ADR-011 forbids a
	// confident positive state persisting indefinitely on stale evidence,
	// and StateOpened would otherwise never resolve to anything if the
	// master that sent OPEN dies before ever sending START. Reuse
	// SilenceInterval, this Timeline's only configured notion of "too long
	// without a packet"; see StateUnknown's doc comment.
	if t.state == StateOpened && t.lastSyncAtValid && now.Sub(t.lastSyncAt) >= t.cfg.SilenceInterval {
		t.setStateLocked(StateUnknown, now)
	}
}

// setStateLocked updates t.state and, only if the value is actually
// changing, stamps t.stateSince to now. Every assignment to t.state in this
// file goes through this helper instead of setting the field directly, so
// Snapshot.StateSince stays correct without every call site needing to
// remember an if-changed check of its own.
func (t *Timeline) setStateLocked(state State, now time.Time) {
	if state != t.state {
		t.stateSince = now
	}
	t.state = state
}

// isConflictingLocked reports whether a packet for filename from source
// should be treated as a competing master: the timeline must already have
// an established driving source and filename, the incoming filename must
// match it, and the incoming source must differ. An empty source or
// filename never conflicts, since there is nothing to compare it against.
func (t *Timeline) isConflictingLocked(filename, source string) bool {
	if source == "" || t.source == "" || filename == "" {
		return false
	}
	// A timeline that is stopped or has never been driven is not live: no
	// output is being actively driven for anything to contend over, so a
	// genuinely new master (an fppd restart, a different box entirely, or
	// simply the start of a new session) must be able to take over rather
	// than being locked out forever by whoever drove the previous session.
	// Without this, the review that added it reproduced a permanent wedge:
	// once a stale source claim was established, every subsequent packet
	// from any other source, including a legitimate new master, was
	// dropped as a conflict with no path back except a filename change.
	if t.state == StateStopped || t.state == StateUnknown {
		return false
	}
	return filename == t.filename && source != t.source
}

// beginFileLocked establishes filename/fileType/source as what this
// timeline is now tracking. If filename differs from what was previously
// tracked, this is a new session: correction counts, the last correction,
// and any prior conflict bookkeeping are reset, since they describe the
// previous file's playback, not this one's.
func (t *Timeline) beginFileLocked(filename string, fileType SyncFileType, source string) {
	if filename != t.filename {
		t.onTimeCount, t.slewCount, t.skipCount, t.jumpCount = 0, 0, 0, 0
		t.lastDrift = 0
		t.lastCorrection = CorrectionNone
		t.conflict = false
		t.conflictCount = 0
	}
	t.filename = filename
	t.fileType = fileType
	t.source = source
}

// applyOpenLocked handles action OPEN: the file is identified and readied,
// but not yet playing, so position is anchored and frozen rather than
// free-running. This also cancels any pending stop-blank delay, extending
// (as a judgment call) the same "new activity cancels the blank" behavior
// RES-002 states explicitly for START.
func (t *Timeline) applyOpenLocked(pkt SyncPacket, source string, now time.Time) {
	t.beginFileLocked(pkt.Filename, pkt.FileType, source)
	t.anchorPosition = t.positionFromPacketLocked(pkt)
	t.anchorAt = now
	t.setStateLocked(StateOpened, now)
	t.stopRequestedAt = time.Time{}
	t.lastSyncAt = now
	t.lastSyncAtValid = true
}

// applyStartLocked handles action START, including START with no
// preceding OPEN (RES-002 requires tolerating this): it (re)anchors
// position from the packet and begins free-running.
func (t *Timeline) applyStartLocked(pkt SyncPacket, source string, now time.Time) {
	t.beginFileLocked(pkt.Filename, pkt.FileType, source)
	t.anchorPosition = t.positionFromPacketLocked(pkt)
	t.anchorAt = now
	t.setStateLocked(StatePlaying, now)
	t.stopRequestedAt = time.Time{}
	t.lastSyncAt = now
	t.lastSyncAtValid = true
}

// applySyncLocked handles action SYNC. If there is no active
// Playing/Unsynchronized session for this filename, RES-002 requires
// treating this as an implicit START (a "bare SYNC" for a sequence that
// was never started). Otherwise it computes the drift between the master's
// reported position and the local free-run estimate and applies the
// matching correction class.
func (t *Timeline) applySyncLocked(pkt SyncPacket, source string, now time.Time) {
	live := (t.state == StatePlaying || t.state == StateUnsynchronized) && t.filename == pkt.Filename

	if !live {
		t.beginFileLocked(pkt.Filename, pkt.FileType, source)
		t.anchorPosition = t.positionFromPacketLocked(pkt)
		t.anchorAt = now
		t.setStateLocked(StatePlaying, now)
		t.stopRequestedAt = time.Time{}
		t.lastSyncAt = now
		t.lastSyncAtValid = true
		return
	}

	// Correction path, mirroring FPP's channeloutputthread.cpp bounds as
	// recorded in RES-002: slew within SlewThresholdFrames, jump beyond
	// JumpThreshold, skip for the moderate band in between. RES-002
	// describes these bounds for the case where the local free-run
	// estimate is behind the master; this implementation classifies by
	// absolute drift so an estimate that has drifted ahead is corrected
	// the same way, which is a judgment call beyond what RES-002
	// documents (free-running at the same nominal rate as the master
	// should rarely overshoot, but a defensive implementation should not
	// leave that case unhandled).
	masterPos := t.positionFromPacketLocked(pkt)
	localPos := t.estimatePositionLocked(now)
	delta := masterPos - localPos
	absDelta := delta
	if absDelta < 0 {
		absDelta = -absDelta
	}
	t.lastDrift = delta

	noCorrectionThreshold := time.Duration(t.cfg.NoCorrectionThresholdFrames) * t.stepTime
	slewThreshold := time.Duration(t.cfg.SlewThresholdFrames) * t.stepTime

	var kind CorrectionKind
	switch {
	case absDelta <= noCorrectionThreshold:
		kind = CorrectionOnTime
		// No adjustment: localPos is left as the current free-run
		// estimate. See Config.NoCorrectionThresholdFrames for why this
		// band exists (without it, a perfectly synced stream reports every
		// packet as a slew of zero drift).
	case absDelta <= slewThreshold:
		kind = CorrectionSlew
		// Gradual: close only a configured fraction of the gap now,
		// rather than snapping to the master's position; see
		// Config.SlewFractionPerSync.
		localPos += time.Duration(float64(delta) * t.cfg.SlewFractionPerSync)
	case absDelta > t.cfg.JumpThreshold:
		kind = CorrectionJump
		localPos = masterPos
	default:
		kind = CorrectionSkip
		localPos = masterPos
	}

	t.anchorPosition = localPos
	t.anchorAt = now
	t.lastCorrection = kind
	switch kind {
	case CorrectionOnTime:
		t.onTimeCount++
	case CorrectionSlew:
		t.slewCount++
	case CorrectionSkip:
		t.skipCount++
	case CorrectionJump:
		t.jumpCount++
	}

	t.setStateLocked(StatePlaying, now)
	t.lastSyncAt = now
	t.lastSyncAtValid = true
}

// applyStopLocked handles action STOP. Per RES-002, FPP remotes
// deliberately do not blank immediately: they hold the last position for
// about BlankDelayFrames so a back to back stop and start does not blink.
// This freezes the position estimate at the moment of STOP (output is no
// longer advancing) and moves to StateStopping; settleLocked resolves the
// transition to StateStopped once the blank delay has elapsed on the
// clock.
func (t *Timeline) applyStopLocked(pkt SyncPacket, source string, now time.Time) {
	frozen := t.estimatePositionLocked(now)

	if pkt.Filename != "" {
		t.filename = pkt.Filename
		t.fileType = pkt.FileType
	}
	if t.source == "" {
		t.source = source
	}

	t.anchorPosition = frozen
	t.anchorAt = now
	t.setStateLocked(StateStopping, now)
	t.stopRequestedAt = now
	t.lastSyncAt = now
	t.lastSyncAtValid = true
}

// estimatePositionLocked returns the estimated position at now. Only
// StatePlaying and StateUnsynchronized free-run; every other state is
// frozen at anchorPosition, since output is not advancing (not yet
// started, stopping, or stopped). This is the free-run behavior RES-002
// requires between sync packets: the timeline keeps advancing on its own
// clock through silence, and silence alone never stops, blanks, or resets
// it.
func (t *Timeline) estimatePositionLocked(now time.Time) time.Duration {
	if t.state != StatePlaying && t.state != StateUnsynchronized {
		return t.anchorPosition
	}
	elapsed := now.Sub(t.anchorAt)
	if elapsed < 0 {
		elapsed = 0
	}
	return t.anchorPosition + elapsed
}

// positionFromPacketLocked derives a position from a sync packet. RES-002:
// prefer SecondsElapsed when greater than zero, mirroring FPP's own
// remote; otherwise fall back to FrameNumber converted via the live step
// time. FPP's remote recomputes frame as round(seconds*1000/stepTimeMs);
// this is that relationship inverted, used only when SecondsElapsed is
// unusable (for example, zero from an FPP 8.x or earlier master).
//
// isSaneSecondsElapsed is checked here too, defensively, even though
// DecodeSync already rejects a non-finite or out-of-range SecondsElapsed at
// the wire boundary (see BLOCKER 3 in the review this responds to):
// Timeline is driven through the public Observe method, which any caller
// can call directly with a SyncPacket that did not come from DecodeSync.
// Trusting the field blindly here would reintroduce, one layer up, exactly
// the architecture-dependent float-to-Duration corruption DecodeSync exists
// to prevent.
func (t *Timeline) positionFromPacketLocked(pkt SyncPacket) time.Duration {
	if pkt.SecondsElapsed > 0 && isSaneSecondsElapsed(pkt.SecondsElapsed) {
		return time.Duration(float64(pkt.SecondsElapsed) * float64(time.Second))
	}
	return time.Duration(pkt.FrameNumber) * t.stepTime
}
