package multisync

import (
	"math"
	"sync"
	"testing"
	"time"
)

// fakeClock lets tests drive Timeline's clock deterministically, without
// real sleeps. Mirrors the fakeClock convention already used in
// internal/coordinator/broker_test.go.
type fakeClock struct {
	t time.Time
}

func newFakeClock() *fakeClock {
	return &fakeClock{t: time.Date(2026, 8, 10, 20, 0, 0, 0, time.UTC)}
}

func (c *fakeClock) now() time.Time { return c.t }

func (c *fakeClock) advance(d time.Duration) { c.t = c.t.Add(d) }

// syncPacket builds a minimal sequence sync packet for the given action.
func syncPacket(action SyncAction, filename string, frame uint32, seconds float32) SyncPacket {
	return SyncPacket{
		Action:         action,
		FileType:       SyncFileTypeSequence,
		FrameNumber:    frame,
		SecondsElapsed: seconds,
		Filename:       filename,
	}
}

func TestNewTimeline_InitialSnapshot_IsUnknownWithNoSyncEvidence(t *testing.T) {
	clock := newFakeClock()
	tl := NewTimeline(clock.now, Config{})

	snap := tl.Snapshot()

	if snap.State != StateUnknown {
		t.Errorf("State = %q, want %q", snap.State, StateUnknown)
	}
	if snap.LastSyncAtValid {
		t.Errorf("LastSyncAtValid = true before any packet, want false")
	}
	if snap.PositionMS != 0 {
		t.Errorf("PositionMS = %d before any packet, want 0", snap.PositionMS)
	}
}

func TestLifecycle_OpenStartSyncSyncStop_TransitionsThroughExpectedStates(t *testing.T) {
	clock := newFakeClock()
	tl := NewTimeline(clock.now, Config{})

	// OPEN: file identified, not yet playing, position frozen at 0.
	tl.Observe(syncPacket(SyncActionOpen, "show.fseq", 0, 0), "master-a")
	clock.advance(200 * time.Millisecond)
	snap := tl.Snapshot()
	if snap.State != StateOpened {
		t.Fatalf("after OPEN: State = %q, want %q", snap.State, StateOpened)
	}
	if snap.PositionMS != 0 {
		t.Errorf("after OPEN and 200ms elapsed: PositionMS = %d, want 0 (frozen, not yet playing)", snap.PositionMS)
	}

	// START: begins free-running from the packet's position.
	tl.Observe(syncPacket(SyncActionStart, "show.fseq", 0, 0), "master-a")
	snap = tl.Snapshot()
	if snap.State != StatePlaying {
		t.Fatalf("after START: State = %q, want %q", snap.State, StatePlaying)
	}
	if snap.PositionMS != 0 {
		t.Errorf("immediately after START: PositionMS = %d, want 0", snap.PositionMS)
	}

	// Free-run for 1s, then a SYNC that matches exactly: zero drift, within
	// the no-correction band, not a slew.
	clock.advance(1 * time.Second)
	tl.Observe(syncPacket(SyncActionSync, "show.fseq", 0, 1.0), "master-a")
	snap = tl.Snapshot()
	if snap.State != StatePlaying {
		t.Errorf("after first SYNC: State = %q, want %q", snap.State, StatePlaying)
	}
	if snap.LastCorrection != CorrectionOnTime {
		t.Errorf("after first SYNC: LastCorrection = %q, want %q", snap.LastCorrection, CorrectionOnTime)
	}
	if snap.OnTimeCount != 1 {
		t.Errorf("after first SYNC: OnTimeCount = %d, want 1", snap.OnTimeCount)
	}
	if snap.SlewCount != 0 {
		t.Errorf("after first SYNC: SlewCount = %d, want 0 (zero drift must not count as a slew)", snap.SlewCount)
	}

	// Free-run another 500ms, another matching SYNC.
	clock.advance(500 * time.Millisecond)
	tl.Observe(syncPacket(SyncActionSync, "show.fseq", 0, 1.5), "master-a")
	snap = tl.Snapshot()
	if snap.OnTimeCount != 2 {
		t.Errorf("after second SYNC: OnTimeCount = %d, want 2", snap.OnTimeCount)
	}
	if snap.PositionMS != 1500 {
		t.Errorf("after second SYNC: PositionMS = %d, want 1500", snap.PositionMS)
	}

	// STOP: holds position, does not blank immediately.
	tl.Observe(syncPacket(SyncActionStop, "show.fseq", 0, 0), "master-a")
	snap = tl.Snapshot()
	if snap.State != StateStopping {
		t.Fatalf("immediately after STOP: State = %q, want %q", snap.State, StateStopping)
	}
	if snap.PositionMS != 1500 {
		t.Errorf("immediately after STOP: PositionMS = %d, want 1500 (frozen)", snap.PositionMS)
	}

	// Past the blank delay (5 frames * 25ms default = 125ms): resolves to stopped.
	clock.advance(200 * time.Millisecond)
	snap = tl.Snapshot()
	if snap.State != StateStopped {
		t.Errorf("after blank delay elapses: State = %q, want %q", snap.State, StateStopped)
	}
	if snap.PositionMS != 1500 {
		t.Errorf("after blank delay elapses: PositionMS = %d, want 1500 (still frozen)", snap.PositionMS)
	}
}

