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

// AlignmentSnapshot reports this node's current program-to-LTC alignment:
// the session that holds this node's one LTC run, and, only while that
// session is actually Playing with a loaded handle and the wired engine
// can take an honest sample, the signed millisecond offset between the
// LTC timecode and the program position the shared pipeline is presenting
// for it, both read at the same pipeline running time.
func (m *Manager) AlignmentSnapshot(ctx context.Context) AlignmentSnapshot {
	s, reason := m.ltcHolderSession()
	if s == nil {
		return notMeasuredAlignment(reason)
	}

	s.mu.Lock()
	sessID := s.id
	handle := s.handle
	ready := s.handleLoaded && s.state == pkgaudio.StatePlaying
	override := s.desired.LTCStartOffset
	s.mu.Unlock()

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
		s.mu.Lock()
		held := s.ltcClaimState == LTCClaimHeld
		s.mu.Unlock()
		if held {
			return s, ""
		}
	}
	return nil, "no session on this node currently holds this node's one LTC run"
}
