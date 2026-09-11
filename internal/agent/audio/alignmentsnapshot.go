package audio

import (
	"context"
	"time"

	pkgaudio "github.com/showmeshsystems/showmesh/pkg/audio"
)

// AlignmentSnapshot is a node's node.audio.clock.alignment evidence for
// one report tick. Measured is false, with Reason stating why, whenever
// no honest sample could be taken this tick: never a cached value from
// an earlier tick, and never a value inferred from anything but a fresh
// read of the pipeline itself.
type AlignmentSnapshot struct {
	Measured  bool
	OffsetMs  int64
	SampledAt time.Time
	SessionID pkgaudio.SessionID
	Reason    string
}

// notMeasuredAlignment is what a node with no honest sample reports, not
// collected with a reason, never a zeroed offset that would read as
// perfectly aligned.
func notMeasuredAlignment(reason string) AlignmentSnapshot {
	return AlignmentSnapshot{Reason: reason}
}

// alignmentSessionFields is the small slice of one session's state
// [Manager.AlignmentSnapshot] needs, read together under s.mu by
// [Session.alignmentFieldsWithBudget].
type alignmentSessionFields struct {
	id       pkgaudio.SessionID
	handle   EngineHandle
	ready    bool
	override *pkgaudio.LTCTimecode
}

// alignmentFieldsWithBudget reads s's alignment-relevant fields under
// s.mu, falling back to (zero, false) if that lock is not free within
// budget, the same bounded pattern [Session.snapshotWithBudget] uses:
// a report tick must never stall on one session's engine call.
func (s *Session) alignmentFieldsWithBudget(budget time.Duration) (alignmentSessionFields, bool) {
	done := make(chan alignmentSessionFields, 1)
	go func() {
		s.mu.Lock()
		f := alignmentSessionFields{
			id:       s.id,
			handle:   s.handle,
			ready:    s.handleLoaded && s.state == pkgaudio.StatePlaying,
			override: s.desired.LTCStartOffset,
		}
		s.mu.Unlock()
		done <- f
	}()
	select {
	case f := <-done:
		return f, true
	case <-time.After(budget):
		return alignmentSessionFields{}, false
	}
}

// AlignmentSnapshot reports this node's current program-to-LTC alignment:
// the signed millisecond offset between LTC and program position for the
// session holding this node's one LTC run, both read at the same running time.
func (m *Manager) AlignmentSnapshot(ctx context.Context) AlignmentSnapshot {
	s, reason := m.ltcHolderSession()
	if s == nil {
		return notMeasuredAlignment(reason)
	}

	fields, ok := s.alignmentFieldsWithBudget(snapshotLockBudget)
	if !ok {
		return notMeasuredAlignment("this node's LTC-holding session lock was busy past the alignment snapshot's lock budget")
	}
	sessID := fields.id
	handle := fields.handle
	ready := fields.ready
	override := fields.override

	if !ready {
		snap := notMeasuredAlignment("this node's LTC-holding session is not currently playing with a loaded engine handle")
		snap.SessionID = sessID
		return snap
	}

	sample, known, reason := ObserveEngineAlignment(ctx, m.engine, handle)
	if !known {
		snap := notMeasuredAlignment(reason)
		snap.SessionID = sessID
		return snap
	}

	_, defaultOffset, ok, reason := m.resolveLTCSpec()
	if !ok {
		snap := notMeasuredAlignment(reason)
		snap.SessionID = sessID
		return snap
	}
	base := ResolveLTCStartOffset(override, defaultOffset)

	expected, err := base.Advance(sample.ProgramPosition, sample.LTCFrameRate)
	if err != nil {
		snap := notMeasuredAlignment("could not resolve this session's expected LTC timecode: " + err.Error())
		snap.SessionID = sessID
		return snap
	}

	offsetMs, err := sample.LTCTimecode.DiffMs(expected, sample.LTCFrameRate)
	if err != nil {
		snap := notMeasuredAlignment("could not compute the alignment offset: " + err.Error())
		snap.SessionID = sessID
		return snap
	}

	return AlignmentSnapshot{Measured: true, OffsetMs: offsetMs, SampledAt: sample.SampledAt, SessionID: sessID}
}

// ltcClaimHeldWithBudget reports whether s currently holds this node's LTC
// run, falling back to (false, false) if s.mu is not free within budget:
// the same bounded pattern [Session.snapshotWithBudget] uses.
func (s *Session) ltcClaimHeldWithBudget(budget time.Duration) (held bool, ok bool) {
	done := make(chan bool, 1)
	go func() {
		s.mu.Lock()
		held := s.ltcClaimState == LTCClaimHeld
		s.mu.Unlock()
		done <- held
	}()
	select {
	case held := <-done:
		return held, true
	case <-time.After(budget):
		return false, false
	}
}

// ltcHolderSession returns the one session, if any, that currently holds
// this node's one LTC run (see ltcOwner's own doc comment on why at most
// one ever can), or (nil, reason) when none does.
func (m *Manager) ltcHolderSession() (*Session, string) {
	m.mu.Lock()
	sessions := make([]*Session, 0, len(m.sessions))
	for _, s := range m.sessions {
		sessions = append(sessions, s)
	}
	m.mu.Unlock()

	for _, s := range sessions {
		held, ok := s.ltcClaimHeldWithBudget(snapshotLockBudget)
		if !ok {
			return nil, "a session's lock was busy past the alignment snapshot's lock budget"
		}
		if held {
			return s, ""
		}
	}
	return nil, "no session on this node currently holds this node's one LTC run"
}
