package audio

import (
	"context"
	"fmt"
	"time"

	agentclock "github.com/showmeshsystems/showmesh/internal/agent/clock"
	pkgaudio "github.com/showmeshsystems/showmesh/pkg/audio"
)

// ClockSource is the read side of this node's media clock a scheduled
// session needs: the same two calls internal/agent/clock.Manager already
// offers, narrowed to what this package uses so a test can substitute a
// clock without building a provider. Nothing here ever configures,
// starts, or steps a clock.
type ClockSource interface {
	Poll(ctx context.Context) agentclock.Status
	Now(ctx context.Context) agentclock.MediaTime
}

// Resync reasons: the three causes that can make a discontinuity, and
// nothing else. Each names an event observed from evidence, never a
// conclusion drawn from the size of the error.
const (
	ResyncReasonPTPStep         = "ptp_step"
	ResyncReasonProviderRestart = "provider_restart"
	ResyncReasonDeviceChange    = "device_change"
)

// timeline is one scheduled session's presentation timeline: the T0 it
// was given, the anchors it judges a discontinuity's CAUSE against, and
// the most recent measurement.
//
// THE RULE THIS TYPE EXISTS TO HOLD (RES-019 section 6, as the owner
// narrowed it): an error beyond the configured threshold AFTER a PTP
// step, a clock provider restart, or a device change is a discontinuity,
// answered with one flushing seek and reported as a resync. Nothing else
// moves the position. The gate is on cause; the threshold can only ever
// SUPPRESS a seek a cause would otherwise justify, and there is no path
// in this file from a magnitude alone to a seek. A sound card's own skew
// correction, which produces a recurring step of tens of milliseconds
// with no step, no restart and no device change behind it, therefore
// cannot move a show's position at any threshold value.
type timeline struct {
	// t0 is the instant this session was told to present its first
	// sample, read on THIS node's media clock. startPosition is where in
	// the media that sample sits, so a resync seek targets
	// startPosition+expected rather than expected alone.
	t0            time.Time
	startPosition time.Duration

	// presentedAtStart is the engine's sink-clock reading when this
	// session started, the baseline every later reading is differenced
	// against. presentedKnown is false when the engine could not report
	// one, which makes actual and error unmeasurable rather than zero.
	presentedAtStart time.Duration
	presentedKnown   bool

	// lastStepAt/lastStepKnown are the most recent PTP step this timeline
	// has ALREADY accounted for. A poll reporting a step strictly newer
	// than this one is a step that happened since the last evaluation.
	lastStepAt    time.Time
	lastStepKnown bool

	// sawUnlocked records that this timeline has observed the clock
	// provider outside StateLocked since its last evaluation. Locked
	// again after that is a provider restart (RES-019 section 9: the
	// ptp4l owner stopping or restarting it is failed then acquiring),
	// which is evidence of a lock episode broken and remade rather than
	// an inference from the numbers moving.
	sawUnlocked bool

	// engineEpoch is the engine binding this timeline is anchored to. A
	// different epoch means an audio.node rebind, a device change, or a
	// sample-rate rebuild swapped the pipeline out underneath it; see
	// [Manager.RebindEngine], the single path all three take.
	engineEpoch uint64

	resyncs          int64
	lastResyncReason string

	// measured/expected/actual/errorDur are the most recent evaluation.
	// measuredReason is required whenever measured is false, naming what
	// was missing rather than reporting a zero error.
	measured       bool
	measuredReason string
	expected       time.Duration
	actual         time.Duration
	errorDur       time.Duration
}

// TimelineSnapshot is a node's node.audio.timeline.* evidence for one
// report tick. Scheduled and Measured are separate because they answer
// separate questions: a session can hold a T0 (so its scheduled instant
// and resync count are real) on a tick where the media clock or the sink
// clock could not be read (so expected, actual and error are not).
type TimelineSnapshot struct {
	// Scheduled is true when a session on this node is currently playing
	// against a T0. ScheduledAtNs, Resyncs and LastResyncReason are
	// meaningful only then; Reason says why not when it is false.
	Scheduled     bool
	SessionID     pkgaudio.SessionID
	ScheduledAtNs int64

	Resyncs          int64
	LastResyncReason string

	// Measured is true when this tick actually read both clocks.
	// ExpectedMs, ActualMs and ErrorMs are meaningful only then, and
	// Reason states what was missing otherwise.
	Measured   bool
	ExpectedMs int64
	ActualMs   int64
	ErrorMs    int64

	Reason string
}