func TestStart_WithoutPrecedingOpen_BeginsPlayingDirectly(t *testing.T) {
	clock := newFakeClock()
	tl := NewTimeline(clock.now, Config{})

	// RES-002: a robust listener must tolerate START with no preceding OPEN.
	tl.Observe(syncPacket(SyncActionStart, "a.fseq", 0, 0), "master-a")

	snap := tl.Snapshot()
	if snap.State != StatePlaying {
		t.Fatalf("State = %q, want %q", snap.State, StatePlaying)
	}
	if snap.Filename != "a.fseq" {
		t.Errorf("Filename = %q, want %q", snap.Filename, "a.fseq")
	}
}

func TestBareSync_WithNoPriorStart_ImplicitlyStartsTimeline(t *testing.T) {
	clock := newFakeClock()
	tl := NewTimeline(clock.now, Config{})

	// RES-002: a robust listener must tolerate a bare SYNC for a sequence
	// that was never started.
	tl.Observe(syncPacket(SyncActionSync, "b.fseq", 0, 2.0), "master-a")

	snap := tl.Snapshot()
	if snap.State != StatePlaying {
		t.Fatalf("State = %q, want %q", snap.State, StatePlaying)
	}
	if snap.PositionMS != 2000 {
		t.Errorf("PositionMS = %d, want 2000", snap.PositionMS)
	}
	if snap.LastCorrection != CorrectionNone {
		t.Errorf("LastCorrection = %q, want %q (an implicit start is not a correction)", snap.LastCorrection, CorrectionNone)
	}
}

func TestFreeRun_PositionAdvancesBetweenSyncPackets(t *testing.T) {
	clock := newFakeClock()
	tl := NewTimeline(clock.now, Config{})

	tl.Observe(syncPacket(SyncActionStart, "a.fseq", 0, 0), "master-a")

	clock.advance(75 * time.Millisecond)
	snap := tl.Snapshot()
	if snap.PositionMS != 75 {
		t.Errorf("after 75ms with no sync packet: PositionMS = %d, want 75 (free-running)", snap.PositionMS)
	}

	clock.advance(925 * time.Millisecond) // total 1000ms
	snap = tl.Snapshot()
	if snap.PositionMS != 1000 {
		t.Errorf("after 1000ms with no sync packet: PositionMS = %d, want 1000 (free-running)", snap.PositionMS)
	}
}

func TestSyncCorrection_WithinSlewThreshold_AppliesGradualAdjustment(t *testing.T) {
	clock := newFakeClock()
	tl := NewTimeline(clock.now, Config{})

	tl.Observe(syncPacket(SyncActionStart, "a.fseq", 0, 0), "master-a")
	clock.advance(1 * time.Second) // local free-run estimate: 1000ms

	// Master reports 1062.5ms: 62.5ms drift, within the default 4-frame
	// (100ms) slew threshold. 1.0625 is used (rather than a decimal like
	// 1.08) because it is exactly representable in float32, so the
	// assertion below is not at the mercy of SecondsElapsed's wire
	// precision.
	tl.Observe(syncPacket(SyncActionSync, "a.fseq", 0, 1.0625), "master-a")

	snap := tl.Snapshot()
	if snap.LastCorrection != CorrectionSlew {
		t.Fatalf("LastCorrection = %q, want %q", snap.LastCorrection, CorrectionSlew)
	}
	if snap.SlewCount != 1 {
		t.Errorf("SlewCount = %d, want 1", snap.SlewCount)
	}
	// DefaultSlewFractionPerSync (0.5) closes only half the 62.5ms gap: 1000 + 31.25 = 1031.25, truncated to 1031ms.
	if snap.PositionMS != 1031 {
		t.Errorf("PositionMS = %d, want 1031 (gradual, not snapped to master's 1062.5)", snap.PositionMS)
	}
}

