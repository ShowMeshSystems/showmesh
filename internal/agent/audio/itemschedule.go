package audio

import (
	"context"
	"fmt"
	"time"

	pkgaudio "github.com/showmeshsystems/showmesh/pkg/audio"
)

// itemSchedule is a playing session's ADR-049 decision 8 item-boundary
// state: itemStartAt is T_i, the CURRENT item's own scheduled start
// instant on this node's media clock -- never its actual start, so a
// late or early decoder does not itself move the arithmetic. Once this
// item's own duration is known, boundaryAt (= itemStartAt + duration) is
// the instant the successor is scheduled to begin. An item whose
// duration is unknown leaves boundaryKnown false: nothing here schedules
// that item's own end, and the natural decoder-end path advances it
// exactly as an unscheduled session would (see watchTick).
type itemSchedule struct {
	itemStartAt   time.Time
	boundaryKnown bool
	boundaryAt    time.Time
}

// itemStage is the playlist's successor item, prepared ahead of
// schedule's own boundary (ADR-049 decision 8: "prepared early enough to
// be ready at its boundary"), retried every watch tick until it succeeds
// or the boundary is reached. index/identity name which item this
// attempt is for, so a playlist revision landing mid-flight is detected
// as stale rather than promoted wrongly. ready is true only once handle
// is actually loaded on the engine; err is set when the most recent
// attempt found a genuine fault (as opposed to a merely transient "the
// probe could not run yet").
type itemStage struct {
	index    int
	itemID   string
	identity string

	ready         bool
	err           error
	handle        EngineHandle
	durationKnown bool
	duration      time.Duration
}

// anchorItemScheduleLocked installs (or clears) a session's item-boundary
// schedule once its engine has actually started or resumed against a T0:
// T_i, this item's own scheduled start instant, is sched.t0 minus
// position, so a scheduled resume mid-item (position p at instant R)
// re-anchors to T_i = R - p exactly as if this item had itself begun
// there (ADR-049 decision 8's own resume rule), never sched.t0 itself,
// which would treat the whole elapsed position as scheduling drift. A
// nil sched (unscheduled start, or a node with no usable clock) clears
// any schedule this session held instead. Caller holds s.mu.
func (m *Manager) anchorItemScheduleLocked(ctx context.Context, s *Session, sched *startSchedule, position time.Duration) {
	if sched == nil {
		s.schedule = nil
		s.discardStageLocked(ctx)
		return
	}
	s.schedule = &itemSchedule{itemStartAt: sched.t0.Add(-position)}
	s.refreshBoundaryFromProbeLocked()
	s.discardStageLocked(ctx)
}

// refreshBoundaryFromProbeLocked recomputes s.schedule's own boundary
// from s.lastProbe -- the just-loaded current item's own probe evidence.
// A no-op when there is no schedule to refresh. Caller holds s.mu.
func (s *Session) refreshBoundaryFromProbeLocked() {
	if s.schedule == nil {
		return
	}
	s.schedule.boundaryKnown = s.lastProbe.DurationKnown
	if s.schedule.boundaryKnown {
		s.schedule.boundaryAt = s.schedule.itemStartAt.Add(s.lastProbe.Duration)
	} else {
		s.schedule.boundaryAt = time.Time{}
	}
}

// reanchorScheduleAfterNaturalAdvanceLocked re-anchors s.schedule once a
// duration-unknown item's decoder-end advance has moved s onto a new
// item: the shared boundary arithmetic cannot continue from a duration
// it never had, so this node re-anchors T to its own fresh media-clock
// reading instead, best effort. A node with no readable media clock at
// this instant cannot continue the schedule at all, and drops it, so
// every later item on this node plays decoder-end exactly like an
// unscheduled session rather than silently keeping a schedule anchored
// to a reading that never happened. Caller holds s.mu.
func (m *Manager) reanchorScheduleAfterNaturalAdvanceLocked(ctx context.Context, s *Session) {
	source := m.clockSourceSnapshot()
	if source == nil {
		s.schedule = nil
		s.discardStageLocked(ctx)
		return
	}
	mediaNow := source.Now(ctx)
	if !mediaNow.Valid {
		s.schedule = nil
		s.discardStageLocked(ctx)
		return
	}
	s.schedule = &itemSchedule{itemStartAt: mediaNow.Time}
	s.refreshBoundaryFromProbeLocked()
	s.discardStageLocked(ctx)
}