// notScheduledSnapshot is what a node with no session playing against a
// T0 reports: not collected with a reason, never a zeroed timeline that
// would read as a session perfectly on time.
func notScheduledSnapshot(reason string) TimelineSnapshot {
	return TimelineSnapshot{Reason: reason}
}

// TimelineSnapshot reports this node's current scheduled-playback
// timeline. A node running more than one scheduled session at once
// reports the lowest session id among them, deterministically, rather
// than whichever the map iterated first.
func (m *Manager) TimelineSnapshot(ctx context.Context) TimelineSnapshot {
	m.mu.Lock()
	sessions := make([]*Session, 0, len(m.sessions))
	for _, s := range m.sessions {
		sessions = append(sessions, s)
	}
	m.mu.Unlock()

	var best TimelineSnapshot
	for _, s := range sessions {
		s.mu.Lock()
		snap, ok := s.timelineSnapshotLocked()
		s.mu.Unlock()
		if !ok {
			continue
		}
		if !best.Scheduled || snap.SessionID < best.SessionID {
			best = snap
		}
	}
	if !best.Scheduled {
		return notScheduledSnapshot("no session on this node is playing against a scheduled start instant")
	}
	return best
}

// timelineSnapshotLocked reports s's timeline, or ok=false when it holds
// none. Caller holds s.mu.
func (s *Session) timelineSnapshotLocked() (TimelineSnapshot, bool) {
	if s.timeline == nil || s.state != pkgaudio.StatePlaying {
		return TimelineSnapshot{}, false
	}
	t := s.timeline
	snap := TimelineSnapshot{
		Scheduled:        true,
		SessionID:        s.id,
		ScheduledAtNs:    t.t0.UnixNano(),
		Resyncs:          t.resyncs,
		LastResyncReason: t.lastResyncReason,
		Measured:         t.measured,
		Reason:           t.measuredReason,
	}
	if t.measured {
		snap.ExpectedMs = t.expected.Milliseconds()
		snap.ActualMs = t.actual.Milliseconds()
		snap.ErrorMs = t.errorDur.Milliseconds()
	}
	return snap, true
}

// evaluateTimelineLocked runs one timeline evaluation for a playing
// scheduled session: measure, establish whether a discontinuity CAUSE
// occurred since the last evaluation, and seek only when one did and the
// error exceeds the configured threshold. Caller holds s.mu.
func (m *Manager) evaluateTimelineLocked(ctx context.Context, s *Session) {
	t := s.timeline
	if t == nil || s.state != pkgaudio.StatePlaying {
		return
	}

	source := m.clockSourceSnapshot()
	if source == nil {
		t.markUnmeasured("this node has no media clock wired; a scheduled timeline cannot be measured")
		return
	}

	status := source.Poll(ctx)
	cause := t.consumeCause(status, m.engineEpoch.Load())

	mediaNow := source.Now(ctx)
	if !mediaNow.Valid {
		t.markUnmeasured("media clock unreadable: " + mediaNow.Reason)
		return
	}
	presented, presentedOK, presentedReason := ObserveEngineSinkClock(ctx, m.engine)
	if !presentedOK {
		t.markUnmeasured("sink clock unreadable: " + presentedReason)
		return
	}
	// A device change replaced the pipeline, so the presented running
	// time this reading came from restarted and cannot be differenced
	// against a baseline taken on the previous one.
	if cause == ResyncReasonDeviceChange || !t.presentedKnown {
		t.presentedAtStart = presented
		t.presentedKnown = true
	}

	t.expected = mediaNow.Time.Sub(t.t0)
	t.actual = presented - t.presentedAtStart
	t.errorDur = t.expected - t.actual
	t.measured = true
	t.measuredReason = ""

	if cause == "" {
		// The whole point of this file. A large error with no cause
		// behind it is reported and left alone: it is evidence about the
		// output, not a reason to move a running show's position. This is
		// the gate; everything below it is a secondary condition INSIDE a
		// discontinuity that has already been established from evidence.
		return
	}
	threshold, ok := m.driftThreshold()
	if !ok {
		return
	}
	// STRICTLY greater, chosen deliberately rather than typed by
	// accident, and it decides a real case rather than a hypothetical
	// one. The field is a drift IGNORE threshold: an error exactly at it
	// is on the ignore side by the setting's own name. The audio sink's
	// own skew correction is measured to fire at exactly 20.000000 ms,
	// and the value this threshold is expected to carry is also 20, so
	// an error landing exactly on the threshold is the one number where
	// the two coincide. Greater-or-equal would seek there. The
	// comparison itself runs at the duration's full nanosecond
	// precision; only the REPORTED error_ms truncates to milliseconds.
	if absDuration(t.errorDur) <= threshold {
		return
	}
	m.resyncLocked(ctx, s, cause, presented)
}