func TestSyncCorrection_ModeratelyBehind_Skips(t *testing.T) {
	clock := newFakeClock()
	tl := NewTimeline(clock.now, Config{})

	tl.Observe(syncPacket(SyncActionStart, "a.fseq", 0, 0), "master-a")
	clock.advance(1 * time.Second) // local free-run estimate: 1000ms

	// Master reports 1250ms: 250ms drift (a binary-exact float32 value, so
	// the assertion below is not at the mercy of SecondsElapsed's wire
	// precision), above the 100ms slew threshold but at or below the
	// 500ms jump threshold.
	tl.Observe(syncPacket(SyncActionSync, "a.fseq", 0, 1.25), "master-a")

	snap := tl.Snapshot()
	if snap.LastCorrection != CorrectionSkip {
		t.Fatalf("LastCorrection = %q, want %q", snap.LastCorrection, CorrectionSkip)
	}
	if snap.SkipCount != 1 {
		t.Errorf("SkipCount = %d, want 1", snap.SkipCount)
	}
	if snap.PositionMS != 1250 {
		t.Errorf("PositionMS = %d, want 1250 (skip applies immediately)", snap.PositionMS)
	}
}

func TestSyncCorrection_MoreThanHalfSecondBehind_JumpsDirectly(t *testing.T) {
	clock := newFakeClock()
	tl := NewTimeline(clock.now, Config{})

	tl.Observe(syncPacket(SyncActionStart, "a.fseq", 0, 0), "master-a")
	clock.advance(1 * time.Second) // local free-run estimate: 1000ms

	// Master reports 1750ms: 750ms drift (a binary-exact float32 value),
	// above the default 500ms jump threshold.
	tl.Observe(syncPacket(SyncActionSync, "a.fseq", 0, 1.75), "master-a")

	snap := tl.Snapshot()
	if snap.LastCorrection != CorrectionJump {
		t.Fatalf("LastCorrection = %q, want %q", snap.LastCorrection, CorrectionJump)
	}
	if snap.JumpCount != 1 {
		t.Errorf("JumpCount = %d, want 1", snap.JumpCount)
	}
	if snap.PositionMS != 1750 {
		t.Errorf("PositionMS = %d, want 1750 (jump applies immediately)", snap.PositionMS)
	}
}

func TestSilenceWindow_TransitionsToUnsynchronized_WhilePositionKeepsAdvancing(t *testing.T) {
	clock := newFakeClock()
	tl := NewTimeline(clock.now, Config{})

	tl.Observe(syncPacket(SyncActionStart, "a.fseq", 0, 0), "master-a")

	// Exactly at the default 5s silence interval: unsynchronized.
	clock.advance(5 * time.Second)
	snap := tl.Snapshot()
	if snap.State != StateUnsynchronized {
		t.Fatalf("at silence interval: State = %q, want %q", snap.State, StateUnsynchronized)
	}
	if snap.PositionMS != 5000 {
		t.Errorf("at silence interval: PositionMS = %d, want 5000", snap.PositionMS)
	}

	// RES-002: silence is never a teardown trigger. Position must keep
	// advancing on its own clock past the silence window.
	clock.advance(2 * time.Second)
	snap = tl.Snapshot()
	if snap.State != StateUnsynchronized {
		t.Errorf("past silence interval: State = %q, want %q", snap.State, StateUnsynchronized)
	}
	if snap.PositionMS != 7000 {
		t.Errorf("past silence interval: PositionMS = %d, want 7000 (still free-running)", snap.PositionMS)
	}
}

func TestSyncResumingAfterSilence_RecoversToPlaying(t *testing.T) {
	clock := newFakeClock()
	tl := NewTimeline(clock.now, Config{})

	tl.Observe(syncPacket(SyncActionStart, "a.fseq", 0, 0), "master-a")
	clock.advance(6 * time.Second) // past the default 5s silence interval

	if snap := tl.Snapshot(); snap.State != StateUnsynchronized {
		t.Fatalf("precondition: State = %q, want %q", snap.State, StateUnsynchronized)
	}

	// A resumed SYNC matching the free-run estimate should recover to Playing.
	tl.Observe(syncPacket(SyncActionSync, "a.fseq", 0, 6.0), "master-a")

	snap := tl.Snapshot()
	if snap.State != StatePlaying {
		t.Errorf("after resumed SYNC: State = %q, want %q", snap.State, StatePlaying)
	}
}

