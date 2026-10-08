package audio

import (
	"context"
	"fmt"
	"time"

	agentclock "github.com/showmeshsystems/showmesh/internal/agent/clock"
	pkgaudio "github.com/showmeshsystems/showmesh/pkg/audio"
)

// ScheduleClockIdentity names the media clock a persisted schedule's
// instants were read on: everything internal/agent/clock reports that
// survives this process, since a lock episode does not.
type ScheduleClockIdentity struct {
	Provider            agentclock.ProviderKind
	Domain              int
	DomainKnown         bool
	GrandmasterIdentity string
	Timescale           agentclock.Timescale
}

// scheduleClockIdentityOf returns status's clock identity, or nil when
// the provider is not locked or does not name its grandmaster: a clock
// nothing identifies cannot be recognised again after a restart.
func scheduleClockIdentityOf(status agentclock.Status) *ScheduleClockIdentity {
	if status.State != agentclock.StateLocked || !status.GMKnown || status.GrandmasterIdentity == "" {
		return nil
	}
	return &ScheduleClockIdentity{
		Provider:            status.Provider,
		Domain:              status.Domain,
		DomainKnown:         status.DomainKnown,
		GrandmasterIdentity: status.GrandmasterIdentity,
		Timescale:           status.Timescale,
	}
}

// restartRejoin is what a persisted schedule means for one restore.
// onSchedule rejoins at the expected position; ended means that position
// is past the end of a session that does not repeat. Neither set restores
// as a record with no clock identity always has.
type restartRejoin struct {
	onSchedule bool
	ended      bool

	index       int
	item        pkgaudio.PlaylistItem
	itemStartAt time.Time
	duration    time.Duration
	position    time.Duration
	sameItem    bool

	source ClockSource
	status agentclock.Status
}

// resolveRestartRejoinLocked decides where a session persisted under a
// schedule rejoins after this agent restarted. The stored instants mean
// something only on the clock they were read on, so anything short of a
// locked provider naming the same clock ignores them. Caller holds s.mu.
func (m *Manager) resolveRestartRejoinLocked(ctx context.Context, s *Session, rec PersistedSession) restartRejoin {
	if !rec.ScheduleActive || rec.ScheduleItemIndex != rec.CurrentIndex || rec.ScheduleClock == nil {
		return restartRejoin{}
	}
	ignored := func(why string) restartRejoin {
		m.logf("audio session %s: restore did not rejoin at its scheduled position and starts on arrival: %s", s.id, why)
		return restartRejoin{}
	}

	source := m.clockSourceSnapshot()
	if source == nil {
		return ignored("this node has no media clock wired")
	}
	status, polledAt, ok := source.Last()
	if !ok || m.now().Sub(polledAt) > clockStatusFreshnessBound {
		status = source.Poll(ctx)
	}
	if status.State != agentclock.StateLocked {
		return ignored(fmt.Sprintf("this node's clock provider reports %q (%s)", status.State, status.Reason))
	}
	current := scheduleClockIdentityOf(status)
	if current == nil || *current != *rec.ScheduleClock {
		return ignored("this node's clock provider is not locked to the clock the schedule was read on")
	}
	mediaNow := source.Now(ctx)
	if !mediaNow.Valid {
		return ignored("this node's media clock is unreadable (" + mediaNow.Reason + ")")
	}
	if mediaNow.Time.Before(rec.ScheduleItemStartAt) {
		return ignored("this node's media clock reads earlier than the stored item start")
	}

	rejoin := restartRejoin{index: rec.CurrentIndex, itemStartAt: rec.ScheduleItemStartAt, sameItem: true, source: source, status: status}
	var cycleSkipped bool
	for {
		item, ok := playlistItemAt(s, rejoin.index)
		if !ok {
			return ignored("the stored item is no longer in this session")
		}
		duration, known := m.restartItemDuration(ctx, rec, item, rejoin.index, rejoin.sameItem)
		if !known || duration <= 0 {
			return ignored(fmt.Sprintf("the length of item %s is unknown, so the expected position cannot be computed", item.ItemID))
		}
		elapsed := mediaNow.Time.Sub(rejoin.itemStartAt)
		if elapsed < duration {
			rejoin.onSchedule = true
			rejoin.item, rejoin.duration, rejoin.position = item, duration, elapsed
			return rejoin
		}
		next, ok := nextPlaylistIndexLocked(s.desired.Playlist, rejoin.index)
		if !ok {
			return restartRejoin{ended: true}
		}
		rejoin.itemStartAt = rejoin.itemStartAt.Add(duration)
		rejoin.index = next
		rejoin.sameItem = false
		// Back at the stored item: one pass of the loop took this long,
		// so every whole pass still to come is skipped in one step.
		if next == rec.CurrentIndex && !cycleSkipped {
			cycleSkipped = true
			cycle := rejoin.itemStartAt.Sub(rec.ScheduleItemStartAt)
			passes := mediaNow.Time.Sub(rejoin.itemStartAt) / cycle
			rejoin.itemStartAt = rejoin.itemStartAt.Add(passes * cycle)
		}
	}
}