// consumeCause reports which of the three discontinuity causes has
// occurred since the previous evaluation, and folds the evidence for it
// into this timeline's own anchors so the same event cannot fire twice.
// It reads only observed evidence: the tracker's own step instants, the
// lock episode, and the engine binding epoch. It never looks at the
// error.
func (t *timeline) consumeCause(status agentclock.Status, engineEpoch uint64) string {
	cause := ""

	if status.LastStepKnown && (!t.lastStepKnown || status.LastStepAt.After(t.lastStepAt)) {
		t.lastStepAt, t.lastStepKnown = status.LastStepAt, true
		cause = ResyncReasonPTPStep
	}

	if status.State != agentclock.StateLocked {
		t.sawUnlocked = true
	} else if t.sawUnlocked {
		t.sawUnlocked = false
		if cause == "" {
			cause = ResyncReasonProviderRestart
		}
	}

	if engineEpoch != t.engineEpoch {
		t.engineEpoch = engineEpoch
		cause = ResyncReasonDeviceChange
	}
	return cause
}

// markUnmeasured records why this tick produced no measurement. The
// previous expected/actual/error are dropped rather than left standing:
// a stale measurement reported as current is the thing this whole seam
// exists not to do.
func (t *timeline) markUnmeasured(reason string) {
	t.measured = false
	t.measuredReason = reason
	t.expected, t.actual, t.errorDur = 0, 0, 0
}

// resyncLocked performs the one flushing seek a confirmed discontinuity
// earns, to the media position this session should be playing now, and
// re-anchors the timeline against the reading that seek lands on. Caller
// holds s.mu.
func (m *Manager) resyncLocked(ctx context.Context, s *Session, cause string, presented time.Duration) {
	t := s.timeline
	target := t.startPosition + t.expected
	if target < 0 {
		target = 0
	}
	seekCtx, cancel := boundedEngineCallContext(ctx)
	obs, err := m.engine.Seek(seekCtx, s.handle, target)
	cancel()
	if err != nil {
		m.logf("audio session %s: scheduled timeline resync seek failed (%s): %v", s.id, cause, err)
		s.setFaultLocked(pkgaudio.ClassifyFault(err), err.Error())
		return
	}
	s.lastObservedAt = obs.ObservedAt
	// A seek is a discontinuity by [Engine.Seek]'s own contract: the
	// position it lands on must not be reported as a continuation of the
	// timing that preceded it.
	s.timingKnown = false
	t.presentedAtStart = presented - t.expected
	t.actual = t.expected
	t.errorDur = 0
	t.resyncs++
	t.lastResyncReason = cause
	m.logf("audio session %s: scheduled timeline resynced to %s after %s", s.id, target, cause)
}

// driftThreshold reports the configured discontinuity threshold. A node
// that has never received an audio.settings push, or one pushed a
// non-positive value, has no usable threshold: ok is false and no seek
// can happen. This package picks no value of its own.
func (m *Manager) driftThreshold() (time.Duration, bool) {
	s := m.SettingsSnapshot()
	if !s.Configured || s.DriftIgnoreThresholdMs <= 0 {
		return 0, false
	}
	return time.Duration(s.DriftIgnoreThresholdMs) * time.Millisecond, true
}

func absDuration(d time.Duration) time.Duration {
	if d < 0 {
		return -d
	}
	return d
}

// SetClockSource wires this node's media clock. internal/agent's own
// startup is the only caller; a Manager with none reports no scheduled
// timeline and refuses no start, since a node with no clock provider is
// exactly the node that keeps today's start-on-arrival behaviour.
func (m *Manager) SetClockSource(src ClockSource) {
	m.clockMu.Lock()
	m.clockSource = src
	m.clockMu.Unlock()
}