func TestStop_ThenBlankDelayElapses_ResolvesToStopped(t *testing.T) {
	clock := newFakeClock()
	tl := NewTimeline(clock.now, Config{})

	tl.Observe(syncPacket(SyncActionStart, "a.fseq", 0, 0), "master-a")
	clock.advance(500 * time.Millisecond)
	tl.Observe(syncPacket(SyncActionStop, "a.fseq", 0, 0), "master-a")

	snap := tl.Snapshot()
	if snap.State != StateStopping {
		t.Fatalf("immediately after STOP: State = %q, want %q", snap.State, StateStopping)
	}

	// Still within the default 125ms (5 frames * 25ms) blank delay.
	clock.advance(50 * time.Millisecond)
	snap = tl.Snapshot()
	if snap.State != StateStopping {
		t.Errorf("within blank delay: State = %q, want %q", snap.State, StateStopping)
	}
	if snap.PositionMS != 500 {
		t.Errorf("within blank delay: PositionMS = %d, want 500 (frozen)", snap.PositionMS)
	}

	// Past the blank delay.
	clock.advance(100 * time.Millisecond)
	snap = tl.Snapshot()
	if snap.State != StateStopped {
		t.Errorf("past blank delay: State = %q, want %q", snap.State, StateStopped)
	}
	if snap.PositionMS != 500 {
		t.Errorf("past blank delay: PositionMS = %d, want 500 (still frozen)", snap.PositionMS)
	}
}

func TestStop_ThenStartDuringBlankDelay_CancelsBlank(t *testing.T) {
	clock := newFakeClock()
	tl := NewTimeline(clock.now, Config{})

	tl.Observe(syncPacket(SyncActionStart, "a.fseq", 0, 0), "master-a")
	clock.advance(500 * time.Millisecond)
	tl.Observe(syncPacket(SyncActionStop, "a.fseq", 0, 0), "master-a")

	// Still within the blank delay when the next START arrives. 0.75 is
	// used (rather than a decimal like 0.8) because it is exactly
	// representable in float32.
	clock.advance(50 * time.Millisecond)
	tl.Observe(syncPacket(SyncActionStart, "a.fseq", 0, 0.75), "master-a")

	snap := tl.Snapshot()
	if snap.State != StatePlaying {
		t.Fatalf("State = %q, want %q (START during blank delay must cancel it)", snap.State, StatePlaying)
	}
	if snap.PositionMS != 750 {
		t.Errorf("PositionMS = %d, want 750 (re-anchored from the new START)", snap.PositionMS)
	}
}

func TestSecondsElapsedZero_FallsBackToFrameNumber(t *testing.T) {
	clock := newFakeClock()
	tl := NewTimeline(clock.now, Config{}) // default step time 25ms

	tl.Observe(syncPacket(SyncActionOpen, "c.fseq", 80, 0), "master-a")

	snap := tl.Snapshot()
	if snap.PositionMS != 2000 {
		t.Errorf("PositionMS = %d, want 2000 (80 frames * 25ms default step time)", snap.PositionMS)
	}
}

func TestStepTimeChange_AffectsFrameNumberDerivedPositionForNewFile(t *testing.T) {
	clock := newFakeClock()
	tl := NewTimeline(clock.now, Config{}) // default step time 25ms

	tl.Observe(syncPacket(SyncActionOpen, "file1.fseq", 40, 0), "master-a")
	snap := tl.Snapshot()
	if snap.PositionMS != 1000 {
		t.Fatalf("file1 PositionMS = %d, want 1000 (40 * 25ms)", snap.PositionMS)
	}

	// A new file starts with a different step time; the caller must update
	// it (RES-002: rate is not carried on the wire).
	tl.SetStepTime(50 * time.Millisecond)
	tl.Observe(syncPacket(SyncActionOpen, "file2.fseq", 40, 0), "master-a")

	snap = tl.Snapshot()
	if snap.PositionMS != 2000 {
		t.Errorf("file2 PositionMS = %d, want 2000 (40 * 50ms)", snap.PositionMS)
	}
	if snap.Filename != "file2.fseq" {
		t.Errorf("Filename = %q, want %q", snap.Filename, "file2.fseq")
	}
}