// maybeStageNextItemLocked attempts, once per watch tick, to prepare the
// playlist's successor item ahead of its own scheduled boundary (ADR-049
// decision 8): a probe-and-load that would otherwise run AT the
// boundary, paying its own latency against the boundary's precision. A
// transient probe result ([MediaUnknown]: the probe itself could not run
// right now) or a Load failure is retried on the next tick rather than
// failing the session outright -- only [Manager.scheduledAdvanceLocked],
// once the boundary is actually reached, decides whether a still-unready
// stage means "wait one more tick" or "fail". Caller holds s.mu.
func (m *Manager) maybeStageNextItemLocked(ctx context.Context, s *Session) {
	if s.schedule == nil || !s.schedule.boundaryKnown || s.desired.Playlist == nil {
		return
	}
	next, ok := nextPlaylistIndexLocked(s.desired.Playlist, s.currentIndex)
	if !ok {
		s.discardStageLocked(ctx)
		return
	}
	item := s.desired.Playlist.Items[next]
	identity := itemIdentity(item)
	if s.stage != nil && s.stage.ready && s.stage.index == next && s.stage.identity == identity {
		return // already staged and ready; nothing more to do until promoted
	}

	s.discardStageLocked(ctx)
	s.stageSeq++
	handle := EngineHandle(fmt.Sprintf("%s/stage/%d", s.id, s.stageSeq))

	probe := ProbeAsset(ctx, m.assetDir, item.Media, m.decoder)
	switch {
	case probe.State == MediaUnknown:
		// The probe itself could not run right now -- not a fault, just
		// not resolvable yet. Retried next tick.
		s.stage = &itemStage{index: next, itemID: item.ItemID, identity: identity}
	case probe.State != MediaReady:
		s.stage = &itemStage{
			index: next, itemID: item.ItemID, identity: identity,
			err: fmt.Errorf("media not ready: %s", probe.Reason),
		}
	default:
		loadCtx, cancel := boundedEngineCallContext(ctx)
		_, err := m.engine.Load(loadCtx, handle, item.Media, probe.Duration)
		cancel()
		if err != nil {
			s.stage = &itemStage{index: next, itemID: item.ItemID, identity: identity, err: err}
			return
		}
		s.stage = &itemStage{
			index: next, itemID: item.ItemID, identity: identity, handle: handle,
			ready: true, durationKnown: probe.DurationKnown, duration: probe.Duration,
		}
	}
}

// scheduledAdvanceLocked performs one ADR-049 decision 8 item-boundary
// transition: the playlist's next item begins at s.schedule's own
// boundary instant, on this node's media clock, regardless of whether
// the predecessor's own decoder has actually reached its end -- an early
// decoder end plays nothing further until the boundary; a late one is
// cut off at it. mediaNow is the media-clock reading that showed the
// boundary had already been reached, so the lateness reported here is
// measured entirely on the media clock, never mixed with evidence from
// the engine's own (possibly different) clock domain. Caller holds s.mu.
func (m *Manager) scheduledAdvanceLocked(ctx context.Context, s *Session, mediaNow time.Time) {
	boundaryAt := s.schedule.boundaryAt
	lateBy := mediaNow.Sub(boundaryAt)
	if lateBy < 0 {
		lateBy = 0
	}

	next, ok := nextPlaylistIndexLocked(s.desired.Playlist, s.currentIndex)
	if !ok {
		// No successor (RepeatNone exhausted): nothing left for a
		// boundary to schedule. The current item's own natural
		// completion (watchTick's decoder-end path) still ends it.
		s.schedule = nil
		s.discardStageLocked(ctx)
		return
	}

	item := s.desired.Playlist.Items[next]
	identity := itemIdentity(item)
	stage := s.stage
	staged := stage != nil && stage.index == next && stage.identity == identity

	if !staged || !stage.ready {
		if staged && stage.err != nil {
			m.logf("audio session %s: item %s's successor could not be prepared: %v", s.id, s.currentItemID, stage.err)
			s.releaseEngineLocked(ctx)
			s.state = pkgaudio.StateFailed
			s.setFaultLocked(pkgaudio.ClassifyFault(stage.err), stage.err.Error())
			s.schedule = nil
			s.discardStageLocked(ctx)
			m.stopLTCLocked(ctx, s)
			s.persistBestEffortLocked("state change")
			return
		}
		// Still preparing, or no tick ever got the chance to start it (an
		// item shorter than one watch tick): try once more right now, and
		// wait for a later tick if it still is not ready. This never
		// holds the schedule open indefinitely, but it also never starts
		// an item that does not exist yet.
		m.maybeStageNextItemLocked(ctx, s)
		return
	}

	s.releaseEngineLocked(ctx)
	s.currentIndex = next
	s.currentItemID = item.ItemID
	s.state = pkgaudio.StatePreparing
	s.bookmark = nil
	s.persistBestEffortLocked("scheduled item boundary")

	s.handle = stage.handle
	s.handleLoaded = true
	s.loadedIdentity = stage.identity
	s.lastProbe = MediaItemResult{State: MediaReady, DurationKnown: stage.durationKnown, Duration: stage.duration}
	m.applyEffectiveGainBestEffortLocked(ctx, s)

	startCtx, cancel := boundedEngineCallContext(ctx)
	obs, err := m.engine.Start(startCtx, s.handle, 0)
	cancel()
	if err != nil {
		s.state = pkgaudio.StateFailed
		s.setFaultLocked(pkgaudio.ClassifyFault(err), err.Error())
		s.handleLoaded = false
		s.loadedIdentity = ""
		s.schedule = nil
		s.stage = nil
		m.stopLTCLocked(ctx, s)
		s.persistBestEffortLocked("state change")
		return
	}
	s.state = pkgaudio.StatePlaying
	s.timingKnown = true
	s.lastObservedAt = obs.ObservedAt
	if lateBy > 0 {
		m.logf("audio session %s: item %s started %s late at its scheduled boundary", s.id, item.ItemID, lateBy)
	}
	s.setGapKnownLocked(lateBy, obs.ObservedAt)
	m.startLTCLocked(ctx, s, 0)

	s.stage = nil
	s.schedule = &itemSchedule{itemStartAt: boundaryAt}
	s.refreshBoundaryFromProbeLocked()

	s.persistBestEffortLocked("state change")
}