// clockSourceSnapshot returns the wired [ClockSource], or nil.
func (m *Manager) clockSourceSnapshot() ClockSource {
	m.clockMu.RLock()
	defer m.clockMu.RUnlock()
	return m.clockSource
}

// SetOutputLatency stores this node's currently bound calibrated output
// latency (RES-019 section 8), signed microseconds, read by every
// subsequent scheduled start until the next audio.node.configure
// delivery replaces it (internal/agent/audioengine.go's rebuildLocked is
// the one caller, resolving the wire method to zero before calling this
// when it is "unmeasured": see outputLatencyConfig.effectiveOutputLatencyUs).
func (m *Manager) SetOutputLatency(latencyUs int) {
	m.outputLatencyUs.Store(int64(latencyUs))
}

// outputLatencyUsSnapshot returns the currently bound output latency, in
// signed microseconds. Zero (the default before any audio.node.configure
// has ever delivered a measured value) applies no adjustment to a
// scheduled start.
func (m *Manager) outputLatencyUsSnapshot() int64 {
	return m.outputLatencyUs.Load()
}

// maxScheduledStartLead bounds how far into the future a start instant
// may sit before this node refuses it. A scheduled start holds its
// session's lock until T0 arrives, so an instant days away would wedge
// the session rather than schedule it.
//
// SHOWMESH HYPOTHESIS, NOT MEASURED: RES-019 section 6 makes T0 the
// coordinator's own "max node ready time plus a command delivery bound
// plus margin", which is a sub-second quantity in every deployment
// anyone has described; 30 seconds is chosen to be far above that while
// still bounding the wedge. It is not the drift threshold and has
// nothing to do with it.
var maxScheduledStartLead = 30 * time.Second // var, not const: shrunk by tests exercising the bound itself

// startSchedule is one accepted scheduled start: the T0 to present the
// first sample at, and the clock evidence the timeline anchors against.
type startSchedule struct {
	source ClockSource
	t0     time.Time
	status agentclock.Status
}

// waitUntilT0 blocks until this node's media clock reaches T0, re-reading
// the media clock rather than sleeping on the wall clock: the two are
// different clocks, and a sleep measured on the wrong one is the whole
// class of error this seam exists to remove. Returns ctx's error if the
// command is cancelled first.
func (sc *startSchedule) waitUntilT0(ctx context.Context) error {
	for {
		now := sc.source.Now(ctx)
		if !now.Valid {
			return fmt.Errorf("media clock became unreadable while waiting for the start instant: %s", now.Reason)
		}
		remaining := sc.t0.Sub(now.Time)
		if remaining <= 0 {
			return nil
		}
		if remaining > scheduledStartPollInterval {
			remaining = scheduledStartPollInterval
		}
		timer := time.NewTimer(remaining)
		select {
		case <-ctx.Done():
			timer.Stop()
			return ctx.Err()
		case <-timer.C:
		}
	}
}

// scheduledStartPollInterval is how long waitUntilT0 sleeps before
// re-reading the media clock, so a clock that steps while a node waits
// is noticed rather than slept through.
//
// SHOWMESH HYPOTHESIS, NOT MEASURED.
var scheduledStartPollInterval = 100 * time.Millisecond