func TestCompetingMaster_SameFileDifferentSource_IsFlaggedWithoutArbitration(t *testing.T) {
	clock := newFakeClock()
	tl := NewTimeline(clock.now, Config{})

	tl.Observe(syncPacket(SyncActionStart, "show.fseq", 0, 0), "master-a")
	snap := tl.Snapshot()
	if snap.Source != "master-a" {
		t.Fatalf("Source = %q, want %q", snap.Source, "master-a")
	}
	if snap.Conflict {
		t.Fatalf("Conflict = true before any competing packet, want false")
	}

	clock.advance(200 * time.Millisecond)

	// A different source sends sync data for the same file: this must be
	// flagged, not arbitrated over. The conflicting packet's position data
	// must not be applied.
	tl.Observe(syncPacket(SyncActionSync, "show.fseq", 0, 9.0), "master-b")

	snap = tl.Snapshot()
	if !snap.Conflict {
		t.Fatalf("Conflict = false after a same-file packet from a different source, want true")
	}
	if snap.ConflictCount != 1 {
		t.Errorf("ConflictCount = %d, want 1", snap.ConflictCount)
	}
	if snap.Source != "master-a" {
		t.Errorf("Source = %q, want %q (Timeline must not switch to the competing source)", snap.Source, "master-a")
	}
	if snap.PositionMS != 200 {
		t.Errorf("PositionMS = %d, want 200 (master-b's packet must not be applied)", snap.PositionMS)
	}

	// A second conflicting packet increments the count.
	tl.Observe(syncPacket(SyncActionSync, "show.fseq", 0, 9.0), "master-b")
	snap = tl.Snapshot()
	if snap.ConflictCount != 2 {
		t.Errorf("ConflictCount = %d, want 2", snap.ConflictCount)
	}

	// A subsequent packet from the original driving source is not itself a
	// conflict, but the Conflict flag stays set (sticky for this file's
	// session): the fact that a competing master appeared is evidence
	// worth keeping visible, not something a single clean packet erases.
	tl.Observe(syncPacket(SyncActionSync, "show.fseq", 0, 0.2), "master-a")
	snap = tl.Snapshot()
	if !snap.Conflict {
		t.Errorf("Conflict = false after a clean packet from the original source, want true (sticky for this file)")
	}
	if snap.ConflictCount != 2 {
		t.Errorf("ConflictCount = %d, want unchanged at 2", snap.ConflictCount)
	}
}

func TestSnapshotObservedAt_AdvancesIndependentlyFromLastSyncAt(t *testing.T) {
	clock := newFakeClock()
	tl := NewTimeline(clock.now, Config{})

	startTime := clock.now()
	tl.Observe(syncPacket(SyncActionStart, "a.fseq", 0, 0), "master-a")

	snap := tl.Snapshot()
	if !snap.LastSyncAt.Equal(startTime) {
		t.Fatalf("LastSyncAt = %v, want %v", snap.LastSyncAt, startTime)
	}
	if !snap.ObservedAt.Equal(startTime) {
		t.Fatalf("ObservedAt = %v, want %v", snap.ObservedAt, startTime)
	}

	// No new packet arrives, but time passes: ObservedAt must still move
	// so a caller can compute staleness (ADR-011), while LastSyncAt stays
	// pinned to the last real evidence.
	clock.advance(300 * time.Millisecond)
	snap = tl.Snapshot()
	if !snap.LastSyncAt.Equal(startTime) {
		t.Errorf("LastSyncAt = %v after silent 300ms, want unchanged %v", snap.LastSyncAt, startTime)
	}
	if !snap.ObservedAt.Equal(clock.now()) {
		t.Errorf("ObservedAt = %v, want %v", snap.ObservedAt, clock.now())
	}
	if snap.ObservedAt.Equal(startTime) {
		t.Errorf("ObservedAt did not advance despite the clock moving forward")
	}
}

// --- BLOCKER 2: competing-master detection must not wedge permanently ---