// playlistItemAt returns s's item at index: a playlist entry, or the one
// item of a session that plays a single file. Caller holds s.mu.
func playlistItemAt(s *Session, index int) (pkgaudio.PlaylistItem, bool) {
	if s.desired.Playlist == nil {
		item, ok := s.currentItemLocked()
		return item, ok && index == s.currentIndex
	}
	if index < 0 || index >= len(s.desired.Playlist.Items) {
		return pkgaudio.PlaylistItem{}, false
	}
	return s.desired.Playlist.Items[index], true
}

// restartItemDuration reports item's length for the boundary walk: the
// persisted probe for the item the record was written on, a fresh probe
// for any other, which is the same evidence a running node schedules by.
func (m *Manager) restartItemDuration(ctx context.Context, rec PersistedSession, item pkgaudio.PlaylistItem, index int, stored bool) (time.Duration, bool) {
	if stored && index == rec.CurrentIndex && rec.LastProbe.State == MediaReady && rec.LastProbe.DurationKnown {
		return rec.LastProbe.Duration, true
	}
	probe := ProbeAsset(ctx, m.assetDir, item.Media, m.decoder)
	if probe.State != MediaReady || !probe.DurationKnown {
		return 0, false
	}
	return probe.Duration, true
}

// positionAfterLoad re-reads the media clock once the item is loaded, so
// the time the load took is not played late. A reading that is unusable
// or already past this item keeps the position computed before the load.
func (r restartRejoin) positionAfterLoad(ctx context.Context) time.Duration {
	mediaNow := r.source.Now(ctx)
	if !mediaNow.Valid {
		return r.position
	}
	position := mediaNow.Time.Sub(r.itemStartAt)
	if position < r.position || position >= r.duration {
		return r.position
	}
	return position
}

// anchorRestartRejoinLocked rebuilds the item schedule, and the timeline
// when the session is still inside the item its T0 started, for a
// session just restarted at position on its schedule. Caller holds s.mu.
func (m *Manager) anchorRestartRejoinLocked(ctx context.Context, s *Session, rec PersistedSession, rejoin restartRejoin, position time.Duration) {
	s.schedule = &itemSchedule{itemIndex: rejoin.index, itemStartAt: rejoin.itemStartAt}
	s.scheduleClock = rec.ScheduleClock
	s.refreshBoundaryFromProbeLocked()
	s.discardStageLocked(ctx)
	m.logf("audio session %s: rejoined its schedule at %s into item %s after an agent restart", s.id, position, s.currentItemID)

	s.timeline = nil
	if !rec.TimelineActive || !rejoin.sameItem {
		return
	}
	t := &timeline{
		t0:               rec.TimelineT0,
		startPosition:    rec.TimelineStartPosition,
		lastStepAt:       rejoin.status.LastStepAt,
		lastStepKnown:    rejoin.status.LastStepKnown,
		engineEpoch:      m.engineEpoch.Load(),
		restartPending:   true,
		lastResyncReason: ResyncReasonAgentRestart,
	}
	if presented, ok, _ := ObserveEngineSinkClock(ctx, m.engine); ok {
		t.presentedAtStart = presented - (position - t.startPosition)
		t.presentedKnown = true
	}
	s.timeline = t
}

// completeEndedWhileDownLocked settles a session whose schedule ran past
// the end of its last item while this agent was down: nothing is loaded
// or started, and it is recorded completed. Caller holds s.mu.
func (m *Manager) completeEndedWhileDownLocked(s *Session) {
	m.logf("audio session %s: its schedule ended while this agent was down; not started", s.id)
	s.state = pkgaudio.StateCompleted
	s.bookmark = nil
	s.schedule = nil
	s.setGapUnknownLocked(gapReasonPlaylistEnded)
	s.resolveFadePendingStrandedLocked("session completed before its pending fade resolved")
	s.persistBestEffortLocked("state change")
}