// resolveScheduleLocked decides what a requested start instant means on
// this node right now. It returns at most one of: an accepted schedule,
// a note explaining why the instant was ignored and the start proceeds
// on arrival, or a refusal. Caller holds s.mu.
//
// Ignoring is deliberate and is the seam's stated behaviour for a node
// whose provider is not locked: it keeps today's start-on-arrival path
// exactly, and says so. Refusing is reserved for an instant that is
// itself unusable, which a past T0 is.
func (m *Manager) resolveScheduleLocked(ctx context.Context, scheduledAtNs *int64) (*startSchedule, string, *pkgaudio.OutcomeResult) {
	if scheduledAtNs == nil {
		return nil, "", nil
	}
	source := m.clockSourceSnapshot()
	if source == nil {
		return nil, "started on arrival: this node has no media clock wired, so the requested start instant was ignored", nil
	}
	status := source.Poll(ctx)
	if status.State != agentclock.StateLocked {
		return nil, fmt.Sprintf("started on arrival: this node's clock provider reports %q (%s), so the requested start instant was ignored", status.State, status.Reason), nil
	}
	mediaNow := source.Now(ctx)
	if !mediaNow.Valid {
		return nil, "started on arrival: this node's media clock is unreadable (" + mediaNow.Reason + "), so the requested start instant was ignored", nil
	}

	t0 := time.Unix(0, *scheduledAtNs)
	// RES-019 section 8: this node's output chain (USB isochronous depth,
	// ALSA period/buffer, PipeWire quantum, DAC group delay) delays a
	// sample between engine start and the air by a calibrated amount, so
	// the engine must start that much EARLIER for the sample to reach the
	// air at the requested instant. Zero (unmeasured, or no
	// audio.node.configure delivery yet) leaves T0 unadjusted, exactly
	// today's behavior.
	latencyUs := m.outputLatencyUsSnapshot()
	if latencyUs != 0 {
		t0 = t0.Add(-time.Duration(latencyUs) * time.Microsecond)
	}
	if !t0.After(mediaNow.Time) {
		latencyNote := ""
		if latencyUs != 0 {
			latencyNote = fmt.Sprintf(" (adjusted from the requested %d by this node's calibrated output latency of %dus)", *scheduledAtNs, latencyUs)
		}
		return nil, "", &pkgaudio.OutcomeResult{
			Outcome: pkgaudio.OutcomeRefused,
			Reason: fmt.Sprintf("%s: the adjusted start instant %d%s is %s behind this node's media clock, which already reads %d; this node refuses rather than starting late",
				pkgaudio.ReasonScheduledStartInPast, t0.UnixNano(), latencyNote, mediaNow.Time.Sub(t0), mediaNow.Time.UnixNano()),
		}
	}
	if lead := t0.Sub(mediaNow.Time); lead > maxScheduledStartLead {
		return nil, "", &pkgaudio.OutcomeResult{
			Outcome: pkgaudio.OutcomeRefused,
			Reason:  fmt.Sprintf("the requested start instant is %s ahead of this node's media clock, beyond the %s a scheduled start may wait", lead, maxScheduledStartLead),
		}
	}
	return &startSchedule{source: source, t0: t0, status: status}, "", nil
}

// anchorTimelineLocked installs (or clears) a session's timeline once its
// engine has actually started. The anchors come from the clock evidence
// read when the schedule was accepted, so a PTP step that happened BEFORE
// this start can never be counted as one that happened after it. Caller
// holds s.mu.
func (m *Manager) anchorTimelineLocked(ctx context.Context, s *Session, sched *startSchedule, position time.Duration) {
	if sched == nil {
		s.timeline = nil
		return
	}
	t := &timeline{
		t0:            sched.t0,
		startPosition: position,
		lastStepAt:    sched.status.LastStepAt,
		lastStepKnown: sched.status.LastStepKnown,
		engineEpoch:   m.engineEpoch.Load(),
	}
	if presented, ok, _ := ObserveEngineSinkClock(ctx, m.engine); ok {
		t.presentedAtStart, t.presentedKnown = presented, true
	}
	s.timeline = t
}

// PrerollLatency reports how long id's most recent successful
// [Manager.Prepare] took, and whether one has ever succeeded. This is
// what a coordinator adds to a start instant so every node is prerolled
// before T0; a node that has never prepared reports known=false rather
// than a zero that would read as "instantaneous".
func (m *Manager) PrerollLatency(id pkgaudio.SessionID) (time.Duration, bool) {
	s, ok := m.get(id)
	if !ok {
		return 0, false
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.prerollLatency, s.prerollKnown
}

// MediaNow reports a fresh reading of this node's media clock, taken from
// the clock provider's own Now and never from the tracker's status or
// from wall time: those answer "what state is the clock in" and "what
// time does this host think it is", which are different questions from
// "what does the media clock read right now".
//
// A node with no clock wired reports an INVALID reading with a reason,
// exactly as an unconfigured provider does, so no caller can mistake a
// missing clock for a working one.
func (m *Manager) MediaNow(ctx context.Context) agentclock.MediaTime {
	source := m.clockSourceSnapshot()
	if source == nil {
		return agentclock.MediaTime{Valid: false, Reason: "no media clock is wired on this node"}
	}
	return source.Now(ctx)
}