func TestObserve_SourceIdentityContract_SameMasterAcrossPorts_NoConflictWhenCallerKeysOnIP(t *testing.T) {
	clock := newFakeClock()
	tl := NewTimeline(clock.now, Config{})

	// Simulates one FPP master whose packets legitimately arrive from
	// different source ports because SendControlPacket fans out over
	// independent unbound sockets (unicast, broadcast, one per interface
	// for multicast); RES-002 records this, and an fppd restart changes
	// all of them. Observe's doc comment requires the caller to key on the
	// stable identity (the source IP), never "ip:port" - done correctly
	// here, so the different underlying ports never show up in source at
	// all.
	const masterIP = "192.168.1.10"

	tl.Observe(syncPacket(SyncActionStart, "show.fseq", 0, 0), masterIP)
	clock.advance(100 * time.Millisecond)
	tl.Observe(syncPacket(SyncActionSync, "show.fseq", 0, 0.1), masterIP)

	snap := tl.Snapshot()
	if snap.Conflict {
		t.Errorf("Conflict = true for the same master's IP observed twice, want false")
	}
	if snap.ConflictCount != 0 {
		t.Errorf("ConflictCount = %d, want 0", snap.ConflictCount)
	}
}

func TestObserve_GenuinelyDifferentSourceIP_IsAConflict(t *testing.T) {
	clock := newFakeClock()
	tl := NewTimeline(clock.now, Config{})

	tl.Observe(syncPacket(SyncActionStart, "show.fseq", 0, 0), "192.168.1.10")
	clock.advance(100 * time.Millisecond)
	tl.Observe(syncPacket(SyncActionSync, "show.fseq", 0, 0.1), "192.168.1.99")

	snap := tl.Snapshot()
	if !snap.Conflict {
		t.Errorf("Conflict = false for a packet from a genuinely different source IP, want true")
	}
	if snap.ConflictCount != 1 {
		t.Errorf("ConflictCount = %d, want 1", snap.ConflictCount)
	}
}

func TestCompetingMaster_NewSourceCanTakeOver_AfterTimelineReachesStopped(t *testing.T) {
	clock := newFakeClock()
	tl := NewTimeline(clock.now, Config{})

	tl.Observe(syncPacket(SyncActionStart, "show.fseq", 0, 0), "192.168.1.10")
	clock.advance(500 * time.Millisecond)
	tl.Observe(syncPacket(SyncActionStop, "show.fseq", 0, 0), "192.168.1.10")

	// Past the default blank delay (5 frames * 25ms = 125ms): Stopped.
	clock.advance(200 * time.Millisecond)
	if snap := tl.Snapshot(); snap.State != StateStopped {
		t.Fatalf("precondition: State = %q, want %q", snap.State, StateStopped)
	}

	// A genuinely new master (e.g. fppd restarted under new source ports,
	// or a different box entirely) starts the same file. Before BLOCKER
	// 2(a)'s fix, isConflictingLocked would have locked this out forever
	// since the filename is unchanged: the review reproduced 40
	// consecutive dropped SYNC packets and a dropped STOP, recovering only
	// on a filename change.
	tl.Observe(syncPacket(SyncActionStart, "show.fseq", 0, 0), "192.168.1.20")

	snap := tl.Snapshot()
	if snap.Conflict {
		t.Errorf("Conflict = true for a new source taking over after Stopped, want false")
	}
	if snap.State != StatePlaying {
		t.Errorf("State = %q, want %q (new source's START must apply)", snap.State, StatePlaying)
	}
	if snap.Source != "192.168.1.20" {
		t.Errorf("Source = %q, want %q (timeline must switch to the new source)", snap.Source, "192.168.1.20")
	}
}

func TestIsConflicting_ReleasesClaim_AfterOpenedAgesOutToUnknown(t *testing.T) {
	clock := newFakeClock()
	tl := NewTimeline(clock.now, Config{})

	tl.Observe(syncPacket(SyncActionOpen, "show.fseq", 0, 0), "192.168.1.10")
	clock.advance(5 * time.Second) // default SilenceInterval: ages Opened -> Unknown

	if snap := tl.Snapshot(); snap.State != StateUnknown {
		t.Fatalf("precondition: State = %q, want %q", snap.State, StateUnknown)
	}

	tl.Observe(syncPacket(SyncActionOpen, "show.fseq", 0, 0), "192.168.1.20")

	snap := tl.Snapshot()
	if snap.Conflict {
		t.Errorf("Conflict = true for a new source after the previous OPEN aged out to Unknown, want false")
	}
	if snap.Source != "192.168.1.20" {
		t.Errorf("Source = %q, want %q", snap.Source, "192.168.1.20")
	}
}

// --- Smaller item: StateOpened must age out, not persist forever ---

func TestStateOpened_AgesOutToUnknownAfterSilenceInterval(t *testing.T) {
	clock := newFakeClock()
	tl := NewTimeline(clock.now, Config{})

	tl.Observe(syncPacket(SyncActionOpen, "a.fseq", 0, 0), "master-a")
	if snap := tl.Snapshot(); snap.State != StateOpened {
		t.Fatalf("precondition: State = %q, want %q", snap.State, StateOpened)
	}

	// Default SilenceInterval is 5s; a master that OPENed and then died (no
	// START, no further packets) must not report `opened` forever
	// (ADR-011: a state must not read as a confident positive indefinitely).
	clock.advance(5 * time.Second)
	snap := tl.Snapshot()
	if snap.State != StateUnknown {
		t.Errorf("after silence interval with no START: State = %q, want %q", snap.State, StateUnknown)
	}
}

// --- Smaller item: StateSince tracks the most recent transition only ---

func TestSnapshotStateSince_TracksMostRecentStateTransitionOnly(t *testing.T) {
	clock := newFakeClock()
	tl := NewTimeline(clock.now, Config{})

	openAt := clock.now()
	tl.Observe(syncPacket(SyncActionOpen, "a.fseq", 0, 0), "master-a")
	snap := tl.Snapshot()
	if !snap.StateSince.Equal(openAt) {
		t.Fatalf("StateSince = %v, want %v (OPEN just transitioned Unknown -> Opened)", snap.StateSince, openAt)
	}

	clock.advance(2 * time.Second)
	snap = tl.Snapshot() // no new packet: state unchanged (still Opened)
	if !snap.StateSince.Equal(openAt) {
		t.Errorf("StateSince = %v after re-observing the same state, want unchanged %v", snap.StateSince, openAt)
	}

	startAt := clock.now()
	tl.Observe(syncPacket(SyncActionStart, "a.fseq", 0, 0), "master-a")
	snap = tl.Snapshot()
	if !snap.StateSince.Equal(startAt) {
		t.Errorf("StateSince = %v after START, want %v (Opened -> Playing is a real transition)", snap.StateSince, startAt)
	}
}

// --- BLOCKER 3: a malformed SecondsElapsed must not poison PositionMS ---

func TestPositionFromPacket_DefendsAgainstNonFiniteSecondsElapsed(t *testing.T) {
	// DecodeSync now rejects a non-finite or out-of-range SecondsElapsed at
	// the wire boundary (see packet_test.go), but Timeline is driven
	// through the public Observe method, which any caller can call
	// directly with a SyncPacket that did not come through DecodeSync.
	// This exercises Timeline's own defense in depth.
	tests := []struct {
		name string
		se   float32
	}{
		{"+Inf", float32(math.Inf(1))},
		{"-Inf", float32(math.Inf(-1))},
		{"NaN", float32(math.NaN())},
		{"far too large", 1e30},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			clock := newFakeClock()
			tl := NewTimeline(clock.now, Config{}) // default step time 25ms

			pkt := SyncPacket{Action: SyncActionOpen, FileType: SyncFileTypeSequence, FrameNumber: 40, SecondsElapsed: tt.se, Filename: "c.fseq"}
			tl.Observe(pkt, "master-a")

			snap := tl.Snapshot()
			if snap.PositionMS != 1000 { // 40 frames * 25ms default step time
				t.Errorf("PositionMS = %d, want 1000 (fallback to FrameNumber when SecondsElapsed is not sane)", snap.PositionMS)
			}
		})
	}
}

// --- SHOULD FIX 8: Observe and Snapshot must be safe under concurrent use ---

func TestConcurrentObserveAndSnapshot_NoDataRace(t *testing.T) {
	tl := NewTimeline(time.Now, Config{})

	var wg sync.WaitGroup
	const iterations = 500

	wg.Add(2)
	go func() {
		defer wg.Done()
		for i := 0; i < iterations; i++ {
			tl.Observe(syncPacket(SyncActionSync, "race.fseq", uint32(i), float32(i)*0.01), "master-a")
		}
	}()
	go func() {
		defer wg.Done()
		for i := 0; i < iterations; i++ {
			_ = tl.Snapshot()
		}
	}()
	wg.Wait()
}
